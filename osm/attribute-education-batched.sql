SET search_path TO :"OSM_BUILD_SCHEMA", public;

SELECT set_config('workouts_explorer.osm_region_id', :'OSM_REGION_ID', false);

CREATE OR REPLACE PROCEDURE attribute_education_batched(target_generation bigint)
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
      AND stage = 'attribute-education'
    FOR UPDATE;

    IF batch_index IS NULL OR batch_total IS NULL THEN
        SELECT count(*), count(*) FILTER (WHERE cursor_id IS NOT NULL AND segment_id <= cursor_id)
        INTO phase_rows, cursor_rows
        FROM path_segments;
        batch_index := (cursor_rows + 750000 - 1) / 750000 + 1;
        batch_total := (phase_rows + 750000 - 1) / 750000 + 1;
    END IF;

    CREATE TEMP TABLE area_batch_keys ON COMMIT DROP AS
    SELECT segment_id
    FROM path_segments
    WHERE cursor_id IS NULL OR segment_id > cursor_id
    ORDER BY segment_id
    LIMIT 750000;

    GET DIAGNOSTICS batch_rows = ROW_COUNT;
    IF batch_rows = 0 THEN
        PERFORM osm_catalog.complete_generation_stage(
            target_generation,
            'attribute-education',
            expected_batch,
            jsonb_build_object('version', 1, 'done', true, 'last_segment_id', cursor_id, 'batch_size', 750000,
                'batch_index', batch_index, 'batch_total', batch_total),
            processed
        );
        RETURN;
    END IF;

    SELECT segment_id INTO next_cursor FROM area_batch_keys ORDER BY segment_id DESC LIMIT 1;

    WITH selected AS MATERIALIZED (
        SELECT segment.segment_id,
            education.source_type,
            education.source_id,
            education.version,
            education.name,
            education.normalized_name,
            education.education_kind,
            md5(format(
                'workouts-explorer/osm-education/v1:%s:%s:%s',
                current_setting('workouts_explorer.osm_region_id'),
                education.source_type,
                education.source_id
            ))::uuid AS education_id
        FROM area_batch_keys AS key
        JOIN path_segments AS segment USING (segment_id)
        JOIN LATERAL (
            SELECT candidate.*
            FROM education_areas AS candidate
            WHERE segment.normalized_name IS NULL
              AND candidate.geom && segment.geom
              AND ST_Covers(candidate.geom, ST_LineInterpolatePoint(segment.geom, 0.5))
              AND ST_Length(
                  ST_CollectionExtract(ST_Difference(segment.geom, candidate.geom), 2)::geography
              ) <= 0.01
            ORDER BY candidate.area_m2,
                candidate.type_priority,
                CASE candidate.source_type WHEN 'relation' THEN 0 ELSE 1 END,
                candidate.source_id
            LIMIT 1
        ) AS education ON true
    )
    UPDATE path_segments AS segment
    SET tags = segment.tags || jsonb_build_object(
        'workouts:education_id', selected.education_id::text,
        'workouts:education_kind', selected.education_kind,
        'workouts:education_name', selected.name,
        'workouts:education_normalized_name', selected.normalized_name,
        'workouts:education_source_type', selected.source_type,
        'workouts:education_source_id', selected.source_id,
        'workouts:education_source_version', selected.version
    )
    FROM selected
    WHERE selected.segment_id = segment.segment_id;

    PERFORM osm_catalog.checkpoint_generation_stage(
        target_generation,
        'attribute-education',
        expected_batch,
        jsonb_build_object('version', 1, 'done', false, 'last_segment_id', next_cursor, 'batch_size', 750000,
            'batch_index', batch_index + 1, 'batch_total', batch_total),
        processed + batch_rows
    );
END;
$procedure$;

CALL attribute_education_batched(:'OSM_GENERATION_ID'::bigint);

DROP PROCEDURE attribute_education_batched(bigint);
