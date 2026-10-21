package worker

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestCoveragePipelineBatchSize(t *testing.T) {
	for _, size := range []int{-1, 0, 1, 10, 25, 1000, 1001} {
		runner := NewRunnerWithOptions(nil, slog.Default(), nil, nil, RunnerOptions{CoverageBatchSize: size})
		want := size
		if size < 1 || size > 1000 {
			want = 25
		}
		if runner.coverageBatchSize != want {
			t.Fatalf("size=%d got=%d want=%d", size, runner.coverageBatchSize, want)
		}
		if size < 1 || size > 1000 {
			if err := ConfigureCoverageBatchSize(context.Background(), nil, size); err == nil {
				t.Fatal("invalid startup policy reached database")
			}
		}
	}
}

func TestCoveragePipelineStopsBeforeCheckout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, revision := range []*int64{nil, new(int64)} {
		if err := replayIngestCoverage(ctx, nil, nil, uuid.New(), uuid.New(), "test", uuid.New(), revision, 25, false,
			coverageReplayOptions{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled replay: %v", err)
		}
	}
	if err := replayIngestCoverage(context.Background(), nil, nil, uuid.New(), uuid.Nil, "test", uuid.New(), new(int64), 25, true,
		coverageReplayOptions{renew: func(context.Context) error { return errLeaseLost }}); !errors.Is(err, errLeaseLost) {
		t.Fatalf("lost lease replay: %v", err)
	}
}

func TestCoveragePipelineAuthorityErrorsPreserveCause(t *testing.T) {
	for _, code := range []string{"42501", "40001", "XX000"} {
		cause := &pgconn.PgError{Code: code}
		err := coverageHandoffError(cause)
		if !errors.Is(err, cause) || errors.Is(err, errLeaseLost) != (code == "42501") {
			t.Fatalf("code=%s error=%v", code, err)
		}
	}
}

func TestCoveragePipelineTypedTransientErrors(t *testing.T) {
	for _, cause := range []error{context.DeadlineExceeded, osm.ErrMatcherReplicaNotReady,
		&pgconn.PgError{Code: "08006"}, &pgconn.PgError{Code: "53300"}} {
		if !transientCoverageReadinessError(cause) {
			t.Fatalf("typed readiness failure must defer durably: %v", cause)
		}
	}
	for _, cause := range []error{context.Canceled, errors.New("unknown"), &pgconn.PgError{Code: "XX000"},
		&pgconn.PgError{Code: "42501"}, &pgconn.PgError{Code: "42P01"}} {
		if transientCoverageReadinessError(cause) {
			t.Fatalf("unknown/permanent/cancelled failure cannot become success: %v", cause)
		}
	}
}

type readinessUnknownTimeout struct{}

func (readinessUnknownTimeout) Error() string   { return "unclassified timeout" }
func (readinessUnknownTimeout) Timeout() bool   { return true }
func (readinessUnknownTimeout) Temporary() bool { return true }

type readinessCyclicError struct{}

func (err *readinessCyclicError) Error() string { return "cyclic readiness error" }
func (err *readinessCyclicError) Unwrap() error { return err }

func TestCoveragePipelineReadinessEveryBranchMustBeSafe(t *testing.T) {
	for _, cause := range []error{nil, context.Canceled, errors.New("unknown readiness cause"), readinessUnknownTimeout{},
		(*pgconn.PgError)(nil), (*net.DNSError)(nil), (*readinessCyclicError)(nil),
		&tls.RecordHeaderError{Msg: "invalid TLS"}, &net.DNSError{IsNotFound: true}, &net.DNSError{IsTimeout: true},
		&pgconn.PgError{Code: "42501"}, &pgconn.PgError{Code: "28P01"}, &pgconn.PgError{Code: "08004"},
		&pgconn.PgError{Code: "08P01"}, &pgconn.PgError{Code: "XX000"}, &pgconn.PgError{Code: "invalid"}, &readinessCyclicError{}} {
		if cause == nil {
			if transientCoverageReadinessError(nil) {
				t.Fatal("nil is not a transient failure")
			}
			continue
		}
		for _, joined := range []error{cause,
			errors.Join(cause, context.DeadlineExceeded, osm.ErrMatcherReplicaNotReady),
			errors.Join(context.DeadlineExceeded, osm.ErrMatcherReplicaNotReady, cause),
			errors.Join(fmt.Errorf("neutral wrapper: %w", cause), &pgconn.PgError{Code: "08006"})} {
			if transientCoverageReadinessError(joined) {
				t.Fatalf("unsafe branch %T became a successful durable deferral", cause)
			}
		}
	}
	for _, cause := range []error{context.DeadlineExceeded, osm.ErrMatcherReplicaNotReady, io.EOF,
		&pgconn.PgError{Code: "08003"}, &pgconn.PgError{Code: "08006"}, &pgconn.PgError{Code: "08007"},
		&pgconn.PgError{Code: "40001"}, &pgconn.PgError{Code: "40P01"}, &pgconn.PgError{Code: "55P03"},
		&pgconn.PgError{Code: "57P01"}, &pgconn.PgError{Code: "57P02"}, &pgconn.PgError{Code: "57P03"}, &pgconn.PgError{Code: "53300"}} {
		if !transientCoverageReadinessError(fmt.Errorf("neutral wrapper: %w", errors.Join(cause, osm.ErrMatcherReplicaNotReady))) {
			t.Fatalf("known safe readiness branch %T was lost", cause)
		}
	}
	if transientCoverageReadinessError(&pgconn.PgError{Code: "57014"}) {
		t.Fatal("unexplained query cancellation is not a deadline")
	}
	if !transientCoverageReadinessError(errors.Join(&pgconn.PgError{Code: "57014"}, context.DeadlineExceeded)) {
		t.Fatal("a proven operation deadline lost timeout identity")
	}
	children := make([]error, 257)
	for i := range children {
		children[i] = io.EOF
	}
	if transientCoverageReadinessError(errors.Join(children...)) {
		t.Fatal("over-budget readiness error tree must fail closed")
	}
}

func TestCoveragePipelineAPPContentionBackoff(t *testing.T) {
	for _, code := range []string{"40001", "55P03", "40P01"} {
		cause := &pgconn.PgError{Code: code}
		for retries := 0; retries < 2; retries++ {
			started := time.Now()
			if err := waitCoverageHandoffRetry(context.Background(), fmt.Errorf("APP boundary: %w", cause), retries); err != nil {
				t.Fatalf("code=%s retry=%d: %v", code, retries, err)
			}
			if elapsed := time.Since(started); elapsed < 25*time.Millisecond<<retries {
				t.Fatalf("contention retried without backoff: code=%s retry=%d elapsed=%s", code, retries, elapsed)
			}
		}
		if err := waitCoverageHandoffRetry(context.Background(), cause, 2); err != cause {
			t.Fatalf("exhaustion lost original typed error: %v", err)
		}
	}
	for _, cause := range []error{errors.New("unknown APP failure"), context.Canceled, context.DeadlineExceeded,
		&pgconn.PgError{Code: "42501"}, &pgconn.PgError{Code: "08006"}, &pgconn.PgError{Code: "57014"},
		&pgconn.PgError{Code: "XX000"}, &readinessCyclicError{},
		errors.Join(&pgconn.PgError{Code: "55P03"}, errors.New("unknown branch")),
		errors.Join(&pgconn.PgError{Code: "40P01"}, &pgconn.PgError{Code: "42501"})} {
		if err := waitCoverageHandoffRetry(context.Background(), cause, 0); err != cause {
			t.Fatalf("unclassified/permanent APP error was retried or replaced: %T", cause)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := time.AfterFunc(time.Millisecond, cancel)
	defer stop.Stop()
	if err := waitCoverageHandoffRetry(ctx, &pgconn.PgError{Code: "55P03"}, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("backoff ignored cancellation: %v", err)
	}
}
