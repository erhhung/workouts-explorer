package worker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestCoverageReplicaPreflightDoesNotClaim(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	pool, err := pgxpool.New(context.Background(), "postgres://fixture-private-user:fixture-private-password@127.0.0.1:1/fixture-private-db?sslmode=disable&connect_timeout=1")
	if err != nil {
		t.Fatal("create lazy test pool")
	}
	defer pool.Close()
	for _, parent := range []context.Context{context.Background(), ctx} {
		for _, reader := range []*pgxpool.Pool{nil, pool} {
			runner := NewCoverageRunnerWithOptions(nil, reader, slog.New(slog.NewTextHandler(io.Discard, nil)), CoverageRunnerOptions{UseReadReplicasOnly: true})
			// A nil app DB proves no claim query is attempted after preflight fails.
			worked, err := runner.RunOnce(parent)
			if worked || err == nil || err.Error() != "coverage OSM reader preflight failed" {
				t.Fatalf("preflight worked=%v error=%v", worked, err)
			}
		}
	}
}

func TestRecoverableCoverageReaderErrors(t *testing.T) {
	for _, test := range []struct {
		err  error
		want bool
	}{
		{osm.ErrMatcherReplicaNotReady, false},
		{&net.OpError{Op: "dial", Err: errors.New("fixture network outage")}, true},
		{&pgconn.PgError{Code: "08006"}, true},
		{&pgconn.PgError{Code: "57P01"}, true},
		{&pgconn.PgError{Code: "57P02"}, true},
		{&pgconn.PgError{Code: "57P03"}, true},
		{&pgconn.PgError{Code: "42501"}, false},
		{&pgconn.PgError{Code: "28P01"}, false},
		{&pgconn.PgError{Code: "22000"}, false},
		{errors.New("OSM reader requires a standby"), true},
		{errors.Join(errors.New("dial wrapper"), errors.New("OSM reader requires a standby")), true},
		{errors.New("check OSM reader role (SQLSTATE 42501)"), false},
		{errors.New("resolve OSM reader nodes"), false},
		{io.EOF, true},
		{pgconn.ErrConnClosed, true},
		{errors.New("invalid generation metadata"), false},
		{errors.Join(&pgconn.PgError{Code: "42501"}, errors.New("OSM reader requires a standby")), false},
		{errors.Join(context.Canceled, &net.OpError{Op: "dial", Err: errors.New("fixture network outage")}), false},
	} {
		if got := recoverableCoverageReaderError(fmt.Errorf("snapshot: %w", test.err)); got != test.want {
			t.Errorf("recoverable(%T)=%v, want %v", test.err, got, test.want)
		}
	}
}

// Produce stock driver connection wrappers without DNS, sockets or a database.
// The public redactor exercises the same sanitizer used by reader role probes.
func redactedCoverageReaderError(t testing.TB, cause error) error {
	t.Helper()
	config, err := pgconn.ParseConfig("host=reader-host-canary.invalid user=reader-user-canary password=reader-password-canary dbname=reader-db-canary sslmode=disable")
	if err != nil {
		t.Fatal("create synthetic reader connection configuration")
	}
	config.LookupFunc = func(context.Context, string) ([]string, error) { return []string{"127.0.0.1"}, nil }
	config.DialFunc = func(context.Context, string, string) (net.Conn, error) {
		return nil, fmt.Errorf("reader-private-detail-canary: %w", cause)
	}
	conn, raw := pgconn.ConnectConfig(context.Background(), config)
	if raw == nil {
		_ = conn.Close(context.Background())
		t.Fatal("synthetic connection failure unexpectedly succeeded")
	}
	return database.RedactOSMConnectionError(raw)
}

func TestCoverageReplicaSanitizedReaderClassification(t *testing.T) {
	for _, test := range []struct {
		name    string
		cause   error
		retry   bool
		timeout bool
	}{
		{"shutdown", &pgconn.PgError{Code: "57P01", Message: "reader-private-detail-canary"}, true, false},
		{"crash", &pgconn.PgError{Code: "57P02"}, true, false},
		{"starting", &pgconn.PgError{Code: "57P03"}, true, false},
		{"connection failure", &pgconn.PgError{Code: "08006"}, true, false},
		{"authentication", &pgconn.PgError{Code: "28P01", Detail: "reader-password-canary"}, false, false},
		{"authorization", &pgconn.PgError{Code: "28000"}, false, false},
		{"permission", &pgconn.PgError{Code: "42501"}, false, false},
		{"invalid query", &pgconn.PgError{Code: "42601"}, false, false},
		{"metadata", &pgconn.PgError{Code: "22000"}, false, false},
		{"EOF", io.EOF, true, false},
		{"closed connection", pgconn.ErrConnClosed, true, false},
		{"network", &net.OpError{Op: "dial", Err: errors.New("reader-host-canary.invalid reader-password-canary")}, true, false},
		{"network timeout", &net.DNSError{Name: "reader-host-canary.invalid", Err: "reader-password-canary", IsTimeout: true}, true, true},
		{"EOF with network timeout", errors.Join(io.EOF, &net.DNSError{Name: "reader-host-canary.invalid", IsTimeout: true}), true, true},
		{"cancelled", context.Canceled, false, false},
		{"bounded timeout", context.DeadlineExceeded, true, true},
		{"unknown", errors.New("reader-private-detail-canary"), false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			safe := redactedCoverageReaderError(t, test.cause)
			// Sanitization may occur at role-check, driver connect, telemetry
			// and caller boundaries. Every pass must retain safe classification.
			for range 2 {
				if got := recoverableCoverageReaderError(fmt.Errorf("snapshot: %w", safe)); got != test.retry {
					t.Fatalf("worker retry=%v, want %v for %T", got, test.retry, safe)
				}
				if got := database.IsRetryableOSMConnectionError(safe); got != test.retry {
					t.Fatalf("reader retry=%v, want %v", got, test.retry)
				}
				var network net.Error
				if test.timeout && (!errors.As(safe, &network) || !network.Timeout()) {
					t.Fatal("sanitization lost timeout metadata")
				}
				var rawSQL *pgconn.PgError
				var rawConnect *pgconn.ConnectError
				var rawNetwork *net.OpError
				var rawDNS *net.DNSError
				if errors.As(safe, &rawSQL) || errors.As(safe, &rawConnect) || errors.As(safe, &rawNetwork) || errors.As(safe, &rawDNS) {
					t.Fatal("safe error retained raw private driver fields")
				}
				for _, private := range []string{"canary", "127.0.0.1", "postgres://", "postgresql://"} {
					if strings.Contains(fmt.Sprintf("%+v", safe), private) {
						t.Fatalf("safe error exposes %q", private)
					}
				}
				if sql, ok := test.cause.(interface{ SQLState() string }); ok {
					var retained interface{ SQLState() string }
					if !errors.As(safe, &retained) || retained.SQLState() != sql.SQLState() {
						t.Fatal("sanitization lost SQLSTATE interface")
					}
					if !test.retry {
						mixed := errors.Join(safe, io.EOF, errors.New("OSM reader requires a standby"))
						if database.IsRetryableOSMConnectionError(mixed) || recoverableCoverageReaderError(mixed) {
							t.Fatal("retryable leaf overrides a fatal SQLSTATE")
						}
					}
				} else if test.retry {
					var temporary interface{ Temporary() bool }
					if !errors.As(safe, &temporary) || !temporary.Temporary() {
						t.Fatal("safe connection error lost transient classification")
					}
				}
				if errors.Is(test.cause, context.Canceled) || errors.Is(test.cause, context.DeadlineExceeded) {
					if !errors.Is(safe, test.cause) {
						t.Fatal("sanitization lost context identity")
					}
				}
				safe = redactedCoverageReaderError(t, safe)
			}
		})
	}
}

func TestCoverageReplicaSnapshotWaitStopsOnParentContext(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	cancel(errLeaseLost)
	runner := &CoverageRunner{useReadReplicasOnly: true}
	started := time.Now()
	_, err := runner.beginMatcherSnapshot(ctx, coverageClaim{}, uuid.New(), []osm.MatcherGeneration{{RegionID: "fixture:region", GenerationID: 2}})
	if !errors.Is(err, errLeaseLost) || time.Since(started) > time.Second {
		t.Fatalf("snapshot interruption=%v duration=%s", err, time.Since(started))
	}
}
