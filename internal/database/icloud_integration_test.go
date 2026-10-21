package database

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Use only a fully migrated, explicitly disposable loopback database. Faults
// are transaction-local and rolled back; no application URL fallback is used.
func TestICloudReadinessIntegration(t *testing.T) {
	dsn := os.Getenv("ICLOUD_READINESS_TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("ICLOUD_TEST_DISPOSABLE") != "1" {
		t.Skip("ICLOUD_READINESS_TEST_DATABASE_URL and ICLOUD_TEST_DISPOSABLE=1 are required")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid scratch readiness database configuration")
	}
	if !strings.HasPrefix(config.ConnConfig.Database, "workouts_icloud_test_") ||
		(config.ConnConfig.Host != "localhost" && config.ConnConfig.Host != "127.0.0.1" && config.ConnConfig.Host != "::1") {
		t.Fatal("readiness integration requires a named loopback scratch database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	config.MaxConns = 1
	admin, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("create scratch readiness pool")
	}
	defer admin.Close()
	roles := []string{"workouts_api", "workouts_worker", "workouts_coverage_worker"}
	for _, role := range roles {
		roleConfig := config.Copy()
		roleConfig.ConnConfig.RuntimeParams["role"] = role
		pool, err := pgxpool.NewWithConfig(ctx, roleConfig)
		if err != nil {
			t.Fatal("create runtime readiness pool")
		}
		ready := Ready(ctx, pool)
		pool.Close()
		if !ready {
			t.Fatalf("fully migrated schema-%d database is not ready for %s", SupportedSchemaVersion, role)
		}
	}

	faults := map[string]string{
		"missing runtime column": `ALTER TABLE app.account_sync_schedules RENAME COLUMN lease_runtime_version TO readiness_missing_runtime`,
		"missing metadata grant": `REVOKE SELECT ON app.schema_metadata FROM workouts_security_owner`,
		"obsolete worker fence": `CREATE OR REPLACE FUNCTION app.claim_next_worker_job(claiming_worker text,new_lease_token uuid,lease_duration interval,runtime_version integer)
			RETURNS TABLE(job_id uuid,account_id uuid,kind text) LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app AS $$
			BEGIN IF runtime_version < 8 THEN RAISE EXCEPTION 'worker runtime version 8 or newer is required'; END IF; RETURN; END $$`,
		"obsolete scheduler fence": `CREATE OR REPLACE FUNCTION app.claim_due_sync_account(claiming_worker text,new_lease_token uuid,lease_duration interval,runtime_version integer)
			RETURNS TABLE(account_id uuid,lease_token uuid,next_run_at timestamptz) LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app AS $$
			BEGIN IF runtime_version < 6 THEN RAISE EXCEPTION 'worker runtime version 6 or newer is required'; END IF; RETURN; END $$`,
	}
	for _, signature := range []string{
		"app.claim_due_sync_account_internal(text,uuid,interval,integer)",
		"app.enqueue_leased_scheduled_ingest_internal(uuid,text,uuid,uuid,bytea,jsonb)",
		"app.finish_sync_account_internal(uuid,text,uuid,uuid)",
	} {
		faults[signature+" unsafe owner"] = "ALTER FUNCTION " + signature + " OWNER TO workouts_migration"
		faults[signature+" invoker"] = "ALTER FUNCTION " + signature + " SECURITY INVOKER"
		faults[signature+" unsafe path"] = "ALTER FUNCTION " + signature + " SET search_path=pg_catalog,app,public"
		for _, role := range []string{"PUBLIC", "workouts_api", "workouts_worker", "workouts_coverage_worker", "workouts_tiles"} {
			faults[signature+" exposed to "+role] = "GRANT EXECUTE ON FUNCTION " + signature + " TO " + role
		}
	}
	for name, fault := range faults {
		t.Run(name, func(t *testing.T) {
			tx, err := admin.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if _, err := tx.Exec(ctx, `SET LOCAL ROLE workouts_migration`); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, fault); err != nil {
				t.Fatal(err)
			}
			for _, role := range roles {
				if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+role); err != nil {
					t.Fatal(err)
				}
				var ready bool
				if err := tx.QueryRow(ctx, runtimeReadinessSQL, SupportedSchemaVersion).Scan(&ready); err != nil {
					t.Fatalf("readiness query failed for %s: %v", role, err)
				}
				if ready {
					t.Fatalf("readiness accepted %s for %s", name, role)
				}
			}
		})
	}
}
