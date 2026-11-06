package migrations_test

import (
	"crypto/sha256"
	"fmt"
	"math"
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/migrations"
)

func tileLocalSQL(t *testing.T) string {
	t.Helper()
	raw, err := migrations.Files.ReadFile("031_tile_rendering.sql")
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(string(raw), "-- +goose Down")[0]
}

func TestTileLocalMigrationContract(t *testing.T) {
	source := tileLocalSQL(t)
	canonicalRequire(t, source, "schema_version = 31, minimum_runtime_version = 31",
		"CREATE INDEX map_selection_workouts_tile_lookup_idx ON app.map_selection_workouts(account_id, selection_id, workout_id) INCLUDE(sort_order)",
		"CREATE INDEX workouts_tile_lookup_idx ON app.workouts(account_id, id) INCLUDE(workout_type_id, local_start_date, deletion_requested_at)",
		"LOCK TABLE app.workout_routes, app.workout_segment_matches IN SHARE ROW EXCLUSIVE MODE",
		"SET LOCAL ROLE workouts_security_owner", "RESET ROLE",
		"FROM app.workout_routes route CROSS JOIN LATERAL app.project_tile_chunks(route.route)",
		"FROM app.workout_segment_matches match CROSS JOIN LATERAL app.project_tile_chunks(match.geom)")
	canonicalForbid(t, source, "NO FORCE ROW LEVEL SECURITY", "DISABLE TRIGGER", "UPDATE app.workouts",
		"UPDATE app.workout_segment_matches", "UPDATE app.path_segments", "migration_baseline =", "statement_timeout")
	for _, table := range []string{"raw_route_tile_chunks", "coverage_tile_chunks"} {
		canonicalRequire(t, source, "ALTER TABLE app."+table+" ENABLE ROW LEVEL SECURITY",
			"ALTER TABLE app."+table+" FORCE ROW LEVEL SECURITY", "CREATE POLICY "+table+"_owner_policy",
			"ALTER TABLE app."+table+" OWNER TO workouts_security_owner",
			"CREATE INDEX "+table+"_geom_idx ON app."+table+" USING gist(geom)")
	}
	for _, name := range []string{"maintain_raw_route_tile_chunks", "maintain_coverage_tile_chunks"} {
		body := coverageDisplayFunction(t, source, name)
		canonicalRequire(t, body, "TG_OP = 'UPDATE'", "ST_AsEWKB(", "IS NOT DISTINCT FROM",
			"TG_OP <> 'INSERT'", "TG_OP = 'DELETE'", "app.project_tile_chunks(NEW.")
	}
}

func TestTileChunkAndLODContract(t *testing.T) {
	source := tileLocalSQL(t)
	chunks := coverageDisplayFunction(t, source, "project_tile_chunks")
	canonicalRequire(t, chunks, "ST_Dump(input)", "ST_Transform(d.geom, 3857)", "ST_StartPoint(c.geom)",
		"generate_series(1, ST_NPoints(c.geom) - 1, 255)", "least(start.vertex + 255, ST_NPoints(c.geom))")
	lod := coverageDisplayFunction(t, source, "tile_display_geometry")
	canonicalRequire(t, lod, "z >= 15", "GeometryType(input) = 'POINT'", "ST_SimplifyPreserveTopology",
		"40075016.68557849 / power(2.0, z) / 2048.0")
	canonicalForbid(t, lod, "ST_Snap", "ST_LineMerge", "ST_MakeLine")
	// A 512-CSS-pixel tile and 4096-unit MVT grid leave rounding headroom.
	if tolerance := 512.0/2048 + math.Sqrt2*512.0/4096/2; tolerance >= .5 {
		t.Fatalf("display error bound=%g pixels", tolerance)
	}
}

func TestTileLocalReadersAndIndependentFocusContract(t *testing.T) {
	source := tileLocalSQL(t)
	for _, name := range []string{"raw_route_mvt", "coverage_mvt"} {
		body := coverageDisplayFunction(t, source, name)
		canonicalRequire(t, body, "chunk.geom && clip_bounds", "app.tile_scope(query_params,",
			"* 64.0 / 4096.0", "bounds::box2d, 4096, 64, true")
		canonicalForbid(t, body, "ST_Transform(", "app.authorize_map_tile(", "app.current_account_id()",
			"app.current_session_id()", "focused_workout_id", "coverage_focus", "coverage_parks_focus")
	}
	raw := coverageDisplayFunction(t, source, "raw_route_mvt")
	canonicalRequire(t, raw, "GROUP BY workout_id, sort_order, type_key, provider_label", "WHERE stationary",
		"ORDER BY component, piece", "\"workoutId\"", "\"sortOrder\"")
	canonicalForbid(t, raw, "SELECT DISTINCT", "ST_UnaryUnion", "ST_Union")
	coverage := coverageDisplayFunction(t, source, "coverage_mvt")
	canonicalRequire(t, coverage, "SELECT DISTINCT canonical_id, source_kind, source_id, ewkb",
		"CASE WHEN count(*) = 1", "ST_UnaryUnion(ST_Collect(ST_GeomFromEWKB(ewkb)))",
		"entity.range_workout_count", "\"sourceEntityId\"", "\"sourceEntityKind\"",
		"state.applied_route_input_revision IS NOT NULL", "'coverage_parks'", "'coverage'")
	canonicalRequire(t, coverage, "eligible AS MATERIALIZED")
	canonicalForbid(t, coverage, "segment.geom", "match.geom", "match.*", "count(DISTINCT workout")
}

func TestAuthorizeMapTileContract(t *testing.T) {
	source := tileLocalSQL(t)
	body := coverageDisplayFunction(t, source, "authorize_map_tile")
	canonicalRequire(t, source, "target_generation bigint, coverage boolean) RETURNS timestamptz",
		"GRANT EXECUTE ON FUNCTION app.authorize_map_tile(uuid,uuid,uuid,bigint,boolean) TO workouts_api")
	canonicalRequire(t, body, "LEAST(selection.expires_at, session_row.expires_at)",
		"target_account_id = app.current_account_id() AND target_session_id = app.current_session_id()",
		"target_generation >= 1 AND coverage IS NOT NULL", "selection.id = target_selection_id",
		"selection.account_id = target_account_id", "selection.session_id = target_session_id",
		"selection.generation = target_generation", "generation.generation = target_generation",
		"selection.expires_at > transaction_timestamp()", "session_row.expires_at > transaction_timestamp()",
		"session_row.revoked_at IS NULL", "principal.disabled_at IS NULL", "account.state = 'active'",
		"NOT coverage OR EXISTS", "context.id = selection.coverage_display_context_id",
		"context.generation = target_generation", "context.revision = revision.revision",
		"context.start_date = selection.start_date AND context.end_date = selection.end_date",
		"context.ready", "context.policy_version = 'coverage-display-identity-v1'", "context.label_locale = 'en'")
	canonicalForbid(t, body, "INSERT", "UPDATE", "DELETE", "ST_", "tile_chunks", "workout_segment_matches", "RAISE")
}

// Approved 001-030 foundation source snapshot, not a database receipt.
func TestTileMigrationPreservesHistoricalSQL(t *testing.T) {
	hash := sha256.New()
	for _, file := range migrationFiles[:30] {
		raw, err := migrations.Files.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintln(hash, file)
		hash.Write(raw)
	}
	const expected = "c38333cbc5ab91e95653747973b710fe9b6b5f93449bd51a26887d418a8071b3"
	if got := fmt.Sprintf("%x", hash.Sum(nil)); got != expected {
		t.Fatalf("001-030 must remain byte-identical: sha256=%s", got)
	}
}
