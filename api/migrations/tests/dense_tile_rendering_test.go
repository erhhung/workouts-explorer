package migrations_test

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/migrations"
)

func denseTileSQL(t *testing.T) string {
	t.Helper()
	raw, err := migrations.Files.ReadFile("031_tile_rendering.sql")
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(string(raw), "-- +goose Down")[0]
}

func TestDenseTileCurrentContract(t *testing.T) {
	source := denseTileSQL(t)
	canonicalRequire(t, source, "schema_version = 31, minimum_runtime_version = 31", "LOCK TABLE app.workout_routes, app.workout_segment_matches",
		"CREATE TABLE app.raw_route_lod_chunks", "ENABLE ROW LEVEL SECURITY", "FORCE ROW LEVEL SECURITY",
		"REFERENCES app.workout_routes(account_id, workout_id) ON UPDATE CASCADE ON DELETE CASCADE",
		"FROM app.workout_routes route CROSS JOIN LATERAL app.project_route_lod(route.route)", "SET LOCAL ROLE workouts_security_owner")
	canonicalForbid(t, source, "UPDATE app.workout_routes", "UPDATE app.workout_segment_matches",
		"workout_route_points", "migration_baseline =", "DISABLE TRIGGER")
	lod := coverageDisplayFunction(t, source, "project_route_lod")
	canonicalRequire(t, lod, "ST_Dump(input)", "ST_Transform(d.geom, 3857)", "ST_Simplify(c.geom", "(VALUES (10),(12),(14))",
		"ST_StartPoint(c.geom)", "original_length > 0 AND ST_Length(geom) = 0 THEN original_geom",
		"generate_series(1, ST_NPoints(c.geom) - 1, 255)")
	if strings.Index(lod, "ST_Simplify(c.geom") > strings.Index(lod, "generate_series(1,") {
		t.Fatal("must simplify whole components before chunking")
	}
	for _, name := range []string{"raw_route_mvt", "coverage_mvt"} {
		body := coverageDisplayFunction(t, source, name)
		canonicalRequire(t, body, "z IS NULL OR x IS NULL OR y IS NULL", "z NOT BETWEEN 0 AND 22",
			"app.tile_scope(query_params,", "chunk.geom && clip_bounds", "bounds::box2d, 4096, 64, true")
		canonicalForbid(t, body, "ST_Intersection", "ST_Transform", "app.current_account_id", "app.current_session_id", "focused_workout_id")
	}
	raw := coverageDisplayFunction(t, source, "raw_route_mvt")
	canonicalRequire(t, raw, "ST_AsMVTGeom(geom, bounds::box2d, 4096, 64, true) geometry FROM shapes")
	canonicalForbid(t, raw, "ST_AsMVTGeom(chunk.geom")
	coverage := coverageDisplayFunction(t, source, "coverage_mvt")
	canonicalRequire(t, coverage, "eligible AS MATERIALIZED", "state.applied_route_input_revision IS NOT NULL",
		"FROM (SELECT DISTINCT workout_id FROM local_chunks) local_workout", "selected.workout_id = local_workout.workout_id", "OFFSET 0",
		"SELECT DISTINCT canonical_id, source_kind, source_id, ewkb", "ST_UnaryUnion(ST_Collect(ST_GeomFromEWKB(ewkb)))",
		"entity.range_workout_count", "member.source_id", "'coverage_parks'")
	canonicalForbid(t, coverage, "segment.geom", "match.geom", "ST_LineSubstring", "ST_LineLocatePoint")
	// The approved consolidated source is the current migration baseline.
	hash := sha256.New()
	for _, file := range migrationFiles[:31] {
		raw, err := migrations.Files.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(hash, file)
		hash.Write(raw)
	}
	const expected = "fa9a930ea85f560b3204e67576896e05aff14621215723ff897e38a46d1cecd1"
	if got := fmt.Sprintf("%x", hash.Sum(nil)); got != expected {
		t.Fatalf("001-031 must remain byte-identical: %s", got)
	}
}
