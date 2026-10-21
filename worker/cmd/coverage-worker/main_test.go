package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSuperviseCancelsAndJoinsEveryComponent(t *testing.T) {
	for _, stop := range []string{"parent", "failure", "unexpected_return"} {
		t.Run(stop, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			failure := errors.New("fixture component failed")
			trigger := make(chan struct{})
			cleaning := make(chan error, 2)
			cleanup := make(chan struct{})
			defer close(cleanup)
			waiting := func(ctx context.Context) error {
				<-ctx.Done()
				cleaning <- context.Cause(ctx)
				<-cleanup
				return nil
			}
			done := make(chan error, 1)
			go func() {
				done <- supervise(ctx, waiting, waiting, func(ctx context.Context) error {
					select {
					case <-ctx.Done():
						return nil
					case <-trigger:
						if stop == "failure" {
							return failure
						}
						return nil
					}
				})
			}()
			if stop == "parent" {
				cancel()
			} else {
				close(trigger)
			}
			for range 2 {
				select {
				case cause := <-cleaning:
					if stop == "failure" && !errors.Is(cause, failure) {
						t.Fatalf("cancellation cause=%v, want component failure", cause)
					}
				case <-time.After(time.Second):
					t.Fatal("component was not cancelled")
				}
			}
			select {
			case <-done:
				t.Fatal("supervisor returned before all components finished cleanup")
			default:
			}
			cleanup <- struct{}{}
			cleanup <- struct{}{}
			select {
			case err := <-done:
				if stop == "parent" && err != nil || stop == "failure" && !errors.Is(err, failure) || stop == "unexpected_return" && err == nil {
					t.Fatalf("supervisor result=%v for %s", err, stop)
				}
			case <-time.After(time.Second):
				t.Fatal("supervisor did not join components")
			}
		})
	}
}
