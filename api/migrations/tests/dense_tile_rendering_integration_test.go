package migrations_test

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func TestDenseTileLODGeometryPostgreSQL(t *testing.T) {
	db := tileTestDatabases(t)
	coverageDisplayCheck(t, db, `WITH source AS (
		SELECT ST_Collect(ARRAY[ST_MakeLine(ARRAY(SELECT ST_SetSRID(ST_MakePoint(10+i*.000001,
		20+(i%2)*.0000001),4326) FROM generate_series(0,20000) i ORDER BY i)),
		ST_GeomFromText('LINESTRING(10.03 20,10.04 20)',4326),
		ST_GeomFromText('LINESTRING(10.05 20,10.05 20)',4326)]) geom
	), chunks AS (SELECT c.* FROM source CROSS JOIN LATERAL app.project_route_lod(source.geom) c),
	assembled AS (SELECT zoom_band,component,ST_Collect(geom ORDER BY piece) geom FROM chunks GROUP BY zoom_band,component)
	SELECT (SELECT count(*)=9 AND max(ST_NPoints(geom))=2 AND count(*) FILTER(WHERE GeometryType(geom)='POINT')=3 FROM chunks)
		AND bool_and(ST_HausdorffDistance(a.geom,ST_Transform(ST_GeometryN(s.geom,a.component),3857))
		<=40075016.68557849/power(2.0,a.zoom_band)/2048)
		AND NOT EXISTS(SELECT 1 FROM chunks WHERE ST_Intersects(geom,
		ST_Transform(ST_SetSRID(ST_MakePoint(10.025,20),4326),3857))) FROM assembled a,source s`)
	f := newCanonicalDisplayFixture(t, db)
	tx := beginAccount(t, db.ctx, db.migration, f.account)
	defer tx.Rollback(db.ctx)
	canonicalDisplayFence(t, db, tx, f)
	coverageDisplayExec(t, db, tx, `UPDATE app.workout_routes SET route=ST_GeomFromText(
		'MULTILINESTRING((10 20,10.001 20.001),(10.002 20,10.003 20.001),(10.0015 20.0015,10.0015 20.0015),(10.0001 20,10.000100001 20))',4326)
		WHERE account_id=$1 AND workout_id=ANY($2::uuid[])`, f.account, f.workouts[:2])
	snapshot := func(tx pgx.Tx) string {
		var value string
		if err := tx.QueryRow(db.ctx, `SELECT md5(string_agg(workout_id::text||zoom_band||':'||component||':'||piece||':'||encode(ST_AsEWKB(geom),'hex'),
			',' ORDER BY workout_id,zoom_band,component,piece)) FROM app.raw_route_lod_chunks WHERE account_id=$1`, f.account).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := snapshot(tx)
	sp, err := tx.Begin(db.ctx)
	if err != nil {
		t.Fatal(err)
	}
	coverageDisplayExec(t, db, sp, `UPDATE app.workout_routes SET route=NULL WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0])
	if err := sp.Rollback(db.ctx); err != nil || snapshot(tx) != before {
		t.Fatal("LOD maintenance leaked across rollback", err)
	}
	coverageDisplayExec(t, db, tx, `UPDATE app.workout_routes SET point_count=point_count WHERE account_id=$1`, f.account)
	if snapshot(tx) != before {
		t.Fatal("metadata-only update rewrote LOD")
	}
	coverageDisplayCommit(t, db, tx)
	s := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", f.workouts[:2])
	for _, z := range []int{10, 11, 12, 13, 14, 15} {
		x, y := tileXY(10.001, 20.001, z)
		var tile []byte
		if err := db.migration.QueryRow(db.ctx, `SELECT app.raw_route_mvt($5,$6,$7,json_build_object(
			'target_account_id',$1::text,'target_session_id',$2::text,'target_selection_id',$3::text,'target_generation',$4::bigint::text))`,
			f.account, f.session, s.id, s.generation, z, x, y).Scan(&tile); err != nil {
			t.Fatal(err)
		}
		features := decodeCoverageMVT(t, tile)["routes"]
		if len(features) != 4 {
			t.Fatalf("z%d must retain two independent multi-component workouts and two stationary points, not turn collapsed movement into dots: %v", z, features)
		}
		seen := map[string]int{}
		for _, feature := range features {
			seen[feature.properties["workoutId"].(string)]++
		}
		for _, id := range f.workouts[:2] {
			if seen[strings.ToUpper(strings.ReplaceAll(id.String(), "-", ""))] != 2 {
				t.Fatal("workout identity collapsed")
			}
		}
	}
	tx = beginAccount(t, db.ctx, db.migration, f.account)
	defer tx.Rollback(db.ctx)
	canonicalDisplayFence(t, db, tx, f)
	coverageDisplayExec(t, db, tx, `DELETE FROM app.workout_routes WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0])
	var gone bool
	if err := tx.QueryRow(db.ctx, `SELECT NOT EXISTS(SELECT 1 FROM app.raw_route_lod_chunks WHERE account_id=$1 AND workout_id=$2)`, f.account, f.workouts[0]).Scan(&gone); err != nil || !gone {
		t.Fatal("LOD deletion did not cascade", err)
	}
}

func TestDenseTileClosedMovingLoopPostgreSQL(t *testing.T) {
	db := tileTestDatabases(t)
	coverageDisplayCheck(t, db, `WITH source AS (
		SELECT ST_Transform(ST_GeomFromText('LINESTRING(100 100,110 100,110 110,100 110,100 100)',3857),4326) geom
	), projected AS (SELECT ST_Transform(geom,3857) geom FROM source), chunks AS (
		SELECT chunk.* FROM source CROSS JOIN LATERAL app.project_route_lod(source.geom) chunk
	) SELECT (SELECT ST_Length(geom)>39.999 AND ST_Length(ST_Simplify(geom,40075016.68557849/power(2.0,10)/2048,true))=0 FROM projected)
		AND count(*)=3 AND count(DISTINCT zoom_band)=3
		AND bool_and(GeometryType(chunks.geom)='LINESTRING' AND ST_Length(chunks.geom)>0
			AND ST_Equals(chunks.geom,projected.geom)
			AND ST_AsMVTGeom(chunks.geom,ST_TileEnvelope(zoom_band,(1<<zoom_band)/2,(1<<zoom_band)/2-1)::box2d,4096,64,true) IS NOT NULL)
		FROM chunks CROSS JOIN projected`)
	f := newCanonicalDisplayFixture(t, db)
	tx := beginAccount(t, db.ctx, db.migration, f.account)
	canonicalDisplayFence(t, db, tx, f)
	coverageDisplayExec(t, db, tx, `UPDATE app.workout_routes SET route=ST_Multi(ST_Transform(
		ST_GeomFromText('LINESTRING(100 100,110 100,110 110,100 110,100 100)',3857),4326)),point_count=5
		WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0])
	coverageDisplayCommit(t, db, tx)
	s := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", f.workouts[:1])
	for _, z := range []int{10, 12, 14} {
		var tile, expected []byte
		if err := db.migration.QueryRow(db.ctx, `SELECT app.raw_route_mvt($5,(1<<$5)/2,(1<<$5)/2-1,json_build_object(
			'target_account_id',$1::text,'target_session_id',$2::text,'target_selection_id',$3::text,'target_generation',$4::bigint::text)),
			(SELECT ST_AsMVT(rows,'routes',4096,'geometry') FROM (SELECT ST_AsMVTGeom(ST_Transform(route,3857),
			ST_TileEnvelope($5,(1<<$5)/2,(1<<$5)/2-1)::box2d,4096,64,true) geometry
			FROM app.workout_routes WHERE account_id=$1::uuid AND workout_id=$6::uuid) rows)`,
			f.account, f.session, s.id, s.generation, z, f.workouts[0]).Scan(&tile, &expected); err != nil {
			t.Fatal(err)
		}
		features, full := decodeCoverageMVT(t, tile)["routes"], decodeCoverageMVT(t, expected)["routes"]
		if len(features) != 1 || len(full) != 1 || features[0].geometry != full[0].geometry ||
			features[0].properties["workoutId"] != strings.ToUpper(strings.ReplaceAll(f.workouts[0].String(), "-", "")) {
			t.Fatalf("z%d closed movement lost identity/full geometry or became a fabricated dot: actual=%v full=%v", z, features, full)
		}
	}
}

func TestDenseTileAuthorizationPostgreSQL(t *testing.T) {
	db := tileTestDatabases(t)
	f := newCanonicalDisplayFixture(t, db)
	s := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", f.workouts[:1])
	scope := map[string]any{"target_account_id": f.account.String(), "target_session_id": f.session.String(),
		"target_selection_id": s.id.String(), "target_generation": s.generation}
	params, _ := json.Marshal(scope)
	for _, name := range []string{"raw_route_mvt", "coverage_mvt"} {
		for _, invalid := range []struct{ params, coordinates, code string }{
			{"{}", "12,2161,1815", "22023"}, {"null", "12,2161,1815", "22023"}, {"[]", "12,2161,1815", "22023"},
			{strings.TrimSuffix(string(params), "}") + `,"extra":true}`, "12,2161,1815", "22023"},
			{strings.Replace(string(params), f.account.String(), "bad", 1), "12,2161,1815", "22023"},
			{strings.Replace(string(params), f.account.String(), uuid.NewString(), 1), "12,2161,1815", "42501"},
			{strings.Replace(string(params), f.session.String(), uuid.NewString(), 1), "12,2161,1815", "42501"},
			{strings.Replace(string(params), s.id.String(), uuid.NewString(), 1), "12,2161,1815", "42501"},
			{strings.Replace(string(params), `"target_generation":`+fmt.Sprint(s.generation), `"target_generation":`+fmt.Sprint(s.generation+1), 1), "12,2161,1815", "42501"},
			{strings.Replace(string(params), `"target_generation":`+fmt.Sprint(s.generation), `"target_generation":null`, 1), "12,2161,1815", "22023"},
			{string(params), "NULL,0,0", "22023"}, {string(params), "12,NULL,0", "22023"}, {string(params), "12,0,NULL", "22023"},
			{string(params), "-1,0,0", "22023"}, {string(params), "23,0,0", "22023"}, {string(params), "12,4096,0", "22023"},
		} {
			tx := beginAccount(t, db.ctx, db.migration, f.account)
			_, err := tx.Exec(db.ctx, "SELECT app."+name+"("+invalid.coordinates+",$1::json)", invalid.params)
			bootstrapSQLState(t, err, invalid.code)
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
			coverageDisplayExec(t, db, tx, `SET LOCAL session_replication_role='replica'`)
			coverageDisplayExec(t, db, tx, `WITH scope AS (SELECT $1::uuid account,$2::uuid session,$3::uuid selection) `+mutation, f.account, f.session, s.id)
			coverageDisplayExec(t, db, tx, `SET LOCAL session_replication_role='origin'`)
			_, err := tx.Exec(db.ctx, "SELECT app."+name+"(12,2161,1815,$1::json)", string(params))
			bootstrapSQLState(t, err, "42501")
			_ = tx.Rollback(db.ctx)
		}
	}
	for _, mutation := range []string{
		`UPDATE app.coverage_display_contexts SET ready=false WHERE account_id=$1`,
		`UPDATE app.coverage_display_revisions SET revision=revision+1 WHERE account_id=$1`,
		`UPDATE app.coverage_display_contexts SET start_date=start_date-1 WHERE account_id=$1`,
		`UPDATE app.coverage_display_contexts SET generation=generation+1 WHERE account_id=$1`,
	} {
		tx := beginAccount(t, db.ctx, db.migration, f.account)
		coverageDisplayExec(t, db, tx, mutation, f.account)
		coverageDisplayExec(t, db, tx, `SELECT app.raw_route_mvt(12,2161,1815,$1::json)`, string(params))
		_, err := tx.Exec(db.ctx, `SELECT app.coverage_mvt(12,2161,1815,$1::json)`, string(params))
		bootstrapSQLState(t, err, "42501")
		_ = tx.Rollback(db.ctx)
	}
	// Exercise the actual Martin login role with no account/session GUCs.
	cfg, err := pgx.ParseConfig(os.Getenv("TILE_DATABASE_URL"))
	if err != nil || cfg.User != "workouts_tiles" {
		t.Fatal("explicit synthetic Martin login URL required")
	}
	migrationConfig, _ := pgx.ParseConfig(os.Getenv("MIGRATION_DATABASE_URL"))
	if cfg.Host != migrationConfig.Host || cfg.Port != migrationConfig.Port || cfg.Database != migrationConfig.Database {
		t.Fatal("Martin fixture must use the verified synthetic endpoint")
	}
	tiles := openPool(t, db.ctx, os.Getenv("TILE_DATABASE_URL"), 1)
	var unbound bool
	if err := tiles.QueryRow(db.ctx, `SELECT current_user='workouts_tiles'
		AND COALESCE(current_setting('app.account_id',true),'')=''
		AND COALESCE(current_setting('app.session_id',true),'')=''`).Scan(&unbound); err != nil || !unbound {
		t.Fatal("Martin fixture must not inherit account/session GUCs", err)
	}
	for _, name := range []string{"raw_route_mvt", "coverage_mvt"} {
		var tile []byte
		x, y := tileXY(10.0005, 20.0005, 12)
		if err := tiles.QueryRow(db.ctx, "SELECT app."+name+"(12,$1,$2,$3::json)", x, y, string(params)).Scan(&tile); err != nil || len(tile) == 0 {
			t.Fatalf("Martin explicit scope failed for %s: %v", name, err)
		}
	}
}

func TestDenseTileCoverageGapsAndIdentityPostgreSQL(t *testing.T) {
	db := tileTestDatabases(t)
	f := newCanonicalDisplayFixture(t, db)
	tx := beginAccount(t, db.ctx, db.migration, f.account)
	coverageDisplayExec(t, db, tx, `UPDATE app.workout_segment_matches match SET geom=ST_Multi(ST_Collect(
		ST_LineSubstring(segment.geom,0,.2),ST_Reverse(ST_LineSubstring(segment.geom,.8,1))))
		FROM app.path_segments segment WHERE match.account_id=$1 AND match.workout_id=$2 AND match.physical_segment_id=$3
		AND segment.account_id=match.account_id AND segment.region_id=match.region_id
		AND segment.generation_id=match.generation_id AND segment.physical_segment_id=match.physical_segment_id`, f.account, f.workouts[0], f.segments[0])
	coverageDisplayCommit(t, db, tx)
	canonicalDisplayPark(t, db, f)
	s := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", f.workouts[:1])
	before := canonicalDisplayRows(t, db, f, s)
	focus := func() string {
		api := canonicalDisplayAPI(t, db, f)
		defer api.Rollback(db.ctx)
		var value string
		if err := api.QueryRow(db.ctx, `SELECT app.map_selection_coverage_focus($1,$2,$3,$4,$5)::text`,
			f.account, f.session, s.id, s.generation, f.workouts[0]).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	exactFocus := focus()
	x, y := tileXY(10.0005, 20.0005, 15)
	var tile []byte
	if err := db.migration.QueryRow(db.ctx, `SELECT app.coverage_mvt(15,$5,$6,json_build_object(
        'target_account_id',$1::text,'target_session_id',$2::text,'target_selection_id',$3::text,'target_generation',$4::bigint::text))`,
		f.account, f.session, s.id, s.generation, x, y).Scan(&tile); err != nil {
		t.Fatal(err)
	}
	current := decodeCoverageMVT(t, tile)
	if len(current["coverage"]) == 0 || len(current["coverage_parks"]) == 0 {
		t.Fatal("missing independently attributed layers")
	}
	for _, row := range before {
		canonicalDisplaySurfaces(t, db, f, s, row)
	}
	if !reflect.DeepEqual(before, canonicalDisplayRows(t, db, f, s)) {
		t.Fatal("rendering changed authoritative counts")
	}
	if focus() != exactFocus {
		t.Fatal("rendering changed independent exact focus geometry")
	}
}

func TestDenseTileSubpixelChunksDoNotEraseVisibleComponentPostgreSQL(t *testing.T) {
	db := tileTestDatabases(t)
	f := newCanonicalDisplayFixture(t, db)
	tx := beginAccount(t, db.ctx, db.migration, f.account)
	canonicalDisplayFence(t, db, tx, f)
	coverageDisplayExec(t, db, tx, `WITH origin AS (
		SELECT ST_Transform(ST_SetSRID(ST_MakePoint(.0005,.0005),4326),3857) geom
	), line AS (SELECT ST_Multi(ST_Transform(ST_MakeLine(ARRAY(SELECT ST_Translate(origin.geom,i/2048.0,0)
		FROM generate_series(0,2048) i ORDER BY i)),4326)) geom FROM origin)
	UPDATE app.workout_routes SET route=line.geom,point_count=2049,
		minimum_longitude=ST_XMin(line.geom),minimum_latitude=ST_YMin(line.geom),
		maximum_longitude=ST_XMax(line.geom),maximum_latitude=ST_YMax(line.geom)
		FROM line WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0])
	coverageDisplayExec(t, db, tx, `UPDATE app.path_segments segment SET geom=ST_GeometryN(route.route,1),segment_meters=1
		FROM app.workout_routes route WHERE segment.account_id=$1 AND segment.physical_segment_id=$3
		AND route.account_id=segment.account_id AND route.workout_id=$2`, f.account, f.workouts[0], f.segments[0])
	coverageDisplayExec(t, db, tx, `UPDATE app.workout_segment_matches match SET geom=route.route FROM app.workout_routes route
		WHERE match.account_id=$1 AND match.workout_id=$2 AND match.physical_segment_id=$3
		AND route.account_id=match.account_id AND route.workout_id=match.workout_id`, f.account, f.workouts[0], f.segments[0])
	coverageDisplayCommit(t, db, tx)
	s := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", f.workouts[:1])
	x, y := tileXY(.0005, .0005, 15)
	coverageDisplayCheck(t, db, `SELECT count(*)>1 AND bool_and(ST_AsMVTGeom(geom,ST_TileEnvelope(15,$3,$4)::box2d,4096,64,true) IS NULL)
		FROM app.raw_route_tile_chunks WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0], x, y)
	for _, surface := range []struct{ function, layer, property, id string }{
		{"raw_route_mvt", "routes", "workoutId", f.workouts[0].String()},
		{"coverage_mvt", "coverage", "sourceEntityId", f.paths[0].String()},
	} {
		var tile []byte
		if err := db.migration.QueryRow(db.ctx, "SELECT app."+surface.function+`(15,$5,$6,json_build_object(
			'target_account_id',$1::text,'target_session_id',$2::text,'target_selection_id',$3::text,'target_generation',$4::bigint::text))`,
			f.account, f.session, s.id, s.generation, x, y).Scan(&tile); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, feature := range decodeCoverageMVT(t, tile)[surface.layer] {
			if feature.properties[surface.property] == strings.ToUpper(strings.ReplaceAll(surface.id, "-", "")) {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s erased a visible moving component made of subpixel storage pieces", surface.function)
		}
	}
}
