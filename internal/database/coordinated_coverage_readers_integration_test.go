package database

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Run after migration verification, against its fully migrated disposable
// loopback scratch database. Every readiness fault is rolled back.
func TestCoordinatedCoverageReadersReadinessIntegration(t *testing.T) {
	dsn := os.Getenv("PROJECT_COVERAGE_READERS_READINESS_TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("PROJECT_COVERAGE_READERS_TEST_DISPOSABLE") != "1" {
		t.Skip("PROJECT_COVERAGE_READERS_READINESS_TEST_DATABASE_URL and PROJECT_COVERAGE_READERS_TEST_DISPOSABLE=1 are required")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid scratch reader readiness configuration")
	}
	if !strings.HasPrefix(config.ConnConfig.Database, "workouts_coverage_readers_test_") ||
		(config.ConnConfig.Host != "127.0.0.1" && config.ConnConfig.Host != "localhost" && config.ConnConfig.Host != "::1") {
		t.Fatal("reader readiness requires a named loopback scratch database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	config.MaxConns = 1
	admin, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("create scratch reader readiness pool")
	}
	defer admin.Close()
	roles := []string{"workouts_api", "workouts_worker", "workouts_coverage_worker"}
	check := func(t *testing.T, fault string, wantReady bool) {
		t.Helper()
		tx, err := admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(context.Background())
		if _, err := tx.Exec(ctx, `SET LOCAL ROLE workouts_migration`); err != nil {
			t.Fatal(err)
		}
		if fault != "" {
			if _, err := tx.Exec(ctx, fault); err != nil {
				t.Fatal(err)
			}
		}
		for _, role := range roles {
			if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+role); err != nil {
				t.Fatal(err)
			}
			var ready bool
			if err := tx.QueryRow(ctx, runtimeReadinessSQL, SupportedSchemaVersion).Scan(&ready); err != nil {
				t.Fatalf("readiness query for %s: %v", role, err)
			}
			if ready != wantReady {
				t.Fatalf("readiness=%t want=%t role=%s fault=%s", ready, wantReady, role, fault)
			}
		}
	}
	check(t, "", true)
	faults := map[string]string{
		"missing active count index": `ALTER INDEX app.matcher_slots_reader_active_idx RENAME TO readiness_missing_reader_index`,
		"missing cluster column":     `ALTER TABLE app.matcher_slots RENAME COLUMN reader_cluster_key TO readiness_missing_cluster`,
		"missing backend column":     `ALTER TABLE app.matcher_slots RENAME COLUMN reader_backend_key TO readiness_missing_backend`,
		"missing reader limit":       `ALTER TABLE app.matcher_slot_limits RENAME COLUMN reader_limit TO readiness_missing_limit`,
		"disabled force RLS":         `ALTER TABLE app.matcher_slots NO FORCE ROW LEVEL SECURITY`,
		"old coverage runtime": `CREATE OR REPLACE FUNCTION app.claim_next_coverage_route(claiming_worker text,new_lease_token uuid,lease_duration interval,runtime_version integer)
			RETURNS TABLE(job_id uuid,account_id uuid,parent_job_id uuid,workout_id uuid,route_input_revision bigint,route_input_sha256 bytea,
			 rules_version text,sampling_version text,path_policy_version text,minimum_traversal_meters double precision,target_generations jsonb)
			LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app AS $$ BEGIN IF runtime_version < 22 THEN RAISE EXCEPTION 'old runtime'; END IF; RETURN; END $$`,
	}
	for _, signature := range []string{"app.assign_matcher_reader(uuid,text,uuid,text,text[])", "app.clear_matcher_reader(uuid,text,uuid)"} {
		faults[signature+" unsafe owner"] = "ALTER FUNCTION " + signature + " OWNER TO workouts_migration"
		faults[signature+" invoker"] = "ALTER FUNCTION " + signature + " SECURITY INVOKER"
		faults[signature+" unsafe path"] = "ALTER FUNCTION " + signature + " SET search_path=pg_catalog,app,public"
		for _, role := range []string{"workouts_api", "workouts_coverage_worker"} {
			faults[signature+" missing "+role] = "REVOKE EXECUTE ON FUNCTION " + signature + " FROM " + role
		}
		for _, role := range []string{"PUBLIC", "workouts_worker", "workouts_tiles"} {
			faults[signature+" exposed to "+role] = "GRANT EXECUTE ON FUNCTION " + signature + " TO " + role
		}
	}
	for _, role := range []string{"workouts_api", "workouts_worker", "workouts_coverage_worker", "workouts_tiles"} {
		faults[role+" direct table read"] = "GRANT SELECT ON app.matcher_slots TO " + role
		faults[role+" direct opaque column read"] = "GRANT SELECT(reader_backend_key) ON app.matcher_slots TO " + role
	}
	heartbeat := "app.heartbeat_diagnostic_matcher_slot(uuid,text,uuid,interval)"
	faults[heartbeat+" unsafe owner"] = "ALTER FUNCTION " + heartbeat + " OWNER TO workouts_migration"
	faults[heartbeat+" invoker"] = "ALTER FUNCTION " + heartbeat + " SECURITY INVOKER"
	faults[heartbeat+" unsafe path"] = "ALTER FUNCTION " + heartbeat + " SET search_path=pg_catalog,app,public"
	faults[heartbeat+" missing API grant"] = "REVOKE EXECUTE ON FUNCTION " + heartbeat + " FROM workouts_api"
	for _, role := range []string{"PUBLIC", "workouts_worker", "workouts_coverage_worker", "workouts_tiles"} {
		faults[heartbeat+" exposed to "+role] = "GRANT EXECUTE ON FUNCTION " + heartbeat + " TO " + role
	}
	claim := "app.claim_next_coverage_route(text,uuid,interval,integer)"
	faults[claim+" unsafe owner"] = "ALTER FUNCTION " + claim + " OWNER TO workouts_migration"
	faults[claim+" invoker"] = "ALTER FUNCTION " + claim + " SECURITY INVOKER"
	faults[claim+" unsafe path"] = "ALTER FUNCTION " + claim + " SET search_path=pg_catalog,app,public"
	faults[claim+" missing coverage grant"] = "REVOKE EXECUTE ON FUNCTION " + claim + " FROM workouts_coverage_worker"
	for _, role := range []string{"PUBLIC", "workouts_api", "workouts_worker", "workouts_tiles"} {
		faults[claim+" exposed to "+role] = "GRANT EXECUTE ON FUNCTION " + claim + " TO " + role
	}
	for _, signature := range []string{
		"app.persist_coverage_route(uuid,text,uuid,integer,jsonb,jsonb)",
		"app.finish_coverage_route(uuid,text,uuid,text,integer,text,text)",
	} {
		for _, fence := range []string{"job.cancel_requested_at IS NULL", "parent.cancel_requested_at IS NULL"} {
			faults[signature+" missing "+fence] = `DO $$ BEGIN EXECUTE replace(pg_get_functiondef('` + signature + `'::regprocedure),'` + fence + `','true'); END $$`
		}
	}
	cancellation := "app.job_cancel_requested(uuid,text,uuid)"
	faults[cancellation+" unsafe owner"] = "ALTER FUNCTION " + cancellation + " OWNER TO workouts_migration"
	faults[cancellation+" invoker"] = "ALTER FUNCTION " + cancellation + " SECURITY INVOKER"
	faults[cancellation+" unsafe path"] = "ALTER FUNCTION " + cancellation + " SET search_path=pg_catalog,app,public"
	faults[cancellation+" volatile"] = "ALTER FUNCTION " + cancellation + " VOLATILE"
	faults[cancellation+" missing coverage grant"] = "REVOKE EXECUTE ON FUNCTION " + cancellation + " FROM workouts_coverage_worker"
	for _, role := range []string{"PUBLIC", "workouts_api", "workouts_worker", "workouts_tiles"} {
		faults[cancellation+" exposed to "+role] = "GRANT EXECUTE ON FUNCTION " + cancellation + " TO " + role
	}
	for name, fault := range faults {
		t.Run(name, func(t *testing.T) { check(t, fault, false) })
	}
}
