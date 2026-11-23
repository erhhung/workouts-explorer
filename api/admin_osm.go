package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/erhhung/workouts-explorer/api/generated"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Server) osmProviders() []string {
	if s.config.OSMProviders == nil {
		return []string{"geofabrik"}
	}
	return s.config.OSMProviders
}

func (s *Server) GetAdminOSMRegions(w http.ResponseWriter, r *http.Request) {
	session, ok := s.requireSession(w, r, "administrator")
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	providers := s.osmProviders()
	result := struct {
		RefreshEnabled bool                   `json:"refreshEnabled"`
		UpdatesEnabled bool                   `json:"updatesEnabled"`
		Providers      []osm.ProviderCatalog  `json:"providers"`
		Regions        []osm.CatalogRegion    `json:"regions"`
		Refresh        *osm.CatalogRefreshJob `json:"refresh"`
	}{RefreshEnabled: s.config.OSMCatalogEnabled, UpdatesEnabled: s.config.OSMUpdatesEnabled, Providers: []osm.ProviderCatalog{}, Regions: []osm.CatalogRegion{}}
	for _, provider := range providers {
		value := osm.ProviderCatalog{Provider: provider, State: "not_refreshed"}
		var raw []byte
		err := s.db.QueryRow(r.Context(), `SELECT payload FROM app.osm_provider_catalogs WHERE provider=$1`, provider).Scan(&raw)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			writeAdminUnavailable(w, r)
			return
		}
		if err == nil {
			if json.Unmarshal(raw, &value) != nil {
				writeAdminUnavailable(w, r)
				return
			}
		}
		result.Providers = append(result.Providers, value)
	}
	rows, err := s.db.Query(r.Context(), `SELECT payload-'boundary' FROM app.osm_region_catalog WHERE provider=ANY($1::text[])
        AND (payload->>'available' IS DISTINCT FROM 'false' OR payload->>'generation' IS NOT NULL)
        ORDER BY lower(payload->>'name'),id LIMIT 10001`, providers)
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		var region osm.CatalogRegion
		if rows.Scan(&raw) != nil || json.Unmarshal(raw, &region) != nil {
			writeAdminUnavailable(w, r)
			return
		}
		region.Name = osm.DisplayRegionName(region.Name)
		result.Regions = append(result.Regions, region)
	}
	if rows.Err() != nil || len(result.Regions) > 10000 {
		writeAdminUnavailable(w, r)
		return
	}
	rows.Close()
	counts, err := s.db.Query(r.Context(), `SELECT region_id,workout_count,coverage_route_count FROM app.admin_osm_region_workout_counts($1)`, session.sessionID)
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	byID := make(map[string]*osm.CatalogRegion, len(result.Regions))
	for i := range result.Regions {
		byID[result.Regions[i].ID] = &result.Regions[i]
	}
	for counts.Next() {
		var id string
		var workouts, coverage int64
		if counts.Scan(&id, &workouts, &coverage) != nil {
			counts.Close()
			writeAdminUnavailable(w, r)
			return
		}
		if region := byID[id]; region != nil {
			region.WorkoutCount, region.CoverageRouteCount = workouts, coverage
		}
	}
	err = counts.Err()
	counts.Close()
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	ids := make([]string, len(result.Regions))
	for i, region := range result.Regions {
		ids[i] = region.ID
	}
	updates, err := s.db.Query(r.Context(), `SELECT DISTINCT ON(region_id) `+osm.RegionUpdateJSON+` FROM app.osm_region_updates WHERE region_id=ANY($1::text[]) ORDER BY region_id,created_at DESC,id DESC`, ids)
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	latest := map[string]*osm.RegionUpdate{}
	for updates.Next() {
		var raw []byte
		value := new(osm.RegionUpdate)
		if updates.Scan(&raw) != nil || json.Unmarshal(raw, value) != nil {
			updates.Close()
			writeAdminUnavailable(w, r)
			return
		}
		if !value.Cleared {
			latest[value.RegionID] = value
		}
	}
	err = updates.Err()
	updates.Close()
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	for i := range result.Regions {
		region := &result.Regions[i]
		region.Update = latest[region.ID]
		if region.Update != nil {
			region.Status = map[string]string{"waiting": "Waiting", "downloading": "Downloading", "processing": "Processing", "pausing": "Pausing", "canceling": "Canceling", "paused": "Paused", "canceled": "Canceled", "failed": "Failed", "deleting": "Deleting"}[region.Update.State]
		}
	}
	// Duration belongs to the promoted generation, even when a newer failed or
	// dismissed attempt is the region's latest request.
	durations, err := s.db.Query(r.Context(), `SELECT DISTINCT ON(u.region_id,u.generation_id) u.region_id,u.generation_id,
		u.elapsed_total_ms/1000.0
		FROM app.osm_region_updates u
		WHERE u.region_id=ANY($1::text[]) AND u.promoted AND u.state='succeeded' AND u.elapsed_total_ms IS NOT NULL
		ORDER BY u.region_id,u.generation_id,u.finished_at DESC`, ids)
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	for durations.Next() {
		var regionID string
		var generation int64
		var seconds float64
		if durations.Scan(&regionID, &generation, &seconds) != nil {
			durations.Close()
			writeAdminUnavailable(w, r)
			return
		}
		for i := range result.Regions {
			region := &result.Regions[i]
			if region.ID == regionID && region.BuildID != nil && *region.BuildID == generation {
				value := seconds
				region.GenerationElapsedSeconds = &value
				break
			}
		}
	}
	err = durations.Err()
	durations.Close()
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	var raw []byte
	err = s.db.QueryRow(r.Context(), `SELECT `+osm.CatalogJobJSON+` FROM app.osm_catalog_refresh_jobs ORDER BY created_at DESC,id DESC LIMIT 1`).Scan(&raw)
	if err == nil {
		if json.Unmarshal(raw, &result.Refresh) != nil {
			writeAdminUnavailable(w, r)
			return
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		writeAdminUnavailable(w, r)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) GetAdminOSMRegionBoundary(w http.ResponseWriter, r *http.Request, params generated.GetAdminOSMRegionBoundaryParams) {
	if _, ok := s.requireSession(w, r, "administrator"); !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	var boundary []byte
	err := s.db.QueryRow(r.Context(), `SELECT payload->'boundary' FROM app.osm_region_catalog WHERE id=$1 AND provider=ANY($2::text[])`, params.RegionId, s.osmProviders()).Scan(&boundary)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "Not Found", "region not found")
		return
	}
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	if len(boundary) == 0 || string(boundary) == "null" {
		writeProblem(w, r, 503, "Service Unavailable", "region boundary is not cached yet")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		ID       string          `json:"id"`
		Geometry json.RawMessage `json:"geometry"`
	}{params.RegionId, boundary})
}

func (s *Server) CreateAdminOSMCatalogRefresh(w http.ResponseWriter, r *http.Request, params generated.CreateAdminOSMCatalogRefreshParams) {
	session, ok := s.requireSession(w, r, "administrator")
	if !ok || !requireCSRF(w, r, session, params.XCSRFToken) {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if !s.config.OSMCatalogEnabled {
		writeProblem(w, r, 503, "Service Unavailable", "catalog refresh is not enabled in deployment configuration")
		return
	}
	var body map[string]json.RawMessage
	decoder := json.NewDecoder(r.Body)
	if decoder.Decode(&body) != nil || body == nil || len(body) != 0 || decoder.Decode(&struct{}{}) != io.EOF {
		writeProblem(w, r, 400, "Bad Request", "catalog refresh body must be an empty JSON object")
		return
	}
	providers := s.osmProviders()
	if len(providers) == 0 || len(providers) > 16 {
		writeProblem(w, r, 409, "Conflict", "no supported catalog refresh configuration is available")
		return
	}
	tx, err := s.db.Begin(r.Context())
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	defer tx.Rollback(r.Context())
	_, err = tx.Exec(r.Context(), `SET LOCAL lock_timeout='3s'`)
	if err == nil {
		_, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended('osm-catalog-refresh-command',0))`)
	}
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	var authorized bool
	err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM app.sessions s JOIN app.authentication_principals p ON p.id=s.principal_id
        WHERE s.id=$1 AND p.role='administrator' AND p.disabled_at IS NULL AND s.revoked_at IS NULL
        AND s.expires_at>clock_timestamp() FOR SHARE OF s,p)`, session.sessionID).Scan(&authorized)
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	if !authorized {
		writeProblem(w, r, 401, "Unauthorized", "administrator session is no longer active")
		return
	}
	var raw []byte
	err = tx.QueryRow(r.Context(), `SELECT `+osm.CatalogJobJSON+` FROM app.osm_catalog_refresh_jobs WHERE state IN('queued','running') LIMIT 1`).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		id := uuid.Must(uuid.NewV7())
		err = tx.QueryRow(r.Context(), `INSERT INTO app.osm_catalog_refresh_jobs(id,administrator_id,providers) VALUES($1,$2,$3) RETURNING `+osm.CatalogJobJSON, id, session.principalID, providers).Scan(&raw)
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO app.audit_records(id,actor_principal_id,action,target_type,target_id,request_id)
            VALUES($1,$2,'osm.catalog_refresh_requested','catalog_refresh',$3,$4)`, uuid.Must(uuid.NewV7()), session.principalID, id, requestIDFrom(r.Context()))
		}
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	var job osm.CatalogRefreshJob
	if json.Unmarshal(raw, &job) != nil {
		writeAdminUnavailable(w, r)
		return
	}
	w.Header().Set("Location", "/api/admin/osm/catalog-refreshes/"+job.ID)
	writeJSON(w, http.StatusAccepted, job)
}

func (s *Server) GetAdminOSMCatalogRefresh(w http.ResponseWriter, r *http.Request, jobID generated.CompactUUID) {
	if _, ok := s.requireSession(w, r, "administrator"); !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	id, err := uuid.Parse(jobID)
	if err != nil {
		writeProblem(w, r, 400, "Bad Request", "job ID is invalid")
		return
	}
	var raw []byte
	err = s.db.QueryRow(r.Context(), `SELECT `+osm.CatalogJobJSON+` FROM app.osm_catalog_refresh_jobs WHERE id=$1`, id).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		writeProblem(w, r, 404, "Not Found", "catalog refresh not found")
		return
	}
	if err != nil {
		writeAdminUnavailable(w, r)
		return
	}
	var job osm.CatalogRefreshJob
	if json.Unmarshal(raw, &job) != nil {
		writeAdminUnavailable(w, r)
		return
	}
	writeJSON(w, http.StatusOK, job)
}
