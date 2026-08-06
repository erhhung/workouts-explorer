package healthautoexport

import (
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestSampleFixtures(t *testing.T) {
	fixtureDir := filepath.Join("..", "..", "data", "samples")
	paths := []string{
		filepath.Join(fixtureDir, "HealthAutoExport-2026-04-11.json"),
		filepath.Join(fixtureDir, "HealthAutoExport-2026-07-15.json"),
		filepath.Join(fixtureDir, "HealthAutoExport-2026-07-20.json"),
	}
	for _, path := range paths {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			t.Skip("workspace sample fixtures are not present")
		} else if err != nil {
			t.Fatal(err)
		}
	}

	wantIDs := []string{
		"0ACE8792-3C73-4554-83A4-724434A75279", "3191EA2A-D556-4986-B169-46FF66CB42E1",
		"495C8ABF-1C7C-4C8F-A8DF-9AC2E3E96C1F", "AF30D204-3343-4E43-901A-7A793CD29D64",
		"E98FD4CD-0C14-4951-9128-7BAB9F872819",
	}
	// Hashes pin the v2 normalized model and intentionally exclude ignored samples/extensions.
	wantHashes := map[string]string{
		"0ACE8792-3C73-4554-83A4-724434A75279": "6fab705b041c827aa62024da74c16be202ed1e91656a69b31b9387f8c70a9c22",
		"3191EA2A-D556-4986-B169-46FF66CB42E1": "8e52a4215568472f78684ed2bbb32cee3dc793a77ab392ec0a4e5632763dbffc",
		"495C8ABF-1C7C-4C8F-A8DF-9AC2E3E96C1F": "7296762caa8369ea60cf368cc0207b30c039abc1f89746bf1d1874d1b0d667df",
		"AF30D204-3343-4E43-901A-7A793CD29D64": "f2988db85dd632d829c2bc6131523ce9ca7950e87a2acf3ccafc0b95a66d7e4f",
		"E98FD4CD-0C14-4951-9128-7BAB9F872819": "886917558c2be15ffc003bf1d79e1245e20798402d2b78f7e2fe824a5877fdc4",
	}

	var workouts []Workout
	for _, path := range paths {
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		doc, parseErr := Parse(file)
		closeErr := file.Close()
		if parseErr != nil {
			t.Fatalf("%s: %v", filepath.Base(path), parseErr)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		workouts = append(workouts, doc.Workouts...)
	}

	ids := make([]string, 0, len(workouts))
	typeCounts := map[string]int{}
	typeKeyCounts := map[string]int{}
	routeCount, duplicateTimestampGroups, invalidQualityValues := 0, 0, 0
	for _, workout := range workouts {
		ids = append(ids, workout.ProviderID)
		typeCounts[workout.ProviderLabel]++
		typeKeyCounts[workout.TypeKey]++
		routeCount += len(workout.Route)
		if workout.StartOffsetMins != -420 || workout.EndOffsetMins != -420 || workout.FallbackSHA256 != ([32]byte{}) {
			t.Errorf("provider identity/time changed for %s", workout.ProviderID)
		}
		if got := hex.EncodeToString(workout.ContentSHA256[:]); got != wantHashes[workout.ProviderID] {
			t.Errorf("hash %s = %s", workout.ProviderID, got)
		}
		seenTimestamps := map[int64]int{}
		for _, point := range workout.Route {
			if point.TimestampOffsetMins != -420 {
				t.Errorf("route offset not preserved at %s/%d", workout.ProviderID, point.Sequence)
			}
			for _, quality := range []*float64{point.Speed, point.Course, point.HorizontalAccuracy, point.VerticalAccuracy, point.SpeedAccuracy, point.CourseAccuracy} {
				if quality == nil {
					invalidQualityValues++
				}
			}
			seenTimestamps[point.Timestamp.UnixNano()]++
		}
		for _, count := range seenTimestamps {
			if count > 1 {
				duplicateTimestampGroups++
			}
		}
	}
	slices.Sort(ids)
	if !slices.Equal(ids, wantIDs) || len(workouts) != 5 {
		t.Fatalf("fixture IDs = %v", ids)
	}
	if typeCounts["Climbing"] != 3 || typeCounts["Indoor Run"] != 1 || typeCounts["Outdoor Walk"] != 1 {
		t.Fatalf("type counts = %v", typeCounts)
	}
	if typeKeyCounts["climbing-df7da510a6fe1ab64a57e49687b9b1d2"] != 3 ||
		typeKeyCounts["indoor-run-77655f0656a7061fea2a8decffef3247"] != 1 ||
		typeKeyCounts["outdoor-walk-d382cfff87408643a86b7a842d85134a"] != 1 {
		t.Fatalf("normalized type-key counts = %v", typeKeyCounts)
	}
	if routeCount != 788 || duplicateTimestampGroups != 6 || invalidQualityValues != 81 {
		t.Fatalf("route count/duplicate groups/invalid quality = %d/%d/%d", routeCount, duplicateTimestampGroups, invalidQualityValues)
	}

	walk := workoutByID(t, workouts, "0ACE8792-3C73-4554-83A4-724434A75279")
	assertMetric(t, walk, MetricDistance, "2.9405940629292266", "km", OriginDirect)
	assertMetric(t, walk, MetricActiveEnergyBurned, "122.48453388293568", "kcal", OriginDirect)
	assertMetric(t, walk, MetricFlightsClimbed, "11.999999999999998", "count", OriginDirect)
	assertMetric(t, walk, MetricAverageHeartRate, "118.27670240235592", "count/min", OriginDirect)
	assertMetric(t, walk, MetricHeartRateMinimum, "103", "count/min", OriginHeartRate)
	assertWarning(t, walk.Warnings, WarningUnexpectedUnit, WarningFieldAverageSpeed)
	assertMetric(t, walk, MetricMaximumSpeed, "11.909202430745099", "km", OriginDirect)
	assertWarning(t, walk.Warnings, WarningUnexpectedUnit, WarningFieldMaximumSpeed)
	qualityWarnings := 0
	for _, warning := range walk.Warnings {
		if warning.Code == WarningInvalidOptionalRouteValue {
			qualityWarnings++
		}
	}
	if qualityWarnings != 81 {
		t.Fatalf("quality warning count = %d", qualityWarnings)
	}
	firstPoint := walk.Route[0]
	if firstPoint.Altitude == nil || *firstPoint.Altitude != 10.941685147583485 || firstPoint.Speed == nil || *firstPoint.Speed != 1.7709006248559591 ||
		firstPoint.Course == nil || *firstPoint.Course != 254.53749236413327 || firstPoint.HorizontalAccuracy == nil || *firstPoint.HorizontalAccuracy != 1.5263682025436989 ||
		firstPoint.VerticalAccuracy == nil || *firstPoint.VerticalAccuracy != 1.4386278278132325 || firstPoint.SpeedAccuracy == nil || *firstPoint.SpeedAccuracy != 0.52336820532729789 ||
		firstPoint.CourseAccuracy == nil || *firstPoint.CourseAccuracy != 21.472970045792245 {
		t.Fatalf("first route quality values changed: %#v", firstPoint)
	}
	if walk.IsIndoor == nil || *walk.IsIndoor || walk.Location == nil || *walk.Location != "Outdoor" || walk.ProviderDuration.String() != "2195.6228786706924" {
		t.Fatalf("walk nullability/provider values changed")
	}
	indoor := workoutByID(t, workouts, "AF30D204-3343-4E43-901A-7A793CD29D64")
	if indoor.IsIndoor == nil || !*indoor.IsIndoor || indoor.Location == nil || *indoor.Location != "Indoor" {
		t.Fatal("indoor nullability changed")
	}
	climbing := workoutByID(t, workouts, "3191EA2A-D556-4986-B169-46FF66CB42E1")
	if climbing.IsIndoor != nil || climbing.Location != nil {
		t.Fatal("absent nullable values were invented")
	}
}

func workoutByID(t *testing.T, workouts []Workout, id string) Workout {
	t.Helper()
	for _, workout := range workouts {
		if workout.ProviderID == id {
			return workout
		}
	}
	t.Fatalf("workout not found")
	return Workout{}
}
