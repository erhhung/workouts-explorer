SELECT set_config('workouts_explorer.osm_region_id', :'OSM_REGION_ID', false);

SET search_path TO :"OSM_BUILD_SCHEMA", public;

-- Propagation is complete before rewrite begins. Reclaim graph structures that
-- rewrite no longer reads, while retaining canonical ways, identity segments,
-- and the component primary key used by the batch join.
DROP TABLE IF EXISTS attribution_identity_edges;

DROP INDEX IF EXISTS attribution_identity_components_parent_idx;

CREATE TABLE IF NOT EXISTS path_segments_rewritten (
    LIKE path_segments INCLUDING DEFAULTS INCLUDING GENERATED INCLUDING IDENTITY INCLUDING CONSTRAINTS INCLUDING STORAGE INCLUDING COMMENTS,
    PRIMARY KEY (segment_id)
);

CREATE TABLE IF NOT EXISTS logical_paths_rewritten (
    logical_path_id uuid PRIMARY KEY,
    locality_relation_id bigint,
    name text,
    normalized_name text,
    broad_class text,
    member_segment_count bigint NOT NULL,
    member_length_m double precision NOT NULL
);

CREATE OR REPLACE PROCEDURE rewrite_identity_segments(target_generation bigint)
LANGUAGE plpgsql
AS $procedure$
DECLARE
    cursor_id uuid;
    next_cursor uuid;
    expected_batch bigint;
    processed bigint;
    batch_rows bigint;
    source_count bigint;
    rewritten_count bigint;
    missing_count bigint;
    batch_index bigint;
    batch_total bigint;
    batch_size constant integer := 250000;
BEGIN
    SELECT NULLIF(cursor->>'last_segment_id', '')::uuid, batch_count, rows_processed,
        (cursor->>'batch_index')::bigint, (cursor->>'batch_total')::bigint
    INTO cursor_id, expected_batch, processed, batch_index, batch_total
    FROM osm_catalog.generation_stages
    WHERE generation_id = target_generation AND stage = 'identity-rewrite'
    FOR UPDATE;

    IF batch_index IS NULL OR batch_total IS NULL THEN
        SELECT count(*) INTO source_count FROM path_segments;
        batch_index := (processed + batch_size - 1) / batch_size + 1;
        batch_total := (source_count + batch_size - 1) / batch_size + 1;
    END IF;

    CREATE TEMP TABLE rewrite_keys ON COMMIT DROP AS
    SELECT segment_id
    FROM path_segments
    WHERE cursor_id IS NULL OR segment_id > cursor_id
    ORDER BY segment_id
    LIMIT batch_size;

    GET DIAGNOSTICS batch_rows = ROW_COUNT;
    IF batch_rows = 0 THEN
        SELECT count(*) INTO source_count FROM path_segments;
        SELECT count(*) INTO rewritten_count FROM path_segments_rewritten;
        SELECT count(*) INTO missing_count
        FROM path_segments AS source
        FULL JOIN path_segments_rewritten AS rewritten USING (segment_id)
        WHERE source.segment_id IS NULL OR rewritten.segment_id IS NULL;
        IF source_count <> rewritten_count OR missing_count <> 0 THEN
            RAISE EXCEPTION 'identity rewrite incomplete: source %, rewritten %, missing %', source_count, rewritten_count, missing_count;
        END IF;
        DROP TABLE path_segments;
        ALTER TABLE path_segments_rewritten RENAME TO path_segments;
        DROP TABLE IF EXISTS logical_paths;
        ALTER TABLE logical_paths_rewritten RENAME TO logical_paths;
        CREATE INDEX logical_paths_locality_name_idx
        ON logical_paths (locality_relation_id, normalized_name, broad_class);
        DROP TABLE IF EXISTS attribution_identity_segments;
        DROP TABLE IF EXISTS attribution_identity_edges;
        DROP TABLE IF EXISTS attribution_identity_components;
        DROP TABLE IF EXISTS attribution_identity_branch_classes;
        DROP TABLE IF EXISTS attribution_identity_branch_nodes;
        DROP TABLE IF EXISTS attribution_identity_named_roads;
        DROP TABLE IF EXISTS attribution_identity_component_overrides;
        DROP TABLE IF EXISTS attribution_identity_component_labels;
        ANALYZE path_segments;
        ANALYZE logical_paths;
        ANALYZE attribution_identity_stats;
        PERFORM osm_catalog.complete_generation_stage(
            target_generation, 'identity-rewrite', expected_batch,
            jsonb_build_object('version', 2, 'done', true, 'last_segment_id', cursor_id, 'batch_size', batch_size,
                'batch_index', batch_index, 'batch_total', batch_total),
            processed
        );
        RETURN;
    END IF;
    SELECT segment_id INTO next_cursor FROM rewrite_keys ORDER BY segment_id DESC LIMIT 1;
    CREATE TEMP TABLE rewrite_batch_rows (
        LIKE path_segments INCLUDING DEFAULTS INCLUDING GENERATED INCLUDING IDENTITY INCLUDING CONSTRAINTS
    ) ON COMMIT DROP;
    SET LOCAL enable_hashjoin = off;
    SET LOCAL enable_mergejoin = off;

    INSERT INTO rewrite_batch_rows
    SELECT segment.segment_id, segment.source_way_id, segment.source_way_version, segment.derivation_version,
        segment.start_node_index, segment.end_node_index, segment.boundary_piece,
        segment.start_graph_node_id, segment.end_graph_node_id,
        coalesce(label_override.replacement_name, segment.name), segment.normalized_name,
        segment.highway, segment.broad_class, segment.tags, segment.motor_forward_allowed,
        segment.motor_reverse_allowed, segment.geom, segment.locality_relation_id,
        CASE WHEN NOT identity.unnamed THEN md5(format(
            'workouts-explorer/osm-logical-path/v14:group:%s:member:%s:locality:%s',
            coalesce(label_override.replacement_group_id, identity.group_id),
            coalesce(label_override.replacement_parent_segment_id, component.parent_segment_id, segment.segment_id),
            coalesce(segment.locality_relation_id::text, current_setting('workouts_explorer.osm_region_id'))))::uuid
        ELSE coalesce(component.merged_logical_path_id, md5(format(
            'workouts-explorer/osm-logical-path/v12:group:%s:member:%s',
            identity.group_id, segment.segment_id))::uuid) END,
        segment.length_m
    FROM rewrite_keys AS key
    JOIN path_segments AS segment USING (segment_id)
    JOIN attribution_identity_segments AS identity USING (segment_id)
    LEFT JOIN attribution_identity_components AS component
      ON component.group_id = identity.group_id AND component.segment_id = segment.segment_id
    LEFT JOIN attribution_identity_component_overrides AS label_override
      ON label_override.group_id = identity.group_id
     AND label_override.parent_segment_id = coalesce(component.parent_segment_id, segment.segment_id)
     AND label_override.scope_id = coalesce(segment.locality_relation_id::text, current_setting('workouts_explorer.osm_region_id'));
    IF (SELECT count(*) FROM rewrite_batch_rows) <> batch_rows THEN
        RAISE EXCEPTION 'identity staging is incomplete for rewrite batch';
    END IF;

    INSERT INTO path_segments_rewritten
    SELECT * FROM rewrite_batch_rows
    ON CONFLICT (segment_id) DO NOTHING;

    INSERT INTO logical_paths_rewritten
    SELECT logical_path_id, min(locality_relation_id), min(name), min(normalized_name),
        min(broad_class), count(*), sum(length_m)
    FROM rewrite_batch_rows
    GROUP BY logical_path_id
    ON CONFLICT (logical_path_id) DO UPDATE SET
        locality_relation_id = coalesce(least(logical_paths_rewritten.locality_relation_id, excluded.locality_relation_id), logical_paths_rewritten.locality_relation_id, excluded.locality_relation_id),
        name = coalesce(least(logical_paths_rewritten.name, excluded.name), logical_paths_rewritten.name, excluded.name),
        normalized_name = coalesce(least(logical_paths_rewritten.normalized_name, excluded.normalized_name), logical_paths_rewritten.normalized_name, excluded.normalized_name),
        broad_class = coalesce(least(logical_paths_rewritten.broad_class, excluded.broad_class), logical_paths_rewritten.broad_class, excluded.broad_class),
        member_segment_count = logical_paths_rewritten.member_segment_count + excluded.member_segment_count,
        member_length_m = logical_paths_rewritten.member_length_m + excluded.member_length_m;

    PERFORM osm_catalog.checkpoint_generation_stage(
        target_generation, 'identity-rewrite', expected_batch,
        jsonb_build_object('version', 2, 'done', false, 'last_segment_id', next_cursor, 'batch_size', batch_size,
            'batch_index', batch_index + 1, 'batch_total', batch_total),
        processed + batch_rows
    );
END;
$procedure$;

CALL rewrite_identity_segments(:'OSM_GENERATION_ID'::bigint);

DROP PROCEDURE rewrite_identity_segments(bigint);
