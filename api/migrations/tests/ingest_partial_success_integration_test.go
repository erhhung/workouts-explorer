package migrations_test

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestIngestPartialSuccessMigrationContract(t *testing.T) {
	read := func(name string) string {
		t.Helper()
		body, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	source := read("029_ingest_status_reporting.sql")
	status, _, found := strings.Cut(source, "-- BEGIN PRECISE NEAR-IDLE COVERAGE RECONCILIATION")
	if !found {
		t.Fatal("missing metadata reconciliation feature boundary")
	}
	for _, required := range []string{
		"CREATE OR REPLACE FUNCTION app.derive_parent_status (parent_id uuid)",
		"CREATE OR REPLACE FUNCTION app.notify_terminal_ingest_parent ()",
		"COALESCE(sum(progress.workouts_created), 0) > 0 OR COALESCE(sum(progress.workouts_updated), 0) > 0",
		"WHEN failed_count + cancelled_count > 0 AND ingest_has_workouts THEN 'partially_succeeded'",
		"child.failure_code IS NOT DISTINCT FROM 'source-file-invalid' AND EXISTS",
		"file.account_id = child.account_id AND file.state = 'failed'",
		"child.status IN ('failed', 'cancelled')",
		"ON CONFLICT(account_id, type, condition_key) DO NOTHING",
		"UPDATE app.schema_metadata SET schema_version = 29, minimum_runtime_version = 29 WHERE singleton;",
		"FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;",
		"GRANT EXECUTE ON FUNCTION app.derive_parent_status(uuid) TO workouts_security_owner;",
		"CREATE FUNCTION app.read_owned_job_files (target_job_id uuid, requested_limit integer, requested_offset integer, failures_only boolean)",
		"CREATE OR REPLACE FUNCTION app.count_owned_job_rows (target_job_id uuid, row_kind text)",
		"ALTER FUNCTION app.read_owned_job_files(uuid, integer, integer, boolean) OWNER TO workouts_security_owner;",
		"REVOKE ALL ON FUNCTION app.read_owned_job_files(uuid, integer, integer, boolean)",
		"GRANT EXECUTE ON FUNCTION app.read_owned_job_files(uuid, integer, integer, boolean) TO workouts_api;",
		"IF row_kind IN ('file', 'failed_file') THEN",
		"AND (row_kind = 'file' OR file.state = 'failed');",
		"ingest status reporting is forward-only",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("missing migration contract: %s", required)
		}
	}
	for _, forbidden := range []string{
		"DISABLE TRIGGER", "session_replication_role", "NO FORCE ROW LEVEL SECURITY", "DROP FUNCTION",
		"migration_baseline =", "SELECT app.derive_parent_status",
		"UPDATE app.source_files", "UPDATE app.notifications", "boolean DEFAULT",
		"ALTER FUNCTION app.derive_parent_status", "ALTER FUNCTION app.notify_terminal_ingest_parent",
		"ALTER FUNCTION app.count_owned_job_rows",
		"CREATE OR REPLACE FUNCTION app.read_owned_job_files",
		"GRANT ALL",
	} {
		if strings.Contains(status, forbidden) {
			t.Errorf("migration changes existing history or security contract: %s", forbidden)
		}
	}
	if strings.Count(source, "UPDATE app.jobs") != 1 || strings.Count(source, "UPDATE app.schema_metadata") != 1 {
		t.Fatal("expected only the guarded parent update and one atomic status29 metadata publication")
	}
	// The overload preserves the deployed reader's scope, bounds and redaction;
	// its only query change filters before the window count and LIMIT/OFFSET.
	_, oldReader, _ := strings.Cut(read("006_durable_data_sync.sql"), "    IF requested_limit NOT BETWEEN 1 AND 100 OR requested_offset < 0 THEN\n")
	oldReader, _, _ = strings.Cut(oldReader, "$function$;")
	_, newReader, _ := strings.Cut(source, "    IF requested_limit NOT BETWEEN 1 AND 100 OR requested_offset < 0 THEN\n")
	newReader, _, _ = strings.Cut(newReader, "$function$;")
	filter := "       AND (failures_only IS NOT TRUE OR file.state = 'failed')\n"
	if oldReader == "" || !strings.Contains(newReader, filter) || strings.Replace(newReader, filter, "", 1) != oldReader {
		t.Fatal("file reader changes more than the pre-pagination failure predicate")
	}
	// Coverage's counters and original child-only terminal contributions stay exact.
	old := read("015_coverage_route_jobs.sql")
	_, oldLogs, _ := strings.Cut(old, "    ELSIF row_kind = 'log' THEN\n")
	oldLogs, _, _ = strings.Cut(oldLogs, "$function$;")
	_, newLogs, _ := strings.Cut(source, "    ELSIF row_kind = 'log' THEN\n")
	newLogs, _, _ = strings.Cut(newLogs, "$function$;")
	if oldLogs == "" || oldLogs != newLogs {
		t.Fatal("coverage log count behavior changed")
	}
	_, oldCoverage, _ := strings.Cut(old, "    IF parent_kind = 'coverage_update' THEN\n")
	oldCoverage, _, _ = strings.Cut(oldCoverage, "    END IF;")
	_, newCoverage, _ := strings.Cut(source, "    IF parent_kind = 'coverage_update' THEN\n")
	newCoverage, _, _ = strings.Cut(newCoverage, "    ELSIF failed_count")
	if oldCoverage == "" || oldCoverage != newCoverage {
		t.Fatal("Coverage rollup changed")
	}
	_, oldTail, _ := strings.Cut(old, "    PERFORM set_config('app.job_transition', 'derive_parent', true);\n")
	oldTail, _, _ = strings.Cut(oldTail, "$function$;")
	_, newTail, _ := strings.Cut(source, "    PERFORM set_config('app.job_transition', 'derive_parent', true);\n")
	newTail, _, _ = strings.Cut(newTail, "$function$;")
	if oldTail == "" || oldTail != newTail {
		t.Fatal("parent timestamps, counters or status-change gate changed")
	}
}

// Only explicitly opted-in, synthetic loopback test databases are accepted.
// The caller applies migrations; these tests never read application config.
func TestIngestPartialSuccessPostgreSQL(t *testing.T) {
	if os.Getenv("INGEST_PARTIAL_SUCCESS_TEST_DISPOSABLE") != "1" {
		t.Skip("INGEST_PARTIAL_SUCCESS_TEST_DISPOSABLE=1 and disposable role database URLs are required")
	}
	var databaseName string
	var port uint16
	for _, name := range []string{"API_DATABASE_URL", "WORKER_DATABASE_URL", "MIGRATION_DATABASE_URL", "COVERAGE_WORKER_DATABASE_URL", "TILE_DATABASE_URL"} {
		value := os.Getenv(name)
		if value == "" && (name == "COVERAGE_WORKER_DATABASE_URL" || name == "TILE_DATABASE_URL") {
			continue
		}
		config, err := pgx.ParseConfig(value)
		if value == "" || err != nil || !strings.HasPrefix(config.Database, "workouts_") || !strings.Contains(config.Database, "_test_") ||
			(config.Host != "127.0.0.1" && config.Host != "localhost" && config.Host != "::1") {
			t.Fatalf("%s must explicitly name a disposable loopback workouts_*_test_* database", name)
		}
		if databaseName == "" {
			databaseName, port = config.Database, config.Port
		}
		if config.Database != databaseName || config.Port != port {
			t.Fatalf("%s must target the same disposable database and port", name)
		}
		for _, fallback := range config.Fallbacks {
			if fallback.Host != config.Host || fallback.Port != port {
				t.Fatalf("%s must not include remote/fallback endpoints", name)
			}
		}
	}
	db := openTestDatabases(t)
	const worker = "ingest-partial-test"
	const filePartial = "Data sync partially completed. Some source files could not be processed. Successfully imported data has been kept. You can retry this sync."
	const fileFailed = "Data sync could not be completed because source files could not be processed. You can retry this sync."
	const genericPartial = "Data sync partially completed. One or more sources could not finish. Successfully imported data has been kept. You can retry this sync."
	const genericFailed = "Data sync could not be completed. Check the source details, then retry this sync."
	type child struct{ id, source, lease uuid.UUID }
	type fixture struct {
		account, parent uuid.UUID
		kind            string
		children        []child
	}
	create := func(t *testing.T, kind string, count int, account uuid.UUID) fixture {
		t.Helper()
		if account == uuid.Nil {
			account = uuid.New()
		}
		f := fixture{account: account, kind: kind}
		tx := beginAccount(t, db.ctx, db.migration, account)
		if _, err := tx.Exec(db.ctx, `INSERT INTO app.accounts(id) VALUES($1) ON CONFLICT DO NOTHING`, account); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(db.ctx); err != nil {
			t.Fatal(err)
		}
		f.parent = insertQueued(t, db.ctx, db.migration, account, uuid.Nil, kind)
		for range count {
			id := insertQueued(t, db.ctx, db.migration, account, f.parent, kind+"_source")
			f.children = append(f.children, child{id, snapshotSourceID(t, db, account, id), uuid.New()})
		}
		tx = beginAccount(t, db.ctx, db.migration, account)
		if _, err := tx.Exec(db.ctx, `INSERT INTO app.job_progress(job_id,account_id)
			SELECT id,account_id FROM app.jobs WHERE id=$1 OR parent_job_id=$1`, f.parent); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(db.ctx, `INSERT INTO app.job_source_contexts(job_id,account_id,source_id,source_generation,display_name,source_type)
			SELECT job.id,job.account_id,source.id,source.generation,source.display_name,source.type
			FROM app.jobs job JOIN app.job_config_snapshots snapshot ON snapshot.job_id=job.id AND snapshot.account_id=job.account_id
			JOIN app.sources source ON source.id=snapshot.source_id AND source.account_id=job.account_id WHERE job.parent_job_id=$1`, f.parent); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(db.ctx); err != nil {
			t.Fatal(err)
		}
		return f
	}
	claim := func(t *testing.T, f fixture, c child) {
		t.Helper()
		if !callBool(t, db.ctx, db.worker, f.account, `SELECT app.claim_job($1,$2,$3,interval '5 minutes')`, c.id, worker, c.lease) {
			t.Fatal("claim source child")
		}
	}
	files := func(t *testing.T, f fixture, c child, states ...string) {
		t.Helper()
		tx := beginAccount(t, db.ctx, db.worker, f.account)
		var source uuid.UUID
		if err := tx.QueryRow(db.ctx, `SELECT source_id FROM app.fence_ingest_job($1,$2,$3,30)`, c.id, worker, c.lease).Scan(&source); err != nil || source != c.source {
			t.Fatalf("file fence source=%s err=%v", source, err)
		}
		var frozen string
		if err := tx.QueryRow(db.ctx, `SELECT app.record_ingest_file_manifest($1,$2,$3,$4)`, c.id, worker, c.lease, reprocessingManifest(len(states))).Scan(&frozen); err != nil || frozen != "created" {
			t.Fatalf("freeze synthetic file receipts: %s %v", frozen, err)
		}
		for index, state := range states {
			id := uuid.New()
			name := fmt.Sprintf("HealthAutoExport-2026-01-%02d.json", index+1)
			if _, err := tx.Exec(db.ctx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes,modified_at,state,processing_started_at)
				VALUES($1,$2,$3,$4,$5,10,to_timestamp(100),'processing',clock_timestamp())`, id, f.account, c.source, c.id, name); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(db.ctx, `UPDATE app.source_files SET state=$2,processed_at=clock_timestamp(),
				checksum_sha256=CASE WHEN $2='succeeded' THEN decode(repeat('ab',32),'hex') END,
				failure_code=CASE WHEN $2='failed' THEN 'source-file-invalid' END,
				failure_summary=CASE WHEN $2='failed' THEN 'Synthetic invalid file.' END WHERE id=$1`, id, state); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(db.ctx); err != nil {
			t.Fatal(err)
		}
	}
	progress := func(t *testing.T, f fixture, c child, succeeded, skipped, failed int) {
		t.Helper()
		if !callBool(t, db.ctx, db.worker, f.account, `SELECT app.record_ingest_progress($1,$2,$3,$4,$5,$6,$7,0,0,0,0)`,
			c.id, worker, c.lease, succeeded+skipped+failed, skipped, succeeded, failed) {
			t.Fatal("record progress")
		}
	}
	finish := func(t *testing.T, f fixture, c child, status, code string) {
		t.Helper()
		var failureCode, summary any
		if code != "" {
			failureCode, summary = code, "Synthetic source failure."
		}
		if !callBool(t, db.ctx, db.worker, f.account, `SELECT app.finish_job($1,$2,$3,$4,$5,$6)`, c.id, worker, c.lease, status, failureCode, summary) {
			t.Fatal("finish source child")
		}
	}
	check := func(t *testing.T, f fixture, status, message string) {
		t.Helper()
		tx := beginAccount(t, db.ctx, db.migration, f.account)
		defer tx.Rollback(db.ctx)
		var got string
		var terminal bool
		var notifications int
		if err := tx.QueryRow(db.ctx, `SELECT status,terminal_at IS NOT NULL,
			(SELECT count(*) FROM app.notifications WHERE account_id=$2 AND job_id=$1 AND type='ingest-terminal')
			FROM app.jobs WHERE id=$1`, f.parent, f.account).Scan(&got, &terminal, &notifications); err != nil {
			t.Fatal(err)
		}
		wantTerminal := status != "running" && status != "queued"
		wantNotifications := 0
		if message != "" {
			wantNotifications = 1
		}
		if got != status || terminal != wantTerminal || notifications != wantNotifications {
			t.Fatalf("parent status=%s terminal=%t notifications=%d, want %s/%t/%d", got, terminal, notifications, status, wantTerminal, wantNotifications)
		}
		if message == "" {
			return
		}
		var correct bool
		severity := map[string]string{"succeeded": "info", "partially_succeeded": "warning", "failed": "error", "cancelled": "info"}[status]
		title := map[string]string{"succeeded": "Data sync completed", "partially_succeeded": "Data sync partially completed", "failed": "Data sync failed", "cancelled": "Data sync cancelled"}[status]
		if err := tx.QueryRow(db.ctx, `SELECT message=$2 AND severity=$3 AND title=$4 AND condition_key='job:'||$1::uuid::text
			AND subject_type='job' AND subject_id=$1 AND account_id=$5 FROM app.notifications WHERE job_id=$1 AND type='ingest-terminal'`,
			f.parent, message, severity, title, f.account).Scan(&correct); err != nil || !correct {
			t.Fatalf("incorrect persisted terminal banner: %t %v", correct, err)
		}
	}
	stable := func(t *testing.T, f fixture) {
		t.Helper()
		tx := beginAccount(t, db.ctx, db.migration, f.account)
		defer tx.Rollback(db.ctx)
		const snapshot = `SELECT jsonb_build_object('parent',to_jsonb(parent),'notifications',
			(SELECT jsonb_agg(to_jsonb(notification) ORDER BY id) FROM app.notifications notification WHERE job_id=$1))::text
			FROM app.jobs parent WHERE id=$1`
		var before, after string
		if err := tx.QueryRow(db.ctx, snapshot, f.parent).Scan(&before); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if _, err := tx.Exec(db.ctx, `SELECT app.derive_parent_status($1)`, f.parent); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.QueryRow(db.ctx, snapshot, f.parent).Scan(&after); err != nil || before != after {
			t.Fatalf("terminal parent/timestamps/notification changed on repeat derivation: %v", err)
		}
	}

	t.Run("MetadataAndInternalSecurity", func(t *testing.T) {
		var safe bool
		if err := db.migration.QueryRow(db.ctx, `SELECT (SELECT schema_version=30 AND minimum_runtime_version=30 AND migration_baseline=27 FROM app.schema_metadata WHERE singleton)
			AND (SELECT bool_and(p.prosecdef AND p.proconfig=ARRAY['search_path=pg_catalog, app']
			 AND pg_get_userbyid(p.proowner)=expected.owner
			 AND NOT has_function_privilege('workouts_api',p.oid,'EXECUTE')
			 AND NOT has_function_privilege('workouts_worker',p.oid,'EXECUTE')
			 AND NOT has_function_privilege('workouts_coverage_worker',p.oid,'EXECUTE')
			 AND NOT has_function_privilege('workouts_tiles',p.oid,'EXECUTE')
			 AND has_function_privilege('workouts_security_owner',p.oid,'EXECUTE')
			 AND NOT EXISTS(SELECT 1 FROM aclexplode(p.proacl) WHERE grantee=0))
			 FROM (VALUES ('app.derive_parent_status(uuid)','workouts_migration'),('app.notify_terminal_ingest_parent()','workouts_security_owner')) expected(signature,owner)
			 JOIN pg_proc p ON p.oid=expected.signature::regprocedure)
			AND (SELECT bool_and(relrowsecurity AND relforcerowsecurity) FROM pg_class
			 WHERE oid IN ('app.jobs'::regclass,'app.source_files'::regclass,'app.job_progress'::regclass,'app.notifications'::regclass))`).Scan(&safe); err != nil || !safe {
			t.Fatalf("metadata, helper security or account RLS changed: %t %v", safe, err)
		}
		if err := db.migration.QueryRow(db.ctx, `SELECT count(*)=3 AND bool_and(
			p.prosecdef AND p.pronargdefaults=0 AND p.proconfig=ARRAY['search_path=pg_catalog, app']
			AND pg_get_userbyid(p.proowner)='workouts_security_owner'
			AND has_function_privilege('workouts_api',p.oid,'EXECUTE')
			AND NOT has_function_privilege('workouts_worker',p.oid,'EXECUTE')
			AND NOT has_function_privilege('workouts_coverage_worker',p.oid,'EXECUTE')
			AND NOT has_function_privilege('workouts_tiles',p.oid,'EXECUTE')
			AND NOT EXISTS(SELECT 1 FROM aclexplode(p.proacl) WHERE grantee=0 OR is_grantable))
			FROM pg_proc p WHERE p.oid IN ('app.read_owned_job_files(uuid,integer,integer)'::regprocedure,
			'app.read_owned_job_files(uuid,integer,integer,boolean)'::regprocedure,
			'app.count_owned_job_rows(uuid,text)'::regprocedure)`).Scan(&safe); err != nil || !safe {
			t.Fatalf("file read ABIs or helper grants changed: %t %v", safe, err)
		}
	})
	t.Run("FailedFileScopeAndPagination", func(t *testing.T) {
		f := create(t, "manual_ingest", 3, uuid.Nil)
		for index, states := range [][]string{{"succeeded", "failed", "failed"}, {"failed"}} {
			claim(t, f, f.children[index])
			files(t, f, f.children[index], states...)
			finish(t, f, f.children[index], "failed", "source-file-invalid")
		}
		unrelated := create(t, "manual_ingest", 1, f.account)
		claim(t, unrelated, unrelated.children[0])
		files(t, unrelated, unrelated.children[0], "failed")
		other := create(t, "manual_ingest", 1, uuid.Nil)
		claim(t, other, other.children[0])
		files(t, other, other.children[0], "failed")
		for _, tc := range []struct {
			job         uuid.UUID
			all, failed int64
		}{{f.parent, 4, 3}, {f.children[0].id, 3, 2}, {f.children[1].id, 1, 1}, {f.children[2].id, 0, 0},
			{unrelated.parent, 1, 1}, {other.parent, 0, 0}, {other.children[0].id, 0, 0}, {uuid.New(), 0, 0}} {
			tx := beginAccount(t, db.ctx, db.api, f.account)
			var all, failed, legacyRows, falseRows, filteredRows, beyondRows int64
			if err := tx.QueryRow(db.ctx, `SELECT app.count_owned_job_rows($1,'file'),app.count_owned_job_rows($1,'failed_file'),
				(SELECT count(*) FROM app.read_owned_job_files($1,100,0)),
				(SELECT count(*) FROM app.read_owned_job_files($1,100,0,false)),
				(SELECT count(*) FROM app.read_owned_job_files($1,100,0,true)),
				(SELECT count(*) FROM app.read_owned_job_files($1,100,100,true))`, tc.job).
				Scan(&all, &failed, &legacyRows, &falseRows, &filteredRows, &beyondRows); err != nil ||
				all != tc.all || failed != tc.failed || legacyRows != tc.all || falseRows != tc.all || filteredRows != tc.failed || beyondRows != 0 {
				t.Fatalf("job=%s totals=%d/%d rows legacy/false/true/beyond=%d/%d/%d/%d want=%d/%d err=%v",
					tc.job, all, failed, legacyRows, falseRows, filteredRows, beyondRows, tc.all, tc.failed, err)
			}
			_ = tx.Rollback(db.ctx)
		}
		for _, bounds := range [][2]int{{0, 0}, {101, 0}, {1, -1}} {
			tx := beginAccount(t, db.ctx, db.api, f.account)
			_, err := tx.Exec(db.ctx, `SELECT * FROM app.read_owned_job_files($1,$2,$3,true)`, f.parent, bounds[0], bounds[1])
			_ = tx.Rollback(db.ctx)
			if err == nil {
				t.Fatalf("reader accepted invalid bounds %v", bounds)
			}
		}
	})
	for _, kind := range []string{"manual_ingest", "scheduled_ingest"} {
		t.Run(kind, func(t *testing.T) {
			for _, tc := range []struct {
				name, code, status, message string
				succeeded, skipped, failed  int
				progressSucceeded           int
				lag                         bool
			}{
				{"mixed_files_without_workouts", "source-file-invalid", "failed", fileFailed, 1, 0, 1, 1, false},
				{"all_files_failed", "source-file-invalid", "failed", fileFailed, 0, 0, 2, 0, false},
				{"skipped_and_failed", "source-file-invalid", "failed", fileFailed, 0, 1, 1, 0, false},
				{"durable_success_before_progress", "source-file-invalid", "failed", fileFailed, 1, 0, 1, 0, true},
				{"retained_success_progress", "source-file-invalid", "failed", fileFailed, 0, 0, 1, 1, false},
				{"fatal_after_file_commits", "source-read-failed", "failed", genericFailed, 1, 0, 0, 0, true},
				{"fatal_with_failed_files", "source-read-failed", "failed", genericFailed, 1, 0, 1, 1, false},
				{"source_failure", "source-config-invalid", "failed", genericFailed, 0, 0, 0, 0, false},
				{"file_code_without_failed_file", "source-file-invalid", "failed", genericFailed, 0, 0, 0, 0, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					f := create(t, kind, 1, uuid.Nil)
					c := f.children[0]
					check(t, f, "queued", "")
					claim(t, f, c)
					states := make([]string, 0, tc.succeeded+tc.failed)
					for range tc.succeeded {
						states = append(states, "succeeded")
					}
					for range tc.failed {
						states = append(states, "failed")
					}
					files(t, f, c, states...)
					if !tc.lag {
						progress(t, f, c, tc.progressSucceeded, tc.skipped, tc.failed)
					}
					check(t, f, "running", "")
					finish(t, f, c, "failed", tc.code)
					check(t, f, tc.status, tc.message)
					tx := beginAccount(t, db.ctx, db.migration, f.account)
					var unchanged bool
					if err := tx.QueryRow(db.ctx, `SELECT job.status='failed' AND job.failure_code=$2 AND state.last_sync_succeeded_at IS NULL
						AND (SELECT count(*) FROM app.source_files WHERE job_id=$1 AND state='succeeded')=$3
						AND (SELECT count(*) FROM app.source_files WHERE job_id=$1 AND state='failed')=$4
						AND (SELECT files_succeeded=$5 FROM app.job_progress WHERE job_id=$1)
						FROM app.jobs job JOIN app.source_sync_state state ON state.source_id=$6 AND state.account_id=job.account_id WHERE job.id=$1`,
						c.id, tc.code, tc.succeeded, tc.failed, tc.progressSucceeded, c.source).Scan(&unchanged); err != nil || !unchanged {
						t.Fatalf("child failure, durable files, lagging progress or freshness changed: %t %v", unchanged, err)
					}
					_ = tx.Rollback(db.ctx)
					if callBool(t, db.ctx, db.worker, f.account, `SELECT app.finish_job($1,$2,$3,'failed',$4,'Synthetic source failure.')`, c.id, worker, c.lease, tc.code) {
						t.Fatal("duplicate completion succeeded")
					}
					stable(t, f)
				})
			}
			t.Run("QueuedAndRunningSiblings", func(t *testing.T) {
				f := create(t, kind, 2, uuid.Nil)
				claim(t, f, f.children[0])
				files(t, f, f.children[0], "succeeded", "failed")
				finish(t, f, f.children[0], "failed", "source-file-invalid")
				check(t, f, "running", "")
				claim(t, f, f.children[1])
				check(t, f, "running", "")
				finish(t, f, f.children[1], "failed", "source-config-invalid")
				check(t, f, "failed", genericFailed)
				stable(t, f)
			})
			t.Run("SuccessfulChildContribution", func(t *testing.T) {
				f := create(t, kind, 2, uuid.Nil)
				claim(t, f, f.children[0])
				finish(t, f, f.children[0], "succeeded", "")
				claim(t, f, f.children[1])
				files(t, f, f.children[1], "failed")
				finish(t, f, f.children[1], "failed", "source-file-invalid")
				check(t, f, "failed", fileFailed)
			})
			for _, mixed := range []bool{false, true} {
				t.Run(fmt.Sprintf("CancelledChildren/mixed=%t", mixed), func(t *testing.T) {
					f := create(t, kind, 2, uuid.Nil)
					for i, c := range f.children {
						claim(t, f, c)
						files(t, f, c, "succeeded", "failed")
						progress(t, f, c, 1, 1, 1)
						if mixed && i == 0 {
							finish(t, f, c, "failed", "source-file-invalid")
						} else {
							if !requestCancellation(t, db, f.account, c.id) {
								t.Fatal("request source cancellation")
							}
							finish(t, f, c, "cancelled", "")
						}
					}
					status, message := "cancelled", "Your requested data sync was cancelled."
					if kind == "scheduled_ingest" {
						message = ""
					}
					if mixed {
						status, message = "failed", genericFailed
					}
					check(t, f, status, message)
					stable(t, f)
				})
			}
			for _, code := range []string{"source-file-invalid", "source-read-failed"} {
				t.Run("ImportedWorkoutBanner/"+code, func(t *testing.T) {
					f := create(t, kind, 1, uuid.Nil)
					c := f.children[0]
					claim(t, f, c)
					files(t, f, c, "succeeded", "failed")
					if !callBool(t, db.ctx, db.worker, f.account, `SELECT app.record_ingest_progress($1,$2,$3,2,0,1,1,1,0,0,0)`, c.id, worker, c.lease) {
						t.Fatal("record imported workout")
					}
					finish(t, f, c, "failed", code)
					message := genericPartial
					if code == "source-file-invalid" {
						message = filePartial
					}
					check(t, f, "partially_succeeded", message)
					stable(t, f)
				})
			}
			t.Run("SuccessfulParentPolicy", func(t *testing.T) {
				f := create(t, kind, 1, uuid.Nil)
				claim(t, f, f.children[0])
				finish(t, f, f.children[0], "succeeded", "")
				message := "Your requested data sync completed successfully."
				if kind == "scheduled_ingest" {
					message = ""
				}
				check(t, f, "succeeded", message)
			})
		})
	}
	t.Run("AccountAndJobIsolation", func(t *testing.T) {
		other := create(t, "manual_ingest", 1, uuid.Nil)
		claim(t, other, other.children[0])
		files(t, other, other.children[0], "succeeded", "failed")
		progress(t, other, other.children[0], 1, 1, 1)
		finish(t, other, other.children[0], "failed", "source-file-invalid")
		check(t, other, "failed", fileFailed)
		for _, account := range []uuid.UUID{uuid.Nil, other.account} {
			f := create(t, "manual_ingest", 1, account)
			claim(t, f, f.children[0])
			finish(t, f, f.children[0], "failed", "source-file-invalid")
			check(t, f, "failed", genericFailed)
			stable(t, f)
			if f.account == other.account {
				continue
			}
			tx := beginAccount(t, db.ctx, db.migration, f.account)
			if _, err := tx.Exec(db.ctx, `SELECT app.derive_parent_status($1)`, other.parent); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(db.ctx); err != nil {
				t.Fatal(err)
			}
			api := beginAccount(t, db.ctx, db.api, f.account)
			var invisible bool
			if err := api.QueryRow(db.ctx, `SELECT NOT EXISTS(SELECT 1 FROM app.jobs WHERE id=$1)
				AND NOT EXISTS(SELECT 1 FROM app.notifications WHERE job_id=$1)`, other.parent).Scan(&invisible); err != nil || !invisible {
				t.Fatalf("cross-account job/banner visible: %t %v", invisible, err)
			}
			_ = api.Rollback(db.ctx)
			check(t, other, "failed", fileFailed)
		}
	})
}
