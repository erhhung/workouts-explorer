package worker

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

func NewHandler(db, osmDB *pgxpool.Pool, logger *slog.Logger) http.Handler {
	router := chi.NewRouter()
	router.Use(func(next http.Handler) http.Handler { return otelhttp.NewHandler(next, "worker.probe") })
	router.Get("/health/live", func(w http.ResponseWriter, _ *http.Request) {
		writeHealth(w, http.StatusOK, "ok")
	})
	router.Get("/health/ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		appReady, osmReady := database.Ready(ctx, db), database.OSMReady(ctx, osmDB)
		if !appReady || !osmReady {
			logger.WarnContext(r.Context(), "worker readiness check failed",
				"application_database_ready", appReady, "osm_database_ready", osmReady)
			writeHealth(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		writeHealth(w, http.StatusOK, "ok")
	})
	return router
}

func writeHealth(w http.ResponseWriter, status int, value string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": value})
}
