package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestAdoptionCoverageCancellationBeforeCheckout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, reconciliation := range []*int64{nil, new(int64)} {
		err := replayIngestCoverage(ctx, nil, nil, uuid.New(), uuid.New(), "adoption-test", uuid.New(), reconciliation, 25, true, coverageReplayOptions{})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled replay error = %v", err)
		}
	}
}

func TestAdoptionCoverageRenewalLossBeforeCheckout(t *testing.T) {
	renewals := 0
	err := replayIngestCoverage(context.Background(), nil, nil, uuid.New(), uuid.Nil, "adoption-test", uuid.New(), new(int64), 25, true,
		coverageReplayOptions{renew: func(context.Context) error {
			renewals++
			return errLeaseLost
		}})
	if !errors.Is(err, errLeaseLost) || renewals != 1 {
		t.Fatalf("lost renewal error=%v renewals=%d", err, renewals)
	}
}
