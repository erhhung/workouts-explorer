package migrations_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/migrations"
	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestIngestCoveragePipelineMigrationPathsPostgreSQL(t *testing.T) {
	dsn := os.Getenv("PROJECT_COVERAGE_PIPELINE_TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("PROJECT_COVERAGE_PIPELINE_TEST_DISPOSABLE") != "1" {
		t.Skip("explicit disposable pipeline database required")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(config.Database, "workouts_pipeline_test_") || (config.Host != "127.0.0.1" && config.Host != "localhost" && config.Host != "::1") {
		t.Fatal("pipeline migration paths require a named loopback scratch database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	for index, mode := range []string{"fresh", "canonical27", "status29"} {
		t.Run(mode, func(t *testing.T) {
			name := fmt.Sprintf("workouts_pipeline_test_path_%d_%d", os.Getpid(), index)
			if _, err := admin.Exec(ctx, "CREATE DATABASE "+name+" OWNER workouts_migration"); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if _, err := admin.Exec(context.Background(), "DROP DATABASE "+name); err != nil {
					t.Error(err)
				}
			}()
			cfg := config.Copy()
			cfg.Database = name
			bootstrap := stdlib.OpenDB(*cfg)
			defer bootstrap.Close()
			if _, err := bootstrap.ExecContext(ctx, `CREATE EXTENSION postgis`); err != nil {
				t.Fatal(err)
			}
			cfg.RuntimeParams["role"] = "workouts_migration"
			db := stdlib.OpenDB(*cfg)
			defer db.Close()
			active, err := goose.NewProvider(goose.DialectPostgres, db, migrations.Files)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "canonical27" {
				if _, err := active.UpTo(ctx, 27); err != nil {
					t.Fatal(err)
				}
				var canonical bool
				if err := db.QueryRowContext(ctx, `SELECT schema_version=27 AND minimum_runtime_version=26 AND migration_baseline=27 FROM app.schema_metadata WHERE singleton`).Scan(&canonical); err != nil || !canonical {
					t.Fatalf("canonical27 checkpoint: %t %v", canonical, err)
				}
			}
			var seeded *coverageDisplayFixture
			var phase reprocessingFixture
			var actors testDatabases
			var before string
			if mode == "status29" {
				if _, err := active.UpTo(ctx, 29); err != nil {
					t.Fatal(err)
				}
				var historical bool
				if err := db.QueryRowContext(ctx, `SELECT schema_version=29 AND minimum_runtime_version=29 AND migration_baseline=27
					AND (SELECT count(*)=30 AND max(version_id)=29 AND bool_and(is_applied) FROM public.goose_db_version)
					FROM app.schema_metadata WHERE singleton`).Scan(&historical); err != nil || !historical {
					t.Fatalf("status29 checkpoint: %t %v", historical, err)
				}
				actors.ctx = ctx
				for _, role := range []string{"workouts_api", "workouts_worker", "workouts_migration"} {
					poolConfig, err := pgxpool.ParseConfig(dsn)
					if err != nil {
						t.Fatal(err)
					}
					poolConfig.ConnConfig.Database, poolConfig.MaxConns = name, 3
					poolConfig.ConnConfig.RuntimeParams["role"] = role
					pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
					if err != nil {
						t.Fatal(err)
					}
					defer pool.Close()
					switch role {
					case "workouts_api":
						actors.api = pool
					case "workouts_worker":
						actors.worker = pool
					case "workouts_migration":
						actors.migration = pool
					}
				}
				f := newCoverageDisplayFixture(t, actors)
				seeded = &f
				phase = reprocessingJob(t, actors, nil, uuid.Nil, uuid.Nil, "false")
				if !callBool(t, ctx, actors.worker, phase.account, ingestPhaseCall, phase.job, reprocessingWorker, phase.lease, "importing", 1, 2) {
					t.Fatal("status29 must already support runtime30 phase reporting")
				}
				reprocessingFreeze(t, actors, phase, reprocessingManifest(1), "created")
				reprocessingFile(t, actors, phase, reprocessingManifest(1), 0, "succeeded", uuid.Nil, false)
				if err := db.QueryRowContext(ctx, `SELECT string_agg(oid::text||pg_get_functiondef(oid)||proacl::text,'|' ORDER BY oid)
					FROM pg_proc WHERE pronamespace='app'::regnamespace AND proname IN
					('ingest_outcome_status','derive_parent_status','read_ingest_candidate_actions','stamp_ingest_source_file_receipt')`).Scan(&before); err != nil {
					t.Fatal(err)
				}
				if _, err := active.Down(ctx); err == nil || !strings.Contains(err.Error(), "forward-only") {
					t.Fatalf("status29 Down must reject unsafe downgrade: %v", err)
				}
				if err := migrations.ValidateHistory(ctx, db); err != nil {
					t.Fatal("rejected status29 Down must preserve history:", err)
				}
			}
			if _, err := active.Up(ctx); err != nil {
				t.Fatal(err)
			}
			if err := migrations.ValidateHistory(ctx, db); err != nil {
				t.Fatal("marked current-schema history must remain valid:", err)
			}
			var valid bool
			if err := db.QueryRowContext(ctx, `SELECT schema_version=$1 AND minimum_runtime_version=31 AND migration_baseline=27
				AND (SELECT count(*)=$1+1 AND max(version_id)=$1 FROM public.goose_db_version) FROM app.schema_metadata WHERE singleton`, database.SupportedSchemaVersion).Scan(&valid); err != nil || !valid {
				t.Fatalf("supported path valid=%t err=%v", valid, err)
			}
			if seeded != nil {
				coverageDisplayProjectionParity(t, actors, *seeded)
				s := coverageEntityStatsAll(t, actors, *seeded)
				coverageEntityStatsCheck(t, actors, *seeded, s, "path", seeded.campus, `range_workout_count=5 AND all_time_workout_count=7
					AND minimum_longitude=1 AND maximum_longitude=12.25`)
				var after string
				if err := db.QueryRowContext(ctx, `SELECT string_agg(oid::text||pg_get_functiondef(oid)||proacl::text,'|' ORDER BY oid)
					FROM pg_proc WHERE pronamespace='app'::regnamespace AND proname IN
					('ingest_outcome_status','derive_parent_status','read_ingest_candidate_actions','stamp_ingest_source_file_receipt')`).Scan(&after); err != nil || after != before {
					t.Fatalf("display30 changed status29 function identities, definitions or ACLs: %v", err)
				}
				coverageDisplayCheck(t, actors, `SELECT EXISTS(SELECT 1 FROM app.job_events WHERE account_id=$1 AND job_id=$2
					AND code='ingest-phase-importing' AND fields='{"current":1,"total":2,"attempt":1}'::jsonb)
					AND EXISTS(SELECT 1 FROM app.source_files WHERE account_id=$1 AND job_id=$2 AND state='succeeded' AND observed_identity IS NOT NULL)`, phase.account, phase.job)
			}
			if _, err := active.Down(ctx); err == nil || !strings.Contains(err.Error(), "forward-only") {
				t.Fatalf("display30 Down must reject unsafe downgrade: %v", err)
			}
			if err := migrations.ValidateHistory(ctx, db); err != nil {
				t.Fatal("rejected Down must preserve final ledger:", err)
			}
		})
	}
}
