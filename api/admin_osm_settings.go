package api

import (
	"context"
	"net/http"
	"time"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type osmProcessingSettings struct {
	MonitorDatabaseStorage bool      `json:"monitorDatabaseStorage"`
	Revision               int64     `json:"revision"`
	UpdatedAt              time.Time `json:"updatedAt"`
}

func readOSMProcessingSettings(ctx context.Context, tx pgx.Tx, lock bool) (osmProcessingSettings, error) {
	query := `SELECT monitor_database_storage,revision,updated_at FROM app.osm_processing_settings WHERE singleton`
	if lock {
		query += " FOR UPDATE"
	}
	var value osmProcessingSettings
	err := tx.QueryRow(ctx, query).Scan(&value.MonitorDatabaseStorage, &value.Revision, &value.UpdatedAt)
	return value, err
}

func (s *Server) GetAdminOSMProcessingSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r, "administrator"); !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context())
	value, err := readOSMProcessingSettings(r.Context(), tx, false)
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	writeJSON(w, 200, value)
}

func (s *Server) UpdateAdminOSMProcessingSettings(w http.ResponseWriter, r *http.Request, params generated.UpdateAdminOSMProcessingSettingsParams) {
	session, ok := s.requireSession(w, r, "administrator")
	if !ok || !requireCSRF(w, r, session, params.XCSRFToken) {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	var command struct {
		Monitor  *bool  `json:"monitorDatabaseStorage"`
		Revision *int64 `json:"revision"`
	}
	if !decodeOSMCommand(r, &command) || command.Monitor == nil || command.Revision == nil || *command.Revision < 1 {
		writeProblem(w, r, 400, "Bad Request", "invalid OSM processing settings")
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
	value, err := readOSMProcessingSettings(r.Context(), tx, true)
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	if value.MonitorDatabaseStorage != *command.Monitor {
		if value.Revision != *command.Revision {
			writeProblem(w, r, 409, "Conflict", "settings changed; review the latest value before saving")
			return
		}
		if err = tx.QueryRow(r.Context(), `UPDATE app.osm_processing_settings SET monitor_database_storage=$1,revision=revision+1,updated_at=clock_timestamp(),updated_by=$2 WHERE singleton RETURNING monitor_database_storage,revision,updated_at`, *command.Monitor, session.principalID).Scan(&value.MonitorDatabaseStorage, &value.Revision, &value.UpdatedAt); err != nil {
			writeAdminUnavailable(w, r)
			return
		}
		action := "osm.storage_monitoring_disabled"
		if value.MonitorDatabaseStorage {
			action = "osm.storage_monitoring_enabled"
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO app.audit_records(id,actor_principal_id,action,target_type,request_id) VALUES($1,$2,$3,'osm_processing_settings',$4)`, uuid.Must(uuid.NewV7()), session.principalID, action, requestIDFrom(r.Context())); err != nil {
			writeAdminUnavailable(w, r)
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	writeJSON(w, 200, value)
}
