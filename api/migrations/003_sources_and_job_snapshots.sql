-- +goose Up
CREATE TABLE app.sources (
    id uuid PRIMARY KEY,
    account_id uuid NOT NULL REFERENCES app.accounts(id) ON DELETE CASCADE,
    display_name text NOT NULL CHECK (length(display_name) BETWEEN 1 AND 200),
    canonical_display_name text COLLATE "C" NOT NULL CHECK (length(canonical_display_name) BETWEEN 1 AND 200),
    type text NOT NULL CHECK (type IN ('health-auto-export-local')),
    auto_sync_enabled boolean NOT NULL DEFAULT false,
    status text NOT NULL DEFAULT 'checking-connection' CHECK (status IN (
        'checking-connection', 'connected', 'connection-failed'
    )),
    generation bigint NOT NULL DEFAULT 1 CHECK (generation > 0),
    config_envelope bytea,
    status_code text CHECK (status_code IS NULL OR length(status_code) <= 64),
    status_summary text CHECK (status_summary IS NULL OR length(status_summary) <= 512),
    checked_at timestamptz,
    deleted_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    UNIQUE (id, account_id),
    CHECK ((deleted_at IS NULL AND config_envelope IS NOT NULL AND octet_length(config_envelope) > 0) OR
           (deleted_at IS NOT NULL AND config_envelope IS NULL AND NOT auto_sync_enabled))
);
CREATE UNIQUE INDEX sources_active_canonical_display_name_idx
    ON app.sources (account_id, canonical_display_name) WHERE deleted_at IS NULL;
CREATE INDEX sources_auto_sync_idx
    ON app.sources (account_id, id) WHERE deleted_at IS NULL AND auto_sync_enabled AND status = 'connected';

ALTER TABLE app.sources ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.sources FORCE ROW LEVEL SECURITY;
CREATE POLICY sources_account_policy ON app.sources
    USING (account_id = app.current_account_id() AND deleted_at IS NULL)
    WITH CHECK (account_id = app.current_account_id());
-- Completion must lock the source before its job, even when API deletion has
-- already tombstoned the source. This role is NOLOGIN and only runs fixed functions.
CREATE POLICY sources_completion_policy ON app.sources TO workouts_security_owner
    USING (account_id = app.current_account_id());

ALTER TABLE app.jobs ADD CONSTRAINT jobs_id_account_unique UNIQUE (id, account_id);

CREATE TABLE app.job_config_snapshots (
    job_id uuid PRIMARY KEY,
    account_id uuid NOT NULL,
    source_id uuid NOT NULL,
    source_generation bigint NOT NULL CHECK (source_generation > 0),
    config_envelope bytea NOT NULL CHECK (octet_length(config_envelope) > 0),
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    FOREIGN KEY (job_id, account_id) REFERENCES app.jobs(id, account_id) ON DELETE CASCADE,
    FOREIGN KEY (source_id, account_id) REFERENCES app.sources(id, account_id) ON DELETE RESTRICT
);
CREATE INDEX job_config_snapshots_source_idx ON app.job_config_snapshots (account_id, source_id);

ALTER TABLE app.job_config_snapshots ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.job_config_snapshots FORCE ROW LEVEL SECURITY;
CREATE POLICY job_config_snapshots_account_policy ON app.job_config_snapshots
    USING (account_id = app.current_account_id())
    WITH CHECK (account_id = app.current_account_id());

-- +goose StatementBegin
CREATE FUNCTION app.enforce_source_write()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.generation <> 1 OR NEW.deleted_at IS NOT NULL THEN
            RAISE EXCEPTION 'sources must be inserted as generation one and active' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.id <> NEW.id OR OLD.account_id <> NEW.account_id OR OLD.type <> NEW.type OR OLD.created_at <> NEW.created_at THEN
        RAISE EXCEPTION 'immutable source fields changed' USING ERRCODE = '23514';
    END IF;
    IF (OLD.display_name IS DISTINCT FROM NEW.display_name) <>
       (OLD.canonical_display_name IS DISTINCT FROM NEW.canonical_display_name) THEN
        RAISE EXCEPTION 'display name and canonical display name must change together' USING ERRCODE = '23514';
    END IF;
    IF OLD.deleted_at IS NOT NULL THEN
        RAISE EXCEPTION 'deleted sources are immutable' USING ERRCODE = '23514';
    END IF;
    IF NEW.generation <> OLD.generation AND
       (OLD.generation = 9223372036854775807 OR NEW.generation <> OLD.generation + 1) THEN
        RAISE EXCEPTION 'source generation must remain unchanged or increment once' USING ERRCODE = '23514';
    END IF;
    NEW.updated_at := transaction_timestamp();
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER sources_write_before
BEFORE INSERT OR UPDATE ON app.sources
FOR EACH ROW EXECUTE FUNCTION app.enforce_source_write();

-- +goose StatementBegin
CREATE FUNCTION app.enforce_job_snapshot_consistency()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    target_job_id uuid;
    target_account_id uuid;
    target_source_id uuid;
    target_generation bigint;
    target_kind text;
    target_status text;
    source_account_id uuid;
    source_generation_value bigint;
    source_deleted_at timestamptz;
    snapshot_exists boolean;
BEGIN
    IF TG_TABLE_NAME = 'jobs' THEN
        target_job_id := NEW.id;
    ELSIF TG_TABLE_NAME = 'job_config_snapshots' THEN
        target_job_id := NEW.job_id;
    ELSE
        RAISE EXCEPTION 'unsupported snapshot consistency trigger table %', TG_TABLE_NAME USING ERRCODE = '55000';
    END IF;
    SELECT account_id, kind, status INTO target_account_id, target_kind, target_status
      FROM app.jobs WHERE id = target_job_id;
    SELECT EXISTS (SELECT 1 FROM app.job_config_snapshots WHERE job_id = target_job_id)
      INTO snapshot_exists;

    IF target_kind IN ('source_connection_check', 'manual_ingest_source', 'scheduled_ingest_source')
       AND target_status IN ('queued', 'running') AND NOT snapshot_exists THEN
        RAISE EXCEPTION 'active source job requires a config snapshot' USING ERRCODE = '23514';
    END IF;
    IF (target_kind NOT IN ('source_connection_check', 'manual_ingest_source', 'scheduled_ingest_source') OR
        target_status IN ('succeeded', 'partially_succeeded', 'failed', 'cancelled')) AND snapshot_exists THEN
        RAISE EXCEPTION 'job must not retain a config snapshot' USING ERRCODE = '23514';
    END IF;
    IF NOT snapshot_exists THEN
        RETURN NULL;
    END IF;

    -- Current source state is a creation-time fence. Existing immutable snapshots
    -- deliberately survive later source updates and cooperative deletion cleanup.
    IF TG_TABLE_NAME = 'job_config_snapshots' THEN
        SELECT source_id, source_generation INTO target_source_id, target_generation
          FROM app.job_config_snapshots WHERE job_id = target_job_id;
        SELECT account_id, generation, deleted_at
          INTO source_account_id, source_generation_value, source_deleted_at
          FROM app.sources WHERE id = target_source_id;
        IF target_account_id IS NULL OR source_account_id IS DISTINCT FROM target_account_id OR
           source_deleted_at IS NOT NULL OR source_generation_value IS DISTINCT FROM target_generation THEN
            RAISE EXCEPTION 'job config snapshot is inconsistent with its source and job' USING ERRCODE = '23514';
        END IF;
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER jobs_snapshot_consistency
AFTER INSERT OR UPDATE OF account_id, kind, status ON app.jobs
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION app.enforce_job_snapshot_consistency();
CREATE CONSTRAINT TRIGGER job_config_snapshots_consistency
AFTER INSERT OR UPDATE ON app.job_config_snapshots
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION app.enforce_job_snapshot_consistency();

-- +goose StatementBegin
CREATE FUNCTION app.reject_job_snapshot_update()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    RAISE EXCEPTION 'job config snapshots are immutable' USING ERRCODE = '23514';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER job_config_snapshots_immutable
BEFORE UPDATE ON app.job_config_snapshots
FOR EACH ROW EXECUTE FUNCTION app.reject_job_snapshot_update();

-- +goose StatementBegin
CREATE FUNCTION app.read_job_config_snapshot(job_id uuid, claiming_worker text, current_lease_token uuid)
RETURNS TABLE(source_id uuid, source_generation bigint, config_envelope bytea)
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
    SELECT snapshot.source_id, snapshot.source_generation, snapshot.config_envelope
      FROM app.job_config_snapshots snapshot
      JOIN app.jobs job ON job.id = snapshot.job_id AND job.account_id = snapshot.account_id
     WHERE snapshot.job_id = read_job_config_snapshot.job_id
       AND snapshot.account_id = app.current_account_id()
       AND job.status = 'running'
       AND job.worker_id = claiming_worker
       AND job.lease_token = current_lease_token
       AND job.lease_expires_at >= transaction_timestamp()
$$;
-- +goose StatementEnd

-- This function must be owned by the jobs table owner so it can discover work
-- before an account RLS context is known. Its fixed query exposes one account.
-- +goose StatementBegin
CREATE FUNCTION app.claim_next_source_connection_check(claiming_worker text, new_lease_token uuid, lease_duration interval)
RETURNS TABLE(job_id uuid, account_id uuid)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    candidate_job_id uuid;
    candidate_account_id uuid;
    candidate_status text;
BEGIN
    IF claiming_worker = '' OR new_lease_token IS NULL OR lease_duration <= interval '0 seconds' THEN
        RAISE EXCEPTION 'invalid claim arguments' USING ERRCODE = '22023';
    END IF;
    LOOP
        SELECT job.id, job.account_id, job.status
          INTO candidate_job_id, candidate_account_id, candidate_status
          FROM app.jobs job
         WHERE job.kind = 'source_connection_check' AND
               ((job.status = 'queued' AND job.cancel_requested_at IS NULL) OR
                (job.status = 'running' AND job.lease_expires_at < transaction_timestamp()))
         ORDER BY job.priority DESC, job.created_at, job.id
         FOR UPDATE SKIP LOCKED
         LIMIT 1;
        IF NOT FOUND THEN RETURN; END IF;
        PERFORM set_config('app.account_id', candidate_account_id::text, true);
        IF candidate_status = 'running' AND NOT app.recover_expired_job(candidate_job_id) THEN
            CONTINUE;
        END IF;
        IF app.claim_job(candidate_job_id, claiming_worker, new_lease_token, lease_duration) THEN
            job_id := candidate_job_id;
            account_id := candidate_account_id;
            RETURN NEXT;
            RETURN;
        END IF;
    END LOOP;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.complete_source_connection_check(job_id uuid, claiming_worker text, current_lease_token uuid,
    result_status text, status_code_value text DEFAULT NULL, status_summary_value text DEFAULT NULL)
RETURNS TABLE(finished boolean, source_updated boolean)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    snapshot_source_id uuid;
    snapshot_generation bigint;
    source_generation_value bigint;
    source_deleted_at timestamptz;
    cancellation_pending boolean;
    terminal_status text;
BEGIN
    IF result_status NOT IN ('connected', 'connection-failed') OR
       (result_status = 'connected' AND (status_code_value IS NOT NULL OR status_summary_value IS NOT NULL)) OR
       (result_status = 'connection-failed' AND
        (status_code_value IS NULL OR length(status_code_value) > 64 OR
         status_summary_value IS NULL OR length(status_summary_value) > 512)) THEN
        RAISE EXCEPTION 'invalid source connection result' USING ERRCODE = '22023';
    END IF;
    SELECT snapshot.source_id, snapshot.source_generation
      INTO snapshot_source_id, snapshot_generation
      FROM app.job_config_snapshots snapshot
     WHERE snapshot.job_id = complete_source_connection_check.job_id
       AND snapshot.account_id = app.current_account_id();
    IF NOT FOUND THEN
        finished := false;
        source_updated := false;
        RETURN NEXT;
        RETURN;
    END IF;
    -- API deletion locks source then job. Include tombstones here so a completion
    -- queued behind deletion observes its cancellation fence without reversing order.
    SELECT source.generation, source.deleted_at
      INTO source_generation_value, source_deleted_at
      FROM app.sources source
     WHERE source.id = snapshot_source_id
       AND source.account_id = app.current_account_id()
     FOR UPDATE;
    IF NOT FOUND THEN
        finished := false;
        source_updated := false;
        RETURN NEXT;
        RETURN;
    END IF;
    SELECT job.cancel_requested_at IS NOT NULL
      INTO cancellation_pending
      FROM app.jobs job
     WHERE job.id = complete_source_connection_check.job_id
       AND job.account_id = app.current_account_id()
       AND job.kind = 'source_connection_check' AND job.status = 'running'
       AND job.worker_id = claiming_worker AND job.lease_token = current_lease_token
       AND job.lease_expires_at >= clock_timestamp()
     FOR UPDATE;
    IF NOT FOUND THEN
        finished := false;
        source_updated := false;
        RETURN NEXT;
        RETURN;
    END IF;
    source_updated := false;
    IF NOT cancellation_pending AND source_deleted_at IS NULL AND
       source_generation_value = snapshot_generation THEN
        UPDATE app.sources source SET status = result_status,
            status_code = status_code_value, status_summary = status_summary_value,
            checked_at = transaction_timestamp()
         WHERE source.id = snapshot_source_id AND source.account_id = app.current_account_id()
           AND source.generation = snapshot_generation AND source.deleted_at IS NULL
           AND source.status = 'checking-connection';
        source_updated := FOUND;
    END IF;
    terminal_status := CASE WHEN cancellation_pending THEN 'cancelled'
                            WHEN result_status = 'connected' THEN 'succeeded'
                            ELSE 'failed' END;
    finished := app.finish_job(complete_source_connection_check.job_id, claiming_worker,
        current_lease_token, terminal_status,
        CASE WHEN terminal_status = 'failed' THEN status_code_value ELSE NULL END,
        CASE WHEN terminal_status = 'failed' THEN status_summary_value ELSE NULL END);
    RETURN NEXT;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.heartbeat_job(job_id uuid, claiming_worker text, current_lease_token uuid, lease_duration interval)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
BEGIN
    IF lease_duration <= interval '0 seconds' THEN RAISE EXCEPTION 'invalid lease duration' USING ERRCODE = '22023'; END IF;
    PERFORM set_config('app.job_transition', 'heartbeat', true);
    UPDATE app.jobs SET heartbeat_at = clock_timestamp(), lease_expires_at = clock_timestamp() + lease_duration
    WHERE id = job_id AND account_id = app.current_account_id()
      AND status = 'running' AND worker_id = claiming_worker AND lease_token = current_lease_token
      AND lease_expires_at >= clock_timestamp();
    RETURN FOUND;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.finish_job(job_id uuid, claiming_worker text, current_lease_token uuid, terminal_status text,
    failure_code_value text DEFAULT NULL, failure_summary_value text DEFAULT NULL)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE parent_id uuid;
BEGIN
    IF terminal_status NOT IN ('succeeded', 'failed', 'cancelled') THEN RAISE EXCEPTION 'invalid terminal status' USING ERRCODE = '22023'; END IF;
    PERFORM 1 FROM app.jobs WHERE id = job_id AND account_id = app.current_account_id()
      AND status = 'running' AND worker_id = claiming_worker AND lease_token = current_lease_token
      AND lease_expires_at >= clock_timestamp() FOR UPDATE;
    IF NOT FOUND THEN RETURN false; END IF;
    DELETE FROM app.job_config_snapshots snapshot WHERE snapshot.job_id = finish_job.job_id;
    PERFORM set_config('app.job_transition', 'finish', true);
    UPDATE app.jobs SET status = terminal_status, terminal_at = now(), worker_id = NULL, lease_token = NULL,
        claimed_at = NULL, heartbeat_at = NULL, lease_expires_at = NULL,
        failure_code = failure_code_value, failure_summary = failure_summary_value
    WHERE id = job_id AND account_id = app.current_account_id()
      AND status = 'running' AND worker_id = claiming_worker AND lease_token = current_lease_token
    RETURNING parent_job_id INTO parent_id;
    IF NOT FOUND THEN RETURN false; END IF;
    IF parent_id IS NOT NULL THEN PERFORM app.derive_parent_status(parent_id); END IF;
    RETURN true;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.recover_expired_job(job_id uuid)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE parent_id uuid; cancellation_pending boolean;
BEGIN
    SELECT cancel_requested_at IS NOT NULL INTO cancellation_pending FROM app.jobs
    WHERE id = job_id AND account_id = app.current_account_id() AND status = 'running' AND lease_expires_at < now()
    FOR UPDATE;
    IF NOT FOUND THEN RETURN false; END IF;
    IF cancellation_pending THEN
        DELETE FROM app.job_config_snapshots snapshot WHERE snapshot.job_id = recover_expired_job.job_id;
    END IF;
    PERFORM set_config('app.job_transition', 'recover', true);
    UPDATE app.jobs SET status = CASE WHEN cancel_requested_at IS NULL THEN 'queued' ELSE 'cancelled' END,
        terminal_at = CASE WHEN cancel_requested_at IS NULL THEN NULL ELSE now() END,
        worker_id = NULL, lease_token = NULL, claimed_at = NULL, heartbeat_at = NULL, lease_expires_at = NULL
    WHERE id = job_id AND account_id = app.current_account_id() AND status = 'running' AND lease_expires_at < now()
    RETURNING parent_job_id INTO parent_id;
    IF NOT FOUND THEN RETURN false; END IF;
    IF parent_id IS NOT NULL THEN PERFORM app.derive_parent_status(parent_id); END IF;
    RETURN true;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.request_job_cancellation(job_id uuid, requester_id uuid)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE target_kind text; target_status text; parent_id uuid;
BEGIN
    IF requester_id IS NULL THEN RAISE EXCEPTION 'requester is required' USING ERRCODE = '22023'; END IF;
    SELECT kind, status, parent_job_id INTO target_kind, target_status, parent_id FROM app.jobs
    WHERE id = job_id AND account_id = app.current_account_id() FOR UPDATE;
    IF NOT FOUND OR target_status IN ('succeeded', 'partially_succeeded', 'failed', 'cancelled') THEN RETURN false; END IF;
    PERFORM set_config('app.job_transition', 'cancel', true);
    IF target_kind IN ('manual_ingest', 'scheduled_ingest') THEN
        UPDATE app.jobs SET cancel_requested_at = COALESCE(cancel_requested_at, now()), cancel_requested_by = COALESCE(cancel_requested_by, requester_id)
        WHERE id = job_id AND account_id = app.current_account_id();
        DELETE FROM app.job_config_snapshots snapshot USING app.jobs child
        WHERE child.parent_job_id = request_job_cancellation.job_id AND child.account_id = app.current_account_id()
          AND child.status = 'queued' AND snapshot.job_id = child.id;
        UPDATE app.jobs SET status = 'cancelled', terminal_at = now(), cancel_requested_at = now(), cancel_requested_by = requester_id
        WHERE parent_job_id = job_id AND account_id = app.current_account_id() AND status = 'queued';
        UPDATE app.jobs SET cancel_requested_at = COALESCE(cancel_requested_at, now()), cancel_requested_by = COALESCE(cancel_requested_by, requester_id)
        WHERE parent_job_id = job_id AND account_id = app.current_account_id() AND status = 'running';
        PERFORM app.derive_parent_status(job_id);
    ELSE
        IF target_status = 'queued' THEN
            DELETE FROM app.job_config_snapshots snapshot WHERE snapshot.job_id = request_job_cancellation.job_id;
        END IF;
        UPDATE app.jobs SET status = CASE WHEN status = 'queued' THEN 'cancelled' ELSE status END,
            terminal_at = CASE WHEN status = 'queued' THEN now() ELSE terminal_at END,
            cancel_requested_at = COALESCE(cancel_requested_at, now()), cancel_requested_by = COALESCE(cancel_requested_by, requester_id)
        WHERE id = job_id AND account_id = app.current_account_id() AND status IN ('queued', 'running');
        IF parent_id IS NOT NULL THEN PERFORM app.derive_parent_status(parent_id); END IF;
    END IF;
    RETURN true;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.delete_source(source_id uuid, requester_id uuid)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    parent_to_lock uuid;
    job_to_lock uuid;
    active_job_ids uuid[] := ARRAY[]::uuid[];
BEGIN
    IF requester_id IS NULL THEN RAISE EXCEPTION 'requester is required' USING ERRCODE = '22023'; END IF;
    PERFORM 1 FROM app.sources source
     WHERE source.id = delete_source.source_id AND source.account_id = app.current_account_id()
       AND source.deleted_at IS NULL
     FOR UPDATE;
    IF NOT FOUND THEN RETURN false; END IF;

    FOR parent_to_lock IN
        SELECT DISTINCT job.parent_job_id FROM app.jobs job
        JOIN app.job_config_snapshots snapshot ON snapshot.job_id = job.id AND snapshot.account_id = job.account_id
        WHERE snapshot.source_id = delete_source.source_id AND snapshot.account_id = app.current_account_id()
          AND job.status IN ('queued', 'running') AND job.parent_job_id IS NOT NULL
        ORDER BY job.parent_job_id
    LOOP
        PERFORM 1 FROM app.jobs parent
         WHERE parent.id = parent_to_lock AND parent.account_id = app.current_account_id()
         FOR UPDATE;
    END LOOP;
    FOR job_to_lock IN
        SELECT job.id FROM app.jobs job
        JOIN app.job_config_snapshots snapshot ON snapshot.job_id = job.id AND snapshot.account_id = job.account_id
        WHERE snapshot.source_id = delete_source.source_id AND snapshot.account_id = app.current_account_id()
          AND job.status IN ('queued', 'running')
        ORDER BY job.id
    LOOP
        PERFORM 1 FROM app.jobs job
         WHERE job.id = job_to_lock AND job.account_id = app.current_account_id()
         FOR UPDATE;
        active_job_ids := array_append(active_job_ids, job_to_lock);
    END LOOP;
    FOREACH job_to_lock IN ARRAY active_job_ids LOOP
        PERFORM app.request_job_cancellation(job_to_lock, requester_id);
    END LOOP;

    UPDATE app.sources source SET auto_sync_enabled = false, config_envelope = NULL,
        deleted_at = transaction_timestamp()
     WHERE source.id = delete_source.source_id AND source.account_id = app.current_account_id()
       AND source.deleted_at IS NULL;
    RETURN FOUND;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON app.sources, app.job_config_snapshots FROM PUBLIC, workouts_api, workouts_worker;
REVOKE ALL ON FUNCTION app.read_job_config_snapshot(uuid,text,uuid) FROM PUBLIC, workouts_api, workouts_worker;
REVOKE ALL ON FUNCTION app.claim_next_source_connection_check(text,uuid,interval) FROM PUBLIC, workouts_api, workouts_worker;
REVOKE ALL ON FUNCTION app.complete_source_connection_check(uuid,text,uuid,text,text,text) FROM PUBLIC, workouts_api, workouts_worker;
REVOKE ALL ON FUNCTION app.delete_source(uuid,uuid) FROM PUBLIC, workouts_api, workouts_worker;

GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.enforce_job_snapshot_consistency() OWNER TO workouts_security_owner;
ALTER FUNCTION app.read_job_config_snapshot(uuid,text,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.complete_source_connection_check(uuid,text,uuid,text,text,text) OWNER TO workouts_security_owner;
ALTER FUNCTION app.finish_job(uuid,text,uuid,text,text,text) OWNER TO workouts_security_owner;
ALTER FUNCTION app.recover_expired_job(uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.request_job_cancellation(uuid,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.delete_source(uuid,uuid) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
GRANT USAGE ON SCHEMA app TO workouts_security_owner;
GRANT SELECT ON app.jobs, app.sources, app.job_config_snapshots TO workouts_security_owner;
GRANT UPDATE ON app.jobs TO workouts_security_owner;
GRANT UPDATE (auto_sync_enabled,config_envelope,deleted_at,status,status_code,status_summary,checked_at,updated_at)
    ON app.sources TO workouts_security_owner;
GRANT DELETE ON app.job_config_snapshots TO workouts_security_owner;
GRANT EXECUTE ON FUNCTION app.current_account_id(), app.derive_parent_status(uuid) TO workouts_security_owner;

GRANT SELECT (id,account_id,display_name,canonical_display_name,type,auto_sync_enabled,status,generation,config_envelope,
    status_code,status_summary,checked_at,created_at,updated_at) ON app.sources TO workouts_api;
GRANT INSERT (id,account_id,display_name,canonical_display_name,type,auto_sync_enabled,status,generation,config_envelope,
    status_code,status_summary,checked_at) ON app.sources TO workouts_api;
GRANT UPDATE (display_name,canonical_display_name,auto_sync_enabled,status,generation,config_envelope,status_code,status_summary,checked_at,updated_at)
    ON app.sources TO workouts_api;
GRANT INSERT (job_id,account_id,source_id,source_generation,config_envelope) ON app.job_config_snapshots TO workouts_api;
GRANT SELECT (job_id,account_id,source_id,source_generation) ON app.job_config_snapshots TO workouts_api;
GRANT EXECUTE ON FUNCTION app.read_job_config_snapshot(uuid,text,uuid) TO workouts_worker;
GRANT EXECUTE ON FUNCTION app.claim_next_source_connection_check(text,uuid,interval) TO workouts_worker;
GRANT EXECUTE ON FUNCTION app.complete_source_connection_check(uuid,text,uuid,text,text,text) TO workouts_worker;
GRANT EXECUTE ON FUNCTION app.delete_source(uuid,uuid) TO workouts_api;

UPDATE app.schema_metadata SET schema_version = 3, minimum_runtime_version = 1;

-- +goose Down
UPDATE app.schema_metadata SET schema_version = 2, minimum_runtime_version = 1;

ALTER FUNCTION app.finish_job(uuid,text,uuid,text,text,text) OWNER TO workouts_migration;
ALTER FUNCTION app.recover_expired_job(uuid) OWNER TO workouts_migration;
ALTER FUNCTION app.request_job_cancellation(uuid,uuid) OWNER TO workouts_migration;
ALTER FUNCTION app.delete_source(uuid,uuid) OWNER TO workouts_migration;
DROP FUNCTION app.delete_source(uuid,uuid);
REVOKE SELECT, UPDATE ON app.jobs FROM workouts_security_owner;
REVOKE UPDATE (auto_sync_enabled,config_envelope,deleted_at,status,status_code,status_summary,checked_at,updated_at)
    ON app.sources FROM workouts_security_owner;
REVOKE EXECUTE ON FUNCTION app.current_account_id(), app.derive_parent_status(uuid) FROM workouts_security_owner;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.heartbeat_job(job_id uuid, claiming_worker text, current_lease_token uuid, lease_duration interval)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $$
BEGIN
    IF lease_duration <= interval '0 seconds' THEN RAISE EXCEPTION 'invalid lease duration' USING ERRCODE = '22023'; END IF;
    PERFORM set_config('app.job_transition', 'heartbeat', true);
    UPDATE app.jobs SET heartbeat_at = now(), lease_expires_at = now() + lease_duration
    WHERE id = job_id AND account_id = app.current_account_id()
      AND status = 'running' AND worker_id = claiming_worker AND lease_token = current_lease_token;
    RETURN FOUND;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.finish_job(job_id uuid, claiming_worker text, current_lease_token uuid, terminal_status text,
    failure_code_value text DEFAULT NULL, failure_summary_value text DEFAULT NULL)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $$
DECLARE parent_id uuid;
BEGIN
    IF terminal_status NOT IN ('succeeded', 'failed', 'cancelled') THEN RAISE EXCEPTION 'invalid terminal status' USING ERRCODE = '22023'; END IF;
    PERFORM set_config('app.job_transition', 'finish', true);
    UPDATE app.jobs SET status = terminal_status, terminal_at = now(), worker_id = NULL, lease_token = NULL,
        claimed_at = NULL, heartbeat_at = NULL, lease_expires_at = NULL,
        failure_code = failure_code_value, failure_summary = failure_summary_value
    WHERE id = job_id AND account_id = app.current_account_id()
      AND status = 'running' AND worker_id = claiming_worker AND lease_token = current_lease_token
    RETURNING parent_job_id INTO parent_id;
    IF NOT FOUND THEN RETURN false; END IF;
    IF parent_id IS NOT NULL THEN PERFORM app.derive_parent_status(parent_id); END IF;
    RETURN true;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.recover_expired_job(job_id uuid)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $$
DECLARE parent_id uuid;
BEGIN
    PERFORM set_config('app.job_transition', 'recover', true);
    UPDATE app.jobs SET status = CASE WHEN cancel_requested_at IS NULL THEN 'queued' ELSE 'cancelled' END,
        terminal_at = CASE WHEN cancel_requested_at IS NULL THEN NULL ELSE now() END,
        worker_id = NULL, lease_token = NULL, claimed_at = NULL, heartbeat_at = NULL, lease_expires_at = NULL
    WHERE id = job_id AND account_id = app.current_account_id() AND status = 'running' AND lease_expires_at < now()
    RETURNING parent_job_id INTO parent_id;
    IF NOT FOUND THEN RETURN false; END IF;
    IF parent_id IS NOT NULL THEN PERFORM app.derive_parent_status(parent_id); END IF;
    RETURN true;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.request_job_cancellation(job_id uuid, requester_id uuid)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $$
DECLARE target_kind text; target_status text; parent_id uuid;
BEGIN
    IF requester_id IS NULL THEN RAISE EXCEPTION 'requester is required' USING ERRCODE = '22023'; END IF;
    SELECT kind, status, parent_job_id INTO target_kind, target_status, parent_id FROM app.jobs
    WHERE id = job_id AND account_id = app.current_account_id() FOR UPDATE;
    IF NOT FOUND OR target_status IN ('succeeded', 'partially_succeeded', 'failed', 'cancelled') THEN RETURN false; END IF;
    PERFORM set_config('app.job_transition', 'cancel', true);
    IF target_kind IN ('manual_ingest', 'scheduled_ingest') THEN
        UPDATE app.jobs SET cancel_requested_at = COALESCE(cancel_requested_at, now()), cancel_requested_by = COALESCE(cancel_requested_by, requester_id)
        WHERE id = job_id AND account_id = app.current_account_id();
        UPDATE app.jobs SET status = 'cancelled', terminal_at = now(), cancel_requested_at = now(), cancel_requested_by = requester_id
        WHERE parent_job_id = job_id AND account_id = app.current_account_id() AND status = 'queued';
        UPDATE app.jobs SET cancel_requested_at = COALESCE(cancel_requested_at, now()), cancel_requested_by = COALESCE(cancel_requested_by, requester_id)
        WHERE parent_job_id = job_id AND account_id = app.current_account_id() AND status = 'running';
        PERFORM app.derive_parent_status(job_id);
    ELSE
        UPDATE app.jobs SET status = CASE WHEN status = 'queued' THEN 'cancelled' ELSE status END,
            terminal_at = CASE WHEN status = 'queued' THEN now() ELSE terminal_at END,
            cancel_requested_at = COALESCE(cancel_requested_at, now()), cancel_requested_by = COALESCE(cancel_requested_by, requester_id)
        WHERE id = job_id AND account_id = app.current_account_id() AND status IN ('queued', 'running');
        IF parent_id IS NOT NULL THEN PERFORM app.derive_parent_status(parent_id); END IF;
    END IF;
    RETURN true;
END;
$$;
-- +goose StatementEnd

DROP FUNCTION app.complete_source_connection_check(uuid,text,uuid,text,text,text);
DROP FUNCTION app.claim_next_source_connection_check(text,uuid,interval);
DROP FUNCTION app.read_job_config_snapshot(uuid,text,uuid);
DROP FUNCTION app.reject_job_snapshot_update() CASCADE;
DROP FUNCTION app.enforce_job_snapshot_consistency() CASCADE;
DROP TABLE app.job_config_snapshots;
ALTER TABLE app.jobs DROP CONSTRAINT jobs_id_account_unique;
DROP FUNCTION app.enforce_source_write() CASCADE;
DROP TABLE app.sources;
