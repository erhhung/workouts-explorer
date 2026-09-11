-- +goose Up
ALTER TABLE app.workouts
    ADD COLUMN timezone_reference_workout_id uuid,
    ADD COLUMN timezone_dataset_release text CHECK (
        timezone_dataset_release IS NULL OR length(timezone_dataset_release) BETWEEN 1 AND 128
    ),
    ADD CONSTRAINT workouts_timezone_derivation_check CHECK (
        (timezone_name IS NULL AND timezone_source IS NULL AND timezone_reference_workout_id IS NULL AND timezone_dataset_release IS NULL)
        OR (timezone_name IS NOT NULL AND timezone_source='route_boundary'
            AND timezone_reference_workout_id IS NULL AND timezone_dataset_release IS NOT NULL)
        OR (timezone_name IS NOT NULL AND timezone_source='nearest_route_boundary'
            AND timezone_reference_workout_id IS NOT NULL AND timezone_dataset_release IS NOT NULL)
    );
CREATE INDEX workouts_timezone_seed_idx
ON app.workouts (account_id,start_offset_minutes,started_at,id)
WHERE timezone_source='route_boundary' AND deletion_requested_at IS NULL;

CREATE TABLE app.timezone_migration_function_backup (
    function_name text PRIMARY KEY,
    definition text NOT NULL
);
REVOKE ALL ON app.timezone_migration_function_backup FROM PUBLIC,workouts_api,workouts_worker;
INSERT INTO app.timezone_migration_function_backup(function_name,definition)
SELECT 'require_ingest_write_capability',pg_get_functiondef('app.require_ingest_write_capability()'::regprocedure);

CREATE TABLE app.workout_timezone_write_capabilities (
    backend_pid integer NOT NULL,
    transaction_id bigint NOT NULL,
    account_id uuid NOT NULL,
    workout_id uuid NOT NULL,
    PRIMARY KEY (backend_pid,transaction_id),
    FOREIGN KEY (workout_id,account_id) REFERENCES app.workouts(id,account_id) ON DELETE CASCADE
);
ALTER TABLE app.workout_timezone_write_capabilities ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.workout_timezone_write_capabilities FORCE ROW LEVEL SECURITY;
CREATE POLICY workout_timezone_capabilities_owner_policy ON app.workout_timezone_write_capabilities
TO workouts_security_owner USING (true) WITH CHECK (true);
REVOKE ALL ON app.workout_timezone_write_capabilities FROM PUBLIC,workouts_api,workouts_worker;
GRANT SELECT,INSERT,DELETE ON app.workout_timezone_write_capabilities TO workouts_security_owner;

-- +goose StatementBegin
CREATE FUNCTION app.clear_workout_timezone_write_capability()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,app
AS $$
BEGIN
    DELETE FROM app.workout_timezone_write_capabilities
     WHERE backend_pid=NEW.backend_pid AND transaction_id=NEW.transaction_id;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER workout_timezone_write_capability_cleanup
AFTER INSERT ON app.workout_timezone_write_capabilities
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION app.clear_workout_timezone_write_capability();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.require_ingest_write_capability()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,app
AS $$
DECLARE
    target_account_id uuid;
    target_source_id uuid;
    target_job_id uuid;
    target_record_id uuid;
    target_workout_id uuid;
    capability_exists boolean;
BEGIN
    IF TG_OP='UPDATE' AND TG_TABLE_NAME='workouts' THEN
        IF EXISTS (
            SELECT 1 FROM app.workout_timezone_write_capabilities capability
             WHERE capability.backend_pid=pg_backend_pid() AND capability.transaction_id=txid_current()
               AND capability.account_id=NEW.account_id AND capability.workout_id=NEW.id
        ) THEN RETURN NEW; END IF;
    END IF;
    IF TG_OP='DELETE' AND TG_TABLE_NAME IN ('workouts','workout_aggregates','workout_route_points','workout_import_events') THEN
        target_account_id:=OLD.account_id;
        IF TG_TABLE_NAME='workouts' THEN target_workout_id:=OLD.id; ELSE target_workout_id:=OLD.workout_id; END IF;
        IF EXISTS (
            SELECT 1 FROM app.workout_deletion_capabilities capability
             WHERE capability.backend_pid=pg_backend_pid() AND capability.transaction_id=txid_current()
               AND capability.account_id=target_account_id AND capability.workout_id=target_workout_id
        ) THEN RETURN OLD; END IF;
    END IF;
    IF TG_TABLE_NAME='workouts' THEN
        IF TG_OP='UPDATE' AND
           OLD.deletion_requested_at IS NULL AND NEW.deletion_requested_at IS NOT NULL AND
           OLD.deletion_target_id IS NULL AND NEW.deletion_target_id IS NOT NULL AND
           (OLD.id,OLD.account_id,OLD.source_id,OLD.source_file_id,OLD.workout_type_id,OLD.provider_id,
            OLD.fallback_fingerprint_version,OLD.fallback_sha256,OLD.content_sha256,OLD.provider_label,
            OLD.started_at,OLD.ended_at,OLD.start_offset_minutes,OLD.end_offset_minutes,OLD.local_start_date,
            OLD.timezone_name,OLD.timezone_source,OLD.timezone_reference_workout_id,OLD.timezone_dataset_release,
            OLD.provider_duration,OLD.is_indoor,OLD.location,OLD.created_at) IS NOT DISTINCT FROM
           (NEW.id,NEW.account_id,NEW.source_id,NEW.source_file_id,NEW.workout_type_id,NEW.provider_id,
            NEW.fallback_fingerprint_version,NEW.fallback_sha256,NEW.content_sha256,NEW.provider_label,
            NEW.started_at,NEW.ended_at,NEW.start_offset_minutes,NEW.end_offset_minutes,NEW.local_start_date,
            NEW.timezone_name,NEW.timezone_source,NEW.timezone_reference_workout_id,NEW.timezone_dataset_release,
            NEW.provider_duration,NEW.is_indoor,NEW.location,NEW.created_at) AND
           EXISTS (SELECT 1 FROM app.workout_deletion_targets target
                    WHERE target.id=NEW.deletion_target_id AND target.account_id=NEW.account_id
                      AND target.workout_id=NEW.id AND target.state='pending') THEN
            RETURN NEW;
        END IF;
        IF TG_OP='INSERT' AND (NEW.deletion_requested_at IS NOT NULL OR NEW.deletion_target_id IS NOT NULL) THEN
            RAISE EXCEPTION 'workout deletion markers require the enqueue function' USING ERRCODE='42501';
        END IF;
        IF TG_OP='UPDATE' AND (OLD.deletion_requested_at,OLD.deletion_target_id) IS DISTINCT FROM
           (NEW.deletion_requested_at,NEW.deletion_target_id) THEN
            RAISE EXCEPTION 'workout deletion markers require the enqueue function' USING ERRCODE='42501';
        END IF;
    END IF;
    IF TG_OP='DELETE' THEN
        IF TG_TABLE_NAME='source_files' THEN
            target_account_id:=OLD.account_id; target_source_id:=OLD.source_id; target_job_id:=OLD.job_id;
        ELSIF TG_TABLE_NAME='workout_types' THEN target_account_id:=OLD.account_id;
        ELSIF TG_TABLE_NAME='workouts' THEN
            target_account_id:=OLD.account_id; target_source_id:=OLD.source_id; target_record_id:=OLD.source_file_id;
        ELSIF TG_TABLE_NAME IN ('workout_aggregates','workout_route_points') THEN
            target_account_id:=OLD.account_id; target_record_id:=OLD.workout_id;
        ELSIF TG_TABLE_NAME='workout_import_events' THEN
            target_account_id:=OLD.account_id; target_source_id:=OLD.source_id; target_job_id:=OLD.job_id;
        ELSE RAISE EXCEPTION 'unsupported ingest capability trigger table %',TG_TABLE_NAME USING ERRCODE='55000';
        END IF;
    ELSE
        IF TG_TABLE_NAME='source_files' THEN
            target_account_id:=NEW.account_id; target_source_id:=NEW.source_id; target_job_id:=NEW.job_id;
        ELSIF TG_TABLE_NAME='workout_types' THEN target_account_id:=NEW.account_id;
        ELSIF TG_TABLE_NAME='workouts' THEN
            target_account_id:=NEW.account_id; target_source_id:=NEW.source_id; target_record_id:=NEW.source_file_id;
        ELSIF TG_TABLE_NAME IN ('workout_aggregates','workout_route_points') THEN
            target_account_id:=NEW.account_id; target_record_id:=NEW.workout_id;
        ELSIF TG_TABLE_NAME='workout_import_events' THEN
            target_account_id:=NEW.account_id; target_source_id:=NEW.source_id; target_job_id:=NEW.job_id;
        ELSE RAISE EXCEPTION 'unsupported ingest capability trigger table %',TG_TABLE_NAME USING ERRCODE='55000';
        END IF;
    END IF;
    IF TG_TABLE_NAME='workouts' THEN
        SELECT file.job_id INTO target_job_id FROM app.source_files file
         WHERE file.id=target_record_id AND file.account_id=target_account_id AND file.source_id=target_source_id;
    ELSIF TG_TABLE_NAME IN ('workout_aggregates','workout_route_points') THEN
        SELECT workout.source_id,file.job_id INTO target_source_id,target_job_id
          FROM app.workouts workout JOIN app.source_files file ON file.id=workout.source_file_id
           AND file.account_id=workout.account_id AND file.source_id=workout.source_id
         WHERE workout.id=target_record_id AND workout.account_id=target_account_id;
    END IF;
    SELECT EXISTS (SELECT 1 FROM app.ingest_write_capabilities capability
        WHERE capability.backend_pid=pg_backend_pid() AND capability.transaction_id=txid_current()
          AND capability.account_id=target_account_id
          AND (target_source_id IS NULL OR capability.source_id=target_source_id)
          AND (target_job_id IS NULL OR capability.job_id=target_job_id)) INTO capability_exists;
    IF NOT capability_exists THEN
        RAISE EXCEPTION 'ingest domain write requires a live transaction fence' USING ERRCODE='42501';
    END IF;
    IF TG_OP='DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.apply_workout_timezone(
    target_account_id uuid,target_workout_id uuid,new_timezone_name text,new_timezone_source text,
    new_reference_workout_id uuid,new_dataset_release text
) RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,app
AS $$
DECLARE changed integer;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM app.workouts WHERE account_id=target_account_id AND id=target_workout_id
                   AND deletion_requested_at IS NULL) THEN RETURN false; END IF;
    INSERT INTO app.workout_timezone_write_capabilities(backend_pid,transaction_id,account_id,workout_id)
    VALUES(pg_backend_pid(),txid_current(),target_account_id,target_workout_id);
    UPDATE app.workouts SET timezone_name=new_timezone_name,timezone_source=new_timezone_source,
        timezone_reference_workout_id=new_reference_workout_id,timezone_dataset_release=new_dataset_release
     WHERE account_id=target_account_id AND id=target_workout_id
       AND (timezone_name,timezone_source,timezone_reference_workout_id,timezone_dataset_release)
           IS DISTINCT FROM (new_timezone_name,new_timezone_source,new_reference_workout_id,new_dataset_release);
    GET DIAGNOSTICS changed=ROW_COUNT;
    DELETE FROM app.workout_timezone_write_capabilities
     WHERE backend_pid=pg_backend_pid() AND transaction_id=txid_current();
    RETURN changed=1;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.set_workout_route_timezone(
    target_account_id uuid,target_workout_id uuid,new_timezone_name text,new_dataset_release text
) RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,app
AS $$
BEGIN
    IF (new_timezone_name IS NULL)<>(new_dataset_release IS NULL) OR
       (new_timezone_name IS NOT NULL AND (new_timezone_name !~ '^[A-Za-z0-9._+-]+(/[A-Za-z0-9._+-]+)+$'
        OR length(new_timezone_name)>255 OR length(new_dataset_release) NOT BETWEEN 1 AND 128)) THEN
        RAISE EXCEPTION 'invalid route timezone derivation' USING ERRCODE='22023';
    END IF;
    RETURN app.apply_workout_timezone(target_account_id,target_workout_id,new_timezone_name,
        CASE WHEN new_timezone_name IS NULL THEN NULL ELSE 'route_boundary' END,NULL,new_dataset_release);
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.reconcile_account_workout_timezones_internal(target_account_id uuid)
RETURNS integer
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,app
AS $$
DECLARE resolution record; changed integer:=0;
BEGIN
    FOR resolution IN
        SELECT target.id,seed.id AS seed_id,seed.timezone_name,seed.timezone_dataset_release
          FROM app.workouts target
          LEFT JOIN LATERAL (
              SELECT candidate.id,candidate.timezone_name,candidate.timezone_dataset_release
                FROM app.workouts candidate
               WHERE candidate.account_id=target.account_id
                 AND candidate.timezone_source='route_boundary'
                 AND candidate.start_offset_minutes=target.start_offset_minutes
                 AND candidate.deletion_requested_at IS NULL
               ORDER BY abs(extract(epoch FROM candidate.started_at-target.started_at)),candidate.started_at,candidate.id
               LIMIT 1
          ) seed ON true
         WHERE target.account_id=target_account_id AND target.timezone_source IS DISTINCT FROM 'route_boundary'
           AND target.deletion_requested_at IS NULL
    LOOP
        IF app.apply_workout_timezone(target_account_id,resolution.id,resolution.timezone_name,
            CASE WHEN resolution.seed_id IS NULL THEN NULL ELSE 'nearest_route_boundary' END,
            resolution.seed_id,resolution.timezone_dataset_release) THEN
            changed:=changed+1;
        END IF;
    END LOOP;
    RETURN changed;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.reconcile_account_workout_timezones(
    target_account_id uuid,target_job_id uuid,claiming_worker text,current_lease_token uuid
) RETURNS integer
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,app
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM app.jobs job
         WHERE job.id=target_job_id AND job.account_id=target_account_id AND job.status='running'
           AND job.worker_id=claiming_worker AND job.lease_token=current_lease_token
           AND job.lease_expires_at>=clock_timestamp()
           AND job.kind IN ('manual_ingest_source','scheduled_ingest_source','workout_deletion')
    ) THEN
        RAISE EXCEPTION 'timezone reconciliation requires a live job lease' USING ERRCODE='42501';
    END IF;
    RETURN app.reconcile_account_workout_timezones_internal(target_account_id);
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.read_workout_timezone_backfill(
    after_account_id uuid,after_workout_id uuid,batch_size integer
) RETURNS TABLE(account_id uuid,workout_id uuid,started_at timestamptz,start_offset_minutes smallint,
                longitude double precision,latitude double precision)
LANGUAGE sql
SECURITY DEFINER
STABLE
SET search_path=pg_catalog,app
AS $$
    SELECT workout.account_id,workout.id,workout.started_at,workout.start_offset_minutes,point.longitude,point.latitude
      FROM app.workouts workout
      JOIN LATERAL (
          SELECT route.longitude,route.latitude FROM app.workout_route_points route
           WHERE route.account_id=workout.account_id AND route.workout_id=workout.id
           ORDER BY route.sequence LIMIT 1
      ) point ON true
     WHERE workout.deletion_requested_at IS NULL
       AND (after_account_id IS NULL OR (workout.account_id,workout.id)>(after_account_id,after_workout_id))
     ORDER BY workout.account_id,workout.id
     LIMIT greatest(1,least(coalesce(batch_size,250),1000))
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.reconcile_all_workout_timezones()
RETURNS integer
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,app
AS $$
DECLARE account_row record; changed integer:=0;
BEGIN
    FOR account_row IN SELECT DISTINCT account_id FROM app.workouts LOOP
        changed:=changed+app.reconcile_account_workout_timezones_internal(account_row.account_id);
    END LOOP;
    RETURN changed;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.clear_workout_timezone_write_capability() FROM PUBLIC;
REVOKE ALL ON FUNCTION app.apply_workout_timezone(uuid,uuid,text,text,uuid,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION app.set_workout_route_timezone(uuid,uuid,text,text) FROM PUBLIC;
REVOKE ALL ON FUNCTION app.reconcile_account_workout_timezones_internal(uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION app.reconcile_account_workout_timezones(uuid,uuid,text,uuid) FROM PUBLIC;
REVOKE ALL ON FUNCTION app.read_workout_timezone_backfill(uuid,uuid,integer) FROM PUBLIC;
REVOKE ALL ON FUNCTION app.reconcile_all_workout_timezones() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.reconcile_account_workout_timezones(uuid,uuid,text,uuid) TO workouts_worker;
GRANT EXECUTE ON FUNCTION app.set_workout_route_timezone(uuid,uuid,text,text) TO workouts_migration;
GRANT EXECUTE ON FUNCTION app.read_workout_timezone_backfill(uuid,uuid,integer) TO workouts_migration;
GRANT EXECUTE ON FUNCTION app.reconcile_all_workout_timezones() TO workouts_migration;
GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.clear_workout_timezone_write_capability() OWNER TO workouts_security_owner;
ALTER FUNCTION app.apply_workout_timezone(uuid,uuid,text,text,uuid,text) OWNER TO workouts_security_owner;
ALTER FUNCTION app.set_workout_route_timezone(uuid,uuid,text,text) OWNER TO workouts_security_owner;
ALTER FUNCTION app.reconcile_account_workout_timezones_internal(uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.reconcile_account_workout_timezones(uuid,uuid,text,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.read_workout_timezone_backfill(uuid,uuid,integer) OWNER TO workouts_security_owner;
ALTER FUNCTION app.reconcile_all_workout_timezones() OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;

UPDATE app.schema_metadata SET schema_version=12,minimum_runtime_version=11 WHERE singleton;

-- +goose Down
SELECT app.assert_no_active_manual_ingest();
SELECT app.assert_no_active_scheduled_ingest();
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM app.jobs WHERE kind='workout_deletion' AND status='running') THEN
        RAISE EXCEPTION 'cannot downgrade while workout deletion jobs are running';
    END IF;
END;
$$;
-- +goose StatementEnd
UPDATE app.schema_metadata SET schema_version=11,minimum_runtime_version=8 WHERE singleton;

-- Restore the schema-11 ingest trigger before removing timezone capability objects.
-- +goose StatementBegin
DO $$
DECLARE prior_definition text;
BEGIN
    SELECT definition INTO prior_definition FROM app.timezone_migration_function_backup
     WHERE function_name='require_ingest_write_capability';
    IF prior_definition IS NULL THEN
        RAISE EXCEPTION 'timezone trigger backup is unavailable';
    END IF;
    EXECUTE prior_definition;
END;
$$;
-- +goose StatementEnd

DROP FUNCTION app.reconcile_all_workout_timezones();
DROP FUNCTION app.read_workout_timezone_backfill(uuid,uuid,integer);
DROP FUNCTION app.reconcile_account_workout_timezones(uuid,uuid,text,uuid);
DROP FUNCTION app.reconcile_account_workout_timezones_internal(uuid);
DROP FUNCTION app.set_workout_route_timezone(uuid,uuid,text,text);
DROP FUNCTION app.apply_workout_timezone(uuid,uuid,text,text,uuid,text);
DROP TRIGGER workout_timezone_write_capability_cleanup ON app.workout_timezone_write_capabilities;
DROP FUNCTION app.clear_workout_timezone_write_capability();
DROP POLICY workout_timezone_capabilities_owner_policy ON app.workout_timezone_write_capabilities;
DROP TABLE app.workout_timezone_write_capabilities;
DROP TABLE app.timezone_migration_function_backup;
DROP INDEX app.workouts_timezone_seed_idx;
ALTER TABLE app.workouts NO FORCE ROW LEVEL SECURITY;
ALTER TABLE app.workouts DISABLE TRIGGER workouts_capability_before_write;
UPDATE app.workouts SET timezone_name=NULL,timezone_source=NULL,
    timezone_reference_workout_id=NULL,timezone_dataset_release=NULL
 WHERE timezone_source IN ('route_boundary','nearest_route_boundary');
ALTER TABLE app.workouts ENABLE TRIGGER workouts_capability_before_write;
ALTER TABLE app.workouts FORCE ROW LEVEL SECURITY;
ALTER TABLE app.workouts
    DROP CONSTRAINT workouts_timezone_derivation_check,
    DROP COLUMN timezone_reference_workout_id,
    DROP COLUMN timezone_dataset_release;
