package migrations

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// Names and security attributes are retained along with case/whitespace-normalized
// function definitions. The digest is independent of database object identifiers.
func canonicalFunctionContract(ctx context.Context, db *sql.DB) (string, error) {
	rows, err := db.QueryContext(ctx, `SELECT p.proname,pg_get_function_identity_arguments(p.oid),
		pg_get_userbyid(p.proowner),p.proacl::text,p.provolatile,p.prosecdef,p.proisstrict,p.proparallel,p.proleakproof,
		p.proconfig::text,pg_get_functiondef(p.oid)
		FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='app'
		ORDER BY p.proname,pg_get_function_identity_arguments(p.oid)`)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var contract strings.Builder
	for rows.Next() {
		var name, arguments, owner, volatility, parallel, definition string
		var acl, config sql.NullString
		var security, strict, leakproof bool
		if err := rows.Scan(&name, &arguments, &owner, &acl, &volatility, &security, &strict, &parallel, &leakproof, &config, &definition); err != nil {
			return "", err
		}
		fmt.Fprintf(&contract, "%s|%s|%s|%s|%s|%t|%t|%s|%t|%s|%s\n", name, arguments, owner, acl.String, volatility, security, strict, parallel, leakproof, config.String, strings.Join(strings.Fields(strings.ToLower(definition)), " "))
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256([]byte(contract.String()))), nil
}

func TestCanonicalHistoryPostgreSQL(t *testing.T) {
	dsn := os.Getenv("PROJECT_CANONICAL_HISTORY_TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("PROJECT_CANONICAL_HISTORY_TEST_DISPOSABLE") != "1" {
		t.Skip("explicit disposable canonical-history database required")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if config.Host != "127.0.0.1" && config.Host != "::1" && !strings.HasPrefix(config.Host, "/tmp/") {
		t.Fatal("local disposable cluster required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	admin := stdlib.OpenDB(*config)
	defer admin.Close()
	execute := func(t *testing.T, db *sql.DB, query string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	serial := 0
	newDB := func(t *testing.T, template string) (*sql.DB, *sql.DB, string, string) {
		t.Helper()
		serial++
		name := fmt.Sprintf("canonical_history_%d_%d", os.Getpid(), serial)
		query := "CREATE DATABASE " + name + " OWNER workouts_migration"
		if template != "" {
			query += " TEMPLATE " + template
		}
		execute(t, admin, query)
		ownerConfig := config.Copy()
		ownerConfig.Database = name
		owner := stdlib.OpenDB(*ownerConfig)
		migrationConfig := ownerConfig.Copy()
		migrationConfig.User = "workouts_migration"
		migrationConfig.Password = ""
		db := stdlib.OpenDB(*migrationConfig)
		db.SetMaxOpenConns(1)
		t.Cleanup(func() {
			db.Close()
			owner.Close()
			cleanup := stdlib.OpenDB(*config)
			defer cleanup.Close()
			if _, err := cleanup.ExecContext(context.Background(), "DROP DATABASE "+name); err != nil {
				t.Errorf("drop owned disposable database: %v", err)
			}
		})
		cliURL := url.URL{Scheme: "postgresql", User: url.User("workouts_migration"), Path: "/" + name}
		cliURL.RawQuery = url.Values{"host": {config.Host}, "port": {fmt.Sprint(config.Port)}, "sslmode": {"disable"}}.Encode()
		return db, owner, name, cliURL.String()
	}
	data := func(t *testing.T, db *sql.DB) map[string]string {
		t.Helper()
		rows, err := db.QueryContext(ctx, `SELECT tablename FROM pg_tables WHERE schemaname IN ('app','public') AND (schemaname='app' OR tablename='goose_db_version') ORDER BY schemaname,tablename`)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				t.Fatal(err)
			}
			names = append(names, name)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		result := map[string]string{}
		for _, name := range names {
			schema := "app"
			if name == "goose_db_version" {
				schema = "public"
			}
			var value string
			if err := db.QueryRowContext(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(v) ORDER BY to_jsonb(v)::text),'[]')::text FROM `+pgx.Identifier{schema, name}.Sanitize()+` v`).Scan(&value); err != nil {
				t.Fatal(err)
			}
			result[name] = value
		}
		return result
	}
	validateUnchanged := func(t *testing.T, db, owner *sql.DB, wantError bool) {
		t.Helper()
		before := data(t, owner)
		err := ValidateHistory(ctx, db)
		if (err != nil) != wantError {
			t.Fatalf("ValidateHistory error=%v, want error=%t", err, wantError)
		}
		if err != nil && !strings.Contains(err.Error(), "operator recovery procedure") {
			t.Fatalf("missing safe recovery instruction: %v", err)
		}
		if !reflect.DeepEqual(before, data(t, owner)) {
			t.Fatal("history validation changed database rows")
		}
	}
	goose.SetBaseFS(Files)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	active, err := goose.CollectMigrations(".", 0, 1<<62)
	if err != nil {
		t.Fatal(err)
	}
	maximum := active[len(active)-1].Version
	assertCanonical := func(t *testing.T, db *sql.DB) {
		t.Helper()
		var valid bool
		if err := db.QueryRowContext(ctx, `SELECT schema_version=27 AND minimum_runtime_version=26 AND migration_baseline=27
		AND (SELECT count(*)=28 AND count(DISTINCT version_id)=28 AND min(version_id)=0 AND max(version_id)=27 AND bool_and(is_applied) FROM public.goose_db_version)
		AND to_regprocedure('app.read_coverage_retry_eligibility(uuid,integer)') IS NOT NULL
		AND to_regprocedure('app.read_job_history(text,text,integer,bigint,integer)') IS NOT NULL
		AND (SELECT NOT rolsuper AND NOT rolbypassrls FROM pg_roles WHERE rolname='workouts_migration')
		AND (SELECT bool_and(has_column_privilege(role_name,'app.schema_metadata','migration_baseline','SELECT')) FROM unnest(ARRAY['workouts_api','workouts_worker','workouts_coverage_worker']) role_name)
		FROM app.schema_metadata WHERE singleton`).Scan(&valid); err != nil || !valid {
			t.Fatalf("invalid canonical metadata/features: %t %v", valid, err)
		}
	}
	base, baseOwner, template, _ := newDB(t, "")
	execute(t, baseOwner, "CREATE EXTENSION postgis")
	validateUnchanged(t, base, baseOwner, false)
	var ledgerAbsent bool
	if err := base.QueryRowContext(ctx, `SELECT to_regclass('public.goose_db_version') IS NULL`).Scan(&ledgerAbsent); err != nil || !ledgerAbsent {
		t.Fatal("validator created migration ledger")
	}
	if err := goose.UpToContext(ctx, base, ".", 26); err != nil {
		t.Fatal(err)
	}
	execute(t, baseOwner, `SELECT set_config('app.account_id','00000000-0000-4000-8000-000000000001',true);
	INSERT INTO app.accounts(id) VALUES('00000000-0000-4000-8000-000000000001');
	INSERT INTO app.sources(id,account_id,display_name,canonical_display_name,type,config_envelope)
	VALUES('00000000-0000-4000-8000-000000000002','00000000-0000-4000-8000-000000000001','Canonical fixture','canonical fixture','health-auto-export-local',decode('00','hex'))`)
	validateUnchanged(t, base, baseOwner, false)
	base.Close()
	baseOwner.Close()
	var expectedFunctions string
	t.Run("ordinary upgrade", func(t *testing.T) {
		db, owner, _, _ := newDB(t, template)
		before := data(t, owner)
		if err := goose.UpToContext(ctx, db, ".", 27); err != nil {
			t.Fatal(err)
		}
		assertCanonical(t, owner)
		validateUnchanged(t, db, owner, false)
		after := data(t, owner)
		var historyRetained bool
		if err := owner.QueryRowContext(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(v) ORDER BY to_jsonb(v)::text),'[]') = $1::jsonb
			FROM public.goose_db_version v WHERE version_id<27`, before["goose_db_version"]).Scan(&historyRetained); err != nil || !historyRetained {
			t.Fatalf("ordinary migration changed applied history: %v", err)
		}
		for _, table := range []string{"accounts", "sources", "source_sync_state"} {
			if before[table] != after[table] {
				t.Fatalf("upgrade changed %s", table)
			}
		}
		expectedFunctions, err = canonicalFunctionContract(ctx, owner)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("canonical function contract SHA256=%s", expectedFunctions)
		if expectedFunctions != "103e898d42709d7bca91d0cef368f7f5615549ce8574f25d9edcc581326972e9" {
			t.Fatal("canonical function/security contract changed")
		}
		objects := `SELECT jsonb_object_agg(p.proname||'('||pg_get_function_identity_arguments(p.oid)||')',p.oid)::text FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname='app'`
		var beforeObjects, afterObjects string
		if err := owner.QueryRowContext(ctx, objects).Scan(&beforeObjects); err != nil {
			t.Fatal(err)
		}
		unchanged := data(t, owner)
		if err := goose.UpToContext(ctx, db, ".", 27); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(unchanged, data(t, owner)) {
			t.Fatal("already-applied canonical migration replayed")
		}
		if err := owner.QueryRowContext(ctx, objects).Scan(&afterObjects); err != nil || beforeObjects != afterObjects {
			t.Fatalf("already-applied function OIDs changed: %v", err)
		}
	})
	t.Run("semantic checkpoints", func(t *testing.T) {
		db, owner, _, _ := newDB(t, template)
		raw, err := Files.ReadFile("027_coverage_recovery_baseline.sql")
		if err != nil {
			t.Fatal(err)
		}
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		for _, phase := range []string{"route-counts", "auto-retries", "runtime-capability", "diagnostic-limits", "retry-presentation", "canonical-metadata"} {
			_, rest, found := strings.Cut(string(raw), "-- coverage-contract:"+phase+":begin\n")
			body, _, ended := strings.Cut(rest, "-- coverage-contract:"+phase+":end")
			if !found || !ended {
				t.Fatal("checkpoint missing")
			}
			if _, err := tx.ExecContext(ctx, body); err != nil {
				t.Fatalf("checkpoint %s: %v", phase, err)
			}
			if phase != "canonical-metadata" {
				var valid bool
				if err := tx.QueryRowContext(ctx, `SELECT schema_version=26 AND minimum_runtime_version=26 AND NOT(to_jsonb(m)?'migration_baseline') FROM app.schema_metadata m WHERE singleton`).Scan(&valid); err != nil || !valid {
					t.Fatalf("checkpoint %s changed metadata: %v", phase, err)
				}
			}
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO public.goose_db_version(version_id,is_applied) VALUES(27,true)`); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
		assertCanonical(t, owner)
		got, err := canonicalFunctionContract(ctx, owner)
		if err != nil || got != expectedFunctions {
			t.Fatalf("checkpoint function contract differs: %s %v", got, err)
		}
	})
	for name, setup := range map[string]string{
		"missing active entry":    `TRUNCATE public.goose_db_version; INSERT INTO public.goose_db_version(version_id,is_applied) SELECT v,true FROM generate_series(0,26) v WHERE v<>25`,
		"duplicate":               `INSERT INTO public.goose_db_version(version_id,is_applied) VALUES(25,true)`,
		"unapplied":               `UPDATE public.goose_db_version SET is_applied=false WHERE version_id=26`,
		"metadata mismatch":       `UPDATE app.schema_metadata SET schema_version=25,minimum_runtime_version=25`,
		"unmarked coverage":       `UPDATE app.schema_metadata SET schema_version=27; INSERT INTO public.goose_db_version(version_id,is_applied) VALUES(27,true)`,
		"premature marker":        `ALTER TABLE app.schema_metadata ADD COLUMN migration_baseline integer; UPDATE app.schema_metadata SET migration_baseline=27`,
		"unknown marker":          `ALTER TABLE app.schema_metadata ADD COLUMN migration_baseline integer; UPDATE app.schema_metadata SET schema_version=27,migration_baseline=99; INSERT INTO public.goose_db_version(version_id,is_applied) VALUES(27,true)`,
		"wrong canonical minimum": `ALTER TABLE app.schema_metadata ADD COLUMN migration_baseline integer; UPDATE app.schema_metadata SET schema_version=27,minimum_runtime_version=27,migration_baseline=27; INSERT INTO public.goose_db_version(version_id,is_applied) VALUES(27,true)`,
		"empty ledger":            `TRUNCATE public.goose_db_version`,
		"negative entry":          `INSERT INTO public.goose_db_version(version_id,is_applied) VALUES(-1,true)`,
		"missing metadata":        `DROP TABLE app.schema_metadata`,
		"missing singleton":       `TRUNCATE app.schema_metadata`,
		"missing ledger":          `DROP TABLE public.goose_db_version`,
		"unknown future":          fmt.Sprintf(`ALTER TABLE app.schema_metadata ADD COLUMN migration_baseline integer; UPDATE app.schema_metadata SET schema_version=%d,migration_baseline=27; INSERT INTO public.goose_db_version(version_id,is_applied) SELECT v,true FROM generate_series(27,%d) v`, maximum+1, maximum+1),
	} {
		t.Run(name, func(t *testing.T) {
			db, owner, _, _ := newDB(t, template)
			execute(t, owner, setup)
			validateUnchanged(t, db, owner, true)
		})
	}
	for name, setup := range map[string]string{
		"partial DDL":   `CREATE FUNCTION public.reject_coverage_ddl() RETURNS event_trigger LANGUAGE plpgsql AS $$ BEGIN IF EXISTS(SELECT 1 FROM pg_event_trigger_ddl_commands() WHERE object_identity LIKE 'app.read_job_history(%') THEN RAISE EXCEPTION 'injected DDL failure'; END IF; END $$; CREATE EVENT TRIGGER reject_coverage_ddl ON ddl_command_end EXECUTE FUNCTION public.reject_coverage_ddl()`,
		"ledger insert": `CREATE FUNCTION public.reject_coverage_insert() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.version_id=27 THEN RAISE EXCEPTION 'injected ledger failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_coverage_insert BEFORE INSERT ON public.goose_db_version FOR EACH ROW EXECUTE FUNCTION public.reject_coverage_insert()`,
	} {
		t.Run(name, func(t *testing.T) {
			db, owner, _, _ := newDB(t, template)
			execute(t, owner, setup)
			before := data(t, owner)
			beforeFunctions, err := canonicalFunctionContract(ctx, owner)
			if err != nil {
				t.Fatal(err)
			}
			if err := goose.UpToContext(ctx, db, ".", 27); err == nil || !strings.Contains(err.Error(), "injected") {
				t.Fatalf("expected injected failure: %v", err)
			}
			afterFunctions, err := canonicalFunctionContract(ctx, owner)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, data(t, owner)) || beforeFunctions != afterFunctions {
				t.Fatal("failed ordinary migration changed data/catalog/ledger")
			}
			validateUnchanged(t, db, owner, false)
		})
	}
	t.Run("native CLI", func(t *testing.T) {
		db, owner, _, cliDSN := newDB(t, template)
		if err := goose.UpToContext(ctx, db, ".", 27); err != nil {
			t.Fatal(err)
		}
		before := data(t, owner)["goose_db_version"]
		binary := filepath.Join(t.TempDir(), "migrate")
		if out, err := exec.CommandContext(ctx, "go", "build", "-o", binary, "../cmd/migrate").CombinedOutput(); err != nil {
			t.Fatalf("build native CLI: %v\n%s", err, out)
		}
		command := exec.CommandContext(ctx, binary)
		command.Env = append(os.Environ(), "MIGRATION_DATABASE_URL="+cliDSN)
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("native CLI: %v\n%s", err, out)
		}
		validateUnchanged(t, db, owner, false)
		var applied int64
		if err := db.QueryRowContext(ctx, `SELECT schema_version FROM app.schema_metadata WHERE singleton`).Scan(&applied); err != nil || applied != maximum {
			t.Fatalf("native CLI version=%d want=%d err=%v", applied, maximum, err)
		}
		var retained bool
		if err := owner.QueryRowContext(ctx, `SELECT coalesce(jsonb_agg(to_jsonb(v) ORDER BY to_jsonb(v)::text),'[]') @> $1::jsonb FROM public.goose_db_version v WHERE version_id<=27`, before).Scan(&retained); err != nil || !retained {
			t.Fatalf("native CLI rewrote applied history: %v", err)
		}
		unchanged := data(t, owner)
		command = exec.CommandContext(ctx, binary)
		command.Env = append(os.Environ(), "MIGRATION_DATABASE_URL="+cliDSN)
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("native CLI rerun: %v\n%s", err, out)
		}
		if !reflect.DeepEqual(unchanged, data(t, owner)) {
			t.Fatal("native CLI rerun mutated canonical history")
		}
		execute(t, owner, fmt.Sprintf(`UPDATE app.schema_metadata SET schema_version=%d;
			INSERT INTO public.goose_db_version(version_id,is_applied) VALUES(%d,true)`, maximum+1, maximum+1))
		unknown := data(t, owner)
		command = exec.CommandContext(ctx, binary)
		command.Env = append(os.Environ(), "MIGRATION_DATABASE_URL="+cliDSN)
		if out, err := command.CombinedOutput(); err == nil || !strings.Contains(string(out), "operator recovery procedure") {
			t.Fatalf("native CLI did not safely reject unknown history: %v\n%s", err, out)
		}
		if !reflect.DeepEqual(unknown, data(t, owner)) {
			t.Fatal("native CLI mutated rejected history")
		}
	})
}
