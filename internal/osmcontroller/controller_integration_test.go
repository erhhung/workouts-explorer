package osmcontroller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type fakeJobs struct {
	jobs             map[string]*Job
	gone             bool
	creates, deletes int
	ambiguous        bool
	failure          ExecutionFailure
}

func (f *fakeJobs) Get(_ context.Context, name string) (*Job, error) { return f.jobs[name], nil }
func (f *fakeJobs) Create(_ context.Context, spec map[string]any) (*Job, error) {
	f.creates++
	raw, _ := json.Marshal(spec)
	var job Job
	_ = json.Unmarshal(raw, &job)
	job.Metadata.UID = uuid.NewString()
	job.Status.Active = 1
	f.jobs[job.Metadata.Name] = &job
	if f.ambiguous {
		f.ambiguous = false
		return nil, errors.New("lost create response")
	}
	return &job, nil
}
func (f *fakeJobs) Delete(_ context.Context, name, uid string) error {
	if j := f.jobs[name]; j != nil {
		if j.Metadata.UID != uid {
			return errors.New("UID mismatch")
		}
		delete(f.jobs, name)
		f.deletes++
	}
	return nil
}
func (f *fakeJobs) PodsStopped(context.Context, string) (bool, error) { return f.gone, nil }
func (f *fakeJobs) Failure(context.Context, string, string) (ExecutionFailure, error) {
	return f.failure, nil
}

func controllerTemplate() map[string]any {
	var spec map[string]any
	_ = json.Unmarshal([]byte(`{"spec":{"template":{"spec":{"restartPolicy":"Never","automountServiceAccountToken":false,"securityContext":{"runAsNonRoot":true,"runAsUser":65532},"containers":[{"name":"osm-update","image":"harbor.example/workouts-osm:dev@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","securityContext":{"readOnlyRootFilesystem":true,"allowPrivilegeEscalation":false},"env":[],"volumeMounts":[{"name":"scratch","mountPath":"/scratch"},{"name":"tls","mountPath":"/tls/prod-db","readOnly":true}]}],"volumes":[{"name":"scratch","emptyDir":{}},{"name":"tls","secret":{"secretName":"writer-tls"}}]}}}}`), &spec)
	return spec
}

func TestControllerPostgreSQL(t *testing.T) {
	ownerURL, controllerURL, executorURL, catalogURL := os.Getenv("MIGRATION_DATABASE_URL"), os.Getenv("OSM_CONTROLLER_DATABASE_URL"), os.Getenv("OSM_EXECUTOR_DATABASE_URL"), os.Getenv("OSM_CATALOG_DATABASE_URL")
	if ownerURL == "" || controllerURL == "" || executorURL == "" || catalogURL == "" {
		t.Skip("isolated controller databases required")
	}
	if !strings.Contains(ownerURL, "workouts_admin_test_") {
		t.Fatal("refusing non-test database")
	}
	ctx := context.Background()
	pool := func(url string) *pgxpool.Pool {
		p, err := pgxpool.New(ctx, url)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		return p
	}
	owner, db, executor, catalog := pool(ownerURL), pool(controllerURL), pool(executorURL), pool(catalogURL)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	var admin uuid.UUID
	if err := owner.QueryRow(ctx, `SELECT principal_id FROM app.administrators LIMIT 1`).Scan(&admin); err != nil {
		t.Fatal("run administrator fixture first", err)
	}
	definitions, _ := json.Marshal(osm.UpdateTasks)
	add := func(region string) uuid.UUID {
		id := uuid.New()
		exec(`INSERT INTO app.osm_region_updates(id,administrator_id,region_id,operation) VALUES($1,$2,$3,'download')`, id, admin, region)
		exec(`INSERT INTO app.osm_region_update_steps(update_id,task_order,name,repeated) SELECT $1,(value->>'order')::integer,value->>'name',(value->>'repeated')::boolean FROM jsonb_array_elements($2::jsonb)value`, id, definitions)
		return id
	}
	a, b := add("geofabrik:controller-a"), add("geofabrik:controller-b")
	jobs := &fakeJobs{jobs: map[string]*Job{}, gone: true, ambiguous: true}
	controller := &Controller{DB: db, Catalog: catalog, Jobs: jobs, Template: controllerTemplate(), Concurrency: 1, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
	once := func() {
		t.Helper()
		if err := controller.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	once()
	if err := controller.RunOnce(ctx); err == nil {
		t.Fatal("ambiguous create was not exercised")
	}
	once()
	if jobs.creates != 1 {
		t.Fatal("controller duplicated an ambiguous execution")
	}
	state := func(id uuid.UUID) (string, bool, string, string) {
		t.Helper()
		var state, token, name string
		var slot bool
		if err := owner.QueryRow(ctx, `SELECT state,slot_held,COALESCE(execution_token,''),COALESCE(job_name,'') FROM app.osm_region_updates WHERE id=$1`, id).Scan(&state, &slot, &token, &name); err != nil {
			t.Fatal(err)
		}
		return state, slot, token, name
	}
	_, slot, token, name := state(a)
	if !slot {
		t.Fatal("first task has no slot")
	}
	if s, slot, _, _ := state(b); s != "waiting" || slot {
		t.Fatal("second task did not wait")
	}
	event := func(id uuid.UUID, token string, event osm.UpdateProgress) {
		t.Helper()
		raw, _ := json.Marshal(event)
		if _, err := executor.Exec(ctx, `SELECT app.osm_update_event($1,$2,$3::jsonb)`, id, token, raw); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := executor.Exec(ctx, `SELECT * FROM app.osm_region_updates`); err == nil {
		t.Fatal("executor can browse control state")
	}
	if _, err := executor.Exec(ctx, `SELECT app.osm_update_event($1,'bad-token','{"type":"read"}')`, a); err == nil {
		t.Fatal("bad execution capability accepted")
	}
	event(a, token, osm.UpdateProgress{Type: "start", Task: 1})
	if s, _, _, _ := state(a); s != "downloading" {
		t.Fatal("download phase missing")
	}
	event(a, token, osm.UpdateProgress{Type: "complete", Task: 1, Completed: true})
	event(a, token, osm.UpdateProgress{Type: "start", Task: 20, Batch: 25, Batches: 95})
	if s, _, _, _ := state(a); s != "processing" {
		t.Fatal("processing phase missing")
	}
	exec(`UPDATE app.osm_region_updates SET state='pausing',intent='pause' WHERE id=$1`, a)
	event(a, token, osm.UpdateProgress{Type: "finish", Outcome: "paused"})
	once()
	if s, slot, _, _ := state(a); s != "pausing" || !slot {
		t.Fatal("pause released capacity before pod termination")
	}
	jobs.jobs[name].Status.Active = 0
	jobs.jobs[name].Status.Succeeded = 1
	jobs.gone = false
	once()
	once()
	if _, slot, _, _ := state(a); !slot {
		t.Fatal("pause released before owned pods stopped")
	}
	jobs.gone = true
	once()
	if s, slot, _, _ := state(a); s != "paused" || slot {
		t.Fatal("pause did not free capacity")
	}
	if jobs.jobs[name] == nil || jobs.deletes != 0 {
		t.Fatal("terminal updater Job was deleted instead of retained for logs")
	}
	exec(`UPDATE app.osm_region_updates SET state='waiting',intent='run',enqueued_at=clock_timestamp() WHERE id=$1`, a)
	once()
	if s, slot, _, _ := state(a); s != "waiting" || slot {
		t.Fatal("resume bypassed a running queued region")
	}
	_, _, bt, bname := state(b)
	event(b, bt, osm.UpdateProgress{Type: "start", Task: 1})
	exec(`UPDATE app.osm_region_updates SET state='canceling',intent='cancel' WHERE id=$1`, b)
	// Forced loss without a terminal report must launch owned cleanup, not mark
	// cancellation complete or let the queued resume start heavy work.
	delete(jobs.jobs, bname)
	once()
	once()
	var cleanup string
	if err := owner.QueryRow(ctx, `SELECT cleanup_name FROM app.osm_region_updates WHERE id=$1`, b).Scan(&cleanup); err != nil {
		t.Fatal(err)
	}
	if jobs.jobs[cleanup] == nil {
		t.Fatal("missing cleanup execution")
	}
	if s, slot, _, _ := state(b); s != "canceling" || !slot {
		t.Fatal("cleanup was not fenced by capacity")
	}
	failedCleanup := cleanup
	jobs.jobs[cleanup].Status.Active, jobs.jobs[cleanup].Status.Failed = 0, 1
	once()
	once()
	once()
	if err := owner.QueryRow(ctx, `SELECT cleanup_name FROM app.osm_region_updates WHERE id=$1`, b).Scan(&cleanup); err != nil {
		t.Fatal(err)
	}
	if cleanup == failedCleanup || jobs.jobs[failedCleanup] == nil || jobs.jobs[cleanup] == nil {
		t.Fatal("cleanup retry reused/deleted its retained failed Job")
	}
	event(b, bt, osm.UpdateProgress{Type: "finish", Outcome: "canceled"})
	jobs.jobs[cleanup].Status.Active = 0
	jobs.jobs[cleanup].Status.Succeeded = 1
	once()
	once()
	if s, slot, _, _ := state(b); s != "canceled" || slot {
		t.Fatal("cancel did not wait for cleanup completion")
	}
	if jobs.jobs[cleanup] == nil {
		t.Fatal("successful cleanup Job was not retained for logs")
	}
	once()
	_, slot, newToken, newName := state(a)
	if !slot || newToken == token || newName == name {
		t.Fatal("resumed execution did not rotate its identity")
	}
	if _, err := executor.Exec(ctx, `SELECT app.osm_update_event($1,$2,'{"type":"heartbeat"}')`, a, token); err == nil {
		t.Fatal("superseded execution can report")
	}
	foreign := jobs.jobs[newName]
	original := foreign.Metadata.UID
	foreign.Metadata.UID = uuid.NewString()
	before := jobs.deletes
	if err := controller.RunOnce(ctx); err == nil {
		t.Fatal("foreign UID accepted")
	}
	if jobs.deletes != before {
		t.Fatal("controller deleted a foreign execution")
	}
	foreign.Metadata.UID = original
	event(a, newToken, osm.UpdateProgress{Type: "start", Task: 20})
	// An execution created before retention was configured must still be reclaimed.
	var legacySpec map[string]any
	if err := json.Unmarshal(foreign.Spec, &legacySpec); err != nil {
		t.Fatal(err)
	}
	delete(legacySpec, "ttlSecondsAfterFinished")
	foreign.Spec, _ = json.Marshal(legacySpec)
	jobs.failure = ExecutionFailure{Reason: "OOMKilled", ExitCode: 137, MemoryLimit: "1Gi"}
	foreign.Status.Active, foreign.Status.Failed = 0, 1
	once()
	var summary string
	if err := owner.QueryRow(ctx, `SELECT error_summary FROM app.osm_region_updates WHERE id=$1`, a).Scan(&summary); err != nil || summary != jobs.failure.Summary() {
		t.Fatalf("termination evidence was not saved before deletion: %q %v", summary, err)
	}
	once()
	if s, held, _, _ := state(a); s != "failed" || held {
		t.Fatal("OOM execution did not settle after pod removal")
	}
	var retained string
	if err := owner.QueryRow(ctx, `SELECT error_summary FROM app.osm_region_updates WHERE id=$1`, a).Scan(&retained); err != nil || retained != summary {
		t.Fatalf("finalizer overwrote OOM evidence: %q %v", retained, err)
	}
	t.Log("single capacity, queued resume, ambiguous-create adoption, fenced progress, stop/cleanup confirmation and owned-only deletion verified")
	for _, task := range []int{2, len(osm.UpdateTasks)} {
		t.Run(osm.UpdateTasks[task-1].Name+" preserves the specific failure without task prefixes", func(t *testing.T) {
			writerURL := os.Getenv("OSM_CATALOG_TEST_OWNER_URL")
			if writerURL == "" {
				t.Skip("isolated OSM owner fixture required")
			}
			if !strings.Contains(writerURL, "osm_catalog_test") {
				t.Fatal("refusing a non-test OSM database")
			}
			writer := pool(writerURL)
			region := "geofabrik:controller-download-" + uuid.NewString()[:8]
			request := add(region)
			wanted := "Failed to download https://download.geofabrik.de/europe/norway/svalbard-janmayen/svalbard-janmayen-latest.osm.pbf | unexpected HTTP status 503"
			if task == len(osm.UpdateTasks) {
				wanted = "OSM validation gate: materialLocalityResiduals is 4, expected 0"
			}
			if _, err := writer.Exec(ctx, `INSERT INTO osm_catalog.regions(id,provider,provider_region_id,display_name,catalog_url,source_url,boundary)
            VALUES($1,'geofabrik',$2,'Download failure fixture','https://download.geofabrik.de/index-v1.json','https://download.geofabrik.de/test.osm.pbf',ST_Multi(ST_GeomFromText('POLYGON((0 0,1 0,1 1,0 0))',4326)))`, region, strings.TrimPrefix(region, "geofabrik:")); err != nil {
				t.Fatal(err)
			}
			var generation int64
			if err := writer.QueryRow(ctx, `SELECT nextval(pg_get_serial_sequence('osm_catalog.generations','id'))`).Scan(&generation); err != nil {
				t.Fatal(err)
			}
			if _, err := writer.Exec(ctx, `INSERT INTO osm_catalog.generations(id,region_id,state,schema_name,source_url,importer_version,derivation_version,failure_summary,control_job_id)
            OVERRIDING SYSTEM VALUE VALUES($1,$2,'failed',('osm_build_'||$1::bigint)::name,'https://download.geofabrik.de/test.osm.pbf',1,1,$3,$4)`, generation, region, wanted, request); err != nil {
				t.Fatal(err)
			}
			if task == len(osm.UpdateTasks) {
				if _, err := writer.Exec(ctx, `WITH candidate AS (UPDATE osm_catalog.generations SET state='building',failure_summary=NULL WHERE id=$1 RETURNING id)
                INSERT INTO osm_catalog.generation_stages(generation_id,stage,stage_order,state,fence,cursor,started_at,finished_at)
                SELECT id,'validate',1,'completed',repeat('a',64),'{}'::jsonb,clock_timestamp(),clock_timestamp() FROM candidate`, generation); err != nil {
					t.Fatal(err)
				}
				store := osm.PostgreSQLUpdateStore{Pool: writer}
				if err := store.FailStage(ctx, osm.Generation{ID: generation}, "validate", wanted); err != nil {
					t.Fatal(err)
				}
				var retained bool
				if err := writer.QueryRow(ctx, `SELECT state='completed' FROM osm_catalog.generation_stages WHERE generation_id=$1 AND stage='validate'`, generation).Scan(&retained); err != nil || !retained {
					t.Fatal("recording a validation-gate failure changed its committed SQL checkpoint", err)
				}
			}
			once()
			once()
			_, held, token, name := state(request)
			if !held {
				t.Fatal("download fixture not admitted")
			}
			event(request, token, osm.UpdateProgress{Type: "generation", GenerationID: generation})
			event(request, token, osm.UpdateProgress{Type: "start", Task: task})
			event(request, token, osm.UpdateProgress{Type: "finish", Outcome: "failed"})
			jobs.failure = ExecutionFailure{Reason: "Error", ExitCode: 1}
			jobs.jobs[name].Status.Active, jobs.jobs[name].Status.Failed = 0, 1
			once()
			once()
			var summary string
			if err := owner.QueryRow(ctx, `SELECT error_summary FROM app.osm_region_updates WHERE id=$1 AND state='failed' AND NOT slot_held`, request).Scan(&summary); err != nil || summary != wanted {
				t.Fatalf("stored failure=%q, want %q: %v", summary, wanted, err)
			}
			if jobs.jobs[name] == nil {
				t.Fatal("failed Job with configured retention was deleted")
			}
		})
	}

	deletion := uuid.New()
	region := "geofabrik:controller-deletion-" + deletion.String()[:8]
	exec(`INSERT INTO app.osm_region_updates(id,administrator_id,region_id,operation,state) VALUES($1,$2,$3,'delete','deleting')`, deletion, admin, region)
	for _, task := range osm.DeleteTasks {
		exec(`INSERT INTO app.osm_region_update_steps(update_id,task_order,name,repeated) VALUES($1,$2,$3,false)`, deletion, task.Order, task.Name)
	}
	once()
	once()
	_, held, deleteToken, deleteName := state(deletion)
	if !held || !strings.Contains(string(jobs.jobs[deleteName].Spec), `"name":"OSM_CONTROL_MODE","value":"delete"`) {
		t.Fatal("deletion did not dispatch the approved deletion mode")
	}
	event(deletion, deleteToken, osm.UpdateProgress{Type: "start", Task: 1})
	if s, _, _, _ := state(deletion); s != "deleting" {
		t.Fatal("deletion became Downloading", s)
	}
	jobs.jobs[deleteName].Status.Active, jobs.jobs[deleteName].Status.Failed = 0, 1
	jobs.gone = false
	once()
	once()
	if _, held, _, _ := state(deletion); !held {
		t.Fatal("deletion released before its pod stopped")
	}
	jobs.gone = true
	once()
	if s, held, _, _ := state(deletion); s != "deleting" || held {
		t.Fatal("incomplete deletion did not remain queued for cleanup", s, held)
	}
	once()
	if _, held, _, _ := state(deletion); held {
		t.Fatal("cleanup retry ignored dispatch backoff")
	}
	writer := pool(os.Getenv("OSM_CATALOG_TEST_OWNER_URL"))
	for _, q := range []string{
		`INSERT INTO osm_catalog.regions(id,provider,provider_region_id,display_name,catalog_url,source_url,boundary) VALUES($1,'geofabrik',split_part($1,':',2),'Controller deletion','https://fixture.invalid/catalog','https://fixture.invalid/source',ST_Multi(ST_GeomFromText('POLYGON((0 0,1 0,1 1,0 0))',4326)))`,
		`INSERT INTO osm_catalog.region_deletions(id,region_id,state,database_bytes_before,database_bytes_after,finished_at) VALUES($2,$1,'completed',0,0,clock_timestamp())`,
	} {
		args := []any{region}
		if strings.Contains(q, "$2") {
			args = append(args, deletion)
		}
		if _, err := writer.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE app.osm_region_updates SET enqueued_at=clock_timestamp() WHERE id=$1`, deletion)
	once()
	once()
	_, _, deleteToken, deleteName = state(deletion)
	event(deletion, deleteToken, osm.UpdateProgress{Type: "finish", Outcome: "succeeded"})
	jobs.jobs[deleteName].Status.Active, jobs.jobs[deleteName].Status.Succeeded = 0, 1
	once()
	once()
	if s, held, _, _ := state(deletion); s != "succeeded" || held {
		t.Fatal("verified cleanup did not complete deletion", s, held)
	}
	t.Log("deletion dispatch mode, stop confirmation, retry/backoff and independent storage receipt verification passed")
}

func TestControllerRejectsUnsafeTemplates(t *testing.T) {
	c := &Controller{Template: controllerTemplate()}
	e := execution{ID: uuid.New(), Region: "geofabrik:small", Token: "synthetic", Image: c.container()["image"].(string)}
	if _, err := c.specification(e, "controlled-job", "run"); err != nil {
		t.Fatal(err)
	}
	e.Image = "other.example/arbitrary@sha256:" + strings.Repeat("b", 64)
	if _, err := c.specification(e, "controlled-job", "run"); err == nil {
		t.Fatal("foreign image repository accepted")
	}
	e.Image = c.container()["image"].(string)
	pod := c.Template["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	pod["automountServiceAccountToken"] = true
	if _, err := c.specification(e, "controlled-job", "run"); err == nil {
		t.Fatal("Kubernetes token admitted to updater")
	}
}
