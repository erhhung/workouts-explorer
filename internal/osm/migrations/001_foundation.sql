-- +goose Up
CREATE SCHEMA osm_catalog;
REVOKE ALL ON SCHEMA osm_catalog FROM PUBLIC;
CREATE SCHEMA osm_active;
REVOKE ALL ON SCHEMA osm_active FROM PUBLIC;

CREATE TABLE osm_catalog.schema_metadata (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    schema_version integer NOT NULL CHECK (schema_version >= 1),
    minimum_runtime_version integer NOT NULL CHECK (
        minimum_runtime_version >= 1 AND minimum_runtime_version <= schema_version
    )
);
INSERT INTO osm_catalog.schema_metadata (schema_version, minimum_runtime_version)
VALUES (1, 1);

CREATE TABLE osm_catalog.regions (
    id text PRIMARY KEY CHECK (id ~ '^[a-z][a-z0-9-]{0,63}:[a-z][a-z0-9-]{0,63}$'),
    provider text NOT NULL CHECK (provider ~ '^[a-z][a-z0-9-]{0,63}$'),
    provider_region_id text NOT NULL CHECK (provider_region_id ~ '^[a-z][a-z0-9-]{0,63}$'),
    display_name text NOT NULL CHECK (length(display_name) BETWEEN 1 AND 256),
    catalog_url text NOT NULL CHECK (catalog_url LIKE 'https://%'),
    source_url text NOT NULL CHECK (source_url LIKE 'https://%'),
    advertised_download_bytes bigint CHECK (advertised_download_bytes > 0),
    boundary geometry(MultiPolygon, 4326) NOT NULL,
    configured boolean NOT NULL DEFAULT false,
    discovered_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    UNIQUE (provider, provider_region_id),
    CHECK (id = provider || ':' || provider_region_id),
    CHECK (ST_IsValid(boundary) AND NOT ST_IsEmpty(boundary))
);
CREATE INDEX regions_boundary_gist ON osm_catalog.regions USING gist (boundary);

CREATE TABLE osm_catalog.generations (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    region_id text NOT NULL REFERENCES osm_catalog.regions (id),
    state text NOT NULL CHECK (state IN ('building', 'validating', 'active', 'retired', 'failed')),
    schema_name name NOT NULL UNIQUE CHECK (schema_name::text ~ '^osm_build_[0-9]+$'),
    source_url text NOT NULL CHECK (source_url LIKE 'https://%'),
    downloaded_bytes bigint CHECK (downloaded_bytes > 0),
    source_sha256 text CHECK (source_sha256 ~ '^[0-9a-f]{64}$'),
    source_header_timestamp timestamptz,
    osmium_version text,
    osm2pgsql_version text,
    importer_version integer NOT NULL CHECK (importer_version > 0),
    derivation_version integer NOT NULL CHECK (derivation_version > 0),
    validation jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(validation) = 'object'),
    failure_summary text CHECK (failure_summary IS NULL OR length(failure_summary) <= 512),
    started_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    validated_at timestamptz,
    promoted_at timestamptz,
    retired_at timestamptz,
    CHECK ((state IN ('active', 'retired')) = (promoted_at IS NOT NULL)),
    CHECK (state <> 'retired' OR retired_at IS NOT NULL),
    CHECK (state <> 'failed' OR failure_summary IS NOT NULL)
);
CREATE UNIQUE INDEX generations_one_active_region_idx
ON osm_catalog.generations (region_id) WHERE state = 'active';
CREATE UNIQUE INDEX generations_one_incomplete_region_idx
ON osm_catalog.generations (region_id) WHERE state IN ('building', 'validating');

REVOKE ALL ON ALL TABLES IN SCHEMA osm_catalog FROM PUBLIC;
REVOKE ALL ON ALL SEQUENCES IN SCHEMA osm_catalog FROM PUBLIC;

-- +goose Down
DROP SCHEMA osm_active;
DROP SCHEMA osm_catalog CASCADE;
