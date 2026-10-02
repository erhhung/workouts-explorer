SET search_path TO :"OSM_BUILD_SCHEMA", public;

SELECT set_config('workouts_explorer.osm_region_id', :'OSM_REGION_ID', false);

CREATE OR REPLACE PROCEDURE stage_identity_label_overrides(target_generation bigint)
LANGUAGE plpgsql
AS $procedure$
DECLARE
    phase text;
    cursor_id uuid;
    next_cursor uuid;
    expected_batch bigint;
    processed bigint;
    batch_rows bigint;
    batch_index bigint;
    batch_total bigint;
    phase_rows bigint;
    cursor_rows bigint;
    batch_size CONSTANT integer := 250000;
BEGIN
    SELECT
        coalesce(cursor ->> 'phase', 'init'),
        NULLIF(cursor ->> 'last_segment_id', '')::uuid,
        batch_count,
        rows_processed,
        (cursor ->> 'batch_index')::bigint,
        (cursor ->> 'batch_total')::bigint
    INTO
        phase,
        cursor_id,
        expected_batch,
        processed,
        batch_index,
        batch_total
    FROM osm_catalog.generation_stages
    WHERE generation_id = target_generation
        AND stage = 'identity-label-overrides'
    FOR UPDATE;

    IF phase = 'labels' AND (batch_index IS NULL OR batch_total IS NULL) THEN
        SELECT count(*), count(*) FILTER (WHERE cursor_id IS NOT NULL AND segment_id <= cursor_id)
        INTO phase_rows, cursor_rows
        FROM path_segments;
        batch_index := (cursor_rows + batch_size - 1) / batch_size + 1;
        batch_total := (phase_rows + batch_size - 1) / batch_size + 1;
    END IF;

    IF phase = 'init' THEN
        DROP TABLE IF EXISTS attribution_identity_component_labels;
        DROP TABLE IF EXISTS attribution_identity_component_overrides;

        CREATE TABLE attribution_identity_component_labels (
            group_id uuid NOT NULL,
            parent_segment_id uuid NOT NULL,
            scope_id text NOT NULL,
            locality_relation_id bigint,
            normalized_name text NOT NULL,
            exact_name text NOT NULL,
            display_name text NOT NULL,
            length_m double precision NOT NULL,
            PRIMARY KEY (group_id, parent_segment_id, scope_id)
        );

        CREATE TABLE attribution_identity_component_overrides (
            group_id uuid NOT NULL,
            parent_segment_id uuid NOT NULL,
            scope_id text NOT NULL,
            replacement_group_id uuid NOT NULL,
            replacement_parent_segment_id uuid NOT NULL,
            replacement_name text NOT NULL,
            PRIMARY KEY (group_id, parent_segment_id, scope_id)
        );

        SELECT count(*) INTO phase_rows FROM path_segments;
        PERFORM osm_catalog.checkpoint_generation_stage(
            target_generation,
            'identity-label-overrides',
            expected_batch,
            jsonb_build_object(
                'version', 1,
                'phase', 'labels',
                'last_segment_id', NULL,
                'batch_size', batch_size,
                'batch_index', 1,
                'batch_total', (phase_rows + batch_size - 1) / batch_size + 1
            ),
            processed
        );
        RETURN;
    END IF;

    IF phase = 'labels' THEN
        CREATE TEMP TABLE identity_label_keys ON COMMIT DROP AS
        SELECT segment_id
        FROM path_segments
        WHERE cursor_id IS NULL
            OR segment_id > cursor_id
        ORDER BY segment_id
        LIMIT batch_size;

        GET DIAGNOSTICS batch_rows = ROW_COUNT;
        IF batch_rows = 0 THEN
            PERFORM osm_catalog.checkpoint_generation_stage(
                target_generation,
                'identity-label-overrides',
                expected_batch,
                jsonb_build_object('version', 1, 'phase', 'neighbors'),
                processed
            );
            RETURN;
        END IF;

        SELECT segment_id
        INTO next_cursor
        FROM identity_label_keys
        ORDER BY segment_id DESC
        LIMIT 1;

        INSERT INTO attribution_identity_component_labels
        SELECT
            identity.group_id,
            coalesce(component.parent_segment_id, segment.segment_id),
            coalesce(
                segment.locality_relation_id::text,
                current_setting('workouts_explorer.osm_region_id')
            ),
            segment.locality_relation_id,
            segment.normalized_name,
            lower(regexp_replace(btrim(segment.name), '[[:space:]]+', ' ', 'g')),
            min(segment.name),
            sum(segment.length_m)
        FROM identity_label_keys AS key
        JOIN path_segments AS segment USING (segment_id)
        JOIN attribution_identity_segments AS identity USING (segment_id)
        LEFT JOIN attribution_identity_components AS component
            ON component.group_id = identity.group_id
            AND component.segment_id = segment.segment_id
        WHERE NOT identity.unnamed
            AND segment.broad_class = 'road'
        GROUP BY
            identity.group_id,
            coalesce(component.parent_segment_id, segment.segment_id),
            coalesce(
                segment.locality_relation_id::text,
                current_setting('workouts_explorer.osm_region_id')
            ),
            segment.locality_relation_id,
            segment.normalized_name,
            lower(regexp_replace(btrim(segment.name), '[[:space:]]+', ' ', 'g'))
        ON CONFLICT (group_id, parent_segment_id, scope_id) DO UPDATE
        SET
            display_name = least(attribution_identity_component_labels.display_name, excluded.display_name),
            length_m = attribution_identity_component_labels.length_m + excluded.length_m;

        PERFORM osm_catalog.checkpoint_generation_stage(
            target_generation,
            'identity-label-overrides',
            expected_batch,
            jsonb_build_object(
                'version', 1,
                'phase', phase,
                'last_segment_id', next_cursor,
                'batch_size', batch_size,
                'batch_index', batch_index + 1,
                'batch_total', batch_total
            ),
            processed + batch_rows
        );
        RETURN;
    END IF;

    IF phase = 'neighbors' THEN
        INSERT INTO attribution_identity_component_overrides
        WITH candidates AS (
            SELECT *
            FROM attribution_identity_component_labels
            WHERE length_m <= 25
        ),
        members AS (
            SELECT
                candidate.*,
                segment.segment_id,
                segment.start_graph_node_id,
                segment.end_graph_node_id
            FROM candidates AS candidate
            JOIN attribution_identity_segments AS identity
                ON identity.group_id = candidate.group_id
            JOIN path_segments AS segment
                ON segment.segment_id = identity.segment_id
            LEFT JOIN attribution_identity_components AS component
                ON component.group_id = identity.group_id
                AND component.segment_id = identity.segment_id
            WHERE coalesce(component.parent_segment_id, segment.segment_id) = candidate.parent_segment_id
                AND coalesce(
                    segment.locality_relation_id::text,
                    current_setting('workouts_explorer.osm_region_id')
                ) = candidate.scope_id
        ),
        neighbors AS (
            SELECT
                member.group_id,
                member.parent_segment_id,
                member.scope_id,
                neighbor_identity.group_id replacement_group_id,
                coalesce(
                    neighbor_component.parent_segment_id,
                    neighbor.segment_id
                ) replacement_parent_segment_id,
                neighbor.name replacement_name
            FROM members AS member
            JOIN path_segments AS neighbor
                ON neighbor.segment_id <> member.segment_id
                AND (
                    neighbor.start_graph_node_id IN (member.start_graph_node_id, member.end_graph_node_id)
                    OR neighbor.end_graph_node_id IN (member.start_graph_node_id, member.end_graph_node_id)
                )
            JOIN attribution_identity_segments AS neighbor_identity
                ON neighbor_identity.segment_id = neighbor.segment_id
            LEFT JOIN attribution_identity_components AS neighbor_component
                ON neighbor_component.group_id = neighbor_identity.group_id
                AND neighbor_component.segment_id = neighbor.segment_id
            WHERE NOT neighbor_identity.unnamed
                AND neighbor.broad_class = 'road'
                AND neighbor.normalized_name = member.normalized_name
                AND coalesce(
                    neighbor.locality_relation_id::text,
                    current_setting('workouts_explorer.osm_region_id')
                ) = member.scope_id
                AND (
                    neighbor_identity.group_id,
                    coalesce(neighbor_component.parent_segment_id, neighbor.segment_id)
                ) <> (member.group_id, member.parent_segment_id)
        )
        SELECT
            group_id,
            parent_segment_id,
            scope_id,
            min(replacement_group_id::text)::uuid,
            min(replacement_parent_segment_id::text)::uuid,
            min(replacement_name)
        FROM neighbors
        GROUP BY
            group_id,
            parent_segment_id,
            scope_id
        HAVING count(DISTINCT (replacement_group_id, replacement_parent_segment_id)) = 1;

        ANALYZE attribution_identity_component_overrides;

        PERFORM osm_catalog.complete_generation_stage(
            target_generation,
            'identity-label-overrides',
            expected_batch,
            jsonb_build_object('version', 1, 'phase', 'done'),
            processed
        );
        RETURN;
    END IF;

    RAISE EXCEPTION 'unknown identity-label override phase %', phase;
END;
$procedure$;

CALL stage_identity_label_overrides(:'OSM_GENERATION_ID'::bigint);

DROP PROCEDURE stage_identity_label_overrides(bigint);
