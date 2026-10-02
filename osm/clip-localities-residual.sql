SELECT set_config('workouts_explorer.osm_region_id', :'OSM_REGION_ID', false);

SET search_path TO :"OSM_BUILD_SCHEMA", public;

CREATE OR REPLACE PROCEDURE correct_locality_clip_residuals(target_generation bigint)
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
    batch_size constant integer := 10000;
BEGIN
    SELECT NULLIF(cursor->>'last_segment_id', '')::uuid, batch_count, rows_processed,
        (cursor->>'batch_index')::bigint, (cursor->>'batch_total')::bigint
    INTO cursor_id, expected_batch, processed, batch_index, batch_total
    FROM osm_catalog.generation_stages
    WHERE generation_id = target_generation
      AND stage = 'clip-residual'
    FOR UPDATE;

    IF batch_index IS NULL OR batch_total IS NULL THEN
        SELECT count(*), count(*) FILTER (WHERE cursor_id IS NOT NULL AND segment_id <= cursor_id)
        INTO phase_rows, cursor_rows
        FROM path_segments;
        batch_index := (cursor_rows + batch_size - 1) / batch_size + 1;
        batch_total := (phase_rows + batch_size - 1) / batch_size + 1;
    END IF;

    CREATE TEMP TABLE clip_residual_keys ON COMMIT DROP AS
    SELECT segment_id
    FROM path_segments
    WHERE cursor_id IS NULL OR segment_id > cursor_id
    ORDER BY segment_id
    LIMIT batch_size;

    GET DIAGNOSTICS batch_rows = ROW_COUNT;
    IF batch_rows = 0 THEN
        PERFORM osm_catalog.complete_generation_stage(
            target_generation,
            'clip-residual',
            expected_batch,
            jsonb_build_object('version', 2, 'done', true, 'last_segment_id', cursor_id, 'batch_size', batch_size,
                'batch_index', batch_index, 'batch_total', batch_total),
            processed
        );
        RETURN;
    END IF;

    SELECT segment_id INTO next_cursor FROM clip_residual_keys ORDER BY segment_id DESC LIMIT 1;

    UPDATE path_segments AS segment
    SET locality_relation_id = NULL,
        logical_path_id = CASE
            WHEN segment.normalized_name IS NOT NULL THEN md5(format(
                'workouts-explorer/osm-logical-path/v2:region:%s:%s:%s:%s:%s:',
                length(current_setting('workouts_explorer.osm_region_id')),
                current_setting('workouts_explorer.osm_region_id'),
                length(segment.broad_class),
                segment.broad_class,
                length(segment.normalized_name)
            ) || segment.normalized_name)::uuid
            ELSE md5(format(
                'workouts-explorer/osm-unnamed-path/v1:%s:%s:%s:%s:%s',
                segment.source_way_id,
                segment.source_way_version,
                segment.start_node_index,
                segment.end_node_index,
                segment.boundary_piece
            ))::uuid
        END
    FROM clip_residual_keys AS key, localities AS locality
    WHERE segment.segment_id = key.segment_id
      AND locality.relation_id = segment.locality_relation_id
      AND ST_Length(
          ST_CollectionExtract(ST_Difference(segment.geom, locality.geom), 2)::geography
      ) > 0.01;

    PERFORM osm_catalog.checkpoint_generation_stage(
        target_generation,
        'clip-residual',
        expected_batch,
        jsonb_build_object('version', 2, 'done', false, 'last_segment_id', next_cursor, 'batch_size', batch_size,
            'batch_index', batch_index + 1, 'batch_total', batch_total),
        processed + batch_rows
    );
END;
$procedure$;

CALL correct_locality_clip_residuals(:'OSM_GENERATION_ID'::bigint);

DROP PROCEDURE correct_locality_clip_residuals(bigint);
