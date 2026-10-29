package migrations_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const coverageEntityStatsColumns = `entity_id,entity_kind,broad_class,name,locality_name,region_id,region_name,
	range_workout_count,range_first_date,range_first_workout_id,range_latest_date,range_latest_workout_id,
	all_time_workout_count,all_time_first_date,all_time_first_workout_id,all_time_latest_date,all_time_latest_workout_id,
	minimum_longitude,minimum_latitude,maximum_longitude,maximum_latitude`

func TestCoverageEntityStatsPostgreSQL(t *testing.T) {
	if os.Getenv("COVERAGE_DISPLAY_PRECOMPUTATIONS_TEST_DISPOSABLE") != "1" ||
		os.Getenv("API_DATABASE_URL") == "" || os.Getenv("WORKER_DATABASE_URL") == "" || os.Getenv("MIGRATION_DATABASE_URL") == "" {
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
			t.Fatal("entity stats tests require a named loopback test database")
		}
		for _, fallback := range cfg.Fallbacks {
			if fallback.Host != cfg.Host || fallback.Port != cfg.Port {
				t.Fatal("entity stats tests prohibit alternate database endpoints")
			}
		}
		target := fmt.Sprintf("%s:%d/%s", cfg.Host, cfg.Port, cfg.Database)
		if endpoint != "" && endpoint != target {
			t.Fatal("runtime and migration URLs must address the same disposable database")
		}
		endpoint = target
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	db := testDatabases{ctx: ctx,
		api: openPool(t, ctx, os.Getenv("API_DATABASE_URL"), 2), worker: openPool(t, ctx, os.Getenv("WORKER_DATABASE_URL"), 2),
		migration: openPool(t, ctx, os.Getenv("MIGRATION_DATABASE_URL"), 4)}
	t.Cleanup(cancel)
	assertRuntimeRole(t, ctx, db.api, "workouts_api")
	assertRuntimeRole(t, ctx, db.worker, "workouts_worker")
	assertRuntimeRole(t, ctx, db.migration, "workouts_migration")
	coverageDisplayMigrate30(t, ctx)

	t.Run("Full21FieldsEducationDedupAndCheckedSubsetIndependence", func(t *testing.T) {
		f := newCoverageDisplayFixture(t, db)
		s := coverageEntityStatsAll(t, db, f)
		coverageEntityStatsCheck(t, db, f, s, "path", f.campus, `broad_class='other' AND name='Fixture School'
			AND range_workout_count=5 AND all_time_workout_count=7
			AND range_first_workout_id=$7 AND range_latest_workout_id=$8
			AND all_time_first_workout_id=$9 AND all_time_latest_workout_id=$10
			AND minimum_longitude=1 AND maximum_longitude=12.25`, f.workouts[2], f.workouts[4], f.workouts[5], f.workouts[6])
		coverageEntityStatsCheck(t, db, f, s, "path", f.paths[4], `broad_class='other' AND name='Empty Key Campus'`)
		checked := coverageEntityStatsSelection(t, db, f, s.generation, "2026-01-10", "2026-01-12", f.workouts[:1])
		coverageEntityStatsAllSelection(t, db, f, checked)
		for _, entity := range []struct {
			kind string
			id   uuid.UUID
		}{{"path", f.campus}, {"park", f.parks[0]}} {
			before := coverageEntityStatsAPI(t, db, f.account, f.account, f.session, s.id, s.generation, entity.kind, entity.id)
			after := coverageEntityStatsAPI(t, db, f.account, f.account, f.session, checked.id, checked.generation, entity.kind, entity.id)
			if before == "[]" || after != before {
				t.Fatalf("checked/focused subset changed %s stats: before=%s after=%s", entity.kind, before, after)
			}
		}
		narrow := coverageEntityStatsSelection(t, db, f, s.generation, "2026-01-12", "2026-01-12", nil)
		coverageEntityStatsAllSelection(t, db, f, narrow)
		coverageEntityStatsCheck(t, db, f, narrow, "path", f.campus, `range_workout_count=2 AND all_time_workout_count=7
			AND range_first_workout_id=$7 AND range_latest_workout_id=$7 AND maximum_longitude=1.25`, f.workouts[4])
	})

	t.Run("LiveMetadataSourcePathQualificationAndHistoricalIdentity", func(t *testing.T) {
		f := newCoverageDisplayFixture(t, db)
		coverageEntityStatsAll(t, db, f)
		bootstrapExec(t, db, f.bootstrapFixture, `UPDATE app.coverage_paths SET name='Named Local Walk',normalized_name='named local walk'
			WHERE account_id=$1 AND logical_path_id=$2`, f.account, f.paths[2])
		s := coverageEntityStatsAll(t, db, f)
		coverageEntityStatsCheck(t, db, f, s, "path", f.paths[2], `name='Named Local Walk' AND range_workout_count=1`)
		bootstrapExec(t, db, f.bootstrapFixture, `UPDATE app.coverage_paths SET name=NULL,normalized_name=NULL
			WHERE account_id=$1 AND logical_path_id IN ($2,$3)`, f.account, f.paths[0], f.paths[2])
		s = coverageEntityStatsAll(t, db, f)
		coverageEntityStatsCheck(t, db, f, s, "path", f.campus, `range_workout_count=5 AND maximum_longitude=2.25`)
		bootstrapExec(t, db, f.bootstrapFixture, `UPDATE app.coverage_paths SET name='Changed Walk',normalized_name='changed walk',
			broad_class='cycleway',locality_name='Changed Town' WHERE account_id=$1 AND logical_path_id=$2`, f.account, f.paths[3])
		bootstrapExec(t, db, f.bootstrapFixture, `UPDATE app.coverage_parks SET name='Changed Park',normalized_name='changed park',
			locality_name='Changed Town' WHERE account_id=$1 AND park_id=$2`, f.account, f.parks[0])
		s = coverageEntityStatsAll(t, db, f)
		coverageEntityStatsCheck(t, db, f, s, "path", f.paths[3], `name='Changed Walk' AND broad_class='cycleway'
			AND locality_name='Excluded Regional Context'`)
		coverageEntityStatsCheck(t, db, f, s, "park", f.parks[0], `name='Changed Park' AND locality_name='Changed Town' AND region_id='display:zeta'`)
		tx := beginAccount(t, ctx, db.migration, f.account)
		segment := coverageEntityStatsClone(t, db, tx, f, f.segments[0], `{"workouts:education_name":"A Historical School"}`, 0)
		coverageDisplayExec(t, db, tx, `UPDATE app.workout_segment_matches SET physical_segment_id=$3
			WHERE account_id=$1 AND workout_id=$2 AND physical_segment_id=$4`, f.account, f.workouts[6], segment, f.segments[0])
		coverageDisplayCommit(t, db, tx)
		s = coverageEntityStatsAll(t, db, f)
		coverageEntityStatsCheck(t, db, f, s, "path", f.campus, `name='A Historical School' AND range_workout_count=5
			AND all_time_workout_count=7 AND maximum_longitude=2.25`)
	})

	t.Run("FirstLatestStartAndUUIDTies", func(t *testing.T) {
		f := newCoverageDisplayFixture(t, db)
		coverageEntityStatsAll(t, db, f)
		coverageDisplayWorkoutExec(t, db, f, `UPDATE app.workouts SET started_at='2026-01-12T08:00:00Z'
			WHERE account_id=$1 AND id=$2`, f.account, f.workouts[3])
		s := coverageEntityStatsAll(t, db, f)
		coverageEntityStatsCheck(t, db, f, s, "path", f.campus, `range_latest_workout_id=$7`, f.workouts[3])
		coverageDisplayWorkoutExec(t, db, f, `UPDATE app.workouts SET local_start_date='2026-01-11'
			WHERE account_id=$1 AND id IN ($2,$3)`, f.account, f.workouts[2], f.workouts[4])
		s = coverageEntityStatsAll(t, db, f)
		coverageEntityStatsCheck(t, db, f, s, "path", f.campus, `range_first_workout_id=$7 AND range_latest_workout_id=$8`, f.workouts[0], f.workouts[3])
		coverageDisplayWorkoutExec(t, db, f, `UPDATE app.workouts SET
			local_start_date=CASE WHEN id=$2 THEN DATE '2026-01-10' ELSE DATE '2026-01-12' END,
			started_at=CASE WHEN id=$2 THEN TIMESTAMPTZ '2026-01-10T08:00:00Z' ELSE TIMESTAMPTZ '2026-01-12T08:00:00Z' END,
			ended_at=CASE WHEN id=$2 THEN TIMESTAMPTZ '2026-01-10T09:00:00Z' ELSE TIMESTAMPTZ '2026-01-12T09:00:00Z' END
			WHERE account_id=$1 AND id IN ($2,$3)`, f.account, f.workouts[5], f.workouts[6])
		s = coverageEntityStatsAll(t, db, f)
		coverageEntityStatsCheck(t, db, f, s, "path", f.campus, `range_workout_count=7 AND all_time_workout_count=7
			AND range_first_workout_id=$7 AND all_time_first_workout_id=$7
			AND range_latest_workout_id=$8 AND all_time_latest_workout_id=$8`, f.workouts[0], f.workouts[3])
	})

	t.Run("LiveEligibilityStaleAppliedDeletionAndDateUpdates", func(t *testing.T) {
		f := newCoverageDisplayFixture(t, db)
		bootstrapExec(t, db, f.bootstrapFixture, `UPDATE app.workout_coverage_states SET processing_state='stale',route_input_revision=2
			WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0])
		s := coverageEntityStatsAll(t, db, f)
		coverageEntityStatsCheck(t, db, f, s, "path", f.campus, `range_workout_count=5 AND all_time_workout_count=7`)
		bootstrapExec(t, db, f.bootstrapFixture, `UPDATE app.workout_routes SET route=NULL WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[1])
		s = coverageEntityStatsAll(t, db, f)
		coverageEntityStatsCheck(t, db, f, s, "path", f.campus, `range_workout_count=4 AND all_time_workout_count=6`)
		tx := beginAccount(t, ctx, db.api, f.account)
		coverageDisplayExec(t, db, tx, `SELECT * FROM app.enqueue_workout_deletion($1,$2)`, f.workouts[2], accountRequester(t, db, f.account))
		coverageDisplayCommit(t, db, tx)
		s = coverageEntityStatsAll(t, db, f)
		coverageEntityStatsCheck(t, db, f, s, "path", f.campus, `range_workout_count=3 AND all_time_workout_count=5`)
		bootstrapExec(t, db, f.bootstrapFixture, `UPDATE app.workout_coverage_states SET applied_route_input_revision=NULL
			WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[3])
		s = coverageEntityStatsAll(t, db, f)
		coverageEntityStatsCheck(t, db, f, s, "path", f.campus, `range_workout_count=2 AND all_time_workout_count=4`)
		coverageDisplayWorkoutExec(t, db, f, `UPDATE app.workouts SET local_start_date='2026-01-15'
			WHERE account_id=$1 AND id=$2`, f.account, f.workouts[4])
		s = coverageEntityStatsAll(t, db, f)
		coverageEntityStatsCheck(t, db, f, s, "path", f.campus, `range_workout_count=1 AND all_time_workout_count=4`)
		coverageDisplayWorkoutExec(t, db, f, `UPDATE app.workouts SET local_start_date=NULL WHERE account_id=$1 AND id=$2`, f.account, f.workouts[6])
		s = coverageEntityStatsAll(t, db, f)
		coverageEntityStatsCheck(t, db, f, s, "path", f.campus, `range_workout_count=1 AND all_time_workout_count=3`)
	})

	t.Run("ParkCanonicalGeometryAvailabilityAndMixedBounds", func(t *testing.T) {
		f := newCoverageDisplayFixture(t, db)
		s := coverageEntityStatsAll(t, db, f)
		coverageEntityStatsCheck(t, db, f, s, "park", f.parks[0], `region_id='display:zeta' AND range_first_workout_id=$7
			AND minimum_longitude=3 AND maximum_longitude=12.25`, f.workouts[1])
		coverageDisplayCheck(t, db, `SELECT NOT EXISTS(SELECT 1 FROM app.coverage_display_entities entity
			JOIN app.coverage_display_members member USING(account_id,context_id,canonical_id)
			WHERE entity.account_id=$1 AND member.source_id=$2 AND entity.context_id=(
			SELECT coverage_display_context_id FROM app.map_selections WHERE id=$3))`, f.account, f.parks[2], s.id)
		bootstrapExec(t, db, f.bootstrapFixture, `UPDATE app.path_segments SET tags=jsonb_set(tags,'{workouts:park_id}',to_jsonb($3::text))
			WHERE account_id=$1 AND physical_segment_id=$2`, f.account, f.segments[7], f.parks[2].String())
		s = coverageEntityStatsAll(t, db, f)
		coverageEntityStatsCheck(t, db, f, s, "park", f.parks[2], `region_id='display:alpha' AND maximum_longitude=8.25`)
		tx := beginAccount(t, ctx, db.migration, f.account)
		segment := coverageEntityStatsClone(t, db, tx, f, f.segments[7], fmt.Sprintf(`{"workouts:park_id":%q}`, strings.ToUpper(f.parks[2].String())), 2)
		coverageDisplayInsertMatch(t, db, tx, f, f.workouts[0], segment, 99)
		coverageDisplayCommit(t, db, tx)
		s = coverageEntityStatsAll(t, db, f)
		coverageEntityStatsCheck(t, db, f, s, "park", f.parks[2], `range_workout_count=1 AND minimum_longitude=8 AND maximum_longitude=10.25`)
		coverageDisplayCheck(t, db, `SELECT fit_maximum_longitude < maximum_longitude
			FROM app.map_selection_coverage_entity_detail($1,$2,$3,$4,'park',$5)`, f.account, f.session, s.id, s.generation,
			coverageDisplayCanonicalID(t, db, f.account, s.id, "park", f.parks[2]))
		tx = beginAccount(t, ctx, db.migration, f.account)
		coverageDisplayInsertMatch(t, db, tx, f, f.workouts[6], f.segments[7], 99)
		coverageDisplayExec(t, db, tx, `DELETE FROM app.workout_segment_matches WHERE account_id=$1 AND workout_id=$2 AND physical_segment_id=$3`,
			f.account, f.workouts[0], f.segments[7])
		// 018 prunes attributions by canonical text on match deletion. Retain an
		// independent range visit to isolate 020's range-only geometry gate.
		coverageDisplayExec(t, db, tx, `INSERT INTO app.workout_park_attributions(account_id,workout_id,park_id,first_traversed_at,visit_date)
			VALUES($1,$2,$3,'2026-01-10T08:00:00Z','2026-01-10')`, f.account, f.workouts[0], f.parks[2])
		coverageDisplayCommit(t, db, tx)
		s = coverageEntityStatsAll(t, db, f)
		// Historical canonical region plus noncanonical range bounds keep the
		// cached row visible, but cannot manufacture canonical range geometry.
		id := coverageDisplayCanonicalID(t, db, f.account, s.id, "park", f.parks[2])
		coverageEntityStatsCheck(t, db, f, s, "park", f.parks[2], `range_workout_count=1 AND all_time_workout_count=2
			AND region_id='display:alpha' AND minimum_longitude=10 AND maximum_longitude=10.25`)
		coverageDisplayCheck(t, db, `SELECT NOT EXISTS(SELECT 1 FROM app.map_selection_coverage_entity_detail($1,$2,$3,$4,'park',$5))`,
			f.account, f.session, s.id, s.generation, id)
		coverageDisplayWorkoutExec(t, db, f, `UPDATE app.workouts SET local_start_date='2026-01-11' WHERE account_id=$1 AND id=$2`, f.account, f.workouts[6])
		s = coverageEntityStatsAll(t, db, f)
		coverageEntityStatsCheck(t, db, f, s, "park", f.parks[2], `range_workout_count=2 AND all_time_workout_count=2
			AND minimum_longitude=8 AND maximum_longitude=10.25`)
	})

	t.Run("WarmCanonicalProbeDoesNotScaleWithAccountWorkouts", func(t *testing.T) {
		f := newCoverageDisplayFixture(t, db)
		contextWorkout := coverageEntityStatsAddWorkouts(t, db, f, 1)[0]
		tx := beginAccount(t, ctx, db.migration, f.account)
		contextSegment := coverageEntityStatsClone(t, db, tx, f, f.segments[4],
			`{"workouts:park_name":"A Context Only Candidate","workouts:park_normalized_name":"a context only candidate"}`, 0)
		coverageDisplayInsertMatch(t, db, tx, f, contextWorkout, contextSegment, 0)
		coverageDisplayCommit(t, db, tx)
		s := coverageEntityStatsAll(t, db, f)
		coverageEntityStatsCheck(t, db, f, s, "path", f.paths[3],
			`range_workout_count=1 AND locality_name='A Context Only Candidate'`)
		coverageDisplayCheck(t, db, `SELECT
			EXISTS(SELECT 1 FROM app.workout_path_display_context WHERE account_id=$1 AND workout_id=$2 AND entity_id=$3)
			AND NOT EXISTS(SELECT 1 FROM app.workout_path_display_partials WHERE account_id=$1 AND workout_id=$2 AND entity_id=$3)`,
			f.account, contextWorkout, f.paths[3])
		targets := []struct {
			kind       string
			id         uuid.UUID
			candidates int64
		}{
			{"path", f.campus, 7},
			{"path", f.paths[3], 2},
			{"park", f.parks[0], 2},
		}
		before := make([]string, len(targets))
		for i, target := range targets {
			before[i] = coverageEntityStatsAPI(t, db, f.account, f.account, f.session, s.id, s.generation, target.kind, target.id)
			if before[i] == "[]" {
				t.Fatal("plan regression targets must have visible stats")
			}
			coverageEntityStatsExplain(t, db, f, s, target.kind, target.id, target.candidates, 8)
		}
		unrelated := coverageEntityStatsAddWorkouts(t, db, f, 50)
		coverageDisplayCheck(t, db, `SELECT NOT EXISTS(SELECT 1 FROM app.workout_segment_matches WHERE account_id=$1 AND workout_id=ANY($2::uuid[]))
			AND NOT EXISTS(SELECT 1 FROM app.workout_park_attributions WHERE account_id=$1 AND workout_id=ANY($2::uuid[]))`, f.account, unrelated)
		s = coverageEntityStatsAll(t, db, f)
		for i, target := range targets {
			if after := coverageEntityStatsAPI(t, db, f.account, f.account, f.session, s.id, s.generation, target.kind, target.id); after != before[i] {
				t.Fatalf("unrelated eligible workouts changed all 21 fields: kind=%s before=%s after=%s", target.kind, before[i], after)
			}
			coverageEntityStatsExplain(t, db, f, s, target.kind, target.id, target.candidates, 58)
		}
	})

	t.Run("AuthorizationScopeAndSelectionLiveness", func(t *testing.T) {
		f, foreign := newCoverageDisplayFixture(t, db), newCoverageDisplayFixture(t, db)
		s, other := coverageEntityStatsAll(t, db, f), coverageEntityStatsAll(t, db, foreign)
		valid := []any{f.account, f.session, s.id, s.generation, "path", f.campus}
		if got := coverageEntityStatsAPI(t, db, f.account, valid...); got == "[]" {
			t.Fatal("authorization fixture must have visible stats before testing rejection")
		}
		for _, test := range []struct {
			name    string
			context uuid.UUID
			args    []any
		}{
			{"foreign_context", foreign.account, valid},
			{"foreign_account", f.account, []any{foreign.account, foreign.session, other.id, other.generation, "path", foreign.campus}},
			{"foreign_session", f.account, []any{f.account, foreign.session, s.id, s.generation, "path", f.campus}},
			{"foreign_selection", f.account, []any{f.account, f.session, other.id, s.generation, "path", f.campus}},
			{"foreign_entity", f.account, []any{f.account, f.session, s.id, s.generation, "path", foreign.campus}},
			{"wrong_generation", f.account, []any{f.account, f.session, s.id, s.generation + 1, "path", f.campus}},
			{"missing_selection", f.account, []any{f.account, f.session, uuid.New(), s.generation, "path", f.campus}},
			{"missing_session", f.account, []any{f.account, nil, s.id, s.generation, "path", f.campus}},
			{"missing_context", uuid.Nil, valid},
		} {
			t.Run(test.name, func(t *testing.T) {
				if got := coverageEntityStatsAPI(t, db, test.context, test.args...); got != "[]" {
					t.Fatalf("unauthorized stats leaked: %s", got)
				}
			})
		}
		for _, gate := range []struct{ name, apply, restore string }{
			{"revoked_session", `UPDATE app.sessions SET revoked_at=clock_timestamp() WHERE id=$2 AND $1::uuid IS NOT NULL`, `UPDATE app.sessions SET revoked_at=NULL WHERE id=$2 AND $1::uuid IS NOT NULL`},
			{"expired_session", `UPDATE app.sessions SET created_at=clock_timestamp()-interval '2 hours',expires_at=clock_timestamp()-interval '1 hour' WHERE id=$2 AND $1::uuid IS NOT NULL`,
				`UPDATE app.sessions SET expires_at=clock_timestamp()+interval '1 hour' WHERE id=$2 AND $1::uuid IS NOT NULL`},
			{"disabled_principal", `UPDATE app.authentication_principals SET disabled_at=clock_timestamp() WHERE id=(SELECT principal_id FROM app.users WHERE account_id=$1) AND $2::uuid IS NOT NULL`,
				`UPDATE app.authentication_principals SET disabled_at=NULL WHERE id=(SELECT principal_id FROM app.users WHERE account_id=$1) AND $2::uuid IS NOT NULL`},
			{"inactive_account", `UPDATE app.accounts SET state='deleting' WHERE id=$1 AND $2::uuid IS NOT NULL`, `UPDATE app.accounts SET state='active' WHERE id=$1 AND $2::uuid IS NOT NULL`},
		} {
			t.Run(gate.name, func(t *testing.T) {
				bootstrapExec(t, db, f.bootstrapFixture, gate.apply, f.account, f.session)
				if got := coverageEntityStatsAPI(t, db, f.account, valid...); got != "[]" {
					t.Fatalf("invalid scope leaked stats: %s", got)
				}
				bootstrapExec(t, db, f.bootstrapFixture, gate.restore, f.account, f.session)
				coverageEntityStatsRow(t, db, f, s, "path", f.campus)
				if got := coverageEntityStatsAPI(t, db, f.account, valid...); got == "[]" {
					t.Fatal("restoring authorization did not restore visible stats")
				}
			})
		}
		coverageDisplayWorkoutExec(t, db, f, `UPDATE app.workouts SET provider_label='Updated Run' WHERE account_id=$1 AND id=$2`, f.account, f.workouts[0])
		if got := coverageEntityStatsAPI(t, db, f.account, valid...); got != "[]" {
			t.Fatalf("stale selection generation leaked stats: %s", got)
		}
		coverageEntityStatsAll(t, db, f)
	})

	t.Run("RuntimePrivilegesAndPrivateFacts", func(t *testing.T) {
		f := newCoverageDisplayFixture(t, db)
		s := coverageEntityStatsAll(t, db, f)
		coverageDisplayCheck(t, db, `SELECT has_function_privilege('workouts_api',
			'app.map_selection_coverage_entity_stats(uuid,uuid,uuid,bigint,text,uuid)','EXECUTE')
			AND NOT has_function_privilege('workouts_worker','app.map_selection_coverage_entity_stats(uuid,uuid,uuid,bigint,text,uuid)','EXECUTE')
			AND NOT has_function_privilege('workouts_coverage_worker','app.map_selection_coverage_entity_stats(uuid,uuid,uuid,bigint,text,uuid)','EXECUTE')
			AND NOT has_function_privilege('workouts_tiles','app.map_selection_coverage_entity_stats(uuid,uuid,uuid,bigint,text,uuid)','EXECUTE')`)
		for _, worker := range []bool{false, true} {
			pool := db.api
			if worker {
				pool = db.worker
			}
			for _, query := range []string{
				`SELECT * FROM app.workout_path_display_partials LIMIT 0`, `SELECT * FROM app.workout_path_display_context LIMIT 0`,
				`SELECT * FROM app.workout_park_display_bounds LIMIT 0`, `SELECT * FROM app.coverage_display_dirty LIMIT 0`,
				`SELECT app.rebuild_coverage_display_precomputations(NULL)`, `SELECT app.flush_coverage_display_precomputations(NULL)`,
				`SELECT app.mark_coverage_display_dirty()`, `SELECT app.flush_coverage_display_dirty_account()`,
			} {
				tx := beginAccount(t, ctx, pool, f.account)
				_, err := tx.Exec(ctx, query)
				_ = tx.Rollback(ctx)
				bootstrapSQLState(t, err, "42501")
			}
		}
		tx := beginAccount(t, ctx, db.worker, f.account)
		_, err := tx.Exec(ctx, `SELECT * FROM app.map_selection_coverage_entity_stats($1,$2,$3,$4,'path',$5)`, f.account, f.session, s.id, s.generation, f.campus)
		_ = tx.Rollback(ctx)
		bootstrapSQLState(t, err, "42501")
	})
}

func TestCoverageEntityStatsExplainQueryAndCardinalityExtraction(t *testing.T) {
	body := coverageDisplayFunction(t, coverageDisplaySQL(t), "map_selection_coverage_entity_stats")
	query := coverageEntityStatsReturnQuery(t, body)
	if !strings.HasPrefix(query, "SELECT ") || !strings.Contains(query, "entity.canonical_id = target_entity_id") ||
		strings.Contains(query, "RETURN QUERY") || strings.HasSuffix(query, "END;") {
		t.Fatal("plan regression must explain the actual canonical point query, not a function scan")
	}
	for _, join := range []string{"Hash Join", "Nested Loop"} {
		for _, literal := range []string{"2", "2.00", "2e0"} {
			plan := fmt.Sprintf(`[{"Plan":{"Node Type":%q,"Plans":[
				{"Relation Name":"coverage_display_entities","Actual Rows":%s,"Actual Loops":1.00},
				{"Node Type":"CTE Scan","CTE Name":"eligible","Actual Rows":200.00,"Actual Loops":2}]}}]`, join, literal)
			rows, loops := coverageEntityStatsEntityPlanRows(t, []byte(plan))
			if rows != 2 || loops != 1 {
				t.Fatalf("must inspect the producer, not repeated CTE scans: rows=%d loops=%d", rows, loops)
			}
		}
	}
	for _, literal := range []string{"", "1.25", "1.00000000000000000001", "-1", "9223372036854775808"} {
		if _, err := coverageEntityStatsPlanCardinality(json.Number(literal)); err == nil {
			t.Errorf("invalid or non-integral plan cardinality accepted: %q", literal)
		}
	}
}

func coverageEntityStatsAll(t *testing.T, db testDatabases, f coverageDisplayFixture) coverageDisplaySelection {
	t.Helper()
	s := coverageDisplayParity(t, db, f)
	coverageEntityStatsAllSelection(t, db, f, s)
	return s
}

func coverageEntityStatsAllSelection(t *testing.T, db testDatabases, f coverageDisplayFixture, s coverageDisplaySelection) {
	t.Helper()
	for _, entity := range append([]uuid.UUID{f.campus}, f.paths...) {
		coverageEntityStatsRow(t, db, f, s, "path", entity)
	}
	for _, entity := range f.parks {
		coverageEntityStatsRow(t, db, f, s, "park", entity)
	}
	for _, target := range []struct {
		kind any
		id   uuid.UUID
	}{
		{"path", uuid.New()}, {"park", uuid.New()}, {"park", f.campus}, {"path", f.parks[0]}, {"PATH", f.campus}, {"", f.campus}, {nil, f.campus},
	} {
		coverageEntityStatsRow(t, db, f, s, target.kind, target.id)
	}
}

func coverageEntityStatsRow(t *testing.T, db testDatabases, f coverageDisplayFixture, s coverageDisplaySelection, kind any, entity uuid.UUID) {
	t.Helper()
	entity = coverageDisplayCanonicalID(t, db, f.account, s.id, kind, entity)
	tx := beginAccount(t, db.ctx, db.migration, f.account)
	coverageDisplayExec(t, db, tx, `SELECT set_config('app.session_id',$1,true)`, f.session.String())
	coverageDisplayExec(t, db, tx, `SET LOCAL jit=off`)
	var delta, actual string
	err := tx.QueryRow(db.ctx, `WITH actual AS MATERIALIZED (
		SELECT * FROM app.map_selection_coverage_entity_stats($1,$2,$3,$4,$5,$6)
	), listed AS MATERIALIZED (SELECT * FROM app.map_selection_coverage_entities($1,$2,$3,$4,NULL)
		WHERE entity_kind=$5 AND entity_id=$6),
	geometry_available AS (SELECT EXISTS(
		SELECT 1 FROM app.map_selections selection
		JOIN app.coverage_display_members member ON member.account_id=selection.account_id AND member.context_id=selection.coverage_display_context_id
		JOIN app.workout_segment_matches match ON match.account_id=member.account_id
		JOIN app.path_segments segment ON segment.account_id=match.account_id AND segment.region_id=match.region_id
		AND segment.generation_id=match.generation_id AND segment.physical_segment_id=match.physical_segment_id
		JOIN app.workouts workout ON workout.account_id=match.account_id AND workout.id=match.workout_id
		JOIN app.workout_routes route ON route.account_id=workout.account_id AND route.workout_id=workout.id
		JOIN app.workout_coverage_states coverage ON coverage.account_id=workout.account_id AND coverage.workout_id=workout.id
		LEFT JOIN app.coverage_paths path ON path.account_id=match.account_id AND path.logical_path_id=match.logical_path_id
		WHERE selection.account_id=$1 AND selection.id=$3 AND member.canonical_id=$6
		AND workout.local_start_date BETWEEN selection.start_date AND selection.end_date
		AND workout.deletion_requested_at IS NULL AND route.route IS NOT NULL AND coverage.applied_route_input_revision IS NOT NULL
		AND ((member.source_kind='park' AND segment.tags->>'workouts:park_id'=member.source_id::text)
		 OR (member.source_kind='path' AND path.logical_path_id IS NOT NULL
		 AND member.source_id=COALESCE(NULLIF(segment.tags->>'workouts:education_id','')::uuid,match.logical_path_id)
		 AND NOT(segment.highway='service' AND COALESCE(segment.tags->>'service','') IN ('driveway','parking_aisle'))
		 AND segment.tags->>'amenity' IS DISTINCT FROM 'parking'
		 AND (path.name IS NOT NULL OR segment.tags->>'workouts:park_kind' IN ('state_park','national_park') OR NOT(segment.tags ? 'workouts:park_id'))))) available),
	expected_detail AS (SELECT actual.* FROM actual,geometry_available WHERE available),
	shipped AS MATERIALIZED (SELECT `+coverageEntityStatsColumns+` FROM app.map_selection_coverage_entity_detail($1,$2,$3,$4,$5,$6)),
	differences AS (
		SELECT 'list-only' direction,to_jsonb(d) value FROM (TABLE listed EXCEPT ALL TABLE actual) d UNION ALL
		SELECT 'stats-not-list',to_jsonb(d) FROM (TABLE actual EXCEPT ALL TABLE listed) d UNION ALL
		SELECT 'detail-only',to_jsonb(d) FROM (TABLE shipped EXCEPT ALL TABLE expected_detail) d UNION ALL
		SELECT 'missing-detail',to_jsonb(d) FROM (TABLE expected_detail EXCEPT ALL TABLE shipped) d
	) SELECT (SELECT COALESCE(jsonb_agg(to_jsonb(differences)),'[]'::jsonb)::text FROM differences),
		(SELECT COALESCE(jsonb_agg(to_jsonb(actual)),'[]'::jsonb)::text FROM actual)`,
		f.account, f.session, s.id, s.generation, kind, entity).Scan(&delta, &actual)
	_ = tx.Rollback(db.ctx)
	if err != nil || delta != "[]" {
		t.Fatalf("21-column canonical stats/detail/list parity kind=%v entity=%s: %s err=%v", kind, entity, delta, err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(actual), &rows); err != nil || len(rows) > 1 {
		t.Fatalf("stats cardinality/encoding: %s err=%v", actual, err)
	}
	if len(rows) == 1 && len(rows[0]) != 21 {
		t.Fatalf("stats must expose exactly 21 fields, got %d: %s", len(rows[0]), actual)
	}
	if exposed := coverageEntityStatsAPI(t, db, f.account, f.account, f.session, s.id, s.generation, kind, entity); exposed != actual {
		t.Fatalf("API stats changed the full row: got=%s want=%s", exposed, actual)
	}
}

func coverageEntityStatsAPI(t *testing.T, db testDatabases, account uuid.UUID, args ...any) string {
	t.Helper()
	if selection, ok := args[2].(uuid.UUID); ok {
		if entity, ok := args[5].(uuid.UUID); ok {
			args[5] = coverageDisplayCanonicalID(t, db, account, selection, args[4], entity)
		}
	}
	tx := beginAccount(t, db.ctx, db.api, account)
	defer tx.Rollback(db.ctx)
	if session, ok := args[1].(uuid.UUID); ok {
		coverageDisplayExec(t, db, tx, `SELECT set_config('app.session_id',$1,true)`, session.String())
	}
	var rows string
	if err := tx.QueryRow(db.ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(stats)),'[]'::jsonb)::text
		FROM app.map_selection_coverage_entity_stats($1,$2,$3,$4,$5,$6) stats`, args...).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	return rows
}

func coverageEntityStatsCheck(t *testing.T, db testDatabases, f coverageDisplayFixture, s coverageDisplaySelection, kind string, entity uuid.UUID, predicate string, args ...any) {
	t.Helper()
	values := append([]any{f.account, f.session, s.id, s.generation, kind, entity}, args...)
	values[5] = coverageDisplayCanonicalID(t, db, f.account, s.id, kind, entity)
	coverageDisplayCheck(t, db, `SELECT count(*)=1 AND bool_and(`+predicate+`) FROM app.map_selection_coverage_entity_stats($1,$2,$3,$4,$5,$6)`, values...)
}

func coverageEntityStatsSelection(t *testing.T, db testDatabases, f coverageDisplayFixture, generation int64, start, end string, checked []uuid.UUID) coverageDisplaySelection {
	t.Helper()
	s := coverageDisplaySelection{id: uuid.New(), generation: generation}
	tx := beginAccount(t, db.ctx, db.migration, f.account)
	var focus any
	if len(checked) > 0 {
		focus = checked[0]
	}
	coverageDisplayExec(t, db, tx, `INSERT INTO app.map_selections(id,account_id,session_id,generation,expires_at,start_date,end_date,selection_kind,focused_workout_id)
		VALUES($1,$2,$3,$4,transaction_timestamp()+interval '30 minutes',$5::date,$6::date,'explicit_subset',$7)`, s.id, f.account, f.session, generation, start, end, focus)
	for i, workout := range checked {
		coverageDisplayExec(t, db, tx, `INSERT INTO app.map_selection_workouts(account_id,selection_id,workout_id,sort_order) VALUES($1,$2,$3,$4)`, f.account, s.id, workout, i)
	}
	coverageDisplayCommit(t, db, tx)
	api := deferredBeginSelection(t, db, f.account, f.session)
	coverageDisplayExec(t, db, api, `SELECT * FROM app.prepare_coverage_display_context($1,$2,$3,$4)`, f.account, f.session, s.id, s.generation)
	coverageDisplayCommit(t, db, api)
	return s
}

func coverageEntityStatsClone(t *testing.T, db testDatabases, tx pgx.Tx, f coverageDisplayFixture, original uuid.UUID, tags string, offset float64) uuid.UUID {
	t.Helper()
	segment := uuid.New()
	coverageDisplayExec(t, db, tx, `INSERT INTO app.path_segments(account_id,region_id,generation_id,physical_segment_id,
		logical_path_id,derivation_version,locality_relation_id,source_way_id,source_way_version,name,normalized_name,
		highway,broad_class,tags,segment_meters,geom)
		SELECT account_id,region_id,generation_id,$3,logical_path_id,derivation_version,locality_relation_id,source_way_id+1000,
		source_way_version,name,normalized_name,highway,broad_class,tags || $4::jsonb,segment_meters,ST_Translate(geom,$5,0)
		FROM app.path_segments WHERE account_id=$1 AND physical_segment_id=$2`, f.account, original, segment, tags, offset)
	return segment
}

func coverageEntityStatsAddWorkouts(t *testing.T, db testDatabases, f coverageDisplayFixture, count int) []uuid.UUID {
	t.Helper()
	tx := beginAccount(t, db.ctx, db.migration, f.account)
	coverageDisplayFence(t, db, tx, f)
	workouts := make([]uuid.UUID, count)
	for i := range workouts {
		workouts[i] = uuid.Must(uuid.NewV7())
		coverageDisplayExec(t, db, tx, `INSERT INTO app.workouts(id,account_id,source_id,source_file_id,workout_type_id,
			provider_id,content_sha256,provider_label,local_start_date,started_at,ended_at,provider_duration)
			SELECT $3,account_id,source_id,source_file_id,workout_type_id,$3::uuid::text,content_sha256,
			provider_label,local_start_date,started_at,ended_at,provider_duration
			FROM app.workouts WHERE account_id=$1 AND id=$2`, f.account, f.workouts[0], workouts[i])
		coverageDisplayExec(t, db, tx, `INSERT INTO app.workout_route_points(account_id,workout_id,sequence,recorded_at,latitude,longitude)
			SELECT account_id,$3,sequence,recorded_at,latitude,longitude FROM app.workout_route_points
			WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0], workouts[i])
		coverageDisplayExec(t, db, tx, `INSERT INTO app.workout_routes(account_id,workout_id,point_count,
			minimum_longitude,minimum_latitude,maximum_longitude,maximum_latitude,has_complete_altitude,route)
			SELECT account_id,$3,point_count,minimum_longitude,minimum_latitude,maximum_longitude,maximum_latitude,
			has_complete_altitude,route FROM app.workout_routes WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0], workouts[i])
		coverageDisplayExec(t, db, tx, `INSERT INTO app.workout_coverage_states(account_id,workout_id,route_input_revision,
			route_input_sha256,readiness_state,map_data_ready_at,processing_state,applied_route_input_revision,
			applied_route_input_sha256,applied_rules_version,applied_sampling_version,applied_path_policy_version,
			applied_generations,processing_finished_at)
			SELECT account_id,$3,route_input_revision,route_input_sha256,readiness_state,map_data_ready_at,processing_state,
			applied_route_input_revision,applied_route_input_sha256,applied_rules_version,applied_sampling_version,
			applied_path_policy_version,applied_generations,processing_finished_at
			FROM app.workout_coverage_states WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0], workouts[i])
	}
	coverageDisplayCommit(t, db, tx)
	return workouts
}

func coverageEntityStatsReturnQuery(t *testing.T, body string) string {
	t.Helper()
	_, query, found := strings.Cut(body, "RETURN QUERY ")
	query = strings.TrimSpace(query)
	if !found || !strings.HasSuffix(query, "END;") {
		t.Fatal("stats must expose one SQL RETURN QUERY for nested-plan regression")
	}
	return strings.TrimSuffix(strings.TrimSpace(strings.TrimSuffix(query, "END;")), ";")
}

func coverageEntityStatsExplain(t *testing.T, db testDatabases, f coverageDisplayFixture, s coverageDisplaySelection, kind string, entity uuid.UUID, wantCandidates, wantAccount int64) {
	t.Helper()
	tx := beginAccount(t, db.ctx, db.migration, f.account)
	defer tx.Rollback(db.ctx)
	var body, owner string
	if err := tx.QueryRow(db.ctx, `SELECT prosrc,pg_get_userbyid(proowner) FROM pg_proc
		WHERE oid='app.map_selection_coverage_entity_stats(uuid,uuid,uuid,bigint,text,uuid)'::regprocedure
		AND prosecdef AND provolatile='s'`).Scan(&body, &owner); err != nil {
		t.Fatal(err)
	}
	if owner != "workouts_security_owner" || compactSQL(body) != compactSQL(coverageDisplayFunction(t, coverageDisplaySQL(t), "map_selection_coverage_entity_stats")) {
		t.Fatal("nested-plan regression requires a fresh fixture matching the embedded stats function and its owner")
	}
	// This role is already inherited by the restricted migrator. Reproduce the
	// function's execution context without auto_explain, grants or global settings.
	coverageDisplayExec(t, db, tx, `SET LOCAL ROLE workouts_security_owner`)
	coverageDisplayExec(t, db, tx, `SET LOCAL search_path=pg_catalog,app`)
	coverageDisplayExec(t, db, tx, `SET LOCAL jit=off`)
	var context uuid.UUID
	if err := tx.QueryRow(db.ctx, `SELECT coverage_display_context_id FROM app.map_selections
		WHERE account_id=$1 AND session_id=$2 AND id=$3 AND generation=$4 AND expires_at>transaction_timestamp()`,
		f.account, f.session, s.id, s.generation).Scan(&context); err != nil {
		t.Fatal(err)
	}
	if kind != "path" && kind != "park" {
		t.Fatal("plan literals require a validated entity kind")
	}
	canonical := coverageDisplayCanonicalID(t, db, f.account, s.id, kind, entity)
	query := strings.NewReplacer("target_account_id", "'"+f.account.String()+"'::uuid",
		"target_session_id", "'"+f.session.String()+"'::uuid", "target_selection_id", "'"+s.id.String()+"'::uuid",
		"target_generation", fmt.Sprintf("%d::bigint", s.generation), "target_entity_kind", "'"+kind+"'::text",
		"target_entity_id", "'"+canonical.String()+"'::uuid", "authorized_id", "'"+context.String()+"'::uuid").Replace(coverageEntityStatsReturnQuery(t, body))
	var candidates, account int64
	if err := tx.QueryRow(db.ctx, `WITH candidate AS (
		SELECT workout_id FROM app.workout_path_display_partials WHERE account_id=$1 AND entity_id=$3 AND $2='path'
		UNION SELECT workout_id FROM app.workout_path_display_context WHERE account_id=$1 AND entity_id=$3 AND $2='path'
		UNION SELECT workout_id FROM app.workout_park_attributions WHERE account_id=$1 AND park_id=$3 AND $2='park'
		UNION SELECT workout_id FROM app.workout_park_display_bounds WHERE account_id=$1 AND park_id=$3 AND $2='park'
	), all_eligible AS (
		SELECT workout.id FROM app.workouts workout
		JOIN app.workout_routes route ON route.account_id=workout.account_id AND route.workout_id=workout.id
		JOIN app.workout_coverage_states coverage ON coverage.account_id=workout.account_id AND coverage.workout_id=workout.id
		WHERE workout.account_id=$1 AND workout.deletion_requested_at IS NULL AND workout.local_start_date IS NOT NULL
		AND route.route IS NOT NULL AND coverage.applied_route_input_revision IS NOT NULL
	) SELECT (SELECT count(*) FROM all_eligible JOIN candidate ON candidate.workout_id=all_eligible.id),
		(SELECT count(*) FROM all_eligible)`, f.account, kind, entity).Scan(&candidates, &account); err != nil {
		t.Fatal(err)
	}
	if candidates != wantCandidates || account != wantAccount || candidates >= account {
		t.Fatalf("scaling fixture lost target/account separation: candidates=%d want=%d account=%d want=%d", candidates, wantCandidates, account, wantAccount)
	}
	var inline string
	if err := tx.QueryRow(db.ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(stats)),'[]'::jsonb)::text FROM (`+query+`) stats`).Scan(&inline); err != nil {
		t.Fatal(err)
	}
	if actual := coverageEntityStatsAPI(t, db, f.account, f.account, f.session, s.id, s.generation, kind, entity); actual == "[]" || inline != actual {
		t.Fatalf("extracted SQL did not reproduce actual API stats: inline=%s actual=%s", inline, actual)
	}
	var plan []byte
	if err := tx.QueryRow(db.ctx, `EXPLAIN (ANALYZE, FORMAT JSON, TIMING OFF, SUMMARY OFF) `+query).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	rows, loops := coverageEntityStatsEntityPlanRows(t, plan)
	if loops != 1 || rows != 1 || strings.Contains(string(plan), `"Relation Name": "workouts"`) || strings.Contains(string(plan), `"Relation Name": "workout_segment_matches"`) {
		t.Fatalf("warm canonical stats must read exactly one cached entity without rescanning workouts: rows=%d loops=%d plan=%s", rows, loops, plan)
	}
	t.Logf("%s target: cached rows=%d source candidates=%d account eligible=%d", kind, rows, candidates, account)
}

func coverageEntityStatsEntityPlanRows(t *testing.T, encoded []byte) (int64, int64) {
	t.Helper()
	type node struct {
		Relation string      `json:"Relation Name"`
		Rows     json.Number `json:"Actual Rows"`
		Loops    json.Number `json:"Actual Loops"`
		Plans    []node      `json:"Plans"`
	}
	var document []struct {
		Plan node `json:"Plan"`
	}
	if err := json.Unmarshal(encoded, &document); err != nil || len(document) != 1 {
		t.Fatalf("invalid nested EXPLAIN JSON: %s err=%v", encoded, err)
	}
	var rows, loops int64
	found := 0
	var visit func(node)
	visit = func(n node) {
		if n.Relation == "coverage_display_entities" {
			var err error
			if rows, err = coverageEntityStatsPlanCardinality(n.Rows); err != nil {
				t.Fatal(err)
			}
			if loops, err = coverageEntityStatsPlanCardinality(n.Loops); err != nil {
				t.Fatal(err)
			}
			found++
		}
		for _, child := range n.Plans {
			visit(child)
		}
	}
	visit(document[0].Plan)
	if found != 1 {
		t.Fatalf("expected one cached canonical entity probe, found=%d: %s", found, encoded)
	}
	return rows, loops
}

func coverageEntityStatsPlanCardinality(value json.Number) (int64, error) {
	count, ok := new(big.Rat).SetString(value.String())
	if !ok || count.Sign() < 0 || !count.IsInt() || !count.Num().IsInt64() {
		return 0, fmt.Errorf("plan cardinality must be a nonnegative integral int64: %q", value)
	}
	return count.Num().Int64(), nil
}
