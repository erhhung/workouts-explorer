package worker

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func parseCoverageAcceptanceConfig(value, database string) (*pgxpool.Config, error) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "postgresql" || u.Host == "" || u.Fragment != "" || strings.Contains(u.Host, ",") {
		return nil, fmt.Errorf("one explicit PostgreSQL URL host is required")
	}
	// Connection query parameters can override the URL authority and database.
	switch u.RawQuery {
	case "", "sslmode=disable", "sslmode=allow", "sslmode=prefer", "sslmode=require", "sslmode=verify-ca", "sslmode=verify-full":
	default:
		return nil, fmt.Errorf("only an optional sslmode query parameter is permitted")
	}
	config, err := pgxpool.ParseConfig(value)
	if err != nil {
		return nil, fmt.Errorf("invalid PostgreSQL connection configuration")
	}
	if database == "" {
		database = config.ConnConfig.Database
		if database != "coverage_test" && database != "acceptance_test" {
			return nil, fmt.Errorf("integration app database must be coverage_test or acceptance_test")
		}
	}
	if err := validateCoverageAcceptanceConfig(config, database); err != nil {
		return nil, err
	}
	return config, nil
}

func validateCoverageAcceptanceConfig(config *pgxpool.Config, database string) error {
	benchmark := false
	switch database {
	case "coverage_test", "acceptance_test", "coverage_test_osm", "acceptance_test_osm":
	case "acceptance_benchmark", "osm":
		benchmark = true
	default:
		return fmt.Errorf("database is not an approved disposable database")
	}
	if config == nil || config.ConnConfig == nil || config.ConnConfig.Database != database {
		return fmt.Errorf("effective database does not match the disposable database")
	}
	c := config.ConnConfig
	approved := (c.Host == "127.0.0.1" || c.Host == "localhost") && (c.Port == 15432 || !benchmark && c.Port == 5432) ||
		c.Host == "acceptance-postgres.workouts-acceptance.svc.cluster.local" && c.Port == 5432
	if !approved {
		return fmt.Errorf("effective host/port is not an approved disposable endpoint")
	}
	// pgx may add a TLS-to-plaintext fallback; it must never change destination.
	for _, fallback := range c.Fallbacks {
		if fallback == nil || fallback.Host != c.Host || fallback.Port != c.Port {
			return fmt.Errorf("fallback changes the disposable endpoint")
		}
	}
	return nil
}

func validateCoverageAcceptancePair(app, osm *pgxpool.Config) error {
	if app == nil || app.ConnConfig == nil {
		return fmt.Errorf("disposable app configuration is required")
	}
	database := app.ConnConfig.Database
	if database != "coverage_test" && database != "acceptance_test" && database != "acceptance_benchmark" {
		return fmt.Errorf("invalid disposable app database")
	}
	osmDatabase := database + "_osm"
	if database == "acceptance_benchmark" {
		osmDatabase = "osm"
	}
	if err := validateCoverageAcceptanceConfig(app, database); err != nil {
		return err
	}
	if err := validateCoverageAcceptanceConfig(osm, osmDatabase); err != nil {
		return err
	}
	if app.ConnConfig.Host != osm.ConnConfig.Host || app.ConnConfig.Port != osm.ConnConfig.Port {
		return fmt.Errorf("disposable app/OSM databases must use the same endpoint")
	}
	return nil
}

func validateCoverageAcceptanceOSMReader(ctx context.Context, pool *pgxpool.Pool) error {
	var readerOnly bool
	err := pool.QueryRow(ctx, `SELECT has_table_privilege(current_user,'osm_canonical.path_segments','SELECT')
		AND NOT has_table_privilege(current_user,'osm_canonical.path_segments','INSERT,UPDATE,DELETE,TRUNCATE')
		AND NOT EXISTS (
			SELECT 1 FROM pg_proc function JOIN pg_namespace namespace ON namespace.oid=function.pronamespace
			WHERE namespace.nspname='osm_catalog' AND function.prosecdef
			AND function.proname IN ('checkpoint_generation_stage','complete_generation_stage')
			AND has_function_privilege(current_user,function.oid,'EXECUTE'))`).Scan(&readerOnly)
	if err != nil {
		return fmt.Errorf("inspect OSM reader privileges: %w", err)
	}
	if !readerOnly {
		return fmt.Errorf("OSM role must have SELECT without table writes or mutating checkpoint execution")
	}
	return nil
}

func TestCoverageAcceptanceSafetyConfig(t *testing.T) {
	for _, name := range []string{"PGSERVICE", "PGSERVICEFILE", "PGSSLMODE", "PGSSLROOTCERT", "PGSSLCERT", "PGSSLKEY"} {
		t.Setenv(name, "")
	}
	const service = "acceptance-postgres.workouts-acceptance.svc.cluster.local:5432"
	for _, test := range []struct {
		name, address, path, database string
		want                          bool
	}{
		{"CI", "127.0.0.1:5432", "coverage_test", "coverage_test", true},
		{"first integration CI config", "127.0.0.1:5432", "coverage_test", "", true},
		{"first integration scratch config", "127.0.0.1:15432", "acceptance_test", "", true},
		{"benchmark is not an integration app", "127.0.0.1:15432", "acceptance_benchmark", "", false},
		{"scratch", "127.0.0.1:15432", "acceptance_test?sslmode=disable", "acceptance_test", true},
		{"localhost", "localhost:15432", "acceptance_test_osm", "acceptance_test_osm", true},
		{"service", service, "coverage_test_osm?sslmode=require", "coverage_test_osm", true},
		{"benchmark", "127.0.0.1:15432", "acceptance_benchmark?sslmode=disable", "acceptance_benchmark", true},
		{"benchmark localhost", "localhost:15432", "osm?sslmode=disable", "osm", true},
		{"benchmark service", service, "osm?sslmode=require", "osm", true},
		{"host override", "127.0.0.1:15432", "acceptance_test?host=example.invalid&sslmode=disable", "acceptance_test", false},
		{"database override", "127.0.0.1:15432", "acceptance_test?dbname=workouts&sslmode=disable", "acceptance_test", false},
		{"allowed host override", "127.0.0.1:15432", "acceptance_test?host=localhost", "acceptance_test", false},
		{"duplicate sslmode", "127.0.0.1:15432", "acceptance_test?sslmode=disable&sslmode=require", "acceptance_test", false},
		{"remote acceptance name", "example.invalid:5432", "acceptance_test", "acceptance_test", false},
		{"production acceptance name", "127.0.0.1:15432", "production_acceptance", "production_acceptance", false},
		{"name substring", "127.0.0.1:15432", "acceptance_live", "acceptance_test", false},
		{"remote fallback", "127.0.0.1:15432,example.invalid:5432", "acceptance_test?sslmode=disable", "acceptance_test", false},
		{"benchmark remote fallback", "127.0.0.1:15432,example.invalid:5432", "osm?sslmode=disable", "osm", false},
		{"approved multi host", "127.0.0.1:15432,localhost:15432", "acceptance_test?sslmode=disable", "acceptance_test", false},
		{"same multi host", "127.0.0.1:15432,127.0.0.1:15432", "acceptance_test?sslmode=disable", "acceptance_test", false},
		{"benchmark CI port", "127.0.0.1:5432", "osm?sslmode=disable", "osm", false},
		{"unapproved port", "localhost:5433", "coverage_test", "coverage_test", false},
		{"service wrong port", "acceptance-postgres.workouts-acceptance.svc.cluster.local:15432", "coverage_test", "coverage_test", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := "postgresql://workouts_migration:acceptance-only@" + test.address + "/" + test.path
			_, err := parseCoverageAcceptanceConfig(value, test.database)
			if (err == nil) != test.want {
				t.Fatalf("URL safety accepted=%t, want %t: %v", err == nil, test.want, err)
			}
			config, err := pgxpool.ParseConfig(value)
			if err != nil {
				t.Fatal(err)
			}
			// These query overrides must also fail after pgx has resolved them.
			if test.name == "host override" || test.name == "database override" || strings.HasSuffix(test.name, "remote fallback") {
				if err := validateCoverageAcceptanceConfig(config, test.database); err == nil {
					t.Fatal("effective connection configuration bypassed the guard")
				}
			}
		})
	}
	for _, mode := range []string{"disable", "allow", "prefer", "require", "verify-ca", "verify-full"} {
		t.Run("sslmode="+mode, func(t *testing.T) {
			if _, err := parseCoverageAcceptanceConfig("postgresql://workouts_migration:acceptance-only@localhost:5432/coverage_test?sslmode="+mode, "coverage_test"); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCoverageAcceptanceSafetyPair(t *testing.T) {
	for _, test := range []struct {
		name, app, osm, appAddress, osmAddress string
		want                                   bool
	}{
		{"CI", "coverage_test", "coverage_test_osm", "127.0.0.1:5432", "127.0.0.1:5432", true},
		{"scratch", "acceptance_test", "acceptance_test_osm", "localhost:15432", "localhost:15432", true},
		{"benchmark", "acceptance_benchmark", "osm", "127.0.0.1:15432", "127.0.0.1:15432", true},
		{"wrong pair", "coverage_test", "acceptance_test_osm", "127.0.0.1:5432", "127.0.0.1:5432", false},
		{"same database", "acceptance_test", "acceptance_test", "127.0.0.1:15432", "127.0.0.1:15432", false},
		{"different endpoint", "acceptance_test", "acceptance_test_osm", "127.0.0.1:15432", "127.0.0.1:5432", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			parse := func(address, database string) *pgxpool.Config {
				config, err := pgxpool.ParseConfig("postgresql://workouts_migration:acceptance-only@" + address + "/" + database + "?sslmode=disable")
				if err != nil {
					t.Fatal(err)
				}
				return config
			}
			err := validateCoverageAcceptancePair(parse(test.appAddress, test.app), parse(test.osmAddress, test.osm))
			if (err == nil) != test.want {
				t.Fatalf("pair safety accepted=%t, want %t: %v", err == nil, test.want, err)
			}
		})
	}
}

func TestCoverageAcceptanceSafetyFallbacks(t *testing.T) {
	config, err := parseCoverageAcceptanceConfig("postgresql://workouts_migration:acceptance-only@127.0.0.1:15432/acceptance_test?sslmode=prefer", "acceptance_test")
	if err != nil {
		t.Fatal(err)
	}
	if len(config.ConnConfig.Fallbacks) == 0 {
		t.Fatal("sslmode=prefer should exercise pgx's same-endpoint SSL fallback")
	}
	for _, test := range []struct {
		name   string
		mutate func(*pgxpool.Config)
	}{
		{"remote", func(c *pgxpool.Config) { c.ConnConfig.Fallbacks[0].Host = "example.invalid" }},
		{"other approved host", func(c *pgxpool.Config) { c.ConnConfig.Fallbacks[0].Host = "localhost" }},
		{"other approved port", func(c *pgxpool.Config) { c.ConnConfig.Fallbacks[0].Port = 5432 }},
		{"nil fallback", func(c *pgxpool.Config) { c.ConnConfig.Fallbacks[0] = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			mutated := config.Copy()
			test.mutate(mutated)
			if err := validateCoverageAcceptanceConfig(mutated, "acceptance_test"); err == nil {
				t.Fatal("mutated fallback passed the destination guard")
			}
		})
	}
}

func TestCoverageAcceptanceSafetyShell(t *testing.T) {
	bin := t.TempDir()
	// Stub every external command so this wrapper test cannot migrate or connect.
	for _, command := range []string{"go", "psql"} {
		if err := os.WriteFile(filepath.Join(bin, command), []byte("#!/bin/sh\nprintf 'fixture-command\\n'\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	names := []string{"MIGRATION_DATABASE_URL", "API_DATABASE_URL", "WORKER_DATABASE_URL", "COVERAGE_WORKER_DATABASE_URL", "OSM_MIGRATION_DATABASE_URL", "COVERAGE_WORKER_OSM_DATABASE_URL"}
	for _, test := range []struct {
		name, endpoint, appDatabase, osmDatabase, query string
		changeIndex                                     int
		want                                            bool
	}{
		{"CI", "127.0.0.1:5432", "coverage_test", "coverage_test_osm", "", 0, true},
		{"scratch", "127.0.0.1:15432", "acceptance_test", "acceptance_test_osm", "?sslmode=disable", 0, true},
		{"service", "acceptance-postgres.workouts-acceptance.svc.cluster.local:5432", "acceptance_test", "acceptance_test_osm", "?sslmode=require", 0, true},
		{"host override", "127.0.0.1:15432", "acceptance_test", "acceptance_test_osm", "?host=example.invalid", 0, false},
		{"database override", "127.0.0.1:15432", "acceptance_test", "acceptance_test_osm", "?dbname=workouts", 0, false},
		{"OSM override before app migration", "127.0.0.1:15432", "acceptance_test", "acceptance_test_osm", "?host=example.invalid&dbname=osm", 5, false},
		{"port override", "127.0.0.1:15432", "acceptance_test", "acceptance_test_osm", "?sslmode=disable&port=5432", 0, false},
		{"duplicate sslmode", "127.0.0.1:15432", "acceptance_test", "acceptance_test_osm", "?sslmode=disable&sslmode=require", 0, false},
		{"invalid sslmode", "127.0.0.1:15432", "acceptance_test", "acceptance_test_osm", "?sslmode=invalid", 0, false},
		{"encoded override", "127.0.0.1:15432", "acceptance_test", "acceptance_test_osm", "?%68ost=example.invalid", 0, false},
		{"remote primary", "example.invalid:5432", "acceptance_test", "acceptance_test_osm", "", 0, false},
		{"multi host", "127.0.0.1:15432,example.invalid:5432", "acceptance_test", "acceptance_test_osm", "", 0, false},
		{"unapproved port", "127.0.0.1:5433", "coverage_test", "coverage_test_osm", "", 0, false},
		{"wrong pair", "127.0.0.1:15432", "coverage_test", "acceptance_test_osm", "", 0, false},
		{"production name", "127.0.0.1:15432", "production_acceptance", "acceptance_test_osm", "", 0, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command("bash", "../scripts/test-coverage-worker.sh")
			command.Env = []string{"PATH=" + bin, "COVERAGE_ACCEPTANCE_DISPOSABLE=true"}
			for i, name := range names {
				database := test.appDatabase
				if i >= 4 {
					database = test.osmDatabase
				}
				query := ""
				if i == test.changeIndex {
					query = test.query
				}
				command.Env = append(command.Env, name+"=postgresql://workouts_migration:acceptance-only@"+test.endpoint+"/"+database+query)
			}
			output, err := command.CombinedOutput()
			if (err == nil) != test.want {
				t.Fatalf("shell safety accepted=%t, want %t: %v\n%s", err == nil, test.want, err, output)
			}
			calls := strings.Count(string(output), "fixture-command")
			if !test.want && calls != 0 || test.want && calls != 4 {
				t.Fatalf("external commands invoked=%d; rejected URLs must stop before any migration", calls)
			}
		})
	}
}

func TestCoverageAcceptanceOSMReaderMutationIntegration(t *testing.T) {
	f := newCoverageIntegrationFixture(t)
	for _, signature := range []string{
		"osm_catalog.checkpoint_generation_stage(bigint,text,bigint,jsonb,bigint)",
		"osm_catalog.complete_generation_stage(bigint,text,bigint,jsonb,bigint)",
	} {
		t.Run(signature, func(t *testing.T) {
			var exists bool
			if err := f.osmAdminDB.QueryRow(f.ctx, `SELECT to_regprocedure($1) IS NOT NULL`, signature).Scan(&exists); err != nil {
				t.Fatal(err)
			}
			if !exists {
				t.Skip("checkpoint function is absent")
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
				defer cancel()
				if _, err := f.osmAdminDB.Exec(ctx, "REVOKE EXECUTE ON FUNCTION "+signature+" FROM workouts_coverage_worker"); err != nil {
					t.Error(err)
				}
			})
			if _, err := f.osmAdminDB.Exec(f.ctx, "GRANT EXECUTE ON FUNCTION "+signature+" TO workouts_coverage_worker"); err != nil {
				t.Fatal(err)
			}
			if err := validateCoverageAcceptanceOSMReader(f.ctx, f.osmDB); err == nil {
				t.Fatal("executable mutating SECURITY DEFINER function passed the OSM reader guard")
			}
		})
	}
	if err := validateCoverageAcceptanceOSMReader(f.ctx, f.osmDB); err != nil {
		t.Fatal(err)
	}
}
