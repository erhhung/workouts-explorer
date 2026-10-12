package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/healthautoexport"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestIngestCanonicalActivityLabelsPostgreSQL(t *testing.T) {
	if os.Getenv("COVERAGE_ACCEPTANCE_DISPOSABLE") != "true" {
		t.Skip("requires COVERAGE_ACCEPTANCE_DISPOSABLE=true and guarded disposable database URLs")
	}
	var destination string
	for _, name := range []string{"MIGRATION_DATABASE_URL", "WORKER_DATABASE_URL"} {
		u, err := url.Parse(os.Getenv(name))
		if err != nil || u.Scheme != "postgresql" || (u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1") ||
			(u.Port() != "15432" && u.Port() != "5432") || (u.Path != "/acceptance_test" && u.Path != "/coverage_test") ||
			(u.RawQuery != "" && u.RawQuery != "sslmode=disable") || u.Fragment != "" {
			t.Fatalf("refusing non-disposable %s (URL omitted)", name)
		}
		if destination != "" && destination != u.Host+u.Path {
			t.Fatal("database URLs must address the same disposable database")
		}
		destination = u.Host + u.Path
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	setupDB := openWorkerTestPool(t, ctx, os.Getenv("MIGRATION_DATABASE_URL"))
	workerDB := openWorkerTestPool(t, ctx, os.Getenv("WORKER_DATABASE_URL"))
	for _, pool := range []struct {
		role string
		db   interface {
			QueryRow(context.Context, string, ...any) pgx.Row
		}
	}{{"workouts_migration", setupDB}, {"workouts_worker", workerDB}} {
		var role string
		if err := pool.db.QueryRow(ctx, `SELECT current_user`).Scan(&role); err != nil || role != pool.role {
			t.Fatalf("expected role %s, got %s: %v", pool.role, role, err)
		}
	}
	keys := testKeyring(t)
	root := t.TempDir()
	labels := []string{"Outdoor Running", "Outdoor Walking", "Outdoor Cycling", "IndoorRunning", "IndoorWalking", "IndoorBike", "Cycling", "Private Unknown"}
	var rawWorkouts []map[string]any
	for _, label := range labels {
		for _, id := range []string{"", "opaque/" + label, uuid.NewString()} {
			rawWorkouts = append(rawWorkouts, map[string]any{
				"id": id, "name": label, "start": "2026-08-01 12:00:00 +0000", "end": "2026-08-01 12:02:00 +0000", "duration": 120,
				"distance": map[string]any{"qty": 0.1, "units": "km"},
				"route": []map[string]any{
					{"timestamp": "2026-08-01 12:00:00 +0000", "latitude": 37.0, "longitude": -122.0},
					{"timestamp": "2026-08-01 12:01:00 +0000", "latitude": 37.001, "longitude": -122.001},
				},
			})
		}
	}
	raw, err := json.Marshal(map[string]any{"data": map[string]any{"workouts": rawWorkouts}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "HealthAutoExport-2026-08-01.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	document, err := healthautoexport.Parse(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	fixture := insertIngestSource(t, setupDB, root)
	runner := testRunner(workerDB, keys, root, io.Discard)
	runner.workerID = "naming-fixture-" + fixture.accountID.String()
	principalID := uuid.New()
	ownerTx, err := beginAccount(ctx, setupDB, fixture.accountID)
	if err != nil {
		t.Fatal(err)
	}
	defer ownerTx.Rollback(ctx)
	name := "naming-" + principalID.String()
	if _, err := ownerTx.Exec(ctx, `INSERT INTO app.authentication_principals(id,role,username,canonical_username,email,canonical_email,
		canonicalization_version,password_hash,full_name) VALUES($1,'user',$2,$2,$3,$3,1,'test-only-hash','Naming Test')`, principalID, name, name+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := ownerTx.Exec(ctx, `INSERT INTO app.users(principal_id,account_id) VALUES($1,$2)`, principalID, fixture.accountID); err != nil {
		t.Fatal(err)
	}
	if err := ownerTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var claimed []claimedJob
	var durableImport bool
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		for _, job := range claimed {
			tx, err := beginAccount(cleanup, workerDB, fixture.accountID)
			if err != nil {
				t.Error("cleanup transaction:", err)
				return
			}
			var finished bool
			err = tx.QueryRow(cleanup, `SELECT app.finish_job($1,$2,$3,'cancelled')`, job.id, runner.workerID, job.lease).Scan(&finished)
			if err == nil {
				err = tx.Commit(cleanup)
			}
			_ = tx.Rollback(cleanup)
			if err != nil {
				t.Error("cleanup job lease:", err)
				return
			}
		}
		if durableImport {
			// Full wrapper executions commit protected import history. Purge
			// workouts through the owner deletion API function and worker, then
			// archive this fixture instead of deleting persistent tombstones.
			tx, err := beginAccount(cleanup, setupDB, fixture.accountID)
			if err != nil {
				t.Error("cleanup deletion enqueue transaction:", err)
				return
			}
			var deletionID uuid.UUID
			err = tx.QueryRow(cleanup, `SELECT job_id FROM app.enqueue_workout_range_deletion('2026-08-01','2026-08-01',$1)`, principalID).Scan(&deletionID)
			if err == nil {
				err = tx.Commit(cleanup)
			}
			_ = tx.Rollback(cleanup)
			if err != nil && err != pgx.ErrNoRows {
				t.Error("cleanup owner deletion enqueue:", err)
				return
			}
			if err == nil {
				deletion := claimedJob{id: deletionID, accountID: fixture.accountID, kind: "workout_deletion", lease: uuid.New()}
				tx, err = beginAccount(cleanup, workerDB, fixture.accountID)
				if err != nil {
					t.Error("cleanup deletion claim transaction:", err)
					return
				}
				var ok bool
				err = tx.QueryRow(cleanup, `SELECT app.claim_job($1,$2,$3,interval '5 minutes')`, deletion.id, runner.workerID, deletion.lease).Scan(&ok)
				if err == nil && ok {
					err = tx.Commit(cleanup)
				}
				_ = tx.Rollback(cleanup)
				if err != nil || !ok {
					t.Errorf("cleanup explicit deletion claim=%t: %v", ok, err)
					return
				}
				if err := runner.executeWorkoutDeletion(cleanup, deletion); err != nil {
					t.Error("cleanup deletion execution:", err)
					return
				}
			}
			tx, err = beginAccount(cleanup, setupDB, fixture.accountID)
			if err != nil {
				t.Error("cleanup archive transaction:", err)
				return
			}
			defer tx.Rollback(cleanup)
			var retained int
			if err := tx.QueryRow(cleanup, `SELECT (SELECT count(*) FROM app.workouts WHERE account_id=$1)
				+(SELECT count(*) FROM app.workout_import_events WHERE account_id=$1)
				+(SELECT count(*) FROM app.workout_deletion_targets WHERE account_id=$1 AND state<>'completed')
				+(SELECT count(*) FROM app.jobs WHERE account_id=$1 AND status IN ('queued','running'))`, fixture.accountID).Scan(&retained); err != nil || retained != 0 {
				t.Errorf("cleanup retained workouts/history/unfinished jobs: %d, %v", retained, err)
				return
			}
			if _, err := tx.Exec(cleanup, `UPDATE app.accounts SET state='deleted' WHERE id=$1`, fixture.accountID); err != nil {
				t.Error("cleanup archive account:", err)
				return
			}
			if _, err := tx.Exec(cleanup, `UPDATE app.authentication_principals SET disabled_at=transaction_timestamp() WHERE id=$1`, principalID); err != nil {
				t.Error("cleanup disable fixture owner:", err)
				return
			}
			if err := tx.Commit(cleanup); err != nil {
				t.Error("cleanup archive commit:", err)
			} else {
				t.Log("own fixture workouts/history purged; terminal metadata/tombstones retained in archived scratch account")
			}
			return
		}
		tx, err := beginAccount(cleanup, setupDB, fixture.accountID)
		if err != nil {
			t.Error("cleanup account transaction:", err)
			return
		}
		defer tx.Rollback(cleanup)
		// Domain test transactions roll back before cleanup. Never delete
		// committed import history or persistent deletion tombstones.
		var retained int
		if err := tx.QueryRow(cleanup, `SELECT (SELECT count(*) FROM app.workouts WHERE account_id=$1)
			+(SELECT count(*) FROM app.source_files WHERE account_id=$1)
			+(SELECT count(*) FROM app.workout_deletion_targets WHERE account_id=$1)
			+(SELECT count(*) FROM app.jobs WHERE account_id=$1 AND status='running')`, fixture.accountID).Scan(&retained); err != nil || retained != 0 {
			t.Errorf("refusing cleanup with retained domain rows/live jobs: %d, %v", retained, err)
			return
		}
		for _, query := range []string{`DELETE FROM app.source_objects WHERE account_id=$1`, `DELETE FROM app.jobs WHERE account_id=$1`, `DELETE FROM app.sources WHERE account_id=$1`, `DELETE FROM app.users WHERE account_id=$1`} {
			if _, err := tx.Exec(cleanup, query, fixture.accountID); err != nil {
				t.Error("cleanup own fixture:", err)
				return
			}
		}
		if _, err := tx.Exec(cleanup, `DELETE FROM app.authentication_principals WHERE id=$1`, principalID); err != nil {
			t.Error("cleanup own principal:", err)
			return
		}
		if _, err := tx.Exec(cleanup, `DELETE FROM app.accounts WHERE id=$1`, fixture.accountID); err != nil {
			t.Error("cleanup own account:", err)
			return
		}
		if err := tx.Commit(cleanup); err != nil {
			t.Error("cleanup commit:", err)
			return
		}
		var remaining int
		if err := setupDB.QueryRow(cleanup, `SELECT count(*) FROM app.accounts WHERE id=$1`, fixture.accountID).Scan(&remaining); err != nil || remaining != 0 {
			t.Errorf("account cleanup not verified: %d, %v", remaining, err)
		} else {
			t.Log("own fixture account/job cleanup verified")
		}
	})
	claim := func(f ingestJobFixture) claimedJob {
		t.Helper()
		job := claimedJob{id: f.childID, accountID: fixture.accountID, kind: "manual_ingest_source", lease: uuid.New()}
		tx, err := beginAccount(ctx, workerDB, fixture.accountID)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		var ok bool
		if err := tx.QueryRow(ctx, `SELECT app.claim_job($1,$2,$3,interval '5 minutes')`, job.id, runner.workerID, job.lease).Scan(&ok); err != nil || !ok {
			t.Fatalf("explicit fixture claim=%t err=%v", ok, err)
		}
		var parameters []byte
		if err := tx.QueryRow(ctx, `SELECT parameters FROM app.jobs WHERE id=$1`, job.id).Scan(&parameters); err != nil {
			t.Fatal(err)
		}
		job.mode, job.startDate, job.endDate, job.paramsErr = decodeIngestParameters(parameters)
		if job.paramsErr != nil {
			t.Fatal(job.paramsErr)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		claimed = append(claimed, job)
		return job
	}
	first := enqueueIngestJob(t, setupDB, keys, fixture)
	job := claim(first)
	insertFile := func(t *testing.T, tx pgx.Tx, name string) uuid.UUID {
		t.Helper()
		id := uuid.New()
		if _, err := tx.Exec(ctx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes)
			VALUES($1,$2,$3,$4,$5,$6)`, id, fixture.accountID, fixture.sourceID, job.id, name, len(raw)); err != nil {
			t.Fatal(err)
		}
		return id
	}
	t.Run("worker-role creation legacy repair and unchanged reimport", func(t *testing.T) {
		tx, _, err := runner.beginIngestWrite(ctx, job)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		firstFile := insertFile(t, tx, "original.json")
		for _, workout := range document.Workouts {
			kind, err := runner.persistWorkout(ctx, tx, job, fixture.sourceID, firstFile, workout)
			if err != nil || kind != "created" {
				t.Fatalf("create=%s err=%v", kind, err)
			}
			var storedLabel, storedTypeLabel, typeKey string
			var hash, fallback []byte
			if err := tx.QueryRow(ctx, `SELECT w.provider_label,wt.provider_label,wt.type_key,w.content_sha256,w.fallback_sha256
				FROM app.workouts w JOIN app.workout_types wt ON wt.id=w.workout_type_id
				WHERE w.source_id=$1 AND wt.type_key=$2 AND w.provider_id IS NOT DISTINCT FROM NULLIF($3,'')`, fixture.sourceID, workout.TypeKey, workout.ProviderID).
				Scan(&storedLabel, &storedTypeLabel, &typeKey, &hash, &fallback); err != nil {
				t.Fatal(err)
			}
			canonical := healthautoexport.CanonicalActivityLabel(workout.ProviderLabel)
			if storedLabel != canonical || storedTypeLabel != canonical || typeKey != workout.TypeKey || !bytes.Equal(hash, workout.ContentSHA256[:]) ||
				(workout.ProviderID == "" && !bytes.Equal(fallback, workout.FallbackSHA256[:])) {
				t.Fatalf("created display/key/hash/fallback=%q/%q/%s/%x/%x", storedLabel, storedTypeLabel, typeKey, hash, fallback)
			}
		}
		for _, label := range labels {
			if _, err := tx.Exec(ctx, `UPDATE app.workouts SET provider_label=$3 WHERE source_id=$1 AND workout_type_id IN
				(SELECT id FROM app.workout_types WHERE type_key=$2)`, fixture.sourceID, healthautoexport.NormalizedTypeKey(label), label); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `UPDATE app.workout_types SET provider_label=$2 WHERE type_key=$1`, healthautoexport.NormalizedTypeKey(label), label); err != nil {
				t.Fatal(err)
			}
		}
		snapshot := func() []byte {
			var result []byte
			if err := tx.QueryRow(ctx, `SELECT jsonb_agg(jsonb_build_object(
				'workout',to_jsonb(w)-'provider_label'-'source_file_id'-'updated_at',
				'type',to_jsonb(wt)-'provider_label'-'updated_at',
				'aggregates',(SELECT jsonb_agg(to_jsonb(a) ORDER BY a.metric) FROM app.workout_aggregates a WHERE a.workout_id=w.id),
				'points',(SELECT jsonb_agg(to_jsonb(p) ORDER BY p.sequence) FROM app.workout_route_points p WHERE p.workout_id=w.id),
				'route',(SELECT jsonb_build_object('accountId',r.account_id,'workoutId',r.workout_id)
					FROM app.workout_routes r WHERE r.workout_id=w.id),
				'coverage',(SELECT to_jsonb(state) FROM (SELECT c.route_input_revision,c.route_input_sha256,
					c.readiness_state,c.processing_state,c.applied_route_input_revision,c.applied_rules_version,
					c.applied_sampling_version,c.applied_path_policy_version,c.applied_generations,
					c.processing_started_at,c.processing_finished_at,c.processing_failure_code,c.processing_failure_summary,
					c.target_job_id,c.target_revision,c.target_rules_version,c.target_sampling_version,
					c.target_path_policy_version,c.target_generations FROM app.workout_coverage_states c WHERE c.workout_id=w.id) state)) ORDER BY w.id)
				FROM app.workouts w JOIN app.workout_types wt ON wt.id=w.workout_type_id WHERE w.source_id=$1`, fixture.sourceID).Scan(&result); err != nil {
				t.Fatal(err)
			}
			return result
		}
		before := snapshot()
		repairFile := insertFile(t, tx, "reimport.json")
		for _, phase := range []string{"legacy aliases", "already canonical"} {
			t.Run(phase, func(t *testing.T) {
				for _, workout := range document.Workouts {
					kind, err := runner.persistWorkout(ctx, tx, job, fixture.sourceID, repairFile, workout)
					if err != nil || kind != "matched_unchanged" {
						t.Fatalf("repair=%s err=%v", kind, err)
					}
				}
				if !bytes.Equal(before, snapshot()) {
					t.Fatal("display repair changed UUID, raw key, content/fallback hashes, metrics, or route/coverage state")
				}
				assertCountQuery(t, tx, len(rawWorkouts), `SELECT count(*) FROM app.workouts WHERE source_id=$1`, fixture.sourceID)
				for _, label := range labels {
					assertCountQuery(t, tx, 3, `SELECT count(*) FROM app.workouts w JOIN app.workout_types wt ON wt.id=w.workout_type_id
						WHERE w.source_id=$1 AND wt.type_key=$2 AND wt.provider_label=$3 AND w.provider_label=$3`, fixture.sourceID,
						healthautoexport.NormalizedTypeKey(label), healthautoexport.CanonicalActivityLabel(label))
				}
			})
		}
		assertCountQuery(t, tx, 2*len(rawWorkouts), `SELECT count(*) FROM app.workout_import_events WHERE source_file_id=$1 AND kind='matched_unchanged'`, repairFile)
		assertCountQuery(t, tx, 0, `SELECT count(*) FROM app.workout_import_events WHERE source_file_id=$1 AND kind<>'matched_unchanged'`, repairFile)
		assertCountQuery(t, tx, len(labels), `SELECT count(*) FROM app.workouts WHERE source_id=$1 AND provider_id IS NULL
			AND fallback_fingerprint_version='health-auto-export-fallback-v2'`, fixture.sourceID)
		// Validate deferred capabilities while the exact claiming job is live,
		// then roll back domain history rather than bypassing its delete guards.
		if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("deletion suppression retains raw provider identity", func(t *testing.T) {
		tx, err := beginAccount(ctx, setupDB, fixture.accountID)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		var sourceID uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT source_id FROM app.fence_ingest_job($1,$2,$3)`, job.id, runner.workerID, job.lease).Scan(&sourceID); err != nil || sourceID != fixture.sourceID {
			t.Fatalf("suppression fixture fence=%s err=%v", sourceID, err)
		}
		fileID := insertFile(t, tx, "suppression.json")
		for _, workout := range document.Workouts {
			kind, err := runner.persistWorkout(ctx, tx, job, fixture.sourceID, fileID, workout)
			if err != nil || kind != "created" {
				t.Fatalf("suppression seed=%s err=%v", kind, err)
			}
			var workoutID uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT id FROM app.workouts WHERE source_id=$1 AND
				provider_id IS NOT DISTINCT FROM NULLIF($2,'') AND workout_type_id IN
				(SELECT id FROM app.workout_types WHERE type_key=$3)`, fixture.sourceID, workout.ProviderID, workout.TypeKey).Scan(&workoutID); err != nil {
				t.Fatal(err)
			}
			// The source worker can read route IDs but not every summary column.
			// The migration-role fixture also verifies full geometry/split state
			// without changing grants or bypassing the live ingest fence.
			var before, after []byte
			if err := tx.QueryRow(ctx, `SELECT to_jsonb(r) FROM app.workout_routes r WHERE workout_id=$1`, workoutID).Scan(&before); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `UPDATE app.workouts SET provider_label=$2 WHERE id=$1`, workoutID, workout.ProviderLabel); err != nil {
				t.Fatal(err)
			}
			kind, err = runner.persistWorkout(ctx, tx, job, fixture.sourceID, fileID, workout)
			if err != nil || kind != "matched_unchanged" {
				t.Fatalf("summary-state repair=%s err=%v", kind, err)
			}
			if err := tx.QueryRow(ctx, `SELECT to_jsonb(r) FROM app.workout_routes r WHERE workout_id=$1`, workoutID).Scan(&after); err != nil || !bytes.Equal(before, after) {
				t.Fatalf("display repair changed route geometry/summary/splits: %v", err)
			}
			var deletionID uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT job_id FROM app.enqueue_workout_deletion($1,$2)`, workoutID, principalID).Scan(&deletionID); err != nil {
				t.Fatal(err)
			}
			kind, err = runner.persistWorkout(ctx, tx, job, fixture.sourceID, fileID, workout)
			if err != nil || kind != "suppressed" {
				t.Fatalf("raw identity suppression=%s err=%v", kind, err)
			}
		}
		assertCountQuery(t, tx, len(rawWorkouts), `SELECT count(*) FROM app.workouts WHERE source_id=$1`, fixture.sourceID)
		assertCountQuery(t, tx, len(rawWorkouts), `SELECT count(*) FROM app.workout_import_events WHERE job_id=$1 AND kind='created'`, job.id)
		assertCountQuery(t, tx, len(rawWorkouts), `SELECT count(*) FROM app.workout_import_events WHERE job_id=$1 AND kind='matched_unchanged'`, job.id)
		assertCountQuery(t, tx, 0, `SELECT count(*) FROM app.workout_import_events WHERE job_id=$1 AND kind NOT IN ('created','matched_unchanged')`, job.id)
		if _, err := tx.Exec(ctx, `SET CONSTRAINTS ALL IMMEDIATE`); err != nil {
			t.Fatal(err)
		}
	})
	runWrapper := func(t *testing.T, f ingestJobFixture, current claimedJob, created, unchanged, skipped int) {
		t.Helper()
		tx := beginWorkerTestAccount(t, workerDB, fixture.accountID)
		var live bool
		if err := tx.QueryRow(ctx, `SELECT status='running' AND worker_id=$2 AND lease_token=$3
			AND lease_expires_at>clock_timestamp() FROM app.jobs WHERE id=$1`, current.id, runner.workerID, current.lease).Scan(&live); err != nil || !live {
			_ = tx.Rollback(ctx)
			t.Fatalf("wrapper must start with exact live lease: %t, %v", live, err)
		}
		_ = tx.Rollback(ctx)
		durableImport = true
		execution, err := runner.executeIngest(ctx, current)
		if err != nil {
			t.Fatalf("normal executeIngest wrapper: %v", err)
		}
		results := execution.ingest
		if results == nil || results.workoutsCreated != created || results.workoutsUnchanged != unchanged || results.workoutsUpdated != 0 ||
			results.filesSkipped != skipped || results.filesFailed != 0 || results.WorkoutsIngested != created {
			t.Fatalf("wrapper results=%+v", results)
		}
		tx = beginWorkerTestAccount(t, workerDB, fixture.accountID)
		defer tx.Rollback(ctx)
		var status string
		var worker *string
		var lease *uuid.UUID
		var expires, terminal *time.Time
		if err := tx.QueryRow(ctx, `SELECT status,worker_id,lease_token,lease_expires_at,terminal_at FROM app.jobs WHERE id=$1`, current.id).
			Scan(&status, &worker, &lease, &expires, &terminal); err != nil || status != "succeeded" || worker != nil || lease != nil || expires != nil || terminal == nil {
			t.Fatalf("terminal lease state=%s/%v/%v/%v/%v err=%v", status, worker, lease, expires, terminal, err)
		}
		metadata := beginWorkerTestAccount(t, setupDB, fixture.accountID)
		defer metadata.Rollback(ctx)
		assertTerminalIngest(t, metadata, f, "succeeded", "succeeded")
		assertCountQuery(t, tx, len(rawWorkouts), `SELECT count(*) FROM app.workouts WHERE source_id=$1`, fixture.sourceID)
		for _, label := range labels {
			assertCountQuery(t, tx, 3, `SELECT count(*) FROM app.workouts w JOIN app.workout_types wt ON wt.id=w.workout_type_id
				WHERE w.source_id=$1 AND wt.type_key=$2 AND w.provider_label=$3 AND wt.provider_label=$3`, fixture.sourceID,
				healthautoexport.NormalizedTypeKey(label), healthautoexport.CanonicalActivityLabel(label))
		}
	}
	graphSnapshot := func(t *testing.T) []byte {
		t.Helper()
		tx := beginWorkerTestAccount(t, setupDB, fixture.accountID)
		defer tx.Rollback(ctx)
		var result []byte
		if err := tx.QueryRow(ctx, `SELECT jsonb_agg(jsonb_build_object(
			'workout',to_jsonb(w)-'source_file_id'-'updated_at'-'timezone_name'-'timezone_source'
				-'timezone_reference_workout_id'-'timezone_dataset_release',
			'type',to_jsonb(wt)-'updated_at',
			'aggregates',(SELECT jsonb_agg(to_jsonb(a) ORDER BY a.metric) FROM app.workout_aggregates a WHERE a.workout_id=w.id),
			'points',(SELECT jsonb_agg(to_jsonb(p) ORDER BY p.sequence) FROM app.workout_route_points p WHERE p.workout_id=w.id),
			'route',(SELECT to_jsonb(r) FROM app.workout_routes r WHERE r.workout_id=w.id),
			'coverage',(SELECT to_jsonb(c) FROM app.workout_coverage_states c WHERE c.workout_id=w.id)) ORDER BY w.id)
			FROM app.workouts w JOIN app.workout_types wt ON wt.id=w.workout_type_id WHERE w.source_id=$1`, fixture.sourceID).Scan(&result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	if !t.Run("fresh canonical import through executeIngest", func(t *testing.T) {
		runWrapper(t, first, job, len(rawWorkouts), 0, 0)
		tx := beginWorkerTestAccount(t, workerDB, fixture.accountID)
		defer tx.Rollback(ctx)
		for _, workout := range document.Workouts {
			var id, typeID uuid.UUID
			var typeKey string
			var hash, fallback []byte
			if err := tx.QueryRow(ctx, `SELECT w.id,wt.id,wt.type_key,w.content_sha256,w.fallback_sha256
				FROM app.workouts w JOIN app.workout_types wt ON wt.id=w.workout_type_id
				WHERE w.source_id=$1 AND wt.type_key=$2 AND w.provider_id IS NOT DISTINCT FROM NULLIF($3,'')`, fixture.sourceID, workout.TypeKey, workout.ProviderID).
				Scan(&id, &typeID, &typeKey, &hash, &fallback); err != nil || id == uuid.Nil || typeID == uuid.Nil || typeKey != workout.TypeKey ||
				!bytes.Equal(hash, workout.ContentSHA256[:]) || (workout.ProviderID == "" && !bytes.Equal(fallback, workout.FallbackSHA256[:])) {
				t.Fatalf("normal import identity=%s/%s/%s/%x/%x err=%v", id, typeID, typeKey, hash, fallback, err)
			}
		}
		assertCountQuery(t, tx, len(rawWorkouts), `SELECT count(*) FROM app.workout_import_events WHERE job_id=$1 AND kind='created'`, job.id)
	}) {
		return
	}
	before := graphSnapshot(t)
	if !t.Run("unchanged raw reimport through executeIngest", func(t *testing.T) {
		second := enqueueIngestJob(t, setupDB, keys, fixture)
		secondJob := claim(second)
		runWrapper(t, second, secondJob, 0, len(rawWorkouts), 0)
		if !bytes.Equal(before, graphSnapshot(t)) {
			t.Fatal("normal reimport changed UUIDs, raw keys, hashes/fingerprints, metrics, or route/coverage graph")
		}
		tx := beginWorkerTestAccount(t, workerDB, fixture.accountID)
		defer tx.Rollback(ctx)
		assertCountQuery(t, tx, len(rawWorkouts), `SELECT count(*) FROM app.workout_import_events WHERE job_id=$1 AND kind='matched_unchanged'`, secondJob.id)
		assertCountQuery(t, tx, 0, `SELECT count(*) FROM app.workout_import_events WHERE job_id=$1 AND kind<>'matched_unchanged'`, secondJob.id)
	}) {
		return
	}
	t.Run("incremental skip reconciles before terminal lease release", func(t *testing.T) {
		tx := beginWorkerTestAccount(t, setupDB, fixture.accountID)
		defer tx.Rollback(ctx)
		var seed uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM app.workouts WHERE source_id=$1 ORDER BY id LIMIT 1`, fixture.sourceID).Scan(&seed); err != nil {
			t.Fatal(err)
		}
		var set bool
		if err := tx.QueryRow(ctx, `SELECT app.set_workout_route_timezone($1,$2,'America/Los_Angeles','naming-fixture')`, fixture.accountID, seed).Scan(&set); err != nil || !set {
			t.Fatalf("approved timezone seed=%t err=%v", set, err)
		}
		assertCountQuery(t, tx, len(rawWorkouts)-1, `SELECT count(*) FROM app.workouts WHERE source_id=$1 AND timezone_name IS NULL`, fixture.sourceID)
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		skipped := enqueueIncrementalIngestJob(t, setupDB, keys, fixture)
		skipJob := claim(skipped)
		runWrapper(t, skipped, skipJob, 0, 0, 1)
		if !bytes.Equal(before, graphSnapshot(t)) {
			t.Fatal("incremental wrapper changed canonical identity, metrics, or route/coverage graph")
		}
		tx = beginWorkerTestAccount(t, workerDB, fixture.accountID)
		defer tx.Rollback(ctx)
		// The real reconciliation function requires a live lease. Its persisted
		// result plus cleared terminal lease proves it ran before finishJob.
		assertCountQuery(t, tx, len(rawWorkouts)-1, `SELECT count(*) FROM app.workouts WHERE source_id=$1
			AND timezone_name='America/Los_Angeles' AND timezone_source='nearest_route_boundary'
			AND timezone_reference_workout_id=$2 AND timezone_dataset_release='naming-fixture'`, fixture.sourceID, seed)
		assertCountQuery(t, tx, 0, `SELECT count(*) FROM app.source_files WHERE job_id=$1`, skipJob.id)
		assertCountQuery(t, tx, 0, `SELECT count(*) FROM app.workout_import_events WHERE job_id=$1`, skipJob.id)
		assertCountQuery(t, tx, len(rawWorkouts), `SELECT count(*) FROM app.workout_import_events WHERE source_id=$1 AND kind='matched_unchanged'`, fixture.sourceID)
		assertCountQuery(t, tx, 0, `SELECT count(*) FROM app.workout_import_events WHERE source_id=$1 AND kind='updated'`, fixture.sourceID)
	})
}
