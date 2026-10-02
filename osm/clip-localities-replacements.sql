SET jit = off;
SET work_mem = '64MB';
SET max_parallel_workers_per_gather = 0;

SELECT set_config('workouts_explorer.osm_derivation_version', :'OSM_DERIVATION_VERSION', false);

SELECT set_config('workouts_explorer.osm_region_id', :'OSM_REGION_ID', false);

SET search_path TO :"OSM_BUILD_SCHEMA", public;

CREATE TABLE IF NOT EXISTS locality_clip_replacements (
    source_segment_id uuid NOT NULL,
    LIKE path_segments INCLUDING DEFAULTS INCLUDING GENERATED INCLUDING IDENTITY INCLUDING CONSTRAINTS INCLUDING STORAGE INCLUDING COMMENTS,
    PRIMARY KEY (segment_id)
);

CREATE INDEX IF NOT EXISTS locality_clip_replacements_source_idx
ON locality_clip_replacements (source_segment_id);

CREATE OR REPLACE PROCEDURE build_locality_clip_replacements(target_generation bigint)
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
    WHERE generation_id = target_generation AND stage = 'clip-replacements'
    FOR UPDATE;

    IF batch_index IS NULL OR batch_total IS NULL THEN
        SELECT count(*), count(*) FILTER (WHERE cursor_id IS NOT NULL AND segment_id <= cursor_id)
        INTO phase_rows, cursor_rows
        FROM locality_clip_candidates;
        batch_index := (cursor_rows + 7500 - 1) / 7500 + 1;
        batch_total := (phase_rows + 7500 - 1) / 7500 + 1;
    END IF;

    CREATE TEMP TABLE clip_replacement_keys ON COMMIT DROP AS
    SELECT segment_id
    FROM locality_clip_candidates
    WHERE cursor_id IS NULL OR segment_id > cursor_id
    ORDER BY segment_id
    LIMIT 7500;

    GET DIAGNOSTICS batch_rows = ROW_COUNT;
    IF batch_rows = 0 THEN
        PERFORM osm_catalog.complete_generation_stage(
            target_generation, 'clip-replacements', expected_batch,
            jsonb_build_object('version', 1, 'done', true, 'last_segment_id', cursor_id, 'batch_size', 7500,
                'batch_index', batch_index, 'batch_total', batch_total),
            processed
        );
        RETURN;
    END IF;

    SELECT segment_id INTO next_cursor FROM clip_replacement_keys ORDER BY segment_id DESC LIMIT 1;

    INSERT INTO locality_clip_replacements
    WITH boundary_hits AS (
        SELECT segment.segment_id,
            ST_Intersection(segment.geom, ST_Boundary(locality.geom)) AS hit
        FROM path_segments AS segment
        JOIN clip_replacement_keys AS key USING (segment_id)
        JOIN localities AS locality
          ON locality.geom && segment.geom
         AND ST_Intersects(segment.geom, ST_Boundary(locality.geom))
    ), hit_points AS (
        SELECT segment_id, point.geom::geometry(Point, 4326) AS geom
        FROM boundary_hits
        CROSS JOIN LATERAL ST_Dump(ST_CollectionExtract(hit, 1)) AS point

        UNION ALL

        SELECT segment_id, ST_StartPoint(line.geom)::geometry(Point, 4326)
        FROM boundary_hits
        CROSS JOIN LATERAL ST_Dump(ST_CollectionExtract(hit, 2)) AS line

        UNION ALL

        SELECT segment_id, ST_EndPoint(line.geom)::geometry(Point, 4326)
        FROM boundary_hits
        CROSS JOIN LATERAL ST_Dump(ST_CollectionExtract(hit, 2)) AS line
    ), fractions AS (
        SELECT segment_id, 0::double precision AS fraction FROM clip_replacement_keys
        UNION
        SELECT segment_id, 1::double precision FROM clip_replacement_keys
        UNION
        SELECT point.segment_id,
            round(ST_LineLocatePoint(segment.geom, point.geom)::numeric, 12)::double precision
        FROM hit_points AS point
        JOIN path_segments AS segment USING (segment_id)
    ), ranges AS (
        SELECT segment_id, fraction AS start_fraction,
            lead(fraction) OVER (PARTITION BY segment_id ORDER BY fraction) AS end_fraction
        FROM fractions
    ), pieces AS (
        SELECT segment.*, range.start_fraction, range.end_fraction,
            row_number() OVER (PARTITION BY segment.segment_id ORDER BY range.start_fraction)::integer AS final_piece,
            count(*) OVER (PARTITION BY segment.segment_id)::integer AS piece_count,
            ST_LineSubstring(segment.geom, range.start_fraction, range.end_fraction)::geometry(LineString, 4326) AS piece_geom
        FROM ranges AS range
        JOIN path_segments AS segment USING (segment_id)
        WHERE range.end_fraction IS NOT NULL AND range.start_fraction < range.end_fraction
    ), assigned AS (
        SELECT piece.*, locality.relation_id AS clipped_locality_id
        FROM pieces AS piece
        LEFT JOIN LATERAL (
            SELECT candidate.relation_id
            FROM localities AS candidate
            WHERE candidate.geom && ST_LineInterpolatePoint(piece.piece_geom, 0.5)
              AND ST_Covers(candidate.geom, ST_LineInterpolatePoint(piece.piece_geom, 0.5))
            ORDER BY ST_Area(candidate.geom::geography), candidate.relation_id
            LIMIT 1
        ) AS locality ON true
        WHERE ST_NPoints(piece.piece_geom) >= 2
          AND ST_Length(piece.piece_geom::geography) > 0
    )
    SELECT segment_id AS source_segment_id,
    md5(format('workouts-explorer/osm-segment/v%s:%s:%s:%s:%s:%s',
      current_setting('workouts_explorer.osm_derivation_version'),source_way_id,source_way_version,
      start_node_index,end_node_index,final_piece))::uuid,
    source_way_id,source_way_version,derivation_version,start_node_index,end_node_index,final_piece,
    CASE WHEN final_piece=1 THEN start_graph_node_id ELSE md5(format(
      'workouts-explorer/osm-boundary-node/v%s:%s:%s:%s:%s',current_setting('workouts_explorer.osm_derivation_version'),
      source_way_id,start_node_index,end_node_index,final_piece))::uuid END,
    CASE WHEN final_piece=piece_count THEN end_graph_node_id ELSE md5(format(
      'workouts-explorer/osm-boundary-node/v%s:%s:%s:%s:%s',current_setting('workouts_explorer.osm_derivation_version'),
      source_way_id,start_node_index,end_node_index,final_piece+1))::uuid END,
    name,normalized_name,highway,broad_class,tags,motor_forward_allowed,motor_reverse_allowed,piece_geom,
    clipped_locality_id,
    CASE WHEN normalized_name IS NOT NULL THEN md5(CASE WHEN clipped_locality_id IS NOT NULL THEN format(
      'workouts-explorer/osm-logical-path/v1:%s:%s:%s:%s',clipped_locality_id,length(broad_class),broad_class,length(normalized_name))||normalized_name
      ELSE format('workouts-explorer/osm-logical-path/v2:region:%s:%s:%s:%s:%s:',
      length(current_setting('workouts_explorer.osm_region_id')),current_setting('workouts_explorer.osm_region_id'),
      length(broad_class),broad_class,length(normalized_name))||normalized_name END)::uuid
    ELSE md5(format('workouts-explorer/osm-unnamed-path/v1:%s:%s:%s:%s:%s',source_way_id,source_way_version,
      start_node_index,end_node_index,final_piece))::uuid END,
    ST_Length(piece_geom::geography)
    FROM assigned
    ON CONFLICT (segment_id) DO NOTHING;

    PERFORM osm_catalog.checkpoint_generation_stage(
        target_generation, 'clip-replacements', expected_batch,
        jsonb_build_object('version', 1, 'done', false, 'last_segment_id', next_cursor, 'batch_size', 7500,
            'batch_index', batch_index + 1, 'batch_total', batch_total),
        processed + batch_rows
    );
END;
$procedure$;

CALL build_locality_clip_replacements(:'OSM_GENERATION_ID'::bigint);

DROP PROCEDURE build_locality_clip_replacements(bigint);
