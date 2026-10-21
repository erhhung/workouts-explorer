package worker

import (
	"context"
	"errors"
	"fmt"

	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// This APP-only control call is independent of route execution and OSM health.
// The database owns cooldown, cutover, lineage and budget, including lost replies.
func (r *CoverageRunner) retryCoverageOnce(ctx context.Context) (bool, error) {
	if r.autoRetryCount == 0 {
		return false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, cleanupTimeout)
	defer cancel()
	var parent uuid.UUID
	var routes int
	err := r.db.QueryRow(ctx, `SELECT job_id,route_count FROM app.retry_next_coverage_update($1,$2)`,
		r.autoRetryCount, database.SupportedSchemaVersion).Scan(&parent, &routes)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("automatic coverage retry: %w", err)
	}
	r.logger.Info("automatic coverage retry created", "job_id", parent, "routes", routes)
	return true, nil
}
