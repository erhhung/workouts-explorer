package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const coverageProductionContractV1 = "coverage-production-v1"

type CoverageReconciler struct {
	db, osmDB           *pgxpool.Pool
	logger              *slog.Logger
	workerID            string
	pollInterval        time.Duration
	scanInterval        time.Duration
	leaseDuration       time.Duration
	pageSize            int
	minimumTraversal    float64
	osmAutoAddRegions   bool
	useReadReplicasOnly bool
	nextObservation     time.Time
	roiTimeout          time.Duration
}

type reconciliationClaim struct {
	accountID    uuid.UUID
	revision     int64
	cursor       *uuid.UUID
	leaseID      uuid.UUID
	scanRequired bool
}

type reconciliationRoute struct {
	workoutID uuid.UUID
	revision  int64
	digest    []byte
}

type CoverageReconcilerOptions struct {
	PollInterval, ScanInterval, LeaseDuration time.Duration
	PageSize                                  int
	MinimumTraversalMeters                    float64
	OSMAutoAddRegions                         bool
	UseReadReplicasOnly                       bool
}

func NewCoverageReconciler(db, osmDB *pgxpool.Pool, logger *slog.Logger, options CoverageReconcilerOptions) *CoverageReconciler {
	result := &CoverageReconciler{db: db, osmDB: osmDB, logger: logger,
		workerID: "coverage-reconciler-" + uuid.NewString(), pollInterval: 30 * time.Second,
		scanInterval: 24 * time.Hour, leaseDuration: 2 * time.Minute, pageSize: 10, minimumTraversal: 5, roiTimeout: 30 * time.Second}
	if options.PollInterval > 0 {
		result.pollInterval = options.PollInterval
	}
	if options.ScanInterval > 0 {
		result.scanInterval = options.ScanInterval
	}
	if options.LeaseDuration > 0 {
		result.leaseDuration = options.LeaseDuration
	}
	if options.PageSize > 0 {
		result.pageSize = options.PageSize
	}
	if options.MinimumTraversalMeters > 0 {
		result.minimumTraversal = options.MinimumTraversalMeters
	}
	result.osmAutoAddRegions = options.OSMAutoAddRegions
	result.useReadReplicasOnly = options.UseReadReplicasOnly
	return result
}

func (r *CoverageReconciler) Run(ctx context.Context) error {
	for {
		worked, err := r.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			r.logger.Error("coverage reconciliation cycle failed", "error", err)
		}
		if ctx.Err() != nil {
			return nil
		}
		if worked {
			continue
		}
		timer := time.NewTimer(r.pollInterval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case <-timer.C:
		}
	}
}

func (r *CoverageReconciler) RunOnce(ctx context.Context) (bool, error) {
	if !time.Now().Before(r.nextObservation) {
		// Work pages may run back-to-back; catalog observation is poll-paced.
		r.nextObservation = time.Now().Add(r.pollInterval)
		if err := r.observe(ctx); err != nil {
			return false, err
		}
	}
	claim, found, err := r.claim(ctx)
	if err != nil || !found {
		return found, err
	}
	if err := r.process(ctx, claim); err != nil {
		_ = r.failDetached(claim, "coverage-reconciliation-failed", "Coverage reconciliation could not be completed.")
		return true, err
	}
	return true, nil
}

func (r *CoverageReconciler) syncRegionCatalog(ctx context.Context) error {
	return r.observe(ctx)
}

func (r *CoverageReconciler) observe(ctx context.Context) error {
	eventID, revision, err := readCoverageCatalogFloor(ctx, r.db)
	if err != nil {
		return fmt.Errorf("read coverage catalog floor: %w", err)
	}
	catalog, err := osm.ReadRouteRegionCatalog(ctx, r.osmDB, eventID, revision)
	if err != nil {
		return database.RedactOSMConnectionError(err)
	}
	heads, err := json.Marshal(catalog.ActiveGenerations)
	if err != nil {
		return err
	}
	configured, err := json.Marshal(catalog.ConfiguredRegions)
	if err != nil {
		return err
	}
	descriptor, err := json.Marshal(productionMatchingDescriptor(r.minimumTraversal))
	if err != nil {
		return err
	}
	var accepted bool
	if err := r.db.QueryRow(ctx, `SELECT app.observe_coverage_catalog($1,$2,$3,$4,$5,$6,$7,$8)`,
		catalog.Revision, catalog.PromotionEventID, catalog.Fingerprint, catalog.SelectionFingerprint, heads,
		coverageSelectionPolicy(r.osmAutoAddRegions), descriptor, configured).Scan(&accepted); err != nil {
		return fmt.Errorf("observe coverage reconciliation: %w", err)
	}
	if !accepted {
		return osm.ErrMatcherReplicaNotReady
	}
	return nil
}

func (r *CoverageReconciler) claim(ctx context.Context) (reconciliationClaim, bool, error) {
	leaseID := uuid.New()
	var claim reconciliationClaim
	if err := r.db.QueryRow(ctx, `SELECT account_id,reconciliation_revision,cursor_workout_id,scan_required
		FROM app.claim_coverage_reconciliation_work($1,$2,$3,$4)`, r.workerID, leaseID, r.leaseDuration,
		database.SupportedSchemaVersion).Scan(&claim.accountID, &claim.revision, &claim.cursor, &claim.scanRequired); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return reconciliationClaim{}, false, nil
		}
		return reconciliationClaim{}, false, fmt.Errorf("claim coverage reconciliation: %w", err)
	}
	claim.leaseID = leaseID
	return claim, true, nil
}

func (r *CoverageReconciler) process(ctx context.Context, claim reconciliationClaim) error {
	// Prepared-only accounts still owe durable work even when the ordinary
	// route scan is empty. Never use a finished source job's former lease here.
	if err := replayIngestCoverage(ctx, r.db, r.osmDB, claim.accountID, uuid.Nil, r.workerID, claim.leaseID, &claim.revision,
		r.pageSize, true, coverageReplayOptions{minimumTraversal: r.minimumTraversal, autoAdd: r.osmAutoAddRegions,
			replicasOnly: r.useReadReplicasOnly, requestTimeout: r.leaseDuration / 2,
			renew: func(ctx context.Context) error { return r.heartbeat(ctx, claim) }}); err != nil {
		return err
	}
	cursor := claim.cursor
	if !claim.scanRequired {
		// Queue recovery must never enter an ordinary campaign or alter its cursor.
		if err := r.processDirtyRoutes(ctx, claim); err != nil {
			return err
		}
		return r.advance(ctx, claim, cursor, true)
	}
	for {
		if err := r.heartbeat(ctx, claim); err != nil {
			return err
		}
		routes, err := r.readRoutes(ctx, claim, cursor)
		if err != nil {
			return err
		}
		if len(routes) == 0 {
			if err := replayIngestCoverage(ctx, r.db, r.osmDB, claim.accountID, uuid.Nil, r.workerID, claim.leaseID, &claim.revision,
				r.pageSize, true, coverageReplayOptions{minimumTraversal: r.minimumTraversal, autoAdd: r.osmAutoAddRegions,
					replicasOnly: r.useReadReplicasOnly, requestTimeout: r.leaseDuration / 2,
					renew: func(ctx context.Context) error { return r.heartbeat(ctx, claim) }}); err != nil {
				return err
			}
			return r.advance(ctx, claim, cursor, true)
		}
		for _, route := range routes {
			if err := r.heartbeat(ctx, claim); err != nil {
				return err
			}
			routeCtx, cancel := context.WithTimeout(ctx, r.leaseDuration/2)
			err := r.reconcileRoute(routeCtx, claim, route)
			cancel()
			if err != nil {
				if err := r.deferRouteFailure(ctx, claim, err); err != nil {
					return err
				}
			}
			cursor = &route.workoutID
			if err := r.advance(ctx, claim, cursor, false); err != nil {
				return err
			}
		}
	}
}

func (r *CoverageReconciler) readRoutes(ctx context.Context, claim reconciliationClaim, cursor *uuid.UUID) ([]reconciliationRoute, error) {
	tx, err := beginAccount(ctx, r.db, claim.accountID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT workout_id,route_input_revision,route_input_sha256
		FROM app.read_coverage_reconciliation_routes($1,$2,$3,$4,$5,$6)`, claim.accountID, claim.revision,
		r.workerID, claim.leaseID, cursor, r.pageSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var routes []reconciliationRoute
	for rows.Next() {
		var route reconciliationRoute
		if err := rows.Scan(&route.workoutID, &route.revision, &route.digest); err != nil {
			return nil, err
		}
		routes = append(routes, route)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return routes, tx.Commit(ctx)
}

func (r *CoverageReconciler) reconcileRoute(ctx context.Context, claim reconciliationClaim, route reconciliationRoute) error {
	item, err := readCoverageSelectionMetadata(ctx, r.db, claim.accountID, nil, r.workerID, claim.leaseID, &claim.revision, route.workoutID)
	if err != nil {
		return err
	}
	if item.Readiness != "" {
		eventID, catalogRevision, err := readCoverageCatalogFloor(ctx, r.db)
		if err != nil {
			return err
		}
		catalog, err := osm.ReadRouteRegionCatalog(ctx, r.osmDB, eventID, max(catalogRevision, item.CatalogRevision))
		if err != nil {
			return database.RedactOSMConnectionError(err)
		}
		if rebindCoverageSelection(&item, catalog, r.osmAutoAddRegions) {
			return r.applySelection(ctx, claim, route, item)
		}
	}
	coordinates, digest, err := r.readRouteSnapshot(ctx, claim, route)
	if err != nil {
		return err
	}
	if !bytes.Equal(digest, route.digest) {
		newRevision, err := r.repairRouteDigest(ctx, claim, route, digest)
		if err != nil {
			return err
		}
		if newRevision == 0 {
			return nil
		}
		route.revision, route.digest = newRevision, digest
	}
	eventID, catalogRevision, err := readCoverageCatalogFloor(ctx, r.db)
	if err != nil {
		return err
	}
	var readiness osm.RouteRegionReadiness
	if len(coordinates) == 0 {
		catalog, catalogErr := osm.ReadRouteRegionCatalog(ctx, r.osmDB, eventID, catalogRevision)
		err = catalogErr
		readiness = osm.RouteRegionReadiness{State: "unavailable", Reason: "no_provider_region", Regions: []osm.RegionGeneration{},
			CatalogRevision: catalog.Revision, CatalogFingerprint: catalog.Fingerprint, SelectionFingerprint: catalog.SelectionFingerprint}
	} else {
		readiness, err = osm.ResolveRouteRegionsWithCatalogBudget(ctx, r.osmDB, coordinates, eventID, catalogRevision,
			min(r.roiTimeout, r.leaseDuration/4))
	}
	if err != nil {
		if reason := deferredRouteResolutionReason(ctx, err); reason != "" {
			return &reconciliationRouteFailure{route: route, mutationVersion: item.MutationVersion, reason: reason,
				cause: database.RedactOSMConnectionError(err)}
		}
		return database.RedactOSMConnectionError(err)
	}
	readiness = routeReadinessForAutoAddPolicy(readiness, r.osmAutoAddRegions)
	setCoverageSelectionProof(&item, readiness, r.osmAutoAddRegions)
	item.RouteDigest = fmt.Sprintf("%x", route.digest)
	return r.applySelection(ctx, claim, route, item)
}

func (r *CoverageReconciler) processDirtyRoutes(ctx context.Context, claim reconciliationClaim) error {
	var cursor *uuid.UUID
	for {
		routes, err := r.readRoutes(ctx, claim, cursor)
		if err != nil {
			return err
		}
		if len(routes) == 0 {
			return nil
		}
		for _, route := range routes {
			if err := r.heartbeat(ctx, claim); err != nil {
				return err
			}
			routeCtx, cancel := context.WithTimeout(ctx, r.leaseDuration/2)
			err := r.reconcileRoute(routeCtx, claim, route)
			cancel()
			if err != nil {
				if err := r.deferRouteFailure(ctx, claim, err); err != nil {
					return err
				}
			}
			cursor = &route.workoutID
		}
	}
}

type reconciliationRouteFailure struct {
	route           reconciliationRoute
	mutationVersion int64
	reason          string
	cause           error
}

func (failure *reconciliationRouteFailure) Error() string {
	return "route map resolution failed: " + failure.reason
}
func (failure *reconciliationRouteFailure) Unwrap() error { return failure.cause }

func deferredRouteResolutionReason(ctx context.Context, err error) string {
	var resolution *osm.RouteRegionResolutionError
	if !errors.As(err, &resolution) || resolution == nil {
		return ""
	}
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, osm.ErrMatcherReplicaNotReady) {
		return ""
	}
	pending := []error{resolution.Err}
	allowed := false
	for visited := 0; len(pending) > 0; visited++ {
		if visited >= 256 {
			return ""
		}
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if current == nil {
			return ""
		}
		if sql, ok := current.(*pgconn.PgError); ok {
			if sql == nil {
				return ""
			}
			if sql.Code == "57014" && resolution.DeadlineExceeded {
				allowed = true
				continue
			}
			if resolution.InvalidData && (sql.Code == "22003" || sql.Code == "22P02" || sql.Code == "22023") {
				allowed = true
				continue
			}
			return ""
		}
		if current == context.DeadlineExceeded && resolution.DeadlineExceeded {
			allowed = true
			continue
		}
		if current == osm.ErrRouteRegionResultInvalid && resolution.InvalidData {
			allowed = true
			continue
		}
		if row, ok := current.(pgx.ScanArgError); ok && resolution.InvalidData {
			var sql *pgconn.PgError
			if errors.As(row.Err, &sql) {
				pending = append(pending, sql)
			} else {
				allowed = true
			}
			continue
		}
		switch wrapped := current.(type) {
		case interface{ Unwrap() []error }:
			pending = append(pending, wrapped.Unwrap()...)
		case interface{ Unwrap() error }:
			pending = append(pending, wrapped.Unwrap())
		default:
			return ""
		}
	}
	if !allowed {
		return ""
	}
	if resolution.DeadlineExceeded {
		return "roi_deadline"
	}
	return "roi_invalid_data"
}

func (r *CoverageReconciler) deferRouteFailure(ctx context.Context, claim reconciliationClaim, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var failure *reconciliationRouteFailure
	if !errors.As(err, &failure) {
		return err
	}
	tx, beginErr := beginAccount(ctx, r.db, claim.accountID)
	if beginErr != nil {
		return beginErr
	}
	defer tx.Rollback(ctx)
	var deferred bool
	if deferErr := tx.QueryRow(ctx, `SELECT app.defer_coverage_reconciliation_route($1,$2,$3,$4,$5,$6,$7,$8)`,
		claim.accountID, claim.revision, r.workerID, claim.leaseID, failure.route.workoutID, failure.route.revision,
		failure.route.digest, failure.mutationVersion).Scan(&deferred); deferErr != nil {
		return deferErr
	}
	if commitErr := tx.Commit(ctx); commitErr != nil {
		return commitErr
	}
	r.logger.Warn("coverage route reconciliation remains pending", "workout_id", failure.route.workoutID,
		"reason", failure.reason, "deferred", deferred)
	return nil
}

func (r *CoverageReconciler) applySelection(ctx context.Context, claim reconciliationClaim, route reconciliationRoute, item coverageHandoff) error {
	item.MatchingDescriptor = productionMatchingDescriptor(r.minimumTraversal)
	encoded, err := json.Marshal(item)
	if err != nil {
		return err
	}
	tx, err := beginAccount(ctx, r.db, claim.accountID)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var applied bool
	if err := tx.QueryRow(ctx, `SELECT app.apply_coverage_selection($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		claim.accountID, claim.revision, r.workerID, claim.leaseID, route.workoutID, route.revision, route.digest, encoded,
		string(coverage.ExperimentalRulesV1), string(coverage.ExperimentalSamplingV1),
		string(coverage.ExperimentalPathPolicyV82), r.minimumTraversal).Scan(&applied); err != nil {
		return err
	}
	if !applied {
		return errLeaseLost
	}
	return tx.Commit(ctx)
}

func (r *CoverageReconciler) readRouteSnapshot(ctx context.Context, claim reconciliationClaim, route reconciliationRoute) ([]osm.RouteCoordinate, []byte, error) {
	tx, err := beginAccount(ctx, r.db, claim.accountID)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx)
	var content, digest, encoded []byte
	var epoch int64
	var count int
	if err := tx.QueryRow(ctx, `SELECT content_sha256,ownership_epoch,route_input_sha256,point_count,coordinates
		FROM app.read_coverage_reconciliation_snapshot($1,$2,$3,$4,$5,$6,$7)`,
		claim.accountID, claim.revision, r.workerID, claim.leaseID, route.workoutID, route.revision, route.digest).
		Scan(&content, &epoch, &digest, &count, &encoded); err != nil {
		return nil, nil, err
	}
	if len(content) != 32 || len(digest) != 32 || epoch < 0 {
		return nil, nil, errors.New("invalid reconciliation route snapshot")
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	coordinates, err := decodeCoverageCoordinates(encoded, count)
	return coordinates, digest, err
}

func (r *CoverageReconciler) repairRouteDigest(ctx context.Context, claim reconciliationClaim, route reconciliationRoute, digest []byte) (int64, error) {
	tx, err := beginAccount(ctx, r.db, claim.accountID)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	var revision *int64
	if err := tx.QueryRow(ctx, `SELECT app.repair_coverage_reconciliation_input($1,$2,$3,$4,$5,$6,$7,$8)`,
		claim.accountID, claim.revision, r.workerID, claim.leaseID, route.workoutID, route.revision, route.digest, digest).Scan(&revision); err != nil {
		return 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, err
	}
	if revision == nil {
		return 0, nil
	}
	return *revision, nil
}

func (r *CoverageReconciler) heartbeat(ctx context.Context, claim reconciliationClaim) error {
	ctx, cancel := context.WithTimeout(ctx, r.leaseDuration/2)
	defer cancel()
	var alive bool
	if err := r.db.QueryRow(ctx, `SELECT app.heartbeat_coverage_reconciliation($1,$2,$3,$4,$5)`,
		claim.accountID, claim.revision, r.workerID, claim.leaseID, r.leaseDuration).Scan(&alive); err != nil {
		return err
	}
	if !alive {
		return errLeaseLost
	}
	return nil
}

func (r *CoverageReconciler) advance(ctx context.Context, claim reconciliationClaim, cursor *uuid.UUID, completed bool) error {
	var advanced bool
	if err := r.db.QueryRow(ctx, `SELECT app.advance_coverage_reconciliation($1,$2,$3,$4,$5,$6,$7)`,
		claim.accountID, claim.revision, r.workerID, claim.leaseID, cursor, completed, r.scanInterval).Scan(&advanced); err != nil {
		return err
	}
	if !advanced {
		return errLeaseLost
	}
	return nil
}

func (r *CoverageReconciler) failDetached(claim reconciliationClaim, code, summary string) error {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	var failed bool
	if err := r.db.QueryRow(ctx, `SELECT app.fail_coverage_reconciliation($1,$2,$3,$4,$5,$6,interval '5 minutes')`,
		claim.accountID, claim.revision, r.workerID, claim.leaseID, code, summary).Scan(&failed); err != nil {
		return err
	}
	return nil
}
