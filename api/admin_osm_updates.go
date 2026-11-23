package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func decodeOSMCommand(r *http.Request, target any) bool {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	return decoder.Decode(target) == nil && decoder.Decode(&struct{}{}) == io.EOF
}

func updateJSON(ctx context.Context, tx pgx.Tx, id uuid.UUID) (osm.RegionUpdate, error) {
	var raw []byte
	var result osm.RegionUpdate
	err := tx.QueryRow(ctx, `SELECT `+osm.RegionUpdateJSON+` FROM app.osm_region_updates WHERE id=$1`, id).Scan(&raw)
	if err == nil {
		err = json.Unmarshal(raw, &result)
	}
	return result, err
}

func fenceOSMAdministrator(ctx context.Context, tx pgx.Tx, sessionID uuid.UUID) error {
	var active bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM app.sessions s JOIN app.authentication_principals p ON p.id=s.principal_id WHERE s.id=$1
        AND p.role='administrator' AND p.disabled_at IS NULL AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp() FOR SHARE OF s,p)`, sessionID).Scan(&active)
	if err == nil && !active {
		err = errors.New("administrator session expired")
	}
	return err
}

func (s *Server) CreateAdminOSMRegionUpdate(w http.ResponseWriter, r *http.Request, params generated.CreateAdminOSMRegionUpdateParams) {
	session, ok := s.requireSession(w, r, "administrator")
	if !ok || !requireCSRF(w, r, session, params.XCSRFToken) {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if !s.config.OSMUpdatesEnabled {
		writeProblem(w, r, 503, "Service Unavailable", "region updates are not enabled")
		return
	}
	var command struct {
		RegionID string `json:"regionId"`
		Action   string `json:"action"`
	}
	if !decodeOSMCommand(r, &command) || len(command.RegionID) > 257 || (command.Action != "download" && command.Action != "reprocess" && command.Action != "delete") {
		writeProblem(w, r, 400, "Bad Request", "invalid region update command")
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context())
	if err = fenceOSMAdministrator(r.Context(), tx, session.sessionID); err != nil {
		writeProblem(w, r, 401, "Unauthorized", "administrator session is no longer active")
		return
	}
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended('osm-region-update-command',0))`); err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	var available bool
	err = tx.QueryRow(r.Context(), `SELECT COALESCE((payload->>'available')::boolean,true) FROM app.osm_region_catalog WHERE id=$1 AND provider=ANY($2::text[])`, command.RegionID, s.osmProviders()).Scan(&available)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "Not Found", "region not found")
		return
	}
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	if !available && command.Action != "delete" {
		writeProblem(w, r, 409, "Conflict", "the provider extract is unavailable")
		return
	}
	var existing uuid.UUID
	var state, operation string
	err = tx.QueryRow(r.Context(), `SELECT id,state,operation FROM app.osm_region_updates WHERE region_id=$1 AND (slot_held OR state IN('waiting','downloading','processing','pausing','canceling','paused','deleting') OR state='failed' AND(resumable OR promoted))
        AND NOT EXISTS(SELECT 1 FROM app.osm_region_update_events cleared WHERE cleared.update_id=app.osm_region_updates.id AND cleared.event_type='osm.region_update_clear') ORDER BY created_at DESC LIMIT 1 FOR UPDATE`, command.RegionID).Scan(&existing, &state, &operation)
	if err == nil {
		if command.Action == "delete" && (state == "paused" || state == "failed") {
			var held bool
			if err = tx.QueryRow(r.Context(), `SELECT slot_held FROM app.osm_region_updates WHERE id=$1`, existing).Scan(&held); err != nil {
				writeAdminUnavailable(w, r)
				return
			}
			if held {
				writeProblem(w, r, 409, "Conflict", "wait for the updater to stop before deleting the region")
				return
			}
			if _, err = tx.Exec(r.Context(), `UPDATE app.osm_region_updates SET state='canceled',finished_at=clock_timestamp(),error_summary='Superseded by region deletion.' WHERE id=$1`, existing); err != nil {
				writeAdminUnavailable(w, r)
				return
			}
			if err = recordOSMUpdateAudit(r.Context(), tx, session.principalID, existing, "osm.region_update_superseded", requestIDFrom(r.Context())); err != nil {
				writeAdminUnavailable(w, r)
				return
			}
			err = pgx.ErrNoRows
		} else {
			if (command.Action == "delete") != (operation == "delete") {
				writeProblem(w, r, 409, "Conflict", "wait for the current region operation to finish")
				return
			}
			if state == "paused" || state == "failed" {
				writeProblem(w, r, 409, "Conflict", "resume or cancel the paused update first")
				return
			}
			value, err := updateJSON(r.Context(), tx, existing)
			if err != nil {
				writeAdminUnavailable(w, r)
				return
			}
			if err = recordOSMUpdateAudit(r.Context(), tx, session.principalID, existing, "osm.region_update_coalesced", requestIDFrom(r.Context())); err != nil {
				writeAdminUnavailable(w, r)
				return
			}
			if tx.Commit(r.Context()) != nil {
				writeAdminUnavailable(w, r)
				return
			}
			writeJSON(w, 202, value)
			return
		}
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		writeAdminUnavailable(w, r)
		return
	}
	id := uuid.Must(uuid.NewV7())
	if _, err = tx.Exec(r.Context(), `INSERT INTO app.osm_region_updates(id,administrator_id,region_id,operation) VALUES($1,$2,$3,$4)`, id, session.principalID, command.RegionID, command.Action); err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	if command.Action == "delete" {
		// INSERT privileges intentionally exclude state/phase; set them through the
		// same narrow UPDATE privileges used by other administrator commands.
		if _, err = tx.Exec(r.Context(), `UPDATE app.osm_region_updates SET state='deleting' WHERE id=$1`, id); err != nil {
			writeAdminUnavailable(w, r)
			return
		}
	}
	tasks := osm.UpdateTasks
	if command.Action == "delete" {
		tasks = osm.DeleteTasks
	}
	definitions, _ := json.Marshal(tasks)
	if _, err = tx.Exec(r.Context(), `INSERT INTO app.osm_region_update_steps(update_id,task_order,name,repeated)
        SELECT $1,(value->>'order')::integer,value->>'name',(value->>'repeated')::boolean FROM jsonb_array_elements($2::jsonb) value`, id, definitions); err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	if err = recordOSMUpdateAudit(r.Context(), tx, session.principalID, id, "osm.region_update_requested", requestIDFrom(r.Context())); err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	value, err := updateJSON(r.Context(), tx, id)
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	w.Header().Set("Location", "/api/admin/osm/region-updates/"+value.ID)
	writeJSON(w, 202, value)
}

func recordOSMUpdateAudit(ctx context.Context, tx pgx.Tx, actor, target uuid.UUID, action, request string) error {
	if _, err := tx.Exec(ctx, `INSERT INTO app.audit_records(id,actor_principal_id,action,target_type,target_id,request_id) VALUES($1,$2,$3,'osm_region_update',$4,$5)`, uuid.Must(uuid.NewV7()), actor, action, target, request); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO app.osm_region_update_events(update_id,event_type) VALUES($1,$2)`, target, action)
	return err
}

func (s *Server) GetAdminOSMRegionUpdate(w http.ResponseWriter, r *http.Request, jobID generated.CompactUUID) {
	if _, ok := s.requireSession(w, r, "administrator"); !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	id, err := uuid.Parse(jobID)
	if err != nil {
		writeProblem(w, r, 400, "Bad Request", "invalid update ID")
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context())
	value, err := updateJSON(r.Context(), tx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "Not Found", "update not found")
		return
	}
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	rows, err := tx.Query(r.Context(), `SELECT task_order,name,repeated,state,batch_index,batch_total,started_at,finished_at,elapsed_total_ms/1000.0 FROM app.osm_region_update_steps WHERE update_id=$1 ORDER BY task_order`, id)
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	value.Steps = []osm.RegionUpdateStep{}
	for rows.Next() {
		var step osm.RegionUpdateStep
		if err = rows.Scan(&step.Order, &step.Name, &step.Repeated, &step.State, &step.Batch, &step.Batches, &step.StartedAt, &step.FinishedAt, &step.ElapsedTotal); err != nil {
			rows.Close()
			writeAdminUnavailable(w, r)
			return
		}
		value.Steps = append(value.Steps, step)
	}
	err = rows.Err()
	rows.Close()
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	writeJSON(w, 200, value)
}

func (s *Server) CommandAdminOSMRegionUpdate(w http.ResponseWriter, r *http.Request, jobID generated.CompactUUID, params generated.CommandAdminOSMRegionUpdateParams) {
	session, ok := s.requireSession(w, r, "administrator")
	if !ok || !requireCSRF(w, r, session, params.XCSRFToken) {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	var command struct {
		Action string `json:"action"`
	}
	id, err := uuid.Parse(jobID)
	if err != nil || !decodeOSMCommand(r, &command) || (command.Action != "pause" && command.Action != "resume" && command.Action != "cancel" && command.Action != "clear") {
		writeProblem(w, r, 400, "Bad Request", "invalid update command")
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context())
	var state, intent, operation string
	var slot, resumable, promoted bool
	var generation *int64
	if err = fenceOSMAdministrator(r.Context(), tx, session.sessionID); err != nil {
		writeProblem(w, r, 401, "Unauthorized", "administrator session is no longer active")
		return
	}
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended('osm-region-update-command',0))`); err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	err = tx.QueryRow(r.Context(), `SELECT state,intent,slot_held,resumable,promoted,generation_id,operation FROM app.osm_region_updates WHERE id=$1 FOR UPDATE`, id).Scan(&state, &intent, &slot, &resumable, &promoted, &generation, &operation)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "Not Found", "update not found")
		return
	}
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	var cleared bool
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM app.osm_region_update_events WHERE update_id=$1 AND event_type='osm.region_update_clear')`, id).Scan(&cleared); err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	if command.Action == "clear" {
		if slot || (state != "failed" && state != "canceled") {
			writeProblem(w, r, 409, "Conflict", "only stopped failed or canceled updates can be cleared")
			return
		}
		if !cleared {
			err = recordOSMUpdateAudit(r.Context(), tx, session.principalID, id, "osm.region_update_clear", requestIDFrom(r.Context()))
		}
		if err != nil {
			writeAdminUnavailable(w, r)
			return
		}
		value, readErr := updateJSON(r.Context(), tx, id)
		if readErr != nil || tx.Commit(r.Context()) != nil {
			writeAdminUnavailable(w, r)
			return
		}
		writeJSON(w, 202, value)
		return
	}
	if cleared {
		writeProblem(w, r, 409, "Conflict", "the update status has been cleared; start a new region update")
		return
	}
	var superseded bool
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM app.osm_region_updates newer JOIN app.osm_region_updates target ON target.id=$1 WHERE newer.region_id=target.region_id AND (newer.created_at,newer.id)>(target.created_at,target.id))`, id).Scan(&superseded); err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	if superseded {
		writeProblem(w, r, 409, "Conflict", "a newer region operation supersedes this job")
		return
	}
	if operation == "delete" {
		writeProblem(w, r, 409, "Conflict", "region deletion cannot be paused or canceled")
		return
	}
	next, newIntent := state, intent
	switch command.Action {
	case "resume":
		if slot || (state != "paused" && state != "failed") {
			writeProblem(w, r, 409, "Conflict", "update cannot be resumed")
			return
		}
		next, newIntent = "waiting", "run"
		if !resumable && !promoted {
			_, err = tx.Exec(r.Context(), `UPDATE app.osm_region_update_steps SET state='pending',batch_index=0,batch_total=0,started_at=NULL,finished_at=NULL WHERE update_id=$1`, id)
		}
	case "pause":
		if promoted || !(state == "waiting" || state == "downloading" || state == "processing" || state == "pausing") {
			writeProblem(w, r, 409, "Conflict", "update cannot be paused")
			return
		}
		next, newIntent = "pausing", "pause"
		if !slot {
			next = "paused"
		}
	case "cancel":
		if promoted || state == "succeeded" || state == "canceled" {
			writeProblem(w, r, 409, "Conflict", "update cannot be canceled")
			return
		}
		next, newIntent = "canceling", "cancel"
		if !slot && generation == nil {
			next = "canceled"
		}
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE app.osm_region_updates SET state=$2,intent=$3,
        enqueued_at=CASE WHEN $4='resume' OR ($2='canceling' AND NOT slot_held) THEN clock_timestamp() ELSE enqueued_at END,
        finished_at=CASE WHEN $2 IN('canceled','paused') THEN clock_timestamp() ELSE NULL END,
        error_summary=CASE $2 WHEN 'canceled' THEN 'This update was canceled.' WHEN 'paused' THEN 'This update is paused.' ELSE NULL END WHERE id=$1`, id, next, newIntent, command.Action)
	}
	if err == nil && (next != state || newIntent != intent) {
		err = recordOSMUpdateAudit(r.Context(), tx, session.principalID, id, "osm.region_update_"+command.Action, requestIDFrom(r.Context()))
	}
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	value, err := updateJSON(r.Context(), tx, id)
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	writeJSON(w, 202, value)
}
