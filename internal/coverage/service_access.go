package coverage

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
)

func (options OSMEvaluationOptions) residentialTags(raw json.RawMessage, generation int64) (json.RawMessage, error) {
	if options.ResidentialContextGenerations == nil || options.ResidentialContextGenerations[generation] ||
		!bytes.Contains(raw, []byte("workouts:residential_")) {
		return raw, nil
	}
	var tags map[string]json.RawMessage
	if err := json.Unmarshal(raw, &tags); err != nil {
		return nil, errors.New("invalid residential context metadata")
	}
	for key := range tags {
		if strings.HasPrefix(key, "workouts:residential_") {
			delete(tags, key)
		}
	}
	return json.Marshal(tags)
}

func (policy pathPolicy) sharedAccessPolicy() bool {
	return policy.version == ExperimentalPathPolicyV84 && (policy.mode == MovementFoot || policy.mode == MovementBicycle)
}

func (policy pathPolicy) sharedNeighborhoodRoad(tags map[string]string) bool {
	if policy.sharedAccessPolicy() && tags["workouts:residential_context_version"] != "" {
		return tags["workouts:residential_context_version"] == "1" && tags["workouts:residential_access"] == "shared"
	}
	return namedPrivateNeighborhoodRoad(tags)
}

func (policy pathPolicy) parkingCandidate(highway string, tags map[string]string) bool {
	return policy.sharedAccessPolicy() && pedestrianParkingAisle(highway, tags)
}

func (policy pathPolicy) excludedServiceRoad(highway string, tags map[string]string) bool {
	if policy.sharedAccessPolicy() && highway == "service" {
		switch tags["service"] {
		case "driveway":
			return !policy.sharedNeighborhoodRoad(tags)
		case "parking_aisle":
			return false
		}
	}
	return excludedServiceRoad(highway, tags)
}

func (policy pathPolicy) drivableRoad(highway string, tags map[string]string) bool {
	if policy.sharedAccessPolicy() && highway == "service" {
		switch tags["service"] {
		case "driveway":
			return policy.sharedNeighborhoodRoad(tags)
		case "parking_aisle":
			return true
		}
	}
	return drivableRoad(highway, tags)
}

// DisplayClass is the matcher's persisted display decision, rather than another
// copy of its road eligibility rules in each SQL reader. Legacy jobs omit it.
func DisplayClass(version PathPolicyVersion, mode MovementMode, highway string, tags json.RawMessage) string {
	if version == "" {
		version = CurrentPathPolicyVersion
	}
	if version != ExperimentalPathPolicyV84 {
		return ""
	}
	if mode != MovementFoot && mode != MovementBicycle {
		return "legacy"
	}
	policy := pathPolicyForVersion(mode, version)
	if !policy.eligible(highway, tags) {
		return "connector"
	}
	decoded := decodeTags(tags)
	if policy.parkingCandidate(highway, decoded) {
		return "parking_aisle"
	}
	if highway == "service" && (decoded["service"] == "driveway" || decoded["service"] == "shared_driveway") {
		if decoded["service"] == "shared_driveway" || decoded["workouts:residential_context_version"] == "1" {
			return "shared_road"
		}
		return "ordinary"
	}
	return "ordinary"
}
