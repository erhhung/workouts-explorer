package worker

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/internal/osm"
)

func TestCoverageSelectionRebindUsesSelectionNotFullFingerprint(t *testing.T) {
	for _, state := range []string{"map_data_ready", "pending", "unavailable"} {
		t.Run(state, func(t *testing.T) {
			item := coverageHandoff{Readiness: state, CatalogRevision: 10, CatalogFingerprint: strings.Repeat("a", 64),
				SelectionFingerprint: strings.Repeat("b", 64), SelectionPolicy: coverageSelectionPolicy(false), MutationVersion: 7,
				RouteDigest: strings.Repeat("c", 64)}
			if state == "map_data_ready" {
				item.Generations = []osm.RegionGeneration{{RegionID: "geofabrik:chosen", Generation: 1}}
			}
			catalog := osm.RouteRegionCatalog{Revision: 11, Fingerprint: strings.Repeat("d", 64), SelectionFingerprint: item.SelectionFingerprint,
				ActiveGenerations: map[string]int64{"geofabrik:chosen": 2, "geofabrik:unselected": 99}}
			if !rebindCoverageSelection(&item, catalog, false) {
				t.Fatal("generation-only full fingerprint change invalidated an exact selection proof")
			}
			if item.CatalogRevision != 11 || item.CatalogFingerprint != catalog.Fingerprint || item.MutationVersion != 7 ||
				item.RouteDigest != strings.Repeat("c", 64) || item.Readiness != state {
				t.Fatalf("rebind lost immutable input or negative proof: %+v", item)
			}
			if state == "map_data_ready" && (len(item.Generations) != 1 || item.Generations[0].Generation != 2) {
				t.Fatalf("rebind selected new/competing IDs instead of updating only chosen heads: %+v", item.Generations)
			}
		})
	}
}

func TestCoverageSelectionRebindFailsClosedWithoutMutation(t *testing.T) {
	for _, change := range []string{"selection", "policy", "missing selected head", "unknown proof"} {
		t.Run(change, func(t *testing.T) {
			item := coverageHandoff{Readiness: "map_data_ready", CatalogFingerprint: strings.Repeat("a", 64),
				SelectionFingerprint: strings.Repeat("b", 64), SelectionPolicy: coverageSelectionPolicy(false),
				Generations: []osm.RegionGeneration{{RegionID: "geofabrik:a", Generation: 1}, {RegionID: "geofabrik:b", Generation: 2}}}
			catalog := osm.RouteRegionCatalog{Revision: 2, Fingerprint: strings.Repeat("c", 64), SelectionFingerprint: item.SelectionFingerprint,
				ActiveGenerations: map[string]int64{"geofabrik:a": 3, "geofabrik:b": 4}}
			switch change {
			case "selection":
				catalog.SelectionFingerprint = strings.Repeat("d", 64)
			case "policy":
				item.SelectionPolicy = coverageSelectionPolicy(true)
			case "missing selected head":
				delete(catalog.ActiveGenerations, "geofabrik:b")
			case "unknown proof":
				item.SelectionFingerprint = ""
			}
			before := item
			before.Generations = append([]osm.RegionGeneration(nil), item.Generations...)
			if rebindCoverageSelection(&item, catalog, false) || !reflect.DeepEqual(item, before) {
				t.Fatalf("invalid proof was rebound or partially mutated: before=%+v after=%+v", before, item)
			}
		})
	}
}

func TestCoverageMatchingDescriptorIncludesEveryProductionParameter(t *testing.T) {
	base := productionMatchingDescriptor(5)
	for _, field := range []string{"Rules", "Sampling", "PathPolicy", "MinimumTraversal", "Contract"} {
		changed := base
		value := reflect.ValueOf(&changed).Elem().FieldByName(field)
		if value.Kind() == reflect.String {
			value.SetString(value.String() + "-changed")
		} else {
			value.SetFloat(6)
		}
		left, _ := json.Marshal(base)
		right, _ := json.Marshal(changed)
		if string(left) == string(right) {
			t.Fatalf("effective matching identity omitted %s", field)
		}
	}
	encoded, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	var descriptor map[string]any
	if err := json.Unmarshal(encoded, &descriptor); err != nil {
		t.Fatal(err)
	}
	if len(descriptor) != 5 || descriptor["minimumTraversalMeters"] != float64(5) || descriptor["productionMatcherContract"] != coverageProductionContractV1 {
		t.Fatalf("incomplete immutable production descriptor: %s", encoded)
	}
}

func TestCoverageAutoAddPolicyPreservesAllSelectionProofFields(t *testing.T) {
	pending := osm.RouteRegionReadiness{State: "pending", Reason: "region_not_active", Regions: []osm.RegionGeneration{},
		CatalogRevision: 19, CatalogFingerprint: strings.Repeat("a", 64), SelectionFingerprint: strings.Repeat("b", 64)}
	readiness := routeReadinessForAutoAddPolicy(pending, false)
	if readiness.State != "unavailable" || readiness.CatalogRevision != pending.CatalogRevision ||
		readiness.CatalogFingerprint != pending.CatalogFingerprint || readiness.SelectionFingerprint != pending.SelectionFingerprint {
		t.Fatalf("auto-add policy discarded exact negative proof: %+v", readiness)
	}
	var handoff coverageHandoff
	setCoverageSelectionProof(&handoff, readiness, false)
	if handoff.SelectionPolicy != coverageSelectionPolicy(false) || handoff.CatalogRevision != 19 || handoff.SelectionFingerprint != pending.SelectionFingerprint {
		t.Fatalf("prepared handoff omitted proof: %+v", handoff)
	}
}
