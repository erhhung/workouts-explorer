package healthautoexport

import (
	"encoding/json"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

const providerTimeLayout = "2006-01-02 15:04:05 -0700"

type workoutBuilder struct {
	workout                      Workout
	hasName, hasStart, hasEnd    bool
	hasDuration                  bool
	directMetrics, nestedMetrics map[MetricKey]metricCandidate
}

func Parse(r io.Reader) (Document, error) { return ParseWithLimits(r, DefaultLimits()) }

// ParseWithLimits token-streams only data.workouts. Unknown fields are walked
// for bounds and duplicate keys, then discarded without materialization.
func ParseWithLimits(r io.Reader, limits Limits) (Document, error) {
	if !validLimits(limits) {
		return Document{}, parseError(ErrorInvalidLimits)
	}
	stream, limited := newTokenStream(r, limits)
	doc, err := parseRoot(stream)
	if err != nil {
		if limited.N <= 0 {
			return Document{}, parseError(ErrorInputLimit)
		}
		return Document{}, err
	}
	if err := stream.finish(); err != nil {
		if limited.N <= 0 {
			return Document{}, parseError(ErrorInputLimit)
		}
		return Document{}, err
	}
	if limited.N <= 0 {
		return Document{}, parseError(ErrorInputLimit)
	}
	return doc, nil
}

func validLimits(l Limits) bool {
	return l.MaxInputBytes > 0 && l.MaxInputBytes < math.MaxInt64 && l.MaxWorkouts > 0 && l.MaxRoutePoints > 0 &&
		l.MaxAggregates > 0 && l.MaxStringBytes > 0 && l.MaxUnitBytes > 0 &&
		l.MaxUnknownValueBytes > 0 && l.MaxUnknownCollectionItems > 0 && l.MaxNestingDepth > 0 &&
		l.MaxNumericMagnitude > 0 && !math.IsInf(l.MaxNumericMagnitude, 0) && !math.IsNaN(l.MaxNumericMagnitude) &&
		l.MaxDecimalMagnitude.IsPositive()
}

func parseRoot(s *tokenStream) (Document, error) {
	token, err := s.token()
	if err != nil {
		return Document{}, err
	}
	if token != json.Delim('{') {
		return Document{}, parseError(ErrorInvalidRoot)
	}
	seen := make(map[string]struct{})
	foundData := false
	var doc Document
	for s.decoder.More() {
		key, err := s.objectKey(seen)
		if err != nil {
			return Document{}, err
		}
		if key == "data" {
			foundData = true
			doc, err = parseData(s)
		} else {
			err = s.skipValue()
		}
		if err != nil {
			return Document{}, err
		}
	}
	if err := s.expectDelim('}'); err != nil {
		return Document{}, err
	}
	if !foundData {
		return Document{}, parseError(ErrorInvalidRoot)
	}
	return doc, nil
}

func parseData(s *tokenStream) (Document, error) {
	token, err := s.token()
	if err != nil {
		return Document{}, err
	}
	if token != json.Delim('{') {
		return Document{}, parseError(ErrorInvalidData)
	}
	seen := make(map[string]struct{})
	foundWorkouts := false
	var doc Document
	for s.decoder.More() {
		key, err := s.objectKey(seen)
		if err != nil {
			return Document{}, err
		}
		if key == "workouts" {
			foundWorkouts = true
			doc.Workouts, err = parseWorkouts(s)
		} else {
			err = s.skipValue()
		}
		if err != nil {
			return Document{}, err
		}
	}
	if err := s.expectDelim('}'); err != nil {
		return Document{}, err
	}
	if !foundWorkouts {
		return Document{}, parseError(ErrorInvalidData)
	}
	return doc, nil
}

func parseWorkouts(s *tokenStream) ([]Workout, error) {
	token, err := s.token()
	if err != nil {
		return nil, err
	}
	if token != json.Delim('[') {
		return nil, parseError(ErrorInvalidData)
	}
	workouts := make([]Workout, 0)
	providerIDs := make(map[string]struct{})
	for s.decoder.More() {
		if len(workouts) >= s.limits.MaxWorkouts {
			return nil, parseError(ErrorCollectionLimit)
		}
		workout, err := parseWorkout(s)
		if err != nil {
			if parsed, ok := err.(*ParseError); ok {
				parsed.Workout = len(workouts)
			}
			return nil, err
		}
		if workout.ProviderID != "" {
			if _, duplicate := providerIDs[workout.ProviderID]; duplicate {
				err := parseError(ErrorDuplicateProviderID)
				err.Workout = len(workouts)
				return nil, err
			}
			providerIDs[workout.ProviderID] = struct{}{}
		}
		workouts = append(workouts, workout)
	}
	if err := s.expectDelim(']'); err != nil {
		return nil, err
	}
	return workouts, nil
}

func parseWorkout(s *tokenStream) (Workout, error) {
	if err := s.expectDelim('{'); err != nil {
		return Workout{}, parseError(ErrorInvalidWorkout)
	}
	b := workoutBuilder{directMetrics: make(map[MetricKey]metricCandidate), nestedMetrics: make(map[MetricKey]metricCandidate)}
	seen := make(map[string]struct{})
	for s.decoder.More() {
		key, err := s.objectKey(seen)
		if err != nil {
			return Workout{}, err
		}
		switch key {
		case "id":
			b.workout.ProviderID, err = optionalOpaqueString(s)
		case "name":
			b.workout.ProviderLabel, b.hasName, err = requiredStringToken(s)
		case "start":
			b.workout.Start, b.hasStart, err = timestampToken(s)
		case "end":
			b.workout.End, b.hasEnd, err = timestampToken(s)
		case "duration":
			b.workout.ProviderDuration, b.hasDuration, err = decimalToken(s, s.limits.MaxDecimalMagnitude)
		case "isIndoor":
			b.workout.IsIndoor, err = optionalBoolToken(s)
		case "location":
			b.workout.Location, err = optionalStringToken(s)
		case "heartRate":
			err = parseHeartRate(s, &b)
		case "route":
			b.workout.Route, b.workout.Warnings, err = parseRoute(s, b.workout.Warnings)
		default:
			if spec, ok := directMetricSpecs[key]; ok {
				err = parseMetric(s, spec, OriginDirect, b.directMetrics, &b.workout.Warnings)
			} else {
				err = s.skipValue()
			}
		}
		if err != nil {
			return Workout{}, err
		}
	}
	if err := s.expectDelim('}'); err != nil {
		return Workout{}, err
	}
	if !b.hasName || !b.hasStart || !b.hasEnd || !b.hasDuration || strings.TrimSpace(b.workout.ProviderLabel) == "" ||
		!b.workout.ProviderDuration.IsPositive() || b.workout.End.Before(b.workout.Start) {
		return Workout{}, parseError(ErrorInvalidWorkout)
	}
	b.workout.StartOffsetMins = offsetMinutes(b.workout.Start)
	b.workout.EndOffsetMins = offsetMinutes(b.workout.End)
	b.workout.LocalStartDate = time.Date(b.workout.Start.Year(), b.workout.Start.Month(), b.workout.Start.Day(), 0, 0, 0, 0, time.UTC)
	b.workout.TypeKey = NormalizedTypeKey(b.workout.ProviderLabel)
	b.workout.Aggregates = resolveMetrics(b.directMetrics, b.nestedMetrics)
	if len(b.workout.Aggregates) > s.limits.MaxAggregates {
		return Workout{}, parseError(ErrorCollectionLimit)
	}
	sortWarnings(b.workout.Warnings)
	b.workout.ContentSHA256 = contentHash(b.workout)
	if b.workout.ProviderID == "" {
		b.workout.FallbackSHA256 = fallbackHash(b.workout)
	}
	return b.workout, nil
}

func requiredStringToken(s *tokenStream) (string, bool, error) {
	token, err := s.token()
	if err != nil {
		return "", false, err
	}
	value, ok := token.(string)
	if !ok {
		return "", false, parseError(ErrorInvalidWorkout)
	}
	return value, true, nil
}

func optionalOpaqueString(s *tokenStream) (string, error) {
	token, err := s.token()
	if err != nil {
		return "", err
	}
	if token == nil {
		return "", nil
	}
	value, ok := token.(string)
	if !ok {
		return "", parseError(ErrorInvalidWorkout)
	}
	return value, nil
}

func optionalStringToken(s *tokenStream) (*string, error) {
	token, err := s.token()
	if err != nil {
		return nil, err
	}
	if token == nil {
		return nil, nil
	}
	value, ok := token.(string)
	if !ok {
		return nil, parseError(ErrorInvalidWorkout)
	}
	return &value, nil
}

func optionalBoolToken(s *tokenStream) (*bool, error) {
	token, err := s.token()
	if err != nil {
		return nil, err
	}
	if token == nil {
		return nil, nil
	}
	value, ok := token.(bool)
	if !ok {
		return nil, parseError(ErrorInvalidWorkout)
	}
	return &value, nil
}

func timestampToken(s *tokenStream) (time.Time, bool, error) {
	value, present, err := requiredStringToken(s)
	if err != nil {
		return time.Time{}, false, parseError(ErrorInvalidTimestamp)
	}
	parsed, err := time.Parse(providerTimeLayout, value)
	if err != nil {
		return time.Time{}, false, parseError(ErrorInvalidTimestamp)
	}
	return parsed, present, nil
}

func numberToken(s *tokenStream, max float64) (float64, bool, error) {
	token, err := s.token()
	if err != nil {
		return 0, false, err
	}
	number, ok := token.(json.Number)
	if !ok {
		return 0, false, parseError(ErrorInvalidNumber)
	}
	value, err := strconv.ParseFloat(string(number), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || math.Abs(value) > max {
		return 0, false, parseError(ErrorInvalidNumber)
	}
	return value, true, nil
}

func decimalToken(s *tokenStream, max Decimal) (Decimal, bool, error) {
	token, err := s.token()
	if err != nil {
		return Decimal{}, false, err
	}
	number, ok := token.(json.Number)
	if !ok {
		return Decimal{}, false, parseError(ErrorInvalidNumber)
	}
	value, err := ParseDecimal(string(number))
	if err != nil || value.compareAbs(max) > 0 {
		return Decimal{}, false, parseError(ErrorInvalidNumber)
	}
	return value, true, nil
}

func offsetMinutes(value time.Time) int {
	_, seconds := value.Zone()
	return seconds / 60
}
