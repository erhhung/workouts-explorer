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

func TestIngestPhaseReadinessPostgreSQL(t *testing.T) {
	const prefix = "PROJECT_INGEST_PHASE_READINESS_"
	dsn := os.Getenv(prefix + "DATABASE_URL")
	if dsn == "" || os.Getenv(prefix+"DISPOSABLE") != "1" {
		t.Skip("explicit phase-readiness scratch admin URL and PROJECT_INGEST_PHASE_READINESS_DISPOSABLE=1 required")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid phase-readiness admin URL")
	}
	validEndpoint := func(c *pgx.ConnConfig) bool {
		if !regexp.MustCompile(`^workouts_ingest_phase_test_[a-z0-9_]+$`).MatchString(c.Database) ||
			(c.Host != "127.0.0.1" && c.Host != "::1") || strings.Contains(c.Database, "clone") ||
			strings.Contains(c.Database, "actual") || strings.Contains(c.Database, "live") || c.RuntimeParams["role"] != "" {
			return false
		}
		for _, fallback := range c.Fallbacks {
			if fallback.Host != c.Host || fallback.Port != c.Port {
				return false
			}
		}
		return true
	}
	if !validEndpoint(config.ConnConfig) {
		t.Fatal("mutations require a synthetic workouts_ingest_phase_test_ loopback scratch database without alternate endpoints")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, config.ConnConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	var safe, locked bool
	if err := admin.QueryRow(ctx, `SELECT current_database()=$1 AND rolsuper,
		pg_try_advisory_lock(340034,hashtext(current_database())) FROM pg_roles WHERE rolname=current_user`, config.ConnConfig.Database).Scan(&safe, &locked); err != nil || !safe || !locked {
		t.Fatalf("scratch superuser and exclusive mutation harness required: safe=%t locked=%t err=%v", safe, locked, err)
	}
	if err := admin.QueryRow(ctx, `SELECT schema_version=31 AND minimum_runtime_version=31 AND migration_baseline=27
		FROM app.schema_metadata WHERE singleton`).Scan(&safe); err != nil || !safe {
		t.Fatalf("harness requires the current schema/min31/baseline27: %v", err)
	}
	var pools []*pgxpool.Pool
	for _, role := range []struct{ name, env string }{
		{"workouts_api", prefix + "API_DATABASE_URL"}, {"workouts_worker", prefix + "WORKER_DATABASE_URL"},
		{"workouts_coverage_worker", prefix + "COVERAGE_WORKER_DATABASE_URL"},
	} {
		runtimeConfig := config.Copy()
		if roleDSN := os.Getenv(role.env); roleDSN != "" {
			runtimeConfig, err = pgxpool.ParseConfig(roleDSN)
			if err != nil {
				t.Fatal("invalid " + role.env)
			}
		} else {
			runtimeConfig.ConnConfig.User, runtimeConfig.ConnConfig.Password = role.name, ""
		}
		c := runtimeConfig.ConnConfig
		if !validEndpoint(c) || c.Host != config.ConnConfig.Host || c.Port != config.ConnConfig.Port ||
			c.Database != config.ConnConfig.Database || c.User != role.name {
			t.Fatal(role.env + " must authenticate the expected role on the same scratch endpoint")
		}
		runtimeConfig.MaxConns = 1
		pool, err := pgxpool.NewWithConfig(ctx, runtimeConfig)
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
			for _, runtime := range []int{29, 30} {
				var ready bool
				wantPhase := runtime == 30 && want
				if err := pool.QueryRow(ctx, runtimeIngestPhaseReadinessSQL, runtime).Scan(&ready); err != nil || ready != wantPhase {
					t.Fatalf("runtime%d phase readiness=%t want=%t err=%v", runtime, ready, wantPhase, err)
				}
			}
			var gated bool
			if err := pool.QueryRow(ctx, runtimeIngestPhaseReadinessSQL, 28).Scan(&gated); err != nil || !gated {
				t.Fatalf("schema28 checkpoint must skip phase checks: %v", err)
			}
			if Ready(ctx, pool) != want {
				t.Fatalf("Ready must require the schema29 phase contract: want=%t", want)
			}
		}
	}
	readiness(t, true)
	const function = "app.record_ingest_phase(uuid,text,uuid,text,bigint,bigint)"
	var definition, indexDefinition string
	if err := admin.QueryRow(ctx, `SELECT pg_get_functiondef($1::regprocedure),pg_get_indexdef('app.job_events_ingest_phase_latest_idx'::regclass)`, function).Scan(&definition, &indexDefinition); err != nil {
		t.Fatal(err)
	}
	alter := "ALTER FUNCTION " + function + " "
	const index = "app.job_events_ingest_phase_latest_idx"
	type mutation struct{ name, sql, restore string }
	mutations := []mutation{
		{"minimum runtime lowered", `UPDATE app.schema_metadata SET minimum_runtime_version=28 WHERE singleton`, `UPDATE app.schema_metadata SET minimum_runtime_version=31 WHERE singleton`},
		{"partial schema metadata", `UPDATE app.schema_metadata SET schema_version=28,minimum_runtime_version=28 WHERE singleton`, `UPDATE app.schema_metadata SET schema_version=31,minimum_runtime_version=31 WHERE singleton`},
		{"function missing", alter + "RENAME TO missing_ingest_phase", "ALTER FUNCTION app.missing_ingest_phase(uuid,text,uuid,text,bigint,bigint) RENAME TO record_ingest_phase"},
		{"function owner", alter + "OWNER TO workouts_migration", alter + "OWNER TO workouts_security_owner"},
		{"function invoker", alter + "SECURITY INVOKER", definition},
		{"function path", alter + "SET search_path=app,pg_catalog", definition},
		{"function extra config", alter + "SET enable_seqscan=off", alter + "RESET enable_seqscan; " + definition},
		{"function strict", alter + "STRICT", definition},
		{"function volatility", alter + "STABLE", definition},
		{"index missing", "DROP INDEX " + index, indexDefinition},
		{"index ascending", "DROP INDEX " + index + "; CREATE INDEX job_events_ingest_phase_latest_idx ON app.job_events(job_id,id) WHERE code LIKE 'ingest-phase-%'", "DROP INDEX " + index + "; " + indexDefinition},
		{"index wrong predicate", "DROP INDEX " + index + "; CREATE INDEX job_events_ingest_phase_latest_idx ON app.job_events(job_id,id DESC) WHERE code LIKE 'ingest-%'", "DROP INDEX " + index + "; " + indexDefinition},
		{"index wrong keys", "DROP INDEX " + index + "; CREATE INDEX job_events_ingest_phase_latest_idx ON app.job_events(account_id,id DESC) WHERE code LIKE 'ingest-phase-%'", "DROP INDEX " + index + "; " + indexDefinition},
	}
	for _, flag := range []string{"indisvalid", "indisready", "indislive"} {
		where := " WHERE indexrelid='" + index + "'::regclass"
		mutations = append(mutations, mutation{"index " + flag, "UPDATE pg_index SET " + flag + "=false" + where, "UPDATE pg_index SET " + flag + "=true" + where})
	}
	for _, role := range []string{"PUBLIC", "workouts_api", "workouts_coverage_worker", "workouts_tiles", "workouts_migration"} {
		mutations = append(mutations, mutation{"execute grant " + role,
			"GRANT EXECUTE ON FUNCTION " + function + " TO " + role, "REVOKE EXECUTE ON FUNCTION " + function + " FROM " + role})
	}
	mutations = append(mutations,
		mutation{"worker missing grant", "REVOKE EXECUTE ON FUNCTION " + function + " FROM workouts_worker", "GRANT EXECUTE ON FUNCTION " + function + " TO workouts_worker"},
		mutation{"worker grant option", "GRANT EXECUTE ON FUNCTION " + function + " TO workouts_worker WITH GRANT OPTION", "REVOKE GRANT OPTION FOR EXECUTE ON FUNCTION " + function + " FROM workouts_worker"})
	for _, mutation := range mutations {
		if !t.Run(mutation.name, func(t *testing.T) {
			if _, err := admin.Exec(ctx, mutation.sql); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				restoreCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if _, err := admin.Exec(restoreCtx, mutation.restore); err != nil {
					t.Fatalf("restore phase contract: %v", err)
				}
			})
			readiness(t, false)
		}) {
			t.Fatal("stopping readiness mutation harness after failure")
		}
	}
	readiness(t, true)
	t.Run("phase required at schema29 checkpoint", func(t *testing.T) {
		if _, err := admin.Exec(ctx, `UPDATE app.schema_metadata SET schema_version=29,minimum_runtime_version=29 WHERE singleton`); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			restoreCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := admin.Exec(restoreCtx, `UPDATE app.schema_metadata SET schema_version=31,minimum_runtime_version=31 WHERE singleton`); err != nil {
				t.Fatal(err)
			}
		})
		for _, pool := range pools {
			var ready bool
			if err := pool.QueryRow(ctx, runtimeIngestPhaseReadinessSQL, 29).Scan(&ready); err != nil || !ready {
				t.Fatalf("schema29 phase readiness=%t err=%v", ready, err)
			}
		}
		if _, err := admin.Exec(ctx, "ALTER INDEX "+index+" RENAME TO missing_ingest_phase_index"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			restoreCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if _, err := admin.Exec(restoreCtx, "ALTER INDEX app.missing_ingest_phase_index RENAME TO job_events_ingest_phase_latest_idx"); err != nil {
				t.Fatal(err)
			}
		})
		for _, pool := range pools {
			var ready bool
			if err := pool.QueryRow(ctx, runtimeIngestPhaseReadinessSQL, 29).Scan(&ready); err != nil || ready {
				t.Fatalf("schema29 must require the phase index: ready=%t err=%v", ready, err)
			}
		}
	})
	readiness(t, true)
}
