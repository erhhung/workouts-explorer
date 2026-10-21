package database

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
)

const osmRedactionPrivate = "password-boundary-canary reader-host-canary.invalid provider-boundary-canary route-boundary-canary SELECT boundary_canary ST_GeomFromText('LINESTRING(1 2,3 4)')"

// A real pgx ConnectError, including its private Config and multi-address join,
// produced entirely in memory. No DNS lookup, socket, or database is used.
func osmRedactionConnectError(t *testing.T, failures ...error) error {
	t.Helper()
	config, err := pgconn.ParseConfig("host=reader-host-canary.invalid user=reader-user-canary password=password-boundary-canary dbname=reader-db-canary sslmode=disable")
	if err != nil {
		t.Fatal("parse synthetic connection configuration")
	}
	config.LookupFunc = func(context.Context, string) ([]string, error) {
		addresses := make([]string, len(failures))
		for i := range addresses {
			addresses[i] = fmt.Sprintf("192.0.2.%d", i+1)
		}
		return addresses, nil
	}
	config.DialFunc = func(context.Context, string, string) (net.Conn, error) {
		wire, _ := (&pgproto3.AuthenticationOk{}).Encode(nil)
		wire, _ = (&pgproto3.ReadyForQuery{TxStatus: 'I'}).Encode(wire)
		return &osmRedactionConn{Reader: bytes.NewReader(wire)}, nil
	}
	attempt := 0
	config.ValidateConnect = func(context.Context, *pgconn.PgConn) error {
		failure := failures[attempt]
		attempt++
		return fmt.Errorf("%s: %w", osmRedactionPrivate, failure)
	}
	conn, raw := pgconn.ConnectConfig(context.Background(), config)
	if raw == nil {
		_ = conn.Close(context.Background())
		t.Fatal("synthetic connection unexpectedly succeeded")
	}
	var driver *pgconn.ConnectError
	if !errors.As(raw, &driver) || driver.Config.Password != "password-boundary-canary" {
		t.Fatal("fixture lacks actual private driver configuration")
	}
	return raw
}

type osmRedactionConn struct{ *bytes.Reader }

func (*osmRedactionConn) Write(p []byte) (int, error)      { return len(p), nil }
func (*osmRedactionConn) Close() error                     { return nil }
func (*osmRedactionConn) LocalAddr() net.Addr              { return &net.TCPAddr{} }
func (*osmRedactionConn) RemoteAddr() net.Addr             { return &net.TCPAddr{} }
func (*osmRedactionConn) SetDeadline(time.Time) error      { return nil }
func (*osmRedactionConn) SetReadDeadline(time.Time) error  { return nil }
func (*osmRedactionConn) SetWriteDeadline(time.Time) error { return nil }

func assertOSMRedactionPrivate(t *testing.T, safe error) {
	t.Helper()
	queue := []error{safe}
	for len(queue) != 0 {
		current := queue[0]
		queue = queue[1:]
		text := current.Error() + fmt.Sprintf("%v %+v %#v %s %q", current, current, current, current, current)
		for _, forbidden := range []string{"canary", "192.0.2.", "postgres://", "postgresql://", "SELECT", "ST_GeomFromText", "LINESTRING"} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("sanitized %T exposes private marker %q", current, forbidden)
			}
		}
		switch current.(type) {
		case *osmReaderError, *osmReaderSQLStateError, *osmReaderTransientError:
		default:
			if current != context.Canceled && current != context.DeadlineExceeded && current.Error() != "unclassified OSM connection failure" {
				t.Fatalf("unwrap retained non-sanitized %T", current)
			}
		}
		switch wrapped := current.(type) {
		case interface{ Unwrap() []error }:
			queue = append(queue, wrapped.Unwrap()...)
		case interface{ Unwrap() error }:
			if child := wrapped.Unwrap(); child != nil {
				queue = append(queue, child)
			}
		}
	}
}

func TestOSMReaderRedactionMultiAddressPermissionAndDeadline(t *testing.T) {
	permission := &pgconn.PgError{Code: "42501", Message: osmRedactionPrivate, Detail: osmRedactionPrivate, InternalQuery: osmRedactionPrivate}
	raw := osmRedactionConnectError(t, context.DeadlineExceeded, permission)
	for _, input := range []error{
		raw,
		fmt.Errorf("%s: %w", osmRedactionPrivate, raw),
		errors.Join(context.DeadlineExceeded, fmt.Errorf("%s: %w", osmRedactionPrivate, raw)),
		errors.Join(fmt.Errorf("%s: %w", osmRedactionPrivate, raw), context.DeadlineExceeded),
	} {
		safe := RedactOSMConnectionError(input)
		for range 3 {
			assertOSMRedactionPrivate(t, safe)
			var state interface{ SQLState() string }
			if !errors.As(safe, &state) || state.SQLState() != "42501" {
				t.Fatal("sanitization lost safe permission SQLSTATE")
			}
			if !errors.Is(safe, context.DeadlineExceeded) || IsRetryableOSMConnectionError(safe) {
				t.Fatal("incidental deadline erased permanent permission failure")
			}
			if safe.Error() != "OSM database connection unavailable (SQLSTATE 42501)" {
				t.Fatalf("unexpected public error: %v", safe)
			}
			safe = RedactOSMConnectionError(osmRedactionConnectError(t, safe))
		}
	}
}

func TestOSMReaderRedactionAllBranches(t *testing.T) {
	timeout := &net.DNSError{Name: "reader-host-canary.invalid", Err: osmRedactionPrivate, IsTimeout: true}
	for _, test := range []struct {
		name  string
		cause error
		code  string
	}{
		{"permission", &pgconn.PgError{Code: "42501", Message: osmRedactionPrivate}, "42501"},
		{"authentication", &pgconn.PgError{Code: "28P01", Message: osmRedactionPrivate}, "28P01"},
		{"schema", &pgconn.PgError{Code: "42P01", Message: osmRedactionPrivate}, "42P01"},
		{"protocol state", &pgconn.PgError{Code: "08P01", Message: osmRedactionPrivate}, "08P01"},
		{"invalid state", &pgconn.PgError{Code: "a4250", Message: osmRedactionPrivate}, ""},
		{"control in state", &pgconn.PgError{Code: "42\n01", Message: osmRedactionPrivate}, ""},
		{"punctuation in state", &pgconn.PgError{Code: "42'01", Message: osmRedactionPrivate}, ""},
		{"oversize state", &pgconn.PgError{Code: osmRedactionPrivate}, ""},
		{"unknown", errors.New(osmRedactionPrivate), ""},
		{"cancel", context.Canceled, ""},
		{"unknown CA", x509.UnknownAuthorityError{Cert: &x509.Certificate{DNSNames: []string{"reader-host-canary.invalid"}}}, ""},
		{"TLS wrapper", &tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}, ""},
		{"TLS through network", &net.OpError{Op: "remote error", Err: x509.UnknownAuthorityError{}}, ""},
		{"TLS pointer through network", &net.OpError{Op: "remote error", Err: &tls.RecordHeaderError{Msg: osmRedactionPrivate}}, ""},
		{"TLS record", tls.RecordHeaderError{Msg: osmRedactionPrivate}, ""},
		{"configuration", pgconn.NewParseConfigError(osmRedactionPrivate, osmRedactionPrivate, context.DeadlineExceeded), ""},
		{"invalid address", &net.AddrError{Err: osmRedactionPrivate, Addr: osmRedactionPrivate}, ""},
		{"network joined unknown", &net.OpError{Op: "dial", Err: errors.Join(errors.New(osmRedactionPrivate), timeout)}, ""},
		{"URL joined unknown", &url.Error{Op: "connect", URL: osmRedactionPrivate, Err: errors.Join(timeout, errors.New(osmRedactionPrivate))}, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, transient := range []error{context.DeadlineExceeded, timeout, io.EOF, &pgconn.PgError{Code: "08006"}} {
				for _, reverse := range []bool{false, true} {
					causes := []error{transient, fmt.Errorf("%s: %w", osmRedactionPrivate, test.cause)}
					if reverse {
						causes[0], causes[1] = causes[1], causes[0]
					}
					raw := osmRedactionConnectError(t, errors.Join(causes...))
					safe := RedactOSMConnectionError(raw)
					assertOSMRedactionPrivate(t, safe)
					if IsRetryableOSMConnectionError(raw) || IsRetryableOSMConnectionError(safe) {
						t.Fatal("transient branch overrode permanent cause")
					}
					if test.code != "" {
						var state interface{ SQLState() string }
						if !errors.As(safe, &state) || state.SQLState() != test.code {
							t.Fatal("errors.As did not select permanent safe SQLSTATE")
						}
					}
					if test.name == "cancel" && !errors.Is(safe, context.Canceled) {
						t.Fatal("sanitization lost cancellation identity")
					}
				}
			}
		})
	}
}

func TestOSMReaderRedactionPureTransientCompatibility(t *testing.T) {
	for _, cause := range []error{
		context.DeadlineExceeded, io.EOF, pgconn.ErrConnClosed,
		&net.DNSError{Name: "reader-host-canary.invalid", Err: osmRedactionPrivate, IsTimeout: true},
		&net.OpError{Op: "dial", Err: &net.DNSError{Name: "reader-host-canary.invalid", IsTimeout: true}},
		&net.OpError{Op: "dial", Err: errors.New(osmRedactionPrivate)},
		&url.Error{Op: "connect", URL: osmRedactionPrivate, Err: context.DeadlineExceeded},
		&pgconn.PgError{Code: "08006", Message: osmRedactionPrivate},
		errors.Join(context.DeadlineExceeded, &pgconn.PgError{Code: "57P03", Message: osmRedactionPrivate}),
	} {
		safe := RedactOSMConnectionError(osmRedactionConnectError(t, cause))
		assertOSMRedactionPrivate(t, safe)
		if !IsRetryableOSMConnectionError(safe) {
			t.Fatalf("lost pure transient classification for %T", cause)
		}
		if errors.Is(cause, context.DeadlineExceeded) && !errors.Is(safe, context.DeadlineExceeded) {
			t.Fatal("lost pure deadline identity")
		}
		var network net.Error
		if errors.As(cause, &network) && network.Timeout() {
			if !errors.As(safe, &network) || !network.Timeout() {
				t.Fatal("lost pure network timeout class")
			}
		}
		if !errors.Is(cause, context.DeadlineExceeded) {
			if _, sql := cause.(*pgconn.PgError); !sql {
				var classified interface{ OSMConnectionFailureClass() string }
				if !errors.As(safe, &classified) || classified.OSMConnectionFailureClass() != "transient" {
					t.Fatal("safe transport leaf lost sanitized provenance")
				}
			}
		}
	}
	cancel := RedactOSMConnectionError(osmRedactionConnectError(t, context.Canceled))
	if !errors.Is(cancel, context.Canceled) || IsRetryableOSMConnectionError(cancel) {
		t.Fatal("lost pure cancellation semantics")
	}
}

type osmRedactionCycle struct{ next error }

func (*osmRedactionCycle) Error() string   { return osmRedactionPrivate }
func (e *osmRedactionCycle) Unwrap() error { return e.next }

type osmRedactionSliceError []error

func (osmRedactionSliceError) Error() string     { return osmRedactionPrivate }
func (e osmRedactionSliceError) Unwrap() []error { return e }

type osmRedactionInterfaceError struct{ value any }

func (osmRedactionInterfaceError) Error() string { return osmRedactionPrivate }

type osmRedactionCountingError struct {
	calls *int
	data  []byte // Force a noncomparable value error.
}

func (osmRedactionCountingError) Error() string { return osmRedactionPrivate }
func (e osmRedactionCountingError) Unwrap() error {
	*e.calls += 1
	return e
}

func TestOSMReaderRedactionBoundedGraphs(t *testing.T) {
	cycle := &osmRedactionCycle{}
	cycle.next = cycle
	sliceCycle := make(osmRedactionSliceError, 1)
	sliceCycle[0] = sliceCycle
	var deep error = context.DeadlineExceeded
	for range 300 {
		deep = fmt.Errorf("%s: %w", osmRedactionPrivate, deep)
	}
	wide := make([]error, 1000)
	for i := range wide {
		wide[i] = context.DeadlineExceeded
	}
	var typedNil *net.OpError
	for _, cause := range []error{
		cycle, sliceCycle, deep, errors.Join(wide...), typedNil,
		osmRedactionSliceError(make([]error, 1000)),
		osmRedactionSliceError{context.DeadlineExceeded, errors.New(osmRedactionPrivate)},
		osmRedactionInterfaceError{value: []string{osmRedactionPrivate}},
	} {
		// Construct the driver error before attaching pathological graphs: pgx's
		// own errors.As is unbounded and must not traverse a cycle in the fixture.
		raw := osmRedactionConnectError(t, context.DeadlineExceeded)
		safe := RedactOSMConnectionError(errors.Join(cause, raw))
		assertOSMRedactionPrivate(t, safe)
		if IsRetryableOSMConnectionError(safe) {
			t.Fatalf("incomplete/unknown graph %T became transient", cause)
		}
	}
	permission := &pgconn.PgError{Code: "42501", Message: osmRedactionPrivate}
	safe := safeOSMReaderError("OSM database connection unavailable", errors.Join(cycle, permission))
	var state interface{ SQLState() string }
	if !errors.As(safe, &state) || state.SQLState() != "42501" {
		t.Fatal("cycle prevented classification of sibling permission branch")
	}
	shared := fmt.Errorf("%s: %w", osmRedactionPrivate, context.DeadlineExceeded)
	if !IsRetryableOSMConnectionError(errors.Join(shared, shared)) {
		t.Fatal("shared acyclic subgraph was mistaken for a cycle")
	}
	calls := 0
	safe = safeOSMReaderError("OSM database connection unavailable", osmRedactionCountingError{calls: &calls})
	if calls != 256 || IsRetryableOSMConnectionError(safe) {
		t.Fatalf("noncomparable cycle: visited=%d, want 256 and fail closed", calls)
	}
	assertOSMRedactionPrivate(t, safe)
}
