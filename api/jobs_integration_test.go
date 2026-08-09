package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestJobOwnerAPIsIntegration(t *testing.T) {
	databaseURL, migrationURL := os.Getenv("API_DATABASE_URL"), os.Getenv("MIGRATION_DATABASE_URL")
	if databaseURL == "" || migrationURL == "" {
		t.Skip("API_DATABASE_URL and MIGRATION_DATABASE_URL are required")
	}
	ctx := context.Background()
	apiDB, err := pgxpool.New(ctx, databaseURL)
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
	routerContext, cancel := context.WithCancel(ctx)
	defer cancel()
	handler, err := NewHandlerContext(routerContext, server.config, apiDB, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}

	principalID, accountID := insertSourceTestUser(t, adminDB)
	bearer := insertTestSession(t, apiDB, principalID, "bearer", "")
	sourceA := createIntegrationSource(t, server, bearer, "Job source A", "/data/workouts/job-source-a")
	sourceB := createIntegrationSource(t, server, bearer, "Job source B", "/data/workouts/job-source-b")
	for _, source := range []generated.Source{sourceA, sourceB} {
		id, _ := parseCompactUUID(source.Id)
		setIntegrationSourceStatus(t, adminDB, accountID, id, "connected")
	}
	cookie := insertTestSession(t, apiDB, principalID, "cookie", "jobs-csrf")
	enqueued := routeIngest(handler, `{"sourceIds":["`+sourceB.Id+`","`+sourceA.Id+`"]}`, cookie, "jobs-csrf", true)
	if enqueued.recorder.Code != http.StatusAccepted {
		t.Fatalf("enqueue status=%d body=%s", enqueued.recorder.Code, enqueued.recorder.Body.String())
	}
	var accepted generated.IngestAccepted
	if err := json.Unmarshal(enqueued.recorder.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}

	listed := routeJobRequest(handler, http.MethodGet, "/api/jobs?page=1&pageSize=1", "", bearer, "", false)
	if listed.recorder.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listed.recorder.Code, listed.recorder.Body.String())
	}
	var list generated.JobList
	if err := json.Unmarshal(listed.recorder.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].Id != accepted.JobId || list.Pagination.PageSize != 1 || list.Pagination.TotalItems < 1 {
		t.Fatalf("unexpected list=%#v", list)
	}
	validateRecordedResponse(t, http.MethodGet, "/api/jobs?page=1&pageSize=1", listed.recorder)

	detailed := routeJobRequest(handler, http.MethodGet, "/api/jobs/"+accepted.JobId, "", bearer, "", false)
	if detailed.recorder.Code != http.StatusOK {
		t.Fatalf("detail status=%d body=%s", detailed.recorder.Code, detailed.recorder.Body.String())
	}
	validateRecordedResponse(t, http.MethodGet, "/api/jobs/"+accepted.JobId, detailed.recorder)
	var detail generated.JobDetail
	if err := json.Unmarshal(detailed.recorder.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if len(detail.Children) != 2 || detail.Children[0].Source == nil || detail.Children[1].Source == nil ||
		detail.Children[0].Source.SourceId != sourceA.Id || detail.Children[1].Source.SourceId != sourceB.Id ||
		detail.RetryRootJobId != nil || detail.RetryOrdinal != nil || detail.Children[0].RetryRootJobId != nil ||
		detail.Children[0].RetryOrdinal != nil || detail.Children[1].RetryRootJobId != nil || detail.Children[1].RetryOrdinal != nil {
		t.Fatalf("children are not safely source-sorted: %#v", detail.Children)
	}
	directFiles := integrationAccountTransaction(t, apiDB, accountID)
	defer directFiles.Rollback(ctx)
	var directFileRows int
	acceptedID, _ := parseCompactUUID(accepted.JobId)
	if err := directFiles.QueryRow(ctx, `SELECT count(*) FROM app.read_owned_job_files($1,10,0)`, acceptedID).Scan(&directFileRows); err != nil {
		t.Fatalf("direct owned files reader: %v", err)
	}
	if err := directFiles.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"files", "events", "logs"} {
		call := routeJobRequest(handler, http.MethodGet, "/api/jobs/"+accepted.JobId+"/"+suffix+"?page=1&pageSize=10", "", bearer, "", false)
		if call.recorder.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", suffix, call.recorder.Code, call.recorder.Body.String())
		}
		validateRecordedResponse(t, http.MethodGet, "/api/jobs/"+accepted.JobId+"/"+suffix+"?page=1&pageSize=10", call.recorder)
	}
	childDetail := routeJobRequest(handler, http.MethodGet, "/api/jobs/"+detail.Children[0].Id, "", bearer, "", false)
	if childDetail.recorder.Code != http.StatusOK {
		t.Fatalf("child detail status=%d body=%s", childDetail.recorder.Code, childDetail.recorder.Body.String())
	}
	validateRecordedResponse(t, http.MethodGet, "/api/jobs/"+detail.Children[0].Id, childDetail.recorder)

	foreignPrincipal, _ := insertSourceTestUser(t, adminDB)
	foreignBearer := insertTestSession(t, apiDB, foreignPrincipal, "bearer", "")
	foreign := routeJobRequest(handler, http.MethodGet, "/api/jobs/"+accepted.JobId, "", foreignBearer, "", false)
	if foreign.recorder.Code != http.StatusNotFound {
		t.Fatalf("foreign detail status=%d body=%s", foreign.recorder.Code, foreign.recorder.Body.String())
	}
	administrator := insertIngestTestAdministrator(t, adminDB)
	adminBearer := insertTestSession(t, apiDB, administrator, "bearer", "")
	admin := routeJobRequest(handler, http.MethodGet, "/api/jobs", "", adminBearer, "", false)
	if admin.recorder.Code != http.StatusForbidden {
		t.Fatalf("administrator list status=%d body=%s", admin.recorder.Code, admin.recorder.Body.String())
	}

	cancelled := routeJobRequest(handler, http.MethodPost, "/api/jobs/"+accepted.JobId+"/cancellation", `{}`, cookie, "jobs-csrf", true)
	if cancelled.recorder.Code != http.StatusOK {
		t.Fatalf("cancellation status=%d body=%s", cancelled.recorder.Code, cancelled.recorder.Body.String())
	}
	validateRecordedResponse(t, http.MethodPost, "/api/jobs/"+accepted.JobId+"/cancellation", cancelled.recorder)
	detail = generated.JobDetail{}
	if err := json.Unmarshal(cancelled.recorder.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Status != generated.JobStatus("cancelled") || len(detail.Children) != 2 ||
		detail.Children[0].Status != generated.JobStatus("cancelled") || detail.Children[1].Status != generated.JobStatus("cancelled") {
		t.Fatalf("unexpected cancellation detail=%#v", detail)
	}
	originalChildren := append([]generated.JobDetail(nil), detail.Children...)
	notifications := routeJobRequest(handler, http.MethodGet, "/api/notifications?page=1&pageSize=10", "", bearer, "", false)
	if notifications.recorder.Code != http.StatusOK {
		t.Fatalf("notifications status=%d body=%s", notifications.recorder.Code, notifications.recorder.Body.String())
	}
	validateRecordedResponse(t, http.MethodGet, "/api/notifications?page=1&pageSize=10", notifications.recorder)
	var notificationList generated.NotificationList
	if err := json.Unmarshal(notifications.recorder.Body.Bytes(), &notificationList); err != nil || len(notificationList.Items) == 0 {
		t.Fatalf("notifications=%#v err=%v", notificationList, err)
	}
	dismissal := routeJobRequest(handler, http.MethodPost, "/api/notifications/"+notificationList.Items[0].Id+"/dismissal", `{}`, cookie, "jobs-csrf", true)
	if dismissal.recorder.Code != http.StatusOK {
		t.Fatalf("dismissal status=%d body=%s", dismissal.recorder.Code, dismissal.recorder.Body.String())
	}
	validateRecordedResponse(t, http.MethodPost, "/api/notifications/"+notificationList.Items[0].Id+"/dismissal", dismissal.recorder)
	seedNotifications := integrationAccountTransaction(t, adminDB, accountID)
	defer seedNotifications.Rollback(ctx)
	if _, err := seedNotifications.Exec(ctx, `INSERT INTO app.notifications
		(id,account_id,type,severity,condition_key,subject_type,title,message)
		SELECT gen_random_uuid(),$1,'test-terminal','info','truncation:'||value,'account','Test notification','Safe test message.'
		FROM generate_series(1,101) value`, accountID); err != nil {
		t.Fatalf("seed valid terminal notifications: %v", err)
	}
	if err := seedNotifications.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	dataSync := routeJobRequest(handler, http.MethodGet, "/api/data-sync", "", bearer, "", false)
	if dataSync.recorder.Code != http.StatusOK {
		t.Fatalf("data sync status=%d body=%s", dataSync.recorder.Code, dataSync.recorder.Body.String())
	}
	validateRecordedResponse(t, http.MethodGet, "/api/data-sync", dataSync.recorder)
	var syncSnapshot generated.DataSync
	if err := json.Unmarshal(dataSync.recorder.Body.Bytes(), &syncSnapshot); err != nil ||
		len(syncSnapshot.Notifications) != 100 || !syncSnapshot.NotificationsTruncated {
		t.Fatalf("data sync notification truncation=%d/%t err=%v", len(syncSnapshot.Notifications), syncSnapshot.NotificationsTruncated, err)
	}
	parentID, _ := parseCompactUUID(accepted.JobId)
	tx := integrationAccountTransaction(t, adminDB, accountID)
	defer tx.Rollback(ctx)
	var snapshots int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.job_config_snapshots snapshot
		JOIN app.jobs child ON child.id=snapshot.job_id WHERE child.parent_job_id=$1`, parentID).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if snapshots != 0 {
		t.Fatalf("cancelled queued children retained %d snapshots", snapshots)
	}
	_ = tx.Rollback(ctx)

	terminal := routeJobRequest(handler, http.MethodPost, "/api/jobs/"+accepted.JobId+"/cancellation", `{}`, bearer, "", false)
	if terminal.recorder.Code != http.StatusConflict {
		t.Fatalf("terminal cancellation status=%d body=%s", terminal.recorder.Code, terminal.recorder.Body.String())
	}
	retried := routeJobRequest(handler, http.MethodPost, "/api/jobs/"+accepted.JobId+"/retry", `{}`, cookie, "jobs-csrf", true)
	if retried.recorder.Code != http.StatusAccepted {
		t.Fatalf("retry status=%d body=%s", retried.recorder.Code, retried.recorder.Body.String())
	}
	validateRecordedResponse(t, http.MethodPost, "/api/jobs/"+accepted.JobId+"/retry", retried.recorder)
	var retryAccepted generated.IngestAccepted
	if err := json.Unmarshal(retried.recorder.Body.Bytes(), &retryAccepted); err != nil {
		t.Fatal(err)
	}
	retryDetailCall := routeJobRequest(handler, http.MethodGet, "/api/jobs/"+retryAccepted.JobId, "", bearer, "", false)
	validateRecordedResponse(t, http.MethodGet, "/api/jobs/"+retryAccepted.JobId, retryDetailCall.recorder)
	detail = generated.JobDetail{}
	if err := json.Unmarshal(retryDetailCall.recorder.Body.Bytes(), &detail); err != nil || detail.RetryOfJobId == nil ||
		*detail.RetryOfJobId != accepted.JobId || detail.RetryRootJobId == nil || *detail.RetryRootJobId != accepted.JobId ||
		detail.RetryOrdinal == nil || *detail.RetryOrdinal != 1 || len(detail.Children) != 2 {
		t.Fatalf("retry lineage detail=%#v err=%v", detail, err)
	}
	for index := range detail.Children {
		if detail.Children[index].RetryOfJobId == nil || *detail.Children[index].RetryOfJobId != originalChildren[index].Id ||
			detail.Children[index].RetryRootJobId == nil || *detail.Children[index].RetryRootJobId != originalChildren[index].Id ||
			detail.Children[index].RetryOrdinal == nil || *detail.Children[index].RetryOrdinal != 1 {
			t.Fatalf("child retry lineage=%#v", detail.Children)
		}
	}
	originalDetailCall := routeJobRequest(handler, http.MethodGet, "/api/jobs/"+accepted.JobId, "", bearer, "", false)
	detail = generated.JobDetail{}
	if err := json.Unmarshal(originalDetailCall.recorder.Body.Bytes(), &detail); err != nil ||
		len(detail.RetriedByJobIds) != 1 || detail.RetriedByJobIds[0] != retryAccepted.JobId ||
		detail.LatestRetryJobId == nil || *detail.LatestRetryJobId != retryAccepted.JobId ||
		detail.LatestRetryOrdinal == nil || *detail.LatestRetryOrdinal != 1 {
		t.Fatalf("reverse retry lineage=%#v err=%v", detail.RetriedByJobIds, err)
	}
	historyCall := routeJobRequest(handler, http.MethodGet, "/api/jobs?page=1&pageSize=100", "", bearer, "", false)
	var history generated.JobList
	if err := json.Unmarshal(historyCall.recorder.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	var originalListed, retryListed bool
	for _, item := range history.Items {
		originalListed = originalListed || item.Id == accepted.JobId
		retryListed = retryListed || item.Id == retryAccepted.JobId
	}
	if historyCall.recorder.Code != http.StatusOK || originalListed || !retryListed {
		t.Fatalf("retry history original=%t retry=%t status=%d", originalListed, retryListed, historyCall.recorder.Code)
	}
	foreignRetry := routeJobRequest(handler, http.MethodPost, "/api/jobs/"+accepted.JobId+"/retry", `{}`, foreignBearer, "", false)
	if foreignRetry.recorder.Code != http.StatusNotFound {
		t.Fatalf("foreign retry status=%d", foreignRetry.recorder.Code)
	}
	foreignRetryDetail := routeJobRequest(handler, http.MethodGet, "/api/jobs/"+retryAccepted.JobId, "", foreignBearer, "", false)
	if foreignRetryDetail.recorder.Code != http.StatusNotFound {
		t.Fatalf("foreign retry detail status=%d body=%s", foreignRetryDetail.recorder.Code, foreignRetryDetail.recorder.Body.String())
	}
	adminRetry := routeJobRequest(handler, http.MethodPost, "/api/jobs/"+accepted.JobId+"/retry", `{}`, adminBearer, "", false)
	if adminRetry.recorder.Code != http.StatusForbidden {
		t.Fatalf("administrator retry status=%d", adminRetry.recorder.Code)
	}
	retryCancel := routeJobRequest(handler, http.MethodPost, "/api/jobs/"+retryAccepted.JobId+"/cancellation", `{}`, cookie, "jobs-csrf", true)
	if retryCancel.recorder.Code != http.StatusOK {
		t.Fatalf("retry cleanup status=%d body=%s", retryCancel.recorder.Code, retryCancel.recorder.Body.String())
	}
	previousRetryID := retryAccepted.JobId
	for ordinal := 2; ordinal <= 3; ordinal++ {
		nextRetry := routeJobRequest(handler, http.MethodPost, "/api/jobs/"+previousRetryID+"/retry", `{}`, cookie, "jobs-csrf", true)
		var nextAccepted generated.IngestAccepted
		if nextRetry.recorder.Code != http.StatusAccepted || json.Unmarshal(nextRetry.recorder.Body.Bytes(), &nextAccepted) != nil {
			t.Fatalf("retry ordinal %d status=%d body=%s", ordinal, nextRetry.recorder.Code, nextRetry.recorder.Body.String())
		}
		nextDetailCall := routeJobRequest(handler, http.MethodGet, "/api/jobs/"+nextAccepted.JobId, "", bearer, "", false)
		validateRecordedResponse(t, http.MethodGet, "/api/jobs/"+nextAccepted.JobId, nextDetailCall.recorder)
		var nextDetail generated.JobDetail
		if err := json.Unmarshal(nextDetailCall.recorder.Body.Bytes(), &nextDetail); err != nil || nextDetail.RetryOfJobId == nil ||
			*nextDetail.RetryOfJobId != previousRetryID || nextDetail.RetryRootJobId == nil || *nextDetail.RetryRootJobId != accepted.JobId ||
			nextDetail.RetryOrdinal == nil || *nextDetail.RetryOrdinal != ordinal || len(nextDetail.Children) != len(originalChildren) {
			t.Fatalf("retry ordinal %d detail=%#v err=%v", ordinal, nextDetail, err)
		}
		for index := range nextDetail.Children {
			child := nextDetail.Children[index]
			if child.RetryRootJobId == nil || *child.RetryRootJobId != originalChildren[index].Id ||
				child.RetryOrdinal == nil || *child.RetryOrdinal != ordinal {
				t.Fatalf("retry ordinal %d child lineage=%#v", ordinal, nextDetail.Children)
			}
		}
		if ordinal == 3 {
			childDetailCall := routeJobRequest(handler, http.MethodGet, "/api/jobs/"+nextDetail.Children[0].Id, "", bearer, "", false)
			validateRecordedResponse(t, http.MethodGet, "/api/jobs/"+nextDetail.Children[0].Id, childDetailCall.recorder)
			var childDetail generated.JobDetail
			if err := json.Unmarshal(childDetailCall.recorder.Body.Bytes(), &childDetail); err != nil || childDetail.RetryRootJobId == nil ||
				*childDetail.RetryRootJobId != originalChildren[0].Id || childDetail.RetryOrdinal == nil || *childDetail.RetryOrdinal != ordinal {
				t.Fatalf("direct retry child detail=%#v err=%v", childDetail, err)
			}
		}
		cleanup := routeJobRequest(handler, http.MethodPost, "/api/jobs/"+nextAccepted.JobId+"/cancellation", `{}`, cookie, "jobs-csrf", true)
		if cleanup.recorder.Code != http.StatusOK {
			t.Fatalf("retry ordinal %d cleanup status=%d body=%s", ordinal, cleanup.recorder.Code, cleanup.recorder.Body.String())
		}
		previousRetryID = nextAccepted.JobId
	}
	directRetry := routeJobRequest(handler, http.MethodPost, "/api/jobs/"+originalChildren[0].Id+"/retry", `{}`, cookie, "jobs-csrf", true)
	if directRetry.recorder.Code != http.StatusAccepted {
		t.Fatalf("direct child retry status=%d body=%s", directRetry.recorder.Code, directRetry.recorder.Body.String())
	}
	var directAccepted generated.IngestAccepted
	if err := json.Unmarshal(directRetry.recorder.Body.Bytes(), &directAccepted); err != nil {
		t.Fatal(err)
	}
	directDetailCall := routeJobRequest(handler, http.MethodGet, "/api/jobs/"+directAccepted.JobId, "", bearer, "", false)
	detail = generated.JobDetail{}
	if err := json.Unmarshal(directDetailCall.recorder.Body.Bytes(), &detail); err != nil || detail.RetryOfJobId == nil ||
		*detail.RetryOfJobId != accepted.JobId || detail.RetryRootJobId == nil || *detail.RetryRootJobId != accepted.JobId ||
		detail.RetryOrdinal == nil || *detail.RetryOrdinal != 1 || len(detail.Children) != 1 || detail.Children[0].RetryOfJobId == nil ||
		*detail.Children[0].RetryOfJobId != originalChildren[0].Id || detail.Children[0].RetryRootJobId == nil ||
		*detail.Children[0].RetryRootJobId != originalChildren[0].Id || detail.Children[0].RetryOrdinal == nil ||
		*detail.Children[0].RetryOrdinal != 1 {
		t.Fatalf("direct child retry lineage=%#v err=%v", detail, err)
	}
	originalChildCall := routeJobRequest(handler, http.MethodGet, "/api/jobs/"+originalChildren[0].Id, "", bearer, "", false)
	var originalChild generated.JobDetail
	if err := json.Unmarshal(originalChildCall.recorder.Body.Bytes(), &originalChild); err != nil ||
		!slices.Contains(originalChild.RetriedByJobIds, detail.Children[0].Id) {
		t.Fatalf("direct child reverse lineage=%#v err=%v", originalChild.RetriedByJobIds, err)
	}
	directCancel := routeJobRequest(handler, http.MethodPost, "/api/jobs/"+directAccepted.JobId+"/cancellation", `{}`, cookie, "jobs-csrf", true)
	if directCancel.recorder.Code != http.StatusOK {
		t.Fatalf("direct retry cleanup status=%d body=%s", directCancel.recorder.Code, directCancel.recorder.Body.String())
	}
	concurrent := make([]endpointCall, 2)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := range concurrent {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			concurrent[index] = routeJobRequest(handler, http.MethodPost, "/api/jobs/"+accepted.JobId+"/retry", `{}`, cookie, "jobs-csrf", true)
		}(index)
	}
	close(start)
	wait.Wait()
	var concurrentAccepted [2]generated.IngestAccepted
	for index, call := range concurrent {
		if call.recorder.Code != http.StatusAccepted || json.Unmarshal(call.recorder.Body.Bytes(), &concurrentAccepted[index]) != nil {
			t.Fatalf("concurrent retry %d status=%d body=%s", index, call.recorder.Code, call.recorder.Body.String())
		}
	}
	if concurrentAccepted[0].JobId != concurrentAccepted[1].JobId || concurrentAccepted[0].Reused == concurrentAccepted[1].Reused {
		t.Fatalf("concurrent retry results=%#v", concurrentAccepted)
	}
	concurrentCleanup := routeJobRequest(handler, http.MethodPost, "/api/jobs/"+concurrentAccepted[0].JobId+"/cancellation", `{}`, cookie, "jobs-csrf", true)
	if concurrentCleanup.recorder.Code != http.StatusOK {
		t.Fatalf("concurrent retry cleanup status=%d", concurrentCleanup.recorder.Code)
	}
	changedConfig := callEndpoint(http.MethodPatch, "/api/sources/"+sourceA.Id,
		`{"config":{"version":1,"path":"/data/workouts/job-source-a-current"}}`, bearer, "")
	server.UpdateSource(changedConfig.recorder, changedConfig.request, sourceA.Id, generated.UpdateSourceParams{})
	if changedConfig.recorder.Code != http.StatusOK {
		t.Fatalf("changed retry config status=%d body=%s", changedConfig.recorder.Code, changedConfig.recorder.Body.String())
	}
	var currentSource generated.Source
	if err := json.Unmarshal(changedConfig.recorder.Body.Bytes(), &currentSource); err != nil {
		t.Fatal(err)
	}
	originalGeneration := originalChildren[0].Source.Generation
	if currentSource.Generation != originalGeneration+1 {
		t.Fatalf("source config generations original/current=%d/%d", originalGeneration, currentSource.Generation)
	}
	currentSourceAID, _ := parseCompactUUID(sourceA.Id)
	setIntegrationSourceStatus(t, adminDB, accountID, currentSourceAID, "connected")
	currentRetry := routeJobRequest(handler, http.MethodPost, "/api/jobs/"+originalChildren[0].Id+"/retry", `{}`, cookie, "jobs-csrf", true)
	var currentAccepted generated.IngestAccepted
	if currentRetry.recorder.Code != http.StatusAccepted || json.Unmarshal(currentRetry.recorder.Body.Bytes(), &currentAccepted) != nil {
		t.Fatalf("current config retry status=%d body=%s", currentRetry.recorder.Code, currentRetry.recorder.Body.String())
	}
	currentDetailCall := routeJobRequest(handler, http.MethodGet, "/api/jobs/"+currentAccepted.JobId, "", bearer, "", false)
	detail = generated.JobDetail{}
	if err := json.Unmarshal(currentDetailCall.recorder.Body.Bytes(), &detail); err != nil || len(detail.Children) != 1 ||
		detail.Children[0].Source == nil || detail.Children[0].Source.Generation != currentSource.Generation || currentAccepted.Reused {
		var currentGeneration int64
		if len(detail.Children) == 1 && detail.Children[0].Source != nil {
			currentGeneration = detail.Children[0].Source.Generation
		}
		t.Fatalf("current config retry generations original/source/job=%d/%d/%d reused=%t detail=%#v err=%v",
			originalGeneration, currentSource.Generation, currentGeneration, currentAccepted.Reused, detail, err)
	}
	currentCleanup := routeJobRequest(handler, http.MethodPost, "/api/jobs/"+currentAccepted.JobId+"/cancellation", `{}`, cookie, "jobs-csrf", true)
	if currentCleanup.recorder.Code != http.StatusOK {
		t.Fatalf("current config retry cleanup status=%d", currentCleanup.recorder.Code)
	}
	deletedSource := createIntegrationSource(t, server, bearer, "Deleted retry source", "/data/workouts/deleted-retry-source")
	deletedSourceID, _ := parseCompactUUID(deletedSource.Id)
	setIntegrationSourceStatus(t, adminDB, accountID, deletedSourceID, "connected")
	deletedEnqueue := routeIngest(handler, `{"sourceIds":["`+deletedSource.Id+`"]}`, bearer, "", false)
	var deletedAccepted generated.IngestAccepted
	if deletedEnqueue.recorder.Code != http.StatusAccepted || json.Unmarshal(deletedEnqueue.recorder.Body.Bytes(), &deletedAccepted) != nil {
		t.Fatalf("deleted retry fixture status=%d body=%s", deletedEnqueue.recorder.Code, deletedEnqueue.recorder.Body.String())
	}
	deletedCancel := routeJobRequest(handler, http.MethodPost, "/api/jobs/"+deletedAccepted.JobId+"/cancellation", `{}`, cookie, "jobs-csrf", true)
	if deletedCancel.recorder.Code != http.StatusOK {
		t.Fatalf("deleted retry fixture cancellation status=%d", deletedCancel.recorder.Code)
	}
	deleteRequest := callEndpoint(http.MethodDelete, "/api/sources/"+deletedSource.Id, "", bearer, "")
	server.DeleteSource(deleteRequest.recorder, deleteRequest.request, deletedSource.Id, generated.DeleteSourceParams{})
	if deleteRequest.recorder.Code != http.StatusNoContent {
		t.Fatalf("delete retry source status=%d body=%s", deleteRequest.recorder.Code, deleteRequest.recorder.Body.String())
	}
	deletedRetry := routeJobRequest(handler, http.MethodPost, "/api/jobs/"+deletedAccepted.JobId+"/retry", `{}`, cookie, "jobs-csrf", true)
	if deletedRetry.recorder.Code != http.StatusConflict || strings.Contains(deletedRetry.recorder.Body.String(), "/data/workouts") {
		t.Fatalf("deleted source retry status=%d body=%s", deletedRetry.recorder.Code, deletedRetry.recorder.Body.String())
	}
	partialEnqueue := routeIngest(handler, `{"sourceIds":["`+sourceA.Id+`","`+sourceB.Id+`"]}`, bearer, "", false)
	var partialAccepted generated.IngestAccepted
	if partialEnqueue.recorder.Code != http.StatusAccepted || json.Unmarshal(partialEnqueue.recorder.Body.Bytes(), &partialAccepted) != nil {
		t.Fatalf("partial fixture status=%d body=%s", partialEnqueue.recorder.Code, partialEnqueue.recorder.Body.String())
	}
	partialDetailCall := routeJobRequest(handler, http.MethodGet, "/api/jobs/"+partialAccepted.JobId, "", bearer, "", false)
	detail = generated.JobDetail{}
	if err := json.Unmarshal(partialDetailCall.recorder.Body.Bytes(), &detail); err != nil || len(detail.Children) != 2 {
		t.Fatalf("partial fixture detail=%#v err=%v", detail, err)
	}
	failedPartialChild := detail.Children[1].Id
	for index, terminalStatus := range []string{"succeeded", "failed"} {
		childID, _ := parseCompactUUID(detail.Children[index].Id)
		lease := uuid.New()
		finishPartial := integrationAccountTransaction(t, adminDB, accountID)
		defer finishPartial.Rollback(ctx)
		var claimed, finished bool
		if err := finishPartial.QueryRow(ctx, `SELECT app.claim_job($1,'partial-retry-worker',$2,interval '1 minute')`, childID, lease).Scan(&claimed); err != nil || !claimed {
			t.Fatalf("partial child claim=%t err=%v", claimed, err)
		}
		if err := finishPartial.QueryRow(ctx, `SELECT app.finish_job($1,'partial-retry-worker',$2,$3)`, childID, lease, terminalStatus).Scan(&finished); err != nil || !finished {
			t.Fatalf("partial child finish=%t err=%v", finished, err)
		}
		if err := finishPartial.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	partialRetry := routeJobRequest(handler, http.MethodPost, "/api/jobs/"+partialAccepted.JobId+"/retry", `{}`, cookie, "jobs-csrf", true)
	var partialRetryAccepted generated.IngestAccepted
	if partialRetry.recorder.Code != http.StatusAccepted || json.Unmarshal(partialRetry.recorder.Body.Bytes(), &partialRetryAccepted) != nil {
		t.Fatalf("partial retry status=%d body=%s", partialRetry.recorder.Code, partialRetry.recorder.Body.String())
	}
	partialRetryDetail := routeJobRequest(handler, http.MethodGet, "/api/jobs/"+partialRetryAccepted.JobId, "", bearer, "", false)
	detail = generated.JobDetail{}
	if err := json.Unmarshal(partialRetryDetail.recorder.Body.Bytes(), &detail); err != nil || len(detail.Children) != 1 ||
		detail.Children[0].RetryOfJobId == nil || *detail.Children[0].RetryOfJobId != failedPartialChild {
		t.Fatalf("partial retry selected children=%#v err=%v", detail.Children, err)
	}
	partialCleanup := routeJobRequest(handler, http.MethodPost, "/api/jobs/"+partialRetryAccepted.JobId+"/cancellation", `{}`, cookie, "jobs-csrf", true)
	if partialCleanup.recorder.Code != http.StatusOK {
		t.Fatalf("partial retry cleanup status=%d", partialCleanup.recorder.Code)
	}

	runningEnqueue := routeIngest(handler, `{"sourceIds":["`+sourceA.Id+`"]}`, bearer, "", false)
	if runningEnqueue.recorder.Code != http.StatusAccepted {
		t.Fatalf("running fixture enqueue status=%d body=%s", runningEnqueue.recorder.Code, runningEnqueue.recorder.Body.String())
	}
	if err := json.Unmarshal(runningEnqueue.recorder.Body.Bytes(), &accepted); err != nil {
		t.Fatal(err)
	}
	runningParent := routeJobRequest(handler, http.MethodGet, "/api/jobs/"+accepted.JobId, "", bearer, "", false)
	detail = generated.JobDetail{}
	if err := json.Unmarshal(runningParent.recorder.Body.Bytes(), &detail); err != nil || len(detail.Children) != 1 {
		t.Fatalf("running fixture detail=%#v err=%v", detail, err)
	}
	runningChild, _ := parseCompactUUID(detail.Children[0].Id)
	lease := uuid.New()
	claim := integrationAccountTransaction(t, adminDB, accountID)
	defer claim.Rollback(ctx)
	var claimed bool
	if err := claim.QueryRow(ctx, `SELECT app.claim_job($1,'jobs-running-cancel',$2,interval '1 minute')`, runningChild, lease).Scan(&claimed); err != nil || !claimed {
		t.Fatalf("running child claim=%t err=%v", claimed, err)
	}
	if err := claim.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	runningCancel := routeJobRequest(handler, http.MethodPost, "/api/jobs/"+detail.Children[0].Id+"/cancellation", `{}`, cookie, "jobs-csrf", true)
	if runningCancel.recorder.Code != http.StatusOK {
		t.Fatalf("running cancellation status=%d body=%s", runningCancel.recorder.Code, runningCancel.recorder.Body.String())
	}
	detail = generated.JobDetail{}
	if err := json.Unmarshal(runningCancel.recorder.Body.Bytes(), &detail); err != nil || detail.Status != generated.JobStatus("running") || !detail.CancelRequested {
		t.Fatalf("running cancellation detail=%#v err=%v", detail, err)
	}
	validateRecordedResponse(t, http.MethodPost, "/api/jobs/"+detail.Id+"/cancellation", runningCancel.recorder)
	finish := integrationAccountTransaction(t, adminDB, accountID)
	defer finish.Rollback(ctx)
	var finished bool
	if err := finish.QueryRow(ctx, `SELECT app.finish_job($1,'jobs-running-cancel',$2,'cancelled')`, runningChild, lease).Scan(&finished); err != nil || !finished {
		t.Fatalf("running cancellation cleanup=%t err=%v", finished, err)
	}
	if err := finish.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestJobRetryLimitAndSourceLineageIntegration(t *testing.T) {
	databaseURL, migrationURL := os.Getenv("API_DATABASE_URL"), os.Getenv("MIGRATION_DATABASE_URL")
	if databaseURL == "" || migrationURL == "" {
		t.Skip("API_DATABASE_URL and MIGRATION_DATABASE_URL are required")
	}
	ctx := context.Background()
	apiDB, err := pgxpool.New(ctx, databaseURL)
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
	principalID, accountID := insertSourceTestUser(t, adminDB)
	bearer := insertTestSession(t, apiDB, principalID, "bearer", "")
	cookie := insertTestSession(t, apiDB, principalID, "cookie", "retry-limit-csrf")
	sourceA := createIntegrationSource(t, server, bearer, "Retry lineage source A", "/data/workouts/retry-lineage-a")
	sourceB := createIntegrationSource(t, server, bearer, "Retry lineage source B", "/data/workouts/retry-lineage-b")
	sourceAID, _ := parseCompactUUID(sourceA.Id)
	sourceBID, _ := parseCompactUUID(sourceB.Id)
	setIntegrationSourceStatus(t, adminDB, accountID, sourceAID, "connected")
	setIntegrationSourceStatus(t, adminDB, accountID, sourceBID, "connected")
	parameters := `{"sourceId":"` + sourceA.Id + `","generation":` + strconv.FormatInt(sourceA.Generation, 10) + `,"mode":"incremental"}`

	parentIDs := make([]uuid.UUID, maxJobRetryOrdinal)
	childIDs := make([]uuid.UUID, maxJobRetryOrdinal)
	seed := integrationAccountTransaction(t, adminDB, accountID)
	defer seed.Rollback(ctx)
	for index := range parentIDs {
		parentIDs[index], childIDs[index] = uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
		if index == 0 {
			_, err = seed.Exec(ctx, `INSERT INTO app.jobs(id,account_id,kind,priority) VALUES($1,$2,'manual_ingest',80)`, parentIDs[index], accountID)
		} else {
			_, err = seed.Exec(ctx, `INSERT INTO app.jobs(id,account_id,kind,priority,retry_of_job_id)
				VALUES($1,$2,'manual_ingest',80,$3)`, parentIDs[index], accountID, parentIDs[index-1])
		}
		if err != nil {
			t.Fatalf("seed retry parent %d: %v", index, err)
		}
		if index == 0 {
			_, err = seed.Exec(ctx, `INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority,parameters)
				VALUES($1,$2,$3,'manual_ingest_source',80,$4)`, childIDs[index], parentIDs[index], accountID, parameters)
		} else {
			_, err = seed.Exec(ctx, `INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority,parameters,retry_of_job_id)
				VALUES($1,$2,$3,'manual_ingest_source',80,$4,$5)`, childIDs[index], parentIDs[index], accountID, parameters, childIDs[index-1])
		}
		if err != nil {
			t.Fatalf("seed retry child %d: %v", index, err)
		}
		if _, err = seed.Exec(ctx, `INSERT INTO app.job_source_contexts(job_id,account_id,source_id,source_generation,display_name,source_type)
			SELECT $1,$2,id,generation,display_name,type FROM app.sources WHERE id=$3`, childIDs[index], accountID, sourceAID); err != nil {
			t.Fatalf("seed retry child context %d: %v", index, err)
		}
	}
	allJobIDs := append(append(make([]uuid.UUID, 0, len(parentIDs)+len(childIDs)), parentIDs...), childIDs...)
	if _, err = seed.Exec(ctx, `SELECT set_config('app.job_transition','cancel',true)`); err != nil {
		t.Fatal(err)
	}
	if _, err = seed.Exec(ctx, `UPDATE app.jobs SET status='cancelled',terminal_at=transaction_timestamp(),
		cancel_requested_at=transaction_timestamp(),cancel_requested_by=$1 WHERE id=ANY($2::uuid[])`, principalID, allJobIDs); err != nil {
		t.Fatal(err)
	}
	if err = seed.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	ordinal99 := routeJobRequest(handler, http.MethodGet, "/api/jobs/"+compactUUID(parentIDs[maxJobRetryOrdinal-1]), "", bearer, "", false)
	var detail generated.JobDetail
	if ordinal99.recorder.Code != http.StatusOK || json.Unmarshal(ordinal99.recorder.Body.Bytes(), &detail) != nil ||
		detail.RetryOrdinal == nil || *detail.RetryOrdinal != maxJobRetryOrdinal-1 {
		t.Fatalf("ordinal 99 detail status=%d body=%s", ordinal99.recorder.Code, ordinal99.recorder.Body.String())
	}
	retry100 := routeJobRequest(handler, http.MethodPost, "/api/jobs/"+compactUUID(parentIDs[maxJobRetryOrdinal-1])+"/retry", `{}`, cookie, "retry-limit-csrf", true)
	var accepted generated.IngestAccepted
	if retry100.recorder.Code != http.StatusAccepted || json.Unmarshal(retry100.recorder.Body.Bytes(), &accepted) != nil {
		t.Fatalf("100th retry status=%d body=%s", retry100.recorder.Code, retry100.recorder.Body.String())
	}
	ordinal100 := routeJobRequest(handler, http.MethodGet, "/api/jobs/"+accepted.JobId, "", bearer, "", false)
	detail = generated.JobDetail{}
	if ordinal100.recorder.Code != http.StatusOK || json.Unmarshal(ordinal100.recorder.Body.Bytes(), &detail) != nil ||
		detail.RetryOrdinal == nil || *detail.RetryOrdinal != maxJobRetryOrdinal {
		t.Fatalf("ordinal 100 detail status=%d body=%s", ordinal100.recorder.Code, ordinal100.recorder.Body.String())
	}
	cleanup100 := routeJobRequest(handler, http.MethodPost, "/api/jobs/"+accepted.JobId+"/cancellation", `{}`, cookie, "retry-limit-csrf", true)
	if cleanup100.recorder.Code != http.StatusOK {
		t.Fatalf("100th retry cleanup status=%d body=%s", cleanup100.recorder.Code, cleanup100.recorder.Body.String())
	}
	retry101 := routeJobRequest(handler, http.MethodPost, "/api/jobs/"+accepted.JobId+"/retry", `{}`, cookie, "retry-limit-csrf", true)
	if retry101.recorder.Code != http.StatusConflict || !strings.Contains(retry101.recorder.Body.String(), "job retry limit has been reached") {
		t.Fatalf("101st retry status=%d body=%s", retry101.recorder.Code, retry101.recorder.Body.String())
	}
	validateRecordedResponse(t, http.MethodPost, "/api/jobs/"+accepted.JobId+"/retry", retry101.recorder)

	goodParent, goodChild := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	badParent, badChild := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())
	adversarial := integrationAccountTransaction(t, adminDB, accountID)
	defer adversarial.Rollback(ctx)
	for _, fixture := range []struct {
		parentID uuid.UUID
		childID  uuid.UUID
		sourceID uuid.UUID
	}{{goodParent, goodChild, sourceAID}, {badParent, badChild, sourceBID}} {
		if _, err = adversarial.Exec(ctx, `INSERT INTO app.jobs(id,account_id,kind,priority,retry_of_job_id)
			VALUES($1,$2,'manual_ingest',80,$3)`, fixture.parentID, accountID, parentIDs[0]); err != nil {
			t.Fatal(err)
		}
		if _, err = adversarial.Exec(ctx, `INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority,parameters,retry_of_job_id)
			VALUES($1,$2,$3,'manual_ingest_source',80,$4,$5)`, fixture.childID, fixture.parentID, accountID, parameters, childIDs[0]); err != nil {
			t.Fatal(err)
		}
		if _, err = adversarial.Exec(ctx, `INSERT INTO app.job_source_contexts(job_id,account_id,source_id,source_generation,display_name,source_type)
			SELECT $1,$2,id,generation,display_name,type FROM app.sources WHERE id=$3`, fixture.childID, accountID, fixture.sourceID); err != nil {
			t.Fatal(err)
		}
		if _, err = adversarial.Exec(ctx, `INSERT INTO app.job_config_snapshots(job_id,account_id,source_id,source_generation,config_envelope)
			SELECT $1,$2,id,generation,$4 FROM app.sources WHERE id=$3`, fixture.childID, accountID, fixture.sourceID, []byte("lineage-fixture")); err != nil {
			t.Fatal(err)
		}
	}
	if err = adversarial.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	good := routeJobRequest(handler, http.MethodGet, "/api/jobs/"+compactUUID(goodChild), "", bearer, "", false)
	detail = generated.JobDetail{}
	if good.recorder.Code != http.StatusOK || json.Unmarshal(good.recorder.Body.Bytes(), &detail) != nil ||
		detail.RetryRootJobId == nil || *detail.RetryRootJobId != compactUUID(childIDs[0]) ||
		detail.RetryOrdinal == nil || *detail.RetryOrdinal != 1 {
		t.Fatalf("same-source child lineage status=%d body=%s", good.recorder.Code, good.recorder.Body.String())
	}
	bad := routeJobRequest(handler, http.MethodGet, "/api/jobs/"+compactUUID(badChild), "", bearer, "", false)
	if bad.recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("cross-source child lineage status=%d body=%s", bad.recorder.Code, bad.recorder.Body.String())
	}
	validateRecordedResponse(t, http.MethodGet, "/api/jobs/"+compactUUID(badChild), bad.recorder)
	badRetry := routeJobRequest(handler, http.MethodPost, "/api/jobs/"+compactUUID(badChild)+"/retry", `{}`, cookie, "retry-limit-csrf", true)
	if badRetry.recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("cross-source child retry status=%d body=%s", badRetry.recorder.Code, badRetry.recorder.Body.String())
	}
	validateRecordedResponse(t, http.MethodPost, "/api/jobs/"+compactUUID(badChild)+"/retry", badRetry.recorder)
}

func TestJobRetryCycleFailsSafelyIntegration(t *testing.T) {
	databaseURL, migrationURL := os.Getenv("API_DATABASE_URL"), os.Getenv("MIGRATION_DATABASE_URL")
	if databaseURL == "" || migrationURL == "" {
		t.Skip("API_DATABASE_URL and MIGRATION_DATABASE_URL are required")
	}
	ctx := context.Background()
	apiDB, err := pgxpool.New(ctx, databaseURL)
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
	principalID, accountID := insertSourceTestUser(t, adminDB)
	bearer := insertTestSession(t, apiDB, principalID, "bearer", "")
	cookie := insertTestSession(t, apiDB, principalID, "cookie", "retry-cycle-csrf")
	firstID, secondID := uuid.Must(uuid.NewV7()), uuid.Must(uuid.NewV7())

	tx, err := adminDB.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, accountID.String()); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE app.jobs DISABLE TRIGGER jobs_retry_lineage_before_insert`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO app.jobs(id,account_id,kind,priority,retry_of_job_id) VALUES
		($1,$3,'manual_ingest',80,$2),($2,$3,'manual_ingest',80,$1)`, firstID, secondID, accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `ALTER TABLE app.jobs ENABLE TRIGGER jobs_retry_lineage_before_insert`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	response := routeJobRequest(handler, http.MethodGet, "/api/jobs/"+compactUUID(firstID), "", bearer, "", false)
	if response.recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("cyclic retry detail status=%d body=%s", response.recorder.Code, response.recorder.Body.String())
	}
	validateRecordedResponse(t, http.MethodGet, "/api/jobs/"+compactUUID(firstID), response.recorder)
	retry := routeJobRequest(handler, http.MethodPost, "/api/jobs/"+compactUUID(firstID)+"/retry", `{}`, cookie, "retry-cycle-csrf", true)
	if retry.recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("cyclic retry status=%d body=%s", retry.recorder.Code, retry.recorder.Body.String())
	}
	validateRecordedResponse(t, http.MethodPost, "/api/jobs/"+compactUUID(firstID)+"/retry", retry.recorder)
}

func routeJobRequest(handler http.Handler, method, path, body, credential, csrf string, cookie bool) endpointCall {
	call := callEndpoint(method, path, body, "", csrf)
	if cookie {
		call.request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: credential})
	} else if credential != "" {
		call.request.Header.Set("Authorization", "Bearer "+credential)
	}
	handler.ServeHTTP(call.recorder, call.request)
	return call
}
