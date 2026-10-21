package readerplacement

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestPlacementSanitizeFatalPrecedence(t *testing.T) {
	denied := &pgconn.PgError{Code: "42501", Message: "private-password-canary"}
	auth := &pgconn.PgError{Code: "28P01", Message: "private-password-canary"}
	transient := &pgconn.PgError{Code: "08006", Message: "private-password-canary"}
	ordinary := errors.New("private-password-canary invalid digest")
	for _, test := range []struct {
		name      string
		err       error
		fatal     bool
		code      string
		unready   bool
		deadline  bool
		cancelled bool
	}{
		{name: "ordinary", err: ordinary, fatal: true},
		{name: "ordinary joined deadline", err: errors.Join(context.DeadlineExceeded, ordinary), fatal: true},
		{name: "denied joined deadline", err: errors.Join(context.DeadlineExceeded, denied), fatal: true, code: "42501"},
		{name: "auth joined lag", err: errors.Join(osm.ErrMatcherReplicaNotReady, auth), fatal: true, code: "28P01"},
		{name: "denied joined cancel", err: errors.Join(context.Canceled, denied), fatal: true, code: "42501"},
		{name: "transient before auth", err: errors.Join(transient, context.DeadlineExceeded, fmt.Errorf("wrapped: %w", auth)), fatal: true, code: "28P01"},
		{name: "auth before transient", err: errors.Join(auth, transient), fatal: true, code: "28P01"},
		{name: "network", err: &net.OpError{Op: "dial", Err: errors.New("private-password-canary")}, unready: true},
		{name: "EOF", err: fmt.Errorf("private-password-canary: %w", io.EOF), unready: true},
		{name: "unexpected EOF", err: io.ErrUnexpectedEOF, unready: true},
		{name: "transient SQL", err: transient, code: "08006"},
		{name: "lag", err: osm.ErrMatcherReplicaNotReady, unready: true},
		{name: "DNS missing", err: &net.DNSError{Err: "private-password-canary", IsNotFound: true}, unready: true},
		{name: "deadline", err: context.DeadlineExceeded, deadline: true},
		{name: "cancel ordinary", err: errors.Join(context.Canceled, ordinary), cancelled: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := sanitize(test.err)
			if permanent(err) != test.fatal || errorSQLState(err) != test.code ||
				errors.Is(err, osm.ErrMatcherReplicaNotReady) != test.unready ||
				errors.Is(err, context.DeadlineExceeded) != test.deadline || errors.Is(err, context.Canceled) != test.cancelled {
				t.Fatalf("classification changed: err=%v fatal=%v", err, permanent(err))
			}
			assertPrivate(t, err.Error())
		})
	}
}

func TestPlacementCaughtUpInvalidDigestIsFatal(t *testing.T) {
	for _, strict := range []bool{false, true} {
		t.Run(fmt.Sprint(strict), func(t *testing.T) {
			m, nodes, _ := fixtureManager(t, strict, 1)
			nodes[1].invalidDigest.Store(true)
			clears := 0
			snapshot, err := m.Begin(context.Background(), testTargets, func(_ context.Context, _ string, keys []string) (string, error) {
				for _, key := range keys {
					if key == nodes[1].key {
						return key, nil
					}
				}
				t.Error("caught-up replica not offered")
				return "", nil
			}, func(context.Context) error {
				clears++
				nodes[1].mu.Lock()
				defer nodes[1].mu.Unlock()
				for _, socket := range nodes[1].clients {
					if !socket.closed.Load() {
						t.Error("fatal metadata error cleared before physical close")
					}
				}
				return nil
			})
			if snapshot != nil || err == nil || !permanent(err) || errors.Is(err, osm.ErrMatcherReplicaNotReady) || clears != 1 || len(m.budget) != 0 {
				t.Fatalf("invalid digest became a retry: snapshot=%v err=%v clears=%d", snapshot, err, clears)
			}
			assertPrivate(t, err.Error())
		})
	}
}

func TestPlacementDeniedProbeWinsOverStalledProbeDeadline(t *testing.T) {
	for _, code := range []string{"42501", "28P01"} {
		t.Run(code, func(t *testing.T) {
			m, nodes, _ := fixtureManager(t, true, 1)
			endpoints, err := m.endpoints(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			var denied, stalled *wireNode
			for _, node := range nodes {
				node.standby.Store(true)
				if node.key == endpoints[0].key {
					denied = node
				}
				if node.key == endpoints[1].key {
					stalled = node
				}
			}
			started := make(chan struct{})
			denied.roleGate = started
			if code == "42501" {
				denied.deny.Store(true)
			} else {
				denied.startupState = code
			}
			dial := m.config.DialFunc
			m.config.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
				if address == stalled.address {
					close(started)
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return dial(ctx, network, address)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			_, err = m.Begin(ctx, testTargets, func(context.Context, string, []string) (string, error) {
				t.Error("denied discovery called allocator")
				return "", nil
			}, func(context.Context) error { t.Error("unreserved discovery cleared"); return nil })
			if errorSQLState(err) != code || !permanent(err) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, osm.ErrMatcherReplicaNotReady) {
				t.Fatalf("fatal probe was hidden by deadline: %v", err)
			}
			if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
				t.Fatal("fixture did not overlap a fatal probe and the parent deadline")
			}
			assertPrivate(t, err.Error())
		})
	}
}

func TestPlacementServiceSRVFailureNeverChangesIdentityScheme(t *testing.T) {
	for _, failure := range []string{"timeout", "NXDOMAIN", "NODATA", "unexpected"} {
		t.Run(failure, func(t *testing.T) {
			m, _, pool := fixtureManager(t, true, 1)
			other, err := New(pool, true, 1)
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			other.lookupSRV = func(context.Context, string, string, string) (string, []*net.SRV, error) {
				switch failure {
				case "timeout":
					return "", nil, &net.DNSError{Err: "private-password-canary", IsTimeout: true}
				case "NXDOMAIN":
					return "", nil, &net.DNSError{Err: "private-password-canary", IsNotFound: true}
				case "NODATA":
					return "", nil, nil
				default:
					return "", nil, errors.New("private-password-canary unexpected resolver failure")
				}
			}
			other.config.LookupFunc = func(context.Context, string) ([]string, error) {
				t.Error("SRV failure switched to A/AAAA identity")
				return nil, io.EOF
			}
			if m.clusterKey != other.clusterKey {
				t.Fatal("same configured service split cluster identity")
			}
			snapshot, err := m.Begin(context.Background(), testTargets, func(_ context.Context, _ string, keys []string) (string, error) {
				return keys[0], nil
			}, func(context.Context) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			defer snapshot.Close(context.Background())
			_, err = other.Begin(context.Background(), testTargets, func(context.Context, string, []string) (string, error) {
				t.Error("failed service SRV discovery allocated under a different key")
				return "", nil
			}, func(context.Context) error { t.Error("unreserved discovery cleared"); return nil })
			if err == nil || errors.Is(err, osm.ErrMatcherReplicaNotReady) != (failure != "unexpected") {
				t.Fatalf("SRV failure classification: %v", err)
			}
			assertPrivate(t, err.Error())
		})
	}
}

func TestPlacementGenericHostIdentityIgnoresSRVAndAddressFamilies(t *testing.T) {
	m, _, pool := fixtureManager(t, true, 1)
	other, err := New(pool, true, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	for i, manager := range []*Manager{m, other} {
		manager.config.Host = "Reader.private."
		manager.config.LookupFunc = func(context.Context, string) ([]string, error) {
			if i == 0 {
				return []string{"100::1", "192.0.2.2", "::ffff:192.0.2.2"}, nil
			}
			return []string{"192.0.2.2", "100::1"}, nil
		}
		manager.lookupSRV = func(context.Context, string, string, string) (string, []*net.SRV, error) {
			t.Error("generic host identity depended on SRV success/failure")
			return "", nil, nil
		}
		endpoints, err := manager.endpoints(context.Background())
		if err != nil || len(endpoints) != 1 || endpoints[0].key != hashIdentity("reader.private:5432") || len(endpoints[0].addresses) != 2 || endpoints[0].addresses[0] != "192.0.2.2" {
			t.Fatalf("generic host did not use one stable dual-stack key: %+v %v", endpoints, err)
		}
	}
}

func TestPlacementSRVDualStackSharesSinglePodIdentity(t *testing.T) {
	m, _, _ := fixtureManager(t, true, 1)
	m.lookupSRV = func(context.Context, string, string, string) (string, []*net.SRV, error) {
		return "", []*net.SRV{{Target: "POD-1.fixture.", Port: 5432}, {Target: "pod-1.fixture", Port: 5432}}, nil
	}
	m.config.LookupFunc = func(context.Context, string) ([]string, error) {
		return []string{"100::1", "192.0.2.2", "::ffff:192.0.2.2"}, nil
	}
	endpoints, err := m.endpoints(context.Background())
	if err != nil || len(endpoints) != 1 || endpoints[0].key != hashIdentity("pod-1.fixture:5432") || len(endpoints[0].addresses) != 2 {
		t.Fatalf("dual-stack SRV split backend accounting: %+v %v", endpoints, err)
	}
}

func TestPlacementExplicitLoopbackDoesNotRequireSRV(t *testing.T) {
	m, _, _ := fixtureManager(t, true, 1)
	m.config.Host = "127.0.0.1"
	m.config.LookupFunc = func(context.Context, string) ([]string, error) { return []string{"192.0.2.2"}, nil }
	m.lookupSRV = func(context.Context, string, string, string) (string, []*net.SRV, error) {
		t.Error("explicit socket endpoint unexpectedly required SRV")
		return "", nil, nil
	}
	snapshot, err := m.Begin(context.Background(), testTargets, func(_ context.Context, _ string, keys []string) (string, error) {
		if len(keys) != 1 || keys[0] != hashIdentity("127.0.0.1:5432") {
			t.Error("explicit endpoint key depended on its resolved socket")
		}
		return keys[0], nil
	}, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type retryableCloseSocket struct {
	net.Conn
	confirm atomic.Bool
}

func (s *retryableCloseSocket) Close() error {
	if !s.confirm.Load() {
		return errors.New("private-password-canary unknown socket state")
	}
	return s.Conn.Close()
}

func TestPlacementUnconfirmedProbesRetainBudgetAndCloseRetries(t *testing.T) {
	m, _, _ := fixtureManager(t, true, 1)
	dial := m.config.DialFunc
	sockets := make(chan *retryableCloseSocket, 3)
	m.config.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(ctx, network, address)
		if err != nil {
			return nil, err
		}
		socket := &retryableCloseSocket{Conn: conn}
		sockets <- socket
		return socket, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := m.Begin(ctx, testTargets, func(context.Context, string, []string) (string, error) {
		t.Error("unconfirmed probe cleanup proceeded to reservation")
		return "", nil
	}, func(context.Context) error { t.Error("probe cleanup cleared a reservation"); return nil })
	if !errors.Is(err, ErrCleanupUnconfirmed) || errors.Is(err, context.DeadlineExceeded) || len(m.probes) == 0 || len(m.probes) > 2 {
		t.Fatalf("uncertain probes were released or deadlocked refresh: %v probes=%d", err, len(m.probes))
	}
	for len(sockets) > 0 {
		(<-sockets).confirm.Store(true)
	}
	m.Close()
	if len(m.probes) != 0 || len(m.budget) != 0 {
		t.Fatal("Close did not retry and confirm held probe resources")
	}
}

func TestPlacementCleanupConfirmationContract(t *testing.T) {
	for _, success := range []bool{false, true} {
		t.Run(fmt.Sprint(success), func(t *testing.T) {
			m, nodes, _ := fixtureManager(t, true, 1)
			dial := m.config.DialFunc
			var socket *retryableCloseSocket
			m.config.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
				conn, err := dial(ctx, network, address)
				socket = &retryableCloseSocket{Conn: conn}
				return socket, err
			}
			m.cache = []probeResult{{endpoint: endpoint{key: nodes[1].key, port: 5432, addresses: []string{"192.0.2.2"}, policies: []*tls.Config{nil}}, heads: map[string]int64{"fixture:region": 2}}}
			m.cachedAt = time.Now()
			clears := 0
			snapshot, err := m.Begin(context.Background(), testTargets, func(_ context.Context, _ string, keys []string) (string, error) {
				if !success {
					nodes[1].generation.Store(3)
				}
				return keys[0], nil
			}, func(context.Context) error { clears++; return nil })
			if success {
				if err != nil || snapshot.PhysicalConnectionClosed() {
					t.Fatalf("open snapshot falsely confirmed closure: %v", err)
				}
				err = snapshot.Close(context.Background())
				if err == nil || snapshot.PhysicalConnectionClosed() {
					t.Fatal("snapshot treated logical pgx closure as physical confirmation")
				}
			} else if snapshot != nil || !errors.Is(err, ErrCleanupUnconfirmed) || !errors.Is(err, osm.ErrMatcherGenerationChanged) {
				t.Fatalf("failed Begin did not expose cleanup sentinel: %v", err)
			}
			if len(m.budget) != 1 || clears != 0 {
				t.Fatal("unconfirmed close released local/global capacity")
			}
			m.mu.Lock()
			for job := range m.active {
				if !job.conn.IsClosed() {
					t.Error("fixture did not exercise logically closed pgx connection")
				}
			}
			m.mu.Unlock()
			assertPrivate(t, err.Error())
			socket.confirm.Store(true)
			m.Close()
			if len(m.budget) != 0 || clears != 0 {
				t.Fatal("Close retry did not confirm local cleanup or cleared caller ledger")
			}
			if success {
				confirmation := interface{ PhysicalConnectionClosed() bool }(snapshot)
				if !confirmation.PhysicalConnectionClosed() || snapshot.Close(context.Background()) != nil {
					t.Fatal("successful retry was not observable and idempotent")
				}
			}
			if !strings.Contains(err.Error(), "OSM") {
				t.Fatal("cleanup error lost static context")
			}
		})
	}
}
