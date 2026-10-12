package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/erhhung/workouts-explorer/internal/coverage/routepipeline"
	"github.com/erhhung/workouts-explorer/internal/healthautoexport"
)

// Independent test expectations, never used as a fallback for the shared JSON.
func acceptanceTestManifest() acceptanceManifest {
	return acceptanceManifest{
		SchemaVersion: 1, Year: 2026, Seed: 7104, GPSSampleSeconds: 5, DistanceMetersPerMile: 1609.344, OtherDurationSeconds: 1800,
		OtherTypes: []acceptanceOtherType{{"climbing", "Climbing"}, {"paddle_sports", "Paddle Sports"}, {"skating_sports", "Skating Sports"}},
		Profiles: map[string]acceptanceProfile{
			"typical": {1, 365, []acceptanceActivity{
				{"running", "Outdoor Run", 60, 219, true, 8, 7200}, {"walking", "Outdoor Walk", 20, 73, true, 8, 7200},
				{"cycling", "Outdoor Cycle", 10, 37, true, 20, 7200}, {"other", "Other", 10, 36, false, 0, 1800}}},
			"stress": {2, 730, []acceptanceActivity{
				{"running", "Outdoor Run", 40, 292, true, 13.1, 9000}, {"walking", "Outdoor Walk", 20, 146, true, 10, 10800},
				{"cycling", "Outdoor Cycle", 10, 73, true, 40, 14400}, {"other", "Other", 30, 219, false, 0, 1800}}},
		},
	}
}

func TestCoverageAcceptanceProfileValidation(t *testing.T) {
	if err := acceptanceValidateManifest(acceptanceTestManifest()); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*acceptanceManifest)
	}{
		{"version", func(m *acceptanceManifest) { m.SchemaVersion++ }},
		{"leap year", func(m *acceptanceManifest) { m.Year = 2028 }},
		{"year below range", func(m *acceptanceManifest) { m.Year = 1899 }},
		{"year above range", func(m *acceptanceManifest) { m.Year = 2101 }},
		{"seed below range", func(m *acceptanceManifest) { m.Seed = -2147483649 }},
		{"seed above range", func(m *acceptanceManifest) { m.Seed = 2147483648 }},
		{"cadence", func(m *acceptanceManifest) { m.GPSSampleSeconds = 0 }},
		{"cadence above range", func(m *acceptanceManifest) { m.GPSSampleSeconds = 31 }},
		{"mile conversion", func(m *acceptanceManifest) { m.DistanceMetersPerMile = 1600 }},
		{"empty catalog", func(m *acceptanceManifest) { m.OtherTypes = nil }},
		{"duplicate catalog", func(m *acceptanceManifest) { m.OtherTypes[1].Key = m.OtherTypes[0].Key }},
		{"reserved catalog", func(m *acceptanceManifest) { m.OtherTypes[0].Key = "running" }},
		{"other bucket catalog", func(m *acceptanceManifest) { m.OtherTypes[0].Key = "other" }},
		{"invalid catalog key", func(m *acceptanceManifest) { m.OtherTypes[0].Key = "has spaces" }},
		{"empty catalog label", func(m *acceptanceManifest) { m.OtherTypes[0].Label = " " }},
		{"null-byte catalog label", func(m *acceptanceManifest) { m.OtherTypes[0].Label = "Climbing\x00" }},
		{"duration zero", func(m *acceptanceManifest) { p := m.Profiles["typical"]; p.Activities[0].DurationSeconds = 0 }},
		{"duration overlaps", func(m *acceptanceManifest) { m.Profiles["stress"].Activities[0].DurationSeconds = 7*3600 + 1 }},
		{"other duration inconsistent", func(m *acceptanceManifest) { m.OtherDurationSeconds++ }},
		{"other duration zero", func(m *acceptanceManifest) { m.OtherDurationSeconds = 0 }},
		{"quota", func(m *acceptanceManifest) { p := m.Profiles["typical"]; p.Activities[0].Count-- }},
		{"negative quota", func(m *acceptanceManifest) { m.Profiles["typical"].Activities[0].Count = -1 }},
		{"repeated type quota", func(m *acceptanceManifest) { m.Profiles["stress"].Activities[0].Count = 366 }},
		{"percentage", func(m *acceptanceManifest) { p := m.Profiles["stress"]; p.Activities[0].Percentage++ }},
		{"percentage sum", func(m *acceptanceManifest) { m.Profiles["typical"].Activities[0].Percentage += .01 }},
		{"nonfinite percentage", func(m *acceptanceManifest) { m.Profiles["typical"].Activities[0].Percentage = math.NaN() }},
		{"negative percentage", func(m *acceptanceManifest) { m.Profiles["typical"].Activities[0].Percentage = -1 }},
		{"inconsistent rounding", func(m *acceptanceManifest) { a := m.Profiles["typical"].Activities; a[0].Count -= 2; a[1].Count += 2 }},
		{"no routed workouts", func(m *acceptanceManifest) {
			a := m.Profiles["typical"].Activities
			for i := range a {
				a[i].Count = 0
				a[i].Percentage = 0
			}
			a[3].Count = 365
			a[3].Percentage = 100
		}},
		{"routed other", func(m *acceptanceManifest) { p := m.Profiles["stress"]; p.Activities[3].Routed = true }},
		{"unrouted primary", func(m *acceptanceManifest) { m.Profiles["stress"].Activities[0].Routed = false }},
		{"other distance", func(m *acceptanceManifest) { p := m.Profiles["stress"]; p.Activities[3].DistanceMiles = 1 }},
		{"nonfinite distance", func(m *acceptanceManifest) { p := m.Profiles["stress"]; p.Activities[2].DistanceMiles = math.NaN() }},
		{"infinite distance", func(m *acceptanceManifest) { m.Profiles["typical"].Activities[0].DistanceMiles = math.Inf(1) }},
		{"overflowed meters", func(m *acceptanceManifest) { m.Profiles["typical"].Activities[0].DistanceMiles = math.MaxFloat64 }},
		{"zero primary distance", func(m *acceptanceManifest) { m.Profiles["typical"].Activities[0].DistanceMiles = 0 }},
		{"empty primary label", func(m *acceptanceManifest) { m.Profiles["typical"].Activities[0].Label = " " }},
		{"category order", func(m *acceptanceManifest) { a := m.Profiles["typical"].Activities; a[0], a[1] = a[1], a[0] }},
		{"calendar total", func(m *acceptanceManifest) { p := m.Profiles["stress"]; p.TotalWorkouts--; m.Profiles["stress"] = p }},
		{"profile identity", func(m *acceptanceManifest) {
			p := m.Profiles["typical"]
			p.WorkoutsPerDay = 2
			m.Profiles["typical"] = p
		}},
		{"unknown profile", func(m *acceptanceManifest) { m.Profiles["large"] = m.Profiles["stress"] }},
		{"missing profile", func(m *acceptanceManifest) { delete(m.Profiles, "stress"); m.Profiles["large"] = m.Profiles["typical"] }},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := acceptanceTestManifest()
			test.change(&m)
			if err := acceptanceValidateManifest(m); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
	if _, err := acceptanceSchedule(acceptanceTestManifest(), "unknown"); err == nil {
		t.Fatal("unknown schedule profile accepted")
	}
}

func TestCoverageAcceptanceProfileEditableBounds(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*acceptanceManifest)
	}{
		{"1900", func(m *acceptanceManifest) { m.Year = 1900 }},
		{"2100", func(m *acceptanceManifest) { m.Year = 2100 }},
		{"minimum seed", func(m *acceptanceManifest) { m.Seed = -2147483648 }},
		{"maximum seed", func(m *acceptanceManifest) { m.Seed = 2147483647 }},
		{"zero seed", func(m *acceptanceManifest) { m.Seed = 0 }},
		{"cadence one", func(m *acceptanceManifest) { m.GPSSampleSeconds = 1 }},
		{"cadence thirty", func(m *acceptanceManifest) { m.GPSSampleSeconds = 30 }},
		{"minimum duration", func(m *acceptanceManifest) { m.Profiles["typical"].Activities[0].DurationSeconds = 1 }},
		{"maximum duration", func(m *acceptanceManifest) { m.Profiles["stress"].Activities[0].DurationSeconds = 7 * 3600 }},
		{"other duration coupled", func(m *acceptanceManifest) {
			m.OtherDurationSeconds = 7 * 3600
			for _, p := range m.Profiles {
				p.Activities[3].DurationSeconds = m.OtherDurationSeconds
			}
		}},
		{"single renamed catalog", func(m *acceptanceManifest) { m.OtherTypes = []acceptanceOtherType{{"mobility-flow", "Mobility Flow"}} }},
		{"positive distance", func(m *acceptanceManifest) { m.Profiles["typical"].Activities[0].DistanceMiles = .1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			m := acceptanceTestManifest()
			test.change(&m)
			if err := acceptanceValidateManifest(m); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCoverageAcceptanceProfileManifestBytes(t *testing.T) {
	raw, err := json.Marshal(acceptanceTestManifest())
	if err != nil {
		t.Fatal(err)
	}
	input, err := acceptanceDecodeProfile(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(raw)
	if input.Name != "typical" || input.SHA256 != hex.EncodeToString(hash[:]) {
		t.Fatal("default profile/raw-byte SHA256 differs")
	}
	whitespace, err := acceptanceDecodeProfile(append(raw, '\n'), "stress")
	if err != nil {
		t.Fatal(err)
	}
	if input.SHA256 == whitespace.SHA256 || whitespace.Name != "stress" || !reflect.DeepEqual(input.Manifest, whitespace.Manifest) {
		t.Fatal("hash must bind raw JSON, not re-serialized contents")
	}
	if _, err := acceptanceDecodeProfile(raw, "unknown"); err == nil {
		t.Fatal("invalid environment profile accepted")
	}
	if _, err := acceptanceDecodeProfile([]byte(`{"schema_version":1}`), "typical"); err == nil {
		t.Fatal("incomplete manifest accepted")
	}
	if _, err := acceptanceDecodeProfile([]byte("not JSON"), "typical"); err == nil {
		t.Fatal("malformed manifest accepted")
	}
	for _, text := range []string{
		strings.Replace(string(raw), `"seed":7104`, `"seed":null`, 1),
		strings.Replace(string(raw), `"seed":7104`, `"unused_seed":7104`, 1),
		strings.Replace(string(raw), `"count":219`, `"count":null`, 1),
		strings.Replace(string(raw), `"count":219`, `"count":219.5`, 1),
		strings.Replace(string(raw), `"percentage":60`, `"percentage":null`, 1),
		strings.Replace(string(raw), `"routed":false`, `"routed":null`, 1),
	} {
		if _, err := acceptanceDecodeProfile([]byte(text), "typical"); err == nil {
			t.Fatal("missing/null/noninteger numeric field accepted")
		}
	}
}

func TestCoverageAcceptanceProfileSharedManifest(t *testing.T) {
	raw, err := os.ReadFile("../deploy/acceptance/profiles.json")
	if err != nil {
		t.Fatal(err)
	}
	expectedManifest := acceptanceTestManifest()
	hash := sha256.Sum256(raw)
	t.Logf("shared manifest sha256=%s", hex.EncodeToString(hash[:]))
	for _, name := range []string{"typical", "stress"} {
		input, err := acceptanceDecodeProfile(raw, name)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(input.Manifest, expectedManifest) {
			t.Fatalf("shared manifest differs from independent expectations for %s", name)
		}
		actual, err := acceptanceSchedule(input.Manifest, name)
		if err != nil {
			t.Fatal(err)
		}
		expected, err := acceptanceSchedule(expectedManifest, name)
		if err != nil || !reflect.DeepEqual(actual, expected) {
			t.Fatalf("shared manifest %s schedule differs from expected calendar", name)
		}
		acceptanceAssertCalendar(t, input.Manifest, name)
	}
}

func TestCoverageAcceptanceProfileSchedule(t *testing.T) {
	m := acceptanceTestManifest()
	for _, name := range []string{"typical", "stress"} {
		t.Run(name, func(t *testing.T) { acceptanceAssertCalendar(t, m, name) })
	}
}

func acceptanceAssertCalendar(t *testing.T, m acceptanceManifest, name string) {
	t.Helper()
	first, err := acceptanceSchedule(m, name)
	if err != nil {
		t.Fatal(err)
	}
	second, err := acceptanceSchedule(m, name)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("schedule is not deterministic")
	}
	profile := m.Profiles[name]
	if len(first) != profile.TotalWorkouts {
		t.Fatal("incorrect total")
	}
	catalog := map[string]bool{}
	for _, a := range m.OtherTypes {
		catalog[a.Key] = true
	}
	days, monthly, counts := map[string][]acceptanceScheduledWorkout{}, map[time.Month]int{}, map[string]int{}
	for index, w := range first {
		if w.Index != index || w.Start.Year() != m.Year || w.Start.Location() != time.UTC || w.Start.Minute() != 0 || w.Start.Second() != 0 ||
			(w.Start.Hour() != 8 && w.Start.Hour() != 15) {
			t.Fatal("incorrect schedule identity/time")
		}
		key := w.Activity.Key
		if !w.Activity.Routed {
			if !catalog[key] || w.Activity.DistanceMiles != 0 || w.Activity.DurationSeconds != m.OtherDurationSeconds {
				t.Fatal("unrouted catalog/distance/duration")
			}
			key = "other"
		}
		counts[key]++
		days[w.Start.Format("2006-01-02")] = append(days[w.Start.Format("2006-01-02")], w)
		monthly[w.Start.Month()]++
	}
	if len(days) != 365 || len(monthly) != 12 {
		t.Fatal("not a full calendar")
	}
	routed := 0
	for _, a := range profile.Activities {
		if counts[a.Key] != a.Count {
			t.Fatalf("%s quota=%d want %d", a.Key, counts[a.Key], a.Count)
		}
		if a.Routed {
			routed += a.Count
		}
	}
	doubleRouted := 0
	for date, ws := range days {
		if len(ws) != profile.WorkoutsPerDay || ws[0].Start.Hour() != 8 {
			t.Fatalf("day %s has wrong slots/count", date)
		}
		if len(ws) == 2 {
			if ws[1].Start.Hour() != 15 || ws[0].Activity.Key == ws[1].Activity.Key || ws[0].Start.Add(time.Duration(ws[0].Activity.DurationSeconds)*time.Second).After(ws[1].Start) {
				t.Fatalf("duplicate/overlap on %s", date)
			}
			if ws[0].Activity.Routed && ws[1].Activity.Routed {
				doubleRouted++
			}
		}
	}
	if doubleRouted != max(0, routed-365) {
		t.Fatalf("double-routed days=%d want minimum %d", doubleRouted, max(0, routed-365))
	}
	for month, count := range monthly {
		daysInMonth := time.Date(m.Year, month+1, 0, 0, 0, 0, 0, time.UTC).Day()
		if count != daysInMonth*profile.WorkoutsPerDay {
			t.Fatal("incorrect monthly calendar count")
		}
	}
}

func TestCoverageAcceptanceProfileCustomizedJSON(t *testing.T) {
	m := acceptanceTestManifest()
	m.Year, m.Seed, m.GPSSampleSeconds, m.OtherDurationSeconds = 2031, -99, 11, 2400
	m.OtherTypes = []acceptanceOtherType{{"mobility", "Mobility"}, {"dance", "Dance"}, {"stretching", "Stretching"}}
	for _, name := range []string{"typical", "stress"} {
		p := m.Profiles[name]
		counts, pct, miles, durations := []int{146, 109, 74, 36}, []float64{40, 29.8, 20.2, 10}, []float64{3.5, 1.25, 11.75, 0}, []int{3701, 7219, 3601, 2400}
		if name == "stress" {
			counts, pct, miles, durations = []int{365, 73, 73, 219}, []float64{50, 10, 10, 30}, []float64{7.8, 4.2, 26.25, 0}, []int{8903, 12345, 15001, 2400}
		}
		for i := range p.Activities {
			a := &p.Activities[i]
			a.Count, a.Percentage, a.DistanceMiles, a.DurationSeconds = counts[i], pct[i], miles[i], durations[i]
			a.Label = "Custom " + a.Label
		}
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	grid := acceptanceTestGrid()
	for _, name := range []string{"typical", "stress"} {
		input, err := acceptanceDecodeProfile(raw, name)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(input.Manifest, m) {
			t.Fatal("edited JSON parameters were not retained")
		}
		catalog := input.typeCatalog(false)
		for i, key := range input.typeKeys(false) {
			if key != healthautoexport.NormalizedTypeKey(catalog[i].Label) || key == catalog[i].Key {
				t.Fatal("edited labels must determine canonical keys, not internal catalog/bucket keys")
			}
		}
		hash := sha256.Sum256(raw)
		if input.SHA256 != hex.EncodeToString(hash[:]) || input.metadata()["year"] != 2031 || input.metadata()["seed"] != int64(-99) || input.metadata()["gps_sample_seconds"] != 11 {
			t.Fatal("edited manifest metadata/hash mismatch")
		}
		start, end := input.dateRange(0)
		if start != "2031-01-01" || end != "2031-12-31" {
			t.Fatal("year scope does not follow edited manifest")
		}
		start, end = input.dateRange(time.September)
		if start != "2031-09-01" || end != "2031-09-30" {
			t.Fatal("month scope does not follow edited manifest")
		}
		acceptanceAssertCalendar(t, input.Manifest, name)
		schedule, err := acceptanceSchedule(input.Manifest, name)
		if err != nil {
			t.Fatal(err)
		}
		editedSeed := input.Manifest
		editedSeed.Seed++
		different, err := acceptanceSchedule(editedSeed, name)
		if err != nil || reflect.DeepEqual(schedule, different) {
			t.Fatal("edited seed did not reshuffle the calendar")
		}
		seen := map[string]bool{}
		for _, w := range schedule {
			if !w.Activity.Routed || seen[w.Activity.Key] {
				continue
			}
			seen[w.Activity.Key] = true
			route, err := acceptanceGenerateRoute(grid, input.Manifest, w, 0)
			if err != nil {
				t.Fatal(err)
			}
			again, err := acceptanceGenerateRoute(grid, input.Manifest, w, 0)
			if err != nil || !reflect.DeepEqual(route, again) {
				t.Fatal("custom route not deterministic")
			}
			seedRoute, err := acceptanceGenerateRoute(grid, editedSeed, w, 0)
			if err != nil || canonicalRouteInputSHA256(seedRoute.Points) == canonicalRouteInputSHA256(route.Points) {
				t.Fatal("edited seed did not change route geometry/digest")
			}
			target := w.Activity.DistanceMiles * 1609.344
			if route.Meters != target || len(route.Points) < (w.Activity.DurationSeconds+10)/11+1 ||
				!route.Points[0].Timestamp.Equal(w.Start) || !route.Points[len(route.Points)-1].Timestamp.Equal(w.Start.Add(time.Duration(w.Activity.DurationSeconds)*time.Second)) {
				t.Fatal("custom distance/duration/cadence not generated")
			}
			for i, p := range route.Points {
				if p.Timestamp.Nanosecond()%1000 != 0 || p.Longitude < 0 || p.Longitude > .039 || p.Latitude < 0 || p.Latitude > .039 {
					t.Fatal("custom GPS precision/bounds invalid")
				}
				if i > 0 {
					delta := p.Timestamp.Sub(*route.Points[i-1].Timestamp)
					if delta <= 0 || delta > 11*time.Second {
						t.Fatal("custom GPS cadence/timestamps invalid")
					}
				}
			}
		}
		if len(seen) != 3 {
			t.Fatal("custom primary types not exercised")
		}
	}
}

func TestCoverageAcceptanceProfileStressMixtures(t *testing.T) {
	cases := [][4]int{{365, 365, 0, 0}, {365, 0, 0, 365}, {1, 1, 365, 363}, {0, 365, 0, 365}, {300, 200, 0, 230}, {100, 100, 311, 219}}
	for run := 0; run <= 365; run += 73 {
		for walk := 0; walk <= 365; walk += 73 {
			for bike := 0; bike <= 365; bike += 73 {
				other := 730 - run - walk - bike
				if other >= 0 && other <= 365 {
					cases = append(cases, [4]int{run, walk, bike, other})
				}
			}
		}
	}
	for _, counts := range cases {
		t.Run(fmt.Sprint(counts), func(t *testing.T) {
			m := acceptanceTestManifest()
			m.Seed = 0
			p := m.Profiles["stress"]
			for i, count := range counts {
				p.Activities[i].Count = count
				p.Activities[i].Percentage = float64(count) * 100 / 730
			}
			acceptanceAssertCalendar(t, m, "stress")
		})
	}
	m := acceptanceTestManifest()
	for i := range m.Profiles["typical"].Activities {
		a := &m.Profiles["typical"].Activities[i]
		a.Count, a.Percentage = 0, 0
	}
	m.Profiles["typical"].Activities[0].Count, m.Profiles["typical"].Activities[0].Percentage = 365, 100
	acceptanceAssertCalendar(t, m, "typical")
}

func TestCoverageAcceptanceProfileShortRoutesAndBounds(t *testing.T) {
	m, grid := acceptanceTestManifest(), acceptanceTestGrid()
	w := acceptanceScheduledWorkout{Index: 1, Start: time.Date(2031, 1, 1, 8, 0, 0, 0, time.UTC), Activity: m.Profiles["typical"].Activities[0]}
	w.Activity.DistanceMiles = .01
	w.Activity.DurationSeconds = 61
	m.GPSSampleSeconds = 30
	route, err := acceptanceGenerateRoute(grid, m, w, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(route.Points) != 4 || len(route.Evidence) != 1 || math.Abs(route.Meters-.01*1609.344) > 1e-10 {
		t.Fatal("short non-loop route not supported")
	}
	if route.Points[3].Timestamp.Sub(w.Start) != 61*time.Second {
		t.Fatal("nondivisible cadence lost the exact end time")
	}
	w.Activity.DistanceMiles = 1e100
	if _, err := acceptanceGenerateRoute(grid, m, w, 0); err == nil {
		t.Fatal("unbounded route walk accepted")
	}
	w.Activity.DistanceMiles = math.SmallestNonzeroFloat64
	if _, err := acceptanceGenerateRoute(grid, m, w, 0); err == nil {
		t.Fatal("unrepresentable positive distance accepted")
	}
	w.Activity.DistanceMiles = 8
	w.Activity.DurationSeconds = 1
	probe, err := acceptanceGenerateRoute(grid, m, w, 0)
	if err != nil {
		t.Fatal(err)
	}
	for i, leg := range probe.Traversals[:len(probe.Traversals)-1] {
		if leg.Key[0] != probe.Traversals[i+1].Key[0] {
			w.Activity.DistanceMiles = (leg.End + 1e-5) / m.DistanceMetersPerMile
			if _, err := acceptanceGenerateRoute(grid, m, w, 0); err == nil {
				t.Fatal("colliding turn/end microsecond timestamps accepted")
			}
			break
		}
	}
	w.Activity.DistanceMiles = 8
	m.GPSSampleSeconds = 0
	if _, err := acceptanceGenerateRoute(grid, m, w, 0); err == nil {
		t.Fatal("zero cadence accepted")
	}
}

func acceptanceTestGrid() acceptanceGrid {
	grid := acceptanceGrid{}
	for road := range 80 {
		for edge := range 39 {
			meters := 110.574
			if road < 40 {
				meters = 111.319490793 * math.Cos(float64(road)*.001*math.Pi/180)
			}
			grid[[2]int{road, edge}] = acceptanceGridEdge{Road: road, Edge: edge, Meters: meters}
		}
	}
	return grid
}

func TestCoverageAcceptanceProfileRoutes(t *testing.T) {
	m, grid := acceptanceTestManifest(), acceptanceTestGrid()
	for _, name := range []string{"typical", "stress"} {
		t.Run(name, func(t *testing.T) {
			schedule, err := acceptanceSchedule(m, name)
			if err != nil {
				t.Fatal(err)
			}
			totalPoints, reversePartials := 0, 0
			for _, w := range schedule {
				if !w.Activity.Routed {
					continue
				}
				route, err := acceptanceGenerateRoute(grid, m, w, 0)
				if err != nil {
					t.Fatal(err)
				}
				target := w.Activity.DistanceMiles * m.DistanceMetersPerMile
				if math.Abs(route.Meters-target) > 1e-8 || len(route.Points) < w.Activity.DurationSeconds/m.GPSSampleSeconds+1 {
					t.Fatal("wrong route distance/cadence")
				}
				if !route.Points[0].Timestamp.Equal(w.Start) || !route.Points[len(route.Points)-1].Timestamp.Equal(w.Start.Add(time.Duration(w.Activity.DurationSeconds)*time.Second)) {
					t.Fatal("wrong route start/end time")
				}
				last := route.Traversals[len(route.Traversals)-1]
				if last.To != 0 && last.To != 1 && last.From == 1 {
					reversePartials++
				}
				unique, covered := map[[2]int]bool{}, 0.0
				for _, e := range route.Evidence {
					if unique[e.Key] || e.Low < 0 || e.High > 1 || e.Low >= e.High {
						t.Fatal("invalid dissolved fractions")
					}
					unique[e.Key] = true
					covered += (e.High - e.Low) * grid[e.Key].Meters
					first := route.Traversals[e.FirstOrder]
					if first.Key != e.Key || e.FirstTime.Sub(w.Start).Microseconds() != int64(math.Round(first.Begin/target*float64(w.Activity.DurationSeconds)*1e6)) {
						t.Fatal("incorrect first traversal provenance")
					}
				}
				if covered >= target-1 {
					t.Fatal("loop traveled distance incorrectly used as unique coverage")
				}
				traveled := 0.0
				for i, p := range route.Points {
					if p.Sequence != i || p.Timestamp.Nanosecond()%1000 != 0 || p.Longitude < 0 || p.Longitude > .039 || p.Latitude < 0 || p.Latitude > .039 {
						t.Fatal("invalid point bounds/precision/sequence")
					}
					if i == 0 {
						continue
					}
					previous := route.Points[i-1]
					seconds := p.Timestamp.Sub(*previous.Timestamp).Seconds()
					if seconds <= 0 || seconds > float64(m.GPSSampleSeconds)+.000001 {
						t.Fatal("nonmonotonic timestamps/cadence")
					}
					dx, dy := math.Abs(p.Longitude-previous.Longitude), math.Abs(p.Latitude-previous.Latitude)
					if dx > 1e-12 && dy > 1e-12 {
						t.Fatal("corner cut/teleport between graph edges")
					}
					meters := dy / .001 * 110.574
					if dx > 1e-12 {
						road := int(math.Round(p.Latitude / .001))
						meters = dx / .001 * grid[[2]int{road, 0}].Meters
					}
					if math.Abs(meters/seconds-target/float64(w.Activity.DurationSeconds)) > .1 {
						t.Fatalf("interval speed differs: %f want %f", meters/seconds, target/float64(w.Activity.DurationSeconds))
					}
					traveled += meters
				}
				if math.Abs(traveled-target) > 1e-5 {
					t.Fatalf("independent sampled distance=%f target=%f", traveled, target)
				}
				totalPoints += len(route.Points)
			}
			if reversePartials == 0 {
				t.Fatal("reverse partial-edge coverage not exercised")
			}
			t.Logf("%s full calendar generated %d points; %d reverse partial endings", name, totalPoints, reversePartials)
		})
	}
}

func TestCoverageAcceptanceProfileRouteDeterminism(t *testing.T) {
	m, grid := acceptanceTestManifest(), acceptanceTestGrid()
	w := acceptanceScheduledWorkout{Index: 17, Start: time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC), Activity: m.Profiles["stress"].Activities[2]}
	first, err := acceptanceGenerateRoute(grid, m, w, 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := acceptanceGenerateRoute(grid, m, w, 0)
	if err != nil || !reflect.DeepEqual(first, second) {
		t.Fatal("route generation nondeterministic")
	}
	variant, err := acceptanceGenerateRoute(grid, m, w, 1)
	if err != nil {
		t.Fatal(err)
	}
	if canonicalRouteInputSHA256(first.Points) == canonicalRouteInputSHA256(variant.Points) || variant.Meters != first.Meters {
		t.Fatal("variant must change geometry/digest without changing distance")
	}
	if _, err := acceptanceGenerateRoute(acceptanceGrid{}, m, w, 0); err == nil {
		t.Fatal("missing authoritative graph accepted")
	}
	w.Activity = m.Profiles["stress"].Activities[3]
	if _, err := acceptanceGenerateRoute(grid, m, w, 0); err == nil {
		t.Fatal("route generated for unrouted workout")
	}
}

func TestCoverageAcceptanceProfileMovementModesAndMetadata(t *testing.T) {
	m := acceptanceTestManifest()
	for _, test := range []struct {
		label string
		slug  string
		mode  coverage.MovementMode
	}{
		{"Outdoor Run", "outdoor-run", coverage.MovementFoot},
		{"Outdoor Walk", "outdoor-walk", coverage.MovementFoot},
		{"Outdoor Cycle", "outdoor-cycle", coverage.MovementBicycle},
		{"Climbing", "climbing", coverage.MovementFoot},
		{"Paddle Sports", "paddle-sports", coverage.MovementSharedPublic},
		{"Skating Sports", "skating-sports", coverage.MovementSharedPublic},
		{"Indoor Run", "indoor-run", coverage.MovementFoot},
		{"Indoor Walk", "indoor-walk", coverage.MovementFoot},
		{"Indoor Cycle", "indoor-cycle", coverage.MovementBicycle},
	} {
		key := healthautoexport.NormalizedTypeKey(test.label)
		digest := sha256.Sum256([]byte(strings.ToLower(test.label)))
		wantKey := test.slug + "-" + hex.EncodeToString(digest[:16])
		if key != wantKey || key != healthautoexport.NormalizedTypeKey("  "+strings.ToUpper(test.label)+"  ") {
			t.Fatalf("incorrect stable canonical type key for %s: %s, want %s", test.label, key, wantKey)
		}
		if routepipeline.MovementMode(key, test.label) != test.mode {
			t.Fatalf("incorrect production movement mode for %s", test.label)
		}
		t.Logf("activity=%s type_key=%s movement_mode=%s", test.label, key, test.mode)
	}
	for _, name := range []string{"typical", "stress"} {
		input := acceptanceProfileInput{Manifest: m, Name: name}
		schedule, err := acceptanceSchedule(m, name)
		if err != nil {
			t.Fatal(err)
		}
		seen := map[string]bool{}
		for _, w := range schedule {
			if strings.HasPrefix(w.Activity.Label, "Indoor ") {
				t.Fatal("indoor counterparts must not enter annual acceptance profiles")
			}
			seen[healthautoexport.NormalizedTypeKey(w.Activity.Label)] = true
		}
		if len(seen) != 6 || len(input.typeKeys(false)) != 6 || len(input.typeKeys(true)) != 3 {
			t.Fatal("default schedule must exercise exactly six actual types and three routed types")
		}
		for _, key := range input.typeKeys(false) {
			if !seen[key] {
				t.Fatalf("canonical catalog key %s absent from %s schedule", key, name)
			}
		}
	}
	p := acceptanceProfileInput{Manifest: m, Name: "stress", SHA256: "raw-json-sha256"}
	raw, err := json.Marshal(p.metadata())
	if err != nil {
		t.Fatal(err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["profile"] != "stress" || metadata["profile_version"] != float64(1) || metadata["profile_manifest_sha256"] != p.SHA256 || metadata["seed"] != float64(7104) ||
		p.routedCount() != 511 || p.artifact("queue96") != "/tmp/opencode/coverage-stress-queue96.json" {
		t.Fatal("incorrect Node-facing profile metadata")
	}
}
