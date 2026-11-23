package osmcontroller

import (
	"context"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"strings"
	"testing"
)

func TestSuccessfulElapsedCountersPostgreSQL(t *testing.T) {
	ownerURL, executorURL := os.Getenv("MIGRATION_DATABASE_URL"), os.Getenv("OSM_EXECUTOR_DATABASE_URL")
	if ownerURL == "" || executorURL == "" {
		t.Skip("owned elapsed-counter database required")
	}
	if !strings.Contains(ownerURL, "workouts_admin_test_") {
		t.Fatal("refusing non-test database")
	}
	ctx := context.Background()
	owner, err := pgxpool.New(ctx, ownerURL)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	executor, err := pgxpool.New(ctx, executorURL)
	if err != nil {
		t.Fatal(err)
	}
	defer executor.Close()
	var admin uuid.UUID
	if err = owner.QueryRow(ctx, `SELECT principal_id FROM app.administrators LIMIT 1`).Scan(&admin); err != nil {
		t.Fatal(err)
	}
	id, token := uuid.New(), strings.Repeat("a", 43)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := owner.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO app.osm_region_updates(id,administrator_id,region_id,operation,state,slot_held,execution_hash,started_at) VALUES($1,$2,'geofabrik:elapsed-fixture','download','processing',true,encode(sha256(convert_to($3,'UTF8')),'hex'),clock_timestamp()-interval '2 days')`, id, admin, token)
	exec(`INSERT INTO app.osm_region_update_steps(update_id,task_order,name,repeated) VALUES($1,1,'download',false),($1,2,'tags-filter',false),($1,57,'validate/finalize',false)`, id)
	report := func(secret, event string) {
		t.Helper()
		if _, err := executor.Exec(ctx, `SELECT app.osm_update_event($1,$2,$3::jsonb)`, id, secret, event); err != nil {
			t.Fatal(err)
		}
	}
	report(token, `{"type":"start","task":1}`)
	exec(`UPDATE app.osm_region_update_steps SET started_at=clock_timestamp()-interval '160 seconds' WHERE update_id=$1 AND task_order=1`, id)
	exec(`UPDATE app.osm_region_update_events SET happened_at=clock_timestamp()-interval '160 seconds' WHERE update_id=$1 AND event_type='start'`, id)
	event := `{"type":"complete","task":1,"completed":true,"awaitingMetrics":true,"elapsedMilliseconds":1,"elapsedSequence":1}`
	report(token, event)
	var before int64
	if err := owner.QueryRow(ctx, `SELECT elapsed_total_ms FROM app.osm_region_updates WHERE id=$1`, id).Scan(&before); err != nil {
		t.Fatal(err)
	}
	report(token, event)
	total := func(minimum, maximum int64) {
		t.Helper()
		var actual int64
		if err := owner.QueryRow(ctx, `SELECT elapsed_total_ms FROM app.osm_region_updates WHERE id=$1`, id).Scan(&actual); err != nil || actual < minimum || actual > maximum {
			t.Fatalf("elapsed total=%d want[%d,%d]: %v", actual, minimum, maximum, err)
		}
	}
	total(before, before)
	var pending bool
	if err := owner.QueryRow(ctx, `SELECT state='running' AND finished_at IS NULL FROM app.osm_region_update_steps WHERE update_id=$1 AND task_order=1`, id).Scan(&pending); err != nil || !pending {
		t.Fatal("metrics wait was presented as finished", err)
	}
	report(token, `{"type":"settled","task":1,"completed":true,"elapsedMilliseconds":1,"elapsedSequence":2}`)
	total(160000, 170000)
	report(token, `{"type":"finish","outcome":"paused"}`)
	total(160000, 170000)
	next := strings.Repeat("b", 43)
	exec(`UPDATE app.osm_region_updates SET state='processing',execution_hash=encode(sha256(convert_to($2,'UTF8')),'hex') WHERE id=$1`, id, next)
	report(next, `{"type":"start","task":2}`)
	report(next, `{"type":"finish","outcome":"failed"}`)
	total(160000, 170000)
	report(next, `{"type":"complete","task":2,"completed":true,"elapsedMilliseconds":2000,"elapsedSequence":1}`)
	total(160000, 170000)
	report(next, `{"type":"elapsed","task":57,"elapsedMilliseconds":3000,"elapsedSequence":2}`)
	total(160000, 170000)
	var first int64
	if err := owner.QueryRow(ctx, `SELECT elapsed_total_ms FROM app.osm_region_update_steps WHERE update_id=$1 AND task_order=1`, id).Scan(&first); err != nil || first < 160000 || first > 170000 {
		t.Fatal("task elapsed duplicated", first, err)
	}
	if _, err := executor.Exec(ctx, `SELECT app.osm_update_event_base($1,$2,'{"type":"read"}')`, id, next); err == nil {
		t.Fatal("executor bypassed elapsed/capability wrapper")
	}
	if _, err := executor.Exec(ctx, `SELECT app.osm_update_event($1,$2,'{"type":"read"}')`, id, token); err == nil {
		t.Fatal("superseded capability accepted")
	}
	exec(`UPDATE app.osm_region_updates SET slot_held=false,state='failed' WHERE id=$1`, id)
}
