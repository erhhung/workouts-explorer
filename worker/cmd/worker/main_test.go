package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSupervisorTreatsActiveComponentExitAsFatal(t *testing.T) {
	cancelled := make(chan struct{})
	err := supervise(context.Background(), time.Second,
		supervisedComponent{name: "runner", run: func(context.Context) error { return nil }},
		supervisedComponent{name: "scheduler", run: func(ctx context.Context) error {
			<-ctx.Done()
			close(cancelled)
			return nil
		}},
	)
	if err == nil || !strings.Contains(err.Error(), "runner stopped unexpectedly") {
		t.Fatalf("unexpected supervisor result: %v", err)
	}
	select {
	case <-cancelled:
	default:
		t.Fatal("peer component was not cancelled")
	}
}

func TestSupervisorReturnsFirstComponentError(t *testing.T) {
	want := errors.New("database unavailable")
	err := supervise(context.Background(), time.Second,
		supervisedComponent{name: "scheduler", run: func(context.Context) error { return want }},
		supervisedComponent{name: "server", run: func(ctx context.Context) error { <-ctx.Done(); return nil }},
	)
	if !errors.Is(err, want) {
		t.Fatalf("supervisor error=%v", err)
	}
}

func TestSupervisorExternalCancellationIsClean(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := supervise(ctx, time.Second,
		supervisedComponent{name: "runner", run: func(ctx context.Context) error { <-ctx.Done(); return nil }},
		supervisedComponent{name: "scheduler", run: func(ctx context.Context) error { <-ctx.Done(); return nil }},
	)
	if err != nil {
		t.Fatalf("clean shutdown error=%v", err)
	}
}

func TestSupervisorBoundsShutdown(t *testing.T) {
	release := make(chan struct{})
	err := supervise(context.Background(), 10*time.Millisecond,
		supervisedComponent{name: "server", run: func(context.Context) error { return nil }},
		supervisedComponent{name: "stuck", run: func(context.Context) error { <-release; return nil }},
	)
	close(release)
	if err == nil || err.Error() != "worker shutdown timed out" {
		t.Fatalf("timeout result=%v", err)
	}
}
