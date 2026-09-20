package worker

import (
	"testing"
	"time"
)

func TestCanonicalRouteInputDigestPreservesOrderAndNulls(t *testing.T) {
	timestamp := time.Date(2026, 8, 30, 12, 0, 0, 123000000, time.FixedZone("offset", -6*60*60))
	altitude := 1600.0
	base := []coverageRoutePoint{
		{Sequence: 0, Timestamp: &timestamp, Latitude: 40, Longitude: -105, Altitude: &altitude},
		{Sequence: 1, Timestamp: nil, Latitude: 40.1, Longitude: -104.9},
	}
	digest := canonicalRouteInputSHA256(base)
	if digest != canonicalRouteInputSHA256(append([]coverageRoutePoint(nil), base...)) {
		t.Fatal("same route input produced a different digest")
	}
	reordered := []coverageRoutePoint{base[1], base[0]}
	if digest == canonicalRouteInputSHA256(reordered) {
		t.Fatal("point order did not affect digest")
	}
	nonNull := append([]coverageRoutePoint(nil), base...)
	zero := 0.0
	nonNull[1].Speed = &zero
	if digest == canonicalRouteInputSHA256(nonNull) {
		t.Fatal("null and zero speed produced the same digest")
	}
	changedAccuracy := append([]coverageRoutePoint(nil), base...)
	accuracy := 4.0
	changedAccuracy[0].HorizontalAccuracy = &accuracy
	if digest == canonicalRouteInputSHA256(changedAccuracy) {
		t.Fatal("accuracy did not affect digest")
	}
	changedTimestamp := append([]coverageRoutePoint(nil), base...)
	later := timestamp.Add(time.Nanosecond)
	changedTimestamp[0].Timestamp = &later
	if digest == canonicalRouteInputSHA256(changedTimestamp) {
		t.Fatal("timestamp did not affect digest")
	}
}

func TestNullableReadinessReason(t *testing.T) {
	if nullableReadinessReason("") != nil {
		t.Fatal("empty readiness reason must be persisted as SQL NULL")
	}
	if got := nullableReadinessReason("region_not_active"); got != "region_not_active" {
		t.Fatalf("readiness reason = %#v", got)
	}
}
