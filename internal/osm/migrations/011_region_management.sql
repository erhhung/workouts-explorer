-- +goose Up
-- Region catalog metadata, managed processing, cleanup, promotion numbering and statistics receipts.
ALTER TABLE osm_catalog.regions
    DROP CONSTRAINT regions_id_check,
    DROP CONSTRAINT regions_provider_region_id_check;
ALTER TABLE osm_catalog.regions
    ADD CONSTRAINT regions_id_check CHECK (id ~ '^[a-z][a-z0-9-]{0,63}:[a-z][a-z0-9/-]{0,191}$'),
    ADD CONSTRAINT regions_provider_region_id_check CHECK (provider_region_id ~ '^[a-z][a-z0-9/-]{0,191}$'),
    ADD COLUMN provider_catalog_id text,
    ADD COLUMN provider_parent_id text,
    ADD COLUMN catalog_checked_at timestamptz,
    ADD COLUMN catalog_available boolean NOT NULL DEFAULT true,
    ADD COLUMN metadata_checked_at timestamptz,
    ADD COLUMN metadata_attempted_at timestamptz,
    ADD COLUMN source_last_modified timestamptz,
    ADD COLUMN metadata_error_code text CHECK (length(metadata_error_code)<=64);
ALTER TABLE osm_catalog.generations
    ADD COLUMN downloaded_at timestamptz,
    ADD COLUMN processed_database_bytes bigint CHECK (processed_database_bytes>=0),
    ADD COLUMN database_measured_at timestamptz;

-- Capture completion from the established RecordDownload update too, so an
-- existing updater image does not fabricate a date from generation start time.
-- +goose StatementBegin
CREATE FUNCTION osm_catalog.record_download_completed_at()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog AS $function$
BEGIN
    IF NEW.downloaded_bytes IS NOT NULL AND
       (OLD.downloaded_bytes,OLD.source_sha256) IS DISTINCT FROM (NEW.downloaded_bytes,NEW.source_sha256) THEN
        NEW.downloaded_at=clock_timestamp();
    END IF;
    RETURN NEW;
END;
$function$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION osm_catalog.record_download_completed_at() FROM PUBLIC;
CREATE TRIGGER generation_record_download_time BEFORE UPDATE OF downloaded_bytes,source_sha256 ON osm_catalog.generations
    FOR EACH ROW EXECUTE FUNCTION osm_catalog.record_download_completed_at();

-- +goose StatementBegin
CREATE FUNCTION osm_catalog.region_database_bytes(target_region text)
RETURNS bigint LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,osm_catalog AS $function$
    SELECT pg_total_relation_size(to_regclass(format('osm_canonical.%I',ways_relation)))
        +pg_total_relation_size(to_regclass(format('osm_canonical.%I',localities_relation)))
        +pg_total_relation_size(to_regclass(format('osm_canonical.%I',path_segments_relation)))
    FROM osm_catalog.region_storage WHERE region_id=target_region;
$function$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION osm_catalog.region_database_bytes(text) FROM PUBLIC;

-- +goose StatementBegin
CREATE FUNCTION osm_catalog.record_region_database_size()
RETURNS trigger LANGUAGE plpgsql SET search_path=pg_catalog,osm_catalog AS $function$
DECLARE
    storage osm_catalog.region_storage%ROWTYPE;
BEGIN
    SELECT * INTO storage FROM osm_catalog.region_storage WHERE region_id=NEW.region_id;
    UPDATE osm_catalog.generations SET
        processed_database_bytes=osm_catalog.region_database_bytes(NEW.region_id),
        database_measured_at=clock_timestamp()
    WHERE id=NEW.generation_id AND state='active';
    RETURN NULL;
END;
$function$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION osm_catalog.record_region_database_size() FROM PUBLIC;
CREATE TRIGGER region_storage_record_database_size AFTER INSERT OR UPDATE ON osm_catalog.region_storage
    FOR EACH ROW EXECUTE FUNCTION osm_catalog.record_region_database_size();

-- +goose StatementBegin
DO $grant$
BEGIN
    IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='workouts_osm_catalog') THEN
        GRANT USAGE ON SCHEMA osm_catalog TO workouts_osm_catalog;
        GRANT SELECT ON osm_catalog.regions,osm_catalog.generations,osm_catalog.region_storage TO workouts_osm_catalog;
        GRANT INSERT,UPDATE ON osm_catalog.regions TO workouts_osm_catalog;
        GRANT EXECUTE ON FUNCTION osm_catalog.region_database_bytes(text) TO workouts_osm_catalog;
    END IF;
END;
$grant$;
-- +goose StatementEnd

ALTER TABLE osm_catalog.generations ADD COLUMN control_job_id uuid;
CREATE UNIQUE INDEX generations_control_job ON osm_catalog.generations(control_job_id) WHERE control_job_id IS NOT NULL AND state IN('building','validating','evaluating');
-- +goose StatementBegin
DO $grant$ BEGIN
    IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='workouts_osm_catalog') THEN
        GRANT SELECT ON osm_catalog.generation_stages TO workouts_osm_catalog;
    END IF;
END; $grant$;
-- +goose StatementEnd

CREATE TABLE osm_catalog.region_deletions (
    id uuid PRIMARY KEY,
    region_id text NOT NULL REFERENCES osm_catalog.regions(id),
    state text NOT NULL CHECK(state IN('reclaiming','completed')),
    database_bytes_before bigint NOT NULL CHECK(database_bytes_before>=0),
    database_bytes_after bigint CHECK(database_bytes_after>=0),
    started_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    finished_at timestamptz,
    CHECK((state='completed')=(finished_at IS NOT NULL)),
    CHECK(state<>'completed' OR database_bytes_after=0)
);
REVOKE ALL ON osm_catalog.region_deletions FROM PUBLIC;
-- +goose StatementBegin
DO $grant$ BEGIN
    IF EXISTS(SELECT 1 FROM pg_roles WHERE rolname='workouts_osm_catalog') THEN
        GRANT SELECT ON osm_catalog.region_deletions,osm_catalog.storage_gc TO workouts_osm_catalog;
        GRANT USAGE ON SCHEMA osm_canonical TO workouts_osm_catalog;
    END IF;
END; $grant$;
-- +goose StatementEnd

ALTER TABLE osm_catalog.regions ADD COLUMN generation_high_watermark bigint NOT NULL DEFAULT 0 CHECK(generation_high_watermark>=0);
ALTER TABLE osm_catalog.generations ADD COLUMN promotion_number bigint CHECK(promotion_number>0);
WITH numbered AS (
    SELECT id,row_number() OVER(PARTITION BY region_id ORDER BY promoted_at,id) AS number
    FROM osm_catalog.generations WHERE promoted_at IS NOT NULL
)
UPDATE osm_catalog.generations g SET promotion_number=numbered.number FROM numbered WHERE numbered.id=g.id;
UPDATE osm_catalog.regions r SET generation_high_watermark=COALESCE((
    SELECT max(g.promotion_number) FROM osm_catalog.generations g WHERE g.region_id=r.id),0);
CREATE UNIQUE INDEX generations_region_promotion_number ON osm_catalog.generations(region_id,promotion_number) WHERE promotion_number IS NOT NULL;
-- +goose StatementBegin
CREATE FUNCTION osm_catalog.number_region_promotion() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,osm_catalog AS $function$
BEGIN
    IF TG_OP='UPDATE' AND OLD.promotion_number IS NOT NULL THEN
        IF NEW.promotion_number IS DISTINCT FROM OLD.promotion_number OR NEW.region_id IS DISTINCT FROM OLD.region_id THEN
            RAISE EXCEPTION 'promoted region identity is immutable';
        END IF;
    ELSIF NEW.state='active' THEN
        UPDATE osm_catalog.regions SET generation_high_watermark=generation_high_watermark+1 WHERE id=NEW.region_id
            RETURNING generation_high_watermark INTO NEW.promotion_number;
        IF NOT FOUND THEN RAISE EXCEPTION 'missing promoted region'; END IF;
    ELSIF NEW.promotion_number IS NOT NULL THEN
        RAISE EXCEPTION 'only a promoted region can receive a generation number';
    END IF;
    RETURN NEW;
END;
$function$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION osm_catalog.number_region_promotion() FROM PUBLIC;
CREATE TRIGGER generations_number_promotion BEFORE INSERT OR UPDATE ON osm_catalog.generations
    FOR EACH ROW EXECUTE FUNCTION osm_catalog.number_region_promotion();

CREATE TABLE osm_catalog.statistics_maintenance (
    singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
    analyzed_generations jsonb CHECK(jsonb_typeof(analyzed_generations)='object'),
    policy_version integer NOT NULL DEFAULT 1 CHECK(policy_version>0),
    analyzed_at timestamptz
);
REVOKE ALL ON osm_catalog.statistics_maintenance FROM PUBLIC;
-- Adopt already-current statistics using catalog timestamps only; do not scan
-- map data during migration. Uncertain/interrupted maintenance stays pending.
INSERT INTO osm_catalog.statistics_maintenance(singleton,analyzed_generations,analyzed_at)
SELECT true,CASE WHEN catalog.active_generations='{}'::jsonb OR NOT EXISTS(
    SELECT 1 FROM (
        SELECT relid FROM pg_partition_tree('osm_canonical.ways')
        UNION SELECT relid FROM pg_partition_tree('osm_canonical.localities')
        UNION SELECT relid FROM pg_partition_tree('osm_canonical.path_segments')
    ) relation LEFT JOIN pg_stat_all_tables stats ON stats.relid=relation.relid
    WHERE stats.last_analyze IS NULL OR stats.last_analyze<COALESCE((
        SELECT max(promoted_at) FROM osm_catalog.generations WHERE state='active'),'-infinity'::timestamptz)
) THEN catalog.active_generations END,clock_timestamp()
FROM osm_catalog.route_region_catalog_state catalog WHERE singleton;
UPDATE osm_catalog.schema_metadata SET schema_version=11 WHERE singleton;

-- +goose Down
DROP TABLE osm_catalog.statistics_maintenance;

DROP TRIGGER generations_number_promotion ON osm_catalog.generations;
DROP FUNCTION osm_catalog.number_region_promotion();
ALTER TABLE osm_catalog.generations DROP COLUMN promotion_number;
ALTER TABLE osm_catalog.regions DROP COLUMN generation_high_watermark;

DROP TABLE osm_catalog.region_deletions;

DROP INDEX osm_catalog.generations_control_job;
ALTER TABLE osm_catalog.generations DROP COLUMN control_job_id;

DROP TRIGGER region_storage_record_database_size ON osm_catalog.region_storage;
DROP TRIGGER generation_record_download_time ON osm_catalog.generations;
DROP FUNCTION osm_catalog.record_download_completed_at();
DROP FUNCTION osm_catalog.record_region_database_size();
DROP FUNCTION osm_catalog.region_database_bytes(text);
ALTER TABLE osm_catalog.generations DROP COLUMN downloaded_at,DROP COLUMN processed_database_bytes,DROP COLUMN database_measured_at;
ALTER TABLE osm_catalog.regions DROP COLUMN provider_catalog_id,DROP COLUMN provider_parent_id,
    DROP COLUMN catalog_checked_at,DROP COLUMN catalog_available,DROP COLUMN metadata_checked_at,
    DROP COLUMN metadata_attempted_at,DROP COLUMN source_last_modified,DROP COLUMN metadata_error_code;
UPDATE osm_catalog.schema_metadata SET schema_version=10 WHERE singleton;
