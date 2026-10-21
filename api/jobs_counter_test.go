package api

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/google/uuid"
)

func TestJobRouteCounterSerialization(t *testing.T) {
	for _, kind := range []string{"manual_ingest", "scheduled_ingest", "manual_ingest_source", "scheduled_ingest_source"} {
		for _, known := range []bool{true, false} {
			t.Run(kind+"/known="+map[bool]string{true: "true", false: "false"}[known], func(t *testing.T) {
				row := make(activityDisplayRow, 60)
				row[0], row[2], row[3] = uuid.New(), kind, "running"
				row[19], row[20] = int64(516), int64(33)
				if known {
					created, updated := int64(491), int64(0)
					row[23], row[24] = &created, &updated
				}
				detail, _, err := scanJobDetail(row)
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := json.Marshal(detail)
				if err != nil {
					t.Fatal(err)
				}
				if detail.Progress.WorkoutsCreated != 516 || detail.Progress.WorkoutsUpdated != 33 {
					t.Fatalf("totals lost: %s", encoded)
				}
				for _, field := range []string{`"workoutsCreatedWithRoute":491`, `"workoutsUpdatedWithRoute":0`} {
					count := strings.Count(string(encoded), field)
					if (known && count != 2) || (!known && count != 0) {
						t.Fatalf("nullable counter %s appears %d times: %s", field, count, encoded)
					}
				}
			})
		}
	}
}

func TestDataSyncRouteCounterScan(t *testing.T) {
	for _, active := range []bool{true, false} {
		for _, known := range []bool{true, false} {
			row := make(activityDisplayRow, 19)
			row[0], row[1], row[2] = uuid.New(), "scheduled_ingest", generated.JobStatus("running")
			row[13], row[14] = int64(516), int64(33)
			if known {
				created, updated := int64(491), int64(0)
				row[17], row[18] = &created, &updated
			}
			item, err := readDataSyncJob(context.Background(), activityDisplayTx{total: row}, active)
			if err != nil {
				t.Fatal(err)
			}
			if (item.Progress.WorkoutsCreatedWithRoute != nil) != known || (item.Progress.WorkoutsUpdatedWithRoute != nil) != known {
				t.Fatalf("nullable snapshot counters: %+v", item.Progress)
			}
		}
	}
}
