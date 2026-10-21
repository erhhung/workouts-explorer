-- +goose Up
-- Ingest owns the workout before this row. No account FK or reconciliation
-- wakeup here: either would invert ingest's source/workout lock order.
CREATE TABLE app.adopted_workout_coverage_refreshes (
    workout_id uuid NOT NULL,
    account_id uuid NOT NULL,
    event_id uuid NOT NULL DEFAULT gen_random_uuid(),
    source_id uuid NOT NULL,
    source_file_id uuid NOT NULL,
    requested_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    next_attempt_at timestamptz NOT NULL DEFAULT '-infinity',
    PRIMARY KEY (account_id, workout_id),
    FOREIGN KEY (workout_id, account_id) REFERENCES app.workouts(id, account_id) ON DELETE CASCADE
);
CREATE INDEX adopted_workout_coverage_refreshes_due_idx
ON app.adopted_workout_coverage_refreshes(account_id, next_attempt_at);
ALTER TABLE app.adopted_workout_coverage_refreshes ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.adopted_workout_coverage_refreshes FORCE ROW LEVEL SECURITY;
CREATE POLICY adopted_workout_coverage_refreshes_owner_policy ON app.adopted_workout_coverage_refreshes
TO workouts_security_owner USING (TRUE) WITH CHECK (TRUE);
REVOKE ALL ON app.adopted_workout_coverage_refreshes
FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;

-- +goose StatementBegin
CREATE FUNCTION app.record_adopted_workout_coverage_refresh (
    target_job_id uuid, claiming_worker text, current_lease_token uuid, target_workout_id uuid
) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
DECLARE target record;
BEGIN
    -- The private capability pins the file transaction under source/parent/child
    -- locks. Check lease expiry when granting a fresh fence, not after admission.
    SELECT workout.account_id, workout.source_id, workout.source_file_id INTO target
      FROM app.ingest_write_capabilities capability
      JOIN app.jobs job ON job.id = capability.job_id AND job.account_id = capability.account_id
      JOIN app.jobs parent ON parent.id = job.parent_job_id AND parent.account_id = job.account_id
      JOIN app.sources source ON source.id = capability.source_id AND source.account_id = capability.account_id
      JOIN app.workouts workout ON workout.id = target_workout_id AND workout.account_id = capability.account_id
        AND workout.source_id = capability.source_id
      JOIN app.source_files file ON file.id = workout.source_file_id AND file.account_id = workout.account_id
        AND file.source_id = workout.source_id AND file.job_id = job.id
      JOIN app.accounts account ON account.id = workout.account_id AND account.state = 'active'
     WHERE capability.backend_pid = pg_backend_pid() AND capability.transaction_id = txid_current()
       AND capability.account_id = app.current_account_id() AND capability.job_id = target_job_id
       AND capability.worker_id = claiming_worker AND capability.lease_token = current_lease_token
       AND capability.runtime_version >= 26
       AND capability.runtime_version >= (SELECT minimum_runtime_version FROM app.schema_metadata WHERE singleton)
       AND app.valid_ingest_ownership_epoch(capability.account_id, capability.ownership_epoch, capability.runtime_version)
       AND job.kind IN ('manual_ingest_source', 'scheduled_ingest_source') AND job.status = 'running'
       AND job.worker_id = claiming_worker AND job.lease_token = current_lease_token
       AND job.cancel_requested_at IS NULL
       AND parent.status = 'running' AND parent.cancel_requested_at IS NULL
       AND parent.kind = CASE job.kind WHEN 'manual_ingest_source' THEN 'manual_ingest' ELSE 'scheduled_ingest' END
       AND source.deleted_at IS NULL AND workout.deletion_requested_at IS NULL;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'adopted coverage refresh requires a current ingest fence and ownership' USING ERRCODE = '42501';
    END IF;
    INSERT INTO app.adopted_workout_coverage_refreshes(account_id,workout_id,source_id,source_file_id)
    VALUES(target.account_id,target_workout_id,target.source_id,target.source_file_id)
    ON CONFLICT (account_id,workout_id) DO UPDATE SET event_id = gen_random_uuid(),
        source_id = EXCLUDED.source_id, source_file_id = EXCLUDED.source_file_id,
        requested_at = clock_timestamp(), next_attempt_at = '-infinity';
END;
$function$;
-- +goose StatementEnd

-- A NULL reconciliation revision selects an ingest lease; otherwise only a
-- current account reconciliation lease authorizes the read/write.
-- +goose StatementBegin
CREATE FUNCTION app.adopted_coverage_authority_current (
    target_account_id uuid, target_job_id uuid, claiming_worker text, current_lease_token uuid, reconciliation_revision bigint
) RETURNS boolean LANGUAGE sql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
    SELECT target_account_id = app.current_account_id() AND EXISTS (
        SELECT 1 FROM app.accounts WHERE id = target_account_id AND state = 'active'
    ) AND CASE WHEN reconciliation_revision IS NULL THEN EXISTS (
        SELECT 1 FROM app.jobs job JOIN app.jobs parent ON parent.id = job.parent_job_id AND parent.account_id = job.account_id
         WHERE job.id = target_job_id AND job.account_id = target_account_id
           AND job.kind IN ('manual_ingest_source','scheduled_ingest_source') AND job.status = 'running'
           AND job.worker_id = claiming_worker AND job.lease_token = current_lease_token
           AND job.lease_expires_at >= clock_timestamp() AND job.cancel_requested_at IS NULL
           AND parent.status IN ('queued','running') AND parent.cancel_requested_at IS NULL
    ) ELSE target_job_id IS NULL AND EXISTS (
        SELECT 1 FROM app.coverage_reconciliation_accounts reconciliation
         WHERE reconciliation.account_id = target_account_id AND reconciliation.active_revision = reconciliation_revision
           AND reconciliation.worker_id = claiming_worker AND reconciliation.lease_token = current_lease_token
           AND reconciliation.lease_expires_at >= clock_timestamp()
    ) END
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.read_adopted_workout_coverage_refreshes (
    target_account_id uuid, target_job_id uuid, claiming_worker text, current_lease_token uuid,
    reconciliation_revision bigint, after_workout_id uuid, page_limit integer
) RETURNS TABLE(workout_id uuid, event_id uuid) LANGUAGE sql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
    SELECT request.workout_id, request.event_id FROM app.adopted_workout_coverage_refreshes request
     WHERE request.account_id = target_account_id AND request.next_attempt_at <= clock_timestamp()
       AND (after_workout_id IS NULL OR request.workout_id > after_workout_id) AND page_limit BETWEEN 1 AND 100
       AND app.adopted_coverage_authority_current(target_account_id,target_job_id,claiming_worker,current_lease_token,reconciliation_revision)
     ORDER BY request.workout_id LIMIT LEAST(page_limit,100)
$function$;
-- +goose StatementEnd

-- Ordinary ingest repair must exclude every obligation, including deferred ones.
-- Keep this distinct from the due-only replay reader, without exposing the table.
-- +goose StatementBegin
CREATE FUNCTION app.read_pending_adopted_workout_coverage_refreshes (
    target_account_id uuid, target_job_id uuid, claiming_worker text, current_lease_token uuid
) RETURNS TABLE(workout_id uuid) LANGUAGE sql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
    SELECT request.workout_id FROM app.adopted_workout_coverage_refreshes request
     WHERE request.account_id = target_account_id
       AND app.adopted_coverage_authority_current(target_account_id,target_job_id,claiming_worker,current_lease_token,NULL)
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.read_adopted_workout_coverage_route (
    target_account_id uuid, target_job_id uuid, claiming_worker text, current_lease_token uuid,
    reconciliation_revision bigint, target_workout_id uuid, target_event_id uuid
) RETURNS TABLE(sequence integer, recorded_at timestamptz, latitude double precision, longitude double precision,
    altitude double precision, speed double precision, course double precision, horizontal_accuracy double precision,
    vertical_accuracy double precision, speed_accuracy double precision, course_accuracy double precision)
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
    SELECT point.sequence,point.recorded_at,point.latitude,point.longitude,point.altitude,point.speed,point.course,
        point.horizontal_accuracy,point.vertical_accuracy,point.speed_accuracy,point.course_accuracy
      FROM app.adopted_workout_coverage_refreshes request
      JOIN app.workouts workout ON workout.id = request.workout_id AND workout.account_id = request.account_id
      JOIN app.workout_routes route ON route.workout_id = workout.id AND route.account_id = workout.account_id
      JOIN app.workout_route_points point ON point.workout_id = workout.id AND point.account_id = workout.account_id
     WHERE request.account_id = target_account_id AND request.workout_id = target_workout_id AND request.event_id = target_event_id
       AND workout.deletion_requested_at IS NULL
       AND app.adopted_coverage_authority_current(target_account_id,target_job_id,claiming_worker,current_lease_token,reconciliation_revision)
     ORDER BY point.sequence LIMIT 50001
$function$;
-- +goose StatementEnd

-- Reuse schema 22's exact bounded canonical encoding without changing its public
-- repair signature. This helper is internal to the security owner.
-- +goose StatementBegin
DO $block$
DECLARE definition text; digest_body text; anchor text; anchor_count integer;
DECLARE digest_start integer; digest_end integer;
BEGIN
    definition := pg_get_functiondef('app.repair_coverage_reconciliation_input(uuid,bigint,text,uuid,uuid,bigint,bytea,bytea)'::regprocedure);
    anchor := $pattern$(?i)\mWITH[[:space:]]+points[[:space:]]+AS[[:space:]]+MATERIALIZED[[:space:]]*\($pattern$;
    SELECT count(*) INTO anchor_count FROM regexp_matches(definition, anchor, 'g');
    IF anchor_count <> 1 THEN RAISE EXCEPTION 'canonical coverage digest start anchor must occur exactly once (found %)', anchor_count; END IF;
    digest_start := regexp_instr(definition, anchor);
    anchor := $pattern$(?i)\mFROM[[:space:]]+encoded[[:space:]]+point[[:space:]]*;$pattern$;
    SELECT count(*) INTO anchor_count FROM regexp_matches(definition, anchor, 'g');
    IF anchor_count <> 1 THEN RAISE EXCEPTION 'canonical coverage digest end anchor must occur exactly once (found %)', anchor_count; END IF;
    digest_end := regexp_instr(definition, anchor, 1, 1, 1);
    IF digest_end <= digest_start THEN RAISE EXCEPTION 'canonical coverage digest anchors are out of order'; END IF;
    digest_body := substring(definition FROM digest_start FOR digest_end - digest_start);
    EXECUTE 'CREATE FUNCTION app.adopted_coverage_route_digest(target_account_id uuid,target_workout_id uuid) RETURNS bytea
        LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $digest$
        DECLARE current_digest bytea; BEGIN ' || digest_body || ' RETURN current_digest; END; $digest$';
END;
$block$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.apply_adopted_workout_coverage_refresh (
    target_account_id uuid, target_job_id uuid, claiming_worker text, current_lease_token uuid,
    reconciliation_revision bigint, target_workout_id uuid, target_event_id uuid, route_digest bytea,
    readiness_value text, reason_value text, region_generations jsonb, new_parent_id uuid, new_child_id uuid,
    rules_value text, sampling_value text, policy_value text, minimum_value double precision
) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
DECLARE request app.adopted_workout_coverage_refreshes%ROWTYPE; workout app.workouts%ROWTYPE;
DECLARE state app.workout_coverage_states%ROWTYPE; revision bigint; anchor record; enqueued uuid; prior_timeout text;
BEGIN
    IF NOT app.adopted_coverage_authority_current(target_account_id,target_job_id,claiming_worker,current_lease_token,reconciliation_revision)
    THEN RETURN false; END IF;
    -- Fail fast, never hold a Coverage lock waiting for ingest's workout or its
    -- data-generation trigger. No APP locks are held during OSM resolution.
    PERFORM 1 FROM app.accounts account WHERE account.id = target_account_id AND account.state = 'active' FOR UPDATE NOWAIT;
    IF NOT FOUND THEN RETURN false; END IF;
    IF reconciliation_revision IS NOT NULL THEN
        PERFORM 1 FROM app.coverage_reconciliation_accounts WHERE account_id = target_account_id FOR UPDATE NOWAIT;
    ELSE
        PERFORM 1 FROM app.jobs parent JOIN app.jobs child ON child.parent_job_id = parent.id AND child.account_id = parent.account_id
         WHERE child.id = target_job_id AND child.account_id = target_account_id FOR UPDATE OF parent NOWAIT;
        PERFORM 1 FROM app.jobs WHERE id = target_job_id AND account_id = target_account_id FOR UPDATE NOWAIT;
    END IF;
    PERFORM 1 FROM app.jobs parent WHERE parent.account_id = target_account_id AND parent.kind = 'coverage_update'
      AND parent.status IN ('queued','running') ORDER BY parent.id FOR UPDATE NOWAIT;
    SELECT * INTO workout FROM app.workouts WHERE account_id = target_account_id AND id = target_workout_id FOR UPDATE NOWAIT;
    IF NOT FOUND THEN RETURN false; END IF;
    -- Workout first, then request, matching adoption. Event CAS prevents an old
    -- OSM lookup from consuming a later adoption, even with identical raw input.
    SELECT * INTO request FROM app.adopted_workout_coverage_refreshes
     WHERE account_id = target_account_id AND workout_id = target_workout_id AND event_id = target_event_id FOR UPDATE NOWAIT;
    IF NOT FOUND THEN RETURN true; END IF;
    IF workout.deletion_requested_at IS NOT NULL OR NOT EXISTS (
        SELECT 1 FROM app.workout_routes WHERE account_id = target_account_id AND workout_id = target_workout_id
    ) OR NOT EXISTS (
        SELECT 1 FROM app.workout_route_points WHERE account_id = target_account_id AND workout_id = target_workout_id
    ) THEN
        DELETE FROM app.adopted_workout_coverage_refreshes WHERE account_id = target_account_id AND workout_id = target_workout_id AND event_id = target_event_id;
    ELSE
        IF readiness_value IS NULL OR readiness_value NOT IN ('pending','unavailable','map_data_ready') OR region_generations IS NULL
           OR (readiness_value = 'pending' AND reason_value IS DISTINCT FROM 'region_not_active')
           OR (readiness_value = 'unavailable' AND reason_value IS DISTINCT FROM 'no_provider_region')
           OR (readiness_value = 'map_data_ready' AND (reason_value IS NOT NULL OR NOT app.valid_coverage_generation_vector(region_generations)))
           OR (readiness_value <> 'map_data_ready' AND region_generations <> '[]'::jsonb) THEN
            RAISE EXCEPTION 'invalid adopted coverage readiness' USING ERRCODE = '22023';
        END IF;
        IF octet_length(route_digest) IS DISTINCT FROM 32 OR app.adopted_coverage_route_digest(target_account_id,target_workout_id) IS DISTINCT FROM route_digest THEN
            RAISE EXCEPTION 'adopted coverage route input changed' USING ERRCODE = '40001';
        END IF;
        SELECT * INTO state FROM app.workout_coverage_states WHERE account_id = target_account_id AND workout_id = target_workout_id FOR UPDATE NOWAIT;
        revision := CASE WHEN state.workout_id IS NULL THEN 1 WHEN state.route_input_sha256 = route_digest THEN state.route_input_revision ELSE state.route_input_revision + 1 END;
        prior_timeout := current_setting('lock_timeout');
        PERFORM set_config('lock_timeout','1ms',true);
        INSERT INTO app.workout_coverage_states(account_id,workout_id,route_input_revision,route_input_sha256,readiness_state,reason,map_data_ready_at)
        VALUES(target_account_id,target_workout_id,revision,route_digest,readiness_value,reason_value,
            CASE WHEN readiness_value = 'map_data_ready' THEN clock_timestamp() END)
        ON CONFLICT (account_id,workout_id) DO UPDATE SET route_input_revision = EXCLUDED.route_input_revision,
            route_input_sha256 = EXCLUDED.route_input_sha256, readiness_state = EXCLUDED.readiness_state,
            reason = EXCLUDED.reason, map_data_ready_at = EXCLUDED.map_data_ready_at,
            processing_state = CASE WHEN readiness_value <> 'map_data_ready' OR app.workout_coverage_states.route_input_sha256 <> route_digest
                THEN CASE WHEN app.workout_coverage_states.applied_route_input_revision IS NULL THEN 'not_started' ELSE 'stale' END
                ELSE app.workout_coverage_states.processing_state END,
            target_job_id = CASE WHEN readiness_value = 'map_data_ready' THEN app.workout_coverage_states.target_job_id END,
            target_revision = CASE WHEN readiness_value = 'map_data_ready' THEN app.workout_coverage_states.target_revision END,
            target_rules_version = CASE WHEN readiness_value = 'map_data_ready' THEN app.workout_coverage_states.target_rules_version END,
            target_sampling_version = CASE WHEN readiness_value = 'map_data_ready' THEN app.workout_coverage_states.target_sampling_version END,
            target_path_policy_version = CASE WHEN readiness_value = 'map_data_ready' THEN app.workout_coverage_states.target_path_policy_version END,
            target_generations = CASE WHEN readiness_value = 'map_data_ready' THEN app.workout_coverage_states.target_generations END,
            updated_at = clock_timestamp();
        DELETE FROM app.workout_coverage_regions WHERE account_id = target_account_id AND workout_id = target_workout_id;
        INSERT INTO app.workout_coverage_regions(account_id,workout_id,region_id,desired_osm_generation)
        SELECT target_account_id,target_workout_id,item->>'regionId',(item->>'generation')::bigint FROM jsonb_array_elements(region_generations) item;
        IF readiness_value = 'map_data_ready' THEN
            -- Defeat normal identical-queued-target dedup only for this event.
            -- Running children keep their leases but lose target_job_id authority.
            UPDATE app.coverage_route_job_contexts context SET result_outcome = 'superseded' FROM app.jobs child
             WHERE child.id = context.job_id AND child.account_id = context.account_id AND context.account_id = target_account_id
               AND context.workout_id = target_workout_id AND child.status = 'queued';
            PERFORM set_config('app.job_transition','supersede',true);
            UPDATE app.jobs child SET status = 'succeeded', terminal_at = clock_timestamp() FROM app.coverage_route_job_contexts context
             WHERE child.id = context.job_id AND child.account_id = context.account_id AND context.account_id = target_account_id
               AND context.workout_id = target_workout_id AND child.status = 'queued';
            PERFORM app.derive_parent_status(parent.id) FROM app.jobs parent
             WHERE parent.account_id = target_account_id AND parent.kind = 'coverage_update' AND parent.status IN ('queued','running')
               AND EXISTS (SELECT 1 FROM app.jobs child JOIN app.coverage_route_job_contexts context
                   ON context.job_id = child.id AND context.account_id = child.account_id
                   WHERE child.parent_job_id = parent.id AND child.account_id = parent.account_id
                     AND context.workout_id = target_workout_id AND child.status = 'succeeded' AND context.result_outcome = 'superseded');
            SELECT item->>'regionId' region_id,(item->>'generation')::bigint generation INTO anchor
              FROM jsonb_array_elements(region_generations) item ORDER BY item->>'regionId' LIMIT 1;
            SELECT result.job_id INTO enqueued FROM app.enqueue_coverage_update(target_account_id,new_parent_id,anchor.region_id,anchor.generation,
                GREATEST(COALESCE(reconciliation_revision,revision),1),rules_value,sampling_value,policy_value,minimum_value,
                jsonb_build_array(jsonb_build_object('jobId',new_child_id,'workoutId',target_workout_id,'routeRevision',revision,'generations',region_generations))) result;
            IF enqueued IS NULL THEN RAISE EXCEPTION 'adopted coverage enqueue failed' USING ERRCODE = '40001'; END IF;
            DELETE FROM app.adopted_workout_coverage_refreshes WHERE account_id = target_account_id AND workout_id = target_workout_id AND event_id = target_event_id;
        ELSE
            -- Keep the obligation until real map data exists, without a hot loop.
            UPDATE app.adopted_workout_coverage_refreshes SET next_attempt_at = clock_timestamp() + interval '1 hour'
             WHERE account_id = target_account_id AND workout_id = target_workout_id AND event_id = target_event_id;
        END IF;
        PERFORM set_config('lock_timeout',prior_timeout,true);
    END IF;
    IF NOT app.adopted_coverage_authority_current(target_account_id,target_job_id,claiming_worker,current_lease_token,reconciliation_revision) THEN
        RAISE EXCEPTION 'adopted coverage authority expired' USING ERRCODE = '40001';
    END IF;
    RETURN true;
END;
$function$;
-- +goose StatementEnd

-- Claims discover committed requests without ingest updating the account/header.
-- Reset the cursor for an eligible adoption, including a previously completed scan.
-- +goose StatementBegin
DO $block$
DECLARE definition text; anchor text; anchor_count integer;
BEGIN
    definition := pg_get_functiondef('app.claim_coverage_reconciliation(text,uuid,interval,integer)'::regprocedure);
    anchor := $pattern$(?i)\mAND[[:space:]]+reconciliation[[:space:]]*\.[[:space:]]*next_scan_at[[:space:]]*<=[[:space:]]*transaction_timestamp[[:space:]]*\([[:space:]]*\)$pattern$;
    SELECT count(*) INTO anchor_count FROM regexp_matches(definition, anchor, 'g');
    IF anchor_count <> 1 THEN RAISE EXCEPTION 'reconciliation claim due anchor must occur exactly once (found %)', anchor_count; END IF;
    definition := regexp_replace(definition,anchor,'AND (reconciliation.next_scan_at <= transaction_timestamp() OR EXISTS (
        SELECT 1 FROM app.adopted_workout_coverage_refreshes request WHERE request.account_id = reconciliation.account_id
          AND request.next_attempt_at <= clock_timestamp() AND reconciliation.last_failure_code IS NULL))');
    anchor := $pattern$(?i)\mIF[[:space:]]+candidate[[:space:]]*\.[[:space:]]*active_revision[[:space:]]+IS[[:space:]]+DISTINCT[[:space:]]+FROM[[:space:]]+candidate[[:space:]]*\.[[:space:]]*desired_revision[[:space:]]+OR[[:space:]]+candidate[[:space:]]*\.[[:space:]]*completed_revision[[:space:]]*>=[[:space:]]*candidate[[:space:]]*\.[[:space:]]*desired_revision[[:space:]]+THEN\M$pattern$;
    SELECT count(*) INTO anchor_count FROM regexp_matches(definition, anchor, 'g');
    IF anchor_count <> 1 THEN RAISE EXCEPTION 'reconciliation claim cursor anchor must occur exactly once (found %)', anchor_count; END IF;
    definition := regexp_replace(definition,anchor,'IF candidate.active_revision IS DISTINCT FROM candidate.desired_revision OR candidate.completed_revision >= candidate.desired_revision OR EXISTS (
        SELECT 1 FROM app.adopted_workout_coverage_refreshes request WHERE request.account_id = candidate.account_id
          AND request.next_attempt_at <= clock_timestamp()) THEN');
    EXECUTE definition;
    definition := pg_get_functiondef('app.claim_next_coverage_route(text,uuid,interval,integer)'::regprocedure);
    anchor := $pattern$(?i)\mIF[[:space:]]+runtime_version[[:space:]]+IS[[:space:]]+NULL[[:space:]]+OR[[:space:]]+runtime_version[[:space:]]*<[[:space:]]*23[[:space:]]+THEN\M$pattern$;
    SELECT count(*) INTO anchor_count FROM regexp_matches(definition, anchor, 'g');
    IF anchor_count <> 1 THEN RAISE EXCEPTION 'coverage runtime floor anchor must occur exactly once (found %)', anchor_count; END IF;
    definition := regexp_replace(definition,anchor,'IF runtime_version IS NULL OR runtime_version < 26 THEN');
    anchor := $pattern$(?i)'coverage[[:space:]]+worker[[:space:]]+runtime[[:space:]]+version[[:space:]]+23[[:space:]]+or[[:space:]]+newer[[:space:]]+is[[:space:]]+required'$pattern$;
    SELECT count(*) INTO anchor_count FROM regexp_matches(definition, anchor, 'g');
    IF anchor_count <> 1 THEN RAISE EXCEPTION 'coverage runtime message anchor must occur exactly once (found %)', anchor_count; END IF;
    definition := regexp_replace(definition,anchor,'''coverage worker runtime version 26 or newer is required''');
    EXECUTE definition;
    definition := pg_get_functiondef('app.read_coverage_reconciliation_routes(uuid,bigint,text,uuid,uuid,integer)'::regprocedure);
    anchor := $pattern$(?i)\mAND[[:space:]]+workout[[:space:]]*\.[[:space:]]*deletion_requested_at[[:space:]]+IS[[:space:]]+NULL\M$pattern$;
    SELECT count(*) INTO anchor_count FROM regexp_matches(definition, anchor, 'g');
    IF anchor_count <> 1 THEN RAISE EXCEPTION 'reconciliation route reader anchor must occur exactly once (found %)', anchor_count; END IF;
    definition := regexp_replace(definition,anchor,E'\\&' || ' AND NOT EXISTS (
        SELECT 1 FROM app.adopted_workout_coverage_refreshes request
         WHERE request.account_id = workout.account_id AND request.workout_id = workout.id)');
    EXECUTE definition;
    -- After an adoption is consumed, a crash/retry must not replace its already
    -- running target merely because applied evidence is not current yet.
    definition := pg_get_functiondef('app.apply_coverage_reconciliation(uuid,bigint,text,uuid,uuid,bigint,bytea,text,text,jsonb,uuid,uuid,text,text,text,double precision)'::regprocedure);
    anchor := $pattern$(?i)\mIF[[:space:]]+NOT[[:space:]]+needs_match[[:space:]]+THEN[[:space:]]+RETURN[[:space:]]+true[[:space:]]*;[[:space:]]*END[[:space:]]+IF[[:space:]]*;$pattern$;
    SELECT count(*) INTO anchor_count FROM regexp_matches(definition, anchor, 'g');
    IF anchor_count <> 1 THEN RAISE EXCEPTION 'reconciliation live-target anchor must occur exactly once (found %)', anchor_count; END IF;
    definition := regexp_replace(definition,anchor,'IF NOT needs_match OR EXISTS (
        SELECT 1 FROM app.jobs child
        JOIN app.jobs parent ON parent.id = child.parent_job_id AND parent.account_id = child.account_id
        JOIN app.coverage_route_job_contexts route_context ON route_context.job_id = child.id AND route_context.account_id = child.account_id
        JOIN app.coverage_job_contexts parent_context ON parent_context.job_id = parent.id AND parent_context.account_id = parent.account_id
        WHERE child.id = current_state.target_job_id AND child.account_id = target_account_id
          AND child.kind = ''coverage_update_route'' AND child.status IN (''queued'',''running'') AND child.cancel_requested_at IS NULL
          AND parent.kind = ''coverage_update'' AND parent.status IN (''queued'',''running'') AND parent.cancel_requested_at IS NULL
          AND route_context.workout_id = target_workout_id AND route_context.route_input_revision = target_route_revision
          AND route_context.route_input_sha256 = target_route_digest
          AND app.coverage_generation_vectors_equal(route_context.target_generations,region_generations)
          AND parent_context.rules_version = rules_version_value AND parent_context.sampling_version = sampling_version_value
          AND parent_context.path_policy_version = path_policy_version_value
          AND parent_context.minimum_traversal_meters = minimum_traversal_meters_value
    ) THEN RETURN true; END IF;');
    EXECUTE definition;
END;
$block$;
-- +goose StatementEnd

GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER TABLE app.adopted_workout_coverage_refreshes OWNER TO workouts_security_owner;
ALTER FUNCTION app.record_adopted_workout_coverage_refresh(uuid,text,uuid,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.adopted_coverage_authority_current(uuid,uuid,text,uuid,bigint) OWNER TO workouts_security_owner;
ALTER FUNCTION app.adopted_coverage_route_digest(uuid,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.read_adopted_workout_coverage_refreshes(uuid,uuid,text,uuid,bigint,uuid,integer) OWNER TO workouts_security_owner;
ALTER FUNCTION app.read_pending_adopted_workout_coverage_refreshes(uuid,uuid,text,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.read_adopted_workout_coverage_route(uuid,uuid,text,uuid,bigint,uuid,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.apply_adopted_workout_coverage_refresh(uuid,uuid,text,uuid,bigint,uuid,uuid,bytea,text,text,jsonb,uuid,uuid,text,text,text,double precision) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
REVOKE ALL ON FUNCTION app.record_adopted_workout_coverage_refresh(uuid,text,uuid,uuid),
app.adopted_coverage_authority_current(uuid,uuid,text,uuid,bigint), app.adopted_coverage_route_digest(uuid,uuid),
app.read_adopted_workout_coverage_refreshes(uuid,uuid,text,uuid,bigint,uuid,integer),
app.read_pending_adopted_workout_coverage_refreshes(uuid,uuid,text,uuid),
app.read_adopted_workout_coverage_route(uuid,uuid,text,uuid,bigint,uuid,uuid),
app.apply_adopted_workout_coverage_refresh(uuid,uuid,text,uuid,bigint,uuid,uuid,bytea,text,text,jsonb,uuid,uuid,text,text,text,double precision)
FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;
GRANT EXECUTE ON FUNCTION app.record_adopted_workout_coverage_refresh(uuid,text,uuid,uuid) TO workouts_worker;
GRANT EXECUTE ON FUNCTION app.read_pending_adopted_workout_coverage_refreshes(uuid,uuid,text,uuid) TO workouts_worker;
GRANT EXECUTE ON FUNCTION app.read_adopted_workout_coverage_refreshes(uuid,uuid,text,uuid,bigint,uuid,integer),
app.read_adopted_workout_coverage_route(uuid,uuid,text,uuid,bigint,uuid,uuid),
app.apply_adopted_workout_coverage_refresh(uuid,uuid,text,uuid,bigint,uuid,uuid,bytea,text,text,jsonb,uuid,uuid,text,text,text,double precision)
TO workouts_worker, workouts_coverage_worker;

UPDATE app.schema_metadata SET schema_version = 26, minimum_runtime_version = 26 WHERE singleton;

-- +goose Down
-- +goose StatementBegin
DO $block$ BEGIN
    RAISE EXCEPTION 'adopted workout coverage is forward-only' USING ERRCODE = '55000';
END; $block$;
-- +goose StatementEnd
