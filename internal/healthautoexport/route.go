package healthautoexport

import (
	"encoding/json"
	"math"
)

func parseRoute(s *tokenStream, warnings []Warning) ([]RoutePoint, []Warning, error) {
	token, err := s.token()
	if err != nil {
		return nil, nil, err
	}
	if token == nil {
		return nil, warnings, nil
	}
	if token != json.Delim('[') {
		return nil, nil, parseError(ErrorInvalidRoute)
	}
	points := make([]RoutePoint, 0)
	for s.decoder.More() {
		if len(points) >= s.limits.MaxRoutePoints {
			return nil, nil, parseError(ErrorCollectionLimit)
		}
		point, pointWarnings, err := parseRoutePoint(s, len(points))
		if err != nil {
			if parsed, ok := err.(*ParseError); ok {
				parsed.RoutePoint = len(points)
			}
			return nil, nil, err
		}
		points = append(points, point)
		warnings = append(warnings, pointWarnings...)
	}
	if err := s.expectDelim(']'); err != nil {
		return nil, nil, err
	}
	if len(points) == 0 {
		return nil, warnings, nil
	}
	return points, warnings, nil
}

func parseRoutePoint(s *tokenStream, sequence int) (RoutePoint, []Warning, error) {
	if err := s.expectDelim('{'); err != nil {
		return RoutePoint{}, nil, parseError(ErrorInvalidRoute)
	}
	point := RoutePoint{Sequence: sequence}
	warnings := make([]Warning, 0)
	seen := make(map[string]struct{})
	hasTimestamp, hasLatitude, hasLongitude := false, false, false
	for s.decoder.More() {
		key, err := s.objectKey(seen)
		if err != nil {
			return RoutePoint{}, nil, err
		}
		switch key {
		case "timestamp":
			point.Timestamp, hasTimestamp, err = timestampToken(s)
			if err == nil {
				point.TimestampOffsetMins = offsetMinutes(point.Timestamp)
			}
		case "latitude":
			point.Latitude, hasLatitude, err = numberToken(s, s.limits.MaxNumericMagnitude)
			if err == nil && (point.Latitude < -90 || point.Latitude > 90) {
				err = parseError(ErrorInvalidRoute)
			}
		case "longitude":
			point.Longitude, hasLongitude, err = numberToken(s, s.limits.MaxNumericMagnitude)
			if err == nil && (point.Longitude < -180 || point.Longitude > 180) {
				err = parseError(ErrorInvalidRoute)
			}
		case "altitude":
			point.Altitude, warnings, err = optionalRouteNumber(s, -1e6, 1e6, false, WarningFieldAltitude, sequence, warnings)
		case "speed":
			point.Speed, warnings, err = optionalRouteNumber(s, 0, 1e4, true, WarningFieldRouteSpeed, sequence, warnings)
		case "course":
			point.Course, warnings, err = optionalRouteNumber(s, 0, 360, true, WarningFieldCourse, sequence, warnings)
		case "horizontalAccuracy":
			point.HorizontalAccuracy, warnings, err = optionalRouteNumber(s, 0, 1e6, true, WarningFieldHorizontalAccuracy, sequence, warnings)
		case "verticalAccuracy":
			point.VerticalAccuracy, warnings, err = optionalRouteNumber(s, 0, 1e6, true, WarningFieldVerticalAccuracy, sequence, warnings)
		case "speedAccuracy":
			point.SpeedAccuracy, warnings, err = optionalRouteNumber(s, 0, 1e6, true, WarningFieldSpeedAccuracy, sequence, warnings)
		case "courseAccuracy":
			point.CourseAccuracy, warnings, err = optionalRouteNumber(s, 0, 360, true, WarningFieldCourseAccuracy, sequence, warnings)
		default:
			err = s.skipValue()
		}
		if err != nil {
			return RoutePoint{}, nil, err
		}
	}
	if err := s.expectDelim('}'); err != nil {
		return RoutePoint{}, nil, err
	}
	if !hasTimestamp || !hasLatitude || !hasLongitude {
		return RoutePoint{}, nil, parseError(ErrorInvalidRoute)
	}
	return point, warnings, nil
}

func optionalRouteNumber(s *tokenStream, min, max float64, negativeUnavailable bool, field WarningField, sequence int, warnings []Warning) (*float64, []Warning, error) {
	start := s.decoder.InputOffset()
	token, err := s.token()
	if err != nil {
		return nil, warnings, err
	}
	if token == nil {
		return nil, warnings, nil
	}
	number, ok := token.(json.Number)
	if !ok {
		if err := s.skipTokenValue(token, start, 0); err != nil {
			return nil, warnings, err
		}
		return nil, append(warnings, Warning{Code: WarningInvalidOptionalRouteValue, Field: field, RoutePoint: sequence}), nil
	}
	value, err := number.Float64()
	if err != nil || !finiteInRange(value, math.Max(math.Abs(min), math.Abs(max))) {
		return nil, append(warnings, Warning{Code: WarningInvalidOptionalRouteValue, Field: field, RoutePoint: sequence}), nil
	}
	if negativeUnavailable && value < 0 {
		return nil, warnings, nil
	}
	if value < min || value > max {
		return nil, append(warnings, Warning{Code: WarningInvalidOptionalRouteValue, Field: field, RoutePoint: sequence}), nil
	}
	return &value, warnings, nil
}

func finiteInRange(value, max float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && math.Abs(value) <= max
}
