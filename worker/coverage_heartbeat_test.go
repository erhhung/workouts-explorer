package worker

import (
	"context"
	"testing"
	"time"
)

func TestCoverageHeartbeatCanStopWithoutCancelingTheOperation(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	control := newCoverageHeartbeatControl()
	runner := &CoverageRunner{heartbeatInterval: time.Hour}
	go runner.heartbeat(ctx, coverageClaim{}, cancel, control.done, control.stop)
	finished := make(chan struct{})
	go func() {
		control.stopAndWait()
		control.stopAndWait()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("stopping the heartbeat waited for its next tick or consumed completion twice")
	}
	if ctx.Err() != nil {
		t.Fatal("heartbeat shutdown canceled the operation before persistence")
	}
}

func TestCoverageHeartbeatStopWaitsForInFlightControlCleanup(t *testing.T) {
	control := newCoverageHeartbeatControl()
	active := make(chan struct{})
	release := make(chan struct{})
	go func() {
		close(active)
		<-control.stop
		<-release
		control.done <- nil
	}()
	<-active
	finished := make(chan struct{})
	go func() { control.stopAndWait(); close(finished) }()
	<-control.stop
	select {
	case <-finished:
		t.Fatal("persistence could start while its previous heartbeat was still cleaning up")
	default:
	}
	close(release)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("heartbeat cleanup did not release persistence")
	}
}
