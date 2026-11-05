package migrations_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestDenseTileNonidenticalScalePostgreSQL(t *testing.T) {
	db := tileTestDatabases(t)
	f := newCanonicalDisplayFixture(t, db)
	const workouts = 1000
	ids := make([]uuid.UUID, workouts)
	for i := range ids {
		ids[i] = uuid.New()
	}
	tx := beginAccount(t, db.ctx, db.migration, f.account)
	coverageDisplayExec(t, db, tx, `UPDATE app.path_segments SET geom=ST_MakeLine(ARRAY(
		SELECT ST_SetSRID(ST_MakePoint(10+i*.000001,20+(i%2)*.00000001),4326)
		FROM generate_series(0,2048) i ORDER BY i)) WHERE account_id=$1 AND physical_segment_id=$2`, f.account, f.segments[0])
	coverageDisplayCommit(t, db, tx)
	for start := 0; start < workouts; start += 50 {
		tx := beginAccount(t, db.ctx, db.migration, f.account)
		canonicalDisplayFence(t, db, tx, f)
		coverageDisplayExec(t, db, tx, `SET LOCAL statement_timeout='4s'`)
		batch := ids[start : start+50]
		coverageDisplayExec(t, db, tx, `INSERT INTO app.workouts(id,account_id,source_id,source_file_id,workout_type_id,
			provider_id,content_sha256,provider_label,local_start_date,started_at,ended_at,provider_duration)
			SELECT ids.id,workout.account_id,source_id,source_file_id,workout_type_id,ids.id::text,content_sha256,provider_label,
			local_start_date,started_at,ended_at,provider_duration FROM unnest($3::uuid[]) ids(id)
			CROSS JOIN app.workouts workout WHERE workout.account_id=$1 AND workout.id=$2`, f.account, f.workouts[0], batch)
		coverageDisplayExec(t, db, tx, `INSERT INTO app.workout_routes(account_id,workout_id,point_count,minimum_longitude,minimum_latitude,
			maximum_longitude,maximum_latitude,has_complete_altitude,route)
			SELECT $1,ids.id,2049,10,20,10.002048,20.000001,false,
			ST_Multi(ST_Translate(segment.geom,0,(ids.n+$4)*.00000000001))
			FROM unnest($3::uuid[]) WITH ORDINALITY ids(id,n) CROSS JOIN app.path_segments segment
			WHERE segment.account_id=$1 AND segment.physical_segment_id=$2`, f.account, f.segments[0], batch, start)
		coverageDisplayExec(t, db, tx, `INSERT INTO app.workout_coverage_states(account_id,workout_id,route_input_revision,route_input_sha256,
			readiness_state,map_data_ready_at,processing_state,applied_route_input_revision,applied_route_input_sha256,
			applied_rules_version,applied_sampling_version,applied_path_policy_version,applied_generations,processing_finished_at)
			SELECT $1,id,route_input_revision,route_input_sha256,readiness_state,map_data_ready_at,processing_state,
			applied_route_input_revision,applied_route_input_sha256,applied_rules_version,applied_sampling_version,
			applied_path_policy_version,applied_generations,processing_finished_at
			FROM unnest($3::uuid[]) id CROSS JOIN app.workout_coverage_states state WHERE state.account_id=$1 AND state.workout_id=$2`,
			f.account, f.workouts[0], batch)
		coverageDisplayExec(t, db, tx, `INSERT INTO app.workout_segment_matches(account_id,workout_id,physical_segment_id,
			region_id,generation_id,logical_path_id,first_traversed_at,first_route_order,covered_meters,geom)
			SELECT $1,ids.id,segment.physical_segment_id,segment.region_id,segment.generation_id,segment.logical_path_id,
			'2026-01-10T08:00:00Z',0,100,ST_Multi(ST_LineSubstring(segment.geom,(ids.n+$4)*.00000001,1-(ids.n+$4)*.00000001))
			FROM unnest($3::uuid[]) WITH ORDINALITY ids(id,n) CROSS JOIN app.path_segments segment
			WHERE segment.account_id=$1 AND segment.physical_segment_id=$2`, f.account, f.segments[0], batch, start)
		coverageDisplayCommit(t, db, tx)
	}
	s := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", ids)
	x, y := tileXY(10.001, 20, 12)
	tx = beginAccount(t, db.ctx, db.migration, f.account)
	defer tx.Rollback(db.ctx)
	coverageDisplayExec(t, db, tx, `SET LOCAL ROLE workouts_security_owner`)
	coverageDisplayExec(t, db, tx, `SET LOCAL search_path=pg_catalog,app,public`)
	coverageDisplayExec(t, db, tx, `SET LOCAL jit=off`)
	coverageDisplayExec(t, db, tx, `SET LOCAL work_mem='32MB'`)
	coverageDisplayExec(t, db, tx, `SET LOCAL statement_timeout='4s'`)
	// As in the 031 scale comparison, give both input producers current scalar
	// statistics after bulk fixture inserts; do not sample the large geometries.
	for _, relation := range []string{
		"workouts(account_id,id,workout_type_id,local_start_date,deletion_requested_at)",
		"workout_routes(account_id,workout_id)", "workout_types(account_id,id)",
		"map_selections(account_id,id)", "map_selection_workouts(account_id,selection_id,workout_id)",
		"workout_coverage_states(account_id,workout_id,applied_route_input_revision)",
		"workout_segment_matches(account_id,workout_id,physical_segment_id,logical_path_id,region_id,generation_id)",
		"path_segments(account_id,physical_segment_id,region_id,generation_id)",
		"raw_route_tile_chunks(account_id,workout_id)", "raw_route_lod_chunks(account_id,workout_id,zoom_band)",
		"coverage_tile_chunks(account_id,workout_id,physical_segment_id)",
	} {
		coverageDisplayExec(t, db, tx, "ANALYZE app."+relation)
	}
	var rawDistinct, matchDistinct, baseVertices, lodVertices int
	if err := tx.QueryRow(db.ctx, `SELECT
		(SELECT count(DISTINCT ST_AsEWKB(route)) FROM app.workout_routes WHERE account_id=$1 AND workout_id=ANY($2::uuid[])),
		(SELECT count(DISTINCT ST_AsEWKB(geom)) FROM app.workout_segment_matches WHERE account_id=$1 AND workout_id=ANY($2::uuid[])),
		(SELECT sum(ST_NPoints(geom)) FROM app.raw_route_tile_chunks WHERE account_id=$1 AND workout_id=ANY($2::uuid[])),
		(SELECT sum(ST_NPoints(geom)) FROM app.raw_route_lod_chunks WHERE account_id=$1 AND workout_id=ANY($2::uuid[]) AND zoom_band=12)`,
		f.account, ids).Scan(&rawDistinct, &matchDistinct, &baseVertices, &lodVertices); err != nil || rawDistinct != workouts || matchDistinct != workouts || baseVertices < 2000000 || lodVertices != workouts*2 {
		t.Fatalf("nonidentical scale inputs raw=%d match=%d base=%d lod=%d: %v", rawDistinct, matchDistinct, baseVertices, lodVertices, err)
	}
	var body string
	if err := tx.QueryRow(db.ctx, `SELECT prosrc FROM pg_proc WHERE oid='app.coverage_mvt(integer,integer,integer,json)'::regprocedure`).Scan(&body); err != nil {
		t.Fatal(err)
	}
	_, query, ok := strings.Cut(body, "    WITH local_chunks AS MATERIALIZED (")
	if !ok {
		t.Fatal("must profile installed nested query")
	}
	query, _, ok = strings.Cut(query, "    ), shapes AS (")
	if !ok {
		t.Fatal("missing source-isolated dedup barrier")
	}
	query = "WITH local_chunks AS MATERIALIZED (" + query + `) SELECT
		(SELECT count(*) FROM local_chunks),(SELECT count(*) FROM eligible),
		(SELECT count(*) FROM evidence),(SELECT count(*) FROM unique_pieces)`
	bounds := fmt.Sprintf("ST_TileEnvelope(12,%d,%d)", x, y)
	query = strings.NewReplacer("scope.account_id", "'"+f.account.String()+"'::uuid", "scope.selection_id", "'"+s.id.String()+"'::uuid",
		"scope.context_id", "'"+s.context.String()+"'::uuid", "scope.start_date", "DATE '2026-01-10'", "scope.end_date", "DATE '2026-01-12'",
		"clip_bounds", "ST_Expand("+bounds+",(ST_XMax("+bounds+")-ST_XMin("+bounds+"))/64)", "bounds", bounds).Replace(query)
	var local, eligible, evidence, unique int
	if err := tx.QueryRow(db.ctx, query).Scan(&local, &eligible, &evidence, &unique); err != nil || local < workouts*8 || eligible != workouts || evidence > 20 || unique > 20 || unique < 1 {
		t.Fatalf("display-precision dedup did not bound actual varied inputs: local=%d eligible=%d evidence=%d unique=%d: %v", local, eligible, evidence, unique, err)
	}
	t.Logf("1000 nonidentical workouts: raw full vertices=%d LOD vertices=%d coverage chunks=%d eligible workouts=%d quantized evidence=%d source-isolated union inputs=%d",
		baseVertices, lodVertices, local, eligible, evidence, unique)
	for _, surface := range []struct {
		function, layer string
		features        int
	}{{"raw_route_mvt", "routes", workouts}, {"coverage_mvt", "coverage", 1}} {
		started := time.Now()
		var tile []byte
		if err := tx.QueryRow(db.ctx, "SELECT app."+surface.function+`(12,$5,$6,json_build_object(
			'target_account_id',$1::text,'target_session_id',$2::text,'target_selection_id',$3::text,'target_generation',$4::bigint::text))`,
			f.account, f.session, s.id, s.generation, x, y).Scan(&tile); err != nil {
			t.Fatal(err)
		}
		layers := decodeCoverageMVT(t, tile)
		if len(layers[surface.layer]) != surface.features {
			t.Fatalf("varied scale identities lost for %s: features=%d", surface.function, len(layers[surface.layer]))
		}
		if surface.layer == "coverage" && layers["coverage"][0].properties["rangeWorkoutCount"] != uint64(workouts+5) {
			t.Fatal("render dedup changed exact canonical counts")
		}
		t.Logf("%s: elapsed=%s bytes=%d features=%d; local synthetic only, 4s safety cap", surface.function, time.Since(started), len(tile), surface.features)
	}
}
