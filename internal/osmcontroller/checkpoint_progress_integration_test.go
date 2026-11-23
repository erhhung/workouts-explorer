package osmcontroller

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"strings"
	"testing"
	"time"
)

func TestCheckpointProgressReconciliationPostgreSQL(t *testing.T) {
	appURL, catalogURL := os.Getenv("MIGRATION_DATABASE_URL"), os.Getenv("OSM_CATALOG_TEST_OWNER_URL")
	if appURL == "" || catalogURL == "" {
		t.Skip("owned checkpoint fixtures required")
	}
	if !strings.Contains(appURL, "workouts_admin_test_") || !strings.Contains(catalogURL, "osm_catalog_test_") {
		t.Fatal("refusing non-test checkpoint fixtures")
	}
	ctx := context.Background()
	owner, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	catalog, err := pgxpool.New(ctx, catalogURL)
	if err != nil {
		t.Fatal(err)
	}
	defer catalog.Close()
	controller, err := pgxpool.New(ctx, os.Getenv("OSM_CONTROLLER_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer controller.Close()
	reader, err := pgxpool.New(ctx, os.Getenv("OSM_CATALOG_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	exec := func(pool *pgxpool.Pool, q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	var admin uuid.UUID
	if err := owner.QueryRow(ctx, `SELECT principal_id FROM app.administrators LIMIT 1`).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	id, foreign := uuid.New(), uuid.New()
	region := "geofabrik:checkpoint-fixture-" + id.String()[:8]
	now := time.Now().UTC().Truncate(time.Second)
	exec(catalog, `INSERT INTO osm_catalog.regions(id,provider,provider_region_id,display_name,catalog_url,source_url,boundary)
    VALUES($1,'geofabrik',$2,'Checkpoint fixture','https://fixture.invalid/catalog','https://fixture.invalid/source',ST_Multi(ST_GeomFromText('POLYGON((0 0,1 0,1 1,0 1,0 0))',4326))) ON CONFLICT(id) DO NOTHING`, region, strings.TrimPrefix(region, "geofabrik:"))
	var build int64
	if err := catalog.QueryRow(ctx, `WITH next AS(SELECT nextval('osm_catalog.generations_id_seq') AS id)
    INSERT INTO osm_catalog.generations(id,region_id,state,schema_name,source_url,importer_version,derivation_version,control_job_id) OVERRIDING SYSTEM VALUE
    SELECT next.id,$1,'building',('osm_build_'||next.id)::name,'https://fixture.invalid/source',1,1,$2 FROM next RETURNING id`, region, foreign).Scan(&build); err != nil {
		t.Fatal(err)
	}
	exec(owner, `INSERT INTO app.osm_region_updates(id,administrator_id,region_id,operation,state,phase,slot_held,generation_id) VALUES($1,$2,$3,'download','processing','Processing',true,$4)`, id, admin, region, build)
	exec(owner, `INSERT INTO app.osm_region_update_steps(update_id,task_order,name,repeated,state,started_at,elapsed_total_ms) VALUES($1,26,'attribute-slivers/discover',true,'completed',$2,60000),($1,27,'attribute-slivers/apply',true,'paused',$2,0),($1,51,'validate/ways',false,'paused',$2,0)`, id, now.Add(-time.Minute))
	for _, stage := range []string{"attribute-slivers", "validate"} {
		order := 12
		if stage == "validate" {
			order = 21
		}
		exec(catalog, `INSERT INTO osm_catalog.generation_stages(generation_id,stage,stage_order,fence,state,finished_at,cursor) VALUES($1,$2,$3,repeat('a',64),'completed',$4,'{"phase":"done"}')`, build, stage, order, now)
	}
	c := Controller{DB: controller, Catalog: reader}
	e := execution{ID: id, Region: region, Generation: &build}
	if err := c.reconcileCheckpointProgress(ctx, e, false); err != nil {
		t.Fatal(err)
	}
	var state string
	readState := func(order int) string {
		t.Helper()
		if err := owner.QueryRow(ctx, `SELECT state FROM app.osm_region_update_steps WHERE update_id=$1 AND task_order=$2`, id, order).Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	if readState(27) != "paused" {
		t.Fatal("foreign build bound to a different request was adopted")
	}
	exec(catalog, `UPDATE osm_catalog.generations SET control_job_id=$2 WHERE id=$1`, build, id)
	if err := c.reconcileCheckpointProgress(ctx, e, false); err != nil {
		t.Fatal(err)
	}
	if readState(27) != "completed" || readState(51) != "paused" {
		t.Fatal("committed checkpoint or replayed validation status wrong")
	}
	var elapsed *int64
	var finished time.Time
	if err := owner.QueryRow(ctx, `SELECT elapsed_total_ms,finished_at FROM app.osm_region_update_steps WHERE update_id=$1 AND task_order=27`, id).Scan(&elapsed, &finished); err != nil || elapsed != nil || !finished.Equal(now) {
		t.Fatal("reconciliation invented elapsed time or wrong checkpoint timestamp", elapsed, finished, err)
	}
	if err := c.reconcileCheckpointProgress(ctx, e, false); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := owner.QueryRow(ctx, `SELECT count(*) FROM app.osm_region_update_events WHERE update_id=$1 AND event_type='executor.checkpoint_reconciled'`, id).Scan(&count); err != nil || count != 1 {
		t.Fatal("checkpoint synchronization was not idempotent", count, err)
	}
	exec(owner, `UPDATE app.osm_region_update_steps SET state='running' WHERE update_id=$1 AND task_order=27`, id)
	if err := c.reconcileCheckpointProgress(ctx, e, false); err != nil || readState(27) != "running" {
		t.Fatal("active report was raced", err)
	}
	if err := c.reconcileCheckpointProgress(ctx, e, true); err != nil || readState(27) != "completed" {
		t.Fatal("confirmed stopped batch not reconciled", err)
	}
	exec(owner, `UPDATE app.osm_region_updates SET elapsed_total_ms=90000 WHERE id=$1`, id)
	exec(owner, `UPDATE app.osm_region_update_steps SET state='paused',elapsed_total_ms=5000 WHERE update_id=$1 AND task_order=27`, id)
	exec(owner, `INSERT INTO app.osm_region_update_events(update_id,event_type,task_order,happened_at) VALUES($1,'start',27,$2)`, id, now.Add(-2750*time.Millisecond))
	if err := c.reconcileCheckpointProgress(ctx, e, false); err != nil {
		t.Fatal(err)
	}
	var jobElapsed int64
	if err := owner.QueryRow(ctx, `SELECT u.elapsed_total_ms,s.elapsed_total_ms FROM app.osm_region_updates u JOIN app.osm_region_update_steps s ON s.update_id=u.id AND s.task_order=27 WHERE u.id=$1`, id).Scan(&jobElapsed, &elapsed); err != nil || jobElapsed != 92750 || elapsed == nil || *elapsed != 7750 {
		t.Fatal("successful checkpoint timing lost prior totals", jobElapsed, elapsed, err)
	}
	if err := c.reconcileCheckpointProgress(ctx, e, false); err != nil {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `SELECT elapsed_total_ms FROM app.osm_region_updates WHERE id=$1`, id).Scan(&jobElapsed); err != nil || jobElapsed != 92750 {
		t.Fatal("recovered timing duplicated", jobElapsed, err)
	}
	exec(owner, `UPDATE app.osm_region_updates SET slot_held=false,state='failed' WHERE id=$1`, id)
	t.Log(fmt.Sprintf("build%d/task27 checkpoint repaired without changing data or validation", build))
}
