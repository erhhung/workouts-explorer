package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/coverage/routepipeline"
	"github.com/erhhung/workouts-explorer/internal/healthautoexport"
	"github.com/erhhung/workouts-explorer/internal/sourcecrypto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This is an opt-in fixture driver, not a normal unit test or a production import.
// It reuses the runtime suite's same-package fence, queue, and persistence helpers.
func acceptanceFixture(t testing.TB) *coverageIntegrationFixture {
	t.Helper()
	if os.Getenv("COVERAGE_ACCEPTANCE_DISPOSABLE") != "true" || os.Getenv("COVERAGE_ACCEPTANCE_ACTION") == "" {
		t.Skip("explicit disposable switch and COVERAGE_ACCEPTANCE_ACTION required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	t.Cleanup(cancel)
	f := &coverageIntegrationFixture{t: t, ctx: ctx, regionID: "fixture:dense", generation: 1}
	specs := []struct {
		name, database string
		pool           **pgxpool.Pool
	}{
		{"COVERAGE_ACCEPTANCE_DATABASE_URL", "acceptance_benchmark", &f.adminDB},
		{"COVERAGE_ACCEPTANCE_OSM_DATABASE_URL", "osm", &f.osmAdminDB},
	}
	configs := make([]*pgxpool.Config, len(specs))
	for i, spec := range specs {
		config, err := parseCoverageAcceptanceConfig(os.Getenv(spec.name), spec.database)
		if err != nil {
			t.Fatalf("refusing non-disposable %s: %v (URL omitted)", spec.name, err)
		}
		c := config.ConnConfig
		if c.User != "workouts_migration" || c.Password != "acceptance-only" {
			t.Fatalf("refusing non-disposable %s", spec.name)
		}
		config.MaxConns = 4
		config.ConnConfig.RuntimeParams["application_name"] = "coverage-acceptance"
		configs[i] = config
	}
	if err := validateCoverageAcceptancePair(configs[0], configs[1]); err != nil {
		t.Fatal(err)
	}
	for i, spec := range specs {
		config := configs[i]
		pool, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		*spec.pool = pool
		t.Cleanup(pool.Close)
	}
	rolePool := func(role string) *pgxpool.Pool {
		config := f.adminDB.Config()
		config.ConnConfig.User = role
		pool, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		return pool
	}
	f.workerDB, f.apiDB, f.coverageDB = rolePool("workouts_worker"), rolePool("workouts_api"), rolePool("workouts_coverage_worker")
	f.osmDB = f.osmAdminDB
	return f
}

func acceptanceID(kind string, n int) uuid.UUID {
	return uuid.NewMD5(uuid.Nil, []byte(fmt.Sprintf("acceptance-%s-%d", kind, n)))
}

func acceptanceConfigEnvelope(t testing.TB, account, record uuid.UUID, purpose sourcecrypto.Purpose) []byte {
	t.Helper()
	file := t.TempDir() + "/fake-keyring.json"
	if err := os.WriteFile(file, []byte(`{"activeKeyId":"acceptance","keys":{"acceptance":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}}`), 0600); err != nil {
		t.Fatal(err)
	}
	keys, err := sourcecrypto.LoadKeyring(file)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := keys.Encrypt(sourcecrypto.Context{Purpose: purpose, AccountID: account, RecordID: record}, []byte(`{"version":1,"path":"/tmp/acceptance-source"}`))
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

func TestCoverageAcceptanceSourceConfig(t *testing.T) {
	f := acceptanceFixture(t)
	if os.Getenv("COVERAGE_ACCEPTANCE_ACTION") != "source-config" {
		t.Skip("fixture source configuration only")
	}
	f.accountID, f.sourceID = acceptanceID("account", 0), acceptanceID("source", 0)
	tx := f.accountTx(f.ctx, f.adminDB)
	f.exec(tx, `UPDATE app.sources SET config_envelope=$2 WHERE id=$1`, f.sourceID, acceptanceConfigEnvelope(t, f.accountID, f.sourceID, sourcecrypto.SourceConfig))
	f.commit(tx)
}

func TestCoverageAcceptanceSeed(t *testing.T) {
	f := acceptanceFixture(t)
	if os.Getenv("COVERAGE_ACCEPTANCE_ACTION") != "seed" {
		t.Skip("seed only")
	}
	profile := acceptanceLoadProfile(t)
	schedule, err := acceptanceSchedule(profile.Manifest, profile.Name)
	if err != nil {
		t.Fatal(err)
	}
	var accounts int
	if err := f.adminDB.QueryRow(f.ctx, `SELECT count(*) FROM app.accounts`).Scan(&accounts); err != nil {
		t.Fatal(err)
	}
	if accounts != 0 {
		var workouts int
		if err := f.adminDB.QueryRow(f.ctx, `SELECT count(*) FROM app.workouts`).Scan(&workouts); err != nil {
			t.Fatal(err)
		}
		if accounts != 1 || workouts != 0 {
			t.Fatal("benchmark DB already seeded; refusing duplicate fixture writes")
		}
	}
	f.accountID, f.sourceID, f.fileID, f.typeID, f.ingestID, f.ingestLease = acceptanceID("account", 0), acceptanceID("source", 0), acceptanceID("file", 0), acceptanceID("type", 0), acceptanceID("ingest", 0), uuid.New()
	var tx pgx.Tx
	var claimed bool
	if accounts == 0 {
		tx = f.accountTx(f.ctx, f.adminDB)
		f.exec(tx, `INSERT INTO app.accounts(id) VALUES($1)`, f.accountID)
		monthStart, monthEnd := profile.dateRange(time.September)
		f.exec(tx, `INSERT INTO app.preferences(account_id,theme,units,timezone,date_range,coverage_diagnostics_enabled,initialized_at)
			VALUES($1,'light','metric','UTC',$2,false,clock_timestamp())`, f.accountID, monthStart+"/"+monthEnd)
		f.exec(tx, `INSERT INTO app.authentication_principals(id,role,username,canonical_username,email,canonical_email,canonicalization_version,password_hash,full_name)
		VALUES($1,'user','acceptance-fixture','acceptance-fixture','acceptance@example.test','acceptance@example.test',1,'not-a-login-hash','Synthetic Acceptance Owner')`, acceptanceID("principal", 0))
		f.exec(tx, `INSERT INTO app.users(principal_id,account_id) VALUES($1,$2)`, acceptanceID("principal", 0), f.accountID)
		raw := sha256.Sum256([]byte("FAKE disposable synthetic coverage acceptance bearer; never a live credential"))
		verifier := sha256.Sum256(raw[:])
		f.exec(tx, `INSERT INTO app.sessions(id,principal_id,credential_kind,credential_verifier,expires_at) VALUES($1,$2,'bearer',$3,clock_timestamp()+interval '7 days')`, acceptanceID("session", 0), acceptanceID("principal", 0), verifier[:])
		f.exec(tx, `INSERT INTO app.sources(id,account_id,display_name,canonical_display_name,type,config_envelope,status)
		VALUES($1,$2,'Synthetic Grid Fixture','synthetic grid fixture','health-auto-export-local',$3,'connected')`, f.sourceID, f.accountID, acceptanceConfigEnvelope(t, f.accountID, f.sourceID, sourcecrypto.SourceConfig))
		f.exec(tx, `INSERT INTO app.jobs(id,account_id,kind,priority) VALUES($1,$2,'manual_ingest',80)`, acceptanceID("ingest-parent", 0), f.accountID)
		parameters := fmt.Sprintf(`{"sourceId":"%s","generation":1,"mode":"incremental"}`, strings.ToUpper(strings.ReplaceAll(f.sourceID.String(), "-", "")))
		f.exec(tx, `INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority,parameters) VALUES($1,$2,$3,'manual_ingest_source',80,$4)`, f.ingestID, acceptanceID("ingest-parent", 0), f.accountID, parameters)
		f.exec(tx, `INSERT INTO app.job_config_snapshots(job_id,account_id,source_id,source_generation,config_envelope) VALUES($1,$2,$3,1,$4)`, f.ingestID, f.accountID, f.sourceID, acceptanceConfigEnvelope(t, f.accountID, f.ingestID, sourcecrypto.JobConfigSnapshot))
		f.commit(tx)
	} else {
		tx = f.accountTx(f.ctx, f.adminDB)
		f.scan(tx, `SELECT lease_token FROM app.jobs WHERE id=$1 AND status='running' AND worker_id='coverage-fixture-ingest' AND lease_expires_at>clock_timestamp()`, []any{f.ingestID}, &f.ingestLease)
		f.commit(tx)
	}
	if accounts == 0 {
		tx = f.accountTx(f.ctx, f.workerDB)
		f.scan(tx, `SELECT app.claim_job($1,'coverage-fixture-ingest',$2,interval '60 minutes')`, []any{f.ingestID, f.ingestLease}, &claimed)
		if !claimed {
			t.Fatal("seed ingest lease rejected")
		}
		f.fence(tx)
		hash := sha256.Sum256([]byte("public synthetic fixture"))
		f.exec(tx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes,checksum_sha256,state,processing_started_at,processed_at)
		VALUES($1,$2,$3,$4,'synthetic-grid.json',1,$5,'succeeded',clock_timestamp(),clock_timestamp())`, f.fileID, f.accountID, f.sourceID, f.ingestID, hash[:])
		f.commit(tx)
	}
	typeIDs := map[string]uuid.UUID{}
	tx = f.accountTx(f.ctx, f.workerDB)
	f.fence(tx)
	for _, a := range profile.typeCatalog(false) {
		typeIDs[a.Key] = acceptanceID("type-"+a.Key, 0)
		f.exec(tx, `INSERT INTO app.workout_types(id,account_id,type_key,provider_label) VALUES($1,$2,$3,$4)`, typeIDs[a.Key], f.accountID, healthautoexport.NormalizedTypeKey(a.Label), a.Label)
	}
	f.commit(tx)
	grid := acceptanceLoadGrid(f)
	seedStarted := time.Now()
	runner := f.runner()
	// Precomputed persistence is not RunOnce and has no heartbeat goroutine.
	// Use the production lease length for these larger, fully fenced payloads.
	runner.leaseDuration = 4 * time.Minute
	pointCount, matchCount := 0, 0
	for _, w := range schedule {
		var route acceptanceGeneratedRoute
		if w.Activity.Routed {
			route, err = acceptanceGenerateRoute(grid, profile.Manifest, w, 0)
			if err != nil {
				t.Fatal(err)
			}
		}
		id := acceptanceID("workout", w.Index)
		digest := canonicalRouteInputSHA256(route.Points)
		tx = f.accountTx(f.ctx, f.workerDB)
		f.fence(tx)
		var indoor *bool
		if w.Activity.Routed {
			outdoor := false
			indoor = &outdoor
		}
		f.exec(tx, `INSERT INTO app.workouts(id,account_id,source_id,source_file_id,workout_type_id,provider_id,content_sha256,provider_label,started_at,ended_at,local_start_date,provider_duration,is_indoor,start_offset_minutes,end_offset_minutes)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,0,0)`, id, f.accountID, f.sourceID, f.fileID, typeIDs[w.Activity.Key], id.String(), digest[:], w.Activity.Label,
			w.Start, w.Start.Add(time.Duration(w.Activity.DurationSeconds)*time.Second), w.Start.Format("2006-01-02"), w.Activity.DurationSeconds, indoor)
		if w.Activity.Routed {
			acceptanceWriteRoute(f, tx, id, route.Points)
			f.exec(tx, `INSERT INTO app.workout_aggregates(account_id,workout_id,metric,value,unit,origin) VALUES($1,$2,'distance',$3,'km','provider_direct')`, f.accountID, id, route.Meters/1000)
		}
		// Readiness has no no_route enum in this schema. An unrouted workout has
		// no route summary/state, so detail derives legitimate no-route SourceFacts.
		f.commit(tx)
		if w.Activity.Routed {
			data := acceptanceMatches(f, grid, route)
			f.queue(id)
			claim, found, err := runner.claim(f.ctx)
			if err != nil || !found || claim.workoutID != id {
				t.Fatalf("precompute claim found=%t err=%v", found, err)
			}
			outcome, err := runner.persist(f.ctx, claim, 0, []byte(`[{"regionId":"fixture:dense","generation":1}]`), data)
			if err != nil || outcome != "applied" {
				t.Fatalf("precompute outcome=%s err=%v", outcome, err)
			}
			pointCount += len(route.Points)
			matchCount += len(route.Evidence)
		}
		if w.Index%30 == 29 {
			t.Logf("seeded %d workouts in %s", w.Index+1, time.Since(seedStarted).Round(time.Second))
		}
	}
	tx = f.accountTx(f.ctx, f.workerDB)
	f.scan(tx, `SELECT app.finish_job($1,'coverage-fixture-ingest',$2,'succeeded')`, []any{f.ingestID, f.ingestLease}, &claimed)
	if !claimed {
		t.Fatal("seed ingest completion rejected")
	}
	f.commit(tx)
	if _, err := f.adminDB.Exec(f.ctx, `ANALYZE`); err != nil {
		t.Fatal(err)
	}
	acceptanceDeferReconciliation(f)
	artifact := profile.metadata()
	artifact["total_workouts"], artifact["routed_workouts"], artifact["route_points"], artifact["segment_matches"] = len(schedule), profile.routedCount(), pointCount, matchCount
	artifact["duration_seconds"] = time.Since(seedStarted).Seconds()
	data, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profile.artifact("profile"), append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("seed complete: profile=%s workouts=%d applied=%d points=%d unique segment matches=%d manifest_sha256=%s duration=%s", profile.Name, len(schedule), profile.routedCount(), pointCount, matchCount, profile.SHA256, time.Since(seedStarted))
}

func acceptanceDeferReconciliation(f *coverageIntegrationFixture) {
	var head int64
	if err := f.osmAdminDB.QueryRow(f.ctx, `SELECT osm_catalog.promotion_event_head()`).Scan(&head); err != nil {
		f.t.Fatal(err)
	}
	tx := f.accountTx(f.ctx, f.adminDB)
	f.exec(tx, `SELECT app.observe_coverage_reconciliation($1,'coverage-production-v1')`, head)
	f.exec(tx, `UPDATE app.coverage_reconciliation_accounts SET completed_revision=desired_revision,next_scan_at=clock_timestamp()+interval '30 days',
		worker_id=NULL,lease_token=NULL,heartbeat_at=NULL,lease_expires_at=NULL WHERE account_id=$1`, f.accountID)
	f.commit(tx)
}

func TestCoverageAcceptanceDeferReconciliation(t *testing.T) {
	f := acceptanceFixture(t)
	if os.Getenv("COVERAGE_ACCEPTANCE_ACTION") != "defer-reconciliation" {
		t.Skip("fixture scheduling only")
	}
	f.accountID = acceptanceID("account", 0)
	acceptanceDeferReconciliation(f)
	// Cancel interrupted exploratory work only through the real cancellation and
	// live-lease finish contracts, never by editing job status or lease timestamps.
	tx := f.accountTx(f.ctx, f.adminDB)
	rows, err := tx.Query(f.ctx, `SELECT id FROM app.jobs WHERE kind='coverage_update' AND status IN ('queued','running')`)
	if err != nil {
		t.Fatal(err)
	}
	var parents []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		parents = append(parents, id)
	}
	rows.Close()
	for _, id := range parents {
		f.exec(tx, `SELECT app.request_coverage_job_cancellation($1,$2)`, id, uuid.New())
	}
	f.commit(tx)
	tx = f.accountTx(f.ctx, f.coverageDB)
	rows, err = tx.Query(f.ctx, `SELECT id,worker_id,lease_token FROM app.jobs WHERE kind='coverage_update_route' AND status='running' AND cancel_requested_at IS NOT NULL`)
	if err != nil {
		t.Fatal(err)
	}
	type lease struct {
		id     uuid.UUID
		worker string
		token  uuid.UUID
	}
	var leases []lease
	for rows.Next() {
		var l lease
		if err := rows.Scan(&l.id, &l.worker, &l.token); err != nil {
			t.Fatal(err)
		}
		leases = append(leases, l)
	}
	rows.Close()
	for _, l := range leases {
		f.exec(tx, `SELECT app.finish_job($1,$2,$3,'cancelled')`, l.id, l.worker, l.token)
	}
	f.commit(tx)
	tx = f.accountTx(f.ctx, f.adminDB)
	rows, err = tx.Query(f.ctx, `SELECT id,worker_id,lease_token FROM app.jobs WHERE kind='manual_ingest_source' AND status='running' AND worker_id='coverage-fixture-ingest'`)
	if err != nil {
		t.Fatal(err)
	}
	leases = nil
	for rows.Next() {
		var l lease
		if err := rows.Scan(&l.id, &l.worker, &l.token); err != nil {
			t.Fatal(err)
		}
		leases = append(leases, l)
	}
	rows.Close()
	for _, l := range leases {
		f.exec(tx, `SELECT app.request_job_cancellation($1,$2)`, l.id, uuid.New())
		f.exec(tx, `SELECT app.finish_job($1,$2,$3,'cancelled')`, l.id, l.worker, l.token)
	}
	f.commit(tx)
}

type acceptanceTiming struct {
	Samples     int         `json:"samples"`
	FirstMS     float64     `json:"first_ms"`
	P50MS       float64     `json:"p50_ms"`
	P95MS       float64     `json:"p95_ms"`
	MaxMS       float64     `json:"max_ms"`
	Statuses    map[int]int `json:"statuses"`
	StartedAt   time.Time   `json:"started_at"`
	FinishedAt  time.Time   `json:"finished_at"`
	ActiveStart int         `json:"active_children_start"`
	ActiveEnd   int         `json:"active_children_end"`
}

func TestCoverageAcceptanceBacklog(t *testing.T) {
	f := acceptanceFixture(t)
	if os.Getenv("COVERAGE_ACCEPTANCE_ACTION") != "queue" {
		t.Skip("queue only")
	}
	profile := acceptanceLoadProfile(t)
	schedule, err := acceptanceSchedule(profile.Manifest, profile.Name)
	if err != nil {
		t.Fatal(err)
	}
	grid := acceptanceLoadGrid(f)
	count := 48
	switch os.Getenv("COVERAGE_ACCEPTANCE_BACKLOG") {
	case "", "48":
	case "96":
		count = 96
	default:
		t.Fatal("COVERAGE_ACCEPTANCE_BACKLOG must be 48 or 96")
	}
	queueStarted := time.Now()
	var children []uuid.UUID
	f.accountID, f.sourceID, f.fileID, f.typeID = acceptanceID("account", 0), acceptanceID("source", 0), acceptanceID("file", 0), acceptanceID("type", 0)
	var active int
	tx := f.accountTx(f.ctx, f.adminDB)
	f.scan(tx, `SELECT count(*) FROM app.jobs WHERE kind='coverage_update_route' AND status IN ('queued','running')`, nil, &active)
	if active != 0 {
		t.Fatal("existing backlog must drain before a new wave")
	}
	rows, err := tx.Query(f.ctx, `SELECT w.id FROM app.workouts w JOIN app.workout_routes r ON r.account_id=w.account_id AND r.workout_id=w.id
		JOIN app.workout_types t ON t.account_id=w.account_id AND t.id=w.workout_type_id
		JOIN app.workout_coverage_states s ON s.account_id=w.account_id AND s.workout_id=w.id
		WHERE w.account_id=$1 AND w.deletion_requested_at IS NULL AND r.route IS NOT NULL AND s.readiness_state='map_data_ready'
		AND t.type_key=ANY($2::text[]) AND s.applied_route_input_revision IS NOT NULL ORDER BY w.started_at,w.id`, f.accountID, profile.typeKeys(true))
	if err != nil {
		t.Fatal(err)
	}
	var eligible []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		eligible = append(eligible, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(eligible) != profile.routedCount() || len(eligible) < count {
		t.Fatalf("eligible applied routes=%d, expected %d", len(eligible), profile.routedCount())
	}
	var revision int64
	f.scan(tx, `SELECT max(route_input_revision) FROM app.workout_coverage_states WHERE account_id=$1`, []any{f.accountID}, &revision)
	f.ingestID, f.ingestLease = uuid.New(), uuid.New()
	parent := uuid.New()
	f.exec(tx, `INSERT INTO app.jobs(id,account_id,kind,priority) VALUES($1,$2,'manual_ingest',80)`, parent, f.accountID)
	parameters := fmt.Sprintf(`{"sourceId":"%s","generation":1,"mode":"incremental"}`, strings.ToUpper(strings.ReplaceAll(f.sourceID.String(), "-", "")))
	f.exec(tx, `INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority,parameters) VALUES($1,$2,$3,'manual_ingest_source',80,$4)`, f.ingestID, parent, f.accountID, parameters)
	f.exec(tx, `INSERT INTO app.job_config_snapshots(job_id,account_id,source_id,source_generation,config_envelope) VALUES($1,$2,$3,1,$4)`, f.ingestID, f.accountID, f.sourceID, acceptanceConfigEnvelope(t, f.accountID, f.ingestID, sourcecrypto.JobConfigSnapshot))
	f.commit(tx)
	tx = f.accountTx(f.ctx, f.workerDB)
	var claimed bool
	f.scan(tx, `SELECT app.claim_job($1,'coverage-fixture-ingest',$2,interval '60 minutes')`, []any{f.ingestID, f.ingestLease}, &claimed)
	if !claimed {
		t.Fatal("backlog ingest lease rejected")
	}
	f.fence(tx)
	f.fileID = uuid.New()
	hash := sha256.Sum256([]byte("public synthetic revision"))
	f.exec(tx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes,checksum_sha256,state,processing_started_at,processed_at)
		VALUES($1,$2,$3,$4,'synthetic-revision.json',1,$5,'succeeded',clock_timestamp(),clock_timestamp())`, f.fileID, f.accountID, f.sourceID, f.ingestID, hash[:])
	f.commit(tx)
	// A different route revision is the target. Applied copies remain untouched
	// until the real production runner replaces each result atomically.
	byID := map[uuid.UUID]acceptanceScheduledWorkout{}
	for _, w := range schedule {
		byID[acceptanceID("workout", w.Index)] = w
	}
	pointCounts := make([]int, 0, count)
	selected := make([]uuid.UUID, 0, count)
	for i := range count {
		// Evenly spaced through the entire calendar, not a fixed September slice.
		id := eligible[i*len(eligible)/count]
		w, ok := byID[id]
		if !ok {
			t.Fatal("eligible route does not belong to selected profile schedule")
		}
		tx = f.accountTx(f.ctx, f.workerDB)
		f.fence(tx)
		var start, end time.Time
		var key, label, workoutLabel string
		var indoor *bool
		var duration, distance float64
		f.scan(tx, `SELECT w.started_at,w.ended_at,t.type_key,t.provider_label,w.provider_label,w.is_indoor,w.provider_duration,a.value*1000 FROM app.workouts w
			JOIN app.workout_types t ON t.account_id=w.account_id AND t.id=w.workout_type_id
			JOIN app.workout_aggregates a ON a.account_id=w.account_id AND a.workout_id=w.id AND a.metric='distance' AND a.unit='km'
			WHERE w.account_id=$1 AND w.id=$2`, []any{f.accountID, id}, &start, &end, &key, &label, &workoutLabel, &indoor, &duration, &distance)
		if !start.Equal(w.Start) || !end.Equal(w.Start.Add(time.Duration(w.Activity.DurationSeconds)*time.Second)) || key != healthautoexport.NormalizedTypeKey(w.Activity.Label) || label != w.Activity.Label || workoutLabel != label || indoor == nil || *indoor ||
			duration != float64(w.Activity.DurationSeconds) || math.Abs(distance-w.Activity.DistanceMiles*profile.Manifest.DistanceMetersPerMile) > 1e-6 {
			t.Fatal("stored backlog source facts differ from profile")
		}
		route, err := acceptanceGenerateRoute(grid, profile.Manifest, w, revision)
		if err != nil {
			t.Fatal(err)
		}
		var oldDigest []byte
		f.scan(tx, `SELECT route_input_sha256 FROM app.workout_coverage_states WHERE account_id=$1 AND workout_id=$2`, []any{f.accountID, id}, &oldDigest)
		digest := canonicalRouteInputSHA256(route.Points)
		if bytes.Equal(oldDigest, digest[:]) {
			t.Fatal("backlog variant did not change route digest")
		}
		f.exec(tx, `UPDATE app.workouts SET source_file_id=$3 WHERE account_id=$1 AND id=$2`, f.accountID, id, f.fileID)
		acceptanceWriteRoute(f, tx, id, route.Points)
		f.commit(tx)
		job := f.queue(id)
		children = append(children, job.childID)
		pointCounts = append(pointCounts, len(route.Points))
		selected = append(selected, id)
	}
	tx = f.accountTx(f.ctx, f.workerDB)
	f.scan(tx, `SELECT app.finish_job($1,'coverage-fixture-ingest',$2,'succeeded')`, []any{f.ingestID, f.ingestLease}, &claimed)
	if !claimed {
		t.Fatal("backlog ingest completion rejected")
	}
	f.commit(tx)
	tx = f.accountTx(f.ctx, f.apiDB)
	var retained int
	f.scan(tx, `SELECT count(DISTINCT workout_id) FROM app.workout_segment_matches WHERE account_id=$1`, []any{f.accountID}, &retained)
	f.commit(tx)
	if retained != profile.routedCount() {
		t.Fatalf("applied evidence was not retained: %d", retained)
	}
	tx = f.accountTx(f.ctx, f.adminDB)
	var createdStart, createdEnd time.Time
	var actual int
	f.scan(tx, `SELECT min(created_at),max(created_at),count(*) FROM app.jobs WHERE id=ANY($1::uuid[]) AND kind='coverage_update_route' AND account_id=$2`, []any{children, f.accountID}, &createdStart, &createdEnd, &actual)
	f.commit(tx)
	if actual != count {
		t.Fatalf("explicit queue children=%d, want %d", actual, count)
	}
	artifact := profile.metadata()
	artifact["children"], artifact["created_start"], artifact["created_end"], artifact["preparation_seconds"] = count, createdStart, createdEnd, time.Since(queueStarted).Seconds()
	artifact["retained_applied_workouts"], artifact["route_point_counts"], artifact["workout_ids"] = retained, pointCounts, selected
	data, _ := json.MarshalIndent(artifact, "", "  ")
	if err := os.WriteFile(profile.artifact(fmt.Sprintf("queue%d", count)), append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("queued %d full profile route children across the year; %d existing coverage rows retain evidence", count, retained)
}

func TestCoverageAcceptanceMatcherSmoke(t *testing.T) {
	f := acceptanceFixture(t)
	if os.Getenv("COVERAGE_ACCEPTANCE_ACTION") != "matcher-smoke" {
		t.Skip("real matcher correctness smoke only")
	}
	profile := acceptanceLoadProfile(t)
	for _, a := range profile.Manifest.Profiles[profile.Name].Activities[:3] {
		if a.Count == 0 {
			t.Fatal("three-type matcher smoke requires positive Outdoor Run, Outdoor Walk, and Outdoor Cycle counts")
		}
	}
	namespaceUID := os.Getenv("COVERAGE_ACCEPTANCE_NAMESPACE_UID")
	if _, err := uuid.Parse(namespaceUID); err != nil {
		t.Fatal("matcher smoke requires an explicit disposable namespace UUID")
	}
	ctx, cancel := context.WithTimeout(f.ctx, 12*time.Minute)
	t.Cleanup(cancel)
	f.ctx = ctx
	f.accountID, f.sourceID = acceptanceID("account", 0), acceptanceID("source", 0)
	var binding struct {
		Profile      string `json:"profile"`
		Version      int    `json:"profile_version"`
		Hash         string `json:"profile_manifest_sha256"`
		NamespaceUID string `json:"namespace_uid"`
	}
	// The operator-owned marker is intentionally unreadable to app roles. The
	// existing read-only preflight checks namespace, ConfigMap, and DB markers.
	raw, err := exec.CommandContext(f.ctx, "node", "-e", `const p=require("../deploy/acceptance/profile.cjs"); const ns=p.requireProfile(); process.stdout.write(JSON.stringify({...p.binding,namespace_uid:ns.metadata.uid}));`).Output()
	if err != nil {
		t.Fatalf("read-only acceptance binding preflight failed: %v", err)
	}
	if err := json.Unmarshal(raw, &binding); err != nil {
		t.Fatal(err)
	}
	if binding.Profile != profile.Name || binding.Version != profile.Manifest.SchemaVersion || binding.Hash != profile.SHA256 || binding.NamespaceUID != namespaceUID {
		t.Fatal("live database profile/namespace binding differs from the loaded manifest and supplied UID")
	}
	tx := f.accountTx(f.ctx, f.adminDB)
	var ownerOK bool
	f.scan(tx, `SELECT EXISTS(SELECT 1 FROM app.accounts a JOIN app.sources s ON s.account_id=a.id
		JOIN app.users u ON u.account_id=a.id JOIN app.authentication_principals p ON p.id=u.principal_id
		WHERE a.id=$1 AND s.id=$2 AND s.generation=1 AND p.username='acceptance-fixture')`, []any{f.accountID, f.sourceID}, &ownerOK)
	var active int
	f.scan(tx, `SELECT (SELECT count(*) FROM app.jobs WHERE kind='coverage_update_route' AND status IN ('queued','running'))
		+(SELECT count(*) FROM app.matcher_slots)`, nil, &active)
	f.commit(tx)
	if !ownerOK || active != 0 {
		t.Fatalf("refusing unrelated owner/work: fixture owner=%t active jobs/slots=%d", ownerOK, active)
	}
	config := f.osmAdminDB.Config()
	config.ConnConfig.User = "workouts_coverage_worker"
	config.MaxConns = 1
	config.ConnConfig.RuntimeParams["application_name"] = "coverage-acceptance-matcher-smoke"
	reader, err := pgxpool.NewWithConfig(f.ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.Close)
	if err := validateCoverageAcceptanceOSMReader(f.ctx, reader); err != nil {
		t.Fatal(err)
	}
	var runnerLogs bytes.Buffer
	runner := NewCoverageRunnerWithOptions(f.coverageDB, reader, slog.New(slog.NewJSONHandler(io.MultiWriter(os.Stderr, &runnerLogs), nil)), CoverageRunnerOptions{
		LeaseDuration: 4 * time.Minute, HeartbeatInterval: 20 * time.Second, RouteTimeout: 3 * time.Minute})
	started := time.Now().UTC()
	artifact := profile.metadata()
	artifact["scope"] = "correctness smoke on host, not resource-capped performance"
	artifact["started_at"], artifact["account_id"], artifact["osm_role"] = started, f.accountID, "workouts_coverage_worker"
	artifact["osm_reader_privileges_validated"] = true
	artifact["lease_seconds"], artifact["heartbeat_seconds"], artifact["route_timeout_seconds"] = 240, 20, 180
	results := []map[string]any{}
	writeArtifact := func() {
		artifact["results"], artifact["recorded_at"], artifact["elapsed_seconds"] = results, time.Now().UTC(), time.Since(started).Seconds()
		artifact["status"] = "FAIL"
		if !t.Failed() && len(results) == 3 {
			artifact["status"] = "PASS"
		}
		data, err := json.MarshalIndent(artifact, "", "  ")
		if err != nil {
			t.Error(err)
			return
		}
		if err := os.WriteFile(profile.artifact("matcher-smoke"), append(data, '\n'), 0600); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(writeArtifact)
	schedule, err := acceptanceSchedule(profile.Manifest, profile.Name)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[uuid.UUID]acceptanceScheduledWorkout{}
	for _, w := range schedule {
		byID[acceptanceID("workout", w.Index)] = w
	}
	grid := acceptanceLoadGrid(f)
	// Snapshot the immutable workout facts and applied evidence before ordinary
	// fenced route revisions; no shortened or alternate-account inputs are used.
	type smokeCase struct {
		ID          uuid.UUID
		Workout     acceptanceScheduledWorkout
		Facts       []byte
		OldRevision int64
		OldEvidence string
		Route       acceptanceGeneratedRoute
	}
	cases := []smokeCase{}
	for _, activity := range profile.Manifest.Profiles[profile.Name].Activities[:3] {
		tx = f.accountTx(f.ctx, f.adminDB)
		var c smokeCase
		f.scan(tx, `SELECT w.id,to_jsonb(w)-ARRAY['source_file_id','updated_at'],s.route_input_revision,
			(SELECT md5(string_agg(m.physical_segment_id::text||':'||encode(ST_AsEWKB(m.geom),'hex')||':'||m.first_traversed_at::text,',' ORDER BY m.physical_segment_id))
			 FROM app.workout_segment_matches m WHERE m.account_id=w.account_id AND m.workout_id=w.id)
			FROM app.workouts w JOIN app.workout_types t ON t.account_id=w.account_id AND t.id=w.workout_type_id
			JOIN app.workout_routes r ON r.account_id=w.account_id AND r.workout_id=w.id
			JOIN app.workout_coverage_states s ON s.account_id=w.account_id AND s.workout_id=w.id
			WHERE w.account_id=$1 AND t.type_key=$2 AND t.provider_label=$3 AND w.provider_label=$3 AND w.is_indoor=false AND w.deletion_requested_at IS NULL AND r.route IS NOT NULL
			AND s.readiness_state='map_data_ready' AND s.processing_state='current' AND s.applied_route_input_revision=s.route_input_revision
			ORDER BY w.started_at,w.id LIMIT 1`, []any{f.accountID, healthautoexport.NormalizedTypeKey(activity.Label), activity.Label}, &c.ID, &c.Facts, &c.OldRevision, &c.OldEvidence)
		f.commit(tx)
		var ok bool
		c.Workout, ok = byID[c.ID]
		if !ok || c.Workout.Activity != activity {
			t.Fatal("smoke selection differs from stress schedule")
		}
		c.Route, err = acceptanceGenerateRoute(grid, profile.Manifest, c.Workout, c.OldRevision+1)
		if err != nil {
			t.Fatal(err)
		}
		if len(c.Route.Points) > coverageOriginalPointLimit {
			t.Fatal("smoke input exceeds the full-route read limit")
		}
		cases = append(cases, c)
	}
	f.ingestID, f.ingestLease, f.fileID = uuid.New(), uuid.New(), uuid.New()
	parent := uuid.New()
	tx = f.accountTx(f.ctx, f.adminDB)
	f.exec(tx, `INSERT INTO app.jobs(id,account_id,kind,priority) VALUES($1,$2,'manual_ingest',80)`, parent, f.accountID)
	parameters := fmt.Sprintf(`{"sourceId":"%s","generation":1,"mode":"incremental"}`, strings.ToUpper(strings.ReplaceAll(f.sourceID.String(), "-", "")))
	f.exec(tx, `INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority,parameters) VALUES($1,$2,$3,'manual_ingest_source',80,$4)`, f.ingestID, parent, f.accountID, parameters)
	f.exec(tx, `INSERT INTO app.job_config_snapshots(job_id,account_id,source_id,source_generation,config_envelope) VALUES($1,$2,$3,1,$4)`, f.ingestID, f.accountID, f.sourceID, acceptanceConfigEnvelope(t, f.accountID, f.ingestID, sourcecrypto.JobConfigSnapshot))
	f.commit(tx)
	tx = f.accountTx(f.ctx, f.workerDB)
	var claimed bool
	f.scan(tx, `SELECT app.claim_job($1,'coverage-fixture-ingest',$2,interval '15 minutes')`, []any{f.ingestID, f.ingestLease}, &claimed)
	if !claimed {
		t.Fatal("smoke ingest lease rejected")
	}
	f.fence(tx)
	hash := sha256.Sum256([]byte("public synthetic full-route matcher smoke"))
	f.exec(tx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes,checksum_sha256,state,processing_started_at,processed_at)
		VALUES($1,$2,$3,$4,'synthetic-matcher-smoke.json',1,$5,'succeeded',clock_timestamp(),clock_timestamp())`, f.fileID, f.accountID, f.sourceID, f.ingestID, hash[:])
	f.commit(tx)
	for _, c := range cases {
		tx = f.accountTx(f.ctx, f.workerDB)
		f.fence(tx)
		var oldDigest []byte
		f.scan(tx, `SELECT route_input_sha256 FROM app.workout_coverage_states WHERE account_id=$1 AND workout_id=$2`, []any{f.accountID, c.ID}, &oldDigest)
		digest := canonicalRouteInputSHA256(c.Route.Points)
		if bytes.Equal(oldDigest, digest[:]) {
			t.Fatal("smoke variant did not change the route digest")
		}
		f.exec(tx, `UPDATE app.workouts SET source_file_id=$3 WHERE account_id=$1 AND id=$2`, f.accountID, c.ID, f.fileID)
		acceptanceWriteRoute(f, tx, c.ID, c.Route.Points)
		f.commit(tx)
		tx = f.accountTx(f.ctx, f.adminDB)
		var evidence string
		var appliedRevision int64
		f.scan(tx, `SELECT s.applied_route_input_revision,
			(SELECT md5(string_agg(m.physical_segment_id::text||':'||encode(ST_AsEWKB(m.geom),'hex')||':'||m.first_traversed_at::text,',' ORDER BY m.physical_segment_id))
			 FROM app.workout_segment_matches m WHERE m.account_id=s.account_id AND m.workout_id=s.workout_id)
			FROM app.workout_coverage_states s WHERE s.account_id=$1 AND s.workout_id=$2`, []any{f.accountID, c.ID}, &appliedRevision, &evidence)
		f.commit(tx)
		if appliedRevision != c.OldRevision || evidence != c.OldEvidence {
			t.Fatal("route revision changed the retained applied evidence before matching")
		}
	}
	tx = f.accountTx(f.ctx, f.workerDB)
	f.scan(tx, `SELECT app.finish_job($1,'coverage-fixture-ingest',$2,'succeeded')`, []any{f.ingestID, f.ingestLease}, &claimed)
	if !claimed {
		t.Fatal("smoke ingest completion rejected")
	}
	f.commit(tx)
	for _, c := range cases {
		activity := c.Workout.Activity.Label
		key := healthautoexport.NormalizedTypeKey(activity)
		job := f.queue(c.ID)
		result := map[string]any{"activity": activity, "type_key": key, "bucket": c.Workout.Activity.Key, "workout_id": c.ID, "job_id": job.childID, "point_count": len(c.Route.Points),
			"movement_mode": string(routepipeline.MovementMode(key, c.Workout.Activity.Label)), "input_duration_seconds": c.Workout.Activity.DurationSeconds,
			"distance_meters": c.Route.Meters, "old_applied_revision": c.OldRevision}
		results = append(results, result)
		t.Logf("matcher smoke starting activity=%s workout=%s full_points=%d duration=%ds distance=%.3fm", activity, c.ID, len(c.Route.Points), c.Workout.Activity.DurationSeconds, c.Route.Meters)
		runnerLogs.Reset()
		runStarted := time.Now()
		worked, runErr := runner.RunOnce(f.ctx)
		result["run_once_seconds"], result["worked"] = time.Since(runStarted).Seconds(), worked
		if runErr != nil {
			result["run_error"] = runErr.Error()
		}
		// Terminal jobs intentionally clear worker_id and all lease fields. The
		// local runner's completion record identifies the actual RunOnce target.
		completedHere := false
		decoder := json.NewDecoder(bytes.NewReader(runnerLogs.Bytes()))
		for {
			var record struct {
				Message  string `json:"msg"`
				JobID    string `json:"job_id"`
				Outcome  string `json:"outcome"`
				Segments int    `json:"segments"`
			}
			if err := decoder.Decode(&record); err == io.EOF {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			if record.Message == "coverage route completed" && record.JobID == job.childID.String() && record.Outcome == "applied" && record.Segments > 0 {
				completedHere = true
			}
		}
		result["processed_by_smoke_runner"], result["runner_worker_id"] = completedHere, runner.workerID
		tx = f.accountTx(f.ctx, f.adminDB)
		var status, outcome, workerID, failureCode, failureSummary, processing, evidence string
		var revision, appliedRevision int64
		var generationsOK bool
		var digest []byte
		f.scan(tx, `SELECT j.status,COALESCE(c.result_outcome,''),COALESCE(j.worker_id,''),COALESCE(j.failure_code,''),COALESCE(j.failure_summary,''),
			s.processing_state,s.route_input_revision,s.applied_route_input_revision,s.applied_route_input_sha256,
			app.coverage_generation_vectors_equal(s.applied_generations,'[{"regionId":"fixture:dense","generation":1}]'::jsonb)
			FROM app.jobs j JOIN app.coverage_route_job_contexts c ON c.job_id=j.id AND c.account_id=j.account_id
			JOIN app.workout_coverage_states s ON s.workout_id=c.workout_id AND s.account_id=c.account_id
			WHERE j.id=$1 AND j.account_id=$2`, []any{job.childID, f.accountID}, &status, &outcome, &workerID, &failureCode, &failureSummary, &processing, &revision, &appliedRevision, &digest, &generationsOK)
		var matches, invalidMatches int
		var covered float64
		f.scan(tx, `SELECT count(*),count(*) FILTER(WHERE m.generation_id<>1 OR m.region_id<>'fixture:dense' OR m.covered_meters<=0 OR ST_IsEmpty(m.geom) OR NOT ST_IsValid(m.geom)
			OR m.covered_meters>s.segment_meters+.001 OR NOT ST_CoveredBy(m.geom,ST_Buffer(s.geom,1e-12))),COALESCE(sum(m.covered_meters),0),
			COALESCE(md5(string_agg(m.physical_segment_id::text||':'||encode(ST_AsEWKB(m.geom),'hex')||':'||m.first_traversed_at::text,',' ORDER BY m.physical_segment_id)),'')
			FROM app.workout_segment_matches m JOIN app.path_segments s ON s.account_id=m.account_id AND s.physical_segment_id=m.physical_segment_id
			AND s.region_id=m.region_id AND s.generation_id=m.generation_id WHERE m.account_id=$1 AND m.workout_id=$2`, []any{f.accountID, c.ID}, &matches, &invalidMatches, &covered, &evidence)
		var facts []byte
		var geometryMeters, providerMeters float64
		var pointCount int
		var firstTime, lastTime time.Time
		f.scan(tx, `SELECT to_jsonb(w)-ARRAY['source_file_id','updated_at'],a.value*1000,ST_Length(r.route::geography),
			(SELECT count(*) FROM app.workout_route_points p WHERE p.account_id=w.account_id AND p.workout_id=w.id),
			(SELECT min(recorded_at) FROM app.workout_route_points p WHERE p.account_id=w.account_id AND p.workout_id=w.id),
			(SELECT max(recorded_at) FROM app.workout_route_points p WHERE p.account_id=w.account_id AND p.workout_id=w.id)
			FROM app.workouts w JOIN app.workout_aggregates a ON a.account_id=w.account_id AND a.workout_id=w.id AND a.metric='distance' AND a.unit='km'
			JOIN app.workout_routes r ON r.account_id=w.account_id AND r.workout_id=w.id WHERE w.account_id=$1 AND w.id=$2`, []any{f.accountID, c.ID}, &facts, &providerMeters, &geometryMeters, &pointCount, &firstTime, &lastTime)
		rows, err := tx.Query(f.ctx, `SELECT sequence,recorded_at,latitude,longitude,altitude,speed,course,horizontal_accuracy,vertical_accuracy,speed_accuracy,course_accuracy
			FROM app.workout_route_points WHERE account_id=$1 AND workout_id=$2 ORDER BY sequence`, f.accountID, c.ID)
		if err != nil {
			t.Fatal(err)
		}
		points := []coverageRoutePoint{}
		for rows.Next() {
			var p coverageRoutePoint
			if err := rows.Scan(&p.Sequence, &p.Timestamp, &p.Latitude, &p.Longitude, &p.Altitude, &p.Speed, &p.Course, &p.HorizontalAccuracy, &p.VerticalAccuracy, &p.SpeedAccuracy, &p.CourseAccuracy); err != nil {
				t.Fatal(err)
			}
			points = append(points, p)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		f.commit(tx)
		expectedDigest := canonicalRouteInputSHA256(c.Route.Points)
		actualDigest := canonicalRouteInputSHA256(points)
		result["status"], result["outcome"], result["failure_code"], result["failure_summary"] = status, outcome, failureCode, failureSummary
		result["route_revision"], result["applied_revision"], result["segment_matches"], result["unique_covered_meters"] = revision, appliedRevision, matches, covered
		result["stored_point_count"], result["geometry_meters"], result["generation_vector_matches"] = pointCount, geometryMeters, generationsOK
		result["evidence_replaced"] = evidence != c.OldEvidence
		if !worked || !completedHere || runErr != nil || status != "succeeded" || outcome != "applied" || workerID != "" || failureCode != "" || processing != "current" ||
			revision != c.OldRevision+1 || appliedRevision != revision || !generationsOK || !bytes.Equal(digest, expectedDigest[:]) || actualDigest != expectedDigest || matches == 0 || invalidMatches != 0 ||
			covered <= 0 || covered > c.Route.Meters+1 || evidence == c.OldEvidence || !bytes.Equal(facts, c.Facts) || math.Abs(providerMeters-c.Route.Meters) > 1e-6 || math.Abs(geometryMeters-c.Route.Meters) > 1 ||
			pointCount != len(c.Route.Points) || len(points) != pointCount || !firstTime.Equal(c.Workout.Start) || !lastTime.Equal(c.Workout.Start.Add(time.Duration(c.Workout.Activity.DurationSeconds)*time.Second)) {
			t.Errorf("matcher smoke %s failed: worked=%t err=%v status=%s outcome=%s failure=%s/%s matches=%d invalid=%d revision=%d applied=%d points=%d/%d geometry=%.3fm target=%.3fm worker=%s", activity, worked, runErr, status, outcome, failureCode, failureSummary, matches, invalidMatches, revision, appliedRevision, pointCount, len(c.Route.Points), geometryMeters, c.Route.Meters, workerID)
		}
		writeArtifact()
		t.Logf("matcher smoke completed activity=%s status=%s outcome=%s full_points=%d matches=%d elapsed=%s", activity, status, outcome, pointCount, matches, time.Since(runStarted).Round(time.Millisecond))
	}
}

func BenchmarkCoverageAcceptanceReads(b *testing.B) {
	f := acceptanceFixture(b)
	if os.Getenv("COVERAGE_ACCEPTANCE_ACTION") != "benchmark" {
		b.Skip("benchmark only")
	}
	f.accountID = acceptanceID("account", 0)
	profile := acceptanceLoadProfile(b)
	start, end := profile.dateRange(0)
	b.ResetTimer()
	for range b.N {
		tx := f.accountTx(f.ctx, f.apiDB)
		var count int
		f.scan(tx, `SELECT count(*) FROM app.workouts w JOIN app.workout_types t ON t.account_id=w.account_id AND t.id=w.workout_type_id
			WHERE w.account_id=$1 AND w.local_start_date BETWEEN $2::date AND $3::date AND t.type_key=ANY($4::text[])`, []any{f.accountID, start, end, profile.typeKeys(false)}, &count)
		coverageIntegrationRollback(tx)
		if count != profile.Manifest.Profiles[profile.Name].TotalWorkouts {
			b.Fatalf("workouts=%d", count)
		}
	}
}

func TestCoverageAcceptanceDiagnostics(t *testing.T) {
	f := acceptanceFixture(t)
	if os.Getenv("COVERAGE_ACCEPTANCE_ACTION") != "diagnostics" {
		t.Skip("diagnostics only")
	}
	f.accountID = acceptanceID("account", 0)
	profile := acceptanceLoadProfile(t)
	start, end := profile.dateRange(0)
	focus := acceptanceEligibleWorkout(f, profile, f.accountID, start, end)
	tx := f.accountTx(f.ctx, f.apiDB)
	var diagnosticsEnabled bool
	f.scan(tx, `SELECT coverage_diagnostics_enabled FROM app.preferences WHERE account_id=$1 FOR UPDATE`, []any{f.accountID}, &diagnosticsEnabled)
	f.exec(tx, `UPDATE app.preferences SET coverage_diagnostics_enabled=true WHERE account_id=$1`, f.accountID)
	f.commit(tx)
	defer func() {
		tx := f.accountTx(f.ctx, f.apiDB)
		f.exec(tx, `UPDATE app.preferences SET coverage_diagnostics_enabled=$2 WHERE account_id=$1`, f.accountID, diagnosticsEnabled)
		f.commit(tx)
	}()
	slot, requestID := uuid.New(), uuid.New()
	acquired := false
	for i := 0; i < 100 && !acquired; i++ {
		tx := f.accountTx(f.ctx, f.apiDB)
		f.scan(tx, `SELECT app.acquire_diagnostic_matcher_slot($1,'acceptance-diagnostic-guard',$2,$3,ARRAY[]::text[],interval '15 seconds')`, []any{f.accountID, slot, requestID}, &acquired)
		f.commit(tx)
		if !acquired {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if !acquired {
		t.Fatal("cannot obtain controlled bounded diagnostic slot")
	}
	defer func() {
		tx := f.accountTx(f.ctx, f.apiDB)
		f.exec(tx, `SELECT app.release_matcher_slot($1,'acceptance-diagnostic-guard',$2)`, slot, requestID)
		f.commit(tx)
	}()
	// Keep the global cap occupied across production heartbeat ticks, then verify
	// API rejection without running another matcher or raising concurrency.
	time.Sleep(3 * time.Second)
	raw := sha256.Sum256([]byte("FAKE disposable synthetic coverage acceptance bearer; never a live credential"))
	statuses := map[int]int{}
	client := &http.Client{Timeout: 5 * time.Second}
	for range 8 {
		req, err := http.NewRequestWithContext(f.ctx, "POST", "http://127.0.0.1:18081/api/workouts/"+focus.String()+"/coverage-diagnostic-runs", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+base64.RawURLEncoding.EncodeToString(raw[:]))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		statuses[resp.StatusCode]++
		if resp.StatusCode != 429 {
			t.Fatalf("diagnostic must be bounded 429, got %d", resp.StatusCode)
		}
	}
	t.Logf("competing diagnostic outcomes=%v; global matcher limit unchanged at 1", statuses)
}

func TestCoverageAcceptanceValidateFixture(t *testing.T) {
	f := acceptanceFixture(t)
	if os.Getenv("COVERAGE_ACCEPTANCE_ACTION") != "validate" {
		t.Skip("read-only fixture validation only")
	}
	profile := acceptanceLoadProfile(t)
	schedule, err := acceptanceSchedule(profile.Manifest, profile.Name)
	if err != nil {
		t.Fatal(err)
	}
	f.accountID = acceptanceID("account", 0)
	tx := f.accountTx(f.ctx, f.adminDB)
	defer coverageIntegrationRollback(tx)
	var active int
	f.scan(tx, `SELECT count(*) FROM app.jobs WHERE kind='coverage_update_route' AND status IN ('queued','running')`, nil, &active)
	if active != 0 {
		t.Fatalf("validation requires a drained backlog, active=%d", active)
	}
	var labels []string
	for _, a := range profile.typeCatalog(false) {
		labels = append(labels, a.Label)
	}
	var invalidTypes int
	f.scan(tx, `WITH expected AS (SELECT * FROM unnest($2::text[],$3::text[]) e(type_key,provider_label))
		SELECT count(*) FROM expected e FULL JOIN (SELECT type_key,provider_label FROM app.workout_types WHERE account_id=$1) t
		ON t.type_key=e.type_key AND t.provider_label=e.provider_label WHERE t.type_key IS NULL OR e.type_key IS NULL`,
		[]any{f.accountID, profile.typeKeys(false), labels}, &invalidTypes)
	if invalidTypes != 0 {
		t.Fatal("stored canonical type keys/provider labels differ from the profile catalog")
	}
	rows, err := tx.Query(f.ctx, `SELECT w.id,w.started_at,w.ended_at,w.local_start_date::text,w.provider_duration,t.type_key,t.provider_label,w.provider_label,w.is_indoor,
		r.route IS NOT NULL,COALESCE(a.value*1000,0)::float8,a.workout_id IS NOT NULL,COALESCE(a.unit,''),
		COALESCE(s.readiness_state,''),s.applied_route_input_revision IS NOT NULL
		FROM app.workouts w JOIN app.workout_types t ON t.id=w.workout_type_id AND t.account_id=w.account_id
		LEFT JOIN app.workout_routes r ON r.workout_id=w.id AND r.account_id=w.account_id
		LEFT JOIN app.workout_aggregates a ON a.workout_id=w.id AND a.account_id=w.account_id AND a.metric='distance'
		LEFT JOIN app.workout_coverage_states s ON s.workout_id=w.id AND s.account_id=w.account_id
		WHERE w.account_id=$1 AND w.deletion_requested_at IS NULL ORDER BY w.started_at,w.id`, f.accountID)
	if err != nil {
		t.Fatal(err)
	}
	index := 0
	monthly, daily, types := map[string]int{}, map[string]int{}, map[string]int{}
	samples := map[string]uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		var start, end time.Time
		var date, key, typeLabel, label, unit, state string
		var duration, distance float64
		var indoor *bool
		var routed, hasDistance, applied bool
		if err := rows.Scan(&id, &start, &end, &date, &duration, &key, &typeLabel, &label, &indoor, &routed, &distance, &hasDistance, &unit, &state, &applied); err != nil {
			t.Fatal(err)
		}
		if index >= len(schedule) {
			t.Fatal("too many profile workouts")
		}
		w := schedule[index]
		if id != acceptanceID("workout", w.Index) || !start.Equal(w.Start) || !end.Equal(w.Start.Add(time.Duration(w.Activity.DurationSeconds)*time.Second)) ||
			date != w.Start.Format("2006-01-02") || duration != float64(w.Activity.DurationSeconds) || key != healthautoexport.NormalizedTypeKey(w.Activity.Label) || label != w.Activity.Label ||
			typeLabel != label || (w.Activity.Routed && (indoor == nil || *indoor)) || (!w.Activity.Routed && indoor != nil) || routed != w.Activity.Routed || hasDistance != routed || applied != routed ||
			(routed && (unit != "km" || state != "map_data_ready" || math.Abs(distance-w.Activity.DistanceMiles*profile.Manifest.DistanceMetersPerMile) > 1e-6)) || (!routed && state != "") {
			t.Fatalf("stored source facts/coverage differ from %s schedule at index=%d type=%s", profile.Name, index, key)
		}
		monthly[date[:7]]++
		daily[date]++
		types[w.Activity.Key]++
		if routed {
			if _, ok := samples[key]; !ok {
				samples[key] = id
			}
		}
		index++
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	expectedSampleTypes := 0
	for _, a := range profile.Manifest.Profiles[profile.Name].Activities[:3] {
		if a.Count > 0 {
			expectedSampleTypes++
		}
	}
	if index != len(schedule) || len(daily) != 365 || len(monthly) != 12 || len(samples) != expectedSampleTypes {
		t.Fatal("incomplete profile calendar/types")
	}
	for date, count := range daily {
		if count != profile.Manifest.Profiles[profile.Name].WorkoutsPerDay {
			t.Fatalf("daily count %s=%d", date, count)
		}
	}
	for month, count := range monthly {
		start, err := time.Parse("2006-01", month)
		if err != nil {
			t.Fatal(err)
		}
		want := start.AddDate(0, 1, 0).AddDate(0, 0, -1).Day() * profile.Manifest.Profiles[profile.Name].WorkoutsPerDay
		if count != want {
			t.Fatalf("monthly count %s=%d want %d", month, count, want)
		}
	}
	for _, a := range profile.Manifest.Profiles[profile.Name].Activities {
		count := types[a.Key]
		if !a.Routed {
			count = 0
			for _, other := range profile.Manifest.OtherTypes {
				count += types[other.Key]
			}
		}
		if count != a.Count {
			t.Fatalf("activity quota %s=%d want %d", a.Label, count, a.Count)
		}
	}
	var invalid int
	f.scan(tx, `SELECT count(*) FROM (SELECT w.local_start_date,w.workout_type_id FROM app.workouts w WHERE w.account_id=$1 GROUP BY 1,2 HAVING count(*)>1) duplicate`, []any{f.accountID}, &invalid)
	if invalid != 0 {
		t.Fatal("duplicate actual activity on a calendar day")
	}
	// All points, not just endpoints, must satisfy bounds, sequence/time/speed,
	// and independently measured WGS84 traveled distance.
	f.scan(tx, `WITH intervals AS (
		SELECT p.*,lag(recorded_at) OVER win prior_time,lag(sequence) OVER win prior_sequence,
		lag(ST_SetSRID(ST_MakePoint(longitude,latitude),4326)) OVER win prior_point
		FROM app.workout_route_points p WHERE account_id=$1 WINDOW win AS (PARTITION BY workout_id ORDER BY sequence)
	), measured AS (
		SELECT i.*,w.started_at,w.ended_at,w.provider_duration,a.value*1000 expected_meters,r.minimum_longitude,r.maximum_longitude,r.minimum_latitude,r.maximum_latitude,
		ST_Distance(prior_point::geography,ST_SetSRID(ST_MakePoint(longitude,latitude),4326)::geography) step_meters,
		extract(epoch FROM recorded_at-prior_time) seconds FROM intervals i
		JOIN app.workouts w ON w.id=i.workout_id AND w.account_id=i.account_id
		JOIN app.workout_aggregates a ON a.workout_id=w.id AND a.account_id=w.account_id AND a.metric='distance'
		JOIN app.workout_routes r ON r.workout_id=w.id AND r.account_id=w.account_id
	)
	SELECT count(*) FROM measured WHERE longitude NOT BETWEEN 0 AND .039 OR latitude NOT BETWEEN 0 AND .039
		OR longitude NOT BETWEEN minimum_longitude AND maximum_longitude OR latitude NOT BETWEEN minimum_latitude AND maximum_latitude
		OR recorded_at NOT BETWEEN started_at AND ended_at OR recorded_at<>date_trunc('microseconds',recorded_at)
		OR (prior_time IS NULL AND (sequence<>0 OR recorded_at<>started_at))
		OR (prior_time IS NOT NULL AND (sequence<>prior_sequence+1 OR seconds<=0 OR seconds>$2::float8+0.000001
		OR abs(step_meters/NULLIF(seconds,0)-expected_meters/provider_duration)>0.1))`, []any{f.accountID, float64(profile.Manifest.GPSSampleSeconds)}, &invalid)
	if invalid != 0 {
		t.Fatalf("invalid geographic bounds, monotonic timestamps, cadence, or interval speeds: %d", invalid)
	}
	f.scan(tx, `WITH steps AS (
		SELECT workout_id,recorded_at,ST_Distance(ST_SetSRID(ST_MakePoint(longitude,latitude),4326)::geography,
		lag(ST_SetSRID(ST_MakePoint(longitude,latitude),4326)) OVER (PARTITION BY workout_id ORDER BY sequence)::geography) meters
		FROM app.workout_route_points WHERE account_id=$1
	), totals AS (SELECT workout_id,sum(meters) meters,max(recorded_at) last_time,count(*) points FROM steps GROUP BY 1)
	SELECT count(*) FROM app.workouts w JOIN app.workout_routes r ON r.workout_id=w.id AND r.account_id=w.account_id
	JOIN app.workout_aggregates a ON a.workout_id=w.id AND a.account_id=w.account_id AND a.metric='distance'
	LEFT JOIN totals ON totals.workout_id=w.id WHERE w.account_id=$1 AND
	(totals.workout_id IS NULL OR totals.points<2 OR abs(totals.meters-a.value*1000)>1 OR abs(ST_Length(r.route::geography)-a.value*1000)>1 OR totals.last_time<>w.ended_at OR totals.points<>r.point_count)`, []any{f.accountID}, &invalid)
	if invalid != 0 {
		t.Fatalf("independent SQL geography distance/end time/point counts disagree: %d", invalid)
	}
	f.scan(tx, `SELECT count(*) FROM app.workouts w JOIN app.workout_types t ON t.id=w.workout_type_id AND t.account_id=w.account_id
		WHERE w.account_id=$1 AND NOT (t.type_key=ANY($2::text[])) AND (
		EXISTS(SELECT 1 FROM app.workout_route_points p WHERE p.account_id=w.account_id AND p.workout_id=w.id) OR
		EXISTS(SELECT 1 FROM app.workout_routes r WHERE r.account_id=w.account_id AND r.workout_id=w.id) OR
		EXISTS(SELECT 1 FROM app.workout_segment_matches m WHERE m.account_id=w.account_id AND m.workout_id=w.id) OR
		EXISTS(SELECT 1 FROM app.workout_coverage_states s WHERE s.account_id=w.account_id AND s.workout_id=w.id) OR
		EXISTS(SELECT 1 FROM app.coverage_route_job_contexts c WHERE c.account_id=w.account_id AND c.workout_id=w.id))`, []any{f.accountID, profile.typeKeys(true)}, &invalid)
	if invalid != 0 {
		t.Fatal("unrouted workouts acquired route points, summary, state, evidence, or coverage jobs")
	}
	f.scan(tx, `SELECT count(*) FROM app.workout_segment_matches m JOIN app.path_segments s
		ON s.account_id=m.account_id AND s.region_id=m.region_id AND s.generation_id=m.generation_id AND s.physical_segment_id=m.physical_segment_id
		JOIN app.workouts w ON w.account_id=m.account_id AND w.id=m.workout_id
		WHERE m.account_id=$1 AND (m.covered_meters<=0 OR m.covered_meters>s.segment_meters+.001
		OR NOT ST_CoveredBy(m.geom,ST_Buffer(s.geom,1e-12)) OR abs(m.covered_meters-ST_Length(m.geom::geography))>.001
		OR m.first_traversed_at NOT BETWEEN w.started_at AND w.ended_at OR m.first_route_order<0)`, []any{f.accountID}, &invalid)
	if invalid != 0 {
		t.Fatal("coverage evidence is not a physical segment subset")
	}
	var loopWorkouts int
	f.scan(tx, `SELECT count(*) FROM (SELECT m.workout_id,sum(m.covered_meters) covered FROM app.workout_segment_matches m WHERE m.account_id=$1 GROUP BY 1) evidence
		JOIN app.workout_aggregates a ON a.workout_id=evidence.workout_id AND a.account_id=$1 AND a.metric='distance'
		WHERE evidence.covered<a.value*1000-1`, []any{f.accountID}, &loopWorkouts)
	f.scan(tx, `SELECT count(*) FROM (SELECT m.workout_id,sum(m.covered_meters) covered FROM app.workout_segment_matches m WHERE m.account_id=$1 GROUP BY 1) evidence
		JOIN app.workout_aggregates a ON a.workout_id=evidence.workout_id AND a.account_id=$1 AND a.metric='distance'
		WHERE evidence.covered>a.value*1000+1`, []any{f.accountID}, &invalid)
	if invalid != 0 {
		t.Fatal("unique physical coverage exceeds traveled distance")
	}
	for key, id := range samples {
		rows, err := tx.Query(f.ctx, `SELECT sequence,recorded_at,latitude,longitude,altitude,speed,course,
			horizontal_accuracy,vertical_accuracy,speed_accuracy,course_accuracy FROM app.workout_route_points
			WHERE account_id=$1 AND workout_id=$2 ORDER BY sequence`, f.accountID, id)
		if err != nil {
			t.Fatal(err)
		}
		var points []coverageRoutePoint
		for rows.Next() {
			var p coverageRoutePoint
			if err := rows.Scan(&p.Sequence, &p.Timestamp, &p.Latitude, &p.Longitude, &p.Altitude, &p.Speed, &p.Course,
				&p.HorizontalAccuracy, &p.VerticalAccuracy, &p.SpeedAccuracy, &p.CourseAccuracy); err != nil {
				t.Fatal(err)
			}
			points = append(points, p)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		digest := canonicalRouteInputSHA256(points)
		var matches bool
		f.scan(tx, `SELECT route_input_sha256=$3 FROM app.workout_coverage_states WHERE account_id=$1 AND workout_id=$2`, []any{f.accountID, id, digest[:]}, &matches)
		if !matches {
			t.Fatalf("retained %s route input does not satisfy canonical matcher digest contract", key)
		}
	}
	f.commit(tx)
	var edges, ways int
	if err := f.osmAdminDB.QueryRow(f.ctx, `SELECT (SELECT count(*) FROM osm_canonical.path_segments),(SELECT count(*) FROM osm_canonical.ways)`).Scan(&edges, &ways); err != nil {
		t.Fatal(err)
	}
	if edges != 3120 || ways != 80 {
		t.Fatalf("canonical graph edges=%d ways=%d", edges, ways)
	}
	t.Logf("retained %s fixture validated: %d workouts, 365 dates, 12 calendar months, %d applied routed workouts; actual types/durations/distances, no-route isolation, SQL geography/speeds/bounds, %d dissolved loop workouts, %d canonical digests, 3120 edges; no seed writes", profile.Name, len(schedule), profile.routedCount(), loopWorkouts, len(samples))
}

func TestCoverageAcceptanceGeneralWorker(t *testing.T) {
	f := acceptanceFixture(t)
	if os.Getenv("COVERAGE_ACCEPTANCE_ACTION") != "general-worker" {
		t.Skip("general worker only")
	}
	profile := acceptanceLoadProfile(t)
	f.accountID = acceptanceID("account", 0)
	start, end := profile.dateRange(0)
	foreignWorkout := acceptanceEligibleWorkout(f, profile, f.accountID, start, end)
	f.accountID = uuid.New()
	principal, session := uuid.New(), uuid.New()
	raw := sha256.Sum256([]byte("FAKE general-worker acceptance " + session.String()))
	verifier := sha256.Sum256(raw[:])
	tx := f.accountTx(f.ctx, f.adminDB)
	f.exec(tx, `INSERT INTO app.accounts(id) VALUES($1)`, f.accountID)
	f.exec(tx, `INSERT INTO app.preferences(account_id) VALUES($1)`, f.accountID)
	username := "acceptance-general-" + principal.String()
	f.exec(tx, `INSERT INTO app.authentication_principals(id,role,username,canonical_username,email,canonical_email,canonicalization_version,password_hash,full_name)
		VALUES($1,'user',$2,$2,$3,$3,1,'not-a-login-hash','Public Synthetic Worker Owner')`, principal, username, username+"@example.test")
	f.exec(tx, `INSERT INTO app.users(principal_id,account_id) VALUES($1,$2)`, principal, f.accountID)
	f.exec(tx, `INSERT INTO app.sessions(id,principal_id,credential_kind,credential_verifier,expires_at) VALUES($1,$2,'bearer',$3,clock_timestamp()+interval '1 hour')`, session, principal, verifier[:])
	f.commit(tx)
	client := &http.Client{Timeout: 10 * time.Second}
	call := func(method, path, body string, want int) []byte {
		req, err := http.NewRequestWithContext(f.ctx, method, "http://127.0.0.1:18081"+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+base64.RawURLEncoding.EncodeToString(raw[:]))
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != want {
			t.Fatalf("synthetic general request status=%d body=%s", resp.StatusCode, data)
		}
		return data
	}
	started := time.Now()
	data := call("POST", "/api/sources", `{"displayName":"Public Synthetic Indoor Fixture","type":"health-auto-export-local","autoSyncEnabled":false,"config":{"version":1,"path":"/tmp/acceptance-source"}}`, 201)
	var source struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &source); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { call("DELETE", "/api/sources/"+source.ID, "", 204) })
	connected := false
	for i := 0; i < 100; i++ {
		data = call("GET", "/api/sources/"+source.ID, "", 200)
		var status struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(data, &status); err != nil {
			t.Fatal(err)
		}
		if status.Status == "connected" {
			connected = true
			break
		}
		if status.Status == "connection-failed" {
			t.Fatal("synthetic connection check failed")
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !connected {
		t.Fatal("connection check did not finish")
	}
	connectionMS := time.Since(started).Milliseconds()
	started = time.Now()
	data = call("POST", "/api/ingest", fmt.Sprintf(`{"sourceIds":[%q],"startDate":"2026-09-15","endDate":"2026-09-15"}`, source.ID), 202)
	var job struct {
		JobID string `json:"jobId"`
	}
	if err := json.Unmarshal(data, &job); err != nil {
		t.Fatal(err)
	}
	waitJob := func(id string) {
		for i := 0; i < 200; i++ {
			data := call("GET", "/api/jobs/"+id, "", 200)
			var result struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal(data, &result); err != nil {
				t.Fatal(err)
			}
			if result.Status == "succeeded" {
				return
			}
			if result.Status == "failed" || result.Status == "cancelled" {
				t.Fatalf("synthetic job ended %s", result.Status)
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatal("synthetic job did not finish")
	}
	waitJob(job.JobID)
	ingestMS := time.Since(started).Milliseconds()
	tx = f.accountTx(f.ctx, f.apiDB)
	var imported, unexpectedAttempts, coverageChildren int
	f.scan(tx, `SELECT count(*) FROM app.workouts`, nil, &imported)
	f.scan(tx, `SELECT count(*) FROM app.jobs WHERE kind IN ('source_connection_check','manual_ingest_source') AND status='succeeded' AND attempt<>1`, nil, &unexpectedAttempts)
	f.scan(tx, `SELECT count(*) FROM app.jobs WHERE kind='coverage_update_route'`, nil, &coverageChildren)
	f.commit(tx)
	if imported != 16 || unexpectedAttempts != 0 || coverageChildren != 0 {
		t.Fatalf("imported=%d unexpected attempts=%d coverage children=%d", imported, unexpectedAttempts, coverageChildren)
	}
	// A second owner cannot observe the dense account's workouts.
	call("GET", "/api/workouts/"+foreignWorkout.String()+"/route", "", 404)
	started = time.Now()
	data = call("DELETE", "/api/workouts?startDate=2026-09-15&endDate=2026-09-15", "", 202)
	if err := json.Unmarshal(data, &job); err != nil {
		t.Fatal(err)
	}
	waitJob(job.JobID)
	deleteMS := time.Since(started).Milliseconds()
	tx = f.accountTx(f.ctx, f.apiDB)
	f.scan(tx, `SELECT count(*) FROM app.workouts`, nil, &imported)
	f.commit(tx)
	if imported != 0 {
		t.Fatal("synthetic physical deletion incomplete")
	}
	artifact := map[string]any{"fixture": "separate disposable owner; 16 synthetic workouts, 17 points each, outside the synthetic OSM provider boundary", "connection_ms": connectionMS, "ingest_ms": ingestMS, "delete_ms": deleteMS, "imported": 16, "remaining": 0, "cross_owner_route_status": 404, "coverage_children_enqueued": 0,
		"route_less_probe": "blocked separately: worker/ingest.go rejects false from replace_workout_split_summary when app.workout_routes has no row; no production code changed"}
	for k, v := range profile.metadata() {
		artifact[k] = v
	}
	encoded, _ := json.MarshalIndent(artifact, "", "  ")
	if err := os.WriteFile(profile.artifact("general-worker"), append(encoded, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("general worker connection=%dms ingest16=%dms delete16=%dms; cross-owner route rejected", connectionMS, ingestMS, deleteMS)
}

func TestCoverageAcceptanceReads(t *testing.T) {
	f := acceptanceFixture(t)
	if os.Getenv("COVERAGE_ACCEPTANCE_ACTION") != "reads" {
		t.Skip("reads only")
	}
	input := acceptanceLoadProfile(t)
	namespaceUID := os.Getenv("COVERAGE_ACCEPTANCE_NAMESPACE_UID")
	if _, err := uuid.Parse(namespaceUID); err != nil {
		t.Fatal("COVERAGE_ACCEPTANCE_NAMESPACE_UID must be supplied by the disposable namespace driver")
	}
	f.accountID = acceptanceID("account", 0)
	activeChildren := func() int {
		tx := f.accountTx(f.ctx, f.adminDB)
		var count int
		f.scan(tx, `SELECT count(*) FROM app.jobs WHERE kind='coverage_update_route' AND status IN ('queued','running')`, nil, &count)
		f.commit(tx)
		return count
	}
	startedAt, activeStart := time.Now().UTC(), activeChildren()
	base := os.Getenv("COVERAGE_ACCEPTANCE_API_URL")
	u, err := url.Parse(base)
	if err != nil || u.Host != "127.0.0.1:18081" || u.Scheme != "http" {
		t.Fatal("API URL must be disposable localhost forward")
	}
	raw := sha256.Sum256([]byte("FAKE disposable synthetic coverage acceptance bearer; never a live credential"))
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	client := &http.Client{Timeout: 30 * time.Second}
	request := func(method, path, body string) (int, []byte, float64) {
		req, err := http.NewRequestWithContext(f.ctx, method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		started := time.Now()
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, data, float64(time.Since(started).Microseconds()) / 1000
	}
	results := map[string]acceptanceTiming{}
	pathCounts := map[string]int{}
	workoutCounts := map[string]int{}
	routedCounts := map[string]int{}
	type typeTotals struct{ count, routed int }
	typeCounts := map[string]map[string]typeTotals{}
	typeLabels := map[string]string{}
	routedTypes := map[string]bool{}
	for _, a := range input.typeCatalog(false) {
		typeLabels[healthautoexport.NormalizedTypeKey(a.Label)] = a.Label
	}
	for _, key := range input.typeKeys(true) {
		routedTypes[key] = true
	}
	tileMinimumBytes := map[string]int{}
	resolvePath := func(path string) string { return path }
	var refreshSelection func()
	measure := func(label, method, path, body string, want int) []byte {
		activeStart, windowStart := activeChildren(), time.Now().UTC()
		var values []float64
		statuses := map[int]int{}
		var data []byte
		for range 30 {
			started := time.Now()
			status, payload, _ := request(method, resolvePath(path), body)
			statuses[status]++
			for retry := 0; status == 404 && refreshSelection != nil && strings.HasPrefix(path, "/api/map-selections/") && retry < 3; retry++ {
				refreshSelection()
				status, payload, _ = request(method, resolvePath(path), body)
				statuses[status]++
			}
			ms := float64(time.Since(started).Microseconds()) / 1000
			values = append(values, ms)
			data = payload
			if status != want {
				t.Fatalf("%s status=%d body=%s", label, status, payload)
			}
			if strings.HasSuffix(label, "_tile") {
				if len(payload) == 0 {
					t.Fatalf("%s returned an empty tile", label)
				}
				if old := tileMinimumBytes[label]; old == 0 || len(payload) < old {
					tileMinimumBytes[label] = len(payload)
				}
			}
			if strings.Contains(label, "_paths_page") || strings.Contains(label, "_workouts_page") {
				var list struct {
					Items      []json.RawMessage `json:"items"`
					Pagination struct {
						Page       int `json:"page"`
						TotalItems int `json:"totalItems"`
					} `json:"pagination"`
				}
				if err := json.Unmarshal(payload, &list); err != nil {
					t.Fatal(err)
				}
				profile := strings.SplitN(label, "_", 2)[0]
				total := pathCounts[profile]
				if strings.Contains(label, "_workouts_page") {
					total = workoutCounts[profile]
				}
				page := 1
				if strings.HasSuffix(label, "page2") {
					page = 2
				}
				count := min(25, max(0, total-(page-1)*25))
				if list.Pagination.Page != page || list.Pagination.TotalItems != total || len(list.Items) != count {
					t.Fatalf("%s cardinality differs from scoped read-only SQL oracle: page=%d total=%d items=%d, want page=%d total=%d items=%d", label, list.Pagination.Page, list.Pagination.TotalItems, len(list.Items), page, total, count)
				}
				if strings.Contains(label, "_workouts_page") {
					for _, item := range list.Items {
						var workout generated.Workout
						if err := json.Unmarshal(item, &workout); err != nil {
							t.Fatal(err)
						}
						key := workout.Type.Key
						name, known := typeLabels[key]
						indoor, indoorErr := workout.Indoor.Get()
						if !known || workout.Type.DisplayName != name || key != healthautoexport.NormalizedTypeKey(name) || workout.RouteAvailable != routedTypes[key] ||
							(routedTypes[key] && (indoorErr != nil || indoor || workout.RoutePointCount < 2)) ||
							(!routedTypes[key] && (!workout.Indoor.IsNull() || workout.RoutePointCount != 0)) {
							t.Fatalf("%s workout activity/key/indoor/route fields differ from profile: %s", label, item)
						}
					}
				}
			}
			if strings.HasSuffix(label, "_summary") {
				var summary generated.WorkoutSummary
				if err := json.Unmarshal(payload, &summary); err != nil {
					t.Fatal(err)
				}
				profile := strings.TrimSuffix(label, "_summary")
				if summary.Totals.Count != int64(workoutCounts[profile]) || summary.Totals.RouteCount != int64(routedCounts[profile]) || len(summary.ByType) != len(typeCounts[profile]) {
					t.Fatalf("%s totals differ from canonical-type SQL oracle", label)
				}
				seen := map[string]bool{}
				for _, item := range summary.ByType {
					key := item.Type.Key
					want, ok := typeCounts[profile][key]
					if !ok || seen[key] || item.Type.DisplayName != typeLabels[key] || item.Totals.Count != int64(want.count) || item.Totals.RouteCount != int64(want.routed) {
						t.Fatalf("%s per-activity totals differ from canonical-type SQL oracle: %+v", label, item)
					}
					seen[key] = true
				}
			}
		}
		first := values[0]
		sort.Float64s(values)
		results[label] = acceptanceTiming{Samples: len(values), FirstMS: first, P50MS: values[len(values)/2], P95MS: values[28], MaxMS: values[29], Statuses: statuses,
			StartedAt: windowStart, FinishedAt: time.Now().UTC(), ActiveStart: activeStart, ActiveEnd: activeChildren()}
		return data
	}
	monthStart, monthEnd := input.dateRange(time.September)
	yearStart, yearEnd := input.dateRange(0)
	for _, profile := range []struct{ name, start, end string }{{"month", monthStart, monthEnd}, {"year", yearStart, yearEnd}} {
		// This fixture has named road paths only. Count distinct applied paths in
		// the same account/date scope without creating a selection for the oracle.
		tx := f.accountTx(f.ctx, f.adminDB)
		var count int
		f.scan(tx, `SELECT count(DISTINCT match.logical_path_id) FROM app.workout_segment_matches match
			JOIN app.workouts workout ON workout.account_id=match.account_id AND workout.id=match.workout_id
			JOIN app.workout_types t ON t.account_id=workout.account_id AND t.id=workout.workout_type_id
			JOIN app.workout_routes route ON route.account_id=workout.account_id AND route.workout_id=workout.id
			JOIN app.workout_coverage_states state ON state.account_id=workout.account_id AND state.workout_id=workout.id
			WHERE workout.account_id=$1 AND workout.local_start_date BETWEEN $2::date AND $3::date
			AND t.type_key=ANY($4::text[]) AND workout.deletion_requested_at IS NULL AND route.route IS NOT NULL AND state.applied_route_input_revision IS NOT NULL`, []any{f.accountID, profile.start, profile.end, input.typeKeys(true)}, &count)
		var workoutCount int
		f.scan(tx, `SELECT count(*) FROM app.workouts w JOIN app.workout_types t ON t.account_id=w.account_id AND t.id=w.workout_type_id
			WHERE w.account_id=$1 AND w.local_start_date BETWEEN $2::date AND $3::date AND w.deletion_requested_at IS NULL AND t.type_key=ANY($4::text[])`, []any{f.accountID, profile.start, profile.end, input.typeKeys(false)}, &workoutCount)
		var routedCount int
		f.scan(tx, `SELECT count(*) FROM app.workouts w JOIN app.workout_routes r ON r.account_id=w.account_id AND r.workout_id=w.id
			JOIN app.workout_types t ON t.account_id=w.account_id AND t.id=w.workout_type_id
			WHERE w.account_id=$1 AND w.local_start_date BETWEEN $2::date AND $3::date AND w.deletion_requested_at IS NULL AND r.route IS NOT NULL AND t.type_key=ANY($4::text[])`, []any{f.accountID, profile.start, profile.end, input.typeKeys(true)}, &routedCount)
		rows, err := tx.Query(f.ctx, `SELECT t.type_key,t.provider_label,count(*),count(*) FILTER(WHERE r.route IS NOT NULL)
			FROM app.workouts w JOIN app.workout_types t ON t.account_id=w.account_id AND t.id=w.workout_type_id
			LEFT JOIN app.workout_routes r ON r.account_id=w.account_id AND r.workout_id=w.id
			WHERE w.account_id=$1 AND w.local_start_date BETWEEN $2::date AND $3::date AND w.deletion_requested_at IS NULL GROUP BY t.type_key,t.provider_label`, f.accountID, profile.start, profile.end)
		if err != nil {
			t.Fatal(err)
		}
		typeCounts[profile.name] = map[string]typeTotals{}
		for rows.Next() {
			var key, name string
			var totals typeTotals
			if err := rows.Scan(&key, &name, &totals.count, &totals.routed); err != nil {
				t.Fatal(err)
			}
			if expected, known := typeLabels[key]; !known || expected != name || key != healthautoexport.NormalizedTypeKey(name) {
				t.Fatal("read SQL oracle contains an unexpected canonical type key/provider label")
			}
			typeCounts[profile.name][key] = totals
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		f.commit(tx)
		if count < 1 {
			t.Fatalf("%s fixture has no applied paths; cannot skip detail/list acceptance", profile.name)
		}
		pathCounts[profile.name] = count
		workoutCounts[profile.name] = workoutCount
		routedCounts[profile.name] = routedCount
		query := "?startDate=" + profile.start + "&endDate=" + profile.end
		measure(profile.name+"_summary", "GET", "/api/summary"+query, "", 200)
		measure(profile.name+"_workouts_page1", "GET", "/api/workouts"+query+"&page=1&pageSize=25", "", 200)
		measure(profile.name+"_workouts_page2", "GET", "/api/workouts"+query+"&page=2&pageSize=25", "", 200)
		body := fmt.Sprintf(`{"startDate":%q,"endDate":%q}`, profile.start, profile.end)
		data := measure(profile.name+"_selection", "POST", "/api/map-selections", body, 201)
		var selection struct {
			ID              string `json:"id"`
			Generation      int64  `json:"dataGeneration"`
			TileURL         string `json:"tileUrlTemplate"`
			CoverageTileURL string `json:"coverageTileUrlTemplate"`
		}
		if err := json.Unmarshal(data, &selection); err != nil {
			t.Fatal(err)
		}
		if selection.ID == "" {
			t.Fatalf("selection shape: %s", data)
		}
		prefix := "/api/map-selections/" + selection.ID
		generation := fmt.Sprintf("?generation=%d", selection.Generation)
		original := selection
		resolvePath = func(path string) string {
			path = strings.ReplaceAll(path, original.ID, selection.ID)
			path = strings.ReplaceAll(path, fmt.Sprintf("?generation=%d", original.Generation), fmt.Sprintf("?generation=%d", selection.Generation))
			path = strings.ReplaceAll(path, fmt.Sprintf("-tiles/%d/", original.Generation), fmt.Sprintf("-tiles/%d/", selection.Generation))
			return path
		}
		refreshSelection = func() {
			status, data, _ := request("POST", "/api/map-selections", body)
			if status != 201 {
				t.Fatalf("selection refresh status=%d", status)
			}
			if err := json.Unmarshal(data, &selection); err != nil {
				t.Fatal(err)
			}
		}
		data = measure(profile.name+"_paths_page1", "GET", prefix+"/coverage/paths"+generation+"&pageSize=25", "", 200)
		measure(profile.name+"_paths_page2", "GET", prefix+"/coverage/paths"+generation+"&page=2&pageSize=25", "", 200)
		var paths struct {
			Items []struct {
				ID       string `json:"id"`
				EntityID string `json:"entityId"`
			} `json:"items"`
		}
		if err := json.Unmarshal(data, &paths); err != nil {
			t.Fatal(err)
		}
		if len(paths.Items) > 0 {
			id := paths.Items[0].ID
			if id == "" {
				id = paths.Items[0].EntityID
			}
			if id == "" {
				t.Fatal("path detail identifier missing")
			}
			measure(profile.name+"_path_detail", "GET", prefix+"/coverage/path/"+id+generation, "", 200)
		} else {
			t.Fatalf("%s path detail cannot be skipped", profile.name)
		}
		focus := acceptanceEligibleWorkout(f, input, f.accountID, profile.start, profile.end)
		measure(profile.name+"_focus", "GET", prefix+"/coverage-focus/"+focus.String()+generation, "", 200)
		for _, kind := range []string{"route", "coverage"} {
			path := fmt.Sprintf("%s/%s-tiles/%d/14/8192/8191.pbf", prefix, kind, original.Generation)
			measure(profile.name+"_"+kind+"_tile", "GET", path, "", 200)
		}
		refreshSelection = nil
		resolvePath = func(path string) string { return path }
	}
	if len(results) != 20 || len(tileMinimumBytes) != 4 {
		t.Fatal("required endpoint or non-empty tile measurements missing")
	}
	artifact := map[string]any{"schema_version": 3, "namespace": "workouts-acceptance", "namespace_uid": namespaceUID,
		"provenance":     map[string]any{"kind": "captured_fenced_run", "namespace_uid": namespaceUID},
		"content_checks": map[string]any{"status": "PASS", "path_totals_sql_oracle": pathCounts, "workout_totals_sql_oracle": workoutCounts, "routed_workout_totals_sql_oracle": routedCounts, "tile_min_bytes": tileMinimumBytes},
		"started_at":     startedAt, "recorded_at": time.Now().UTC(), "active_children_start": activeStart, "active_children_end": activeChildren(), "scope": "public synthetic fixture only; not archive-scale production evidence", "phase": os.Getenv("COVERAGE_ACCEPTANCE_PHASE"), "cache": "first request cold-ish per endpoint; database and OS caches not flushed; remaining 29 warm samples; localhost kubectl forward included; stale-selection 404 refresh cost included in successful read latency", "targets_ms": map[string]int{"api_p95": 500, "summary": 2000, "initial_map_render": 3000}, "render_measurement": "deferred to main visual acceptance; backend requests are not render time", "timings": results}
	for k, v := range input.metadata() {
		artifact[k] = v
	}
	data, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := os.Getenv("COVERAGE_ACCEPTANCE_RESULTS")
	if path != input.artifact(os.Getenv("COVERAGE_ACCEPTANCE_PHASE")) {
		t.Fatal("COVERAGE_ACCEPTANCE_RESULTS must be the disposable phase artifact path")
	}
	if err := os.WriteFile(path, append(data, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	for name, value := range results {
		t.Logf("%s first=%.1fms p95=%.1fms max=%.1fms", name, value.FirstMS, value.P95MS, value.MaxMS)
		if value.P95MS > 500 {
			t.Errorf("TARGET FAIL: %s logical read p95=%.1fms exceeds 500ms; measurement artifact retained", name, value.P95MS)
		}
	}
}
