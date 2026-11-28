package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestManagedCancellationDiscardsOnlyOwnedCandidate(t *testing.T) {
	appURL, osmURL, executorURL := os.Getenv("MIGRATION_DATABASE_URL"), os.Getenv("OSM_CATALOG_TEST_OWNER_URL"), os.Getenv("OSM_EXECUTOR_DATABASE_URL")
	if appURL == "" || osmURL == "" || executorURL == "" {
		t.Skip("isolated managed cancellation databases required")
	}
	if !strings.Contains(appURL, "workouts_admin_test_") || !strings.Contains(osmURL, "osm_catalog_test") {
		t.Fatal("refusing non-test databases")
	}
	ctx := context.Background()
	open := func(url string) *pgxpool.Pool {
		p, e := pgxpool.New(ctx, url)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(p.Close)
		return p
	}
	app, writer, executor := open(appURL), open(osmURL), open(executorURL)
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
	id := uuid.New()
	token := strings.Repeat("c", 43)
	part := "managed-" + uuid.NewString()[:8]
	region := "geofabrik:" + part
	exec(app, `INSERT INTO app.osm_region_updates(id,administrator_id,region_id,operation,state,intent,slot_held,execution_hash) VALUES($1,$2,$3,'download','canceling','cancel',true,encode(sha256(convert_to($4,'UTF8')),'hex'))`, id, admin, region, token)
	exec(writer, `INSERT INTO osm_catalog.regions(id,provider,provider_region_id,display_name,catalog_url,source_url,boundary)
		VALUES($1,'geofabrik',$2,'Managed cancellation','https://download.geofabrik.de/index-v1.json','https://download.geofabrik.de/test-latest.osm.pbf',ST_Multi(ST_GeomFromText('POLYGON((0 0,1 0,1 1,0 0))',4326)))`, region, part)
	var active, candidate int64
	if err := writer.QueryRow(ctx, `SELECT nextval(pg_get_serial_sequence('osm_catalog.generations','id'))`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	exec(writer, `INSERT INTO osm_catalog.generations(id,region_id,state,schema_name,source_url,importer_version,derivation_version,promoted_at) OVERRIDING SYSTEM VALUE VALUES($1,$2,'active',('osm_build_'||$1::bigint)::name,'https://download.geofabrik.de/test-latest.osm.pbf',1,1,clock_timestamp())`, active, region)
	if err := writer.QueryRow(ctx, `SELECT nextval(pg_get_serial_sequence('osm_catalog.generations','id'))`).Scan(&candidate); err != nil {
		t.Fatal(err)
	}
	exec(writer, `INSERT INTO osm_catalog.generations(id,region_id,state,schema_name,source_url,importer_version,derivation_version,control_job_id) OVERRIDING SYSTEM VALUE VALUES($1,$2,'building',('osm_build_'||$1::bigint)::name,'https://download.geofabrik.de/test-latest.osm.pbf',1,1,$3)`, candidate, region, id)
	if _, err := writer.Exec(ctx, "CREATE SCHEMA "+"osm_build_"+strconv.FormatInt(candidate, 10)); err != nil {
		t.Fatal(err)
	}
	control := &managedControl{db: executor, id: id, token: token}
	control.intent.Store("cancel")
	if err := cancelManaged(ctx, control, osm.PostgreSQLUpdateStore{Pool: writer, WaitForLock: true, ControlJobID: id.String()}, managedState{RegionID: region, Intent: "cancel"}); err != nil {
		t.Fatal(err)
	}
	var valid bool
	if err := writer.QueryRow(ctx, `SELECT (SELECT state='active' FROM osm_catalog.generations WHERE id=$1) AND (SELECT state='failed' AND to_regnamespace(schema_name::text) IS NULL FROM osm_catalog.generations WHERE id=$2)`, active, candidate).Scan(&valid); err != nil || !valid {
		t.Fatalf("candidate/active isolation %v", err)
	}
	if err := app.QueryRow(ctx, `SELECT state='canceling' AND slot_held AND executor_outcome='canceled' FROM app.osm_region_updates WHERE id=$1`, id).Scan(&valid); err != nil || !valid {
		t.Fatalf("executor prematurely released cleanup capacity: %v", err)
	}
}

func TestManagedPreparationInventoryAndExecutionClock(t *testing.T) {
	appURL, executorURL, apiURL := os.Getenv("MIGRATION_DATABASE_URL"), os.Getenv("OSM_EXECUTOR_DATABASE_URL"), os.Getenv("API_DATABASE_URL")
	if appURL == "" || executorURL == "" || apiURL == "" {
		t.Skip("isolated updater protocol fixture required")
	}
	if !strings.Contains(appURL, "workouts_admin_test_") {
		t.Fatal("refusing a non-test updater database")
	}
	ctx := context.Background()
	connect := func(url string) *pgxpool.Pool {
		p, e := pgxpool.New(ctx, url)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(p.Close)
		return p
	}
	owner, executor, apiDB := connect(appURL), connect(executorURL), connect(apiURL)
	var admin uuid.UUID
	if err := owner.QueryRow(ctx, `SELECT principal_id FROM app.administrators LIMIT 1`).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct{ legacy, configured bool }{{false, true}, {true, true}, {false, false}} {
		legacy, configured := fixture.legacy, fixture.configured
		t.Run(fmt.Sprint("legacy=", legacy, "/configured=", configured), func(t *testing.T) {
			id, token := uuid.New(), strings.Repeat("p", 43)
			region := "geofabrik:preparation-" + id.String()[:8]
			exec := func(q string, args ...any) {
				t.Helper()
				if _, err := owner.Exec(ctx, q, args...); err != nil {
					t.Fatal(err)
				}
			}
			exec(`UPDATE app.osm_processing_settings SET monitor_database_storage=false,revision=revision+1 WHERE singleton`)
			exec(`INSERT INTO app.osm_region_updates(id,administrator_id,region_id,operation,state,slot_held,execution_hash) VALUES($1,$2,$3,'download','waiting',true,encode(sha256(convert_to($4,'UTF8')),'hex'))`, id, admin, region, token)
			exec(`UPDATE app.osm_processing_settings SET monitor_database_storage=true,revision=revision+1 WHERE singleton`)
			definitions := append([]osm.UpdateTaskDefinition(nil), osm.UpdateTasks...)
			if legacy {
				definitions = definitions[1:]
				for i := range definitions {
					definitions[i].Order--
				}
			}
			for _, task := range definitions {
				exec(`INSERT INTO app.osm_region_update_steps(update_id,task_order,name,repeated) VALUES($1,$2,$3,$4)`, id, task.Order, task.Name, task.Repeated)
			}
			control := &managedControl{db: executor, id: id, token: token, legacyTasks: legacy}
			control.intent.Store("run")
			state, err := control.event(ctx, osm.UpdateProgress{Type: "read", StorageMetricsConfigured: &configured})
			if err != nil || state.TaskCount != len(definitions) {
				t.Fatal("inventory capability", state, err)
			}
			if state.MonitorDatabaseStorage == nil || *state.MonitorDatabaseStorage != configured {
				t.Fatal("execution did not capture the latest enabled policy at startup")
			}
			exec(`UPDATE app.osm_processing_settings SET monitor_database_storage=false,revision=revision+1 WHERE singleton`)
			if unchanged, err := control.event(ctx, osm.UpdateProgress{Type: "read"}); err != nil || unchanged.MonitorDatabaseStorage == nil || *unchanged.MonitorDatabaseStorage != configured {
				t.Fatal("running execution changed policy on a repeated read", err)
			}
			base := time.Now().UTC().Truncate(time.Millisecond)
			exec(`INSERT INTO app.osm_region_update_events(update_id,event_type,task_order,happened_at) VALUES($1,'start',1,$2::timestamptz-interval '1 hour'),($1,'finish',NULL,$2::timestamptz-interval '50 minutes'),($1,'osm.region_update_resume',NULL,$2::timestamptz-interval '40 minutes')`, id, base)
			report := func(event osm.UpdateProgress) {
				t.Helper()
				if err := control.Report(ctx, event); err != nil {
					t.Fatal(err)
				}
			}
			report(osm.UpdateProgress{Type: "execution"})
			exec(`UPDATE app.osm_region_update_events SET happened_at=$2::timestamptz-interval '10 minutes' WHERE update_id=$1 AND event_type='execution'`, id, base)
			prep, download := int64(178000), int64(2000)
			report(osm.UpdateProgress{Type: "start", Task: 1})
			report(osm.UpdateProgress{Type: "complete", Task: 1, Completed: true, ElapsedMilliseconds: &prep})
			var stepState string
			if err := owner.QueryRow(ctx, `SELECT state FROM app.osm_region_update_steps WHERE update_id=$1 AND task_order=1`, id).Scan(&stepState); err != nil || (legacy && stepState != "running") || (!legacy && stepState != "completed") {
				t.Fatal("preparation completion boundary", stepState, err)
			}
			report(osm.UpdateProgress{Type: "start", Task: 2})
			report(osm.UpdateProgress{Type: "complete", Task: 2, Completed: true, ElapsedMilliseconds: &download})
			var phase string
			var elapsed int64
			if err := owner.QueryRow(ctx, `SELECT phase,elapsed_total_ms FROM app.osm_region_updates WHERE id=$1`, id).Scan(&phase, &elapsed); err != nil || phase != "Downloading" || elapsed < 1200000 {
				t.Fatal("download phase/accounting", phase, elapsed, err)
			}
			if err := owner.QueryRow(ctx, `SELECT elapsed_total_ms FROM app.osm_region_update_steps WHERE update_id=$1 AND name='download'`, id).Scan(&elapsed); err != nil || elapsed < 0 || elapsed > 10000 {
				t.Fatal("preparation leaked into new download timing", elapsed, err)
			}
			report(osm.UpdateProgress{Type: "start", Task: 3})
			var raw []byte
			if err := apiDB.QueryRow(ctx, `SELECT `+osm.RegionUpdateJSON+` FROM app.osm_region_updates WHERE id=$1`, id).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var result osm.RegionUpdate
			if err := json.Unmarshal(raw, &result); err != nil || result.Phase != "Processing" || result.ExecutionElapsed == nil || *result.ExecutionElapsed != 600 || result.ExecutionSince == nil || !result.ExecutionSince.Equal(base.Add(-10*time.Minute)) {
				t.Fatal("active execution clock includes queue/pause or omits between-batch time", string(raw), err)
			}
			outcome := "paused"
			if legacy {
				outcome = "failed"
			}
			report(osm.UpdateProgress{Type: "finish", Outcome: outcome})
			exec(`UPDATE app.osm_region_updates SET state=$2,slot_held=false WHERE id=$1`, id, outcome)
			if err := apiDB.QueryRow(ctx, `SELECT `+osm.RegionUpdateJSON+` FROM app.osm_region_updates WHERE id=$1`, id).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &result); err != nil || result.ExecutionSince != nil || result.ExecutionElapsed == nil || *result.ExecutionElapsed < 1200 {
				t.Fatal("paused execution clock did not freeze", string(raw), err)
			}
			next := strings.Repeat("r", 43)
			control.token = next
			exec(`UPDATE app.osm_region_updates SET state='waiting',slot_held=true,executor_outcome=NULL,execution_hash=encode(sha256(convert_to($2,'UTF8')),'hex'),generation_id=90210,resumable=true WHERE id=$1`, id, next)
			resumed, err := control.event(ctx, osm.UpdateProgress{Type: "read"})
			if err != nil || resumed.MonitorDatabaseStorage == nil || *resumed.MonitorDatabaseStorage || resumed.GenerationID == nil || *resumed.GenerationID != 90210 || !resumed.Resumable {
				t.Fatal("resume did not apply the current disabled setting while retaining checkpoint identity", resumed, err)
			}
			assertFinal := func(state string, finished bool) {
				t.Helper()
				var valid bool
				if err := owner.QueryRow(ctx, `SELECT state=$3 AND (finished_at IS NOT NULL)=$4 FROM app.osm_region_update_steps WHERE update_id=$1 AND task_order=$2`, id, len(definitions), state, finished).Scan(&valid); err != nil || !valid {
					t.Fatal("final task maintenance boundary", state, finished, err)
				}
			}
			report(osm.UpdateProgress{Type: "start", Task: len(osm.UpdateTasks)})
			report(osm.UpdateProgress{Type: "complete", Task: len(osm.UpdateTasks), AwaitingMetrics: true})
			report(osm.UpdateProgress{Type: "settled", Task: len(osm.UpdateTasks)})
			report(osm.UpdateProgress{Type: "promoted", Task: len(osm.UpdateTasks), Promoted: true})
			maintenance := int64(203000)
			report(osm.UpdateProgress{Type: "elapsed", Task: len(osm.UpdateTasks), ElapsedMilliseconds: &maintenance})
			assertFinal("running", false)
			report(osm.UpdateProgress{Type: "finish", Outcome: "failed", Promoted: true})
			assertFinal("failed", true)
			// A post-promotion retry reopens even an old executor's completed row.
			control.token = strings.Repeat("f", 43)
			exec(`UPDATE app.osm_region_updates SET state='waiting',executor_outcome=NULL,execution_hash=encode(sha256(convert_to($2,'UTF8')),'hex') WHERE id=$1`, id, control.token)
			if _, err = control.event(ctx, osm.UpdateProgress{Type: "read"}); err != nil {
				t.Fatal(err)
			}
			report(osm.UpdateProgress{Type: "start", Task: len(osm.UpdateTasks)})
			assertFinal("running", false)
			report(osm.UpdateProgress{Type: "settled", Task: len(osm.UpdateTasks), Completed: true})
			assertFinal("completed", true)
			report(osm.UpdateProgress{Type: "start", Task: len(osm.UpdateTasks)})
			assertFinal("running", false)
			report(osm.UpdateProgress{Type: "settled", Task: len(osm.UpdateTasks), Completed: true})
			exec(`UPDATE app.osm_processing_settings SET monitor_database_storage=true,revision=revision+1 WHERE singleton`)
			report(osm.UpdateProgress{Type: "finish", Outcome: "succeeded"})
			assertFinal("completed", true)
			exec(`UPDATE app.osm_region_updates SET state='succeeded',slot_held=false WHERE id=$1`, id)
			if err := apiDB.QueryRow(ctx, `SELECT `+osm.RegionUpdateJSON+` FROM app.osm_region_updates WHERE id=$1`, id).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(raw, &result); err != nil || result.MonitorDatabaseStorage {
				t.Fatal("completed history did not retain the final execution policy", err)
			}
		})
	}
}

func TestManagedRegionDeletionReportsCapability(t *testing.T) {
	appURL, osmURL, executorURL := os.Getenv("MIGRATION_DATABASE_URL"), os.Getenv("OSM_CATALOG_TEST_OWNER_URL"), os.Getenv("OSM_EXECUTOR_DATABASE_URL")
	if appURL == "" || osmURL == "" || executorURL == "" {
		t.Skip("isolated managed deletion databases required")
	}
	if !strings.Contains(appURL, "workouts_admin_test_") || !strings.Contains(osmURL, "osm_catalog_test_") {
		t.Fatal("refusing non-test databases")
	}
	ctx := context.Background()
	app, err := pgxpool.New(ctx, appURL)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	writer, err := pgxpool.New(ctx, osmURL)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	exec := func(pool *pgxpool.Pool, q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	var admin uuid.UUID
	if err := app.QueryRow(ctx, `SELECT principal_id FROM app.administrators LIMIT 1`).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	id := uuid.New()
	token := strings.Repeat("d", 43)
	part := "managed-deletion-" + id.String()[:8]
	region := "geofabrik:" + part
	exec(writer, `INSERT INTO osm_catalog.regions(id,provider,provider_region_id,display_name,catalog_url,source_url,boundary) VALUES($1,'geofabrik',$2,$2,'https://fixture.invalid/catalog','https://fixture.invalid/source',ST_Multi(ST_GeomFromText('POLYGON((0 0,1 0,1 1,0 0))',4326)))`, region, part)
	exec(app, `INSERT INTO app.osm_region_updates(id,administrator_id,region_id,operation,state,slot_held,execution_hash) VALUES($1,$2,$3,'delete','deleting',true,encode(sha256(convert_to($4,'UTF8')),'hex'))`, id, admin, region, token)
	for _, task := range osm.DeleteTasks {
		exec(app, `INSERT INTO app.osm_region_update_steps(update_id,task_order,name,repeated) VALUES($1,$2,$3,false)`, id, task.Order, task.Name)
	}
	environment := map[string]string{"OSM_CONTROL_JOB_ID": id.String(), "OSM_CONTROL_TOKEN": token, "OSM_REGION_ID": region, "OSM_EXECUTOR_DATABASE_URL": executorURL, "OSM_CONTROL_MODE": "run"}
	if err := runManaged(ctx, func(k string) string { return environment[k] }, io.Discard, writer); err == nil {
		t.Fatal("deletion capability launched a rebuild")
	}
	environment["OSM_CONTROL_MODE"] = "delete"
	if err := runManaged(ctx, func(k string) string { return environment[k] }, io.Discard, writer); err != nil {
		t.Fatal(err)
	}
	var valid bool
	if err := app.QueryRow(ctx, `SELECT state='deleting' AND slot_held AND phase='Processing' AND executor_outcome='succeeded'
        AND (SELECT count(*)=3 AND bool_and(state='completed' AND elapsed_total_ms IS NOT NULL) FROM app.osm_region_update_steps WHERE update_id=$1)
        FROM app.osm_region_updates WHERE id=$1`, id).Scan(&valid); err != nil || !valid {
		t.Fatal("managed deletion released capacity or lost task reports", valid, err)
	}
	if complete, err := osm.RegionDeletionComplete(ctx, writer, id.String(), region); err != nil || !complete {
		t.Fatal("managed deletion has no storage receipt", complete, err)
	}
	exec(app, `UPDATE app.osm_region_updates SET slot_held=false,state='succeeded' WHERE id=$1`, id)
}
