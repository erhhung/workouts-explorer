package migrations_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/api/migrations"
	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// Never connect through ambient URLs without an explicit synthetic-only opt-in.
func tileTestDatabases(t *testing.T) testDatabases {
	t.Helper()
	if os.Getenv("TILE_GEOMETRY_TEST_DISPOSABLE") != "1" {
		t.Skip("TILE_GEOMETRY_TEST_DISPOSABLE=1 and migrated synthetic loopback role URLs required")
	}
	var endpoint string
	for _, name := range []string{"MIGRATION_DATABASE_URL", "API_DATABASE_URL", "WORKER_DATABASE_URL"} {
		value := os.Getenv(name)
		cfg, err := pgx.ParseConfig(value)
		if value == "" || err != nil {
			t.Fatal("explicit synthetic role URL required: " + name)
		}
		privateSynthetic := cfg.Host == "/tmp/opencode/tile-local031-rehearsal/socket" && cfg.Database == "schema031_reference_test"
		if (!privateSynthetic && cfg.Host != "127.0.0.1" && cfg.Host != "localhost" && cfg.Host != "::1") ||
			!strings.Contains(cfg.Database, "test") || strings.Contains(cfg.Database, "clone") ||
			strings.Contains(cfg.Database, "actual") || strings.Contains(cfg.Database, "live") {
			t.Fatal("tile tests prohibit non-synthetic endpoints")
		}
		for _, fallback := range cfg.Fallbacks {
			if fallback.Host != cfg.Host || fallback.Port != cfg.Port {
				t.Fatal("tile tests prohibit alternate endpoints")
			}
		}
		target := fmt.Sprintf("%s:%d/%s", cfg.Host, cfg.Port, cfg.Database)
		if endpoint != "" && endpoint != target {
			t.Fatal("tile test roles must address the same database")
		}
		endpoint = target
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	t.Cleanup(cancel)
	db := testDatabases{ctx: ctx, migration: openPool(t, ctx, os.Getenv("MIGRATION_DATABASE_URL"), 4),
		api: openPool(t, ctx, os.Getenv("API_DATABASE_URL"), 2), worker: openPool(t, ctx, os.Getenv("WORKER_DATABASE_URL"), 2)}
	coverageDisplayCheck(t, db, fmt.Sprintf(`SELECT schema_version=%d AND minimum_runtime_version=31 AND migration_baseline=27 FROM app.schema_metadata WHERE singleton`, database.SupportedSchemaVersion))
	return db
}

func tileFixtureAdmin(t *testing.T, db testDatabases) *pgxpool.Pool {
	t.Helper()
	value := os.Getenv("TILE_FIXTURE_ADMIN_DATABASE_URL")
	cfg, err := pgx.ParseConfig(value)
	expected := db.migration.Config().ConnConfig
	if err != nil || value == "" || cfg.Host != expected.Host || cfg.Port != expected.Port || cfg.Database != expected.Database || !strings.Contains(cfg.Database, "test") || strings.Contains(cfg.Database, "actual") || strings.Contains(cfg.Database, "clone") {
		t.Fatal("deliberately invalid lifecycle fixtures require an explicit same-synthetic-database admin URL")
	}
	return openPool(t, db.ctx, value, 1)
}

func TestTileChunkGeometryPostgreSQL(t *testing.T) {
	db := tileTestDatabases(t)
	for _, query := range []struct{ name, sql string }{
		{"bounded independent components and dots", `WITH source AS (
			SELECT ST_Collect(ARRAY[ST_MakeLine(ARRAY(SELECT ST_SetSRID(ST_MakePoint(10+i*.00001,20),4326)
			FROM generate_series(0,1024) i ORDER BY i)),ST_GeomFromText('LINESTRING(11 21,11.001 21.001)',4326),
			ST_GeomFromText('LINESTRING(12 22,12 22)',4326)]) geom
		), chunks AS (SELECT c.* FROM source CROSS JOIN LATERAL app.project_tile_chunks(source.geom) c)
		SELECT count(*)=7 AND max(ST_NPoints(geom))<=256 AND count(DISTINCT component)=3
			AND count(*) FILTER(WHERE GeometryType(geom)='POINT')=1
			AND ST_Equals(ST_UnaryUnion(ST_Collect(geom) FILTER(WHERE component=1)),
				(SELECT ST_Transform(ST_GeometryN(geom,1),3857) FROM source)) FROM chunks`},
		{"partial overlapping reversed evidence retains real gap", `WITH pieces AS (
			SELECT geom FROM (VALUES ('MULTILINESTRING((10 20,10.002 20),(10.008 20,10.01 20))'),
			('MULTILINESTRING((10.003 20,10.001 20))'),('MULTILINESTRING((10.008 20,10.01 20))')) v(wkt)
			CROSS JOIN LATERAL app.project_tile_chunks(ST_GeomFromText(wkt,4326)) c
		), dedup AS (SELECT DISTINCT ST_AsEWKB(geom) ewkb FROM pieces), dissolved AS (
			SELECT ST_UnaryUnion(ST_Collect(ST_GeomFromEWKB(ewkb))) geom FROM dedup)
		SELECT ST_Equals(geom,ST_Transform(ST_GeomFromText('MULTILINESTRING((10 20,10.003 20),(10.008 20,10.01 20))',4326),3857))
			AND NOT ST_Intersects(geom,ST_Transform(ST_SetSRID(ST_MakePoint(10.005,20),4326),3857)) FROM dissolved`},
		{"LOD endpoints short lines and error bound", `WITH source AS (
			SELECT ST_SetSRID(ST_MakeLine(ARRAY(SELECT ST_MakePoint(i*.1,CASE WHEN i%2=0 THEN 0 ELSE .1 END)
			FROM generate_series(0,250) i ORDER BY i)),3857) geom
		), levels AS (SELECT z,geom,app.tile_display_geometry(geom,z) display FROM source CROSS JOIN generate_series(0,22) z)
		SELECT bool_and(ST_Equals(ST_StartPoint(geom),ST_StartPoint(display)) AND ST_Equals(ST_EndPoint(geom),ST_EndPoint(display))
			AND ST_NPoints(display)>=2 AND (CASE WHEN z>=15 THEN ST_AsEWKB(geom)=ST_AsEWKB(display)
			ELSE ST_HausdorffDistance(geom,display)<=40075016.68557849/power(2.0,z)/2048.0 END)) FROM levels`},
		{"buffer and adjacent tile clipping", `WITH bounds AS (SELECT ST_TileEnvelope(15,17294,14525) geom), source AS (
			SELECT ST_MakeLine(ST_SetSRID(ST_MakePoint(ST_XMax(geom)-10,ST_YMin(geom)+100),3857),
			ST_SetSRID(ST_MakePoint(ST_XMax(geom)+10,ST_YMin(geom)+100),3857)) geom FROM bounds
		), display AS (SELECT app.tile_display_geometry(source.geom,14) geom FROM source), clips AS (
			SELECT ST_Intersection(display.geom,ST_Expand(bounds.geom,(ST_XMax(bounds.geom)-ST_XMin(bounds.geom))/64)) geom FROM display,bounds)
		SELECT ST_Equals(clips.geom,display.geom) AND ST_NPoints(app.tile_display_geometry(ST_SetSRID(ST_MakeLine(ST_MakePoint(1,1),ST_MakePoint(1.00001,1)),3857),0))=2
			AND GeometryType(app.tile_display_geometry(ST_SetSRID(ST_MakePoint(1,1),3857),0))='POINT' FROM clips,display`},
	} {
		t.Run(query.name, func(t *testing.T) { coverageDisplayCheck(t, db, query.sql) })
	}
}

func TestTileChunkMaintenanceAndRawIdentityPostgreSQL(t *testing.T) {
	db := tileTestDatabases(t)
	f := newCanonicalDisplayFixture(t, db)
	tx := beginAccount(t, db.ctx, db.migration, f.account)
	defer tx.Rollback(db.ctx)
	canonicalDisplayFence(t, db, tx, f)
	// Long continuous geometry plus a genuinely separate stationary component.
	coverageDisplayExec(t, db, tx, `WITH source AS (
		SELECT ST_Collect(ST_MakeLine(ARRAY(SELECT ST_SetSRID(ST_MakePoint(10+i*.0001,20),4326)
		FROM generate_series(0,100000) i ORDER BY i)),ST_GeomFromText('LINESTRING(10.0005 20.0005,10.0005 20.0005)',4326)) geom)
		UPDATE app.workout_routes route SET route=ST_Multi(source.geom),point_count=100003 FROM source
		WHERE route.account_id=$1 AND route.workout_id=ANY($2::uuid[])`, f.account, f.workouts[:2])
	var valid bool
	if err := tx.QueryRow(db.ctx, `SELECT count(*)=788 AND max(ST_NPoints(geom))<=256
		AND count(*) FILTER(WHERE GeometryType(geom)='POINT')=2 FROM app.raw_route_tile_chunks
		WHERE account_id=$1 AND workout_id=ANY($2::uuid[])`, f.account, f.workouts[:2]).Scan(&valid); err != nil || !valid {
		t.Fatalf("raw chunks not bounded/independent: %t %v", valid, err)
	}
	// A rolled-back replacement must not leave either stale or new derived rows.
	before := tileChunkSnapshot(t, db, tx, f.account)
	savepoint, err := tx.Begin(db.ctx)
	if err != nil {
		t.Fatal(err)
	}
	coverageDisplayExec(t, db, savepoint, `UPDATE app.workout_routes SET route=NULL WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0])
	if err := savepoint.Rollback(db.ctx); err != nil {
		t.Fatal(err)
	}
	if tileChunkSnapshot(t, db, tx, f.account) != before {
		t.Fatal("raw chunk replacement leaked across savepoint rollback")
	}
	coverageDisplayCommit(t, db, tx)
	s := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", f.workouts[:2])
	for _, z := range []int{12, 15} {
		x, y := tileXY(10.001, 20.001, z)
		var local int
		if err := db.migration.QueryRow(db.ctx, `WITH bounds AS (SELECT ST_TileEnvelope($3,$4,$5) geom)
			SELECT count(*) FROM app.raw_route_tile_chunks chunk,bounds WHERE account_id=$1 AND workout_id=ANY($2::uuid[])
			AND chunk.geom && ST_Expand(bounds.geom,(ST_XMax(bounds.geom)-ST_XMin(bounds.geom))/64)`, f.account, f.workouts[:2], z, x, y).Scan(&local); err != nil || local < 4 || local > 12 {
			t.Fatalf("off-tile route growth entered local inputs: chunks=%d z=%d err=%v", local, z, err)
		}
		var tile []byte
		if err := db.migration.QueryRow(db.ctx, `SELECT app.raw_route_mvt($5,$6,$7,json_build_object(
			'target_account_id',$1::text,'target_session_id',$2::text,'target_selection_id',$3::text,'target_generation',$4::bigint::text))`,
			f.account, f.session, s.id, s.generation, z, x, y).Scan(&tile); err != nil {
			t.Fatal(err)
		}
		features := decodeCoverageMVT(t, tile)["routes"]
		seen := map[string]int{}
		for _, feature := range features {
			seen[feature.properties["workoutId"].(string)]++
		}
		if len(features) != 4 || len(seen) != 2 {
			t.Fatalf("identical workouts must remain two lines plus two dots at z%d: %v", z, features)
		}
		for order, workout := range f.workouts[:2] {
			id := strings.ToUpper(strings.ReplaceAll(workout.String(), "-", ""))
			if seen[id] != 2 {
				t.Fatalf("workout %s lost its line/dot", id)
			}
			for _, feature := range features {
				if feature.properties["workoutId"] == id && feature.properties["sortOrder"] != uint64(order) {
					t.Fatalf("raw ordering changed: %v", feature.properties)
				}
			}
		}
	}
	tx = beginAccount(t, db.ctx, db.migration, f.account)
	defer tx.Rollback(db.ctx)
	canonicalDisplayFence(t, db, tx, f)
	coverageDisplayExec(t, db, tx, `DELETE FROM app.workout_routes WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0])
	if err := tx.QueryRow(db.ctx, `SELECT NOT EXISTS(SELECT 1 FROM app.raw_route_tile_chunks WHERE account_id=$1 AND workout_id=$2)`, f.account, f.workouts[0]).Scan(&valid); err != nil || !valid {
		t.Fatalf("raw summary deletion retained chunks: %v", err)
	}
	coverageDisplayExec(t, db, tx, `DELETE FROM app.workout_segment_matches WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0])
	if err := tx.QueryRow(db.ctx, `SELECT NOT EXISTS(SELECT 1 FROM app.coverage_tile_chunks WHERE account_id=$1 AND workout_id=$2)`, f.account, f.workouts[0]).Scan(&valid); err != nil || !valid {
		t.Fatalf("match deletion retained chunks: %v", err)
	}
}

func tileChunkSnapshot(t *testing.T, db testDatabases, tx pgx.Tx, account uuid.UUID) string {
	t.Helper()
	var snapshot string
	if err := tx.QueryRow(db.ctx, `SELECT md5(string_agg(workout_id::text||component||':'||piece||':'||encode(ST_AsEWKB(geom),'hex'),
		',' ORDER BY workout_id,component,piece)) FROM app.raw_route_tile_chunks WHERE account_id=$1`, account).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func tileXY(lon, lat float64, z int) (int, int) {
	return int(math.Floor((lon + 180) / 360 * math.Exp2(float64(z)))),
		int(math.Floor((1 - math.Asinh(math.Tan(lat*math.Pi/180))/math.Pi) / 2 * math.Exp2(float64(z))))
}

func TestCoverageChunkPartialMaintenancePostgreSQL(t *testing.T) {
	db := tileTestDatabases(t)
	f := newCanonicalDisplayFixture(t, db)
	tx := beginAccount(t, db.ctx, db.migration, f.account)
	defer tx.Rollback(db.ctx)
	savepoint, err := tx.Begin(db.ctx)
	if err != nil {
		t.Fatal(err)
	}
	coverageDisplayExec(t, db, savepoint, `UPDATE app.workout_segment_matches match SET geom=ST_Multi(ST_Collect(
		ST_LineSubstring(segment.geom,0,.2),ST_LineSubstring(segment.geom,.8,1)))
		FROM app.path_segments segment WHERE match.account_id=$1 AND match.workout_id=$2 AND match.physical_segment_id=$3
		AND segment.account_id=match.account_id AND segment.region_id=match.region_id
		AND segment.generation_id=match.generation_id AND segment.physical_segment_id=match.physical_segment_id`, f.account, f.workouts[0], f.segments[0])
	for range 2 {
		var valid bool
		if err := savepoint.QueryRow(db.ctx, `WITH chunks AS (
			SELECT count(*) n,ST_UnaryUnion(ST_Collect(geom)) geom FROM app.coverage_tile_chunks
			WHERE account_id=$1 AND workout_id=$2 AND physical_segment_id=$3)
			SELECT chunks.n=2 AND ST_Equals(chunks.geom,ST_Transform(match.geom,3857))
			AND NOT ST_Intersects(chunks.geom,ST_Transform(ST_LineInterpolatePoint(segment.geom,.5),3857))
			FROM chunks,app.workout_segment_matches match JOIN app.path_segments segment
			USING(account_id,region_id,generation_id,physical_segment_id)
			WHERE match.account_id=$1 AND match.workout_id=$2 AND match.physical_segment_id=$3`,
			f.account, f.workouts[0], f.segments[0]).Scan(&valid); err != nil || !valid {
			t.Fatalf("partial/reversed chunk maintenance lost evidence or filled a gap: %v", err)
		}
		coverageDisplayExec(t, db, savepoint, `UPDATE app.workout_segment_matches SET geom=ST_Reverse(geom)
			WHERE account_id=$1 AND workout_id=$2 AND physical_segment_id=$3`, f.account, f.workouts[0], f.segments[0])
	}
	if err := savepoint.Rollback(db.ctx); err != nil {
		t.Fatal(err)
	}
	var restored bool
	if err := tx.QueryRow(db.ctx, `SELECT count(*)=1 FROM app.coverage_tile_chunks
		WHERE account_id=$1 AND workout_id=$2 AND physical_segment_id=$3`, f.account, f.workouts[0], f.segments[0]).Scan(&restored); err != nil || !restored {
		t.Fatalf("coverage rollback retained partial render pieces: %v", err)
	}
}

func TestAuthorizeMapTilePostgreSQL(t *testing.T) {
	db := tileTestDatabases(t)
	f := newCanonicalDisplayFixture(t, db)
	s := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", f.workouts[:1])
	for _, coverage := range []bool{false, true} {
		tx, err := db.api.BeginTx(db.ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
		if err != nil {
			t.Fatal(err)
		}
		coverageDisplayExec(t, db, tx, `SELECT set_config('app.account_id',$1,true),set_config('app.session_id',$2,true)`,
			f.account.String(), f.session.String())
		var expiry, expected time.Time
		if err := tx.QueryRow(db.ctx, `SELECT app.authorize_map_tile($1,$2,$3,$4,$5),
			LEAST(selection.expires_at,session_row.expires_at) FROM app.map_selections selection
			JOIN app.sessions session_row ON session_row.id=selection.session_id WHERE selection.id=$3`,
			f.account, f.session, s.id, s.generation, coverage).Scan(&expiry, &expected); err != nil || !expiry.Equal(expected) {
			t.Fatalf("authorization expiry=%v want=%v: %v", expiry, expected, err)
		}
		for _, invalid := range []struct {
			account, session, selection uuid.UUID
			generation                  int64
		}{{uuid.New(), f.session, s.id, s.generation}, {f.account, uuid.New(), s.id, s.generation},
			{f.account, f.session, uuid.New(), s.generation}, {f.account, f.session, s.id, s.generation + 1}} {
			var denied bool
			if err := tx.QueryRow(db.ctx, `SELECT app.authorize_map_tile($1,$2,$3,$4,$5) IS NULL`,
				invalid.account, invalid.session, invalid.selection, invalid.generation, coverage).Scan(&denied); err != nil || !denied {
				t.Fatalf("foreign/stale authorization accepted: %v", err)
			}
		}
		var nullDenied bool
		if err := tx.QueryRow(db.ctx, `SELECT app.authorize_map_tile($1,$2,$3,NULL,$4) IS NULL
			AND app.authorize_map_tile($1,$2,$3,$5,NULL) IS NULL`, f.account, f.session, s.id, coverage, s.generation).Scan(&nullDenied); err != nil || !nullDenied {
			t.Fatalf("NULL authorization scope was accepted: %v", err)
		}
		_ = tx.Rollback(db.ctx)
	}
	for _, mutation := range []string{
		`UPDATE app.coverage_display_contexts SET ready=false WHERE account_id=$1`,
		`UPDATE app.coverage_display_revisions SET revision=revision+1 WHERE account_id=$1`,
		`UPDATE app.coverage_display_contexts SET start_date=start_date-1 WHERE account_id=$1`,
		`UPDATE app.coverage_display_contexts SET generation=generation+1 WHERE account_id=$1`,
	} {
		tx := beginAccount(t, db.ctx, db.migration, f.account)
		coverageDisplayExec(t, db, tx, mutation, f.account)
		coverageDisplayExec(t, db, tx, `SELECT set_config('app.session_id',$1,true)`, f.session.String())
		var denied, rawValid bool
		if err := tx.QueryRow(db.ctx, `SELECT app.authorize_map_tile($1,$2,$3,$4,true) IS NULL,
			app.authorize_map_tile($1,$2,$3,$4,false) IS NOT NULL`, f.account, f.session, s.id, s.generation).Scan(&denied, &rawValid); err != nil || !denied || !rawValid {
			t.Fatalf("coverage context invalidation must deny coverage only: %t/%t %v", denied, rawValid, err)
		}
		_ = tx.Rollback(db.ctx)
	}
	for _, mutation := range []string{
		`UPDATE app.sessions SET revoked_at=transaction_timestamp() WHERE id=$2`,
		`UPDATE app.sessions SET expires_at=transaction_timestamp()-interval '1 second',created_at=transaction_timestamp()-interval '1 hour' WHERE id=$2`,
		`UPDATE app.authentication_principals SET disabled_at=transaction_timestamp() WHERE id=(SELECT principal_id FROM app.sessions WHERE id=$2)`,
		`UPDATE app.accounts SET state='deleting' WHERE id=$1`,
		`UPDATE app.account_data_generations SET generation=generation+1 WHERE account_id=$1`,
		`UPDATE app.map_selections SET expires_at=transaction_timestamp()-interval '1 second',created_at=transaction_timestamp()-interval '1 hour' WHERE id=$3`,
	} {
		tx := beginAccount(t, db.ctx, tileFixtureAdmin(t, db), f.account)
		// Deliberately invalid lifecycle states are confined to a rolled-back fixture.
		coverageDisplayExec(t, db, tx, `SET LOCAL session_replication_role='replica'`)
		// Bind all three typed parameters even for mutations using only one.
		query := `WITH scope AS (SELECT $1::uuid account,$2::uuid session,$3::uuid selection) ` + mutation
		coverageDisplayExec(t, db, tx, query, f.account, f.session, s.id)
		coverageDisplayExec(t, db, tx, `SET LOCAL session_replication_role='origin'`)
		coverageDisplayExec(t, db, tx, `SELECT set_config('app.session_id',$1,true)`, f.session.String())
		var denied bool
		if err := tx.QueryRow(db.ctx, `SELECT app.authorize_map_tile($1,$2,$3,$4,true) IS NULL
			AND app.authorize_map_tile($1,$2,$3,$4,false) IS NULL`, f.account, f.session, s.id, s.generation).Scan(&denied); err != nil || !denied {
			t.Fatalf("invalid session/account/generation/selection accepted: %v", err)
		}
		_ = tx.Rollback(db.ctx)
	}
	worker := beginAccount(t, db.ctx, db.worker, f.account)
	_, err := worker.Exec(db.ctx, `SELECT app.authorize_map_tile($1,$2,$3,$4,false)`, f.account, f.session, s.id, s.generation)
	bootstrapSQLState(t, err, "42501")
	_ = worker.Rollback(db.ctx)
	for _, table := range []string{"raw_route_tile_chunks", "coverage_tile_chunks"} {
		tx := canonicalDisplayAPI(t, db, f)
		_, err := tx.Exec(db.ctx, "SELECT geom FROM app."+table+" LIMIT 1")
		bootstrapSQLState(t, err, "42501")
		_ = tx.Rollback(db.ctx)
	}
	coverageDisplayCheck(t, db, `SELECT p.provolatile='s' AND p.prosecdef
		AND has_function_privilege('workouts_api',p.oid,'EXECUTE')
		AND NOT has_function_privilege('workouts_worker',p.oid,'EXECUTE')
		AND NOT has_function_privilege('workouts_coverage_worker',p.oid,'EXECUTE')
		AND NOT has_function_privilege('workouts_tiles',p.oid,'EXECUTE')
		AND NOT has_table_privilege('workouts_api','app.raw_route_tile_chunks','SELECT')
		AND NOT has_table_privilege('workouts_api','app.coverage_tile_chunks','SELECT')
		AND NOT has_function_privilege('workouts_api','app.raw_route_mvt(integer,integer,integer,json)','EXECUTE')
		AND NOT has_function_privilege('workouts_api','app.coverage_mvt(integer,integer,integer,json)','EXECUTE')
		FROM pg_proc p WHERE p.oid='app.authorize_map_tile(uuid,uuid,uuid,bigint,boolean)'::regprocedure`)
}

func TestTileMigrationBackfillPostgreSQL(t *testing.T) {
	const name = "TILE_GEOMETRY_UPGRADE_DATABASE_URL"
	dsn := os.Getenv(name)
	if dsn == "" || os.Getenv("TILE_GEOMETRY_TEST_DISPOSABLE") != "1" {
		t.Skip("explicit empty synthetic upgrade database and TILE_GEOMETRY_TEST_DISPOSABLE=1 required")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid synthetic upgrade configuration")
	}
	if (cfg.Host != "127.0.0.1" && cfg.Host != "localhost" && cfg.Host != "::1") || !strings.Contains(cfg.Database, "test") ||
		strings.Contains(cfg.Database, "clone") || strings.Contains(cfg.Database, "actual") || strings.Contains(cfg.Database, "live") {
		t.Fatal("upgrade tests require an empty synthetic loopback database")
	}
	for _, fallback := range cfg.Fallbacks {
		if fallback.Host != cfg.Host || fallback.Port != cfg.Port {
			t.Fatal("alternate upgrade endpoints are prohibited")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	var empty bool
	if err := admin.QueryRow(ctx, `SELECT to_regnamespace('app') IS NULL AND to_regclass('public.goose_db_version') IS NULL`).Scan(&empty); err != nil || !empty {
		t.Fatal("upgrade scratch database must be empty", err)
	}
	if _, err := admin.Exec(ctx, `CREATE EXTENSION IF NOT EXISTS postgis`); err != nil {
		t.Fatal(err)
	}
	migrator := cfg.Copy()
	migrator.RuntimeParams["role"] = "workouts_migration"
	sqlDB := stdlib.OpenDB(*migrator)
	defer sqlDB.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.Files)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.UpTo(ctx, 30); err != nil {
		t.Fatal(err)
	}
	pool := func(role string) *pgxpool.Pool {
		config, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatal(err)
		}
		config.ConnConfig.RuntimeParams["role"] = role
		p, err := pgxpool.NewWithConfig(ctx, config)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(p.Close)
		return p
	}
	db := testDatabases{ctx: ctx, migration: pool("workouts_migration"), api: pool("workouts_api"), worker: pool("workouts_worker")}
	f := newCanonicalDisplayFixture(t, db)
	seed := beginAccount(t, ctx, db.migration, f.account)
	canonicalDisplayFence(t, db, seed, f)
	coverageDisplayExec(t, db, seed, `UPDATE app.workout_routes SET route=ST_GeomFromText(
		'MULTILINESTRING((10 20,10.001 20.001),(11 21,11 21))',4326) WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0])
	coverageDisplayExec(t, db, seed, `UPDATE app.workout_segment_matches match SET geom=ST_Multi(ST_Collect(
		ST_LineSubstring(segment.geom,0,.2),ST_LineSubstring(segment.geom,.8,1))) FROM app.path_segments segment
		WHERE match.account_id=$1 AND match.workout_id=$2 AND match.physical_segment_id=$3
		AND segment.account_id=match.account_id AND segment.region_id=match.region_id
		AND segment.generation_id=match.generation_id AND segment.physical_segment_id=match.physical_segment_id`, f.account, f.workouts[0], f.segments[0])
	coverageDisplayCommit(t, db, seed)
	snapshot := func() string {
		tx := beginAccount(t, ctx, db.migration, f.account)
		defer tx.Rollback(ctx)
		var value string
		if err := tx.QueryRow(ctx, `SELECT jsonb_build_object(
			'routes',(SELECT jsonb_agg(to_jsonb(r) ORDER BY workout_id) FROM app.workout_routes r WHERE account_id=$1),
			'matches',(SELECT jsonb_agg(to_jsonb(m) ORDER BY workout_id,physical_segment_id) FROM app.workout_segment_matches m WHERE account_id=$1),
			'points',(SELECT jsonb_agg(to_jsonb(p) ORDER BY workout_id,sequence) FROM app.workout_route_points p WHERE account_id=$1))::text`, f.account).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := snapshot()
	// Late failure proves backfill, trigger installation, function replacement and
	// metadata publication are one transaction, with no partial render storage.
	tx, err := db.migration.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(ctx, tileLocalSQL(t)+"\nSELECT 1/0;")
	bootstrapSQLState(t, err, "22012")
	_ = tx.Rollback(ctx)
	coverageDisplayCheck(t, db, `SELECT to_regclass('app.raw_route_tile_chunks') IS NULL
		AND to_regclass('app.coverage_tile_chunks') IS NULL AND schema_version=30 AND minimum_runtime_version=30
		FROM app.schema_metadata WHERE singleton`)
	if _, err := provider.UpTo(ctx, 31); err != nil {
		t.Fatal(err)
	}
	if snapshot() != before {
		t.Fatal("backfill rewrote authoritative GPS/routes/matches")
	}
	coverageDisplayCheck(t, db, `SELECT NOT EXISTS(SELECT 1 FROM app.workout_routes r WHERE account_id=$1 AND route IS NOT NULL
		AND NOT EXISTS(SELECT 1 FROM app.raw_route_tile_chunks c WHERE c.account_id=r.account_id AND c.workout_id=r.workout_id))
		AND NOT EXISTS(SELECT 1 FROM app.workout_segment_matches m WHERE account_id=$1
		AND NOT EXISTS(SELECT 1 FROM app.coverage_tile_chunks c WHERE c.account_id=m.account_id AND c.workout_id=m.workout_id AND c.physical_segment_id=m.physical_segment_id))`, f.account)
	coverageDisplayCheck(t, db, `WITH raw_expected AS (
		SELECT route.workout_id,ST_UnaryUnion(ST_Collect(ST_Transform(CASE WHEN ST_Length(d.geom)=0
			THEN ST_StartPoint(d.geom) ELSE d.geom END,3857))) geom FROM app.workout_routes route
		CROSS JOIN LATERAL ST_Dump(route.route) d WHERE account_id=$1 GROUP BY workout_id
	), raw_actual AS (
		SELECT workout_id,ST_UnaryUnion(ST_Collect(geom)) geom FROM app.raw_route_tile_chunks WHERE account_id=$1 GROUP BY workout_id
	) SELECT bool_and(ST_Equals(expected.geom,actual.geom)) FROM raw_expected expected JOIN raw_actual actual USING(workout_id)`, f.account)
	coverageDisplayCheck(t, db, `SELECT bool_and(ST_Equals(ST_Transform(match.geom,3857),chunks.geom))
		FROM app.workout_segment_matches match JOIN LATERAL (
			SELECT ST_UnaryUnion(ST_Collect(geom)) geom FROM app.coverage_tile_chunks chunk WHERE chunk.account_id=match.account_id
			AND chunk.workout_id=match.workout_id AND chunk.physical_segment_id=match.physical_segment_id
		) chunks ON true WHERE match.account_id=$1`, f.account)
	coverageDisplayCheck(t, db, `SELECT schema_version=31 AND minimum_runtime_version=31 AND migration_baseline=27
        AND NOT EXISTS(SELECT 1 FROM app.workout_routes r CROSS JOIN (VALUES(10),(12),(14)) band(z)
        WHERE r.account_id=$1 AND r.route IS NOT NULL AND NOT EXISTS(SELECT 1 FROM app.raw_route_lod_chunks c
        WHERE c.account_id=r.account_id AND c.workout_id=r.workout_id AND c.zoom_band=band.z))
        FROM app.schema_metadata WHERE singleton`, f.account)
	worker := beginAccount(t, ctx, db.worker, f.account)
	var source uuid.UUID
	if err := worker.QueryRow(ctx, `SELECT source_id FROM app.fence_ingest_job($1,$2,$3,31)`,
		f.job, reprocessingWorker, f.lease).Scan(&source); err != nil || source == uuid.Nil {
		t.Fatal("current ingest fence failed", err)
	}
	var recorded bool
	if err := worker.QueryRow(ctx, `SELECT app.record_ingest_phase($1,$2,$3,'importing',0,1)`,
		f.job, reprocessingWorker, f.lease).Scan(&recorded); err != nil || !recorded {
		t.Fatal("phase writer failed", err)
	}
	_ = worker.Rollback(ctx)
	for _, runtime := range []*pgxpool.Pool{db.api, db.worker} {
		if !database.Ready(ctx, runtime) {
			t.Fatal("current runtime is not ready after backfill")
		}
	}
	if err := migrations.ValidateHistory(ctx, sqlDB); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Down(ctx); err == nil || !strings.Contains(err.Error(), "forward-only") {
		t.Fatalf("unsafe downgrade was not rejected: %v", err)
	}
	if err := migrations.ValidateHistory(ctx, sqlDB); err != nil {
		t.Fatal("rejected downgrade damaged history", err)
	}
}

func TestCoverageTileLocalInputCardinalityPostgreSQL(t *testing.T) {
	db := tileTestDatabases(t)
	f := newCanonicalDisplayFixture(t, db)
	// Every eligible workout repeats source zero. Add one large distant evidence
	// component so growth cannot quietly enter local transforms/dissolves.
	tx := beginAccount(t, db.ctx, db.migration, f.account)
	coverageDisplayExec(t, db, tx, `UPDATE app.workout_segment_matches SET geom=ST_Multi(ST_CollectionExtract(ST_Collect(geom,
		ST_Multi((SELECT ST_MakeLine(ARRAY(SELECT ST_SetSRID(ST_MakePoint(40+i*.0001,20),4326)
		FROM generate_series(0,20000) i ORDER BY i))))),2))
		WHERE account_id=$1 AND physical_segment_id=$2`, f.account, f.segments[0])
	// Repeat the tile-local component explicitly; the distant component must not
	// enter local processing and repeated evidence must reach one dissolve input.
	coverageDisplayExec(t, db, tx, `UPDATE app.workout_segment_matches SET geom=ST_Multi(ST_CollectionExtract(
		ST_Collect(ARRAY[geom,ST_GeometryN(geom,1),ST_GeometryN(geom,1)]),2))
		WHERE account_id=$1 AND physical_segment_id=$2`, f.account, f.segments[0])
	coverageDisplayCommit(t, db, tx)
	s := canonicalDisplaySelection(t, db, f, "2026-01-01", "2026-02-01", f.workouts)
	before := canonicalDisplayRows(t, db, f, s)
	tx = beginAccount(t, db.ctx, db.migration, f.account)
	defer tx.Rollback(db.ctx)
	coverageDisplayExec(t, db, tx, `SET LOCAL ROLE workouts_security_owner`)
	coverageDisplayExec(t, db, tx, `SET LOCAL search_path=pg_catalog,app,public`)
	coverageDisplayExec(t, db, tx, `SET LOCAL jit=off`)
	var body string
	if err := tx.QueryRow(db.ctx, `SELECT prosrc FROM pg_proc WHERE oid='app.coverage_mvt(integer,integer,integer,json)'::regprocedure`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	_, query, ok := strings.Cut(body, "    WITH local_chunks AS MATERIALIZED (")
	if !ok {
		t.Fatal("must inspect the installed tile query, not a function scan")
	}
	query, _, ok = strings.Cut(query, "    ), shapes AS (")
	if !ok {
		t.Fatal("missing tile-local dedup barrier")
	}
	query = "WITH local_chunks AS MATERIALIZED (" + query + `) SELECT
		(SELECT count(*) FROM render_inputs) checked,(SELECT count(*) FROM unique_pieces) unique_pieces`
	x, y := tileXY(10.001, 20.001, 15)
	bounds := fmt.Sprintf("ST_TileEnvelope(15,%d,%d)", x, y)
	query = strings.NewReplacer("scope.account_id", "'"+f.account.String()+"'::uuid",
		"scope.selection_id", "'"+s.id.String()+"'::uuid", "scope.context_id", "'"+s.context.String()+"'::uuid",
		"scope.start_date", "DATE '2026-01-01'", "scope.end_date", "DATE '2026-02-01'",
		"clip_bounds", "ST_Expand("+bounds+",(ST_XMax("+bounds+")-ST_XMin("+bounds+"))/64)", "bounds::box2d", bounds+"::box2d", "chunk.geom, z", "chunk.geom, 15").Replace(query)
	var checked, unique int
	if err := tx.QueryRow(db.ctx, query).Scan(&checked, &unique); err != nil || checked < 7 || unique >= checked || unique < 1 {
		t.Fatalf("actual tile inputs not deduplicated: checked=%d unique=%d err=%v", checked, unique, err)
	}
	var total int
	if err := tx.QueryRow(db.ctx, `SELECT count(*) FROM app.coverage_tile_chunks WHERE account_id=$1`, f.account).Scan(&total); err != nil || total < checked*20 {
		t.Fatalf("fixture lost off-tile/local separation: total=%d checked=%d %v", total, checked, err)
	}
	var plan json.RawMessage
	if err := tx.QueryRow(db.ctx, `EXPLAIN (ANALYZE,FORMAT JSON,TIMING OFF,SUMMARY OFF) `+query).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(plan), `"CTE Name": "checked"`) || strings.Contains(string(plan), `"Function Name": "coverage_mvt"`) {
		t.Fatalf("expected actual nested tile query: %s", plan)
	}
	_ = tx.Rollback(db.ctx)
	if !reflect.DeepEqual(before, canonicalDisplayRows(t, db, f, s)) {
		t.Fatal("tile geometry optimization changed canonical counts or metadata")
	}
	if database.SupportedSchemaVersion != 31 {
		t.Fatal("tile test requires the current tile rendering contract")
	}
}
