-- +goose Up
ALTER TABLE app.workout_routes
DROP CONSTRAINT workout_routes_route_check;

ALTER TABLE app.workout_routes
ALTER COLUMN route TYPE geometry (MultiLineString, 4326) USING ST_Multi (route);

ALTER TABLE app.workout_routes
ADD CONSTRAINT workout_routes_route_check CHECK (
    route IS NULL
    OR (
        GeometryType (route) = 'MULTILINESTRING'
        AND ST_SRID (route) = 4326
    )
);

-- Shared display/export membership. Imported observations remain immutable.
-- Suspect endpoints are excluded, never reconnected across a rejected sample.
-- +goose StatementBegin
CREATE FUNCTION app.raw_route_display_points(target_account_id uuid, target_workout_id uuid)
RETURNS TABLE(sequence integer, component integer) LANGUAGE plpgsql STABLE
SET search_path = pg_catalog, app, public AS $function$
DECLARE
    item record;
    sequences integer[] := '{}';
    components integer[] := '{}';
    rejected boolean[] := '{}';
    implausible boolean[] := '{}';
    epochs double precision[] := '{}';
    accuracies double precision[] := '{}';
    deltas double precision[] := '{}';
    speeds double precision[] := '{}';
    prior_time timestamptz;
    prior_geom geometry;
    current_geom geometry;
    elapsed double precision;
    meters double precision;
    positive_total double precision := 0;
    positive_count integer := 0;
    temporal_gap boolean;
    spatial_gap boolean;
    foot boolean := false;
    recovery_speed double precision := 3;
    reference_accuracy double precision;
    reference_count integer;
    recovery_accuracy double precision;
    pending_start integer := 0;
    pending_origin integer;
    last_seed integer;
    stable_count integer := 0;
    good_count integer;
    good_end integer;
    current_component integer := 1;
    n integer := 0;
    start_index integer := 1;
    i integer;
    j integer;
BEGIN
    SELECT lower(btrim(type.provider_label)) IN
        ('walk','walking','outdoor walk','indoor walk','run','running','outdoor run','indoor run'),
        CASE WHEN lower(btrim(type.provider_label)) IN ('run','running','outdoor run','indoor run') THEN 6 ELSE 3 END
    INTO foot,recovery_speed FROM app.workouts workout
    JOIN app.workout_types type ON type.account_id=workout.account_id AND type.id=workout.workout_type_id
    WHERE workout.account_id=target_account_id AND workout.id=target_workout_id;
    FOR item IN SELECT p.sequence,p.recorded_at,p.longitude,p.latitude,p.horizontal_accuracy
        FROM app.workout_route_points p
        WHERE p.account_id=target_account_id AND p.workout_id=target_workout_id ORDER BY p.sequence
    LOOP
        n := n+1;
        current_geom := ST_SetSRID(ST_MakePoint(item.longitude,item.latitude),4326);
        temporal_gap := false;
        spatial_gap := false;
        IF n>1 THEN
            elapsed := extract(epoch FROM item.recorded_at-prior_time);
            meters := ST_DistanceSphere(prior_geom,current_geom);
            temporal_gap := elapsed>0 AND positive_count>0 AND elapsed>=3*positive_total/positive_count;
            spatial_gap := meters>200;
            IF temporal_gap OR spatial_gap THEN current_component := current_component+1; END IF;
            IF temporal_gap THEN positive_total:=0; positive_count:=0;
            ELSIF elapsed>0 THEN positive_total:=positive_total+elapsed; positive_count:=positive_count+1;
            END IF;
        END IF;
        sequences[n] := item.sequence;
        components[n] := current_component;
        rejected[n] := false;
        implausible[n] := false;
        epochs[n] := extract(epoch FROM item.recorded_at);
        accuracies[n] := item.horizontal_accuracy;
        deltas[n] := CASE WHEN n>1 THEN elapsed ELSE 0 END;
        speeds[n] := CASE WHEN n>1 AND elapsed>0 THEN meters/elapsed ELSE NULL END;
        IF n>1 AND foot AND NOT temporal_gap AND NOT spatial_gap AND elapsed>0 AND meters>12.5*elapsed THEN
            implausible[n] := true;
            rejected[n-1] := true;
            rejected[n] := true;
        END IF;
        prior_time := item.recorded_at;
        prior_geom := current_geom;
    END LOOP;
    -- Recover complete GPS failure episodes, including slower fragments between
    -- bursts. Expansion requires a reliable baseline and a confirmed recovery;
    -- an unresolved or over-budget episode retains endpoint-only rejection.
    IF foot THEN
        FOR i IN 2..n LOOP
            IF pending_start=0 AND implausible[i] THEN
                reference_accuracy := NULL;
                reference_count := 0;
                FOR j IN greatest(1,i-33)..i-2 LOOP
                    IF NOT rejected[j] AND accuracies[j]>0 AND epochs[i]>=epochs[j] AND epochs[i]-epochs[j]<=300 THEN
                        reference_accuracy := least(reference_accuracy,accuracies[j]);
                        reference_count := reference_count+1;
                    END IF;
                END LOOP;
                IF reference_count>=8 THEN
                    recovery_accuracy := greatest(3,2*reference_accuracy);
                    IF accuracies[i-1]>recovery_accuracy OR accuracies[i]>recovery_accuracy THEN
                        pending_start:=i-1; pending_origin:=i; last_seed:=i; stable_count:=0;
                        good_count:=0; good_end:=0;
                        FOR j IN REVERSE (i-2)..greatest(1,i-33) LOOP
                            IF NOT rejected[j] AND accuracies[j]>0 AND accuracies[j]<=recovery_accuracy
                                AND deltas[j]>0 AND speeds[j]<=recovery_speed THEN
                                IF good_count>0 AND components[j]<>components[j+1] THEN good_count:=0; END IF;
                                IF good_count=0 THEN good_end:=j; END IF;
                                good_count:=good_count+1;
                                IF good_count=10 THEN pending_start:=good_end+1; EXIT; END IF;
                            ELSE good_count:=0; good_end:=0;
                            END IF;
                        END LOOP;
                    END IF;
                END IF;
            END IF;
            IF pending_start>0 THEN
                IF implausible[i] THEN last_seed:=i; END IF;
                IF i-pending_origin>=600 OR epochs[i]-epochs[last_seed]>300 THEN
                    pending_start:=0; stable_count:=0; CONTINUE;
                END IF;
                IF NOT rejected[i] AND accuracies[i]>0 AND accuracies[i]<=recovery_accuracy
                    AND deltas[i]>0 AND speeds[i]<=recovery_speed THEN
                    stable_count:=CASE WHEN components[i]=components[i-1] THEN stable_count+1 ELSE 1 END;
                ELSE stable_count:=0;
                END IF;
                IF stable_count=10 THEN
                    FOR j IN pending_start..i-10 LOOP rejected[j]:=true; END LOOP;
                    pending_start:=0; stable_count:=0;
                END IF;
            END IF;
        END LOOP;
    END IF;
    component := 0;
    FOR i IN 1..n+1 LOOP
        IF i<=n AND NOT rejected[i] AND (i=start_index OR components[i]=components[start_index]) THEN CONTINUE; END IF;
        IF i-start_index>=2 THEN
            component := component+1;
            FOR j IN start_index..i-1 LOOP sequence:=sequences[j]; RETURN NEXT; END LOOP;
        END IF;
        start_index := CASE WHEN i<=n AND NOT rejected[i] THEN i ELSE i+1 END;
    END LOOP;
END;
$function$;
-- +goose StatementEnd
GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.raw_route_display_points(uuid,uuid) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
REVOKE ALL ON FUNCTION app.raw_route_display_points(uuid,uuid) FROM PUBLIC,workouts_worker,workouts_tiles;
GRANT EXECUTE ON FUNCTION app.raw_route_display_points(uuid,uuid) TO workouts_api;

-- A positive gap starts a new segment when it is at least three times the
-- running average for the current segment. Duplicate or backward timestamps
-- remain ordered points but do not distort the positive-delta baseline.
-- +goose StatementBegin
CREATE FUNCTION app.build_segmented_workout_route(target_account_id uuid, target_workout_id uuid)
RETURNS geometry LANGUAGE sql STABLE
SET search_path = pg_catalog, app, public AS $function$
    WITH segment_lines AS (
        SELECT displayed.component,
            ST_SetSRID(ST_MakeLine(ST_MakePoint(point.longitude,point.latitude) ORDER BY point.sequence),4326) segment_line
        FROM app.raw_route_display_points(target_account_id,target_workout_id) displayed
        JOIN app.workout_route_points point ON point.account_id=target_account_id
            AND point.workout_id=target_workout_id AND point.sequence=displayed.sequence
        GROUP BY displayed.component
    )
    SELECT ST_Multi(ST_Collect(segment_line ORDER BY component))
    FROM segment_lines
$function$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.build_segmented_workout_route (uuid, uuid)
FROM
    PUBLIC,
    workouts_api,
    workouts_worker,
    workouts_tiles;

-- Forced RLS applies to the migration owner. Relax it only for this
-- transactional rebuild, then immediately restore it.
ALTER TABLE app.workout_route_points NO FORCE ROW LEVEL SECURITY;

ALTER TABLE app.workout_routes NO FORCE ROW LEVEL SECURITY;

UPDATE app.workout_routes summary
SET
    route = app.build_segmented_workout_route (summary.account_id, summary.workout_id)
WHERE
    summary.point_count >= 2;

ALTER TABLE app.workout_routes FORCE ROW LEVEL SECURITY;

ALTER TABLE app.workout_route_points FORCE ROW LEVEL SECURITY;

-- Keep the worker-facing signature stable while applying segmentation to all
-- future route replacements.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.replace_workout_route_summary (
    target_workout_id uuid,
    new_point_count integer,
    new_minimum_longitude double precision,
    new_minimum_latitude double precision,
    new_maximum_longitude double precision,
    new_maximum_latitude double precision,
    new_minimum_altitude double precision,
    new_maximum_altitude double precision,
    new_elevation_gain double precision,
    new_has_complete_altitude boolean
) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app,
    public AS $function$
DECLARE
    target_account_id uuid;
    authorized boolean;
    new_route geometry;
BEGIN
    target_account_id := app.current_account_id();
    SELECT EXISTS (
        SELECT 1 FROM app.workouts workout
        JOIN app.source_files file ON file.id = workout.source_file_id AND file.account_id = workout.account_id
        JOIN app.ingest_write_capabilities capability
          ON capability.backend_pid = pg_backend_pid() AND capability.transaction_id = txid_current()
         AND capability.account_id = workout.account_id AND capability.source_id = workout.source_id
         AND capability.job_id = file.job_id
        WHERE workout.id = target_workout_id AND workout.account_id = target_account_id
    ) INTO authorized;
    IF NOT authorized THEN
        RAISE EXCEPTION 'route summary write requires a live transaction fence' USING ERRCODE = '42501';
    END IF;
    IF new_point_count = 0 THEN
        DELETE FROM app.workout_routes WHERE account_id = target_account_id AND workout_id = target_workout_id;
        RETURN true;
    END IF;
    IF new_point_count >= 2 THEN
        SELECT app.build_segmented_workout_route(target_account_id, target_workout_id) INTO new_route;
    END IF;
    INSERT INTO app.workout_routes(account_id, workout_id, point_count, minimum_longitude, minimum_latitude,
        maximum_longitude, maximum_latitude, minimum_altitude, maximum_altitude, elevation_gain, has_complete_altitude, route)
    VALUES(target_account_id, target_workout_id, new_point_count, new_minimum_longitude, new_minimum_latitude,
        new_maximum_longitude, new_maximum_latitude, new_minimum_altitude, new_maximum_altitude, new_elevation_gain,
        new_has_complete_altitude, new_route)
    ON CONFLICT(account_id, workout_id) DO UPDATE SET
        point_count = EXCLUDED.point_count, minimum_longitude = EXCLUDED.minimum_longitude, minimum_latitude = EXCLUDED.minimum_latitude,
        maximum_longitude = EXCLUDED.maximum_longitude, maximum_latitude = EXCLUDED.maximum_latitude,
        minimum_altitude = EXCLUDED.minimum_altitude, maximum_altitude = EXCLUDED.maximum_altitude,
        elevation_gain = EXCLUDED.elevation_gain, has_complete_altitude = EXCLUDED.has_complete_altitude,
        route = EXCLUDED.route, updated_at = transaction_timestamp();
    RETURN true;
END;
$function$;

-- +goose StatementEnd

GRANT CREATE ON SCHEMA app TO workouts_security_owner;

ALTER FUNCTION app.build_segmented_workout_route (uuid, uuid) OWNER TO workouts_security_owner;

REVOKE CREATE ON SCHEMA app
FROM
    workouts_security_owner;

-- Preserve zero-length route components as point features. Line renderers cannot
-- paint repeated coordinates, and ST_AsMVTGeom drops them during quantization.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.raw_route_mvt (
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
        SELECT upper(replace(workout.id::text, '-', '')) AS workout_id,
               workout_type.type_key AS workout_type_key,
               workout_type.provider_label AS workout_type,
               selected.sort_order,
               ST_AsMVTGeom(ST_Transform(route.route, 3857), bounds, 4096, 64, true) AS geometry
          FROM app.map_selection_workouts selected
          JOIN app.workouts workout ON workout.id = selected.workout_id AND workout.account_id = selected.account_id
          JOIN app.workout_types workout_type ON workout_type.id = workout.workout_type_id AND workout_type.account_id = workout.account_id
          JOIN app.workout_routes route ON route.workout_id = workout.id AND route.account_id = workout.account_id
         WHERE selected.selection_id = target_selection_id AND selected.account_id = target_account_id
           AND workout.deletion_requested_at IS NULL AND route.route IS NOT NULL
           AND route.route && ST_Transform(bounds, 4326)
        UNION ALL
        SELECT upper(replace(workout.id::text, '-', '')) AS workout_id,
               workout_type.type_key AS workout_type_key,
               workout_type.provider_label AS workout_type,
               selected.sort_order,
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

UPDATE app.schema_metadata
SET
    schema_version = 10,
    minimum_runtime_version = 8
WHERE
    singleton;

-- +goose Down
SELECT
    app.assert_no_active_manual_ingest ();

SELECT
    app.assert_no_active_scheduled_ingest ();

UPDATE app.schema_metadata
SET
    schema_version = 9,
    minimum_runtime_version = 8
WHERE
    singleton;

-- Restore schema 9's line-only tile output before reverting route geometry.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.raw_route_mvt (
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
        SELECT upper(replace(workout.id::text, '-', '')) AS workout_id,
               workout_type.type_key AS workout_type_key,
               workout_type.provider_label AS workout_type,
               selected.sort_order,
               ST_AsMVTGeom(ST_Transform(route.route, 3857), bounds, 4096, 64, true) AS geometry
          FROM app.map_selection_workouts selected
          JOIN app.workouts workout ON workout.id = selected.workout_id AND workout.account_id = selected.account_id
          JOIN app.workout_types workout_type ON workout_type.id = workout.workout_type_id AND workout_type.account_id = workout.account_id
          JOIN app.workout_routes route ON route.workout_id = workout.id AND route.account_id = workout.account_id
         WHERE selected.selection_id = target_selection_id AND selected.account_id = target_account_id
           AND workout.deletion_requested_at IS NULL AND route.route IS NOT NULL
           AND route.route && ST_Transform(bounds, 4326)
         ORDER BY selected.sort_order
      ) tile_rows;
    RETURN COALESCE(tile, ''::bytea);
END;
$function$;

-- +goose StatementEnd

-- Restore schema 9's contiguous geometry replacement before removing the
-- segmented geometry helper.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.replace_workout_route_summary (
    target_workout_id uuid,
    new_point_count integer,
    new_minimum_longitude double precision,
    new_minimum_latitude double precision,
    new_maximum_longitude double precision,
    new_maximum_latitude double precision,
    new_minimum_altitude double precision,
    new_maximum_altitude double precision,
    new_elevation_gain double precision,
    new_has_complete_altitude boolean
) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app,
    public AS $function$
DECLARE
    target_account_id uuid;
    authorized boolean;
    new_route geometry;
BEGIN
    target_account_id := app.current_account_id();
    SELECT EXISTS (
        SELECT 1 FROM app.workouts workout
        JOIN app.source_files file ON file.id = workout.source_file_id AND file.account_id = workout.account_id
        JOIN app.ingest_write_capabilities capability
          ON capability.backend_pid = pg_backend_pid() AND capability.transaction_id = txid_current()
         AND capability.account_id = workout.account_id AND capability.source_id = workout.source_id
         AND capability.job_id = file.job_id
        WHERE workout.id = target_workout_id AND workout.account_id = target_account_id
    ) INTO authorized;
    IF NOT authorized THEN
        RAISE EXCEPTION 'route summary write requires a live transaction fence' USING ERRCODE = '42501';
    END IF;
    IF new_point_count = 0 THEN
        DELETE FROM app.workout_routes WHERE account_id = target_account_id AND workout_id = target_workout_id;
        RETURN true;
    END IF;
    IF new_point_count >= 2 THEN
        SELECT ST_SetSRID(ST_MakeLine(ST_MakePoint(point.longitude, point.latitude) ORDER BY point.sequence), 4326)
          INTO new_route FROM app.workout_route_points point
         WHERE point.account_id = target_account_id AND point.workout_id = target_workout_id;
    END IF;
    INSERT INTO app.workout_routes(account_id, workout_id, point_count, minimum_longitude, minimum_latitude,
        maximum_longitude, maximum_latitude, minimum_altitude, maximum_altitude, elevation_gain, has_complete_altitude, route)
    VALUES(target_account_id, target_workout_id, new_point_count, new_minimum_longitude, new_minimum_latitude,
        new_maximum_longitude, new_maximum_latitude, new_minimum_altitude, new_maximum_altitude, new_elevation_gain,
        new_has_complete_altitude, new_route)
    ON CONFLICT(account_id, workout_id) DO UPDATE SET
        point_count = EXCLUDED.point_count, minimum_longitude = EXCLUDED.minimum_longitude, minimum_latitude = EXCLUDED.minimum_latitude,
        maximum_longitude = EXCLUDED.maximum_longitude, maximum_latitude = EXCLUDED.maximum_latitude,
        minimum_altitude = EXCLUDED.minimum_altitude, maximum_altitude = EXCLUDED.maximum_altitude,
        elevation_gain = EXCLUDED.elevation_gain, has_complete_altitude = EXCLUDED.has_complete_altitude,
        route = EXCLUDED.route, updated_at = transaction_timestamp();
    RETURN true;
END;
$function$;

-- +goose StatementEnd

ALTER FUNCTION app.build_segmented_workout_route (uuid, uuid) OWNER TO workouts_migration;

DROP FUNCTION app.build_segmented_workout_route (uuid, uuid);
DROP FUNCTION app.raw_route_display_points (uuid, uuid);

ALTER TABLE app.workout_route_points NO FORCE ROW LEVEL SECURITY;

ALTER TABLE app.workout_routes NO FORCE ROW LEVEL SECURITY;

ALTER TABLE app.workout_routes
DROP CONSTRAINT workout_routes_route_check;

UPDATE app.workout_routes
SET
    route = NULL;

ALTER TABLE app.workout_routes
ALTER COLUMN route TYPE geometry (LineString, 4326) USING NULL::geometry (LineString, 4326);

ALTER TABLE app.workout_routes
ADD CONSTRAINT workout_routes_route_check CHECK (
    route IS NULL
    OR (
        GeometryType (route) = 'LINESTRING'
        AND ST_SRID (route) = 4326
    )
);

WITH
    route_geometry AS (
        SELECT
            point.account_id,
            point.workout_id,
            ST_SetSRID (
                ST_MakeLine (
                    ST_MakePoint (point.longitude, point.latitude)
                    ORDER BY
                        point.sequence
                ),
                4326
            ) AS route
        FROM
            app.workout_route_points point
        GROUP BY
            point.account_id,
            point.workout_id
        HAVING
            count( * ) >= 2
    )
UPDATE app.workout_routes summary
SET
    route = route_geometry.route
FROM
    route_geometry
WHERE
    summary.account_id = route_geometry.account_id
    AND summary.workout_id = route_geometry.workout_id;

ALTER TABLE app.workout_routes FORCE ROW LEVEL SECURITY;

ALTER TABLE app.workout_route_points FORCE ROW LEVEL SECURITY;
