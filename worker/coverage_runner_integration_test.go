package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// Shared by the RunOnce integration tests and coverage acceptance benchmarks.
// These tests require explicit, disposable, separately provisioned app/OSM DBs.
type coverageIntegrationFixture struct {
	t                                                          testing.TB
	ctx                                                        context.Context
	adminDB, apiDB, workerDB, coverageDB                       *pgxpool.Pool
	osmAdminDB, osmDB                                          *pgxpool.Pool
	accountID, sourceID, fileID, typeID, ingestID, ingestLease uuid.UUID
	regionID                                                   string
	generation                                                 int64
	segments, paths                                            []uuid.UUID
}

type coverageIntegrationJob struct {
	parentID, childID uuid.UUID
}

func newCoverageIntegrationFixture(t testing.TB) *coverageIntegrationFixture {
	t.Helper()
	if os.Getenv("COVERAGE_ACCEPTANCE_DISPOSABLE") != "true" {
		t.Skip("requires explicit COVERAGE_ACCEPTANCE_DISPOSABLE=true and disposable app/OSM databases")
	}
	names := []string{"MIGRATION_DATABASE_URL", "API_DATABASE_URL", "WORKER_DATABASE_URL", "COVERAGE_WORKER_DATABASE_URL", "OSM_MIGRATION_DATABASE_URL", "COVERAGE_WORKER_OSM_DATABASE_URL"}
	configs := make([]*pgxpool.Config, len(names))
	for i, name := range names {
		value := os.Getenv(name)
		if value == "" {
			t.Fatalf("%s is required; no database URL fallbacks are permitted", name)
		}
		database := ""
		if i > 0 && i < 4 {
			database = configs[0].ConnConfig.Database
		} else if i >= 4 {
			database = configs[0].ConnConfig.Database + "_osm"
		}
		config, err := parseCoverageAcceptanceConfig(value, database)
		if err != nil {
			t.Fatalf("refusing non-disposable %s: %v (URL omitted)", name, err)
		}
		config.MaxConns = 8
		config.ConnConfig.ConnectTimeout = 5 * time.Second
		config.ConnConfig.RuntimeParams["application_name"] = "coverage-integration-" + uuid.NewString()
		configs[i] = config
	}
	sameDB := func(a, b *pgxpool.Config) bool {
		return a.ConnConfig.Host == b.ConnConfig.Host && a.ConnConfig.Port == b.ConnConfig.Port && a.ConnConfig.Database == b.ConnConfig.Database
	}
	for i := 1; i < 4; i++ {
		if !sameDB(configs[0], configs[i]) {
			t.Fatalf("%s must address the same app database as MIGRATION_DATABASE_URL", names[i])
		}
	}
	if !sameDB(configs[4], configs[5]) {
		t.Fatal("OSM URLs must address the same disposable OSM database")
	}
	if err := validateCoverageAcceptancePair(configs[0], configs[4]); err != nil {
		t.Fatal(err)
	}
	// Production URLs are optional guards, never sources for test connections.
	for _, name := range []string{"DATABASE_URL", "OSM_DATABASE_URL", "PRODUCTION_DATABASE_URL", "PRODUCTION_OSM_DATABASE_URL"} {
		if value := os.Getenv(name); value != "" {
			production, err := pgxpool.ParseConfig(value)
			if err != nil {
				t.Fatalf("invalid optional safety guard %s (URL omitted)", name)
			}
			if sameDB(production, configs[0]) || sameDB(production, configs[4]) {
				t.Fatalf("disposable database overlaps optional production guard %s", name)
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	t.Cleanup(cancel)
	f := &coverageIntegrationFixture{t: t, ctx: ctx, accountID: uuid.New(), sourceID: uuid.New(), fileID: uuid.New(), typeID: uuid.New(), ingestID: uuid.New(), ingestLease: uuid.New()}
	pools := []**pgxpool.Pool{&f.adminDB, &f.apiDB, &f.workerDB, &f.coverageDB, &f.osmAdminDB, &f.osmDB}
	for i, destination := range pools {
		pool, err := pgxpool.NewWithConfig(ctx, configs[i])
		if err != nil {
			t.Fatalf("open %s pool: %v", names[i], err)
		}
		*destination = pool
		t.Cleanup(pool.Close)
		var dbName, role string
		if err := pool.QueryRow(ctx, `SELECT current_database(),current_user`).Scan(&dbName, &role); err != nil {
			t.Fatalf("connect %s: %v", names[i], err)
		}
		if dbName != configs[i].ConnConfig.Database {
			t.Fatalf("%s connected to an unexpected database", names[i])
		}
		want := map[int]string{1: "workouts_api", 2: "workouts_worker", 3: "workouts_coverage_worker"}[i]
		if want != "" && role != want {
			t.Fatalf("%s role=%q, want %q", names[i], role, want)
		}
		if i == 5 {
			if err := validateCoverageAcceptanceOSMReader(ctx, pool); err != nil {
				t.Fatalf("COVERAGE_WORKER_OSM_DATABASE_URL must use a read-only matcher role: %v", err)
			}
		}
	}
	// Serialize this suite and the benchmark across processes; never consume
	// another fixture's global claim queue or matcher slot budget.
	serial, err := f.adminDB.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(serial.Release)
	if _, err := serial.Exec(ctx, `SELECT pg_advisory_lock(713902,5)`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if _, err := serial.Exec(cleanup, `SELECT pg_advisory_unlock(713902,5)`); err != nil {
			t.Error(err)
		}
	})
	var active int
	if err := f.adminDB.QueryRow(ctx, `SELECT (SELECT count(*) FROM app.jobs WHERE kind='coverage_update_route' AND status IN ('queued','running'))
		+(SELECT count(*) FROM app.matcher_slots)`).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatal("scratch app DB already has active coverage jobs or matcher slots; refusing to consume unrelated work/budget")
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		tx := f.accountTx(cleanup, f.adminDB)
		defer coverageIntegrationRollback(tx)
		var fenced uuid.UUID
		if err := tx.QueryRow(cleanup, `SELECT source_id FROM app.fence_ingest_job($1,'coverage-fixture-ingest',$2)`, f.ingestID, f.ingestLease).Scan(&fenced); err != nil {
			t.Error("fixture cleanup ingest fence:", err)
			return
		}
		if fenced != f.sourceID {
			t.Error("fixture cleanup fence returned wrong source")
			return
		}
		// Cascading account deletion removes the lookup rows before domain
		// triggers can resolve their fence. Delete fenced domain rows first.
		for _, query := range []string{`DELETE FROM app.workouts WHERE account_id=$1`, `DELETE FROM app.source_files WHERE account_id=$1`, `DELETE FROM app.workout_types WHERE account_id=$1`} {
			if _, err := tx.Exec(cleanup, query, f.accountID); err != nil {
				t.Error("fixture fenced domain cleanup:", err)
				return
			}
		}
		// Commit while the ingest snapshot still exists: its deferred fence
		// validation must succeed before fixture jobs/sources are removed.
		if err := tx.Commit(cleanup); err != nil {
			t.Error("fixture fenced cleanup commit:", err)
			return
		}
		tx = f.accountTx(cleanup, f.adminDB)
		defer coverageIntegrationRollback(tx)
		if _, err := tx.Exec(cleanup, `DELETE FROM app.jobs WHERE account_id=$1`, f.accountID); err != nil {
			t.Error("fixture job cleanup:", err)
			return
		}
		if _, err := tx.Exec(cleanup, `DELETE FROM app.accounts WHERE id=$1`, f.accountID); err != nil {
			t.Error("fixture account cleanup:", err)
			return
		}
		if err := tx.Commit(cleanup); err != nil {
			t.Error(err)
		}
	})
	tx := f.accountTx(ctx, f.adminDB)
	defer coverageIntegrationRollback(tx)
	f.exec(tx, `INSERT INTO app.accounts(id) VALUES($1)`, f.accountID)
	f.exec(tx, `INSERT INTO app.sources(id,account_id,display_name,canonical_display_name,type,config_envelope,status)
		VALUES($1,$2,$3,$3,'health-auto-export-local',$4,'connected')`, f.sourceID, f.accountID, "coverage-"+f.sourceID.String(), []byte("fixture"))
	parentID := uuid.New()
	f.exec(tx, `INSERT INTO app.jobs(id,account_id,kind,priority) VALUES($1,$2,'manual_ingest',80)`, parentID, f.accountID)
	parameters := fmt.Sprintf(`{"sourceId":"%s","generation":1,"mode":"incremental"}`, strings.ToUpper(strings.ReplaceAll(f.sourceID.String(), "-", "")))
	f.exec(tx, `INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority,parameters) VALUES($1,$2,$3,'manual_ingest_source',80,$4)`, f.ingestID, parentID, f.accountID, parameters)
	f.exec(tx, `INSERT INTO app.job_config_snapshots(job_id,account_id,source_id,source_generation,config_envelope) VALUES($1,$2,$3,1,$4)`, f.ingestID, f.accountID, f.sourceID, []byte("fixture"))
	f.exec(tx, `INSERT INTO app.job_source_contexts(job_id,account_id,source_id,source_generation,display_name,source_type) VALUES($1,$2,$3,1,'Coverage fixture','health-auto-export-local')`, f.ingestID, f.accountID, f.sourceID)
	f.commit(tx)
	tx = f.accountTx(ctx, f.workerDB)
	defer coverageIntegrationRollback(tx)
	var claimed bool
	f.scan(tx, `SELECT app.claim_job($1,'coverage-fixture-ingest',$2,interval '15 minutes')`, []any{f.ingestID, f.ingestLease}, &claimed)
	if !claimed {
		t.Fatal("fixture ingest claim rejected")
	}
	f.fence(tx)
	hash := sha256.Sum256([]byte(f.sourceID.String()))
	f.exec(tx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes,checksum_sha256,state,processing_started_at,processed_at)
		VALUES($1,$2,$3,$4,'coverage-fixture.json',1,$5,'succeeded',transaction_timestamp(),transaction_timestamp())`, f.fileID, f.accountID, f.sourceID, f.ingestID, hash[:])
	f.exec(tx, `INSERT INTO app.workout_types(id,account_id,type_key,provider_label) VALUES($1,$2,'running','Running')`, f.typeID, f.accountID)
	f.commit(tx)
	return f
}

func coverageIntegrationRollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx)
}

func (f *coverageIntegrationFixture) accountTx(ctx context.Context, pool *pgxpool.Pool) pgx.Tx {
	f.t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, f.accountID.String()); err != nil {
		coverageIntegrationRollback(tx)
		f.t.Fatal(err)
	}
	// Also cover Fatal paths in callers that commit their read transaction.
	f.t.Cleanup(func() { coverageIntegrationRollback(tx) })
	return tx
}

func (f *coverageIntegrationFixture) exec(tx pgx.Tx, sql string, args ...any) {
	f.t.Helper()
	if _, err := tx.Exec(f.ctx, sql, args...); err != nil {
		f.t.Fatalf("fixture SQL: %v", err)
	}
}

func (f *coverageIntegrationFixture) scan(tx pgx.Tx, sql string, args []any, dest ...any) {
	f.t.Helper()
	if err := tx.QueryRow(f.ctx, sql, args...).Scan(dest...); err != nil {
		f.t.Fatalf("fixture query: %v", err)
	}
}

func (f *coverageIntegrationFixture) commit(tx pgx.Tx) {
	f.t.Helper()
	if err := tx.Commit(f.ctx); err != nil {
		f.t.Fatal(err)
	}
}

func (f *coverageIntegrationFixture) fence(tx pgx.Tx) {
	f.t.Helper()
	var sourceID uuid.UUID
	f.scan(tx, `SELECT source_id FROM app.fence_ingest_job($1,'coverage-fixture-ingest',$2)`, []any{f.ingestID, f.ingestLease}, &sourceID)
	if sourceID != f.sourceID {
		f.t.Fatal("fixture ingest fence returned wrong source")
	}
}

// seedOSM creates three independent linear roads: two parallel and one crossing.
// Each region has real canonical partitions, provenance and geography lengths.
// Calling it again adds an overlapping decoy region without changing the target.
func (f *coverageIntegrationFixture) seedOSM() (string, int64) {
	f.t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	region := "fixture:coverage-" + suffix
	tx, err := f.osmAdminDB.Begin(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	defer coverageIntegrationRollback(tx)
	f.exec(tx, `INSERT INTO osm_catalog.regions(id,provider,provider_region_id,display_name,catalog_url,source_url,boundary)
		VALUES($1,'fixture',$2,'Coverage fixture','https://example.test/catalog','https://example.test/coverage.pbf',
		ST_Multi(ST_GeomFromText('POLYGON((-123 36,-121 36,-121 38,-123 38,-123 36))',4326)))`, region, "coverage-"+suffix)
	var generation int64
	f.scan(tx, `SELECT nextval(pg_get_serial_sequence('osm_catalog.generations','id'))`, nil, &generation)
	hash := sha256.Sum256([]byte(region))
	f.exec(tx, `INSERT INTO osm_catalog.generations(id,region_id,state,schema_name,source_url,source_sha256,importer_version,derivation_version,promoted_at)
		OVERRIDING SYSTEM VALUE VALUES($1,$2,'active',$3,'https://example.test/coverage.pbf',$4,1,1,clock_timestamp())`, generation, region, fmt.Sprintf("osm_build_%d", generation), hex.EncodeToString(hash[:]))
	f.exec(tx, `CREATE SCHEMA `+pgx.Identifier{fmt.Sprintf("osm_build_%d", generation)}.Sanitize())
	for _, table := range []string{"ways", "localities", "path_segments"} {
		query := fmt.Sprintf(`CREATE TABLE %s PARTITION OF %s FOR VALUES IN ('%s')`, pgx.Identifier{"osm_canonical", table + "_coverage_" + suffix}.Sanitize(), pgx.Identifier{"osm_canonical", table}.Sanitize(), region)
		f.exec(tx, query)
	}
	f.exec(tx, `INSERT INTO osm_catalog.region_storage(region_id,generation_id,ways_relation,localities_relation,path_segments_relation,build_schema)
		VALUES($1,$2,$3,$4,$5,$6)`, region, generation, "ways_coverage_"+suffix, "localities_coverage_"+suffix, "path_segments_coverage_"+suffix, fmt.Sprintf("osm_build_%d", generation))
	f.exec(tx, `INSERT INTO osm_canonical.localities(region_id,generation_id,relation_id,relation_version,admin_level,name,normalized_name,tags,geom)
		VALUES($1,$2,$2,3,8,'Fixture Town','fixture town','{}',ST_Multi(ST_GeomFromText('POLYGON((-123 36,-121 36,-121 38,-123 38,-123 36))',4326)))`, region, generation)
	var segments, paths []uuid.UUID
	for i, wkt := range []string{"LINESTRING(-122 37,-121.998 37)", "LINESTRING(-122 37.002,-121.998 37.002)", "LINESTRING(-121.999 36.999,-121.999 37.001)"} {
		segment, path := uuid.New(), uuid.New()
		segments, paths = append(segments, segment), append(paths, path)
		wayID := generation*10 + int64(i)
		f.exec(tx, `INSERT INTO osm_canonical.ways(region_id,generation_id,way_id,version,tags,node_ids,geom)
			VALUES($1,$2,$3,7,'{"surface":"asphalt"}','[1,2]',ST_GeomFromText($4,4326))`, region, generation, wayID, wkt)
		f.exec(tx, `INSERT INTO osm_canonical.path_segments(region_id,generation_id,segment_id,source_way_id,source_way_version,derivation_version,
			start_node_index,end_node_index,boundary_piece,start_graph_node_id,end_graph_node_id,name,normalized_name,highway,broad_class,tags,
			motor_forward_allowed,motor_reverse_allowed,geom,locality_relation_id,logical_path_id,length_m)
			VALUES($1,$2,$3,$4,7,1,0,1,1,$5,$6,$7,$8,'residential','road','{"surface":"asphalt"}',true,true,
			ST_GeomFromText($9,4326),$2,$10,ST_Length(ST_GeomFromText($9,4326)::geography))`, region, generation, segment, wayID, uuid.New(), uuid.New(), fmt.Sprintf("Fixture Road %d", i), fmt.Sprintf("fixture road %d", i), wkt, path)
	}
	f.commit(tx)
	f.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		tx, err := f.osmAdminDB.Begin(ctx)
		if err != nil {
			f.t.Error(err)
			return
		}
		defer coverageIntegrationRollback(tx)
		rows, err := tx.Query(ctx, `SELECT schema_name::text FROM osm_catalog.generations WHERE region_id=$1`, region)
		if err != nil {
			f.t.Error(err)
			return
		}
		var schemas []string
		for rows.Next() {
			var schema string
			if err := rows.Scan(&schema); err != nil {
				rows.Close()
				f.t.Error(err)
				return
			}
			schemas = append(schemas, schema)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			f.t.Error(err)
			return
		}
		for _, schema := range schemas {
			if _, err := tx.Exec(ctx, `DROP SCHEMA `+pgx.Identifier{schema}.Sanitize()); err != nil {
				f.t.Error(err)
				return
			}
		}
		for _, table := range []string{"path_segments", "localities", "ways"} {
			if _, err := tx.Exec(ctx, `DROP TABLE `+pgx.Identifier{"osm_canonical", table + "_coverage_" + suffix}.Sanitize()); err != nil {
				f.t.Error("OSM partition cleanup:", err)
				return
			}
		}
		for _, query := range []string{`DELETE FROM osm_catalog.region_storage WHERE region_id=$1`, `DELETE FROM osm_catalog.promotion_events WHERE region_id=$1`, `DELETE FROM osm_catalog.generations WHERE region_id=$1`, `DELETE FROM osm_catalog.regions WHERE id=$1`} {
			if _, err := tx.Exec(ctx, query, region); err != nil {
				f.t.Error(err)
				return
			}
		}
		if err := tx.Commit(ctx); err != nil {
			f.t.Error(err)
		}
	})
	if f.regionID == "" {
		f.regionID, f.generation, f.segments, f.paths = region, generation, segments, paths
	}
	return region, generation
}

// coverageIntegrationRoute traverses the middle 90% of a selected parallel road.
func coverageIntegrationRoute(road int) []coverageRoutePoint {
	points := make([]coverageRoutePoint, 19)
	for i := range points {
		timestamp := time.Date(2026, 9, 12, 8, 0, i*5, 0, time.UTC)
		accuracy := 3.0
		points[i] = coverageRoutePoint{Sequence: i, Timestamp: &timestamp, Latitude: 37 + float64(road)*0.002,
			Longitude: -121.9999 + float64(i)*0.0001, HorizontalAccuracy: &accuracy}
	}
	return points
}

func (f *coverageIntegrationFixture) addWorkout(points []coverageRoutePoint) uuid.UUID {
	f.t.Helper()
	id := uuid.New()
	tx := f.accountTx(f.ctx, f.workerDB)
	defer coverageIntegrationRollback(tx)
	f.fence(tx)
	hash := canonicalRouteInputSHA256(points)
	f.exec(tx, `INSERT INTO app.workouts(id,account_id,source_id,source_file_id,workout_type_id,provider_id,content_sha256,provider_label,
		started_at,ended_at,local_start_date,provider_duration,is_indoor)
		VALUES($1,$2,$3,$4,$5,$6,$7,'Running','2026-09-12T08:00:00Z','2026-09-12T08:02:00Z','2026-09-12',120,false)`, id, f.accountID, f.sourceID, f.fileID, f.typeID, id.String(), hash[:])
	f.commit(tx)
	f.updateRoute(id, points)
	return id
}

func (f *coverageIntegrationFixture) updateRoute(workout uuid.UUID, points []coverageRoutePoint) int64 {
	f.t.Helper()
	if len(points) < 2 || f.regionID == "" {
		f.t.Fatal("updateRoute requires at least two points and seedOSM first")
	}
	tx := f.accountTx(f.ctx, f.workerDB)
	defer coverageIntegrationRollback(tx)
	f.fence(tx)
	f.exec(tx, `DELETE FROM app.workout_route_points WHERE account_id=$1 AND workout_id=$2`, f.accountID, workout)
	minLon, maxLon, minLat, maxLat := points[0].Longitude, points[0].Longitude, points[0].Latitude, points[0].Latitude
	for _, point := range points {
		f.exec(tx, `INSERT INTO app.workout_route_points(account_id,workout_id,sequence,recorded_at,latitude,longitude,altitude,speed,course,
			horizontal_accuracy,vertical_accuracy,speed_accuracy,course_accuracy) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
			f.accountID, workout, point.Sequence, point.Timestamp, point.Latitude, point.Longitude, point.Altitude, point.Speed, point.Course,
			point.HorizontalAccuracy, point.VerticalAccuracy, point.SpeedAccuracy, point.CourseAccuracy)
		minLon, maxLon, minLat, maxLat = min(minLon, point.Longitude), max(maxLon, point.Longitude), min(minLat, point.Latitude), max(maxLat, point.Latitude)
	}
	var replaced bool
	f.scan(tx, `SELECT app.replace_workout_route_summary($1,$2,$3,$4,$5,$6,NULL,NULL,NULL,false)`, []any{workout, len(points), minLon, minLat, maxLon, maxLat}, &replaced)
	if !replaced {
		f.t.Fatal("fixture route summary rejected")
	}
	digest := canonicalRouteInputSHA256(points)
	var revision int64
	var changed bool
	f.scan(tx, `SELECT route_input_revision,digest_changed FROM app.initialize_workout_coverage($1,$2,$3,$4,'coverage-fixture-ingest',$5)`, []any{f.accountID, workout, digest[:], f.ingestID, f.ingestLease}, &revision, &changed)
	regions, _ := json.Marshal([]coverageTargetGeneration{{f.regionID, f.generation}})
	f.scan(tx, `SELECT app.set_workout_coverage_readiness($1,$2,$3,'map_data_ready',NULL,$4,$5,'coverage-fixture-ingest',$6)`, []any{f.accountID, workout, revision, regions, f.ingestID, f.ingestLease}, &changed)
	f.commit(tx)
	return revision
}

func (f *coverageIntegrationFixture) queue(workout uuid.UUID) coverageIntegrationJob {
	f.t.Helper()
	job := coverageIntegrationJob{uuid.New(), uuid.New()}
	tx := f.accountTx(f.ctx, f.apiDB)
	defer coverageIntegrationRollback(tx)
	var revision int64
	f.scan(tx, `SELECT route_input_revision FROM app.workout_coverage_states WHERE account_id=$1 AND workout_id=$2`, []any{f.accountID, workout}, &revision)
	f.commit(tx)
	tx = f.accountTx(f.ctx, f.workerDB)
	defer coverageIntegrationRollback(tx)
	targets, _ := json.Marshal([]map[string]any{{"jobId": job.childID, "workoutId": workout, "routeRevision": revision, "generations": []coverageTargetGeneration{{f.regionID, f.generation}}}})
	var count int
	var reused bool
	f.scan(tx, `SELECT job_id,route_count,reused FROM app.enqueue_coverage_update($1,$2,$3,$4,$5,$6,$7,$8,5,$9)`,
		[]any{f.accountID, job.parentID, f.regionID, f.generation, revision, string(coverage.ExperimentalRulesV1), string(coverage.ExperimentalSamplingV1), string(coverage.ExperimentalPathPolicyV82), targets}, &job.parentID, &count, &reused)
	if count < 1 {
		f.t.Fatal("coverage enqueue returned no routes")
	}
	f.commit(tx)
	return job
}

func (f *coverageIntegrationFixture) runner() *CoverageRunner {
	return NewCoverageRunnerWithOptions(f.coverageDB, f.osmDB, slog.New(slog.NewTextHandler(io.Discard, nil)),
		CoverageRunnerOptions{LeaseDuration: 10 * time.Second, HeartbeatInterval: 50 * time.Millisecond, AdmissionInterval: 10 * time.Millisecond, RouteTimeout: 20 * time.Second})
}

type coverageIntegrationGate struct {
	f    *coverageIntegrationFixture
	tx   pgx.Tx
	pool *pgxpool.Pool
	pid  int32
}

func (f *coverageIntegrationFixture) gate(pool *pgxpool.Pool, schema, table, mode string) *coverageIntegrationGate {
	f.t.Helper()
	if mode != "ACCESS EXCLUSIVE" && mode != "EXCLUSIVE" && mode != "SHARE" {
		f.t.Fatal("unsupported fixture lock mode")
	}
	tx, err := pool.Begin(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	g := &coverageIntegrationGate{f: f, tx: tx, pool: pool}
	f.t.Cleanup(g.release)
	f.scan(tx, `SELECT pg_backend_pid()`, nil, &g.pid)
	f.exec(tx, `SET LOCAL lock_timeout='5s'`)
	f.exec(tx, `LOCK TABLE `+pgx.Identifier{schema, table}.Sanitize()+` IN `+mode+` MODE`)
	return g
}

func (g *coverageIntegrationGate) release() {
	if g.tx != nil {
		coverageIntegrationRollback(g.tx)
		g.tx = nil
	}
}

func (g *coverageIntegrationGate) waitBlocked(client *pgxpool.Pool, queryFragment string) {
	g.f.t.Helper()
	application := client.Config().ConnConfig.RuntimeParams["application_name"]
	g.f.wait("database lock wait: "+queryFragment, func(ctx context.Context) (bool, error) {
		var blocked bool
		// NOSUPERUSER roles can inspect their own sessions without granting
		// pg_read_all_stats across the shared acceptance cluster.
		err := client.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity activity
			WHERE activity.application_name=$1 AND activity.state='active' AND activity.wait_event_type='Lock'
			AND strpos(activity.query,$2)>0 AND $3=ANY(pg_blocking_pids(activity.pid)))`, application, queryFragment, g.pid).Scan(&blocked)
		return blocked, err
	})
}

func (f *coverageIntegrationFixture) wait(description string, predicate func(context.Context) (bool, error)) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		ok, err := predicate(ctx)
		if err != nil {
			f.t.Fatalf("waiting for %s: %v", description, err)
		}
		if ok {
			return
		}
		select {
		case <-ctx.Done():
			f.t.Fatalf("timed out waiting for %s: %v", description, ctx.Err())
		case <-ticker.C:
		}
	}
}

type coverageIntegrationRunResult struct {
	worked bool
	err    error
}

func (f *coverageIntegrationFixture) start(runner *CoverageRunner) <-chan coverageIntegrationRunResult {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(f.ctx, 30*time.Second)
	done := make(chan coverageIntegrationRunResult, 1)
	go func() {
		worked, err := runner.RunOnce(ctx)
		done <- coverageIntegrationRunResult{worked, err}
		close(done)
	}()
	f.t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3*cleanupTimeout + time.Second):
			f.t.Error("RunOnce did not stop during bounded cleanup")
		}
	})
	return done
}

func (f *coverageIntegrationFixture) await(done <-chan coverageIntegrationRunResult) {
	f.t.Helper()
	select {
	case result := <-done:
		if !result.worked || result.err != nil {
			f.t.Fatalf("RunOnce worked=%v error=%v", result.worked, result.err)
		}
	case <-time.After(30 * time.Second):
		f.t.Fatal("RunOnce did not finish")
	}
}

func (f *coverageIntegrationFixture) run() {
	f.t.Helper()
	worked, err := f.runner().RunOnce(f.ctx)
	if !worked || err != nil {
		f.t.Fatalf("RunOnce worked=%v error=%v", worked, err)
	}
}

// visible reads through the API role in a fresh transaction, including copies
// and rollups. Exact comparison detects partial replacement and rollback leaks.
func (f *coverageIntegrationFixture) visible() string {
	f.t.Helper()
	tx := f.accountTx(f.ctx, f.apiDB)
	defer coverageIntegrationRollback(tx)
	var result string
	f.scan(tx, `SELECT jsonb_build_object(
		'matches',(SELECT jsonb_agg(to_jsonb(m) ORDER BY workout_id,physical_segment_id) FROM app.workout_segment_matches m WHERE account_id=$1),
		'copies',(SELECT jsonb_agg(to_jsonb(s) ORDER BY physical_segment_id) FROM app.path_segments s WHERE account_id=$1),
		'paths',(SELECT jsonb_agg(to_jsonb(p) ORDER BY logical_path_id) FROM app.coverage_paths p WHERE account_id=$1),
		'attributions',(SELECT jsonb_agg(to_jsonb(a) ORDER BY workout_id,logical_path_id) FROM app.workout_path_attributions a WHERE account_id=$1),
		'daily',(SELECT jsonb_agg(to_jsonb(d) ORDER BY logical_path_id,visit_date) FROM app.account_path_daily_rollups d WHERE account_id=$1),
		'allTime',(SELECT jsonb_agg(to_jsonb(a) ORDER BY logical_path_id) FROM app.account_path_all_time a WHERE account_id=$1))::text`, []any{f.accountID}, &result)
	return result
}

func (f *coverageIntegrationFixture) accountGeneration() int64 {
	f.t.Helper()
	tx := f.accountTx(f.ctx, f.apiDB)
	defer coverageIntegrationRollback(tx)
	var generation int64
	f.scan(tx, `SELECT generation FROM app.account_data_generations WHERE account_id=$1`, []any{f.accountID}, &generation)
	return generation
}

func (f *coverageIntegrationFixture) assertTerminal(job coverageIntegrationJob, status, outcome string) {
	f.t.Helper()
	tx := f.accountTx(f.ctx, f.apiDB)
	defer coverageIntegrationRollback(tx)
	var childStatus, parentStatus, result string
	var cleared, duration bool
	var total, processed, succeeded, failed, cancelled, superseded int
	f.scan(tx, `SELECT child.status,parent.status,COALESCE(route.result_outcome,''),
		child.worker_id IS NULL AND child.lease_token IS NULL AND child.lease_expires_at IS NULL,
		route.duration_milliseconds IS NOT NULL,
		progress.routes_total,progress.routes_processed,progress.routes_succeeded,progress.routes_failed,progress.routes_cancelled,progress.routes_superseded
		FROM app.jobs child JOIN app.jobs parent ON parent.id=child.parent_job_id
		JOIN app.coverage_route_job_contexts route ON route.job_id=child.id
		JOIN app.coverage_job_progress progress ON progress.job_id=parent.id WHERE child.id=$1`, []any{job.childID},
		&childStatus, &parentStatus, &result, &cleared, &duration, &total, &processed, &succeeded, &failed, &cancelled, &superseded)
	if childStatus != status || parentStatus != status || result != outcome || !cleared || total != 1 {
		f.t.Fatalf("terminal child/parent/outcome/cleared/total=%s/%s/%s/%v/%d, want %s/%s/%s/true/1", childStatus, parentStatus, result, cleared, total, status, status, outcome)
	}
	wantSucceeded, wantFailed, wantCancelled, wantSuperseded := 0, 0, 0, 0
	switch {
	case status == "failed":
		wantFailed = 1
	case status == "cancelled":
		wantCancelled = 1
	case outcome == "superseded":
		wantSuperseded = 1
	default:
		wantSucceeded = 1
	}
	if processed != wantSucceeded+wantFailed+wantSuperseded || succeeded != wantSucceeded || failed != wantFailed || cancelled != wantCancelled || superseded != wantSuperseded || (status != "cancelled" && !duration) {
		f.t.Fatalf("incorrect progress processed/succeeded/failed/cancelled/superseded=%d/%d/%d/%d/%d duration=%v", processed, succeeded, failed, cancelled, superseded, duration)
	}
	f.assertNoSlots()
}

func (f *coverageIntegrationFixture) assertNoSlots() {
	f.t.Helper()
	tx := f.accountTx(f.ctx, f.adminDB)
	defer coverageIntegrationRollback(tx)
	var slots, owners int
	f.scan(tx, `SELECT (SELECT count(*) FROM app.matcher_slots WHERE account_id=$1),
		(SELECT count(*) FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route' AND worker_id IS NOT NULL)`, []any{f.accountID}, &slots, &owners)
	if slots != 0 || owners != 0 {
		f.t.Fatalf("leaked matcher slots/coverage lease owners=%d/%d", slots, owners)
	}
}

func (f *coverageIntegrationFixture) assertCurrent(workout uuid.UUID, job coverageIntegrationJob, road int, revision int64) {
	f.t.Helper()
	tx := f.accountTx(f.ctx, f.apiDB)
	defer coverageIntegrationRollback(tx)
	var valid bool
	f.scan(tx, `SELECT state.processing_state='current' AND state.readiness_state='map_data_ready'
		AND state.route_input_revision=$3 AND state.applied_route_input_revision=$3 AND state.applied_route_input_sha256=state.route_input_sha256
		AND state.target_job_id=$2 AND state.target_generations=state.applied_generations
		AND state.applied_generations=jsonb_build_array(jsonb_build_object('regionId',$4::text,'generation',$5::bigint))
		AND state.applied_rules_version=$6 AND state.applied_sampling_version=$7 AND state.applied_path_policy_version=$8
		FROM app.workout_coverage_states state WHERE workout_id=$1`, []any{workout, job.childID, revision, f.regionID, f.generation,
		string(coverage.ExperimentalRulesV1), string(coverage.ExperimentalSamplingV1), string(coverage.ExperimentalPathPolicyV82)}, &valid)
	if !valid {
		f.t.Fatal("applied coverage state does not match the claimed route/generation/contract")
	}
	var matches, attributions int
	f.scan(tx, `SELECT (SELECT count(*) FROM app.workout_segment_matches WHERE workout_id=$1),
		(SELECT count(*) FROM app.workout_path_attributions WHERE workout_id=$1)`, []any{workout}, &matches, &attributions)
	if road < 0 {
		if matches != 0 || attributions != 0 {
			f.t.Fatalf("no-evidence retained matches/attributions=%d/%d", matches, attributions)
		}
		return
	}
	if matches != 1 || attributions != 1 {
		f.t.Fatalf("matches/attributions=%d/%d, want 1/1", matches, attributions)
	}
	f.scan(tx, `SELECT match.physical_segment_id=$2 AND match.logical_path_id=$3 AND match.region_id=$4 AND match.generation_id=$5
		AND match.first_route_order>=0 AND match.first_traversed_at BETWEEN '2026-09-12T08:00:00Z' AND '2026-09-12T08:02:00Z'
		AND ST_SRID(match.geom)=4326 AND GeometryType(match.geom)='MULTILINESTRING' AND ST_IsValid(match.geom)
		AND match.covered_meters>100 AND match.covered_meters<segment.segment_meters
		AND abs(match.covered_meters-ST_Length(match.geom::geography))<0.001
		AND segment.source_way_id=$6 AND segment.source_way_version=7 AND segment.derivation_version=1
		AND segment.highway='residential' AND segment.broad_class='road' AND segment.tags='{"surface":"asphalt"}'::jsonb
		AND abs(segment.segment_meters-ST_Length(segment.geom::geography))<0.001
		AND ST_Equals(segment.geom,ST_GeomFromText($7,4326)) AND ST_CoveredBy(match.geom,ST_Buffer(segment.geom,0.00000001))
		AND path.locality_relation_id=$5 AND path.locality_relation_version=3 AND path.locality_name='Fixture Town'
		AND path.name=$8 AND path.normalized_name=$9
		AND attribution.first_physical_segment_id=match.physical_segment_id AND attribution.first_generation_id=$5
		AND attribution.first_region_id=$4 AND attribution.first_traversed_at=match.first_traversed_at
		FROM app.workout_segment_matches match JOIN app.path_segments segment
		ON segment.account_id=match.account_id AND segment.region_id=match.region_id AND segment.generation_id=match.generation_id AND segment.physical_segment_id=match.physical_segment_id
		JOIN app.coverage_paths path ON path.account_id=match.account_id AND path.logical_path_id=match.logical_path_id
		JOIN app.workout_path_attributions attribution ON attribution.workout_id=match.workout_id AND attribution.logical_path_id=match.logical_path_id
		WHERE match.workout_id=$1`, []any{workout, f.segments[road], f.paths[road], f.regionID, f.generation, f.generation*10 + int64(road),
		fmt.Sprintf("LINESTRING(-122 %g,-121.998 %g)", 37+float64(road)*0.002, 37+float64(road)*0.002), fmt.Sprintf("Fixture Road %d", road), fmt.Sprintf("fixture road %d", road)}, &valid)
	if !valid {
		f.t.Fatal("geometry, clipping, source provenance, path or attribution copy is incorrect")
	}
	f.scan(tx, `SELECT daily.workout_count=(SELECT count(*) FROM app.workout_path_attributions WHERE logical_path_id=$1 AND visit_date=daily.visit_date)
		AND all_time.workout_count=(SELECT count(*) FROM app.workout_path_attributions WHERE logical_path_id=$1)
		AND daily.visit_date='2026-09-12' AND all_time.first_visit_date='2026-09-12' AND all_time.latest_visit_date='2026-09-12'
		FROM app.account_path_daily_rollups daily JOIN app.account_path_all_time all_time USING(account_id,logical_path_id)
		WHERE daily.logical_path_id=$1`, []any{f.paths[road]}, &valid)
	if !valid {
		f.t.Fatal("path rollups disagree with persisted attributions")
	}
}

func assertCoverageIntegrationCounter(t *testing.T, metrics map[string]metricdata.Metrics, name string, labels map[string]string, want int64) {
	t.Helper()
	sum, ok := metrics[name].Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("missing SDK counter %s", name)
	}
	var value int64
	for _, point := range sum.DataPoints {
		match := true
		for key, expected := range labels {
			actual, found := point.Attributes.Value(attribute.Key(key))
			match = match && found && actual.AsString() == expected
		}
		if match {
			value += point.Value
		}
	}
	if value != want {
		t.Fatalf("%s labels=%v value=%d, want %d", name, labels, value, want)
	}
}

func TestCoverageRunnerRunOnceHappyClaimSnapshotAndCopiesIntegration(t *testing.T) {
	f := newCoverageIntegrationFixture(t)
	f.seedOSM()
	// A later overlapping generation must not leak into the target snapshot.
	f.seedOSM()
	workout := f.addWorkout(coverageIntegrationRoute(0))
	job := f.queue(workout)
	runner := f.runner()
	metrics, reader := coverageTestMetrics(t)
	runner.metrics = metrics
	worked, err := runner.RunOnce(f.ctx)
	if !worked || err != nil {
		t.Fatalf("RunOnce worked=%v err=%v", worked, err)
	}
	f.assertTerminal(job, "succeeded", "applied")
	f.assertCurrent(workout, job, 0, 1)
	worked, err = runner.RunOnce(f.ctx)
	if worked || err != nil {
		t.Fatalf("idle RunOnce worked=%v err=%v", worked, err)
	}
	exported := collectCoverageMetrics(t, reader)
	assertCoverageIntegrationCounter(t, exported, "coverage.route.outcomes", map[string]string{"outcome": "applied", "failure": "none"}, 1)
	assertCoverageIntegrationCounter(t, exported, "coverage.control.calls", map[string]string{"operation": "admission", "result": "acquired"}, 1)
	for _, name := range []string{"coverage.queue.age", "coverage.matcher.duration", "coverage.route.duration", "coverage.admission.wait"} {
		histogram, ok := exported[name].Data.(metricdata.Histogram[float64])
		if !ok || len(histogram.DataPoints) != 1 || histogram.DataPoints[0].Count != 1 {
			t.Fatalf("missing single SDK histogram observation: %s", name)
		}
	}
}

func TestCoverageRunnerRunOnceHeartbeatRenewsJobAndSlotIntegration(t *testing.T) {
	f := newCoverageIntegrationFixture(t)
	f.seedOSM()
	job := f.queue(f.addWorkout(coverageIntegrationRoute(0)))
	gate := f.gate(f.osmAdminDB, "osm_canonical", "path_segments", "ACCESS EXCLUSIVE")
	runner := f.runner()
	// Still wait past the original expiry, but allow race instrumentation and
	// port-forward latency enough headroom for the successful persist fence.
	runner.leaseDuration = 3 * time.Second
	metrics, reader := coverageTestMetrics(t)
	runner.metrics = metrics
	done := f.start(runner)
	gate.waitBlocked(f.osmDB, "WITH allowed AS")
	var initialExpiry, initialSlotExpiry time.Time
	tx := f.accountTx(f.ctx, f.adminDB)
	f.scan(tx, `SELECT job.lease_expires_at,slot.expires_at FROM app.jobs job JOIN app.matcher_slots slot ON slot.job_id=job.id
		WHERE job.id=$1 AND slot.owner_id=$2 AND slot.lease_token=job.lease_token`, []any{job.childID, runner.workerID}, &initialExpiry, &initialSlotExpiry)
	f.commit(tx)
	if !initialExpiry.Equal(initialSlotExpiry) {
		t.Fatal("slot initially disagrees with job lease")
	}
	f.wait("heartbeat and slot renewal past original lease expiry", func(ctx context.Context) (bool, error) {
		var renewed bool
		tx, err := f.adminDB.Begin(ctx)
		if err != nil {
			return false, err
		}
		defer coverageIntegrationRollback(tx)
		if _, err = tx.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, f.accountID.String()); err != nil {
			return false, err
		}
		err = tx.QueryRow(ctx, `SELECT job.status='running' AND job.worker_id=$2 AND job.lease_expires_at>$3
			AND slot.expires_at=job.lease_expires_at AND slot.owner_id=$2 AND slot.lease_token=job.lease_token
			AND clock_timestamp()>$3 AND (SELECT count(*) FROM app.matcher_slots WHERE account_id=$4)=1
			FROM app.jobs job JOIN app.matcher_slots slot ON slot.job_id=job.id WHERE job.id=$1`, job.childID, runner.workerID, initialExpiry, f.accountID).Scan(&renewed)
		return renewed, err
	})
	gate.release()
	f.await(done)
	f.assertTerminal(job, "succeeded", "applied")
	exported := collectCoverageMetrics(t, reader)
	sum := exported["coverage.control.calls"].Data.(metricdata.Sum[int64])
	var renewals int64
	for _, point := range sum.DataPoints {
		operation, _ := point.Attributes.Value("operation")
		result, _ := point.Attributes.Value("result")
		if operation.AsString() == "heartbeat" && result.AsString() == "renewed" {
			renewals += point.Value
		}
	}
	if renewals < 2 {
		t.Fatalf("heartbeat renewal metrics=%d, want >=2", renewals)
	}
}

func TestCoverageRunnerRunOnceAtomicReplacementIntegration(t *testing.T) {
	f := newCoverageIntegrationFixture(t)
	f.seedOSM()
	workout := f.addWorkout(coverageIntegrationRoute(0))
	first := f.queue(workout)
	f.run()
	f.assertCurrent(workout, first, 0, 1)
	old := f.visible()
	f.updateRoute(workout, coverageIntegrationRoute(1))
	job := f.queue(workout)
	matcher := f.gate(f.osmAdminDB, "osm_canonical", "path_segments", "ACCESS EXCLUSIVE")
	done := f.start(f.runner())
	matcher.waitBlocked(f.osmDB, "WITH allowed AS")
	persist := f.gate(f.adminDB, "app", "coverage_route_job_contexts", "EXCLUSIVE")
	matcher.release()
	persist.waitBlocked(f.coverageDB, "app.persist_coverage_route")
	generation := f.accountGeneration()
	if got := f.visible(); got != old {
		t.Fatal("old evidence/copies/rollups changed before blocked persistence committed")
	}
	if f.accountGeneration() != generation {
		t.Fatal("blocked persistence changed account data generation")
	}
	persist.release()
	f.await(done)
	f.assertTerminal(job, "succeeded", "applied")
	f.assertCurrent(workout, job, 1, 2)
	if f.accountGeneration() <= generation {
		t.Fatal("committed replacement did not advance account data generation")
	}
	if f.visible() == old {
		t.Fatal("successful replacement left old evidence visible")
	}
	tx := f.accountTx(f.ctx, f.apiDB)
	defer coverageIntegrationRollback(tx)
	var oldRows int
	f.scan(tx, `SELECT (SELECT count(*) FROM app.path_segments WHERE physical_segment_id=$1)+
		(SELECT count(*) FROM app.coverage_paths WHERE logical_path_id=$2)+
		(SELECT count(*) FROM app.account_path_all_time WHERE logical_path_id=$2)+
		(SELECT count(*) FROM app.account_path_daily_rollups WHERE logical_path_id=$2)`, []any{f.segments[0], f.paths[0]}, &oldRows)
	if oldRows != 0 {
		t.Fatalf("unreferenced old copies/rollups=%d", oldRows)
	}
}

func TestCoverageRunnerRunOnceNoEvidenceDeletesPriorRetainsSharedCopiesIntegration(t *testing.T) {
	f := newCoverageIntegrationFixture(t)
	f.seedOSM()
	firstWorkout := f.addWorkout(coverageIntegrationRoute(0))
	f.queue(firstWorkout)
	f.run()
	sharedWorkout := f.addWorkout(coverageIntegrationRoute(0))
	sharedJob := f.queue(sharedWorkout)
	f.run()
	f.assertCurrent(sharedWorkout, sharedJob, 0, 1)
	points := coverageIntegrationRoute(0)
	for i := range points {
		points[i].Latitude += 0.05
	}
	f.updateRoute(firstWorkout, points)
	job := f.queue(firstWorkout)
	f.run()
	f.assertTerminal(job, "succeeded", "no_evidence")
	f.assertCurrent(firstWorkout, job, -1, 2)
	f.assertCurrent(sharedWorkout, sharedJob, 0, 1)
	tx := f.accountTx(f.ctx, f.apiDB)
	defer coverageIntegrationRollback(tx)
	var copies, paths int
	f.scan(tx, `SELECT (SELECT count(*) FROM app.path_segments WHERE account_id=$1),(SELECT count(*) FROM app.coverage_paths WHERE account_id=$1)`, []any{f.accountID}, &copies, &paths)
	if copies != 1 || paths != 1 {
		t.Fatalf("shared copies/paths=%d/%d, want 1/1", copies, paths)
	}
}

func TestCoverageRunnerRunOnceGenerationFenceBeforeSnapshotIntegration(t *testing.T) {
	f := newCoverageIntegrationFixture(t)
	f.seedOSM()
	workout := f.addWorkout(coverageIntegrationRoute(0))
	f.queue(workout)
	f.run()
	old := f.visible()
	job := f.queue(workout)
	// Admission blocks after claim and route load, before BeginMatcherSnapshot.
	admission := f.gate(f.adminDB, "app", "matcher_slot_guard", "ACCESS EXCLUSIVE")
	done := f.start(f.runner())
	admission.waitBlocked(f.coverageDB, "app.acquire_coverage_matcher_slot")
	tx, err := f.osmAdminDB.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer coverageIntegrationRollback(tx)
	f.exec(tx, `UPDATE osm_catalog.generations SET state='retired',retired_at=clock_timestamp() WHERE id=$1`, f.generation)
	var next int64
	f.scan(tx, `SELECT nextval(pg_get_serial_sequence('osm_catalog.generations','id'))`, nil, &next)
	hash := sha256.Sum256([]byte(fmt.Sprintf("replacement-%d", next)))
	f.exec(tx, `INSERT INTO osm_catalog.generations(id,region_id,state,schema_name,source_url,source_sha256,importer_version,derivation_version,promoted_at)
		OVERRIDING SYSTEM VALUE VALUES($1,$2,'active',$3,'https://example.test/coverage.pbf',$4,1,1,clock_timestamp())`, next, f.regionID, fmt.Sprintf("osm_build_%d", next), hex.EncodeToString(hash[:]))
	f.exec(tx, `CREATE SCHEMA `+pgx.Identifier{fmt.Sprintf("osm_build_%d", next)}.Sanitize())
	for _, table := range []string{"ways", "localities", "path_segments"} {
		f.exec(tx, `UPDATE `+pgx.Identifier{"osm_canonical", table}.Sanitize()+` SET generation_id=$1 WHERE region_id=$2`, next, f.regionID)
	}
	f.exec(tx, `UPDATE osm_catalog.region_storage SET generation_id=$1,build_schema=$2 WHERE region_id=$3`, next, fmt.Sprintf("osm_build_%d", next), f.regionID)
	f.commit(tx)
	admission.release()
	f.await(done)
	f.assertTerminal(job, "succeeded", "superseded")
	if f.visible() != old {
		t.Fatal("generation supersession replaced prior evidence")
	}
	tx = f.accountTx(f.ctx, f.apiDB)
	defer coverageIntegrationRollback(tx)
	var stale bool
	f.scan(tx, `SELECT processing_state='stale' AND applied_route_input_revision=1
		AND applied_generations=jsonb_build_array(jsonb_build_object('regionId',$2::text,'generation',$3::bigint))
		FROM app.workout_coverage_states WHERE workout_id=$1`, []any{workout, f.regionID, f.generation}, &stale)
	if !stale {
		t.Fatal("generation fence did not preserve prior applied generation and mark state stale")
	}
}

// Simulate lease expiry without disabling the job state-machine trigger, then
// use its actual recovery transition. This keeps loss tests independent of time.
func (f *coverageIntegrationFixture) recover(job coverageIntegrationJob) {
	f.t.Helper()
	tx := f.accountTx(f.ctx, f.adminDB)
	defer coverageIntegrationRollback(tx)
	f.exec(tx, `SELECT set_config('app.job_transition','heartbeat',true)`)
	f.exec(tx, `UPDATE app.jobs SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE id=$1 AND status='running'`, job.childID)
	var recovered bool
	f.scan(tx, `SELECT app.recover_expired_job($1)`, []any{job.childID}, &recovered)
	if !recovered {
		f.t.Fatal("expired coverage lease was not recovered")
	}
	f.commit(tx)
}

func TestCoverageRunnerRunOnceHeartbeatLeaseLossIntegration(t *testing.T) {
	f := newCoverageIntegrationFixture(t)
	f.seedOSM()
	workout := f.addWorkout(coverageIntegrationRoute(0))
	f.queue(workout)
	f.run()
	old := f.visible()
	job := f.queue(workout)
	matcher := f.gate(f.osmAdminDB, "osm_canonical", "path_segments", "ACCESS EXCLUSIVE")
	runner := f.runner()
	metrics, reader := coverageTestMetrics(t)
	runner.metrics = metrics
	done := f.start(runner)
	matcher.waitBlocked(f.osmDB, "WITH allowed AS")
	f.recover(job)
	// Keep the matcher blocked: only the heartbeat's lost fence can end this run.
	f.await(done)
	matcher.release()
	if f.visible() != old {
		t.Fatal("heartbeat lease loss changed prior evidence")
	}
	tx := f.accountTx(f.ctx, f.apiDB)
	var recovered bool
	f.scan(tx, `SELECT job.status='queued' AND job.worker_id IS NULL AND route.result_outcome IS NULL
		AND state.processing_state='queued' AND state.applied_route_input_revision=1
		FROM app.jobs job JOIN app.coverage_route_job_contexts route ON route.job_id=job.id
		JOIN app.workout_coverage_states state ON state.workout_id=route.workout_id WHERE job.id=$1`, []any{job.childID}, &recovered)
	f.commit(tx)
	if !recovered {
		t.Fatal("lost worker wrote a durable terminal outcome or changed recovered state")
	}
	f.assertNoSlots()
	exported := collectCoverageMetrics(t, reader)
	assertCoverageIntegrationCounter(t, exported, "coverage.control.calls", map[string]string{"operation": "heartbeat", "result": "lost"}, 1)
	if _, found := exported["coverage.route.outcomes"]; found {
		t.Fatal("lost worker emitted a terminal outcome")
	}
	// Recovery remains runnable by a new worker; no leaked slot blocks admission.
	f.run()
	f.assertTerminal(job, "succeeded", "applied")
}

func TestCoverageRunnerRunOncePersistRejectsStaleOwnershipIntegration(t *testing.T) {
	f := newCoverageIntegrationFixture(t)
	f.seedOSM()
	workout := f.addWorkout(coverageIntegrationRoute(0))
	f.queue(workout)
	f.run()
	old := f.visible()
	f.updateRoute(workout, coverageIntegrationRoute(1))
	job := f.queue(workout)
	matcher := f.gate(f.osmAdminDB, "osm_canonical", "path_segments", "ACCESS EXCLUSIVE")
	runner := f.runner()
	// Persistence, not a racing heartbeat, must reject the stale lease.
	runner.heartbeatInterval = time.Minute
	metrics, reader := coverageTestMetrics(t)
	runner.metrics = metrics
	done := f.start(runner)
	matcher.waitBlocked(f.osmDB, "WITH allowed AS")
	persist := f.gate(f.adminDB, "app", "coverage_route_job_contexts", "EXCLUSIVE")
	matcher.release()
	persist.waitBlocked(f.coverageDB, "app.persist_coverage_route")
	// Recovery and claim read contexts without row locks; the EXCLUSIVE gate
	// allows those reads but prevents the old persist's SELECT FOR UPDATE.
	f.recover(job)
	replacement := f.runner()
	claim, found, err := replacement.claim(f.ctx)
	if err != nil || !found || claim.jobID != job.childID {
		t.Fatalf("replacement claim found=%v id=%s err=%v", found, claim.jobID, err)
	}
	persist.release()
	f.await(done)
	if f.visible() != old {
		t.Fatal("stale persistence changed prior coverage")
	}
	tx := f.accountTx(f.ctx, f.adminDB)
	var owned bool
	f.scan(tx, `SELECT status='running' AND worker_id=$2 AND lease_token=$3 AND attempt=2
		AND (SELECT count(*) FROM app.jobs WHERE account_id=$4 AND kind='coverage_update_route' AND worker_id IS NOT NULL)=1
		AND (SELECT count(*) FROM app.matcher_slots WHERE account_id=$4)=0
		FROM app.jobs WHERE id=$1`, []any{job.childID, replacement.workerID, claim.leaseID, f.accountID}, &owned)
	f.commit(tx)
	if !owned {
		t.Fatal("stale persistence overwrote the newer lease or leaked a matcher slot")
	}
	exported := collectCoverageMetrics(t, reader)
	assertCoverageIntegrationCounter(t, exported, "coverage.stale.rejections", map[string]string{"operation": "persist"}, 1)
	if _, found := exported["coverage.route.outcomes"]; found {
		t.Fatal("stale persistence emitted a terminal outcome")
	}
	f.recover(job)
	f.assertNoSlots()
	f.run()
	f.assertTerminal(job, "succeeded", "applied")
	f.assertCurrent(workout, job, 1, 2)
}

func TestCoverageRunnerRunOnceNewerTargetAndRevisionSupersedeIntegration(t *testing.T) {
	for _, changeRevision := range []bool{false, true} {
		name := "newer_target"
		if changeRevision {
			name = "newer_route_revision"
		}
		t.Run(name, func(t *testing.T) {
			f := newCoverageIntegrationFixture(t)
			f.seedOSM()
			workout := f.addWorkout(coverageIntegrationRoute(0))
			f.queue(workout)
			f.run()
			old := f.visible()
			job := f.queue(workout)
			matcher := f.gate(f.osmAdminDB, "osm_canonical", "path_segments", "ACCESS EXCLUSIVE")
			done := f.start(f.runner())
			matcher.waitBlocked(f.osmDB, "WITH allowed AS")
			var revision int64 = 1
			if changeRevision {
				revision = f.updateRoute(workout, coverageIntegrationRoute(1))
			}
			newer := f.queue(workout)
			if newer.childID == job.childID || newer.parentID == job.parentID {
				t.Fatal("running target was unexpectedly reused")
			}
			matcher.release()
			f.await(done)
			f.assertTerminal(job, "succeeded", "superseded")
			if f.visible() != old {
				t.Fatal("superseded route replaced prior evidence")
			}
			tx := f.accountTx(f.ctx, f.apiDB)
			var retained bool
			f.scan(tx, `SELECT state.target_job_id=$2 AND state.route_input_revision=$3 AND state.processing_state='queued'
				AND state.applied_route_input_revision=1 AND newer.status='queued'
				FROM app.workout_coverage_states state JOIN app.jobs newer ON newer.id=state.target_job_id WHERE state.workout_id=$1`, []any{workout, newer.childID, revision}, &retained)
			f.commit(tx)
			if !retained {
				t.Fatal("superseded completion overwrote newer route target/state")
			}
			f.run()
			road := 0
			if changeRevision {
				road = 1
			}
			f.assertTerminal(newer, "succeeded", "applied")
			f.assertCurrent(workout, newer, road, revision)
		})
	}
}

func TestCoverageRunnerRunOncePersistenceFailureRollsBackIntegration(t *testing.T) {
	f := newCoverageIntegrationFixture(t)
	f.seedOSM()
	workout := f.addWorkout(coverageIntegrationRoute(0))
	f.queue(workout)
	f.run()
	old := f.visible()
	f.updateRoute(workout, coverageIntegrationRoute(1))
	job := f.queue(workout)
	matcher := f.gate(f.osmAdminDB, "osm_canonical", "path_segments", "ACCESS EXCLUSIVE")
	runner := f.runner()
	metrics, reader := coverageTestMetrics(t)
	runner.metrics = metrics
	done := f.start(runner)
	matcher.waitBlocked(f.osmDB, "WITH allowed AS")
	// A fixture-scoped CHECK fails only the new copy insert, after deletion of
	// old matches/attributions and rollup/pruning triggers have already executed.
	constraint := "coverage_test_reject_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	tx := f.accountTx(f.ctx, f.adminDB)
	f.exec(tx, fmt.Sprintf(`ALTER TABLE app.path_segments ADD CONSTRAINT %s CHECK (account_id<>'%s'::uuid OR physical_segment_id<>'%s'::uuid) NOT VALID`, pgx.Identifier{constraint}.Sanitize(), f.accountID, f.segments[1]))
	f.commit(tx)
	removeConstraint := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := f.adminDB.Exec(ctx, `ALTER TABLE app.path_segments DROP CONSTRAINT IF EXISTS `+pgx.Identifier{constraint}.Sanitize()); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(removeConstraint)
	matcher.release()
	f.await(done)
	f.assertTerminal(job, "failed", "")
	if f.visible() != old {
		t.Fatal("persistence failure leaked deletes/inserts/pruning/rollup changes")
	}
	tx = f.accountTx(f.ctx, f.apiDB)
	defer coverageIntegrationRollback(tx)
	var failed bool
	f.scan(tx, `SELECT processing_state='failed' AND processing_failure_code='coverage-persist-failed'
		AND applied_route_input_revision=1 AND route_input_revision=2 FROM app.workout_coverage_states WHERE workout_id=$1`, []any{workout}, &failed)
	if !failed {
		t.Fatal("persist failure did not retain prior applied revision and record failure")
	}
	assertCoverageIntegrationCounter(t, collectCoverageMetrics(t, reader), "coverage.route.outcomes", map[string]string{"outcome": "failed", "failure": "persist"}, 1)
}

func TestCoverageRunnerRunOnceCancellationIntegration(t *testing.T) {
	f := newCoverageIntegrationFixture(t)
	f.seedOSM()
	workout := f.addWorkout(coverageIntegrationRoute(0))
	f.queue(workout)
	f.run()
	old := f.visible()
	job := f.queue(workout)
	matcher := f.gate(f.osmAdminDB, "osm_canonical", "path_segments", "ACCESS EXCLUSIVE")
	runner := f.runner()
	metrics, reader := coverageTestMetrics(t)
	runner.metrics = metrics
	done := f.start(runner)
	matcher.waitBlocked(f.osmDB, "WITH allowed AS")
	tx := f.accountTx(f.ctx, f.adminDB)
	var requested bool
	f.scan(tx, `SELECT app.request_coverage_job_cancellation($1,$2)`, []any{job.parentID, uuid.New()}, &requested)
	f.commit(tx)
	if !requested {
		t.Fatal("coverage cancellation request rejected")
	}
	f.await(done)
	matcher.release()
	f.assertTerminal(job, "cancelled", "")
	if f.visible() != old {
		t.Fatal("cancellation changed prior evidence")
	}
	tx = f.accountTx(f.ctx, f.apiDB)
	defer coverageIntegrationRollback(tx)
	var stale bool
	f.scan(tx, `SELECT processing_state='stale' AND applied_route_input_revision=1 FROM app.workout_coverage_states WHERE workout_id=$1`, []any{workout}, &stale)
	if !stale {
		t.Fatal("cancelled route did not preserve prior applied state")
	}
	assertCoverageIntegrationCounter(t, collectCoverageMetrics(t, reader), "coverage.route.outcomes", map[string]string{"outcome": "cancelled", "failure": "none"}, 1)
}
