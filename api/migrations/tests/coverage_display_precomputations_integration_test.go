package migrations_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

type coverageDisplayFixture struct {
	bootstrapFixture
	session  uuid.UUID
	job      uuid.UUID
	campus   uuid.UUID
	paths    []uuid.UUID
	segments []uuid.UUID
	parks    []uuid.UUID
}

type coverageDisplaySelection struct {
	id         uuid.UUID
	generation int64
}

// Provisioning belongs to the integration harness. Never fall back to an
// application database without the explicit disposable opt-in, even to read it.
func TestCoverageDisplayPrecomputationsPostgreSQL(t *testing.T) {
	if os.Getenv("COVERAGE_DISPLAY_PRECOMPUTATIONS_TEST_DISPOSABLE") != "1" ||
		os.Getenv("API_DATABASE_URL") == "" || os.Getenv("MIGRATION_DATABASE_URL") == "" || os.Getenv("WORKER_DATABASE_URL") == "" {
		t.Skip("COVERAGE_DISPLAY_PRECOMPUTATIONS_TEST_DISPOSABLE=1 and disposable API_DATABASE_URL/WORKER_DATABASE_URL/MIGRATION_DATABASE_URL are required")
	}
	var endpoint string
	for _, name := range []string{"API_DATABASE_URL", "WORKER_DATABASE_URL", "MIGRATION_DATABASE_URL"} {
		cfg, err := pgx.ParseConfig(os.Getenv(name))
		if err != nil {
			t.Fatal("invalid disposable database configuration: " + name)
		}
		if (cfg.Host != "127.0.0.1" && cfg.Host != "localhost" && cfg.Host != "::1") ||
			(!strings.Contains(cfg.Database, "_test") && !strings.HasPrefix(cfg.Database, "test_")) {
			t.Fatal("coverage display tests require a named loopback test database")
		}
		for _, fallback := range cfg.Fallbacks {
			if fallback.Host != cfg.Host || fallback.Port != cfg.Port {
				t.Fatal("coverage display tests prohibit alternate database endpoints")
			}
		}
		target := fmt.Sprintf("%s:%d/%s", cfg.Host, cfg.Port, cfg.Database)
		if endpoint != "" && endpoint != target {
			t.Fatal("runtime and migration test URLs must address the same disposable database")
		}
		endpoint = target
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	db := testDatabases{ctx: ctx,
		api:       openPool(t, ctx, os.Getenv("API_DATABASE_URL"), 2),
		worker:    openPool(t, ctx, os.Getenv("WORKER_DATABASE_URL"), 2),
		migration: openPool(t, ctx, os.Getenv("MIGRATION_DATABASE_URL"), 4)}
	t.Cleanup(cancel)
	assertRuntimeRole(t, ctx, db.api, "workouts_api")
	assertRuntimeRole(t, ctx, db.worker, "workouts_worker")
	assertRuntimeRole(t, ctx, db.migration, "workouts_migration")
	coverageDisplayMigrate32(t, ctx)
	coverageDisplayCheck(t, db, `SELECT to_regclass('app.workout_path_display_partials') IS NOT NULL
		AND to_regclass('app.coverage_display_dirty') IS NOT NULL`)

	t.Run("CommittedFullRowsEducationDedupContextAndIndependentParks", func(t *testing.T) {
		f := newCoverageDisplayFixture(t, db)
		s := coverageDisplayParity(t, db, f)
		coverageDisplayEntityCheck(t, db, f, s, f.campus, `entity_kind='path' AND broad_class='other'
			AND name='Fixture School' AND region_id='display:alpha' AND region_name='Alpha Fixture Region'
			AND range_workout_count=5 AND all_time_workout_count=7
			AND range_first_workout_id=$6 AND range_latest_workout_id=$7
			AND all_time_first_workout_id=$8 AND all_time_latest_workout_id=$9
			AND range_first_date=DATE '2026-01-10' AND range_latest_date=DATE '2026-01-12'
			AND minimum_longitude=1 AND minimum_latitude=20
			AND maximum_longitude=12.25 AND maximum_latitude=31.5`,
			f.workouts[2], f.workouts[4], f.workouts[5], f.workouts[6])
		coverageDisplayEntityCheck(t, db, f, s, f.paths[3], `name='Regional Walk'
			AND locality_name='Excluded Regional Context' AND minimum_longitude=4
			AND maximum_longitude=4.25 AND minimum_latitude=23 AND maximum_latitude=23.5`)
		coverageDisplayEntityCheck(t, db, f, s, f.paths[4], `broad_class='other' AND name='Empty Key Campus'`)
		for _, park := range []uuid.UUID{f.parks[0], f.parks[2]} {
			coverageDisplayEntityCheck(t, db, f, s, park, `entity_kind='park' AND range_workout_count=1
				AND region_id IS NULL AND region_name IS NULL AND minimum_longitude IS NOT NULL`)
		}
		coverageDisplayEntityCheck(t, db, f, s, f.parks[0], `range_first_workout_id=$6
			AND minimum_longitude=3 AND maximum_longitude=12.25`, f.workouts[1])
		coverageDisplayCheck(t, db, `SELECT NOT EXISTS (
			SELECT 1 FROM app.map_selection_coverage_entities($1,$2,$3,$4,NULL)
			WHERE entity_id IN ($5,$6,$7))`, f.account, f.session, s.id, s.generation,
			f.parks[3], f.parks[4], f.paths[2])
	})

	t.Run("LiveSourcePathNameQualificationAndMetadata", func(t *testing.T) {
		f := newCoverageDisplayFixture(t, db)
		before := coverageDisplayFacts(t, db, f)
		bootstrapExec(t, db, f.bootstrapFixture, `UPDATE app.coverage_paths SET name='New Local Walk',
			normalized_name='new local walk' WHERE account_id=$1 AND logical_path_id=$2`, f.account, f.paths[2])
		s := coverageDisplayParity(t, db, f)
		coverageDisplayEntityCheck(t, db, f, s, f.paths[2], `name='New Local Walk' AND range_workout_count=1`)
		bootstrapExec(t, db, f.bootstrapFixture, `UPDATE app.coverage_paths SET name=NULL,normalized_name=NULL
			WHERE account_id=$1 AND logical_path_id IN ($2,$3)`, f.account, f.paths[0], f.paths[2])
		s = coverageDisplayParity(t, db, f)
		coverageDisplayEntityCheck(t, db, f, s, f.campus, `range_workout_count=5 AND maximum_longitude=2.25`)
		bootstrapExec(t, db, f.bootstrapFixture, `UPDATE app.coverage_paths SET name='Changed Road',
			normalized_name='changed road',broad_class='cycleway',locality_name='Changed Town'
			WHERE account_id=$1 AND logical_path_id=$2`, f.account, f.paths[3])
		s = coverageDisplayParity(t, db, f)
		coverageDisplayEntityCheck(t, db, f, s, f.paths[3], `name='Changed Road' AND broad_class='cycleway'
			AND locality_name='Excluded Regional Context'`)
		bootstrapExec(t, db, f.bootstrapFixture, `UPDATE app.coverage_paths SET locality_name='Changed Town'
			WHERE account_id=$1 AND logical_path_id=$2`, f.account, f.paths[4])
		s = coverageDisplayParity(t, db, f)
		coverageDisplayEntityCheck(t, db, f, s, f.paths[4], `locality_name='Changed Town'`)
		bootstrapExec(t, db, f.bootstrapFixture, `UPDATE app.coverage_parks SET name='Renamed Park',
			normalized_name='renamed park',locality_name='Changed Town' WHERE account_id=$1 AND park_id=$2`, f.account, f.parks[0])
		s = coverageDisplayParity(t, db, f)
		coverageDisplayEntityCheck(t, db, f, s, f.parks[0], `name='Renamed Park' AND locality_name='Changed Town'`)
		if after := coverageDisplayFacts(t, db, f); after != before {
			t.Fatal("live path metadata rebuilt or changed evidence-only facts")
		}
	})

	t.Run("LiveDatesAscendingStartAndUUIDTies", func(t *testing.T) {
		f := newCoverageDisplayFixture(t, db)
		before := coverageDisplayFacts(t, db, f)
		coverageDisplayWorkoutExec(t, db, f, `UPDATE app.workouts SET started_at='2026-01-12T08:00:00Z'
			WHERE account_id=$1 AND id=$2`, f.account, f.workouts[3])
		s := coverageDisplayParity(t, db, f)
		coverageDisplayEntityCheck(t, db, f, s, f.campus, `range_first_workout_id=$6 AND range_latest_workout_id=$7`,
			f.workouts[2], f.workouts[3])
		coverageDisplayWorkoutExec(t, db, f, `UPDATE app.workouts SET local_start_date='2026-01-11'
			WHERE account_id=$1 AND id IN ($2,$3)`, f.account, f.workouts[2], f.workouts[4])
		s = coverageDisplayParity(t, db, f)
		coverageDisplayEntityCheck(t, db, f, s, f.campus, `range_first_workout_id=$6 AND range_latest_workout_id=$7
			AND range_workout_count=5`, f.workouts[0], f.workouts[3])
		coverageDisplayWorkoutExec(t, db, f, `UPDATE app.workouts SET
			local_start_date=CASE WHEN id=$2 THEN DATE '2026-01-10' ELSE DATE '2026-01-12' END,
			started_at=CASE WHEN id=$2 THEN TIMESTAMPTZ '2026-01-10T08:00:00Z' ELSE TIMESTAMPTZ '2026-01-12T08:00:00Z' END,
			ended_at=CASE WHEN id=$2 THEN TIMESTAMPTZ '2026-01-10T09:00:00Z' ELSE TIMESTAMPTZ '2026-01-12T09:00:00Z' END
			WHERE account_id=$1 AND id IN ($2,$3)`, f.account, f.workouts[5], f.workouts[6])
		s = coverageDisplayParity(t, db, f)
		coverageDisplayEntityCheck(t, db, f, s, f.campus, `range_workout_count=7 AND all_time_workout_count=7
			AND range_first_workout_id=$6 AND all_time_first_workout_id=$6
			AND range_latest_workout_id=$7 AND all_time_latest_workout_id=$7`, f.workouts[0], f.workouts[3])
		coverageDisplayWorkoutExec(t, db, f, `UPDATE app.workouts SET local_start_date='2025-12-31'
			WHERE account_id=$1 AND id=$2`, f.account, f.workouts[0])
		s = coverageDisplayParity(t, db, f)
		coverageDisplayEntityCheck(t, db, f, s, f.campus, `range_workout_count=6 AND all_time_workout_count=7
			AND range_first_workout_id=$6 AND all_time_first_workout_id=$7`, f.workouts[1], f.workouts[0])
		if after := coverageDisplayFacts(t, db, f); after != before {
			t.Fatal("workout dates were snapshotted into display facts")
		}
	})

	t.Run("LiveRouteNULLDeletionAndStaleAppliedEligibility", func(t *testing.T) {
		f := newCoverageDisplayFixture(t, db)
		before := coverageDisplayFacts(t, db, f)
		bootstrapExec(t, db, f.bootstrapFixture, `UPDATE app.workout_coverage_states SET processing_state='stale',
			route_input_revision=2 WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0])
		s := coverageDisplayParity(t, db, f)
		coverageDisplayEntityCheck(t, db, f, s, f.campus, `range_workout_count=5 AND all_time_workout_count=7`)
		bootstrapExec(t, db, f.bootstrapFixture, `UPDATE app.workout_routes SET route=NULL
			WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[1])
		s = coverageDisplayParity(t, db, f)
		coverageDisplayEntityCheck(t, db, f, s, f.campus, `range_workout_count=4 AND all_time_workout_count=6`)
		requester := accountRequester(t, db, f.account)
		tx := beginAccount(t, ctx, db.api, f.account)
		coverageDisplayExec(t, db, tx, `SELECT * FROM app.enqueue_workout_deletion($1,$2)`, f.workouts[2], requester)
		coverageDisplayCommit(t, db, tx)
		s = coverageDisplayParity(t, db, f)
		coverageDisplayEntityCheck(t, db, f, s, f.campus, `range_workout_count=3 AND all_time_workout_count=5`)
		bootstrapExec(t, db, f.bootstrapFixture, `DELETE FROM app.workout_coverage_states
			WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[3])
		s = coverageDisplayParity(t, db, f)
		coverageDisplayEntityCheck(t, db, f, s, f.campus, `range_workout_count=2 AND all_time_workout_count=4`)
		bootstrapExec(t, db, f.bootstrapFixture, `UPDATE app.workout_coverage_states SET
			applied_route_input_revision=NULL,applied_route_input_sha256=NULL,applied_rules_version=NULL,
			applied_sampling_version=NULL,applied_path_policy_version=NULL,applied_generations=NULL,
			processing_state='not_started' WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[4])
		s = coverageDisplayParity(t, db, f)
		coverageDisplayEntityCheck(t, db, f, s, f.campus, `range_workout_count=1 AND all_time_workout_count=3`)
		coverageDisplayWorkoutExec(t, db, f, `UPDATE app.workouts SET local_start_date=NULL
			WHERE account_id=$1 AND id=$2`, f.account, f.workouts[6])
		s = coverageDisplayParity(t, db, f)
		coverageDisplayEntityCheck(t, db, f, s, f.campus, `range_workout_count=1 AND all_time_workout_count=2`)
		if after := coverageDisplayFacts(t, db, f); after != before {
			t.Fatal("eligibility-only changes altered evidence facts")
		}
	})

	t.Run("DeferredOncePerWorkoutAndExplicitFlushRedirty", func(t *testing.T) {
		f := newCoverageDisplayFixture(t, db)
		before := coverageDisplayFacts(t, db, f)
		tx := beginAccount(t, ctx, db.migration, f.account)
		var initialInserts int64
		if err := tx.QueryRow(ctx, `SELECT n_tup_ins FROM pg_stat_xact_user_tables
			WHERE relid='app.workout_path_display_partials'::regclass`).Scan(&initialInserts); err != nil {
			t.Fatal(err)
		}
		for range 3 {
			coverageDisplayExec(t, db, tx, `UPDATE app.workout_segment_matches SET geom=ST_Translate(geom,0.125,0)
				WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0])
		}
		var deduplicated bool
		if err := tx.QueryRow(ctx, `SELECT count(*) FILTER (WHERE scope='account')=1
			AND count(*) FILTER (WHERE scope='workout')=1 AND count(*)=2
			FROM app.coverage_display_dirty WHERE account_id=$1 AND transaction_id=txid_current()`, f.account).Scan(&deduplicated); err != nil || !deduplicated {
			t.Fatalf("dirty markers were not deduplicated per workout/transaction: %t err=%v", deduplicated, err)
		}
		var inserts int64
		if err := tx.QueryRow(ctx, `SELECT n_tup_ins FROM pg_stat_xact_user_tables
			WHERE relid='app.workout_path_display_partials'::regclass`).Scan(&inserts); err != nil || inserts != initialInserts {
			t.Fatalf("display facts were maintained eagerly: insert delta=%d err=%v", inserts-initialInserts, err)
		}
		if after := coverageDisplayFacts(t, db, f); after != before {
			t.Fatal("uncommitted evidence changed committed facts")
		}
		coverageDisplayExec(t, db, tx, `SET CONSTRAINTS app.coverage_display_dirty_account_flush IMMEDIATE`)
		var once bool
		if err := tx.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM app.coverage_display_dirty WHERE account_id=$1)
			AND (SELECT n_tup_ins-$3::bigint FROM pg_stat_xact_user_tables WHERE relid='app.workout_path_display_partials'::regclass)
			= (SELECT count(*) FROM app.workout_path_display_partials WHERE account_id=$1 AND workout_id=$2)`,
			f.account, f.workouts[0], initialInserts).Scan(&once); err != nil || !once {
			t.Fatalf("constraint flush rebuilt a workout more than once or left markers: %t err=%v", once, err)
		}
		coverageDisplayExec(t, db, tx, `SET CONSTRAINTS app.coverage_display_dirty_account_flush DEFERRED`)
		coverageDisplayExec(t, db, tx, `UPDATE app.workout_segment_matches SET geom=ST_Translate(geom,0.125,0)
			WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0])
		coverageDisplayExec(t, db, tx, `SELECT app.flush_coverage_display_precomputations($1)`, f.account)
		var twice bool
		if err := tx.QueryRow(ctx, `SELECT NOT EXISTS (SELECT 1 FROM app.coverage_display_dirty WHERE account_id=$1)
			AND (SELECT n_tup_ins-$3::bigint FROM pg_stat_xact_user_tables WHERE relid='app.workout_path_display_partials'::regclass)
			= 2*(SELECT count(*) FROM app.workout_path_display_partials WHERE account_id=$1 AND workout_id=$2)`,
			f.account, f.workouts[0], initialInserts).Scan(&twice); err != nil || !twice {
			t.Fatalf("explicit re-dirty/flush did not rebuild exactly once: %t err=%v", twice, err)
		}
		coverageDisplayExec(t, db, tx, `SELECT app.flush_coverage_display_precomputations($1)`, f.account)
		if err := tx.QueryRow(ctx, `SELECT n_tup_ins FROM pg_stat_xact_user_tables
			WHERE relid='app.workout_path_display_partials'::regclass`).Scan(&inserts); err != nil {
			t.Fatal(err)
		}
		var partials int64
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.workout_path_display_partials
			WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0]).Scan(&partials); err != nil || inserts-initialInserts != 2*partials {
			t.Fatalf("empty flush was not a no-op: insert delta=%d partials=%d err=%v", inserts-initialInserts, partials, err)
		}
		coverageDisplayExec(t, db, tx, `UPDATE app.workout_segment_matches SET geom=ST_Translate(geom,0.125,0)
			WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0])
		coverageDisplayCommit(t, db, tx)
		s := coverageDisplayParity(t, db, f)
		coverageDisplayEntityCheck(t, db, f, s, f.campus, `maximum_longitude=12.875`)
		coverageDisplayEntityCheck(t, db, f, s, f.paths[3], `minimum_longitude=4.625 AND maximum_longitude=4.875`)
	})

	t.Run("SourceGeometryTagsHighwayAndTextKeyCanonicalization", func(t *testing.T) {
		f := newCoverageDisplayFixture(t, db)
		before := coverageDisplayFacts(t, db, f)
		tx := beginAccount(t, ctx, db.migration, f.account)
		coverageDisplayExec(t, db, tx, `UPDATE app.path_segments SET geom=ST_Translate(geom,0.5,0)
			WHERE account_id=$1`, f.account)
		var clean bool
		if err := tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM app.coverage_display_dirty WHERE account_id=$1)`, f.account).Scan(&clean); err != nil || !clean {
			t.Fatalf("source-only geometry update unnecessarily invalidated match bounds: %t err=%v", clean, err)
		}
		coverageDisplayCommit(t, db, tx)
		coverageDisplayParity(t, db, f)
		if after := coverageDisplayFacts(t, db, f); after != before {
			t.Fatal("bounds used source geometry instead of covered match geometry")
		}
		bootstrapExec(t, db, f.bootstrapFixture, `UPDATE app.path_segments SET highway='service',
			tags=tags || '{"service":"driveway"}'::jsonb WHERE account_id=$1 AND physical_segment_id=$2`, f.account, f.segments[0])
		s := coverageDisplayParity(t, db, f)
		coverageDisplayEntityCheck(t, db, f, s, f.campus, `range_workout_count=1 AND all_time_workout_count=1
			AND minimum_longitude=2`)
		bootstrapExec(t, db, f.bootstrapFixture, `UPDATE app.path_segments SET tags=jsonb_set(tags,
			'{workouts:park_name}','"Updated Regional Context"') WHERE account_id=$1 AND physical_segment_id=$2`, f.account, f.segments[4])
		s = coverageDisplayParity(t, db, f)
		coverageDisplayEntityCheck(t, db, f, s, f.paths[3], `locality_name='Updated Regional Context'`)
		for _, key := range []string{f.parks[2].String(), strings.ReplaceAll(f.parks[2].String(), "-", "")} {
			bootstrapExec(t, db, f.bootstrapFixture, `UPDATE app.path_segments SET tags=jsonb_set(tags,
				'{workouts:park_id}',to_jsonb($3::text)) WHERE account_id=$1 AND physical_segment_id=$2`, f.account, f.segments[7], key)
			s = coverageDisplayParity(t, db, f)
			predicate := `region_id IS NULL AND region_name IS NULL`
			if key == f.parks[2].String() {
				predicate = `region_id='display:alpha'`
			}
			coverageDisplayEntityCheck(t, db, f, s, f.parks[2], predicate+` AND minimum_longitude=8 AND maximum_longitude=8.25`)
		}
	})

	t.Run("MatchKeyMoveSourceDeletionAndPruning", func(t *testing.T) {
		f := newCoverageDisplayFixture(t, db)
		replacement := uuid.New()
		tx := beginAccount(t, ctx, db.migration, f.account)
		coverageDisplayExec(t, db, tx, `INSERT INTO app.path_segments(account_id,region_id,generation_id,
			physical_segment_id,logical_path_id,derivation_version,locality_relation_id,source_way_id,source_way_version,
			name,normalized_name,highway,broad_class,tags,segment_meters,geom)
			SELECT account_id,'display:beta',2,$3,logical_path_id,derivation_version,locality_relation_id,
			source_way_id,source_way_version,name,normalized_name,highway,broad_class,tags,segment_meters,geom
			FROM app.path_segments WHERE account_id=$1 AND physical_segment_id=$2`, f.account, f.segments[1], replacement)
		coverageDisplayExec(t, db, tx, `UPDATE app.workout_segment_matches SET physical_segment_id=$3,
			region_id='display:beta',generation_id=2 WHERE account_id=$1 AND physical_segment_id=$2`, f.account, f.segments[1], replacement)
		coverageDisplayExec(t, db, tx, `DELETE FROM app.path_segments WHERE account_id=$1 AND physical_segment_id IN ($2,$3)`,
			f.account, f.segments[1], f.segments[12])
		coverageDisplayCommit(t, db, tx)
		s := coverageDisplayParity(t, db, f)
		coverageDisplayEntityCheck(t, db, f, s, f.campus, `region_id='display:beta' AND range_workout_count=5`)
		bootstrapExec(t, db, f.bootstrapFixture, `DELETE FROM app.workout_segment_matches
			WHERE account_id=$1 AND logical_path_id=$2`, f.account, f.paths[3])
		coverageDisplayParity(t, db, f)
		coverageDisplayCheck(t, db, `SELECT NOT EXISTS(SELECT 1 FROM app.path_segments WHERE account_id=$1 AND logical_path_id=$2)
			AND NOT EXISTS(SELECT 1 FROM app.workout_path_display_partials WHERE account_id=$1 AND entity_id=$2)
			AND NOT EXISTS(SELECT 1 FROM app.workout_path_display_context WHERE account_id=$1 AND entity_id=$2)
			AND NOT EXISTS(SELECT 1 FROM app.coverage_paths WHERE account_id=$1 AND logical_path_id=$2)`, f.account, f.paths[3])
	})

	t.Run("RollbackSourceChangesAndWorkoutPurge", func(t *testing.T) {
		f := newCoverageDisplayFixture(t, db)
		before := coverageDisplayFacts(t, db, f)
		tx := beginAccount(t, ctx, db.migration, f.account)
		coverageDisplayFence(t, db, tx, f)
		coverageDisplayExec(t, db, tx, `UPDATE app.path_segments SET tags=tags || '{"amenity":"parking"}'
			WHERE account_id=$1`, f.account)
		coverageDisplayExec(t, db, tx, `SELECT app.flush_coverage_display_precomputations($1)`, f.account)
		coverageDisplayExec(t, db, tx, `DELETE FROM app.workouts WHERE account_id=$1 AND id=$2`, f.account, f.workouts[0])
		coverageDisplayExec(t, db, tx, `SELECT app.flush_coverage_display_precomputations($1)`, f.account)
		if err := tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		if after := coverageDisplayFacts(t, db, f); after != before {
			t.Fatal("rolled-back rebuild/purge changed committed facts")
		}
		coverageDisplayParity(t, db, f)
		coverageDisplayWorkoutExec(t, db, f, `DELETE FROM app.workouts WHERE account_id=$1 AND id=$2`, f.account, f.workouts[0])
		coverageDisplayParity(t, db, f)
		coverageDisplayCheck(t, db, `SELECT NOT EXISTS(SELECT 1 FROM app.workout_path_display_partials WHERE account_id=$1 AND workout_id=$2)
			AND NOT EXISTS(SELECT 1 FROM app.workout_path_display_context WHERE account_id=$1 AND workout_id=$2)
			AND NOT EXISTS(SELECT 1 FROM app.workout_park_display_bounds WHERE account_id=$1 AND workout_id=$2)`, f.account, f.workouts[0])
		tx = beginAccount(t, ctx, db.migration, f.account)
		coverageDisplayFence(t, db, tx, f)
		coverageDisplayExec(t, db, tx, `DELETE FROM app.workouts WHERE account_id=$1`, f.account)
		coverageDisplayExec(t, db, tx, `SELECT app.flush_coverage_display_precomputations($1)`, f.account)
		if err := tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		coverageDisplayParity(t, db, f)
		coverageDisplayWorkoutExec(t, db, f, `DELETE FROM app.workouts WHERE account_id=$1`, f.account)
		coverageDisplayParity(t, db, f)
		coverageDisplayCheck(t, db, `SELECT NOT EXISTS(SELECT 1 FROM app.workout_path_display_partials WHERE account_id=$1)
			AND NOT EXISTS(SELECT 1 FROM app.workout_path_display_context WHERE account_id=$1)
			AND NOT EXISTS(SELECT 1 FROM app.workout_park_display_bounds WHERE account_id=$1)
			AND NOT EXISTS(SELECT 1 FROM app.coverage_display_dirty WHERE account_id=$1)`, f.account)
	})

	t.Run("RepeatableReadStaleCommitRollsBackEvidenceAndProjections", func(t *testing.T) {
		f := newCoverageDisplayFixture(t, db)
		coverageDisplayParity(t, db, f)
		stale, err := db.migration.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
		if err != nil {
			t.Fatal(err)
		}
		defer stale.Rollback(context.Background())
		coverageDisplayExec(t, db, stale, `SELECT set_config('app.account_id',$1,true)`, f.account.String())
		var generation int64
		var version string
		if err := stale.QueryRow(ctx, `SELECT generation,xmin::text FROM app.account_data_generations
			WHERE account_id=$1`, f.account).Scan(&generation, &version); err != nil {
			t.Fatal(err)
		}
		coverageDisplayExec(t, db, stale, `UPDATE app.workout_segment_matches SET geom=ST_Translate(geom,0.5,0)
			WHERE account_id=$1 AND workout_id=$2 AND physical_segment_id=$3`, f.account, f.workouts[0], f.segments[3])
		// Evidence-only maintenance must change the barrier's row version even
		// though it deliberately preserves the public generation value.
		bootstrapExec(t, db, f.bootstrapFixture, `UPDATE app.path_segments SET tags=jsonb_set(tags,
			'{workouts:education_name}','"Committed School"') WHERE account_id=$1 AND physical_segment_id=$2`, f.account, f.segments[0])
		coverageDisplayCheck(t, db, `SELECT generation=$2 AND xmin::text<>$3
			FROM app.account_data_generations WHERE account_id=$1`, f.account, generation, version)
		winning := coverageDisplayFacts(t, db, f)
		var oldSnapshot bool
		if err := stale.QueryRow(ctx, `SELECT generation=$2 AND xmin::text=$3
			AND (SELECT education_name='Fixture School' FROM app.workout_path_display_partials
				WHERE account_id=$1 AND workout_id=$4 AND logical_path_id=$5 AND qualifies_without_name)
			AND (SELECT count(*)=2 FROM app.coverage_display_dirty
				WHERE account_id=$1 AND transaction_id=txid_current())
			FROM app.account_data_generations WHERE account_id=$1`,
			f.account, generation, version, f.workouts[6], f.paths[0]).Scan(&oldSnapshot); err != nil || !oldSnapshot {
			t.Fatalf("stale writer did not retain its old snapshot and pending markers: %t err=%v", oldSnapshot, err)
		}
		err = stale.Commit(ctx)
		bootstrapSQLState(t, err, "40001")
		pgError, ok := err.(*pgconn.PgError)
		if !ok || !strings.Contains(pgError.Where, "app.account_data_generations") ||
			!strings.Contains(pgError.Where, "flush_coverage_display_precomputations") {
			t.Fatalf("serialization failure did not originate at the display generation barrier: %v", err)
		}
		if after := coverageDisplayFacts(t, db, f); after != winning {
			t.Fatal("stale commit overwrote or rolled back the winning projections")
		}
		coverageDisplayCheck(t, db, `SELECT ST_XMin(geom)=4 FROM app.workout_segment_matches
			WHERE account_id=$1 AND workout_id=$2 AND physical_segment_id=$3`, f.account, f.workouts[0], f.segments[3])
		s := coverageDisplayParity(t, db, f)
		coverageDisplayEntityCheck(t, db, f, s, f.campus, `name='Committed School' AND range_workout_count=5 AND all_time_workout_count=7`)
		coverageDisplayEntityCheck(t, db, f, s, f.paths[3], `minimum_longitude=4 AND maximum_longitude=4.25`)
	})

	t.Run("SavepointRollbackRestoresFlushMarkersAndRedirty", func(t *testing.T) {
		f := newCoverageDisplayFixture(t, db)
		before := coverageDisplayFacts(t, db, f)
		tx := beginAccount(t, ctx, db.migration, f.account)
		assertState := func(dirty int, matchMinimum, partialMinimum float64) {
			t.Helper()
			var valid bool
			if err := tx.QueryRow(ctx, `SELECT
				(SELECT count(*)=$4 FROM app.coverage_display_dirty WHERE account_id=$1 AND transaction_id=txid_current())
				AND (SELECT ST_XMin(geom)=$5 FROM app.workout_segment_matches
					WHERE account_id=$1 AND workout_id=$2 AND physical_segment_id=$3)
				AND (SELECT minimum_longitude=$6 FROM app.workout_path_display_partials
					WHERE account_id=$1 AND workout_id=$2 AND entity_id=$7)`,
				f.account, f.workouts[0], f.segments[3], dirty, matchMinimum, partialMinimum, f.paths[3]).Scan(&valid); err != nil || !valid {
				t.Fatalf("savepoint state dirty=%d match=%g partial=%g: %t err=%v", dirty, matchMinimum, partialMinimum, valid, err)
			}
		}
		translate := func(delta float64) {
			t.Helper()
			coverageDisplayExec(t, db, tx, `UPDATE app.workout_segment_matches SET geom=ST_Translate(geom,$3,0)
				WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0], delta)
		}
		translate(0.125)
		assertState(2, 4.125, 4)
		coverageDisplayExec(t, db, tx, `SAVEPOINT pending_display`)
		coverageDisplayExec(t, db, tx, `SELECT app.flush_coverage_display_precomputations($1)`, f.account)
		assertState(0, 4.125, 4.125)
		translate(0.25)
		assertState(2, 4.375, 4.125)
		coverageDisplayExec(t, db, tx, `ROLLBACK TO SAVEPOINT pending_display`)
		assertState(2, 4.125, 4)
		coverageDisplayExec(t, db, tx, `RELEASE SAVEPOINT pending_display`)
		translate(0.25)
		assertState(2, 4.375, 4)
		coverageDisplayExec(t, db, tx, `SELECT app.flush_coverage_display_precomputations($1)`, f.account)
		assertState(0, 4.375, 4.375)
		coverageDisplayExec(t, db, tx, `SAVEPOINT flushed_display`)
		translate(0.5)
		coverageDisplayExec(t, db, tx, `SELECT app.flush_coverage_display_precomputations($1)`, f.account)
		assertState(0, 4.875, 4.875)
		translate(0.5)
		assertState(2, 5.375, 4.875)
		coverageDisplayExec(t, db, tx, `ROLLBACK TO SAVEPOINT flushed_display`)
		assertState(0, 4.375, 4.375)
		coverageDisplayExec(t, db, tx, `RELEASE SAVEPOINT flushed_display`)
		translate(0.125)
		assertState(2, 4.5, 4.375)
		if after := coverageDisplayFacts(t, db, f); after != before {
			t.Fatal("savepoint-local flushes escaped the uncommitted transaction")
		}
		coverageDisplayCommit(t, db, tx)
		s := coverageDisplayParity(t, db, f)
		coverageDisplayEntityCheck(t, db, f, s, f.campus, `maximum_longitude=12.75`)
		coverageDisplayEntityCheck(t, db, f, s, f.paths[3], `minimum_longitude=4.5 AND maximum_longitude=4.75`)
		coverageDisplayEntityCheck(t, db, f, s, f.parks[0], `minimum_longitude=3.5 AND maximum_longitude=12.75`)
	})

	t.Run("AccountCascadeDrainsDeletedAccountTombstones", func(t *testing.T) {
		f := newCoverageDisplayFixture(t, db)
		coverageDisplayParity(t, db, f)
		bootstrapExec(t, db, f.bootstrapFixture, `INSERT INTO app.notifications(id,account_id,type,severity,
			condition_key,subject_type,title,message) VALUES($2,$1,'coverage-display-fixture','info',
			'coverage-display-fixture','account','Synthetic account notification','Synthetic account cascade fixture.')`, f.account, uuid.New())
		tx := beginAccount(t, ctx, db.migration, f.account)
		coverageDisplayFence(t, db, tx, f)
		// Domain references restrict source/type deletion. Consume the real fence
		// before deleting its job; display maintenance intentionally stays deferred.
		coverageDisplayExec(t, db, tx, `DELETE FROM app.workouts WHERE account_id=$1`, f.account)
		coverageDisplayExec(t, db, tx, `DELETE FROM app.path_segments WHERE account_id=$1`, f.account)
		coverageDisplayExec(t, db, tx, `DELETE FROM app.source_files WHERE account_id=$1`, f.account)
		coverageDisplayExec(t, db, tx, `DELETE FROM app.workout_types WHERE account_id=$1`, f.account)
		coverageDisplayExec(t, db, tx, `SET CONSTRAINTS app.ingest_write_capability_cleanup IMMEDIATE`)
		coverageDisplayExec(t, db, tx, `DELETE FROM app.jobs WHERE account_id=$1`, f.account)
		var principal uuid.UUID
		if err := tx.QueryRow(ctx, `DELETE FROM app.users WHERE account_id=$1 RETURNING principal_id`, f.account).Scan(&principal); err != nil {
			t.Fatal(err)
		}
		coverageDisplayExec(t, db, tx, `DELETE FROM app.authentication_principals WHERE id=$1`, principal)
		coverageDisplayExec(t, db, tx, `DELETE FROM app.accounts WHERE id=$1`, f.account)
		var tombstones bool
		if err := tx.QueryRow(ctx, `SELECT
			NOT EXISTS(SELECT 1 FROM app.accounts WHERE id=$1)
			AND NOT EXISTS(SELECT 1 FROM app.account_data_generations WHERE account_id=$1)
			AND NOT EXISTS(SELECT 1 FROM app.sources WHERE account_id=$1)
			AND NOT EXISTS(SELECT 1 FROM app.notifications WHERE account_id=$1)
			AND EXISTS(SELECT 1 FROM app.coverage_display_dirty WHERE account_id=$1 AND scope='account')
			AND EXISTS(SELECT 1 FROM app.coverage_display_dirty WHERE account_id=$1 AND scope='workout')
			AND EXISTS(SELECT 1 FROM app.coverage_display_dirty WHERE account_id=$1 AND scope='segment')`, f.account).Scan(&tombstones); err != nil || !tombstones {
			t.Fatalf("account cascade did not preserve deferred tombstones after removing their sources: %t err=%v", tombstones, err)
		}
		coverageDisplayCommit(t, db, tx)
		coverageDisplayCheck(t, db, `SELECT NOT EXISTS(SELECT 1 FROM app.coverage_display_dirty WHERE account_id=$1)
			AND NOT EXISTS(SELECT 1 FROM app.workout_path_display_partials WHERE account_id=$1)
			AND NOT EXISTS(SELECT 1 FROM app.workout_path_display_context WHERE account_id=$1)
			AND NOT EXISTS(SELECT 1 FROM app.workout_park_display_bounds WHERE account_id=$1)
			AND NOT EXISTS(SELECT 1 FROM app.notifications WHERE account_id=$1)
			AND NOT EXISTS(SELECT 1 FROM app.accounts WHERE id=$1)`, f.account)
	})

	for _, sourceFirst := range []bool{true, false} {
		name := "ConcurrentSourceKeyMatchFirst"
		if sourceFirst {
			name = "ConcurrentSourceKeySourceFirst"
		}
		t.Run(name, func(t *testing.T) {
			f := newCoverageDisplayFixture(t, db)
			bootstrapExec(t, db, f.bootstrapFixture, `DELETE FROM app.workout_segment_matches
				WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[6])
			coverageDisplayParity(t, db, f)
			source := beginAccount(t, ctx, db.migration, f.account)
			match := beginAccount(t, ctx, db.migration, f.account)
			coverageDisplayExec(t, db, source, `UPDATE app.path_segments SET tags=jsonb_set(tags,
				'{workouts:education_name}','"Concurrent School"') WHERE account_id=$1 AND physical_segment_id=$2`, f.account, f.segments[0])
			coverageDisplayInsertMatch(t, db, match, f, f.workouts[6], f.segments[0], 0)
			first, second := match, source
			if sourceFirst {
				first, second = source, match
			}
			// Hold the real account barrier before starting the other commit. The
			// waiter must expand source keys/rebuild using a snapshot AFTER this wait.
			coverageDisplayExec(t, db, first, `SELECT app.flush_coverage_display_precomputations($1)`, f.account)
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
				t.Fatalf("waiting commit failed: %v", err)
			}
			s := coverageDisplayParity(t, db, f)
			coverageDisplayEntityCheck(t, db, f, s, f.campus, `name='Concurrent School'
				AND range_workout_count=5 AND all_time_workout_count=7`)
			coverageDisplayCheck(t, db, `SELECT education_name='Concurrent School'
				FROM app.workout_path_display_partials WHERE account_id=$1 AND workout_id=$2
				AND logical_path_id=$3 AND qualifies_without_name`, f.account, f.workouts[6], f.paths[0])
		})
	}
}

// These full-row oracles describe 032 source identities, before canonical aliases.
func coverageDisplayMigrate32(t *testing.T, ctx context.Context) {
	t.Helper()
	config, err := pgx.ParseConfig(os.Getenv("MIGRATION_DATABASE_URL"))
	if err != nil {
		t.Fatal("invalid disposable migration database configuration")
	}
	db := stdlib.OpenDB(*config)
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 32); err != nil {
		t.Fatal(err)
	}
	var historical bool
	if err := db.QueryRowContext(ctx, `SELECT schema_version=32 AND minimum_runtime_version=32 AND migration_baseline=27
		AND (SELECT count(*)=33 AND count(DISTINCT version_id)=33 AND min(version_id)=0 AND max(version_id)=32 AND bool_and(is_applied)
			FROM public.goose_db_version) FROM app.schema_metadata WHERE singleton`).Scan(&historical); err != nil || !historical {
		t.Fatalf("historical display oracle requires an isolated Goose schema32/min32/marker27 fixture, not latest: %t %v", historical, err)
	}
}

func newCoverageDisplayFixture(t *testing.T, db testDatabases) coverageDisplayFixture {
	t.Helper()
	source := reprocessingJob(t, db, nil, uuid.Nil, uuid.Nil, "incremental")
	f := coverageDisplayFixture{bootstrapFixture: bootstrapFixture{account: source.account, lease: source.lease},
		job: source.job, session: uuid.New(), campus: uuid.New()}
	for range 7 {
		f.workouts = append(f.workouts, uuid.Must(uuid.NewV7()))
	}
	principal := accountRequester(t, db, f.account)
	// Like the other pipeline fixtures, rows remain in this explicitly disposable
	// database for inspection; account/job deletion is not the behavior under test.
	for range 6 {
		f.paths = append(f.paths, uuid.New())
	}
	for range 13 {
		f.segments = append(f.segments, uuid.New())
	}
	for range 5 {
		f.parks = append(f.parks, uuid.New())
	}
	// Guarantee alphabetic UUID bytes for the noncanonical uppercase/compact key.
	f.parks[2] = uuid.MustParse("abcdefab-cdef-4abc-8def-abcdefabcdef")
	tx := beginAccount(t, db.ctx, db.migration, f.account)
	coverageDisplayFence(t, db, tx, f)
	file := uuid.New()
	coverageDisplayExec(t, db, tx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,
		relative_name,size_bytes,state,processing_started_at)
		SELECT $1,$2,source_id,job_id,'synthetic-display.json',1,'processing',clock_timestamp()
		FROM app.job_config_snapshots WHERE job_id=$3`, file, f.account, f.job)
	workoutType := uuid.New()
	coverageDisplayExec(t, db, tx, `INSERT INTO app.workout_types(id,account_id,type_key,provider_label)
		VALUES($1,$2,'outdoor-run','Outdoor Run')`, workoutType, f.account)
	coverageDisplayExec(t, db, tx, `INSERT INTO app.coverage_region_catalog(region_id,display_name)
		VALUES('display:alpha','Alpha Fixture Region'),('display:beta','Beta Fixture Region'),('display:zeta','Zeta Fixture Region')
		ON CONFLICT(region_id) DO UPDATE SET display_name=EXCLUDED.display_name`)
	dates := []string{"2026-01-10", "2026-01-10", "2026-01-10", "2026-01-12", "2026-01-12", "2026-01-01", "2026-02-01"}
	hours := []int{8, 8, 7, 9, 8, 8, 8}
	for i, workout := range f.workouts {
		coverageDisplayExec(t, db, tx, `INSERT INTO app.workouts(id,account_id,source_id,source_file_id,
			workout_type_id,provider_id,content_sha256,provider_label,local_start_date,started_at,ended_at,provider_duration)
			VALUES($2,$1,$6,$5,$7,$2::uuid::text,$8,'Outdoor Run',$3::date,
			($3::date+make_interval(hours=>$4)) AT TIME ZONE 'UTC',
			($3::date+make_interval(hours=>$4)+interval '1 hour') AT TIME ZONE 'UTC',3600)`,
			f.account, workout, dates[i], hours[i], file, source.source, workoutType, bootstrapDigest())
		coverageDisplayExec(t, db, tx, `INSERT INTO app.workout_route_points(account_id,workout_id,sequence,recorded_at,latitude,longitude)
			VALUES($1,$2,0,'2026-01-01T00:00:00Z',37,-122),($1,$2,1,'2026-01-01T00:00:01Z',37.0001,-122.0001)`,
			f.account, workout)
		coverageDisplayExec(t, db, tx, `INSERT INTO app.workout_routes(account_id,workout_id,point_count,
			minimum_longitude,minimum_latitude,maximum_longitude,maximum_latitude,has_complete_altitude,route)
			VALUES($1,$2,2,-122.0001,37,-122,37.0001,false,
			ST_GeomFromText('MULTILINESTRING((-122 37,-122.0001 37.0001))',4326))`, f.account, workout)
		coverageDisplayExec(t, db, tx, `INSERT INTO app.workout_coverage_states(account_id,workout_id,
			route_input_revision,route_input_sha256,readiness_state,map_data_ready_at,processing_state,
			applied_route_input_revision,applied_route_input_sha256,applied_rules_version,applied_sampling_version,
			applied_path_policy_version,applied_generations,processing_finished_at)
			VALUES($1,$2,1,$3,'map_data_ready',transaction_timestamp(),'current',1,$3,'coverage-experimental-v1',
			'coverage-sampling-experimental-v1','coverage-path-policy-experimental-v82',
			'[{"regionId":"display:zeta","generation":1}]',transaction_timestamp())`, f.account, workout, bootstrapDigest())
	}
	for i, name := range []any{"Campus Approach", "Campus Trail", nil, "Regional Walk", "Key Present", "Spare Source"} {
		coverageDisplayExec(t, db, tx, `INSERT INTO app.coverage_paths(account_id,logical_path_id,
			locality_relation_id,locality_relation_version,locality_name,name,normalized_name,broad_class)
			VALUES($1,$2,101,1,'Fixture Town',$3,lower($3),'footway')`, f.account, f.paths[i], name)
	}
	parkTags := func(index int, kind, name string, noncanonical bool) string {
		key := f.parks[index].String()
		if noncanonical {
			key = strings.ToUpper(key)
		}
		return fmt.Sprintf(`{"workouts:park_id":%q,"workouts:park_kind":%q,"workouts:park_name":%q,"workouts:park_normalized_name":%q}`,
			key, kind, name, strings.ToLower(name))
	}
	education := fmt.Sprintf(`{"workouts:education_id":"%s","workouts:education_name":"Fixture School"}`, f.campus)
	segments := []struct {
		path    int
		highway string
		tags    string
	}{
		{0, "footway", education},
		{1, "path", education},
		{2, "footway", parkTags(0, "local_park", "Fixture Local Park", false)},
		{3, "residential", "{}"},
		{3, "service", strings.TrimSuffix(parkTags(1, "state_park", "Excluded Regional Context", false), "}") + `,"service":"driveway"}`},
		{4, "footway", `{"workouts:education_id":"","workouts:education_name":"Empty Key Campus"}`},
		{4, "footway", `{"workouts:education_id":null}`},
		{2, "footway", parkTags(2, "local_park", "Noncanonical Park", true)},
		{2, "footway", parkTags(4, "local_park", "Bounds Only Park", false)},
		{3, "footway", strings.TrimSuffix(parkTags(1, "national_park", "Z Excluded Context", false), "}") + `,"amenity":"parking"}`},
		{3, "service", strings.TrimSuffix(parkTags(1, "state_park", "Y Excluded Context", false), "}") + `,"service":"parking_aisle"}`},
		{0, "footway", strings.TrimSuffix(education, "}") + "," + strings.TrimPrefix(parkTags(0, "local_park", "Fixture Local Park", false), "{")},
		{5, "footway", "{}"},
	}
	for i, segment := range segments {
		region := "display:zeta"
		if i == 1 || i == 4 || i == 7 {
			region = "display:alpha"
		}
		line := fmt.Sprintf("LINESTRING(%g %g,%g %g)", float64(i+1), float64(i+20), float64(i+1)+0.25, float64(i+20)+0.5)
		coverageDisplayExec(t, db, tx, `INSERT INTO app.path_segments(account_id,region_id,generation_id,
			physical_segment_id,logical_path_id,derivation_version,locality_relation_id,source_way_id,source_way_version,
			name,normalized_name,highway,broad_class,tags,segment_meters,geom)
			SELECT $1,$2,1,$3,logical_path_id,4,101,$4,1,name,normalized_name,$5,broad_class,$6::jsonb,30,
			ST_GeomFromText($7,4326) FROM app.coverage_paths WHERE account_id=$1 AND logical_path_id=$8`,
			f.account, region, f.segments[i], int64(8000+i), segment.highway, segment.tags, line, f.paths[segment.path])
	}
	// All source, workout and coverage writes run with normal triggers enabled.
	for i, workout := range f.workouts {
		coverageDisplayInsertMatch(t, db, tx, f, workout, f.segments[0], 0)
		if i == 0 {
			for j := 1; j < 12; j++ {
				coverageDisplayInsertMatch(t, db, tx, f, workout, f.segments[j], j)
			}
		}
	}
	// Deliberately separate attribution counts from the evidence bounding boxes.
	coverageDisplayExec(t, db, tx, `INSERT INTO app.workout_park_attributions(account_id,workout_id,park_id,first_traversed_at,visit_date)
		VALUES($1,$2,$3,'2026-01-10T08:00:00Z','1999-01-01')`, f.account, f.workouts[1], f.parks[0])
	coverageDisplayExec(t, db, tx, `DELETE FROM app.workout_park_attributions
		WHERE account_id=$1 AND workout_id=$2 AND park_id IN ($3,$4)`,
		f.account, f.workouts[0], f.parks[0], f.parks[4])
	coverageDisplayExec(t, db, tx, `INSERT INTO app.coverage_parks(account_id,park_id,name,normalized_name,park_kind,
		locality_relation_id,locality_relation_version,locality_name)
		VALUES($1,$2,'Attribution Only Park','attribution only park','local_park',101,1,'Fixture Town')`, f.account, f.parks[3])
	coverageDisplayExec(t, db, tx, `INSERT INTO app.workout_park_attributions(account_id,workout_id,park_id,first_traversed_at,visit_date)
		VALUES($1,$2,$3,'2026-01-10T08:00:00Z','1999-01-01')`, f.account, f.workouts[0], f.parks[3])
	coverageDisplayExec(t, db, tx, `INSERT INTO app.sessions(id,principal_id,credential_kind,credential_verifier,expires_at)
		VALUES($1,$2,'bearer',$3,transaction_timestamp()+interval '1 hour')`, f.session, principal,
		[]byte(strings.ReplaceAll(f.session.String(), "-", "")))
	coverageDisplayCommit(t, db, tx)
	return f
}

func coverageDisplayInsertMatch(t *testing.T, db testDatabases, tx pgx.Tx, f coverageDisplayFixture, workout, segment uuid.UUID, order int) {
	t.Helper()
	coverageDisplayExec(t, db, tx, `INSERT INTO app.workout_segment_matches(account_id,workout_id,physical_segment_id,
		region_id,generation_id,logical_path_id,first_traversed_at,first_route_order,covered_meters,geom)
		SELECT account_id,$2,physical_segment_id,region_id,generation_id,logical_path_id,
		'2026-01-10T08:00:00Z',$4,30,ST_Multi(geom) FROM app.path_segments
		WHERE account_id=$1 AND physical_segment_id=$3`, f.account, workout, segment, order)
}

// Extract only the immutable 019 SQL body, not pg_get_functiondef of the current
// reader. Neither projection tables nor 032's aggregation logic enter the oracle.
func coverageDisplayOracle(t *testing.T) string {
	t.Helper()
	source, err := migrations.Files.ReadFile("019_coverage_explorer_reads.sql")
	if err != nil {
		t.Fatal(err)
	}
	body := coverageDisplayFunction(t, string(source), "map_selection_coverage_entities")
	return strings.NewReplacer("target_account_id", "$1::uuid", "target_session_id", "$2::uuid",
		"target_selection_id", "$3::uuid", "target_generation", "$4::bigint", "target_search", "$5::text").Replace(body)
}

func coverageDisplayParity(t *testing.T, db testDatabases, f coverageDisplayFixture) coverageDisplaySelection {
	t.Helper()
	s := coverageDisplaySelection{id: uuid.New()}
	tx := beginAccount(t, db.ctx, db.migration, f.account)
	if err := tx.QueryRow(db.ctx, `SELECT generation FROM app.account_data_generations WHERE account_id=$1`, f.account).Scan(&s.generation); err != nil {
		t.Fatal(err)
	}
	// A new selection is necessary after committed generation-changing writes.
	// No checked workouts: the read contract deliberately ignores that subset.
	coverageDisplayExec(t, db, tx, `INSERT INTO app.map_selections(id,account_id,session_id,generation,expires_at,
		start_date,end_date,selection_kind) VALUES($1,$2,$3,$4,transaction_timestamp()+interval '30 minutes',
		'2026-01-10','2026-01-12','explicit_subset')`, s.id, f.account, f.session, s.generation)
	coverageDisplayCommit(t, db, tx)
	for _, search := range []any{nil, "", "SCHOOL", "EXCLUDED REGIONAL", f.campus.String(), "%_no_match"} {
		read := beginAccount(t, db.ctx, db.migration, f.account)
		coverageDisplayExec(t, db, read, `SET LOCAL jit=off`)
		var delta, actual string
		err := read.QueryRow(db.ctx, `WITH actual AS MATERIALIZED (
			SELECT * FROM app.map_selection_coverage_entities($1,$2,$3,$4,$5)
		), expected AS MATERIALIZED (`+coverageDisplayOracle(t)+`), differences AS (
			SELECT 'new-only' direction,to_jsonb(difference_row) value FROM (TABLE actual EXCEPT ALL TABLE expected) difference_row
			UNION ALL
			SELECT 'old-only',to_jsonb(difference_row) FROM (TABLE expected EXCEPT ALL TABLE actual) difference_row
		) SELECT (SELECT COALESCE(jsonb_agg(to_jsonb(differences)),'[]'::jsonb)::text FROM differences),
			(SELECT COALESCE(jsonb_agg(to_jsonb(actual) ORDER BY entity_kind,entity_id),'[]'::jsonb)::text FROM actual)`,
			f.account, f.session, s.id, s.generation, search).Scan(&delta, &actual)
		_ = read.Rollback(db.ctx)
		if err != nil || delta != "[]" {
			t.Fatalf("committed full-row 032/019 EXCEPT ALL parity, search=%v: %s err=%v", search, delta, err)
		}
		api := beginAccount(t, db.ctx, db.api, f.account)
		var exposed string
		err = api.QueryRow(db.ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(entity) ORDER BY entity_kind,entity_id),'[]'::jsonb)::text
			FROM app.map_selection_coverage_entities($1,$2,$3,$4,$5) entity`,
			f.account, f.session, s.id, s.generation, search).Scan(&exposed)
		_ = api.Rollback(db.ctx)
		if err != nil || exposed != actual {
			t.Fatalf("API role changed full-row results, search=%v: %s want=%s err=%v", search, exposed, actual, err)
		}
	}
	coverageDisplayCheck(t, db, `SELECT NOT EXISTS(SELECT 1 FROM app.coverage_display_dirty WHERE account_id=$1)`, f.account)
	return s
}

func coverageDisplayEntityCheck(t *testing.T, db testDatabases, f coverageDisplayFixture, s coverageDisplaySelection, entity uuid.UUID, predicate string, args ...any) {
	t.Helper()
	values := append([]any{f.account, f.session, s.id, s.generation, entity}, args...)
	coverageDisplayCheck(t, db, `SELECT count(*)=1 AND bool_and(`+predicate+`)
		FROM app.map_selection_coverage_entities($1,$2,$3,$4,NULL) WHERE entity_id=$5`, values...)
}

func coverageDisplayFacts(t *testing.T, db testDatabases, f coverageDisplayFixture) string {
	t.Helper()
	var snapshot string
	if err := db.migration.QueryRow(db.ctx, `SELECT jsonb_build_object(
		'paths',(SELECT jsonb_agg(to_jsonb(p) ORDER BY workout_id,entity_id,logical_path_id,qualifies_without_name)
			FROM app.workout_path_display_partials p WHERE account_id=$1),
		'context',(SELECT jsonb_agg(to_jsonb(c) ORDER BY workout_id,entity_id)
			FROM app.workout_path_display_context c WHERE account_id=$1),
		'parks',(SELECT jsonb_agg(to_jsonb(b) ORDER BY workout_id,park_id)
			FROM app.workout_park_display_bounds b WHERE account_id=$1))::text`, f.account).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func coverageDisplayCheck(t *testing.T, db testDatabases, query string, args ...any) {
	t.Helper()
	// Oracle/reader calls still require account context, including privileged reads.
	account := uuid.Nil
	if len(args) > 0 {
		account, _ = args[0].(uuid.UUID)
	}
	tx := beginAccount(t, db.ctx, db.migration, account)
	defer tx.Rollback(db.ctx)
	var valid bool
	if err := tx.QueryRow(db.ctx, query, args...).Scan(&valid); err != nil || !valid {
		t.Fatalf("coverage display invariant failed: %v\n%s\nargs=%v", err, query, args)
	}
}

func coverageDisplayExec(t *testing.T, db testDatabases, tx pgx.Tx, query string, args ...any) {
	t.Helper()
	if _, err := tx.Exec(db.ctx, query, args...); err != nil {
		t.Fatalf("coverage display fixture write: %v\n%s", err, query)
	}
}

func coverageDisplayCommit(t *testing.T, db testDatabases, tx pgx.Tx) {
	t.Helper()
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatalf("coverage display fixture commit: %v", err)
	}
}

func coverageDisplayFence(t *testing.T, db testDatabases, tx pgx.Tx, f coverageDisplayFixture) {
	t.Helper()
	var source uuid.UUID
	if err := tx.QueryRow(db.ctx, `SELECT source_id FROM app.fence_ingest_job($1,$2,$3,$4)`,
		f.job, reprocessingWorker, f.lease, 32).Scan(&source); err != nil {
		t.Fatalf("fence synthetic source job for header mutation/purge: %v", err)
	}
}

func coverageDisplayWorkoutExec(t *testing.T, db testDatabases, f coverageDisplayFixture, query string, args ...any) {
	t.Helper()
	tx := beginAccount(t, db.ctx, db.migration, f.account)
	coverageDisplayFence(t, db, tx, f)
	coverageDisplayExec(t, db, tx, query, args...)
	coverageDisplayCommit(t, db, tx)
}
