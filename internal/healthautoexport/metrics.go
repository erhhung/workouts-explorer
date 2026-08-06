package healthautoexport

import (
	"encoding/json"
	"sort"
)

type metricSpec struct {
	key      MetricKey
	expected string
	field    WarningField
}

var directMetricSpecs = map[string]metricSpec{
	"activeEnergyBurned": {MetricActiveEnergyBurned, "kcal", WarningFieldActiveEnergyBurned},
	"avgHeartRate":       {MetricAverageHeartRate, "count/min", WarningFieldAverageHeartRate},
	"avgSpeed":           {MetricAverageSpeed, "km/hr", WarningFieldAverageSpeed},
	"distance":           {MetricDistance, "km", WarningFieldDistance},
	"elevationUp":        {MetricElevationUp, "m", WarningFieldElevationUp},
	"flightsClimbed":     {MetricFlightsClimbed, "count", WarningFieldFlightsClimbed},
	"humidity":           {MetricHumidity, "%", WarningFieldHumidity},
	"intensity":          {MetricIntensity, "kcal/hr·kg", WarningFieldIntensity},
	"maxHeartRate":       {MetricMaximumHeartRate, "count/min", WarningFieldMaximumHeartRate},
	"maxSpeed":           {MetricMaximumSpeed, "km/hr", WarningFieldMaximumSpeed},
	"speed":              {MetricSpeed, "km/hr", WarningFieldSpeed},
	"stepCadence":        {MetricStepCadence, "count/min", WarningFieldStepCadence},
	"temperature":        {MetricTemperature, "degC", WarningFieldTemperature},
	"totalEnergy":        {MetricTotalEnergy, "kcal", WarningFieldTotalEnergy},
}

var nestedHeartRateSpecs = map[string]metricSpec{
	"avg": {MetricAverageHeartRate, "count/min", WarningFieldAverageHeartRate},
	"max": {MetricMaximumHeartRate, "count/min", WarningFieldMaximumHeartRate},
	"min": {MetricHeartRateMinimum, "count/min", WarningFieldHeartRateMinimum},
}

type metricCandidate struct {
	aggregate Aggregate
}

func parseHeartRate(s *tokenStream, b *workoutBuilder) error {
	token, err := s.token()
	if err != nil {
		return err
	}
	if token == nil {
		return nil
	}
	if token != json.Delim('{') {
		return parseError(ErrorInvalidWorkout)
	}
	seen := make(map[string]struct{})
	for s.decoder.More() {
		key, err := s.objectKey(seen)
		if err != nil {
			return err
		}
		if spec, ok := nestedHeartRateSpecs[key]; ok {
			err = parseMetric(s, spec, OriginHeartRate, b.nestedMetrics, &b.workout.Warnings)
		} else {
			err = s.skipValue()
		}
		if err != nil {
			return err
		}
	}
	return s.expectDelim('}')
}

func parseMetric(s *tokenStream, spec metricSpec, origin AggregateOrigin, destination map[MetricKey]metricCandidate, warnings *[]Warning) error {
	token, err := s.token()
	if err != nil {
		return err
	}
	if token == nil {
		*warnings = append(*warnings, Warning{Code: WarningIncompleteMetric, Field: spec.field, RoutePoint: -1})
		return nil
	}
	if token != json.Delim('{') {
		return parseError(ErrorInvalidWorkout)
	}
	seen := make(map[string]struct{})
	var qty Decimal
	var units string
	qtyValid, unitsValid := false, false
	incomplete := false
	for s.decoder.More() {
		key, err := s.objectKey(seen)
		if err != nil {
			return err
		}
		switch key {
		case "qty":
			token, err := s.token()
			if err != nil {
				return err
			}
			if token == nil {
				incomplete = true
				continue
			}
			number, ok := token.(json.Number)
			if !ok {
				return parseError(ErrorInvalidNumber)
			}
			qty, qtyValid, err = parseJSONDecimal(number, s.limits.MaxDecimalMagnitude)
			if err != nil {
				return err
			}
		case "units":
			token, err := s.token()
			if err != nil {
				return err
			}
			if token == nil {
				incomplete = true
				continue
			}
			value, ok := token.(string)
			if !ok {
				return parseError(ErrorInvalidWorkout)
			}
			if len(value) > s.limits.MaxUnitBytes {
				return parseError(ErrorStringLimit)
			}
			units, unitsValid = value, value != ""
		default:
			if err := s.skipValue(); err != nil {
				return err
			}
		}
	}
	if err := s.expectDelim('}'); err != nil {
		return err
	}
	if incomplete || !qtyValid || !unitsValid {
		*warnings = append(*warnings, Warning{Code: WarningIncompleteMetric, Field: spec.field, RoutePoint: -1})
		return nil
	}
	if units != spec.expected {
		*warnings = append(*warnings, Warning{Code: WarningUnexpectedUnit, Field: spec.field, RoutePoint: -1})
	}
	destination[spec.key] = metricCandidate{Aggregate{Key: spec.key, Qty: qty, Units: units, Origin: origin}}
	return nil
}

func parseJSONDecimal(number json.Number, max Decimal) (Decimal, bool, error) {
	value, err := ParseDecimal(string(number))
	if err != nil || value.compareAbs(max) > 0 {
		return Decimal{}, false, parseError(ErrorInvalidNumber)
	}
	return value, true, nil
}

func resolveMetrics(direct, nested map[MetricKey]metricCandidate) []Aggregate {
	// Direct avg/max heart-rate aggregates explicitly take precedence over the
	// equivalent nested heartRate values, independent of provider key order.
	resolved := make(map[MetricKey]Aggregate, len(direct)+len(nested))
	for key, candidate := range nested {
		resolved[key] = candidate.aggregate
	}
	for key, candidate := range direct {
		resolved[key] = candidate.aggregate
	}
	result := make([]Aggregate, 0, len(resolved))
	for _, aggregate := range resolved {
		result = append(result, aggregate)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Key < result[j].Key })
	return result
}

func sortWarnings(warnings []Warning) {
	sort.SliceStable(warnings, func(i, j int) bool {
		if warnings[i].Code != warnings[j].Code {
			return warnings[i].Code < warnings[j].Code
		}
		if warnings[i].Field != warnings[j].Field {
			return warnings[i].Field < warnings[j].Field
		}
		return warnings[i].RoutePoint < warnings[j].RoutePoint
	})
}
