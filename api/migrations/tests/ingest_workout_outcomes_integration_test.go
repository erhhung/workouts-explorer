package migrations_test

import (
	"os"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestIngestWorkoutOutcomesMigrationContract(t *testing.T) {
	read := func(name string) string {
		t.Helper()
		body, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		return string(body)
	}
	source := read("030_ingest_workout_outcomes.sql")
	for _, required := range []string{
		"CREATE FUNCTION app.ingest_outcome_status(job_kind text, job_status text, workouts_created bigint, workouts_updated bigint)",
		"RETURNS text LANGUAGE sql IMMUTABLE SECURITY INVOKER SET search_path = pg_catalog",
		"COALESCE(workouts_created, 0) > 0 OR COALESCE(workouts_updated, 0) > 0",
		"ALTER FUNCTION app.ingest_outcome_status(text, text, bigint, bigint) OWNER TO workouts_security_owner;",
		"GRANT CREATE ON SCHEMA app TO workouts_security_owner;",
		"REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;",
		"FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles, workouts_migration;",
		"GRANT EXECUTE ON FUNCTION app.ingest_outcome_status(text, text, bigint, bigint) TO workouts_api;",
		"COALESCE(sum(progress.workouts_created), 0) > 0 OR COALESCE(sum(progress.workouts_updated), 0) > 0",
		"WHEN cancelled_count = child_count THEN 'cancelled'",
		"WHEN failed_count + cancelled_count > 0 AND ingest_has_workouts THEN 'partially_succeeded'",
		"UPDATE app.schema_metadata SET schema_version = 30 WHERE singleton;",
		"ingest workout outcomes is forward-only",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("missing outcome migration contract: %s", required)
		}
	}
	for _, forbidden := range []string{
		"DISABLE TRIGGER", "session_replication_role", "DROP FUNCTION", "NO FORCE ROW LEVEL SECURITY",
		"UPDATE app.workouts", "UPDATE app.notifications", "UPDATE app.job_progress", "UPDATE app.source_files",
		"minimum_runtime_version =", "migration_baseline =", "GRANT SELECT", "GRANT ALL",
		"files_succeeded", "files_skipped", "workouts_unchanged", "app.source_files",
		"CREATE OR REPLACE FUNCTION app.notify_terminal_ingest_parent",
	} {
		if strings.Contains(source, forbidden) {
			t.Errorf("unexpected history mutation or outcome contribution: %s", forbidden)
		}
	}
	if strings.Count(source, "UPDATE app.jobs") != 1 {
		t.Fatal("only the guarded state-machine update may mutate jobs")
	}
	between := func(body, start, end string) string {
		t.Helper()
		_, body, ok := strings.Cut(body, start)
		if !ok {
			t.Fatalf("missing contract anchor: %s", start)
		}
		body, _, ok = strings.Cut(body, end)
		if !ok {
			t.Fatalf("missing contract anchor: %s", end)
		}
		return body
	}
	old := read("029_ingest_partial_success.sql")
	start := "    IF parent_kind = 'coverage_update' THEN\n"
	if between(old, start, "    ELSIF failed_count") != between(source, start, "    ELSIF failed_count") {
		t.Fatal("coverage progress/superseded accounting changed")
	}
	start = "    PERFORM set_config('app.job_transition', 'derive_parent', true);\n"
	if between(old, start, "$function$;") != between(source, start, "$function$;") {
		t.Fatal("parent counters/timestamps/status-change gate changed")
	}
	start = "    SELECT kind, status INTO parent_kind, parent_status FROM app.jobs\n"
	if between(old, start, "    IF child_count = 0 THEN RETURN; END IF;") != between(source, start, "    IF child_count = 0 THEN RETURN; END IF;") {
		t.Fatal("parent account scope, terminal guard or child status counts changed")
	}
	coverageCase := "WHEN succeeded_count > 0 AND failed_count + cancelled_count > 0 THEN 'partially_succeeded'\n" +
		"            WHEN succeeded_count = 0 AND cancelled_count = child_count THEN 'cancelled'\n" +
		"            ELSE 'failed' END"
	if !strings.Contains(source, coverageCase) {
		t.Fatal("coverage terminal status policy changed")
	}
	// All coverage proof, retry suppression, filtering and pagination SQL remains
	// byte-for-byte identical after removing the three ingest-only reader edits.
	start = "RETURNS TABLE(job_id uuid, display_status text, total_items bigint)\n"
	oldReader := between(read("027_coverage_recovery_baseline.sql"), start, "$function$;")
	newReader := between(source, start, "$function$;")
	newReader = strings.Replace(newReader,
		"ELSE app.ingest_outcome_status(job.kind, job.status, progress.workouts_created, progress.workouts_updated) END AS public_status",
		"ELSE job.status END AS public_status", 1)
	newReader = strings.Replace(newReader,
		"        LEFT JOIN app.job_progress progress ON progress.job_id = job.id AND progress.account_id = job.account_id\n", "", 1)
	newReader = strings.Replace(newReader,
		"              OR (job.kind IN ('manual_ingest','scheduled_ingest') AND job.status IN ('failed','partially_succeeded')\n"+
			"                  AND status_filter IN ('failed','partially_succeeded'))\n", "", 1)
	if oldReader != newReader {
		t.Fatal("history reader changes more than ingest projection and its early-filter admission")
	}
}

// The caller provisions roles and applies migrations through 030. Never use
// application configuration or a remote database for these synthetic fixtures.
func TestIngestWorkoutOutcomesPostgreSQL(t *testing.T) {
	if os.Getenv("INGEST_WORKOUT_OUTCOMES_TEST_DISPOSABLE") != "1" {
		t.Skip("INGEST_WORKOUT_OUTCOMES_TEST_DISPOSABLE=1 and disposable role database URLs are required")
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
			t.Fatalf("%s must name a disposable loopback workouts_*_test_* database", name)
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
	const worker = "ingest-outcomes-test"
	t.Run("MetadataAndSecurity", func(t *testing.T) {
		var safe bool
		if err := db.migration.QueryRow(db.ctx, `SELECT
			(SELECT schema_version=30 AND minimum_runtime_version=28 AND migration_baseline=27 FROM app.schema_metadata WHERE singleton)
			AND (SELECT p.provolatile='i' AND NOT p.prosecdef AND NOT p.proisstrict
			 AND p.proconfig=ARRAY['search_path=pg_catalog'] AND l.lanname='sql'
			 AND pg_get_userbyid(p.proowner)='workouts_security_owner'
			 AND has_function_privilege('workouts_api',p.oid,'EXECUTE')
			 AND has_function_privilege('workouts_security_owner',p.oid,'EXECUTE')
			 AND has_function_privilege('workouts_migration',p.oid,'EXECUTE')
			 AND NOT has_function_privilege('workouts_worker',p.oid,'EXECUTE')
			 AND NOT has_function_privilege('workouts_coverage_worker',p.oid,'EXECUTE')
			 AND NOT has_function_privilege('workouts_tiles',p.oid,'EXECUTE')
			 AND NOT EXISTS(SELECT 1 FROM aclexplode(p.proacl) acl
			     WHERE grantee NOT IN (p.proowner,'workouts_api'::regrole) OR is_grantable)
			 FROM pg_proc p JOIN pg_language l ON l.oid=p.prolang
			 WHERE p.oid='app.ingest_outcome_status(text,text,bigint,bigint)'::regprocedure)
			AND NOT has_schema_privilege('workouts_security_owner','app','CREATE')
			AND (SELECT bool_and(p.prosecdef AND p.proconfig=ARRAY['search_path=pg_catalog, app']
			 AND pg_get_userbyid(p.proowner)=expected.owner)
			 FROM (VALUES ('app.derive_parent_status(uuid)','workouts_migration'),
			 ('app.read_job_history(text,text,integer,bigint,integer)','workouts_security_owner'),
			 ('app.notify_terminal_ingest_parent()','workouts_security_owner')) expected(signature,owner)
			 JOIN pg_proc p ON p.oid=expected.signature::regprocedure)`).Scan(&safe); err != nil || !safe {
			t.Fatalf("outcome metadata/ownership/ACL/search-path contract: %t %v", safe, err)
		}
	})
	t.Run("HelperTruthTable", func(t *testing.T) {
		rows, err := db.api.Query(db.ctx, `SELECT kind,status,created,updated,app.ingest_outcome_status(kind,status,created,updated)
			FROM unnest(ARRAY['manual_ingest','scheduled_ingest','manual_ingest_source','scheduled_ingest_source',
			 'coverage_update','coverage_update_route','workout_deletion','source_connection_check','unknown']) kind
			CROSS JOIN unnest(ARRAY['failed','partially_succeeded','succeeded','cancelled','queued','running','superseded']) status
			CROSS JOIN (VALUES (NULL::bigint,NULL::bigint),(0,0),(1,0),(0,1),(1,1),
			 (NULL,1),(1,NULL),(-1,0),(0,-1)) counts(created,updated)`)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var kind, status, got string
			var created, updated *int64
			if err := rows.Scan(&kind, &status, &created, &updated, &got); err != nil {
				t.Fatal(err)
			}
			want := status
			if (kind == "manual_ingest" || kind == "scheduled_ingest" || kind == "manual_ingest_source" || kind == "scheduled_ingest_source") &&
				(status == "failed" || status == "partially_succeeded") {
				want = "failed"
				if (created != nil && *created > 0) || (updated != nil && *updated > 0) {
					want = "partially_succeeded"
				}
			}
			if got != want {
				t.Fatalf("kind=%s stored=%s counts=%v/%v got=%s want=%s", kind, status, created, updated, got, want)
			}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		rows.Close()
		var nulls bool
		if err := db.api.QueryRow(db.ctx, `SELECT app.ingest_outcome_status(NULL,'failed',1,0)='failed'
			AND app.ingest_outcome_status('manual_ingest',NULL,1,0) IS NULL`).Scan(&nulls); err != nil || !nulls {
			t.Fatalf("helper null kind/status semantics: %t %v", nulls, err)
		}
	})

	type fixture struct {
		account, parent uuid.UUID
		children        []uuid.UUID
	}
	create := func(t *testing.T, kind string, count int, account uuid.UUID) fixture {
		t.Helper()
		if account == uuid.Nil {
			account = uuid.New()
		}
		f := fixture{account: account}
		tx := beginAccount(t, db.ctx, db.migration, account)
		if _, err := tx.Exec(db.ctx, `INSERT INTO app.accounts(id) VALUES($1) ON CONFLICT DO NOTHING`, account); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(db.ctx); err != nil {
			t.Fatal(err)
		}
		f.parent = insertQueued(t, db.ctx, db.migration, account, uuid.Nil, kind)
		for range count {
			f.children = append(f.children, insertQueued(t, db.ctx, db.migration, account, f.parent, kind+"_source"))
		}
		tx = beginAccount(t, db.ctx, db.migration, account)
		defer tx.Rollback(db.ctx)
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
	type counts struct{ created, updated, unchanged, succeeded, skipped int }
	start := func(t *testing.T, tx pgx.Tx, child, lease uuid.UUID, c counts, durableFile bool) {
		t.Helper()
		var ok bool
		if err := tx.QueryRow(db.ctx, `SELECT app.claim_job($1,$2,$3,interval '5 minutes')`, child, worker, lease).Scan(&ok); err != nil || !ok {
			t.Fatalf("claim source: %t %v", ok, err)
		}
		if err := tx.QueryRow(db.ctx, `SELECT app.record_ingest_progress($1,$2,$3,$4,$5,$6,0,$7,$8,$9,0)`,
			child, worker, lease, c.succeeded+c.skipped, c.skipped, c.succeeded, c.created, c.updated, c.unchanged).Scan(&ok); err != nil || !ok {
			t.Fatalf("record source progress: %t %v", ok, err)
		}
		if durableFile {
			var source uuid.UUID
			if err := tx.QueryRow(db.ctx, `SELECT source_id FROM app.fence_ingest_job($1,$2,$3,28)`, child, worker, lease).Scan(&source); err != nil {
				t.Fatal(err)
			}
			file := uuid.New()
			if _, err := tx.Exec(db.ctx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes,state,processing_started_at)
				VALUES($1,app.current_account_id(),$2,$3,$1::uuid::text,1,'processing',clock_timestamp())`, file, source, child); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(db.ctx, `UPDATE app.source_files SET state='succeeded',processed_at=clock_timestamp(),
				checksum_sha256=decode(repeat('ab',32),'hex') WHERE id=$1`, file); err != nil {
				t.Fatal(err)
			}
		}
	}
	finish := func(t *testing.T, tx pgx.Tx, child, lease uuid.UUID, status string) {
		t.Helper()
		var code, summary any
		if status == "failed" {
			code, summary = "source-read-failed", "Synthetic source failure."
		}
		var ok bool
		if err := tx.QueryRow(db.ctx, `SELECT app.finish_job($1,$2,$3,$4,$5,$6)`, child, worker, lease, status, code, summary).Scan(&ok); err != nil || !ok {
			t.Fatalf("finish source: %t %v", ok, err)
		}
	}
	process := func(t *testing.T, f fixture, index int, status string, c counts, durableFile bool) {
		t.Helper()
		lease, child := uuid.New(), f.children[index]
		tx := beginAccount(t, db.ctx, db.worker, f.account)
		defer tx.Rollback(db.ctx)
		start(t, tx, child, lease, c, durableFile)
		if err := tx.Commit(db.ctx); err != nil {
			t.Fatal(err)
		}
		if status == "cancelled" && !requestCancellation(t, db, f.account, child) {
			t.Fatal("request child cancellation")
		}
		tx = beginAccount(t, db.ctx, db.worker, f.account)
		defer tx.Rollback(db.ctx)
		finish(t, tx, child, lease, status)
		if err := tx.Commit(db.ctx); err != nil {
			t.Fatal(err)
		}
	}
	check := func(t *testing.T, f fixture, want string) {
		t.Helper()
		got, _ := readStatus(t, db.ctx, db.migration, f.account, f.parent)
		if got != want {
			t.Fatalf("parent status=%s want=%s", got, want)
		}
		tx := beginAccount(t, db.ctx, db.api, f.account)
		defer tx.Rollback(db.ctx)
		var visible bool
		if err := tx.QueryRow(db.ctx, `SELECT EXISTS(SELECT 1 FROM app.read_job_history($2,'',100,0,3)
			WHERE job_id=$1 AND display_status=$2 AND total_items=1)`, f.parent, want).Scan(&visible); err != nil || !visible {
			t.Fatalf("API history projection/filter disagrees with new parent %s: %t %v", want, visible, err)
		}
	}
	stable := func(t *testing.T, f fixture, status, kind string) {
		t.Helper()
		tx := beginAccount(t, db.ctx, db.migration, f.account)
		defer tx.Rollback(db.ctx)
		const snapshot = `SELECT jsonb_build_object('jobs',(SELECT jsonb_agg(to_jsonb(j) ORDER BY id) FROM app.jobs j WHERE id=$1 OR parent_job_id=$1),
			'progress',(SELECT jsonb_agg(to_jsonb(p) ORDER BY job_id) FROM app.job_progress p WHERE job_id=$1 OR job_id=ANY($2::uuid[])),
			'notifications',(SELECT jsonb_agg(to_jsonb(n) ORDER BY id) FROM app.notifications n WHERE job_id=$1))::text`
		var before, after string
		if err := tx.QueryRow(db.ctx, snapshot, f.parent, f.children).Scan(&before); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			if _, err := tx.Exec(db.ctx, `SELECT app.derive_parent_status($1)`, f.parent); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.QueryRow(db.ctx, snapshot, f.parent, f.children).Scan(&after); err != nil || before != after {
			t.Fatalf("terminal derivation changed jobs/progress/notifications: %v", err)
		}
		var severity *string
		if err := tx.QueryRow(db.ctx, `SELECT (SELECT severity FROM app.notifications WHERE job_id=$1 AND type='ingest-terminal')`, f.parent).Scan(&severity); err != nil {
			t.Fatal(err)
		}
		if kind == "scheduled_ingest" && (status == "succeeded" || status == "cancelled") {
			if severity != nil {
				t.Fatal("scheduled success/cancellation unexpectedly notified")
			}
		} else if want := map[string]string{"succeeded": "info", "cancelled": "info", "failed": "error", "partially_succeeded": "warning"}[status]; severity == nil || *severity != want {
			t.Fatalf("notification severity=%v want=%s", severity, want)
		}
	}
	for _, kind := range []string{"manual_ingest", "scheduled_ingest"} {
		for _, tc := range []struct {
			name, first, second, want string
			counts                    counts
			durableFile               bool
		}{
			{"created_only", "failed", "failed", "partially_succeeded", counts{created: 1}, false},
			{"updated_only", "failed", "failed", "partially_succeeded", counts{updated: 1}, false},
			{"zero", "failed", "failed", "failed", counts{}, false},
			{"unchanged_only", "failed", "failed", "failed", counts{unchanged: 3}, false},
			{"skipped_only", "failed", "failed", "failed", counts{skipped: 3}, false},
			{"successful_files_empty", "failed", "failed", "failed", counts{succeeded: 3}, true},
			{"durable_file_without_progress", "failed", "failed", "failed", counts{}, true},
			{"empty_success_and_failure", "succeeded", "failed", "failed", counts{}, false},
			{"successful_source_imported", "succeeded", "failed", "partially_succeeded", counts{created: 1}, false},
			{"all_success_empty", "succeeded", "succeeded", "succeeded", counts{}, false},
			{"all_cancelled_with_workouts", "cancelled", "cancelled", "cancelled", counts{created: 1, updated: 1}, false},
			{"failed_and_cancelled_with_workouts", "failed", "cancelled", "partially_succeeded", counts{created: 1}, false},
			{"success_and_cancelled_empty", "succeeded", "cancelled", "failed", counts{}, false},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				f := create(t, kind, 2, uuid.Nil)
				check(t, f, "queued")
				process(t, f, 0, tc.first, tc.counts, tc.durableFile)
				check(t, f, "running")
				process(t, f, 1, tc.second, counts{}, false)
				check(t, f, tc.want)
				for index, want := range []string{tc.first, tc.second} {
					got, _ := readStatus(t, db.ctx, db.migration, f.account, f.children[index])
					if got != want {
						t.Fatalf("child status=%s want=%s; retry state must not follow display projection", got, want)
					}
					if want == "failed" {
						tx := beginAccount(t, db.ctx, db.migration, f.account)
						var retained bool
						err := tx.QueryRow(db.ctx, `SELECT job.failure_code='source-read-failed' AND state.last_sync_succeeded_at IS NULL
							FROM app.jobs job JOIN app.job_source_contexts context ON context.job_id=job.id AND context.account_id=job.account_id
							JOIN app.source_sync_state state ON state.source_id=context.source_id AND state.account_id=job.account_id
							WHERE job.id=$1`, f.children[index]).Scan(&retained)
						_ = tx.Rollback(db.ctx)
						if err != nil || !retained {
							t.Fatalf("failed child lost failure/freshness state needed for retry: %t %v", retained, err)
						}
					}
				}
				stable(t, f, tc.want, kind)
			})
		}
	}

	t.Run("HistoricalProjectionFiltersTotalsAndIsolation", func(t *testing.T) {
		f := create(t, "manual_ingest", 1, uuid.Nil)
		oldPartial := create(t, "manual_ingest", 2, f.account)
		success := create(t, "scheduled_ingest", 1, f.account)
		foreign := create(t, "manual_ingest", 1, uuid.Nil)
		process(t, success, 0, "succeeded", counts{}, false)
		process(t, foreign, 0, "failed", counts{created: 50}, false)
		// Use 029's actual state machine inside a rolled-back transaction, never
		// disabled triggers or terminal-row/progress rewrites. Created counters
		// without file-success counters legitimately produced 'failed' in 029;
		// an empty successful sibling produced the opposite old misclassification.
		tx := beginAccount(t, db.ctx, db.migration, f.account)
		defer tx.Rollback(db.ctx)
		definition := func(name string) string {
			t.Helper()
			body, err := migrations.Files.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			const start = "CREATE OR REPLACE FUNCTION app.derive_parent_status "
			_, bodyText, ok := strings.Cut(string(body), start)
			if !ok {
				t.Fatal("missing parent derive definition")
			}
			bodyText, _, ok = strings.Cut(bodyText, "$function$;")
			if !ok {
				t.Fatal("missing parent derive terminator")
			}
			return start + bodyText + "$function$;"
		}
		if _, err := tx.Exec(db.ctx, definition("029_ingest_partial_success.sql")); err != nil {
			t.Fatal(err)
		}
		for _, item := range []struct {
			child uuid.UUID
			state string
			c     counts
		}{{f.children[0], "failed", counts{created: 1}}, {oldPartial.children[0], "succeeded", counts{}}, {oldPartial.children[1], "failed", counts{}}} {
			lease := uuid.New()
			start(t, tx, item.child, lease, item.c, false)
			finish(t, tx, item.child, lease, item.state)
		}
		var historical bool
		if err := tx.QueryRow(db.ctx, `SELECT (SELECT status='failed' FROM app.jobs WHERE id=$1)
			AND (SELECT status='partially_succeeded' FROM app.jobs WHERE id=$2)
			AND (SELECT workouts_created=1 FROM app.job_progress WHERE job_id=$1)`, f.parent, oldPartial.parent).Scan(&historical); err != nil || !historical {
			t.Fatalf("029 failed-with-imports/partial-without-imports baseline: %t %v", historical, err)
		}
		const snapshot = `SELECT jsonb_build_object(
			'jobs',(SELECT jsonb_agg(to_jsonb(j) ORDER BY id) FROM app.jobs j WHERE account_id=$1),
			'progress',(SELECT jsonb_agg(to_jsonb(p) ORDER BY job_id) FROM app.job_progress p WHERE account_id=$1),
			'workouts',(SELECT jsonb_agg(to_jsonb(w) ORDER BY id) FROM app.workouts w WHERE account_id=$1),
			'notifications',(SELECT jsonb_agg(to_jsonb(n) ORDER BY id) FROM app.notifications n WHERE account_id=$1))::text`
		var before, after string
		if err := tx.QueryRow(db.ctx, snapshot, f.account).Scan(&before); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(db.ctx, definition("030_ingest_workout_outcomes.sql")); err != nil {
			t.Fatal(err)
		}
		for _, item := range []struct {
			filter, operation string
			limit, offset     int
			total             int64
			want              map[uuid.UUID]string
		}{
			{"", "", 100, 0, 3, map[uuid.UUID]string{f.parent: "partially_succeeded", oldPartial.parent: "failed", success.parent: "succeeded"}},
			{"partially_succeeded", "", 100, 0, 1, map[uuid.UUID]string{f.parent: "partially_succeeded"}},
			{"failed", "", 100, 0, 1, map[uuid.UUID]string{oldPartial.parent: "failed"}},
			{"failed", "", 1, 1, 1, nil},
			{"partially_succeeded", "manual_sync", 1, 1, 1, nil},
			{"succeeded", "automated_sync", 1, 0, 1, map[uuid.UUID]string{success.parent: "succeeded"}},
			{"", "manual_sync", 1, 2, 2, nil},
			{"", "manual_sync", 1, 0, 2, map[uuid.UUID]string{oldPartial.parent: "failed"}},
			{"", "manual_sync", 1, 1, 2, map[uuid.UUID]string{f.parent: "partially_succeeded"}},
			{"superseded", "", 100, 0, 0, nil},
			{"failed", "automated_sync", 100, 0, 0, nil},
		} {
			rows, err := tx.Query(db.ctx, `SELECT job_id,display_status,total_items FROM app.read_job_history($1,$2,$3,$4,3)`, item.filter, item.operation, item.limit, item.offset)
			if err != nil {
				t.Fatal(err)
			}
			got := map[uuid.UUID]string{}
			rowCount := 0
			for rows.Next() {
				rowCount++
				var id *uuid.UUID
				var status *string
				var total int64
				if err := rows.Scan(&id, &status, &total); err != nil {
					t.Fatal(err)
				}
				if total != item.total || (id == nil) != (status == nil) {
					t.Fatalf("filter=%s operation=%s total=%d want=%d or inconsistent sentinel", item.filter, item.operation, total, item.total)
				}
				if id != nil {
					got[*id] = *status
				}
			}
			rows.Close()
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if rowCount == 0 || (len(item.want) == 0 && rowCount != 1) {
				t.Fatalf("expected page rows or exactly one empty-page total sentinel, got %d rows", rowCount)
			}
			if len(got) != len(item.want) {
				t.Fatalf("filter=%s offset=%d rows=%v want=%v", item.filter, item.offset, got, item.want)
			}
			for id, status := range item.want {
				if got[id] != status {
					t.Fatalf("job %s status=%s want=%s", id, got[id], status)
				}
			}
		}
		for _, parent := range []uuid.UUID{f.parent, oldPartial.parent, foreign.parent} {
			if _, err := tx.Exec(db.ctx, `SELECT app.derive_parent_status($1)`, parent); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.QueryRow(db.ctx, snapshot, f.account).Scan(&after); err != nil || before != after {
			t.Fatalf("historical reads/derivation rewrote persisted history: %v", err)
		}
		// The API executes the same reader with no access to foreign job data.
		api := beginAccount(t, db.ctx, db.api, f.account)
		defer api.Rollback(db.ctx)
		var isolated bool
		if err := api.QueryRow(db.ctx, `SELECT NOT EXISTS(SELECT 1 FROM app.read_job_history('', '',100,0,3) WHERE job_id=$1)
			AND NOT EXISTS(SELECT 1 FROM app.jobs WHERE id=$1)`, foreign.parent).Scan(&isolated); err != nil || !isolated {
			t.Fatalf("history or jobs exposed another account: %t %v", isolated, err)
		}
	})
}
