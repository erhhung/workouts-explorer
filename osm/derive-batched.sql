-- Restart-safe physical graph derivation. Each invocation executes one durable
-- unit and advances the generation-stage cursor in the same transaction.
SET jit = off;
SET work_mem = '64MB';
SET maintenance_work_mem = '512MB';
SET max_parallel_workers_per_gather = 0;
SELECT set_config('workouts_explorer.osm_derivation_version', :'OSM_DERIVATION_VERSION', false);
SELECT set_config('workouts_explorer.osm_region_id', :'OSM_REGION_ID', false);
SET search_path TO :"OSM_BUILD_SCHEMA", public;

CREATE OR REPLACE PROCEDURE derive_path_segment_batch(
    minimum_way_id bigint,
    maximum_way_id bigint
)
LANGUAGE plpgsql
AS $procedure$
BEGIN
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
            WHEN range.tags->>'oneway' IN ('yes','1','true') OR range.tags->>'junction' = 'roundabout' THEN true
            WHEN range.tags->>'oneway' = '-1' THEN false
            ELSE true
        END AS motor_forward_allowed,
        CASE
            WHEN range.tags->>'oneway' IN ('yes','1','true') OR range.tags->>'junction' = 'roundabout' THEN false
            WHEN range.tags->>'oneway' = '-1' THEN true
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
    SELECT physical.*, locality.relation_id AS locality_relation_id
    FROM physical
    LEFT JOIN LATERAL (
        SELECT candidate.relation_id
        FROM localities AS candidate
        WHERE candidate.geom && ST_LineInterpolatePoint(physical.geom, 0.5)
          AND ST_Covers(candidate.geom, ST_LineInterpolatePoint(physical.geom, 0.5))
        ORDER BY candidate.admin_level DESC, candidate.relation_id
        LIMIT 1
    ) AS locality ON true
    WHERE ST_NPoints(physical.geom) >= 2
      AND ST_Length(physical.geom::geography) > 0
)
SELECT
    md5(format('workouts-explorer/osm-segment/v%s:%s:%s:%s:%s:1',
        current_setting('workouts_explorer.osm_derivation_version'), source_way_id, source_way_version,
        start_node_index, end_node_index))::uuid AS segment_id,
    source_way_id,
    source_way_version,
    current_setting('workouts_explorer.osm_derivation_version')::integer AS derivation_version,
    start_node_index,
    end_node_index,
    1 AS boundary_piece,
    md5(format('workouts-explorer/osm-graph-node/v1:%s', start_node_id))::uuid AS start_graph_node_id,
    md5(format('workouts-explorer/osm-graph-node/v1:%s', end_node_id))::uuid AS end_graph_node_id,
    name,
    normalized_name,
    highway,
    broad_class,
    tags,
    motor_forward_allowed,
    motor_reverse_allowed,
    geom,
    locality_relation_id,
    CASE
        WHEN normalized_name IS NOT NULL THEN md5(
            CASE WHEN locality_relation_id IS NOT NULL THEN
                format('workouts-explorer/osm-logical-path/v1:%s:%s:%s:%s',
                    locality_relation_id, length(broad_class), broad_class, length(normalized_name)) || normalized_name
            ELSE
                format('workouts-explorer/osm-logical-path/v2:region:%s:%s:%s:%s:%s:',
                    length(current_setting('workouts_explorer.osm_region_id')),
                    current_setting('workouts_explorer.osm_region_id'),
                    length(broad_class), broad_class, length(normalized_name)) || normalized_name
            END
        )::uuid
        ELSE md5(format('workouts-explorer/osm-unnamed-path/v1:%s:%s:%s:%s:1',
            source_way_id, source_way_version, start_node_index, end_node_index))::uuid
    END AS logical_path_id,
    ST_Length(geom::geography) AS length_m
FROM localized;
END;
$procedure$;

CREATE OR REPLACE PROCEDURE derive_batched(target_generation bigint)
LANGUAGE plpgsql
AS $procedure$
DECLARE
    phase text;
    cursor_way_id bigint;
    next_way_id bigint;
    minimum_way_id bigint;
    expected_batch bigint;
    processed bigint;
    batch_rows bigint;
    batch_index bigint;
    batch_total bigint;
    phase_rows bigint;
    cursor_rows bigint;
    node_count_batch_size constant integer := 150000;
    segment_batch_size constant integer := 100000;
BEGIN
    SELECT
        coalesce(cursor->>'phase', 'init'),
        NULLIF(cursor->>'last_way_id', '')::bigint,
        batch_count,
        rows_processed,
        (cursor->>'batch_index')::bigint,
        (cursor->>'batch_total')::bigint
    INTO phase, cursor_way_id, expected_batch, processed, batch_index, batch_total
    FROM osm_catalog.generation_stages
    WHERE generation_id = target_generation
      AND stage = 'derive'
    FOR UPDATE;

    IF phase IN ('node-counts', 'segments') AND (batch_index IS NULL OR batch_total IS NULL) THEN
        SELECT count(*), count(*) FILTER (WHERE cursor_way_id IS NOT NULL AND way_id <= cursor_way_id)
        INTO phase_rows, cursor_rows
        FROM ways;
        IF phase = 'node-counts' THEN
            batch_index := (cursor_rows + node_count_batch_size - 1) / node_count_batch_size + 1;
            batch_total := (phase_rows + node_count_batch_size - 1) / node_count_batch_size + 1;
        ELSE
            batch_index := (cursor_rows + segment_batch_size - 1) / segment_batch_size + 1;
            batch_total := (phase_rows + segment_batch_size - 1) / segment_batch_size + 1;
        END IF;
    END IF;

    IF phase = 'init' THEN
        DROP TABLE IF EXISTS path_segments CASCADE;
        DROP TABLE IF EXISTS logical_paths CASCADE;
        DROP TABLE IF EXISTS shared_nodes;
        DROP TABLE IF EXISTS derive_node_usage;

        CREATE TABLE derive_node_usage (
            node_id bigint PRIMARY KEY,
            use_count bigint NOT NULL
        );
        CREATE TABLE shared_nodes (
            node_id bigint PRIMARY KEY
        );
        CREATE TABLE path_segments (
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
        CREATE TABLE logical_paths (
            logical_path_id uuid PRIMARY KEY,
            locality_relation_id bigint,
            name text,
            normalized_name text,
            broad_class text,
            member_segment_count bigint NOT NULL,
            member_length_m double precision NOT NULL
        );
        SELECT count(*) INTO phase_rows FROM ways;
        PERFORM osm_catalog.checkpoint_generation_stage(
            target_generation,
            'derive',
            expected_batch,
            jsonb_build_object('version', 2, 'phase', 'node-counts', 'last_way_id', NULL, 'batch_size', node_count_batch_size,
                'batch_index', 1, 'batch_total', (phase_rows + node_count_batch_size - 1) / node_count_batch_size + 1),
            processed
        );
        RETURN;
    END IF;

    IF phase = 'node-counts' THEN
        CREATE TEMP TABLE derive_way_keys ON COMMIT DROP AS
        SELECT way_id
        FROM ways
        WHERE cursor_way_id IS NULL OR way_id > cursor_way_id
        ORDER BY way_id
        LIMIT node_count_batch_size;
        GET DIAGNOSTICS batch_rows = ROW_COUNT;
        IF batch_rows = 0 THEN
            PERFORM osm_catalog.checkpoint_generation_stage(
                target_generation,
                'derive',
                expected_batch,
                jsonb_build_object('version', 2, 'phase', 'shared-nodes', 'last_way_id', NULL, 'batch_size', node_count_batch_size),
                processed
            );
            RETURN;
        END IF;
        SELECT min(way_id), max(way_id) INTO minimum_way_id, next_way_id FROM derive_way_keys;
        INSERT INTO derive_node_usage(node_id, use_count)
        SELECT node.node_id::bigint, count(*)
        FROM derive_way_keys AS key
        JOIN ways AS way USING (way_id)
        CROSS JOIN LATERAL jsonb_array_elements_text(way.node_ids) AS node(node_id)
        GROUP BY node.node_id::bigint
        ON CONFLICT (node_id) DO UPDATE
        SET use_count = derive_node_usage.use_count + excluded.use_count;
        PERFORM osm_catalog.checkpoint_generation_stage(
            target_generation,
            'derive',
            expected_batch,
            jsonb_build_object('version', 2, 'phase', 'node-counts', 'last_way_id', next_way_id, 'batch_size', node_count_batch_size,
                'batch_index', batch_index + 1, 'batch_total', batch_total),
            processed + batch_rows
        );
        RETURN;
    END IF;

    IF phase = 'shared-nodes' THEN
        INSERT INTO shared_nodes(node_id)
        SELECT node_id FROM derive_node_usage WHERE use_count > 1;
        ANALYZE shared_nodes;
        SELECT count(*) INTO phase_rows FROM ways;
        PERFORM osm_catalog.checkpoint_generation_stage(
            target_generation,
            'derive',
            expected_batch,
            jsonb_build_object('version', 2, 'phase', 'segments', 'last_way_id', NULL, 'batch_size', segment_batch_size,
                'batch_index', 1, 'batch_total', (phase_rows + segment_batch_size - 1) / segment_batch_size + 1),
            processed
        );
        RETURN;
    END IF;

    IF phase = 'segments' THEN
        CREATE TEMP TABLE derive_way_keys ON COMMIT DROP AS
        SELECT way_id
        FROM ways
        WHERE cursor_way_id IS NULL OR way_id > cursor_way_id
        ORDER BY way_id
        LIMIT segment_batch_size;
        GET DIAGNOSTICS batch_rows = ROW_COUNT;
        IF batch_rows = 0 THEN
            PERFORM osm_catalog.checkpoint_generation_stage(
                target_generation,
                'derive',
                expected_batch,
                jsonb_build_object('version', 2, 'phase', 'path-primary', 'last_way_id', NULL, 'batch_size', segment_batch_size),
                processed
            );
            RETURN;
        END IF;
        SELECT min(way_id), max(way_id) INTO minimum_way_id, next_way_id FROM derive_way_keys;
        CALL derive_path_segment_batch(minimum_way_id, next_way_id);
        INSERT INTO logical_paths
        SELECT
            logical_path_id,
            min(locality_relation_id),
            min(name),
            min(normalized_name),
            min(broad_class),
            count(*),
            sum(length_m)
        FROM path_segments
        WHERE source_way_id BETWEEN minimum_way_id AND next_way_id
        GROUP BY logical_path_id
        ON CONFLICT (logical_path_id) DO UPDATE
        SET
            locality_relation_id = coalesce(least(logical_paths.locality_relation_id, excluded.locality_relation_id), logical_paths.locality_relation_id, excluded.locality_relation_id),
            name = coalesce(least(logical_paths.name, excluded.name), logical_paths.name, excluded.name),
            normalized_name = coalesce(least(logical_paths.normalized_name, excluded.normalized_name), logical_paths.normalized_name, excluded.normalized_name),
            broad_class = coalesce(least(logical_paths.broad_class, excluded.broad_class), logical_paths.broad_class, excluded.broad_class),
            member_segment_count = logical_paths.member_segment_count + excluded.member_segment_count,
            member_length_m = logical_paths.member_length_m + excluded.member_length_m;
        PERFORM osm_catalog.checkpoint_generation_stage(
            target_generation,
            'derive',
            expected_batch,
            jsonb_build_object('version', 2, 'phase', 'segments', 'last_way_id', next_way_id, 'batch_size', segment_batch_size,
                'batch_index', batch_index + 1, 'batch_total', batch_total),
            processed + batch_rows
        );
        RETURN;
    END IF;

    IF phase = 'path-primary' THEN
        ALTER TABLE path_segments ADD PRIMARY KEY (segment_id);
        PERFORM osm_catalog.checkpoint_generation_stage(target_generation, 'derive', expected_batch,
            jsonb_build_object('version', 1, 'phase', 'source-unique'), processed);
        RETURN;
    END IF;
    IF phase = 'source-unique' THEN
        ALTER TABLE path_segments ADD CONSTRAINT path_segments_source_piece_unique
            UNIQUE (source_way_id, source_way_version, start_node_index, end_node_index, boundary_piece);
        PERFORM osm_catalog.checkpoint_generation_stage(target_generation, 'derive', expected_batch,
            jsonb_build_object('version', 1, 'phase', 'geography-index'), processed);
        RETURN;
    END IF;
    IF phase = 'geography-index' THEN
        CREATE INDEX path_segments_geography_gist ON path_segments USING gist ((geom::geography));
        PERFORM osm_catalog.checkpoint_generation_stage(target_generation, 'derive', expected_batch,
            jsonb_build_object('version', 1, 'phase', 'start-index'), processed);
        RETURN;
    END IF;
    IF phase = 'start-index' THEN
        CREATE INDEX path_segments_start_node_idx ON path_segments (start_graph_node_id);
        PERFORM osm_catalog.checkpoint_generation_stage(target_generation, 'derive', expected_batch,
            jsonb_build_object('version', 1, 'phase', 'end-index'), processed);
        RETURN;
    END IF;
    IF phase = 'end-index' THEN
        CREATE INDEX path_segments_end_node_idx ON path_segments (end_graph_node_id);
        PERFORM osm_catalog.checkpoint_generation_stage(target_generation, 'derive', expected_batch,
            jsonb_build_object('version', 1, 'phase', 'logical-index'), processed);
        RETURN;
    END IF;
    IF phase = 'logical-index' THEN
        CREATE INDEX path_segments_logical_path_idx ON path_segments (logical_path_id);
        PERFORM osm_catalog.checkpoint_generation_stage(target_generation, 'derive', expected_batch,
            jsonb_build_object('version', 1, 'phase', 'locality-index'), processed);
        RETURN;
    END IF;
    IF phase = 'locality-index' THEN
        CREATE INDEX path_segments_locality_idx ON path_segments (locality_relation_id);
        PERFORM osm_catalog.checkpoint_generation_stage(target_generation, 'derive', expected_batch,
            jsonb_build_object('version', 1, 'phase', 'logical-locality-index'), processed);
        RETURN;
    END IF;
    IF phase = 'logical-locality-index' THEN
        CREATE INDEX logical_paths_locality_name_idx ON logical_paths (locality_relation_id, normalized_name, broad_class);
        PERFORM osm_catalog.checkpoint_generation_stage(target_generation, 'derive', expected_batch,
            jsonb_build_object('version', 1, 'phase', 'finalize'), processed);
        RETURN;
    END IF;
    IF phase = 'finalize' THEN
        ANALYZE path_segments;
        ANALYZE logical_paths;
        DROP TABLE derive_node_usage;
        DROP TABLE shared_nodes;
        PERFORM osm_catalog.complete_generation_stage(
            target_generation,
            'derive',
            expected_batch,
            jsonb_build_object('version', 1, 'phase', 'done'),
            processed
        );
        RETURN;
    END IF;

    RAISE EXCEPTION 'unknown derive phase %', phase;
END;
$procedure$;

CALL derive_batched(:'OSM_GENERATION_ID'::bigint);
DROP PROCEDURE derive_batched(bigint);
DROP PROCEDURE derive_path_segment_batch(bigint, bigint);
