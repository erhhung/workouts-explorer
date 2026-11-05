package database

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestTileReadinessPostgreSQL(t *testing.T) {
	const prefix = "PROJECT_TILE_READINESS_"
	dsn := os.Getenv(prefix + "DATABASE_URL")
	if dsn == "" || os.Getenv(prefix+"DISPOSABLE") != "1" {
		t.Skip("explicit synthetic tile-readiness admin URL and opt-in required")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid tile-readiness configuration")
	}
	if (cfg.Host != "127.0.0.1" && cfg.Host != "::1" && cfg.Host != "localhost") ||
		!strings.Contains(cfg.Database, "test") || strings.Contains(cfg.Database, "clone") ||
		strings.Contains(cfg.Database, "actual") || strings.Contains(cfg.Database, "live") {
		t.Fatal("tile-readiness mutations require a synthetic loopback database")
	}
	for _, fallback := range cfg.Fallbacks {
		if fallback.Host != cfg.Host || fallback.Port != cfg.Port {
			t.Fatal("alternate tile-readiness endpoints are prohibited")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	admin, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close(context.Background())
	var safe bool
	if err := admin.QueryRow(ctx, `SELECT rolsuper AND pg_try_advisory_lock(310031,hashtext(current_database()))
		FROM pg_roles WHERE rolname=current_user`).Scan(&safe); err != nil || !safe {
		t.Fatal("exclusive synthetic superuser harness required", err)
	}
	check := func(t *testing.T, tx pgx.Tx, query string, want bool) {
		t.Helper()
		for _, role := range []string{"workouts_api", "workouts_worker", "workouts_coverage_worker"} {
			if _, err := tx.Exec(ctx, "SET LOCAL ROLE "+role); err != nil {
				t.Fatal(err)
			}
			var ready bool
			if err := tx.QueryRow(ctx, query, SupportedSchemaVersion).Scan(&ready); err != nil || ready != want {
				t.Fatalf("tile readiness %s=%t want=%t: %v", role, ready, want, err)
			}
			if _, err := tx.Exec(ctx, "RESET ROLE"); err != nil {
				t.Fatal(err)
			}
		}
	}
	tx, err := admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	check(t, tx, runtimeTileReadinessSQL, true)
	check(t, tx, runtimeTileLODReadinessSQL, true)
	check(t, tx, runtimeFullReadinessSQL, true)
	_ = tx.Rollback(ctx)
	for name, mutation := range map[string]string{
		"minimum runtime":                  `UPDATE app.schema_metadata SET minimum_runtime_version=30`,
		"raw forced RLS":                   `ALTER TABLE app.raw_route_tile_chunks NO FORCE ROW LEVEL SECURITY`,
		"coverage policy":                  `DROP POLICY coverage_tile_chunks_owner_policy ON app.coverage_tile_chunks`,
		"raw runtime table grant":          `GRANT SELECT ON app.raw_route_tile_chunks TO workouts_api`,
		"coverage column grant":            `GRANT SELECT(geom) ON app.coverage_tile_chunks TO workouts_tiles`,
		"missing GiST":                     `DROP INDEX app.raw_route_tile_chunks_geom_idx`,
		"missing selection covering index": `DROP INDEX app.map_selection_workouts_tile_lookup_idx`,
		"missing workout covering index":   `DROP INDEX app.workouts_tile_lookup_idx`,
		"missing trigger":                  `DROP TRIGGER workout_matches_tile_chunks_after_write ON app.workout_segment_matches`,
		"disabled trigger":                 `ALTER TABLE app.workout_routes DISABLE TRIGGER workout_routes_tile_chunks_after_write`,
		"helper public execute":            `GRANT EXECUTE ON FUNCTION app.authorize_map_tile(uuid,uuid,uuid,bigint,boolean) TO PUBLIC`,
		"helper tile execute":              `GRANT EXECUTE ON FUNCTION app.authorize_map_tile(uuid,uuid,uuid,bigint,boolean) TO workouts_tiles`,
		"helper API grant missing":         `REVOKE EXECUTE ON FUNCTION app.authorize_map_tile(uuid,uuid,uuid,bigint,boolean) FROM workouts_api`,
		"helper security invoker":          `ALTER FUNCTION app.authorize_map_tile(uuid,uuid,uuid,bigint,boolean) SECURITY INVOKER`,
		"helper body": `CREATE OR REPLACE FUNCTION app.authorize_map_tile(target_account_id uuid,target_session_id uuid,target_selection_id uuid,target_generation bigint,coverage boolean)
			RETURNS timestamptz LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,app AS 'SELECT now()'`,
		"raw MVT API grant": `GRANT EXECUTE ON FUNCTION app.raw_route_mvt(integer,integer,integer,json) TO workouts_api`,
		"LOD body": `CREATE OR REPLACE FUNCTION app.tile_display_geometry(input geometry,z integer) RETURNS geometry
			LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE SET search_path=pg_catalog,public AS 'SELECT input'`,
	} {
		t.Run(name, func(t *testing.T) {
			tx, err := admin.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if _, err := tx.Exec(ctx, mutation); err != nil {
				t.Fatal(err)
			}
			check(t, tx, runtimeTileReadinessSQL, false)
		})
	}
	for name, mutation := range map[string]string{
		"dense table RLS":         `ALTER TABLE app.raw_route_lod_chunks NO FORCE ROW LEVEL SECURITY`,
		"dense table policy":      `DROP POLICY raw_route_lod_chunks_owner_policy ON app.raw_route_lod_chunks`,
		"dense table grant":       `GRANT SELECT ON app.raw_route_lod_chunks TO workouts_api`,
		"dense column grant":      `GRANT SELECT(geom) ON app.raw_route_lod_chunks TO workouts_tiles`,
		"dense GiST":              `DROP INDEX app.raw_route_lod_chunks_geom_idx`,
		"dense trigger":           `ALTER TABLE app.workout_routes DISABLE TRIGGER workout_routes_lod_chunks_after_write`,
		"dense helper public":     `GRANT EXECUTE ON FUNCTION app.tile_scope(json,boolean) TO PUBLIC`,
		"dense helper tiles":      `GRANT EXECUTE ON FUNCTION app.tile_scope(json,boolean) TO workouts_tiles`,
		"dense MVT API":           `GRANT EXECUTE ON FUNCTION app.raw_route_mvt(integer,integer,integer,json) TO workouts_api`,
		"dense MVT missing tiles": `REVOKE EXECUTE ON FUNCTION app.coverage_mvt(integer,integer,integer,json) FROM workouts_tiles`,
		"dense MVT invoker":       `ALTER FUNCTION app.raw_route_mvt(integer,integer,integer,json) SECURITY INVOKER`,
		"dense memory config":     `ALTER FUNCTION app.coverage_mvt(integer,integer,integer,json) SET work_mem='64MB'`,
		"dense body": `CREATE OR REPLACE FUNCTION app.project_route_lod(input geometry)
			RETURNS TABLE(zoom_band integer,component integer,piece integer,geom geometry)
			LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE SET search_path=pg_catalog,public AS 'SELECT 10,1,0,input'`,
	} {
		t.Run(name, func(t *testing.T) {
			tx, err := admin.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(context.Background())
			if _, err := tx.Exec(ctx, mutation); err != nil {
				t.Fatal(err)
			}
			check(t, tx, runtimeTileLODReadinessSQL, false)
			check(t, tx, runtimeFullReadinessSQL, false)

		})
	}
}
