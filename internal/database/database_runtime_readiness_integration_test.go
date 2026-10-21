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
	"github.com/erhhung/workouts-explorer/internal/testutil/migrationfixture"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// This suite owns an empty, explicitly named loopback scratch database. It never
// loads application configuration, profiles, filesystem exports or live URLs.
func TestRuntimeCapabilityPostgreSQL(t *testing.T) {
	dsn := os.Getenv("PROJECT_RUNTIME_CAPABILITY_TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("PROJECT_RUNTIME_CAPABILITY_TEST_DISPOSABLE") != "1" {
		t.Skip("explicit disposable runtime-capability database is required")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid disposable runtime-capability configuration")
	}
	if !strings.HasPrefix(config.ConnConfig.Database, "workouts_runtime_capability_test_") ||
		(config.ConnConfig.Host != "127.0.0.1" && config.ConnConfig.Host != "localhost" && config.ConnConfig.Host != "::1") {
		t.Fatal("runtime-capability tests require a named loopback scratch database")
	}
	for _, fallback := range config.ConnConfig.Fallbacks {
		if fallback.Host != config.ConnConfig.Host || fallback.Port != config.ConnConfig.Port {
			t.Fatal("runtime-capability tests prohibit remote/fallback endpoints")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	config.MaxConns = 3
	config.ConnConfig.RuntimeParams["statement_timeout"] = "5s"
	admin, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal("create disposable runtime-capability pool")
	}
	t.Cleanup(admin.Close)
	var empty bool
	if err := admin.QueryRow(ctx, `SELECT to_regnamespace('app') IS NULL AND to_regclass('public.goose_db_version') IS NULL`).Scan(&empty); err != nil || !empty {
		t.Fatal("runtime-capability scratch database must be empty")
	}
	if _, err := admin.Exec(ctx, `CREATE EXTENSION postgis`); err != nil {
		t.Fatal(err)
	}
	migrationConfig := config.ConnConfig.Copy()
	migrationConfig.RuntimeParams["role"] = "workouts_migration"
	sqlDB := stdlib.OpenDB(*migrationConfig)
	defer sqlDB.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 26); err != nil {
		t.Fatal(err)
	}
	begin := func(t *testing.T, role string, account uuid.UUID) pgx.Tx {
		t.Helper()
		tx, err := admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
		if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+role); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, account.String()); err != nil {
			t.Fatal(err)
		}
		return tx
	}
	exec := func(tx pgx.Tx, query string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	type fixture struct{ account, source, parent, job, lease uuid.UUID }
	seed := func() fixture {
		f := fixture{uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()}
		tx := begin(t, "workouts_migration", f.account)
		exec(tx, `INSERT INTO app.accounts(id) VALUES($1)`, f.account)
		exec(tx, `INSERT INTO app.sources(id,account_id,display_name,canonical_display_name,type,status,config_envelope)
			VALUES($1,$2,'Synthetic runtime-capability','synthetic runtime-capability','health-auto-export-local','connected',$3)`, f.source, f.account, []byte(`{"synthetic":true}`))
		exec(tx, `INSERT INTO app.jobs(id,account_id,kind,priority) VALUES($1,$2,'manual_ingest',80)`, f.parent, f.account)
		exec(tx, `INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority,parameters)
			VALUES($1,$2,$3,'manual_ingest_source',80,$4)`, f.job, f.parent, f.account,
			fmt.Sprintf(`{"sourceId":"%s","generation":1,"mode":"incremental"}`, strings.ToUpper(strings.ReplaceAll(f.source.String(), "-", ""))))
		exec(tx, `INSERT INTO app.job_config_snapshots(job_id,account_id,source_id,source_generation,config_envelope)
			VALUES($1,$2,$3,1,$4)`, f.job, f.account, f.source, []byte(`{"synthetic":true}`))
		exec(tx, `INSERT INTO app.job_progress(job_id,account_id) VALUES($1,$3),($2,$3)`, f.parent, f.job, f.account)
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		tx = begin(t, "workouts_worker", f.account)
		var claimed bool
		if err := tx.QueryRow(ctx, `SELECT app.claim_job($1,'runtime-capability-worker',$2,interval '30 minutes')`, f.job, f.lease).Scan(&claimed); err != nil || !claimed {
			t.Fatalf("claim synthetic ingest: %t %v", claimed, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return f
	}
	live, legacy, routed := seed(), seed(), seed()
	// Historical totals without retained events are unknown, not zero. Empty
	// updated totals remain provable. No history is enrolled for automatic retry.
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	exec(tx, `SET LOCAL session_replication_role='replica'`)
	exec(tx, `UPDATE app.jobs SET status='succeeded',terminal_at=clock_timestamp(),worker_id=NULL,lease_token=NULL,
		claimed_at=NULL,heartbeat_at=NULL,lease_expires_at=NULL WHERE id=ANY($1::uuid[])`, []uuid.UUID{legacy.job, legacy.parent})
	exec(tx, `UPDATE app.job_progress SET workouts_created=7 WHERE job_id=ANY($1::uuid[])`, []uuid.UUID{legacy.job, legacy.parent})
	file, workout, activity := uuid.New(), uuid.New(), uuid.New()
	exec(tx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes,state,checksum_sha256,processing_started_at,processed_at)
		VALUES($1,$2,$3,$4,'HealthAutoExport-2026-01-01.json',1,'succeeded',decode(repeat('01',32),'hex'),now(),now())`, file, routed.account, routed.source, routed.job)
	exec(tx, `INSERT INTO app.workout_types(id,account_id,type_key,provider_label) VALUES($1,$2,'walking','Walking')`, activity, routed.account)
	exec(tx, `INSERT INTO app.workouts(id,account_id,source_id,source_file_id,workout_type_id,provider_id,content_sha256,provider_label,started_at,ended_at,provider_duration)
		VALUES($1,$2,$3,$4,$5,'synthetic-runtime-capability',decode(repeat('01',32),'hex'),'Walking','2026-01-01Z','2026-01-01Z',0)`, workout, routed.account, routed.source, file, activity)
	exec(tx, `INSERT INTO app.workout_route_points(account_id,workout_id,sequence,recorded_at,latitude,longitude)
		VALUES($1,$2,0,'2026-01-01Z',0,0)`, routed.account, workout)
	exec(tx, `INSERT INTO app.workout_import_events(id,account_id,source_id,source_file_id,job_id,workout_id,kind,content_sha256)
		SELECT gen_random_uuid(),$1,$2,$3,$4,$5,kind,decode(repeat('01',32),'hex') FROM unnest(ARRAY['created','updated','adopted']) kind`, routed.account, routed.source, file, routed.job, workout)
	exec(tx, `UPDATE app.jobs SET status='succeeded',terminal_at=clock_timestamp(),worker_id=NULL,lease_token=NULL,
		claimed_at=NULL,heartbeat_at=NULL,lease_expires_at=NULL WHERE id=ANY($1::uuid[])`, []uuid.UUID{routed.job, routed.parent})
	exec(tx, `UPDATE app.job_progress SET workouts_created=1,workouts_updated=2 WHERE job_id=ANY($1::uuid[])`, []uuid.UUID{routed.job, routed.parent})
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []string{migrationfixture.RouteCounts, migrationfixture.AutoRetries} {
		if err := migrationfixture.ApplyPhase(ctx, sqlDB, migrations.Files, phase); err != nil {
			t.Fatal(err)
		}
	}
	const signature = "app.read_ingest_candidate_actions(uuid,text,uuid,jsonb)"
	const anchor = "app.fence_ingest_job(job, worker, lease, 26)"
	var original, contract string
	const fingerprint = `SELECT jsonb_build_object('oid',oid,'owner',proowner,'acl',proacl,'config',proconfig,
		'definer',prosecdef,'volatility',provolatile,'args',proargnames,'result',pg_get_function_result(oid))::text
		FROM pg_proc WHERE oid=$1::regprocedure`
	if err := admin.QueryRow(ctx, `SELECT pg_get_functiondef($1::regprocedure)`, signature).Scan(&original); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, fingerprint, signature).Scan(&contract); err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"missing", "duplicate"} {
		t.Run("migration rejects "+scenario+" exact anchor", func(t *testing.T) {
			mutated := strings.Replace(original, anchor, "app.fence_ingest_job(job, worker, lease, (26))", 1)
			if scenario == "duplicate" {
				mutated = strings.Replace(original, "\nBEGIN\n", "\n-- "+anchor+"\nBEGIN\n", 1)
			}
			if mutated == original {
				t.Fatal("anchor fixture did not change")
			}
			if _, err := admin.Exec(ctx, mutated); err != nil {
				t.Fatal(err)
			}
			err := migrationfixture.ApplyPhase(ctx, sqlDB, migrations.Files, migrationfixture.RuntimeCapability)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "P0001" || !strings.Contains(pgErr.Message, "anchor must occur exactly once") {
				t.Fatalf("unsafe rewrite was not rejected: %v", err)
			}
			var rolledBack bool
			if err := admin.QueryRow(ctx, `SELECT (SELECT count(*)=27 AND min(version_id)=0 AND max(version_id)=26 AND bool_and(is_applied) FROM public.goose_db_version)
				AND (SELECT schema_version=26 AND minimum_runtime_version=26 FROM app.schema_metadata WHERE singleton)
				AND pg_get_functiondef($1::regprocedure)=$2`, signature, mutated).Scan(&rolledBack); err != nil || !rolledBack {
				t.Fatalf("failed migration did not roll back: %t %v", rolledBack, err)
			}
			if _, err := admin.Exec(ctx, original); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("late phase failure rolls back the function rewrite", func(t *testing.T) {
		phase, err := migrationfixture.PhaseSQL(migrations.Files, migrationfixture.RuntimeCapability)
		if err != nil {
			t.Fatal(err)
		}
		err = migrationfixture.ApplySQL(ctx, sqlDB, string(phase)+`
		DO $late_failure$ BEGIN
		    IF position('capability.runtime_version' IN pg_get_functiondef('app.read_ingest_candidate_actions(uuid,text,uuid,jsonb)'::regprocedure))=0
		    THEN RAISE EXCEPTION 'late failure ran before capability rewrite'; END IF;
		    RAISE EXCEPTION 'synthetic late capability failure' USING ERRCODE='23514';
		END; $late_failure$;`)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23514" {
			t.Fatalf("expected late capability failure: %v", err)
		}
		var rolledBack bool
		if err := admin.QueryRow(ctx, `SELECT pg_get_functiondef($1::regprocedure)=$2
			AND (SELECT schema_version=26 FROM app.schema_metadata WHERE singleton)
			AND (SELECT count(*)=27 AND min(version_id)=0 AND max(version_id)=26 AND bool_and(is_applied) FROM public.goose_db_version)`, signature, original).Scan(&rolledBack); err != nil || !rolledBack {
			t.Fatalf("late failure left partial capability phase: %t %v", rolledBack, err)
		}
	})
	if err := migrationfixture.ApplyPhase(ctx, sqlDB, migrations.Files, migrationfixture.RuntimeCapability); err != nil {
		t.Fatal(err)
	}
	var after string
	if err := admin.QueryRow(ctx, fingerprint, signature).Scan(&after); err != nil || contract != after {
		t.Fatalf("migration changed helper identity/ABI/security: %v", err)
	}
	var preserved bool
	if err := admin.QueryRow(ctx, `SELECT count(*)=2 AND bool_and(workouts_created=7 AND workouts_updated=0
		AND workouts_created_with_route IS NULL AND workouts_updated_with_route=0)
		FROM app.job_progress WHERE job_id=ANY($1::uuid[])`, []uuid.UUID{legacy.parent, legacy.job}).Scan(&preserved); err != nil || !preserved {
		t.Fatalf("legacy totals/unknown route counts changed: %t %v", preserved, err)
	}
	if err := admin.QueryRow(ctx, `SELECT count(*)=2 AND bool_and(workouts_created=1 AND workouts_updated=2
		AND workouts_created_with_route=1 AND workouts_updated_with_route=2)
		FROM app.job_progress WHERE job_id=ANY($1::uuid[])`, []uuid.UUID{routed.parent, routed.job}).Scan(&preserved); err != nil || !preserved {
		t.Fatalf("known legacy route-event counts not preserved: %t %v", preserved, err)
	}
	if err := admin.QueryRow(ctx, `SELECT (SELECT schema_version=26 AND minimum_runtime_version=26 FROM app.schema_metadata WHERE singleton)
		AND NOT EXISTS(SELECT 1 FROM app.coverage_retry_roots) AND NOT EXISTS(SELECT 1 FROM app.coverage_retry_attempts)`).Scan(&preserved); err != nil || !preserved {
		t.Fatalf("upgrade metadata or historical enrollment changed: %t %v", preserved, err)
	}
	// Feature checkpoints retain schema26 while the diagnostic-only change must
	// preserve helpers, ACLs, policies, provenance and counters.
	const diagnosticContract = `SELECT jsonb_build_object(
		'functions',(SELECT jsonb_agg(to_jsonb(p) ORDER BY p.oid) FROM pg_proc p WHERE pronamespace='app'::regnamespace),
		'policies',(SELECT jsonb_agg(to_jsonb(p) ORDER BY p.oid) FROM pg_policy p JOIN pg_class c ON c.oid=p.polrelid WHERE c.relnamespace='app'::regnamespace),
		'tables',(SELECT jsonb_agg(jsonb_build_array(oid,relowner,relacl,relrowsecurity,relforcerowsecurity) ORDER BY oid) FROM pg_class WHERE relnamespace='app'::regnamespace),
		'constraints',(SELECT jsonb_agg(to_jsonb(c) ORDER BY c.oid) FROM pg_constraint c WHERE connamespace='app'::regnamespace
		AND conname NOT IN ('coverage_diagnostic_runs_original_point_count_check','coverage_diagnostic_runs_sampled_point_count_check')),
		'progress',(SELECT jsonb_agg(to_jsonb(p) ORDER BY job_id) FROM app.job_progress p),
		'events',(SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM app.workout_import_events e))::text`
	var beforeParity, afterParity string
	if err := admin.QueryRow(ctx, diagnosticContract).Scan(&beforeParity); err != nil {
		t.Fatal(err)
	}
	if err := migrationfixture.ApplyPhase(ctx, sqlDB, migrations.Files, migrationfixture.DiagnosticLimits); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, diagnosticContract).Scan(&afterParity); err != nil || beforeParity != afterParity {
		t.Fatalf("diagnostic limits changed helper/security/provenance/counter contracts: %v", err)
	}
	if err := migrationfixture.FinishCanonical27(ctx, sqlDB, migrations.Files, migrationfixture.DiagnosticLimits); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `SELECT schema_version=27 AND minimum_runtime_version=26 AND migration_baseline=27 FROM app.schema_metadata WHERE singleton`).Scan(&preserved); err != nil || !preserved {
		t.Fatalf("canonical schema metadata: %t %v", preserved, err)
	}
	const capabilityRuntime = 27
	const candidates = `[{"name":"HealthAutoExport-2026-01-01.json","export_date":"2026-01-01","size":10,"modified_sec":100,"modified_ns":1,"device":2,"inode":3,"ctime_sec":101,"ctime_ns":4,"action":"process"}]`
	fence := func(t *testing.T, tx pgx.Tx, runtime int) error {
		t.Helper()
		var source uuid.UUID
		err := tx.QueryRow(ctx, `SELECT source_id FROM app.fence_ingest_job($1,'runtime-capability-worker',$2,$3)`, live.job, live.lease, runtime).Scan(&source)
		if err == nil && source != live.source {
			t.Fatal("fence returned wrong source")
		}
		return err
	}
	actions := func(tx pgx.Tx, job, lease uuid.UUID, worker string) (string, error) {
		var action string
		err := tx.QueryRow(ctx, `SELECT COALESCE(string_agg(action,','),'') FROM app.read_ingest_candidate_actions($1,$2,$3,$4)`, job, worker, lease, candidates).Scan(&action)
		return action, err
	}
	for _, runtime := range []int{26, capabilityRuntime, SupportedSchemaVersion} {
		t.Run(fmt.Sprintf("fence %d then candidate helper", runtime), func(t *testing.T) {
			tx := begin(t, "workouts_worker", live.account)
			defer tx.Rollback(ctx)
			if err := fence(t, tx, runtime); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if action, err := actions(tx, live.job, live.lease, "runtime-capability-worker"); err != nil || action != "process" {
					t.Fatalf("runtime-%d candidate=%q err=%v", runtime, action, err)
				}
			}
			// This also proves the helper did not replace the runtime in the cap.
			if err := fence(t, tx, runtime); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("already fenced transaction retains authority after clock expiry", func(t *testing.T) {
		tx := begin(t, "workouts_worker", live.account)
		defer tx.Rollback(ctx)
		if err := fence(t, tx, capabilityRuntime); err != nil {
			t.Fatal(err)
		}
		exec(tx, `RESET ROLE`)
		exec(tx, `SET LOCAL session_replication_role='replica'`)
		exec(tx, `UPDATE app.jobs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, live.job)
		exec(tx, `SET LOCAL session_replication_role='origin'`)
		exec(tx, `SET LOCAL ROLE workouts_worker`)
		if action, err := actions(tx, live.job, live.lease, "runtime-capability-worker"); err != nil || action != "process" {
			t.Fatalf("already fenced authority lost on clock expiry: %q %v", action, err)
		}
	})
	for _, boundary := range []string{"backend_pid", "transaction_id"} {
		t.Run("does not borrow another "+boundary+" capability", func(t *testing.T) {
			tx := begin(t, "workouts_worker", live.account)
			defer tx.Rollback(ctx)
			if err := fence(t, tx, capabilityRuntime); err != nil {
				t.Fatal(err)
			}
			// Synthetic administrator corruption models a capability from another
			// backend/transaction. Runtime roles cannot perform this mutation.
			exec(tx, `RESET ROLE`)
			exec(tx, "UPDATE app.ingest_write_capabilities SET "+boundary+"="+boundary+"+1000000")
			exec(tx, `SET LOCAL ROLE workouts_worker`)
			if action, err := actions(tx, live.job, live.lease, "runtime-capability-worker"); err != nil || action != "process" {
				t.Fatalf("legacy standalone fallback failed: %q %v", action, err)
			}
			if err := fence(t, tx, 26); err != nil {
				t.Fatalf("helper borrowed runtime-capability from another capability: %v", err)
			}
		})
	}
	t.Run("standalone stale lease grants no capability", func(t *testing.T) {
		tx := begin(t, "workouts_worker", live.account)
		defer tx.Rollback(ctx)
		if action, err := actions(tx, live.job, uuid.New(), "runtime-capability-worker"); err != nil || action != "" {
			t.Fatalf("standalone stale lease authorized: %q %v", action, err)
		}
		exec(tx, `RESET ROLE`)
		var count int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.ingest_write_capabilities WHERE backend_pid=pg_backend_pid() AND transaction_id=txid_current()`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("stale lease created capability: %d %v", count, err)
		}
	})
	for _, scenario := range []string{"obsolete capability runtime", "stale ownership epoch", "new minimum after fence"} {
		t.Run(scenario, func(t *testing.T) {
			tx := begin(t, "workouts_worker", live.account)
			defer tx.Rollback(ctx)
			if err := fence(t, tx, capabilityRuntime); err != nil {
				t.Fatal(err)
			}
			exec(tx, `RESET ROLE`)
			want := "55000"
			switch scenario {
			case "obsolete capability runtime":
				exec(tx, `UPDATE app.ingest_write_capabilities SET runtime_version=25`)
			case "stale ownership epoch":
				exec(tx, `UPDATE app.ingest_write_capabilities SET ownership_epoch=ownership_epoch+1`)
				want = "55P03"
			case "new minimum after fence":
				exec(tx, `UPDATE app.schema_metadata SET schema_version=$1,minimum_runtime_version=$1 WHERE singleton`, SupportedSchemaVersion)
			}
			exec(tx, `SET LOCAL ROLE workouts_worker`)
			_, err := actions(tx, live.job, live.lease, "runtime-capability-worker")
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != want {
				t.Fatalf("invalid capability bypassed fence: expected %s got %v", want, err)
			}
		})
	}
	t.Run("standalone legacy26 and deferred cleanup", func(t *testing.T) {
		tx := begin(t, "workouts_worker", live.account)
		defer tx.Rollback(ctx)
		if action, err := actions(tx, live.job, live.lease, "runtime-capability-worker"); err != nil || action != "process" {
			t.Fatalf("standalone legacy ABI=%q err=%v", action, err)
		}
		if err := fence(t, tx, 26); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := admin.QueryRow(ctx, `SELECT count(*) FROM app.ingest_write_capabilities`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("committed capability leaked: count=%d err=%v", count, err)
		}
	})
	for _, scenario := range []string{"wrong runtime", "wrong job", "wrong worker", "wrong lease", "wrong account", "runtime below minimum", "standalone cannot spoof new runtime", "capability table private", "expired lease"} {
		t.Run(scenario, func(t *testing.T) {
			tx := begin(t, "workouts_worker", live.account)
			defer tx.Rollback(ctx)
			var err error
			want := "25001"
			switch scenario {
			case "runtime below minimum":
				err, want = fence(t, tx, 25), "55000"
			case "standalone cannot spoof new runtime":
				if _, err := actions(tx, live.job, live.lease, "runtime-capability-worker"); err != nil {
					t.Fatal(err)
				}
				err = fence(t, tx, capabilityRuntime)
			case "capability table private":
				_, err = tx.Exec(ctx, `INSERT INTO app.ingest_write_capabilities(backend_pid,transaction_id) VALUES(pg_backend_pid(),txid_current())`)
				want = "42501"
			case "expired lease":
				exec(tx, `RESET ROLE`)
				exec(tx, `SET LOCAL session_replication_role='replica'`)
				exec(tx, `UPDATE app.jobs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, live.job)
				exec(tx, `SET LOCAL session_replication_role='origin'`)
				exec(tx, `SET LOCAL ROLE workouts_worker`)
				if action, err := actions(tx, live.job, live.lease, "runtime-capability-worker"); err != nil || action != "" {
					t.Fatalf("expired standalone lease authorized: %q %v", action, err)
				}
				return
			default:
				if err := fence(t, tx, capabilityRuntime); err != nil {
					t.Fatal(err)
				}
				job, lease, worker := live.job, live.lease, "runtime-capability-worker"
				switch scenario {
				case "wrong runtime":
					err = fence(t, tx, 26)
				case "wrong job":
					job = uuid.New()
				case "wrong worker":
					worker = "other-worker"
				case "wrong lease":
					lease = uuid.New()
				case "wrong account":
					exec(tx, `SELECT set_config('app.account_id',$1,true)`, legacy.account.String())
				}
				if scenario != "wrong runtime" {
					_, err = actions(tx, job, lease, worker)
				}
			}
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != want {
				t.Fatalf("expected safe SQLSTATE %s, got %v", want, err)
			}
		})
	}
	for _, role := range []string{"workouts_api", "workouts_worker", "workouts_coverage_worker"} {
		tx := begin(t, role, live.account)
		var ready bool
		err := tx.QueryRow(ctx, runtimeReadinessSQL, SupportedSchemaVersion).Scan(&ready)
		if err == nil && ready {
			t.Fatalf("canonical27 %s must not satisfy pipeline readiness", role)
		}
		_ = tx.Rollback(ctx)
	}
	if _, err := provider.UpTo(ctx, SupportedSchemaVersion); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, `SELECT schema_version=$1 AND minimum_runtime_version=28 AND migration_baseline=27 FROM app.schema_metadata WHERE singleton`, SupportedSchemaVersion).Scan(&preserved); err != nil || !preserved {
		t.Fatalf("current schema metadata: %t %v", preserved, err)
	}
	t.Run("pipeline minimum borrows current and future fenced runtimes only", func(t *testing.T) {
		for _, runtime := range []int{SupportedSchemaVersion, SupportedSchemaVersion + 1} {
			tx := begin(t, "workouts_worker", live.account)
			if err := fence(t, tx, runtime); err != nil {
				t.Fatal(err)
			}
			if action, err := actions(tx, live.job, live.lease, "runtime-capability-worker"); err != nil || action != "process" {
				t.Fatalf("runtime%d candidate=%q %v", runtime, action, err)
			}
			_ = tx.Rollback(ctx)
		}
		tx := begin(t, "workouts_worker", live.account)
		defer tx.Rollback(ctx)
		_, err := actions(tx, live.job, live.lease, "runtime-capability-worker")
		var rejected *pgconn.PgError
		if !errors.As(err, &rejected) || rejected.Code != "55000" {
			t.Fatalf("standalone26 bypassed min28: %v", err)
		}
	})
	check := func(t *testing.T, fault string, want bool) {
		t.Helper()
		for _, role := range []string{"workouts_api", "workouts_worker", "workouts_coverage_worker"} {
			func() {
				tx, err := admin.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(ctx)
				if fault != "" {
					if _, err := tx.Exec(ctx, fault); err != nil {
						t.Fatalf("apply readiness fault: %v", err)
					}
				}
				if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+role); err != nil {
					t.Fatal(err)
				}
				var ready bool
				err = tx.QueryRow(ctx, runtimeReadinessSQL, SupportedSchemaVersion).Scan(&ready)
				if (err == nil && ready) != want {
					t.Fatalf("role=%s ready=%t want=%t err=%v", role, ready, want, err)
				}
			}()
		}
	}
	t.Run("all runtime roles ready", func(t *testing.T) { check(t, "", true) })
	t.Run("coverage worker retry ABI accepts both rollout runtimes", func(t *testing.T) {
		for _, runtime := range []int{SupportedSchemaVersion, SupportedSchemaVersion + 1} {
			tx := begin(t, "workouts_coverage_worker", live.account)
			var count int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.retry_next_coverage_update(1,$1)`, runtime).Scan(&count); err != nil || count != 0 {
				t.Fatalf("coverage retry runtime %d: rows=%d err=%v", runtime, count, err)
			}
			var account string
			if err := tx.QueryRow(ctx, `SELECT current_setting('app.account_id',true)`).Scan(&account); err != nil || account != live.account.String() {
				t.Fatalf("retry helper changed account context: %q %v", account, err)
			}
			_ = tx.Rollback(ctx)
		}
	})
	t.Run("compatible newer schema", func(t *testing.T) {
		check(t, fmt.Sprintf(`UPDATE app.schema_metadata SET schema_version=%d,minimum_runtime_version=28 WHERE singleton`, SupportedSchemaVersion+1), true)
	})
	faults := map[string]string{
		"old schema":               `UPDATE app.schema_metadata SET schema_version=25,minimum_runtime_version=25 WHERE singleton`,
		"missing baseline marker":  `ALTER TABLE app.schema_metadata DROP COLUMN migration_baseline`,
		"wrong baseline marker":    `UPDATE app.schema_metadata SET migration_baseline=26 WHERE singleton`,
		"null baseline marker":     `ALTER TABLE app.schema_metadata ALTER COLUMN migration_baseline DROP NOT NULL; UPDATE app.schema_metadata SET migration_baseline=NULL WHERE singleton`,
		"new minimum":              fmt.Sprintf(`UPDATE app.schema_metadata SET schema_version=%d,minimum_runtime_version=%d WHERE singleton`, SupportedSchemaVersion+1, SupportedSchemaVersion+1),
		"goose inventory":          fmt.Sprintf(`DELETE FROM public.goose_db_version WHERE version_id=%d`, SupportedSchemaVersion),
		"old candidate helper":     original,
		"has_route default":        `ALTER TABLE app.workout_import_events ALTER COLUMN has_route SET DEFAULT false`,
		"retry enrollment default": `ALTER TABLE app.coverage_retry_roots ALTER COLUMN enrolled_at SET DEFAULT clock_timestamp()`,
		"retry ordinal default":    `ALTER TABLE app.coverage_retry_attempts ALTER COLUMN automatic_ordinal SET DEFAULT 1`,
		"enrollment disabled":      `ALTER TABLE app.jobs DISABLE TRIGGER coverage_retry_failure_enrollment`,
		"enrollment no condition":  `DROP TRIGGER coverage_retry_failure_enrollment ON app.jobs; CREATE TRIGGER coverage_retry_failure_enrollment AFTER UPDATE OF status ON app.jobs FOR EACH ROW EXECUTE FUNCTION app.enroll_coverage_retry_failure()`,
	}
	for column, bound := range map[string]int{"original_point_count": 50000, "sampled_point_count": 12000} {
		constraint := "coverage_diagnostic_runs_" + column + "_check"
		drop := "ALTER TABLE app.coverage_diagnostic_runs DROP CONSTRAINT " + constraint
		faults[column+" missing check"] = drop
		faults[column+" loose check"] = drop + fmt.Sprintf("; ALTER TABLE app.coverage_diagnostic_runs ADD CONSTRAINT %s CHECK (%s BETWEEN 0 AND %d)", constraint, column, bound+1)
		faults[column+" old check"] = drop + fmt.Sprintf("; ALTER TABLE app.coverage_diagnostic_runs ADD CONSTRAINT %s CHECK (%s BETWEEN 0 AND %d)", constraint, column, map[string]int{"original_point_count": 25000, "sampled_point_count": 10000}[column])
		faults[column+" unvalidated check"] = drop + fmt.Sprintf("; ALTER TABLE app.coverage_diagnostic_runs ADD CONSTRAINT %s CHECK (%s BETWEEN 0 AND %d) NOT VALID", constraint, column, bound)
		faults[column+" nullable"] = "ALTER TABLE app.coverage_diagnostic_runs ALTER COLUMN " + column + " DROP NOT NULL"
		faults[column+" retained old bound"] = fmt.Sprintf("ALTER TABLE app.coverage_diagnostic_runs ADD CONSTRAINT diagnostic_stale_bound CHECK (%s <= 10000)", column)
	}
	for _, table := range []string{"coverage_diagnostic_runs", "coverage_diagnostic_generations", "coverage_diagnostic_evidence", "coverage_diagnostic_overall_labels", "coverage_diagnostic_segment_labels"} {
		relation := "app." + table
		faults[table+" RLS"] = "ALTER TABLE " + relation + " DISABLE ROW LEVEL SECURITY"
		faults[table+" force RLS"] = "ALTER TABLE " + relation + " NO FORCE ROW LEVEL SECURITY"
		faults[table+" owner"] = "ALTER TABLE " + relation + " OWNER TO workouts_migration"
		faults[table+" account policy"] = "ALTER POLICY " + table + "_account_policy ON " + relation + " USING (true)"
		faults[table+" owner policy"] = "DROP POLICY " + table + "_owner_policy ON " + relation
		faults[table+" API read"] = "REVOKE SELECT ON " + relation + " FROM workouts_api"
		for _, role := range []string{"PUBLIC", "workouts_api", "workouts_worker", "workouts_coverage_worker", "workouts_tiles"} {
			faults[table+role+" write"] = "GRANT INSERT ON " + relation + " TO " + role
			faults[table+role+" column write"] = "GRANT UPDATE(account_id) ON " + relation + " TO " + role
			if role != "workouts_api" {
				faults[table+role+" read"] = "GRANT SELECT ON " + relation + " TO " + role
			}
		}
	}
	for _, signature := range []string{
		"app.persist_coverage_diagnostic(uuid,uuid,uuid,bigint,bytea,text,text,text,text,double precision,text,integer,integer,integer,integer,integer,integer,integer,integer,integer,integer,jsonb,jsonb)",
		"app.set_coverage_diagnostic_labels(uuid,uuid,boolean,text,jsonb)",
	} {
		faults[signature+" owner"] = "ALTER FUNCTION " + signature + " OWNER TO workouts_migration"
		faults[signature+" invoker"] = "ALTER FUNCTION " + signature + " SECURITY INVOKER"
		faults[signature+" API revoke"] = "REVOKE EXECUTE ON FUNCTION " + signature + " FROM workouts_api"
		for _, role := range []string{"PUBLIC", "workouts_worker", "workouts_coverage_worker", "workouts_tiles"} {
			faults[signature+role+" exposed"] = "GRANT EXECUTE ON FUNCTION " + signature + " TO " + role
		}
	}
	for _, function := range runtimeCapabilityFunctions {
		s := function.signature
		faults[s+" missing"] = "ALTER FUNCTION " + s + " RENAME TO runtime_capability_missing"
		faults[s+" owner"] = "ALTER FUNCTION " + s + " OWNER TO workouts_migration"
		faults[s+" invoker"] = "ALTER FUNCTION " + s + " SECURITY INVOKER"
		faults[s+" path"] = "ALTER FUNCTION " + s + " SET search_path=pg_catalog,app,public"
		volatility := "STABLE"
		if function.volatility == "s" {
			volatility = "VOLATILE"
		}
		faults[s+" volatility"] = "ALTER FUNCTION " + s + " " + volatility
		for _, role := range []string{"PUBLIC", "workouts_api", "workouts_worker", "workouts_coverage_worker", "workouts_tiles"} {
			allowed := role == "workouts_api" && function.api || role == "workouts_worker" && function.worker || role == "workouts_coverage_worker" && function.coverage
			if allowed {
				faults[s+role+" revoked"] = "REVOKE EXECUTE ON FUNCTION " + s + " FROM " + role
				faults[s+role+" grantable"] = "GRANT EXECUTE ON FUNCTION " + s + " TO " + role + " WITH GRANT OPTION"
			} else {
				faults[s+role+" exposed"] = "GRANT EXECUTE ON FUNCTION " + s + " TO " + role
			}
		}
	}
	for relation, columns := range map[string][]string{
		"app.workout_import_events":   {"has_route"},
		"app.job_progress":            {"workouts_created_with_route", "workouts_updated_with_route"},
		"app.coverage_job_contexts":   {"matcher_contract_version"},
		"app.coverage_retry_roots":    {"root_job_id", "account_id", "enrolled_at", "automatic_attempts_used"},
		"app.coverage_retry_attempts": {"job_id", "root_job_id", "predecessor_job_id", "origin", "automatic_ordinal"},
	} {
		for _, column := range columns {
			faults[relation+column+" missing"] = "ALTER TABLE " + relation + " RENAME COLUMN " + column + " TO runtime_capability_missing"
		}
	}
	for _, table := range []string{"coverage_retry_roots", "coverage_retry_attempts"} {
		relation := "app." + table
		faults[table+" owner"] = "ALTER TABLE " + relation + " OWNER TO workouts_migration"
		faults[table+" RLS"] = "ALTER TABLE " + relation + " DISABLE ROW LEVEL SECURITY"
		faults[table+" force RLS"] = "ALTER TABLE " + relation + " NO FORCE ROW LEVEL SECURITY"
		faults[table+" policy"] = "DROP POLICY " + table + "_owner_policy ON " + relation
		for _, role := range []string{"PUBLIC", "workouts_api", "workouts_worker", "workouts_coverage_worker", "workouts_tiles"} {
			faults[table+role+" direct read"] = "GRANT SELECT ON " + relation + " TO " + role
			faults[table+role+" column read"] = "GRANT SELECT(root_job_id) ON " + relation + " TO " + role
			faults[table+role+" direct write"] = "GRANT INSERT ON " + relation + " TO " + role
		}
	}
	for _, table := range []string{"app.workout_import_events", "app.job_progress", "app.coverage_job_contexts", "app.ingest_write_capabilities"} {
		faults[table+" RLS"] = "ALTER TABLE " + table + " DISABLE ROW LEVEL SECURITY"
		faults[table+" force RLS"] = "ALTER TABLE " + table + " NO FORCE ROW LEVEL SECURITY"
	}
	// Enumerate the actual constraints so every origin, ordinal, budget, matcher,
	// route-counter and lineage constraint is independently proved necessary.
	rows, err := admin.Query(ctx, `SELECT conrelid::regclass::text,conname FROM pg_constraint
		WHERE (conrelid IN ('app.coverage_retry_roots'::regclass,'app.coverage_retry_attempts'::regclass) AND contype IN ('c','f','u','p'))
		OR (conrelid IN ('app.job_progress'::regclass,'app.coverage_job_contexts'::regclass) AND contype='c'
		AND (pg_get_constraintdef(oid) LIKE '%with_route%' OR pg_get_constraintdef(oid) LIKE '%matcher_contract_version%'))`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var table, constraint string
		if err := rows.Scan(&table, &constraint); err != nil {
			t.Fatal(err)
		}
		faults[constraint+" dropped"] = "ALTER TABLE " + table + " DROP CONSTRAINT " + constraint + " CASCADE"
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	for _, index := range []string{"coverage_retry_roots_budget_idx", "coverage_retry_attempts_predecessor_idx"} {
		faults[index+" dropped"] = "DROP INDEX app." + index
	}
	for table, column := range map[string][]string{
		"app.job_progress":          {"workouts_created_with_route", "workouts_updated_with_route"},
		"app.coverage_job_contexts": {"matcher_contract_version"},
		"app.coverage_retry_roots":  {"automatic_attempts_used"},
	} {
		for _, c := range column {
			faults[table+c+" default"] = "ALTER TABLE " + table + " ALTER COLUMN " + c + " DROP DEFAULT"
		}
	}
	for _, column := range []string{"workouts_created_with_route", "workouts_updated_with_route"} {
		faults[column+" not nullable"] = "ALTER TABLE app.job_progress ALTER COLUMN " + column + " SET NOT NULL"
		// Remove the synthetic unknown rows for the NOT NULL fault only.
		faults[column+" not nullable"] = "UPDATE app.job_progress SET " + column + "=0 WHERE " + column + " IS NULL; " + faults[column+" not nullable"]
		faults[column+" API grant"] = "REVOKE SELECT ON app.job_progress FROM workouts_api; REVOKE SELECT(" + column + ") ON app.job_progress FROM workouts_api"
		faults[column+" worker grant"] = "REVOKE SELECT(" + column + ") ON app.job_progress FROM workouts_worker"
		faults[column+" worker update"] = "GRANT UPDATE(" + column + ") ON app.job_progress TO workouts_worker"
	}
	faults["has_route worker grant"] = "REVOKE SELECT ON app.workout_import_events FROM workouts_worker; REVOKE SELECT(has_route) ON app.workout_import_events FROM workouts_worker"
	faults["has_route worker update"] = "GRANT UPDATE(has_route) ON app.workout_import_events TO workouts_worker"
	for name, fault := range faults {
		t.Run("not ready/"+name, func(t *testing.T) { check(t, fault, false) })
	}
}
