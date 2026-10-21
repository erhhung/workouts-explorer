package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/coverage/readerplacement"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// These tests simulate differing replica heads only in the explicitly disposable
// fixture. The production matcher and runner still use the read-only role.
func replicaFixtureHead(f *coverageIntegrationFixture, generation int64, missing bool) {
	f.t.Helper()
	tx, err := f.osmAdminDB.Begin(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	defer coverageIntegrationRollback(tx)
	f.exec(tx, `UPDATE osm_catalog.generations SET state='retired',retired_at=clock_timestamp() WHERE region_id=$1 AND state='active'`, f.regionID)
	if !missing {
		f.exec(tx, `INSERT INTO osm_catalog.generations(id,region_id,state,schema_name,source_url,source_sha256,importer_version,derivation_version,promoted_at)
			OVERRIDING SYSTEM VALUE SELECT $1,region_id,'active',$2,source_url,
			CASE WHEN $1<id THEN NULL ELSE source_sha256 END,importer_version,derivation_version,clock_timestamp()
			FROM osm_catalog.generations WHERE id=$3`, generation, fmt.Sprintf("osm_build_%d", generation), f.generation)
		f.exec(tx, `CREATE SCHEMA `+pgx.Identifier{fmt.Sprintf("osm_build_%d", generation)}.Sanitize())
	}
	f.commit(tx)
}

func replicaFixtureCatchUp(f *coverageIntegrationFixture) {
	f.t.Helper()
	tx, err := f.osmAdminDB.Begin(f.ctx)
	if err != nil {
		f.t.Fatal(err)
	}
	defer coverageIntegrationRollback(tx)
	f.exec(tx, `UPDATE osm_catalog.generations SET state='retired',retired_at=clock_timestamp() WHERE region_id=$1 AND state='active'`, f.regionID)
	f.exec(tx, `UPDATE osm_catalog.generations SET state='active',retired_at=NULL WHERE id=$1`, f.generation)
	f.commit(tx)
}

func replicaFixtureRunning(f *coverageIntegrationFixture, job coverageIntegrationJob, after time.Time) time.Time {
	f.t.Helper()
	var expiry time.Time
	f.wait("running replica wait with renewed job and slot", func(ctx context.Context) (bool, error) {
		tx, err := f.adminDB.Begin(ctx)
		if err != nil {
			return false, err
		}
		defer coverageIntegrationRollback(tx)
		if _, err = tx.Exec(ctx, `SELECT set_config('app.account_id',$1,true)`, f.accountID.String()); err != nil {
			return false, err
		}
		var running bool
		err = tx.QueryRow(ctx, `SELECT job.status='running' AND route.result_outcome IS NULL
			AND slot.expires_at=job.lease_expires_at AND slot.lease_token=job.lease_token,
			job.lease_expires_at FROM app.jobs job JOIN app.coverage_route_job_contexts route ON route.job_id=job.id
			JOIN app.matcher_slots slot ON slot.job_id=job.id WHERE job.id=$1`, job.childID).Scan(&running, &expiry)
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return running && (after.IsZero() || time.Now().After(after) && expiry.After(after)), err
	})
	return expiry
}

func TestCoverageReplicaLagCatchUpIntegration(t *testing.T) {
	for _, missing := range []bool{false, true} {
		name := "lower_head_incomplete_metadata"
		if missing {
			name = "missing_head"
		}
		t.Run(name, func(t *testing.T) {
			f := newCoverageIntegrationFixture(t)
			var older int64
			if err := f.osmAdminDB.QueryRow(f.ctx, `SELECT nextval(pg_get_serial_sequence('osm_catalog.generations','id'))`).Scan(&older); err != nil {
				t.Fatal(err)
			}
			f.seedOSM()
			workout := f.addWorkout(coverageIntegrationRoute(0))
			job := f.queue(workout)
			replicaFixtureHead(f, older, missing)
			runner := f.runner()
			runner.useReadReplicasOnly = true
			runner.routeTimeout = 2 * time.Second
			runner.leaseDuration = 3 * time.Second
			done := f.start(runner)
			initialExpiry := replicaFixtureRunning(f, job, time.Time{})
			// Wait longer than both the matcher budget and initial lease. No
			// terminal mutation is allowed, and heartbeats must renew the slot.
			replicaFixtureRunning(f, job, initialExpiry)
			if f.osmDB.Stat().AcquiredConns() != 0 {
				t.Fatal("lag wait retained a snapshot connection")
			}
			replicaFixtureCatchUp(f)
			f.await(done)
			f.assertTerminal(job, "succeeded", "applied")
			f.assertCurrent(workout, job, 0, 1)
		})
	}
}

func TestCoverageReplicaNewerHeadSupersedesIntegration(t *testing.T) {
	f := newCoverageIntegrationFixture(t)
	f.seedOSM()
	job := f.queue(f.addWorkout(coverageIntegrationRoute(0)))
	var newer int64
	if err := f.osmAdminDB.QueryRow(f.ctx, `SELECT nextval(pg_get_serial_sequence('osm_catalog.generations','id'))`).Scan(&newer); err != nil {
		t.Fatal(err)
	}
	replicaFixtureHead(f, newer, false)
	runner := f.runner()
	runner.useReadReplicasOnly = true
	f.await(f.start(runner))
	f.assertTerminal(job, "succeeded", "superseded")
}

func TestCoverageReplicaWaitInterruptionIntegration(t *testing.T) {
	for _, interruption := range []string{"cancel_job", "lose_lease", "shutdown", "parent_deadline"} {
		t.Run(interruption, func(t *testing.T) {
			f := newCoverageIntegrationFixture(t)
			f.seedOSM()
			workout := f.addWorkout(coverageIntegrationRoute(0))
			job := f.queue(workout)
			replicaFixtureHead(f, 0, true)
			runner := f.runner()
			runner.useReadReplicasOnly = true
			ctx, cancel := context.WithCancel(f.ctx)
			if interruption == "parent_deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(f.ctx, 3*time.Second)
			}
			defer cancel()
			done := make(chan coverageIntegrationRunResult, 1)
			go func() {
				worked, err := runner.RunOnce(ctx)
				done <- coverageIntegrationRunResult{worked, err}
				close(done)
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(3 * cleanupTimeout):
					t.Error("replica wait did not stop")
				}
			})
			replicaFixtureRunning(f, job, time.Time{})
			switch interruption {
			case "cancel_job":
				tx := f.accountTx(f.ctx, f.adminDB)
				var requested bool
				f.scan(tx, `SELECT app.request_coverage_job_cancellation($1,$2)`, []any{job.parentID, uuid.New()}, &requested)
				f.commit(tx)
				if !requested {
					t.Fatal("cancellation rejected")
				}
			case "lose_lease":
				f.recover(job)
			case "shutdown":
				cancel()
			}
			f.await(done)
			if interruption == "cancel_job" {
				f.assertTerminal(job, "cancelled", "")
				return
			}
			tx := f.accountTx(f.ctx, f.adminDB)
			var nonterminal bool
			f.scan(tx, `SELECT job.status IN ('queued','running') AND route.result_outcome IS NULL
				AND (SELECT count(*) FROM app.matcher_slots WHERE account_id=$2)=0
				FROM app.jobs job JOIN app.coverage_route_job_contexts route ON route.job_id=job.id WHERE job.id=$1`, []any{job.childID, f.accountID}, &nonterminal)
			f.commit(tx)
			if !nonterminal {
				t.Fatal("interrupted lag wait wrote a terminal outcome or leaked admission")
			}
			if interruption != "lose_lease" {
				f.recover(job)
			}
			f.assertNoSlots()
		})
	}
}

func TestCoverageReplicaAcquireRaceIntegration(t *testing.T) {
	for _, test := range []struct {
		name       string
		cause      error
		redact     bool
		fatal      bool
		atDeadline bool
	}{
		{"standbys_temporarily_unavailable", osm.ErrMatcherReplicaNotReady, false, false, false},
		{"permission_error_is_fatal", &pgconn.PgError{Code: "42501"}, false, true, false},
		{"sanitized_shutdown", &pgconn.PgError{Code: "57P01"}, true, false, false},
		{"sanitized_connection_failure", &pgconn.PgError{Code: "08006"}, true, false, false},
		{"sanitized_EOF", io.EOF, true, false, false},
		{"sanitized_network_failure", &net.OpError{Op: "dial", Err: errors.New("reader-private-detail-canary")}, true, false, false},
		{"sanitized_permission_is_fatal", &pgconn.PgError{Code: "42501"}, true, true, false},
		{"sanitized_authentication_is_fatal", &pgconn.PgError{Code: "28P01"}, true, true, false},
		{"sanitized_permission_at_attempt_deadline_is_fatal", &pgconn.PgError{Code: "42501"}, true, true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := newCoverageIntegrationFixture(t)
			f.seedOSM()
			workout := f.addWorkout(coverageIntegrationRoute(0))
			job := f.queue(workout)
			config := f.osmDB.Config()
			failure := test.cause
			if test.redact {
				failure = redactedCoverageReaderError(t, failure)
			}
			var calls atomic.Int32
			var ready atomic.Bool
			config.ConnConfig.ValidateConnect = func(ctx context.Context, _ *pgconn.PgConn) error {
				// Let preflight succeed, then model availability changing
				// before fresh placement connects. Its role checks report the
				// not-ready sentinel, not pgxpool's old static checkout error.
				if calls.Add(1) == 1 || ready.Load() {
					return nil
				}
				if test.atDeadline {
					<-ctx.Done()
				}
				return failure
			}
			reader, err := pgxpool.NewWithConfig(f.ctx, config)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(reader.Close)
			runner := f.runner()
			runner.useReadReplicasOnly, runner.osmDB = true, reader
			// The disposable primary models generation lag; the connection policy
			// above independently models a changing standby's availability.
			runner.reader, err = readerplacement.New(reader, false, 2)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(runner.reader.Close)
			done := f.start(runner)
			if test.fatal {
				f.await(done)
				f.assertTerminal(job, "failed", "")
				return
			}
			replicaFixtureRunning(f, job, time.Time{})
			// An empty healthy-node discovery is cached for ten seconds; prove
			// fresh connection retry beyond that cache, not a pool checkout retry.
			retryCtx, stop := context.WithTimeout(f.ctx, 25*time.Second)
			ticker := time.NewTicker(10 * time.Millisecond)
			for calls.Load() < 3 {
				select {
				case <-retryCtx.Done():
					ticker.Stop()
					stop()
					t.Fatal("fresh reader connection was not retried after discovery cache expiry")
				case <-ticker.C:
				}
			}
			ticker.Stop()
			stop()
			replicaFixtureRunning(f, job, time.Time{})
			ready.Store(true)
			f.await(done)
			f.assertTerminal(job, "succeeded", "applied")
			f.assertCurrent(workout, job, 0, 1)
		})
	}
}

func TestCoverageReplicaUnavailableBeforeClaimIntegration(t *testing.T) {
	f := newCoverageIntegrationFixture(t)
	f.seedOSM()
	job := f.queue(f.addWorkout(coverageIntegrationRoute(0)))
	runner := f.runner()
	runner.useReadReplicasOnly, runner.osmDB = true, nil
	worked, err := runner.RunOnce(f.ctx)
	if worked || err == nil || err.Error() != "coverage OSM reader preflight failed" {
		t.Fatalf("preflight worked=%v err=%v", worked, err)
	}
	tx := f.accountTx(f.ctx, f.apiDB)
	var untouched bool
	f.scan(tx, `SELECT job.status='queued' AND job.attempt=0 AND job.worker_id IS NULL AND route.result_outcome IS NULL
		FROM app.jobs job JOIN app.coverage_route_job_contexts route ON route.job_id=job.id WHERE job.id=$1`, []any{job.childID}, &untouched)
	f.commit(tx)
	if !untouched {
		t.Fatal("reader outage consumed a queued job")
	}
}

func TestCoverageReplicaSnapshotAttemptTimeoutIntegration(t *testing.T) {
	f := newCoverageIntegrationFixture(t)
	f.seedOSM()
	workout := f.addWorkout(coverageIntegrationRoute(0))
	job := f.queue(workout)
	gate := f.gate(f.osmAdminDB, "osm_catalog", "generations", "ACCESS EXCLUSIVE")
	runner := f.runner()
	runner.useReadReplicasOnly = true
	runner.routeTimeout = 2 * time.Second
	config := f.osmDB.Config()
	var connections atomic.Int32
	dial := config.ConnConfig.DialFunc
	config.ConnConfig.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		connections.Add(1)
		return dial(ctx, network, address)
	}
	reader, err := pgxpool.NewWithConfig(f.ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.Close)
	runner.reader, err = readerplacement.New(reader, false, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runner.reader.Close)
	done := f.start(runner)
	gate.waitBlocked(f.osmDB, "SELECT region_id,id FROM")
	initialConnections := connections.Load()
	// The first discovery is blocked. Another connection proves initialization
	// expired at its own bound and retried, not failed at the shorter matcher
	// budget or kept an unbounded blocked transaction.
	f.wait("fresh snapshot attempt after bounded initialization timeout", func(context.Context) (bool, error) {
		return connections.Load() > initialConnections, nil
	})
	replicaFixtureRunning(f, job, time.Time{})
	gate.release()
	f.await(done)
	f.assertTerminal(job, "succeeded", "applied")
	f.assertCurrent(workout, job, 0, 1)
}

func TestCoverageReplicaCaughtUpInvalidMetadataFailsIntegration(t *testing.T) {
	f := newCoverageIntegrationFixture(t)
	f.seedOSM()
	job := f.queue(f.addWorkout(coverageIntegrationRoute(0)))
	if _, err := f.osmAdminDB.Exec(f.ctx, `UPDATE osm_catalog.generations SET source_sha256=NULL WHERE id=$1`, f.generation); err != nil {
		t.Fatal(err)
	}
	runner := f.runner()
	runner.useReadReplicasOnly = true
	f.await(f.start(runner))
	f.assertTerminal(job, "failed", "")
}
