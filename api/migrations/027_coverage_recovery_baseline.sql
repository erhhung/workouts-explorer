-- +goose Up
-- +goose StatementBegin
-- coverage-contract:route-counts:begin
ALTER TABLE app.workout_import_events ADD COLUMN has_route boolean;
ALTER TABLE app.job_progress
    ADD COLUMN workouts_created_with_route bigint CHECK (workouts_created_with_route BETWEEN 0 AND workouts_created),
    ADD COLUMN workouts_updated_with_route bigint CHECK (workouts_updated_with_route BETWEEN 0 AND workouts_updated);

-- Only identical retained provider content proves the historical route snapshot.
-- Disable just the two write guards, transactionally, for this one-time backfill.
ALTER TABLE app.workout_import_events NO FORCE ROW LEVEL SECURITY;
ALTER TABLE app.workouts NO FORCE ROW LEVEL SECURITY;
ALTER TABLE app.workout_route_points NO FORCE ROW LEVEL SECURITY;
ALTER TABLE app.jobs NO FORCE ROW LEVEL SECURITY;
ALTER TABLE app.job_progress NO FORCE ROW LEVEL SECURITY;
ALTER TABLE app.workout_import_events DISABLE TRIGGER workout_import_events_append_only;
ALTER TABLE app.workout_import_events DISABLE TRIGGER workout_import_events_capability_before_write;
UPDATE app.workout_import_events event SET has_route = EXISTS (
    SELECT 1 FROM app.workout_route_points point
     WHERE point.account_id = workout.account_id AND point.workout_id = workout.id
)
FROM app.workouts workout
WHERE workout.id = event.workout_id AND workout.account_id = event.account_id
  AND workout.content_sha256 = event.content_sha256
  AND workout.deletion_requested_at IS NULL
  AND event.kind IN ('created','updated','adopted','matched_unchanged');
ALTER TABLE app.workout_import_events ENABLE TRIGGER workout_import_events_capability_before_write;
ALTER TABLE app.workout_import_events ENABLE TRIGGER workout_import_events_append_only;

-- Internal only. Call under the existing source/parent/child progress locks.
-- Counts are events, not distinct workout IDs; retained totals can outlive events.
CREATE FUNCTION app.refresh_ingest_route_counts(target_job_id uuid) RETURNS void
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
DECLARE target_parent uuid; target_account uuid;
BEGIN
    SELECT parent_job_id,account_id INTO target_parent,target_account FROM app.jobs
     WHERE id = target_job_id AND kind IN ('manual_ingest_source','scheduled_ingest_source');
    IF NOT FOUND THEN RETURN; END IF;
    UPDATE app.job_progress progress SET
        workouts_created_with_route = CASE
            WHEN progress.workouts_created = events.known_created AND events.created = events.known_created THEN events.routed_created END,
        workouts_updated_with_route = CASE
            WHEN progress.workouts_updated = events.known_updated AND events.updated = events.known_updated THEN events.routed_updated END
    FROM (SELECT
        count(*) FILTER (WHERE kind = 'created') created,
        count(*) FILTER (WHERE kind IN ('updated','adopted')) updated,
        count(*) FILTER (WHERE kind = 'created' AND has_route IS NOT NULL) known_created,
        count(*) FILTER (WHERE kind IN ('updated','adopted') AND has_route IS NOT NULL) known_updated,
        count(*) FILTER (WHERE kind = 'created' AND has_route) routed_created,
        count(*) FILTER (WHERE kind IN ('updated','adopted') AND has_route) routed_updated
        FROM app.workout_import_events WHERE job_id = target_job_id AND account_id = target_account) events
    WHERE progress.job_id = target_job_id AND progress.account_id = target_account;

    UPDATE app.job_progress parent SET
        workouts_created_with_route = CASE WHEN children.created = parent.workouts_created AND children.created_known
            THEN children.routed_created END,
        workouts_updated_with_route = CASE WHEN children.updated = parent.workouts_updated AND children.updated_known
            THEN children.routed_updated END
    FROM (SELECT COALESCE(sum(progress.workouts_created),0) created, COALESCE(sum(progress.workouts_updated),0) updated,
        COALESCE(bool_and(COALESCE(progress.workouts_created,0) = 0 OR progress.workouts_created_with_route IS NOT NULL),true) created_known,
        COALESCE(bool_and(COALESCE(progress.workouts_updated,0) = 0 OR progress.workouts_updated_with_route IS NOT NULL),true) updated_known,
        COALESCE(sum(progress.workouts_created_with_route),0) routed_created,
        COALESCE(sum(progress.workouts_updated_with_route),0) routed_updated
        FROM app.jobs child LEFT JOIN app.job_progress progress ON progress.job_id = child.id AND progress.account_id = child.account_id
        WHERE child.parent_job_id = target_parent AND child.account_id = target_account) children
    WHERE parent.job_id = target_parent AND parent.account_id = target_account;
END;
$function$;

-- Keep the schema-26 signature, including its fencing and lock order. Its callers
-- omit has_route, so subsequent unknown events must invalidate the route suffix.
DO $block$
DECLARE definition text; anchor text := '    RETURN true;'; anchor_count integer;
BEGIN
    definition := pg_get_functiondef('app.record_ingest_progress(uuid,text,uuid,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint)'::regprocedure);
    SELECT count(*) INTO anchor_count FROM regexp_matches(definition,anchor,'g');
    IF anchor_count <> 1 THEN RAISE EXCEPTION 'ingest progress return anchor must occur exactly once (found %)',anchor_count; END IF;
    EXECUTE replace(definition,anchor,'    PERFORM app.refresh_ingest_route_counts(target_job_id);' || chr(10) || anchor);
END;
$block$;

CREATE FUNCTION app.record_ingest_progress(
    target_job_id uuid, claiming_worker text, current_lease_token uuid,
    new_files_discovered bigint, new_files_skipped bigint, new_files_succeeded bigint, new_files_failed bigint,
    new_workouts_created bigint, new_workouts_updated bigint, new_workouts_unchanged bigint, new_workouts_rejected bigint,
    new_workouts_created_with_route bigint, new_workouts_updated_with_route bigint
) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
BEGIN
    IF NOT app.record_ingest_progress(target_job_id,claiming_worker,current_lease_token,
        new_files_discovered,new_files_skipped,new_files_succeeded,new_files_failed,
        new_workouts_created,new_workouts_updated,new_workouts_unchanged,new_workouts_rejected) THEN RETURN false; END IF;
    IF NOT EXISTS (SELECT 1 FROM app.job_progress WHERE job_id = target_job_id AND account_id = app.current_account_id()
        AND workouts_created_with_route IS NOT DISTINCT FROM new_workouts_created_with_route
        AND workouts_updated_with_route IS NOT DISTINCT FROM new_workouts_updated_with_route) THEN
        RAISE EXCEPTION 'route progress must match durable import events' USING ERRCODE = '22023';
    END IF;
    RETURN true;
END;
$function$;

-- Run as migration owner before restricting execution; no application ACL expands.
SELECT app.refresh_ingest_route_counts(id) FROM app.jobs WHERE kind IN ('manual_ingest_source','scheduled_ingest_source');
-- Parent-only retained history has no provable children; genuine empty totals are known.
UPDATE app.job_progress SET workouts_created_with_route = 0 WHERE workouts_created = 0;
UPDATE app.job_progress SET workouts_updated_with_route = 0 WHERE workouts_updated = 0;
ALTER TABLE app.job_progress ALTER COLUMN workouts_created_with_route SET DEFAULT 0;
ALTER TABLE app.job_progress ALTER COLUMN workouts_updated_with_route SET DEFAULT 0;
ALTER TABLE app.workout_import_events FORCE ROW LEVEL SECURITY;
ALTER TABLE app.workouts FORCE ROW LEVEL SECURITY;
ALTER TABLE app.workout_route_points FORCE ROW LEVEL SECURITY;
ALTER TABLE app.jobs FORCE ROW LEVEL SECURITY;
ALTER TABLE app.job_progress FORCE ROW LEVEL SECURITY;

GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.refresh_ingest_route_counts(uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.record_ingest_progress(uuid,text,uuid,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
REVOKE ALL ON FUNCTION app.refresh_ingest_route_counts(uuid),
    app.record_ingest_progress(uuid,text,uuid,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint)
    FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;
GRANT EXECUTE ON FUNCTION app.record_ingest_progress(uuid,text,uuid,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint) TO workouts_worker;
GRANT SELECT(has_route) ON app.workout_import_events TO workouts_worker, workouts_security_owner;
GRANT SELECT(workouts_created_with_route,workouts_updated_with_route) ON app.job_progress TO workouts_worker, workouts_api;
GRANT SELECT(workouts_created_with_route,workouts_updated_with_route),
    UPDATE(workouts_created_with_route,workouts_updated_with_route) ON app.job_progress TO workouts_security_owner;

-- coverage-contract:route-counts:end

-- coverage-contract:auto-retries:begin
-- Existing jobs use the shipped v1 matcher. New parents snapshot the observed
-- matcher contract, independently of reconciliation revisions/OSM event IDs.
CREATE FUNCTION app.current_coverage_matcher_contract() RETURNS text LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
    SELECT COALESCE(NULLIF(matcher_contract_version,''),'coverage-production-v1')
    FROM app.coverage_reconciliation_state WHERE singleton
$function$;
ALTER TABLE app.coverage_job_contexts ADD COLUMN matcher_contract_version text NOT NULL DEFAULT 'coverage-production-v1'
CHECK (matcher_contract_version ~ '^coverage-production-v[1-9][0-9]*$');
ALTER TABLE app.coverage_job_contexts ALTER COLUMN matcher_contract_version SET DEFAULT app.current_coverage_matcher_contract();

-- Enrollment is event-driven, not a backfill of historical terminal jobs.
CREATE TABLE app.coverage_retry_roots (
    root_job_id uuid PRIMARY KEY REFERENCES app.jobs(id) ON DELETE CASCADE,
    account_id uuid NOT NULL REFERENCES app.accounts(id) ON DELETE CASCADE,
    enrolled_at timestamptz,
    automatic_attempts_used integer NOT NULL DEFAULT 0 CHECK (automatic_attempts_used BETWEEN 0 AND 20)
);
CREATE INDEX coverage_retry_roots_budget_idx ON app.coverage_retry_roots(automatic_attempts_used,root_job_id)
WHERE enrolled_at IS NOT NULL;
CREATE TABLE app.coverage_retry_attempts (
    job_id uuid PRIMARY KEY REFERENCES app.jobs(id) ON DELETE CASCADE,
    root_job_id uuid NOT NULL REFERENCES app.coverage_retry_roots(root_job_id) ON DELETE CASCADE,
    predecessor_job_id uuid REFERENCES app.jobs(id) ON DELETE CASCADE,
    origin text NOT NULL CHECK (origin IN ('observed', 'manual', 'automatic')),
    automatic_ordinal integer,
    CHECK ((origin = 'automatic' AND automatic_ordinal IS NOT NULL AND automatic_ordinal BETWEEN 1 AND 20) OR
           (origin <> 'automatic' AND automatic_ordinal IS NULL)),
    CHECK (origin = 'observed' OR predecessor_job_id IS NOT NULL),
    UNIQUE (root_job_id, automatic_ordinal)
);
CREATE UNIQUE INDEX coverage_retry_attempts_predecessor_idx
ON app.coverage_retry_attempts(predecessor_job_id) WHERE origin <> 'observed';
ALTER TABLE app.coverage_retry_roots ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.coverage_retry_roots FORCE ROW LEVEL SECURITY;
ALTER TABLE app.coverage_retry_attempts ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.coverage_retry_attempts FORCE ROW LEVEL SECURITY;
CREATE POLICY coverage_retry_roots_owner_policy ON app.coverage_retry_roots
TO workouts_security_owner USING (true) WITH CHECK (true);
CREATE POLICY coverage_retry_attempts_owner_policy ON app.coverage_retry_attempts
TO workouts_security_owner USING (true) WITH CHECK (true);
REVOKE ALL ON app.coverage_retry_roots, app.coverage_retry_attempts
FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;

CREATE FUNCTION app.coverage_retry_root(target_job_id uuid)
RETURNS TABLE(root_job_id uuid, depth integer) LANGUAGE sql SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
    WITH RECURSIVE lineage AS (
        SELECT id, account_id, retry_of_job_id, 1 AS ordinal FROM app.jobs WHERE id = target_job_id AND kind = 'coverage_update'
        UNION ALL
        SELECT prior.id, prior.account_id, prior.retry_of_job_id, lineage.ordinal + 1 FROM lineage
        JOIN app.jobs prior ON prior.id = lineage.retry_of_job_id AND prior.account_id = lineage.account_id
        WHERE lineage.ordinal < 100
    ) SELECT id, ordinal FROM lineage ORDER BY ordinal DESC LIMIT 1
$function$;

CREATE FUNCTION app.enroll_coverage_retry_failure() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
DECLARE root_id uuid;
BEGIN
    SELECT root_job_id INTO root_id FROM app.coverage_retry_root(NEW.id);
    INSERT INTO app.coverage_retry_roots(root_job_id,account_id,enrolled_at)
    VALUES(root_id,NEW.account_id,clock_timestamp())
    ON CONFLICT (root_job_id) DO UPDATE SET enrolled_at = COALESCE(coverage_retry_roots.enrolled_at,EXCLUDED.enrolled_at);
    INSERT INTO app.coverage_retry_attempts(job_id,root_job_id,predecessor_job_id,origin)
    VALUES(NEW.id,root_id,NEW.retry_of_job_id,'observed') ON CONFLICT (job_id) DO NOTHING;
    RETURN NEW;
END;
$function$;
CREATE TRIGGER coverage_retry_failure_enrollment AFTER UPDATE OF status ON app.jobs
FOR EACH ROW WHEN (NEW.kind = 'coverage_update' AND NEW.status IN ('failed','partially_succeeded') AND OLD.status IS DISTINCT FROM NEW.status)
EXECUTE FUNCTION app.enroll_coverage_retry_failure();

-- Explicit manual retry can reverse cancellation, but never changed input or
-- replaced authority. Automatic retry applies its separate cancellation gate.
CREATE FUNCTION app.coverage_retry_targets(prior_job_id uuid, target_account_id uuid)
RETURNS TABLE(child_id uuid, workout_id uuid, route_revision bigint, route_digest bytea, generations jsonb, timeout_count integer)
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
    SELECT child.id, context.workout_id, state.route_input_revision, state.route_input_sha256, context.target_generations,
        CASE WHEN child.failure_code = 'coverage-route-timeout' THEN LEAST(context.timeout_retry_count + 1,16) ELSE 0 END
    FROM app.jobs child
    JOIN app.coverage_route_job_contexts context ON context.job_id = child.id AND context.account_id = child.account_id
    JOIN app.workout_coverage_states state ON state.workout_id = context.workout_id AND state.account_id = context.account_id
    JOIN app.workouts workout ON workout.id = state.workout_id AND workout.account_id = state.account_id
    JOIN app.sources source ON source.id = workout.source_id AND source.account_id = workout.account_id
    JOIN app.coverage_job_contexts parent_context ON parent_context.job_id = prior_job_id AND parent_context.account_id = child.account_id
    LEFT JOIN LATERAL (
        SELECT jsonb_agg(jsonb_build_object('regionId',region.region_id,'generation',region.desired_osm_generation) ORDER BY region.region_id) AS generations
        FROM app.workout_coverage_regions region WHERE region.workout_id = context.workout_id AND region.account_id = context.account_id
    ) desired ON true
    WHERE child.parent_job_id = prior_job_id AND child.account_id = target_account_id
      AND child.kind = 'coverage_update_route' AND child.status IN ('failed','cancelled')
      AND state.readiness_state = 'map_data_ready'
      AND state.route_input_revision = context.route_input_revision AND state.route_input_sha256 = context.route_input_sha256
      AND parent_context.matcher_contract_version = app.current_coverage_matcher_contract()
      AND workout.deletion_requested_at IS NULL AND source.deleted_at IS NULL
      AND (
          (state.target_job_id = child.id
           AND ((child.status = 'failed' AND state.processing_state = 'failed')
                OR (child.status = 'cancelled' AND state.processing_state IN ('not_started','stale')))
           AND state.target_revision = context.route_input_revision
           AND state.target_rules_version = parent_context.rules_version AND state.target_sampling_version = parent_context.sampling_version
           AND state.target_path_policy_version = parent_context.path_policy_version
           AND app.coverage_generation_vectors_equal(state.target_generations,context.target_generations))
          OR
          -- A cleared pointer is not authority by itself. Require the exact
          -- cancellation footprint, current generation proof and no later work.
          (child.status = 'cancelled' AND state.target_job_id IS NULL AND state.processing_state IN ('not_started','stale')
           AND state.processing_finished_at = child.terminal_at
           AND (state.target_revision IS NULL OR state.target_revision = context.route_input_revision)
           AND (state.target_rules_version IS NULL OR state.target_rules_version = parent_context.rules_version)
           AND (state.target_sampling_version IS NULL OR state.target_sampling_version = parent_context.sampling_version)
           AND (state.target_path_policy_version IS NULL OR state.target_path_policy_version = parent_context.path_policy_version)
           AND ((state.target_generations IS NOT NULL AND app.coverage_generation_vectors_equal(state.target_generations,context.target_generations))
                OR (state.target_generations IS NULL AND app.coverage_generation_vectors_equal(desired.generations,context.target_generations)))
           AND (desired.generations IS NULL OR app.coverage_generation_vectors_equal(desired.generations,context.target_generations))
           AND NOT EXISTS (
               SELECT 1 FROM app.coverage_route_job_contexts other_context JOIN app.jobs other ON other.id = other_context.job_id AND other.account_id = other_context.account_id
               WHERE other_context.account_id = context.account_id AND other_context.workout_id = context.workout_id AND other.id <> child.id
                 AND (other.status IN ('queued','running') OR other_context.created_at >= context.created_at
                      OR other.terminal_at >= child.created_at)
           ))
      )
    ORDER BY child.created_at, child.id
$function$;

-- Conservative parent-wide policy: retrying just transient siblings would make
-- the remaining permanent failures unreachable through latest-only manual retry.
-- Keep coverage_retry_targets broad; only the automatic path uses this gate.
CREATE FUNCTION app.coverage_auto_retry_parent_eligible(prior_job_id uuid, target_account_id uuid)
RETURNS boolean LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
    WITH children AS MATERIALIZED (
        SELECT child.id,child.account_id,child.kind,child.status,child.failure_code,child.cancel_requested_at,context.job_id AS context_job_id
        FROM app.jobs child LEFT JOIN app.coverage_route_job_contexts context ON context.job_id = child.id AND context.account_id = child.account_id
        WHERE child.parent_job_id = prior_job_id
    ), targets AS MATERIALIZED (
        SELECT child_id FROM app.coverage_retry_targets(prior_job_id,target_account_id)
    )
    SELECT EXISTS (
        SELECT 1 FROM app.jobs parent JOIN app.coverage_job_contexts context ON context.job_id = parent.id AND context.account_id = parent.account_id
        JOIN app.accounts account ON account.id = parent.account_id AND account.state = 'active'
        WHERE parent.id = prior_job_id AND parent.account_id = target_account_id AND parent.kind = 'coverage_update'
          AND parent.status IN ('failed','partially_succeeded') AND parent.cancel_requested_at IS NULL
    ) AND EXISTS (SELECT 1 FROM children WHERE status = 'failed')
      AND NOT EXISTS (SELECT 1 FROM children WHERE account_id IS DISTINCT FROM target_account_id OR kind <> 'coverage_update_route'
          OR status NOT IN ('failed','succeeded') OR cancel_requested_at IS NOT NULL OR context_job_id IS NULL
          OR (status = 'failed' AND (failure_code IS NULL OR failure_code NOT IN ('coverage-route-timeout','coverage-transient-failed'))))
      AND NOT EXISTS (SELECT 1 FROM children WHERE status = 'failed' AND NOT EXISTS (SELECT 1 FROM targets WHERE child_id = children.id))
$function$;

-- No caller grants: wrappers provide either real-principal authentication or a
-- narrowly scoped worker policy, never an impersonated owner/requester.
CREATE FUNCTION app.construct_coverage_retry(prior_job_id uuid, target_account_id uuid, max_ordinal integer, automatic_limit integer)
RETURNS TABLE(job_id uuid, route_count integer) LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
DECLARE prior_parent record; target record; root_id uuid; retry_depth integer; used integer; enrolled timestamptz;
DECLARE new_parent uuid := gen_random_uuid(); new_child uuid; targets uuid[]; child_count integer;
BEGIN
    -- Normal enqueue also locks account first. NOWAIT prevents inversion with
    -- ingest/deletion and bounds each control transaction; a later poll retries.
    PERFORM 1 FROM app.accounts WHERE id = target_account_id AND state = 'active' FOR UPDATE NOWAIT;
    IF NOT FOUND THEN RETURN; END IF;
    SELECT job.*, context.region_id, context.target_osm_generation, context.target_work_revision,
        context.rules_version, context.sampling_version, context.path_policy_version, context.minimum_traversal_meters, context.matcher_contract_version
    INTO prior_parent FROM app.jobs job JOIN app.coverage_job_contexts context ON context.job_id = job.id AND context.account_id = job.account_id
    WHERE job.id = prior_job_id AND job.account_id = target_account_id AND job.kind = 'coverage_update'
      AND job.status IN ('failed','partially_succeeded','cancelled') FOR UPDATE OF job NOWAIT;
    IF NOT FOUND THEN
        IF automatic_limit IS NOT NULL THEN RETURN; END IF;
        RAISE EXCEPTION 'coverage retry requires a terminal parent' USING ERRCODE = '55000';
    END IF;
    IF EXISTS (SELECT 1 FROM app.jobs WHERE retry_of_job_id = prior_job_id AND account_id = target_account_id AND kind = 'coverage_update') THEN
        IF automatic_limit IS NOT NULL THEN RETURN; END IF;
        RAISE EXCEPTION 'only the latest coverage attempt may be retried' USING ERRCODE = '55000';
    END IF;
    IF automatic_limit IS NOT NULL AND (prior_parent.cancel_requested_at IS NOT NULL OR prior_parent.status = 'cancelled') THEN RETURN; END IF;
    SELECT root_job_id, depth INTO root_id, retry_depth FROM app.coverage_retry_root(prior_job_id);
    IF retry_depth >= max_ordinal THEN
        IF automatic_limit IS NOT NULL THEN RETURN; END IF;
        RAISE EXCEPTION 'coverage retry limit reached' USING ERRCODE = '54001';
    END IF;
    IF automatic_limit IS NOT NULL THEN
        SELECT automatic_attempts_used,enrolled_at INTO used,enrolled FROM app.coverage_retry_roots
        WHERE root_job_id = root_id AND account_id = target_account_id FOR UPDATE NOWAIT;
        IF NOT FOUND OR enrolled IS NULL OR used >= automatic_limit OR
           prior_parent.terminal_at > clock_timestamp() - interval '30 seconds' THEN RETURN; END IF;
    END IF;
    SELECT array_agg(child_id) INTO targets FROM app.coverage_retry_targets(prior_job_id,target_account_id);
    IF targets IS NULL THEN RETURN; END IF;
    PERFORM 1 FROM app.workouts workout JOIN app.coverage_route_job_contexts context
      ON context.workout_id = workout.id AND context.account_id = workout.account_id
      WHERE context.job_id = ANY(targets) ORDER BY workout.id FOR UPDATE OF workout NOWAIT;
    PERFORM 1 FROM app.workout_coverage_states state JOIN app.coverage_route_job_contexts context
      ON context.workout_id = state.workout_id AND context.account_id = state.account_id
      WHERE context.job_id = ANY(targets) ORDER BY state.workout_id FOR UPDATE OF state NOWAIT;
    IF automatic_limit IS NOT NULL AND NOT app.coverage_auto_retry_parent_eligible(prior_job_id,target_account_id) THEN RETURN; END IF;
    SELECT count(*)::integer INTO child_count FROM app.coverage_retry_targets(prior_job_id,target_account_id) WHERE child_id = ANY(targets);
    IF child_count = 0 THEN RETURN; END IF;
    INSERT INTO app.coverage_retry_roots(root_job_id,account_id) VALUES(root_id,target_account_id) ON CONFLICT DO NOTHING;
    INSERT INTO app.jobs(id,account_id,kind,priority,parameters,progress_total,retry_of_job_id)
    VALUES(new_parent,target_account_id,'coverage_update',prior_parent.priority,prior_parent.parameters,child_count,prior_job_id);
    INSERT INTO app.coverage_job_contexts(job_id,account_id,region_id,target_osm_generation,target_work_revision,
        rules_version,sampling_version,path_policy_version,minimum_traversal_meters,matcher_contract_version)
    VALUES(new_parent,target_account_id,prior_parent.region_id,prior_parent.target_osm_generation,prior_parent.target_work_revision + 1,
        prior_parent.rules_version,prior_parent.sampling_version,prior_parent.path_policy_version,prior_parent.minimum_traversal_meters,prior_parent.matcher_contract_version);
    FOR target IN SELECT * FROM app.coverage_retry_targets(prior_job_id,target_account_id) WHERE child_id = ANY(targets) LOOP
        new_child := gen_random_uuid();
        INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority,parameters,retry_of_job_id)
        VALUES(new_child,new_parent,target_account_id,'coverage_update_route',prior_parent.priority,'{}',target.child_id);
        INSERT INTO app.coverage_route_job_contexts(job_id,account_id,workout_id,route_input_revision,route_input_sha256,target_generations,timeout_retry_count)
        VALUES(new_child,target_account_id,target.workout_id,target.route_revision,target.route_digest,target.generations,target.timeout_count);
        UPDATE app.workout_coverage_states SET processing_state = 'queued', target_job_id = new_child,
            target_revision = route_input_revision, target_rules_version = prior_parent.rules_version,
            target_sampling_version = prior_parent.sampling_version, target_path_policy_version = prior_parent.path_policy_version,
            target_generations = target.generations, processing_failure_code = NULL, processing_failure_summary = NULL,
            processing_started_at = NULL, processing_finished_at = NULL, updated_at = clock_timestamp()
        WHERE account_id = target_account_id AND workout_id = target.workout_id;
    END LOOP;
    INSERT INTO app.coverage_job_progress(job_id,account_id,routes_total) VALUES(new_parent,target_account_id,child_count);
    IF automatic_limit IS NOT NULL THEN
        UPDATE app.coverage_retry_roots SET automatic_attempts_used = automatic_attempts_used + 1 WHERE root_job_id = root_id
        RETURNING automatic_attempts_used INTO used;
    END IF;
    INSERT INTO app.coverage_retry_attempts(job_id,root_job_id,predecessor_job_id,origin,automatic_ordinal)
    VALUES(new_parent,root_id,prior_job_id,CASE WHEN automatic_limit IS NULL THEN 'manual' ELSE 'automatic' END,
        CASE WHEN automatic_limit IS NOT NULL THEN used END);
    job_id := new_parent; route_count := child_count; RETURN NEXT;
END;
$function$;

-- Preserve the public ABI and its real, active principal authentication.
CREATE OR REPLACE FUNCTION app.retry_coverage_update(prior_job_id uuid, requester_id uuid, max_ordinal integer)
RETURNS TABLE(job_id uuid, route_count integer) LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
DECLARE target_account uuid := app.current_account_id();
BEGIN
    IF target_account IS NULL OR requester_id IS NULL OR max_ordinal IS NULL OR max_ordinal NOT BETWEEN 1 AND 100 OR NOT EXISTS (
        SELECT 1 FROM app.users account_user JOIN app.authentication_principals principal ON principal.id = account_user.principal_id
        WHERE account_user.account_id = target_account AND account_user.principal_id = requester_id AND principal.disabled_at IS NULL
    ) THEN RAISE EXCEPTION 'active account requester and valid retry ordinal are required' USING ERRCODE = '42501'; END IF;
    RETURN QUERY SELECT * FROM app.construct_coverage_retry(prior_job_id,target_account,max_ordinal,NULL);
EXCEPTION WHEN lock_not_available THEN
    -- Keep contention in the public ABI's existing conflict SQLSTATE family.
    RAISE EXCEPTION 'coverage retry target is busy; retry the latest attempt' USING ERRCODE = '55000';
END;
$function$;

-- Global discovery, like claim_next_coverage_route, but only this one bounded
-- policy action is granted. Account RLS routing is not principal impersonation;
-- restore the caller's context and never set a principal/requester identity.
CREATE FUNCTION app.retry_next_coverage_update(automatic_limit integer, runtime_version integer)
RETURNS TABLE(job_id uuid, route_count integer) LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
DECLARE candidate record; created record; prior_account text := current_setting('app.account_id',true);
BEGIN
    IF runtime_version IS NULL OR runtime_version < 26 OR runtime_version < (SELECT minimum_runtime_version FROM app.schema_metadata WHERE singleton)
    THEN RAISE EXCEPTION 'coverage retry runtime version 26 or newer is required' USING ERRCODE = '55000'; END IF;
    IF automatic_limit IS NULL OR automatic_limit NOT BETWEEN 0 AND 20
    THEN RAISE EXCEPTION 'automatic retry count must be between 0 and 20' USING ERRCODE = '22023'; END IF;
    IF automatic_limit = 0 THEN RETURN; END IF;
    FOR candidate IN
        SELECT job.id,job.account_id FROM app.coverage_retry_roots root
        JOIN app.coverage_retry_attempts attempt ON attempt.root_job_id = root.root_job_id
        JOIN app.jobs job ON job.id = attempt.job_id AND job.account_id = root.account_id
        JOIN app.accounts account ON account.id = job.account_id AND account.state = 'active'
        WHERE root.enrolled_at IS NOT NULL AND root.automatic_attempts_used < automatic_limit
          AND job.status IN ('failed','partially_succeeded') AND job.cancel_requested_at IS NULL
          AND job.terminal_at <= clock_timestamp() - interval '30 seconds'
          AND NOT EXISTS (SELECT 1 FROM app.jobs retry WHERE retry.retry_of_job_id = job.id AND retry.account_id = job.account_id AND retry.kind = 'coverage_update')
          AND EXISTS (SELECT 1 FROM app.coverage_retry_targets(job.id,job.account_id))
          AND app.coverage_auto_retry_parent_eligible(job.id,job.account_id)
        ORDER BY job.terminal_at,job.id LIMIT 32
    LOOP
        BEGIN
            PERFORM set_config('app.account_id',candidate.account_id::text,true);
            SELECT * INTO created FROM app.construct_coverage_retry(candidate.id,candidate.account_id,100,automatic_limit);
            IF FOUND THEN
                PERFORM set_config('app.account_id',COALESCE(prior_account,''),true);
                job_id := created.job_id; route_count := created.route_count; RETURN NEXT; RETURN;
            END IF;
            PERFORM set_config('app.account_id',COALESCE(prior_account,''),true);
        EXCEPTION WHEN lock_not_available THEN
            -- Roll back this candidate's locks/writes and try a different root.
            NULL;
        END;
    END LOOP;
END;
$function$;

-- Retry parents are dedicated attempts, never normal enqueue/coalescing buckets.
DO $block$
DECLARE definition text; anchor text; anchor_pattern text; anchor_count integer;
BEGIN
    definition := pg_get_functiondef('app.enqueue_coverage_update(uuid,uuid,text,bigint,bigint,text,text,text,double precision,jsonb)'::regprocedure);
    anchor_pattern := $pattern$\mAND[[:space:]]+job[[:space:]]*\.[[:space:]]*status[[:space:]]*=[[:space:]]*'queued'[[:space:]]+ORDER[[:space:]]+BY[[:space:]]+job[[:space:]]*\.[[:space:]]*created_at\M$pattern$;
    SELECT count(*),min(parts[1]) INTO anchor_count,anchor FROM regexp_matches(definition,anchor_pattern,'gi') AS matched(parts);
    -- Extract the matched text, then replace literally: no replacement-string
    -- backreferences, and no unintended second occurrence outside the SQL node.
    IF anchor_count <> 1 OR (length(definition) - length(replace(definition,anchor,''))) / length(anchor) <> 1
    THEN RAISE EXCEPTION 'coverage enqueue isolation anchor must occur exactly once (found %)', anchor_count; END IF;
    EXECUTE replace(definition,anchor,'AND context.matcher_contract_version = app.current_coverage_matcher_contract()
        AND job.retry_of_job_id IS NULL ' || anchor);
END;
$block$;

-- A scan is not a retry policy. Preserve identical authoritative failed or
-- cancelled targets even for legacy jobs with no automatic-retry enrollment.
-- Genuine target changes and readiness regained still permit fresh work;
-- forced adoption uses its separate event constructor, not this scan guard.
DO $block$
DECLARE definition text; anchor text; anchor_pattern text; anchor_count integer;
BEGIN
    definition := pg_get_functiondef('app.apply_coverage_reconciliation(uuid,bigint,text,uuid,uuid,bigint,bytea,text,text,jsonb,uuid,uuid,text,text,text,double precision)'::regprocedure);
    anchor_pattern := $pattern$\mAND[[:space:]]+parent_context[[:space:]]*\.[[:space:]]*minimum_traversal_meters[[:space:]]*=[[:space:]]*minimum_traversal_meters_value\M$pattern$;
    SELECT count(*),min(parts[1]) INTO anchor_count,anchor FROM regexp_matches(definition,anchor_pattern,'gi') AS matched(parts);
    IF anchor_count <> 1 OR (length(definition) - length(replace(definition,anchor,''))) / length(anchor) <> 1
    THEN RAISE EXCEPTION 'coverage reconciliation matcher anchor must occur exactly once (found %)', anchor_count; END IF;
    definition := replace(definition,anchor,anchor || '
           AND parent_context.matcher_contract_version = app.current_coverage_matcher_contract()');
    anchor_pattern := $pattern$\mSELECT[[:space:]]+item[[:space:]]*->>[[:space:]]*'regionId'[[:space:]]*,[[:space:]]*\([[:space:]]*item[[:space:]]*->>[[:space:]]*'generation'[[:space:]]*\)[[:space:]]*::[[:space:]]*bigint[[:space:]]+INTO[[:space:]]+anchor_region[[:space:]]*,[[:space:]]*anchor_generation\M$pattern$;
    SELECT count(*),min(parts[1]) INTO anchor_count,anchor FROM regexp_matches(definition,anchor_pattern,'gi') AS matched(parts);
    IF anchor_count <> 1 OR (length(definition) - length(replace(definition,anchor,''))) / length(anchor) <> 1
    THEN RAISE EXCEPTION 'coverage reconciliation retry-policy anchor must occur exactly once (found %)', anchor_count; END IF;
    definition := replace(definition,anchor,'IF current_state.readiness_state = ''map_data_ready'' AND EXISTS (
        SELECT 1 FROM app.jobs child
        JOIN app.jobs parent ON parent.id = child.parent_job_id AND parent.account_id = child.account_id
        JOIN app.coverage_route_job_contexts route_context ON route_context.job_id = child.id AND route_context.account_id = child.account_id
        JOIN app.coverage_job_contexts parent_context ON parent_context.job_id = parent.id AND parent_context.account_id = parent.account_id
        WHERE (child.id = current_state.target_job_id OR (current_state.target_job_id IS NULL AND child.status = ''cancelled''
               AND EXISTS (SELECT 1 FROM app.coverage_retry_targets(parent.id,target_account_id) retry_target WHERE retry_target.child_id = child.id)))
          AND child.account_id = target_account_id
          AND child.kind = ''coverage_update_route'' AND parent.kind = ''coverage_update''
          AND (child.status IN (''failed'',''cancelled'') OR child.cancel_requested_at IS NOT NULL
               OR parent.cancel_requested_at IS NOT NULL OR parent.status = ''cancelled'')
          AND route_context.workout_id = target_workout_id AND route_context.route_input_revision = target_route_revision
          AND route_context.route_input_sha256 = target_route_digest
          AND app.coverage_generation_vectors_equal(route_context.target_generations,region_generations)
          AND parent_context.matcher_contract_version = app.current_coverage_matcher_contract()
          AND parent_context.rules_version = rules_version_value AND parent_context.sampling_version = sampling_version_value
          AND parent_context.path_policy_version = path_policy_version_value
          AND parent_context.minimum_traversal_meters = minimum_traversal_meters_value
    ) THEN RETURN true; END IF;
    ' || anchor);
    EXECUTE definition;
END;
$block$;

GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.current_coverage_matcher_contract() OWNER TO workouts_security_owner;
ALTER TABLE app.coverage_retry_roots OWNER TO workouts_security_owner;
ALTER TABLE app.coverage_retry_attempts OWNER TO workouts_security_owner;
ALTER FUNCTION app.coverage_retry_root(uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.enroll_coverage_retry_failure() OWNER TO workouts_security_owner;
ALTER FUNCTION app.coverage_retry_targets(uuid,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.coverage_auto_retry_parent_eligible(uuid,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.construct_coverage_retry(uuid,uuid,integer,integer) OWNER TO workouts_security_owner;
ALTER FUNCTION app.retry_next_coverage_update(integer,integer) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
REVOKE ALL ON FUNCTION app.current_coverage_matcher_contract(), app.coverage_retry_root(uuid), app.enroll_coverage_retry_failure(), app.coverage_retry_targets(uuid,uuid),
    app.coverage_auto_retry_parent_eligible(uuid,uuid),
    app.construct_coverage_retry(uuid,uuid,integer,integer), app.retry_next_coverage_update(integer,integer)
FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles;
GRANT EXECUTE ON FUNCTION app.retry_next_coverage_update(integer,integer) TO workouts_coverage_worker;
-- coverage-contract:auto-retries:end

-- coverage-contract:runtime-capability:begin
-- Reuse only the runtime of this exact transaction's private ingest capability.
-- The existing fence still validates its authority, locks, epoch and live job.
-- No capability means the shipped standalone runtime-26 ABI, not schema_version.
DO $block$
DECLARE
    definition text;
    anchor text := 'app.fence_ingest_job(job, worker, lease, 26)';
    anchor_count integer;
BEGIN
    definition := pg_get_functiondef('app.read_ingest_candidate_actions(uuid,text,uuid,jsonb)'::regprocedure);
    anchor_count := (length(definition) - length(replace(definition,anchor,''))) / length(anchor);
    IF anchor_count <> 1 THEN
        RAISE EXCEPTION 'ingest candidate runtime fence anchor must occur exactly once (found %)',anchor_count;
    END IF;
    EXECUTE replace(definition,anchor,$replacement$app.fence_ingest_job(job, worker, lease, COALESCE((
        SELECT capability.runtime_version FROM app.ingest_write_capabilities capability
        WHERE capability.backend_pid = pg_backend_pid() AND capability.transaction_id = txid_current()
          AND capability.account_id = target_account AND capability.job_id = job
          AND capability.worker_id = worker AND capability.lease_token = lease
    ),26))$replacement$);
END;
$block$;

-- coverage-contract:runtime-capability:end

-- coverage-contract:diagnostic-limits:begin
-- Diagnostics and production share the same raw and sampled point bounds.
-- Preserve all other evidence, provenance, role and account-isolation contracts.
ALTER TABLE app.coverage_diagnostic_runs
    DROP CONSTRAINT coverage_diagnostic_runs_original_point_count_check,
    DROP CONSTRAINT coverage_diagnostic_runs_sampled_point_count_check,
    ADD CONSTRAINT coverage_diagnostic_runs_original_point_count_check
        CHECK (original_point_count BETWEEN 0 AND 50000),
    ADD CONSTRAINT coverage_diagnostic_runs_sampled_point_count_check
        CHECK (sampled_point_count BETWEEN 0 AND 12000);

-- coverage-contract:diagnostic-limits:end

-- coverage-contract:retry-presentation:begin
-- Advisory only: the retry constructor retains all locks and authority checks.
-- VOLATILE matches the private SELECT-only target selector's snapshot semantics.
CREATE FUNCTION app.read_coverage_retry_eligibility(prior_job_id uuid, max_ordinal integer)
RETURNS TABLE(can_retry boolean, reason text, eligible_route_count bigint, unsuccessful_route_count bigint, current_route_count bigint, current_coverage_job_ids uuid[])
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
    WITH owned AS MATERIALIZED (
        SELECT job.id, job.account_id, job.status
        FROM app.jobs job
        JOIN app.accounts account ON account.id = job.account_id AND account.state = 'active'
        JOIN app.coverage_job_contexts context ON context.job_id = job.id AND context.account_id = job.account_id
        WHERE job.id = prior_job_id AND job.account_id = app.current_account_id()
          AND job.kind = 'coverage_update' AND job.parent_job_id IS NULL
          AND max_ordinal BETWEEN 1 AND 100
    ), historical AS MATERIALIZED (
        -- Retain children without optional proof so missing/deleted
        -- workouts or contexts can never make "all current" appear true.
        SELECT owned.id AS parent_id, current_parent.id AS current_parent_id, current_parent.created_at AS current_parent_created_at, (
            state.readiness_state = 'map_data_ready' AND state.processing_state = 'current'
            AND state.applied_route_input_revision = state.route_input_revision
            AND state.applied_route_input_sha256 = state.route_input_sha256
            AND current_context.route_input_revision = state.applied_route_input_revision
            AND current_context.route_input_sha256 = state.applied_route_input_sha256
            AND app.coverage_generation_vectors_equal(current_context.target_generations,state.applied_generations)
            AND current_parent_context.rules_version = state.applied_rules_version
            AND current_parent_context.sampling_version = state.applied_sampling_version
            AND current_parent_context.path_policy_version = state.applied_path_policy_version
            AND current_parent_context.matcher_contract_version = app.current_coverage_matcher_contract()
            AND workout.deletion_requested_at IS NULL AND source.deleted_at IS NULL
            AND workout.id IS NOT NULL AND source.id IS NOT NULL
        ) AS is_current
        FROM owned JOIN app.jobs child ON child.parent_job_id = owned.id AND child.account_id = owned.account_id
        LEFT JOIN app.coverage_route_job_contexts context ON context.job_id = child.id AND context.account_id = child.account_id
        LEFT JOIN app.workout_coverage_states state ON state.workout_id = context.workout_id AND state.account_id = context.account_id
        LEFT JOIN app.workouts workout ON workout.id = context.workout_id AND workout.account_id = context.account_id
        LEFT JOIN app.sources source ON source.id = workout.source_id AND source.account_id = workout.account_id
        LEFT JOIN app.jobs current_child ON current_child.id = state.target_job_id
            AND current_child.account_id = child.account_id
            AND current_child.kind = 'coverage_update_route' AND current_child.status = 'succeeded'
        LEFT JOIN app.coverage_route_job_contexts current_context ON current_context.job_id = current_child.id
            AND current_context.account_id = child.account_id AND current_context.workout_id = context.workout_id
        LEFT JOIN app.jobs current_parent ON current_parent.id = current_child.parent_job_id
            AND current_parent.account_id = child.account_id AND current_parent.kind = 'coverage_update'
            AND current_parent.parent_job_id IS NULL
        LEFT JOIN app.coverage_job_contexts current_parent_context ON current_parent_context.job_id = current_parent.id
            AND current_parent_context.account_id = child.account_id
        WHERE child.kind = 'coverage_update_route' AND child.status IN ('failed','cancelled')
    ), counts AS (
        SELECT owned.*,
            (SELECT count(*) FROM app.coverage_retry_targets(owned.id,owned.account_id)) AS eligible,
            (SELECT count(*) FROM historical WHERE parent_id = owned.id) AS unsuccessful,
            (SELECT count(*) FROM historical WHERE parent_id = owned.id AND is_current) AS current_count,
            -- Supersession is per-route current authority, never retry ancestry.
            ARRAY(SELECT current_parent_id FROM historical
                WHERE parent_id = owned.id AND is_current
                GROUP BY current_parent_id, current_parent_created_at
                ORDER BY current_parent_created_at DESC, current_parent_id DESC LIMIT 100) AS current_jobs,
            (SELECT depth FROM app.coverage_retry_root(owned.id)) AS ordinal,
            EXISTS (SELECT 1 FROM app.jobs successor WHERE successor.retry_of_job_id = owned.id
                AND successor.account_id = owned.account_id AND successor.kind = 'coverage_update') AS has_successor
        FROM owned
    ), decision AS (
        SELECT counts.*, CASE
            WHEN status NOT IN ('failed','partially_succeeded','cancelled') THEN 'not_terminal'
            -- Proven resolution needs no action, even for ancestors or at the limit.
            WHEN eligible = 0 AND unsuccessful > 0 AND current_count = unsuccessful THEN 'already_current'
            WHEN has_successor THEN 'not_latest'
            WHEN ordinal >= max_ordinal THEN 'retry_limit'
            WHEN eligible > 0 THEN 'eligible'
            ELSE 'no_eligible_routes' END AS reason_code
        FROM counts
    )
    SELECT reason_code = 'eligible', reason_code, eligible, unsuccessful, current_count, current_jobs FROM decision
$function$;
-- Only derived-status filters require proof before counting/pagination. Other
-- requests prove page rows only; the sentinel preserves empty-page totals.
CREATE FUNCTION app.read_job_history(status_filter text, operation_filter text, page_size integer, page_offset bigint, max_ordinal integer)
RETURNS TABLE(job_id uuid, display_status text, total_items bigint)
LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
    WITH candidates AS MATERIALIZED (
        SELECT job.id, job.kind, job.status, job.created_at, CASE
            WHEN job.kind = 'coverage_update' AND job.status = 'running' AND NOT EXISTS (
                SELECT 1 FROM app.jobs child WHERE child.parent_job_id = job.id
                    AND child.account_id = job.account_id AND child.status = 'running') THEN 'queued'
            ELSE job.status END AS public_status
        FROM app.jobs job JOIN app.accounts account ON account.id = job.account_id AND account.state = 'active'
        WHERE job.account_id = app.current_account_id() AND job.parent_job_id IS NULL
          AND job.kind IN ('manual_ingest','scheduled_ingest','workout_deletion','coverage_update')
          AND page_size BETWEEN 1 AND 100 AND page_offset >= 0 AND max_ordinal BETWEEN 1 AND 100
          AND COALESCE(status_filter,'') IN ('','queued','running','succeeded','partially_succeeded','failed','cancelled','superseded')
          AND COALESCE(operation_filter,'') IN ('','manual_sync','automated_sync','workout_deletion','coverage_update')
          AND (COALESCE(status_filter,'') NOT IN ('failed','partially_succeeded','superseded') OR job.status = status_filter
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
GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.read_coverage_retry_eligibility(uuid,integer) OWNER TO workouts_security_owner;
ALTER FUNCTION app.read_job_history(text,text,integer,bigint,integer) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
REVOKE ALL ON FUNCTION app.read_coverage_retry_eligibility(uuid,integer)
FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles, workouts_migration;
GRANT EXECUTE ON FUNCTION app.read_coverage_retry_eligibility(uuid,integer) TO workouts_api;
REVOKE ALL ON FUNCTION app.read_job_history(text,text,integer,bigint,integer)
FROM PUBLIC, workouts_api, workouts_worker, workouts_coverage_worker, workouts_tiles, workouts_migration;
GRANT EXECUTE ON FUNCTION app.read_job_history(text,text,integer,bigint,integer) TO workouts_api;

-- coverage-contract:retry-presentation:end

-- coverage-contract:canonical-metadata:begin
ALTER TABLE app.schema_metadata ADD COLUMN migration_baseline integer;
GRANT SELECT (migration_baseline) ON app.schema_metadata TO workouts_api, workouts_worker, workouts_coverage_worker;
UPDATE app.schema_metadata SET schema_version = 27, minimum_runtime_version = 26, migration_baseline = 27 WHERE singleton;
-- coverage-contract:canonical-metadata:end
-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
DO $forward_only$
BEGIN
    RAISE EXCEPTION 'coverage recovery baseline is forward-only' USING ERRCODE = '55000';
END;
$forward_only$;
-- +goose StatementEnd
