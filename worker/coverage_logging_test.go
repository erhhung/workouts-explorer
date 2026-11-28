package worker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

type coverageLogBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *coverageLogBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(data)
}

func (b *coverageLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

func TestCoverageCycleErrorsShareThirtySecondCadence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var output coverageLogBuffer
		runner := NewCoverageRunnerWithOptions(nil, nil, slog.New(slog.NewJSONHandler(&output, nil)), CoverageRunnerOptions{
			JobConcurrency: 2, PollInterval: time.Second,
		})
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var attempts atomic.Int32
		done := make(chan error, 1)
		go func() {
			done <- runner.runWorkers(ctx, func(context.Context) (bool, error) {
				attempts.Add(1)
				return false, errors.New("coverage OSM reader preflight failed")
			})
		}()
		synctest.Wait()
		if attempts.Load() != 2 || strings.Count(output.String(), "coverage worker cycle failed") != 1 {
			t.Fatalf("first failure: attempts=%d log=%s", attempts.Load(), output.String())
		}
		time.Sleep(29 * time.Second)
		synctest.Wait()
		if attempts.Load() != 60 || strings.Count(output.String(), "coverage worker cycle failed") != 1 {
			t.Fatalf("polling was delayed or errors flooded: attempts=%d log=%s", attempts.Load(), output.String())
		}
		time.Sleep(time.Second)
		synctest.Wait()
		lines := strings.Split(strings.TrimSpace(output.String()), "\n")
		if attempts.Load() != 62 || len(lines) != 2 {
			t.Fatalf("30-second boundary: attempts=%d log=%s", attempts.Load(), output.String())
		}
		first, second := decodeLogLine(t, lines[0]), decodeLogLine(t, lines[1])
		if first["suppressed_errors"] != float64(0) || second["suppressed_errors"] != float64(59) ||
			second["error"] != "coverage OSM reader preflight failed" {
			t.Fatalf("unexpected suppression accounting: first=%v second=%v", first, second)
		}
		cancel()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func TestCoverageIdleRetryLoopDoesNotResetErrorCadence(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var output coverageLogBuffer
		runner := NewCoverageRunner(nil, nil, slog.New(slog.NewJSONHandler(&output, nil)))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var attempts atomic.Int32
		done := make(chan struct{})
		go func() {
			runner.runWorker(ctx, func(context.Context) (bool, error) {
				attempts.Add(1)
				return false, nil
			})
			close(done)
		}()
		runner.logCycleError(errors.New("coverage OSM reader preflight failed"))
		time.Sleep(10 * time.Second)
		synctest.Wait()
		runner.logCycleError(errors.New("automatic coverage retry unavailable"))
		if attempts.Load() < 10 || strings.Count(output.String(), "coverage worker cycle failed") != 1 {
			t.Fatalf("idle loop reset shared cadence: attempts=%d log=%s", attempts.Load(), output.String())
		}
		time.Sleep(20 * time.Second)
		runner.logCycleError(errors.New("coverage OSM reader preflight failed"))
		lines := strings.Split(strings.TrimSpace(output.String()), "\n")
		if len(lines) != 2 || decodeLogLine(t, lines[1])["suppressed_errors"] != float64(1) {
			t.Fatalf("unexpected retry-loop suppression: %s", output.String())
		}
		cancel()
		<-done
	})
}

func TestCoverageThrottledCycleErrorsRemainRedacted(t *testing.T) {
	var output coverageLogBuffer
	runner := NewCoverageRunner(nil, nil, slog.New(slog.NewJSONHandler(&output, nil)))
	runner.logCycleError(redactedCoverageReaderError(t, io.EOF))
	for _, private := range []string{"canary", "127.0.0.1", "postgres://", "postgresql://"} {
		if strings.Contains(output.String(), private) {
			t.Fatalf("cycle log exposes %q", private)
		}
	}
	if strings.Count(output.String(), "coverage worker cycle failed") != 1 {
		t.Fatalf("first failure was not logged: %s", output.String())
	}
}
