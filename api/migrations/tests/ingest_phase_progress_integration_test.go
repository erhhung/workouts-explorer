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
)

const ingestPhaseCall = `SELECT app.record_ingest_phase($1,$2,$3,$4,$5,$6)`

// Retained tooling supplies an already migrated synthetic scratch database.
// No provisioning, application configuration, or actual-data clones are used.
func TestIngestPhaseProgressPostgreSQL(t *testing.T) {
	if os.Getenv("INGEST_PHASE_PROGRESS_TEST_DISPOSABLE") != "1" {
		t.Skip("INGEST_PHASE_PROGRESS_TEST_DISPOSABLE=1 and five disposable role URLs required")
	}
	var endpoint string
	for _, role := range [][2]string{{"API_DATABASE_URL", "workouts_api"}, {"WORKER_DATABASE_URL", "workouts_worker"},
		{"MIGRATION_DATABASE_URL", "workouts_migration"}, {"COVERAGE_WORKER_DATABASE_URL", "workouts_coverage_worker"}, {"TILE_DATABASE_URL", "workouts_tiles"}} {
		value := os.Getenv(role[0])
		cfg, err := pgx.ParseConfig(value)
		if value == "" || err != nil {
			t.Fatal(role[0] + " must explicitly name a disposable database")
		}
		if (cfg.Host != "127.0.0.1" && cfg.Host != "::1") || cfg.User != role[1] ||
			!strings.HasPrefix(cfg.Database, "workouts_") || !strings.Contains(cfg.Database, "_test_") ||
			strings.Contains(cfg.Database, "clone") || strings.Contains(cfg.Database, "actual") || strings.Contains(cfg.Database, "live") || cfg.RuntimeParams["role"] != "" {
			t.Fatal("phase fixtures require genuine role logins on a named synthetic loopback workouts_*_test_* database")
		}
		for _, fallback := range cfg.Fallbacks {
			if fallback.Host != cfg.Host || fallback.Port != cfg.Port {
				t.Fatal("phase fixtures prohibit alternate endpoints")
			}
		}
		target := fmt.Sprintf("%s:%d/%s", cfg.Host, cfg.Port, cfg.Database)
		if endpoint != "" && endpoint != target {
			t.Fatal("all role URLs must target the same disposable endpoint")
		}
		endpoint = target
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	db := testDatabases{ctx: ctx, api: openPool(t, ctx, os.Getenv("API_DATABASE_URL"), 1),
		worker: openPool(t, ctx, os.Getenv("WORKER_DATABASE_URL"), 1), migration: openPool(t, ctx, os.Getenv("MIGRATION_DATABASE_URL"), 2),
		coverage: openPool(t, ctx, os.Getenv("COVERAGE_WORKER_DATABASE_URL"), 1), tiles: openPool(t, ctx, os.Getenv("TILE_DATABASE_URL"), 1)}
	for _, role := range []struct {
		pool *pgxpool.Pool
		name string
	}{{db.api, "workouts_api"}, {db.worker, "workouts_worker"}, {db.migration, "workouts_migration"},
		{db.coverage, "workouts_coverage_worker"}, {db.tiles, "workouts_tiles"}} {
		assertRuntimeRole(t, ctx, role.pool, role.name)
	}
	check := func(t *testing.T, f reprocessingFixture, query string, args ...any) {
		t.Helper()
		tx := beginAccount(t, ctx, db.migration, f.account)
		defer tx.Rollback(ctx)
		var ok bool
		if err := tx.QueryRow(ctx, query, args...).Scan(&ok); err != nil || !ok {
			t.Fatalf("phase assertion failed: ok=%t err=%v", ok, err)
		}
	}
	record := func(t *testing.T, f reprocessingFixture, phase string, current, total any) {
		t.Helper()
		if !callBool(t, ctx, db.worker, f.account, ingestPhaseCall, f.job, reprocessingWorker, f.lease, phase, current, total) {
			t.Fatal("live phase snapshot rejected")
		}
	}

	t.Run("ExactMessagesAndUnchangedSourceParentChildProgress", func(t *testing.T) {
		f := reprocessingJob(t, db, nil, uuid.Nil, uuid.Nil, "false")
		snapshot := func() string {
			tx := beginAccount(t, ctx, db.migration, f.account)
			defer tx.Rollback(ctx)
			var value string
			if err := tx.QueryRow(ctx, `SELECT jsonb_build_array(to_jsonb(source),
				(SELECT jsonb_agg(to_jsonb(job) ORDER BY id) FROM app.jobs job WHERE account_id=$1),
				(SELECT jsonb_agg(to_jsonb(progress) ORDER BY job_id) FROM app.job_progress progress WHERE account_id=$1))::text
				FROM app.sources source WHERE id=$2`, f.account, f.source).Scan(&value); err != nil {
				t.Fatal(err)
			}
			return value
		}
		before := snapshot()
		for _, phase := range ingestPhaseMessages {
			record(t, f, phase[0], nil, nil)
			check(t, f, `SELECT count(*)=1 FROM app.job_events WHERE job_id=$1 AND code=$2 AND safe_message=$3
				AND severity='info' AND fields=jsonb_build_object('attempt',1) AND app.valid_job_safe_fields(fields)`,
				f.job, "ingest-phase-"+strings.ReplaceAll(phase[0], "_", "-"), phase[1])
		}
		if after := snapshot(); after != before {
			t.Fatal("phase journal changed source, jobs or aggregate progress")
		}
		record(t, f, "cleanup", nil, nil)
		check(t, f, `SELECT count(*)=8 FROM app.job_events WHERE job_id=$1 AND code LIKE 'ingest-phase-%'`, f.job)
	})

	t.Run("InvalidArgumentsAndRegression", func(t *testing.T) {
		f := reprocessingJob(t, db, nil, uuid.Nil, uuid.Nil, "false")
		record(t, f, "importing", 3, 5)
		for _, args := range [][]any{
			{f.job, reprocessingWorker, f.lease, "queued", nil, nil}, {f.job, reprocessingWorker, f.lease, "succeeded", nil, nil},
			{f.job, reprocessingWorker, f.lease, "analyzing-imported-routes", nil, nil}, {f.job, reprocessingWorker, f.lease, nil, nil, nil},
			{f.job, reprocessingWorker, f.lease, "importing", -1, 5}, {f.job, reprocessingWorker, f.lease, "importing", nil, -1},
			{f.job, reprocessingWorker, f.lease, "importing", 6, 5}, {f.job, reprocessingWorker, f.lease, "importing", 2, 5},
			{f.job, reprocessingWorker, f.lease, "importing", nil, 5}, {nil, reprocessingWorker, f.lease, "preparing", nil, nil},
			{f.job, nil, f.lease, "preparing", nil, nil}, {f.job, "", f.lease, "preparing", nil, nil},
			{f.job, reprocessingWorker, nil, "preparing", nil, nil},
		} {
			tx := beginAccount(t, ctx, db.worker, f.account)
			_, err := tx.Exec(ctx, ingestPhaseCall, args...)
			_ = tx.Rollback(ctx)
			reprocessingError(t, err, "22023")
		}
		record(t, f, "discovering", nil, 10)
		record(t, f, "importing", 0, nil)
		record(t, f, "importing", int64(9223372036854775807), int64(9223372036854775807))
	})

	t.Run("RequiredProgressSurvivesDiagnosticCapAndUsesNewestID", func(t *testing.T) {
		f := reprocessingJob(t, db, nil, uuid.Nil, uuid.Nil, "false")
		tx := beginAccount(t, ctx, db.worker, f.account)
		defer tx.Rollback(ctx)
		for i := range 1001 {
			var ok bool
			if err := tx.QueryRow(ctx, `SELECT app.record_job_event($1,$2,$3,'ingest-progress','{}')`, f.job, reprocessingWorker, f.lease).Scan(&ok); err != nil || ok != (i < 1000) {
				t.Fatalf("optional cap changed at %d: ok=%t err=%v", i, ok, err)
			}
		}
		for i := range 1002 {
			var ok bool
			if err := tx.QueryRow(ctx, ingestPhaseCall, f.job, reprocessingWorker, f.lease, "importing", i, 1001).Scan(&ok); err != nil || !ok {
				t.Fatalf("required progress lost after diagnostic cap at %d: %v", i, err)
			}
		}
		// All timestamps tie in this transaction. Reentry must use the latest ID,
		// not a historical same-phase row whose counter is already 1001.
		for _, phase := range []string{"analyzing_imported_routes", "importing", "importing"} {
			var ok bool
			if err := tx.QueryRow(ctx, ingestPhaseCall, f.job, reprocessingWorker, f.lease, phase, 0, 1).Scan(&ok); err != nil || !ok {
				t.Fatalf("phase reentry/idempotency failed: %v", err)
			}
		}
		reprocessingCommit(t, db, tx)
		check(t, f, `SELECT count(*)=1004 AND count(DISTINCT created_at)=1
			FROM app.job_events WHERE job_id=$1 AND code LIKE 'ingest-phase-%'`, f.job)
		check(t, f, `SELECT code='ingest-phase-importing' AND fields='{"current":0,"total":1,"attempt":1}'::jsonb
			FROM app.job_events WHERE job_id=$1 AND code LIKE 'ingest-phase-%' ORDER BY id DESC LIMIT 1`, f.job)
	})

	t.Run("FencesAndRoleBoundaries", func(t *testing.T) {
		f := reprocessingJob(t, db, nil, uuid.Nil, uuid.Nil, "false")
		for _, tc := range []struct {
			account, job, lease uuid.UUID
			worker              string
		}{
			{uuid.New(), f.job, f.lease, reprocessingWorker}, {f.account, f.job, uuid.New(), reprocessingWorker},
			{f.account, f.job, f.lease, "foreign-worker"}, {f.account, f.parent, f.lease, reprocessingWorker},
			{f.account, uuid.New(), f.lease, reprocessingWorker},
		} {
			if callBool(t, ctx, db.worker, tc.account, ingestPhaseCall, tc.job, tc.worker, tc.lease, "preparing", nil, nil) {
				t.Fatal("invalid account/job/worker/lease accepted")
			}
		}
		for _, mutation := range []struct {
			transition, sql string
			job             uuid.UUID
		}{
			{"cancel", `UPDATE app.jobs SET cancel_requested_at=clock_timestamp(),cancel_requested_by=gen_random_uuid() WHERE id=$1`, f.job},
			{"cancel", `UPDATE app.jobs SET cancel_requested_at=clock_timestamp(),cancel_requested_by=gen_random_uuid() WHERE id=$1`, f.parent},
			{"heartbeat", `UPDATE app.jobs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1`, f.job},
		} {
			tx := beginAccount(t, ctx, db.migration, f.account)
			// Synthetic cancellation/expiry is confined to this rollback-only transaction.
			reprocessingExec(t, db, tx, `SELECT set_config('app.job_transition',$1,true)`, mutation.transition)
			reprocessingExec(t, db, tx, mutation.sql, mutation.job)
			reprocessingExec(t, db, tx, `SET LOCAL ROLE workouts_security_owner`)
			var ok bool
			err := tx.QueryRow(ctx, ingestPhaseCall, f.job, reprocessingWorker, f.lease, "preparing", nil, nil).Scan(&ok)
			_ = tx.Rollback(ctx)
			if err != nil || ok {
				t.Fatalf("cancelled/expired phase accepted: ok=%t err=%v", ok, err)
			}
		}
		for _, pool := range []*pgxpool.Pool{db.api, db.coverage, db.tiles} {
			tx := beginAccount(t, ctx, pool, f.account)
			_, err := tx.Exec(ctx, ingestPhaseCall, f.job, reprocessingWorker, f.lease, "preparing", nil, nil)
			_ = tx.Rollback(ctx)
			reprocessingError(t, err, "42501")
		}
		for _, pool := range []*pgxpool.Pool{db.worker, db.api, db.coverage, db.tiles} {
			for _, sql := range []string{`INSERT INTO app.job_events(account_id,job_id,severity,code,safe_message) VALUES($1,$2,'info','ingest-phase-preparing','unsafe')`,
				`UPDATE app.job_events SET fields='{}' WHERE account_id=$1 AND job_id=$2`, `DELETE FROM app.job_events WHERE account_id=$1 AND job_id=$2`} {
				tx := beginAccount(t, ctx, pool, f.account)
				_, err := tx.Exec(ctx, sql, f.account, f.job)
				_ = tx.Rollback(ctx)
				reprocessingError(t, err, "42501")
			}
		}
		check(t, f, `SELECT count(*)=0 FROM app.job_events WHERE job_id=$1 AND code LIKE 'ingest-phase-%'`, f.job)
		tx := beginAccount(t, ctx, db.worker, f.account)
		defer tx.Rollback(ctx)
		var ok bool
		if err := tx.QueryRow(ctx, ingestPhaseCall, f.job, reprocessingWorker, f.lease, "preparing", nil, nil).Scan(&ok); err != nil || !ok {
			t.Fatalf("initial same-transaction phase rejected: %v", err)
		}
		if err := tx.QueryRow(ctx, ingestPhaseCall, f.job, reprocessingWorker, uuid.New(), "preparing", nil, nil).Scan(&ok); err != nil || ok {
			t.Fatalf("stale token must return false even after this transaction fenced: ok=%t err=%v", ok, err)
		}
	})

	t.Run("AttemptResetAndSameSnapshotInNewAttempt", func(t *testing.T) {
		f := reprocessingJob(t, db, nil, uuid.Nil, uuid.Nil, "false")
		record(t, f, "importing", 3, 5)
		tx := beginAccount(t, ctx, db.migration, f.account)
		defer tx.Rollback(ctx)
		// Emulate two legitimate claim increments without waiting on wall-clock
		// lease recovery; the state guard is suspended only in this rolled-back fixture.
		reprocessingExec(t, db, tx, `ALTER TABLE app.jobs DISABLE TRIGGER jobs_state_before_write`)
		for _, attempt := range []int{2, 3} {
			reprocessingExec(t, db, tx, `UPDATE app.jobs SET attempt=$2 WHERE id=$1`, f.job, attempt)
			reprocessingExec(t, db, tx, `SET LOCAL ROLE workouts_security_owner`)
			for range 2 {
				var ok bool
				if err := tx.QueryRow(ctx, ingestPhaseCall, f.job, reprocessingWorker, f.lease, "importing", 0, 5).Scan(&ok); err != nil || !ok {
					t.Fatalf("new attempt reset/dedup rejected: %v", err)
				}
			}
			reprocessingExec(t, db, tx, `RESET ROLE`)
		}
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT count(*)=3 AND count(DISTINCT fields->>'attempt')=3
			FROM app.job_events WHERE job_id=$1 AND code LIKE 'ingest-phase-%'`, f.job).Scan(&ok); err != nil || !ok {
			t.Fatalf("attempt-scoped idempotency failed: %v", err)
		}
	})

	t.Run("StatusCheckpointAndRuntimeFence", func(t *testing.T) {
		f := reprocessingJob(t, db, nil, uuid.Nil, uuid.Nil, "false")
		future := database.SupportedSchemaVersion + 1
		for _, metadata := range []struct {
			schema, minimum int
			allowed         bool
		}{{28, 28, false}, {29, 29, true}, {30, 30, true}, {31, 31, true}, {future, 31, true}, {future, future, false}} {
			tx := beginAccount(t, ctx, db.migration, f.account)
			defer tx.Rollback(ctx)
			reprocessingExec(t, db, tx, `UPDATE app.schema_metadata SET schema_version=$1,minimum_runtime_version=$2 WHERE singleton`, metadata.schema, metadata.minimum)
			reprocessingExec(t, db, tx, `SET LOCAL ROLE workouts_security_owner`)
			var ok bool
			err := tx.QueryRow(ctx, ingestPhaseCall, f.job, reprocessingWorker, f.lease, "preparing", nil, nil).Scan(&ok)
			if metadata.allowed {
				if err != nil || !ok {
					t.Fatalf("phase writer rejected schema/minimum %d/%d: ok=%t err=%v", metadata.schema, metadata.minimum, ok, err)
				}
				var runtime int
				if err := tx.QueryRow(ctx, `SELECT runtime_version FROM app.ingest_write_capabilities WHERE backend_pid=pg_backend_pid() AND transaction_id=txid_current()`).Scan(&runtime); err != nil || runtime != 31 {
					t.Fatalf("phase writer must use its literal runtime31 fence: runtime=%d err=%v", runtime, err)
				}
			} else {
				reprocessingError(t, err, "55000")
			}
			_ = tx.Rollback(ctx)
		}
		check(t, f, `SELECT NOT EXISTS(SELECT 1 FROM app.job_events WHERE job_id=$1 AND code LIKE 'ingest-phase-%')`, f.job)
	})

	t.Run("ForwardOnlyDownRetainsLiveSnapshot", func(t *testing.T) {
		f := reprocessingJob(t, db, nil, uuid.Nil, uuid.Nil, "false")
		record(t, f, "preparing", nil, nil)
		raw, err := migrations.Files.ReadFile("029_ingest_status_reporting.sql")
		if err != nil {
			t.Fatal(err)
		}
		parts := strings.Split(string(raw), "-- +goose Down")
		tx := beginAccount(t, ctx, db.migration, f.account)
		defer tx.Rollback(ctx)
		oldObjects := `SELECT jsonb_build_array(
			(SELECT jsonb_agg(jsonb_build_array(oid,pg_get_functiondef(oid),proacl::text) ORDER BY oid)
			 FROM pg_proc WHERE pronamespace='app'::regnamespace),
			(SELECT to_jsonb(metadata) FROM app.schema_metadata metadata WHERE singleton),
			(SELECT jsonb_agg(to_jsonb(event) ORDER BY id) FROM app.job_events event WHERE job_id=$1),
			(SELECT pg_get_indexdef('app.job_events_ingest_phase_latest_idx'::regclass)))::text`
		var before, after string
		if err := tx.QueryRow(ctx, oldObjects, f.job).Scan(&before); err != nil {
			t.Fatal(err)
		}
		reprocessingExec(t, db, tx, `SAVEPOINT forward_only`)
		_, err = tx.Exec(ctx, parts[1])
		reprocessingError(t, err, "55000")
		reprocessingExec(t, db, tx, `ROLLBACK TO SAVEPOINT forward_only`)
		if err := tx.QueryRow(ctx, oldObjects, f.job).Scan(&after); err != nil || after != before {
			t.Fatalf("rejected status29 Down changed live functions, metadata, index or journal: %v", err)
		}
	})
	for _, pool := range []*pgxpool.Pool{db.api, db.worker, db.coverage} {
		if !database.Ready(ctx, pool) {
			t.Fatal("runtime30 must be healthy for all three runtime roles")
		}
	}
}
