package osm

// Frozen before the candidate batch optimization. Keep this complete query
// independent of production query construction: it is the equivalence oracle.
const candidateOriginalSQL = `
WITH allowed AS (
    SELECT region_id,generation_id FROM unnest($5::text[],$6::bigint[]) input(region_id,generation_id)
), observations AS (
    SELECT ordinal::integer-1 AS observation_index,
           ST_SetSRID(ST_MakePoint(longitude,latitude),4326) AS location,
           radius_m
    FROM unnest($1::double precision[],$2::double precision[],$3::double precision[])
         WITH ORDINALITY AS input(longitude,latitude,radius_m,ordinal)
)
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
               ST_Distance(segment.geom::geography,observation.location::geography) AS distance_m,
               ST_LineLocatePoint(segment.geom,observation.location) AS fraction
        FROM osm_canonical.path_segments segment
        WHERE ST_DWithin(segment.geom::geography,observation.location::geography,observation.radius_m)
          AND (COALESCE(cardinality($5::text[]),0)=0 OR EXISTS (SELECT 1 FROM allowed
              WHERE allowed.region_id=segment.region_id AND allowed.generation_id=segment.generation_id))
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
        ORDER BY distance_m,segment.segment_id,segment.generation_id DESC,segment.region_id DESC
        LIMIT $4
    ) located
) candidate
ORDER BY observation.observation_index,candidate.distance_m,candidate.segment_id,
         candidate.generation_id DESC,candidate.region_id DESC`
