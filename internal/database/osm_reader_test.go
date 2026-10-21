package database

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestOSMReaderDefaultPreservesNormalPool(t *testing.T) {
	server := newOSMReaderWireServer(t, "primary", false, nil)
	dsn := osmReaderTestDSN(server) + " target_session_attrs=prefer-standby default_transaction_read_only=off pool_max_conns=7 default_query_exec_mode=simple_protocol"
	normal, err := OpenWithMaxConns(context.Background(), dsn, "reader-default", 2)
	if err != nil {
		t.Fatal(err)
	}
	defer normal.Close()
	reader, err := OpenOSMReader(context.Background(), dsn, "reader-default", 2, false)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	a, b := normal.Config(), reader.Config()
	if _, wrapped := b.ConnConfig.Tracer.(*osmReaderTracer); wrapped {
		t.Fatal("disabled replica routing changed normal tracer")
	}
	if a.MaxConns != b.MaxConns || b.MaxConns != 2 || b.PrepareConn != nil || b.BeforeAcquire != nil ||
		a.ConnConfig.DefaultQueryExecMode != b.ConnConfig.DefaultQueryExecMode ||
		a.ConnConfig.ConnectTimeout != b.ConnConfig.ConnectTimeout ||
		a.ConnConfig.TLSConfig != b.ConnConfig.TLSConfig ||
		!reflect.DeepEqual(a.ConnConfig.RuntimeParams, b.ConnConfig.RuntimeParams) ||
		reflect.ValueOf(a.ConnConfig.ValidateConnect).Pointer() != reflect.ValueOf(b.ConnConfig.ValidateConnect).Pointer() {
		t.Fatal("disabled replica routing changed normal pool configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := reader.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	conn.Release()
	if startup := <-server.startups; startup["default_transaction_read_only"] != "off" {
		t.Fatal("disabled replica routing changed session read-only setting")
	}
}

func TestOSMReaderStrictStandbyOverridesURI(t *testing.T) {
	for _, attrs := range []string{"any", "read-write", "read-only", "primary", "standby", "prefer-standby"} {
		t.Run(attrs, func(t *testing.T) {
			primary := newOSMReaderWireServer(t, "primary", false, nil)
			standby := newOSMReaderWireServer(t, "standby", true, nil)
			pool, err := OpenOSMReader(context.Background(), osmReaderTestDSN(primary, standby)+" target_session_attrs="+attrs, "reader-strict", 1, true)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			if _, wrapped := pool.Config().ConnConfig.Tracer.(*osmReaderTracer); !wrapped {
				t.Fatal("replica reader is missing its connection-error tracer")
			}
			if pool.Stat().TotalConns() != 0 {
				t.Fatal("opening reader eagerly connected")
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			conn, err := pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Release()
			var node string
			if err := conn.QueryRow(ctx, "SELECT reader_node()", pgx.QueryExecModeExec).Scan(&node); err != nil || node != "standby" {
				t.Fatalf("selected node = %q, err = %v", node, err)
			}
			if primary.roleReads.Load() != 1 || standby.roleReads.Load() != 2 {
				t.Fatalf("role checks: primary=%d standby=%d", primary.roleReads.Load(), standby.roleReads.Load())
			}
			for _, server := range []*osmReaderWireServer{primary, standby} {
				startup := <-server.startups
				if startup["default_transaction_read_only"] != "on" || startup["application_name"] != "reader-strict" {
					t.Fatal("missing session safety/application parameters")
				}
				if server.namedParses.Load() != 0 {
					t.Fatal("role check used a named prepared statement")
				}
			}
		})
	}
}

func TestOSMReaderNoPrimaryFallback(t *testing.T) {
	first := newOSMReaderWireServer(t, "first", false, nil)
	second := newOSMReaderWireServer(t, "second", false, nil)
	pool, err := OpenOSMReader(context.Background(), osmReaderTestDSN(first, second)+" target_session_attrs=prefer-standby", "reader-unavailable", 1, true)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if conn, err := pool.Acquire(ctx); err == nil {
		conn.Release()
		t.Fatal("acquired a primary when no standby was available")
	}
	if first.roleReads.Load() != 1 || second.roleReads.Load() != 1 {
		t.Fatal("did not try all hosts, or retried a primary as prefer-standby fallback")
	}
	if pool.Stat().AcquiredConns() != 0 {
		t.Fatal("failed acquisition leaked a checked-out connection")
	}
}

func TestOSMReaderPromotionRejectsPooledConnection(t *testing.T) {
	first := newOSMReaderWireServer(t, "first", true, nil)
	second := newOSMReaderWireServer(t, "second", true, nil)
	pool, err := OpenOSMReader(context.Background(), osmReaderTestDSN(first, second), "reader-promotion", 1, true)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	original := conn.Conn()
	conn.Release()
	first.standby.Store(false)
	conn, err = pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	var node string
	if err := conn.QueryRow(ctx, "SELECT reader_node()", pgx.QueryExecModeExec).Scan(&node); err != nil || node != "second" || conn.Conn() == original {
		t.Fatalf("promotion reacquisition: node=%q err=%v reused=%v", node, err, conn.Conn() == original)
	}
	if first.roleReads.Load() != 4 || second.roleReads.Load() != 2 {
		t.Fatalf("unexpected promotion checks: %d, %d", first.roleReads.Load(), second.roleReads.Load())
	}
}

func TestOSMReaderTransactionKeepsPhysicalConnection(t *testing.T) {
	first := newOSMReaderWireServer(t, "first", true, nil)
	second := newOSMReaderWireServer(t, "second", true, nil)
	pool, err := OpenOSMReader(context.Background(), osmReaderTestDSN(first, second), "reader-transaction", 1, true)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	checks := first.roleReads.Load()
	first.standby.Store(false)
	for range 2 {
		var node string
		if err := tx.QueryRow(ctx, "SELECT reader_node()", pgx.QueryExecModeExec).Scan(&node); err != nil || node != "first" {
			t.Fatalf("transaction moved physical node: %q, %v", node, err)
		}
	}
	if first.roleReads.Load() != checks || second.roleReads.Load() != 0 {
		t.Fatal("held transaction triggered per-statement role checks or rerouting")
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if pool.Stat().AcquiredConns() != 0 {
		t.Fatal("rollback did not release pooled connection")
	}
}

func TestOSMReaderPrepareFailureIsSafeAndDoesNotRetryForever(t *testing.T) {
	for _, failure := range []string{"sql", "deadline", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			server := newOSMReaderWireServer(t, "standby", true, nil)
			pool, err := OpenOSMReader(context.Background(), osmReaderTestDSN(server), "reader-role-failure", 1, true)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			conn, err := pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			conn.Release()
			checks := server.roleReads.Load()
			acquireCtx := ctx
			if failure == "sql" {
				server.failRole.Store(true)
			} else {
				server.roleDelay.Store(int64(time.Second))
				var stop context.CancelFunc
				acquireCtx, stop = context.WithCancel(ctx)
				defer stop()
				if failure == "deadline" {
					acquireCtx, stop = context.WithTimeout(ctx, 30*time.Millisecond)
					defer stop()
				} else {
					timer := time.AfterFunc(30*time.Millisecond, stop)
					defer timer.Stop()
				}
			}
			if conn, err = pool.Acquire(acquireCtx); err == nil {
				conn.Release()
				t.Fatal("role read failure succeeded")
			}
			if failure == "sql" && !strings.Contains(err.Error(), "SQLSTATE 42501") {
				t.Fatalf("missing safe SQLSTATE: %v", err)
			}
			if failure == "deadline" && !errors.Is(err, context.DeadlineExceeded) || failure == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost context error: %v", err)
			}
			for _, forbidden := range []string{"reader-private", "password-canary", "127.0.0.1", "postgres://"} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("role error leaked %q", forbidden)
				}
			}
			// Close waits for destruction, but puddle removes the resource from
			// its statistics just after signalling that destruction is complete.
			pool.Close()
			for pool.Stat().AcquiredConns() != 0 {
				select {
				case <-ctx.Done():
					t.Fatal("rejected connection remained in pool accounting")
				case <-time.After(time.Millisecond):
				}
			}
			if server.roleReads.Load() != checks+1 || pool.Stat().AcquiredConns() != 0 {
				t.Fatalf("role failure checks=%d want=%d acquired=%d", server.roleReads.Load(), checks+1, pool.Stat().AcquiredConns())
			}
		})
	}
}

func TestOSMReaderRoleProbeDoesNotLeakTraceErrors(t *testing.T) {
	for _, failureAt := range []string{"connect", "checkout"} {
		t.Run(failureAt, func(t *testing.T) {
			server := newOSMReaderWireServer(t, "standby", true, nil)
			recorder := tracetest.NewSpanRecorder()
			provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder), sdktrace.WithSampler(sdktrace.AlwaysSample()))
			defer provider.Shutdown(context.Background())
			config, err := pgxpool.ParseConfig(osmReaderTestDSN(server))
			if err != nil {
				t.Fatal(err)
			}
			config.MaxConns = 1
			config.ConnConfig.Tracer = &osmReaderTracer{Tracer: newTracer(otelpgx.WithTracerProvider(provider))}
			configureOSMReadReplicas(config)
			pool, err := pgxpool.NewWithConfig(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			ctx, parent := provider.Tracer("test").Start(ctx, "reader-role-privacy")
			defer parent.End()
			workload := func() {
				conn, err := pool.Acquire(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Release()
				var node string
				if err := conn.QueryRow(ctx, "SELECT reader_node()", pgx.QueryExecModeExec).Scan(&node); err != nil || node != "standby" {
					t.Fatalf("workload query: %q, %v", node, err)
				}
				err = conn.QueryRow(ctx, "SELECT reader_workload_error()", pgx.QueryExecModeExec).Scan(&node)
				var pgErr *pgconn.PgError
				if !errors.As(err, &pgErr) || pgErr.Code != "22003" {
					t.Fatalf("workload SQL error changed: %v", err)
				}
			}
			if failureAt == "checkout" {
				workload()
			}
			server.failRole.Store(true)
			if conn, err := pool.Acquire(ctx); err == nil {
				conn.Release()
				t.Fatal("poisoned role probe succeeded")
			} else if strings.Contains(err.Error(), "password-canary") || !strings.Contains(err.Error(), "SQLSTATE 42501") {
				t.Fatalf("role failure did not retain only safe diagnostics: %v", err)
			}
			if failureAt == "connect" {
				server.failRole.Store(false)
				workload()
			}
			pool.Close()
			parent.End()
			querySpans, workloadErrorSpans, errorEvents := 0, 0, 0
			for _, span := range recorder.Ended() {
				if span.Name() == "postgresql.query" {
					querySpans++
					for _, attr := range span.Attributes() {
						if attr.Key == otelpgx.SQLStateKey && attr.Value.AsString() == "22003" {
							workloadErrorSpans++
						}
					}
				}
				errorEvents += len(span.Events())
				serialized := fmt.Sprint(span.Name(), span.Attributes(), span.Events(), span.Status())
				if len(span.Events()) > 0 && (span.Name() == "connect" || span.Name() == "pool.acquire") && !strings.Contains(span.Status().Description, "SQLSTATE 42501") {
					t.Fatal("role failure telemetry lost its safe SQLSTATE")
				}
				for _, forbidden := range []string{"password-canary", "reader-private", "127.0.0.1", osmReaderRoleSQL, "reader_node"} {
					if strings.Contains(serialized, forbidden) {
						t.Fatalf("trace exposed %q", forbidden)
					}
				}
			}
			if querySpans != 2 || workloadErrorSpans != 1 || errorEvents == 0 || server.namedParses.Load() != 0 {
				t.Fatalf("query spans=%d want=2, workload SQLSTATE spans=%d want=1, error events=%d want>0, named parses=%d want=0", querySpans, workloadErrorSpans, errorEvents, server.namedParses.Load())
			}
		})
	}
}

func TestOSMReaderConnectionFailureTelemetryIsPrivate(t *testing.T) {
	first := newOSMReaderWireServer(t, "first", false, nil)
	second := newOSMReaderWireServer(t, "second", false, nil)
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	defer provider.Shutdown(context.Background())
	metricsReader := metric.NewManualReader()
	metricsProvider := metric.NewMeterProvider(metric.WithReader(metricsReader))
	defer metricsProvider.Shutdown(context.Background())
	config, err := pgxpool.ParseConfig("host=reader-host-canary.invalid user=reader-user-canary dbname=reader-db-canary password=password-canary sslmode=disable target_session_attrs=prefer-standby")
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	config.ConnConfig.Tracer = &osmReaderTracer{Tracer: newTracer(otelpgx.WithTracerProvider(provider), otelpgx.WithMeterProvider(metricsProvider))}
	config.ConnConfig.LookupFunc = func(context.Context, string) ([]string, error) {
		return []string{first.listener.Addr().String(), second.listener.Addr().String()}, nil
	}
	configureOSMReadReplicas(config)
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := recordPoolStats(pool, "reader-private-telemetry-test", otelpgx.WithStatsMeterProvider(metricsProvider)); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ctx, parent := provider.Tracer("test").Start(ctx, "reader-connection-privacy")
	defer parent.End()
	conn, rawErr := pool.Acquire(ctx)
	if rawErr == nil {
		conn.Release()
		t.Fatal("all-primary acquisition succeeded")
	}
	pool.Close()
	parent.End()
	var connectErr *pgconn.ConnectError
	if !errors.As(rawErr, &connectErr) {
		t.Fatalf("expected stock connection error, got %T", rawErr)
	}
	rawText := rawErr.Error()
	for _, marker := range []string{"reader-host-canary.invalid", "reader-user-canary", "reader-db-canary", "127.0.0.1"} {
		if !strings.Contains(rawText, marker) {
			t.Fatalf("raw error fixture is missing private metadata %q", marker)
		}
	}
	redacted := RedactOSMConnectionError(rawErr)
	if redacted.Error() != "OSM database connection unavailable" || errors.As(redacted, &connectErr) || rawErr.Error() != rawText {
		t.Fatal("redactor retained connection fields or mutated the raw driver error")
	}
	var metrics metricdata.ResourceMetrics
	if err := metricsReader.Collect(context.Background(), &metrics); err != nil {
		t.Fatal(err)
	}
	serialized := fmt.Sprint(metrics, redacted)
	seen := map[string]bool{}
	for _, span := range recorder.Ended() {
		serialized += fmt.Sprint(span.Name(), span.Attributes(), span.Events(), span.Status())
		if span.Name() == "connect" || span.Name() == "pool.acquire" {
			seen[span.Name()] = true
			if len(span.Events()) != 1 || span.Status().Description != "OSM database connection unavailable" {
				t.Fatalf("connection span %s did not retain a sanitized error event/status", span.Name())
			}
		}
	}
	for _, forbidden := range []string{"reader-host-canary.invalid", "reader-user-canary", "reader-db-canary", "password-canary", "127.0.0.1", first.listener.Addr().String(), second.listener.Addr().String()} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("reader telemetry/redacted error exposed %q", forbidden)
		}
	}
	if !seen["connect"] || !seen["pool.acquire"] || first.roleReads.Load() != 1 || second.roleReads.Load() != 1 {
		t.Fatal("missing connection/acquisition telemetry or strict all-node role checks")
	}
}

func TestOSMReaderRedactConnectionErrorIsNarrow(t *testing.T) {
	workloadErr := &pgconn.PgError{Code: "22003", Message: "keep workload diagnostics", Detail: "keep workload detail"}
	for _, err := range []error{nil, workloadErr, fmt.Errorf("workload: %w", workloadErr), context.Canceled, context.DeadlineExceeded, errors.New("pgxpool: too many failed attempts acquiring connection")} {
		if RedactOSMConnectionError(err) != err {
			t.Fatal("redactor changed an error without a driver connection error")
		}
	}
	server := newOSMReaderWireServer(t, "standby", true, nil)
	for _, test := range []struct {
		name  string
		cause error
	}{
		{name: "cancel", cause: context.Canceled},
		{name: "deadline", cause: context.DeadlineExceeded},
		{name: "SQLSTATE", cause: &pgconn.PgError{Code: "42501", Message: "password-canary", Detail: "password-canary", Where: "password-canary"}},
		{name: "safe SQLSTATE", cause: safeOSMReaderError("check OSM reader role", &pgconn.PgError{Code: "42501", Message: "password-canary"})},
		{name: "generic", cause: errors.New("password-canary")},
	} {
		t.Run(test.name, func(t *testing.T) {
			config, err := pgconn.ParseConfig(osmReaderTestDSN(server))
			if err != nil {
				t.Fatal(err)
			}
			config.ValidateConnect = func(context.Context, *pgconn.PgConn) error { return test.cause }
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			conn, rawErr := pgconn.ConnectConfig(ctx, config)
			if rawErr == nil {
				conn.Close(ctx)
				t.Fatal("fixture did not reject the connection")
			}
			err = RedactOSMConnectionError(fmt.Errorf("outer password-canary: %w", rawErr))
			for _, forbidden := range []string{"password-canary", "reader-private", "127.0.0.1"} {
				if strings.Contains(err.Error(), forbidden) {
					t.Fatalf("redactor exposed %q", forbidden)
				}
			}
			if strings.Contains(test.name, "SQLSTATE") && !strings.Contains(err.Error(), "SQLSTATE 42501") {
				t.Fatal("redactor lost safe SQLSTATE")
			}
			if (test.name == "cancel" || test.name == "deadline") && !errors.Is(err, test.cause) {
				t.Fatal("redactor lost context error identity")
			}
			var connectErr *pgconn.ConnectError
			var pgErr *pgconn.PgError
			if errors.As(err, &connectErr) || errors.As(err, &pgErr) {
				t.Fatal("redactor retained raw driver error fields")
			}
		})
	}
}

func TestOSMReaderRoleProbeValidatesResult(t *testing.T) {
	server := newOSMReaderWireServer(t, "standby", true, nil)
	pool, err := OpenOSMReader(context.Background(), osmReaderTestDSN(server), "reader-role-result", 1, true)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	for _, test := range []struct {
		name    string
		rows    [][][]byte
		standby bool
		invalid bool
	}{
		{name: "standby", rows: [][][]byte{{[]byte("t")}}, standby: true},
		{name: "primary", rows: [][][]byte{{[]byte("f")}}},
		{name: "no rows", invalid: true},
		{name: "multiple rows", rows: [][][]byte{{[]byte("t")}, {[]byte("t")}}, invalid: true},
		{name: "no columns", rows: [][][]byte{{}}, invalid: true},
		{name: "multiple columns", rows: [][][]byte{{[]byte("t"), []byte("t")}}, invalid: true},
		{name: "null", rows: [][][]byte{{nil}}, invalid: true},
		{name: "invalid boolean", rows: [][][]byte{{[]byte("password-canary")}}, invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server.roleResult.Store(&pgconn.Result{Rows: test.rows})
			standby, err := checkOSMStandby(ctx, conn.Conn().PgConn())
			if standby != test.standby || (err != nil) != test.invalid {
				t.Fatalf("standby=%v err=%v, want standby=%v invalid=%v", standby, err, test.standby, test.invalid)
			}
			if err != nil && strings.Contains(err.Error(), "password-canary") {
				t.Fatal("invalid role result leaked raw data")
			}
		})
	}
}

func TestOSMReaderDynamicDNSAndVerifiedTLS(t *testing.T) {
	serverTLS, tlsParams := osmReaderTestTLS(t)
	primary := newOSMReaderWireServer(t, "primary", false, serverTLS)
	first := newOSMReaderWireServer(t, "first", true, serverTLS)
	second := newOSMReaderWireServer(t, "second", true, serverTLS)
	config, err := pgxpool.ParseConfig("host=all-nodes.reader.test user=reader-private dbname=reader-private " + tlsParams)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	addresses := []string{primary.listener.Addr().String(), first.listener.Addr().String(), second.listener.Addr().String()}
	original := slices.Clone(addresses)
	var lookups atomic.Int32
	var updated atomic.Bool
	config.ConnConfig.LookupFunc = func(ctx context.Context, host string) ([]string, error) {
		if host != "all-nodes.reader.test" || ctx.Err() != nil {
			t.Error("lookup lost original hostname or context")
		}
		lookups.Add(1)
		if updated.Load() {
			return []string{second.listener.Addr().String()}, nil
		}
		return addresses, nil
	}
	configureOSMReadReplicas(config)
	// Every DNS result is complete, shuffled without mutating the resolver's slice.
	orders := map[string]bool{}
	for range 64 {
		got, err := config.ConnConfig.LookupFunc(context.Background(), "all-nodes.reader.test")
		if err != nil {
			t.Fatal(err)
		}
		orders[strings.Join(got, ",")] = true
		slices.Sort(got)
		want := slices.Clone(original)
		slices.Sort(want)
		if !slices.Equal(got, want) || !slices.Equal(addresses, original) {
			t.Fatal("DNS candidates were dropped or resolver-owned slice mutated")
		}
	}
	if len(orders) < 2 {
		t.Fatal("DNS candidates were not shuffled")
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var node string
	if err := conn.QueryRow(ctx, "SELECT reader_node()", pgx.QueryExecModeExec).Scan(&node); err != nil || node == "primary" {
		t.Fatalf("headless DNS selected %q: %v", node, err)
	}
	if conn.Conn().PgConn().Conn().(*tls.Conn).ConnectionState().ServerName != "all-nodes.reader.test" {
		t.Fatal("DNS routing lost TLS server name")
	}
	physical := conn.Hijack()
	if err := physical.Close(ctx); err != nil {
		t.Fatal(err)
	}
	priorLookups := lookups.Load()
	updated.Store(true)
	conn, err = pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if err := conn.QueryRow(ctx, "SELECT reader_node()", pgx.QueryExecModeExec).Scan(&node); err != nil || node != "second" || lookups.Load() <= priorLookups {
		t.Fatalf("DNS did not refresh for new connection: %q, %v", node, err)
	}
}

func TestOSMReaderTLSAndFallbackConfigurationUnchanged(t *testing.T) {
	_, tlsParams := osmReaderTestTLS(t)
	for _, mode := range []string{"verify-full", "prefer", "allow"} {
		t.Run(mode, func(t *testing.T) {
			config, err := pgxpool.ParseConfig("host=all-nodes.reader.test,other.reader.test user=reader-private dbname=reader-private " + strings.Replace(tlsParams, "sslmode=verify-full", "sslmode="+mode, 1) + " connect_timeout=2")
			if err != nil {
				t.Fatal(err)
			}
			before := *config.ConnConfig
			configureOSMReadReplicas(config)
			if !reflect.DeepEqual(before.Fallbacks, config.ConnConfig.Fallbacks) ||
				before.TLSConfig != config.ConnConfig.TLSConfig ||
				before.Host != config.ConnConfig.Host || before.Port != config.ConnConfig.Port ||
				config.ConnConfig.ConnectTimeout != 2*time.Second {
				t.Fatal("reader changed TLS/host fallback configurations or explicit timeout")
			}
			if mode == "verify-full" {
				for _, tlsConfig := range []*tls.Config{config.ConnConfig.TLSConfig, config.ConnConfig.Fallbacks[0].TLSConfig} {
					if tlsConfig == nil || tlsConfig.InsecureSkipVerify || tlsConfig.RootCAs == nil || len(tlsConfig.Certificates) != 1 {
						t.Fatal("verify-full lost CA, client certificate, or verification")
					}
				}
				if config.ConnConfig.TLSConfig.ServerName != "all-nodes.reader.test" || config.ConnConfig.Fallbacks[0].TLSConfig.ServerName != "other.reader.test" {
					t.Fatal("fallback TLS hostname changed")
				}
			}
			for _, forbidden := range []string{"sslmode", "sslhost", "target_session_attrs"} {
				if _, ok := config.ConnConfig.RuntimeParams[forbidden]; ok {
					t.Fatalf("driver parameter %s became a server runtime parameter", forbidden)
				}
			}
		})
	}
}

func TestOSMReaderHeadlessDNSChecksEveryNode(t *testing.T) {
	var servers []*osmReaderWireServer
	for _, name := range []string{"first", "second", "third"} {
		servers = append(servers, newOSMReaderWireServer(t, name, false, nil))
	}
	config, err := pgxpool.ParseConfig("host=all-nodes.reader.test user=reader-private dbname=reader-private sslmode=disable target_session_attrs=prefer-standby")
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.LookupFunc = func(context.Context, string) ([]string, error) {
		var addresses []string
		for _, server := range servers {
			addresses = append(addresses, server.listener.Addr().String())
		}
		return addresses, nil
	}
	configureOSMReadReplicas(config)
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if conn, err := pool.Acquire(ctx); err == nil {
		conn.Release()
		t.Fatal("all-primary headless DNS did not fail closed")
	}
	for _, server := range servers {
		if server.roleReads.Load() != 1 {
			t.Fatalf("DNS candidate %s checked %d times, want once", server.name, server.roleReads.Load())
		}
	}
}

func TestOSMReaderVerifiedTLSRejectsWrongHostname(t *testing.T) {
	serverTLS, tlsParams := osmReaderTestTLS(t)
	server := newOSMReaderWireServer(t, "standby", true, serverTLS)
	config, err := pgxpool.ParseConfig("host=wrong.reader.test user=reader-private dbname=reader-private " + tlsParams)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.LookupFunc = func(context.Context, string) ([]string, error) {
		return []string{server.listener.Addr().String()}, nil
	}
	configureOSMReadReplicas(config)
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if conn, err := pool.Acquire(ctx); err == nil {
		conn.Release()
		t.Fatal("verify-full accepted a certificate for another hostname")
	}
	if server.roleReads.Load() != 0 || len(config.ConnConfig.Fallbacks) != 0 {
		t.Fatal("TLS failure bypassed verification or used a plaintext fallback")
	}
}

func TestOSMReaderLookupUsesParentDeadline(t *testing.T) {
	config, err := pgxpool.ParseConfig("host=all-nodes.reader.test user=reader-private dbname=reader-private sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.LookupFunc = func(ctx context.Context, _ string) ([]string, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > osmReaderRoleTimeout {
			t.Error("DNS lookup is not bounded")
		}
		<-ctx.Done()
		return nil, fmt.Errorf("password-canary: %w", ctx.Err())
	}
	configureOSMReadReplicas(config)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = config.ConnConfig.LookupFunc(ctx, "all-nodes.reader.test")
	if !errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "canary") {
		t.Fatalf("lookup did not preserve safe parent deadline: %v", err)
	}
}

func TestOSMReaderSafeErrorsAndDNSFailures(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		if _, err := OpenOSMReader(context.Background(), "postgres://reader-private:password-canary@%zz", "reader-error", 0, enabled); err == nil || strings.Contains(err.Error(), "canary") || strings.Contains(err.Error(), "reader-private") {
			t.Fatalf("unsafe parse error: %v", err)
		}
	}
	for _, raw := range []error{
		errors.New("reader-private password-canary"),
		&pgconn.PgError{Code: "42501", Message: "reader-private password-canary", Detail: "password-canary", Where: "password-canary"},
		&pgconn.PgError{Code: "canary", Message: "password-canary"},
		fmt.Errorf("password-canary: %w", context.Canceled),
		fmt.Errorf("password-canary: %w", context.DeadlineExceeded),
	} {
		err := safeOSMReaderError("check OSM reader role", raw)
		if strings.Contains(fmt.Sprintf("%+v", err), "canary") || strings.Contains(err.Error(), "reader-private") {
			t.Fatal("safe error retained private fields")
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			t.Fatal("safe error unwraps private PostgreSQL fields")
		}
	}
	for _, lookupErr := range []error{nil, errors.New("reader-private password-canary")} {
		config, err := pgxpool.ParseConfig("host=all-nodes.reader.test user=reader-private dbname=reader-private sslmode=disable")
		if err != nil {
			t.Fatal(err)
		}
		config.ConnConfig.LookupFunc = func(context.Context, string) ([]string, error) { return nil, lookupErr }
		configureOSMReadReplicas(config)
		if addresses, err := config.ConnConfig.LookupFunc(context.Background(), "all-nodes.reader.test"); len(addresses) != 0 || err == nil || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "canary") {
			t.Fatalf("unsafe/empty DNS result accepted: %v", err)
		}
		if config.ConnConfig.ConnectTimeout != osmReaderRoleTimeout || config.PingTimeout != osmReaderRoleTimeout || config.BeforeAcquire != nil {
			t.Fatal("missing bounded connect/ping or using deprecated acquisition hook")
		}
	}
}

// These fixtures implement only the protocol exercised by reader role checks,
// transactions, and node identity. They never connect to an external database.
type osmReaderWireServer struct {
	listener    net.Listener
	name        string
	tlsConfig   *tls.Config
	standby     atomic.Bool
	failRole    atomic.Bool
	roleResult  atomic.Pointer[pgconn.Result]
	roleDelay   atomic.Int64
	roleReads   atomic.Int32
	namedParses atomic.Int32
	startups    chan map[string]string
	done        chan struct{}
	acceptDone  chan struct{}
	mu          sync.Mutex
	connections []net.Conn
	wg          sync.WaitGroup
}

func newOSMReaderWireServer(t *testing.T, name string, standby bool, tlsConfig *tls.Config) *osmReaderWireServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &osmReaderWireServer{listener: listener, name: name, tlsConfig: tlsConfig, startups: make(chan map[string]string, 32), done: make(chan struct{}), acceptDone: make(chan struct{})}
	server.standby.Store(standby)
	server.wg.Add(1)
	go func() {
		defer server.wg.Done()
		defer close(server.acceptDone)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			server.mu.Lock()
			server.connections = append(server.connections, conn)
			server.mu.Unlock()
			server.wg.Add(1)
			go func() {
				defer server.wg.Done()
				defer conn.Close()
				server.serve(conn)
			}()
		}
	}()
	t.Cleanup(func() {
		close(server.done)
		listener.Close()
		// Wait for the accept loop before taking the final connections snapshot.
		<-server.acceptDone
		server.mu.Lock()
		for _, conn := range server.connections {
			conn.Close()
		}
		server.mu.Unlock()
		server.wg.Wait()
	})
	return server
}

func (s *osmReaderWireServer) serve(conn net.Conn) {
	backend := pgproto3.NewBackend(conn, conn)
	startup, err := backend.ReceiveStartupMessage()
	if err != nil {
		return
	}
	if _, ok := startup.(*pgproto3.SSLRequest); ok {
		if s.tlsConfig == nil {
			conn.Write([]byte("N"))
		} else {
			if _, err := conn.Write([]byte("S")); err != nil {
				return
			}
			tlsConn := tls.Server(conn, s.tlsConfig)
			if err := tlsConn.Handshake(); err != nil {
				return
			}
			conn = tlsConn
		}
		backend = pgproto3.NewBackend(conn, conn)
		startup, err = backend.ReceiveStartupMessage()
		if err != nil {
			return
		}
	}
	message, ok := startup.(*pgproto3.StartupMessage)
	if !ok {
		return // Including out-of-band CancelRequest messages.
	}
	select {
	case s.startups <- message.Parameters:
	case <-s.done:
		return
	}
	backend.Send(&pgproto3.AuthenticationOk{})
	backend.Send(&pgproto3.ParameterStatus{Name: "server_version", Value: "17.0"})
	backend.Send(&pgproto3.ParameterStatus{Name: "client_encoding", Value: "UTF8"})
	backend.Send(&pgproto3.ParameterStatus{Name: "standard_conforming_strings", Value: "on"})
	backend.Send(&pgproto3.BackendKeyData{ProcessID: 1, SecretKey: []byte{0, 0, 0, 1}})
	status := byte('I')
	backend.Send(&pgproto3.ReadyForQuery{TxStatus: status})
	if backend.Flush() != nil {
		return
	}
	var query string
	rowDescription := func() {
		oid, size := uint32(25), int16(-1)
		if strings.Contains(query, "pg_is_in_recovery") {
			oid, size = 16, 1
		}
		backend.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("value"), DataTypeOID: oid, DataTypeSize: size, TypeModifier: -1}}})
	}
	execute := func(simple bool) bool {
		lower := strings.ToLower(query)
		switch {
		case strings.Contains(lower, "pg_is_in_recovery"):
			s.roleReads.Add(1)
			if delay := time.Duration(s.roleDelay.Load()); delay > 0 {
				select {
				case <-time.After(delay):
				case <-s.done:
					return false
				}
			}
			if s.failRole.Load() {
				backend.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "42501", Message: "reader-private password-canary", Detail: "password-canary", Where: "reader-private"})
				return true
			}
			if simple {
				rowDescription()
			}
			value := "f"
			if s.standby.Load() {
				value = "t"
			}
			if result := s.roleResult.Load(); result != nil {
				for _, row := range result.Rows {
					backend.Send(&pgproto3.DataRow{Values: row})
				}
			} else {
				backend.Send(&pgproto3.DataRow{Values: [][]byte{[]byte(value)}})
			}
			backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
		case strings.Contains(lower, "reader_node"):
			if simple {
				rowDescription()
			}
			backend.Send(&pgproto3.DataRow{Values: [][]byte{[]byte(s.name)}})
			backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
		case strings.Contains(lower, "reader_workload_error"):
			backend.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "22003", Message: "synthetic workload error"})
		case strings.HasPrefix(lower, "begin"):
			status = 'T'
			backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("BEGIN")})
		case strings.HasPrefix(lower, "rollback"):
			status = 'I'
			backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("ROLLBACK")})
		default:
			backend.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
		}
		return true
	}
	for {
		message, err := backend.Receive()
		if err != nil {
			return
		}
		switch message := message.(type) {
		case *pgproto3.Parse:
			query = message.Query
			if message.Name != "" {
				s.namedParses.Add(1)
			}
			backend.Send(&pgproto3.ParseComplete{})
		case *pgproto3.Bind:
			backend.Send(&pgproto3.BindComplete{})
		case *pgproto3.Describe:
			if message.ObjectType == 'S' {
				backend.Send(&pgproto3.ParameterDescription{})
			}
			rowDescription()
		case *pgproto3.Execute:
			if !execute(false) {
				return
			}
		case *pgproto3.Sync:
			backend.Send(&pgproto3.ReadyForQuery{TxStatus: status})
		case *pgproto3.Query:
			query = message.String
			if !execute(true) {
				return
			}
			backend.Send(&pgproto3.ReadyForQuery{TxStatus: status})
		case *pgproto3.Close:
			backend.Send(&pgproto3.CloseComplete{})
		case *pgproto3.Terminate:
			return
		}
		if backend.Flush() != nil {
			return
		}
	}
}

func osmReaderTestDSN(servers ...*osmReaderWireServer) string {
	var hosts, ports []string
	for _, server := range servers {
		host, port, _ := net.SplitHostPort(server.listener.Addr().String())
		hosts, ports = append(hosts, host), append(ports, port)
	}
	return "host=" + strings.Join(hosts, ",") + " port=" + strings.Join(ports, ",") + " user=reader-private password=password-canary dbname=reader-private sslmode=disable"
}

func osmReaderTestTLS(t *testing.T) (*tls.Config, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "reader-test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		DNSNames: []string{"all-nodes.reader.test", "other.reader.test"},
		IsCA:     true, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	dir := t.TempDir()
	certPath, keyPath := filepath.Join(dir, "reader.crt"), filepath.Join(dir, "reader.key")
	if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		t.Fatal(err)
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(certPEM)
	return &tls.Config{Certificates: []tls.Certificate{cert}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: roots, MinVersion: tls.VersionTLS12},
		fmt.Sprintf("sslmode=verify-full sslrootcert='%s' sslcert='%s' sslkey='%s'", certPath, certPath, keyPath)
}
