package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/erhhung/workouts-explorer/internal/coverage/readerplacement"
	"github.com/erhhung/workouts-explorer/internal/coverage/routepipeline"
	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
)

const (
	coverageOriginalPointLimit    = routepipeline.OriginalPointLimit
	maximumCoverageRouteTimeout   = 15 * time.Minute
	coverageReplicaAttemptTimeout = 5 * time.Second
	coverageReplicaPollInterval   = 2 * time.Second
)

type CoverageRunner struct {
	db                  *pgxpool.Pool
	osmDB               *pgxpool.Pool
	logger              *slog.Logger
	workerID            string
	pollInterval        time.Duration
	admissionInterval   time.Duration
	leaseDuration       time.Duration
	heartbeatInterval   time.Duration
	routeTimeout        time.Duration
	jobConcurrency      int
	autoRetryCount      int
	useReadReplicasOnly bool
	reader              *readerplacement.Manager
	readerInitErr       error
	metrics             coverageMetrics
}

type CoverageRunnerOptions struct {
	PollInterval, AdmissionInterval, LeaseDuration, HeartbeatInterval, RouteTimeout time.Duration
	UseReadReplicasOnly                                                             bool
	JobConcurrency                                                                  int
	// Nil selects the default; an explicit zero disables automatic retries.
	AutoRetryCount *int
	Reader         *readerplacement.Manager
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
		pollInterval: time.Second, admissionInterval: time.Second, leaseDuration: 7 * time.Minute,
		heartbeatInterval: 20 * time.Second, routeTimeout: 6 * time.Minute, jobConcurrency: 2, autoRetryCount: 1,
		metrics: newCoverageMetrics(otel.Meter(coverageMeterName)), useReadReplicasOnly: options.UseReadReplicasOnly}
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
	if options.JobConcurrency != 0 {
		runner.jobConcurrency = options.JobConcurrency
	}
	if runner.jobConcurrency < 1 || runner.jobConcurrency > 8 {
		runner.readerInitErr = errors.New("coverage job concurrency must be between 1 and 8")
		return runner
	}
	if options.AutoRetryCount != nil {
		runner.autoRetryCount = *options.AutoRetryCount
	}
	if runner.autoRetryCount < 0 || runner.autoRetryCount > 20 {
		runner.readerInitErr = errors.New("coverage automatic retry count must be between 0 and 20")
		return runner
	}
	runner.reader = options.Reader
	if runner.reader == nil && osmDB != nil {
		runner.reader, runner.readerInitErr = readerplacement.New(osmDB, options.UseReadReplicasOnly, runner.jobConcurrency)
	}
	return runner
}

func (r *CoverageRunner) Run(ctx context.Context) error {
	if r.readerInitErr != nil {
		return r.readerInitErr
	}
	var retries sync.WaitGroup
	if r.autoRetryCount > 0 {
		retries.Go(func() { r.runWorker(ctx, r.retryCoverageOnce) })
	}
	defer retries.Wait()
	return r.runWorkers(ctx, r.RunOnce)
}

func (r *CoverageRunner) runWorkers(ctx context.Context, runOnce func(context.Context) (bool, error)) error {
	var workers sync.WaitGroup
	for range r.jobConcurrency {
		workers.Go(func() { r.runWorker(ctx, runOnce) })
	}
	// Cleanup, heartbeats and physical reader closes must finish before pool close.
	workers.Wait()
	return nil
}

func (r *CoverageRunner) runWorker(ctx context.Context, runOnce func(context.Context) (bool, error)) {
	for {
		if ctx.Err() != nil {
			return
		}
		worked, err := runOnce(ctx)
		if err != nil && ctx.Err() == nil {
			r.logger.Error("coverage worker cycle failed", "error", database.RedactOSMConnectionError(err))
		}
		if ctx.Err() != nil {
			return
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
			return
		case <-timer.C:
		}
	}
}

func (r *CoverageRunner) RunOnce(ctx context.Context) (bool, error) {
	if r.readerInitErr != nil {
		return false, r.readerInitErr
	}
	if r.useReadReplicasOnly {
		// Do not drain the queue into failures while no standby is available.
		preflightCtx, stop := context.WithTimeout(ctx, coverageReplicaAttemptTimeout)
		var err error
		if r.osmDB == nil {
			err = errors.New("OSM reader is unavailable")
		} else {
			err = r.osmDB.Ping(preflightCtx)
		}
		stop()
		if err != nil {
			// Pool errors can contain hosts, users and connection strings.
			return false, errors.New("coverage OSM reader preflight failed")
		}
	}
	claim, found, err := r.claim(ctx)
	started := time.Now()
	if found {
		r.metrics.inflight.Add(ctx, 1)
		defer r.metrics.inflight.Add(context.WithoutCancel(ctx), -1)
		defer func() { r.metrics.routeDuration.Record(ctx, time.Since(started).Seconds()) }()
	}
	if err != nil && found {
		return true, r.failClassifiedDetached(claim, started, coverageStageRouteRead, coverageFailure{code: "coverage-route-invalid", summary: "Coverage job parameters are invalid."})
	}
	if err != nil || !found {
		return found, err
	}
	opCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	heartbeatDone := make(chan error, 1)
	go r.heartbeat(opCtx, claim, cancel, heartbeatDone)

	route, err := r.loadRoute(opCtx, claim, started)
	if err != nil {
		failure := classifyCoverageFailure(opCtx, coverageStageRouteRead, err)
		cause := context.Cause(opCtx)
		cancel(err)
		<-heartbeatDone
		if handled, interruptedErr := r.handleInterruption(claim, cause); handled {
			return true, interruptedErr
		}
		return true, r.failClassifiedDetached(claim, started, coverageStageRouteRead, failure)
	}
	digest := canonicalRouteInputSHA256(route.points)
	if !bytes.Equal(digest[:], claim.routeDigest) {
		cancel(errors.New("coverage route digest changed"))
		<-heartbeatDone
		return true, r.failClassifiedDetached(claim, started, coverageStageRouteRead, coverageFailure{code: "coverage-route-invalid", summary: "Coverage route input changed before matching."})
	}

	slotID := uuid.New()
	admissionStarted := time.Now()
	for {
		acquired, acquireErr := r.acquireSlot(opCtx, claim, slotID)
		if acquireErr != nil {
			r.metrics.admissionWait.Record(ctx, time.Since(admissionStarted).Seconds())
			cancel(acquireErr)
			<-heartbeatDone
			if handled, interruptedErr := r.handleInterruption(claim, context.Cause(opCtx)); handled {
				return true, interruptedErr
			}
			return true, acquireErr
		}
		if acquired {
			r.metrics.admissionWait.Record(ctx, time.Since(admissionStarted).Seconds())
			break
		}
		timer := time.NewTimer(r.admissionInterval)
		select {
		case <-opCtx.Done():
			r.metrics.admissionWait.Record(ctx, time.Since(admissionStarted).Seconds())
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
	cleanupConfirmed := true
	defer func() {
		if cleanupConfirmed && !released {
			_ = r.releaseSlotDetached(claim, slotID)
		}
	}()

	matchTimeout := coverageRouteTimeout(r.routeTimeout, route.timeoutRetryCount)
	targetGenerations := make([]osm.MatcherGeneration, len(claim.targetGenerations))
	for i, target := range claim.targetGenerations {
		targetGenerations[i] = osm.MatcherGeneration{RegionID: target.RegionID, GenerationID: target.Generation}
	}
	cleanupConfirmed = false
	snapshot, err := r.beginMatcherSnapshot(opCtx, claim, slotID, targetGenerations)
	if err != nil {
		if errors.Is(err, readerplacement.ErrCleanupUnconfirmed) {
			cancel(readerplacement.ErrCleanupUnconfirmed)
			<-heartbeatDone
			return true, readerplacement.ErrCleanupUnconfirmed
		}
		cleanupConfirmed = true
		cause := context.Cause(opCtx)
		failure := classifyCoverageFailure(opCtx, coverageStageSnapshotBegin, coverageDatabaseFailure(err))
		cancel(err)
		<-heartbeatDone
		released = r.releaseSlotDetached(claim, slotID) == nil
		if handled, interruptedErr := r.handleInterruption(claim, cause); handled {
			return true, interruptedErr
		}
		if errors.Is(err, osm.ErrMatcherGenerationChanged) {
			return true, r.supersedeDetached(claim, started)
		}
		return true, r.failClassifiedDetached(claim, started, coverageStageSnapshotBegin, failure)
	}
	matchCtx, stopMatch := context.WithTimeout(opCtx, matchTimeout)
	matcherStarted := time.Now()
	failureStage := coverageStageEvaluate
	dbSnapshot := coverageDatabaseSnapshot{snapshot}
	result, matchErr := routepipeline.Evaluate(matchCtx, dbSnapshot, route.observations, routepipeline.Config{
		Rules:    coverage.ExperimentalRules().WithMinimumTraversalMeters(claim.minimumTraversalMeters),
		Sampling: coverage.ExperimentalSamplingRules(), RoadGeometry: coverage.DefaultRoadGeometryRules(),
		MovementMode: routepipeline.MovementMode(route.typeKey, route.providerLabel), Limits: routepipeline.DefaultLimits(),
	})
	r.metrics.matcherDuration.Record(ctx, time.Since(matcherStarted).Seconds())
	var prepared []routepipeline.PreparedMatch
	if matchErr == nil {
		failureStage = coverageStageVerifyGenerations
		matchErr = verifyCoverageGenerations(matchCtx, dbSnapshot, claim.targetGenerations)
	}
	if matchErr == nil {
		failureStage = coverageStagePrepare
		prepared, matchErr = routepipeline.Prepare(matchCtx, dbSnapshot, result, route.startedAt)
	}
	var outcome string
	if matchErr == nil {
		var payload []byte
		payload, matchErr = json.Marshal(coveragePersistencePayload(prepared))
		if matchErr == nil {
			failureStage = coverageStagePersist
			generations, _ := json.Marshal(claim.targetGenerations)
			outcome, matchErr = r.persist(matchCtx, claim, int(time.Since(started).Milliseconds()), generations, payload)
		}
	}
	// Classify before cleanup can cancel or expire the matcher context.
	failure := classifyCoverageFailure(matchCtx, failureStage, matchErr)
	closeCtx, closeCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	closeErr := snapshot.Close(closeCtx)
	cleanupConfirmed = snapshot.PhysicalConnectionClosed()
	closeCancel()
	stopMatch()
	if !cleanupConfirmed {
		cancel(readerplacement.ErrCleanupUnconfirmed)
		<-heartbeatDone
		return true, readerplacement.ErrCleanupUnconfirmed
	}
	if closeErr != nil {
		// Socket/rollback errors may contain private driver details. Physical
		// closure, not rollback success or job status, decides capacity release.
		r.logger.Warn("coverage OSM snapshot cleanup reported an error after physical close")
		r.metrics.control(ctx, "reader_close", "", errors.New("coverage OSM snapshot cleanup failed"))
		closeErr = errors.New("coverage OSM snapshot cleanup failed")
	}
	if matchErr == nil && outcome == "" {
		matchErr = closeErr
		failure = classifyCoverageFailure(context.WithoutCancel(ctx), failureStage, matchErr)
	}
	// Even if clear fails, releasing the slot is safe after confirmed closure.
	_ = r.clearReaderDetached(claim, slotID)
	released = r.releaseSlotDetached(claim, slotID) == nil
	if matchErr != nil {
		cause := context.Cause(opCtx)
		if errors.Is(matchErr, errCancellationRequested) {
			cause = errCancellationRequested
		}
		cancel(matchErr)
		<-heartbeatDone
		if handled, interruptedErr := r.handleInterruption(claim, cause); handled {
			return true, interruptedErr
		}
		if errors.Is(matchErr, errLeaseLost) {
			return true, nil
		}
		return true, r.failClassifiedDetached(claim, started, failureStage, failure)
	}
	cancel(nil)
	<-heartbeatDone
	r.logger.Info("coverage route completed", "job_id", claim.jobID, "outcome", outcome,
		"segments", len(prepared), "duration_ms", time.Since(started).Milliseconds(), "timeout_ms", matchTimeout.Milliseconds())
	return true, nil
}

func (r *CoverageRunner) beginMatcherSnapshot(ctx context.Context, claim coverageClaim, slot uuid.UUID, targets []osm.MatcherGeneration) (*osm.MatcherSnapshot, error) {
	// Catch-up has no execution budget. A pending route retains admission and
	// its renewing lease; operators should monitor sustained reader unavailability.
	waiting := false
	for {
		if ctx.Err() != nil {
			return nil, context.Cause(ctx)
		}
		attemptCtx, stop := context.WithTimeout(ctx, coverageReplicaAttemptTimeout)
		var snapshot *osm.MatcherSnapshot
		var err error
		if r.reader == nil {
			err = errors.New("coverage OSM reader is unavailable")
		} else {
			snapshot, err = r.reader.Begin(attemptCtx, targets,
				func(ctx context.Context, cluster string, candidates []string) (string, error) {
					return r.accountReader(ctx, claim, slot, cluster, candidates)
				},
				func(context.Context) error { return r.clearReaderDetached(claim, slot) })
		}
		attemptErr := attemptCtx.Err()
		stop()
		if errors.Is(err, readerplacement.ErrCleanupUnconfirmed) {
			return nil, readerplacement.ErrCleanupUnconfirmed
		}
		if ctx.Err() != nil {
			closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			closeErr := snapshot.Close(closeCtx)
			cancel()
			if snapshot != nil {
				if !snapshot.PhysicalConnectionClosed() {
					return nil, readerplacement.ErrCleanupUnconfirmed
				}
				if closeErr != nil {
					r.logger.Warn("coverage OSM snapshot cleanup reported an error after physical close")
				}
				_ = r.clearReaderDetached(claim, slot)
			}
			return nil, context.Cause(ctx)
		}
		if err == nil {
			return snapshot, nil
		}
		if !errors.Is(err, osm.ErrMatcherReplicaNotReady) && !recoverableCoverageReaderError(err) {
			var sqlErr interface{ SQLState() string }
			// A coincident initialization deadline must not turn a fatal
			// authentication or permission SQLSTATE into an endless retry.
			if !errors.Is(attemptErr, context.DeadlineExceeded) || errors.As(err, &sqlErr) {
				return nil, err
			}
		}
		if !waiting {
			r.logger.Warn("coverage OSM reader is not ready; waiting for target snapshot")
			waiting = true
		}
		timer := time.NewTimer(coverageReplicaPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, context.Cause(ctx)
		case <-timer.C:
		}
	}
}

func recoverableCoverageReaderError(err error) bool {
	retryable := database.IsRetryableOSMConnectionError(err)
	var sqlErr interface{ SQLState() string }
	if retryable || errors.As(err, &sqlErr) || errors.Is(err, context.Canceled) {
		// Do not let a retryable leaf override a sanitized permission error.
		return retryable
	}
	// The reader policy and pgxpool do not export these errors as sentinels.
	// Match only their exact static leaf errors, never arbitrary connect errors.
	pending := []error{err}
	for len(pending) > 0 {
		current := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		switch wrapped := current.(type) {
		case interface{ Unwrap() []error }:
			pending = append(pending, wrapped.Unwrap()...)
		case interface{ Unwrap() error }:
			if inner := wrapped.Unwrap(); inner != nil {
				pending = append(pending, inner)
			}
		default:
			if current != nil && (current.Error() == "OSM reader requires a standby" || current.Error() == "pgxpool: too many failed attempts acquiring connection; likely bug in PrepareConn, BeforeAcquire, or ShouldPing hook") {
				return true
			}
		}
	}
	return false
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
			r.metrics.reject(ctx, "cancel")
			return true, errLeaseLost
		}
		r.metrics.terminal(ctx, "cancelled", "")
		return true, nil
	}
	if errors.Is(cause, errLeaseLost) || errors.Is(cause, context.Canceled) || r.useReadReplicasOnly && errors.Is(cause, context.DeadlineExceeded) {
		return true, nil
	}
	return false, nil
}

func (r *CoverageRunner) claim(ctx context.Context) (coverageClaim, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, cleanupTimeout)
	defer cancel()
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

func (r *CoverageRunner) loadRoute(ctx context.Context, claim coverageClaim, claimedAt time.Time) (coverageRoute, error) {
	tx, err := r.beginAccount(ctx, claim.accountID)
	if err != nil {
		return coverageRoute{}, coverageDatabaseFailure(err)
	}
	defer r.rollback(tx)
	var result coverageRoute
	var createdAt time.Time
	var attempts int
	var retry bool
	if err := tx.QueryRow(ctx, `SELECT created_at, attempt, retry_of_job_id IS NOT NULL FROM app.jobs
		WHERE id=$1 AND account_id=$2 AND status='running' AND worker_id=$3 AND lease_token=$4`,
		claim.jobID, claim.accountID, r.workerID, claim.leaseID).Scan(&createdAt, &attempts, &retry); err != nil {
		return coverageRoute{}, coverageDatabaseFailure(err)
	}
	if err := tx.QueryRow(ctx, `SELECT app.coverage_route_timeout_retry_count($1,$2,$3)`,
		claim.jobID, r.workerID, claim.leaseID).Scan(&result.timeoutRetryCount); err != nil {
		return coverageRoute{}, coverageDatabaseFailure(err)
	}
	r.metrics.loaded(ctx, claimedAt, createdAt, attempts, retry, result.timeoutRetryCount)
	rows, err := tx.Query(ctx, `SELECT * FROM app.read_coverage_route($1,$2,$3)`, claim.jobID, r.workerID, claim.leaseID)
	if err != nil {
		return coverageRoute{}, coverageDatabaseFailure(err)
	}
	defer rows.Close()
	for rows.Next() {
		var point coverageRoutePoint
		if err := rows.Scan(&point.Sequence, &point.Timestamp, &point.Latitude, &point.Longitude, &point.Altitude,
			&point.Speed, &point.Course, &point.HorizontalAccuracy, &point.VerticalAccuracy, &point.SpeedAccuracy,
			&point.CourseAccuracy, &result.startedAt, &result.typeKey, &result.providerLabel); err != nil {
			return coverageRoute{}, coverageDatabaseFailure(err)
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
		return coverageRoute{}, coverageDatabaseFailure(err)
	}
	if len(result.points) > routepipeline.OriginalPointLimit {
		return coverageRoute{}, &routepipeline.LimitError{Field: "original points", Limit: routepipeline.OriginalPointLimit}
	}
	if len(result.points) == 0 {
		return coverageRoute{}, errors.New("coverage route point count is invalid")
	}
	if err := tx.Commit(ctx); err != nil {
		return coverageRoute{}, coverageDatabaseFailure(err)
	}
	return result, nil
}

func coverageRouteTimeout(base time.Duration, timeoutRetryCount int) time.Duration {
	if base <= 0 || timeoutRetryCount <= 0 {
		return min(base, maximumCoverageRouteTimeout)
	}
	// Retry floors are fixed, not multiples of the configured initial budget.
	if timeoutRetryCount == 1 {
		return min(max(base, 10*time.Minute), maximumCoverageRouteTimeout)
	}
	return maximumCoverageRouteTimeout
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
			// Bound the entire control transaction so a blocked call cannot
			// silently starve lease renewal for the route timeout's duration.
			controlCtx, stop := context.WithTimeout(ctx, cleanupTimeout)
			tx, err := r.beginAccount(controlCtx, claim.accountID)
			var alive bool
			var cancelled *bool
			if err == nil {
				err = tx.QueryRow(controlCtx, `SELECT app.heartbeat_job($1,$2,$3,$4)`, claim.jobID, r.workerID, claim.leaseID, r.leaseDuration).Scan(&alive)
				if err == nil && alive {
					err = tx.QueryRow(controlCtx, `SELECT app.job_cancel_requested($1,$2,$3)`, claim.jobID, r.workerID, claim.leaseID).Scan(&cancelled)
				}
				if err == nil {
					err = tx.Commit(controlCtx)
				}
				r.rollback(tx)
			}
			// Some database timeout errors do not wrap the context error.
			metricErr := err
			if err != nil && controlCtx.Err() != nil {
				metricErr = controlCtx.Err()
			}
			stop()
			result := "renewed"
			if err == nil && (!alive || cancelled == nil) {
				result, err = "lost", errLeaseLost
			} else if err == nil && cancelled != nil && *cancelled {
				err = errCancellationRequested
			}
			r.metrics.control(ctx, "heartbeat", result, metricErr)
			if err != nil {
				cancel(err)
				return
			}
		}
	}
}

func (r *CoverageRunner) acquireSlot(ctx context.Context, claim coverageClaim, slot uuid.UUID) (bool, error) {
	acquired, err := r.accountBool(ctx, claim.accountID, `SELECT app.acquire_coverage_matcher_slot($1,$2,$3,$4)`,
		claim.jobID, r.workerID, claim.leaseID, slot)
	result := "denied"
	if acquired {
		result = "acquired"
	}
	r.metrics.control(ctx, "admission", result, err)
	return acquired, err
}

func (r *CoverageRunner) accountReader(ctx context.Context, claim coverageClaim, slot uuid.UUID, cluster string, candidates []string) (chosen string, err error) {
	ctx, cancel := context.WithTimeout(ctx, cleanupTimeout)
	defer cancel()
	defer func() {
		if err != nil && ctx.Err() != nil {
			err = errors.Join(err, ctx.Err())
		}
	}()
	tx, err := r.beginAccount(ctx, claim.accountID)
	if err != nil {
		return "", err
	}
	defer r.rollback(tx)
	var assigned *string
	if err = tx.QueryRow(ctx, `SELECT app.assign_matcher_reader($1,$2,$3,$4,$5)`,
		slot, r.workerID, claim.leaseID, cluster, candidates).Scan(&assigned); err != nil {
		return "", err
	}
	if err = tx.Commit(ctx); err != nil {
		return "", err
	}
	result := "assigned"
	if assigned == nil {
		result = "denied"
	}
	r.metrics.control(ctx, "reader_assignment", result, nil)
	if assigned == nil {
		// Capacity and ownership denial are both fail-closed. Heartbeats fence
		// lost ownership while a valid lease waits for reader capacity.
		return "", osm.ErrMatcherReplicaNotReady
	}
	return *assigned, nil
}

func (r *CoverageRunner) clearReaderDetached(claim coverageClaim, slot uuid.UUID) error {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	cleared, err := r.accountBool(ctx, claim.accountID, `SELECT app.clear_matcher_reader($1,$2,$3)`,
		slot, r.workerID, claim.leaseID)
	result := "not_found"
	if cleared {
		result = "cleared"
	}
	r.metrics.control(ctx, "reader_clear", result, err)
	return err
}

func (r *CoverageRunner) releaseSlotDetached(claim coverageClaim, slot uuid.UUID) error {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	released, err := r.accountBool(ctx, claim.accountID, `SELECT app.release_matcher_slot($1,$2,$3)`, slot, r.workerID, claim.leaseID)
	result := "not_found"
	if released {
		result = "released"
	}
	r.metrics.control(ctx, "release", result, err)
	if err != nil {
		r.logger.Warn("coverage matcher slot release failed", "error", err)
	}
	// A committed false result proves this account/owner/lease no longer owns
	// the slot. Errors leave ownership unresolved and require deferred retry.
	return err
}

func (r *CoverageRunner) persist(ctx context.Context, claim coverageClaim, duration int, generations, matches []byte) (string, error) {
	tx, err := r.beginAccount(ctx, claim.accountID)
	if err != nil {
		return "", coverageDatabaseFailure(err)
	}
	defer r.rollback(tx)
	var outcome *string
	if err := tx.QueryRow(ctx, `SELECT app.persist_coverage_route($1,$2,$3,$4,$5,$6)`, claim.jobID,
		r.workerID, claim.leaseID, duration, generations, matches).Scan(&outcome); err != nil {
		return "", coverageDatabaseFailure(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", coverageDatabaseFailure(err)
	}
	if outcome == nil {
		r.metrics.reject(ctx, "persist")
		return "", r.rejectionCause(claim)
	}
	r.metrics.terminal(ctx, *outcome, "")
	return *outcome, nil
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
		r.metrics.reject(ctx, "fail")
		err := r.rejectionCause(claim)
		if errors.Is(err, errCancellationRequested) {
			_, err = r.handleInterruption(claim, err)
		}
		return err
	}
	r.metrics.terminal(ctx, "failed", code)
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
		r.metrics.reject(ctx, "supersede")
		err := r.rejectionCause(claim)
		if errors.Is(err, errCancellationRequested) {
			_, err = r.handleInterruption(claim, err)
		}
		return err
	}
	r.metrics.terminal(ctx, "superseded", "")
	return nil
}

func (r *CoverageRunner) rejectionCause(claim coverageClaim) error {
	// Rejection can mean either committed cancellation or lost ownership. Use
	// a fresh fenced read, never the old transaction's cancellation snapshot.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tx, err := r.beginAccount(ctx, claim.accountID)
	if err != nil {
		return err
	}
	defer r.rollback(tx)
	var requested *bool
	if err := tx.QueryRow(ctx, `SELECT app.job_cancel_requested($1,$2,$3)`,
		claim.jobID, r.workerID, claim.leaseID).Scan(&requested); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if requested != nil && *requested {
		return errCancellationRequested
	}
	return errLeaseLost
}

func (r *CoverageRunner) accountBool(ctx context.Context, account uuid.UUID, query string, args ...any) (value bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, cleanupTimeout)
	defer cancel()
	defer func() {
		if err != nil && ctx.Err() != nil {
			err = errors.Join(err, ctx.Err())
		}
	}()
	tx, err := r.beginAccount(ctx, account)
	if err != nil {
		return false, err
	}
	defer r.rollback(tx)
	if err := tx.QueryRow(ctx, query, args...).Scan(&value); err != nil {
		return false, err
	}
	return value, tx.Commit(ctx)
}

func (r *CoverageRunner) beginAccount(ctx context.Context, account uuid.UUID) (pgx.Tx, error) {
	tx, err := r.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, account.String()); err != nil {
		r.rollback(tx)
		return nil, err
	}
	return tx, nil
}

func (r *CoverageRunner) rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		r.metrics.control(ctx, "rollback", "", err)
		r.logger.Warn("coverage transaction rollback failed", "error", err)
	}
}

func verifyCoverageGenerations(ctx context.Context, snapshot routepipeline.Snapshot, targets []coverageTargetGeneration) error {
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

func sortedTargetGenerations(targets []coverageTargetGeneration) {
	sort.Slice(targets, func(i, j int) bool { return targets[i].RegionID < targets[j].RegionID })
}
