-- +goose Up
ALTER TABLE app.workout_routes
    ADD COLUMN fastest_kilometer_split_seconds double precision,
    ADD COLUMN slowest_kilometer_split_seconds double precision,
    ADD COLUMN fastest_mile_split_seconds double precision,
    ADD COLUMN slowest_mile_split_seconds double precision,
    ADD CONSTRAINT workout_routes_kilometer_splits_check CHECK (
        (fastest_kilometer_split_seconds IS NULL)=(slowest_kilometer_split_seconds IS NULL)
        AND (fastest_kilometer_split_seconds IS NULL OR
             (fastest_kilometer_split_seconds>0 AND fastest_kilometer_split_seconds<=slowest_kilometer_split_seconds))
    ),
    ADD CONSTRAINT workout_routes_mile_splits_check CHECK (
        (fastest_mile_split_seconds IS NULL)=(slowest_mile_split_seconds IS NULL)
        AND (fastest_mile_split_seconds IS NULL OR
             (fastest_mile_split_seconds>0 AND fastest_mile_split_seconds<=slowest_mile_split_seconds))
    );

-- +goose StatementBegin
CREATE FUNCTION app.replace_workout_split_summary(target_workout_id uuid)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app, public
AS $$
DECLARE
    target_account_id uuid;
    authorized boolean;
    replaced integer;
BEGIN
    target_account_id:=app.current_account_id();
    SELECT EXISTS (
        SELECT 1 FROM app.workouts workout
        JOIN app.source_files file ON file.id=workout.source_file_id AND file.account_id=workout.account_id
        JOIN app.ingest_write_capabilities capability
          ON capability.backend_pid=pg_backend_pid() AND capability.transaction_id=txid_current()
         AND capability.account_id=workout.account_id AND capability.source_id=workout.source_id
         AND capability.job_id=file.job_id
        WHERE workout.id=target_workout_id AND workout.account_id=target_account_id
    ) INTO authorized;
    IF NOT authorized THEN
        RAISE EXCEPTION 'split summary write requires a live transaction fence' USING ERRCODE='42501';
    END IF;
    WITH ordered AS (
        SELECT point.sequence,point.recorded_at,point.latitude,point.longitude,
               lag(point.recorded_at) OVER (ORDER BY point.sequence) AS prior_at,
               lag(point.latitude) OVER (ORDER BY point.sequence) AS prior_latitude,
               lag(point.longitude) OVER (ORDER BY point.sequence) AS prior_longitude
          FROM app.workout_route_points point
         WHERE point.account_id=target_account_id AND point.workout_id=target_workout_id
    ), segments AS (
        SELECT *,extract(epoch FROM recorded_at-prior_at) AS elapsed_seconds,
               ST_Distance(
                   ST_SetSRID(ST_MakePoint(prior_longitude,prior_latitude),4326)::geography,
                   ST_SetSRID(ST_MakePoint(longitude,latitude),4326)::geography
               ) AS distance_meters
          FROM ordered WHERE prior_at IS NOT NULL AND recorded_at>prior_at
    ), cumulative AS (
        SELECT *,sum(distance_meters) OVER (ORDER BY sequence) AS ending_meters
          FROM segments WHERE distance_meters>0
    ), boundaries AS (
        SELECT unit.name,unit.meters,boundary.index,
               segment.prior_at + (segment.elapsed_seconds*((boundary.index*unit.meters-(segment.ending_meters-segment.distance_meters))/segment.distance_meters))*interval '1 second' AS boundary_at
          FROM cumulative segment
          CROSS JOIN (VALUES ('kilometer'::text,1000::double precision),('mile'::text,1609.344::double precision)) unit(name,meters)
          CROSS JOIN LATERAL generate_series(
              floor((segment.ending_meters-segment.distance_meters)/unit.meters)::integer+1,
              floor(segment.ending_meters/unit.meters)::integer
          ) boundary(index)
    ), moments AS (
        SELECT unit.name,0 AS index,min(point.recorded_at) AS boundary_at
          FROM app.workout_route_points point
          CROSS JOIN (VALUES ('kilometer'::text),('mile'::text)) unit(name)
         WHERE point.account_id=target_account_id AND point.workout_id=target_workout_id
         GROUP BY unit.name
        UNION ALL
        SELECT name,index,boundary_at FROM boundaries
    ), split_values AS (
        SELECT name,index,extract(epoch FROM boundary_at-lag(boundary_at) OVER (PARTITION BY name ORDER BY index)) AS seconds
          FROM moments
    ), summary AS (
        SELECT min(seconds) FILTER (WHERE name='kilometer' AND index>0 AND seconds>0) AS fastest_kilometer,
               max(seconds) FILTER (WHERE name='kilometer' AND index>0 AND seconds>0) AS slowest_kilometer,
               min(seconds) FILTER (WHERE name='mile' AND index>0 AND seconds>0) AS fastest_mile,
               max(seconds) FILTER (WHERE name='mile' AND index>0 AND seconds>0) AS slowest_mile
          FROM split_values
    )
    UPDATE app.workout_routes route SET
        fastest_kilometer_split_seconds=summary.fastest_kilometer,
        slowest_kilometer_split_seconds=summary.slowest_kilometer,
        fastest_mile_split_seconds=summary.fastest_mile,
        slowest_mile_split_seconds=summary.slowest_mile,
        updated_at=transaction_timestamp()
      FROM summary
     WHERE route.account_id=target_account_id AND route.workout_id=target_workout_id;
    GET DIAGNOSTICS replaced=ROW_COUNT;
    RETURN replaced=1;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.replace_workout_split_summary(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.replace_workout_split_summary(uuid) TO workouts_worker;
GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.replace_workout_split_summary(uuid) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;

-- Existing route summaries are backfilled from their ordered raw route points.
ALTER TABLE app.workout_routes NO FORCE ROW LEVEL SECURITY;
WITH ordered AS (
    SELECT point.account_id,point.workout_id,point.sequence,point.recorded_at,point.latitude,point.longitude,
           lag(point.recorded_at) OVER (PARTITION BY point.account_id,point.workout_id ORDER BY point.sequence) AS prior_at,
           lag(point.latitude) OVER (PARTITION BY point.account_id,point.workout_id ORDER BY point.sequence) AS prior_latitude,
           lag(point.longitude) OVER (PARTITION BY point.account_id,point.workout_id ORDER BY point.sequence) AS prior_longitude
      FROM app.workout_route_points point
), segments AS (
    SELECT *,extract(epoch FROM recorded_at-prior_at) AS elapsed_seconds,
           ST_Distance(ST_SetSRID(ST_MakePoint(prior_longitude,prior_latitude),4326)::geography,
                       ST_SetSRID(ST_MakePoint(longitude,latitude),4326)::geography) AS distance_meters
      FROM ordered WHERE prior_at IS NOT NULL AND recorded_at>prior_at
), cumulative AS (
    SELECT *,sum(distance_meters) OVER (PARTITION BY account_id,workout_id ORDER BY sequence) AS ending_meters
      FROM segments WHERE distance_meters>0
), boundaries AS (
    SELECT segment.account_id,segment.workout_id,unit.name,unit.meters,boundary.index,
           segment.prior_at + (segment.elapsed_seconds*((boundary.index*unit.meters-(segment.ending_meters-segment.distance_meters))/segment.distance_meters))*interval '1 second' AS boundary_at
      FROM cumulative segment
      CROSS JOIN (VALUES ('kilometer'::text,1000::double precision),('mile'::text,1609.344::double precision)) unit(name,meters)
      CROSS JOIN LATERAL generate_series(
          floor((segment.ending_meters-segment.distance_meters)/unit.meters)::integer+1,
          floor(segment.ending_meters/unit.meters)::integer
      ) boundary(index)
), moments AS (
    SELECT point.account_id,point.workout_id,unit.name,0 AS index,min(point.recorded_at) AS boundary_at
      FROM app.workout_route_points point
      CROSS JOIN (VALUES ('kilometer'::text),('mile'::text)) unit(name)
     GROUP BY point.account_id,point.workout_id,unit.name
    UNION ALL
    SELECT account_id,workout_id,name,index,boundary_at FROM boundaries
), split_values AS (
    SELECT account_id,workout_id,name,index,
           extract(epoch FROM boundary_at-lag(boundary_at) OVER (PARTITION BY account_id,workout_id,name ORDER BY index)) AS seconds
      FROM moments
), summaries AS (
    SELECT account_id,workout_id,
           min(seconds) FILTER (WHERE name='kilometer' AND index>0 AND seconds>0) AS fastest_kilometer,
           max(seconds) FILTER (WHERE name='kilometer' AND index>0 AND seconds>0) AS slowest_kilometer,
           min(seconds) FILTER (WHERE name='mile' AND index>0 AND seconds>0) AS fastest_mile,
           max(seconds) FILTER (WHERE name='mile' AND index>0 AND seconds>0) AS slowest_mile
      FROM split_values GROUP BY account_id,workout_id
)
UPDATE app.workout_routes route SET
    fastest_kilometer_split_seconds=summary.fastest_kilometer,
    slowest_kilometer_split_seconds=summary.slowest_kilometer,
    fastest_mile_split_seconds=summary.fastest_mile,
    slowest_mile_split_seconds=summary.slowest_mile,
    updated_at=transaction_timestamp()
  FROM summaries summary
 WHERE route.account_id=summary.account_id AND route.workout_id=summary.workout_id;
ALTER TABLE app.workout_routes FORCE ROW LEVEL SECURITY;

ALTER TABLE app.preferences NO FORCE ROW LEVEL SECURITY;
UPDATE app.preferences preference SET workout_columns=(
    SELECT array_agg(CASE WHEN column_name='elevation' THEN 'elevationGain' ELSE column_name END ORDER BY ordinal)
      FROM unnest(preference.workout_columns) WITH ORDINALITY selected(column_name,ordinal)
) WHERE 'elevation'=ANY(workout_columns);
ALTER TABLE app.preferences FORCE ROW LEVEL SECURITY;

UPDATE app.schema_metadata SET schema_version=11,minimum_runtime_version=8 WHERE singleton;

-- +goose Down
SELECT app.assert_no_active_manual_ingest();
SELECT app.assert_no_active_scheduled_ingest();
UPDATE app.schema_metadata SET schema_version=10,minimum_runtime_version=8 WHERE singleton;
ALTER TABLE app.preferences NO FORCE ROW LEVEL SECURITY;
UPDATE app.preferences preference SET workout_columns=(
    SELECT array_agg(CASE WHEN column_name='elevationGain' THEN 'elevation' ELSE column_name END ORDER BY ordinal)
      FROM unnest(preference.workout_columns) WITH ORDINALITY selected(column_name,ordinal)
) WHERE 'elevationGain'=ANY(workout_columns);
ALTER TABLE app.preferences FORCE ROW LEVEL SECURITY;
REVOKE EXECUTE ON FUNCTION app.replace_workout_split_summary(uuid) FROM workouts_worker;
DROP FUNCTION app.replace_workout_split_summary(uuid);
ALTER TABLE app.workout_routes
    DROP CONSTRAINT workout_routes_kilometer_splits_check,
    DROP CONSTRAINT workout_routes_mile_splits_check,
    DROP COLUMN fastest_kilometer_split_seconds,
    DROP COLUMN slowest_kilometer_split_seconds,
    DROP COLUMN fastest_mile_split_seconds,
    DROP COLUMN slowest_mile_split_seconds;
