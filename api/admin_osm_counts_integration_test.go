package api

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAdminOSMRegionCountsIntegration(t *testing.T) {
	if os.Getenv("OSM_REGION_COUNTS_TEST_DISPOSABLE") != "1" {
		t.Skip("requires owned disposable PostGIS database")
	}
	ctx := context.Background()
	owner, err := pgxpool.New(ctx, os.Getenv("MIGRATION_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	api, err := pgxpool.New(ctx, os.Getenv("API_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer api.Close()
	admin := insertIngestTestAdministrator(t, owner)
	insertTestSession(t, api, admin, "cookie", strings.Repeat("a", 43))
	var session uuid.UUID
	if err := owner.QueryRow(ctx, `SELECT id FROM app.sessions WHERE principal_id=$1`, admin).Scan(&session); err != nil {
		t.Fatal(err)
	}
	_, first := insertSourceTestUser(t, owner)
	_, second := insertSourceTestUser(t, owner)
	workouts := []uuid.UUID{uuid.New(), uuid.New()}
	for i, account := range []uuid.UUID{first, second} {
		tx := integrationAccountTransaction(t, owner, account)
		exec := func(q string, args ...any) {
			t.Helper()
			if _, err := tx.Exec(ctx, q, args...); err != nil {
				t.Fatal(err)
			}
		}
		exec(`SET LOCAL session_replication_role='replica'`)
		source, activity := uuid.New(), uuid.New()
		exec(`INSERT INTO app.sources(id,account_id,display_name,canonical_display_name,type,config_envelope) VALUES($1,$2,'counts','counts','health-auto-export-local',decode('01','hex'))`, source, account)
		exec(`INSERT INTO app.workout_types(id,account_id,type_key,provider_label) VALUES($1,$2,'run','Outdoor Run')`, activity, account)
		exec(`INSERT INTO app.workouts(id,account_id,source_id,source_file_id,workout_type_id,provider_id,content_sha256,provider_label,started_at,ended_at,provider_duration)
		 VALUES($1,$2,$3,$4,$5,'counts',decode(repeat('01',32),'hex'),'Outdoor Run','2023-05-24Z','2023-05-24Z',0)`, workouts[i], account, source, uuid.New(), activity)
		exec(`INSERT INTO app.workout_routes(account_id,workout_id,point_count,minimum_longitude,minimum_latitude,maximum_longitude,maximum_latitude,has_complete_altitude,route)
		 VALUES($1,$2,3,0,0,3,1,false,ST_GeomFromText('LINESTRING(0.5 0.5,1.5 0.5,2.5 0.5)',4326))`, account, workouts[i])
		exec(`INSERT INTO app.workout_coverage_states(account_id,workout_id,route_input_revision,route_input_sha256,readiness_state,map_data_ready_at,
		 applied_route_input_revision,applied_route_input_sha256,applied_generations) VALUES($1,$2,1,decode(repeat('01',32),'hex'),'map_data_ready',now(),1,decode(repeat('01',32),'hex'),
		 '[{"regionId":"fixture:counts-west","generation":10},{"regionId":"fixture:counts-east","generation":20}]')`, account, workouts[i])
		exec(`INSERT INTO app.coverage_applied_match_proofs(account_id,workout_id,route_input_revision,route_input_sha256,generations,matching_descriptor,job_id)
		 VALUES($1,$2,1,decode(repeat('01',32),'hex'),'[{"regionId":"fixture:counts-west","generation":10},{"regionId":"fixture:counts-east","generation":20}]','{}',$3)`, account, workouts[i], uuid.New())
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := owner.Exec(ctx, `INSERT INTO app.osm_region_catalog(id,provider,payload) VALUES
	 ('fixture:counts-west','fixture','{"buildId":10,"boundary":{"type":"Polygon","coordinates":[[[0,0],[2,0],[2,2],[0,2],[0,0]]]}}'),
	 ('fixture:counts-east','fixture','{"buildId":20,"boundary":{"type":"Polygon","coordinates":[[[2,0],[4,0],[4,2],[2,2],[2,0]]]}}')`); err != nil {
		t.Fatal(err)
	}
	check := func(west, east int64) {
		t.Helper()
		rows, err := api.Query(ctx, `SELECT region_id,workout_count,coverage_route_count FROM app.admin_osm_region_workout_counts($1) WHERE region_id LIKE 'fixture:counts-%' ORDER BY region_id`, session)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		seen := 0
		for rows.Next() {
			var id string
			var total, current int64
			if err := rows.Scan(&id, &total, &current); err != nil {
				t.Fatal(err)
			}
			want := west
			if id == "fixture:counts-east" {
				want = east
			}
			if total != 2 || current != want {
				t.Fatalf("%s total=%d current=%d; want total=2 current=%d", id, total, current, want)
			}
			seen++
		}
		if rows.Err() != nil || seen != 2 {
			t.Fatal("missing region aggregates", rows.Err(), seen)
		}
	}
	check(2, 2)
	if _, err := owner.Exec(ctx, `UPDATE app.osm_region_catalog SET payload=jsonb_set(payload,'{buildId}','11') WHERE id='fixture:counts-west'`); err != nil {
		t.Fatal(err)
	}
	check(0, 2)
	for i, account := range []uuid.UUID{first, second} {
		tx := integrationAccountTransaction(t, owner, account)
		if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"workout_coverage_states", "coverage_applied_match_proofs"} {
			column := "applied_generations"
			if table == "coverage_applied_match_proofs" {
				column = "generations"
			}
			if _, err := tx.Exec(ctx, `UPDATE app.`+table+` SET `+column+`='[{"regionId":"fixture:counts-west","generation":11},{"regionId":"fixture:counts-east","generation":20}]' WHERE account_id=$1 AND workout_id=$2`, account, workouts[i]); err != nil {
				t.Fatal(err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		check(int64(i+1), 2)
	}
	retained := integrationAccountTransaction(t, owner, first)
	defer retained.Rollback(ctx)
	if _, err := retained.Exec(ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Fatal(err)
	}
	if _, err := retained.Exec(ctx, `UPDATE app.sources SET deleted_at=clock_timestamp(),config_envelope=NULL,auto_sync_enabled=false WHERE account_id=$1 AND id=(SELECT source_id FROM app.workouts WHERE id=$2 AND account_id=$1)`, first, workouts[0]); err != nil {
		t.Fatal(err)
	}
	if err := retained.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	check(2, 2)
	// Exercise the real projection while leaving unrelated ingest fences disabled
	// for this synthetic fixture. Rollback restores both data and trigger modes.
	projection := integrationAccountTransaction(t, owner, first)
	defer projection.Rollback(ctx)
	for _, query := range []string{
		`ALTER TABLE app.workout_routes ENABLE ALWAYS TRIGGER osm_region_workouts_route`,
		`ALTER TABLE app.workout_routes ENABLE ALWAYS TRIGGER osm_region_workouts_route_lock`,
		`SET LOCAL session_replication_role='replica'`,
	} {
		if _, err := projection.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := projection.Exec(ctx, `UPDATE app.workout_routes SET route=ST_Translate(route,10,0) WHERE account_id=$1 AND workout_id=$2`, first, workouts[0]); err != nil {
		t.Fatal(err)
	}
	var total, current int64
	if err := projection.QueryRow(ctx, `SELECT workout_count,coverage_route_count FROM app.admin_osm_region_workout_counts($1) WHERE region_id='fixture:counts-west'`, session).Scan(&total, &current); err != nil || total != 1 || current != 1 {
		t.Fatal("route edit left stale membership", total, current, err)
	}
	if err := projection.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	check(2, 2)
	boundary, err := owner.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer boundary.Rollback(ctx)
	if _, err := boundary.Exec(ctx, `UPDATE app.osm_region_catalog SET payload=jsonb_set(payload,'{boundary}','{"type":"Polygon","coordinates":[[[10,10],[12,10],[12,12],[10,12],[10,10]]]}') WHERE id='fixture:counts-west'`); err != nil {
		t.Fatal(err)
	}
	if err := boundary.QueryRow(ctx, `SELECT workout_count,coverage_route_count FROM app.admin_osm_region_workout_counts($1) WHERE region_id='fixture:counts-west'`, session).Scan(&total, &current); err != nil || total != 0 || current != 0 {
		t.Fatal("boundary edit left stale membership", total, current, err)
	}
	if err := boundary.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	check(2, 2)
	if _, err := api.Exec(ctx, `SELECT * FROM app.admin_osm_region_workout_counts($1)`, uuid.New()); err == nil {
		t.Fatal("unauthenticated aggregate read accepted")
	}
}
