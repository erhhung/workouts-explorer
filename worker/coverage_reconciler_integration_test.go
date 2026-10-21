package worker

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Only the explicitly disposable fixture's admin connection simulates replay.
// Production lookups still run with the actual read-only OSM/coverage roles.
func reconcilerFixtureReplay(f *coverageIntegrationFixture, newerGeneration, newerEvent int64, caughtUp bool) {
	f.t.Helper()
	tx, err := f.osmAdminDB.Begin(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	defer coverageIntegrationRollback(tx)
	if caughtUp {
		// Restore the exact promotion event before activation so the trigger does
		// not create a different event while simulating replay catch-up.
		f.exec(tx, `INSERT INTO osm_catalog.promotion_events(id,region_id,generation_id,importer_version,derivation_version,source_sha256,promoted_at)
			OVERRIDING SYSTEM VALUE SELECT $1,region_id,id,importer_version,derivation_version,source_sha256,promoted_at
			FROM osm_catalog.generations WHERE id=$2`, newerEvent, newerGeneration)
	}
	f.exec(tx, `UPDATE osm_catalog.generations SET state='retired',retired_at=clock_timestamp() WHERE region_id=$1 AND state='active'`, f.regionID)
	active := f.generation
	if caughtUp {
		active = newerGeneration
	}
	f.exec(tx, `UPDATE osm_catalog.generations SET state='active',retired_at=NULL WHERE id=$1`, active)
	if !caughtUp {
		f.exec(tx, `DELETE FROM osm_catalog.promotion_events WHERE id=$1`, newerEvent)
	}
	f.commit(tx)
}

func TestCoverageReconcilerReplicaFreshnessIntegration(t *testing.T) {
	for _, afterClaim := range []bool{false, true} {
		name := "global_observation_before_claim"
		if afterClaim {
			name = "other_pod_observation_after_claim"
		}
		t.Run(name, func(t *testing.T) {
			f := newCoverageIntegrationFixture(t)
			var helperOnly bool
			if err := f.coverageDB.QueryRow(f.ctx, `SELECT NOT has_table_privilege(current_user,'app.coverage_reconciliation_state','SELECT')
				AND has_function_privilege(current_user,'app.coverage_reconciliation_observation()','EXECUTE')`).Scan(&helperOnly); err != nil || !helperOnly {
				t.Fatalf("observation must use the granted helper, not direct table SELECT: allowed=%v error=%v", helperOnly, err)
			}
			f.seedOSM()
			if _, err := f.osmAdminDB.Exec(f.ctx, `UPDATE osm_catalog.regions SET configured=true WHERE id=$1`, f.regionID); err != nil {
				t.Fatal(err)
			}
			first, second := f.addWorkout(coverageIntegrationRoute(0)), f.addWorkout(coverageIntegrationRoute(1))
			if first.String() > second.String() {
				first, second = second, first
			}
			r := NewCoverageReconciler(f.coverageDB, f.osmDB, slog.Default(), CoverageReconcilerOptions{UseReadReplicasOnly: true})
			if err := r.syncRegionCatalog(f.ctx); err != nil {
				t.Fatal(err)
			}
			if err := r.observe(f.ctx); err != nil {
				t.Fatal(err)
			}
			var claim reconciliationClaim
			claimCampaign := func() {
				var found bool
				var err error
				claim, found, err = r.claim(f.ctx)
				if err != nil || !found || claim.accountID != f.accountID {
					t.Fatalf("claim=%+v found=%v error=%v", claim, found, err)
				}
				if err := r.advance(f.ctx, claim, &first, false); err != nil {
					t.Fatal(err)
				}
				claim.cursor = &first
			}
			if afterClaim {
				claimCampaign()
			}
			var newerGeneration int64
			if err := f.osmAdminDB.QueryRow(f.ctx, `SELECT nextval(pg_get_serial_sequence('osm_catalog.generations','id'))`).Scan(&newerGeneration); err != nil {
				t.Fatal(err)
			}
			replicaFixtureHead(f, newerGeneration, false)
			newerEvent, err := osm.PromotionEventHead(f.ctx, f.osmDB)
			if err != nil {
				t.Fatal(err)
			}
			// A different coverage pod observes the ahead standby. The route
			// lookup below must use this APP maximum, not r's earlier observation.
			other := NewCoverageReconciler(f.coverageDB, f.osmDB, slog.Default(), CoverageReconcilerOptions{})
			if err := other.observe(f.ctx); err != nil {
				t.Fatal(err)
			}
			if !afterClaim {
				claimCampaign()
				if err := r.failDetached(claim, "fixture-retry", "Fixture retry."); err != nil {
					t.Fatal(err)
				}
			}
			reconcilerFixtureReplay(f, newerGeneration, newerEvent, false)
			makeDue := func() {
				tx := f.accountTx(f.ctx, f.adminDB)
				f.exec(tx, `UPDATE app.coverage_reconciliation_accounts SET next_scan_at=clock_timestamp()-interval '1 second' WHERE account_id=$1`, f.accountID)
				f.commit(tx)
			}
			if afterClaim {
				err = r.process(f.ctx, claim)
				if err != nil {
					if failErr := r.failDetached(claim, "coverage-reconciliation-failed", "Coverage reconciliation could not be completed."); failErr != nil {
						t.Fatal(failErr)
					}
				}
			} else {
				makeDue()
				var worked bool
				worked, err = r.RunOnce(f.ctx)
				if !worked {
					t.Fatal("lagged campaign was not claimed")
				}
			}
			if !errors.Is(err, osm.ErrMatcherReplicaNotReady) {
				t.Fatalf("lagged reconciliation error=%v", err)
			}
			if f.osmDB.Stat().AcquiredConns() != 0 {
				t.Fatal("lagged lookup retained its snapshot")
			}
			tx := f.accountTx(f.ctx, f.adminDB)
			var untouched bool
			f.scan(tx, `SELECT cursor_workout_id=$2 AND completed_revision<active_revision AND active_revision=$3
				AND worker_id IS NULL AND lease_token IS NULL AND last_failure_code='coverage-reconciliation-failed'
				AND next_scan_at BETWEEN clock_timestamp()+interval '4 minutes' AND clock_timestamp()+interval '6 minutes'
				AND NOT EXISTS(SELECT 1 FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route')
				AND (SELECT desired_osm_generation=$5 FROM app.workout_coverage_regions WHERE account_id=$1 AND workout_id=$4 AND region_id=$6)
				AND (SELECT target_job_id IS NULL AND target_generations IS NULL FROM app.workout_coverage_states WHERE account_id=$1 AND workout_id=$4)
				FROM app.coverage_reconciliation_accounts WHERE account_id=$1`,
				[]any{f.accountID, first, claim.revision, second, f.generation, f.regionID}, &untouched)
			f.commit(tx)
			if !untouched {
				t.Fatal("lag advanced/completed the cursor, changed a target, queued a job, or lost the campaign retry")
			}
			f.assertNoSlots()
			// The default resolver remains unfenced on the same older catalog.
			readiness, err := osm.ResolveRouteRegions(f.ctx, f.osmDB, []osm.RouteCoordinate{{Longitude: -122, Latitude: 37}})
			if err != nil || len(readiness.Regions) != 1 || readiness.Regions[0].Generation != f.generation {
				t.Fatalf("default readiness=%+v error=%v", readiness, err)
			}
			for _, points := range [][]osm.RouteCoordinate{nil, {{Longitude: -122, Latitude: 37}}} {
				if _, err := osm.ResolveRouteRegionsAtPromotion(f.ctx, f.osmDB, points, newerEvent); !errors.Is(err, osm.ErrMatcherReplicaNotReady) {
					t.Fatalf("fenced lookup error=%v", err)
				}
			}
			reconcilerFixtureReplay(f, newerGeneration, newerEvent, true)
			makeDue()
			if worked, err := r.RunOnce(f.ctx); err != nil || !worked {
				t.Fatalf("caught-up retry worked=%v error=%v", worked, err)
			}
			tx = f.accountTx(f.ctx, f.adminDB)
			var completed bool
			f.scan(tx, `SELECT completed_revision=desired_revision AND cursor_workout_id IS NULL AND worker_id IS NULL
				AND last_failure_code IS NULL
				AND (SELECT desired_osm_generation=$3 FROM app.workout_coverage_regions WHERE account_id=$1 AND workout_id=$2 AND region_id=$4)
				AND (SELECT target_generations @> jsonb_build_array(jsonb_build_object('regionId',$4::text,'generation',$3::bigint))
				FROM app.workout_coverage_states WHERE account_id=$1 AND workout_id=$2)
				FROM app.coverage_reconciliation_accounts WHERE account_id=$1`, []any{f.accountID, second, newerGeneration, f.regionID}, &completed)
			f.commit(tx)
			if !completed {
				t.Fatal("caught-up retry did not persist the new generation and complete its campaign")
			}
		})
	}
}

type reconcilerSnapshotTrace struct {
	f                           *coverageIntegrationFixture
	newerGeneration, newerEvent int64
	headPID, geometryPID        uint32
	checkouts                   int
	role, isolation, readOnly   string
}

func (t *reconcilerSnapshotTrace) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if data.SQL == `SELECT osm_catalog.promotion_event_head()` {
		t.headPID = conn.PgConn().PID()
	}
	if strings.Contains(data.SQL, "WITH route_point AS") {
		t.geometryPID = conn.PgConn().PID()
		if err := conn.QueryRow(ctx, `SELECT current_user,current_setting('transaction_isolation'),current_setting('transaction_read_only')`).Scan(&t.role, &t.isolation, &t.readOnly); err != nil {
			t.f.t.Fatal(err)
		}
		// Change both tables after the head read but before geometry SQL. A
		// separate connection or READ COMMITTED lookup would now return G1.
		reconcilerFixtureReplay(t.f, t.newerGeneration, t.newerEvent, false)
	}
	return ctx
}

func (*reconcilerSnapshotTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestCoverageReconcilerPromotionSnapshotIntegration(t *testing.T) {
	f := newCoverageIntegrationFixture(t)
	f.seedOSM()
	if _, err := f.osmAdminDB.Exec(f.ctx, `UPDATE osm_catalog.regions SET configured=true WHERE id=$1`, f.regionID); err != nil {
		t.Fatal(err)
	}
	var newerGeneration int64
	if err := f.osmAdminDB.QueryRow(f.ctx, `SELECT nextval(pg_get_serial_sequence('osm_catalog.generations','id'))`).Scan(&newerGeneration); err != nil {
		t.Fatal(err)
	}
	replicaFixtureHead(f, newerGeneration, false)
	newerEvent, err := osm.PromotionEventHead(f.ctx, f.osmDB)
	if err != nil {
		t.Fatal(err)
	}
	trace := &reconcilerSnapshotTrace{f: f, newerGeneration: newerGeneration, newerEvent: newerEvent}
	config := f.osmDB.Config()
	config.ConnConfig.Tracer = trace
	config.PrepareConn = func(context.Context, *pgx.Conn) (bool, error) {
		trace.checkouts++
		return true, nil
	}
	pool, err := pgxpool.NewWithConfig(f.ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	readiness, err := osm.ResolveRouteRegionsAtPromotion(f.ctx, pool, []osm.RouteCoordinate{{Longitude: -122, Latitude: 37}}, newerEvent)
	if err != nil || len(readiness.Regions) != 1 || readiness.Regions[0].Generation != newerGeneration {
		t.Fatalf("snapshot readiness=%+v error=%v", readiness, err)
	}
	if trace.checkouts != 1 || trace.headPID == 0 || trace.headPID != trace.geometryPID ||
		trace.role != "workouts_coverage_worker" || trace.isolation != "repeatable read" || trace.readOnly != "on" {
		t.Fatalf("snapshot checkouts=%d samePID=%v role=%q isolation=%q readOnly=%q", trace.checkouts, trace.headPID == trace.geometryPID, trace.role, trace.isolation, trace.readOnly)
	}
	if pool.Stat().AcquiredConns() != 0 {
		t.Fatal("successful lookup retained its snapshot")
	}
	// Empty routes still establish the fence. Once caught up, they keep the
	// original no-provider readiness behavior rather than running geometry SQL.
	reconcilerFixtureReplay(f, newerGeneration, newerEvent, true)
	readiness, err = osm.ResolveRouteRegionsAtPromotion(f.ctx, pool, nil, newerEvent)
	if err != nil || readiness.State != "unavailable" || readiness.Reason != "no_provider_region" {
		t.Fatalf("empty caught-up readiness=%+v error=%v", readiness, err)
	}
	if _, err := osm.ResolveRouteRegionsAtPromotion(f.ctx, pool, nil, newerEvent-1); err != nil {
		t.Fatalf("ahead snapshot failed minimum fence: %v", err)
	}
}

func TestCoverageReconcilerPromotionDeadlineIntegration(t *testing.T) {
	f := newCoverageIntegrationFixture(t)
	f.seedOSM()
	gate := f.gate(f.osmAdminDB, "osm_catalog", "promotion_events", "ACCESS EXCLUSIVE")
	defer gate.release()
	ctx, cancel := context.WithTimeout(f.ctx, 100*time.Millisecond)
	defer cancel()
	readiness, err := osm.ResolveRouteRegionsAtPromotion(ctx, f.osmDB, nil, 1)
	if !errors.Is(err, context.DeadlineExceeded) || readiness.State != "" {
		t.Fatalf("blocked fence readiness=%+v error=%v", readiness, err)
	}
	// pgxpool asynchronously destroys a connection closed by query cancellation.
	f.wait("deadline snapshot connection released", func(context.Context) (bool, error) {
		return f.osmDB.Stat().AcquiredConns() == 0, nil
	})
}
