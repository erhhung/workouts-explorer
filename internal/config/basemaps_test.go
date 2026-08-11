package config

import (
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/internal/healthautoexport"
)

const validBaseMapsJSON = `{
  "styleFamilies": [
    {
      "id": "outdoor",
      "label": "Outdoor",
      "styles": {"light": "https://api.maptiler.com/maps/outdoor/style.json?key=public", "dark": "https://api.maptiler.com/maps/outdoor-v2/style.json?key=public"},
      "attribution": {"text": "MapTiler and OpenStreetMap", "links": [{"label": "OpenStreetMap contributors", "url": "https://www.openstreetmap.org/copyright"}]},
      "resourceOrigins": ["https://api.maptiler.com"]
    },
    {
      "id": "smooth",
      "label": "Alidade Smooth",
      "styles": {"light": "https://tiles.stadiamaps.com/styles/alidade_smooth.json", "dark": "https://tiles.stadiamaps.com/styles/alidade_smooth_dark.json"},
      "attribution": {"text": "Stadia Maps", "links": [{"label": "Stadia Maps", "url": "https://stadiamaps.com/"}]},
      "resourceOrigins": ["https://tiles.stadiamaps.com"]
    }
  ],
  "fallbackFamilyId": "smooth",
  "workoutTypeMappings": [{"providerLabel": " Outdoor Run ", "familyId": "outdoor"}]
}`

func TestParseBaseMapsPreservesOrderAndNormalizesMappings(t *testing.T) {
	baseMaps, err := parseBaseMaps(validBaseMapsJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(baseMaps.StyleFamilies) != 2 || baseMaps.StyleFamilies[0].ID != "outdoor" || baseMaps.StyleFamilies[1].ID != "smooth" {
		t.Fatalf("style-family order changed: %+v", baseMaps.StyleFamilies)
	}
	mapping := baseMaps.WorkoutTypeMappings[0]
	if mapping.ProviderLabel != " Outdoor Run " || mapping.NormalizedTypeKey != healthautoexport.NormalizedTypeKey(mapping.ProviderLabel) || mapping.FamilyID != "outdoor" {
		t.Fatalf("mapping was not normalized with ingest algorithm: %+v", mapping)
	}
}

func TestParseBaseMapsRejectsInvalidRuntimeContract(t *testing.T) {
	tests := []struct {
		name string
		edit func(string) string
	}{
		{"unknown field", func(raw string) string {
			return strings.Replace(raw, `"label": "Outdoor"`, `"label": "Outdoor", "unknown": true`, 1)
		}},
		{"trailing JSON", func(raw string) string { return raw + `{}` }},
		{"duplicate family", func(raw string) string { return strings.Replace(raw, `"id": "smooth"`, `"id": "outdoor"`, 1) }},
		{"missing fallback", func(raw string) string {
			return strings.Replace(raw, `"fallbackFamilyId": "smooth"`, `"fallbackFamilyId": "missing"`, 1)
		}},
		{"unknown mapped family", func(raw string) string {
			return strings.Replace(raw, `"familyId": "outdoor"`, `"familyId": "missing"`, 1)
		}},
		{"plaintext style", func(raw string) string {
			return strings.Replace(raw, `https://api.maptiler.com/maps/outdoor/style.json`, `http://api.maptiler.com/maps/outdoor/style.json`, 1)
		}},
		{"missing style origin", func(raw string) string {
			return strings.Replace(raw, `https://api.maptiler.com/maps/outdoor/style.json`, `https://styles.example.com/maps/outdoor/style.json`, 1)
		}},
		{"origin path", func(raw string) string {
			return strings.Replace(raw, `"https://api.maptiler.com"`, `"https://api.maptiler.com/path"`, 1)
		}},
		{"attribution HTML URL", func(raw string) string {
			return strings.Replace(raw, `https://www.openstreetmap.org/copyright`, `javascript:alert(1)`, 1)
		}},
		{"derived key supplied", func(raw string) string {
			return strings.Replace(raw, `"providerLabel": " Outdoor Run "`, `"providerLabel": " Outdoor Run ", "normalizedTypeKey": "operator-value"`, 1)
		}},
		{"canonical duplicate mapping", func(raw string) string {
			return strings.Replace(raw, `{"providerLabel": " Outdoor Run ", "familyId": "outdoor"}`, `{"providerLabel": " Outdoor Run ", "familyId": "outdoor"}, {"providerLabel": "outdoor run", "familyId": "outdoor"}`, 1)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseBaseMaps(test.edit(validBaseMapsJSON)); err == nil {
				t.Fatal("invalid base-map runtime configuration was accepted")
			}
		})
	}
}
