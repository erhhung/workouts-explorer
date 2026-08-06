-- +goose Up
REVOKE EXECUTE ON FUNCTION app.read_worker_job_log_context(uuid,text,uuid) FROM workouts_worker;
ALTER FUNCTION app.read_worker_job_log_context(uuid,text,uuid) OWNER TO workouts_migration;
DROP FUNCTION app.read_worker_job_log_context(uuid,text,uuid);

-- +goose StatementBegin
CREATE FUNCTION app.read_worker_job_log_context(job_id uuid, claiming_worker text, current_lease_token uuid)
RETURNS TABLE(owner_username text, source_name text, source_type text)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    target_account_id uuid;
    target_administrator_id uuid;
    prior_account_id text := current_setting('app.account_id', true);
BEGIN
    IF job_id IS NULL OR claiming_worker = '' OR current_lease_token IS NULL THEN
        RETURN;
    END IF;

    SELECT job.account_id, job.administrator_id
      INTO target_account_id, target_administrator_id
      FROM app.jobs job
     WHERE job.id = read_worker_job_log_context.job_id
       AND job.status = 'running'
       AND job.worker_id = claiming_worker
       AND job.lease_token = current_lease_token
       AND job.lease_expires_at >= clock_timestamp();
    IF NOT FOUND THEN RETURN; END IF;

    BEGIN
        IF target_account_id IS NOT NULL THEN
            PERFORM set_config('app.account_id', target_account_id::text, true);
            RETURN QUERY
            SELECT principal.username, source.display_name, source.type
              FROM app.users account_user
              JOIN app.authentication_principals principal ON principal.id = account_user.principal_id
              LEFT JOIN app.job_config_snapshots snapshot
                ON snapshot.job_id = read_worker_job_log_context.job_id
               AND snapshot.account_id = target_account_id
              LEFT JOIN app.sources source
                ON source.id = snapshot.source_id
               AND source.account_id = snapshot.account_id
             WHERE account_user.account_id = target_account_id;
        ELSE
            RETURN QUERY
            SELECT principal.username, NULL::text, NULL::text
              FROM app.administrators administrator
              JOIN app.authentication_principals principal ON principal.id = administrator.principal_id
             WHERE administrator.principal_id = target_administrator_id;
        END IF;
    EXCEPTION WHEN OTHERS THEN
        PERFORM set_config('app.account_id', COALESCE(prior_account_id, ''), true);
        RAISE;
    END;
    PERFORM set_config('app.account_id', COALESCE(prior_account_id, ''), true);
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.read_worker_job_log_context(uuid,text,uuid)
    FROM PUBLIC, workouts_api, workouts_worker;
GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.read_worker_job_log_context(uuid,text,uuid) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
GRANT EXECUTE ON FUNCTION app.read_worker_job_log_context(uuid,text,uuid) TO workouts_worker;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.clear_ingest_write_capability()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    snapshot_source_id uuid;
    parent_id uuid;
    child_kind text;
    expected_parent_kind text;
BEGIN
    -- The initial fence still holds source, parent, and child locks; recovery and
    -- cancellation cannot change ownership before commit after wall-clock expiry.
    IF NEW.backend_pid <> pg_backend_pid() OR NEW.transaction_id <> txid_current() THEN
        RAISE EXCEPTION 'ingest capability transaction changed before commit' USING ERRCODE = '40001';
    END IF;
    SELECT snapshot.source_id INTO snapshot_source_id
      FROM app.job_config_snapshots snapshot
     WHERE snapshot.job_id = NEW.job_id AND snapshot.account_id = NEW.account_id
       AND snapshot.source_id = NEW.source_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'ingest snapshot changed before commit' USING ERRCODE = '40001';
    END IF;

    PERFORM 1 FROM app.sources source
     WHERE source.id = NEW.source_id AND source.account_id = NEW.account_id
       AND source.deleted_at IS NULL
     FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'ingest source changed before commit' USING ERRCODE = '40001';
    END IF;

    SELECT job.parent_job_id, job.kind INTO parent_id, child_kind FROM app.jobs job
     WHERE job.id = NEW.job_id AND job.account_id = NEW.account_id
       AND job.kind IN ('manual_ingest_source', 'scheduled_ingest_source');
    IF NOT FOUND OR parent_id IS NULL THEN
        RAISE EXCEPTION 'ingest job changed before commit' USING ERRCODE = '40001';
    END IF;
    expected_parent_kind := CASE child_kind
        WHEN 'manual_ingest_source' THEN 'manual_ingest'
        WHEN 'scheduled_ingest_source' THEN 'scheduled_ingest'
    END;
    PERFORM 1 FROM app.jobs parent
     WHERE parent.id = parent_id AND parent.account_id = NEW.account_id
       AND parent.kind = expected_parent_kind AND parent.status = 'running'
       AND parent.cancel_requested_at IS NULL
     FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'ingest parent changed before commit' USING ERRCODE = '40001';
    END IF;

    PERFORM 1 FROM app.jobs job
     WHERE job.id = NEW.job_id AND job.account_id = NEW.account_id
       AND job.kind = child_kind AND job.status = 'running'
       AND job.cancel_requested_at IS NULL AND job.worker_id = NEW.worker_id
       AND job.lease_token = NEW.lease_token
     FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'ingest lease ownership changed before commit' USING ERRCODE = '40001';
    END IF;
    DELETE FROM app.ingest_write_capabilities capability
     WHERE capability.backend_pid = NEW.backend_pid
       AND capability.transaction_id = NEW.transaction_id;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.fence_ingest_job(job_id uuid, claiming_worker text, current_lease_token uuid)
RETURNS TABLE(source_id uuid)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    snapshot_source_id uuid;
    parent_id uuid;
    child_kind text;
    expected_parent_kind text;
BEGIN
    IF claiming_worker = '' OR current_lease_token IS NULL THEN
        RAISE EXCEPTION 'invalid ingest fence arguments' USING ERRCODE = '22023';
    END IF;
    SELECT snapshot.source_id INTO snapshot_source_id
      FROM app.job_config_snapshots snapshot
     WHERE snapshot.job_id = fence_ingest_job.job_id
       AND snapshot.account_id = app.current_account_id();
    IF NOT FOUND THEN RETURN; END IF;

    PERFORM 1 FROM app.sources source
     WHERE source.id = snapshot_source_id
       AND source.account_id = app.current_account_id()
       AND source.deleted_at IS NULL
     FOR UPDATE;
    IF NOT FOUND THEN RETURN; END IF;

    SELECT job.parent_job_id, job.kind INTO parent_id, child_kind FROM app.jobs job
     WHERE job.id = fence_ingest_job.job_id AND job.account_id = app.current_account_id()
       AND job.kind IN ('manual_ingest_source', 'scheduled_ingest_source');
    IF NOT FOUND OR parent_id IS NULL THEN RETURN; END IF;
    expected_parent_kind := CASE child_kind
        WHEN 'manual_ingest_source' THEN 'manual_ingest'
        WHEN 'scheduled_ingest_source' THEN 'scheduled_ingest'
    END;
    PERFORM 1 FROM app.jobs parent
     WHERE parent.id = parent_id AND parent.account_id = app.current_account_id()
       AND parent.kind = expected_parent_kind
     FOR UPDATE;
    IF NOT FOUND THEN RETURN; END IF;

    PERFORM 1 FROM app.jobs job
     WHERE job.id = fence_ingest_job.job_id
       AND job.account_id = app.current_account_id()
       AND job.kind = child_kind
       AND job.status = 'running'
       AND job.cancel_requested_at IS NULL
       AND job.worker_id = claiming_worker
       AND job.lease_token = current_lease_token
       AND job.lease_expires_at >= clock_timestamp()
     FOR UPDATE;
    IF NOT FOUND THEN RETURN; END IF;
    INSERT INTO app.ingest_write_capabilities
        (backend_pid, transaction_id, account_id, source_id, job_id, worker_id, lease_token)
    VALUES (pg_backend_pid(), txid_current(), app.current_account_id(), snapshot_source_id,
        fence_ingest_job.job_id, claiming_worker, current_lease_token)
    ON CONFLICT (backend_pid, transaction_id) DO NOTHING;
    PERFORM 1 FROM app.ingest_write_capabilities capability
     WHERE capability.backend_pid = pg_backend_pid() AND capability.transaction_id = txid_current()
       AND capability.account_id = app.current_account_id() AND capability.source_id = snapshot_source_id
       AND capability.job_id = fence_ingest_job.job_id AND capability.worker_id = claiming_worker
       AND capability.lease_token = current_lease_token;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'transaction already fenced for another ingest job' USING ERRCODE = '25001';
    END IF;
    source_id := snapshot_source_id;
    RETURN NEXT;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.claim_next_worker_job_internal(claiming_worker text, new_lease_token uuid,
    lease_duration interval, include_manual_ingest boolean)
RETURNS TABLE(job_id uuid, account_id uuid, kind text)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    candidate_job_id uuid;
    candidate_account_id uuid;
    candidate_kind text;
    candidate_status text;
    candidate_parent_id uuid;
    candidate_source_id uuid;
    attempted uuid[] := ARRAY[]::uuid[];
BEGIN
    IF claiming_worker = '' OR new_lease_token IS NULL OR lease_duration <= interval '0 seconds' THEN
        RAISE EXCEPTION 'invalid claim arguments' USING ERRCODE = '22023';
    END IF;
    LOOP
        SELECT job.id, job.account_id, job.kind, job.status, job.parent_job_id
          INTO candidate_job_id, candidate_account_id, candidate_kind, candidate_status, candidate_parent_id
          FROM app.jobs job
         WHERE (job.kind = 'source_connection_check' OR
                (include_manual_ingest AND job.kind IN ('manual_ingest_source', 'scheduled_ingest_source'))) AND
               ((job.status = 'queued' AND job.cancel_requested_at IS NULL) OR
                (job.status = 'running' AND job.lease_expires_at < transaction_timestamp())) AND
               NOT (job.id = ANY(attempted))
         ORDER BY job.priority DESC, job.created_at, job.id
         LIMIT 1;
        IF NOT FOUND THEN RETURN; END IF;
        attempted := array_append(attempted, candidate_job_id);
        PERFORM set_config('app.account_id', candidate_account_id::text, true);

        SELECT snapshot.source_id INTO candidate_source_id FROM app.job_config_snapshots snapshot
         WHERE snapshot.job_id = candidate_job_id AND snapshot.account_id = candidate_account_id;
        IF NOT FOUND THEN CONTINUE; END IF;
        PERFORM 1 FROM app.sources source
         WHERE source.id = candidate_source_id AND source.account_id = candidate_account_id
           AND source.deleted_at IS NULL
         FOR UPDATE SKIP LOCKED;
        IF NOT FOUND THEN CONTINUE; END IF;

        IF candidate_parent_id IS NOT NULL THEN
            PERFORM 1 FROM app.jobs parent
             WHERE parent.id = candidate_parent_id AND parent.account_id = candidate_account_id
               AND ((candidate_kind = 'manual_ingest_source' AND parent.kind = 'manual_ingest') OR
                    (candidate_kind = 'scheduled_ingest_source' AND parent.kind = 'scheduled_ingest'))
             FOR UPDATE SKIP LOCKED;
            IF NOT FOUND THEN CONTINUE; END IF;
        END IF;

        SELECT job.status INTO candidate_status FROM app.jobs job
         WHERE job.id = candidate_job_id AND job.account_id = candidate_account_id
           AND job.kind = candidate_kind
           AND ((job.status = 'queued' AND job.cancel_requested_at IS NULL) OR
                (job.status = 'running' AND job.lease_expires_at < transaction_timestamp()))
         FOR UPDATE SKIP LOCKED;
        IF NOT FOUND THEN CONTINUE; END IF;
        IF candidate_status = 'running' AND NOT app.recover_expired_job(candidate_job_id) THEN
            CONTINUE;
        END IF;
        IF app.claim_job(candidate_job_id, claiming_worker, new_lease_token, lease_duration) THEN
            job_id := candidate_job_id;
            account_id := candidate_account_id;
            kind := candidate_kind;
            RETURN NEXT;
            RETURN;
        END IF;
    END LOOP;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.assert_no_active_scheduled_ingest()
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
BEGIN
    LOCK TABLE app.jobs, app.job_config_snapshots IN SHARE ROW EXCLUSIVE MODE;
    IF EXISTS (
        SELECT 1 FROM app.jobs job
         WHERE job.kind = 'scheduled_ingest_source' AND job.status IN ('queued', 'running')
    ) OR EXISTS (
        SELECT 1 FROM app.job_config_snapshots snapshot
        JOIN app.jobs job ON job.id = snapshot.job_id AND job.account_id = snapshot.account_id
        WHERE job.kind = 'scheduled_ingest_source'
    ) THEN
        RAISE EXCEPTION 'cannot downgrade while scheduled ingest jobs or snapshots are active' USING ERRCODE = '55006';
    END IF;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.assert_no_active_scheduled_ingest() FROM PUBLIC, workouts_api, workouts_worker;
GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.assert_no_active_scheduled_ingest() OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
GRANT EXECUTE ON FUNCTION app.assert_no_active_scheduled_ingest() TO workouts_migration;

UPDATE app.schema_metadata SET schema_version = 6, minimum_runtime_version = 1;

-- +goose Down
SELECT app.assert_no_active_scheduled_ingest();
UPDATE app.schema_metadata SET schema_version = 5, minimum_runtime_version = 1;

-- Restore migration-4's manual-only ingest plumbing exactly.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.clear_ingest_write_capability()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    snapshot_source_id uuid;
    parent_id uuid;
BEGIN
    -- The initial fence still holds source, parent, and child locks; recovery and
    -- cancellation cannot change ownership before commit after wall-clock expiry.
    IF NEW.backend_pid <> pg_backend_pid() OR NEW.transaction_id <> txid_current() THEN
        RAISE EXCEPTION 'ingest capability transaction changed before commit' USING ERRCODE = '40001';
    END IF;
    SELECT snapshot.source_id INTO snapshot_source_id
      FROM app.job_config_snapshots snapshot
     WHERE snapshot.job_id = NEW.job_id AND snapshot.account_id = NEW.account_id
       AND snapshot.source_id = NEW.source_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'ingest snapshot changed before commit' USING ERRCODE = '40001';
    END IF;

    PERFORM 1 FROM app.sources source
     WHERE source.id = NEW.source_id AND source.account_id = NEW.account_id
       AND source.deleted_at IS NULL
     FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'ingest source changed before commit' USING ERRCODE = '40001';
    END IF;

    SELECT job.parent_job_id INTO parent_id FROM app.jobs job
     WHERE job.id = NEW.job_id AND job.account_id = NEW.account_id
       AND job.kind = 'manual_ingest_source';
    IF NOT FOUND OR parent_id IS NULL THEN
        RAISE EXCEPTION 'ingest job changed before commit' USING ERRCODE = '40001';
    END IF;
    PERFORM 1 FROM app.jobs parent
     WHERE parent.id = parent_id AND parent.account_id = NEW.account_id
       AND parent.kind = 'manual_ingest' AND parent.status = 'running'
       AND parent.cancel_requested_at IS NULL
     FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'ingest parent changed before commit' USING ERRCODE = '40001';
    END IF;

    PERFORM 1 FROM app.jobs job
     WHERE job.id = NEW.job_id AND job.account_id = NEW.account_id
       AND job.kind = 'manual_ingest_source' AND job.status = 'running'
       AND job.cancel_requested_at IS NULL AND job.worker_id = NEW.worker_id
       AND job.lease_token = NEW.lease_token
     FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'ingest lease ownership changed before commit' USING ERRCODE = '40001';
    END IF;
    DELETE FROM app.ingest_write_capabilities capability
     WHERE capability.backend_pid = NEW.backend_pid
       AND capability.transaction_id = NEW.transaction_id;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.fence_ingest_job(job_id uuid, claiming_worker text, current_lease_token uuid)
RETURNS TABLE(source_id uuid)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    snapshot_source_id uuid;
    parent_id uuid;
BEGIN
    IF claiming_worker = '' OR current_lease_token IS NULL THEN
        RAISE EXCEPTION 'invalid ingest fence arguments' USING ERRCODE = '22023';
    END IF;
    SELECT snapshot.source_id INTO snapshot_source_id
      FROM app.job_config_snapshots snapshot
     WHERE snapshot.job_id = fence_ingest_job.job_id
       AND snapshot.account_id = app.current_account_id();
    IF NOT FOUND THEN RETURN; END IF;

    PERFORM 1 FROM app.sources source
     WHERE source.id = snapshot_source_id
       AND source.account_id = app.current_account_id()
       AND source.deleted_at IS NULL
     FOR UPDATE;
    IF NOT FOUND THEN RETURN; END IF;

    SELECT job.parent_job_id INTO parent_id FROM app.jobs job
     WHERE job.id = fence_ingest_job.job_id AND job.account_id = app.current_account_id()
       AND job.kind = 'manual_ingest_source';
    IF NOT FOUND OR parent_id IS NULL THEN RETURN; END IF;
    PERFORM 1 FROM app.jobs parent
     WHERE parent.id = parent_id AND parent.account_id = app.current_account_id()
       AND parent.kind = 'manual_ingest'
     FOR UPDATE;
    IF NOT FOUND THEN RETURN; END IF;

    PERFORM 1 FROM app.jobs job
     WHERE job.id = fence_ingest_job.job_id
       AND job.account_id = app.current_account_id()
       AND job.kind = 'manual_ingest_source'
       AND job.status = 'running'
       AND job.cancel_requested_at IS NULL
       AND job.worker_id = claiming_worker
       AND job.lease_token = current_lease_token
       AND job.lease_expires_at >= clock_timestamp()
     FOR UPDATE;
    IF NOT FOUND THEN RETURN; END IF;
    INSERT INTO app.ingest_write_capabilities
        (backend_pid, transaction_id, account_id, source_id, job_id, worker_id, lease_token)
    VALUES (pg_backend_pid(), txid_current(), app.current_account_id(), snapshot_source_id,
        fence_ingest_job.job_id, claiming_worker, current_lease_token)
    ON CONFLICT (backend_pid, transaction_id) DO NOTHING;
    PERFORM 1 FROM app.ingest_write_capabilities capability
     WHERE capability.backend_pid = pg_backend_pid() AND capability.transaction_id = txid_current()
       AND capability.account_id = app.current_account_id() AND capability.source_id = snapshot_source_id
       AND capability.job_id = fence_ingest_job.job_id AND capability.worker_id = claiming_worker
       AND capability.lease_token = current_lease_token;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'transaction already fenced for another ingest job' USING ERRCODE = '25001';
    END IF;
    source_id := snapshot_source_id;
    RETURN NEXT;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.claim_next_worker_job_internal(claiming_worker text, new_lease_token uuid,
    lease_duration interval, include_manual_ingest boolean)
RETURNS TABLE(job_id uuid, account_id uuid, kind text)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    candidate_job_id uuid;
    candidate_account_id uuid;
    candidate_kind text;
    candidate_status text;
    candidate_parent_id uuid;
    candidate_source_id uuid;
    attempted uuid[] := ARRAY[]::uuid[];
BEGIN
    IF claiming_worker = '' OR new_lease_token IS NULL OR lease_duration <= interval '0 seconds' THEN
        RAISE EXCEPTION 'invalid claim arguments' USING ERRCODE = '22023';
    END IF;
    LOOP
        SELECT job.id, job.account_id, job.kind, job.status, job.parent_job_id
          INTO candidate_job_id, candidate_account_id, candidate_kind, candidate_status, candidate_parent_id
          FROM app.jobs job
         WHERE (job.kind = 'source_connection_check' OR
                (include_manual_ingest AND job.kind = 'manual_ingest_source')) AND
               ((job.status = 'queued' AND job.cancel_requested_at IS NULL) OR
                (job.status = 'running' AND job.lease_expires_at < transaction_timestamp())) AND
               NOT (job.id = ANY(attempted))
         ORDER BY job.priority DESC, job.created_at, job.id
         LIMIT 1;
        IF NOT FOUND THEN RETURN; END IF;
        attempted := array_append(attempted, candidate_job_id);
        PERFORM set_config('app.account_id', candidate_account_id::text, true);

        SELECT snapshot.source_id INTO candidate_source_id FROM app.job_config_snapshots snapshot
         WHERE snapshot.job_id = candidate_job_id AND snapshot.account_id = candidate_account_id;
        IF NOT FOUND THEN CONTINUE; END IF;
        PERFORM 1 FROM app.sources source
         WHERE source.id = candidate_source_id AND source.account_id = candidate_account_id
           AND source.deleted_at IS NULL
         FOR UPDATE SKIP LOCKED;
        IF NOT FOUND THEN CONTINUE; END IF;

        IF candidate_parent_id IS NOT NULL THEN
            PERFORM 1 FROM app.jobs parent
             WHERE parent.id = candidate_parent_id AND parent.account_id = candidate_account_id
               AND parent.kind = 'manual_ingest'
             FOR UPDATE SKIP LOCKED;
            IF NOT FOUND THEN CONTINUE; END IF;
        END IF;

        SELECT job.status INTO candidate_status FROM app.jobs job
         WHERE job.id = candidate_job_id AND job.account_id = candidate_account_id
           AND job.kind = candidate_kind
           AND ((job.status = 'queued' AND job.cancel_requested_at IS NULL) OR
                (job.status = 'running' AND job.lease_expires_at < transaction_timestamp()))
         FOR UPDATE SKIP LOCKED;
        IF NOT FOUND THEN CONTINUE; END IF;
        IF candidate_status = 'running' AND NOT app.recover_expired_job(candidate_job_id) THEN
            CONTINUE;
        END IF;
        IF app.claim_job(candidate_job_id, claiming_worker, new_lease_token, lease_duration) THEN
            job_id := candidate_job_id;
            account_id := candidate_account_id;
            kind := candidate_kind;
            RETURN NEXT;
            RETURN;
        END IF;
    END LOOP;
END;
$$;
-- +goose StatementEnd

REVOKE EXECUTE ON FUNCTION app.assert_no_active_scheduled_ingest() FROM workouts_migration;
ALTER FUNCTION app.assert_no_active_scheduled_ingest() OWNER TO workouts_migration;
DROP FUNCTION app.assert_no_active_scheduled_ingest();

REVOKE EXECUTE ON FUNCTION app.read_worker_job_log_context(uuid,text,uuid) FROM workouts_worker;
ALTER FUNCTION app.read_worker_job_log_context(uuid,text,uuid) OWNER TO workouts_migration;
DROP FUNCTION app.read_worker_job_log_context(uuid,text,uuid);

-- +goose StatementBegin
CREATE FUNCTION app.read_worker_job_log_context(job_id uuid, claiming_worker text, current_lease_token uuid)
RETURNS TABLE(owner_name text, source_name text, source_type text)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    target_account_id uuid;
    target_administrator_id uuid;
    prior_account_id text := current_setting('app.account_id', true);
BEGIN
    IF job_id IS NULL OR claiming_worker = '' OR current_lease_token IS NULL THEN
        RETURN;
    END IF;

    SELECT job.account_id, job.administrator_id
      INTO target_account_id, target_administrator_id
      FROM app.jobs job
     WHERE job.id = read_worker_job_log_context.job_id
       AND job.status = 'running'
       AND job.worker_id = claiming_worker
       AND job.lease_token = current_lease_token
       AND job.lease_expires_at >= clock_timestamp();
    IF NOT FOUND THEN RETURN; END IF;

    IF target_account_id IS NOT NULL THEN
        PERFORM set_config('app.account_id', target_account_id::text, true);
        RETURN QUERY
        SELECT principal.full_name, source.display_name, source.type
          FROM app.users account_user
          JOIN app.authentication_principals principal ON principal.id = account_user.principal_id
          LEFT JOIN app.job_config_snapshots snapshot
            ON snapshot.job_id = read_worker_job_log_context.job_id
           AND snapshot.account_id = target_account_id
          LEFT JOIN app.sources source
            ON source.id = snapshot.source_id
           AND source.account_id = snapshot.account_id
         WHERE account_user.account_id = target_account_id;
        PERFORM set_config('app.account_id', COALESCE(prior_account_id, ''), true);
    ELSE
        RETURN QUERY
        SELECT principal.full_name, NULL::text, NULL::text
          FROM app.administrators administrator
          JOIN app.authentication_principals principal ON principal.id = administrator.principal_id
         WHERE administrator.principal_id = target_administrator_id;
    END IF;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON FUNCTION app.read_worker_job_log_context(uuid,text,uuid)
    FROM PUBLIC, workouts_api, workouts_worker;
GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.read_worker_job_log_context(uuid,text,uuid) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
GRANT EXECUTE ON FUNCTION app.read_worker_job_log_context(uuid,text,uuid) TO workouts_worker;
