package osm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

func SyncCatalog(ctx context.Context, pool *pgxpool.Pool, regions []Region, configured []string) error {
	configuredSet := make(map[string]struct{}, len(configured))
	configuredFound := make(map[string]struct{}, len(configured))
	for _, id := range configured {
		configuredSet[id] = struct{}{}
	}
	// One statement per catalog, not one refresh of the metadata proof per row.
	type catalogEntry struct {
		ID, Provider, ProviderID, Name, CatalogURL, SourceURL string
		AdvertisedBytes                                       int64
		Boundary                                              json.RawMessage
		Configured                                            bool
	}
	entries := make([]catalogEntry, 0, len(regions))
	for _, region := range regions {
		_, isConfigured := configuredSet[region.ID]
		if isConfigured {
			configuredFound[region.ID] = struct{}{}
		}
		entries = append(entries, catalogEntry{region.ID, region.Provider, region.ProviderID, region.Name, region.CatalogURL,
			region.SourceURL, region.AdvertisedBytes, region.GeometryJSON, isConfigured})
	}
	for id := range configuredSet {
		if _, exists := configuredFound[id]; !exists {
			return fmt.Errorf("configured OSM region %s is unavailable from its provider", id)
		}
	}
	encoded, err := json.Marshal(entries)
	if err != nil {
		return fmt.Errorf("encode OSM catalog: %w", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin OSM catalog sync: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `UPDATE osm_catalog.regions SET configured=false
		WHERE configured AND NOT (id=ANY(COALESCE($1::text[],'{}'::text[])))`, configured); err != nil {
		return fmt.Errorf("reset configured OSM regions: %w", err)
	}
	if _, err := tx.Exec(ctx, `
			INSERT INTO osm_catalog.regions AS stored (
				id,provider,provider_region_id,display_name,catalog_url,source_url,
				advertised_download_bytes,boundary,configured
			) SELECT "ID","Provider","ProviderID","Name","CatalogURL","SourceURL",
				CASE WHEN "AdvertisedBytes">0 THEN "AdvertisedBytes" END,
				ST_Multi(ST_CollectionExtract(ST_MakeValid(ST_SetSRID(ST_GeomFromGeoJSON("Boundary"::text),4326)),3)),"Configured"
			FROM jsonb_to_recordset($1::jsonb) AS entry(
				"ID" text,"Provider" text,"ProviderID" text,"Name" text,"CatalogURL" text,"SourceURL" text,
				"AdvertisedBytes" bigint,"Boundary" jsonb,"Configured" boolean)
			WHERE true
			ON CONFLICT (id) DO UPDATE SET
				display_name=EXCLUDED.display_name,
				catalog_url=EXCLUDED.catalog_url,
				source_url=EXCLUDED.source_url,
				advertised_download_bytes=EXCLUDED.advertised_download_bytes,
				boundary=EXCLUDED.boundary,
				configured=EXCLUDED.configured,
				updated_at=transaction_timestamp()
			WHERE (stored.display_name,stored.catalog_url,stored.source_url,stored.advertised_download_bytes,
				stored.boundary_sha256,stored.configured) IS DISTINCT FROM
				(EXCLUDED.display_name,EXCLUDED.catalog_url,EXCLUDED.source_url,EXCLUDED.advertised_download_bytes,
				EXCLUDED.boundary_sha256,EXCLUDED.configured)`, encoded); err != nil {
		return fmt.Errorf("upsert OSM catalog: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit OSM catalog sync: %w", err)
	}
	return nil
}
