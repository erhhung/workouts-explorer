SET search_path TO :"OSM_BUILD_SCHEMA", public;

CREATE OR REPLACE PROCEDURE apply_locality_clip_replacements(target_generation bigint)
LANGUAGE plpgsql
AS $procedure$
DECLARE
    cursor_id uuid;
    next_cursor uuid;
    expected_batch bigint;
    processed bigint;
    batch_rows bigint;
    batch_index bigint;
    batch_total bigint;
    phase_rows bigint;
    cursor_rows bigint;
    batch_size constant integer := 18000;
BEGIN
    SELECT NULLIF(cursor->>'last_segment_id', '')::uuid, batch_count, rows_processed,
        (cursor->>'batch_index')::bigint, (cursor->>'batch_total')::bigint
    INTO cursor_id, expected_batch, processed, batch_index, batch_total
    FROM osm_catalog.generation_stages
    WHERE generation_id = target_generation
      AND stage = 'clip-apply'
    FOR UPDATE;

    IF batch_index IS NULL OR batch_total IS NULL THEN
        SELECT count(*), count(*) FILTER (WHERE cursor_id IS NOT NULL AND segment_id <= cursor_id)
        INTO phase_rows, cursor_rows
        FROM locality_clip_candidates;
        batch_index := (cursor_rows + batch_size - 1) / batch_size + 1;
        batch_total := (phase_rows + batch_size - 1) / batch_size + 1;
    END IF;

    CREATE TEMP TABLE clip_apply_keys ON COMMIT DROP AS
    SELECT segment_id
    FROM locality_clip_candidates
    WHERE cursor_id IS NULL OR segment_id > cursor_id
    ORDER BY segment_id
    LIMIT batch_size;

    GET DIAGNOSTICS batch_rows = ROW_COUNT;
    IF batch_rows = 0 THEN
        PERFORM osm_catalog.complete_generation_stage(
            target_generation,
            'clip-apply',
            expected_batch,
            jsonb_build_object('version', 1, 'done', true, 'last_segment_id', cursor_id, 'batch_size', batch_size,
                'batch_index', batch_index, 'batch_total', batch_total),
            processed
        );
        RETURN;
    END IF;

    SELECT segment_id INTO next_cursor FROM clip_apply_keys ORDER BY segment_id DESC LIMIT 1;

    DELETE FROM path_segments AS segment
    USING clip_apply_keys AS key
    WHERE segment.segment_id = key.segment_id;

    INSERT INTO path_segments
    SELECT replacement.segment_id,
        replacement.source_way_id,
        replacement.source_way_version,
        replacement.derivation_version,
        replacement.start_node_index,
        replacement.end_node_index,
        replacement.boundary_piece,
        replacement.start_graph_node_id,
        replacement.end_graph_node_id,
        replacement.name,
        replacement.normalized_name,
        replacement.highway,
        replacement.broad_class,
        replacement.tags,
        replacement.motor_forward_allowed,
        replacement.motor_reverse_allowed,
        replacement.geom,
        replacement.locality_relation_id,
        replacement.logical_path_id,
        replacement.length_m
    FROM locality_clip_replacements AS replacement
    JOIN clip_apply_keys AS key ON key.segment_id = replacement.source_segment_id;

    PERFORM osm_catalog.checkpoint_generation_stage(
        target_generation,
        'clip-apply',
        expected_batch,
        jsonb_build_object('version', 1, 'done', false, 'last_segment_id', next_cursor, 'batch_size', batch_size,
            'batch_index', batch_index + 1, 'batch_total', batch_total),
        processed + batch_rows
    );
END;
$procedure$;

CALL apply_locality_clip_replacements(:'OSM_GENERATION_ID'::bigint);

DROP PROCEDURE apply_locality_clip_replacements(bigint);
