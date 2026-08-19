package osm

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSharedOSMReadiness(t *testing.T) {
	databaseURL := os.Getenv("OSM_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("OSM_DATABASE_URL is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if !Ready(ctx, pool) {
		t.Fatal("shared OSM database is not ready")
	}
}
