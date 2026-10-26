-- +goose Up
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

-- Runtime 28 and canonical history marker 27 remain supported.
UPDATE app.schema_metadata SET schema_version = 30 WHERE singleton;

-- +goose Down
-- +goose StatementBegin
DO $block$ BEGIN
    RAISE EXCEPTION 'ingest workout outcomes is forward-only' USING ERRCODE = '55000';
END; $block$;
-- +goose StatementEnd
