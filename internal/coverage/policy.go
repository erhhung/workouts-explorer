package coverage

import (
	"encoding/json"
	"strings"
)

type MovementMode string

const (
	MovementFoot         MovementMode = "foot"
	MovementBicycle      MovementMode = "bicycle"
	MovementSharedPublic MovementMode = "shared_public"
)

type PathPolicyVersion string

const ExperimentalPathPolicyV82 PathPolicyVersion = "coverage-path-policy-experimental-v82"

const footRoadAttributionDistanceMeters = 5.0
const footRoadCandidateSearchRadiusMeters = 20.0
const footRoadDriftSearchRadiusMeters = 40.0
const maxContinuityConnectorMeters = 10.0
const maxDrivewayConnectorMeters = 40.0
const drivewayConnectorHeadingDegrees = 30.0
const maxDrivewayRoadJoinMeters = 10.0
const maxAccessoryConnectorMeters = 25.0
const accessoryRawSupportLengthMeters = 12.0
const maxParkingAisleConnectorMeters = 200.0
const parkingAisleRawSupportOffsetMeters = 20.0

type pathPolicy struct {
	version PathPolicyVersion
	mode    MovementMode
}

func experimentalPathPolicy(mode MovementMode) pathPolicy {
	if mode != MovementFoot && mode != MovementBicycle {
		mode = MovementSharedPublic
	}
	return pathPolicy{ExperimentalPathPolicyV82, mode}
}

func (policy pathPolicy) transitionEligible(highway string, rawTags json.RawMessage) bool {
	if policy.eligible(highway, rawTags) {
		return true
	}
	tags := decodeTags(rawTags)
	if policy.mode != MovementFoot && policy.mode != MovementBicycle {
		return false
	}
	if pedestrianDriveway(highway, tags) {
		return !denied(tags["access"]) && !denied(tags[string(policy.mode)])
	}
	if pedestrianParkingAisle(highway, tags) {
		return !forbidden(tags["access"]) && !forbidden(tags[string(policy.mode)])
	}
	if !pedestrianRoadAccessory(highway, tags) {
		return false
	}
	return !forbidden(tags["access"]) && !forbidden(tags[string(policy.mode)])
}

func pedestrianDriveway(highway string, tags map[string]string) bool {
	return highway == "service" && tags["service"] == "driveway"
}

func pedestrianParkingAisle(highway string, tags map[string]string) bool {
	return highway == "service" && tags["service"] == "parking_aisle"
}

func (policy pathPolicy) eligible(highway string, rawTags json.RawMessage) bool {
	tags := decodeTags(rawTags)
	highway = strings.ToLower(highway)
	privatePath := policy.privatePedestrianAccess(highway, tags)
	if (policy.mode == MovementFoot || policy.mode == MovementBicycle) && excludedServiceRoad(highway, tags) {
		return false
	}
	if denied(tags["access"]) && !allowed(tags[string(policy.mode)]) &&
		!((policy.mode == MovementFoot || policy.mode == MovementBicycle) && drivableRoad(highway, tags)) && !privatePath {
		return false
	}
	switch policy.mode {
	case MovementFoot:
		if pedestrianRoadAccessory(highway, tags) {
			return false
		}
		if denied(tags["foot"]) && !drivableRoad(highway, tags) && !privatePath {
			return false
		}
		return !motorOnly(highway) || allowed(tags["foot"])
	case MovementBicycle:
		if pedestrianRoadAccessory(highway, tags) {
			return false
		}
		if denied(tags["bicycle"]) && !drivableRoad(highway, tags) && !privatePath {
			return false
		}
		if highway == "steps" && !allowed(tags["bicycle"]) && tags["bicycle"] != "dismount" {
			return false
		}
		return !motorOnly(highway) || allowed(tags["bicycle"])
	default:
		if motorOnly(highway) && !allowed(tags["foot"]) && !allowed(tags["bicycle"]) {
			return false
		}
		return !denied(tags["foot"]) || !denied(tags["bicycle"])
	}
}

func (policy pathPolicy) privatePedestrianAccess(highway string, tags map[string]string) bool {
	if (policy.mode != MovementFoot && policy.mode != MovementBicycle) || !pedestrianOnlyPathTags(highway, tags) {
		return false
	}
	modeAccess := tags[string(policy.mode)]
	return !forbidden(tags["access"]) && !forbidden(modeAccess) &&
		(tags["access"] == "private" || modeAccess == "private")
}

func excludedServiceRoad(highway string, tags map[string]string) bool {
	if highway != "service" {
		return false
	}
	switch tags["service"] {
	case "driveway":
		return !namedPrivateNeighborhoodRoad(tags)
	case "parking_aisle", "drive-through", "emergency_access":
		return true
	default:
		return false
	}
}

func namedPrivateNeighborhoodRoad(tags map[string]string) bool {
	return strings.TrimSpace(tags["name"]) != "" && strings.EqualFold(tags["access"], "private")
}

func pedestrianRoadAccessory(highway string, tags map[string]string) bool {
	if highway != "footway" && highway != "path" && highway != "cycleway" {
		return false
	}
	switch tags["footway"] {
	case "sidewalk", "crossing", "traffic_island", "link":
		return true
	}
	return tags["sidewalk"] == "yes" || tags["sidewalk"] == "both" || tags["sidewalk"] == "left" || tags["sidewalk"] == "right"
}

func pedestrianContinuityConnector(highway string, tags map[string]string) bool {
	if highway != "footway" {
		return false
	}
	return tags["footway"] == "crossing" || tags["footway"] == "traffic_island"
}

func pedestrianAccessoryConnector(highway string, tags map[string]string) bool {
	if highway != "footway" && highway != "path" && highway != "cycleway" {
		return false
	}
	switch tags["footway"] {
	case "sidewalk", "crossing", "traffic_island":
		return true
	default:
		return tags["sidewalk"] == "yes" || tags["sidewalk"] == "both" || tags["sidewalk"] == "left" || tags["sidewalk"] == "right"
	}
}

func drivableRoad(highway string, tags map[string]string) bool {
	switch strings.ToLower(highway) {
	case "primary", "primary_link", "secondary", "secondary_link", "tertiary", "tertiary_link",
		"unclassified", "residential", "living_street", "road":
		return true
	case "service":
		switch tags["service"] {
		case "driveway":
			return namedPrivateNeighborhoodRoad(tags)
		case "parking_aisle", "drive-through", "emergency_access":
			return false
		default:
			return true
		}
	default:
		return false
	}
}

func pedestrianOnlyPath(highway string, rawTags json.RawMessage) bool {
	return pedestrianOnlyPathTags(highway, decodeTags(rawTags))
}

func pedestrianOnlyPathTags(highway string, tags map[string]string) bool {
	switch strings.ToLower(highway) {
	case "footway", "pedestrian", "steps", "corridor", "cycleway":
		return true
	case "path":
		return !allowed(tags["motor_vehicle"]) && !allowed(tags["motorcar"])
	default:
		return false
	}
}

func (policy pathPolicy) directions(rawTags json.RawMessage, motorForward, motorReverse bool) (bool, bool) {
	tags := decodeTags(rawTags)
	switch policy.mode {
	case MovementFoot:
		if value := tags["oneway:foot"]; value != "" {
			return onewayDirections(value)
		}
		return true, true
	case MovementBicycle:
		if value := tags["oneway:bicycle"]; value != "" {
			return onewayDirections(value)
		}
		if value := tags["oneway"]; value != "" {
			return onewayDirections(value)
		}
		return motorForward, motorReverse
	default:
		return true, true
	}
}

func decodeTags(raw json.RawMessage) map[string]string {
	result := make(map[string]string)
	_ = json.Unmarshal(raw, &result)
	for key, value := range result {
		result[strings.ToLower(key)] = strings.ToLower(strings.TrimSpace(value))
	}
	return result
}

func denied(value string) bool {
	switch strings.ToLower(value) {
	case "no", "private":
		return true
	default:
		return false
	}
}

func forbidden(value string) bool {
	return strings.EqualFold(value, "no")
}

func allowed(value string) bool {
	switch strings.ToLower(value) {
	case "yes", "designated", "permissive", "official":
		return true
	default:
		return false
	}
}

func motorOnly(highway string) bool {
	switch highway {
	case "motorway", "motorway_link", "trunk", "trunk_link", "construction", "proposed", "raceway":
		return true
	default:
		return false
	}
}

func onewayDirections(value string) (bool, bool) {
	switch strings.ToLower(value) {
	case "yes", "1", "true":
		return true, false
	case "-1", "reverse":
		return false, true
	default:
		return true, true
	}
}
