-- +goose Up
-- +goose StatementBegin
CREATE FUNCTION app.repair_orphaned_source_jobs()
RETURNS integer
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    orphan record;
    repaired_count integer := 0;
    prior_account_id text := current_setting('app.account_id', true);
    prior_transition text := current_setting('app.job_transition', true);
BEGIN
    FOR orphan IN
        SELECT job.id, job.account_id, job.parent_job_id, job.status
          FROM app.jobs job
         WHERE job.kind IN ('source_connection_check', 'manual_ingest_source', 'scheduled_ingest_source')
           AND job.status IN ('queued', 'running')
           AND NOT EXISTS (SELECT 1 FROM app.job_config_snapshots snapshot WHERE snapshot.job_id = job.id)
         ORDER BY job.account_id, job.id
    LOOP
        PERFORM set_config('app.account_id', orphan.account_id::text, true);
        PERFORM set_config('app.job_transition', CASE orphan.status WHEN 'queued' THEN 'cancel' ELSE 'finish' END, true);
        UPDATE app.jobs job
           SET status = 'cancelled',
               terminal_at = transaction_timestamp(),
               worker_id = NULL,
               lease_token = NULL,
               claimed_at = NULL,
               heartbeat_at = NULL,
               lease_expires_at = NULL,
               failure_code = 'source-snapshot-missing',
               failure_summary = 'Job cancelled during schema upgrade because its source snapshot was missing.'
         WHERE job.id = orphan.id
           AND job.account_id = orphan.account_id
           AND job.status = orphan.status
           AND NOT EXISTS (SELECT 1 FROM app.job_config_snapshots snapshot WHERE snapshot.job_id = job.id);
        IF FOUND THEN
            repaired_count := repaired_count + 1;
            IF orphan.parent_job_id IS NOT NULL THEN
                PERFORM app.derive_parent_status(orphan.parent_job_id);
            END IF;
        END IF;
    END LOOP;
    PERFORM set_config('app.account_id', COALESCE(prior_account_id, ''), true);
    PERFORM set_config('app.job_transition', COALESCE(prior_transition, ''), true);
    RETURN repaired_count;
END;
$$;
-- +goose StatementEnd

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
REVOKE ALL ON FUNCTION app.repair_orphaned_source_jobs()
    FROM PUBLIC, workouts_api, workouts_worker;
GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.repair_orphaned_source_jobs() OWNER TO workouts_security_owner;
ALTER FUNCTION app.read_worker_job_log_context(uuid,text,uuid) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
GRANT EXECUTE ON FUNCTION app.repair_orphaned_source_jobs() TO workouts_migration;
SELECT app.repair_orphaned_source_jobs();
REVOKE EXECUTE ON FUNCTION app.repair_orphaned_source_jobs() FROM workouts_migration;
ALTER FUNCTION app.repair_orphaned_source_jobs() OWNER TO workouts_migration;
DROP FUNCTION app.repair_orphaned_source_jobs();
GRANT EXECUTE ON FUNCTION app.read_worker_job_log_context(uuid,text,uuid) TO workouts_worker;

UPDATE app.schema_metadata SET schema_version = 5, minimum_runtime_version = 1;

-- +goose Down
UPDATE app.schema_metadata SET schema_version = 4, minimum_runtime_version = 1;
REVOKE EXECUTE ON FUNCTION app.read_worker_job_log_context(uuid,text,uuid) FROM workouts_worker;
ALTER FUNCTION app.read_worker_job_log_context(uuid,text,uuid) OWNER TO workouts_migration;
DROP FUNCTION app.read_worker_job_log_context(uuid,text,uuid);
