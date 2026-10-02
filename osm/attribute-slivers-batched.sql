SET search_path TO :"OSM_BUILD_SCHEMA", public;

CREATE TABLE IF NOT EXISTS locality_scope_slivers (
    segment_id uuid PRIMARY KEY,
    replacement_locality_relation_id bigint NOT NULL
);

CREATE OR REPLACE PROCEDURE attribute_slivers_batched(target_generation bigint)
LANGUAGE plpgsql
AS $procedure$
DECLARE
    phase text;
    cursor_id uuid;
    next_cursor uuid;
    expected_batch bigint;
    processed bigint;
    batch_rows bigint;
    batch_index bigint;
    batch_total bigint;
    phase_rows bigint;
    cursor_rows bigint;
    batch_size constant integer := 500000;
BEGIN
    SELECT coalesce(cursor->>'phase', 'discover'),
        NULLIF(cursor->>'last_segment_id', '')::uuid,
        batch_count,
        rows_processed,
        (cursor->>'batch_index')::bigint,
        (cursor->>'batch_total')::bigint
    INTO phase, cursor_id, expected_batch, processed, batch_index, batch_total
    FROM osm_catalog.generation_stages
    WHERE generation_id = target_generation
      AND stage = 'attribute-slivers'
    FOR UPDATE;

    IF batch_index IS NULL OR batch_total IS NULL THEN
        IF phase = 'apply' THEN
            SELECT count(*), count(*) FILTER (WHERE cursor_id IS NOT NULL AND segment_id <= cursor_id)
            INTO phase_rows, cursor_rows
            FROM locality_scope_slivers;
        ELSE
            SELECT count(*), count(*) FILTER (WHERE cursor_id IS NOT NULL AND segment_id <= cursor_id)
            INTO phase_rows, cursor_rows
            FROM path_segments;
        END IF;
        batch_index := (cursor_rows + batch_size - 1) / batch_size + 1;
        batch_total := (phase_rows + batch_size - 1) / batch_size + 1;
    END IF;

    IF phase = 'discover' THEN
        CREATE TEMP TABLE sliver_batch_keys ON COMMIT DROP AS
        SELECT segment_id
        FROM path_segments
        WHERE cursor_id IS NULL OR segment_id > cursor_id
        ORDER BY segment_id
        LIMIT batch_size;

        GET DIAGNOSTICS batch_rows = ROW_COUNT;
        IF batch_rows = 0 THEN
            DROP TABLE IF EXISTS attribution_scope_sliver_stats;

            CREATE TABLE attribution_scope_sliver_stats AS
            SELECT count(*)::bigint AS absorbed_segments,
                coalesce(sum(segment.length_m), 0)::double precision AS absorbed_length_m
            FROM locality_scope_slivers AS sliver
            JOIN path_segments AS segment USING (segment_id);

            SELECT count(*) INTO phase_rows FROM locality_scope_slivers;
            PERFORM osm_catalog.checkpoint_generation_stage(
                target_generation,
                'attribute-slivers',
                expected_batch,
                jsonb_build_object('version', 1, 'phase', 'apply', 'last_segment_id', NULL, 'batch_size', batch_size,
                    'batch_index', 1, 'batch_total', (phase_rows + batch_size - 1) / batch_size + 1),
                processed
            );
            RETURN;
        END IF;

        SELECT segment_id INTO next_cursor FROM sliver_batch_keys ORDER BY segment_id DESC LIMIT 1;

        INSERT INTO locality_scope_slivers
        SELECT middle.segment_id, previous.locality_relation_id
        FROM sliver_batch_keys AS key
        JOIN path_segments AS middle USING (segment_id)
        JOIN path_segments AS previous
          ON previous.source_way_id = middle.source_way_id
         AND previous.source_way_version = middle.source_way_version
         AND previous.start_node_index = middle.start_node_index
         AND previous.end_node_index = middle.end_node_index
         AND previous.boundary_piece = middle.boundary_piece - 1
        JOIN path_segments AS following
          ON following.source_way_id = middle.source_way_id
         AND following.source_way_version = middle.source_way_version
         AND following.start_node_index = middle.start_node_index
         AND following.end_node_index = middle.end_node_index
         AND following.boundary_piece = middle.boundary_piece + 1
        WHERE middle.locality_relation_id IS NOT NULL
          AND middle.broad_class <> 'road'
          AND middle.length_m <= 25
          AND previous.locality_relation_id IS NOT NULL
          AND following.locality_relation_id = previous.locality_relation_id
          AND middle.locality_relation_id <> previous.locality_relation_id
          AND EXISTS (
              SELECT 1
              FROM localities AS municipality
              WHERE municipality.relation_id = middle.locality_relation_id
                AND municipality.admin_level = 8
          )
          AND EXISTS (
              SELECT 1
              FROM localities AS county
              WHERE county.relation_id = previous.locality_relation_id
                AND county.admin_level = 6
          )
          AND previous.normalized_name IS NOT DISTINCT FROM middle.normalized_name
          AND following.normalized_name IS NOT DISTINCT FROM middle.normalized_name
          AND previous.broad_class = middle.broad_class
          AND following.broad_class = middle.broad_class
          AND NOT previous.tags ? 'workouts:park_id'
          AND NOT middle.tags ? 'workouts:park_id'
          AND NOT following.tags ? 'workouts:park_id'
        ON CONFLICT (segment_id) DO NOTHING;

        PERFORM osm_catalog.checkpoint_generation_stage(
            target_generation,
            'attribute-slivers',
            expected_batch,
            jsonb_build_object('version', 1, 'phase', 'discover', 'last_segment_id', next_cursor, 'batch_size', batch_size,
                'batch_index', batch_index + 1, 'batch_total', batch_total),
            processed + batch_rows
        );
        RETURN;
    END IF;

    IF phase <> 'apply' THEN
        RAISE EXCEPTION 'unknown attribution sliver phase %', phase;
    END IF;

    CREATE TEMP TABLE sliver_apply_keys ON COMMIT DROP AS
    SELECT segment_id
    FROM locality_scope_slivers
    WHERE cursor_id IS NULL OR segment_id > cursor_id
    ORDER BY segment_id
    LIMIT batch_size;

    GET DIAGNOSTICS batch_rows = ROW_COUNT;
    IF batch_rows = 0 THEN
        DROP TABLE locality_scope_slivers;
        PERFORM osm_catalog.complete_generation_stage(
            target_generation,
            'attribute-slivers',
            expected_batch,
            jsonb_build_object('version', 1, 'phase', 'done', 'last_segment_id', cursor_id, 'batch_size', batch_size,
                'batch_index', batch_index, 'batch_total', batch_total),
            processed
        );
        RETURN;
    END IF;

    SELECT segment_id INTO next_cursor FROM sliver_apply_keys ORDER BY segment_id DESC LIMIT 1;

    UPDATE path_segments AS segment
    SET locality_relation_id = sliver.replacement_locality_relation_id
    FROM locality_scope_slivers AS sliver
    JOIN sliver_apply_keys AS key USING (segment_id)
    WHERE segment.segment_id = sliver.segment_id;

    PERFORM osm_catalog.checkpoint_generation_stage(
        target_generation,
        'attribute-slivers',
        expected_batch,
        jsonb_build_object('version', 1, 'phase', 'apply', 'last_segment_id', next_cursor, 'batch_size', batch_size,
            'batch_index', batch_index + 1, 'batch_total', batch_total),
        processed + batch_rows
    );
END;
$procedure$;

CALL attribute_slivers_batched(:'OSM_GENERATION_ID'::bigint);

DROP PROCEDURE attribute_slivers_batched(bigint);
