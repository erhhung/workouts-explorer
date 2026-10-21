package helm_test

import (
	"encoding/json"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/internal/config"
	"github.com/oasdiff/yaml3"
)

func TestHelmBaseMapExamples(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm is required")
	}
	output, err := exec.Command("helm", "template", "map-examples", chartDir, "--show-only", "templates/configmap.yaml").CombinedOutput()
	if err != nil {
		t.Fatalf("render map examples: %v\n%s", err, output)
	}
	var document struct{ Data map[string]string }
	if err := yaml.Unmarshal(output, &document); err != nil {
		t.Fatal(err)
	}
	var maps config.BaseMaps
	if err := json.Unmarshal([]byte(document.Data["BASE_MAPS_JSON"]), &maps); err != nil {
		t.Fatal(err)
	}
	if len(maps.StyleFamilies) != 2 || maps.FallbackFamilyID != "streets" || len(maps.WorkoutTypeMappings) != 2 {
		t.Fatalf("unexpected example families, fallback or active mappings: %+v", maps)
	}
	links := []config.BaseMapAttributionLink{
		{Label: "MapTiler", URL: "https://www.maptiler.com/copyright"},
		{Label: "OpenStreetMap", URL: "https://www.openstreetmap.org/copyright"},
	}
	for i, id := range []string{"outdoor", "streets"} {
		family := maps.StyleFamilies[i]
		if family.ID != id || family.Label != strings.ToUpper(id[:1])+id[1:] {
			t.Errorf("unexpected family identity: %+v", family)
		}
		want := config.BaseMapStyles{
			Light: "https://api.maptiler.com/maps/" + id + "-v4/style.json?key=YOUR_MAPTILER_KEY",
			Dark:  "https://api.maptiler.com/maps/" + id + "-v4-dark/style.json?key=YOUR_MAPTILER_KEY",
		}
		if family.Styles != want || family.Attribution.Text != "MapTiler and OpenStreetMap contributors" ||
			!reflect.DeepEqual(family.Attribution.Links, links) ||
			!reflect.DeepEqual(family.ResourceOrigins, []string{"https://api.maptiler.com"}) {
			t.Errorf("incomplete provider example for %s: %+v", id, family)
		}
	}
	wantMappings := []config.BaseMapWorkoutTypeMapping{
		{ProviderLabel: "Hiking", FamilyID: "outdoor"},
		{ProviderLabel: "Running", FamilyID: "streets"},
	}
	if !reflect.DeepEqual(maps.WorkoutTypeMappings, wantMappings) {
		t.Errorf("default mappings = %+v, want %+v", maps.WorkoutTypeMappings, wantMappings)
	}
}
