package worker

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCoverageMetadataNewCampaignSurvivesOldCompletionIntegration(t *testing.T) {
	f, producer, source, keys := newCoveragePipelineFixture(t)
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 2)
	enqueueIngestJob(t, f.adminDB, keys, source)
	assertRanIngest(t, producer, f.ctx)
	r := NewCoverageReconciler(f.coverageDB, f.osmDB, producer.logger, CoverageReconcilerOptions{})
	if _, err := r.RunOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	r.minimumTraversal = 6
	if err := r.observe(f.ctx); err != nil {
		t.Fatal(err)
	}
	claim, found, err := r.claim(f.ctx)
	if err != nil || !found || !claim.scanRequired {
		t.Fatalf("claim N: %+v %t %v", claim, found, err)
	}
	routes, err := r.readRoutes(f.ctx, claim, nil)
	if err != nil || len(routes) != 2 {
		t.Fatalf("campaign routes: %d %v", len(routes), err)
	}
	unauthorized, err := beginAccount(f.ctx, f.coverageDB, f.accountID)
	if err != nil {
		t.Fatal(err)
	}
	var ignored bool
	err = unauthorized.QueryRow(f.ctx, `SELECT app.defer_coverage_reconciliation_route($1,$2,$3,$4,$5,$6,$7,1)`,
		f.accountID, claim.revision, r.workerID, uuid.New(), routes[0].workoutID, routes[0].revision, routes[0].digest).Scan(&ignored)
	_ = unauthorized.Rollback(f.ctx)
	var authority interface{ SQLState() string }
	if !errors.As(err, &authority) || authority.SQLState() != "42501" {
		t.Fatalf("invalid lease deferred a route: %v", err)
	}
	for _, route := range routes {
		if err := r.reconcileRoute(f.ctx, claim, route); err != nil {
			t.Fatal(err)
		}
		if err := r.advance(f.ctx, claim, &route.workoutID, false); err != nil {
			t.Fatal(err)
		}
	}
	last := routes[len(routes)-1].workoutID
	if _, err := f.osmAdminDB.Exec(f.ctx, `UPDATE osm_catalog.regions SET boundary=ST_Multi(ST_Buffer(boundary,0.001)) WHERE id=$1`, f.regionID); err != nil {
		t.Fatal(err)
	}
	if err := r.observe(f.ctx); err != nil {
		t.Fatal(err)
	}
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.coverage_reconciliation_accounts WHERE account_id=$1
		AND desired_revision>$2 AND cursor_workout_id=$3 AND next_scan_at<=clock_timestamp()`, f.accountID, claim.revision, last)
	if err := r.advance(f.ctx, claim, &last, true); err != nil {
		t.Fatal(err)
	}
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.coverage_reconciliation_accounts WHERE account_id=$1
		AND completed_revision=$2 AND desired_revision>$2 AND next_scan_at<=clock_timestamp()`, f.accountID, claim.revision)
	next, found, err := r.claim(f.ctx)
	if err != nil || !found || !next.scanRequired || next.revision <= claim.revision || next.cursor != nil {
		t.Fatalf("claim N+1: %+v %t %v", next, found, err)
	}
	if err := r.process(f.ctx, next); err != nil {
		t.Fatal(err)
	}
	// The low UUID was behind N's cursor when N+1 invalidated its proof.
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.coverage_route_selection_proofs proof CROSS JOIN app.coverage_reconciliation_state catalog
		WHERE proof.account_id=$1 AND proof.workout_id=$2 AND proof.selection_fingerprint=catalog.selection_fingerprint
		AND proof.catalog_revision=catalog.catalog_revision`, f.accountID, routes[0].workoutID)
}

func TestCoverageMetadataLateSelectionPolicyRejectedIntegration(t *testing.T) {
	f, producer, source, keys := newCoveragePipelineFixture(t)
	if _, err := f.osmAdminDB.Exec(f.ctx, `UPDATE osm_catalog.generations SET state='retired',retired_at=clock_timestamp() WHERE id=$1`, f.generation); err != nil {
		t.Fatal(err)
	}
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 1)
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-02.json"), 1, 1)
	jobs := enqueueIngestJob(t, f.adminDB, keys, source)
	job, err := producer.claimNext(f.ctx)
	if err != nil || job.id != jobs.childID {
		t.Fatalf("claim: %v", err)
	}
	commitEligibleCohortFile(t, f, producer, job, source, 0)
	if err := producer.drainIngestCoverage(f.ctx, job, false); err != nil {
		t.Fatal(err)
	}
	r := NewCoverageReconciler(f.coverageDB, f.osmDB, producer.logger, CoverageReconcilerOptions{OSMAutoAddRegions: true})
	if _, err := r.RunOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.coverage_reconciliation_accounts WHERE account_id=$1 AND completed_revision=desired_revision`, f.accountID)
	commitEligibleCohortFile(t, f, producer, job, source, 1)
	err = producer.drainIngestCoverage(f.ctx, job, false)
	var state interface{ SQLState() string }
	if !errors.As(err, &state) || state.SQLState() != "40001" {
		t.Fatalf("late auto-add=false producer must fail policy CAS: %v", err)
	}
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.ingest_coverage_handoffs WHERE account_id=$1 AND acknowledged_at IS NULL`, f.accountID)
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.coverage_route_changes change JOIN app.workouts workout ON workout.id=change.workout_id
		WHERE change.account_id=$1 AND workout.provider_id='pipeline-synthetic-1' AND change.mutation_version>change.resolved_version
		AND change.next_attempt_at<=clock_timestamp()`, f.accountID)
	assertCountQuery(t, f.adminDB, 0, `SELECT count(*) FROM app.coverage_route_selection_proofs WHERE account_id=$1 AND selection_policy=$2`, f.accountID, coverageSelectionPolicy(false))
	// A newer real OSM snapshot is still admissible when the accepted policy agrees.
	if _, err := f.osmAdminDB.Exec(f.ctx, `UPDATE osm_catalog.regions SET display_name=display_name||' newer' WHERE id=$1`, f.regionID); err != nil {
		t.Fatal(err)
	}
	producer.osmAutoAddRegions = true
	if err := producer.drainIngestCoverage(f.ctx, job, false); err != nil {
		t.Fatal(err)
	}
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.coverage_route_selection_proofs proof JOIN app.workouts workout ON workout.id=proof.workout_id
		CROSS JOIN app.coverage_reconciliation_state catalog WHERE proof.account_id=$1 AND workout.provider_id='pipeline-synthetic-1'
		AND proof.catalog_revision>catalog.catalog_revision AND proof.selection_policy=catalog.selection_policy AND proof.readiness='pending'`, f.accountID)
}

func TestCoverageMetadataRouteFailureIsolationIntegration(t *testing.T) {
	for _, mode := range []string{"queue", "campaign", "historical change row", "concurrent mutation", "parent cancellation", "metadata permission"} {
		t.Run(mode, func(t *testing.T) {
			f, producer, source, keys := newCoveragePipelineFixture(t)
			if err := ConfigureCoverageBatchSize(f.ctx, f.workerDB, 1); err != nil {
				t.Fatal(err)
			}
			writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 3)
			jobs := enqueueIngestJob(t, f.adminDB, keys, source)
			job, err := producer.claimNext(f.ctx)
			if err != nil || job.id != jobs.childID {
				t.Fatalf("claim: %v", err)
			}
			commitEligibleCohortFile(t, f, producer, job, source, 0)
			if err := producer.drainIngestCoverage(f.ctx, job, false); err != nil {
				t.Fatal(err)
			}
			// Preserve actual terminal-failure targets. Revalidating selection must
			// not turn route-local recovery into an alternative matching retry.
			matcher := NewCoverageRunner(f.coverageDB, f.osmDB, producer.logger)
			for range 3 {
				claim, found, err := matcher.claim(f.ctx)
				if err != nil || !found {
					t.Fatalf("matching claim: %t %v", found, err)
				}
				if err := matcher.failDetached(claim, time.Now(), "coverage-route-invalid", "Synthetic route failure."); err != nil {
					t.Fatal(err)
				}
			}
			r := NewCoverageReconciler(f.coverageDB, f.osmDB, producer.logger, CoverageReconcilerOptions{})
			if _, err := r.RunOnce(f.ctx); err != nil {
				t.Fatal(err)
			}
			var low uuid.UUID
			if err := f.adminDB.QueryRow(f.ctx, `SELECT min(workout_id::text)::uuid FROM app.coverage_route_selection_proofs WHERE account_id=$1`, f.accountID).Scan(&low); err != nil {
				t.Fatal(err)
			}
			if _, err := f.adminDB.Exec(f.ctx, `DELETE FROM app.coverage_route_selection_proofs WHERE account_id=$1`, f.accountID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.adminDB.Exec(f.ctx, `UPDATE app.coverage_route_changes SET resolved_version=0,next_attempt_at='-infinity' WHERE account_id=$1`, f.accountID); err != nil {
				t.Fatal(err)
			}
			if _, err := f.adminDB.Exec(f.ctx, `UPDATE app.coverage_reconciliation_accounts SET queue_retry_at='-infinity' WHERE account_id=$1`, f.accountID); err != nil {
				t.Fatal(err)
			}
			if mode == "historical change row" {
				if _, err := f.adminDB.Exec(f.ctx, `DELETE FROM app.coverage_route_changes WHERE account_id=$1 AND workout_id=$2`, f.accountID, low); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "campaign" || mode == "historical change row" {
				if _, err := f.osmAdminDB.Exec(f.ctx, `UPDATE osm_catalog.regions SET boundary=ST_Multi(ST_Buffer(boundary,0.001)) WHERE id=$1`, f.regionID); err != nil {
					t.Fatal(err)
				}
				if err := r.observe(f.ctx); err != nil {
					t.Fatal(err)
				}
			}
			r.roiTimeout = 100 * time.Millisecond
			ctx, cancel := context.WithCancel(f.ctx)
			defer cancel()
			var first atomic.Bool
			var lookups atomic.Int64
			cfg := f.osmDB.Config()
			cfg.ConnConfig.Tracer = pipelineQueryTrace(func(queryCtx context.Context, q pgx.TraceQueryStartData) context.Context {
				if !strings.Contains(q.SQL, "WITH route_point AS") {
					return queryCtx
				}
				lookups.Add(1)
				if first.CompareAndSwap(false, true) {
					if mode == "parent cancellation" {
						cancel()
						return queryCtx
					}
					if mode == "concurrent mutation" {
						tx, _, err := producer.beginIngestWrite(f.ctx, job)
						if err != nil {
							t.Fatal(err)
						}
						if _, err := tx.Exec(f.ctx, `UPDATE app.workout_route_points SET horizontal_accuracy=COALESCE(horizontal_accuracy,0)+1 WHERE account_id=$1 AND workout_id=$2`, f.accountID, low); err != nil {
							t.Fatal(err)
						}
						if err := tx.Commit(f.ctx); err != nil {
							t.Fatal(err)
						}
					}
					time.Sleep(2 * r.roiTimeout)
				}
				return queryCtx
			})
			reader, err := pgxpool.NewWithConfig(f.ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Close()
			r.osmDB = reader
			if mode == "metadata permission" {
				if _, err := f.osmAdminDB.Exec(f.ctx, `REVOKE SELECT ON osm_catalog.route_region_catalog_state FROM workouts_coverage_worker`); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if _, err := f.osmAdminDB.Exec(f.ctx, `GRANT SELECT ON osm_catalog.route_region_catalog_state TO workouts_coverage_worker`); err != nil {
						t.Error(err)
					}
				}()
			}
			worked, err := r.RunOnce(ctx)
			if mode == "parent cancellation" || mode == "metadata permission" {
				if !worked || err == nil {
					t.Fatalf("account-fatal failure was hidden: %t %v", worked, err)
				}
				assertCountQuery(t, f.adminDB, 0, `SELECT count(*) FROM app.coverage_route_changes WHERE account_id=$1 AND next_attempt_at>clock_timestamp()`, f.accountID)
				return
			}
			if err != nil || !worked {
				t.Fatalf("route-local failure starved healthy routes: %t %v", worked, err)
			}
			assertCountQuery(t, f.adminDB, 2, `SELECT count(*) FROM app.coverage_route_selection_proofs WHERE account_id=$1 AND workout_id<>$2`, f.accountID, low)
			assertCountQuery(t, f.adminDB, 3, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route' AND status='failed'`, f.accountID)
			assertCountQuery(t, f.adminDB, 3, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route'`, f.accountID)
			if mode == "concurrent mutation" {
				assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.coverage_route_changes WHERE account_id=$1 AND workout_id=$2
					AND mutation_version>resolved_version AND next_attempt_at<=clock_timestamp()`, f.accountID, low)
				before := lookups.Load()
				if again, err := r.RunOnce(f.ctx); err != nil || !again || lookups.Load() != before+1 {
					t.Fatalf("new mutation wake was hidden by old deferral: %t %v ROI=%d", again, err, lookups.Load())
				}
				for range 3 {
					if again, err := r.RunOnce(f.ctx); err != nil || again || lookups.Load() != before+1 {
						t.Fatalf("healthy mutation recovery repeated ROI: %t %v", again, err)
					}
				}
				return
			}
			assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.coverage_route_changes WHERE account_id=$1 AND workout_id=$2
				AND mutation_version>resolved_version AND next_attempt_at BETWEEN clock_timestamp()+interval '4 minutes' AND clock_timestamp()+interval '6 minutes'`, f.accountID, low)
			before := lookups.Load()
			if again, err := r.RunOnce(f.ctx); err != nil || again || lookups.Load() != before {
				t.Fatalf("deferred key caused immediate ordinary/queue work: %t %v ROI=%d", again, err, lookups.Load())
			}
			if _, err := f.adminDB.Exec(f.ctx, `UPDATE app.coverage_route_changes SET next_attempt_at='-infinity' WHERE account_id=$1 AND workout_id=$2`, f.accountID, low); err != nil {
				t.Fatal(err)
			}
			if _, err := f.adminDB.Exec(f.ctx, `UPDATE app.coverage_reconciliation_accounts SET queue_retry_at='-infinity' WHERE account_id=$1`, f.accountID); err != nil {
				t.Fatal(err)
			}
			if again, err := r.RunOnce(f.ctx); err != nil || !again || lookups.Load() != before+1 {
				t.Fatalf("retained route obligation did not recover exactly once: %t %v ROI=%d", again, err, lookups.Load())
			}
			for range 3 {
				if again, err := r.RunOnce(f.ctx); err != nil || again || lookups.Load() != before+1 {
					t.Fatalf("healthy idle repeated ROI: %t %v", again, err)
				}
			}
			assertCountQuery(t, f.adminDB, 3, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route' AND status='failed'`, f.accountID)
		})
	}
}
