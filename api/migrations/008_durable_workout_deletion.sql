-- +goose Up
CREATE TABLE app.workout_deletion_targets (
    id uuid PRIMARY KEY,
    account_id uuid NOT NULL,
    workout_id uuid NOT NULL,
    source_id uuid NOT NULL,
    provider_id text COLLATE "C",
    fallback_fingerprint_version text COLLATE "C",
    fallback_sha256 bytea,
    root_job_id uuid NOT NULL,
    current_job_id uuid NOT NULL,
    requested_by uuid NOT NULL,
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','completed')),
    requested_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    completed_at timestamptz,
    UNIQUE (id, account_id),
    UNIQUE (account_id, workout_id),
    FOREIGN KEY (account_id) REFERENCES app.accounts(id) ON DELETE RESTRICT,
    FOREIGN KEY (source_id, account_id) REFERENCES app.sources(id, account_id) ON DELETE RESTRICT,
    FOREIGN KEY (root_job_id, account_id) REFERENCES app.jobs(id, account_id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY (current_job_id, account_id) REFERENCES app.jobs(id, account_id) ON DELETE RESTRICT DEFERRABLE INITIALLY DEFERRED,
    FOREIGN KEY (requested_by) REFERENCES app.authentication_principals(id) ON DELETE RESTRICT,
    CHECK ((provider_id IS NOT NULL)::integer + (fallback_sha256 IS NOT NULL)::integer = 1),
    CHECK ((fallback_sha256 IS NULL) = (fallback_fingerprint_version IS NULL)),
    CHECK (provider_id IS NULL OR length(provider_id) BETWEEN 1 AND 4096),
    CHECK (fallback_fingerprint_version IS NULL OR length(fallback_fingerprint_version) BETWEEN 1 AND 128),
    CHECK (fallback_sha256 IS NULL OR octet_length(fallback_sha256) = 32),
    CHECK ((state='completed') = (completed_at IS NOT NULL))
);
CREATE UNIQUE INDEX workout_deletion_targets_provider_identity_idx
    ON app.workout_deletion_targets(account_id,source_id,provider_id) WHERE provider_id IS NOT NULL;
CREATE UNIQUE INDEX workout_deletion_targets_fallback_identity_idx
    ON app.workout_deletion_targets(account_id,source_id,fallback_fingerprint_version,fallback_sha256)
    WHERE provider_id IS NULL;
CREATE INDEX workout_deletion_targets_current_job_idx
    ON app.workout_deletion_targets(account_id,current_job_id);

ALTER TABLE app.workouts ADD COLUMN deletion_requested_at timestamptz;
ALTER TABLE app.workouts ADD COLUMN deletion_target_id uuid;
ALTER TABLE app.workouts ADD CONSTRAINT workouts_deletion_target_fk
    FOREIGN KEY (deletion_target_id,account_id)
    REFERENCES app.workout_deletion_targets(id,account_id) ON DELETE RESTRICT;
ALTER TABLE app.workouts ADD CONSTRAINT workouts_deletion_marker_check
    CHECK ((deletion_requested_at IS NULL)=(deletion_target_id IS NULL));

CREATE TABLE app.workout_deletion_capabilities (
    backend_pid integer NOT NULL,
    transaction_id bigint NOT NULL,
    account_id uuid NOT NULL,
    target_id uuid NOT NULL,
    workout_id uuid NOT NULL,
    job_id uuid NOT NULL,
    worker_id text NOT NULL CHECK (length(worker_id) BETWEEN 1 AND 512),
    lease_token uuid NOT NULL,
    PRIMARY KEY (backend_pid,transaction_id,target_id),
    FOREIGN KEY (target_id,account_id) REFERENCES app.workout_deletion_targets(id,account_id) ON DELETE CASCADE,
    FOREIGN KEY (job_id,account_id) REFERENCES app.jobs(id,account_id) ON DELETE CASCADE
);

ALTER TABLE app.workout_deletion_targets ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.workout_deletion_targets FORCE ROW LEVEL SECURITY;
CREATE POLICY workout_deletion_targets_owner_policy ON app.workout_deletion_targets
    TO workouts_security_owner USING (true) WITH CHECK (true);
ALTER TABLE app.workout_deletion_capabilities ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.workout_deletion_capabilities FORCE ROW LEVEL SECURITY;
CREATE POLICY workout_deletion_capabilities_owner_policy ON app.workout_deletion_capabilities
    TO workouts_security_owner USING (true) WITH CHECK (true);

-- The existing account policy remains the tenant boundary. This restrictive
-- API-only policy centrally hides a workout as soon as deletion is requested.
CREATE POLICY workouts_api_not_deleted_policy ON app.workouts AS RESTRICTIVE
    FOR SELECT TO workouts_api USING (deletion_requested_at IS NULL);
CREATE POLICY workouts_security_owner_policy ON app.workouts
    TO workouts_security_owner USING (true) WITH CHECK (true);

-- +goose StatementBegin
CREATE FUNCTION app.enforce_workout_deletion_target_lifecycle()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    IF TG_OP='DELETE' THEN
        RAISE EXCEPTION 'workout deletion tombstones are persistent' USING ERRCODE='23514';
    END IF;
    IF (OLD.id,OLD.account_id,OLD.workout_id,OLD.source_id,OLD.provider_id,
        OLD.fallback_fingerprint_version,OLD.fallback_sha256,OLD.root_job_id,
        OLD.requested_by,OLD.requested_at) IS DISTINCT FROM
       (NEW.id,NEW.account_id,NEW.workout_id,NEW.source_id,NEW.provider_id,
        NEW.fallback_fingerprint_version,NEW.fallback_sha256,NEW.root_job_id,
        NEW.requested_by,NEW.requested_at) THEN
        RAISE EXCEPTION 'workout deletion tombstones are immutable' USING ERRCODE='23514';
    END IF;
    IF OLD.state='pending' AND NEW.state='completed' AND OLD.completed_at IS NULL AND
       NEW.completed_at IS NOT NULL AND OLD.current_job_id=NEW.current_job_id THEN
        RETURN NEW;
    END IF;
    IF OLD.state='pending' AND NEW.state='pending' AND OLD.completed_at IS NULL AND
       NEW.completed_at IS NULL AND OLD.current_job_id<>NEW.current_job_id AND EXISTS (
           SELECT 1 FROM app.jobs retry JOIN app.jobs prior
             ON prior.id=retry.retry_of_job_id AND prior.account_id=retry.account_id
            WHERE retry.id=NEW.current_job_id AND retry.account_id=NEW.account_id
              AND retry.kind='workout_deletion' AND retry.status='queued'
              AND prior.id=OLD.current_job_id AND prior.kind='workout_deletion'
              AND prior.status IN ('failed','cancelled') AND retry.parameters=prior.parameters
       ) THEN
        RETURN NEW;
    END IF;
    RAISE EXCEPTION 'workout deletion tombstones are immutable' USING ERRCODE='23514';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER workout_deletion_targets_immutable
    BEFORE UPDATE OR DELETE ON app.workout_deletion_targets
    FOR EACH ROW EXECUTE FUNCTION app.enforce_workout_deletion_target_lifecycle();

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.reject_workout_import_event_mutation()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, app
AS $$
BEGIN
    IF TG_OP='DELETE' AND EXISTS (
        SELECT 1 FROM app.workout_deletion_capabilities capability
         WHERE capability.backend_pid=pg_backend_pid()
           AND capability.transaction_id=txid_current()
           AND capability.account_id=OLD.account_id
           AND capability.workout_id=OLD.workout_id
    ) THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION 'workout import events are append-only' USING ERRCODE='23514';
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.require_ingest_write_capability()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    target_account_id uuid;
    target_source_id uuid;
    target_job_id uuid;
    target_record_id uuid;
    target_workout_id uuid;
    capability_exists boolean;
BEGIN
    IF TG_OP='DELETE' AND TG_TABLE_NAME IN ('workouts','workout_aggregates','workout_route_points','workout_import_events') THEN
        target_account_id:=OLD.account_id;
        IF TG_TABLE_NAME='workouts' THEN target_workout_id:=OLD.id; ELSE target_workout_id:=OLD.workout_id; END IF;
        IF EXISTS (
            SELECT 1 FROM app.workout_deletion_capabilities capability
             WHERE capability.backend_pid=pg_backend_pid() AND capability.transaction_id=txid_current()
               AND capability.account_id=target_account_id AND capability.workout_id=target_workout_id
        ) THEN RETURN OLD; END IF;
    END IF;
    IF TG_TABLE_NAME='workouts' THEN
        -- Only the enqueue function can create the referenced target; API has no marker update grant.
        IF TG_OP='UPDATE' AND
           OLD.deletion_requested_at IS NULL AND NEW.deletion_requested_at IS NOT NULL AND
           OLD.deletion_target_id IS NULL AND NEW.deletion_target_id IS NOT NULL AND
           (OLD.id,OLD.account_id,OLD.source_id,OLD.source_file_id,OLD.workout_type_id,OLD.provider_id,
            OLD.fallback_fingerprint_version,OLD.fallback_sha256,OLD.content_sha256,OLD.provider_label,
            OLD.started_at,OLD.ended_at,OLD.start_offset_minutes,OLD.end_offset_minutes,OLD.local_start_date,
            OLD.timezone_name,OLD.timezone_source,OLD.provider_duration,OLD.is_indoor,OLD.location,OLD.created_at) IS NOT DISTINCT FROM
           (NEW.id,NEW.account_id,NEW.source_id,NEW.source_file_id,NEW.workout_type_id,NEW.provider_id,
            NEW.fallback_fingerprint_version,NEW.fallback_sha256,NEW.content_sha256,NEW.provider_label,
            NEW.started_at,NEW.ended_at,NEW.start_offset_minutes,NEW.end_offset_minutes,NEW.local_start_date,
            NEW.timezone_name,NEW.timezone_source,NEW.provider_duration,NEW.is_indoor,NEW.location,NEW.created_at) AND
           EXISTS (SELECT 1 FROM app.workout_deletion_targets target
                    WHERE target.id=NEW.deletion_target_id AND target.account_id=NEW.account_id
                      AND target.workout_id=NEW.id AND target.state='pending') THEN
            RETURN NEW;
        END IF;
        IF TG_OP='INSERT' AND
           (NEW.deletion_requested_at IS NOT NULL OR NEW.deletion_target_id IS NOT NULL) THEN
            RAISE EXCEPTION 'workout deletion markers require the enqueue function' USING ERRCODE='42501';
        END IF;
        IF TG_OP='UPDATE' AND
           (OLD.deletion_requested_at,OLD.deletion_target_id) IS DISTINCT FROM
           (NEW.deletion_requested_at,NEW.deletion_target_id) THEN
            RAISE EXCEPTION 'workout deletion markers require the enqueue function' USING ERRCODE='42501';
        END IF;
    END IF;

    IF TG_OP='DELETE' THEN
        IF TG_TABLE_NAME='source_files' THEN
            target_account_id:=OLD.account_id; target_source_id:=OLD.source_id; target_job_id:=OLD.job_id;
        ELSIF TG_TABLE_NAME='workout_types' THEN
            target_account_id:=OLD.account_id;
        ELSIF TG_TABLE_NAME='workouts' THEN
            target_account_id:=OLD.account_id; target_source_id:=OLD.source_id; target_record_id:=OLD.source_file_id;
        ELSIF TG_TABLE_NAME IN ('workout_aggregates','workout_route_points') THEN
            target_account_id:=OLD.account_id; target_record_id:=OLD.workout_id;
        ELSIF TG_TABLE_NAME='workout_import_events' THEN
            target_account_id:=OLD.account_id; target_source_id:=OLD.source_id; target_job_id:=OLD.job_id;
        ELSE
            RAISE EXCEPTION 'unsupported ingest capability trigger table %',TG_TABLE_NAME USING ERRCODE='55000';
        END IF;
    ELSE
        IF TG_TABLE_NAME='source_files' THEN
            target_account_id:=NEW.account_id; target_source_id:=NEW.source_id; target_job_id:=NEW.job_id;
        ELSIF TG_TABLE_NAME='workout_types' THEN
            target_account_id:=NEW.account_id;
        ELSIF TG_TABLE_NAME='workouts' THEN
            target_account_id:=NEW.account_id; target_source_id:=NEW.source_id; target_record_id:=NEW.source_file_id;
        ELSIF TG_TABLE_NAME IN ('workout_aggregates','workout_route_points') THEN
            target_account_id:=NEW.account_id; target_record_id:=NEW.workout_id;
        ELSIF TG_TABLE_NAME='workout_import_events' THEN
            target_account_id:=NEW.account_id; target_source_id:=NEW.source_id; target_job_id:=NEW.job_id;
        ELSE
            RAISE EXCEPTION 'unsupported ingest capability trigger table %',TG_TABLE_NAME USING ERRCODE='55000';
        END IF;
    END IF;
    IF TG_TABLE_NAME='workouts' THEN
        SELECT file.job_id INTO target_job_id FROM app.source_files file
         WHERE file.id=target_record_id AND file.account_id=target_account_id AND file.source_id=target_source_id;
    ELSIF TG_TABLE_NAME IN ('workout_aggregates','workout_route_points') THEN
        SELECT workout.source_id,file.job_id INTO target_source_id,target_job_id
          FROM app.workouts workout JOIN app.source_files file ON file.id=workout.source_file_id
           AND file.account_id=workout.account_id AND file.source_id=workout.source_id
         WHERE workout.id=target_record_id AND workout.account_id=target_account_id;
    END IF;
    SELECT EXISTS (SELECT 1 FROM app.ingest_write_capabilities capability
        WHERE capability.backend_pid=pg_backend_pid() AND capability.transaction_id=txid_current()
          AND capability.account_id=target_account_id
          AND (target_source_id IS NULL OR capability.source_id=target_source_id)
          AND (target_job_id IS NULL OR capability.job_id=target_job_id)) INTO capability_exists;
    IF NOT capability_exists THEN
        RAISE EXCEPTION 'ingest domain write requires a live transaction fence' USING ERRCODE='42501';
    END IF;
    IF TG_OP='DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.enqueue_workout_deletion(target_workout_id uuid,requester_id uuid)
RETURNS TABLE(job_id uuid,reused boolean,target_count integer)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    target_account uuid:=app.current_account_id();
    target_source uuid;
    workout_row record;
    new_target_id uuid:=gen_random_uuid();
    new_job_id uuid:=gen_random_uuid();
BEGIN
    IF target_account IS NULL OR requester_id IS NULL THEN
        RAISE EXCEPTION 'active account requester is required' USING ERRCODE='42501';
    END IF;
    PERFORM 1 FROM app.accounts account WHERE account.id=target_account AND account.state='active' FOR UPDATE;
    IF NOT FOUND OR NOT EXISTS (
        SELECT 1 FROM app.users account_user
        JOIN app.authentication_principals principal ON principal.id=account_user.principal_id
        WHERE account_user.account_id=target_account AND account_user.principal_id=requester_id
          AND principal.disabled_at IS NULL
    ) THEN RAISE EXCEPTION 'active account requester is required' USING ERRCODE='42501'; END IF;

    SELECT workout.source_id INTO target_source FROM app.workouts workout
     WHERE workout.id=target_workout_id AND workout.account_id=target_account;
    IF NOT FOUND THEN
        SELECT target.current_job_id INTO job_id FROM app.workout_deletion_targets target
         WHERE target.account_id=target_account AND target.workout_id=target_workout_id;
        IF FOUND THEN reused:=true; target_count:=1; RETURN NEXT; END IF;
        RETURN;
    END IF;
    PERFORM 1 FROM app.sources source WHERE source.id=target_source AND source.account_id=target_account FOR UPDATE;
    IF NOT FOUND THEN RETURN; END IF;
    SELECT workout.id,workout.source_id,workout.provider_id,workout.fallback_fingerprint_version,
           workout.fallback_sha256,workout.deletion_target_id
      INTO workout_row FROM app.workouts workout
     WHERE workout.id=target_workout_id AND workout.account_id=target_account AND workout.source_id=target_source FOR UPDATE;
    IF NOT FOUND THEN RETURN; END IF;
    IF workout_row.deletion_target_id IS NOT NULL THEN
        SELECT target.current_job_id INTO job_id FROM app.workout_deletion_targets target
         WHERE target.id=workout_row.deletion_target_id AND target.account_id=target_account;
        reused:=true; target_count:=1; RETURN NEXT; RETURN;
    END IF;
    PERFORM 1 FROM app.workout_deletion_targets target
     WHERE target.account_id=target_account AND target.source_id=target_source AND
       ((workout_row.provider_id IS NOT NULL AND target.provider_id=workout_row.provider_id) OR
        (workout_row.provider_id IS NULL AND target.provider_id IS NULL AND
         target.fallback_fingerprint_version=workout_row.fallback_fingerprint_version AND
         target.fallback_sha256=workout_row.fallback_sha256));
    IF FOUND THEN RAISE EXCEPTION 'workout identity already has a deletion tombstone' USING ERRCODE='23505'; END IF;

    INSERT INTO app.workout_deletion_targets(id,account_id,workout_id,source_id,provider_id,
        fallback_fingerprint_version,fallback_sha256,root_job_id,current_job_id,requested_by)
    VALUES(new_target_id,target_account,target_workout_id,target_source,workout_row.provider_id,
        workout_row.fallback_fingerprint_version,workout_row.fallback_sha256,new_job_id,new_job_id,requester_id);
    INSERT INTO app.jobs(id,account_id,kind,priority,parameters,progress_total,coalescing_version,coalescing_scope,coalescing_key)
    VALUES(new_job_id,target_account,'workout_deletion',100,
        jsonb_build_object('targetType','individual','workoutId',upper(replace(target_workout_id::text,'-',''))),1,1,
        'workout-deletion-individual/v1',pg_catalog.sha256(pg_catalog.convert_to(target_workout_id::text,'UTF8')));
    UPDATE app.workouts workout SET deletion_requested_at=transaction_timestamp(),deletion_target_id=new_target_id
     WHERE workout.id=target_workout_id AND workout.account_id=target_account;
    job_id:=new_job_id; reused:=false; target_count:=1; RETURN NEXT;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.enqueue_workout_range_deletion(start_date date,end_date date,requester_id uuid)
RETURNS TABLE(job_id uuid,reused boolean,target_count integer)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    target_account uuid:=app.current_account_id();
    new_job_id uuid:=gen_random_uuid();
    job_parameters jsonb;
    range_key bytea;
    workout_row record;
    new_target_id uuid;
    visible_count integer;
BEGIN
    IF target_account IS NULL OR requester_id IS NULL OR start_date IS NULL OR end_date IS NULL OR
       NOT isfinite(start_date) OR NOT isfinite(end_date) OR
       start_date NOT BETWEEN date '0001-01-01' AND date '9999-12-31' OR
       end_date NOT BETWEEN date '0001-01-01' AND date '9999-12-31' OR start_date>end_date THEN
        RAISE EXCEPTION 'valid inclusive workout deletion dates are required' USING ERRCODE='22023';
    END IF;
    PERFORM 1 FROM app.accounts account WHERE account.id=target_account AND account.state='active' FOR UPDATE;
    IF NOT FOUND OR NOT EXISTS (
        SELECT 1 FROM app.users account_user
        JOIN app.authentication_principals principal ON principal.id=account_user.principal_id
        WHERE account_user.account_id=target_account AND account_user.principal_id=requester_id
          AND principal.disabled_at IS NULL
    ) THEN RAISE EXCEPTION 'active account requester is required' USING ERRCODE='42501'; END IF;
    job_parameters:=jsonb_build_object('targetType','range','startDate',to_char(start_date,'YYYY-MM-DD'),
        'endDate',to_char(end_date,'YYYY-MM-DD'));
    range_key:=pg_catalog.sha256(pg_catalog.convert_to(
        to_char(start_date,'YYYY-MM-DD')||'/'||to_char(end_date,'YYYY-MM-DD'),'UTF8'));

    -- Ingest fences the source before writing workouts, so these locks freeze the captured set.
    PERFORM 1 FROM app.sources source WHERE source.account_id=target_account
      ORDER BY source.id FOR UPDATE;
    PERFORM 1 FROM app.workouts workout WHERE workout.account_id=target_account
      AND workout.deletion_requested_at IS NULL AND workout.local_start_date BETWEEN start_date AND end_date
      ORDER BY workout.id FOR UPDATE;
    SELECT count(*)::integer INTO visible_count FROM app.workouts workout
     WHERE workout.account_id=target_account AND workout.deletion_requested_at IS NULL
       AND workout.local_start_date BETWEEN start_date AND end_date;
    IF visible_count=0 THEN
        SELECT job.id,count(*)::integer INTO job_id,target_count
          FROM app.jobs job JOIN app.workout_deletion_targets target
            ON target.current_job_id=job.id AND target.account_id=job.account_id
         WHERE job.account_id=target_account AND job.kind='workout_deletion' AND job.parameters=job_parameters
         GROUP BY job.id,job.created_at ORDER BY job.created_at DESC LIMIT 1;
        IF FOUND THEN reused:=true; RETURN NEXT; END IF;
        RETURN;
    END IF;
    IF EXISTS (SELECT 1 FROM app.jobs job WHERE job.account_id=target_account AND job.kind='workout_deletion'
        AND job.parameters=job_parameters AND job.status IN ('queued','running')) THEN
        RAISE EXCEPTION 'workout range deletion changed while an exact request is active' USING ERRCODE='40001';
    END IF;
    FOR workout_row IN SELECT workout.id,workout.source_id,workout.provider_id,
        workout.fallback_fingerprint_version,workout.fallback_sha256 FROM app.workouts workout
        WHERE workout.account_id=target_account AND workout.deletion_requested_at IS NULL
          AND workout.local_start_date BETWEEN start_date AND end_date ORDER BY workout.id LOOP
        new_target_id:=gen_random_uuid();
        INSERT INTO app.workout_deletion_targets(id,account_id,workout_id,source_id,provider_id,
            fallback_fingerprint_version,fallback_sha256,root_job_id,current_job_id,requested_by)
        VALUES(new_target_id,target_account,workout_row.id,workout_row.source_id,workout_row.provider_id,
            workout_row.fallback_fingerprint_version,workout_row.fallback_sha256,new_job_id,new_job_id,requester_id);
        UPDATE app.workouts workout SET deletion_requested_at=transaction_timestamp(),deletion_target_id=new_target_id
         WHERE workout.id=workout_row.id AND workout.account_id=target_account;
    END LOOP;
    INSERT INTO app.jobs(id,account_id,kind,priority,parameters,progress_total,coalescing_version,coalescing_scope,coalescing_key)
    VALUES(new_job_id,target_account,'workout_deletion',100,job_parameters,visible_count,1,
        'workout-deletion-range/v1',range_key);
    job_id:=new_job_id; reused:=false; target_count:=visible_count; RETURN NEXT;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.retry_workout_deletion(prior_job_id uuid,requester_id uuid,max_ordinal integer)
RETURNS TABLE(job_id uuid,target_count integer)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    target_account uuid:=app.current_account_id();
    prior_job record;
    new_job_id uuid:=gen_random_uuid();
    retry_depth integer;
BEGIN
    IF target_account IS NULL OR requester_id IS NULL OR max_ordinal IS NULL OR max_ordinal NOT BETWEEN 1 AND 100 THEN
        RAISE EXCEPTION 'active account requester and valid retry ordinal are required' USING ERRCODE='42501';
    END IF;
    PERFORM 1 FROM app.accounts account WHERE account.id=target_account AND account.state='active' FOR UPDATE;
    IF NOT FOUND OR NOT EXISTS (SELECT 1 FROM app.users account_user
        JOIN app.authentication_principals principal ON principal.id=account_user.principal_id
        WHERE account_user.account_id=target_account AND account_user.principal_id=requester_id
          AND principal.disabled_at IS NULL) THEN
        RAISE EXCEPTION 'active account requester and valid retry ordinal are required' USING ERRCODE='42501';
    END IF;
    PERFORM 1 FROM app.workout_deletion_targets target
     WHERE target.account_id=target_account AND target.current_job_id=prior_job_id AND target.state='pending'
     ORDER BY target.id FOR UPDATE;
    GET DIAGNOSTICS target_count=ROW_COUNT;
    IF target_count=0 THEN RETURN; END IF;
    SELECT job.* INTO prior_job FROM app.jobs job WHERE job.id=prior_job_id AND job.account_id=target_account
      AND job.kind='workout_deletion' AND job.status IN ('failed','cancelled') FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'workout deletion retry requires a terminal current job' USING ERRCODE='55000'; END IF;
    WITH RECURSIVE lineage AS (
        SELECT job.id,job.retry_of_job_id,0 AS ordinal FROM app.jobs job WHERE job.id=prior_job_id
        UNION ALL
        SELECT prior.id,prior.retry_of_job_id,lineage.ordinal+1 FROM lineage
        JOIN app.jobs prior ON prior.id=lineage.retry_of_job_id AND prior.account_id=target_account
        WHERE lineage.ordinal<100
    ) SELECT max(ordinal) INTO retry_depth FROM lineage;
    IF retry_depth>=max_ordinal THEN
        RAISE EXCEPTION 'workout deletion retry limit reached' USING ERRCODE='54001';
    END IF;
    INSERT INTO app.jobs(id,account_id,kind,priority,parameters,progress_total,coalescing_version,
        coalescing_scope,coalescing_key,retry_of_job_id)
    VALUES(new_job_id,target_account,'workout_deletion',100,prior_job.parameters,target_count,
        prior_job.coalescing_version,prior_job.coalescing_scope,prior_job.coalescing_key,prior_job_id);
    UPDATE app.workout_deletion_targets target SET current_job_id=new_job_id
     WHERE target.account_id=target_account AND target.current_job_id=prior_job_id AND target.state='pending';
    job_id:=new_job_id; RETURN NEXT;
END;
$$;
-- +goose StatementEnd

-- This is intentionally separate from every schema-7 claim signature.
-- +goose StatementBegin
CREATE FUNCTION app.claim_next_workout_deletion(claiming_worker text,new_lease_token uuid,
    lease_duration interval,runtime_version integer)
RETURNS TABLE(job_id uuid,account_id uuid,kind text)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    candidate record;
    candidate_status text;
    attempted uuid[]:=ARRAY[]::uuid[];
    expected_count integer;
    locked_count integer;
BEGIN
    IF runtime_version<8 THEN RAISE EXCEPTION 'worker runtime version 8 or newer is required' USING ERRCODE='55000'; END IF;
    IF claiming_worker='' OR new_lease_token IS NULL OR lease_duration<=interval '0 seconds' THEN
        RAISE EXCEPTION 'invalid deletion claim arguments' USING ERRCODE='22023';
    END IF;
    LOOP
        SELECT job.id AS job_id,job.account_id
          INTO candidate FROM app.jobs job
         WHERE job.kind='workout_deletion' AND EXISTS (
               SELECT 1 FROM app.workout_deletion_targets target
                WHERE target.current_job_id=job.id AND target.account_id=job.account_id)
           AND ((job.status='queued' AND job.cancel_requested_at IS NULL) OR
                (job.status='running' AND job.lease_expires_at<transaction_timestamp()))
           AND NOT (job.id=ANY(attempted))
         ORDER BY job.priority DESC,job.created_at,job.id LIMIT 1;
        IF NOT FOUND THEN RETURN; END IF;
        attempted:=array_append(attempted,candidate.job_id);
        PERFORM set_config('app.account_id',candidate.account_id::text,true);
        SELECT count(DISTINCT target.source_id)::integer INTO expected_count
          FROM app.workout_deletion_targets target WHERE target.account_id=candidate.account_id
           AND target.current_job_id=candidate.job_id;
        SELECT count(*)::integer INTO locked_count FROM (
            SELECT source.id FROM app.sources source WHERE source.account_id=candidate.account_id AND EXISTS (
                SELECT 1 FROM app.workout_deletion_targets target WHERE target.account_id=candidate.account_id
                  AND target.current_job_id=candidate.job_id AND target.source_id=source.id)
             ORDER BY source.id FOR UPDATE OF source SKIP LOCKED) locked_sources;
        IF locked_count<>expected_count THEN CONTINUE; END IF;

        SELECT count(*)::integer INTO expected_count FROM app.workouts workout WHERE workout.account_id=candidate.account_id
          AND EXISTS (SELECT 1 FROM app.workout_deletion_targets target WHERE target.account_id=candidate.account_id
              AND target.current_job_id=candidate.job_id AND target.workout_id=workout.id);
        SELECT count(*)::integer INTO locked_count FROM (
            SELECT workout.id FROM app.workouts workout WHERE workout.account_id=candidate.account_id AND EXISTS (
                SELECT 1 FROM app.workout_deletion_targets target WHERE target.account_id=candidate.account_id
                  AND target.current_job_id=candidate.job_id AND target.workout_id=workout.id)
             ORDER BY workout.id FOR UPDATE OF workout SKIP LOCKED) locked_workouts;
        IF locked_count<>expected_count THEN CONTINUE; END IF;

        SELECT count(*)::integer INTO expected_count FROM app.workout_deletion_targets target
         WHERE target.account_id=candidate.account_id AND target.current_job_id=candidate.job_id;
        SELECT count(*)::integer INTO locked_count FROM (
            SELECT target.id FROM app.workout_deletion_targets target
             WHERE target.account_id=candidate.account_id AND target.current_job_id=candidate.job_id
             ORDER BY target.id FOR UPDATE OF target SKIP LOCKED) locked_targets;
        IF locked_count<>expected_count OR locked_count=0 THEN CONTINUE; END IF;
        SELECT job.status INTO candidate_status FROM app.jobs job
         WHERE job.id=candidate.job_id AND job.account_id=candidate.account_id AND job.kind='workout_deletion'
           AND ((job.status='queued' AND job.cancel_requested_at IS NULL) OR
                (job.status='running' AND job.lease_expires_at<transaction_timestamp()))
         FOR UPDATE SKIP LOCKED;
        IF NOT FOUND THEN CONTINUE; END IF;
        IF candidate_status='running' AND NOT app.recover_expired_job(candidate.job_id) THEN CONTINUE; END IF;
        IF app.claim_job(candidate.job_id,claiming_worker,new_lease_token,lease_duration) THEN
            job_id:=candidate.job_id; account_id:=candidate.account_id; kind:='workout_deletion'; RETURN NEXT; RETURN;
        END IF;
    END LOOP;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.fence_workout_deletion(target_job_id uuid,claiming_worker text,current_lease_token uuid)
RETURNS TABLE(target_id uuid,workout_id uuid,target_state text)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    target_account uuid:=app.current_account_id();
    expected_count integer;
    capability_count integer;
BEGIN
    IF claiming_worker='' OR current_lease_token IS NULL THEN
        RAISE EXCEPTION 'invalid deletion fence arguments' USING ERRCODE='22023';
    END IF;
    SELECT count(*)::integer INTO expected_count FROM app.workout_deletion_targets target
     WHERE target.current_job_id=target_job_id AND target.account_id=target_account;
    IF expected_count=0 THEN RETURN; END IF;
    PERFORM 1 FROM app.sources source WHERE source.account_id=target_account AND EXISTS (
        SELECT 1 FROM app.workout_deletion_targets target WHERE target.account_id=target_account
          AND target.current_job_id=target_job_id AND target.source_id=source.id)
      ORDER BY source.id FOR UPDATE;
    PERFORM 1 FROM app.workouts workout WHERE workout.account_id=target_account AND EXISTS (
        SELECT 1 FROM app.workout_deletion_targets target WHERE target.account_id=target_account
          AND target.current_job_id=target_job_id AND target.workout_id=workout.id)
      ORDER BY workout.id FOR UPDATE;
    PERFORM 1 FROM app.workout_deletion_targets target WHERE target.account_id=target_account
      AND target.current_job_id=target_job_id ORDER BY target.id FOR UPDATE;
    PERFORM 1 FROM app.jobs job WHERE job.id=target_job_id AND job.account_id=target_account
      AND job.kind='workout_deletion' AND job.status='running' AND job.cancel_requested_at IS NULL
      AND job.worker_id=claiming_worker AND job.lease_token=current_lease_token
      AND job.lease_expires_at>=clock_timestamp() FOR UPDATE;
    IF NOT FOUND THEN RETURN; END IF;
    INSERT INTO app.workout_deletion_capabilities
        (backend_pid,transaction_id,account_id,target_id,workout_id,job_id,worker_id,lease_token)
    SELECT pg_backend_pid(),txid_current(),target.account_id,target.id,target.workout_id,target_job_id,
        claiming_worker,current_lease_token FROM app.workout_deletion_targets target
     WHERE target.account_id=target_account AND target.current_job_id=target_job_id ORDER BY target.id
    ON CONFLICT ON CONSTRAINT workout_deletion_capabilities_pkey DO NOTHING;
    SELECT count(*)::integer INTO capability_count FROM app.workout_deletion_capabilities capability
     WHERE capability.backend_pid=pg_backend_pid() AND capability.transaction_id=txid_current()
       AND capability.account_id=target_account AND capability.job_id=target_job_id
       AND capability.worker_id=claiming_worker AND capability.lease_token=current_lease_token;
    IF capability_count<>expected_count OR EXISTS (
        SELECT 1 FROM app.workout_deletion_capabilities capability
         WHERE capability.backend_pid=pg_backend_pid() AND capability.transaction_id=txid_current()
           AND capability.job_id<>target_job_id) THEN
        RAISE EXCEPTION 'transaction already fenced for another deletion' USING ERRCODE='25001';
    END IF;
    RETURN QUERY SELECT target.id,target.workout_id,target.state FROM app.workout_deletion_targets target
     WHERE target.account_id=target_account AND target.current_job_id=target_job_id ORDER BY target.id;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.clear_workout_deletion_capability()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
BEGIN
    IF NEW.backend_pid<>pg_backend_pid() OR NEW.transaction_id<>txid_current() THEN
        RAISE EXCEPTION 'deletion capability transaction changed before commit' USING ERRCODE='40001';
    END IF;
    PERFORM 1 FROM app.workout_deletion_targets target
     WHERE target.id=NEW.target_id AND target.account_id=NEW.account_id
       AND target.workout_id=NEW.workout_id AND target.current_job_id=NEW.job_id FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'deletion target changed before commit' USING ERRCODE='40001'; END IF;
    PERFORM 1 FROM app.jobs job WHERE job.id=NEW.job_id AND job.account_id=NEW.account_id
      AND job.kind='workout_deletion' AND job.status='running' AND job.worker_id=NEW.worker_id
      AND job.lease_token=NEW.lease_token FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'deletion lease ownership changed before commit' USING ERRCODE='40001'; END IF;
    DELETE FROM app.workout_deletion_capabilities capability
     WHERE capability.backend_pid=NEW.backend_pid AND capability.transaction_id=NEW.transaction_id
       AND capability.target_id=NEW.target_id;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER workout_deletion_capability_cleanup
    AFTER INSERT ON app.workout_deletion_capabilities
    DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION app.clear_workout_deletion_capability();

-- +goose StatementBegin
CREATE FUNCTION app.purge_workout_deletion(target_job_id uuid,claiming_worker text,current_lease_token uuid)
RETURNS TABLE(targets_completed integer,total_completed integer)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    target_account uuid:=app.current_account_id();
    expected_count integer;
    capability_count integer;
BEGIN
    PERFORM 1 FROM app.jobs job WHERE job.id=target_job_id AND job.account_id=target_account
      AND job.kind='workout_deletion' AND job.status='running' AND job.cancel_requested_at IS NULL
      AND job.worker_id=claiming_worker AND job.lease_token=current_lease_token
      AND job.lease_expires_at>=clock_timestamp() FOR UPDATE;
    IF NOT FOUND THEN RETURN; END IF;
    SELECT count(*)::integer INTO expected_count FROM app.workout_deletion_targets target
     WHERE target.account_id=target_account AND target.current_job_id=target_job_id;
    SELECT count(*)::integer INTO capability_count FROM app.workout_deletion_capabilities capability
     JOIN app.workout_deletion_targets target ON target.id=capability.target_id
       AND target.account_id=capability.account_id AND target.workout_id=capability.workout_id
     WHERE capability.backend_pid=pg_backend_pid() AND capability.transaction_id=txid_current()
       AND capability.account_id=target_account AND capability.job_id=target_job_id
       AND capability.worker_id=claiming_worker AND capability.lease_token=current_lease_token
       AND target.current_job_id=target_job_id;
    IF expected_count=0 OR capability_count<>expected_count THEN RETURN; END IF;
    DELETE FROM app.workout_import_events event USING app.workout_deletion_targets target
     WHERE target.account_id=target_account AND target.current_job_id=target_job_id AND target.state='pending'
       AND event.account_id=target.account_id AND event.workout_id=target.workout_id;
    DELETE FROM app.workouts workout USING app.workout_deletion_targets target
     WHERE target.account_id=target_account AND target.current_job_id=target_job_id AND target.state='pending'
       AND workout.account_id=target.account_id AND workout.id=target.workout_id
       AND workout.deletion_target_id=target.id;
    UPDATE app.workout_deletion_targets target SET state='completed',completed_at=transaction_timestamp()
     WHERE target.account_id=target_account AND target.current_job_id=target_job_id AND target.state='pending';
    GET DIAGNOSTICS targets_completed=ROW_COUNT;
    SELECT count(*)::integer INTO total_completed FROM app.workout_deletion_targets target
     WHERE target.account_id=target_account AND target.current_job_id=target_job_id AND target.state='completed';
    UPDATE app.jobs job SET progress_current=total_completed
     WHERE job.id=target_job_id AND job.account_id=target_account;
    RETURN NEXT;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.workout_deletion_suppressed(target_source_id uuid,target_provider_id text,
    target_fallback_fingerprint_version text,target_fallback_sha256 bytea)
RETURNS boolean
LANGUAGE plpgsql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
BEGIN
    IF (target_provider_id IS NOT NULL)::integer+(target_fallback_sha256 IS NOT NULL)::integer<>1 OR
       (target_fallback_sha256 IS NULL)<>(target_fallback_fingerprint_version IS NULL) THEN
        RAISE EXCEPTION 'invalid workout deletion identity' USING ERRCODE='22023';
    END IF;
    RETURN EXISTS (SELECT 1 FROM app.workout_deletion_targets target
        WHERE target.account_id=app.current_account_id() AND target.source_id=target_source_id AND
          ((target_provider_id IS NOT NULL AND target.provider_id=target_provider_id) OR
           (target_provider_id IS NULL AND target.provider_id IS NULL
            AND target.fallback_fingerprint_version=target_fallback_fingerprint_version
            AND target.fallback_sha256=target_fallback_sha256)));
END;
$$;
-- +goose StatementEnd

-- Runtime-8 workers do not check persistent deletion identities before writing.
-- Stop their new ingest claims during rollout so deleted identities cannot be resurrected.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.claim_next_worker_job(claiming_worker text,new_lease_token uuid,
    lease_duration interval,runtime_version integer)
RETURNS TABLE(job_id uuid,account_id uuid,kind text)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
BEGIN
    IF runtime_version<8 THEN
        RAISE EXCEPTION 'worker runtime version 8 or newer is required' USING ERRCODE='55000';
    END IF;
    RETURN QUERY SELECT claimed.job_id,claimed.account_id,claimed.kind
      FROM app.claim_next_worker_job_internal(claiming_worker,new_lease_token,lease_duration,true) claimed;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.notify_failed_workout_deletion()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
BEGIN
    IF NEW.kind='workout_deletion' AND NEW.status='failed' AND OLD.status<>'failed' THEN
        INSERT INTO app.notifications(id,account_id,type,severity,condition_key,subject_type,subject_id,job_id,title,message)
        VALUES(gen_random_uuid(),NEW.account_id,'workout-deletion-failed','error','job:'||NEW.id::text,
            'job',NEW.id,NEW.id,'Workout deletion failed','The workout could not be deleted, but you can retry the task.')
        ON CONFLICT(account_id,type,condition_key) DO NOTHING;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER jobs_workout_deletion_failure_notification
    AFTER UPDATE OF status ON app.jobs FOR EACH ROW EXECUTE FUNCTION app.notify_failed_workout_deletion();

-- +goose StatementBegin
CREATE FUNCTION app.assert_no_workout_deletions()
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
BEGIN
    LOCK TABLE app.jobs,app.workout_deletion_targets IN SHARE ROW EXCLUSIVE MODE;
    IF EXISTS (SELECT 1 FROM app.jobs job WHERE job.kind='workout_deletion') OR
       EXISTS (SELECT 1 FROM app.workout_deletion_targets) THEN
        RAISE EXCEPTION 'cannot downgrade while workout deletion jobs or targets exist' USING ERRCODE='55006';
    END IF;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON app.workout_deletion_targets,app.workout_deletion_capabilities FROM PUBLIC,workouts_api,workouts_worker;
REVOKE ALL ON FUNCTION app.enqueue_workout_deletion(uuid,uuid),app.enqueue_workout_range_deletion(date,date,uuid),
    app.retry_workout_deletion(uuid,uuid,integer),
    app.claim_next_workout_deletion(text,uuid,interval,integer),app.fence_workout_deletion(uuid,text,uuid),
    app.purge_workout_deletion(uuid,text,uuid),app.workout_deletion_suppressed(uuid,text,text,bytea),
    app.clear_workout_deletion_capability(),app.assert_no_workout_deletions() FROM PUBLIC,workouts_api,workouts_worker;
GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER TABLE app.workout_deletion_targets OWNER TO workouts_security_owner;
ALTER TABLE app.workout_deletion_capabilities OWNER TO workouts_security_owner;
ALTER FUNCTION app.enforce_workout_deletion_target_lifecycle() OWNER TO workouts_security_owner;
ALTER FUNCTION app.reject_workout_import_event_mutation() OWNER TO workouts_security_owner;
ALTER FUNCTION app.require_ingest_write_capability() OWNER TO workouts_security_owner;
ALTER FUNCTION app.enqueue_workout_deletion(uuid,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.enqueue_workout_range_deletion(date,date,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.retry_workout_deletion(uuid,uuid,integer) OWNER TO workouts_security_owner;
ALTER FUNCTION app.claim_next_workout_deletion(text,uuid,interval,integer) OWNER TO workouts_security_owner;
ALTER FUNCTION app.fence_workout_deletion(uuid,text,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.clear_workout_deletion_capability() OWNER TO workouts_security_owner;
ALTER FUNCTION app.purge_workout_deletion(uuid,text,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.workout_deletion_suppressed(uuid,text,text,bytea) OWNER TO workouts_security_owner;
ALTER FUNCTION app.notify_failed_workout_deletion() OWNER TO workouts_security_owner;
ALTER FUNCTION app.assert_no_workout_deletions() OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;

GRANT SELECT,INSERT,UPDATE ON app.workout_deletion_targets TO workouts_security_owner;
GRANT SELECT,INSERT,DELETE ON app.workout_deletion_capabilities TO workouts_security_owner;
GRANT SELECT,UPDATE,DELETE ON app.workouts TO workouts_security_owner;
GRANT SELECT,DELETE ON app.workout_import_events TO workouts_security_owner;
GRANT EXECUTE ON FUNCTION app.enqueue_workout_deletion(uuid,uuid),
    app.enqueue_workout_range_deletion(date,date,uuid),app.retry_workout_deletion(uuid,uuid,integer) TO workouts_api;
GRANT EXECUTE ON FUNCTION app.claim_next_workout_deletion(text,uuid,interval,integer),
    app.fence_workout_deletion(uuid,text,uuid),app.purge_workout_deletion(uuid,text,uuid),
    app.workout_deletion_suppressed(uuid,text,text,bytea) TO workouts_worker;
GRANT EXECUTE ON FUNCTION app.assert_no_workout_deletions() TO workouts_migration;

ALTER TABLE app.notifications NO FORCE ROW LEVEL SECURITY;
UPDATE app.notifications
   SET message='The workout could not be deleted, but you can retry the task.'
 WHERE type='workout-deletion-failed';
ALTER TABLE app.notifications FORCE ROW LEVEL SECURITY;

UPDATE app.schema_metadata SET schema_version=8,minimum_runtime_version=8 WHERE singleton;

-- +goose Down
SELECT app.assert_no_workout_deletions();
UPDATE app.schema_metadata SET schema_version=7,minimum_runtime_version=6 WHERE singleton;

REVOKE UPDATE,DELETE ON app.workouts FROM workouts_security_owner;
REVOKE DELETE ON app.workout_import_events FROM workouts_security_owner;
REVOKE EXECUTE ON FUNCTION app.assert_no_workout_deletions() FROM workouts_migration;
REVOKE EXECUTE ON FUNCTION app.enqueue_workout_deletion(uuid,uuid),
    app.enqueue_workout_range_deletion(date,date,uuid),app.retry_workout_deletion(uuid,uuid,integer) FROM workouts_api;
REVOKE EXECUTE ON FUNCTION app.claim_next_workout_deletion(text,uuid,interval,integer),
    app.fence_workout_deletion(uuid,text,uuid),app.purge_workout_deletion(uuid,text,uuid),
    app.workout_deletion_suppressed(uuid,text,text,bytea) FROM workouts_worker;
DROP TRIGGER jobs_workout_deletion_failure_notification ON app.jobs;
DROP FUNCTION app.notify_failed_workout_deletion();
DROP FUNCTION app.assert_no_workout_deletions();
DROP FUNCTION app.purge_workout_deletion(uuid,text,uuid);
DROP TRIGGER workout_deletion_capability_cleanup ON app.workout_deletion_capabilities;
DROP FUNCTION app.clear_workout_deletion_capability();
DROP FUNCTION app.fence_workout_deletion(uuid,text,uuid);
DROP FUNCTION app.claim_next_workout_deletion(text,uuid,interval,integer);
DROP FUNCTION app.workout_deletion_suppressed(uuid,text,text,bytea);
DROP FUNCTION app.retry_workout_deletion(uuid,uuid,integer);
DROP FUNCTION app.enqueue_workout_range_deletion(date,date,uuid);
DROP FUNCTION app.enqueue_workout_deletion(uuid,uuid);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.claim_next_worker_job(claiming_worker text,new_lease_token uuid,
    lease_duration interval,runtime_version integer)
RETURNS TABLE(job_id uuid,account_id uuid,kind text)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
BEGIN
    IF runtime_version<6 THEN
        RAISE EXCEPTION 'worker runtime version 6 or newer is required' USING ERRCODE='55000';
    END IF;
    RETURN QUERY SELECT claimed.job_id,claimed.account_id,claimed.kind
      FROM app.claim_next_worker_job_internal(claiming_worker,new_lease_token,lease_duration,true) claimed;
END;
$$;
-- +goose StatementEnd

-- Restore the schema-7 append-only guard.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.reject_workout_import_event_mutation()
RETURNS trigger LANGUAGE plpgsql SET search_path = pg_catalog AS $$
BEGIN
    RAISE EXCEPTION 'workout import events are append-only' USING ERRCODE='23514';
END;
$$;
-- +goose StatementEnd

-- Restore schema 7's ingest fence by removing only the schema-8 branches.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.require_ingest_write_capability()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    target_account_id uuid; target_source_id uuid; target_job_id uuid; target_record_id uuid; capability_exists boolean;
BEGIN
    IF TG_OP='DELETE' THEN
        IF TG_TABLE_NAME='source_files' THEN target_account_id:=OLD.account_id; target_source_id:=OLD.source_id; target_job_id:=OLD.job_id;
        ELSIF TG_TABLE_NAME='workout_types' THEN target_account_id:=OLD.account_id;
        ELSIF TG_TABLE_NAME='workouts' THEN target_account_id:=OLD.account_id; target_source_id:=OLD.source_id; target_record_id:=OLD.source_file_id;
        ELSIF TG_TABLE_NAME IN ('workout_aggregates','workout_route_points') THEN target_account_id:=OLD.account_id; target_record_id:=OLD.workout_id;
        ELSIF TG_TABLE_NAME='workout_import_events' THEN target_account_id:=OLD.account_id; target_source_id:=OLD.source_id; target_job_id:=OLD.job_id;
        ELSE RAISE EXCEPTION 'unsupported ingest capability trigger table %',TG_TABLE_NAME USING ERRCODE='55000'; END IF;
    ELSE
        IF TG_TABLE_NAME='source_files' THEN target_account_id:=NEW.account_id; target_source_id:=NEW.source_id; target_job_id:=NEW.job_id;
        ELSIF TG_TABLE_NAME='workout_types' THEN target_account_id:=NEW.account_id;
        ELSIF TG_TABLE_NAME='workouts' THEN target_account_id:=NEW.account_id; target_source_id:=NEW.source_id; target_record_id:=NEW.source_file_id;
        ELSIF TG_TABLE_NAME IN ('workout_aggregates','workout_route_points') THEN target_account_id:=NEW.account_id; target_record_id:=NEW.workout_id;
        ELSIF TG_TABLE_NAME='workout_import_events' THEN target_account_id:=NEW.account_id; target_source_id:=NEW.source_id; target_job_id:=NEW.job_id;
        ELSE RAISE EXCEPTION 'unsupported ingest capability trigger table %',TG_TABLE_NAME USING ERRCODE='55000'; END IF;
    END IF;
    IF TG_TABLE_NAME='workouts' THEN
        SELECT file.job_id INTO target_job_id FROM app.source_files file
         WHERE file.id=target_record_id AND file.account_id=target_account_id AND file.source_id=target_source_id;
    ELSIF TG_TABLE_NAME IN ('workout_aggregates','workout_route_points') THEN
        SELECT workout.source_id,file.job_id INTO target_source_id,target_job_id FROM app.workouts workout
        JOIN app.source_files file ON file.id=workout.source_file_id AND file.account_id=workout.account_id AND file.source_id=workout.source_id
        WHERE workout.id=target_record_id AND workout.account_id=target_account_id;
    END IF;
    SELECT EXISTS(SELECT 1 FROM app.ingest_write_capabilities capability
      WHERE capability.backend_pid=pg_backend_pid() AND capability.transaction_id=txid_current()
        AND capability.account_id=target_account_id AND (target_source_id IS NULL OR capability.source_id=target_source_id)
        AND (target_job_id IS NULL OR capability.job_id=target_job_id)) INTO capability_exists;
    IF NOT capability_exists THEN RAISE EXCEPTION 'ingest domain write requires a live transaction fence' USING ERRCODE='42501'; END IF;
    IF TG_OP='DELETE' THEN RETURN OLD; END IF; RETURN NEW;
END;
$$;
-- +goose StatementEnd
ALTER FUNCTION app.reject_workout_import_event_mutation() OWNER TO workouts_security_owner;
ALTER FUNCTION app.require_ingest_write_capability() OWNER TO workouts_security_owner;
DROP POLICY workouts_security_owner_policy ON app.workouts;
DROP POLICY workouts_api_not_deleted_policy ON app.workouts;
ALTER TABLE app.workouts DROP CONSTRAINT workouts_deletion_marker_check;
ALTER TABLE app.workouts DROP CONSTRAINT workouts_deletion_target_fk;
ALTER TABLE app.workouts DROP COLUMN deletion_target_id;
ALTER TABLE app.workouts DROP COLUMN deletion_requested_at;
DROP TRIGGER workout_deletion_targets_immutable ON app.workout_deletion_targets;
DROP FUNCTION app.enforce_workout_deletion_target_lifecycle();
DROP TABLE app.workout_deletion_capabilities;
DROP TABLE app.workout_deletion_targets;
