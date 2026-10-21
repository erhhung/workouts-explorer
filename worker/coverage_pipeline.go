package worker

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"syscall"
	"time"

	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/erhhung/workouts-explorer/internal/healthautoexport"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

type coverageHandoff struct {
	WorkoutID          uuid.UUID              `json:"workoutId"`
	EventID            uuid.UUID              `json:"eventId"`
	Origin             string                 `json:"origin"`
	ContentSHA256      string                 `json:"contentSha256"`
	RouteDigest        string                 `json:"routeDigest"`
	OwnershipEpoch     int64                  `json:"ownershipEpoch"`
	Readiness          string                 `json:"readiness"`
	Reason             any                    `json:"reason"`
	Generations        []osm.RegionGeneration `json:"generations"`
	CatalogFingerprint string                 `json:"catalogFingerprint"`
}

type coverageReplayOptions struct {
	minimumTraversal      float64
	autoAdd, replicasOnly bool
	renew                 func(context.Context) error
	requestTimeout        time.Duration
}

type coverageReadinessFailure struct{ cause error }

func (failure *coverageReadinessFailure) Error() string { return failure.cause.Error() }
func (failure *coverageReadinessFailure) Unwrap() error { return failure.cause }

func (r *Runner) drainIngestCoverage(ctx context.Context, job claimedJob, flush bool) error {
	if r.osmDB == nil {
		return nil
	}
	size := r.coverageBatchSize
	if size < 1 || size > 1000 {
		size = 25
	}
	return replayIngestCoverage(ctx, r.db, r.osmDB, job.accountID, job.id, r.workerID, job.lease, nil, size, flush,
		coverageReplayOptions{minimumTraversal: r.coverageMinTraversalMeters, autoAdd: r.osmAutoAddRegions,
			requestTimeout: r.leaseDuration / 2, renew: func(ctx context.Context) error {
				alive, err := r.heartbeat(ctx, job)
				if err != nil {
					return err
				}
				if !alive {
					return errLeaseLost
				}
				cancelled, err := r.observeIngest(ctx, job)
				if err != nil {
					return err
				}
				if cancelled {
					return errCancellationRequested
				}
				return nil
			}})
}

// Only durable rows cross file/lease boundaries. Every OSM lookup happens after
// releasing the authorized APP snapshot; apply rechecks authority and both hashes.
func replayIngestCoverage(ctx context.Context, db, osmDB *pgxpool.Pool, accountID, jobID uuid.UUID, workerID string,
	lease uuid.UUID, revision *int64, size int, flush bool, options coverageReplayOptions) error {
	var authorityJob any
	if jobID != uuid.Nil {
		authorityJob = jobID
	}
	retries := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if options.renew != nil {
			if err := options.renew(ctx); err != nil {
				return err
			}
		}
		if err := drainPreparedIngestCoverage(ctx, db, osmDB, accountID, authorityJob, workerID, lease, revision, options); err != nil {
			if retryErr := waitCoverageHandoffRetry(ctx, err, retries); retryErr != nil {
				return coverageHandoffError(retryErr)
			}
			retries++
			continue
		}
		items, err := func() ([]coverageHandoff, error) {
			ctx := ctx
			if options.requestTimeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, options.requestTimeout)
				defer cancel()
			}
			tx, err := beginAccount(ctx, db, accountID)
			if err != nil {
				return nil, err
			}
			defer tx.Rollback(ctx)
			rows, err := tx.Query(ctx, `SELECT workout_id,event_id,origin,content_sha256,ownership_epoch
				FROM app.read_ingest_coverage_batch($1,$2,$3,$4,$5,NULL,$6,$7)`,
				accountID, authorityJob, workerID, lease, revision, size, flush)
			if err != nil {
				return nil, err
			}
			var items []coverageHandoff
			for rows.Next() {
				var item coverageHandoff
				var content []byte
				if err := rows.Scan(&item.WorkoutID, &item.EventID, &item.Origin, &content, &item.OwnershipEpoch); err != nil {
					rows.Close()
					return nil, err
				}
				if len(content) != 32 || item.OwnershipEpoch < 0 ||
					(item.Origin != "normal" && item.Origin != "adopted") {
					rows.Close()
					return nil, errors.New("invalid coverage handoff snapshot")
				}
				item.ContentSHA256 = hex.EncodeToString(content)
				items = append(items, item)
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				return nil, err
			}
			return items, tx.Commit(ctx)
		}()
		if err != nil {
			if retryErr := waitCoverageHandoffRetry(ctx, err, retries); retryErr != nil {
				return coverageHandoffError(retryErr)
			}
			retries++
			continue
		}
		if len(items) == 0 {
			return nil
		}
		retryBatch := false
		for i := range items {
			if options.renew != nil {
				if err := options.renew(ctx); err != nil {
					return err
				}
			}
			var minimumEventID int64
			if options.replicasOnly {
				observationCtx, cancel := context.WithCancel(ctx)
				if options.requestTimeout > 0 {
					cancel()
					observationCtx, cancel = context.WithTimeout(ctx, options.requestTimeout)
				}
				err := db.QueryRow(observationCtx, `SELECT last_osm_event_id FROM app.coverage_reconciliation_observation()`).Scan(&minimumEventID)
				cancel()
				if err != nil {
					return fmt.Errorf("read coverage reconciliation observation: %w", err)
				}
			}
			if err := func() error {
				ctx := ctx
				if options.requestTimeout > 0 {
					var cancel context.CancelFunc
					ctx, cancel = context.WithTimeout(ctx, options.requestTimeout)
					defer cancel()
				}
				coordinates, err := readCoverageHandoffCoordinates(ctx, db, accountID, authorityJob, workerID, lease, revision, &items[i])
				if err != nil {
					return coverageHandoffError(err)
				}
				readiness, err := osm.ResolveRouteRegionsWithCatalog(ctx, osmDB, coordinates, minimumEventID)
				if err != nil {
					return &coverageReadinessFailure{cause: database.RedactOSMConnectionError(err)}
				}
				readiness = routeReadinessForAutoAddPolicy(readiness, options.autoAdd)
				items[i].Readiness, items[i].Reason, items[i].Generations = readiness.State, nullableReadinessReason(readiness.Reason), readiness.Regions
				items[i].CatalogFingerprint = readiness.CatalogFingerprint
				if options.renew != nil {
					if err := options.renew(ctx); err != nil {
						return err
					}
				}
				// Persist this item before loading another route. Prepared metadata
				// survives file boundaries, leases, and failures in later page items.
				return prepareCoverageHandoff(ctx, db, accountID, authorityJob, workerID, lease, revision, items[i], options)
			}(); err != nil {
				var readinessErr *coverageReadinessFailure
				if errors.As(err, &readinessErr) {
					if ctx.Err() != nil || !transientCoverageReadinessError(readinessErr.cause) {
						return readinessErr.cause
					}
					// Only this item's digest has been snapshotted. Never invent CAS
					// hashes for the rest of the metadata-only page when deferring.
					err = deferCoverageHandoffs(ctx, db, accountID, authorityJob, workerID, lease, revision, items[i:i+1], options)
					if err == nil {
						continue
					}
				}
				if retryErr := waitCoverageHandoffRetry(ctx, err, retries); retryErr != nil {
					return coverageHandoffError(retryErr)
				}
				retries++
				retryBatch = true
				break
			}
			retries = 0
			// The saved capacity can be smaller than this metadata page, or
			// earlier pages/files may already have filled most of the builder.
			if err := drainPreparedIngestCoverage(ctx, db, osmDB, accountID, authorityJob, workerID, lease, revision, options); err != nil {
				if retryErr := waitCoverageHandoffRetry(ctx, err, retries); retryErr != nil {
					return coverageHandoffError(retryErr)
				}
				retries++
				retryBatch = true
				break
			}
		}
		if retryBatch {
			continue
		}
		retries = 0
	}
}

func prepareCoverageHandoff(ctx context.Context, db *pgxpool.Pool, accountID uuid.UUID, authorityJob any,
	workerID string, lease uuid.UUID, revision *int64, item coverageHandoff, options coverageReplayOptions) error {
	encoded, err := json.Marshal([]coverageHandoff{item})
	if err != nil {
		return err
	}
	tx, err := beginAccount(ctx, db, accountID)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var outcome string
	if err := tx.QueryRow(ctx, `SELECT outcome FROM app.apply_ingest_coverage_batch($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		accountID, authorityJob, workerID, lease, revision, encoded, string(coverage.ExperimentalRulesV1),
		string(coverage.ExperimentalSamplingV1), string(coverage.ExperimentalPathPolicyV82), options.minimumTraversal,
		database.SupportedSchemaVersion).Scan(&outcome); err != nil {
		return err
	}
	switch outcome {
	case "prepared", "existing", "pending", "unavailable", "deferred":
	default:
		return errors.New("invalid coverage handoff outcome")
	}
	return tx.Commit(ctx)
}

type coveragePreparedRefresh struct {
	WorkoutID          uuid.UUID              `json:"workoutId"`
	EventID            uuid.UUID              `json:"eventId"`
	Generations        []osm.RegionGeneration `json:"generations"`
	CatalogFingerprint string                 `json:"catalogFingerprint"`
}

// SQL returns actionable builders only. A held result ends this drain, rather
// than spinning on an underfull or concurrently changed durable builder.
func drainPreparedIngestCoverage(ctx context.Context, db, osmDB *pgxpool.Pool, accountID uuid.UUID,
	authorityJob any, workerID string, lease uuid.UUID, revision *int64, options coverageReplayOptions) error {
	for {
		worked, err := func() (bool, error) {
			ctx := ctx
			if options.requestTimeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, options.requestTimeout)
				defer cancel()
			}
			if err := ctx.Err(); err != nil {
				return false, err
			}
			if options.renew != nil {
				if err := options.renew(ctx); err != nil {
					return false, err
				}
			}
			var builderID, rootID uuid.UUID
			var capacity int
			var encoded []byte
			err := func() error {
				tx, err := beginAccount(ctx, db, accountID)
				if err != nil {
					return err
				}
				defer tx.Rollback(ctx)
				if err := tx.QueryRow(ctx, `SELECT builder_id,root_job_id,batch_size,targets
					FROM app.read_ingest_coverage_prepared($1,$2,$3,$4,$5,1)`,
					accountID, authorityJob, workerID, lease, revision).
					Scan(&builderID, &rootID, &capacity, &encoded); err != nil {
					return err
				}
				return tx.Commit(ctx)
			}()
			if errors.Is(err, pgx.ErrNoRows) {
				return false, nil
			}
			if err != nil {
				return false, err
			}
			var targets []coveragePreparedRefresh
			if err := json.Unmarshal(encoded, &targets); err != nil {
				return false, fmt.Errorf("decode prepared coverage metadata: %w", err)
			}
			if builderID == uuid.Nil || rootID == uuid.Nil || capacity < 1 || capacity > 1000 || len(targets) == 0 || len(targets) > capacity {
				return false, errors.New("invalid prepared coverage cohort")
			}
			var minimumEventID int64
			if options.replicasOnly {
				if err := db.QueryRow(ctx, `SELECT last_osm_event_id FROM app.coverage_reconciliation_observation()`).Scan(&minimumEventID); err != nil {
					return false, fmt.Errorf("read coverage reconciliation observation: %w", err)
				}
			}
			// There is no APP checkout during catalog validation. This proves an
			// observed OSM snapshot, not cross-database latest-generation atomicity.
			if err := refreshPreparedCoverageVectors(ctx, osmDB, targets, minimumEventID, options.replicasOnly); err != nil {
				return false, database.RedactOSMConnectionError(err)
			}
			if options.renew != nil {
				if err := options.renew(ctx); err != nil {
					return false, err
				}
			}
			encoded, err = json.Marshal(targets)
			if err != nil {
				return false, err
			}
			tx, err := beginAccount(ctx, db, accountID)
			if err != nil {
				return false, err
			}
			defer tx.Rollback(ctx)
			var outcome string
			if err := tx.QueryRow(ctx, `SELECT outcome FROM app.seal_ingest_coverage_cohort($1,$2,$3,$4,$5,$6,$7,$8)`,
				accountID, authorityJob, workerID, lease, revision, builderID, encoded, database.SupportedSchemaVersion).
				Scan(&outcome); err != nil {
				return false, err
			}
			switch outcome {
			case "sealed", "invalidated", "empty", "held":
			default:
				return false, errors.New("invalid coverage cohort outcome")
			}
			if err := tx.Commit(ctx); err != nil {
				return false, err
			}
			return outcome != "held", nil
		}()
		if err != nil || !worked {
			return err
		}
	}
}

func refreshPreparedCoverageVectors(ctx context.Context, db *pgxpool.Pool, targets []coveragePreparedRefresh, minimumEventID int64, replicasOnly bool) error {
	catalog, err := osm.ReadRouteRegionCatalog(ctx, db, minimumEventID)
	if err != nil {
		return err
	}
	for i := range targets {
		current := len(targets[i].Generations) > 0 && targets[i].CatalogFingerprint == catalog.Fingerprint
		for _, region := range targets[i].Generations {
			if replicasOnly && catalog.ActiveGenerations[region.RegionID] < region.Generation {
				return osm.ErrMatcherReplicaNotReady
			}
			current = current && catalog.ActiveGenerations[region.RegionID] == region.Generation
		}
		if !current {
			// Never substitute a new head for an old ROI result. Invalidation
			// makes SQL expose the obligation for a fresh full-route lookup.
			targets[i].Generations = []osm.RegionGeneration{}
		}
		targets[i].CatalogFingerprint = catalog.Fingerprint
	}
	return nil
}

func waitCoverageHandoffRetry(ctx context.Context, cause error, retries int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Three full attempts, including the initial OSM lookup. Do not turn an
	// exhausted or mixed unknown/authority failure into a new physical-work retry.
	if retries < 0 || retries >= 2 {
		return cause
	}
	pending := []error{cause}
	found := false
	for visited := 0; len(pending) > 0; visited++ {
		if visited >= 256 {
			return cause
		}
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if current == nil {
			return cause
		}
		value := reflect.ValueOf(current)
		if value.Kind() == reflect.Pointer && value.IsNil() {
			return cause
		}
		if sql, ok := current.(*pgconn.PgError); ok {
			if sql == nil || (sql.Code != "40001" && sql.Code != "55P03" && sql.Code != "40P01") {
				return cause
			}
			found = true
			continue
		}
		switch wrapped := current.(type) {
		case interface{ Unwrap() []error }:
			children := wrapped.Unwrap()
			if len(children) == 0 || len(children)+len(pending) > 256 {
				return cause
			}
			pending = append(pending, children...)
		case interface{ Unwrap() error }:
			pending = append(pending, wrapped.Unwrap())
		default:
			return cause
		}
	}
	if !found {
		return cause
	}
	timer := time.NewTimer(min(25*time.Millisecond<<retries, 100*time.Millisecond))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func readCoverageHandoffCoordinates(ctx context.Context, db *pgxpool.Pool, accountID uuid.UUID, authorityJob any,
	workerID string, lease uuid.UUID, revision *int64, item *coverageHandoff) ([]osm.RouteCoordinate, error) {
	tx, err := beginAccount(ctx, db, accountID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var content, digest, encoded []byte
	var epoch int64
	var count int
	if err := tx.QueryRow(ctx, `SELECT content_sha256,ownership_epoch,route_input_sha256,point_count,coordinates
		FROM app.read_ingest_coverage_route_snapshot($1,$2,$3,$4,$5,$6,$7,$8)`,
		accountID, authorityJob, workerID, lease, revision, item.WorkoutID, item.EventID, item.Origin).
		Scan(&content, &epoch, &digest, &count, &encoded); err != nil {
		return nil, err
	}
	if hex.EncodeToString(content) != item.ContentSHA256 || epoch != item.OwnershipEpoch {
		return nil, &pgconn.PgError{Code: "40001", Message: "coverage handoff metadata changed"}
	}
	if len(digest) != 32 || count < 0 || count > healthautoexport.DefaultLimits().MaxRoutePoints {
		return nil, errors.New("invalid coverage route snapshot")
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	item.RouteDigest = hex.EncodeToString(digest)
	return decodeCoverageCoordinates(encoded, count)
}

func decodeCoverageCoordinates(encoded []byte, count int) ([]osm.RouteCoordinate, error) {
	if count < 0 || count > healthautoexport.DefaultLimits().MaxRoutePoints {
		return nil, errors.New("invalid raw coverage point count")
	}
	var pairs [][2]float64
	if err := json.Unmarshal(encoded, &pairs); err != nil {
		return nil, fmt.Errorf("decode coverage readiness coordinates: %w", err)
	}
	if len(pairs) != count {
		return nil, errors.New("coverage coordinate count does not match snapshot")
	}
	coordinates := make([]osm.RouteCoordinate, count)
	for i, point := range pairs {
		coordinates[i] = osm.RouteCoordinate{Longitude: point[0], Latitude: point[1]}
	}
	return coordinates, nil
}

func deferCoverageHandoffs(ctx context.Context, db *pgxpool.Pool, accountID uuid.UUID, authorityJob any, workerID string,
	lease uuid.UUID, revision *int64, items []coverageHandoff, options coverageReplayOptions) error {
	if options.renew != nil {
		if err := options.renew(ctx); err != nil {
			return err
		}
	}
	if options.requestTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, options.requestTimeout)
		defer cancel()
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return err
	}
	tx, err := beginAccount(ctx, db, accountID)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var deferred bool
	if err := tx.QueryRow(ctx, `SELECT app.defer_ingest_coverage_batch($1,$2,$3,$4,$5,$6,$7)`,
		accountID, authorityJob, workerID, lease, revision, encoded, database.SupportedSchemaVersion).Scan(&deferred); err != nil {
		return err
	}
	if !deferred {
		return errLeaseLost
	}
	return tx.Commit(ctx)
}

func transientCoverageReadinessError(err error) bool {
	pending := []error{err}
	var transient, deadline, queryCancelled bool
	// Inspect every branch, not just the first errors.As/Is match. Wrappers are
	// neutral; an unknown/permanent/cancelled leaf defeats incidental deadlines.
	for visited := 0; len(pending) > 0; visited++ {
		if visited >= 256 {
			return false
		}
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if current == nil {
			return false
		}
		value := reflect.ValueOf(current)
		if value.Kind() == reflect.Pointer && value.IsNil() {
			return false
		}
		recognized := false
		if sql, ok := current.(interface{ SQLState() string }); ok {
			recognized = true
			switch code := sql.SQLState(); {
			case code == "57014":
				queryCancelled = true
			case transientCoverageSQLState(code):
				transient = true
			default:
				return false
			}
		}
		switch current {
		case context.Canceled:
			return false
		case context.DeadlineExceeded:
			deadline, recognized = true, true
		case osm.ErrMatcherReplicaNotReady:
			transient, recognized = true, true
		case io.EOF, io.ErrUnexpectedEOF, pgconn.ErrConnClosed, net.ErrClosed, syscall.ECONNRESET, syscall.ECONNABORTED, syscall.EPIPE:
			transient, recognized = true, true
		}
		if transport, ok := current.(interface{ OSMConnectionFailureClass() string }); ok {
			if transport.OSMConnectionFailureClass() != "transient" {
				return false
			}
			transient, recognized = true, true
		}
		var children []error
		switch wrapped := current.(type) {
		case interface{ Unwrap() []error }:
			children = wrapped.Unwrap()
		case interface{ Unwrap() error }:
			if child := wrapped.Unwrap(); child != nil {
				children = []error{child}
			}
		}
		if len(children)+len(pending) > 256 {
			return false
		}
		childCount := 0
		for _, child := range children {
			if child != nil {
				pending = append(pending, child)
				childCount++
			}
		}
		if !recognized && childCount == 0 {
			return false
		}
	}
	return deadline || transient && !queryCancelled
}

func coverageHandoffError(err error) error {
	var sqlErr *pgconn.PgError
	if errors.As(err, &sqlErr) && sqlErr.Code == "42501" {
		return fmt.Errorf("%w: %w", errLeaseLost, err)
	}
	return err
}
