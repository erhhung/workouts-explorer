package api

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIngestReprocessingIntegration(t *testing.T) {
	apiURL, migrationURL := os.Getenv("API_DATABASE_URL"), os.Getenv("MIGRATION_DATABASE_URL")
	if apiURL == "" || migrationURL == "" {
		t.Skip("API_DATABASE_URL and MIGRATION_DATABASE_URL are required; bounded persistence requires schema 31")
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
	principal, account := insertSourceTestUser(t, adminDB)
	bearer := insertTestSession(t, apiDB, principal, "bearer", "")
	sourceIDs := make([]uuid.UUID, 0, 2)
	for _, name := range []string{"a", "b"} {
		source := createIntegrationSource(t, server, bearer, "Reprocessing "+name, "/data/workouts/reprocessing-"+name)
		id, _ := parseCompactUUID(source.Id)
		setIntegrationSourceStatus(t, adminDB, account, id, "connected")
		sourceIDs = append(sourceIDs, id)
	}
	sourceIDs = normalizedUUIDs(sourceIDs)
	const dates = `,"startDate":"2026-01-01","endDate":"2026-01-02"`
	base := `{"sourceIds":["` + compactUUID(sourceIDs[0]) + `","` + compactUUID(sourceIDs[1]) + `"]`
	accept := func(call endpointCall) generated.IngestAccepted {
		t.Helper()
		var result generated.IngestAccepted
		if call.recorder.Code != http.StatusAccepted || json.Unmarshal(call.recorder.Body.Bytes(), &result) != nil {
			t.Fatalf("enqueue status=%d body=%s", call.recorder.Code, call.recorder.Body.String())
		}
		return result
	}
	ingest := func(body string) generated.IngestAccepted {
		t.Helper()
		call := routeIngest(handler, body, bearer, "", false)
		validateRecordedResponse(t, http.MethodPost, "/api/ingest", call.recorder)
		return accept(call)
	}
	retry := func(id string) generated.IngestAccepted {
		t.Helper()
		call := routeJobRequest(handler, http.MethodPost, "/api/jobs/"+id+"/retry", `{}`, bearer, "", false)
		validateRecordedResponse(t, http.MethodPost, "/api/jobs/"+id+"/retry", call.recorder)
		return accept(call)
	}
	detail := func(id string) generated.JobDetail {
		t.Helper()
		path := "/api/jobs/" + id
		call := routeJobRequest(handler, http.MethodGet, path, "", bearer, "", false)
		var result generated.JobDetail
		if call.recorder.Code != http.StatusOK || json.Unmarshal(call.recorder.Body.Bytes(), &result) != nil {
			t.Fatalf("detail status=%d body=%s", call.recorder.Code, call.recorder.Body.String())
		}
		validateRecordedResponse(t, http.MethodGet, path, call.recorder)
		for _, secret := range []string{"parameters", "reprocessAllFiles", "config_envelope", "/data/workouts/"} {
			if strings.Contains(call.recorder.Body.String(), secret) {
				t.Fatalf("job detail leaked %s", secret)
			}
		}
		return result
	}
	cancelJob := func(id string) {
		t.Helper()
		call := routeJobRequest(handler, http.MethodPost, "/api/jobs/"+id+"/cancellation", `{}`, bearer, "", false)
		if call.recorder.Code != http.StatusOK {
			t.Fatalf("cancel status=%d body=%s", call.recorder.Code, call.recorder.Body.String())
		}
	}
	assertParameters := func(id string, force bool, children int) {
		t.Helper()
		jobID, _ := parseCompactUUID(id)
		tx := integrationAccountTransaction(t, apiDB, account)
		defer tx.Rollback(ctx)
		rows, err := tx.Query(ctx, `SELECT parent_job_id IS NULL,parameters,COALESCE(coalescing_version,0)
			FROM app.jobs WHERE id=$1 OR parent_job_id=$1 ORDER BY parent_job_id NULLS FIRST,id`, jobID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		count := 0
		for rows.Next() {
			var parent bool
			var parameters map[string]any
			var version int
			if err := rows.Scan(&parent, &parameters, &version); err != nil {
				t.Fatal(err)
			}
			wantFields, wantVersion := 6, 0
			if parent {
				wantFields, wantVersion = 5, 2
			}
			if len(parameters) != wantFields || parameters["reprocessAllFiles"] != force || version != wantVersion {
				t.Fatalf("persisted parent=%t parameters=%+v version=%d", parent, parameters, version)
			}
			count++
		}
		if rows.Err() != nil || count != children+1 {
			t.Fatalf("artifacts=%d want=%d err=%v", count, children+1, rows.Err())
		}
	}
	assertLineage := func(id, parent string, priorChildren []generated.JobDetail) generated.JobDetail {
		t.Helper()
		result := detail(id)
		if result.RetryOfJobId == nil || *result.RetryOfJobId != parent || len(result.Children) != len(priorChildren) {
			t.Fatalf("retry parent/children mismatch: %+v", result)
		}
		for index, child := range result.Children {
			prior := priorChildren[index]
			if child.RetryOfJobId == nil || *child.RetryOfJobId != prior.Id || child.Source.SourceId != prior.Source.SourceId ||
				child.RetryRootJobId == nil || child.RetryOrdinal == nil || *child.RetryOrdinal != 2 {
				t.Fatalf("retry lost child ancestry: %+v", child)
			}
		}
		return result
	}

	for _, fields := range []string{
		`,"reprocessAllFiles":false`, `,"reprocessAllFiles":true`,
		`,"startDate":"2026-01-01","reprocessAllFiles":false`,
		`,"endDate":"2026-01-02","reprocessAllFiles":true`,
	} {
		call := routeIngest(handler, base+fields+`}`, bearer, "", false)
		var problem generated.Problem
		if call.recorder.Code != http.StatusBadRequest || json.Unmarshal(call.recorder.Body.Bytes(), &problem) != nil ||
			problem.Errors == nil || len(*problem.Errors) != 1 || (*problem.Errors)[0].Field != "reprocessAllFiles" {
			t.Fatalf("policy misuse status=%d body=%s", call.recorder.Code, call.recorder.Body.String())
		}
		validateRecordedResponse(t, http.MethodPost, "/api/ingest", call.recorder)
	}
	for _, invalid := range []string{`null`, `"false"`, `0`, `{}`, `[]`} {
		call := routeIngest(handler, base+dates+`,"reprocessAllFiles":`+invalid+`}`, bearer, "", false)
		if call.recorder.Code != http.StatusBadRequest {
			t.Fatalf("policy %s status=%d body=%s", invalid, call.recorder.Code, call.recorder.Body.String())
		}
	}

	root := ingest(base + dates + `}`)
	explicitFalse := ingest(base + dates + `,"reprocessAllFiles":false}`)
	forced := ingest(base + dates + `,"reprocessAllFiles":true}`)
	if root.Reused || !explicitFalse.Reused || explicitFalse.JobId != root.JobId || forced.Reused || forced.JobId == root.JobId {
		t.Fatalf("policy coalescing root=%+v false=%+v true=%+v", root, explicitFalse, forced)
	}
	assertParameters(root.JobId, false, 2)
	assertParameters(forced.JobId, true, 2)
	rootDetail := detail(root.JobId)
	cancelJob(root.JobId)
	otherRoot := ingest(base + dates + `}`)
	if otherRoot.Reused || otherRoot.JobId == root.JobId {
		t.Fatal("terminal root was reused")
	}

	// Overlapping source locks serialize these identical retry commands without
	// a separate advisory lock, while the active root must remain independent.
	concurrent := make([]endpointCall, 2)
	start := make(chan struct{})
	var wait sync.WaitGroup
	for index := range concurrent {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			concurrent[index] = routeJobRequest(handler, http.MethodPost, "/api/jobs/"+root.JobId+"/retry", `{}`, bearer, "", false)
		}(index)
	}
	close(start)
	wait.Wait()
	first, second := accept(concurrent[0]), accept(concurrent[1])
	if first.JobId != second.JobId || first.Reused == second.Reused || first.JobId == otherRoot.JobId || first.JobId == forced.JobId {
		t.Fatalf("concurrent retry results=%+v/%+v", first, second)
	}
	assertParameters(first.JobId, false, 2)
	assertLineage(first.JobId, root.JobId, rootDetail.Children)
	for _, child := range rootDetail.Children {
		branch := retry(child.Id)
		if branch.Reused || branch.JobId == first.JobId || branch.JobId == otherRoot.JobId {
			t.Fatalf("direct-child branch coalesced with unrelated command: %+v", branch)
		}
		assertParameters(branch.JobId, false, 1)
		assertLineage(branch.JobId, root.JobId, []generated.JobDetail{child})
		again := retry(child.Id)
		if !again.Reused || again.JobId != branch.JobId {
			t.Fatal("identical active direct-child retry was not reused")
		}
	}
	cancelJob(otherRoot.JobId)
	otherDetail := detail(otherRoot.JobId)
	otherRetry := retry(otherRoot.JobId)
	if otherRetry.Reused || otherRetry.JobId == first.JobId {
		t.Fatal("retries of unrelated roots coalesced")
	}
	assertLineage(otherRetry.JobId, otherRoot.JobId, otherDetail.Children)
	cancelJob(forced.JobId)
	forcedDetail := detail(forced.JobId)
	forcedRetry := retry(forced.JobId)
	assertParameters(forcedRetry.JobId, true, 2)
	assertLineage(forcedRetry.JobId, forced.JobId, forcedDetail.Children)

	oldChild := rootDetail.Children[0]
	update := callEndpoint(http.MethodPatch, "/api/sources/"+oldChild.Source.SourceId,
		`{"config":{"version":1,"path":"/data/workouts/reprocessing-current"}}`, bearer, "")
	server.UpdateSource(update.recorder, update.request, oldChild.Source.SourceId, generated.UpdateSourceParams{})
	if update.recorder.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", update.recorder.Code, update.recorder.Body.String())
	}
	setIntegrationSourceStatus(t, adminDB, account, sourceIDs[0], "connected")
	current := retry(oldChild.Id)
	currentDetail := assertLineage(current.JobId, root.JobId, []generated.JobDetail{oldChild})
	if current.Reused || currentDetail.Children[0].Source.Generation != oldChild.Source.Generation+1 {
		t.Fatal("changed source generation reused an old retry")
	}
	assertParameters(current.JobId, false, 1)

	foreignPrincipal, foreignAccount := insertSourceTestUser(t, adminDB)
	foreignBearer := insertTestSession(t, apiDB, foreignPrincipal, "bearer", "")
	for _, id := range []string{root.JobId, oldChild.Id, first.JobId, current.JobId} {
		for _, method := range []string{http.MethodGet, http.MethodPost} {
			path, body := "/api/jobs/"+id, ""
			if method == http.MethodPost {
				path, body = path+"/retry", `{}`
			}
			call := routeJobRequest(handler, method, path, body, foreignBearer, "", false)
			if call.recorder.Code != http.StatusNotFound {
				t.Fatalf("foreign %s %s status=%d body=%s", method, path, call.recorder.Code, call.recorder.Body.String())
			}
		}
	}
	tx := integrationAccountTransaction(t, apiDB, foreignAccount)
	var visible int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.jobs WHERE account_id=$1`, account).Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("foreign jobs visible=%d err=%v", visible, err)
	}
	_ = tx.Rollback(ctx)

	// A real v1 digest and the old five-field child shape remain unchanged.
	// Neither new intent can reuse it, but retry must preserve its forced policy.
	legacyID := insertReprocessingV1Ingest(t, adminDB, account, sourceIDs[1])
	legacyBase := `{"sourceIds":["` + compactUUID(sourceIDs[1]) + `"]` + dates
	legacyNonforce := ingest(legacyBase + `,"reprocessAllFiles":false}`)
	legacyForce := ingest(legacyBase + `,"reprocessAllFiles":true}`)
	if legacyNonforce.Reused || legacyForce.Reused || legacyNonforce.JobId == compactUUID(legacyID) || legacyForce.JobId == compactUUID(legacyID) {
		t.Fatal("new policy reused v1 active work")
	}
	legacyDetail := detail(compactUUID(legacyID))
	cancelJob(compactUUID(legacyID))
	legacyRetry := retry(compactUUID(legacyID))
	assertParameters(legacyRetry.JobId, true, 1)
	assertLineage(legacyRetry.JobId, compactUUID(legacyID), legacyDetail.Children)
	tx = integrationAccountTransaction(t, apiDB, account)
	defer tx.Rollback(ctx)
	var preserved bool
	if err := tx.QueryRow(ctx, `SELECT parent.coalescing_version=1 AND NOT parent.parameters ? 'reprocessAllFiles'
		AND NOT child.parameters ? 'reprocessAllFiles'
		FROM app.jobs parent JOIN app.jobs child ON child.parent_job_id=parent.id WHERE parent.id=$1`, legacyID).Scan(&preserved); err != nil || !preserved {
		t.Fatalf("legacy persisted parameters rewritten=%t err=%v", !preserved, err)
	}
}

func insertReprocessingV1Ingest(t *testing.T, db *pgxpool.Pool, account, source uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	tx := integrationAccountTransaction(t, db, account)
	defer tx.Rollback(ctx)
	var generation int64
	if err := tx.QueryRow(ctx, `SELECT generation FROM app.sources WHERE id=$1`, source).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	hash := sha256.New()
	hash.Write([]byte("workouts-explorer/manual-ingest/v1\x00"))
	hash.Write(source[:])
	var encodedGeneration [8]byte
	binary.BigEndian.PutUint64(encodedGeneration[:], uint64(generation))
	hash.Write(encodedGeneration[:])
	hash.Write([]byte("\x00bounded\x002026-01-01\x002026-01-02"))
	parent, child := uuid.New(), uuid.New()
	parentParameters := `{"sourceIds":["` + compactUUID(source) + `"],"mode":"bounded","startDate":"2026-01-01","endDate":"2026-01-02"}`
	childParameters, err := json.Marshal(map[string]any{"sourceId": compactUUID(source), "generation": generation,
		"mode": "bounded", "startDate": "2026-01-01", "endDate": "2026-01-02"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO app.jobs(id,account_id,kind,priority,parameters,coalescing_version,coalescing_scope,coalescing_key)
		VALUES($1,$2,'manual_ingest',80,$3,1,'manual-ingest',$4)`, parent, account, parentParameters, hash.Sum(nil)); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority,parameters)
		VALUES($1,$2,$3,'manual_ingest_source',80,$4)`, child, parent, account, childParameters); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO app.job_config_snapshots(job_id,account_id,source_id,source_generation,config_envelope)
		VALUES($1,$2,$3,$4,$5)`, child, account, source, generation, []byte("unprocessed-legacy-fixture")); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO app.job_source_contexts(job_id,account_id,source_id,source_generation,display_name,source_type)
		SELECT $1,account_id,id,generation,display_name,type FROM app.sources WHERE id=$2 ON CONFLICT DO NOTHING`, child, source); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO app.job_progress(job_id,account_id) VALUES($1,$3),($2,$3) ON CONFLICT DO NOTHING`, parent, child, account); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return parent
}
