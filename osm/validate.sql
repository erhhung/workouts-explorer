\set OSM_WAYS_LEAF 'ways_g' :OSM_GENERATION_ID
\set OSM_LOCALITIES_LEAF 'localities_g' :OSM_GENERATION_ID
\set OSM_SEGMENTS_LEAF 'path_segments_g' :OSM_GENERATION_ID

\set QUIET 1
SET max_parallel_workers_per_gather = 0;
SET enable_hashjoin = off;
SET enable_mergejoin = off;
\set QUIET 0

SELECT jsonb_build_object(
    'partitionPrepared', true,
    'preparedRegionId', :'OSM_REGION_ID',
    'preparedGenerationId', :'OSM_GENERATION_ID'::bigint,
    'importerVersion', :'OSM_IMPORTER_VERSION'::integer,
    'derivationVersion', :'OSM_DERIVATION_VERSION'::integer,
    'provenanceMismatches', (
        SELECT count(*) FROM (
            SELECT region_id,generation_id FROM :"OSM_BUILD_SCHEMA".:"OSM_WAYS_LEAF"
            UNION ALL SELECT region_id,generation_id FROM :"OSM_BUILD_SCHEMA".:"OSM_LOCALITIES_LEAF"
            UNION ALL SELECT region_id,generation_id FROM :"OSM_BUILD_SCHEMA".:"OSM_SEGMENTS_LEAF"
        ) prepared
        WHERE region_id<>:'OSM_REGION_ID' OR generation_id<>:'OSM_GENERATION_ID'::bigint
    ),
    'ways', (SELECT count(*) FROM :"OSM_BUILD_SCHEMA".:"OSM_WAYS_LEAF"),
    'waysWithVersion', (SELECT count(version) FROM :"OSM_BUILD_SCHEMA".:"OSM_WAYS_LEAF"),
    'waysWithTimestamp', (SELECT count(osm_timestamp) FROM :"OSM_BUILD_SCHEMA".:"OSM_WAYS_LEAF"),
    'waysWithNodeLineage', (SELECT count(node_ids) FROM :"OSM_BUILD_SCHEMA".:"OSM_WAYS_LEAF"),
    'sourceVersionMismatches', (
        SELECT count(*) FROM :"OSM_BUILD_SCHEMA".:"OSM_SEGMENTS_LEAF" AS segment
        LEFT JOIN :"OSM_BUILD_SCHEMA".:"OSM_WAYS_LEAF" AS way
          ON way.region_id=segment.region_id AND way.generation_id=segment.generation_id
         AND way.way_id=segment.source_way_id AND way.version=segment.source_way_version
        WHERE way.way_id IS NULL OR segment.derivation_version <> :'OSM_DERIVATION_VERSION'::integer
    ),
    'invalidWays', (
        SELECT count(*) FROM :"OSM_BUILD_SCHEMA".:"OSM_WAYS_LEAF"
        WHERE geom IS NULL OR ST_IsEmpty(geom) OR NOT ST_IsValid(geom)
    ),
    'boundaryRelations', (SELECT count(*) FROM :"OSM_BUILD_SCHEMA".boundaries),
    'assembledBoundaries', (SELECT count(geom) FROM :"OSM_BUILD_SCHEMA".boundaries),
    'municipalLocalities', (SELECT count(*) FROM :"OSM_BUILD_SCHEMA".:"OSM_LOCALITIES_LEAF"),
    'pathSegments', (SELECT count(*) FROM :"OSM_BUILD_SCHEMA".:"OSM_SEGMENTS_LEAF"),
    'logicalPaths', (SELECT count(*) FROM :"OSM_BUILD_SCHEMA".logical_paths),
    'invalidPathSegments', (
        SELECT count(*) FROM :"OSM_BUILD_SCHEMA".:"OSM_SEGMENTS_LEAF"
        WHERE ST_IsEmpty(geom) OR NOT ST_IsValid(geom) OR length_m <= 0
    ),
    'orphanPathSegments', (
        SELECT count(*) FROM :"OSM_BUILD_SCHEMA".:"OSM_SEGMENTS_LEAF" AS segment
        LEFT JOIN :"OSM_BUILD_SCHEMA".logical_paths AS path USING (logical_path_id)
        WHERE path.logical_path_id IS NULL
    ),
    'logicalPathMismatches', (
        SELECT count(*) FROM (
            SELECT logical_path_id,min(locality_relation_id) locality_relation_id,min(name) name,
                min(normalized_name) normalized_name,min(broad_class) broad_class,count(*) member_segment_count,
                sum(length_m) member_length_m
            FROM :"OSM_BUILD_SCHEMA".:"OSM_SEGMENTS_LEAF" GROUP BY logical_path_id
        ) derived
        FULL JOIN :"OSM_BUILD_SCHEMA".logical_paths stored USING (logical_path_id)
        WHERE derived.logical_path_id IS NULL OR stored.logical_path_id IS NULL
           OR derived.locality_relation_id IS DISTINCT FROM stored.locality_relation_id
           OR derived.name IS DISTINCT FROM stored.name
           OR derived.normalized_name IS DISTINCT FROM stored.normalized_name
           OR derived.broad_class IS DISTINCT FROM stored.broad_class
           OR derived.member_segment_count <> stored.member_segment_count
           OR abs(derived.member_length_m-stored.member_length_m) > 0.01
    ),
    'missingEndpointIndexes', (
        SELECT count(*)
        FROM unnest(ARRAY[
            'path_segments_start_graph_node_g' || :'OSM_GENERATION_ID',
            'path_segments_end_graph_node_g' || :'OSM_GENERATION_ID'
        ]) required(index_name)
        WHERE NOT EXISTS (
            SELECT 1 FROM pg_index
            WHERE indexrelid=to_regclass(format('%I.%I',:'OSM_BUILD_SCHEMA',required.index_name))
              AND indisvalid AND indisready
        )
    ),
    'materialLocalityResiduals', (
        SELECT count(*)
        FROM :"OSM_BUILD_SCHEMA".:"OSM_SEGMENTS_LEAF" AS segment
        JOIN :"OSM_BUILD_SCHEMA".:"OSM_LOCALITIES_LEAF" AS locality
          ON locality.relation_id = segment.locality_relation_id
        WHERE ST_Length(
            ST_CollectionExtract(ST_Difference(segment.geom, locality.geom), 2)::geography
        ) > 0.01
    ),
    'qualifyingParkAreas', (SELECT count(*) FROM :"OSM_BUILD_SCHEMA".park_areas),
    'qualifyingNationalParkAreas', (
        SELECT count(*) FROM :"OSM_BUILD_SCHEMA".park_areas WHERE park_kind='national_park'
    ),
    'invalidNationalParkAreas', (
        SELECT count(*) FROM :"OSM_BUILD_SCHEMA".park_areas
        WHERE park_kind='national_park' AND (name IS NULL OR area_m2 NOT BETWEEN 1000000 AND 100000000000
          OR geom IS NULL OR ST_IsEmpty(geom) OR NOT ST_IsValid(geom))
    ),
    'parkAttributedSegments', (
        SELECT count(*) FROM :"OSM_BUILD_SCHEMA".:"OSM_SEGMENTS_LEAF" WHERE tags ? 'workouts:park_id'
    ),
    'nationalParkAttributedSegments', (
        SELECT count(*) FROM :"OSM_BUILD_SCHEMA".:"OSM_SEGMENTS_LEAF"
        WHERE tags->>'workouts:park_kind'='national_park'
    ),
    'qualifyingEducationAreas', (SELECT count(*) FROM :"OSM_BUILD_SCHEMA".education_areas),
    'educationAttributedSegments', (
        SELECT count(*) FROM :"OSM_BUILD_SCHEMA".:"OSM_SEGMENTS_LEAF" WHERE tags ? 'workouts:education_id'
    ),
    'invalidEducationAttributions', (
        SELECT count(*) FROM :"OSM_BUILD_SCHEMA".:"OSM_SEGMENTS_LEAF"
        WHERE tags ? 'workouts:education_id' AND (
            normalized_name IS NOT NULL OR
            COALESCE(tags->>'workouts:education_kind','') NOT IN ('school','college','university','education') OR
            NULLIF(tags->>'workouts:education_name','') IS NULL OR
            NULLIF(tags->>'workouts:education_source_type','') IS NULL OR
            NULLIF(tags->>'workouts:education_source_id','') IS NULL)
    ),
    'materialEducationResiduals', (
        SELECT count(*) FROM :"OSM_BUILD_SCHEMA".:"OSM_SEGMENTS_LEAF" segment
        JOIN :"OSM_BUILD_SCHEMA".education_areas education
          ON education.source_type=segment.tags->>'workouts:education_source_type'
         AND education.source_id=(segment.tags->>'workouts:education_source_id')::bigint
        WHERE ST_Length(ST_CollectionExtract(ST_Difference(segment.geom,education.geom),2)::geography)>0.01
    ),
    'attributionIdentityLogicalIdEdges', (
        SELECT logical_id_edges FROM :"OSM_BUILD_SCHEMA".attribution_identity_stats
    ),
    'attributionIdentityAffectedLogicalIds', (
        SELECT affected_logical_ids FROM :"OSM_BUILD_SCHEMA".attribution_identity_stats
    ),
    'attributionIdentityMergedComponents', (
        SELECT merged_components FROM :"OSM_BUILD_SCHEMA".attribution_identity_stats
    ),
    'attributionScopeRebasedSegments', (
        SELECT scope_rebased_segments FROM :"OSM_BUILD_SCHEMA".attribution_identity_stats
    ),
    'attributionScopeRebasedLogicalIds', (
        SELECT scope_rebased_logical_ids FROM :"OSM_BUILD_SCHEMA".attribution_identity_stats
    ),
    'localityScopeSliversAbsorbed', (
        SELECT absorbed_segments FROM :"OSM_BUILD_SCHEMA".attribution_scope_sliver_stats
    ),
    'localityScopeSliverLengthMeters', (
        SELECT absorbed_length_m FROM :"OSM_BUILD_SCHEMA".attribution_scope_sliver_stats
    ),
    'remainingConnectedAttributionSplits', (
        SELECT remaining_connected_splits FROM :"OSM_BUILD_SCHEMA".attribution_identity_stats
    ),
    'invalidParkAttributions', (
        SELECT count(*) FROM :"OSM_BUILD_SCHEMA".:"OSM_SEGMENTS_LEAF"
        WHERE tags ? 'workouts:park_id' AND (
            COALESCE(tags->>'workouts:park_kind','') NOT IN ('local_park','nature_reserve','protected_area','national_park') OR
            (locality_relation_id IS NULL AND tags->>'workouts:park_kind'<>'national_park') OR
            NULLIF(tags->>'workouts:park_name','') IS NULL OR
            NULLIF(tags->>'workouts:park_source_type','') IS NULL OR NULLIF(tags->>'workouts:park_source_id','') IS NULL)
    ),
    'materialParkResiduals', (
        SELECT count(*) FROM :"OSM_BUILD_SCHEMA".:"OSM_SEGMENTS_LEAF" segment
        JOIN :"OSM_BUILD_SCHEMA".park_areas park
          ON park.source_type=segment.tags->>'workouts:park_source_type'
         AND park.source_id=(segment.tags->>'workouts:park_source_id')::bigint
        WHERE ST_Length(ST_CollectionExtract(ST_Difference(segment.geom,park.geom),2)::geography)>0.01
    ),
    'sourceLengthMeters', (SELECT sum(ST_Length(geom::geography)) FROM :"OSM_BUILD_SCHEMA".:"OSM_WAYS_LEAF"),
    'segmentLengthMeters', (SELECT sum(length_m) FROM :"OSM_BUILD_SCHEMA".:"OSM_SEGMENTS_LEAF"),
    'schemaBytes', (
        SELECT sum(pg_total_relation_size(format('%I.%I', schemaname, tablename)::regclass))
        FROM pg_tables WHERE schemaname = :'OSM_BUILD_SCHEMA'
    )
);

\set QUIET 1
RESET max_parallel_workers_per_gather;
RESET enable_hashjoin;
RESET enable_mergejoin;
\set QUIET 0
