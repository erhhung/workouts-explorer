package routepipeline

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/erhhung/workouts-explorer/internal/osm"
)

// These synthetic goldens compare the full pre-leg-split Evaluate result and
// Prepare global union, including evidence, stats, generations, order and times.
// Expected hashes came from the sealed deployed 40OLD pipeline/window source
// (ingest-reprocessing source manifest) in a private oracle: old repeat
// determinism and current equality were checked before recording them.
// No private source or runtime regeneration is required.
// Old pipeline.go SHA256: 4399a420573f1eb33f0bff4945d839542d0e1a2a1fd2fa76f70c21d51a929dd4
// Old window.go SHA256: b548aa346b2d39b9f059beb51f0cbdb9075d2f542c13ea456ad8aa0db07a839e
func TestContinuousPipelineGolden(t *testing.T) {
	for _, tc := range []struct {
		kind    string
		count   int
		cap     int
		mode    coverage.MovementMode
		reverse bool
		want    string
	}{
		{"line", 0, 128, coverage.MovementFoot, false, "53037254ea34ad61315f66d309a50a1282ad9350e3432cf10cceb68878585e12"},
		{"line", 1, 128, coverage.MovementFoot, false, "4b8a4609a4f57ed5b1043d7febf29a3dbd3d05d628cde59559b1cc9d5b1735da"},
		{"line", 2, 128, coverage.MovementFoot, false, "cb83fa943db2e1d7505b8de9f3a518c28fe2e7b480f8d0d7815fb12647449e96"},
		{"line", 32, 128, coverage.MovementFoot, false, "45a4941f9062330d13fbf60eb8870f7b154b54493c29fe4a228c74695a7aa583"},
		{"line", 64, 128, coverage.MovementFoot, false, "064135b1c8c11a6c481c4611c63d47561638d30853e3aefa3da730a9e673ba26"},
		{"line", 128, 128, coverage.MovementFoot, false, "e9ecf9416b75e7a603c5eec596c752546ee7dc7ca544e7715b6569b560ae7370"},
		{"line", 129, 128, coverage.MovementFoot, false, "7e13248411541050b7c905e27c6417ee10e3eca1fd6506b630533d555e9c5ae4"},
		{"line", 258, 128, coverage.MovementFoot, false, "3e3f5d5a4787c49fd7dcdc6640b97f1beb9e4ecf6b9d5023b779464e5470c889"},
		{"line", 300, 128, coverage.MovementFoot, false, "120d66681b22db68356bff2eb5a2f9063f49cd80869ff9a7872ae4f68f0c51ef"},
		{"line", 300, 128, coverage.MovementBicycle, false, "120d66681b22db68356bff2eb5a2f9063f49cd80869ff9a7872ae4f68f0c51ef"},
		{"line", 300, 128, coverage.MovementSharedPublic, false, "120d66681b22db68356bff2eb5a2f9063f49cd80869ff9a7872ae4f68f0c51ef"},
		{"line", 300, 64, coverage.MovementFoot, false, "a1177a8750371407b6beb047679e2eff8221952ae90f048bba742d5a513a9ff5"},
		{"line", 300, 32, coverage.MovementFoot, false, "5fd2d4c1872489225223947c36ade83fb8ed7ab124cdd4cc8c82408284bf32b8"},
		{"turn", 256, 128, coverage.MovementFoot, false, "3d2cb3d6b60cc645379aded6f9bb47f8cebeef2963cd708e3f360e0f7cea9e9e"},
		{"turn", 256, 128, coverage.MovementFoot, true, "e618d285a09ea935b051f2c4e06fdeb82567944673c6de6c86757b5259f6d834"},
		{"turn", 256, 64, coverage.MovementBicycle, false, "b02fa55eceab7a2005368071c755216ab332bf34e6a487d29add6e6ae4c1b531"},
		{"turn", 256, 64, coverage.MovementBicycle, true, "c8ef94acd1689d1348aaeee37d58046e3626f6c12809c2b44f70c9ba734a7830"},
		{"turn", 256, 32, coverage.MovementSharedPublic, false, "df45fa3e7233fe8652c0a9ff25a3d538a2190ae457ccb6e07dc6f133407bb908"},
		{"turn", 256, 32, coverage.MovementSharedPublic, true, "4e329c5fffea9f77a2bb82213bf8c340f853277733baf9553373f20539267756"},
		{"tangent", 256, 128, coverage.MovementFoot, false, "e9b9739d545ce37521420a39cab9de825cd050354e2bcd47c7539c9246c0e49c"},
		{"tangent", 256, 128, coverage.MovementFoot, true, "9d1c401ed8f886e3f684327db79e983dfdce42f17b2ae37281b35fc60af199ef"},
		{"tangent", 256, 64, coverage.MovementBicycle, false, "020d68bacf75b5ac3462e818006360306da092f985d6fcafb750edfb31dc9e97"},
		{"tangent", 256, 64, coverage.MovementBicycle, true, "98508cf753194f7b68a750bc9de24277ccdfca1761348e6a2708f74277badf01"},
		{"tangent", 256, 32, coverage.MovementSharedPublic, false, "b3f11d9756841532752f8ade87499bb1858d694a9b3b190acd7a2d600dd1ab0e"},
		{"tangent", 256, 32, coverage.MovementSharedPublic, true, "b54d1157c2390c671e6ddda012ab700a9c7d965fb85b70ea7a4ac1ea59a69f7c"},
		{"return", 300, 128, coverage.MovementFoot, false, "2869147ebdd1fdeb8ce705a58f270ff838b3c60a7c1b3d4cb86d31e079192328"},
		{"missing", 300, 128, coverage.MovementFoot, false, "e45d3534ca0a7ce22abfbb414ed55e6d8b9441aa29bff28ec0d78177a6f579f9"},
	} {
		t.Run(fmt.Sprintf("%s/%d/cap=%d/%s/reverse=%v", tc.kind, tc.count, tc.cap, tc.mode, tc.reverse), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			snapshot := &windowSnapshot{roads: fixtureRoads(fixturePoint{0, 0}, fixturePoint{1000, 0})}
			points := fixtureObservations(tc.count)
			switch tc.kind {
			case "turn":
				snapshot, points, _ = turnFixture(tc.reverse, 128)
			case "tangent":
				snapshot, points, _ = tangentFixture(tc.reverse, 128)
			case "return":
				for i := len(points) / 2; i < len(points); i++ {
					points[i].Longitude = points[len(points)-1-i].Longitude
				}
			case "missing":
				snapshot.roads = nil
				snapshot.unavailable = []osm.MatcherUnavailableRegion{{RegionID: "fixture:missing", DisplayName: "Synthetic"}}
			}
			cfg := fixtureConfig()
			cfg.MovementMode = tc.mode
			for i, point := range points {
				if !finite(point.Longitude) || !finite(point.Latitude) || math.Abs(point.Longitude) > 180 || math.Abs(point.Latitude) > 90 || point.Time.IsZero() {
					t.Fatalf("invalid synthetic observation %d", i)
				}
				if i > 0 {
					previous := points[i-1]
					dt := point.Time.Sub(previous.Time)
					distance := math.Hypot(point.Longitude-previous.Longitude, point.Latitude-previous.Latitude) * fixtureMetersPerDegree
					if dt < 0 || dt > 30*time.Second || distance >= 200 || coverage.GeographicGapReason(previous, point, cfg.Rules) != "" {
						t.Fatalf("fixture is not continuous at %d", i)
					}
				}
			}
			if sampled := coverage.SampleGeographicObservations(points, cfg.Sampling, cfg.Rules); len(sampled) != len(points) {
				t.Fatal("fixture sampling changed the window boundaries")
			}
			snapshot.fail = func(query []osm.MatcherObservation) error {
				if len(query) > tc.cap {
					return &coverage.OSMEvaluationLimitError{Field: "graph node", Limit: 2048}
				}
				return nil
			}
			result, err := Evaluate(ctx, snapshot, points, cfg)
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := Prepare(ctx, snapshot, result, time.Unix(1, 0).UTC())
			if err != nil {
				t.Fatal(err)
			}
			// Marshal the typed pair directly: sorted map keys, standard RawMessage
			// compaction, and original slice order, floats, nulls and timestamps.
			payload, err := json.Marshal(struct {
				Result   Result
				Prepared []PreparedMatch
			}{result, prepared})
			if err != nil {
				t.Fatal(err)
			}
			if got := fmt.Sprintf("%x", sha256.Sum256(payload)); got != tc.want {
				t.Fatalf("full Result + Prepare SHA256 = %s, want %s", got, tc.want)
			}
		})
	}
}
