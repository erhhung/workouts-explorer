package osm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

func SyncCatalog(ctx context.Context, pool *pgxpool.Pool, regions []Region) error {
	providers := []string{}
	for _, region := range regions {
		found := false
		for _, provider := range providers {
			if provider == region.Provider {
				found = true
			}
		}
		if !found {
			providers = append(providers, region.Provider)
		}
	}
	// One statement per catalog, not one refresh of the metadata proof per row.
	type catalogEntry struct {
		ID, Provider, ProviderID, Name, CatalogURL, SourceURL string
		AdvertisedBytes                                       int64
		Boundary                                              json.RawMessage
		CatalogID, ParentID                                   string
	}
	entries := make([]catalogEntry, 0, len(regions))
	for _, region := range regions {
		entries = append(entries, catalogEntry{region.ID, region.Provider, region.ProviderID, region.Name, region.CatalogURL,
			region.SourceURL, region.AdvertisedBytes, region.GeometryJSON, region.CatalogID, region.ParentID})
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
	if _, err := tx.Exec(ctx, `
			INSERT INTO osm_catalog.regions AS stored (
				id,provider,provider_region_id,display_name,catalog_url,source_url,
				advertised_download_bytes,boundary
			) SELECT "ID","Provider","ProviderID","Name","CatalogURL","SourceURL",
				CASE WHEN "AdvertisedBytes">0 THEN "AdvertisedBytes" END,
				ST_Multi(ST_CollectionExtract(ST_MakeValid(ST_SetSRID(ST_GeomFromGeoJSON("Boundary"::text),4326)),3))
			FROM jsonb_to_recordset($1::jsonb) AS entry(
				"ID" text,"Provider" text,"ProviderID" text,"Name" text,"CatalogURL" text,"SourceURL" text,
				"AdvertisedBytes" bigint,"Boundary" jsonb)
			WHERE true
			ON CONFLICT (id) DO UPDATE SET
				display_name=EXCLUDED.display_name,
				catalog_url=EXCLUDED.catalog_url,
				source_url=EXCLUDED.source_url,
				advertised_download_bytes=COALESCE(EXCLUDED.advertised_download_bytes,stored.advertised_download_bytes),
				boundary=EXCLUDED.boundary,
				updated_at=transaction_timestamp()
			WHERE (stored.display_name,stored.catalog_url,stored.source_url,stored.advertised_download_bytes,
				stored.boundary_sha256) IS DISTINCT FROM
				(EXCLUDED.display_name,EXCLUDED.catalog_url,EXCLUDED.source_url,COALESCE(EXCLUDED.advertised_download_bytes,stored.advertised_download_bytes),
				EXCLUDED.boundary_sha256)`, encoded); err != nil {
		return fmt.Errorf("upsert OSM catalog: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE osm_catalog.regions stored SET
		provider_catalog_id=entry."CatalogID",provider_parent_id=NULLIF(entry."ParentID",''),
		catalog_checked_at=clock_timestamp(),catalog_available=true
		FROM jsonb_to_recordset($1::jsonb) AS entry("ID" text,"CatalogID" text,"ParentID" text)
		WHERE stored.id=entry."ID"`, encoded); err != nil {
		return fmt.Errorf("record OSM catalog observation: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE osm_catalog.regions SET catalog_available=false
		WHERE provider=ANY($2::text[]) AND NOT(id IN(SELECT entry->>'ID' FROM jsonb_array_elements($1::jsonb) entry))`, encoded, providers); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit OSM catalog sync: %w", err)
	}
	return nil
}
