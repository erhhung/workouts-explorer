package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/erhhung/workouts-explorer/internal/coverage/readerplacement"
	"github.com/erhhung/workouts-explorer/internal/coverage/routepipeline"
	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/erhhung/workouts-explorer/worker"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Reuses only the explicitly provisioned synthetic runtime-readiness database,
// after that suite has applied the current schema. No application URL fallback.
func TestCoverageDiagnosticLimitParityPostgreSQL(t *testing.T) {
	dsn := os.Getenv("PROJECT_RUNTIME_CAPABILITY_TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("PROJECT_RUNTIME_CAPABILITY_TEST_DISPOSABLE") != "1" {
		t.Skip("explicit disposable runtime database is required")
	}
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(config.ConnConfig.Database, "workouts_runtime_capability_test_") ||
		(config.ConnConfig.Host != "127.0.0.1" && config.ConnConfig.Host != "localhost" && config.ConnConfig.Host != "::1") {
		t.Fatal("diagnostic parity requires a named loopback scratch database")
	}
	for _, fallback := range config.ConnConfig.Fallbacks {
		if fallback.Host != config.ConnConfig.Host || fallback.Port != config.ConnConfig.Port {
			t.Fatal("diagnostic parity prohibits remote/fallback endpoints")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	config.MaxConns = 2
	config.ConnConfig.RuntimeParams["statement_timeout"] = "5s"
	admin, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	config = config.Copy()
	config.ConnConfig.RuntimeParams["role"] = "workouts_api"
	apiDB, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer apiDB.Close()
	if !database.Ready(ctx, apiDB) {
		t.Fatal("diagnostic parity requires the current migrated schema and role contract")
	}
	principal, account := insertSourceTestUser(t, admin)
	_, foreignAccount := insertSourceTestUser(t, admin)
	bearer := insertTestSession(t, apiDB, principal, "bearer", "")
	server := integrationServer(t, apiDB, &recordingSender{})
	snapshotOpened := false
	server.diagnostics = &coverageDiagnosticService{enabled: true, timeout: 120 * time.Second, minimumTraversalMeters: 5,
		ownerID: "synthetic-parity-api", roadGeometry: coverage.DefaultRoadGeometryRules(),
		gate: make(chan struct{}, 1), begin: func(context.Context) (coverageDiagnosticSnapshot, error) {
			snapshotOpened = true
			return &diagnosticSnapshotFixture{}, nil
		}}
	source, file, activity, job := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	seed, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer seed.Rollback(ctx)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := seed.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Only synthetic administrator setup bypasses ingest triggers. Runtime calls
	// below use the actual API role, forced RLS and SECURITY DEFINER persistence.
	exec(`SET LOCAL session_replication_role='replica'`)
	exec(`INSERT INTO app.sources(id,account_id,display_name,canonical_display_name,type,status,config_envelope)
		VALUES($1,$2,'Synthetic parity','synthetic parity','health-auto-export-local','connected',$3)`, source, account, []byte(`{"synthetic":true}`))
	exec(`INSERT INTO app.jobs(id,account_id,kind,priority) VALUES($1,$2,'manual_ingest',80)`, job, account)
	exec(`INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes,state,checksum_sha256,processing_started_at,processed_at)
		VALUES($1,$2,$3,$4,'HealthAutoExport-2026-01-01.json',1,'succeeded',decode(repeat('01',32),'hex'),now(),now())`, file, account, source, job)
	exec(`INSERT INTO app.workout_types(id,account_id,type_key,provider_label) VALUES($1,$2,'walking','Walking')`, activity, account)
	workouts := make(map[int]uuid.UUID)
	for _, count := range []int{25001, routepipeline.OriginalPointLimit, routepipeline.OriginalPointLimit + 1} {
		id := uuid.New()
		workouts[count] = id
		exec(`INSERT INTO app.workouts(id,account_id,source_id,source_file_id,workout_type_id,provider_id,content_sha256,provider_label,started_at,ended_at,provider_duration)
			VALUES($1,$2,$3,$4,$5,$6,decode(repeat('01',32),'hex'),'Walking','2026-01-01Z','2026-01-01Z',0)`, id, account, source, file, activity, id.String())
		exec(`INSERT INTO app.workout_routes(account_id,workout_id,point_count,minimum_longitude,maximum_longitude,minimum_latitude,maximum_latitude,has_complete_altitude)
			VALUES($1,$2,$3,0,0,0,0,false)`, account, id, count)
		exec(`INSERT INTO app.workout_coverage_states(account_id,workout_id,route_input_revision,route_input_sha256,readiness_state)
			VALUES($1,$2,1,decode(repeat('01',32),'hex'),'unresolved')`, account, id)
		if count <= routepipeline.OriginalPointLimit {
			exec(`INSERT INTO app.workout_route_points(account_id,workout_id,sequence,recorded_at,latitude,longitude)
				SELECT $1,$2,n,'2026-01-01Z'::timestamptz+n*interval '1 second',0,0 FROM generate_series(0,$3::integer-1) n`, account, id, count)
		}
	}
	if err := seed.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	for count, id := range workouts {
		t.Run(fmt.Sprintf("original points %d", count), func(t *testing.T) {
			snapshotOpened = false
			workout, status := server.loadDiagnosticWorkout(ctx, account, id)
			request := httptest.NewRequest(http.MethodPost, "/api/workouts/"+compactUUID(id)+"/coverage-diagnostic-runs", strings.NewReader(`{}`))
			request.Header.Set("Authorization", "Bearer "+bearer)
			response := httptest.NewRecorder()
			server.CreateCoverageDiagnosticRun(response, request, generated.WorkoutID(compactUUID(id)), generated.CreateCoverageDiagnosticRunParams{})
			var slots int
			if err := admin.QueryRow(ctx, `SELECT count(*) FROM app.matcher_slots WHERE account_id=$1`, account).Scan(&slots); err != nil {
				t.Fatal(err)
			}
			if count <= routepipeline.OriginalPointLimit {
				if status != 0 || len(workout.points) != count || response.Code != http.StatusCreated || !snapshotOpened || slots != 0 {
					t.Fatalf("raw route count=%d status=%d HTTP=%d snapshot=%t slots=%d body=%s", len(workout.points), status, response.Code, snapshotOpened, slots, response.Body.String())
				}
				validateRecordedResponse(t, http.MethodPost, request.URL.Path, response)
				var run generated.CoverageDiagnosticRun
				if err := json.Unmarshal(response.Body.Bytes(), &run); err != nil || run.Counts.OriginalPoints != count {
					t.Fatalf("accepted route counts=%+v err=%v", run.Counts, err)
				}
				if count == 50000 && run.Counts.SampledPoints <= 10000 {
					t.Fatalf("large route did not exercise the widened sampled bound: %+v", run.Counts)
				}
				return
			}
			// No point rows were seeded for this route. Reject by summary before
			// route loading, matcher admission or opening an OSM snapshot.
			if status != http.StatusRequestEntityTooLarge || response.Code != status || snapshotOpened || slots != 0 {
				t.Fatalf("guard status=%d HTTP=%d snapshot=%t slots=%d body=%s", status, response.Code, snapshotOpened, slots, response.Body.String())
			}
		})
	}
	workout, status := server.loadDiagnosticWorkout(ctx, account, workouts[50000])
	if status != 0 {
		t.Fatal(status)
	}
	var persistedRun uuid.UUID
	for _, test := range []struct {
		original, sampled int
		valid             bool
	}{
		{25001, 10001, true}, {50000, 12000, true}, {50001, 12000, false}, {50000, 12001, false}, {-1, 0, false}, {1, -1, false},
	} {
		t.Run(fmt.Sprintf("persist %d/%d", test.original, test.sampled), func(t *testing.T) {
			result := diagnosticResult{runID: uuid.New(), workout: workout, counts: generated.CoverageDiagnosticCounts{OriginalPoints: test.original, SampledPoints: test.sampled}}
			ok, err := server.persistCoverageDiagnostic(ctx, account, &result)
			if test.valid {
				if err != nil || !ok {
					t.Fatalf("valid count persistence=%t %v", ok, err)
				}
				persistedRun = result.runID
				var original, sampled int
				if err := admin.QueryRow(ctx, `SELECT original_point_count,sampled_point_count FROM app.coverage_diagnostic_runs WHERE id=$1`, result.runID).Scan(&original, &sampled); err != nil || original != test.original || sampled != test.sampled {
					t.Fatalf("stored counts=%d/%d err=%v", original, sampled, err)
				}
			} else {
				var pgErr *pgconn.PgError
				if ok || !errors.As(err, &pgErr) || pgErr.Code != "23514" {
					t.Fatalf("invalid counts accepted: %t %v", ok, err)
				}
			}
		})
	}
	foreign, err := server.accountTransaction(ctx, foreignAccount)
	if err != nil {
		t.Fatal(err)
	}
	defer foreign.Rollback(ctx)
	var visible int
	if err := foreign.QueryRow(ctx, `SELECT count(*) FROM app.coverage_diagnostic_runs WHERE id=$1`, persistedRun).Scan(&visible); err != nil || visible != 0 {
		t.Fatalf("cross-account diagnostic visible=%d err=%v", visible, err)
	}
	if _, status := server.loadDiagnosticWorkout(ctx, foreignAccount, workout.id); status != http.StatusNotFound {
		t.Fatalf("cross-account workout status=%d", status)
	}
	foreign.Rollback(ctx)
	result := diagnosticResult{runID: uuid.New(), workout: workout}
	if ok, err := server.persistCoverageDiagnostic(ctx, foreignAccount, &result); err != nil || ok {
		t.Fatalf("cross-account route persisted=%t err=%v", ok, err)
	}
	result.counts.Portions = 1
	result.evidence = []diagnosticEvidence{{id: uuid.New(), class: "matched", clipped: osm.MatcherClippedPortion{
		SegmentID: uuid.New(), LogicalPathID: uuid.New(), RegionID: "fixture:region", GenerationID: 1,
		DerivationVersion: 1, SourceWayID: 1, SourceWayVersion: 1, Direction: "forward",
		SourceFromFraction: 0, SourceToFraction: 1, LengthMeters: 10,
		GeoJSON: []byte(`{"type":"LineString","coordinates":[[0,0],[0.001,0]]}`),
	}}}
	if ok, err := server.persistCoverageDiagnostic(ctx, account, &result); ok || err == nil {
		t.Fatal("evidence without matching generation provenance persisted")
	} else {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23503" {
			t.Fatalf("provenance constraint error=%v", err)
		}
	}
	workout.revision++
	result = diagnosticResult{runID: uuid.New(), workout: workout}
	if ok, err := server.persistCoverageDiagnostic(ctx, account, &result); err != nil || ok {
		t.Fatalf("stale route revision persisted=%t err=%v", ok, err)
	}
	t.Run("actual worker RunOnce Prepare persist parity", func(t *testing.T) {
		osmURL := os.Getenv("PROJECT_RUNTIME_CAPABILITY_TEST_OSM_DATABASE_URL")
		if osmURL == "" {
			t.Skip("explicit paired disposable OSM reader URL is required")
		}
		osmConfig, err := pgxpool.ParseConfig(osmURL)
		appConfig := admin.Config()
		if err != nil || osmConfig.ConnConfig.Database != appConfig.ConnConfig.Database+"_osm" ||
			osmConfig.ConnConfig.Host != appConfig.ConnConfig.Host || osmConfig.ConnConfig.Port != appConfig.ConnConfig.Port ||
			osmConfig.ConnConfig.User != "workouts_coverage_worker" {
			t.Fatal("worker parity requires a paired loopback scratch OSM database and matcher role")
		}
		for _, fallback := range osmConfig.ConnConfig.Fallbacks {
			if fallback.Host != osmConfig.ConnConfig.Host || fallback.Port != osmConfig.ConnConfig.Port {
				t.Fatal("worker parity prohibits remote/fallback OSM endpoints")
			}
		}
		open := func(role, name string) *pgxpool.Pool {
			t.Helper()
			cfg := appConfig.Copy()
			cfg.MaxConns = 3
			cfg.ConnConfig.Database = name
			if role != "" {
				cfg.ConnConfig.RuntimeParams["role"] = role
			}
			pool, err := pgxpool.NewWithConfig(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(pool.Close)
			return pool
		}
		ingestDB := open("workouts_worker", appConfig.ConnConfig.Database)
		coverageDB := open("workouts_coverage_worker", appConfig.ConnConfig.Database)
		osmAdmin := open("", osmConfig.ConnConfig.Database)
		if !database.Ready(ctx, ingestDB) || !database.Ready(ctx, coverageDB) {
			t.Fatal("worker fixture roles are not ready on the current coverage baseline")
		}
		var active int
		if err := admin.QueryRow(ctx, `SELECT (SELECT count(*) FROM app.jobs WHERE kind='coverage_update_route' AND status IN ('queued','running'))
			+(SELECT count(*) FROM app.matcher_slots)`).Scan(&active); err != nil || active != 0 {
			t.Fatalf("refusing to consume unrelated scratch coverage work or admission: active=%d err=%v", active, err)
		}
		// Reuse the boundary fixture's ancestry and route. Only the OSM road is new.
		tx := integrationAccountTransaction(t, osmAdmin, account)
		defer tx.Rollback(ctx)
		exec := func(query string, args ...any) {
			t.Helper()
			if _, err := tx.Exec(ctx, query, args...); err != nil {
				t.Fatal(err)
			}
		}
		scan := func(query string, args []any, dest ...any) {
			t.Helper()
			if err := tx.QueryRow(ctx, query, args...).Scan(dest...); err != nil {
				t.Fatal(err)
			}
		}
		commit := func() {
			t.Helper()
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
		}
		region, segment, path := "fixture:production-parity", uuid.New(), uuid.New()
		exec(`INSERT INTO osm_catalog.regions(id,provider,provider_region_id,display_name,catalog_url,source_url,boundary)
			VALUES($1,'fixture','production-parity','Synthetic parity','https://example.test/catalog','https://example.test/parity.pbf',
			ST_Multi(ST_GeomFromText('POLYGON((-123 36,-121 36,-121 38,-123 38,-123 36))',4326)))`, region)
		var generation int64
		scan(`SELECT nextval(pg_get_serial_sequence('osm_catalog.generations','id'))`, nil, &generation)
		build := fmt.Sprintf("osm_build_%d", generation)
		exec(`INSERT INTO osm_catalog.generations(id,region_id,state,schema_name,source_url,source_sha256,importer_version,derivation_version,promoted_at)
			OVERRIDING SYSTEM VALUE VALUES($1,$2,'active',$3,'https://example.test/parity.pbf',repeat('03',32),1,1,clock_timestamp())`, generation, region, build)
		exec(`CREATE SCHEMA ` + pgx.Identifier{build}.Sanitize())
		for _, table := range []string{"ways", "localities", "path_segments"} {
			exec(fmt.Sprintf(`CREATE TABLE osm_canonical.%s PARTITION OF osm_canonical.%s FOR VALUES IN ('fixture:production-parity')`, table+"_parity", table))
		}
		exec(`INSERT INTO osm_catalog.region_storage(region_id,generation_id,ways_relation,localities_relation,path_segments_relation,build_schema)
			VALUES($1,$2,'ways_parity','localities_parity','path_segments_parity',$3)`, region, generation, build)
		exec(`INSERT INTO osm_canonical.ways(region_id,generation_id,way_id,version,tags,node_ids,geom)
			VALUES($1,$2,10,7,'{}','[1,2]',ST_GeomFromText('LINESTRING(-122 37,-121.998 37)',4326))`, region, generation)
		exec(`INSERT INTO osm_canonical.path_segments(region_id,generation_id,segment_id,source_way_id,source_way_version,derivation_version,
			start_node_index,end_node_index,boundary_piece,start_graph_node_id,end_graph_node_id,name,normalized_name,highway,broad_class,tags,
			motor_forward_allowed,motor_reverse_allowed,geom,logical_path_id,length_m)
			SELECT $1,$2,$3,10,7,1,0,1,1,$4,$5,'Parity Road','parity road','residential','road','{}',true,true,geom,$6,ST_Length(geom::geography)
			FROM (SELECT ST_GeomFromText('LINESTRING(-122 37,-121.998 37)',4326) geom) road`, region, generation, segment, uuid.New(), uuid.New(), path)
		commit()
		ingest, lease, parent, child := uuid.New(), uuid.New(), uuid.New(), uuid.New()
		id := workouts[25001]
		tx = integrationAccountTransaction(t, admin, account)
		exec(`INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority,parameters)
			VALUES($1,$2,$3,'manual_ingest_source',80,jsonb_build_object('sourceId',$4::text,'generation',1,'mode','incremental'))`, ingest, job, account, compactUUID(source))
		exec(`INSERT INTO app.job_config_snapshots(job_id,account_id,source_id,source_generation,config_envelope)
			VALUES($1,$2,$3,1,$4)`, ingest, account, source, []byte(`{"synthetic":true}`))
		commit()
		tx = integrationAccountTransaction(t, ingestDB, account)
		var ok bool
		scan(`SELECT app.claim_job($1,'parity-ingest',$2,interval '2 minutes')`, []any{ingest, lease}, &ok)
		if !ok {
			t.Fatal("authoritative ingest claim rejected")
		}
		var fenced uuid.UUID
		scan(`SELECT source_id FROM app.fence_ingest_job($1,'parity-ingest',$2,$3)`, []any{ingest, lease, database.SupportedSchemaVersion}, &fenced)
		if fenced != source {
			t.Fatal("ingest fence returned a different source")
		}
		// Domain writes must reference this claimed source child's file, not the
		// boundary fixture's deliberately abbreviated parent-job ancestry.
		importFile := uuid.New()
		exec(`INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes,state,checksum_sha256,processing_started_at,processed_at)
			VALUES($1,$2,$3,$4,'parity-route.json',1,'succeeded',decode(repeat('01',32),'hex'),now(),now())`, importFile, account, source, ingest)
		exec(`UPDATE app.workouts SET source_file_id=$3,ended_at=started_at+interval '270 seconds',provider_duration=270,local_start_date='2026-01-01'
			WHERE account_id=$1 AND id=$2`, account, id, importFile)
		exec(`DELETE FROM app.workout_route_points WHERE account_id=$1 AND workout_id=$2`, account, id)
		// Forward, reverse, forward over the same road: raw evidence must dissolve.
		exec(`INSERT INTO app.workout_route_points(account_id,workout_id,sequence,recorded_at,latitude,longitude,horizontal_accuracy)
			SELECT $1,$2,n,'2026-01-01Z'::timestamptz+n*interval '5 seconds',37,
			-121.9999+(CASE WHEN n<=18 THEN n WHEN n<=36 THEN 36-n ELSE n-36 END)*0.0001,3 FROM generate_series(0,54) n`, account, id)
		scan(`SELECT app.replace_workout_route_summary($1,55,-121.9999,37,-121.9981,37,NULL,NULL,NULL,false)`, []any{id}, &ok)
		if !ok {
			t.Fatal("fenced route summary rejected")
		}
		commit()
		var digest []byte
		if err := admin.QueryRow(ctx, `SELECT app.adopted_coverage_route_digest($1,$2)`, account, id).Scan(&digest); err != nil {
			t.Fatal(err)
		}
		tx = integrationAccountTransaction(t, ingestDB, account)
		scan(`SELECT source_id FROM app.fence_ingest_job($1,'parity-ingest',$2,$3)`, []any{ingest, lease, database.SupportedSchemaVersion}, &fenced)
		var revision int64
		scan(`SELECT route_input_revision,digest_changed FROM app.initialize_workout_coverage($1,$2,$3,$4,'parity-ingest',$5)`, []any{account, id, digest, ingest, lease}, &revision, &ok)
		generations := fmt.Sprintf(`[{"regionId":%q,"generation":%d}]`, region, generation)
		scan(`SELECT app.set_workout_coverage_readiness($1,$2,$3,'map_data_ready',NULL,$4,$5,'parity-ingest',$6)`, []any{account, id, revision, generations, ingest, lease}, &ok)
		if !ok {
			t.Fatal("fenced coverage readiness rejected")
		}
		targets := fmt.Sprintf(`[{"jobId":%q,"workoutId":%q,"routeRevision":%d,"generations":%s}]`, child, id, revision, generations)
		var queued uuid.UUID
		var count int
		scan(`SELECT job_id,route_count,reused FROM app.enqueue_coverage_update($1,$2,$3,$4,$5,$6,$7,$8,5,$9)`,
			[]any{account, parent, region, generation, revision, coverage.ExperimentalRulesV1, coverage.ExperimentalSamplingV1, coverage.ExperimentalPathPolicyV82, targets}, &queued, &count, &ok)
		if queued != parent || count != 1 || ok {
			t.Fatal("authoritative coverage enqueue did not create the single target child")
		}
		commit()
		osmReader, err := database.OpenOSMReader(ctx, osmURL, "parity-actual-worker-osm", 3, false)
		if err != nil {
			t.Fatal(err)
		}
		defer osmReader.Close()
		productionReader, err := readerplacement.New(osmReader, false, 2)
		if err != nil {
			t.Fatal(err)
		}
		defer productionReader.Close()
		var workerLog bytes.Buffer
		runner := worker.NewCoverageRunnerWithOptions(coverageDB, osmReader, slog.New(slog.NewTextHandler(&workerLog, nil)), worker.CoverageRunnerOptions{Reader: productionReader})
		if worked, err := runner.RunOnce(ctx); err != nil || !worked {
			t.Fatalf("actual CoverageRunner.RunOnce worked=%t err=%v", worked, err)
		}
		if err := admin.QueryRow(ctx, `SELECT child.status='succeeded' AND parent.status='succeeded' AND route.result_outcome='applied'
			FROM app.jobs child JOIN app.jobs parent ON parent.id=child.parent_job_id JOIN app.coverage_route_job_contexts route ON route.job_id=child.id WHERE child.id=$1`, child).Scan(&ok); err != nil || !ok {
			t.Fatalf("actual production child did not persist an applied result: %t %v; worker log: %s", ok, err, workerLog.String())
		}
		diagnosticReader, err := readerplacement.New(osmReader, false, 1)
		if err != nil {
			t.Fatal(err)
		}
		defer diagnosticReader.Close()
		server.diagnostics.reader, server.diagnostics.begin = diagnosticReader, nil
		request := httptest.NewRequest(http.MethodPost, "/api/workouts/"+compactUUID(id)+"/coverage-diagnostic-runs", strings.NewReader(`{}`)).WithContext(ctx)
		request.Header.Set("Authorization", "Bearer "+bearer)
		response := httptest.NewRecorder()
		server.CreateCoverageDiagnosticRun(response, request, generated.WorkoutID(compactUUID(id)), generated.CreateCoverageDiagnosticRunParams{})
		if response.Code != http.StatusCreated {
			t.Fatalf("actual OSM diagnostic HTTP=%d body=%s", response.Code, response.Body.String())
		}
		validateRecordedResponse(t, http.MethodPost, request.URL.Path, response)
		var diagnostic generated.CoverageDiagnosticRun
		if err := json.Unmarshal(response.Body.Bytes(), &diagnostic); err != nil {
			t.Fatal(err)
		}
		run, valid := parseCompactUUID(diagnostic.Id)
		if !valid || diagnostic.Counts.OriginalPoints != 55 || diagnostic.Counts.Portions <= diagnostic.Counts.UniqueSegments || diagnostic.Counts.MatchedPoints == 0 {
			t.Fatalf("expected nonempty repeated-segment diagnostic: %+v", diagnostic.Counts)
		}
		// Compare geometric sets, not summed per-traversal lengths or raw counts.
		if err := admin.QueryRow(ctx, `WITH diagnostic AS (
			SELECT physical_segment_id,region_id,generation_id,logical_path_id,derivation_version,source_way_id,source_way_version,
			ST_UnaryUnion(ST_Collect(geom)) geom FROM app.coverage_diagnostic_evidence WHERE account_id=$1 AND run_id=$2
			GROUP BY physical_segment_id,region_id,generation_id,logical_path_id,derivation_version,source_way_id,source_way_version), production AS (
			SELECT m.*,s.derivation_version,s.source_way_id,s.source_way_version FROM app.workout_segment_matches m
			JOIN app.path_segments s USING(account_id,region_id,generation_id,physical_segment_id,logical_path_id)
			WHERE m.account_id=$1 AND m.workout_id=$3)
			SELECT count(*),COALESCE(bool_and(d.physical_segment_id IS NOT NULL AND p.physical_segment_id IS NOT NULL
			AND d.region_id=p.region_id AND d.generation_id=p.generation_id AND d.logical_path_id=p.logical_path_id
			AND d.derivation_version=p.derivation_version AND d.source_way_id=p.source_way_id AND d.source_way_version=p.source_way_version
			AND ST_Equals(d.geom,p.geom) AND abs(ST_Length(d.geom::geography)-p.covered_meters)<0.01),false)
			FROM diagnostic d FULL JOIN production p USING(physical_segment_id)`, account, run, id).Scan(&count, &ok); err != nil || !ok || count != 1 {
			t.Fatalf("dissolved diagnostic vs actual persisted production matches: rows=%d equal=%t err=%v", count, ok, err)
		}
		if err := admin.QueryRow(ctx, `SELECT s.processing_state='current' AND s.applied_route_input_revision=d.route_input_revision
			AND s.applied_route_input_sha256=d.route_input_sha256 AND s.applied_rules_version=d.rules_version
			AND s.applied_sampling_version=d.sampling_version AND s.applied_path_policy_version=d.path_policy_version
			AND s.applied_generations=$4::jsonb AND d.minimum_traversal_meters=c.minimum_traversal_meters AND d.movement_mode='foot'
			FROM app.workout_coverage_states s JOIN app.coverage_diagnostic_runs d USING(account_id,workout_id)
			JOIN app.coverage_job_contexts c ON c.job_id=$3 WHERE s.account_id=$1 AND d.id=$2`, account, run, parent, generations).Scan(&ok); err != nil || !ok {
			t.Fatalf("production/diagnostic input, rules or generation provenance differ: %t %v", ok, err)
		}
		t.Logf("actual CoverageRunner.RunOnce applied %d persisted segment; diagnostic %d portions dissolve to the same geometry, meters and provenance", count, diagnostic.Counts.Portions)
	})
}
