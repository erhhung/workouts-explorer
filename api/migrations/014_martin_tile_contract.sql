-- +goose Up
SELECT
    app.assert_no_active_manual_ingest ();

SELECT
    app.assert_no_active_scheduled_ingest ();

REVOKE
EXECUTE ON FUNCTION app.raw_route_mvt (integer, integer, integer, uuid, uuid, uuid, bigint)
FROM
    workouts_tiles;

DROP FUNCTION app.raw_route_mvt (integer, integer, integer, uuid, uuid, uuid, bigint);

-- Martin passes request query parameters through one JSON argument. Parse only
-- the opaque scope values approved by the authenticated API proxy.
-- +goose StatementBegin
CREATE FUNCTION app.raw_route_mvt (z integer, x integer, y integer, query_params json) RETURNS bytea LANGUAGE plpgsql STABLE SECURITY DEFINER
SET
    search_path = pg_catalog,
    app,
    public AS $function$
DECLARE
    bounds geometry;
    target_account_id uuid;
    target_session_id uuid;
    target_selection_id uuid;
    target_generation bigint;
    tile bytea;
BEGIN
    IF z NOT BETWEEN 0 AND 22 OR x < 0 OR y < 0 OR
       x >= (1::bigint << z) OR y >= (1::bigint << z) THEN
        RAISE EXCEPTION 'invalid tile coordinates' USING ERRCODE = '22023';
    END IF;
    IF json_typeof(query_params) IS DISTINCT FROM 'object' OR
       (SELECT count( * ) FROM json_object_keys(query_params)) <> 4 OR
       NOT query_params::jsonb ?& ARRAY['target_account_id', 'target_session_id', 'target_selection_id', 'target_generation'] THEN
        RAISE EXCEPTION 'invalid tile scope' USING ERRCODE = '22023';
    END IF;
    BEGIN
        target_account_id := (query_params->> 'target_account_id')::uuid;
        target_session_id := (query_params->> 'target_session_id')::uuid;
        target_selection_id := (query_params->> 'target_selection_id')::uuid;
        target_generation := (query_params->> 'target_generation')::bigint;
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
        JOIN app.account_data_generations data_generation ON data_generation.account_id = selection.account_id
        WHERE selection.id = target_selection_id AND selection.account_id = target_account_id
          AND selection.session_id = target_session_id AND selection.generation = target_generation
          AND data_generation.generation = target_generation
          AND selection.expires_at > transaction_timestamp()
          AND session_row.revoked_at IS NULL AND session_row.expires_at > transaction_timestamp()
          AND principal.disabled_at IS NULL AND account.state = 'active'
    ) THEN
        RAISE EXCEPTION 'invalid or expired map selection' USING ERRCODE = '42501';
    END IF;
    bounds := ST_TileEnvelope(z, x, y);
    SELECT ST_AsMVT(tile_rows, 'routes', 4096, 'geometry') INTO tile
      FROM (
        SELECT upper(replace(workout.id::text, '-', '')) AS "workoutId",
               workout_type.type_key AS "workoutTypeKey",
               workout_type.provider_label AS "workoutType",
               selected.sort_order AS "sortOrder",
               ST_AsMVTGeom(ST_Transform(route.route, 3857), bounds, 4096, 64, true) AS geometry
          FROM app.map_selection_workouts selected
          JOIN app.workouts workout ON workout.id = selected.workout_id AND workout.account_id = selected.account_id
          JOIN app.workout_types workout_type ON workout_type.id = workout.workout_type_id AND workout_type.account_id = workout.account_id
          JOIN app.workout_routes route ON route.workout_id = workout.id AND route.account_id = workout.account_id
         WHERE selected.selection_id = target_selection_id AND selected.account_id = target_account_id
           AND workout.deletion_requested_at IS NULL AND route.route IS NOT NULL
           AND route.route && ST_Transform(bounds, 4326)
        UNION ALL
        SELECT upper(replace(workout.id::text, '-', '')) AS "workoutId",
               workout_type.type_key AS "workoutTypeKey",
               workout_type.provider_label AS "workoutType",
               selected.sort_order AS "sortOrder",
               ST_AsMVTGeom(ST_Transform(ST_StartPoint(component.geom), 3857), bounds, 4096, 64, true) AS geometry
          FROM app.map_selection_workouts selected
          JOIN app.workouts workout ON workout.id = selected.workout_id AND workout.account_id = selected.account_id
          JOIN app.workout_types workout_type ON workout_type.id = workout.workout_type_id AND workout_type.account_id = workout.account_id
          JOIN app.workout_routes route ON route.workout_id = workout.id AND route.account_id = workout.account_id
          CROSS JOIN LATERAL ST_Dump(route.route) component
         WHERE selected.selection_id = target_selection_id AND selected.account_id = target_account_id
           AND workout.deletion_requested_at IS NULL AND route.route IS NOT NULL
           AND ST_Length(component.geom) = 0
           AND component.geom && ST_Transform(bounds, 4326)
      ) tile_rows;
    RETURN COALESCE(tile, ''::bytea);
END;
$function$;

-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.raw_route_mvt (integer, integer, integer, json)
FROM
    PUBLIC,
    workouts_api,
    workouts_worker,
    workouts_tiles;

GRANT
EXECUTE ON FUNCTION app.raw_route_mvt (integer, integer, integer, json) TO workouts_tiles;

GRANT CREATE ON SCHEMA app TO workouts_security_owner;

ALTER FUNCTION app.raw_route_mvt (integer, integer, integer, json) OWNER TO workouts_security_owner;

REVOKE CREATE ON SCHEMA app
FROM
    workouts_security_owner;

UPDATE app.schema_metadata
SET
    schema_version = 14,
    minimum_runtime_version = 14
WHERE
    singleton;

-- +goose Down
SELECT
    app.assert_no_active_manual_ingest ();

SELECT
    app.assert_no_active_scheduled_ingest ();

REVOKE
EXECUTE ON FUNCTION app.raw_route_mvt (integer, integer, integer, json)
FROM
    workouts_tiles;

DROP FUNCTION app.raw_route_mvt (integer, integer, integer, json);

-- +goose StatementBegin
CREATE FUNCTION app.raw_route_mvt (
    z integer,
    x integer,
    y integer,
    target_account_id uuid,
    target_session_id uuid,
    target_selection_id uuid,
    target_generation bigint
) RETURNS bytea LANGUAGE plpgsql STABLE SECURITY DEFINER
SET
    search_path = pg_catalog,
    app,
    public AS $function$
DECLARE
    bounds geometry;
    tile bytea;
BEGIN
    IF z NOT BETWEEN 0 AND 22 OR x < 0 OR y < 0 OR
       x >= (1::bigint << z) OR y >= (1::bigint << z) THEN
        RAISE EXCEPTION 'invalid tile coordinates' USING ERRCODE = '22023';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM app.map_selections selection
        JOIN app.sessions session_row ON session_row.id = selection.session_id
        JOIN app.authentication_principals principal ON principal.id = session_row.principal_id
        JOIN app.users account_owner ON account_owner.principal_id = principal.id AND account_owner.account_id = selection.account_id
        JOIN app.accounts account ON account.id = account_owner.account_id
        JOIN app.account_data_generations data_generation ON data_generation.account_id = selection.account_id
        WHERE selection.id = target_selection_id AND selection.account_id = target_account_id
          AND selection.session_id = target_session_id AND selection.generation = target_generation
          AND data_generation.generation = target_generation
          AND selection.expires_at > transaction_timestamp()
          AND session_row.revoked_at IS NULL AND session_row.expires_at > transaction_timestamp()
          AND principal.disabled_at IS NULL AND account.state = 'active'
    ) THEN
        RAISE EXCEPTION 'invalid or expired map selection' USING ERRCODE = '42501';
    END IF;
    bounds := ST_TileEnvelope(z, x, y);
    SELECT ST_AsMVT(tile_rows, 'routes', 4096, 'geometry') INTO tile
      FROM (
        SELECT upper(replace(workout.id::text, '-', '')) AS "workoutId",
               workout_type.type_key AS "workoutTypeKey",
               workout_type.provider_label AS "workoutType",
               selected.sort_order AS "sortOrder",
               ST_AsMVTGeom(ST_Transform(route.route, 3857), bounds, 4096, 64, true) AS geometry
          FROM app.map_selection_workouts selected
          JOIN app.workouts workout ON workout.id = selected.workout_id AND workout.account_id = selected.account_id
          JOIN app.workout_types workout_type ON workout_type.id = workout.workout_type_id AND workout_type.account_id = workout.account_id
          JOIN app.workout_routes route ON route.workout_id = workout.id AND route.account_id = workout.account_id
         WHERE selected.selection_id = target_selection_id AND selected.account_id = target_account_id
           AND workout.deletion_requested_at IS NULL AND route.route IS NOT NULL
           AND route.route && ST_Transform(bounds, 4326)
        UNION ALL
        SELECT upper(replace(workout.id::text, '-', '')) AS "workoutId",
               workout_type.type_key AS "workoutTypeKey",
               workout_type.provider_label AS "workoutType",
               selected.sort_order AS "sortOrder",
               ST_AsMVTGeom(ST_Transform(ST_StartPoint(component.geom), 3857), bounds, 4096, 64, true) AS geometry
          FROM app.map_selection_workouts selected
          JOIN app.workouts workout ON workout.id = selected.workout_id AND workout.account_id = selected.account_id
          JOIN app.workout_types workout_type ON workout_type.id = workout.workout_type_id AND workout_type.account_id = workout.account_id
          JOIN app.workout_routes route ON route.workout_id = workout.id AND route.account_id = workout.account_id
          CROSS JOIN LATERAL ST_Dump(route.route) component
         WHERE selected.selection_id = target_selection_id AND selected.account_id = target_account_id
           AND workout.deletion_requested_at IS NULL AND route.route IS NOT NULL
           AND ST_Length(component.geom) = 0
           AND component.geom && ST_Transform(bounds, 4326)
      ) tile_rows;
    RETURN COALESCE(tile, ''::bytea);
END;
$function$;

-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.raw_route_mvt (integer, integer, integer, uuid, uuid, uuid, bigint)
FROM
    PUBLIC,
    workouts_api,
    workouts_worker,
    workouts_tiles;

GRANT
EXECUTE ON FUNCTION app.raw_route_mvt (integer, integer, integer, uuid, uuid, uuid, bigint) TO workouts_tiles;

GRANT CREATE ON SCHEMA app TO workouts_security_owner;

ALTER FUNCTION app.raw_route_mvt (integer, integer, integer, uuid, uuid, uuid, bigint) OWNER TO workouts_security_owner;

REVOKE CREATE ON SCHEMA app
FROM
    workouts_security_owner;

UPDATE app.schema_metadata
SET
    schema_version = 13,
    minimum_runtime_version = 12
WHERE
    singleton;
