-- Split only boundary-crossing physical segments at ordered fractions along the
-- original line. This conserves source length even when locality polygons overlap.
SET jit = off;
SET work_mem = '64MB';
SET maintenance_work_mem = '512MB';
SET max_parallel_workers_per_gather = 0;
SELECT set_config('workouts_explorer.osm_derivation_version', :'OSM_DERIVATION_VERSION', false);
SET search_path TO :"OSM_BUILD_SCHEMA", public;

DROP TABLE IF EXISTS locality_clip_candidates;
DROP TABLE IF EXISTS locality_clip_replacements;

CREATE UNLOGGED TABLE locality_clip_candidates AS
SELECT DISTINCT segment.segment_id
FROM path_segments AS segment
JOIN localities AS locality
  ON locality.geom && segment.geom
 AND ST_Intersects(segment.geom, ST_Boundary(locality.geom));
ALTER TABLE locality_clip_candidates ADD PRIMARY KEY (segment_id);
ANALYZE locality_clip_candidates;

CREATE UNLOGGED TABLE locality_clip_replacements AS
WITH boundary_hits AS (
    SELECT segment.segment_id,
        ST_Intersection(segment.geom, ST_Boundary(locality.geom)) AS hit
    FROM path_segments AS segment
    JOIN locality_clip_candidates AS candidate USING (segment_id)
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
    SELECT candidate.segment_id, 0::double precision AS fraction
    FROM locality_clip_candidates AS candidate
    UNION
    SELECT candidate.segment_id, 1::double precision
    FROM locality_clip_candidates AS candidate
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
    SELECT segment.*,
        range.start_fraction, range.end_fraction,
        row_number() OVER (
            PARTITION BY segment.segment_id ORDER BY range.start_fraction
        )::integer AS final_piece,
        count(*) OVER (PARTITION BY segment.segment_id)::integer AS piece_count,
        ST_LineSubstring(segment.geom, range.start_fraction, range.end_fraction)
            ::geometry(LineString, 4326) AS piece_geom
    FROM ranges AS range
    JOIN path_segments AS segment USING (segment_id)
    WHERE range.end_fraction IS NOT NULL
      AND range.start_fraction < range.end_fraction
), assigned AS (
    SELECT piece.*,
        locality.relation_id AS clipped_locality_id
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
SELECT
    md5(format('workouts-explorer/osm-segment/v%s:%s:%s:%s:%s:%s',
        current_setting('workouts_explorer.osm_derivation_version'),
        source_way_id, source_way_version, start_node_index, end_node_index,
        final_piece))::uuid AS segment_id,
    source_way_id, source_way_version, derivation_version,
    start_node_index, end_node_index, final_piece AS boundary_piece,
    CASE WHEN final_piece = 1 THEN start_graph_node_id
         ELSE md5(format('workouts-explorer/osm-boundary-node/v%s:%s:%s:%s:%s',
             current_setting('workouts_explorer.osm_derivation_version'),
             source_way_id, start_node_index, end_node_index, final_piece))::uuid
    END AS start_graph_node_id,
    CASE WHEN final_piece = piece_count THEN end_graph_node_id
         ELSE md5(format('workouts-explorer/osm-boundary-node/v%s:%s:%s:%s:%s',
             current_setting('workouts_explorer.osm_derivation_version'),
             source_way_id, start_node_index, end_node_index, final_piece + 1))::uuid
    END AS end_graph_node_id,
    name, normalized_name, highway, broad_class, tags,
    motor_forward_allowed, motor_reverse_allowed,
    piece_geom AS geom,
    clipped_locality_id AS locality_relation_id,
    CASE
        WHEN normalized_name IS NOT NULL THEN md5(
            format('workouts-explorer/osm-logical-path/v1:%s:%s:%s:%s',
                coalesce(clipped_locality_id::text, 'outside'),
                length(broad_class), broad_class, length(normalized_name)) || normalized_name
        )::uuid
        ELSE md5(format('workouts-explorer/osm-unnamed-path/v1:%s:%s:%s:%s:%s',
            source_way_id, source_way_version, start_node_index, end_node_index,
            final_piece))::uuid
    END AS logical_path_id,
    ST_Length(piece_geom::geography) AS length_m
FROM assigned;

BEGIN;
DELETE FROM path_segments AS segment
USING locality_clip_candidates AS candidate
WHERE segment.segment_id = candidate.segment_id;

INSERT INTO path_segments SELECT * FROM locality_clip_replacements;

UPDATE path_segments AS segment
SET locality_relation_id = NULL,
    logical_path_id = CASE
        WHEN segment.normalized_name IS NOT NULL THEN md5(
            format('workouts-explorer/osm-logical-path/v1:outside:%s:%s:%s',
                length(segment.broad_class), segment.broad_class,
                length(segment.normalized_name)) || segment.normalized_name
        )::uuid
        ELSE md5(format('workouts-explorer/osm-unnamed-path/v1:%s:%s:%s:%s:%s',
            segment.source_way_id, segment.source_way_version,
            segment.start_node_index, segment.end_node_index,
            segment.boundary_piece))::uuid
    END
FROM localities AS locality
WHERE locality.relation_id = segment.locality_relation_id
  AND ST_Length(
      ST_CollectionExtract(ST_Difference(segment.geom, locality.geom), 2)::geography
  ) > 0.01;

TRUNCATE logical_paths;
INSERT INTO logical_paths
SELECT logical_path_id,
    min(locality_relation_id), min(name), min(normalized_name), min(broad_class),
    count(*), sum(length_m)
FROM path_segments
GROUP BY logical_path_id;
COMMIT;

ANALYZE path_segments;
ANALYZE logical_paths;
DROP TABLE locality_clip_replacements;
DROP TABLE locality_clip_candidates;
