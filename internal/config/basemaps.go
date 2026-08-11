package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"

	"github.com/erhhung/workouts-explorer/internal/healthautoexport"
)

const defaultBaseMapsJSON = `{
  "styleFamilies": [{
    "id": "local-placeholder",
    "label": "Local placeholder",
    "styles": {
      "light": "https://maps.example.invalid/styles/light.json",
      "dark": "https://maps.example.invalid/styles/dark.json"
    },
    "attribution": {"text": "Base map not configured", "links": []},
    "resourceOrigins": ["https://maps.example.invalid"]
  }],
  "fallbackFamilyId": "local-placeholder",
  "workoutTypeMappings": []
}`

var baseMapFamilyID = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}[a-z0-9]$|^[a-z]$`)

type BaseMaps struct {
	StyleFamilies       []BaseMapStyleFamily        `json:"styleFamilies"`
	FallbackFamilyID    string                      `json:"fallbackFamilyId"`
	WorkoutTypeMappings []BaseMapWorkoutTypeMapping `json:"workoutTypeMappings"`
}

type BaseMapStyleFamily struct {
	ID              string             `json:"id"`
	Label           string             `json:"label"`
	Styles          BaseMapStyles      `json:"styles"`
	Attribution     BaseMapAttribution `json:"attribution"`
	ResourceOrigins []string           `json:"resourceOrigins"`
}

type BaseMapStyles struct {
	Light string `json:"light"`
	Dark  string `json:"dark"`
}

type BaseMapAttribution struct {
	Text  string                   `json:"text"`
	Links []BaseMapAttributionLink `json:"links"`
}

type BaseMapAttributionLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

type BaseMapWorkoutTypeMapping struct {
	ProviderLabel     string `json:"providerLabel"`
	NormalizedTypeKey string `json:"normalizedTypeKey"`
	FamilyID          string `json:"familyId"`
}

type baseMapsInput struct {
	StyleFamilies       []BaseMapStyleFamily        `json:"styleFamilies"`
	FallbackFamilyID    string                      `json:"fallbackFamilyId"`
	WorkoutTypeMappings []baseMapWorkoutTypeMapping `json:"workoutTypeMappings"`
}

type baseMapWorkoutTypeMapping struct {
	ProviderLabel string `json:"providerLabel"`
	FamilyID      string `json:"familyId"`
}

func DefaultBaseMaps() BaseMaps {
	value, _ := parseBaseMaps(defaultBaseMapsJSON)
	return value
}

func parseBaseMaps(raw string) (BaseMaps, error) {
	if strings.TrimSpace(raw) == "" {
		raw = defaultBaseMapsJSON
	}
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	var input baseMapsInput
	if err := decoder.Decode(&input); err != nil {
		return BaseMaps{}, fmt.Errorf("BASE_MAPS_JSON must be one strict JSON object: %w", err)
	}
	if err := ensureJSONEnd(decoder); err != nil {
		return BaseMaps{}, err
	}
	result := BaseMaps{StyleFamilies: input.StyleFamilies, FallbackFamilyID: input.FallbackFamilyID}
	result.WorkoutTypeMappings = make([]BaseMapWorkoutTypeMapping, len(input.WorkoutTypeMappings))
	for index, mapping := range input.WorkoutTypeMappings {
		result.WorkoutTypeMappings[index] = BaseMapWorkoutTypeMapping{
			ProviderLabel: mapping.ProviderLabel, NormalizedTypeKey: healthautoexport.NormalizedTypeKey(mapping.ProviderLabel), FamilyID: mapping.FamilyID,
		}
	}
	if err := validateBaseMaps(result); err != nil {
		return BaseMaps{}, fmt.Errorf("BASE_MAPS_JSON %w", err)
	}
	return result, nil
}

func ensureJSONEnd(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("BASE_MAPS_JSON must contain exactly one JSON object")
	}
	return nil
}

func validateBaseMaps(baseMaps BaseMaps) error {
	if len(baseMaps.StyleFamilies) == 0 || len(baseMaps.StyleFamilies) > 32 {
		return fmt.Errorf("must define between 1 and 32 styleFamilies")
	}
	families := make(map[string]struct{}, len(baseMaps.StyleFamilies))
	for index, family := range baseMaps.StyleFamilies {
		prefix := fmt.Sprintf("styleFamilies[%d]", index)
		if !baseMapFamilyID.MatchString(family.ID) {
			return fmt.Errorf("%s.id must be a lowercase stable ID of at most 64 characters", prefix)
		}
		if _, exists := families[family.ID]; exists {
			return fmt.Errorf("contains duplicate family id %q", family.ID)
		}
		families[family.ID] = struct{}{}
		if strings.TrimSpace(family.Label) == "" || len(family.Label) > 128 {
			return fmt.Errorf("%s.label must contain at most 128 characters", prefix)
		}
		origins := make(map[string]struct{}, len(family.ResourceOrigins))
		if len(family.ResourceOrigins) == 0 || len(family.ResourceOrigins) > 16 {
			return fmt.Errorf("%s.resourceOrigins must contain between 1 and 16 origins", prefix)
		}
		for _, origin := range family.ResourceOrigins {
			if err := validateHTTPSOrigin(origin); err != nil {
				return fmt.Errorf("%s.resourceOrigins: %w", prefix, err)
			}
			if _, exists := origins[origin]; exists {
				return fmt.Errorf("%s.resourceOrigins contains duplicate origin %q", prefix, origin)
			}
			origins[origin] = struct{}{}
		}
		for variant, styleURL := range map[string]string{"light": family.Styles.Light, "dark": family.Styles.Dark} {
			origin, err := validateHTTPSResourceURL(styleURL)
			if err != nil {
				return fmt.Errorf("%s.styles.%s: %w", prefix, variant, err)
			}
			if _, exists := origins[origin]; !exists {
				return fmt.Errorf("%s.resourceOrigins must include style origin %q", prefix, origin)
			}
		}
		if strings.TrimSpace(family.Attribution.Text) == "" || len(family.Attribution.Text) > 1024 {
			return fmt.Errorf("%s.attribution.text must contain at most 1024 characters", prefix)
		}
		if len(family.Attribution.Links) > 16 {
			return fmt.Errorf("%s.attribution.links must not contain more than 16 links", prefix)
		}
		for linkIndex, link := range family.Attribution.Links {
			if strings.TrimSpace(link.Label) == "" || len(link.Label) > 128 {
				return fmt.Errorf("%s.attribution.links[%d].label must contain at most 128 characters", prefix, linkIndex)
			}
			if _, err := validateHTTPSResourceURL(link.URL); err != nil {
				return fmt.Errorf("%s.attribution.links[%d].url: %w", prefix, linkIndex, err)
			}
		}
	}
	if _, exists := families[baseMaps.FallbackFamilyID]; !exists {
		return fmt.Errorf("fallbackFamilyId must reference a configured family")
	}
	if len(baseMaps.WorkoutTypeMappings) > 256 {
		return fmt.Errorf("must not define more than 256 workoutTypeMappings")
	}
	mappings := make(map[string]struct{}, len(baseMaps.WorkoutTypeMappings))
	for index, mapping := range baseMaps.WorkoutTypeMappings {
		if strings.TrimSpace(mapping.ProviderLabel) == "" || len(mapping.ProviderLabel) > 4096 {
			return fmt.Errorf("workoutTypeMappings[%d].providerLabel is invalid", index)
		}
		if _, exists := families[mapping.FamilyID]; !exists {
			return fmt.Errorf("workoutTypeMappings[%d].familyId must reference a configured family", index)
		}
		if _, exists := mappings[mapping.NormalizedTypeKey]; exists {
			return fmt.Errorf("workoutTypeMappings contains canonically duplicate provider label %q", mapping.ProviderLabel)
		}
		mappings[mapping.NormalizedTypeKey] = struct{}{}
	}
	return nil
}

func validateHTTPSResourceURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		return "", fmt.Errorf("must be an https URL without user information or fragment")
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

func validateHTTPSOrigin(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || raw != parsed.Scheme+"://"+parsed.Host {
		return fmt.Errorf("%q must be an exact https origin", raw)
	}
	return nil
}
