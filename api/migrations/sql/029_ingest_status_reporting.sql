-- +goose Up
-- Ingest outcomes, reprocessing receipts and reliable phase reporting share
-- one contract. Historical terminal jobs and notifications remain untouched.
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

-- The notification helper retains its owner and remains internal to the state machine.
REVOKE ALL ON FUNCTION app.notify_terminal_ingest_parent()
    FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;

-- The failures-only overload has no default, preserving unambiguous file reads.
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

-- Presentation is derived from imported/updated workouts, never file checks.
-- Historical jobs, progress, workouts and notifications remain untouched.
-- +goose StatementBegin
CREATE FUNCTION app.ingest_outcome_status(job_kind text, job_status text, workouts_created bigint, workouts_updated bigint)
RETURNS text LANGUAGE sql IMMUTABLE SECURITY INVOKER SET search_path = pg_catalog AS $function$
    SELECT CASE
        WHEN job_kind IN ('manual_ingest', 'scheduled_ingest', 'manual_ingest_source', 'scheduled_ingest_source')
             AND job_status IN ('failed', 'partially_succeeded') THEN
            CASE WHEN COALESCE(workouts_created, 0) > 0 OR COALESCE(workouts_updated, 0) > 0
                THEN 'partially_succeeded' ELSE 'failed' END
        ELSE job_status END
$function$;
-- +goose StatementEnd

GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.ingest_outcome_status(text, text, bigint, bigint) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
REVOKE ALL ON FUNCTION app.ingest_outcome_status(text, text, bigint, bigint)
    FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles, workouts_migration;
GRANT EXECUTE ON FUNCTION app.ingest_outcome_status(text, text, bigint, bigint) TO workouts_api;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.derive_parent_status (parent_id uuid) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
DECLARE derived_status text; parent_kind text; parent_status text; child_count integer; queued_count integer; running_count integer;
DECLARE succeeded_count integer; failed_count integer; cancelled_count integer; superseded_count integer;
DECLARE ingest_has_workouts boolean := false;
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
    ELSIF failed_count + cancelled_count > 0 AND queued_count = 0 AND running_count = 0 THEN
        SELECT COALESCE(sum(progress.workouts_created), 0) > 0 OR COALESCE(sum(progress.workouts_updated), 0) > 0
          INTO ingest_has_workouts
          FROM app.jobs child
          JOIN app.job_progress progress ON progress.job_id = child.id AND progress.account_id = child.account_id
         WHERE child.parent_job_id = parent_id AND child.account_id = app.current_account_id()
           AND child.kind IN ('manual_ingest_source', 'scheduled_ingest_source');
    END IF;
    derived_status := CASE
        WHEN queued_count = child_count THEN 'queued'
        WHEN running_count > 0 OR (queued_count > 0 AND queued_count < child_count) THEN 'running'
        WHEN succeeded_count = child_count THEN 'succeeded'
        WHEN parent_kind = 'coverage_update' THEN CASE
            WHEN succeeded_count > 0 AND failed_count + cancelled_count > 0 THEN 'partially_succeeded'
            WHEN succeeded_count = 0 AND cancelled_count = child_count THEN 'cancelled'
            ELSE 'failed' END
        WHEN cancelled_count = child_count THEN 'cancelled'
        WHEN failed_count + cancelled_count > 0 AND ingest_has_workouts THEN 'partially_succeeded'
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

-- CREATE OR REPLACE retains the internal state-machine owner and ACLs.
REVOKE ALL ON FUNCTION app.derive_parent_status(uuid)
    FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;
GRANT EXECUTE ON FUNCTION app.derive_parent_status(uuid) TO workouts_security_owner;

-- Project ingest outcomes before status filtering, counting and pagination.
-- Coverage proof, retry supersession, account scope and empty-page totals retain
-- the deployed reader's behavior. The progress join cannot expose other accounts.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.read_job_history(status_filter text, operation_filter text, page_size integer, page_offset bigint, max_ordinal integer)
RETURNS TABLE(job_id uuid, display_status text, total_items bigint)
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
    WITH candidates AS MATERIALIZED (
        SELECT job.id, job.kind, job.status, job.created_at, CASE
            WHEN job.kind = 'coverage_update' AND job.status = 'running' AND NOT EXISTS (
                SELECT 1 FROM app.jobs child WHERE child.parent_job_id = job.id
                    AND child.account_id = job.account_id AND child.status = 'running') THEN 'queued'
            ELSE app.ingest_outcome_status(job.kind, job.status, progress.workouts_created, progress.workouts_updated) END AS public_status
        FROM app.jobs job JOIN app.accounts account ON account.id = job.account_id AND account.state = 'active'
        LEFT JOIN app.job_progress progress ON progress.job_id = job.id AND progress.account_id = job.account_id
        WHERE job.account_id = app.current_account_id() AND job.parent_job_id IS NULL
          AND job.kind IN ('manual_ingest','scheduled_ingest','workout_deletion','coverage_update')
          AND page_size BETWEEN 1 AND 100 AND page_offset >= 0 AND max_ordinal BETWEEN 1 AND 100
          AND COALESCE(status_filter,'') IN ('','queued','running','succeeded','partially_succeeded','failed','cancelled','superseded')
          AND COALESCE(operation_filter,'') IN ('','manual_sync','automated_sync','workout_deletion','coverage_update')
          AND (COALESCE(status_filter,'') NOT IN ('failed','partially_succeeded','superseded') OR job.status = status_filter
              OR (job.kind IN ('manual_ingest','scheduled_ingest') AND job.status IN ('failed','partially_succeeded')
                  AND status_filter IN ('failed','partially_succeeded'))
              OR (status_filter = 'superseded' AND job.kind = 'coverage_update' AND job.status IN ('failed','partially_succeeded')))
          AND (COALESCE(operation_filter,'') = '' OR job.kind = CASE operation_filter
              WHEN 'manual_sync' THEN 'manual_ingest' WHEN 'automated_sync' THEN 'scheduled_ingest'
              WHEN 'workout_deletion' THEN 'workout_deletion' WHEN 'coverage_update' THEN 'coverage_update' END)
          AND NOT EXISTS (SELECT 1 FROM app.jobs successor WHERE successor.retry_of_job_id = job.id
              AND successor.account_id = job.account_id AND successor.parent_job_id IS NULL
              AND ((job.kind IN ('manual_ingest','scheduled_ingest') AND successor.kind IN ('manual_ingest','scheduled_ingest'))
                  OR (job.kind = 'workout_deletion' AND successor.kind = 'workout_deletion')
                  OR (job.kind = 'coverage_update' AND successor.kind = 'coverage_update')))
    ), plain_filtered AS MATERIALIZED (
        SELECT * FROM candidates WHERE COALESCE(status_filter,'') NOT IN ('failed','partially_succeeded','superseded')
          AND (COALESCE(status_filter,'') = '' OR public_status = status_filter)
    ), plain_page AS MATERIALIZED (
        SELECT * FROM plain_filtered ORDER BY created_at DESC,id DESC
        LIMIT CASE WHEN page_size BETWEEN 1 AND 100 THEN page_size ELSE 0 END OFFSET GREATEST(page_offset,0)
    ), proof_targets AS MATERIALIZED (
        SELECT candidate.id FROM candidates candidate
        WHERE status_filter IN ('failed','partially_succeeded','superseded')
          AND candidate.kind = 'coverage_update' AND candidate.status IN ('failed','partially_succeeded')
        UNION ALL
        SELECT candidate.id FROM plain_page candidate
        WHERE candidate.kind = 'coverage_update' AND candidate.status IN ('failed','partially_succeeded')
    ), eligibility AS MATERIALIZED (
        SELECT candidate.id, (SELECT proof.reason FROM app.read_coverage_retry_eligibility(candidate.id,max_ordinal) proof) AS reason
        FROM proof_targets candidate
    ), projected AS MATERIALIZED (
        SELECT candidate.id, candidate.created_at, CASE
            WHEN eligibility.reason = 'already_current' THEN 'superseded'
            ELSE candidate.public_status END AS projected_status
        FROM candidates candidate LEFT JOIN eligibility ON eligibility.id = candidate.id
        WHERE status_filter IN ('failed','partially_succeeded','superseded')
    ), filtered AS MATERIALIZED (
        SELECT * FROM projected WHERE projected_status = status_filter
    ), classified_page AS (
        SELECT * FROM filtered ORDER BY created_at DESC,id DESC
        LIMIT CASE WHEN page_size BETWEEN 1 AND 100 THEN page_size ELSE 0 END OFFSET GREATEST(page_offset,0)
    ), page AS (
        SELECT candidate.id, candidate.created_at, CASE
            WHEN eligibility.reason = 'already_current' THEN 'superseded'
            ELSE candidate.public_status END AS projected_status
        FROM plain_page candidate LEFT JOIN eligibility ON eligibility.id = candidate.id
        UNION ALL
        SELECT * FROM classified_page
    )
    SELECT page.id, page.projected_status, totals.total
    FROM (SELECT (SELECT count(*) FROM plain_filtered) + (SELECT count(*) FROM filtered) AS total) totals LEFT JOIN page ON true
    ORDER BY page.created_at DESC,page.id DESC
$function$;
-- +goose StatementEnd

-- Omission in persisted bounded jobs retains the shipped force policy. New API
-- jobs explicitly persist reprocessAllFiles; incremental jobs never carry it.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.valid_ingest_child_parameters(value jsonb) RETURNS boolean
LANGUAGE plpgsql IMMUTABLE SET search_path = pg_catalog AS $function$
DECLARE start_value date; end_value date;
BEGIN
    IF jsonb_typeof(value) IS DISTINCT FROM 'object' THEN RETURN false; END IF;
    IF NOT value ?& ARRAY['sourceId','generation','mode'] OR
       jsonb_typeof(value->'sourceId') IS DISTINCT FROM 'string' OR value->>'sourceId' !~ '^[0-9A-F]{32}$' OR
       jsonb_typeof(value->'generation') IS DISTINCT FROM 'number' OR value->>'generation' !~ '^[1-9][0-9]*$' OR
       jsonb_typeof(value->'mode') IS DISTINCT FROM 'string' OR value->>'mode' NOT IN ('incremental','bounded') THEN
        RETURN false;
    END IF;
    IF value->>'mode' = 'incremental' THEN
        RETURN (SELECT count(*) = 3 FROM jsonb_object_keys(value));
    END IF;
    IF NOT value ?& ARRAY['startDate','endDate'] OR
       EXISTS (SELECT 1 FROM jsonb_object_keys(value) key WHERE key NOT IN
           ('sourceId','generation','mode','startDate','endDate','reprocessAllFiles','legacySchema6')) OR
       jsonb_typeof(value->'startDate') IS DISTINCT FROM 'string' OR
       jsonb_typeof(value->'endDate') IS DISTINCT FROM 'string' OR
       value->>'startDate' !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' OR
       value->>'endDate' !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' OR
       (value ? 'reprocessAllFiles' AND jsonb_typeof(value->'reprocessAllFiles') IS DISTINCT FROM 'boolean') OR
       (value ? 'legacySchema6' AND (value ? 'reprocessAllFiles' OR
           value->'legacySchema6' IS DISTINCT FROM 'true'::jsonb OR
           value->>'startDate' <> '0001-01-01' OR value->>'endDate' <> '9999-12-31')) THEN
        RETURN false;
    END IF;
    BEGIN
        start_value := (value->>'startDate')::date;
        end_value := (value->>'endDate')::date;
    EXCEPTION WHEN datetime_field_overflow OR invalid_datetime_format THEN RETURN false;
    END;
    RETURN to_char(start_value,'YYYY-MM-DD') = value->>'startDate' AND
        to_char(end_value,'YYYY-MM-DD') = value->>'endDate' AND start_value <= end_value;
END;
$function$;
-- +goose StatementEnd

ALTER TABLE app.source_files
    ADD COLUMN observed_identity text,
    ADD COLUMN ownership_blocked_epoch bigint CHECK (ownership_blocked_epoch >= 0),
    ADD CONSTRAINT source_files_success_receipt CHECK (
        (observed_identity IS NULL AND ownership_blocked_epoch IS NULL) OR
        (state = 'succeeded' AND observed_identity IS NOT NULL AND length(observed_identity) BETWEEN 1 AND 512));

-- Private, once per discovery, not once per file. Follow only predecessor edges;
-- a newer branch is neither required nor eligible evidence for this branch.
-- +goose StatementBegin
CREATE FUNCTION app.ingest_retry_ancestor_jobs(job uuid, target_account uuid, target_source uuid) RETURNS uuid[]
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
DECLARE
    cursor_id uuid := job;
    visited uuid[] := ARRAY[]::uuid[];
    parents uuid[] := ARRAY[]::uuid[];
    expected_parent uuid;
    policy jsonb;
    node_policy jsonb;
    node record;
BEGIN
    IF target_account IS DISTINCT FROM app.current_account_id() OR target_source IS NULL THEN
        RAISE EXCEPTION 'invalid ingest retry lineage' USING ERRCODE = '22023';
    END IF;
    LOOP
        IF cursor_id IS NULL OR cursor_id = ANY(visited) OR cardinality(visited) >= 100 THEN
            RAISE EXCEPTION 'invalid ingest retry lineage' USING ERRCODE = '22023';
        END IF;
        SELECT child.id,child.kind,child.parameters,child.retry_of_job_id,child.parent_job_id,
            parent.retry_of_job_id AS parent_retry,parent.parent_job_id AS parent_parent,
            parent.kind AS parent_kind,context.source_id,context.source_generation,context.source_type,
            snapshot.job_id AS snapshot_job,snapshot.source_id AS snapshot_source,snapshot.source_generation AS snapshot_generation
          INTO node FROM app.jobs child
          JOIN app.jobs parent ON parent.id = child.parent_job_id AND parent.account_id = target_account
          JOIN app.job_source_contexts context ON context.job_id = child.id AND context.account_id = target_account
          LEFT JOIN app.job_config_snapshots snapshot ON snapshot.job_id = child.id AND snapshot.account_id = target_account
         WHERE child.id = cursor_id AND child.account_id = target_account;
        IF NOT FOUND THEN RAISE EXCEPTION 'invalid ingest retry lineage' USING ERRCODE = '22023'; END IF;
        IF node.kind NOT IN ('manual_ingest_source','scheduled_ingest_source') OR
           node.parent_kind IS DISTINCT FROM (CASE node.kind WHEN 'manual_ingest_source' THEN 'manual_ingest' ELSE 'scheduled_ingest' END) OR
           node.parent_parent IS NOT NULL OR node.parent_job_id = ANY(parents) OR
           (cardinality(visited) > 0 AND node.parent_job_id IS DISTINCT FROM expected_parent) OR
           node.source_id IS DISTINCT FROM target_source OR
           -- Terminal cleanup deletes encrypted snapshots, not durable contexts.
           (cardinality(visited) = 0 AND node.snapshot_job IS NULL) OR
           (node.snapshot_job IS NOT NULL AND (node.snapshot_source IS DISTINCT FROM target_source OR
               node.snapshot_generation IS DISTINCT FROM node.source_generation)) OR
           NOT app.valid_ingest_child_parameters(node.parameters) OR
           node.parameters->>'sourceId' IS DISTINCT FROM upper(replace(target_source::text,'-','')) OR
           node.parameters->>'generation' IS DISTINCT FROM node.source_generation::text OR
           app.workout_provider_namespace(node.source_type) IS DISTINCT FROM
               (SELECT app.workout_provider_namespace(source.type) FROM app.sources source
                WHERE source.id = target_source AND source.account_id = target_account) THEN
            RAISE EXCEPTION 'invalid ingest retry lineage' USING ERRCODE = '22023';
        END IF;
        node_policy := jsonb_build_object('mode',node.parameters->'mode',
            'startDate',node.parameters->'startDate','endDate',node.parameters->'endDate',
            'force',CASE WHEN node.parameters->>'mode' = 'bounded'
                THEN COALESCE(node.parameters->'reprocessAllFiles','true'::jsonb) ELSE 'false'::jsonb END);
        IF cardinality(visited) = 0 THEN policy := node_policy;
        ELSIF node_policy IS DISTINCT FROM policy THEN
            RAISE EXCEPTION 'invalid ingest retry lineage' USING ERRCODE = '22023';
        END IF;
        visited := array_append(visited,node.id);
        parents := array_append(parents,node.parent_job_id);
        IF node.retry_of_job_id IS NULL THEN
            IF node.parent_retry IS NOT NULL THEN
                RAISE EXCEPTION 'invalid ingest retry lineage' USING ERRCODE = '22023';
            END IF;
            RETURN visited[2:cardinality(visited)];
        END IF;
        expected_parent := node.parent_retry;
        cursor_id := node.retry_of_job_id;
    END LOOP;
EXCEPTION WHEN invalid_parameter_value THEN
    RAISE EXCEPTION 'invalid ingest retry lineage' USING ERRCODE = '22023';
END;
$function$;
-- +goose StatementEnd

-- A receipt is stamped after import events, before the existing inventory write.
-- NULL identity remains historical/unknown, not evidence of a clean success.
-- +goose StatementBegin
CREATE FUNCTION app.stamp_ingest_source_file_receipt() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
DECLARE candidate app.job_file_candidates%ROWTYPE; captured_epoch bigint;
BEGIN
    IF NEW.observed_identity IS NOT NULL OR NEW.ownership_blocked_epoch IS NOT NULL THEN
        RAISE EXCEPTION 'source file receipts are private' USING ERRCODE = '42501';
    END IF;
    IF TG_OP = 'INSERT' THEN
        IF NEW.state = 'succeeded' THEN
            RAISE EXCEPTION 'source file success requires a processing transition' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.state <> 'processing' OR NEW.state <> 'succeeded' THEN RETURN NEW; END IF;
    SELECT capability.ownership_epoch INTO captured_epoch
      FROM app.ingest_write_capabilities capability
      JOIN app.accounts account ON account.id = capability.account_id AND account.state = 'active'
      JOIN app.jobs child ON child.id = capability.job_id AND child.account_id = capability.account_id
      JOIN app.jobs parent ON parent.id = child.parent_job_id AND parent.account_id = child.account_id
      JOIN app.sources source ON source.id = capability.source_id AND source.account_id = capability.account_id
     WHERE capability.backend_pid = pg_backend_pid() AND capability.transaction_id = txid_current()
       AND capability.account_id = NEW.account_id AND capability.account_id = app.current_account_id()
       AND capability.source_id = NEW.source_id AND capability.job_id = NEW.job_id
       AND capability.runtime_version >= 29
       AND capability.runtime_version >= (SELECT minimum_runtime_version FROM app.schema_metadata WHERE singleton)
       AND child.kind IN ('manual_ingest_source','scheduled_ingest_source')
       AND child.status = 'running' AND child.cancel_requested_at IS NULL
       AND child.worker_id = capability.worker_id AND child.lease_token = capability.lease_token
       AND parent.kind = CASE child.kind WHEN 'manual_ingest_source' THEN 'manual_ingest' ELSE 'scheduled_ingest' END
       AND parent.status = 'running' AND parent.cancel_requested_at IS NULL AND source.deleted_at IS NULL;
    IF NOT FOUND OR captured_epoch IS NULL THEN
        RAISE EXCEPTION 'source file receipt requires a transaction fence' USING ERRCODE = '42501';
    END IF;
    SELECT stored.* INTO candidate FROM app.job_file_candidates stored
      JOIN app.job_file_candidate_sets frozen ON frozen.job_id = stored.job_id AND frozen.account_id = stored.account_id
          AND frozen.source_id = stored.source_id
     WHERE stored.job_id = NEW.job_id AND stored.account_id = NEW.account_id
       AND stored.source_id = NEW.source_id AND stored.relative_name = NEW.relative_name
       AND stored.action = 'process' AND stored.observed_size = NEW.size_bytes
       AND stored.export_date = substring(NEW.relative_name FROM '^HealthAutoExport-([0-9]{4}-[0-9]{2}-[0-9]{2})\.json$')::date
       -- pgx encodes timestamps at microsecond precision; identity retains all ns.
       AND extract(epoch FROM NEW.modified_at) = stored.modified_sec::numeric + (stored.modified_ns / 1000)::numeric / 1000000;
    IF NOT FOUND THEN RAISE EXCEPTION 'invalid source file receipt manifest' USING ERRCODE = '22023'; END IF;
    NEW.observed_identity := COALESCE(candidate.observed_identity,
        format('v1:%s:%s:%s:%s:%s:%s:%s',candidate.observed_size,candidate.modified_sec,candidate.modified_ns,
            candidate.device,candidate.inode,candidate.ctime_sec,candidate.ctime_ns));
    IF EXISTS (SELECT 1 FROM app.workout_import_events event
        WHERE event.account_id = NEW.account_id AND event.source_id = NEW.source_id
          AND event.job_id = NEW.job_id AND event.source_file_id = NEW.id AND event.kind = 'skipped_live_owner') THEN
        NEW.ownership_blocked_epoch := captured_epoch;
    END IF;
    RETURN NEW;
END;
$function$;
-- +goose StatementEnd

CREATE TRIGGER source_files_receipt_before_write BEFORE INSERT OR UPDATE ON app.source_files
FOR EACH ROW EXECUTE FUNCTION app.stamp_ingest_source_file_receipt();

-- Full replacement retains 027's exact private capability runtime lookup. The
-- four-argument fence (including 028's nonlocking generation check) is untouched.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.read_ingest_candidate_actions(job uuid, worker text, lease uuid, candidates jsonb)
RETURNS TABLE(relative_name text, action text) LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
DECLARE
    target_source uuid;
    target_account uuid := app.current_account_id();
    current_epoch bigint;
    parameters jsonb;
    effective_force boolean;
    ancestors uuid[];
    item jsonb;
BEGIN
    IF jsonb_typeof(candidates) IS DISTINCT FROM 'array' THEN
        RAISE EXCEPTION 'invalid ingest file manifest' USING ERRCODE = '22023';
    END IF;
    IF jsonb_array_length(candidates) > 10000 THEN
        RAISE EXCEPTION 'invalid ingest file manifest' USING ERRCODE = '22023';
    END IF;
    FOR item IN SELECT value FROM jsonb_array_elements(candidates) LOOP
        IF jsonb_typeof(item) IS DISTINCT FROM 'object' THEN
            RAISE EXCEPTION 'invalid ingest file manifest' USING ERRCODE = '22023';
        END IF;
        IF NOT item ?& ARRAY['name','export_date','size','modified_sec','modified_ns','device','inode','ctime_sec','ctime_ns','action'] OR
           EXISTS (SELECT 1 FROM jsonb_object_keys(item) key WHERE key NOT IN
               ('name','export_date','size','modified_sec','modified_ns','device','inode','ctime_sec','ctime_ns','action','observed_identity')) OR
           (SELECT count(*) FROM jsonb_object_keys(item)) <> 10 + (item ? 'observed_identity')::integer OR
           jsonb_typeof(item->'name') IS DISTINCT FROM 'string' OR item->>'name' !~ '^HealthAutoExport-[0-9]{4}-[0-9]{2}-[0-9]{2}\.json$' OR
           jsonb_typeof(item->'export_date') IS DISTINCT FROM 'string' OR item->>'export_date' !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' OR
           jsonb_typeof(item->'action') IS DISTINCT FROM 'string' OR item->>'action' NOT IN ('process','skip') OR
           EXISTS (SELECT 1 FROM unnest(ARRAY['size','modified_sec','modified_ns','device','inode','ctime_sec','ctime_ns']) field
               WHERE jsonb_typeof(item->field) IS DISTINCT FROM 'number' OR
                   item->>field !~ CASE WHEN field IN ('modified_sec','ctime_sec') THEN '^-?[0-9]+$' ELSE '^[0-9]+$' END) OR
           (item ? 'observed_identity' AND (jsonb_typeof(item->'observed_identity') IS DISTINCT FROM 'string' OR
               length(item->>'observed_identity') > 512 OR item->>'observed_identity' !~ '^v2:rclone:[0-9a-f]{64}$')) THEN
            RAISE EXCEPTION 'invalid ingest file manifest' USING ERRCODE = '22023';
        END IF;
        IF (item->>'modified_ns')::numeric NOT BETWEEN 0 AND 999999999 OR
           (item->>'ctime_ns')::numeric NOT BETWEEN 0 AND 999999999 OR
           (item->>'size')::numeric > 9223372036854775807 OR
           (item->>'modified_sec')::numeric NOT BETWEEN -9223372036854775808 AND 9223372036854775807 OR
           (item->>'ctime_sec')::numeric NOT BETWEEN -9223372036854775808 AND 9223372036854775807 OR
           (item->>'device')::numeric > 18446744073709551615 OR (item->>'inode')::numeric > 18446744073709551615 OR
           to_char((item->>'export_date')::date, 'YYYY-MM-DD') <> item->>'export_date' THEN
            RAISE EXCEPTION 'invalid ingest file manifest' USING ERRCODE = '22023';
        END IF;
    END LOOP;
    IF (SELECT count(DISTINCT value->>'name') FROM jsonb_array_elements(candidates) value) <> jsonb_array_length(candidates) THEN
        RAISE EXCEPTION 'invalid ingest file manifest' USING ERRCODE = '22023';
    END IF;

    SELECT fence.source_id INTO target_source FROM app.fence_ingest_job(job, worker, lease, COALESCE((
        SELECT capability.runtime_version FROM app.ingest_write_capabilities capability
        WHERE capability.backend_pid = pg_backend_pid() AND capability.transaction_id = txid_current()
          AND capability.account_id = target_account AND capability.job_id = job
          AND capability.worker_id = worker AND capability.lease_token = lease
    ),26)) fence;
    IF NOT FOUND THEN RETURN; END IF;

    -- Frozen decisions precede even lineage validation. Metadata/set matching is
    -- still enforced by record_ingest_file_manifest; terminal checkpoints resume
    -- through the worker, never by rewriting a frozen process to skip.
    IF EXISTS (SELECT 1 FROM app.job_file_candidate_sets s WHERE s.job_id = job AND s.account_id = target_account) THEN
        RETURN QUERY SELECT candidate->>'name',COALESCE(stored.action,'process')
        FROM jsonb_array_elements(candidates) candidate
        LEFT JOIN app.job_file_candidates stored ON stored.job_id = job AND stored.account_id = target_account
            AND stored.relative_name = candidate->>'name'
        ORDER BY (candidate->>'name') COLLATE "C";
        RETURN;
    END IF;
    ancestors := app.ingest_retry_ancestor_jobs(job,target_account,target_source);
    SELECT j.parameters INTO parameters FROM app.jobs j WHERE j.id = job AND j.account_id = target_account;
    effective_force := parameters->>'mode' = 'bounded' AND COALESCE((parameters->>'reprocessAllFiles')::boolean,true);
    SELECT account.workout_ownership_epoch INTO current_epoch FROM app.accounts account
     WHERE account.id = target_account AND account.state = 'active';
    IF NOT FOUND THEN RETURN; END IF;

    RETURN QUERY
    SELECT candidate.name,CASE
        WHEN EXISTS (SELECT 1 FROM app.source_files own_file WHERE own_file.job_id = job AND own_file.account_id = target_account
            AND own_file.relative_name = candidate.name AND own_file.state IN ('succeeded','failed')) THEN 'process'
        WHEN EXISTS (
            SELECT 1 FROM unnest(ancestors) ancestor(id)
            JOIN app.source_files file ON file.job_id = ancestor.id AND file.account_id = target_account
                AND file.source_id = target_source AND file.relative_name = candidate.name AND file.state = 'succeeded'
                AND file.checksum_sha256 IS NOT NULL AND file.size_bytes = candidate.size
            -- The trusted receipt survives private manifest retention and later
            -- inventory replacement. Historical NULL receipts instead require
            -- the current inventory to prove this exact successful file.
            WHERE candidate.export_date = substring(file.relative_name FROM '^HealthAutoExport-([0-9]{4}-[0-9]{2}-[0-9]{2})\.json$')::date
              AND extract(epoch FROM file.modified_at) = candidate.modified_sec::numeric + (candidate.modified_ns / 1000)::numeric / 1000000
              AND ((file.observed_identity = candidate.identity
                    AND (file.ownership_blocked_epoch IS NULL OR file.ownership_blocked_epoch = current_epoch))
                OR (file.observed_identity IS NULL AND inventory.successful_job_id = file.job_id
                    AND inventory.successful_checksum = file.checksum_sha256 AND inventory.observed_identity = candidate.identity
                    AND inventory.observed_size = candidate.size AND inventory.export_date = candidate.export_date
                    AND inventory.observed_modified_at = file.modified_at
                    AND (inventory.ownership_blocked_epoch IS NULL OR inventory.ownership_blocked_epoch = current_epoch)))
        ) THEN 'skip'
        WHEN effective_force THEN 'process'
        WHEN inventory.successful_checksum IS NOT NULL AND inventory.observed_identity = candidate.identity
            AND (inventory.ownership_blocked_epoch IS NULL OR inventory.ownership_blocked_epoch = current_epoch) THEN 'skip'
        ELSE 'process' END
    FROM (SELECT input.*,COALESCE(input.observed_identity,
        format('v1:%s:%s:%s:%s:%s:%s:%s',input.size,input.modified_sec,input.modified_ns,
            input.device,input.inode,input.ctime_sec,input.ctime_ns)) AS identity
        FROM jsonb_to_recordset(candidates) AS input(name text,export_date date,size bigint,modified_sec bigint,modified_ns integer,
            device numeric,inode numeric,ctime_sec bigint,ctime_ns integer,observed_identity text)) candidate
    LEFT JOIN app.source_objects inventory ON inventory.account_id = target_account AND inventory.source_id = target_source
        AND inventory.relative_name = candidate.name
    ORDER BY candidate.name COLLATE "C";
END;
$function$;
-- +goose StatementEnd

-- Replace 004's table-level read grants: otherwise they expose every new column.
-- Preserve the fifteen pre-existing columns and the read_owned_job_files ABI.
-- Worker INSERT/UPDATE remain table-level and are guarded by the receipt trigger.
REVOKE SELECT ON app.source_files FROM workouts_api, workouts_worker;
GRANT SELECT(id,account_id,source_id,job_id,relative_name,size_bytes,modified_at,checksum_sha256,
    state,failure_code,failure_summary,processing_started_at,processed_at,created_at,updated_at)
    ON app.source_files TO workouts_api, workouts_worker;
GRANT SELECT(observed_identity,ownership_blocked_epoch) ON app.source_files TO workouts_security_owner;
-- The existing invoker INSERT trigger calls this pure validator as the API role.
GRANT EXECUTE ON FUNCTION app.valid_ingest_child_parameters(jsonb) TO workouts_security_owner, workouts_api;
GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.ingest_retry_ancestor_jobs(uuid,uuid,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.stamp_ingest_source_file_receipt() OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
REVOKE ALL ON FUNCTION app.ingest_retry_ancestor_jobs(uuid,uuid,uuid),app.stamp_ingest_source_file_receipt()
    FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles, workouts_migration;
REVOKE ALL ON FUNCTION app.read_ingest_candidate_actions(uuid,text,uuid,jsonb)
    FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;
GRANT EXECUTE ON FUNCTION app.read_ingest_candidate_actions(uuid,text,uuid,jsonb) TO workouts_worker;

-- Reliable phase snapshots share the append-only journal, not the optional
-- diagnostic writer's 1000-event budget. Payloads contain only safe counters.
CREATE INDEX job_events_ingest_phase_latest_idx ON app.job_events(job_id, id DESC)
    WHERE code LIKE 'ingest-phase-%';

-- +goose StatementBegin
CREATE FUNCTION app.record_ingest_phase(target_job_id uuid, claiming_worker text, current_lease_token uuid,
    phase text, phase_current bigint, phase_total bigint) RETURNS boolean
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
DECLARE
    event_message text;
    event_code text;
    event_fields jsonb;
    previous_event app.job_events%ROWTYPE;
    job_attempt integer;
BEGIN
    SELECT message INTO event_message FROM (VALUES
        ('resuming', 'Resuming incomplete sync job.'),
        ('preparing', 'Preparing to read workout files.'),
        ('discovering', 'Looking for workout files that need to be processed.'),
        ('importing', 'Parsing workout files and importing new and changed workouts.'),
        ('analyzing_imported_routes', 'Analyzing map data for imported routes.'),
        ('analyzing_existing_routes', 'Analyzing map data for existing routes.'),
        ('updating_time_zones', 'Determining and updating workout time zones.'),
        ('cleanup', 'Cleaning up temporary files.')
    ) allowed(name, message) WHERE name = phase;
    IF NOT FOUND OR target_job_id IS NULL OR claiming_worker IS NULL OR claiming_worker = ''
        OR current_lease_token IS NULL OR phase_current < 0 OR phase_total < 0
        OR phase_total < phase_current THEN
        RAISE EXCEPTION 'invalid ingest phase arguments' USING ERRCODE = '22023';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM app.schema_metadata WHERE singleton
        AND schema_version >= 29 AND minimum_runtime_version <= 30) THEN
        RAISE EXCEPTION 'ingest phase runtime 30 is required' USING ERRCODE = '55000';
    END IF;
    -- Return false for stale calls even when this transaction already has a
    -- capability; the shared fence otherwise rejects changed tokens by exception.
    PERFORM 1 FROM app.jobs WHERE id = target_job_id AND account_id = app.current_account_id()
        AND kind IN ('manual_ingest_source', 'scheduled_ingest_source') AND status = 'running'
        AND cancel_requested_at IS NULL AND worker_id = claiming_worker AND lease_token = current_lease_token
        AND lease_expires_at >= clock_timestamp();
    IF NOT FOUND THEN RETURN false; END IF;
    -- The fence takes source, parent, then child locks and validates account,
    -- kind, cancellation, worker and live lease before granting write authority.
    PERFORM source_id FROM app.fence_ingest_job(target_job_id, claiming_worker, current_lease_token, 30);
    IF NOT FOUND THEN RETURN false; END IF;
    SELECT attempt INTO job_attempt FROM app.jobs WHERE id = target_job_id AND account_id = app.current_account_id();
    event_code := 'ingest-phase-' || replace(phase, '_', '-');
    event_fields := jsonb_strip_nulls(jsonb_build_object('current', phase_current, 'total', phase_total, 'attempt', job_attempt));
    IF app.valid_job_safe_fields(event_fields) IS NOT TRUE THEN
        RAISE EXCEPTION 'invalid ingest phase arguments' USING ERRCODE = '22023';
    END IF;
    -- Identity order is authoritative even inside one transaction; timestamps
    -- are transaction-scoped and cannot order progress ticks.
    SELECT * INTO previous_event FROM app.job_events
    WHERE job_id = target_job_id AND account_id = app.current_account_id() AND code LIKE 'ingest-phase-%'
    ORDER BY id DESC LIMIT 1;
    IF FOUND AND previous_event.code = event_code AND previous_event.fields->>'attempt' = job_attempt::text THEN
        IF previous_event.fields = event_fields THEN RETURN true; END IF;
        IF (previous_event.fields->>'current')::bigint IS NOT NULL AND
            (phase_current IS NULL OR phase_current < (previous_event.fields->>'current')::bigint) THEN
            RAISE EXCEPTION 'ingest phase current cannot regress' USING ERRCODE = '22023';
        END IF;
    END IF;
    INSERT INTO app.job_events(account_id, job_id, severity, code, safe_message, fields)
    VALUES(app.current_account_id(), target_job_id, 'info', event_code, event_message, event_fields);
    RETURN true;
END;
$function$;
-- +goose StatementEnd

GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.record_ingest_phase(uuid,text,uuid,text,bigint,bigint) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
REVOKE ALL ON FUNCTION app.record_ingest_phase(uuid,text,uuid,text,bigint,bigint)
    FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles, workouts_migration;
GRANT EXECUTE ON FUNCTION app.record_ingest_phase(uuid,text,uuid,text,bigint,bigint) TO workouts_worker;

-- BEGIN PRECISE NEAR-IDLE COVERAGE RECONCILIATION
-- Selection proofs are not matching results. Neither preparation nor observing
-- a catalog may manufacture successful matching provenance.
ALTER TABLE app.coverage_reconciliation_state
    ADD COLUMN catalog_revision bigint NOT NULL DEFAULT 0 CHECK (catalog_revision>=0),
    ADD COLUMN catalog_fingerprint text NOT NULL DEFAULT '',
    ADD COLUMN selection_fingerprint text NOT NULL DEFAULT '',
    ADD COLUMN selection_policy text NOT NULL DEFAULT '',
    ADD COLUMN active_heads jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN matching_descriptor jsonb NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE app.coverage_reconciliation_accounts
    ADD COLUMN scan_required boolean NOT NULL DEFAULT false,
    ADD COLUMN queue_retry_at timestamptz NOT NULL DEFAULT '-infinity';
CREATE TABLE app.coverage_reconciliation_wakes (
    account_id uuid NOT NULL,
    transaction_id bigint NOT NULL DEFAULT txid_current() CHECK (transaction_id>0),
    PRIMARY KEY(account_id,transaction_id)
);

-- +goose StatementBegin
CREATE FUNCTION app.wake_coverage_reconciliation() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
BEGIN
    INSERT INTO app.coverage_reconciliation_wakes(account_id) SELECT NEW.account_id
    FROM app.accounts account WHERE account.id=NEW.account_id AND account.state='active'
    ON CONFLICT(account_id,transaction_id) DO NOTHING;
    RETURN NEW;
END;
$function$;
-- +goose StatementEnd
CREATE TRIGGER ingest_coverage_handoffs_reconciliation_wake AFTER INSERT ON app.ingest_coverage_handoffs
    FOR EACH ROW EXECUTE FUNCTION app.wake_coverage_reconciliation();
CREATE TRIGGER jobs_ingest_reconciliation_wake AFTER UPDATE OF status ON app.jobs FOR EACH ROW
    WHEN (NEW.kind IN ('manual_ingest','scheduled_ingest','manual_ingest_source','scheduled_ingest_source')
        AND NEW.status IN ('succeeded','partially_succeeded','failed','cancelled') AND NEW.status IS DISTINCT FROM OLD.status)
    EXECUTE FUNCTION app.wake_coverage_reconciliation();

-- No account FK here: point writes must not acquire an account-generation lock.
-- Version, not count/bounds/content, detects every supported point mutation.
CREATE TABLE app.coverage_route_changes (
    account_id uuid NOT NULL,
    workout_id uuid NOT NULL,
    mutation_version bigint NOT NULL DEFAULT 1 CHECK (mutation_version>0),
    resolved_version bigint NOT NULL DEFAULT 0 CHECK (resolved_version>=0),
    next_attempt_at timestamptz NOT NULL DEFAULT '-infinity',
    PRIMARY KEY(account_id,workout_id),
    FOREIGN KEY(workout_id,account_id) REFERENCES app.workouts(id,account_id) ON DELETE CASCADE
);
CREATE INDEX coverage_route_changes_dirty_idx ON app.coverage_route_changes(account_id,workout_id)
    WHERE mutation_version>resolved_version;
CREATE TABLE app.coverage_route_selection_proofs (
    account_id uuid NOT NULL,
    workout_id uuid NOT NULL,
    content_sha256 bytea NOT NULL CHECK (octet_length(content_sha256)=32),
    source_id uuid NOT NULL,
    route_input_revision bigint NOT NULL CHECK (route_input_revision>0),
    route_input_sha256 bytea NOT NULL CHECK (octet_length(route_input_sha256)=32),
    mutation_version bigint NOT NULL CHECK (mutation_version>=0),
    catalog_revision bigint NOT NULL CHECK (catalog_revision>=0),
    catalog_fingerprint text NOT NULL CHECK (catalog_fingerprint ~ '^[0-9a-f]{64}$'),
    selection_fingerprint text NOT NULL CHECK (selection_fingerprint ~ '^[0-9a-f]{64}$'),
    selection_policy text NOT NULL CHECK (selection_policy IN ('route-selection-v1:auto-add=true','route-selection-v1:auto-add=false')),
    readiness text NOT NULL CHECK (readiness IN ('map_data_ready','pending','unavailable')),
    reason text,
    region_ids text[] NOT NULL,
    generations jsonb NOT NULL CHECK (CASE WHEN readiness='map_data_ready' THEN app.valid_coverage_generation_vector(generations) ELSE generations='[]'::jsonb END),
    PRIMARY KEY(account_id,workout_id),
    FOREIGN KEY(workout_id,account_id) REFERENCES app.workouts(id,account_id) ON DELETE CASCADE,
    CHECK ((readiness='map_data_ready' AND reason IS NULL AND cardinality(region_ids)>0)
        OR (readiness='pending' AND reason='region_not_active' AND cardinality(region_ids)=0)
        OR (readiness='unavailable' AND reason='no_provider_region' AND cardinality(region_ids)=0))
);
CREATE TABLE app.coverage_applied_match_proofs (
    account_id uuid NOT NULL,
    workout_id uuid NOT NULL,
    route_input_revision bigint NOT NULL,
    route_input_sha256 bytea NOT NULL CHECK (octet_length(route_input_sha256)=32),
    generations jsonb NOT NULL CHECK (app.valid_coverage_generation_vector(generations)),
    matching_descriptor jsonb NOT NULL,
    job_id uuid NOT NULL,
    PRIMARY KEY(account_id,workout_id),
    FOREIGN KEY(workout_id,account_id) REFERENCES app.workouts(id,account_id) ON DELETE CASCADE
);
CREATE INDEX coverage_route_selection_proofs_catalog_idx ON app.coverage_route_selection_proofs(catalog_revision DESC);
ALTER TABLE app.coverage_job_contexts ADD COLUMN matching_descriptor jsonb;
GRANT SELECT(matching_descriptor),INSERT(matching_descriptor),UPDATE(matching_descriptor)
    ON app.coverage_job_contexts TO workouts_security_owner;

-- +goose StatementBegin
CREATE FUNCTION app.coverage_matching_descriptor(rules text,sampling text,policy text,minimum double precision,contract text)
RETURNS jsonb LANGUAGE sql IMMUTABLE SET search_path=pg_catalog AS $function$
    SELECT jsonb_build_object('rules',rules,'sampling',sampling,'pathPolicy',policy,
        'minimumTraversalMeters',minimum,'productionMatcherContract',contract)
$function$;
-- +goose StatementEnd

-- Capture the actual producer contract, not a later mutable observation. Existing
-- immutable contexts are known shipped V1 history; no selection proof is backfilled.
-- +goose StatementBegin
CREATE FUNCTION app.capture_coverage_matching_descriptor() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
BEGIN
    NEW.matching_descriptor:=app.coverage_matching_descriptor(NEW.rules_version,NEW.sampling_version,
        NEW.path_policy_version,NEW.minimum_traversal_meters,NEW.matcher_contract_version);
    RETURN NEW;
END;
$function$;
-- +goose StatementEnd
CREATE TRIGGER coverage_job_contexts_matching_descriptor BEFORE INSERT ON app.coverage_job_contexts
    FOR EACH ROW EXECUTE FUNCTION app.capture_coverage_matching_descriptor();
ALTER TABLE app.coverage_job_contexts NO FORCE ROW LEVEL SECURITY;
UPDATE app.coverage_job_contexts SET matching_descriptor=app.coverage_matching_descriptor(rules_version,sampling_version,
    path_policy_version,minimum_traversal_meters,matcher_contract_version)
    WHERE matcher_contract_version='coverage-production-v1';
ALTER TABLE app.coverage_job_contexts FORCE ROW LEVEL SECURITY;

-- +goose StatementBegin
CREATE FUNCTION app.stamp_coverage_applied_match_proof() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
BEGIN
    INSERT INTO app.coverage_applied_match_proofs(account_id,workout_id,route_input_revision,route_input_sha256,
        generations,matching_descriptor,job_id)
    SELECT NEW.account_id,NEW.workout_id,context.route_input_revision,context.route_input_sha256,
        context.target_generations,parent.matching_descriptor,child.id
    FROM app.jobs child JOIN app.coverage_route_job_contexts context ON context.job_id=child.id AND context.account_id=child.account_id
    JOIN app.coverage_job_contexts parent ON parent.job_id=child.parent_job_id AND parent.account_id=child.account_id
    WHERE child.id=NEW.target_job_id AND child.account_id=NEW.account_id AND child.kind='coverage_update_route'
        AND context.workout_id=NEW.workout_id AND context.result_outcome IN ('applied','no_evidence')
        AND context.route_input_revision=NEW.applied_route_input_revision AND context.route_input_sha256=NEW.applied_route_input_sha256
        AND app.coverage_generation_vectors_equal(context.target_generations,NEW.applied_generations)
        AND parent.matching_descriptor IS NOT NULL
    ON CONFLICT(account_id,workout_id) DO UPDATE SET route_input_revision=EXCLUDED.route_input_revision,
        route_input_sha256=EXCLUDED.route_input_sha256,generations=EXCLUDED.generations,
        matching_descriptor=EXCLUDED.matching_descriptor,job_id=EXCLUDED.job_id;
    RETURN NEW;
END;
$function$;
-- +goose StatementEnd
CREATE TRIGGER workout_coverage_states_applied_match_proof AFTER UPDATE OF processing_finished_at ON app.workout_coverage_states
    FOR EACH ROW WHEN (NEW.processing_state='current' AND NEW.processing_finished_at IS DISTINCT FROM OLD.processing_finished_at)
    EXECUTE FUNCTION app.stamp_coverage_applied_match_proof();

-- Historical applied provenance is accepted only from the exact successful,
-- retained immutable context, including minimum traversal and matcher contract.
ALTER TABLE app.workout_coverage_states NO FORCE ROW LEVEL SECURITY;
ALTER TABLE app.jobs NO FORCE ROW LEVEL SECURITY;
ALTER TABLE app.coverage_route_job_contexts NO FORCE ROW LEVEL SECURITY;
ALTER TABLE app.coverage_job_contexts NO FORCE ROW LEVEL SECURITY;
-- +goose StatementBegin
DO $block$
BEGIN
    WITH candidates AS MATERIALIZED (
    SELECT state.account_id,state.workout_id,state.applied_route_input_revision,state.applied_route_input_sha256,
        state.applied_generations,parent.matching_descriptor,child.id AS job_id
    FROM app.workout_coverage_states state JOIN app.jobs child ON child.account_id=state.account_id
        AND (child.id=state.target_job_id OR (state.target_job_id IS NULL AND state.processing_finished_at=child.terminal_at))
    JOIN app.coverage_route_job_contexts context ON context.job_id=child.id AND context.account_id=child.account_id
    JOIN app.coverage_job_contexts parent ON parent.job_id=child.parent_job_id AND parent.account_id=child.account_id
    WHERE state.processing_state='current' AND child.status='succeeded' AND context.result_outcome IN ('applied','no_evidence')
        AND context.workout_id=state.workout_id AND context.route_input_revision=state.applied_route_input_revision
        AND context.route_input_sha256=state.applied_route_input_sha256
        AND parent.rules_version=state.applied_rules_version AND parent.sampling_version=state.applied_sampling_version
        AND parent.path_policy_version=state.applied_path_policy_version AND parent.matching_descriptor IS NOT NULL
        AND app.coverage_generation_vectors_equal(context.target_generations,state.applied_generations)
    )
    INSERT INTO app.coverage_applied_match_proofs
    SELECT DISTINCT ON (candidate.account_id,candidate.workout_id) candidate.* FROM candidates candidate
    WHERE NOT EXISTS (SELECT 1 FROM candidates other WHERE other.account_id=candidate.account_id AND other.workout_id=candidate.workout_id
        AND other.matching_descriptor IS DISTINCT FROM candidate.matching_descriptor)
    ORDER BY candidate.account_id,candidate.workout_id,candidate.job_id;
END;
$block$;
-- +goose StatementEnd
ALTER TABLE app.workout_coverage_states FORCE ROW LEVEL SECURITY;
ALTER TABLE app.jobs FORCE ROW LEVEL SECURITY;
ALTER TABLE app.coverage_route_job_contexts FORCE ROW LEVEL SECURITY;
ALTER TABLE app.coverage_job_contexts FORCE ROW LEVEL SECURITY;

-- All INSERT/UPDATE/DELETE point paths, including repairs and identical bounds,
-- coalesce keys once per statement. The existing summary invalidator is retained.
-- +goose StatementBegin
CREATE FUNCTION app.dirty_coverage_route_inputs() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
BEGIN
    INSERT INTO app.coverage_route_changes(account_id,workout_id)
    SELECT DISTINCT changed.account_id,changed.workout_id FROM changed_points changed
    JOIN app.workouts workout ON workout.id=changed.workout_id AND workout.account_id=changed.account_id
    JOIN app.accounts account ON account.id=workout.account_id AND account.state='active'
    JOIN app.sources source ON source.id=workout.source_id AND source.account_id=workout.account_id AND source.deleted_at IS NULL
    WHERE workout.deletion_requested_at IS NULL
    ORDER BY changed.account_id,changed.workout_id
    ON CONFLICT(account_id,workout_id) DO UPDATE SET mutation_version=app.coverage_route_changes.mutation_version+1,
        next_attempt_at='-infinity';
    INSERT INTO app.coverage_reconciliation_wakes(account_id)
    SELECT DISTINCT changed.account_id FROM changed_points changed
    JOIN app.workouts workout ON workout.id=changed.workout_id AND workout.account_id=changed.account_id
    JOIN app.accounts account ON account.id=workout.account_id AND account.state='active'
    JOIN app.sources source ON source.id=workout.source_id AND source.account_id=workout.account_id AND source.deleted_at IS NULL
    WHERE workout.deletion_requested_at IS NULL ORDER BY changed.account_id
    ON CONFLICT(account_id,transaction_id) DO NOTHING;
    RETURN NULL;
END;
$function$;
-- +goose StatementEnd
CREATE TRIGGER workout_route_points_coverage_dirty_insert AFTER INSERT ON app.workout_route_points
    REFERENCING NEW TABLE AS changed_points FOR EACH STATEMENT EXECUTE FUNCTION app.dirty_coverage_route_inputs();
CREATE TRIGGER workout_route_points_coverage_dirty_update AFTER UPDATE ON app.workout_route_points
    REFERENCING NEW TABLE AS changed_points FOR EACH STATEMENT EXECUTE FUNCTION app.dirty_coverage_route_inputs();
CREATE TRIGGER workout_route_points_coverage_dirty_update_old AFTER UPDATE ON app.workout_route_points
    REFERENCING OLD TABLE AS changed_points FOR EACH STATEMENT EXECUTE FUNCTION app.dirty_coverage_route_inputs();
CREATE TRIGGER workout_route_points_coverage_dirty_delete AFTER DELETE ON app.workout_route_points
    REFERENCING OLD TABLE AS changed_points FOR EACH STATEMENT EXECUTE FUNCTION app.dirty_coverage_route_inputs();

-- +goose StatementBegin
CREATE FUNCTION app.coverage_applied_target_current(target_account uuid,target_workout uuid,revision bigint,digest bytea,
    vector jsonb,rules text,sampling text,policy text,minimum double precision) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
    SELECT EXISTS (SELECT 1 FROM app.coverage_applied_match_proofs proof
        WHERE proof.account_id=target_account AND proof.workout_id=target_workout
        AND proof.route_input_revision=revision AND proof.route_input_sha256=digest
        AND app.coverage_generation_vectors_equal(proof.generations,vector)
        AND proof.matching_descriptor=app.coverage_matching_descriptor(rules,sampling,policy,minimum,app.current_coverage_matcher_contract()))
$function$;
-- +goose StatementEnd

-- The cached digest is trusted only while the exact point-mutation version and
-- immutable content/source identity still match. Otherwise use the canonical hash.
-- +goose StatementBegin
CREATE FUNCTION app.coverage_verified_route_digest(target_account uuid,target_workout uuid) RETURNS bytea
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
DECLARE digest bytea;
BEGIN
    SELECT proof.route_input_sha256 INTO digest FROM app.coverage_route_selection_proofs proof
    JOIN app.workouts workout ON workout.id=proof.workout_id AND workout.account_id=proof.account_id
    LEFT JOIN app.coverage_route_changes change ON change.account_id=proof.account_id AND change.workout_id=proof.workout_id
    WHERE proof.account_id=target_account AND proof.workout_id=target_workout
        AND proof.content_sha256=workout.content_sha256 AND proof.source_id=workout.source_id
        AND proof.mutation_version=COALESCE(change.mutation_version,0);
    IF FOUND THEN RETURN digest; END IF;
    RETURN app.ingest_coverage_snapshot_digest(target_account,target_workout);
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.read_coverage_catalog_observation() RETURNS TABLE(catalog_revision bigint,last_osm_event_id bigint,
    catalog_fingerprint text,selection_fingerprint text,selection_policy text,active_heads jsonb,matching_descriptor jsonb)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
    SELECT GREATEST(catalog.catalog_revision,COALESCE((SELECT max(proof.catalog_revision) FROM app.coverage_route_selection_proofs proof),0)),
        last_osm_event_id,catalog_fingerprint,selection_fingerprint,selection_policy,active_heads,matching_descriptor
    FROM app.coverage_reconciliation_state catalog WHERE singleton
$function$;
-- +goose StatementEnd

-- Identical observations do no DML, including no account enumeration. A lagged
-- replica cannot discard accepted heads, policy, matching identity or progress.
-- +goose StatementBegin
CREATE FUNCTION app.observe_coverage_catalog(revision bigint,event_id bigint,full_fingerprint text,selection_fp text,
    heads jsonb,selection_policy_value text,descriptor jsonb,configured jsonb) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
DECLARE prior app.coverage_reconciliation_state%ROWTYPE; campaign boolean; next_revision bigint;
BEGIN
    IF revision IS NULL OR revision<0 OR event_id IS NULL OR event_id<0
        OR full_fingerprint IS NULL OR full_fingerprint !~ '^[0-9a-f]{64}$'
        OR selection_fp IS NULL OR selection_fp !~ '^[0-9a-f]{64}$'
        OR selection_policy_value IS NULL OR selection_policy_value NOT IN ('route-selection-v1:auto-add=true','route-selection-v1:auto-add=false')
        OR jsonb_typeof(heads) IS DISTINCT FROM 'object' OR jsonb_typeof(descriptor) IS DISTINCT FROM 'object'
        OR NOT descriptor ?& ARRAY['rules','sampling','pathPolicy','minimumTraversalMeters','productionMatcherContract']
        OR descriptor->>'productionMatcherContract' !~ '^coverage-production-v[1-9][0-9]*$'
        OR (descriptor->>'minimumTraversalMeters')::double precision NOT BETWEEN 0.1 AND 100 THEN
        RAISE EXCEPTION 'invalid coverage catalog observation' USING ERRCODE='22023';
    END IF;
    SELECT * INTO prior FROM app.coverage_reconciliation_state WHERE singleton;
    IF revision<COALESCE((SELECT max(proof.catalog_revision) FROM app.coverage_route_selection_proofs proof),0) THEN RETURN false; END IF;
    IF revision<prior.catalog_revision OR event_id<prior.last_osm_event_id THEN RETURN false; END IF;
    IF revision=prior.catalog_revision AND prior.catalog_fingerprint<>'' AND full_fingerprint<>prior.catalog_fingerprint THEN
        RAISE EXCEPTION 'conflicting coverage catalog revision' USING ERRCODE='40001';
    END IF;
    IF (revision,event_id,full_fingerprint,selection_fp,heads,selection_policy_value,descriptor)
        IS NOT DISTINCT FROM (prior.catalog_revision,prior.last_osm_event_id,prior.catalog_fingerprint,
            prior.selection_fingerprint,prior.active_heads,prior.selection_policy,prior.matching_descriptor) THEN RETURN true; END IF;
    SELECT * INTO prior FROM app.coverage_reconciliation_state WHERE singleton FOR UPDATE;
    IF revision<COALESCE((SELECT max(proof.catalog_revision) FROM app.coverage_route_selection_proofs proof),0) THEN RETURN false; END IF;
    IF revision<prior.catalog_revision OR event_id<prior.last_osm_event_id THEN RETURN false; END IF;
    IF revision=prior.catalog_revision AND prior.catalog_fingerprint<>'' AND full_fingerprint<>prior.catalog_fingerprint THEN
        RAISE EXCEPTION 'conflicting coverage catalog revision' USING ERRCODE='40001';
    END IF;
    IF (revision,event_id,full_fingerprint,selection_fp,heads,selection_policy_value,descriptor)
        IS NOT DISTINCT FROM (prior.catalog_revision,prior.last_osm_event_id,prior.catalog_fingerprint,
            prior.selection_fingerprint,prior.active_heads,prior.selection_policy,prior.matching_descriptor) THEN RETURN true; END IF;
    campaign:=(full_fingerprint,selection_fp,heads,selection_policy_value,descriptor) IS DISTINCT FROM
        (prior.catalog_fingerprint,prior.selection_fingerprint,prior.active_heads,prior.selection_policy,prior.matching_descriptor);
    next_revision:=prior.desired_revision;
    IF campaign THEN
        SELECT GREATEST(prior.desired_revision,COALESCE(max(GREATEST(account.desired_revision,
            account.completed_revision,COALESCE(account.active_revision,0))),0))+1 INTO next_revision
        FROM app.coverage_reconciliation_accounts account;
    END IF;
    UPDATE app.coverage_reconciliation_state SET catalog_revision=revision,last_osm_event_id=event_id,
        catalog_fingerprint=full_fingerprint,selection_fingerprint=selection_fp,active_heads=heads,
        selection_policy=selection_policy_value,matching_descriptor=descriptor,
        matcher_contract_version=descriptor->>'productionMatcherContract',desired_revision=next_revision,observed_at=clock_timestamp()
    WHERE singleton;
    PERFORM app.sync_coverage_region_catalog(configured);
    IF campaign THEN
        INSERT INTO app.coverage_reconciliation_accounts(account_id,desired_revision,next_scan_at)
        SELECT id,GREATEST(next_revision,1),'-infinity' FROM app.accounts WHERE state='active'
        ON CONFLICT(account_id) DO UPDATE SET desired_revision=GREATEST(app.coverage_reconciliation_accounts.desired_revision,
            EXCLUDED.desired_revision),next_scan_at='-infinity';
        UPDATE app.adopted_workout_coverage_refreshes request SET next_attempt_at='-infinity'
        WHERE request.next_attempt_at>'-infinity' AND EXISTS (SELECT 1 FROM app.coverage_route_selection_proofs proof
            WHERE proof.account_id=request.account_id AND proof.workout_id=request.workout_id
                AND (proof.selection_fingerprint,proof.selection_policy) IS DISTINCT FROM (selection_fp,selection_policy_value));
    END IF;
    RETURN true;
END;
$function$;
-- +goose StatementEnd

-- A single proof reader is shared by ordinary campaigns, ingest replay, and the
-- source EOF repair. It never reads points, hashes a route, or writes app state.
-- +goose StatementBegin
CREATE FUNCTION app.coverage_selection_metadata(target_account uuid,target_workout uuid)
RETURNS jsonb LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
    SELECT jsonb_build_object('readiness',proof.readiness,'reason',proof.reason,
        'routeDigest',encode(proof.route_input_sha256,'hex'),'routeRevision',proof.route_input_revision,
        'catalogRevision',catalog.catalog_revision,'catalogFingerprint',catalog.catalog_fingerprint,
        'selectionFingerprint',catalog.selection_fingerprint,'selectionPolicy',catalog.selection_policy,
        'mutationVersion',proof.mutation_version,'generations',CASE WHEN proof.readiness='map_data_ready'
            THEN (SELECT jsonb_agg(jsonb_build_object('regionId',id,'generation',(catalog.active_heads->>id)::bigint) ORDER BY id)
                FROM unnest(proof.region_ids) id) ELSE '[]'::jsonb END)
    FROM app.coverage_route_selection_proofs proof
    JOIN app.workouts workout ON workout.id=proof.workout_id AND workout.account_id=proof.account_id
    JOIN app.workout_coverage_states state ON state.account_id=proof.account_id AND state.workout_id=proof.workout_id
    JOIN app.sources source ON source.id=workout.source_id AND source.account_id=workout.account_id AND source.deleted_at IS NULL
    JOIN app.coverage_reconciliation_state catalog ON catalog.singleton
    LEFT JOIN app.coverage_route_changes change ON change.account_id=proof.account_id AND change.workout_id=proof.workout_id
    WHERE proof.account_id=target_account AND proof.workout_id=target_workout AND workout.deletion_requested_at IS NULL
        AND proof.content_sha256=workout.content_sha256 AND proof.source_id=workout.source_id
        AND proof.route_input_revision=state.route_input_revision AND proof.route_input_sha256=state.route_input_sha256
        AND proof.mutation_version=COALESCE(change.mutation_version,0)
        AND proof.catalog_revision<=catalog.catalog_revision
        AND proof.selection_fingerprint=catalog.selection_fingerprint AND proof.selection_policy=catalog.selection_policy
        AND NOT EXISTS (SELECT 1 FROM unnest(proof.region_ids) id WHERE NOT catalog.active_heads ? id)
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.coverage_route_actionable(target_account uuid,target_workout uuid) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
    SELECT metadata.value IS NULL OR (metadata.value->>'readiness'='map_data_ready' AND NOT EXISTS (
        SELECT 1 FROM app.ingest_coverage_existing_target(target_account,target_workout,
            (metadata.value->>'routeRevision')::bigint,decode(metadata.value->>'routeDigest','hex'),metadata.value->'generations',
            catalog.matching_descriptor->>'rules',catalog.matching_descriptor->>'sampling',catalog.matching_descriptor->>'pathPolicy',
            (catalog.matching_descriptor->>'minimumTraversalMeters')::double precision)))
    FROM app.coverage_reconciliation_state catalog
    CROSS JOIN LATERAL (SELECT app.coverage_selection_metadata(target_account,target_workout) value) metadata WHERE catalog.singleton
$function$;
-- +goose StatementEnd

-- Fenced metadata access: callers never get another account's private proof.
-- +goose StatementBegin
CREATE FUNCTION app.read_coverage_selection_metadata(target_account uuid,target_job uuid,worker text,lease uuid,
    revision bigint,target_workout uuid) RETURNS jsonb
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
DECLARE metadata jsonb; selection jsonb;
BEGIN
    IF NOT app.adopted_coverage_authority_current(target_account,target_job,worker,lease,revision) THEN
        RAISE EXCEPTION 'selection proof authority lost' USING ERRCODE='42501';
    END IF;
    SELECT jsonb_build_object('contentSha256',encode(workout.content_sha256,'hex'),'sourceId',workout.source_id,
        'ownershipEpoch',account.workout_ownership_epoch) INTO metadata
    FROM app.workouts workout JOIN app.accounts account ON account.id=workout.account_id AND account.state='active'
    JOIN app.sources source ON source.id=workout.source_id AND source.account_id=workout.account_id AND source.deleted_at IS NULL
    WHERE workout.account_id=target_account AND workout.id=target_workout AND workout.deletion_requested_at IS NULL
        AND (target_job IS NULL OR workout.source_id=app.read_ingest_coverage_source_id(target_account,target_job,worker,lease));
    IF NOT FOUND THEN RAISE EXCEPTION 'selection proof workout authority lost' USING ERRCODE='40001'; END IF;
    selection:=app.coverage_selection_metadata(target_account,target_workout);
    IF selection IS NULL THEN
        -- A real source resolution can precede global catalog acceptance. Keep
        -- its exact proof available for an externally fenced metadata rebind.
        SELECT jsonb_build_object('readiness',proof.readiness,'reason',proof.reason,'routeRevision',proof.route_input_revision,
            'routeDigest',encode(proof.route_input_sha256,'hex'),'mutationVersion',proof.mutation_version,
            'catalogRevision',proof.catalog_revision,'catalogFingerprint',proof.catalog_fingerprint,
            'selectionFingerprint',proof.selection_fingerprint,'selectionPolicy',proof.selection_policy,'generations',proof.generations)
        INTO selection FROM app.coverage_route_selection_proofs proof
        JOIN app.workouts workout ON workout.id=proof.workout_id AND workout.account_id=proof.account_id
        JOIN app.workout_coverage_states state ON state.workout_id=proof.workout_id AND state.account_id=proof.account_id
        LEFT JOIN app.coverage_route_changes change ON change.account_id=proof.account_id AND change.workout_id=proof.workout_id
        WHERE proof.account_id=target_account AND proof.workout_id=target_workout
            AND proof.content_sha256=workout.content_sha256 AND proof.source_id=workout.source_id
            AND proof.route_input_revision=state.route_input_revision AND proof.route_input_sha256=state.route_input_sha256
            AND proof.mutation_version=COALESCE(change.mutation_version,0);
    END IF;
    RETURN metadata || COALESCE(selection,jsonb_build_object(
        'mutationVersion',COALESCE((SELECT mutation_version FROM app.coverage_route_changes
            WHERE account_id=target_account AND workout_id=target_workout),0)));
END;
$function$;
-- +goose StatementEnd

-- EOF repair is not a catalog campaign. An input proof already established by
-- this import (including negative readiness) belongs to the observer thereafter.
-- +goose StatementBegin
CREATE FUNCTION app.ingest_coverage_route_needs_proof(target_account uuid,target_job uuid,worker text,lease uuid,target_workout uuid)
RETURNS boolean LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
    SELECT app.adopted_coverage_authority_current(target_account,target_job,worker,lease,NULL)
    AND NOT EXISTS (SELECT 1 FROM app.coverage_route_selection_proofs proof
        JOIN app.workouts workout ON workout.account_id=proof.account_id AND workout.id=proof.workout_id
        JOIN app.workout_coverage_states state ON state.account_id=proof.account_id AND state.workout_id=proof.workout_id
        LEFT JOIN app.coverage_route_changes change ON change.account_id=proof.account_id AND change.workout_id=proof.workout_id
        WHERE proof.account_id=target_account AND proof.workout_id=target_workout
            AND proof.source_id=workout.source_id AND proof.content_sha256=workout.content_sha256
            AND proof.route_input_revision=state.route_input_revision AND proof.route_input_sha256=state.route_input_sha256
            AND proof.mutation_version=COALESCE(change.mutation_version,0))
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.coverage_production_descriptor_current(job uuid,worker text,lease uuid,descriptor jsonb) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
    SELECT EXISTS (SELECT 1 FROM app.jobs child JOIN app.coverage_job_contexts parent
        ON parent.job_id=child.parent_job_id AND parent.account_id=child.account_id
        WHERE child.id=job AND child.account_id=app.current_account_id() AND child.kind='coverage_update_route'
            AND child.status='running' AND child.worker_id=worker AND child.lease_token=lease
            AND child.lease_expires_at>=clock_timestamp() AND parent.matching_descriptor=descriptor
            AND parent.matcher_contract_version=descriptor->>'productionMatcherContract')
$function$;
-- +goose StatementEnd

-- Called only after the existing authority/content/ownership/event and canonical
-- digest checks, in the SAME preparation/apply transaction. No fabricated bootstrap.
-- +goose StatementBegin
CREATE FUNCTION app.record_coverage_selection_proof(target_account uuid,target_workout uuid,item jsonb) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
DECLARE accepted_catalog app.coverage_reconciliation_state%ROWTYPE; version bigint;
BEGIN
    IF NOT EXISTS (SELECT 1 FROM app.workout_coverage_states WHERE account_id=target_account AND workout_id=target_workout) THEN RETURN; END IF;
    IF item->>'catalogFingerprint' IS NULL OR item->>'catalogFingerprint' !~ '^[0-9a-f]{64}$'
        OR item->>'selectionFingerprint' IS NULL OR item->>'selectionFingerprint' !~ '^[0-9a-f]{64}$'
        OR item->>'selectionPolicy' IS NULL OR item->>'selectionPolicy' NOT IN ('route-selection-v1:auto-add=true','route-selection-v1:auto-add=false')
        OR item->>'catalogRevision' IS NULL OR (item->>'catalogRevision')::bigint<0 THEN
        RAISE EXCEPTION 'invalid route selection proof' USING ERRCODE='22023';
    END IF;
    SELECT * INTO accepted_catalog FROM app.coverage_reconciliation_state WHERE singleton FOR SHARE NOWAIT;
    IF accepted_catalog.selection_policy<>'' AND item->>'selectionPolicy' IS DISTINCT FROM accepted_catalog.selection_policy THEN
        RAISE EXCEPTION 'route selection policy changed' USING ERRCODE='40001';
    END IF;
    -- A newer external snapshot may be prepared before the observer sees it;
    -- an older selection cannot be installed over an already accepted catalog.
    IF (item->>'catalogRevision')::bigint<accepted_catalog.catalog_revision
        OR ((item->>'catalogRevision')::bigint=accepted_catalog.catalog_revision AND accepted_catalog.catalog_fingerprint<>''
            AND item->>'catalogFingerprint'<>accepted_catalog.catalog_fingerprint) THEN
        RAISE EXCEPTION 'route selection catalog changed' USING ERRCODE='40001';
    END IF;
    IF EXISTS (SELECT 1 FROM app.coverage_route_selection_proofs proof WHERE proof.account_id=target_account AND proof.workout_id=target_workout
        AND proof.catalog_revision>(item->>'catalogRevision')::bigint) THEN
        RAISE EXCEPTION 'route selection catalog regressed' USING ERRCODE='40001';
    END IF;
    SELECT mutation_version INTO version FROM app.coverage_route_changes WHERE account_id=target_account AND workout_id=target_workout FOR UPDATE NOWAIT;
    version:=COALESCE(version,0);
    IF item ? 'mutationVersion' AND (item->>'mutationVersion')::bigint IS DISTINCT FROM version THEN
        RAISE EXCEPTION 'route selection input changed' USING ERRCODE='40001';
    END IF;
    INSERT INTO app.coverage_route_selection_proofs(account_id,workout_id,content_sha256,source_id,route_input_revision,
        route_input_sha256,mutation_version,catalog_revision,catalog_fingerprint,selection_fingerprint,selection_policy,readiness,reason,region_ids,generations)
    SELECT target_account,target_workout,workout.content_sha256,workout.source_id,state.route_input_revision,
        state.route_input_sha256,version,(item->>'catalogRevision')::bigint,item->>'catalogFingerprint',item->>'selectionFingerprint',
        item->>'selectionPolicy',item->>'readiness',item->>'reason',ARRAY(SELECT value->>'regionId' FROM jsonb_array_elements(item->'generations') ORDER BY value->>'regionId'),item->'generations'
    FROM app.workouts workout JOIN app.workout_coverage_states state ON state.workout_id=workout.id AND state.account_id=workout.account_id
    WHERE workout.account_id=target_account AND workout.id=target_workout AND state.route_input_sha256=decode(item->>'routeDigest','hex')
    ON CONFLICT(account_id,workout_id) DO UPDATE SET content_sha256=EXCLUDED.content_sha256,source_id=EXCLUDED.source_id,
        route_input_revision=EXCLUDED.route_input_revision,route_input_sha256=EXCLUDED.route_input_sha256,
        mutation_version=EXCLUDED.mutation_version,catalog_revision=EXCLUDED.catalog_revision,catalog_fingerprint=EXCLUDED.catalog_fingerprint,
        selection_fingerprint=EXCLUDED.selection_fingerprint,selection_policy=EXCLUDED.selection_policy,
        readiness=EXCLUDED.readiness,reason=EXCLUDED.reason,region_ids=EXCLUDED.region_ids,generations=EXCLUDED.generations;
    UPDATE app.coverage_route_changes SET resolved_version=version WHERE account_id=target_account AND workout_id=target_workout
        AND mutation_version=version AND resolved_version<>version;
END;
$function$;
-- +goose StatementEnd

-- Preserve the 027/028 guards and ABI. Patch only the exact validation/preparation
-- nodes; anchored replacements fail closed if the baseline changes.
-- +goose StatementBegin
DO $block$
DECLARE definition text; anchor text; replacement text; signature text;
BEGIN
    definition:=pg_get_functiondef('app.sync_coverage_region_catalog(jsonb)'::regprocedure);
    anchor:='updated_at = transaction_timestamp();';
    IF (length(definition)-length(replace(definition,anchor,'')))/length(anchor)<>1 THEN RAISE EXCEPTION 'catalog sync anchor missing'; END IF;
    EXECUTE replace(definition,anchor,'updated_at = transaction_timestamp() WHERE app.coverage_region_catalog.display_name IS DISTINCT FROM EXCLUDED.display_name;');

    definition:=pg_get_functiondef('app.ingest_coverage_existing_target(uuid,uuid,bigint,bytea,jsonb,text,text,text,double precision)'::regprocedure);
    anchor:='AND state.target_job_id IS NULL';
    replacement:=anchor||' AND app.coverage_applied_target_current($1,$2,$3,$4,$5,$6,$7,$8,$9)';
    -- This anchor occurs in the last UNION only (the cancellation arm has a suffix).
    anchor:='AND state.target_job_id IS NULL' || chr(10);
    IF (length(definition)-length(replace(definition,anchor,'')))/length(anchor)<>1 THEN RAISE EXCEPTION 'applied provenance anchor missing'; END IF;
    definition:=replace(definition,anchor,replacement||chr(10));
    anchor:='AND parent.matcher_contract_version=app.current_coverage_matcher_contract()';
    IF (length(definition)-length(replace(definition,anchor,'')))/length(anchor)<>2 THEN RAISE EXCEPTION 'immutable target descriptor anchors missing'; END IF;
    EXECUTE replace(definition,anchor,anchor||' AND parent.matching_descriptor=app.coverage_matching_descriptor($6,$7,$8,$9,app.current_coverage_matcher_contract())');

    definition:=pg_get_functiondef('app.ingest_coverage_candidates(uuid,uuid)'::regprocedure);
    anchor:='AND (candidate.origin = ''adopted'' OR EXISTS';
    IF (length(definition)-length(replace(definition,anchor,'')))/length(anchor)<>1 THEN RAISE EXCEPTION 'negative queue proof anchor missing'; END IF;
    EXECUTE replace(definition,anchor,'AND NOT (candidate.origin=''adopted'' AND COALESCE(
        app.coverage_selection_metadata(target_account_id,candidate.workout_id)->>''readiness'' IN (''pending'',''unavailable''),false)) '||anchor);

    FOREACH signature IN ARRAY ARRAY[
        'app.apply_coverage_reconciliation(uuid,bigint,text,uuid,uuid,bigint,bytea,text,text,jsonb,uuid,uuid,text,text,text,double precision)',
        'app.apply_ingest_coverage_normal(uuid,uuid,bigint,text,uuid,uuid,bigint,bytea,text,text,jsonb,uuid,uuid,text,text,text,double precision)'] LOOP
        definition:=pg_get_functiondef(signature::regprocedure);
        anchor:='needs_match :=';
        IF (length(definition)-length(replace(definition,anchor,'')))/length(anchor)<>1 THEN RAISE EXCEPTION 'matching identity anchor missing'; END IF;
        definition:=replace(definition,anchor,anchor||' NOT app.coverage_applied_target_current(target_account_id,target_workout_id,target_route_revision,
            target_route_digest,region_generations,rules_version_value,sampling_version_value,path_policy_version_value,minimum_traversal_meters_value) OR');
        anchor:='AND parent_context.matcher_contract_version = app.current_coverage_matcher_contract()';
        IF (length(definition)-length(replace(definition,anchor,'')))/length(anchor)<>2 THEN RAISE EXCEPTION 'normal immutable matcher guard anchors missing'; END IF;
        EXECUTE replace(definition,anchor,anchor||' AND parent_context.matching_descriptor=app.coverage_matching_descriptor(
            rules_version_value,sampling_version_value,path_policy_version_value,minimum_traversal_meters_value,app.current_coverage_matcher_contract())');
    END LOOP;

    definition:=pg_get_functiondef('app.enqueue_coverage_update_core(uuid,uuid,text,bigint,bigint,text,text,text,double precision,jsonb,boolean)'::regprocedure);
    anchor:='AND context.matcher_contract_version = app.current_coverage_matcher_contract()';
    IF (length(definition)-length(replace(definition,anchor,'')))/length(anchor)<>1 THEN RAISE EXCEPTION 'coalescing immutable descriptor anchor missing'; END IF;
    EXECUTE replace(definition,anchor,anchor||' AND context.matching_descriptor=app.coverage_matching_descriptor(
        target_rules,target_sampling,target_policy,target_minimum,app.current_coverage_matcher_contract())');

    definition:=pg_get_functiondef('app.apply_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,jsonb,text,text,text,double precision,integer)'::regprocedure);
    anchor:='DECLARE builder app.ingest_coverage_builders%ROWTYPE; root_id uuid; canonical_existing boolean;';
    IF (length(definition)-length(replace(definition,anchor,'')))/length(anchor)<>1 THEN RAISE EXCEPTION 'batch descriptor declaration anchor missing'; END IF;
    definition:=replace(definition,anchor,anchor||chr(10)||'DECLARE desired_descriptor jsonb; producer_descriptor jsonb;');
    anchor:='    -- Claimers take Coverage parent/child before state.';
    IF (length(definition)-length(replace(definition,anchor,'')))/length(anchor)<>1 THEN RAISE EXCEPTION 'batch desired descriptor anchor missing'; END IF;
    definition:=replace(definition,anchor,'    SELECT matching_descriptor INTO desired_descriptor FROM app.coverage_reconciliation_state WHERE singleton FOR SHARE NOWAIT;
    producer_descriptor:=ready->0->''matchingDescriptor'';
    IF producer_descriptor IS NULL OR producer_descriptor IS DISTINCT FROM app.coverage_matching_descriptor(
        rules,sampling,policy,minimum,producer_descriptor->>''productionMatcherContract'')
        OR producer_descriptor->>''productionMatcherContract'' IS DISTINCT FROM app.current_coverage_matcher_contract()
        OR EXISTS (SELECT 1 FROM jsonb_array_elements(ready) producer_item(value) WHERE producer_item.value->''matchingDescriptor'' IS DISTINCT FROM producer_descriptor) THEN
        RAISE EXCEPTION ''invalid producer matching descriptor'' USING ERRCODE=''22023'';
    END IF;
    IF desired_descriptor<>''{}''::jsonb THEN
        IF desired_descriptor-''minimumTraversalMeters'' IS DISTINCT FROM
            producer_descriptor-''minimumTraversalMeters'' THEN
            RAISE EXCEPTION ''unsupported desired matching descriptor'' USING ERRCODE=''22023'';
        END IF;
        minimum:=(desired_descriptor->>''minimumTraversalMeters'')::double precision;
    END IF;
'||anchor);
    anchor:='app.ingest_coverage_snapshot_digest(target_account_id,workout_id)';
    IF (length(definition)-length(replace(definition,anchor,'')))/length(anchor)<>1 THEN RAISE EXCEPTION 'preparation digest anchor missing'; END IF;
    definition:=replace(definition,anchor,'app.coverage_verified_route_digest(target_account_id,workout_id)');
    anchor:='        RETURN NEXT;';
    IF (length(definition)-length(replace(definition,anchor,'')))/length(anchor)<>1 THEN RAISE EXCEPTION 'preparation proof anchor missing'; END IF;
    definition:=replace(definition,anchor,'        PERFORM app.record_coverage_selection_proof(target_account_id,workout_id,item);'||chr(10)||anchor);
    EXECUTE definition;

    definition:=pg_get_functiondef('app.seal_ingest_coverage_cohort(uuid,uuid,text,uuid,bigint,uuid,jsonb,integer)'::regprocedure);
    anchor:='DECLARE new_parent uuid; new_child uuid; old_parent uuid; changed boolean:=false; targets integer;';
    IF (length(definition)-length(replace(definition,anchor,'')))/length(anchor)<>1 THEN RAISE EXCEPTION 'cohort descriptor declaration anchor missing'; END IF;
    definition:=replace(definition,anchor,anchor||chr(10)||'DECLARE desired_descriptor jsonb;');
    anchor:='    PERFORM app.cleanup_ingest_coverage_origins';
    IF (length(definition)-length(replace(definition,anchor,'')))/length(anchor)<>1 THEN RAISE EXCEPTION 'seal catalog admission anchor missing'; END IF;
    definition:=replace(definition,anchor,'    IF EXISTS (SELECT 1 FROM jsonb_array_elements(refreshed) item CROSS JOIN app.coverage_reconciliation_state catalog
        WHERE (item->>''catalogRevision'')::bigint<catalog.catalog_revision OR
            ((item->>''catalogRevision'')::bigint=catalog.catalog_revision AND catalog.catalog_fingerprint<>''''
                AND item->>''catalogFingerprint''<>catalog.catalog_fingerprint)
            OR EXISTS (SELECT 1 FROM app.coverage_route_selection_proofs proof WHERE proof.account_id=target_account_id
                AND proof.workout_id=(item->>''workoutId'')::uuid AND proof.catalog_revision>(item->>''catalogRevision'')::bigint)) THEN
        parent_job_id:=NULL; route_count:=0; outcome:=''held''; RETURN NEXT; RETURN;
    END IF;
'||anchor);
    anchor:='app.ingest_coverage_snapshot_digest(target_account_id,prepared.workout_id)';
    IF (length(definition)-length(replace(definition,anchor,'')))/length(anchor)<>1 THEN RAISE EXCEPTION 'seal digest anchor missing'; END IF;
    definition:=replace(definition,anchor,'app.coverage_verified_route_digest(target_account_id,prepared.workout_id)');
    anchor:='OR app.coverage_verified_route_digest(target_account_id,prepared.workout_id)';
    IF (length(definition)-length(replace(definition,anchor,'')))/length(anchor)<>1 THEN RAISE EXCEPTION 'prepared exact selection CAS anchor missing'; END IF;
    definition:=replace(definition,anchor,'OR NOT EXISTS (SELECT 1 FROM app.coverage_route_selection_proofs proof
                LEFT JOIN app.coverage_route_changes change ON change.account_id=proof.account_id AND change.workout_id=proof.workout_id
                JOIN LATERAL (SELECT value FROM jsonb_array_elements(refreshed)
                    WHERE (value->>''workoutId'')::uuid=prepared.workout_id AND (value->>''eventId'')::uuid=prepared.event_id) refresh ON true
                WHERE proof.account_id=target_account_id AND proof.workout_id=prepared.workout_id
                    AND proof.route_input_revision=prepared.route_input_revision AND proof.route_input_sha256=prepared.route_input_sha256
                    AND proof.selection_fingerprint=refresh.value->>''selectionFingerprint''
                    AND proof.selection_policy=refresh.value->>''selectionPolicy'' AND proof.mutation_version=COALESCE(change.mutation_version,0))
            '||anchor);
    anchor:='    -- Parent/child before workout/state';
    IF (length(definition)-length(replace(definition,anchor,'')))/length(anchor)<>1 THEN RAISE EXCEPTION 'prepared matching descriptor anchor missing'; END IF;
    definition:=replace(definition,anchor,'    IF jsonb_array_length(refreshed)>0 AND refreshed->0 ? ''matchingDescriptor'' THEN
        SELECT matching_descriptor INTO desired_descriptor FROM app.coverage_reconciliation_state WHERE singleton FOR SHARE NOWAIT;
        IF desired_descriptor<>''{}''::jsonb THEN
            IF EXISTS (SELECT 1 FROM jsonb_array_elements(refreshed) item WHERE (item->''matchingDescriptor'')-''minimumTraversalMeters''
                IS DISTINCT FROM desired_descriptor-''minimumTraversalMeters'') THEN
                RAISE EXCEPTION ''unsupported desired cohort descriptor'' USING ERRCODE=''22023'';
            END IF;
            refreshed:=(SELECT jsonb_agg(jsonb_set(value,''{matchingDescriptor}'',desired_descriptor)) FROM jsonb_array_elements(refreshed));
        END IF;
        IF EXISTS (SELECT 1 FROM jsonb_array_elements(refreshed) item
            WHERE item->''matchingDescriptor'' IS DISTINCT FROM refreshed->0->''matchingDescriptor'') THEN
            RAISE EXCEPTION ''mixed prepared matching descriptors'' USING ERRCODE=''22023'';
        END IF;
        builder.rules:=refreshed->0->''matchingDescriptor''->>''rules'';
        builder.sampling:=refreshed->0->''matchingDescriptor''->>''sampling'';
        builder.policy:=refreshed->0->''matchingDescriptor''->>''pathPolicy'';
        builder.minimum:=(refreshed->0->''matchingDescriptor''->>''minimumTraversalMeters'')::double precision;
        builder.matcher_contract:=refreshed->0->''matchingDescriptor''->>''productionMatcherContract'';
        IF builder.rules IS DISTINCT FROM ''coverage-experimental-v1''
            OR builder.sampling IS DISTINCT FROM ''coverage-sampling-experimental-v1''
            OR builder.policy IS NULL OR builder.policy !~ ''^coverage-path-policy-experimental-v[1-9][0-9]*$''
            OR builder.minimum IS NULL OR builder.minimum NOT BETWEEN 0.1 AND 100
            OR builder.matcher_contract IS DISTINCT FROM app.current_coverage_matcher_contract() THEN
            RAISE EXCEPTION ''unsupported prepared matching descriptor'' USING ERRCODE=''22023'';
        END IF;
        UPDATE app.ingest_coverage_builders current_builder SET rules=builder.rules,sampling=builder.sampling,policy=builder.policy,
            minimum=builder.minimum,matcher_contract=builder.matcher_contract WHERE current_builder.builder_id=target_builder_id
            AND (current_builder.rules,current_builder.sampling,current_builder.policy,current_builder.minimum,current_builder.matcher_contract)
                IS DISTINCT FROM (builder.rules,builder.sampling,builder.policy,builder.minimum,builder.matcher_contract);
    END IF;
'||anchor);
    anchor:='        SELECT * INTO current_event FROM app.ingest_coverage_candidates';
    IF (length(definition)-length(replace(definition,anchor,'')))/length(anchor)<>1 THEN RAISE EXCEPTION 'seal metadata rebind anchor missing'; END IF;
    definition:=replace(definition,anchor,'        PERFORM app.rebind_ingest_coverage_prepared(target_account_id,prepared.workout_id,prepared.event_id,refreshed);
        SELECT * INTO prepared FROM app.ingest_coverage_prepared current_prepared
            WHERE current_prepared.account_id=target_account_id AND current_prepared.workout_id=prepared.workout_id;
'||anchor);
    anchor:='RETURN NEXT;';
    IF position(anchor IN definition)=0 THEN RAISE EXCEPTION 'cohort result authority anchor missing'; END IF;
    definition:=replace(definition,anchor,'IF NOT app.adopted_coverage_authority_current(target_account_id,target_job_id,claiming_worker,current_lease_token,reconciliation_revision)
        THEN RAISE EXCEPTION ''cohort result authority expired'' USING ERRCODE=''40001''; END IF; RETURN NEXT;');
    EXECUTE definition;

    definition:=pg_get_functiondef('app.read_ingest_coverage_prepared(uuid,uuid,text,uuid,bigint,integer)'::regprocedure);
    anchor:='''matcherContract'',builder.matcher_contract';
    IF (length(definition)-length(replace(definition,anchor,'')))/length(anchor)<>1 THEN RAISE EXCEPTION 'prepared selection metadata anchor missing'; END IF;
    EXECUTE replace(definition,anchor,'''selectionFingerprint'',(SELECT proof.selection_fingerprint FROM app.coverage_route_selection_proofs proof
            WHERE proof.account_id=prepared.account_id AND proof.workout_id=prepared.workout_id),
        ''catalogRevision'',(SELECT proof.catalog_revision FROM app.coverage_route_selection_proofs proof
            WHERE proof.account_id=prepared.account_id AND proof.workout_id=prepared.workout_id),
        ''selectionPolicy'',(SELECT proof.selection_policy FROM app.coverage_route_selection_proofs proof
            WHERE proof.account_id=prepared.account_id AND proof.workout_id=prepared.workout_id),'||anchor);

    definition:=pg_get_functiondef('app.repair_coverage_reconciliation_input(uuid,bigint,text,uuid,uuid,bigint,bytea,bytea)'::regprocedure);
    anchor:='    PERFORM 1 FROM app.workout_routes route';
    IF (length(definition)-length(replace(definition,anchor,'')))/length(anchor)<>1 THEN RAISE EXCEPTION 'dirty input repair summary anchor missing'; END IF;
    definition:=replace(definition,anchor,'    PERFORM 1 FROM app.workouts route');
    -- The workout lock above is authoritative even when point invalidation
    -- removed the summary. Keep the remaining existing repair fences verbatim.
    definition:=replace(definition,'route.workout_id = target_workout_id','route.id = target_workout_id');
    EXECUTE definition;

    definition:=pg_get_functiondef('app.fail_coverage_reconciliation(uuid,bigint,text,uuid,text,text,interval)'::regprocedure);
    anchor:='next_scan_at = transaction_timestamp() + retry_delay,';
    IF (length(definition)-length(replace(definition,anchor,'')))/length(anchor)<>1 THEN RAISE EXCEPTION 'queue failure backoff anchor missing'; END IF;
    EXECUTE replace(definition,anchor,'next_scan_at = CASE WHEN scan_required THEN transaction_timestamp() + retry_delay ELSE next_scan_at END,
        queue_retry_at = transaction_timestamp() + retry_delay,');
END;
$block$;
-- +goose StatementEnd

-- Rebind prepared selected IDs only under immutable-input/event CAS. Catalog
-- selection changes intentionally leave the old cache invalid for fresh intake.
-- +goose StatementBegin
CREATE FUNCTION app.rebind_ingest_coverage_prepared(target_account uuid,target_workout uuid,event uuid,refreshed jsonb) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
DECLARE item jsonb; proof app.coverage_route_selection_proofs%ROWTYPE; saved app.ingest_coverage_prepared%ROWTYPE;
BEGIN
    SELECT * INTO saved FROM app.ingest_coverage_prepared WHERE account_id=target_account AND workout_id=target_workout AND event_id=event;
    IF NOT FOUND THEN RETURN; END IF;
    SELECT value INTO item FROM jsonb_array_elements(refreshed) WHERE (value->>'workoutId')::uuid=target_workout AND (value->>'eventId')::uuid=event;
    SELECT p.* INTO proof FROM app.coverage_route_selection_proofs p
    JOIN app.workouts workout ON workout.id=p.workout_id AND workout.account_id=p.account_id
    LEFT JOIN app.coverage_route_changes change ON change.account_id=p.account_id AND change.workout_id=p.workout_id
    WHERE p.account_id=target_account AND p.workout_id=target_workout AND p.route_input_revision=saved.route_input_revision
        AND p.route_input_sha256=saved.route_input_sha256 AND p.content_sha256=saved.content_sha256
        AND p.content_sha256=workout.content_sha256 AND p.source_id=workout.source_id AND p.source_id=saved.source_id
        AND p.mutation_version=COALESCE(change.mutation_version,0)
        AND p.selection_fingerprint=item->>'selectionFingerprint' AND p.selection_policy=item->>'selectionPolicy'
        AND p.region_ids=ARRAY(SELECT value->>'regionId' FROM jsonb_array_elements(item->'generations') ORDER BY value->>'regionId');
    IF NOT FOUND OR NOT COALESCE(app.valid_coverage_generation_vector(item->'generations'),false)
        OR (item->>'catalogRevision')::bigint<proof.catalog_revision THEN RETURN; END IF;
    PERFORM 1 FROM app.coverage_reconciliation_state catalog WHERE singleton
        AND catalog.catalog_revision<=(item->>'catalogRevision')::bigint
        AND (catalog.selection_policy='' OR catalog.selection_policy=item->>'selectionPolicy')
        AND (catalog.catalog_revision<(item->>'catalogRevision')::bigint OR catalog.catalog_fingerprint=''
            OR catalog.catalog_fingerprint=item->>'catalogFingerprint') FOR SHARE NOWAIT;
    IF NOT FOUND THEN RAISE EXCEPTION 'prepared selection catalog changed' USING ERRCODE='40001'; END IF;
    UPDATE app.ingest_coverage_prepared SET catalog_fingerprint=decode(item->>'catalogFingerprint','hex'),generations=item->'generations'
    WHERE account_id=target_account AND workout_id=target_workout AND event_id=event
        AND (catalog_fingerprint,generations) IS DISTINCT FROM (decode(item->>'catalogFingerprint','hex'),item->'generations');
    UPDATE app.workout_coverage_regions region SET desired_osm_generation=(generation->>'generation')::bigint
    FROM jsonb_array_elements(item->'generations') generation
    WHERE region.account_id=target_account AND region.workout_id=target_workout AND region.region_id=generation->>'regionId'
        AND region.desired_osm_generation IS DISTINCT FROM (generation->>'generation')::bigint;
    UPDATE app.coverage_route_selection_proofs current_proof SET catalog_revision=(item->>'catalogRevision')::bigint,
        catalog_fingerprint=item->>'catalogFingerprint',generations=item->'generations'
    WHERE current_proof.account_id=target_account AND current_proof.workout_id=target_workout
        AND (current_proof.catalog_revision,current_proof.catalog_fingerprint,current_proof.generations) IS DISTINCT FROM
            ((item->>'catalogRevision')::bigint,item->>'catalogFingerprint',item->'generations');
END;
$function$;
-- +goose StatementEnd

-- Queue eligibility is independent of campaign backoff/completion and never
-- resets a campaign cursor. Deferred poison routes do not block other handoffs.
-- +goose StatementBegin
CREATE FUNCTION app.coverage_reconciliation_queue_due(target_account uuid) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
    SELECT EXISTS (SELECT 1 FROM app.ingest_coverage_candidates(target_account,NULL) candidate WHERE NOT EXISTS (
        SELECT 1 FROM app.ingest_coverage_prepared prepared WHERE prepared.account_id=target_account
            AND prepared.workout_id=candidate.workout_id AND prepared.event_id=candidate.event_id AND prepared.origin=candidate.origin))
    OR app.ingest_coverage_cleanup_due(target_account)
    OR EXISTS (SELECT 1 FROM app.ingest_coverage_builders builder WHERE builder.account_id=target_account
        AND EXISTS (SELECT 1 FROM app.ingest_coverage_prepared item WHERE item.builder_id=builder.builder_id)
        AND app.ingest_coverage_root_terminal(target_account,builder.root_job_id))
    OR EXISTS (SELECT 1 FROM app.coverage_route_changes change JOIN app.workouts workout
        ON workout.id=change.workout_id AND workout.account_id=change.account_id
        JOIN app.sources source ON source.id=workout.source_id AND source.account_id=workout.account_id AND source.deleted_at IS NULL
        WHERE change.account_id=target_account AND change.mutation_version>change.resolved_version
        AND change.next_attempt_at<=clock_timestamp() AND workout.deletion_requested_at IS NULL
        AND NOT EXISTS (SELECT 1 FROM app.ingest_coverage_handoffs handoff WHERE handoff.account_id=target_account
            AND handoff.workout_id=change.workout_id AND handoff.acknowledged_at IS NULL))
$function$;
-- +goose StatementEnd

-- A metadata-only seed of EXACT durable queue accounts also covers first imports
-- after bootstrap, without an account-wide generation lock in their write tx.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.claim_coverage_reconciliation(claiming_worker text,new_lease_token uuid,lease_duration interval,runtime_version integer)
RETURNS TABLE(account_id uuid,reconciliation_revision bigint,cursor_workout_id uuid) LANGUAGE plpgsql SECURITY DEFINER
SET search_path=pg_catalog,app AS $function$
DECLARE candidate app.coverage_reconciliation_accounts%ROWTYPE; scan boolean;
BEGIN
    IF runtime_version IS NULL OR runtime_version<30 OR runtime_version<(SELECT minimum_runtime_version FROM app.schema_metadata WHERE singleton) THEN
        RAISE EXCEPTION 'coverage reconciliation runtime 30 is required' USING ERRCODE='55000';
    END IF;
    IF claiming_worker IS NULL OR length(claiming_worker) NOT BETWEEN 1 AND 200 OR new_lease_token IS NULL
        OR lease_duration IS NULL OR lease_duration NOT BETWEEN interval '30 seconds' AND interval '15 minutes' THEN
        RAISE EXCEPTION 'invalid coverage reconciliation claim' USING ERRCODE='22023';
    END IF;
    -- Wake keys are transaction-local, not a shared account mutex in file writes.
    -- Consume only committed rows; an older still-uncommitted transaction remains
    -- discoverable when it commits, irrespective of any newer transaction ID.
    DELETE FROM app.coverage_reconciliation_wakes wake WHERE NOT app.coverage_reconciliation_queue_due(wake.account_id);
    INSERT INTO app.coverage_reconciliation_accounts(account_id,desired_revision,completed_revision)
    SELECT queued.account_id,GREATEST(catalog.desired_revision,1),GREATEST(catalog.desired_revision,1)
    FROM (SELECT handoff.account_id FROM app.ingest_coverage_handoffs handoff WHERE handoff.acknowledged_at IS NULL
        UNION SELECT builder.account_id FROM app.ingest_coverage_builders builder
        UNION SELECT change.account_id FROM app.coverage_route_changes change WHERE change.mutation_version>change.resolved_version
        UNION SELECT wake.account_id FROM app.coverage_reconciliation_wakes wake) queued
    JOIN app.accounts account ON account.id=queued.account_id AND account.state='active'
    CROSS JOIN app.coverage_reconciliation_state catalog
    WHERE NOT EXISTS (SELECT 1 FROM app.coverage_reconciliation_accounts existing WHERE existing.account_id=queued.account_id)
        AND app.coverage_reconciliation_queue_due(queued.account_id)
    ON CONFLICT DO NOTHING;
    SELECT reconciliation.* INTO candidate FROM app.coverage_reconciliation_accounts reconciliation
    JOIN app.accounts account ON account.id=reconciliation.account_id AND account.state='active'
    WHERE (reconciliation.worker_id IS NULL OR reconciliation.lease_expires_at<clock_timestamp())
        AND ((reconciliation.desired_revision>reconciliation.completed_revision AND reconciliation.next_scan_at<=clock_timestamp())
            OR ((reconciliation.queue_retry_at<=clock_timestamp() OR EXISTS (SELECT 1
                FROM app.coverage_reconciliation_wakes wake WHERE wake.account_id=reconciliation.account_id))
                AND app.coverage_reconciliation_queue_due(reconciliation.account_id)))
    ORDER BY reconciliation.updated_at,reconciliation.account_id LIMIT 1 FOR UPDATE OF reconciliation SKIP LOCKED;
    IF NOT FOUND THEN RETURN; END IF;
    scan:=candidate.desired_revision>candidate.completed_revision AND candidate.next_scan_at<=clock_timestamp();
    IF scan AND candidate.active_revision IS DISTINCT FROM candidate.desired_revision THEN
        candidate.active_revision:=candidate.desired_revision; candidate.cursor_workout_id:=NULL;
    END IF;
    UPDATE app.coverage_reconciliation_accounts reconciliation SET active_revision=COALESCE(candidate.active_revision,candidate.desired_revision),
        cursor_workout_id=candidate.cursor_workout_id,scan_required=scan,worker_id=claiming_worker,lease_token=new_lease_token,
        heartbeat_at=clock_timestamp(),lease_expires_at=clock_timestamp()+lease_duration,lease_runtime_version=runtime_version,
        last_failure_code=NULL,last_failure_summary=NULL,updated_at=clock_timestamp()
    WHERE reconciliation.account_id=candidate.account_id;
    DELETE FROM app.coverage_reconciliation_wakes wake WHERE wake.account_id=candidate.account_id;
    account_id:=candidate.account_id; reconciliation_revision:=COALESCE(candidate.active_revision,candidate.desired_revision);
    cursor_workout_id:=candidate.cursor_workout_id; RETURN NEXT;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.claim_coverage_reconciliation_work(worker text,lease uuid,duration interval,runtime integer)
RETURNS TABLE(account_id uuid,reconciliation_revision bigint,cursor_workout_id uuid,scan_required boolean)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
DECLARE claimed record;
BEGIN
    SELECT * INTO claimed FROM app.claim_coverage_reconciliation(worker,lease,duration,runtime);
    IF NOT FOUND THEN RETURN; END IF;
    RETURN QUERY SELECT claimed.account_id,claimed.reconciliation_revision,claimed.cursor_workout_id,account.scan_required
        FROM app.coverage_reconciliation_accounts account WHERE account.account_id=claimed.account_id;
END;
$function$;
-- +goose StatementEnd

-- Actionability is filtered BEFORE page limiting and before any GPS/hash read.
-- Queue-only calls expose only dirty exact keys, never the ordinary route set.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.read_coverage_reconciliation_routes(target_account_id uuid,reconciliation_revision bigint,
    claiming_worker text,current_lease_token uuid,after_workout_id uuid,page_limit integer)
RETURNS TABLE(workout_id uuid,route_input_revision bigint,route_input_sha256 bytea)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
    SELECT workout.id,COALESCE(state.route_input_revision,0),state.route_input_sha256
    FROM app.coverage_reconciliation_accounts reconciliation
    JOIN app.workouts workout ON workout.account_id=reconciliation.account_id
    LEFT JOIN app.workout_routes route ON route.account_id=workout.account_id AND route.workout_id=workout.id
    JOIN app.sources source ON source.id=workout.source_id AND source.account_id=workout.account_id AND source.deleted_at IS NULL
    LEFT JOIN app.workout_coverage_states state ON state.account_id=workout.account_id AND state.workout_id=workout.id
    LEFT JOIN app.coverage_route_changes change ON change.account_id=workout.account_id AND change.workout_id=workout.id
    WHERE reconciliation.account_id=target_account_id AND target_account_id=app.current_account_id()
        AND app.adopted_coverage_authority_current(target_account_id,NULL,claiming_worker,current_lease_token,reconciliation_revision)
        AND workout.deletion_requested_at IS NULL AND page_limit BETWEEN 1 AND 100
        AND (route.workout_id IS NOT NULL OR change.mutation_version>change.resolved_version)
        AND (after_workout_id IS NULL OR workout.id>after_workout_id)
        AND (change.mutation_version IS NULL OR change.mutation_version<=change.resolved_version OR change.next_attempt_at<=clock_timestamp())
        AND (reconciliation.scan_required OR (change.mutation_version>change.resolved_version AND change.next_attempt_at<=clock_timestamp()))
        AND NOT EXISTS (SELECT 1 FROM app.ingest_coverage_handoffs handoff WHERE handoff.account_id=workout.account_id
            AND handoff.workout_id=workout.id AND handoff.acknowledged_at IS NULL)
        AND app.coverage_route_actionable(workout.account_id,workout.id)
    ORDER BY workout.id LIMIT LEAST(page_limit,100)
$function$;
-- +goose StatementEnd

-- New apply entry point reuses the ordinary guards, then records the independent
-- proof under the same locks. A metadata rebind never reads coordinates or hashes.
-- +goose StatementBegin
CREATE FUNCTION app.apply_coverage_selection(target_account uuid,revision bigint,worker text,lease uuid,target_workout uuid,
    route_revision bigint,digest bytea,item jsonb,rules text,sampling text,policy text,minimum double precision) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
DECLARE applied boolean; metadata jsonb;
BEGIN
    IF NOT app.adopted_coverage_authority_current(target_account,NULL,worker,lease,revision) THEN RETURN false; END IF;
    PERFORM 1 FROM app.coverage_reconciliation_state catalog WHERE singleton AND
        (catalog.matching_descriptor='{}'::jsonb OR catalog.matching_descriptor=app.coverage_matching_descriptor(
            rules,sampling,policy,minimum,item->'matchingDescriptor'->>'productionMatcherContract'))
        AND item->'matchingDescriptor'=app.coverage_matching_descriptor(rules,sampling,policy,minimum,app.current_coverage_matcher_contract())
        AND (catalog.selection_policy='' OR catalog.selection_policy=item->>'selectionPolicy')
        FOR SHARE NOWAIT;
    IF NOT FOUND THEN RAISE EXCEPTION 'selection matching descriptor or policy changed' USING ERRCODE='40001'; END IF;
    -- Reserve the same account -> reconciliation -> parent/child -> workout/state
    -- order as batch admission, before either reusing a target or invoking it.
    PERFORM 1 FROM app.accounts WHERE id=target_account AND target_account=app.current_account_id() AND state='active' FOR UPDATE NOWAIT;
    IF NOT FOUND THEN RETURN false; END IF;
    PERFORM 1 FROM app.coverage_reconciliation_accounts WHERE account_id=target_account FOR UPDATE NOWAIT;
    PERFORM 1 FROM app.jobs parent WHERE parent.account_id=target_account AND parent.kind='coverage_update'
        ORDER BY parent.id FOR UPDATE NOWAIT;
    PERFORM 1 FROM app.jobs child JOIN app.jobs parent ON parent.id=child.parent_job_id AND parent.account_id=child.account_id
        WHERE parent.account_id=target_account AND parent.kind='coverage_update' ORDER BY parent.id,child.id FOR UPDATE OF child NOWAIT;
    PERFORM 1 FROM app.workouts WHERE id=target_workout AND account_id=target_account AND deletion_requested_at IS NULL FOR UPDATE NOWAIT;
    IF NOT FOUND THEN RETURN false; END IF;
    IF NOT EXISTS (SELECT 1 FROM app.workouts workout JOIN app.accounts account ON account.id=workout.account_id
        JOIN app.sources source ON source.id=workout.source_id AND source.account_id=workout.account_id AND source.deleted_at IS NULL
        WHERE workout.id=target_workout AND workout.account_id=target_account AND workout.content_sha256=decode(item->>'contentSha256','hex')
            AND workout.source_id=(item->>'sourceId')::uuid AND account.workout_ownership_epoch=(item->>'ownershipEpoch')::bigint) THEN
        RAISE EXCEPTION 'selection immutable workout snapshot changed' USING ERRCODE='40001';
    END IF;
    PERFORM 1 FROM app.coverage_route_changes WHERE account_id=target_account AND workout_id=target_workout FOR UPDATE NOWAIT;
    PERFORM 1 FROM app.workout_coverage_states WHERE account_id=target_account AND workout_id=target_workout
        AND route_input_revision=route_revision AND route_input_sha256=digest FOR UPDATE NOWAIT;
    IF NOT FOUND THEN RAISE EXCEPTION 'selection route input changed' USING ERRCODE='40001'; END IF;
    metadata:=app.coverage_selection_metadata(target_account,target_workout);
    IF item ? 'mutationVersion' AND (item->>'mutationVersion')::bigint IS DISTINCT FROM COALESCE((SELECT mutation_version
        FROM app.coverage_route_changes WHERE account_id=target_account AND workout_id=target_workout),0) THEN
        RAISE EXCEPTION 'selection route mutation changed' USING ERRCODE='40001';
    END IF;
    IF metadata IS NOT NULL AND item->>'readiness'<>'map_data_ready' THEN
        RETURN app.adopted_coverage_authority_current(target_account,NULL,worker,lease,revision);
    END IF;
    IF item->>'readiness'='map_data_ready' AND EXISTS (SELECT 1 FROM app.ingest_coverage_existing_target(target_account,target_workout,
        route_revision,digest,item->'generations',rules,sampling,policy,minimum)) THEN
        INSERT INTO app.workout_coverage_regions(account_id,workout_id,region_id,desired_osm_generation)
        SELECT target_account,target_workout,value->>'regionId',(value->>'generation')::bigint FROM jsonb_array_elements(item->'generations')
        ON CONFLICT(account_id,workout_id,region_id) DO UPDATE SET desired_osm_generation=EXCLUDED.desired_osm_generation
            WHERE app.workout_coverage_regions.desired_osm_generation IS DISTINCT FROM EXCLUDED.desired_osm_generation;
        applied:=true;
    ELSE
        applied:=app.apply_coverage_reconciliation(target_account,revision,worker,lease,target_workout,route_revision,digest,
            item->>'readiness',item->>'reason',item->'generations',gen_random_uuid(),gen_random_uuid(),rules,sampling,policy,minimum);
    END IF;
    IF applied THEN PERFORM app.record_coverage_selection_proof(target_account,target_workout,item); END IF;
    IF NOT app.adopted_coverage_authority_current(target_account,NULL,worker,lease,revision) THEN
        RAISE EXCEPTION 'selection authority expired' USING ERRCODE='40001';
    END IF;
    RETURN applied;
END;
$function$;
-- +goose StatementEnd

-- Preserve a route-local failed lookup as a durable obligation. Missing historical
-- change rows get their first logical dirty version; an actual concurrent mutation
-- wins the CAS and is never delayed by an old lookup's result.
-- +goose StatementBegin
CREATE FUNCTION app.defer_coverage_reconciliation_route(target_account uuid,revision bigint,worker text,lease uuid,
    target_workout uuid,route_revision bigint,digest bytea,target_mutation_version bigint) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
DECLARE current_version bigint; prior_lock_timeout text;
BEGIN
    IF target_mutation_version IS NULL OR target_mutation_version<0 OR route_revision IS NULL OR route_revision<0
        OR (route_revision=0 AND digest IS NOT NULL) OR (route_revision>0 AND octet_length(digest) IS DISTINCT FROM 32) THEN
        RAISE EXCEPTION 'invalid route deferral identity' USING ERRCODE='22023';
    END IF;
    IF NOT app.adopted_coverage_authority_current(target_account,NULL,worker,lease,revision) THEN
        RAISE EXCEPTION 'route deferral authority lost' USING ERRCODE='42501';
    END IF;
    PERFORM 1 FROM app.accounts WHERE id=target_account AND target_account=app.current_account_id() AND state='active' FOR UPDATE NOWAIT;
    IF NOT FOUND THEN RAISE EXCEPTION 'route deferral account authority lost' USING ERRCODE='42501'; END IF;
    PERFORM 1 FROM app.coverage_reconciliation_accounts account WHERE account.account_id=target_account
        AND account.active_revision=revision AND account.worker_id=worker AND account.lease_token=lease
        AND account.lease_expires_at>=clock_timestamp() AND account.lease_runtime_version>=30
        AND account.lease_runtime_version>=(SELECT minimum_runtime_version FROM app.schema_metadata WHERE singleton) FOR UPDATE NOWAIT;
    IF NOT FOUND THEN RAISE EXCEPTION 'route deferral lease lost' USING ERRCODE='42501'; END IF;
    PERFORM 1 FROM app.workouts workout JOIN app.sources source ON source.id=workout.source_id AND source.account_id=workout.account_id
        WHERE workout.id=target_workout AND workout.account_id=target_account AND workout.deletion_requested_at IS NULL
            AND source.deleted_at IS NULL FOR UPDATE OF workout NOWAIT;
    IF NOT FOUND THEN RETURN false; END IF;
    IF EXISTS (SELECT 1 FROM app.ingest_coverage_handoffs handoff WHERE handoff.account_id=target_account
        AND handoff.workout_id=target_workout AND handoff.acknowledged_at IS NULL) THEN RETURN false; END IF;
    PERFORM 1 FROM app.workout_coverage_states state WHERE state.account_id=target_account AND state.workout_id=target_workout
        AND state.route_input_revision=route_revision AND state.route_input_sha256=digest FOR SHARE NOWAIT;
    IF NOT FOUND AND NOT (route_revision=0 AND digest IS NULL AND NOT EXISTS (SELECT 1 FROM app.workout_coverage_states state
        WHERE state.account_id=target_account AND state.workout_id=target_workout)) THEN RETURN false; END IF;
    SELECT mutation_version INTO current_version FROM app.coverage_route_changes
        WHERE account_id=target_account AND workout_id=target_workout FOR UPDATE NOWAIT;
    IF FOUND THEN
        IF current_version<>target_mutation_version THEN RETURN false; END IF;
        UPDATE app.coverage_route_changes SET resolved_version=LEAST(resolved_version,mutation_version-1),
            next_attempt_at=clock_timestamp()+interval '5 minutes'
        WHERE account_id=target_account AND workout_id=target_workout AND mutation_version=target_mutation_version;
    ELSE
        IF target_mutation_version<>0 THEN RETURN false; END IF;
        -- Bound a concurrent unique-key initializer without holding account locks
        -- behind its point-write/FK transaction. Contention remains account-fatal.
        prior_lock_timeout:=current_setting('lock_timeout');
        PERFORM set_config('lock_timeout','1ms',true);
        INSERT INTO app.coverage_route_changes(account_id,workout_id,next_attempt_at)
        VALUES(target_account,target_workout,clock_timestamp()+interval '5 minutes') ON CONFLICT(account_id,workout_id) DO NOTHING;
        IF NOT FOUND THEN
            PERFORM set_config('lock_timeout',prior_lock_timeout,true);
            RETURN false;
        END IF;
        PERFORM set_config('lock_timeout',prior_lock_timeout,true);
    END IF;
    IF NOT app.adopted_coverage_authority_current(target_account,NULL,worker,lease,revision) THEN
        RAISE EXCEPTION 'route deferral authority expired' USING ERRCODE='40001';
    END IF;
    RETURN true;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.advance_coverage_reconciliation(target_account_id uuid,reconciliation_revision bigint,
    claiming_worker text,current_lease_token uuid,last_workout_id uuid,completed boolean,scan_interval interval) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
BEGIN
    UPDATE app.coverage_reconciliation_accounts reconciliation SET
        cursor_workout_id=CASE WHEN NOT scan_required THEN cursor_workout_id WHEN completed THEN NULL ELSE last_workout_id END,
        completed_revision=CASE WHEN completed AND scan_required THEN GREATEST(completed_revision,reconciliation_revision) ELSE completed_revision END,
        worker_id=CASE WHEN completed THEN NULL ELSE worker_id END,lease_token=CASE WHEN completed THEN NULL ELSE lease_token END,
        heartbeat_at=CASE WHEN completed THEN NULL ELSE heartbeat_at END,lease_expires_at=CASE WHEN completed THEN NULL ELSE lease_expires_at END,
        next_scan_at=CASE WHEN completed AND scan_required AND reconciliation_revision>=desired_revision THEN 'infinity'::timestamptz ELSE next_scan_at END,
        queue_retry_at=CASE WHEN completed THEN clock_timestamp()+interval '30 seconds' ELSE queue_retry_at END,updated_at=clock_timestamp()
    WHERE account_id=target_account_id AND active_revision=reconciliation_revision AND worker_id=claiming_worker
        AND lease_token=current_lease_token AND lease_expires_at>=clock_timestamp() AND lease_runtime_version>=30
        AND lease_runtime_version>=(SELECT minimum_runtime_version FROM app.schema_metadata WHERE singleton);
    RETURN FOUND;
END;
$function$;
-- +goose StatementEnd

-- Private tables have owner-only forced RLS and no runtime direct privileges.
GRANT CREATE ON SCHEMA app TO workouts_security_owner;
-- +goose StatementBegin
DO $block$
DECLARE name text; signature text;
BEGIN
    FOREACH name IN ARRAY ARRAY['coverage_route_changes','coverage_route_selection_proofs','coverage_applied_match_proofs','coverage_reconciliation_wakes'] LOOP
        EXECUTE format('ALTER TABLE app.%I ENABLE ROW LEVEL SECURITY',name);
        EXECUTE format('ALTER TABLE app.%I FORCE ROW LEVEL SECURITY',name);
        EXECUTE format('CREATE POLICY %I ON app.%I TO workouts_security_owner USING(true) WITH CHECK(true)',name||'_owner_policy',name);
        EXECUTE format('ALTER TABLE app.%I OWNER TO workouts_security_owner',name);
        EXECUTE format('REVOKE ALL ON app.%I FROM PUBLIC,workouts_api,workouts_worker,workouts_coverage_worker,workouts_tiles',name);
    END LOOP;
    FOR signature IN SELECT p.oid::regprocedure::text FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
        WHERE n.nspname='app' AND p.proname=ANY(ARRAY['coverage_matching_descriptor','capture_coverage_matching_descriptor',
            'stamp_coverage_applied_match_proof','dirty_coverage_route_inputs','coverage_applied_target_current','coverage_verified_route_digest',
            'read_coverage_catalog_observation','observe_coverage_catalog','coverage_selection_metadata','coverage_route_actionable',
            'read_coverage_selection_metadata','record_coverage_selection_proof','coverage_reconciliation_queue_due',
            'claim_coverage_reconciliation_work','apply_coverage_selection','ingest_coverage_route_needs_proof',
            'rebind_ingest_coverage_prepared','coverage_production_descriptor_current','wake_coverage_reconciliation',
            'defer_coverage_reconciliation_route']) LOOP
        EXECUTE 'ALTER FUNCTION '||signature||' OWNER TO workouts_security_owner';
        EXECUTE 'REVOKE ALL ON FUNCTION '||signature||' FROM PUBLIC,workouts_api,workouts_worker,workouts_coverage_worker,workouts_tiles,workouts_migration';
    END LOOP;
END;
$block$;
-- +goose StatementEnd
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
GRANT EXECUTE ON FUNCTION app.read_coverage_catalog_observation(),
    app.read_coverage_selection_metadata(uuid,uuid,text,uuid,bigint,uuid) TO workouts_worker,workouts_coverage_worker;
GRANT EXECUTE ON FUNCTION app.ingest_coverage_route_needs_proof(uuid,uuid,text,uuid,uuid) TO workouts_worker;
GRANT EXECUTE ON FUNCTION app.coverage_production_descriptor_current(uuid,text,uuid,jsonb) TO workouts_coverage_worker;
GRANT EXECUTE ON FUNCTION app.observe_coverage_catalog(bigint,bigint,text,text,jsonb,text,jsonb,jsonb),
    app.claim_coverage_reconciliation_work(text,uuid,interval,integer),
    app.defer_coverage_reconciliation_route(uuid,bigint,text,uuid,uuid,bigint,bytea,bigint),
    app.apply_coverage_selection(uuid,bigint,text,uuid,uuid,bigint,bytea,jsonb,text,text,text,double precision) TO workouts_coverage_worker;
-- END PRECISE NEAR-IDLE COVERAGE RECONCILIATION

-- Keep the canonical migration baseline unchanged.
UPDATE app.schema_metadata SET schema_version = 29, minimum_runtime_version = 29 WHERE singleton;

-- +goose Down
-- +goose StatementBegin
DO $block$
BEGIN
    RAISE EXCEPTION 'ingest status reporting is forward-only: persisted reprocessing receipts and job policy cannot be undone safely; restore the pre-migration backup to roll back'
        USING ERRCODE = '55000';
END;
$block$;
-- +goose StatementEnd
