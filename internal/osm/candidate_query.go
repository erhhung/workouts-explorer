package osm

// ST_Collect retains every observation point (not a planar envelope). Geography
// distance to that collection is the minimum distance to its members, so using
// the maximum radius gives a superset of every individual DWithin search, even
// across the dateline and at the poles. The individual spheroid tests and ranking
// still run below. Bound the materialization BEFORE global preference checks;
// the extra row selects the exact baseline, never a truncated candidate prefix.
const matcherCandidatesBatchSQL = `
WITH allowed AS (
    SELECT region_id,generation_id FROM unnest($5::text[],$6::bigint[]) input(region_id,generation_id)
), observations AS MATERIALIZED (
    SELECT ordinal::integer-1 AS observation_index,
           ST_SetSRID(ST_MakePoint(longitude,latitude),4326) AS location,
           radius_m
    FROM unnest($1::double precision[],$2::double precision[],$3::double precision[])
         WITH ORDINALITY AS input(longitude,latitude,radius_m,ordinal)
), nearby AS MATERIALIZED (
    SELECT segment.*,segment.geom::geography AS search_geog
    FROM osm_canonical.path_segments segment
    WHERE ST_DWithin(segment.geom::geography,
              (SELECT ST_Collect(location)::geography FROM observations),
              (SELECT max(radius_m) FROM observations))
      AND (COALESCE(cardinality($5::text[]),0)=0 OR EXISTS (SELECT 1 FROM allowed
          WHERE allowed.region_id=segment.region_id AND allowed.generation_id=segment.generation_id))
    LIMIT 8193
), budget AS MATERIALIZED (
    SELECT count(*)<=8192 AS fits FROM nearby
), eligible AS MATERIALIZED (
    SELECT segment.* FROM nearby segment
    WHERE (SELECT fits FROM budget)
      AND NOT EXISTS (
          SELECT 1 FROM osm_canonical.ways preferred_way
          WHERE preferred_way.way_id=segment.source_way_id
            AND (COALESCE(cardinality($5::text[]),0)=0 OR EXISTS (SELECT 1 FROM allowed
                WHERE allowed.region_id=preferred_way.region_id AND allowed.generation_id=preferred_way.generation_id))
            AND (preferred_way.version,preferred_way.generation_id,preferred_way.region_id)
                > (segment.source_way_version,segment.generation_id,segment.region_id)
      )
      AND NOT EXISTS (
          SELECT 1 FROM osm_canonical.path_segments preferred_segment
          WHERE preferred_segment.segment_id=segment.segment_id
            AND (COALESCE(cardinality($5::text[]),0)=0 OR EXISTS (SELECT 1 FROM allowed
                WHERE allowed.region_id=preferred_segment.region_id AND allowed.generation_id=preferred_segment.generation_id))
            AND (preferred_segment.generation_id,preferred_segment.region_id)
                > (segment.generation_id,segment.region_id)
      )
)
SELECT batch.* FROM (
    SELECT observation.observation_index,candidate.segment_id,candidate.region_id,
           candidate.generation_id,candidate.source_way_id,candidate.source_way_version,
           candidate.derivation_version,candidate.start_graph_node_id,candidate.end_graph_node_id,
           candidate.logical_path_id,candidate.locality_relation_id,candidate.name,candidate.normalized_name,
           candidate.highway,candidate.broad_class,candidate.tags,
           candidate.motor_forward_allowed,candidate.motor_reverse_allowed,candidate.length_m,
           candidate.distance_m,candidate.fraction,ST_X(candidate.projected),ST_Y(candidate.projected),
           degrees(ST_Azimuth(
               ST_LineInterpolatePoint(candidate.geom,greatest(0,candidate.fraction-least(0.01,1/greatest(candidate.length_m,1)))),
               ST_LineInterpolatePoint(candidate.geom,least(1,candidate.fraction+least(0.01,1/greatest(candidate.length_m,1))))
            )) AS tangent_degrees,
           ST_X(ST_StartPoint(candidate.geom)),ST_Y(ST_StartPoint(candidate.geom)),
           ST_X(ST_EndPoint(candidate.geom)),ST_Y(ST_EndPoint(candidate.geom))
    FROM observations observation
    CROSS JOIN LATERAL (
        SELECT located.*,ST_LineInterpolatePoint(located.geom,located.fraction) AS projected
        FROM (
            SELECT segment.*,
                   ST_Distance(segment.search_geog,observation.location::geography) AS distance_m,
                   ST_LineLocatePoint(segment.geom,observation.location) AS fraction
            FROM eligible segment
            WHERE ST_DWithin(segment.search_geog,observation.location::geography,observation.radius_m)
            ORDER BY distance_m,segment.segment_id,segment.generation_id DESC,segment.region_id DESC
            LIMIT $4
        ) located
    ) candidate
) batch WHERE (SELECT fits FROM budget)
UNION ALL
SELECT baseline.* FROM (` + matcherCandidatesSQL + `
) baseline WHERE NOT (SELECT fits FROM budget)
ORDER BY 1,20,2,4 DESC,3 DESC`
