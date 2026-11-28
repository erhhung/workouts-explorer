package osm

import "testing"

func TestProcessingReadinessWaitsForPromotedRequiredGeneration(t *testing.T) {
	required := MapProcessingRequirements{ImporterVersion: 4, DerivationVersion: 21}
	old := RouteRegionCatalog{ActiveGenerations: map[string]int64{"norcal": 62}, ProcessingVersions: map[string]MapProcessingVersion{
		"norcal": {Generation: 62, ImporterVersion: 3, DerivationVersion: 21},
	}}
	ready := RouteRegionReadiness{State: "map_data_ready", Regions: []RegionGeneration{{RegionID: "norcal", Generation: 62}}, CatalogFingerprint: "old"}
	blocked := old.QualifyProcessing(ready, required)
	if blocked.State != "pending" || blocked.Reason != "region_reprocessing_required" || len(blocked.Regions) != 0 {
		t.Fatalf("old promoted map admitted new-policy work: %+v", blocked)
	}
	if legacy := old.QualifyProcessing(ready, MapProcessingRequirements{}); legacy.State != "map_data_ready" {
		t.Fatal("legacy matcher lost access to its compatible map")
	}
	// A building generation must not appear in the active processing catalog.
	old.ProcessingVersions["norcal"] = MapProcessingVersion{Generation: 82, ImporterVersion: 4, DerivationVersion: 21}
	if old.ProcessingReady(ready.Regions, required) {
		t.Fatal("unpromoted replacement unlocked an old target")
	}
	ready.Regions[0].Generation = 82
	old.ActiveGenerations["norcal"] = 82
	if got := old.QualifyProcessing(ready, required); got.State != "map_data_ready" {
		t.Fatal("promoted current map remained blocked")
	}
	old.ProcessingVersions["nevada"] = MapProcessingVersion{Generation: 77, ImporterVersion: 3, DerivationVersion: 21}
	ready.Regions = append(ready.Regions, RegionGeneration{RegionID: "nevada", Generation: 77})
	if old.ProcessingReady(ready.Regions, required) {
		t.Fatal("one fresh map admitted a mixed route needing another stale map")
	}
}
