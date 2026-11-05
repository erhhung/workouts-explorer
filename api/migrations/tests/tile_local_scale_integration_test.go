package migrations_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/migrations"
	"github.com/google/uuid"
)

// Compare the actual geometry-input producers, not an opaque function scan or
// wall-clock threshold. Do not execute the old repeated-evidence global union.
func TestTileLocalScaleComparisonPostgreSQL(t *testing.T) {
	db := tileTestDatabases(t)
	f := newCanonicalDisplayFixture(t, db)
	const workouts = 1000
	ids := make([]uuid.UUID, workouts)
	for i := range ids {
		ids[i] = uuid.New()
	}
	for start := 0; start < workouts; start += 50 {
		tx := beginAccount(t, db.ctx, db.migration, f.account)
		canonicalDisplayFence(t, db, tx, f)
		coverageDisplayExec(t, db, tx, `SET LOCAL statement_timeout='3s'`)
		coverageDisplayExec(t, db, tx, `SET LOCAL lock_timeout='250ms'`)
		batch := ids[start : start+50]
		coverageDisplayExec(t, db, tx, `INSERT INTO app.workouts(id,account_id,source_id,source_file_id,workout_type_id,
			provider_id,content_sha256,provider_label,local_start_date,started_at,ended_at,provider_duration)
			SELECT ids.id,workout.account_id,source_id,source_file_id,workout_type_id,ids.id::text,content_sha256,provider_label,
			local_start_date,started_at,ended_at,provider_duration FROM unnest($3::uuid[]) ids(id)
			CROSS JOIN app.workouts workout WHERE workout.account_id=$1 AND workout.id=$2`, f.account, f.workouts[0], batch)
		coverageDisplayExec(t, db, tx, `WITH shape AS MATERIALIZED (
			SELECT ST_Multi(ST_Collect(
				ST_MakeLine(ARRAY(SELECT ST_SetSRID(ST_MakePoint(10+i*.000001,20+(i%2)*.000001),4326)
					FROM generate_series(0,128) i ORDER BY i)),
				ST_MakeLine(ARRAY(SELECT ST_SetSRID(ST_MakePoint(40+i*.00001,20),4326)
					FROM generate_series(0,510) i ORDER BY i)))) geom
		) INSERT INTO app.workout_routes(account_id,workout_id,point_count,minimum_longitude,minimum_latitude,
			maximum_longitude,maximum_latitude,has_complete_altitude,route)
			SELECT $1,id,640,10,20,40.0051,20.000001,false,shape.geom FROM unnest($2::uuid[]) id CROSS JOIN shape`, f.account, batch)
		if start == 0 {
			coverageDisplayExec(t, db, tx, `UPDATE app.path_segments segment
				SET geom=ST_MakeLine(ST_GeometryN(route.route,1),ST_GeometryN(route.route,2)),
				segment_meters=ST_Length(ST_MakeLine(ST_GeometryN(route.route,1),ST_GeometryN(route.route,2))::geography)
				FROM app.workout_routes route WHERE segment.account_id=$1 AND segment.physical_segment_id=$2
				AND route.account_id=$1 AND route.workout_id=$3`, f.account, f.segments[0], batch[0])
		}
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
			SELECT $1,id,segment.physical_segment_id,segment.region_id,segment.generation_id,segment.logical_path_id,
			'2026-01-10T08:00:00Z',0,ST_Length(route.route::geography),route.route FROM unnest($3::uuid[]) id
			JOIN app.workout_routes route ON route.account_id=$1 AND route.workout_id=id
			CROSS JOIN app.path_segments segment WHERE segment.account_id=$1 AND segment.physical_segment_id=$2`,
			f.account, f.segments[0], batch)
		coverageDisplayCommit(t, db, tx)
	}
	s := canonicalDisplaySelection(t, db, f, "2026-01-10", "2026-01-12", ids)
	tx := beginAccount(t, db.ctx, db.migration, f.account)
	defer tx.Rollback(db.ctx)
	coverageDisplayExec(t, db, tx, `SET LOCAL ROLE workouts_security_owner`)
	coverageDisplayExec(t, db, tx, `SET LOCAL search_path=pg_catalog,app,public`)
	coverageDisplayExec(t, db, tx, `SET LOCAL jit=off`)
	coverageDisplayExec(t, db, tx, `SET LOCAL statement_timeout='3s'`)
	coverageDisplayExec(t, db, tx, `SET LOCAL lock_timeout='250ms'`)
	// Give BOTH versions the same current scalar statistics. Otherwise the old
	// account-wide join can exhaust the safety budget before its input is counted.
	// Avoid geometry sampling and retain normal planner settings.
	for _, relation := range []string{
		"workouts(account_id,id,workout_type_id,local_start_date,deletion_requested_at)",
		"workout_routes(account_id,workout_id)", "workout_types(account_id,id)",
		"map_selections(account_id,id)", "map_selection_workouts(account_id,selection_id,workout_id)",
		"workout_coverage_states(account_id,workout_id,applied_route_input_revision)",
		"workout_segment_matches(account_id,workout_id,physical_segment_id,logical_path_id,region_id,generation_id)",
		"path_segments(account_id,physical_segment_id,region_id,generation_id)",
		"raw_route_tile_chunks(account_id,workout_id)", "coverage_tile_chunks(account_id,workout_id,physical_segment_id)",
	} {
		coverageDisplayExec(t, db, tx, "ANALYZE app."+relation)
	}
	x, y := tileXY(10.00005, 20.0000005, 12)
	bounds := fmt.Sprintf("ST_TileEnvelope(12,%d,%d)", x, y)
	bind := func(query string) string {
		return strings.NewReplacer("target_account_id", "'"+f.account.String()+"'::uuid",
			"target_selection_id", "'"+s.id.String()+"'::uuid", "display_context_id", "'"+s.context.String()+"'::uuid",
			"clip_bounds", "ST_Expand("+bounds+",(ST_XMax("+bounds+")-ST_XMin("+bounds+"))/64)",
			"bounds", bounds, "chunk.geom, z", "chunk.geom, 12").Replace(query)
	}
	body := func(file, name string) string {
		raw, err := migrations.Files.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		return coverageDisplayFunction(t, string(raw), name)
	}
	cut := func(source, begin, end string) string {
		_, rest, ok := strings.Cut(source, begin)
		if !ok {
			t.Fatal("missing actual query producer: " + begin)
		}
		query, _, ok := strings.Cut(rest, end)
		if !ok {
			t.Fatal("missing actual query barrier: " + end)
		}
		return query
	}
	oldRaw := cut(body("014_martin_tile_contract.sql", "raw_route_mvt"), "      FROM (", "        UNION ALL")
	oldRaw = strings.Replace(oldRaw, "ST_AsMVTGeom(ST_Transform(route.route, 3857), bounds, 4096, 64, true)", "route.route", 1)
	oldRaw = "WITH candidates AS (" + oldRaw + ") SELECT count(*),sum(ST_NPoints(geometry)),count(*) FROM candidates"
	oldCoverage := "WITH selected AS MATERIALIZED (" + cut(body("030_coverage_display.sql", "coverage_mvt"),
		"WITH selected AS MATERIALIZED (", "    ), shapes AS MATERIALIZED (") + `)
		SELECT (SELECT count(*) FROM checked),(SELECT sum(ST_NPoints(geom)) FROM checked),(SELECT count(*) FROM pieces)`
	newRaw := "WITH local_chunks AS MATERIALIZED (" + cut(body("031_tile_rendering.sql", "raw_route_mvt"),
		"WITH local_chunks AS MATERIALIZED (", "    ), shapes AS (") + `)
		SELECT count(*),sum(ST_NPoints(geom)),count(*) FROM local`
	newCoverage := "WITH local_chunks AS MATERIALIZED (" + cut(body("031_tile_rendering.sql", "coverage_mvt"),
		"WITH local_chunks AS MATERIALIZED (", "    ), shapes AS (") + `)
		SELECT (SELECT count(*) FROM checked),(SELECT sum(ST_NPoints(geom)) FROM checked),(SELECT count(*) FROM unique_pieces)`
	for _, test := range []struct {
		name, query            string
		rows, vertices, pieces int64
	}{{"014 raw", oldRaw, 1000, 640000, 1000}, {"031 raw", newRaw, 1000, 2000, 1000},
		{"030 coverage", oldCoverage, 1000, 640000, 1000}, {"031 coverage", newCoverage, 1000, 2000, 1}} {
		query := bind(test.query)
		var rows, vertices, pieces int64
		probe, err := tx.Begin(db.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := probe.QueryRow(db.ctx, query).Scan(&rows, &vertices, &pieces); err != nil {
			_ = probe.Rollback(db.ctx)
			var plan []byte
			planErr := tx.QueryRow(db.ctx, `EXPLAIN (FORMAT JSON) `+query).Scan(&plan)
			t.Fatalf("%s actual input query: %v; plan error=%v plan=%s", test.name, err, planErr, plan)
		}
		if err := probe.Commit(db.ctx); err != nil {
			t.Fatal(err)
		}
		if rows != test.rows || vertices != test.vertices || pieces != test.pieces {
			t.Fatalf("%s inputs=%d/%d/%d want=%d/%d/%d", test.name, rows, vertices, pieces, test.rows, test.vertices, test.pieces)
		}
		var plan []byte
		if err := tx.QueryRow(db.ctx, `EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON,TIMING OFF) `+query).Scan(&plan); err != nil {
			t.Fatal(err)
		}
		var document []struct {
			ExecutionMS float64 `json:"Execution Time"`
		}
		if err := json.Unmarshal(plan, &document); err != nil || len(document) != 1 {
			t.Fatal("invalid nested plan", err)
		}
		t.Logf("%s: checked=%d produced_vertices=%d dissolve_inputs=%d producer_explain_ms=%.3f (local synthetic; not tile latency)",
			test.name, rows, vertices, pieces, document[0].ExecutionMS)
	}
	var allChunks, localVertices int64
	if err := tx.QueryRow(db.ctx, `SELECT count(*),sum(ST_NPoints(chunk.geom)) FILTER(WHERE chunk.geom && `+bounds+`)
		FROM app.raw_route_tile_chunks chunk JOIN app.map_selection_workouts selected USING(account_id,workout_id)
		WHERE selected.account_id=$1 AND selected.selection_id=$2`, f.account, s.id).Scan(&allChunks, &localVertices); err != nil || allChunks != 3000 || localVertices != 129000 {
		t.Fatalf("spatial input bound chunks=%d local_vertices=%d err=%v", allChunks, localVertices, err)
	}
	t.Logf("1000 raw workouts retained: stored_chunks=%d local_source_vertices=%d; coverage dedup removes 999 repeated local pieces before union", allChunks, localVertices)
	for _, surface := range []struct{ function, layer string }{{"raw_route_mvt", "routes"}, {"coverage_mvt", "coverage"}} {
		var tile []byte
		if err := tx.QueryRow(db.ctx, `SELECT app.`+surface.function+`(12,$5,$6,json_build_object(
			'target_account_id',$1::text,'target_session_id',$2::text,'target_selection_id',$3::text,'target_generation',$4::bigint::text))`,
			f.account, f.session, s.id, s.generation, x, y).Scan(&tile); err != nil {
			t.Fatalf("bounded full %s tile: %v", surface.function, err)
		}
		layers := decodeCoverageMVT(t, tile)
		if surface.layer == "routes" {
			if len(layers["routes"]) != workouts {
				t.Fatalf("scale tile lost separate workouts: %d", len(layers["routes"]))
			}
			seen := make(map[string]bool)
			orders := make(map[string]uint64, len(ids))
			for order, id := range ids {
				orders[strings.ToUpper(strings.ReplaceAll(id.String(), "-", ""))] = uint64(order)
			}
			for _, feature := range layers["routes"] {
				id := feature.properties["workoutId"].(string)
				order, known := orders[id]
				if seen[id] || !known || feature.properties["sortOrder"] != order ||
					feature.properties["workoutTypeKey"] != "outdoor-run" || feature.properties["workoutType"] != "Synthetic Run" {
					t.Fatalf("scale tile lost workout attribution/type/order: %v", feature.properties)
				}
				seen[id] = true
			}
		} else if len(layers["coverage"]) != 1 || len(layers["coverage_focus"]) != 0 || len(layers["coverage_parks_focus"]) != 0 ||
			layers["coverage"][0].properties["rangeWorkoutCount"] != uint64(workouts+5) ||
			layers["coverage"][0].properties["entityId"] != strings.ToUpper(strings.ReplaceAll(canonicalDisplayID("display:synthetic", "synthetic walk", "synthetic city", "").String(), "-", "")) ||
			layers["coverage"][0].properties["sourceEntityKind"] != "path" ||
			layers["coverage"][0].properties["sourceEntityId"] != strings.ToUpper(strings.ReplaceAll(f.paths[0].String(), "-", "")) {
			t.Fatalf("scale coverage tile lost canonical counts/source provenance or embedded focus: %v", layers)
		}
		t.Logf("full %s tile: bytes=%d features=%d (3s safety cap, no latency acceptance threshold)", surface.function, len(tile), len(layers[surface.layer]))
	}
}
