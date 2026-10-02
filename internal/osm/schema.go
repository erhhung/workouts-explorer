package osm

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	MinimumSchemaVersion   = 8
	SupportedSchemaVersion = 9
)

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
		   AND to_regclass('osm_catalog.region_storage') IS NOT NULL
		   AND to_regclass('osm_catalog.storage_gc') IS NOT NULL
		   AND to_regclass('osm_catalog.promotion_events') IS NOT NULL
		   AND to_regclass('osm_catalog.regions_boundary_gist') IS NOT NULL
		   AND to_regclass('osm_canonical.ways') IS NOT NULL
		   AND to_regclass('osm_canonical.localities') IS NOT NULL
		   AND to_regclass('osm_canonical.path_segments') IS NOT NULL
		   AND to_regclass('osm_active.ways') IS NOT NULL
		   AND to_regclass('osm_active.localities') IS NOT NULL
		   AND to_regclass('osm_active.path_segments') IS NOT NULL
		   AND to_regclass('osm_active.logical_paths') IS NOT NULL
		   AND EXISTS (
		       SELECT 1 FROM pg_index
		       WHERE indexrelid=to_regclass('osm_canonical.canonical_path_segments_start_graph_node_idx')
		         AND indisvalid AND indisready
		   )
		   AND EXISTS (
		       SELECT 1 FROM pg_index
		       WHERE indexrelid=to_regclass('osm_canonical.canonical_path_segments_end_graph_node_idx')
		         AND indisvalid AND indisready
		   )
		   AND to_regprocedure('osm_catalog.promote_region_generation(text,bigint,name,jsonb)') IS NOT NULL
		   AND to_regprocedure('osm_catalog.read_promotion_events(bigint,integer)') IS NOT NULL
		   AND to_regprocedure('osm_catalog.promotion_event_head()') IS NOT NULL
		   AND position(
		       'remainingConnectedAttributionSplits' IN pg_get_functiondef(
		           to_regprocedure('osm_catalog.promote_region_generation(text,bigint,name,jsonb)')
		       )
		   ) > 0
		   AND to_regclass('osm_catalog.timezone_datasets') IS NOT NULL
		   AND to_regclass('osm_catalog.timezone_geometries') IS NOT NULL
		   AND to_regprocedure('osm_active.timezone_at(double precision,double precision)') IS NOT NULL
		   AND EXISTS (SELECT 1 FROM osm_catalog.generations WHERE state = 'active')
		   AND NOT EXISTS (
		       SELECT 1
		       FROM osm_catalog.generations AS generation
		       LEFT JOIN osm_catalog.region_storage AS storage
		         ON storage.region_id = generation.region_id
		        AND storage.generation_id = generation.id
		       WHERE generation.state = 'active'
		         AND (storage.region_id IS NULL
		              OR to_regclass(format('osm_canonical.%I', storage.ways_relation)) IS NULL
		              OR to_regclass(format('osm_canonical.%I', storage.localities_relation)) IS NULL
		              OR to_regclass(format('osm_canonical.%I', storage.path_segments_relation)) IS NULL
		              OR NOT EXISTS (
		                  SELECT 1 FROM pg_inherits
		                  WHERE inhparent='osm_canonical.ways'::regclass
		                    AND inhrelid=to_regclass(format('osm_canonical.%I',storage.ways_relation))
		              )
		              OR NOT EXISTS (
		                  SELECT 1 FROM pg_inherits
		                  WHERE inhparent='osm_canonical.localities'::regclass
		                    AND inhrelid=to_regclass(format('osm_canonical.%I',storage.localities_relation))
		              )
		              OR NOT EXISTS (
		                  SELECT 1 FROM pg_inherits
		                  WHERE inhparent='osm_canonical.path_segments'::regclass
		                    AND inhrelid=to_regclass(format('osm_canonical.%I',storage.path_segments_relation))
		              ))
		   )
		   AND NOT EXISTS (
		       SELECT 1
		       FROM osm_catalog.generations AS generation
		       WHERE generation.state = 'active'
		         AND (
		             NOT EXISTS (
		                 SELECT 1 FROM osm_canonical.ways AS way
		                 WHERE way.region_id = generation.region_id
		                   AND way.generation_id = generation.id
		             )
		             OR NOT EXISTS (
		                 SELECT 1 FROM osm_canonical.path_segments AS segment
		                 WHERE segment.region_id = generation.region_id
		                   AND segment.generation_id = generation.id
		             )
		         )
		   )
		   AND EXISTS (
		       SELECT 1 FROM osm_catalog.schema_metadata
		       WHERE singleton AND schema_version BETWEEN $1 AND $2 AND minimum_runtime_version <= $2
		   )`, MinimumSchemaVersion, SupportedSchemaVersion).Scan(&ready)
	return err == nil && ready
}
