package worker

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type adoptionCoverageReplayTrace struct {
	lookup func(context.Context, pgx.TraceQueryStartData) context.Context
}

func (trace *adoptionCoverageReplayTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, query pgx.TraceQueryStartData) context.Context {
	if strings.Contains(query.SQL, "WITH route_point AS") {
		return trace.lookup(ctx, query)
	}
	return ctx
}

func (*adoptionCoverageReplayTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {
}

func TestAdoptionCoverageBacklogRenewalAndDeferredEligibilityIntegration(t *testing.T) {
	f, normal, _ := newAdoptionCoverageFixture(t)
	const deferredCount, readyCount, batchSize = 8, 12, 10
	const shortLease = 2 * time.Second
	var deferred []uuid.UUID
	for i := range deferredCount + readyCount {
		workout := f.accountID
		workout[0], workout[1] = 0xf0, byte(i)
		longitude := -122.02 - float64(i)*0.000001
		if i < deferredCount {
			workout[0] = 0
			longitude = -122.001 - float64(i)*0.000001
			deferred = append(deferred, workout)
		}
		adoptionCoverageBacklogWorkout(t, f, workout, longitude, i < deferredCount)
	}
	tx := f.accountTx(f.ctx, f.adminDB)
	f.exec(tx, `UPDATE app.workout_coverage_states SET processing_state='stale' WHERE account_id=$1 AND workout_id=$2`, f.accountID, normal)
	f.exec(tx, `INSERT INTO app.coverage_reconciliation_accounts(account_id,desired_revision,active_revision,completed_revision,next_scan_at)
		VALUES($1,1,1,1,clock_timestamp()+interval '1 day')`, f.accountID)
	f.commit(tx)
	lockTrace := &adoptionCoverageLockTrace{fixture: f, workout: normal}
	deferredLookups := 0
	normalQueuedBeforeReplay := false
	trace := &adoptionCoverageReplayTrace{lookup: func(ctx context.Context, query pgx.TraceQueryStartData) context.Context {
		ctx = lockTrace.TraceQueryStart(ctx, nil, query)
		longitude := query.Args[0].([]float64)[0]
		if longitude < -122.0005 && longitude > -122.005 {
			deferredLookups++
		}
		if longitude < -122.01 && !normalQueuedBeforeReplay {
			tx, err := beginAccount(ctx, f.adminDB, f.accountID)
			if err == nil {
				err = tx.QueryRow(ctx, `SELECT target_job_id IS NOT NULL FROM app.workout_coverage_states WHERE account_id=$1 AND workout_id=$2`, f.accountID, normal).Scan(&normalQueuedBeforeReplay)
				_ = tx.Rollback(ctx)
			}
			if err != nil {
				lockTrace.err = err
			} else if !normalQueuedBeforeReplay {
				lockTrace.err = errors.New("adoption backlog ran before ordinary reconciliation got a turn")
			}
		}
		timer := time.NewTimer(250 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
		case <-timer.C:
		}
		return ctx
	}}
	config := f.osmDB.Config()
	config.ConnConfig.Tracer = trace
	tracedOSM, err := pgxpool.NewWithConfig(f.ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	f.osmDB = tracedOSM
	t.Cleanup(tracedOSM.Close)
	r := NewCoverageReconciler(f.coverageDB, f.osmDB, slog.Default(), CoverageReconcilerOptions{PageSize: batchSize})
	if err := r.syncRegionCatalog(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := r.observe(f.ctx); err != nil {
		t.Fatal(err)
	}
	// Claim with the production SQL minimum, then renew to a short actual
	// lease. The heartbeat helper intentionally accepts sub-30s durations.
	r.leaseDuration = 30 * time.Second
	claim, found, err := r.claim(f.ctx)
	if err != nil || !found || claim.accountID != f.accountID {
		t.Fatalf("backlog claim found=%v err=%v", found, err)
	}
	r.leaseDuration = shortLease
	if err := r.heartbeat(f.ctx, claim); err != nil {
		t.Fatal("shorten the actual claimed lease:", err)
	}
	started := time.Now()
	if err := r.process(f.ctx, claim); err != nil {
		t.Fatal("backlog replay:", err)
	}
	if time.Since(started) <= shortLease {
		t.Fatal("fixture did not span the short reconciliation lease")
	}
	tx = f.accountTx(f.ctx, f.adminDB)
	var children, requests int
	f.scan(tx, `SELECT (SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route'),
		(SELECT count(*) FROM app.adopted_workout_coverage_refreshes WHERE account_id=$1)`, []any{f.accountID}, &children, &requests)
	f.commit(tx)
	if children != readyCount+1 || requests != deferredCount || !normalQueuedBeforeReplay || deferredLookups != 0 {
		t.Fatalf("children=%d pending=%d normal-before-replay=%v deferred-lookups=%d", children, requests, normalQueuedBeforeReplay, deferredLookups)
	}
	// Ordinary ingest repair must not bypass deferred force obligations even
	// though the provider is now available and their cached state is unavailable.
	runner := &Runner{db: f.workerDB, osmDB: f.osmDB, workerID: "adoption-worker-fixture", coverageMinTraversalMeters: 5, leaseDuration: defaultLease}
	if err := runner.repairCoverageReadiness(f.ctx, claimedJob{id: f.ingestID, accountID: f.accountID, lease: f.ingestLease}); err != nil {
		t.Fatal(err)
	}
	if deferredLookups != 0 {
		t.Fatal("ordinary repair replayed a deferred adoption")
	}
	r.leaseDuration = 30 * time.Second
	if worked, err := r.RunOnce(f.ctx); err != nil || worked {
		t.Fatalf("deferred prefix caused a hot claim loop: worked=%v err=%v", worked, err)
	}
	tx = f.accountTx(f.ctx, f.adminDB)
	f.exec(tx, `UPDATE app.adopted_workout_coverage_refreshes SET next_attempt_at='-infinity' WHERE account_id=$1 AND workout_id=$2`, f.accountID, deferred[0])
	f.commit(tx)
	if worked, err := r.RunOnce(f.ctx); err != nil || !worked {
		t.Fatalf("due deferred request was not resumed: worked=%v err=%v", worked, err)
	}
	tx = f.accountTx(f.ctx, f.adminDB)
	var valid bool
	f.scan(tx, `SELECT (SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route')=$2
		AND (SELECT count(*) FROM app.adopted_workout_coverage_refreshes WHERE account_id=$1)=$3
		AND NOT EXISTS(SELECT 1 FROM app.adopted_workout_coverage_refreshes WHERE account_id=$1 AND next_attempt_at<=clock_timestamp())`,
		[]any{f.accountID, readyCount + 2, deferredCount - 1}, &valid)
	f.commit(tx)
	if !valid || deferredLookups != 1 || lockTrace.checks == 0 || lockTrace.err != nil {
		t.Fatalf("due replay valid=%v deferred-lookups=%d lock-checks=%d err=%v", valid, deferredLookups, lockTrace.checks, lockTrace.err)
	}
}

func TestAdoptionCoverageReplayStopsOnLossCancellationAndTimeoutIntegration(t *testing.T) {
	for _, scenario := range []string{"lease_loss", "cancellation", "lookup_timeout"} {
		t.Run(scenario, func(t *testing.T) {
			f, workout, _ := newAdoptionCoverageFixture(t)
			tx := f.accountTx(f.ctx, f.adminDB)
			f.exec(tx, `INSERT INTO app.adopted_workout_coverage_refreshes(account_id,workout_id,source_id,source_file_id)
				VALUES($1,$2,$3,$4)`, f.accountID, workout, f.sourceID, f.fileID)
			f.exec(tx, `INSERT INTO app.coverage_reconciliation_accounts(account_id,desired_revision,active_revision,completed_revision,next_scan_at)
				VALUES($1,1,1,1,clock_timestamp()+interval '1 day')`, f.accountID)
			f.commit(tx)
			r := NewCoverageReconciler(f.coverageDB, f.osmDB, slog.Default(), CoverageReconcilerOptions{LeaseDuration: 30 * time.Second})
			claim, found, err := r.claim(f.ctx)
			if err != nil || !found || claim.accountID != f.accountID {
				t.Fatalf("stop fixture claim found=%v err=%v", found, err)
			}
			r.leaseDuration = 400 * time.Millisecond
			ctx, cancel := context.WithCancel(f.ctx)
			defer cancel()
			lookups := 0
			var hookErr error
			trace := &adoptionCoverageReplayTrace{lookup: func(ctx context.Context, _ pgx.TraceQueryStartData) context.Context {
				lookups++
				switch scenario {
				case "lease_loss":
					tx, err := beginAccount(f.ctx, f.adminDB, f.accountID)
					if err == nil {
						_, err = tx.Exec(f.ctx, `UPDATE app.coverage_reconciliation_accounts SET lease_token=$2 WHERE account_id=$1`, f.accountID, uuid.New())
						if err == nil {
							err = tx.Commit(f.ctx)
						} else {
							_ = tx.Rollback(f.ctx)
						}
					}
					hookErr = err
					if err != nil {
						cancel()
					}
				case "cancellation":
					cancel()
				case "lookup_timeout":
					<-ctx.Done()
				}
				return ctx
			}}
			config := f.osmDB.Config()
			config.ConnConfig.Tracer = trace
			f.osmDB, err = pgxpool.NewWithConfig(f.ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(f.osmDB.Close)
			r.osmDB = f.osmDB
			err = r.process(ctx, claim)
			want := errLeaseLost
			if scenario == "cancellation" {
				want = context.Canceled
			} else if scenario == "lookup_timeout" {
				want = context.DeadlineExceeded
			}
			if !errors.Is(err, want) || lookups != 1 || hookErr != nil {
				t.Fatalf("stop reason=%v want=%v lookups=%d hook=%v", err, want, lookups, hookErr)
			}
			tx = f.accountTx(f.ctx, f.adminDB)
			var heartbeat time.Time
			f.scan(tx, `SELECT heartbeat_at FROM app.coverage_reconciliation_accounts WHERE account_id=$1`, []any{f.accountID}, &heartbeat)
			f.exec(tx, `SELECT pg_sleep(0.5)`)
			var stopped bool
			f.scan(tx, `SELECT heartbeat_at=$2 AND lease_expires_at<clock_timestamp()
				AND EXISTS(SELECT 1 FROM app.adopted_workout_coverage_refreshes WHERE account_id=$1)
				AND NOT EXISTS(SELECT 1 FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route')
				FROM app.coverage_reconciliation_accounts WHERE account_id=$1`, []any{f.accountID, heartbeat}, &stopped)
			f.commit(tx)
			if !stopped || f.workerDB.Stat().AcquiredConns() != 0 || f.coverageDB.Stat().AcquiredConns() != 0 || f.osmDB.Stat().AcquiredConns() != 0 {
				t.Fatal("replay lost the request, renewed after return, or leaked a connection")
			}
		})
	}
}

func adoptionCoverageBacklogWorkout(t *testing.T, f *coverageIntegrationFixture, workout uuid.UUID, longitude float64, deferred bool) {
	t.Helper()
	first := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	second := first.Add(time.Second)
	digest := canonicalRouteInputSHA256([]coverageRoutePoint{
		{Sequence: 0, Timestamp: &first, Latitude: 37, Longitude: longitude},
		{Sequence: 1, Timestamp: &second, Latitude: 37.0001, Longitude: longitude - 0.0001},
	})
	tx := f.accountTx(f.ctx, f.adminDB)
	f.exec(tx, `SET LOCAL session_replication_role='replica'`)
	f.exec(tx, `INSERT INTO app.workouts(id,account_id,source_id,source_file_id,workout_type_id,provider_id,content_sha256,provider_label,started_at,ended_at,local_start_date,provider_duration)
		VALUES($1,$2,$3,$4,$5,$1::uuid::text,$6,'Running','2026-01-01Z','2026-01-01T00:01:00Z','2026-01-01',60)`, workout, f.accountID, f.sourceID, f.fileID, f.typeID, digest[:])
	f.exec(tx, `INSERT INTO app.workout_route_points(account_id,workout_id,sequence,recorded_at,latitude,longitude)
		VALUES($1,$2,0,$3,37,$5),($1,$2,1,$4,37.0001,$5::double precision-0.0001)`, f.accountID, workout, first, second, longitude)
	f.exec(tx, `INSERT INTO app.workout_routes(account_id,workout_id,point_count,minimum_longitude,minimum_latitude,maximum_longitude,maximum_latitude,has_complete_altitude,route)
		VALUES($1,$2,2,$3::double precision-0.0001,37,$3,37.0001,false,
		ST_Multi(ST_SetSRID(ST_MakeLine(ST_MakePoint($3,37),ST_MakePoint($3::double precision-0.0001,37.0001)),4326)))`, f.accountID, workout, longitude)
	readiness := "map_data_ready"
	var reason any
	if deferred {
		readiness, reason = "unavailable", "no_provider_region"
	}
	f.exec(tx, `INSERT INTO app.workout_coverage_states(account_id,workout_id,route_input_revision,route_input_sha256,readiness_state,reason,map_data_ready_at)
		VALUES($1,$2,7,$3,$4,$5,CASE WHEN $4='map_data_ready' THEN clock_timestamp() END)`, f.accountID, workout, digest[:], readiness, reason)
	if !deferred {
		f.exec(tx, `INSERT INTO app.workout_coverage_regions(account_id,workout_id,region_id,desired_osm_generation) VALUES($1,$2,$3,$4)`, f.accountID, workout, f.regionID, f.generation)
		f.exec(tx, `UPDATE app.workout_coverage_states SET processing_state='current',applied_route_input_revision=route_input_revision,
			applied_route_input_sha256=route_input_sha256,applied_rules_version='coverage-experimental-v1',
			applied_sampling_version='coverage-sampling-experimental-v1',applied_path_policy_version='coverage-path-policy-experimental-v82',
			applied_generations=jsonb_build_array(jsonb_build_object('regionId',$3::text,'generation',$4::bigint)) WHERE account_id=$1 AND workout_id=$2`,
			f.accountID, workout, f.regionID, f.generation)
	}
	f.exec(tx, `INSERT INTO app.adopted_workout_coverage_refreshes(account_id,workout_id,source_id,source_file_id,next_attempt_at)
		VALUES($1,$2,$3,$4,CASE WHEN $5 THEN clock_timestamp()+interval '1 hour' ELSE '-infinity'::timestamptz END)`, f.accountID, workout, f.sourceID, f.fileID, deferred)
	f.commit(tx)
}
