package database

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCoverageMetadataReadinessPostgreSQL(t *testing.T) {
	const prefix = "PROJECT_COVERAGE_METADATA_READINESS_"
	dsn := os.Getenv(prefix + "DATABASE_URL")
	if dsn == "" || os.Getenv(prefix+"DISPOSABLE") != "1" {
		t.Skip("requires an explicit migrated synthetic metadata-readiness loopback scratch database")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid scratch configuration")
	}
	if !regexp.MustCompile(`^workouts_coverage_metadata_test_[a-z0-9_]+$`).MatchString(config.Database) ||
		(config.Host != "127.0.0.1" && config.Host != "::1") || config.RuntimeParams["role"] != "" ||
		strings.Contains(config.Database, "clone") || strings.Contains(config.Database, "live") || strings.Contains(config.Database, "actual") {
		t.Fatal("metadata-readiness mutations require a named synthetic loopback scratch database")
	}
	for _, fallback := range config.Fallbacks {
		if fallback.Host != config.Host || fallback.Port != config.Port {
			t.Fatal("alternate scratch endpoints are forbidden")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	var safe bool
	if err := admin.QueryRow(ctx, `SELECT rolsuper AND current_database()=$1 AND pg_try_advisory_lock(290030,hashtext(current_database()))
		FROM pg_roles WHERE rolname=current_user`, config.Database).Scan(&safe); err != nil || !safe {
		t.Fatalf("exclusive scratch superuser required: %v", err)
	}
	var ready bool
	if err := admin.QueryRow(ctx, runtimeCoverageMetadataReadinessSQL, 30).Scan(&ready); err != nil || !ready {
		t.Fatalf("native metadata feature is not ready: %t %v", ready, err)
	}
	var pools []*pgxpool.Pool
	for _, role := range []struct{ name, env string }{
		{"workouts_api", "API_DATABASE_URL"}, {"workouts_worker", "WORKER_DATABASE_URL"},
		{"workouts_coverage_worker", "COVERAGE_WORKER_DATABASE_URL"},
	} {
		runtimeConfig := config.Copy()
		if roleDSN := os.Getenv(prefix + role.env); roleDSN != "" {
			runtimeConfig, err = pgx.ParseConfig(roleDSN)
			if err != nil {
				t.Fatal("invalid " + prefix + role.env)
			}
		} else {
			runtimeConfig.User, runtimeConfig.Password = role.name, ""
		}
		if runtimeConfig.Host != config.Host || runtimeConfig.Port != config.Port ||
			runtimeConfig.Database != config.Database || runtimeConfig.User != role.name || runtimeConfig.RuntimeParams["role"] != "" {
			t.Fatal(prefix + role.env + " must authenticate the expected role on the same scratch endpoint")
		}
		for _, fallback := range runtimeConfig.Fallbacks {
			if fallback.Host != config.Host || fallback.Port != config.Port {
				t.Fatal("alternate runtime scratch endpoints are forbidden")
			}
		}
		poolConfig, err := pgxpool.ParseConfig("")
		if err != nil {
			t.Fatal(err)
		}
		poolConfig.ConnConfig, poolConfig.MaxConns = runtimeConfig, 1
		pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
		if err != nil {
			t.Fatal(err)
		}
		defer pool.Close()
		var restricted bool
		if err := pool.QueryRow(ctx, `SELECT session_user=$1 AND current_user=$1 AND NOT rolsuper AND NOT rolbypassrls
			AND NOT pg_has_role(current_user,'workouts_security_owner','USAGE') FROM pg_roles WHERE rolname=current_user`, role.name).Scan(&restricted); err != nil || !restricted {
			t.Fatalf("%s must be a genuine restricted login: %v", role.name, err)
		}
		pools = append(pools, pool)
	}
	readiness := func(t *testing.T, want bool) {
		t.Helper()
		for _, pool := range pools {
			if err := pool.QueryRow(ctx, runtimeCoverageMetadataReadinessSQL, 30).Scan(&ready); err != nil || ready != want {
				t.Fatalf("runtime metadata readiness=%t want=%t err=%v", ready, want, err)
			}
			if Ready(ctx, pool) != want {
				t.Fatalf("full runtime readiness must require the metadata contract: want=%t", want)
			}
		}
	}
	readiness(t, true)
	for name, mutation := range map[string]string{
		"proof RLS":                 `ALTER TABLE app.coverage_route_selection_proofs NO FORCE ROW LEVEL SECURITY`,
		"proof column":              `ALTER TABLE app.coverage_route_selection_proofs ALTER COLUMN selection_fingerprint DROP NOT NULL`,
		"private table grant":       `GRANT SELECT ON app.coverage_route_changes TO workouts_worker`,
		"private column grant":      `GRANT SELECT(region_ids) ON app.coverage_route_selection_proofs TO workouts_api`,
		"public helper grant":       `GRANT EXECUTE ON FUNCTION app.coverage_selection_metadata(uuid,uuid) TO PUBLIC`,
		"API helper grant":          `GRANT EXECUTE ON FUNCTION app.read_coverage_selection_metadata(uuid,uuid,text,uuid,bigint,uuid) TO workouts_api`,
		"missing worker capability": `REVOKE EXECUTE ON FUNCTION app.read_coverage_selection_metadata(uuid,uuid,text,uuid,bigint,uuid) FROM workouts_worker`,
		"wrong helper path":         `ALTER FUNCTION app.record_coverage_selection_proof(uuid,uuid,jsonb) SET search_path=app,pg_catalog`,
		"wrong helper volatility":   `ALTER FUNCTION app.coverage_route_actionable(uuid,uuid) VOLATILE`,
		"missing UPDATE hook":       `ALTER TABLE app.workout_route_points DISABLE TRIGGER workout_route_points_coverage_dirty_update`,
		"missing terminal wake":     `ALTER TABLE app.jobs DISABLE TRIGGER jobs_ingest_reconciliation_wake`,
		"missing proof floor index": `DROP INDEX app.coverage_route_selection_proofs_catalog_idx`,
		"wrong dirty index":         `DROP INDEX app.coverage_route_changes_dirty_idx; CREATE INDEX coverage_route_changes_dirty_idx ON app.coverage_route_changes(account_id,workout_id) WHERE mutation_version=resolved_version`,
	} {
		t.Run(name, func(t *testing.T) {
			tx, err := admin.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if _, err := tx.Exec(ctx, mutation); err != nil {
				t.Fatal(err)
			}
			if err := tx.QueryRow(ctx, runtimeCoverageMetadataReadinessSQL, 30).Scan(&ready); err != nil || ready {
				t.Fatalf("readiness accepted mutation: ready=%t err=%v", ready, err)
			}
		})
	}
	for _, table := range []string{"coverage_reconciliation_wakes", "coverage_route_changes", "coverage_route_selection_proofs", "coverage_applied_match_proofs"} {
		t.Run("required PK "+table, func(t *testing.T) {
			tx, err := admin.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if _, err := tx.Exec(ctx, "ALTER TABLE app."+table+" DROP CONSTRAINT "+table+"_pkey"); err != nil {
				t.Fatal(err)
			}
			for _, role := range []string{"workouts_api", "workouts_worker", "workouts_coverage_worker"} {
				if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+role); err != nil {
					t.Fatal(err)
				}
				if err := tx.QueryRow(ctx, runtimeFullReadinessSQL, 30).Scan(&ready); err != nil || ready {
					t.Fatalf("%s accepted missing %s PK: ready=%t err=%v", role, table, ready, err)
				}
				if _, err := tx.Exec(ctx, "RESET ROLE"); err != nil {
					t.Fatal(err)
				}
			}
			if err := tx.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
			readiness(t, true)
		})
	}
	readiness(t, true)
}
