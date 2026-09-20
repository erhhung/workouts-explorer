// Package evaluator runs the manual, aggregate-only real-route coverage evaluation.
package evaluator

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/erhhung/workouts-explorer/internal/coverage/routepipeline"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
)

const (
	WindowSize       = routepipeline.WindowSize
	workoutTimeout   = 2 * time.Minute
	outcomeEvaluated = "evaluated"
	outcomeEmpty     = "no_observations"
	outcomeError     = "error"
)

type Config struct {
	Limit                  int     `json:"workout_limit"`
	MinimumTraversalMeters float64 `json:"minimum_traversal_meters"`
}

type Workout struct {
	ID                     uuid.UUID
	TypeKey, ProviderLabel string
}

type Source interface {
	ResolveAccount(context.Context, string) (uuid.UUID, error)
	Workouts(context.Context, uuid.UUID, int) ([]Workout, error)
	Points(context.Context, uuid.UUID, uuid.UUID) ([]coverage.GeographicObservation, error)
}

type Topology interface {
	Candidates(context.Context, []osm.MatcherObservation, int) ([]osm.MatcherCandidate, error)
	IncidentEdges(context.Context, []uuid.UUID, int) ([]osm.MatcherIncidentEdge, error)
}

type Snapshot interface {
	Topology
	Close(context.Context) error
}

type SnapshotFactory interface {
	Begin(context.Context) (Snapshot, error)
}

type WindowEvaluator interface {
	Evaluate(context.Context, Topology, []coverage.GeographicObservation, coverage.Rules, coverage.OSMEvaluationOptions) (coverage.Result, coverage.OSMEvaluationStats, error)
}

type Matcher struct{}

func (Matcher) Evaluate(ctx context.Context, topology Topology, observations []coverage.GeographicObservation, rules coverage.Rules, options coverage.OSMEvaluationOptions) (coverage.Result, coverage.OSMEvaluationStats, error) {
	return coverage.MatchOSM(ctx, topology, observations, rules, options)
}

type Measure struct {
	Total int `json:"total"`
	Max   int `json:"max"`
}

type ObservationCounts struct {
	Original  int `json:"original"`
	Sampled   int `json:"sampled"`
	Matched   int `json:"matched"`
	Ambiguous int `json:"ambiguous"`
	Unmatched int `json:"unmatched"`
	Rejected  int `json:"rejected"`
}

type WorkoutCounts struct {
	Total            int            `json:"total"`
	ByOutcome        map[string]int `json:"by_outcome"`
	ByMode           map[string]int `json:"by_mode"`
	ByOutcomeAndMode map[string]int `json:"by_outcome_and_mode"`
}

type MatchCounts struct {
	Candidates      Measure `json:"candidates"`
	GraphNodes      Measure `json:"graph_nodes"`
	GraphEdges      Measure `json:"graph_edges"`
	ExpansionLayers Measure `json:"expansion_layers"`
	Traversals      int     `json:"traversals"`
	Portions        int     `json:"portions"`
	UniqueSegments  int     `json:"unique_public_physical_segments"`
}

type Durations struct {
	TotalMilliseconds    int64 `json:"total_ms"`
	DatabaseMilliseconds int64 `json:"database_ms"`
	SamplingMilliseconds int64 `json:"sampling_ms"`
	MatchingMilliseconds int64 `json:"matching_ms"`
}

type Report struct {
	RulesVersion    coverage.RulesVersion    `json:"rules_version"`
	SamplingVersion coverage.SamplingVersion `json:"sampling_version"`
	Config          Config                   `json:"config"`
	Workouts        WorkoutCounts            `json:"workouts"`
	Observations    ObservationCounts        `json:"observations"`
	Matching        MatchCounts              `json:"matching"`
	Errors          map[string]int           `json:"errors_by_category"`
	Durations       Durations                `json:"durations"`
	segmentIDs      map[string]struct{}
}

type Runner struct {
	Source    Source
	Snapshots SnapshotFactory
	Windows   WindowEvaluator
	Now       func() time.Time
}

func NewReport(cfg Config) Report {
	rules := coverage.ExperimentalRules().WithMinimumTraversalMeters(cfg.MinimumTraversalMeters)
	return Report{
		RulesVersion: rules.Version, SamplingVersion: coverage.ExperimentalSamplingRules().Version, Config: cfg,
		Workouts: WorkoutCounts{ByOutcome: make(map[string]int), ByMode: make(map[string]int), ByOutcomeAndMode: make(map[string]int)},
		Errors:   make(map[string]int), segmentIDs: make(map[string]struct{}),
	}
}

func (r Runner) Run(ctx context.Context, identity string, cfg Config) (report Report, runErr error) {
	rules := coverage.ExperimentalRules().WithMinimumTraversalMeters(cfg.MinimumTraversalMeters)
	sampling := coverage.ExperimentalSamplingRules()
	report = NewReport(cfg)
	started := r.now()
	defer func() { report.Durations.TotalMilliseconds = r.now().Sub(started).Milliseconds() }()

	dbStarted := r.now()
	accountID, err := r.Source.ResolveAccount(ctx, identity)
	report.Durations.DatabaseMilliseconds += r.now().Sub(dbStarted).Milliseconds()
	if err != nil {
		category := classify(err, "account_database")
		report.Errors[category]++
		return report, &SafeError{Category: category}
	}
	dbStarted = r.now()
	workouts, err := r.Source.Workouts(ctx, accountID, cfg.Limit)
	report.Durations.DatabaseMilliseconds += r.now().Sub(dbStarted).Milliseconds()
	if err != nil {
		report.Errors["application_database"]++
		return report, &SafeError{Category: "application_database"}
	}
	for _, workout := range workouts {
		r.runWorkout(ctx, accountID, workout, rules, sampling, &report)
	}
	report.Matching.UniqueSegments = len(report.segmentIDs)
	return report, nil
}

func (r Runner) runWorkout(parent context.Context, accountID uuid.UUID, workout Workout, rules coverage.Rules, sampling coverage.SamplingRules, report *Report) {
	ctx, cancel := context.WithTimeout(parent, workoutTimeout)
	defer cancel()
	mode := routepipeline.MovementMode(workout.TypeKey, workout.ProviderLabel)
	dbStarted := r.now()
	points, err := r.Source.Points(ctx, accountID, workout.ID)
	report.Durations.DatabaseMilliseconds += r.now().Sub(dbStarted).Milliseconds()
	if err != nil {
		r.recordError(report, mode, classifyContext(ctx, "application_database"))
		return
	}
	report.Observations.Original += len(points)
	if len(points) == 0 {
		recordOutcome(report, mode, outcomeEmpty)
		return
	}
	sampleStarted := r.now()
	points = coverage.SampleGeographicObservations(points, sampling, rules)
	report.Durations.SamplingMilliseconds += r.now().Sub(sampleStarted).Milliseconds()
	report.Observations.Sampled += len(points)

	snapshot, err := r.Snapshots.Begin(ctx)
	if err != nil {
		r.recordError(report, mode, classifyContext(ctx, "osm_database"))
		return
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
		defer closeCancel()
		if err := snapshot.Close(closeCtx); err != nil {
			report.Errors["osm_snapshot_close"]++
		}
	}()

	options := coverage.DefaultOSMEvaluationOptions()
	options.MovementMode = mode
	// The route was sampled as a whole; retain every point inside each window.
	options.Sampling.MinimumDistanceMeters = 0
	for start := 0; start < len(points); {
		end := min(start+WindowSize, len(points))
		matchStarted := r.now()
		result, stats, err := r.Windows.Evaluate(ctx, snapshot, points[start:end], rules, options)
		report.Durations.MatchingMilliseconds += r.now().Sub(matchStarted).Milliseconds()
		if err != nil {
			r.recordError(report, mode, classifyMatcherError(ctx, err))
			return
		}
		addMeasure(&report.Matching.Candidates, stats.CandidateCount)
		addMeasure(&report.Matching.GraphNodes, stats.GraphNodeCount)
		addMeasure(&report.Matching.GraphEdges, stats.GraphEdgeCount)
		addMeasure(&report.Matching.ExpansionLayers, stats.ExpansionLayers)
		skip := 0
		if start > 0 {
			skip = 1
		}
		for i := skip; i < len(result.Observations); i++ {
			switch result.Observations[i].Status {
			case coverage.ObservationMatched:
				report.Observations.Matched++
			case coverage.ObservationAmbiguous:
				report.Observations.Ambiguous++
			case coverage.ObservationRejected:
				report.Observations.Rejected++
			default:
				report.Observations.Unmatched++
			}
		}
		report.Matching.Traversals += len(result.Traversals)
		for _, traversal := range result.Traversals {
			report.Matching.Portions += len(traversal.Portions)
			for _, portion := range traversal.Portions {
				report.segmentIDs[strings.TrimSuffix(strings.TrimSuffix(portion.SegmentID, "/f"), "/r")] = struct{}{}
			}
		}
		if end == len(points) {
			break
		}
		start = end - 1
	}
	report.Matching.UniqueSegments = len(report.segmentIDs)
	recordOutcome(report, mode, outcomeEvaluated)
}

func (r Runner) recordError(report *Report, mode coverage.MovementMode, category string) {
	report.Errors[category]++
	recordOutcome(report, mode, outcomeError)
}

func recordOutcome(report *Report, mode coverage.MovementMode, outcome string) {
	report.Workouts.Total++
	report.Workouts.ByOutcome[outcome]++
	report.Workouts.ByMode[string(mode)]++
	report.Workouts.ByOutcomeAndMode[outcome+"/"+string(mode)]++
}

func (r Runner) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func addMeasure(measure *Measure, value int) {
	measure.Total += value
	measure.Max = max(measure.Max, value)
}

type SafeError struct{ Category string }

func (e *SafeError) Error() string { return e.Category }

type AccountResolutionError struct{ Kind string }

func (e *AccountResolutionError) Error() string { return "account_" + e.Kind }

func classify(err error, fallback string) string {
	var resolution *AccountResolutionError
	if errors.As(err, &resolution) && (resolution.Kind == "not_found" || resolution.Kind == "ambiguous") {
		return "account_" + resolution.Kind
	}
	return fallback
}

func classifyContext(ctx context.Context, fallback string) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "workout_timeout"
	}
	return fallback
}

func classifyMatcherError(ctx context.Context, err error) string {
	if category := classifyContext(ctx, ""); category != "" {
		return category
	}
	var bounds *osm.MatcherBoundsError
	if errors.As(err, &bounds) {
		return "matcher_bounds"
	}
	var overflow *osm.MatcherOverflowError
	if errors.As(err, &overflow) {
		return "matcher_overflow"
	}
	return "osm_database"
}
