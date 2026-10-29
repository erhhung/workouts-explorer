-- +goose Up
-- PostgreSQL checks generated expressions for immutability. Do not substitute
-- ST_AsBinary (no SRID) or native-endian EWKB: existing cohort proofs use NDR.
ALTER TABLE osm_catalog.regions
    ADD COLUMN boundary_sha256 bytea GENERATED ALWAYS AS
        (pg_catalog.sha256(public.ST_AsEWKB(boundary, 'NDR'))) STORED NOT NULL,
    ADD COLUMN geographic_area_m2 double precision GENERATED ALWAYS AS
        (public.ST_Area(boundary::public.geography)) STORED NOT NULL;

CREATE TABLE osm_catalog.route_region_catalog_state (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    revision bigint NOT NULL CHECK (revision >= 1),
    fingerprint bytea NOT NULL CHECK (octet_length(fingerprint) = 32),
    selection_fingerprint bytea NOT NULL CHECK (octet_length(selection_fingerprint) = 32),
    active_generations jsonb NOT NULL CHECK (jsonb_typeof(active_generations) = 'object'),
    configured_regions jsonb NOT NULL CHECK (jsonb_typeof(configured_regions) = 'array'),
    region_names jsonb NOT NULL CHECK (jsonb_typeof(region_names) = 'object')
);

REVOKE ALL ON osm_catalog.route_region_catalog_state FROM PUBLIC;

INSERT INTO osm_catalog.route_region_catalog_state
    (revision, fingerprint, selection_fingerprint, active_generations, configured_regions, region_names)
VALUES (1, sha256(convert_to('route-region-catalog-v1', 'UTF8')),
    sha256(convert_to('route-region-selection-v1', 'UTF8')), '{}', '[]', '{}');

-- +goose StatementBegin
CREATE FUNCTION osm_catalog.lock_route_region_catalog()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, osm_catalog
AS $function$
BEGIN
    -- Lock BEFORE source writes as well as before computing the proof. The lock
    -- lives until commit; a stale REPEATABLE READ writer gets SQLSTATE 40001.
    PERFORM 1 FROM osm_catalog.route_region_catalog_state WHERE singleton FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'missing OSM route region catalog state';
    END IF;
    RETURN NULL;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION osm_catalog.refresh_route_region_catalog()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, osm_catalog
AS $function$
DECLARE
    full_proof bytea;
    selection_proof bytea;
    heads jsonb;
    configured_names jsonb;
    names jsonb;
BEGIN
    PERFORM 1 FROM osm_catalog.route_region_catalog_state WHERE singleton FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'missing OSM route region catalog state';
    END IF;

    SELECT sha256(convert_to('route-region-catalog-v1', 'UTF8') || COALESCE(string_agg(
            sha256(int4send(octet_length(convert_to(region.id, 'UTF8'))) ||
                convert_to(region.id, 'UTF8') || boolsend(region.configured) ||
                region.boundary_sha256 || int8send(COALESCE(generation.id, 0))),
            ''::bytea ORDER BY region.id COLLATE "C"), ''::bytea)),
        sha256(convert_to('route-region-selection-v1', 'UTF8') || COALESCE(string_agg(
            sha256(int4send(octet_length(convert_to(region.id, 'UTF8'))) ||
                convert_to(region.id, 'UTF8') || boolsend(region.configured) ||
                region.boundary_sha256 || boolsend(region.configured AND generation.id IS NOT NULL)),
            ''::bytea ORDER BY region.id COLLATE "C"), ''::bytea)),
        COALESCE(jsonb_object_agg(region.id, generation.id)
            FILTER (WHERE generation.id IS NOT NULL), '{}'::jsonb),
        COALESCE(jsonb_agg(jsonb_build_object('regionId', region.id, 'displayName', region.display_name)
            ORDER BY region.id COLLATE "C") FILTER (WHERE region.configured), '[]'::jsonb),
        COALESCE(jsonb_object_agg(region.id, region.display_name), '{}'::jsonb)
    INTO full_proof, selection_proof, heads, configured_names, names
    FROM osm_catalog.regions region LEFT JOIN osm_catalog.generations generation
        ON generation.region_id = region.id AND generation.state = 'active';

    -- No sequence: neither rollback nor an equal refresh publishes a new head.
    UPDATE osm_catalog.route_region_catalog_state
    SET revision = revision + 1, fingerprint = full_proof,
        selection_fingerprint = selection_proof, active_generations = heads,
        configured_regions = configured_names, region_names = names
    WHERE singleton AND (fingerprint, selection_fingerprint, active_generations, configured_regions, region_names)
        IS DISTINCT FROM (full_proof, selection_proof, heads, configured_names, names);
    RETURN NULL;
END;
$function$;
-- +goose StatementEnd

-- Statement triggers intentionally exclude progress, timestamps and storage.
-- UPDATE OF cannot use transition relations; compare the small derived state
-- instead, once per statement (including bulk inserts/upserts and zero rows).
CREATE TRIGGER regions_lock_route_catalog
    BEFORE INSERT OR DELETE OR UPDATE OF id, boundary, configured, display_name ON osm_catalog.regions
    FOR EACH STATEMENT EXECUTE FUNCTION osm_catalog.lock_route_region_catalog();
CREATE TRIGGER regions_refresh_route_catalog
    AFTER INSERT OR DELETE OR UPDATE OF id, boundary, configured, display_name ON osm_catalog.regions
    FOR EACH STATEMENT EXECUTE FUNCTION osm_catalog.refresh_route_region_catalog();
CREATE TRIGGER generations_lock_route_catalog
    BEFORE INSERT OR DELETE OR UPDATE OF id, region_id, state ON osm_catalog.generations
    FOR EACH STATEMENT EXECUTE FUNCTION osm_catalog.lock_route_region_catalog();
CREATE TRIGGER generations_refresh_route_catalog
    AFTER INSERT OR DELETE OR UPDATE OF id, region_id, state ON osm_catalog.generations
    FOR EACH STATEMENT EXECUTE FUNCTION osm_catalog.refresh_route_region_catalog();

-- Initialize from existing rows without rewriting any source boundary.
UPDATE osm_catalog.regions SET configured = configured WHERE false;

-- +goose StatementBegin
CREATE FUNCTION osm_catalog.deny_route_catalog_truncate()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $function$
BEGIN
    RAISE EXCEPTION 'OSM route catalog TRUNCATE requires an explicit migration';
END;
$function$;
-- +goose StatementEnd

CREATE TRIGGER regions_deny_catalog_truncate BEFORE TRUNCATE ON osm_catalog.regions
    FOR EACH STATEMENT EXECUTE FUNCTION osm_catalog.deny_route_catalog_truncate();
CREATE TRIGGER generations_deny_catalog_truncate BEFORE TRUNCATE ON osm_catalog.generations
    FOR EACH STATEMENT EXECUTE FUNCTION osm_catalog.deny_route_catalog_truncate();
CREATE TRIGGER route_catalog_deny_truncate BEFORE TRUNCATE ON osm_catalog.route_region_catalog_state
    FOR EACH STATEMENT EXECUTE FUNCTION osm_catalog.deny_route_catalog_truncate();

REVOKE ALL ON FUNCTION osm_catalog.lock_route_region_catalog() FROM PUBLIC;
REVOKE ALL ON FUNCTION osm_catalog.refresh_route_region_catalog() FROM PUBLIC;
REVOKE ALL ON FUNCTION osm_catalog.deny_route_catalog_truncate() FROM PUBLIC;

-- Keep the existing catalog owner and its provisioned reader roles, including
-- installations whose roles are supplied by the external PostgreSQL module.
-- No reader receives DML or trigger-helper execution privileges.
-- +goose StatementBegin
DO $catalog_acl$
DECLARE
    catalog_owner name;
    reader record;
BEGIN
    SELECT pg_get_userbyid(relowner) INTO catalog_owner
    FROM pg_class WHERE oid = 'osm_catalog.regions'::regclass;
    EXECUTE format('ALTER TABLE osm_catalog.route_region_catalog_state OWNER TO %I', catalog_owner);
    EXECUTE format('ALTER FUNCTION osm_catalog.lock_route_region_catalog() OWNER TO %I', catalog_owner);
    EXECUTE format('ALTER FUNCTION osm_catalog.refresh_route_region_catalog() OWNER TO %I', catalog_owner);
    EXECUTE format('ALTER FUNCTION osm_catalog.deny_route_catalog_truncate() OWNER TO %I', catalog_owner);
    FOR reader IN
        SELECT DISTINCT pg_get_userbyid(acl.grantee) AS role_name
        FROM pg_class relation,
            LATERAL aclexplode(COALESCE(relation.relacl, acldefault('r', relation.relowner))) acl
        WHERE relation.oid = 'osm_catalog.regions'::regclass
            AND acl.privilege_type = 'SELECT' AND acl.grantee <> 0 AND acl.grantee <> relation.relowner
    LOOP
        EXECUTE format('REVOKE ALL ON osm_catalog.route_region_catalog_state FROM %I', reader.role_name);
        EXECUTE format('GRANT SELECT ON osm_catalog.route_region_catalog_state TO %I', reader.role_name);
    END LOOP;
END;
$catalog_acl$;
-- +goose StatementEnd

UPDATE osm_catalog.schema_metadata
SET schema_version = 10, minimum_runtime_version = 10 WHERE singleton;

-- +goose Down
-- +goose StatementBegin
DO $forward_only$
BEGIN
    RAISE EXCEPTION 'OSM schema 10 is forward-only: route catalog revision proofs must not be reset';
END;
$forward_only$;
-- +goose StatementEnd
