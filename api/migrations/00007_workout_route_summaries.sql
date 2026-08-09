-- +goose Up
CREATE TABLE app.workout_routes (
    account_id uuid NOT NULL,
    workout_id uuid NOT NULL,
    point_count integer NOT NULL CHECK (point_count > 0 AND point_count <= 250000),
    minimum_longitude double precision NOT NULL CHECK (minimum_longitude BETWEEN -180 AND 180),
    minimum_latitude double precision NOT NULL CHECK (minimum_latitude BETWEEN -90 AND 90),
    maximum_longitude double precision NOT NULL CHECK (maximum_longitude BETWEEN -180 AND 180),
    maximum_latitude double precision NOT NULL CHECK (maximum_latitude BETWEEN -90 AND 90),
    minimum_altitude double precision CHECK (minimum_altitude BETWEEN -1000000 AND 1000000),
    maximum_altitude double precision CHECK (maximum_altitude BETWEEN -1000000 AND 1000000),
    elevation_gain double precision CHECK (elevation_gain >= 0 AND elevation_gain <= 500000000000),
    has_complete_altitude boolean NOT NULL,
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    PRIMARY KEY (account_id, workout_id),
    FOREIGN KEY (workout_id, account_id) REFERENCES app.workouts(id, account_id) ON DELETE CASCADE,
    CHECK (minimum_longitude <= maximum_longitude AND minimum_latitude <= maximum_latitude),
    CHECK ((minimum_altitude IS NULL) = (maximum_altitude IS NULL)),
    CHECK ((minimum_altitude IS NULL) = (elevation_gain IS NULL)),
    CHECK (minimum_altitude IS NULL OR minimum_altitude <= maximum_altitude),
    CHECK (NOT has_complete_altitude OR minimum_altitude IS NOT NULL)
);

-- Repair schema-6 backfills that the table-owning migration role could not perform through forced RLS.
-- These ALTERs take exclusive locks; FORCE RLS is restored in the same transaction before runtime access resumes.
ALTER TABLE app.sources NO FORCE ROW LEVEL SECURITY;
ALTER TABLE app.jobs NO FORCE ROW LEVEL SECURITY;
ALTER TABLE app.job_source_contexts NO FORCE ROW LEVEL SECURITY;
ALTER TABLE app.workout_route_points NO FORCE ROW LEVEL SECURITY;

INSERT INTO app.job_source_contexts(job_id,account_id,source_id,source_generation,display_name,source_type)
SELECT job.id,job.account_id,snapshot.source_id,snapshot.source_generation,source.display_name,source.type
  FROM app.jobs job
  JOIN app.job_config_snapshots snapshot ON snapshot.job_id=job.id AND snapshot.account_id=job.account_id
  JOIN app.sources source ON source.id=snapshot.source_id AND source.account_id=snapshot.account_id
 WHERE job.kind IN ('manual_ingest_source','scheduled_ingest_source')
ON CONFLICT (job_id) DO NOTHING;

ALTER TABLE app.jobs DISABLE TRIGGER jobs_state_before_write;
UPDATE app.jobs SET parameters=parameters||jsonb_build_object(
    'mode','bounded','startDate','0001-01-01','endDate','9999-12-31','legacySchema6',true)
 WHERE kind IN ('manual_ingest_source','scheduled_ingest_source')
   AND jsonb_typeof(parameters)='object' AND NOT parameters ? 'mode'
   AND (SELECT count(*) FROM jsonb_object_keys(parameters))=2
   AND parameters ? 'sourceId' AND parameters ? 'generation';
ALTER TABLE app.jobs ENABLE TRIGGER jobs_state_before_write;

WITH ordered AS (
    SELECT point.*,lag(altitude) OVER (PARTITION BY account_id,workout_id ORDER BY sequence) AS prior_altitude
      FROM app.workout_route_points point
), summaries AS (
    SELECT account_id,workout_id,count(*)::integer AS point_count,
           min(longitude) AS minimum_longitude,min(latitude) AS minimum_latitude,
           max(longitude) AS maximum_longitude,max(latitude) AS maximum_latitude,
           min(altitude) AS minimum_altitude,max(altitude) AS maximum_altitude,
           CASE WHEN count(altitude)>0 THEN sum(CASE WHEN prior_altitude IS NOT NULL AND altitude>prior_altitude THEN altitude-prior_altitude ELSE 0 END) END AS elevation_gain,
           bool_and(altitude IS NOT NULL) AS has_complete_altitude
      FROM ordered GROUP BY account_id,workout_id
)
INSERT INTO app.workout_routes(account_id,workout_id,point_count,minimum_longitude,minimum_latitude,
    maximum_longitude,maximum_latitude,minimum_altitude,maximum_altitude,elevation_gain,has_complete_altitude)
SELECT account_id,workout_id,point_count,minimum_longitude,minimum_latitude,maximum_longitude,maximum_latitude,
       minimum_altitude,maximum_altitude,elevation_gain,has_complete_altitude FROM summaries;

ALTER TABLE app.workout_route_points FORCE ROW LEVEL SECURITY;
ALTER TABLE app.job_source_contexts FORCE ROW LEVEL SECURITY;
ALTER TABLE app.jobs FORCE ROW LEVEL SECURITY;
ALTER TABLE app.sources FORCE ROW LEVEL SECURITY;

ALTER TABLE app.workout_routes ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.workout_routes FORCE ROW LEVEL SECURITY;
CREATE POLICY workout_routes_account_policy ON app.workout_routes
    USING (account_id=app.current_account_id()) WITH CHECK (account_id=app.current_account_id());

-- Runtime-7 workers do not write summaries. Invalidating after their already-fenced
-- point mutations prevents stale summaries during the rolling runtime-7 upgrade.
-- +goose StatementBegin
CREATE FUNCTION app.invalidate_workout_route_summary()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
BEGIN
    IF TG_OP='DELETE' THEN
        DELETE FROM app.workout_routes WHERE account_id=OLD.account_id AND workout_id=OLD.workout_id;
        RETURN OLD;
    END IF;
    DELETE FROM app.workout_routes WHERE account_id=NEW.account_id AND workout_id=NEW.workout_id;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER workout_route_points_summary_after_write
AFTER INSERT OR UPDATE OR DELETE ON app.workout_route_points
FOR EACH ROW EXECUTE FUNCTION app.invalidate_workout_route_summary();

-- +goose StatementBegin
CREATE FUNCTION app.replace_workout_route_summary(target_workout_id uuid, new_point_count integer,
    new_minimum_longitude double precision, new_minimum_latitude double precision,
    new_maximum_longitude double precision, new_maximum_latitude double precision,
    new_minimum_altitude double precision, new_maximum_altitude double precision,
    new_elevation_gain double precision, new_has_complete_altitude boolean)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    target_account_id uuid;
    authorized boolean;
BEGIN
    target_account_id := app.current_account_id();
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
        RAISE EXCEPTION 'route summary write requires a live transaction fence' USING ERRCODE='42501';
    END IF;
    IF new_point_count=0 THEN
        DELETE FROM app.workout_routes WHERE account_id=target_account_id AND workout_id=target_workout_id;
        RETURN true;
    END IF;
    INSERT INTO app.workout_routes(account_id,workout_id,point_count,minimum_longitude,minimum_latitude,
        maximum_longitude,maximum_latitude,minimum_altitude,maximum_altitude,elevation_gain,has_complete_altitude)
    VALUES(target_account_id,target_workout_id,new_point_count,new_minimum_longitude,new_minimum_latitude,
        new_maximum_longitude,new_maximum_latitude,new_minimum_altitude,new_maximum_altitude,new_elevation_gain,new_has_complete_altitude)
    ON CONFLICT(account_id,workout_id) DO UPDATE SET
        point_count=EXCLUDED.point_count,minimum_longitude=EXCLUDED.minimum_longitude,minimum_latitude=EXCLUDED.minimum_latitude,
        maximum_longitude=EXCLUDED.maximum_longitude,maximum_latitude=EXCLUDED.maximum_latitude,
        minimum_altitude=EXCLUDED.minimum_altitude,maximum_altitude=EXCLUDED.maximum_altitude,
        elevation_gain=EXCLUDED.elevation_gain,has_complete_altitude=EXCLUDED.has_complete_altitude,
        updated_at=transaction_timestamp();
    RETURN true;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON app.workout_routes FROM PUBLIC;
REVOKE ALL ON FUNCTION app.invalidate_workout_route_summary() FROM PUBLIC;
REVOKE ALL ON FUNCTION app.replace_workout_route_summary(uuid,integer,double precision,double precision,double precision,double precision,double precision,double precision,double precision,boolean) FROM PUBLIC;
GRANT SELECT ON app.workout_routes TO workouts_api;
GRANT SELECT,INSERT,UPDATE,DELETE ON app.workout_routes TO workouts_security_owner;
GRANT EXECUTE ON FUNCTION app.replace_workout_route_summary(uuid,integer,double precision,double precision,double precision,double precision,double precision,double precision,double precision,boolean) TO workouts_worker;
GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.replace_workout_route_summary(uuid,integer,double precision,double precision,double precision,double precision,double precision,double precision,double precision,boolean) OWNER TO workouts_security_owner;
ALTER FUNCTION app.invalidate_workout_route_summary() OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
UPDATE app.schema_metadata SET schema_version=7,minimum_runtime_version=6 WHERE singleton;

-- +goose Down
SELECT app.assert_no_active_manual_ingest();
SELECT app.assert_no_active_scheduled_ingest();
UPDATE app.schema_metadata SET schema_version=6,minimum_runtime_version=1 WHERE singleton;
REVOKE EXECUTE ON FUNCTION app.replace_workout_route_summary(uuid,integer,double precision,double precision,double precision,double precision,double precision,double precision,double precision,boolean) FROM workouts_worker;
REVOKE SELECT ON app.workout_routes FROM workouts_api;
REVOKE SELECT,INSERT,UPDATE,DELETE ON app.workout_routes FROM workouts_security_owner;
DROP FUNCTION app.replace_workout_route_summary(uuid,integer,double precision,double precision,double precision,double precision,double precision,double precision,double precision,boolean);
DROP TRIGGER workout_route_points_summary_after_write ON app.workout_route_points;
DROP FUNCTION app.invalidate_workout_route_summary();
DROP TABLE app.workout_routes;
