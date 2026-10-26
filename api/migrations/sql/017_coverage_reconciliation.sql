-- +goose Up
CREATE TABLE app.coverage_reconciliation_state (
    singleton boolean PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    desired_revision bigint NOT NULL DEFAULT 0 CHECK (desired_revision >= 0),
    last_osm_event_id bigint NOT NULL DEFAULT 0 CHECK (last_osm_event_id >= 0),
    matcher_contract_version text NOT NULL DEFAULT '',
    observed_at timestamptz NOT NULL DEFAULT transaction_timestamp()
);

INSERT INTO
    app.coverage_reconciliation_state (singleton)
VALUES
    (TRUE);

CREATE TABLE app.coverage_reconciliation_accounts (
    account_id uuid PRIMARY KEY REFERENCES app.accounts (id) ON DELETE CASCADE,
    desired_revision bigint NOT NULL CHECK (desired_revision > 0),
    active_revision bigint CHECK (active_revision > 0),
    completed_revision bigint NOT NULL DEFAULT 0 CHECK (completed_revision >= 0),
    cursor_workout_id uuid,
    worker_id text CHECK (
        worker_id IS NULL
        OR length(worker_id) BETWEEN 1 AND 200
    ),
    lease_token uuid,
    heartbeat_at timestamptz,
    lease_expires_at timestamptz,
    next_scan_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    last_failure_code text CHECK (
        last_failure_code IS NULL
        OR length(last_failure_code) <= 64
    ),
    last_failure_summary text CHECK (
        last_failure_summary IS NULL
        OR length(last_failure_summary) <= 512
    ),
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    CHECK ((worker_id IS NULL) = (lease_token IS NULL)),
    CHECK ((worker_id IS NULL) = (heartbeat_at IS NULL)),
    CHECK ((worker_id IS NULL) = (lease_expires_at IS NULL)),
    CHECK (
        active_revision IS NULL
        OR active_revision <= desired_revision
    ),
    CHECK (completed_revision <= desired_revision)
);

CREATE INDEX coverage_reconciliation_accounts_claim_idx ON app.coverage_reconciliation_accounts (next_scan_at, updated_at, account_id)
WHERE
    worker_id IS NULL
    OR lease_expires_at IS NOT NULL;

CREATE TABLE app.coverage_region_catalog (
    region_id text PRIMARY KEY CHECK (region_id ~ '^[a-z][a-z0-9-]{0,63}:[a-z][a-z0-9-]{0,63}$'),
    display_name text NOT NULL CHECK (length(display_name) BETWEEN 1 AND 256),
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp()
);

REVOKE ALL ON app.coverage_reconciliation_state,
app.coverage_reconciliation_accounts,
app.coverage_region_catalog
FROM
    PUBLIC,
    workouts_api,
    workouts_worker,
    workouts_coverage_worker,
    workouts_tiles;

-- +goose StatementBegin
CREATE FUNCTION app.sync_coverage_region_catalog (regions jsonb) RETURNS integer LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
DECLARE supplied_count integer;
BEGIN
    IF regions IS NULL OR jsonb_typeof(regions) IS DISTINCT FROM 'array' OR jsonb_array_length(regions) > 256 THEN
        RAISE EXCEPTION 'invalid coverage region catalog' USING ERRCODE = '22023';
    END IF;
    IF EXISTS (
        SELECT 1 FROM jsonb_array_elements(regions) item
        WHERE jsonb_typeof(item) IS DISTINCT FROM 'object'
           OR (SELECT count( * ) FROM jsonb_object_keys(item)) <> 2
           OR NOT item ?& ARRAY['regionId', 'displayName']
           OR jsonb_typeof(item->'regionId') IS DISTINCT FROM 'string'
           OR jsonb_typeof(item->'displayName') IS DISTINCT FROM 'string'
           OR item->> 'regionId' !~ '^[a-z][a-z0-9-]{0,63}:[a-z][a-z0-9-]{0,63}$'
           OR length(item->> 'displayName') NOT BETWEEN 1 AND 256
    ) OR EXISTS (
        SELECT 1 FROM jsonb_array_elements(regions) item GROUP BY item->> 'regionId' HAVING count( * ) > 1
    ) THEN
        RAISE EXCEPTION 'invalid coverage region catalog item' USING ERRCODE = '22023';
    END IF;
    INSERT INTO app.coverage_region_catalog(region_id, display_name)
    SELECT item->> 'regionId', item->> 'displayName' FROM jsonb_array_elements(regions) item
    ON CONFLICT (region_id) DO UPDATE SET display_name = EXCLUDED.display_name, updated_at = transaction_timestamp();
    GET DIAGNOSTICS supplied_count = ROW_COUNT;
    RETURN supplied_count;
END;
$function$;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.observe_coverage_reconciliation (osm_event_id bigint, new_matcher_contract_version text) RETURNS TABLE (desired_revision bigint, changed boolean) LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
DECLARE state app.coverage_reconciliation_state%ROWTYPE; next_revision bigint;
BEGIN
    IF osm_event_id < 0 OR new_matcher_contract_version !~ '^coverage-production-v[1-9][0-9]*$' THEN
        RAISE EXCEPTION 'invalid coverage reconciliation observation' USING ERRCODE = '22023';
    END IF;
    SELECT * INTO state FROM app.coverage_reconciliation_state WHERE singleton FOR UPDATE;
    IF osm_event_id < state.last_osm_event_id THEN
        desired_revision := state.desired_revision;
        changed := false;
        RETURN NEXT;
        RETURN;
    END IF;
    changed := osm_event_id > state.last_osm_event_id OR new_matcher_contract_version <> state.matcher_contract_version;
    IF changed THEN
        next_revision := state.desired_revision + 1;
        UPDATE app.coverage_reconciliation_state SET desired_revision = next_revision, last_osm_event_id = osm_event_id,
            matcher_contract_version = new_matcher_contract_version, observed_at = transaction_timestamp() WHERE singleton;
        INSERT INTO app.coverage_reconciliation_accounts(account_id, desired_revision, next_scan_at)
        SELECT account.id, next_revision, transaction_timestamp() FROM app.accounts account WHERE account.state = 'active'
        ON CONFLICT (account_id) DO UPDATE SET desired_revision = GREATEST(app.coverage_reconciliation_accounts.desired_revision, EXCLUDED.desired_revision),
            next_scan_at = LEAST(app.coverage_reconciliation_accounts.next_scan_at, transaction_timestamp()), updated_at = transaction_timestamp();
    ELSE
        next_revision := state.desired_revision;
        INSERT INTO app.coverage_reconciliation_accounts(account_id, desired_revision, next_scan_at)
        SELECT account.id, GREATEST(next_revision, 1), transaction_timestamp() FROM app.accounts account
         WHERE account.state = 'active' AND NOT EXISTS (
            SELECT 1 FROM app.coverage_reconciliation_accounts existing WHERE existing.account_id = account.id)
        ON CONFLICT DO NOTHING;
    END IF;
    desired_revision := next_revision; RETURN NEXT;
END;
$function$;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.coverage_reconciliation_observation () RETURNS TABLE (last_osm_event_id bigint, matcher_contract_version text) LANGUAGE sql STABLE SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
    SELECT state.last_osm_event_id, state.matcher_contract_version FROM app.coverage_reconciliation_state state WHERE singleton
$function$;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.claim_coverage_reconciliation (claiming_worker text, new_lease_token uuid, lease_duration interval, runtime_version integer) RETURNS TABLE (account_id uuid, reconciliation_revision bigint, cursor_workout_id uuid) LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
DECLARE candidate app.coverage_reconciliation_accounts%ROWTYPE;
BEGIN
    IF runtime_version < 17 OR claiming_worker = '' OR new_lease_token IS NULL OR
       lease_duration < interval '30 seconds' OR lease_duration > interval '15 minutes' THEN
        RAISE EXCEPTION 'invalid coverage reconciliation claim' USING ERRCODE = '22023';
    END IF;
    SELECT reconciliation. * INTO candidate FROM app.coverage_reconciliation_accounts reconciliation
      JOIN app.accounts account ON account.id = reconciliation.account_id AND account.state = 'active'
     WHERE (reconciliation.worker_id IS NULL OR reconciliation.lease_expires_at < clock_timestamp())
       AND reconciliation.next_scan_at <= transaction_timestamp()
     ORDER BY CASE WHEN reconciliation.desired_revision > reconciliation.completed_revision THEN 0 ELSE 1 END,
        reconciliation.next_scan_at, reconciliation.updated_at, reconciliation.account_id
     LIMIT 1 FOR UPDATE OF reconciliation SKIP LOCKED;
    IF NOT FOUND THEN RETURN; END IF;
    IF candidate.active_revision IS DISTINCT FROM candidate.desired_revision OR candidate.completed_revision >= candidate.desired_revision THEN
        candidate.active_revision := candidate.desired_revision;
        candidate.cursor_workout_id := NULL;
    END IF;
    UPDATE app.coverage_reconciliation_accounts SET active_revision = candidate.active_revision,
        cursor_workout_id = candidate.cursor_workout_id, worker_id = claiming_worker, lease_token = new_lease_token,
        heartbeat_at = clock_timestamp(), lease_expires_at = clock_timestamp() + lease_duration,
        last_failure_code = NULL, last_failure_summary = NULL, updated_at = transaction_timestamp()
     WHERE coverage_reconciliation_accounts.account_id = candidate.account_id;
    account_id := candidate.account_id; reconciliation_revision := candidate.active_revision;
    cursor_workout_id := candidate.cursor_workout_id; RETURN NEXT;
END;
$function$;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.heartbeat_coverage_reconciliation (
    target_account_id uuid,
    reconciliation_revision bigint,
    claiming_worker text,
    current_lease_token uuid,
    lease_duration interval
) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
BEGIN
    UPDATE app.coverage_reconciliation_accounts SET heartbeat_at = clock_timestamp(),
        lease_expires_at = clock_timestamp() + lease_duration, updated_at = transaction_timestamp()
     WHERE account_id = target_account_id AND active_revision = reconciliation_revision AND worker_id = claiming_worker
       AND lease_token = current_lease_token AND lease_expires_at >= clock_timestamp();
    RETURN FOUND;
END;
$function$;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.read_coverage_reconciliation_routes (
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
    SELECT state.workout_id, state.route_input_revision, state.route_input_sha256
      FROM app.coverage_reconciliation_accounts reconciliation
      JOIN app.workout_coverage_states state ON state.account_id = reconciliation.account_id
      JOIN app.workout_routes route ON route.account_id = state.account_id AND route.workout_id = state.workout_id
      JOIN app.workouts workout ON workout.account_id = state.account_id AND workout.id = state.workout_id
     WHERE reconciliation.account_id = target_account_id AND reconciliation.active_revision = reconciliation_revision
       AND reconciliation.worker_id = claiming_worker AND reconciliation.lease_token = current_lease_token
       AND reconciliation.lease_expires_at >= clock_timestamp() AND workout.deletion_requested_at IS NULL
       AND (after_workout_id IS NULL OR state.workout_id > after_workout_id) AND page_limit BETWEEN 1 AND 100
     ORDER BY state.workout_id LIMIT LEAST(page_limit, 100)
$function$;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.read_coverage_reconciliation_route (
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
      JOIN app.workout_coverage_states state ON state.account_id = reconciliation.account_id
      JOIN app.workout_route_points point ON point.account_id = state.account_id AND point.workout_id = state.workout_id
     WHERE reconciliation.account_id = target_account_id AND reconciliation.active_revision = reconciliation_revision
       AND reconciliation.worker_id = claiming_worker AND reconciliation.lease_token = current_lease_token
       AND reconciliation.lease_expires_at >= clock_timestamp() AND state.workout_id = target_workout_id
       AND state.route_input_revision = target_route_revision AND state.route_input_sha256 = target_route_digest
     ORDER BY point.sequence LIMIT 25001
$function$;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.repair_coverage_reconciliation_input (
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
DECLARE new_revision bigint;
BEGIN
    IF octet_length(old_route_digest) <> 32 OR octet_length(new_route_digest) <> 32 THEN
        RAISE EXCEPTION 'invalid reconciliation route digest' USING ERRCODE = '22023';
    END IF;
    PERFORM 1 FROM app.coverage_reconciliation_accounts reconciliation
     WHERE reconciliation.account_id = target_account_id AND reconciliation.active_revision = reconciliation_revision
       AND reconciliation.worker_id = claiming_worker AND reconciliation.lease_token = current_lease_token
       AND reconciliation.lease_expires_at >= clock_timestamp() FOR UPDATE;
    IF NOT FOUND THEN RETURN NULL; END IF;
    UPDATE app.workout_coverage_states state SET route_input_revision = state.route_input_revision + 1,
        route_input_sha256 = new_route_digest,
        processing_state = CASE WHEN state.applied_route_input_revision IS NULL THEN 'not_started' ELSE 'stale' END,
        target_job_id = NULL, target_revision = NULL, target_rules_version = NULL, target_sampling_version = NULL,
        target_path_policy_version = NULL, target_generations = NULL, updated_at = transaction_timestamp()
     WHERE state.account_id = target_account_id AND state.workout_id = target_workout_id
       AND state.route_input_revision = old_route_revision AND state.route_input_sha256 = old_route_digest
     RETURNING state.route_input_revision INTO new_revision;
    RETURN new_revision;
END;
$function$;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.apply_coverage_reconciliation (
    target_account_id uuid,
    reconciliation_revision bigint,
    claiming_worker text,
    current_lease_token uuid,
    target_workout_id uuid,
    target_route_revision bigint,
    target_route_digest bytea,
    readiness_state_value text,
    reason_value text,
    region_generations jsonb,
    new_parent_id uuid,
    new_child_id uuid,
    rules_version_value text,
    sampling_version_value text,
    path_policy_version_value text,
    minimum_traversal_meters_value double precision
) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
DECLARE current_state app.workout_coverage_states%ROWTYPE; ready_at timestamptz; anchor_region text; anchor_generation bigint;
DECLARE route_targets jsonb; enqueued_job uuid; enqueued_count integer; reused boolean; needs_match boolean;
BEGIN
    IF readiness_state_value NOT IN ('pending', 'unavailable', 'map_data_ready') OR region_generations IS NULL OR
       (readiness_state_value = 'pending' AND reason_value IS DISTINCT FROM 'region_not_active') OR
       (readiness_state_value <> 'pending' AND reason_value = 'region_not_active') OR
       (readiness_state_value = 'unavailable' AND reason_value IS DISTINCT FROM 'no_provider_region') OR
       (readiness_state_value <> 'unavailable' AND reason_value = 'no_provider_region') OR
       (readiness_state_value = 'map_data_ready' AND (reason_value IS NOT NULL OR NOT app.valid_coverage_generation_vector(region_generations))) OR
       (readiness_state_value <> 'map_data_ready' AND region_generations <> '[]'::jsonb) THEN
        RAISE EXCEPTION 'invalid reconciliation readiness' USING ERRCODE = '22023';
    END IF;
    PERFORM 1 FROM app.coverage_reconciliation_accounts reconciliation
     WHERE reconciliation.account_id = target_account_id AND reconciliation.active_revision = reconciliation_revision
       AND reconciliation.worker_id = claiming_worker AND reconciliation.lease_token = current_lease_token
       AND reconciliation.lease_expires_at >= clock_timestamp() FOR UPDATE;
    IF NOT FOUND THEN RETURN false; END IF;
    SELECT * INTO current_state FROM app.workout_coverage_states state
     WHERE state.account_id = target_account_id AND state.workout_id = target_workout_id
       AND state.route_input_revision = target_route_revision AND state.route_input_sha256 = target_route_digest FOR UPDATE;
    IF NOT FOUND THEN RETURN false; END IF;
    ready_at := CASE WHEN readiness_state_value = 'map_data_ready' THEN COALESCE(current_state.map_data_ready_at, transaction_timestamp()) END;
    DELETE FROM app.workout_coverage_regions WHERE account_id = target_account_id AND workout_id = target_workout_id;
    INSERT INTO app.workout_coverage_regions(account_id, workout_id, region_id, desired_osm_generation)
    SELECT target_account_id, target_workout_id, item->> 'regionId', (item->> 'generation')::bigint
      FROM jsonb_array_elements(region_generations) item;
    IF readiness_state_value <> 'map_data_ready' THEN
        UPDATE app.workout_coverage_states state SET readiness_state = readiness_state_value, reason = reason_value, map_data_ready_at = NULL,
            processing_state = CASE WHEN state.applied_route_input_revision IS NULL THEN 'not_started' ELSE 'stale' END,
            target_job_id = NULL, target_revision = NULL, target_rules_version = NULL, target_sampling_version = NULL,
            target_path_policy_version = NULL, target_generations = NULL, updated_at = transaction_timestamp()
         WHERE state.account_id = target_account_id AND state.workout_id = target_workout_id;
        RETURN true;
    END IF;
    needs_match := current_state.processing_state <> 'current' OR current_state.applied_route_input_revision IS DISTINCT FROM target_route_revision OR
        current_state.applied_route_input_sha256 IS DISTINCT FROM target_route_digest OR
        current_state.applied_rules_version IS DISTINCT FROM rules_version_value OR
        current_state.applied_sampling_version IS DISTINCT FROM sampling_version_value OR
        current_state.applied_path_policy_version IS DISTINCT FROM path_policy_version_value OR
        NOT app.coverage_generation_vectors_equal(current_state.applied_generations, region_generations);
    UPDATE app.workout_coverage_states state SET readiness_state = 'map_data_ready', reason = NULL, map_data_ready_at = ready_at,
        processing_state = CASE WHEN needs_match AND state.processing_state = 'current' THEN 'stale' ELSE state.processing_state END,
        updated_at = transaction_timestamp() WHERE state.account_id = target_account_id AND state.workout_id = target_workout_id;
    IF NOT needs_match THEN RETURN true; END IF;
    SELECT item->> 'regionId', (item->> 'generation')::bigint INTO anchor_region, anchor_generation
      FROM jsonb_array_elements(region_generations) item ORDER BY item->> 'regionId' LIMIT 1;
    route_targets := jsonb_build_array(jsonb_build_object('jobId', new_child_id, 'workoutId', target_workout_id,
        'routeRevision', target_route_revision, 'generations', region_generations));
    SELECT result.job_id, result.route_count, result.reused INTO enqueued_job, enqueued_count, reused
      FROM app.enqueue_coverage_update(target_account_id, new_parent_id, anchor_region, anchor_generation, reconciliation_revision,
        rules_version_value, sampling_version_value, path_policy_version_value, minimum_traversal_meters_value, route_targets) result;
    RETURN enqueued_job IS NOT NULL;
END;
$function$;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.advance_coverage_reconciliation (
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
        next_scan_at = CASE WHEN completed THEN transaction_timestamp() + scan_interval ELSE reconciliation.next_scan_at END,
        updated_at = transaction_timestamp()
     WHERE reconciliation.account_id = target_account_id AND reconciliation.active_revision = reconciliation_revision
       AND reconciliation.worker_id = claiming_worker AND reconciliation.lease_token = current_lease_token
       AND reconciliation.lease_expires_at >= clock_timestamp();
    RETURN FOUND;
END;
$function$;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.fail_coverage_reconciliation (
    target_account_id uuid,
    reconciliation_revision bigint,
    claiming_worker text,
    current_lease_token uuid,
    failure_code_value text,
    failure_summary_value text,
    retry_delay interval
) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
BEGIN
    IF length(failure_code_value) NOT BETWEEN 1 AND 64 OR length(failure_summary_value) NOT BETWEEN 1 AND 512 OR
       retry_delay < interval '1 minute' OR retry_delay > interval '1 day' THEN
        RAISE EXCEPTION 'invalid reconciliation failure' USING ERRCODE = '22023';
    END IF;
    UPDATE app.coverage_reconciliation_accounts reconciliation SET worker_id = NULL, lease_token = NULL, heartbeat_at = NULL,
        lease_expires_at = NULL, next_scan_at = transaction_timestamp() + retry_delay, last_failure_code = failure_code_value,
        last_failure_summary = failure_summary_value, updated_at = transaction_timestamp()
     WHERE reconciliation.account_id = target_account_id AND reconciliation.active_revision = reconciliation_revision
       AND reconciliation.worker_id = claiming_worker AND reconciliation.lease_token = current_lease_token;
    RETURN FOUND;
END;
$function$;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.supersede_coverage_route (target_job_id uuid, claiming_worker text, current_lease_token uuid, duration_milliseconds integer) RETURNS boolean LANGUAGE sql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
    SELECT app.finish_coverage_route(target_job_id, claiming_worker, current_lease_token, 'superseded', duration_milliseconds)
$function$;

-- +goose StatementEnd

GRANT CREATE ON SCHEMA app TO workouts_security_owner;

ALTER TABLE app.coverage_reconciliation_state OWNER TO workouts_security_owner;

ALTER TABLE app.coverage_reconciliation_accounts OWNER TO workouts_security_owner;

ALTER TABLE app.coverage_region_catalog OWNER TO workouts_security_owner;

ALTER FUNCTION app.sync_coverage_region_catalog (jsonb) OWNER TO workouts_security_owner;

ALTER FUNCTION app.observe_coverage_reconciliation (bigint, text) OWNER TO workouts_security_owner;

ALTER FUNCTION app.coverage_reconciliation_observation () OWNER TO workouts_security_owner;

ALTER FUNCTION app.claim_coverage_reconciliation (text, uuid, interval, integer) OWNER TO workouts_security_owner;

ALTER FUNCTION app.heartbeat_coverage_reconciliation (uuid, bigint, text, uuid, interval) OWNER TO workouts_security_owner;

ALTER FUNCTION app.read_coverage_reconciliation_routes (uuid, bigint, text, uuid, uuid, integer) OWNER TO workouts_security_owner;

ALTER FUNCTION app.read_coverage_reconciliation_route (uuid, bigint, text, uuid, uuid, bigint, bytea) OWNER TO workouts_security_owner;

ALTER FUNCTION app.repair_coverage_reconciliation_input (uuid, bigint, text, uuid, uuid, bigint, bytea, bytea) OWNER TO workouts_security_owner;

ALTER FUNCTION app.apply_coverage_reconciliation (
    uuid,
    bigint,
    text,
    uuid,
    uuid,
    bigint,
    bytea,
    text,
    text,
    jsonb,
    uuid,
    uuid,
    text,
    text,
    text,
    double precision
) OWNER TO workouts_security_owner;

ALTER FUNCTION app.advance_coverage_reconciliation (uuid, bigint, text, uuid, uuid, boolean, interval) OWNER TO workouts_security_owner;

ALTER FUNCTION app.fail_coverage_reconciliation (uuid, bigint, text, uuid, text, text, interval) OWNER TO workouts_security_owner;

ALTER FUNCTION app.supersede_coverage_route (uuid, text, uuid, integer) OWNER TO workouts_security_owner;

REVOKE CREATE ON SCHEMA app
FROM
    workouts_security_owner;

REVOKE ALL ON FUNCTION app.sync_coverage_region_catalog (jsonb),
app.observe_coverage_reconciliation (bigint, text),
app.coverage_reconciliation_observation (),
app.claim_coverage_reconciliation (text, uuid, interval, integer),
app.heartbeat_coverage_reconciliation (uuid, bigint, text, uuid, interval),
app.read_coverage_reconciliation_routes (uuid, bigint, text, uuid, uuid, integer),
app.read_coverage_reconciliation_route (uuid, bigint, text, uuid, uuid, bigint, bytea),
app.repair_coverage_reconciliation_input (uuid, bigint, text, uuid, uuid, bigint, bytea, bytea),
app.apply_coverage_reconciliation (
    uuid,
    bigint,
    text,
    uuid,
    uuid,
    bigint,
    bytea,
    text,
    text,
    jsonb,
    uuid,
    uuid,
    text,
    text,
    text,
    double precision
),
app.advance_coverage_reconciliation (uuid, bigint, text, uuid, uuid, boolean, interval),
app.fail_coverage_reconciliation (uuid, bigint, text, uuid, text, text, interval),
app.supersede_coverage_route (uuid, text, uuid, integer)
FROM
    PUBLIC,
    workouts_api,
    workouts_worker,
    workouts_coverage_worker,
    workouts_tiles;

GRANT
EXECUTE ON FUNCTION app.sync_coverage_region_catalog (jsonb),
app.observe_coverage_reconciliation (bigint, text),
app.coverage_reconciliation_observation (),
app.claim_coverage_reconciliation (text, uuid, interval, integer),
app.heartbeat_coverage_reconciliation (uuid, bigint, text, uuid, interval),
app.read_coverage_reconciliation_routes (uuid, bigint, text, uuid, uuid, integer),
app.read_coverage_reconciliation_route (uuid, bigint, text, uuid, uuid, bigint, bytea),
app.repair_coverage_reconciliation_input (uuid, bigint, text, uuid, uuid, bigint, bytea, bytea),
app.apply_coverage_reconciliation (
    uuid,
    bigint,
    text,
    uuid,
    uuid,
    bigint,
    bytea,
    text,
    text,
    jsonb,
    uuid,
    uuid,
    text,
    text,
    text,
    double precision
),
app.advance_coverage_reconciliation (uuid, bigint, text, uuid, uuid, boolean, interval),
app.fail_coverage_reconciliation (uuid, bigint, text, uuid, text, text, interval),
app.supersede_coverage_route (uuid, text, uuid, integer) TO workouts_coverage_worker;

DELETE FROM app.map_selections;

ALTER TABLE app.map_selections
ADD COLUMN start_date date NOT NULL,
ADD COLUMN end_date date NOT NULL,
ADD COLUMN selection_kind text NOT NULL CHECK (selection_kind IN ('complete_range', 'explicit_subset')),
ADD CHECK (start_date <= end_date);

ALTER TABLE app.workout_path_attributions
ADD COLUMN visit_date date;

UPDATE app.workout_path_attributions attribution
SET
    visit_date = workout.local_start_date
FROM
    app.workouts workout
WHERE
    workout.id = attribution.workout_id
    AND workout.account_id = attribution.account_id;

-- +goose StatementBegin
DO $block$
BEGIN
    IF EXISTS (SELECT 1 FROM app.workout_path_attributions WHERE visit_date IS NULL) THEN
        RAISE EXCEPTION 'coverage attribution lacks a workout-local visit date' USING ERRCODE = '23502';
    END IF;
END;
$block$;

-- +goose StatementEnd

ALTER TABLE app.workout_path_attributions
ALTER COLUMN visit_date
SET NOT NULL;

CREATE TABLE app.account_path_daily_rollups (
    account_id uuid NOT NULL,
    logical_path_id uuid NOT NULL,
    visit_date date NOT NULL,
    workout_count integer NOT NULL CHECK (workout_count > 0),
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    PRIMARY KEY (account_id, logical_path_id, visit_date),
    FOREIGN KEY (account_id, logical_path_id) REFERENCES app.coverage_paths (account_id, logical_path_id) ON DELETE CASCADE
);

CREATE TABLE app.account_path_all_time (
    account_id uuid NOT NULL,
    logical_path_id uuid NOT NULL,
    workout_count integer NOT NULL CHECK (workout_count > 0),
    first_visit_date date NOT NULL,
    latest_visit_date date NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    PRIMARY KEY (account_id, logical_path_id),
    FOREIGN KEY (account_id, logical_path_id) REFERENCES app.coverage_paths (account_id, logical_path_id) ON DELETE CASCADE,
    CHECK (first_visit_date <= latest_visit_date)
);

CREATE INDEX account_path_all_time_count_idx ON app.account_path_all_time (account_id, workout_count DESC, logical_path_id);

INSERT INTO
    app.account_path_daily_rollups (account_id, logical_path_id, visit_date, workout_count)
SELECT
    account_id,
    logical_path_id,
    visit_date,
    count( * )::integer
FROM
    app.workout_path_attributions
GROUP BY
    account_id,
    logical_path_id,
    visit_date;

INSERT INTO
    app.account_path_all_time (account_id, logical_path_id, workout_count, first_visit_date, latest_visit_date)
SELECT
    account_id,
    logical_path_id,
    count( * )::integer,
    min(visit_date),
    max(visit_date)
FROM
    app.workout_path_attributions
GROUP BY
    account_id,
    logical_path_id;

ALTER TABLE app.account_path_daily_rollups ENABLE ROW LEVEL SECURITY;

ALTER TABLE app.account_path_daily_rollups FORCE ROW LEVEL SECURITY;

ALTER TABLE app.account_path_all_time ENABLE ROW LEVEL SECURITY;

ALTER TABLE app.account_path_all_time FORCE ROW LEVEL SECURITY;

CREATE POLICY account_path_daily_rollups_account_policy ON app.account_path_daily_rollups USING (account_id = app.current_account_id ())
WITH
    CHECK (account_id = app.current_account_id ());

CREATE POLICY account_path_daily_rollups_owner_policy ON app.account_path_daily_rollups TO workouts_security_owner USING (TRUE)
WITH
    CHECK (TRUE);

CREATE POLICY account_path_all_time_account_policy ON app.account_path_all_time USING (account_id = app.current_account_id ())
WITH
    CHECK (account_id = app.current_account_id ());

CREATE POLICY account_path_all_time_owner_policy ON app.account_path_all_time TO workouts_security_owner USING (TRUE)
WITH
    CHECK (TRUE);

-- +goose StatementBegin
CREATE FUNCTION app.set_coverage_attribution_visit_date () RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
BEGIN
    SELECT workout.local_start_date INTO NEW.visit_date FROM app.workouts workout
     WHERE workout.id = NEW.workout_id AND workout.account_id = NEW.account_id;
    IF NEW.visit_date IS NULL THEN
        RAISE EXCEPTION 'coverage attribution requires workout local date' USING ERRCODE = '23502';
    END IF;
    RETURN NEW;
END;
$function$;

-- +goose StatementEnd

CREATE TRIGGER workout_path_attributions_visit_date_before_insert
BEFORE INSERT ON app.workout_path_attributions FOR EACH ROW
EXECUTE FUNCTION app.set_coverage_attribution_visit_date ();

-- +goose StatementBegin
CREATE FUNCTION app.increment_coverage_path_rollups () RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
BEGIN
    INSERT INTO app.account_path_daily_rollups(account_id, logical_path_id, visit_date, workout_count)
    VALUES(NEW.account_id, NEW.logical_path_id, NEW.visit_date, 1)
    ON CONFLICT (account_id, logical_path_id, visit_date) DO UPDATE
       SET workout_count = app.account_path_daily_rollups.workout_count + 1, updated_at = transaction_timestamp();
    INSERT INTO app.account_path_all_time(account_id, logical_path_id, workout_count, first_visit_date, latest_visit_date)
    VALUES(NEW.account_id, NEW.logical_path_id, 1, NEW.visit_date, NEW.visit_date)
    ON CONFLICT (account_id, logical_path_id) DO UPDATE
       SET workout_count = app.account_path_all_time.workout_count + 1,
           first_visit_date = LEAST(app.account_path_all_time.first_visit_date, EXCLUDED.first_visit_date),
           latest_visit_date = GREATEST(app.account_path_all_time.latest_visit_date, EXCLUDED.latest_visit_date),
           updated_at = transaction_timestamp();
    RETURN NEW;
END;
$function$;

-- +goose StatementEnd

CREATE TRIGGER workout_path_attributions_rollups_after_insert
AFTER INSERT ON app.workout_path_attributions FOR EACH ROW
EXECUTE FUNCTION app.increment_coverage_path_rollups ();

-- +goose StatementBegin
CREATE FUNCTION app.decrement_coverage_path_rollups () RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
BEGIN
    UPDATE app.account_path_daily_rollups SET workout_count = workout_count - 1, updated_at = transaction_timestamp()
     WHERE account_id = OLD.account_id AND logical_path_id = OLD.logical_path_id AND visit_date = OLD.visit_date AND workout_count > 1;
    IF NOT FOUND THEN
        DELETE FROM app.account_path_daily_rollups WHERE account_id = OLD.account_id
          AND logical_path_id = OLD.logical_path_id AND visit_date = OLD.visit_date;
    END IF;
    DELETE FROM app.account_path_all_time WHERE account_id = OLD.account_id AND logical_path_id = OLD.logical_path_id;
    INSERT INTO app.account_path_all_time(account_id, logical_path_id, workout_count, first_visit_date, latest_visit_date)
    SELECT attribution.account_id, attribution.logical_path_id, count( * )::integer, min(attribution.visit_date), max(attribution.visit_date)
      FROM app.workout_path_attributions attribution
     WHERE attribution.account_id = OLD.account_id AND attribution.logical_path_id = OLD.logical_path_id
     GROUP BY attribution.account_id, attribution.logical_path_id;
    RETURN OLD;
END;
$function$;

-- +goose StatementEnd

CREATE TRIGGER workout_path_attributions_rollups_after_delete
AFTER DELETE ON app.workout_path_attributions FOR EACH ROW
EXECUTE FUNCTION app.decrement_coverage_path_rollups ();

DROP TRIGGER workout_coverage_states_data_generation_after_write ON app.workout_coverage_states;

CREATE TRIGGER workout_coverage_states_data_generation_after_current
AFTER UPDATE OF processing_finished_at ON app.workout_coverage_states FOR EACH ROW WHEN (
    NEW.processing_state = 'current'
    AND NEW.processing_finished_at IS DISTINCT FROM OLD.processing_finished_at
)
EXECUTE FUNCTION app.advance_account_data_generation ();

-- +goose StatementBegin
CREATE FUNCTION app.map_selection_path_counts (target_account_id uuid, target_session_id uuid, target_selection_id uuid, target_generation bigint) RETURNS TABLE (
    logical_path_id uuid,
    name text,
    locality_name text,
    broad_class text,
    selected_workout_count bigint,
    all_time_workout_count bigint,
    first_visit_date date,
    latest_visit_date date,
    minimum_longitude double precision,
    minimum_latitude double precision,
    maximum_longitude double precision,
    maximum_latitude double precision
) LANGUAGE sql STABLE SECURITY DEFINER
SET
    search_path = pg_catalog,
    app,
    public AS $function$
    WITH selected AS MATERIALIZED (
        SELECT selection.start_date, selection.end_date, selection.selection_kind
          FROM app.map_selections selection
          JOIN app.sessions session ON session.id = selection.session_id
          JOIN app.authentication_principals principal ON principal.id = session.principal_id
          JOIN app.users account_owner ON account_owner.principal_id = principal.id AND account_owner.account_id = selection.account_id
          JOIN app.accounts account ON account.id = account_owner.account_id
          JOIN app.account_data_generations data_generation ON data_generation.account_id = selection.account_id
         WHERE selection.id = target_selection_id AND selection.account_id = target_account_id
           AND selection.session_id = target_session_id AND selection.generation = target_generation
           AND data_generation.generation = target_generation AND selection.expires_at > transaction_timestamp()
           AND session.revoked_at IS NULL AND session.expires_at > transaction_timestamp()
           AND principal.disabled_at IS NULL AND account.state = 'active' AND target_account_id = app.current_account_id()
    ), counts AS MATERIALIZED (
        SELECT rollup.logical_path_id, sum(rollup.workout_count)::bigint AS workout_count
          FROM selected selection JOIN app.account_path_daily_rollups rollup
            ON selection.selection_kind = 'complete_range' AND rollup.account_id = target_account_id
           AND rollup.visit_date BETWEEN selection.start_date AND selection.end_date
         GROUP BY rollup.logical_path_id
        UNION ALL
        SELECT attribution.logical_path_id, count( * )::bigint
          FROM selected selection JOIN app.map_selection_workouts selected_workout
            ON selection.selection_kind = 'explicit_subset' AND selected_workout.account_id = target_account_id
           AND selected_workout.selection_id = target_selection_id
          JOIN app.workout_path_attributions attribution ON attribution.account_id = selected_workout.account_id
           AND attribution.workout_id = selected_workout.workout_id
         GROUP BY attribution.logical_path_id
    ), bounds AS (
        SELECT match.logical_path_id, ST_Extent(match.geom)::box2d AS extent
          FROM app.map_selection_workouts selected_workout
          JOIN app.workout_segment_matches match ON match.account_id = selected_workout.account_id
           AND match.workout_id = selected_workout.workout_id
         WHERE selected_workout.account_id = target_account_id AND selected_workout.selection_id = target_selection_id
         GROUP BY match.logical_path_id
    )
    SELECT path.logical_path_id, path.name, path.locality_name, path.broad_class, counts.workout_count,
           all_time.workout_count::bigint, all_time.first_visit_date, all_time.latest_visit_date,
           ST_XMin(bounds.extent), ST_YMin(bounds.extent), ST_XMax(bounds.extent), ST_YMax(bounds.extent)
      FROM counts JOIN app.coverage_paths path ON path.account_id = target_account_id AND path.logical_path_id = counts.logical_path_id
      JOIN app.account_path_all_time all_time ON all_time.account_id = path.account_id AND all_time.logical_path_id = path.logical_path_id
      JOIN bounds ON bounds.logical_path_id = path.logical_path_id
     WHERE counts.workout_count > 0
$function$;

-- +goose StatementEnd

REVOKE ALL ON app.account_path_daily_rollups,
app.account_path_all_time
FROM
    PUBLIC,
    workouts_api,
    workouts_worker,
    workouts_tiles;

GRANT
SELECT
    ON app.account_path_daily_rollups,
    app.account_path_all_time TO workouts_api;

GRANT CREATE ON SCHEMA app TO workouts_security_owner;

ALTER TABLE app.account_path_daily_rollups OWNER TO workouts_security_owner;

ALTER TABLE app.account_path_all_time OWNER TO workouts_security_owner;

ALTER FUNCTION app.set_coverage_attribution_visit_date () OWNER TO workouts_security_owner;

ALTER FUNCTION app.increment_coverage_path_rollups () OWNER TO workouts_security_owner;

ALTER FUNCTION app.decrement_coverage_path_rollups () OWNER TO workouts_security_owner;

ALTER FUNCTION app.map_selection_path_counts (uuid, uuid, uuid, bigint) OWNER TO workouts_security_owner;

REVOKE CREATE ON SCHEMA app
FROM
    workouts_security_owner;

REVOKE ALL ON FUNCTION app.set_coverage_attribution_visit_date (),
app.increment_coverage_path_rollups (),
app.decrement_coverage_path_rollups (),
app.map_selection_path_counts (uuid, uuid, uuid, bigint)
FROM
    PUBLIC,
    workouts_api,
    workouts_worker,
    workouts_tiles;

GRANT
EXECUTE ON FUNCTION app.map_selection_path_counts (uuid, uuid, uuid, bigint) TO workouts_api;

-- +goose StatementBegin
CREATE FUNCTION app.coverage_count_bucket (workout_count bigint) RETURNS integer LANGUAGE sql IMMUTABLE STRICT
SET
    search_path = pg_catalog AS $function$
    SELECT CASE
        WHEN workout_count = 1 THEN 1
        WHEN workout_count = 2 THEN 2
        WHEN workout_count BETWEEN 3 AND 5 THEN 3
        WHEN workout_count BETWEEN 6 AND 10 THEN 4
        WHEN workout_count BETWEEN 11 AND 25 THEN 5
        WHEN workout_count >= 26 THEN 6
    END
$function$;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.coverage_mvt (z integer, x integer, y integer, query_params json DEFAULT '{}'::json) RETURNS bytea LANGUAGE plpgsql STABLE SECURITY DEFINER
SET
    search_path = pg_catalog,
    app,
    public AS $function$
DECLARE target_account_id uuid; target_session_id uuid; target_selection_id uuid;
DECLARE target_generation bigint; bounds box2d; tile bytea;
BEGIN
    IF z NOT BETWEEN 0 AND 22 OR x < 0 OR y < 0 OR x >= (1::bigint << z) OR y >= (1::bigint << z) THEN
        RAISE EXCEPTION 'invalid tile coordinates' USING ERRCODE = '22023';
    END IF;
    IF json_typeof(query_params) IS DISTINCT FROM 'object' OR
       (SELECT count( * ) FROM json_object_keys(query_params)) <> 4 OR
       NOT query_params::jsonb ?& ARRAY['target_account_id', 'target_session_id', 'target_selection_id', 'target_generation'] THEN
        RAISE EXCEPTION 'invalid tile scope' USING ERRCODE = '22023';
    END IF;
    BEGIN
        target_account_id := (query_params->> 'target_account_id')::uuid;
        target_session_id := (query_params->> 'target_session_id')::uuid;
        target_selection_id := (query_params->> 'target_selection_id')::uuid;
        target_generation := (query_params->> 'target_generation')::bigint;
    EXCEPTION WHEN invalid_text_representation OR numeric_value_out_of_range THEN
        RAISE EXCEPTION 'invalid tile scope' USING ERRCODE = '22023';
    END;
    IF target_account_id IS NULL OR target_session_id IS NULL OR target_selection_id IS NULL OR target_generation < 1 THEN
        RAISE EXCEPTION 'invalid tile scope' USING ERRCODE = '22023';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM app.map_selections selection
        JOIN app.sessions session_row ON session_row.id = selection.session_id
        JOIN app.authentication_principals principal ON principal.id = session_row.principal_id
        JOIN app.users account_owner ON account_owner.principal_id = principal.id AND account_owner.account_id = selection.account_id
        JOIN app.accounts account ON account.id = account_owner.account_id
        JOIN app.account_data_generations data_generation ON data_generation.account_id = selection.account_id
        WHERE selection.id = target_selection_id AND selection.account_id = target_account_id
          AND selection.session_id = target_session_id AND selection.generation = target_generation
          AND data_generation.generation = target_generation AND selection.expires_at > transaction_timestamp()
          AND session_row.revoked_at IS NULL AND session_row.expires_at > transaction_timestamp()
          AND principal.disabled_at IS NULL AND account.state = 'active'
    ) THEN RAISE EXCEPTION 'invalid or expired map selection' USING ERRCODE = '42501'; END IF;
    bounds := ST_TileEnvelope(z, x, y);
    WITH selected AS MATERIALIZED (
        SELECT selection.selection_kind, selection.start_date, selection.end_date
        FROM app.map_selections selection WHERE selection.id = target_selection_id AND selection.account_id = target_account_id
    ), counts AS MATERIALIZED (
        SELECT rollup.logical_path_id, sum(rollup.workout_count)::bigint AS workout_count
        FROM selected selection JOIN app.account_path_daily_rollups rollup
          ON selection.selection_kind = 'complete_range' AND rollup.account_id = target_account_id
         AND rollup.visit_date BETWEEN selection.start_date AND selection.end_date
        GROUP BY rollup.logical_path_id
        UNION ALL
        SELECT attribution.logical_path_id, count( * )::bigint
        FROM selected selection JOIN app.map_selection_workouts selected_workout
          ON selection.selection_kind = 'explicit_subset' AND selected_workout.account_id = target_account_id
         AND selected_workout.selection_id = target_selection_id
        JOIN app.workout_path_attributions attribution ON attribution.account_id = selected_workout.account_id
         AND attribution.workout_id = selected_workout.workout_id
        GROUP BY attribution.logical_path_id
    ), covered AS MATERIALIZED (
        SELECT match.logical_path_id, match.physical_segment_id,
               NULLIF(segment.tags->> 'workouts:park_id', '') AS park_id, ST_UnaryUnion(ST_Collect(match.geom)) AS geom
        FROM app.map_selection_workouts selected_workout
        JOIN app.workout_segment_matches match ON match.account_id = selected_workout.account_id
         AND match.workout_id = selected_workout.workout_id
        JOIN app.path_segments segment ON segment.account_id = match.account_id AND segment.region_id = match.region_id
         AND segment.generation_id = match.generation_id AND segment.physical_segment_id = match.physical_segment_id
        WHERE selected_workout.account_id = target_account_id AND selected_workout.selection_id = target_selection_id
          AND match.geom && ST_Transform(bounds, 4326)
        GROUP BY match.logical_path_id, match.physical_segment_id, NULLIF(segment.tags->> 'workouts:park_id', '')
    )
    SELECT COALESCE(ST_AsMVT(tile_rows, 'coverage', 4096, 'geometry'), '') INTO tile FROM (
        SELECT upper(replace(covered.logical_path_id::text, '-', '')) AS logical_path_id,
               upper(replace(covered.physical_segment_id::text, '-', '')) AS physical_segment_id,
               upper(replace(covered.park_id, '-', '')) AS park_id,
               path.name, path.locality_name, path.broad_class,
               counts.workout_count AS selected_workout_count,
               app.coverage_count_bucket(counts.workout_count) AS count_bucket,
               ST_AsMVTGeom(ST_Transform(covered.geom, 3857), bounds, 4096, 64, true) AS geometry
        FROM covered JOIN counts ON counts.logical_path_id = covered.logical_path_id
        JOIN app.coverage_paths path ON path.account_id = target_account_id AND path.logical_path_id = covered.logical_path_id
        WHERE counts.workout_count > 0 AND ST_Intersects(ST_Transform(covered.geom, 3857), bounds)
        ORDER BY covered.logical_path_id, covered.physical_segment_id
    ) tile_rows WHERE tile_rows.geometry IS NOT NULL;
    RETURN COALESCE(tile, ''::bytea);
END;
$function$;

-- +goose StatementEnd

GRANT CREATE ON SCHEMA app TO workouts_security_owner;

ALTER FUNCTION app.coverage_count_bucket (bigint) OWNER TO workouts_security_owner;

ALTER FUNCTION app.coverage_mvt (integer, integer, integer, json) OWNER TO workouts_security_owner;

REVOKE CREATE ON SCHEMA app
FROM
    workouts_security_owner;

REVOKE ALL ON FUNCTION app.coverage_count_bucket (bigint),
app.coverage_mvt (integer, integer, integer, json)
FROM
    PUBLIC,
    workouts_api,
    workouts_worker,
    workouts_tiles;

GRANT
EXECUTE ON FUNCTION app.coverage_count_bucket (bigint) TO workouts_api;

GRANT
EXECUTE ON FUNCTION app.coverage_mvt (integer, integer, integer, json) TO workouts_tiles;

UPDATE app.schema_metadata
SET
    schema_version = 17,
    minimum_runtime_version = 16
WHERE
    singleton;

-- +goose Down
UPDATE app.schema_metadata
SET
    schema_version = 16,
    minimum_runtime_version = 15
WHERE
    singleton;

DROP FUNCTION app.coverage_mvt (integer, integer, integer, json);

DROP FUNCTION app.coverage_count_bucket (bigint);

DROP FUNCTION app.map_selection_path_counts (uuid, uuid, uuid, bigint);

DROP TRIGGER workout_coverage_states_data_generation_after_current ON app.workout_coverage_states;

CREATE TRIGGER workout_coverage_states_data_generation_after_write
AFTER INSERT OR UPDATE OR DELETE ON app.workout_coverage_states FOR EACH ROW
EXECUTE FUNCTION app.advance_account_data_generation ();

DROP TRIGGER workout_path_attributions_rollups_after_delete ON app.workout_path_attributions;

DROP FUNCTION app.decrement_coverage_path_rollups ();

DROP TRIGGER workout_path_attributions_rollups_after_insert ON app.workout_path_attributions;

DROP FUNCTION app.increment_coverage_path_rollups ();

DROP TRIGGER workout_path_attributions_visit_date_before_insert ON app.workout_path_attributions;

DROP FUNCTION app.set_coverage_attribution_visit_date ();

DROP TABLE app.account_path_all_time;

DROP TABLE app.account_path_daily_rollups;

ALTER TABLE app.workout_path_attributions
DROP COLUMN visit_date;

ALTER TABLE app.map_selections
DROP COLUMN selection_kind,
DROP COLUMN end_date,
DROP COLUMN start_date;

DROP FUNCTION app.fail_coverage_reconciliation (uuid, bigint, text, uuid, text, text, interval);

DROP FUNCTION app.supersede_coverage_route (uuid, text, uuid, integer);

DROP FUNCTION app.advance_coverage_reconciliation (uuid, bigint, text, uuid, uuid, boolean, interval);

DROP FUNCTION app.apply_coverage_reconciliation (
    uuid,
    bigint,
    text,
    uuid,
    uuid,
    bigint,
    bytea,
    text,
    text,
    jsonb,
    uuid,
    uuid,
    text,
    text,
    text,
    double precision
);

DROP FUNCTION app.repair_coverage_reconciliation_input (uuid, bigint, text, uuid, uuid, bigint, bytea, bytea);

DROP FUNCTION app.read_coverage_reconciliation_route (uuid, bigint, text, uuid, uuid, bigint, bytea);

DROP FUNCTION app.read_coverage_reconciliation_routes (uuid, bigint, text, uuid, uuid, integer);

DROP FUNCTION app.heartbeat_coverage_reconciliation (uuid, bigint, text, uuid, interval);

DROP FUNCTION app.claim_coverage_reconciliation (text, uuid, interval, integer);

DROP FUNCTION app.coverage_reconciliation_observation ();

DROP FUNCTION app.observe_coverage_reconciliation (bigint, text);

DROP FUNCTION app.sync_coverage_region_catalog (jsonb);

DROP TABLE app.coverage_reconciliation_accounts;

DROP TABLE app.coverage_reconciliation_state;

DROP TABLE app.coverage_region_catalog;
