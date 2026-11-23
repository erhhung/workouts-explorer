package osmcontroller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type interruptedDeletion struct{}

func (interruptedDeletion) Report(_ context.Context, e osm.UpdateProgress) error {
	if e.Type == "start" && e.Task == 2 {
		return errors.New("simulated executor loss after detach")
	}
	return nil
}

func TestRegionDeletionReclaimsStoragePostgreSQL(t *testing.T) {
	appURL, catalogURL := os.Getenv("MIGRATION_DATABASE_URL"), os.Getenv("OSM_CATALOG_TEST_OWNER_URL")
	if appURL == "" || catalogURL == "" {
		t.Skip("owned deletion fixtures required")
	}
	if !strings.Contains(appURL, "workouts_admin_test_") || !strings.Contains(catalogURL, "osm_catalog_test_") {
		t.Fatal("refusing non-test deletion fixtures")
	}
	ctx := context.Background()
	connect := func(url string) *pgxpool.Pool {
		t.Helper()
		pool, err := pgxpool.New(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		return pool
	}
	app, catalog := connect(appURL), connect(catalogURL)
	controller, reader := connect(os.Getenv("OSM_CONTROLLER_DATABASE_URL")), connect(os.Getenv("OSM_CATALOG_DATABASE_URL"))
	exec := func(pool *pgxpool.Pool, sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	var admin uuid.UUID
	if err := app.QueryRow(ctx, `SELECT principal_id FROM app.administrators LIMIT 1`).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	request := uuid.New()
	prefix := "deletion-" + request.String()[:8]
	deleted, neighbor := "geofabrik:"+prefix, "geofabrik:"+prefix+"-neighbor"
	for _, region := range []string{deleted, neighbor} {
		exec(catalog, `INSERT INTO osm_catalog.regions(id,provider,provider_region_id,display_name,catalog_url,source_url,boundary) VALUES($1,'geofabrik',$2,$2,'https://fixture.invalid/catalog','https://fixture.invalid/source',ST_Multi(ST_GeomFromText('POLYGON((0 0,1 0,1 1,0 1,0 0))',4326)))`, region, strings.TrimPrefix(region, "geofabrik:"))
	}
	reserve := func(region, state string) int64 {
		t.Helper()
		var id int64
		if err := catalog.QueryRow(ctx, `WITH next AS(SELECT nextval('osm_catalog.generations_id_seq') AS id)
        INSERT INTO osm_catalog.generations(id,region_id,state,schema_name,source_url,importer_version,derivation_version,promoted_at,failure_summary) OVERRIDING SYSTEM VALUE
        SELECT id,$1,$2,('osm_build_'||id)::name,'https://fixture.invalid/source',1,1,CASE WHEN $2='active' THEN clock_timestamp() END,CASE WHEN $2='failed' THEN 'fixture failed attempt' END FROM next RETURNING id`, region, state).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	build, other, failed := reserve(deleted, "active"), reserve(neighbor, "active"), reserve(deleted, "failed")
	for _, r := range []struct {
		region string
		id     int64
	}{{deleted, build}, {neighbor, other}} {
		for _, parent := range []string{"ways", "localities", "path_segments"} {
			name := fmt.Sprintf("%s_g%d", parent, r.id)
			exec(catalog, "CREATE TABLE "+pgx.Identifier{"osm_canonical", name}.Sanitize()+" PARTITION OF "+pgx.Identifier{"osm_canonical", parent}.Sanitize()+" FOR VALUES IN ('"+r.region+"')")
		}
		exec(catalog, `INSERT INTO osm_catalog.region_storage(region_id,generation_id,ways_relation,localities_relation,path_segments_relation,build_schema) VALUES($1,$2::bigint,('ways_g'||($2::bigint)::text)::name,('localities_g'||($2::bigint)::text)::name,('path_segments_g'||($2::bigint)::text)::name,('osm_build_'||($2::bigint)::text)::name)`, r.region, r.id)
		exec(catalog, `INSERT INTO osm_canonical.ways(region_id,generation_id,way_id,version,tags,node_ids,geom) VALUES($1,$2,1,1,'{}','[]',ST_GeomFromText('LINESTRING(0 0,1 1)',4326))`, r.region, r.id)
	}
	schema := fmt.Sprintf("osm_build_%d", failed)
	exec(catalog, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize())
	exec(catalog, "CREATE TABLE "+pgx.Identifier{schema, "scratch"}.Sanitize()+" AS SELECT g, string_agg(md5((g::text||s::text)), '') AS payload FROM generate_series(1,12000) g CROSS JOIN generate_series(1,64) s GROUP BY g")
	exec(catalog, "CREATE INDEX ON "+pgx.Identifier{schema, "scratch"}.Sanitize()+"(g)")
	var bytesBefore int64
	if err := catalog.QueryRow(ctx, `SELECT pg_database_size(current_database())`).Scan(&bytesBefore); err != nil {
		t.Fatal(err)
	}
	exec(app, `INSERT INTO app.osm_region_updates(id,administrator_id,region_id,operation,state,slot_held,job_name) VALUES($1,$2,$3,'delete','deleting',true,'fixture-delete')`, request, admin, deleted)
	for _, task := range osm.DeleteTasks {
		exec(app, `INSERT INTO app.osm_region_update_steps(update_id,task_order,name,repeated) VALUES($1,$2,$3,false)`, request, task.Order, task.Name)
	}
	c := Controller{DB: controller, Catalog: reader}
	e := execution{ID: request, Region: deleted, Operation: "delete"}
	store := osm.PostgreSQLUpdateStore{Pool: catalog}
	if err := store.DeleteRegion(ctx, request.String(), deleted, interruptedDeletion{}); err == nil {
		t.Fatal("interruption did not stop cleanup")
	}
	complete, err := osm.RegionDeletionComplete(ctx, reader, request.String(), deleted)
	if err != nil || complete {
		t.Fatal("detached data was falsely reported reclaimed", complete, err)
	}
	if err = c.finishDeletion(ctx, e); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = app.QueryRow(ctx, `SELECT state FROM app.osm_region_updates WHERE id=$1`, request).Scan(&state); err != nil || state != "deleting" {
		t.Fatal("cleanup released Deleting prematurely", state, err)
	}
	if err = store.DeleteRegion(ctx, request.String(), deleted, nil); err != nil {
		t.Fatal(err)
	}
	if err = c.finishDeletion(ctx, e); err != nil {
		t.Fatal(err)
	}
	var bytesAfter int64
	if err = catalog.QueryRow(ctx, `SELECT pg_database_size(current_database())`).Scan(&bytesAfter); err != nil {
		t.Fatal(err)
	}
	if bytesBefore-bytesAfter < 16*1024*1024 {
		t.Fatalf("physical database storage was not reclaimed: before=%d after=%d", bytesBefore, bytesAfter)
	}
	var safe bool
	if err = catalog.QueryRow(ctx, `SELECT (SELECT count(*)=1 FROM osm_canonical.ways WHERE region_id=$1)
        AND EXISTS(SELECT 1 FROM osm_catalog.generations WHERE id=$2 AND state='active')
        AND EXISTS(SELECT 1 FROM osm_catalog.generations WHERE id=$3 AND state='retired' AND promoted_at IS NOT NULL)
        AND EXISTS(SELECT 1 FROM osm_catalog.promotion_events WHERE generation_id=$3)
        AND to_regnamespace($4) IS NULL`, neighbor, other, build, schema).Scan(&safe); err != nil || !safe {
		t.Fatal("neighbor/history preservation failed", err)
	}
	var held bool
	var payload []byte
	if err = app.QueryRow(ctx, `SELECT u.state,u.slot_held,c.payload FROM app.osm_region_updates u JOIN app.osm_region_catalog c ON c.id=u.region_id WHERE u.id=$1`, request).Scan(&state, &held, &payload); err != nil || state != "succeeded" || held || !strings.Contains(string(payload), `"generation": null`) {
		t.Fatal("region did not return to Available", state, held, string(payload), err)
	}
	// A duplicate delivery cannot delete a subsequent region download/build.
	if err = store.DeleteRegion(ctx, request.String(), neighbor, nil); err == nil {
		t.Fatal("deletion receipt was retargeted")
	}
	if err = store.DeleteRegion(ctx, request.String(), deleted, nil); err != nil {
		t.Fatal("completed deletion not idempotent", err)
	}
	t.Logf("physical reclamation=%d bytes; active neighbor and promotion history preserved", bytesBefore-bytesAfter)
}
