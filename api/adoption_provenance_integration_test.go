package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/api/migrations"
	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// Never use application DSNs: fixture-only trigger bypasses require an empty,
// explicitly disposable loopback PostGIS database with the standard test roles.
func TestAdoptionProvenancePostgreSQL(t *testing.T) {
	dsn := os.Getenv("PROJECT_ADOPTION_PROVENANCE_TEST_DATABASE_URL")
	if dsn == "" || os.Getenv("PROJECT_ADOPTION_PROVENANCE_TEST_DISPOSABLE") != "1" {
		t.Skip("explicit disposable adoption provenance database is required")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid provenance scratch configuration")
	}
	loopback := func(host string) bool { return host == "127.0.0.1" || host == "localhost" || host == "::1" }
	if !strings.HasPrefix(cfg.Database, "workouts_adoption_provenance_test_") || !loopback(cfg.Host) {
		t.Fatal("provenance tests require a named loopback scratch database")
	}
	for _, fallback := range cfg.Fallbacks {
		if !loopback(fallback.Host) {
			t.Fatal("provenance scratch fallbacks must also be loopback")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	pool := func(role string) *pgxpool.Pool {
		t.Helper()
		poolCfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatal("invalid provenance scratch pool configuration")
		}
		if role != "" {
			poolCfg.ConnConfig.RuntimeParams["role"] = role
		}
		poolCfg.ConnConfig.RuntimeParams["statement_timeout"] = "5s"
		poolCfg.MaxConns = 4
		db, err := pgxpool.NewWithConfig(ctx, poolCfg)
		if err != nil {
			t.Fatal("create provenance scratch pool")
		}
		t.Cleanup(db.Close)
		return db
	}
	adminDB := pool("")
	var empty bool
	if err := adminDB.QueryRow(ctx, `SELECT to_regnamespace('app') IS NULL AND to_regclass('public.goose_db_version') IS NULL`).Scan(&empty); err != nil || !empty {
		t.Fatal("provenance scratch database must initially be empty")
	}
	if _, err := adminDB.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS postgis`); err != nil {
		t.Fatal(err)
	}
	cfg.RuntimeParams["role"] = "workouts_migration"
	sqlDB := stdlib.OpenDB(*cfg)
	defer sqlDB.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, database.SupportedSchemaVersion); err != nil {
		t.Fatal(err)
	}
	apiDB, workerDB := pool("workouts_api"), pool("workouts_worker")

	t.Run("HelperContractAndACL", func(t *testing.T) {
		var valid bool
		if err := adminDB.QueryRow(ctx, `SELECT p.prosecdef AND p.provolatile='s'
			AND pg_get_userbyid(p.proowner)='workouts_security_owner'
			AND p.proconfig=ARRAY['search_path=pg_catalog, app']
			AND pg_get_function_result(p.oid)='TABLE(event_id uuid, kind text, job_id uuid, source_id uuid, source_name text, source_type text, relative_name text, warnings jsonb, imported_at timestamp with time zone)'
			AND NOT EXISTS (SELECT 1 FROM aclexplode(p.proacl) acl WHERE acl.grantee=0)
			AND has_function_privilege('workouts_api',p.oid,'EXECUTE')
			AND NOT has_function_privilege('workouts_worker',p.oid,'EXECUTE')
			AND NOT has_function_privilege('workouts_coverage_worker',p.oid,'EXECUTE')
			AND NOT has_function_privilege('workouts_tiles',p.oid,'EXECUTE')
			FROM pg_proc p WHERE p.oid='app.read_workout_provenance(uuid)'::regprocedure`).Scan(&valid); err != nil || !valid {
			t.Fatalf("provenance helper contract/ACL: valid=%t err=%v", valid, err)
		}
		for _, role := range []string{"workouts_worker", "workouts_coverage_worker", "workouts_tiles"} {
			_, err := pool(role).Exec(ctx, `SELECT * FROM app.read_workout_provenance($1)`, uuid.New())
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
				t.Fatalf("%s provenance execution was not denied", role)
			}
		}
	})

	t.Run("HistorySurvivesSourceHandoff", func(t *testing.T) {
		principalID, accountID := insertSourceTestUser(t, adminDB)
		foreignPrincipal, foreignAccount := insertSourceTestUser(t, adminDB)
		fixture := insertWorkoutReadFixtures(t, adminDB, workerDB, accountID)
		workoutID := fixture.workouts[1]
		server := integrationServer(t, apiDB, &recordingSender{})
		var logs bytes.Buffer
		handler, err := NewHandlerContext(ctx, server.config, apiDB, slog.New(slog.NewJSONHandler(&logs, nil)))
		if err != nil {
			t.Fatal(err)
		}
		bearer := insertTestSession(t, apiDB, principalID, "bearer", "")
		foreignBearer := insertTestSession(t, apiDB, foreignPrincipal, "bearer", "")
		provenancePath := "/api/workouts/" + compactUUID(workoutID) + "/provenance"
		read := func() generated.WorkoutProvenance {
			t.Helper()
			response := routeOwnerRead(handler, provenancePath, bearer)
			if response.Code != http.StatusOK {
				t.Fatalf("provenance status=%d", response.Code)
			}
			validateRecordedResponse(t, http.MethodGet, provenancePath, response)
			var result generated.WorkoutProvenance
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			for _, private := range []string{"private-provenance-directory", "credential-envelope-canary", "config_envelope", "configEnvelope"} {
				if strings.Contains(response.Body.String(), private) || strings.Contains(logs.String(), private) {
					t.Fatal("provenance response or logs exposed private fixture values")
				}
			}
			return result
		}
		before := read()
		if len(before.Items) != 3 || before.Items[0].Kind != generated.Created || before.Items[1].Kind != generated.Updated || before.Items[2].Kind != generated.MatchedUnchanged {
			t.Fatal("legacy provenance kinds changed")
		}

		incomingSource := uuid.New()
		legacyEvent := uuid.MustParse("00000000-0000-4000-8000-000000000001")
		skippedEvent := uuid.MustParse("00000000-0000-4000-8000-000000000002")
		adoptedEvent := uuid.MustParse("00000000-0000-4000-8000-000000000003")
		tx := integrationAccountTransaction(t, adminDB, accountID)
		// Model retained pre-context history and a completed handoff. Write-side
		// capability enforcement is covered by the worker adoption integration suite.
		if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role=replica`); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO app.sources(id,account_id,display_name,canonical_display_name,type,config_envelope,status)
			VALUES($1,$2,'Renamed incoming','renamed incoming','health-auto-export-icloud',$3,'connected')`, incomingSource, accountID, []byte("credential-envelope-canary")); err != nil {
			t.Fatal(err)
		}
		parentJob := uuid.New()
		if _, err := tx.Exec(ctx, `INSERT INTO app.jobs(id,account_id,kind,priority) VALUES($1,$2,'manual_ingest',80)`, parentJob, accountID); err != nil {
			t.Fatal(err)
		}
		// Reverse insertion order makes the tied timestamp exercise event-ID ordering.
		for _, event := range []struct {
			id, source uuid.UUID
			kind       string
			at         string
			context    bool
		}{
			{adoptedEvent, incomingSource, "adopted", "2026-03-08T15:07:00Z", true},
			{skippedEvent, incomingSource, "skipped_live_owner", "2026-03-08T15:07:00Z", true},
			{legacyEvent, fixture.sourceID, "matched_unchanged", "2026-03-08T15:06:00Z", false},
		} {
			job, file := uuid.New(), uuid.New()
			if _, err := tx.Exec(ctx, `INSERT INTO app.jobs(id,account_id,parent_job_id,kind,priority) VALUES($1,$2,$3,'manual_ingest_source',80)`, job, accountID, parentJob); err != nil {
				t.Fatal(err)
			}
			if _, err := tx.Exec(ctx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes)
				VALUES($1,$2,$3,$4,'private-provenance-directory/history.json',1)`, file, accountID, event.source, job); err != nil {
				t.Fatal(err)
			}
			if event.context {
				if _, err := tx.Exec(ctx, `INSERT INTO app.job_source_contexts(job_id,account_id,source_id,source_generation,display_name,source_type)
					VALUES($1,$2,$3,1,'Incoming at import','health-auto-export-icloud')`, job, accountID, event.source); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := tx.Exec(ctx, `INSERT INTO app.workout_import_events(id,account_id,source_id,workout_id,source_file_id,job_id,kind,content_sha256,created_at)
				VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9::timestamptz)`, event.id, accountID, event.source, workoutID, file, job, event.kind, fixture.hash[:], event.at); err != nil {
				t.Fatal(err)
			}
			if event.id == adoptedEvent {
				if _, err := tx.Exec(ctx, `UPDATE app.workouts SET source_id=$2,source_file_id=$3 WHERE id=$1`, workoutID, incomingSource, file); err != nil {
					t.Fatal(err)
				}
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE app.sources SET deleted_at=clock_timestamp(),config_envelope=NULL WHERE id=$1`, fixture.sourceID); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		after := read()
		if after.WorkoutId != compactUUID(workoutID) || len(after.Items) != 6 || !reflect.DeepEqual(before.Items, after.Items[:3]) {
			t.Fatal("handoff lost stable workout identity or immutable old history")
		}
		if after.Items[3].Id != compactUUID(legacyEvent) || after.Items[3].SourceName != "read-fixture-"+fixture.sourceID.String() || after.Items[3].SourceType != "health-auto-export-local" {
			t.Fatal("missing job context did not fall back to retained source metadata")
		}
		for i, kind := range []generated.WorkoutProvenanceEventKind{generated.SkippedLiveOwner, generated.Adopted} {
			item := after.Items[i+4]
			if item.Kind != kind || item.SourceId != compactUUID(incomingSource) || item.SourceName != "Incoming at import" || item.SourceType != "health-auto-export-icloud" || item.SourceFile != "history.json" || len(item.Warnings) != 0 {
				t.Fatal("new provenance kind, immutable context, basename, or empty warnings changed")
			}
		}
		if after.Items[4].Id != compactUUID(skippedEvent) || after.Items[5].Id != compactUUID(adoptedEvent) {
			t.Fatal("equal timestamps were not ordered by event ID")
		}
		ownerTx := integrationAccountTransaction(t, apiDB, accountID)
		var visible int
		if err := ownerTx.QueryRow(ctx, `SELECT count(*) FROM app.sources WHERE id=$1`, fixture.sourceID).Scan(&visible); err != nil || visible != 0 {
			t.Fatal("API unexpectedly has direct source tombstone access")
		}
		_ = ownerTx.Rollback(ctx)
		for _, test := range []struct {
			name, path, token string
			status            int
		}{
			{"unauthenticated", provenancePath, "", http.StatusUnauthorized},
			{"foreign owner", provenancePath, foreignBearer, http.StatusNotFound},
			{"missing workout", "/api/workouts/" + compactUUID(uuid.New()) + "/provenance", bearer, http.StatusNotFound},
			{"invalid ID", "/api/workouts/not-a-uuid/provenance", bearer, http.StatusBadRequest},
		} {
			if response := routeOwnerRead(handler, test.path, test.token); response.Code != test.status {
				t.Fatalf("%s status=%d want=%d", test.name, response.Code, test.status)
			}
		}
		assertCount := func(account uuid.UUID, id uuid.UUID, want int) {
			t.Helper()
			tx := integrationAccountTransaction(t, apiDB, account)
			defer tx.Rollback(ctx)
			var count int
			if err := tx.QueryRow(ctx, `SELECT count(*) FROM app.read_workout_provenance($1)`, id).Scan(&count); err != nil || count != want {
				t.Fatalf("helper isolation: count=%d want=%d err=%v", count, want, err)
			}
		}
		assertCount(accountID, workoutID, 6)
		assertCount(foreignAccount, workoutID, 0)
		assertCount(accountID, uuid.New(), 0)
		var noContextCount int
		if err := apiDB.QueryRow(ctx, `SELECT count(*) FROM app.read_workout_provenance($1)`, workoutID).Scan(&noContextCount); err != nil || noContextCount != 0 {
			t.Fatal("helper exposed history without account context")
		}
		emptyResponse := routeOwnerRead(handler, "/api/workouts/"+compactUUID(fixture.workouts[0])+"/provenance", bearer)
		if emptyResponse.Code != http.StatusOK || !strings.Contains(emptyResponse.Body.String(), `"items":[]`) {
			t.Fatal("existing workout without events no longer returns an empty array")
		}
		for _, state := range []string{"deleting", "deleted"} {
			tx := integrationAccountTransaction(t, adminDB, accountID)
			if _, err := tx.Exec(ctx, `UPDATE app.accounts SET state=$2 WHERE id=$1`, accountID, state); err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			assertCount(accountID, workoutID, 0)
		}
		if _, err := adminDB.Exec(ctx, `UPDATE app.accounts SET state='active' WHERE id=$1`, accountID); err != nil {
			t.Fatal(err)
		}
		deleted := routeWorkoutDelete(handler, workoutID, bearer)
		if deleted.Code != http.StatusAccepted {
			t.Fatalf("delete workout status=%d", deleted.Code)
		}
		assertCount(accountID, workoutID, 0)
		if response := routeOwnerRead(handler, provenancePath, bearer); response.Code != http.StatusNotFound {
			t.Fatal("deletion-requested workout provenance was not hidden")
		}
	})
}
