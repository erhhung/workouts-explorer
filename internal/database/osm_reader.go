package database

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"reflect"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const osmReaderRoleTimeout = 5 * time.Second
const osmReaderRoleSQL = "SELECT pg_catalog.pg_is_in_recovery()"

// OpenOSMReader opens a lazy OSM pool. With useReadReplicasOnly, databaseURL
// should name the all-node headless DNS service (or list all possible hosts).
// Role is checked at connection and checkout, not during an acquired connection
// or transaction: promotion cannot reroute an in-flight transaction.
// DNS shuffling spreads new connections, not individual jobs, among standbys.
// Callers must use RedactOSMConnectionError before exposing acquisition errors;
// connection/acquisition telemetry is sanitized, but pgxpool returns raw errors.
func OpenOSMReader(ctx context.Context, databaseURL, applicationName string, maxConns int32, useReadReplicasOnly bool) (*pgxpool.Pool, error) {
	if !useReadReplicasOnly {
		return OpenWithMaxConns(ctx, databaseURL, applicationName, maxConns)
	}

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, errors.New("parse OSM reader configuration")
	}
	config.ConnConfig.RuntimeParams["application_name"] = applicationName
	config.ConnConfig.Tracer = &osmReaderTracer{Tracer: newTracer()}
	if maxConns > 0 {
		config.MaxConns = maxConns
	}
	configureOSMReadReplicas(config)
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, errors.New("create OSM reader pool")
	}
	if err := recordPoolStats(pool, applicationName); err != nil {
		pool.Close()
		return nil, errors.New("register OSM reader pool metrics")
	}
	return pool, nil
}

// RedactOSMConnectionError removes private details from driver connection errors
// before logging them. Other errors, including workload SQL errors, are unchanged.
// Its bounded unwrap graph contains only safe classification metadata; consumers
// must inspect all branches rather than letting an incidental deadline win. An
// uninspectable (cyclic or over-budget) graph is also redacted and fails closed.
func RedactOSMConnectionError(err error) error {
	safe, connection := sanitizeOSMReaderError("OSM database connection unavailable", err)
	if connection {
		return safe
	}
	return err
}

// IsRetryableOSMConnectionError recognizes raw and sanitized connection failures.
// Callers must check their parent context before retrying a bounded reader timeout.
// Authentication, permissions, workload SQL errors and cancellation are not retried.
// Every branch must be retryable; unknown or incomplete branches fail closed.
func IsRetryableOSMConnectionError(err error) bool {
	safe, _ := sanitizeOSMReaderError("OSM database connection unavailable", err)
	causes := safe.causes
	if len(causes) == 0 {
		return false
	}
	for _, cause := range causes {
		switch cause := cause.(type) {
		case *osmReaderSQLStateError:
			if !retryableOSMReaderSQLState(cause.code) {
				return false
			}
		case net.Error:
			// Only sanitized transport classes and the deadline sentinel occur here.
		default:
			return false
		}
	}
	return true
}

type osmReaderTracer struct {
	*otelpgx.Tracer
}

func (t *osmReaderTracer) TraceConnectEnd(ctx context.Context, data pgx.TraceConnectEndData) {
	if data.Err != nil {
		data.Err = safeOSMReaderError("OSM database connection unavailable", data.Err)
	}
	t.Tracer.TraceConnectEnd(ctx, data)
}

func (t *osmReaderTracer) TraceAcquireEnd(ctx context.Context, pool *pgxpool.Pool, data pgxpool.TraceAcquireEndData) {
	if data.Err != nil {
		data.Err = safeOSMReaderError("OSM database connection unavailable", data.Err)
	}
	t.Tracer.TraceAcquireEnd(ctx, pool, data)
}

func configureOSMReadReplicas(config *pgxpool.Config) {
	connConfig := config.ConnConfig
	connConfig.RuntimeParams["default_transaction_read_only"] = "on"
	// Replace, rather than chain, URI target_session_attrs validation. In
	// particular, prefer-standby's NotPreferredError would permit primary fallback.
	connConfig.ValidateConnect = func(ctx context.Context, conn *pgconn.PgConn) error {
		standby, err := checkOSMStandby(ctx, conn)
		if err != nil {
			return err
		}
		if !standby {
			return errors.New("OSM reader requires a standby")
		}
		return nil
	}
	lookup := connConfig.LookupFunc
	connConfig.LookupFunc = func(ctx context.Context, host string) ([]string, error) {
		ctx, cancel := context.WithTimeout(ctx, osmReaderRoleTimeout)
		defer cancel()
		addresses, err := lookup(ctx, host)
		if err != nil {
			return nil, safeOSMReaderError("resolve OSM reader nodes", err)
		}
		if len(addresses) == 0 {
			return nil, errors.New("resolve OSM reader nodes: no addresses")
		}
		// Leave host/TLS fallback configurations intact, including their order.
		// pgx resolves again for each new connection and retains the TLS hostname.
		addresses = append([]string(nil), addresses...)
		rand.Shuffle(len(addresses), func(i, j int) {
			addresses[i], addresses[j] = addresses[j], addresses[i]
		})
		return addresses, nil
	}
	// pgxpool constructs connections in the background after Acquire cancellation.
	// Bound each address attempt even when the URL has no connect_timeout.
	if connConfig.ConnectTimeout <= 0 {
		connConfig.ConnectTimeout = osmReaderRoleTimeout
	}
	if config.PingTimeout <= 0 {
		config.PingTimeout = osmReaderRoleTimeout
	}
	config.PrepareConn = func(ctx context.Context, conn *pgx.Conn) (bool, error) {
		// false,nil destroys a promoted connection and retries acquisition. pgxpool
		// bounds these retries; a failed role read instead terminates this acquisition.
		return checkOSMStandby(ctx, conn.PgConn())
	}
}

func checkOSMStandby(ctx context.Context, conn *pgconn.PgConn) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, osmReaderRoleTimeout)
	defer cancel()
	// Unnamed extended protocol avoids prepared statements and pgx query tracing,
	// which would record a raw server error before we could sanitize it.
	result := conn.ExecParams(ctx, osmReaderRoleSQL, nil, nil, nil, nil).Read()
	if result.Err != nil {
		return false, safeOSMReaderError("check OSM reader role", result.Err)
	}
	if len(result.Rows) != 1 || len(result.Rows[0]) != 1 {
		return false, errors.New("check OSM reader role: invalid result")
	}
	switch string(result.Rows[0][0]) {
	case "t":
		return true, nil
	case "f":
		return false, nil
	default:
		return false, errors.New("check OSM reader role: invalid result")
	}
}

type osmReaderSQLStateError struct {
	message string
	code    string
}

func (e *osmReaderSQLStateError) Error() string {
	return fmt.Sprintf("%s (SQLSTATE %s)", e.message, e.code)
}

func (e *osmReaderSQLStateError) SQLState() string { return e.code }

type osmReaderTransientError struct {
	message string
	timeout bool
}

func (e *osmReaderTransientError) Error() string   { return e.message }
func (e *osmReaderTransientError) Temporary() bool { return true }
func (e *osmReaderTransientError) Timeout() bool   { return e.timeout }

// OSMConnectionFailureClass identifies sanitized transport metadata, unlike an
// arbitrary net.Error supplied by unrelated validation or application code.
func (*osmReaderTransientError) OSMConnectionFailureClass() string { return "transient" }

// osmReaderError is a neutral wrapper. Consumers must inspect every child:
// interface{ SQLState() string } carries validated codes, net.Error carries only
// transport metadata (with OSMConnectionFailureClass() == "transient"), and an
// opaque leaf means unknown, not retryable. No child retains a raw error,
// connection configuration, address, or server message.
type osmReaderError struct {
	message string
	causes  []error
}

func (e *osmReaderError) Error() string {
	var code string
	for _, cause := range e.causes {
		if sql, ok := cause.(*osmReaderSQLStateError); ok {
			if code == "" || retryableOSMReaderSQLState(code) && !retryableOSMReaderSQLState(sql.code) {
				code = sql.code
			}
		}
	}
	if code != "" {
		return fmt.Sprintf("%s (SQLSTATE %s)", e.message, code)
	}
	if len(e.causes) == 1 && (e.causes[0] == context.Canceled || e.causes[0] == context.DeadlineExceeded) {
		return e.message + ": " + e.causes[0].Error()
	}
	return e.message
}

func (e *osmReaderError) Unwrap() []error { return e.causes }

func retryableOSMReaderSQLState(code string) bool {
	return len(code) == 5 && code[:2] == "08" && code != "08P01" || code == "57P01" || code == "57P02" || code == "57P03"
}

func safeOSMReaderError(message string, err error) error {
	safe, _ := sanitizeOSMReaderError(message, err)
	return safe
}

func sanitizeOSMReaderError(message string, err error) (*osmReaderError, bool) {
	const maxNodes = 256
	safe := &osmReaderError{message: message}
	connection, incomplete := false, false
	remaining := maxNodes
	active := make(map[error]bool)
	unknown := func() { safe.causes = append(safe.causes, errors.New("unclassified OSM connection failure")) }
	transient := func(timeout bool) {
		leaf := &osmReaderTransientError{message: message, timeout: timeout}
		if timeout {
			// errors.As(net.Error) must not hide a timeout behind an EOF sibling.
			safe.causes = append([]error{leaf}, safe.causes...)
		} else {
			safe.causes = append(safe.causes, leaf)
		}
	}
	var walk func(error, net.Error)
	walk = func(err error, transport net.Error) {
		if remaining == 0 {
			incomplete = true
			return
		}
		remaining--
		if err == nil {
			return
		}
		value := reflect.ValueOf(err)
		if value.Kind() == reflect.Pointer && value.IsNil() {
			unknown()
			return
		}
		// Value.Comparable also checks interface fields containing slices/maps.
		// Noncomparable value errors are still bounded by the shared node budget.
		if value.Comparable() {
			if active[err] {
				incomplete = true
				return
			}
			active[err] = true
			defer delete(active, err)
		}
		if _, ok := err.(*pgconn.ConnectError); ok {
			connection = true
		}
		classified := false
		switch err.(type) {
		case *pgconn.ParseConfigError, *tls.CertificateVerificationError,
			tls.RecordHeaderError, *tls.RecordHeaderError, tls.AlertError, *tls.AlertError,
			x509.UnknownAuthorityError, *x509.UnknownAuthorityError,
			x509.CertificateInvalidError, *x509.CertificateInvalidError,
			x509.HostnameError, *x509.HostnameError, x509.SystemRootsError, *x509.SystemRootsError,
			x509.InsecureAlgorithmError, *x509.InsecureAlgorithmError,
			x509.ConstraintViolationError, *x509.ConstraintViolationError,
			x509.UnhandledCriticalExtension, *x509.UnhandledCriticalExtension,
			*net.AddrError, *net.ParseError, net.InvalidAddrError, *net.InvalidAddrError,
			net.UnknownNetworkError, *net.UnknownNetworkError:
			unknown()
			classified = true
		}
		if sql, ok := err.(interface{ SQLState() string }); ok {
			code := sql.SQLState()
			valid := len(code) == 5
			for i := 0; valid && i < len(code); i++ {
				valid = code[i] >= '0' && code[i] <= '9' || code[i] >= 'A' && code[i] <= 'Z'
			}
			if valid {
				leaf := &osmReaderSQLStateError{message: message, code: code}
				// Put permanent SQLSTATEs first for existing errors.As callers too.
				if !retryableOSMReaderSQLState(code) {
					safe.causes = append([]error{leaf}, safe.causes...)
				} else {
					safe.causes = append(safe.causes, leaf)
				}
			} else {
				unknown()
			}
			classified = true
		}
		hasChild := false
		switch wrapped := err.(type) {
		case interface{ Unwrap() []error }:
			for _, child := range wrapped.Unwrap() {
				// Joined leaves are independent failures, never incidental socket text.
				hasChild = hasChild || child != nil
				walk(child, nil)
				if remaining == 0 {
					// Fail closed even when the unvisited tail contains only nils.
					incomplete = true
					break
				}
			}
		case interface{ Unwrap() error }:
			if network, ok := err.(*net.OpError); ok {
				// An OpError's opaque syscall/dial leaf describes the transport
				// failure itself. Still inspect structured causes for SQL/TLS/cancel.
				transport = network
			}
			child := wrapped.Unwrap()
			hasChild = child != nil
			walk(child, transport)
		}
		if classified || hasChild {
			return
		}
		switch err {
		case context.Canceled:
			safe.causes = append(safe.causes, err)
		case context.DeadlineExceeded:
			safe.causes = append([]error{context.DeadlineExceeded}, safe.causes...)
		case io.EOF, pgconn.ErrConnClosed:
			transient(false)
		default:
			if network, ok := err.(net.Error); ok {
				transient(network.Timeout())
			} else if transport != nil {
				transient(transport.Timeout())
			} else {
				unknown()
			}
		}
	}
	walk(err, nil)
	if incomplete {
		unknown()
	}
	return safe, connection || incomplete
}
