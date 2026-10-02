-- +goose Up
SELECT
    app.assert_no_active_manual_ingest ();

SELECT
    app.assert_no_active_scheduled_ingest ();

-- +goose StatementBegin
DO $block$
BEGIN
    IF to_regrole('workouts_coverage_worker') IS NULL THEN
        RAISE EXCEPTION 'required role workouts_coverage_worker is missing' USING ERRCODE = '55000';
    END IF;
END;
$block$;

-- +goose StatementEnd

CREATE TABLE app.workout_coverage_states (
    account_id uuid NOT NULL,
    workout_id uuid NOT NULL,
    route_input_revision bigint NOT NULL CHECK (route_input_revision > 0),
    route_input_sha256 bytea NOT NULL CHECK (octet_length(route_input_sha256) = 32),
    readiness_state text NOT NULL CHECK (readiness_state IN ('unresolved', 'pending', 'unavailable', 'map_data_ready')),
    processing_state text NOT NULL DEFAULT 'not_started' CHECK (processing_state = 'not_started'),
    reason text CHECK (reason IN ('region_not_active', 'no_provider_region')),
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    map_data_ready_at timestamptz,
    PRIMARY KEY (account_id, workout_id),
    FOREIGN KEY (workout_id, account_id) REFERENCES app.workouts (id, account_id) ON DELETE CASCADE,
    CHECK (
        (
            readiness_state = 'pending'
            AND reason = 'region_not_active'
        )
        OR (
            readiness_state = 'unavailable'
            AND reason = 'no_provider_region'
        )
        OR (
            readiness_state IN ('unresolved', 'map_data_ready')
            AND reason IS NULL
        )
    ),
    CHECK ((readiness_state = 'map_data_ready') = (map_data_ready_at IS NOT NULL))
);

CREATE TABLE app.workout_coverage_regions (
    account_id uuid NOT NULL,
    workout_id uuid NOT NULL,
    region_id text NOT NULL CHECK (region_id ~ '^[a-z][a-z0-9-]{0,63}:[a-z][a-z0-9-]{0,63}$'),
    desired_osm_generation bigint NOT NULL CHECK (desired_osm_generation > 0),
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    PRIMARY KEY (account_id, workout_id, region_id),
    FOREIGN KEY (account_id, workout_id) REFERENCES app.workout_coverage_states (account_id, workout_id) ON DELETE CASCADE
);

ALTER TABLE app.workout_coverage_states ENABLE ROW LEVEL SECURITY;

ALTER TABLE app.workout_coverage_states FORCE ROW LEVEL SECURITY;

CREATE POLICY workout_coverage_states_account_policy ON app.workout_coverage_states USING (account_id = app.current_account_id ())
WITH
    CHECK (account_id = app.current_account_id ());

CREATE POLICY workout_coverage_states_owner_policy ON app.workout_coverage_states TO workouts_security_owner USING (TRUE)
WITH
    CHECK (TRUE);

ALTER TABLE app.workout_coverage_regions ENABLE ROW LEVEL SECURITY;

ALTER TABLE app.workout_coverage_regions FORCE ROW LEVEL SECURITY;

CREATE POLICY workout_coverage_regions_account_policy ON app.workout_coverage_regions USING (account_id = app.current_account_id ())
WITH
    CHECK (account_id = app.current_account_id ());

CREATE POLICY workout_coverage_regions_owner_policy ON app.workout_coverage_regions TO workouts_security_owner USING (TRUE)
WITH
    CHECK (TRUE);

CREATE TRIGGER workout_coverage_states_data_generation_after_write
AFTER INSERT OR UPDATE OR DELETE ON app.workout_coverage_states FOR EACH ROW
EXECUTE FUNCTION app.advance_account_data_generation ();

-- +goose StatementBegin
CREATE FUNCTION app.assert_workout_coverage_job_lease (target_account_id uuid, target_job_id uuid, claiming_worker text, current_lease_token uuid) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM app.jobs job WHERE job.id = target_job_id AND job.account_id = target_account_id
          AND job.kind IN ('manual_ingest_source', 'scheduled_ingest_source') AND job.status = 'running'
          AND job.worker_id = claiming_worker AND job.lease_token = current_lease_token
          AND job.lease_expires_at >= clock_timestamp()
    ) THEN RAISE EXCEPTION 'coverage readiness requires a live ingest lease' USING ERRCODE = '42501'; END IF;
END;
$function$;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.initialize_workout_coverage (
    target_account_id uuid,
    target_workout_id uuid,
    new_route_input_sha256 bytea,
    target_job_id uuid,
    claiming_worker text,
    current_lease_token uuid
) RETURNS TABLE (route_input_revision bigint, digest_changed boolean) LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
DECLARE prior_revision bigint; prior_digest bytea;
BEGIN
    PERFORM app.assert_workout_coverage_job_lease(target_account_id, target_job_id, claiming_worker, current_lease_token);
    IF octet_length(new_route_input_sha256) <> 32 OR NOT EXISTS (
        SELECT 1 FROM app.workouts workout WHERE workout.account_id = target_account_id
          AND workout.id = target_workout_id AND workout.deletion_requested_at IS NULL
    ) THEN RAISE EXCEPTION 'invalid workout coverage initialization' USING ERRCODE = '22023'; END IF;
    SELECT state.route_input_revision, state.route_input_sha256 INTO prior_revision, prior_digest
      FROM app.workout_coverage_states state WHERE state.account_id = target_account_id
       AND state.workout_id = target_workout_id FOR UPDATE;
    IF NOT FOUND THEN
        INSERT INTO app.workout_coverage_states(account_id, workout_id, route_input_revision, route_input_sha256, readiness_state)
        VALUES(target_account_id, target_workout_id, 1, new_route_input_sha256, 'unresolved');
        route_input_revision := 1; digest_changed := true; RETURN NEXT; RETURN;
    END IF;
    IF prior_digest = new_route_input_sha256 THEN
        route_input_revision := prior_revision; digest_changed := false; RETURN NEXT; RETURN;
    END IF;
    DELETE FROM app.workout_coverage_regions region WHERE region.account_id = target_account_id AND region.workout_id = target_workout_id;
    UPDATE app.workout_coverage_states state SET route_input_revision = prior_revision + 1,
        route_input_sha256 = new_route_input_sha256, readiness_state = 'unresolved', reason = NULL,
        map_data_ready_at = NULL, updated_at = transaction_timestamp()
     WHERE state.account_id = target_account_id AND state.workout_id = target_workout_id;
    route_input_revision := prior_revision + 1; digest_changed := true; RETURN NEXT;
END;
$function$;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.set_workout_coverage_readiness (
    target_account_id uuid,
    target_workout_id uuid,
    target_route_input_revision bigint,
    new_readiness_state text,
    new_reason text,
    new_regions jsonb,
    target_job_id uuid,
    claiming_worker text,
    current_lease_token uuid
) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
DECLARE region_value jsonb; changed boolean;
BEGIN
    PERFORM app.assert_workout_coverage_job_lease(target_account_id, target_job_id, claiming_worker, current_lease_token);
    IF new_readiness_state NOT IN ('pending', 'unavailable', 'map_data_ready')
       OR (new_readiness_state = 'pending' AND new_reason IS DISTINCT FROM 'region_not_active')
       OR (new_readiness_state = 'unavailable' AND new_reason IS DISTINCT FROM 'no_provider_region')
       OR (new_readiness_state = 'map_data_ready' AND new_reason IS NOT NULL)
       OR new_regions IS NULL OR jsonb_typeof(new_regions) IS DISTINCT FROM 'array'
       OR (new_readiness_state <> 'map_data_ready' AND jsonb_array_length(new_regions) <> 0)
       OR (new_readiness_state = 'map_data_ready' AND jsonb_array_length(new_regions) = 0) THEN
        RAISE EXCEPTION 'invalid workout coverage readiness' USING ERRCODE = '22023';
    END IF;
    PERFORM 1 FROM app.workout_coverage_states state WHERE state.account_id = target_account_id
      AND state.workout_id = target_workout_id AND state.route_input_revision = target_route_input_revision FOR UPDATE;
    IF NOT FOUND THEN RETURN false; END IF;
    DELETE FROM app.workout_coverage_regions region WHERE region.account_id = target_account_id AND region.workout_id = target_workout_id;
    FOR region_value IN SELECT value FROM jsonb_array_elements(new_regions) LOOP
        IF jsonb_typeof(region_value) IS DISTINCT FROM 'object' OR (SELECT count( * ) FROM jsonb_object_keys(region_value)) <> 2
           OR NOT (region_value ? 'regionId' AND region_value ? 'generation')
           OR region_value->> 'regionId' !~ '^[a-z][a-z0-9-]{0,63}:[a-z][a-z0-9-]{0,63}$'
           OR region_value->> 'generation' !~ '^[1-9][0-9]*$' THEN
            RAISE EXCEPTION 'invalid workout coverage region' USING ERRCODE = '22023';
        END IF;
        INSERT INTO app.workout_coverage_regions(account_id, workout_id, region_id, desired_osm_generation)
        VALUES(target_account_id, target_workout_id, region_value->> 'regionId', (region_value->> 'generation')::bigint);
    END LOOP;
    UPDATE app.workout_coverage_states state SET readiness_state = new_readiness_state, reason = new_reason,
        map_data_ready_at = CASE WHEN new_readiness_state = 'map_data_ready' THEN transaction_timestamp() ELSE NULL END,
        updated_at = transaction_timestamp()
     WHERE state.account_id = target_account_id AND state.workout_id = target_workout_id
       AND (state.readiness_state, state.reason) IS DISTINCT FROM (new_readiness_state, new_reason);
    GET DIAGNOSTICS changed = ROW_COUNT; RETURN changed;
END;
$function$;

-- +goose StatementEnd

REVOKE ALL ON app.workout_coverage_states,
app.workout_coverage_regions
FROM
    PUBLIC,
    workouts_api,
    workouts_worker;

GRANT
SELECT
    ON app.workout_coverage_states,
    app.workout_coverage_regions TO workouts_api;

GRANT CREATE ON SCHEMA app TO workouts_security_owner;

ALTER TABLE app.workout_coverage_states OWNER TO workouts_security_owner;

ALTER TABLE app.workout_coverage_regions OWNER TO workouts_security_owner;

ALTER FUNCTION app.assert_workout_coverage_job_lease (uuid, uuid, text, uuid) OWNER TO workouts_security_owner;

ALTER FUNCTION app.initialize_workout_coverage (uuid, uuid, bytea, uuid, text, uuid) OWNER TO workouts_security_owner;

ALTER FUNCTION app.set_workout_coverage_readiness (uuid, uuid, bigint, text, text, jsonb, uuid, text, uuid) OWNER TO workouts_security_owner;

REVOKE CREATE ON SCHEMA app
FROM
    workouts_security_owner;

REVOKE ALL ON FUNCTION app.assert_workout_coverage_job_lease (uuid, uuid, text, uuid),
app.initialize_workout_coverage (uuid, uuid, bytea, uuid, text, uuid),
app.set_workout_coverage_readiness (uuid, uuid, bigint, text, text, jsonb, uuid, text, uuid)
FROM
    PUBLIC,
    workouts_api,
    workouts_worker;

GRANT
EXECUTE ON FUNCTION app.initialize_workout_coverage (uuid, uuid, bytea, uuid, text, uuid),
app.set_workout_coverage_readiness (uuid, uuid, bigint, text, text, jsonb, uuid, text, uuid) TO workouts_worker;

CREATE TABLE app.coverage_job_migration_function_backup (function_name text PRIMARY KEY, definition text NOT NULL);

REVOKE ALL ON app.coverage_job_migration_function_backup
FROM
    PUBLIC,
    workouts_api,
    workouts_worker;

INSERT INTO
    app.coverage_job_migration_function_backup (function_name, definition)
SELECT
    procedure.proname,
    pg_get_functiondef(procedure.oid)
FROM
    pg_proc procedure
    JOIN pg_namespace namespace ON namespace.oid = procedure.pronamespace
WHERE
    namespace.nspname = 'app'
    AND procedure.proname IN (
        'enforce_job_hierarchy',
        'enforce_job_write',
        'derive_parent_status',
        'enforce_ingest_retry_lineage',
        'request_owned_job_cancellation',
        'read_owned_job_logs',
        'count_owned_job_rows'
    );

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.read_owned_job_logs (target_job_id uuid, requested_limit integer, requested_offset integer) RETURNS TABLE (
    total_count bigint,
    id bigint,
    job_id uuid,
    severity text,
    code text,
    message text,
    fields jsonb,
    created_at timestamptz
) LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
BEGIN
    IF requested_limit NOT BETWEEN 1 AND 100 OR requested_offset < 0 THEN
        RAISE EXCEPTION 'invalid pagination' USING ERRCODE = '22023';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM app.jobs owned_job WHERE owned_job.id = target_job_id
        AND owned_job.account_id = app.current_account_id()
        AND owned_job.kind IN ('manual_ingest', 'scheduled_ingest', 'manual_ingest_source', 'scheduled_ingest_source',
            'coverage_update', 'coverage_update_route')) THEN RETURN; END IF;
    RETURN QUERY SELECT count( * ) OVER(), log.id, log.job_id, log.severity, log.code,
      CASE WHEN log.code = 'coverage-route-start-context' THEN
        format('%s (%s): Coverage matching started.', workout.provider_label, to_char(workout.local_start_date, 'Mon FMDD, YYYY'))
        ELSE log.redacted_message END, log.fields, log.created_at
      FROM app.job_logs log JOIN app.jobs job ON job.id = log.job_id AND job.account_id = log.account_id
      LEFT JOIN app.coverage_route_job_contexts route ON route.job_id = job.id AND route.account_id = job.account_id
      LEFT JOIN app.workouts workout ON workout.id = route.workout_id AND workout.account_id = route.account_id
     WHERE log.account_id = app.current_account_id() AND (job.id = target_job_id OR job.parent_job_id = target_job_id)
       AND NOT ((log.code = 'coverage-route-started' AND EXISTS (
               SELECT 1 FROM app.job_logs contextual
               WHERE contextual.account_id = log.account_id AND contextual.job_id = log.job_id
                 AND contextual.code = 'coverage-route-start-context')) OR
           (log.code IN ('coverage-route-applied', 'coverage-route-no-evidence', 'coverage-route-failed',
           'coverage-route-superseded', 'coverage-route-cancelled') AND EXISTS (
               SELECT 1 FROM app.job_logs contextual
               WHERE contextual.account_id = log.account_id AND contextual.job_id = log.job_id
                 AND contextual.code = 'coverage-route-result-context')))
     ORDER BY log.created_at, log.id LIMIT requested_limit OFFSET requested_offset;
END;
$function$;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.count_owned_job_rows (target_job_id uuid, row_kind text) RETURNS bigint LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
DECLARE result bigint;
BEGIN
    IF row_kind = 'file' THEN
        SELECT count( * ) INTO result FROM app.source_files file JOIN app.jobs job
          ON job.id = file.job_id AND job.account_id = file.account_id
         WHERE file.account_id = app.current_account_id() AND (job.id = target_job_id OR job.parent_job_id = target_job_id);
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

-- Replace the anonymous schema-1 checks with named cumulative constraints.
-- +goose StatementBegin
DO $block$
DECLARE constraint_row record;
BEGIN
    FOR constraint_row IN
        SELECT constraint_value.conname
          FROM pg_constraint constraint_value
         WHERE constraint_value.conrelid = 'app.jobs'::regclass AND constraint_value.contype = 'c'
           AND (
               pg_get_constraintdef(constraint_value.oid) LIKE 'CHECK ((kind = ANY (%' OR
               pg_get_constraintdef(constraint_value.oid) LIKE 'CHECK (((parent_job_id IS NULL) = (kind <> ALL (%' OR
               pg_get_constraintdef(constraint_value.oid) LIKE 'CHECK (((status <> %partially_succeeded%' OR
               pg_get_constraintdef(constraint_value.oid) LIKE 'CHECK (((kind <> ALL (%manual_ingest%attempt = 0%' OR
               pg_get_constraintdef(constraint_value.oid) LIKE 'CHECK (((kind <> ALL (%manual_ingest%worker_id IS NULL%' OR
               pg_get_constraintdef(constraint_value.oid) LIKE 'CHECK ((((status = %running%kind <> ALL (%'
           )
    LOOP
        EXECUTE format('ALTER TABLE app.jobs DROP CONSTRAINT %I', constraint_row.conname);
    END LOOP;
END;
$block$;

-- +goose StatementEnd

ALTER TABLE app.jobs
ADD CONSTRAINT jobs_kind_v16_check CHECK (
    kind IN (
        'source_connection_check',
        'workout_deletion',
        'account_deletion',
        'manual_ingest',
        'manual_ingest_source',
        'scheduled_ingest',
        'scheduled_ingest_source',
        'osm_bootstrap',
        'osm_refresh',
        'coverage_update',
        'coverage_update_route'
    )
);

ALTER TABLE app.jobs
ADD CONSTRAINT jobs_parent_kind_v16_check CHECK (
    (parent_job_id IS NULL) = (kind NOT IN ('manual_ingest_source', 'scheduled_ingest_source', 'coverage_update_route'))
);

ALTER TABLE app.jobs
ADD CONSTRAINT jobs_partial_parent_v16_check CHECK (
    status <> 'partially_succeeded'
    OR kind IN ('manual_ingest', 'scheduled_ingest', 'coverage_update')
);

ALTER TABLE app.jobs
ADD CONSTRAINT jobs_parent_attempt_v16_check CHECK (
    kind NOT IN ('manual_ingest', 'scheduled_ingest', 'coverage_update')
    OR attempt = 0
);

ALTER TABLE app.jobs
ADD CONSTRAINT jobs_parent_lease_v16_check CHECK (
    kind NOT IN ('manual_ingest', 'scheduled_ingest', 'coverage_update')
    OR (
        worker_id IS NULL
        AND lease_token IS NULL
        AND claimed_at IS NULL
        AND heartbeat_at IS NULL
        AND lease_expires_at IS NULL
    )
);

ALTER TABLE app.jobs
ADD CONSTRAINT jobs_running_lease_v16_check CHECK (
    (
        status = 'running'
        AND kind NOT IN ('manual_ingest', 'scheduled_ingest', 'coverage_update')
    ) = (
        worker_id IS NOT NULL
        AND lease_token IS NOT NULL
        AND claimed_at IS NOT NULL
        AND heartbeat_at IS NOT NULL
        AND lease_expires_at IS NOT NULL
    )
);

CREATE TABLE app.coverage_job_contexts (
    job_id uuid PRIMARY KEY,
    account_id uuid NOT NULL,
    region_id text NOT NULL CHECK (region_id ~ '^[a-z][a-z0-9-]{0,63}:[a-z][a-z0-9-]{0,63}$'),
    target_osm_generation bigint NOT NULL CHECK (target_osm_generation > 0),
    target_work_revision bigint NOT NULL CHECK (target_work_revision > 0),
    rules_version text NOT NULL CHECK (rules_version = 'coverage-experimental-v1'),
    sampling_version text NOT NULL CHECK (sampling_version = 'coverage-sampling-experimental-v1'),
    path_policy_version text NOT NULL CHECK (path_policy_version ~ '^coverage-path-policy-experimental-v[1-9][0-9]*$'),
    minimum_traversal_meters double precision NOT NULL CHECK (minimum_traversal_meters BETWEEN 0.1 AND 100),
    last_child_claimed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    FOREIGN KEY (job_id, account_id) REFERENCES app.jobs (id, account_id) ON DELETE CASCADE
);

CREATE INDEX coverage_job_contexts_region_idx ON app.coverage_job_contexts (account_id, region_id, created_at DESC);

CREATE TABLE app.coverage_route_job_contexts (
    job_id uuid PRIMARY KEY,
    account_id uuid NOT NULL,
    workout_id uuid NOT NULL,
    route_input_revision bigint NOT NULL CHECK (route_input_revision > 0),
    route_input_sha256 bytea NOT NULL CHECK (octet_length(route_input_sha256) = 32),
    target_generations jsonb NOT NULL CHECK (
        jsonb_typeof(target_generations) = 'array'
        AND jsonb_array_length(target_generations) BETWEEN 1 AND 256
    ),
    timeout_retry_count integer NOT NULL DEFAULT 0 CHECK (timeout_retry_count BETWEEN 0 AND 16),
    result_outcome text CHECK (result_outcome IN ('applied', 'no_evidence', 'superseded')),
    duration_milliseconds integer CHECK (duration_milliseconds >= 0),
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    FOREIGN KEY (job_id, account_id) REFERENCES app.jobs (id, account_id) ON DELETE CASCADE,
    FOREIGN KEY (workout_id, account_id) REFERENCES app.workouts (id, account_id) ON DELETE CASCADE
);

CREATE INDEX coverage_route_job_contexts_workout_idx ON app.coverage_route_job_contexts (account_id, workout_id, created_at DESC);

-- +goose StatementBegin
CREATE FUNCTION app.coverage_route_timeout_retry_count (target_job_id uuid, claiming_worker text, current_lease_token uuid) RETURNS integer LANGUAGE sql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
    SELECT context.timeout_retry_count FROM app.jobs job
    JOIN app.coverage_route_job_contexts context ON context.job_id = job.id AND context.account_id = job.account_id
    WHERE job.id = target_job_id AND job.account_id = app.current_account_id() AND job.kind = 'coverage_update_route'
      AND job.status = 'running' AND job.worker_id = claiming_worker AND job.lease_token = current_lease_token
      AND job.lease_expires_at >= clock_timestamp() AND job.cancel_requested_at IS NULL
$function$;

-- +goose StatementEnd

CREATE TABLE app.coverage_job_progress (
    job_id uuid PRIMARY KEY,
    account_id uuid NOT NULL,
    routes_total integer NOT NULL CHECK (routes_total >= 0),
    routes_processed integer NOT NULL DEFAULT 0 CHECK (routes_processed >= 0),
    routes_succeeded integer NOT NULL DEFAULT 0 CHECK (routes_succeeded >= 0),
    routes_failed integer NOT NULL DEFAULT 0 CHECK (routes_failed >= 0),
    routes_cancelled integer NOT NULL DEFAULT 0 CHECK (routes_cancelled >= 0),
    routes_superseded integer NOT NULL DEFAULT 0 CHECK (routes_superseded >= 0),
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    FOREIGN KEY (job_id, account_id) REFERENCES app.jobs (id, account_id) ON DELETE CASCADE,
    CHECK (routes_processed = routes_succeeded + routes_failed + routes_superseded),
    CHECK (routes_processed + routes_cancelled <= routes_total)
);

ALTER TABLE app.workout_coverage_states
DROP CONSTRAINT workout_coverage_states_processing_state_check;

ALTER TABLE app.workout_coverage_states
ADD CONSTRAINT workout_coverage_states_processing_state_check CHECK (processing_state IN ('not_started', 'queued', 'running', 'current', 'failed', 'stale'));

ALTER TABLE app.workout_coverage_states
ADD COLUMN target_job_id uuid,
ADD COLUMN target_revision bigint CHECK (target_revision > 0),
ADD COLUMN target_rules_version text,
ADD COLUMN target_sampling_version text,
ADD COLUMN target_path_policy_version text,
ADD COLUMN target_generations jsonb,
ADD COLUMN applied_route_input_revision bigint CHECK (applied_route_input_revision > 0),
ADD COLUMN applied_route_input_sha256 bytea CHECK (
    applied_route_input_sha256 IS NULL
    OR octet_length(applied_route_input_sha256) = 32
),
ADD COLUMN applied_rules_version text,
ADD COLUMN applied_sampling_version text,
ADD COLUMN applied_path_policy_version text,
ADD COLUMN applied_generations jsonb,
ADD COLUMN processing_failure_code text CHECK (
    processing_failure_code IS NULL
    OR length(processing_failure_code) <= 64
),
ADD COLUMN processing_failure_summary text CHECK (
    processing_failure_summary IS NULL
    OR length(processing_failure_summary) <= 512
),
ADD COLUMN processing_started_at timestamptz,
ADD COLUMN processing_finished_at timestamptz,
ADD FOREIGN KEY (target_job_id, account_id) REFERENCES app.jobs (id, account_id) ON DELETE SET NULL (target_job_id),
ADD CHECK (
    (target_generations IS NULL)
    OR (
        jsonb_typeof(target_generations) = 'array'
        AND jsonb_array_length(target_generations) BETWEEN 1 AND 256
    )
),
ADD CHECK (
    (applied_generations IS NULL)
    OR jsonb_typeof(applied_generations) = 'array'
);

CREATE TABLE app.matcher_slot_guard (singleton boolean PRIMARY KEY DEFAULT TRUE CHECK (singleton));

INSERT INTO
    app.matcher_slot_guard (singleton)
VALUES
    (TRUE);

CREATE TABLE app.matcher_slot_limits (
    singleton boolean PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    global_limit integer NOT NULL CHECK (global_limit BETWEEN 1 AND 16),
    account_limit integer NOT NULL CHECK (
        account_limit BETWEEN 1 AND 16
        AND account_limit <= global_limit
    ),
    region_limit integer NOT NULL CHECK (
        region_limit BETWEEN 1 AND 16
        AND region_limit <= global_limit
    ),
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp()
);

INSERT INTO
    app.matcher_slot_limits (singleton, global_limit, account_limit, region_limit)
VALUES
    (TRUE, 1, 1, 1);

CREATE TABLE app.matcher_slots (
    slot_token uuid PRIMARY KEY,
    slot_kind text NOT NULL CHECK (slot_kind IN ('production', 'diagnostic')),
    account_id uuid NOT NULL REFERENCES app.accounts (id) ON DELETE CASCADE,
    job_id uuid,
    owner_id text NOT NULL CHECK (length(owner_id) BETWEEN 1 AND 200),
    lease_token uuid NOT NULL,
    region_ids TEXT[] NOT NULL CHECK (cardinality(region_ids) BETWEEN 0 AND 256),
    acquired_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    expires_at timestamptz NOT NULL,
    FOREIGN KEY (job_id, account_id) REFERENCES app.jobs (id, account_id) ON DELETE CASCADE,
    CHECK ((slot_kind = 'production') = (job_id IS NOT NULL))
);

CREATE INDEX matcher_slots_account_idx ON app.matcher_slots (account_id);

CREATE INDEX matcher_slots_job_idx ON app.matcher_slots (job_id)
WHERE
    job_id IS NOT NULL;

ALTER TABLE app.coverage_job_contexts ENABLE ROW LEVEL SECURITY;

ALTER TABLE app.coverage_job_contexts FORCE ROW LEVEL SECURITY;

ALTER TABLE app.coverage_route_job_contexts ENABLE ROW LEVEL SECURITY;

ALTER TABLE app.coverage_route_job_contexts FORCE ROW LEVEL SECURITY;

ALTER TABLE app.coverage_job_progress ENABLE ROW LEVEL SECURITY;

ALTER TABLE app.coverage_job_progress FORCE ROW LEVEL SECURITY;

CREATE POLICY coverage_job_contexts_account_policy ON app.coverage_job_contexts USING (account_id = app.current_account_id ())
WITH
    CHECK (account_id = app.current_account_id ());

CREATE POLICY coverage_job_contexts_owner_policy ON app.coverage_job_contexts TO workouts_security_owner USING (TRUE)
WITH
    CHECK (TRUE);

CREATE POLICY coverage_route_job_contexts_account_policy ON app.coverage_route_job_contexts USING (account_id = app.current_account_id ())
WITH
    CHECK (account_id = app.current_account_id ());

CREATE POLICY coverage_route_job_contexts_owner_policy ON app.coverage_route_job_contexts TO workouts_security_owner USING (TRUE)
WITH
    CHECK (TRUE);

CREATE POLICY coverage_job_progress_account_policy ON app.coverage_job_progress USING (account_id = app.current_account_id ())
WITH
    CHECK (account_id = app.current_account_id ());

CREATE POLICY coverage_job_progress_owner_policy ON app.coverage_job_progress TO workouts_security_owner USING (TRUE)
WITH
    CHECK (TRUE);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.enforce_job_hierarchy () RETURNS trigger LANGUAGE plpgsql
SET
    search_path = pg_catalog,
    app AS $function$
DECLARE parent app.jobs%ROWTYPE;
BEGIN
    IF NEW.parent_job_id IS NULL THEN RETURN NEW; END IF;
    SELECT * INTO parent FROM app.jobs WHERE id = NEW.parent_job_id;
    IF NOT FOUND OR parent.account_id IS DISTINCT FROM NEW.account_id OR NEW.administrator_id IS NOT NULL OR
       (parent.kind = 'manual_ingest' AND NEW.kind <> 'manual_ingest_source') OR
       (parent.kind = 'scheduled_ingest' AND NEW.kind <> 'scheduled_ingest_source') OR
       (parent.kind = 'coverage_update' AND NEW.kind <> 'coverage_update_route') OR
       parent.kind NOT IN ('manual_ingest', 'scheduled_ingest', 'coverage_update') THEN
        RAISE EXCEPTION 'invalid job hierarchy' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$function$;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.enforce_job_write () RETURNS trigger LANGUAGE plpgsql
SET
    search_path = pg_catalog AS $function$
DECLARE transition text := COALESCE(current_setting('app.job_transition', true), '');
BEGIN
    IF TG_OP = 'INSERT' THEN
        IF NEW.status <> 'queued' OR NEW.attempt <> 0 OR NEW.terminal_at IS NOT NULL OR NEW.cancel_requested_at IS NOT NULL OR
           NEW.worker_id IS NOT NULL OR NEW.lease_token IS NOT NULL OR NEW.claimed_at IS NOT NULL OR
           NEW.heartbeat_at IS NOT NULL OR NEW.lease_expires_at IS NOT NULL THEN
            RAISE EXCEPTION 'jobs must be inserted in a clean queued state' USING ERRCODE = '23514';
        END IF;
        RETURN NEW;
    END IF;
    IF OLD.status IN ('succeeded', 'partially_succeeded', 'failed', 'cancelled') THEN
        RAISE EXCEPTION 'terminal jobs are immutable' USING ERRCODE = '23514';
    END IF;
    IF OLD.id <> NEW.id OR OLD.kind <> NEW.kind OR OLD.account_id IS DISTINCT FROM NEW.account_id OR
       OLD.administrator_id IS DISTINCT FROM NEW.administrator_id OR OLD.parent_job_id IS DISTINCT FROM NEW.parent_job_id OR
       OLD.parameters <> NEW.parameters OR OLD.coalescing_key IS DISTINCT FROM NEW.coalescing_key OR
       OLD.coalescing_scope IS DISTINCT FROM NEW.coalescing_scope OR OLD.coalescing_version IS DISTINCT FROM NEW.coalescing_version OR
       OLD.retry_of_job_id IS DISTINCT FROM NEW.retry_of_job_id THEN
        RAISE EXCEPTION 'immutable job fields changed' USING ERRCODE = '23514';
    END IF;
    IF NEW.kind IN ('manual_ingest', 'scheduled_ingest', 'coverage_update') THEN
        IF transition NOT IN ('derive_parent', 'cancel') THEN
            RAISE EXCEPTION 'parent jobs are changed only by state-machine functions' USING ERRCODE = '23514';
        END IF;
    ELSE
        IF NEW.status IS DISTINCT FROM OLD.status AND NOT (
            (transition = 'claim' AND OLD.status = 'queued' AND NEW.status = 'running') OR
            (transition = 'supersede' AND OLD.status = 'queued' AND NEW.status = 'succeeded') OR
            (transition = 'recover' AND OLD.status = 'running' AND NEW.status IN ('queued', 'cancelled')) OR
            (transition = 'finish' AND OLD.status = 'running' AND NEW.status IN ('succeeded', 'failed', 'cancelled')) OR
            (transition = 'cancel' AND OLD.status = 'queued' AND NEW.status = 'cancelled')) THEN
            RAISE EXCEPTION 'invalid job status transition' USING ERRCODE = '23514';
        END IF;
        IF NEW.attempt <> OLD.attempt AND NOT (transition = 'claim' AND NEW.attempt = OLD.attempt + 1) THEN
            RAISE EXCEPTION 'attempt changes only when a queued job is claimed' USING ERRCODE = '23514';
        END IF;
        IF (NEW.worker_id, NEW.lease_token, NEW.claimed_at, NEW.heartbeat_at, NEW.lease_expires_at) IS DISTINCT FROM
           (OLD.worker_id, OLD.lease_token, OLD.claimed_at, OLD.heartbeat_at, OLD.lease_expires_at) AND
           transition NOT IN ('claim', 'heartbeat', 'recover', 'finish') THEN
            RAISE EXCEPTION 'lease fields change only through lease functions' USING ERRCODE = '23514';
        END IF;
    END IF;
    IF (NEW.cancel_requested_at, NEW.cancel_requested_by) IS DISTINCT FROM
       (OLD.cancel_requested_at, OLD.cancel_requested_by) AND transition <> 'cancel' THEN
        RAISE EXCEPTION 'cancellation intent changes only through cancellation functions' USING ERRCODE = '23514';
    END IF;
    NEW.updated_at := now();
    RETURN NEW;
END;
$function$;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.derive_parent_status (parent_id uuid) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
DECLARE derived_status text; parent_kind text; child_count integer; queued_count integer; running_count integer;
DECLARE succeeded_count integer; failed_count integer; cancelled_count integer; superseded_count integer;
BEGIN
    SELECT kind INTO parent_kind FROM app.jobs WHERE id = parent_id AND account_id = app.current_account_id() FOR UPDATE;
    IF NOT FOUND OR parent_kind NOT IN ('manual_ingest', 'scheduled_ingest', 'coverage_update') THEN RETURN; END IF;
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
    END IF;
    derived_status := CASE
        WHEN queued_count = child_count THEN 'queued'
        WHEN running_count > 0 OR (queued_count > 0 AND queued_count < child_count) THEN 'running'
        WHEN succeeded_count = child_count THEN 'succeeded'
        WHEN succeeded_count > 0 AND failed_count + cancelled_count > 0 THEN 'partially_succeeded'
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
CREATE OR REPLACE FUNCTION app.enforce_ingest_retry_lineage () RETURNS trigger LANGUAGE plpgsql
SET
    search_path = pg_catalog,
    app AS $function$
DECLARE prior_kind text; prior_account uuid;
BEGIN
    IF NEW.retry_of_job_id IS NULL THEN RETURN NEW; END IF;
    SELECT kind, account_id INTO prior_kind, prior_account FROM app.jobs WHERE id = NEW.retry_of_job_id;
    IF NOT FOUND OR prior_account IS DISTINCT FROM NEW.account_id OR
       (NEW.kind IN ('manual_ingest', 'scheduled_ingest') AND prior_kind NOT IN ('manual_ingest', 'scheduled_ingest')) OR
       (NEW.kind IN ('manual_ingest_source', 'scheduled_ingest_source') AND prior_kind NOT IN ('manual_ingest_source', 'scheduled_ingest_source')) OR
       (NEW.kind = 'coverage_update' AND prior_kind <> 'coverage_update') OR
       (NEW.kind = 'coverage_update_route' AND prior_kind <> 'coverage_update_route') OR
       (NEW.kind NOT IN ('manual_ingest', 'scheduled_ingest', 'manual_ingest_source', 'scheduled_ingest_source', 'coverage_update', 'coverage_update_route') AND prior_kind <> NEW.kind) THEN
        RAISE EXCEPTION 'invalid job retry lineage' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$function$;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.request_coverage_job_cancellation (job_id uuid, requester_id uuid) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
DECLARE target_status text; child_to_lock uuid;
BEGIN
    IF requester_id IS NULL THEN RAISE EXCEPTION 'requester is required' USING ERRCODE = '22023'; END IF;
    SELECT status INTO target_status FROM app.jobs
     WHERE id = job_id AND account_id = app.current_account_id() AND kind = 'coverage_update' FOR UPDATE;
    IF NOT FOUND OR target_status IN ('succeeded', 'partially_succeeded', 'failed', 'cancelled') THEN RETURN false; END IF;
    FOR child_to_lock IN SELECT child.id FROM app.jobs child
      WHERE child.parent_job_id = job_id AND child.account_id = app.current_account_id()
        AND child.status IN ('queued', 'running') ORDER BY child.id
    LOOP
        PERFORM 1 FROM app.jobs child WHERE child.id = child_to_lock AND child.account_id = app.current_account_id() FOR UPDATE;
    END LOOP;
    PERFORM set_config('app.job_transition', 'cancel', true);
    UPDATE app.jobs parent SET cancel_requested_at = COALESCE(parent.cancel_requested_at, transaction_timestamp()),
        cancel_requested_by = COALESCE(parent.cancel_requested_by, requester_id)
     WHERE parent.id = request_coverage_job_cancellation.job_id AND parent.account_id = app.current_account_id();
    UPDATE app.jobs child SET status = 'cancelled', terminal_at = transaction_timestamp(),
        cancel_requested_at = transaction_timestamp(), cancel_requested_by = requester_id
     WHERE child.parent_job_id = request_coverage_job_cancellation.job_id AND child.account_id = app.current_account_id()
       AND child.status = 'queued';
    UPDATE app.jobs child SET cancel_requested_at = COALESCE(child.cancel_requested_at, transaction_timestamp()),
        cancel_requested_by = COALESCE(child.cancel_requested_by, requester_id)
     WHERE child.parent_job_id = request_coverage_job_cancellation.job_id AND child.account_id = app.current_account_id()
       AND child.status = 'running';
    PERFORM app.derive_parent_status(job_id);
    RETURN true;
END;
$function$;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.request_owned_job_cancellation (target_job_id uuid, requester_id uuid) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
DECLARE target_kind text;
BEGIN
    IF requester_id IS NULL OR NOT EXISTS (
        SELECT 1 FROM app.users account_user WHERE account_user.principal_id = requester_id
          AND account_user.account_id = app.current_account_id()
    ) THEN RETURN false; END IF;
    SELECT job.kind INTO target_kind FROM app.jobs job
     WHERE job.id = target_job_id AND job.account_id = app.current_account_id();
    IF target_kind = 'coverage_update' THEN
        RETURN app.request_coverage_job_cancellation(target_job_id, requester_id);
    END IF;
    RETURN app.request_job_cancellation(target_job_id, requester_id);
END;
$function$;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.sync_coverage_route_job_state () RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
BEGIN
    IF NEW.kind <> 'coverage_update_route' OR NEW.status = OLD.status THEN RETURN NEW; END IF;
    IF NEW.status = 'queued' THEN
        UPDATE app.workout_coverage_states state SET processing_state = 'queued', updated_at = transaction_timestamp()
         WHERE state.account_id = NEW.account_id AND state.target_job_id = NEW.id;
    ELSIF NEW.status = 'cancelled' THEN
        UPDATE app.workout_coverage_states state SET
            processing_state = CASE WHEN state.applied_route_input_revision IS NULL THEN 'not_started' ELSE 'stale' END,
            processing_finished_at = transaction_timestamp(), updated_at = transaction_timestamp()
         WHERE state.account_id = NEW.account_id AND state.target_job_id = NEW.id;
    END IF;
    RETURN NEW;
END;
$function$;

-- +goose StatementEnd

CREATE TRIGGER jobs_coverage_state_after_status
AFTER UPDATE OF status ON app.jobs FOR EACH ROW
EXECUTE FUNCTION app.sync_coverage_route_job_state ();

-- +goose StatementBegin
CREATE FUNCTION app.sync_coverage_matcher_slot_lease () RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
BEGIN
    IF NEW.kind = 'coverage_update_route' AND NEW.status = 'running' AND NEW.lease_expires_at IS DISTINCT FROM OLD.lease_expires_at THEN
        UPDATE app.matcher_slots slot SET expires_at = NEW.lease_expires_at
         WHERE slot.slot_kind = 'production' AND slot.job_id = NEW.id AND slot.account_id = NEW.account_id
           AND slot.owner_id = NEW.worker_id AND slot.lease_token = NEW.lease_token;
    END IF;
    RETURN NEW;
END;
$function$;

-- +goose StatementEnd

CREATE TRIGGER jobs_coverage_matcher_slot_lease_after_heartbeat
AFTER UPDATE OF lease_expires_at ON app.jobs FOR EACH ROW
EXECUTE FUNCTION app.sync_coverage_matcher_slot_lease ();

-- Explicit route targets keep private coordinates out of job parameters.
-- +goose StatementBegin
CREATE FUNCTION app.enqueue_coverage_update (
    target_account_id uuid,
    new_parent_id uuid,
    target_region_id text,
    target_osm_generation bigint,
    target_work_revision bigint,
    target_rules text,
    target_sampling text,
    target_policy text,
    target_minimum double precision,
    route_targets jsonb
) RETURNS TABLE (job_id uuid, route_count integer, reused boolean) LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
DECLARE target jsonb; child_id uuid; child_count integer := 0; existing_job uuid; actual_parent uuid; generations jsonb;
BEGIN
    IF target_account_id <> app.current_account_id() OR new_parent_id IS NULL OR
       target_region_id !~ '^[a-z][a-z0-9-]{0,63}:[a-z][a-z0-9-]{0,63}$' OR target_osm_generation < 1 OR
       target_work_revision < 1 OR target_rules <> 'coverage-experimental-v1' OR
       target_sampling <> 'coverage-sampling-experimental-v1' OR
       target_policy !~ '^coverage-path-policy-experimental-v[1-9][0-9]*$' OR
       target_minimum NOT BETWEEN 0.1 AND 100 OR jsonb_typeof(route_targets) <> 'array' OR
       jsonb_array_length(route_targets) NOT BETWEEN 1 AND 1000 THEN
        RAISE EXCEPTION 'invalid coverage update target' USING ERRCODE = '22023';
    END IF;
    PERFORM 1 FROM app.accounts WHERE id = target_account_id AND state = 'active' FOR UPDATE;
    IF NOT FOUND THEN RETURN; END IF;
    SELECT context.job_id INTO existing_job FROM app.coverage_job_contexts context
      JOIN app.jobs job ON job.id = context.job_id AND job.account_id = context.account_id
     WHERE context.account_id = target_account_id AND context.region_id = target_region_id
       AND context.rules_version = target_rules AND context.sampling_version = target_sampling
       AND context.path_policy_version = target_policy AND context.minimum_traversal_meters = target_minimum
       AND job.status = 'queued' ORDER BY job.created_at LIMIT 1 FOR UPDATE OF job;
    IF FOUND THEN
        actual_parent := existing_job; reused := true;
        UPDATE app.coverage_job_contexts SET target_osm_generation = GREATEST(coverage_job_contexts.target_osm_generation, enqueue_coverage_update.target_osm_generation),
            target_work_revision = GREATEST(coverage_job_contexts.target_work_revision, enqueue_coverage_update.target_work_revision)
         WHERE coverage_job_contexts.job_id = actual_parent;
    ELSE
        actual_parent := new_parent_id; reused := false;
        INSERT INTO app.jobs(id, account_id, kind, priority, parameters, progress_total)
        VALUES(actual_parent, target_account_id, 'coverage_update', 20, jsonb_build_object('regionId', target_region_id), jsonb_array_length(route_targets));
        INSERT INTO app.coverage_job_contexts(job_id, account_id, region_id, target_osm_generation, target_work_revision,
            rules_version, sampling_version, path_policy_version, minimum_traversal_meters)
        VALUES(actual_parent, target_account_id, target_region_id, target_osm_generation, target_work_revision,
            target_rules, target_sampling, target_policy, target_minimum);
    END IF;
    FOR target IN SELECT value FROM jsonb_array_elements(route_targets) LOOP
        IF jsonb_typeof(target) <> 'object' OR (SELECT count( * ) FROM jsonb_object_keys(target)) <> 4 OR
           NOT (target ?& ARRAY['jobId', 'workoutId', 'routeRevision', 'generations']) OR
           target->> 'jobId' !~ '^[0-9a-fA-F-]{36}$' OR target->> 'workoutId' !~ '^[0-9a-fA-F-]{36}$' OR
           target->> 'routeRevision' !~ '^[1-9][0-9]*$' OR jsonb_typeof(target->'generations') <> 'array' OR
           jsonb_array_length(target->'generations') NOT BETWEEN 1 AND 256 THEN
            RAISE EXCEPTION 'invalid coverage route target' USING ERRCODE = '22023';
        END IF;
        child_id := (target->> 'jobId')::uuid; generations := target->'generations';
        IF EXISTS (SELECT 1 FROM app.jobs child JOIN app.coverage_route_job_contexts context
            ON context.job_id = child.id AND context.account_id = child.account_id
            WHERE child.parent_job_id = actual_parent AND child.account_id = target_account_id
              AND context.workout_id = (target->> 'workoutId')::uuid
              AND context.route_input_revision = (target->> 'routeRevision')::bigint
              AND context.target_generations = generations AND child.status = 'queued') THEN
            CONTINUE;
        END IF;
        UPDATE app.coverage_route_job_contexts context SET result_outcome = 'superseded'
         FROM app.jobs child WHERE child.id = context.job_id AND child.account_id = context.account_id
           AND child.parent_job_id = actual_parent AND child.account_id = target_account_id
           AND context.workout_id = (target->> 'workoutId')::uuid AND child.status = 'queued';
        PERFORM set_config('app.job_transition', 'supersede', true);
        UPDATE app.jobs child SET status = 'succeeded', terminal_at = transaction_timestamp()
         FROM app.coverage_route_job_contexts context WHERE context.job_id = child.id AND context.account_id = child.account_id
           AND child.parent_job_id = actual_parent AND child.account_id = target_account_id
           AND context.workout_id = (target->> 'workoutId')::uuid AND child.status = 'queued';
        INSERT INTO app.jobs(id, parent_job_id, account_id, kind, priority, parameters)
        VALUES(child_id, actual_parent, target_account_id, 'coverage_update_route', 20, '{}');
        INSERT INTO app.coverage_route_job_contexts(job_id, account_id, workout_id, route_input_revision, route_input_sha256, target_generations)
        SELECT child_id, target_account_id, state.workout_id, state.route_input_revision, state.route_input_sha256, generations
          FROM app.workout_coverage_states state
         WHERE state.account_id = target_account_id AND state.workout_id = (target->> 'workoutId')::uuid
           AND state.route_input_revision = (target->> 'routeRevision')::bigint AND state.readiness_state = 'map_data_ready';
        IF NOT FOUND THEN RAISE EXCEPTION 'coverage route target is stale' USING ERRCODE = '40001'; END IF;
        UPDATE app.workout_coverage_states SET processing_state = 'queued', target_job_id = child_id,
            target_revision = route_input_revision, target_rules_version = target_rules,
            target_sampling_version = target_sampling, target_path_policy_version = target_policy, target_generations = generations,
            processing_failure_code = NULL, processing_failure_summary = NULL, processing_started_at = NULL, processing_finished_at = NULL,
            updated_at = transaction_timestamp()
         WHERE account_id = target_account_id AND workout_id = (target->> 'workoutId')::uuid;
        child_count := child_count + 1;
    END LOOP;
    SELECT count( * )::integer INTO child_count FROM app.jobs WHERE parent_job_id = actual_parent AND account_id = target_account_id;
    INSERT INTO app.coverage_job_progress(job_id, account_id, routes_total) VALUES(actual_parent, target_account_id, child_count)
    ON CONFLICT ON CONSTRAINT coverage_job_progress_pkey DO UPDATE
       SET routes_total = EXCLUDED.routes_total, updated_at = transaction_timestamp();
    PERFORM set_config('app.job_transition', 'derive_parent', true);
    UPDATE app.jobs SET progress_total = child_count WHERE id = actual_parent AND account_id = target_account_id;
    job_id := actual_parent; route_count := child_count; RETURN NEXT;
END;
$function$;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.claim_next_coverage_route (claiming_worker text, new_lease_token uuid, lease_duration interval, runtime_version integer) RETURNS TABLE (
    job_id uuid,
    account_id uuid,
    parent_job_id uuid,
    workout_id uuid,
    route_input_revision bigint,
    route_input_sha256 bytea,
    rules_version text,
    sampling_version text,
    path_policy_version text,
    minimum_traversal_meters double precision,
    target_generations jsonb
) LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
DECLARE candidate record; candidate_status text; attempted uuid[] := ARRAY[]::uuid[];
BEGIN
    IF runtime_version < 15 THEN RAISE EXCEPTION 'coverage worker runtime version 15 or newer is required' USING ERRCODE = '55000'; END IF;
    IF claiming_worker = '' OR new_lease_token IS NULL OR lease_duration < interval '1 second' OR lease_duration > interval '15 minutes' THEN
        RAISE EXCEPTION 'invalid coverage claim arguments' USING ERRCODE = '22023';
    END IF;
    LOOP
        SELECT child.id, child.account_id, child.parent_job_id, route.workout_id, route.route_input_revision, route.route_input_sha256,
               parent_context.rules_version, parent_context.sampling_version, parent_context.path_policy_version,
               parent_context.minimum_traversal_meters, route.target_generations, child.status,
               workout.local_start_date, workout.provider_label
          INTO candidate
          FROM app.jobs child
          JOIN app.jobs parent ON parent.id = child.parent_job_id AND parent.account_id = child.account_id
          JOIN app.coverage_job_contexts parent_context ON parent_context.job_id = parent.id AND parent_context.account_id = parent.account_id
          JOIN app.coverage_route_job_contexts route ON route.job_id = child.id AND route.account_id = child.account_id
          JOIN app.workouts workout ON workout.id = route.workout_id AND workout.account_id = route.account_id
         WHERE child.kind = 'coverage_update_route' AND parent.kind = 'coverage_update'
           AND ((child.status = 'queued' AND child.cancel_requested_at IS NULL) OR
                (child.status = 'running' AND child.lease_expires_at < transaction_timestamp()))
           AND NOT child.id = ANY(attempted)
         ORDER BY parent_context.last_child_claimed_at NULLS FIRST, parent.created_at, child.created_at, child.id LIMIT 1;
        IF NOT FOUND THEN RETURN; END IF;
        attempted := array_append(attempted, candidate.id);
        PERFORM set_config('app.account_id', candidate.account_id::text, true);
        SELECT child.status INTO candidate_status FROM app.jobs child
          JOIN app.jobs parent ON parent.id = child.parent_job_id AND parent.account_id = child.account_id
         WHERE child.id = candidate.id AND child.account_id = candidate.account_id AND parent.status IN ('queued', 'running')
           AND ((child.status = 'queued' AND child.cancel_requested_at IS NULL) OR
                (child.status = 'running' AND child.lease_expires_at < transaction_timestamp()))
         FOR UPDATE OF parent, child SKIP LOCKED;
        IF NOT FOUND THEN CONTINUE; END IF;
        IF candidate_status = 'running' AND NOT app.recover_expired_job(candidate.id) THEN CONTINUE; END IF;
        IF NOT app.claim_job(candidate.id, claiming_worker, new_lease_token, lease_duration) THEN CONTINUE; END IF;
        UPDATE app.coverage_job_contexts parent_context SET last_child_claimed_at = clock_timestamp()
         WHERE parent_context.job_id = candidate.parent_job_id;
        UPDATE app.workout_coverage_states state SET processing_state = 'running',
            processing_started_at = COALESCE(state.processing_started_at, transaction_timestamp()), updated_at = transaction_timestamp()
         WHERE state.account_id = candidate.account_id AND state.workout_id = candidate.workout_id AND state.target_job_id = candidate.id;
        INSERT INTO app.job_events(account_id, job_id, severity, code, safe_message, fields)
        VALUES(candidate.account_id, candidate.id, 'info', 'coverage-route-started', 'Coverage route matching started.', '{}');
        INSERT INTO app.job_logs(account_id, job_id, severity, code, redacted_message, fields)
        VALUES(candidate.account_id, candidate.id, 'info', 'coverage-route-started',
            format('%s (%s): Coverage matching started.', candidate.provider_label, to_char(candidate.local_start_date, 'Mon FMDD, YYYY')), '{}');
        job_id := candidate.id; account_id := candidate.account_id; parent_job_id := candidate.parent_job_id;
        workout_id := candidate.workout_id; route_input_revision := candidate.route_input_revision;
        route_input_sha256 := candidate.route_input_sha256; rules_version := candidate.rules_version;
        sampling_version := candidate.sampling_version; path_policy_version := candidate.path_policy_version;
        minimum_traversal_meters := candidate.minimum_traversal_meters; target_generations := candidate.target_generations;
        RETURN NEXT; RETURN;
    END LOOP;
END;
$function$;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.finish_coverage_route (
    target_job_id uuid,
    claiming_worker text,
    current_lease_token uuid,
    result_outcome text,
    duration_milliseconds integer,
    failure_code_value text DEFAULT NULL,
    failure_summary_value text DEFAULT NULL
) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
DECLARE target record; terminal_status text;
DECLARE diagnostic_severity text; diagnostic_code text; diagnostic_message text;
BEGIN
    IF result_outcome NOT IN ('applied', 'no_evidence', 'superseded', 'failed') OR duration_milliseconds < 0 OR
       (result_outcome = 'failed') <> (failure_code_value IS NOT NULL) THEN
        RAISE EXCEPTION 'invalid coverage result' USING ERRCODE = '22023';
    END IF;
    SELECT job.account_id, context.workout_id, context.route_input_revision, context.route_input_sha256,
           parent_context.rules_version, parent_context.sampling_version, parent_context.path_policy_version, context.target_generations,
           workout.local_start_date, workout.provider_label
      INTO target FROM app.jobs job
      JOIN app.coverage_route_job_contexts context ON context.job_id = job.id AND context.account_id = job.account_id
      JOIN app.coverage_job_contexts parent_context ON parent_context.job_id = job.parent_job_id AND parent_context.account_id = job.account_id
      JOIN app.workouts workout ON workout.id = context.workout_id AND workout.account_id = context.account_id
     WHERE job.id = finish_coverage_route.target_job_id AND job.account_id = app.current_account_id() AND job.kind = 'coverage_update_route'
       AND job.status = 'running' AND job.worker_id = claiming_worker AND job.lease_token = current_lease_token
       AND job.lease_expires_at >= clock_timestamp() FOR UPDATE OF job, context;
    IF NOT FOUND THEN RETURN false; END IF;
    IF result_outcome IN ('applied', 'no_evidence') AND NOT EXISTS (
        SELECT 1 FROM app.workout_coverage_states state WHERE state.account_id = target.account_id AND state.workout_id = target.workout_id
          AND state.route_input_revision = target.route_input_revision AND state.route_input_sha256 = target.route_input_sha256
          AND state.target_job_id = finish_coverage_route.target_job_id
    ) THEN result_outcome := 'superseded'; END IF;
    UPDATE app.coverage_route_job_contexts context SET result_outcome = NULLIF(finish_coverage_route.result_outcome, 'failed'),
        duration_milliseconds = finish_coverage_route.duration_milliseconds
     WHERE context.job_id = finish_coverage_route.target_job_id;
    IF result_outcome IN ('applied', 'no_evidence') THEN
        UPDATE app.workout_coverage_states state SET processing_state = 'current', applied_route_input_revision = target.route_input_revision,
            applied_route_input_sha256 = target.route_input_sha256, applied_rules_version = target.rules_version,
            applied_sampling_version = target.sampling_version, applied_path_policy_version = target.path_policy_version,
            applied_generations = target.target_generations, processing_failure_code = NULL, processing_failure_summary = NULL,
            processing_finished_at = transaction_timestamp(), updated_at = transaction_timestamp()
         WHERE state.account_id = target.account_id AND state.workout_id = target.workout_id
           AND state.target_job_id = finish_coverage_route.target_job_id;
    ELSIF result_outcome = 'superseded' THEN
        UPDATE app.workout_coverage_states state SET processing_state = 'stale', processing_finished_at = transaction_timestamp(), updated_at = transaction_timestamp()
         WHERE state.account_id = target.account_id AND state.workout_id = target.workout_id
           AND state.target_job_id = finish_coverage_route.target_job_id;
    ELSE
        UPDATE app.workout_coverage_states state SET processing_state = 'failed', processing_failure_code = failure_code_value,
            processing_failure_summary = failure_summary_value, processing_finished_at = transaction_timestamp(), updated_at = transaction_timestamp()
         WHERE state.account_id = target.account_id AND state.workout_id = target.workout_id
           AND state.target_job_id = finish_coverage_route.target_job_id;
    END IF;
    terminal_status := CASE WHEN result_outcome = 'failed' THEN 'failed' ELSE 'succeeded' END;
    SELECT severity, code, message INTO diagnostic_severity, diagnostic_code, diagnostic_message FROM (VALUES
        ('applied', 'info', 'coverage-route-applied', 'Coverage matching produced route attribution.'),
        ('no_evidence', 'info', 'coverage-route-no-evidence', 'Coverage matching completed without attributable paths.'),
        ('superseded', 'warning', 'coverage-route-superseded', 'Coverage matching was superseded.'),
        ('failed', 'error', 'coverage-route-failed', 'Coverage matching failed.')
    ) diagnostic(outcome, severity, code, message) WHERE outcome = result_outcome;
    INSERT INTO app.job_events(account_id, job_id, severity, code, safe_message, fields)
    VALUES(target.account_id, target_job_id, diagnostic_severity, diagnostic_code, diagnostic_message, '{}');
    INSERT INTO app.job_logs(account_id, job_id, severity, code, redacted_message, fields)
    VALUES(target.account_id, target_job_id, diagnostic_severity, diagnostic_code,
        format('%s (%s): %s after %s ms.', target.provider_label, to_char(target.local_start_date, 'Mon FMDD, YYYY'),
            rtrim(diagnostic_message, '.'), to_char(duration_milliseconds, 'FM999G999G999G990')), '{}');
    RETURN app.finish_job(finish_coverage_route.target_job_id, claiming_worker, current_lease_token, terminal_status, failure_code_value, failure_summary_value);
END;
$function$;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.retry_coverage_update (prior_job_id uuid, requester_id uuid, max_ordinal integer) RETURNS TABLE (job_id uuid, route_count integer) LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
DECLARE target_account uuid := app.current_account_id(); prior_parent record; prior_child record;
DECLARE new_parent_id uuid := gen_random_uuid(); new_child_id uuid; retry_depth integer; child_count integer := 0;
BEGIN
    IF target_account IS NULL OR requester_id IS NULL OR max_ordinal NOT BETWEEN 1 AND 100 OR NOT EXISTS (
        SELECT 1 FROM app.users account_user JOIN app.authentication_principals principal ON principal.id = account_user.principal_id
         WHERE account_user.account_id = target_account AND account_user.principal_id = requester_id AND principal.disabled_at IS NULL
    ) THEN RAISE EXCEPTION 'active account requester and valid retry ordinal are required' USING ERRCODE = '42501'; END IF;
    SELECT job. * , context.region_id, context.target_osm_generation, context.target_work_revision, context.rules_version,
           context.sampling_version, context.path_policy_version, context.minimum_traversal_meters
      INTO prior_parent FROM app.jobs job JOIN app.coverage_job_contexts context
        ON context.job_id = job.id AND context.account_id = job.account_id
     WHERE job.id = prior_job_id AND job.account_id = target_account AND job.kind = 'coverage_update'
       AND job.status IN ('failed', 'partially_succeeded', 'cancelled') FOR UPDATE OF job;
    IF NOT FOUND THEN RAISE EXCEPTION 'coverage retry requires a terminal parent' USING ERRCODE = '55000'; END IF;
    IF EXISTS (SELECT 1 FROM app.jobs retry WHERE retry.retry_of_job_id = prior_job_id AND retry.account_id = target_account
        AND retry.kind = 'coverage_update') THEN
        RAISE EXCEPTION 'only the latest coverage attempt may be retried' USING ERRCODE = '55000';
    END IF;
    WITH RECURSIVE lineage AS (
        SELECT job.id, job.retry_of_job_id, 1 AS ordinal FROM app.jobs job WHERE job.id = prior_job_id
        UNION ALL SELECT previous.id, previous.retry_of_job_id, lineage.ordinal + 1 FROM lineage
        JOIN app.jobs previous ON previous.id = lineage.retry_of_job_id AND previous.account_id = target_account
        WHERE lineage.ordinal < 100
    ) SELECT max(ordinal) INTO retry_depth FROM lineage;
    IF retry_depth >= max_ordinal THEN RAISE EXCEPTION 'coverage retry limit reached' USING ERRCODE = '54001'; END IF;
    FOR prior_child IN
        SELECT child.id, context.workout_id FROM app.jobs child JOIN app.coverage_route_job_contexts context
          ON context.job_id = child.id AND context.account_id = child.account_id
         JOIN app.workout_coverage_states state ON state.workout_id = context.workout_id AND state.account_id = context.account_id
         WHERE child.parent_job_id = prior_job_id AND child.account_id = target_account
           AND child.status IN ('failed', 'cancelled') AND state.readiness_state = 'map_data_ready'
         ORDER BY child.created_at, child.id FOR UPDATE OF state
    LOOP
        child_count := child_count + 1;
    END LOOP;
    IF child_count = 0 THEN RETURN; END IF;
    INSERT INTO app.jobs(id, account_id, kind, priority, parameters, progress_total, retry_of_job_id)
    VALUES(new_parent_id, target_account, 'coverage_update', prior_parent.priority, prior_parent.parameters, child_count, prior_job_id);
    INSERT INTO app.coverage_job_contexts(job_id, account_id, region_id, target_osm_generation, target_work_revision,
        rules_version, sampling_version, path_policy_version, minimum_traversal_meters)
    VALUES(new_parent_id, target_account, prior_parent.region_id, prior_parent.target_osm_generation, prior_parent.target_work_revision + 1,
        prior_parent.rules_version, prior_parent.sampling_version, prior_parent.path_policy_version, prior_parent.minimum_traversal_meters);
    FOR prior_child IN
        SELECT child.id, context.workout_id, state.route_input_revision, state.route_input_sha256,
               COALESCE(state.target_generations, context.target_generations) AS target_generations,
               CASE WHEN child.failure_code = 'coverage-route-timeout'
                    THEN LEAST(context.timeout_retry_count + 1, 16) ELSE 0 END AS timeout_retry_count
          FROM app.jobs child JOIN app.coverage_route_job_contexts context
            ON context.job_id = child.id AND context.account_id = child.account_id
          JOIN app.workout_coverage_states state ON state.workout_id = context.workout_id AND state.account_id = context.account_id
         WHERE child.parent_job_id = prior_job_id AND child.account_id = target_account
           AND child.status IN ('failed', 'cancelled') AND state.readiness_state = 'map_data_ready'
         ORDER BY child.created_at, child.id
    LOOP
        new_child_id := gen_random_uuid();
        INSERT INTO app.jobs(id, parent_job_id, account_id, kind, priority, parameters, retry_of_job_id)
        VALUES(new_child_id, new_parent_id, target_account, 'coverage_update_route', prior_parent.priority, '{}', prior_child.id);
        INSERT INTO app.coverage_route_job_contexts(job_id, account_id, workout_id, route_input_revision, route_input_sha256, target_generations, timeout_retry_count)
        VALUES(new_child_id, target_account, prior_child.workout_id, prior_child.route_input_revision,
            prior_child.route_input_sha256, prior_child.target_generations, prior_child.timeout_retry_count);
        UPDATE app.workout_coverage_states SET processing_state = 'queued', target_job_id = new_child_id,
            target_revision = route_input_revision, target_rules_version = prior_parent.rules_version,
            target_sampling_version = prior_parent.sampling_version, target_path_policy_version = prior_parent.path_policy_version,
            target_generations = prior_child.target_generations, processing_failure_code = NULL, processing_failure_summary = NULL,
            processing_started_at = NULL, processing_finished_at = NULL, updated_at = transaction_timestamp()
         WHERE account_id = target_account AND workout_id = prior_child.workout_id;
    END LOOP;
    INSERT INTO app.coverage_job_progress(job_id, account_id, routes_total) VALUES(new_parent_id, target_account, child_count);
    job_id := new_parent_id; route_count := child_count; RETURN NEXT;
END;
$function$;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.acquire_coverage_matcher_slot (target_job_id uuid, claiming_worker text, current_lease_token uuid, new_slot_token uuid) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
DECLARE target_account uuid; regions text[]; limits app.matcher_slot_limits%ROWTYPE;
BEGIN
    IF claiming_worker = '' OR current_lease_token IS NULL OR new_slot_token IS NULL THEN
        RAISE EXCEPTION 'invalid matcher slot arguments' USING ERRCODE = '22023';
    END IF;
    SELECT job.account_id, ARRAY(SELECT value->> 'regionId' FROM jsonb_array_elements(context.target_generations) value ORDER BY value->> 'regionId')
      INTO target_account, regions FROM app.jobs job
      JOIN app.coverage_route_job_contexts context ON context.job_id = job.id AND context.account_id = job.account_id
     WHERE job.id = target_job_id AND job.account_id = app.current_account_id() AND job.kind = 'coverage_update_route'
       AND job.status = 'running' AND job.worker_id = claiming_worker AND job.lease_token = current_lease_token
       AND job.lease_expires_at >= clock_timestamp() FOR UPDATE OF job;
    IF NOT FOUND OR cardinality(regions) = 0 OR EXISTS (SELECT 1 FROM unnest(regions) region WHERE region IS NULL) THEN RETURN false; END IF;
    PERFORM 1 FROM app.matcher_slot_guard WHERE singleton FOR UPDATE;
    DELETE FROM app.matcher_slots slot WHERE slot.expires_at < clock_timestamp() OR
      (slot.slot_kind = 'production' AND NOT EXISTS (SELECT 1 FROM app.jobs job WHERE job.id = slot.job_id AND job.account_id = slot.account_id
        AND job.status = 'running' AND job.worker_id = slot.owner_id AND job.lease_token = slot.lease_token));
    SELECT * INTO limits FROM app.matcher_slot_limits WHERE singleton;
    IF (SELECT count( * ) FROM app.matcher_slots) >= limits.global_limit OR
       (SELECT count( * ) FROM app.matcher_slots WHERE account_id = target_account) >= limits.account_limit OR
       EXISTS (SELECT 1 FROM unnest(regions) region WHERE
          (SELECT count( * ) FROM app.matcher_slots slot WHERE slot.region_ids && ARRAY[region]) >= limits.region_limit) THEN RETURN false; END IF;
    INSERT INTO app.matcher_slots(slot_token, slot_kind, account_id, job_id, owner_id, lease_token, region_ids, expires_at)
    SELECT new_slot_token, 'production', target_account, target_job_id, claiming_worker, current_lease_token, regions, job.lease_expires_at
      FROM app.jobs job WHERE job.id = target_job_id AND job.account_id = target_account;
    RETURN true;
END;
$function$;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.acquire_diagnostic_matcher_slot (
    target_account_id uuid,
    owner_id text,
    new_slot_token uuid,
    request_token uuid,
    requested_regions TEXT[],
    slot_duration interval
) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
DECLARE limits app.matcher_slot_limits%ROWTYPE;
BEGIN
    IF target_account_id <> app.current_account_id() OR owner_id = '' OR new_slot_token IS NULL OR request_token IS NULL OR
       cardinality(requested_regions) NOT BETWEEN 0 AND 256 OR slot_duration < interval '1 second' OR slot_duration > interval '2 minutes' OR
       EXISTS (SELECT 1 FROM unnest(requested_regions) region WHERE region !~ '^[a-z][a-z0-9-]{0,63}:[a-z][a-z0-9-]{0,63}$') THEN
        RAISE EXCEPTION 'invalid diagnostic matcher slot arguments' USING ERRCODE = '22023';
    END IF;
    PERFORM 1 FROM app.matcher_slot_guard WHERE singleton FOR UPDATE;
    DELETE FROM app.matcher_slots slot WHERE slot.expires_at < clock_timestamp() OR
      (slot.slot_kind = 'production' AND NOT EXISTS (SELECT 1 FROM app.jobs job WHERE job.id = slot.job_id AND job.account_id = slot.account_id
        AND job.status = 'running' AND job.worker_id = slot.owner_id AND job.lease_token = slot.lease_token));
    SELECT * INTO limits FROM app.matcher_slot_limits WHERE singleton;
    IF (SELECT count( * ) FROM app.matcher_slots) >= limits.global_limit OR
       (SELECT count( * ) FROM app.matcher_slots WHERE account_id = target_account_id) >= limits.account_limit OR
       EXISTS (SELECT 1 FROM unnest(requested_regions) region WHERE
          (SELECT count( * ) FROM app.matcher_slots slot WHERE slot.region_ids && ARRAY[region]) >= limits.region_limit) THEN RETURN false; END IF;
    INSERT INTO app.matcher_slots(slot_token, slot_kind, account_id, owner_id, lease_token, region_ids, expires_at)
    VALUES(new_slot_token, 'diagnostic', target_account_id, owner_id, request_token, requested_regions, clock_timestamp() + slot_duration);
    RETURN true;
END;
$function$;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.release_matcher_slot (target_slot_token uuid, owner_id text, current_lease_token uuid) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
BEGIN
    DELETE FROM app.matcher_slots WHERE slot_token = target_slot_token AND account_id = app.current_account_id()
      AND matcher_slots.owner_id = release_matcher_slot.owner_id AND lease_token = current_lease_token;
    RETURN FOUND;
END;
$function$;

-- +goose StatementEnd

REVOKE ALL ON app.coverage_job_contexts,
app.coverage_route_job_contexts,
app.coverage_job_progress,
app.matcher_slot_guard,
app.matcher_slot_limits,
app.matcher_slots
FROM
    PUBLIC,
    workouts_api,
    workouts_worker;

GRANT
SELECT
    ON app.coverage_job_contexts,
    app.coverage_route_job_contexts,
    app.coverage_job_progress TO workouts_api;

GRANT
SELECT
    (
        account_id,
        workout_id,
        route_input_revision,
        route_input_sha256,
        readiness_state,
        processing_state,
        target_job_id,
        target_revision,
        target_rules_version,
        target_sampling_version,
        target_path_policy_version,
        target_generations,
        applied_route_input_revision,
        applied_rules_version,
        applied_sampling_version,
        applied_path_policy_version,
        applied_generations,
        processing_failure_code,
        processing_failure_summary,
        processing_started_at,
        processing_finished_at
    ) ON app.workout_coverage_states TO workouts_worker;

GRANT
SELECT
    (account_id, workout_id) ON app.workout_routes TO workouts_worker;

GRANT
SELECT
    (account_id, workout_id, readiness_state) ON app.workout_coverage_states TO workouts_worker;

GRANT
SELECT
    (account_id, workout_id, region_id, desired_osm_generation) ON app.workout_coverage_regions TO workouts_worker;

GRANT CREATE ON SCHEMA app TO workouts_security_owner;

ALTER TABLE app.coverage_job_contexts OWNER TO workouts_security_owner;

ALTER TABLE app.coverage_route_job_contexts OWNER TO workouts_security_owner;

ALTER TABLE app.coverage_job_progress OWNER TO workouts_security_owner;

ALTER TABLE app.matcher_slot_guard OWNER TO workouts_security_owner;

ALTER TABLE app.matcher_slot_limits OWNER TO workouts_security_owner;

ALTER TABLE app.matcher_slots OWNER TO workouts_security_owner;

ALTER FUNCTION app.enqueue_coverage_update (uuid, uuid, text, bigint, bigint, text, text, text, double precision, jsonb) OWNER TO workouts_security_owner;

ALTER FUNCTION app.claim_next_coverage_route (text, uuid, interval, integer) OWNER TO workouts_security_owner;

ALTER FUNCTION app.finish_coverage_route (uuid, text, uuid, text, integer, text, text) OWNER TO workouts_security_owner;

ALTER FUNCTION app.retry_coverage_update (uuid, uuid, integer) OWNER TO workouts_security_owner;

ALTER FUNCTION app.coverage_route_timeout_retry_count (uuid, text, uuid) OWNER TO workouts_security_owner;

ALTER FUNCTION app.request_coverage_job_cancellation (uuid, uuid) OWNER TO workouts_security_owner;

ALTER FUNCTION app.sync_coverage_route_job_state () OWNER TO workouts_security_owner;

ALTER FUNCTION app.sync_coverage_matcher_slot_lease () OWNER TO workouts_security_owner;

ALTER FUNCTION app.acquire_coverage_matcher_slot (uuid, text, uuid, uuid) OWNER TO workouts_security_owner;

ALTER FUNCTION app.acquire_diagnostic_matcher_slot (uuid, text, uuid, uuid, TEXT[], interval) OWNER TO workouts_security_owner;

ALTER FUNCTION app.release_matcher_slot (uuid, text, uuid) OWNER TO workouts_security_owner;

REVOKE CREATE ON SCHEMA app
FROM
    workouts_security_owner;

REVOKE ALL ON FUNCTION app.enqueue_coverage_update (uuid, uuid, text, bigint, bigint, text, text, text, double precision, jsonb),
app.claim_next_coverage_route (text, uuid, interval, integer),
app.finish_coverage_route (uuid, text, uuid, text, integer, text, text),
app.coverage_route_timeout_retry_count (uuid, text, uuid),
app.retry_coverage_update (uuid, uuid, integer),
app.request_coverage_job_cancellation (uuid, uuid),
app.sync_coverage_route_job_state (),
app.sync_coverage_matcher_slot_lease (),
app.acquire_coverage_matcher_slot (uuid, text, uuid, uuid),
app.acquire_diagnostic_matcher_slot (uuid, text, uuid, uuid, TEXT[], interval),
app.release_matcher_slot (uuid, text, uuid)
FROM
    PUBLIC,
    workouts_api,
    workouts_worker,
    workouts_coverage_worker;

GRANT
EXECUTE ON FUNCTION app.enqueue_coverage_update (uuid, uuid, text, bigint, bigint, text, text, text, double precision, jsonb) TO workouts_worker;

GRANT
EXECUTE ON FUNCTION app.claim_next_coverage_route (text, uuid, interval, integer),
app.finish_coverage_route (uuid, text, uuid, text, integer, text, text),
app.acquire_coverage_matcher_slot (uuid, text, uuid, uuid),
app.release_matcher_slot (uuid, text, uuid),
app.coverage_route_timeout_retry_count (uuid, text, uuid) TO workouts_coverage_worker;

GRANT
EXECUTE ON FUNCTION app.retry_coverage_update (uuid, uuid, integer) TO workouts_api;

GRANT
EXECUTE ON FUNCTION app.acquire_diagnostic_matcher_slot (uuid, text, uuid, uuid, TEXT[], interval),
app.release_matcher_slot (uuid, text, uuid) TO workouts_api;

-- +goose StatementBegin
DO $block$
BEGIN
    EXECUTE format('GRANT CONNECT ON DATABASE %I TO workouts_coverage_worker', current_database());
END;
$block$;

-- +goose StatementEnd

GRANT USAGE ON SCHEMA app TO workouts_coverage_worker;

GRANT
SELECT
    ON public.goose_db_version,
    app.schema_metadata TO workouts_coverage_worker;

GRANT
SELECT
    (id, account_id, kind, status, cancel_requested_at, worker_id, lease_token, lease_expires_at) ON app.jobs TO workouts_coverage_worker;

GRANT
EXECUTE ON FUNCTION app.current_account_id (),
app.heartbeat_job (uuid, text, uuid, interval),
app.finish_job (uuid, text, uuid, text, text, text) TO workouts_coverage_worker;

UPDATE app.schema_metadata
SET
    schema_version = 15,
    minimum_runtime_version = 14
WHERE
    singleton;

-- +goose Down
SELECT
    app.assert_no_active_manual_ingest ();

SELECT
    app.assert_no_active_scheduled_ingest ();

-- +goose StatementBegin
DO $block$
BEGIN
    IF EXISTS (SELECT 1 FROM app.jobs WHERE kind IN ('coverage_update', 'coverage_update_route')) THEN
        RAISE EXCEPTION 'cannot downgrade while coverage jobs exist' USING ERRCODE = '55006';
    END IF;
END;
$block$;

-- +goose StatementEnd

UPDATE app.schema_metadata
SET
    schema_version = 14,
    minimum_runtime_version = 14
WHERE
    singleton;

REVOKE ALL ON app.jobs
FROM
    workouts_coverage_worker;

REVOKE
SELECT
    ON public.goose_db_version,
    app.schema_metadata
FROM
    workouts_coverage_worker;

REVOKE ALL ON ALL FUNCTIONS IN SCHEMA app
FROM
    workouts_coverage_worker;

REVOKE USAGE ON SCHEMA app
FROM
    workouts_coverage_worker;

-- +goose StatementBegin
DO $block$ BEGIN EXECUTE format('REVOKE CONNECT ON DATABASE %I FROM workouts_coverage_worker', current_database()); END $block$;

-- +goose StatementEnd

DROP TRIGGER jobs_coverage_state_after_status ON app.jobs;

DROP FUNCTION app.sync_coverage_route_job_state ();

DROP TRIGGER jobs_coverage_matcher_slot_lease_after_heartbeat ON app.jobs;

DROP FUNCTION app.sync_coverage_matcher_slot_lease ();

DROP FUNCTION app.request_coverage_job_cancellation (uuid, uuid);

DROP FUNCTION app.release_matcher_slot (uuid, text, uuid);

DROP FUNCTION app.acquire_diagnostic_matcher_slot (uuid, text, uuid, uuid, TEXT[], interval);

DROP FUNCTION app.acquire_coverage_matcher_slot (uuid, text, uuid, uuid);

DROP FUNCTION app.finish_coverage_route (uuid, text, uuid, text, integer, text, text);

DROP FUNCTION app.retry_coverage_update (uuid, uuid, integer);

DROP FUNCTION app.coverage_route_timeout_retry_count (uuid, text, uuid);

DROP FUNCTION app.claim_next_coverage_route (text, uuid, interval, integer);

DROP FUNCTION app.enqueue_coverage_update (uuid, uuid, text, bigint, bigint, text, text, text, double precision, jsonb);

DROP TABLE app.matcher_slots;

DROP TABLE app.matcher_slot_limits;

DROP TABLE app.matcher_slot_guard;

ALTER TABLE app.workout_coverage_states
DROP COLUMN processing_finished_at,
DROP COLUMN processing_started_at,
DROP COLUMN processing_failure_summary,
DROP COLUMN processing_failure_code,
DROP COLUMN applied_generations,
DROP COLUMN applied_path_policy_version,
DROP COLUMN applied_sampling_version,
DROP COLUMN applied_rules_version,
DROP COLUMN applied_route_input_sha256,
DROP COLUMN applied_route_input_revision,
DROP COLUMN target_generations,
DROP COLUMN target_path_policy_version,
DROP COLUMN target_sampling_version,
DROP COLUMN target_rules_version,
DROP COLUMN target_revision,
DROP COLUMN target_job_id;

ALTER TABLE app.workout_coverage_states
DROP CONSTRAINT workout_coverage_states_processing_state_check;

ALTER TABLE app.workout_coverage_states
ADD CONSTRAINT workout_coverage_states_processing_state_check CHECK (processing_state = 'not_started');

DROP TABLE app.coverage_job_progress;

DROP TABLE app.coverage_route_job_contexts;

DROP TABLE app.coverage_job_contexts;

ALTER TABLE app.jobs
DROP CONSTRAINT jobs_running_lease_v16_check;

ALTER TABLE app.jobs
DROP CONSTRAINT jobs_parent_lease_v16_check;

ALTER TABLE app.jobs
DROP CONSTRAINT jobs_parent_attempt_v16_check;

ALTER TABLE app.jobs
DROP CONSTRAINT jobs_partial_parent_v16_check;

ALTER TABLE app.jobs
DROP CONSTRAINT jobs_parent_kind_v16_check;

ALTER TABLE app.jobs
DROP CONSTRAINT jobs_kind_v16_check;

ALTER TABLE app.jobs
ADD CHECK (
    kind IN (
        'source_connection_check',
        'workout_deletion',
        'account_deletion',
        'manual_ingest',
        'manual_ingest_source',
        'scheduled_ingest',
        'scheduled_ingest_source',
        'osm_bootstrap',
        'osm_refresh'
    )
);

ALTER TABLE app.jobs
ADD CHECK ((parent_job_id IS NULL) = (kind NOT IN ('manual_ingest_source', 'scheduled_ingest_source')));

ALTER TABLE app.jobs
ADD CHECK (
    status <> 'partially_succeeded'
    OR kind IN ('manual_ingest', 'scheduled_ingest')
);

ALTER TABLE app.jobs
ADD CHECK (
    kind NOT IN ('manual_ingest', 'scheduled_ingest')
    OR attempt = 0
);

ALTER TABLE app.jobs
ADD CHECK (
    kind NOT IN ('manual_ingest', 'scheduled_ingest')
    OR (
        worker_id IS NULL
        AND lease_token IS NULL
        AND claimed_at IS NULL
        AND heartbeat_at IS NULL
        AND lease_expires_at IS NULL
    )
);

ALTER TABLE app.jobs
ADD CHECK (
    (
        status = 'running'
        AND kind NOT IN ('manual_ingest', 'scheduled_ingest')
    ) = (
        worker_id IS NOT NULL
        AND lease_token IS NOT NULL
        AND claimed_at IS NOT NULL
        AND heartbeat_at IS NOT NULL
        AND lease_expires_at IS NOT NULL
    )
);

-- +goose StatementBegin
DO $block$
DECLARE function_row record;
BEGIN
    FOR function_row IN SELECT definition FROM app.coverage_job_migration_function_backup ORDER BY function_name LOOP
        EXECUTE function_row.definition;
    END LOOP;
END;
$block$;

-- +goose StatementEnd

DROP TABLE app.coverage_job_migration_function_backup;

DROP FUNCTION app.set_workout_coverage_readiness (uuid, uuid, bigint, text, text, jsonb, uuid, text, uuid);

DROP FUNCTION app.initialize_workout_coverage (uuid, uuid, bytea, uuid, text, uuid);

DROP FUNCTION app.assert_workout_coverage_job_lease (uuid, uuid, text, uuid);

DROP TRIGGER workout_coverage_states_data_generation_after_write ON app.workout_coverage_states;

DROP POLICY workout_coverage_regions_owner_policy ON app.workout_coverage_regions;

DROP POLICY workout_coverage_regions_account_policy ON app.workout_coverage_regions;

DROP TABLE app.workout_coverage_regions;

DROP POLICY workout_coverage_states_owner_policy ON app.workout_coverage_states;

DROP POLICY workout_coverage_states_account_policy ON app.workout_coverage_states;

DROP TABLE app.workout_coverage_states;
