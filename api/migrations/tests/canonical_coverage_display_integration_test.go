package migrations_test

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// This suite provisions synthetic rows, never databases or migrations. In
// particular, a retained actual-data clone is not a disposable fixture.
func TestCanonicalCoverageDisplayPostgreSQL(t *testing.T) {
	if os.Getenv("COVERAGE_CANONICAL_DISPLAY_TEST_DISPOSABLE") != "1" {
		t.Skip("COVERAGE_CANONICAL_DISPLAY_TEST_DISPOSABLE=1 and disposable role URLs are required")
	}
	var endpoint string
	for _, name := range []string{"API_DATABASE_URL", "WORKER_DATABASE_URL", "MIGRATION_DATABASE_URL"} {
		value := os.Getenv(name)
		cfg, err := pgx.ParseConfig(value)
		if value == "" || err != nil {
			t.Fatal(name + " must explicitly name a disposable database")
		}
		database := strings.ToLower(cfg.Database)
		privateSynthetic := cfg.Host == "/tmp/opencode/tile-local031-rehearsal/socket" && cfg.Database == "schema031_reference_test"
		if (!privateSynthetic && cfg.Host != "127.0.0.1" && cfg.Host != "localhost" && cfg.Host != "::1") ||
			(!strings.HasPrefix(database, "test_") && !strings.Contains(database, "_test")) ||
			strings.Contains(database, "clone") || strings.Contains(database, "actual") || strings.Contains(database, "live") {
			t.Fatal("canonical fixtures require a named loopback test database, not an actual-data clone")
		}
		for _, fallback := range cfg.Fallbacks {
			if fallback.Host != cfg.Host || fallback.Port != cfg.Port {
				t.Fatal("canonical fixtures prohibit alternate endpoints")
			}
		}
		target := fmt.Sprintf("%s:%d/%s", cfg.Host, cfg.Port, cfg.Database)
		if endpoint != "" && endpoint != target {
			t.Fatal("all three role URLs must address the same disposable database")
		}
		endpoint = target
	}
	if os.Getenv("COVERAGE_CANONICAL_DISPLAY_TEST_CLONE") == "1" || os.Getenv("COVERAGE_ENTITY_DETAIL_TEST_CLONE") == "1" {
		t.Fatal("actual-data clone fixtures are prohibited")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)
	db := testDatabases{ctx: ctx, api: openPool(t, ctx, os.Getenv("API_DATABASE_URL"), 3),
		worker:    openPool(t, ctx, os.Getenv("WORKER_DATABASE_URL"), 2),
		migration: openPool(t, ctx, os.Getenv("MIGRATION_DATABASE_URL"), 5)}
	assertRuntimeRole(t, ctx, db.api, "workouts_api")
	assertRuntimeRole(t, ctx, db.worker, "workouts_worker")
	assertRuntimeRole(t, ctx, db.migration, "workouts_migration")
	coverageDisplayCheck(t, db, `SELECT schema_version=31 AND minimum_runtime_version=31
		AND migration_baseline=27 FROM app.schema_metadata WHERE singleton`)

	t.Run("DisconnectedClassesAndCityReentry", func(t *testing.T) {
		f := newCanonicalDisplayFixture(t, db)
		s := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", nil)
		rows := canonicalDisplayRows(t, db, f, s)
		if len(rows) != 2 {
			t.Fatalf("two city components plus one county component must yield city=1 county=1: %s", rows)
		}
		city := canonicalDisplayFind(t, rows, "Synthetic City")
		county := canonicalDisplayFind(t, rows, "Synthetic County")
		canonicalDisplayAssert(t, city, "range_workout_count", float64(5), "all_time_workout_count", float64(7))
		canonicalDisplayAssert(t, county, "range_workout_count", float64(1), "all_time_workout_count", float64(1))
		canonicalDisplaySurfaces(t, db, f, s, county)
		id := uuid.MustParse(city["entity_id"].(string))
		if id == f.paths[0] || id == f.paths[1] || id != canonicalDisplayID("display:synthetic", "synthetic walk", "synthetic city", "") {
			t.Fatalf("identity must use the versioned display tuple, not source/class/component: %s", id)
		}
		narrow := canonicalDisplaySelection(t, db, f, "2026-01-12", "2026-01-12", nil)
		canonicalDisplayAssert(t, canonicalDisplayFind(t, canonicalDisplayRows(t, db, f, narrow), "Synthetic City"),
			"entity_id", id.String(), "range_workout_count", float64(2), "all_time_workout_count", float64(7))
		tx := canonicalDisplayAPI(t, db, f)
		defer tx.Rollback(ctx)
		var members int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.map_selection_coverage_entity_members($1,$2,$3,$4,'path',$5)`,
			f.account, f.session, s.id, s.generation, id).Scan(&members); err != nil || members != 2 {
			t.Fatalf("city members=%d err=%v", members, err)
		}
		_ = tx.Rollback(ctx)
		write := beginAccount(t, ctx, db.migration, f.account)
		canonicalDisplayAddPath(t, db, write, &f, "Synthetic Walk", "Synthetic City", "footway", "{}", nil)
		coverageDisplayExec(t, db, write, `INSERT INTO app.coverage_region_catalog(region_id,display_name)
			VALUES('display:synthetic-other','Synthetic Other Region') ON CONFLICT DO NOTHING`)
		coverageDisplayExec(t, db, write, `UPDATE app.path_segments SET region_id='display:synthetic-other'
			WHERE account_id=$1 AND physical_segment_id=$2`, f.account, f.segments[3])
		coverageDisplayInsertMatch(t, db, write, f, f.workouts[0], f.segments[3], 99)
		coverageDisplayCommit(t, db, write)
		otherRegion := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", nil)
		coverageDisplayCheck(t, db, `SELECT count(*)=2 AND count(DISTINCT canonical_id)=2
			FROM app.coverage_display_entities WHERE account_id=$1 AND context_id=$2 AND locality_name='Synthetic City'
			AND canonical_id=ANY($3::uuid[])`, f.account, otherRegion.context, []uuid.UUID{id,
			canonicalDisplayID("display:synthetic-other", "synthetic walk", "synthetic city", "")})
	})

	t.Run("NormalizationPreservesDirectionsPunctuationAccentsAndUnnamed", func(t *testing.T) {
		f := newCanonicalDisplayFixture(t, db)
		tx := beginAccount(t, ctx, db.migration, f.account)
		for _, name := range []any{"  SYNTHETIC\tWALK  ", "Synthetic East Walk", "Synthetic West Walk", "Synthetic-Walk", "Synthetic W\u00e1lk", "Synthetic Wa\u0301lk", nil, " \t "} {
			canonicalDisplayAddPath(t, db, tx, &f, name, " synthetic  CITY ", "footway", "{}", f.workouts[:1])
		}
		coverageDisplayCommit(t, db, tx)
		s := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", nil)
		rows := canonicalDisplayRows(t, db, f, s)
		if len(rows) != 8 {
			t.Fatalf("case/space and NFC must merge, directions/punctuation/accents and two unnamed sources must not: %s", rows)
		}
		coverageDisplayCheck(t, db, `SELECT count(*)=2 AND count(DISTINCT canonical_id)=2
			FROM app.coverage_display_members WHERE account_id=$1 AND context_id=$2 AND normalized_name=''
			AND key_discriminator=source_kind||'/'||source_id::text`, f.account, s.context)
		coverageDisplayCheck(t, db, `SELECT count(*)=2 AND count(DISTINCT canonical_id)=1
			FROM app.coverage_display_members WHERE account_id=$1 AND context_id=$2 AND normalized_name=U&'synthetic w\00E1lk'`, f.account, s.context)
	})

	t.Run("RegionalContextOverridesHiddenLocalityAndHistoricalFallback", func(t *testing.T) {
		f := newCanonicalDisplayFixture(t, db)
		tx := beginAccount(t, ctx, db.migration, f.account)
		for i := range 2 {
			coverageDisplayExec(t, db, tx, `UPDATE app.path_segments SET tags=$3::jsonb WHERE account_id=$1 AND physical_segment_id=$2`,
				f.account, f.segments[i], `{"workouts:park_kind":"state_park","workouts:park_name":"Synthetic State Reserve"}`)
		}
		coverageDisplayExec(t, db, tx, `UPDATE app.coverage_paths SET locality_name='Hidden Different County'
			WHERE account_id=$1 AND logical_path_id=$2`, f.account, f.paths[1])
		coverageDisplayCommit(t, db, tx)
		s := canonicalDisplaySelection(t, db, f, "2026-01-12", "2026-01-12", nil)
		row := canonicalDisplayFind(t, canonicalDisplayRows(t, db, f, s), "Synthetic State Reserve")
		canonicalDisplayAssert(t, row, "range_workout_count", float64(2), "all_time_workout_count", float64(7))
		coverageDisplayCheck(t, db, `SELECT count(*)=2 AND count(DISTINCT canonical_id)=1
			FROM app.coverage_display_members WHERE account_id=$1 AND context_id=$2
			AND source_id=ANY($3::uuid[]) AND locality_name='Synthetic State Reserve'`, f.account, s.context, f.paths[:2])
	})

	t.Run("CrossKindParkPreferredDistinctVisitUnionAndWholeGeometry", func(t *testing.T) {
		f := newCanonicalDisplayFixture(t, db)
		park := canonicalDisplayPark(t, db, f)
		bootstrapExec(t, db, f.bootstrapFixture, `UPDATE app.path_segments SET geom=ST_Translate(geom,0.1,0)
			WHERE account_id=$1`, f.account)
		s := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", f.workouts[1:2])
		rows := canonicalDisplayRows(t, db, f, s)
		if len(rows) != 2 {
			t.Fatalf("path and park with identical display labels must be one group: %s", rows)
		}
		row := canonicalDisplayFind(t, rows, "Synthetic City")
		canonicalDisplayAssert(t, row, "entity_kind", "park", "broad_class", "park", "range_workout_count", float64(5), "all_time_workout_count", float64(7))
		id := uuid.MustParse(row["entity_id"].(string))
		coverageDisplayCheck(t, db, `SELECT count(*)=3 AND count(*) FILTER(WHERE source_kind='park' AND source_id=$3)=1
			FROM app.coverage_display_members WHERE account_id=$1 AND context_id=$2 AND canonical_id=$4`, f.account, s.context, park, id)
		canonicalDisplayGeometry(t, db, f, s, "park", id, f.segments[:3])
		canonicalDisplaySurfaces(t, db, f, s, row)
		allLayers := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", f.workouts[:1])
		canonicalDisplayMapSurfaces(t, db, f, allLayers, true)
	})

	t.Run("DisplayedCatalogFallbackMergesExplicitParkPlace", func(t *testing.T) {
		f := newCanonicalDisplayFixture(t, db)
		park := canonicalDisplayPark(t, db, f)
		tx := beginAccount(t, ctx, db.migration, f.account)
		defer tx.Rollback(ctx)
		coverageDisplayExec(t, db, tx, `UPDATE app.coverage_paths SET locality_relation_id=NULL,
			locality_relation_version=NULL,locality_name=NULL WHERE account_id=$1 AND logical_path_id=ANY($2::uuid[])`, f.account, f.paths[:2])
		coverageDisplayExec(t, db, tx, `UPDATE app.coverage_region_catalog SET display_name='TestCounty'
			WHERE region_id='display:synthetic' AND $1::uuid IS NOT NULL`, f.account)
		coverageDisplayExec(t, db, tx, `UPDATE app.coverage_parks SET locality_name='TestCounty'
			WHERE account_id=$1 AND park_id=$2`, f.account, park)
		coverageDisplayCommit(t, db, tx)
		s := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", f.workouts[:1])
		rows := canonicalDisplayRows(t, db, f, s)
		if len(rows) != 2 {
			t.Fatalf("NULL-locality paths and explicitly placed park must merge by displayed catalog fallback: %v", rows)
		}
		row := canonicalDisplayFind(t, rows, "TestCounty")
		canonicalDisplayAssert(t, row, "entity_kind", "park", "region_id", "display:synthetic", "region_name", "TestCounty",
			"entity_id", canonicalDisplayID("display:synthetic", "synthetic walk", "testcounty", "").String(),
			"range_workout_count", float64(5), "all_time_workout_count", float64(7))
		coverageDisplayCheck(t, db, `SELECT count(*)=3 AND count(DISTINCT canonical_id)=1
			FROM app.coverage_display_members WHERE account_id=$1 AND context_id=$2
			AND (source_id=ANY($3::uuid[]) OR source_id=$4)`, f.account, s.context, f.paths[:2], park)
		canonicalDisplaySurfaces(t, db, f, s, row)
		canonicalDisplayMapSurfaces(t, db, f, s, true)
	})

	t.Run("MissingCatalogSlugFallbackAndExplicitPlaceMergeWithoutBlankFallback", func(t *testing.T) {
		f := newCanonicalDisplayFixture(t, db)
		park := uuid.New()
		tx := beginAccount(t, ctx, db.migration, f.account)
		defer tx.Rollback(ctx)
		tags := fmt.Sprintf(`{"workouts:park_id":%q,"workouts:park_kind":"local_park","workouts:park_name":"Synthetic Coastal Walk","workouts:park_normalized_name":"synthetic coastal walk"}`, park.String())
		canonicalDisplayAddPath(t, db, tx, &f, "Synthetic Coastal Walk", "Coastal Test", "footway", "{}", nil)
		canonicalDisplayAddPath(t, db, tx, &f, "Synthetic Coastal Walk", "Coastal Test", "footway", tags, nil)
		canonicalDisplayAddPath(t, db, tx, &f, "Synthetic Coastal Walk", " ", "footway", "{}", nil)
		coverageDisplayExec(t, db, tx, `UPDATE app.coverage_paths SET locality_relation_id=NULL,
			locality_relation_version=NULL,locality_name=NULL WHERE account_id=$1 AND logical_path_id=$2`, f.account, f.paths[3])
		// Physical region IDs permit hyphens, not underscores. The exact underscore
		// and empty-string resolver inputs are covered separately below.
		coverageDisplayExec(t, db, tx, `UPDATE app.path_segments SET region_id='geofabrik:coastal-test'
			WHERE account_id=$1 AND physical_segment_id=ANY($2::uuid[])`, f.account, f.segments[3:])
		coverageDisplayInsertMatch(t, db, tx, f, f.workouts[1], f.segments[3], 50)
		coverageDisplayInsertMatch(t, db, tx, f, f.workouts[0], f.segments[4], 51)
		coverageDisplayInsertMatch(t, db, tx, f, f.workouts[0], f.segments[5], 52)
		coverageDisplayCommit(t, db, tx)
		coverageDisplayCheck(t, db, `SELECT NOT EXISTS(SELECT 1 FROM app.coverage_region_catalog
			WHERE region_id='geofabrik:coastal-test') AND $1::uuid IS NOT NULL`, f.account)
		s := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", nil)
		rows := canonicalDisplayRows(t, db, f, s)
		if len(rows) != 4 {
			t.Fatalf("missing catalog fallback must merge NULL and explicit place, but not non-NULL blank place: %v", rows)
		}
		row := canonicalDisplayFind(t, rows, "Coastal Test")
		merged := canonicalDisplayID("geofabrik:coastal-test", "synthetic coastal walk", "coastal test", "")
		blank := canonicalDisplayID("geofabrik:coastal-test", "synthetic coastal walk", "", "")
		canonicalDisplayAssert(t, row, "entity_id", merged.String(), "entity_kind", "park", "region_name", nil,
			"range_workout_count", float64(2), "all_time_workout_count", float64(2))
		coverageDisplayCheck(t, db, `SELECT count(*)=4 AND count(DISTINCT canonical_id)=2
			AND count(*) FILTER(WHERE canonical_id=$3)=3 AND count(*) FILTER(WHERE canonical_id=$4)=1
			FROM app.coverage_display_members WHERE account_id=$1 AND context_id=$2
			AND region_id='geofabrik:coastal-test'`, f.account, s.context, merged, blank)
		canonicalDisplaySurfaces(t, db, f, s, row)
	})

	t.Run("StoredPlaceResolverExactEmptyAndUnderscoreBoundaries", func(t *testing.T) {
		f := newCanonicalDisplayFixture(t, db)
		tx := beginAccount(t, ctx, db.migration, f.account)
		defer tx.Rollback(ctx)
		coverageDisplayExec(t, db, tx, `UPDATE app.coverage_region_catalog SET display_name='TestCounty'
			WHERE region_id='display:synthetic' AND $1::uuid IS NOT NULL`, f.account)
		var body string
		if err := tx.QueryRow(ctx, `SELECT prosrc FROM pg_proc WHERE oid='app.coverage_display_source_visits(uuid,date,date)'::regprocedure
			AND prosecdef AND pg_get_userbyid(proowner)='workouts_security_owner'`).Scan(&body); err != nil {
			t.Fatal(err)
		}
		projection := strings.LastIndex(body, "SELECT source.source_kind")
		if projection < 0 {
			t.Fatal("stored source resolver must expose its final resolved_sources place projection")
		}
		// Execute the real stored projection, not a copied fallback expression.
		// Legacy source constraints cannot represent these two scalar inputs;
		// no source constraints, trigger state, or compact evidence is modified.
		coverageDisplayExec(t, db, tx, `SET LOCAL ROLE workouts_security_owner`)
		query := `WITH resolved_sources AS (
			SELECT sample.source_kind,lpad(sample.source_id,32,'0')::uuid source_id,$1::uuid workout_id,DATE '2026-01-10' local_start_date,
			TIMESTAMPTZ '2026-01-10T08:00:00Z' started_at,'footway'::text broad_class,'Synthetic Place Boundary'::text name,
			sample.locality_name,sample.region_id,10::double precision minimum_longitude,20::double precision minimum_latitude,
			10.001::double precision maximum_longitude,20.001::double precision maximum_latitude,
			10::double precision range_minimum_longitude,20::double precision range_minimum_latitude,
			10.001::double precision range_maximum_longitude,20.001::double precision range_maximum_latitude
			FROM (VALUES
			('path'::text,1::text,NULL::text,'geofabrik:coastal_test'::text),
			('park',2::text,'Coastal Test','geofabrik:coastal_test'),
			('path',3::text,'','geofabrik:coastal_test'),
			('path',4::text,NULL,'display:synthetic'),
			('park',5::text,'TestCounty','display:synthetic'),
			('path',6::text,'','display:synthetic')) sample(source_kind,source_id,locality_name,region_id)
		) SELECT resolved.source_id,resolved.locality_name,
		app.coverage_display_identity(resolved.region_id,app.coverage_display_normalize(resolved.name),
			app.coverage_display_normalize(resolved.locality_name),'')
		FROM (` + strings.TrimSuffix(strings.TrimSpace(body[projection:]), ";") + `) resolved ORDER BY resolved.source_id`
		rows, err := tx.Query(ctx, query, f.workouts[0])
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		wantPlaces := []string{"Coastal Test", "Coastal Test", "", "TestCounty", "TestCounty", ""}
		count := 0
		for rows.Next() {
			var place string
			var source, id uuid.UUID
			if err := rows.Scan(&source, &place, &id); err != nil {
				t.Fatal(err)
			}
			if count >= len(wantPlaces) {
				t.Fatal("stored place projection multiplied source rows")
			}
			region := "geofabrik:coastal_test"
			if count >= 3 {
				region = "display:synthetic"
			}
			want := wantPlaces[count]
			if source != uuid.MustParse(fmt.Sprintf("%032d", count+1)) || place != want || id != canonicalDisplayID(region, "synthetic place boundary", strings.ToLower(want), "") {
				t.Fatalf("stored place resolver must preserve empty, humanize underscore slug, and merge explicit/fallback text: source=%s place=%q id=%s wantPlace=%q", source, place, id, want)
			}
			count++
		}
		if err := rows.Err(); err != nil || count != len(wantPlaces) {
			t.Fatalf("stored place boundary rows=%d want=%d err=%v", count, len(wantPlaces), err)
		}
	})

	t.Run("LastParkTagMetadataRemovalHidesInvalidSingletonButRetainsMergedHistory", func(t *testing.T) {
		f := newCanonicalDisplayFixture(t, db)
		park := canonicalDisplayPark(t, db, f)
		tx := beginAccount(t, ctx, db.migration, f.account)
		defer tx.Rollback(ctx)
		historical := coverageEntityStatsClone(t, db, tx, f, f.segments[1], "{}", 0.004)
		coverageDisplayInsertMatch(t, db, tx, f, f.workouts[5], historical, 40)
		coverageDisplayExec(t, db, tx, `UPDATE app.coverage_parks SET name='Synthetic Vanishing Park'
			WHERE account_id=$1 AND park_id=$2`, f.account, park)
		coverageDisplayCommit(t, db, tx)
		before := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", nil)
		coverageDisplayCheck(t, db, `SELECT count(*)=1 FROM app.coverage_display_entities WHERE account_id=$1
			AND context_id=$2 AND name='Synthetic Vanishing Park' AND range_workout_count=1
			AND minimum_longitude IS NOT NULL AND region_id='display:synthetic'`, f.account, before.context)
		// Updating source metadata is supported and does not delete park visits.
		// Remove every tag, including historical evidence, without touching facts.
		bootstrapExec(t, db, f.bootstrapFixture, `UPDATE app.path_segments SET tags=tags-'workouts:park_id'
			-'workouts:park_kind'-'workouts:park_name'-'workouts:park_normalized_name' WHERE account_id=$1`, f.account)
		missing := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", nil)
		if missing.generation != before.generation+1 || missing.revision != before.revision+1 || missing.context == before.context {
			t.Fatalf("last-tag metadata transaction must invalidate and prepare a fresh context once: before=%+v after=%+v", before, missing)
		}
		rows := canonicalDisplayRows(t, db, f, missing)
		if len(rows) != 2 {
			t.Fatalf("a park with visits but no geometry must not poison the list: %v", rows)
		}
		read := canonicalDisplayAPI(t, db, f)
		var complete bool
		err := read.QueryRow(ctx, `SELECT count(*)=2 AND bool_and(region_id IS NOT NULL
			AND minimum_longitude IS NOT NULL AND minimum_latitude IS NOT NULL
			AND maximum_longitude IS NOT NULL AND maximum_latitude IS NOT NULL
			AND minimum_longitude<=maximum_longitude AND minimum_latitude<=maximum_latitude)
			FROM app.map_selection_coverage_entities($1,$2,$3,$4,NULL)`, f.account, f.session, missing.id, missing.generation).Scan(&complete)
		_ = read.Rollback(ctx)
		if err != nil || !complete {
			t.Fatalf("last-tag metadata update exposed incomplete SQL list bounds/region: complete=%t err=%v", complete, err)
		}
		for _, row := range rows {
			if row["name"] == "Synthetic Vanishing Park" {
				t.Fatalf("geometry-less park remained API-visible: %v", row)
			}
			for _, field := range []string{"minimum_longitude", "minimum_latitude", "maximum_longitude", "maximum_latitude"} {
				if _, ok := row[field].(float64); !ok {
					t.Fatalf("list would fail the Go API's numeric bounds decoder: %s=%v", field, row[field])
				}
			}
			if row["region_id"] != "display:synthetic" {
				t.Fatalf("geometry-less member poisoned a visible region: %v", row)
			}
		}
		coverageDisplayCheck(t, db, `SELECT (SELECT count(*)=2 AND bool_and(workout_id=ANY($3::uuid[]))
			FROM app.workout_park_attributions WHERE account_id=$1 AND park_id=$2)
			AND NOT EXISTS(SELECT 1 FROM app.workout_park_display_bounds WHERE account_id=$1 AND park_id=$2)`,
			f.account, park, []uuid.UUID{f.workouts[0], f.workouts[5]})
		// Restore only historical geometry. The retained park visits now resolve
		// to the valid named-path group, whose in-range paths supply its geometry.
		tx = beginAccount(t, ctx, db.migration, f.account)
		tags := fmt.Sprintf(`{"workouts:park_id":%q,"workouts:park_kind":"local_park","workouts:park_name":"Synthetic Walk","workouts:park_normalized_name":"synthetic walk"}`, park.String())
		coverageDisplayExec(t, db, tx, `UPDATE app.path_segments SET tags=$3::jsonb WHERE account_id=$1 AND physical_segment_id=$2`, f.account, historical, tags)
		coverageDisplayExec(t, db, tx, `UPDATE app.coverage_parks SET name='Synthetic Walk' WHERE account_id=$1 AND park_id=$2`, f.account, park)
		coverageDisplayCommit(t, db, tx)
		merged := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", f.workouts[:1])
		row := canonicalDisplayFind(t, canonicalDisplayRows(t, db, f, merged), "Synthetic City")
		canonicalDisplayAssert(t, row, "entity_kind", "park", "region_id", "display:synthetic",
			"entity_id", canonicalDisplayID("display:synthetic", "synthetic walk", "synthetic city", "").String(),
			"range_workout_count", float64(5), "all_time_workout_count", float64(7),
			"minimum_longitude", float64(10), "maximum_longitude", 10.003,
			"all_time_first_workout_id", f.workouts[5].String(), "all_time_latest_workout_id", f.workouts[6].String())
		coverageDisplayCheck(t, db, `SELECT count(*)=3 AND count(*) FILTER(WHERE source_kind='park' AND source_id=$3)=1
			FROM app.coverage_display_members WHERE account_id=$1 AND context_id=$2 AND canonical_id=$4`,
			f.account, merged.context, park, uuid.MustParse(row["entity_id"].(string)))
		canonicalDisplaySurfaces(t, db, f, merged, row)
		canonicalDisplayGeometry(t, db, f, merged, "park", uuid.MustParse(row["entity_id"].(string)), f.segments[:2])
		canonicalDisplayMapSurfaces(t, db, f, merged, false)
	})

	t.Run("HistoricalParkVisitsAndIndependentRangeBoundsDoNotInventVisits", func(t *testing.T) {
		f := newCanonicalDisplayFixture(t, db)
		park := canonicalDisplayPark(t, db, f)
		tx := beginAccount(t, ctx, db.migration, f.account)
		defer tx.Rollback(ctx)
		coverageDisplayExec(t, db, tx, `DELETE FROM app.workout_segment_matches WHERE account_id=$1
			AND workout_id=$2 AND physical_segment_id=$3`, f.account, f.workouts[4], f.segments[0])
		tags := fmt.Sprintf(`{"workouts:park_id":%q,"workouts:park_kind":"local_park","workouts:park_name":"Synthetic Walk","workouts:park_normalized_name":"synthetic walk"}`, park.String())
		canonicalDisplayAddPath(t, db, tx, &f, nil, "Synthetic City", "footway", tags, f.workouts[4:5])
		coverageDisplayExec(t, db, tx, `DELETE FROM app.workout_park_attributions WHERE account_id=$1 AND park_id=$2
			AND workout_id=ANY($3::uuid[])`, f.account, park, []uuid.UUID{f.workouts[0], f.workouts[4]})
		coverageDisplayCommit(t, db, tx)
		coverageDisplayCheck(t, db, `SELECT
			(SELECT count(*)=1 AND bool_and(workout_id=$3) FROM app.workout_park_attributions WHERE account_id=$1 AND park_id=$2)
			AND EXISTS(SELECT 1 FROM app.workout_park_display_bounds WHERE account_id=$1 AND park_id=$2 AND workout_id=$4)
			AND NOT EXISTS(SELECT 1 FROM app.workout_path_display_partials partial JOIN app.coverage_paths path
				ON path.account_id=partial.account_id AND path.logical_path_id=partial.logical_path_id
				WHERE partial.account_id=$1 AND partial.workout_id=$4 AND (path.name IS NOT NULL OR partial.qualifies_without_name))`,
			f.account, park, f.workouts[5], f.workouts[4])
		s := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", f.workouts[4:5])
		row := canonicalDisplayFind(t, canonicalDisplayRows(t, db, f, s), "Synthetic City")
		id := canonicalDisplayID("display:synthetic", "synthetic walk", "synthetic city", "")
		canonicalDisplayAssert(t, row, "entity_id", id.String(), "entity_kind", "park", "region_id", "display:synthetic",
			"range_workout_count", float64(4), "all_time_workout_count", float64(6),
			"range_first_date", "2026-01-10", "range_first_workout_id", f.workouts[2].String(),
			"range_latest_date", "2026-01-12", "range_latest_workout_id", f.workouts[3].String(),
			"all_time_first_date", "2026-01-01", "all_time_first_workout_id", f.workouts[5].String(),
			"all_time_latest_date", "2026-02-01", "all_time_latest_workout_id", f.workouts[6].String(),
			"minimum_longitude", float64(10), "minimum_latitude", float64(20), "maximum_longitude", 10.007, "maximum_latitude", 20.007)
		coverageDisplayCheck(t, db, `SELECT
			EXISTS(SELECT 1 FROM app.coverage_display_members WHERE account_id=$1 AND context_id=$2
				AND source_kind='park' AND source_id=$3 AND canonical_id=$4)
			AND (SELECT count(*)=6 FROM app.coverage_display_visits WHERE account_id=$1 AND context_id=$2 AND canonical_id=$4)
			AND NOT EXISTS(SELECT 1 FROM app.coverage_display_visits WHERE account_id=$1 AND context_id=$2
				AND canonical_id=$4 AND workout_id=$5)`, f.account, s.context, park, id, f.workouts[4])
		canonicalDisplaySurfaces(t, db, f, s, row)
		canonicalDisplayGeometry(t, db, f, s, "park", id, f.segments)
		canonicalDisplayMapSurfaces(t, db, f, s, false)
	})

	t.Run("HistoricalOnlyMembersCompleteCountsAndTieOrder", func(t *testing.T) {
		f := newCanonicalDisplayFixture(t, db)
		bootstrapExec(t, db, f.bootstrapFixture, `DELETE FROM app.workout_segment_matches
			WHERE account_id=$1 AND physical_segment_id=$2 AND workout_id=$3`, f.account, f.segments[1], f.workouts[0])
		s := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", nil)
		row := canonicalDisplayFind(t, canonicalDisplayRows(t, db, f, s), "Synthetic City")
		canonicalDisplayAssert(t, row, "all_time_workout_count", float64(7), "range_workout_count", float64(5),
			"range_first_workout_id", f.workouts[2].String(), "range_latest_workout_id", f.workouts[4].String(),
			"all_time_first_workout_id", f.workouts[5].String(), "all_time_latest_workout_id", f.workouts[6].String())
		canonicalDisplayWorkoutWrite(t, db, f, `UPDATE app.workouts SET started_at='2026-01-12T08:00:00Z'
			WHERE account_id=$1 AND id=$2`, f.account, f.workouts[3])
		s = canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", nil)
		row = canonicalDisplayFind(t, canonicalDisplayRows(t, db, f, s), "Synthetic City")
		canonicalDisplayAssert(t, row, "range_latest_workout_id", f.workouts[3].String())
		canonicalDisplayWorkoutWrite(t, db, f, `UPDATE app.workouts SET started_at='2026-01-10T08:00:00Z'
			WHERE account_id=$1 AND id=$2`, f.account, f.workouts[2])
		s = canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", nil)
		row = canonicalDisplayFind(t, canonicalDisplayRows(t, db, f, s), "Synthetic City")
		canonicalDisplayAssert(t, row, "range_first_workout_id", f.workouts[0].String(), "range_latest_workout_id", f.workouts[3].String())
		coverageDisplayCheck(t, db, `SELECT count(*)=2 FROM app.coverage_display_members
			WHERE account_id=$1 AND context_id=$2 AND canonical_id=$3`, f.account, s.context, uuid.MustParse(row["entity_id"].(string)))
	})

	t.Run("HistoricalSourceMetadataAndRangeContextNotCheckedOrViewport", func(t *testing.T) {
		f := newCanonicalDisplayFixture(t, db)
		tx := beginAccount(t, ctx, db.migration, f.account)
		// A non-qualifying driveway still supplies range-specific locality.
		segment := coverageEntityStatsClone(t, db, tx, f, f.segments[0],
			`{"workouts:park_kind":"national_park","workouts:park_name":"Synthetic National Reserve","service":"driveway"}`, 0.04)
		coverageDisplayExec(t, db, tx, `UPDATE app.path_segments SET highway='service' WHERE account_id=$1 AND physical_segment_id=$2`, f.account, segment)
		coverageDisplayInsertMatch(t, db, tx, f, f.workouts[4], segment, 20)
		school := uuid.New()
		canonicalDisplayAddPath(t, db, tx, &f, "Synthetic Later Label", "Synthetic School City", "footway",
			fmt.Sprintf(`{"workouts:education_id":%q,"workouts:education_name":"Z Synthetic School"}`, school), f.workouts[:1])
		historical := coverageEntityStatsClone(t, db, tx, f, f.segments[len(f.segments)-1],
			`{"workouts:education_name":"A Synthetic Historical School"}`, 0.01)
		coverageDisplayInsertMatch(t, db, tx, f, f.workouts[5], historical, 30)
		coverageDisplayCommit(t, db, tx)
		full := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", nil)
		checked := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", f.workouts[:1])
		if full.context != checked.context || !reflect.DeepEqual(canonicalDisplayRows(t, db, f, full), canonicalDisplayRows(t, db, f, checked)) {
			t.Fatal("checked/focused selection must reuse complete context without changing labels, IDs or counts")
		}
		row := canonicalDisplayFind(t, canonicalDisplayRows(t, db, f, full), "Synthetic National Reserve")
		canonicalDisplayAssert(t, row, "name", "Synthetic Walk", "range_workout_count", float64(5), "all_time_workout_count", float64(6))
		historicalLabel := canonicalDisplayFind(t, canonicalDisplayRows(t, db, f, full), "Synthetic School City")
		canonicalDisplayAssert(t, historicalLabel, "name", "A Synthetic Historical School", "range_workout_count", float64(1), "all_time_workout_count", float64(2),
			"entity_id", canonicalDisplayID("display:synthetic", "a synthetic historical school", "synthetic school city", "").String())
		earlier := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-10", f.workouts[:1])
		early := canonicalDisplayFind(t, canonicalDisplayRows(t, db, f, earlier), "Synthetic City")
		canonicalDisplayAssert(t, early, "all_time_workout_count", float64(7))
		if early["entity_id"] == row["entity_id"] {
			t.Fatal("source reassignment by date context must change the canonical group")
		}
		canonicalDisplaySurfaces(t, db, f, checked, row)
		canonicalDisplayMapSurfaces(t, db, f, checked, false)
	})

	t.Run("MaterializedIndexedStatsWithoutFreshAggregation", func(t *testing.T) {
		f := newCanonicalDisplayFixture(t, db)
		s := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", nil)
		row := canonicalDisplayFind(t, canonicalDisplayRows(t, db, f, s), "Synthetic City")
		canonicalDisplaySurfaces(t, db, f, s, row)
		tx := beginAccount(t, ctx, db.migration, f.account)
		defer tx.Rollback(ctx)
		coverageDisplayExec(t, db, tx, `SET LOCAL ROLE workouts_security_owner`)
		coverageDisplayExec(t, db, tx, `SELECT set_config('app.session_id',$1,true)`, f.session.String())
		var body string
		if err := tx.QueryRow(ctx, `SELECT prosrc FROM pg_proc WHERE oid='app.map_selection_coverage_entity_stats(uuid,uuid,uuid,bigint,text,uuid)'::regprocedure`).Scan(&body); err != nil {
			t.Fatal(err)
		}
		_, query, found := strings.Cut(body, "RETURN QUERY ")
		if !found || !strings.HasSuffix(strings.TrimSpace(query), "END;") {
			t.Fatal("display30 point reader must expose its actual RETURN QUERY for nested-plan inspection")
		}
		query = strings.TrimSuffix(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(query), "END;")), ";")
		query = strings.NewReplacer("target_account_id", "'"+f.account.String()+"'::uuid", "authorized_id", "'"+s.context.String()+"'::uuid",
			"target_entity_id", "'"+row["entity_id"].(string)+"'::uuid", "target_entity_kind", "'path'::text").Replace(query)
		coverageDisplayExec(t, db, tx, `SET LOCAL enable_seqscan=off`)
		var plan []byte
		if err := tx.QueryRow(ctx, `EXPLAIN (ANALYZE,FORMAT JSON,TIMING OFF) `+query).Scan(&plan); err != nil {
			t.Fatal(err)
		}
		var document []struct{ Plan map[string]any }
		if err := json.Unmarshal(plan, &document); err != nil || len(document) != 1 {
			t.Fatalf("invalid point lookup plan: %s err=%v", plan, err)
		}
		indexed := false
		var inspect func(map[string]any)
		inspect = func(node map[string]any) {
			kind, _ := node["Node Type"].(string)
			if strings.Contains(kind, "Aggregate") || kind == "Seq Scan" {
				t.Fatalf("stats performs fresh aggregation/account scan: %s", plan)
			}
			if relation, ok := node["Relation Name"].(string); ok {
				if relation != "coverage_display_entities" {
					t.Fatalf("point stats touches source facts: %s", plan)
				}
				indexed = strings.Contains(kind, "Index") && node["Actual Rows"] == float64(1)
			}
			children, _ := node["Plans"].([]any)
			for _, child := range children {
				inspect(child.(map[string]any))
			}
		}
		inspect(document[0].Plan)
		if !indexed {
			t.Fatalf("expected one indexed canonical entity lookup: %s", plan)
		}
		for _, worker := range []bool{false, true} {
			pool := db.api
			if worker {
				pool = db.worker
			}
			for _, relation := range []string{"coverage_display_revisions", "coverage_display_contexts", "coverage_display_members", "coverage_display_visits", "coverage_display_entities", "coverage_display_invalidations"} {
				private := beginAccount(t, ctx, pool, f.account)
				_, err := private.Exec(ctx, `SELECT * FROM app.`+relation+` LIMIT 0`)
				_ = private.Rollback(ctx)
				bootstrapSQLState(t, err, "42501")
			}
		}
	})

	t.Run("MetadataCatalogAttributionDateRouteAppliedDeletionInvalidate", func(t *testing.T) {
		f := newCanonicalDisplayFixture(t, db)
		park := canonicalDisplayPark(t, db, f)
		for index, change := range []struct {
			query string
			args  []any
			fence bool
		}{
			{`UPDATE app.coverage_paths SET name='Synthetic Renamed Walk' WHERE account_id=$1 AND logical_path_id=$2`, []any{f.account, f.paths[0]}, false},
			{`UPDATE app.coverage_parks SET name='Synthetic Renamed Park' WHERE account_id=$1 AND park_id=$2`, []any{f.account, park}, false},
			{`UPDATE app.coverage_region_catalog SET display_name=display_name||' Updated' WHERE region_id=$2 AND $1::uuid IS NOT NULL`, []any{f.account, "display:synthetic"}, false},
			{`DELETE FROM app.workout_park_attributions WHERE account_id=$1 AND workout_id=$2 AND park_id=$3`, []any{f.account, f.workouts[0], park}, false},
			{`UPDATE app.workouts SET local_start_date='2026-01-11',started_at=started_at+interval '1 minute' WHERE account_id=$1 AND id=$2`, []any{f.account, f.workouts[1]}, true},
			{`UPDATE app.workout_routes SET route=NULL WHERE account_id=$1 AND workout_id=$2`, []any{f.account, f.workouts[2]}, false},
			{`UPDATE app.workout_coverage_states SET applied_route_input_revision=NULL WHERE account_id=$1 AND workout_id=$2`, []any{f.account, f.workouts[3]}, false},
			{`DELETE FROM app.workouts WHERE account_id=$1 AND id=$2`, []any{f.account, f.workouts[4]}, true},
		} {
			old := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", nil)
			tx := beginAccount(t, ctx, db.migration, f.account)
			if change.fence {
				canonicalDisplayFence(t, db, tx, f)
			}
			coverageDisplayExec(t, db, tx, change.query, change.args...)
			coverageDisplayCommit(t, db, tx)
			generation, revision := canonicalDisplayVersions(t, db, f)
			if generation != old.generation+1 || revision != old.revision+1 {
				t.Fatalf("input must invalidate exactly once: generation=%d revision=%d old=%+v query=%s", generation, revision, old, change.query)
			}
			if rows := canonicalDisplayRows(t, db, f, old); len(rows) != 0 {
				t.Fatalf("stale capability returned rows after %s: %s", change.query, rows)
			}
			fresh := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", nil)
			freshRows := canonicalDisplayRows(t, db, f, fresh)
			if fresh.context == old.context || len(freshRows) == 0 {
				t.Fatal("fresh selection must bind a new ready context")
			}
			coverageDisplayCheck(t, db, `SELECT coverage_display_context_id IS NULL
				FROM app.map_selections WHERE account_id=$1 AND id=$2`, f.account, old.id)
			var renamed map[string]any
			for _, row := range freshRows {
				if row["name"] == "Synthetic Renamed Walk" {
					renamed = row
				}
				if index >= 2 && !strings.HasSuffix(row["region_name"].(string), " Updated") {
					t.Fatalf("catalog invalidation retained stale label: %v", row)
				}
			}
			if renamed == nil {
				t.Fatal("metadata invalidation retained the old path label")
			}
			wantRange := 5
			if index >= 5 {
				wantRange -= index - 4
			}
			canonicalDisplayAssert(t, renamed, "range_workout_count", float64(wantRange), "all_time_workout_count", float64(wantRange+1))
			canonicalDisplaySurfaces(t, db, f, fresh, renamed)
		}
	})

	t.Run("CoalescedQueueDeferredGenerationAndSavepointRollback", func(t *testing.T) {
		f := newCanonicalDisplayFixture(t, db)
		g, r := canonicalDisplayVersions(t, db, f)
		tx := beginAccount(t, ctx, db.migration, f.account)
		defer tx.Rollback(ctx)
		for range 3 {
			coverageDisplayExec(t, db, tx, `UPDATE app.workout_segment_matches SET geom=ST_Translate(geom,0.00001,0)
				WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0])
		}
		var queued bool
		if err := tx.QueryRow(ctx, `SELECT (SELECT count(*)=1 FROM app.coverage_display_invalidations
			WHERE account_id=$1 AND txid=txid_current()) AND (SELECT generation=$2 FROM app.account_data_generations WHERE account_id=$1)
			AND (SELECT revision=$3 FROM app.coverage_display_revisions WHERE account_id=$1)`, f.account, g, r).Scan(&queued); err != nil || !queued {
			t.Fatalf("deferred queue is not coalesced or versions advanced early: %t err=%v", queued, err)
		}
		coverageDisplayExec(t, db, tx, `SAVEPOINT canonical_pending`)
		coverageDisplayExec(t, db, tx, `SET CONSTRAINTS ALL IMMEDIATE`)
		coverageDisplayExec(t, db, tx, `ROLLBACK TO SAVEPOINT canonical_pending`)
		if err := tx.QueryRow(ctx, `SELECT count(*)=1 FROM app.coverage_display_invalidations WHERE account_id=$1 AND txid=txid_current()`, f.account).Scan(&queued); err != nil || !queued {
			t.Fatalf("savepoint lost deferred invalidation: %t err=%v", queued, err)
		}
		coverageDisplayCommit(t, db, tx)
		ng, nr := canonicalDisplayVersions(t, db, f)
		if ng != g+1 || nr != r+1 {
			t.Fatalf("multi-row/multi-statement transaction bumped more than once: %d/%d -> %d/%d", g, r, ng, nr)
		}
		coverageDisplayCheck(t, db, `SELECT NOT EXISTS(SELECT 1 FROM app.coverage_display_invalidations WHERE account_id=$1)
			AND NOT EXISTS(SELECT 1 FROM app.coverage_display_dirty WHERE account_id=$1)`, f.account)
	})

	t.Run("MetadataAndCatalogNoOpsVersusMixedTransactionSingleFlush", func(t *testing.T) {
		f := newCanonicalDisplayFixture(t, db)
		park := canonicalDisplayPark(t, db, f)
		warm := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", nil)
		before := canonicalDisplayRows(t, db, f, warm)
		tx := beginAccount(t, ctx, db.migration, f.account)
		defer tx.Rollback(ctx)
		canonicalDisplayFence(t, db, tx, f)
		for _, relationAndSet := range []string{
			"coverage_paths SET name=name,locality_name=locality_name,broad_class=broad_class",
			"coverage_parks SET name=name,locality_name=locality_name,park_kind=park_kind",
			"path_segments SET tags=tags,highway=highway",
			"workout_park_attributions SET first_traversed_at=first_traversed_at,visit_date=visit_date",
			"workouts SET local_start_date=local_start_date,started_at=started_at",
		} {
			coverageDisplayExec(t, db, tx, `UPDATE app.`+relationAndSet+` WHERE account_id=$1`, f.account)
		}
		coverageDisplayExec(t, db, tx, `UPDATE app.coverage_region_catalog SET display_name=display_name
			WHERE region_id='display:synthetic' AND $1::uuid IS NOT NULL`, f.account)
		var unchanged bool
		if err := tx.QueryRow(ctx, `SELECT
			NOT EXISTS(SELECT 1 FROM app.coverage_display_invalidations WHERE account_id=$1 AND txid=txid_current())
			AND NOT EXISTS(SELECT 1 FROM app.coverage_display_dirty WHERE account_id=$1 AND transaction_id=txid_current())
			AND (SELECT generation=$2 FROM app.account_data_generations WHERE account_id=$1)
			AND (SELECT revision=$3 FROM app.coverage_display_revisions WHERE account_id=$1)`, f.account, warm.generation, warm.revision).Scan(&unchanged); err != nil || !unchanged {
			t.Fatalf("true no-op metadata/catalog writes queued invalidation or advanced versions: %t err=%v", unchanged, err)
		}
		coverageDisplayCommit(t, db, tx)
		g, r := canonicalDisplayVersions(t, db, f)
		if g != warm.generation || r != warm.revision || !reflect.DeepEqual(before, canonicalDisplayRows(t, db, f, warm)) {
			t.Fatal("committing no-op writes invalidated a warm capability")
		}
		tx = beginAccount(t, ctx, db.migration, f.account)
		canonicalDisplayFence(t, db, tx, f)
		coverageDisplayExec(t, db, tx, `UPDATE app.coverage_paths SET name='Synthetic Batch Walk'
			WHERE account_id=$1 AND logical_path_id=$2`, f.account, f.paths[0])
		coverageDisplayExec(t, db, tx, `UPDATE app.coverage_parks SET name='Synthetic Batch Park'
			WHERE account_id=$1 AND park_id=$2`, f.account, park)
		coverageDisplayExec(t, db, tx, `UPDATE app.coverage_region_catalog SET display_name=display_name||' Batch'
			WHERE region_id='display:synthetic' AND $1::uuid IS NOT NULL`, f.account)
		coverageDisplayExec(t, db, tx, `DELETE FROM app.workout_park_attributions WHERE account_id=$1 AND park_id=$2 AND workout_id=$3`, f.account, park, f.workouts[0])
		coverageDisplayExec(t, db, tx, `UPDATE app.workouts SET local_start_date='2026-01-11',started_at=started_at+interval '1 minute'
			WHERE account_id=$1 AND id=$2`, f.account, f.workouts[1])
		var queued bool
		if err := tx.QueryRow(ctx, `SELECT
			(SELECT count(*)=1 FROM app.coverage_display_invalidations WHERE account_id=$1 AND txid=txid_current())
			AND (SELECT generation=$2 FROM app.account_data_generations WHERE account_id=$1)
			AND (SELECT revision=$3 FROM app.coverage_display_revisions WHERE account_id=$1)`, f.account, g, r).Scan(&queued); err != nil || !queued {
			t.Fatalf("mixed metadata/attribution/date writes must enqueue once without early version changes: %t err=%v", queued, err)
		}
		coverageDisplayExec(t, db, tx, `SET CONSTRAINTS ALL IMMEDIATE`)
		if err := tx.QueryRow(ctx, `SELECT
			NOT EXISTS(SELECT 1 FROM app.coverage_display_invalidations WHERE account_id=$1 AND txid=txid_current())
			AND (SELECT generation=$2+1 FROM app.account_data_generations WHERE account_id=$1)
			AND (SELECT revision=$3+1 FROM app.coverage_display_revisions WHERE account_id=$1)`, f.account, g, r).Scan(&queued); err != nil || !queued {
			t.Fatalf("mixed transaction did not flush exactly once: %t err=%v", queued, err)
		}
		coverageDisplayExec(t, db, tx, `SET CONSTRAINTS ALL DEFERRED`)
		coverageDisplayExec(t, db, tx, `UPDATE app.coverage_paths SET broad_class='other'
			WHERE account_id=$1 AND logical_path_id=$2`, f.account, f.paths[0])
		coverageDisplayCommit(t, db, tx)
		ng, nr := canonicalDisplayVersions(t, db, f)
		if ng != g+1 || nr != r+1 {
			t.Fatalf("re-dirty after an explicit flush advanced versions twice: %d/%d -> %d/%d", g, r, ng, nr)
		}
		if len(canonicalDisplayRows(t, db, f, warm)) != 0 {
			t.Fatal("mixed changes left the old cached capability usable")
		}
		fresh := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", nil)
		coverageDisplayCheck(t, db, `SELECT count(*)=1 AND bool_and(name='Synthetic Batch Walk' AND broad_class='other'
			AND region_name LIKE '% Batch' AND range_workout_count=5 AND all_time_workout_count=6)
			FROM app.coverage_display_entities WHERE account_id=$1 AND context_id=$2
			AND canonical_id=$3`, f.account, fresh.context, canonicalDisplayID("display:synthetic", "synthetic batch walk", "synthetic city", ""))
	})

	t.Run("WarmReadyContextConcurrentRepeatableReadPrepareReusesWithout40001", func(t *testing.T) {
		f := newCanonicalDisplayFixture(t, db)
		warm := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", nil)
		var selections []canonicalDisplayScope
		for range 2 {
			tx := canonicalDisplayAPI(t, db, f)
			selections = append(selections, canonicalDisplayCapture(t, db, tx, f, "2026-01-10", "2026-01-12", nil))
			coverageDisplayCommit(t, db, tx)
		}
		a, b := canonicalDisplayAPI(t, db, f), canonicalDisplayAPI(t, db, f)
		defer a.Rollback(ctx)
		defer b.Rollback(ctx)
		for _, tx := range []pgx.Tx{a, b} {
			var visible int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.map_selections WHERE account_id=$1 AND id=ANY($2::uuid[])
				AND coverage_display_context_id IS NULL`, f.account, []uuid.UUID{selections[0].id, selections[1].id}).Scan(&visible); err != nil || visible != 2 {
				t.Fatalf("both RR snapshots must see both new, unbound selections and the already warm input: visible=%d err=%v", visible, err)
			}
		}
		canonicalDisplayPrepare(t, db, a, f, &selections[0])
		type preparation struct {
			context  uuid.UUID
			revision int64
			policy   string
			err      error
		}
		started, done := make(chan struct{}), make(chan preparation, 1)
		go func() {
			close(started)
			var result preparation
			result.err = b.QueryRow(ctx, `SELECT * FROM app.prepare_coverage_display_context($1,$2,$3,$4)`,
				f.account, f.session, selections[1].id, selections[1].generation).Scan(&result.context, &result.revision, &result.policy)
			done <- result
		}()
		<-started
		// B's snapshot is already old even if its query reaches PostgreSQL after
		// this commit. Mutating a shared ready context would incorrectly cause 40001.
		commitErr := a.Commit(ctx)
		if commitErr != nil {
			_ = a.Rollback(context.Background())
		}
		result := <-done
		if commitErr != nil || result.err != nil || result.context != warm.context || selections[0].context != warm.context ||
			result.revision != warm.revision || result.policy != "coverage-display-identity-v1" {
			t.Fatalf("warm cache prepare must succeed in both RR snapshots without retries: A=%+v Acommit=%v B=%+v warm=%+v", selections[0], commitErr, result, warm)
		}
		coverageDisplayCommit(t, db, b)
		g, r := canonicalDisplayVersions(t, db, f)
		if g != warm.generation || r != warm.revision {
			t.Fatal("cache reuse changed public generation or display revision")
		}
		coverageDisplayCheck(t, db, `SELECT
			(SELECT count(*)=1 AND bool_and(ready) FROM app.coverage_display_contexts WHERE account_id=$1)
			AND (SELECT count(*)=2 AND bool_and(coverage_display_context_id=$2) FROM app.map_selections
				WHERE account_id=$1 AND id=ANY($3::uuid[]))`, f.account, warm.context, []uuid.UUID{selections[0].id, selections[1].id})
	})

	for _, sourceFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("ReadCommittedBothCommitOrdersSourceFirst=%t", sourceFirst), func(t *testing.T) {
			f := newCanonicalDisplayFixture(t, db)
			old := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", nil)
			source := beginAccount(t, ctx, db.migration, f.account)
			match := beginAccount(t, ctx, db.migration, f.account)
			defer source.Rollback(ctx)
			defer match.Rollback(ctx)
			coverageDisplayExec(t, db, source, `UPDATE app.path_segments SET tags=tags||'{"workouts:park_kind":"state_park","workouts:park_name":"Synthetic Concurrent Reserve"}'::jsonb
				WHERE account_id=$1 AND physical_segment_id=$2`, f.account, f.segments[1])
			coverageDisplayInsertMatch(t, db, match, f, f.workouts[4], f.segments[1], 77)
			// Both writes have already completed: neither took an early generation lock.
			first, second := match, source
			if sourceFirst {
				first, second = source, match
			}
			coverageDisplayExec(t, db, first, `SET CONSTRAINTS ALL IMMEDIATE`)
			var blocker, waiter int
			if err := first.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blocker); err != nil {
				t.Fatal(err)
			}
			if err := second.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&waiter); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- second.Commit(ctx) }()
			joined := false
			defer func() {
				if !joined {
					_ = first.Rollback(context.Background())
					<-done
				}
			}()
			deferredWaitBlocked(t, db, waiter, blocker)
			coverageDisplayCommit(t, db, first)
			err := <-done
			joined = true
			if err != nil {
				t.Fatal(err)
			}
			g, r := canonicalDisplayVersions(t, db, f)
			if g != old.generation+2 || r != old.revision+2 {
				t.Fatalf("each writer must invalidate once: generation=%d revision=%d old=%+v", g, r, old)
			}
			s := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", nil)
			row := canonicalDisplayFind(t, canonicalDisplayRows(t, db, f, s), "Synthetic Concurrent Reserve")
			canonicalDisplayAssert(t, row, "range_workout_count", float64(2), "all_time_workout_count", float64(3))
			coverageDisplayCheck(t, db, `SELECT locality_name='Synthetic Concurrent Reserve' FROM app.workout_path_display_context
				WHERE account_id=$1 AND workout_id=$2 AND entity_id=$3`, f.account, f.workouts[4], f.paths[1])
		})
	}

	t.Run("RepeatableReadStaleWriter40001AtomicRollback", func(t *testing.T) {
		f := newCanonicalDisplayFixture(t, db)
		g, r := canonicalDisplayVersions(t, db, f)
		stale, err := db.migration.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
		if err != nil {
			t.Fatal(err)
		}
		defer stale.Rollback(ctx)
		coverageDisplayExec(t, db, stale, `SELECT set_config('app.account_id',$1,true)`, f.account.String())
		coverageDisplayExec(t, db, stale, `SELECT generation FROM app.account_data_generations WHERE account_id=$1`, f.account)
		coverageDisplayExec(t, db, stale, `UPDATE app.workout_segment_matches SET geom=ST_Translate(geom,0.01,0)
			WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0])
		bootstrapExec(t, db, f.bootstrapFixture, `UPDATE app.coverage_paths SET locality_name='Synthetic Winning City'
			WHERE account_id=$1 AND logical_path_id=$2`, f.account, f.paths[0])
		bootstrapSQLState(t, stale.Commit(ctx), "40001")
		ng, nr := canonicalDisplayVersions(t, db, f)
		if ng != g+1 || nr != r+1 {
			t.Fatal("stale transaction changed committed versions")
		}
		coverageDisplayCheck(t, db, `SELECT ST_Equals(match.geom,ST_Multi(segment.geom))
			FROM app.workout_segment_matches match JOIN app.path_segments segment
			USING(account_id,region_id,generation_id,physical_segment_id)
			WHERE match.account_id=$1 AND match.workout_id=$2 AND match.physical_segment_id=$3`, f.account, f.workouts[0], f.segments[0])
		s := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", nil)
		canonicalDisplayFind(t, canonicalDisplayRows(t, db, f, s), "Synthetic Winning City")
	})

	t.Run("PrepareRollbackAndSavepointNeverExposeHalfContext", func(t *testing.T) {
		f := newCanonicalDisplayFixture(t, db)
		for _, savepoint := range []bool{false, true} {
			tx := canonicalDisplayAPI(t, db, f)
			s := canonicalDisplayCapture(t, db, tx, f, "2026-01-11", "2026-01-12", nil)
			if savepoint {
				coverageDisplayExec(t, db, tx, `SAVEPOINT before_prepare`)
			}
			canonicalDisplayPrepare(t, db, tx, f, &s)
			coverageDisplayCheck(t, db, `SELECT NOT EXISTS(SELECT 1 FROM app.coverage_display_contexts WHERE account_id=$1 AND id=$2)`, f.account, s.context)
			if savepoint {
				coverageDisplayExec(t, db, tx, `ROLLBACK TO SAVEPOINT before_prepare`)
				coverageDisplayCommit(t, db, tx)
				coverageDisplayCheck(t, db, `SELECT coverage_display_context_id IS NULL FROM app.map_selections WHERE account_id=$1 AND id=$2`, f.account, s.id)
			} else if err := tx.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			coverageDisplayCheck(t, db, `SELECT NOT EXISTS(SELECT 1 FROM app.coverage_display_contexts WHERE account_id=$1 AND id=$2)
				AND NOT EXISTS(SELECT 1 FROM app.coverage_display_members WHERE account_id=$1 AND context_id=$2)
				AND NOT EXISTS(SELECT 1 FROM app.coverage_display_visits WHERE account_id=$1 AND context_id=$2)
				AND NOT EXISTS(SELECT 1 FROM app.coverage_display_entities WHERE account_id=$1 AND context_id=$2)`, f.account, s.context)
		}
	})

	t.Run("DirectBindingRejectsWrongContextScopeAndStaleFKCanNull", func(t *testing.T) {
		f, foreign := newCanonicalDisplayFixture(t, db), newCanonicalDisplayFixture(t, db)
		warm := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", f.workouts[:1])
		api := canonicalDisplayAPI(t, db, f)
		unbound := canonicalDisplayCapture(t, db, api, f, "2026-01-10", "2026-01-12", nil)
		coverageDisplayCommit(t, db, api)
		for _, invalid := range []struct {
			name, generation, revision, start, end string
			account                                uuid.UUID
		}{
			{"account", "generation", "revision", "start_date", "end_date", foreign.account},
			{"start_date", "generation", "revision", "start_date+1", "end_date", f.account},
			{"end_date", "generation", "revision", "start_date", "end_date+1", f.account},
			{"generation", "generation+1", "revision", "start_date", "end_date", f.account},
			{"revision", "generation", "revision+1", "start_date", "end_date", f.account},
			{"unready", "generation", "revision", "start_date", "end_date", f.account},
		} {
			tx := beginAccount(t, ctx, db.migration, f.account)
			bad := uuid.New()
			if invalid.name == "unready" {
				bad = warm.context
				coverageDisplayExec(t, db, tx, `UPDATE app.coverage_display_contexts SET ready=false WHERE account_id=$1 AND id=$2`, f.account, bad)
			} else {
				coverageDisplayExec(t, db, tx, `INSERT INTO app.coverage_display_contexts(account_id,id,generation,revision,start_date,end_date,ready)
					SELECT $3,$4,`+invalid.generation+`,`+invalid.revision+`,`+invalid.start+`,`+invalid.end+`,true
					FROM app.coverage_display_contexts WHERE account_id=$1 AND id=$2`, f.account, warm.context, invalid.account, bad)
			}
			_, err := tx.Exec(ctx, `UPDATE app.map_selections SET coverage_display_context_id=$3 WHERE account_id=$1 AND id=$2`, f.account, unbound.id, bad)
			_ = tx.Rollback(ctx)
			if err == nil {
				t.Fatalf("direct context binding accepted wrong %s", invalid.name)
			}
			bootstrapSQLState(t, err, "23514")
		}
		// A valid direct binding proves the failures came from the validator,
		// not from an unavailable UPDATE grant or an unrelated selection error.
		bootstrapExec(t, db, f.bootstrapFixture, `UPDATE app.map_selections SET coverage_display_context_id=$3
			WHERE account_id=$1 AND id=$2`, f.account, unbound.id, warm.context)
		coverageDisplayCheck(t, db, `SELECT coverage_display_context_id=$3 FROM app.map_selections
			WHERE account_id=$1 AND id=$2`, f.account, unbound.id, warm.context)
		canonicalDisplayWorkoutWrite(t, db, f, `UPDATE app.workouts SET local_start_date='2027-01-01'
			WHERE account_id=$1 AND id=$2`, f.account, f.workouts[0])
		tx := beginAccount(t, ctx, db.migration, f.account)
		_, err := tx.Exec(ctx, `UPDATE app.map_selections SET coverage_display_context_id=$3
			WHERE account_id=$1 AND id=$2`, f.account, unbound.id, warm.context)
		_ = tx.Rollback(ctx)
		bootstrapSQLState(t, err, "23514")
		// FK cleanup must still work after both input versions and the focused
		// workout's date become stale. Neither account nor focus may be nulled.
		bootstrapExec(t, db, f.bootstrapFixture, `DELETE FROM app.coverage_display_contexts WHERE account_id=$1 AND id=$2`, f.account, warm.context)
		coverageDisplayCheck(t, db, `SELECT count(*)=2 AND bool_and(account_id=$1 AND generation=$4 AND coverage_display_context_id IS NULL)
			AND bool_and(CASE WHEN id=$2 THEN focused_workout_id=$5 ELSE focused_workout_id IS NULL END)
			FROM app.map_selections WHERE account_id=$1 AND id IN($2,$3)`, f.account, warm.id, unbound.id, warm.generation, f.workouts[0])
		coverageDisplayCheck(t, db, `SELECT
			NOT EXISTS(SELECT 1 FROM app.coverage_display_members WHERE account_id=$1 AND context_id=$2)
			AND NOT EXISTS(SELECT 1 FROM app.coverage_display_visits WHERE account_id=$1 AND context_id=$2)
			AND NOT EXISTS(SELECT 1 FROM app.coverage_display_entities WHERE account_id=$1 AND context_id=$2)`, f.account, warm.context)
	})

	t.Run("AuthorizationExpiredForeignOwnerGenerationAndMissingReady", func(t *testing.T) {
		f, foreign := newCanonicalDisplayFixture(t, db), newCanonicalDisplayFixture(t, db)
		s := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", f.workouts[:1])
		other := canonicalDisplaySelection(t, db, foreign, "2026-01-10", "2026-01-12", nil)
		for _, denied := range []struct {
			account, session, selection uuid.UUID
			generation                  int64
		}{
			{foreign.account, foreign.session, other.id, other.generation},
			{f.account, foreign.session, s.id, s.generation},
			{f.account, f.session, other.id, s.generation},
			{f.account, f.session, s.id, s.generation + 1},
			{f.account, f.session, uuid.New(), s.generation},
		} {
			tx := canonicalDisplayAPI(t, db, f)
			var count int
			err := tx.QueryRow(ctx, `SELECT
				(SELECT count(*) FROM app.map_selection_coverage_entities($1,$2,$3,$4,NULL))+
				(SELECT count(*) FROM app.map_selection_coverage_entity_stats($1,$2,$3,$4,'path',$5))+
				(SELECT count(*) FROM app.map_selection_coverage_entity_detail($1,$2,$3,$4,'path',$5))+
				(SELECT count(*) FROM app.map_selection_coverage_entity_members($1,$2,$3,$4,'path',$5))`,
				denied.account, denied.session, denied.selection, denied.generation,
				canonicalDisplayID("display:synthetic", "synthetic walk", "synthetic city", "")).Scan(&count)
			_ = tx.Rollback(ctx)
			if err != nil || count != 0 {
				t.Fatalf("foreign capability leaked: count=%d err=%v", count, err)
			}
			tx = canonicalDisplayAPI(t, db, f)
			var focus []byte
			err = tx.QueryRow(ctx, `SELECT app.map_selection_coverage_focus($1,$2,$3,$4,$5)`, denied.account, denied.session,
				denied.selection, denied.generation, f.workouts[0]).Scan(&focus)
			_ = tx.Rollback(ctx)
			if err != nil || len(focus) != 0 {
				t.Fatalf("foreign focus leaked: %s err=%v", focus, err)
			}
		}
		for _, gate := range []struct{ apply, restore string }{
			{`UPDATE app.sessions SET expires_at=clock_timestamp()-interval '1 second',created_at=clock_timestamp()-interval '1 hour' WHERE id=$2 AND $1::uuid IS NOT NULL`,
				`UPDATE app.sessions SET expires_at=clock_timestamp()+interval '1 hour' WHERE id=$2 AND $1::uuid IS NOT NULL`},
			{`UPDATE app.authentication_principals SET disabled_at=clock_timestamp() WHERE id=(SELECT principal_id FROM app.users WHERE account_id=$1) AND $2::uuid IS NOT NULL`,
				`UPDATE app.authentication_principals SET disabled_at=NULL WHERE id=(SELECT principal_id FROM app.users WHERE account_id=$1) AND $2::uuid IS NOT NULL`},
		} {
			bootstrapExec(t, db, f.bootstrapFixture, gate.apply, f.account, f.session)
			if rows := canonicalDisplayRows(t, db, f, s); len(rows) != 0 {
				t.Fatalf("expired/disabled principal leaked: %v", rows)
			}
			bootstrapExec(t, db, f.bootstrapFixture, gate.restore, f.account, f.session)
		}
		tx := canonicalDisplayAPI(t, db, f)
		expired := canonicalDisplayScope{coverageDisplaySelection: coverageDisplaySelection{id: uuid.New(), generation: s.generation}}
		// Valid throughout capture's transaction_timestamp snapshot, expired in
		// every later transaction, without sleeps or immutable-selection updates.
		coverageDisplayExec(t, db, tx, `INSERT INTO app.map_selections(id,account_id,session_id,generation,expires_at,start_date,end_date,selection_kind)
			VALUES($1,$2,$3,$4,transaction_timestamp()+interval '1 microsecond','2026-01-10','2026-01-12','explicit_subset')`, expired.id, f.account, f.session, expired.generation)
		canonicalDisplayPrepare(t, db, tx, f, &expired)
		coverageDisplayCommit(t, db, tx)
		if rows := canonicalDisplayRows(t, db, f, expired); len(rows) != 0 {
			t.Fatalf("expired selection leaked canonical entities: %v", rows)
		}
		tx = canonicalDisplayAPI(t, db, f)
		missing := canonicalDisplayCapture(t, db, tx, f, "2026-01-11", "2026-01-12", nil)
		coverageDisplayCommit(t, db, tx)
		bootstrapExec(t, db, f.bootstrapFixture, `UPDATE app.coverage_display_contexts SET ready=false WHERE account_id=$1 AND id=$2`, f.account, s.context)
		for _, scope := range []canonicalDisplayScope{missing, s} {
			for _, reader := range []string{"map_selection_coverage_entities", "map_selection_coverage_entity_stats", "map_selection_coverage_entity_detail", "map_selection_coverage_entity_members"} {
				tx = canonicalDisplayAPI(t, db, f)
				arguments := []any{f.account, f.session, scope.id, scope.generation}
				suffix := ",NULL)"
				if reader != "map_selection_coverage_entities" {
					suffix = ",'path',$5)"
					arguments = append(arguments, canonicalDisplayID("display:synthetic", "synthetic walk", "synthetic city", ""))
				}
				_, err := tx.Exec(ctx, `SELECT * FROM app.`+reader+`($1,$2,$3,$4`+suffix, arguments...)
				_ = tx.Rollback(ctx)
				bootstrapSQLState(t, err, "55000")
			}
		}
	})

	t.Run("SoftGCPinsActiveScopesAndAccountCascade", func(t *testing.T) {
		f := newCanonicalDisplayFixture(t, db)
		var pinned []canonicalDisplayScope
		for day := 1; day <= 10; day++ {
			pinned = append(pinned, canonicalDisplaySelection(t, db, f, fmt.Sprintf("2026-01-%02d", day), "2026-01-12", nil))
		}
		coverageDisplayCheck(t, db, `SELECT count(*)=10 AND bool_and(ready) FROM app.coverage_display_contexts WHERE account_id=$1`, f.account)
		for _, s := range pinned {
			if len(canonicalDisplayRows(t, db, f, s)) == 0 {
				t.Fatal("soft cache limit evicted an active valid scope")
			}
		}
		bootstrapExec(t, db, f.bootstrapFixture, `DELETE FROM app.map_selections WHERE account_id=$1`, f.account)
		bootstrapExec(t, db, f.bootstrapFixture, `UPDATE app.coverage_display_contexts SET last_used_at=clock_timestamp()-interval '2 days' WHERE account_id=$1`, f.account)
		canonicalDisplaySelection(t, db, f, "2026-01-11", "2026-01-12", nil)
		coverageDisplayCheck(t, db, `SELECT count(*)=1 FROM app.coverage_display_contexts WHERE account_id=$1`, f.account)
		tx := beginAccount(t, ctx, db.migration, f.account)
		canonicalDisplayFence(t, db, tx, f)
		for _, relation := range []string{"workouts", "path_segments", "source_files", "workout_types"} {
			coverageDisplayExec(t, db, tx, `DELETE FROM app.`+relation+` WHERE account_id=$1`, f.account)
		}
		coverageDisplayExec(t, db, tx, `SET CONSTRAINTS app.ingest_write_capability_cleanup IMMEDIATE`)
		coverageDisplayExec(t, db, tx, `DELETE FROM app.jobs WHERE account_id=$1`, f.account)
		var principal uuid.UUID
		if err := tx.QueryRow(ctx, `DELETE FROM app.users WHERE account_id=$1 RETURNING principal_id`, f.account).Scan(&principal); err != nil {
			t.Fatal(err)
		}
		coverageDisplayExec(t, db, tx, `DELETE FROM app.authentication_principals WHERE id=$1`, principal)
		coverageDisplayExec(t, db, tx, `DELETE FROM app.accounts WHERE id=$1`, f.account)
		coverageDisplayCommit(t, db, tx)
		for _, relation := range []string{"coverage_display_revisions", "coverage_display_contexts", "coverage_display_members", "coverage_display_visits", "coverage_display_entities", "coverage_display_invalidations"} {
			coverageDisplayCheck(t, db, `SELECT NOT EXISTS(SELECT 1 FROM app.`+relation+` WHERE account_id=$1)`, f.account)
		}
	})
}

type canonicalDisplayScope struct {
	coverageDisplaySelection
	context  uuid.UUID
	revision int64
}

// Canonical grouping fixture complements the independent raw-projection fixture.
// Coordinates and names are invented; no retained workout or OSM input is read.
func newCanonicalDisplayFixture(t *testing.T, db testDatabases) coverageDisplayFixture {
	t.Helper()
	job := reprocessingJob(t, db, nil, uuid.Nil, uuid.Nil, "incremental")
	f := coverageDisplayFixture{bootstrapFixture: bootstrapFixture{account: job.account, lease: job.lease}, job: job.job, session: uuid.New()}
	principal := accountRequester(t, db, f.account)
	tx := beginAccount(t, db.ctx, db.migration, f.account)
	defer tx.Rollback(db.ctx)
	canonicalDisplayFence(t, db, tx, f)
	file, workoutType := uuid.New(), uuid.New()
	coverageDisplayExec(t, db, tx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes,state,processing_started_at)
		VALUES($1,$2,$3,$4,'synthetic-canonical-display.json',1,'processing',clock_timestamp())`, file, f.account, job.source, job.job)
	coverageDisplayExec(t, db, tx, `INSERT INTO app.workout_types(id,account_id,type_key,provider_label) VALUES($1,$2,'outdoor-run','Synthetic Run')`, workoutType, f.account)
	coverageDisplayExec(t, db, tx, `INSERT INTO app.coverage_region_catalog(region_id,display_name)
		VALUES('display:synthetic','Synthetic Region') ON CONFLICT(region_id) DO NOTHING`)
	dates := []string{"2026-01-10", "2026-01-10", "2026-01-10", "2026-01-12", "2026-01-12", "2026-01-01", "2026-02-01"}
	hours := []int{8, 8, 7, 9, 8, 8, 8}
	for i, date := range dates {
		workout := uuid.Must(uuid.NewV7())
		f.workouts = append(f.workouts, workout)
		coverageDisplayExec(t, db, tx, `INSERT INTO app.workouts(id,account_id,source_id,source_file_id,workout_type_id,
			provider_id,content_sha256,provider_label,local_start_date,started_at,ended_at,provider_duration)
			VALUES($2,$1,$3,$4,$5,$2::uuid::text,$6,'Synthetic Run',$7::date,
			($7::date+make_interval(hours=>$8)) AT TIME ZONE 'UTC',
			($7::date+make_interval(hours=>$8)+interval '1 hour') AT TIME ZONE 'UTC',3600)`,
			f.account, workout, job.source, file, workoutType, bootstrapDigest(), date, hours[i])
		coverageDisplayExec(t, db, tx, `INSERT INTO app.workout_route_points(account_id,workout_id,sequence,recorded_at,latitude,longitude)
			VALUES($1,$2,0,'2026-01-01T00:00:00Z',20,10),($1,$2,1,'2026-01-01T00:00:01Z',20.001,10.001)`, f.account, workout)
		coverageDisplayExec(t, db, tx, `INSERT INTO app.workout_routes(account_id,workout_id,point_count,minimum_longitude,minimum_latitude,
			maximum_longitude,maximum_latitude,has_complete_altitude,route) VALUES($1,$2,2,10,20,10.001,20.001,false,
			ST_GeomFromText('MULTILINESTRING((10 20,10.001 20.001))',4326))`, f.account, workout)
		coverageDisplayExec(t, db, tx, `INSERT INTO app.workout_coverage_states(account_id,workout_id,route_input_revision,route_input_sha256,
			readiness_state,map_data_ready_at,processing_state,applied_route_input_revision,applied_route_input_sha256,
			applied_rules_version,applied_sampling_version,applied_path_policy_version,applied_generations,processing_finished_at)
			VALUES($1,$2,1,$3,'map_data_ready',transaction_timestamp(),'current',1,$3,'coverage-experimental-v1',
			'coverage-sampling-experimental-v1','coverage-path-policy-experimental-v82',
			'[{"regionId":"display:synthetic","generation":1}]',transaction_timestamp())`, f.account, workout, bootstrapDigest())
	}
	canonicalDisplayAddPath(t, db, tx, &f, "Synthetic Walk", "Synthetic City", "footway", "{}", append(append([]uuid.UUID{}, f.workouts[:5]...), f.workouts[6]))
	canonicalDisplayAddPath(t, db, tx, &f, "Synthetic Walk", "Synthetic City", "cycleway", "{}", []uuid.UUID{f.workouts[0], f.workouts[5]})
	canonicalDisplayAddPath(t, db, tx, &f, "Synthetic Walk", "Synthetic County", "road", "{}", f.workouts[:1])
	coverageDisplayExec(t, db, tx, `INSERT INTO app.sessions(id,principal_id,credential_kind,credential_verifier,expires_at)
		VALUES($1,$2,'bearer',$3,transaction_timestamp()+interval '1 hour')`, f.session, principal, []byte(strings.ReplaceAll(f.session.String(), "-", "")))
	coverageDisplayCommit(t, db, tx)
	return f
}

func canonicalDisplayFence(t *testing.T, db testDatabases, tx pgx.Tx, f coverageDisplayFixture) {
	t.Helper()
	var source uuid.UUID
	if err := tx.QueryRow(db.ctx, `SELECT source_id FROM app.fence_ingest_job($1,$2,$3,$4)`, f.job, reprocessingWorker, f.lease, database.SupportedSchemaVersion).Scan(&source); err != nil || source == uuid.Nil {
		t.Fatalf("current-runtime synthetic ingest fence: source=%s err=%v", source, err)
	}
}

func canonicalDisplayWorkoutWrite(t *testing.T, db testDatabases, f coverageDisplayFixture, query string, args ...any) {
	t.Helper()
	tx := beginAccount(t, db.ctx, db.migration, f.account)
	defer tx.Rollback(db.ctx)
	canonicalDisplayFence(t, db, tx, f)
	coverageDisplayExec(t, db, tx, query, args...)
	coverageDisplayCommit(t, db, tx)
}

func canonicalDisplayAddPath(t *testing.T, db testDatabases, tx pgx.Tx, f *coverageDisplayFixture, name any, locality, class, tags string, workouts []uuid.UUID) {
	t.Helper()
	path, segment := uuid.New(), uuid.New()
	i := len(f.paths)
	f.paths, f.segments = append(f.paths, path), append(f.segments, segment)
	coverageDisplayExec(t, db, tx, `INSERT INTO app.coverage_paths(account_id,logical_path_id,locality_relation_id,locality_relation_version,
		locality_name,name,normalized_name,broad_class) VALUES($1,$2,$3,1,$4,$5,
		CASE WHEN $5::text IS NOT NULL THEN 'deliberately not the display key' END,$6)`, f.account, path, int64(33000+i), locality, name, class)
	x, y := 10+float64(i)*0.002, 20+float64(i)*0.002
	line := fmt.Sprintf("LINESTRING(%.6f %.6f,%.6f %.6f)", x, y, x+0.001, y+0.001)
	coverageDisplayExec(t, db, tx, `INSERT INTO app.path_segments(account_id,region_id,generation_id,physical_segment_id,logical_path_id,
		derivation_version,locality_relation_id,source_way_id,source_way_version,name,normalized_name,highway,broad_class,tags,segment_meters,geom)
		VALUES($1,'display:synthetic',1,$2,$3,4,$4,$4,1,$5,
		CASE WHEN $5::text IS NOT NULL THEN 'deliberately not the display key' END,'footway',$6,$7::jsonb,30,ST_GeomFromText($8,4326))`,
		f.account, segment, path, int64(33000+i), name, class, tags, line)
	for order, workout := range workouts {
		coverageDisplayInsertMatch(t, db, tx, *f, workout, segment, order)
	}
}

func canonicalDisplayPark(t *testing.T, db testDatabases, f coverageDisplayFixture) uuid.UUID {
	t.Helper()
	park := uuid.New()
	tx := beginAccount(t, db.ctx, db.migration, f.account)
	defer tx.Rollback(db.ctx)
	tags, _ := json.Marshal(map[string]string{"workouts:park_id": park.String(), "workouts:park_kind": "local_park",
		"workouts:park_name": "Synthetic Walk", "workouts:park_normalized_name": "synthetic walk"})
	coverageDisplayExec(t, db, tx, `UPDATE app.path_segments SET tags=$3::jsonb WHERE account_id=$1 AND physical_segment_id=ANY($2::uuid[])`, f.account, f.segments[1:3], string(tags))
	coverageDisplayExec(t, db, tx, `INSERT INTO app.coverage_parks(account_id,park_id,name,normalized_name,park_kind,
		locality_relation_id,locality_relation_version,locality_name)
		VALUES($1,$2,'Synthetic Walk','synthetic walk','local_park',33001,1,'Synthetic City')`, f.account, park)
	coverageDisplayExec(t, db, tx, `INSERT INTO app.workout_park_attributions(account_id,workout_id,park_id,first_traversed_at,visit_date)
		SELECT match.account_id,match.workout_id,$3,min(match.first_traversed_at),workout.local_start_date
		FROM app.workout_segment_matches match JOIN app.workouts workout
		ON workout.account_id=match.account_id AND workout.id=match.workout_id
		WHERE match.account_id=$1 AND match.physical_segment_id=ANY($2::uuid[])
		GROUP BY match.account_id,match.workout_id,workout.local_start_date`, f.account, f.segments[1:3], park)
	coverageDisplayCommit(t, db, tx)
	return park
}

func canonicalDisplayAPI(t *testing.T, db testDatabases, f coverageDisplayFixture) pgx.Tx {
	t.Helper()
	return deferredBeginSelection(t, db, f.account, f.session)
}

func canonicalDisplayCapture(t *testing.T, db testDatabases, tx pgx.Tx, f coverageDisplayFixture, start, end string, checked []uuid.UUID) canonicalDisplayScope {
	t.Helper()
	s := canonicalDisplayScope{coverageDisplaySelection: coverageDisplaySelection{id: uuid.New()}}
	if err := tx.QueryRow(db.ctx, `SELECT app.lock_account_data_generation()`).Scan(&s.generation); err != nil {
		t.Fatal(err)
	}
	var focus any
	if len(checked) > 0 {
		focus = checked[0]
	}
	coverageDisplayExec(t, db, tx, `INSERT INTO app.map_selections(id,account_id,session_id,generation,expires_at,start_date,end_date,selection_kind,focused_workout_id)
		VALUES($1,$2,$3,$4,transaction_timestamp()+interval '30 minutes',$5::date,$6::date,'explicit_subset',$7)`, s.id, f.account, f.session, s.generation, start, end, focus)
	for order, workout := range checked {
		coverageDisplayExec(t, db, tx, `INSERT INTO app.map_selection_workouts(account_id,selection_id,workout_id,sort_order) VALUES($1,$2,$3,$4)`, f.account, s.id, workout, order)
	}
	return s
}

func canonicalDisplayPrepare(t *testing.T, db testDatabases, tx pgx.Tx, f coverageDisplayFixture, s *canonicalDisplayScope) {
	t.Helper()
	var policy string
	if err := tx.QueryRow(db.ctx, `SELECT * FROM app.prepare_coverage_display_context($1,$2,$3,$4)`, f.account, f.session, s.id, s.generation).
		Scan(&s.context, &s.revision, &policy); err != nil || s.context == uuid.Nil || s.revision < 1 || policy != "coverage-display-identity-v1" {
		t.Fatalf("API prepare must bind a ready policy-v1 context: scope=%+v policy=%s err=%v", s, policy, err)
	}
}

func canonicalDisplaySelection(t *testing.T, db testDatabases, f coverageDisplayFixture, start, end string, checked []uuid.UUID) canonicalDisplayScope {
	t.Helper()
	tx := canonicalDisplayAPI(t, db, f)
	s := canonicalDisplayCapture(t, db, tx, f, start, end, checked)
	canonicalDisplayPrepare(t, db, tx, f, &s)
	coverageDisplayCommit(t, db, tx)
	coverageDisplayCheck(t, db, `SELECT context.ready AND context.revision=$4 AND selection.coverage_display_context_id=context.id
		FROM app.coverage_display_contexts context JOIN app.map_selections selection ON selection.account_id=context.account_id
		WHERE context.account_id=$1 AND context.id=$2 AND selection.id=$3`, f.account, s.context, s.id, s.revision)
	return s
}

func canonicalDisplayRows(t *testing.T, db testDatabases, f coverageDisplayFixture, s canonicalDisplayScope) []map[string]any {
	t.Helper()
	tx := canonicalDisplayAPI(t, db, f)
	defer tx.Rollback(db.ctx)
	var encoded []byte
	if err := tx.QueryRow(db.ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(entity) ORDER BY entity_id),'[]'::jsonb)
		FROM app.map_selection_coverage_entities($1,$2,$3,$4,NULL) entity`, f.account, f.session, s.id, s.generation).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(encoded, &rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func canonicalDisplayFind(t *testing.T, rows []map[string]any, locality string) map[string]any {
	t.Helper()
	var result map[string]any
	for _, row := range rows {
		if row["locality_name"] == locality {
			if result != nil {
				t.Fatalf("duplicate visible location %q: %s", locality, rows)
			}
			result = row
		}
	}
	if result == nil {
		t.Fatalf("missing visible location %q: %s", locality, rows)
	}
	return result
}

func canonicalDisplayAssert(t *testing.T, row map[string]any, pairs ...any) {
	t.Helper()
	for i := 0; i < len(pairs); i += 2 {
		key := pairs[i].(string)
		if row[key] != pairs[i+1] {
			t.Errorf("%s=%v want=%v: %v", key, row[key], pairs[i+1], row)
		}
	}
}

// Independent implementation of the approved tuple encoding, not a SQL helper.
func canonicalDisplayID(region, name, locality, discriminator string) uuid.UUID {
	var payload []byte
	for _, component := range []string{"workouts-explorer/coverage-display", "coverage-display-identity-v1", region, name, locality, discriminator} {
		payload = binary.BigEndian.AppendUint32(payload, uint32(len(component)))
		payload = append(payload, component...)
	}
	digest := sha256.Sum256(payload)
	var id uuid.UUID
	copy(id[:], digest[:16])
	id[6], id[8] = id[6]&15|128, id[8]&63|128
	return id
}

func canonicalDisplayVersions(t *testing.T, db testDatabases, f coverageDisplayFixture) (int64, int64) {
	t.Helper()
	tx := beginAccount(t, db.ctx, db.migration, f.account)
	defer tx.Rollback(db.ctx)
	var generation, revision int64
	if err := tx.QueryRow(db.ctx, `SELECT generation,revision FROM app.account_data_generations JOIN app.coverage_display_revisions USING(account_id) WHERE account_id=$1`, f.account).
		Scan(&generation, &revision); err != nil {
		t.Fatal(err)
	}
	return generation, revision
}

func canonicalDisplayGeometry(t *testing.T, db testDatabases, f coverageDisplayFixture, s canonicalDisplayScope, kind string, id uuid.UUID, segments []uuid.UUID) {
	t.Helper()
	tx := canonicalDisplayAPI(t, db, f)
	defer tx.Rollback(db.ctx)
	var geometry []byte
	var width, height float64
	if err := tx.QueryRow(db.ctx, `SELECT geometry,
		ST_XMax(fit)-ST_XMin(fit),ST_YMax(fit)-ST_YMin(fit)
		FROM app.map_selection_coverage_entity_detail($1,$2,$3,$4,$5,$6) detail
		CROSS JOIN LATERAL (SELECT ST_Transform(ST_MakeEnvelope(fit_minimum_longitude,fit_minimum_latitude,
		fit_maximum_longitude,fit_maximum_latitude,4326),3857) fit) projected`, f.account, f.session, s.id, s.generation, kind, id).
		Scan(&geometry, &width, &height); err != nil || width < 499 || height < 499 {
		t.Fatalf("detail fit must be at least 500m: width=%g height=%g err=%v", width, height, err)
	}
	// Raw matched pieces are the oracle, not canonical members, partial bounds,
	// source segment geometry, or a previous source-ID detail implementation.
	coverageDisplayCheck(t, db, `WITH expected AS (
		SELECT ST_UnaryUnion(ST_Collect(match.geom)) geom FROM app.workout_segment_matches match
		JOIN app.workouts workout ON workout.account_id=match.account_id AND workout.id=match.workout_id
		WHERE match.account_id=$1 AND match.physical_segment_id=ANY($2::uuid[])
		AND workout.local_start_date BETWEEN DATE '2026-01-10' AND DATE '2026-01-12'
	) SELECT ST_Equals(ST_GeomFromGeoJSON($3::text),geom) AND ST_NumGeometries(ST_GeomFromGeoJSON($3::text))=cardinality($2::uuid[])
		AND NOT ST_Intersects(ST_GeomFromGeoJSON($3::text),ST_SetSRID(ST_MakePoint(10.0015,20.0015),4326)) FROM expected`, f.account, segments, string(geometry))
}

func canonicalDisplaySurfaces(t *testing.T, db testDatabases, f coverageDisplayFixture, s canonicalDisplayScope, row map[string]any) {
	t.Helper()
	id, kind := uuid.MustParse(row["entity_id"].(string)), row["entity_kind"].(string)
	tx, err := db.api.BeginTx(db.ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(db.ctx)
	coverageDisplayExec(t, db, tx, `SELECT set_config('app.account_id',$1,true),set_config('app.session_id',$2,true)`, f.account.String(), f.session.String())
	coverageDisplayExec(t, db, tx, `SET LOCAL jit=off`)
	var stats, detail []byte
	if err := tx.QueryRow(db.ctx, `SELECT to_jsonb(stats) FROM app.map_selection_coverage_entity_stats($1,$2,$3,$4,$5,$6) stats`, f.account, f.session, s.id, s.generation, kind, id).Scan(&stats); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(db.ctx, `SELECT to_jsonb(detail)-'geometry'-'fit_minimum_longitude'-'fit_minimum_latitude'-'fit_maximum_longitude'-'fit_maximum_latitude'
		FROM app.map_selection_coverage_entity_detail($1,$2,$3,$4,$5,$6) detail`, f.account, f.session, s.id, s.generation, kind, id).Scan(&detail); err != nil {
		t.Fatal(err)
	}
	for _, encoded := range [][]byte{stats, detail} {
		var metadata map[string]any
		if err := json.Unmarshal(encoded, &metadata); err != nil || !reflect.DeepEqual(metadata, row) {
			t.Fatalf("list/stat/detail 21-field canonical parity: got=%s want=%v err=%v", encoded, row, err)
		}
	}
	// Search is applied after grouping: neither matching count nor ID changes.
	var searched []byte
	if err := tx.QueryRow(db.ctx, `SELECT to_jsonb(entity) FROM app.map_selection_coverage_entities($1,$2,$3,$4,$5) entity WHERE entity_id=$6`,
		f.account, f.session, s.id, s.generation, row["name"], id).Scan(&searched); err != nil || string(searched) != string(stats) {
		t.Fatalf("search changed grouped metadata: %s stats=%s err=%v", searched, stats, err)
	}
	var fitted bool
	if err := tx.QueryRow(db.ctx, `SELECT ST_XMax(fit)-ST_XMin(fit)>=499.99 AND ST_YMax(fit)-ST_YMin(fit)>=499.99
		FROM app.map_selection_coverage_entity_detail($1,$2,$3,$4,$5,$6) detail
		CROSS JOIN LATERAL (SELECT ST_Transform(ST_MakeEnvelope(fit_minimum_longitude,fit_minimum_latitude,
		fit_maximum_longitude,fit_maximum_latitude,4326),3857) fit) projected`, f.account, f.session, s.id, s.generation, kind, id).Scan(&fitted); err != nil || !fitted {
		t.Fatalf("both Web Mercator fit axes must be at least 500m: %t err=%v", fitted, err)
	}
}

// Compare decoded MVT commands to PostGIS encodings of independent raw pieces.
// No generated vector_tile package or additional module dependency is needed.
func canonicalDisplayMapSurfaces(t *testing.T, db testDatabases, f coverageDisplayFixture, s canonicalDisplayScope, allLayers bool) {
	t.Helper()
	listed := canonicalDisplayRows(t, db, f, s)
	metadata := make(map[string]map[string]any)
	for _, row := range listed {
		metadata[strings.ToUpper(strings.ReplaceAll(row["entity_id"].(string), "-", ""))] = row
	}
	api := canonicalDisplayAPI(t, db, f)
	defer api.Rollback(db.ctx)
	members, err := api.Query(db.ctx, `SELECT upper(replace(entity.entity_id::text,'-','')),member.source_kind,
		upper(replace(member.source_id::text,'-','')) FROM app.map_selection_coverage_entities($1,$2,$3,$4,NULL) entity
		CROSS JOIN LATERAL app.map_selection_coverage_entity_members($1,$2,$3,$4,entity.entity_kind,entity.entity_id) member`,
		f.account, f.session, s.id, s.generation)
	if err != nil {
		t.Fatal(err)
	}
	canonicalForSource := make(map[string]string)
	for members.Next() {
		var canonical, kind, id string
		if err := members.Scan(&canonical, &kind, &id); err != nil {
			t.Fatal(err)
		}
		canonicalForSource[kind+"/"+id] = canonical
	}
	members.Close()
	if err := members.Err(); err != nil {
		t.Fatal(err)
	}
	var focused uuid.UUID
	if err := api.QueryRow(db.ctx, `SELECT focused_workout_id FROM app.map_selections WHERE account_id=$1 AND id=$2`, f.account, s.id).Scan(&focused); err != nil {
		t.Fatal(err)
	}
	var focus []byte
	if err := api.QueryRow(db.ctx, `SELECT app.map_selection_coverage_focus($1,$2,$3,$4,$5)`, f.account, f.session, s.id, s.generation, focused).Scan(&focus); err != nil {
		t.Fatal(err)
	}
	var collection struct {
		Features []struct {
			Properties map[string]any  `json:"properties"`
			Geometry   json.RawMessage `json:"geometry"`
		} `json:"features"`
	}
	if err := json.Unmarshal(focus, &collection); err != nil || len(collection.Features) == 0 {
		t.Fatalf("missing focused geometry: %s err=%v", focus, err)
	}
	oracle := beginAccount(t, db.ctx, db.migration, f.account)
	defer oracle.Rollback(db.ctx)
	// Only the requested workout's raw pieces enter this geometry oracle. In
	// particular it cannot accidentally include the unchecked context driveway.
	rows, err := oracle.Query(db.ctx, `WITH raw AS MATERIALIZED (
		SELECT match.*,segment.tags,segment.highway,path.name FROM app.workout_segment_matches match
		JOIN app.path_segments segment USING(account_id,region_id,generation_id,physical_segment_id,logical_path_id)
		JOIN app.coverage_paths path USING(account_id,logical_path_id)
		WHERE match.account_id=$1 AND match.workout_id=$2
	), pieces AS (
		SELECT 'path' source_kind,COALESCE(NULLIF(tags->>'workouts:education_id','')::uuid,logical_path_id) source_id,geom FROM raw
		WHERE NOT(highway='service' AND COALESCE(tags->>'service','') IN('driveway','parking_aisle'))
		AND tags->>'amenity' IS DISTINCT FROM 'parking'
		AND(name IS NOT NULL OR tags->>'workouts:park_kind' IN('state_park','national_park') OR NOT(tags ? 'workouts:park_id'))
		UNION ALL SELECT 'park',(tags->>'workouts:park_id')::uuid,geom FROM raw WHERE tags ? 'workouts:park_id'
	) SELECT source_kind,upper(replace(source_id::text,'-','')),ST_AsGeoJSON(ST_UnaryUnion(ST_Collect(geom)))
	FROM pieces GROUP BY source_kind,source_id`, f.account, focused)
	if err != nil {
		t.Fatal(err)
	}
	expected := make(map[string]string)
	for rows.Next() {
		var kind, id, geometry string
		if err := rows.Scan(&kind, &id, &geometry); err != nil {
			t.Fatal(err)
		}
		expected[kind+"/"+id] = geometry
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	checkMetadata := func(properties map[string]any) string {
		t.Helper()
		id, _ := properties["entityId"].(string)
		row, ok := metadata[id]
		if !ok {
			t.Fatalf("map returned a source alias or unknown canonical entity: %v", properties)
		}
		count := uint64(row["range_workout_count"].(float64))
		bucket := uint64(1)
		switch {
		case count >= 26:
			bucket = 6
		case count >= 11:
			bucket = 5
		case count >= 6:
			bucket = 4
		case count >= 3:
			bucket = 3
		case count == 2:
			bucket = 2
		}
		for _, pair := range [][2]string{{"entityKind", "entity_kind"}, {"broadClass", "broad_class"}, {"name", "name"}, {"localityName", "locality_name"}, {"regionId", "region_id"}} {
			if properties[pair[0]] != row[pair[1]] {
				t.Fatalf("map/list metadata mismatch %s: properties=%v row=%v", pair[0], properties, row)
			}
		}
		for key, want := range map[string]uint64{"rangeWorkoutCount": count, "countBucket": bucket} {
			actual := properties[key]
			if actual != want && actual != float64(want) {
				t.Fatalf("map %s=%v want=%d", key, actual, want)
			}
		}
		sourceKind, _ := properties["sourceEntityKind"].(string)
		sourceID, _ := properties["sourceEntityId"].(string)
		key := sourceKind + "/" + sourceID
		if _, ok := expected[key]; !ok {
			t.Fatalf("missing/foreign source provenance: %v", properties)
		}
		if id != canonicalForSource[key] {
			t.Fatalf("map assigned source to another canonical group: %v want=%s", properties, canonicalForSource[key])
		}
		return key
	}
	seen := make(map[string]bool)
	for _, feature := range collection.Features {
		key := checkMetadata(feature.Properties)
		var equal bool
		if err := oracle.QueryRow(db.ctx, `SELECT ST_Equals(ST_GeomFromGeoJSON($1::text),ST_GeomFromGeoJSON($2::text))`, string(feature.Geometry), expected[key]).Scan(&equal); err != nil || !equal || seen[key] {
			t.Fatalf("focus invented/lost/duplicated source geometry %s: equal=%t err=%v", key, equal, err)
		}
		seen[key] = true
	}
	if len(seen) != len(expected) {
		t.Fatalf("focus omitted members: got=%d want=%d", len(seen), len(expected))
	}
	// Use the existing migrator's inherited security-owner execution privilege;
	// the API has no tile-function grant. Do not invent a tile-role membership or
	// require another URL. Martin's four-part capability needs no account GUC.
	tiles := beginAccount(t, db.ctx, db.migration, uuid.Nil)
	defer tiles.Rollback(db.ctx)
	for _, z := range []int{12, 13, 15} {
		x := int(math.Floor((10.001 + 180) / 360 * math.Exp2(float64(z))))
		y := int(math.Floor((1 - math.Asinh(math.Tan(20.001*math.Pi/180))/math.Pi) / 2 * math.Exp2(float64(z))))
		var tile []byte
		if err := tiles.QueryRow(db.ctx, `SELECT app.coverage_mvt($5,$6,$7,json_build_object('target_account_id',$1::text,
			'target_session_id',$2::text,'target_selection_id',$3::text,'target_generation',$4::bigint::text))`, f.account, f.session, s.id, s.generation, z, x, y).Scan(&tile); err != nil {
			t.Fatal(err)
		}
		layers := decodeCoverageMVT(t, tile)
		// Focus is independently fetched as exact GeoJSON, not unioned into tiles.
		for _, layer := range []string{"coverage_focus", "coverage_parks_focus"} {
			if len(layers[layer]) != 0 {
				t.Fatalf("aggregate-only tile unexpectedly contains %s", layer)
			}
		}
		for _, layer := range []string{"coverage", "coverage_parks"} {
			if allLayers && len(layers[layer]) == 0 {
				t.Fatalf("missing %s in cross-kind tile at zoom %d", layer, z)
			}
			seen := make(map[string]bool)
			for _, feature := range layers[layer] {
				key := checkMetadata(feature.properties)
				parkLayer := strings.Contains(layer, "parks")
				if parkLayer != strings.HasPrefix(key, "park/") || seen[key] {
					t.Fatalf("wrong source layer/duplicate: %s %s", layer, key)
				}
				var encoded []byte
				if err := oracle.QueryRow(db.ctx, `WITH projected AS (
					SELECT ST_Transform(d.geom,3857) geom FROM ST_Dump(ST_GeomFromGeoJSON($1::text)) d
				), display AS (
					SELECT CASE WHEN $2>=15 THEN geom ELSE ST_SimplifyPreserveTopology(geom,40075016.68557849/power(2.0,$2)/2048.0) END geom
					FROM projected
				) SELECT ST_AsMVT(rows,'expected',4096,'geometry') FROM (
					SELECT ST_AsMVTGeom(ST_UnaryUnion(ST_Collect(geom)),ST_TileEnvelope($2,$3,$4)::box2d,4096,64,true) geometry FROM display) rows`, expected[key], z, x, y).Scan(&encoded); err != nil {
					t.Fatal(err)
				}
				geometry := decodeCoverageMVT(t, encoded)["expected"]
				if len(geometry) != 1 || feature.geometry != geometry[0].geometry {
					t.Fatalf("tile geometry differs from independent matched pieces: layer=%s source=%s got=%s want=%v", layer, key, feature.geometry, geometry)
				}
				seen[key] = true
			}
			want := 0
			for key, geojson := range expected {
				if strings.Contains(layer, "parks") == strings.HasPrefix(key, "park/") {
					var visible bool
					if err := oracle.QueryRow(db.ctx, `SELECT ST_AsMVTGeom(ST_Transform(ST_GeomFromGeoJSON($1::text),3857),
						ST_TileEnvelope($2,$3,$4)::box2d,4096,64,true) IS NOT NULL`, geojson, z, x, y).Scan(&visible); err != nil {
						t.Fatal(err)
					}
					if visible {
						want++
					}
				}
			}
			if len(seen) != want {
				t.Fatalf("tile omitted source geometry layer=%s got=%d want=%d", layer, len(seen), want)
			}
		}
		var panned []byte
		if err := tiles.QueryRow(db.ctx, `SELECT app.coverage_mvt($5,$6,$7,json_build_object('target_account_id',$1::text,
			'target_session_id',$2::text,'target_selection_id',$3::text,'target_generation',$4::bigint::text))`,
			f.account, f.session, s.id, s.generation, z, x+100, y).Scan(&panned); err != nil || len(panned) != 0 {
			t.Fatalf("panning away must render no geometry: bytes=%d err=%v", len(panned), err)
		}
	}
	if !reflect.DeepEqual(listed, canonicalDisplayRows(t, db, f, s)) {
		t.Fatal("tile viewport/zoom changed canonical metadata, identities or counts")
	}
}
