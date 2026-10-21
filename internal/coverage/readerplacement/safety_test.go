package readerplacement

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestPlacementSRVVerifiedMTLSAndIPv6(t *testing.T) {
	m, nodes, _ := fixtureManager(t, true, 1)
	clientTLS, serverTLS := fixtureTLS(t)
	m.config.TLSConfig = clientTLS
	for _, node := range nodes {
		node.serverTLS = serverTLS
	}
	nodes[1].address = "[2001:db8::2]:5432"
	nodes[1].key = hashIdentity("pod-1.fixture:5432")
	nodes[2].key = hashIdentity("pod-2.fixture:5432")
	m.config.LookupFunc = func(_ context.Context, host string) ([]string, error) {
		switch host {
		case "pod-1.fixture":
			return []string{"2001:db8::2"}, nil
		case "pod-2.fixture":
			return []string{"192.0.2.3"}, nil
		default:
			return []string{"192.0.2.1"}, nil
		}
	}
	m.lookupSRV = func(context.Context, string, string, string) (string, []*net.SRV, error) {
		return "", []*net.SRV{{Target: "pod-0.fixture.", Port: 5432}, {Target: "pod-1.fixture.", Port: 5432}, {Target: "pod-2.fixture.", Port: 5432}}, nil
	}
	snapshot, err := m.Begin(context.Background(), testTargets, func(_ context.Context, _ string, keys []string) (string, error) {
		for _, key := range keys {
			if key == nodes[1].key {
				return key, nil
			}
		}
		t.Error("IPv6 pod missing from candidates")
		return "", nil
	}, func(context.Context) error { t.Error("successful TLS route cleared reservation"); return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close(context.Background())
	if !nodes[1].verifiedTLS.Load() || clientTLS.ServerName != fixtureServiceHost || clientTLS.InsecureSkipVerify {
		t.Fatal("SRV pod connection did not retain verified service-name mTLS")
	}
}

func fixtureTLS(t *testing.T) (*tls.Config, *tls.Config) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fixture CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(parsed)
	leaf := func(serial int64, usage x509.ExtKeyUsage) tls.Certificate {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		template := &x509.Certificate{SerialNumber: big.NewInt(serial), DNSNames: []string{fixtureServiceHost},
			NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, ExtKeyUsage: []x509.ExtKeyUsage{usage}, KeyUsage: x509.KeyUsageDigitalSignature}
		der, err := x509.CreateCertificate(rand.Reader, template, parsed, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	}
	return &tls.Config{ServerName: fixtureServiceHost, RootCAs: roots, Certificates: []tls.Certificate{leaf(2, x509.ExtKeyUsageClientAuth)}, MinVersion: tls.VersionTLS12},
		&tls.Config{Certificates: []tls.Certificate{leaf(3, x509.ExtKeyUsageServerAuth)}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert, MinVersion: tls.VersionTLS12}
}

func TestPlacementRetainsValidationAndSanitizesTracing(t *testing.T) {
	m, nodes, _ := fixtureManager(t, true, 1)
	var validations atomic.Int32
	m.config.ValidateConnect = func(ctx context.Context, conn *pgconn.PgConn) error {
		validations.Add(1)
		_, err := checkRole(ctx, conn)
		return err
	}
	trace := &recordingTracer{}
	m.config.Tracer = &safeTracer{query: trace}
	var chosen *wireNode
	snapshot, err := m.Begin(context.Background(), testTargets, func(_ context.Context, _ string, keys []string) (string, error) {
		for _, node := range nodes {
			if node.key == keys[0] {
				chosen = node
			}
		}
		return keys[0], nil
	}, func(context.Context) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if validations.Load() != 3 {
		t.Fatal("existing ValidateConnect hook was dropped or bypassed")
	}
	chosen.deny.Store(true)
	_, err = snapshot.Generations(context.Background())
	if err == nil {
		t.Fatal("fixture workload error missing")
	}
	snapshot.Close(context.Background())
	trace.mu.Lock()
	defer trace.mu.Unlock()
	if trace.connects == 0 || trace.queries == 0 {
		t.Fatal("existing tracer not retained")
	}
	assertPrivate(t, strings.Join(trace.records, "\n"))
	if strings.Contains(strings.Join(trace.records, "\n"), "pg_is_in_recovery") {
		t.Fatal("role probe passed through query tracing")
	}
}

type recordingTracer struct {
	mu       sync.Mutex
	records  []string
	connects int
	queries  int
}

func (t *recordingTracer) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.queries++
	t.records = append(t.records, fmt.Sprint(conn, data.SQL, data.Args))
	return ctx
}

func (t *recordingTracer) TraceQueryEnd(_ context.Context, conn *pgx.Conn, data pgx.TraceQueryEndData) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.records = append(t.records, fmt.Sprint(conn, data.Err))
}

func (t *recordingTracer) TraceConnectStart(ctx context.Context, data pgx.TraceConnectStartData) context.Context {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.connects++
	if data.ConnConfig != nil {
		t.records = append(t.records, fmt.Sprint(data.ConnConfig.ConnString(), data.ConnConfig.Host, data.ConnConfig.User, data.ConnConfig.Password, data.ConnConfig.RuntimeParams))
	}
	return ctx
}

func (t *recordingTracer) TraceConnectEnd(_ context.Context, data pgx.TraceConnectEndData) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.records = append(t.records, fmt.Sprint(data.Conn, data.Err))
}

func TestPlacementDiscoveryBoundedAndCached(t *testing.T) {
	m, nodes, _ := fixtureManager(t, false, 3)
	original := m.config.DialFunc
	var active, peak atomic.Int32
	m.config.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); current > old && !peak.CompareAndSwap(old, current); old = peak.Load() {
		}
		deadline, bounded := ctx.Deadline()
		if !bounded || time.Until(deadline) > connectTimeout {
			t.Error("discovery connect deadline was not bounded")
		}
		time.Sleep(10 * time.Millisecond)
		return original(ctx, network, address)
	}
	if _, err := m.discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := m.discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	if peak.Load() != 2 {
		t.Fatal("discovery did not use at most two parallel probes")
	}
	for _, node := range nodes {
		if node.dials.Load() != 1 {
			t.Fatal("ten-second discovery cache missed")
		}
	}
	m.mu.Lock()
	m.cachedAt = time.Now().Add(-cacheLifetime)
	m.mu.Unlock()
	if _, err := m.discover(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, node := range nodes {
		if node.dials.Load() != 2 {
			t.Fatal("expired discovery did not refresh all roles")
		}
	}
}

func TestPlacementPreservesSSLPolicyOrderWithoutHostFailover(t *testing.T) {
	fixtureEnvironment(t)
	config, err := pgx.ParseConfig("host=first.fixture,second.fixture port=5432,5433 dbname=fixture sslmode=prefer")
	if err != nil {
		t.Fatal(err)
	}
	hosts := configuredHosts(config)
	if len(hosts) != 2 || hosts[0].port != 5432 || hosts[1].port != 5433 || len(hosts[0].policies) != 2 || hosts[0].policies[0] == nil || hosts[0].policies[1] != nil {
		t.Fatal("configured host/SSL ordering changed")
	}
	reversed := config.Copy()
	reversed.Host, reversed.Port, reversed.TLSConfig = config.Fallbacks[1].Host, config.Fallbacks[1].Port, config.Fallbacks[1].TLSConfig
	reversed.Fallbacks = append([]*pgconn.FallbackConfig{{Host: config.Host, Port: config.Port, TLSConfig: config.TLSConfig}}, config.Fallbacks...)
	if clusterIdentity(reversed) != clusterIdentity(config) {
		t.Fatal("host ordering or duplicate SSL policies changed cluster identity")
	}
	m, nodes, _ := fixtureManager(t, true, 1)
	nodes[1].failStartup.Store(true)
	nodes[2].failStartup.Store(true)
	_, err = m.Begin(context.Background(), testTargets, func(context.Context, string, []string) (string, error) {
		t.Error("ineligible nodes offered")
		return "", nil
	}, func(context.Context) error { return nil })
	if !errors.Is(err, osm.ErrMatcherReplicaNotReady) {
		t.Fatal(err)
	}
	if len(m.config.Fallbacks) != 0 {
		t.Fatal("fixture unexpectedly has failover endpoints")
	}
}

type uncertainSocket struct{ net.Conn }

func (s uncertainSocket) Close() error { return errors.New("socket closure unconfirmed") }

func TestPlacementUnconfirmedCloseRetainsAssignmentAndBudget(t *testing.T) {
	m, nodes, _ := fixtureManager(t, true, 1)
	dial := m.config.DialFunc
	var client net.Conn
	m.config.DialFunc = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := dial(ctx, network, address)
		if address == nodes[1].address {
			client = conn
			return uncertainSocket{conn}, err
		}
		return conn, err
	}
	// Preload discovery so only the reserved job socket is uncertain.
	m.cache = []probeResult{{endpoint: endpoint{key: nodes[1].key, port: 5432, addresses: []string{"192.0.2.2"}, policies: []*tls.Config{nil}}, heads: map[string]int64{"fixture:region": 2}}}
	m.cachedAt = time.Now()
	_, err := m.Begin(context.Background(), testTargets, func(_ context.Context, _ string, keys []string) (string, error) {
		nodes[1].generation.Store(3)
		return keys[0], nil
	}, func(context.Context) error { t.Error("unconfirmed socket freed global assignment"); return nil })
	if !errors.Is(err, ErrCleanupUnconfirmed) || !errors.Is(err, osm.ErrMatcherGenerationChanged) || len(m.budget) != 1 {
		t.Fatalf("unconfirmed closure released budget: err=%v budget=%d", err, len(m.budget))
	}
	if client != nil {
		client.Close()
	}
}

func TestPlacementSnapshotCloseConfirmsSocketDuringAsyncCancellation(t *testing.T) {
	m, nodes, _ := fixtureManager(t, true, 1)
	var chosen *wireNode
	snapshot, err := m.Begin(context.Background(), testTargets, func(_ context.Context, _ string, keys []string) (string, error) {
		for _, node := range nodes {
			if node.key == keys[0] {
				chosen = node
			}
		}
		return keys[0], nil
	}, func(context.Context) error { t.Error("successful snapshot auto-cleared"); return nil })
	if err != nil {
		t.Fatal(err)
	}
	chosen.mu.Lock()
	socket := chosen.clients[len(chosen.clients)-1]
	chosen.mu.Unlock()
	chosen.metadataDelay.Store(int64(100 * time.Millisecond))
	chosen.cancelDelay.Store(int64(200 * time.Millisecond))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	if _, err := snapshot.Generations(ctx); err == nil {
		t.Fatal("fixture query did not cancel")
	}
	_ = snapshot.Close(ctx)
	if !socket.closed.Load() || len(m.budget) != 0 {
		t.Fatal("snapshot Close returned before closing asynchronous pgx socket cleanup")
	}
}
