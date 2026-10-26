-- +goose Up
-- Preserve the shipped 019 bodies; CREATE OR REPLACE also preserves their ACLs
-- and security-owner ownership when restoring them on downgrade.
CREATE TABLE app.education_coverage_function_backup (definition text NOT NULL);

INSERT INTO app.education_coverage_function_backup (definition)
SELECT pg_get_functiondef('app.coverage_mvt(integer,integer,integer,json)'::regprocedure)
UNION ALL
SELECT pg_get_functiondef('app.map_selection_coverage_focus(uuid,uuid,uuid,bigint,uuid)'::regprocedure)
UNION ALL
SELECT pg_get_functiondef('app.map_selection_coverage_entity_detail(uuid,uuid,uuid,bigint,text,uuid)'::regprocedure);

REVOKE ALL ON app.education_coverage_function_backup
FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.coverage_mvt(z integer, x integer, y integer, query_params json DEFAULT '{}'::json)
RETURNS bytea
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, app, public
AS $function$
DECLARE
    target_account_id uuid;
    target_session_id uuid;
    target_selection_id uuid;
    target_generation bigint;
    bounds geometry;
    tile bytea;
BEGIN
    IF z NOT BETWEEN 0 AND 22 OR x < 0 OR y < 0 OR x >= (1::bigint << z) OR y >= (1::bigint << z) THEN
        RAISE EXCEPTION 'invalid tile coordinates' USING ERRCODE = '22023';
    END IF;
    IF json_typeof(query_params) IS DISTINCT FROM 'object' OR
        (SELECT count(*) FROM json_object_keys(query_params)) <> 4 OR
        NOT query_params::jsonb ?& ARRAY['target_account_id', 'target_session_id', 'target_selection_id', 'target_generation'] THEN
        RAISE EXCEPTION 'invalid tile scope' USING ERRCODE = '22023';
    END IF;
    BEGIN
        target_account_id := (query_params->>'target_account_id')::uuid;
        target_session_id := (query_params->>'target_session_id')::uuid;
        target_selection_id := (query_params->>'target_selection_id')::uuid;
        target_generation := (query_params->>'target_generation')::bigint;
    EXCEPTION WHEN invalid_text_representation OR numeric_value_out_of_range THEN
        RAISE EXCEPTION 'invalid tile scope' USING ERRCODE = '22023';
    END;
    IF target_account_id IS NULL OR target_session_id IS NULL OR target_selection_id IS NULL OR target_generation < 1 THEN
        RAISE EXCEPTION 'invalid tile scope' USING ERRCODE = '22023';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM app.map_selections selection
        JOIN app.sessions session_row ON session_row.id = selection.session_id
        JOIN app.authentication_principals principal ON principal.id = session_row.principal_id
        JOIN app.users account_owner ON account_owner.principal_id = principal.id AND account_owner.account_id = selection.account_id
        JOIN app.accounts account ON account.id = account_owner.account_id
        JOIN app.account_data_generations generation ON generation.account_id = selection.account_id
        WHERE selection.id = target_selection_id AND selection.account_id = target_account_id
            AND selection.session_id = target_session_id AND selection.generation = target_generation
            AND generation.generation = target_generation AND selection.expires_at > transaction_timestamp()
            AND session_row.revoked_at IS NULL AND session_row.expires_at > transaction_timestamp()
            AND principal.disabled_at IS NULL AND account.state = 'active'
    ) THEN
        RAISE EXCEPTION 'invalid or expired map selection' USING ERRCODE = '42501';
    END IF;

    bounds := ST_TileEnvelope(z, x, y);
    WITH selected AS MATERIALIZED (
        SELECT selection.start_date, selection.end_date, selection.focused_workout_id
        FROM app.map_selections selection WHERE selection.account_id = target_account_id AND selection.id = target_selection_id
    ), eligible AS MATERIALIZED (
        SELECT workout.id, workout.local_start_date FROM app.workouts workout
        JOIN app.workout_routes route ON route.account_id = workout.account_id AND route.workout_id = workout.id
        JOIN app.workout_coverage_states coverage ON coverage.account_id = workout.account_id AND coverage.workout_id = workout.id
        CROSS JOIN selected WHERE workout.account_id = target_account_id AND workout.deletion_requested_at IS NULL
            AND route.route IS NOT NULL AND coverage.applied_route_input_revision IS NOT NULL
            AND workout.local_start_date BETWEEN selected.start_date AND selected.end_date
    ), path_counts AS MATERIALIZED (
        SELECT COALESCE(NULLIF(segment.tags->>'workouts:education_id', '')::uuid, match.logical_path_id) entity_id,
            count(DISTINCT eligible.id)::bigint workout_count
        FROM eligible JOIN app.workout_segment_matches match ON match.account_id = target_account_id AND match.workout_id = eligible.id
        JOIN app.path_segments segment ON segment.account_id = match.account_id AND segment.region_id = match.region_id
            AND segment.generation_id = match.generation_id AND segment.physical_segment_id = match.physical_segment_id
        JOIN app.coverage_paths path ON path.account_id = match.account_id AND path.logical_path_id = match.logical_path_id
        WHERE NOT (segment.highway = 'service' AND COALESCE(segment.tags->>'service', '') IN ('driveway', 'parking_aisle'))
            AND segment.tags->>'amenity' IS DISTINCT FROM 'parking'
            AND (path.name IS NOT NULL OR segment.tags->>'workouts:park_kind' IN ('state_park', 'national_park')
                OR NOT (segment.tags ? 'workouts:park_id'))
        GROUP BY COALESCE(NULLIF(segment.tags->>'workouts:education_id', '')::uuid, match.logical_path_id)
    ), park_counts AS MATERIALIZED (
        SELECT attribution.park_id entity_id, count(DISTINCT eligible.id)::bigint workout_count
        FROM eligible JOIN app.workout_park_attributions attribution
            ON attribution.account_id = target_account_id AND attribution.workout_id = eligible.id GROUP BY attribution.park_id
    ), checked AS MATERIALIZED (
        SELECT match.*, segment.highway, segment.tags FROM app.map_selection_workouts selected_workout
        JOIN eligible ON eligible.id = selected_workout.workout_id
        JOIN app.workout_segment_matches match ON match.account_id = selected_workout.account_id
            AND match.workout_id = selected_workout.workout_id
        JOIN app.path_segments segment ON segment.account_id = match.account_id AND segment.region_id = match.region_id
            AND segment.generation_id = match.generation_id AND segment.physical_segment_id = match.physical_segment_id
        WHERE selected_workout.account_id = target_account_id AND selected_workout.selection_id = target_selection_id
            AND match.geom && ST_Transform(bounds, 4326)
    ), path_geometry AS MATERIALIZED (
        SELECT COALESCE(NULLIF(checked.tags->>'workouts:education_id', '')::uuid, checked.logical_path_id) entity_id,
            ST_UnaryUnion(ST_Collect(checked.geom)) geom FROM checked
        JOIN app.coverage_paths path ON path.account_id = checked.account_id AND path.logical_path_id = checked.logical_path_id
        WHERE NOT (checked.highway = 'service' AND COALESCE(checked.tags->>'service', '') IN ('driveway', 'parking_aisle'))
            AND checked.tags->>'amenity' IS DISTINCT FROM 'parking'
            AND (path.name IS NOT NULL OR checked.tags->>'workouts:park_kind' IN ('state_park', 'national_park')
                OR NOT (checked.tags ? 'workouts:park_id'))
        GROUP BY COALESCE(NULLIF(checked.tags->>'workouts:education_id', '')::uuid, checked.logical_path_id)
    ), park_geometry AS MATERIALIZED (
        SELECT (tags->>'workouts:park_id')::uuid entity_id, ST_UnaryUnion(ST_Collect(geom)) geom FROM checked
        WHERE tags ? 'workouts:park_id' GROUP BY (tags->>'workouts:park_id')::uuid
    ), path_identity AS MATERIALIZED (
        SELECT COALESCE(NULLIF(checked.tags->>'workouts:education_id', '')::uuid, checked.logical_path_id) entity_id,
            CASE WHEN bool_or(checked.tags ? 'workouts:education_id') THEN 'other' ELSE min(path.broad_class) END broad_class,
            COALESCE(min(checked.tags->>'workouts:education_name'), min(path.name)) name,
            COALESCE(min(NULLIF(checked.tags->>'workouts:park_name', ''))
                FILTER (WHERE checked.tags->>'workouts:park_kind' IN ('state_park', 'national_park')), min(path.locality_name)) locality_name,
            min(checked.region_id) region_id
        FROM app.coverage_paths path JOIN checked
            ON checked.account_id = path.account_id AND checked.logical_path_id = path.logical_path_id
        WHERE path.account_id = target_account_id
            AND NOT (checked.highway = 'service' AND COALESCE(checked.tags->>'service', '') IN ('driveway', 'parking_aisle'))
            AND checked.tags->>'amenity' IS DISTINCT FROM 'parking'
            AND (path.name IS NOT NULL OR checked.tags->>'workouts:park_kind' IN ('state_park', 'national_park')
                OR NOT (checked.tags ? 'workouts:park_id'))
        GROUP BY COALESCE(NULLIF(checked.tags->>'workouts:education_id', '')::uuid, checked.logical_path_id)
    ), park_identity AS MATERIALIZED (
        SELECT park.park_id entity_id, 'park'::text broad_class, park.name, park.locality_name, min(segment.region_id) region_id
        FROM app.coverage_parks park JOIN app.path_segments segment ON segment.account_id = park.account_id
            AND segment.tags->>'workouts:park_id' = park.park_id::text WHERE park.account_id = target_account_id
        GROUP BY park.park_id, park.name, park.locality_name
    ), focus_path_geometry AS MATERIALIZED (
        SELECT COALESCE(NULLIF(segment.tags->>'workouts:education_id', '')::uuid, match.logical_path_id) entity_id,
            ST_UnaryUnion(ST_Collect(match.geom)) geom FROM selected
        JOIN app.workout_segment_matches match ON match.account_id = target_account_id AND match.workout_id = selected.focused_workout_id
        JOIN app.path_segments segment ON segment.account_id = match.account_id AND segment.region_id = match.region_id
            AND segment.generation_id = match.generation_id AND segment.physical_segment_id = match.physical_segment_id
        JOIN app.coverage_paths path ON path.account_id = match.account_id AND path.logical_path_id = match.logical_path_id
        WHERE selected.focused_workout_id IS NOT NULL AND match.geom && ST_Transform(bounds, 4326)
            AND NOT (segment.highway = 'service' AND COALESCE(segment.tags->>'service', '') IN ('driveway', 'parking_aisle'))
            AND segment.tags->>'amenity' IS DISTINCT FROM 'parking'
            AND (path.name IS NOT NULL OR segment.tags->>'workouts:park_kind' IN ('state_park', 'national_park')
                OR NOT (segment.tags ? 'workouts:park_id'))
        GROUP BY COALESCE(NULLIF(segment.tags->>'workouts:education_id', '')::uuid, match.logical_path_id)
    ), focus_park_geometry AS MATERIALIZED (
        SELECT (segment.tags->>'workouts:park_id')::uuid entity_id, ST_UnaryUnion(ST_Collect(match.geom)) geom FROM selected
        JOIN app.workout_segment_matches match ON match.account_id = target_account_id AND match.workout_id = selected.focused_workout_id
        JOIN app.path_segments segment ON segment.account_id = match.account_id AND segment.region_id = match.region_id
            AND segment.generation_id = match.generation_id AND segment.physical_segment_id = match.physical_segment_id
        WHERE selected.focused_workout_id IS NOT NULL AND segment.tags ? 'workouts:park_id'
            AND match.geom && ST_Transform(bounds, 4326) GROUP BY (segment.tags->>'workouts:park_id')::uuid
    )
    SELECT COALESCE((SELECT ST_AsMVT(rows, 'coverage', 4096, 'geometry') FROM (
        SELECT upper(replace(identity.entity_id::text, '-', '')) "entityId", 'path'::text "entityKind",
            identity.broad_class "broadClass", identity.name, identity.locality_name "localityName", identity.region_id "regionId",
            counts.workout_count "rangeWorkoutCount", app.coverage_count_bucket(counts.workout_count) "countBucket",
            ST_AsMVTGeom(ST_Transform(geometry.geom, 3857), bounds::box2d, 4096, 64, true) geometry
        FROM path_geometry geometry JOIN path_counts counts USING (entity_id) JOIN path_identity identity USING (entity_id)
        WHERE ST_Intersects(ST_Transform(geometry.geom, 3857), bounds)) rows WHERE geometry IS NOT NULL), '') ||
        COALESCE((SELECT ST_AsMVT(rows, 'coverage_parks', 4096, 'geometry') FROM (
        SELECT upper(replace(identity.entity_id::text, '-', '')) "entityId", 'park'::text "entityKind",
            identity.broad_class "broadClass", identity.name, identity.locality_name "localityName", identity.region_id "regionId",
            counts.workout_count "rangeWorkoutCount", app.coverage_count_bucket(counts.workout_count) "countBucket",
            ST_AsMVTGeom(ST_Transform(geometry.geom, 3857), bounds::box2d, 4096, 64, true) geometry
        FROM park_geometry geometry JOIN park_counts counts USING (entity_id) JOIN park_identity identity USING (entity_id)
        WHERE ST_Intersects(ST_Transform(geometry.geom, 3857), bounds)) rows WHERE geometry IS NOT NULL), '') ||
        COALESCE((SELECT ST_AsMVT(rows, 'coverage_focus', 4096, 'geometry') FROM (
        SELECT upper(replace(identity.entity_id::text, '-', '')) "entityId", 'path'::text "entityKind",
            identity.broad_class "broadClass", identity.name, identity.locality_name "localityName", identity.region_id "regionId",
            counts.workout_count "rangeWorkoutCount", app.coverage_count_bucket(counts.workout_count) "countBucket",
            ST_AsMVTGeom(ST_Transform(geometry.geom, 3857), bounds::box2d, 4096, 64, true) geometry
        FROM focus_path_geometry geometry JOIN path_counts counts USING (entity_id) JOIN path_identity identity USING (entity_id)
        WHERE ST_Intersects(ST_Transform(geometry.geom, 3857), bounds)) rows WHERE geometry IS NOT NULL), '') ||
        COALESCE((SELECT ST_AsMVT(rows, 'coverage_parks_focus', 4096, 'geometry') FROM (
        SELECT upper(replace(identity.entity_id::text, '-', '')) "entityId", 'park'::text "entityKind",
            identity.broad_class "broadClass", identity.name, identity.locality_name "localityName", identity.region_id "regionId",
            counts.workout_count "rangeWorkoutCount", app.coverage_count_bucket(counts.workout_count) "countBucket",
            ST_AsMVTGeom(ST_Transform(geometry.geom, 3857), bounds::box2d, 4096, 64, true) geometry
        FROM focus_park_geometry geometry JOIN park_counts counts USING (entity_id) JOIN park_identity identity USING (entity_id)
        WHERE ST_Intersects(ST_Transform(geometry.geom, 3857), bounds)) rows WHERE geometry IS NOT NULL), '') INTO tile;
    RETURN COALESCE(tile, ''::bytea);
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.map_selection_coverage_focus(
    target_account_id uuid,
    target_session_id uuid,
    target_selection_id uuid,
    target_generation bigint,
    target_workout_id uuid
)
RETURNS jsonb
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, app, public
AS $function$
DECLARE
    result jsonb;
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM app.map_selections selection
        JOIN app.map_selection_workouts selected ON selected.account_id = selection.account_id
            AND selected.selection_id = selection.id AND selected.workout_id = target_workout_id
        JOIN app.sessions session_row ON session_row.id = selection.session_id
        JOIN app.authentication_principals principal ON principal.id = session_row.principal_id
        JOIN app.users owner_row ON owner_row.principal_id = principal.id AND owner_row.account_id = selection.account_id
        JOIN app.accounts account ON account.id = owner_row.account_id
        JOIN app.account_data_generations generation ON generation.account_id = selection.account_id
        JOIN app.workout_coverage_states coverage ON coverage.account_id = selection.account_id
            AND coverage.workout_id = target_workout_id AND coverage.applied_route_input_revision IS NOT NULL
        WHERE selection.id = target_selection_id AND selection.account_id = target_account_id
            AND selection.session_id = target_session_id AND selection.generation = target_generation
            AND generation.generation = target_generation AND selection.expires_at > transaction_timestamp()
            AND session_row.revoked_at IS NULL AND session_row.expires_at > transaction_timestamp()
            AND principal.disabled_at IS NULL AND account.state = 'active'
    ) THEN RETURN NULL; END IF;

    WITH selected AS MATERIALIZED (
        SELECT start_date, end_date FROM app.map_selections
        WHERE account_id = target_account_id AND id = target_selection_id
    ), eligible AS MATERIALIZED (
        SELECT workout.id FROM app.workouts workout
        JOIN app.workout_routes route ON route.account_id = workout.account_id AND route.workout_id = workout.id
        JOIN app.workout_coverage_states coverage ON coverage.account_id = workout.account_id AND coverage.workout_id = workout.id
        CROSS JOIN selected WHERE workout.account_id = target_account_id AND workout.deletion_requested_at IS NULL
            AND route.route IS NOT NULL AND coverage.applied_route_input_revision IS NOT NULL
            AND workout.local_start_date BETWEEN selected.start_date AND selected.end_date
    ), checked AS MATERIALIZED (
        SELECT match.*, segment.highway, segment.tags FROM app.workout_segment_matches match
        JOIN app.path_segments segment ON segment.account_id = match.account_id AND segment.region_id = match.region_id
            AND segment.generation_id = match.generation_id AND segment.physical_segment_id = match.physical_segment_id
        WHERE match.account_id = target_account_id AND match.workout_id = target_workout_id
    ), path_counts AS MATERIALIZED (
        SELECT COALESCE(NULLIF(segment.tags->>'workouts:education_id', '')::uuid, match.logical_path_id) entity_id,
            count(DISTINCT eligible.id)::bigint workout_count
        FROM eligible JOIN app.workout_segment_matches match ON match.account_id = target_account_id AND match.workout_id = eligible.id
        JOIN app.path_segments segment ON segment.account_id = match.account_id AND segment.region_id = match.region_id
            AND segment.generation_id = match.generation_id AND segment.physical_segment_id = match.physical_segment_id
        JOIN app.coverage_paths path ON path.account_id = match.account_id AND path.logical_path_id = match.logical_path_id
        WHERE COALESCE(NULLIF(segment.tags->>'workouts:education_id', '')::uuid, match.logical_path_id) IN (
            SELECT DISTINCT COALESCE(NULLIF(checked.tags->>'workouts:education_id', '')::uuid, checked.logical_path_id) FROM checked)
            AND NOT (segment.highway = 'service' AND COALESCE(segment.tags->>'service', '') IN ('driveway', 'parking_aisle'))
            AND segment.tags->>'amenity' IS DISTINCT FROM 'parking'
            AND (path.name IS NOT NULL OR segment.tags->>'workouts:park_kind' IN ('state_park', 'national_park')
                OR NOT (segment.tags ? 'workouts:park_id'))
        GROUP BY COALESCE(NULLIF(segment.tags->>'workouts:education_id', '')::uuid, match.logical_path_id)
    ), park_counts AS MATERIALIZED (
        SELECT attribution.park_id entity_id, count(DISTINCT eligible.id)::bigint workout_count
        FROM eligible JOIN app.workout_park_attributions attribution ON attribution.account_id = target_account_id
            AND attribution.workout_id = eligible.id
        WHERE attribution.park_id IN (SELECT DISTINCT (tags->>'workouts:park_id')::uuid FROM checked WHERE tags ? 'workouts:park_id')
        GROUP BY attribution.park_id
    ), features AS (
        SELECT 'path'::text entity_kind, app.coverage_count_bucket(counts.workout_count) count_bucket,
            ST_UnaryUnion(ST_Collect(checked.geom)) geom
        FROM checked JOIN app.coverage_paths path ON path.account_id = checked.account_id AND path.logical_path_id = checked.logical_path_id
        JOIN path_counts counts ON counts.entity_id = COALESCE(NULLIF(checked.tags->>'workouts:education_id', '')::uuid, checked.logical_path_id)
        WHERE NOT (checked.highway = 'service' AND COALESCE(checked.tags->>'service', '') IN ('driveway', 'parking_aisle'))
            AND checked.tags->>'amenity' IS DISTINCT FROM 'parking'
            AND (path.name IS NOT NULL OR checked.tags->>'workouts:park_kind' IN ('state_park', 'national_park')
                OR NOT (checked.tags ? 'workouts:park_id'))
        GROUP BY COALESCE(NULLIF(checked.tags->>'workouts:education_id', '')::uuid, checked.logical_path_id), counts.workout_count
        UNION ALL
        SELECT 'park'::text, app.coverage_count_bucket(counts.workout_count), ST_UnaryUnion(ST_Collect(checked.geom))
        FROM checked JOIN park_counts counts ON counts.entity_id = (checked.tags->>'workouts:park_id')::uuid
        WHERE checked.tags ? 'workouts:park_id' GROUP BY checked.tags->>'workouts:park_id', counts.workout_count
    )
    SELECT jsonb_build_object('type', 'FeatureCollection', 'features', COALESCE(jsonb_agg(jsonb_build_object(
        'type', 'Feature', 'properties', jsonb_build_object('entityKind', entity_kind, 'countBucket', count_bucket),
        'geometry', ST_AsGeoJSON(geom, 6)::jsonb)), '[]'::jsonb)) INTO result FROM features WHERE geom IS NOT NULL;
    RETURN result;
END;
$function$;
-- +goose StatementEnd

-- Detail must use the same per-segment identity and source-path eligibility as
-- list, tiles, and focus, not the aggregated education name or raw logical ID.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.map_selection_coverage_entity_detail(
    target_account_id uuid,
    target_session_id uuid,
    target_selection_id uuid,
    target_generation bigint,
    target_entity_kind text,
    target_entity_id uuid
)
RETURNS TABLE (
    entity_id uuid,
    entity_kind text,
    broad_class text,
    name text,
    locality_name text,
    region_id text,
    region_name text,
    range_workout_count bigint,
    range_first_date date,
    range_first_workout_id uuid,
    range_latest_date date,
    range_latest_workout_id uuid,
    all_time_workout_count bigint,
    all_time_first_date date,
    all_time_first_workout_id uuid,
    all_time_latest_date date,
    all_time_latest_workout_id uuid,
    minimum_longitude double precision,
    minimum_latitude double precision,
    maximum_longitude double precision,
    maximum_latitude double precision,
    geometry jsonb,
    fit_minimum_longitude double precision,
    fit_minimum_latitude double precision,
    fit_maximum_longitude double precision,
    fit_maximum_latitude double precision
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, app, public
AS $function$
    WITH metadata AS MATERIALIZED (
        SELECT * FROM app.map_selection_coverage_entities(target_account_id, target_session_id,
            target_selection_id, target_generation, NULL)
        WHERE entity_kind = target_entity_kind AND entity_id = target_entity_id
    ), eligible AS MATERIALIZED (
        SELECT workout.id FROM metadata CROSS JOIN app.map_selections selection
        JOIN app.workouts workout ON workout.account_id = selection.account_id
            AND workout.local_start_date BETWEEN selection.start_date AND selection.end_date
        JOIN app.workout_routes route ON route.workout_id = workout.id AND route.account_id = workout.account_id
        JOIN app.workout_coverage_states coverage ON coverage.workout_id = workout.id
            AND coverage.account_id = workout.account_id
        WHERE selection.account_id = target_account_id AND selection.id = target_selection_id
            AND workout.deletion_requested_at IS NULL AND route.route IS NOT NULL
            AND coverage.applied_route_input_revision IS NOT NULL
    ), pieces AS (
        SELECT match.geom FROM metadata JOIN eligible ON true
        JOIN app.workout_segment_matches match ON match.account_id = target_account_id AND match.workout_id = eligible.id
        JOIN app.path_segments segment ON segment.account_id = match.account_id AND segment.region_id = match.region_id
            AND segment.generation_id = match.generation_id AND segment.physical_segment_id = match.physical_segment_id
        JOIN app.coverage_paths path ON path.account_id = match.account_id AND path.logical_path_id = match.logical_path_id
        WHERE metadata.entity_kind = 'path'
            AND COALESCE(NULLIF(segment.tags->>'workouts:education_id', '')::uuid, match.logical_path_id) = metadata.entity_id
            AND NOT (segment.highway = 'service' AND COALESCE(segment.tags->>'service', '') IN ('driveway', 'parking_aisle'))
            AND segment.tags->>'amenity' IS DISTINCT FROM 'parking'
            AND (path.name IS NOT NULL OR segment.tags->>'workouts:park_kind' IN ('state_park', 'national_park')
                OR NOT (segment.tags ? 'workouts:park_id'))
        UNION ALL
        SELECT match.geom FROM metadata JOIN eligible ON true
        JOIN app.workout_segment_matches match ON match.account_id = target_account_id AND match.workout_id = eligible.id
        JOIN app.path_segments segment ON segment.account_id = match.account_id AND segment.region_id = match.region_id
            AND segment.generation_id = match.generation_id AND segment.physical_segment_id = match.physical_segment_id
        WHERE metadata.entity_kind = 'park' AND segment.tags->>'workouts:park_id' = metadata.entity_id::text
    ), aggregate AS (
        SELECT ST_UnaryUnion(ST_Collect(geom)) geom FROM pieces
    ), projected AS (
        SELECT geom, ST_Transform(geom, 3857) web_mercator FROM aggregate WHERE geom IS NOT NULL
    ), fitted AS (
        SELECT geom, ST_Transform(ST_SetSRID(ST_Envelope(ST_Expand(ST_Extent(web_mercator)::box2d,
            greatest((500 - ST_XMax(ST_Extent(web_mercator)::box2d) + ST_XMin(ST_Extent(web_mercator)::box2d)) / 2, 0),
            greatest((500 - ST_YMax(ST_Extent(web_mercator)::box2d) + ST_YMin(ST_Extent(web_mercator)::box2d)) / 2, 0))), 3857), 4326) fit
        FROM projected GROUP BY geom
    )
    SELECT metadata.*, ST_AsGeoJSON(fitted.geom)::jsonb,
        ST_XMin(fitted.fit), ST_YMin(fitted.fit), ST_XMax(fitted.fit), ST_YMax(fitted.fit)
    FROM metadata JOIN fitted ON true
    WHERE target_entity_kind IN ('path', 'park')
$function$;
-- +goose StatementEnd

GRANT CREATE ON SCHEMA app TO workouts_security_owner;

ALTER FUNCTION app.coverage_mvt(integer, integer, integer, json) OWNER TO workouts_security_owner;

ALTER FUNCTION app.map_selection_coverage_focus(uuid, uuid, uuid, bigint, uuid) OWNER TO workouts_security_owner;

ALTER FUNCTION app.map_selection_coverage_entity_detail(uuid, uuid, uuid, bigint, text, uuid) OWNER TO workouts_security_owner;

REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;

REVOKE ALL ON FUNCTION app.coverage_mvt(integer, integer, integer, json),
    app.map_selection_coverage_focus(uuid, uuid, uuid, bigint, uuid),
    app.map_selection_coverage_entity_detail(uuid, uuid, uuid, bigint, text, uuid)
FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;

GRANT EXECUTE ON FUNCTION app.coverage_mvt(integer, integer, integer, json) TO workouts_tiles;

GRANT EXECUTE ON FUNCTION app.map_selection_coverage_focus(uuid, uuid, uuid, bigint, uuid) TO workouts_api;

GRANT EXECUTE ON FUNCTION app.map_selection_coverage_entity_detail(uuid, uuid, uuid, bigint, text, uuid) TO workouts_api;

UPDATE app.schema_metadata
SET schema_version = 20, minimum_runtime_version = 18
WHERE singleton;

-- +goose Down

-- +goose StatementBegin
DO $block$
DECLARE
    saved_definition text;
BEGIN
    FOR saved_definition IN SELECT definition FROM app.education_coverage_function_backup LOOP
        EXECUTE saved_definition;
    END LOOP;
END;
$block$;
-- +goose StatementEnd

DROP TABLE app.education_coverage_function_backup;

UPDATE app.schema_metadata
SET schema_version = 19, minimum_runtime_version = 18
WHERE singleton;
