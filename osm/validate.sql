SELECT jsonb_build_object(
    'ways', (SELECT count(*) FROM :"OSM_BUILD_SCHEMA".ways),
    'waysWithVersion', (SELECT count(version) FROM :"OSM_BUILD_SCHEMA".ways),
    'waysWithTimestamp', (SELECT count(osm_timestamp) FROM :"OSM_BUILD_SCHEMA".ways),
    'waysWithNodeLineage', (SELECT count(node_ids) FROM :"OSM_BUILD_SCHEMA".ways),
    'invalidWays', (
        SELECT count(*) FROM :"OSM_BUILD_SCHEMA".ways
        WHERE geom IS NULL OR ST_IsEmpty(geom) OR NOT ST_IsValid(geom)
    ),
    'boundaryRelations', (SELECT count(*) FROM :"OSM_BUILD_SCHEMA".boundaries),
    'assembledBoundaries', (SELECT count(geom) FROM :"OSM_BUILD_SCHEMA".boundaries),
    'municipalLocalities', (SELECT count(*) FROM :"OSM_BUILD_SCHEMA".localities),
    'pathSegments', (SELECT count(*) FROM :"OSM_BUILD_SCHEMA".path_segments),
    'logicalPaths', (SELECT count(*) FROM :"OSM_BUILD_SCHEMA".logical_paths),
    'invalidPathSegments', (
        SELECT count(*) FROM :"OSM_BUILD_SCHEMA".path_segments
        WHERE ST_IsEmpty(geom) OR NOT ST_IsValid(geom) OR length_m <= 0
    ),
    'orphanPathSegments', (
        SELECT count(*) FROM :"OSM_BUILD_SCHEMA".path_segments AS segment
        LEFT JOIN :"OSM_BUILD_SCHEMA".logical_paths AS path USING (logical_path_id)
        WHERE path.logical_path_id IS NULL
    ),
    'materialLocalityResiduals', (
        SELECT count(*)
        FROM :"OSM_BUILD_SCHEMA".path_segments AS segment
        JOIN :"OSM_BUILD_SCHEMA".localities AS locality
          ON locality.relation_id = segment.locality_relation_id
        WHERE ST_Length(
            ST_CollectionExtract(ST_Difference(segment.geom, locality.geom), 2)::geography
        ) > 0.01
    ),
    'sourceLengthMeters', (SELECT sum(ST_Length(geom::geography)) FROM :"OSM_BUILD_SCHEMA".ways),
    'segmentLengthMeters', (SELECT sum(length_m) FROM :"OSM_BUILD_SCHEMA".path_segments),
    'schemaBytes', (
        SELECT sum(pg_total_relation_size(format('%I.%I', schemaname, tablename)::regclass))
        FROM pg_tables WHERE schemaname = :'OSM_BUILD_SCHEMA'
    )
);
