package migrations_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/migrations"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

const providerIdentityWorker = "provider-identity-test"

type providerIdentitySource struct {
	account, requester, source, parent, job, lease, file uuid.UUID
}

type providerIdentityResult struct {
	workout *uuid.UUID
	hash    []byte
	outcome string
}

// Never fall back to application URLs. This suite mutates only an explicitly
// disposable, empty, named loopback PostGIS database with standard test roles.
func TestProviderWorkoutIdentityIntegration(t *testing.T) {
	dsn := os.Getenv("PROVIDER_WORKOUT_IDENTITY_TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("PROVIDER_WORKOUT_IDENTITY_TEST_DISPOSABLE") != "1" {
		t.Skip("explicit disposable provider identity scratch database is required")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid identity scratch configuration")
	}
	loopback := func(host string) bool { return host == "127.0.0.1" || host == "localhost" || host == "::1" }
	if !strings.HasPrefix(config.Database, "workouts_provider_identity_test_") || !loopback(config.Host) {
		t.Fatal("identity tests require a named loopback scratch database")
	}
	for _, fallback := range config.Fallbacks {
		if !loopback(fallback.Host) {
			t.Fatal("identity scratch fallbacks must also be loopback")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	config.RuntimeParams["statement_timeout"] = "5s"
	admin, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal("connect to identity scratch database")
	}
	defer admin.Close(context.Background())
	var empty bool
	if err := admin.QueryRow(ctx, `SELECT to_regnamespace('app') IS NULL AND to_regclass('public.goose_db_version') IS NULL`).Scan(&empty); err != nil || !empty {
		t.Fatal("identity scratch database must initially be empty")
	}
	if _, err := admin.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS postgis`); err != nil {
		t.Fatal(err)
	}
	migrationConfig := config.Copy()
	migrationConfig.RuntimeParams["role"] = "workouts_migration"
	sqlDB := stdlib.OpenDB(*migrationConfig)
	defer sqlDB.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 23); err != nil {
		t.Fatal(err)
	}
	pool := func(role string) *pgxpool.Pool {
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatal("invalid identity scratch pool configuration")
		}
		cfg.ConnConfig.RuntimeParams["role"] = role
		cfg.ConnConfig.RuntimeParams["statement_timeout"] = "5s"
		cfg.MaxConns = 4
		result, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatal("create identity scratch pool")
		}
		t.Cleanup(result.Close)
		return result
	}
	db := testDatabases{ctx: ctx, migration: pool("workouts_migration"), worker: pool("workouts_worker"),
		api: pool("workouts_api"), coverage: pool("workouts_coverage_worker"), tiles: pool("workouts_tiles")}
	local := providerIdentityNewSource(t, db, uuid.Nil, "health-auto-export-local")
	cloud := providerIdentityNewSource(t, db, local.account, "health-auto-export-icloud")
	legacyWorkout := providerIdentitySeed(t, db, local, "legacy/provider", false)
	cloudOld := providerIdentityNewSource(t, db, uuid.Nil, "health-auto-export-icloud")
	localNew := providerIdentityNewSource(t, db, cloudOld.account, "health-auto-export-local")
	cloudWorkout := providerIdentitySeed(t, db, cloudOld, "reverse/provider", false)
	duplicate := providerIdentitySeed(t, db, cloud, "legacy/provider", false)
	pending := providerIdentityNewSource(t, db, uuid.Nil, "health-auto-export-local")
	pendingReplacement := providerIdentityNewSource(t, db, pending.account, "health-auto-export-icloud")
	pendingWorkout := providerIdentitySeed(t, db, pending, "pending/provider", false)
	providerIdentityDeleteWorkout(t, db, pending, pendingWorkout, false)
	completed := providerIdentityNewSource(t, db, uuid.Nil, "health-auto-export-icloud")
	completedReplacement := providerIdentityNewSource(t, db, completed.account, "health-auto-export-local")
	completedWorkout := providerIdentitySeed(t, db, completed, "completed/provider", false)
	providerIdentityDeleteWorkout(t, db, completed, completedWorkout, true)

	t.Run("UpgradeRefusesLegacyDuplicatesAtomically", func(t *testing.T) {
		_, err := provider.UpTo(ctx, 24)
		providerIdentitySQLState(t, err, "23505")
		var intact bool
		if err := admin.QueryRow(ctx, `SELECT (SELECT schema_version=23 AND minimum_runtime_version=23 FROM app.schema_metadata)
			AND (SELECT count(*)=2 FROM app.workouts WHERE account_id=$1 AND provider_id='legacy/provider')
			AND NOT EXISTS(SELECT 1 FROM pg_attribute WHERE attrelid='app.workouts'::regclass AND attname='provider_namespace' AND NOT attisdropped)`, local.account).Scan(&intact); err != nil || !intact {
			t.Fatalf("failed upgrade mutated legacy rows/schema: %t %v", intact, err)
		}
		// Explicit scratch-fixture remediation, not behavior performed by 024.
		tx := beginAccount(t, ctx, db.worker, cloud.account)
		defer tx.Rollback(ctx)
		var source uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT source_id FROM app.fence_ingest_job($1,$2,$3)`, cloud.job, providerIdentityWorker, cloud.lease).Scan(&source); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM app.workout_route_points WHERE workout_id=$1`, duplicate); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		cleanup, err := admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer cleanup.Rollback(ctx)
		if _, err := cleanup.Exec(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
			t.Fatal(err)
		}
		if _, err := cleanup.Exec(ctx, `DELETE FROM app.workout_import_events WHERE workout_id=$1`, duplicate); err != nil {
			t.Fatal(err)
		}
		if _, err := cleanup.Exec(ctx, `DELETE FROM app.workouts WHERE id=$1`, duplicate); err != nil {
			t.Fatal(err)
		}
		if err := cleanup.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	})
	for range 2 {
		if _, err := provider.UpTo(ctx, 24); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("UpgradePreservesDataAndHistoricalProvenance", func(t *testing.T) {
		var intact bool
		if err := admin.QueryRow(ctx, `SELECT (SELECT schema_version=24 AND minimum_runtime_version=24 FROM app.schema_metadata)
			AND EXISTS(SELECT 1 FROM app.workouts WHERE id=$1 AND source_id=$2 AND provider_namespace='health-auto-export')
			AND EXISTS(SELECT 1 FROM app.workout_import_events WHERE workout_id=$1 AND source_id=$2 AND source_file_id=$3 AND job_id=$4)
			AND (SELECT bool_and(provider_namespace='health-auto-export') FROM app.workout_deletion_targets)
			AND NOT EXISTS(SELECT 1 FROM app.accounts WHERE workout_ownership_epoch<>0)`, legacyWorkout, local.source, local.file, local.job).Scan(&intact); err != nil || !intact {
			t.Fatalf("upgrade failed preservation: %t %v", intact, err)
		}
	})
	t.Run("MetadataAndCapabilityBoundaries", func(t *testing.T) { providerIdentitySecurity(t, db, admin, cloud) })
	t.Run("AdoptBothTransportsPreservingUUIDAndHistory", func(t *testing.T) {
		for _, tc := range []struct {
			old, replacement providerIdentitySource
			workout          uuid.UUID
			provider         string
		}{{local, cloud, legacyWorkout, "legacy/provider"}, {cloudOld, localNew, cloudWorkout, "reverse/provider"}} {
			providerIdentityDeleteSource(t, db, tc.old)
			tx := providerIdentityBegin(t, db, tc.replacement)
			result := providerIdentityResolve(t, db, tx, tc.replacement, tc.provider, nil, nil)
			if result.outcome != "adopted" || result.workout == nil || *result.workout != tc.workout || !bytes.Equal(result.hash, bytes.Repeat([]byte{1}, 32)) {
				t.Fatalf("unexpected adoption: %+v", result)
			}
			providerIdentityExec(t, db, tx, `UPDATE app.workouts SET source_id=$2,source_file_id=$3 WHERE id=$1`, tc.workout, tc.replacement.source, tc.replacement.file)
			providerIdentityEvent(t, db, tx, tc.replacement, tc.workout, "adopted")
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			var intact bool
			if err := admin.QueryRow(ctx, `SELECT
				EXISTS(SELECT 1 FROM app.workouts WHERE id=$1 AND source_id=$2 AND source_file_id=$3 AND content_sha256=$4)
				AND EXISTS(SELECT 1 FROM app.workout_import_events WHERE workout_id=$1 AND source_id=$5 AND source_file_id=$6 AND kind='created')
				AND EXISTS(SELECT 1 FROM app.workouts workout JOIN app.workout_import_events event ON event.workout_id=workout.id
				    WHERE workout.id=$1 AND event.kind='created' AND event.created_at=workout.created_at)
				AND EXISTS(SELECT 1 FROM app.workout_import_events WHERE workout_id=$1 AND source_id=$2 AND kind='adopted')
				AND EXISTS(SELECT 1 FROM app.workout_route_points WHERE workout_id=$1)
				AND (SELECT workout_ownership_epoch=1 FROM app.accounts WHERE id=$7)
				AND NOT EXISTS(SELECT 1 FROM app.workout_adoption_capabilities)
				AND NOT EXISTS(SELECT 1 FROM app.ingest_write_capabilities)`, tc.workout, tc.replacement.source, tc.replacement.file,
				bytes.Repeat([]byte{1}, 32), tc.old.source, tc.old.file, tc.old.account).Scan(&intact); err != nil || !intact {
				t.Fatalf("adoption lost history/content/epoch cleanup: %t %v", intact, err)
			}
			tx = providerIdentityBegin(t, db, tc.replacement)
			result = providerIdentityResolve(t, db, tx, tc.replacement, tc.provider, nil, nil)
			if result.outcome != "existing" || result.workout == nil || *result.workout != tc.workout {
				t.Fatalf("same-source identity changed: %+v", result)
			}
			providerIdentityExec(t, db, tx, `UPDATE app.workouts SET content_sha256=$2 WHERE id=$1`, tc.workout, bytes.Repeat([]byte{2}, 32))
			providerIdentityEvent(t, db, tx, tc.replacement, tc.workout, "updated")
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
		}
	})
	t.Run("LiveOwnersIncludeDisconnectedSources", func(t *testing.T) {
		for _, status := range []string{"connected", "checking-connection", "connection-failed"} {
			owner := providerIdentityNewSource(t, db, uuid.Nil, "health-auto-export-local")
			other := providerIdentityNewSource(t, db, owner.account, "health-auto-export-icloud")
			workout := providerIdentitySeed(t, db, owner, "live/provider", true)
			setup := beginAccount(t, ctx, db.migration, owner.account)
			providerIdentityExec(t, db, setup, `UPDATE app.sources SET status=$2 WHERE id=$1`, owner.source, status)
			if err := setup.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			tx := providerIdentityBegin(t, db, other)
			result := providerIdentityResolve(t, db, tx, other, "live/provider", nil, nil)
			if result.outcome != "skipped_live_owner" || result.workout == nil || *result.workout != workout {
				t.Fatalf("live owner status=%s result=%+v", status, result)
			}
			providerIdentityEvent(t, db, tx, other, workout, "skipped_live_owner")
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			// A live ingest capability alone cannot reassign the other owner's row.
			tx = providerIdentityBegin(t, db, other)
			_, err := tx.Exec(ctx, `UPDATE app.workouts SET source_id=$2,source_file_id=$3 WHERE id=$1`, workout, other.source, other.file)
			providerIdentitySQLState(t, err, "42501")
			_ = tx.Rollback(ctx)
		}
	})
	t.Run("PendingAndCompletedTombstonesSuppressReplacementSources", func(t *testing.T) {
		for _, tc := range []struct {
			old, replacement providerIdentitySource
			provider         string
		}{{pending, pendingReplacement, "pending/provider"}, {completed, completedReplacement, "completed/provider"}} {
			providerIdentityDeleteSource(t, db, tc.old)
			tx := providerIdentityBegin(t, db, tc.replacement)
			result := providerIdentityResolve(t, db, tx, tc.replacement, tc.provider, nil, nil)
			if result.outcome != "suppressed" || result.workout != nil {
				t.Fatalf("deletion resurrected: %+v", result)
			}
			_ = tx.Rollback(ctx)
		}
	})
	t.Run("FallbackRemainsSourceLocal", func(t *testing.T) {
		owner := providerIdentityNewSource(t, db, uuid.Nil, "health-auto-export-local")
		other := providerIdentityNewSource(t, db, owner.account, "health-auto-export-icloud")
		tx := providerIdentityBegin(t, db, owner)
		fallback := bytes.Repeat([]byte{3}, 32)
		workout := uuid.New()
		providerIdentityInsert(t, db, tx, owner, workout, nil, "fallback-v1", fallback)
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		providerIdentityDeleteWorkout(t, db, owner, workout, false)
		providerIdentityDeleteSource(t, db, owner)
		tx = providerIdentityBegin(t, db, other)
		result := providerIdentityResolve(t, db, tx, other, "", "fallback-v1", fallback)
		if result.outcome != "created" || result.workout == nil || *result.workout == workout {
			t.Fatalf("fallback crossed source identity: %+v", result)
		}
		_ = tx.Rollback(ctx)
	})
	t.Run("NamespaceUniquenessAndHistoryAppendOnly", func(t *testing.T) {
		f := providerIdentityNewSource(t, db, cloud.account, "health-auto-export-local")
		tx := providerIdentityBegin(t, db, f)
		_, err := tx.Exec(ctx, `INSERT INTO app.workouts(id,account_id,source_id,source_file_id,workout_type_id,provider_id,
			content_sha256,provider_label,started_at,ended_at,provider_duration)
			SELECT $1,account_id,$2,$3,workout_type_id,provider_id,content_sha256,provider_label,started_at,ended_at,provider_duration
			FROM app.workouts WHERE id=$4`, uuid.New(), f.source, f.file, legacyWorkout)
		providerIdentitySQLState(t, err, "23505")
		_ = tx.Rollback(ctx)
		tx = providerIdentityBegin(t, db, f)
		_, err = tx.Exec(ctx, `UPDATE app.workout_import_events SET kind='updated' WHERE workout_id=$1`, legacyWorkout)
		providerIdentitySQLState(t, err, "42501") // Worker has INSERT, never UPDATE.
		_ = tx.Rollback(ctx)
		tx = beginAccount(t, ctx, db.migration, f.account)
		_, err = tx.Exec(ctx, `UPDATE app.workout_import_events SET kind='updated' WHERE workout_id=$1`, legacyWorkout)
		providerIdentitySQLState(t, err, "23514")
		_ = tx.Rollback(ctx)
		// The timezone exception remains usable without an ordinary ingest capability.
		setup := beginAccount(t, ctx, db.migration, cloud.account)
		var changed bool
		if err := setup.QueryRow(ctx, `SELECT app.apply_workout_timezone($1,$2,'Etc/UTC','route_boundary',NULL,'synthetic')`, cloud.account, legacyWorkout).Scan(&changed); err != nil || !changed {
			t.Fatalf("timezone exception lost: %t %v", changed, err)
		}
		_ = setup.Rollback(ctx)
	})
	t.Run("DefensiveAmbiguityIsNotArbitrarilyResolved", func(t *testing.T) {
		tx, err := admin.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(ctx)
		providerIdentityExec(t, db, tx, `SELECT set_config('app.account_id',$1,true)`, cloud.account.String())
		var source uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT source_id FROM app.fence_ingest_job($1,$2,$3,26)`, cloud.job, providerIdentityWorker, cloud.lease).Scan(&source); err != nil {
			t.Fatal(err)
		}
		providerIdentityExec(t, db, tx, `DROP INDEX app.workouts_account_provider_identity_idx`)
		providerIdentityExec(t, db, tx, `SET LOCAL session_replication_role=replica`)
		providerIdentityExec(t, db, tx, `INSERT INTO app.workouts(id,account_id,source_id,source_file_id,workout_type_id,provider_id,
			content_sha256,provider_label,started_at,ended_at,provider_duration)
			SELECT $1,account_id,$2,$3,workout_type_id,provider_id,content_sha256,provider_label,started_at,ended_at,provider_duration
			FROM app.workouts WHERE id=$4`, uuid.New(), local.source, local.file, legacyWorkout)
		providerIdentityExec(t, db, tx, `SET LOCAL session_replication_role=origin`)
		_, err = tx.Exec(ctx, `SELECT * FROM app.resolve_ingest_workout_identity($1,$2,$3,$4,$5,'legacy/provider',NULL,NULL)`, cloud.job, providerIdentityWorker, cloud.lease, cloud.file, uuid.New())
		providerIdentitySQLState(t, err, "23514")
	})
	t.Run("LockAndLeaseRaces", func(t *testing.T) { providerIdentityRaces(t, db) })
}

func providerIdentityNewSource(t *testing.T, db testDatabases, account uuid.UUID, sourceType string) providerIdentitySource {
	t.Helper()
	f := providerIdentitySource{account: account, source: uuid.New(), parent: uuid.New(), job: uuid.New(), lease: uuid.New(), file: uuid.New()}
	newAccount := account == uuid.Nil
	if newAccount {
		f.account = uuid.New()
	}
	tx := beginAccount(t, db.ctx, db.migration, f.account)
	defer tx.Rollback(db.ctx)
	if newAccount {
		f.requester = uuid.New()
		providerIdentityExec(t, db, tx, `INSERT INTO app.accounts(id) VALUES($1)`, f.account)
		name := "identity-" + f.requester.String()
		providerIdentityExec(t, db, tx, `INSERT INTO app.authentication_principals(id,role,username,canonical_username,email,canonical_email,canonicalization_version,password_hash,full_name)
			VALUES($1,'user',$2,$2,$3,$3,1,'synthetic-test-only','Identity fixture')`, f.requester, name, name+"@example.invalid")
		providerIdentityExec(t, db, tx, `INSERT INTO app.users(principal_id,account_id) VALUES($1,$2)`, f.requester, f.account)
	} else if err := tx.QueryRow(db.ctx, `SELECT principal_id FROM app.users WHERE account_id=$1`, account).Scan(&f.requester); err != nil {
		t.Fatal(err)
	}
	name := "Source " + f.source.String()
	providerIdentityExec(t, db, tx, `INSERT INTO app.sources(id,account_id,display_name,canonical_display_name,type,status,config_envelope)
		VALUES($1,$2,$3,$3,$4,'connected',$5)`, f.source, f.account, name, sourceType, []byte("synthetic-config"))
	providerIdentityExec(t, db, tx, `INSERT INTO app.jobs(id,account_id,kind,priority) VALUES($1,$2,'manual_ingest',80)`, f.parent, f.account)
	parameters := fmt.Sprintf(`{"sourceId":"%s","generation":1,"mode":"incremental"}`, strings.ToUpper(strings.ReplaceAll(f.source.String(), "-", "")))
	providerIdentityExec(t, db, tx, `INSERT INTO app.jobs(id,account_id,parent_job_id,kind,priority,parameters)
		VALUES($1,$2,$3,'manual_ingest_source',80,$4)`, f.job, f.account, f.parent, parameters)
	providerIdentityExec(t, db, tx, `INSERT INTO app.job_config_snapshots(job_id,account_id,source_id,source_generation,config_envelope)
		VALUES($1,$2,$3,1,$4)`, f.job, f.account, f.source, []byte("synthetic-config"))
	providerIdentityExec(t, db, tx, `INSERT INTO app.job_source_contexts(job_id,account_id,source_id,source_generation,display_name,source_type)
		VALUES($1,$2,$3,1,$4,$5)`, f.job, f.account, f.source, name, sourceType)
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	if !callBool(t, db.ctx, db.worker, f.account, `SELECT app.claim_job($1,$2,$3,interval '10 minutes')`, f.job, providerIdentityWorker, f.lease) {
		t.Fatal("identity fixture claim failed")
	}
	return f
}

func providerIdentityBegin(t *testing.T, db testDatabases, f providerIdentitySource) pgx.Tx {
	t.Helper()
	tx := beginAccount(t, db.ctx, db.worker, f.account)
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	var source uuid.UUID
	if err := tx.QueryRow(db.ctx, `SELECT source_id FROM app.fence_ingest_job($1,$2,$3,26)`, f.job, providerIdentityWorker, f.lease).Scan(&source); err != nil || source != f.source {
		t.Fatalf("identity fence: source=%s err=%v", source, err)
	}
	providerIdentityExec(t, db, tx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes)
		VALUES($1,$2,$3,$4,'synthetic.json',1) ON CONFLICT(id) DO NOTHING`, f.file, f.account, f.source, f.job)
	return tx
}

func providerIdentitySeed(t *testing.T, db testDatabases, f providerIdentitySource, provider string, versioned bool) uuid.UUID {
	t.Helper()
	var tx pgx.Tx
	if versioned {
		tx = providerIdentityBegin(t, db, f)
	} else {
		tx = beginAccount(t, db.ctx, db.worker, f.account)
		defer tx.Rollback(db.ctx)
		var source uuid.UUID
		if err := tx.QueryRow(db.ctx, `SELECT source_id FROM app.fence_ingest_job($1,$2,$3)`, f.job, providerIdentityWorker, f.lease).Scan(&source); err != nil {
			t.Fatal(err)
		}
		providerIdentityExec(t, db, tx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes)
			VALUES($1,$2,$3,$4,'synthetic.json',1) ON CONFLICT(id) DO NOTHING`, f.file, f.account, f.source, f.job)
	}
	workout := uuid.New()
	if versioned {
		result := providerIdentityResolve(t, db, tx, f, provider, nil, nil)
		if result.outcome != "created" || result.workout == nil {
			t.Fatalf("seed resolve=%+v", result)
		}
		workout = *result.workout
	}
	providerIdentityInsert(t, db, tx, f, workout, provider, nil, nil)
	providerIdentityEvent(t, db, tx, f, workout, "created")
	providerIdentityExec(t, db, tx, `INSERT INTO app.workout_route_points(account_id,workout_id,sequence,recorded_at,latitude,longitude)
		VALUES($1,$2,0,'2026-01-01T00:00:00Z',40,-70)`, f.account, workout)
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	return workout
}

func providerIdentityInsert(t *testing.T, db testDatabases, tx pgx.Tx, f providerIdentitySource, workout uuid.UUID, provider, fallback, hash any) {
	t.Helper()
	var typeID uuid.UUID
	if err := tx.QueryRow(db.ctx, `INSERT INTO app.workout_types(id,account_id,type_key,provider_label) VALUES($1,$2,'walking','Walking')
		ON CONFLICT(account_id,type_key) DO UPDATE SET provider_label=EXCLUDED.provider_label RETURNING id`, uuid.New(), f.account).Scan(&typeID); err != nil {
		t.Fatal(err)
	}
	providerIdentityExec(t, db, tx, `INSERT INTO app.workouts(id,account_id,source_id,source_file_id,workout_type_id,provider_id,
		fallback_fingerprint_version,fallback_sha256,content_sha256,provider_label,started_at,ended_at,local_start_date,provider_duration)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'Walking','2026-01-01T00:00:00Z','2026-01-01T00:01:00Z','2026-01-01',60)`,
		workout, f.account, f.source, f.file, typeID, provider, fallback, hash, bytes.Repeat([]byte{1}, 32))
}

func providerIdentityEvent(t *testing.T, db testDatabases, tx pgx.Tx, f providerIdentitySource, workout uuid.UUID, kind string) {
	t.Helper()
	providerIdentityExec(t, db, tx, `INSERT INTO app.workout_import_events(id,account_id,source_id,workout_id,source_file_id,job_id,kind,content_sha256)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, uuid.New(), f.account, f.source, workout, f.file, f.job, kind, bytes.Repeat([]byte{1}, 32))
}

func providerIdentityResolve(t *testing.T, db testDatabases, tx pgx.Tx, f providerIdentitySource, provider string, fallback, hash any) providerIdentityResult {
	t.Helper()
	var id any
	if provider != "" {
		id = provider
	}
	var result providerIdentityResult
	if err := tx.QueryRow(db.ctx, `SELECT workout_id,prior_content_sha256,outcome FROM app.resolve_ingest_workout_identity($1,$2,$3,$4,$5,$6,$7,$8)`,
		f.job, providerIdentityWorker, f.lease, f.file, uuid.New(), id, fallback, hash).Scan(&result.workout, &result.hash, &result.outcome); err != nil {
		t.Fatal(err)
	}
	return result
}

func providerIdentityDeleteSource(t *testing.T, db testDatabases, f providerIdentitySource) {
	t.Helper()
	if !callBool(t, db.ctx, db.api, f.account, `SELECT app.delete_source($1,$2)`, f.source, f.requester) {
		t.Fatal("source delete failed")
	}
}

func providerIdentityDeleteWorkout(t *testing.T, db testDatabases, f providerIdentitySource, workout uuid.UUID, complete bool) {
	t.Helper()
	tx := beginAccount(t, db.ctx, db.api, f.account)
	defer tx.Rollback(db.ctx)
	var job uuid.UUID
	var reused bool
	var count int
	if err := tx.QueryRow(db.ctx, `SELECT job_id,reused,target_count FROM app.enqueue_workout_deletion($1,$2)`, workout, f.requester).Scan(&job, &reused, &count); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
	if !complete {
		return
	}
	lease := uuid.New()
	if !callBool(t, db.ctx, db.worker, f.account, `SELECT app.claim_job($1,$2,$3,interval '10 minutes')`, job, providerIdentityWorker, lease) {
		t.Fatal("deletion fixture claim failed")
	}
	tx = beginAccount(t, db.ctx, db.worker, f.account)
	defer tx.Rollback(db.ctx)
	providerIdentityExec(t, db, tx, `SELECT * FROM app.fence_workout_deletion($1,$2,$3)`, job, providerIdentityWorker, lease)
	providerIdentityExec(t, db, tx, `SELECT * FROM app.purge_workout_deletion($1,$2,$3)`, job, providerIdentityWorker, lease)
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
}

func providerIdentityExec(t *testing.T, db testDatabases, tx pgx.Tx, sql string, args ...any) {
	t.Helper()
	if _, err := tx.Exec(db.ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}

func providerIdentitySQLState(t *testing.T, err error, state string) {
	t.Helper()
	var pgError *pgconn.PgError
	if !errors.As(err, &pgError) || pgError.Code != state {
		t.Fatalf("SQLSTATE=%v want=%s", err, state)
	}
}

func providerIdentitySecurity(t *testing.T, db testDatabases, admin *pgx.Conn, f providerIdentitySource) {
	t.Helper()
	for _, signature := range []string{"app.fence_ingest_job(uuid,text,uuid,integer)", "app.resolve_ingest_workout_identity(uuid,text,uuid,uuid,uuid,text,text,bytea)"} {
		var safe bool
		if err := admin.QueryRow(db.ctx, `SELECT pg_get_userbyid(proowner)='workouts_security_owner' AND prosecdef
			AND proconfig=ARRAY['search_path=pg_catalog, app'] AND has_function_privilege('workouts_worker',oid,'EXECUTE')
			AND NOT has_function_privilege('workouts_api',oid,'EXECUTE') AND NOT has_function_privilege('workouts_coverage_worker',oid,'EXECUTE')
			AND NOT has_function_privilege('workouts_tiles',oid,'EXECUTE')
			AND NOT EXISTS(SELECT 1 FROM aclexplode(proacl) acl WHERE grantee=0) FROM pg_proc WHERE oid=$1::regprocedure`, signature).Scan(&safe); err != nil || !safe {
			t.Fatalf("unsafe identity function %s: %t %v", signature, safe, err)
		}
	}
	for _, version := range []any{nil, 23, 25} {
		tx := beginAccount(t, db.ctx, db.worker, f.account)
		_, err := tx.Exec(db.ctx, `SELECT * FROM app.fence_ingest_job($1,$2,$3,$4)`, f.job, providerIdentityWorker, f.lease, version)
		providerIdentitySQLState(t, err, "55000")
		_ = tx.Rollback(db.ctx)
	}
	for _, query := range []string{
		`SELECT * FROM app.fence_ingest_job($1,$2,$3)`,
		`SELECT * FROM app.fence_ingest_job($1,$2,$3,26)`,
	} {
		tx := beginAccount(t, db.ctx, db.worker, f.account)
		var source uuid.UUID
		err := tx.QueryRow(db.ctx, query, f.job, providerIdentityWorker, uuid.New()).Scan(&source)
		if strings.Contains(query, ",26)") {
			if !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("wrong lease admitted: %v", err)
			}
		} else {
			providerIdentitySQLState(t, err, "55000")
		}
		_ = tx.Rollback(db.ctx)
	}
	for _, query := range []string{
		`INSERT INTO app.workout_adoption_capabilities(backend_pid,transaction_id,account_id,workout_id,old_source_id,new_source_id,source_file_id,job_id) VALUES(pg_backend_pid(),txid_current(),$1,$2,$3,$3,$4,$5)`,
		`INSERT INTO app.ingest_write_capabilities(backend_pid,transaction_id,account_id,source_id,job_id,worker_id,lease_token,runtime_version,ownership_epoch) VALUES(pg_backend_pid(),txid_current(),$1,$3,$5,$4::uuid::text,$2,26,0)`,
	} {
		tx := beginAccount(t, db.ctx, db.worker, f.account)
		_, err := tx.Exec(db.ctx, query, f.account, uuid.New(), f.source, f.file, f.job)
		providerIdentitySQLState(t, err, "42501")
		_ = tx.Rollback(db.ctx)
	}
	tx := beginAccount(t, db.ctx, db.worker, f.account)
	providerIdentityExec(t, db, tx, `SELECT set_config('app.ingest_write_capability','26',true)`)
	_, err := tx.Exec(db.ctx, `INSERT INTO app.workout_types(id,account_id,type_key,provider_label) VALUES($1,$2,'spoof','Spoof')`, uuid.New(), f.account)
	providerIdentitySQLState(t, err, "42501")
	_ = tx.Rollback(db.ctx)
	// Reproduce a pre-upgrade capability without allowing it to authorize a write.
	tx, err = admin.Begin(db.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(db.ctx)
	providerIdentityExec(t, db, tx, `SELECT set_config('app.account_id',$1,true)`, f.account.String())
	providerIdentityExec(t, db, tx, `INSERT INTO app.ingest_write_capabilities(backend_pid,transaction_id,account_id,source_id,job_id,worker_id,lease_token,runtime_version,ownership_epoch)
		VALUES(pg_backend_pid(),txid_current(),$1,$2,$3,$4,$5,23,0)`, f.account, f.source, f.job, providerIdentityWorker, f.lease)
	providerIdentityExec(t, db, tx, `SET LOCAL ROLE workouts_worker`)
	_, err = tx.Exec(db.ctx, `INSERT INTO app.workout_types(id,account_id,type_key,provider_label) VALUES($1,$2,'stale','Stale')`, uuid.New(), f.account)
	providerIdentitySQLState(t, err, "42501")
}
