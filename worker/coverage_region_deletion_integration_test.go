package worker

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestCoverageSurvivesRegionDeletionAndNewPromotionIntegration(t *testing.T) {
	f := newCoverageIntegrationFixture(t)
	f.seedOSM()
	workout := f.addWorkout(coverageIntegrationRoute(0))
	f.queue(workout)
	f.run()
	var count int
	if err := f.adminDB.QueryRow(f.ctx, `SELECT count(*) FROM app.workout_segment_matches WHERE account_id=$1`, f.accountID).Scan(&count); err != nil || count == 0 {
		t.Fatal("fixture has no applied coverage", count, err)
	}
	before := f.visible()
	current := func(account, id uuid.UUID, policy string) bool {
		t.Helper()
		tx := f.accountTx(f.ctx, f.apiDB)
		defer coverageIntegrationRollback(tx)
		var value bool
		f.scan(tx, `SELECT app.coverage_display_target_current($1,$2,$3,$4,$5)`, []any{account, id, string(coverage.ExperimentalRulesV1), string(coverage.ExperimentalSamplingV1), policy}, &value)
		return value
	}
	policy := string(coverage.CurrentPathPolicyVersion)
	if !current(f.accountID, workout, policy) {
		t.Fatal("fresh applied coverage is not current")
	}
	if current(uuid.New(), workout, policy) || current(f.accountID, workout, "coverage-path-policy-experimental-v999") {
		t.Fatal("currentness bypassed account or matching policy")
	}
	if _, err := f.adminDB.Exec(f.ctx, `UPDATE app.workout_coverage_states SET route_input_revision=route_input_revision+1 WHERE account_id=$1 AND workout_id=$2`, f.accountID, workout); err != nil {
		t.Fatal(err)
	}
	if current(f.accountID, workout, policy) {
		t.Fatal("a changed route revision was treated as current")
	}
	if _, err := f.adminDB.Exec(f.ctx, `UPDATE app.workout_coverage_states SET route_input_revision=route_input_revision-1 WHERE account_id=$1 AND workout_id=$2`, f.accountID, workout); err != nil {
		t.Fatal(err)
	}
	var originals []string
	if err := f.osmAdminDB.QueryRow(f.ctx, `SELECT ARRAY[ways_relation::text,localities_relation::text,path_segments_relation::text] FROM osm_catalog.region_storage WHERE region_id=$1`, f.regionID).Scan(&originals); err != nil {
		t.Fatal(err)
	}
	// The general matcher fixture uses synthetic names; the production deletion
	// contract deliberately accepts only region-owned generation partition names.
	for i, parent := range []string{"ways", "localities", "path_segments"} {
		if _, err := f.osmAdminDB.Exec(f.ctx, "ALTER TABLE "+pgx.Identifier{"osm_canonical", originals[i]}.Sanitize()+" RENAME TO "+pgx.Identifier{fmt.Sprintf("%s_g%d", parent, f.generation)}.Sanitize()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.osmAdminDB.Exec(f.ctx, `UPDATE osm_catalog.region_storage SET ways_relation=('ways_g'||generation_id)::name,localities_relation=('localities_g'||generation_id)::name,path_segments_relation=('path_segments_g'||generation_id)::name WHERE region_id=$1`, f.regionID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, q := range []string{`DELETE FROM osm_catalog.region_deletions WHERE region_id=$1`, `DELETE FROM osm_catalog.storage_gc WHERE region_id=$1`} {
			if _, err := f.osmAdminDB.Exec(ctx, q, f.regionID); err != nil {
				t.Error(err)
			}
		}
		// Restore empty fixture-only containers for its original teardown routine.
		rows, err := f.osmAdminDB.Query(ctx, `SELECT schema_name::text FROM osm_catalog.generations WHERE region_id=$1`, f.regionID)
		if err != nil {
			t.Error(err)
			return
		}
		var schemas []string
		for rows.Next() {
			var name string
			if err = rows.Scan(&name); err != nil {
				t.Error(err)
			}
			schemas = append(schemas, name)
		}
		rows.Close()
		for _, name := range schemas {
			if _, err = f.osmAdminDB.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+pgx.Identifier{name}.Sanitize()); err != nil {
				t.Error(err)
			}
		}
		for _, name := range originals {
			if _, err = f.osmAdminDB.Exec(ctx, "CREATE TABLE IF NOT EXISTS "+pgx.Identifier{"osm_canonical", name}.Sanitize()+"(fixture integer)"); err != nil {
				t.Error(err)
			}
		}
	})
	request := uuid.New()
	if err := (osm.PostgreSQLUpdateStore{Pool: f.osmAdminDB}).DeleteRegion(f.ctx, request.String(), f.regionID, nil); err != nil {
		t.Fatal(err)
	}
	if f.visible() != before {
		t.Fatal("region deletion changed saved private coverage")
	}
	r := NewCoverageReconciler(f.coverageDB, f.osmDB, slog.Default(), CoverageReconcilerOptions{})
	if worked, err := r.RunOnce(f.ctx); err != nil || !worked {
		t.Fatal("deleted-region reconciliation", worked, err)
	}
	if !current(f.accountID, workout, policy) || f.visible() != before {
		t.Fatal("reconciliation invalidated retained coverage without a replacement")
	}
	var internalState string
	if err := f.adminDB.QueryRow(f.ctx, `SELECT readiness_state FROM app.workout_coverage_states WHERE account_id=$1 AND workout_id=$2`, f.accountID, workout).Scan(&internalState); err != nil {
		t.Fatal(err)
	}
	if internalState != "unavailable" {
		t.Fatal("new calculation prerequisites must remain unavailable", internalState)
	}
	if _, err := f.adminDB.Exec(f.ctx, `DELETE FROM app.coverage_applied_match_proofs WHERE account_id=$1 AND workout_id=$2`, f.accountID, workout); err != nil {
		t.Fatal(err)
	}
	if !current(f.accountID, workout, policy) {
		t.Fatal("historical applied coverage without an auxiliary match proof was invalidated")
	}
	newWorkout := f.addWorkout(coverageIntegrationRoute(1))
	r.nextObservation = time.Time{}
	if _, err := r.RunOnce(f.ctx); err != nil {
		t.Fatal(err)
	}
	if current(f.accountID, newWorkout, policy) {
		t.Fatal("a route without applied coverage became current")
	}
	var oldNumber, watermark int64
	if err := f.osmAdminDB.QueryRow(f.ctx, `SELECT promotion_number,generation_high_watermark FROM osm_catalog.generations g JOIN osm_catalog.regions r ON r.id=g.region_id WHERE g.id=$1`, f.generation).Scan(&oldNumber, &watermark); err != nil || oldNumber != 1 || watermark != 1 {
		t.Fatal("deletion reset the region watermark", oldNumber, watermark, err)
	}
	var next int64
	if err := f.osmAdminDB.QueryRow(f.ctx, `WITH id AS(SELECT nextval('osm_catalog.generations_id_seq') n)
        INSERT INTO osm_catalog.generations(id,region_id,state,schema_name,source_url,importer_version,derivation_version,promoted_at) OVERRIDING SYSTEM VALUE
        SELECT n,$1,'active',('osm_build_'||n)::name,'https://example.test/new-region',1,1,'2000-01-01' FROM id RETURNING id`, f.regionID).Scan(&next); err != nil {
		t.Fatal(err)
	}
	var number int64
	if err := f.osmAdminDB.QueryRow(f.ctx, `SELECT promotion_number FROM osm_catalog.generations WHERE id=$1`, next).Scan(&number); err != nil || number != 2 {
		t.Fatal("new promotion reused a generation number", number, err)
	}
	if current(f.accountID, workout, policy) == false {
		t.Fatal("unobserved promotion changed the accepted display snapshot")
	}
	if err := r.observe(f.ctx); err != nil {
		t.Fatal(err)
	}
	if current(f.accountID, workout, policy) {
		t.Fatal("newer promoted regional data did not make prior coverage stale")
	}
	if f.visible() != before {
		t.Fatal("new promotion erased the previous applied coverage")
	}
	t.Log("physical region cleanup preserves current cached coverage; unavailable new work stays unavailable; durable generation2 survives an older timestamp and makes prior results stale")
}
