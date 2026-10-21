package database

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Requires an already migrated, explicitly disposable scratch database. All faults roll back.
func TestAdoptionReadinessIntegration(t *testing.T) {
	dsn := os.Getenv("PROJECT_ADOPTION_READINESS_TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("PROJECT_ADOPTION_READINESS_TEST_DISPOSABLE") != "1" {
		t.Skip("PROJECT_ADOPTION_READINESS_TEST_DATABASE_URL and PROJECT_ADOPTION_READINESS_TEST_DISPOSABLE=1 are required")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid scratch adoption readiness configuration")
	}
	if !strings.HasPrefix(config.ConnConfig.Database, "workouts_adoption_readiness_test_") ||
		(config.ConnConfig.Host != "127.0.0.1" && config.ConnConfig.Host != "localhost" && config.ConnConfig.Host != "::1") {
		t.Fatal("adoption readiness requires a named loopback scratch database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	config.MaxConns = 1
	config.ConnConfig.RuntimeParams["statement_timeout"] = "5s"
	admin, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("create scratch adoption readiness pool")
	}
	defer admin.Close()
	check := func(t *testing.T, fault string, wantReady bool) {
		t.Helper()
		for _, role := range []string{"workouts_api", "workouts_worker", "workouts_coverage_worker"} {
			// A missing catalog object may abort the query, just as Ready returns false on query errors.
			func() {
				tx, err := admin.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(context.Background())
				if fault != "" {
					if _, err := tx.Exec(ctx, fault); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+role); err != nil {
					t.Fatal(err)
				}
				var ready bool
				err = tx.QueryRow(ctx, runtimeReadinessSQL, SupportedSchemaVersion).Scan(&ready)
				if (err == nil && ready) != wantReady {
					t.Fatalf("readiness=%t err=%v want=%t role=%s fault=%s", ready, err, wantReady, role, fault)
				}
			}()
		}
	}
	check(t, "", true)
	t.Run("compatible newer schema", func(t *testing.T) {
		check(t, fmt.Sprintf(`UPDATE app.schema_metadata SET schema_version=%d,minimum_runtime_version=28 WHERE singleton`, SupportedSchemaVersion+1), true)
	})
	faults := map[string]string{
		"newer minimum runtime": fmt.Sprintf(`UPDATE app.schema_metadata SET schema_version=%d,minimum_runtime_version=%d WHERE singleton`, SupportedSchemaVersion+1, SupportedSchemaVersion+1),
		"older schema":          `UPDATE app.schema_metadata SET schema_version=25,minimum_runtime_version=25 WHERE singleton`,
		"old coverage fence": `DO $$ BEGIN EXECUTE replace(pg_get_functiondef('app.claim_next_coverage_route(text,uuid,interval,integer)'::regprocedure),
			'runtime_version IS NULL OR runtime_version < 26','runtime_version IS NULL OR runtime_version < 23'); END $$`,
		"unversioned fence not rejected": `DO $$ BEGIN EXECUTE replace(pg_get_functiondef('app.fence_ingest_job(uuid,text,uuid)'::regprocedure),
			'55000','42501'); END $$`,
		"old ingest fence": `DO $$ BEGIN EXECUTE replace(pg_get_functiondef('app.fence_ingest_job(uuid,text,uuid,integer)'::regprocedure),
			'runtime_version IS NULL OR runtime_version < 26','runtime_version IS NULL OR runtime_version < 23'); END $$`,
		"missing metadata fence": `DO $$ BEGIN EXECUTE replace(pg_get_functiondef('app.fence_ingest_job(uuid,text,uuid,integer)'::regprocedure),
			'minimum_runtime_version <= runtime_version','true'); END $$`,
		"mutable namespace":                `ALTER FUNCTION app.workout_provider_namespace(text) VOLATILE`,
		"nullable outbox event":            `ALTER TABLE app.adopted_workout_coverage_refreshes ALTER COLUMN event_id DROP NOT NULL`,
		"missing outbox event default":     `ALTER TABLE app.adopted_workout_coverage_refreshes ALTER COLUMN event_id DROP DEFAULT`,
		"missing outbox requested default": `ALTER TABLE app.adopted_workout_coverage_refreshes ALTER COLUMN requested_at DROP DEFAULT`,
		"missing outbox due default":       `ALTER TABLE app.adopted_workout_coverage_refreshes ALTER COLUMN next_attempt_at DROP DEFAULT`,
		"wrong epoch type":                 `ALTER TABLE app.source_objects ALTER COLUMN ownership_blocked_epoch TYPE numeric`,
		"disabled ownership trigger":       `ALTER TABLE app.sources DISABLE TRIGGER sources_ownership_epoch_after_delete`,
		"disabled namespace trigger":       `ALTER TABLE app.workouts DISABLE TRIGGER workouts_namespace_before_insert`,
		"missing adoption cascade":         `ALTER TABLE app.workout_adoption_capabilities DROP CONSTRAINT workout_adoption_capabilities_backend_pid_transaction_id_fkey`,
		"missing outbox cascade":           `ALTER TABLE app.adopted_workout_coverage_refreshes DROP CONSTRAINT adopted_workout_coverage_refreshes_workout_id_account_id_fkey`,
	}
	for _, function := range adoptionReadinessFunctions {
		signature := function.signature
		faults[signature+" missing"] = "ALTER FUNCTION " + signature + " RENAME TO adoption_readiness_missing_function"
		faults[signature+" wrong owner"] = "ALTER FUNCTION " + signature + " OWNER TO workouts_migration"
		security := "SECURITY INVOKER"
		if !function.definer {
			security = "SECURITY DEFINER"
		}
		faults[signature+" wrong security"] = "ALTER FUNCTION " + signature + " " + security
		faults[signature+" unsafe path"] = "ALTER FUNCTION " + signature + " SET search_path=pg_catalog,app,public"
		volatility := "STABLE"
		if function.volatility == "s" {
			volatility = "VOLATILE"
			faults[signature+" immutable"] = "ALTER FUNCTION " + signature + " IMMUTABLE"
		}
		faults[signature+" wrong volatility"] = "ALTER FUNCTION " + signature + " " + volatility
		for _, role := range []string{"PUBLIC", "workouts_api", "workouts_tiles", "workouts_worker", "workouts_coverage_worker"} {
			allowed := role == "workouts_worker" && function.worker || role == "workouts_coverage_worker" && function.coverage || role == "workouts_api" && function.api
			if allowed {
				faults[signature+" missing "+role] = "REVOKE EXECUTE ON FUNCTION " + signature + " FROM " + role
				faults[signature+" grant option "+role] = "GRANT EXECUTE ON FUNCTION " + signature + " TO " + role + " WITH GRANT OPTION"
			} else {
				faults[signature+" exposed "+role] = "GRANT EXECUTE ON FUNCTION " + signature + " TO " + role
			}
		}
	}
	for relation, columns := range map[string][]string{
		"app.accounts": {"workout_ownership_epoch"}, "app.ingest_write_capabilities": {"ownership_epoch", "runtime_version"},
		"app.workouts": {"provider_namespace"}, "app.workout_deletion_targets": {"provider_namespace"},
		"app.source_objects":                     {"ownership_blocked_epoch"},
		"app.workout_adoption_capabilities":      {"backend_pid", "transaction_id", "account_id", "workout_id", "old_source_id", "new_source_id", "source_file_id", "job_id"},
		"app.adopted_workout_coverage_refreshes": {"workout_id", "account_id", "event_id", "source_id", "source_file_id", "requested_at", "next_attempt_at"},
	} {
		for _, column := range columns {
			faults[relation+" missing "+column] = "ALTER TABLE " + relation + " RENAME COLUMN " + column + " TO adoption_readiness_missing_column"
		}
	}
	for _, index := range []string{
		"app.workouts_account_provider_identity_idx", "app.workout_deletion_targets_provider_namespace_idx",
		"app.workout_import_events_ownership_blocked_file_idx", "app.workout_adoption_capabilities_pkey",
		"app.adopted_workout_coverage_refreshes_pkey", "app.adopted_workout_coverage_refreshes_due_idx",
	} {
		faults[index+" missing"] = "ALTER INDEX " + index + " RENAME TO adoption_readiness_missing_index"
	}
	for _, table := range []string{"app.workout_adoption_capabilities", "app.adopted_workout_coverage_refreshes"} {
		faults[table+" missing"] = "ALTER TABLE " + table + " RENAME TO adoption_readiness_missing_table"
		faults[table+" wrong owner"] = "ALTER TABLE " + table + " OWNER TO workouts_migration"
		faults[table+" disabled RLS"] = "ALTER TABLE " + table + " DISABLE ROW LEVEL SECURITY"
		faults[table+" disabled force RLS"] = "ALTER TABLE " + table + " NO FORCE ROW LEVEL SECURITY"
		for _, privilege := range []string{"SELECT", "INSERT", "UPDATE", "DELETE"} {
			faults[table+" missing owner "+privilege] = "REVOKE " + privilege + " ON " + table + " FROM workouts_security_owner"
		}
		for _, role := range []string{"PUBLIC", "workouts_api", "workouts_worker", "workouts_coverage_worker", "workouts_tiles"} {
			faults[table+" direct read "+role] = "GRANT SELECT ON " + table + " TO " + role
			faults[table+" column read "+role] = "GRANT SELECT(workout_id) ON " + table + " TO " + role
			faults[table+" direct write "+role] = "GRANT INSERT ON " + table + " TO " + role
		}
	}
	for name, fault := range faults {
		t.Run(name, func(t *testing.T) { check(t, fault, false) })
	}
}
