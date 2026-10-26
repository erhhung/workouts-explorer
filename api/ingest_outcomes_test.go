package api

import (
	"context"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/google/uuid"
)

var ingestOutcomeProgressCases = []struct {
	name     string
	progress generated.JobProgress
	imported bool
}{
	{"empty", generated.JobProgress{}, false},
	{"created only", generated.JobProgress{WorkoutsCreated: 1}, true},
	{"updated only", generated.JobProgress{WorkoutsUpdated: 1}, true},
	{"created and updated", generated.JobProgress{WorkoutsCreated: 516, WorkoutsUpdated: 33}, true},
	{"successful files only", generated.JobProgress{FilesDiscovered: 2, FilesSucceeded: 1, FilesFailed: 1}, false},
	{"skips only", generated.JobProgress{FilesDiscovered: 1, FilesSkipped: 1}, false},
	{"unchanged only", generated.JobProgress{WorkoutsUnchanged: 10}, false},
	{"rejected only", generated.JobProgress{WorkoutsRejected: 10}, false},
	{"all without writes", generated.JobProgress{FilesDiscovered: 3, FilesSucceeded: 1, FilesSkipped: 1, FilesFailed: 1, WorkoutsUnchanged: 10, WorkoutsRejected: 2}, false},
	{"large counters without sum overflow", generated.JobProgress{WorkoutsCreated: math.MaxInt64, WorkoutsUpdated: math.MaxInt64}, true},
}

func TestIngestPresentationStatus(t *testing.T) {
	for _, kind := range []string{"manual_ingest", "scheduled_ingest", "manual_ingest_source", "scheduled_ingest_source", "coverage_update", "coverage_update_route", "workout_deletion", "source_check", "unknown"} {
		for _, status := range []string{"queued", "running", "succeeded", "partially_succeeded", "failed", "cancelled"} {
			for _, test := range ingestOutcomeProgressCases {
				t.Run(kind+"/"+status+"/"+test.name, func(t *testing.T) {
					want := status
					ingest := kind == "manual_ingest" || kind == "scheduled_ingest" || kind == "manual_ingest_source" || kind == "scheduled_ingest_source"
					if ingest && (status == "failed" || status == "partially_succeeded") {
						want = "failed"
						if test.imported {
							want = "partially_succeeded"
						}
					}
					if got := ingestPresentationStatus(kind, status, test.progress); got != want {
						t.Fatalf("status=%s want=%s progress=%+v", got, want, test.progress)
					}
					if !ingest {
						return
					}
					row := make(activityDisplayRow, 60)
					id := uuid.New()
					row[0], row[2], row[3] = id, kind, status
					row[15], row[16], row[17], row[18] = test.progress.FilesDiscovered, test.progress.FilesSkipped, test.progress.FilesSucceeded, test.progress.FilesFailed
					row[19], row[20], row[21], row[22] = test.progress.WorkoutsCreated, test.progress.WorkoutsUpdated, test.progress.WorkoutsUnchanged, test.progress.WorkoutsRejected
					failure := "source-file-invalid"
					row[10] = &failure
					detail, gotKind, err := scanJobDetail(row)
					if err != nil || gotKind != kind || string(detail.Status) != status || detail.Progress != test.progress || detail.FailureCode == nil || *detail.FailureCode != failure {
						t.Fatalf("scan changed execution evidence: %+v kind=%s err=%v", detail, gotKind, err)
					}
					assertDisplay := func(display *generated.JobDisplayStatus) {
						t.Helper()
						if want == status {
							if display != nil {
								t.Fatalf("redundant displayStatus=%s", *display)
							}
						} else if display == nil || string(*display) != want {
							t.Fatalf("displayStatus=%v want=%s", display, want)
						}
					}
					assertDisplay(detail.DisplayStatus)
					if kind == "manual_ingest_source" || kind == "scheduled_ingest_source" {
						return
					}
					syncRow := make(activityDisplayRow, 19)
					syncRow[0], syncRow[1], syncRow[2] = id, kind, generated.JobStatus(status)
					copy(syncRow[9:], row[15:25])
					item, err := readDataSyncJob(context.Background(), activityDisplayTx{total: syncRow}, status == "queued" || status == "running")
					if err != nil || string(item.Status) != status || item.Progress != test.progress {
						t.Fatalf("snapshot changed execution evidence: %+v err=%v", item, err)
					}
					assertDisplay(item.DisplayStatus)
				})
			}
		}
	}
}

func TestIngestNotificationProjectionPreservesEvidence(t *testing.T) {
	const genericPartial = "Data sync partially completed. One or more sources could not finish. Successfully imported data has been kept. You can retry this sync."
	const filePartial = "Data sync partially completed. Some source files could not be processed. Successfully imported data has been kept. You can retry this sync."
	const failed = "Data sync could not be completed. Check the source details, then retry this sync."
	for _, test := range []struct {
		name, notificationType, kind, status string
		created, updated                     int64
		fileOnly                             bool
		severity, title, message             string
	}{
		{"historical file failure", "ingest-terminal", "manual_ingest", "failed", 516, 0, true, "warning", "Data sync partially completed", filePartial},
		{"historical source failure", "ingest-terminal", "scheduled_ingest", "failed", 0, 33, false, "warning", "Data sync partially completed", genericPartial},
		{"historical empty partial", "ingest-terminal", "manual_ingest", "partially_succeeded", 0, 0, true, "error", "Data sync failed", failed},
		{"current partial", "ingest-terminal", "manual_ingest", "partially_succeeded", 1, 0, true, "info", "Stored title", "Stored message"},
		{"current failed", "ingest-terminal", "manual_ingest", "failed", 0, 0, true, "info", "Stored title", "Stored message"},
		{"successful empty", "ingest-terminal", "manual_ingest", "succeeded", 0, 0, false, "info", "Stored title", "Stored message"},
		{"cancelled with imports", "ingest-terminal", "manual_ingest", "cancelled", 1, 0, true, "info", "Stored title", "Stored message"},
		{"source connection failure", "source-connection-failed", "manual_ingest", "failed", 1, 0, true, "info", "Stored title", "Stored message"},
		{"deletion", "workout-deletion-terminal", "workout_deletion", "partially_succeeded", 0, 0, true, "info", "Stored title", "Stored message"},
		{"coverage", "ingest-terminal", "coverage_update", "failed", 1, 0, true, "info", "Stored title", "Stored message"},
		{"missing job", "ingest-terminal", "", "", 0, 0, false, "info", "Stored title", "Stored message"},
	} {
		for _, state := range []generated.NotificationState{generated.Unresolved, generated.Remind, generated.Dismissed, generated.Resolved} {
			t.Run(test.name+"/"+string(state), func(t *testing.T) {
				id, job, source := uuid.New(), uuid.New(), uuid.New()
				created := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
				updated, resolved, remind := created.Add(time.Hour), created.Add(2*time.Hour), created.Add(3*time.Hour)
				row := activityDisplayRow{id, test.notificationType, generated.NotificationSeverityInfo, state, generated.NotificationSubjectTypeJob,
					&job, &job, &source, "Stored title", "Stored message", created, updated, &resolved, &remind,
					test.kind, test.status, test.created, test.updated, test.fileOnly}
				want := generated.Notification{Id: compactUUID(id), Type: test.notificationType, Severity: generated.NotificationSeverity(test.severity), State: state,
					SubjectType: generated.NotificationSubjectTypeJob, SubjectId: stringPointer(compactUUID(job)), JobId: stringPointer(compactUUID(job)), SourceId: stringPointer(compactUUID(source)),
					Title: test.title, Message: test.message, CreatedAt: created, UpdatedAt: updated, ResolvedAt: &resolved, RemindAt: &remind}
				item, err := scanNotification(row)
				if err != nil || !reflect.DeepEqual(item, want) {
					t.Fatalf("notification=%+v want=%+v err=%v", item, want, err)
				}
				var queries []string
				tx := activityDisplayTx{rows: []activityDisplayRow{row, row}, total: row, queries: &queries}
				items, err := readNotifications(context.Background(), tx, "active", 2, 1)
				if err != nil || len(items) != 2 || !reflect.DeepEqual(items[0], want) || !reflect.DeepEqual(items[1], want) || len(queries) != 1 {
					t.Fatalf("bounded notification reader=%+v queries=%d err=%v", items, len(queries), err)
				}
				if strings.Contains(queries[0], "SELECT *") || !strings.Contains(queries[0], "LIMIT $3 OFFSET $4)") || !strings.Contains(queries[0], "j.account_id=n.account_id") || !strings.Contains(queries[0], "file.account_id=child.account_id") {
					t.Fatal("notification projection must be page-bounded and account-scoped")
				}
				item, err = readNotificationByID(context.Background(), tx, id)
				if err != nil || !reflect.DeepEqual(item, want) || len(queries) != 2 {
					t.Fatalf("notification by ID=%+v queries=%d err=%v", item, len(queries), err)
				}
			})
		}
	}
}
