package healthautoexport

import (
	"encoding/hex"
	"errors"
	"io"
	"math"
	"strings"
	"testing"
	"unicode/utf8"
)

const syntheticWorkout = `{
  "id":"opaque/provider:v1",
  "name":"Outdoor Run",
  "start":"2026-04-11 19:24:01 -0700",
  "end":"2026-04-11 20:02:44 -0700",
  "duration":2323.5488079786301,
  "isIndoor":null,
  "location":null,
  "avgHeartRate":{"qty":120,"units":"count/min"},
  "maxHeartRate":{"qty":150,"units":"count/min"},
  "avgSpeed":{"qty":8,"units":"km"},
  "distance":{"qty":null,"units":"km"},
  "totalEnergy":{"qty":300,"units":null},
  "heartRate":{"avg":{"qty":999,"units":"count/min"},"max":{"qty":151,"units":"count/min"},"min":{"qty":90,"units":"count/min"},"extension":{"deep":[1,{"safe":true}]}},
  "providerMetric":{"qty":123,"units":"mystery","samples":[1,2,3]},
  "route":[
    {"timestamp":"2026-04-11 19:24:02 -0700","latitude":37.1,"longitude":-122.1,"altitude":10,"speed":2,"course":90,"horizontalAccuracy":3,"verticalAccuracy":4,"speedAccuracy":0.5,"courseAccuracy":8,"extension":{"x":[1]}},
    {"timestamp":"2026-04-11 19:24:02 -0700","latitude":37.2,"longitude":-122.2,"speed":-1,"course":361,"horizontalAccuracy":{"invalid":[1,2]}}
  ],
  "heartRateData":[{"qty":999999,"source":"discarded"}],
  "metadata":{"device":"discarded","nested":[{"safe":true}]}
}`

func document(workouts ...string) string {
	return `{"extension":{"array":[1,{"ok":true}]},"data":{"extension":false,"workouts":[` + strings.Join(workouts, ",") + `]}}`
}

func TestParseProviderModelMetricsAndRoute(t *testing.T) {
	doc, err := Parse(strings.NewReader(document(syntheticWorkout)))
	if err != nil {
		t.Fatal(err)
	}
	w := doc.Workouts[0]
	if w.ProviderID != "opaque/provider:v1" || w.ProviderDuration.String() != "2323.5488079786301" {
		t.Fatalf("provider identity/duration changed: %#v", w)
	}
	if w.FallbackSHA256 != ([32]byte{}) || w.StartOffsetMins != -420 || w.EndOffsetMins != -420 || w.LocalStartDate.String()[:10] != "2026-04-11" {
		t.Fatalf("incorrect identity/time normalization: %#v", w)
	}
	if w.IsIndoor != nil || w.Location != nil {
		t.Fatal("nullable workout values were not preserved")
	}
	assertMetric(t, w, MetricAverageHeartRate, "120", "count/min", OriginDirect)
	assertMetric(t, w, MetricMaximumHeartRate, "150", "count/min", OriginDirect)
	assertMetric(t, w, MetricHeartRateMinimum, "90", "count/min", OriginHeartRate)
	assertMetric(t, w, MetricAverageSpeed, "8", "km", OriginDirect)
	if findMetric(w, MetricDistance) != nil || findMetric(w, MetricTotalEnergy) != nil {
		t.Fatal("nullable metric was retained")
	}
	assertWarning(t, w.Warnings, WarningIncompleteMetric, WarningFieldDistance)
	assertWarning(t, w.Warnings, WarningIncompleteMetric, WarningFieldTotalEnergy)
	if len(w.Route) != 2 || w.Route[0].Sequence != 0 || w.Route[1].Sequence != 1 || !w.Route[0].Timestamp.Equal(w.Route[1].Timestamp) {
		t.Fatalf("route ordering/duplicates changed: %#v", w.Route)
	}
	if w.Route[0].TimestampOffsetMins != -420 || w.Route[1].TimestampOffsetMins != -420 {
		t.Fatalf("route offsets not retained: %#v", w.Route)
	}
	if w.Route[1].Speed != nil || w.Route[1].Course != nil || w.Route[1].HorizontalAccuracy != nil {
		t.Fatalf("invalid optional route values retained: %#v", w.Route[1])
	}
	assertWarning(t, w.Warnings, WarningInvalidOptionalRouteValue, WarningFieldCourse)
}

func TestOptionalOpaqueProviderIDsAndFallback(t *testing.T) {
	absent := strings.Replace(syntheticWorkout, `"id":"opaque/provider:v1",`, "", 1)
	nullID := strings.Replace(syntheticWorkout, `"id":"opaque/provider:v1"`, `"id":null`, 1)
	reordered := strings.Replace(absent, `"metadata":{"device":"discarded","nested":[{"safe":true}]}`, `"metadata":["different",{"shape":true}]`, 1)
	first := parseOne(t, absent)
	second := parseOne(t, reordered)
	third := parseOne(t, nullID)
	if first.ProviderID != "" || first.FallbackSHA256 == ([32]byte{}) {
		t.Fatalf("missing ID fallback not populated: %#v", first)
	}
	if first.FallbackSHA256 != second.FallbackSHA256 || first.ContentSHA256 != second.ContentSHA256 || first.FallbackSHA256 != third.FallbackSHA256 {
		t.Fatal("fallback/content identity depends on ignored extensions or null-vs-absent ID")
	}
	if parseOne(t, syntheticWorkout).ProviderID != "opaque/provider:v1" {
		t.Fatal("opaque provider ID was interpreted")
	}
}

func TestFallbackAndContentHashesIgnoreWorkoutKeyOrder(t *testing.T) {
	firstJSON := `{"name":"Walk","start":"2026-01-02 03:04:05 -0700","end":"2026-01-02 03:05:05 -0700","duration":60,"distance":{"qty":1,"units":"km"},"extension":[1,2]}`
	secondJSON := `{"extension":{"different":true},"distance":{"units":"km","qty":1},"duration":60,"end":"2026-01-02 03:05:05 -0700","start":"2026-01-02 03:04:05 -0700","name":"Walk"}`
	first, second := parseOne(t, firstJSON), parseOne(t, secondJSON)
	if first.ContentSHA256 != second.ContentSHA256 || first.FallbackSHA256 != second.FallbackSHA256 {
		t.Fatal("normalized hashes depend on provider key order or ignored extensions")
	}
}

func TestFallbackFingerprintStableAcrossMutableContent(t *testing.T) {
	base := `{"name":"Walk","start":"2026-01-02 03:04:05 -0700","end":"2026-01-02 03:05:05 -0700","duration":60,"distance":{"qty":1,"units":"km"}}`
	mutable := `{"name":"Walk","start":"2026-01-02 03:04:05 -0700","end":"2026-01-02 03:06:05 -0700","duration":120,"distance":{"qty":9,"units":"km"},"route":[{"timestamp":"2026-01-02 03:04:06 -0700","latitude":1,"longitude":2}]}`
	first, changed := parseOne(t, base), parseOne(t, mutable)
	if FallbackFingerprintVersion != "health-auto-export-fallback-v2" || hex.EncodeToString(first.FallbackSHA256[:]) != "3a624db8fa6d92aa80bf2bbc18e732f49ea05fc6a613bc0aa8143f8ddecda7c6" {
		t.Fatalf("fallback fingerprint wire format changed: %s/%x", FallbackFingerprintVersion, first.FallbackSHA256)
	}
	if first.FallbackSHA256 != changed.FallbackSHA256 {
		t.Fatal("mutable workout content changed fallback identity")
	}
	if first.ContentSHA256 == changed.ContentSHA256 {
		t.Fatal("mutable workout content did not change content identity")
	}
	changedStart := parseOne(t, strings.Replace(base, "03:04:05", "03:04:06", 1))
	changedType := parseOne(t, strings.Replace(base, `"name":"Walk"`, `"name":"Run"`, 1))
	if first.FallbackSHA256 == changedStart.FallbackSHA256 || first.FallbackSHA256 == changedType.FallbackSHA256 {
		t.Fatal("meaningful provider identity change retained fallback identity")
	}
}

func TestMetricPrecedenceIsProviderKeyOrderIndependent(t *testing.T) {
	reversed := strings.Replace(syntheticWorkout,
		`"avgHeartRate":{"qty":120,"units":"count/min"},`, "", 1)
	reversed = strings.Replace(reversed, `"heartRateData":`, `"avgHeartRate":{"qty":120,"units":"count/min"},"heartRateData":`, 1)
	w := parseOne(t, reversed)
	assertMetric(t, w, MetricAverageHeartRate, "120", "count/min", OriginDirect)
	unknownUnit := strings.Replace(syntheticWorkout, `"avgSpeed":{"qty":8,"units":"km"}`, `"avgSpeed":{"qty":8,"units":"furlongs/fortnight"}`, 1)
	w = parseOne(t, unknownUnit)
	assertMetric(t, w, MetricAverageSpeed, "8", "furlongs/fortnight", OriginDirect)
	assertWarning(t, w.Warnings, WarningUnexpectedUnit, WarningFieldAverageSpeed)
}

func TestAverageAndMaximumSpeedAcceptObservedProviderUnits(t *testing.T) {
	tests := []struct {
		field string
		key   MetricKey
	}{
		{"avgSpeed", MetricAverageSpeed},
		{"maxSpeed", MetricMaximumSpeed},
	}
	for _, test := range tests {
		for _, unit := range []string{"km", "km/hr"} {
			t.Run(test.field+"/"+unit, func(t *testing.T) {
				workout := `{"name":"Walk","start":"2026-01-02 03:04:05 -0700","end":"2026-01-02 03:05:05 -0700","duration":60,"` + test.field + `":{"qty":8,"units":"` + unit + `"}}`
				parsed := parseOne(t, workout)
				assertMetric(t, parsed, test.key, "8", unit, OriginDirect)
				if len(parsed.Warnings) != 0 {
					t.Fatalf("warnings = %#v", parsed.Warnings)
				}
			})
		}
	}
}

func TestRouteTimestampOffsetCanDifferFromWorkout(t *testing.T) {
	changed := strings.Replace(syntheticWorkout, `"timestamp":"2026-04-11 19:24:02 -0700","latitude":37.2`, `"timestamp":"2026-04-12 07:54:02 +0530","latitude":37.2`, 1)
	w := parseOne(t, changed)
	if w.Route[0].TimestampOffsetMins != -420 || w.Route[1].TimestampOffsetMins != 330 {
		t.Fatalf("route offsets = %d/%d", w.Route[0].TimestampOffsetMins, w.Route[1].TimestampOffsetMins)
	}
}

func TestOptionalRouteWarningsUseIndependentFields(t *testing.T) {
	workout := `{"name":"Walk","start":"2026-01-02 03:04:05 -0700","end":"2026-01-02 03:05:05 -0700","duration":60,"route":[{"timestamp":"2026-01-02 03:04:06 -0700","latitude":1,"longitude":2,"altitude":1000001,"speed":10001,"course":361,"horizontalAccuracy":1000001,"verticalAccuracy":1000001,"speedAccuracy":1000001,"courseAccuracy":361}]}`
	parsed := parseOne(t, workout)
	warnings := parsed.Warnings
	want := []WarningField{
		WarningFieldAltitude, WarningFieldRouteSpeed, WarningFieldCourse,
		WarningFieldHorizontalAccuracy, WarningFieldVerticalAccuracy,
		WarningFieldSpeedAccuracy,
	}
	counts := make(map[WarningField]int)
	for _, warning := range warnings {
		if warning.Code == WarningInvalidOptionalRouteValue {
			counts[warning.Field]++
		}
	}
	for _, field := range want {
		if counts[field] != 1 {
			t.Errorf("warning field %s count = %d", field, counts[field])
		}
	}
	if len(counts) != len(want) {
		t.Fatalf("unexpected route warning fields: %v", counts)
	}
	point := parsed.Route[0]
	if point.CourseAccuracy == nil || *point.CourseAccuracy != maximumCourseAccuracy {
		t.Fatalf("course accuracy = %v", point.CourseAccuracy)
	}
}

func TestInvalidCourseAccuracyIsSilentlyNormalized(t *testing.T) {
	workout := `{"name":"Walk","start":"2026-01-02 03:04:05 -0700","end":"2026-01-02 03:05:05 -0700","duration":60,"route":[{"timestamp":"2026-01-02 03:04:06 -0700","latitude":1,"longitude":2,"courseAccuracy":"unknown"},{"timestamp":"2026-01-02 03:04:07 -0700","latitude":1,"longitude":2,"courseAccuracy":9999}]}`
	parsed := parseOne(t, workout)
	if parsed.Route[0].CourseAccuracy != nil || parsed.Route[1].CourseAccuracy == nil || *parsed.Route[1].CourseAccuracy != maximumCourseAccuracy {
		t.Fatalf("course accuracy values = %v/%v", parsed.Route[0].CourseAccuracy, parsed.Route[1].CourseAccuracy)
	}
	if len(parsed.Warnings) != 0 {
		t.Fatalf("warnings = %#v", parsed.Warnings)
	}
}

func TestNegativeRouteQualityValuesAreUnavailable(t *testing.T) {
	workout := `{"name":"Walk","start":"2026-01-02 03:04:05 -0700","end":"2026-01-02 03:05:05 -0700","duration":60,"route":[{"timestamp":"2026-01-02 03:04:06 -0700","latitude":1,"longitude":2,"altitude":-5,"speed":-1,"course":-1,"horizontalAccuracy":-1,"verticalAccuracy":-1,"speedAccuracy":-1,"courseAccuracy":-1}]}`
	parsed := parseOne(t, workout)
	point := parsed.Route[0]
	if point.Altitude == nil || *point.Altitude != -5 {
		t.Fatalf("altitude = %v", point.Altitude)
	}
	if point.Speed != nil || point.Course != nil || point.HorizontalAccuracy != nil || point.VerticalAccuracy != nil || point.SpeedAccuracy != nil || point.CourseAccuracy != nil {
		t.Fatalf("unavailable route quality values retained: %#v", point)
	}
	if len(parsed.Warnings) != 0 {
		t.Fatalf("warnings = %#v", parsed.Warnings)
	}
}

func TestDuplicateProviderIDs(t *testing.T) {
	_, err := Parse(strings.NewReader(document(syntheticWorkout, syntheticWorkout)))
	assertErrorCode(t, err, ErrorDuplicateProviderID)
	empty := strings.Replace(syntheticWorkout, `"id":"opaque/provider:v1"`, `"id":""`, 1)
	if _, err := Parse(strings.NewReader(document(empty, empty))); err != nil {
		t.Fatalf("empty IDs should use independent fallbacks: %v", err)
	}
}

func TestDuplicateKeysAtEveryObjectLevel(t *testing.T) {
	tests := map[string]string{
		"root":              `{"data":{"workouts":[]},"data":{"workouts":[]}}`,
		"data":              `{"data":{"workouts":[],"workouts":[]}}`,
		"workout":           document(strings.Replace(syntheticWorkout, `"name":"Outdoor Run"`, `"name":"Outdoor Run","name":"Run"`, 1)),
		"metric":            document(strings.Replace(syntheticWorkout, `"qty":120`, `"qty":120,"qty":121`, 1)),
		"heart rate":        document(strings.Replace(syntheticWorkout, `"avg":{"qty":999`, `"avg":{"qty":1,"units":"count/min"},"avg":{"qty":999`, 1)),
		"route point":       document(strings.Replace(syntheticWorkout, `"latitude":37.1`, `"latitude":37.1,"latitude":37.2`, 1)),
		"unknown extension": document(strings.Replace(syntheticWorkout, `"nested":[{"safe":true}]`, `"nested":[{"safe":true,"safe":false}]`, 1)),
		"escaped key":       document(strings.Replace(syntheticWorkout, `"nested":[{"safe":true}]`, `"nested":[{"safe":true,"s\u0061fe":false}]`, 1)),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(input))
			assertErrorCode(t, err, ErrorDuplicateKey)
		})
	}
}

func TestUnknownExtensionsAreBoundedAndIgnored(t *testing.T) {
	base := parseOne(t, syntheticWorkout)
	changed := strings.Replace(syntheticWorkout, `"providerMetric":{"qty":123,"units":"mystery","samples":[1,2,3]}`, `"providerMetric":[{"arbitrary":{"shape":"changed"}}]`, 1)
	other := parseOne(t, changed)
	if base.ContentSHA256 != other.ContentSHA256 || len(base.Aggregates) != len(other.Aggregates) {
		t.Fatal("unknown extension affected normalized content")
	}

	limits := DefaultLimits()
	limits.MaxUnknownCollectionItems = 2
	_, err := ParseWithLimits(strings.NewReader(document(syntheticWorkout)), limits)
	assertErrorCode(t, err, ErrorCollectionLimit)
	limits = DefaultLimits()
	limits.MaxUnknownValueBytes = 8
	_, err = ParseWithLimits(strings.NewReader(document(syntheticWorkout)), limits)
	assertErrorCode(t, err, ErrorUnknownValueLimit)
	limits = DefaultLimits()
	limits.MaxStringBytes = 16
	input := strings.Replace(syntheticWorkout, `"discarded"`, `"this unknown value is too long"`, 1)
	_, err = ParseWithLimits(strings.NewReader(document(input)), limits)
	assertErrorCode(t, err, ErrorStringLimit)
}

func TestCollectionLimitsCheckedBeforeAppend(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxWorkouts = 1
	_, err := ParseWithLimits(strings.NewReader(document(syntheticWorkout, syntheticWorkout)), limits)
	assertErrorCode(t, err, ErrorCollectionLimit)
	limits = DefaultLimits()
	limits.MaxRoutePoints = 1
	_, err = ParseWithLimits(strings.NewReader(document(syntheticWorkout)), limits)
	assertErrorCode(t, err, ErrorCollectionLimit)
}

func TestTypeKeyNormalizationAndCollisionResistance(t *testing.T) {
	composed := NormalizedTypeKey("  CAFÉ  ")
	decomposed := NormalizedTypeKey("cafe\u0301")
	if composed != decomposed {
		t.Fatalf("canonically equivalent labels differ: %q != %q", composed, decomposed)
	}
	if NormalizedTypeKey("Run!") == NormalizedTypeKey("Run?") {
		t.Fatal("labels with colliding slugs share a key")
	}
	if NormalizedTypeKey("Outdoor Run") != "outdoor-run-27e9c638ed73cbf21bee19a1d4db970f" {
		t.Fatalf("type-key scheme changed: %s", NormalizedTypeKey("Outdoor Run"))
	}
}

func TestTypeKeyCapsLongUnicodeLabels(t *testing.T) {
	labels := []string{strings.Repeat("a", 4096), strings.Repeat("界", 1365) + "a"}
	keys := make([]string, len(labels))
	for index, label := range labels {
		if len(label) != 4096 {
			t.Fatalf("test label bytes = %d", len(label))
		}
		keys[index] = parseOne(t, strings.Replace(syntheticWorkout, "Outdoor Run", label, 1)).TypeKey
		if len(keys[index]) > MaxTypeKeyBytes || !utf8.ValidString(keys[index]) {
			t.Fatalf("unsafe type key length/encoding: %d/%q", len(keys[index]), keys[index])
		}
	}
	if keys[0] == keys[1] {
		t.Fatal("truncated labels lost collision-resistant identity")
	}
	if len(keys[0]) != MaxTypeKeyBytes {
		t.Fatalf("ASCII type key length = %d", len(keys[0]))
	}
}

func TestExactDecimalCanonicalizationAndValue(t *testing.T) {
	tests := map[string]string{
		"1.2300": "1.23", "123e-2": "1.23", "0.0012300e3": "1.23",
		"-0.000": "0", "1e3": "1000", "1e-3": "0.001",
	}
	for input, want := range tests {
		decimal, err := ParseDecimal(input)
		if err != nil || decimal.String() != want {
			t.Fatalf("ParseDecimal(%q) = %q/%v", input, decimal.String(), err)
		}
		value, err := decimal.Value()
		if err != nil || value != want {
			t.Fatalf("Value(%q) = %#v/%v", input, value, err)
		}
	}
	for _, invalid := range []string{"", "+1", "01", "1e999999", "NaN", "Infinity"} {
		if _, err := ParseDecimal(invalid); !errors.Is(err, ErrInvalidDecimal) {
			t.Fatalf("ParseDecimal(%q) error = %v", invalid, err)
		}
	}
}

func TestEquivalentDecimalFormsHaveStableContentHash(t *testing.T) {
	first := `{"name":"Walk","start":"2026-01-02 03:04:05 -0700","end":"2026-01-02 03:05:05 -0700","duration":60.00,"distance":{"qty":1.2300,"units":"km"}}`
	second := `{"distance":{"units":"km","qty":123e-2},"duration":6e1,"end":"2026-01-02 03:05:05 -0700","start":"2026-01-02 03:04:05 -0700","name":"Walk"}`
	a, b := parseOne(t, first), parseOne(t, second)
	metric := findMetric(a, MetricDistance)
	if ContentHashVersion != "health-auto-export-content-v3" || metric == nil || a.ProviderDuration.String() != "60" || metric.Qty.String() != "1.23" || a.ContentSHA256 != b.ContentSHA256 {
		t.Fatalf("equivalent decimals differ: %s/%#v/%x/%x", a.ProviderDuration, metric, a.ContentSHA256, b.ContentSHA256)
	}
}

func TestDecimalMagnitudeIsExact(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxDecimalMagnitude = mustDecimal("9007199254740992")
	accepted := strings.Replace(syntheticWorkout, "2323.5488079786301", "9007199254740992", 1)
	if _, err := ParseWithLimits(strings.NewReader(document(accepted)), limits); err != nil {
		t.Fatalf("exact boundary rejected: %v", err)
	}
	rejected := strings.Replace(syntheticWorkout, "2323.5488079786301", "9007199254740993", 1)
	_, err := ParseWithLimits(strings.NewReader(document(rejected)), limits)
	assertErrorCode(t, err, ErrorInvalidNumber)
	rejectedMetric := strings.Replace(syntheticWorkout, `"qty":120`, `"qty":9007199254740993`, 1)
	_, err = ParseWithLimits(strings.NewReader(document(rejectedMetric)), limits)
	assertErrorCode(t, err, ErrorInvalidNumber)
}

func TestStrictMalformedInputs(t *testing.T) {
	tests := map[string]struct {
		input string
		code  ErrorCode
	}{
		"invalid JSON":        {`{"data":`, ErrorInvalidJSON},
		"root array":          {`[]`, ErrorInvalidRoot},
		"missing data":        {`{}`, ErrorInvalidRoot},
		"data null":           {`{"data":null}`, ErrorInvalidData},
		"missing workouts":    {`{"data":{}}`, ErrorInvalidData},
		"workouts object":     {`{"data":{"workouts":{}}}`, ErrorInvalidData},
		"workout null":        {`{"data":{"workouts":[null]}}`, ErrorInvalidWorkout},
		"missing name":        {document(strings.Replace(syntheticWorkout, `"name":"Outdoor Run",`, "", 1)), ErrorInvalidWorkout},
		"bad start":           {document(strings.Replace(syntheticWorkout, "2026-04-11 19:24:01 -0700", "2026-04-11T19:24:01-07:00", 1)), ErrorInvalidTimestamp},
		"end before start":    {document(strings.Replace(syntheticWorkout, "2026-04-11 20:02:44 -0700", "2026-04-11 18:02:44 -0700", 1)), ErrorInvalidWorkout},
		"zero duration":       {document(strings.Replace(syntheticWorkout, "2323.5488079786301", "0", 1)), ErrorInvalidWorkout},
		"overflow duration":   {document(strings.Replace(syntheticWorkout, "2323.5488079786301", "1e999", 1)), ErrorInvalidNumber},
		"bad metric number":   {document(strings.Replace(syntheticWorkout, `"qty":120`, `"qty":"private"`, 1)), ErrorInvalidNumber},
		"route missing time":  {document(strings.Replace(syntheticWorkout, `"timestamp":"2026-04-11 19:24:02 -0700",`, "", 1)), ErrorInvalidRoute},
		"route bad latitude":  {document(strings.Replace(syntheticWorkout, `"latitude":37.1`, `"latitude":91`, 1)), ErrorInvalidRoute},
		"route bad longitude": {document(strings.Replace(syntheticWorkout, `"longitude":-122.1`, `"longitude":"west"`, 1)), ErrorInvalidNumber},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(strings.NewReader(test.input))
			assertErrorCode(t, err, test.code)
		})
	}
}

func TestRemainingLimits(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Limits)
		code   ErrorCode
	}{
		{"input", func(l *Limits) { l.MaxInputBytes = 20 }, ErrorInputLimit},
		{"metrics", func(l *Limits) { l.MaxAggregates = 1 }, ErrorCollectionLimit},
		{"units", func(l *Limits) { l.MaxUnitBytes = 1 }, ErrorStringLimit},
		{"numeric", func(l *Limits) { l.MaxNumericMagnitude = 100 }, ErrorInvalidNumber},
		{"nesting", func(l *Limits) { l.MaxNestingDepth = 1 }, ErrorNestingLimit},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			limits := DefaultLimits()
			test.mutate(&limits)
			_, err := ParseWithLimits(strings.NewReader(document(syntheticWorkout)), limits)
			assertErrorCode(t, err, test.code)
		})
	}
}

func TestMaxInputBytesRejectsInt64Overflow(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxInputBytes = math.MaxInt64
	_, err := ParseWithLimits(strings.NewReader(document(syntheticWorkout)), limits)
	assertErrorCode(t, err, ErrorInvalidLimits)
}

func TestErrorsContainOnlySanitizedCodes(t *testing.T) {
	input := strings.Replace(syntheticWorkout, `"duration":2323.5488079786301`, `"duration":"PRIVATE-VALUE"`, 1)
	_, err := Parse(strings.NewReader(document(input)))
	assertErrorCode(t, err, ErrorInvalidNumber)
	if strings.Contains(err.Error(), "duration") || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatalf("error leaked provider content: %q", err)
	}
}

func TestReaderErrorsAreSanitized(t *testing.T) {
	_, err := Parse(errorReader{})
	assertErrorCode(t, err, ErrorReadFailure)
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, io.ErrClosedPipe }

func parseOne(t *testing.T, workout string) Workout {
	t.Helper()
	doc, err := Parse(strings.NewReader(document(workout)))
	if err != nil {
		t.Fatal(err)
	}
	return doc.Workouts[0]
}

func findMetric(workout Workout, key MetricKey) *Aggregate {
	for i := range workout.Aggregates {
		if workout.Aggregates[i].Key == key {
			return &workout.Aggregates[i]
		}
	}
	return nil
}

func assertMetric(t *testing.T, workout Workout, key MetricKey, qty string, units string, origin AggregateOrigin) {
	t.Helper()
	metric := findMetric(workout, key)
	if metric == nil || metric.Qty.String() != qty || metric.Units != units || metric.Origin != origin {
		t.Fatalf("metric %s = %#v", key, metric)
	}
}

func assertWarning(t *testing.T, warnings []Warning, code WarningCode, field WarningField) {
	t.Helper()
	for _, warning := range warnings {
		if warning.Code == code && warning.Field == field {
			return
		}
	}
	t.Fatalf("warning %s/%s not found in %#v", code, field, warnings)
}

func assertErrorCode(t *testing.T, err error, code ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s", code)
	}
	var parsed *ParseError
	if !errors.As(err, &parsed) || parsed.Code != code || err.Error() != string(code) {
		t.Fatalf("error = %#v, want %s", err, code)
	}
}
