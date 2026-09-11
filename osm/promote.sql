BEGIN;

SELECT osm_catalog.promote_region_generation(
    :'OSM_REGION_ID',
    :'OSM_GENERATION_ID'::bigint,
    :'OSM_BUILD_SCHEMA'::name,
    :'OSM_VALIDATION'::jsonb
);

COMMIT;
