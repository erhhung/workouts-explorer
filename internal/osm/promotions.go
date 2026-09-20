package osm

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

func PromotionEventHead(ctx context.Context, pool *pgxpool.Pool) (int64, error) {
	if pool == nil {
		return 0, fmt.Errorf("OSM database pool is unavailable")
	}
	var eventID int64
	if err := pool.QueryRow(ctx, `SELECT osm_catalog.promotion_event_head()`).Scan(&eventID); err != nil {
		return 0, fmt.Errorf("read OSM promotion event head: %w", err)
	}
	return eventID, nil
}
