package coverage

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

type RoadGeometryRules struct {
	MotorLaneWidthMeters           float64
	BicycleLaneWidthMeters         float64
	ParkingLaneWidthMeters         float64
	SidewalkSetbackMeters          float64
	DirectionalDriftMeters         float64
	DeadEndEndpointAllowanceMeters float64
}

func DefaultRoadGeometryRules() RoadGeometryRules {
	return RoadGeometryRules{
		MotorLaneWidthMeters:           3,
		BicycleLaneWidthMeters:         1.5,
		ParkingLaneWidthMeters:         2.1,
		SidewalkSetbackMeters:          3,
		DirectionalDriftMeters:         7,
		DeadEndEndpointAllowanceMeters: 3,
	}
}

func (rules RoadGeometryRules) valid() bool {
	return rules.MotorLaneWidthMeters >= 2.5 && rules.MotorLaneWidthMeters <= 4 &&
		rules.BicycleLaneWidthMeters >= 1 && rules.BicycleLaneWidthMeters <= 2.5 &&
		rules.ParkingLaneWidthMeters >= 1.5 && rules.ParkingLaneWidthMeters <= 3 &&
		rules.SidewalkSetbackMeters >= 1 && rules.SidewalkSetbackMeters <= 5 &&
		rules.DirectionalDriftMeters >= 0 && rules.DirectionalDriftMeters <= 10 &&
		rules.DeadEndEndpointAllowanceMeters >= 0 && rules.DeadEndEndpointAllowanceMeters <= 10
}

func roadAttributionOffset(mode MovementMode, highway string, raw json.RawMessage, rules RoadGeometryRules) float64 {
	tags := decodeTags(raw)
	if (mode != MovementFoot && mode != MovementBicycle) || !drivableRoad(highway, tags) {
		return 0
	}
	width, ok := taggedWidthMeters(tags["width"])
	if !ok {
		lanes, lanesOK := positiveTagNumber(tags["lanes"], 12)
		if !lanesOK {
			lanes = defaultLaneCount(highway)
		}
		width = lanes * rules.MotorLaneWidthMeters
		width += float64(taggedBicycleLaneCount(tags)) * rules.BicycleLaneWidthMeters
		width += float64(taggedParkingLaneCount(tags)) * rules.ParkingLaneWidthMeters
	}
	// The OSM line represents the carriageway center. Extend its estimated edge
	// by the expected curb-to-sidewalk-center setback, but retain the hard query cap.
	return math.Min(footRoadDriftSearchRadiusMeters, width/2+rules.SidewalkSetbackMeters)
}

func taggedWidthMeters(value string) (float64, bool) {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" || strings.ContainsAny(value, ";~") {
		return 0, false
	}
	multiplier := 1.0
	switch {
	case strings.HasSuffix(value, " m"):
		value = strings.TrimSpace(strings.TrimSuffix(value, " m"))
	case strings.HasSuffix(value, " ft"):
		value, multiplier = strings.TrimSpace(strings.TrimSuffix(value, " ft")), 0.3048
	case strings.HasSuffix(value, "'"):
		value, multiplier = strings.TrimSpace(strings.TrimSuffix(value, "'")), 0.3048
	}
	width, err := strconv.ParseFloat(value, 64)
	width *= multiplier
	return width, err == nil && finite(width) && width >= 2 && width <= 40
}

func positiveTagNumber(value string, maximum float64) (float64, bool) {
	number, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	return number, err == nil && finite(number) && number > 0 && number <= maximum
}

func defaultLaneCount(_ string) float64 {
	return 2
}

func taggedBicycleLaneCount(tags map[string]string) int {
	if laneFacility(tags["cycleway:both"]) {
		return 2
	}
	count := 0
	if laneFacility(tags["cycleway:left"]) {
		count++
	}
	if laneFacility(tags["cycleway:right"]) {
		count++
	}
	if count == 0 && laneFacility(tags["cycleway"]) {
		if tags["oneway"] == "yes" || tags["oneway"] == "1" || tags["oneway"] == "true" {
			return 1
		}
		return 2
	}
	return count
}

func laneFacility(value string) bool {
	return value == "lane" || value == "opposite_lane"
}

func taggedParkingLaneCount(tags map[string]string) int {
	if parkingFacility(tags["parking:lane:both"]) || parkingFacility(tags["parking:both"]) {
		return 2
	}
	count := 0
	if parkingFacility(tags["parking:lane:left"]) || parkingFacility(tags["parking:left"]) {
		count++
	}
	if parkingFacility(tags["parking:lane:right"]) || parkingFacility(tags["parking:right"]) {
		count++
	}
	return count
}

func parkingFacility(value string) bool {
	switch value {
	case "parallel", "diagonal", "perpendicular", "marked", "on_street", "yes":
		return true
	default:
		return false
	}
}
