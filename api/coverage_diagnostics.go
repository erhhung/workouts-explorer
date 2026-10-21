package api

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/config"
	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/erhhung/workouts-explorer/internal/coverage/readerplacement"
	"github.com/erhhung/workouts-explorer/internal/coverage/routepipeline"
	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	diagnosticSlotDuration      = 2 * time.Minute
	diagnosticHeartbeatInterval = 20 * time.Second
)

type coverageDiagnosticSnapshot interface {
	Candidates(context.Context, []osm.MatcherObservation, int) ([]osm.MatcherCandidate, error)
	IncidentEdges(context.Context, []uuid.UUID, int) ([]osm.MatcherIncidentEdge, error)
	ClipPortions(context.Context, []osm.MatcherPortionRef) ([]osm.MatcherClippedPortion, error)
	Generations(context.Context) ([]osm.MatcherGeneration, error)
	UnavailableRegions(context.Context, []osm.MatcherObservation) ([]osm.MatcherUnavailableRegion, error)
	Close(context.Context) error
}

type coverageDiagnosticService struct {
	enabled bool
	timeout time.Duration
	gate    chan struct{}
	ownerID string
	reader  *readerplacement.Manager
	// begin is an injected snapshot factory for tests without reader placement.
	begin                  func(context.Context) (coverageDiagnosticSnapshot, error)
	minimumTraversalMeters float64
	roadGeometry           coverage.RoadGeometryRules
}

func newCoverageDiagnosticService(ctx context.Context, cfg config.CoverageDiagnostics) (*coverageDiagnosticService, error) {
	service := &coverageDiagnosticService{
		enabled: cfg.Enabled, timeout: cfg.Timeout, minimumTraversalMeters: cfg.MinimumTraversalMeters,
		ownerID: "coverage-diagnostics-" + uuid.NewString(),
		roadGeometry: coverage.RoadGeometryRules{
			MotorLaneWidthMeters: cfg.MotorLaneWidthMeters, BicycleLaneWidthMeters: cfg.BicycleLaneWidthMeters,
			ParkingLaneWidthMeters: cfg.ParkingLaneWidthMeters, SidewalkSetbackMeters: cfg.SidewalkSetbackMeters,
			DirectionalDriftMeters: cfg.DirectionalDriftMeters, DeadEndEndpointAllowanceMeters: cfg.DeadEndEndpointAllowanceMeters,
		},
	}
	if !cfg.Enabled {
		return service, nil
	}
	pool, err := database.OpenOSMReader(ctx, cfg.OSMDatabaseURL, "workouts-api-coverage-diagnostics", int32(cfg.Concurrency), cfg.UseReadReplicasOnly)
	if err != nil {
		return nil, fmt.Errorf("open coverage diagnostics OSM database pool: %w", err)
	}
	service.reader, err = readerplacement.New(pool, cfg.UseReadReplicasOnly, cfg.Concurrency)
	if err != nil {
		pool.Close()
		return nil, errors.New("initialize coverage diagnostics OSM reader placement")
	}
	go func() {
		<-ctx.Done()
		service.reader.Close()
		pool.Close()
	}()
	service.gate = make(chan struct{}, cfg.Concurrency)
	return service, nil
}

type diagnosticWorkout struct {
	id                uuid.UUID
	revision          int64
	digest            []byte
	typeKey           string
	providerLabel     string
	points            []coverage.GeographicObservation
	mode              coverage.MovementMode
	regionIDs         []string
	targetGenerations []osm.MatcherGeneration
}

type diagnosticEvidence struct {
	id      uuid.UUID
	portion coverage.TraversedPortion
	class   string
	clipped osm.MatcherClippedPortion
}

type diagnosticResult struct {
	runID                  uuid.UUID
	workout                diagnosticWorkout
	counts                 generated.CoverageDiagnosticCounts
	generations            []osm.MatcherGeneration
	evidence               []diagnosticEvidence
	unavailableRegions     []osm.MatcherUnavailableRegion
	createdAt              time.Time
	minimumTraversalMeters float64
}

func (s *Server) CreateCoverageDiagnosticRun(w http.ResponseWriter, r *http.Request, workoutID generated.WorkoutID, params generated.CreateCoverageDiagnosticRunParams) {
	if s.diagnostics == nil || !s.diagnostics.enabled {
		writeProblem(w, r, http.StatusNotFound, "Not Found", "resource was not found")
		return
	}
	session, ok := s.requireSession(w, r, "user")
	if !ok || !requireCSRF(w, r, session, params.XCSRFToken) {
		return
	}
	id, valid := parseCompactUUID(workoutID)
	if !valid {
		writeProblem(w, r, http.StatusBadRequest, "Bad Request", "workout ID is invalid")
		return
	}
	if !s.enterCoverageDiagnostics(r.Context()) {
		w.Header().Set("Retry-After", "1")
		writeProblem(w, r, http.StatusTooManyRequests, "Too Many Requests", "coverage diagnostic capacity is busy")
		return
	}
	defer func() { <-s.diagnostics.gate }()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(s.diagnostics.timeout + 5*time.Second))

	ctx, cancel := context.WithTimeout(r.Context(), s.diagnostics.timeout)
	defer cancel()
	started := time.Now()
	workout, status := s.loadDiagnosticWorkout(ctx, *session.accountID, id)
	if status != 0 {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status = http.StatusGatewayTimeout
		}
		writeDiagnosticProblem(w, r, status)
		return
	}
	if s.diagnostics.reader != nil && len(workout.targetGenerations) == 0 {
		writeDiagnosticProblem(w, r, http.StatusConflict)
		return
	}
	slotID, requestID := uuid.New(), uuid.New()
	acquired, err := s.acquireDiagnosticMatcherSlot(ctx, *session.accountID, workout.regionIDs, slotID, requestID)
	if err != nil {
		writeDiagnosticProblem(w, r, diagnosticErrorStatus(ctx, err))
		return
	}
	if !acquired {
		w.Header().Set("Retry-After", "1")
		writeProblem(w, r, http.StatusTooManyRequests, "Too Many Requests", "coverage diagnostic capacity is busy")
		return
	}
	stopHeartbeat := s.keepDiagnosticMatcherSlot(ctx, *session.accountID, slotID, requestID, cancel)
	releaseSlot := false
	defer func() {
		stopHeartbeat()
		if releaseSlot {
			s.releaseDiagnosticMatcherSlot(*session.accountID, slotID, requestID)
		}
	}()
	result, err := s.evaluateCoverageDiagnostic(ctx, workout, started, *session.accountID, slotID, requestID)
	// Unconfirmed physical cleanup keeps both reader assignment and admission
	// reserved until lease expiry; the manager can retry closing its connection.
	releaseSlot = !errors.Is(err, readerplacement.ErrCleanupUnconfirmed)
	if err != nil {
		status := diagnosticErrorStatus(ctx, err)
		slog.Warn("coverage diagnostic failed", "stage", "evaluate", "status", status)
		writeDiagnosticProblem(w, r, status)
		return
	}
	persisted, err := s.persistCoverageDiagnostic(ctx, *session.accountID, &result)
	if err != nil {
		status := diagnosticErrorStatus(ctx, err)
		slog.Warn("coverage diagnostic failed", "stage", "persist", "status", status)
		writeDiagnosticProblem(w, r, status)
		return
	}
	if !persisted {
		writeDiagnosticProblem(w, r, http.StatusConflict)
		return
	}
	writeJSON(w, http.StatusCreated, diagnosticResponse(result))
}

func (s *Server) enterCoverageDiagnostics(ctx context.Context) bool {
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case s.diagnostics.gate <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	case <-timer.C:
		return false
	}
}

func (s *Server) acquireDiagnosticMatcherSlot(ctx context.Context, accountID uuid.UUID, regions []string, slotID, requestID uuid.UUID) (bool, error) {
	tx, err := s.accountTransaction(ctx, accountID)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var acquired bool
	if err := tx.QueryRow(ctx, `SELECT app.acquire_diagnostic_matcher_slot($1,$2,$3,$4,$5,$6)`, accountID,
		s.diagnostics.ownerID, slotID, requestID, regions, diagnosticSlotDuration).Scan(&acquired); err != nil {
		return false, err
	}
	return acquired, tx.Commit(ctx)
}

func (s *Server) assignDiagnosticMatcherReader(ctx context.Context, accountID, slotID, requestID uuid.UUID, clusterKey string, keys []string) (string, error) {
	tx, err := s.accountTransaction(ctx, accountID)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	var chosen *string
	if err := tx.QueryRow(ctx, `SELECT app.assign_matcher_reader($1,$2,$3,$4,$5)`, slotID,
		s.diagnostics.ownerID, requestID, clusterKey, keys).Scan(&chosen); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	if chosen == nil {
		return "", nil
	}
	return *chosen, nil
}

func (s *Server) clearDiagnosticMatcherReader(ctx context.Context, accountID, slotID, requestID uuid.UUID) error {
	tx, err := s.accountTransaction(ctx, accountID)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var cleared bool
	if err := tx.QueryRow(ctx, `SELECT app.clear_matcher_reader($1,$2,$3)`, slotID,
		s.diagnostics.ownerID, requestID).Scan(&cleared); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Server) keepDiagnosticMatcherSlot(ctx context.Context, accountID, slotID, requestID uuid.UUID, cancelRequest context.CancelFunc) func() {
	// CPU evaluation and physical snapshot cleanup can outlive the request budget.
	heartbeatCtx, stop := context.WithCancel(context.WithoutCancel(ctx))
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(diagnosticHeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				attemptCtx, cancel := context.WithTimeout(heartbeatCtx, 2*time.Second)
				renewed, err := s.heartbeatDiagnosticMatcherSlot(attemptCtx, accountID, slotID, requestID)
				cancel()
				if err != nil || !renewed {
					cancelRequest()
				}
			}
		}
	}()
	return func() { stop(); <-done }
}

func (s *Server) heartbeatDiagnosticMatcherSlot(ctx context.Context, accountID, slotID, requestID uuid.UUID) (bool, error) {
	tx, err := s.accountTransaction(ctx, accountID)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var renewed bool
	if err := tx.QueryRow(ctx, `SELECT app.heartbeat_diagnostic_matcher_slot($1,$2,$3,$4)`, slotID,
		s.diagnostics.ownerID, requestID, diagnosticSlotDuration).Scan(&renewed); err != nil {
		return false, err
	}
	return renewed, tx.Commit(ctx)
}

func (s *Server) releaseDiagnosticMatcherSlot(accountID, slotID, requestID uuid.UUID) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	tx, err := s.accountTransaction(ctx, accountID)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)
	var released bool
	if err := tx.QueryRow(ctx, `SELECT app.release_matcher_slot($1,$2,$3)`, slotID,
		s.diagnostics.ownerID, requestID).Scan(&released); err == nil {
		_ = tx.Commit(ctx)
	}
}

func (s *Server) loadDiagnosticWorkout(ctx context.Context, accountID, workoutID uuid.UUID) (diagnosticWorkout, int) {
	tx, err := s.accountTransactionWithOptions(ctx, accountID, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return diagnosticWorkout{}, http.StatusServiceUnavailable
	}
	defer tx.Rollback(ctx)
	var result diagnosticWorkout
	result.id = workoutID
	var revision *int64
	var digest []byte
	var pointCount *int
	err = tx.QueryRow(ctx, `SELECT state.route_input_revision,state.route_input_sha256,route.point_count,
		type.type_key,type.provider_label,COALESCE((SELECT array_agg(region.region_id ORDER BY region.region_id)
		FROM app.workout_coverage_regions region WHERE region.account_id=workout.account_id AND region.workout_id=workout.id),'{}'::text[])
		FROM app.workouts workout
		JOIN app.workout_types type ON type.account_id=workout.account_id AND type.id=workout.workout_type_id
		LEFT JOIN app.workout_routes route ON route.account_id=workout.account_id AND route.workout_id=workout.id
		LEFT JOIN app.workout_coverage_states state ON state.account_id=workout.account_id AND state.workout_id=workout.id
		WHERE workout.account_id=$1 AND workout.id=$2 AND workout.deletion_requested_at IS NULL`, accountID, workoutID).
		Scan(&revision, &digest, &pointCount, &result.typeKey, &result.providerLabel, &result.regionIDs)
	if errors.Is(err, pgx.ErrNoRows) {
		return diagnosticWorkout{}, http.StatusNotFound
	}
	if err != nil {
		return diagnosticWorkout{}, http.StatusServiceUnavailable
	}
	if revision == nil || len(digest) != 32 || pointCount == nil || *pointCount < 1 {
		return diagnosticWorkout{}, http.StatusConflict
	}
	if *pointCount > routepipeline.OriginalPointLimit {
		return diagnosticWorkout{}, http.StatusRequestEntityTooLarge
	}
	result.revision, result.digest = *revision, append([]byte(nil), digest...)
	regionRows, err := tx.Query(ctx, `SELECT region_id,desired_osm_generation FROM app.workout_coverage_regions
		WHERE account_id=$1 AND workout_id=$2 ORDER BY region_id`, accountID, workoutID)
	if err != nil {
		return diagnosticWorkout{}, http.StatusServiceUnavailable
	}
	for regionRows.Next() {
		var target osm.MatcherGeneration
		if err := regionRows.Scan(&target.RegionID, &target.GenerationID); err != nil {
			regionRows.Close()
			return diagnosticWorkout{}, http.StatusServiceUnavailable
		}
		result.targetGenerations = append(result.targetGenerations, target)
	}
	regionRows.Close()
	if regionRows.Err() != nil {
		return diagnosticWorkout{}, http.StatusServiceUnavailable
	}
	rows, err := tx.Query(ctx, `SELECT sequence,longitude,latitude,recorded_at,COALESCE(horizontal_accuracy,0),course,course_accuracy
		FROM app.workout_route_points WHERE account_id=$1 AND workout_id=$2 ORDER BY sequence LIMIT $3`, accountID, workoutID, routepipeline.OriginalPointLimit+1)
	if err != nil {
		return diagnosticWorkout{}, http.StatusServiceUnavailable
	}
	defer rows.Close()
	for rows.Next() {
		var point coverage.GeographicObservation
		var recordedAt *time.Time
		if err := rows.Scan(&point.Sequence, &point.Longitude, &point.Latitude, &recordedAt, &point.AccuracyMeters, &point.HeadingDegrees, &point.HeadingAccuracy); err != nil {
			return diagnosticWorkout{}, http.StatusServiceUnavailable
		}
		if recordedAt != nil {
			point.Time = *recordedAt
		}
		result.points = append(result.points, point)
	}
	if rows.Err() != nil {
		return diagnosticWorkout{}, http.StatusServiceUnavailable
	}
	if len(result.points) != *pointCount {
		return diagnosticWorkout{}, http.StatusConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return diagnosticWorkout{}, http.StatusServiceUnavailable
	}
	result.mode = diagnosticMovementMode(result.typeKey, result.providerLabel)
	return result, 0
}

func diagnosticMovementMode(typeKey, providerLabel string) coverage.MovementMode {
	return routepipeline.MovementMode(typeKey, providerLabel)
}

func (s *Server) evaluateCoverageDiagnostic(ctx context.Context, workout diagnosticWorkout, started time.Time, accountID, slotID, requestID uuid.UUID) (result diagnosticResult, err error) {
	rules := coverage.ExperimentalRules().WithMinimumTraversalMeters(s.diagnostics.minimumTraversalMeters)
	var snapshot coverageDiagnosticSnapshot
	if s.diagnostics.reader != nil {
		if len(workout.targetGenerations) == 0 {
			return diagnosticResult{}, osm.ErrMatcherReplicaNotReady
		}
		snapshot, err = s.diagnostics.reader.Begin(ctx, workout.targetGenerations,
			func(ctx context.Context, clusterKey string, keys []string) (string, error) {
				return s.assignDiagnosticMatcherReader(ctx, accountID, slotID, requestID, clusterKey, keys)
			}, func(ctx context.Context) error {
				return s.clearDiagnosticMatcherReader(ctx, accountID, slotID, requestID)
			})
	} else if s.diagnostics.begin != nil {
		snapshot, err = s.diagnostics.begin(ctx)
	} else {
		err = errors.New("coverage diagnostic reader is unavailable")
	}
	if err != nil {
		return diagnosticResult{}, err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = snapshot.Close(closeCtx)
		confirmation, ok := snapshot.(interface{ PhysicalConnectionClosed() bool })
		if ok && !confirmation.PhysicalConnectionClosed() || !ok && s.diagnostics.reader != nil {
			result = diagnosticResult{}
			err = errors.Join(err, readerplacement.ErrCleanupUnconfirmed)
		}
	}()
	result = diagnosticResult{runID: uuid.Must(uuid.NewV7()), workout: workout, minimumTraversalMeters: rules.MinTraversalLengthMeters}
	evaluated, err := routepipeline.Evaluate(ctx, snapshot, workout.points, routepipeline.Config{
		Rules: rules, Sampling: coverage.ExperimentalSamplingRules(), RoadGeometry: s.diagnostics.roadGeometry,
		MovementMode: workout.mode, Limits: routepipeline.DefaultLimits(),
	})
	if err != nil {
		return diagnosticResult{}, err
	}
	result.counts = generated.CoverageDiagnosticCounts{
		OriginalPoints: evaluated.Counts.OriginalPoints, SampledPoints: evaluated.Counts.SampledPoints,
		MatchedPoints: evaluated.Counts.MatchedPoints, AmbiguousPoints: evaluated.Counts.AmbiguousPoints,
		UnmatchedPoints: evaluated.Counts.UnmatchedPoints, RejectedPoints: evaluated.Counts.RejectedPoints,
		Traversals: evaluated.Counts.Traversals, Portions: evaluated.Counts.Portions, UniqueSegments: evaluated.Counts.UniqueSegments,
		DurationMilliseconds: int(time.Since(started).Milliseconds()),
	}
	result.generations, result.unavailableRegions = evaluated.Generations, evaluated.UnavailableRegions
	for _, evidence := range evaluated.Evidence {
		result.evidence = append(result.evidence, diagnosticEvidence{id: uuid.Must(uuid.NewV7()), portion: evidence.Portion, class: evidence.Class, clipped: evidence.Clipped})
	}
	slog.Info("coverage diagnostic evaluation summary",
		"movement_mode", workout.mode, "windows", evaluated.Stats.Windows,
		"raw_candidates", evaluated.Stats.Candidates, "suppressed_candidates", evaluated.Stats.Suppressed,
		"graph_nodes", evaluated.Stats.GraphNodes, "graph_edges", evaluated.Stats.GraphEdges,
		"invalid_clips", evaluated.Stats.InvalidClips,
		"no_candidate_splits", evaluated.Stats.Splits[coverage.SplitNoCandidate],
		"network_splits", evaluated.Stats.Splits[coverage.SplitNetwork],
		"temporal_splits", evaluated.Stats.Splits[coverage.SplitTemporal],
		"spatial_splits", evaluated.Stats.Splits[coverage.SplitSpatial],
		"matched", result.counts.MatchedPoints, "ambiguous", result.counts.AmbiguousPoints,
		"unmatched", result.counts.UnmatchedPoints, "rejected", result.counts.RejectedPoints)
	return result, nil
}

func validDiagnosticClip(clip osm.MatcherClippedPortion) bool {
	return routepipeline.ValidClip(clip)
}

func diagnosticErrorStatus(ctx context.Context, err error) int {
	var limit *routepipeline.LimitError
	var evaluationLimit *coverage.OSMEvaluationLimitError
	var bounds *osm.MatcherBoundsError
	var overflow *osm.MatcherOverflowError
	// A positive processing-limit cause remains permanent even if its budget expires.
	if errors.As(err, &limit) || errors.As(err, &evaluationLimit) || errors.As(err, &bounds) || errors.As(err, &overflow) {
		return http.StatusRequestEntityTooLarge
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout
	}
	return http.StatusServiceUnavailable
}

func writeDiagnosticProblem(w http.ResponseWriter, r *http.Request, status int) {
	switch status {
	case http.StatusNotFound:
		writeProblem(w, r, status, "Not Found", "workout was not found")
	case http.StatusConflict:
		writeProblem(w, r, status, "Conflict", "workout route changed or is not ready for diagnostics")
	case http.StatusRequestEntityTooLarge:
		writeProblem(w, r, status, "Content Too Large", "workout or diagnostic evidence exceeds the allowed bound")
	case http.StatusGatewayTimeout:
		writeProblem(w, r, status, "Gateway Timeout", "coverage diagnostic timed out; result is inconclusive and does not indicate a production matching failure")
	default:
		writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "coverage diagnostic could not be completed")
	}
}

func (s *Server) persistCoverageDiagnostic(ctx context.Context, accountID uuid.UUID, result *diagnosticResult) (bool, error) {
	generations := make([]map[string]any, 0, len(result.generations))
	for _, generation := range result.generations {
		header := ""
		if generation.SourceHeaderTimestamp != nil {
			header = generation.SourceHeaderTimestamp.Format(time.RFC3339Nano)
		}
		generations = append(generations, map[string]any{"regionId": generation.RegionID, "generation": generation.GenerationID,
			"sourceUrl": generation.SourceURL, "sourceSha256": base64.StdEncoding.EncodeToString(generation.SourceSHA256),
			"sourceHeaderTimestamp": header, "importerVersion": generation.ImporterVersion,
			"derivationVersion": generation.DerivationVersion, "promotedAt": generation.PromotedAt.Format(time.RFC3339Nano)})
	}
	evidence := make([]map[string]any, 0, len(result.evidence))
	for ordinal, item := range result.evidence {
		locality := any(nil)
		if item.clipped.LocalityRelationID != nil {
			locality = *item.clipped.LocalityRelationID
		}
		evidence = append(evidence, map[string]any{"id": item.id, "ordinal": ordinal, "segmentId": item.clipped.SegmentID,
			"direction": item.clipped.Direction, "regionId": item.clipped.RegionID, "generation": item.clipped.GenerationID,
			"derivationVersion": item.clipped.DerivationVersion, "logicalPathId": item.clipped.LogicalPathID,
			"localityRelationId": locality, "sourceWayId": item.clipped.SourceWayID, "sourceWayVersion": item.clipped.SourceWayVersion,
			"sourceFromFraction": item.clipped.SourceFromFraction, "sourceToFraction": item.clipped.SourceToFraction,
			"traversedMeters": item.clipped.LengthMeters, "evidenceClass": item.class, "geometry": json.RawMessage(item.clipped.GeoJSON)})
	}
	generationJSON, err := json.Marshal(generations)
	if err != nil {
		return false, err
	}
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		return false, err
	}
	tx, err := s.accountTransaction(ctx, accountID)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	outcome := "evaluated"
	if len(result.evidence) == 0 {
		outcome = "no_evidence"
	}
	var persisted bool
	err = tx.QueryRow(ctx, `SELECT app.persist_coverage_diagnostic($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)`,
		accountID, result.runID, result.workout.id, result.workout.revision, result.workout.digest,
		coverage.ExperimentalRules().Version, coverage.ExperimentalSamplingRules().Version, coverage.ExperimentalPathPolicyV82,
		result.workout.mode, s.diagnostics.minimumTraversalMeters, outcome, result.counts.OriginalPoints, result.counts.SampledPoints, result.counts.MatchedPoints,
		result.counts.AmbiguousPoints, result.counts.UnmatchedPoints, result.counts.RejectedPoints, result.counts.Traversals,
		result.counts.Portions, result.counts.UniqueSegments, result.counts.DurationMilliseconds, generationJSON, evidenceJSON).Scan(&persisted)
	if err != nil || !persisted {
		return persisted, err
	}
	if err := tx.QueryRow(ctx, `SELECT created_at FROM app.coverage_diagnostic_runs WHERE account_id=$1 AND id=$2`, accountID, result.runID).Scan(&result.createdAt); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func diagnosticResponse(result diagnosticResult) generated.CoverageDiagnosticRun {
	response := generated.CoverageDiagnosticRun{
		Id: compactUUID(result.runID), WorkoutId: compactUUID(result.workout.id), RouteRevision: result.workout.revision,
		RulesVersion: generated.CoverageExperimentalV1, SamplingVersion: generated.CoverageSamplingExperimentalV1,
		PathPolicyVersion: generated.CoveragePathPolicyExperimentalV82, MovementMode: generated.CoverageDiagnosticRunMovementMode(result.workout.mode),
		MinimumTraversalMeters: result.minimumTraversalMeters, Counts: result.counts, CreatedAt: result.createdAt,
		Generations: make([]generated.CoverageDiagnosticGeneration, 0, len(result.generations)),
		Overlay:     generated.CoverageDiagnosticEvidenceCollection{Type: generated.CoverageDiagnosticEvidenceCollectionTypeFeatureCollection, Features: make([]generated.CoverageDiagnosticEvidenceFeature, 0, len(result.evidence))},
		Labels:      generated.CoverageDiagnosticLabels{Segments: []generated.CoverageDiagnosticSegmentLabel{}}, UnavailableRegions: []generated.CoverageDiagnosticUnavailableRegion{},
	}
	response.Outcome = generated.CoverageDiagnosticRunOutcomeEvaluated
	if len(result.evidence) == 0 {
		response.Outcome = generated.CoverageDiagnosticRunOutcomeNoEvidence
	}
	for _, generation := range result.generations {
		item := generated.CoverageDiagnosticGeneration{RegionId: generation.RegionID, Generation: generation.GenerationID,
			SourceUrl: generation.SourceURL, SourceSha256: hex.EncodeToString(generation.SourceSHA256), ImporterVersion: generation.ImporterVersion,
			DerivationVersion: generation.DerivationVersion, PromotedAt: generation.PromotedAt}
		if generation.SourceHeaderTimestamp != nil {
			item.SourceHeaderTimestamp.Set(*generation.SourceHeaderTimestamp)
		} else {
			item.SourceHeaderTimestamp.SetNull()
		}
		response.Generations = append(response.Generations, item)
	}
	for _, region := range result.unavailableRegions {
		response.UnavailableRegions = append(response.UnavailableRegions, generated.CoverageDiagnosticUnavailableRegion{RegionId: region.RegionID, DisplayName: region.DisplayName})
	}
	for ordinal, item := range result.evidence {
		var geometry generated.GeoJSONLineString
		_ = json.Unmarshal(item.clipped.GeoJSON, &geometry)
		response.Overlay.Features = append(response.Overlay.Features, generated.CoverageDiagnosticEvidenceFeature{
			Type: generated.CoverageDiagnosticEvidenceFeatureTypeFeature, Geometry: geometry,
			Properties: generated.CoverageDiagnosticEvidenceProperties{PortionOrdinal: ordinal, PhysicalSegmentId: compactUUID(item.clipped.SegmentID),
				Direction: generated.CoverageDiagnosticEvidencePropertiesDirection(item.clipped.Direction), RegionId: item.clipped.RegionID,
				Generation: item.clipped.GenerationID, TraversedMeters: item.clipped.LengthMeters,
				EvidenceClass: generated.CoverageDiagnosticEvidencePropertiesEvidenceClass(item.class)},
		})
	}
	return response
}

func (s *Server) UpdateCoverageDiagnosticLabels(w http.ResponseWriter, r *http.Request, runID generated.UUIDInput, params generated.UpdateCoverageDiagnosticLabelsParams) {
	if s.diagnostics == nil || !s.diagnostics.enabled {
		writeProblem(w, r, http.StatusNotFound, "Not Found", "resource was not found")
		return
	}
	session, ok := s.requireSession(w, r, "user")
	if !ok || !requireCSRF(w, r, session, params.XCSRFToken) {
		return
	}
	id, valid := parseCompactUUID(runID)
	if !valid {
		writeProblem(w, r, http.StatusBadRequest, "Bad Request", "diagnostic run ID is invalid")
		return
	}
	var patch generated.CoverageDiagnosticLabelsPatch
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		writeProblem(w, r, http.StatusBadRequest, "Bad Request", "request does not match the API contract")
		return
	}
	seen := make(map[int]struct{}, len(patch.Segments))
	for _, label := range patch.Segments {
		if _, duplicate := seen[label.PortionOrdinal]; duplicate {
			writeProblem(w, r, http.StatusBadRequest, "Bad Request", "segment labels must reference unique evidence portions")
			return
		}
		seen[label.PortionOrdinal] = struct{}{}
	}
	tx, err := s.accountTransaction(r.Context(), *session.accountID)
	if err != nil {
		writeDiagnosticProblem(w, r, http.StatusServiceUnavailable)
		return
	}
	defer tx.Rollback(r.Context())
	var exists bool
	if err := tx.QueryRow(r.Context(), `SELECT true FROM app.coverage_diagnostic_runs WHERE account_id=$1 AND id=$2`, *session.accountID, id).Scan(&exists); errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, http.StatusNotFound, "Not Found", "diagnostic run was not found")
		return
	} else if err != nil {
		writeDiagnosticProblem(w, r, http.StatusServiceUnavailable)
		return
	}
	encoded := make([]map[string]any, 0, len(patch.Segments))
	for _, label := range patch.Segments {
		var evidenceID uuid.UUID
		if err := tx.QueryRow(r.Context(), `SELECT id FROM app.coverage_diagnostic_evidence WHERE account_id=$1 AND run_id=$2 AND portion_ordinal=$3`, *session.accountID, id, label.PortionOrdinal).Scan(&evidenceID); errors.Is(err, pgx.ErrNoRows) {
			writeProblem(w, r, http.StatusBadRequest, "Bad Request", "segment label must reference diagnostic evidence")
			return
		} else if err != nil {
			writeDiagnosticProblem(w, r, http.StatusServiceUnavailable)
			return
		}
		encoded = append(encoded, map[string]any{"evidenceId": evidenceID, "label": label.Label})
	}
	segmentJSON, _ := json.Marshal(encoded)
	overall := ""
	if patch.Overall != nil {
		overall = string(*patch.Overall)
	}
	if _, err := tx.Exec(r.Context(), `SELECT app.set_coverage_diagnostic_labels($1,$2,$3,$4,$5)`, *session.accountID, id, patch.Overall != nil, overall, segmentJSON); err != nil {
		writeDiagnosticProblem(w, r, http.StatusServiceUnavailable)
		return
	}
	labels, err := queryCoverageDiagnosticLabels(r.Context(), tx, *session.accountID, id)
	if err != nil || tx.Commit(r.Context()) != nil {
		writeDiagnosticProblem(w, r, http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, labels)
}

func queryCoverageDiagnosticLabels(ctx context.Context, tx pgx.Tx, accountID, runID uuid.UUID) (generated.CoverageDiagnosticLabels, error) {
	result := generated.CoverageDiagnosticLabels{Segments: []generated.CoverageDiagnosticSegmentLabel{}}
	var overall string
	err := tx.QueryRow(ctx, `SELECT label FROM app.coverage_diagnostic_overall_labels WHERE account_id=$1 AND run_id=$2`, accountID, runID).Scan(&overall)
	if err == nil {
		result.Overall.Set(generated.CoverageDiagnosticLabelsOverall(overall))
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return result, err
	} else {
		result.Overall.SetNull()
	}
	rows, err := tx.Query(ctx, `SELECT evidence.portion_ordinal,label.label FROM app.coverage_diagnostic_segment_labels label
		JOIN app.coverage_diagnostic_evidence evidence ON evidence.account_id=label.account_id AND evidence.run_id=label.run_id AND evidence.id=label.evidence_id
		WHERE label.account_id=$1 AND label.run_id=$2 ORDER BY evidence.portion_ordinal`, accountID, runID)
	if err != nil {
		return result, err
	}
	defer rows.Close()
	for rows.Next() {
		var item generated.CoverageDiagnosticSegmentLabel
		if err := rows.Scan(&item.PortionOrdinal, &item.Label); err != nil {
			return result, err
		}
		result.Segments = append(result.Segments, item)
	}
	sort.Slice(result.Segments, func(i, j int) bool { return result.Segments[i].PortionOrdinal < result.Segments[j].PortionOrdinal })
	return result, rows.Err()
}
