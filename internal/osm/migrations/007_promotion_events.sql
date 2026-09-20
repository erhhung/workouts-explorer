-- +goose Up
CREATE TABLE osm_catalog.promotion_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    region_id text NOT NULL REFERENCES osm_catalog.regions(id),
    generation_id bigint NOT NULL REFERENCES osm_catalog.generations(id),
    importer_version integer NOT NULL CHECK (importer_version>0),
    derivation_version integer NOT NULL CHECK (derivation_version>0),
    source_sha256 text CHECK (source_sha256 ~ '^[0-9a-f]{64}$'),
    promoted_at timestamptz NOT NULL,
    recorded_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    UNIQUE (region_id,generation_id)
);
CREATE INDEX promotion_events_recorded_idx ON osm_catalog.promotion_events(id,region_id,generation_id);
REVOKE ALL ON osm_catalog.promotion_events FROM PUBLIC;
REVOKE ALL ON SEQUENCE osm_catalog.promotion_events_id_seq FROM PUBLIC;

-- +goose StatementBegin
CREATE FUNCTION osm_catalog.record_promotion_event()
RETURNS trigger LANGUAGE plpgsql SECURITY INVOKER SET search_path=pg_catalog,osm_catalog AS $$
BEGIN
    IF NEW.state='active' AND (TG_OP='INSERT' OR OLD.state<>'active') THEN
        INSERT INTO osm_catalog.promotion_events(region_id,generation_id,importer_version,derivation_version,source_sha256,promoted_at)
        VALUES(NEW.region_id,NEW.id,NEW.importer_version,NEW.derivation_version,NEW.source_sha256,NEW.promoted_at)
        ON CONFLICT (region_id,generation_id) DO NOTHING;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER generations_record_promotion_event
AFTER INSERT OR UPDATE OF state ON osm_catalog.generations
FOR EACH ROW EXECUTE FUNCTION osm_catalog.record_promotion_event();

INSERT INTO osm_catalog.promotion_events(region_id,generation_id,importer_version,derivation_version,source_sha256,promoted_at)
SELECT region_id,id,importer_version,derivation_version,source_sha256,promoted_at
FROM osm_catalog.generations WHERE state='active'
ON CONFLICT (region_id,generation_id) DO NOTHING;

-- +goose StatementBegin
CREATE FUNCTION osm_catalog.read_promotion_events(after_event_id bigint,event_limit integer)
RETURNS TABLE(event_id bigint,region_id text,generation_id bigint,importer_version integer,
    derivation_version integer,source_sha256 text,promoted_at timestamptz)
LANGUAGE sql STABLE SECURITY INVOKER SET search_path=pg_catalog,osm_catalog AS $$
    SELECT event.id,event.region_id,event.generation_id,event.importer_version,event.derivation_version,
           event.source_sha256,event.promoted_at
    FROM osm_catalog.promotion_events event
    WHERE event.id>GREATEST(after_event_id,0) AND event_limit BETWEEN 1 AND 1000
    ORDER BY event.id LIMIT LEAST(event_limit,1000)
$$;
-- +goose StatementEnd

CREATE FUNCTION osm_catalog.promotion_event_head()
RETURNS bigint LANGUAGE sql STABLE SECURITY INVOKER SET search_path=pg_catalog,osm_catalog AS $$
    SELECT COALESCE(max(event.id),0) FROM osm_catalog.promotion_events event
$$;

UPDATE osm_catalog.schema_metadata SET schema_version=7,minimum_runtime_version=6 WHERE singleton;

-- +goose Down
UPDATE osm_catalog.schema_metadata SET schema_version=6,minimum_runtime_version=1 WHERE singleton;
DROP FUNCTION osm_catalog.read_promotion_events(bigint,integer);
DROP FUNCTION osm_catalog.promotion_event_head();
DROP TRIGGER generations_record_promotion_event ON osm_catalog.generations;
DROP FUNCTION osm_catalog.record_promotion_event();
DROP TABLE osm_catalog.promotion_events;
