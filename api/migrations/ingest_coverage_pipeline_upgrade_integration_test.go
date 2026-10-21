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
	"github.com/jackc/pgx/v5"
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
	for index, mode := range []string{"fresh", "canonical27"} {
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
			if _, err := active.Up(ctx); err != nil {
				t.Fatal(err)
			}
			if err := migrations.ValidateHistory(ctx, db); err != nil {
				t.Fatal("marked schema28/min28 history must remain valid:", err)
			}
			var valid bool
			if err := db.QueryRowContext(ctx, `SELECT schema_version=$1 AND minimum_runtime_version=28 AND migration_baseline=27
				AND (SELECT count(*)=$1+1 AND max(version_id)=$1 FROM public.goose_db_version) FROM app.schema_metadata WHERE singleton`, database.SupportedSchemaVersion).Scan(&valid); err != nil || !valid {
				t.Fatalf("supported path valid=%t err=%v", valid, err)
			}
		})
	}
}
