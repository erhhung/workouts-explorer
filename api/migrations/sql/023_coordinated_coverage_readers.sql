-- +goose Up
-- Store only opaque SHA-256 identities, never reader endpoints or credentials.
ALTER TABLE app.matcher_slots
ADD COLUMN reader_cluster_key text,
ADD COLUMN reader_backend_key text,
ADD CONSTRAINT matcher_slots_reader_keys_check CHECK (
    (reader_cluster_key IS NULL AND reader_backend_key IS NULL)
    OR (reader_cluster_key IS NOT NULL AND reader_backend_key IS NOT NULL
        AND reader_cluster_key ~ '^[0-9a-f]{64}$' AND reader_backend_key ~ '^[0-9a-f]{64}$')
);

CREATE INDEX matcher_slots_reader_active_idx
ON app.matcher_slots (reader_cluster_key, reader_backend_key, expires_at)
WHERE reader_cluster_key IS NOT NULL;

-- The backend cap is shared by all callers; preserve existing admission limits.
ALTER TABLE app.matcher_slot_limits
ADD COLUMN reader_limit integer NOT NULL DEFAULT 1 CHECK (reader_limit > 0);

ALTER TABLE app.matcher_slots ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.matcher_slots FORCE ROW LEVEL SECURITY;
CREATE POLICY matcher_slots_owner_policy ON app.matcher_slots
TO workouts_security_owner USING (TRUE) WITH CHECK (TRUE);
ALTER TABLE app.matcher_slot_limits ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.matcher_slot_limits FORCE ROW LEVEL SECURITY;
CREATE POLICY matcher_slot_limits_owner_policy ON app.matcher_slot_limits
TO workouts_security_owner USING (TRUE) WITH CHECK (TRUE);
ALTER TABLE app.matcher_slot_guard ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.matcher_slot_guard FORCE ROW LEVEL SECURITY;
CREATE POLICY matcher_slot_guard_owner_policy ON app.matcher_slot_guard
TO workouts_security_owner USING (TRUE) WITH CHECK (TRUE);

-- +goose StatementBegin
CREATE FUNCTION app.assign_matcher_reader (
    slot uuid, owner text, lease uuid, cluster_key text, eligible_backend_keys text[]
) RETURNS text LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
DECLARE reservation app.matcher_slots%ROWTYPE; chosen text; backend_limit integer;
BEGIN
    IF slot IS NULL OR owner IS NULL OR length(owner) NOT BETWEEN 1 AND 200 OR lease IS NULL
       OR cluster_key IS NULL OR cluster_key !~ '^[0-9a-f]{64}$'
       OR eligible_backend_keys IS NULL OR cardinality(eligible_backend_keys) NOT BETWEEN 1 AND 64
       OR array_ndims(eligible_backend_keys) IS DISTINCT FROM 1
       OR EXISTS (SELECT 1 FROM unnest(eligible_backend_keys) key WHERE key IS NULL OR key !~ '^[0-9a-f]{64}$')
       OR (SELECT count(DISTINCT key) FROM unnest(eligible_backend_keys) key) <> cardinality(eligible_backend_keys) THEN
        RAISE EXCEPTION 'invalid matcher reader arguments' USING ERRCODE = '22023';
    END IF;
    PERFORM 1 FROM app.accounts account
     WHERE account.id = app.current_account_id() AND account.state = 'active' FOR SHARE;
    IF NOT FOUND THEN RETURN NULL; END IF;
    -- Read without a slot lock: production always locks its job before the guard.
    SELECT s.* INTO reservation FROM app.matcher_slots s
     WHERE s.slot_token = slot AND s.account_id = app.current_account_id()
       AND s.owner_id = owner AND s.lease_token = lease AND s.expires_at >= clock_timestamp();
    IF NOT FOUND THEN RETURN NULL; END IF;
    IF reservation.slot_kind = 'production' THEN
        PERFORM 1 FROM app.jobs job
         WHERE job.id = reservation.job_id AND job.account_id = reservation.account_id
           AND job.kind = 'coverage_update_route' AND job.status = 'running' AND job.cancel_requested_at IS NULL
           AND job.worker_id = owner AND job.lease_token = lease AND job.lease_expires_at >= clock_timestamp()
         FOR UPDATE;
        IF NOT FOUND THEN RETURN NULL; END IF;
    END IF;
    PERFORM 1 FROM app.matcher_slot_guard WHERE singleton FOR UPDATE;
    DELETE FROM app.matcher_slots s WHERE s.slot_token IN (
        SELECT expired.slot_token FROM app.matcher_slots expired
         WHERE expired.expires_at < clock_timestamp() FOR UPDATE SKIP LOCKED
    );
    -- Lock waits can outlive a lease. Recheck the clock after every blocking lock.
    SELECT s.* INTO reservation FROM app.matcher_slots s
     WHERE s.slot_token = slot AND s.account_id = app.current_account_id()
       AND s.owner_id = owner AND s.lease_token = lease FOR UPDATE;
    IF NOT FOUND OR reservation.expires_at < clock_timestamp() THEN RETURN NULL; END IF;
    IF reservation.slot_kind = 'production' AND NOT EXISTS (
        SELECT 1 FROM app.jobs job WHERE job.id = reservation.job_id AND job.account_id = reservation.account_id
          AND job.kind = 'coverage_update_route' AND job.status = 'running' AND job.cancel_requested_at IS NULL
          AND job.worker_id = owner AND job.lease_token = lease AND job.lease_expires_at >= clock_timestamp()
    ) THEN RETURN NULL; END IF;
    SELECT reader_limit INTO backend_limit FROM app.matcher_slot_limits WHERE singleton;
    SELECT candidate.key INTO chosen FROM unnest(eligible_backend_keys) candidate(key)
    LEFT JOIN app.matcher_slots s ON s.reader_cluster_key = cluster_key AND s.reader_backend_key = candidate.key
        AND s.expires_at >= clock_timestamp() AND s.slot_token <> slot
    GROUP BY candidate.key HAVING count(s.slot_token) < backend_limit
    ORDER BY count(s.slot_token), md5(candidate.key || slot::text) LIMIT 1;
    IF chosen IS NULL THEN RETURN NULL; END IF;
    UPDATE app.matcher_slots s SET reader_cluster_key = cluster_key, reader_backend_key = chosen
     WHERE s.slot_token = slot AND s.account_id = app.current_account_id()
       AND s.owner_id = owner AND s.lease_token = lease AND s.expires_at >= clock_timestamp();
    IF NOT FOUND THEN RETURN NULL; END IF;
    RETURN chosen;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.clear_matcher_reader (slot uuid, owner text, lease uuid)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
BEGIN
    PERFORM 1 FROM app.matcher_slot_guard WHERE singleton FOR UPDATE;
    PERFORM 1 FROM app.matcher_slots s
     WHERE s.slot_token = slot AND s.account_id = app.current_account_id()
       AND s.owner_id = owner AND s.lease_token = lease FOR UPDATE;
    IF NOT FOUND THEN RETURN false; END IF;
    -- Physical cleanup remains valid after terminal job transitions. Keep admission.
    UPDATE app.matcher_slots s SET reader_cluster_key = NULL, reader_backend_key = NULL
     WHERE s.slot_token = slot AND s.account_id = app.current_account_id()
       AND s.owner_id = owner AND s.lease_token = lease AND s.expires_at >= clock_timestamp();
    RETURN FOUND;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.heartbeat_diagnostic_matcher_slot (
    slot uuid, owner text, lease uuid, slot_duration interval
) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
BEGIN
    IF slot IS NULL OR owner IS NULL OR length(owner) NOT BETWEEN 1 AND 200 OR lease IS NULL
       OR slot_duration IS NULL OR slot_duration < interval '1 second' OR slot_duration > interval '2 minutes' THEN
        RAISE EXCEPTION 'invalid diagnostic matcher heartbeat arguments' USING ERRCODE = '22023';
    END IF;
    PERFORM 1 FROM app.accounts account
     WHERE account.id = app.current_account_id() AND account.state = 'active' FOR SHARE;
    IF NOT FOUND THEN RETURN false; END IF;
    PERFORM 1 FROM app.matcher_slot_guard WHERE singleton FOR UPDATE;
    PERFORM 1 FROM app.matcher_slots s
     WHERE s.slot_token = slot AND s.account_id = app.current_account_id() AND s.slot_kind = 'diagnostic'
       AND s.owner_id = owner AND s.lease_token = lease FOR UPDATE;
    IF NOT FOUND THEN RETURN false; END IF;
    -- Evaluate expiry after the guard and slot locks; never resurrect a lease.
    UPDATE app.matcher_slots s SET expires_at = clock_timestamp() + slot_duration
     WHERE s.slot_token = slot AND s.account_id = app.current_account_id() AND s.slot_kind = 'diagnostic'
       AND s.owner_id = owner AND s.lease_token = lease AND s.expires_at >= clock_timestamp();
    RETURN FOUND;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.acquire_coverage_matcher_slot (
    target_job_id uuid, claiming_worker text, current_lease_token uuid, new_slot_token uuid
) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
DECLARE target_account uuid; regions text[]; limits app.matcher_slot_limits%ROWTYPE; job_expiry timestamptz;
BEGIN
    IF claiming_worker IS NULL OR length(claiming_worker) NOT BETWEEN 1 AND 200
       OR current_lease_token IS NULL OR new_slot_token IS NULL THEN
        RAISE EXCEPTION 'invalid matcher slot arguments' USING ERRCODE = '22023';
    END IF;
    PERFORM 1 FROM app.accounts account
     WHERE account.id = app.current_account_id() AND account.state = 'active' FOR SHARE;
    IF NOT FOUND THEN RETURN false; END IF;
    SELECT job.account_id, ARRAY(SELECT value->> 'regionId' FROM jsonb_array_elements(context.target_generations) value ORDER BY value->> 'regionId')
      INTO target_account, regions FROM app.jobs job
      JOIN app.coverage_route_job_contexts context ON context.job_id = job.id AND context.account_id = job.account_id
     WHERE job.id = target_job_id AND job.account_id = app.current_account_id() AND job.kind = 'coverage_update_route'
       AND job.status = 'running' AND job.cancel_requested_at IS NULL
       AND job.worker_id = claiming_worker AND job.lease_token = current_lease_token
       AND job.lease_expires_at >= clock_timestamp() FOR UPDATE OF job;
    IF NOT FOUND OR cardinality(regions) = 0 OR EXISTS (SELECT 1 FROM unnest(regions) region WHERE region IS NULL) THEN RETURN false; END IF;
    PERFORM 1 FROM app.matcher_slot_guard WHERE singleton FOR UPDATE;
    SELECT job.lease_expires_at INTO job_expiry FROM app.jobs job
     WHERE job.id = target_job_id AND job.account_id = target_account AND job.status = 'running'
       AND job.cancel_requested_at IS NULL AND job.worker_id = claiming_worker AND job.lease_token = current_lease_token
       AND job.lease_expires_at >= clock_timestamp();
    IF NOT FOUND THEN RETURN false; END IF;
    -- A terminal job can still own an open OSM snapshot. Only expiry or explicit
    -- release retires its reservation, including reservations without assignments.
    DELETE FROM app.matcher_slots s WHERE s.slot_token IN (
        SELECT expired.slot_token FROM app.matcher_slots expired
         WHERE expired.expires_at < clock_timestamp() FOR UPDATE SKIP LOCKED
    );
    SELECT * INTO limits FROM app.matcher_slot_limits WHERE singleton;
    IF (SELECT count(*) FROM app.matcher_slots) >= limits.global_limit OR
       (SELECT count(*) FROM app.matcher_slots WHERE account_id = target_account) >= limits.account_limit OR
       EXISTS (SELECT 1 FROM unnest(regions) region WHERE
          (SELECT count(*) FROM app.matcher_slots s WHERE s.region_ids && ARRAY[region]) >= limits.region_limit) THEN RETURN false; END IF;
    IF job_expiry < clock_timestamp() THEN RETURN false; END IF;
    INSERT INTO app.matcher_slots(slot_token, slot_kind, account_id, job_id, owner_id, lease_token, region_ids, expires_at)
    VALUES(new_slot_token, 'production', target_account, target_job_id, claiming_worker, current_lease_token, regions, job_expiry);
    RETURN true;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.acquire_diagnostic_matcher_slot (
    target_account_id uuid, owner_id text, new_slot_token uuid, request_token uuid,
    requested_regions text[], slot_duration interval
) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
DECLARE limits app.matcher_slot_limits%ROWTYPE;
BEGIN
    IF target_account_id IS NULL OR target_account_id IS DISTINCT FROM app.current_account_id()
       OR owner_id IS NULL OR length(owner_id) NOT BETWEEN 1 AND 200 OR new_slot_token IS NULL OR request_token IS NULL
       OR requested_regions IS NULL OR cardinality(requested_regions) NOT BETWEEN 0 AND 256
       OR slot_duration IS NULL OR slot_duration < interval '1 second' OR slot_duration > interval '2 minutes'
       OR EXISTS (SELECT 1 FROM unnest(requested_regions) region WHERE region IS NULL OR region !~ '^[a-z][a-z0-9-]{0,63}:[a-z][a-z0-9-]{0,63}$') THEN
        RAISE EXCEPTION 'invalid diagnostic matcher slot arguments' USING ERRCODE = '22023';
    END IF;
    PERFORM 1 FROM app.accounts account
     WHERE account.id = target_account_id AND account.state = 'active' FOR SHARE;
    IF NOT FOUND THEN RETURN false; END IF;
    PERFORM 1 FROM app.matcher_slot_guard WHERE singleton FOR UPDATE;
    DELETE FROM app.matcher_slots s WHERE s.slot_token IN (
        SELECT expired.slot_token FROM app.matcher_slots expired
         WHERE expired.expires_at < clock_timestamp() FOR UPDATE SKIP LOCKED
    );
    SELECT * INTO limits FROM app.matcher_slot_limits WHERE singleton;
    IF (SELECT count(*) FROM app.matcher_slots) >= limits.global_limit OR
       (SELECT count(*) FROM app.matcher_slots WHERE account_id = target_account_id) >= limits.account_limit OR
       EXISTS (SELECT 1 FROM unnest(requested_regions) region WHERE
          (SELECT count(*) FROM app.matcher_slots s WHERE s.region_ids && ARRAY[region]) >= limits.region_limit) THEN RETURN false; END IF;
    INSERT INTO app.matcher_slots(slot_token, slot_kind, account_id, owner_id, lease_token, region_ids, expires_at)
    VALUES(new_slot_token, 'diagnostic', target_account_id, owner_id, request_token, requested_regions, clock_timestamp() + slot_duration);
    RETURN true;
END;
$function$;
-- +goose StatementEnd

-- A cancellation observation confers no completion authority. The caller still
-- finishes with the same fenced lease; missing, stale, or terminal scope is NULL.
-- +goose StatementBegin
CREATE FUNCTION app.job_cancel_requested (job uuid, owner text, lease uuid)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
    SELECT child.cancel_requested_at IS NOT NULL OR parent.cancel_requested_at IS NOT NULL
      FROM app.jobs child
      JOIN app.jobs parent ON parent.id = child.parent_job_id AND parent.account_id = child.account_id
      JOIN app.accounts account ON account.id = child.account_id AND account.state = 'active'
     WHERE job IS NOT NULL AND owner IS NOT NULL AND length(owner) BETWEEN 1 AND 200 AND lease IS NOT NULL
       AND child.id = job AND child.account_id = app.current_account_id()
       AND child.kind = 'coverage_update_route' AND child.status = 'running'
       AND child.worker_id = owner AND child.lease_token = lease AND child.lease_expires_at >= clock_timestamp()
       AND parent.kind = 'coverage_update' AND parent.status IN ('queued', 'running')
$function$;
-- +goose StatementEnd

GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.assign_matcher_reader (uuid, text, uuid, text, text[]) OWNER TO workouts_security_owner;
ALTER FUNCTION app.clear_matcher_reader (uuid, text, uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.heartbeat_diagnostic_matcher_slot (uuid, text, uuid, interval) OWNER TO workouts_security_owner;
ALTER FUNCTION app.job_cancel_requested (uuid, text, uuid) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
REVOKE ALL ON FUNCTION app.assign_matcher_reader (uuid, text, uuid, text, text[]),
app.clear_matcher_reader (uuid, text, uuid)
FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;
GRANT EXECUTE ON FUNCTION app.assign_matcher_reader (uuid, text, uuid, text, text[]),
app.clear_matcher_reader (uuid, text, uuid) TO workouts_api, workouts_coverage_worker;
REVOKE ALL ON FUNCTION app.heartbeat_diagnostic_matcher_slot (uuid, text, uuid, interval)
FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;
GRANT EXECUTE ON FUNCTION app.heartbeat_diagnostic_matcher_slot (uuid, text, uuid, interval) TO workouts_api;
REVOKE ALL ON FUNCTION app.job_cancel_requested (uuid, text, uuid)
FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;
GRANT EXECUTE ON FUNCTION app.job_cancel_requested (uuid, text, uuid) TO workouts_coverage_worker;
REVOKE ALL ON app.matcher_slots, app.matcher_slot_limits, app.matcher_slot_guard
FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;

-- Preserve the shipped claim implementation and ACLs, changing only its floor.
-- Older coverage workers must stop claiming even if they ignore readiness.
-- +goose StatementBegin
DO $block$
DECLARE definition text; anchor_count integer;
DECLARE floor_pattern text := $pattern$(?i)\mIF[[:space:]]+runtime_version[[:space:]]*<[[:space:]]*15[[:space:]]+THEN\M$pattern$;
BEGIN
    definition := pg_get_functiondef('app.claim_next_coverage_route(text,uuid,interval,integer)'::regprocedure);
    SELECT count(*) INTO anchor_count FROM regexp_matches(definition, floor_pattern, 'g');
    IF anchor_count <> 1 THEN
        RAISE EXCEPTION 'unexpected coverage claim definition: runtime floor anchors % (expected one)', anchor_count USING ERRCODE = '55000';
    END IF;
    definition := regexp_replace(definition, floor_pattern, 'IF runtime_version IS NULL OR runtime_version < 23 THEN');
    definition := replace(definition, 'coverage worker runtime version 15 or newer is required', 'coverage worker runtime version 23 or newer is required');
    EXECUTE definition;
END;
$block$;
-- +goose StatementEnd

-- Coverage completion derives the parent status. Lock that parent before its
-- child/context, matching parent cancellation and avoiding a child -> parent cycle.
-- Fence cancellation before payload writes as well as after every lock wait.
-- Preserve the shipped persistence payload logic and rejection return values.
-- +goose StatementBegin
DO $block$
DECLARE signature text; definition text; function_name text; rejected text; fence text;
DECLARE select_count integer; rejection_count integer; rejection_pattern text;
DECLARE select_pattern text := $pattern$(?i)\mSELECT[[:space:]]+job[[:space:]]*\.[[:space:]]*account_id[[:space:]]*,[[:space:]]*context[[:space:]]*\.[[:space:]]*workout_id[[:space:]]*,[[:space:]]*context[[:space:]]*\.[[:space:]]*route_input_revision[[:space:]]*,[[:space:]]*context[[:space:]]*\.[[:space:]]*route_input_sha256[[:space:]]*,$pattern$;
DECLARE rejection_template text := $pattern$(?i)\mIF[[:space:]]+NOT[[:space:]]+FOUND[[:space:]]+THEN[[:space:]]+RETURN[[:space:]]+%s\M[[:space:]]*;[[:space:]]*END[[:space:]]+IF[[:space:]]*;$pattern$;
BEGIN
    FOREACH signature IN ARRAY ARRAY[
        'app.persist_coverage_route(uuid,text,uuid,integer,jsonb,jsonb)',
        'app.finish_coverage_route(uuid,text,uuid,text,integer,text,text)'
    ] LOOP
        definition := pg_get_functiondef(signature::regprocedure);
        function_name := CASE WHEN signature LIKE 'app.persist_%' THEN 'persist_coverage_route' ELSE 'finish_coverage_route' END;
        rejected := CASE WHEN function_name = 'persist_coverage_route' THEN 'NULL' ELSE 'false' END;
        rejection_pattern := format(rejection_template, rejected);
        SELECT count(*) INTO select_count FROM regexp_matches(definition, select_pattern, 'g');
        SELECT count(*) INTO rejection_count FROM regexp_matches(definition, rejection_pattern, 'g');
        IF select_count <> 1 OR rejection_count <> 1 THEN
            RAISE EXCEPTION 'unexpected coverage completion definition for %: select anchors %, rejection anchors % (expected one each)',
                signature, select_count, rejection_count USING ERRCODE = '55000';
        END IF;
        -- Cancellation can commit while waiting for the parent or child/context.
        -- Recheck both flags in a fresh statement before any evidence/state writes.
        fence := format('
            IF NOT EXISTS (SELECT 1 FROM app.jobs job JOIN app.jobs parent
                ON parent.id = job.parent_job_id AND parent.account_id = job.account_id
                WHERE job.id = %s.target_job_id AND job.account_id = app.current_account_id()
                  AND job.status = ''running'' AND job.worker_id = claiming_worker
                  AND job.lease_token = current_lease_token AND job.lease_expires_at >= clock_timestamp()
                  AND job.cancel_requested_at IS NULL AND parent.cancel_requested_at IS NULL
                  AND parent.kind = ''coverage_update'' AND parent.status IN (''queued'', ''running''))
            THEN RETURN %s; END IF;', function_name, rejected);
        -- Patch the original rejection before introducing another IF NOT FOUND.
        -- Backreferences retain the original SELECT/rejection spelling verbatim;
        -- no whitespace normalization is applied to SQL or its string literals.
        definition := regexp_replace(definition, rejection_pattern, E'\\&' || fence);
        definition := regexp_replace(definition, select_pattern, format(
            '    PERFORM 1 FROM app.jobs parent JOIN app.jobs child ON parent.id = child.parent_job_id AND parent.account_id = child.account_id
             WHERE child.id = %s.target_job_id AND child.account_id = app.current_account_id()
               AND child.kind = ''coverage_update_route'' AND child.cancel_requested_at IS NULL
               AND parent.kind = ''coverage_update'' AND parent.status IN (''queued'', ''running'')
               AND parent.cancel_requested_at IS NULL FOR UPDATE OF parent;
             IF NOT FOUND THEN RETURN %s; END IF;
%s
', function_name, rejected, fence) || E'\\&');
        EXECUTE definition;
    END LOOP;
END;
$block$;
-- +goose StatementEnd

-- Account/job deletion still cascades reservations; trusted shutdown must close
-- in-flight snapshots. Inactive accounts cannot acquire or assign new readers.
UPDATE app.schema_metadata SET schema_version = 23, minimum_runtime_version = 23 WHERE singleton;

-- +goose Down
-- +goose StatementBegin
DO $block$
BEGIN
    RAISE EXCEPTION 'coordinated coverage readers are forward-only' USING ERRCODE = '55000';
END;
$block$;
-- +goose StatementEnd
