package osm

import "testing"

func TestReadinessFromRegionObservations(t *testing.T) {
	regionA, regionB := "geofabrik:a", "geofabrik:b"
	gen1, gen2, replacement := int64(11), int64(22), int64(23)
	tests := []struct {
		name          string
		observations  []pointRegionObservation
		state, reason string
		regions       []RegionGeneration
	}{
		{"one region", []pointRegionObservation{{&regionA, &gen1, true}, {&regionA, &gen1, true}}, "map_data_ready", "", []RegionGeneration{{regionA, gen1}}},
		{"two regions", []pointRegionObservation{{&regionB, &gen2, true}, {&regionA, &gen1, true}}, "map_data_ready", "", []RegionGeneration{{regionA, gen1}, {regionB, gen2}}},
		{"catalog but inactive", []pointRegionObservation{{nil, nil, true}}, "pending", "region_not_active", nil},
		{"no region", []pointRegionObservation{{nil, nil, false}}, "unavailable", "no_provider_region", nil},
		{"generation replacement", []pointRegionObservation{{&regionB, &replacement, true}}, "map_data_ready", "", []RegionGeneration{{regionB, replacement}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := readinessFromObservations(test.observations)
			if got.State != test.state || got.Reason != test.reason || len(got.Regions) != len(test.regions) {
				t.Fatalf("readiness=%+v", got)
			}
			for i := range test.regions {
				if got.Regions[i] != test.regions[i] {
					t.Fatalf("regions=%+v", got.Regions)
				}
			}
		})
	}
}
