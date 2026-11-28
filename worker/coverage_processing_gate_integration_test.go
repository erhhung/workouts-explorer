package worker

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestCoveragePolicyWaitsForUpdatedMapPromotionIntegration(t *testing.T) {
	f := newCoverageIntegrationFixture(t)
	f.seedOSM()
	workout := f.addWorkout(coverageIntegrationRoute(0))
	legacy := f.queuePolicy(workout, coverage.ExperimentalPathPolicyV83)
	f.run()
	f.assertTerminal(legacy, "succeeded", "applied")
	old := f.visible()
	premature := f.queuePolicy(workout, coverage.ExperimentalPathPolicyV84)
	// Reproduce a queue admitted before processing prerequisites were enforced.
	// Only this disposable fixture changes a declared version in place.
	if _, err := f.osmAdminDB.Exec(f.ctx, `UPDATE osm_catalog.generations SET importer_version=3 WHERE id=$1`, f.generation); err != nil {
		t.Fatal(err)
	}
	if _, err := f.adminDB.Exec(f.ctx, `UPDATE app.coverage_map_processing_versions SET importer_version=3 WHERE region_id=$1`, f.regionID); err != nil {
		t.Fatal(err)
	}
	if worked, err := f.runner().RunOnce(f.ctx); err != nil || worked {
		t.Fatalf("new policy consumed an old map: worked=%t error=%v", worked, err)
	}
	if f.visible() != old {
		t.Fatal("waiting for updated maps altered durable Coverage")
	}
	blockedTx := f.accountTx(f.ctx, f.adminDB)
	targets, _ := json.Marshal([]map[string]any{{"jobId": uuid.New(), "workoutId": workout, "routeRevision": 1,
		"generations": []coverageTargetGeneration{{f.regionID, f.generation}}}})
	_, admissionErr := blockedTx.Exec(f.ctx, `SELECT * FROM app.enqueue_adopted_coverage_update($1,$2,$3,$4,1,$5,$6,$7,5,$8)`,
		f.accountID, uuid.New(), f.regionID, f.generation, string(coverage.ExperimentalRulesV1), string(coverage.ExperimentalSamplingV1), string(coverage.ExperimentalPathPolicyV84), targets)
	coverageIntegrationRollback(blockedTx)
	var gateError *pgconn.PgError
	if !errors.As(admissionErr, &gateError) || gateError.Code != "40001" {
		t.Fatalf("direct admission bypassed map prerequisites: %v", admissionErr)
	}
	r := NewCoverageReconciler(f.coverageDB, f.osmDB, slog.New(slog.NewTextHandler(io.Discard, nil)), CoverageReconcilerOptions{OSMAutoAddRegions: true})
	if worked, err := r.RunOnce(f.ctx); err != nil || !worked {
		t.Fatalf("reconcile processing prerequisites: %t %v", worked, err)
	}
	tx := f.accountTx(f.ctx, f.adminDB)
	var waiting bool
	f.scan(tx, `SELECT readiness_state='pending' AND reason='region_reprocessing_required'
        AND target_job_id IS NULL AND processing_state='stale' AND applied_path_policy_version=$2
        FROM app.workout_coverage_states WHERE workout_id=$1`, []any{workout, string(coverage.ExperimentalPathPolicyV83)}, &waiting)
	f.commit(tx)
	if !waiting || f.visible() != old {
		t.Fatal("outdated-map wait erased/relabelled the prior applied result")
	}
	f.assertTerminal(premature, "canceled", "")
	if worked, err := f.runner().RunOnce(f.ctx); err != nil || worked {
		t.Fatalf("waiting route recreated physical work: %t %v", worked, err)
	}

	// A compatible building candidate is insufficient: it must actually promote.
	next := f.generation + 1000000
	hash := sha256.Sum256([]byte("compatible replacement"))
	osmtx, err := f.osmAdminDB.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer coverageIntegrationRollback(osmtx)
	f.exec(osmtx, `INSERT INTO osm_catalog.generations(id,region_id,state,schema_name,source_url,source_sha256,importer_version,derivation_version)
        OVERRIDING SYSTEM VALUE VALUES($1,$2,'building',$3,'https://example.test/coverage.pbf',$4,4,21)`,
		next, f.regionID, fmt.Sprintf("osm_build_%d", next), hex.EncodeToString(hash[:]))
	f.exec(osmtx, `CREATE SCHEMA `+pgx.Identifier{fmt.Sprintf("osm_build_%d", next)}.Sanitize())
	f.commit(osmtx)
	r.nextObservation = time.Time{}
	if _, err := r.RunOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	if worked, err := f.runner().RunOnce(f.ctx); err != nil || worked {
		t.Fatalf("building map unlocked matching: %t %v", worked, err)
	}

	osmtx, err = f.osmAdminDB.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer coverageIntegrationRollback(osmtx)
	f.exec(osmtx, `UPDATE osm_catalog.generations SET state='retired',retired_at=clock_timestamp() WHERE id=$1`, f.generation)
	f.exec(osmtx, `UPDATE osm_catalog.generations SET state='active',promoted_at=clock_timestamp() WHERE id=$1`, next)
	for _, table := range []string{"ways", "localities", "path_segments"} {
		f.exec(osmtx, `UPDATE `+pgx.Identifier{"osm_canonical", table}.Sanitize()+` SET generation_id=$1 WHERE region_id=$2`, next, f.regionID)
	}
	f.exec(osmtx, `UPDATE osm_catalog.region_storage SET generation_id=$1,build_schema=$2 WHERE region_id=$3`, next, fmt.Sprintf("osm_build_%d", next), f.regionID)
	f.commit(osmtx)
	f.generation = next
	r.nextObservation = time.Time{}
	if worked, err := r.RunOnce(f.ctx); err != nil || !worked {
		t.Fatalf("promotion did not wake waiting work: %t %v", worked, err)
	}
	if f.visible() != old {
		t.Fatal("promotion rewrote evidence before matching committed")
	}
	f.run()
	tx = f.accountTx(f.ctx, f.adminDB)
	var current bool
	f.scan(tx, `SELECT processing_state='current' AND applied_path_policy_version=$2
        AND applied_generations=jsonb_build_array(jsonb_build_object('regionId',$3::text,'generation',$4::bigint))
        FROM app.workout_coverage_states WHERE workout_id=$1`, []any{workout, string(coverage.ExperimentalPathPolicyV84), f.regionID, next}, &current)
	f.commit(tx)
	if !current {
		t.Fatal("waiting workout did not rematch against the promoted compatible generation")
	}
}
