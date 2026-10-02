SET jit = off;
SET work_mem = '64MB';
SET max_parallel_workers_per_gather = 0;
SET search_path TO :"OSM_BUILD_SCHEMA", public;

CREATE TABLE IF NOT EXISTS locality_clip_candidates (
    segment_id uuid PRIMARY KEY
);

CREATE OR REPLACE PROCEDURE populate_locality_clip_candidates(target_generation bigint)
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
BEGIN
    SELECT NULLIF(cursor->>'last_segment_id', '')::uuid, batch_count, rows_processed,
        (cursor->>'batch_index')::bigint, (cursor->>'batch_total')::bigint
    INTO cursor_id, expected_batch, processed, batch_index, batch_total
    FROM osm_catalog.generation_stages
    WHERE generation_id = target_generation
      AND stage = 'clip-candidates'
    FOR UPDATE;

    IF batch_index IS NULL OR batch_total IS NULL THEN
        SELECT count(*), count(*) FILTER (WHERE cursor_id IS NOT NULL AND segment_id <= cursor_id)
        INTO phase_rows, cursor_rows
        FROM path_segments;
        batch_index := (cursor_rows + 200000 - 1) / 200000 + 1;
        batch_total := (phase_rows + 200000 - 1) / 200000 + 1;
    END IF;

    CREATE TEMP TABLE clip_candidate_keys ON COMMIT DROP AS
    SELECT segment_id
    FROM path_segments
    WHERE cursor_id IS NULL OR segment_id > cursor_id
    ORDER BY segment_id
    LIMIT 200000;

    GET DIAGNOSTICS batch_rows = ROW_COUNT;
    IF batch_rows = 0 THEN
        PERFORM osm_catalog.complete_generation_stage(
            target_generation,
            'clip-candidates',
            expected_batch,
            jsonb_build_object('version', 1, 'done', true, 'last_segment_id', cursor_id, 'batch_size', 200000,
                'batch_index', batch_index, 'batch_total', batch_total),
            processed
        );
        RETURN;
    END IF;

    SELECT segment_id INTO next_cursor FROM clip_candidate_keys ORDER BY segment_id DESC LIMIT 1;

    INSERT INTO locality_clip_candidates (segment_id)
    SELECT DISTINCT segment.segment_id
    FROM clip_candidate_keys AS key
    JOIN path_segments AS segment USING (segment_id)
    JOIN localities AS locality
      ON locality.geom && segment.geom
     AND ST_Intersects(segment.geom, ST_Boundary(locality.geom))
    ON CONFLICT DO NOTHING;

    PERFORM osm_catalog.checkpoint_generation_stage(
        target_generation,
        'clip-candidates',
        expected_batch,
        jsonb_build_object('version', 1, 'done', false, 'last_segment_id', next_cursor, 'batch_size', 200000,
            'batch_index', batch_index + 1, 'batch_total', batch_total),
        processed + batch_rows
    );
END;
$procedure$;

CALL populate_locality_clip_candidates(:'OSM_GENERATION_ID'::bigint);

DROP PROCEDURE populate_locality_clip_candidates(bigint);
