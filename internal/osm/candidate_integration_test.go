package osm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// Like the copy-query harness, this only accepts an explicitly disposable,
// loopback database. All fixture DDL/data is rolled back, including indexes.
func candidateTestDatabase(t testing.TB) (context.Context, pgx.Tx) {
	t.Helper()
	dsn := os.Getenv("OSM_MATCHER_TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("OSM_MATCHER_TEST_DISPOSABLE") != "1" {
		t.Skip("OSM_MATCHER_TEST_DATABASE_URL and OSM_MATCHER_TEST_DISPOSABLE=1 are required")
	}
	for _, key := range []string{"PGSERVICE", "PGSERVICEFILE", "PGSSLKEY", "PGSSLCERT", "PGSSLROOTCERT"} {
		t.Setenv(key, "")
	}
	t.Setenv("PGPASSFILE", "/dev/null")
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid disposable matcher database configuration")
	}
	loopback := func(host string) bool { return host == "127.0.0.1" || host == "localhost" || host == "::1" }
	if !strings.HasPrefix(config.Database, "workouts_matcher_copy_test_") || !loopback(config.Host) {
		t.Fatal("candidate integration requires a workouts_matcher_copy_test_* loopback database")
	}
	for _, fallback := range config.Fallbacks {
		if !loopback(fallback.Host) {
			t.Fatal("candidate integration requires loopback database fallbacks")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal("connect to disposable matcher database failed")
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(ctx, candidateFixtureSQL); err != nil {
		t.Fatalf("create synthetic PostGIS fixture (requires unused OSM schemas): %v", err)
	}
	var pgVersion, postgisVersion string
	if err := tx.QueryRow(ctx, "SELECT current_setting('server_version'),postgis_lib_version()").Scan(&pgVersion, &postgisVersion); err != nil {
		t.Fatal(err)
	}
	t.Logf("synthetic database: PostgreSQL %s, PostGIS %s", pgVersion, postgisVersion)
	return ctx, tx
}

func candidateTestArgs(observations []MatcherObservation, limit int, regions []string, generations []int64) []any {
	lon, lat, radii := make([]float64, len(observations)), make([]float64, len(observations)), make([]float64, len(observations))
	for i, observation := range observations {
		lon[i], lat[i], radii[i] = observation.Longitude, observation.Latitude, observation.RadiusMeters
	}
	return []any{lon, lat, radii, limit, regions, generations}
}

func candidateTestRows(t testing.TB, ctx context.Context, tx pgx.Tx, sql string, args []any) []MatcherCandidate {
	t.Helper()
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows.FieldDescriptions()) != 28 {
		t.Fatalf("query returned %d columns, want 28", len(rows.FieldDescriptions()))
	}
	result, err := pgx.CollectRows(rows, pgx.RowToStructByPos[MatcherCandidate])
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func candidateRowsExactlyEqual(a, b []MatcherCandidate) bool {
	if !reflect.DeepEqual(a, b) {
		return false
	}
	// DeepEqual treats +0 and -0 as equal; also check float bits, including the
	// nullable tangent, so cached geography cannot hide a projection change.
	for i := range a {
		av, bv := reflect.ValueOf(a[i]), reflect.ValueOf(b[i])
		for j := range av.NumField() {
			af, bf := av.Field(j), bv.Field(j)
			if af.Kind() == reflect.Pointer && !af.IsNil() && af.Elem().Kind() == reflect.Float64 {
				af, bf = af.Elem(), bf.Elem()
			}
			if af.Kind() == reflect.Float64 && math.Float64bits(af.Float()) != math.Float64bits(bf.Float()) {
				return false
			}
		}
	}
	return true
}

func candidateTestEquivalent(t testing.TB, ctx context.Context, tx pgx.Tx, observations []MatcherObservation, limit int, regions []string, generations []int64) {
	t.Helper()
	args := candidateTestArgs(observations, limit+1, regions, generations)
	original := candidateTestRows(t, ctx, tx, candidateOriginalSQL, args)
	batch := candidateTestRows(t, ctx, tx, matcherCandidatesBatchSQL, args)
	if !candidateRowsExactlyEqual(original, batch) {
		for i := 0; i < min(len(original), len(batch)); i++ {
			if !candidateRowsExactlyEqual(original[i:i+1], batch[i:i+1]) {
				t.Fatalf("all-field ordered comparison differs at row %d: original=%+v batch=%+v", i, original[i], batch[i])
			}
		}
		t.Fatalf("ordered row counts differ: original=%d batch=%d", len(original), len(batch))
	}
	if len(observations) == 0 {
		return // The public wrapper separately rejects empty input before querying.
	}
	counts := make([]int, len(observations))
	var firstOverflowInfo *MatcherOverflowError
	for _, candidate := range original {
		counts[candidate.ObservationIndex]++
		if counts[candidate.ObservationIndex] > limit {
			index := candidate.ObservationIndex
			firstOverflowInfo = &MatcherOverflowError{Operation: "candidates", Limit: limit, ObservationIndex: &index}
			break
		}
	}
	actual, err := (&MatcherSnapshot{tx: tx, allowedRegions: regions, allowedGenerations: generations}).Candidates(ctx, observations, limit)
	if firstOverflowInfo != nil {
		var overflow *MatcherOverflowError
		if !errors.As(err, &overflow) || !reflect.DeepEqual(overflow, firstOverflowInfo) || actual != nil {
			t.Fatalf("wrapper first overflow differs: got=%+v err=%v want=%+v", actual, err, firstOverflowInfo)
		}
	} else if err != nil || !candidateRowsExactlyEqual(actual, original) {
		t.Fatalf("wrapper differs from oracle: rows=%d want=%d err=%v", len(actual), len(original), err)
	}
}

func TestCandidateEquivalenceIntegration(t *testing.T) {
	ctx, tx := candidateTestDatabase(t)
	if _, err := tx.Exec(ctx, candidateSemanticFixtureSQL); err != nil {
		t.Fatal(err)
	}
	var boundary float64
	if err := tx.QueryRow(ctx, `SELECT ST_Distance(geom::geography,ST_SetSRID(ST_Point(0,0.0001),4326)::geography)
        FROM osm_canonical.path_segments WHERE region_id='fixture:a' AND segment_id=lpad('4',32,'0')::uuid`).Scan(&boundary); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		obs  []MatcherObservation
	}{
		{"zero_observations", nil},
		{"no_matches", []MatcherObservation{{Longitude: 40, Latitude: 40, RadiusMeters: 25}}},
		{"ties_and_preference", []MatcherObservation{{RadiusMeters: 25}, {RadiusMeters: 1}}},
		{"duplicates_variable_radii", []MatcherObservation{{RadiusMeters: 1}, {RadiusMeters: 250}, {RadiusMeters: 25}}},
		{"discontinuous", []MatcherObservation{{RadiusMeters: 25}, {Longitude: 10, Latitude: 10, RadiusMeters: 250}, {Longitude: -80, Latitude: -30, RadiusMeters: 1}}},
		{"dateline", []MatcherObservation{{Longitude: 179.99999, RadiusMeters: 250}, {Longitude: -179.99999, RadiusMeters: 25}, {Longitude: 180, RadiusMeters: 1}, {Longitude: -180, RadiusMeters: 1}}},
		{"poles", []MatcherObservation{{Latitude: 90, RadiusMeters: 25}, {Longitude: 180, Latitude: 89.99999, RadiusMeters: 250}, {Latitude: -90, RadiusMeters: 25}}},
		{"boundary", []MatcherObservation{{Latitude: 0.0001, RadiusMeters: boundary}, {Latitude: 0.0001, RadiusMeters: math.Nextafter(boundary, 0)}, {Latitude: 0.0001, RadiusMeters: math.Nextafter(boundary, math.Inf(1))}}},
		{"floating_projection", []MatcherObservation{{Longitude: 0.0000123456789, Latitude: 0.000034567891, RadiusMeters: 50}}},
	}
	scopes := []struct {
		name        string
		regions     []string
		generations []int64
	}{
		{"unrestricted_null", nil, nil},
		{"unrestricted_empty", []string{}, []int64{}},
		{"single", []string{"fixture:a"}, []int64{10}},
		{"zipped_not_cross_product", []string{"fixture:a", "fixture:b"}, []int64{20, 10}},
		{"all", []string{"fixture:a", "fixture:b", "fixture:c"}, []int64{10, 20, 20}},
		{"duplicate_pairs", []string{"fixture:a", "fixture:b", "fixture:a"}, []int64{10, 20, 10}},
	}
	for _, scope := range scopes {
		for _, test := range cases {
			t.Run(scope.name+"/"+test.name, func(t *testing.T) {
				for _, limit := range []int{1, 2, 256} {
					candidateTestEquivalent(t, ctx, tx, test.obs, limit, scope.regions, scope.generations)
				}
			})
		}
	}
	maximumBatch := make([]MatcherObservation, maxMatcherObservations)
	for i := range maximumBatch {
		maximumBatch[i] = MatcherObservation{Longitude: float64(i%5) * 0.00001, RadiusMeters: float64(i%250 + 1)}
	}
	candidateTestEquivalent(t, ctx, tx, maximumBatch, 256, nil, nil)
	// A preferred segment whose own way is obsolete still suppresses the older
	// segment. Both preference checks must look outside the spatial workset.
	all := candidateTestRows(t, ctx, tx, matcherCandidatesBatchSQL, candidateTestArgs([]MatcherObservation{{RadiusMeters: 25}}, 257, nil, nil))
	for _, candidate := range all {
		if candidate.SourceWayID == 1 || candidate.SourceWayID == 3 || candidate.SourceWayID == 6 || candidate.SourceWayID == 7 {
			t.Fatalf("global way/independent segment precedence lost: %+v", candidate)
		}
	}
	plan := candidateExplain(t, ctx, tx, matcherCandidatesBatchSQL, candidateTestArgs(maximumBatch, 257, nil, nil))
	for _, retired := range []string{"retired_segments", "retired_ways"} {
		if candidatePlanNode(plan.Plan, "Relation Name", retired) != nil {
			t.Fatal("candidate query scanned detached retired poison")
		}
	}
	var validAttachedIndexes int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM pg_index i
        JOIN pg_inherits attached ON attached.inhrelid=i.indexrelid
        JOIN pg_class parent ON parent.oid=attached.inhparent
        WHERE parent.relname='candidate_geography' AND i.indisvalid AND i.indisready`).Scan(&validAttachedIndexes); err != nil {
		t.Fatal(err)
	}
	if validAttachedIndexes != 3 {
		t.Fatalf("fixture must exercise valid, attached geography indexes on all active leaves: %d", validAttachedIndexes)
	}
}

func TestCandidateWorksetAndResultBoundsIntegration(t *testing.T) {
	ctx, tx := candidateTestDatabase(t)
	for _, count := range []int{256, 257, 8192, 8193} {
		t.Run(fmt.Sprintf("segments_%d", count), func(t *testing.T) {
			if _, err := tx.Exec(ctx, "TRUNCATE osm_canonical.path_segments,osm_canonical.ways"); err != nil {
				t.Fatal(err)
			}
			candidateInsertDense(t, ctx, tx, count, false)
			observations := []MatcherObservation{{Longitude: 30, RadiusMeters: 1}, {RadiusMeters: 25}, {RadiusMeters: 250}}
			candidateTestEquivalent(t, ctx, tx, observations, 256, nil, nil)
			plan := candidateExplain(t, ctx, tx, matcherCandidatesBatchSQL, candidateTestArgs(observations, 257, nil, nil))
			nearby := candidatePlanNode(plan.Plan, "Subplan Name", "CTE nearby")
			if nearby == nil || nearby.ActualRows != float64(count) {
				t.Fatalf("bounded spatial materialization rows: %+v, want %d", nearby, count)
			}
			eligible := candidatePlanNode(plan.Plan, "Subplan Name", "CTE eligible")
			wantEligible := count
			if count > 8192 {
				wantEligible = 0
			}
			if eligible == nil || eligible.ActualRows != float64(wantEligible) {
				t.Fatalf("optimized preference rows: %+v, want %d (overflow must bypass it)", eligible, wantEligible)
			}
		})
	}
	// Even a much larger search must materialize only the sentinel-sized prefix.
	candidateInsertDense(t, ctx, tx, 1000, false)
	observations := []MatcherObservation{{RadiusMeters: 25}}
	candidateTestEquivalent(t, ctx, tx, observations, 256, nil, nil)
	plan := candidateExplain(t, ctx, tx, matcherCandidatesBatchSQL, candidateTestArgs(observations, 257, nil, nil))
	if node := candidatePlanNode(plan.Plan, "Subplan Name", "CTE nearby"); node == nil || node.ActualRows != 8193 {
		t.Fatalf("workset exceeds sentinel: %+v", node)
	}
}

func candidateInsertDense(t testing.TB, ctx context.Context, tx pgx.Tx, count int, spread bool) {
	t.Helper()
	// Unique IDs start beyond existing rows so the overflow-extension is additive.
	if _, err := tx.Exec(ctx, `INSERT INTO osm_canonical.path_segments
    SELECT 'fixture:a',10,lpad((n+offset_id)::text,32,'0')::uuid,1,
           lpad('1',32,'0')::uuid,NULL,n+offset_id,1,
           lpad('2',32,'0')::uuid,lpad('3',32,'0')::uuid,
           'Synthetic dense','synthetic dense','footway','path','{"synthetic":true}'::jsonb,
           true,false,ST_SetSRID(ST_MakeLine(ST_Point(x-0.00001,y),ST_Point(x+0.00001,y)),4326),2.25
    FROM (SELECT n,CASE WHEN $2 THEN (n%32)*0.00004 ELSE 0 END AS x,
                     CASE WHEN $2 THEN (n/32)*0.00004 ELSE 0 END AS y,
                     (SELECT count(*) FROM osm_canonical.path_segments) AS offset_id
          FROM generate_series(1,$1::integer) n) input`, count, spread); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO osm_canonical.ways
        SELECT region_id,generation_id,source_way_id,source_way_version FROM osm_canonical.path_segments
        ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
}

type candidatePlan struct {
	Plan          candidatePlanTree `json:"Plan"`
	ExecutionTime float64           `json:"Execution Time"`
	JIT           struct {
		Timing struct {
			Total float64 `json:"Total"`
		} `json:"Timing"`
	} `json:"JIT"`
}

type candidatePlanTree struct {
	NodeType   string              `json:"Node Type"`
	Subplan    string              `json:"Subplan Name"`
	Relation   string              `json:"Relation Name"`
	Index      string              `json:"Index Name"`
	ActualRows float64             `json:"Actual Rows"`
	Loops      float64             `json:"Actual Loops"`
	Hits       float64             `json:"Shared Hit Blocks"`
	Plans      []candidatePlanTree `json:"Plans"`
}

func candidateExplain(t testing.TB, ctx context.Context, tx pgx.Tx, sql string, args []any) candidatePlan {
	t.Helper()
	var raw []byte
	if err := tx.QueryRow(ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) "+sql, args...).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var plans []candidatePlan
	if err := json.Unmarshal(raw, &plans); err != nil || len(plans) != 1 {
		t.Fatalf("decode candidate plan: %v", err)
	}
	return plans[0]
}

func candidatePlanNode(plan candidatePlanTree, field, value string) *candidatePlanTree {
	if (field == "Subplan Name" && plan.Subplan == value) || (field == "Relation Name" && plan.Relation == value) {
		return &plan
	}
	for _, child := range plan.Plans {
		if found := candidatePlanNode(child, field, value); found != nil {
			return found
		}
	}
	return nil
}

func TestCandidateDenseSQLCostIntegration(t *testing.T) {
	ctx, tx := candidateTestDatabase(t)
	candidateInsertDense(t, ctx, tx, 512, true)
	observations := make([]MatcherObservation, 128)
	for i := range observations {
		observations[i] = MatcherObservation{Longitude: float64(i%32) * 0.00004, Latitude: float64(i/32+6) * 0.00004, RadiusMeters: 25}
	}
	candidateTestEquivalent(t, ctx, tx, observations, 256, nil, nil)
	args := candidateTestArgs(observations, 257, nil, nil)
	// Warm both statements, then alternate order; do not modify statistics,
	// planner settings or JIT. Buffer work is deterministic, wall time is not.
	candidateExplain(t, ctx, tx, candidateOriginalSQL, args)
	candidateExplain(t, ctx, tx, matcherCandidatesBatchSQL, args)
	var originalTimes, batchTimes []float64
	var originalHits, batchHits float64
	var originalJIT, batchJIT float64
	for i := range 5 {
		queries := []string{candidateOriginalSQL, matcherCandidatesBatchSQL}
		if i%2 != 0 {
			queries[0], queries[1] = queries[1], queries[0]
		}
		for _, sql := range queries {
			plan := candidateExplain(t, ctx, tx, sql, args)
			if sql == candidateOriginalSQL {
				originalTimes = append(originalTimes, plan.ExecutionTime)
				originalHits = plan.Plan.Hits
				originalJIT = plan.JIT.Timing.Total
			} else {
				batchTimes = append(batchTimes, plan.ExecutionTime)
				batchHits = plan.Plan.Hits
				batchJIT = plan.JIT.Timing.Total
			}
		}
	}
	sort.Float64s(originalTimes)
	sort.Float64s(batchTimes)
	t.Logf("512 segments / 128 observations: median SQL ms %.3f -> %.3f; shared buffer hits %.0f -> %.0f",
		originalTimes[2], batchTimes[2], originalHits, batchHits)
	t.Logf("last measured JIT compilation ms %.3f -> %.3f (database defaults unchanged)", originalJIT, batchJIT)
	if batchHits >= originalHits {
		t.Fatal("bounded batching did not decrease measured SQL buffer work per request")
	}
}

func BenchmarkCandidateDenseSQL(b *testing.B) {
	ctx, tx := candidateTestDatabase(b)
	candidateInsertDense(b, ctx, tx, 512, true)
	observations := make([]MatcherObservation, 128)
	for i := range observations {
		observations[i] = MatcherObservation{Longitude: float64(i%32) * 0.00004, Latitude: float64(i/32+6) * 0.00004, RadiusMeters: 25}
	}
	args := candidateTestArgs(observations, 257, nil, nil)
	for _, query := range []struct{ name, sql string }{{"original", candidateOriginalSQL}, {"bounded_batch", matcherCandidatesBatchSQL}} {
		b.Run(query.name, func(b *testing.B) {
			candidateTestRows(b, ctx, tx, query.sql, args)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				candidateTestRows(b, ctx, tx, query.sql, args)
			}
			b.StopTimer()
			plan := candidateExplain(b, ctx, tx, query.sql, args)
			b.ReportMetric(plan.ExecutionTime, "sql-ms/op")
			b.ReportMetric(plan.Plan.Hits, "buffer-hits/op")
		})
	}
}

const candidateFixtureSQL = `
CREATE EXTENSION IF NOT EXISTS postgis;
CREATE SCHEMA osm_canonical;
CREATE TABLE osm_canonical.ways (
 region_id text,generation_id bigint,way_id bigint,version integer,
 PRIMARY KEY(region_id,generation_id,way_id)
) PARTITION BY LIST(region_id);
CREATE TABLE osm_canonical.path_segments (
 region_id text,generation_id bigint,segment_id uuid,derivation_version integer,
 logical_path_id uuid,locality_relation_id bigint,source_way_id bigint,source_way_version integer,
 start_graph_node_id uuid,end_graph_node_id uuid,name text,normalized_name text,
 highway text,broad_class text,tags jsonb,motor_forward_allowed boolean,motor_reverse_allowed boolean,
 geom geometry(LineString,4326),length_m double precision,
 PRIMARY KEY(region_id,generation_id,segment_id)
) PARTITION BY LIST(region_id);
CREATE INDEX candidate_geography ON osm_canonical.path_segments USING gist((geom::geography));
CREATE INDEX candidate_segment_identity ON osm_canonical.path_segments(segment_id,generation_id DESC,region_id DESC);
CREATE INDEX candidate_way_identity ON osm_canonical.ways(way_id,version DESC,generation_id DESC,region_id DESC);
CREATE TABLE osm_canonical.segments_a PARTITION OF osm_canonical.path_segments FOR VALUES IN ('fixture:a');
CREATE TABLE osm_canonical.segments_b PARTITION OF osm_canonical.path_segments FOR VALUES IN ('fixture:b');
CREATE TABLE osm_canonical.segments_c PARTITION OF osm_canonical.path_segments FOR VALUES IN ('fixture:c');
CREATE TABLE osm_canonical.ways_a PARTITION OF osm_canonical.ways FOR VALUES IN ('fixture:a');
CREATE TABLE osm_canonical.ways_b PARTITION OF osm_canonical.ways FOR VALUES IN ('fixture:b');
CREATE TABLE osm_canonical.ways_c PARTITION OF osm_canonical.ways FOR VALUES IN ('fixture:c');
-- Detached retired poison must neither participate in preference nor be scanned.
CREATE TABLE osm_canonical.retired_segments (LIKE osm_canonical.path_segments INCLUDING ALL);
CREATE TABLE osm_canonical.retired_ways (LIKE osm_canonical.ways INCLUDING ALL);
`

const candidateSemanticFixtureSQL = `
INSERT INTO osm_canonical.path_segments
SELECT 'fixture:'||region,generation,lpad(segment::text,32,'0')::uuid,2,lpad('99',32,'0')::uuid,
       CASE WHEN segment%2=0 THEN 42 ELSE NULL END,way,version,
       lpad('11',32,'0')::uuid,CASE WHEN segment=8 THEN lpad('11',32,'0')::uuid ELSE lpad('12',32,'0')::uuid END,
       CASE WHEN segment%2=0 THEN 'Synthetic' ELSE NULL END,
       CASE WHEN segment%3=0 THEN '' ELSE NULL END,'footway','path',jsonb_build_object('fixture',segment),
       segment%2=0,segment%3=0,ST_GeomFromText(wkt,4326),CASE WHEN segment=9 THEN 0 ELSE 123.456789 END
FROM (VALUES
 ('a',10,1,1,1,'LINESTRING(-0.001 0,0.001 0)'),
 ('b',20,1,2,1,'LINESTRING(10 10,10.001 10)'),
 ('c',20,2,2,2,'LINESTRING(20 20,20.001 20)'),
 ('a',10,3,3,1,'LINESTRING(-0.001 0,0.001 0)'),
 ('b',20,30,3,2,'LINESTRING(10 10,10.001 10)'),
 ('a',10,4,4,2,'LINESTRING(-0.001 0,0.001 0)'),
 ('b',20,40,4,1,'LINESTRING(10 10,10.001 10)'),
 ('a',10,5,5,1,'LINESTRING(-0.001 0,0.001 0)'),
 ('b',20,6,6,1,'LINESTRING(-0.001 0,0.001 0)'),
 ('c',20,60,6,1,'LINESTRING(10 10,10.001 10)'),
 ('a',10,7,7,1,'LINESTRING(-0.001 0,0.001 0)'),
 ('b',20,70,7,1,'LINESTRING(10 10,10.001 10)'),
 ('a',10,8,8,1,'LINESTRING(-0.0001 0,0 0.0001,0.0001 0,-0.0001 0)'),
 ('a',10,9,9,1,'LINESTRING(0 0,0 0)'),
 ('a',10,10,10,1,'LINESTRING(-0.00123456789 -0.0001234567,0.000031415926 0.000027182818,0.00123456789 0.0001234567)'),
 ('a',10,11,11,1,'LINESTRING(179.9999 0,-179.9999 0)'),
 ('b',20,12,12,1,'LINESTRING(179.9999 0.00001,180 0.00001)'),
 ('a',10,13,13,1,'LINESTRING(-1 89.99999,1 89.99999)'),
 ('b',20,14,14,1,'LINESTRING(-1 -89.99999,1 -89.99999)'),
 ('a',10,15,15,1,'LINESTRING(-0.001 0,0.001 0)')
) input(region,generation,segment,way,version,wkt);
INSERT INTO osm_canonical.ways
 SELECT region_id,generation_id,source_way_id,source_way_version FROM osm_canonical.path_segments;
INSERT INTO osm_canonical.retired_segments
 SELECT region_id,999,segment_id,derivation_version,logical_path_id,locality_relation_id,source_way_id,999,
        start_graph_node_id,end_graph_node_id,'Retired poison','retired poison',highway,broad_class,tags,
        motor_forward_allowed,motor_reverse_allowed,geom,length_m FROM osm_canonical.path_segments;
INSERT INTO osm_canonical.retired_ways
 SELECT region_id,999,way_id,999 FROM osm_canonical.ways;
`
