-- +goose Up
CREATE SCHEMA osm_canonical;
REVOKE ALL ON SCHEMA osm_canonical FROM PUBLIC;

ALTER TABLE osm_catalog.generations
ADD CONSTRAINT generations_region_id_id_unique UNIQUE (region_id, id);

CREATE TABLE osm_canonical.ways (
    region_id text NOT NULL,
    generation_id bigint NOT NULL,
    way_id bigint NOT NULL,
    version integer NOT NULL,
    osm_timestamp text,
    tags jsonb NOT NULL,
    node_ids jsonb NOT NULL,
    geom geometry(LineString, 4326) NOT NULL
);

CREATE TABLE osm_canonical.localities (
    region_id text NOT NULL,
    generation_id bigint NOT NULL,
    relation_id bigint NOT NULL,
    relation_version integer NOT NULL,
    admin_level smallint,
    name text,
    normalized_name text,
    tags jsonb NOT NULL,
    geom geometry(MultiPolygon, 4326) NOT NULL
);

CREATE TABLE osm_canonical.path_segments (
    region_id text NOT NULL,
    generation_id bigint NOT NULL,
    segment_id uuid NOT NULL,
    source_way_id bigint NOT NULL,
    source_way_version integer NOT NULL,
    derivation_version integer NOT NULL,
    start_node_index integer NOT NULL,
    end_node_index integer NOT NULL,
    boundary_piece integer NOT NULL,
    start_graph_node_id uuid NOT NULL,
    end_graph_node_id uuid NOT NULL,
    name text,
    normalized_name text,
    highway text NOT NULL,
    broad_class text NOT NULL,
    tags jsonb NOT NULL,
    motor_forward_allowed boolean NOT NULL,
    motor_reverse_allowed boolean NOT NULL,
    geom geometry(LineString, 4326) NOT NULL,
    locality_relation_id bigint,
    logical_path_id uuid NOT NULL,
    length_m double precision NOT NULL
);

-- Bootstrap all pre-migration active regions before replacing their legacy views.
-- +goose StatementBegin
DO $bootstrap$
DECLARE
    generation record;
BEGIN
    FOR generation IN
        SELECT id, region_id, schema_name
        FROM osm_catalog.generations
        WHERE state = 'active'
        ORDER BY region_id
    LOOP
        IF to_regclass(format('%I.ways', generation.schema_name)) IS NULL
           OR to_regclass(format('%I.localities', generation.schema_name)) IS NULL
           OR to_regclass(format('%I.path_segments', generation.schema_name)) IS NULL
           OR to_regclass(format('%I.logical_paths', generation.schema_name)) IS NULL THEN
            RAISE EXCEPTION 'active generation % schema % is incomplete',
                generation.id, generation.schema_name;
        END IF;

        EXECUTE format(
            'INSERT INTO osm_canonical.ways
             SELECT $1, $2, way_id, version, osm_timestamp, tags, node_ids, geom
             FROM %I.ways', generation.schema_name)
        USING generation.region_id, generation.id;
        EXECUTE format(
            'INSERT INTO osm_canonical.localities
             SELECT $1, $2, relation_id, relation_version, admin_level, name,
                    normalized_name, tags, geom
             FROM %I.localities', generation.schema_name)
        USING generation.region_id, generation.id;
        EXECUTE format(
            'INSERT INTO osm_canonical.path_segments
             SELECT $1, $2, segment_id, source_way_id, source_way_version,
                    derivation_version, start_node_index, end_node_index,
                    boundary_piece, start_graph_node_id, end_graph_node_id, name,
                    normalized_name, highway, broad_class, tags,
                    motor_forward_allowed, motor_reverse_allowed, geom,
                    locality_relation_id, logical_path_id, length_m
             FROM %I.path_segments', generation.schema_name)
        USING generation.region_id, generation.id;
    END LOOP;
END
$bootstrap$;
-- +goose StatementEnd

ALTER TABLE osm_canonical.ways
ADD PRIMARY KEY (region_id, generation_id, way_id),
ADD FOREIGN KEY (region_id, generation_id)
    REFERENCES osm_catalog.generations (region_id, id);
CREATE INDEX canonical_ways_identity_idx
ON osm_canonical.ways (way_id, version DESC, generation_id DESC, region_id DESC);
CREATE INDEX canonical_ways_geography_gist
ON osm_canonical.ways USING gist ((geom::geography));

ALTER TABLE osm_canonical.localities
ADD PRIMARY KEY (region_id, generation_id, relation_id),
ADD FOREIGN KEY (region_id, generation_id)
    REFERENCES osm_catalog.generations (region_id, id);
CREATE INDEX canonical_localities_identity_idx
ON osm_canonical.localities
    (relation_id, relation_version DESC, generation_id DESC, region_id DESC);
CREATE INDEX canonical_localities_geom_gist
ON osm_canonical.localities USING gist (geom);

ALTER TABLE osm_canonical.path_segments
ADD PRIMARY KEY (region_id, generation_id, segment_id),
ADD FOREIGN KEY (region_id, generation_id)
    REFERENCES osm_catalog.generations (region_id, id),
ADD FOREIGN KEY (region_id, generation_id, source_way_id)
    REFERENCES osm_canonical.ways (region_id, generation_id, way_id);
CREATE INDEX canonical_path_segments_identity_idx
ON osm_canonical.path_segments (segment_id, generation_id DESC, region_id DESC);
CREATE INDEX canonical_path_segments_source_way_idx
ON osm_canonical.path_segments
    (source_way_id, source_way_version, generation_id DESC, region_id DESC);
CREATE INDEX canonical_path_segments_geography_gist
ON osm_canonical.path_segments USING gist ((geom::geography));
CREATE INDEX canonical_path_segments_logical_path_idx
ON osm_canonical.path_segments (logical_path_id);

DROP VIEW IF EXISTS osm_active.logical_paths;
DROP VIEW IF EXISTS osm_active.path_segments;
DROP VIEW IF EXISTS osm_active.localities;
DROP VIEW IF EXISTS osm_active.ways;

CREATE VIEW osm_active.ways AS
SELECT contribution.way_id, contribution.version, contribution.osm_timestamp,
    contribution.tags, contribution.node_ids, contribution.geom
FROM osm_canonical.ways AS contribution
JOIN osm_catalog.generations AS generation
  ON generation.id = contribution.generation_id
 AND generation.region_id = contribution.region_id
 AND generation.state = 'active'
WHERE NOT EXISTS (
    SELECT 1
    FROM osm_canonical.ways AS preferred
    JOIN osm_catalog.generations AS preferred_generation
      ON preferred_generation.id = preferred.generation_id
     AND preferred_generation.region_id = preferred.region_id
     AND preferred_generation.state = 'active'
    WHERE preferred.way_id = contribution.way_id
      AND (preferred.version,preferred.generation_id,preferred.region_id)
          > (contribution.version,contribution.generation_id,contribution.region_id)
);

CREATE VIEW osm_active.localities AS
SELECT contribution.relation_id, contribution.relation_version,
    contribution.admin_level, contribution.name, contribution.normalized_name,
    contribution.tags, contribution.geom
FROM osm_canonical.localities AS contribution
JOIN osm_catalog.generations AS generation
  ON generation.id = contribution.generation_id
 AND generation.region_id = contribution.region_id
 AND generation.state = 'active'
WHERE NOT EXISTS (
    SELECT 1
    FROM osm_canonical.localities AS preferred
    JOIN osm_catalog.generations AS preferred_generation
      ON preferred_generation.id = preferred.generation_id
     AND preferred_generation.region_id = preferred.region_id
     AND preferred_generation.state = 'active'
    WHERE preferred.relation_id = contribution.relation_id
      AND (preferred.relation_version,preferred.generation_id,preferred.region_id)
          > (contribution.relation_version,contribution.generation_id,contribution.region_id)
);

CREATE VIEW osm_active.path_segments AS
SELECT segment.segment_id, segment.source_way_id, segment.source_way_version,
    segment.derivation_version, segment.start_node_index, segment.end_node_index,
    segment.boundary_piece, segment.start_graph_node_id, segment.end_graph_node_id,
    segment.name, segment.normalized_name, segment.highway, segment.broad_class,
    segment.tags, segment.motor_forward_allowed, segment.motor_reverse_allowed,
    segment.geom, segment.locality_relation_id, segment.logical_path_id, segment.length_m
FROM osm_canonical.path_segments AS segment
JOIN osm_catalog.generations AS generation
  ON generation.id = segment.generation_id
 AND generation.region_id = segment.region_id
 AND generation.state = 'active'
WHERE NOT EXISTS (
    SELECT 1
    FROM osm_canonical.ways AS preferred_way
    JOIN osm_catalog.generations AS preferred_generation
      ON preferred_generation.id = preferred_way.generation_id
     AND preferred_generation.region_id = preferred_way.region_id
     AND preferred_generation.state = 'active'
    WHERE preferred_way.way_id = segment.source_way_id
      AND (preferred_way.version,preferred_way.generation_id,preferred_way.region_id)
          > (segment.source_way_version,segment.generation_id,segment.region_id)
)
AND NOT EXISTS (
    SELECT 1
    FROM osm_canonical.path_segments AS preferred_segment
    JOIN osm_catalog.generations AS preferred_generation
      ON preferred_generation.id = preferred_segment.generation_id
     AND preferred_generation.region_id = preferred_segment.region_id
     AND preferred_generation.state = 'active'
    WHERE preferred_segment.segment_id = segment.segment_id
      AND (preferred_segment.generation_id,preferred_segment.region_id)
          > (segment.generation_id,segment.region_id)
);

CREATE VIEW osm_active.logical_paths AS
SELECT logical_path_id,
    min(locality_relation_id) AS locality_relation_id,
    min(name) AS name,
    min(normalized_name) AS normalized_name,
    min(broad_class) AS broad_class,
    count(*) AS member_segment_count,
    sum(length_m) AS member_length_m
FROM osm_active.path_segments
GROUP BY logical_path_id;

-- +goose StatementBegin
CREATE FUNCTION osm_catalog.promote_region_generation(
    promote_region_id text,
    promote_generation_id bigint,
    promote_schema_name name,
    promote_validation jsonb
)
RETURNS void
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = pg_catalog, public
AS $function$
DECLARE
    candidate osm_catalog.generations%ROWTYPE;
    staged_way_count bigint;
    staged_segment_count bigint;
    staged_logical_path_count bigint;
    build_logical_path_count bigint;
BEGIN
    PERFORM pg_advisory_xact_lock(hashtextextended(promote_region_id, 0));

    SELECT * INTO candidate
    FROM osm_catalog.generations
    WHERE id = promote_generation_id
      AND region_id = promote_region_id
      AND schema_name = promote_schema_name
      AND state IN ('building', 'validating')
    FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'generation % is not a promotable generation for region % and schema %',
            promote_generation_id, promote_region_id, promote_schema_name;
    END IF;
    IF promote_schema_name::text !~ '^osm_build_[0-9]+$'
       OR to_regnamespace(promote_schema_name::text) IS NULL
       OR to_regclass(format('%I.ways', promote_schema_name)) IS NULL
       OR to_regclass(format('%I.localities', promote_schema_name)) IS NULL
       OR to_regclass(format('%I.path_segments', promote_schema_name)) IS NULL
       OR to_regclass(format('%I.logical_paths', promote_schema_name)) IS NULL THEN
        RAISE EXCEPTION 'generation % build schema % is incomplete',
            promote_generation_id, promote_schema_name;
    END IF;
    IF promote_validation IS NULL OR jsonb_typeof(promote_validation) <> 'object' THEN
        RAISE EXCEPTION 'promotion validation must be a JSON object';
    END IF;

    -- Target-generation canonical rows are staging rows until catalog activation.
    DELETE FROM osm_canonical.path_segments
    WHERE region_id = promote_region_id AND generation_id = promote_generation_id;
    DELETE FROM osm_canonical.localities
    WHERE region_id = promote_region_id AND generation_id = promote_generation_id;
    DELETE FROM osm_canonical.ways
    WHERE region_id = promote_region_id AND generation_id = promote_generation_id;
    EXECUTE format(
        'INSERT INTO osm_canonical.ways
         SELECT $1, $2, way_id, version, osm_timestamp, tags, node_ids, geom
         FROM %I.ways', promote_schema_name)
    USING promote_region_id, promote_generation_id;
    EXECUTE format(
        'INSERT INTO osm_canonical.localities
         SELECT $1, $2, relation_id, relation_version, admin_level, name,
                normalized_name, tags, geom
         FROM %I.localities', promote_schema_name)
    USING promote_region_id, promote_generation_id;
    EXECUTE format(
        'INSERT INTO osm_canonical.path_segments
         SELECT $1, $2, segment_id, source_way_id, source_way_version,
                derivation_version, start_node_index, end_node_index,
                boundary_piece, start_graph_node_id, end_graph_node_id, name,
                normalized_name, highway, broad_class, tags,
                motor_forward_allowed, motor_reverse_allowed, geom,
                locality_relation_id, logical_path_id, length_m
         FROM %I.path_segments', promote_schema_name)
    USING promote_region_id, promote_generation_id;

    SELECT count(*) INTO staged_way_count
    FROM osm_canonical.ways
    WHERE region_id = promote_region_id AND generation_id = promote_generation_id;
    SELECT count(*) INTO staged_segment_count
    FROM osm_canonical.path_segments
    WHERE region_id = promote_region_id AND generation_id = promote_generation_id;
    IF staged_way_count = 0 OR staged_segment_count = 0 THEN
        RAISE EXCEPTION 'generation % is empty: % ways, % path segments',
            promote_generation_id, staged_way_count, staged_segment_count;
    END IF;
    IF EXISTS (
        SELECT 1
        FROM osm_canonical.path_segments AS segment
        LEFT JOIN osm_canonical.ways AS way
          ON way.region_id = segment.region_id
         AND way.generation_id = segment.generation_id
         AND way.way_id = segment.source_way_id
         AND way.version = segment.source_way_version
        WHERE segment.region_id = promote_region_id
          AND segment.generation_id = promote_generation_id
          AND way.way_id IS NULL
    ) THEN
        RAISE EXCEPTION 'generation % has path segments without matching source way versions',
            promote_generation_id;
    END IF;
    SELECT count(DISTINCT logical_path_id)
    INTO staged_logical_path_count
    FROM osm_canonical.path_segments
    WHERE region_id = promote_region_id AND generation_id = promote_generation_id;
    EXECUTE format('SELECT count(*) FROM %I.logical_paths', promote_schema_name)
    INTO build_logical_path_count;
    IF staged_logical_path_count <> build_logical_path_count THEN
        RAISE EXCEPTION 'generation % has % segment logical paths, build schema has %',
            promote_generation_id, staged_logical_path_count, build_logical_path_count;
    END IF;

    UPDATE osm_catalog.generations
    SET state = 'retired', retired_at = transaction_timestamp()
    WHERE region_id = promote_region_id AND state = 'active';
    UPDATE osm_catalog.generations
    SET state = 'active', validation = promote_validation,
        validated_at = transaction_timestamp(), promoted_at = transaction_timestamp(),
        retired_at = NULL
    WHERE id = promote_generation_id;

    DELETE FROM osm_canonical.path_segments
    WHERE region_id = promote_region_id AND generation_id <> promote_generation_id;
    DELETE FROM osm_canonical.localities
    WHERE region_id = promote_region_id AND generation_id <> promote_generation_id;
    DELETE FROM osm_canonical.ways
    WHERE region_id = promote_region_id AND generation_id <> promote_generation_id;
END;
$function$;
-- +goose StatementEnd

UPDATE osm_catalog.schema_metadata
SET schema_version = 3
WHERE singleton;

REVOKE ALL ON ALL TABLES IN SCHEMA osm_canonical FROM PUBLIC;
REVOKE ALL ON FUNCTION osm_catalog.promote_region_generation(text, bigint, name, jsonb)
FROM PUBLIC;

-- +goose Down
-- +goose StatementBegin
DO $down$
DECLARE
    active_count integer;
    active_schema name;
BEGIN
    SELECT count(*), min(schema_name::text)::name
    INTO active_count, active_schema
    FROM osm_catalog.generations
    WHERE state = 'active';
    IF active_count > 1 THEN
        RAISE EXCEPTION 'cannot downgrade canonical OSM schema with % active regions', active_count;
    END IF;

    DROP VIEW osm_active.logical_paths;
    DROP VIEW osm_active.path_segments;
    DROP VIEW osm_active.localities;
    DROP VIEW osm_active.ways;

    IF active_count = 1 THEN
        EXECUTE format('CREATE VIEW osm_active.ways AS SELECT * FROM %I.ways', active_schema);
        EXECUTE format('CREATE VIEW osm_active.localities AS SELECT * FROM %I.localities', active_schema);
        EXECUTE format('CREATE VIEW osm_active.path_segments AS SELECT * FROM %I.path_segments', active_schema);
        EXECUTE format('CREATE VIEW osm_active.logical_paths AS SELECT * FROM %I.logical_paths', active_schema);
    END IF;
END
$down$;
-- +goose StatementEnd

DROP FUNCTION osm_catalog.promote_region_generation(text, bigint, name, jsonb);
DROP TABLE osm_canonical.path_segments;
DROP TABLE osm_canonical.localities;
DROP TABLE osm_canonical.ways;
DROP SCHEMA osm_canonical;
ALTER TABLE osm_catalog.generations
DROP CONSTRAINT generations_region_id_id_unique;
UPDATE osm_catalog.schema_metadata
SET schema_version = 2
WHERE singleton;
