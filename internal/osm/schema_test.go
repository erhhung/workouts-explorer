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
	if MinimumSchemaVersion != 10 {
		t.Fatalf("MinimumSchemaVersion = %d, want 10", MinimumSchemaVersion)
	}
	if SupportedSchemaVersion != 10 {
		t.Fatalf("SupportedSchemaVersion = %d, want 10", SupportedSchemaVersion)
	}
}

func TestRouteCatalogReadinessContract(t *testing.T) {
	for _, required := range []string{
		"osm_catalog.route_region_catalog_state", "revision >= 1", "octet_length(fingerprint) = 32",
		"octet_length(selection_fingerprint) = 32", "jsonb_typeof(active_generations) = 'object'",
		"jsonb_typeof(configured_regions) = 'array'", "'boundary_sha256','geographic_area_m2'",
		"attgenerated = 's' AND attnotnull", "tgenabled = 'O'", "count(*) = 7",
		"regions_lock_route_catalog", "regions_refresh_route_catalog", "generations_lock_route_catalog",
		"generations_refresh_route_catalog", "route_catalog_deny_truncate",
		"schema_version BETWEEN $1 AND $2 AND minimum_runtime_version BETWEEN $1 AND $2",
	} {
		if !strings.Contains(osmReadinessSQL, required) {
			t.Errorf("OSM readiness lacks %q", required)
		}
	}
	if strings.Contains(osmReadinessSQL, "ST_Area") || strings.Contains(osmReadinessSQL, "ST_AsEWKB") {
		t.Fatal("startup readiness must not recompute boundary metadata")
	}
	if Ready(context.Background(), nil) {
		t.Fatal("missing OSM pool is ready")
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
