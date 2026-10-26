-- +goose Up
-- Parent presentation only: failed source children remain failed for retry and
-- freshness. Existing terminal jobs and their persisted notifications are history;
-- this migration does not rewrite them or bypass any state-machine triggers.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.derive_parent_status (parent_id uuid) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
DECLARE derived_status text; parent_kind text; parent_status text; child_count integer; queued_count integer; running_count integer;
DECLARE succeeded_count integer; failed_count integer; cancelled_count integer; superseded_count integer;
DECLARE failed_child_has_success boolean := false;
BEGIN
    SELECT kind, status INTO parent_kind, parent_status FROM app.jobs
     WHERE id = parent_id AND account_id = app.current_account_id() FOR UPDATE;
    IF NOT FOUND OR parent_kind NOT IN ('manual_ingest', 'scheduled_ingest', 'coverage_update') THEN RETURN; END IF;
    IF parent_kind IN ('manual_ingest', 'scheduled_ingest') AND
       parent_status IN ('succeeded', 'partially_succeeded', 'failed', 'cancelled') THEN RETURN; END IF;
    SELECT count( * ), count( * ) FILTER (WHERE status = 'queued'), count( * ) FILTER (WHERE status = 'running'),
           count( * ) FILTER (WHERE status = 'succeeded'), count( * ) FILTER (WHERE status = 'failed'), count( * ) FILTER (WHERE status = 'cancelled')
      INTO child_count, queued_count, running_count, succeeded_count, failed_count, cancelled_count
      FROM app.jobs WHERE parent_job_id = parent_id AND account_id = app.current_account_id();
    IF child_count = 0 THEN RETURN; END IF;
    IF parent_kind = 'coverage_update' THEN
        SELECT count( * ) INTO superseded_count FROM app.jobs child
          JOIN app.coverage_route_job_contexts context ON context.job_id = child.id AND context.account_id = child.account_id
         WHERE child.parent_job_id = parent_id AND child.account_id = app.current_account_id()
           AND child.status = 'succeeded' AND context.result_outcome = 'superseded';
        INSERT INTO app.coverage_job_progress(job_id, account_id, routes_total, routes_processed, routes_succeeded, routes_failed, routes_cancelled, routes_superseded)
        VALUES(parent_id, app.current_account_id(), child_count, succeeded_count + failed_count,
            succeeded_count - superseded_count, failed_count, cancelled_count, superseded_count)
        ON CONFLICT (job_id) DO UPDATE SET routes_total = EXCLUDED.routes_total, routes_processed = EXCLUDED.routes_processed,
            routes_succeeded = EXCLUDED.routes_succeeded, routes_failed = EXCLUDED.routes_failed,
            routes_cancelled = EXCLUDED.routes_cancelled, routes_superseded = EXCLUDED.routes_superseded, updated_at = transaction_timestamp();
    ELSIF failed_count > 0 AND queued_count = 0 AND running_count = 0 THEN
        -- Skips are successful checks of previously imported, unchanged files,
        -- not failed attempts. File commits can precede their progress snapshot.
        SELECT EXISTS (
            SELECT 1 FROM app.jobs child
            LEFT JOIN app.job_progress progress ON progress.job_id = child.id AND progress.account_id = child.account_id
             WHERE child.parent_job_id = parent_id AND child.account_id = app.current_account_id()
               AND child.kind IN ('manual_ingest_source', 'scheduled_ingest_source') AND child.status = 'failed'
               AND (progress.files_succeeded > 0 OR progress.files_skipped > 0 OR EXISTS (
                   SELECT 1 FROM app.source_files file WHERE file.job_id = child.id
                     AND file.account_id = child.account_id AND file.state = 'succeeded'
               ))
        ) INTO failed_child_has_success;
    END IF;
    derived_status := CASE
        WHEN queued_count = child_count THEN 'queued'
        WHEN running_count > 0 OR (queued_count > 0 AND queued_count < child_count) THEN 'running'
        WHEN succeeded_count = child_count THEN 'succeeded'
        WHEN (succeeded_count > 0 OR failed_child_has_success) AND failed_count + cancelled_count > 0 THEN 'partially_succeeded'
        WHEN succeeded_count = 0 AND cancelled_count = child_count THEN 'cancelled'
        ELSE 'failed' END;
    PERFORM set_config('app.job_transition', 'derive_parent', true);
    UPDATE app.jobs SET status = derived_status,
        progress_current = CASE WHEN parent_kind = 'coverage_update' THEN succeeded_count + failed_count ELSE progress_current END,
        progress_total = CASE WHEN parent_kind = 'coverage_update' THEN child_count ELSE progress_total END,
        started_at = CASE WHEN derived_status = 'running' THEN COALESCE(started_at, now()) ELSE started_at END,
        terminal_at = CASE WHEN derived_status IN ('succeeded', 'partially_succeeded', 'failed', 'cancelled') THEN now() ELSE NULL END
     WHERE id = parent_id AND account_id = app.current_account_id() AND status IS DISTINCT FROM derived_status;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.notify_terminal_ingest_parent () RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
DECLARE
    notification_severity text;
    notification_title text;
    notification_message text;
    file_only_failure boolean;
BEGIN
    IF NEW.parent_job_id IS NOT NULL OR NEW.kind NOT IN ('manual_ingest', 'scheduled_ingest') OR
       NEW.status NOT IN ('succeeded', 'partially_succeeded', 'failed', 'cancelled') OR
       OLD.status IN ('succeeded', 'partially_succeeded', 'failed', 'cancelled') OR
       (NEW.kind = 'scheduled_ingest' AND NEW.status IN ('succeeded', 'cancelled')) THEN
        RETURN NEW;
    END IF;
    SELECT severity, title, message INTO notification_severity, notification_title, notification_message FROM (VALUES
        ('succeeded', 'info', 'Data sync completed', 'Your requested data sync completed successfully.'),
        ('partially_succeeded', 'warning', 'Data sync partially completed', 'Data sync partially completed. One or more sources could not finish. Successfully imported data has been kept. You can retry this sync.'),
        ('failed', 'error', 'Data sync failed', 'Data sync could not be completed. Check the source details, then retry this sync.'),
        ('cancelled', 'info', 'Data sync cancelled', 'Your requested data sync was cancelled.')
    ) policy(status, severity, title, message) WHERE status = NEW.status;
    IF NEW.status IN ('partially_succeeded', 'failed') THEN
        SELECT COALESCE(bool_and(
            child.status = 'failed' AND child.kind IN ('manual_ingest_source', 'scheduled_ingest_source')
            AND child.failure_code IS NOT DISTINCT FROM 'source-file-invalid' AND EXISTS (
                SELECT 1 FROM app.source_files file WHERE file.job_id = child.id
                  AND file.account_id = child.account_id AND file.state = 'failed'
            )), false) INTO file_only_failure
          FROM app.jobs child WHERE child.parent_job_id = NEW.id AND child.account_id = NEW.account_id
           AND child.status IN ('failed', 'cancelled');
        IF file_only_failure THEN
            notification_message := CASE WHEN NEW.status = 'partially_succeeded' THEN
                'Data sync partially completed. Some source files could not be processed. Successfully imported data has been kept. You can retry this sync.'
            ELSE
                'Data sync could not be completed because source files could not be processed. You can retry this sync.' END;
        END IF;
    END IF;
    INSERT INTO app.notifications(id, account_id, type, severity, condition_key, subject_type, subject_id, job_id, title, message)
    VALUES(gen_random_uuid(), NEW.account_id, 'ingest-terminal', notification_severity, 'job:' || NEW.id::text,
        'job', NEW.id, NEW.id, notification_title, notification_message)
    ON CONFLICT(account_id, type, condition_key) DO NOTHING;
    RETURN NEW;
END;
$function$;
-- +goose StatementEnd

-- CREATE OR REPLACE retains the existing owners and ACLs. Both helpers remain
-- internal: source-worker completion invokes them through the state machine.
REVOKE ALL ON FUNCTION app.derive_parent_status(uuid), app.notify_terminal_ingest_parent()
    FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;
GRANT EXECUTE ON FUNCTION app.derive_parent_status(uuid) TO workouts_security_owner;

-- Retain the deployed three-argument read ABI for runtime 28. The overload
-- has no default, so old calls remain unambiguous during rolling upgrades.
-- +goose StatementBegin
CREATE FUNCTION app.read_owned_job_files (target_job_id uuid, requested_limit integer, requested_offset integer, failures_only boolean) RETURNS TABLE (
    total_count bigint,
    id uuid,
    job_id uuid,
    source_id uuid,
    source_generation bigint,
    display_name text,
    source_type text,
    basename text,
    state text,
    size_bytes bigint,
    processing_started_at timestamptz,
    processed_at timestamptz,
    failure_code text,
    failure_summary text,
    created_at timestamptz,
    updated_at timestamptz
) LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
BEGIN
    IF requested_limit NOT BETWEEN 1 AND 100 OR requested_offset < 0 THEN
        RAISE EXCEPTION 'invalid pagination' USING ERRCODE = '22023';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM app.jobs owned_job WHERE owned_job.id = target_job_id
        AND owned_job.account_id = app.current_account_id()
        AND owned_job.kind IN ('manual_ingest', 'scheduled_ingest', 'manual_ingest_source', 'scheduled_ingest_source')) THEN RETURN; END IF;
    RETURN QUERY SELECT count( * ) OVER(), file.id, file.job_id, context.source_id, context.source_generation, context.display_name,
        context.source_type, regexp_replace(file.relative_name, '^.*/', ''), file.state, file.size_bytes, file.processing_started_at,
        file.processed_at,
        CASE WHEN file.failure_code IN ('source-file-unavailable', 'source-file-mutated', 'source-file-invalid') THEN file.failure_code
             WHEN file.state = 'failed' THEN 'source-file-invalid' END,
        CASE WHEN file.failure_code = 'source-file-unavailable' THEN 'A source file could not be read.'
             WHEN file.failure_code = 'source-file-mutated' THEN 'A source file changed before it could be processed.'
             WHEN file.state = 'failed' THEN 'A source file could not be processed.' END,
        file.created_at, file.updated_at
      FROM app.source_files file JOIN app.jobs child ON child.id = file.job_id AND child.account_id = file.account_id
      JOIN app.job_source_contexts context ON context.job_id = child.id AND context.account_id = child.account_id
     WHERE file.account_id = app.current_account_id() AND (child.id = target_job_id OR child.parent_job_id = target_job_id)
       AND (failures_only IS NOT TRUE OR file.state = 'failed')
     ORDER BY file.created_at, file.id LIMIT requested_limit OFFSET requested_offset;
END;
$function$;
-- +goose StatementEnd

-- The separate count preserves totals on empty and beyond-last pages. Existing
-- file/log callers and coverage-log suppression retain their deployed behavior.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.count_owned_job_rows (target_job_id uuid, row_kind text) RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
DECLARE result bigint;
BEGIN
    IF row_kind IN ('file', 'failed_file') THEN
        SELECT count( * ) INTO result FROM app.source_files file JOIN app.jobs job
          ON job.id = file.job_id AND job.account_id = file.account_id
         WHERE file.account_id = app.current_account_id() AND (job.id = target_job_id OR job.parent_job_id = target_job_id)
           AND (row_kind = 'file' OR file.state = 'failed');
    ELSIF row_kind = 'log' THEN
        SELECT count( * ) INTO result FROM app.job_logs log JOIN app.jobs job
          ON job.id = log.job_id AND job.account_id = log.account_id
         WHERE log.account_id = app.current_account_id() AND (job.id = target_job_id OR job.parent_job_id = target_job_id)
           AND NOT ((log.code = 'coverage-route-started' AND EXISTS (
                   SELECT 1 FROM app.job_logs contextual
                   WHERE contextual.account_id = log.account_id AND contextual.job_id = log.job_id
                     AND contextual.code = 'coverage-route-start-context')) OR
               (log.code IN ('coverage-route-applied', 'coverage-route-no-evidence', 'coverage-route-failed',
               'coverage-route-superseded', 'coverage-route-cancelled') AND EXISTS (
                   SELECT 1 FROM app.job_logs contextual
                   WHERE contextual.account_id = log.account_id AND contextual.job_id = log.job_id
                     AND contextual.code = 'coverage-route-result-context')));
    ELSE
        RAISE EXCEPTION 'invalid row kind' USING ERRCODE = '22023';
    END IF;
    RETURN result;
END;
$function$;
-- +goose StatementEnd

GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.read_owned_job_files(uuid, integer, integer, boolean) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
REVOKE ALL ON FUNCTION app.read_owned_job_files(uuid, integer, integer, boolean)
    FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;
GRANT EXECUTE ON FUNCTION app.read_owned_job_files(uuid, integer, integer, boolean) TO workouts_api;

-- Existing count helper ownership and ACLs are retained by CREATE OR REPLACE.
-- Runtime 28 and the canonical migration baseline remain supported.
UPDATE app.schema_metadata SET schema_version = 29 WHERE singleton;

-- +goose Down
-- +goose StatementBegin
DO $block$ BEGIN
    RAISE EXCEPTION 'ingest partial success is forward-only' USING ERRCODE = '55000';
END; $block$;
-- +goose StatementEnd
