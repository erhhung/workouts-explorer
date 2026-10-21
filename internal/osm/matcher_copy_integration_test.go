package osm

import (
	"context"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// This test owns an empty, disposable loopback database, never an application DB.
// Fixture schemas and rows are created transactionally and always rolled back.
func TestMatcherCopySegmentsEquivalenceIntegration(t *testing.T) {
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
	for _, fallback := range config.Fallbacks {
		if fallback.Host != "127.0.0.1" && fallback.Host != "localhost" && fallback.Host != "::1" {
			t.Fatal("matcher copy integration requires loopback database fallbacks")
		}
	}
	if !strings.HasPrefix(config.Database, "workouts_matcher_copy_test_") ||
		(config.Host != "127.0.0.1" && config.Host != "localhost" && config.Host != "::1") {
		t.Fatal("matcher copy integration requires a workouts_matcher_copy_test_* loopback database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal("connect to disposable matcher database failed")
	}
	defer conn.Close(context.Background())
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(ctx, matcherCopyFixtureSQL); err != nil {
		t.Fatalf("create empty synthetic PostGIS fixture (requires PostGIS and unused OSM schemas): %v", err)
	}
	id := func(n int) uuid.UUID { return uuid.MustParse(fmt.Sprintf("%032d", n)) }
	ref := func(n int, region string, generation int64) MatcherSegmentRef {
		return MatcherSegmentRef{SegmentID: id(n), RegionID: "fixture:" + region, GenerationID: generation}
	}
	// Deliberately not segment-ID order. Missing references must leave ordinal gaps;
	// repeated paths and exact versions must not collapse or multiply copy rows.
	refs := []MatcherSegmentRef{
		ref(13, "a", 10), ref(999, "a", 10), ref(1, "a", 1), ref(3, "b", 20),
		ref(1, "a", 40), ref(12, "a", 10), ref(11, "a", 1), ref(14, "a", 10),
		ref(2, "b", 20), ref(3, "c", 20), ref(15, "a", 10), ref(1, "a", 10),
	}
	regions, generations, segments := make([]string, len(refs)), make([]int64, len(refs)), make([]uuid.UUID, len(refs))
	for i, ref := range refs {
		regions[i], generations[i], segments[i] = ref.RegionID, ref.GenerationID, ref.SegmentID
	}
	// Freeze the previous join as the comparison oracle; all other query clauses
	// remain identical, so the comparison covers every returned copy column.
	start := strings.Index(matcherCopySegmentsSQL, "LEFT JOIN LATERAL (")
	end := strings.Index(matcherCopySegmentsSQL, "ORDER BY requested.ordinal")
	if start < 0 || end <= start {
		t.Fatal("cannot locate scoped path join")
	}
	oldQuery := matcherCopySegmentsSQL[:start] +
		"LEFT JOIN osm_active.logical_paths path ON path.logical_path_id=segment.logical_path_id\n" + matcherCopySegmentsSQL[end:]
	query := func(t *testing.T, sql string, limit int) []MatcherSegmentCopy {
		t.Helper()
		rows, err := tx.Query(ctx, sql, regions, generations, segments, limit)
		if err != nil {
			t.Fatal(err)
		}
		copies, err := pgx.CollectRows(rows, pgx.RowToStructByPos[MatcherSegmentCopy])
		if err != nil {
			t.Fatal(err)
		}
		return copies
	}
	full := query(t, oldQuery, len(refs)+1)
	if len(full) != len(refs)-1 {
		t.Fatalf("old query returned %d copies, want %d", len(full), len(refs)-1)
	}
	for _, limit := range []int{0, 1, 4, len(refs) - 1, len(refs) + 1} {
		t.Run(fmt.Sprintf("limit_%d", limit), func(t *testing.T) {
			old, scoped := query(t, oldQuery, limit), query(t, matcherCopySegmentsSQL, limit)
			wantCount := min(limit, len(full))
			if len(scoped) != wantCount || !reflect.DeepEqual(old, scoped) || !reflect.DeepEqual(scoped, full[:wantCount]) {
				t.Fatalf("old/scoped ordered copies differ at limit %d: old=%+v scoped=%+v", limit, old, scoped)
			}
		})
	}
	copies, err := (&MatcherSnapshot{tx: tx}).CopySegments(ctx, refs)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(copies, full) {
		t.Fatal("CopySegments differs from the full old-query output")
	}
	wantOrdinals := []int{0, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}
	for i, copy := range copies {
		if copy.Ordinal != wantOrdinals[i] || copy.SegmentID != refs[copy.Ordinal].SegmentID ||
			copy.RegionID != refs[copy.Ordinal].RegionID || copy.GenerationID != refs[copy.Ordinal].GenerationID {
			t.Fatalf("copy lost input ordinal or exact provenance: %+v", copy)
		}
		var name, normalized *string
		switch copy.LogicalPathID {
		case id(100):
			// Independent minima across active, preferred members in all regions.
			n, norm := "Beta", "alpha"
			name, normalized = &n, &norm
		case id(400):
			n, norm := "Fixture School", "fixture school"
			name, normalized = &n, &norm
		case id(500):
			n, norm := "Explicit", "fixture school"
			name, normalized = &n, &norm
		case id(600):
			n := ""
			name, normalized = &n, &n
		}
		if !reflect.DeepEqual(copy.PathName, name) || !reflect.DeepEqual(copy.PathNormalizedName, normalized) {
			t.Fatalf("wrong active path aggregate/fallback: %+v", copy)
		}
		switch copy.Ordinal {
		case 0, 7, 10:
			if !reflect.DeepEqual(copy.SegmentName, name) || !reflect.DeepEqual(copy.SegmentNormalizedName, normalized) {
				t.Fatalf("wrong segment null/education fallback: %+v", copy)
			}
		case 2:
			if copy.SegmentName == nil || *copy.SegmentName != "Retired Segment" ||
				copy.SegmentNormalizedName == nil || *copy.SegmentNormalizedName != "retired segment" ||
				copy.LocalityRelationID == nil || *copy.LocalityRelationID != 99 ||
				copy.LocalityRelationVersion == nil || *copy.LocalityRelationVersion != 7 ||
				copy.LocalityName == nil || *copy.LocalityName != "Retired Locality" ||
				copy.DerivationVersion != 2 || copy.SourceWayID != 1 || copy.SourceWayVersion != 1 ||
				copy.Highway != "footway" || copy.BroadClass != "path" || copy.LengthMeters != 1001 ||
				string(copy.Tags) != `{"fixture_generation": 1}` ||
				string(copy.GeoJSON) != `{"type": "LineString", "coordinates": [[0, 0], [1, 1]]}` {
				t.Fatalf("retired copy did not preserve full exact segment/locality: %+v", copy)
			}
		case 4:
			if copy.LocalityRelationID == nil || copy.LocalityRelationVersion != nil || copy.LocalityName != nil {
				t.Fatalf("missing versioned locality did not remain null: %+v", copy)
			}
		case 5:
			if copy.SegmentName != nil || copy.SegmentNormalizedName != nil || copy.LocalityRelationID != nil ||
				copy.LocalityRelationVersion != nil || copy.LocalityName != nil {
				t.Fatalf("null names/locality did not remain null: %+v", copy)
			}
		case 6:
			if copy.SegmentName == nil || *copy.SegmentName != "Retired Only" ||
				copy.SegmentNormalizedName == nil || *copy.SegmentNormalizedName != "retired only" {
				t.Fatalf("inactive-only path lost its exact segment names: %+v", copy)
			}
		}
	}
	missing, err := (&MatcherSnapshot{tx: tx}).CopySegments(ctx, []MatcherSegmentRef{ref(999, "a", 10)})
	if err != nil || len(missing) != 0 {
		t.Fatalf("absent exact reference: copies=%+v err=%v", missing, err)
	}
}

const matcherCopyFixtureSQL = `
CREATE EXTENSION IF NOT EXISTS postgis;
CREATE SCHEMA osm_catalog;
CREATE SCHEMA osm_canonical;
CREATE SCHEMA osm_active;
CREATE TABLE osm_catalog.generations (region_id text, id bigint, state text);
INSERT INTO osm_catalog.generations VALUES
 ('fixture:a',1,'retired'),('fixture:a',10,'active'),('fixture:a',40,'validating'),
 ('fixture:b',20,'active'),('fixture:c',20,'active'),('fixture:d',30,'active');
CREATE TABLE osm_canonical.ways (region_id text, generation_id bigint, way_id bigint, version integer);
CREATE TABLE osm_canonical.localities (
 region_id text, generation_id bigint, relation_id bigint, relation_version integer, name text);
INSERT INTO osm_canonical.localities VALUES
 ('fixture:a',1,99,7,'Retired Locality'),('fixture:a',10,99,8,'Active Locality'),
 ('fixture:b',20,99,9,'Other Region Locality');
CREATE TABLE osm_canonical.path_segments (
 region_id text, generation_id bigint, segment_id uuid, derivation_version integer,
 logical_path_id uuid, locality_relation_id bigint, source_way_id bigint, source_way_version integer,
 name text, normalized_name text, highway text, broad_class text, tags jsonb,
 geom geometry(LineString,4326), length_m double precision,
 PRIMARY KEY (region_id,generation_id,segment_id));
INSERT INTO osm_canonical.path_segments
SELECT 'fixture:'||region,generation,lpad(segment::text,32,'0')::uuid,(generation+1)::integer,
 lpad(path::text,32,'0')::uuid,locality,way,version,name,normalized,'footway','path',
 jsonb_build_object('fixture_generation',generation) || CASE WHEN education THEN
 '{"workouts:education_name":"Fixture School","workouts:education_normalized_name":"fixture school"}'::jsonb
 ELSE '{}'::jsonb END,
 ST_SetSRID(ST_MakeLine(ST_Point(0,0),ST_Point(generation,segment)),4326),1000+generation
FROM (VALUES
 ('a',1,1,100,99,1,1,'Retired Segment','retired segment',false),
 ('a',40,1,100,99,1,9,'A inactive poison','000 inactive',false),
 ('a',10,1,100,99,1,2,'Zulu','alpha',true),
 ('b',20,2,100,NULL,2,1,'Beta','zeta',false),
 ('b',20,3,100,NULL,3,1,'A duplicate region','000 duplicate region',false),
 ('c',20,3,100,NULL,3,1,'Delta','delta',false),
 ('a',10,4,100,NULL,4,1,'A obsolete way','000 obsolete',false),
 ('b',20,5,100,NULL,4,2,'Gamma','gamma',false),
 ('a',10,6,100,NULL,6,2,'A older way generation','000 older generation',false),
 ('c',20,7,100,NULL,6,2,'Theta','theta',false),
 ('b',20,8,100,NULL,8,2,'A lower way region','000 lower region',false),
 ('c',20,9,100,NULL,8,2,'Omega','omega',false),
 ('b',20,10,100,NULL,10,1,'A duplicate generation','000 duplicate generation',false),
 ('d',30,10,100,NULL,10,1,'Iota','iota',false),
 ('a',1,11,200,NULL,11,1,'Retired Only','retired only',false),
 ('a',10,12,300,NULL,12,1,NULL,NULL,false),
 ('a',10,13,400,NULL,13,1,NULL,NULL,true),
 ('a',10,14,500,NULL,14,1,'Explicit',NULL,true),
 ('a',10,15,600,NULL,15,1,'','',true)
) fixture(region,generation,segment,path,locality,way,version,name,normalized,education);
INSERT INTO osm_canonical.ways SELECT DISTINCT region_id,generation_id,source_way_id,source_way_version
 FROM osm_canonical.path_segments;
-- Match the production active-view precedence before either aggregation runs.
CREATE VIEW osm_active.path_segments AS
SELECT segment.* FROM osm_canonical.path_segments segment
JOIN osm_catalog.generations generation
 ON generation.region_id=segment.region_id AND generation.id=segment.generation_id AND generation.state='active'
WHERE NOT EXISTS (
 SELECT 1 FROM osm_canonical.ways preferred_way
 JOIN osm_catalog.generations preferred_generation
  ON preferred_generation.region_id=preferred_way.region_id AND preferred_generation.id=preferred_way.generation_id
  AND preferred_generation.state='active'
 WHERE preferred_way.way_id=segment.source_way_id
  AND (preferred_way.version,preferred_way.generation_id,preferred_way.region_id)
    > (segment.source_way_version,segment.generation_id,segment.region_id)
) AND NOT EXISTS (
 SELECT 1 FROM osm_canonical.path_segments preferred_segment
 JOIN osm_catalog.generations preferred_generation
  ON preferred_generation.region_id=preferred_segment.region_id AND preferred_generation.id=preferred_segment.generation_id
  AND preferred_generation.state='active'
 WHERE preferred_segment.segment_id=segment.segment_id
  AND (preferred_segment.generation_id,preferred_segment.region_id) > (segment.generation_id,segment.region_id)
);
CREATE VIEW osm_active.logical_paths AS
SELECT logical_path_id,min(locality_relation_id) AS locality_relation_id,min(name) AS name,
 min(normalized_name) AS normalized_name,min(broad_class) AS broad_class,
 count(*) AS member_segment_count,sum(length_m) AS member_length_m
FROM osm_active.path_segments GROUP BY logical_path_id;
`
