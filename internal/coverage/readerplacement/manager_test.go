package readerplacement

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"
)

var testTargets = []osm.MatcherGeneration{{RegionID: "fixture:region", GenerationID: 2}}

const fixtureServiceHost = "headless.fixture.svc.cluster.local"

func TestPlacementCoordinatedConcurrentRoutesAndBudget(t *testing.T) {
	m, nodes, _ := fixtureManager(t, true, 2)
	var mu sync.Mutex
	counts := make(map[string]int)
	selected := make(chan string, 4)
	reserve := func(_ context.Context, cluster string, keys []string) (string, error) {
		mu.Lock()
		defer mu.Unlock()
		if len(cluster) != 64 || !sort.StringsAreSorted(keys) || len(keys) != 2 {
			t.Errorf("allocator did not receive sorted opaque keys for both standbys")
		}
		choice := keys[0]
		for _, key := range keys {
			if counts[key] < counts[choice] {
				choice = key
			}
		}
		counts[choice]++
		selected <- choice
		return choice, nil
	}
	var clears atomic.Int32
	clear := func(context.Context) error { clears.Add(1); return nil }
	results := make(chan *osm.MatcherSnapshot, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			snapshot, err := m.Begin(context.Background(), testTargets, reserve, clear)
			if err != nil {
				t.Error(err)
			}
			results <- snapshot
		})
	}
	wg.Wait()
	first, second := <-selected, <-selected
	if first == second {
		t.Fatal("coordinated concurrent jobs chose the same backend")
	}
	if nodes[0].dials.Load() != 1 || nodes[1].dials.Load() != 2 || nodes[2].dials.Load() != 2 {
		t.Fatal("did not probe all three nodes or created extra idle connections")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := m.Begin(ctx, testTargets, reserve, clear); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("third job did not wait for its local budget: %v", err)
	}
	for range 2 {
		snapshot := <-results
		if snapshot == nil {
			t.Fatal("missing snapshot")
		}
		generations, err := snapshot.Generations(context.Background())
		if err != nil || len(generations) != 1 {
			t.Fatalf("held snapshot query: %v", err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := snapshot.Close(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if clears.Load() != 0 || len(m.budget) != 0 {
		t.Fatal("successful Close cleared caller reservation or retained local budget")
	}
	for _, node := range nodes {
		if node.named.Load() != 0 || node.unsafeStartup.Load() || node.unsafeBegin.Load() {
			t.Fatal("connection/probe was not read-only RR or used named role prepares")
		}
	}
}

func TestPlacementFailureClosesBeforeClearing(t *testing.T) {
	for _, failure := range []string{"socket", "promotion", "lag", "newer", "permission", "canceled", "clear failure"} {
		t.Run(failure, func(t *testing.T) {
			m, nodes, _ := fixtureManager(t, true, 1)
			var chosen *wireNode
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			reserve := func(_ context.Context, _ string, keys []string) (string, error) {
				for _, node := range nodes[1:] {
					if node.key == keys[0] {
						chosen = node
					}
				}
				switch failure {
				case "socket", "clear failure":
					chosen.failStartup.Store(true)
				case "promotion":
					chosen.standby.Store(false)
				case "lag":
					chosen.generation.Store(1)
				case "newer":
					chosen.generation.Store(3)
				case "permission":
					chosen.deny.Store(true)
				case "canceled":
					cancel()
				}
				return keys[0], nil
			}
			cleared := false
			clear := func(cleanup context.Context) error {
				cleared = true
				if cleanup.Err() != nil {
					t.Error("clear inherited cancellation")
				}
				chosen.mu.Lock()
				defer chosen.mu.Unlock()
				for _, socket := range chosen.clients {
					if !socket.closed.Load() {
						t.Error("ledger cleared before physical socket closed")
					}
				}
				if failure == "clear failure" {
					return &pgconn.PgError{Code: "42501", Message: "private-password-canary"}
				}
				return nil
			}
			snapshot, err := m.Begin(ctx, testTargets, reserve, clear)
			if snapshot != nil || err == nil || !cleared || len(m.budget) != 0 {
				t.Fatalf("failed begin cleanup: snapshot=%v err=%v cleared=%v budget=%d", snapshot, err, cleared, len(m.budget))
			}
			switch failure {
			case "newer":
				if !errors.Is(err, osm.ErrMatcherGenerationChanged) {
					t.Fatal(err)
				}
			case "permission", "clear failure":
				var state interface{ SQLState() string }
				if !errors.As(err, &state) || state.SQLState() != "42501" {
					t.Fatal("permission failure became retryable")
				}
			case "canceled":
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			default:
				if !errors.Is(err, osm.ErrMatcherReplicaNotReady) {
					t.Fatal(err)
				}
			}
			assertPrivate(t, err.Error())
			for _, node := range nodes {
				if node != chosen && node.dials.Load() != 1 {
					t.Error("failed reserved connection silently failed over to another backend")
				}
			}
			if failure == "promotion" || failure == "lag" {
				var offered []string
				snapshot, err = m.Begin(context.Background(), testTargets, func(_ context.Context, _ string, keys []string) (string, error) {
					offered = keys
					return keys[0], nil
				}, clear)
				if err != nil || len(offered) != 1 || offered[0] == chosen.key {
					t.Fatalf("failed backend not refreshed out of candidates: %v", err)
				}
				if err := snapshot.Close(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestPlacementAvailabilityAndReservationContract(t *testing.T) {
	for _, scenario := range []string{"primaries", "lag", "capacity", "invalid", "callback error", "default"} {
		t.Run(scenario, func(t *testing.T) {
			m, nodes, pool := fixtureManager(t, scenario != "default", 1)
			before := pool.Config().ConnConfig
			if scenario == "primaries" {
				for _, node := range nodes {
					node.standby.Store(false)
				}
			}
			if scenario == "lag" {
				for _, node := range nodes {
					node.generation.Store(1)
				}
			}
			var calls, clears int
			reserve := func(_ context.Context, _ string, keys []string) (string, error) {
				calls++
				if scenario == "capacity" {
					return "", nil
				}
				if scenario == "invalid" {
					return "private-password-canary", nil
				}
				if scenario == "callback error" {
					return "", &pgconn.PgError{Code: "42501", Message: "private-password-canary"}
				}
				if scenario == "default" && len(keys) != 3 {
					t.Error("default placement excluded primary")
				}
				if scenario == "default" {
					return nodes[0].key, nil
				}
				return keys[0], nil
			}
			snapshot, err := m.Begin(context.Background(), testTargets, reserve, func(context.Context) error { clears++; return nil })
			if scenario == "default" {
				if err != nil {
					t.Fatal(err)
				}
				snapshot.Close(context.Background())
			} else {
				if err == nil || snapshot != nil {
					t.Fatal("unavailable placement succeeded")
				}
				assertPrivate(t, err.Error())
			}
			if (scenario == "primaries" || scenario == "lag") && calls != 0 {
				t.Fatal("allocator called before eligibility was established")
			}
			wantClears := 0
			if scenario == "invalid" || scenario == "callback error" {
				wantClears = 1
			}
			if clears != wantClears || len(m.budget) != 0 {
				t.Fatal("incorrect reservation/budget cleanup")
			}
			after := pool.Config().ConnConfig
			if !reflect.DeepEqual(before.RuntimeParams, after.RuntimeParams) || after.RuntimeParams["default_transaction_read_only"] != "off" || pool.Stat().TotalConns() != 0 {
				t.Fatal("placement modified or acquired the control pool")
			}
		})
	}
}

func TestPlacementMissingAndMixedHeadsExcludedBeforeAllocator(t *testing.T) {
	m, nodes, _ := fixtureManager(t, true, 1)
	targets := []osm.MatcherGeneration{{RegionID: "fixture:region", GenerationID: 2}, {RegionID: "fixture:other", GenerationID: 5}}
	endpointFor := func(node *wireNode) endpoint {
		return endpoint{key: node.key, port: 5432, addresses: []string{strings.Split(node.address, ":")[0]}, policies: []*tls.Config{nil}}
	}
	for _, heads := range []map[string]int64{
		{"fixture:region": 3},
		{"fixture:region": 3, "fixture:other": 4},
		{"fixture:region": 1, "fixture:other": 6},
	} {
		m.cache = []probeResult{
			{endpointFor(nodes[1]), heads},
			{endpointFor(nodes[2]), map[string]int64{"fixture:region": 3, "fixture:other": 5}},
		}
		m.cachedAt = time.Now()
		_, err := m.Begin(context.Background(), targets, func(_ context.Context, _ string, keys []string) (string, error) {
			if len(keys) != 1 || keys[0] != nodes[2].key {
				t.Error("missing/mixed lagging heads were offered or newer heads rejected")
			}
			return "", nil
		}, func(context.Context) error { t.Error("empty reservation cleared"); return nil })
		if !errors.Is(err, osm.ErrMatcherReplicaNotReady) {
			t.Fatal(err)
		}
	}
}

func TestPlacementCloseHeldSnapshotDoesNotClearCallerReservation(t *testing.T) {
	m, _, _ := fixtureManager(t, true, 1)
	snapshot, err := m.Begin(context.Background(), testTargets, func(_ context.Context, _ string, keys []string) (string, error) {
		return keys[0], nil
	}, func(context.Context) error { t.Error("manager cleared caller reservation"); return nil })
	if err != nil {
		t.Fatal(err)
	}
	m.Close()
	if len(m.budget) != 0 {
		t.Fatal("manager Close did not physically close active snapshot")
	}
	_ = snapshot.Close(context.Background())
}

func TestPlacementCloseCancelsInFlightAndFutureBegins(t *testing.T) {
	m, _, _ := fixtureManager(t, true, 2)
	entered := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		_, err := m.Begin(context.Background(), testTargets, func(ctx context.Context, _ string, _ []string) (string, error) {
			close(entered)
			<-ctx.Done()
			return "", ctx.Err()
		}, func(context.Context) error { return nil })
		finished <- err
	}()
	<-entered
	m.Close()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	_, err := m.Begin(context.Background(), testTargets, func(context.Context, string, []string) (string, error) {
		t.Error("closed manager reserved a backend")
		return "", nil
	}, func(context.Context) error { return nil })
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	m.Close()
}

func TestPlacementSRVIdentitiesTLSAndConfig(t *testing.T) {
	fixtureEnvironment(t)
	config, err := pgxpool.ParseConfig("postgresql://fixture-user:private-password-canary@Headless.fixture.svc.cluster.local:5432/fixture?sslmode=verify-full")
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.TLSConfig.Certificates = []tls.Certificate{{Certificate: [][]byte{[]byte("fixture-cert")}}}
	config.ConnConfig.LookupFunc = func(_ context.Context, host string) ([]string, error) {
		if host == "pod-0.fixture" {
			return []string{"2001:db8::1", "192.0.2.1", "::ffff:192.0.2.1", "2001:db8:0::1"}, nil
		}
		return []string{"192.0.2.2"}, nil
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	m, err := New(pool, true, 2)
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	m.lookupSRV = func(_ context.Context, service, protocol, host string) (string, []*net.SRV, error) {
		if service != "postgresql" || protocol != "tcp" || host != fixtureServiceHost {
			t.Error("incorrect SRV query")
		}
		return "", []*net.SRV{{Target: "POD-0.fixture.", Port: 5432}, {Target: "pod-0.fixture.", Port: 5432}, {Target: "pod-1.fixture.", Port: 5432}}, nil
	}
	endpoints, err := m.endpoints(context.Background())
	if err != nil || len(endpoints) != 2 {
		t.Fatalf("SRV endpoint discovery: %v %+v", err, endpoints)
	}
	for _, endpoint := range endpoints {
		policy := endpoint.policies[0]
		if policy.ServerName != "Headless.fixture.svc.cluster.local" || policy.InsecureSkipVerify || len(policy.Certificates) != 1 || policy.RootCAs != config.ConnConfig.TLSConfig.RootCAs {
			t.Fatal("pod discovery changed original TLS identity, roots, or client certificate")
		}
		if endpoint.key == hashIdentity("pod-0.fixture:5432") && len(endpoint.addresses) != 2 {
			t.Fatal("SRV did not deduplicate/group IPv4 and IPv6 aliases")
		}
	}
	other := m.config.Copy()
	other.User, other.Password = "different-role", "different-password"
	other.Host = fixtureServiceHost + "."
	if clusterIdentity(other) != m.clusterKey {
		t.Fatal("credential differences split global cluster identity")
	}
	other.Database = "different-database"
	if clusterIdentity(other) == m.clusterKey {
		t.Fatal("different databases shared cluster identity")
	}
}

type wireNode struct {
	address       string
	key           string
	standby       atomic.Bool
	generation    atomic.Int64
	failStartup   atomic.Bool
	deny          atomic.Bool
	invalidDigest atomic.Bool
	startupState  string
	roleGate      <-chan struct{}
	serverTLS     *tls.Config
	verifiedTLS   atomic.Bool
	metadataDelay atomic.Int64
	cancelDelay   atomic.Int64
	dials         atomic.Int32
	named         atomic.Int32
	unsafeStartup atomic.Bool
	unsafeBegin   atomic.Bool
	mu            sync.Mutex
	clients       []*observedSocket
	servers       []net.Conn
	wg            sync.WaitGroup
}

type observedSocket struct {
	net.Conn
	closed atomic.Bool
}

func (s *observedSocket) Close() error {
	err := s.Conn.Close()
	s.closed.Store(true)
	return err
}

func fixtureManager(t *testing.T, strict bool, budget int) (*Manager, []*wireNode, *pgxpool.Pool) {
	t.Helper()
	fixtureEnvironment(t)
	var nodes []*wireNode
	for i := range 3 {
		address := fmt.Sprintf("192.0.2.%d:5432", i+1)
		node := &wireNode{address: address, key: hashIdentity(fmt.Sprintf("pod-%d.fixture:5432", i))}
		node.standby.Store(i != 0)
		node.generation.Store(2)
		nodes = append(nodes, node)
	}
	config, err := pgxpool.ParseConfig("postgresql://fixture-user:private-password-canary@" + fixtureServiceHost + "/fixture?sslmode=disable&default_query_exec_mode=simple_protocol&default_transaction_read_only=off")
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.LookupFunc = func(_ context.Context, host string) ([]string, error) {
		for i := range nodes {
			if host == fmt.Sprintf("pod-%d.fixture", i) {
				return []string{fmt.Sprintf("192.0.2.%d", i+1)}, nil
			}
		}
		return []string{"192.0.2.1", "192.0.2.2", "192.0.2.3", "::ffff:192.0.2.3"}, nil
	}
	config.ConnConfig.DialFunc = func(_ context.Context, _ string, address string) (net.Conn, error) {
		for _, node := range nodes {
			if node.address == address {
				return node.dial(), nil
			}
		}
		return nil, errors.New("private-password-canary: unpinned dial")
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(pool, strict, budget)
	if err != nil {
		t.Fatal(err)
	}
	m.lookupSRV = func(context.Context, string, string, string) (string, []*net.SRV, error) {
		return "", []*net.SRV{{Target: "pod-0.fixture.", Port: 5432}, {Target: "pod-1.fixture.", Port: 5432}, {Target: "pod-2.fixture.", Port: 5432}}, nil
	}
	t.Cleanup(func() {
		m.Close()
		pool.Close()
		for _, node := range nodes {
			node.mu.Lock()
			for _, server := range node.servers {
				server.Close()
			}
			node.mu.Unlock()
			node.wg.Wait()
		}
	})
	return m, nodes, pool
}

func fixtureEnvironment(t *testing.T) {
	t.Helper()
	// Parsing fixture configurations must not load an operator's service,
	// password file, or client TLS keys from libpq environment defaults.
	for _, key := range []string{"PGSERVICE", "PGSERVICEFILE", "PGSSLKEY", "PGSSLCERT", "PGSSLROOTCERT"} {
		t.Setenv(key, "")
	}
	t.Setenv("PGPASSFILE", "/dev/null")
	t.Setenv("PGUSER", "fixture-user")
	t.Setenv("PGPASSWORD", "fixture-password")
}

func (n *wireNode) dial() net.Conn {
	client, server := net.Pipe()
	socket := &observedSocket{Conn: client}
	n.dials.Add(1)
	n.mu.Lock()
	n.clients = append(n.clients, socket)
	n.servers = append(n.servers, server)
	n.mu.Unlock()
	n.wg.Go(func() {
		defer server.Close()
		backend := pgproto3.NewBackend(server, server)
		message, err := backend.ReceiveStartupMessage()
		if _, cancel := message.(*pgproto3.CancelRequest); cancel {
			time.Sleep(time.Duration(n.cancelDelay.Load()))
			return
		}
		if _, ssl := message.(*pgproto3.SSLRequest); ssl && err == nil && n.serverTLS != nil {
			if _, err = server.Write([]byte("S")); err != nil {
				return
			}
			secured := tls.Server(server, n.serverTLS)
			if secured.Handshake() != nil {
				return
			}
			n.verifiedTLS.Store(len(secured.ConnectionState().VerifiedChains) > 0)
			backend = pgproto3.NewBackend(secured, secured)
			message, err = backend.ReceiveStartupMessage()
		}
		if err != nil || n.failStartup.Load() {
			return
		}
		startup, ok := message.(*pgproto3.StartupMessage)
		if !ok {
			return
		}
		if startup.Parameters["default_transaction_read_only"] != "on" {
			n.unsafeStartup.Store(true)
		}
		if n.startupState != "" {
			if n.roleGate != nil {
				<-n.roleGate
			}
			backend.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: n.startupState, Message: "private-password-canary"})
			_ = backend.Flush()
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
		var sql string
		for {
			message, err := backend.Receive()
			if err != nil {
				return
			}
			switch message := message.(type) {
			case *pgproto3.Terminate:
				return
			case *pgproto3.Parse:
				sql = message.Query
				if message.Name != "" {
					n.named.Add(1)
				}
				backend.Send(&pgproto3.ParseComplete{})
			case *pgproto3.Bind:
				backend.Send(&pgproto3.BindComplete{})
			case *pgproto3.Describe:
				fields, _, _, _ := n.answer(sql, &status)
				backend.Send(&pgproto3.RowDescription{Fields: fields})
			case *pgproto3.Execute:
				n.respond(backend, sql, &status, false)
			case *pgproto3.Query:
				n.respond(backend, message.String, &status, true)
				backend.Send(&pgproto3.ReadyForQuery{TxStatus: status})
				if backend.Flush() != nil {
					return
				}
			case *pgproto3.Sync:
				backend.Send(&pgproto3.ReadyForQuery{TxStatus: status})
				if backend.Flush() != nil {
					return
				}
			}
		}
	})
	return socket
}

func (n *wireNode) respond(backend *pgproto3.Backend, sql string, status *byte, describe bool) {
	fields, rows, tag, denied := n.answer(sql, status)
	if denied {
		backend.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "42501", Message: "private-password-canary at 192.0.2.1"})
		return
	}
	if describe && len(fields) != 0 {
		backend.Send(&pgproto3.RowDescription{Fields: fields})
	}
	for _, row := range rows {
		backend.Send(&pgproto3.DataRow{Values: row})
	}
	backend.Send(&pgproto3.CommandComplete{CommandTag: []byte(tag)})
}

func (n *wireNode) answer(sql string, status *byte) ([]pgproto3.FieldDescription, [][][]byte, string, bool) {
	lower := strings.ToLower(sql)
	if strings.Contains(lower, "pg_is_in_recovery") {
		if n.roleGate != nil {
			<-n.roleGate
		}
		value := "f"
		if n.standby.Load() {
			value = "t"
		}
		return []pgproto3.FieldDescription{{Name: []byte("recovery"), DataTypeOID: 16, DataTypeSize: 1}}, [][][]byte{{[]byte(value)}}, "SELECT 1", n.deny.Load()
	}
	if strings.HasPrefix(lower, "begin") {
		*status = 'T'
		if !strings.Contains(lower, "repeatable read") || !strings.Contains(lower, "read only") {
			n.unsafeBegin.Store(true)
		}
		return nil, nil, "BEGIN", false
	}
	if strings.HasPrefix(lower, "rollback") || strings.HasPrefix(lower, "commit") {
		*status = 'I'
		return nil, nil, "ROLLBACK", false
	}
	fields := []pgproto3.FieldDescription{
		{Name: []byte("region_id"), DataTypeOID: 25, DataTypeSize: -1},
		{Name: []byte("id"), DataTypeOID: 20, DataTypeSize: 8},
	}
	row := [][]byte{[]byte("fixture:region"), []byte(strconv.FormatInt(n.generation.Load(), 10))}
	if strings.Contains(lower, "source_url") {
		time.Sleep(time.Duration(n.metadataDelay.Load()))
		for _, oid := range []uint32{25, 25, 1184, 23, 23, 1184} {
			fields = append(fields, pgproto3.FieldDescription{DataTypeOID: oid, DataTypeSize: -1})
		}
		row = append(row, []byte("https://example.test/fixture.pbf"), []byte(strings.Repeat("0", 64)), nil,
			[]byte("1"), []byte("1"), []byte("2026-10-13 00:00:00+00"))
		if n.invalidDigest.Load() {
			row[3] = []byte("private-password-canary-invalid-digest")
		}
	}
	return fields, [][][]byte{row}, "SELECT 1", n.deny.Load()
}

func assertPrivate(t *testing.T, value string) {
	t.Helper()
	for _, forbidden := range []string{"private-password-canary", "fixture-user", "192.0.2.", "headless.fixture", "postgresql://"} {
		if strings.Contains(value, forbidden) {
			t.Errorf("private metadata leaked: %q", forbidden)
		}
	}
}
