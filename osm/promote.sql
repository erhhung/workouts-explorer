BEGIN;

UPDATE osm_catalog.generations
SET state = 'retired', retired_at = transaction_timestamp()
WHERE region_id = :'OSM_REGION_ID'
  AND id <> :'OSM_GENERATION_ID'::bigint
  AND state = 'active';

WITH updated AS (
    UPDATE osm_catalog.generations
    SET state = 'active',
        validation = :'OSM_VALIDATION'::jsonb,
        validated_at = transaction_timestamp(),
        promoted_at = transaction_timestamp()
    WHERE id = :'OSM_GENERATION_ID'::bigint
      AND region_id = :'OSM_REGION_ID'
      AND schema_name = :'OSM_BUILD_SCHEMA'::name
      AND state IN ('building', 'validating', 'active')
    RETURNING 1
)
SELECT 1 / count(*) FROM updated;

CREATE OR REPLACE VIEW osm_active.ways AS
SELECT * FROM :"OSM_BUILD_SCHEMA".ways;

CREATE OR REPLACE VIEW osm_active.localities AS
SELECT * FROM :"OSM_BUILD_SCHEMA".localities;

CREATE OR REPLACE VIEW osm_active.path_segments AS
SELECT * FROM :"OSM_BUILD_SCHEMA".path_segments;

CREATE OR REPLACE VIEW osm_active.logical_paths AS
SELECT * FROM :"OSM_BUILD_SCHEMA".logical_paths;

COMMIT;
