-- +goose Up
CREATE TABLE osm_catalog.timezone_datasets (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    provider text NOT NULL CHECK (provider ~ '^[a-z][a-z0-9-]{0,63}$'),
    release text NOT NULL CHECK (length(release) BETWEEN 1 AND 128),
    source_url text NOT NULL CHECK (source_url LIKE 'https://%'),
    source_sha256 text NOT NULL CHECK (source_sha256 ~ '^[0-9a-f]{64}$'),
    boundary_count bigint NOT NULL DEFAULT 0 CHECK (boundary_count >= 0),
    state text NOT NULL DEFAULT 'building' CHECK (state IN ('building', 'active', 'retired')),
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    promoted_at timestamptz,
    retired_at timestamptz,
    UNIQUE (provider, release, source_sha256),
    CHECK ((state = 'active') = (promoted_at IS NOT NULL AND retired_at IS NULL)),
    CHECK (state <> 'retired' OR (promoted_at IS NOT NULL AND retired_at IS NOT NULL))
);
CREATE UNIQUE INDEX timezone_datasets_one_active_idx
ON osm_catalog.timezone_datasets ((state)) WHERE state = 'active';

CREATE TABLE osm_catalog.timezone_geometries (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    dataset_id bigint NOT NULL REFERENCES osm_catalog.timezone_datasets (id) ON DELETE CASCADE,
    tzid text NOT NULL CHECK (
        length(tzid) BETWEEN 1 AND 255
        AND tzid ~ '^[A-Za-z0-9._+-]+(/[A-Za-z0-9._+-]+)+$'
    ),
    boundary geometry(MultiPolygon, 4326) NOT NULL,
    CHECK (NOT ST_IsEmpty(boundary) AND ST_IsValid(boundary))
);
CREATE INDEX timezone_geometries_boundary_gist
ON osm_catalog.timezone_geometries USING gist (boundary);
CREATE INDEX timezone_geometries_dataset_idx
ON osm_catalog.timezone_geometries (dataset_id);

-- +goose StatementBegin
CREATE FUNCTION osm_catalog.promote_timezone_dataset(promote_id bigint)
RETURNS void
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = pg_catalog, public
AS $$
DECLARE
    expected_count bigint;
    actual_count bigint;
    previous_ids bigint[];
BEGIN
    LOCK TABLE osm_catalog.timezone_datasets IN SHARE ROW EXCLUSIVE MODE;

    SELECT boundary_count INTO expected_count
    FROM osm_catalog.timezone_datasets
    WHERE id = promote_id AND state = 'building'
    FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'timezone dataset % is not building', promote_id;
    END IF;

    SELECT count(*) INTO actual_count
    FROM osm_catalog.timezone_geometries
    WHERE dataset_id = promote_id;
    IF actual_count = 0 OR actual_count <> expected_count THEN
        RAISE EXCEPTION 'timezone dataset % has % geometries, expected %',
            promote_id, actual_count, expected_count;
    END IF;

    SELECT coalesce(array_agg(id), '{}'::bigint[]) INTO previous_ids
    FROM osm_catalog.timezone_datasets
    WHERE state = 'active';

    UPDATE osm_catalog.timezone_datasets
    SET state = 'retired', retired_at = transaction_timestamp()
    WHERE state = 'active';

    UPDATE osm_catalog.timezone_datasets
    SET state = 'active', promoted_at = transaction_timestamp()
    WHERE id = promote_id;

    DELETE FROM osm_catalog.timezone_geometries
    WHERE dataset_id = ANY(previous_ids);
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION osm_active.timezone_at(longitude double precision, latitude double precision)
RETURNS text
LANGUAGE sql
STABLE
STRICT
PARALLEL SAFE
AS $$
    WITH point AS (
        SELECT ST_SetSRID(ST_MakePoint(longitude, latitude), 4326) AS geom
        WHERE longitude BETWEEN -180 AND 180 AND latitude BETWEEN -90 AND 90
    )
    SELECT geometry.tzid
    FROM point
    JOIN osm_catalog.timezone_datasets dataset ON dataset.state = 'active'
    JOIN osm_catalog.timezone_geometries geometry ON geometry.dataset_id = dataset.id
    WHERE geometry.boundary && point.geom
      AND ST_Covers(geometry.boundary, point.geom)
    ORDER BY ST_Area(geometry.boundary::geography), geometry.tzid, geometry.id
    LIMIT 1
$$;
-- +goose StatementEnd

UPDATE osm_catalog.schema_metadata
SET schema_version = 2
WHERE singleton;

REVOKE ALL ON TABLE osm_catalog.timezone_datasets FROM PUBLIC;
REVOKE ALL ON TABLE osm_catalog.timezone_geometries FROM PUBLIC;
REVOKE ALL ON SEQUENCE osm_catalog.timezone_datasets_id_seq FROM PUBLIC;
REVOKE ALL ON SEQUENCE osm_catalog.timezone_geometries_id_seq FROM PUBLIC;
REVOKE ALL ON FUNCTION osm_catalog.promote_timezone_dataset(bigint) FROM PUBLIC;

-- +goose Down
DROP FUNCTION osm_active.timezone_at(double precision, double precision);
DROP FUNCTION osm_catalog.promote_timezone_dataset(bigint);
DROP TABLE osm_catalog.timezone_geometries;
DROP TABLE osm_catalog.timezone_datasets;
UPDATE osm_catalog.schema_metadata
SET schema_version = 1
WHERE singleton;
