package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/coverage/readerplacement"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Use real app SQL/RLS/leases with two synthetic, directly pinned OSM nodes.
// No network database, credential or live reader configuration is synthesized.
func TestCoverageConcurrentRunOnceDifferentAccountsIntegration(t *testing.T) {
	f := newCoverageIntegrationFixture(t)
	f.seedOSM()
	g := coverageConcurrentAccount(f)
	var global, account, region, readerLimit int
	if err := f.adminDB.QueryRow(f.ctx, `SELECT global_limit,account_limit,region_limit,reader_limit FROM app.matcher_slot_limits WHERE singleton`).Scan(
		&global, &account, &region, &readerLimit); err != nil {
		t.Fatal(err)
	}
	if _, err := f.adminDB.Exec(f.ctx, `UPDATE app.matcher_slot_limits SET global_limit=2,account_limit=1,region_limit=2,reader_limit=1 WHERE singleton`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if _, err := f.adminDB.Exec(ctx, `UPDATE app.matcher_slot_limits SET global_limit=$1,account_limit=$2,region_limit=$3,reader_limit=$4 WHERE singleton`,
			global, account, region, readerLimit); err != nil {
			t.Error(err)
		}
	})
	w1, w2 := f.addWorkout(coverageIntegrationRoute(0)), g.addWorkout(coverageIntegrationRoute(1))
	j1, j2 := f.queue(w1), g.queue(w2)
	pool, blocked, release, active := coverageConcurrentReaderPool(t, f.regionID, f.generation, nil)
	placement, err := readerplacement.New(pool, true, 2)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(placement.Close)
	runner := f.runner()
	runner.reader = placement
	m, metrics := coverageTestMetrics(t)
	runner.metrics = m
	first, second := f.start(runner), f.start(runner)
	t.Cleanup(release)
	for range 2 {
		select {
		case <-blocked:
		case <-time.After(10 * time.Second):
			t.Fatal("two RunOnce calls did not reach matching on different readers")
		}
	}
	for i := range active {
		if got := active[i].Load(); got != 1 {
			t.Fatalf("reader %d has %d active private route connections, want 1", i, got)
		}
	}
	var fenced bool
	if err := f.adminDB.QueryRow(f.ctx, `SELECT count(*)=2 AND count(DISTINCT s.slot_token)=2
		AND count(DISTINCT s.lease_token)=2 AND count(DISTINCT s.account_id)=2
		AND count(DISTINCT s.reader_backend_key)=2 AND count(DISTINCT s.reader_cluster_key)=1
		AND bool_and(s.owner_id=$3 AND j.worker_id=$3 AND s.lease_token=j.lease_token AND s.expires_at=j.lease_expires_at)
		FROM app.matcher_slots s JOIN app.jobs j ON j.id=s.job_id WHERE s.job_id IN ($1,$2)`,
		j1.childID, j2.childID, runner.workerID).Scan(&fenced); err != nil {
		t.Fatal(err)
	}
	if !fenced {
		t.Fatal("overlapping routes did not retain independent fenced leases, slots and reader assignments")
	}
	assertCoverageIntegrationCounter(t, collectCoverageMetrics(t, metrics), "coverage.route.inflight", nil, 2)
	release()
	f.await(first)
	f.await(second)
	f.assertTerminal(j1, "succeeded", "no_evidence")
	g.assertTerminal(j2, "succeeded", "no_evidence")
	f.assertCurrent(w1, j1, -1, 1)
	g.assertCurrent(w2, j2, -1, 1)
	assertCoverageIntegrationCounter(t, collectCoverageMetrics(t, metrics), "coverage.route.inflight", nil, 0)
	for i := range active {
		if active[i].Load() != 0 {
			t.Fatal("completed RunOnce retained a physical reader connection")
		}
	}
}

func TestCoveragePinnedReaderFixtureConcurrentSnapshots(t *testing.T) {
	pool, _, _, active := coverageConcurrentReaderPool(t, "fixture:region", 2, nil)
	manager, err := readerplacement.New(pool, true, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var mu sync.Mutex
	counts := make(map[string]int)
	var snapshots [2]*osm.MatcherSnapshot
	errorsCh := make(chan error, 2)
	var workers sync.WaitGroup
	for i := range snapshots {
		workers.Go(func() {
			var chosen string
			reserve := func(_ context.Context, cluster string, candidates []string) (string, error) {
				mu.Lock()
				defer mu.Unlock()
				if len(cluster) != 64 || len(candidates) != 2 {
					return "", fmt.Errorf("unexpected opaque reader identities")
				}
				for _, key := range candidates {
					if counts[key] == 0 {
						counts[key]++
						chosen = key
						return key, nil
					}
				}
				return "", osm.ErrMatcherReplicaNotReady
			}
			clear := func(context.Context) error {
				mu.Lock()
				defer mu.Unlock()
				delete(counts, chosen)
				return nil
			}
			var err error
			snapshots[i], err = manager.Begin(ctx, []osm.MatcherGeneration{{RegionID: "fixture:region", GenerationID: 2}}, reserve, clear)
			errorsCh <- err
		})
	}
	workers.Wait()
	for range snapshots {
		if err := <-errorsCh; err != nil {
			t.Fatal(err)
		}
	}
	for i := range active {
		if active[i].Load() != 1 {
			t.Fatalf("node %d active=%d, want 1", i, active[i].Load())
		}
	}
	for _, snapshot := range snapshots {
		if err := snapshot.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}
	for i := range active {
		if active[i].Load() != 0 {
			t.Fatalf("node %d retained a closed snapshot socket", i)
		}
	}
}

func TestCoveragePinnedReaderFixtureUnconfirmedClose(t *testing.T) {
	for _, failedBegin := range []bool{false, true} {
		t.Run(fmt.Sprint("failed_begin_", failedBegin), func(t *testing.T) {
			faults := &coverageReaderFaults{invalidMetadata: failedBegin}
			faults.failClose.Store(true)
			pool, _, _, active := coverageConcurrentReaderPool(t, "fixture:region", 2, faults)
			manager, err := readerplacement.New(pool, true, 1)
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var clears atomic.Int32
			snapshot, err := manager.Begin(ctx, []osm.MatcherGeneration{{RegionID: "fixture:region", GenerationID: 2}},
				func(_ context.Context, _ string, keys []string) (string, error) { return keys[0], nil },
				func(context.Context) error { clears.Add(1); return nil })
			if failedBegin {
				if !errors.Is(err, readerplacement.ErrCleanupUnconfirmed) || clears.Load() != 0 {
					t.Fatalf("failed Begin error=%v clears=%d", err, clears.Load())
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				_ = snapshot.Close(ctx)
				if snapshot.PhysicalConnectionClosed() {
					t.Fatal("failed socket close was reported as physically confirmed")
				}
			}
			if active[0].Load()+active[1].Load() != 1 {
				t.Fatal("unconfirmed close did not retain its live physical socket")
			}
		})
	}
}

func TestCoverageRunOncePhysicalCleanupIntegration(t *testing.T) {
	for _, phase := range []string{"persisted_close_failure", "begin_close_failure", "rollback_error_closed", "cancelled_close_failure"} {
		t.Run(phase, func(t *testing.T) {
			f := newCoverageIntegrationFixture(t)
			f.seedOSM()
			job := f.queue(f.addWorkout(coverageIntegrationRoute(0)))
			faults := &coverageReaderFaults{invalidMetadata: phase == "begin_close_failure", rollbackError: phase == "rollback_error_closed"}
			faults.failClose.Store(phase != "rollback_error_closed")
			pool, blocked, release, active := coverageConcurrentReaderPool(t, f.regionID, f.generation, faults)
			manager, err := readerplacement.New(pool, true, 1)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(manager.Close)
			runner := f.runner()
			runner.reader = manager
			var logs bytes.Buffer
			runner.logger = slog.New(slog.NewTextHandler(&logs, nil))
			ctx, cancel := context.WithCancel(f.ctx)
			defer cancel()
			done := make(chan coverageIntegrationRunResult, 1)
			go func() {
				worked, err := runner.RunOnce(ctx)
				done <- coverageIntegrationRunResult{worked, err}
				close(done)
			}()
			t.Cleanup(func() {
				cancel()
				release()
				select {
				case <-done:
				case <-time.After(3 * cleanupTimeout):
					t.Error("cleanup-failure RunOnce did not stop")
				}
			})
			if phase != "begin_close_failure" {
				select {
				case <-blocked:
				case <-time.After(10 * time.Second):
					t.Fatal("matching did not start")
				}
				if phase == "cancelled_close_failure" {
					cancel()
				}
				release()
			}
			select {
			case result := <-done:
				if !result.worked {
					t.Fatal("fixture route was not claimed")
				}
				if phase == "rollback_error_closed" {
					if result.err != nil {
						t.Fatalf("confirmed physical cleanup failed: %v", result.err)
					}
				} else if !errors.Is(result.err, readerplacement.ErrCleanupUnconfirmed) || strings.Contains(result.err.Error(), "canary") {
					t.Fatalf("cleanup error=%v, want safe unconfirmed sentinel", result.err)
				}
			case <-time.After(15 * time.Second):
				t.Fatal("RunOnce did not return after unconfirmed physical cleanup")
			}
			if strings.Contains(logs.String(), "canary") {
				t.Fatal("cleanup logging leaked private socket/rollback fields")
			}
			if phase == "rollback_error_closed" {
				f.assertTerminal(job, "succeeded", "no_evidence")
				if active[0].Load()+active[1].Load() != 0 || !strings.Contains(logs.String(), "after physical close") {
					t.Fatal("rollback error did not close the socket and produce a safe warning")
				}
				return
			}
			var retained bool
			if err := f.adminDB.QueryRow(f.ctx, `SELECT count(*)=1 AND bool_and(reader_backend_key IS NOT NULL
				AND reader_cluster_key IS NOT NULL AND expires_at>clock_timestamp()) FROM app.matcher_slots WHERE job_id=$1`, job.childID).Scan(&retained); err != nil {
				t.Fatal(err)
			}
			if !retained || active[0].Load()+active[1].Load() != 1 {
				t.Fatal("unconfirmed close freed admission, assignment or physical route capacity")
			}
			if phase == "persisted_close_failure" {
				var terminal bool
				if err := f.adminDB.QueryRow(f.ctx, `SELECT j.status='succeeded' AND c.result_outcome='no_evidence'
					FROM app.jobs j JOIN app.coverage_route_job_contexts c ON c.job_id=j.id WHERE j.id=$1`, job.childID).Scan(&terminal); err != nil || !terminal {
					t.Fatalf("durable outcome was not preserved: terminal=%v error=%v", terminal, err)
				}
			}
			future := f.queue(f.addWorkout(coverageIntegrationRoute(1)))
			waitCtx, stop := context.WithTimeout(f.ctx, time.Second)
			worked, err := runner.RunOnce(waitCtx)
			stop()
			if !worked || err != nil && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("future admission wait worked=%v error=%v", worked, err)
			}
			var denied bool
			if err := f.adminDB.QueryRow(f.ctx, `SELECT count(*)=1 AND bool_and(job_id=$1) FROM app.matcher_slots WHERE account_id=$2`, job.childID, f.accountID).Scan(&denied); err != nil || !denied {
				t.Fatalf("future job %s took uncertain capacity: retained=%v error=%v", future.childID, denied, err)
			}
			// Manager's sole local permit is retained too, independently of SQL
			// admission. A future Begin must not reach its allocator callback.
			beginCtx, stop := context.WithTimeout(f.ctx, 50*time.Millisecond)
			defer stop()
			_, err = manager.Begin(beginCtx, []osm.MatcherGeneration{{RegionID: f.regionID, GenerationID: f.generation}},
				func(context.Context, string, []string) (string, error) {
					t.Error("unconfirmed socket freed the local reader permit")
					return "", nil
				}, func(context.Context) error { return nil })
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("local capacity wait error=%v", err)
			}
		})
	}
}

// Add another account under the fixture's existing process-wide scratch lock.
func coverageConcurrentAccount(f *coverageIntegrationFixture) *coverageIntegrationFixture {
	f.t.Helper()
	g := *f
	g.accountID, g.sourceID, g.fileID, g.typeID, g.ingestID, g.ingestLease = uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	tx := g.accountTx(g.ctx, g.adminDB)
	defer coverageIntegrationRollback(tx)
	g.exec(tx, `INSERT INTO app.accounts(id) VALUES($1)`, g.accountID)
	g.exec(tx, `INSERT INTO app.sources(id,account_id,display_name,canonical_display_name,type,config_envelope,status)
		VALUES($1,$2,$3,$3,'health-auto-export-local',$4,'connected')`, g.sourceID, g.accountID, "coverage-"+g.sourceID.String(), []byte("fixture"))
	parent := uuid.New()
	g.exec(tx, `INSERT INTO app.jobs(id,account_id,kind,priority) VALUES($1,$2,'manual_ingest',80)`, parent, g.accountID)
	parameters := fmt.Sprintf(`{"sourceId":"%s","generation":1,"mode":"incremental"}`, strings.ToUpper(strings.ReplaceAll(g.sourceID.String(), "-", "")))
	g.exec(tx, `INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority,parameters) VALUES($1,$2,$3,'manual_ingest_source',80,$4)`, g.ingestID, parent, g.accountID, parameters)
	g.exec(tx, `INSERT INTO app.job_config_snapshots(job_id,account_id,source_id,source_generation,config_envelope) VALUES($1,$2,$3,1,$4)`, g.ingestID, g.accountID, g.sourceID, []byte("fixture"))
	g.exec(tx, `INSERT INTO app.job_source_contexts(job_id,account_id,source_id,source_generation,display_name,source_type) VALUES($1,$2,$3,1,'Coverage fixture','health-auto-export-local')`, g.ingestID, g.accountID, g.sourceID)
	g.commit(tx)
	tx = g.accountTx(g.ctx, g.workerDB)
	var claimed bool
	g.scan(tx, `SELECT app.claim_job($1,'coverage-fixture-ingest',$2,interval '15 minutes')`, []any{g.ingestID, g.ingestLease}, &claimed)
	if !claimed {
		g.t.Fatal("second account ingest fixture claim rejected")
	}
	g.fence(tx)
	hash := sha256.Sum256([]byte(g.sourceID.String()))
	g.exec(tx, `INSERT INTO app.source_files(id,account_id,source_id,job_id,relative_name,size_bytes,checksum_sha256,state,processing_started_at,processed_at)
		VALUES($1,$2,$3,$4,'coverage-fixture.json',1,$5,'succeeded',transaction_timestamp(),transaction_timestamp())`, g.fileID, g.accountID, g.sourceID, g.ingestID, hash[:])
	g.exec(tx, `INSERT INTO app.workout_types(id,account_id,type_key,provider_label) VALUES($1,$2,'running','Running')`, g.typeID, g.accountID)
	g.commit(tx)
	g.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		tx := g.accountTx(ctx, g.adminDB)
		defer coverageIntegrationRollback(tx)
		g.fence(tx)
		for _, query := range []string{`DELETE FROM app.workouts WHERE account_id=$1`, `DELETE FROM app.source_files WHERE account_id=$1`, `DELETE FROM app.workout_types WHERE account_id=$1`} {
			g.exec(tx, query, g.accountID)
		}
		g.commit(tx)
		tx = g.accountTx(ctx, g.adminDB)
		defer coverageIntegrationRollback(tx)
		g.exec(tx, `DELETE FROM app.jobs WHERE account_id=$1`, g.accountID)
		g.exec(tx, `DELETE FROM app.accounts WHERE id=$1`, g.accountID)
		g.commit(tx)
	})
	return &g
}

type coverageRouteSocket struct {
	net.Conn
	once   sync.Once
	route  *atomic.Bool
	active *atomic.Int32
	faults *coverageReaderFaults
	closed chan struct{}
}

type coverageReaderFaults struct {
	failClose       atomic.Bool
	invalidMetadata bool
	rollbackError   bool
}

func (c *coverageRouteSocket) Close() error {
	if c.faults != nil && c.route.Load() && c.faults.failClose.Load() {
		return errors.New("private-reader-socket-canary close failed")
	}
	err := c.Conn.Close()
	c.once.Do(func() {
		close(c.closed)
		if c.route.Load() {
			c.active.Add(-1)
		}
	})
	return err
}

func coverageConcurrentReaderPool(t *testing.T, region string, generation int64, faults *coverageReaderFaults) (*pgxpool.Pool, <-chan struct{}, func(), []*atomic.Int32) {
	t.Helper()
	config, err := pgxpool.ParseConfig("postgresql://fixture@127.0.0.2,127.0.0.3/fixture?sslmode=disable&default_query_exec_mode=simple_protocol")
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.LookupFunc = func(_ context.Context, host string) ([]string, error) {
		return []string{host}, nil
	}
	active := []*atomic.Int32{new(atomic.Int32), new(atomic.Int32)}
	blocked, gate := make(chan struct{}, 2), make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate) }) }
	var mu sync.Mutex
	var sockets []net.Conn
	var servers sync.WaitGroup
	config.ConnConfig.DialFunc = func(_ context.Context, _, address string) (net.Conn, error) {
		node := 0
		if strings.HasPrefix(address, "127.0.0.3:") {
			node = 1
		}
		client, server := net.Pipe()
		var route atomic.Bool
		socket := &coverageRouteSocket{Conn: client, route: &route, active: active[node], faults: faults, closed: make(chan struct{})}
		mu.Lock()
		sockets = append(sockets, socket, server)
		mu.Unlock()
		servers.Go(func() {
			defer server.Close()
			backend := pgproto3.NewBackend(server, server)
			if _, err := backend.ReceiveStartupMessage(); err != nil {
				return
			}
			backend.Send(&pgproto3.AuthenticationOk{})
			backend.Send(&pgproto3.ParameterStatus{Name: "server_version", Value: "17.0"})
			backend.Send(&pgproto3.ParameterStatus{Name: "client_encoding", Value: "UTF8"})
			backend.Send(&pgproto3.ParameterStatus{Name: "standard_conforming_strings", Value: "on"})
			backend.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
			if backend.Flush() != nil {
				return
			}
			status := byte('I')
			query := ""
			respond := func() {
				lower := strings.ToLower(query)
				var fields []pgproto3.FieldDescription
				var values [][]byte
				command := "SELECT 0"
				switch {
				case strings.HasPrefix(lower, "begin"):
					status, command = 'T', "BEGIN"
				case strings.HasPrefix(lower, "rollback"):
					if faults != nil && faults.rollbackError && route.Load() {
						backend.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "XX000", Message: "private-reader-rollback-canary"})
						return
					}
					status, command = 'I', "ROLLBACK"
				case strings.Contains(lower, "pg_is_in_recovery"):
					fields = []pgproto3.FieldDescription{{DataTypeOID: 16, DataTypeSize: 1}}
					values = [][]byte{[]byte("t")}
				case strings.Contains(lower, "from osm_catalog.generations where"):
					fields = []pgproto3.FieldDescription{{DataTypeOID: 25, DataTypeSize: -1}, {DataTypeOID: 20, DataTypeSize: 8}}
					values = [][]byte{[]byte(region), []byte(strconv.FormatInt(generation, 10))}
					if strings.Contains(lower, "source_url") {
						if route.CompareAndSwap(false, true) {
							active[node].Add(1)
						}
						for _, oid := range []uint32{25, 25, 1184, 23, 23, 1184} {
							fields = append(fields, pgproto3.FieldDescription{DataTypeOID: oid, DataTypeSize: -1})
						}
						values = append(values, []byte("https://example.test/fixture.pbf"), []byte(strings.Repeat("0", 64)), nil, []byte("1"), []byte("1"), []byte("2026-10-13 00:00:00+00"))
						if faults != nil && faults.invalidMetadata {
							values[3] = nil
						}
					}
				case strings.Contains(lower, "candidate.segment_id"):
					blocked <- struct{}{}
					<-gate
				}
				if strings.HasPrefix(command, "SELECT") {
					backend.Send(&pgproto3.RowDescription{Fields: fields})
				}
				if values != nil {
					backend.Send(&pgproto3.DataRow{Values: values})
					command = "SELECT 1"
				}
				backend.Send(&pgproto3.CommandComplete{CommandTag: []byte(command)})
			}
			for {
				message, err := backend.Receive()
				if err != nil {
					return
				}
				flush := false
				switch message := message.(type) {
				case *pgproto3.Query:
					query = message.String
					respond()
					backend.Send(&pgproto3.ReadyForQuery{TxStatus: status})
					flush = true
				case *pgproto3.Parse:
					query = message.Query
					backend.Send(&pgproto3.ParseComplete{})
				case *pgproto3.Bind:
					backend.Send(&pgproto3.BindComplete{})
				case *pgproto3.Execute:
					respond()
				case *pgproto3.Describe:
				case *pgproto3.Sync:
					backend.Send(&pgproto3.ReadyForQuery{TxStatus: status})
					flush = true
				case *pgproto3.Terminate:
					<-socket.closed
					return
				default:
					return
				}
				if flush && backend.Flush() != nil {
					return
				}
			}
		})
		return socket, nil
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		release()
		if faults != nil {
			faults.failClose.Store(false)
		}
		pool.Close()
		mu.Lock()
		for _, socket := range sockets {
			_ = socket.Close()
		}
		mu.Unlock()
		servers.Wait()
	})
	return pool, blocked, release, active
}
