package worker

import (
	"testing"

	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const coverageReconciliationApplySignature = "app.apply_coverage_reconciliation(uuid,bigint,text,uuid,uuid,bigint,bytea,text,text,jsonb,uuid,uuid,text,text,text,double precision)"

func testCoverageRetryReconciliation(t *testing.T, historical *coverageAutoRetryFixture, newFixture func(*testing.T, int) *coverageAutoRetryFixture) {
	t.Run("reconciliation legacy failures remain manual eligible without fresh roots", func(t *testing.T) {
		f := *historical
		f.t = t
		for range 3 {
			f.reconcileUnchanged()
		}
		assertCountQuery(t, f.admin, 0, `SELECT count(*) FROM app.coverage_retry_roots WHERE root_job_id=$1`, f.parent)
		f.automatic(20, false)
		retry, err := f.manual(f.parent, f.principal)
		if err != nil || retry == uuid.Nil {
			t.Fatalf("legacy manual retry lost: %s %v", retry, err)
		}
		f.adopt(retry)
		f.finish(0, "")
	})

	t.Run("reconciliation cannot bypass cooldown default one or raised two", func(t *testing.T) {
		f := newFixture(t, 1)
		root := f.parent
		f.finish(0, "coverage-route-timeout")
		for range 3 {
			f.reconcileUnchanged()
		}
		assertCountQuery(t, f.admin, 1, `SELECT count(*) FROM app.coverage_reconciliation_accounts WHERE account_id=$1 AND active_revision=3`, f.account)
		f.automatic(1, false)
		for limit := 1; limit <= 2; limit++ {
			f.age()
			retry := f.automatic(limit, true)
			f.adopt(retry)
			f.finish(0, "coverage-route-timeout")
			f.age()
			f.automatic(limit, false)
			for range 3 {
				f.reconcileUnchanged()
			}
			assertCountQuery(t, f.admin, limit+1, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update'`, f.account)
			assertCountQuery(t, f.admin, 1, `SELECT count(*) FROM app.coverage_retry_roots WHERE root_job_id=$1 AND automatic_attempts_used=$2`, root, limit)
		}
		retry, err := f.manual(f.parent, f.principal)
		if err != nil || retry == uuid.Nil {
			t.Fatalf("manual retry after reconciliation and exhaustion: %s %v", retry, err)
		}
		f.adopt(retry)
		f.finish(0, "")
	})

	for _, running := range []bool{false, true} {
		name := "queued"
		if running {
			name = "running"
		}
		t.Run("reconciliation preserves real "+name+" cancellation", func(t *testing.T) {
			f := newFixture(t, 1)
			lease := uuid.New()
			if running {
				tx := beginWorkerTestAccount(t, f.admin, f.account)
				var ok bool
				if err := tx.QueryRow(f.ctx, `SELECT app.claim_job($1,'synthetic-retry-worker',$2,interval '7 minutes')`, f.children[0], lease).Scan(&ok); err != nil || !ok {
					t.Fatalf("claim before cancellation: %t %v", ok, err)
				}
				if err := tx.Commit(f.ctx); err != nil {
					t.Fatal(err)
				}
			}
			tx := beginWorkerTestAccount(t, f.api, f.account)
			var cancelled bool
			if err := tx.QueryRow(f.ctx, `SELECT app.request_owned_job_cancellation($1,$2)`, f.parent, f.principal).Scan(&cancelled); err != nil || !cancelled {
				t.Fatalf("cancel: %t %v", cancelled, err)
			}
			if err := tx.Commit(f.ctx); err != nil {
				t.Fatal(err)
			}
			for range 3 {
				if child := f.reconcile("", "", 0, true); child != f.children[0] {
					t.Fatal("reconciliation replaced cancellation intent")
				}
			}
			if running {
				tx := beginWorkerTestAccount(t, f.worker, f.account)
				var ok bool
				if err := tx.QueryRow(f.ctx, `SELECT app.finish_job($1,'synthetic-retry-worker',$2,'cancelled')`, f.children[0], lease).Scan(&ok); err != nil || !ok {
					t.Fatalf("finish cancellation: %t %v", ok, err)
				}
				if err := tx.Commit(f.ctx); err != nil {
					t.Fatal(err)
				}
				if child := f.reconcile("", "", 0, true); child != f.children[0] {
					t.Fatal("reconciliation revived terminal cancellation")
				}
			}
			assertCountQuery(t, f.admin, 1, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update'`, f.account)
			assertCountQuery(t, f.admin, 1, `SELECT count(*) FROM app.jobs WHERE id=$1 AND status='cancelled' AND cancel_requested_at IS NOT NULL`, f.children[0])
			f.automatic(20, false)
		})
	}

	for _, change := range []string{"raw input", "revision", "digest", "generation", "region list", "path policy", "minimum traversal", "matcher contract", "readiness regained"} {
		t.Run("reconciliation fresh target/"+change, func(t *testing.T) {
			f := newFixture(t, 1)
			f.finish(0, "coverage-route-timeout")
			f.reconcileUnchanged()
			generations, policy, minimum := "", "", 0.0
			switch change {
			case "raw input":
				f.installRawRoute()
				f.synthetic(`UPDATE app.workout_coverage_states SET route_input_revision=2,route_input_sha256=app.adopted_coverage_route_digest(account_id,workout_id) WHERE workout_id=$1`, f.workouts[0])
			case "revision":
				f.synthetic(`UPDATE app.workout_coverage_states SET route_input_revision=2 WHERE workout_id=$1`, f.workouts[0])
			case "digest":
				f.synthetic(`UPDATE app.workout_coverage_states SET route_input_sha256=decode(repeat('02',32),'hex') WHERE workout_id=$1`, f.workouts[0])
			case "generation":
				generations = `[{"regionId":"geofabrik:test","generation":2}]`
			case "region list":
				generations = `[{"regionId":"geofabrik:test","generation":1},{"regionId":"geofabrik:other","generation":1}]`
			case "path policy":
				policy = "coverage-path-policy-experimental-v83"
			case "minimum traversal":
				minimum = 6
			case "matcher contract":
				// An unrelated queued v1 parent must not absorb v2 work either.
				if _, err := f.admin.Exec(f.ctx, `INSERT INTO app.jobs(id,account_id,kind,priority) VALUES($1,$2,'coverage_update',20)`, uuid.New(), f.account); err != nil {
					t.Fatal(err)
				}
				if _, err := f.admin.Exec(f.ctx, `INSERT INTO app.coverage_job_contexts(job_id,account_id,region_id,target_osm_generation,target_work_revision,rules_version,sampling_version,path_policy_version,minimum_traversal_meters,matcher_contract_version)
					SELECT job.id,context.account_id,context.region_id,1,1,context.rules_version,context.sampling_version,context.path_policy_version,5,context.matcher_contract_version
					FROM app.jobs job JOIN app.coverage_job_contexts context ON context.job_id=$2 WHERE job.account_id=$1 AND job.kind='coverage_update' AND job.status='queued'`, f.account, f.parent); err != nil {
					t.Fatal(err)
				}
				if _, err := f.worker.Exec(f.ctx, `SELECT * FROM app.observe_coverage_reconciliation(0,'coverage-production-v2')`); err != nil {
					t.Fatal(err)
				}
				f.age()
				f.automatic(20, false)
				t.Cleanup(func() {
					if _, err := f.worker.Exec(f.ctx, `SELECT * FROM app.observe_coverage_reconciliation(0,'coverage-production-v1')`); err != nil {
						t.Error(err)
					}
				})
			case "readiness regained":
				if child := f.reconcile("", "", 0, false); child != uuid.Nil {
					t.Fatal("pending readiness retained job authority")
				}
			}
			child := f.reconcile(generations, policy, minimum, true)
			if child == uuid.Nil || child == f.children[0] {
				t.Fatal("changed target did not create fresh work")
			}
			assertCountQuery(t, f.admin, 1, `SELECT count(*) FROM app.jobs child JOIN app.jobs parent ON parent.id=child.parent_job_id WHERE child.id=$1 AND child.retry_of_job_id IS NULL AND parent.retry_of_job_id IS NULL`, child)
			if change == "matcher contract" {
				assertCountQuery(t, f.admin, 1, `SELECT count(*) FROM app.jobs child JOIN app.coverage_job_contexts context ON context.job_id=child.parent_job_id WHERE child.id=$1 AND context.matcher_contract_version='coverage-production-v2'`, child)
			}
			if replay := f.reconcile(generations, policy, minimum, true); replay != child {
				t.Fatal("identical live target was replaced")
			}
		})
	}

	t.Run("reconciliation generation order is not a new target", func(t *testing.T) {
		f := newFixture(t, 1)
		vector := `[{"regionId":"geofabrik:test","generation":1},{"regionId":"geofabrik:other","generation":2}]`
		f.synthetic(`UPDATE app.coverage_route_job_contexts SET target_generations=$2::jsonb WHERE job_id=$1`, f.children[0], vector)
		f.synthetic(`UPDATE app.workout_coverage_states SET target_generations=$2::jsonb WHERE workout_id=$1`, f.workouts[0], vector)
		f.finish(0, "coverage-route-timeout")
		if child := f.reconcile(`[{"regionId":"geofabrik:other","generation":2},{"regionId":"geofabrik:test","generation":1}]`, "", 0, true); child != f.children[0] {
			t.Fatal("generation list reordering bypassed retry policy")
		}
		if retry, err := f.manual(f.parent, f.principal); err != nil || retry == uuid.Nil {
			t.Fatalf("explicit retry after equivalent generation ordering: %s %v", retry, err)
		}
	})

	t.Run("distinct forced adoption can replace exhausted unchanged target", func(t *testing.T) {
		f := newFixture(t, 1)
		f.installRawRoute()
		f.synthetic(`UPDATE app.workout_coverage_states SET route_input_sha256=app.adopted_coverage_route_digest(account_id,workout_id) WHERE workout_id=$1`, f.workouts[0])
		f.synthetic(`UPDATE app.coverage_route_job_contexts SET route_input_sha256=app.adopted_coverage_route_digest(account_id,workout_id) WHERE job_id=$1`, f.children[0])
		root := f.parent
		f.finish(0, "coverage-route-timeout")
		f.age()
		f.adopt(f.automatic(1, true))
		f.finish(0, "coverage-route-timeout")
		f.age()
		f.automatic(1, false)
		f.reconcileUnchanged()
		var lease uuid.UUID
		var revision int64
		var digest []byte
		if err := f.admin.QueryRow(f.ctx, `SELECT lease_token,active_revision FROM app.coverage_reconciliation_accounts WHERE account_id=$1`, f.account).Scan(&lease, &revision); err != nil {
			t.Fatal(err)
		}
		if err := f.admin.QueryRow(f.ctx, `SELECT route_input_sha256 FROM app.workout_coverage_states WHERE workout_id=$1`, f.workouts[0]).Scan(&digest); err != nil {
			t.Fatal(err)
		}
		// Record a genuine new source import, not a direct legacy force-helper
		// call (that bypass is intentionally no longer executable in 028).
		parent, ingest, file, importEvent, ingestLease := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
		seed := beginWorkerTestAccount(t, f.admin, f.account)
		defer seed.Rollback(f.ctx)
		for _, statement := range []struct {
			q    string
			args []any
		}{
			{`INSERT INTO app.jobs(id,account_id,kind,priority) VALUES($1,$2,'manual_ingest',80)`, []any{parent, f.account}},
			{`INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority,parameters) SELECT $1,$2,$3,'manual_ingest_source',80,
		 jsonb_build_object('sourceId',upper(replace(source_id::text,'-','')),'generation',1,'mode','incremental') FROM app.workouts WHERE id=$4`, []any{ingest, parent, f.account, f.workouts[0]}},
			{`INSERT INTO app.job_config_snapshots(job_id,account_id,source_id,source_generation,config_envelope)
		 SELECT $1,workout.account_id,source.id,source.generation,source.config_envelope FROM app.workouts workout JOIN app.sources source ON source.id=workout.source_id WHERE workout.id=$2`, []any{ingest, f.workouts[0]}},
		} {
			if _, err := seed.Exec(f.ctx, statement.q, statement.args...); err != nil {
				t.Fatal(err)
			}
		}
		if err := seed.Commit(f.ctx); err != nil {
			t.Fatal(err)
		}
		ingestConfig := f.admin.Config()
		ingestConfig.ConnConfig.RuntimeParams["role"] = "workouts_worker"
		ingestDB, err := pgxpool.NewWithConfig(f.ctx, ingestConfig)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(ingestDB.Close)
		write := beginWorkerTestAccount(t, ingestDB, f.account)
		defer write.Rollback(f.ctx)
		for _, statement := range []struct {
			q    string
			args []any
		}{
			{`SELECT app.claim_job($1,'synthetic-adoption',$2,interval '2 minutes')`, []any{ingest, ingestLease}},
			{`SELECT * FROM app.fence_ingest_job($1,'synthetic-adoption',$2,28)`, []any{ingest, ingestLease}},
			{`INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes,state,processing_started_at)
		 SELECT $1,account_id,source_id,$2,'synthetic-adoption.json',1,'processing',clock_timestamp() FROM app.workouts WHERE id=$3`, []any{file, ingest, f.workouts[0]}},
			{`UPDATE app.workouts SET source_file_id=$2 WHERE id=$1`, []any{f.workouts[0], file}},
			{`INSERT INTO app.workout_import_events(id,account_id,source_id,workout_id,source_file_id,job_id,kind,content_sha256,has_route)
		 SELECT $1,account_id,source_id,id,$2,$3,'adopted',content_sha256,true FROM app.workouts WHERE id=$4`, []any{importEvent, file, ingest, f.workouts[0]}},
			{`SELECT app.record_adopted_workout_coverage_refresh($1,'synthetic-adoption',$2,$3)`, []any{ingest, ingestLease, f.workouts[0]}},
			{`UPDATE app.source_files SET state='succeeded',processed_at=clock_timestamp(),checksum_sha256=decode(repeat('01',32),'hex') WHERE id=$1`, []any{file}},
		} {
			if _, err := write.Exec(f.ctx, statement.q, statement.args...); err != nil {
				t.Fatal(err)
			}
		}
		if err := write.Commit(f.ctx); err != nil {
			t.Fatal(err)
		}
		finish := beginWorkerTestAccount(t, ingestDB, f.account)
		var finished bool
		if err := finish.QueryRow(f.ctx, `SELECT app.finish_job($1,'synthetic-adoption',$2,'succeeded',NULL,NULL)`, ingest, ingestLease).Scan(&finished); err != nil || !finished {
			t.Fatalf("finish genuine adoption root: %v", err)
		}
		if err := finish.Commit(f.ctx); err != nil {
			t.Fatal(err)
		}
		var payload []byte
		if err := f.admin.QueryRow(f.ctx, `SELECT jsonb_build_array(jsonb_build_object('workoutId',workout.id,'eventId',request.event_id,'origin','adopted',
		 'contentSha256',encode(workout.content_sha256,'hex'),'routeDigest',encode(state.route_input_sha256,'hex'),'ownershipEpoch',account.workout_ownership_epoch,
		 'readiness','map_data_ready','reason',NULL,'catalogFingerprint',repeat('a',64),'generations','[{"regionId":"geofabrik:test","generation":1}]'::jsonb))
		 FROM app.workouts workout JOIN app.accounts account ON account.id=workout.account_id
		 JOIN app.workout_coverage_states state ON state.workout_id=workout.id JOIN app.adopted_workout_coverage_refreshes request ON request.workout_id=workout.id WHERE workout.id=$1`, f.workouts[0]).Scan(&payload); err != nil {
			t.Fatal(err)
		}
		tx := beginWorkerTestAccount(t, f.worker, f.account)
		defer tx.Rollback(f.ctx)
		if _, err := tx.Exec(f.ctx, `SELECT * FROM app.apply_ingest_coverage_batch($1,NULL,'synthetic-reconciler',$2,$3,$4,
		 'coverage-experimental-v1','coverage-sampling-experimental-v1','coverage-path-policy-experimental-v82',5,28)`, f.account, lease, revision, payload); err != nil {
			t.Fatal(err)
		}
		var builder uuid.UUID
		var targets []byte
		if err := tx.QueryRow(f.ctx, `SELECT builder_id,targets FROM app.read_ingest_coverage_prepared($1,NULL,'synthetic-reconciler',$2,$3,1)`, f.account, lease, revision).Scan(&builder, &targets); err != nil {
			t.Fatal(err)
		}
		var freshParent *uuid.UUID
		var routeCount int
		var outcome string
		if err := tx.QueryRow(f.ctx, `SELECT * FROM app.seal_ingest_coverage_cohort($1,NULL,'synthetic-reconciler',$2,$3,$4,$5,28)`, f.account, lease, revision, builder, targets).Scan(&freshParent, &routeCount, &outcome); err != nil || outcome != "sealed" || routeCount != 1 {
			t.Fatalf("forced cohort seal: %s %d %v", outcome, routeCount, err)
		}
		if err := tx.Commit(f.ctx); err != nil {
			t.Fatal(err)
		}
		var child uuid.UUID
		if err := f.admin.QueryRow(f.ctx, `SELECT target_job_id FROM app.workout_coverage_states WHERE workout_id=$1`, f.workouts[0]).Scan(&child); err != nil {
			t.Fatal(err)
		}
		assertCountQuery(t, f.admin, 1, `SELECT count(*) FROM app.workout_coverage_states WHERE workout_id=$1 AND target_job_id=$2 AND processing_state='queued'`, f.workouts[0], child)
		assertCountQuery(t, f.admin, 1, `SELECT count(*) FROM app.coverage_retry_roots WHERE root_job_id=$1 AND automatic_attempts_used=1`, root)
		assertCountQuery(t, f.admin, 1, `SELECT count(*) FROM app.jobs WHERE id=$1 AND retry_of_job_id IS NULL`, child)
	})
}

func (f *coverageAutoRetryFixture) reconcileUnchanged() {
	f.t.Helper()
	if child := f.reconcile("", "", 0, true); child != f.children[0] {
		f.t.Fatalf("unchanged failed target replaced: got=%s prior=%s", child, f.children[0])
	}
	assertCountQuery(f.t, f.admin, 1, `SELECT count(*) FROM app.workout_coverage_states WHERE workout_id=$1 AND target_job_id=$2 AND processing_state='failed' AND processing_failure_code='coverage-route-timeout'`, f.workouts[0], f.children[0])
}

func (f *coverageAutoRetryFixture) reconcile(generations, policy string, minimum float64, ready bool) uuid.UUID {
	f.t.Helper()
	if generations == "" {
		generations = `[{"regionId":"geofabrik:test","generation":1}]`
	}
	if policy == "" {
		policy = "coverage-path-policy-experimental-v82"
	}
	if minimum == 0 {
		minimum = 5
	}
	readiness := "map_data_ready"
	var reason *string
	if !ready {
		readiness, generations = "pending", "[]"
		value := "region_not_active"
		reason = &value
	}
	lease := uuid.New()
	var reconciliationRevision int64
	if err := f.admin.QueryRow(f.ctx, `INSERT INTO app.coverage_reconciliation_accounts AS reconciliation(account_id,desired_revision,active_revision,worker_id,lease_token,heartbeat_at,lease_expires_at,lease_runtime_version)
		VALUES($1,1,1,'synthetic-reconciler',$2,clock_timestamp(),clock_timestamp()+interval '7 minutes',$3)
		ON CONFLICT(account_id) DO UPDATE SET desired_revision=reconciliation.desired_revision+1,active_revision=reconciliation.desired_revision+1,
		worker_id=EXCLUDED.worker_id,lease_token=EXCLUDED.lease_token,heartbeat_at=EXCLUDED.heartbeat_at,lease_expires_at=EXCLUDED.lease_expires_at,
		lease_runtime_version=EXCLUDED.lease_runtime_version
		RETURNING active_revision`, f.account, lease, database.SupportedSchemaVersion).Scan(&reconciliationRevision); err != nil {
		f.t.Fatal(err)
	}
	var revision int64
	var digest []byte
	if err := f.admin.QueryRow(f.ctx, `SELECT route_input_revision,route_input_sha256 FROM app.workout_coverage_states WHERE workout_id=$1`, f.workouts[0]).Scan(&revision, &digest); err != nil {
		f.t.Fatal(err)
	}
	tx := beginWorkerTestAccount(f.t, f.worker, f.account)
	defer tx.Rollback(f.ctx)
	var ok bool
	if err := tx.QueryRow(f.ctx, `SELECT app.apply_coverage_reconciliation($1,$13,'synthetic-reconciler',$2,$3,$4,$5,$6,$7,$8::jsonb,$9,$10,
		'coverage-experimental-v1','coverage-sampling-experimental-v1',$11,$12)`, f.account, lease, f.workouts[0], revision, digest,
		readiness, reason, generations, uuid.New(), uuid.New(), policy, minimum, reconciliationRevision).Scan(&ok); err != nil || !ok {
		f.t.Fatalf("normal reconciliation apply: %t %v", ok, err)
	}
	if err := tx.Commit(f.ctx); err != nil {
		f.t.Fatal(err)
	}
	var child *uuid.UUID
	if err := f.admin.QueryRow(f.ctx, `SELECT target_job_id FROM app.workout_coverage_states WHERE workout_id=$1`, f.workouts[0]).Scan(&child); err != nil {
		f.t.Fatal(err)
	}
	if child == nil {
		return uuid.Nil
	}
	return *child
}

func (f *coverageAutoRetryFixture) installRawRoute() {
	f.t.Helper()
	f.synthetic(`INSERT INTO app.workout_route_points(account_id,workout_id,sequence,recorded_at,latitude,longitude)
		VALUES($1,$2,0,'2026-08-01T00:00:00Z',0,0),($1,$2,1,'2026-08-01T00:00:01Z',0,0.001)`, f.account, f.workouts[0])
	f.synthetic(`INSERT INTO app.workout_routes(account_id,workout_id,point_count,minimum_longitude,minimum_latitude,maximum_longitude,maximum_latitude,has_complete_altitude)
		VALUES($1,$2,2,0,0,0.001,0,false)`, f.account, f.workouts[0])
}
