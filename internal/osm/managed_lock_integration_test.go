package osm

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestManagedUpdaterWaitersKeepTheirConnections(t *testing.T) {
	url := os.Getenv("OSM_CATALOG_TEST_OWNER_URL")
	if url == "" {
		t.Skip("isolated OSM database required")
	}
	if !strings.Contains(url, "osm_catalog_test") {
		t.Fatal("refusing non-test database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	blocker, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Release()
	if _, err = blocker.Exec(ctx, `SELECT pg_advisory_lock(hashtextextended('workouts-osm-updater',0))`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	for range 2 {
		go func() {
			store := PostgreSQLUpdateStore{Pool: pool, WaitForLock: true}
			unlock, err := store.LockUpdater(ctx)
			if err == nil {
				time.Sleep(30 * time.Millisecond)
				unlock()
			}
			done <- err
		}()
	}
	time.Sleep(100 * time.Millisecond)
	if _, err = blocker.Exec(ctx, `SELECT pg_advisory_unlock(hashtextextended('workouts-osm-updater',0))`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal("waiting updater was mistaken for a stale writer:", err)
		}
	}
}
