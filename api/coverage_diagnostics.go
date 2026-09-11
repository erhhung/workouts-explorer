package api

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/config"
	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/erhhung/workouts-explorer/internal/coverage/evaluator"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	diagnosticOriginalPointLimit = 25000
	diagnosticSampledPointLimit  = 10000
	diagnosticSegmentLimit       = 4096
	diagnosticPortionLimit       = 10000
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
	enabled                bool
	timeout                time.Duration
	gate                   chan struct{}
	begin                  func(context.Context) (coverageDiagnosticSnapshot, error)
	minimumTraversalMeters float64
	roadGeometry           coverage.RoadGeometryRules
}

func newCoverageDiagnosticService(ctx context.Context, cfg config.CoverageDiagnostics) (*coverageDiagnosticService, error) {
	service := &coverageDiagnosticService{
		enabled: cfg.Enabled, timeout: cfg.Timeout, minimumTraversalMeters: cfg.MinimumTraversalMeters,
		roadGeometry: coverage.RoadGeometryRules{
			MotorLaneWidthMeters: cfg.MotorLaneWidthMeters, BicycleLaneWidthMeters: cfg.BicycleLaneWidthMeters,
			ParkingLaneWidthMeters: cfg.ParkingLaneWidthMeters, SidewalkSetbackMeters: cfg.SidewalkSetbackMeters,
			DirectionalDriftMeters: cfg.DirectionalDriftMeters, DeadEndEndpointAllowanceMeters: cfg.DeadEndEndpointAllowanceMeters,
		},
	}
	if !cfg.Enabled {
		return service, nil
	}
	poolConfig, err := pgxpool.ParseConfig(cfg.OSMDatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse coverage diagnostics OSM database configuration")
	}
	poolConfig.MaxConns = int32(cfg.Concurrency)
	poolConfig.ConnConfig.RuntimeParams["application_name"] = "workouts-api-coverage-diagnostics"
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("create coverage diagnostics OSM database pool")
	}
	go func() {
		<-ctx.Done()
		pool.Close()
	}()
	service.gate = make(chan struct{}, cfg.Concurrency)
	service.begin = func(ctx context.Context) (coverageDiagnosticSnapshot, error) {
		return osm.BeginMatcherSnapshot(ctx, pool)
	}
	return service, nil
}

type diagnosticWorkout struct {
	id            uuid.UUID
	revision      int64
	digest        []byte
	typeKey       string
	providerLabel string
	points        []coverage.GeographicObservation
	mode          coverage.MovementMode
}

type diagnosticEvidence struct {
	id      uuid.UUID
	portion coverage.TraversedPortion
	class   string
	clipped osm.MatcherClippedPortion
}

type diagnosticMatchedPortion struct {
	portion coverage.TraversedPortion
	class   string
	window  int
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
		writeDiagnosticProblem(w, r, status)
		return
	}
	result, err := s.evaluateCoverageDiagnostic(ctx, workout, started)
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
		type.type_key,type.provider_label FROM app.workouts workout
		JOIN app.workout_types type ON type.account_id=workout.account_id AND type.id=workout.workout_type_id
		LEFT JOIN app.workout_routes route ON route.account_id=workout.account_id AND route.workout_id=workout.id
		LEFT JOIN app.workout_coverage_states state ON state.account_id=workout.account_id AND state.workout_id=workout.id
		WHERE workout.account_id=$1 AND workout.id=$2 AND workout.deletion_requested_at IS NULL`, accountID, workoutID).
		Scan(&revision, &digest, &pointCount, &result.typeKey, &result.providerLabel)
	if errors.Is(err, pgx.ErrNoRows) {
		return diagnosticWorkout{}, http.StatusNotFound
	}
	if err != nil {
		return diagnosticWorkout{}, http.StatusServiceUnavailable
	}
	if revision == nil || len(digest) != 32 || pointCount == nil || *pointCount < 1 {
		return diagnosticWorkout{}, http.StatusConflict
	}
	if *pointCount > diagnosticOriginalPointLimit {
		return diagnosticWorkout{}, http.StatusRequestEntityTooLarge
	}
	result.revision, result.digest = *revision, append([]byte(nil), digest...)
	rows, err := tx.Query(ctx, `SELECT sequence,longitude,latitude,recorded_at,COALESCE(horizontal_accuracy,0),course,course_accuracy
		FROM app.workout_route_points WHERE account_id=$1 AND workout_id=$2 ORDER BY sequence LIMIT $3`, accountID, workoutID, diagnosticOriginalPointLimit+1)
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
	value := strings.ToLower(typeKey + " " + providerLabel)
	for _, token := range []string{"cycl", "bicycl", "bike", "biking"} {
		if strings.Contains(value, token) {
			return coverage.MovementBicycle
		}
	}
	for _, token := range []string{"walk", "run", "hik", "foot", "climb", "trek"} {
		if strings.Contains(value, token) {
			return coverage.MovementFoot
		}
	}
	return coverage.MovementSharedPublic
}

func (s *Server) evaluateCoverageDiagnostic(ctx context.Context, workout diagnosticWorkout, started time.Time) (diagnosticResult, error) {
	rules := coverage.ExperimentalRules().WithMinimumTraversalMeters(s.diagnostics.minimumTraversalMeters)
	sampling := coverage.ExperimentalSamplingRules()
	points := coverage.SampleGeographicObservations(workout.points, sampling, rules)
	if len(points) > diagnosticSampledPointLimit {
		return diagnosticResult{}, &diagnosticHTTPError{http.StatusRequestEntityTooLarge}
	}
	snapshot, err := s.diagnostics.begin(ctx)
	if err != nil {
		return diagnosticResult{}, err
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
		defer cancel()
		_ = snapshot.Close(closeCtx)
	}()
	allGenerations, err := snapshot.Generations(ctx)
	if err != nil {
		return diagnosticResult{}, err
	}
	result := diagnosticResult{runID: uuid.Must(uuid.NewV7()), workout: workout, minimumTraversalMeters: rules.MinTraversalLengthMeters}
	result.counts.OriginalPoints, result.counts.SampledPoints = len(workout.points), len(points)
	options := coverage.DefaultOSMEvaluationOptions()
	options.RoadGeometry = s.diagnostics.roadGeometry
	options.MaxCandidatesPerObservation = 16
	options.MovementMode = workout.mode
	options.Sampling.MinimumDistanceMeters = 0
	var portions []diagnosticMatchedPortion
	windowStarts := []int{}
	windowOrdinal := 0
	matcherStats := struct {
		windows, candidates, suppressed, graphNodes, graphEdges int
		splits                                                  map[coverage.SplitReason]int
	}{splits: make(map[coverage.SplitReason]int)}
	for start := 0; start < len(points); {
		windowStarts = append(windowStarts, start)
		end := min(start+evaluator.WindowSize, len(points))
		matched, stats, err := coverage.MatchOSM(ctx, snapshot, points[start:end], rules, options)
		if err != nil {
			return diagnosticResult{}, err
		}
		matcherStats.windows++
		matcherStats.candidates += stats.CandidateCount
		matcherStats.suppressed += stats.SuppressedPedestrianCandidates
		matcherStats.graphNodes += stats.GraphNodeCount
		matcherStats.graphEdges += stats.GraphEdgeCount
		for _, split := range matched.Splits {
			matcherStats.splits[split.Reason]++
		}
		skip := 0
		if start > 0 {
			skip = 1
		}
		for i := skip; i < len(matched.Observations); i++ {
			switch matched.Observations[i].Status {
			case coverage.ObservationMatched:
				result.counts.MatchedPoints++
			case coverage.ObservationAmbiguous:
				result.counts.AmbiguousPoints++
			case coverage.ObservationRejected:
				result.counts.RejectedPoints++
			default:
				result.counts.UnmatchedPoints++
			}
		}
		result.counts.Traversals += len(matched.Traversals)
		for _, traversal := range matched.Traversals {
			class := diagnosticTraversalClass(traversal)
			for _, portion := range traversal.Portions {
				portions = append(portions, diagnosticMatchedPortion{portion: portion, class: class, window: windowOrdinal})
			}
		}
		if end == len(points) {
			break
		}
		start = end - 1
		windowOrdinal++
	}
	portions, supplementalStats, err := completeCrossWindowConnectedRoadTurns(ctx, snapshot, points, windowStarts, portions, rules, options)
	if err != nil {
		return diagnosticResult{}, err
	}
	matcherStats.windows += supplementalStats.windows
	matcherStats.candidates += supplementalStats.candidates
	matcherStats.suppressed += supplementalStats.suppressed
	matcherStats.graphNodes += supplementalStats.graphNodes
	matcherStats.graphEdges += supplementalStats.graphEdges
	portions, tangentStats, err := replaceCrossWindowRoadTangents(ctx, snapshot, points, windowStarts, portions, rules, options)
	if err != nil {
		return diagnosticResult{}, err
	}
	matcherStats.windows += tangentStats.windows
	matcherStats.candidates += tangentStats.candidates
	matcherStats.suppressed += tangentStats.suppressed
	matcherStats.graphNodes += tangentStats.graphNodes
	matcherStats.graphEdges += tangentStats.graphEdges
	if len(portions) > diagnosticPortionLimit {
		return diagnosticResult{}, &diagnosticHTTPError{http.StatusRequestEntityTooLarge}
	}
	segments := make(map[uuid.UUID]struct{})
	uniqueRefs := make([]osm.MatcherPortionRef, 0, len(portions))
	refIndexes := make(map[osm.MatcherPortionRef]int, len(portions))
	portionRefs := make([]int, len(portions))
	for i, item := range portions {
		segments[item.portion.PhysicalSegmentID] = struct{}{}
		ref := osm.MatcherPortionRef{SegmentID: item.portion.PhysicalSegmentID, RegionID: item.portion.RegionID,
			GenerationID: item.portion.GenerationID, Direction: osm.MatcherDirection(item.portion.Direction),
			SourceFromFraction: item.portion.SourceFromFraction, SourceToFraction: item.portion.SourceToFraction}
		index, exists := refIndexes[ref]
		if !exists {
			index = len(uniqueRefs)
			refIndexes[ref] = index
			uniqueRefs = append(uniqueRefs, ref)
		}
		portionRefs[i] = index
	}
	if len(segments) > diagnosticSegmentLimit {
		return diagnosticResult{}, &diagnosticHTTPError{http.StatusRequestEntityTooLarge}
	}
	clipped := make([]osm.MatcherClippedPortion, len(uniqueRefs))
	for start := 0; start < len(uniqueRefs); start += diagnosticSegmentLimit {
		end := min(start+diagnosticSegmentLimit, len(uniqueRefs))
		batch, err := snapshot.ClipPortions(ctx, uniqueRefs[start:end])
		if err != nil {
			return diagnosticResult{}, err
		}
		for _, item := range batch {
			item.Ordinal += start
			clipped[item.Ordinal] = item
		}
	}
	usedGenerations := make(map[string]struct{})
	clear(segments)
	invalidClips := 0
	for i, item := range portions {
		clip := clipped[portionRefs[i]]
		if !validDiagnosticClip(clip) {
			invalidClips++
			continue
		}
		result.evidence = append(result.evidence, diagnosticEvidence{id: uuid.Must(uuid.NewV7()), portion: item.portion, class: item.class, clipped: clip})
		segments[clip.SegmentID] = struct{}{}
		usedGenerations[fmt.Sprintf("%s\x00%d", clip.RegionID, clip.GenerationID)] = struct{}{}
	}
	for _, generation := range allGenerations {
		if _, used := usedGenerations[fmt.Sprintf("%s\x00%d", generation.RegionID, generation.GenerationID)]; used {
			result.generations = append(result.generations, generation)
		}
	}
	if len(result.generations) != len(usedGenerations) {
		return diagnosticResult{}, errors.New("OSM generation provenance unavailable")
	}
	if len(result.generations) > 256 {
		return diagnosticResult{}, &diagnosticHTTPError{http.StatusRequestEntityTooLarge}
	}
	if len(result.evidence) == 0 {
		regionPoints := points
		if len(regionPoints) > 256 {
			sampled := make([]coverage.GeographicObservation, 256)
			for i := range sampled {
				sampled[i] = regionPoints[i*(len(regionPoints)-1)/(len(sampled)-1)]
			}
			regionPoints = sampled
		}
		matcherPoints := make([]osm.MatcherObservation, len(regionPoints))
		for i, point := range regionPoints {
			matcherPoints[i] = osm.MatcherObservation{Longitude: point.Longitude, Latitude: point.Latitude}
		}
		result.unavailableRegions, err = snapshot.UnavailableRegions(ctx, matcherPoints)
		if err != nil {
			return diagnosticResult{}, err
		}
		if len(result.unavailableRegions) > 64 {
			return diagnosticResult{}, &diagnosticHTTPError{http.StatusRequestEntityTooLarge}
		}
	}
	result.counts.Portions, result.counts.UniqueSegments = len(result.evidence), len(segments)
	result.counts.DurationMilliseconds = int(time.Since(started).Milliseconds())
	slog.Info("coverage diagnostic evaluation summary",
		"movement_mode", workout.mode, "windows", matcherStats.windows,
		"raw_candidates", matcherStats.candidates, "suppressed_candidates", matcherStats.suppressed,
		"graph_nodes", matcherStats.graphNodes, "graph_edges", matcherStats.graphEdges,
		"invalid_clips", invalidClips,
		"no_candidate_splits", matcherStats.splits[coverage.SplitNoCandidate],
		"network_splits", matcherStats.splits[coverage.SplitNetwork],
		"temporal_splits", matcherStats.splits[coverage.SplitTemporal],
		"spatial_splits", matcherStats.splits[coverage.SplitSpatial],
		"matched", result.counts.MatchedPoints, "ambiguous", result.counts.AmbiguousPoints,
		"unmatched", result.counts.UnmatchedPoints, "rejected", result.counts.RejectedPoints)
	return result, nil
}

func validDiagnosticClip(clip osm.MatcherClippedPortion) bool {
	if clip.LengthMeters <= 0 || math.IsNaN(clip.LengthMeters) || math.IsInf(clip.LengthMeters, 0) {
		return false
	}
	var geometry struct {
		Type        string      `json:"type"`
		Coordinates [][]float64 `json:"coordinates"`
	}
	if json.Unmarshal(clip.GeoJSON, &geometry) != nil || geometry.Type != "LineString" || len(geometry.Coordinates) < 2 {
		return false
	}
	first := geometry.Coordinates[0]
	if len(first) < 2 || math.IsNaN(first[0]) || math.IsNaN(first[1]) || math.IsInf(first[0], 0) || math.IsInf(first[1], 0) {
		return false
	}
	for _, coordinate := range geometry.Coordinates[1:] {
		if len(coordinate) >= 2 && !math.IsNaN(coordinate[0]) && !math.IsNaN(coordinate[1]) && !math.IsInf(coordinate[0], 0) && !math.IsInf(coordinate[1], 0) &&
			(coordinate[0] != first[0] || coordinate[1] != first[1]) {
			return true
		}
	}
	return false
}

func completeCrossWindowConnectedRoadTurns(ctx context.Context, snapshot coverageDiagnosticSnapshot, points []coverage.GeographicObservation, windowStarts []int, portions []diagnosticMatchedPortion, rules coverage.Rules, options coverage.OSMEvaluationOptions) ([]diagnosticMatchedPortion, struct{ windows, candidates, suppressed, graphNodes, graphEdges int }, error) {
	stats := struct{ windows, candidates, suppressed, graphNodes, graphEdges int }{}
	for i := 0; i+1 < len(portions); i++ {
		before, after := portions[i], portions[i+1]
		if before.window == after.window || after.window < 0 || after.window >= len(windowStarts) ||
			before.portion.ContinuityClass != "road" || after.portion.ContinuityClass != "road" ||
			before.portion.LogicalPathID == "" || before.portion.LogicalPathID == after.portion.LogicalPathID ||
			before.portion.ToMeter-before.portion.FromMeter < 20 || after.portion.ToMeter-after.portion.FromMeter < 20 ||
			!directedPortionEndIsClipped(before.portion) || !directedPortionStartIsClipped(after.portion) {
			continue
		}
		start := max(0, windowStarts[after.window]-evaluator.WindowSize/2)
		end := min(len(points), start+evaluator.WindowSize)
		start = max(0, end-evaluator.WindowSize)
		rematched, matchStats, err := coverage.MatchOSM(ctx, snapshot, points[start:end], rules, options)
		if err != nil {
			return nil, stats, err
		}
		stats.windows++
		stats.candidates += matchStats.CandidateCount
		stats.suppressed += matchStats.SuppressedPedestrianCandidates
		stats.graphNodes += matchStats.GraphNodeCount
		stats.graphEdges += matchStats.GraphEdgeCount
		flat := []coverage.TraversedPortion{}
		for _, traversal := range rematched.Traversals {
			flat = append(flat, traversal.Portions...)
		}
		for j := 0; j+1 < len(flat); j++ {
			newBefore, newAfter := flat[j], flat[j+1]
			if newBefore.PhysicalSegmentID != before.portion.PhysicalSegmentID || newBefore.Direction != before.portion.Direction ||
				newAfter.PhysicalSegmentID != after.portion.PhysicalSegmentID || newAfter.Direction != after.portion.Direction ||
				newBefore.SourceFromFraction > before.portion.SourceFromFraction+1e-6 || newBefore.SourceToFraction < before.portion.SourceToFraction-1e-6 ||
				newAfter.SourceFromFraction > after.portion.SourceFromFraction+1e-6 || newAfter.SourceToFraction < after.portion.SourceToFraction-1e-6 {
				continue
			}
			added := (newBefore.ToMeter - newBefore.FromMeter) + (newAfter.ToMeter - newAfter.FromMeter) -
				(before.portion.ToMeter - before.portion.FromMeter) - (after.portion.ToMeter - after.portion.FromMeter)
			if added <= 1e-6 || added > 35 {
				continue
			}
			portions[i].portion, portions[i+1].portion = newBefore, newAfter
			break
		}
	}
	return portions, stats, nil
}

func replaceCrossWindowRoadTangents(ctx context.Context, snapshot coverageDiagnosticSnapshot, points []coverage.GeographicObservation, windowStarts []int, portions []diagnosticMatchedPortion, rules coverage.Rules, options coverage.OSMEvaluationOptions) ([]diagnosticMatchedPortion, struct{ windows, candidates, suppressed, graphNodes, graphEdges int }, error) {
	stats := struct{ windows, candidates, suppressed, graphNodes, graphEdges int }{}
	present := make(map[uuid.UUID]bool)
	for _, item := range portions {
		present[item.portion.PhysicalSegmentID] = true
	}
	for i := 1; i+1 < len(portions); i++ {
		before, selected, after := portions[i-1], portions[i], portions[i+1]
		if before.window != selected.window || selected.window == after.window || after.window < 0 || after.window >= len(windowStarts) ||
			before.portion.ContinuityClass != "road" || selected.portion.ContinuityClass != "road" || after.portion.ContinuityClass != "road" ||
			before.portion.LogicalPathID == "" || selected.portion.LogicalPathID == "" || after.portion.LogicalPathID == "" ||
			before.portion.LogicalPathID == after.portion.LogicalPathID || selected.portion.LogicalPathID == after.portion.LogicalPathID ||
			selected.portion.SourceFromFraction > 1e-6 || selected.portion.SourceToFraction < 1-1e-6 ||
			selected.portion.ToMeter-selected.portion.FromMeter < 20 || selected.portion.ToMeter-selected.portion.FromMeter > 60 {
			continue
		}
		const supplementalWindowSize = 192
		start := max(0, windowStarts[after.window]-128)
		end := min(len(points), start+supplementalWindowSize)
		start = max(0, end-supplementalWindowSize)
		rematched, matchStats, err := coverage.MatchOSM(ctx, snapshot, points[start:end], rules, options)
		if err != nil {
			return nil, stats, err
		}
		stats.windows++
		stats.candidates += matchStats.CandidateCount
		stats.suppressed += matchStats.SuppressedPedestrianCandidates
		stats.graphNodes += matchStats.GraphNodeCount
		stats.graphEdges += matchStats.GraphEdgeCount
		flat := []coverage.TraversedPortion{}
		for _, traversal := range rematched.Traversals {
			flat = append(flat, traversal.Portions...)
		}
		replaced := false
		for left := 0; left < len(flat) && !replaced; left++ {
			if flat[left].PhysicalSegmentID != before.portion.PhysicalSegmentID || flat[left].Direction != before.portion.Direction {
				continue
			}
			for right := left + 3; right < len(flat) && right <= left+6; right++ {
				if flat[right].PhysicalSegmentID != after.portion.PhysicalSegmentID || flat[right].Direction != after.portion.Direction {
					continue
				}
				var replacement []diagnosticMatchedPortion
				portions, replacement = replaceCrossWindowTangent(portions, i, flat[left+1:right], flat[right], before, selected, after, present)
				if len(replacement) == 0 {
					continue
				}
				i += len(replacement) - 1
				replaced = true
				break
			}
		}
		if replaced {
			continue
		}
	}
	return portions, stats, nil
}

func replaceCrossWindowTangent(portions []diagnosticMatchedPortion, index int, middle []coverage.TraversedPortion, supplementalAfter coverage.TraversedPortion, before, selected, after diagnosticMatchedPortion, present map[uuid.UUID]bool) ([]diagnosticMatchedPortion, []diagnosticMatchedPortion) {
	distance := 0.0
	for i, portion := range middle {
		distance += portion.ToMeter - portion.FromMeter
		selectedTurnaround := len(middle) >= 2 && i < 2 && portion.PhysicalSegmentID == selected.portion.PhysicalSegmentID &&
			middle[0].Direction == selected.portion.Direction && middle[1].Direction != selected.portion.Direction
		if portion.ContinuityClass != "road" || !selectedTurnaround && (portion.PhysicalSegmentID == selected.portion.PhysicalSegmentID || present[portion.PhysicalSegmentID]) {
			return portions, nil
		}
	}
	if len(middle) < 2 || len(middle) > 5 || distance > 500 {
		return portions, nil
	}
	if selected.portion.LogicalPathID == before.portion.LogicalPathID && selected.portion.SourceFromFraction <= 1e-6 && selected.portion.SourceToFraction >= 1-1e-6 {
		returned := selected.portion
		if returned.Direction == coverage.SegmentForward {
			returned.Direction = coverage.SegmentReverse
		} else {
			returned.Direction = coverage.SegmentForward
		}
		middle = append([]coverage.TraversedPortion{selected.portion, returned}, middle...)
		distance += 2 * (selected.portion.ToMeter - selected.portion.FromMeter)
	}
	if len(middle) > 5 || distance > 500 {
		return portions, nil
	}
	replacement := make([]diagnosticMatchedPortion, len(middle))
	for i, portion := range middle {
		replacement[i] = diagnosticMatchedPortion{portion: portion, class: selected.class, window: after.window}
		present[portion.PhysicalSegmentID] = true
	}
	portions = append(portions, make([]diagnosticMatchedPortion, len(replacement)-1)...)
	copy(portions[index+len(replacement):], portions[index+1:len(portions)-len(replacement)+1])
	copy(portions[index:], replacement)
	afterIndex := index + len(replacement)
	if supplementalAfter.PhysicalSegmentID == portions[afterIndex].portion.PhysicalSegmentID && supplementalAfter.Direction == portions[afterIndex].portion.Direction {
		merged := portions[afterIndex].portion
		merged.FromMeter = math.Min(merged.FromMeter, supplementalAfter.FromMeter)
		merged.ToMeter = math.Max(merged.ToMeter, supplementalAfter.ToMeter)
		merged.SourceFromFraction = math.Min(merged.SourceFromFraction, supplementalAfter.SourceFromFraction)
		merged.SourceToFraction = math.Max(merged.SourceToFraction, supplementalAfter.SourceToFraction)
		portions[afterIndex].portion = merged
	}
	return portions, replacement
}

func directedPortionEndIsClipped(portion coverage.TraversedPortion) bool {
	if portion.Direction == coverage.SegmentReverse {
		return portion.SourceFromFraction > 1e-6
	}
	return portion.SourceToFraction < 1-1e-6
}

func directedPortionStartIsClipped(portion coverage.TraversedPortion) bool {
	if portion.Direction == coverage.SegmentReverse {
		return portion.SourceToFraction < 1-1e-6
	}
	return portion.SourceFromFraction > 1e-6
}

func diagnosticTraversalClass(traversal coverage.DecodedTraversal) string {
	for _, observation := range traversal.Observations {
		if observation.Status == coverage.ObservationAmbiguous {
			return "ambiguous"
		}
	}
	return "matched"
}

type diagnosticHTTPError struct{ status int }

func (e *diagnosticHTTPError) Error() string { return http.StatusText(e.status) }

func diagnosticErrorStatus(ctx context.Context, err error) int {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return http.StatusGatewayTimeout
	}
	var status *diagnosticHTTPError
	if errors.As(err, &status) {
		return status.status
	}
	var bounds *osm.MatcherBoundsError
	var overflow *osm.MatcherOverflowError
	if errors.As(err, &bounds) || errors.As(err, &overflow) {
		return http.StatusRequestEntityTooLarge
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
		writeProblem(w, r, status, "Gateway Timeout", "coverage diagnostic timed out")
	default:
		writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "coverage diagnostic is temporarily unavailable")
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
		Overlay: generated.CoverageDiagnosticEvidenceCollection{Type: generated.FeatureCollection, Features: make([]generated.CoverageDiagnosticEvidenceFeature, 0, len(result.evidence))},
		Labels:  generated.CoverageDiagnosticLabels{Segments: []generated.CoverageDiagnosticSegmentLabel{}}, UnavailableRegions: []generated.CoverageDiagnosticUnavailableRegion{},
	}
	response.Outcome = generated.Evaluated
	if len(result.evidence) == 0 {
		response.Outcome = generated.NoEvidence
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
