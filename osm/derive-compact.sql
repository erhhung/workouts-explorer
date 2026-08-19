-- Storage-bounded full-region derivation. Only shared node IDs are staged;
-- source vertices are read directly from each imported way geometry.
SET jit = off;
SET work_mem = '64MB';
SET maintenance_work_mem = '512MB';
SET max_parallel_workers_per_gather = 0;
SELECT set_config('workouts_explorer.osm_derivation_version', :'OSM_DERIVATION_VERSION', false);
SET search_path TO :"OSM_BUILD_SCHEMA", public;

DROP TABLE IF EXISTS :"OSM_BUILD_SCHEMA".path_segments CASCADE;
DROP TABLE IF EXISTS :"OSM_BUILD_SCHEMA".logical_paths CASCADE;
DROP TABLE IF EXISTS :"OSM_BUILD_SCHEMA".shared_nodes;

CREATE UNLOGGED TABLE :"OSM_BUILD_SCHEMA".shared_nodes AS
SELECT node.node_id
FROM :"OSM_BUILD_SCHEMA".ways AS way
CROSS JOIN LATERAL jsonb_array_elements_text(way.node_ids) AS node_value
CROSS JOIN LATERAL (SELECT node_value::bigint AS node_id) AS node
GROUP BY node.node_id
HAVING count(*) > 1;
ALTER TABLE :"OSM_BUILD_SCHEMA".shared_nodes ADD PRIMARY KEY (node_id);
ANALYZE :"OSM_BUILD_SCHEMA".shared_nodes;

CREATE TABLE :"OSM_BUILD_SCHEMA".path_segments (
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

CREATE OR REPLACE PROCEDURE derive_path_segment_batch(
    minimum_way_id bigint,
    maximum_way_id bigint
)
LANGUAGE SQL
AS $procedure$
INSERT INTO path_segments
WITH cut_ranges AS (
    SELECT
        way.way_id AS source_way_id,
        way.version AS source_way_version,
        way.tags,
        way.geom AS way_geom,
        cut.node_index AS start_node_index,
        lead(cut.node_index) OVER way_order AS end_node_index,
        cut.node_id AS start_node_id,
        lead(cut.node_id) OVER way_order AS end_node_id
    FROM ways AS way
    CROSS JOIN LATERAL (
        SELECT node.ordinality::integer AS node_index, node.node_id::bigint AS node_id
        FROM jsonb_array_elements_text(way.node_ids)
            WITH ORDINALITY AS node(node_id, ordinality)
        LEFT JOIN shared_nodes AS shared
          ON shared.node_id = node.node_id::bigint
        WHERE node.ordinality IN (1, jsonb_array_length(way.node_ids))
           OR shared.node_id IS NOT NULL
    ) AS cut
    WHERE way.way_id BETWEEN minimum_way_id AND maximum_way_id
    WINDOW way_order AS (PARTITION BY way.way_id ORDER BY cut.node_index)
), physical AS (
    SELECT range.*,
        NULLIF(btrim(range.tags->>'name'), '') AS name,
        NULLIF(lower(regexp_replace(
            CASE
                WHEN range.tags->>'highway' IN ('motorway','motorway_link','trunk','trunk_link','primary','primary_link','secondary','secondary_link','tertiary','tertiary_link','residential','unclassified','living_street','service','road')
                THEN regexp_replace(btrim(range.tags->>'name'), '^(North|South|East|West)[[:space:]]+', '', 'i')
                ELSE btrim(range.tags->>'name')
            END,
            '[[:space:]]+', ' ', 'g'
        )), '') AS normalized_name,
        range.tags->>'highway' AS highway,
        CASE
            WHEN range.tags->>'highway' IN ('motorway','motorway_link','trunk','trunk_link','primary','primary_link','secondary','secondary_link','tertiary','tertiary_link','residential','unclassified','living_street','service','road') THEN 'road'
            WHEN range.tags->>'highway' = 'cycleway' THEN 'cycleway'
            WHEN range.tags->>'highway' IN ('footway','pedestrian','steps','corridor') THEN 'footway'
            WHEN range.tags->>'highway' IN ('path','track','bridleway') THEN 'trail'
            ELSE 'other'
        END AS broad_class,
        CASE
            WHEN range.tags->>'oneway' IN ('yes','1','true') OR range.tags->>'junction'='roundabout' THEN true
            WHEN range.tags->>'oneway'='-1' THEN false
            ELSE true
        END AS motor_forward_allowed,
        CASE
            WHEN range.tags->>'oneway' IN ('yes','1','true') OR range.tags->>'junction'='roundabout' THEN false
            WHEN range.tags->>'oneway'='-1' THEN true
            ELSE true
        END AS motor_reverse_allowed,
        (
            SELECT ST_MakeLine(ST_PointN(range.way_geom, vertex))
            FROM generate_series(range.start_node_index, range.end_node_index) AS vertex
        )::geometry(LineString, 4326) AS geom
    FROM cut_ranges AS range
    WHERE range.end_node_index IS NOT NULL
      AND range.start_node_index < range.end_node_index
), localized AS (
    SELECT physical.*,
        locality.relation_id AS locality_relation_id
    FROM physical
    LEFT JOIN LATERAL (
        SELECT candidate.relation_id
        FROM localities AS candidate
        WHERE candidate.geom && ST_LineInterpolatePoint(physical.geom, 0.5)
          AND ST_Covers(candidate.geom, ST_LineInterpolatePoint(physical.geom, 0.5))
        ORDER BY candidate.relation_id
        LIMIT 1
    ) AS locality ON true
    WHERE ST_NPoints(physical.geom) >= 2
      AND ST_Length(physical.geom::geography) > 0
)
SELECT
    md5(format('workouts-explorer/osm-segment/v%s:%s:%s:%s:%s:1',
        current_setting('workouts_explorer.osm_derivation_version'), source_way_id, source_way_version,
        start_node_index, end_node_index))::uuid AS segment_id,
    source_way_id, source_way_version,
    current_setting('workouts_explorer.osm_derivation_version')::integer AS derivation_version,
    start_node_index, end_node_index, 1 AS boundary_piece,
    md5(format('workouts-explorer/osm-graph-node/v1:%s', start_node_id))::uuid AS start_graph_node_id,
    md5(format('workouts-explorer/osm-graph-node/v1:%s', end_node_id))::uuid AS end_graph_node_id,
    name, normalized_name, highway, broad_class, tags,
    motor_forward_allowed, motor_reverse_allowed,
    geom, locality_relation_id,
    CASE
        WHEN normalized_name IS NOT NULL THEN md5(
            format('workouts-explorer/osm-logical-path/v1:%s:%s:%s:%s',
                coalesce(locality_relation_id::text, 'outside'),
                length(broad_class), broad_class, length(normalized_name)) || normalized_name
        )::uuid
        ELSE md5(format('workouts-explorer/osm-unnamed-path/v1:%s:%s:%s:%s:1',
            source_way_id, source_way_version, start_node_index, end_node_index))::uuid
    END AS logical_path_id,
    ST_Length(geom::geography) AS length_m
FROM localized;
$procedure$;

DO $block$
DECLARE
    lower_way_id bigint;
    upper_way_id bigint;
    inserted_rows bigint;
BEGIN
    SELECT min(way_id) INTO lower_way_id FROM ways;
    WHILE lower_way_id IS NOT NULL LOOP
        SELECT way_id INTO upper_way_id
        FROM ways
        WHERE way_id >= lower_way_id
        ORDER BY way_id
        OFFSET 19999 LIMIT 1;
        IF upper_way_id IS NULL THEN
            SELECT max(way_id) INTO upper_way_id
            FROM ways WHERE way_id >= lower_way_id;
        END IF;
        CALL derive_path_segment_batch(lower_way_id, upper_way_id);
        GET DIAGNOSTICS inserted_rows = ROW_COUNT;
        RAISE NOTICE 'derived through way %, inserted % segments', upper_way_id, inserted_rows;
        SELECT min(way_id) INTO lower_way_id
        FROM ways WHERE way_id > upper_way_id;
    END LOOP;
END
$block$;

DROP PROCEDURE derive_path_segment_batch(bigint, bigint);

ALTER TABLE :"OSM_BUILD_SCHEMA".path_segments ADD PRIMARY KEY (segment_id);
ALTER TABLE :"OSM_BUILD_SCHEMA".path_segments ADD CONSTRAINT path_segments_source_piece_unique
UNIQUE (source_way_id, source_way_version, start_node_index, end_node_index, boundary_piece);
CREATE INDEX path_segments_geography_gist
ON :"OSM_BUILD_SCHEMA".path_segments USING gist ((geom::geography));
CREATE INDEX path_segments_start_node_idx
ON :"OSM_BUILD_SCHEMA".path_segments (start_graph_node_id);
CREATE INDEX path_segments_end_node_idx
ON :"OSM_BUILD_SCHEMA".path_segments (end_graph_node_id);
CREATE INDEX path_segments_logical_path_idx
ON :"OSM_BUILD_SCHEMA".path_segments (logical_path_id);
CREATE INDEX path_segments_locality_idx
ON :"OSM_BUILD_SCHEMA".path_segments (locality_relation_id);

CREATE TABLE :"OSM_BUILD_SCHEMA".logical_paths AS
SELECT logical_path_id,
    min(locality_relation_id) AS locality_relation_id,
    min(name) AS name,
    min(normalized_name) AS normalized_name,
    min(broad_class) AS broad_class,
    count(*) AS member_segment_count,
    sum(length_m) AS member_length_m
FROM :"OSM_BUILD_SCHEMA".path_segments
GROUP BY logical_path_id;
ALTER TABLE :"OSM_BUILD_SCHEMA".logical_paths ADD PRIMARY KEY (logical_path_id);
CREATE INDEX logical_paths_locality_name_idx
ON :"OSM_BUILD_SCHEMA".logical_paths (locality_relation_id, normalized_name, broad_class);

ANALYZE :"OSM_BUILD_SCHEMA".path_segments;
ANALYZE :"OSM_BUILD_SCHEMA".logical_paths;
DROP TABLE :"OSM_BUILD_SCHEMA".shared_nodes;
