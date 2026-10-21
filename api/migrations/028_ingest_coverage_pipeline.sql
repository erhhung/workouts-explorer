-- +goose Up
-- Capacity is a creation snapshot, not a live limit. Retained terminal and
-- superseded children consume capacity too; historical retry cohorts may exceed 1000.
CREATE TABLE app.coverage_batch_policy (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    batch_size integer NOT NULL DEFAULT 25 CHECK (batch_size BETWEEN 1 AND 1000)
);
INSERT INTO app.coverage_batch_policy(singleton) VALUES(true);
ALTER TABLE app.coverage_batch_policy ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.coverage_batch_policy FORCE ROW LEVEL SECURITY;
CREATE POLICY coverage_batch_policy_owner_policy ON app.coverage_batch_policy
TO workouts_security_owner USING (true) WITH CHECK (true);

-- +goose StatementBegin
CREATE FUNCTION app.current_coverage_batch_size() RETURNS integer LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
    SELECT batch_size FROM app.coverage_batch_policy WHERE singleton
$function$;
-- +goose StatementEnd

ALTER TABLE app.coverage_job_contexts ADD COLUMN parent_batch_size integer;
ALTER TABLE app.coverage_job_contexts NO FORCE ROW LEVEL SECURITY;
ALTER TABLE app.jobs NO FORCE ROW LEVEL SECURITY;
UPDATE app.coverage_job_contexts context SET parent_batch_size = GREATEST(25,
    (SELECT count(*)::integer FROM app.jobs child WHERE child.parent_job_id = context.job_id));
ALTER TABLE app.coverage_job_contexts FORCE ROW LEVEL SECURITY;
ALTER TABLE app.jobs FORCE ROW LEVEL SECURITY;
ALTER TABLE app.coverage_job_contexts ALTER COLUMN parent_batch_size SET NOT NULL,
    ALTER COLUMN parent_batch_size SET DEFAULT app.current_coverage_batch_size(),
    ADD CONSTRAINT coverage_job_contexts_parent_batch_size_check CHECK (parent_batch_size >= 1);
ALTER TABLE app.coverage_job_contexts ADD COLUMN sealed_ingest_root_id uuid;

-- Preparation has no job identity: an incomplete eligible cohort is not queued
-- work. Its capacity survives policy changes and source-worker lease boundaries.
CREATE TABLE app.ingest_coverage_builders (
    builder_id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id uuid NOT NULL REFERENCES app.accounts(id) ON DELETE CASCADE,
    root_job_id uuid NOT NULL,
    batch_size integer NOT NULL CHECK (batch_size BETWEEN 1 AND 1000),
    rules text NOT NULL,
    sampling text NOT NULL,
    policy text NOT NULL,
    minimum double precision NOT NULL CHECK (minimum BETWEEN 0.1 AND 100),
    matcher_contract text NOT NULL,
    UNIQUE(account_id,root_job_id),
    FOREIGN KEY(root_job_id,account_id) REFERENCES app.jobs(id,account_id) ON DELETE CASCADE
);
CREATE TABLE app.ingest_coverage_prepared (
    account_id uuid NOT NULL,
    workout_id uuid NOT NULL,
    builder_id uuid NOT NULL REFERENCES app.ingest_coverage_builders(builder_id) ON DELETE CASCADE,
    event_id uuid NOT NULL,
    source_id uuid NOT NULL,
    source_file_id uuid NOT NULL,
    ingest_job_id uuid NOT NULL,
    origin text NOT NULL CHECK (origin IN ('normal','adopted')),
    content_sha256 bytea NOT NULL CHECK (octet_length(content_sha256)=32),
    route_input_sha256 bytea NOT NULL CHECK (octet_length(route_input_sha256)=32),
    route_input_revision bigint NOT NULL CHECK (route_input_revision>0),
    ownership_epoch bigint NOT NULL CHECK (ownership_epoch>=0),
    catalog_fingerprint bytea NOT NULL CHECK (octet_length(catalog_fingerprint)=32),
    generations jsonb NOT NULL CHECK (app.valid_coverage_generation_vector(generations)),
    PRIMARY KEY(account_id,workout_id),
    FOREIGN KEY(workout_id,account_id) REFERENCES app.workouts(id,account_id) ON DELETE CASCADE
);
CREATE INDEX ingest_coverage_prepared_builder_idx ON app.ingest_coverage_prepared(builder_id,workout_id);
ALTER TABLE app.ingest_coverage_builders ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.ingest_coverage_builders FORCE ROW LEVEL SECURITY;
ALTER TABLE app.ingest_coverage_prepared ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.ingest_coverage_prepared FORCE ROW LEVEL SECURITY;
CREATE POLICY ingest_coverage_builders_owner_policy ON app.ingest_coverage_builders
TO workouts_security_owner USING(true) WITH CHECK(true);
CREATE POLICY ingest_coverage_prepared_owner_policy ON app.ingest_coverage_prepared
TO workouts_security_owner USING(true) WITH CHECK(true);

-- +goose StatementBegin
CREATE FUNCTION app.ingest_coverage_root_terminal(target_account_id uuid,target_root_id uuid)
RETURNS boolean LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
    SELECT EXISTS (SELECT 1 FROM app.jobs root WHERE root.id=target_root_id AND root.account_id=target_account_id
        AND root.parent_job_id IS NULL AND root.kind IN ('manual_ingest','scheduled_ingest')
        AND root.status IN ('succeeded','partially_succeeded','failed','cancelled'))
      AND NOT EXISTS (SELECT 1 FROM app.jobs source WHERE source.parent_job_id=target_root_id
        AND source.account_id=target_account_id AND source.status IN ('queued','running'))
$function$;
-- +goose StatementEnd

-- An anchor is retained for existing clients; the complete scope is the union of
-- actual children, including retry subsets, never the representative anchor.
-- +goose StatementBegin
CREATE FUNCTION app.read_coverage_job_scope(target_job_id uuid)
RETURNS TABLE(region_ids text[],mixed_regions boolean) LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
    SELECT COALESCE(array_agg(DISTINCT generation->>'regionId' ORDER BY generation->>'regionId'),ARRAY[]::text[]),
        count(DISTINCT generation->>'regionId')>1
    FROM app.jobs parent JOIN app.jobs child ON child.parent_job_id=parent.id AND child.account_id=parent.account_id
    JOIN app.coverage_route_job_contexts context ON context.job_id=child.id AND context.account_id=child.account_id
    CROSS JOIN LATERAL jsonb_array_elements(context.target_generations) generation
    WHERE parent.id=target_job_id AND parent.account_id=app.current_account_id() AND parent.kind='coverage_update'
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.configure_coverage_batch_size(runtime_version integer, size integer) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
BEGIN
    IF runtime_version IS NULL OR runtime_version < 28 OR runtime_version <
       (SELECT minimum_runtime_version FROM app.schema_metadata WHERE singleton) THEN
        RAISE EXCEPTION 'coverage pipeline runtime 28 or newer is required' USING ERRCODE = '55000';
    END IF;
    IF size IS NULL OR size NOT BETWEEN 1 AND 1000 THEN
        RAISE EXCEPTION 'coverage batch size must be between 1 and 1000' USING ERRCODE = '22023';
    END IF;
    UPDATE app.coverage_batch_policy SET batch_size = size WHERE singleton;
    RETURN FOUND;
END;
$function$;
-- +goose StatementEnd

-- One row per import origin, retained after acknowledgement. No account/source/job
-- FK here: recording under ingest's existing file fence must not invert its locks.
CREATE TABLE app.ingest_coverage_handoffs (
    event_id uuid PRIMARY KEY,
    account_id uuid NOT NULL,
    workout_id uuid NOT NULL,
    source_id uuid NOT NULL,
    source_file_id uuid NOT NULL,
    ingest_job_id uuid NOT NULL,
    origin text NOT NULL DEFAULT 'normal' CHECK (origin IN ('normal','adopted')),
    repair boolean NOT NULL DEFAULT false CHECK (NOT repair OR origin='normal'),
    content_sha256 bytea NOT NULL CHECK (octet_length(content_sha256) = 32),
    ordinal bigint GENERATED ALWAYS AS IDENTITY,
    requested_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    next_attempt_at timestamptz NOT NULL DEFAULT '-infinity',
    acknowledged_at timestamptz,
    coverage_parent_job_id uuid,
    coverage_child_job_id uuid,
    outcome text CHECK (outcome IN ('queued','existing','pending','unavailable','superseded','deleted')),
    CHECK ((acknowledged_at IS NULL) = (outcome IS NULL)),
    FOREIGN KEY (workout_id,account_id) REFERENCES app.workouts(id,account_id) ON DELETE CASCADE
);
CREATE INDEX ingest_coverage_handoffs_pending_idx ON app.ingest_coverage_handoffs(account_id,workout_id,ordinal DESC)
WHERE acknowledged_at IS NULL;
CREATE INDEX ingest_coverage_handoffs_origin_idx ON app.ingest_coverage_handoffs(account_id,ingest_job_id,source_file_id);
CREATE INDEX ingest_coverage_handoffs_history_idx ON app.ingest_coverage_handoffs(account_id,workout_id,ordinal);
CREATE UNIQUE INDEX ingest_coverage_handoffs_repair_idx ON app.ingest_coverage_handoffs(ingest_job_id,workout_id) WHERE repair;
ALTER TABLE app.ingest_coverage_handoffs ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.ingest_coverage_handoffs FORCE ROW LEVEL SECURITY;
CREATE POLICY ingest_coverage_handoffs_owner_policy ON app.ingest_coverage_handoffs
TO workouts_security_owner USING (true) WITH CHECK (true);

-- Adoption acknowledgements are independent of the ephemeral force-refresh queue.
CREATE TABLE app.adopted_coverage_handoff_receipts (
    event_id uuid PRIMARY KEY,
    account_id uuid NOT NULL,
    workout_id uuid NOT NULL,
    source_id uuid NOT NULL,
    source_file_id uuid NOT NULL,
    ingest_job_id uuid NOT NULL,
    import_event_id uuid,
    acknowledged_at timestamptz,
    coverage_parent_job_id uuid,
    coverage_child_job_id uuid,
    FOREIGN KEY (workout_id,account_id) REFERENCES app.workouts(id,account_id) ON DELETE CASCADE
);
ALTER TABLE app.adopted_coverage_handoff_receipts ENABLE ROW LEVEL SECURITY;
CREATE INDEX adopted_coverage_handoff_receipts_workout_idx
ON app.adopted_coverage_handoff_receipts(account_id,workout_id,acknowledged_at);
ALTER TABLE app.adopted_coverage_handoff_receipts FORCE ROW LEVEL SECURITY;
CREATE POLICY adopted_coverage_handoff_receipts_owner_policy ON app.adopted_coverage_handoff_receipts
TO workouts_security_owner USING (true) WITH CHECK (true);

-- +goose StatementBegin
CREATE FUNCTION app.record_adopted_coverage_origin() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
BEGIN
    INSERT INTO app.adopted_coverage_handoff_receipts(event_id,account_id,workout_id,source_id,source_file_id,ingest_job_id,import_event_id)
    SELECT NEW.event_id,NEW.account_id,NEW.workout_id,NEW.source_id,NEW.source_file_id,file.job_id,
        (SELECT event.id FROM app.workout_import_events event WHERE event.account_id = NEW.account_id AND event.workout_id = NEW.workout_id
          AND event.source_file_id = NEW.source_file_id AND event.kind = 'adopted'
          ORDER BY (SELECT handoff.ordinal FROM app.ingest_coverage_handoffs handoff WHERE handoff.event_id = event.id) DESC NULLS LAST,
              event.created_at DESC,event.id DESC LIMIT 1)
    FROM app.source_files file WHERE file.id = NEW.source_file_id AND file.account_id = NEW.account_id
    ON CONFLICT (event_id) DO NOTHING;
    RETURN NEW;
END;
$function$;
-- +goose StatementEnd
CREATE TRIGGER adopted_coverage_origin AFTER INSERT OR UPDATE OF event_id ON app.adopted_workout_coverage_refreshes
FOR EACH ROW EXECUTE FUNCTION app.record_adopted_coverage_origin();
ALTER TABLE app.source_files NO FORCE ROW LEVEL SECURITY;
ALTER TABLE app.workout_import_events NO FORCE ROW LEVEL SECURITY;
-- Preserve every provable legacy import origin for a retained force request;
-- historical same-transaction UUID ordering is not evidence of import order.
INSERT INTO app.ingest_coverage_handoffs(event_id,account_id,workout_id,source_id,source_file_id,ingest_job_id,content_sha256,origin)
SELECT event.id,event.account_id,event.workout_id,event.source_id,event.source_file_id,event.job_id,event.content_sha256,'adopted'
FROM app.adopted_workout_coverage_refreshes request JOIN app.workout_import_events event
  ON event.account_id = request.account_id AND event.workout_id = request.workout_id
    AND event.source_file_id = request.source_file_id AND event.source_id = request.source_id AND event.kind = 'adopted';
INSERT INTO app.adopted_coverage_handoff_receipts(event_id,account_id,workout_id,source_id,source_file_id,ingest_job_id,import_event_id)
SELECT request.event_id,request.account_id,request.workout_id,request.source_id,request.source_file_id,file.job_id,
    (SELECT CASE WHEN count(*) = 1 THEN (array_agg(event.id))[1] END FROM app.workout_import_events event WHERE event.account_id = request.account_id
      AND event.workout_id = request.workout_id AND event.source_file_id = request.source_file_id AND event.kind = 'adopted'
    )
FROM app.adopted_workout_coverage_refreshes request JOIN app.source_files file
  ON file.id = request.source_file_id AND file.account_id = request.account_id;
ALTER TABLE app.source_files FORCE ROW LEVEL SECURITY;
ALTER TABLE app.workout_import_events FORCE ROW LEVEL SECURITY;

-- +goose StatementBegin
CREATE FUNCTION app.record_ingest_coverage_handoff() RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
BEGIN
    IF NEW.kind IN ('created','updated','adopted') AND (NEW.kind = 'adopted' OR EXISTS (
        SELECT 1 FROM app.workout_route_points WHERE account_id = NEW.account_id AND workout_id = NEW.workout_id
    )) THEN
        INSERT INTO app.ingest_coverage_handoffs(event_id,account_id,workout_id,source_id,source_file_id,ingest_job_id,content_sha256,origin)
        VALUES(NEW.id,NEW.account_id,NEW.workout_id,NEW.source_id,NEW.source_file_id,NEW.job_id,NEW.content_sha256,
            CASE WHEN NEW.kind = 'adopted' THEN 'adopted' ELSE 'normal' END);
    END IF;
    RETURN NEW;
END;
$function$;
-- +goose StatementEnd
CREATE TRIGGER workout_import_events_coverage_handoff AFTER INSERT ON app.workout_import_events
FOR EACH ROW EXECUTE FUNCTION app.record_ingest_coverage_handoff();

-- +goose StatementBegin
CREATE FUNCTION app.enqueue_coverage_update_core(
    target_account_id uuid, new_parent_id uuid, target_region_id text, target_osm_generation bigint,
    target_work_revision bigint, target_rules text, target_sampling text, target_policy text,
    target_minimum double precision, route_targets jsonb, force_refresh boolean
) RETURNS TABLE(job_id uuid,route_count integer,reused boolean) LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
DECLARE target jsonb; additions jsonb := '[]'; actual_parent uuid; existing_parent uuid;
DECLARE child_id uuid; generations jsonb; capacity integer; needed integer; total integer; old_parent uuid;
DECLARE seen jsonb := '{}'; workout_key text;
BEGIN
    IF target_account_id IS DISTINCT FROM app.current_account_id() OR new_parent_id IS NULL OR
       target_region_id IS NULL OR target_region_id !~ '^[a-z][a-z0-9-]{0,63}:[a-z][a-z0-9-]{0,63}$' OR
       target_osm_generation IS NULL OR target_osm_generation < 1 OR target_work_revision IS NULL OR target_work_revision < 1 OR
       target_rules IS DISTINCT FROM 'coverage-experimental-v1' OR target_sampling IS DISTINCT FROM 'coverage-sampling-experimental-v1' OR
       target_policy IS NULL OR target_policy !~ '^coverage-path-policy-experimental-v[1-9][0-9]*$' OR
       target_minimum IS NULL OR target_minimum NOT BETWEEN 0.1 AND 100 OR
       jsonb_typeof(route_targets) IS DISTINCT FROM 'array' THEN
        RAISE EXCEPTION 'invalid coverage update target' USING ERRCODE = '22023';
    END IF;
    IF jsonb_array_length(route_targets) NOT BETWEEN 1 AND 1000 OR force_refresh IS NULL THEN
        RAISE EXCEPTION 'invalid coverage target count' USING ERRCODE = '22023';
    END IF;
    PERFORM 1 FROM app.accounts WHERE id = target_account_id AND state = 'active' FOR UPDATE;
    IF NOT FOUND THEN RETURN; END IF;
    capacity := app.current_coverage_batch_size();
    FOR target IN SELECT value FROM jsonb_array_elements(route_targets) LOOP
        IF jsonb_typeof(target) IS DISTINCT FROM 'object' OR (SELECT count(*) FROM jsonb_object_keys(target)) <> 4 OR
           NOT (target ?& ARRAY['jobId','workoutId','routeRevision','generations']) OR
           target->>'jobId' IS NULL OR target->>'workoutId' IS NULL OR target->>'routeRevision' IS NULL OR
           target->>'jobId' !~ '^[0-9a-fA-F-]{36}$' OR target->>'workoutId' !~ '^[0-9a-fA-F-]{36}$' OR
           target->>'routeRevision' !~ '^[1-9][0-9]*$' OR
           app.valid_coverage_generation_vector(target->'generations') IS DISTINCT FROM true THEN
            RAISE EXCEPTION 'invalid coverage route target' USING ERRCODE = '22023';
        END IF;
        generations := (SELECT jsonb_agg(value ORDER BY value->>'regionId') FROM jsonb_array_elements(target->'generations'));
        target := jsonb_set(target,'{generations}',generations);
        workout_key := ((target->>'workoutId')::uuid)::text;
        IF seen ? workout_key THEN
            IF seen->workout_key->>'routeRevision' IS DISTINCT FROM target->>'routeRevision'
               OR NOT app.coverage_generation_vectors_equal(seen->workout_key->'generations',generations) THEN
                RAISE EXCEPTION 'conflicting targets for one workout' USING ERRCODE = '22023';
            END IF;
            CONTINUE;
        END IF;
        seen := seen || jsonb_build_object(workout_key,target);
        PERFORM 1 FROM app.workout_coverage_states state JOIN app.workouts workout
          ON workout.id = state.workout_id AND workout.account_id = state.account_id
          JOIN app.sources source ON source.id = workout.source_id AND source.account_id = workout.account_id
         WHERE state.account_id = target_account_id AND state.workout_id = (target->>'workoutId')::uuid
           AND state.route_input_revision = (target->>'routeRevision')::bigint AND state.readiness_state = 'map_data_ready'
           AND workout.deletion_requested_at IS NULL AND source.deleted_at IS NULL;
        IF NOT FOUND THEN RAISE EXCEPTION 'coverage route target is stale' USING ERRCODE = '40001'; END IF;
        IF NOT force_refresh THEN
            SELECT existing.parent_job_id INTO existing_parent FROM app.workout_coverage_states state
            CROSS JOIN LATERAL app.ingest_coverage_existing_target(target_account_id,state.workout_id,
                (target->>'routeRevision')::bigint,state.route_input_sha256,generations,
                target_rules,target_sampling,target_policy,target_minimum) existing
            JOIN app.coverage_route_job_contexts context ON context.job_id=existing.child_job_id
                AND context.account_id=state.account_id AND context.workout_id=state.workout_id
            WHERE state.account_id=target_account_id AND state.workout_id=(target->>'workoutId')::uuid
              AND app.coverage_generation_vectors_equal(context.target_generations,generations);
            IF FOUND THEN CONTINUE; END IF;
        END IF;
        additions := additions || jsonb_build_array(target);
    END LOOP;
    needed := jsonb_array_length(additions);
    IF needed = 0 THEN
        job_id := existing_parent; reused := true;
        SELECT count(*)::integer INTO route_count FROM app.jobs WHERE parent_job_id = existing_parent;
        RETURN NEXT; RETURN;
    END IF;
    IF needed > capacity THEN
        RAISE EXCEPTION 'coverage request exceeds parent capacity' USING ERRCODE = '22023';
    END IF;
    -- A retry's spare inherited capacity belongs only to its failed-route cohort.
    SELECT context.job_id INTO actual_parent FROM app.coverage_job_contexts context
    JOIN app.jobs job ON job.id = context.job_id AND job.account_id = context.account_id
    WHERE context.account_id = target_account_id AND context.region_id = target_region_id
      AND context.target_osm_generation = enqueue_coverage_update_core.target_osm_generation
      AND context.rules_version = target_rules AND context.sampling_version = target_sampling
      AND context.path_policy_version = target_policy AND context.minimum_traversal_meters = target_minimum
      AND context.matcher_contract_version = app.current_coverage_matcher_contract() AND context.parent_batch_size = capacity
      AND context.sealed_ingest_root_id IS NULL
      AND job.kind = 'coverage_update' AND job.status = 'queued' AND job.retry_of_job_id IS NULL AND job.cancel_requested_at IS NULL
      AND (SELECT count(*) FROM app.jobs child WHERE child.parent_job_id = job.id) + needed <= context.parent_batch_size
    ORDER BY job.created_at,job.id LIMIT 1 FOR UPDATE OF job;
    reused := FOUND;
    IF NOT reused THEN
        actual_parent := new_parent_id;
        INSERT INTO app.jobs(id,account_id,kind,priority,parameters,progress_total)
        VALUES(actual_parent,target_account_id,'coverage_update',20,jsonb_build_object('regionId',target_region_id),needed);
        INSERT INTO app.coverage_job_contexts(job_id,account_id,region_id,target_osm_generation,target_work_revision,
            rules_version,sampling_version,path_policy_version,minimum_traversal_meters,parent_batch_size)
        VALUES(actual_parent,target_account_id,target_region_id,target_osm_generation,target_work_revision,
            target_rules,target_sampling,target_policy,target_minimum,capacity);
    ELSE
        UPDATE app.coverage_job_contexts SET target_work_revision = GREATEST(coverage_job_contexts.target_work_revision,
            enqueue_coverage_update_core.target_work_revision) WHERE coverage_job_contexts.job_id = actual_parent;
    END IF;
    FOR target IN SELECT value FROM jsonb_array_elements(additions) LOOP
        child_id := (target->>'jobId')::uuid; generations := target->'generations';
        UPDATE app.coverage_route_job_contexts context SET result_outcome = 'superseded' FROM app.jobs child
         WHERE child.id = context.job_id AND child.account_id = context.account_id AND child.account_id = target_account_id
           AND context.workout_id = (target->>'workoutId')::uuid AND child.status = 'queued';
        PERFORM set_config('app.job_transition','supersede',true);
        UPDATE app.jobs child SET status = 'succeeded',terminal_at = clock_timestamp() FROM app.coverage_route_job_contexts context
         WHERE child.id = context.job_id AND child.account_id = context.account_id AND child.account_id = target_account_id
           AND context.workout_id = (target->>'workoutId')::uuid AND child.status = 'queued';
        INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority,parameters)
        VALUES(child_id,actual_parent,target_account_id,'coverage_update_route',20,'{}');
        INSERT INTO app.coverage_route_job_contexts(job_id,account_id,workout_id,route_input_revision,route_input_sha256,target_generations)
        SELECT child_id,target_account_id,workout_id,route_input_revision,route_input_sha256,generations
        FROM app.workout_coverage_states WHERE account_id = target_account_id AND workout_id = (target->>'workoutId')::uuid;
        UPDATE app.workout_coverage_states SET processing_state = 'queued',target_job_id = child_id,target_revision = route_input_revision,
            target_rules_version = target_rules,target_sampling_version = target_sampling,target_path_policy_version = target_policy,
            target_generations = generations,processing_failure_code = NULL,processing_failure_summary = NULL,
            processing_started_at = NULL,processing_finished_at = NULL,updated_at = clock_timestamp()
        WHERE account_id = target_account_id AND workout_id = (target->>'workoutId')::uuid;
    END LOOP;
    SELECT count(*)::integer INTO total FROM app.jobs WHERE parent_job_id = actual_parent;
    INSERT INTO app.coverage_job_progress(job_id,account_id,routes_total) VALUES(actual_parent,target_account_id,total)
    ON CONFLICT ON CONSTRAINT coverage_job_progress_pkey DO UPDATE SET routes_total = EXCLUDED.routes_total,updated_at = clock_timestamp();
    PERFORM set_config('app.job_transition','derive_parent',true);
    UPDATE app.jobs SET progress_total = total WHERE id = actual_parent AND account_id = target_account_id;
    FOR old_parent IN SELECT parent.id FROM app.jobs parent WHERE parent.account_id = target_account_id
        AND parent.kind = 'coverage_update' AND parent.status IN ('queued','running') AND parent.id <> actual_parent
        AND NOT EXISTS (SELECT 1 FROM app.jobs child WHERE child.parent_job_id = parent.id AND child.status IN ('queued','running'))
    LOOP PERFORM app.derive_parent_status(old_parent); END LOOP;
    job_id := actual_parent; route_count := total; RETURN NEXT;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.enqueue_coverage_update(
    target_account_id uuid, new_parent_id uuid, target_region_id text, target_osm_generation bigint,
    target_work_revision bigint, target_rules text, target_sampling text, target_policy text,
    target_minimum double precision, route_targets jsonb
) RETURNS TABLE(job_id uuid,route_count integer,reused boolean) LANGUAGE sql SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
    SELECT * FROM app.enqueue_coverage_update_core($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,false)
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.enqueue_adopted_coverage_update(
    target_account_id uuid, new_parent_id uuid, target_region_id text, target_osm_generation bigint,
    target_work_revision bigint, target_rules text, target_sampling text, target_policy text,
    target_minimum double precision, route_targets jsonb
) RETURNS TABLE(job_id uuid,route_count integer,reused boolean) LANGUAGE sql SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
    SELECT * FROM app.enqueue_coverage_update_core($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,true)
$function$;
-- +goose StatementEnd

-- Use bounded semantic anchors, each required exactly once. CREATE OR REPLACE
-- preserves shipped helper OIDs, argument names, owner and grants.
-- +goose StatementBegin
DO $block$
DECLARE definition text; pattern text; anchor text; occurrences integer;
BEGIN
    definition := pg_get_functiondef('app.record_adopted_workout_coverage_refresh(uuid,text,uuid,uuid)'::regprocedure);
    pattern := '\mINSERT\s+INTO\s+app\s*\.\s*adopted_workout_coverage_refreshes\M';
    SELECT count(*) INTO occurrences FROM regexp_matches(definition,pattern,'gi');
    IF occurrences <> 1 THEN RAISE EXCEPTION 'adoption import authority anchor must occur exactly once (%)',occurrences; END IF;
    definition := regexp_replace(definition,pattern,'IF NOT EXISTS (SELECT 1 FROM app.ingest_coverage_handoffs handoff
        JOIN app.workout_import_events event ON event.id = handoff.event_id AND event.account_id = handoff.account_id AND event.kind = ''adopted''
        WHERE handoff.account_id = target.account_id AND handoff.workout_id = target_workout_id
          AND handoff.source_id = target.source_id AND handoff.source_file_id = target.source_file_id
          AND handoff.ingest_job_id = target_job_id AND handoff.origin = ''adopted'' AND handoff.acknowledged_at IS NULL) THEN
        RAISE EXCEPTION ''force coverage requires an unacknowledged adopted import event'' USING ERRCODE = ''42501''; END IF;
        INSERT INTO app.adopted_workout_coverage_refreshes','gi');
    EXECUTE definition;
    definition := pg_get_functiondef('app.construct_coverage_retry(uuid,uuid,integer,integer)'::regprocedure);
    FOR pattern,anchor IN SELECT * FROM (VALUES
        ('\mcontext\s*\.\s*matcher_contract_version\M','context.matcher_contract_version, context.parent_batch_size, context.sealed_ingest_root_id'),
        ('\mminimum_traversal_meters\s*,\s*matcher_contract_version\s*\)', 'minimum_traversal_meters,matcher_contract_version,parent_batch_size,sealed_ingest_root_id)'),
        ('\mprior_parent\s*\.\s*matcher_contract_version\s*\)', 'prior_parent.matcher_contract_version,prior_parent.parent_batch_size,prior_parent.sealed_ingest_root_id)')
    ) replacements(pattern,replacement) LOOP
        SELECT count(*) INTO occurrences FROM regexp_matches(definition,pattern,'gi');
        IF occurrences <> 1 THEN RAISE EXCEPTION 'coverage retry capacity anchor must occur exactly once: % (%)',pattern,occurrences; END IF;
        definition := regexp_replace(definition,pattern,anchor,'gi');
    END LOOP;
    EXECUTE definition;
    definition := pg_get_functiondef('app.apply_adopted_workout_coverage_refresh(uuid,uuid,text,uuid,bigint,uuid,uuid,bytea,text,text,jsonb,uuid,uuid,text,text,text,double precision)'::regprocedure);
    pattern := '\mapp\s*\.\s*enqueue_coverage_update\s*\(';
    SELECT count(*) INTO occurrences FROM regexp_matches(definition,pattern,'gi');
    IF occurrences <> 1 THEN RAISE EXCEPTION 'adoption enqueue anchor must occur exactly once (%)',occurrences; END IF;
    definition := regexp_replace(definition,pattern,'app.enqueue_adopted_coverage_update(','gi');
    pattern := '\mDELETE\s+FROM\s+app\s*\.\s*adopted_workout_coverage_refreshes\s+WHERE';
    -- Both deletion/no-route and successful enqueue acknowledge their own event.
    SELECT count(*) INTO occurrences FROM regexp_matches(definition,pattern,'gi');
    IF occurrences <> 2 THEN RAISE EXCEPTION 'adoption receipt anchors must occur exactly twice (%)',occurrences; END IF;
    definition := regexp_replace(definition,pattern,'PERFORM app.record_adopted_coverage_receipt(target_account_id,target_workout_id,target_event_id);
        DELETE FROM app.adopted_workout_coverage_refreshes WHERE','gi');
    EXECUTE definition;
END;
$block$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.record_adopted_coverage_receipt(target_account_id uuid,target_workout_id uuid,target_event_id uuid)
RETURNS void LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
    INSERT INTO app.adopted_coverage_handoff_receipts(event_id,account_id,workout_id,source_id,source_file_id,ingest_job_id,acknowledged_at,
        import_event_id,coverage_parent_job_id,coverage_child_job_id)
    SELECT request.event_id,request.account_id,request.workout_id,request.source_id,request.source_file_id,file.job_id,clock_timestamp(),
        (SELECT event.id FROM app.workout_import_events event WHERE event.account_id = request.account_id
          AND event.workout_id = request.workout_id AND event.source_file_id = request.source_file_id AND event.kind = 'adopted'
          ORDER BY (SELECT handoff.ordinal FROM app.ingest_coverage_handoffs handoff WHERE handoff.event_id = event.id) DESC NULLS LAST,
              event.created_at DESC,event.id DESC LIMIT 1),
        CASE WHEN EXISTS (SELECT 1 FROM app.workout_routes route WHERE route.account_id = request.account_id AND route.workout_id = request.workout_id)
             AND EXISTS (SELECT 1 FROM app.workout_route_points point WHERE point.account_id = request.account_id AND point.workout_id = request.workout_id)
             THEN child.parent_job_id END,
        CASE WHEN EXISTS (SELECT 1 FROM app.workout_routes route WHERE route.account_id = request.account_id AND route.workout_id = request.workout_id)
             AND EXISTS (SELECT 1 FROM app.workout_route_points point WHERE point.account_id = request.account_id AND point.workout_id = request.workout_id)
             THEN state.target_job_id END
    FROM app.adopted_workout_coverage_refreshes request
    JOIN app.source_files file ON file.id = request.source_file_id AND file.account_id = request.account_id
    LEFT JOIN app.workout_coverage_states state ON state.workout_id = request.workout_id AND state.account_id = request.account_id
    LEFT JOIN app.jobs child ON child.id = state.target_job_id AND child.account_id = state.account_id
    WHERE request.account_id = target_account_id AND request.workout_id = target_workout_id AND request.event_id = target_event_id
    ON CONFLICT (event_id) DO UPDATE SET acknowledged_at = EXCLUDED.acknowledged_at,
        coverage_parent_job_id = EXCLUDED.coverage_parent_job_id,coverage_child_job_id = EXCLUDED.coverage_child_job_id;
    UPDATE app.adopted_coverage_handoff_receipts receipt SET acknowledged_at = clock_timestamp(),
        coverage_parent_job_id = current_receipt.coverage_parent_job_id,coverage_child_job_id = current_receipt.coverage_child_job_id
    FROM app.adopted_coverage_handoff_receipts current_receipt WHERE current_receipt.event_id = target_event_id
      AND receipt.account_id = target_account_id AND receipt.workout_id = target_workout_id
      AND receipt.source_id=current_receipt.source_id AND receipt.acknowledged_at IS NULL;
    UPDATE app.ingest_coverage_handoffs handoff SET acknowledged_at = clock_timestamp(),
        coverage_parent_job_id = receipt.coverage_parent_job_id,coverage_child_job_id = receipt.coverage_child_job_id,
        outcome = CASE WHEN receipt.coverage_child_job_id IS NULL THEN 'unavailable' ELSE 'existing' END
    FROM app.adopted_coverage_handoff_receipts receipt WHERE receipt.event_id = target_event_id
      AND receipt.account_id = target_account_id AND receipt.workout_id = target_workout_id
      AND handoff.account_id = target_account_id AND handoff.workout_id = target_workout_id AND handoff.acknowledged_at IS NULL
      AND handoff.source_id=receipt.source_id AND handoff.origin='adopted'
      AND handoff.source_file_id=receipt.source_file_id AND handoff.ingest_job_id=receipt.ingest_job_id
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.ingest_coverage_candidates(target_account_id uuid,target_job_id uuid)
RETURNS TABLE(workout_id uuid,event_id uuid,origin text,source_id uuid,source_file_id uuid,ingest_job_id uuid)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
    -- The file is immutable import provenance, not necessarily the workout's
    -- latest display-repair pointer. Equal content must not force unchanged work.
    WITH normal AS (
        SELECT DISTINCT ON (handoff.workout_id) handoff.workout_id,handoff.event_id,'normal'::text origin,
            handoff.source_id,handoff.source_file_id,handoff.ingest_job_id
        FROM app.ingest_coverage_handoffs handoff
        WHERE handoff.account_id = target_account_id AND handoff.origin = 'normal'
          AND handoff.acknowledged_at IS NULL AND handoff.next_attempt_at <= clock_timestamp()
          AND NOT EXISTS (SELECT 1 FROM app.adopted_workout_coverage_refreshes adoption
              WHERE adoption.account_id = handoff.account_id AND adoption.workout_id = handoff.workout_id)
        ORDER BY handoff.workout_id,handoff.ordinal DESC
    ), candidates AS (
        SELECT * FROM normal
        UNION ALL
        SELECT adoption.workout_id,adoption.event_id,'adopted',adoption.source_id,adoption.source_file_id,receipt.ingest_job_id
        FROM app.adopted_workout_coverage_refreshes adoption JOIN app.adopted_coverage_handoff_receipts receipt
          ON receipt.event_id = adoption.event_id AND receipt.account_id = adoption.account_id
            AND receipt.workout_id = adoption.workout_id AND receipt.source_id = adoption.source_id
            AND receipt.source_file_id = adoption.source_file_id
        WHERE adoption.account_id = target_account_id AND adoption.next_attempt_at <= clock_timestamp()
          -- Global discovery has no account setting. The private handoff is
          -- durable import-event proof, including backfilled legacy origins;
          -- account-only import-event RLS must not hide it from the claimer.
          AND EXISTS (SELECT 1 FROM app.ingest_coverage_handoffs proof
              WHERE proof.account_id=adoption.account_id AND proof.workout_id=adoption.workout_id
                AND proof.source_id=adoption.source_id AND proof.source_file_id=adoption.source_file_id
                AND proof.ingest_job_id=receipt.ingest_job_id AND proof.origin='adopted')
    )
    SELECT candidate.* FROM candidates candidate JOIN app.jobs job ON job.id = candidate.ingest_job_id AND job.account_id = target_account_id
    JOIN app.workouts workout ON workout.id = candidate.workout_id AND workout.account_id = target_account_id
        AND workout.source_id = candidate.source_id AND workout.deletion_requested_at IS NULL
    JOIN app.sources source ON source.id = workout.source_id AND source.account_id = target_account_id AND source.deleted_at IS NULL
    WHERE ((target_job_id IS NOT NULL AND job.id = target_job_id)
       OR (target_job_id IS NULL AND app.ingest_coverage_root_terminal(target_account_id,job.parent_job_id)))
      AND (candidate.origin = 'adopted' OR EXISTS (SELECT 1 FROM app.ingest_coverage_handoffs handoff
        WHERE handoff.event_id = candidate.event_id AND handoff.content_sha256 = workout.content_sha256))
$function$;
-- +goose StatementEnd

-- Invalid terminal origins must remain discoverable without eligible targets.
-- +goose StatementBegin
CREATE FUNCTION app.ingest_coverage_cleanup_due(target_account_id uuid) RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
    SELECT EXISTS (SELECT 1 FROM app.ingest_coverage_handoffs handoff
        JOIN app.jobs source_job ON source_job.id=handoff.ingest_job_id AND source_job.account_id=handoff.account_id
        JOIN app.workouts workout ON workout.id=handoff.workout_id AND workout.account_id=handoff.account_id
        LEFT JOIN app.sources source ON source.id=handoff.source_id AND source.account_id=handoff.account_id
        WHERE handoff.account_id=target_account_id AND handoff.acknowledged_at IS NULL
          AND app.ingest_coverage_root_terminal(target_account_id,source_job.parent_job_id)
          AND (workout.source_id IS DISTINCT FROM handoff.source_id OR workout.deletion_requested_at IS NOT NULL
              OR source.id IS NULL OR source.deleted_at IS NOT NULL
              OR (handoff.origin='normal' AND handoff.content_sha256<>workout.content_sha256 AND EXISTS (
                  SELECT 1 FROM app.ingest_coverage_handoffs successor WHERE successor.account_id=handoff.account_id
                    AND successor.workout_id=handoff.workout_id AND successor.source_id=workout.source_id
                    AND successor.content_sha256=workout.content_sha256 AND successor.ordinal>handoff.ordinal))))
$function$;
-- +goose StatementEnd

-- A terminal root may owe cleanup, not matching. Bound each pass and retire
-- only immutable old origin tuples, never a replacement owner's event or force.
-- +goose StatementBegin
CREATE FUNCTION app.cleanup_ingest_coverage_origins(target_account_id uuid,target_job_id uuid,claiming_worker text,
    current_lease_token uuid,reconciliation_revision bigint) RETURNS integer
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
DECLARE retired record; receipt record; cleaned integer:=0; retired_roots uuid[]:='{}';
BEGIN
    IF NOT app.adopted_coverage_authority_current(target_account_id,target_job_id,claiming_worker,current_lease_token,reconciliation_revision) THEN
        RAISE EXCEPTION 'coverage cleanup authority lost' USING ERRCODE='42501';
    END IF;
    IF target_job_id IS NOT NULL OR NOT app.ingest_coverage_cleanup_due(target_account_id) THEN RETURN 0; END IF;
    PERFORM 1 FROM app.accounts WHERE id=target_account_id AND state='active' FOR UPDATE NOWAIT;
    PERFORM 1 FROM app.coverage_reconciliation_accounts WHERE account_id=target_account_id FOR UPDATE NOWAIT;
    FOR retired IN SELECT handoff.*,source_job.parent_job_id cleanup_root,
        CASE WHEN workout.source_id IS DISTINCT FROM handoff.source_id THEN 'superseded'
             WHEN workout.deletion_requested_at IS NOT NULL OR source.id IS NULL OR source.deleted_at IS NOT NULL THEN 'deleted'
             ELSE 'superseded' END cleanup_outcome
        FROM app.ingest_coverage_handoffs handoff
        JOIN app.jobs source_job ON source_job.id=handoff.ingest_job_id AND source_job.account_id=handoff.account_id
        JOIN app.workouts workout ON workout.id=handoff.workout_id AND workout.account_id=handoff.account_id
        LEFT JOIN app.sources source ON source.id=handoff.source_id AND source.account_id=handoff.account_id
        WHERE handoff.account_id=target_account_id AND handoff.acknowledged_at IS NULL
          AND app.ingest_coverage_root_terminal(target_account_id,source_job.parent_job_id)
          AND (workout.source_id IS DISTINCT FROM handoff.source_id OR workout.deletion_requested_at IS NOT NULL
              OR source.id IS NULL OR source.deleted_at IS NOT NULL
              OR (handoff.origin='normal' AND handoff.content_sha256<>workout.content_sha256 AND EXISTS (
                  SELECT 1 FROM app.ingest_coverage_handoffs successor WHERE successor.account_id=handoff.account_id
                    AND successor.workout_id=handoff.workout_id AND successor.source_id=workout.source_id
                    AND successor.content_sha256=workout.content_sha256 AND successor.ordinal>handoff.ordinal)))
        ORDER BY handoff.workout_id,handoff.ordinal LIMIT 1000 FOR UPDATE OF workout,handoff NOWAIT
    LOOP
        UPDATE app.ingest_coverage_handoffs handoff SET acknowledged_at=clock_timestamp(),
            coverage_parent_job_id=NULL,coverage_child_job_id=NULL,outcome=retired.cleanup_outcome
        WHERE handoff.event_id=retired.event_id AND handoff.account_id=target_account_id
          AND handoff.workout_id=retired.workout_id AND handoff.source_id=retired.source_id
          AND handoff.source_file_id=retired.source_file_id AND handoff.ingest_job_id=retired.ingest_job_id
          AND handoff.content_sha256=retired.content_sha256 AND handoff.ordinal=retired.ordinal AND handoff.acknowledged_at IS NULL;
        IF NOT FOUND THEN CONTINUE; END IF;
        cleaned:=cleaned+1;
        retired_roots:=array_append(retired_roots,retired.cleanup_root);
        IF retired.origin='adopted' THEN
            FOR receipt IN SELECT saved.* FROM app.adopted_coverage_handoff_receipts saved
                WHERE saved.account_id=target_account_id AND saved.workout_id=retired.workout_id
                  AND saved.source_id=retired.source_id AND saved.source_file_id=retired.source_file_id
                  AND saved.ingest_job_id=retired.ingest_job_id AND saved.acknowledged_at IS NULL
                  AND (saved.import_event_id=retired.event_id OR saved.import_event_id IS NULL)
                ORDER BY saved.event_id FOR UPDATE NOWAIT
            LOOP
                UPDATE app.adopted_coverage_handoff_receipts saved SET acknowledged_at=clock_timestamp(),
                    coverage_parent_job_id=NULL,coverage_child_job_id=NULL
                WHERE saved.event_id=receipt.event_id AND saved.account_id=target_account_id
                  AND saved.workout_id=retired.workout_id AND saved.source_id=retired.source_id
                  AND saved.source_file_id=retired.source_file_id AND saved.ingest_job_id=retired.ingest_job_id;
                DELETE FROM app.adopted_workout_coverage_refreshes request WHERE request.event_id=receipt.event_id
                  AND request.account_id=target_account_id AND request.workout_id=retired.workout_id
                  AND request.source_id=retired.source_id AND request.source_file_id=retired.source_file_id;
            END LOOP;
        END IF;
        DELETE FROM app.ingest_coverage_prepared prepared WHERE prepared.account_id=target_account_id
          AND prepared.workout_id=retired.workout_id AND prepared.source_id=retired.source_id
          AND prepared.source_file_id=retired.source_file_id AND prepared.ingest_job_id=retired.ingest_job_id
          AND ((prepared.origin='normal' AND prepared.event_id=retired.event_id)
              OR (prepared.origin='adopted' AND EXISTS (SELECT 1 FROM app.adopted_coverage_handoff_receipts saved
                  WHERE saved.event_id=prepared.event_id AND saved.account_id=target_account_id
                    AND saved.workout_id=retired.workout_id AND saved.source_id=retired.source_id
                    AND saved.source_file_id=retired.source_file_id AND saved.ingest_job_id=retired.ingest_job_id
                    AND (saved.import_event_id=retired.event_id OR saved.import_event_id IS NULL))));
    END LOOP;
    DELETE FROM app.ingest_coverage_builders builder WHERE builder.account_id=target_account_id
        AND builder.root_job_id=ANY(retired_roots)
        AND NOT EXISTS (SELECT 1 FROM app.ingest_coverage_prepared prepared WHERE prepared.builder_id=builder.builder_id)
        AND NOT EXISTS (SELECT 1 FROM app.ingest_coverage_candidates(target_account_id,NULL) candidate
            JOIN app.jobs source_job ON source_job.id=candidate.ingest_job_id AND source_job.account_id=target_account_id
            WHERE source_job.parent_job_id=builder.root_job_id);
    IF NOT app.adopted_coverage_authority_current(target_account_id,NULL,claiming_worker,current_lease_token,reconciliation_revision) THEN
        RAISE EXCEPTION 'coverage cleanup authority expired' USING ERRCODE='40001';
    END IF;
    RETURN cleaned;
END;
$function$;
-- +goose StatementEnd

-- Pipeline input is bounded by the parser, not the matcher's smaller CPU limit.
-- Keep the canonical telemetry encoding; never hash a truncated route prefix.
-- +goose StatementBegin
CREATE FUNCTION app.ingest_coverage_snapshot_digest(target_account_id uuid,target_workout_id uuid)
RETURNS bytea LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
DECLARE current_digest bytea;
BEGIN
    WITH points AS MATERIALIZED (
        SELECT point.* FROM app.workout_route_points point
        WHERE point.account_id = target_account_id AND point.workout_id = target_workout_id
        ORDER BY point.sequence LIMIT 250001
    ), encoded AS (
        SELECT point.*,pg_catalog.convert_to(
            to_char(point.recorded_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS') ||
            CASE WHEN to_char(point.recorded_at AT TIME ZONE 'UTC','US') = '000000' THEN ''
                 ELSE '.' || rtrim(to_char(point.recorded_at AT TIME ZONE 'UTC','US'),'0') END || 'Z','UTF8') AS timestamp_bytes
        FROM points point
    )
    SELECT CASE WHEN count(*) <= 250000 THEN pg_catalog.sha256(pg_catalog.int8send(count(*)) ||
        COALESCE(string_agg(pg_catalog.int8send(point.sequence::bigint) ||
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
            ''::bytea ORDER BY point.sequence),''::bytea)) END INTO current_digest FROM encoded point;
    IF current_digest IS NULL THEN
        RAISE EXCEPTION 'coverage pipeline route exceeds parser bound 250000' USING ERRCODE = '22023';
    END IF;
    RETURN current_digest;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.read_ingest_coverage_batch(
    target_account_id uuid,target_job_id uuid,claiming_worker text,current_lease_token uuid,
    reconciliation_revision bigint,after_workout_id uuid,size integer,flush boolean
) RETURNS TABLE(workout_id uuid,event_id uuid,origin text,content_sha256 bytea,ownership_epoch bigint)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
BEGIN
    IF size IS NULL OR size NOT BETWEEN 1 AND 1000 OR flush IS NULL THEN
        RAISE EXCEPTION 'invalid coverage batch page' USING ERRCODE = '22023';
    END IF;
    IF NOT app.adopted_coverage_authority_current(target_account_id,target_job_id,claiming_worker,current_lease_token,reconciliation_revision)
    THEN RAISE EXCEPTION 'coverage batch authority lost' USING ERRCODE = '42501'; END IF;
    PERFORM app.cleanup_ingest_coverage_origins(target_account_id,target_job_id,claiming_worker,current_lease_token,reconciliation_revision);
    RETURN QUERY SELECT candidate.workout_id,candidate.event_id,candidate.origin,workout.content_sha256,account.workout_ownership_epoch
    FROM app.ingest_coverage_candidates(target_account_id,target_job_id) candidate
    JOIN app.workouts workout ON workout.id = candidate.workout_id AND workout.account_id = target_account_id
    JOIN app.accounts account ON account.id = workout.account_id
    WHERE (after_workout_id IS NULL OR candidate.workout_id > after_workout_id)
      AND NOT EXISTS (SELECT 1 FROM app.ingest_coverage_prepared prepared
          WHERE prepared.account_id=target_account_id AND prepared.workout_id=candidate.workout_id
            AND prepared.event_id=candidate.event_id AND prepared.origin=candidate.origin)
    ORDER BY candidate.workout_id LIMIT size;
END;
$function$;
-- +goose StatementEnd

-- One route at a time: all coordinates, but no telemetry JSON or batch-wide
-- point buffers. STABLE makes content, full hash and coordinates one snapshot.
-- +goose StatementBegin
CREATE FUNCTION app.read_ingest_coverage_route_snapshot(
    target_account_id uuid,target_job_id uuid,claiming_worker text,current_lease_token uuid,
    reconciliation_revision bigint,target_workout_id uuid,target_event_id uuid,target_origin text
) RETURNS TABLE(content_sha256 bytea,ownership_epoch bigint,route_input_sha256 bytea,point_count integer,coordinates jsonb)
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
BEGIN
    IF target_origin IS NULL OR target_origin NOT IN ('normal','adopted') THEN
        RAISE EXCEPTION 'invalid coverage snapshot origin' USING ERRCODE = '22023';
    END IF;
    IF NOT app.adopted_coverage_authority_current(target_account_id,target_job_id,claiming_worker,current_lease_token,reconciliation_revision)
    THEN RAISE EXCEPTION 'coverage snapshot authority lost' USING ERRCODE = '42501'; END IF;
    RETURN QUERY SELECT workout.content_sha256,account.workout_ownership_epoch,
        app.ingest_coverage_snapshot_digest(target_account_id,target_workout_id),route.point_count,route.coordinates
    FROM app.ingest_coverage_candidates(target_account_id,target_job_id) candidate
    JOIN app.workouts workout ON workout.id = candidate.workout_id AND workout.account_id = target_account_id
    JOIN app.accounts account ON account.id = workout.account_id
    CROSS JOIN LATERAL (
        SELECT count(*)::integer point_count,COALESCE(jsonb_agg(jsonb_build_array(point.longitude,point.latitude) ORDER BY point.sequence),'[]'::jsonb) coordinates
        FROM (SELECT point.sequence,point.longitude,point.latitude FROM app.workout_route_points point
            WHERE point.account_id = target_account_id AND point.workout_id = target_workout_id ORDER BY point.sequence LIMIT 250001) point
    ) route
    WHERE candidate.workout_id = target_workout_id AND candidate.event_id = target_event_id AND candidate.origin = target_origin;
    IF NOT FOUND THEN RAISE EXCEPTION 'coverage snapshot event changed' USING ERRCODE = '40001'; END IF;
END;
$function$;
-- +goose StatementEnd

-- Ordinary future-generation sweeps need the same raw-input domain after a
-- missing-maps handoff was acknowledged. This is readiness, not matcher work.
-- +goose StatementBegin
CREATE FUNCTION app.read_coverage_reconciliation_snapshot(
    target_account_id uuid,reconciliation_revision bigint,claiming_worker text,current_lease_token uuid,
    target_workout_id uuid,target_route_revision bigint,target_route_digest bytea
) RETURNS TABLE(content_sha256 bytea,ownership_epoch bigint,route_input_sha256 bytea,point_count integer,coordinates jsonb)
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
BEGIN
    IF NOT app.adopted_coverage_authority_current(target_account_id,NULL,claiming_worker,current_lease_token,reconciliation_revision)
    THEN RAISE EXCEPTION 'coverage snapshot reconciliation authority lost' USING ERRCODE = '42501'; END IF;
    RETURN QUERY SELECT workout.content_sha256,account.workout_ownership_epoch,
        app.ingest_coverage_snapshot_digest(target_account_id,target_workout_id),route.point_count,route.coordinates
    FROM app.workouts workout JOIN app.accounts account ON account.id = workout.account_id
    JOIN app.sources source ON source.id = workout.source_id AND source.account_id = workout.account_id AND source.deleted_at IS NULL
    LEFT JOIN app.workout_coverage_states state ON state.workout_id = workout.id AND state.account_id = workout.account_id
    CROSS JOIN LATERAL (
        SELECT count(*)::integer point_count,COALESCE(jsonb_agg(jsonb_build_array(point.longitude,point.latitude) ORDER BY point.sequence),'[]'::jsonb) coordinates
        FROM (SELECT point.sequence,point.longitude,point.latitude FROM app.workout_route_points point
            WHERE point.account_id = target_account_id AND point.workout_id = target_workout_id ORDER BY point.sequence LIMIT 250001) point
    ) route
    WHERE workout.account_id = target_account_id AND workout.id = target_workout_id AND workout.deletion_requested_at IS NULL
      AND ((state.route_input_revision = target_route_revision AND state.route_input_sha256 = target_route_digest)
        OR (state.workout_id IS NULL AND target_route_revision = 0 AND target_route_digest IS NULL));
    IF NOT FOUND THEN RAISE EXCEPTION 'coverage reconciliation snapshot changed' USING ERRCODE = '40001'; END IF;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $block$
DECLARE definition text; pattern text; occurrences integer; digest_start integer; digest_end integer;
BEGIN
    definition := pg_get_functiondef('app.repair_coverage_reconciliation_input(uuid,bigint,text,uuid,uuid,bigint,bytea,bytea)'::regprocedure);
    pattern := '\mWITH\s+points\s+AS\s+MATERIALIZED\s*\(';
    SELECT count(*) INTO occurrences FROM regexp_matches(definition,pattern,'gi');
    IF occurrences <> 1 THEN RAISE EXCEPTION 'pipeline repair digest start anchor must occur exactly once (%)',occurrences; END IF;
    digest_start := regexp_instr(definition,pattern,1,1,0,'i');
    pattern := '\mFROM\s+encoded\s+point\s*;';
    SELECT count(*) INTO occurrences FROM regexp_matches(definition,pattern,'gi');
    IF occurrences <> 1 THEN RAISE EXCEPTION 'pipeline repair digest end anchor must occur exactly once (%)',occurrences; END IF;
    digest_end := regexp_instr(definition,pattern,1,1,1,'i');
    IF digest_end <= digest_start THEN RAISE EXCEPTION 'pipeline repair digest anchors are out of order'; END IF;
    definition := overlay(definition placing 'current_digest := app.ingest_coverage_snapshot_digest(target_account_id,target_workout_id);'
        FROM digest_start FOR digest_end-digest_start);
    EXECUTE definition;
END;
$block$;
-- +goose StatementEnd

-- Retain the adopted ABI, but scope legacy readers to the same source-job
-- authority as batches. Account recovery must not steal an active partial cohort.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.read_adopted_workout_coverage_refreshes(
    target_account_id uuid,target_job_id uuid,claiming_worker text,current_lease_token uuid,
    reconciliation_revision bigint,after_workout_id uuid,page_limit integer
) RETURNS TABLE(workout_id uuid,event_id uuid) LANGUAGE sql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
    SELECT candidate.workout_id,candidate.event_id FROM app.ingest_coverage_candidates(target_account_id,target_job_id) candidate
    WHERE candidate.origin = 'adopted' AND (after_workout_id IS NULL OR candidate.workout_id > after_workout_id)
      AND page_limit BETWEEN 1 AND 100
      AND app.adopted_coverage_authority_current(target_account_id,target_job_id,claiming_worker,current_lease_token,reconciliation_revision)
    ORDER BY candidate.workout_id LIMIT LEAST(page_limit,100)
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $block$
DECLARE definition text; pattern text; occurrences integer;
BEGIN
    definition := pg_get_functiondef('app.apply_adopted_workout_coverage_refresh(uuid,uuid,text,uuid,bigint,uuid,uuid,bytea,text,text,jsonb,uuid,uuid,text,text,text,double precision)'::regprocedure);
    pattern := '\mIF\s+NOT\s+FOUND\s+THEN\s+RETURN\s+true\s*;\s*END\s+IF\s*;';
    SELECT count(*) INTO occurrences FROM regexp_matches(definition,pattern,'gi');
    IF occurrences <> 1 THEN RAISE EXCEPTION 'adoption scope anchor must occur exactly once (%)',occurrences; END IF;
    definition := regexp_replace(definition,pattern,'IF NOT FOUND THEN RETURN true; END IF;
        IF request.source_id IS DISTINCT FROM workout.source_id
           OR NOT EXISTS (SELECT 1 FROM app.sources source WHERE source.id = request.source_id AND source.account_id = target_account_id AND source.deleted_at IS NULL)
           OR NOT EXISTS (SELECT 1 FROM app.ingest_coverage_candidates(target_account_id,target_job_id) candidate
               WHERE candidate.workout_id = target_workout_id AND candidate.event_id = target_event_id AND candidate.origin = ''adopted'') THEN
            RAISE EXCEPTION ''adopted coverage source or event changed'' USING ERRCODE = ''40001''; END IF;
        IF reconciliation_revision IS NULL AND NOT EXISTS (SELECT 1 FROM app.ingest_write_capabilities capability
            WHERE capability.backend_pid = pg_backend_pid() AND capability.transaction_id = txid_current()
              AND capability.account_id = target_account_id AND capability.job_id = target_job_id
              AND capability.worker_id = claiming_worker AND capability.lease_token = current_lease_token
              AND capability.runtime_version >= 28
              AND app.valid_ingest_ownership_epoch(capability.account_id,capability.ownership_epoch,capability.runtime_version)) THEN
            RAISE EXCEPTION ''adopted coverage requires a current pipeline fence'' USING ERRCODE = ''42501''; END IF;','gi');
    EXECUTE definition;
END;
$block$;
-- +goose StatementEnd

-- Private copy of the existing normal reconciler, including its live/current and
-- failed/cancelled target guards. Only the already-locked authority check changes.
-- +goose StatementBegin
DO $block$
DECLARE definition text; pattern text; occurrences integer;
BEGIN
    definition := pg_get_functiondef('app.apply_coverage_reconciliation(uuid,bigint,text,uuid,uuid,bigint,bytea,text,text,jsonb,uuid,uuid,text,text,text,double precision)'::regprocedure);
    pattern := '\mFUNCTION\s+app\s*\.\s*apply_coverage_reconciliation\s*\(';
    SELECT count(*) INTO occurrences FROM regexp_matches(definition,pattern,'gi');
    IF occurrences <> 1 THEN RAISE EXCEPTION 'normal core header anchor must occur exactly once (%)',occurrences; END IF;
    definition := regexp_replace(definition,pattern,'FUNCTION app.apply_ingest_coverage_normal(target_job_id uuid,','gi');
    pattern := '\mPERFORM\s+1\s+FROM\s+app\s*\.\s*coverage_reconciliation_accounts\s+reconciliation\M[^;]+;\s*IF\s+NOT\s+FOUND\s+THEN\s+RETURN\s+false\s*;\s*END\s+IF\s*;';
    SELECT count(*) INTO occurrences FROM regexp_matches(definition,pattern,'gi');
    IF occurrences <> 1 THEN RAISE EXCEPTION 'normal core authority anchor must occur exactly once (%)',occurrences; END IF;
    definition := regexp_replace(definition,pattern,'IF NOT app.adopted_coverage_authority_current(target_account_id,target_job_id,claiming_worker,current_lease_token,
        CASE WHEN target_job_id IS NULL THEN reconciliation_revision END) THEN RETURN false; END IF;','gi');
    EXECUTE definition;
    -- Ordinary scans cannot consume a committed obligation ahead of batching.
    definition := pg_get_functiondef('app.apply_coverage_reconciliation(uuid,bigint,text,uuid,uuid,bigint,bytea,text,text,jsonb,uuid,uuid,text,text,text,double precision)'::regprocedure);
    pattern := '\mBEGIN\M';
    SELECT count(*) INTO occurrences FROM regexp_matches(definition,pattern,'gi');
    IF occurrences <> 1 THEN RAISE EXCEPTION 'normal apply exclusion anchor must occur exactly once (%)',occurrences; END IF;
    definition := regexp_replace(definition,pattern,'BEGIN
        IF EXISTS (SELECT 1 FROM app.ingest_coverage_handoffs handoff WHERE handoff.account_id = target_account_id
            AND handoff.workout_id = target_workout_id AND handoff.acknowledged_at IS NULL) THEN RETURN false; END IF;','gi');
    EXECUTE definition;
    definition := pg_get_functiondef('app.read_coverage_reconciliation_routes(uuid,bigint,text,uuid,uuid,integer)'::regprocedure);
    pattern := '\mAND\s+workout\s*\.\s*deletion_requested_at\s+IS\s+NULL\M';
    SELECT count(*) INTO occurrences FROM regexp_matches(definition,pattern,'gi');
    IF occurrences <> 1 THEN RAISE EXCEPTION 'normal reader exclusion anchor must occur exactly once (%)',occurrences; END IF;
    definition := regexp_replace(definition,pattern,'AND workout.deletion_requested_at IS NULL
        AND NOT EXISTS (SELECT 1 FROM app.ingest_coverage_handoffs handoff WHERE handoff.account_id = workout.account_id
            AND handoff.workout_id = workout.id AND handoff.acknowledged_at IS NULL)
        AND EXISTS (SELECT 1 FROM app.sources source WHERE source.id=workout.source_id
            AND source.account_id=workout.account_id AND source.deleted_at IS NULL)','gi');
    EXECUTE definition;
END;
$block$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.ingest_coverage_existing_target(target_account_id uuid,target_workout_id uuid,target_revision bigint,
    target_digest bytea,generations jsonb,rules text,sampling text,policy text,minimum double precision)
RETURNS TABLE(parent_job_id uuid,child_job_id uuid) LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
    SELECT child.parent_job_id,child.id FROM app.workout_coverage_states state
    JOIN app.jobs child ON child.id=state.target_job_id AND child.account_id=state.account_id
    JOIN app.coverage_route_job_contexts context ON context.job_id=child.id AND context.account_id=child.account_id
    JOIN app.coverage_job_contexts parent ON parent.job_id=child.parent_job_id AND parent.account_id=child.account_id
    WHERE state.account_id=$1 AND state.workout_id=$2
      AND context.workout_id=$2 AND context.route_input_revision=$3
      AND context.route_input_sha256=$4
      AND (child.status IN ('queued','running','failed','cancelled') OR
          (child.status='succeeded' AND state.processing_state='current' AND context.result_outcome IN ('applied','no_evidence')))
      AND app.coverage_generation_vectors_equal(context.target_generations,$5)
      AND parent.matcher_contract_version=app.current_coverage_matcher_contract()
      AND parent.rules_version=$6 AND parent.sampling_version=$7
      AND parent.path_policy_version=$8 AND parent.minimum_traversal_meters=$9
    UNION ALL
    -- Match 027's cleared-pointer cancellation footprint, not just a NULL
    -- target. Ordinary metadata changes must not reverse explicit cancellation.
    SELECT child.parent_job_id,child.id FROM app.workout_coverage_states state
    JOIN app.coverage_route_job_contexts context ON context.account_id=state.account_id AND context.workout_id=state.workout_id
    JOIN app.jobs child ON child.id=context.job_id AND child.account_id=context.account_id
    JOIN app.jobs parent_job ON parent_job.id=child.parent_job_id AND parent_job.account_id=child.account_id
    JOIN app.coverage_job_contexts parent ON parent.job_id=child.parent_job_id AND parent.account_id=child.account_id
    JOIN app.workouts workout ON workout.id=state.workout_id AND workout.account_id=state.account_id
    JOIN app.sources source ON source.id=workout.source_id AND source.account_id=workout.account_id
    LEFT JOIN LATERAL (
        SELECT jsonb_agg(jsonb_build_object('regionId',region.region_id,'generation',region.desired_osm_generation) ORDER BY region.region_id) generations
        FROM app.workout_coverage_regions region WHERE region.account_id=state.account_id AND region.workout_id=state.workout_id
    ) desired ON true
    WHERE state.account_id=$1 AND state.workout_id=$2
      AND state.target_job_id IS NULL AND state.processing_state IN ('not_started','stale')
      AND state.readiness_state='map_data_ready' AND child.kind='coverage_update_route'
      AND parent_job.kind='coverage_update'
      AND ((child.status='cancelled' AND state.processing_finished_at=child.terminal_at)
          OR (child.status IN ('queued','running') AND state.processing_finished_at IS NULL
              AND (child.cancel_requested_at IS NOT NULL OR parent_job.cancel_requested_at IS NOT NULL)))
      AND state.route_input_revision=context.route_input_revision AND state.route_input_sha256=context.route_input_sha256
      AND context.route_input_revision=$3 AND context.route_input_sha256=$4
      AND workout.deletion_requested_at IS NULL AND source.deleted_at IS NULL
      AND parent.matcher_contract_version=app.current_coverage_matcher_contract()
      AND parent.rules_version=$6 AND parent.sampling_version=$7
      AND parent.path_policy_version=$8 AND parent.minimum_traversal_meters=$9
      AND app.coverage_generation_vectors_equal(context.target_generations,$5)
      AND (state.target_revision IS NULL OR state.target_revision=context.route_input_revision)
      AND (state.target_rules_version IS NULL OR state.target_rules_version=parent.rules_version)
      AND (state.target_sampling_version IS NULL OR state.target_sampling_version=parent.sampling_version)
      AND (state.target_path_policy_version IS NULL OR state.target_path_policy_version=parent.path_policy_version)
      AND ((state.target_generations IS NOT NULL AND app.coverage_generation_vectors_equal(state.target_generations,context.target_generations))
          OR (state.target_generations IS NULL AND app.coverage_generation_vectors_equal(desired.generations,context.target_generations)))
      AND (desired.generations IS NULL OR app.coverage_generation_vectors_equal(desired.generations,context.target_generations))
      AND NOT EXISTS (SELECT 1 FROM app.coverage_route_job_contexts other_context JOIN app.jobs other
          ON other.id=other_context.job_id AND other.account_id=other_context.account_id
          WHERE other_context.account_id=context.account_id AND other_context.workout_id=context.workout_id AND other.id<>child.id
            AND (other.status IN ('queued','running') OR other_context.created_at>=context.created_at OR other.terminal_at>=child.created_at))
    UNION ALL
    SELECT NULL::uuid,NULL::uuid FROM app.workout_coverage_states state
    WHERE state.account_id=$1 AND state.workout_id=$2 AND state.processing_state='current'
      AND state.applied_route_input_revision=$3 AND state.applied_route_input_sha256=$4
      AND state.applied_rules_version=$6 AND state.applied_sampling_version=$7
      AND state.applied_path_policy_version=$8 AND app.coverage_generation_vectors_equal(state.applied_generations,$5)
      AND state.target_job_id IS NULL
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.apply_ingest_coverage_batch(
    target_account_id uuid,target_job_id uuid,claiming_worker text,current_lease_token uuid,
    reconciliation_revision bigint,ready jsonb,rules text,sampling text,policy text,minimum double precision,runtime_version integer
) RETURNS TABLE(workout_id uuid,event_id uuid,parent_job_id uuid,child_job_id uuid,outcome text)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
DECLARE item jsonb; candidate record; workout app.workouts%ROWTYPE; state app.workout_coverage_states%ROWTYPE;
DECLARE digest bytea; revision bigint; epoch bigint; source uuid;
DECLARE builder app.ingest_coverage_builders%ROWTYPE; root_id uuid; canonical_existing boolean;
BEGIN
    IF runtime_version IS NULL OR runtime_version < 28 OR runtime_version <
       (SELECT minimum_runtime_version FROM app.schema_metadata WHERE singleton) THEN
        RAISE EXCEPTION 'coverage pipeline runtime is obsolete' USING ERRCODE = '55000';
    END IF;
    IF jsonb_typeof(ready) IS DISTINCT FROM 'array' THEN
        RAISE EXCEPTION 'invalid coverage batch' USING ERRCODE = '22023';
    END IF;
    IF jsonb_array_length(ready) NOT BETWEEN 1 AND 1000 OR
       (SELECT count(DISTINCT value->>'workoutId') FROM jsonb_array_elements(ready)) <> jsonb_array_length(ready) THEN
        RAISE EXCEPTION 'invalid coverage batch count or duplicate workout' USING ERRCODE = '22023';
    END IF;
    IF rules IS DISTINCT FROM 'coverage-experimental-v1' OR sampling IS DISTINCT FROM 'coverage-sampling-experimental-v1'
       OR policy IS NULL OR policy !~ '^coverage-path-policy-experimental-v[1-9][0-9]*$' OR minimum IS NULL OR minimum NOT BETWEEN 0.1 AND 100 THEN
        RAISE EXCEPTION 'invalid coverage batch matcher contract' USING ERRCODE = '22023';
    END IF;
    -- Post-file admission always starts with account, never with a source lock.
    SELECT account.workout_ownership_epoch INTO epoch FROM app.accounts account
    WHERE account.id = target_account_id AND target_account_id = app.current_account_id() AND account.state = 'active' FOR UPDATE NOWAIT;
    IF NOT FOUND THEN RAISE EXCEPTION 'coverage batch account authority lost' USING ERRCODE = '42501'; END IF;
    IF target_job_id IS NOT NULL THEN
        IF reconciliation_revision IS NOT NULL THEN RAISE EXCEPTION 'ambiguous coverage authority' USING ERRCODE = '42501'; END IF;
        SELECT fence.source_id INTO source FROM app.fence_ingest_job(target_job_id,claiming_worker,current_lease_token,COALESCE((
            SELECT capability.runtime_version FROM app.ingest_write_capabilities capability
            WHERE capability.backend_pid = pg_backend_pid() AND capability.transaction_id = txid_current()
              AND capability.account_id = target_account_id AND capability.job_id = target_job_id
              AND capability.worker_id = claiming_worker AND capability.lease_token = current_lease_token
        ),runtime_version)) fence;
        IF NOT FOUND THEN RAISE EXCEPTION 'coverage batch ingest lease lost' USING ERRCODE = '42501'; END IF;
    ELSE
        PERFORM 1 FROM app.coverage_reconciliation_accounts reconciliation WHERE reconciliation.account_id = target_account_id FOR UPDATE NOWAIT;
    END IF;
    IF NOT app.adopted_coverage_authority_current(target_account_id,target_job_id,claiming_worker,current_lease_token,reconciliation_revision)
    THEN RAISE EXCEPTION 'coverage batch authority lost' USING ERRCODE = '42501'; END IF;
    -- Claimers take Coverage parent/child before state. Reserve every active
    -- parent (including spare coalescing buckets and running targets), then its
    -- live children, before any workout/state lock. Never wait behind a claimer
    -- while holding the state it needs; contention rolls back the whole batch.
    PERFORM 1 FROM app.jobs parent WHERE parent.account_id = target_account_id AND parent.kind = 'coverage_update'
        AND parent.status IN ('queued','running') ORDER BY parent.id FOR UPDATE NOWAIT;
    PERFORM 1 FROM app.jobs child JOIN app.jobs parent ON parent.id = child.parent_job_id AND parent.account_id = child.account_id
        WHERE parent.account_id = target_account_id AND parent.kind = 'coverage_update' AND parent.status IN ('queued','running')
          AND child.kind = 'coverage_update_route' AND child.status IN ('queued','running')
        ORDER BY parent.id,child.id FOR UPDATE OF child NOWAIT;
    -- The whole page is one atomic transaction. Sorted locks also cover mixed origins.
    FOR item IN SELECT value FROM jsonb_array_elements(ready) ORDER BY value->>'workoutId' LOOP
        IF jsonb_typeof(item) IS DISTINCT FROM 'object' OR NOT (item ?& ARRAY['workoutId','eventId','origin','contentSha256','routeDigest','ownershipEpoch','readiness','generations'])
           OR item->>'origin' IS NULL OR item->>'origin' NOT IN ('normal','adopted')
           OR item->>'readiness' IS NULL OR item->>'readiness' NOT IN ('map_data_ready','pending','unavailable')
           OR item->>'contentSha256' IS NULL OR item->>'contentSha256' !~ '^[0-9a-f]{64}$'
           OR item->>'routeDigest' IS NULL OR item->>'routeDigest' !~ '^[0-9a-f]{64}$'
           OR (item->>'readiness' = 'map_data_ready' AND (item->>'reason' IS NOT NULL
               OR jsonb_typeof(item->'catalogFingerprint') IS DISTINCT FROM 'string'
               OR item->>'catalogFingerprint' !~ '^[0-9a-f]{64}$'
               OR app.valid_coverage_generation_vector(item->'generations') IS DISTINCT FROM true))
           OR (item->>'readiness' = 'pending' AND item->>'reason' IS DISTINCT FROM 'region_not_active')
           OR (item->>'readiness' = 'unavailable' AND item->>'reason' IS DISTINCT FROM 'no_provider_region')
           OR (item->>'readiness' <> 'map_data_ready' AND item->'generations' IS DISTINCT FROM '[]'::jsonb) THEN
            RAISE EXCEPTION 'invalid coverage batch item' USING ERRCODE = '22023';
        END IF;
        workout_id := (item->>'workoutId')::uuid; event_id := (item->>'eventId')::uuid;
        parent_job_id := NULL; child_job_id := NULL; outcome := NULL;
        SELECT * INTO workout FROM app.workouts current_workout WHERE current_workout.id = workout_id
            AND current_workout.account_id = target_account_id FOR UPDATE NOWAIT;
        IF NOT FOUND OR workout.deletion_requested_at IS NOT NULL OR
           workout.content_sha256 IS DISTINCT FROM decode(item->>'contentSha256','hex') OR
           epoch IS DISTINCT FROM (item->>'ownershipEpoch')::bigint OR
           (source IS NOT NULL AND source IS DISTINCT FROM workout.source_id) OR NOT EXISTS (
               SELECT 1 FROM app.sources current_source WHERE current_source.id = workout.source_id
                 AND current_source.account_id = target_account_id AND current_source.deleted_at IS NULL) THEN
            RAISE EXCEPTION 'coverage batch workout snapshot changed' USING ERRCODE = '40001';
        END IF;
        SELECT * INTO candidate FROM app.ingest_coverage_candidates(target_account_id,target_job_id) request
          WHERE request.workout_id = apply_ingest_coverage_batch.workout_id AND request.event_id = apply_ingest_coverage_batch.event_id
            AND request.origin = item->>'origin';
        IF NOT FOUND THEN RAISE EXCEPTION 'coverage batch event changed' USING ERRCODE = '40001'; END IF;
        IF candidate.source_id IS DISTINCT FROM workout.source_id
           OR (candidate.origin = 'normal' AND NOT EXISTS (SELECT 1 FROM app.ingest_coverage_handoffs handoff
               WHERE handoff.event_id = candidate.event_id AND handoff.account_id = target_account_id
                 AND handoff.content_sha256 = workout.content_sha256 AND handoff.acknowledged_at IS NULL)) THEN
            RAISE EXCEPTION 'coverage batch file event snapshot changed' USING ERRCODE = '40001';
        END IF;
        digest := decode(item->>'routeDigest','hex');
        IF octet_length(digest) IS DISTINCT FROM 32 OR app.ingest_coverage_snapshot_digest(target_account_id,workout_id) IS DISTINCT FROM digest THEN
            RAISE EXCEPTION 'coverage batch route digest changed' USING ERRCODE = '40001';
        END IF;
        SELECT * INTO state FROM app.workout_coverage_states current_state WHERE current_state.account_id = target_account_id
            AND current_state.workout_id = apply_ingest_coverage_batch.workout_id FOR UPDATE NOWAIT;
        IF candidate.origin = 'adopted' THEN
            PERFORM 1 FROM app.adopted_workout_coverage_refreshes request WHERE request.account_id = target_account_id
                AND request.workout_id = apply_ingest_coverage_batch.workout_id AND request.event_id = apply_ingest_coverage_batch.event_id FOR UPDATE NOWAIT;
            IF NOT FOUND THEN RAISE EXCEPTION 'adoption coverage event changed' USING ERRCODE = '40001'; END IF;
        END IF;
        IF candidate.origin = 'adopted' AND (NOT EXISTS (SELECT 1 FROM app.workout_routes route
            WHERE route.account_id = target_account_id AND route.workout_id = apply_ingest_coverage_batch.workout_id)
            OR NOT EXISTS (SELECT 1 FROM app.workout_route_points point WHERE point.account_id = target_account_id
                AND point.workout_id = apply_ingest_coverage_batch.workout_id)) THEN
            -- Empty input has a real canonical hash above, but owes no matching job.
            PERFORM app.record_adopted_coverage_receipt(target_account_id,workout_id,event_id);
            DELETE FROM app.adopted_workout_coverage_refreshes request WHERE request.account_id = target_account_id
                AND request.workout_id = apply_ingest_coverage_batch.workout_id AND request.event_id = apply_ingest_coverage_batch.event_id;
            DELETE FROM app.ingest_coverage_prepared prepared WHERE prepared.account_id=target_account_id
                AND prepared.workout_id=apply_ingest_coverage_batch.workout_id;
            outcome := 'unavailable';
        ELSE
            revision := CASE WHEN state.workout_id IS NULL THEN 1 WHEN state.route_input_sha256 = digest
                THEN state.route_input_revision ELSE state.route_input_revision + 1 END;
            -- Capture the original descriptor before readiness/desired-region
            -- writes. A cleared cancellation pointer is evidence, not a reset.
            canonical_existing := false;
            IF candidate.origin='normal' AND item->>'readiness'='map_data_ready' THEN
                SELECT existing.parent_job_id,existing.child_job_id INTO parent_job_id,child_job_id
                FROM app.ingest_coverage_existing_target(target_account_id,workout_id,revision,digest,item->'generations',
                    rules,sampling,policy,minimum) existing;
                canonical_existing := FOUND;
            END IF;
            INSERT INTO app.workout_coverage_states(account_id,workout_id,route_input_revision,route_input_sha256,readiness_state)
            VALUES(target_account_id,workout_id,revision,digest,'unresolved')
            ON CONFLICT ON CONSTRAINT workout_coverage_states_pkey DO UPDATE SET route_input_revision = EXCLUDED.route_input_revision,
                route_input_sha256 = EXCLUDED.route_input_sha256,
                processing_state = CASE WHEN app.workout_coverage_states.route_input_sha256 IS DISTINCT FROM digest
                    THEN CASE WHEN app.workout_coverage_states.applied_route_input_revision IS NULL THEN 'not_started' ELSE 'stale' END
                    ELSE app.workout_coverage_states.processing_state END,updated_at = clock_timestamp();
            -- Readiness is durable even when no physical work is needed. Do not
            -- reset a canonical current/live/failed target's retry provenance.
                UPDATE app.workout_coverage_states current_state SET readiness_state = item->>'readiness',reason = item->>'reason',
                    map_data_ready_at = CASE WHEN item->>'readiness' = 'map_data_ready' THEN clock_timestamp() END,
                    processing_state = CASE WHEN item->>'readiness' <> 'map_data_ready'
                        THEN CASE WHEN current_state.applied_route_input_revision IS NULL THEN 'not_started' ELSE 'stale' END ELSE current_state.processing_state END,
                    target_job_id = CASE WHEN item->>'readiness' = 'map_data_ready' THEN current_state.target_job_id END,
                    target_revision = CASE WHEN item->>'readiness' = 'map_data_ready' THEN current_state.target_revision END,
                    target_rules_version = CASE WHEN item->>'readiness' = 'map_data_ready' THEN current_state.target_rules_version END,
                    target_sampling_version = CASE WHEN item->>'readiness' = 'map_data_ready' THEN current_state.target_sampling_version END,
                    target_path_policy_version = CASE WHEN item->>'readiness' = 'map_data_ready' THEN current_state.target_path_policy_version END,
                    target_generations = CASE WHEN item->>'readiness' = 'map_data_ready' THEN current_state.target_generations END,updated_at = clock_timestamp()
                WHERE current_state.account_id = target_account_id AND current_state.workout_id = apply_ingest_coverage_batch.workout_id;
                DELETE FROM app.workout_coverage_regions region WHERE region.account_id = target_account_id AND region.workout_id = apply_ingest_coverage_batch.workout_id;
                INSERT INTO app.workout_coverage_regions(account_id,workout_id,region_id,desired_osm_generation)
                SELECT target_account_id,workout_id,element->>'regionId',(element->>'generation')::bigint FROM jsonb_array_elements(item->'generations') element;
            IF item->>'readiness' <> 'map_data_ready' THEN
                DELETE FROM app.ingest_coverage_prepared prepared WHERE prepared.account_id=target_account_id
                    AND prepared.workout_id=apply_ingest_coverage_batch.workout_id;
                IF candidate.origin='adopted' THEN
                    UPDATE app.adopted_workout_coverage_refreshes request SET next_attempt_at = clock_timestamp() + interval '1 hour'
                    WHERE request.account_id = target_account_id AND request.workout_id = apply_ingest_coverage_batch.workout_id
                        AND request.event_id = apply_ingest_coverage_batch.event_id;
                    outcome := 'deferred';
                ELSE
                    outcome := item->>'readiness';
                END IF;
            ELSE
                IF canonical_existing THEN
                    outcome := 'existing';
                    DELETE FROM app.ingest_coverage_prepared prepared WHERE prepared.account_id=target_account_id
                        AND prepared.workout_id=apply_ingest_coverage_batch.workout_id;
                ELSE
                    SELECT job.parent_job_id INTO root_id FROM app.jobs job
                    WHERE job.id=candidate.ingest_job_id AND job.account_id=target_account_id;
                    INSERT INTO app.ingest_coverage_builders(account_id,root_job_id,batch_size,rules,sampling,policy,minimum,matcher_contract)
                    VALUES(target_account_id,root_id,app.current_coverage_batch_size(),rules,sampling,policy,minimum,app.current_coverage_matcher_contract())
                    ON CONFLICT(account_id,root_job_id) DO NOTHING;
                    SELECT * INTO builder FROM app.ingest_coverage_builders current_builder
                    WHERE current_builder.account_id=target_account_id AND current_builder.root_job_id=root_id FOR UPDATE NOWAIT;
                    IF (SELECT count(*) FROM app.ingest_coverage_prepared prepared WHERE prepared.builder_id=builder.builder_id
                        AND prepared.workout_id<>apply_ingest_coverage_batch.workout_id)>=builder.batch_size THEN
                        RAISE EXCEPTION 'eligible cohort must be sealed before further preparation' USING ERRCODE='40001';
                    END IF;
                    IF (builder.rules,builder.sampling,builder.policy,builder.minimum,builder.matcher_contract)
                        IS DISTINCT FROM (rules,sampling,policy,minimum,app.current_coverage_matcher_contract()) THEN
                        -- A changed matcher contract requires fresh external readiness,
                        -- not mixing a stale cached cohort with the new producer.
                        DELETE FROM app.ingest_coverage_prepared prepared WHERE prepared.builder_id=builder.builder_id;
                        UPDATE app.ingest_coverage_builders current_builder SET rules=apply_ingest_coverage_batch.rules,
                            sampling=apply_ingest_coverage_batch.sampling,policy=apply_ingest_coverage_batch.policy,
                            minimum=apply_ingest_coverage_batch.minimum,matcher_contract=app.current_coverage_matcher_contract()
                        WHERE current_builder.builder_id=builder.builder_id;
                    END IF;
                    INSERT INTO app.ingest_coverage_prepared(account_id,workout_id,builder_id,event_id,source_id,source_file_id,
                        ingest_job_id,origin,content_sha256,route_input_sha256,route_input_revision,ownership_epoch,catalog_fingerprint,generations)
                    VALUES(target_account_id,workout_id,builder.builder_id,event_id,candidate.source_id,candidate.source_file_id,
                        candidate.ingest_job_id,candidate.origin,workout.content_sha256,digest,revision,epoch,decode(item->>'catalogFingerprint','hex'),
                        (SELECT jsonb_agg(value ORDER BY value->>'regionId') FROM jsonb_array_elements(item->'generations')))
                    ON CONFLICT ON CONSTRAINT ingest_coverage_prepared_pkey DO UPDATE SET builder_id=EXCLUDED.builder_id,event_id=EXCLUDED.event_id,
                        source_id=EXCLUDED.source_id,source_file_id=EXCLUDED.source_file_id,ingest_job_id=EXCLUDED.ingest_job_id,
                        origin=EXCLUDED.origin,content_sha256=EXCLUDED.content_sha256,route_input_sha256=EXCLUDED.route_input_sha256,
                        route_input_revision=EXCLUDED.route_input_revision,ownership_epoch=EXCLUDED.ownership_epoch,
                        catalog_fingerprint=EXCLUDED.catalog_fingerprint,generations=EXCLUDED.generations;
                    outcome := 'prepared';
                END IF;
            END IF;
        END IF;
        IF outcome IS DISTINCT FROM 'existing' THEN
            SELECT child.parent_job_id,current_state.target_job_id INTO parent_job_id,child_job_id
            FROM app.workout_coverage_states current_state LEFT JOIN app.jobs child
              ON child.id = current_state.target_job_id AND child.account_id = current_state.account_id
            WHERE current_state.account_id = target_account_id AND current_state.workout_id = apply_ingest_coverage_batch.workout_id;
        END IF;
        IF outcome IN ('pending','unavailable','deferred','prepared') THEN parent_job_id := NULL; child_job_id := NULL; END IF;
        IF outcome NOT IN ('deferred','prepared') THEN
            UPDATE app.ingest_coverage_handoffs handoff SET acknowledged_at = clock_timestamp(),
                coverage_parent_job_id = apply_ingest_coverage_batch.parent_job_id,coverage_child_job_id = apply_ingest_coverage_batch.child_job_id,
                outcome = apply_ingest_coverage_batch.outcome
             WHERE handoff.account_id = target_account_id AND handoff.workout_id = apply_ingest_coverage_batch.workout_id
               AND handoff.source_id=candidate.source_id AND handoff.content_sha256=workout.content_sha256
               AND handoff.origin='normal' AND handoff.ordinal<=(SELECT current_handoff.ordinal
                   FROM app.ingest_coverage_handoffs current_handoff WHERE current_handoff.event_id=candidate.event_id)
               AND handoff.acknowledged_at IS NULL;
        END IF;
        RETURN NEXT;
    END LOOP;
    IF NOT app.adopted_coverage_authority_current(target_account_id,target_job_id,claiming_worker,current_lease_token,reconciliation_revision)
    THEN RAISE EXCEPTION 'coverage batch authority expired' USING ERRCODE = '40001'; END IF;
END;
$function$;
-- +goose StatementEnd

-- Lease runtime is persisted for reconciliation so an already leased old binary
-- cannot bypass the new minimum after the DDL commits.
ALTER TABLE app.coverage_reconciliation_accounts ADD COLUMN lease_runtime_version integer
CHECK (lease_runtime_version IS NULL OR lease_runtime_version >= 28);
GRANT SELECT(lease_runtime_version),UPDATE(lease_runtime_version) ON app.coverage_reconciliation_accounts TO workouts_security_owner;

-- NULL account settings must be a denied capability, never SQL's third truth
-- value: callers use this predicate both in WHERE and in PL/pgSQL admission.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.adopted_coverage_authority_current(
    target_account_id uuid,target_job_id uuid,claiming_worker text,current_lease_token uuid,reconciliation_revision bigint
) RETURNS boolean LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
    SELECT COALESCE(target_account_id = app.current_account_id() AND EXISTS (
        SELECT 1 FROM app.accounts WHERE id = target_account_id AND state = 'active'
    ) AND CASE WHEN reconciliation_revision IS NULL THEN EXISTS (
        SELECT 1 FROM app.jobs job JOIN app.jobs parent ON parent.id = job.parent_job_id AND parent.account_id = job.account_id
        JOIN app.job_config_snapshots snapshot ON snapshot.job_id = job.id AND snapshot.account_id = job.account_id
        JOIN app.sources source ON source.id = snapshot.source_id AND source.account_id = job.account_id AND source.deleted_at IS NULL
        WHERE job.id = target_job_id AND job.account_id = target_account_id
          AND job.kind IN ('manual_ingest_source','scheduled_ingest_source') AND job.status = 'running'
          AND job.worker_id = claiming_worker AND job.lease_token = current_lease_token
          AND job.lease_expires_at >= clock_timestamp() AND job.cancel_requested_at IS NULL
          AND parent.status = 'running' AND parent.cancel_requested_at IS NULL
          AND parent.kind = CASE job.kind WHEN 'manual_ingest_source' THEN 'manual_ingest' ELSE 'scheduled_ingest' END
    ) ELSE target_job_id IS NULL AND EXISTS (
        SELECT 1 FROM app.coverage_reconciliation_accounts reconciliation WHERE reconciliation.account_id = target_account_id
          AND reconciliation.active_revision = reconciliation_revision AND reconciliation.worker_id = claiming_worker
          AND reconciliation.lease_token = current_lease_token AND reconciliation.lease_expires_at >= clock_timestamp()
          AND reconciliation.lease_runtime_version >= 28
          AND reconciliation.lease_runtime_version >= (SELECT minimum_runtime_version FROM app.schema_metadata WHERE singleton)
    ) END,false)
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.claim_coverage_reconciliation(claiming_worker text,new_lease_token uuid,lease_duration interval,runtime_version integer)
RETURNS TABLE(account_id uuid,reconciliation_revision bigint,cursor_workout_id uuid) LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
DECLARE candidate app.coverage_reconciliation_accounts%ROWTYPE;
BEGIN
    IF runtime_version IS NULL OR runtime_version < 28 OR runtime_version <
       (SELECT minimum_runtime_version FROM app.schema_metadata WHERE singleton) THEN
        RAISE EXCEPTION 'coverage pipeline runtime 28 or newer is required' USING ERRCODE = '55000';
    END IF;
    IF claiming_worker IS NULL OR claiming_worker = '' OR new_lease_token IS NULL OR lease_duration IS NULL OR
       lease_duration NOT BETWEEN interval '30 seconds' AND interval '15 minutes' THEN
        RAISE EXCEPTION 'invalid coverage reconciliation claim' USING ERRCODE = '22023';
    END IF;
    SELECT reconciliation.* INTO candidate FROM app.coverage_reconciliation_accounts reconciliation
    JOIN app.accounts account ON account.id = reconciliation.account_id AND account.state = 'active'
    WHERE (reconciliation.worker_id IS NULL OR reconciliation.lease_expires_at < clock_timestamp())
      AND (reconciliation.next_scan_at <= transaction_timestamp() OR (reconciliation.last_failure_code IS NULL
        AND (EXISTS (SELECT 1 FROM app.ingest_coverage_candidates(reconciliation.account_id,NULL))
          OR app.ingest_coverage_cleanup_due(reconciliation.account_id)
          OR EXISTS (SELECT 1 FROM app.ingest_coverage_builders builder
              WHERE builder.account_id=reconciliation.account_id
                AND EXISTS (SELECT 1 FROM app.ingest_coverage_prepared item WHERE item.builder_id=builder.builder_id)
                AND app.ingest_coverage_root_terminal(reconciliation.account_id,builder.root_job_id)))))
    ORDER BY CASE WHEN reconciliation.desired_revision > reconciliation.completed_revision THEN 0 ELSE 1 END,
        reconciliation.next_scan_at,reconciliation.updated_at,reconciliation.account_id
    LIMIT 1 FOR UPDATE OF reconciliation SKIP LOCKED;
    IF NOT FOUND THEN RETURN; END IF;
    IF candidate.active_revision IS DISTINCT FROM candidate.desired_revision OR candidate.completed_revision >= candidate.desired_revision
       OR EXISTS (SELECT 1 FROM app.ingest_coverage_candidates(candidate.account_id,NULL))
       OR app.ingest_coverage_cleanup_due(candidate.account_id)
       OR EXISTS (SELECT 1 FROM app.ingest_coverage_builders builder WHERE builder.account_id=candidate.account_id
           AND EXISTS (SELECT 1 FROM app.ingest_coverage_prepared item WHERE item.builder_id=builder.builder_id)
           AND app.ingest_coverage_root_terminal(candidate.account_id,builder.root_job_id)) THEN
        candidate.active_revision := candidate.desired_revision; candidate.cursor_workout_id := NULL;
    END IF;
    UPDATE app.coverage_reconciliation_accounts reconciliation SET active_revision = candidate.active_revision,
        cursor_workout_id = candidate.cursor_workout_id,worker_id = claiming_worker,lease_token = new_lease_token,
        heartbeat_at = clock_timestamp(),lease_expires_at = clock_timestamp() + lease_duration,lease_runtime_version = runtime_version,
        last_failure_code = NULL,last_failure_summary = NULL,updated_at = transaction_timestamp()
    WHERE reconciliation.account_id = candidate.account_id;
    account_id := candidate.account_id; reconciliation_revision := candidate.active_revision;
    cursor_workout_id := candidate.cursor_workout_id; RETURN NEXT;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
DO $block$
DECLARE definition text; pattern text; occurrences integer; signature text;
BEGIN
    FOREACH signature IN ARRAY ARRAY[
        'app.apply_coverage_reconciliation(uuid,bigint,text,uuid,uuid,bigint,bytea,text,text,jsonb,uuid,uuid,text,text,text,double precision)',
        'app.repair_coverage_reconciliation_input(uuid,bigint,text,uuid,uuid,bigint,bytea,bytea)',
        'app.read_coverage_reconciliation_routes(uuid,bigint,text,uuid,uuid,integer)',
        'app.read_coverage_reconciliation_route(uuid,bigint,text,uuid,uuid,bigint,bytea)',
        'app.advance_coverage_reconciliation(uuid,bigint,text,uuid,uuid,boolean,interval)'
    ] LOOP
        definition := pg_get_functiondef(signature::regprocedure);
        pattern := '\mreconciliation\s*\.\s*lease_expires_at\s*>=\s*clock_timestamp\s*\(\s*\)';
        SELECT count(*) INTO occurrences FROM regexp_matches(definition,pattern,'gi');
        IF occurrences < 1 THEN RAISE EXCEPTION 'reconciliation runtime lease anchor missing: %',signature; END IF;
        definition := regexp_replace(definition,pattern,'reconciliation.lease_expires_at >= clock_timestamp()
            AND reconciliation.lease_runtime_version >= 28
            AND reconciliation.lease_runtime_version >= (SELECT minimum_runtime_version FROM app.schema_metadata WHERE singleton)','gi');
        EXECUTE definition;
    END LOOP;
    definition := pg_get_functiondef('app.fail_coverage_reconciliation(uuid,bigint,text,uuid,text,text,interval)'::regprocedure);
    pattern := '\mreconciliation\s*\.\s*lease_token\s*=\s*current_lease_token\M';
    SELECT count(*) INTO occurrences FROM regexp_matches(definition,pattern,'gi');
    IF occurrences <> 1 THEN RAISE EXCEPTION 'reconciliation failure runtime anchor must occur exactly once (%)',occurrences; END IF;
    definition := regexp_replace(definition,pattern,'reconciliation.lease_token = current_lease_token
        AND reconciliation.lease_runtime_version >= 28
        AND reconciliation.lease_runtime_version >= (SELECT minimum_runtime_version FROM app.schema_metadata WHERE singleton)','gi');
    EXECUTE definition;
    -- Coverage's shipped floor remains recognizable; metadata raises it to 28.
    definition := pg_get_functiondef('app.claim_next_coverage_route(text,uuid,interval,integer)'::regprocedure);
    pattern := '\mBEGIN\M';
    SELECT count(*) INTO occurrences FROM regexp_matches(definition,pattern,'gi');
    IF occurrences <> 1 THEN RAISE EXCEPTION 'coverage claim metadata anchor must occur exactly once (%)',occurrences; END IF;
    definition := regexp_replace(definition,pattern,'BEGIN
        IF runtime_version IS NULL OR runtime_version < (SELECT minimum_runtime_version FROM app.schema_metadata WHERE singleton)
        THEN RAISE EXCEPTION ''coverage pipeline runtime is obsolete'' USING ERRCODE = ''55000''; END IF;','gi');
    EXECUTE definition;
END;
$block$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.heartbeat_coverage_reconciliation(target_account_id uuid,reconciliation_revision bigint,
    claiming_worker text,current_lease_token uuid,lease_duration interval) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
BEGIN
    IF lease_duration IS NULL OR lease_duration NOT BETWEEN interval '30 seconds' AND interval '15 minutes' THEN RETURN false; END IF;
    UPDATE app.coverage_reconciliation_accounts reconciliation SET heartbeat_at = clock_timestamp(),lease_expires_at = clock_timestamp() + lease_duration
    WHERE reconciliation.account_id = target_account_id AND reconciliation.active_revision = reconciliation_revision
      AND reconciliation.worker_id = claiming_worker AND reconciliation.lease_token = current_lease_token
      AND reconciliation.lease_expires_at >= clock_timestamp() AND reconciliation.lease_runtime_version >= 28
      AND reconciliation.lease_runtime_version >= (SELECT minimum_runtime_version FROM app.schema_metadata WHERE singleton);
    RETURN FOUND;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.read_pending_ingest_coverage_workouts(target_account_id uuid,target_job_id uuid,claiming_worker text,current_lease_token uuid)
RETURNS TABLE(workout_id uuid) LANGUAGE sql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
    SELECT handoff.workout_id FROM app.ingest_coverage_handoffs handoff
    WHERE handoff.account_id = target_account_id AND handoff.acknowledged_at IS NULL
      AND app.adopted_coverage_authority_current(target_account_id,target_job_id,claiming_worker,current_lease_token,NULL)
    UNION
    SELECT request.workout_id FROM app.adopted_workout_coverage_refreshes request WHERE request.account_id = target_account_id
      AND app.adopted_coverage_authority_current(target_account_id,target_job_id,claiming_worker,current_lease_token,NULL)
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.defer_ingest_coverage_batch(target_account_id uuid,target_job_id uuid,claiming_worker text,current_lease_token uuid,
    reconciliation_revision bigint,items jsonb,runtime_version integer) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
DECLARE item jsonb; candidate record; epoch bigint; source uuid; workout app.workouts%ROWTYPE;
BEGIN
    IF runtime_version IS NULL OR runtime_version<28 OR runtime_version<
        (SELECT minimum_runtime_version FROM app.schema_metadata WHERE singleton) THEN
        RAISE EXCEPTION 'coverage pipeline runtime is obsolete' USING ERRCODE='55000';
    END IF;
    IF jsonb_typeof(items) IS DISTINCT FROM 'array' THEN RAISE EXCEPTION 'invalid coverage defer batch' USING ERRCODE = '22023'; END IF;
    IF jsonb_array_length(items) NOT BETWEEN 1 AND 1000 THEN RAISE EXCEPTION 'invalid coverage defer count' USING ERRCODE = '22023'; END IF;
    SELECT account.workout_ownership_epoch INTO epoch FROM app.accounts account
    WHERE account.id = target_account_id AND target_account_id = app.current_account_id() AND account.state = 'active' FOR UPDATE NOWAIT;
    IF NOT FOUND THEN RAISE EXCEPTION 'coverage defer account authority lost' USING ERRCODE = '42501'; END IF;
    IF target_job_id IS NOT NULL THEN
        IF reconciliation_revision IS NOT NULL THEN RAISE EXCEPTION 'ambiguous coverage authority' USING ERRCODE = '42501'; END IF;
        SELECT fence.source_id INTO source FROM app.fence_ingest_job(target_job_id,claiming_worker,current_lease_token,COALESCE((
            SELECT capability.runtime_version FROM app.ingest_write_capabilities capability
            WHERE capability.backend_pid = pg_backend_pid() AND capability.transaction_id = txid_current()
              AND capability.account_id = target_account_id AND capability.job_id = target_job_id
              AND capability.worker_id = claiming_worker AND capability.lease_token = current_lease_token
        ),runtime_version)) fence;
        IF NOT FOUND THEN RAISE EXCEPTION 'coverage defer ingest lease lost' USING ERRCODE = '42501'; END IF;
    ELSE
        PERFORM 1 FROM app.coverage_reconciliation_accounts reconciliation WHERE reconciliation.account_id = target_account_id FOR UPDATE NOWAIT;
    END IF;
    IF NOT app.adopted_coverage_authority_current(target_account_id,target_job_id,claiming_worker,current_lease_token,reconciliation_revision)
    THEN RAISE EXCEPTION 'coverage defer authority lost' USING ERRCODE = '42501'; END IF;
    FOR item IN SELECT value FROM jsonb_array_elements(items) ORDER BY value->>'workoutId' LOOP
        SELECT current_workout.* INTO workout FROM app.workouts current_workout WHERE current_workout.id = (item->>'workoutId')::uuid
          AND current_workout.account_id = target_account_id FOR UPDATE NOWAIT;
        IF NOT FOUND OR workout.deletion_requested_at IS NOT NULL OR workout.content_sha256 IS DISTINCT FROM decode(item->>'contentSha256','hex')
           OR epoch IS DISTINCT FROM (item->>'ownershipEpoch')::bigint OR (source IS NOT NULL AND source IS DISTINCT FROM workout.source_id)
            OR app.ingest_coverage_snapshot_digest(target_account_id,workout.id) IS DISTINCT FROM decode(item->>'routeDigest','hex') THEN
            RAISE EXCEPTION 'coverage defer snapshot changed' USING ERRCODE = '40001';
        END IF;
        SELECT * INTO candidate FROM app.ingest_coverage_candidates(target_account_id,target_job_id) request
          WHERE request.workout_id = workout.id AND request.event_id = (item->>'eventId')::uuid AND request.origin = item->>'origin';
        IF NOT FOUND THEN RAISE EXCEPTION 'coverage defer event changed' USING ERRCODE = '40001'; END IF;
        IF candidate.source_id IS DISTINCT FROM workout.source_id
           OR NOT EXISTS (SELECT 1 FROM app.sources current_source WHERE current_source.id = workout.source_id
                AND current_source.account_id = target_account_id AND current_source.deleted_at IS NULL) THEN
            RAISE EXCEPTION 'coverage defer file event changed' USING ERRCODE = '40001';
        END IF;
        IF candidate.origin = 'normal' THEN
            UPDATE app.ingest_coverage_handoffs SET next_attempt_at = clock_timestamp() + interval '1 hour'
            WHERE account_id = target_account_id AND workout_id = workout.id AND acknowledged_at IS NULL;
        ELSE
            UPDATE app.adopted_workout_coverage_refreshes SET next_attempt_at = clock_timestamp() + interval '1 hour'
            WHERE account_id = target_account_id AND workout_id = workout.id AND event_id = candidate.event_id;
        END IF;
    END LOOP;
    IF NOT app.adopted_coverage_authority_current(target_account_id,target_job_id,claiming_worker,current_lease_token,reconciliation_revision)
    THEN RAISE EXCEPTION 'coverage defer authority expired' USING ERRCODE = '40001'; END IF;
    RETURN true;
END;
$function$;
-- +goose StatementEnd

-- EOF repairs record ordinary obligations, not fabricated import history or
-- force requests. A repair event belongs to this visible ingest root, while its
-- source-file identity remains the workout's genuine import provenance.
-- +goose StatementBegin
CREATE FUNCTION app.read_ingest_coverage_source_id(target_account_id uuid,target_job_id uuid,claiming_worker text,
    current_lease_token uuid) RETURNS uuid LANGUAGE sql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
    SELECT snapshot.source_id FROM app.job_config_snapshots snapshot
    WHERE snapshot.job_id=target_job_id AND snapshot.account_id=target_account_id
      AND app.adopted_coverage_authority_current(target_account_id,target_job_id,claiming_worker,current_lease_token,NULL)
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.record_ingest_coverage_repair(target_account_id uuid,target_job_id uuid,claiming_worker text,
    current_lease_token uuid,target_workout_id uuid,runtime_version integer) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
DECLARE source uuid; workout app.workouts%ROWTYPE;
BEGIN
    IF runtime_version IS NULL OR runtime_version<28 OR runtime_version<
        (SELECT minimum_runtime_version FROM app.schema_metadata WHERE singleton) THEN
        RAISE EXCEPTION 'coverage pipeline runtime is obsolete' USING ERRCODE='55000';
    END IF;
    IF target_account_id IS DISTINCT FROM app.current_account_id() THEN
        RAISE EXCEPTION 'coverage repair account authority lost' USING ERRCODE='42501';
    END IF;
    SELECT fence.source_id INTO source FROM app.fence_ingest_job(target_job_id,claiming_worker,current_lease_token,runtime_version) fence;
    IF source IS NULL OR NOT app.adopted_coverage_authority_current(target_account_id,target_job_id,claiming_worker,current_lease_token,NULL) THEN
        RAISE EXCEPTION 'coverage repair authority lost' USING ERRCODE='42501';
    END IF;
    SELECT * INTO workout FROM app.workouts current_workout WHERE current_workout.account_id=target_account_id
        AND current_workout.id=target_workout_id AND current_workout.source_id=source
        AND current_workout.deletion_requested_at IS NULL FOR UPDATE NOWAIT;
    IF NOT FOUND OR NOT EXISTS (SELECT 1 FROM app.workout_route_points point WHERE point.account_id=target_account_id
        AND point.workout_id=target_workout_id) OR EXISTS (SELECT 1 FROM app.ingest_coverage_handoffs handoff
            WHERE handoff.account_id=target_account_id AND handoff.workout_id=target_workout_id AND handoff.acknowledged_at IS NULL)
        OR EXISTS (SELECT 1 FROM app.adopted_workout_coverage_refreshes request WHERE request.account_id=target_account_id
            AND request.workout_id=target_workout_id) OR NOT EXISTS (SELECT 1 FROM app.source_files file
                WHERE file.id=workout.source_file_id AND file.account_id=target_account_id
                    AND file.source_id=source AND file.state='succeeded') THEN RETURN false; END IF;
    INSERT INTO app.ingest_coverage_handoffs(event_id,account_id,workout_id,source_id,source_file_id,ingest_job_id,content_sha256,repair)
    VALUES(gen_random_uuid(),target_account_id,target_workout_id,source,workout.source_file_id,target_job_id,workout.content_sha256,true)
    ON CONFLICT(ingest_job_id,workout_id) WHERE repair DO NOTHING;
    RETURN FOUND;
END;
$function$;
-- +goose StatementEnd

-- Prepared-only tails remain discoverable after every source lease has ended.
-- Each row is at most B small metadata records, never coordinates or telemetry.
-- +goose StatementBegin
CREATE FUNCTION app.read_ingest_coverage_prepared(target_account_id uuid,target_job_id uuid,claiming_worker text,
    current_lease_token uuid,reconciliation_revision bigint,page_limit integer)
RETURNS TABLE(builder_id uuid,root_job_id uuid,batch_size integer,rules text,sampling text,policy text,
    minimum double precision,targets jsonb) LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
BEGIN
    IF page_limit IS NULL OR page_limit NOT BETWEEN 1 AND 32 THEN
        RAISE EXCEPTION 'invalid prepared cohort page' USING ERRCODE='22023';
    END IF;
    IF NOT app.adopted_coverage_authority_current(target_account_id,target_job_id,claiming_worker,current_lease_token,reconciliation_revision) THEN
        RAISE EXCEPTION 'prepared cohort authority lost' USING ERRCODE='42501';
    END IF;
    PERFORM app.cleanup_ingest_coverage_origins(target_account_id,target_job_id,claiming_worker,current_lease_token,reconciliation_revision);
    RETURN QUERY SELECT builder.builder_id,builder.root_job_id,builder.batch_size,builder.rules,builder.sampling,builder.policy,
        builder.minimum,items.targets FROM app.ingest_coverage_builders builder
    CROSS JOIN LATERAL (SELECT count(*) count,jsonb_agg(jsonb_build_object(
        'workoutId',prepared.workout_id,'eventId',prepared.event_id,'origin',prepared.origin,
        'contentSha256',encode(prepared.content_sha256,'hex'),'routeDigest',encode(prepared.route_input_sha256,'hex'),
        'ownershipEpoch',prepared.ownership_epoch,'routeRevision',prepared.route_input_revision,
        'catalogFingerprint',encode(prepared.catalog_fingerprint,'hex'),
        'matcherContract',builder.matcher_contract,'readiness','map_data_ready','reason',NULL,
        'generations',prepared.generations) ORDER BY prepared.workout_id) targets
        FROM app.ingest_coverage_prepared prepared WHERE prepared.builder_id=builder.builder_id) items
    WHERE builder.account_id=target_account_id AND items.count>0
      AND ((target_job_id IS NULL AND app.ingest_coverage_root_terminal(target_account_id,builder.root_job_id))
          OR EXISTS (SELECT 1 FROM app.jobs source WHERE source.id=target_job_id
              AND source.account_id=target_account_id AND source.parent_job_id=builder.root_job_id))
      AND (items.count>=builder.batch_size OR (app.ingest_coverage_root_terminal(target_account_id,builder.root_job_id)
          AND NOT EXISTS (SELECT 1 FROM app.ingest_coverage_candidates(target_account_id,NULL) candidate
              JOIN app.jobs source ON source.id=candidate.ingest_job_id AND source.account_id=target_account_id
              WHERE source.parent_job_id=builder.root_job_id AND NOT EXISTS (SELECT 1 FROM app.ingest_coverage_prepared prepared
                  WHERE prepared.account_id=target_account_id AND prepared.workout_id=candidate.workout_id
                    AND prepared.event_id=candidate.event_id AND prepared.origin=candidate.origin))))
    ORDER BY builder.root_job_id,builder.builder_id LIMIT page_limit;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.ack_ingest_coverage_prepared(target_account_id uuid,target_workout_id uuid,target_event_id uuid,
    target_parent_id uuid,target_child_id uuid,target_outcome text) RETURNS void LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
DECLARE handoff_ids uuid[]; selected app.ingest_coverage_prepared%ROWTYPE;
BEGIN
    SELECT * INTO selected FROM app.ingest_coverage_prepared prepared
    WHERE prepared.account_id=target_account_id AND prepared.workout_id=target_workout_id AND prepared.event_id=target_event_id;
    IF NOT FOUND THEN RETURN; END IF;
    SELECT array_agg(handoff.event_id) INTO handoff_ids FROM app.ingest_coverage_handoffs handoff
    WHERE handoff.account_id=target_account_id AND handoff.workout_id=target_workout_id AND handoff.source_id=selected.source_id
      AND handoff.acknowledged_at IS NULL AND ((selected.origin='normal' AND handoff.origin='normal'
          AND handoff.content_sha256=selected.content_sha256 AND handoff.ordinal<=(SELECT current_handoff.ordinal
              FROM app.ingest_coverage_handoffs current_handoff WHERE current_handoff.event_id=selected.event_id))
        OR (selected.origin='adopted' AND handoff.origin='adopted' AND handoff.source_file_id=selected.source_file_id
            AND handoff.ingest_job_id=selected.ingest_job_id));
    IF EXISTS (SELECT 1 FROM app.adopted_workout_coverage_refreshes request WHERE request.account_id=target_account_id
        AND request.workout_id=target_workout_id AND request.event_id=target_event_id) THEN
        PERFORM app.record_adopted_coverage_receipt(target_account_id,target_workout_id,target_event_id);
        DELETE FROM app.adopted_workout_coverage_refreshes request WHERE request.account_id=target_account_id
            AND request.workout_id=target_workout_id AND request.event_id=target_event_id;
    END IF;
    UPDATE app.ingest_coverage_handoffs handoff SET acknowledged_at=clock_timestamp(),
        coverage_parent_job_id=target_parent_id,coverage_child_job_id=target_child_id,outcome=target_outcome
    WHERE handoff.account_id=target_account_id AND handoff.workout_id=target_workout_id AND handoff.event_id=ANY(handoff_ids);
    DELETE FROM app.ingest_coverage_prepared prepared WHERE prepared.account_id=target_account_id
        AND prepared.workout_id=target_workout_id AND prepared.event_id=target_event_id AND prepared.source_id=selected.source_id;
END;
$function$;
-- +goose StatementEnd

-- This is deliberately not an enqueue/coalescing wrapper. Admission reclassifies
-- the entire saved cohort under fresh CAS, then constructs one sealed parent.
-- OSM is a separate database: refreshed vectors are the producer's bounded,
-- outside-APP observation, not a claim of cross-database atomicity.
-- +goose StatementBegin
CREATE FUNCTION app.seal_ingest_coverage_cohort(target_account_id uuid,target_job_id uuid,claiming_worker text,
    current_lease_token uuid,reconciliation_revision bigint,target_builder_id uuid,refreshed jsonb,runtime_version integer)
RETURNS TABLE(parent_job_id uuid,route_count integer,outcome text) LANGUAGE plpgsql SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
DECLARE builder app.ingest_coverage_builders%ROWTYPE; prepared app.ingest_coverage_prepared%ROWTYPE;
DECLARE workout app.workouts%ROWTYPE; state app.workout_coverage_states%ROWTYPE;
DECLARE epoch bigint; vector jsonb; catalog_proof bytea; current_event record; existing record; anchor record;
DECLARE new_parent uuid; new_child uuid; old_parent uuid; changed boolean:=false; targets integer;
BEGIN
    IF runtime_version IS NULL OR runtime_version<28 OR runtime_version<
        (SELECT minimum_runtime_version FROM app.schema_metadata WHERE singleton) THEN
        RAISE EXCEPTION 'coverage pipeline runtime is obsolete' USING ERRCODE='55000';
    END IF;
    IF jsonb_typeof(refreshed) IS DISTINCT FROM 'array' THEN
        RAISE EXCEPTION 'invalid refreshed cohort' USING ERRCODE='22023';
    END IF;
    IF jsonb_array_length(refreshed)>1000 OR
        (SELECT count(DISTINCT value->>'workoutId') FROM jsonb_array_elements(refreshed))<>jsonb_array_length(refreshed) THEN
        RAISE EXCEPTION 'invalid refreshed cohort count' USING ERRCODE='22023';
    END IF;
    SELECT account.workout_ownership_epoch INTO epoch FROM app.accounts account
    WHERE account.id=target_account_id AND account.id=app.current_account_id() AND account.state='active' FOR UPDATE NOWAIT;
    IF NOT FOUND THEN RAISE EXCEPTION 'cohort account authority lost' USING ERRCODE='42501'; END IF;
    IF target_job_id IS NOT NULL THEN
        PERFORM app.fence_ingest_job(target_job_id,claiming_worker,current_lease_token,runtime_version);
    ELSE
        PERFORM 1 FROM app.coverage_reconciliation_accounts reconciliation
        WHERE reconciliation.account_id=target_account_id FOR UPDATE NOWAIT;
    END IF;
    IF NOT app.adopted_coverage_authority_current(target_account_id,target_job_id,claiming_worker,current_lease_token,reconciliation_revision) THEN
        RAISE EXCEPTION 'cohort authority lost' USING ERRCODE='42501';
    END IF;
    PERFORM app.cleanup_ingest_coverage_origins(target_account_id,target_job_id,claiming_worker,current_lease_token,reconciliation_revision);
    SELECT * INTO builder FROM app.ingest_coverage_builders current_builder
    WHERE current_builder.builder_id=target_builder_id AND current_builder.account_id=target_account_id FOR UPDATE NOWAIT;
    IF NOT FOUND THEN parent_job_id:=NULL; route_count:=0; outcome:='empty'; RETURN NEXT; RETURN; END IF;
    IF target_job_id IS NOT NULL AND NOT EXISTS (SELECT 1 FROM app.jobs source WHERE source.id=target_job_id
        AND source.account_id=target_account_id AND source.parent_job_id=builder.root_job_id) THEN
        RAISE EXCEPTION 'cohort ingest root authority lost' USING ERRCODE='42501';
    END IF;
    IF target_job_id IS NULL AND NOT app.ingest_coverage_root_terminal(target_account_id,builder.root_job_id) THEN
        parent_job_id:=NULL; route_count:=0; outcome:='held'; RETURN NEXT; RETURN;
    END IF;
    SELECT count(*)::integer INTO targets FROM app.ingest_coverage_prepared item WHERE item.builder_id=target_builder_id;
    IF targets<>jsonb_array_length(refreshed) OR EXISTS (SELECT 1 FROM jsonb_array_elements(refreshed) item
        WHERE NOT EXISTS (SELECT 1 FROM app.ingest_coverage_prepared prepared_item WHERE prepared_item.builder_id=target_builder_id
            AND prepared_item.workout_id=(item->>'workoutId')::uuid AND prepared_item.event_id=(item->>'eventId')::uuid)) THEN
        RAISE EXCEPTION 'prepared cohort snapshot changed' USING ERRCODE='40001';
    END IF;
    -- Parent/child before workout/state, matching claimers; never wait holding a
    -- state row for a parent lock. Include terminal canonical targets as well.
    PERFORM 1 FROM app.jobs parent WHERE parent.account_id=target_account_id AND parent.kind='coverage_update'
        ORDER BY parent.id FOR UPDATE NOWAIT;
    PERFORM 1 FROM app.jobs child JOIN app.jobs parent ON parent.id=child.parent_job_id AND parent.account_id=child.account_id
        WHERE parent.account_id=target_account_id AND parent.kind='coverage_update'
        ORDER BY parent.id,child.id FOR UPDATE OF child NOWAIT;
    FOR prepared IN SELECT item.* FROM app.ingest_coverage_prepared item WHERE item.builder_id=target_builder_id
        ORDER BY item.workout_id FOR UPDATE NOWAIT LOOP
        SELECT * INTO workout FROM app.workouts current_workout WHERE current_workout.id=prepared.workout_id
            AND current_workout.account_id=target_account_id FOR UPDATE NOWAIT;
        IF NOT FOUND OR workout.deletion_requested_at IS NOT NULL OR NOT EXISTS (SELECT 1 FROM app.sources source
            WHERE source.id=workout.source_id AND source.account_id=target_account_id AND source.deleted_at IS NULL) THEN
            DELETE FROM app.ingest_coverage_prepared item WHERE item.account_id=target_account_id
                AND item.workout_id=prepared.workout_id AND item.event_id=prepared.event_id AND item.source_id=prepared.source_id;
            changed:=true; CONTINUE;
        END IF;
        SELECT * INTO current_event FROM app.ingest_coverage_candidates(target_account_id,prepared.ingest_job_id) candidate
        WHERE candidate.workout_id=prepared.workout_id AND candidate.event_id=prepared.event_id AND candidate.origin=prepared.origin
            AND candidate.source_id=prepared.source_id AND candidate.source_file_id=prepared.source_file_id;
        SELECT item->'generations',CASE WHEN jsonb_typeof(item->'catalogFingerprint')='string'
            AND item->>'catalogFingerprint' ~ '^[0-9a-f]{64}$' THEN decode(item->>'catalogFingerprint','hex') END
        INTO vector,catalog_proof FROM jsonb_array_elements(refreshed) item
        WHERE (item->>'workoutId')::uuid=prepared.workout_id AND (item->>'eventId')::uuid=prepared.event_id;
        IF current_event.workout_id IS NULL OR epoch IS DISTINCT FROM prepared.ownership_epoch
            OR workout.content_sha256 IS DISTINCT FROM prepared.content_sha256 OR workout.source_id IS DISTINCT FROM prepared.source_id
            OR app.ingest_coverage_snapshot_digest(target_account_id,prepared.workout_id) IS DISTINCT FROM prepared.route_input_sha256
            OR builder.matcher_contract IS DISTINCT FROM app.current_coverage_matcher_contract()
            -- A new catalog proof cannot legitimize ROI computed against the
            -- old catalog, even if all previously intersecting heads still match.
            OR catalog_proof IS DISTINCT FROM prepared.catalog_fingerprint
            OR NOT COALESCE(app.coverage_generation_vectors_equal(vector,prepared.generations),false) THEN
            DELETE FROM app.ingest_coverage_prepared item WHERE item.account_id=target_account_id AND item.workout_id=prepared.workout_id;
            changed:=true; CONTINUE;
        END IF;
        SELECT * INTO state FROM app.workout_coverage_states current_state WHERE current_state.account_id=target_account_id
            AND current_state.workout_id=prepared.workout_id FOR UPDATE NOWAIT;
        IF NOT FOUND OR state.route_input_revision IS DISTINCT FROM prepared.route_input_revision
            OR state.route_input_sha256 IS DISTINCT FROM prepared.route_input_sha256 OR state.readiness_state<>'map_data_ready' THEN
            DELETE FROM app.ingest_coverage_prepared item WHERE item.account_id=target_account_id AND item.workout_id=prepared.workout_id;
            changed:=true; CONTINUE;
        END IF;
        IF prepared.origin='normal' THEN
            SELECT * INTO existing FROM app.ingest_coverage_existing_target(target_account_id,prepared.workout_id,
                prepared.route_input_revision,prepared.route_input_sha256,prepared.generations,
                builder.rules,builder.sampling,builder.policy,builder.minimum);
            IF FOUND THEN
                PERFORM app.ack_ingest_coverage_prepared(target_account_id,prepared.workout_id,prepared.event_id,
                    existing.parent_job_id,existing.child_job_id,'existing');
                changed:=true;
            END IF;
        END IF;
    END LOOP;
    SELECT count(*)::integer INTO targets FROM app.ingest_coverage_prepared item WHERE item.builder_id=target_builder_id;
    parent_job_id:=NULL; route_count:=0;
    IF targets=0 THEN
        DELETE FROM app.ingest_coverage_builders current_builder WHERE current_builder.builder_id=target_builder_id;
        outcome:=CASE WHEN changed THEN 'invalidated' ELSE 'empty' END; RETURN NEXT; RETURN;
    END IF;
    IF targets>builder.batch_size THEN RAISE EXCEPTION 'eligible cohort exceeds saved capacity' USING ERRCODE='55000'; END IF;
    IF targets<builder.batch_size AND (NOT app.ingest_coverage_root_terminal(target_account_id,builder.root_job_id)
        OR EXISTS (SELECT 1 FROM app.ingest_coverage_candidates(target_account_id,NULL) candidate
            JOIN app.jobs source ON source.id=candidate.ingest_job_id AND source.account_id=target_account_id
            WHERE source.parent_job_id=builder.root_job_id AND NOT EXISTS (SELECT 1 FROM app.ingest_coverage_prepared item
                WHERE item.account_id=target_account_id AND item.workout_id=candidate.workout_id
                    AND item.event_id=candidate.event_id AND item.origin=candidate.origin))) THEN
        outcome:=CASE WHEN changed THEN 'invalidated' ELSE 'held' END; RETURN NEXT; RETURN;
    END IF;
    -- If anything was dropped, return to intake before considering a terminal
    -- tail: an invalidated generation needs a new ROI, not an early partial job.
    IF changed THEN outcome:='invalidated'; RETURN NEXT; RETURN; END IF;
    new_parent:=gen_random_uuid();
    SELECT generation->>'regionId' region_id,(generation->>'generation')::bigint generation INTO anchor
    FROM app.ingest_coverage_prepared item CROSS JOIN LATERAL jsonb_array_elements(item.generations) generation
    WHERE item.builder_id=target_builder_id ORDER BY item.workout_id,generation->>'regionId' LIMIT 1;
    INSERT INTO app.jobs(id,account_id,kind,priority,parameters,progress_total)
    VALUES(new_parent,target_account_id,'coverage_update',20,jsonb_build_object('regionId',anchor.region_id),targets);
    INSERT INTO app.coverage_job_contexts(job_id,account_id,region_id,target_osm_generation,target_work_revision,
        rules_version,sampling_version,path_policy_version,minimum_traversal_meters,parent_batch_size,sealed_ingest_root_id)
    VALUES(new_parent,target_account_id,anchor.region_id,anchor.generation,GREATEST(COALESCE(reconciliation_revision,1),1),
        builder.rules,builder.sampling,builder.policy,builder.minimum,builder.batch_size,builder.root_job_id);
    FOR prepared IN SELECT item.* FROM app.ingest_coverage_prepared item WHERE item.builder_id=target_builder_id ORDER BY item.workout_id LOOP
        UPDATE app.coverage_route_job_contexts context SET result_outcome='superseded' FROM app.jobs child
        WHERE child.id=context.job_id AND child.account_id=context.account_id AND child.account_id=target_account_id
            AND context.workout_id=prepared.workout_id AND child.status='queued';
        PERFORM set_config('app.job_transition','supersede',true);
        UPDATE app.jobs child SET status='succeeded',terminal_at=clock_timestamp() FROM app.coverage_route_job_contexts context
        WHERE child.id=context.job_id AND child.account_id=context.account_id AND child.account_id=target_account_id
            AND context.workout_id=prepared.workout_id AND child.status='queued';
        new_child:=gen_random_uuid();
        INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority,parameters)
        VALUES(new_child,new_parent,target_account_id,'coverage_update_route',20,'{}');
        INSERT INTO app.coverage_route_job_contexts(job_id,account_id,workout_id,route_input_revision,route_input_sha256,target_generations)
        VALUES(new_child,target_account_id,prepared.workout_id,prepared.route_input_revision,prepared.route_input_sha256,prepared.generations);
        UPDATE app.workout_coverage_states current_state SET processing_state='queued',target_job_id=new_child,
            target_revision=prepared.route_input_revision,target_rules_version=builder.rules,target_sampling_version=builder.sampling,
            target_path_policy_version=builder.policy,target_generations=prepared.generations,processing_failure_code=NULL,
            processing_failure_summary=NULL,processing_started_at=NULL,processing_finished_at=NULL,updated_at=clock_timestamp()
        WHERE current_state.account_id=target_account_id AND current_state.workout_id=prepared.workout_id;
        PERFORM app.ack_ingest_coverage_prepared(target_account_id,prepared.workout_id,prepared.event_id,new_parent,new_child,'queued');
    END LOOP;
    INSERT INTO app.coverage_job_progress(job_id,account_id,routes_total) VALUES(new_parent,target_account_id,targets);
    DELETE FROM app.ingest_coverage_builders current_builder WHERE current_builder.builder_id=target_builder_id;
    FOR old_parent IN SELECT parent.id FROM app.jobs parent WHERE parent.account_id=target_account_id
        AND parent.kind='coverage_update' AND parent.status IN ('queued','running') AND parent.id<>new_parent
        AND NOT EXISTS (SELECT 1 FROM app.jobs child WHERE child.parent_job_id=parent.id AND child.status IN ('queued','running'))
    LOOP PERFORM app.derive_parent_status(old_parent); END LOOP;
    IF NOT app.adopted_coverage_authority_current(target_account_id,target_job_id,claiming_worker,current_lease_token,reconciliation_revision) THEN
        RAISE EXCEPTION 'cohort authority expired' USING ERRCODE='40001';
    END IF;
    parent_job_id:=new_parent; route_count:=targets; outcome:='sealed'; RETURN NEXT;
END;
$function$;
-- +goose StatementEnd

GRANT SELECT(parent_batch_size,sealed_ingest_root_id),INSERT(parent_batch_size,sealed_ingest_root_id),UPDATE(parent_batch_size,sealed_ingest_root_id) ON app.coverage_job_contexts TO workouts_security_owner;
GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER TABLE app.coverage_batch_policy OWNER TO workouts_security_owner;
ALTER TABLE app.ingest_coverage_handoffs OWNER TO workouts_security_owner;
ALTER TABLE app.adopted_coverage_handoff_receipts OWNER TO workouts_security_owner;
ALTER TABLE app.ingest_coverage_builders OWNER TO workouts_security_owner;
ALTER TABLE app.ingest_coverage_prepared OWNER TO workouts_security_owner;
ALTER SEQUENCE app.ingest_coverage_handoffs_ordinal_seq OWNER TO workouts_security_owner;
ALTER FUNCTION app.current_coverage_batch_size() OWNER TO workouts_security_owner;
ALTER FUNCTION app.configure_coverage_batch_size(integer,integer) OWNER TO workouts_security_owner;
ALTER FUNCTION app.record_ingest_coverage_handoff() OWNER TO workouts_security_owner;
ALTER FUNCTION app.record_adopted_coverage_origin() OWNER TO workouts_security_owner;
ALTER FUNCTION app.enqueue_coverage_update_core(uuid,uuid,text,bigint,bigint,text,text,text,double precision,jsonb,boolean) OWNER TO workouts_security_owner;
ALTER FUNCTION app.enqueue_adopted_coverage_update(uuid,uuid,text,bigint,bigint,text,text,text,double precision,jsonb) OWNER TO workouts_security_owner;
ALTER FUNCTION app.record_adopted_coverage_receipt(uuid,uuid,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.ingest_coverage_candidates(uuid,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.ingest_coverage_snapshot_digest(uuid,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.read_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,uuid,integer,boolean) OWNER TO workouts_security_owner;
ALTER FUNCTION app.read_ingest_coverage_route_snapshot(uuid,uuid,text,uuid,bigint,uuid,uuid,text) OWNER TO workouts_security_owner;
ALTER FUNCTION app.read_coverage_reconciliation_snapshot(uuid,bigint,text,uuid,uuid,bigint,bytea) OWNER TO workouts_security_owner;
ALTER FUNCTION app.apply_ingest_coverage_normal(uuid,uuid,bigint,text,uuid,uuid,bigint,bytea,text,text,jsonb,uuid,uuid,text,text,text,double precision) OWNER TO workouts_security_owner;
ALTER FUNCTION app.apply_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,jsonb,text,text,text,double precision,integer) OWNER TO workouts_security_owner;
ALTER FUNCTION app.ingest_coverage_root_terminal(uuid,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.ingest_coverage_cleanup_due(uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.cleanup_ingest_coverage_origins(uuid,uuid,text,uuid,bigint) OWNER TO workouts_security_owner;
ALTER FUNCTION app.ingest_coverage_existing_target(uuid,uuid,bigint,bytea,jsonb,text,text,text,double precision) OWNER TO workouts_security_owner;
ALTER FUNCTION app.read_ingest_coverage_prepared(uuid,uuid,text,uuid,bigint,integer) OWNER TO workouts_security_owner;
ALTER FUNCTION app.ack_ingest_coverage_prepared(uuid,uuid,uuid,uuid,uuid,text) OWNER TO workouts_security_owner;
ALTER FUNCTION app.seal_ingest_coverage_cohort(uuid,uuid,text,uuid,bigint,uuid,jsonb,integer) OWNER TO workouts_security_owner;
ALTER FUNCTION app.read_coverage_job_scope(uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.record_ingest_coverage_repair(uuid,uuid,text,uuid,uuid,integer) OWNER TO workouts_security_owner;
ALTER FUNCTION app.read_ingest_coverage_source_id(uuid,uuid,text,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.read_pending_ingest_coverage_workouts(uuid,uuid,text,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.defer_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,jsonb,integer) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
REVOKE ALL ON app.coverage_batch_policy,app.ingest_coverage_handoffs,app.adopted_coverage_handoff_receipts
FROM PUBLIC,workouts_api,workouts_worker,workouts_coverage_worker,workouts_tiles;
REVOKE ALL ON app.ingest_coverage_builders,app.ingest_coverage_prepared
FROM PUBLIC,workouts_api,workouts_worker,workouts_coverage_worker,workouts_tiles;
REVOKE ALL ON FUNCTION app.ingest_coverage_root_terminal(uuid,uuid),
    app.ingest_coverage_cleanup_due(uuid),app.cleanup_ingest_coverage_origins(uuid,uuid,text,uuid,bigint),
    app.ingest_coverage_existing_target(uuid,uuid,bigint,bytea,jsonb,text,text,text,double precision),
    app.read_ingest_coverage_prepared(uuid,uuid,text,uuid,bigint,integer),
    app.ack_ingest_coverage_prepared(uuid,uuid,uuid,uuid,uuid,text),
    app.seal_ingest_coverage_cohort(uuid,uuid,text,uuid,bigint,uuid,jsonb,integer),app.read_coverage_job_scope(uuid),
    app.record_ingest_coverage_repair(uuid,uuid,text,uuid,uuid,integer)
    ,app.read_ingest_coverage_source_id(uuid,uuid,text,uuid)
FROM PUBLIC,workouts_api,workouts_worker,workouts_coverage_worker,workouts_tiles;
GRANT EXECUTE ON FUNCTION app.read_ingest_coverage_prepared(uuid,uuid,text,uuid,bigint,integer),
    app.seal_ingest_coverage_cohort(uuid,uuid,text,uuid,bigint,uuid,jsonb,integer)
TO workouts_worker,workouts_coverage_worker;
GRANT EXECUTE ON FUNCTION app.read_coverage_job_scope(uuid) TO workouts_api;
GRANT EXECUTE ON FUNCTION app.record_ingest_coverage_repair(uuid,uuid,text,uuid,uuid,integer) TO workouts_worker;
GRANT EXECUTE ON FUNCTION app.read_ingest_coverage_source_id(uuid,uuid,text,uuid) TO workouts_worker;
REVOKE ALL ON SEQUENCE app.ingest_coverage_handoffs_ordinal_seq
FROM PUBLIC,workouts_api,workouts_worker,workouts_coverage_worker,workouts_tiles;
REVOKE ALL ON FUNCTION app.current_coverage_batch_size(),app.configure_coverage_batch_size(integer,integer),
    app.record_ingest_coverage_handoff(),
    app.record_adopted_coverage_origin(),app.read_pending_ingest_coverage_workouts(uuid,uuid,text,uuid),
    app.defer_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,jsonb,integer),
    app.enqueue_coverage_update_core(uuid,uuid,text,bigint,bigint,text,text,text,double precision,jsonb,boolean),
    app.enqueue_adopted_coverage_update(uuid,uuid,text,bigint,bigint,text,text,text,double precision,jsonb),
    app.record_adopted_coverage_receipt(uuid,uuid,uuid),app.ingest_coverage_candidates(uuid,uuid),
    app.ingest_coverage_snapshot_digest(uuid,uuid),
    app.read_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,uuid,integer,boolean),
    app.read_ingest_coverage_route_snapshot(uuid,uuid,text,uuid,bigint,uuid,uuid,text),
    app.read_coverage_reconciliation_snapshot(uuid,bigint,text,uuid,uuid,bigint,bytea),
    app.apply_ingest_coverage_normal(uuid,uuid,bigint,text,uuid,uuid,bigint,bytea,text,text,jsonb,uuid,uuid,text,text,text,double precision),
    app.apply_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,jsonb,text,text,text,double precision,integer)
FROM PUBLIC,workouts_api,workouts_worker,workouts_coverage_worker,workouts_tiles;
GRANT EXECUTE ON FUNCTION app.configure_coverage_batch_size(integer,integer) TO workouts_worker;
GRANT EXECUTE ON FUNCTION app.read_pending_ingest_coverage_workouts(uuid,uuid,text,uuid) TO workouts_worker;
GRANT EXECUTE ON FUNCTION app.read_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,uuid,integer,boolean),
    app.read_ingest_coverage_route_snapshot(uuid,uuid,text,uuid,bigint,uuid,uuid,text),
    app.apply_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,jsonb,text,text,text,double precision,integer),
    app.defer_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,jsonb,integer)
TO workouts_worker,workouts_coverage_worker;
GRANT EXECUTE ON FUNCTION app.read_coverage_reconciliation_snapshot(uuid,bigint,text,uuid,uuid,bigint,bytea) TO workouts_coverage_worker;

-- One transient/timeout failure triggers ALL still-authoritative failed members,
-- including permanent and unknown failures. No resource or retry-budget change.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.coverage_auto_retry_parent_eligible(prior_job_id uuid, target_account_id uuid)
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
    ) AND EXISTS (SELECT 1 FROM children WHERE status = 'failed'
          AND failure_code IN ('coverage-route-timeout','coverage-transient-failed'))
      AND NOT EXISTS (SELECT 1 FROM children WHERE account_id IS DISTINCT FROM target_account_id OR kind <> 'coverage_update_route'
          OR status NOT IN ('failed','succeeded') OR cancel_requested_at IS NOT NULL OR context_job_id IS NULL)
      AND NOT EXISTS (SELECT 1 FROM children WHERE status = 'failed' AND NOT EXISTS (SELECT 1 FROM targets WHERE child_id = children.id))
$function$;
-- +goose StatementEnd

-- The legacy singleton adoption apply cannot bypass eligible preparation.
REVOKE EXECUTE ON FUNCTION app.apply_adopted_workout_coverage_refresh(uuid,uuid,text,uuid,bigint,uuid,uuid,bytea,text,text,jsonb,uuid,uuid,text,text,text,double precision)
FROM workouts_worker,workouts_coverage_worker;

-- Keep source/job/ownership fencing intact without holding the map generation
-- row throughout a file transaction. Preserve capability and runtime checks.
-- +goose StatementBegin
DO $block$
DECLARE definition text; pattern text; occurrences integer;
BEGIN
    definition := pg_get_functiondef('app.fence_ingest_job(uuid,text,uuid,integer)'::regprocedure);
    pattern := '\mPERFORM\s+1\s+FROM\s+app\s*\.\s*account_data_generations\s+WHERE\s+account_id\s*=\s*target_account\s+FOR\s+UPDATE\s+NOWAIT\s*;';
    SELECT count(*) INTO occurrences FROM regexp_matches(definition,pattern,'gi');
    IF occurrences <> 1 THEN
        RAISE EXCEPTION 'ingest generation lock anchor must occur exactly once (%)',occurrences;
    END IF;
    definition := regexp_replace(definition,pattern,$replacement$PERFORM 1 FROM app.account_data_generations WHERE account_id = target_account;$replacement$,'gi');
    definition := replace(definition,
        '-- All type/header/route writes advance this row. Never wait on a Coverage writer',
        '-- Generation advances are deferred until commit; this existence check takes no row lock.');
    definition := replace(definition,
        '-- while holding source/job locks; its workout FK lock is likewise NOWAIT below.',
        '-- Source/job and workout FK locks remain unchanged.');
    EXECUTE definition;
END;
$block$;
-- +goose StatementEnd

-- Preserve the four shipped event/column/WHEN contracts, including 017's
-- finish-only invalidation. The account seed intentionally stays immediate.
DROP TRIGGER workout_routes_data_generation_after_write ON app.workout_routes;
CREATE CONSTRAINT TRIGGER workout_routes_data_generation_after_write
AFTER INSERT OR UPDATE OR DELETE ON app.workout_routes
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION app.advance_account_data_generation();
DROP TRIGGER workouts_data_generation_after_write ON app.workouts;
CREATE CONSTRAINT TRIGGER workouts_data_generation_after_write
AFTER INSERT OR UPDATE OR DELETE ON app.workouts
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION app.advance_account_data_generation();
DROP TRIGGER workout_types_data_generation_after_update ON app.workout_types;
CREATE CONSTRAINT TRIGGER workout_types_data_generation_after_update
AFTER UPDATE OF type_key, provider_label ON app.workout_types
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION app.advance_account_data_generation();
DROP TRIGGER workout_coverage_states_data_generation_after_current ON app.workout_coverage_states;
CREATE CONSTRAINT TRIGGER workout_coverage_states_data_generation_after_current
AFTER UPDATE OF processing_finished_at ON app.workout_coverage_states
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW WHEN (
    NEW.processing_state = 'current'
    AND NEW.processing_finished_at IS DISTINCT FROM OLD.processing_finished_at
)
EXECUTE FUNCTION app.advance_account_data_generation();

-- Generation belongs to the API's REPEATABLE READ snapshot, not a newer
-- generation-first lock. A concurrent commit may immediately stale a selection.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.lock_account_data_generation() RETURNS bigint
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
DECLARE
    current_generation bigint;
BEGIN
    SELECT generation INTO current_generation
      FROM app.account_data_generations
     WHERE account_id = app.current_account_id();
    IF current_generation IS NULL THEN
        RAISE EXCEPTION 'account data generation is unavailable' USING ERRCODE = '55000';
    END IF;
    RETURN current_generation;
END;
$function$;
-- +goose StatementEnd
COMMENT ON FUNCTION app.lock_account_data_generation() IS
'Nonlocking generation read in the caller''s REPEATABLE READ snapshot; concurrent commits may make a newly created map selection stale.';

UPDATE app.schema_metadata SET schema_version = 28,minimum_runtime_version = 28 WHERE singleton;

-- +goose Down
-- +goose StatementBegin
DO $block$ BEGIN
    RAISE EXCEPTION 'ingest coverage pipeline is forward-only' USING ERRCODE = '55000';
END; $block$;
-- +goose StatementEnd
