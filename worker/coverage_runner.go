package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/erhhung/workouts-explorer/internal/coverage/routepipeline"
	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Production consumes already-normalized, trusted route data and can safely
// accept larger inputs than the public diagnostics endpoint.
const (
	coverageOriginalPointLimit  = 50000
	maximumCoverageRouteTimeout = 10 * time.Minute
)

type CoverageRunner struct {
	db                *pgxpool.Pool
	osmDB             *pgxpool.Pool
	logger            *slog.Logger
	workerID          string
	pollInterval      time.Duration
	admissionInterval time.Duration
	leaseDuration     time.Duration
	heartbeatInterval time.Duration
	routeTimeout      time.Duration
}

type CoverageRunnerOptions struct {
	PollInterval, AdmissionInterval, LeaseDuration, HeartbeatInterval, RouteTimeout time.Duration
}

type coverageClaim struct {
	jobID, accountID, parentID, workoutID uuid.UUID
	leaseID                               uuid.UUID
	routeRevision                         int64
	routeDigest                           []byte
	rulesVersion, samplingVersion         string
	pathPolicyVersion                     string
	minimumTraversalMeters                float64
	targetGenerations                     []coverageTargetGeneration
}

type coverageTargetGeneration struct {
	RegionID   string `json:"regionId"`
	Generation int64  `json:"generation"`
}

type coverageRoute struct {
	points                 []coverageRoutePoint
	observations           []coverage.GeographicObservation
	startedAt              time.Time
	typeKey, providerLabel string
	timeoutRetryCount      int
}

func NewCoverageRunner(db, osmDB *pgxpool.Pool, logger *slog.Logger) *CoverageRunner {
	return NewCoverageRunnerWithOptions(db, osmDB, logger, CoverageRunnerOptions{})
}

func NewCoverageRunnerWithOptions(db, osmDB *pgxpool.Pool, logger *slog.Logger, options CoverageRunnerOptions) *CoverageRunner {
	runner := &CoverageRunner{db: db, osmDB: osmDB, logger: logger, workerID: "coverage-worker-" + uuid.NewString(),
		pollInterval: time.Second, admissionInterval: time.Second, leaseDuration: 4 * time.Minute,
		heartbeatInterval: 20 * time.Second, routeTimeout: 3 * time.Minute}
	if options.PollInterval > 0 {
		runner.pollInterval = options.PollInterval
	}
	if options.AdmissionInterval > 0 {
		runner.admissionInterval = options.AdmissionInterval
	}
	if options.LeaseDuration > 0 {
		runner.leaseDuration = options.LeaseDuration
	}
	if options.HeartbeatInterval > 0 {
		runner.heartbeatInterval = options.HeartbeatInterval
	}
	if options.RouteTimeout > 0 {
		runner.routeTimeout = options.RouteTimeout
	}
	return runner
}

func (r *CoverageRunner) Run(ctx context.Context) error {
	for {
		worked, err := r.RunOnce(ctx)
		if err != nil && ctx.Err() == nil {
			r.logger.Error("coverage worker cycle failed", "error", err)
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

func (r *CoverageRunner) RunOnce(ctx context.Context) (bool, error) {
	claim, found, err := r.claim(ctx)
	if err != nil && found {
		return true, r.failDetached(claim, time.Now(), "coverage-route-invalid", "Coverage job parameters are invalid.")
	}
	if err != nil || !found {
		return found, err
	}
	started := time.Now()
	opCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	heartbeatDone := make(chan error, 1)
	go r.heartbeat(opCtx, claim, cancel, heartbeatDone)

	route, err := r.loadRoute(opCtx, claim)
	if err != nil {
		cause := context.Cause(opCtx)
		cancel(err)
		<-heartbeatDone
		if handled, interruptedErr := r.handleInterruption(claim, cause); handled {
			return true, interruptedErr
		}
		return true, r.failDetached(claim, started, "coverage-route-read-failed", "Coverage route input could not be read.")
	}
	digest := canonicalRouteInputSHA256(route.points)
	if !bytes.Equal(digest[:], claim.routeDigest) {
		cancel(errors.New("coverage route digest changed"))
		<-heartbeatDone
		return true, r.failDetached(claim, started, "coverage-route-invalid", "Coverage route input changed before matching.")
	}

	slotID := uuid.New()
	for {
		acquired, acquireErr := r.acquireSlot(opCtx, claim, slotID)
		if acquireErr != nil {
			cancel(acquireErr)
			<-heartbeatDone
			return true, acquireErr
		}
		if acquired {
			break
		}
		timer := time.NewTimer(r.admissionInterval)
		select {
		case <-opCtx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			cause := context.Cause(opCtx)
			cancel(cause)
			<-heartbeatDone
			_, interruptedErr := r.handleInterruption(claim, cause)
			return true, interruptedErr
		case <-timer.C:
		}
	}
	released := false
	defer func() {
		if !released {
			_ = r.releaseSlotDetached(claim, slotID)
		}
	}()

	matchTimeout := coverageRouteTimeout(r.routeTimeout, route.timeoutRetryCount)
	matchCtx, stopMatch := context.WithTimeout(opCtx, matchTimeout)
	targetGenerations := make([]osm.MatcherGeneration, len(claim.targetGenerations))
	for i, target := range claim.targetGenerations {
		targetGenerations[i] = osm.MatcherGeneration{RegionID: target.RegionID, GenerationID: target.Generation}
	}
	snapshot, err := osm.BeginMatcherSnapshotForGenerations(matchCtx, r.osmDB, targetGenerations)
	if err != nil {
		stopMatch()
		cancel(err)
		<-heartbeatDone
		_ = r.releaseSlotDetached(claim, slotID)
		released = true
		if errors.Is(err, osm.ErrMatcherGenerationChanged) {
			return true, r.supersedeDetached(claim, started)
		}
		return true, r.failDetached(claim, started, "coverage-osm-unavailable", "Coverage map data is temporarily unavailable.")
	}
	result, matchErr := routepipeline.Evaluate(matchCtx, snapshot, route.observations, routepipeline.Config{
		Rules:    coverage.ExperimentalRules().WithMinimumTraversalMeters(claim.minimumTraversalMeters),
		Sampling: coverage.ExperimentalSamplingRules(), RoadGeometry: coverage.DefaultRoadGeometryRules(),
		MovementMode: routepipeline.MovementMode(route.typeKey, route.providerLabel), Limits: routepipeline.ProductionLimits(),
	})
	var prepared []routepipeline.PreparedMatch
	if matchErr == nil {
		matchErr = verifyCoverageGenerations(matchCtx, snapshot, claim.targetGenerations)
	}
	if matchErr == nil {
		prepared, matchErr = routepipeline.Prepare(matchCtx, snapshot, result, route.startedAt)
	}
	var outcome string
	persistFailed := false
	if matchErr == nil {
		var payload []byte
		payload, matchErr = json.Marshal(coveragePersistencePayload(prepared))
		if matchErr == nil {
			generations, _ := json.Marshal(claim.targetGenerations)
			outcome, matchErr = r.persist(matchCtx, claim, int(time.Since(started).Milliseconds()), generations, payload)
			persistFailed = matchErr != nil
		}
	}
	closeCtx, closeCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	closeErr := snapshot.Close(closeCtx)
	closeCancel()
	stopMatch()
	if matchErr == nil && outcome == "" {
		matchErr = closeErr
	}
	_ = r.releaseSlotDetached(claim, slotID)
	released = true
	if matchErr != nil {
		cause := context.Cause(opCtx)
		cancel(matchErr)
		<-heartbeatDone
		if handled, interruptedErr := r.handleInterruption(claim, cause); handled {
			return true, interruptedErr
		}
		if persistFailed {
			return true, r.failDetached(claim, started, "coverage-persist-failed", "Coverage results could not be persisted.")
		}
		code, summary := classifyCoverageFailure(matchCtx, matchErr)
		return true, r.failDetached(claim, started, code, summary)
	}
	cancel(nil)
	<-heartbeatDone
	r.logger.Info("coverage route completed", "job_id", claim.jobID, "outcome", outcome,
		"segments", len(prepared), "duration_ms", time.Since(started).Milliseconds(), "timeout_ms", matchTimeout.Milliseconds())
	return true, nil
}

func (r *CoverageRunner) handleInterruption(claim coverageClaim, cause error) (bool, error) {
	if errors.Is(cause, errCancellationRequested) {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		ok, err := r.accountBool(ctx, claim.accountID, `SELECT app.finish_job($1,$2,$3,'cancelled')`,
			claim.jobID, r.workerID, claim.leaseID)
		if err != nil {
			return true, err
		}
		if !ok {
			return true, errLeaseLost
		}
		return true, nil
	}
	if errors.Is(cause, errLeaseLost) || errors.Is(cause, context.Canceled) {
		return true, nil
	}
	return false, nil
}

func (r *CoverageRunner) claim(ctx context.Context) (coverageClaim, bool, error) {
	lease := uuid.New()
	var claim coverageClaim
	var generations []byte
	err := r.db.QueryRow(ctx, `SELECT * FROM app.claim_next_coverage_route($1,$2,$3,$4)`,
		r.workerID, lease, r.leaseDuration, database.SupportedSchemaVersion).Scan(&claim.jobID, &claim.accountID,
		&claim.parentID, &claim.workoutID, &claim.routeRevision, &claim.routeDigest, &claim.rulesVersion,
		&claim.samplingVersion, &claim.pathPolicyVersion, &claim.minimumTraversalMeters, &generations)
	if errors.Is(err, pgx.ErrNoRows) {
		return coverageClaim{}, false, nil
	}
	if err != nil {
		return coverageClaim{}, false, fmt.Errorf("claim coverage route: %w", err)
	}
	claim.leaseID = lease
	if len(claim.routeDigest) != 32 || claim.rulesVersion != string(coverage.ExperimentalRulesV1) ||
		claim.samplingVersion != string(coverage.ExperimentalSamplingV1) || claim.pathPolicyVersion != string(coverage.ExperimentalPathPolicyV82) {
		return claim, true, errors.New("unsupported coverage claim contract")
	}
	decoder := json.NewDecoder(bytes.NewReader(generations))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&claim.targetGenerations); err != nil || len(claim.targetGenerations) == 0 {
		return claim, true, errors.New("invalid coverage generation target")
	}
	sortedTargetGenerations(claim.targetGenerations)
	for i, target := range claim.targetGenerations {
		if target.RegionID == "" || target.Generation <= 0 || i > 0 && target.RegionID == claim.targetGenerations[i-1].RegionID {
			return claim, true, errors.New("invalid coverage generation target")
		}
	}
	return claim, true, nil
}

func (r *CoverageRunner) loadRoute(ctx context.Context, claim coverageClaim) (coverageRoute, error) {
	tx, err := beginAccount(ctx, r.db, claim.accountID)
	if err != nil {
		return coverageRoute{}, err
	}
	defer tx.Rollback(ctx)
	var result coverageRoute
	if err := tx.QueryRow(ctx, `SELECT app.coverage_route_timeout_retry_count($1,$2,$3)`,
		claim.jobID, r.workerID, claim.leaseID).Scan(&result.timeoutRetryCount); err != nil {
		return coverageRoute{}, err
	}
	rows, err := tx.Query(ctx, `SELECT * FROM app.read_coverage_route($1,$2,$3)`, claim.jobID, r.workerID, claim.leaseID)
	if err != nil {
		return coverageRoute{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var point coverageRoutePoint
		if err := rows.Scan(&point.Sequence, &point.Timestamp, &point.Latitude, &point.Longitude, &point.Altitude,
			&point.Speed, &point.Course, &point.HorizontalAccuracy, &point.VerticalAccuracy, &point.SpeedAccuracy,
			&point.CourseAccuracy, &result.startedAt, &result.typeKey, &result.providerLabel); err != nil {
			return coverageRoute{}, err
		}
		result.points = append(result.points, point)
		observation := coverage.GeographicObservation{Sequence: point.Sequence, Longitude: point.Longitude, Latitude: point.Latitude}
		if point.Timestamp != nil {
			observation.Time = *point.Timestamp
		}
		if point.HorizontalAccuracy != nil {
			observation.AccuracyMeters = *point.HorizontalAccuracy
		}
		observation.HeadingDegrees, observation.HeadingAccuracy = point.Course, point.CourseAccuracy
		result.observations = append(result.observations, observation)
	}
	if err := rows.Err(); err != nil {
		return coverageRoute{}, err
	}
	if len(result.points) == 0 || len(result.points) > coverageOriginalPointLimit {
		return coverageRoute{}, errors.New("coverage route point count is invalid")
	}
	if err := tx.Commit(ctx); err != nil {
		return coverageRoute{}, err
	}
	return result, nil
}

func coverageRouteTimeout(base time.Duration, timeoutRetryCount int) time.Duration {
	if base <= 0 || timeoutRetryCount <= 0 {
		return min(base, maximumCoverageRouteTimeout)
	}
	timeout := min(base, maximumCoverageRouteTimeout)
	for range timeoutRetryCount {
		if timeout >= maximumCoverageRouteTimeout/2 {
			return maximumCoverageRouteTimeout
		}
		timeout *= 2
	}
	return timeout
}

func (r *CoverageRunner) heartbeat(ctx context.Context, claim coverageClaim, cancel context.CancelCauseFunc, done chan<- error) {
	ticker := time.NewTicker(r.heartbeatInterval)
	defer ticker.Stop()
	defer func() { done <- context.Cause(ctx) }()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tx, err := beginAccount(ctx, r.db, claim.accountID)
			if err == nil {
				var alive bool
				err = tx.QueryRow(ctx, `SELECT app.heartbeat_job($1,$2,$3,$4)`, claim.jobID, r.workerID, claim.leaseID, r.leaseDuration).Scan(&alive)
				var cancelled bool
				if err == nil && alive {
					err = tx.QueryRow(ctx, `SELECT cancel_requested_at IS NOT NULL FROM app.jobs WHERE id=$1`, claim.jobID).Scan(&cancelled)
				}
				if err == nil {
					err = tx.Commit(ctx)
				} else {
					_ = tx.Rollback(ctx)
				}
				if err == nil && (!alive || cancelled) {
					if cancelled {
						err = errCancellationRequested
					} else {
						err = errLeaseLost
					}
				}
			}
			if err != nil {
				cancel(err)
				return
			}
		}
	}
}

func (r *CoverageRunner) acquireSlot(ctx context.Context, claim coverageClaim, slot uuid.UUID) (bool, error) {
	return r.accountBool(ctx, claim.accountID, `SELECT app.acquire_coverage_matcher_slot($1,$2,$3,$4)`,
		claim.jobID, r.workerID, claim.leaseID, slot)
}

func (r *CoverageRunner) releaseSlotDetached(claim coverageClaim, slot uuid.UUID) error {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	_, err := r.accountBool(ctx, claim.accountID, `SELECT app.release_matcher_slot($1,$2,$3)`, slot, r.workerID, claim.leaseID)
	return err
}

func (r *CoverageRunner) persist(ctx context.Context, claim coverageClaim, duration int, generations, matches []byte) (string, error) {
	tx, err := beginAccount(ctx, r.db, claim.accountID)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var outcome string
	if err := tx.QueryRow(ctx, `SELECT app.persist_coverage_route($1,$2,$3,$4,$5,$6)`, claim.jobID,
		r.workerID, claim.leaseID, duration, generations, matches).Scan(&outcome); err != nil {
		return "", err
	}
	return outcome, tx.Commit(ctx)
}

func (r *CoverageRunner) failDetached(claim coverageClaim, started time.Time, code, summary string) error {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	ok, err := r.accountBool(ctx, claim.accountID, `SELECT app.fail_coverage_route($1,$2,$3,$4,$5,$6)`,
		claim.jobID, r.workerID, claim.leaseID, int(time.Since(started).Milliseconds()), code, summary)
	if err != nil {
		return err
	}
	if !ok {
		return errLeaseLost
	}
	return nil
}

func (r *CoverageRunner) supersedeDetached(claim coverageClaim, started time.Time) error {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	ok, err := r.accountBool(ctx, claim.accountID, `SELECT app.supersede_coverage_route($1,$2,$3,$4)`,
		claim.jobID, r.workerID, claim.leaseID, int(time.Since(started).Milliseconds()))
	if err != nil {
		return err
	}
	if !ok {
		return errLeaseLost
	}
	return nil
}

func (r *CoverageRunner) accountBool(ctx context.Context, account uuid.UUID, query string, args ...any) (bool, error) {
	tx, err := beginAccount(ctx, r.db, account)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var value bool
	if err := tx.QueryRow(ctx, query, args...).Scan(&value); err != nil {
		return false, err
	}
	return value, tx.Commit(ctx)
}

func verifyCoverageGenerations(ctx context.Context, snapshot *osm.MatcherSnapshot, targets []coverageTargetGeneration) error {
	active, err := snapshot.Generations(ctx)
	if err != nil {
		return err
	}
	byRegion := make(map[string]int64, len(active))
	for _, generation := range active {
		byRegion[generation.RegionID] = generation.GenerationID
	}
	for _, target := range targets {
		if byRegion[target.RegionID] != target.Generation {
			return errors.New("coverage OSM generation changed")
		}
	}
	return nil
}

func coveragePersistencePayload(matches []routepipeline.PreparedMatch) []map[string]any {
	result := make([]map[string]any, len(matches))
	for i, match := range matches {
		segment := match.Segment
		result[i] = map[string]any{
			"physicalSegmentId": segment.SegmentID, "regionId": segment.RegionID, "generation": segment.GenerationID,
			"derivationVersion": segment.DerivationVersion, "logicalPathId": segment.LogicalPathID,
			"localityRelationId": segment.LocalityRelationID, "localityRelationVersion": segment.LocalityRelationVersion,
			"localityName": segment.LocalityName, "sourceWayId": segment.SourceWayID, "sourceWayVersion": segment.SourceWayVersion,
			"segmentName": segment.SegmentName, "segmentNormalizedName": segment.SegmentNormalizedName,
			"pathName": segment.PathName, "pathNormalizedName": segment.PathNormalizedName,
			"highway": segment.Highway, "broadClass": segment.BroadClass, "tags": segment.Tags,
			"segmentMeters": segment.LengthMeters, "segmentGeometry": segment.GeoJSON,
			"firstTraversedAt": match.FirstTraversedAt, "firstRouteOrder": match.FirstRouteOrder,
			"coveredGeometry": match.CoveredGeoJSON,
		}
	}
	return result
}

func classifyCoverageFailure(ctx context.Context, err error) (string, string) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "coverage-route-timeout", "Coverage matching timed out."
	}
	var bounds *osm.MatcherBoundsError
	var overflow *osm.MatcherOverflowError
	var limit *routepipeline.LimitError
	if errors.As(err, &bounds) || errors.As(err, &overflow) || errors.As(err, &limit) {
		return "coverage-route-too-large", "Coverage route exceeded a processing limit."
	}
	return "coverage-osm-unavailable", "Coverage map data is temporarily unavailable."
}

func sortedTargetGenerations(targets []coverageTargetGeneration) {
	sort.Slice(targets, func(i, j int) bool { return targets[i].RegionID < targets[j].RegionID })
}
