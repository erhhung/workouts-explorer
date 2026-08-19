package osm

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

const SupportedSchemaVersion = 1

func Ready(ctx context.Context, pool *pgxpool.Pool) bool {
	if pool == nil {
		return false
	}
	var ready bool
	err := pool.QueryRow(ctx, `
		SELECT current_database() = 'osm'
		   AND EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'postgis')
		   AND to_regclass('osm_catalog.schema_metadata') IS NOT NULL
		   AND to_regclass('osm_catalog.regions') IS NOT NULL
		   AND to_regclass('osm_catalog.generations') IS NOT NULL
		   AND to_regclass('osm_active.path_segments') IS NOT NULL
		   AND to_regclass('osm_active.logical_paths') IS NOT NULL
		   AND EXISTS (SELECT 1 FROM osm_catalog.generations WHERE state = 'active')
		   AND EXISTS (
		       SELECT 1 FROM osm_catalog.schema_metadata
		       WHERE singleton AND schema_version >= $1 AND minimum_runtime_version <= $1
		   )`, SupportedSchemaVersion).Scan(&ready)
	return err == nil && ready
}
