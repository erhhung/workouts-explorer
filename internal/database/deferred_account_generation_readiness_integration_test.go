package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestDeferredGenerationReadinessPostgreSQL(t *testing.T) {
	dsn := os.Getenv("PROJECT_DEFERRED_GENERATION_DATABASE_URL")
	if dsn == "" || os.Getenv("PROJECT_DEFERRED_GENERATION_DISPOSABLE") != "1" {
		t.Skip("explicit disposable deferred-generation cluster required")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil || (config.Host != "127.0.0.1" && config.Host != "::1") {
		t.Fatal("deferred readiness requires an explicit loopback admin endpoint")
	}
	for _, fallback := range config.Fallbacks {
		if fallback.Host != config.Host || fallback.Port != config.Port {
			t.Fatal("fallback endpoints prohibited")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	// The test owns only this synthetic database, never the caller's database.
	name := fmt.Sprintf("workouts_deferred_readiness_test_%d", os.Getpid())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name+" OWNER workouts_migration"); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := admin.Exec(context.Background(), "DROP DATABASE "+name); err != nil {
			t.Error(err)
		}
	}()
	config.Database = name
	bootstrap, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer bootstrap.Close(context.Background())
	if _, err := bootstrap.Exec(ctx, `CREATE EXTENSION postgis`); err != nil {
		t.Fatal(err)
	}
	config.User, config.Password = "workouts_migration", ""
	delete(config.RuntimeParams, "role")
	sqlDB := stdlib.OpenDB(*config)
	defer sqlDB.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 27); err != nil {
		t.Fatal(err)
	}
	raw, err := migrations.Files.ReadFile("028_ingest_coverage_pipeline.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(raw), "-- +goose Down")
	var original string
	if err := bootstrap.QueryRow(ctx, `SELECT pg_get_functiondef('app.fence_ingest_job(uuid,text,uuid,integer)'::regprocedure)`).Scan(&original); err != nil {
		t.Fatal(err)
	}
	const locking = "PERFORM 1 FROM app.account_data_generations WHERE account_id = target_account FOR UPDATE NOWAIT;"
	const nonlocking = "PERFORM 1 FROM app.account_data_generations WHERE account_id = target_account;"
	const errorBlock = "IF NOT FOUND THEN RAISE EXCEPTION 'missing account data generation' USING ERRCODE = '55000'; END IF;"
	const comment1 = "-- All type/header/route writes advance this row. Never wait on a Coverage writer"
	const comment2 = "-- while holding source/job locks; its workout FK lock is likewise NOWAIT below."
	commentFree := strings.ReplaceAll(strings.ReplaceAll(original, comment1, ""), comment2, "")
	for name, variant := range map[string]struct {
		definition, statement string
		fail                  bool
	}{
		"shipped27":    {original, locking, false},
		"comment free": {commentFree, locking, false},
		"mixed case": {strings.Replace(commentFree, locking, "pErFoRm 1 fRoM APP . account_data_generations wHeRe account_id=target_account fOr uPdAtE nOwAiT ;", 1),
			"pErFoRm 1 fRoM APP . account_data_generations wHeRe account_id=target_account fOr uPdAtE nOwAiT ;", false},
		"compact": {strings.Replace(strings.Replace(commentFree, locking, "perform 1 from app.account_data_generations where account_id=target_account for update nowait;", 1),
			errorBlock, "if not found then raise exception 'missing account data generation' using errcode='55000';end if;", 1),
			"perform 1 from app.account_data_generations where account_id=target_account for update nowait;", false},
		"zero anchors":      {strings.Replace(original, locking, nonlocking, 1), "", true},
		"duplicate anchors": {strings.Replace(original, locking, locking+"\n"+locking, 1), "", true},
	} {
		t.Run("Canonical28Upgrade/"+name, func(t *testing.T) {
			tx, err := bootstrap.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if _, err := tx.Exec(ctx, variant.definition); err != nil {
				t.Fatal(err)
			}
			_, err = tx.Exec(ctx, up)
			if variant.fail {
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || pgErr.Code != "P0001" || !strings.Contains(pgErr.Message, "anchor must occur exactly once") {
					t.Fatalf("missing/duplicate structural anchor did not fail closed: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				want := strings.Replace(variant.definition, variant.statement, nonlocking, 1)
				want = strings.ReplaceAll(want, comment1, "-- Generation advances are deferred until commit; this existence check takes no row lock.")
				want = strings.ReplaceAll(want, comment2, "-- Source/job and workout FK locks remain unchanged.")
				var got string
				if err := tx.QueryRow(ctx, `SELECT pg_get_functiondef('app.fence_ingest_job(uuid,text,uuid,integer)'::regprocedure)`).Scan(&got); err != nil || got != want {
					t.Fatalf("folded generation patch changed bytes outside its statement and optional documentation: %v", err)
				}
				// Model Goose's final ledger insert in this rollback-only migration
				// transaction, so readiness sees the complete canonical 028 contract.
				if _, err := tx.Exec(ctx, `INSERT INTO public.goose_db_version(version_id,is_applied) VALUES(28,true)`); err != nil {
					t.Fatal(err)
				}
				for _, role := range []string{"workouts_api", "workouts_worker", "workouts_coverage_worker"} {
					if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+role); err != nil {
						t.Fatal(err)
					}
					var ready bool
					if err := tx.QueryRow(ctx, runtimeFullReadinessSQL, 28).Scan(&ready); err != nil || !ready {
						t.Fatalf("formatted fence readiness %s: %t %v", role, ready, err)
					}
				}
			}
			if err := tx.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			var intact bool
			if err := bootstrap.QueryRow(ctx, `SELECT
				(SELECT schema_version=27 AND minimum_runtime_version=26 AND migration_baseline=27 FROM app.schema_metadata WHERE singleton)
				AND (SELECT count(*)=28 AND max(version_id)=27 AND bool_and(is_applied) FROM public.goose_db_version)
				AND to_regclass('app.ingest_coverage_builders') IS NULL AND to_regclass('app.ingest_coverage_prepared') IS NULL
				AND NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgfoid='app.advance_account_data_generation()'::regprocedure AND tgdeferrable)
				AND pg_get_functiondef('app.fence_ingest_job(uuid,text,uuid,integer)'::regprocedure)=$1`, original).Scan(&intact); err != nil || !intact {
				t.Fatalf("failed/rolled-back migration changed immutable27 ledger/metadata/triggers/fence: %t %v", intact, err)
			}
		})
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatal(err)
	}
	mutations := map[string]string{
		"disabled route trigger": `ALTER TABLE app.workout_routes DISABLE TRIGGER workout_routes_data_generation_after_write`,
		"replica route trigger":  `ALTER TABLE app.workout_routes ENABLE REPLICA TRIGGER workout_routes_data_generation_after_write`,
		"immediate same name": `DROP TRIGGER workouts_data_generation_after_write ON app.workouts;
			CREATE TRIGGER workouts_data_generation_after_write AFTER INSERT OR UPDATE OR DELETE ON app.workouts
			FOR EACH ROW EXECUTE FUNCTION app.advance_account_data_generation()`,
		"initially immediate": `DROP TRIGGER workouts_data_generation_after_write ON app.workouts;
			CREATE CONSTRAINT TRIGGER workouts_data_generation_after_write AFTER INSERT OR UPDATE OR DELETE ON app.workouts
			DEFERRABLE INITIALLY IMMEDIATE FOR EACH ROW EXECUTE FUNCTION app.advance_account_data_generation()`,
		"route event narrowed": `DROP TRIGGER workout_routes_data_generation_after_write ON app.workout_routes;
			CREATE CONSTRAINT TRIGGER workout_routes_data_generation_after_write AFTER UPDATE ON app.workout_routes
			DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION app.advance_account_data_generation()`,
		"type columns broadened": `DROP TRIGGER workout_types_data_generation_after_update ON app.workout_types;
			CREATE CONSTRAINT TRIGGER workout_types_data_generation_after_update AFTER UPDATE ON app.workout_types
			DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION app.advance_account_data_generation()`,
		"type columns narrowed": `DROP TRIGGER workout_types_data_generation_after_update ON app.workout_types;
			CREATE CONSTRAINT TRIGGER workout_types_data_generation_after_update AFTER UPDATE OF type_key ON app.workout_types
			DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION app.advance_account_data_generation()`,
		"coverage IUD broadened": `DROP TRIGGER workout_coverage_states_data_generation_after_current ON app.workout_coverage_states;
			CREATE CONSTRAINT TRIGGER workout_coverage_states_data_generation_after_current AFTER INSERT OR UPDATE OR DELETE ON app.workout_coverage_states
			DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION app.advance_account_data_generation()`,
		"coverage WHEN broadened": `DROP TRIGGER workout_coverage_states_data_generation_after_current ON app.workout_coverage_states;
			CREATE CONSTRAINT TRIGGER workout_coverage_states_data_generation_after_current AFTER UPDATE OF processing_finished_at ON app.workout_coverage_states
			DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (NEW.processing_state = 'current')
			EXECUTE FUNCTION app.advance_account_data_generation()`,
		"route WHEN added": `DROP TRIGGER workout_routes_data_generation_after_write ON app.workout_routes;
			CREATE CONSTRAINT TRIGGER workout_routes_data_generation_after_write AFTER INSERT OR UPDATE OR DELETE ON app.workout_routes
			DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (app.current_account_id() IS NOT NULL)
			EXECUTE FUNCTION app.advance_account_data_generation()`,
		"wrong trigger function": `DROP TRIGGER workouts_data_generation_after_write ON app.workouts;
			CREATE CONSTRAINT TRIGGER workouts_data_generation_after_write AFTER INSERT OR UPDATE OR DELETE ON app.workouts
			DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION app.seed_account_data_generation()`,
		"extra immediate invalidator": `CREATE TRIGGER extra_generation AFTER UPDATE ON app.workouts
			FOR EACH ROW EXECUTE FUNCTION app.advance_account_data_generation()`,
		"deferred account seed": `DROP TRIGGER accounts_data_generation_after_insert ON app.accounts;
			CREATE CONSTRAINT TRIGGER accounts_data_generation_after_insert AFTER INSERT ON app.accounts
			DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION app.seed_account_data_generation()`,
		"counter body": `DO $block$ BEGIN EXECUTE replace(pg_get_functiondef('app.advance_account_data_generation()'::regprocedure),
			'generation + 1','generation + 2'); END $block$`,
		"counter invoker": `ALTER FUNCTION app.advance_account_data_generation() SECURITY INVOKER`,
		"counter public":  `GRANT EXECUTE ON FUNCTION app.advance_account_data_generation() TO PUBLIC`,
		"counter worker":  `GRANT EXECUTE ON FUNCTION app.advance_account_data_generation() TO workouts_worker`,
		"helper row lock": `DO $block$ BEGIN EXECUTE replace(pg_get_functiondef('app.lock_account_data_generation()'::regprocedure),
			'WHERE account_id = app.current_account_id();','WHERE account_id = app.current_account_id() FOR UPDATE;'); END $block$`,
		"helper body": `DO $block$ BEGIN EXECUTE replace(pg_get_functiondef('app.lock_account_data_generation()'::regprocedure),
			'RETURN current_generation;','RETURN current_generation + 1;'); END $block$`,
		"helper invoker":      `ALTER FUNCTION app.lock_account_data_generation() SECURITY INVOKER`,
		"helper volatility":   `ALTER FUNCTION app.lock_account_data_generation() STABLE`,
		"helper search path":  `ALTER FUNCTION app.lock_account_data_generation() SET search_path=app,pg_catalog`,
		"helper owner":        `ALTER FUNCTION app.lock_account_data_generation() OWNER TO workouts_migration`,
		"helper API grant":    `REVOKE EXECUTE ON FUNCTION app.lock_account_data_generation() FROM workouts_api`,
		"helper worker":       `GRANT EXECUTE ON FUNCTION app.lock_account_data_generation() TO workouts_worker`,
		"helper public":       `GRANT EXECUTE ON FUNCTION app.lock_account_data_generation() TO PUBLIC`,
		"helper grant option": `GRANT EXECUTE ON FUNCTION app.lock_account_data_generation() TO workouts_api WITH GRANT OPTION`,
		"fence generation lock": `DO $block$ BEGIN EXECUTE replace(pg_get_functiondef('app.fence_ingest_job(uuid,text,uuid,integer)'::regprocedure),
			'FROM app.account_data_generations WHERE account_id = target_account;',
			'FROM app.account_data_generations WHERE account_id = target_account FOR UPDATE NOWAIT;'); END $block$`,
		"fence missing error": `DO $block$ BEGIN EXECUTE replace(pg_get_functiondef('app.fence_ingest_job(uuid,text,uuid,integer)'::regprocedure),
			'''missing account data generation''','''wrong error'''); END $block$`,
		"fence invoker":             `ALTER FUNCTION app.fence_ingest_job(uuid,text,uuid,integer) SECURITY INVOKER`,
		"fence worker grant":        `REVOKE EXECUTE ON FUNCTION app.fence_ingest_job(uuid,text,uuid,integer) FROM workouts_worker`,
		"fence coverage grant":      `GRANT EXECUTE ON FUNCTION app.fence_ingest_job(uuid,text,uuid,integer) TO workouts_coverage_worker`,
		"fence public":              `GRANT EXECUTE ON FUNCTION app.fence_ingest_job(uuid,text,uuid,integer) TO PUBLIC`,
		"API counter update":        `GRANT UPDATE ON app.account_data_generations TO workouts_api`,
		"API counter column update": `GRANT UPDATE(generation) ON app.account_data_generations TO workouts_api`,
		"worker counter update":     `GRANT UPDATE ON app.account_data_generations TO workouts_worker`,
		"counter owner update":      `REVOKE UPDATE ON app.account_data_generations FROM workouts_security_owner`,
		"minimum raised":            `UPDATE app.schema_metadata SET minimum_runtime_version=30,schema_version=30`,
	}
	for _, role := range []string{"workouts_api", "workouts_worker", "workouts_coverage_worker"} {
		t.Run(role, func(t *testing.T) {
			check := func(t *testing.T, mutation string, want bool) {
				t.Helper()
				tx, err := bootstrap.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(context.Background())
				if mutation != "" {
					if _, err := tx.Exec(ctx, mutation); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+role); err != nil {
					t.Fatal(err)
				}
				var ready bool
				if err := tx.QueryRow(ctx, runtimeFullReadinessSQL, SupportedSchemaVersion).Scan(&ready); err != nil {
					t.Fatal(err)
				}
				if ready != want {
					t.Fatalf("full readiness=%t want %t", ready, want)
				}
			}
			check(t, "", true)
			check(t, `UPDATE app.schema_metadata SET schema_version=30`, true)
			for name, mutation := range mutations {
				t.Run(name, func(t *testing.T) { check(t, mutation, false) })
			}
		})
	}
}
