package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIngestOutcomesIntegration(t *testing.T) {
	apiURL, migrationURL := os.Getenv("API_DATABASE_URL"), os.Getenv("MIGRATION_DATABASE_URL")
	if os.Getenv("INGEST_OUTCOMES_TEST_DISPOSABLE") != "1" || apiURL == "" || migrationURL == "" {
		t.Skip("INGEST_OUTCOMES_TEST_DISPOSABLE=1 and disposable API_DATABASE_URL/MIGRATION_DATABASE_URL are required")
	}
	var databaseName string
	for _, dsn := range []string{apiURL, migrationURL} {
		cfg, err := pgx.ParseConfig(dsn)
		if err != nil || !strings.Contains(cfg.Database, "test") ||
			(cfg.Host != "127.0.0.1" && cfg.Host != "localhost" && cfg.Host != "::1") {
			t.Fatal("ingest outcome tests require a named loopback test database")
		}
		if databaseName != "" && databaseName != cfg.Database {
			t.Fatal("API and migration connections must use the same test database")
		}
		databaseName = cfg.Database
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	apiDB, err := pgxpool.New(ctx, apiURL)
	if err != nil {
		t.Fatal(err)
	}
	defer apiDB.Close()
	adminDB, err := pgxpool.New(ctx, migrationURL)
	if err != nil {
		t.Fatal(err)
	}
	defer adminDB.Close()
	server := integrationServer(t, apiDB, &recordingSender{})
	handler, err := NewHandlerContext(ctx, server.config, apiDB, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("SQL projection parity under API role", func(t *testing.T) {
		_, account := insertSourceTestUser(t, adminDB)
		tx := integrationAccountTransaction(t, apiDB, account)
		defer tx.Rollback(ctx)
		for _, kind := range []string{"manual_ingest", "scheduled_ingest", "manual_ingest_source", "scheduled_ingest_source", "coverage_update", "coverage_update_route", "workout_deletion", "source_check"} {
			for _, status := range []string{"queued", "running", "succeeded", "partially_succeeded", "failed", "cancelled"} {
				for _, test := range ingestOutcomeProgressCases {
					var got string
					if err := tx.QueryRow(ctx, `SELECT app.ingest_outcome_status($1,$2,$3,$4)`, kind, status,
						test.progress.WorkoutsCreated, test.progress.WorkoutsUpdated).Scan(&got); err != nil {
						t.Fatal(err)
					}
					if want := ingestPresentationStatus(kind, status, test.progress); got != want {
						t.Fatalf("SQL/Go mismatch kind=%s status=%s case=%s SQL=%s Go=%s", kind, status, test.name, got, want)
					}
				}
			}
		}
	})

	for _, test := range []struct {
		name, failureCode, outcome string
		created, updated           int64
	}{
		{"created with invalid file", "source-file-invalid", "partially_succeeded", 2, 0},
		{"updated with source failure", "source-auth-expired", "partially_succeeded", 0, 2},
		{"files skips and unchanged are not imports", "source-file-invalid", "failed", 0, 0},
		{"successful empty sync", "", "succeeded", 0, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			principal, account := insertSourceTestUser(t, adminDB)
			bearer := insertTestSession(t, apiDB, principal, "bearer", "")
			foreignPrincipal, foreignAccount := insertSourceTestUser(t, adminDB)
			foreignBearer := insertTestSession(t, apiDB, foreignPrincipal, "bearer", "")
			var sourceIDs []string
			for _, name := range []string{"Unsuccessful", "Empty successful"} {
				source := createIntegrationSource(t, server, bearer, name, "/data/workouts/ingest-outcomes-"+compactUUID(uuid.New()))
				id, _ := parseCompactUUID(source.Id)
				setIntegrationSourceStatus(t, adminDB, account, id, "connected")
				sourceIDs = append(sourceIDs, source.Id)
			}
			body, err := json.Marshal(map[string]any{"sourceIds": sourceIDs, "startDate": "2024-01-01", "endDate": "2024-12-31"})
			if err != nil {
				t.Fatal(err)
			}
			enqueued := routeIngest(handler, string(body), bearer, "", false)
			var accepted generated.IngestAccepted
			if enqueued.recorder.Code != http.StatusAccepted || json.Unmarshal(enqueued.recorder.Body.Bytes(), &accepted) != nil {
				t.Fatalf("enqueue status=%d body=%s", enqueued.recorder.Code, enqueued.recorder.Body.String())
			}
			read := func(path, credential string, out any) {
				t.Helper()
				call := routeJobRequest(handler, http.MethodGet, path, "", credential, "", false)
				if call.recorder.Code != http.StatusOK {
					t.Fatalf("GET %s status=%d body=%s", path, call.recorder.Code, call.recorder.Body.String())
				}
				validateRecordedResponse(t, http.MethodGet, strings.Split(path, "?")[0], call.recorder)
				if err := json.Unmarshal(call.recorder.Body.Bytes(), out); err != nil {
					t.Fatal(err)
				}
			}
			var queued generated.JobDetail
			read("/api/jobs/"+accepted.JobId, bearer, &queued)
			if len(queued.Children) != 2 {
				t.Fatalf("queued children=%+v", queued.Children)
			}
			children := make(map[string]string)
			for _, child := range queued.Children {
				children[child.Source.SourceId] = child.Id
			}
			const worker = "api-ingest-outcomes"
			// Native transitions leave the unsuccessful source failed for retry.
			// Historical failed parents are covered by the Go matrix and the
			// migration rehearsal; never bypass terminal immutability to seed one.
			for index, source := range sourceIDs {
				child, _ := parseCompactUUID(children[source])
				lease := uuid.New()
				tx := integrationAccountTransaction(t, adminDB, account)
				var changed bool
				if err := tx.QueryRow(ctx, `SELECT app.claim_job($1,$2,$3,interval '5 minutes')`, child, worker, lease).Scan(&changed); err != nil || !changed {
					t.Fatalf("claim=%t err=%v", changed, err)
				}
				status := "succeeded"
				var failureCode, failureSummary any
				if index == 0 && test.failureCode != "" {
					status, failureCode, failureSummary = "failed", test.failureCode, "Synthetic source failure."
					var sourceID uuid.UUID
					if err := tx.QueryRow(ctx, `SELECT source_id FROM app.fence_ingest_job($1,$2,$3,$4)`, child, worker, lease, database.SupportedSchemaVersion).Scan(&sourceID); err != nil {
						t.Fatal(err)
					}
					names, modified := recordIngestReceiptFixtureManifest(t, ctx, tx, child, worker, lease,
						time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), 2)
					for index, state := range []string{"succeeded", "failed"} {
						file := uuid.New()
						if _, err := tx.Exec(ctx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes,modified_at,state,processing_started_at)
							VALUES($1,$2,$3,$4,$5,1,$6,'processing',clock_timestamp())`, file, account, sourceID, child, names[index], modified); err != nil {
							t.Fatal(err)
						}
						if _, err := tx.Exec(ctx, `UPDATE app.source_files SET state=$2,processed_at=clock_timestamp(),
							checksum_sha256=CASE WHEN $2='succeeded' THEN decode(repeat('ab',32),'hex') END,
							failure_code=CASE WHEN $2='failed' THEN 'source-file-invalid' END,
							failure_summary=CASE WHEN $2='failed' THEN 'Synthetic invalid file.' END WHERE id=$1`, file, state); err != nil {
							t.Fatal(err)
						}
					}
					if err := tx.QueryRow(ctx, `SELECT app.record_ingest_progress($1,$2,$3,3,1,1,1,$4,$5,3,0)`,
						child, worker, lease, test.created, test.updated).Scan(&changed); err != nil || !changed {
						t.Fatalf("progress=%t err=%v", changed, err)
					}
				}
				if err := tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				tx = integrationAccountTransaction(t, adminDB, account)
				if err := tx.QueryRow(ctx, `SELECT app.finish_job($1,$2,$3,$4,$5,$6)`, child, worker, lease, status, failureCode, failureSummary).Scan(&changed); err != nil || !changed {
					t.Fatalf("finish=%t err=%v", changed, err)
				}
				if err := tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
			}
			parent, _ := parseCompactUUID(accepted.JobId)
			snapshot := func() string {
				t.Helper()
				tx := integrationAccountTransaction(t, adminDB, account)
				defer tx.Rollback(ctx)
				var result string
				if err := tx.QueryRow(ctx, `SELECT jsonb_build_object(
					'jobs',(SELECT jsonb_agg(to_jsonb(j) ORDER BY id) FROM app.jobs j WHERE id=$1 OR parent_job_id=$1),
					'progress',(SELECT jsonb_agg(to_jsonb(p) ORDER BY job_id) FROM app.job_progress p JOIN app.jobs j ON j.id=p.job_id AND j.account_id=p.account_id WHERE j.id=$1 OR j.parent_job_id=$1),
					'notifications',(SELECT jsonb_agg(to_jsonb(n) ORDER BY id) FROM app.notifications n WHERE job_id=$1))::text`, parent).Scan(&result); err != nil {
					t.Fatal(err)
				}
				return result
			}
			before := snapshot()
			var detail generated.JobDetail
			read("/api/jobs/"+accepted.JobId, bearer, &detail)
			if string(detail.Status) != test.outcome || detail.DisplayStatus != nil || detail.Progress.WorkoutsCreated != test.created || detail.Progress.WorkoutsUpdated != test.updated {
				t.Fatalf("parent outcome=%+v", detail)
			}
			var child generated.JobDetail
			read("/api/jobs/"+children[sourceIDs[0]], bearer, &child)
			for _, nested := range detail.Children {
				if nested.Id == child.Id && !reflect.DeepEqual(nested, child) {
					t.Fatal("nested and direct source detail disagree")
				}
			}
			if test.failureCode != "" {
				if string(child.Status) != "failed" || child.FailureCode == nil || *child.FailureCode != test.failureCode {
					t.Fatalf("lost failed-source evidence: %+v", child)
				}
				if test.outcome == "partially_succeeded" {
					if child.DisplayStatus == nil || string(*child.DisplayStatus) != test.outcome {
						t.Fatalf("failed source with imports must display partial: %+v", child)
					}
				} else if child.DisplayStatus != nil {
					t.Fatal("zero imported workouts must remain failed")
				}
			}
			for _, status := range []string{"failed", "partially_succeeded", "succeeded"} {
				for _, page := range []string{"1", "2"} {
					var history generated.JobList
					read("/api/jobs?operation=manual_sync&status="+status+"&pageSize=1&page="+page, bearer, &history)
					want := int64(0)
					if status == test.outcome {
						want = 1
					}
					if history.Pagination.TotalItems != want || history.Pagination.TotalPages != want ||
						len(history.Items) != int(want)*map[string]int{"1": 1, "2": 0}[page] {
						t.Fatalf("history status=%s page=%s: %+v", status, page, history)
					}
					if len(history.Items) == 1 && (history.Items[0].Id != detail.Id || history.Items[0].Status != detail.Status || !reflect.DeepEqual(history.Items[0].DisplayStatus, detail.DisplayStatus)) {
						t.Fatalf("history/detail disagree: %+v %+v", history.Items[0], detail)
					}
				}
			}
			var sync generated.DataSync
			read("/api/data-sync", bearer, &sync)
			if sync.ActiveJob != nil || sync.LatestJob == nil || sync.LatestJob.Id != detail.Id || sync.LatestJob.Status != detail.Status || !reflect.DeepEqual(sync.LatestJob.DisplayStatus, detail.DisplayStatus) {
				t.Fatalf("sync/detail disagree: %+v", sync)
			}
			var notifications generated.NotificationList
			read("/api/notifications", bearer, &notifications)
			var terminal *generated.Notification
			for index := range notifications.Items {
				item := &notifications.Items[index]
				if item.Type == "ingest-terminal" && item.JobId != nil && *item.JobId == detail.Id {
					terminal = item
				}
			}
			if terminal == nil || string(terminal.Severity) != map[string]string{"partially_succeeded": "warning", "failed": "error", "succeeded": "info"}[test.outcome] {
				t.Fatalf("terminal notification=%+v", terminal)
			}
			wantTitle, wantMessage := "Data sync completed", "Your requested data sync completed successfully."
			if test.outcome == "partially_succeeded" {
				wantTitle = "Data sync partially completed"
				wantMessage = "Data sync partially completed. One or more sources could not finish. Successfully imported data has been kept. You can retry this sync."
				if test.failureCode == "source-file-invalid" {
					wantMessage = "Data sync partially completed. Some source files could not be processed. Successfully imported data has been kept. You can retry this sync."
				}
			} else if test.outcome == "failed" {
				wantTitle = "Data sync failed"
				wantMessage = "Data sync could not be completed because source files could not be processed. You can retry this sync."
			}
			if terminal.Title != wantTitle || terminal.Message != wantMessage {
				t.Fatalf("terminal title/message=%+v", terminal)
			}
			apiTx := integrationAccountTransaction(t, apiDB, account)
			notificationID, _ := parseCompactUUID(terminal.Id)
			byID, err := readNotificationByID(ctx, apiTx, notificationID)
			byID.CreatedAt = byID.CreatedAt.UTC()
			byID.UpdatedAt = byID.UpdatedAt.UTC()
			if err != nil || !reflect.DeepEqual(*terminal, byID) {
				t.Fatalf("notification list/by-ID differ: %+v %+v %v", terminal, byID, err)
			}
			_ = apiTx.Rollback(ctx)
			foundBanner := false
			for _, item := range sync.Notifications {
				if item.Id == terminal.Id {
					foundBanner = true
					if !reflect.DeepEqual(item, *terminal) {
						t.Fatal("Data Sync banner differs from notification list")
					}
				}
			}
			if !foundBanner {
				t.Fatal("Data Sync omitted the active terminal banner")
			}
			var foreignJobs generated.JobList
			read("/api/jobs?operation=manual_sync", foreignBearer, &foreignJobs)
			var foreignSync generated.DataSync
			read("/api/data-sync", foreignBearer, &foreignSync)
			var foreignNotifications generated.NotificationList
			read("/api/notifications", foreignBearer, &foreignNotifications)
			if len(foreignJobs.Items) != 0 || foreignJobs.Pagination.TotalItems != 0 || foreignSync.LatestJob != nil || foreignSync.ActiveJob != nil || len(foreignSync.Notifications) != 0 || len(foreignNotifications.Items) != 0 {
				t.Fatal("ingest outcomes leaked across accounts")
			}
			foreignTx := integrationAccountTransaction(t, apiDB, foreignAccount)
			if _, err := readNotificationByID(ctx, foreignTx, notificationID); !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("foreign notification by ID: %v", err)
			}
			_ = foreignTx.Rollback(ctx)
			for _, id := range []string{detail.Id, child.Id} {
				for _, method := range []string{http.MethodGet, http.MethodPost} {
					path, body := "/api/jobs/"+id, ""
					if method == http.MethodPost {
						path, body = path+"/retry", `{}`
					}
					call := routeJobRequest(handler, method, path, body, foreignBearer, "", false)
					if call.recorder.Code != http.StatusNotFound {
						t.Fatalf("foreign %s status=%d body=%s", path, call.recorder.Code, call.recorder.Body.String())
					}
				}
			}
			if after := snapshot(); before != after {
				t.Fatal("outcome reads changed persisted execution evidence or notifications")
			}
			if test.failureCode == "" {
				return
			}
			retried := routeJobRequest(handler, http.MethodPost, "/api/jobs/"+detail.Id+"/retry", `{}`, bearer, "", false)
			var retry generated.IngestAccepted
			if retried.recorder.Code != http.StatusAccepted || json.Unmarshal(retried.recorder.Body.Bytes(), &retry) != nil {
				t.Fatalf("retry status=%d body=%s", retried.recorder.Code, retried.recorder.Body.String())
			}
			var retryDetail generated.JobDetail
			read("/api/jobs/"+retry.JobId, bearer, &retryDetail)
			if len(retryDetail.Children) != 1 || retryDetail.Children[0].Source.SourceId != sourceIDs[0] ||
				retryDetail.Children[0].RetryOfJobId == nil || *retryDetail.Children[0].RetryOfJobId != child.Id {
				t.Fatalf("retry did not select only the failed source: %+v", retryDetail)
			}
			if after := snapshot(); before != after {
				t.Fatal("retry changed original execution evidence or notification")
			}
		})
	}
}
