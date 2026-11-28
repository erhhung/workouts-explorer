package migrations_test

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestCleanedRouteStatisticsPostgreSQL(t *testing.T) {
	if os.Getenv("ROUTE_STATS_TEST_DISPOSABLE") != "1" {
		t.Skip("explicit synthetic route-statistics database required")
	}
	cfg, err := pgx.ParseConfig(os.Getenv("MIGRATION_DATABASE_URL"))
	if err != nil || cfg.Host != "127.0.0.1" || !strings.HasPrefix(cfg.Database, "acceptance_test_route_stats") {
		t.Fatal("refusing non-synthetic statistics database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	db := testDatabases{ctx: ctx, migration: openPool(t, ctx, os.Getenv("MIGRATION_DATABASE_URL"), 2), api: openPool(t, ctx, os.Getenv("API_DATABASE_URL"), 2), worker: openPool(t, ctx, os.Getenv("WORKER_DATABASE_URL"), 2)}
	f := newCanonicalDisplayFixture(t, db)
	for _, fixture := range []struct {
		name, points             string
		count                    int
		gain                     float64
		fastestMile, slowestMile float64
	}{
		{"continuous units and normalized final partial", `SELECT i AS sequence,i*5 AS seconds,i*10.0 AS meters,100.0+i AS altitude FROM generate_series(0,240) i`, 241, 240, 804.672, 804.672},
		{"cached pre-workout outlier", `SELECT 0 AS sequence,-1045 AS seconds,-5000.0 AS meters,9000.0 AS altitude UNION ALL SELECT i+1,i*5,i*10.0,100.0+i FROM generate_series(0,240) i`, 242, 240, 804.672, 804.672},
		{"time-invalid near fix cannot shrink displayed geometry bounds", `SELECT 0 AS sequence,-100 AS seconds,-50.0 AS meters,9000.0 AS altitude UNION ALL SELECT i+1,i*5,i*10.0,100.0+i FROM generate_series(0,240) i`, 242, 240, 804.672, 804.672},
		{"spatially separated partials normalized without gap", `SELECT i AS sequence,i*5 AS seconds,CASE WHEN i<=90 THEN i*10.0 ELSE 5000+(i-91)*10.0 END AS meters,CASE WHEN i<=90 THEN 100.0+i ELSE 1000.0+i-91 END AS altitude FROM generate_series(0,181) i`, 182, 180, 804.672, 804.672},
		{"time-separated partials normalized without pause gap", `SELECT i AS sequence,i*5+CASE WHEN i>90 THEN 60 ELSE 0 END AS seconds,i*10.0 AS meters,100.0+i AS altitude FROM generate_series(0,181) i`, 182, 180, 804.672, 804.672},
		{"duplicate time retains continuity without instantaneous distance", `SELECT i AS sequence,CASE WHEN i=91 THEN 450 ELSE i*5 END AS seconds,i*10.0 AS meters,100.0+i AS altitude FROM generate_series(0,181) i`, 182, 180, 804.672, 809.672},
		{"backward time excludes unobserved leg", `SELECT i AS sequence,CASE WHEN i=91 THEN 449 ELSE i*5 END AS seconds,i*10.0 AS meters,100.0+i AS altitude FROM generate_series(0,181) i`, 182, 180, 804.672, 810.672},
		{"rejected walking spike is not reintroduced", `SELECT i AS sequence,i*5 AS seconds,CASE WHEN i=91 THEN 1010.0 ELSE i*10.0 END AS meters,CASE WHEN i=91 THEN 9000.0 ELSE 100.0+i END AS altitude FROM generate_series(0,181) i`, 182, 177, 804.672, 804.672},
		{"short route average is distance weighted", `SELECT i AS sequence,CASE WHEN i<=100 THEN i*5 ELSE 500+(i-100)*3 END AS seconds,i*10.0 AS meters,100.0+i AS altitude FROM generate_series(0,150) i`, 151, 150, 697.3824, 697.3824},
		{"six minute half mile normalizes to twelve minute pace", `SELECT i AS sequence,i*3.6 AS seconds,i*8.04672 AS meters,100.0+i AS altitude FROM generate_series(0,100) i`, 101, 100, 720, 720},
		{"stationary tail is retained on final unit", `SELECT i AS sequence,i*5 AS seconds,least(i,100)*10.0 AS meters,100.0+least(i,100) AS altitude FROM generate_series(0,200) i`, 201, 100, 1609.344, 1609.344},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			tx := beginAccount(t, ctx, db.migration, f.account)
			defer tx.Rollback(ctx)
			canonicalDisplayFence(t, db, tx, f)
			coverageDisplayExec(t, db, tx, `UPDATE app.workout_types SET provider_label='Outdoor Walk' WHERE id=(SELECT workout_type_id FROM app.workouts WHERE id=$1)`, f.workouts[0])
			coverageDisplayExec(t, db, tx, `UPDATE app.workouts SET started_at='2026-01-10T08:00:00Z',ended_at='2026-01-10T09:00:00Z' WHERE id=$1`, f.workouts[0])
			coverageDisplayExec(t, db, tx, `DELETE FROM app.workout_route_points WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0])
			coverageDisplayExec(t, db, tx, `INSERT INTO app.workout_route_points(account_id,workout_id,sequence,recorded_at,longitude,latitude,altitude,horizontal_accuracy)
            SELECT $1,$2,sequence,'2026-01-10T08:00:00Z'::timestamptz+seconds*interval '1 second',
            ST_X(ST_Project(ST_SetSRID(ST_MakePoint(10,20),4326)::geography,abs(meters),CASE WHEN meters<0 THEN 3*pi()/2 ELSE pi()/2 END)::geometry),
            ST_Y(ST_Project(ST_SetSRID(ST_MakePoint(10,20),4326)::geography,abs(meters),CASE WHEN meters<0 THEN 3*pi()/2 ELSE pi()/2 END)::geometry),altitude,5 FROM (`+fixture.points+`) p`, f.account, f.workouts[0])
			coverageDisplayExec(t, db, tx, `SELECT app.replace_workout_route_summary($1,$2,9,19,11,21,0,9000,9000,true)`, f.workouts[0], fixture.count)
			var before string
			coverageDisplayScan(t, db, tx, `SELECT md5(string_agg(row_to_json(p)::text,'' ORDER BY sequence)) FROM app.workout_route_points p WHERE account_id=$1 AND workout_id=$2`, []any{f.account, f.workouts[0]}, &before)
			var replaced bool
			coverageDisplayScan(t, db, tx, `SELECT app.replace_workout_split_summary($1)`, []any{f.workouts[0]}, &replaced)
			if !replaced {
				t.Fatal("fenced statistics replacement failed")
			}
			var fastest, slowest *float64
			var gain, minAltitude, maxAltitude float64
			var count int
			var after string
			coverageDisplayScan(t, db, tx, `SELECT fastest_mile_split_seconds,slowest_mile_split_seconds,elevation_gain,minimum_altitude,maximum_altitude,point_count FROM app.workout_routes WHERE account_id=$1 AND workout_id=$2`, []any{f.account, f.workouts[0]}, &fastest, &slowest, &gain, &minAltitude, &maxAltitude, &count)
			if fastest == nil || slowest == nil || math.Abs(*fastest-fixture.fastestMile) > 0.05 || math.Abs(*slowest-fixture.slowestMile) > 0.05 {
				t.Fatalf("normalized paces: %v %v", fastest, slowest)
			}
			var meters, seconds, fastKM, slowKM float64
			coverageDisplayScan(t, db, tx, `SELECT pace_distance_meters,pace_elapsed_seconds,fastest_kilometer_split_seconds,slowest_kilometer_split_seconds FROM app.workout_routes WHERE account_id=$1 AND workout_id=$2`, []any{f.account, f.workouts[0]}, &meters, &seconds, &fastKM, &slowKM)
			average := seconds / meters
			if average*1609.344 < *fastest-0.001 || average*1609.344 > *slowest+0.001 || average*1000 < fastKM-0.001 || average*1000 > slowKM+0.001 {
				t.Fatal("average outside the normalized split range")
			}
			if fixture.count == 151 && (math.Abs(meters-1500) > 0.01 || math.Abs(seconds-650) > 0.01) {
				t.Fatal("partial weighting changed totals", meters, seconds)
			}
			if gain != fixture.gain || minAltitude != 100 || maxAltitude >= 9000 || count != fixture.count {
				t.Fatalf("cleaned allocation gain=%v altitude=%v..%v rawCount=%d", gain, minAltitude, maxAltitude, count)
			}
			var contains bool
			coverageDisplayScan(t, db, tx, `SELECT minimum_longitude<=ST_XMin(route::box3d) AND minimum_latitude<=ST_YMin(route::box3d)
                AND maximum_longitude>=ST_XMax(route::box3d) AND maximum_latitude>=ST_YMax(route::box3d)
                FROM app.workout_routes WHERE account_id=$1 AND workout_id=$2`, []any{f.account, f.workouts[0]}, &contains)
			if !contains {
				t.Fatal("statistics bounds clipped the existing displayed route")
			}
			coverageDisplayScan(t, db, tx, `SELECT md5(string_agg(row_to_json(p)::text,'' ORDER BY sequence)) FROM app.workout_route_points p WHERE account_id=$1 AND workout_id=$2`, []any{f.account, f.workouts[0]}, &after)
			if before != after {
				t.Fatal("immutable observations changed")
			}
			var generationBefore, generationAfter int64
			coverageDisplayScan(t, db, tx, `SELECT generation FROM app.account_data_generations WHERE account_id=$1`, []any{f.account}, &generationBefore)
			coverageDisplayScan(t, db, tx, `SELECT app.replace_workout_split_summary($1)`, []any{f.workouts[0]}, &replaced)
			coverageDisplayScan(t, db, tx, `SELECT generation FROM app.account_data_generations WHERE account_id=$1`, []any{f.account}, &generationAfter)
			if !replaced || generationBefore != generationAfter {
				t.Fatal("unchanged statistics caused a write/invalidation")
			}
			coverageDisplayExec(t, db, tx, `SAVEPOINT pace_range_sanity`)
			_, invalid := tx.Exec(ctx, `UPDATE app.workout_routes SET fastest_kilometer_split_seconds=pace_elapsed_seconds*1000/pace_distance_meters+10,
                slowest_kilometer_split_seconds=pace_elapsed_seconds*1000/pace_distance_meters+20 WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0])
			if problem, ok := invalid.(*pgconn.PgError); !ok || problem.Code != "23514" || problem.ConstraintName != "workout_routes_pace_consistency_check" {
				t.Fatal("contradictory pace cache was accepted", invalid)
			}
			coverageDisplayExec(t, db, tx, `ROLLBACK TO SAVEPOINT pace_range_sanity`)
			coverageDisplayExec(t, db, tx, `RELEASE SAVEPOINT pace_range_sanity`)
			var outside *float64
			coverageDisplayScan(t, db, tx, `SELECT fastest_mile_split_seconds FROM app.workout_route_statistics($1,$2)`, []any{uuid.New(), f.workouts[0]}, &outside)
			if outside != nil {
				t.Fatal("foreign account read statistics")
			}
		})
	}
	for _, role := range []string{"workouts_api", "workouts_worker", "workouts_coverage_worker", "workouts_tiles"} {
		var allowed bool
		if err := db.migration.QueryRow(ctx, `SELECT has_function_privilege($1,'app.workout_route_statistics(uuid,uuid)','EXECUTE')`, role).Scan(&allowed); err != nil || allowed {
			t.Fatal("private statistics helper exposed", role, err)
		}
	}
}

func coverageDisplayScan(t *testing.T, db testDatabases, tx pgx.Tx, query string, args []any, dest ...any) {
	t.Helper()
	if err := tx.QueryRow(db.ctx, query, args...).Scan(dest...); err != nil {
		t.Fatal(err)
	}
}
