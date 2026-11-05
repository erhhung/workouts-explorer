package migrations_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/coverage"
	"github.com/jackc/pgx/v5"
)

func TestRawRouteSpatialGapPostgreSQL(t *testing.T) {
	if os.Getenv("ROUTE_GAP_TEST_DISPOSABLE") != "1" {
		t.Skip("explicit private synthetic route-gap database required")
	}
	cfg, err := pgx.ParseConfig(os.Getenv("MIGRATION_DATABASE_URL"))
	if err != nil || cfg.Host != "/tmp/opencode/tile-local031-rehearsal/socket" || (cfg.Database != "workouts_route_gap_test" && cfg.Database != "schema031_reference_test") {
		t.Fatal("refusing non-synthetic route-gap database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	db := testDatabases{ctx: ctx, migration: openPool(t, ctx, os.Getenv("MIGRATION_DATABASE_URL"), 2), api: openPool(t, ctx, os.Getenv("API_DATABASE_URL"), 2), worker: openPool(t, ctx, os.Getenv("WORKER_DATABASE_URL"), 2)}
	if coverage.ExperimentalRules().MaxSpatialGapMeters != 200 {
		t.Fatal("raw and Coverage limits diverged")
	}
	for _, test := range []struct{ name, sql string }{
		{"unchanged components", `WITH r AS(SELECT ST_GeomFromText('MULTILINESTRING((0 0,0.0001 0),(0.1 0,0.1001 0))',4326) g)SELECT ST_AsEWKB(g)=ST_AsEWKB(app.split_raw_route_spatial_gaps(g)) FROM r`},
		{"spatial split", `SELECT ST_AsEWKB(app.split_raw_route_spatial_gaps(ST_GeomFromText('MULTILINESTRING((0 0,0.0001 0,0.1 0,0.1001 0))',4326)))=ST_AsEWKB(ST_GeomFromText('MULTILINESTRING((0 0,0.0001 0),(0.1 0,0.1001 0))',4326))`},
		{"isolated outlier", `SELECT ST_AsEWKB(app.split_raw_route_spatial_gaps(ST_GeomFromText('MULTILINESTRING((0 0,0.0001 0,0.1 0,0.0002 0,0.0003 0))',4326)))=ST_AsEWKB(ST_GeomFromText('MULTILINESTRING((0 0,0.0001 0),(0.0002 0,0.0003 0))',4326))`},
		{"no invented singleton dots", `SELECT app.split_raw_route_spatial_gaps(ST_GeomFromText('MULTILINESTRING((0 0,0.1 0))',4326)) IS NULL`},
		{"stationary preservation", `WITH r AS(SELECT ST_GeomFromText('MULTILINESTRING((0 0,0 0),(0.1 0,0.1 0))',4326)g)SELECT ST_AsEWKB(g)=ST_AsEWKB(app.split_raw_route_spatial_gaps(g))FROM r`},
		{"idempotence", `WITH r AS(SELECT app.split_raw_route_spatial_gaps(ST_GeomFromText('MULTILINESTRING((0 0,0.0001 0,0.1 0,0.1001 0))',4326))g)SELECT ST_AsEWKB(g)=ST_AsEWKB(app.split_raw_route_spatial_gaps(g)) FROM r`},
	} {
		t.Run(test.name, func(t *testing.T) { coverageDisplayCheck(t, db, test.sql) })
	}
	f := newCanonicalDisplayFixture(t, db)
	tx := beginAccount(t, ctx, db.migration, f.account)
	defer tx.Rollback(ctx)
	canonicalDisplayFence(t, db, tx, f)
	coverageDisplayExec(t, db, tx, `DELETE FROM app.workout_route_points WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0])
	coverageDisplayExec(t, db, tx, `INSERT INTO app.workout_route_points(account_id,workout_id,sequence,recorded_at,longitude,latitude)SELECT $1,$2,i,'2026-01-10'::timestamptz+i*interval '1 second',lon,0 FROM unnest(ARRAY[0,0.0001,0.1,0.1001]::double precision[])WITH ORDINALITY AS p(lon,i)`, f.account, f.workouts[0])
	coverageDisplayExec(t, db, tx, `SELECT app.replace_workout_route_summary($1,4,0,0,0.1001,0,NULL,NULL,NULL,false)`, f.workouts[0])
	var valid bool
	if err := tx.QueryRow(ctx, `SELECT ST_AsEWKB(route)=ST_AsEWKB(ST_GeomFromText('MULTILINESTRING((0 0,0.0001 0),(0.1 0,0.1001 0))',4326)) AND point_count=4 FROM app.workout_routes WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0]).Scan(&valid); err != nil || !valid {
		t.Fatal("fenced future-ingest summary retained a jump", err)
	}
	var points int
	if err := tx.QueryRow(ctx, `SELECT count(*)FROM app.workout_route_points WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0]).Scan(&points); err != nil || points != 4 {
		t.Fatal("source observations changed", err)
	}
	for _, role := range []string{"workouts_api", "workouts_worker", "workouts_coverage_worker", "workouts_tiles"} {
		var allowed bool
		if err := db.migration.QueryRow(ctx, `SELECT has_function_privilege($1,'app.split_raw_route_spatial_gaps(geometry)','EXECUTE')`, role).Scan(&allowed); err != nil || allowed {
			t.Fatal("unfenced geometry helper exposed to "+role, err)
		}
	}
}
