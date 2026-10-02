\o /dev/null
SET client_min_messages = warning;

SELECT set_config('workouts_explorer.osm_region_id', :'OSM_REGION_ID', false);
SELECT set_config('workouts_explorer.osm_generation_id', :'OSM_GENERATION_ID', false);
SELECT set_config('workouts_explorer.osm_build_schema', :'OSM_BUILD_SCHEMA', false);
SELECT set_config('workouts_explorer.osm_importer_version', :'OSM_IMPORTER_VERSION', false);
SELECT set_config('workouts_explorer.osm_derivation_version', :'OSM_DERIVATION_VERSION', false);

SET search_path TO :"OSM_BUILD_SCHEMA", public;
SET max_parallel_workers_per_gather = 0;
SET enable_hashjoin = off;
SET enable_mergejoin = off;

CREATE TABLE IF NOT EXISTS validation_metrics (
    metric text PRIMARY KEY,
    value jsonb NOT NULL
);

CREATE OR REPLACE PROCEDURE validate_candidate_batch(target_generation bigint)
LANGUAGE plpgsql
AS $procedure$
DECLARE
    phase text;
    cursor_id uuid;
    next_cursor uuid;
    expected_batch bigint;
    processed bigint;
    batch_rows bigint;
    metric_count bigint;
    batch_index bigint;
    batch_total bigint;
    phase_rows bigint;
    cursor_rows bigint;
    report jsonb;
    build_schema name := current_setting('workouts_explorer.osm_build_schema')::name;
    target_region_id text := current_setting('workouts_explorer.osm_region_id');
    logical_batch_size constant integer := 50000;
    residual_batch_size constant integer := 100000;
BEGIN
    IF target_generation <> current_setting('workouts_explorer.osm_generation_id')::bigint
       OR build_schema::text <> 'osm_build_' || target_generation THEN
        RAISE EXCEPTION 'validation generation/build schema mismatch';
    END IF;

    SELECT coalesce(cursor->>'phase', 'init'),
        NULLIF(cursor->>'last_id', '')::uuid,
        batch_count,
        rows_processed,
        (cursor->>'batch_index')::bigint,
        (cursor->>'batch_total')::bigint
    INTO phase, cursor_id, expected_batch, processed, batch_index, batch_total
    FROM osm_catalog.generation_stages
    WHERE generation_id = target_generation AND stage = 'validate'
    FOR UPDATE;

    IF phase IN ('logical-paths', 'locality-residuals') AND (batch_index IS NULL OR batch_total IS NULL) THEN
        IF phase = 'logical-paths' THEN
            SELECT count(*), count(*) FILTER (WHERE cursor_id IS NOT NULL AND logical_path_id <= cursor_id)
            INTO phase_rows, cursor_rows
            FROM logical_paths;
            batch_index := (cursor_rows + logical_batch_size - 1) / logical_batch_size + 1;
            batch_total := (phase_rows + logical_batch_size - 1) / logical_batch_size + 1;
        ELSE
            SELECT count(*), count(*) FILTER (WHERE cursor_id IS NOT NULL AND segment_id <= cursor_id)
            INTO phase_rows, cursor_rows
            FROM validation_segments;
            batch_index := (cursor_rows + residual_batch_size - 1) / residual_batch_size + 1;
            batch_total := (phase_rows + residual_batch_size - 1) / residual_batch_size + 1;
        END IF;
    END IF;

    IF phase = 'init' THEN
        TRUNCATE validation_metrics;
        DROP VIEW IF EXISTS validation_ways;
        DROP VIEW IF EXISTS validation_localities;
        DROP VIEW IF EXISTS validation_segments;
        EXECUTE format('CREATE VIEW validation_ways AS SELECT * FROM %I.%I', build_schema, 'ways_g' || target_generation);
        EXECUTE format('CREATE VIEW validation_localities AS SELECT * FROM %I.%I', build_schema, 'localities_g' || target_generation);
        EXECUTE format('CREATE VIEW validation_segments AS SELECT * FROM %I.%I', build_schema, 'path_segments_g' || target_generation);

        INSERT INTO validation_metrics VALUES
            ('partitionPrepared', 'true'),
            ('preparedRegionId', to_jsonb(target_region_id)),
            ('preparedGenerationId', to_jsonb(target_generation)),
            ('importerVersion', to_jsonb(current_setting('workouts_explorer.osm_importer_version')::integer)),
            ('derivationVersion', to_jsonb(current_setting('workouts_explorer.osm_derivation_version')::integer)),
            ('ways', to_jsonb((SELECT count(*) FROM validation_ways))),
            ('municipalLocalities', to_jsonb((SELECT count(*) FROM validation_localities))),
            ('pathSegments', to_jsonb((SELECT count(*) FROM validation_segments))),
            ('logicalPaths', to_jsonb((SELECT count(*) FROM logical_paths))),
            ('provenanceMismatches', to_jsonb((
                SELECT count(*) FROM (
                    SELECT region_id, generation_id FROM validation_ways
                    UNION ALL SELECT region_id, generation_id FROM validation_localities
                    UNION ALL SELECT region_id, generation_id FROM validation_segments
                ) AS prepared
                WHERE prepared.region_id <> target_region_id OR prepared.generation_id <> target_generation
            ))),
            ('missingEndpointIndexes', to_jsonb((
                SELECT count(*) FROM unnest(ARRAY[
                    'path_segments_start_graph_node_g' || target_generation,
                    'path_segments_end_graph_node_g' || target_generation
                ]) AS required(index_name)
                WHERE NOT EXISTS (
                    SELECT 1 FROM pg_index
                    WHERE indexrelid = to_regclass(format('%I.%I', build_schema, required.index_name))
                      AND indisvalid AND indisready
                )
            ))),
            ('qualifyingParkAreas', to_jsonb((SELECT count(*) FROM park_areas))),
            ('qualifyingNationalParkAreas', to_jsonb((SELECT count(*) FROM park_areas WHERE park_kind = 'national_park'))),
            ('qualifyingStateParkAreas', to_jsonb((SELECT count(*) FROM park_areas WHERE park_kind = 'state_park'))),
            ('parkAttributedSegments', to_jsonb((SELECT count(*) FROM validation_segments WHERE tags ? 'workouts:park_id'))),
            ('nationalParkAttributedSegments', to_jsonb((SELECT count(*) FROM validation_segments WHERE tags->>'workouts:park_kind' = 'national_park'))),
            ('stateParkAttributedSegments', to_jsonb((SELECT count(*) FROM validation_segments WHERE tags->>'workouts:park_kind' = 'state_park'))),
            ('qualifyingEducationAreas', to_jsonb((SELECT count(*) FROM education_areas))),
            ('educationAttributedSegments', to_jsonb((SELECT count(*) FROM validation_segments WHERE tags ? 'workouts:education_id'))),
            ('attributionIdentityLogicalIdEdges', (SELECT to_jsonb(logical_id_edges) FROM attribution_identity_stats)),
            ('attributionIdentityAffectedLogicalIds', (SELECT to_jsonb(affected_logical_ids) FROM attribution_identity_stats)),
            ('attributionIdentityMergedComponents', (SELECT to_jsonb(merged_components) FROM attribution_identity_stats)),
            ('attributionScopeRebasedSegments', (SELECT to_jsonb(scope_rebased_segments) FROM attribution_identity_stats)),
            ('attributionScopeRebasedLogicalIds', (SELECT to_jsonb(scope_rebased_logical_ids) FROM attribution_identity_stats)),
            ('remainingConnectedAttributionSplits', (SELECT to_jsonb(remaining_connected_splits) FROM attribution_identity_stats)),
            ('localityScopeSliversAbsorbed', (SELECT to_jsonb(absorbed_segments) FROM attribution_scope_sliver_stats)),
            ('localityScopeSliverLengthMeters', (SELECT to_jsonb(absorbed_length_m) FROM attribution_scope_sliver_stats)),
            ('logicalPathMismatches', '0'),
            ('materialLocalityResiduals', '0');
        PERFORM osm_catalog.checkpoint_generation_stage(target_generation, 'validate', expected_batch,
            jsonb_build_object('version', 1, 'phase', 'ways'), processed);
        RETURN;
    END IF;

    IF phase = 'ways' THEN
        INSERT INTO validation_metrics VALUES
            ('waysWithVersion', to_jsonb((SELECT count(version) FROM validation_ways))),
            ('waysWithTimestamp', to_jsonb((SELECT count(osm_timestamp) FROM validation_ways))),
            ('waysWithNodeLineage', to_jsonb((SELECT count(node_ids) FROM validation_ways))),
            ('invalidWays', to_jsonb((SELECT count(*) FROM validation_ways WHERE geom IS NULL OR ST_IsEmpty(geom) OR NOT ST_IsValid(geom)))),
            ('sourceLengthMeters', to_jsonb((SELECT sum(ST_Length(geom::geography)) FROM validation_ways)));
        PERFORM osm_catalog.checkpoint_generation_stage(target_generation, 'validate', expected_batch,
            jsonb_build_object('version', 1, 'phase', 'segments'), processed);
        RETURN;
    END IF;

    IF phase = 'segments' THEN
        INSERT INTO validation_metrics VALUES
            ('sourceVersionMismatches', to_jsonb((
                SELECT count(*) FROM validation_segments AS segment
                LEFT JOIN validation_ways AS way
                  ON way.region_id = segment.region_id AND way.generation_id = segment.generation_id
                 AND way.way_id = segment.source_way_id AND way.version = segment.source_way_version
                WHERE way.way_id IS NULL OR segment.derivation_version <> current_setting('workouts_explorer.osm_derivation_version')::integer
            ))),
            ('invalidPathSegments', to_jsonb((SELECT count(*) FROM validation_segments WHERE ST_IsEmpty(geom) OR NOT ST_IsValid(geom) OR length_m <= 0))),
            ('orphanPathSegments', to_jsonb((
                SELECT count(*) FROM validation_segments AS segment
                LEFT JOIN logical_paths AS path USING (logical_path_id)
                WHERE path.logical_path_id IS NULL
            ))),
            ('segmentLengthMeters', to_jsonb((SELECT sum(length_m) FROM validation_segments)));
        SELECT count(*) INTO phase_rows FROM logical_paths;
        PERFORM osm_catalog.checkpoint_generation_stage(target_generation, 'validate', expected_batch,
            jsonb_build_object('version', 1, 'phase', 'logical-paths', 'last_id', NULL, 'batch_size', logical_batch_size,
                'batch_index', 1, 'batch_total', (phase_rows + logical_batch_size - 1) / logical_batch_size + 1), processed);
        RETURN;
    END IF;

    IF phase = 'logical-paths' THEN
        CREATE TEMP TABLE validation_logical_keys ON COMMIT DROP AS
        SELECT logical_path_id
        FROM logical_paths
        WHERE cursor_id IS NULL OR logical_path_id > cursor_id
        ORDER BY logical_path_id
        LIMIT logical_batch_size;
        GET DIAGNOSTICS batch_rows = ROW_COUNT;
        IF batch_rows = 0 THEN
            SELECT count(*) INTO phase_rows FROM validation_segments;
            PERFORM osm_catalog.checkpoint_generation_stage(target_generation, 'validate', expected_batch,
                jsonb_build_object('version', 1, 'phase', 'locality-residuals', 'last_id', NULL, 'batch_size', residual_batch_size,
                    'batch_index', 1, 'batch_total', (phase_rows + residual_batch_size - 1) / residual_batch_size + 1), processed);
            RETURN;
        END IF;
        SELECT logical_path_id INTO next_cursor FROM validation_logical_keys ORDER BY logical_path_id DESC LIMIT 1;
        SELECT count(*) INTO metric_count
        FROM validation_logical_keys AS key
        JOIN logical_paths AS stored USING (logical_path_id)
        LEFT JOIN LATERAL (
            SELECT min(locality_relation_id) AS locality_relation_id,
                min(name) AS name,
                min(normalized_name) AS normalized_name,
                min(broad_class) AS broad_class,
                count(*) AS member_segment_count,
                sum(length_m) AS member_length_m
            FROM validation_segments
            WHERE logical_path_id = key.logical_path_id
        ) AS derived ON true
        WHERE derived.member_segment_count = 0
           OR derived.locality_relation_id IS DISTINCT FROM stored.locality_relation_id
           OR derived.name IS DISTINCT FROM stored.name
           OR derived.normalized_name IS DISTINCT FROM stored.normalized_name
           OR derived.broad_class IS DISTINCT FROM stored.broad_class
           OR derived.member_segment_count <> stored.member_segment_count
           OR abs(derived.member_length_m - stored.member_length_m) > 0.01;
        UPDATE validation_metrics
        SET value = to_jsonb((value::text)::bigint + metric_count)
        WHERE metric = 'logicalPathMismatches';
        PERFORM osm_catalog.checkpoint_generation_stage(target_generation, 'validate', expected_batch,
            jsonb_build_object('version', 1, 'phase', phase, 'last_id', next_cursor, 'batch_size', logical_batch_size,
                'batch_index', batch_index + 1, 'batch_total', batch_total), processed + batch_rows);
        RETURN;
    END IF;

    IF phase = 'locality-residuals' THEN
        CREATE TEMP TABLE validation_segment_keys ON COMMIT DROP AS
        SELECT segment_id
        FROM validation_segments
        WHERE cursor_id IS NULL OR segment_id > cursor_id
        ORDER BY segment_id
        LIMIT residual_batch_size;
        GET DIAGNOSTICS batch_rows = ROW_COUNT;
        IF batch_rows = 0 THEN
            PERFORM osm_catalog.checkpoint_generation_stage(target_generation, 'validate', expected_batch,
                jsonb_build_object('version', 1, 'phase', 'parks'), processed);
            RETURN;
        END IF;
        SELECT segment_id INTO next_cursor FROM validation_segment_keys ORDER BY segment_id DESC LIMIT 1;
        SELECT count(*) INTO metric_count
        FROM validation_segment_keys AS key
        JOIN validation_segments AS segment USING (segment_id)
        JOIN validation_localities AS locality ON locality.relation_id = segment.locality_relation_id
        WHERE ST_Length(ST_CollectionExtract(ST_Difference(segment.geom, locality.geom), 2)::geography) > 0.01;
        UPDATE validation_metrics
        SET value = to_jsonb((value::text)::bigint + metric_count)
        WHERE metric = 'materialLocalityResiduals';
        PERFORM osm_catalog.checkpoint_generation_stage(target_generation, 'validate', expected_batch,
            jsonb_build_object('version', 1, 'phase', phase, 'last_id', next_cursor, 'batch_size', residual_batch_size,
                'batch_index', batch_index + 1, 'batch_total', batch_total), processed + batch_rows);
        RETURN;
    END IF;

    IF phase = 'parks' THEN
        INSERT INTO validation_metrics VALUES
            ('invalidNationalParkAreas', to_jsonb((SELECT count(*) FROM park_areas WHERE park_kind='national_park' AND (name IS NULL OR area_m2 NOT BETWEEN 1000000 AND 100000000000 OR geom IS NULL OR ST_IsEmpty(geom) OR NOT ST_IsValid(geom))))),
            ('invalidStateParkAreas', to_jsonb((SELECT count(*) FROM park_areas WHERE park_kind='state_park' AND (name IS NULL OR area_m2 NOT BETWEEN 1000 AND 100000000000 OR geom IS NULL OR ST_IsEmpty(geom) OR NOT ST_IsValid(geom))))),
            ('invalidParkAttributions', to_jsonb((SELECT count(*) FROM validation_segments WHERE tags ? 'workouts:park_id' AND (coalesce(tags->>'workouts:park_kind','') NOT IN ('local_park','nature_reserve','protected_area','state_park','national_park') OR (locality_relation_id IS NULL AND tags->>'workouts:park_kind' NOT IN ('state_park','national_park')) OR NULLIF(tags->>'workouts:park_name','') IS NULL OR NULLIF(tags->>'workouts:park_source_type','') IS NULL OR NULLIF(tags->>'workouts:park_source_id','') IS NULL)))),
            ('materialParkResiduals', to_jsonb((SELECT count(*) FROM validation_segments AS segment JOIN park_areas AS park ON park.source_type=segment.tags->>'workouts:park_source_type' AND park.source_id=(segment.tags->>'workouts:park_source_id')::bigint WHERE ST_Length(ST_CollectionExtract(ST_Difference(segment.geom,park.geom),2)::geography)>0.01)));
        PERFORM osm_catalog.checkpoint_generation_stage(target_generation, 'validate', expected_batch,
            jsonb_build_object('version', 1, 'phase', 'education'), processed);
        RETURN;
    END IF;

    IF phase = 'education' THEN
        INSERT INTO validation_metrics VALUES
            ('invalidEducationAttributions', to_jsonb((SELECT count(*) FROM validation_segments WHERE tags ? 'workouts:education_id' AND (normalized_name IS NOT NULL OR coalesce(tags->>'workouts:education_kind','') NOT IN ('school','college','university','education') OR NULLIF(tags->>'workouts:education_name','') IS NULL OR NULLIF(tags->>'workouts:education_source_type','') IS NULL OR NULLIF(tags->>'workouts:education_source_id','') IS NULL)))),
            ('materialEducationResiduals', to_jsonb((SELECT count(*) FROM validation_segments AS segment JOIN education_areas AS education ON education.source_type=segment.tags->>'workouts:education_source_type' AND education.source_id=(segment.tags->>'workouts:education_source_id')::bigint WHERE ST_Length(ST_CollectionExtract(ST_Difference(segment.geom,education.geom),2)::geography)>0.01)));
        PERFORM osm_catalog.checkpoint_generation_stage(target_generation, 'validate', expected_batch,
            jsonb_build_object('version', 1, 'phase', 'finalize'), processed);
        RETURN;
    END IF;

    IF phase = 'finalize' THEN
        INSERT INTO validation_metrics VALUES ('schemaBytes', to_jsonb((
            SELECT sum(pg_total_relation_size(format('%I.%I', schemaname, tablename)::regclass))
            FROM pg_tables WHERE schemaname = build_schema::text
        )));
        SELECT jsonb_object_agg(metric, value) INTO report FROM validation_metrics WHERE metric <> 'report';
        INSERT INTO validation_metrics(metric, value) VALUES ('report', report)
        ON CONFLICT (metric) DO UPDATE SET value = excluded.value;
        DROP VIEW validation_ways;
        DROP VIEW validation_localities;
        DROP VIEW validation_segments;
        PERFORM osm_catalog.complete_generation_stage(target_generation, 'validate', expected_batch,
            jsonb_build_object('version', 1, 'phase', 'done'), processed);
        RETURN;
    END IF;

    IF phase = 'done' THEN
        PERFORM osm_catalog.complete_generation_stage(target_generation, 'validate', expected_batch,
            jsonb_build_object('version', 1, 'phase', 'done'), processed);
        RETURN;
    END IF;

    RAISE EXCEPTION 'unknown validation phase %', phase;
END;
$procedure$;

CALL validate_candidate_batch(:'OSM_GENERATION_ID'::bigint);

\o
SELECT value FROM validation_metrics WHERE metric = 'report';

\o /dev/null
DROP PROCEDURE validate_candidate_batch(bigint);
RESET max_parallel_workers_per_gather;
RESET enable_hashjoin;
RESET enable_mergejoin;
RESET client_min_messages;
