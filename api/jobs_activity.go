package api

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var errInvalidIngestActivity = errors.New("invalid stored ingest activity")

func readIngestActivities(ctx context.Context, tx pgx.Tx, detail *generated.JobDetail) error {
	ids := make([]uuid.UUID, 0, len(detail.Children)+1)
	byID := make(map[uuid.UUID]*generated.JobDetail, len(detail.Children)+1)
	for index := -1; index < len(detail.Children); index++ {
		item := detail
		if index >= 0 {
			item = &detail.Children[index]
		}
		if item.Source == nil {
			continue
		}
		id, valid := parseCompactUUID(item.Id)
		if !valid {
			return errInvalidIngestActivity
		}
		ids = append(ids, id)
		byID[id] = item
	}
	if len(ids) == 0 {
		return nil
	}
	// The lateral limit uses the partial (job_id, id DESC) phase index per source,
	// while the attempt predicate excludes evidence left by an earlier lease.
	rows, err := tx.Query(ctx, `SELECT j.id,phase.code,phase.fields,phase.created_at
		FROM unnest($1::uuid[]) requested(id) JOIN app.jobs j ON j.id=requested.id
		CROSS JOIN LATERAL (
			SELECT e.code,e.fields,e.created_at FROM app.job_events e
			WHERE e.job_id=j.id AND e.account_id=j.account_id AND e.code LIKE 'ingest-phase-%'
				AND e.fields->>'attempt'=j.attempt::text
			ORDER BY e.id DESC LIMIT 1
		) phase
		WHERE j.account_id=app.current_account_id()
			AND j.kind IN ('manual_ingest_source','scheduled_ingest_source')`, ids)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var code string
		var fields []byte
		var updated time.Time
		if err := rows.Scan(&id, &code, &fields, &updated); err != nil {
			return err
		}
		activity, err := decodeIngestActivity(code, fields, updated)
		if err != nil {
			return err
		}
		if item := byID[id]; item != nil {
			item.Activity = activity
		}
	}
	return rows.Err()
}

func decodeIngestActivity(code string, fields []byte, updated time.Time) (*generated.IngestActivity, error) {
	var phase generated.IngestActivityPhase
	switch code {
	case "ingest-phase-resuming":
		phase = generated.Resuming
	case "ingest-phase-preparing":
		phase = generated.Preparing
	case "ingest-phase-discovering":
		phase = generated.Discovering
	case "ingest-phase-importing":
		phase = generated.Importing
	case "ingest-phase-analyzing-imported-routes":
		phase = generated.AnalyzingImportedRoutes
	case "ingest-phase-analyzing-existing-routes":
		phase = generated.AnalyzingExistingRoutes
	case "ingest-phase-updating-time-zones":
		phase = generated.UpdatingTimeZones
	case "ingest-phase-cleanup":
		phase = generated.Cleanup
	default:
		return nil, errInvalidIngestActivity
	}
	var counters *struct {
		Current *int64 `json:"current"`
		Total   *int64 `json:"total"`
	}
	if json.Unmarshal(fields, &counters) != nil || counters == nil ||
		(counters.Current != nil && *counters.Current < 0) || (counters.Total != nil && *counters.Total < 0) ||
		(counters.Current != nil && counters.Total != nil && *counters.Current > *counters.Total) {
		return nil, errInvalidIngestActivity
	}
	return &generated.IngestActivity{Phase: phase, Current: counters.Current, Total: counters.Total, UpdatedAt: updated}, nil
}
