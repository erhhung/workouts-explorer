// Package healthautoexport parses Health Auto Export workout JSON into a
// bounded, provider-faithful model. It never retains samples, device details,
// metadata, or unknown provider extensions.
package healthautoexport

import "time"

const (
	OriginDirect    AggregateOrigin = "provider_direct"
	OriginHeartRate AggregateOrigin = "provider_heart_rate"
)

// FallbackFingerprintVersion identifies the stable fallback fingerprint wire
// format. Persist this version with the digest if multiple versions coexist.
const FallbackFingerprintVersion = "health-auto-export-fallback-v2"

// ContentHashVersion identifies canonical hashes using exact Decimal values.
const ContentHashVersion = "health-auto-export-content-v3"

// MaxTypeKeyBytes bounds normalized type keys including their digest suffix.
const MaxTypeKeyBytes = 512

// MetricKey is a provider-independent metric identity.
type MetricKey string

const (
	MetricActiveEnergyBurned MetricKey = "active_energy_burned"
	MetricAverageHeartRate   MetricKey = "heart_rate_average"
	MetricAverageSpeed       MetricKey = "speed_average"
	MetricDistance           MetricKey = "distance"
	MetricElevationUp        MetricKey = "elevation_up"
	MetricFlightsClimbed     MetricKey = "flights_climbed"
	MetricHeartRateMinimum   MetricKey = "heart_rate_minimum"
	MetricHumidity           MetricKey = "humidity"
	MetricIntensity          MetricKey = "intensity"
	MetricMaximumHeartRate   MetricKey = "heart_rate_maximum"
	MetricMaximumSpeed       MetricKey = "speed_maximum"
	MetricSpeed              MetricKey = "speed"
	MetricStepCadence        MetricKey = "step_cadence"
	MetricTemperature        MetricKey = "temperature"
	MetricTotalEnergy        MetricKey = "total_energy"
)

type AggregateOrigin string

type WarningCode string

const (
	WarningIncompleteMetric          WarningCode = "incomplete_metric"
	WarningUnexpectedUnit            WarningCode = "unexpected_unit"
	WarningInvalidOptionalRouteValue WarningCode = "invalid_optional_route_value"
)

// WarningField values are a closed set; they never contain provider text.
type WarningField string

const (
	WarningFieldActiveEnergyBurned WarningField = "active_energy_burned"
	WarningFieldAverageHeartRate   WarningField = "heart_rate_average"
	WarningFieldAverageSpeed       WarningField = "speed_average"
	WarningFieldDistance           WarningField = "distance"
	WarningFieldElevationUp        WarningField = "elevation_up"
	WarningFieldFlightsClimbed     WarningField = "flights_climbed"
	WarningFieldHeartRateMinimum   WarningField = "heart_rate_minimum"
	WarningFieldHumidity           WarningField = "humidity"
	WarningFieldIntensity          WarningField = "intensity"
	WarningFieldMaximumHeartRate   WarningField = "heart_rate_maximum"
	WarningFieldMaximumSpeed       WarningField = "speed_maximum"
	WarningFieldSpeed              WarningField = "speed"
	WarningFieldStepCadence        WarningField = "step_cadence"
	WarningFieldTemperature        WarningField = "temperature"
	WarningFieldTotalEnergy        WarningField = "total_energy"
	WarningFieldAltitude           WarningField = "route_altitude"
	WarningFieldCourse             WarningField = "route_course"
	WarningFieldCourseAccuracy     WarningField = "route_course_accuracy"
	WarningFieldHorizontalAccuracy WarningField = "route_horizontal_accuracy"
	WarningFieldRouteSpeed         WarningField = "route_speed"
	WarningFieldSpeedAccuracy      WarningField = "route_speed_accuracy"
	WarningFieldVerticalAccuracy   WarningField = "route_vertical_accuracy"
)

type ErrorCode string

const (
	ErrorInvalidJSON         ErrorCode = "invalid_json"
	ErrorReadFailure         ErrorCode = "read_failure"
	ErrorInputLimit          ErrorCode = "input_limit_exceeded"
	ErrorCollectionLimit     ErrorCode = "collection_limit_exceeded"
	ErrorStringLimit         ErrorCode = "string_limit_exceeded"
	ErrorNestingLimit        ErrorCode = "nesting_limit_exceeded"
	ErrorUnknownValueLimit   ErrorCode = "unknown_value_limit_exceeded"
	ErrorDuplicateKey        ErrorCode = "duplicate_object_key"
	ErrorDuplicateProviderID ErrorCode = "duplicate_provider_id"
	ErrorInvalidLimits       ErrorCode = "invalid_limits"
	ErrorInvalidRoot         ErrorCode = "invalid_root"
	ErrorInvalidData         ErrorCode = "invalid_data"
	ErrorInvalidWorkout      ErrorCode = "invalid_workout"
	ErrorInvalidTimestamp    ErrorCode = "invalid_timestamp"
	ErrorInvalidNumber       ErrorCode = "invalid_number"
	ErrorInvalidRoute        ErrorCode = "invalid_route"
)

// ParseError intentionally exposes only an allowlisted code and safe indexes.
type ParseError struct {
	Code       ErrorCode
	Workout    int
	RoutePoint int
}

func (e *ParseError) Error() string { return string(e.Code) }

// Limits bounds parser input, retained collections, and skipped extensions.
type Limits struct {
	MaxInputBytes             int64
	MaxWorkouts               int
	MaxRoutePoints            int
	MaxAggregates             int
	MaxStringBytes            int
	MaxUnitBytes              int
	MaxUnknownValueBytes      int64
	MaxUnknownCollectionItems int
	MaxNestingDepth           int
	MaxNumericMagnitude       float64
	MaxDecimalMagnitude       Decimal
}

func DefaultLimits() Limits {
	return Limits{
		MaxInputBytes:             64 << 20,
		MaxWorkouts:               10_000,
		MaxRoutePoints:            250_000,
		MaxAggregates:             256,
		MaxStringBytes:            4 << 10,
		MaxUnitBytes:              128,
		MaxUnknownValueBytes:      1 << 20,
		MaxUnknownCollectionItems: 100_000,
		MaxNestingDepth:           32,
		MaxNumericMagnitude:       1e15,
		MaxDecimalMagnitude:       mustDecimal("1000000000000000"),
	}
}

type Document struct{ Workouts []Workout }

type Workout struct {
	// ProviderID is preserved byte-for-byte and is empty when omitted. No trim
	// or UUID interpretation is safe under the current provider contract.
	ProviderID string
	// FallbackSHA256 is populated only without a provider ID. The database must
	// additionally scope it by source.
	FallbackSHA256   [32]byte
	ProviderLabel    string
	TypeKey          string
	Start            time.Time
	End              time.Time
	StartOffsetMins  int
	EndOffsetMins    int
	LocalStartDate   time.Time
	ProviderDuration Decimal
	IsIndoor         *bool
	Location         *string
	Aggregates       []Aggregate
	Route            []RoutePoint
	Warnings         []Warning
	ContentSHA256    [32]byte
}

type Aggregate struct {
	Key    MetricKey
	Qty    Decimal
	Units  string
	Origin AggregateOrigin
}

type RoutePoint struct {
	// Sequence is the zero-based provider array position. Duplicate timestamps
	// are retained and do not affect sequence assignment.
	Sequence            int
	Timestamp           time.Time
	TimestampOffsetMins int
	Latitude            float64
	Longitude           float64
	Altitude            *float64
	Speed               *float64
	Course              *float64
	HorizontalAccuracy  *float64
	VerticalAccuracy    *float64
	SpeedAccuracy       *float64
	CourseAccuracy      *float64
}

type Warning struct {
	Code       WarningCode
	Field      WarningField
	RoutePoint int
}
