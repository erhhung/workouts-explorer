package database

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// This test mutates and rolls back only an explicitly disposable migrated scratch
// database. It never reads production configuration or falls back to other URLs.
func TestIngestCoveragePipelineReadinessPostgreSQL(t *testing.T) {
	dsn := os.Getenv("PROJECT_COVERAGE_PIPELINE_READINESS_DATABASE_URL")
	if dsn == "" || os.Getenv("PROJECT_COVERAGE_PIPELINE_TEST_DISPOSABLE") != "1" {
		t.Skip("explicit migrated disposable pipeline database required")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(config.Database, "workouts_pipeline_test_") ||
		(config.Host != "127.0.0.1" && config.Host != "localhost" && config.Host != "::1") {
		t.Fatal("pipeline readiness requires a named loopback scratch database")
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
			check := func(t *testing.T, mutation string, want bool) {
				t.Helper()
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
				if err := tx.QueryRow(ctx, runtimeReadinessSQL, SupportedSchemaVersion).Scan(&ready); err != nil {
					t.Fatal(err)
				}
				if ready != want {
					// Report the failing top-level catalogue condition, not credentials or rows.
					body := strings.TrimSpace(runtimeReadinessSQL[:strings.LastIndex(runtimeReadinessSQL, "FROM public.goose_db_version")])
					conditions := pipelineReadinessConditions(strings.TrimPrefix(body, "SELECT "))
					for _, condition := range conditions {
						var valid *bool
						var args []any
						if strings.Contains(condition, "$1") {
							args = append(args, SupportedSchemaVersion)
						}
						if err := tx.QueryRow(ctx, "SELECT "+condition+" FROM public.goose_db_version", args...).Scan(&valid); err != nil {
							t.Fatal("readiness condition", condition, err)
						} else if valid == nil || !*valid {
							t.Log("failed readiness condition:", condition)
						}
					}
					t.Fatalf("ready=%t want %t mutation=%s", ready, want, mutation)
				}
			}
			check(t, "", true)
			for name, mutation := range map[string]string{
				"old minimum":                    `UPDATE app.schema_metadata SET minimum_runtime_version=27`,
				"policy role":                    `GRANT SELECT ON app.coverage_batch_policy TO workouts_api`,
				"outbox RLS":                     `ALTER TABLE app.ingest_coverage_handoffs NO FORCE ROW LEVEL SECURITY`,
				"receipt RLS":                    `ALTER TABLE app.adopted_coverage_handoff_receipts DISABLE ROW LEVEL SECURITY`,
				"outbox index":                   `DROP INDEX app.ingest_coverage_handoffs_pending_idx`,
				"capacity default":               `ALTER TABLE app.coverage_job_contexts ALTER COLUMN parent_batch_size SET DEFAULT 25`,
				"capacity bound":                 `ALTER TABLE app.coverage_job_contexts DROP CONSTRAINT coverage_job_contexts_parent_batch_size_check`,
				"policy bound":                   `ALTER TABLE app.coverage_batch_policy DROP CONSTRAINT coverage_batch_policy_batch_size_check`,
				"record trigger":                 `ALTER TABLE app.workout_import_events DISABLE TRIGGER workout_import_events_coverage_handoff`,
				"configure grant":                `REVOKE EXECUTE ON FUNCTION app.configure_coverage_batch_size(integer,integer) FROM workouts_worker`,
				"private core":                   `GRANT EXECUTE ON FUNCTION app.enqueue_coverage_update_core(uuid,uuid,text,bigint,bigint,text,text,text,double precision,jsonb,boolean) TO workouts_worker`,
				"apply search path":              `ALTER FUNCTION app.apply_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,jsonb,text,text,text,double precision,integer) SET search_path=app,pg_catalog`,
				"defer grant":                    `REVOKE EXECUTE ON FUNCTION app.defer_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,jsonb,integer) FROM workouts_coverage_worker`,
				"snapshot volatility":            `ALTER FUNCTION app.ingest_coverage_snapshot_digest(uuid,uuid) VOLATILE`,
				"coordinate snapshot volatility": `ALTER FUNCTION app.read_ingest_coverage_route_snapshot(uuid,uuid,text,uuid,bigint,uuid,uuid,text) VOLATILE`,
				"coordinate snapshot API grant":  `GRANT EXECUTE ON FUNCTION app.read_ingest_coverage_route_snapshot(uuid,uuid,text,uuid,bigint,uuid,uuid,text) TO workouts_api`,
				"candidate volatility":           `ALTER FUNCTION app.ingest_coverage_candidates(uuid,uuid) VOLATILE`,
			} {
				t.Run(name, func(t *testing.T) { check(t, mutation, false) })
			}
		})
	}
}

func pipelineReadinessConditions(body string) []string {
	var result []string
	depth, start := 0, 0
	quoted := false
	for i := 0; i < len(body); i++ {
		if body[i] == '\'' {
			if quoted && i+1 < len(body) && body[i+1] == '\'' {
				i++
				continue
			}
			quoted = !quoted
		}
		if quoted {
			continue
		}
		if body[i] == '(' {
			depth++
		}
		if body[i] == ')' {
			depth--
		}
		if depth == 0 && strings.HasPrefix(body[i:], "AND ") && i > 0 && strings.ContainsRune(" \t\n", rune(body[i-1])) {
			result = append(result, body[start:i])
			start = i + 4
		}
	}
	return append(result, body[start:])
}
