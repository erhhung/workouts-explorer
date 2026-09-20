-- Attach stable park identity to fully contained path segments.
SET search_path TO :"OSM_BUILD_SCHEMA", public;

\if :{?OSM_SKIP_PARK_ATTRIBUTION}
\echo 'OSM identity evaluation: preserving existing park attribution'
\else
\echo 'OSM progress: attributing segments to parks'

WITH selected AS MATERIALIZED (
    SELECT segment.segment_id,park.source_type,park.source_id,park.version,park.name,park.normalized_name,park.park_kind,
        CASE WHEN park.park_kind='national_park' THEN
            md5(format('workouts-explorer/osm-national-park/v1:%s:%s:%s',
                :'OSM_REGION_ID',park.source_type,park.source_id))::uuid
        ELSE md5(format('workouts-explorer/osm-park/v1:%s:%s:%s',
            segment.locality_relation_id,park.source_type,park.source_id))::uuid END AS park_id
    FROM path_segments segment
    JOIN LATERAL (
        SELECT candidate.*
        FROM park_areas candidate
        WHERE candidate.geom && segment.geom
          AND (candidate.park_kind='national_park' OR EXISTS (
              SELECT 1 FROM localities locality
              WHERE locality.relation_id=segment.locality_relation_id
                AND locality.admin_level=8
                AND NULLIF(btrim(locality.name),'') IS NOT NULL))
          AND ST_Covers(candidate.geom,ST_LineInterpolatePoint(segment.geom,0.5))
          AND ST_Length(ST_CollectionExtract(ST_Difference(segment.geom,candidate.geom),2)::geography)<=0.01
        ORDER BY candidate.type_priority,candidate.area_m2,
            CASE candidate.source_type WHEN 'relation' THEN 0 ELSE 1 END,candidate.source_id
        LIMIT 1
    ) park ON true
)
UPDATE path_segments segment SET tags=segment.tags||jsonb_build_object(
    'workouts:park_id',selected.park_id::text,
    'workouts:park_kind',selected.park_kind,
    'workouts:park_name',selected.name,
    'workouts:park_normalized_name',selected.normalized_name,
    'workouts:park_source_type',selected.source_type,
    'workouts:park_source_id',selected.source_id,
    'workouts:park_source_version',selected.version
)
FROM selected WHERE selected.segment_id=segment.segment_id;
\endif

\echo 'OSM progress: attributing unnamed segments to educational grounds'
WITH selected AS MATERIALIZED (
    SELECT segment.segment_id,education.source_type,education.source_id,education.version,
        education.name,education.normalized_name,education.education_kind,
        md5(format('workouts-explorer/osm-education/v1:%s:%s:%s',
            :'OSM_REGION_ID',education.source_type,education.source_id))::uuid education_id
    FROM path_segments segment
    JOIN LATERAL (
        SELECT candidate.* FROM education_areas candidate
        WHERE segment.normalized_name IS NULL
          AND candidate.geom && segment.geom
          AND ST_Covers(candidate.geom,ST_LineInterpolatePoint(segment.geom,0.5))
          AND ST_Length(ST_CollectionExtract(ST_Difference(segment.geom,candidate.geom),2)::geography)<=0.01
        ORDER BY candidate.area_m2,candidate.type_priority,
            CASE candidate.source_type WHEN 'relation' THEN 0 ELSE 1 END,candidate.source_id
        LIMIT 1
    ) education ON true
)
UPDATE path_segments segment SET tags=segment.tags||jsonb_build_object(
    'workouts:education_id',selected.education_id::text,
    'workouts:education_kind',selected.education_kind,
    'workouts:education_name',selected.name,
    'workouts:education_normalized_name',selected.normalized_name,
    'workouts:education_source_type',selected.source_type,
    'workouts:education_source_id',selected.source_id,
    'workouts:education_source_version',selected.version
)
FROM selected WHERE selected.segment_id=segment.segment_id;

\echo 'OSM progress: absorbing short path locality slivers'
DROP TABLE IF EXISTS attribution_scope_sliver_stats;
DROP TABLE IF EXISTS locality_scope_slivers;
CREATE UNLOGGED TABLE locality_scope_slivers AS
SELECT middle.segment_id,previous.locality_relation_id AS replacement_locality_relation_id
FROM path_segments middle
JOIN path_segments previous
  ON previous.source_way_id=middle.source_way_id
 AND previous.source_way_version=middle.source_way_version
 AND previous.start_node_index=middle.start_node_index
 AND previous.end_node_index=middle.end_node_index
 AND previous.boundary_piece=middle.boundary_piece-1
JOIN path_segments following
  ON following.source_way_id=middle.source_way_id
 AND following.source_way_version=middle.source_way_version
 AND following.start_node_index=middle.start_node_index
 AND following.end_node_index=middle.end_node_index
 AND following.boundary_piece=middle.boundary_piece+1
WHERE middle.locality_relation_id IS NOT NULL
  AND middle.broad_class<>'road'
  AND middle.length_m<=25
  AND previous.locality_relation_id IS NOT NULL
  AND following.locality_relation_id=previous.locality_relation_id
  AND middle.locality_relation_id<>previous.locality_relation_id
  AND EXISTS (SELECT 1 FROM localities municipality
      WHERE municipality.relation_id=middle.locality_relation_id AND municipality.admin_level=8)
  AND EXISTS (SELECT 1 FROM localities county
      WHERE county.relation_id=previous.locality_relation_id AND county.admin_level=6)
  AND previous.normalized_name IS NOT DISTINCT FROM middle.normalized_name
  AND following.normalized_name IS NOT DISTINCT FROM middle.normalized_name
  AND previous.broad_class=middle.broad_class
  AND following.broad_class=middle.broad_class
  AND NOT previous.tags ? 'workouts:park_id'
  AND NOT middle.tags ? 'workouts:park_id'
  AND NOT following.tags ? 'workouts:park_id';
ALTER TABLE locality_scope_slivers ADD PRIMARY KEY(segment_id);
CREATE UNLOGGED TABLE attribution_scope_sliver_stats AS
SELECT count(*)::bigint AS absorbed_segments,
    coalesce(sum(segment.length_m),0)::double precision AS absorbed_length_m
FROM locality_scope_slivers sliver JOIN path_segments segment USING(segment_id);
UPDATE path_segments segment
SET locality_relation_id=sliver.replacement_locality_relation_id
FROM locality_scope_slivers sliver
WHERE sliver.segment_id=segment.segment_id;
DROP TABLE locality_scope_slivers;

\echo 'OSM progress: deriving graph-connected attribution identities'

-- Final identities are connected components of physical segments. Incoming
-- logical IDs are deliberately ignored because they may already span disconnected
-- occurrences of one name. Class is part of the group, so roads and paths never
-- bridge each other's components.
DROP TABLE IF EXISTS attribution_identity_stats;
DROP TABLE IF EXISTS attribution_identity_components;
DROP TABLE IF EXISTS attribution_identity_branch_classes;
DROP TABLE IF EXISTS attribution_identity_branch_nodes;
DROP TABLE IF EXISTS attribution_identity_named_roads;
DROP TABLE IF EXISTS attribution_identity_edges;
DROP TABLE IF EXISTS attribution_identity_segments;

\echo 'OSM progress: staging connected segment edges (1/4)'

CREATE UNLOGGED TABLE attribution_identity_segments AS
SELECT segment_id,source_way_id,start_graph_node_id,end_graph_node_id,unnamed,broad_class,oneway,
    md5(format('workouts-explorer/osm-attribution-group/v2:%s:%s:%s:%s:name:%s:%s:class:%s:%s',
        length(scope_kind),scope_kind,length(scope_id),scope_id,length(name_key),name_key,
        length(attribution_class),attribution_class))::uuid AS group_id
FROM (
    SELECT segment_id,source_way_id,start_graph_node_id,end_graph_node_id,broad_class,geom,
        normalized_name IS NULL AS unnamed,
        COALESCE(tags->>'oneway','') IN ('yes','1','-1') AS oneway,
        CASE WHEN normalized_name IS NOT NULL THEN 'named'
             WHEN broad_class='road' THEN 'road' ELSE 'path' END AS attribution_class,
        CASE WHEN normalized_name IS NULL AND tags ? 'workouts:education_id' THEN 'education'
             WHEN broad_class<>'road' AND tags ? 'workouts:park_id' THEN 'park'
             WHEN locality_relation_id IS NOT NULL THEN 'locality'
             ELSE 'region' END AS scope_kind,
        CASE WHEN normalized_name IS NULL AND tags ? 'workouts:education_id' THEN tags->>'workouts:education_id'
             WHEN broad_class<>'road' AND tags ? 'workouts:park_id' THEN tags->>'workouts:park_id'
             WHEN locality_relation_id IS NOT NULL THEN locality_relation_id::text
             ELSE :'OSM_REGION_ID' END AS scope_id,
        CASE WHEN normalized_name IS NULL THEN 'U' ELSE 'N:'||normalized_name END AS name_key
    FROM path_segments
) scoped;
ALTER TABLE attribution_identity_segments ADD PRIMARY KEY(segment_id);
CREATE INDEX attribution_identity_segments_start_idx
ON attribution_identity_segments(group_id,start_graph_node_id,segment_id);
CREATE INDEX attribution_identity_segments_end_idx
ON attribution_identity_segments(group_id,end_graph_node_id,segment_id);
ANALYZE attribution_identity_segments;

CREATE UNLOGGED TABLE attribution_identity_edges (
    group_id uuid NOT NULL,
    connecting_node_id uuid NOT NULL,
    left_segment_id uuid NOT NULL,
    right_segment_id uuid NOT NULL,
    left_source_way_id bigint NOT NULL,
    right_source_way_id bigint NOT NULL,
    unnamed boolean NOT NULL,
    left_broad_class text NOT NULL,
    right_broad_class text NOT NULL,
    PRIMARY KEY(group_id,connecting_node_id,left_segment_id,right_segment_id),
    CHECK(left_segment_id < right_segment_id)
);

-- Hash joins can spill a full segment scan. Nested endpoint probes keep working
-- storage proportional to the deduplicated connected-segment graph.
SET enable_hashjoin = off;
SET enable_mergejoin = off;

INSERT INTO attribution_identity_edges
SELECT a.group_id,
    a.start_graph_node_id,least(a.segment_id,b.segment_id),greatest(a.segment_id,b.segment_id),
    CASE WHEN a.segment_id<b.segment_id THEN a.source_way_id ELSE b.source_way_id END,
    CASE WHEN a.segment_id<b.segment_id THEN b.source_way_id ELSE a.source_way_id END,a.unnamed,
    CASE WHEN a.segment_id<b.segment_id THEN a.broad_class ELSE b.broad_class END,
    CASE WHEN a.segment_id<b.segment_id THEN b.broad_class ELSE a.broad_class END
FROM attribution_identity_segments a
JOIN attribution_identity_segments b ON b.group_id=a.group_id AND b.start_graph_node_id=a.start_graph_node_id
WHERE a.segment_id<>b.segment_id
ON CONFLICT DO NOTHING;

\echo 'OSM progress: staging connected segment edges (2/4)'
INSERT INTO attribution_identity_edges
SELECT a.group_id,
    a.start_graph_node_id,least(a.segment_id,b.segment_id),greatest(a.segment_id,b.segment_id),
    CASE WHEN a.segment_id<b.segment_id THEN a.source_way_id ELSE b.source_way_id END,
    CASE WHEN a.segment_id<b.segment_id THEN b.source_way_id ELSE a.source_way_id END,a.unnamed,
    CASE WHEN a.segment_id<b.segment_id THEN a.broad_class ELSE b.broad_class END,
    CASE WHEN a.segment_id<b.segment_id THEN b.broad_class ELSE a.broad_class END
FROM attribution_identity_segments a
JOIN attribution_identity_segments b ON b.group_id=a.group_id AND b.end_graph_node_id=a.start_graph_node_id
WHERE a.segment_id<>b.segment_id
ON CONFLICT DO NOTHING;

\echo 'OSM progress: staging connected segment edges (3/4)'
INSERT INTO attribution_identity_edges
SELECT a.group_id,
    a.end_graph_node_id,least(a.segment_id,b.segment_id),greatest(a.segment_id,b.segment_id),
    CASE WHEN a.segment_id<b.segment_id THEN a.source_way_id ELSE b.source_way_id END,
    CASE WHEN a.segment_id<b.segment_id THEN b.source_way_id ELSE a.source_way_id END,a.unnamed,
    CASE WHEN a.segment_id<b.segment_id THEN a.broad_class ELSE b.broad_class END,
    CASE WHEN a.segment_id<b.segment_id THEN b.broad_class ELSE a.broad_class END
FROM attribution_identity_segments a
JOIN attribution_identity_segments b ON b.group_id=a.group_id AND b.start_graph_node_id=a.end_graph_node_id
WHERE a.segment_id<>b.segment_id
ON CONFLICT DO NOTHING;

\echo 'OSM progress: staging connected segment edges (4/4)'
INSERT INTO attribution_identity_edges
SELECT a.group_id,
    a.end_graph_node_id,least(a.segment_id,b.segment_id),greatest(a.segment_id,b.segment_id),
    CASE WHEN a.segment_id<b.segment_id THEN a.source_way_id ELSE b.source_way_id END,
    CASE WHEN a.segment_id<b.segment_id THEN b.source_way_id ELSE a.source_way_id END,a.unnamed,
    CASE WHEN a.segment_id<b.segment_id THEN a.broad_class ELSE b.broad_class END,
    CASE WHEN a.segment_id<b.segment_id THEN b.broad_class ELSE a.broad_class END
FROM attribution_identity_segments a
JOIN attribution_identity_segments b ON b.group_id=a.group_id AND b.end_graph_node_id=a.end_graph_node_id
WHERE a.segment_id<>b.segment_id
ON CONFLICT DO NOTHING;

RESET enable_hashjoin;
RESET enable_mergejoin;

\echo 'OSM progress: staging nearby named divided-road edges'
CREATE UNLOGGED TABLE attribution_identity_named_roads AS
SELECT identity.group_id,identity.segment_id,identity.source_way_id,identity.broad_class,identity.oneway,segment.geom
FROM attribution_identity_segments identity JOIN path_segments segment USING(segment_id)
WHERE identity.broad_class='road' AND NOT identity.unnamed;
ALTER TABLE attribution_identity_named_roads ADD PRIMARY KEY(group_id,segment_id);
CREATE INDEX attribution_identity_named_roads_geom_gist
ON attribution_identity_named_roads USING gist(geom);
ANALYZE attribution_identity_named_roads;
INSERT INTO attribution_identity_edges
SELECT DISTINCT ON (nearby.group_id,least(nearby.left_source_way_id,nearby.right_source_way_id),greatest(nearby.left_source_way_id,nearby.right_source_way_id))
    nearby.group_id,
    md5(format('workouts-explorer/osm-named-road-proximity/v1:%s:%s',nearby.left_segment_id,nearby.right_segment_id))::uuid,
    nearby.left_segment_id,nearby.right_segment_id,nearby.left_source_way_id,nearby.right_source_way_id,
    false,nearby.left_broad_class,nearby.right_broad_class
FROM (
    SELECT a.group_id,a.segment_id left_segment_id,candidate.segment_id right_segment_id,
        a.source_way_id left_source_way_id,candidate.source_way_id right_source_way_id,
        a.broad_class left_broad_class,candidate.broad_class right_broad_class,
        ST_Distance(a.geom::geography,candidate.geom::geography) distance_m
    FROM attribution_identity_named_roads a
    JOIN LATERAL (SELECT candidate.* FROM attribution_identity_named_roads candidate
    WHERE candidate.group_id=a.group_id
      AND candidate.segment_id>a.segment_id
      AND candidate.geom && ST_Expand(a.geom,0.0006)
      AND ST_DWithin(a.geom::geography,candidate.geom::geography,50)
      AND (ST_DWithin(a.geom::geography,candidate.geom::geography,15) OR (a.oneway AND candidate.oneway))
) candidate ON true
) nearby
ORDER BY nearby.group_id,least(nearby.left_source_way_id,nearby.right_source_way_id),
    greatest(nearby.left_source_way_id,nearby.right_source_way_id),nearby.distance_m,
    nearby.left_segment_id,nearby.right_segment_id
ON CONFLICT DO NOTHING;

\echo 'OSM progress: preserving branch boundaries between source ways'
CREATE UNLOGGED TABLE attribution_identity_branch_nodes AS
SELECT group_id,connecting_node_id
FROM (
    SELECT group_id,connecting_node_id,left_segment_id AS segment_id FROM attribution_identity_edges
    UNION ALL
    SELECT group_id,connecting_node_id,right_segment_id FROM attribution_identity_edges
) incidence
GROUP BY group_id,connecting_node_id
HAVING count(DISTINCT segment_id)>2;
CREATE UNIQUE INDEX attribution_identity_branch_nodes_key
ON attribution_identity_branch_nodes(group_id,connecting_node_id);

CREATE UNLOGGED TABLE attribution_identity_branch_classes AS
SELECT group_id,connecting_node_id,broad_class
FROM (
    SELECT group_id,connecting_node_id,left_segment_id AS segment_id,left_broad_class AS broad_class
    FROM attribution_identity_edges
    UNION ALL
    SELECT group_id,connecting_node_id,right_segment_id,right_broad_class
    FROM attribution_identity_edges
) incidence
GROUP BY group_id,connecting_node_id,broad_class
HAVING count(DISTINCT segment_id)=2;
CREATE UNIQUE INDEX attribution_identity_branch_classes_key
ON attribution_identity_branch_classes(group_id,connecting_node_id,broad_class);
DELETE FROM attribution_identity_edges edge
USING attribution_identity_branch_nodes branch
WHERE branch.group_id=edge.group_id
  AND branch.connecting_node_id=edge.connecting_node_id
  AND edge.unnamed
  AND edge.left_source_way_id<>edge.right_source_way_id
  AND NOT EXISTS (
      SELECT 1 FROM attribution_identity_branch_classes class_pair
      WHERE class_pair.group_id=edge.group_id
        AND class_pair.connecting_node_id=edge.connecting_node_id
        AND edge.left_broad_class=edge.right_broad_class
        AND class_pair.broad_class=edge.left_broad_class
  );

CREATE UNLOGGED TABLE attribution_identity_components (
    group_id uuid NOT NULL,
    segment_id uuid NOT NULL,
    parent_segment_id uuid NOT NULL,
    merged_logical_path_id uuid,
    PRIMARY KEY(group_id,segment_id)
);
INSERT INTO attribution_identity_components
SELECT group_id,left_segment_id,left_segment_id,NULL
FROM attribution_identity_edges ON CONFLICT DO NOTHING;
INSERT INTO attribution_identity_components
SELECT group_id,right_segment_id,right_segment_id,NULL
FROM attribution_identity_edges ON CONFLICT DO NOTHING;
CREATE INDEX attribution_identity_components_parent_idx
ON attribution_identity_components(group_id,parent_segment_id);
ANALYZE attribution_identity_edges;
ANALYZE attribution_identity_components;

SELECT format('OSM progress: propagating %s connected edges across %s physical segments',
    (SELECT count(*) FROM attribution_identity_edges),
    (SELECT count(*) FROM attribution_identity_components));

-- Hook component roots toward the minimum member ID, fully compressing parent
-- chains between hooks. This scans only segments that participate in an edge;
-- isolated segments receive their own deterministic identity during the rewrite.
DO $component_propagation$
DECLARE
    changed_rows bigint;
    hooked_rows bigint;
    iterations bigint := 0;
    maximum_iterations bigint;
BEGIN
    SELECT count(*) INTO maximum_iterations FROM attribution_identity_components;
    LOOP
        LOOP
            UPDATE attribution_identity_components node
            SET parent_segment_id=parent.parent_segment_id
            FROM attribution_identity_components parent
            WHERE parent.group_id=node.group_id
              AND parent.segment_id=node.parent_segment_id
              AND node.parent_segment_id<>parent.parent_segment_id;
            GET DIAGNOSTICS changed_rows = ROW_COUNT;
            RAISE NOTICE 'OSM progress: component compression iteration %, updated % rows',iterations+1,changed_rows;
            EXIT WHEN changed_rows=0;
        END LOOP;

        WITH hooks AS (
            SELECT edge.group_id,
                greatest(left_node.parent_segment_id,right_node.parent_segment_id) AS root_id,
                min(least(left_node.parent_segment_id,right_node.parent_segment_id)::text)::uuid AS parent_id
            FROM attribution_identity_edges edge
            JOIN attribution_identity_components left_node
              ON left_node.group_id=edge.group_id
             AND left_node.segment_id=edge.left_segment_id
            JOIN attribution_identity_components right_node
              ON right_node.group_id=edge.group_id
             AND right_node.segment_id=edge.right_segment_id
            WHERE left_node.parent_segment_id<>right_node.parent_segment_id
            GROUP BY edge.group_id,
                greatest(left_node.parent_segment_id,right_node.parent_segment_id)
        )
        UPDATE attribution_identity_components root
        SET parent_segment_id=hooks.parent_id
        FROM hooks
        WHERE root.group_id=hooks.group_id AND root.segment_id=hooks.root_id
          AND root.parent_segment_id=root.segment_id;
        GET DIAGNOSTICS hooked_rows = ROW_COUNT;
        RAISE NOTICE 'OSM progress: component hook iteration %, updated % roots',iterations+1,hooked_rows;
        EXIT WHEN hooked_rows=0;
        iterations := iterations+1;
        IF iterations>maximum_iterations THEN
            RAISE EXCEPTION 'attribution identity propagation did not converge after % iterations',maximum_iterations;
        END IF;
    END LOOP;

    LOOP
        UPDATE attribution_identity_components node
        SET parent_segment_id=parent.parent_segment_id
        FROM attribution_identity_components parent
        WHERE parent.group_id=node.group_id
          AND parent.segment_id=node.parent_segment_id
          AND node.parent_segment_id<>parent.parent_segment_id;
        GET DIAGNOSTICS changed_rows = ROW_COUNT;
        RAISE NOTICE 'OSM progress: final component compression updated % rows',changed_rows;
        EXIT WHEN changed_rows=0;
    END LOOP;
END
$component_propagation$;

UPDATE attribution_identity_components
SET merged_logical_path_id=md5(format(
    'workouts-explorer/osm-logical-path/v12:group:%s:member:%s',
    group_id,parent_segment_id))::uuid;

CREATE UNLOGGED TABLE attribution_identity_stats AS
SELECT (SELECT count(*) FROM attribution_identity_edges) AS logical_id_edges,
    count(*) AS affected_logical_ids,
    count(DISTINCT (group_id,parent_segment_id)) AS merged_components,
    0::bigint AS scope_rebased_segments,
    0::bigint AS scope_rebased_logical_ids,
    (SELECT count(*) FROM attribution_identity_edges edge
        JOIN attribution_identity_components left_component
          ON left_component.group_id=edge.group_id AND left_component.segment_id=edge.left_segment_id
        JOIN attribution_identity_components right_component
          ON right_component.group_id=edge.group_id AND right_component.segment_id=edge.right_segment_id
        WHERE left_component.merged_logical_path_id<>right_component.merged_logical_path_id
    ) AS remaining_connected_splits
FROM attribution_identity_components;

\echo 'OSM progress: rewriting segments with merged logical path identities'
CREATE UNLOGGED TABLE path_segments_rewritten (
    LIKE path_segments INCLUDING DEFAULTS INCLUDING GENERATED INCLUDING IDENTITY
        INCLUDING CONSTRAINTS INCLUDING STORAGE INCLUDING COMMENTS
);
INSERT INTO path_segments_rewritten (
    segment_id,source_way_id,source_way_version,derivation_version,
    start_node_index,end_node_index,boundary_piece,start_graph_node_id,end_graph_node_id,
    name,normalized_name,highway,broad_class,tags,motor_forward_allowed,
    motor_reverse_allowed,geom,locality_relation_id,logical_path_id,length_m
)
SELECT segment.segment_id,segment.source_way_id,segment.source_way_version,segment.derivation_version,
    segment.start_node_index,segment.end_node_index,segment.boundary_piece,
    segment.start_graph_node_id,segment.end_graph_node_id,segment.name,segment.normalized_name,
    segment.highway,segment.broad_class,segment.tags,segment.motor_forward_allowed,
    segment.motor_reverse_allowed,segment.geom,segment.locality_relation_id,
    coalesce(component.merged_logical_path_id,md5(format(
        'workouts-explorer/osm-logical-path/v12:group:%s:member:%s',
        identity.group_id,segment.segment_id))::uuid),segment.length_m
FROM path_segments segment
JOIN attribution_identity_segments identity USING(segment_id)
LEFT JOIN attribution_identity_components component
  ON component.group_id=identity.group_id AND component.segment_id=segment.segment_id;

DROP TABLE attribution_identity_segments;
DROP TABLE path_segments;
ALTER TABLE path_segments_rewritten RENAME TO path_segments;

-- An old ID can retain unaffected members in another scope, so aggregate every
-- final ID rather than trying to patch only newly merged logical-path rows.
\echo 'OSM progress: rebuilding logical path aggregates'
TRUNCATE logical_paths;
INSERT INTO logical_paths
SELECT logical_path_id,min(locality_relation_id),min(name),min(normalized_name),
    min(broad_class),count(*),sum(length_m)
FROM path_segments
GROUP BY logical_path_id;

DROP TABLE attribution_identity_edges;
DROP TABLE attribution_identity_components;
DROP TABLE attribution_identity_branch_classes;
DROP TABLE attribution_identity_branch_nodes;
DROP TABLE attribution_identity_named_roads;

ANALYZE path_segments;
ANALYZE logical_paths;
ANALYZE attribution_identity_stats;

\echo 'OSM progress: park attribution and connected identities complete'
