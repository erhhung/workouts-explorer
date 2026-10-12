package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/healthautoexport"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const acceptanceProfileVersion = 1

var acceptanceOtherKey = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,511}$`)

type acceptanceActivity struct {
	Key             string  `json:"key"`
	Label           string  `json:"label"`
	Percentage      float64 `json:"percentage"`
	Count           int     `json:"count"`
	Routed          bool    `json:"routed"`
	DistanceMiles   float64 `json:"distance_miles"`
	DurationSeconds int     `json:"duration_seconds"`
}

type acceptanceProfile struct {
	WorkoutsPerDay int                  `json:"workouts_per_day"`
	TotalWorkouts  int                  `json:"total_workouts"`
	Activities     []acceptanceActivity `json:"activities"`
}

type acceptanceOtherType struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

type acceptanceManifest struct {
	SchemaVersion         int                          `json:"schema_version"`
	Year                  int                          `json:"year"`
	Seed                  int64                        `json:"seed"`
	GPSSampleSeconds      int                          `json:"gps_sample_seconds"`
	DistanceMetersPerMile float64                      `json:"distance_meters_per_mile"`
	OtherDurationSeconds  int                          `json:"other_duration_seconds"`
	OtherTypes            []acceptanceOtherType        `json:"other_types"`
	Profiles              map[string]acceptanceProfile `json:"profiles"`
}

type acceptanceProfileInput struct {
	Manifest acceptanceManifest
	Name     string
	SHA256   string
}

func acceptanceLoadProfile(t testing.TB) acceptanceProfileInput {
	t.Helper()
	raw, err := os.ReadFile("../deploy/acceptance/profiles.json")
	if err != nil {
		t.Fatalf("read shared acceptance manifest: %v", err)
	}
	input, err := acceptanceDecodeProfile(raw, os.Getenv("COVERAGE_ACCEPTANCE_PROFILE"))
	if err != nil {
		t.Fatal(err)
	}
	if version := os.Getenv("COVERAGE_ACCEPTANCE_PROFILE_VERSION"); version != "" && version != fmt.Sprint(input.Manifest.SchemaVersion) {
		t.Fatal("acceptance profile version differs from shared manifest")
	}
	if digest := os.Getenv("COVERAGE_ACCEPTANCE_PROFILE_MANIFEST_SHA256"); digest != "" && digest != input.SHA256 {
		t.Fatal("acceptance profile hash differs from shared manifest")
	}
	return input
}

func acceptanceDecodeProfile(raw []byte, name string) (acceptanceProfileInput, error) {
	var m acceptanceManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return acceptanceProfileInput{}, err
	}
	// JSON null/missing values must not silently become valid zero quotas/seeds.
	var required struct {
		Seed     *int64 `json:"seed"`
		Profiles map[string]struct {
			Activities []struct {
				Percentage *float64 `json:"percentage"`
				Count      *int     `json:"count"`
				Routed     *bool    `json:"routed"`
			} `json:"activities"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(raw, &required); err != nil {
		return acceptanceProfileInput{}, err
	}
	if required.Seed == nil {
		return acceptanceProfileInput{}, fmt.Errorf("manifest requires an integer seed")
	}
	for _, p := range required.Profiles {
		for _, a := range p.Activities {
			if a.Percentage == nil || a.Count == nil || a.Routed == nil {
				return acceptanceProfileInput{}, fmt.Errorf("activity requires percentage, integer count, and routed flag")
			}
		}
	}
	if err := acceptanceValidateManifest(m); err != nil {
		return acceptanceProfileInput{}, err
	}
	if name == "" {
		name = "typical"
	}
	if name != "typical" && name != "stress" {
		return acceptanceProfileInput{}, fmt.Errorf("COVERAGE_ACCEPTANCE_PROFILE must be typical or stress, got %q", name)
	}
	hash := sha256.Sum256(raw)
	return acceptanceProfileInput{Manifest: m, Name: name, SHA256: hex.EncodeToString(hash[:])}, nil
}

func (p acceptanceProfileInput) metadata() map[string]any {
	return map[string]any{"schema_version": 3, "namespace": "workouts-acceptance", "namespace_uid": os.Getenv("COVERAGE_ACCEPTANCE_NAMESPACE_UID"),
		"profile": p.Name, "profile_version": p.Manifest.SchemaVersion, "manifest_sha256": p.SHA256, "profile_manifest_sha256": p.SHA256,
		"seed": p.Manifest.Seed, "year": p.Manifest.Year, "gps_sample_seconds": p.Manifest.GPSSampleSeconds}
}

func (p acceptanceProfileInput) artifact(suffix string) string {
	return "/tmp/opencode/coverage-" + p.Name + "-" + suffix + ".json"
}

// A zero month selects the entire manifest year; other months select that month.
func (p acceptanceProfileInput) dateRange(month time.Month) (string, string) {
	start := time.Date(p.Manifest.Year, month, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, -1)
	if month == 0 {
		start = time.Date(p.Manifest.Year, 1, 1, 0, 0, 0, 0, time.UTC)
		end = start.AddDate(1, 0, -1)
	}
	return start.Format("2006-01-02"), end.Format("2006-01-02")
}

func (p acceptanceProfileInput) routedCount() int {
	n := 0
	for _, a := range p.Manifest.Profiles[p.Name].Activities {
		if a.Routed {
			n += a.Count
		}
	}
	return n
}

func (p acceptanceProfileInput) typeCatalog(routedOnly bool) []acceptanceOtherType {
	var catalog []acceptanceOtherType
	for _, a := range p.Manifest.Profiles[p.Name].Activities[:3] {
		catalog = append(catalog, acceptanceOtherType{a.Key, a.Label})
	}
	if !routedOnly {
		catalog = append(catalog, p.Manifest.OtherTypes...)
	}
	return catalog
}

// Quota/catalog keys are internal scheduling identities, not database type keys.
func (p acceptanceProfileInput) typeKeys(routedOnly bool) []string {
	var keys []string
	for _, a := range p.typeCatalog(routedOnly) {
		keys = append(keys, healthautoexport.NormalizedTypeKey(a.Label))
	}
	return keys
}

func acceptanceValidateManifest(m acceptanceManifest) error {
	if m.SchemaVersion != acceptanceProfileVersion || m.Year < 1900 || m.Year > 2100 || time.Date(m.Year, 12, 31, 0, 0, 0, 0, time.UTC).YearDay() != 365 ||
		m.Seed < -2147483648 || m.Seed > 2147483647 || m.GPSSampleSeconds < 1 || m.GPSSampleSeconds > 30 ||
		m.DistanceMetersPerMile != 1609.344 || m.OtherDurationSeconds < 1 || m.OtherDurationSeconds > 7*3600 {
		return fmt.Errorf("invalid acceptance manifest version/year/seed/cadence/mile definition/other duration")
	}
	if len(m.OtherTypes) == 0 || len(m.Profiles) != 2 {
		return fmt.Errorf("acceptance manifest requires an other-type catalog and exactly typical/stress profiles")
	}
	keys := map[string]bool{"running": true, "walking": true, "cycling": true, "other": true}
	for _, a := range m.OtherTypes {
		if !acceptanceOtherKey.MatchString(a.Key) || keys[a.Key] || strings.TrimSpace(a.Label) == "" || strings.ContainsRune(a.Label, 0) {
			return fmt.Errorf("invalid/duplicate/reserved other type %q", a.Key)
		}
		keys[a.Key] = true
	}
	for n, name := range []string{"typical", "stress"} {
		p, ok := m.Profiles[name]
		perDay := n + 1
		if !ok || p.WorkoutsPerDay != perDay || p.TotalWorkouts != 365*perDay || len(p.Activities) != 4 {
			return fmt.Errorf("invalid %s calendar/quota", name)
		}
		total, routed, percentage := 0, 0, 0.0
		for i, key := range []string{"running", "walking", "cycling", "other"} {
			a := p.Activities[i]
			if a.Key != key || strings.TrimSpace(a.Label) == "" || strings.ContainsRune(a.Label, 0) || a.Count < 0 || a.Count > 365 ||
				math.IsNaN(a.Percentage) || math.IsInf(a.Percentage, 0) || a.Percentage < 0 || a.Percentage > 100 ||
				math.Abs(float64(a.Count)-float64(p.TotalWorkouts)*a.Percentage/100) > 1+1e-9 || a.Routed != (i < 3) ||
				math.IsNaN(a.DistanceMiles) || math.IsInf(a.DistanceMiles*m.DistanceMetersPerMile, 0) ||
				(a.Routed && a.DistanceMiles <= 0) || (!a.Routed && (a.DistanceMiles != 0 || a.DurationSeconds != m.OtherDurationSeconds)) ||
				a.DurationSeconds < 1 || a.DurationSeconds > 7*3600 {
				return fmt.Errorf("invalid %s activity %s quota/distance/duration", name, key)
			}
			total += a.Count
			percentage += a.Percentage
			if a.Routed {
				routed += a.Count
			}
		}
		if total != p.TotalWorkouts || math.Abs(percentage-100) > 1e-9 || routed == 0 {
			return fmt.Errorf("invalid %s total/percentage/routed-workout quota", name)
		}
	}
	return nil
}

type acceptanceScheduledWorkout struct {
	Index    int
	Start    time.Time
	Activity acceptanceActivity
}

func acceptanceSchedule(m acceptanceManifest, name string) ([]acceptanceScheduledWorkout, error) {
	if err := acceptanceValidateManifest(m); err != nil {
		return nil, err
	}
	p, ok := m.Profiles[name]
	if !ok {
		return nil, fmt.Errorf("unknown acceptance profile %q", name)
	}
	rng := rand.New(rand.NewSource(m.Seed))
	var days [][]acceptanceActivity
	if p.WorkoutsPerDay == 1 {
		for _, a := range p.Activities {
			for range a.Count {
				days = append(days, []acceptanceActivity{a})
			}
		}
	} else {
		remaining := []int{p.Activities[0].Count, p.Activities[1].Count, p.Activities[2].Count}
		// Put every unrouted workout on its own day with a routed partner. The
		// remaining days must have two distinct routed activities.
		for range 365 - p.Activities[3].Count {
			order := []int{0, 1, 2}
			sort.SliceStable(order, func(i, j int) bool { return remaining[order[i]] > remaining[order[j]] })
			if remaining[order[1]] == 0 {
				return nil, fmt.Errorf("cannot pair distinct routed activities")
			}
			days = append(days, []acceptanceActivity{p.Activities[order[0]], p.Activities[order[1]]})
			remaining[order[0]]--
			remaining[order[1]]--
		}
		for i, count := range remaining {
			for range count {
				days = append(days, []acceptanceActivity{p.Activities[i], p.Activities[3]})
			}
		}
	}
	rng.Shuffle(len(days), func(i, j int) { days[i], days[j] = days[j], days[i] })
	var result []acceptanceScheduledWorkout
	for d, activities := range days {
		rng.Shuffle(len(activities), func(i, j int) { activities[i], activities[j] = activities[j], activities[i] })
		for slot, a := range activities {
			if !a.Routed {
				other := m.OtherTypes[rng.Intn(len(m.OtherTypes))]
				a.Key, a.Label = other.Key, other.Label
			}
			start := time.Date(m.Year, 1, 1, 8+7*slot, 0, 0, 0, time.UTC).AddDate(0, 0, d)
			result = append(result, acceptanceScheduledWorkout{len(result), start, a})
		}
	}
	return result, nil
}

type acceptanceGridEdge struct {
	Road, Edge int
	Meters     float64
	Payload    map[string]any
}

type acceptanceGrid map[[2]int]acceptanceGridEdge

func acceptanceLoadGrid(f *coverageIntegrationFixture) acceptanceGrid {
	rows, err := f.osmAdminDB.Query(f.ctx, `SELECT source_way_id-1,start_node_index,length_m,jsonb_build_object(
		'physicalSegmentId',segment_id,'regionId',region_id,'generation',generation_id,'derivationVersion',derivation_version,
		'logicalPathId',logical_path_id,'localityRelationId',1,'localityRelationVersion',1,'localityName','Synthetic Acceptance City',
		'sourceWayId',source_way_id,'sourceWayVersion',1,'segmentName',name,'segmentNormalizedName',normalized_name,'pathName',name,'pathNormalizedName',normalized_name,
		'highway',highway,'broadClass',broad_class,'tags',tags,'segmentMeters',length_m,'segmentGeometry',ST_AsGeoJSON(geom,15)::jsonb)
		FROM osm_canonical.path_segments WHERE region_id='fixture:dense' AND generation_id=1 ORDER BY source_way_id,start_node_index`)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	grid := acceptanceGrid{}
	for rows.Next() {
		var edge acceptanceGridEdge
		var raw []byte
		if err := rows.Scan(&edge.Road, &edge.Edge, &edge.Meters, &raw); err != nil {
			f.t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &edge.Payload); err != nil {
			f.t.Fatal(err)
		}
		if edge.Road < 0 || edge.Road >= 80 || edge.Edge < 0 || edge.Edge >= 39 || edge.Meters <= 0 || math.IsNaN(edge.Meters) {
			f.t.Fatal("invalid canonical grid edge")
		}
		var geometry struct {
			Type        string       `json:"type"`
			Coordinates [][2]float64 `json:"coordinates"`
		}
		var payload struct {
			Geometry json.RawMessage `json:"segmentGeometry"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			f.t.Fatal(err)
		}
		if err := json.Unmarshal(payload.Geometry, &geometry); err != nil {
			f.t.Fatal(err)
		}
		key := [2]int{edge.Road, edge.Edge}
		x1, y1 := acceptanceEdgeXY(key, 0)
		x2, y2 := acceptanceEdgeXY(key, 1)
		if geometry.Type != "LineString" || len(geometry.Coordinates) != 2 || math.Abs(geometry.Coordinates[0][0]-x1) > 1e-12 || math.Abs(geometry.Coordinates[0][1]-y1) > 1e-12 ||
			math.Abs(geometry.Coordinates[1][0]-x2) > 1e-12 || math.Abs(geometry.Coordinates[1][1]-y2) > 1e-12 {
			f.t.Fatal("canonical geometry differs from acceptance grid")
		}
		grid[[2]int{edge.Road, edge.Edge}] = edge
	}
	if err := rows.Err(); err != nil {
		f.t.Fatal(err)
	}
	if len(grid) != 3120 {
		f.t.Fatalf("canonical grid edges=%d, want 3120", len(grid))
	}
	return grid
}

type acceptanceTraversal struct {
	Key                  [2]int
	From, To, Begin, End float64
}

type acceptanceRouteEvidence struct {
	Key        [2]int
	Low, High  float64
	FirstTime  time.Time
	FirstOrder int
}

type acceptanceGeneratedRoute struct {
	Points     []coverageRoutePoint
	Traversals []acceptanceTraversal
	Evidence   []acceptanceRouteEvidence
	Meters     float64
}

func acceptanceEdgeXY(key [2]int, fraction float64) (float64, float64) {
	if key[0] < 40 {
		return (float64(key[1]) + fraction) * .001, float64(key[0]) * .001
	}
	return float64(key[0]-40) * .001, (float64(key[1]) + fraction) * .001
}

func acceptanceGenerateRoute(grid acceptanceGrid, m acceptanceManifest, w acceptanceScheduledWorkout, variant int64) (acceptanceGeneratedRoute, error) {
	var result acceptanceGeneratedRoute
	if !w.Activity.Routed || w.Activity.DistanceMiles <= 0 || math.IsNaN(w.Activity.DistanceMiles) || w.Activity.DurationSeconds < 1 || w.Activity.DurationSeconds > 7*3600 ||
		m.GPSSampleSeconds < 1 || m.GPSSampleSeconds > 30 || m.DistanceMetersPerMile != 1609.344 {
		return result, fmt.Errorf("route requires routed activity with positive distance and duration")
	}
	target := w.Activity.DistanceMiles * m.DistanceMetersPerMile
	maxEdge := 0.0
	for _, edge := range grid {
		maxEdge = max(maxEdge, edge.Meters)
	}
	// A straight leg cannot exceed the grid's 39 edges. Reject enormous routes
	// before walking them; retain the worker's actual point cap without truncation.
	if math.IsInf(target, 0) || target > maxEdge*39*float64(coverageOriginalPointLimit-2) {
		return result, fmt.Errorf("route exceeds the %d-point generation bound", coverageOriginalPointLimit)
	}
	rng := rand.New(rand.NewSource(m.Seed + int64(w.Index)*1009 + variant*1000003))
	x, y := rng.Intn(30), rng.Intn(30)
	turns := 0
	move := func(nx, ny int) error {
		for (x != nx || y != ny) && result.Meters < target {
			x2, y2 := x, y
			if x != nx {
				if nx > x {
					x2++
				} else {
					x2--
				}
			} else if ny > y {
				y2++
			} else {
				y2--
			}
			key, from, to := [2]int{y, min(x, x2)}, 0.0, 1.0
			if x == x2 {
				key = [2]int{40 + x, min(y, y2)}
			}
			if x2 < x || y2 < y {
				from, to = 1, 0
			}
			edge, ok := grid[key]
			if !ok || edge.Meters <= 0 || math.IsNaN(edge.Meters) || math.IsInf(edge.Meters, 0) {
				return fmt.Errorf("missing/invalid canonical edge %v", key)
			}
			length := min(edge.Meters, target-result.Meters)
			to = from + (to-from)*length/edge.Meters
			fromLon, fromLat := acceptanceEdgeXY(key, from)
			toLon, toLat := acceptanceEdgeXY(key, to)
			if fromLon == toLon && fromLat == toLat {
				return fmt.Errorf("route distance is below grid coordinate precision")
			}
			if len(result.Traversals) > 0 {
				previous := result.Traversals[len(result.Traversals)-1]
				if previous.Key[0] != key[0] || (previous.To > previous.From) != (to > from) {
					turns++
				}
			}
			if turns+2 > coverageOriginalPointLimit {
				return fmt.Errorf("route exceeds the %d-point generation bound", coverageOriginalPointLimit)
			}
			result.Traversals = append(result.Traversals, acceptanceTraversal{key, from, to, result.Meters, result.Meters + length})
			result.Meters += length
			x, y = x2, y2
		}
		return nil
	}
	for result.Meters < target {
		left, bottom := rng.Intn(30), rng.Intn(30)
		if len(result.Traversals) == 0 {
			left, bottom = x, y
		}
		right, top := left+3+rng.Intn(8), bottom+3+rng.Intn(8)
		if err := move(left, bottom); err != nil {
			return result, err
		}
		// Repeated rectangular loops deliberately distinguish traveled distance
		// from dissolved physical coverage; connectors diversify the next loop.
		for loops := 2 + rng.Intn(3); loops > 0 && result.Meters < target; loops-- {
			for _, node := range [][2]int{{right, bottom}, {right, top}, {left, top}, {left, bottom}} {
				if err := move(node[0], node[1]); err != nil {
					return result, err
				}
			}
		}
	}
	durationUS := int64(w.Activity.DurationSeconds) * 1000000
	timeAt := func(meters float64) time.Time {
		return w.Start.Add(time.Duration(math.Round(meters/target*float64(durationUS))) * time.Microsecond)
	}
	evidence := map[[2]int]int{}
	for order, leg := range result.Traversals {
		low, high := min(leg.From, leg.To), max(leg.From, leg.To)
		if i, ok := evidence[leg.Key]; ok {
			// Every non-final visit spans a full edge, so this interval union
			// cannot bridge a gap between two disjoint partial visits.
			result.Evidence[i].Low = min(result.Evidence[i].Low, low)
			result.Evidence[i].High = max(result.Evidence[i].High, high)
		} else {
			evidence[leg.Key] = len(result.Evidence)
			result.Evidence = append(result.Evidence, acceptanceRouteEvidence{leg.Key, low, high, timeAt(leg.Begin), order})
		}
	}
	// Include every turn/reversal as well as the regular GPS cadence. Collinear
	// graph vertices need no extra GPS sample. Round once to SQL microseconds.
	times := []int64{0, durationUS}
	vertices := map[int64]float64{0: 0, durationUS: target}
	for us := int64(m.GPSSampleSeconds) * 1000000; us < durationUS; us += int64(m.GPSSampleSeconds) * 1000000 {
		times = append(times, us)
	}
	for i, leg := range result.Traversals {
		if i+1 < len(result.Traversals) {
			next := result.Traversals[i+1]
			if leg.Key[0] == next.Key[0] && (leg.To > leg.From) == (next.To > next.From) {
				continue
			}
		}
		us := timeAt(leg.End).Sub(w.Start).Microseconds()
		if previous, ok := vertices[us]; ok && previous != leg.End {
			return acceptanceGeneratedRoute{}, fmt.Errorf("route turns cannot fit distinct microsecond timestamps")
		}
		times = append(times, us)
		vertices[us] = leg.End
	}
	sort.Slice(times, func(i, j int) bool { return times[i] < times[j] })
	legIndex := 0
	last := int64(-1)
	for _, us := range times {
		if us == last {
			continue
		}
		last = us
		distance := target * float64(us) / float64(durationUS)
		if vertex, ok := vertices[us]; ok {
			distance = vertex
		}
		for legIndex+1 < len(result.Traversals) && distance > result.Traversals[legIndex].End {
			legIndex++
		}
		leg := result.Traversals[legIndex]
		fraction := leg.From + (leg.To-leg.From)*max(0, min(1, (distance-leg.Begin)/(leg.End-leg.Begin)))
		lon, lat := acceptanceEdgeXY(leg.Key, fraction)
		ts, accuracy := w.Start.Add(time.Duration(us)*time.Microsecond), 3.0
		result.Points = append(result.Points, coverageRoutePoint{Sequence: len(result.Points), Timestamp: &ts, Longitude: lon, Latitude: lat, HorizontalAccuracy: &accuracy})
		if len(result.Points) > coverageOriginalPointLimit {
			return acceptanceGeneratedRoute{}, fmt.Errorf("route exceeds the %d-point generation bound", coverageOriginalPointLimit)
		}
	}
	return result, nil
}

func acceptanceMatches(f *coverageIntegrationFixture, grid acceptanceGrid, route acceptanceGeneratedRoute) []byte {
	roads, edges := make([]int32, len(route.Evidence)), make([]int32, len(route.Evidence))
	lows, highs := make([]float64, len(route.Evidence)), make([]float64, len(route.Evidence))
	for i, e := range route.Evidence {
		roads[i], edges[i], lows[i], highs[i] = int32(e.Key[0]), int32(e.Key[1]), e.Low, e.High
	}
	// Clip even reverse partial visits in canonical edge direction in one query,
	// using the authoritative SQL geometry rather than an invented payload.
	rows, err := f.osmAdminDB.Query(f.ctx, `SELECT p.ordinality,ST_AsGeoJSON(ST_Multi(ST_LineSubstring(s.geom,p.low,p.high)),15)::jsonb
		FROM unnest($1::int[],$2::int[],$3::float8[],$4::float8[]) WITH ORDINALITY p(road,edge,low,high,ordinality)
		JOIN osm_canonical.path_segments s ON s.source_way_id=p.road+1 AND s.start_node_index=p.edge
		WHERE s.region_id='fixture:dense' AND s.generation_id=1 ORDER BY p.ordinality`, roads, edges, lows, highs)
	if err != nil {
		f.t.Fatal(err)
	}
	defer rows.Close()
	matches := make([]map[string]any, 0, len(route.Evidence))
	for rows.Next() {
		var ordinal int
		var geometry json.RawMessage
		if err := rows.Scan(&ordinal, &geometry); err != nil {
			f.t.Fatal(err)
		}
		e := route.Evidence[ordinal-1]
		item := map[string]any{}
		for k, v := range grid[e.Key].Payload {
			item[k] = v
		}
		item["coveredGeometry"], item["firstTraversedAt"], item["firstRouteOrder"] = geometry, e.FirstTime, e.FirstOrder
		matches = append(matches, item)
	}
	if err := rows.Err(); err != nil {
		f.t.Fatal(err)
	}
	if len(matches) != len(route.Evidence) {
		f.t.Fatal("incomplete canonical coverage evidence")
	}
	raw, err := json.Marshal(matches)
	if err != nil {
		f.t.Fatal(err)
	}
	return raw
}

func acceptanceWriteRoute(f *coverageIntegrationFixture, tx pgx.Tx, id uuid.UUID, points []coverageRoutePoint) {
	sequences, timestamps := make([]int32, len(points)), make([]time.Time, len(points))
	latitudes, longitudes := make([]float64, len(points)), make([]float64, len(points))
	minLon, maxLon, minLat, maxLat := points[0].Longitude, points[0].Longitude, points[0].Latitude, points[0].Latitude
	for i, p := range points {
		sequences[i], timestamps[i], latitudes[i], longitudes[i] = int32(p.Sequence), *p.Timestamp, p.Latitude, p.Longitude
		minLon, maxLon, minLat, maxLat = min(minLon, p.Longitude), max(maxLon, p.Longitude), min(minLat, p.Latitude), max(maxLat, p.Latitude)
	}
	f.exec(tx, `DELETE FROM app.workout_route_points WHERE account_id=$1 AND workout_id=$2`, f.accountID, id)
	f.exec(tx, `INSERT INTO app.workout_route_points(account_id,workout_id,sequence,recorded_at,latitude,longitude,horizontal_accuracy)
		SELECT $1,$2,p.sequence,p.recorded_at,p.latitude,p.longitude,3 FROM unnest($3::int[],$4::timestamptz[],$5::float8[],$6::float8[]) p(sequence,recorded_at,latitude,longitude)`, f.accountID, id, sequences, timestamps, latitudes, longitudes)
	var replaced bool
	f.scan(tx, `SELECT app.replace_workout_route_summary($1,$2,$3,$4,$5,$6,NULL,NULL,NULL,false)`, []any{id, len(points), minLon, minLat, maxLon, maxLat}, &replaced)
	if !replaced {
		f.t.Fatal("route summary fenced out")
	}
	digest := canonicalRouteInputSHA256(points)
	var revision int64
	var changed bool
	f.scan(tx, `SELECT route_input_revision,digest_changed FROM app.initialize_workout_coverage($1,$2,$3,$4,'coverage-fixture-ingest',$5)`, []any{f.accountID, id, digest[:], f.ingestID, f.ingestLease}, &revision, &changed)
	f.scan(tx, `SELECT app.set_workout_coverage_readiness($1,$2,$3,'map_data_ready',NULL,$4,$5,'coverage-fixture-ingest',$6)`, []any{f.accountID, id, revision, `[{"regionId":"fixture:dense","generation":1}]`, f.ingestID, f.ingestLease}, &changed)
}

func acceptanceEligibleWorkout(f *coverageIntegrationFixture, profile acceptanceProfileInput, account uuid.UUID, start, end string) uuid.UUID {
	tx := f.accountTx(f.ctx, f.adminDB)
	defer coverageIntegrationRollback(tx)
	var id uuid.UUID
	f.scan(tx, `SELECT w.id FROM app.workouts w JOIN app.workout_routes r ON r.account_id=w.account_id AND r.workout_id=w.id
		JOIN app.workout_types t ON t.account_id=w.account_id AND t.id=w.workout_type_id
		JOIN app.workout_coverage_states s ON s.account_id=w.account_id AND s.workout_id=w.id
		WHERE w.account_id=$1 AND w.local_start_date BETWEEN $2::date AND $3::date AND w.deletion_requested_at IS NULL
		AND t.type_key=ANY($4::text[]) AND r.route IS NOT NULL AND s.readiness_state='map_data_ready' AND s.applied_route_input_revision IS NOT NULL
		ORDER BY w.started_at,w.id LIMIT 1`, []any{account, start, end, profile.typeKeys(true)}, &id)
	return id
}
