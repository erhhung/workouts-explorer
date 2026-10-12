package migrations_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestRawRouteTilePrefilterSemantics(t *testing.T) {
	db := openTestDatabases(t)
	account, principal, session, selection := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	tx := beginAccount(t, db.ctx, db.migration, account)
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.accounts(id) VALUES($1)`, account); err != nil {
		t.Fatal(err)
	}
	identity := strings.ReplaceAll(principal.String(), "-", "")
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.authentication_principals
		(id,role,username,canonical_username,email,canonical_email,canonicalization_version,password_hash,full_name)
		VALUES($1,'user',$2,$2,$3,$3,1,'test-hash','Raw Route Test')`, principal, "route"+identity, identity+"@example.test"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.users(principal_id,account_id) VALUES($1,$2)`, principal, account); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.sessions(id,principal_id,credential_kind,credential_verifier,expires_at)
		VALUES($1,$2,'bearer',$3,transaction_timestamp()+interval '1 hour')`, session, principal, bytes.Repeat([]byte{0x71}, 32)); err != nil {
		t.Fatal(err)
	}
	// As in the coverage fixtures, bypass ingest fences and unrelated source FKs
	// only while inserting synthetic workouts; tile authorization runs normally.
	if _, err := tx.Exec(db.ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Fatal(err)
	}
	workoutType := uuid.New()
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.workout_types(id,account_id,type_key,provider_label)
		VALUES($1,$2,'outdoor-run','Outdoor Run')`, workoutType, account); err != nil {
		t.Fatal(err)
	}
	fixtures := []struct {
		id       uuid.UUID
		wkt      string
		selected bool
		deleted  bool
	}{
		{uuid.New(), "MULTILINESTRING((-122 37,-122.0002 37.0002),(-122.0003 37.0003,-122.0003 37.0003),(-120 35,-120 35))", true, false},
		{uuid.New(), "MULTILINESTRING((-122.0005 37.0005,-122.0005 37.0005))", true, false},
		{uuid.New(), "MULTILINESTRING((-120 35,-120 35),(-120.001 35.001,-120.002 35.002))", true, false},
		{uuid.New(), "MULTILINESTRING((-122.0007 37.0007,-122.0007 37.0007))", false, false},
		{uuid.New(), "MULTILINESTRING((-122.0009 37.0009,-122.0009 37.0009))", true, true},
	}
	for _, fixture := range fixtures {
		if _, err := tx.Exec(db.ctx, `INSERT INTO app.workouts(id,account_id,source_id,source_file_id,workout_type_id,
			provider_id,content_sha256,provider_label,started_at,ended_at,local_start_date,provider_duration,deletion_requested_at,deletion_target_id)
			VALUES($1,$2,$3,$4,$5,$1::uuid::text,$6,'Outdoor Run',transaction_timestamp(),transaction_timestamp(),current_date,0,
			CASE WHEN $7 THEN transaction_timestamp() END,CASE WHEN $7 THEN $1::uuid END)`, fixture.id, account, uuid.New(), uuid.New(), workoutType,
			bytes.Repeat([]byte{0x72}, 32), fixture.deleted); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(db.ctx, `INSERT INTO app.workout_routes(account_id,workout_id,point_count,
			minimum_longitude,minimum_latitude,maximum_longitude,maximum_latitude,has_complete_altitude,route)
			VALUES($1,$2,6,-122.001,35,-120,37.001,false,ST_GeomFromText($3,4326))`, account, fixture.id, fixture.wkt); err != nil {
			t.Fatal(err)
		}
	}
	var generation int64
	if err := tx.QueryRow(db.ctx, `SELECT generation FROM app.account_data_generations WHERE account_id=$1`, account).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(db.ctx, `INSERT INTO app.map_selections(id,account_id,session_id,generation,expires_at,start_date,end_date,selection_kind)
		VALUES($1,$2,$3,$4,transaction_timestamp()+interval '30 minutes',current_date,current_date,'explicit_subset')`, selection, account, session, generation); err != nil {
		t.Fatal(err)
	}
	for index, fixture := range fixtures {
		if fixture.selected {
			if _, err := tx.Exec(db.ctx, `INSERT INTO app.map_selection_workouts(selection_id,account_id,workout_id,sort_order)
				VALUES($1,$2,$3,$4)`, selection, account, fixture.id, index+1); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := tx.Exec(db.ctx, `SET LOCAL session_replication_role='origin'`); err != nil {
		t.Fatal(err)
	}

	scope := map[string]any{"target_account_id": account.String(), "target_session_id": session.String(),
		"target_selection_id": selection.String(), "target_generation": generation}
	queryParams, err := json.Marshal(scope)
	if err != nil {
		t.Fatal(err)
	}
	var expectedTile []byte
	if err := tx.QueryRow(db.ctx, `SELECT ST_AsMVT(rows,'routes',4096,'geometry') FROM (
		SELECT upper(replace(workout_id::text,'-','')) "workoutId", 'outdoor-run'::text "workoutTypeKey",
		'Outdoor Run'::text "workoutType", sort_order "sortOrder",
		ST_AsMVTGeom(ST_Transform(ST_GeomFromText(wkt,4326),3857),ST_TileEnvelope(14,2639,6377),4096,64,true) geometry
		FROM (VALUES ($1::uuid,1,'LINESTRING(-122 37,-122.0002 37.0002)'),
		($1::uuid,1,'POINT(-122.0003 37.0003)'),($2::uuid,2,'POINT(-122.0005 37.0005)')) expected(workout_id,sort_order,wkt)) rows`,
		fixtures[0].id, fixtures[1].id).Scan(&expectedTile); err != nil {
		t.Fatal(err)
	}
	expected := decodeCoverageMVT(t, expectedTile)
	if len(expected["routes"]) != 3 {
		t.Fatalf("expected fixture has %d features, want line and two point markers", len(expected["routes"]))
	}
	t.Run("schema 20", func(t *testing.T) {
		var version, minimum int
		if err := tx.QueryRow(db.ctx, `SELECT schema_version,minimum_runtime_version FROM app.schema_metadata WHERE singleton`).Scan(&version, &minimum); err != nil || version != 20 || minimum != 18 {
			t.Fatalf("metadata=%d/%d, want 20/18 err=%v", version, minimum, err)
		}
		var boundaries bool
		if err := tx.QueryRow(db.ctx, `SELECT pg_get_userbyid(p.proowner)='workouts_security_owner'
				AND p.prosecdef AND p.provolatile='s' AND p.proconfig=ARRAY['search_path=pg_catalog, app, public']
				AND has_function_privilege('workouts_tiles',p.oid,'EXECUTE')
				AND NOT EXISTS(SELECT 1 FROM aclexplode(p.proacl) acl WHERE acl.grantee=0)
				AND NOT has_function_privilege('workouts_api',p.oid,'EXECUTE')
				AND NOT has_function_privilege('workouts_worker',p.oid,'EXECUTE')
				AND NOT has_function_privilege('workouts_coverage_worker',p.oid,'EXECUTE')
				AND NOT has_table_privilege('workouts_tiles','app.workout_routes','SELECT')
				FROM pg_proc p WHERE p.oid='app.raw_route_mvt(integer,integer,integer,json)'::regprocedure`).Scan(&boundaries); err != nil || !boundaries {
			t.Fatalf("tile security boundaries=%t err=%v", boundaries, err)
		}
		var tile []byte
		if err := tx.QueryRow(db.ctx, `SELECT app.raw_route_mvt(14,2639,6377,$1::json)`, string(queryParams)).Scan(&tile); err != nil {
			t.Fatal(err)
		}
		// Compare decoded features without relying on UNION execution order.
		actual := decodeCoverageMVT(t, tile)["routes"]
		if len(actual) != 3 {
			t.Fatalf("route features=%v, want line and two markers only", actual)
		}
		for _, want := range expected["routes"] {
			matches := 0
			for _, got := range actual {
				if reflect.DeepEqual(got, want) {
					matches++
				}
			}
			if matches != 1 {
				t.Fatalf("feature %v occurs %d times, want once", want, matches)
			}
		}
		if err := tx.QueryRow(db.ctx, `SELECT app.raw_route_mvt(14,8192,8192,$1::json)`, string(queryParams)).Scan(&tile); err != nil || tile == nil || len(tile) != 0 {
			t.Fatalf("off-route tile=%x, want non-NULL empty bytea err=%v", tile, err)
		}
		for _, invalid := range []struct {
			name     string
			mutation string
			target   uuid.UUID
			params   string
			code     string
		}{
			{"extra query arg", "", uuid.Nil, strings.TrimSuffix(string(queryParams), "}") + `,"unexpected":true}`, "22023"},
			{"malformed scope", "", uuid.Nil, strings.Replace(string(queryParams), account.String(), "not-a-uuid", 1), "22023"},
			{"wrong account", "", uuid.Nil, strings.Replace(string(queryParams), account.String(), uuid.NewString(), 1), "42501"},
			{"wrong session", "", uuid.Nil, strings.Replace(string(queryParams), session.String(), uuid.NewString(), 1), "42501"},
			{"expired selection", `UPDATE app.map_selections SET created_at=transaction_timestamp()-interval '1 hour',expires_at=transaction_timestamp()-interval '1 second' WHERE id=$1`, selection, "", "42501"},
			{"revoked session", `UPDATE app.sessions SET revoked_at=transaction_timestamp() WHERE id=$1`, session, "", "42501"},
			{"expired session", `UPDATE app.sessions SET created_at=transaction_timestamp()-interval '1 hour',expires_at=transaction_timestamp()-interval '1 second' WHERE id=$1`, session, "", "42501"},
			{"disabled principal", `UPDATE app.authentication_principals SET disabled_at=transaction_timestamp() WHERE id=$1`, principal, "", "42501"},
			{"inactive account", `UPDATE app.accounts SET state='deleting' WHERE id=$1`, account, "", "42501"},
			{"stale generation", `UPDATE app.account_data_generations SET generation=generation+1 WHERE account_id=$1`, account, "", "42501"},
		} {
			savepoint, err := tx.Begin(db.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if invalid.mutation != "" {
				// Permit deliberately invalid authorization state, not invalid writes
				// through normal map-selection or account-lifecycle triggers.
				if _, err := savepoint.Exec(db.ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
					t.Fatal(err)
				}
				if _, err := savepoint.Exec(db.ctx, invalid.mutation, invalid.target); err != nil {
					t.Fatal(err)
				}
				if _, err := savepoint.Exec(db.ctx, `SET LOCAL session_replication_role='origin'`); err != nil {
					t.Fatal(err)
				}
			}
			params := invalid.params
			if params == "" {
				params = string(queryParams)
			}
			_, err = savepoint.Exec(db.ctx, `SELECT app.raw_route_mvt(14,2639,6377,$1::json)`, params)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != invalid.code {
				t.Fatalf("%s error=%v, want SQLSTATE %s", invalid.name, err, invalid.code)
			}
			if err := savepoint.Rollback(db.ctx); err != nil {
				t.Fatal(err)
			}
		}
	})
}
