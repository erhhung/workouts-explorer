-- +goose Up
ALTER TABLE app.workout_routes
ADD COLUMN pace_distance_meters double precision,
ADD COLUMN pace_elapsed_seconds double precision,
ADD COLUMN fastest_kilometer_split_seconds double precision,
ADD COLUMN slowest_kilometer_split_seconds double precision,
ADD COLUMN fastest_mile_split_seconds double precision,
ADD COLUMN slowest_mile_split_seconds double precision,
ADD CONSTRAINT workout_routes_pace_consistency_check CHECK (
    (pace_distance_meters IS NULL)=(pace_elapsed_seconds IS NULL) AND
    (pace_distance_meters IS NULL OR (
        pace_distance_meters>0 AND pace_distance_meters<'Infinity'::double precision AND
        pace_elapsed_seconds>0 AND pace_elapsed_seconds<'Infinity'::double precision AND
        fastest_kilometer_split_seconds IS NOT NULL AND slowest_kilometer_split_seconds IS NOT NULL AND
        fastest_mile_split_seconds IS NOT NULL AND slowest_mile_split_seconds IS NOT NULL AND
        fastest_kilometer_split_seconds<=pace_elapsed_seconds*1000/pace_distance_meters+0.001 AND
        slowest_kilometer_split_seconds>=pace_elapsed_seconds*1000/pace_distance_meters-0.001 AND
        fastest_mile_split_seconds<=pace_elapsed_seconds*1609.344/pace_distance_meters+0.001 AND
        slowest_mile_split_seconds>=pace_elapsed_seconds*1609.344/pace_distance_meters-0.001))
),
ADD CONSTRAINT workout_routes_kilometer_splits_check CHECK (
    (fastest_kilometer_split_seconds IS NULL) = (slowest_kilometer_split_seconds IS NULL)
    AND (
        fastest_kilometer_split_seconds IS NULL
        OR (
            fastest_kilometer_split_seconds > 0
            AND fastest_kilometer_split_seconds <= slowest_kilometer_split_seconds
        )
    )
),
ADD CONSTRAINT workout_routes_mile_splits_check CHECK (
    (fastest_mile_split_seconds IS NULL) = (slowest_mile_split_seconds IS NULL)
    AND (
        fastest_mile_split_seconds IS NULL
        OR (
            fastest_mile_split_seconds > 0
            AND fastest_mile_split_seconds <= slowest_mile_split_seconds
        )
    )
);

-- Accepted display points are the only source for GPS-derived statistics.
-- Component/time breaks never contribute distance, elevation gain or cross-gap splits.
-- +goose StatementBegin
CREATE FUNCTION app.workout_route_statistics_v2(target_account_id uuid,target_workout_id uuid)
RETURNS TABLE(minimum_longitude double precision,minimum_latitude double precision,
 maximum_longitude double precision,maximum_latitude double precision,
 minimum_altitude double precision,maximum_altitude double precision,elevation_gain double precision,
 has_complete_altitude boolean,fastest_kilometer_split_seconds double precision,
 slowest_kilometer_split_seconds double precision,fastest_mile_split_seconds double precision,
 slowest_mile_split_seconds double precision,pace_distance_meters double precision,pace_elapsed_seconds double precision)
LANGUAGE sql STABLE SET search_path=pg_catalog,app,public AS $function$
    WITH accepted AS MATERIALIZED (
        SELECT point.sequence,displayed.component,point.recorded_at,point.longitude,point.latitude,point.altitude
        FROM app.raw_route_display_points(target_account_id,target_workout_id) displayed
        JOIN app.workout_route_points point ON point.account_id=target_account_id
            AND point.workout_id=target_workout_id AND point.sequence=displayed.sequence
        JOIN app.workouts workout ON workout.account_id=point.account_id AND workout.id=point.workout_id
        WHERE point.recorded_at BETWEEN workout.started_at AND workout.ended_at
    ), ordered AS (
        SELECT *,lag(sequence) OVER w prior_sequence,lag(recorded_at) OVER w prior_at,
            lag(longitude) OVER w prior_longitude,lag(latitude) OVER w prior_latitude,
            lag(altitude) OVER w prior_altitude
        FROM accepted WINDOW w AS(PARTITION BY component ORDER BY sequence)
    ), continuity AS (
        -- Equal timestamps can be rounded samples, not a missing section of route.
        SELECT *,CASE WHEN prior_sequence IS NULL OR sequence<>prior_sequence+1 OR recorded_at<prior_at
            THEN 1 ELSE 0 END new_chain FROM ordered
    ), grouped AS (
        SELECT *,sum(new_chain) OVER(PARTITION BY component ORDER BY sequence) chain FROM continuity
    ), legs AS MATERIALIZED (
        SELECT *,extract(epoch FROM recorded_at-prior_at) elapsed_seconds,
            ST_Distance(ST_SetSRID(ST_MakePoint(prior_longitude,prior_latitude),4326)::geography,
                ST_SetSRID(ST_MakePoint(longitude,latitude),4326)::geography) distance_meters
        FROM grouped WHERE new_chain=0 AND recorded_at>prior_at
    ), cumulative AS MATERIALIZED (
        -- Only observed legs contribute. Component gaps add neither distance nor elapsed time.
        SELECT *,sum(distance_meters) OVER(ORDER BY sequence) ending_meters,
            sum(elapsed_seconds) OVER(ORDER BY sequence) ending_seconds FROM legs
    ), totals AS (
        SELECT CASE WHEN sum(distance_meters)>0.0001 THEN sum(distance_meters) END meters,
            CASE WHEN sum(distance_meters)>0.0001 THEN sum(elapsed_seconds)::double precision END seconds FROM legs
    ), units AS (
        SELECT * FROM(VALUES('kilometer'::text,1000::double precision),('mile'::text,1609.344::double precision)) u(name,meters)
    ), boundaries AS (
        SELECT unit.name,boundary.index,
            segment.ending_seconds-segment.elapsed_seconds+segment.elapsed_seconds*((boundary.index*unit.meters-
                (segment.ending_meters-segment.distance_meters))/segment.distance_meters) boundary_seconds
        FROM cumulative segment CROSS JOIN units unit
        CROSS JOIN LATERAL generate_series(floor((segment.ending_meters-segment.distance_meters)/unit.meters)::integer+1,
            floor(segment.ending_meters/unit.meters)::integer) boundary(index)
        WHERE segment.distance_meters>0
    ), moments AS (
        SELECT name,0 AS index,0::double precision boundary_seconds FROM units
        UNION ALL
        SELECT boundary.name,boundary.index,CASE WHEN boundary.index=floor(total.meters/unit.meters)
            AND total.meters-boundary.index*unit.meters<=0.0001 THEN total.seconds ELSE boundary.boundary_seconds END
        FROM boundaries boundary JOIN units unit USING(name) CROSS JOIN totals total
    ), full_intervals AS (
        SELECT moment.name,moment.index,unit.meters,
            boundary_seconds-lag(boundary_seconds) OVER(PARTITION BY moment.name ORDER BY index) seconds
        FROM moments moment JOIN units unit USING(name)
    ), partial_intervals AS (
        SELECT unit.name,total.meters-floor(total.meters/unit.meters)*unit.meters meters,
            total.seconds-COALESCE((SELECT max(boundary_seconds) FROM boundaries b WHERE b.name=unit.name),0) seconds
        FROM totals total CROSS JOIN units unit
        WHERE total.meters-floor(total.meters/unit.meters)*unit.meters>0.0001
    ), intervals AS MATERIALIZED (
        SELECT name,meters,seconds FROM full_intervals WHERE index>0 AND seconds>0
        UNION ALL SELECT name,meters,seconds FROM partial_intervals WHERE seconds>0
    ), splits AS (
        SELECT min(piece.seconds*unit.meters/piece.meters) FILTER(WHERE piece.name='kilometer') fastest_kilometer,
            max(piece.seconds*unit.meters/piece.meters) FILTER(WHERE piece.name='kilometer') slowest_kilometer,
            min(piece.seconds*unit.meters/piece.meters) FILTER(WHERE piece.name='mile') fastest_mile,
            max(piece.seconds*unit.meters/piece.meters) FILTER(WHERE piece.name='mile') slowest_mile
        FROM intervals piece JOIN units unit USING(name)
    ), bounds AS (
        SELECT min(longitude) min_lon,min(latitude) min_lat,max(longitude) max_lon,max(latitude) max_lat,
            min(altitude) min_alt,max(altitude) max_alt,count(*)>0 AND count(altitude)=count(*) complete FROM accepted
    )
    SELECT bounds.min_lon,bounds.min_lat,bounds.max_lon,bounds.max_lat,bounds.min_alt,bounds.max_alt,
        CASE WHEN bounds.min_alt IS NOT NULL THEN
            (SELECT COALESCE(sum(greatest(altitude-prior_altitude,0)),0)::double precision
                FROM legs WHERE altitude IS NOT NULL AND prior_altitude IS NOT NULL) END,
        bounds.complete,splits.fastest_kilometer,splits.slowest_kilometer,splits.fastest_mile,splits.slowest_mile,
        totals.meters,totals.seconds
    FROM bounds CROSS JOIN splits CROSS JOIN totals
$function$;
-- +goose StatementEnd
GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.workout_route_statistics_v2(uuid,uuid) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
REVOKE ALL ON FUNCTION app.workout_route_statistics_v2(uuid,uuid) FROM PUBLIC,workouts_api,workouts_worker,workouts_coverage_worker,workouts_tiles;

-- +goose StatementBegin
CREATE FUNCTION app.workout_route_statistics(target_account_id uuid,target_workout_id uuid)
RETURNS TABLE(minimum_longitude double precision,minimum_latitude double precision,
 maximum_longitude double precision,maximum_latitude double precision,
 minimum_altitude double precision,maximum_altitude double precision,elevation_gain double precision,
 has_complete_altitude boolean,fastest_kilometer_split_seconds double precision,
 slowest_kilometer_split_seconds double precision,fastest_mile_split_seconds double precision,
 slowest_mile_split_seconds double precision)
LANGUAGE sql STABLE SET search_path=pg_catalog,app,public AS $function$
    SELECT minimum_longitude,minimum_latitude,maximum_longitude,maximum_latitude,minimum_altitude,maximum_altitude,elevation_gain,has_complete_altitude,fastest_kilometer_split_seconds,slowest_kilometer_split_seconds,fastest_mile_split_seconds,slowest_mile_split_seconds FROM app.workout_route_statistics_v2(target_account_id,target_workout_id)
$function$;
-- +goose StatementEnd
GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.workout_route_statistics(uuid,uuid) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
REVOKE ALL ON FUNCTION app.workout_route_statistics(uuid,uuid) FROM PUBLIC,workouts_api,workouts_worker,workouts_coverage_worker,workouts_tiles;

-- +goose StatementBegin
CREATE FUNCTION app.replace_workout_split_summary (target_workout_id uuid) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app,
    public AS $function$
DECLARE
    target_account_id uuid;
    authorized boolean;
    replaced integer;
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
        RAISE EXCEPTION 'split summary write requires a live transaction fence' USING ERRCODE = '42501';
    END IF;
    WITH statistics AS MATERIALIZED(SELECT * FROM app.workout_route_statistics_v2(target_account_id,target_workout_id))
    UPDATE app.workout_routes route SET
        fastest_kilometer_split_seconds=statistics.fastest_kilometer_split_seconds,
        slowest_kilometer_split_seconds=statistics.slowest_kilometer_split_seconds,
        fastest_mile_split_seconds=statistics.fastest_mile_split_seconds,
        slowest_mile_split_seconds=statistics.slowest_mile_split_seconds,
        minimum_longitude=COALESCE(ST_XMin(route.route::box3d),statistics.minimum_longitude,route.minimum_longitude),
        minimum_latitude=COALESCE(ST_YMin(route.route::box3d),statistics.minimum_latitude,route.minimum_latitude),
        maximum_longitude=COALESCE(ST_XMax(route.route::box3d),statistics.maximum_longitude,route.maximum_longitude),
        maximum_latitude=COALESCE(ST_YMax(route.route::box3d),statistics.maximum_latitude,route.maximum_latitude),
        minimum_altitude=statistics.minimum_altitude,
        maximum_altitude=statistics.maximum_altitude,
        elevation_gain=statistics.elevation_gain,
        has_complete_altitude=statistics.has_complete_altitude,pace_distance_meters=statistics.pace_distance_meters,pace_elapsed_seconds=statistics.pace_elapsed_seconds,updated_at=transaction_timestamp()
    FROM statistics WHERE route.account_id=target_account_id AND route.workout_id=target_workout_id
        AND ROW(route.fastest_kilometer_split_seconds,route.slowest_kilometer_split_seconds,route.fastest_mile_split_seconds,route.slowest_mile_split_seconds,route.minimum_longitude,route.minimum_latitude,route.maximum_longitude,route.maximum_latitude,route.minimum_altitude,route.maximum_altitude,route.elevation_gain,route.has_complete_altitude,route.pace_distance_meters,route.pace_elapsed_seconds) IS DISTINCT FROM ROW(statistics.fastest_kilometer_split_seconds,statistics.slowest_kilometer_split_seconds,statistics.fastest_mile_split_seconds,statistics.slowest_mile_split_seconds,COALESCE(ST_XMin(route.route::box3d),statistics.minimum_longitude,route.minimum_longitude),COALESCE(ST_YMin(route.route::box3d),statistics.minimum_latitude,route.minimum_latitude),COALESCE(ST_XMax(route.route::box3d),statistics.maximum_longitude,route.maximum_longitude),COALESCE(ST_YMax(route.route::box3d),statistics.maximum_latitude,route.maximum_latitude),statistics.minimum_altitude,statistics.maximum_altitude,statistics.elevation_gain,statistics.has_complete_altitude,statistics.pace_distance_meters,statistics.pace_elapsed_seconds);
    GET DIAGNOSTICS replaced=ROW_COUNT;
    RETURN replaced=1 OR EXISTS(SELECT 1 FROM app.workout_routes WHERE account_id=target_account_id AND workout_id=target_workout_id);
END;
$function$;

-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.replace_workout_split_summary (uuid)
FROM
    PUBLIC;

GRANT
EXECUTE ON FUNCTION app.replace_workout_split_summary (uuid) TO workouts_worker;

GRANT CREATE ON SCHEMA app TO workouts_security_owner;

ALTER FUNCTION app.replace_workout_split_summary (uuid) OWNER TO workouts_security_owner;

REVOKE CREATE ON SCHEMA app
FROM
    workouts_security_owner;

-- Existing caches use the same accepted points and complete component-local splits.
ALTER TABLE app.workout_routes NO FORCE ROW LEVEL SECURITY;
ALTER TABLE app.workout_route_points NO FORCE ROW LEVEL SECURITY;
ALTER TABLE app.workouts NO FORCE ROW LEVEL SECURITY;
ALTER TABLE app.workout_types NO FORCE ROW LEVEL SECURITY;
WITH statistics AS MATERIALIZED(SELECT route.account_id,route.workout_id,computed.* FROM app.workout_routes route
    CROSS JOIN LATERAL app.workout_route_statistics_v2(route.account_id,route.workout_id) computed)
UPDATE app.workout_routes route SET
        fastest_kilometer_split_seconds=statistics.fastest_kilometer_split_seconds,
        slowest_kilometer_split_seconds=statistics.slowest_kilometer_split_seconds,
        fastest_mile_split_seconds=statistics.fastest_mile_split_seconds,
        slowest_mile_split_seconds=statistics.slowest_mile_split_seconds,
        minimum_longitude=COALESCE(ST_XMin(route.route::box3d),statistics.minimum_longitude,route.minimum_longitude),
        minimum_latitude=COALESCE(ST_YMin(route.route::box3d),statistics.minimum_latitude,route.minimum_latitude),
        maximum_longitude=COALESCE(ST_XMax(route.route::box3d),statistics.maximum_longitude,route.maximum_longitude),
        maximum_latitude=COALESCE(ST_YMax(route.route::box3d),statistics.maximum_latitude,route.maximum_latitude),
        minimum_altitude=statistics.minimum_altitude,
        maximum_altitude=statistics.maximum_altitude,
        elevation_gain=statistics.elevation_gain,
        has_complete_altitude=statistics.has_complete_altitude,pace_distance_meters=statistics.pace_distance_meters,pace_elapsed_seconds=statistics.pace_elapsed_seconds,updated_at=transaction_timestamp()
    FROM statistics WHERE route.account_id=statistics.account_id AND route.workout_id=statistics.workout_id AND ROW(route.fastest_kilometer_split_seconds,route.slowest_kilometer_split_seconds,route.fastest_mile_split_seconds,route.slowest_mile_split_seconds,route.minimum_longitude,route.minimum_latitude,route.maximum_longitude,route.maximum_latitude,route.minimum_altitude,route.maximum_altitude,route.elevation_gain,route.has_complete_altitude,route.pace_distance_meters,route.pace_elapsed_seconds) IS DISTINCT FROM ROW(statistics.fastest_kilometer_split_seconds,statistics.slowest_kilometer_split_seconds,statistics.fastest_mile_split_seconds,statistics.slowest_mile_split_seconds,COALESCE(ST_XMin(route.route::box3d),statistics.minimum_longitude,route.minimum_longitude),COALESCE(ST_YMin(route.route::box3d),statistics.minimum_latitude,route.minimum_latitude),COALESCE(ST_XMax(route.route::box3d),statistics.maximum_longitude,route.maximum_longitude),COALESCE(ST_YMax(route.route::box3d),statistics.maximum_latitude,route.maximum_latitude),statistics.minimum_altitude,statistics.maximum_altitude,statistics.elevation_gain,statistics.has_complete_altitude,statistics.pace_distance_meters,statistics.pace_elapsed_seconds);
ALTER TABLE app.workout_routes FORCE ROW LEVEL SECURITY;
ALTER TABLE app.workout_route_points FORCE ROW LEVEL SECURITY;
ALTER TABLE app.workouts FORCE ROW LEVEL SECURITY;
ALTER TABLE app.workout_types FORCE ROW LEVEL SECURITY;

ALTER TABLE app.preferences NO FORCE ROW LEVEL SECURITY;

UPDATE app.preferences preference
SET
    workout_columns = (
        SELECT
            array_agg(
                CASE
                    WHEN column_name = 'elevation' THEN 'elevationGain'
                    ELSE column_name
                END
                ORDER BY
                    ordinal
            )
        FROM
            unnest(preference.workout_columns) WITH ORDINALITY selected (column_name, ordinal)
    )
WHERE
    'elevation' = ANY (workout_columns);

ALTER TABLE app.preferences FORCE ROW LEVEL SECURITY;

UPDATE app.schema_metadata
SET
    schema_version = 11,
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
    schema_version = 10,
    minimum_runtime_version = 8
WHERE
    singleton;

ALTER TABLE app.preferences NO FORCE ROW LEVEL SECURITY;

UPDATE app.preferences preference
SET
    workout_columns = (
        SELECT
            array_agg(
                CASE
                    WHEN column_name = 'elevationGain' THEN 'elevation'
                    ELSE column_name
                END
                ORDER BY
                    ordinal
            )
        FROM
            unnest(preference.workout_columns) WITH ORDINALITY selected (column_name, ordinal)
    )
WHERE
    'elevationGain' = ANY (workout_columns);

ALTER TABLE app.preferences FORCE ROW LEVEL SECURITY;

REVOKE
EXECUTE ON FUNCTION app.replace_workout_split_summary (uuid)
FROM
    workouts_worker;

DROP FUNCTION app.replace_workout_split_summary (uuid);
DROP FUNCTION app.workout_route_statistics(uuid,uuid);
DROP FUNCTION app.workout_route_statistics_v2(uuid,uuid);

ALTER TABLE app.workout_routes
DROP CONSTRAINT workout_routes_pace_consistency_check,
DROP COLUMN pace_distance_meters,
DROP COLUMN pace_elapsed_seconds,
DROP CONSTRAINT workout_routes_kilometer_splits_check,
DROP CONSTRAINT workout_routes_mile_splits_check,
DROP COLUMN fastest_kilometer_split_seconds,
DROP COLUMN slowest_kilometer_split_seconds,
DROP COLUMN fastest_mile_split_seconds,
DROP COLUMN slowest_mile_split_seconds;
