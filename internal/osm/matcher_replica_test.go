package osm

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestClassifyMatcherReplicaGenerations(t *testing.T) {
	targets := []MatcherGeneration{{RegionID: "fixture:private-a", GenerationID: 20}, {RegionID: "fixture:private-b", GenerationID: 30}}
	for _, test := range []struct {
		name     string
		active   map[string]int64
		replicas bool
		want     error
	}{
		{"equal", map[string]int64{"fixture:private-a": 20, "fixture:private-b": 30}, true, nil},
		{"lower", map[string]int64{"fixture:private-a": 19, "fixture:private-b": 30}, true, ErrMatcherReplicaNotReady},
		{"missing", map[string]int64{"fixture:private-a": 20}, true, ErrMatcherReplicaNotReady},
		{"all missing", nil, true, ErrMatcherReplicaNotReady},
		{"newer", map[string]int64{"fixture:private-a": 21, "fixture:private-b": 30}, true, ErrMatcherGenerationChanged},
		{"mixed ahead behind", map[string]int64{"fixture:private-a": 21, "fixture:private-b": 29}, true, ErrMatcherReplicaNotReady},
		{"mixed behind ahead", map[string]int64{"fixture:private-a": 19, "fixture:private-b": 31}, true, ErrMatcherReplicaNotReady},
		{"mixed ahead missing", map[string]int64{"fixture:private-a": 21}, true, ErrMatcherReplicaNotReady},
		{"unrelated newer", map[string]int64{"fixture:private-a": 20, "fixture:private-b": 30, "fixture:other": 100}, true, nil},
		{"default lower", map[string]int64{"fixture:private-a": 19, "fixture:private-b": 30}, false, ErrMatcherGenerationChanged},
		{"default missing", nil, false, ErrMatcherGenerationChanged},
		{"default equal", map[string]int64{"fixture:private-a": 20, "fixture:private-b": 30}, false, nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := classifyMatcherGenerations(targets, test.active, test.replicas); !errors.Is(got, test.want) {
				t.Fatalf("error = %v, want %v", got, test.want)
			}
		})
	}
	if ErrMatcherReplicaNotReady.Error() != "OSM matcher replica is not ready" || errors.Is(ErrMatcherReplicaNotReady, ErrMatcherGenerationChanged) {
		t.Fatal("replica lag must have a distinct, static, private-data-free error")
	}
}

func TestMatcherReplicaLagDiscardsPhysicalConnection(t *testing.T) {
	for _, test := range []struct {
		name     string
		first    int64
		replicas bool
		want     error
		discard  bool
	}{
		{"lower head", 1, true, ErrMatcherReplicaNotReady, true},
		{"missing head", 0, true, ErrMatcherReplicaNotReady, true},
		{"newer head", 3, true, ErrMatcherGenerationChanged, false},
		{"default lower head", 1, false, ErrMatcherGenerationChanged, false},
		{"valid head", 2, true, nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			pool, connected := matcherReplicaTestPool(t, test.first)
			defer pool.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			targets := []MatcherGeneration{{RegionID: "fixture:region", GenerationID: 2}}
			var snapshot *MatcherSnapshot
			var err error
			if test.replicas {
				snapshot, err = BeginMatcherSnapshotForGenerationsOnReader(ctx, pool, targets, true)
			} else {
				snapshot, err = BeginMatcherSnapshotForGenerations(ctx, pool, targets)
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("snapshot error=%v, want %v", err, test.want)
			}
			original := <-connected
			if original.IsClosed() != test.discard {
				t.Fatalf("physical connection closed=%v, want %v", original.IsClosed(), test.discard)
			}
			closeMatcherSnapshot(ctx, snapshot)
			if test.discard {
				// MaxConns=1 and fixed first-node routing reproduce the LIFO
				// trap: only discarding the held connection can reach node two.
				fresh, err := BeginMatcherSnapshotForGenerationsOnReader(ctx, pool, targets, true)
				if err != nil {
					t.Fatalf("fresh replica snapshot: %v", err)
				}
				defer closeMatcherSnapshot(ctx, fresh)
				if fresh.tx.Conn() == original || fresh.tx.Conn().IsClosed() {
					t.Fatal("fresh snapshot reused the lagged physical connection")
				}
			} else {
				conn, err := pool.Acquire(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Release()
				if conn.Conn() != original {
					t.Fatal("normal rejection or valid snapshot discarded its connection")
				}
			}
		})
	}
}

type matcherDiscardOrderTx struct {
	pgx.Tx
	beforeRollback func(context.Context)
}

func (tx matcherDiscardOrderTx) Rollback(ctx context.Context) error {
	tx.beforeRollback(ctx)
	return tx.Tx.Rollback(ctx)
}

func TestDiscardReplicaSnapshotClosesWhileHeldAfterCancellation(t *testing.T) {
	pool, _ := matcherReplicaTestPool(t, 1)
	defer pool.Close()
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	checked := false
	discardReplicaSnapshot(ctx, matcherDiscardOrderTx{Tx: tx, beforeRollback: func(cleanup context.Context) {
		checked = true
		if !tx.Conn().IsClosed() || pool.Stat().AcquiredConns() != 1 {
			t.Error("physical connection was not closed while still checked out")
		}
		deadline, bounded := cleanup.Deadline()
		if cleanup.Err() != nil || !bounded || time.Until(deadline) > 3*time.Second {
			t.Error("discard cleanup did not detach cancellation with a three-second bound")
		}
	}})
	if !checked {
		t.Fatal("discard did not roll back to release the pool resource")
	}
	acquireCtx, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	conn, err := pool.Acquire(acquireCtx)
	if err != nil {
		t.Fatalf("discard leaked the sole pool slot: %v", err)
	}
	conn.Release()
}

// Two in-memory PostgreSQL wire nodes: the first session has the supplied head,
// and every new session is caught up. No DNS, sockets or external DB are used.
func matcherReplicaTestPool(t *testing.T, firstGeneration int64) (*pgxpool.Pool, <-chan *pgx.Conn) {
	t.Helper()
	config, err := pgxpool.ParseConfig("postgresql://fixture@127.0.0.1/fixture?sslmode=disable&default_query_exec_mode=simple_protocol")
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	connected := make(chan *pgx.Conn, 8)
	config.AfterConnect = func(_ context.Context, conn *pgx.Conn) error { connected <- conn; return nil }
	var dials atomic.Int32
	var mu sync.Mutex
	var servers []net.Conn
	var wg sync.WaitGroup
	config.ConnConfig.DialFunc = func(context.Context, string, string) (net.Conn, error) {
		client, server := net.Pipe()
		generation := int64(2)
		if dials.Add(1) == 1 {
			generation = firstGeneration
		}
		mu.Lock()
		servers = append(servers, server)
		mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
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
			for {
				message, err := backend.Receive()
				if err != nil {
					return
				}
				query, ok := message.(*pgproto3.Query)
				if !ok {
					return
				}
				lower := strings.ToLower(query.String)
				switch {
				case strings.HasPrefix(lower, "begin"):
					status = 'T'
					backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("BEGIN")})
				case strings.HasPrefix(lower, "rollback"):
					status = 'I'
					backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("ROLLBACK")})
				case strings.Contains(lower, "osm_catalog.generations"):
					fields := []pgproto3.FieldDescription{
						{Name: []byte("region_id"), DataTypeOID: 25, DataTypeSize: -1},
						{Name: []byte("id"), DataTypeOID: 20, DataTypeSize: 8},
					}
					values := [][]byte{[]byte("fixture:region"), []byte(strconv.FormatInt(generation, 10))}
					if strings.Contains(lower, "source_url") {
						for _, oid := range []uint32{25, 25, 1184, 23, 23, 1184} {
							fields = append(fields, pgproto3.FieldDescription{DataTypeOID: oid, DataTypeSize: -1})
						}
						values = append(values, []byte("https://example.test/fixture.pbf"), []byte(strings.Repeat("0", 64)), nil,
							[]byte("1"), []byte("1"), []byte("2026-10-13 00:00:00+00"))
					}
					backend.Send(&pgproto3.RowDescription{Fields: fields})
					if generation > 0 {
						backend.Send(&pgproto3.DataRow{Values: values})
					}
					backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
				default:
					backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
				}
				backend.Send(&pgproto3.ReadyForQuery{TxStatus: status})
				if backend.Flush() != nil {
					return
				}
			}
		}()
		return client, nil
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		mu.Lock()
		for _, server := range servers {
			_ = server.Close()
		}
		mu.Unlock()
		wg.Wait()
	})
	return pool, connected
}
