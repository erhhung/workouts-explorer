package osm

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

func SyncCatalog(ctx context.Context, pool *pgxpool.Pool, regions []Region, configured []string) error {
	configuredSet := make(map[string]struct{}, len(configured))
	for _, id := range configured {
		configuredSet[id] = struct{}{}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin OSM catalog sync: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE osm_catalog.regions SET configured=false WHERE configured`); err != nil {
		return fmt.Errorf("reset configured OSM regions: %w", err)
	}
	for _, region := range regions {
		_, isConfigured := configuredSet[region.ID]
		if _, err := tx.Exec(ctx, `
			INSERT INTO osm_catalog.regions (
				id,provider,provider_region_id,display_name,catalog_url,source_url,
				advertised_download_bytes,boundary,configured
			) VALUES ($1,$2,$3,$4,$5,$6,$7,
				ST_Multi(ST_CollectionExtract(ST_MakeValid(ST_SetSRID(ST_GeomFromGeoJSON($8),4326)),3)),$9)
			ON CONFLICT (id) DO UPDATE SET
				display_name=EXCLUDED.display_name,
				catalog_url=EXCLUDED.catalog_url,
				source_url=EXCLUDED.source_url,
				advertised_download_bytes=EXCLUDED.advertised_download_bytes,
				boundary=EXCLUDED.boundary,
				configured=EXCLUDED.configured,
				updated_at=transaction_timestamp()`, region.ID, region.Provider, region.ProviderID,
			region.Name, region.CatalogURL, region.SourceURL, nullablePositive(region.AdvertisedBytes),
			region.GeometryJSON, isConfigured); err != nil {
			return fmt.Errorf("upsert OSM region %s: %w", region.ID, err)
		}
	}
	for id := range configuredSet {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM osm_catalog.regions WHERE id=$1 AND configured)`, id).Scan(&exists); err != nil {
			return fmt.Errorf("validate configured OSM region %s: %w", id, err)
		}
		if !exists {
			return fmt.Errorf("configured OSM region %s is unavailable from its provider", id)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit OSM catalog sync: %w", err)
	}
	return nil
}

func nullablePositive(value int64) any {
	if value > 0 {
		return value
	}
	return nil
}
