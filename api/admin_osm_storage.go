package api

import (
	"context"
	"github.com/erhhung/workouts-explorer/internal/osm"
	"net/http"
	"time"
)

func (s *Server) GetAdminOSMStorageUsage(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.requireSession(w, r, "administrator"); !ok {
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if !s.config.OSMStorageMetricsEnabled || s.storageUsage == nil || !s.storageUsage.Configured() {
		writeJSON(w, http.StatusOK, osm.StorageUsage{Configured: false, CheckedAt: time.Now().UTC(), Databases: []string{}, Nodes: []osm.StorageUsageNode{}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	value, err := s.storageUsage.Read(ctx)
	if err != nil {
		writeProblem(w, r, http.StatusServiceUnavailable, "Service Unavailable", "Storage usage could not be measured. Please try again.")
		return
	}
	writeJSON(w, http.StatusOK, value)
}
