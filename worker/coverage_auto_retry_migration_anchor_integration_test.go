package worker

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/migrations"
	"github.com/erhhung/workouts-explorer/internal/testutil/migrationfixture"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const coverageEnqueueSignature = "app.enqueue_coverage_update(uuid,uuid,text,bigint,bigint,text,text,text,double precision,jsonb)"
const coverageEnqueueAnchor = "AND job.status = 'queued' ORDER BY job.created_at"
const coverageMinimumAnchor = "AND parent_context.minimum_traversal_meters = minimum_traversal_meters_value"
const coverageSelectionAnchor = "SELECT item->> 'regionId', (item->> 'generation')::bigint INTO anchor_region, anchor_generation"

type coverageAnchorQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Exclude only the bodies when checking a successful rewrite. Retain every
// pg_proc attribute, including OID, owner, ACL, search_path and execution flags.
func coverageAnchorContract(t *testing.T, ctx context.Context, q coverageAnchorQueryer, bodies bool) string {
	t.Helper()
	var contract string
	if err := q.QueryRow(ctx, `SELECT jsonb_agg(CASE WHEN $3 THEN to_jsonb(proc) ELSE to_jsonb(proc)-'prosrc'-'prosqlbody' END ORDER BY proc.oid)::text
		FROM pg_proc proc WHERE proc.oid IN ($1::regprocedure,$2::regprocedure)`, coverageEnqueueSignature, coverageReconciliationApplySignature, bodies).Scan(&contract); err != nil {
		t.Fatal(err)
	}
	return contract
}

func testCoverageRetryMigrationRollback(t *testing.T, ctx context.Context, admin *pgxpool.Pool, sqlDB *sql.DB, f *coverageAutoRetryFixture) {
	t.Helper()
	var enqueue, reconcile, schemaACL string
	if err := admin.QueryRow(ctx, `SELECT pg_get_functiondef($1::regprocedure),pg_get_functiondef($2::regprocedure),
		COALESCE((SELECT nspacl::text FROM pg_namespace WHERE nspname='app'),'')`, coverageEnqueueSignature, coverageReconciliationApplySignature).Scan(&enqueue, &reconcile, &schemaACL); err != nil {
		t.Fatal(err)
	}
	for _, anchor := range []string{coverageEnqueueAnchor, coverageMinimumAnchor, coverageSelectionAnchor} {
		if strings.Count(enqueue+reconcile, anchor) != 1 {
			t.Fatalf("route-counter checkpoint must contain one canonical anchor: %s", anchor)
		}
	}
	raw, err := migrationfixture.PhaseSQL(migrations.Files, migrationfixture.AutoRetries)
	if err != nil {
		t.Fatal(err)
	}
	up := strings.Split(string(raw), "-- +goose Down")[0]
	variants := []struct{ name, enqueue, minimum, selection string }{
		{"formatted", coverageEnqueueAnchor, coverageMinimumAnchor, coverageSelectionAnchor},
		{"compact", "AND job.status='queued' ORDER BY job.created_at", "AND parent_context.minimum_traversal_meters=minimum_traversal_meters_value", "SELECT item->>'regionId',(item->>'generation')::bigint INTO anchor_region,anchor_generation"},
		{"mixed case", "and JOB.STATUS = 'queued' order by JOB.CREATED_AT", "and PARENT_CONTEXT . MINIMUM_TRAVERSAL_METERS = MINIMUM_TRAVERSAL_METERS_VALUE", "select ITEM ->> 'regionId', ( ITEM ->> 'generation' ) :: BIGINT into ANCHOR_REGION, ANCHOR_GENERATION"},
		{"newlines", "aNd\nJOB \t. status\n=\t'queued'\nORDER\nbY\tJOB . created_at", "AND\nparent_context\n.\nminimum_traversal_meters\n=\nminimum_traversal_meters_value", "SELECT\n item\n ->>\n 'regionId'\n,\n(\n item ->> 'generation'\n)\n::\n BIGINT\nINTO\n anchor_region\n,\n anchor_generation"},
	}
	for _, variant := range variants {
		t.Run("automatic retry SQL anchor variant/"+variant.name, func(t *testing.T) {
			tx, err := admin.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			for _, definition := range []string{
				strings.Replace(enqueue, coverageEnqueueAnchor, variant.enqueue, 1),
				strings.NewReplacer(coverageMinimumAnchor, variant.minimum, coverageSelectionAnchor, variant.selection).Replace(reconcile),
			} {
				if _, err := tx.Exec(ctx, definition); err != nil {
					t.Fatal(err)
				}
			}
			// Nondefault attributes/ACLs make accidental resets observable.
			for _, statement := range []string{
				"ALTER FUNCTION " + coverageEnqueueSignature + " COST 73 ROWS 41 STRICT PARALLEL RESTRICTED",
				"ALTER FUNCTION " + coverageReconciliationApplySignature + " COST 91 PARALLEL RESTRICTED SET plan_cache_mode='force_custom_plan'",
				"GRANT EXECUTE ON FUNCTION " + coverageEnqueueSignature + " TO workouts_tiles",
			} {
				if _, err := tx.Exec(ctx, statement); err != nil {
					t.Fatal(err)
				}
			}
			before := coverageAnchorContract(t, ctx, tx, false)
			if _, err := tx.Exec(ctx, `SET LOCAL ROLE workouts_migration`); err != nil {
				t.Fatal(err)
			}
			// Execute the actual embedded Up SQL in a real migration-role transaction;
			// roll it back so each variant starts from the same feature checkpoint.
			if _, err := tx.Exec(ctx, up); err != nil {
				t.Fatal(err)
			}
			if after := coverageAnchorContract(t, ctx, tx, false); after != before {
				t.Fatal("rewrite changed function OID, owner, ACL, search_path or attributes")
			}
			var rewrittenEnqueue, rewrittenReconcile string
			if err := tx.QueryRow(ctx, `SELECT pg_get_functiondef($1::regprocedure),pg_get_functiondef($2::regprocedure)`, coverageEnqueueSignature, coverageReconciliationApplySignature).Scan(&rewrittenEnqueue, &rewrittenReconcile); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(rewrittenEnqueue, variant.enqueue) || !strings.Contains(rewrittenReconcile, variant.minimum) || !strings.Contains(rewrittenReconcile, variant.selection) {
				t.Fatal("rewrite did not preserve the exact matched SQL substrings")
			}
			coverageAnchorBehavior(t, ctx, tx, f)
			if err := tx.Rollback(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}

	addStatement := func(definition, statement string) string {
		if strings.Count(definition, "\nBEGIN\n") != 1 {
			t.Fatal("synthetic function fixture needs exactly one top-level BEGIN")
		}
		return strings.Replace(definition, "\nBEGIN\n", "\nBEGIN\n    "+statement+"\n", 1)
	}
	failures := []struct{ name, enqueue, reconcile, message string }{
		{"enqueue missing", strings.Replace(enqueue, coverageEnqueueAnchor, "AND job.status IN ('queued') ORDER BY job.created_at", 1), reconcile, "coverage enqueue isolation anchor"},
		{"enqueue duplicate", addStatement(enqueue, "PERFORM 1 FROM app.jobs job WHERE true "+coverageEnqueueAnchor+";"), reconcile, "coverage enqueue isolation anchor"},
		{"minimum missing", enqueue, strings.Replace(reconcile, coverageMinimumAnchor, "AND minimum_traversal_meters_value = parent_context.minimum_traversal_meters", 1), "coverage reconciliation matcher anchor"},
		{"minimum duplicate", enqueue, strings.Replace(reconcile, coverageMinimumAnchor, coverageMinimumAnchor+"\n"+coverageMinimumAnchor, 1), "coverage reconciliation matcher anchor"},
		{"selection missing", enqueue, strings.Replace(reconcile, coverageSelectionAnchor, "SELECT item->>'regionId', CAST(item->>'generation' AS bigint) INTO anchor_region,anchor_generation", 1), "coverage reconciliation retry-policy anchor"},
		{"selection duplicate", enqueue, addStatement(reconcile, coverageSelectionAnchor+" FROM jsonb_array_elements(region_generations) item LIMIT 1;"), "coverage reconciliation retry-policy anchor"},
		{"late failure after rewrites and grants", enqueue, reconcile, "synthetic late coverage retry failure"},
	}
	for _, failure := range failures {
		t.Run("automatic retry rollback/"+failure.name, func(t *testing.T) {
			for _, definition := range []string{failure.enqueue, failure.reconcile} {
				if _, err := admin.Exec(ctx, definition); err != nil {
					t.Fatal(err)
				}
			}
			before := coverageAnchorContract(t, ctx, admin, true)
			source := up
			if strings.HasPrefix(failure.name, "late failure") {
				source += `
				DO $late_failure$ BEGIN
				    IF to_regclass('app.coverage_retry_roots') IS NULL
				       OR position('job.retry_of_job_id IS NULL' IN pg_get_functiondef('app.enqueue_coverage_update(uuid,uuid,text,bigint,bigint,text,text,text,double precision,jsonb)'::regprocedure))=0
				       OR position('coverage_retry_targets(parent.id,target_account_id)' IN pg_get_functiondef('app.apply_coverage_reconciliation(uuid,bigint,text,uuid,uuid,bigint,bytea,text,text,jsonb,uuid,uuid,text,text,text,double precision)'::regprocedure))=0
				       OR NOT has_function_privilege('workouts_coverage_worker','app.retry_next_coverage_update(integer,integer)','EXECUTE')
				    THEN RAISE EXCEPTION 'late failure test ran before completed rewrites and grants'; END IF;
				    RAISE EXCEPTION 'synthetic late coverage retry failure';
				END; $late_failure$;`
			}
			if err := migrationfixture.ApplySQL(ctx, sqlDB, source); err == nil || !strings.Contains(err.Error(), failure.message) {
				t.Fatalf("expected bounded migration failure %q: %v", failure.message, err)
			}
			if after := coverageAnchorContract(t, ctx, admin, true); after != before {
				t.Fatal("failed migration did not restore original function bodies/attributes")
			}
			var rolledBack bool
			if err := admin.QueryRow(ctx, `SELECT to_regclass('app.coverage_retry_roots') IS NULL
				AND to_regprocedure('app.retry_next_coverage_update(integer,integer)') IS NULL
				AND to_regprocedure('app.current_coverage_matcher_contract()') IS NULL
				AND NOT EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid='app.coverage_job_contexts'::regclass AND attname='matcher_contract_version' AND NOT attisdropped)
				AND NOT EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid='app.jobs'::regclass AND tgname='coverage_retry_failure_enrollment')
				AND position('construct_coverage_retry' IN pg_get_functiondef('app.retry_coverage_update(uuid,uuid,integer)'::regprocedure))=0
				AND EXISTS (SELECT 1 FROM app.schema_metadata WHERE schema_version=26 AND minimum_runtime_version=26 AND (to_jsonb(schema_metadata)->>'migration_baseline') IS NULL)
				AND (SELECT count(*)=27 AND min(version_id)=0 AND max(version_id)=26 AND bool_and(is_applied) FROM public.goose_db_version)
				AND COALESCE((SELECT nspacl::text FROM pg_namespace WHERE nspname='app'),'')=$1
				AND EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid='app.job_progress'::regclass AND attname='workouts_created_with_route' AND NOT attisdropped)`, schemaACL).Scan(&rolledBack); err != nil || !rolledBack {
				t.Fatalf("failed migration left DDL/ACL/version residue: %t %v", rolledBack, err)
			}
		})
	}
	// The final semantic upgrade and the entire existing retry/cancel/reconcile
	// suite run against compact definitions matching the sanitized release probe.
	for _, definition := range []string{
		strings.Replace(enqueue, coverageEnqueueAnchor, variants[1].enqueue, 1),
		strings.NewReplacer(coverageMinimumAnchor, variants[1].minimum, coverageSelectionAnchor, variants[1].selection).Replace(reconcile),
	} {
		if _, err := admin.Exec(ctx, definition); err != nil {
			t.Fatal(err)
		}
	}
}

func coverageAnchorBehavior(t *testing.T, ctx context.Context, tx pgx.Tx, f *coverageAutoRetryFixture) {
	t.Helper()
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE NONE`); err != nil {
		t.Fatal(err)
	}
	lease := uuid.New()
	if _, err := tx.Exec(ctx, `INSERT INTO app.coverage_reconciliation_accounts(account_id,desired_revision,active_revision,worker_id,lease_token,heartbeat_at,lease_expires_at)
		VALUES($1,1,1,'synthetic-anchor-worker',$2,clock_timestamp(),clock_timestamp()+interval '7 minutes')`, f.account, lease); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, f.account.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE workouts_coverage_worker`); err != nil {
		t.Fatal(err)
	}
	var applied bool
	if err := tx.QueryRow(ctx, `SELECT app.apply_coverage_reconciliation($1,1,'synthetic-anchor-worker',$2,$3,1,decode(repeat('01',32),'hex'),
		'map_data_ready',NULL,'[{"regionId":"geofabrik:test","generation":1}]',$4,$5,
		'coverage-experimental-v1','coverage-sampling-experimental-v1','coverage-path-policy-experimental-v82',5)`, f.account, lease, f.workouts[0], uuid.New(), uuid.New()).Scan(&applied); err != nil || !applied {
		t.Fatalf("rewritten reconciliation apply failed: %t %v", applied, err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE NONE`); err != nil {
		t.Fatal(err)
	}
	var target uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT target_job_id FROM app.workout_coverage_states WHERE workout_id=$1`, f.workouts[0]).Scan(&target); err != nil || target != f.children[0] {
		t.Fatal("reconciliation formatting variant silently replaced an unchanged failure")
	}
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE workouts_api`); err != nil {
		t.Fatal(err)
	}
	var retry uuid.UUID
	var count int
	if err := tx.QueryRow(ctx, `SELECT * FROM app.retry_coverage_update($1,$2,100)`, f.parent, f.principal).Scan(&retry, &count); err != nil || count != 1 {
		t.Fatalf("manual retry after variant migration failed: count=%d err=%v", count, err)
	}
	if _, err := tx.Exec(ctx, `SET LOCAL ROLE NONE`); err != nil {
		t.Fatal(err)
	}
	var normal uuid.UUID
	var reused bool
	if err := tx.QueryRow(ctx, `SELECT * FROM app.enqueue_coverage_update($1,$2,'geofabrik:test',1,2,
		'coverage-experimental-v1','coverage-sampling-experimental-v1','coverage-path-policy-experimental-v82',5,
		jsonb_build_array(jsonb_build_object('jobId',$3::uuid,'workoutId',$4::uuid,'routeRevision',1,
		'generations','[{"regionId":"geofabrik:test","generation":1}]'::jsonb)))`, f.account, uuid.New(), uuid.New(), f.workouts[0]).Scan(&normal, &count, &reused); err != nil || reused || normal == retry || count != 1 {
		t.Fatalf("enqueue variant reused a retry parent: reused=%t count=%d err=%v", reused, count, err)
	}
}
