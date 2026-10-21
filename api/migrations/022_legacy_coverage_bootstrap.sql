-- +goose Up
-- BEGIN SCHEMA 20 COMPATIBLE FUNCTION PATCH
-- This block uses only shipped schema-20 objects and preserves signatures/ACLs.
-- A controlled hotpatch must exclude the campaign wakeup and metadata below.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.read_coverage_reconciliation_routes (
    target_account_id uuid,
    reconciliation_revision bigint,
    claiming_worker text,
    current_lease_token uuid,
    after_workout_id uuid,
    page_limit integer
) RETURNS TABLE (workout_id uuid, route_input_revision bigint, route_input_sha256 bytea) LANGUAGE sql STABLE SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
    SELECT workout.id, COALESCE(state.route_input_revision, 0), state.route_input_sha256
      FROM app.coverage_reconciliation_accounts reconciliation
      JOIN app.accounts account ON account.id = reconciliation.account_id AND account.state = 'active'
      JOIN app.workout_routes route ON route.account_id = reconciliation.account_id
      JOIN app.workouts workout ON workout.account_id = route.account_id AND workout.id = route.workout_id
      LEFT JOIN app.workout_coverage_states state ON state.account_id = workout.account_id AND state.workout_id = workout.id
     WHERE reconciliation.account_id = target_account_id AND target_account_id = app.current_account_id()
       AND reconciliation.active_revision = reconciliation_revision
       AND reconciliation.worker_id = claiming_worker AND reconciliation.lease_token = current_lease_token
       AND reconciliation.lease_expires_at >= clock_timestamp() AND workout.deletion_requested_at IS NULL
       AND (after_workout_id IS NULL OR workout.id > after_workout_id) AND page_limit BETWEEN 1 AND 100
     ORDER BY workout.id LIMIT LEAST(page_limit, 100)
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.read_coverage_reconciliation_route (
    target_account_id uuid,
    reconciliation_revision bigint,
    claiming_worker text,
    current_lease_token uuid,
    target_workout_id uuid,
    target_route_revision bigint,
    target_route_digest bytea
) RETURNS TABLE (
    sequence integer,
    recorded_at timestamptz,
    latitude double precision,
    longitude double precision,
    altitude double precision,
    speed double precision,
    course double precision,
    horizontal_accuracy double precision,
    vertical_accuracy double precision,
    speed_accuracy double precision,
    course_accuracy double precision
) LANGUAGE sql STABLE SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
    SELECT point.sequence, point.recorded_at, point.latitude, point.longitude, point.altitude, point.speed, point.course,
           point.horizontal_accuracy, point.vertical_accuracy, point.speed_accuracy, point.course_accuracy
      FROM app.coverage_reconciliation_accounts reconciliation
      JOIN app.accounts account ON account.id = reconciliation.account_id AND account.state = 'active'
      JOIN app.workouts workout ON workout.account_id = reconciliation.account_id AND workout.id = target_workout_id
      JOIN app.workout_routes route ON route.account_id = workout.account_id AND route.workout_id = workout.id
      JOIN app.workout_route_points point ON point.account_id = workout.account_id AND point.workout_id = workout.id
      LEFT JOIN app.workout_coverage_states state ON state.account_id = workout.account_id AND state.workout_id = workout.id
     WHERE reconciliation.account_id = target_account_id AND target_account_id = app.current_account_id()
       AND reconciliation.active_revision = reconciliation_revision
       AND reconciliation.worker_id = claiming_worker AND reconciliation.lease_token = current_lease_token
       AND reconciliation.lease_expires_at >= clock_timestamp() AND workout.deletion_requested_at IS NULL
       AND ((state.route_input_revision = target_route_revision AND state.route_input_sha256 = target_route_digest)
         OR (state.workout_id IS NULL AND target_route_revision = 0 AND target_route_digest IS NULL))
     ORDER BY point.sequence LIMIT 50001
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.repair_coverage_reconciliation_input (
    target_account_id uuid,
    reconciliation_revision bigint,
    claiming_worker text,
    current_lease_token uuid,
    target_workout_id uuid,
    old_route_revision bigint,
    old_route_digest bytea,
    new_route_digest bytea
) RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
DECLARE new_revision bigint; missing_state boolean; current_state app.workout_coverage_states%ROWTYPE; current_digest bytea;
DECLARE prior_lock_timeout text;
BEGIN
    missing_state := old_route_revision = 0 AND old_route_digest IS NULL;
    IF octet_length(new_route_digest) IS DISTINCT FROM 32 OR old_route_revision IS NULL OR
       (NOT missing_state AND (old_route_revision < 1 OR octet_length(old_route_digest) IS DISTINCT FROM 32)) THEN
        RAISE EXCEPTION 'invalid reconciliation route digest' USING ERRCODE = '22023';
    END IF;
    -- Never wait for a lower lock while holding the account: ingest can hold
    -- a workout FK lock before enqueueing coverage under an account UPDATE lock.
    PERFORM 1 FROM app.accounts account
     WHERE account.id = target_account_id AND target_account_id = app.current_account_id()
       AND account.state = 'active' FOR SHARE;
    IF NOT FOUND THEN RETURN NULL; END IF;
    PERFORM 1 FROM app.coverage_reconciliation_accounts reconciliation
     WHERE reconciliation.account_id = target_account_id AND reconciliation.active_revision = reconciliation_revision
       AND reconciliation.worker_id = claiming_worker AND reconciliation.lease_token = current_lease_token
       AND reconciliation.lease_expires_at >= clock_timestamp() FOR UPDATE NOWAIT;
    IF NOT FOUND THEN RETURN NULL; END IF;
    PERFORM 1 FROM app.workouts workout
     WHERE workout.account_id = target_account_id AND workout.id = target_workout_id
       AND workout.deletion_requested_at IS NULL FOR UPDATE NOWAIT;
    IF NOT FOUND THEN RETURN NULL; END IF;
    PERFORM 1 FROM app.workout_routes route
     WHERE route.account_id = target_account_id AND route.workout_id = target_workout_id FOR SHARE NOWAIT;
    IF NOT FOUND THEN RETURN NULL; END IF;
    SELECT state.* INTO current_state FROM app.workout_coverage_states state
     WHERE state.account_id = target_account_id AND state.workout_id = target_workout_id FOR UPDATE NOWAIT;
    IF FOUND THEN
        IF missing_state OR current_state.route_input_revision IS DISTINCT FROM old_route_revision OR
           current_state.route_input_sha256 IS DISTINCT FROM old_route_digest THEN RETURN NULL; END IF;
    ELSIF NOT missing_state THEN RETURN NULL;
    END IF;
    -- Match the deployed Go binary encoding, not geometry or a SQL placeholder.
    -- Aggregate at most 50,001 points once; the extra point rejects overflow.
    WITH points AS MATERIALIZED (
        SELECT point.* FROM app.workout_route_points point
         WHERE point.account_id = target_account_id AND point.workout_id = target_workout_id
         ORDER BY point.sequence LIMIT 50001
    ), encoded AS (
        SELECT point.*, pg_catalog.convert_to(
            to_char(point.recorded_at AT TIME ZONE 'UTC', 'YYYY-MM-DD"T"HH24:MI:SS') ||
            CASE WHEN to_char(point.recorded_at AT TIME ZONE 'UTC', 'US') = '000000' THEN ''
                 ELSE '.' || rtrim(to_char(point.recorded_at AT TIME ZONE 'UTC', 'US'), '0') END || 'Z', 'UTF8') AS timestamp_bytes
          FROM points point
    )
    SELECT CASE WHEN count(*) BETWEEN 1 AND 50000 THEN pg_catalog.sha256(pg_catalog.int8send(count(*)) ||
        string_agg(pg_catalog.int8send(point.sequence::bigint) ||
            CASE WHEN point.recorded_at IS NULL THEN '\x00'::bytea ELSE '\x01'::bytea ||
                pg_catalog.int8send(octet_length(point.timestamp_bytes)::bigint) || point.timestamp_bytes END ||
            pg_catalog.float8send(point.latitude) || pg_catalog.float8send(point.longitude) ||
            CASE WHEN point.altitude IS NULL THEN '\x00'::bytea ELSE '\x01'::bytea || pg_catalog.float8send(point.altitude) END ||
            CASE WHEN point.speed IS NULL THEN '\x00'::bytea ELSE '\x01'::bytea || pg_catalog.float8send(point.speed) END ||
            CASE WHEN point.course IS NULL THEN '\x00'::bytea ELSE '\x01'::bytea || pg_catalog.float8send(point.course) END ||
            CASE WHEN point.horizontal_accuracy IS NULL THEN '\x00'::bytea ELSE '\x01'::bytea || pg_catalog.float8send(point.horizontal_accuracy) END ||
            CASE WHEN point.vertical_accuracy IS NULL THEN '\x00'::bytea ELSE '\x01'::bytea || pg_catalog.float8send(point.vertical_accuracy) END ||
            CASE WHEN point.speed_accuracy IS NULL THEN '\x00'::bytea ELSE '\x01'::bytea || pg_catalog.float8send(point.speed_accuracy) END ||
            CASE WHEN point.course_accuracy IS NULL THEN '\x00'::bytea ELSE '\x01'::bytea || pg_catalog.float8send(point.course_accuracy) END,
            ''::bytea ORDER BY point.sequence)) END INTO current_digest
      FROM encoded point;
    IF current_digest IS DISTINCT FROM new_route_digest THEN
        RAISE EXCEPTION 'coverage route input changed' USING ERRCODE = '40001';
    END IF;
    -- Recheck after locking state and hashing; CPU time can consume the lease.
    IF NOT EXISTS (
        SELECT 1 FROM app.coverage_reconciliation_accounts reconciliation
         WHERE reconciliation.account_id = target_account_id AND reconciliation.active_revision = reconciliation_revision
           AND reconciliation.worker_id = claiming_worker AND reconciliation.lease_token = current_lease_token
           AND reconciliation.lease_expires_at >= clock_timestamp()
     ) THEN
        RAISE EXCEPTION 'coverage reconciliation lease expired' USING ERRCODE = '40001';
    END IF;
    IF missing_state THEN
        -- The caller supplies the real canonical digest; revision zero is never stored.
        -- A concurrent initializer can reserve the unique key before checking the
        -- workout FK. Bound that last possible lock wait, too, and propagate 55P03.
        prior_lock_timeout := current_setting('lock_timeout');
        PERFORM set_config('lock_timeout', '1ms', true);
        INSERT INTO app.workout_coverage_states(account_id, workout_id, route_input_revision, route_input_sha256, readiness_state)
        VALUES(target_account_id, target_workout_id, 1, new_route_digest, 'unresolved')
        ON CONFLICT (account_id, workout_id) DO NOTHING
        RETURNING route_input_revision INTO new_revision;
        PERFORM set_config('lock_timeout', prior_lock_timeout, true);
    ELSE
        UPDATE app.workout_coverage_states state SET route_input_revision = state.route_input_revision + 1,
            route_input_sha256 = new_route_digest,
            processing_state = CASE WHEN state.applied_route_input_revision IS NULL THEN 'not_started' ELSE 'stale' END,
            target_job_id = NULL, target_revision = NULL, target_rules_version = NULL, target_sampling_version = NULL,
            target_path_policy_version = NULL, target_generations = NULL, updated_at = transaction_timestamp()
         WHERE state.account_id = target_account_id AND state.workout_id = target_workout_id
           AND state.route_input_revision = old_route_revision AND state.route_input_sha256 = old_route_digest
        RETURNING state.route_input_revision INTO new_revision;
    END IF;
    -- INSERT may wait on a concurrent unique-key initializer. An exception here
    -- rolls back a mutation; returning NULL would allow an expired write to commit.
    IF new_revision IS NOT NULL AND NOT EXISTS (
        SELECT 1 FROM app.coverage_reconciliation_accounts reconciliation
          JOIN app.accounts account ON account.id = reconciliation.account_id AND account.state = 'active'
         WHERE reconciliation.account_id = target_account_id AND target_account_id = app.current_account_id()
           AND reconciliation.active_revision = reconciliation_revision AND reconciliation.worker_id = claiming_worker
           AND reconciliation.lease_token = current_lease_token AND reconciliation.lease_expires_at >= clock_timestamp()
    ) THEN
        RAISE EXCEPTION 'coverage reconciliation lease expired' USING ERRCODE = '40001';
    END IF;
    RETURN new_revision;
END;
$function$;
-- +goose StatementEnd

-- Match the deployed Go 50,000-point bound plus one overflow sentinel in both readers.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.read_coverage_route (target_job_id uuid, claiming_worker text, current_lease_token uuid) RETURNS TABLE (
    sequence integer,
    recorded_at timestamptz,
    latitude double precision,
    longitude double precision,
    altitude double precision,
    speed double precision,
    course double precision,
    horizontal_accuracy double precision,
    vertical_accuracy double precision,
    speed_accuracy double precision,
    course_accuracy double precision,
    workout_started_at timestamptz,
    type_key text,
    provider_label text
) LANGUAGE sql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
    SELECT point.sequence, point.recorded_at, point.latitude, point.longitude, point.altitude, point.speed, point.course,
           point.horizontal_accuracy, point.vertical_accuracy, point.speed_accuracy, point.course_accuracy,
           workout.started_at, workout_type.type_key, workout_type.provider_label
      FROM app.jobs job
      JOIN app.coverage_route_job_contexts context ON context.job_id = job.id AND context.account_id = job.account_id
      JOIN app.workout_coverage_states state ON state.workout_id = context.workout_id AND state.account_id = context.account_id
      JOIN app.workouts workout ON workout.id = context.workout_id AND workout.account_id = context.account_id
      JOIN app.workout_types workout_type ON workout_type.id = workout.workout_type_id AND workout_type.account_id = workout.account_id
      JOIN app.workout_route_points point ON point.workout_id = workout.id AND point.account_id = workout.account_id
     WHERE job.id = $1 AND job.account_id = app.current_account_id() AND job.kind = 'coverage_update_route'
       AND job.status = 'running' AND job.worker_id = $2 AND job.lease_token = $3
       AND job.lease_expires_at >= clock_timestamp() AND job.cancel_requested_at IS NULL
       AND state.target_job_id = job.id AND state.route_input_revision = context.route_input_revision
       AND state.route_input_sha256 = context.route_input_sha256 AND workout.deletion_requested_at IS NULL
     ORDER BY point.sequence LIMIT 50001
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.advance_coverage_reconciliation (
    target_account_id uuid,
    reconciliation_revision bigint,
    claiming_worker text,
    current_lease_token uuid,
    last_workout_id uuid,
    completed boolean,
    scan_interval interval
) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
BEGIN
    IF scan_interval < interval '1 hour' OR scan_interval > interval '30 days' THEN
        RAISE EXCEPTION 'invalid reconciliation scan interval' USING ERRCODE = '22023';
    END IF;
    UPDATE app.coverage_reconciliation_accounts reconciliation SET
        cursor_workout_id = CASE WHEN completed THEN NULL ELSE last_workout_id END,
        completed_revision = CASE WHEN completed THEN GREATEST(reconciliation.completed_revision, reconciliation_revision) ELSE reconciliation.completed_revision END,
        worker_id = CASE WHEN completed THEN NULL ELSE reconciliation.worker_id END,
        lease_token = CASE WHEN completed THEN NULL ELSE reconciliation.lease_token END,
        heartbeat_at = CASE WHEN completed THEN NULL ELSE clock_timestamp() END,
        lease_expires_at = CASE WHEN completed THEN NULL ELSE reconciliation.lease_expires_at END,
        next_scan_at = CASE WHEN completed AND reconciliation.desired_revision > reconciliation_revision THEN transaction_timestamp()
                           WHEN completed THEN transaction_timestamp() + scan_interval ELSE reconciliation.next_scan_at END,
        updated_at = transaction_timestamp()
     WHERE reconciliation.account_id = target_account_id AND reconciliation.active_revision = reconciliation_revision
       AND reconciliation.worker_id = claiming_worker AND reconciliation.lease_token = current_lease_token
       AND reconciliation.lease_expires_at >= clock_timestamp();
    RETURN FOUND;
END;
$function$;
-- +goose StatementEnd
-- END SCHEMA 20 COMPATIBLE FUNCTION PATCH

-- CREATE OR REPLACE preserves the shipped security owner and execute grants.
-- Wake only active accounts with an undeleted routed workout lacking state.
-- Do not reset a live lease's active revision or cursor; its next claim restarts.
INSERT INTO app.coverage_reconciliation_accounts(account_id, desired_revision, next_scan_at)
SELECT account.id, GREATEST(observation.desired_revision, 1), transaction_timestamp()
  FROM app.accounts account CROSS JOIN app.coverage_reconciliation_state observation
 WHERE account.state = 'active' AND observation.singleton AND EXISTS (
    SELECT 1 FROM app.workouts workout
      JOIN app.workout_routes route ON route.account_id = workout.account_id AND route.workout_id = workout.id
      LEFT JOIN app.workout_coverage_states state ON state.account_id = workout.account_id AND state.workout_id = workout.id
     WHERE workout.account_id = account.id AND workout.deletion_requested_at IS NULL AND state.workout_id IS NULL
 )
ON CONFLICT (account_id) DO UPDATE SET
    desired_revision = app.coverage_reconciliation_accounts.desired_revision + 1,
    next_scan_at = transaction_timestamp(), updated_at = transaction_timestamp();

UPDATE app.schema_metadata SET schema_version = 22, minimum_runtime_version = 21 WHERE singleton;

-- +goose Down
-- +goose StatementBegin
DO $block$
BEGIN
    RAISE EXCEPTION 'legacy coverage bootstrap is forward-only' USING ERRCODE = '55000';
END;
$block$;
-- +goose StatementEnd
