SET search_path TO :"OSM_BUILD_SCHEMA", public;

SELECT set_config('workouts_explorer.osm_region_id', :'OSM_REGION_ID', false);

CREATE TABLE IF NOT EXISTS attribution_identity_segments (
    segment_id uuid PRIMARY KEY,
    source_way_id bigint NOT NULL,
    start_graph_node_id uuid NOT NULL,
    end_graph_node_id uuid NOT NULL,
    unnamed boolean NOT NULL,
    broad_class text NOT NULL,
    oneway boolean NOT NULL,
    group_id uuid NOT NULL
);

CREATE OR REPLACE PROCEDURE stage_identity_segments(target_generation bigint)
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
      AND stage = 'identity-segments'
    FOR UPDATE;

    IF batch_index IS NULL OR batch_total IS NULL THEN
        SELECT count(*), count(*) FILTER (WHERE cursor_id IS NOT NULL AND segment_id <= cursor_id)
        INTO phase_rows, cursor_rows
        FROM path_segments;
        batch_index := (cursor_rows + 500000 - 1) / 500000 + 1;
        batch_total := (phase_rows + 500000 - 1) / 500000 + 1;
    END IF;

    CREATE TEMP TABLE identity_segment_keys ON COMMIT DROP AS
    SELECT segment_id
    FROM path_segments
    WHERE cursor_id IS NULL OR segment_id > cursor_id
    ORDER BY segment_id
    LIMIT 500000;

    GET DIAGNOSTICS batch_rows = ROW_COUNT;
    IF batch_rows = 0 THEN
        CREATE INDEX IF NOT EXISTS attribution_identity_segments_start_idx
        ON attribution_identity_segments (group_id, start_graph_node_id, segment_id);

        CREATE INDEX IF NOT EXISTS attribution_identity_segments_end_idx
        ON attribution_identity_segments (group_id, end_graph_node_id, segment_id);

        ANALYZE attribution_identity_segments;

        PERFORM osm_catalog.complete_generation_stage(
            target_generation,
            'identity-segments',
            expected_batch,
            jsonb_build_object('version', 2, 'done', true, 'last_segment_id', cursor_id, 'batch_size', 500000,
                'batch_index', batch_index, 'batch_total', batch_total),
            processed
        );
        RETURN;
    END IF;

    SELECT segment_id INTO next_cursor FROM identity_segment_keys ORDER BY segment_id DESC LIMIT 1;

    INSERT INTO attribution_identity_segments
    SELECT segment_id,
        source_way_id,
        start_graph_node_id,
        end_graph_node_id,
        unnamed,
        broad_class,
        oneway,
        md5(format(
            'workouts-explorer/osm-attribution-group/v3:%s:%s:%s:%s:name:%s:%s:class:%s:%s',
            length(scope_kind),
            scope_kind,
            length(scope_id),
            scope_id,
            length(name_key),
            name_key,
            length(attribution_class),
            attribution_class
        ))::uuid
    FROM (
        SELECT segment.segment_id,
            segment.source_way_id,
            segment.start_graph_node_id,
            segment.end_graph_node_id,
            segment.normalized_name IS NULL AS unnamed,
            segment.broad_class,
            coalesce(segment.tags->>'oneway', '') IN ('yes', '1', '-1') AS oneway,
            CASE
                WHEN segment.normalized_name IS NOT NULL THEN 'named'
                WHEN segment.broad_class = 'road' THEN 'road'
                ELSE 'path'
            END AS attribution_class,
            CASE
                WHEN segment.normalized_name IS NOT NULL THEN 'named'
                WHEN segment.tags ? 'workouts:education_id' THEN 'education'
                WHEN segment.broad_class <> 'road' AND segment.tags ? 'workouts:park_id' THEN 'park'
                WHEN segment.locality_relation_id IS NOT NULL THEN 'locality'
                ELSE 'region'
            END AS scope_kind,
            CASE
                WHEN segment.normalized_name IS NOT NULL THEN current_setting('workouts_explorer.osm_region_id')
                WHEN segment.tags ? 'workouts:education_id' THEN segment.tags->>'workouts:education_id'
                WHEN segment.broad_class <> 'road' AND segment.tags ? 'workouts:park_id' THEN segment.tags->>'workouts:park_id'
                WHEN segment.locality_relation_id IS NOT NULL THEN segment.locality_relation_id::text
                ELSE current_setting('workouts_explorer.osm_region_id')
            END AS scope_id,
            CASE
                WHEN segment.normalized_name IS NULL THEN 'U'
                ELSE 'N:' || exact_name.value
            END AS name_key
        FROM identity_segment_keys AS key
        JOIN path_segments AS segment USING (segment_id)
        CROSS JOIN LATERAL (
            SELECT lower(regexp_replace(btrim(segment.name), '[[:space:]]+', ' ', 'g')) AS value
        ) AS exact_name
    ) AS scoped
    ON CONFLICT (segment_id) DO NOTHING;

    PERFORM osm_catalog.checkpoint_generation_stage(
        target_generation,
        'identity-segments',
        expected_batch,
        jsonb_build_object('version', 2, 'done', false, 'last_segment_id', next_cursor, 'batch_size', 500000,
            'batch_index', batch_index + 1, 'batch_total', batch_total),
        processed + batch_rows
    );
END;
$procedure$;

CALL stage_identity_segments(:'OSM_GENERATION_ID'::bigint);

DROP PROCEDURE stage_identity_segments(bigint);
