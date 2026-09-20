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
	"github.com/jackc/pgx/v5/pgxpool"
)

const coverageProductionContractV1 = "coverage-production-v1"

type CoverageReconciler struct {
	db, osmDB        *pgxpool.Pool
	logger           *slog.Logger
	workerID         string
	pollInterval     time.Duration
	scanInterval     time.Duration
	leaseDuration    time.Duration
	pageSize         int
	minimumTraversal float64
}

type reconciliationClaim struct {
	accountID uuid.UUID
	revision  int64
	cursor    *uuid.UUID
	leaseID   uuid.UUID
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
}

func NewCoverageReconciler(db, osmDB *pgxpool.Pool, logger *slog.Logger, options CoverageReconcilerOptions) *CoverageReconciler {
	result := &CoverageReconciler{db: db, osmDB: osmDB, logger: logger,
		workerID: "coverage-reconciler-" + uuid.NewString(), pollInterval: 30 * time.Second,
		scanInterval: 24 * time.Hour, leaseDuration: 2 * time.Minute, pageSize: 10, minimumTraversal: 5}
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
	if err := r.syncRegionCatalog(ctx); err != nil {
		return false, err
	}
	if err := r.observe(ctx); err != nil {
		return false, err
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
	regions, err := osm.ConfiguredRegions(ctx, r.osmDB)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(regions)
	if err != nil {
		return fmt.Errorf("encode configured OSM regions: %w", err)
	}
	var synced int
	if err := r.db.QueryRow(ctx, `SELECT app.sync_coverage_region_catalog($1)`, payload).Scan(&synced); err != nil {
		return fmt.Errorf("sync coverage region catalog: %w", err)
	}
	return nil
}

func (r *CoverageReconciler) observe(ctx context.Context) error {
	eventID, err := osm.PromotionEventHead(ctx, r.osmDB)
	if err != nil {
		return err
	}
	var revision int64
	var changed bool
	if err := r.db.QueryRow(ctx, `SELECT desired_revision,changed FROM app.observe_coverage_reconciliation($1,$2)`,
		eventID, coverageProductionContractV1).Scan(&revision, &changed); err != nil {
		return fmt.Errorf("observe coverage reconciliation: %w", err)
	}
	return nil
}

func (r *CoverageReconciler) claim(ctx context.Context) (reconciliationClaim, bool, error) {
	leaseID := uuid.New()
	var claim reconciliationClaim
	if err := r.db.QueryRow(ctx, `SELECT account_id,reconciliation_revision,cursor_workout_id
		FROM app.claim_coverage_reconciliation($1,$2,$3,$4)`, r.workerID, leaseID, r.leaseDuration,
		database.SupportedSchemaVersion).Scan(&claim.accountID, &claim.revision, &claim.cursor); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return reconciliationClaim{}, false, nil
		}
		return reconciliationClaim{}, false, fmt.Errorf("claim coverage reconciliation: %w", err)
	}
	claim.leaseID = leaseID
	return claim, true, nil
}

func (r *CoverageReconciler) process(ctx context.Context, claim reconciliationClaim) error {
	cursor := claim.cursor
	for {
		if err := r.heartbeat(ctx, claim); err != nil {
			return err
		}
		routes, err := r.readRoutes(ctx, claim, cursor)
		if err != nil {
			return err
		}
		if len(routes) == 0 {
			return r.advance(ctx, claim, cursor, true)
		}
		for _, route := range routes {
			if err := r.heartbeat(ctx, claim); err != nil {
				return err
			}
			if err := r.reconcileRoute(ctx, claim, route); err != nil {
				return err
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
	points, err := r.readRoutePoints(ctx, claim, route)
	if err != nil {
		return err
	}
	if len(points) == 0 || len(points) > coverageOriginalPointLimit {
		return errors.New("reconciliation route point count is invalid")
	}
	digest := canonicalRouteInputSHA256(points)
	if !bytes.Equal(digest[:], route.digest) {
		newRevision, err := r.repairRouteDigest(ctx, claim, route, digest[:])
		if err != nil {
			return err
		}
		if newRevision == 0 {
			return nil
		}
		route.revision, route.digest = newRevision, append([]byte(nil), digest[:]...)
	}
	coordinates := make([]osm.RouteCoordinate, len(points))
	for i, point := range points {
		coordinates[i] = osm.RouteCoordinate{Longitude: point.Longitude, Latitude: point.Latitude}
	}
	readiness, err := osm.ResolveRouteRegions(ctx, r.osmDB, coordinates)
	if err != nil {
		return err
	}
	regions, err := json.Marshal(readiness.Regions)
	if err != nil {
		return err
	}
	tx, err := beginAccount(ctx, r.db, claim.accountID)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var applied bool
	if err := tx.QueryRow(ctx, `SELECT app.apply_coverage_reconciliation($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,
		$11,$12,$13,$14,$15,$16)`, claim.accountID, claim.revision, r.workerID, claim.leaseID, route.workoutID,
		route.revision, route.digest, readiness.State, nullableReadinessReason(readiness.Reason), regions, uuid.New(), uuid.New(),
		string(coverage.ExperimentalRulesV1), string(coverage.ExperimentalSamplingV1),
		string(coverage.ExperimentalPathPolicyV82), r.minimumTraversal).Scan(&applied); err != nil {
		return err
	}
	if !applied {
		return errLeaseLost
	}
	return tx.Commit(ctx)
}

func (r *CoverageReconciler) readRoutePoints(ctx context.Context, claim reconciliationClaim, route reconciliationRoute) ([]coverageRoutePoint, error) {
	tx, err := beginAccount(ctx, r.db, claim.accountID)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT * FROM app.read_coverage_reconciliation_route($1,$2,$3,$4,$5,$6,$7)`,
		claim.accountID, claim.revision, r.workerID, claim.leaseID, route.workoutID, route.revision, route.digest)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var points []coverageRoutePoint
	for rows.Next() {
		var point coverageRoutePoint
		if err := rows.Scan(&point.Sequence, &point.Timestamp, &point.Latitude, &point.Longitude, &point.Altitude,
			&point.Speed, &point.Course, &point.HorizontalAccuracy, &point.VerticalAccuracy,
			&point.SpeedAccuracy, &point.CourseAccuracy); err != nil {
			return nil, err
		}
		points = append(points, point)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return points, tx.Commit(ctx)
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
