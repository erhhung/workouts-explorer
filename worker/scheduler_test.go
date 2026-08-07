package worker

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestScheduledIngestKeyIsNormalizedAndGenerationBound(t *testing.T) {
	first := scheduledSource{id: uuid.MustParse("00000000-0000-0000-0000-000000000001"), generation: 1}
	second := scheduledSource{id: uuid.MustParse("00000000-0000-0000-0000-000000000002"), generation: 1}
	key := scheduledIngestKey([]scheduledSource{first, second})
	if key != scheduledIngestKey([]scheduledSource{first, second}) {
		t.Fatal("scheduled ingest key is not deterministic")
	}
	second.generation++
	if key == scheduledIngestKey([]scheduledSource{first, second}) {
		t.Fatal("scheduled ingest key is not generation-bound")
	}
	if bytes.Contains(key[:], first.id[:]) {
		t.Fatal("scheduled ingest key exposes a source identifier")
	}
}

func TestSchedulerLoopCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cycles := 0
	err := runSchedulerLoop(ctx, time.Hour, func(context.Context) (bool, error) {
		cycles++
		cancel()
		return false, context.Canceled
	}, func(error) { t.Fatal("cancellation was reported as a cycle failure") })
	if err != nil || cycles != 1 {
		t.Fatalf("cancelled loop cycles=%d err=%v", cycles, err)
	}
}

func TestSafeSchedulerErrorRedactsDetails(t *testing.T) {
	err := safeSchedulerError(errors.New("decrypt /private/source/path: secret envelope"))
	if err == nil || err.Error() != "scheduler operation failed" {
		t.Fatalf("unsafe scheduler error: %v", err)
	}
	if safeSchedulerError(nil) != nil {
		t.Fatal("nil scheduler error was changed")
	}
}
