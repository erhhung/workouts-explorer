SET search_path TO :"OSM_BUILD_SCHEMA", public;

TRUNCATE logical_paths;

INSERT INTO logical_paths
SELECT logical_path_id,
    min(locality_relation_id),
    min(name),
    min(normalized_name),
    min(broad_class),
    count(*),
    sum(length_m)
FROM path_segments
GROUP BY logical_path_id;

ANALYZE path_segments;

ANALYZE logical_paths;

DROP TABLE locality_clip_replacements;

DROP TABLE locality_clip_candidates;
