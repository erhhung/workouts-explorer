SET search_path TO :"OSM_BUILD_SCHEMA", public;

CREATE OR REPLACE PROCEDURE stage_identity_components(target_generation bigint)
LANGUAGE plpgsql
AS $procedure$
DECLARE
    phase text;
    cursor_group_id uuid;
    cursor_node_id uuid;
    cursor_left_id uuid;
    cursor_right_id uuid;
    expected_batch bigint;
    processed bigint;
    batch_rows bigint;
    batch_index bigint;
    batch_total bigint;
    phase_rows bigint;
    cursor_rows bigint;
    filter_batch_size constant integer := 750000;
    component_batch_size constant integer := 2000000;
BEGIN
    SELECT
        coalesce(cursor->>'phase', 'branch-nodes'),
        NULLIF(cursor->>'group_id', '')::uuid,
        NULLIF(cursor->>'connecting_node_id', '')::uuid,
        NULLIF(cursor->>'left_segment_id', '')::uuid,
        NULLIF(cursor->>'right_segment_id', '')::uuid,
        batch_count,
        rows_processed,
        (cursor->>'batch_index')::bigint,
        (cursor->>'batch_total')::bigint
    INTO phase, cursor_group_id, cursor_node_id, cursor_left_id, cursor_right_id, expected_batch, processed,
        batch_index, batch_total
    FROM osm_catalog.generation_stages
    WHERE generation_id = target_generation
      AND stage = 'identity-components'
    FOR UPDATE;

    IF phase IN ('filter-edges', 'components-left', 'components-right')
       AND (batch_index IS NULL OR batch_total IS NULL) THEN
        IF phase = 'filter-edges' THEN
            SELECT count(*) INTO phase_rows
            FROM attribution_identity_edges
            WHERE cursor_group_id IS NULL
               OR (group_id, connecting_node_id, left_segment_id, right_segment_id) >
                  (cursor_group_id, cursor_node_id, cursor_left_id, cursor_right_id);
            phase_rows := processed + phase_rows;
            cursor_rows := processed;
            batch_index := (cursor_rows + filter_batch_size - 1) / filter_batch_size + 1;
            batch_total := (phase_rows + filter_batch_size - 1) / filter_batch_size + 1;
        ELSE
            SELECT count(*), count(*) FILTER (
                WHERE cursor_group_id IS NOT NULL
                  AND (group_id, connecting_node_id, left_segment_id, right_segment_id) <=
                      (cursor_group_id, cursor_node_id, cursor_left_id, cursor_right_id)
            )
            INTO phase_rows, cursor_rows
            FROM attribution_identity_edges;
            batch_index := (cursor_rows + component_batch_size - 1) / component_batch_size + 1;
            batch_total := (phase_rows + component_batch_size - 1) / component_batch_size + 1;
        END IF;
    END IF;

    IF phase = 'branch-nodes' THEN
        DROP TABLE IF EXISTS attribution_identity_components;
        DROP TABLE IF EXISTS attribution_identity_branch_classes;
        DROP TABLE IF EXISTS attribution_identity_branch_nodes;
        CREATE TABLE attribution_identity_branch_nodes AS
        SELECT group_id, connecting_node_id
        FROM (
            SELECT group_id, connecting_node_id, left_segment_id AS segment_id
            FROM attribution_identity_edges
            UNION ALL
            SELECT group_id, connecting_node_id, right_segment_id
            FROM attribution_identity_edges
        ) AS incidence
        GROUP BY group_id, connecting_node_id
        HAVING count(DISTINCT segment_id) > 2;
        CREATE UNIQUE INDEX attribution_identity_branch_nodes_key
        ON attribution_identity_branch_nodes (group_id, connecting_node_id);
        PERFORM osm_catalog.checkpoint_generation_stage(target_generation, 'identity-components', expected_batch,
            jsonb_build_object('version', 2, 'phase', 'branch-classes'), processed);
        RETURN;
    END IF;

    IF phase = 'branch-classes' THEN
        CREATE TABLE attribution_identity_branch_classes AS
        SELECT group_id, connecting_node_id, broad_class
        FROM (
            SELECT group_id, connecting_node_id, left_segment_id AS segment_id, left_broad_class AS broad_class
            FROM attribution_identity_edges
            UNION ALL
            SELECT group_id, connecting_node_id, right_segment_id, right_broad_class
            FROM attribution_identity_edges
        ) AS incidence
        GROUP BY group_id, connecting_node_id, broad_class
        HAVING count(DISTINCT segment_id) = 2;
        CREATE UNIQUE INDEX attribution_identity_branch_classes_key
        ON attribution_identity_branch_classes (group_id, connecting_node_id, broad_class);
        SELECT count(*) INTO phase_rows FROM attribution_identity_edges;
        PERFORM osm_catalog.checkpoint_generation_stage(target_generation, 'identity-components', expected_batch,
            jsonb_build_object('version', 2, 'phase', 'filter-edges', 'batch_size', filter_batch_size,
                'batch_index', 1, 'batch_total', (phase_rows + filter_batch_size - 1) / filter_batch_size + 1), processed);
        RETURN;
    END IF;

    IF phase IN ('filter-edges', 'components-left', 'components-right') THEN
        CREATE TEMP TABLE identity_component_edge_keys ON COMMIT DROP AS
        SELECT group_id, connecting_node_id, left_segment_id, right_segment_id
        FROM attribution_identity_edges
        WHERE cursor_group_id IS NULL
           OR (group_id, connecting_node_id, left_segment_id, right_segment_id) >
              (cursor_group_id, cursor_node_id, cursor_left_id, cursor_right_id)
        ORDER BY group_id, connecting_node_id, left_segment_id, right_segment_id
        LIMIT CASE WHEN phase = 'filter-edges' THEN filter_batch_size ELSE component_batch_size END;
        GET DIAGNOSTICS batch_rows = ROW_COUNT;
        IF batch_rows = 0 THEN
            IF phase = 'filter-edges' THEN
                CREATE TABLE attribution_identity_components (
                    group_id uuid NOT NULL,
                    segment_id uuid NOT NULL,
                    parent_segment_id uuid NOT NULL,
                    merged_logical_path_id uuid,
                    PRIMARY KEY (group_id, segment_id)
                );
                SELECT count(*) INTO phase_rows FROM attribution_identity_edges;
                PERFORM osm_catalog.checkpoint_generation_stage(target_generation, 'identity-components', expected_batch,
                    jsonb_build_object('version', 2, 'phase', 'components-left', 'batch_size', component_batch_size,
                        'batch_index', 1, 'batch_total', (phase_rows + component_batch_size - 1) / component_batch_size + 1), processed);
            ELSIF phase = 'components-left' THEN
                SELECT count(*) INTO phase_rows FROM attribution_identity_edges;
                PERFORM osm_catalog.checkpoint_generation_stage(target_generation, 'identity-components', expected_batch,
                    jsonb_build_object('version', 2, 'phase', 'components-right', 'batch_size', component_batch_size,
                        'batch_index', 1, 'batch_total', (phase_rows + component_batch_size - 1) / component_batch_size + 1), processed);
            ELSE
                PERFORM osm_catalog.checkpoint_generation_stage(target_generation, 'identity-components', expected_batch,
                    jsonb_build_object('version', 2, 'phase', 'component-index'), processed);
            END IF;
            RETURN;
        END IF;

        SELECT group_id, connecting_node_id, left_segment_id, right_segment_id
        INTO cursor_group_id, cursor_node_id, cursor_left_id, cursor_right_id
        FROM identity_component_edge_keys
        ORDER BY group_id DESC, connecting_node_id DESC, left_segment_id DESC, right_segment_id DESC
        LIMIT 1;

        IF phase = 'filter-edges' THEN
            DELETE FROM attribution_identity_edges AS edge
            USING identity_component_edge_keys AS key, attribution_identity_branch_nodes AS branch
            WHERE edge.group_id = key.group_id
              AND edge.connecting_node_id = key.connecting_node_id
              AND edge.left_segment_id = key.left_segment_id
              AND edge.right_segment_id = key.right_segment_id
              AND branch.group_id = edge.group_id
              AND branch.connecting_node_id = edge.connecting_node_id
              AND edge.unnamed
              AND edge.left_source_way_id <> edge.right_source_way_id
              AND NOT EXISTS (
                  SELECT 1 FROM attribution_identity_branch_classes AS class_pair
                  WHERE class_pair.group_id = edge.group_id
                    AND class_pair.connecting_node_id = edge.connecting_node_id
                    AND edge.left_broad_class = edge.right_broad_class
                    AND class_pair.broad_class = edge.left_broad_class
              );
        ELSIF phase = 'components-left' THEN
            INSERT INTO attribution_identity_components
            SELECT edge.group_id, edge.left_segment_id, edge.left_segment_id, NULL
            FROM identity_component_edge_keys AS key
            JOIN attribution_identity_edges AS edge USING (group_id, connecting_node_id, left_segment_id, right_segment_id)
            ON CONFLICT DO NOTHING;
        ELSE
            INSERT INTO attribution_identity_components
            SELECT edge.group_id, edge.right_segment_id, edge.right_segment_id, NULL
            FROM identity_component_edge_keys AS key
            JOIN attribution_identity_edges AS edge USING (group_id, connecting_node_id, left_segment_id, right_segment_id)
            ON CONFLICT DO NOTHING;
        END IF;

        PERFORM osm_catalog.checkpoint_generation_stage(
            target_generation,
            'identity-components',
            expected_batch,
            jsonb_build_object(
                'version', 2,
                'phase', phase,
                'group_id', cursor_group_id,
                'connecting_node_id', cursor_node_id,
                'left_segment_id', cursor_left_id,
                'right_segment_id', cursor_right_id,
                'batch_size', CASE WHEN phase = 'filter-edges' THEN filter_batch_size ELSE component_batch_size END,
                'batch_index', batch_index + 1,
                'batch_total', batch_total
            ),
            processed + batch_rows
        );
        RETURN;
    END IF;

    IF phase = 'component-index' THEN
        CREATE INDEX attribution_identity_components_parent_idx
        ON attribution_identity_components (group_id, parent_segment_id);
        ANALYZE attribution_identity_edges;
        ANALYZE attribution_identity_components;
        DROP TABLE attribution_identity_branch_classes;
        DROP TABLE attribution_identity_branch_nodes;
        DROP TABLE attribution_identity_named_roads;
        ALTER TABLE attribution_identity_edges DROP CONSTRAINT attribution_identity_edges_pkey;
        DROP INDEX IF EXISTS attribution_identity_segments_end_idx;
        PERFORM osm_catalog.complete_generation_stage(target_generation, 'identity-components', expected_batch,
            jsonb_build_object('version', 2, 'phase', 'done'), processed);
        RETURN;
    END IF;

    RAISE EXCEPTION 'unknown identity-component phase %', phase;
END;
$procedure$;

CALL stage_identity_components(:'OSM_GENERATION_ID'::bigint);

DROP PROCEDURE stage_identity_components(bigint);
