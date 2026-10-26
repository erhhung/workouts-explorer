package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Call after claiming and fencing the child in tx, before any success transition.
func recordIngestReceiptFixtureManifest(t *testing.T, ctx context.Context, tx pgx.Tx, job uuid.UUID, worker string, lease uuid.UUID, firstDate time.Time, count int) ([]string, time.Time) {
	t.Helper()
	modified := firstDate.Add(12*time.Hour + 123456789*time.Nanosecond).UTC()
	names := make([]string, count)
	candidates := make([]map[string]any, count)
	for index := range candidates {
		date := firstDate.AddDate(0, 0, index).Format(time.DateOnly)
		names[index] = "HealthAutoExport-" + date + ".json"
		candidates[index] = map[string]any{
			"name": names[index], "export_date": date, "size": 1,
			"modified_sec": modified.Unix(), "modified_ns": modified.Nanosecond(),
			"device": 1, "inode": index + 1, "ctime_sec": modified.Unix() + 1, "ctime_ns": 987654321,
			"action": "process",
		}
	}
	manifest, err := json.Marshal(candidates)
	if err != nil {
		t.Fatal(err)
	}
	var state string
	if err := tx.QueryRow(ctx, `SELECT app.record_ingest_file_manifest($1,$2,$3,$4)`, job, worker, lease, manifest).Scan(&state); err != nil || state != "created" {
		t.Fatalf("freeze receipt fixture manifest=%q err=%v", state, err)
	}
	return names, modified.Truncate(time.Microsecond)
}
