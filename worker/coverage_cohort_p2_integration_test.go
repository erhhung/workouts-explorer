package worker

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/erhhung/workouts-explorer/internal/sourceconfig"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const cohortSmallBoundary = "POLYGON((-122.01 36.99,-121.99 36.99,-121.99 37.01,-122.01 37.01,-122.01 36.99))"

func TestEligibleCohortCatalogChangeRefillsFullCohortIntegration(t *testing.T) {
	f, producer, source, keys := newCoveragePipelineFixture(t)
	if err := ConfigureCoverageBatchSize(f.ctx, f.workerDB, 25); err != nil {
		t.Fatal(err)
	}
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 13)
	writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-02.json"), 13, 12)
	jobs := enqueueIngestJob(t, f.adminDB, keys, source)
	job, err := producer.claimNext(f.ctx)
	if err != nil || job.id != jobs.childID {
		t.Fatalf("claim: %v", err)
	}
	commitEligibleCohortFile(t, f, producer, job, source, 0)
	if err := producer.drainIngestCoverage(f.ctx, job, false); err != nil {
		t.Fatal(err)
	}
	assertCountQuery(t, f.adminDB, 13, `SELECT count(*) FROM app.ingest_coverage_prepared WHERE account_id=$1`, f.accountID)
	if sizes := pipelineParentSizes(t, f); len(sizes) != 0 {
		t.Fatalf("underfull cache created work: %v", sizes)
	}
	region, generation := f.seedOSM()
	if _, err := f.osmAdminDB.Exec(f.ctx, `UPDATE osm_catalog.regions SET configured=true,boundary=ST_Multi(ST_GeomFromText($2,4326)) WHERE id=$1`, region, cohortSmallBoundary); err != nil {
		t.Fatal(err)
	}
	assertCountQuery(t, f.osmAdminDB, 1, `SELECT count(*) FROM osm_catalog.generations WHERE id=$1 AND state='active'`, f.generation)
	commitEligibleCohortFile(t, f, producer, job, source, 1)
	var lookups atomic.Int64
	producer.osmDB = traceEligibleCohortOSM(t, f, producer.db, &lookups)
	if err := producer.drainIngestCoverage(f.ctx, job, false); err != nil {
		t.Fatal(err)
	}
	// Twelve fresh routes plus precisely one recomputation of thirteen cached
	// routes. No terminal root or later ordinary sweep can hide a stale seal.
	if sizes := pipelineParentSizes(t, f); !slices.Equal(sizes, []int{25}) || lookups.Load() != 25 {
		t.Fatalf("catalogue re-fill sizes=%v ROI=%d, want one25 and12+13 lookups", sizes, lookups.Load())
	}
	assertCountQuery(t, f.adminDB, 25, `SELECT count(*) FROM app.coverage_route_job_contexts WHERE account_id=$1 AND target_generations=jsonb_build_array(jsonb_build_object('regionId',$2::text,'generation',$3::bigint))`, f.accountID, region, generation)
	assertCountQuery(t, f.adminDB, 0, `SELECT count(*) FROM app.ingest_coverage_prepared WHERE account_id=$1`, f.accountID)
	assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.jobs WHERE id=$1 AND status='running'`, jobs.parentID)
}

func TestEligibleCohortCatalogLookupSnapshotIntegration(t *testing.T) {
	f, _, _, _ := newCoveragePipelineFixture(t)
	region, _ := f.seedOSM()
	if _, err := f.osmAdminDB.Exec(f.ctx, `UPDATE osm_catalog.regions SET boundary=ST_Multi(ST_GeomFromText($2,4326)) WHERE id=$1`, region, cohortSmallBoundary); err != nil {
		t.Fatal(err)
	}
	before, err := osm.ReadRouteRegionCatalog(f.ctx, f.osmDB, 0)
	if err != nil {
		t.Fatal(err)
	}
	var changed atomic.Bool
	config := f.osmDB.Config()
	config.ConnConfig.Tracer = pipelineQueryTrace(func(ctx context.Context, query pgx.TraceQueryStartData) context.Context {
		if strings.Contains(query.SQL, "route_point AS") && changed.CompareAndSwap(false, true) {
			// The fingerprint read has occurred. Commit a conflicting catalogue
			// before ROI executes; only the same RR snapshot may still choose A.
			if _, err := f.osmAdminDB.Exec(f.ctx, `UPDATE osm_catalog.regions SET configured=true WHERE id=$1`, region); err != nil {
				t.Fatal(err)
			}
		}
		return ctx
	})
	reader, err := pgxpool.NewWithConfig(f.ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.Close)
	points := []osm.RouteCoordinate{{Longitude: -122, Latitude: 37}, {Longitude: -121.999, Latitude: 37}}
	readiness, err := osm.ResolveRouteRegionsWithCatalog(f.ctx, reader, points, 0)
	if err != nil || !changed.Load() || readiness.State != "map_data_ready" || readiness.CatalogFingerprint != before.Fingerprint || len(readiness.Regions) != 1 || readiness.Regions[0].RegionID != f.regionID {
		t.Fatalf("catalogue and ROI did not share old RR snapshot: readiness=%+v changed=%t err=%v", readiness, changed.Load(), err)
	}
	after, err := osm.ReadRouteRegionCatalog(f.ctx, f.osmDB, 0)
	if err != nil || after.Fingerprint == before.Fingerprint {
		t.Fatalf("committed catalogue change invisible: %v", err)
	}
	current, err := osm.ResolveRouteRegionsWithCatalog(f.ctx, f.osmDB, points, 0)
	if err != nil || current.CatalogFingerprint != after.Fingerprint || len(current.Regions) != 1 || current.Regions[0].RegionID != region {
		t.Fatalf("new snapshot did not choose smaller configured B: %+v %v", current, err)
	}
}

func TestEligibleCohortCatalogChangeInvalidatesPreparedIntegration(t *testing.T) {
	for _, change := range []string{"new smaller region", "configure existing region", "move existing boundary", "shrink existing boundary", "unconfigured head"} {
		t.Run(change, func(t *testing.T) {
			f, producer, source, keys := newCoveragePipelineFixture(t)
			if err := ConfigureCoverageBatchSize(f.ctx, f.workerDB, 25); err != nil {
				t.Fatal(err)
			}
			var region string
			var generation int64
			if change != "new smaller region" {
				region, generation = f.seedOSM()
				switch change {
				case "configure existing region":
					if _, err := f.osmAdminDB.Exec(f.ctx, `UPDATE osm_catalog.regions SET boundary=ST_Multi(ST_GeomFromText($2,4326)) WHERE id=$1`, region, cohortSmallBoundary); err != nil {
						t.Fatal(err)
					}
				case "move existing boundary":
					if _, err := f.osmAdminDB.Exec(f.ctx, `UPDATE osm_catalog.regions SET configured=true,boundary=ST_Translate(boundary,20,0) WHERE id=$1`, region); err != nil {
						t.Fatal(err)
					}
				case "shrink existing boundary":
					if _, err := f.osmAdminDB.Exec(f.ctx, `UPDATE osm_catalog.regions SET configured=true,boundary=ST_Multi(ST_GeomFromText('POLYGON((-124 35,-120 35,-120 39,-124 39,-124 35))',4326)) WHERE id=$1`, region); err != nil {
						t.Fatal(err)
					}
				}
			}
			writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 13)
			jobs := enqueueIngestJob(t, f.adminDB, keys, source)
			job, err := producer.claimNext(f.ctx)
			if err != nil || job.id != jobs.childID {
				t.Fatalf("claim: %v", err)
			}
			commitEligibleCohortFile(t, f, producer, job, source, 0)
			if err := producer.drainIngestCoverage(f.ctx, job, false); err != nil {
				t.Fatal(err)
			}
			var stored string
			if err := f.adminDB.QueryRow(f.ctx, `SELECT min(encode(catalog_fingerprint,'hex')) FROM app.ingest_coverage_prepared WHERE account_id=$1`, f.accountID).Scan(&stored); err != nil || len(stored) != 64 {
				t.Fatalf("prepared proof missing: %q %v", stored, err)
			}
			assertCountQuery(t, f.adminDB, 13, `SELECT count(*) FROM app.ingest_coverage_prepared WHERE account_id=$1 AND generations=jsonb_build_array(jsonb_build_object('regionId',$2::text,'generation',$3::bigint))`, f.accountID, f.regionID, f.generation)
			wantRegion, wantGeneration := region, generation
			if change == "new smaller region" {
				wantRegion, wantGeneration = f.seedOSM()
			}
			if change == "unconfigured head" {
				if _, err := f.osmAdminDB.Exec(f.ctx, `UPDATE osm_catalog.generations SET state='retired',retired_at=clock_timestamp() WHERE id=$1`, generation); err != nil {
					t.Fatal(err)
				}
				wantRegion, wantGeneration = f.regionID, f.generation
			} else if _, err := f.osmAdminDB.Exec(f.ctx, `UPDATE osm_catalog.regions SET configured=true,boundary=ST_Multi(ST_GeomFromText($2,4326)) WHERE id=$1`, wantRegion, cohortSmallBoundary); err != nil {
				t.Fatal(err)
			}
			assertCountQuery(t, f.osmAdminDB, 1, `SELECT count(*) FROM osm_catalog.generations WHERE id=$1 AND state='active'`, f.generation)
			current, err := osm.ReadRouteRegionCatalog(f.ctx, f.osmDB, 0)
			if err != nil || current.Fingerprint == stored {
				t.Fatalf("full-catalogue proof ignored %s: %v", change, err)
			}
			if finished, err := producer.finishJob(f.ctx, job, "succeeded", nil); err != nil || !finished {
				t.Fatalf("finish: %v", err)
			}
			var lookups atomic.Int64
			appDB, osmDB, ordinary, snapshots := traceEligibleCohortRecovery(t, f, &lookups)
			recovery := NewCoverageReconciler(appDB, osmDB, producer.logger, CoverageReconcilerOptions{})
			if worked, err := recovery.RunOnce(f.ctx); err != nil || !worked {
				t.Fatalf("catalogue-cut recovery: %v", err)
			}
			if sizes := pipelineParentSizes(t, f); !slices.Equal(sizes, []int{13}) || lookups.Load() != 13 || snapshots.Load() != 13 || ordinary.Load() != 13 {
				t.Fatalf("cached ROI was sealed or ordinary sweep hid stale first seal: sizes=%v cohortROI=%d snapshots=%d ordinary=%d", sizes, lookups.Load(), snapshots.Load(), ordinary.Load())
			}
			assertCountQuery(t, f.adminDB, 13, `SELECT count(*) FROM app.coverage_route_job_contexts WHERE account_id=$1 AND target_generations=jsonb_build_array(jsonb_build_object('regionId',$2::text,'generation',$3::bigint))`, f.accountID, wantRegion, wantGeneration)
		})
	}
}

func TestEligibleCohortCancelledFootprintMetadataIntegration(t *testing.T) {
	for _, cancellation := range []string{"queued parent", "queued child", "claimed without request"} {
		t.Run(cancellation, func(t *testing.T) {
			f, producer, source, keys := newCoveragePipelineFixture(t)
			if err := ConfigureCoverageBatchSize(f.ctx, f.workerDB, 1); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(source.root, "HealthAutoExport-2026-08-01.json")
			writePipelineExport(t, path, 0, 1)
			enqueueIngestJob(t, f.adminDB, keys, source)
			assertRanIngest(t, producer, f.ctx)
			var child, parent, workout uuid.UUID
			if err := f.adminDB.QueryRow(f.ctx, `SELECT child.id,child.parent_job_id,context.workout_id FROM app.jobs child JOIN app.coverage_route_job_contexts context ON context.job_id=child.id WHERE child.account_id=$1`, f.accountID).Scan(&child, &parent, &workout); err != nil {
				t.Fatal(err)
			}
			if cancellation == "claimed without request" {
				matcher := NewCoverageRunner(f.coverageDB, f.osmDB, producer.logger)
				claim, found, err := matcher.claim(f.ctx)
				if err != nil || !found || claim.jobID != child {
					t.Fatalf("claim before cancellation: %v", err)
				}
				if _, err := matcher.handleInterruption(claim, errCancellationRequested); err != nil {
					t.Fatal(err)
				}
				assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.jobs WHERE id=$1 AND status='cancelled' AND cancel_requested_at IS NULL`, child)
			} else {
				target := child
				if cancellation == "queued parent" {
					target = parent
				}
				if !workerCallBool(t, f.adminDB, f.accountID, `SELECT app.request_job_cancellation($1,$2)`, target, uuid.New()) {
					t.Fatal("cancel actual coverage target")
				}
			}
			// Retain 027's exact cleared-pointer cancellation footprint. This
			// historical fixture shape is not an arbitrary NULL target marker.
			tx := f.accountTx(f.ctx, f.adminDB)
			f.exec(tx, `UPDATE app.workout_coverage_states state SET target_job_id=NULL,target_revision=NULL,target_rules_version=NULL,
			 target_sampling_version=NULL,target_path_policy_version=NULL,target_generations=NULL,processing_state='not_started',processing_finished_at=job.terminal_at
			 FROM app.jobs job WHERE state.workout_id=$1 AND job.id=$2`, workout, child)
			f.commit(tx)
			encoded, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(strings.Replace(string(encoded), `"duration":120`, `"duration":121`, 1)), 0600); err != nil {
				t.Fatal(err)
			}
			updated := enqueueIngestJob(t, f.adminDB, keys, source)
			assertRanIngest(t, producer, f.ctx)
			assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update'`, f.accountID)
			assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.ingest_coverage_handoffs WHERE account_id=$1 AND ingest_job_id=$2 AND outcome='existing' AND coverage_child_job_id=$3`, f.accountID, updated.childID, child)
			assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.workout_coverage_states WHERE workout_id=$1 AND target_job_id IS NULL AND processing_state IN ('not_started','stale') AND route_input_revision=1`, workout)
			assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.job_progress WHERE job_id=$1 AND workouts_updated=1 AND workouts_updated_with_route=1`, updated.childID)
			if !workerCallBool(t, f.adminDB, f.accountID, `SELECT app.delete_source($1,$2)`, source.sourceID, uuid.New()) {
				t.Fatal("delete prior owner")
			}
			incoming := insertAdoptionSource(t, f.adminDB, keys, f.accountID, sourceconfig.LocalType, source.root)
			adopted := enqueueAdoptionJob(t, f.adminDB, keys, incoming, sourceconfig.LocalType)
			assertRanIngest(t, producer, f.ctx)
			assertCountQuery(t, f.adminDB, 2, `SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update'`, f.accountID)
			assertCountQuery(t, f.adminDB, 1, `SELECT count(*) FROM app.adopted_coverage_handoff_receipts WHERE account_id=$1 AND ingest_job_id=$2 AND acknowledged_at IS NOT NULL AND coverage_child_job_id<>$3`, f.accountID, adopted.childID, child)
		})
	}
}

func TestEligibleCohortPreparationFinalizationCancellationIntegration(t *testing.T) {
	for _, change := range []string{"cancel requested then terminal", "source deleted", "ownership transferred"} {
		t.Run(change, func(t *testing.T) {
			f, producer, source, keys := newCoveragePipelineFixture(t)
			if err := ConfigureCoverageBatchSize(f.ctx, f.workerDB, 25); err != nil {
				t.Fatal(err)
			}
			writePipelineExport(t, filepath.Join(source.root, "HealthAutoExport-2026-08-01.json"), 0, 13)
			jobs := enqueueIngestJob(t, f.adminDB, keys, source)
			job, err := producer.claimNext(f.ctx)
			if err != nil || job.id != jobs.childID {
				t.Fatalf("claim: %v", err)
			}
			commitEligibleCohortFile(t, f, producer, job, source, 0)
			if err := producer.drainIngestCoverage(f.ctx, job, false); err != nil {
				t.Fatal(err)
			}
			assertCountQuery(t, f.adminDB, 13, `SELECT count(*) FROM app.ingest_coverage_prepared WHERE account_id=$1`, f.accountID)
			if change == "cancel requested then terminal" {
				if !workerCallBool(t, f.adminDB, f.accountID, `SELECT app.request_job_cancellation($1,$2)`, jobs.parentID, uuid.New()) {
					t.Fatal("request source-root cancellation")
				}
				recovery := NewCoverageReconciler(f.coverageDB, f.osmDB, producer.logger, CoverageReconcilerOptions{})
				if _, err := recovery.RunOnce(f.ctx); err != nil {
					t.Fatal(err)
				}
				if sizes := pipelineParentSizes(t, f); len(sizes) != 0 {
					t.Fatalf("cancellation request authorized early tail: %v", sizes)
				}
				assertCountQuery(t, f.adminDB, 13, `SELECT count(*) FROM app.ingest_coverage_prepared WHERE account_id=$1`, f.accountID)
			} else if !workerCallBool(t, f.adminDB, f.accountID, `SELECT app.delete_source($1,$2)`, source.sourceID, uuid.New()) {
				t.Fatal("delete prepared source")
			}
			if finished, err := producer.finishCancelled(f.ctx, job); err != nil || !finished {
				t.Fatalf("cancelled source terminal commit: %v", err)
			}
			if change == "ownership transferred" {
				incoming := insertAdoptionSource(t, f.adminDB, keys, f.accountID, sourceconfig.LocalType, source.root)
				otherJobs := enqueueAdoptionJob(t, f.adminDB, keys, incoming, sourceconfig.LocalType)
				other := testRunner(f.workerDB, keys, source.root, io.Discard)
				other.osmDB = f.osmDB
				claim, err := other.claimNext(f.ctx)
				if err != nil || claim.id != otherJobs.childID {
					t.Fatalf("replacement owner claim: %v", err)
				}
				commitEligibleCohortFile(t, f, other, claim, incoming, 0)
				assertCountQuery(t, f.adminDB, 13, `SELECT count(*) FROM app.workouts WHERE account_id=$1 AND source_id=$2`, f.accountID, incoming.sourceID)
			}
			pipelineRecoverTerminalRoot(t, f, producer, jobs.parentID)
			if change == "cancel requested then terminal" {
				if sizes := pipelineParentSizes(t, f); !slices.Equal(sizes, []int{13}) {
					t.Fatalf("committed ordinary data lost after terminal cancellation: %v", sizes)
				}
			} else {
				if sizes := pipelineParentSizes(t, f); len(sizes) != 0 {
					t.Fatalf("lost source ownership created matching jobs: %v", sizes)
				}
				outcome := "deleted"
				if change == "ownership transferred" {
					outcome = "superseded"
				}
				assertCountQuery(t, f.adminDB, 13, `SELECT count(*) FROM app.ingest_coverage_handoffs WHERE account_id=$1 AND source_id=$2 AND acknowledged_at IS NOT NULL AND outcome=$3 AND coverage_child_job_id IS NULL`, f.accountID, source.sourceID, outcome)
				if change == "ownership transferred" {
					assertCountQuery(t, f.adminDB, 13, `SELECT count(*) FROM app.ingest_coverage_handoffs WHERE account_id=$1 AND origin='adopted' AND acknowledged_at IS NULL`, f.accountID)
				}
			}
			assertCountQuery(t, f.adminDB, 0, `SELECT count(*) FROM app.ingest_coverage_prepared WHERE account_id=$1`, f.accountID)
		})
	}
}
