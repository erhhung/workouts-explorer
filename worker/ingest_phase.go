package worker

import (
	"context"

	"github.com/google/uuid"
)

type ingestPhase string

const (
	phaseResuming                ingestPhase = "resuming"
	phasePreparing               ingestPhase = "preparing"
	phaseDiscovering             ingestPhase = "discovering"
	phaseImporting               ingestPhase = "importing"
	phaseAnalyzingImportedRoutes ingestPhase = "analyzing_imported_routes"
	phaseAnalyzingExistingRoutes ingestPhase = "analyzing_existing_routes"
	phaseUpdatingTimeZones       ingestPhase = "updating_time_zones"
	phaseCleanup                 ingestPhase = "cleanup"
)

// Owned only by the ingest processing goroutine, never the heartbeat or Runner.
type ingestPhaseProgress struct {
	phase           ingestPhase
	imported        int64
	existing        int64
	resolvedImports map[uuid.UUID]bool
	pendingExisting map[uuid.UUID]bool
}

func (r *Runner) recordIngestPhase(ctx context.Context, job claimedJob, phase ingestPhase, current, total *int64) error {
	tx, err := beginAccount(ctx, r.db, job.accountID)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var recorded bool
	if err := tx.QueryRow(ctx, `SELECT app.record_ingest_phase($1,$2,$3,$4,$5,$6)`,
		job.id, r.workerID, job.lease, string(phase), current, total).Scan(&recorded); err != nil {
		return err
	}
	if !recorded {
		return errLeaseLost
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	if job.ingestPhase != nil {
		job.ingestPhase.phase = phase
	}
	return nil
}

func (r *Runner) ingestCoverageStarted(ctx context.Context, job claimedJob) error {
	progress := job.ingestPhase
	if progress == nil || progress.phase == phaseAnalyzingExistingRoutes || progress.phase == phaseAnalyzingImportedRoutes {
		return nil
	}
	return r.recordIngestPhase(ctx, job, phaseAnalyzingImportedRoutes, &progress.imported, nil)
}

// Count only successful route resolution acknowledgments. Deferred lookups and
// prepared-cohort revalidation do not pretend that another route was resolved.
func (r *Runner) ingestCoverageResolved(ctx context.Context, job claimedJob, workoutID uuid.UUID) error {
	progress := job.ingestPhase
	if progress == nil {
		return nil
	}
	if progress.phase == phaseAnalyzingExistingRoutes {
		if !progress.pendingExisting[workoutID] {
			return nil
		}
		current := progress.existing + 1
		if err := r.recordIngestPhase(ctx, job, phaseAnalyzingExistingRoutes, &current, nil); err != nil {
			return err
		}
		progress.existing = current
		delete(progress.pendingExisting, workoutID)
		return nil
	}
	if progress.resolvedImports[workoutID] {
		return nil
	}
	current := progress.imported + 1
	if err := r.recordIngestPhase(ctx, job, phaseAnalyzingImportedRoutes, &current, nil); err != nil {
		return err
	}
	progress.imported = current
	if progress.resolvedImports == nil {
		progress.resolvedImports = make(map[uuid.UUID]bool)
	}
	progress.resolvedImports[workoutID] = true
	return nil
}
