package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAdoptionCoverageWorkerIntegration(t *testing.T) {
	for _, recovery := range []bool{false, true} {
		t.Run(map[bool]string{false: "post_file_repair", true: "completed_job_crash_recovery"}[recovery], func(t *testing.T) {
			f, workout, digest := newAdoptionCoverageFixture(t)
			trace := &adoptionCoverageLockTrace{fixture: f, workout: workout}
			config := f.osmDB.Config()
			config.ConnConfig.Tracer = trace
			tracedOSM, err := pgxpool.NewWithConfig(f.ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(tracedOSM.Close)
			f.osmDB = tracedOSM
			tx := f.accountTx(f.ctx, f.adminDB)
			f.exec(tx, `INSERT INTO app.adopted_workout_coverage_refreshes(account_id,workout_id,source_id,source_file_id)
				VALUES($1,$2,$3,$4)`, f.accountID, workout, f.sourceID, f.fileID)
			// The scan is deliberately completed and not due. Only the request
			// may make this account eligible, even with the active generation.
			f.exec(tx, `INSERT INTO app.coverage_reconciliation_accounts(account_id,desired_revision,active_revision,completed_revision,next_scan_at)
				VALUES($1,1,1,1,clock_timestamp()+interval '1 day')`, f.accountID)
			f.commit(tx)
			if recovery {
				var finished bool
				tx = f.accountTx(f.ctx, f.workerDB)
				f.scan(tx, `SELECT app.finish_job($1,'adoption-worker-fixture',$2,'succeeded')`, []any{f.ingestID, f.ingestLease}, &finished)
				f.commit(tx)
				if !finished {
					t.Fatal("finish fixture ingest")
				}
				r := NewCoverageReconciler(f.coverageDB, f.osmDB, slog.Default(), CoverageReconcilerOptions{})
				if worked, err := r.RunOnce(f.ctx); err != nil || !worked {
					t.Fatalf("crash recovery worked=%v err=%v", worked, err)
				}
				if worked, err := r.RunOnce(f.ctx); err != nil || worked {
					t.Fatalf("completed adoption was reclaimed: worked=%v err=%v", worked, err)
				}
			} else {
				r := &Runner{db: f.workerDB, osmDB: f.osmDB, workerID: "adoption-worker-fixture", coverageMinTraversalMeters: 5, leaseDuration: defaultLease}
				job := claimedJob{id: f.ingestID, accountID: f.accountID, lease: f.ingestLease}
				for range 2 {
					if err := r.repairCoverageReadiness(f.ctx, job); err != nil {
						t.Fatal(err)
					}
				}
			}
			tx = f.accountTx(f.ctx, f.adminDB)
			var valid bool
			f.scan(tx, `SELECT route_input_revision=7 AND route_input_sha256=$3 AND readiness_state='map_data_ready'
				AND target_job_id IS NOT NULL
				AND NOT EXISTS(SELECT 1 FROM app.adopted_workout_coverage_refreshes WHERE account_id=$1)
				AND (SELECT count(*)=1 FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route')
				FROM app.workout_coverage_states WHERE account_id=$1 AND workout_id=$2`, []any{f.accountID, workout, digest}, &valid)
			f.commit(tx)
			if !valid {
				t.Fatal("unchanged/current-generation adoption did not enqueue exactly once while preserving raw revision/hash")
			}
			if trace.checks == 0 || trace.err != nil {
				t.Fatalf("OSM lookup lock scope: checks=%d err=%v", trace.checks, trace.err)
			}
		})
	}
}

type adoptionCoverageLockTrace struct {
	fixture *coverageIntegrationFixture
	workout uuid.UUID
	checks  int
	err     error
}

func (trace *adoptionCoverageLockTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, query pgx.TraceQueryStartData) context.Context {
	if !strings.Contains(query.SQL, "WITH route_point AS") || trace.err != nil {
		return ctx
	}
	trace.checks++
	f := trace.fixture
	if f.workerDB.Stat().AcquiredConns() != 0 || f.coverageDB.Stat().AcquiredConns() != 0 {
		trace.err = errors.New("APP transaction remains checked out during OSM readiness lookup")
		return ctx
	}
	tx, err := beginAccount(ctx, f.adminDB, f.accountID)
	if err != nil {
		trace.err = err
		return ctx
	}
	defer tx.Rollback(ctx)
	for _, query := range []struct {
		sql string
		key uuid.UUID
	}{
		{`SELECT 1 FROM app.accounts WHERE id=$1 FOR UPDATE NOWAIT`, f.accountID},
		{`SELECT 1 FROM app.workouts WHERE id=$1 FOR UPDATE NOWAIT`, trace.workout},
		{`SELECT 1 FROM app.account_data_generations WHERE account_id=$1 FOR UPDATE NOWAIT`, f.accountID},
	} {
		if _, err := tx.Exec(ctx, query.sql, query.key); err != nil {
			trace.err = err
			break
		}
	}
	return ctx
}

func (*adoptionCoverageLockTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func TestAdoptionCoverageTransientReconciliationIntegration(t *testing.T) {
	f, workout, digest := newAdoptionCoverageFixture(t)
	tx := f.accountTx(f.ctx, f.adminDB)
	f.exec(tx, `INSERT INTO app.adopted_workout_coverage_refreshes(account_id,workout_id,source_id,source_file_id)
		VALUES($1,$2,$3,$4)`, f.accountID, workout, f.sourceID, f.fileID)
	f.exec(tx, `INSERT INTO app.coverage_reconciliation_accounts(account_id,desired_revision,active_revision,completed_revision,next_scan_at)
		VALUES($1,1,1,1,clock_timestamp()+interval '1 day')`, f.accountID)
	f.commit(tx)
	blocker := f.accountTx(f.ctx, f.adminDB)
	f.exec(blocker, `SELECT 1 FROM app.workouts WHERE account_id=$1 AND id=$2 FOR UPDATE`, f.accountID, workout)
	r := NewCoverageReconciler(f.coverageDB, f.osmDB, slog.Default(), CoverageReconcilerOptions{})
	worked, err := r.RunOnce(f.ctx)
	var sqlErr *pgconn.PgError
	if !worked || !errors.As(err, &sqlErr) || sqlErr.Code != "55P03" {
		t.Fatalf("contended reconciliation worked=%v err=%v", worked, err)
	}
	tx = f.accountTx(f.ctx, f.adminDB)
	var retained bool
	f.scan(tx, `SELECT worker_id IS NULL AND lease_token IS NULL AND last_failure_code='coverage-reconciliation-failed'
		AND next_scan_at>clock_timestamp() AND EXISTS(SELECT 1 FROM app.adopted_workout_coverage_refreshes WHERE account_id=$1)
		AND NOT EXISTS(SELECT 1 FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route')
		FROM app.coverage_reconciliation_accounts WHERE account_id=$1`, []any{f.accountID}, &retained)
	f.commit(tx)
	if !retained {
		t.Fatal("NOWAIT contention lost the obligation or its bounded retry")
	}
	if worked, err := r.RunOnce(f.ctx); err != nil || worked {
		t.Fatalf("transient failure caused a hot claim loop: worked=%v err=%v", worked, err)
	}
	f.commit(blocker)
	tx = f.accountTx(f.ctx, f.adminDB)
	f.exec(tx, `UPDATE app.coverage_reconciliation_accounts SET next_scan_at='-infinity',cursor_workout_id=$2 WHERE account_id=$1`, f.accountID, workout)
	f.commit(tx)
	claim, found, err := r.claim(f.ctx)
	if err != nil || !found || claim.accountID != f.accountID || claim.cursor != nil {
		t.Fatalf("fresh pending-outbox claim found=%v cursor=%v err=%v", found, claim.cursor, err)
	}
	if err := r.process(f.ctx, claim); err != nil {
		t.Fatal(err)
	}
	tx = f.accountTx(f.ctx, f.adminDB)
	var valid bool
	f.scan(tx, `SELECT route_input_revision=7 AND route_input_sha256=$3 AND target_job_id IS NOT NULL
		AND NOT EXISTS(SELECT 1 FROM app.adopted_workout_coverage_refreshes WHERE account_id=$1)
		AND (SELECT count(*)=1 FROM app.jobs WHERE account_id=$1 AND kind='coverage_update_route')
		FROM app.workout_coverage_states WHERE account_id=$1 AND workout_id=$2`, []any{f.accountID, workout, digest}, &valid)
	f.commit(tx)
	if !valid {
		t.Fatal("fresh claim after NOWAIT did not consume exactly one unchanged adoption")
	}
	if worked, err := r.RunOnce(f.ctx); err != nil || worked {
		t.Fatalf("completed retry was reclaimed: worked=%v err=%v", worked, err)
	}
}

// Reuse only the synthetic OSM seeder and query assertions, not the older
// ingestion fixture's three-argument fence or its cleanup write contract.
func newAdoptionCoverageFixture(t *testing.T) (*coverageIntegrationFixture, uuid.UUID, []byte) {
	t.Helper()
	dsn := os.Getenv("ADOPTION_COVERAGE_WORKER_TEST_DATABASE_URL")
	osmDSN := os.Getenv("ADOPTION_COVERAGE_WORKER_TEST_OSM_DATABASE_URL")
	if dsn == "" || osmDSN == "" || os.Getenv("ADOPTION_COVERAGE_TEST_DISPOSABLE") != "1" {
		t.Skip("requires explicit disposable adoption Coverage worker app/OSM databases")
	}
	appConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil || !strings.HasPrefix(appConfig.ConnConfig.Database, "workouts_adoption_coverage_test_") ||
		(appConfig.ConnConfig.Host != "127.0.0.1" && appConfig.ConnConfig.Host != "localhost" && appConfig.ConnConfig.Host != "::1") {
		t.Fatal("worker adoption fixture requires a named loopback scratch database")
	}
	osmConfig, err := pgxpool.ParseConfig(osmDSN)
	if err != nil || osmConfig.ConnConfig.Host != appConfig.ConnConfig.Host || osmConfig.ConnConfig.Port != appConfig.ConnConfig.Port ||
		osmConfig.ConnConfig.Database != appConfig.ConnConfig.Database+"_osm" {
		t.Fatal("worker adoption fixture requires a paired scratch OSM database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	f := &coverageIntegrationFixture{t: t, ctx: ctx, accountID: uuid.New(), sourceID: uuid.New(), fileID: uuid.New(), typeID: uuid.New(), ingestID: uuid.New(), ingestLease: uuid.New()}
	pool := func(cfg *pgxpool.Config, role string) *pgxpool.Pool {
		cfg = cfg.Copy()
		if role != "" {
			cfg.ConnConfig.RuntimeParams["role"] = role
		}
		cfg.MaxConns = 4
		result, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(result.Close)
		return result
	}
	f.adminDB, f.workerDB, f.coverageDB = pool(appConfig, ""), pool(appConfig, "workouts_worker"), pool(appConfig, "workouts_coverage_worker")
	f.osmAdminDB, f.osmDB = pool(osmConfig, ""), pool(osmConfig, "workouts_coverage_worker")
	f.seedOSM()
	if _, err := f.osmAdminDB.Exec(ctx, `UPDATE osm_catalog.regions SET configured=true WHERE id=$1`, f.regionID); err != nil {
		t.Fatal(err)
	}
	workout, parent := uuid.New(), uuid.New()
	first := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	second := first.Add(time.Second)
	points := []coverageRoutePoint{{Sequence: 0, Timestamp: &first, Latitude: 37, Longitude: -122}, {Sequence: 1, Timestamp: &second, Latitude: 37.0001, Longitude: -122.0001}}
	digest := canonicalRouteInputSHA256(points)
	tx := f.accountTx(ctx, f.adminDB)
	f.exec(tx, `INSERT INTO app.accounts(id) VALUES($1)`, f.accountID)
	f.exec(tx, `INSERT INTO app.sources(id,account_id,display_name,canonical_display_name,type,config_envelope)
		VALUES($1,$2,$3,$3,'health-auto-export-local',$4)`, f.sourceID, f.accountID, f.sourceID.String(), []byte("synthetic"))
	f.exec(tx, `INSERT INTO app.jobs(id,account_id,kind,priority) VALUES($1,$2,'manual_ingest',80)`, parent, f.accountID)
	f.exec(tx, `INSERT INTO app.jobs(id,account_id,parent_job_id,kind,priority,parameters)
		VALUES($1,$2,$3,'manual_ingest_source',80,$4)`, f.ingestID, f.accountID, parent,
		fmt.Sprintf(`{"sourceId":"%s","generation":1,"mode":"incremental"}`, strings.ToUpper(strings.ReplaceAll(f.sourceID.String(), "-", ""))))
	f.exec(tx, `INSERT INTO app.job_config_snapshots(job_id,account_id,source_id,source_generation,config_envelope)
		VALUES($1,$2,$3,1,$4)`, f.ingestID, f.accountID, f.sourceID, []byte("synthetic"))
	f.exec(tx, `SET LOCAL session_replication_role='replica'`)
	f.exec(tx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes)
		VALUES($1,$2,$3,$4,'synthetic.json',1)`, f.fileID, f.accountID, f.sourceID, f.ingestID)
	f.exec(tx, `INSERT INTO app.workout_types(id,account_id,type_key,provider_label) VALUES($1,$2,'running','Running')`, f.typeID, f.accountID)
	f.exec(tx, `INSERT INTO app.workouts(id,account_id,source_id,source_file_id,workout_type_id,provider_id,content_sha256,provider_label,started_at,ended_at,local_start_date,provider_duration)
		VALUES($1,$2,$3,$4,$5,$1::uuid::text,$6,'Running','2026-01-01Z','2026-01-01T00:01:00Z','2026-01-01',60)`, workout, f.accountID, f.sourceID, f.fileID, f.typeID, digest[:])
	f.exec(tx, `INSERT INTO app.workout_route_points(account_id,workout_id,sequence,recorded_at,latitude,longitude)
		VALUES($1,$2,0,$3,37,-122),($1,$2,1,$4,37.0001,-122.0001)`, f.accountID, workout, first, second)
	f.exec(tx, `INSERT INTO app.workout_routes(account_id,workout_id,point_count,minimum_longitude,minimum_latitude,maximum_longitude,maximum_latitude,has_complete_altitude,route)
		VALUES($1,$2,2,-122.0001,37,-122,37.0001,false,ST_GeomFromText('MULTILINESTRING((-122 37,-122.0001 37.0001))',4326))`, f.accountID, workout)
	f.exec(tx, `INSERT INTO app.workout_coverage_states(account_id,workout_id,route_input_revision,route_input_sha256,readiness_state,map_data_ready_at)
		VALUES($1,$2,7,$3,'map_data_ready',clock_timestamp())`, f.accountID, workout, digest[:])
	f.exec(tx, `INSERT INTO app.workout_coverage_regions(account_id,workout_id,region_id,desired_osm_generation) VALUES($1,$2,$3,$4)`, f.accountID, workout, f.regionID, f.generation)
	f.exec(tx, `UPDATE app.workout_coverage_states SET processing_state='current',applied_route_input_revision=route_input_revision,
		applied_route_input_sha256=route_input_sha256,applied_rules_version='coverage-experimental-v1',
		applied_sampling_version='coverage-sampling-experimental-v1',applied_path_policy_version='coverage-path-policy-experimental-v82',
		applied_generations=jsonb_build_array(jsonb_build_object('regionId',$3::text,'generation',$4::bigint))
		WHERE account_id=$1 AND workout_id=$2`, f.accountID, workout, f.regionID, f.generation)
	f.commit(tx)
	tx = f.accountTx(ctx, f.workerDB)
	var claimed bool
	f.scan(tx, `SELECT app.claim_job($1,'adoption-worker-fixture',$2,interval '2 minutes')`, []any{f.ingestID, f.ingestLease}, &claimed)
	f.commit(tx)
	if !claimed {
		t.Fatal("fixture ingest claim rejected")
	}
	// This entire database is disposable; remove only this fixture's account
	// with trigger bypass, independently of the ingest job's terminal state.
	t.Cleanup(func() {
		tx := f.accountTx(ctx, f.adminDB)
		f.exec(tx, `SET LOCAL session_replication_role='replica'`)
		for _, table := range []string{"adopted_workout_coverage_refreshes", "workout_coverage_regions", "workout_coverage_states", "workout_routes", "workout_route_points", "coverage_route_job_contexts", "coverage_job_progress", "coverage_job_contexts", "job_config_snapshots", "job_source_contexts", "job_events", "job_logs", "jobs", "source_files", "workouts", "workout_types", "coverage_reconciliation_accounts", "source_sync_state", "source_objects", "sources", "account_data_generations"} {
			f.exec(tx, `DELETE FROM app.`+table+` WHERE account_id=$1`, f.accountID)
		}
		f.exec(tx, `DELETE FROM app.accounts WHERE id=$1`, f.accountID)
		f.commit(tx)
	})
	return f, workout, digest[:]
}
