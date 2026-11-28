package worker

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/erhhung/workouts-explorer/internal/coverage/routepipeline"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestCoverageDisplayClassificationIsFencedToCurrentPolicyIntegration(t *testing.T) {
	for _, test := range []struct {
		name     string
		policy   coverage.PathPolicyVersion
		class    string
		rejected bool
	}{
		{"current missing", coverage.ExperimentalPathPolicyV84, "", true},
		{"current invalid", coverage.ExperimentalPathPolicyV84, "untrusted", true},
		{"current classified", coverage.ExperimentalPathPolicyV84, "ordinary", false},
		{"legacy cannot change presentation", coverage.ExperimentalPathPolicyV83, "parking_aisle", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newCoverageIntegrationFixture(t)
			f.seedOSM()
			workout := f.addWorkout(coverageIntegrationRoute(0))
			job := f.queuePolicy(workout, test.policy)
			runner := f.runner()
			claim, found, err := runner.claim(f.ctx)
			if err != nil || !found || claim.jobID != job.childID {
				t.Fatalf("claim: %t %v", found, err)
			}
			snapshot, err := osm.BeginMatcherSnapshot(f.ctx, f.osmDB)
			if err != nil {
				t.Fatal(err)
			}
			copies, err := snapshot.CopySegments(f.ctx, []osm.MatcherSegmentRef{{SegmentID: f.segments[0], RegionID: f.regionID, GenerationID: f.generation}})
			_ = snapshot.Close(f.ctx)
			if err != nil || len(copies) != 1 {
				t.Fatalf("segment copy: %v", err)
			}
			var line struct {
				Coordinates [][]float64 `json:"coordinates"`
			}
			if err := json.Unmarshal(copies[0].GeoJSON, &line); err != nil {
				t.Fatal(err)
			}
			geometry, _ := json.Marshal(struct {
				Type        string        `json:"type"`
				Coordinates [][][]float64 `json:"coordinates"`
			}{"MultiLineString", [][][]float64{line.Coordinates}})
			payload := coveragePersistencePayload([]routepipeline.PreparedMatch{{Segment: copies[0], CoveredGeoJSON: geometry, FirstTraversedAt: time.Now().UTC(), DisplayClass: test.class}})
			encoded, _ := json.Marshal(payload)
			generations, _ := json.Marshal(claim.targetGenerations)
			outcome, err := runner.persist(f.ctx, claim, 1, generations, encoded)
			if test.rejected {
				var pg *pgconn.PgError
				if !errors.As(err, &pg) || pg.Code != "22023" {
					t.Fatalf("missing/invalid class accepted: %s %v", outcome, err)
				}
				tx := f.accountTx(f.ctx, f.adminDB)
				var count int
				f.scan(tx, `SELECT count(*) FROM app.workout_segment_matches WHERE account_id=$1 AND workout_id=$2`, []any{f.accountID, workout}, &count)
				f.commit(tx)
				if count != 0 {
					t.Fatal("rejected classification leaked match rows")
				}
				if err := runner.failDetached(claim, time.Now(), "coverage-persist-failed", "Synthetic display classification rejection"); err != nil {
					t.Fatal(err)
				}
				return
			}
			if err != nil || outcome != "applied" {
				t.Fatalf("persist: %s %v", outcome, err)
			}
			tx := f.accountTx(f.ctx, f.adminDB)
			defer coverageIntegrationRollback(tx)
			var class *string
			f.scan(tx, `SELECT display_class FROM app.workout_segment_matches WHERE account_id=$1 AND workout_id=$2`, []any{f.accountID, workout}, &class)
			if test.policy == coverage.ExperimentalPathPolicyV84 {
				if class == nil || *class != "ordinary" {
					t.Fatal("current classification lost")
				}
			} else if class != nil {
				t.Fatal("legacy payload changed historical display semantics")
			}
			f.commit(tx)
		})
	}
}
