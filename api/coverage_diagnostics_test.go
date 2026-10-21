package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/config"
	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/erhhung/workouts-explorer/internal/coverage/readerplacement"
	"github.com/erhhung/workouts-explorer/internal/coverage/routepipeline"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
)

func TestCoverageDiagnosticServiceDisabledSkipsPool(t *testing.T) {
	service, err := newCoverageDiagnosticService(context.Background(), config.CoverageDiagnostics{OSMDatabaseURL: "invalid"})
	if err != nil || service.enabled || service.reader != nil || service.begin != nil || service.gate != nil {
		t.Fatalf("disabled diagnostics initialized a pool: service=%+v err=%v", service, err)
	}
}

func TestCoverageDiagnosticServicePreservesWhatIfRules(t *testing.T) {
	service, err := newCoverageDiagnosticService(context.Background(), config.CoverageDiagnostics{
		MinimumTraversalMeters: 19, MotorLaneWidthMeters: 4, BicycleLaneWidthMeters: 2,
		ParkingLaneWidthMeters: 3, SidewalkSetbackMeters: 7, DirectionalDriftMeters: 9, DeadEndEndpointAllowanceMeters: 13,
	})
	want := coverage.RoadGeometryRules{MotorLaneWidthMeters: 4, BicycleLaneWidthMeters: 2,
		ParkingLaneWidthMeters: 3, SidewalkSetbackMeters: 7, DirectionalDriftMeters: 9, DeadEndEndpointAllowanceMeters: 13}
	if err != nil || service.minimumTraversalMeters != 19 || service.roadGeometry != want || service.roadGeometry == coverage.DefaultRoadGeometryRules() {
		t.Fatalf("what-if configuration changed: service=%+v err=%v", service, err)
	}
}

func TestCoverageDiagnosticPoolErrorOmitsCredentials(t *testing.T) {
	const secret = "private-diagnostic-password"
	_, err := newCoverageDiagnosticService(context.Background(), config.CoverageDiagnostics{
		Enabled: true, Concurrency: 2, OSMDatabaseURL: "postgresql://user:" + secret + "@%zz",
	})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("invalid diagnostic configuration returned unsafe error: %v", err)
	}
}

func TestCoverageDiagnosticPoolExportsSafeNameAndConcurrencyLimit(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	previous := otel.GetMeterProvider()
	otel.SetMeterProvider(provider)
	defer func() {
		otel.SetMeterProvider(previous)
		_ = provider.Shutdown(context.Background())
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service, err := newCoverageDiagnosticService(ctx, config.CoverageDiagnostics{
		Enabled: true, Concurrency: 2,
		OSMDatabaseURL: "postgresql://user:private-password@private-host.invalid/private-database?pool_max_conns=7&application_name=private-app",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !service.enabled || service.reader == nil || service.begin != nil || cap(service.gate) != 2 {
		t.Fatalf("unexpected enabled service: %+v", service)
	}
	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &metrics); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private-password", "private-host", "private-database", "private-app"} {
		if strings.Contains(fmt.Sprint(metrics), forbidden) {
			t.Fatalf("diagnostic pool metrics expose %q", forbidden)
		}
	}
	for _, scope := range metrics.ScopeMetrics {
		for _, instrument := range scope.Metrics {
			if instrument.Name != "pgxpool.max_connections" {
				continue
			}
			gauge, ok := instrument.Data.(metricdata.Gauge[int64])
			if !ok || len(gauge.DataPoints) != 1 || gauge.DataPoints[0].Value != 2 {
				t.Fatalf("unexpected pool maximum: %+v", instrument)
			}
			name, ok := gauge.DataPoints[0].Attributes.Value(semconv.DBClientConnectionPoolNameKey)
			if !ok || name.AsString() != "workouts-api-coverage-diagnostics" {
				t.Fatalf("unexpected pool name: %v", name)
			}
			return
		}
	}
	t.Fatal("diagnostic pool maximum was not exported")
}

func TestDisabledCoverageDiagnosticsReturnsNotFoundWithoutOSM(t *testing.T) {
	called := false
	server := &Server{diagnostics: &coverageDiagnosticService{begin: func(context.Context) (coverageDiagnosticSnapshot, error) {
		called = true
		return nil, nil
	}}}
	request := httptest.NewRequest(http.MethodPost, "/api/workouts/00000000000000000000000000000000/coverage-diagnostic-runs", strings.NewReader(`{}`))
	response := httptest.NewRecorder()
	server.CreateCoverageDiagnosticRun(response, request, generated.WorkoutID("00000000000000000000000000000000"), generated.CreateCoverageDiagnosticRunParams{})
	if response.Code != http.StatusNotFound || called {
		t.Fatalf("status=%d osm_called=%t", response.Code, called)
	}
}

func TestDiagnosticMovementMode(t *testing.T) {
	for input, want := range map[string]string{"Outdoor Cycling": "bicycle", "Trail Running": "foot", "Other": "shared_public"} {
		if got := string(diagnosticMovementMode("", input)); got != want {
			t.Errorf("mode(%q)=%q want %q", input, got, want)
		}
	}
}

func TestDiagnosticResponseReportsCurrentPathPolicy(t *testing.T) {
	response := diagnosticResponse(diagnosticResult{unavailableRegions: []osm.MatcherUnavailableRegion{{RegionID: "geofabrik:new-york", DisplayName: "New York"}}})
	if response.PathPolicyVersion != generated.CoveragePathPolicyExperimentalV82 {
		t.Fatalf("path policy version=%q", response.PathPolicyVersion)
	}
	if response.Generations == nil {
		t.Fatal("no-evidence diagnostic must return an empty generation array, not null")
	}
	if len(response.UnavailableRegions) != 1 || response.UnavailableRegions[0].RegionId != "geofabrik:new-york" || response.UnavailableRegions[0].DisplayName != "New York" {
		t.Fatalf("unavailable regions=%+v", response.UnavailableRegions)
	}
}

func TestValidDiagnosticClipRejectsDegenerateGeometry(t *testing.T) {
	valid := osm.MatcherClippedPortion{LengthMeters: 4, GeoJSON: []byte(`{"type":"LineString","coordinates":[[-122,37],[-122.0001,37.0001]]}`)}
	if !validDiagnosticClip(valid) {
		t.Fatal("valid line rejected")
	}
	for _, clip := range []osm.MatcherClippedPortion{
		{LengthMeters: 0, GeoJSON: valid.GeoJSON},
		{LengthMeters: 4, GeoJSON: []byte(`{"type":"LineString","coordinates":[[-122,37],[-122,37]]}`)},
		{LengthMeters: 4, GeoJSON: []byte(`{"type":"Point","coordinates":[-122,37]}`)},
	} {
		if validDiagnosticClip(clip) {
			t.Fatalf("degenerate clip accepted: %+v", clip)
		}
	}
}

type diagnosticSnapshotFixture struct {
	generationErr  error
	candidateErr   error
	unavailableErr error
	unavailable    []osm.MatcherUnavailableRegion
	close          func(context.Context)
	closeErr       error
}

func (f *diagnosticSnapshotFixture) Generations(context.Context) ([]osm.MatcherGeneration, error) {
	return nil, f.generationErr
}

func (f *diagnosticSnapshotFixture) Candidates(context.Context, []osm.MatcherObservation, int) ([]osm.MatcherCandidate, error) {
	return nil, f.candidateErr
}

func (f *diagnosticSnapshotFixture) IncidentEdges(context.Context, []uuid.UUID, int) ([]osm.MatcherIncidentEdge, error) {
	return nil, nil
}

func (f *diagnosticSnapshotFixture) ClipPortions(context.Context, []osm.MatcherPortionRef) ([]osm.MatcherClippedPortion, error) {
	return nil, nil
}

func (f *diagnosticSnapshotFixture) UnavailableRegions(context.Context, []osm.MatcherObservation) ([]osm.MatcherUnavailableRegion, error) {
	return f.unavailable, f.unavailableErr
}

func (f *diagnosticSnapshotFixture) Close(ctx context.Context) error {
	if f.close != nil {
		f.close(ctx)
	}
	return f.closeErr
}

type diagnosticPhysicalSnapshotFixture struct {
	*diagnosticSnapshotFixture
	physicalClosed bool
}

func (f *diagnosticPhysicalSnapshotFixture) PhysicalConnectionClosed() bool {
	return f.physicalClosed
}

func TestDiagnosticSnapshotCleanupRequiresPhysicalConfirmation(t *testing.T) {
	failure := errors.New("fixture evaluation failure")
	for _, evaluated := range []bool{false, true} {
		for _, physicalClosed := range []bool{false, true} {
			for _, closeFailed := range []bool{false, true} {
				t.Run(fmt.Sprintf("evaluated_%t/physical_closed_%t/close_failed_%t", evaluated, physicalClosed, closeFailed), func(t *testing.T) {
					closed := false
					snapshot := &diagnosticPhysicalSnapshotFixture{
						diagnosticSnapshotFixture: &diagnosticSnapshotFixture{},
					}
					// Confirmation must be checked after Close, not before it.
					snapshot.close = func(context.Context) { closed, snapshot.physicalClosed = true, physicalClosed }
					if !evaluated {
						snapshot.generationErr = failure
					}
					if closeFailed {
						snapshot.closeErr = errors.New("fixture close failure")
					}
					server := &Server{diagnostics: &coverageDiagnosticService{
						minimumTraversalMeters: 5,
						begin:                  func(context.Context) (coverageDiagnosticSnapshot, error) { return snapshot, nil },
					}}
					result, err := server.evaluateCoverageDiagnostic(context.Background(), diagnosticWorkout{}, time.Now(), uuid.Nil, uuid.Nil, uuid.Nil)
					if !closed || errors.Is(err, readerplacement.ErrCleanupUnconfirmed) == physicalClosed {
						t.Fatalf("close=%t physically_closed=%t err=%v", closed, physicalClosed, err)
					}
					if !evaluated && !errors.Is(err, failure) {
						t.Fatalf("cleanup lost evaluation error: %v", err)
					}
					if !physicalClosed && result.runID != uuid.Nil {
						t.Fatal("unconfirmed cleanup returned a persistable diagnostic result")
					}
					if evaluated && physicalClosed && err != nil {
						t.Fatalf("confirmed physical closure prevents release: %v", err)
					}
				})
			}
		}
	}
}

func TestDiagnosticFailedBeginPreservesUnconfirmedCleanup(t *testing.T) {
	failure := errors.Join(context.Canceled, readerplacement.ErrCleanupUnconfirmed)
	server := &Server{diagnostics: &coverageDiagnosticService{
		begin: func(context.Context) (coverageDiagnosticSnapshot, error) { return nil, failure },
	}}
	_, err := server.evaluateCoverageDiagnostic(context.Background(), diagnosticWorkout{}, time.Now(), uuid.Nil, uuid.Nil, uuid.Nil)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, readerplacement.ErrCleanupUnconfirmed) {
		t.Fatalf("failed Begin lost its cleanup fence: %v", err)
	}
}

func TestDiagnosticInjectedSnapshotClosesOnEveryEvaluationOutcome(t *testing.T) {
	failure := errors.New("fixture evaluation failed")
	for _, stage := range []string{"success", "generations", "candidates", "unavailable", "canceled"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			closed := 0
			snapshot := &diagnosticSnapshotFixture{
				unavailable: []osm.MatcherUnavailableRegion{{RegionID: "fixture:region", DisplayName: "Unavailable"}},
				close: func(closeCtx context.Context) {
					closed++
					if closeCtx.Err() != nil {
						t.Errorf("cleanup inherits request cancellation: %v", closeCtx.Err())
					}
					if _, ok := closeCtx.Deadline(); !ok {
						t.Error("cleanup is unbounded")
					}
				},
			}
			switch stage {
			case "generations":
				snapshot.generationErr = failure
			case "candidates":
				snapshot.candidateErr = failure
			case "unavailable":
				snapshot.unavailableErr = failure
			case "canceled":
				cancel()
				snapshot.generationErr = context.Canceled
			}
			server := &Server{diagnostics: &coverageDiagnosticService{
				minimumTraversalMeters: 5,
				roadGeometry:           coverage.DefaultRoadGeometryRules(),
				begin:                  func(context.Context) (coverageDiagnosticSnapshot, error) { return snapshot, nil },
			}}
			// Injected snapshots still accept routes without a placement target vector.
			workout := diagnosticWorkout{mode: coverage.MovementFoot, points: []coverage.GeographicObservation{
				{Longitude: -122, Latitude: 37, Time: time.Now()},
				{Sequence: 1, Longitude: -122.001, Latitude: 37, Time: time.Now().Add(time.Minute)},
			}}
			result, err := server.evaluateCoverageDiagnostic(ctx, workout, time.Now(), uuid.Nil, uuid.Nil, uuid.Nil)
			if stage == "success" {
				if err != nil || result.counts.OriginalPoints != 2 || len(result.unavailableRegions) != 1 {
					t.Fatalf("injected diagnostic result=%+v err=%v", result, err)
				}
			} else {
				want := failure
				if stage == "canceled" {
					want = context.Canceled
				}
				if !errors.Is(err, want) {
					t.Fatalf("evaluation error=%v want %v", err, want)
				}
			}
			if closed != 1 {
				t.Fatalf("snapshot closed %d times", closed)
			}
		})
	}
}

func TestDiagnosticProductionRequiresTargetsWithoutLegacyFallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	service, err := newCoverageDiagnosticService(ctx, config.CoverageDiagnostics{
		Enabled: true, Concurrency: 1,
		OSMDatabaseURL: "postgresql://user:private-password@private-host.invalid/private-database",
	})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	service.begin = func(context.Context) (coverageDiagnosticSnapshot, error) {
		called = true
		return nil, errors.New("legacy fallback used")
	}
	server := &Server{diagnostics: service}
	_, err = server.evaluateCoverageDiagnostic(ctx, diagnosticWorkout{}, time.Now(), uuid.New(), uuid.New(), uuid.New())
	if !errors.Is(err, osm.ErrMatcherReplicaNotReady) || called {
		t.Fatalf("missing placement targets err=%v fallback=%t", err, called)
	}
	if status := diagnosticErrorStatus(ctx, err); status != http.StatusServiceUnavailable {
		t.Fatalf("not-ready status=%d", status)
	}
}

func TestDiagnosticTimeoutIsInconclusive(t *testing.T) {
	const canary = "raw-diagnostic-timeout-canary"
	for _, ctx := range []context.Context{context.Background(), func() context.Context {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		t.Cleanup(cancel)
		return ctx
	}()} {
		status := diagnosticErrorStatus(ctx, errors.Join(context.DeadlineExceeded, errors.New(canary)))
		response := httptest.NewRecorder()
		writeDiagnosticProblem(response, httptest.NewRequest(http.MethodPost, "/", nil), status)
		if response.Code != http.StatusGatewayTimeout || !strings.Contains(response.Body.String(), "inconclusive") || !strings.Contains(response.Body.String(), "does not indicate a production matching failure") || strings.Contains(response.Body.String(), canary) {
			t.Fatalf("timeout response=%d %s", response.Code, response.Body.String())
		}
	}
}

func TestDiagnosticProcessingLimitsTakePriorityOverDeadline(t *testing.T) {
	const canary = "raw-diagnostic-limit-canary"
	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	for name, cause := range map[string]error{
		"graph node":  &coverage.OSMEvaluationLimitError{Field: "graph node", Limit: 2048},
		"graph edge":  &coverage.OSMEvaluationLimitError{Field: "graph edge", Limit: 4096},
		"graph layer": &coverage.OSMEvaluationLimitError{Field: "graph layer", Limit: 64},
		"graph field": &coverage.OSMEvaluationLimitError{Field: canary, Limit: 1},
		"pipeline":    &routepipeline.LimitError{Field: canary, Limit: 12000},
		"bounds":      &osm.MatcherBoundsError{},
		"overflow":    &osm.MatcherOverflowError{},
	} {
		for contextName, ctx := range map[string]context.Context{"active": context.Background(), "expired": expired} {
			for errorName, err := range map[string]error{
				"direct":          cause,
				"wrapped":         fmt.Errorf("%s: %w", canary, cause),
				"joined deadline": fmt.Errorf("%s: %w", canary, errors.Join(context.DeadlineExceeded, cause)),
			} {
				t.Run(name+"/"+contextName+"/"+errorName, func(t *testing.T) {
					status := diagnosticErrorStatus(ctx, err)
					response := httptest.NewRecorder()
					writeDiagnosticProblem(response, httptest.NewRequest(http.MethodPost, "/", nil), status)
					if status != http.StatusRequestEntityTooLarge || response.Code != status || !strings.Contains(response.Body.String(), "exceeds the allowed bound") || strings.Contains(response.Body.String(), canary) {
						t.Fatalf("limit response=%d %s", response.Code, response.Body.String())
					}
				})
			}
		}
	}
}

func TestDiagnosticUnknownFailureDoesNotClaimTemporaryOrExposeCause(t *testing.T) {
	const canary = "raw-diagnostic-permanent-error-canary"
	status := diagnosticErrorStatus(context.Background(), fmt.Errorf("unsupported diagnostic input: %s", canary))
	response := httptest.NewRecorder()
	writeDiagnosticProblem(response, httptest.NewRequest(http.MethodPost, "/", nil), status)
	var problem generated.Problem
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	if status != http.StatusServiceUnavailable || response.Code != status || problem.Detail == nil || *problem.Detail != "coverage diagnostic could not be completed" || strings.Contains(response.Body.String(), "temporar") || strings.Contains(response.Body.String(), canary) {
		t.Fatalf("unknown failure response=%d %s", response.Code, response.Body.String())
	}
}

type diagnosticParitySnapshot struct{ diagnosticSnapshotFixture }

var diagnosticParitySegment = uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")

func (*diagnosticParitySnapshot) Candidates(_ context.Context, observations []osm.MatcherObservation, _ int) ([]osm.MatcherCandidate, error) {
	result := make([]osm.MatcherCandidate, len(observations))
	for i, observation := range observations {
		result[i] = osm.MatcherCandidate{
			ObservationIndex: i, SegmentID: diagnosticParitySegment, RegionID: "fixture:region", GenerationID: 1,
			SourceWayID: 10, SourceWayVersion: 1, DerivationVersion: 1, LogicalPathID: diagnosticParitySegment,
			StartGraphNodeID: uuid.MustParse("10000000-0000-0000-0000-000000000001"), EndGraphNodeID: uuid.MustParse("10000000-0000-0000-0000-000000000002"),
			Highway: "residential", BroadClass: "road", MotorForwardAllowed: true, MotorReverseAllowed: true,
			LengthMeters: 111.2, Fraction: observation.Longitude / 0.001, ProjectedLongitude: observation.Longitude,
			StartLongitude: 0, EndLongitude: 0.001,
		}
	}
	return result, nil
}

func (*diagnosticParitySnapshot) Generations(context.Context) ([]osm.MatcherGeneration, error) {
	return []osm.MatcherGeneration{{RegionID: "fixture:region", GenerationID: 1}}, nil
}

func (*diagnosticParitySnapshot) ClipPortions(_ context.Context, refs []osm.MatcherPortionRef) ([]osm.MatcherClippedPortion, error) {
	result := make([]osm.MatcherClippedPortion, len(refs))
	for i, ref := range refs {
		result[i] = osm.MatcherClippedPortion{Ordinal: i, SegmentID: ref.SegmentID, RegionID: ref.RegionID, GenerationID: ref.GenerationID,
			Direction: ref.Direction, LengthMeters: 111.2 * (ref.SourceToFraction - ref.SourceFromFraction),
			SourceFromFraction: ref.SourceFromFraction, SourceToFraction: ref.SourceToFraction,
			GeoJSON: []byte(`{"type":"LineString","coordinates":[[0.0002,0],[0.0008,0]]}`)}
	}
	return result, nil
}

func TestDiagnosticMatchesProductionPipelineWithSameRulesAndLimits(t *testing.T) {
	ctx := context.Background()
	workout := diagnosticWorkout{mode: coverage.MovementFoot, points: []coverage.GeographicObservation{
		{Sequence: 0, Longitude: 0.0002, Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), AccuracyMeters: 3},
		{Sequence: 1, Longitude: 0.0008, Time: time.Date(2026, 1, 1, 0, 0, 10, 0, time.UTC), AccuracyMeters: 3},
	}}
	server := &Server{diagnostics: &coverageDiagnosticService{
		minimumTraversalMeters: 5, roadGeometry: coverage.DefaultRoadGeometryRules(),
		begin: func(context.Context) (coverageDiagnosticSnapshot, error) { return &diagnosticParitySnapshot{}, nil },
	}}
	got, err := server.evaluateCoverageDiagnostic(ctx, workout, time.Now(), uuid.Nil, uuid.Nil, uuid.Nil)
	if err != nil {
		t.Fatal(err)
	}
	want, err := routepipeline.Evaluate(ctx, &diagnosticParitySnapshot{}, workout.points, routepipeline.Config{
		Rules: coverage.ExperimentalRules().WithMinimumTraversalMeters(5), Sampling: coverage.ExperimentalSamplingRules(),
		RoadGeometry: coverage.DefaultRoadGeometryRules(), MovementMode: workout.mode, Limits: routepipeline.DefaultLimits(),
	})
	if err != nil {
		t.Fatal(err)
	}
	counts := routepipeline.Counts{OriginalPoints: got.counts.OriginalPoints, SampledPoints: got.counts.SampledPoints,
		MatchedPoints: got.counts.MatchedPoints, AmbiguousPoints: got.counts.AmbiguousPoints, UnmatchedPoints: got.counts.UnmatchedPoints,
		RejectedPoints: got.counts.RejectedPoints, Traversals: got.counts.Traversals, Portions: got.counts.Portions, UniqueSegments: got.counts.UniqueSegments}
	if counts != want.Counts || len(got.evidence) == 0 || len(got.evidence) != len(want.Evidence) || !reflect.DeepEqual(got.generations, want.Generations) {
		t.Fatalf("diagnostic counts=%+v evidence=%d; production counts=%+v evidence=%d", counts, len(got.evidence), want.Counts, len(want.Evidence))
	}
	for i, evidence := range got.evidence {
		if !reflect.DeepEqual(evidence.portion, want.Evidence[i].Portion) || evidence.class != want.Evidence[i].Class || !reflect.DeepEqual(evidence.clipped, want.Evidence[i].Clipped) {
			t.Fatalf("evidence %d differs", i)
		}
	}
	server.diagnostics.minimumTraversalMeters = 100
	custom, err := server.evaluateCoverageDiagnostic(ctx, workout, time.Now(), uuid.Nil, uuid.Nil, uuid.Nil)
	if err != nil || len(custom.evidence) != 0 || diagnosticResponse(custom).MinimumTraversalMeters != 100 {
		t.Fatalf("what-if minimum was lost: evidence=%d minimum=%g err=%v", len(custom.evidence), custom.minimumTraversalMeters, err)
	}
}

func TestDiagnosticSharedSampledPointBoundary(t *testing.T) {
	for _, count := range []int{10001, 12000, 12001} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			workout := diagnosticWorkout{mode: coverage.MovementFoot, points: make([]coverage.GeographicObservation, count)}
			for i := range workout.points {
				workout.points[i] = coverage.GeographicObservation{Sequence: i, Longitude: float64(i) * 0.0001}
			}
			server := &Server{diagnostics: &coverageDiagnosticService{
				minimumTraversalMeters: 5, roadGeometry: coverage.DefaultRoadGeometryRules(),
				begin: func(context.Context) (coverageDiagnosticSnapshot, error) { return &diagnosticSnapshotFixture{}, nil },
			}}
			result, err := server.evaluateCoverageDiagnostic(context.Background(), workout, time.Now(), uuid.Nil, uuid.Nil, uuid.Nil)
			if count <= routepipeline.DefaultLimits().SampledPoints {
				if err != nil || result.counts.OriginalPoints != count || result.counts.SampledPoints != count {
					t.Fatalf("diagnostic rejected shared capacity: counts=%+v err=%v", result.counts, err)
				}
			} else {
				var limit *routepipeline.LimitError
				if !errors.As(err, &limit) || limit.Field != "sampled points" || limit.Limit != 12000 {
					t.Fatalf("sampled overflow error=%v", err)
				}
			}
		})
	}
}

func TestDiagnosticOpenAPIPointLimits(t *testing.T) {
	document, err := generated.GetSwagger()
	if err != nil {
		t.Fatal(err)
	}
	schema := document.Components.Schemas["CoverageDiagnosticCounts"].Value
	for _, test := range []struct {
		original, sampled int
		valid             bool
	}{{50000, 12000, true}, {50001, 12000, false}, {50000, 12001, false}} {
		data, err := json.Marshal(generated.CoverageDiagnosticCounts{OriginalPoints: test.original, SampledPoints: test.sampled})
		if err != nil {
			t.Fatal(err)
		}
		var value any
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		if err := schema.VisitJSON(value); (err == nil) != test.valid {
			t.Fatalf("OpenAPI %d/%d valid=%t: %v", test.original, test.sampled, test.valid, err)
		}
	}
}
