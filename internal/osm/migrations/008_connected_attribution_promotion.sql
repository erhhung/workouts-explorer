-- +goose Up
ALTER FUNCTION osm_catalog.promote_region_generation(text,bigint,name,jsonb)
RENAME TO promote_region_generation_v7;

-- +goose StatementBegin
CREATE FUNCTION osm_catalog.promote_region_generation(
    promote_region_id text, promote_generation_id bigint,
    promote_schema_name name, promote_validation jsonb
)
RETURNS void
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path=pg_catalog,osm_catalog
AS $function$
BEGIN
    IF promote_validation @> jsonb_build_object(
        'remainingConnectedAttributionSplits',0
    ) IS NOT TRUE THEN
        RAISE EXCEPTION 'generation % has connected attribution identity splits',promote_generation_id;
    END IF;
    PERFORM osm_catalog.promote_region_generation_v7(
        promote_region_id,promote_generation_id,promote_schema_name,promote_validation
    );
END;
$function$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION osm_catalog.promote_region_generation(text,bigint,name,jsonb) FROM PUBLIC;
UPDATE osm_catalog.schema_metadata SET schema_version=8,minimum_runtime_version=8 WHERE singleton;

-- +goose Down
UPDATE osm_catalog.schema_metadata SET schema_version=7,minimum_runtime_version=6 WHERE singleton;
DROP FUNCTION osm_catalog.promote_region_generation(text,bigint,name,jsonb);
ALTER FUNCTION osm_catalog.promote_region_generation_v7(text,bigint,name,jsonb)
RENAME TO promote_region_generation;
