package database

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestEligibleCohortReadinessPostgreSQL(t *testing.T) {
	dsn := os.Getenv("PROJECT_ELIGIBLE_COHORT_READINESS_DATABASE_URL")
	if dsn == "" || os.Getenv("PROJECT_COVERAGE_PIPELINE_TEST_DISPOSABLE") != "1" {
		t.Skip("explicit migrated disposable eligible-cohort database required")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(config.Database, "workouts_pipeline_test_") ||
		(config.Host != "127.0.0.1" && config.Host != "::1") {
		t.Fatal("requires named loopback scratch database")
	}
	for _, fallback := range config.Fallbacks {
		if fallback.Host != config.Host || fallback.Port != config.Port {
			t.Fatal("fallback endpoints prohibited")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	for _, role := range []string{"workouts_api", "workouts_worker", "workouts_coverage_worker"} {
		t.Run(role, func(t *testing.T) {
			for name, mutation := range map[string]string{
				"complete":              "",
				"generation deferred":   `ALTER TABLE app.workout_routes DISABLE TRIGGER workout_routes_data_generation_after_write`,
				"prepared forced RLS":   `ALTER TABLE app.ingest_coverage_prepared NO FORCE ROW LEVEL SECURITY`,
				"prepared direct read":  `GRANT SELECT ON app.ingest_coverage_prepared TO workouts_worker`,
				"builder direct write":  `GRANT UPDATE ON app.ingest_coverage_builders TO workouts_coverage_worker`,
				"builder capacity":      `ALTER TABLE app.ingest_coverage_builders DROP CONSTRAINT ingest_coverage_builders_batch_size_check`,
				"vector constraint":     `ALTER TABLE app.ingest_coverage_prepared DROP CONSTRAINT ingest_coverage_prepared_generations_check`,
				"prepared metadata":     `ALTER TABLE app.ingest_coverage_prepared ALTER COLUMN ownership_epoch DROP NOT NULL`,
				"coordinates forbidden": `ALTER TABLE app.ingest_coverage_prepared ADD COLUMN coordinates jsonb`,
				"builder index":         `DROP INDEX app.ingest_coverage_prepared_builder_idx`,
				"seal grant":            `REVOKE EXECUTE ON FUNCTION app.seal_ingest_coverage_cohort(uuid,uuid,text,uuid,bigint,uuid,jsonb,integer) FROM workouts_worker`,
				"repair grant":          `GRANT EXECUTE ON FUNCTION app.record_ingest_coverage_repair(uuid,uuid,text,uuid,uuid,integer) TO workouts_api`,
				"old adoption bypass":   `GRANT EXECUTE ON FUNCTION app.apply_adopted_workout_coverage_refresh(uuid,uuid,text,uuid,bigint,uuid,uuid,bytea,text,text,jsonb,uuid,uuid,text,text,text,double precision) TO workouts_worker`,
				"obsolete apply ABI":    `CREATE FUNCTION app.apply_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,jsonb,text,text,text,double precision) RETURNS boolean LANGUAGE sql AS 'SELECT true'`,
				"root guard body":       `CREATE OR REPLACE FUNCTION app.seal_ingest_coverage_cohort(target_account_id uuid,target_job_id uuid,claiming_worker text,current_lease_token uuid,reconciliation_revision bigint,target_builder_id uuid,refreshed jsonb,runtime_version integer) RETURNS TABLE(parent_job_id uuid,route_count integer,outcome text) LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog,app AS 'SELECT NULL::uuid,0,''held''::text'`,
			} {
				t.Run(name, func(t *testing.T) {
					tx, err := admin.Begin(ctx)
					if err != nil {
						t.Fatal(err)
					}
					defer tx.Rollback(ctx)
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
					if ready != (mutation == "") {
						t.Fatalf("ready=%v for mutation %q", ready, name)
					}
				})
			}
		})
	}
}
