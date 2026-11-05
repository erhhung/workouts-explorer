package api

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
)

var mapTileInitialization sync.Mutex

func (s *Server) mapTiles() *mapTileService {
	mapTileInitialization.Lock()
	defer mapTileInitialization.Unlock()
	if s.tiles == nil {
		ctx := s.tileContext
		if ctx == nil {
			ctx = context.Background()
		}
		s.tiles = newMapTileService(ctx, s.config.MapTiles, s.config.TileServerURL, s.tileClient)
	}
	return s.tiles
}

func (s *Server) authorizeMapTile(w http.ResponseWriter, r *http.Request, session authenticatedSession, key mapTileKey) (time.Time, bool) {
	tx, err := s.sessionAccountTransactionWithOptions(r.Context(), session, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	var expires *time.Time
	if err == nil {
		defer tx.Rollback(r.Context())
		err = tx.QueryRow(r.Context(), `SELECT app.authorize_map_tile($1,$2,$3,$4,$5)`,
			key.account, key.session, key.selection, key.generation, key.coverage).Scan(&expires)
		if err == nil {
			err = tx.Commit(r.Context())
		}
	}
	if err != nil || r.Context().Err() != nil {
		s.mapTiles().metrics.event(r.Context(), key, "authorization_error")
		w.Header().Set("Retry-After", "1")
		writeMapUnavailable(w, r)
		return time.Time{}, false
	}
	if expires == nil || !expires.After(time.Now()) {
		s.mapTiles().metrics.event(r.Context(), key, "authorization_denied")
		s.mapTiles().forget(key)
		writeProblem(w, r, http.StatusNotFound, "Not Found", "map selection is unavailable")
		return time.Time{}, false
	}
	return *expires, true
}
