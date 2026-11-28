-- +goose Up
-- Public OSM catalog, durable region processing, successful-work timing and deletion.
CREATE TABLE app.osm_catalog_refresh_jobs (
    id uuid PRIMARY KEY,
    administrator_id uuid NOT NULL REFERENCES app.administrators(principal_id),
    providers text[] NOT NULL CHECK (cardinality(providers) BETWEEN 1 AND 16),
    state text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued','running','succeeded','partially_succeeded','failed')),
    progress jsonb NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(progress)='object'),
    attempt integer NOT NULL DEFAULT 0 CHECK (attempt BETWEEN 0 AND 3),
    lease_token uuid,
    lease_expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    started_at timestamptz,
    finished_at timestamptz,
    failure_code text CHECK (length(failure_code)<=64)
);
CREATE UNIQUE INDEX osm_catalog_refresh_one_active ON app.osm_catalog_refresh_jobs ((true))
    WHERE state IN ('queued','running');

CREATE TABLE app.osm_provider_catalogs (
    provider text PRIMARY KEY,
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload)='object')
);
CREATE TABLE app.osm_region_catalog (
    id text PRIMARY KEY,
    provider text NOT NULL,
    payload jsonb NOT NULL CHECK (jsonb_typeof(payload)='object'),
    boundary geometry(Geometry,4326)
);
CREATE INDEX osm_region_catalog_provider ON app.osm_region_catalog(provider);
CREATE INDEX osm_region_catalog_boundary_gist ON app.osm_region_catalog USING gist(boundary);
-- Workout UUIDs are globally unique. The cascading FK gives this private derived
-- index the workout's lifecycle, including account purge, without a second owner.
CREATE TABLE app.osm_region_workouts (
    region_id text NOT NULL REFERENCES app.osm_region_catalog(id) ON DELETE CASCADE,
    workout_id uuid NOT NULL REFERENCES app.workouts(id) ON DELETE CASCADE,
    PRIMARY KEY(region_id,workout_id)
);
CREATE INDEX osm_region_workouts_workout ON app.osm_region_workouts(workout_id);
ALTER TABLE app.osm_region_workouts ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.osm_region_workouts FORCE ROW LEVEL SECURITY;
CREATE POLICY osm_region_workouts_private ON app.osm_region_workouts TO workouts_security_owner USING(true) WITH CHECK(true);
REVOKE ALL ON app.osm_region_workouts FROM PUBLIC,workouts_api,workouts_worker,workouts_coverage_worker,workouts_tiles;
GRANT SELECT,INSERT,DELETE ON app.osm_region_workouts TO workouts_security_owner;

-- Acquire before source writes so catalog replacement and ordinary READ COMMITTED
-- route writers cannot publish memberships computed from opposite snapshots.
-- +goose StatementBegin
CREATE FUNCTION app.lock_osm_region_workouts() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog AS $function$
BEGIN
    IF TG_TABLE_NAME='osm_region_catalog' THEN
        PERFORM pg_advisory_xact_lock(hashtextextended('osm-region-workouts',0));
    ELSE
        PERFORM pg_advisory_xact_lock_shared(hashtextextended('osm-region-workouts',0));
    END IF;
    RETURN NULL;
END;
$function$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION app.lock_osm_region_workouts() FROM PUBLIC;
CREATE TRIGGER osm_region_workouts_catalog_lock BEFORE INSERT OR UPDATE OF payload,boundary ON app.osm_region_catalog
FOR EACH STATEMENT EXECUTE FUNCTION app.lock_osm_region_workouts();
CREATE TRIGGER osm_region_workouts_route_lock BEFORE INSERT OR UPDATE OF route ON app.workout_routes
FOR EACH STATEMENT EXECUTE FUNCTION app.lock_osm_region_workouts();

-- +goose StatementBegin
CREATE FUNCTION app.osm_region_boundary_projection() RETURNS trigger
LANGUAGE plpgsql SET search_path=pg_catalog,app,public AS $function$
BEGIN
    IF TG_OP='INSERT' OR OLD.payload->'boundary' IS DISTINCT FROM NEW.payload->'boundary' THEN
        NEW.boundary:=CASE WHEN jsonb_typeof(NEW.payload->'boundary')='object'
            THEN ST_SetSRID(ST_GeomFromGeoJSON(NEW.payload->'boundary'),4326) END;
    END IF;
    RETURN NEW;
END;
$function$;
-- +goose StatementEnd
CREATE TRIGGER osm_region_boundary_projection BEFORE INSERT OR UPDATE OF payload ON app.osm_region_catalog
FOR EACH ROW EXECUTE FUNCTION app.osm_region_boundary_projection();

-- +goose StatementBegin
CREATE FUNCTION app.osm_region_workout_membership() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app,public AS $function$
BEGIN
    IF TG_TABLE_NAME='osm_region_catalog' THEN
        IF TG_OP='UPDATE' AND OLD.boundary IS NOT DISTINCT FROM NEW.boundary THEN RETURN NULL; END IF;
        DELETE FROM app.osm_region_workouts WHERE region_id=NEW.id;
        INSERT INTO app.osm_region_workouts(region_id,workout_id)
        SELECT NEW.id,r.workout_id FROM app.workout_routes r WHERE ST_Intersects(r.route,NEW.boundary);
    ELSE
        IF TG_OP='UPDATE' AND OLD.route IS NOT DISTINCT FROM NEW.route THEN RETURN NULL; END IF;
        DELETE FROM app.osm_region_workouts WHERE workout_id=NEW.workout_id;
        INSERT INTO app.osm_region_workouts(region_id,workout_id)
        SELECT c.id,NEW.workout_id FROM app.osm_region_catalog c WHERE ST_Intersects(c.boundary,NEW.route);
    END IF;
    RETURN NULL;
END;
$function$;
-- +goose StatementEnd
GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.osm_region_workout_membership() OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
REVOKE ALL ON FUNCTION app.osm_region_workout_membership(),app.osm_region_boundary_projection() FROM PUBLIC;
CREATE TRIGGER osm_region_workouts_catalog AFTER INSERT OR UPDATE OF payload,boundary ON app.osm_region_catalog
FOR EACH ROW EXECUTE FUNCTION app.osm_region_workout_membership();
CREATE TRIGGER osm_region_workouts_route AFTER INSERT OR UPDATE OF route ON app.workout_routes
FOR EACH ROW EXECUTE FUNCTION app.osm_region_workout_membership();

-- Aggregates are administrator-only; no per-account workout rows cross this ABI.
-- +goose StatementBegin
CREATE FUNCTION app.admin_osm_region_workout_counts(admin_session uuid)
RETURNS TABLE(region_id text,workout_count bigint,coverage_route_count bigint)
LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path=pg_catalog,app,public AS $function$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM app.sessions s JOIN app.authentication_principals p ON p.id=s.principal_id
        WHERE s.id=admin_session AND p.role='administrator' AND p.disabled_at IS NULL
        AND s.revoked_at IS NULL AND s.expires_at>transaction_timestamp()) THEN
        RAISE EXCEPTION 'administrator session required' USING ERRCODE='42501';
    END IF;
    RETURN QUERY WITH regions AS MATERIALIZED (
        SELECT c.id,(c.payload->>'buildId')::bigint AS build_id
        FROM app.osm_region_catalog c
    ) SELECT region.id,totals.workouts,totals.current_routes FROM regions region
    CROSS JOIN LATERAL (
        SELECT count(*) AS workouts,count(*) FILTER (WHERE region.build_id IS NOT NULL
            AND state.applied_route_input_revision=state.route_input_revision
            AND state.applied_route_input_sha256=state.route_input_sha256
            AND state.applied_generations @> jsonb_build_array(jsonb_build_object('regionId',region.id,'generation',region.build_id))
            AND EXISTS (SELECT 1 FROM app.coverage_applied_match_proofs proof
                LEFT JOIN app.coverage_route_changes change ON change.account_id=proof.account_id AND change.workout_id=proof.workout_id
                WHERE proof.account_id=workout.account_id AND proof.workout_id=workout.id
                AND proof.route_input_revision=state.route_input_revision AND proof.route_input_sha256=state.route_input_sha256
                AND proof.generations @> jsonb_build_array(jsonb_build_object('regionId',region.id,'generation',region.build_id))
                AND COALESCE(change.mutation_version,0)<=COALESCE(change.resolved_version,0))) AS current_routes
        FROM app.osm_region_workouts membership JOIN app.workouts workout ON workout.id=membership.workout_id
        JOIN app.accounts account ON account.id=workout.account_id AND account.state='active'
        JOIN app.sources source ON source.id=workout.source_id AND source.account_id=workout.account_id
        LEFT JOIN app.workout_coverage_states state ON state.account_id=workout.account_id AND state.workout_id=workout.id
        WHERE workout.deletion_requested_at IS NULL AND membership.region_id=region.id
    ) totals;
END;
$function$;
-- +goose StatementEnd
GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.admin_osm_region_workout_counts(uuid) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
REVOKE ALL ON FUNCTION app.admin_osm_region_workout_counts(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.admin_osm_region_workout_counts(uuid) TO workouts_api;
GRANT SELECT ON app.osm_region_catalog TO workouts_security_owner;

-- Keep the original three-column counts ABI for older callers. Only confirmed,
-- current negative selection proofs contribute to the missing-map breakdown.
-- +goose StatementBegin
CREATE FUNCTION app.admin_osm_region_workout_progress(admin_session uuid)
RETURNS TABLE(region_id text,workout_count bigint,coverage_route_count bigint,missing_region_route_count bigint)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
    SELECT totals.region_id,totals.workout_count,totals.coverage_route_count,
      CASE WHEN totals.coverage_route_count<totals.workout_count AND catalog.payload->>'buildId' IS NOT NULL THEN (
        SELECT count(*) FROM app.osm_region_workouts member JOIN app.workouts workout ON workout.id=member.workout_id
        JOIN app.accounts account ON account.id=workout.account_id AND account.state='active'
        JOIN app.sources source ON source.id=workout.source_id AND source.account_id=workout.account_id
        JOIN app.workout_coverage_states state ON state.workout_id=workout.id AND state.account_id=workout.account_id
        LEFT JOIN app.coverage_route_changes change ON change.workout_id=workout.id AND change.account_id=workout.account_id
        WHERE member.region_id=totals.region_id AND workout.deletion_requested_at IS NULL
          AND state.readiness_state IN('pending','unavailable')
          AND app.coverage_selection_metadata(workout.account_id,workout.id)->>'readiness' IN('pending','unavailable')
          AND NOT COALESCE(state.applied_route_input_revision=state.route_input_revision
            AND state.applied_route_input_sha256=state.route_input_sha256
            AND state.applied_generations @> jsonb_build_array(jsonb_build_object('regionId',totals.region_id,'generation',(catalog.payload->>'buildId')::bigint))
            AND COALESCE(change.mutation_version,0)<=COALESCE(change.resolved_version,0)
            AND EXISTS(SELECT 1 FROM app.coverage_applied_match_proofs proof WHERE proof.account_id=workout.account_id AND proof.workout_id=workout.id
                AND proof.route_input_revision=state.route_input_revision AND proof.route_input_sha256=state.route_input_sha256
                AND proof.generations @> jsonb_build_array(jsonb_build_object('regionId',totals.region_id,'generation',(catalog.payload->>'buildId')::bigint))),false)
      ) ELSE 0::bigint END
    FROM app.admin_osm_region_workout_counts(admin_session) totals
    JOIN app.osm_region_catalog catalog ON catalog.id=totals.region_id
$function$;
-- +goose StatementEnd
GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.admin_osm_region_workout_progress(uuid) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
REVOKE ALL ON FUNCTION app.admin_osm_region_workout_progress(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.admin_osm_region_workout_progress(uuid) TO workouts_api;

-- Region membership prioritizes discovery, never authorizes a matching result.
-- The ordinary scan remains responsible for routes absent from this derived index.
-- +goose StatementBegin
CREATE FUNCTION app.read_priority_coverage_reconciliation_routes(target_account uuid,revision bigint,worker text,lease uuid,
    after_workout uuid,page_limit integer)
RETURNS TABLE(workout_id uuid,route_input_revision bigint,route_input_sha256 bytea)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
    SELECT workout.id,COALESCE(state.route_input_revision,0),state.route_input_sha256
    FROM app.coverage_reconciliation_accounts reconciliation
    JOIN app.workouts workout ON workout.account_id=reconciliation.account_id
    JOIN app.sources source ON source.id=workout.source_id AND source.account_id=workout.account_id AND source.deleted_at IS NULL
    JOIN app.workout_routes route ON route.workout_id=workout.id AND route.account_id=workout.account_id
    LEFT JOIN app.workout_coverage_states state ON state.workout_id=workout.id AND state.account_id=workout.account_id
    LEFT JOIN app.coverage_route_changes change ON change.workout_id=workout.id AND change.account_id=workout.account_id
    WHERE reconciliation.account_id=target_account AND target_account=app.current_account_id()
      AND reconciliation.scan_required AND page_limit BETWEEN 1 AND 100
      AND app.adopted_coverage_authority_current(target_account,NULL,worker,lease,revision)
      AND workout.deletion_requested_at IS NULL AND (after_workout IS NULL OR workout.id>after_workout)
      AND (change.mutation_version IS NULL OR change.mutation_version<=change.resolved_version OR change.next_attempt_at<=clock_timestamp())
      AND NOT EXISTS(SELECT 1 FROM app.ingest_coverage_handoffs handoff WHERE handoff.account_id=workout.account_id
          AND handoff.workout_id=workout.id AND handoff.acknowledged_at IS NULL)
      AND EXISTS(SELECT 1 FROM app.osm_region_workouts member CROSS JOIN app.coverage_reconciliation_state catalog
          WHERE member.workout_id=workout.id AND catalog.singleton AND catalog.active_heads ? member.region_id
          AND NOT (COALESCE(state.applied_route_input_revision=state.route_input_revision
              AND state.applied_route_input_sha256=state.route_input_sha256
              AND state.applied_generations @> jsonb_build_array(jsonb_build_object('regionId',member.region_id,'generation',(catalog.active_heads->>member.region_id)::bigint)),false)
            AND COALESCE(change.mutation_version,0)<=COALESCE(change.resolved_version,0)
            AND EXISTS(SELECT 1 FROM app.coverage_applied_match_proofs proof WHERE proof.account_id=workout.account_id AND proof.workout_id=workout.id
              AND proof.route_input_revision=state.route_input_revision AND proof.route_input_sha256=state.route_input_sha256
              AND proof.generations @> jsonb_build_array(jsonb_build_object('regionId',member.region_id,'generation',(catalog.active_heads->>member.region_id)::bigint)))))
      AND app.coverage_route_actionable(workout.account_id,workout.id)
    ORDER BY workout.id LIMIT LEAST(page_limit,100)
$function$;
-- +goose StatementEnd
GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.read_priority_coverage_reconciliation_routes(uuid,bigint,text,uuid,uuid,integer) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
REVOKE ALL ON FUNCTION app.read_priority_coverage_reconciliation_routes(uuid,bigint,text,uuid,uuid,integer) FROM PUBLIC,workouts_api,workouts_worker;
GRANT EXECUTE ON FUNCTION app.read_priority_coverage_reconciliation_routes(uuid,bigint,text,uuid,uuid,integer) TO workouts_coverage_worker;

REVOKE ALL ON app.osm_catalog_refresh_jobs,app.osm_provider_catalogs,app.osm_region_catalog FROM PUBLIC;
GRANT SELECT ON app.osm_catalog_refresh_jobs,app.osm_provider_catalogs,app.osm_region_catalog TO workouts_api,workouts_worker;
GRANT INSERT(id,administrator_id,providers) ON app.osm_catalog_refresh_jobs TO workouts_api;
GRANT UPDATE ON app.osm_catalog_refresh_jobs TO workouts_worker;
GRANT INSERT,UPDATE,DELETE ON app.osm_provider_catalogs,app.osm_region_catalog TO workouts_worker;

CREATE TABLE app.osm_processing_settings (
    singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
    monitor_database_storage boolean NOT NULL DEFAULT true,
    revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
    updated_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    updated_by uuid REFERENCES app.authentication_principals(id)
);
INSERT INTO app.osm_processing_settings(singleton) VALUES(true);
REVOKE ALL ON app.osm_processing_settings FROM PUBLIC;
GRANT SELECT ON app.osm_processing_settings TO workouts_api;
GRANT UPDATE(monitor_database_storage,revision,updated_at,updated_by) ON app.osm_processing_settings TO workouts_api;

CREATE TABLE app.osm_region_updates (
    id uuid PRIMARY KEY,
    administrator_id uuid NOT NULL REFERENCES app.administrators(principal_id),
    region_id text NOT NULL,
    operation text NOT NULL CHECK(operation IN('download','reprocess')),
    monitor_database_storage boolean NOT NULL DEFAULT true,
    storage_policy_revision bigint NOT NULL DEFAULT 1 CHECK(storage_policy_revision>0),
    storage_policy_execution_hash text,
    state text NOT NULL DEFAULT 'waiting' CHECK(state IN('waiting','downloading','processing','pausing','canceling','paused','canceled','failed','succeeded')),
    phase text NOT NULL DEFAULT 'Downloading' CHECK(phase IN('Downloading','Processing')),
    intent text NOT NULL DEFAULT 'run' CHECK(intent IN('run','pause','cancel')),
    generation_id bigint,
    resumable boolean NOT NULL DEFAULT false,
    promoted boolean NOT NULL DEFAULT false,
    slot_held boolean NOT NULL DEFAULT false,
    execution_hash text,
    execution_token text,
    image text,
    job_name text,
    job_uid text,
    cleanup_name text,
    cleanup_uid text,
    executor_outcome text CHECK(executor_outcome IN('succeeded','failed','paused','canceled')),
    reported_at timestamptz,
    current_order integer NOT NULL DEFAULT 0 CHECK(current_order BETWEEN 0 AND 58),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    enqueued_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    started_at timestamptz,
    finished_at timestamptz,
    error_summary text CHECK(length(error_summary)<=256)
);
CREATE INDEX osm_region_updates_active_slots ON app.osm_region_updates(id) WHERE slot_held;
CREATE UNIQUE INDEX osm_region_updates_one_region ON app.osm_region_updates(region_id)
    WHERE state IN('waiting','downloading','processing','pausing','canceling','paused');
CREATE TABLE app.osm_region_update_steps (
    update_id uuid NOT NULL REFERENCES app.osm_region_updates(id),
    task_order integer NOT NULL CHECK(task_order BETWEEN 1 AND 58),
    name text NOT NULL,
    repeated boolean NOT NULL,
    state text NOT NULL DEFAULT 'pending' CHECK(state IN('pending','running','completed','failed','canceled','paused')),
    batch_index integer NOT NULL DEFAULT 0 CHECK(batch_index>=0),
    batch_total integer NOT NULL DEFAULT 0 CHECK(batch_total>=0),
    started_at timestamptz,
    finished_at timestamptz,
    PRIMARY KEY(update_id,task_order)
);
CREATE TABLE app.osm_region_update_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    update_id uuid NOT NULL REFERENCES app.osm_region_updates(id),
    event_type text NOT NULL,
    happened_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    task_order integer,
    outcome text
);
REVOKE ALL ON app.osm_region_updates,app.osm_region_update_steps,app.osm_region_update_events FROM PUBLIC;
GRANT SELECT ON app.osm_region_update_steps,app.osm_region_update_events TO workouts_api,workouts_osm_controller;
GRANT SELECT ON app.osm_region_updates TO workouts_osm_controller;
GRANT SELECT(id,administrator_id,region_id,operation,monitor_database_storage,storage_policy_revision,state,phase,intent,generation_id,resumable,promoted,slot_held,current_order,created_at,enqueued_at,started_at,finished_at,error_summary) ON app.osm_region_updates TO workouts_api;
GRANT INSERT(id,administrator_id,region_id,operation) ON app.osm_region_updates TO workouts_api;
GRANT UPDATE(state,intent,enqueued_at,finished_at,error_summary) ON app.osm_region_updates TO workouts_api;
GRANT INSERT(update_id,task_order,name,repeated) ON app.osm_region_update_steps TO workouts_api;
GRANT UPDATE(state,batch_index,batch_total,started_at,finished_at) ON app.osm_region_update_steps TO workouts_api;
GRANT INSERT(update_id,event_type,task_order,outcome) ON app.osm_region_update_events TO workouts_api;
GRANT USAGE ON SEQUENCE app.osm_region_update_events_id_seq TO workouts_api,workouts_osm_controller;
GRANT UPDATE ON app.osm_region_updates,app.osm_region_update_steps TO workouts_osm_controller;
GRANT INSERT ON app.osm_region_update_events TO workouts_osm_controller;
GRANT SELECT ON app.osm_region_catalog TO workouts_osm_controller;
GRANT INSERT,UPDATE ON app.osm_region_catalog TO workouts_osm_controller;

-- +goose StatementBegin
CREATE FUNCTION app.capture_osm_storage_policy() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
BEGIN
    IF TG_OP='UPDATE' THEN
        IF NEW.monitor_database_storage IS DISTINCT FROM OLD.monitor_database_storage OR NEW.storage_policy_revision IS DISTINCT FROM OLD.storage_policy_revision OR NEW.storage_policy_execution_hash IS DISTINCT FROM OLD.storage_policy_execution_hash THEN
            IF NOT(OLD.slot_held AND NEW.slot_held AND NEW.execution_hash IS NOT NULL
                AND NEW.execution_hash IS NOT DISTINCT FROM OLD.execution_hash
                AND NEW.storage_policy_execution_hash IS NOT DISTINCT FROM NEW.execution_hash
                AND OLD.storage_policy_execution_hash IS DISTINCT FROM NEW.execution_hash) THEN
                RAISE EXCEPTION 'region processing storage policy is fixed during an execution';
            END IF;
            PERFORM 1 FROM app.osm_processing_settings WHERE singleton AND (monitor_database_storage OR NOT NEW.monitor_database_storage) AND revision=NEW.storage_policy_revision FOR SHARE;
            IF NOT FOUND THEN RAISE EXCEPTION 'storage policy does not match current processing settings'; END IF;
        END IF;
    ELSE
        NEW.storage_policy_execution_hash:=NULL;
        SELECT monitor_database_storage,revision INTO NEW.monitor_database_storage,NEW.storage_policy_revision
            FROM app.osm_processing_settings WHERE singleton FOR SHARE;
        IF NOT FOUND THEN RAISE EXCEPTION 'region processing settings unavailable'; END IF;
    END IF;
    RETURN NEW;
END;
$function$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION app.capture_osm_storage_policy() FROM PUBLIC;
CREATE TRIGGER osm_region_updates_storage_policy BEFORE INSERT OR UPDATE OF monitor_database_storage,storage_policy_revision,storage_policy_execution_hash ON app.osm_region_updates
    FOR EACH ROW EXECUTE FUNCTION app.capture_osm_storage_policy();

-- +goose StatementBegin
CREATE FUNCTION app.osm_active_duration_ms(target uuid,lower_bound timestamptz,upper_bound timestamptz)
RETURNS bigint LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
    WITH boundaries AS (
        SELECT event_type,happened_at,count(*) FILTER(WHERE event_type IN('finish','executor.failed','executor.stopped','osm.region_update_resume'))
            OVER(ORDER BY happened_at,id ROWS BETWEEN UNBOUNDED PRECEDING AND 1 PRECEDING) AS execution
        FROM app.osm_region_update_events WHERE update_id=target
    ), runs AS (
        SELECT execution,min(happened_at) FILTER(WHERE event_type IN('execution','start')) AS started,
            max(happened_at) FILTER(WHERE event_type IN('finish','executor.failed','executor.stopped')) AS finished
        FROM boundaries GROUP BY execution
    ) SELECT COALESCE(floor(sum(GREATEST(extract(epoch FROM LEAST(COALESCE(finished,upper_bound),upper_bound)-GREATEST(started,lower_bound)),0))*1000),0)::bigint
    FROM runs WHERE started IS NOT NULL AND started<upper_bound
        AND (finished IS NOT NULL OR execution=(SELECT max(execution) FROM boundaries))
$function$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION app.osm_active_duration_ms(uuid,timestamptz,timestamptz) FROM PUBLIC;

-- +goose StatementBegin
CREATE FUNCTION app.osm_update_event(target uuid,token text,event jsonb)
RETURNS jsonb LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
DECLARE job app.osm_region_updates%ROWTYPE; kind text:=event->>'type'; task integer:=COALESCE((event->>'task')::integer,0); downloading boolean;
BEGIN
    SELECT * INTO job FROM app.osm_region_updates WHERE id=target AND slot_held
        AND execution_hash=encode(sha256(convert_to(token,'UTF8')),'hex') FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'invalid updater execution' USING ERRCODE='42501'; END IF;
    IF kind NOT IN('read','heartbeat','execution','start','complete','settled','generation','promoted','finish') THEN RAISE EXCEPTION 'invalid updater event'; END IF;
    IF kind NOT IN('read') THEN
        UPDATE app.osm_region_updates SET reported_at=clock_timestamp() WHERE id=target;
    END IF;
    IF kind IN('start','complete','settled') THEN
        IF NOT EXISTS(SELECT 1 FROM app.osm_region_update_steps WHERE update_id=target AND task_order=task) THEN RAISE EXCEPTION 'invalid updater task'; END IF;
        SELECT name IN('prepare-source','download') INTO downloading FROM app.osm_region_update_steps WHERE update_id=target AND task_order=task;
        UPDATE app.osm_region_update_steps SET
            state=CASE WHEN kind='start' OR COALESCE((event->>'awaitingMetrics')::boolean,false) THEN 'running' WHEN COALESCE((event->>'completed')::boolean,false) THEN 'completed' ELSE 'running' END,
            started_at=COALESCE(started_at,clock_timestamp()),
            finished_at=CASE WHEN kind IN('complete','settled') AND COALESCE((event->>'completed')::boolean,false) AND NOT COALESCE((event->>'awaitingMetrics')::boolean,false) THEN clock_timestamp() ELSE NULL END,
            batch_index=GREATEST(0,COALESCE((event->>'batch')::integer,0)),
            batch_total=GREATEST(0,COALESCE((event->>'batches')::integer,0))
        WHERE update_id=target AND task_order=task;
        UPDATE app.osm_region_updates SET current_order=task,phase=CASE WHEN downloading THEN 'Downloading' ELSE 'Processing' END,
            started_at=COALESCE(started_at,clock_timestamp()),
            state=CASE WHEN state IN('waiting','downloading','processing') THEN CASE WHEN downloading THEN 'downloading' ELSE 'processing' END ELSE state END
        WHERE id=target;
    ELSIF kind='generation' THEN
        UPDATE app.osm_region_updates SET generation_id=(event->>'generationId')::bigint WHERE id=target;
    ELSIF kind='promoted' THEN
        UPDATE app.osm_region_updates SET promoted=true WHERE id=target;
    ELSIF kind='finish' THEN
        IF event->>'outcome' NOT IN('succeeded','failed','paused','canceled') THEN RAISE EXCEPTION 'invalid updater outcome'; END IF;
        UPDATE app.osm_region_updates SET executor_outcome=event->>'outcome',
            resumable=COALESCE((event->>'resumable')::boolean,false),promoted=promoted OR COALESCE((event->>'promoted')::boolean,false),
            error_summary=CASE event->>'outcome' WHEN 'canceled' THEN 'This update was canceled.' WHEN 'paused' THEN 'This update is paused.' WHEN 'failed' THEN 'The region update stopped. Review storage or retry the operation.' ELSE NULL END
        WHERE id=target;
        UPDATE app.osm_region_update_steps SET state=CASE event->>'outcome' WHEN 'paused' THEN 'paused' WHEN 'canceled' THEN 'canceled' ELSE 'failed' END,
            finished_at=CASE WHEN event->>'outcome'='paused' THEN NULL ELSE clock_timestamp() END
        WHERE update_id=target AND state='running' AND event->>'outcome'<>'succeeded';
    END IF;
    IF kind IN('execution','start','complete','settled','generation','promoted','finish') THEN
        INSERT INTO app.osm_region_update_events(update_id,event_type,task_order,outcome) VALUES(target,kind,NULLIF(task,0),event->>'outcome');
    END IF;
    SELECT * INTO job FROM app.osm_region_updates WHERE id=target;
    RETURN jsonb_build_object('regionId',job.region_id,'intent',job.intent,'generationId',job.generation_id,'resumable',job.resumable,'promoted',job.promoted);
END;
$function$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION app.osm_update_event(uuid,text,jsonb) FROM PUBLIC;
GRANT USAGE ON SCHEMA app TO workouts_osm_controller,workouts_osm_executor;
GRANT EXECUTE ON FUNCTION app.osm_update_event(uuid,text,jsonb) TO workouts_osm_executor;

-- Historical execution-only totals are unknown; do not infer them from wall time.
ALTER TABLE app.osm_region_updates ADD COLUMN elapsed_total_ms bigint CHECK(elapsed_total_ms>=0),
    ADD COLUMN elapsed_sequence bigint NOT NULL DEFAULT 0 CHECK(elapsed_sequence>=0),
    ADD COLUMN elapsed_execution_hash text;
ALTER TABLE app.osm_region_update_steps ADD COLUMN elapsed_total_ms bigint CHECK(elapsed_total_ms>=0);
ALTER TABLE app.osm_region_updates ALTER COLUMN elapsed_total_ms SET DEFAULT 0;
ALTER TABLE app.osm_region_update_steps ALTER COLUMN elapsed_total_ms SET DEFAULT 0;
GRANT SELECT(elapsed_total_ms) ON app.osm_region_updates TO workouts_api;
-- Preserve the public RPC OID for prepared statements in running executors.
-- +goose StatementBegin
DO $copy$
BEGIN
    EXECUTE replace(pg_get_functiondef('app.osm_update_event(uuid,text,jsonb)'::regprocedure),
        'FUNCTION app.osm_update_event(', 'FUNCTION app.osm_update_event_base(');
END;
$copy$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION app.osm_update_event_base(uuid,text,jsonb) FROM PUBLIC,workouts_osm_executor;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.osm_update_event(target uuid,token text,event jsonb)
RETURNS jsonb LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
DECLARE job app.osm_region_updates%ROWTYPE; result jsonb; kind text:=event->>'type';
    amount bigint; sequence bigint; task integer:=COALESCE((event->>'task')::integer,0);
BEGIN
    SELECT * INTO job FROM app.osm_region_updates WHERE id=target AND slot_held
        AND execution_hash=encode(sha256(convert_to(token,'UTF8')),'hex') FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'invalid updater execution' USING ERRCODE='42501'; END IF;
    IF event ? 'elapsedMilliseconds' THEN
        amount:=(event->>'elapsedMilliseconds')::bigint; sequence:=(event->>'elapsedSequence')::bigint;
        IF kind NOT IN('complete','settled','elapsed','promoted') OR amount IS NULL OR amount<0 OR sequence IS NULL OR sequence<=0 OR task NOT BETWEEN 0 AND 58
            OR (task>0 AND NOT EXISTS(SELECT 1 FROM app.osm_region_update_steps WHERE update_id=target AND task_order=task)) THEN
            RAISE EXCEPTION 'invalid updater elapsed increment';
        END IF;
        IF job.elapsed_execution_hash IS NOT DISTINCT FROM job.execution_hash AND sequence<=job.elapsed_sequence THEN
            RETURN app.osm_update_event_base(target,token,'{"type":"read"}');
        END IF;
    ELSIF kind='elapsed' THEN RAISE EXCEPTION 'missing updater elapsed increment'; END IF;
    result:=app.osm_update_event_base(target,token,CASE WHEN kind='elapsed' THEN '{"type":"heartbeat"}'::jsonb ELSE event END);
    IF amount IS NOT NULL THEN
        UPDATE app.osm_region_updates SET elapsed_execution_hash=execution_hash,elapsed_sequence=sequence WHERE id=target;
    END IF;
    IF kind IN('complete','settled','elapsed','promoted','finish') THEN
        UPDATE app.osm_region_updates SET elapsed_total_ms=app.osm_active_duration_ms(target,'-infinity',clock_timestamp()) WHERE id=target;
        IF task>0 THEN
            UPDATE app.osm_region_update_steps SET
                finished_at=CASE WHEN kind='elapsed' AND state='completed' THEN clock_timestamp() ELSE finished_at END,
                elapsed_total_ms=app.osm_active_duration_ms(target,started_at,CASE WHEN kind='elapsed' AND state='completed' THEN clock_timestamp() ELSE COALESCE(finished_at,clock_timestamp()) END)
            WHERE update_id=target AND task_order=task AND started_at IS NOT NULL;
        END IF;
    END IF;
    IF kind='finish' AND event->>'outcome'='succeeded' THEN
        UPDATE app.osm_region_update_steps SET finished_at=clock_timestamp() WHERE update_id=target AND task_order=(SELECT max(task_order) FROM app.osm_region_update_steps WHERE update_id=target) AND state='completed';
        UPDATE app.osm_region_update_steps SET elapsed_total_ms=app.osm_active_duration_ms(target,started_at,finished_at) WHERE update_id=target AND task_order=(SELECT max(task_order) FROM app.osm_region_update_steps WHERE update_id=target) AND state='completed';
    END IF;
    RETURN result;
END;
$function$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION app.osm_update_event(uuid,text,jsonb) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.osm_update_event(uuid,text,jsonb) TO workouts_osm_executor;

ALTER TABLE app.osm_region_updates DROP CONSTRAINT osm_region_updates_operation_check,
    ADD CONSTRAINT osm_region_updates_operation_check CHECK(operation IN('download','reprocess','delete')),
    DROP CONSTRAINT osm_region_updates_state_check,
    ADD CONSTRAINT osm_region_updates_state_check CHECK(state IN('waiting','downloading','processing','pausing','canceling','paused','canceled','failed','succeeded','deleting'));
DROP INDEX app.osm_region_updates_one_region;
CREATE UNIQUE INDEX osm_region_updates_one_region ON app.osm_region_updates(region_id)
    WHERE state IN('waiting','downloading','processing','pausing','canceling','paused','deleting');
-- Keep the executor's capability and elapsed accounting on the same public RPC.
-- +goose StatementBegin
DO $copy$
BEGIN
    EXECUTE replace(pg_get_functiondef('app.osm_update_event(uuid,text,jsonb)'::regprocedure),
        'FUNCTION app.osm_update_event(', 'FUNCTION app.osm_update_event_timed(');
END;
$copy$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION app.osm_update_event_timed(uuid,text,jsonb) FROM PUBLIC,workouts_osm_executor;
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.osm_update_event(target uuid,token text,event jsonb)
RETURNS jsonb LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
DECLARE result jsonb; operation text; job app.osm_region_updates%ROWTYPE; monitor boolean; policy_revision bigint;
BEGIN
    result:=app.osm_update_event_timed(target,token,event);
    SELECT * INTO job FROM app.osm_region_updates WHERE id=target;
    operation:=job.operation;
    IF event->>'type'='read' AND job.intent='run' AND operation<>'delete' AND job.storage_policy_execution_hash IS DISTINCT FROM job.execution_hash THEN
        SELECT monitor_database_storage,revision INTO monitor,policy_revision FROM app.osm_processing_settings WHERE singleton FOR SHARE;
        monitor:=monitor AND COALESCE((event->>'storageMetricsConfigured')::boolean,true);
        UPDATE app.osm_region_updates SET monitor_database_storage=monitor,storage_policy_revision=policy_revision,storage_policy_execution_hash=execution_hash WHERE id=target;
        INSERT INTO app.osm_region_update_events(update_id,event_type,outcome) VALUES(target,'osm.storage_policy_applied',CASE WHEN monitor THEN 'enabled' ELSE 'disabled' END);
    END IF;
    IF operation='delete' THEN
        UPDATE app.osm_region_updates SET phase='Processing' WHERE id=target;
    END IF;
    RETURN result || jsonb_build_object('operation',operation,'taskCount',(SELECT count(*) FROM app.osm_region_update_steps WHERE update_id=target),
        'monitorDatabaseStorage',(SELECT monitor_database_storage FROM app.osm_region_updates WHERE id=target));
END;
$function$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION app.osm_update_event(uuid,text,jsonb) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.osm_update_event(uuid,text,jsonb) TO workouts_osm_executor;
-- +goose StatementBegin
CREATE FUNCTION app.osm_finalize_durations(target uuid) RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
DECLARE job app.osm_region_updates%ROWTYPE; step record; marker record; boundary timestamptz; ended timestamptz; began timestamptz; last_work timestamptz; final_order integer;
BEGIN
    SELECT * INTO job FROM app.osm_region_updates WHERE id=target AND NOT slot_held AND state IN('succeeded','failed','canceled','paused') FOR UPDATE;
    IF NOT FOUND THEN RETURN false; END IF;
    SELECT min(happened_at),max(happened_at) INTO began,last_work FROM app.osm_region_update_events WHERE update_id=target AND event_type IN('execution','start');
    SELECT max(happened_at) INTO ended FROM app.osm_region_update_events WHERE update_id=target AND event_type IN('finish','executor.failed','executor.stopped');
    IF last_work IS NOT NULL AND (ended IS NULL OR ended<last_work) THEN
        ended:=clock_timestamp();
        INSERT INTO app.osm_region_update_events(update_id,event_type,outcome,happened_at) VALUES(target,'executor.stopped',job.state,ended);
    END IF;
    ended:=COALESCE(ended,job.finished_at,clock_timestamp());
    SELECT max(task_order) INTO final_order FROM app.osm_region_update_steps WHERE update_id=target;
    FOR step IN SELECT task_order,state,started_at,finished_at FROM app.osm_region_update_steps WHERE update_id=target AND started_at IS NOT NULL LOOP
        SELECT event_type,happened_at INTO marker FROM app.osm_region_update_events WHERE update_id=target AND task_order=step.task_order AND event_type IN('start','complete','settled') ORDER BY happened_at DESC,id DESC LIMIT 1;
        IF NOT FOUND THEN CONTINUE; END IF;
        boundary:=NULL;
        IF marker.event_type='settled' THEN boundary:=marker.happened_at;
        ELSE SELECT min(happened_at) INTO boundary FROM app.osm_region_update_events WHERE update_id=target AND happened_at>marker.happened_at AND event_type IN('start','finish','executor.failed','executor.stopped'); END IF;
        IF step.task_order=final_order THEN boundary:=ended; END IF;
        boundary:=COALESCE(boundary,step.finished_at,ended);
        UPDATE app.osm_region_update_steps SET elapsed_total_ms=app.osm_active_duration_ms(target,step.started_at,boundary),
            finished_at=CASE WHEN state='paused' THEN NULL ELSE boundary END WHERE update_id=target AND task_order=step.task_order;
    END LOOP;
    UPDATE app.osm_region_updates SET started_at=COALESCE(began,started_at),finished_at=ended,
        elapsed_total_ms=app.osm_active_duration_ms(target,'-infinity',ended) WHERE id=target;
    RETURN true;
END;
$function$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION app.osm_finalize_durations(uuid) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION app.osm_finalize_durations(uuid) TO workouts_osm_controller;
UPDATE app.schema_metadata SET schema_version=33 WHERE singleton;

-- +goose Down
DROP FUNCTION app.osm_finalize_durations(uuid);
-- +goose StatementBegin
DO $restore$
BEGIN
    EXECUTE replace(pg_get_functiondef('app.osm_update_event_timed(uuid,text,jsonb)'::regprocedure),
        'FUNCTION app.osm_update_event_timed(', 'FUNCTION app.osm_update_event(');
END;
$restore$;
-- +goose StatementEnd
DROP FUNCTION app.osm_update_event_timed(uuid,text,jsonb);
GRANT EXECUTE ON FUNCTION app.osm_update_event(uuid,text,jsonb) TO workouts_osm_executor;
DROP INDEX app.osm_region_updates_one_region;
CREATE UNIQUE INDEX osm_region_updates_one_region ON app.osm_region_updates(region_id)
    WHERE state IN('waiting','downloading','processing','pausing','canceling','paused');
ALTER TABLE app.osm_region_updates DROP CONSTRAINT osm_region_updates_operation_check,
    ADD CONSTRAINT osm_region_updates_operation_check CHECK(operation IN('download','reprocess')),
    DROP CONSTRAINT osm_region_updates_state_check,
    ADD CONSTRAINT osm_region_updates_state_check CHECK(state IN('waiting','downloading','processing','pausing','canceling','paused','canceled','failed','succeeded'));

-- +goose StatementBegin
DO $restore$
BEGIN
    EXECUTE replace(pg_get_functiondef('app.osm_update_event_base(uuid,text,jsonb)'::regprocedure),
        'FUNCTION app.osm_update_event_base(', 'FUNCTION app.osm_update_event(');
END;
$restore$;
-- +goose StatementEnd
DROP FUNCTION app.osm_update_event_base(uuid,text,jsonb);
GRANT EXECUTE ON FUNCTION app.osm_update_event(uuid,text,jsonb) TO workouts_osm_executor;
ALTER TABLE app.osm_region_update_steps DROP COLUMN elapsed_total_ms;
ALTER TABLE app.osm_region_updates DROP COLUMN elapsed_total_ms,DROP COLUMN elapsed_sequence,DROP COLUMN elapsed_execution_hash;

DROP FUNCTION app.osm_update_event(uuid,text,jsonb);
DROP FUNCTION app.osm_active_duration_ms(uuid,timestamptz,timestamptz);
DROP TABLE app.osm_region_update_events,app.osm_region_update_steps,app.osm_region_updates;
DROP FUNCTION app.capture_osm_storage_policy();
DROP TABLE app.osm_processing_settings;

DROP TABLE app.osm_region_catalog,app.osm_provider_catalogs,app.osm_catalog_refresh_jobs;
UPDATE app.schema_metadata SET schema_version=32 WHERE singleton;
