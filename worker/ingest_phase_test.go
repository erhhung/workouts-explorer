package worker

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIngestPhaseCoverageNoOp(t *testing.T) {
	ctx := context.Background()
	runner := &Runner{} // Any accidental database write panics.
	id := uuid.New()
	for _, job := range []claimedJob{
		{},
		{ingestPhase: &ingestPhaseProgress{phase: phaseAnalyzingImportedRoutes, imported: 7, resolvedImports: map[uuid.UUID]bool{id: true}}},
		{ingestPhase: &ingestPhaseProgress{phase: phaseAnalyzingExistingRoutes, existing: 25}},
	} {
		for range 1671 {
			if err := runner.ingestCoverageStarted(ctx, job); err != nil {
				t.Fatal(err)
			}
			if err := runner.ingestCoverageResolved(ctx, job, id); err != nil {
				t.Fatal(err)
			}
		}
		if job.ingestPhase != nil && job.ingestPhase.phase == phaseAnalyzingExistingRoutes && job.ingestPhase.existing != 25 {
			t.Fatal("nested drain reset existing-route progress")
		}
	}
}

func TestIngestPhaseWriteFailureDoesNotAdvanceState(t *testing.T) {
	ctx := context.Background()
	// A closed lazy pool exercises beginAccount failure without any network I/O.
	pool, err := pgxpool.New(ctx, "postgres://fixture@127.0.0.1:1/fixture?sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	pool.Close()
	_, cause := pool.Begin(ctx)
	runner := &Runner{db: pool}
	id := uuid.New()
	progress := &ingestPhaseProgress{phase: phaseImporting, imported: 3, existing: 25, pendingExisting: map[uuid.UUID]bool{id: true}}
	job := claimedJob{ingestPhase: progress}
	if err := runner.recordIngestPhase(ctx, job, phaseCleanup, nil, nil); !errors.Is(err, cause) || progress.phase != phaseImporting {
		t.Fatalf("phase advanced after write failure: phase=%s err=%v", progress.phase, err)
	}
	if err := runner.ingestCoverageStarted(ctx, job); !errors.Is(err, cause) || progress.phase != phaseImporting {
		t.Fatalf("analysis began after write failure: phase=%s err=%v", progress.phase, err)
	}
	progress.phase = phaseAnalyzingImportedRoutes
	if err := runner.ingestCoverageResolved(ctx, job, id); !errors.Is(err, cause) || progress.imported != 3 || progress.resolvedImports[id] {
		t.Fatalf("imported resolution was counted after write failure: %+v err=%v", progress, err)
	}
	progress.phase = phaseAnalyzingExistingRoutes
	if err := runner.ingestCoverageResolved(ctx, job, id); !errors.Is(err, cause) || progress.existing != 25 || !progress.pendingExisting[id] {
		t.Fatalf("existing resolution was counted after write failure: %+v err=%v", progress, err)
	}
}
