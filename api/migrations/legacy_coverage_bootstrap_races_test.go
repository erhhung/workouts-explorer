package migrations_test

import (
	"bytes"
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func testBootstrapHardening(t *testing.T, db testDatabases, admin *pgx.Conn) {
	t.Run("CanonicalDigestParity", func(t *testing.T) {
		for _, variant := range []struct {
			name, mutation string
			args           []any
		}{
			{"whole seconds and null optional fields", "", nil},
			{"UTC offset and trailing microsecond zeros", `UPDATE app.workout_route_points SET recorded_at='2026-01-01T07:30:00.123400+05:30',altitude=0,speed=0,course=0,horizontal_accuracy=0,vertical_accuracy=0,speed_accuracy=0,course_accuracy=0 WHERE account_id=$1 AND workout_id=$2`, nil},
			{"microsecond precision and mixed optional fields", `UPDATE app.workout_route_points SET recorded_at='2026-01-01T00:00:00.000001Z',altitude=-1.25,course=42.5,vertical_accuracy=3,speed_accuracy=0.25 WHERE account_id=$1 AND workout_id=$2`, nil},
			{"negative zero floats", `UPDATE app.workout_route_points SET latitude=$3,longitude=$3,altitude=$3,speed=$3,course=$3,horizontal_accuracy=$3,vertical_accuracy=$3,speed_accuracy=$3,course_accuracy=$3 WHERE account_id=$1 AND workout_id=$2`, []any{math.Copysign(0, -1)}},
			{"31000 points", `INSERT INTO app.workout_route_points(account_id,workout_id,sequence,recorded_at,latitude,longitude) SELECT $1,$2,n,'2026-01-01Z'::timestamptz+n*interval '1 second',37,-122 FROM generate_series(2,30999) n`, nil},
			{"50000 points", `INSERT INTO app.workout_route_points(account_id,workout_id,sequence,recorded_at,latitude,longitude) SELECT $1,$2,n,'2026-01-01Z'::timestamptz+n*interval '1 second',37,-122 FROM generate_series(2,49999) n`, nil},
		} {
			t.Run(variant.name, func(t *testing.T) {
				f := newBootstrapFixture(t, db, 1)
				claimBootstrap(t, db, &f)
				if variant.mutation != "" {
					args := append([]any{f.account, f.workouts[0]}, variant.args...)
					bootstrapMutatePoints(t, db, f, variant.mutation, args...)
				}
				digest := bootstrapCurrentDigest(t, db, f)
				tx := beginAccount(t, db.ctx, db.coverage, f.account)
				defer tx.Rollback(db.ctx)
				if _, err := tx.Exec(db.ctx, `SET LOCAL TIME ZONE 'America/New_York'`); err != nil {
					t.Fatal(err)
				}
				var revision *int64
				if err := tx.QueryRow(db.ctx, `SELECT app.repair_coverage_reconciliation_input($1,$2,$3,$4,$5,0,NULL,$6)`,
					f.account, f.revision, bootstrapWorker, f.lease, f.workouts[0], digest).Scan(&revision); err != nil || revision == nil || *revision != 1 {
					t.Fatalf("SQL/Go canonical parity rejected: revision=%v err=%v", revision, err)
				}
				if err := tx.Commit(db.ctx); err != nil {
					t.Fatal(err)
				}
				bootstrapAssertState(t, db, f, 1, digest)
			})
		}
	})
	t.Run("CommittedPointsChangedBeforeRepair", func(t *testing.T) {
		for _, existing := range []bool{false, true} {
			t.Run(map[bool]string{false: "missing state", true: "existing state"}[existing], func(t *testing.T) {
				f := newBootstrapFixture(t, db, 1)
				claimBootstrap(t, db, &f)
				old := bootstrapCurrentDigest(t, db, f)
				revision := int64(0)
				var oldDigest []byte
				if existing {
					revision, oldDigest = 1, old
					bootstrapExec(t, db, f, `INSERT INTO app.workout_coverage_states(account_id,workout_id,route_input_revision,route_input_sha256,readiness_state) VALUES($1,$2,1,$3,'unresolved')`, f.account, f.workouts[0], old)
				}
				bootstrapMutatePoints(t, db, f, `UPDATE app.workout_route_points SET altitude=9.5 WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0])
				tx := beginAccount(t, db.ctx, db.coverage, f.account)
				var result *int64
				err := tx.QueryRow(db.ctx, `SELECT app.repair_coverage_reconciliation_input($1,$2,$3,$4,$5,$6,$7,$8)`,
					f.account, f.revision, bootstrapWorker, f.lease, f.workouts[0], revision, oldDigest, old).Scan(&result)
				_ = tx.Rollback(db.ctx)
				bootstrapSQLState(t, err, "40001")
				bootstrapAssertState(t, db, f, revision, oldDigest)
				current := bootstrapCurrentDigest(t, db, f)
				tx = beginAccount(t, db.ctx, db.coverage, f.account)
				defer tx.Rollback(db.ctx)
				if err := tx.QueryRow(db.ctx, `SELECT app.repair_coverage_reconciliation_input($1,$2,$3,$4,$5,$6,$7,$8)`,
					f.account, f.revision, bootstrapWorker, f.lease, f.workouts[0], revision, oldDigest, current).Scan(&result); err != nil || result == nil || *result != revision+1 {
					t.Fatalf("retry rejected current points: result=%v err=%v", result, err)
				}
			})
		}
	})
	t.Run("RouteAndStateContentionThenExpiry", func(t *testing.T) {
		for _, table := range []string{"workout_routes", "workout_coverage_states"} {
			t.Run(table, func(t *testing.T) {
				f := newBootstrapFixture(t, db, 1)
				claimBootstrap(t, db, &f)
				digest := bootstrapDigest()
				revision := int64(0)
				var old []byte
				if table == "workout_coverage_states" {
					revision, old = 1, digest
					bootstrapExec(t, db, f, `INSERT INTO app.workout_coverage_states(account_id,workout_id,route_input_revision,route_input_sha256,readiness_state) VALUES($1,$2,1,$3,'unresolved')`, f.account, f.workouts[0], digest)
				}
				blocker := beginAccount(t, db.ctx, db.migration, f.account)
				defer blocker.Rollback(db.ctx)
				if _, err := blocker.Exec(db.ctx, `SELECT 1 FROM app.`+table+` WHERE account_id=$1 AND workout_id=$2 FOR UPDATE`, f.account, f.workouts[0]); err != nil {
					t.Fatal(err)
				}
				tx := beginAccount(t, db.ctx, db.coverage, f.account)
				var result *int64
				err := tx.QueryRow(db.ctx, `SELECT app.repair_coverage_reconciliation_input($1,$2,$3,$4,$5,$6,$7,$8)`, f.account, f.revision, bootstrapWorker, f.lease, f.workouts[0], revision, old, digest).Scan(&result)
				_ = tx.Rollback(db.ctx)
				bootstrapSQLState(t, err, "55P03")
				bootstrapExec(t, db, f, `UPDATE app.coverage_reconciliation_accounts SET lease_expires_at=clock_timestamp()-interval '1 second' WHERE account_id=$1`, f.account)
				if err := blocker.Commit(db.ctx); err != nil {
					t.Fatal(err)
				}
				tx = beginAccount(t, db.ctx, db.coverage, f.account)
				defer tx.Rollback(db.ctx)
				if err := tx.QueryRow(db.ctx, `SELECT app.repair_coverage_reconciliation_input($1,$2,$3,$4,$5,$6,$7,$8)`, f.account, f.revision, bootstrapWorker, f.lease, f.workouts[0], revision, old, digest).Scan(&result); err != nil || result != nil {
					t.Fatalf("expired retry mutated state: %v err=%v", result, err)
				}
				bootstrapAssertState(t, db, f, revision, old)
			})
		}
	})
	t.Run("PostMutationLeaseExpiryRollsBack", func(t *testing.T) {
		for _, existing := range []bool{false, true} {
			t.Run(map[bool]string{false: "insert", true: "update"}[existing], func(t *testing.T) {
				f := newBootstrapFixture(t, db, 1)
				claimBootstrap(t, db, &f)
				digest := bootstrapDigest()
				revision := int64(0)
				var old []byte
				if existing {
					revision, old = 1, digest
					bootstrapExec(t, db, f, `INSERT INTO app.workout_coverage_states(account_id,workout_id,route_input_revision,route_input_sha256,readiness_state) VALUES($1,$2,1,$3,'unresolved')`, f.account, f.workouts[0], digest)
				}
				bootstrapDelayTrigger(t, db, f, "AFTER", "0.8", false)
				bootstrapExec(t, db, f, `UPDATE app.coverage_reconciliation_accounts SET lease_expires_at=clock_timestamp()+interval '0.5 seconds' WHERE account_id=$1`, f.account)
				tx := beginAccount(t, db.ctx, db.coverage, f.account)
				var result *int64
				err := tx.QueryRow(db.ctx, `SELECT app.repair_coverage_reconciliation_input($1,$2,$3,$4,$5,$6,$7,$8)`, f.account, f.revision, bootstrapWorker, f.lease, f.workouts[0], revision, old, digest).Scan(&result)
				_ = tx.Rollback(db.ctx)
				bootstrapSQLState(t, err, "40001")
				var sqlErr *pgconn.PgError
				if !errors.As(err, &sqlErr) || sqlErr.Message != "coverage reconciliation lease expired" {
					t.Fatal("expected post-mutation lease fence")
				}
				bootstrapAssertState(t, db, f, revision, old)
			})
		}
	})
	t.Run("ConcurrentUniqueReservationCannotDeadlock", func(t *testing.T) {
		f := newBootstrapFixture(t, db, 1)
		claimBootstrap(t, db, &f)
		parent := insertQueued(t, db.ctx, db.api, f.account, uuid.Nil, "manual_ingest")
		child := insertQueued(t, db.ctx, db.api, f.account, parent, "manual_ingest_source")
		lease := uuid.New()
		if !callBool(t, db.ctx, db.worker, f.account, `SELECT app.claim_job($1,'unique-initializer',$2,interval '2 minutes')`, child, lease) {
			t.Fatal("initializer claim failed")
		}
		bootstrapDelayTrigger(t, db, f, "BEFORE", "2", true)
		repair := beginAccount(t, db.ctx, db.coverage, f.account)
		defer repair.Rollback(db.ctx)
		if _, err := repair.Exec(db.ctx, `SELECT set_config('coverage_bootstrap.test_delay','yes',true)`); err != nil {
			t.Fatal(err)
		}
		var repairPID int
		if err := repair.QueryRow(db.ctx, `SELECT pg_backend_pid()`).Scan(&repairPID); err != nil {
			t.Fatal(err)
		}
		digest := bootstrapDigest()
		done := make(chan error, 1)
		go func() {
			var result *int64
			done <- repair.QueryRow(db.ctx, `SELECT app.repair_coverage_reconciliation_input($1,$2,$3,$4,$5,0,NULL,$6)`, f.account, f.revision, bootstrapWorker, f.lease, f.workouts[0], digest).Scan(&result)
		}()
		bootstrapWaitEvent(t, db.ctx, admin, repairPID, "PgSleep")
		actor := beginAccount(t, db.ctx, db.worker, f.account)
		defer actor.Rollback(db.ctx)
		var actorPID int
		if err := actor.QueryRow(db.ctx, `SELECT pg_backend_pid()`).Scan(&actorPID); err != nil {
			t.Fatal(err)
		}
		initialized := make(chan error, 1)
		go func() {
			var revision int64
			var changed bool
			initialized <- actor.QueryRow(db.ctx, `SELECT * FROM app.initialize_workout_coverage($1,$2,$3,$4,'unique-initializer',$5)`, f.account, f.workouts[0], digest, child, lease).Scan(&revision, &changed)
		}()
		bootstrapWaitEvent(t, db.ctx, admin, actorPID, "transactionid")
		bootstrapSQLState(t, <-done, "55P03")
		if err := repair.Rollback(db.ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-initialized; err != nil {
			t.Fatalf("concurrent unique initializer failed: %v", err)
		}
		if err := actor.Commit(db.ctx); err != nil {
			t.Fatal(err)
		}
		bootstrapAssertState(t, db, f, 1, digest)
	})
	t.Run("EmptyAndOverflowRejectRepair", func(t *testing.T) {
		for _, count := range []int{0, 50001} {
			f := newBootstrapFixture(t, db, 1)
			claimBootstrap(t, db, &f)
			if count == 0 {
				bootstrapMutatePoints(t, db, f, `DELETE FROM app.workout_route_points WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0])
			} else {
				bootstrapMutatePoints(t, db, f, `INSERT INTO app.workout_route_points(account_id,workout_id,sequence,recorded_at,latitude,longitude) SELECT $1,$2,n,'2026-01-01Z'::timestamptz+n*interval '1 second',37,-122 FROM generate_series(2,50000) n`, f.account, f.workouts[0])
			}
			digest := bootstrapCurrentDigest(t, db, f)
			tx := beginAccount(t, db.ctx, db.coverage, f.account)
			var result *int64
			err := tx.QueryRow(db.ctx, `SELECT app.repair_coverage_reconciliation_input($1,$2,$3,$4,$5,0,NULL,$6)`, f.account, f.revision, bootstrapWorker, f.lease, f.workouts[0], digest).Scan(&result)
			_ = tx.Rollback(db.ctx)
			bootstrapSQLState(t, err, "40001")
			bootstrapAssertState(t, db, f, 0, nil)
		}
	})
}

func bootstrapMutatePoints(t *testing.T, db testDatabases, f bootstrapFixture, query string, args ...any) {
	t.Helper()
	tx := beginAccount(t, db.ctx, db.migration, f.account)
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(db.ctx, `SET LOCAL session_replication_role='replica'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(db.ctx, query, args...); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(db.ctx); err != nil {
		t.Fatal(err)
	}
}

func bootstrapCurrentDigest(t *testing.T, db testDatabases, f bootstrapFixture) []byte {
	t.Helper()
	tx := beginAccount(t, db.ctx, db.migration, f.account)
	defer tx.Rollback(db.ctx)
	rows, err := tx.Query(db.ctx, `SELECT sequence,recorded_at,latitude,longitude,altitude,speed,course,horizontal_accuracy,vertical_accuracy,speed_accuracy,course_accuracy FROM app.workout_route_points WHERE account_id=$1 AND workout_id=$2 ORDER BY sequence`, f.account, f.workouts[0])
	if err != nil {
		t.Fatal(err)
	}
	var points []bootstrapCanonicalPoint
	for rows.Next() {
		var p bootstrapCanonicalPoint
		if err := rows.Scan(&p.sequence, &p.timestamp, &p.latitude, &p.longitude, &p.optional[0], &p.optional[1], &p.optional[2], &p.optional[3], &p.optional[4], &p.optional[5], &p.optional[6]); err != nil {
			t.Fatal(err)
		}
		points = append(points, p)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return bootstrapCanonicalDigest(points)
}

func bootstrapSQLState(t *testing.T, err error, want string) {
	t.Helper()
	var sqlErr *pgconn.PgError
	if !errors.As(err, &sqlErr) || sqlErr.Code != want {
		t.Fatalf("SQLSTATE=%v, want %s", err, want)
	}
}

func bootstrapAssertState(t *testing.T, db testDatabases, f bootstrapFixture, revision int64, digest []byte) {
	t.Helper()
	tx := beginAccount(t, db.ctx, db.migration, f.account)
	defer tx.Rollback(db.ctx)
	var storedRevision int64
	var storedDigest []byte
	err := tx.QueryRow(db.ctx, `SELECT route_input_revision,route_input_sha256 FROM app.workout_coverage_states WHERE account_id=$1 AND workout_id=$2`, f.account, f.workouts[0]).Scan(&storedRevision, &storedDigest)
	if revision == 0 {
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("missing state was initialized: %v", err)
		}
	} else if err != nil || storedRevision != revision || !bytes.Equal(storedDigest, digest) {
		t.Fatalf("state changed: revision=%d digest=%x err=%v", storedRevision, storedDigest, err)
	}
	var cursor *uuid.UUID
	if err := tx.QueryRow(db.ctx, `SELECT cursor_workout_id FROM app.coverage_reconciliation_accounts WHERE account_id=$1`, f.account).Scan(&cursor); err != nil || cursor != nil {
		t.Fatalf("repair advanced cursor: %v err=%v", cursor, err)
	}
}

func bootstrapDelayTrigger(t *testing.T, db testDatabases, f bootstrapFixture, timing, seconds string, conditional bool) {
	t.Helper()
	condition := "TRUE"
	if conditional {
		condition = "current_setting('coverage_bootstrap.test_delay',true)='yes'"
	}
	bootstrapExec(t, db, f, `CREATE FUNCTION app.coverage_bootstrap_delay_test() RETURNS trigger LANGUAGE plpgsql AS $f$
		BEGIN IF NEW.workout_id::text=TG_ARGV[0] AND `+condition+` THEN PERFORM pg_sleep(`+seconds+`); END IF; RETURN NEW; END; $f$;
		CREATE TRIGGER coverage_bootstrap_delay_test `+timing+` INSERT OR UPDATE ON app.workout_coverage_states
		FOR EACH ROW EXECUTE FUNCTION app.coverage_bootstrap_delay_test('`+f.workouts[0].String()+`')`)
	t.Cleanup(func() {
		_, _ = db.migration.Exec(context.Background(), `DROP TRIGGER coverage_bootstrap_delay_test ON app.workout_coverage_states; DROP FUNCTION app.coverage_bootstrap_delay_test()`)
	})
}

func bootstrapWaitEvent(t *testing.T, ctx context.Context, admin *pgx.Conn, pid int, event string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		var waiting bool
		if err := admin.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND wait_event=$2)`, pid, event).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("backend did not reach %s barrier", event)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
