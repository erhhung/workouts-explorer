package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestRoadCoverageEntityDetailPostgreSQL(t *testing.T) {
	// Existing API fixture helpers commit writes. Never run them on the retained
	// actual-data clone, even when its URLs are present in the environment.
	if os.Getenv("COVERAGE_ENTITY_DETAIL_TEST_DISPOSABLE") != "1" || os.Getenv("COVERAGE_ENTITY_DETAIL_TEST_CLONE") == "1" {
		t.Skip("COVERAGE_ENTITY_DETAIL_TEST_DISPOSABLE=1 and a disposable loopback test database are required; clones are prohibited")
	}
	var endpoint string
	for _, name := range []string{"API_DATABASE_URL", "MIGRATION_DATABASE_URL", "WORKER_DATABASE_URL"} {
		if os.Getenv(name) == "" {
			t.Skip(name + " is required")
		}
		cfg, err := pgx.ParseConfig(os.Getenv(name))
		if err != nil {
			t.Fatal("invalid disposable database configuration: " + name)
		}
		if (cfg.Host != "127.0.0.1" && cfg.Host != "localhost" && cfg.Host != "::1") ||
			(!strings.Contains(cfg.Database, "_test") && !strings.HasPrefix(cfg.Database, "test_")) {
			t.Fatal("detail fixtures require a named loopback test database")
		}
		for _, fallback := range cfg.Fallbacks {
			if fallback.Host != cfg.Host || fallback.Port != cfg.Port {
				t.Fatal("detail fixtures prohibit alternate database endpoints")
			}
		}
		target := fmt.Sprintf("%s:%d/%s", cfg.Host, cfg.Port, cfg.Database)
		if endpoint != "" && endpoint != target {
			t.Fatal("all fixture URLs must address the same disposable database")
		}
		endpoint = target
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	pools := make([]*pgxpool.Pool, 0, 3)
	for i, name := range []string{"API_DATABASE_URL", "MIGRATION_DATABASE_URL", "WORKER_DATABASE_URL"} {
		pool, err := pgxpool.New(ctx, os.Getenv(name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(pool.Close)
		pools = append(pools, pool)
		var role string
		var privileged bool
		if err := pool.QueryRow(ctx, `SELECT current_user,rolsuper OR rolbypassrls FROM pg_roles WHERE rolname=current_user`).Scan(&role, &privileged); err != nil ||
			role != []string{"workouts_api", "workouts_migration", "workouts_worker"}[i] || privileged {
			t.Fatalf("fixture requires actual restricted roles: role=%s privileged=%t err=%v", role, privileged, err)
		}
	}
	apiDB, adminDB, workerDB := pools[0], pools[1], pools[2]
	principal, account := insertSourceTestUser(t, adminDB)
	fixture := insertWorkoutReadFixtures(t, adminDB, workerDB, account)
	_, foreignAccount := insertSourceTestUser(t, adminDB)
	session, selection, narrow, campus := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	park := uuid.MustParse("abcdefab-cdef-4abc-8def-abcdefabcdef")
	paths := []uuid.UUID{uuid.New(), uuid.New()}
	tx := integrationAccountTransaction(t, adminDB, account)
	defer tx.Rollback(context.Background())
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := tx.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO app.sessions(id,principal_id,credential_kind,credential_verifier,expires_at)
		VALUES($1,$2,'bearer',$3,transaction_timestamp()+interval '1 hour')`, session, principal, []byte(strings.ReplaceAll(session.String(), "-", "")))
	for _, workout := range fixture.workouts[:2] {
		exec(`INSERT INTO app.workout_coverage_states(account_id,workout_id,route_input_revision,route_input_sha256,
			readiness_state,map_data_ready_at,processing_state,applied_route_input_revision,applied_route_input_sha256,
			applied_rules_version,applied_sampling_version,applied_path_policy_version,applied_generations,processing_finished_at)
			VALUES($1,$2,2,decode(repeat('11',32),'hex'),'map_data_ready',transaction_timestamp(),'stale',1,
			decode(repeat('11',32),'hex'),'coverage-experimental-v1','coverage-sampling-experimental-v1',
			'coverage-path-policy-experimental-v82','[{"regionId":"geofabrik:test","generation":1}]',transaction_timestamp())`, account, workout)
	}
	for _, path := range paths {
		exec(`INSERT INTO app.coverage_paths(account_id,logical_path_id,name,normalized_name,broad_class)
			VALUES($1,$2,'Detail Road','detail road','road')`, account, path)
	}
	for i, line := range []string{
		"LINESTRING(-105 40,-104.9999 40.0001)",
		"LINESTRING(-104.9998 40.0002,-104.9997 40.0003)",
		"LINESTRING(-104 41,-103.9999 41.0001)",
	} {
		segment, parkText := uuid.New(), park.String()
		if i == 2 {
			parkText = strings.ToUpper(parkText)
		}
		tags, _ := json.Marshal(map[string]string{"workouts:education_id": campus.String(), "workouts:education_name": "Detail School",
			"workouts:park_id": parkText, "workouts:park_kind": "state_park", "workouts:park_name": "Detail Park", "workouts:park_normalized_name": "detail park"})
		exec(`INSERT INTO app.path_segments(account_id,region_id,generation_id,physical_segment_id,logical_path_id,
			derivation_version,source_way_id,source_way_version,name,normalized_name,highway,broad_class,tags,segment_meters,geom)
			VALUES($1,'geofabrik:test',1,$2,$3,4,$4,1,'Detail Road','detail road','residential','road',$5::jsonb,30,ST_GeomFromText($6,4326))`,
			account, segment, paths[i%2], int64(i+1), string(tags), line)
		for j, workout := range fixture.workouts[:2] {
			if j == 1 && i != 0 {
				continue
			}
			exec(`INSERT INTO app.workout_segment_matches(account_id,workout_id,physical_segment_id,region_id,generation_id,
				logical_path_id,first_traversed_at,first_route_order,covered_meters,geom)
				SELECT account_id,$2,physical_segment_id,region_id,generation_id,logical_path_id,
				'2026-03-07T15:01:00Z',$4,30,ST_Multi(geom) FROM app.path_segments WHERE account_id=$1 AND physical_segment_id=$3`, account, workout, segment, i)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	tx = integrationAccountTransaction(t, adminDB, account)
	defer tx.Rollback(context.Background())
	var generation int64
	if err := tx.QueryRow(ctx, `SELECT generation FROM app.account_data_generations WHERE account_id=$1`, account).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uuid.UUID{selection, narrow} {
		end := "2026-03-08"
		focused := fixture.workouts[1]
		if id == narrow {
			end = "2026-03-07"
			focused = fixture.workouts[0]
		}
		exec(`INSERT INTO app.map_selections(id,account_id,session_id,generation,expires_at,start_date,end_date,selection_kind,focused_workout_id)
			VALUES($1,$2,$3,$4,transaction_timestamp()+interval '30 minutes','2026-03-07',$5::date,'explicit_subset',$6)`,
			id, account, session, generation, end, focused)
		// The full-range subset excludes the first workout. Detail must still
		// include its geometry; the narrow selection has a valid in-range focus.
		exec(`INSERT INTO app.map_selection_workouts(account_id,selection_id,workout_id,sort_order) VALUES($1,$2,$3,0)`, account, id, focused)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	server := &Server{db: apiDB}
	for _, id := range []uuid.UUID{selection, narrow} {
		prepared, err := server.sessionAccountTransactionWithOptions(ctx, authenticatedSession{accountID: &account, sessionID: session}, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
		if err != nil {
			t.Fatal(err)
		}
		defer prepared.Rollback(context.Background())
		var contextID uuid.UUID
		var revision int64
		var policy string
		if err := prepared.QueryRow(ctx, `SELECT * FROM app.prepare_coverage_display_context($1,$2,$3,$4)`, account, session, id, generation).
			Scan(&contextID, &revision, &policy); err != nil || revision < 1 || policy != "coverage-display-identity-v1" {
			t.Fatalf("selection preparation: revision=%d policy=%s err=%v", revision, policy, err)
		}
		if err := prepared.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	readTx := func(t *testing.T, contextAccount uuid.UUID) pgx.Tx {
		t.Helper()
		tx, err := server.sessionAccountTransactionWithOptions(ctx, authenticatedSession{accountID: &contextAccount, sessionID: session}, pgx.TxOptions{AccessMode: pgx.ReadOnly})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
		return tx
	}
	canonical := make(map[string]uuid.UUID)
	for _, selected := range []uuid.UUID{selection, narrow} {
		listTx := readTx(t, account)
		body, err := queryBulkRoadCoveragePaths(ctx, listTx, []any{account, session, selected, generation, nil}, "entity_kind,entity_id")
		var list generated.RoadCoverageList
		if err != nil || json.Unmarshal(body, &list) != nil || len(list.Items) != 2 {
			t.Fatalf("expected one canonical path and park group: body=%s err=%v", body, err)
		}
		for _, expected := range list.Items {
			targetKind := string(expected.EntityKind)
			targetID, valid := parseCompactUUID(expected.EntityId)
			if !valid || targetID == campus || targetID == park {
				t.Fatalf("list must return canonical identities: %+v", expected)
			}
			canonical[targetKind] = targetID
			t.Run(targetKind+"/"+selected.String(), func(t *testing.T) {
				tx := readTx(t, account)
				actual, err := queryRoadCoverageEntityDetail(ctx, tx, account, session, selected, generation, targetKind, targetID)
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := json.Marshal(actual)
				if err != nil {
					t.Fatal(err)
				}
				var metadata generated.RoadCoverageEntity
				if err := json.Unmarshal(encoded, &metadata); err != nil || !reflect.DeepEqual(metadata, expected) {
					t.Fatalf("canonical detail/list metadata mismatch: actual=%+v expected=%+v err=%v", metadata, expected, err)
				}
				var readonly, jit string
				if err := tx.QueryRow(ctx, `SELECT current_setting('transaction_read_only'),current_setting('jit')`).Scan(&readonly, &jit); err != nil || readonly != "on" || jit != "off" {
					t.Fatalf("detail must stay read-only with local JIT off: readonly=%s jit=%s err=%v", readonly, jit, err)
				}
				if actual.AllTimeWorkoutCount != 2 || (selected == narrow && actual.RangeWorkoutCount != 1) ||
					(selected == selection && actual.RangeWorkoutCount != 2) {
					t.Fatalf("detail changed date range/stale-applied/subset semantics: %+v", actual)
				}
				geometry, err := actual.Geometry.MarshalJSON()
				if err != nil {
					t.Fatal(err)
				}
				var pieces int
				if err := tx.QueryRow(ctx, `SELECT ST_NumGeometries(ST_GeomFromGeoJSON($1::text))`, string(geometry)).Scan(&pieces); err != nil || pieces != 3 {
					t.Fatalf("canonical detail must retain all three disconnected matched pieces, independent of checked workouts: pieces=%d err=%v", pieces, err)
				}
			})
		}
	}
	for _, denied := range []struct {
		name                                                string
		contextAccount, account, session, selection, entity uuid.UUID
		generation                                          int64
		kind                                                string
	}{
		{"foreign context", foreignAccount, account, session, selection, canonical["path"], generation, "path"},
		{"foreign account", account, foreignAccount, session, selection, canonical["path"], generation, "path"},
		{"foreign session", account, account, uuid.New(), selection, canonical["path"], generation, "path"},
		{"missing selection", account, account, session, uuid.New(), canonical["path"], generation, "path"},
		{"wrong generation", account, account, session, selection, canonical["path"], generation + 1, "path"},
		{"missing entity", account, account, session, selection, uuid.New(), generation, "path"},
		{"wrong kind", account, account, session, selection, canonical["path"], generation, "park"},
	} {
		t.Run(denied.name, func(t *testing.T) {
			tx := readTx(t, denied.contextAccount)
			_, err := queryRoadCoverageEntityDetail(ctx, tx, denied.account, denied.session, denied.selection, denied.generation, denied.kind, denied.entity)
			if !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("denied detail must return no row: %v", err)
			}
		})
	}
	for _, relation := range []string{"coverage_display_contexts", "coverage_display_members", "coverage_display_visits", "coverage_display_entities"} {
		tx := readTx(t, account)
		var allowed bool
		err := tx.QueryRow(ctx, `SELECT has_table_privilege(current_user,$1,'SELECT')`, "app."+relation).Scan(&allowed)
		_ = tx.Rollback(ctx)
		if err != nil || allowed {
			t.Fatalf("detail must not require private-fact SELECT: relation=%s allowed=%t err=%v", relation, allowed, err)
		}
	}
}
