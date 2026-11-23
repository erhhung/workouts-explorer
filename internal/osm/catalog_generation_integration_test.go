package osm

import (
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"testing"
)

func TestCatalogRegionalGenerationsReadOnly(t *testing.T) {
	raw := os.Getenv("OSM_CATALOG_GENERATION_TEST_URL")
	if raw == "" {
		t.Skip("explicit read-only catalog URL required")
	}
	config, err := pgxpool.ParseConfig(raw)
	if err != nil {
		t.Fatal("invalid catalog verification connection")
	}
	config.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	config.ConnConfig.ValidateConnect = nil
	ctx := context.Background()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	regions, err := CatalogRegions(ctx, pool, "geofabrik")
	if err != nil {
		t.Fatal(err)
	}
	verified := 0
	for _, region := range regions {
		if region.BuildID == nil {
			continue
		}
		var expected int64
		err = pool.QueryRow(ctx, `SELECT count(*) FROM osm_catalog.generations WHERE region_id=$1 AND promoted_at IS NOT NULL AND id<=$2`, region.ID, *region.BuildID).Scan(&expected)
		if err != nil || region.Generation == nil || *region.Generation != expected {
			t.Fatalf("regional promotion number mismatch for %s", region.ID)
		}
		if region.ID == "geofabrik:us/us-virgin-islands" && *region.BuildID == 65 && *region.Generation != 1 {
			t.Fatal("failed first attempt consumed a generation")
		}
		verified++
	}
	if verified < 2 {
		t.Fatal("multiple promoted regions required")
	}
}
