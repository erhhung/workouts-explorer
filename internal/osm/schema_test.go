package osm

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestSupportedSchemaVersion(t *testing.T) {
	if MinimumSchemaVersion != 8 {
		t.Fatalf("MinimumSchemaVersion = %d, want 8", MinimumSchemaVersion)
	}
	if SupportedSchemaVersion != 9 {
		t.Fatalf("SupportedSchemaVersion = %d, want 9", SupportedSchemaVersion)
	}
}

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
	regions, err := ConfiguredRegions(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.IsSortedFunc(regions, func(a, b ConfiguredRegion) int { return strings.Compare(a.RegionID, b.RegionID) }) {
		t.Fatalf("configured regions are not sorted: %+v", regions)
	}
	for _, region := range regions {
		if region.RegionID == "" || region.DisplayName == "" {
			t.Fatalf("invalid configured region: %+v", region)
		}
	}
}
