package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/api/migrations"
	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/erhhung/workouts-explorer/internal/testutil/migrationfixture"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// Explicit, empty loopback scratch database only. No application configuration
// or credentials are loaded; all accounts, jobs and route state are synthetic.
func TestCoverageRetryEligibilityPostgreSQL(t *testing.T) {
	dsn := os.Getenv("PROJECT_RETRY_ELIGIBILITY_TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("PROJECT_RETRY_ELIGIBILITY_TEST_DISPOSABLE") != "1" {
		t.Skip("explicit disposable retry-eligibility database is required")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid scratch configuration")
	}
	if !strings.HasPrefix(config.ConnConfig.Database, "workouts_retry_eligibility_test_") ||
		(config.ConnConfig.Host != "127.0.0.1" && config.ConnConfig.Host != "localhost" && config.ConnConfig.Host != "::1") {
		t.Fatal("retry eligibility requires a named loopback scratch database")
	}
	for _, fallback := range config.ConnConfig.Fallbacks {
		if fallback.Host != config.ConnConfig.Host || fallback.Port != config.ConnConfig.Port {
			t.Fatal("remote fallback is prohibited")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	config.ConnConfig.RuntimeParams["statement_timeout"] = "5s"
	admin, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	var empty bool
	if err := admin.QueryRow(ctx, `SELECT to_regnamespace('app') IS NULL AND to_regclass('public.goose_db_version') IS NULL`).Scan(&empty); err != nil || !empty {
		t.Fatal("scratch database must be empty")
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
	for _, phase := range []string{migrationfixture.RouteCounts, migrationfixture.AutoRetries,
		migrationfixture.RuntimeCapability, migrationfixture.DiagnosticLimits} {
		if err := migrationfixture.ApplyPhase(ctx, sqlDB, migrations.Files, phase); err != nil {
			t.Fatal(err)
		}
	}
	apiConfig := config.Copy()
	apiConfig.ConnConfig.RuntimeParams["role"] = "workouts_api"
	apiDB, err := pgxpool.NewWithConfig(ctx, apiConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer apiDB.Close()
	if database.Ready(ctx, apiDB) {
		t.Fatal("incomplete presentation checkpoint must not be current-runtime ready")
	}
	exec := func(tx pgx.Tx, sql string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	synthetic := func(sql string, args ...any) {
		t.Helper()
		tx, err := admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		exec(tx, `SET LOCAL session_replication_role='replica'`)
		exec(tx, sql, args...)
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	type fixture struct {
		principal, account, parent, source uuid.UUID
		children, workouts                 []uuid.UUID
		bearer                             string
	}
	seed := func(depth int) fixture {
		t.Helper()
		principal, account := insertSourceTestUser(t, admin)
		f := fixture{principal: principal, account: account, source: uuid.New(), bearer: insertTestSession(t, apiDB, principal, "bearer", "")}
		tx, err := admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		exec(tx, `SET LOCAL session_replication_role='replica'`)
		exec(tx, `INSERT INTO app.sources(id,account_id,display_name,canonical_display_name,type,status,config_envelope)
			VALUES($1,$2,'Synthetic retry','synthetic retry','health-auto-export-local','connected',$3)`, f.source, account, []byte(`{"synthetic":true}`))
		var prior *uuid.UUID
		for range depth {
			id := uuid.New()
			exec(tx, `INSERT INTO app.jobs(id,account_id,kind,priority,status,terminal_at,retry_of_job_id)
				VALUES($1,$2,'coverage_update',20,'failed',now(),$3)`, id, account, prior)
			exec(tx, `INSERT INTO app.coverage_job_contexts(job_id,account_id,region_id,target_osm_generation,target_work_revision,
				rules_version,sampling_version,path_policy_version,minimum_traversal_meters,matcher_contract_version)
				VALUES($1,$2,'geofabrik:test',1,1,'coverage-experimental-v1','coverage-sampling-experimental-v1','coverage-path-policy-experimental-v82',5,'coverage-production-v1')`, id, account)
			f.parent, prior = id, &id
		}
		file, activity := uuid.New(), uuid.New()
		exec(tx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes,state,checksum_sha256,processing_started_at,processed_at)
			VALUES($1,$2,$3,$4,'HealthAutoExport-2026-01-01.json',1,'succeeded',decode(repeat('01',32),'hex'),now(),now())`, file, account, f.source, f.parent)
		exec(tx, `INSERT INTO app.workout_types(id,account_id,type_key,provider_label) VALUES($1,$2,'walking','Walking')`, activity, account)
		for range 2 {
			child, workout := uuid.New(), uuid.New()
			f.children, f.workouts = append(f.children, child), append(f.workouts, workout)
			exec(tx, `INSERT INTO app.workouts(id,account_id,source_id,source_file_id,workout_type_id,provider_id,content_sha256,provider_label,started_at,ended_at,provider_duration)
				VALUES($1,$2,$3,$4,$5,$6,decode(repeat('01',32),'hex'),'Walking','2026-01-01Z','2026-01-01Z',0)`, workout, account, f.source, file, activity, workout.String())
			exec(tx, `INSERT INTO app.jobs(id,account_id,parent_job_id,kind,priority,status,terminal_at,failure_code)
				VALUES($1,$2,$3,'coverage_update_route',20,'failed',now(),'coverage-route-timeout')`, child, account, f.parent)
			exec(tx, `INSERT INTO app.coverage_route_job_contexts(job_id,account_id,workout_id,route_input_revision,route_input_sha256,target_generations)
				VALUES($1,$2,$3,1,decode(repeat('01',32),'hex'),'[{"regionId":"geofabrik:test","generation":1}]')`, child, account, workout)
			exec(tx, `INSERT INTO app.workout_coverage_states(account_id,workout_id,route_input_revision,route_input_sha256,readiness_state,map_data_ready_at,
				processing_state,target_job_id,target_revision,target_rules_version,target_sampling_version,target_path_policy_version,target_generations)
				VALUES($1,$2,1,decode(repeat('01',32),'hex'),'map_data_ready',now(),'failed',$3,1,
				'coverage-experimental-v1','coverage-sampling-experimental-v1','coverage-path-policy-experimental-v82','[{"regionId":"geofabrik:test","generation":1}]')`, account, workout, child)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		return f
	}
	legacy := seed(2)
	makeCurrent := func(f fixture, ids []uuid.UUID) []uuid.UUID {
		t.Helper()
		tx, err := admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		exec(tx, `SET LOCAL session_replication_role='replica'`)
		parents := make([]uuid.UUID, 0, len(ids))
		for index, workout := range ids {
			prior, replacement, priorChild, child := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			parents = append(parents, replacement)
			exec(tx, `INSERT INTO app.jobs(id,account_id,kind,priority,status,terminal_at,retry_of_job_id,created_at)
				VALUES($1,$3,'coverage_update',20,'failed',now(),NULL,now()),
				($2,$3,'coverage_update',20,'succeeded',now(),$1,now()+$4*interval '1 second')`, prior, replacement, f.account, index)
			exec(tx, `INSERT INTO app.coverage_job_contexts(job_id,account_id,region_id,target_osm_generation,target_work_revision,
				rules_version,sampling_version,path_policy_version,minimum_traversal_meters,matcher_contract_version)
				SELECT target.id,context.account_id,region_id,target_osm_generation,target_work_revision+CASE WHEN target.id=$3 THEN 1 ELSE 0 END,rules_version,sampling_version,
				path_policy_version,minimum_traversal_meters,matcher_contract_version
				FROM app.coverage_job_contexts context CROSS JOIN unnest($2::uuid[]) target(id) WHERE context.job_id=$1`, f.parent, []uuid.UUID{prior, replacement}, replacement)
			exec(tx, `INSERT INTO app.jobs(id,account_id,parent_job_id,kind,priority,status,terminal_at,retry_of_job_id)
				VALUES($1,$3,$4,'coverage_update_route',20,'failed',now(),NULL),
				($2,$3,$5,'coverage_update_route',20,'succeeded',now(),$1)`, priorChild, child, f.account, prior, replacement)
			exec(tx, `INSERT INTO app.coverage_route_job_contexts(job_id,account_id,workout_id,route_input_revision,route_input_sha256,target_generations,result_outcome)
				SELECT target.id,account_id,workout_id,route_input_revision,route_input_sha256,target_generations,
				CASE WHEN target.id=$4 THEN 'applied' END
				FROM app.workout_coverage_states CROSS JOIN unnest($1::uuid[]) target(id)
				WHERE account_id=$2 AND workout_id=$3`, []uuid.UUID{priorChild, child}, f.account, workout, child)
			exec(tx, `UPDATE app.workout_coverage_states SET processing_state='current',target_job_id=$3,
				applied_route_input_revision=route_input_revision,applied_route_input_sha256=route_input_sha256,
				applied_rules_version=target_rules_version,applied_sampling_version=target_sampling_version,
				applied_path_policy_version=target_path_policy_version,applied_generations=target_generations
				WHERE account_id=$1 AND workout_id=$2`, f.account, workout, child)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		slices.Reverse(parents)
		return parents
	}
	legacyCurrent := makeCurrent(legacy, legacy.workouts)
	// One historical child points into a different valid parent chain. Current
	// authority must remain navigable without repairing or inventing ancestry.
	synthetic(`UPDATE app.jobs SET retry_of_job_id=(SELECT child.retry_of_job_id FROM app.jobs child WHERE child.parent_job_id=$2)
		WHERE id=$1`, legacy.children[0], legacyCurrent[1])
	const functions = `SELECT jsonb_agg(to_jsonb(p) ORDER BY p.oid)::text FROM pg_proc p
		WHERE pronamespace='app'::regnamespace AND proname NOT IN ('read_coverage_retry_eligibility','read_job_history')`
	var before, after string
	if err := admin.QueryRow(ctx, functions).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if err := migrationfixture.FinishCanonical27(ctx, sqlDB, migrations.Files, migrationfixture.DiagnosticLimits); err != nil {
		t.Fatal(err)
	}
	if err := admin.QueryRow(ctx, functions).Scan(&after); err != nil || before != after {
		t.Fatalf("retry presentation changed existing helpers: %v", err)
	}
	if database.Ready(ctx, apiDB) {
		t.Fatal("canonical27 alone must not satisfy pipeline readiness")
	}
	if _, err := provider.UpTo(ctx, database.SupportedSchemaVersion); err != nil {
		t.Fatal(err)
	}
	server := integrationServer(t, apiDB, &recordingSender{})
	handler, err := NewHandlerContext(ctx, server.config, apiDB, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	get := func(f fixture, reason string, eligible, unsuccessful, current int64) *generated.CoverageRetryEligibility {
		t.Helper()
		response := routeJobRequest(handler, http.MethodGet, "/api/jobs/"+compactUUID(f.parent), "", f.bearer, "", false)
		if response.recorder.Code != http.StatusOK {
			t.Fatalf("detail: %d %s", response.recorder.Code, response.recorder.Body.String())
		}
		validateRecordedResponse(t, http.MethodGet, "/api/jobs/"+compactUUID(f.parent), response.recorder)
		var detail generated.JobDetail
		if err := json.Unmarshal(response.recorder.Body.Bytes(), &detail); err != nil {
			t.Fatal(err)
		}
		e := detail.RetryEligibility
		if e == nil || string(e.Reason) != reason || e.CanRetry != (reason == "eligible") || e.EligibleRouteCount != eligible || e.UnsuccessfulRouteCount != unsuccessful || e.CurrentRouteCount != current {
			t.Fatalf("unexpected eligibility: %+v", e)
		}
		if e.CurrentCoverageJobIds == nil || len(e.CurrentCoverageJobIds) > 100 {
			t.Fatalf("current coverage IDs must be a bounded non-null array: %+v", e)
		}
		wantSuperseded := reason == "already_current" && (detail.Status == generated.JobStatusFailed || detail.Status == generated.JobStatusPartiallySucceeded)
		if (detail.DisplayStatus != nil) != wantSuperseded || (detail.DisplayStatus != nil && *detail.DisplayStatus != generated.JobDisplayStatusSuperseded) {
			t.Fatalf("detail display projection does not match raw status/proof: %+v", detail)
		}
		for _, child := range detail.Children {
			if child.RetryEligibility != nil || child.DisplayStatus != nil {
				t.Fatal("child exposed parent eligibility or display projection")
			}
		}
		return e
	}
	assertCurrentJobs := func(e *generated.CoverageRetryEligibility, ids ...uuid.UUID) {
		t.Helper()
		want := make([]generated.CompactUUID, len(ids))
		for index, id := range ids {
			want[index] = compactUUID(id)
		}
		if !slices.Equal(e.CurrentCoverageJobIds, want) {
			t.Fatalf("current parents=%v, want %v", e.CurrentCoverageJobIds, want)
		}
	}
	list := func(f fixture, query string) generated.JobList {
		t.Helper()
		path := "/api/jobs" + query
		response := routeJobRequest(handler, http.MethodGet, path, "", f.bearer, "", false)
		if response.recorder.Code != http.StatusOK {
			t.Fatalf("history: %d %s", response.recorder.Code, response.recorder.Body.String())
		}
		validateRecordedResponse(t, http.MethodGet, path, response.recorder)
		var result generated.JobList
		if err := json.Unmarshal(response.recorder.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	checkPage := func(result generated.JobList, total int64, page, size, items int) {
		t.Helper()
		pages := (total + int64(size) - 1) / int64(size)
		if result.Pagination.TotalItems != total || result.Pagination.TotalPages != pages || result.Pagination.Page != page ||
			result.Pagination.PageSize != size || len(result.Items) != items {
			t.Fatalf("pagination=%+v items=%d, want total=%d page=%d size=%d items=%d", result.Pagination, len(result.Items), total, page, size, items)
		}
	}
	post := func(f fixture, status int, reason string) {
		t.Helper()
		response := routeJobRequest(handler, http.MethodPost, "/api/jobs/"+compactUUID(f.parent)+"/retry", "{}", f.bearer, "", false)
		if response.recorder.Code != status {
			t.Fatalf("retry: %d %s", response.recorder.Code, response.recorder.Body.String())
		}
		validateRecordedResponse(t, http.MethodPost, "/api/jobs/"+compactUUID(f.parent)+"/retry", response.recorder)
		if reason != "" {
			var problem generated.Problem
			if err := json.Unmarshal(response.recorder.Body.Bytes(), &problem); err != nil || problem.Type != "https://workouts-explorer.invalid/problems/coverage-retry/"+reason || strings.Contains(response.recorder.Body.String(), "private-sql-canary") {
				t.Fatalf("unsafe/untyped conflict: %s", response.recorder.Body.String())
			}
		}
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := admin.QueryRow(ctx, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	const snapshotSQL = `SELECT jsonb_build_object(
		'jobs',(SELECT jsonb_agg(to_jsonb(j) ORDER BY id) FROM app.jobs j),
		'contexts',(SELECT jsonb_agg(to_jsonb(c) ORDER BY job_id) FROM app.coverage_route_job_contexts c),
		'parentContexts',(SELECT jsonb_agg(to_jsonb(c) ORDER BY job_id) FROM app.coverage_job_contexts c),
		'states',(SELECT jsonb_agg(to_jsonb(s) ORDER BY account_id,workout_id) FROM app.workout_coverage_states s),
		'roots',(SELECT jsonb_agg(to_jsonb(r) ORDER BY root_job_id) FROM app.coverage_retry_roots r),
		'attempts',(SELECT jsonb_agg(to_jsonb(a) ORDER BY job_id) FROM app.coverage_retry_attempts a))::text`
	t.Run("legacy historical failures already current and read only", func(t *testing.T) {
		if count(`SELECT count(*) FROM app.jobs child JOIN app.jobs parent ON parent.id=child.parent_job_id
			JOIN app.jobs predecessor ON predecessor.id=child.retry_of_job_id
			JOIN app.coverage_route_job_contexts context ON context.job_id=child.id
			JOIN app.coverage_route_job_contexts prior_context ON prior_context.job_id=predecessor.id
			WHERE parent.id=$1 AND context.workout_id=prior_context.workout_id
			AND parent.retry_of_job_id IS DISTINCT FROM predecessor.parent_job_id`, legacy.parent) != 1 {
			t.Fatal("fixture must retain one historical child/parent lineage disagreement for the same workout")
		}
		var before, after string
		if err := admin.QueryRow(ctx, snapshotSQL).Scan(&before); err != nil {
			t.Fatal(err)
		}
		assertCurrentJobs(get(legacy, "already_current", 0, 2, 2), legacyCurrent...)
		post(legacy, http.StatusConflict, "already-current")
		tx, err := apiDB.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		exec(tx, `SELECT set_config('app.account_id',$1,true)`, legacy.account.String())
		if e, err := readCoverageRetryEligibility(ctx, tx, legacy.parent); err != nil || e.CurrentRouteCount != 2 {
			t.Fatalf("read-only helper: %+v %v", e, err)
		} else {
			assertCurrentJobs(e, legacyCurrent...)
		}
		if err := admin.QueryRow(ctx, snapshotSQL).Scan(&after); err != nil || before != after {
			t.Fatalf("advisory reads/conflict changed jobs, ancestry, states or policy: %v", err)
		}
		for _, id := range legacyCurrent {
			linked := legacy
			linked.parent = id
			assertCurrentJobs(get(linked, "not_terminal", 0, 0, 0))
			if count(`SELECT count(*) FROM app.jobs child JOIN app.jobs parent ON parent.id=child.parent_job_id
				JOIN app.jobs prior_child ON prior_child.id=child.retry_of_job_id
				WHERE parent.id=$1 AND parent.retry_of_job_id=prior_child.parent_job_id`, id) != 1 {
				t.Fatal("current parent lost its valid independent retry ancestry")
			}
		}
		if count(`SELECT count(*) FROM app.coverage_retry_roots`) != 0 || count(`SELECT count(*) FROM app.coverage_retry_attempts`) != 0 {
			t.Fatal("historical GET/POST enrolled retry policy")
		}
	})
	t.Run("legacy exact eligible manual retry and latest only", func(t *testing.T) {
		f := seed(2)
		assertCurrentJobs(get(f, "eligible", 2, 2, 0))
		post(f, http.StatusAccepted, "")
		if count(`SELECT count(*) FROM app.jobs parent JOIN app.jobs child ON child.parent_job_id=parent.id
			LEFT JOIN app.jobs predecessor ON predecessor.id=child.retry_of_job_id
			WHERE parent.retry_of_job_id=$1 AND predecessor.parent_job_id IS DISTINCT FROM parent.retry_of_job_id`, f.parent) != 0 ||
			count(`SELECT count(*) FROM app.jobs child JOIN app.jobs parent ON parent.id=child.parent_job_id WHERE parent.retry_of_job_id=$1`, f.parent) != 2 {
			t.Fatal("manual constructor produced inconsistent child/parent retry ancestry")
		}
		get(f, "not_latest", 0, 2, 0)
		post(f, http.StatusConflict, "not-latest")
		if count(`SELECT count(*) FROM app.coverage_retry_roots WHERE account_id=$1 AND enrolled_at IS NULL AND automatic_attempts_used=0`, f.account) != 1 ||
			count(`SELECT count(*) FROM app.coverage_retry_attempts attempt JOIN app.jobs job ON job.id=attempt.job_id WHERE job.account_id=$1 AND attempt.origin='manual'`, f.account) != 1 {
			t.Fatal("manual legacy retry enrolled automatic retries")
		}
	})
	t.Run("retry limit and not terminal", func(t *testing.T) {
		f := seed(100)
		get(f, "retry_limit", 2, 2, 0)
		post(f, http.StatusConflict, "retry-limit")
		f = seed(1)
		synthetic(`UPDATE app.jobs SET status='queued',terminal_at=NULL WHERE id=$1`, f.parent)
		get(f, "not_terminal", 2, 2, 0)
		post(f, http.StatusConflict, "not-terminal")
	})
	for _, status := range []string{"failed", "partially_succeeded"} {
		t.Run("resolved ancestor overrides latest advisory guard/"+status, func(t *testing.T) {
			f := seed(1)
			synthetic(`UPDATE app.jobs SET status=$2 WHERE id=$1`, f.parent, status)
			post(f, http.StatusAccepted, "")
			current := makeCurrent(f, f.workouts[:1])
			assertCurrentJobs(get(f, "not_latest", 0, 2, 1), current...)
			post(f, http.StatusConflict, "not-latest")
			rest := makeCurrent(f, f.workouts[1:])
			current = append(rest, current...)
			var before, after string
			if err := admin.QueryRow(ctx, snapshotSQL).Scan(&before); err != nil {
				t.Fatal(err)
			}
			assertCurrentJobs(get(f, "already_current", 0, 2, 2), current...)
			// POST remains authoritative and keeps its existing constructor guard.
			post(f, http.StatusConflict, "not-latest")
			if err := admin.QueryRow(ctx, snapshotSQL).Scan(&after); err != nil || before != after {
				t.Fatalf("resolved ancestor GET/POST mutated history or state: %v", err)
			}
			if count(`SELECT count(*) FROM app.jobs WHERE retry_of_job_id=$1 AND parent_job_id IS NULL`, f.parent) != 1 {
				t.Fatal("resolved ancestor must retain exactly its existing successor")
			}
			synthetic(`UPDATE app.jobs SET status='cancelled' WHERE id=$1`, f.parent)
			assertCurrentJobs(get(f, "already_current", 0, 2, 2), current...)
			synthetic(`UPDATE app.jobs SET status='succeeded' WHERE id=$1`, f.parent)
			assertCurrentJobs(get(f, "not_terminal", 0, 2, 2), current...)
		})
	}
	t.Run("resolved depth 100 overrides limit advisory guard without permitting retry", func(t *testing.T) {
		f := seed(100)
		get(f, "retry_limit", 2, 2, 0)
		post(f, http.StatusConflict, "retry-limit")
		current := makeCurrent(f, f.workouts)
		var before, after string
		if err := admin.QueryRow(ctx, snapshotSQL).Scan(&before); err != nil {
			t.Fatal(err)
		}
		assertCurrentJobs(get(f, "already_current", 0, 2, 2), current...)
		projected := list(f, "?status=superseded")
		checkPage(projected, 1, 1, 25, 1)
		if projected.Items[0].Id != compactUUID(f.parent) || projected.Items[0].DisplayStatus == nil || *projected.Items[0].DisplayStatus != generated.JobDisplayStatusSuperseded {
			t.Fatal("resolved depth-limit attempt must be superseded in history as well as detail")
		}
		checkPage(list(f, "?status=failed"), 0, 1, 25, 0)
		post(f, http.StatusConflict, "retry-limit")
		if err := admin.QueryRow(ctx, snapshotSQL).Scan(&after); err != nil || before != after {
			t.Fatalf("resolved limit GET/POST mutated history or state: %v", err)
		}
	})
	t.Run("cancelled cleared pointer mixed with failed sibling", func(t *testing.T) {
		f := seed(1)
		synthetic(`UPDATE app.jobs SET status='cancelled' WHERE id=$1`, f.children[0])
		synthetic(`UPDATE app.workout_coverage_states SET target_job_id=NULL,processing_state='stale',processing_finished_at=(SELECT terminal_at FROM app.jobs WHERE id=$1)
			WHERE workout_id=$2`, f.children[0], f.workouts[0])
		get(f, "eligible", 2, 2, 0)
		post(f, http.StatusAccepted, "")
	})
	t.Run("target changes after GET no mutation", func(t *testing.T) {
		f := seed(1)
		get(f, "eligible", 2, 2, 0)
		synthetic(`UPDATE app.workout_coverage_states SET route_input_revision=2 WHERE account_id=$1`, f.account)
		post(f, http.StatusConflict, "no-eligible-routes")
		get(f, "no_eligible_routes", 0, 2, 0)
		if count(`SELECT count(*) FROM app.jobs WHERE retry_of_job_id=$1`, f.parent) != 0 {
			t.Fatal("stale GET authorized mutation")
		}
	})
	t.Run("busy rolls back and remains eligible", func(t *testing.T) {
		f := seed(1)
		tx, err := admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		exec(tx, `SELECT 1 FROM app.workout_coverage_states WHERE workout_id=$1 FOR UPDATE`, f.workouts[0])
		post(f, http.StatusConflict, "busy")
		if count(`SELECT count(*) FROM app.jobs WHERE retry_of_job_id=$1`, f.parent) != 0 {
			t.Fatal("busy left partial retry")
		}
		_ = tx.Rollback(ctx)
		get(f, "eligible", 2, 2, 0)
	})
	t.Run("missing proof never claims all current", func(t *testing.T) {
		f := seed(1)
		makeCurrent(f, f.workouts)
		synthetic(`DELETE FROM app.coverage_route_job_contexts WHERE job_id=$1`, f.children[1])
		get(f, "no_eligible_routes", 0, 2, 1)
		post(f, http.StatusConflict, "no-eligible-routes")
	})
	for name, mutation := range map[string]string{
		"changed input":       `UPDATE app.workout_coverage_states SET route_input_revision=2 WHERE account_id=$1`,
		"changed digest":      `UPDATE app.workout_coverage_states SET route_input_sha256=decode(repeat('02',32),'hex') WHERE account_id=$1`,
		"map no longer ready": `UPDATE app.workout_coverage_states SET readiness_state='unresolved',map_data_ready_at=NULL WHERE account_id=$1`,
		"deleting workouts":   `UPDATE app.workouts SET deletion_requested_at=now(),deletion_target_id=gen_random_uuid() WHERE account_id=$1`,
		"deleted source":      `UPDATE app.sources SET deleted_at=now(),config_envelope=NULL,auto_sync_enabled=false WHERE account_id=$1`,
	} {
		t.Run("current requires exact proof/"+name, func(t *testing.T) {
			f := seed(1)
			makeCurrent(f, f.workouts)
			synthetic(mutation, f.account)
			assertCurrentJobs(get(f, "no_eligible_routes", 0, 2, 0))
			post(f, http.StatusConflict, "no-eligible-routes")
		})
	}
	t.Run("mixed current and eligible counts only unsuccessful children", func(t *testing.T) {
		f := seed(1)
		current := makeCurrent(f, f.workouts[:1])
		assertCurrentJobs(get(f, "eligible", 1, 2, 1), current...)
		post(f, http.StatusAccepted, "")
		if count(`SELECT count(*) FROM app.jobs child JOIN app.jobs parent ON parent.id=child.parent_job_id WHERE parent.retry_of_job_id=$1`, f.parent) != 1 {
			t.Fatal("retry included current historical child")
		}
	})
	t.Run("current links are distinct and require exact owned success authority", func(t *testing.T) {
		f := seed(1)
		current := makeCurrent(f, f.workouts)
		synthetic(`UPDATE app.jobs SET parent_job_id=$1 WHERE parent_job_id=$2`, current[0], current[1])
		assertCurrentJobs(get(f, "already_current", 0, 2, 2), current[0])
		synthetic(`UPDATE app.jobs SET status='cancelled' WHERE id=$1`, f.children[0])
		assertCurrentJobs(get(f, "already_current", 0, 2, 2), current[0])
	})
	t.Run("newer complete healthy raw input is already current", func(t *testing.T) {
		f := seed(1)
		current := makeCurrent(f, f.workouts)
		synthetic(`UPDATE app.workout_coverage_states SET route_input_revision=2,applied_route_input_revision=2,
			route_input_sha256=decode(repeat('02',32),'hex'),applied_route_input_sha256=decode(repeat('02',32),'hex') WHERE account_id=$1`, f.account)
		synthetic(`UPDATE app.coverage_route_job_contexts SET route_input_revision=2,route_input_sha256=decode(repeat('02',32),'hex')
			WHERE account_id=$1 AND job_id<>ALL($2::uuid[])`, f.account, f.children)
		assertCurrentJobs(get(f, "already_current", 0, 2, 2), current...)
		post(f, http.StatusConflict, "already-current")
	})
	t.Run("valid independent G2 chains supersede failed P A B without mutation", func(t *testing.T) {
		f := seed(1)
		current := makeCurrent(f, f.workouts)
		const generations = `[{"regionId":"geofabrik:test","generation":2}]`
		synthetic(`UPDATE app.coverage_job_contexts SET target_osm_generation=2 WHERE account_id=$1 AND job_id<>$2`, f.account, f.parent)
		synthetic(`UPDATE app.coverage_route_job_contexts SET target_generations=$3 WHERE account_id=$1 AND job_id<>ALL($2::uuid[])`, f.account, f.children, generations)
		synthetic(`UPDATE app.workout_coverage_states SET applied_generations=$2,target_generations=$2 WHERE account_id=$1`, f.account, generations)
		synthetic(`INSERT INTO app.workout_coverage_regions(account_id,workout_id,region_id,desired_osm_generation)
			SELECT account_id,workout_id,'geofabrik:test',2 FROM app.workout_coverage_states WHERE account_id=$1`, f.account)
		if count(`SELECT count(*) FROM app.jobs child JOIN app.jobs parent ON parent.id=child.parent_job_id
			LEFT JOIN app.jobs predecessor ON predecessor.id=child.retry_of_job_id WHERE child.account_id=$1
			AND ((parent.retry_of_job_id IS NULL AND child.retry_of_job_id IS NOT NULL)
			OR (parent.retry_of_job_id IS NOT NULL AND predecessor.parent_job_id IS DISTINCT FROM parent.retry_of_job_id))`, f.account) != 0 ||
			count(`SELECT count(*) FROM app.jobs WHERE retry_of_job_id=$1`, f.parent) != 0 {
			t.Fatal("future fixture must have only consistent independent chains, not a successor or malformed child of P")
		}
		var before, after string
		if err := admin.QueryRow(ctx, snapshotSQL).Scan(&before); err != nil {
			t.Fatal(err)
		}
		assertCurrentJobs(get(f, "already_current", 0, 2, 2), current...)
		post(f, http.StatusConflict, "already-current")
		if err := admin.QueryRow(ctx, snapshotSQL).Scan(&after); err != nil || before != after {
			t.Fatalf("healthy supersession GET/POST mutated history or state: %v", err)
		}
	})
	t.Run("newer failed raw input is not already current", func(t *testing.T) {
		f := seed(1)
		makeCurrent(f, f.workouts)
		synthetic(`UPDATE app.workout_coverage_states SET route_input_revision=2,route_input_sha256=decode(repeat('02',32),'hex'),
			processing_state='failed' WHERE account_id=$1`, f.account)
		synthetic(`UPDATE app.coverage_route_job_contexts SET route_input_revision=2,route_input_sha256=decode(repeat('02',32),'hex')
			WHERE account_id=$1 AND job_id<>ALL($2::uuid[])`, f.account, f.children)
		synthetic(`UPDATE app.jobs SET status='failed' WHERE id IN (SELECT target_job_id FROM app.workout_coverage_states WHERE account_id=$1)`, f.account)
		assertCurrentJobs(get(f, "no_eligible_routes", 0, 2, 0))
		post(f, http.StatusConflict, "no-eligible-routes")
	})
	t.Run("newer matcher and policy contract is authoritative", func(t *testing.T) {
		f := seed(1)
		current := makeCurrent(f, f.workouts)
		var original string
		if err := admin.QueryRow(ctx, `SELECT matcher_contract_version FROM app.coverage_reconciliation_state WHERE singleton`).Scan(&original); err != nil {
			t.Fatal(err)
		}
		synthetic(`UPDATE app.coverage_reconciliation_state SET matcher_contract_version='coverage-production-v2' WHERE singleton`)
		defer synthetic(`UPDATE app.coverage_reconciliation_state SET matcher_contract_version=$1 WHERE singleton`, original)
		synthetic(`UPDATE app.coverage_job_contexts SET matcher_contract_version='coverage-production-v2',path_policy_version='coverage-path-policy-experimental-v83'
			WHERE account_id=$1 AND job_id<>$2`, f.account, f.parent)
		synthetic(`UPDATE app.workout_coverage_states SET applied_path_policy_version='coverage-path-policy-experimental-v83' WHERE account_id=$1`, f.account)
		assertCurrentJobs(get(f, "already_current", 0, 2, 2), current...)
		post(f, http.StatusConflict, "already-current")
	})
	for _, status := range []string{"running", "partially_succeeded"} {
		t.Run("successful current route in parent "+status, func(t *testing.T) {
			f := seed(1)
			current := makeCurrent(f, f.workouts)
			tx, err := admin.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			exec(tx, `SET LOCAL session_replication_role='replica'`)
			// Model an independent normal parent with a successful current route
			// and another failed/unfinished route, not an appended retry child.
			exec(tx, `UPDATE app.jobs SET retry_of_job_id=NULL WHERE id=$1 OR parent_job_id=$1`, current[0])
			extra := uuid.New()
			exec(tx, `INSERT INTO app.jobs(id,account_id,parent_job_id,kind,priority,status,terminal_at)
				VALUES($1,$2,$3,'coverage_update_route',20,CASE WHEN $4='running' THEN 'queued' ELSE 'failed' END,
				CASE WHEN $4='running' THEN NULL ELSE now() END)`, extra, f.account, current[0], status)
			exec(tx, `INSERT INTO app.coverage_route_job_contexts(job_id,account_id,workout_id,route_input_revision,route_input_sha256,target_generations)
				SELECT $1,account_id,workout_id,route_input_revision,route_input_sha256,target_generations FROM app.coverage_route_job_contexts WHERE job_id=$2`, extra, f.children[0])
			exec(tx, `UPDATE app.jobs SET status=$2,terminal_at=CASE WHEN $2='running' THEN NULL ELSE now() END WHERE id=$1`, current[0], status)
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			assertCurrentJobs(get(f, "already_current", 0, 2, 2), current...)
			post(f, http.StatusConflict, "already-current")
		})
	}
	for name, mutation := range map[string]string{
		"missing pointer": `UPDATE app.workout_coverage_states SET target_job_id=NULL WHERE account_id=$1`,
		"wrong workout": `UPDATE app.coverage_route_job_contexts SET workout_id=CASE WHEN workout_id=$2 THEN $3 ELSE $2 END
			WHERE job_id IN (SELECT target_job_id FROM app.workout_coverage_states WHERE account_id=$1)`,
		"child not succeeded":           `UPDATE app.jobs SET status='failed' WHERE id IN (SELECT target_job_id FROM app.workout_coverage_states WHERE account_id=$1)`,
		"missing current child context": `DELETE FROM app.coverage_route_job_contexts WHERE job_id IN (SELECT target_job_id FROM app.workout_coverage_states WHERE account_id=$1)`,
		"missing current parent context": `DELETE FROM app.coverage_job_contexts WHERE job_id IN
			(SELECT child.parent_job_id FROM app.jobs child JOIN app.workout_coverage_states state ON state.target_job_id=child.id WHERE state.account_id=$1)`,
		"obsolete current matcher":  `UPDATE app.coverage_job_contexts SET matcher_contract_version='coverage-production-v99' WHERE account_id=$1`,
		"wrong current policy":      `UPDATE app.coverage_job_contexts SET path_policy_version='coverage-path-policy-experimental-v83' WHERE account_id=$1`,
		"wrong applied rules":       `UPDATE app.workout_coverage_states SET applied_rules_version='coverage-mismatch' WHERE account_id=$1`,
		"wrong applied sampling":    `UPDATE app.workout_coverage_states SET applied_sampling_version='coverage-mismatch' WHERE account_id=$1`,
		"wrong current revision":    `UPDATE app.coverage_route_job_contexts SET route_input_revision=3 WHERE job_id IN (SELECT target_job_id FROM app.workout_coverage_states WHERE account_id=$1)`,
		"wrong current digest":      `UPDATE app.coverage_route_job_contexts SET route_input_sha256=decode(repeat('02',32),'hex') WHERE job_id IN (SELECT target_job_id FROM app.workout_coverage_states WHERE account_id=$1)`,
		"wrong current generations": `UPDATE app.coverage_route_job_contexts SET target_generations='[{"regionId":"geofabrik:test","generation":2}]' WHERE job_id IN (SELECT target_job_id FROM app.workout_coverage_states WHERE account_id=$1)`,
		"foreign parent": `UPDATE app.jobs SET account_id=$4 WHERE id IN
			(SELECT child.parent_job_id FROM app.jobs child JOIN app.workout_coverage_states state ON state.target_job_id=child.id WHERE state.account_id=$1)`,
		"foreign child":   `UPDATE app.jobs SET account_id=$4 WHERE id IN (SELECT target_job_id FROM app.workout_coverage_states WHERE account_id=$1)`,
		"foreign context": `UPDATE app.coverage_route_job_contexts SET account_id=$4 WHERE job_id IN (SELECT target_job_id FROM app.workout_coverage_states WHERE account_id=$1)`,
		"foreign parent context": `UPDATE app.coverage_job_contexts SET account_id=$4 WHERE job_id IN
			(SELECT child.parent_job_id FROM app.jobs child JOIN app.workout_coverage_states state ON state.target_job_id=child.id WHERE state.account_id=$1)`,
	} {
		t.Run("current state without navigable proof/"+name, func(t *testing.T) {
			f := seed(1)
			current := makeCurrent(f, f.workouts)
			_, foreignAccount := insertSourceTestUser(t, admin)
			// Numbered placeholders are kept consistent across fault variants.
			mutation = `WITH args AS (SELECT $1::uuid,$2::uuid,$3::uuid,$4::uuid) ` + mutation
			synthetic(mutation, f.account, f.workouts[0], f.workouts[1], foreignAccount)
			e := get(f, "no_eligible_routes", 0, 2, 0)
			assertCurrentJobs(e)
			for _, id := range current {
				if slices.Contains(e.CurrentCoverageJobIds, compactUUID(id)) {
					t.Fatal("unproven or foreign current parent leaked")
				}
			}
		})
	}
	t.Run("current links bounded at 100 with deterministic ID tie break", func(t *testing.T) {
		f := seed(1)
		tx, err := admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		exec(tx, `SET LOCAL session_replication_role='replica'`)
		exec(tx, `CREATE TEMP TABLE synthetic_current_links ON COMMIT DROP AS SELECT
			gen_random_uuid() workout,gen_random_uuid() historical_child,gen_random_uuid() current_child,gen_random_uuid() current_parent
			FROM generate_series(1,101)`)
		exec(tx, `UPDATE app.jobs SET status='succeeded' WHERE parent_job_id=$1`, f.parent)
		exec(tx, `INSERT INTO app.workouts(id,account_id,source_id,source_file_id,workout_type_id,provider_id,content_sha256,provider_label,started_at,ended_at,provider_duration)
			SELECT link.workout,original.account_id,original.source_id,source_file_id,workout_type_id,link.workout::text,
			content_sha256,provider_label,started_at,ended_at,provider_duration FROM synthetic_current_links link
			CROSS JOIN app.workouts original WHERE original.id=$1`, f.workouts[0])
		exec(tx, `INSERT INTO app.jobs(id,account_id,kind,priority,status,terminal_at)
			SELECT current_parent,$1,'coverage_update',20,'succeeded',now() FROM synthetic_current_links`, f.account)
		exec(tx, `INSERT INTO app.coverage_job_contexts(job_id,account_id,region_id,target_osm_generation,target_work_revision,
			rules_version,sampling_version,path_policy_version,minimum_traversal_meters,matcher_contract_version)
			SELECT link.current_parent,context.account_id,region_id,target_osm_generation,target_work_revision,rules_version,sampling_version,
			path_policy_version,minimum_traversal_meters,matcher_contract_version
			FROM synthetic_current_links link CROSS JOIN app.coverage_job_contexts context WHERE context.job_id=$1`, f.parent)
		exec(tx, `INSERT INTO app.jobs(id,account_id,parent_job_id,kind,priority,status,terminal_at)
			SELECT historical_child,$1::uuid,$2::uuid,'coverage_update_route',20,'failed',now() FROM synthetic_current_links
			UNION ALL SELECT current_child,$1,current_parent,'coverage_update_route',20,'succeeded',now() FROM synthetic_current_links`, f.account, f.parent)
		exec(tx, `INSERT INTO app.coverage_route_job_contexts(job_id,account_id,workout_id,route_input_revision,route_input_sha256,target_generations)
			SELECT target.id,$1,link.workout,original.route_input_revision,original.route_input_sha256,original.target_generations
			FROM synthetic_current_links link CROSS JOIN app.coverage_route_job_contexts original
			CROSS JOIN LATERAL unnest(ARRAY[link.historical_child,link.current_child]) target(id) WHERE original.job_id=$2`, f.account, f.children[0])
		exec(tx, `INSERT INTO app.workout_coverage_states(account_id,workout_id,route_input_revision,route_input_sha256,readiness_state,map_data_ready_at,
			processing_state,target_job_id,applied_route_input_revision,applied_route_input_sha256,applied_rules_version,applied_sampling_version,
			applied_path_policy_version,applied_generations)
			SELECT $1,link.workout,original.route_input_revision,original.route_input_sha256,'map_data_ready',now(),'current',link.current_child,
			original.route_input_revision,original.route_input_sha256,target_rules_version,target_sampling_version,target_path_policy_version,target_generations
			FROM synthetic_current_links link CROSS JOIN app.workout_coverage_states original WHERE original.workout_id=$2`, f.account, f.workouts[0])
		rows, err := tx.Query(ctx, `SELECT current_parent FROM synthetic_current_links ORDER BY current_parent DESC LIMIT 100`)
		if err != nil {
			t.Fatal(err)
		}
		want, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
		if err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		assertCurrentJobs(get(f, "already_current", 0, 101, 101), want...)
	})
	t.Run("zero unsuccessful is not already current", func(t *testing.T) {
		f := seed(1)
		synthetic(`UPDATE app.jobs SET status='succeeded' WHERE parent_job_id=$1`, f.parent)
		get(f, "no_eligible_routes", 0, 0, 0)
		post(f, http.StatusConflict, "no-eligible-routes")
	})
	t.Run("eligibility omitted from other job kinds and route detail", func(t *testing.T) {
		f := seed(1)
		ids := []uuid.UUID{f.children[0]}
		for _, kind := range []string{"manual_ingest", "workout_deletion"} {
			id := uuid.New()
			synthetic(`INSERT INTO app.jobs(id,account_id,kind,priority) VALUES($1,$2,$3,20)`, id, f.account, kind)
			ids = append(ids, id)
		}
		for _, id := range ids {
			response := routeJobRequest(handler, http.MethodGet, "/api/jobs/"+compactUUID(id), "", f.bearer, "", false)
			if response.recorder.Code != http.StatusOK || strings.Contains(response.recorder.Body.String(), "retryEligibility") {
				t.Fatalf("non-parent eligibility: %d %s", response.recorder.Code, response.recorder.Body.String())
			}
		}
		cookie := insertTestSession(t, apiDB, f.principal, "cookie", "synthetic-csrf")
		response := routeJobRequest(handler, http.MethodPost, "/api/jobs/"+compactUUID(f.parent)+"/retry", "{}", cookie, "", true)
		if response.recorder.Code != http.StatusForbidden || strings.Contains(response.recorder.Body.String(), compactUUID(f.children[0])) {
			t.Fatalf("CSRF denial: %d %s", response.recorder.Code, response.recorder.Body.String())
		}
	})
	t.Run("unknown SQL exception is redacted", func(t *testing.T) {
		f := seed(1)
		var original string
		if err := admin.QueryRow(ctx, `SELECT pg_get_functiondef('app.retry_coverage_update(uuid,uuid,integer)'::regprocedure)`).Scan(&original); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if _, err := admin.Exec(ctx, original); err != nil {
				t.Fatal(err)
			}
		}()
		if _, err := admin.Exec(ctx, `CREATE OR REPLACE FUNCTION app.retry_coverage_update(prior_job_id uuid,requester_id uuid,max_ordinal integer)
			RETURNS TABLE(job_id uuid,route_count integer) LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app AS $$
			BEGIN RAISE EXCEPTION 'private-sql-canary /secret/path' USING ERRCODE='55000'; END $$`); err != nil {
			t.Fatal(err)
		}
		post(f, http.StatusConflict, "unavailable")
	})
	t.Run("foreign invalid inactive and role ACL isolation", func(t *testing.T) {
		f, foreign := seed(1), seed(1)
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			path, body := "/api/jobs/"+compactUUID(f.parent), ""
			if method == http.MethodPost {
				path, body = path+"/retry", "{}"
			}
			response := routeJobRequest(handler, method, path, body, foreign.bearer, "", false)
			if response.recorder.Code != http.StatusNotFound || strings.Contains(response.recorder.Body.String(), compactUUID(f.children[0])) {
				t.Fatalf("foreign leak: %s", response.recorder.Body.String())
			}
		}
		for _, account := range []uuid.UUID{foreign.account, uuid.Nil} {
			tx := integrationAccountTransaction(t, apiDB, account)
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.read_coverage_retry_eligibility($1,100)`, f.parent).Scan(&n); err != nil || n != 0 {
				t.Fatalf("foreign helper rows=%d %v", n, err)
			}
			_ = tx.Rollback(ctx)
		}
		tx := integrationAccountTransaction(t, apiDB, f.account)
		for _, ordinal := range []any{nil, 0, 101} {
			var n int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.read_coverage_retry_eligibility($1,$2)`, f.parent, ordinal).Scan(&n); err != nil || n != 0 {
				t.Fatalf("invalid ordinal rows=%d %v", n, err)
			}
		}
		_ = tx.Rollback(ctx)
		synthetic(`UPDATE app.accounts SET state='deleting' WHERE id=$1`, f.account)
		tx = integrationAccountTransaction(t, apiDB, f.account)
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.read_coverage_retry_eligibility($1,100)`, f.parent).Scan(&n); err != nil || n != 0 {
			t.Fatalf("inactive account rows=%d %v", n, err)
		}
		_ = tx.Rollback(ctx)
		for _, role := range []string{"workouts_worker", "workouts_coverage_worker", "workouts_tiles"} {
			tx, err := admin.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			exec(tx, "SET LOCAL ROLE "+role)
			_, err = tx.Exec(ctx, `SELECT * FROM app.read_coverage_retry_eligibility($1,100)`, foreign.parent)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
				t.Fatalf("%s helper access: %v", role, err)
			}
			_ = tx.Rollback(ctx)
		}
		tx = integrationAccountTransaction(t, apiDB, foreign.account)
		_, err = tx.Exec(ctx, `SELECT * FROM app.coverage_retry_targets($1,$2)`, foreign.parent, foreign.account)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
			t.Fatalf("API private target access: %v", err)
		}
		_ = tx.Rollback(ctx)
	})
	t.Run("history projects before filtering counting and pagination without rewriting failed routes", func(t *testing.T) {
		f := seed(1)
		current := makeCurrent(f, f.workouts)
		const generations = `[{"regionId":"geofabrik:test","generation":2}]`
		synthetic(`UPDATE app.coverage_job_contexts SET target_osm_generation=2 WHERE account_id=$1 AND job_id<>$2`, f.account, f.parent)
		synthetic(`UPDATE app.coverage_route_job_contexts SET target_generations=$3 WHERE account_id=$1 AND job_id<>ALL($2::uuid[])`, f.account, f.children, generations)
		synthetic(`UPDATE app.workout_coverage_states SET applied_generations=$2,target_generations=$2 WHERE account_id=$1`, f.account, generations)
		tx, err := admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		exec(tx, `SET LOCAL session_replication_role='replica'`)
		exec(tx, `CREATE TEMP TABLE synthetic_superseded_parents ON COMMIT DROP AS
			SELECT gen_random_uuid() id,n FROM generate_series(1,24) n`)
		exec(tx, `UPDATE app.jobs SET created_at=now()+interval '1 day 25 seconds' WHERE id=$1`, f.parent)
		exec(tx, `INSERT INTO app.jobs(id,account_id,kind,priority,status,terminal_at,created_at)
			SELECT id,$1,'coverage_update',20,'failed',now(),now()+interval '1 day'+n*interval '1 second' FROM synthetic_superseded_parents`, f.account)
		exec(tx, `INSERT INTO app.coverage_job_contexts(job_id,account_id,region_id,target_osm_generation,target_work_revision,
			rules_version,sampling_version,path_policy_version,minimum_traversal_meters,matcher_contract_version)
			SELECT parent.id,context.account_id,region_id,target_osm_generation,target_work_revision,rules_version,sampling_version,
			path_policy_version,minimum_traversal_meters,matcher_contract_version FROM synthetic_superseded_parents parent
			CROSS JOIN app.coverage_job_contexts context WHERE context.job_id=$1`, f.parent)
		exec(tx, `CREATE TEMP TABLE synthetic_superseded_children ON COMMIT DROP AS SELECT gen_random_uuid() id,
			parent.id parent_id,context.job_id prior_child FROM synthetic_superseded_parents parent
			CROSS JOIN app.coverage_route_job_contexts context WHERE context.job_id=ANY($1::uuid[])`, f.children)
		exec(tx, `INSERT INTO app.jobs(id,account_id,parent_job_id,kind,priority,status,terminal_at,failure_code)
			SELECT id,$1,parent_id,'coverage_update_route',20,'failed',now(),'coverage-route-timeout' FROM synthetic_superseded_children`, f.account)
		exec(tx, `INSERT INTO app.coverage_route_job_contexts(job_id,account_id,workout_id,route_input_revision,route_input_sha256,target_generations)
			SELECT child.id,context.account_id,workout_id,route_input_revision,route_input_sha256,target_generations
			FROM synthetic_superseded_children child JOIN app.coverage_route_job_contexts context ON context.job_id=child.prior_child`)
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		var before, after string
		if err := admin.QueryRow(ctx, snapshotSQL).Scan(&before); err != nil {
			t.Fatal(err)
		}
		all := list(f, "")
		checkPage(all, 27, 1, 25, 25)
		wantIDs := make([]generated.CompactUUID, 0, 25)
		for _, item := range all.Items {
			if item.Status != generated.JobStatusFailed || item.DisplayStatus == nil || *item.DisplayStatus != generated.JobDisplayStatusSuperseded {
				t.Fatalf("history did not preserve failed execution while projecting superseded: %+v", item)
			}
			wantIDs = append(wantIDs, item.Id)
		}
		checkPage(list(f, "?status=failed"), 0, 1, 25, 0)
		checkPage(list(f, "?status=partially_succeeded"), 0, 1, 25, 0)
		checkPage(list(f, "?status=superseded"), 25, 1, 25, 25)
		for page := 1; page <= 4; page++ {
			result := list(f, fmt.Sprintf("?status=superseded&operation=coverage_update&pageSize=10&page=%d", page))
			start := min((page-1)*10, 25)
			end := min(start+10, 25)
			checkPage(result, 25, page, 10, end-start)
			for index, item := range result.Items {
				if item.Id != wantIDs[start+index] || item.DisplayStatus == nil || *item.DisplayStatus != generated.JobDisplayStatusSuperseded {
					t.Fatalf("filtered page reordered or dropped projected rows: %+v", item)
				}
			}
		}
		checkPage(list(f, "?status=superseded&operation=manual_sync"), 0, 1, 25, 0)
		succeeded := list(f, "?status=succeeded")
		checkPage(succeeded, 2, 1, 25, 2)
		for _, item := range succeeded.Items {
			if item.Status != generated.JobStatusSucceeded || item.DisplayStatus != nil {
				t.Fatalf("succeeded parent incorrectly projected: %+v", item)
			}
		}
		assertCurrentJobs(get(f, "already_current", 0, 2, 2), current...)
		post(f, http.StatusConflict, "already-current")
		if err := admin.QueryRow(ctx, snapshotSQL).Scan(&after); err != nil || before != after {
			t.Fatalf("projection/filtering changed persisted history, results or retry policy: %v", err)
		}
		if count(`SELECT count(*) FROM app.jobs WHERE account_id=$1 AND parent_job_id IS NOT NULL AND status='failed'`, f.account) != 52 {
			t.Fatal("historical route attempts must remain failed")
		}
		synthetic(`UPDATE app.jobs SET status='partially_succeeded' WHERE id=$1`, f.parent)
		get(f, "already_current", 0, 2, 2)
		checkPage(list(f, "?status=partially_succeeded"), 0, 1, 25, 0)
		checkPage(list(f, "?status=superseded"), 25, 1, 25, 25)
		synthetic(`UPDATE app.jobs SET status='cancelled' WHERE id=$1`, f.parent)
		get(f, "already_current", 0, 2, 2)
		cancelled := list(f, "?status=cancelled")
		checkPage(cancelled, 1, 1, 25, 1)
		if cancelled.Items[0].Status != generated.JobStatusCancelled || cancelled.Items[0].DisplayStatus != nil {
			t.Fatal("cancelled parent must remain cancelled")
		}
		checkPage(list(f, "?status=superseded"), 24, 1, 25, 24)
		synthetic(`UPDATE app.jobs SET status='succeeded' WHERE id=$1`, f.parent)
		get(f, "not_terminal", 0, 2, 2)
		checkPage(list(f, "?status=succeeded"), 3, 1, 25, 3)
		if count(`SELECT count(*) FROM app.jobs WHERE account_id=$1 AND status='superseded'`, f.account) != 0 {
			t.Fatal("superseded must never be persisted")
		}
		readOnly, err := apiDB.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
		if err != nil {
			t.Fatal(err)
		}
		defer readOnly.Rollback(ctx)
		exec(readOnly, `SELECT set_config('app.account_id',$1,true)`, f.account.String())
		var total int64
		if err := readOnly.QueryRow(ctx, `SELECT total_items FROM app.read_job_history('superseded','',25,1000,100)`).Scan(&total); err != nil || total != 24 {
			t.Fatalf("read-only out-of-range count=%d err=%v", total, err)
		}
	})
	t.Run("history preserves admission operation and latest-only filters", func(t *testing.T) {
		f := seed(1)
		manual, retry, scheduled, deletion := uuid.New(), uuid.New(), uuid.New(), uuid.New()
		synthetic(`INSERT INTO app.jobs(id,account_id,kind,priority,status,terminal_at,retry_of_job_id)
			VALUES($1,$5,'manual_ingest',20,'failed',now(),NULL),($2,$5,'manual_ingest',20,'failed',now(),$1),
			($3,$5,'scheduled_ingest',20,'queued',NULL,NULL),($4,$5,'workout_deletion',20,'failed',now(),NULL)`, manual, retry, scheduled, deletion, f.account)
		synthetic(`UPDATE app.jobs SET status='running',terminal_at=NULL WHERE id=$1`, f.parent)
		queued := list(f, "?status=queued&operation=coverage_update")
		checkPage(queued, 1, 1, 25, 1)
		if queued.Items[0].Status != generated.JobStatusQueued || queued.Items[0].DisplayStatus != nil {
			t.Fatal("coverage parent awaiting admission must remain publicly queued")
		}
		checkPage(list(f, "?status=running&operation=coverage_update"), 0, 1, 25, 0)
		manualHistory := list(f, "?status=failed&operation=manual_sync")
		checkPage(manualHistory, 1, 1, 25, 1)
		if manualHistory.Items[0].Id != compactUUID(retry) || manualHistory.Items[0].DisplayStatus != nil {
			t.Fatal("history must expose only the latest manual attempt and preserve its raw failed display")
		}
		checkPage(list(f, "?status=failed"), 2, 1, 25, 2)
		checkPage(list(f, "?status=queued&operation=automated_sync"), 1, 1, 25, 1)
		checkPage(list(f, "?status=failed&operation=workout_deletion"), 1, 1, 25, 1)
		checkPage(list(f, "?status=superseded"), 0, 1, 25, 0)
		synthetic(`UPDATE app.jobs SET status='running',terminal_at=NULL,worker_id='synthetic-history-worker',lease_token=gen_random_uuid(),
			claimed_at=now(),heartbeat_at=now(),lease_expires_at=now()+interval '2 minutes',started_at=now() WHERE id=$1`, f.children[0])
		admitted := list(f, "?status=running&operation=coverage_update")
		checkPage(admitted, 1, 1, 25, 1)
		if admitted.Items[0].Status != generated.JobStatusRunning || admitted.Items[0].DisplayStatus != nil {
			t.Fatal("admitted coverage must remain running rather than superseded")
		}
		checkPage(list(f, "?status=queued&operation=coverage_update"), 0, 1, 25, 0)
	})
	t.Run("history does not supersede mixed partial or missing foreign or unfinished coverage", func(t *testing.T) {
		f := seed(1)
		makeCurrent(f, f.workouts[:1])
		synthetic(`UPDATE app.jobs SET status='partially_succeeded' WHERE id=$1`, f.parent)
		get(f, "eligible", 1, 2, 1)
		partial := list(f, "?status=partially_succeeded")
		checkPage(partial, 1, 1, 25, 1)
		if partial.Items[0].DisplayStatus != nil || partial.Items[0].Status != generated.JobStatusPartiallySucceeded {
			t.Fatal("mixed partial coverage must retain execution presentation")
		}
		checkPage(list(f, "?status=superseded"), 0, 1, 25, 0)
		foreign := seed(1)
		checkPage(list(foreign, "?status=superseded"), 0, 1, 25, 0)
		for _, state := range []string{"queued", "running", "failed"} {
			synthetic(`UPDATE app.workout_coverage_states SET processing_state=$2 WHERE account_id=$1`, f.account, state)
			checkPage(list(f, "?status=superseded"), 0, 1, 25, 0)
			checkPage(list(f, "?status=partially_succeeded"), 1, 1, 25, 1)
		}
		makeCurrent(f, f.workouts)
		synthetic(`DELETE FROM app.coverage_route_job_contexts WHERE job_id IN
			(SELECT target_job_id FROM app.workout_coverage_states WHERE account_id=$1)`, f.account)
		get(f, "no_eligible_routes", 0, 2, 0)
		checkPage(list(f, "?status=superseded"), 0, 1, 25, 0)
		checkPage(list(f, "?status=partially_succeeded"), 1, 1, 25, 1)
		for _, role := range []string{"workouts_worker", "workouts_coverage_worker", "workouts_tiles"} {
			denied, err := admin.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			exec(denied, "SET LOCAL ROLE "+role)
			_, err = denied.Exec(ctx, `SELECT * FROM app.read_job_history('','',25,0,100)`)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
				t.Fatalf("%s history access: %v", role, err)
			}
			_ = denied.Rollback(ctx)
		}
	})
	t.Run("history proof calls are page bounded or restricted to the derived status family", func(t *testing.T) {
		f := seed(1)
		makeCurrent(f, f.workouts[:1])
		tx, err := admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		exec(tx, `SET LOCAL session_replication_role='replica'`)
		// Together with P and its current succeeded replacement, these give
		// exactly 75 latest failed and 25 succeeded parents; 20 failures resolve.
		exec(tx, `CREATE TEMP TABLE synthetic_history_cost_jobs ON COMMIT DROP AS SELECT gen_random_uuid() id,n,n<=20 resolved,
			CASE WHEN n<=74 THEN 'failed' ELSE 'succeeded' END status FROM generate_series(1,98) n`)
		exec(tx, `INSERT INTO app.jobs(id,account_id,kind,priority,status,terminal_at,created_at)
			SELECT id,$1,'coverage_update',20,status,now(),now()+CASE WHEN resolved THEN interval '2 days'
			WHEN status='failed' THEN interval '1 day' ELSE interval '-1 day' END+n*interval '1 second' FROM synthetic_history_cost_jobs`, f.account)
		exec(tx, `INSERT INTO app.coverage_job_contexts(job_id,account_id,region_id,target_osm_generation,target_work_revision,
			rules_version,sampling_version,path_policy_version,minimum_traversal_meters,matcher_contract_version)
			SELECT parent.id,context.account_id,region_id,target_osm_generation,target_work_revision,rules_version,sampling_version,
			path_policy_version,minimum_traversal_meters,matcher_contract_version FROM synthetic_history_cost_jobs parent
			CROSS JOIN app.coverage_job_contexts context WHERE context.job_id=$1`, f.parent)
		exec(tx, `CREATE TEMP TABLE synthetic_history_cost_children ON COMMIT DROP AS SELECT gen_random_uuid() id,parent.id parent_id,
			CASE WHEN resolved THEN $1::uuid ELSE $2::uuid END prior_child FROM synthetic_history_cost_jobs parent WHERE status='failed'`, f.children[0], f.children[1])
		exec(tx, `INSERT INTO app.jobs(id,account_id,parent_job_id,kind,priority,status,terminal_at)
			SELECT id,$1,parent_id,'coverage_update_route',20,'failed',now() FROM synthetic_history_cost_children`, f.account)
		exec(tx, `INSERT INTO app.coverage_route_job_contexts(job_id,account_id,workout_id,route_input_revision,route_input_sha256,target_generations)
			SELECT child.id,context.account_id,workout_id,route_input_revision,route_input_sha256,target_generations
			FROM synthetic_history_cost_children child JOIN app.coverage_route_job_contexts context ON context.job_id=child.prior_child`)
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}

		var original string
		if err := admin.QueryRow(ctx, `SELECT pg_get_functiondef('app.read_coverage_retry_eligibility(uuid,integer)'::regprocedure)`).Scan(&original); err != nil {
			t.Fatal(err)
		}
		clone := strings.Replace(original, "app.read_coverage_retry_eligibility(", "app.retry_eligibility_test_original(", 1)
		if clone == original {
			t.Fatal("cannot instrument the original proof function")
		}
		defer func() {
			if _, err := admin.Exec(ctx, original); err != nil {
				t.Error(err)
			}
			if _, err := admin.Exec(ctx, `DROP FUNCTION IF EXISTS app.retry_eligibility_test_original(uuid,integer)`); err != nil {
				t.Error(err)
			}
			if _, err := admin.Exec(ctx, `DROP SEQUENCE IF EXISTS app.retry_eligibility_test_calls`); err != nil {
				t.Error(err)
			}
		}()
		instrument, err := admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer instrument.Rollback(ctx)
		exec(instrument, clone)
		exec(instrument, `REVOKE ALL ON FUNCTION app.retry_eligibility_test_original(uuid,integer)
			FROM PUBLIC,workouts_api,workouts_worker,workouts_coverage_worker,workouts_tiles,workouts_migration`)
		exec(instrument, `GRANT CREATE ON SCHEMA app TO workouts_security_owner`)
		exec(instrument, `ALTER FUNCTION app.retry_eligibility_test_original(uuid,integer) OWNER TO workouts_security_owner`)
		exec(instrument, `REVOKE CREATE ON SCHEMA app FROM workouts_security_owner`)
		exec(instrument, `CREATE SEQUENCE app.retry_eligibility_test_calls`)
		exec(instrument, `GRANT USAGE ON SEQUENCE app.retry_eligibility_test_calls TO workouts_security_owner`)
		// Scratch-only instrumentation. The source helper stays SELECT-only;
		// its unchanged body executes under its original owner via the clone.
		exec(instrument, `CREATE OR REPLACE FUNCTION app.read_coverage_retry_eligibility(prior_job_id uuid,max_ordinal integer)
			RETURNS TABLE(can_retry boolean,reason text,eligible_route_count bigint,unsuccessful_route_count bigint,current_route_count bigint,current_coverage_job_ids uuid[])
			LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,app AS $$
			BEGIN
			    PERFORM pg_catalog.nextval('app.retry_eligibility_test_calls'::regclass);
			    RETURN QUERY SELECT * FROM app.retry_eligibility_test_original(prior_job_id,max_ordinal);
			END $$`)
		if err := instrument.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		reset := func() {
			t.Helper()
			if _, err := admin.Exec(ctx, `SELECT setval('app.retry_eligibility_test_calls',1,false)`); err != nil {
				t.Fatal(err)
			}
		}
		calls := func(want int64) {
			t.Helper()
			var value int64
			var called bool
			if err := admin.QueryRow(ctx, `SELECT last_value,is_called FROM app.retry_eligibility_test_calls`).Scan(&value, &called); err != nil {
				t.Fatal(err)
			}
			if !called {
				value = 0
			}
			if value != want {
				t.Fatalf("eligibility calls=%d, want %d", value, want)
			}
		}
		measure := func(query string, wantCalls, total int64, page, items int) generated.JobList {
			t.Helper()
			reset()
			result := list(f, query)
			checkPage(result, total, page, 25, items)
			calls(wantCalls)
			t.Logf("history %q: proofCalls=%d total=%d items=%d", query, wantCalls, total, items)
			return result
		}
		unfiltered := measure("", 25, 100, 1, 25)
		resolved := 0
		for _, item := range unfiltered.Items {
			if item.DisplayStatus != nil && *item.DisplayStatus == generated.JobDisplayStatusSuperseded {
				resolved++
			}
		}
		if resolved != 20 {
			t.Fatalf("page-only proof lost resolved displays: %d", resolved)
		}
		measure("?status=succeeded", 0, 25, 1, 25)
		measure("?page=999", 0, 100, 999, 0)
		measure("?status=succeeded&page=999", 0, 25, 999, 0)
		measure("?status=queued", 0, 0, 1, 0)
		measure("?status=running", 0, 0, 1, 0)
		measure("?status=cancelled", 0, 0, 1, 0)
		measure("?operation=manual_sync", 0, 0, 1, 0)
		measure("?status=failed", 75, 55, 1, 25)
		measure("?status=superseded", 75, 20, 1, 20)
		measure("?status=partially_succeeded", 0, 0, 1, 0)
		reset()
		nullFilter := integrationAccountTransaction(t, apiDB, f.account)
		var n, total int64
		if err := nullFilter.QueryRow(ctx, `SELECT count(job_id),max(total_items) FROM app.read_job_history(NULL,'',25,0,100)`).Scan(&n, &total); err != nil || n != 25 || total != 100 {
			t.Fatalf("NULL filter page=%d total=%d err=%v", n, total, err)
		}
		_ = nullFilter.Rollback(ctx)
		calls(25)

		partial, err := admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer partial.Rollback(ctx)
		exec(partial, `SET LOCAL session_replication_role='replica'`)
		exec(partial, `CREATE TEMP TABLE synthetic_history_cost_partials ON COMMIT DROP AS
			SELECT gen_random_uuid() id,gen_random_uuid() child,n,n<=6 resolved FROM generate_series(1,12) n`)
		exec(partial, `INSERT INTO app.jobs(id,account_id,kind,priority,status,terminal_at,created_at)
			SELECT id,$1,'coverage_update',20,'partially_succeeded',now(),now()+interval '3 days'+n*interval '1 second' FROM synthetic_history_cost_partials`, f.account)
		exec(partial, `INSERT INTO app.coverage_job_contexts(job_id,account_id,region_id,target_osm_generation,target_work_revision,
			rules_version,sampling_version,path_policy_version,minimum_traversal_meters,matcher_contract_version)
			SELECT parent.id,context.account_id,region_id,target_osm_generation,target_work_revision,rules_version,sampling_version,
			path_policy_version,minimum_traversal_meters,matcher_contract_version FROM synthetic_history_cost_partials parent
			CROSS JOIN app.coverage_job_contexts context WHERE context.job_id=$1`, f.parent)
		exec(partial, `INSERT INTO app.jobs(id,account_id,parent_job_id,kind,priority,status,terminal_at)
			SELECT child,$1,id,'coverage_update_route',20,'failed',now() FROM synthetic_history_cost_partials`, f.account)
		exec(partial, `INSERT INTO app.coverage_route_job_contexts(job_id,account_id,workout_id,route_input_revision,route_input_sha256,target_generations)
			SELECT parent.child,context.account_id,workout_id,route_input_revision,route_input_sha256,target_generations
			FROM synthetic_history_cost_partials parent JOIN app.coverage_route_job_contexts context
			ON context.job_id=CASE WHEN parent.resolved THEN $1::uuid ELSE $2::uuid END`, f.children[0], f.children[1])
		if err := partial.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		measure("", 25, 112, 1, 25)
		measure("?status=succeeded", 0, 25, 1, 25)
		measure("?status=failed", 75, 55, 1, 25)
		measure("?status=partially_succeeded", 12, 6, 1, 6)
		measure("?status=superseded", 87, 26, 1, 25)
		measure("?page=999", 0, 112, 999, 0)
	})
	t.Run("region scope uses all child targets and stays account scoped", func(t *testing.T) {
		f, foreign := seed(1), seed(1)
		synthetic(`UPDATE app.coverage_route_job_contexts SET target_generations=
			'[ {"regionId":"geofabrik:private-foreign-canary","generation":1} ]' WHERE job_id=ANY($1::uuid[])`, foreign.children)
		read := func(parent uuid.UUID, want []string) generated.JobDetail {
			t.Helper()
			response := routeJobRequest(handler, http.MethodGet, "/api/jobs/"+compactUUID(parent), "", f.bearer, "", false)
			if response.recorder.Code != http.StatusOK {
				t.Fatalf("scope detail: %d %s", response.recorder.Code, response.recorder.Body.String())
			}
			validateRecordedResponse(t, http.MethodGet, "/api/jobs/"+compactUUID(parent), response.recorder)
			var detail generated.JobDetail
			if err := json.Unmarshal(response.recorder.Body.Bytes(), &detail); err != nil {
				t.Fatal(err)
			}
			if detail.Coverage == nil || detail.Coverage.RegionIds == nil || !slices.Equal(*detail.Coverage.RegionIds, want) {
				t.Fatalf("scope=%+v, want %v", detail.Coverage, want)
			}
			if strings.Contains(response.recorder.Body.String(), "private-foreign-canary") {
				t.Fatal("region scope exposed a different account's target")
			}
			return detail
		}
		read(f.parent, []string{"geofabrik:test"})
		// A queued child without any outcome contributes scope just like a
		// failed child. The anchor remains unchanged, not a fabricated scope.
		synthetic(`UPDATE app.coverage_route_job_contexts SET target_generations=
			'[{"regionId":"geofabrik:south","generation":9},{"regionId":"geofabrik:north","generation":2}]' WHERE job_id=$1`, f.children[0])
		synthetic(`UPDATE app.coverage_route_job_contexts SET target_generations=
			'[{"regionId":"geofabrik:north","generation":7}]' WHERE job_id=$1`, f.children[1])
		synthetic(`UPDATE app.jobs SET status='queued',terminal_at=NULL WHERE id=$1`, f.children[1])
		mixed := read(f.parent, []string{"geofabrik:north", "geofabrik:south"})
		if mixed.Coverage.RegionId != "geofabrik:test" || mixed.Coverage.TargetOsmGeneration != 1 {
			t.Fatal("scope changed the representative anchor")
		}
		synthetic(`UPDATE app.workouts SET deletion_requested_at=now(),deletion_target_id=gen_random_uuid() WHERE id=$1`, f.workouts[0])
		hidden := read(f.parent, []string{"geofabrik:north", "geofabrik:south"})
		for _, child := range hidden.Children {
			if child.Id == compactUUID(f.children[0]) && child.CoverageRoute != nil {
				t.Fatal("deletion-pending workout unexpectedly exposed route metadata")
			}
		}
		synthetic(`UPDATE app.workouts SET deletion_requested_at=NULL,deletion_target_id=NULL WHERE id=$1`, f.workouts[0])
		// Restore retryable authority, then check the actual constructor's
		// successor rather than inventing a retry context in the fixture.
		synthetic(`UPDATE app.jobs SET status='failed',terminal_at=now() WHERE id=$1`, f.children[1])
		synthetic(`UPDATE app.workout_coverage_states state SET target_generations=context.target_generations
			FROM app.coverage_route_job_contexts context WHERE context.workout_id=state.workout_id
			AND context.account_id=state.account_id AND context.job_id=ANY($1::uuid[])`, f.children)
		post(f, http.StatusAccepted, "")
		var retry uuid.UUID
		if err := admin.QueryRow(ctx, `SELECT id FROM app.jobs WHERE retry_of_job_id=$1 AND kind='coverage_update'`, f.parent).Scan(&retry); err != nil {
			t.Fatal(err)
		}
		read(retry, []string{"geofabrik:north", "geofabrik:south"})
		response := routeJobRequest(handler, http.MethodGet, "/api/jobs/"+compactUUID(foreign.parent), "", f.bearer, "", false)
		if response.recorder.Code != http.StatusNotFound || strings.Contains(response.recorder.Body.String(), "private-foreign-canary") {
			t.Fatal("foreign coverage scope was readable")
		}
	})
	for _, role := range []string{"workouts_api", "workouts_worker", "workouts_coverage_worker"} {
		t.Run(fmt.Sprintf("readiness %s", role), func(t *testing.T) {
			roleConfig := config.Copy()
			roleConfig.ConnConfig.RuntimeParams["role"] = role
			pool, err := pgxpool.NewWithConfig(ctx, roleConfig)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			if !database.Ready(ctx, pool) {
				t.Fatal("current runtime is not ready")
			}
		})
	}
}
