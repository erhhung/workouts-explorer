package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/migrations"
	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/erhhung/workouts-explorer/internal/healthautoexport"
	"github.com/erhhung/workouts-explorer/internal/rclonesource"
	"github.com/erhhung/workouts-explorer/internal/sourceconfig"
	"github.com/erhhung/workouts-explorer/internal/sourcecrypto"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// No application DSN fallback. This suite migrates only a named, empty,
// explicitly disposable loopback PostGIS database with standard test roles.
func TestIngestAdoptionPostgreSQL(t *testing.T) {
	dsn := os.Getenv("PROJECT_INGEST_ADOPTION_TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("PROJECT_INGEST_ADOPTION_TEST_DISPOSABLE") != "1" {
		t.Skip("PROJECT_INGEST_ADOPTION_TEST_DATABASE_URL and PROJECT_INGEST_ADOPTION_TEST_DISPOSABLE=1 are required")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid adoption scratch database configuration")
	}
	if !strings.HasPrefix(config.Database, "workouts_ingest_adoption_test_") ||
		(config.Host != "127.0.0.1" && config.Host != "localhost" && config.Host != "::1") {
		t.Fatal("adoption tests require a named loopback scratch database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	setup := openWorkerTestPool(t, ctx, dsn)
	var empty bool
	if err := setup.QueryRow(ctx, `SELECT to_regnamespace('app') IS NULL AND to_regclass('public.goose_db_version') IS NULL`).Scan(&empty); err != nil || !empty {
		t.Fatal("adoption scratch database must be empty")
	}
	if _, err := setup.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS postgis`); err != nil {
		t.Fatal(err)
	}
	config.RuntimeParams["role"] = "workouts_migration"
	sqlDB := stdlib.OpenDB(*config)
	defer sqlDB.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, database.SupportedSchemaVersion); err != nil {
		t.Fatal(err)
	}
	workerConfig, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid adoption worker configuration")
	}
	workerConfig.ConnConfig.RuntimeParams["role"] = "workouts_worker"
	workerConfig.MaxConns = 4
	workerDB, err := pgxpool.NewWithConfig(ctx, workerConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(workerDB.Close)
	if !database.Ready(ctx, workerDB) {
		t.Fatalf("adoption requires the current coverage baseline, runtime %d", database.SupportedSchemaVersion)
	}
	if err := ConfigureFileSlotLimits(ctx, workerDB, 2, 4); err != nil {
		t.Fatal(err)
	}
	keys := testKeyring(t)
	for _, incomingType := range []string{sourceconfig.LocalType, sourceconfig.ICloudType} {
		for _, changed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/changed=%t", incomingType, changed), func(t *testing.T) {
				binary, remoteRoot, staging := fakeWorkerRclone(t)
				localRoot := t.TempDir()
				runner := testRunner(workerDB, keys, localRoot, io.Discard)
				runner.rcloneBinary, runner.stagingRoot = binary, staging
				oldType := sourceconfig.ICloudType
				if incomingType == sourceconfig.ICloudType {
					oldType = sourceconfig.LocalType
				}
				accountID := uuid.New()
				old := insertAdoptionSource(t, setup, keys, accountID, oldType, localRoot)
				incoming := insertAdoptionSource(t, setup, keys, accountID, incomingType, localRoot)
				pathFor := func(sourceType string) string {
					root := localRoot
					if sourceType == sourceconfig.ICloudType {
						root = filepath.Join(remoteRoot, "exports")
					}
					return filepath.Join(root, "HealthAutoExport-2026-08-01.json")
				}
				writeAdoptionExport(t, pathFor(oldType), "60", 2)
				enqueueAdoptionJob(t, setup, keys, old, oldType)
				assertRanIngest(t, runner, ctx)
				var workoutID, oldFileID uuid.UUID
				var createdAt time.Time
				var oldHash []byte
				if err := setup.QueryRow(ctx, `SELECT id,source_file_id,created_at,content_sha256 FROM app.workouts
					WHERE account_id=$1 AND source_id=$2`, accountID, old.sourceID).Scan(&workoutID, &oldFileID, &createdAt, &oldHash); err != nil {
					t.Fatal(err)
				}
				before := adoptionRawSnapshot(t, setup, workoutID)
				duration, points := "60", 2
				if changed {
					duration, points = "75.5", 3
				}
				writeAdoptionExport(t, pathFor(incomingType), duration, points)
				addAdoptionFreshWorkout(t, pathFor(incomingType))
				incomingBytes, err := os.ReadFile(pathFor(incomingType))
				if err != nil {
					t.Fatal(err)
				}
				document, err := healthautoexport.Parse(bytes.NewReader(incomingBytes))
				if err != nil {
					t.Fatal(err)
				}
				expectedHash := document.Workouts[0].ContentSHA256
				expectedChecksum := sha256.Sum256(incomingBytes)
				blocked := enqueueAdoptionJob(t, setup, keys, incoming, incomingType)
				blockedJob, err := runner.claimNext(ctx)
				if err != nil || blockedJob.id != blocked.childID {
					t.Fatalf("claim live-owner ingest: %v", err)
				}
				locker := beginWorkerTestAccount(t, setup, accountID)
				defer locker.Rollback(context.Background())
				if _, err := locker.Exec(ctx, `SELECT id FROM app.sources WHERE id=$1 FOR UPDATE`, old.sourceID); err != nil {
					t.Fatal(err)
				}
				_, executionErr := runner.executeIngest(ctx, blockedJob)
				_ = locker.Rollback(ctx)
				if !errors.Is(executionErr, errJobInterrupted) {
					t.Fatalf("source lock contention did not yield: %v", executionErr)
				}
				assertCountQuery(t, setup, 0, `SELECT count(*) FROM app.source_files WHERE job_id=$1`, blocked.childID)
				assertCountQuery(t, setup, 1, `SELECT count(*) FROM app.jobs WHERE id=$1 AND status='running' AND failure_code IS NULL`, blocked.childID)
				if _, err := runner.executeIngest(ctx, blockedJob); err != nil {
					t.Fatalf("live-owner retry: %v", err)
				}
				assertEmptyStaging(t, staging)
				tx := beginWorkerTestAccount(t, setup, accountID)
				defer tx.Rollback(context.Background())
				assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.workout_import_events WHERE job_id=$1 AND source_id=$2 AND kind='skipped_live_owner' AND workout_id=$3`, blocked.childID, incoming.sourceID, workoutID)
				assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.workouts WHERE id=$1 AND source_id=$2 AND source_file_id=$3`, workoutID, old.sourceID, oldFileID)
				assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.job_progress WHERE job_id=$1 AND workouts_created=1 AND workouts_updated=0 AND workouts_unchanged=1`, blocked.childID)
				assertCountQuery(t, tx, 1, `SELECT count(*) FROM app.job_progress WHERE job_id=$1 AND workouts_created_with_route=0 AND workouts_updated_with_route=0`, blocked.childID)
				assertCountQuery(t, tx, 0, `SELECT count(*) FROM app.adopted_workout_coverage_refreshes WHERE account_id=$1`, accountID)
				_ = tx.Rollback(ctx)
				if !bytes.Equal(before, adoptionRawSnapshot(t, setup, workoutID)) {
					t.Fatal("live owner raw/derived content changed")
				}
				skipped := enqueueAdoptionJob(t, setup, keys, incoming, incomingType)
				assertRanIngest(t, runner, ctx)
				assertCountQuery(t, setup, 1, `SELECT files_skipped FROM app.job_progress WHERE job_id=$1`, skipped.childID)
				if !workerCallBool(t, setup, accountID, `SELECT app.delete_source($1,$2)`, old.sourceID, uuid.New()) {
					t.Fatal("delete old owner")
				}

				// Commit a mixed adopted/unchanged file without progress, then replay
				// the same immutable manifest through the normal ingestion wrapper.
				adopted := enqueueAdoptionJob(t, setup, keys, incoming, incomingType)
				job, err := runner.claimNext(ctx)
				if err != nil || job.id != adopted.childID {
					t.Fatalf("claim adoption: %v", err)
				}
				var files []sourceFile
				var remote *rclonesource.Source
				if incomingType == sourceconfig.ICloudType {
					remote, err = rclonesource.New(ctx, binary, staging, syntheticICloudConfig())
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(func() { _ = runner.closeICloudSource(remote) })
					objects, err := remote.List(ctx)
					if err != nil {
						t.Fatal(err)
					}
					files = remoteSourceFiles(objects)
				} else {
					directory, err := sourceconfig.OpenDirectory(localRoot, []string{localRoot})
					if err != nil {
						t.Fatal(err)
					}
					files, err = discoverSourceFiles(directory)
					_ = directory.Close()
					if err != nil {
						t.Fatal(err)
					}
				}
				if err := runner.resolveCandidateActions(ctx, job, incoming.sourceID, files); err != nil || len(files) != 1 || files[0].action != "process" {
					t.Fatalf("deleted owner did not invalidate blocked metadata: files=%v err=%v", files, err)
				}
				if state, err := runner.recordFileManifest(ctx, job, files); err != nil || state != "created" {
					t.Fatalf("manifest=%s err=%v", state, err)
				}
				locker = beginWorkerTestAccount(t, setup, accountID)
				defer locker.Rollback(context.Background())
				if _, err := locker.Exec(ctx, `SELECT id FROM app.workouts WHERE id=$1 FOR UPDATE`, workoutID); err != nil {
					t.Fatal(err)
				}
				outcome := runner.processSourceFile(ctx, job, incoming.sourceID, localRoot, files[0], remote)
				_ = locker.Rollback(ctx)
				if !errors.Is(ingestCommitError(outcome.fatal), errLeaseLost) || outcome.failed || outcome.succeeded {
					t.Fatalf("workout contention was not transient: %+v err=%v", outcome, outcome.fatal)
				}
				assertCountQuery(t, setup, 1, `SELECT count(*) FROM app.source_files WHERE job_id=$1 AND state='discovered' AND failure_code IS NULL`, job.id)
				assertCountQuery(t, setup, 0, `SELECT count(*) FROM app.workout_import_events WHERE job_id=$1`, job.id)
				assertCountQuery(t, setup, 0, `SELECT count(*) FROM app.adopted_workout_coverage_refreshes WHERE account_id=$1`, accountID)
				outcome = runner.processSourceFile(ctx, job, incoming.sourceID, localRoot, files[0], remote)
				if outcome.fatal != nil || !outcome.succeeded || outcome.workouts.updated != 1 || outcome.workouts.unchanged != 1 {
					t.Fatalf("mixed file transaction=%+v err=%v", outcome, outcome.fatal)
				}
				if outcome.workouts.updatedWithRoute != 1 || outcome.workouts.createdWithRoute != 0 {
					t.Fatalf("adopted route snapshot=%+v", outcome.workouts)
				}
				if remote != nil && runner.closeICloudSource(remote) != nil {
					t.Fatal("private transport cleanup")
				}
				var refreshEvent uuid.UUID
				if err := setup.QueryRow(ctx, `SELECT event_id FROM app.adopted_workout_coverage_refreshes WHERE account_id=$1 AND workout_id=$2`, accountID, workoutID).Scan(&refreshEvent); err != nil {
					t.Fatal(err)
				}
				assertCountQuery(t, setup, 1, `SELECT count(*) FROM app.job_progress WHERE job_id=$1 AND workouts_updated=0 AND workouts_unchanged=0`, adopted.childID)
				if err := runner.resolveCandidateActions(ctx, job, incoming.sourceID, files); err != nil || files[0].action != "process" {
					t.Fatalf("frozen process action was replaced after commit: %v", err)
				}
				results, err := runner.executeIngest(ctx, job)
				if err != nil {
					t.Fatalf("adoption execution: %v", err)
				}
				if results.ingest.workoutsUpdated != 1 || results.ingest.workoutsUnchanged != 1 || results.ingest.WorkoutsProcessed != 2 {
					t.Fatalf("mixed progress=%+v", results.ingest)
				}
				recovered := newIngestResults()
				if err := runner.loadIngestProgress(ctx, job, &recovered); err != nil || recovered.workoutsUpdated != 1 || recovered.workoutsUnchanged != 1 || recovered.filesSucceeded != 1 {
					t.Fatalf("recovered mixed progress=%+v err=%v", recovered, err)
				}
				if err := runner.loadIngestProgress(ctx, job, &recovered); err != nil || recovered.WorkoutsProcessed != 2 {
					t.Fatalf("progress replay doubled counts: %+v err=%v", recovered, err)
				}
				if recovered.workoutsUpdatedWithRoute == nil || *recovered.workoutsUpdatedWithRoute != 1 ||
					recovered.workoutsCreatedWithRoute == nil || *recovered.workoutsCreatedWithRoute != 0 {
					t.Fatalf("adopted route progress replay=%+v", recovered)
				}
				for _, id := range []uuid.UUID{adopted.childID, adopted.parentID} {
					assertCountQuery(t, setup, 1, `SELECT count(*) FROM app.job_progress WHERE job_id=$1 AND workouts_created_with_route=0 AND workouts_updated_with_route=1`, id)
				}
				var currentID, currentSource, currentFile uuid.UUID
				var currentCreated time.Time
				var currentHash []byte
				if err := setup.QueryRow(ctx, `SELECT id,source_id,source_file_id,created_at,content_sha256 FROM app.workouts WHERE id=$1`, workoutID).
					Scan(&currentID, &currentSource, &currentFile, &currentCreated, &currentHash); err != nil {
					t.Fatal(err)
				}
				if currentID != workoutID || currentSource != incoming.sourceID || currentFile == oldFileID || !currentCreated.Equal(createdAt) || bytes.Equal(currentHash, oldHash) == changed {
					t.Fatal("adoption lost stable identity, current provenance, or real digest")
				}
				if !bytes.Equal(currentHash, expectedHash[:]) {
					t.Fatal("adoption stored a synthetic rather than canonical content digest")
				}
				if !changed && !bytes.Equal(before, adoptionRawSnapshot(t, setup, workoutID)) {
					t.Fatal("unchanged adoption rewrote raw/derived rows")
				}
				assertSyntheticWorkoutState(t, setup, incoming, duration, points)
				assertCountQuery(t, setup, 1, `SELECT count(*) FROM app.workout_aggregates WHERE workout_id=$1 AND metric='distance' AND value=$2::numeric AND unit='km'`, workoutID, duration)
				assertCountQuery(t, setup, 1, `SELECT count(*) FROM app.source_files WHERE id=$1 AND checksum_sha256=$2`, currentFile, expectedChecksum[:])
				assertCountQuery(t, setup, 1, `SELECT count(*) FROM app.adopted_workout_coverage_refreshes WHERE account_id=$1 AND workout_id=$2 AND source_id=$3 AND source_file_id=$4`, accountID, workoutID, incoming.sourceID, currentFile)
				assertCountQuery(t, setup, 1, `SELECT count(*) FROM app.adopted_workout_coverage_refreshes WHERE account_id=$1 AND workout_id=$2 AND event_id=$3`, accountID, workoutID, refreshEvent)
				assertCountQuery(t, setup, 1, `SELECT count(*) FROM app.workout_import_events WHERE job_id=$1 AND kind='adopted' AND source_file_id=$2`, adopted.childID, currentFile)
				assertCountQuery(t, setup, 0, `SELECT count(*) FROM app.ingest_file_slots WHERE job_id=$1`, adopted.childID)
				assertEmptyStaging(t, staging)

				requester := uuid.New()
				tx = beginWorkerTestAccount(t, setup, accountID)
				defer tx.Rollback(context.Background())
				if _, err := tx.Exec(ctx, `INSERT INTO app.authentication_principals
					(id,role,username,canonical_username,email,canonical_email,canonicalization_version,password_hash,full_name)
					VALUES($1,'user',$2,$2,$3,$3,1,'synthetic-hash','Adoption Fixture')`, requester, requester.String(), requester.String()+"@example.test"); err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(ctx, `INSERT INTO app.users(principal_id,account_id) VALUES($1,$2)`, requester, accountID); err != nil {
					t.Fatal(err)
				}
				if _, err := tx.Exec(ctx, `SELECT * FROM app.enqueue_workout_deletion($1,$2)`, workoutID, requester); err != nil {
					t.Fatal(err)
				}
				if err := tx.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				// Deletion has priority over source jobs. Finish the real deletion
				// before checking cross-source suppression by its durable tombstone.
				assertRanIngest(t, runner, ctx)
				assertCountQuery(t, setup, 0, `SELECT count(*) FROM app.workouts WHERE id=$1`, workoutID)
				third := insertAdoptionSource(t, setup, keys, accountID, incomingType, localRoot)
				suppressed := enqueueAdoptionJob(t, setup, keys, third, incomingType)
				assertRanIngest(t, runner, ctx)
				assertCountQuery(t, setup, 0, `SELECT count(*) FROM app.workout_import_events WHERE job_id=$1 AND workout_id=$2`, suppressed.childID, workoutID)
				assertCountQuery(t, setup, 1, `SELECT count(*) FROM app.job_progress WHERE job_id=$1 AND workouts_created_with_route=0 AND workouts_updated_with_route=0`, suppressed.childID)
				assertCountQuery(t, setup, 0, `SELECT count(*) FROM app.workouts WHERE source_id=$1`, third.sourceID)
				assertCountQuery(t, setup, 0, `SELECT count(*) FROM app.workouts WHERE id=$1`, workoutID)
				assertCountQuery(t, setup, 1, `SELECT count(*) FROM app.workout_deletion_targets WHERE workout_id=$1 AND source_id=$2 AND state='completed'`, workoutID, incoming.sourceID)
				assertEmptyStaging(t, staging)
			})
		}
		t.Run(incomingType+"/fallback remains source local", func(t *testing.T) {
			binary, remoteRoot, staging := fakeWorkerRclone(t)
			localRoot := t.TempDir()
			runner := testRunner(workerDB, keys, localRoot, io.Discard)
			runner.rcloneBinary, runner.stagingRoot = binary, staging
			payload := []byte(strings.Replace(minimalExport, `"id":"provider-1",`, "", 1))
			for _, root := range []string{localRoot, filepath.Join(remoteRoot, "exports")} {
				if err := os.WriteFile(filepath.Join(root, "HealthAutoExport-2026-08-01.json"), payload, 0600); err != nil {
					t.Fatal(err)
				}
			}
			accountID := uuid.New()
			old := insertAdoptionSource(t, setup, keys, accountID, sourceconfig.LocalType, localRoot)
			enqueueAdoptionJob(t, setup, keys, old, sourceconfig.LocalType)
			assertRanIngest(t, runner, ctx)
			for index := range 2 {
				if index == 1 && !workerCallBool(t, setup, accountID, `SELECT app.delete_source($1,$2)`, old.sourceID, uuid.New()) {
					t.Fatal("delete fallback owner")
				}
				incoming := insertAdoptionSource(t, setup, keys, accountID, incomingType, localRoot)
				job := enqueueAdoptionJob(t, setup, keys, incoming, incomingType)
				assertRanIngest(t, runner, ctx)
				assertCountQuery(t, setup, 1, `SELECT count(*) FROM app.workout_import_events WHERE job_id=$1 AND kind='created'`, job.childID)
			}
			assertCountQuery(t, setup, 3, `SELECT count(*) FROM app.workouts WHERE account_id=$1 AND provider_id IS NULL`, accountID)
			assertCountQuery(t, setup, 0, `SELECT count(*) FROM app.adopted_workout_coverage_refreshes WHERE account_id=$1`, accountID)
			assertEmptyStaging(t, staging)
		})
	}
}

func insertAdoptionSource(t *testing.T, db *pgxpool.Pool, keys *sourcecrypto.Keyring, accountID uuid.UUID, sourceType, root string) ingestSourceFixture {
	t.Helper()
	fixture := ingestSourceFixture{accountID: accountID, sourceID: uuid.New(), root: root}
	canonical := []byte(fmt.Sprintf(`{"version":1,"path":%q}`, root))
	if sourceType == sourceconfig.ICloudType {
		_, canonical, _ = sourceconfig.CanonicalizeICloud(syntheticICloudConfig())
	}
	envelope, err := keys.Encrypt(sourcecrypto.Context{Purpose: sourcecrypto.SourceConfig, AccountID: accountID, RecordID: fixture.sourceID}, canonical)
	if err != nil {
		t.Fatal(err)
	}
	tx := beginWorkerTestAccount(t, db, accountID)
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(context.Background(), `INSERT INTO app.accounts(id) VALUES($1) ON CONFLICT DO NOTHING`, accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(context.Background(), `INSERT INTO app.sources(id,account_id,display_name,canonical_display_name,type,status,config_envelope)
		VALUES($1,$2,$3,$3,$4,'connected',$5)`, fixture.sourceID, accountID, "adoption-"+fixture.sourceID.String(), sourceType, envelope); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(context.Background()); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func enqueueAdoptionJob(t *testing.T, db *pgxpool.Pool, keys *sourcecrypto.Keyring, fixture ingestSourceFixture, sourceType string) ingestJobFixture {
	t.Helper()
	if sourceType == sourceconfig.ICloudType {
		return enqueueICloudWorkerJob(t, db, keys, fixture, ingestIncremental)
	}
	return enqueueIncrementalIngestJob(t, db, keys, fixture)
}

func addAdoptionFreshWorkout(t *testing.T, path string) {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Data struct {
			Workouts []json.RawMessage `json:"workouts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatal(err)
	}
	document.Data.Workouts = append(document.Data.Workouts, json.RawMessage(strings.Replace(minimalExport[len(`{"data":{"workouts":[`):len(minimalExport)-3], "provider-1", "fresh-provider", 1)))
	encoded, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
}

func writeAdoptionExport(t *testing.T, path, duration string, points int) {
	t.Helper()
	writeSyntheticExport(t, path, duration, points)
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	encoded = bytes.Replace(encoded, []byte(`"duration":`), []byte(fmt.Sprintf(`"distance":{"qty":%s,"units":"km"},"duration":`, duration)), 1)
	if err := os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
}

func adoptionRawSnapshot(t *testing.T, db *pgxpool.Pool, workoutID uuid.UUID) []byte {
	t.Helper()
	var snapshot []byte
	if err := db.QueryRow(context.Background(), `SELECT jsonb_build_object(
		'points',(SELECT jsonb_agg(jsonb_build_array(to_jsonb(p),p.xmin::text) ORDER BY sequence) FROM app.workout_route_points p WHERE workout_id=$1),
		'aggregates',(SELECT jsonb_agg(jsonb_build_array(to_jsonb(a),a.xmin::text) ORDER BY metric) FROM app.workout_aggregates a WHERE workout_id=$1),
		'route',(SELECT to_jsonb(r) FROM app.workout_routes r WHERE workout_id=$1))`, workoutID).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}
