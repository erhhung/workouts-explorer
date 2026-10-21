package worker

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/erhhung/workouts-explorer/internal/healthautoexport"
	"github.com/google/uuid"
)

func TestEligibleCohortReadinessPreservesParserDomain(t *testing.T) {
	limit := healthautoexport.DefaultLimits().MaxRoutePoints
	encoded := bytes.NewBuffer(make([]byte, 0, limit*6+1))
	encoded.WriteByte('[')
	for i := 0; i < limit; i++ {
		if i > 0 {
			encoded.WriteByte(',')
		}
		encoded.WriteString("[1,2]")
	}
	encoded.WriteByte(']')
	points, err := decodeCoverageCoordinates(encoded.Bytes(), limit)
	if err != nil || len(points) != 250_000 || points[limit-1].Longitude != 1 || points[limit-1].Latitude != 2 {
		t.Fatalf("readiness must admit the entire parser domain, not the matcher CPU bound: count=%d err=%v", len(points), err)
	}
	for _, count := range []int{-1, limit + 1, limit - 1} {
		if _, err := decodeCoverageCoordinates(encoded.Bytes(), count); err == nil {
			t.Fatalf("accepted invalid snapshot count %d", count)
		}
	}
}

func TestEligibleCohortPreparedDrainStopsBeforeCheckout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := drainPreparedIngestCoverage(ctx, nil, nil, uuid.New(), nil, "test", uuid.New(), new(int64), coverageReplayOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("prepared drain ignored cancellation: %v", err)
	}
	if err := drainPreparedIngestCoverage(context.Background(), nil, nil, uuid.New(), nil, "test", uuid.New(), new(int64),
		coverageReplayOptions{renew: func(context.Context) error { return errLeaseLost }}); !errors.Is(err, errLeaseLost) {
		t.Fatalf("prepared drain ignored authority loss: %v", err)
	}
}
