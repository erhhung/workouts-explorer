package osmcontroller

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestWallDurationsIncludeMetricsAndExcludePausedGapsPostgreSQL(t *testing.T) {
	url, execURL := os.Getenv("MIGRATION_DATABASE_URL"), os.Getenv("OSM_EXECUTOR_DATABASE_URL")
	if url == "" || execURL == "" {
		t.Skip("owned wall-duration fixture required")
	}
	if !strings.Contains(url, "workouts_admin_test_") {
		t.Fatal("refusing non-test database")
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
	owner, executor := connect(url), connect(execURL)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	var admin uuid.UUID
	if err := owner.QueryRow(ctx, `SELECT principal_id FROM app.administrators LIMIT 1`).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	id, token := uuid.New(), strings.Repeat("w", 43)
	exec(`INSERT INTO app.osm_region_updates(id,administrator_id,region_id,operation,state,slot_held,execution_hash) VALUES($1,$2,'fixture:wall-metrics','download','waiting',true,encode(sha256(convert_to($3,'UTF8')),'hex'))`, id, admin, token)
	exec(`INSERT INTO app.osm_region_update_steps(update_id,task_order,name,repeated) VALUES($1,10,'derive/segments',true),($1,11,'derive/path-primary',false)`, id)
	report := func(event string) {
		t.Helper()
		if _, err := executor.Exec(ctx, `SELECT app.osm_update_event($1,$2,$3::jsonb)`, id, token, event); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Now().UTC().Add(-90 * time.Second)
	report(`{"type":"start","task":10,"batch":1,"batches":2}`)
	exec(`UPDATE app.osm_region_update_steps SET started_at=$2 WHERE update_id=$1 AND task_order=10`, id, base)
	exec(`UPDATE app.osm_region_update_events SET happened_at=$2 WHERE update_id=$1 AND event_type='start'`, id, base)
	report(`{"type":"complete","task":10,"awaitingMetrics":true,"completed":false,"elapsedMilliseconds":300,"elapsedSequence":1}`)
	exec(`UPDATE app.osm_region_update_events SET happened_at=$2::timestamptz+interval '0.3 second' WHERE update_id=$1 AND event_type='complete'`, id, base)
	report(`{"type":"settled","task":10,"completed":false,"elapsedMilliseconds":30000,"elapsedSequence":2}`)
	exec(`UPDATE app.osm_region_update_events SET happened_at=$2::timestamptz+interval '30 seconds' WHERE update_id=$1 AND event_type='settled'`, id, base)
	report(`{"type":"start","task":10,"batch":2,"batches":2}`)
	exec(`UPDATE app.osm_region_update_events SET happened_at=$2::timestamptz+interval '30 seconds' WHERE update_id=$1 AND event_type='start' AND id=(SELECT max(id) FROM app.osm_region_update_events WHERE update_id=$1)`, id, base)
	report(`{"type":"complete","task":10,"completed":true,"awaitingMetrics":true,"elapsedMilliseconds":300,"elapsedSequence":3}`)
	var pending bool
	if err := owner.QueryRow(ctx, `SELECT state='running' AND finished_at IS NULL FROM app.osm_region_update_steps WHERE update_id=$1 AND task_order=10`, id).Scan(&pending); err != nil || !pending {
		t.Fatal("unfinished metrics wait was marked finished", err)
	}
	report(`{"type":"settled","task":10,"completed":true,"elapsedMilliseconds":60000,"elapsedSequence":4}`)
	var duration int64
	var finished time.Time
	if err := owner.QueryRow(ctx, `SELECT elapsed_total_ms,finished_at FROM app.osm_region_update_steps WHERE update_id=$1 AND task_order=10`, id).Scan(&duration, &finished); err != nil || duration < 90000 || duration > 105000 {
		t.Fatal("task used CPU increments instead of first-to-last wall time", duration, err)
	}
	if delta := finished.Sub(base).Milliseconds() - duration; delta < 0 || delta > 1 {
		t.Fatal("displayed timestamps and tooltip duration disagree", delta)
	}
	report(`{"type":"finish","outcome":"succeeded"}`)
	exec(`UPDATE app.osm_region_updates SET state='succeeded',slot_held=false WHERE id=$1`, id)
	var finalized bool
	if err := owner.QueryRow(ctx, `SELECT app.osm_finalize_durations($1)`, id).Scan(&finalized); err != nil || !finalized {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `SELECT elapsed_total_ms FROM app.osm_region_updates WHERE id=$1`, id).Scan(&duration); err != nil || duration < 90000 || duration > 105000 {
		t.Fatal("job total lost metrics wait", duration, err)
	}
	paused := uuid.New()
	exec(`INSERT INTO app.osm_region_updates(id,administrator_id,region_id,operation,state) VALUES($1,$2,'fixture:wall-paused','download','succeeded')`, paused, admin)
	exec(`INSERT INTO app.osm_region_update_steps(update_id,task_order,name,repeated,state,started_at,finished_at) VALUES($1,1,'repeat',true,'completed',$2,$2::timestamptz+interval '1 hour 20 seconds')`, paused, base)
	exec(`INSERT INTO app.osm_region_update_events(update_id,event_type,task_order,happened_at,outcome) VALUES
($1,'start',1,$2,NULL),($1,'finish',NULL,$2::timestamptz+interval '10 seconds','paused'),
($1,'osm.region_update_resume',NULL,$2::timestamptz+interval '1 hour',NULL),
($1,'execution',NULL,$2::timestamptz+interval '1 hour 5 seconds',NULL),
($1,'start',1,$2::timestamptz+interval '1 hour 5 seconds',NULL),
($1,'settled',1,$2::timestamptz+interval '1 hour 20 seconds',NULL),
($1,'finish',NULL,$2::timestamptz+interval '1 hour 20 seconds','succeeded')`, paused, base)
	if err := owner.QueryRow(ctx, `SELECT app.osm_active_duration_ms($1,'-infinity',$2::timestamptz+interval '1 hour 20 seconds')`, paused, base).Scan(&duration); err != nil || duration != 25000 {
		t.Fatal("queued/paused gap included in active wall duration", duration, err)
	}
	if err := owner.QueryRow(ctx, `SELECT app.osm_finalize_durations($1)`, paused).Scan(&finalized); err != nil || !finalized {
		t.Fatal(err)
	}
	if err := owner.QueryRow(ctx, `SELECT elapsed_total_ms FROM app.osm_region_update_steps WHERE update_id=$1 AND task_order=1`, paused).Scan(&duration); err != nil || duration != 25000 {
		t.Fatal("resumed task span disagrees with execution intervals", duration, err)
	}
	t.Log("two short SQL batches retain their ninety-second metrics-inclusive span; job total is independent and paused/queued gaps are excluded")
}
