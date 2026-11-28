package routepipeline

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Opt-in private observations are never embedded, logged or written to OSM.
// The native public fixture is independently imported/classified map data, not
// evidence that a production regional update or workout rematch has completed.
func TestResidentialAccessAuthorizedPrivateReplay(t *testing.T) {
	path := os.Getenv("COVERAGE_PRIVATE_REPLAY_FIXTURE")
	if path == "" || os.Getenv("COVERAGE_PRIVATE_REPLAY_DISPOSABLE") != "1" {
		t.Skip("explicit private fixture and owned public-map database required")
	}
	absolute, err := filepath.Abs(path)
	if err != nil || !strings.HasPrefix(absolute, "/tmp/opencode/") {
		t.Fatal("private replay fixture must stay outside Git")
	}
	info, err := os.Stat(absolute)
	if err != nil || info.Mode().Perm()&0077 != 0 {
		t.Fatal("private fixture permissions must exclude group/other access")
	}
	cfg, err := pgxpool.ParseConfig(os.Getenv("COVERAGE_PRIVATE_REPLAY_DATABASE_URL"))
	if err != nil || cfg.ConnConfig.Host != "127.0.0.1" || cfg.ConnConfig.Database != "acceptance_test_public_context" || cfg.ConnConfig.User != "workouts_osm_reader" {
		t.Fatal("owned loopback public-map fixture and read-only role required")
	}
	data, err := os.ReadFile(absolute)
	if err != nil {
		t.Fatal("private input unavailable")
	}
	var frames []struct {
		Label        string                           `json:"label"`
		Observations []coverage.GeographicObservation `json:"observations"`
		MinimumByWay map[int64]float64                `json:"minimumByWay"`
	}
	if json.Unmarshal(data, &frames) != nil || len(frames) == 0 {
		t.Fatal("invalid private fixture")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("public fixture connection failed")
	}
	defer pool.Close()
	receipt := map[string]map[coverage.PathPolicyVersion]map[int64]float64{}
	for _, frame := range frames {
		receipt[frame.Label] = map[coverage.PathPolicyVersion]map[int64]float64{}
		for _, policy := range []coverage.PathPolicyVersion{coverage.ExperimentalPathPolicyV83, coverage.ExperimentalPathPolicyV84} {
			snapshot, err := osm.BeginMatcherSnapshot(ctx, pool)
			if err != nil {
				t.Fatalf("fixture snapshot failed (%T)", err)
			}
			config := Config{PathPolicyVersion: policy, Rules: coverage.ExperimentalRules().WithMinimumTraversalMeters(5),
				Sampling: coverage.ExperimentalSamplingRules(), RoadGeometry: coverage.DefaultRoadGeometryRules(), MovementMode: coverage.MovementFoot,
				Limits: DefaultLimits(), RecoverProcessingLimits: true, MemoizeSnapshot: true}
			result, err := Evaluate(ctx, snapshot, frame.Observations, config)
			if err != nil {
				_ = snapshot.Close(ctx)
				t.Fatalf("%s policy %s evaluation failed (%T)", frame.Label, policy, err)
			}
			prepared, err := Prepare(ctx, snapshot, result, frame.Observations[0].Time)
			_ = snapshot.Close(ctx)
			if err != nil {
				t.Fatalf("%s preparation failed (%T)", frame.Label, err)
			}
			totals := map[int64]float64{}
			for _, match := range prepared {
				if policy == coverage.ExperimentalPathPolicyV84 && match.DisplayClass == "connector" {
					continue
				}
				totals[match.Segment.SourceWayID] += match.CoveredMeters
			}
			receipt[frame.Label][policy] = totals
			if policy == coverage.ExperimentalPathPolicyV84 {
				for way, minimum := range frame.MinimumByWay {
					if totals[way] < minimum {
						t.Errorf("%s way %d: supported metres=%.2f minimum=%.2f", frame.Label, way, totals[way], minimum)
					}
				}
			}
		}
	}
	encoded, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		t.Fatal("receipt encoding failed")
	}
	if err := os.WriteFile(filepath.Join(filepath.Dir(absolute), "private-replay-receipt.json"), encoded, 0600); err != nil {
		t.Fatal("receipt unavailable")
	}
	t.Logf("Completed %d authorized private replay windows against independently imported public context; no production writes", len(frames))
}
