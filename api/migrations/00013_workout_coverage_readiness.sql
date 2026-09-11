-- +goose Up
CREATE TABLE app.workout_coverage_states (
    account_id uuid NOT NULL,
    workout_id uuid NOT NULL,
    route_input_revision bigint NOT NULL CHECK (route_input_revision > 0),
    route_input_sha256 bytea NOT NULL CHECK (octet_length(route_input_sha256)=32),
    readiness_state text NOT NULL CHECK (readiness_state IN ('unresolved','pending','unavailable','map_data_ready')),
    processing_state text NOT NULL DEFAULT 'not_started' CHECK (processing_state='not_started'),
    reason text CHECK (reason IN ('region_not_active','no_provider_region')),
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    map_data_ready_at timestamptz,
    PRIMARY KEY (account_id,workout_id),
    FOREIGN KEY (workout_id,account_id) REFERENCES app.workouts(id,account_id) ON DELETE CASCADE,
    CHECK ((readiness_state='pending' AND reason='region_not_active') OR
           (readiness_state='unavailable' AND reason='no_provider_region') OR
           (readiness_state IN ('unresolved','map_data_ready') AND reason IS NULL)),
    CHECK ((readiness_state='map_data_ready')=(map_data_ready_at IS NOT NULL))
);

CREATE TABLE app.workout_coverage_regions (
    account_id uuid NOT NULL,
    workout_id uuid NOT NULL,
    region_id text NOT NULL CHECK (region_id ~ '^[a-z][a-z0-9-]{0,63}:[a-z][a-z0-9-]{0,63}$'),
    desired_osm_generation bigint NOT NULL CHECK (desired_osm_generation > 0),
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    PRIMARY KEY (account_id,workout_id,region_id),
    FOREIGN KEY (account_id,workout_id) REFERENCES app.workout_coverage_states(account_id,workout_id) ON DELETE CASCADE
);

ALTER TABLE app.workout_coverage_states ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.workout_coverage_states FORCE ROW LEVEL SECURITY;
CREATE POLICY workout_coverage_states_account_policy ON app.workout_coverage_states
USING (account_id=app.current_account_id()) WITH CHECK (account_id=app.current_account_id());
CREATE POLICY workout_coverage_states_owner_policy ON app.workout_coverage_states
TO workouts_security_owner USING (true) WITH CHECK (true);
ALTER TABLE app.workout_coverage_regions ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.workout_coverage_regions FORCE ROW LEVEL SECURITY;
CREATE POLICY workout_coverage_regions_account_policy ON app.workout_coverage_regions
USING (account_id=app.current_account_id()) WITH CHECK (account_id=app.current_account_id());
CREATE POLICY workout_coverage_regions_owner_policy ON app.workout_coverage_regions
TO workouts_security_owner USING (true) WITH CHECK (true);

-- Readiness is embedded in immutable map selections, so owner-visible state
-- changes invalidate prior capabilities just like route-summary changes.
CREATE TRIGGER workout_coverage_states_data_generation_after_write
AFTER INSERT OR UPDATE OR DELETE ON app.workout_coverage_states
FOR EACH ROW EXECUTE FUNCTION app.advance_account_data_generation();

-- +goose StatementBegin
CREATE FUNCTION app.assert_workout_coverage_job_lease(
    target_account_id uuid,target_job_id uuid,claiming_worker text,current_lease_token uuid
) RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,app
AS $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM app.jobs job
         WHERE job.id=target_job_id AND job.account_id=target_account_id
           AND job.kind IN ('manual_ingest_source','scheduled_ingest_source')
           AND job.status='running' AND job.worker_id=claiming_worker
           AND job.lease_token=current_lease_token AND job.lease_expires_at>=clock_timestamp()
    ) THEN
        RAISE EXCEPTION 'coverage readiness requires a live ingest lease' USING ERRCODE='42501';
    END IF;
END;
$$;
-- +goose StatementEnd

-- Initializes old routes lazily and invalidates only when canonical matcher input changes.
-- +goose StatementBegin
CREATE FUNCTION app.initialize_workout_coverage(
    target_account_id uuid,target_workout_id uuid,new_route_input_sha256 bytea,
    target_job_id uuid,claiming_worker text,current_lease_token uuid
) RETURNS TABLE(route_input_revision bigint,digest_changed boolean)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,app
AS $$
DECLARE prior_revision bigint; prior_digest bytea;
BEGIN
    PERFORM app.assert_workout_coverage_job_lease(target_account_id,target_job_id,claiming_worker,current_lease_token);
    IF octet_length(new_route_input_sha256)<>32 OR NOT EXISTS (
        SELECT 1 FROM app.workouts workout WHERE workout.account_id=target_account_id
          AND workout.id=target_workout_id AND workout.deletion_requested_at IS NULL
    ) THEN
        RAISE EXCEPTION 'invalid workout coverage initialization' USING ERRCODE='22023';
    END IF;
    SELECT state.route_input_revision,state.route_input_sha256 INTO prior_revision,prior_digest
      FROM app.workout_coverage_states state
     WHERE state.account_id=target_account_id AND state.workout_id=target_workout_id FOR UPDATE;
    IF NOT FOUND THEN
        INSERT INTO app.workout_coverage_states(account_id,workout_id,route_input_revision,route_input_sha256,readiness_state)
        VALUES(target_account_id,target_workout_id,1,new_route_input_sha256,'unresolved');
        route_input_revision:=1; digest_changed:=true; RETURN NEXT; RETURN;
    END IF;
    IF prior_digest=new_route_input_sha256 THEN
        route_input_revision:=prior_revision; digest_changed:=false; RETURN NEXT; RETURN;
    END IF;
    DELETE FROM app.workout_coverage_regions region
     WHERE region.account_id=target_account_id AND region.workout_id=target_workout_id;
    UPDATE app.workout_coverage_states state SET route_input_revision=prior_revision+1,
        route_input_sha256=new_route_input_sha256,readiness_state='unresolved',reason=NULL,
        map_data_ready_at=NULL,updated_at=transaction_timestamp()
     WHERE state.account_id=target_account_id AND state.workout_id=target_workout_id;
    route_input_revision:=prior_revision+1; digest_changed:=true; RETURN NEXT;
END;
$$;
-- +goose StatementEnd

-- The generation vector is public-map provenance only; route geometry remains in the app database.
-- +goose StatementBegin
CREATE FUNCTION app.set_workout_coverage_readiness(
    target_account_id uuid,target_workout_id uuid,target_route_input_revision bigint,
    new_readiness_state text,new_reason text,new_regions jsonb,
    target_job_id uuid,claiming_worker text,current_lease_token uuid
) RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path=pg_catalog,app
AS $$
DECLARE region_value jsonb; changed boolean;
BEGIN
    PERFORM app.assert_workout_coverage_job_lease(target_account_id,target_job_id,claiming_worker,current_lease_token);
    IF new_readiness_state NOT IN ('pending','unavailable','map_data_ready')
       OR (new_readiness_state='pending' AND new_reason IS DISTINCT FROM 'region_not_active')
       OR (new_readiness_state='unavailable' AND new_reason IS DISTINCT FROM 'no_provider_region')
       OR (new_readiness_state='map_data_ready' AND new_reason IS NOT NULL)
       OR new_regions IS NULL OR jsonb_typeof(new_regions) IS DISTINCT FROM 'array'
       OR (new_readiness_state<>'map_data_ready' AND jsonb_array_length(new_regions)<>0)
       OR (new_readiness_state='map_data_ready' AND jsonb_array_length(new_regions)=0) THEN
        RAISE EXCEPTION 'invalid workout coverage readiness' USING ERRCODE='22023';
    END IF;
    PERFORM 1 FROM app.workout_coverage_states state
     WHERE state.account_id=target_account_id AND state.workout_id=target_workout_id
       AND state.route_input_revision=target_route_input_revision FOR UPDATE;
    IF NOT FOUND THEN RETURN false; END IF;
    DELETE FROM app.workout_coverage_regions region
     WHERE region.account_id=target_account_id AND region.workout_id=target_workout_id;
    FOR region_value IN SELECT value FROM jsonb_array_elements(new_regions) LOOP
        IF jsonb_typeof(region_value) IS DISTINCT FROM 'object' OR (SELECT count(*) FROM jsonb_object_keys(region_value))<>2
           OR NOT (region_value ? 'regionId' AND region_value ? 'generation')
           OR region_value->>'regionId' !~ '^[a-z][a-z0-9-]{0,63}:[a-z][a-z0-9-]{0,63}$'
           OR region_value->>'generation' !~ '^[1-9][0-9]*$' THEN
            RAISE EXCEPTION 'invalid workout coverage region' USING ERRCODE='22023';
        END IF;
        INSERT INTO app.workout_coverage_regions(account_id,workout_id,region_id,desired_osm_generation)
        VALUES(target_account_id,target_workout_id,region_value->>'regionId',(region_value->>'generation')::bigint);
    END LOOP;
    UPDATE app.workout_coverage_states state SET readiness_state=new_readiness_state,reason=new_reason,
        map_data_ready_at=CASE WHEN new_readiness_state='map_data_ready' THEN transaction_timestamp() ELSE NULL END,
        updated_at=transaction_timestamp()
     WHERE state.account_id=target_account_id AND state.workout_id=target_workout_id
       AND (state.readiness_state,state.reason) IS DISTINCT FROM (new_readiness_state,new_reason);
    GET DIAGNOSTICS changed=ROW_COUNT;
    RETURN changed;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON app.workout_coverage_states,app.workout_coverage_regions FROM PUBLIC,workouts_api,workouts_worker;
GRANT SELECT ON app.workout_coverage_states,app.workout_coverage_regions TO workouts_api;
GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER TABLE app.workout_coverage_states OWNER TO workouts_security_owner;
ALTER TABLE app.workout_coverage_regions OWNER TO workouts_security_owner;
ALTER FUNCTION app.assert_workout_coverage_job_lease(uuid,uuid,text,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.initialize_workout_coverage(uuid,uuid,bytea,uuid,text,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.set_workout_coverage_readiness(uuid,uuid,bigint,text,text,jsonb,uuid,text,uuid) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
REVOKE ALL ON FUNCTION app.assert_workout_coverage_job_lease(uuid,uuid,text,uuid),
    app.initialize_workout_coverage(uuid,uuid,bytea,uuid,text,uuid),
    app.set_workout_coverage_readiness(uuid,uuid,bigint,text,text,jsonb,uuid,text,uuid) FROM PUBLIC,workouts_api,workouts_worker;
GRANT EXECUTE ON FUNCTION app.initialize_workout_coverage(uuid,uuid,bytea,uuid,text,uuid),
    app.set_workout_coverage_readiness(uuid,uuid,bigint,text,text,jsonb,uuid,text,uuid) TO workouts_worker;

UPDATE app.schema_metadata SET schema_version=13,minimum_runtime_version=12 WHERE singleton;

-- +goose Down
SELECT app.assert_no_active_manual_ingest();
SELECT app.assert_no_active_scheduled_ingest();
UPDATE app.schema_metadata SET schema_version=12,minimum_runtime_version=11 WHERE singleton;
DROP FUNCTION app.set_workout_coverage_readiness(uuid,uuid,bigint,text,text,jsonb,uuid,text,uuid);
DROP FUNCTION app.initialize_workout_coverage(uuid,uuid,bytea,uuid,text,uuid);
DROP FUNCTION app.assert_workout_coverage_job_lease(uuid,uuid,text,uuid);
DROP TRIGGER workout_coverage_states_data_generation_after_write ON app.workout_coverage_states;
DROP POLICY workout_coverage_regions_owner_policy ON app.workout_coverage_regions;
DROP POLICY workout_coverage_regions_account_policy ON app.workout_coverage_regions;
DROP TABLE app.workout_coverage_regions;
DROP POLICY workout_coverage_states_owner_policy ON app.workout_coverage_states;
DROP POLICY workout_coverage_states_account_policy ON app.workout_coverage_states;
DROP TABLE app.workout_coverage_states;
