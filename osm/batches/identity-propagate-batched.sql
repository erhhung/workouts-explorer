SET search_path TO :"OSM_BUILD_SCHEMA", public;

CREATE TABLE IF NOT EXISTS attribution_identity_component_hooks (
    root_segment_id uuid PRIMARY KEY,
    group_id uuid NOT NULL,
    parent_segment_id uuid NOT NULL
);

CREATE OR REPLACE PROCEDURE propagate_identity_components(target_generation bigint)
LANGUAGE plpgsql
AS $procedure$
DECLARE
    phase text;
    cursor_id uuid;
    next_cursor uuid;
    expected_batch bigint;
    processed bigint;
    batch_rows bigint;
    changed bigint;
    accumulated bigint;
    iteration bigint;
    maximum_iterations bigint;
    batch_index bigint;
    batch_total bigint;
    phase_rows bigint;
    cursor_rows bigint;
    compress_batch_size constant integer := 1000000;
    hook_batch_size constant integer := 100000;
    assign_batch_size constant integer := 500000;
BEGIN
    SELECT coalesce(cursor->>'phase', 'compress'),
        NULLIF(cursor->>'last_segment_id', '')::uuid,
        batch_count,
        rows_processed,
        coalesce((cursor->>'changed')::bigint, 0),
        coalesce((cursor->>'iteration')::bigint, 0),
        (cursor->>'batch_index')::bigint,
        (cursor->>'batch_total')::bigint
    INTO phase, cursor_id, expected_batch, processed, accumulated, iteration, batch_index, batch_total
    FROM osm_catalog.generation_stages
    WHERE generation_id = target_generation AND stage = 'identity-propagate'
    FOR UPDATE;

    IF batch_index IS NULL OR batch_total IS NULL THEN
        IF phase = 'hook' THEN
            SELECT count(*), count(*) FILTER (WHERE cursor_id IS NOT NULL AND root_segment_id <= cursor_id)
            INTO phase_rows, cursor_rows
            FROM attribution_identity_component_hooks;
            batch_total := (phase_rows + hook_batch_size - 1) / hook_batch_size + 1;
            batch_index := (cursor_rows + hook_batch_size - 1) / hook_batch_size + 1;
        ELSE
            SELECT count(*), count(*) FILTER (WHERE cursor_id IS NOT NULL AND segment_id <= cursor_id)
            INTO phase_rows, cursor_rows
            FROM attribution_identity_components;
            IF phase = 'assign' THEN
                batch_total := (phase_rows + assign_batch_size - 1) / assign_batch_size + 1;
                batch_index := (cursor_rows + assign_batch_size - 1) / assign_batch_size + 1;
            ELSE
                batch_total := (phase_rows + compress_batch_size - 1) / compress_batch_size + 1;
                batch_index := (cursor_rows + compress_batch_size - 1) / compress_batch_size + 1;
            END IF;
        END IF;
    END IF;

    IF phase = 'compress' THEN
        CREATE TEMP TABLE component_keys ON COMMIT DROP AS
        SELECT segment_id
        FROM attribution_identity_components
        WHERE cursor_id IS NULL OR segment_id > cursor_id
        ORDER BY segment_id
        LIMIT compress_batch_size;
        GET DIAGNOSTICS batch_rows = ROW_COUNT;
        IF batch_rows = 0 THEN
            IF accumulated > 0 THEN
        SELECT count(*) INTO phase_rows FROM attribution_identity_components;
        PERFORM osm_catalog.checkpoint_generation_stage(target_generation,'identity-propagate',expected_batch,
          jsonb_build_object('version',4,'phase','compress','last_segment_id',NULL,'changed',0,'iteration',iteration,'batch_size',compress_batch_size,
            'batch_index',1,'batch_total',(phase_rows+compress_batch_size-1)/compress_batch_size+1),processed);
      ELSE
        TRUNCATE attribution_identity_component_hooks;
        INSERT INTO attribution_identity_component_hooks(root_segment_id,group_id,parent_segment_id)
        SELECT greatest(left_node.parent_segment_id,right_node.parent_segment_id),edge.group_id,
          min(least(left_node.parent_segment_id,right_node.parent_segment_id)::text)::uuid
        FROM attribution_identity_edges edge
        JOIN attribution_identity_components left_node ON left_node.group_id=edge.group_id AND left_node.segment_id=edge.left_segment_id
        JOIN attribution_identity_components right_node ON right_node.group_id=edge.group_id AND right_node.segment_id=edge.right_segment_id
        WHERE left_node.parent_segment_id<>right_node.parent_segment_id
        GROUP BY edge.group_id,greatest(left_node.parent_segment_id,right_node.parent_segment_id);
        SELECT count(*) INTO phase_rows FROM attribution_identity_component_hooks;
        PERFORM osm_catalog.checkpoint_generation_stage(target_generation,'identity-propagate',expected_batch,
          jsonb_build_object('version',4,'phase','hook','last_segment_id',NULL,'changed',0,'iteration',iteration,'batch_size',hook_batch_size,
            'batch_index',1,'batch_total',(phase_rows+hook_batch_size-1)/hook_batch_size+1),processed);
      END IF;
      RETURN;
    END IF;
        SELECT segment_id INTO next_cursor FROM component_keys ORDER BY segment_id DESC LIMIT 1;
        UPDATE attribution_identity_components AS node
        SET parent_segment_id = parent.parent_segment_id
        FROM component_keys AS key, attribution_identity_components AS parent
        WHERE node.segment_id = key.segment_id AND parent.group_id = node.group_id
          AND parent.segment_id = node.parent_segment_id
          AND node.parent_segment_id <> parent.parent_segment_id;
        GET DIAGNOSTICS changed = ROW_COUNT;
    PERFORM osm_catalog.checkpoint_generation_stage(target_generation,'identity-propagate',expected_batch,
      jsonb_build_object('version',4,'phase','compress','last_segment_id',next_cursor,'changed',accumulated+changed,'iteration',iteration,'batch_size',compress_batch_size,
        'batch_index',batch_index+1,'batch_total',batch_total),
      processed+batch_rows);
    RETURN;
  END IF;
    IF phase = 'hook' THEN
    CREATE TEMP TABLE component_keys ON COMMIT DROP AS SELECT root_segment_id segment_id
      FROM attribution_identity_component_hooks WHERE cursor_id IS NULL OR root_segment_id>cursor_id
      ORDER BY root_segment_id LIMIT hook_batch_size;
    GET DIAGNOSTICS batch_rows=ROW_COUNT;
    IF batch_rows=0 THEN
      IF accumulated>0 THEN
        SELECT count(*) INTO maximum_iterations FROM attribution_identity_components;
        IF iteration+1>maximum_iterations THEN RAISE EXCEPTION 'identity propagation did not converge'; END IF;
        SELECT count(*) INTO phase_rows FROM attribution_identity_components;
        PERFORM osm_catalog.checkpoint_generation_stage(target_generation,'identity-propagate',expected_batch,
          jsonb_build_object('version',4,'phase','compress','last_segment_id',NULL,'changed',0,'iteration',iteration+1,'batch_size',compress_batch_size,
            'batch_index',1,'batch_total',(phase_rows+compress_batch_size-1)/compress_batch_size+1),processed);
      ELSE
        SELECT count(*) INTO phase_rows FROM attribution_identity_components;
        PERFORM osm_catalog.checkpoint_generation_stage(target_generation,'identity-propagate',expected_batch,
          jsonb_build_object('version',4,'phase','assign','last_segment_id',NULL,'changed',0,'iteration',iteration,'batch_size',assign_batch_size,
            'batch_index',1,'batch_total',(phase_rows+assign_batch_size-1)/assign_batch_size+1),processed);
      END IF;
      RETURN;
    END IF;
    SELECT segment_id INTO next_cursor FROM component_keys ORDER BY segment_id DESC LIMIT 1;
    UPDATE attribution_identity_components root SET parent_segment_id=hook.parent_segment_id
    FROM component_keys key JOIN attribution_identity_component_hooks hook ON hook.root_segment_id=key.segment_id
    WHERE root.group_id=hook.group_id AND root.segment_id=hook.root_segment_id AND root.parent_segment_id=root.segment_id;
    GET DIAGNOSTICS changed=ROW_COUNT;
    PERFORM osm_catalog.checkpoint_generation_stage(target_generation,'identity-propagate',expected_batch,
      jsonb_build_object('version',4,'phase','hook','last_segment_id',next_cursor,'changed',accumulated+changed,'iteration',iteration,'batch_size',hook_batch_size,
        'batch_index',batch_index+1,'batch_total',batch_total),
      processed+batch_rows);
    RETURN;
  END IF;
    IF phase = 'assign' THEN
    CREATE TEMP TABLE component_keys ON COMMIT DROP AS SELECT segment_id FROM attribution_identity_components
      WHERE cursor_id IS NULL OR segment_id>cursor_id ORDER BY segment_id LIMIT assign_batch_size;
    GET DIAGNOSTICS batch_rows=ROW_COUNT;
    IF batch_rows=0 THEN
      DROP TABLE IF EXISTS attribution_identity_stats;
      CREATE TABLE attribution_identity_stats AS SELECT
        (SELECT count(*) FROM attribution_identity_edges) logical_id_edges,count(*) affected_logical_ids,
        count(DISTINCT(group_id,parent_segment_id)) merged_components,0::bigint scope_rebased_segments,
        0::bigint scope_rebased_logical_ids,
        (SELECT count(*) FROM attribution_identity_edges edge
          JOIN attribution_identity_components left_component ON left_component.group_id=edge.group_id AND left_component.segment_id=edge.left_segment_id
          JOIN attribution_identity_components right_component ON right_component.group_id=edge.group_id AND right_component.segment_id=edge.right_segment_id
          WHERE left_component.merged_logical_path_id<>right_component.merged_logical_path_id) remaining_connected_splits
      FROM attribution_identity_components;
      DROP TABLE attribution_identity_component_hooks;
      PERFORM osm_catalog.complete_generation_stage(target_generation,'identity-propagate',expected_batch,
        jsonb_build_object('version',4,'phase','done','last_segment_id',cursor_id,'iteration',iteration,'batch_size',assign_batch_size,
          'batch_index',batch_index,'batch_total',batch_total),processed);
      RETURN;
    END IF;
    SELECT segment_id INTO next_cursor FROM component_keys ORDER BY segment_id DESC LIMIT 1;
    UPDATE attribution_identity_components component SET merged_logical_path_id=md5(format(
      'workouts-explorer/osm-logical-path/v12:group:%s:member:%s',component.group_id,component.parent_segment_id))::uuid
    FROM component_keys key WHERE component.segment_id=key.segment_id;
    PERFORM osm_catalog.checkpoint_generation_stage(target_generation,'identity-propagate',expected_batch,
      jsonb_build_object('version',4,'phase','assign','last_segment_id',next_cursor,'changed',0,'iteration',iteration,'batch_size',assign_batch_size,
        'batch_index',batch_index+1,'batch_total',batch_total),processed+batch_rows);
    RETURN;
  END IF;
    RAISE EXCEPTION 'unknown identity propagation phase %', phase;
END;
$procedure$;

CALL propagate_identity_components(:'OSM_GENERATION_ID'::bigint);

DROP PROCEDURE propagate_identity_components(bigint);
