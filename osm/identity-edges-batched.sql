SET search_path TO :"OSM_BUILD_SCHEMA", public;

CREATE TABLE IF NOT EXISTS attribution_identity_edges (
    group_id uuid NOT NULL,
    connecting_node_id uuid NOT NULL,
    left_segment_id uuid NOT NULL,
    right_segment_id uuid NOT NULL,
    left_source_way_id bigint NOT NULL,
    right_source_way_id bigint NOT NULL,
    unnamed boolean NOT NULL,
    left_broad_class text NOT NULL,
    right_broad_class text NOT NULL,
    PRIMARY KEY (group_id, connecting_node_id, left_segment_id, right_segment_id),
    CHECK (left_segment_id < right_segment_id)
);

CREATE OR REPLACE PROCEDURE stage_identity_edges(target_generation bigint)
LANGUAGE plpgsql
AS $procedure$
DECLARE
    orientation integer;
    cursor_id uuid;
    next_cursor uuid;
    expected_batch bigint;
    processed bigint;
    batch_rows bigint;
    inserted_rows bigint;
    batch_index bigint;
    batch_total bigint;
    phase_rows bigint;
    cursor_rows bigint;
    batch_size constant integer := 500000;
BEGIN
    SELECT
        coalesce((cursor->>'orientation')::integer, 1),
        NULLIF(cursor->>'last_segment_id', '')::uuid,
        batch_count,
        rows_processed,
        (cursor->>'batch_index')::bigint,
        (cursor->>'batch_total')::bigint
    INTO orientation, cursor_id, expected_batch, processed, batch_index, batch_total
    FROM osm_catalog.generation_stages
    WHERE generation_id = target_generation
      AND stage = 'identity-edges'
    FOR UPDATE;

    IF orientation <= 4 AND (batch_index IS NULL OR batch_total IS NULL) THEN
        SELECT count(*), count(*) FILTER (WHERE cursor_id IS NOT NULL AND segment_id <= cursor_id)
        INTO phase_rows, cursor_rows
        FROM attribution_identity_segments;
        batch_index := (cursor_rows + batch_size - 1) / batch_size + 1;
        batch_total := (phase_rows + batch_size - 1) / batch_size + 1;
    END IF;

    IF orientation > 4 THEN
        ANALYZE attribution_identity_edges;
        PERFORM osm_catalog.complete_generation_stage(
            target_generation,
            'identity-edges',
            expected_batch,
            jsonb_build_object('version', 2, 'done', true, 'orientation', orientation, 'batch_size', batch_size,
                'batch_index', batch_index, 'batch_total', batch_total),
            processed
        );
        RETURN;
    END IF;

    CREATE TEMP TABLE identity_edge_keys ON COMMIT DROP AS
    SELECT segment_id
    FROM attribution_identity_segments
    WHERE cursor_id IS NULL OR segment_id > cursor_id
    ORDER BY segment_id
    LIMIT batch_size;

    GET DIAGNOSTICS batch_rows = ROW_COUNT;
    IF batch_rows = 0 THEN
        IF orientation = 2 THEN
            -- These structures have no consumers after locality attribution and
            -- start/end edge construction. Canonical indexes are rebuilt during
            -- partition preparation; the identity rewrite builds fresh logical
            -- path aggregates.
            DROP INDEX IF EXISTS attribution_identity_segments_start_idx;
            DROP INDEX IF EXISTS path_segments_geography_gist;
            DROP INDEX IF EXISTS path_segments_start_node_idx;
            DROP INDEX IF EXISTS path_segments_end_node_idx;
            DROP INDEX IF EXISTS path_segments_logical_path_idx;
            DROP INDEX IF EXISTS path_segments_locality_idx;
            ALTER TABLE path_segments DROP CONSTRAINT IF EXISTS path_segments_source_piece_unique;
            DROP INDEX IF EXISTS ways_geography_gist;
            DROP INDEX IF EXISTS ways_geom_idx;
            DROP TABLE IF EXISTS logical_paths;
            DROP TABLE IF EXISTS boundaries;
            DROP TABLE IF EXISTS park_ways;
            DROP TABLE IF EXISTS park_relations;
            DROP TABLE IF EXISTS education_ways;
            DROP TABLE IF EXISTS education_relations;
            DROP TABLE IF EXISTS osm2pgsql_properties;
        END IF;
        IF orientation IN (1, 2) THEN
            SELECT count(*) INTO phase_rows FROM attribution_identity_segments;
        END IF;
        PERFORM osm_catalog.checkpoint_generation_stage(
            target_generation,
            'identity-edges',
            expected_batch,
            jsonb_build_object(
                'version', 2,
                'done', false,
                'orientation', CASE orientation WHEN 1 THEN 2 WHEN 2 THEN 4 ELSE 5 END,
                'last_segment_id', NULL,
                'batch_size', batch_size
            ) || CASE WHEN orientation IN (1, 2) THEN jsonb_build_object(
                'batch_index', 1,
                'batch_total', (phase_rows + batch_size - 1) / batch_size + 1
            ) ELSE '{}'::jsonb END,
            processed
        );
        RETURN;
    END IF;

    SELECT segment_id
    INTO next_cursor
    FROM identity_edge_keys
    ORDER BY segment_id DESC
    LIMIT 1;

    SET LOCAL enable_hashjoin = off;
    SET LOCAL enable_mergejoin = off;

    IF orientation = 1 THEN
        INSERT INTO attribution_identity_edges
        SELECT a.group_id,
            a.start_graph_node_id,
            least(a.segment_id, b.segment_id),
            greatest(a.segment_id, b.segment_id),
            CASE WHEN a.segment_id < b.segment_id THEN a.source_way_id ELSE b.source_way_id END,
            CASE WHEN a.segment_id < b.segment_id THEN b.source_way_id ELSE a.source_way_id END,
            a.unnamed,
            CASE WHEN a.segment_id < b.segment_id THEN a.broad_class ELSE b.broad_class END,
            CASE WHEN a.segment_id < b.segment_id THEN b.broad_class ELSE a.broad_class END
        FROM identity_edge_keys AS key
        JOIN attribution_identity_segments AS a USING (segment_id)
        JOIN attribution_identity_segments AS b
          ON b.group_id = a.group_id
         AND b.start_graph_node_id = a.start_graph_node_id
        WHERE a.segment_id <> b.segment_id
        ON CONFLICT DO NOTHING;
    ELSIF orientation = 2 THEN
        INSERT INTO attribution_identity_edges
        SELECT a.group_id,
            a.start_graph_node_id,
            least(a.segment_id, b.segment_id),
            greatest(a.segment_id, b.segment_id),
            CASE WHEN a.segment_id < b.segment_id THEN a.source_way_id ELSE b.source_way_id END,
            CASE WHEN a.segment_id < b.segment_id THEN b.source_way_id ELSE a.source_way_id END,
            a.unnamed,
            CASE WHEN a.segment_id < b.segment_id THEN a.broad_class ELSE b.broad_class END,
            CASE WHEN a.segment_id < b.segment_id THEN b.broad_class ELSE a.broad_class END
        FROM identity_edge_keys AS key
        JOIN attribution_identity_segments AS a USING (segment_id)
        JOIN attribution_identity_segments AS b
          ON b.group_id = a.group_id
         AND b.end_graph_node_id = a.start_graph_node_id
        WHERE a.segment_id <> b.segment_id
        ON CONFLICT DO NOTHING;
    ELSIF orientation = 4 THEN
        INSERT INTO attribution_identity_edges
        SELECT a.group_id,
            a.end_graph_node_id,
            least(a.segment_id, b.segment_id),
            greatest(a.segment_id, b.segment_id),
            CASE WHEN a.segment_id < b.segment_id THEN a.source_way_id ELSE b.source_way_id END,
            CASE WHEN a.segment_id < b.segment_id THEN b.source_way_id ELSE a.source_way_id END,
            a.unnamed,
            CASE WHEN a.segment_id < b.segment_id THEN a.broad_class ELSE b.broad_class END,
            CASE WHEN a.segment_id < b.segment_id THEN b.broad_class ELSE a.broad_class END
        FROM identity_edge_keys AS key
        JOIN attribution_identity_segments AS a USING (segment_id)
        JOIN attribution_identity_segments AS b
          ON b.group_id = a.group_id
         AND b.end_graph_node_id = a.end_graph_node_id
        WHERE a.segment_id <> b.segment_id
        ON CONFLICT DO NOTHING;
    ELSE
        RAISE EXCEPTION 'unsupported identity-edge orientation %', orientation;
    END IF;

    GET DIAGNOSTICS inserted_rows = ROW_COUNT;
    PERFORM osm_catalog.checkpoint_generation_stage(
        target_generation,
        'identity-edges',
        expected_batch,
        jsonb_build_object(
            'version', 2,
            'done', false,
            'orientation', orientation,
            'last_segment_id', next_cursor,
            'batch_size', batch_size,
            'batch_index', batch_index + 1,
            'batch_total', batch_total
        ),
        processed + inserted_rows
    );
END;
$procedure$;

CALL stage_identity_edges(:'OSM_GENERATION_ID'::bigint);

DROP PROCEDURE stage_identity_edges(bigint);
