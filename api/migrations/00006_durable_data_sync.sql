-- +goose Up
CREATE TABLE app.job_source_contexts (
    job_id uuid PRIMARY KEY,
    account_id uuid NOT NULL,
    source_id uuid NOT NULL,
    source_generation bigint NOT NULL CHECK (source_generation > 0),
    display_name text NOT NULL CHECK (length(display_name) BETWEEN 1 AND 200),
    source_type text NOT NULL CHECK (length(source_type) BETWEEN 1 AND 64),
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    FOREIGN KEY (job_id, account_id) REFERENCES app.jobs(id, account_id) ON DELETE CASCADE,
    FOREIGN KEY (source_id, account_id) REFERENCES app.sources(id, account_id) ON DELETE CASCADE
);
CREATE INDEX job_source_contexts_source_idx ON app.job_source_contexts (account_id, source_id, created_at DESC);

-- Backfill schema-5 children so rollout drain uses the same fenced runtime path.
INSERT INTO app.job_source_contexts(job_id,account_id,source_id,source_generation,display_name,source_type)
SELECT job.id,job.account_id,snapshot.source_id,snapshot.source_generation,source.display_name,source.type
  FROM app.jobs job
  JOIN app.job_config_snapshots snapshot ON snapshot.job_id=job.id AND snapshot.account_id=job.account_id
  JOIN app.sources source ON source.id=snapshot.source_id AND source.account_id=snapshot.account_id
 WHERE job.kind IN ('manual_ingest_source','scheduled_ingest_source');

-- Runtime-6 workers consume schema-5 children as an all-files bounded ingest.
ALTER TABLE app.jobs DISABLE TRIGGER jobs_state_before_write;
UPDATE app.jobs SET parameters=parameters||jsonb_build_object(
    'mode','bounded','startDate','0001-01-01','endDate','9999-12-31','legacySchema6',true)
 WHERE kind IN ('manual_ingest_source','scheduled_ingest_source')
   AND jsonb_typeof(parameters)='object' AND NOT parameters ? 'mode'
   AND (SELECT count(*) FROM jsonb_object_keys(parameters))=2
   AND parameters ? 'sourceId' AND parameters ? 'generation';
ALTER TABLE app.jobs ENABLE TRIGGER jobs_state_before_write;

CREATE TABLE app.job_progress (
    job_id uuid PRIMARY KEY,
    account_id uuid NOT NULL,
    files_discovered bigint NOT NULL DEFAULT 0 CHECK (files_discovered >= 0),
    files_skipped bigint NOT NULL DEFAULT 0 CHECK (files_skipped >= 0),
    files_succeeded bigint NOT NULL DEFAULT 0 CHECK (files_succeeded >= 0),
    files_failed bigint NOT NULL DEFAULT 0 CHECK (files_failed >= 0),
    workouts_created bigint NOT NULL DEFAULT 0 CHECK (workouts_created >= 0),
    workouts_updated bigint NOT NULL DEFAULT 0 CHECK (workouts_updated >= 0),
    workouts_unchanged bigint NOT NULL DEFAULT 0 CHECK (workouts_unchanged >= 0),
    workouts_rejected bigint NOT NULL DEFAULT 0 CHECK (workouts_rejected >= 0),
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    FOREIGN KEY (job_id, account_id) REFERENCES app.jobs(id, account_id) ON DELETE CASCADE
);
CREATE INDEX job_progress_account_idx ON app.job_progress (account_id, updated_at DESC);

CREATE TABLE app.source_objects (
    account_id uuid NOT NULL,
    source_id uuid NOT NULL,
    relative_name text NOT NULL CHECK (length(relative_name) BETWEEN 1 AND 4096 AND relative_name !~ '(^/|(^|/)\.\.(/|$))'),
    export_date date,
    observed_size bigint CHECK (observed_size IS NULL OR observed_size >= 0),
    observed_modified_at timestamptz,
    observed_identity text CHECK (observed_identity IS NULL OR length(observed_identity) <= 512),
    successful_checksum bytea CHECK (successful_checksum IS NULL OR octet_length(successful_checksum) = 32),
    successful_observed_at timestamptz,
    successful_job_id uuid,
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    PRIMARY KEY (account_id, source_id, relative_name),
    FOREIGN KEY (source_id, account_id) REFERENCES app.sources(id, account_id) ON DELETE CASCADE,
    FOREIGN KEY (successful_job_id, account_id) REFERENCES app.jobs(id, account_id) ON DELETE RESTRICT,
    CHECK ((successful_checksum IS NULL) = (successful_observed_at IS NULL)),
    CHECK (successful_checksum IS NOT NULL OR successful_job_id IS NULL)
);
CREATE INDEX source_objects_incremental_idx ON app.source_objects (account_id, source_id, export_date, relative_name);

CREATE TABLE app.ingest_file_slot_guard (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp()
);
INSERT INTO app.ingest_file_slot_guard(singleton) VALUES(true);

CREATE TABLE app.ingest_file_slot_limits (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    account_limit integer NOT NULL CHECK (account_limit BETWEEN 1 AND 16),
    global_limit integer NOT NULL CHECK (global_limit BETWEEN 1 AND 16 AND account_limit <= global_limit),
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp()
);
INSERT INTO app.ingest_file_slot_limits(singleton,account_limit,global_limit) VALUES(true,2,4);

CREATE TABLE app.ingest_file_slots (
    slot_token uuid PRIMARY KEY,
    account_id uuid NOT NULL,
    source_id uuid NOT NULL,
    job_id uuid NOT NULL,
    worker_id text NOT NULL CHECK (length(worker_id) BETWEEN 1 AND 200),
    lease_token uuid NOT NULL,
    acquired_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    FOREIGN KEY (source_id,account_id) REFERENCES app.sources(id,account_id) ON DELETE CASCADE,
    FOREIGN KEY (job_id,account_id) REFERENCES app.jobs(id,account_id) ON DELETE CASCADE
);
CREATE INDEX ingest_file_slots_account_idx ON app.ingest_file_slots(account_id);
CREATE INDEX ingest_file_slots_job_idx ON app.ingest_file_slots(job_id);

CREATE TABLE app.job_file_candidate_sets (
    job_id uuid PRIMARY KEY,
    account_id uuid NOT NULL,
    source_id uuid NOT NULL,
    candidate_count integer NOT NULL CHECK (candidate_count BETWEEN 0 AND 10000),
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    FOREIGN KEY(job_id,account_id) REFERENCES app.jobs(id,account_id) ON DELETE CASCADE,
    FOREIGN KEY(source_id,account_id) REFERENCES app.sources(id,account_id) ON DELETE RESTRICT
);

CREATE TABLE app.job_file_candidates (
    job_id uuid NOT NULL,
    account_id uuid NOT NULL,
    source_id uuid NOT NULL,
    relative_name text NOT NULL CHECK (length(relative_name) BETWEEN 1 AND 4096 AND relative_name !~ '/' AND relative_name NOT IN ('.','..')),
    export_date date NOT NULL,
    observed_size bigint NOT NULL CHECK (observed_size >= 0),
    modified_sec bigint NOT NULL,
    modified_ns integer NOT NULL CHECK (modified_ns BETWEEN 0 AND 999999999),
    device numeric(20,0) NOT NULL CHECK (device >= 0),
    inode numeric(20,0) NOT NULL CHECK (inode >= 0),
    ctime_sec bigint NOT NULL,
    ctime_ns integer NOT NULL CHECK (ctime_ns BETWEEN 0 AND 999999999),
    action text NOT NULL CHECK (action IN ('process','skip')),
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    PRIMARY KEY(job_id,relative_name),
    FOREIGN KEY(job_id,account_id) REFERENCES app.jobs(id,account_id) ON DELETE CASCADE,
    FOREIGN KEY(source_id,account_id) REFERENCES app.sources(id,account_id) ON DELETE RESTRICT
);

CREATE TABLE app.job_events (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    account_id uuid NOT NULL,
    job_id uuid NOT NULL,
    severity text NOT NULL CHECK (severity IN ('info','warning','error')),
    code text NOT NULL CHECK (code ~ '^[a-z][a-z0-9-]{0,63}$'),
    safe_message text NOT NULL CHECK (length(safe_message) BETWEEN 1 AND 512),
    fields jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    FOREIGN KEY (job_id, account_id) REFERENCES app.jobs(id, account_id) ON DELETE CASCADE
);
CREATE INDEX job_events_job_idx ON app.job_events (account_id, job_id, created_at, id);

CREATE TABLE app.job_logs (
    id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    account_id uuid NOT NULL,
    job_id uuid NOT NULL,
    severity text NOT NULL CHECK (severity IN ('debug','info','warning','error')),
    code text NOT NULL CHECK (code ~ '^[a-z][a-z0-9-]{0,63}$'),
    redacted_message text NOT NULL CHECK (length(redacted_message) BETWEEN 1 AND 1024),
    fields jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    FOREIGN KEY (job_id, account_id) REFERENCES app.jobs(id, account_id) ON DELETE CASCADE
);
CREATE INDEX job_logs_job_idx ON app.job_logs (account_id, job_id, created_at, id);

CREATE TABLE app.notifications (
    id uuid PRIMARY KEY,
    account_id uuid NOT NULL REFERENCES app.accounts(id) ON DELETE CASCADE,
    type text NOT NULL CHECK (type ~ '^[a-z][a-z0-9-]{0,63}$'),
    severity text NOT NULL CHECK (severity IN ('info','warning','error')),
    state text NOT NULL DEFAULT 'unresolved' CHECK (state IN ('unresolved','remind','resolved','dismissed')),
    condition_key text NOT NULL CHECK (length(condition_key) BETWEEN 1 AND 200),
    subject_type text NOT NULL CHECK (subject_type IN ('account','job','source')),
    subject_id uuid,
    job_id uuid,
    source_id uuid,
    title text NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    message text NOT NULL CHECK (length(message) BETWEEN 1 AND 512),
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    resolved_at timestamptz,
    remind_at timestamptz,
    FOREIGN KEY (job_id, account_id) REFERENCES app.jobs(id, account_id) ON DELETE CASCADE,
    FOREIGN KEY (source_id, account_id) REFERENCES app.sources(id, account_id) ON DELETE CASCADE,
    CHECK ((subject_type = 'account' AND subject_id IS NULL) OR (subject_type <> 'account' AND subject_id IS NOT NULL)),
    CHECK ((state IN ('resolved','dismissed')) = (resolved_at IS NOT NULL)),
    CHECK ((state = 'remind') = (remind_at IS NOT NULL))
);
CREATE UNIQUE INDEX notifications_unresolved_condition_idx
    ON app.notifications (account_id, type, condition_key, subject_type, COALESCE(subject_id, account_id))
    WHERE state IN ('unresolved','remind');
CREATE UNIQUE INDEX notifications_condition_idx ON app.notifications(account_id,type,condition_key);
CREATE INDEX notifications_account_idx ON app.notifications (account_id, state, created_at DESC, id);

CREATE TABLE app.source_sync_state (
    account_id uuid NOT NULL,
    source_id uuid NOT NULL,
    last_sync_started_at timestamptz,
    last_sync_succeeded_at timestamptz,
    last_new_export_discovered_at timestamptz,
    last_new_export_date date,
    stale_since date,
    PRIMARY KEY(account_id,source_id),
    FOREIGN KEY(source_id,account_id) REFERENCES app.sources(id,account_id) ON DELETE CASCADE
);
INSERT INTO app.source_sync_state(account_id,source_id)
SELECT account_id,id FROM app.sources;

CREATE TABLE app.auto_sync_policy (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    cadence interval NOT NULL CHECK (cadence BETWEEN interval '5 minutes' AND interval '168 hours'),
    stale_days integer NOT NULL CHECK (stale_days BETWEEN 1 AND 30),
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp()
);
INSERT INTO app.auto_sync_policy(singleton,cadence,stale_days) VALUES(true,interval '24 hours',3);

CREATE TABLE app.account_sync_schedules (
    account_id uuid PRIMARY KEY REFERENCES app.accounts(id) ON DELETE CASCADE,
    next_run_at timestamptz NOT NULL,
    last_enqueued_at timestamptz,
    last_job_id uuid,
    lease_worker text CHECK (lease_worker IS NULL OR length(lease_worker) BETWEEN 1 AND 200),
    lease_token uuid,
    lease_expires_at timestamptz,
    consecutive_failures integer NOT NULL DEFAULT 0 CHECK (consecutive_failures BETWEEN 0 AND 1000000),
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    FOREIGN KEY(last_job_id,account_id) REFERENCES app.jobs(id,account_id) ON DELETE SET NULL (last_job_id),
    CHECK ((lease_worker IS NULL)=(lease_token IS NULL) AND (lease_token IS NULL)=(lease_expires_at IS NULL))
);
CREATE INDEX account_sync_schedules_due_idx ON app.account_sync_schedules(next_run_at,account_id);

-- +goose StatementBegin
CREATE FUNCTION app.configure_auto_sync_policy(requested_cadence interval, requested_stale_days integer)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE current_cadence interval;
DECLARE current_stale_days integer;
BEGIN
    IF requested_cadence NOT BETWEEN interval '5 minutes' AND interval '168 hours' OR requested_stale_days NOT BETWEEN 1 AND 30 THEN
        RAISE EXCEPTION 'invalid automatic sync policy' USING ERRCODE='22023';
    END IF;
    SELECT cadence,stale_days INTO current_cadence,current_stale_days FROM app.auto_sync_policy WHERE singleton FOR UPDATE;
    IF current_cadence=requested_cadence AND current_stale_days=requested_stale_days THEN RETURN true; END IF;
    UPDATE app.auto_sync_policy SET cadence=requested_cadence,stale_days=requested_stale_days,updated_at=transaction_timestamp() WHERE singleton;
    RETURN true;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.claim_due_sync_account(claiming_worker text,new_lease_token uuid,lease_duration interval,runtime_version integer)
RETURNS TABLE(account_id uuid,lease_token uuid,next_run_at timestamptz)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
BEGIN
    IF runtime_version<6 THEN RAISE EXCEPTION 'worker runtime version 6 or newer is required' USING ERRCODE='55000'; END IF;
    IF claiming_worker='' OR new_lease_token IS NULL OR lease_duration<interval '1 second' OR lease_duration>interval '15 minutes' THEN
        RAISE EXCEPTION 'invalid scheduler lease arguments' USING ERRCODE='22023';
    END IF;
    INSERT INTO app.account_sync_schedules(account_id,next_run_at)
        SELECT account.id,clock_timestamp() FROM app.accounts account
         WHERE account.state='active'
        ON CONFLICT ON CONSTRAINT account_sync_schedules_pkey DO NOTHING;
    UPDATE app.account_sync_schedules schedule
       SET lease_worker=NULL,lease_token=NULL,lease_expires_at=NULL,updated_at=transaction_timestamp()
     WHERE schedule.lease_expires_at<clock_timestamp();
    RETURN QUERY WITH due AS (
        SELECT schedule.account_id FROM app.account_sync_schedules schedule
         JOIN app.accounts account ON account.id=schedule.account_id
         WHERE account.state='active' AND schedule.next_run_at<=clock_timestamp() AND schedule.lease_token IS NULL
         ORDER BY EXISTS (SELECT 1 FROM app.sources source WHERE source.account_id=schedule.account_id
             AND source.deleted_at IS NULL AND source.status='connected' AND source.auto_sync_enabled) DESC,
             schedule.next_run_at,schedule.account_id FOR UPDATE OF schedule SKIP LOCKED LIMIT 1
    )
    UPDATE app.account_sync_schedules schedule SET lease_worker=claiming_worker,lease_token=new_lease_token,
        lease_expires_at=clock_timestamp()+lease_duration,updated_at=transaction_timestamp()
      FROM due WHERE schedule.account_id=due.account_id
      RETURNING schedule.account_id,schedule.lease_token,schedule.next_run_at;
END;
$$;
-- +goose StatementEnd

-- The encrypted envelope is exposed only while this worker owns a live account scheduler lease.
-- +goose StatementBegin
CREATE FUNCTION app.read_leased_sync_sources(target_account_id uuid,claiming_worker text,current_lease_token uuid)
RETURNS TABLE(source_id uuid,generation bigint,config_envelope bytea,display_name text,source_type text)
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
    SELECT source.id,source.generation,source.config_envelope,source.display_name,source.type
      FROM app.account_sync_schedules schedule
      JOIN app.accounts account ON account.id=schedule.account_id
      JOIN app.sources source ON source.account_id=schedule.account_id
     WHERE schedule.account_id=target_account_id AND schedule.lease_worker=claiming_worker
       AND schedule.lease_token=current_lease_token AND schedule.lease_expires_at>=clock_timestamp()
       AND account.state='active'
       AND source.deleted_at IS NULL AND source.status='connected' AND source.auto_sync_enabled
     ORDER BY source.id
$$;
-- +goose StatementEnd

-- Snapshots are supplied only after worker-side decrypt and canonical validation. This function locks and
-- recomputes the current source set, preventing deletion or generation changes from creating stale children.
-- +goose StatementBegin
CREATE FUNCTION app.enqueue_leased_scheduled_ingest(target_account_id uuid,claiming_worker text,current_lease_token uuid,
    parent_id uuid,input_coalescing_key bytea,children jsonb)
RETURNS TABLE(job_id uuid,reused boolean)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE child jsonb;
DECLARE eligible_count integer;
DECLARE inserted boolean;
DECLARE invalid_count integer := 0;
BEGIN
    IF parent_id IS NULL OR octet_length(input_coalescing_key)<>32 OR jsonb_typeof(children)<>'array' OR jsonb_array_length(children)>100 THEN
        RAISE EXCEPTION 'invalid scheduled ingest artifacts' USING ERRCODE='22023';
    END IF;
    -- Serialize with account deletion before validating the scheduler lease and source set.
    PERFORM 1 FROM app.accounts account WHERE account.id=target_account_id AND account.state='active' FOR UPDATE;
    IF NOT FOUND THEN RETURN; END IF;
    PERFORM 1 FROM app.account_sync_schedules schedule WHERE schedule.account_id=target_account_id
      AND schedule.lease_worker=claiming_worker AND schedule.lease_token=current_lease_token
      AND schedule.lease_expires_at>=clock_timestamp() FOR UPDATE;
    IF NOT FOUND THEN RETURN; END IF;
    PERFORM set_config('app.account_id',target_account_id::text,true);
    PERFORM 1 FROM app.sources source WHERE source.account_id=target_account_id AND source.deleted_at IS NULL
      AND source.status='connected' AND source.auto_sync_enabled ORDER BY source.id FOR UPDATE;
    SELECT count(*) INTO eligible_count FROM app.sources source WHERE source.account_id=target_account_id
      AND source.deleted_at IS NULL AND source.status='connected' AND source.auto_sync_enabled;
    IF eligible_count<>jsonb_array_length(children) OR EXISTS (
        SELECT 1 FROM app.sources source WHERE source.account_id=target_account_id AND source.deleted_at IS NULL
          AND source.status='connected' AND source.auto_sync_enabled AND NOT EXISTS (
            SELECT 1 FROM jsonb_array_elements(children) item
             WHERE (item->>'sourceId')::uuid=source.id AND (item->>'generation')::bigint=source.generation)
    ) OR EXISTS (SELECT 1 FROM jsonb_array_elements(children) item WHERE
        (item->>'failureCode' IS NULL AND COALESCE(item->>'snapshot','')='') OR
        (item->>'failureCode' IS NOT NULL AND (item->>'failureCode'<>'source-config-invalid' OR item ? 'snapshot'))
    ) THEN RAISE EXCEPTION 'scheduled source set changed' USING ERRCODE='40001'; END IF;
    IF eligible_count=0 THEN
        RETURN QUERY SELECT NULL::uuid,false;
        RETURN;
    END IF;
    -- Schema-4/6 children predate parent coalescing. Do not enqueue over an active child for the same source.
    IF EXISTS (
        SELECT 1 FROM app.jobs job
        JOIN app.job_config_snapshots snapshot ON snapshot.job_id=job.id AND snapshot.account_id=job.account_id
        LEFT JOIN app.job_source_contexts source_context ON source_context.job_id=job.id AND source_context.account_id=job.account_id
        WHERE job.account_id=target_account_id AND job.kind IN ('manual_ingest_source','scheduled_ingest_source')
          AND job.status IN ('queued','running')
          AND (job.coalescing_key IS NOT NULL OR source_context.job_id IS NULL)
          AND EXISTS (SELECT 1 FROM jsonb_array_elements(children) item WHERE (item->>'sourceId')::uuid=snapshot.source_id)
    ) THEN
        RETURN QUERY SELECT NULL::uuid,true;
        RETURN;
    END IF;
    INSERT INTO app.jobs(id,account_id,kind,priority,parameters,coalescing_version,coalescing_scope,coalescing_key)
    SELECT parent_id,target_account_id,'scheduled_ingest',60,
        jsonb_build_object('mode','incremental','sourceIds',jsonb_agg(upper(replace(item->>'sourceId','-','')) ORDER BY item->>'sourceId')),1,
        'scheduled-ingest/v1',input_coalescing_key FROM jsonb_array_elements(children) item
    ON CONFLICT ((CASE WHEN account_id IS NOT NULL THEN 'account' ELSE 'administrator' END),
        (COALESCE(account_id,administrator_id)),coalescing_scope,coalescing_key)
    WHERE status IN ('queued','running') AND coalescing_key IS NOT NULL DO NOTHING;
    inserted:=FOUND;
    IF NOT inserted THEN
        RETURN QUERY SELECT job.id,true FROM app.jobs job WHERE job.account_id=target_account_id AND job.parent_job_id IS NULL
          AND job.coalescing_version=1 AND job.coalescing_scope='scheduled-ingest/v1' AND job.coalescing_key=input_coalescing_key
          AND job.status IN ('queued','running');
        RETURN;
    END IF;
    INSERT INTO app.job_progress(job_id,account_id) VALUES(parent_id,target_account_id) ON CONFLICT DO NOTHING;
    IF NOT EXISTS (SELECT 1 FROM app.job_progress progress
        WHERE progress.job_id=parent_id AND progress.account_id=target_account_id) THEN
        RAISE EXCEPTION 'scheduled ingest parent progress mismatch' USING ERRCODE='40001';
    END IF;
    FOR child IN SELECT value FROM jsonb_array_elements(children) value ORDER BY value->>'sourceId' LOOP
        INSERT INTO app.jobs(id,parent_job_id,account_id,kind,priority,parameters)
        VALUES((child->>'childId')::uuid,parent_id,target_account_id,'scheduled_ingest_source',60,
            jsonb_build_object('sourceId',upper(replace(child->>'sourceId','-','')),'generation',(child->>'generation')::bigint,'mode','incremental'));
        IF child->>'failureCode' IS NULL THEN
            INSERT INTO app.job_config_snapshots(job_id,account_id,source_id,source_generation,config_envelope)
            VALUES((child->>'childId')::uuid,target_account_id,(child->>'sourceId')::uuid,(child->>'generation')::bigint,decode(child->>'snapshot','base64'));
        ELSE
            invalid_count:=invalid_count+1;
        END IF;
        INSERT INTO app.job_source_contexts(job_id,account_id,source_id,source_generation,display_name,source_type)
        SELECT (child->>'childId')::uuid,target_account_id,source.id,source.generation,source.display_name,source.type
          FROM app.sources source WHERE source.account_id=target_account_id AND source.id=(child->>'sourceId')::uuid
        ON CONFLICT DO NOTHING;
        IF NOT EXISTS (SELECT 1 FROM app.job_source_contexts context
            WHERE context.job_id=(child->>'childId')::uuid AND context.account_id=target_account_id
              AND context.source_id=(child->>'sourceId')::uuid
              AND context.source_generation=(child->>'generation')::bigint
              AND context.display_name=(SELECT source.display_name FROM app.sources source
                  WHERE source.account_id=target_account_id AND source.id=(child->>'sourceId')::uuid)
              AND context.source_type=(SELECT source.type FROM app.sources source
                  WHERE source.account_id=target_account_id AND source.id=(child->>'sourceId')::uuid)) THEN
            RAISE EXCEPTION 'scheduled ingest source context mismatch' USING ERRCODE='40001';
        END IF;
        INSERT INTO app.job_progress(job_id,account_id) VALUES((child->>'childId')::uuid,target_account_id) ON CONFLICT DO NOTHING;
        IF NOT EXISTS (SELECT 1 FROM app.job_progress progress
            WHERE progress.job_id=(child->>'childId')::uuid AND progress.account_id=target_account_id) THEN
            RAISE EXCEPTION 'scheduled ingest child progress mismatch' USING ERRCODE='40001';
        END IF;
    END LOOP;
    IF invalid_count>0 THEN
		FOR child IN SELECT value FROM jsonb_array_elements(children) value WHERE value->>'failureCode' IS NOT NULL LOOP
			IF NOT app.claim_job((child->>'childId')::uuid,claiming_worker,current_lease_token,interval '1 minute') OR
			   NOT app.finish_job((child->>'childId')::uuid,claiming_worker,current_lease_token,'failed',
			       'source-config-invalid','Source configuration could not be read.') THEN
				RAISE EXCEPTION 'invalid scheduled child transition' USING ERRCODE='55000';
			END IF;
		END LOOP;
    END IF;
    RETURN QUERY SELECT parent_id,false;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.finish_sync_account(target_account_id uuid,claiming_worker text,current_lease_token uuid,enqueued_job_id uuid)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE policy app.auto_sync_policy%ROWTYPE;
DECLARE source_row record;
DECLARE completed_at timestamptz;
BEGIN
    PERFORM 1 FROM app.account_sync_schedules schedule WHERE schedule.account_id=target_account_id
      AND schedule.lease_worker=claiming_worker AND schedule.lease_token=current_lease_token
      AND schedule.lease_expires_at>=clock_timestamp() FOR UPDATE;
    IF NOT FOUND THEN RETURN false; END IF;
    SELECT * INTO policy FROM app.auto_sync_policy WHERE singleton;
    completed_at:=clock_timestamp();
    PERFORM set_config('app.account_id',target_account_id::text,true);
    -- Evaluation also clears warnings for sources that became manual-only, disconnected, failed, or deleted.
    FOR source_row IN SELECT source.id FROM app.sources source WHERE source.account_id=target_account_id ORDER BY source.id LOOP
        PERFORM app.evaluate_source_staleness(source_row.id,policy.stale_days,clock_timestamp());
    END LOOP;
    UPDATE app.account_sync_schedules schedule SET next_run_at=CASE
        WHEN schedule.next_run_at='-infinity'::timestamptz THEN completed_at+policy.cadence
        ELSE schedule.next_run_at+policy.cadence*((floor(extract(epoch FROM (completed_at-schedule.next_run_at))/extract(epoch FROM policy.cadence))+1)::bigint)
      END,
      last_enqueued_at=CASE WHEN enqueued_job_id IS NOT NULL THEN completed_at ELSE schedule.last_enqueued_at END,
      last_job_id=COALESCE(enqueued_job_id,schedule.last_job_id),lease_worker=NULL,lease_token=NULL,lease_expires_at=NULL,
      consecutive_failures=0,updated_at=transaction_timestamp() WHERE schedule.account_id=target_account_id;
    RETURN true;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.release_sync_account(target_account_id uuid,claiming_worker text,current_lease_token uuid,retry_base interval)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
BEGIN
    IF retry_base<interval '1 second' OR retry_base>interval '5 minutes' THEN
        RAISE EXCEPTION 'invalid scheduler retry delay' USING ERRCODE='22023';
    END IF;
    UPDATE app.account_sync_schedules schedule SET consecutive_failures=LEAST(schedule.consecutive_failures,999999)+1,
      next_run_at=clock_timestamp()+LEAST(retry_base*(power(2::numeric,LEAST(schedule.consecutive_failures,7)+1))::double precision,interval '5 minutes'),
      lease_worker=NULL,lease_token=NULL,lease_expires_at=NULL,updated_at=transaction_timestamp() WHERE schedule.account_id=target_account_id
      AND schedule.lease_worker=claiming_worker AND schedule.lease_token=current_lease_token;
    RETURN FOUND;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.read_owned_sync_schedule()
RETURNS TABLE(cadence_seconds integer,stale_days integer,next_run_at timestamptz,last_enqueued_at timestamptz,last_job_id uuid)
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
    SELECT extract(epoch FROM policy.cadence)::integer,policy.stale_days,schedule.next_run_at,schedule.last_enqueued_at,schedule.last_job_id
      FROM app.auto_sync_policy policy LEFT JOIN app.account_sync_schedules schedule ON schedule.account_id=app.current_account_id()
     WHERE policy.singleton
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.track_source_sync_job()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    target_source uuid;
BEGIN
    IF NEW.kind NOT IN ('manual_ingest_source','scheduled_ingest_source') OR NEW.status=OLD.status THEN
        RETURN NEW;
    END IF;
    SELECT source_id INTO target_source FROM app.job_source_contexts WHERE job_id=NEW.id AND account_id=NEW.account_id;
    IF target_source IS NULL THEN RETURN NEW; END IF;
    INSERT INTO app.source_sync_state(account_id,source_id,last_sync_started_at,last_sync_succeeded_at)
    VALUES(NEW.account_id,target_source,
        CASE WHEN NEW.status='running' THEN transaction_timestamp() END,
        CASE WHEN NEW.status='succeeded' THEN transaction_timestamp() END)
    ON CONFLICT(account_id,source_id) DO UPDATE SET
        last_sync_started_at=CASE WHEN NEW.status='running' THEN transaction_timestamp() ELSE app.source_sync_state.last_sync_started_at END,
        last_sync_succeeded_at=CASE WHEN NEW.status='succeeded' THEN transaction_timestamp() ELSE app.source_sync_state.last_sync_succeeded_at END;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER jobs_source_sync_after_update AFTER UPDATE OF status ON app.jobs
    FOR EACH ROW EXECUTE FUNCTION app.track_source_sync_job();

-- +goose StatementBegin
CREATE FUNCTION app.create_source_sync_state()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
BEGIN
    INSERT INTO app.source_sync_state(account_id,source_id) VALUES(NEW.account_id,NEW.id);
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER sources_sync_state_after_insert AFTER INSERT ON app.sources
    FOR EACH ROW EXECUTE FUNCTION app.create_source_sync_state();

-- +goose StatementBegin
CREATE FUNCTION app.notify_terminal_ingest_parent()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    notification_severity text;
    notification_title text;
    notification_message text;
BEGIN
    IF NEW.parent_job_id IS NOT NULL OR NEW.kind NOT IN ('manual_ingest','scheduled_ingest') OR
       NEW.status NOT IN ('succeeded','partially_succeeded','failed','cancelled') OR
       OLD.status IN ('succeeded','partially_succeeded','failed','cancelled') OR
       (NEW.kind='scheduled_ingest' AND NEW.status IN ('succeeded','cancelled')) THEN
        RETURN NEW;
    END IF;
    SELECT severity,title,message INTO notification_severity,notification_title,notification_message FROM (VALUES
        ('succeeded','info','Data sync completed','Your requested data sync completed successfully.'),
        ('partially_succeeded','warning','Data sync partially completed','Data sync completed, but one or more sources did not finish.'),
        ('failed','error','Data sync failed','Data sync could not be completed. You can retry the unsuccessful sources.'),
        ('cancelled','info','Data sync cancelled','Your requested data sync was cancelled.')
    ) policy(status,severity,title,message) WHERE status=NEW.status;
    INSERT INTO app.notifications(id,account_id,type,severity,condition_key,subject_type,subject_id,job_id,title,message)
    VALUES(gen_random_uuid(),NEW.account_id,'ingest-terminal',notification_severity,'job:'||NEW.id::text,
        'job',NEW.id,NEW.id,notification_title,notification_message)
    ON CONFLICT(account_id,type,condition_key) DO NOTHING;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER jobs_terminal_notification_after_update AFTER UPDATE OF status ON app.jobs
    FOR EACH ROW EXECUTE FUNCTION app.notify_terminal_ingest_parent();

-- +goose StatementBegin
CREATE FUNCTION app.evaluate_source_staleness(target_source_id uuid DEFAULT NULL, threshold_days integer DEFAULT 3,
    as_of timestamptz DEFAULT clock_timestamp())
RETURNS integer
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    source_row record;
    local_today date;
    baseline date;
    effective_timezone text;
    affected integer := 0;
BEGIN
    IF threshold_days NOT BETWEEN 1 AND 30 OR as_of IS NULL THEN
        RAISE EXCEPTION 'invalid staleness arguments' USING ERRCODE='22023';
    END IF;
    FOR source_row IN
        SELECT source.id,source.account_id,source.created_at,source.deleted_at,source.status,
               source.auto_sync_enabled,state.last_new_export_discovered_at,
               COALESCE(preference.timezone,'UTC') AS timezone
          FROM app.sources source
          LEFT JOIN app.source_sync_state state ON state.account_id=source.account_id AND state.source_id=source.id
          LEFT JOIN app.preferences preference ON preference.account_id=source.account_id
         WHERE source.account_id=app.current_account_id()
           AND (target_source_id IS NULL OR source.id=target_source_id)
         ORDER BY source.id FOR UPDATE OF source
    LOOP
        IF source_row.deleted_at IS NOT NULL OR source_row.status<>'connected' OR NOT source_row.auto_sync_enabled THEN
            UPDATE app.source_sync_state SET stale_since=NULL
             WHERE account_id=source_row.account_id AND source_id=source_row.id;
            UPDATE app.notifications SET state='resolved',resolved_at=transaction_timestamp(),remind_at=NULL,
                updated_at=transaction_timestamp()
             WHERE account_id=source_row.account_id AND type='source-stale'
               AND condition_key='source:'||source_row.id::text AND state IN ('unresolved','remind');
            CONTINUE;
        END IF;
        effective_timezone := source_row.timezone;
        BEGIN
            local_today := (as_of AT TIME ZONE effective_timezone)::date;
        EXCEPTION WHEN invalid_parameter_value THEN
            effective_timezone := 'UTC';
            local_today := (as_of AT TIME ZONE 'UTC')::date;
        END;
        baseline := COALESCE((source_row.last_new_export_discovered_at AT TIME ZONE effective_timezone)::date,
            (source_row.created_at AT TIME ZONE effective_timezone)::date);
        INSERT INTO app.source_sync_state(account_id,source_id,stale_since)
        VALUES(source_row.account_id,source_row.id,
            CASE WHEN local_today>=baseline+threshold_days THEN baseline+threshold_days END)
        ON CONFLICT(account_id,source_id) DO UPDATE SET stale_since=EXCLUDED.stale_since;
        IF local_today>=baseline+threshold_days THEN
            INSERT INTO app.notifications(id,account_id,type,severity,state,condition_key,subject_type,subject_id,source_id,title,message)
            VALUES(gen_random_uuid(),source_row.account_id,'source-stale','warning','unresolved','source:'||source_row.id::text,
                'source',source_row.id,source_row.id,'No recent source data','No new export data has been found for this source recently.')
            ON CONFLICT(account_id,type,condition_key) DO UPDATE SET state='unresolved',resolved_at=NULL,remind_at=NULL,
                updated_at=transaction_timestamp() WHERE app.notifications.state='resolved';
            affected := affected+1;
        END IF;
    END LOOP;
    RETURN affected;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.dismiss_owned_notification(target_notification_id uuid, requester_id uuid)
RETURNS TABLE(notification_id uuid,new_state text)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
BEGIN
    IF requester_id IS NULL OR NOT EXISTS (SELECT 1 FROM app.users account_user
        WHERE account_user.principal_id=requester_id AND account_user.account_id=app.current_account_id()) THEN
        RETURN;
    END IF;
    RETURN QUERY UPDATE app.notifications notification SET
        state=CASE WHEN notification.type='source-stale' AND notification.state IN ('unresolved','remind') THEN 'remind' ELSE 'dismissed' END,
        resolved_at=CASE WHEN notification.type='source-stale' AND notification.state IN ('unresolved','remind') THEN NULL ELSE transaction_timestamp() END,
        remind_at=CASE WHEN notification.type='source-stale' AND notification.state IN ('unresolved','remind')
            THEN clock_timestamp()+interval '24 hours' ELSE NULL END,
        updated_at=transaction_timestamp()
      WHERE notification.id=target_notification_id AND notification.account_id=app.current_account_id()
        AND notification.state IN ('unresolved','remind')
      RETURNING notification.id,notification.state;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.read_owned_job_files(target_job_id uuid, requested_limit integer, requested_offset integer)
RETURNS TABLE(total_count bigint,id uuid,job_id uuid,source_id uuid,source_generation bigint,display_name text,source_type text,
    basename text,state text,size_bytes bigint,processing_started_at timestamptz,processed_at timestamptz,
    failure_code text,failure_summary text,created_at timestamptz,updated_at timestamptz)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
BEGIN
    IF requested_limit NOT BETWEEN 1 AND 100 OR requested_offset<0 THEN
        RAISE EXCEPTION 'invalid pagination' USING ERRCODE='22023';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM app.jobs owned_job WHERE owned_job.id=target_job_id
        AND owned_job.account_id=app.current_account_id()
        AND owned_job.kind IN ('manual_ingest','scheduled_ingest','manual_ingest_source','scheduled_ingest_source')) THEN RETURN; END IF;
    RETURN QUERY SELECT count(*) OVER(),file.id,file.job_id,context.source_id,context.source_generation,context.display_name,
        context.source_type,regexp_replace(file.relative_name,'^.*/',''),file.state,file.size_bytes,file.processing_started_at,
        file.processed_at,
        CASE WHEN file.failure_code IN ('source-file-unavailable','source-file-mutated','source-file-invalid') THEN file.failure_code
             WHEN file.state='failed' THEN 'source-file-invalid' END,
        CASE WHEN file.failure_code='source-file-unavailable' THEN 'A source file could not be read.'
             WHEN file.failure_code='source-file-mutated' THEN 'A source file changed before it could be processed.'
             WHEN file.state='failed' THEN 'A source file could not be processed.' END,
        file.created_at,file.updated_at
      FROM app.source_files file JOIN app.jobs child ON child.id=file.job_id AND child.account_id=file.account_id
      JOIN app.job_source_contexts context ON context.job_id=child.id AND context.account_id=child.account_id
     WHERE file.account_id=app.current_account_id() AND (child.id=target_job_id OR child.parent_job_id=target_job_id)
     ORDER BY file.created_at,file.id LIMIT requested_limit OFFSET requested_offset;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.read_owned_job_logs(target_job_id uuid, requested_limit integer, requested_offset integer)
RETURNS TABLE(total_count bigint,id bigint,job_id uuid,severity text,code text,message text,fields jsonb,created_at timestamptz)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
BEGIN
    IF requested_limit NOT BETWEEN 1 AND 100 OR requested_offset<0 THEN
        RAISE EXCEPTION 'invalid pagination' USING ERRCODE='22023';
    END IF;
    IF NOT EXISTS (SELECT 1 FROM app.jobs owned_job WHERE owned_job.id=target_job_id
        AND owned_job.account_id=app.current_account_id()
        AND owned_job.kind IN ('manual_ingest','scheduled_ingest','manual_ingest_source','scheduled_ingest_source')) THEN RETURN; END IF;
    RETURN QUERY SELECT count(*) OVER(),log.id,log.job_id,log.severity,log.code,log.redacted_message,log.fields,log.created_at
      FROM app.job_logs log JOIN app.jobs job ON job.id=log.job_id AND job.account_id=log.account_id
     WHERE log.account_id=app.current_account_id() AND (job.id=target_job_id OR job.parent_job_id=target_job_id)
     ORDER BY log.created_at,log.id LIMIT requested_limit OFFSET requested_offset;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.count_owned_job_rows(target_job_id uuid, row_kind text)
RETURNS bigint
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE result bigint;
BEGIN
    IF row_kind='file' THEN
        SELECT count(*) INTO result FROM app.source_files file JOIN app.jobs job ON job.id=file.job_id AND job.account_id=file.account_id
         WHERE file.account_id=app.current_account_id() AND (job.id=target_job_id OR job.parent_job_id=target_job_id);
    ELSIF row_kind='log' THEN
        SELECT count(*) INTO result FROM app.job_logs log JOIN app.jobs job ON job.id=log.job_id AND job.account_id=log.account_id
         WHERE log.account_id=app.current_account_id() AND (job.id=target_job_id OR job.parent_job_id=target_job_id);
    ELSE
        RAISE EXCEPTION 'invalid row kind' USING ERRCODE='22023';
    END IF;
    RETURN result;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.reject_immutable_read_model_write()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    -- Direct writes execute at depth 1; RI cascade actions invoke child-table triggers at a deeper level.
    IF TG_OP='DELETE' AND pg_trigger_depth()>1 THEN
        RETURN OLD;
    END IF;
    RAISE EXCEPTION '% rows are immutable', TG_TABLE_NAME USING ERRCODE = '23514';
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER job_source_contexts_immutable BEFORE UPDATE OR DELETE ON app.job_source_contexts
    FOR EACH ROW EXECUTE FUNCTION app.reject_immutable_read_model_write();
CREATE TRIGGER job_events_append_only BEFORE UPDATE OR DELETE ON app.job_events
    FOR EACH ROW EXECUTE FUNCTION app.reject_immutable_read_model_write();
CREATE TRIGGER job_logs_append_only BEFORE UPDATE OR DELETE ON app.job_logs
    FOR EACH ROW EXECUTE FUNCTION app.reject_immutable_read_model_write();
CREATE TRIGGER job_file_candidate_sets_immutable BEFORE UPDATE OR DELETE ON app.job_file_candidate_sets
    FOR EACH ROW EXECUTE FUNCTION app.reject_immutable_read_model_write();
CREATE TRIGGER job_file_candidates_immutable BEFORE UPDATE OR DELETE ON app.job_file_candidates
    FOR EACH ROW EXECUTE FUNCTION app.reject_immutable_read_model_write();

-- +goose StatementBegin
CREATE FUNCTION app.reject_ingest_child_coalescing()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    IF NEW.kind IN ('manual_ingest_source','scheduled_ingest_source') AND
       (NEW.coalescing_key IS NOT NULL OR NEW.coalescing_scope IS NOT NULL OR NEW.coalescing_version IS NOT NULL) AND NOT (
           jsonb_typeof(NEW.parameters)='object' AND NOT NEW.parameters ? 'mode' AND
           (SELECT count(*) FROM jsonb_object_keys(NEW.parameters))=2 AND
           NEW.parameters ? 'sourceId' AND NEW.parameters ? 'generation'
       ) THEN
        RAISE EXCEPTION 'ingest source children cannot be coalesced independently' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER jobs_ingest_child_coalescing_before_insert BEFORE INSERT ON app.jobs
    FOR EACH ROW EXECUTE FUNCTION app.reject_ingest_child_coalescing();

-- +goose StatementBegin
CREATE FUNCTION app.valid_ingest_child_parameters(value jsonb)
RETURNS boolean
LANGUAGE plpgsql
IMMUTABLE
SET search_path = pg_catalog
AS $$
DECLARE
    start_value date;
    end_value date;
BEGIN
    IF jsonb_typeof(value) <> 'object' OR value->>'sourceId' !~ '^[0-9A-F]{32}$' OR
       jsonb_typeof(value->'generation') <> 'number' OR (value->>'generation') !~ '^[1-9][0-9]*$' OR
       value->>'mode' NOT IN ('incremental','bounded') THEN
        RETURN false;
    END IF;
    IF value->>'mode' = 'incremental' THEN
        RETURN (SELECT count(*)=3 FROM jsonb_object_keys(value));
    END IF;
    IF ((NOT value ? 'legacySchema6' AND (SELECT count(*) FROM jsonb_object_keys(value)) <> 5) OR
        (value ? 'legacySchema6' AND ((SELECT count(*) FROM jsonb_object_keys(value)) <> 6 OR
         value->'legacySchema6' IS DISTINCT FROM 'true'::jsonb OR value->>'startDate'<>'0001-01-01' OR
         value->>'endDate'<>'9999-12-31'))) OR value->>'startDate' !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' OR
       value->>'endDate' !~ '^[0-9]{4}-[0-9]{2}-[0-9]{2}$' THEN
        RETURN false;
    END IF;
    BEGIN
        start_value := (value->>'startDate')::date;
        end_value := (value->>'endDate')::date;
    EXCEPTION WHEN datetime_field_overflow OR invalid_datetime_format THEN
        RETURN false;
    END;
    RETURN to_char(start_value,'YYYY-MM-DD')=value->>'startDate' AND
           to_char(end_value,'YYYY-MM-DD')=value->>'endDate' AND start_value <= end_value;
END;
$$;
-- +goose StatementEnd

-- Existing children without mode predate schema 6 and remain drainable; only new rows are constrained.
-- +goose StatementBegin
CREATE FUNCTION app.enforce_ingest_child_parameters()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, app
AS $$
BEGIN
    IF NEW.kind IN ('manual_ingest_source','scheduled_ingest_source') THEN
        IF jsonb_typeof(NEW.parameters)='object' AND NOT NEW.parameters ? 'mode' AND
           (SELECT count(*) FROM jsonb_object_keys(NEW.parameters))=2 AND
           NEW.parameters ? 'sourceId' AND NEW.parameters ? 'generation' THEN
            NEW.parameters:=NEW.parameters||jsonb_build_object(
                'mode','bounded','startDate','0001-01-01','endDate','9999-12-31','legacySchema6',true);
        END IF;
        IF NOT app.valid_ingest_child_parameters(NEW.parameters) THEN
            RAISE EXCEPTION 'invalid ingest child parameters' USING ERRCODE='22023';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER jobs_ingest_child_parameters_before_insert BEFORE INSERT ON app.jobs
    FOR EACH ROW EXECUTE FUNCTION app.enforce_ingest_child_parameters();

-- Schema-5 APIs insert the snapshot last. Materialize schema-6 read models without exposing its envelope.
-- +goose StatementBegin
CREATE FUNCTION app.create_legacy_ingest_read_models()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    child_row record;
    source_row record;
BEGIN
    SELECT job.account_id,job.parent_job_id,job.kind,job.parameters INTO child_row
      FROM app.jobs job WHERE job.id=NEW.job_id AND job.account_id=NEW.account_id;
    IF NOT FOUND OR child_row.kind NOT IN ('manual_ingest_source','scheduled_ingest_source') OR
       child_row.parameters->'legacySchema6' IS DISTINCT FROM 'true'::jsonb THEN
        RETURN NEW;
    END IF;
    SELECT source.id,source.generation,source.display_name,source.type INTO source_row
      FROM app.sources source WHERE source.id=NEW.source_id AND source.account_id=NEW.account_id FOR UPDATE;
    IF NOT FOUND OR source_row.generation<>NEW.source_generation THEN
        RAISE EXCEPTION 'legacy ingest snapshot source changed' USING ERRCODE='40001';
    END IF;
    PERFORM 1 FROM app.jobs parent WHERE parent.id=child_row.parent_job_id AND parent.account_id=NEW.account_id FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'legacy ingest parent changed' USING ERRCODE='40001';
    END IF;
    PERFORM 1 FROM app.jobs job WHERE job.id=NEW.job_id AND job.account_id=NEW.account_id FOR UPDATE;
    INSERT INTO app.job_source_contexts(job_id,account_id,source_id,source_generation,display_name,source_type)
    VALUES(NEW.job_id,NEW.account_id,source_row.id,source_row.generation,source_row.display_name,source_row.type)
    ON CONFLICT DO NOTHING;
    INSERT INTO app.job_progress(job_id,account_id)
    VALUES(child_row.parent_job_id,NEW.account_id),(NEW.job_id,NEW.account_id)
    ON CONFLICT DO NOTHING;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER job_config_snapshots_ingest_compatibility_after_insert
    AFTER INSERT ON app.job_config_snapshots FOR EACH ROW EXECUTE FUNCTION app.create_legacy_ingest_read_models();

CREATE INDEX jobs_retry_of_idx ON app.jobs(account_id,retry_of_job_id,created_at,id) WHERE retry_of_job_id IS NOT NULL;

-- +goose StatementBegin
CREATE FUNCTION app.enforce_ingest_retry_lineage()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, app
AS $$
DECLARE prior_kind text;
DECLARE prior_account uuid;
BEGIN
    IF NEW.retry_of_job_id IS NULL THEN RETURN NEW; END IF;
    SELECT kind,account_id INTO prior_kind,prior_account FROM app.jobs WHERE id=NEW.retry_of_job_id;
    IF NOT FOUND OR prior_account IS DISTINCT FROM NEW.account_id OR
       (NEW.kind IN ('manual_ingest','scheduled_ingest') AND prior_kind NOT IN ('manual_ingest','scheduled_ingest')) OR
       (NEW.kind IN ('manual_ingest_source','scheduled_ingest_source') AND prior_kind NOT IN ('manual_ingest_source','scheduled_ingest_source')) OR
       (NEW.kind NOT IN ('manual_ingest','scheduled_ingest','manual_ingest_source','scheduled_ingest_source') AND prior_kind<>NEW.kind) THEN
        RAISE EXCEPTION 'invalid ingest retry lineage' USING ERRCODE='23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER jobs_retry_lineage_before_insert BEFORE INSERT ON app.jobs
    FOR EACH ROW EXECUTE FUNCTION app.enforce_ingest_retry_lineage();

-- Schema-6 workers call this signature and must stop claiming immediately after upgrade.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.claim_next_worker_job(claiming_worker text, new_lease_token uuid, lease_duration interval)
RETURNS TABLE(job_id uuid, account_id uuid, kind text)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
BEGIN
    RAISE EXCEPTION 'worker runtime version 6 or newer is required' USING ERRCODE='55000';
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.claim_next_worker_job(claiming_worker text, new_lease_token uuid, lease_duration interval, runtime_version integer)
RETURNS TABLE(job_id uuid, account_id uuid, kind text)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
BEGIN
    IF runtime_version < 6 THEN
        RAISE EXCEPTION 'worker runtime version 6 or newer is required' USING ERRCODE='55000';
    END IF;
    RETURN QUERY SELECT claimed.job_id,claimed.account_id,claimed.kind
      FROM app.claim_next_worker_job_internal(claiming_worker,new_lease_token,lease_duration,true) claimed;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.configure_ingest_file_slot_limits(requested_account_limit integer, requested_global_limit integer)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    current_account_limit integer;
    current_global_limit integer;
BEGIN
    IF requested_account_limit NOT BETWEEN 1 AND 16 OR requested_global_limit NOT BETWEEN 1 AND 16 OR
       requested_account_limit > requested_global_limit THEN
        RAISE EXCEPTION 'invalid ingest file slot limits' USING ERRCODE='22023';
    END IF;
    PERFORM 1 FROM app.ingest_file_slot_guard WHERE singleton FOR UPDATE;
    SELECT account_limit,global_limit INTO current_account_limit,current_global_limit
      FROM app.ingest_file_slot_limits WHERE singleton FOR UPDATE;
    IF current_account_limit=requested_account_limit AND current_global_limit=requested_global_limit THEN
        RETURN true;
    END IF;
    IF EXISTS (SELECT 1 FROM app.ingest_file_slots) THEN
        RETURN false;
    END IF;
    UPDATE app.ingest_file_slot_limits SET account_limit=requested_account_limit,
        global_limit=requested_global_limit,updated_at=transaction_timestamp() WHERE singleton;
    RETURN true;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.acquire_ingest_file_slot(target_job_id uuid, claiming_worker text, current_lease_token uuid)
RETURNS uuid
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    target_account uuid;
    target_parent uuid;
    target_source uuid;
    new_token uuid;
    configured_account_limit integer;
    configured_global_limit integer;
BEGIN
    IF claiming_worker='' OR current_lease_token IS NULL THEN
        RAISE EXCEPTION 'invalid ingest file slot arguments' USING ERRCODE='22023';
    END IF;
    SELECT job.account_id,job.parent_job_id,context.source_id INTO target_account,target_parent,target_source
      FROM app.jobs job JOIN app.job_source_contexts context ON context.job_id=job.id AND context.account_id=job.account_id
     WHERE job.id=target_job_id AND job.account_id=app.current_account_id()
       AND job.kind IN ('manual_ingest_source','scheduled_ingest_source');
    IF NOT FOUND THEN RETURN NULL; END IF;
    PERFORM 1 FROM app.sources source WHERE source.id=target_source AND source.account_id=target_account
       AND source.deleted_at IS NULL FOR UPDATE;
    IF NOT FOUND THEN RETURN NULL; END IF;
    PERFORM 1 FROM app.jobs parent WHERE parent.id=target_parent AND parent.account_id=target_account
       AND parent.status='running' AND parent.cancel_requested_at IS NULL FOR UPDATE;
    IF NOT FOUND THEN RETURN NULL; END IF;
    PERFORM 1 FROM app.jobs job WHERE job.id=target_job_id AND job.account_id=target_account
       AND job.status='running' AND job.cancel_requested_at IS NULL AND job.worker_id=claiming_worker
       AND job.lease_token=current_lease_token AND job.lease_expires_at >= clock_timestamp() FOR UPDATE;
    IF NOT FOUND THEN RETURN NULL; END IF;
    PERFORM 1 FROM app.ingest_file_slot_guard WHERE singleton FOR UPDATE;
    SELECT account_limit,global_limit INTO configured_account_limit,configured_global_limit
      FROM app.ingest_file_slot_limits WHERE singleton FOR SHARE;
    -- Ownership/status is conservative: expiry and cancellation never make an in-flight slot stale.
    DELETE FROM app.ingest_file_slots slot WHERE NOT EXISTS (
        SELECT 1 FROM app.jobs job WHERE job.id=slot.job_id AND job.account_id=slot.account_id
          AND job.status='running' AND job.worker_id=slot.worker_id AND job.lease_token=slot.lease_token
    );
    IF (SELECT count(*) FROM app.ingest_file_slots) >= configured_global_limit OR
       (SELECT count(*) FROM app.ingest_file_slots WHERE account_id=target_account) >= configured_account_limit THEN
        RETURN NULL;
    END IF;
    new_token := gen_random_uuid();
    INSERT INTO app.ingest_file_slots(slot_token,account_id,source_id,job_id,worker_id,lease_token)
    VALUES(new_token,target_account,target_source,target_job_id,claiming_worker,current_lease_token);
    RETURN new_token;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.release_ingest_file_slot(target_job_id uuid, claiming_worker text, current_lease_token uuid, target_slot_token uuid)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
BEGIN
    IF claiming_worker='' OR current_lease_token IS NULL OR target_slot_token IS NULL THEN
        RAISE EXCEPTION 'invalid ingest file slot release arguments' USING ERRCODE='22023';
    END IF;
    DELETE FROM app.ingest_file_slots WHERE slot_token=target_slot_token AND job_id=target_job_id
      AND account_id=app.current_account_id() AND worker_id=claiming_worker AND lease_token=current_lease_token;
    RETURN FOUND;
END;
$$;
-- +goose StatementEnd

-- Called inside the same transaction as workout and source-file writes after fence_ingest_job.
-- +goose StatementBegin
CREATE FUNCTION app.record_ingest_file_manifest(target_job_id uuid, claiming_worker text, current_lease_token uuid,
    candidate_manifest jsonb)
RETURNS text
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    target_account uuid;
    target_parent uuid;
    target_source uuid;
    manifest_count integer;
    stored_count integer;
BEGIN
    IF claiming_worker='' OR current_lease_token IS NULL OR jsonb_typeof(candidate_manifest)<>'array' OR
       jsonb_array_length(candidate_manifest)>10000 THEN
        RAISE EXCEPTION 'invalid ingest file manifest' USING ERRCODE='22023';
    END IF;
    manifest_count := jsonb_array_length(candidate_manifest);
    IF EXISTS (
        SELECT 1 FROM jsonb_array_elements(candidate_manifest) value
         WHERE jsonb_typeof(value)<>'object' OR (SELECT count(*) FROM jsonb_object_keys(value))<>10 OR
               value->>'name' !~ '^HealthAutoExport-[0-9]{4}-[0-9]{2}-[0-9]{2}\.json$' OR
               value->>'action' NOT IN ('process','skip') OR
               (value->>'size') !~ '^[0-9]+$' OR (value->>'modified_sec') !~ '^-?[0-9]+$' OR
               (value->>'modified_ns') !~ '^[0-9]+$' OR (value->>'device') !~ '^[0-9]+$' OR
               (value->>'inode') !~ '^[0-9]+$' OR (value->>'ctime_sec') !~ '^-?[0-9]+$' OR
               (value->>'ctime_ns') !~ '^[0-9]+$'
    ) OR (SELECT count(DISTINCT value->>'name') FROM jsonb_array_elements(candidate_manifest) value)<>manifest_count THEN
        RAISE EXCEPTION 'invalid ingest file manifest' USING ERRCODE='22023';
    END IF;
    SELECT job.account_id,job.parent_job_id,context.source_id INTO target_account,target_parent,target_source
      FROM app.jobs job JOIN app.job_source_contexts context ON context.job_id=job.id AND context.account_id=job.account_id
     WHERE job.id=target_job_id AND job.account_id=app.current_account_id()
       AND job.kind IN ('manual_ingest_source','scheduled_ingest_source');
    IF NOT FOUND THEN RETURN NULL; END IF;
    PERFORM 1 FROM app.sources source WHERE source.id=target_source AND source.account_id=target_account FOR UPDATE;
    IF NOT FOUND THEN RETURN NULL; END IF;
    PERFORM 1 FROM app.jobs parent WHERE parent.id=target_parent AND parent.account_id=target_account
       AND parent.status='running' FOR UPDATE;
    IF NOT FOUND THEN RETURN NULL; END IF;
    PERFORM 1 FROM app.jobs job WHERE job.id=target_job_id AND job.account_id=target_account
       AND job.status='running' AND job.cancel_requested_at IS NULL AND job.worker_id=claiming_worker
       AND job.lease_token=current_lease_token AND job.lease_expires_at>=clock_timestamp() FOR UPDATE;
    IF NOT FOUND THEN RETURN NULL; END IF;
    SELECT candidate_count INTO stored_count FROM app.job_file_candidate_sets WHERE job_id=target_job_id FOR UPDATE;
    IF NOT FOUND THEN
        INSERT INTO app.job_file_candidate_sets(job_id,account_id,source_id,candidate_count)
        VALUES(target_job_id,target_account,target_source,manifest_count);
        INSERT INTO app.job_file_candidates(job_id,account_id,source_id,relative_name,export_date,observed_size,
            modified_sec,modified_ns,device,inode,ctime_sec,ctime_ns,action)
        SELECT target_job_id,target_account,target_source,candidate.name,candidate.export_date,candidate.size,
            candidate.modified_sec,candidate.modified_ns,candidate.device,candidate.inode,candidate.ctime_sec,
            candidate.ctime_ns,candidate.action
          FROM jsonb_to_recordset(candidate_manifest) AS candidate(name text,export_date date,size bigint,
            modified_sec bigint,modified_ns integer,device numeric,inode numeric,ctime_sec bigint,ctime_ns integer,action text);
        RETURN 'created';
    END IF;
    IF stored_count<>manifest_count OR EXISTS (
        (SELECT relative_name,export_date,observed_size,modified_sec,modified_ns,device,inode,ctime_sec,ctime_ns,action
           FROM app.job_file_candidates WHERE job_id=target_job_id
         EXCEPT
         SELECT candidate.name,candidate.export_date,candidate.size,candidate.modified_sec,candidate.modified_ns,
                candidate.device,candidate.inode,candidate.ctime_sec,candidate.ctime_ns,candidate.action
           FROM jsonb_to_recordset(candidate_manifest) AS candidate(name text,export_date date,size bigint,
             modified_sec bigint,modified_ns integer,device numeric,inode numeric,ctime_sec bigint,ctime_ns integer,action text))
        UNION ALL
        (SELECT candidate.name,candidate.export_date,candidate.size,candidate.modified_sec,candidate.modified_ns,
                candidate.device,candidate.inode,candidate.ctime_sec,candidate.ctime_ns,candidate.action
           FROM jsonb_to_recordset(candidate_manifest) AS candidate(name text,export_date date,size bigint,
             modified_sec bigint,modified_ns integer,device numeric,inode numeric,ctime_sec bigint,ctime_ns integer,action text)
         EXCEPT
         SELECT relative_name,export_date,observed_size,modified_sec,modified_ns,device,inode,ctime_sec,ctime_ns,action
           FROM app.job_file_candidates WHERE job_id=target_job_id)
    ) THEN
        RETURN 'mismatch';
    END IF;
    RETURN 'matched';
END;
$$;
-- +goose StatementEnd

-- Called inside the same transaction as workout and source-file writes after fence_ingest_job.
-- +goose StatementBegin
CREATE FUNCTION app.record_successful_source_object(target_job_id uuid, claiming_worker text, current_lease_token uuid,
    object_name text, object_export_date date, object_size bigint, object_modified_at timestamptz,
    object_identity text, object_checksum bytea)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    target_account uuid;
    target_source uuid;
    first_seen boolean;
BEGIN
    IF claiming_worker='' OR current_lease_token IS NULL OR object_name='' OR object_size < 0 OR
       length(object_identity) NOT BETWEEN 1 AND 512 OR octet_length(object_checksum) <> 32 THEN
        RAISE EXCEPTION 'invalid source object arguments' USING ERRCODE='22023';
    END IF;
    SELECT capability.account_id,capability.source_id INTO target_account,target_source
      FROM app.ingest_write_capabilities capability
     WHERE capability.backend_pid=pg_backend_pid() AND capability.transaction_id=txid_current()
       AND capability.job_id=target_job_id AND capability.worker_id=claiming_worker
       AND capability.lease_token=current_lease_token AND capability.account_id=app.current_account_id();
    IF NOT FOUND THEN RETURN false; END IF;
    -- The ingest fence already holds source, parent, then child. Serialize freshness before object upserts.
    PERFORM 1 FROM app.source_sync_state WHERE account_id=target_account AND source_id=target_source FOR UPDATE;
    IF NOT FOUND THEN RETURN false; END IF;
    SELECT NOT EXISTS (SELECT 1 FROM app.source_objects
        WHERE account_id=target_account AND source_id=target_source AND relative_name=object_name) INTO first_seen;
    INSERT INTO app.source_objects(account_id,source_id,relative_name,export_date,observed_size,observed_modified_at,
        observed_identity,successful_checksum,successful_observed_at,successful_job_id)
    VALUES(target_account,target_source,object_name,object_export_date,object_size,object_modified_at,object_identity,
        object_checksum,transaction_timestamp(),target_job_id)
    ON CONFLICT(account_id,source_id,relative_name) DO UPDATE SET export_date=EXCLUDED.export_date,
        observed_size=EXCLUDED.observed_size,observed_modified_at=EXCLUDED.observed_modified_at,
        observed_identity=EXCLUDED.observed_identity,successful_checksum=EXCLUDED.successful_checksum,
        successful_observed_at=EXCLUDED.successful_observed_at,successful_job_id=EXCLUDED.successful_job_id,
        updated_at=transaction_timestamp();
    IF first_seen THEN
        INSERT INTO app.source_sync_state(account_id,source_id,last_new_export_discovered_at,last_new_export_date,stale_since)
        VALUES(target_account,target_source,transaction_timestamp(),object_export_date,NULL)
        ON CONFLICT(account_id,source_id) DO UPDATE SET
            last_new_export_discovered_at=GREATEST(app.source_sync_state.last_new_export_discovered_at,transaction_timestamp()),
            last_new_export_date=CASE
                WHEN app.source_sync_state.last_new_export_date IS NULL THEN EXCLUDED.last_new_export_date
                WHEN EXCLUDED.last_new_export_date IS NULL THEN app.source_sync_state.last_new_export_date
                ELSE GREATEST(app.source_sync_state.last_new_export_date,EXCLUDED.last_new_export_date) END,
            stale_since=NULL;
        UPDATE app.notifications SET state='resolved',resolved_at=transaction_timestamp(),remind_at=NULL,updated_at=transaction_timestamp()
         WHERE account_id=target_account AND type='source-stale' AND condition_key='source:'||target_source::text
           AND state IN ('unresolved','remind');
    END IF;
    RETURN true;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.valid_job_safe_fields(value jsonb)
RETURNS boolean
LANGUAGE sql
IMMUTABLE
SET search_path = pg_catalog
AS $$
    SELECT jsonb_typeof(value) = 'object'
       AND (SELECT count(*) FROM jsonb_object_keys(value)) <= 8
       AND octet_length(value::text) <= 2048
       AND NOT EXISTS (
           SELECT 1 FROM jsonb_each(value) field
            WHERE field.key NOT IN ('sourceId','count','current','total','attempt','operation','reason')
               OR (field.key = 'sourceId' AND (jsonb_typeof(field.value) <> 'string' OR field.value #>> '{}' !~ '^[0-9A-F]{32}$'))
               OR (field.key IN ('count','current','total','attempt') AND
                   (jsonb_typeof(field.value) <> 'number' OR (field.value #>> '{}') !~ '^[0-9]+$'))
               OR (field.key = 'operation' AND
                   (jsonb_typeof(field.value) <> 'string' OR field.value #>> '{}' NOT IN ('discover','import','persist','finalize')))
               OR (field.key = 'reason' AND
                   (jsonb_typeof(field.value) <> 'string' OR field.value #>> '{}' NOT IN
                    ('cancelled','source-unavailable','invalid-data','read-failed','write-failed')))
       )
$$;
-- +goose StatementEnd

ALTER TABLE app.job_events ADD CONSTRAINT job_events_safe_fields CHECK (app.valid_job_safe_fields(fields));
ALTER TABLE app.job_logs ADD CONSTRAINT job_logs_safe_fields CHECK (app.valid_job_safe_fields(fields));

-- +goose StatementBegin
CREATE FUNCTION app.record_ingest_progress(target_job_id uuid, claiming_worker text, current_lease_token uuid,
    new_files_discovered bigint, new_files_skipped bigint, new_files_succeeded bigint, new_files_failed bigint,
    new_workouts_created bigint, new_workouts_updated bigint, new_workouts_unchanged bigint, new_workouts_rejected bigint)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    target_account uuid;
    target_parent uuid;
    target_source uuid;
    old_progress app.job_progress%ROWTYPE;
BEGIN
    IF claiming_worker = '' OR current_lease_token IS NULL OR
       LEAST(new_files_discovered,new_files_skipped,new_files_succeeded,new_files_failed,new_workouts_created,
             new_workouts_updated,new_workouts_unchanged,new_workouts_rejected) < 0 OR
       new_files_skipped + new_files_succeeded + new_files_failed > new_files_discovered THEN
        RAISE EXCEPTION 'invalid progress arguments' USING ERRCODE = '22023';
    END IF;
    SELECT job.account_id,job.parent_job_id,context.source_id INTO target_account,target_parent,target_source
      FROM app.jobs job JOIN app.job_source_contexts context
        ON context.job_id=job.id AND context.account_id=job.account_id
     WHERE job.id=target_job_id AND job.account_id=app.current_account_id()
       AND job.kind IN ('manual_ingest_source','scheduled_ingest_source');
    IF NOT FOUND THEN RETURN false; END IF;
    PERFORM 1 FROM app.sources source WHERE source.id=target_source AND source.account_id=target_account FOR UPDATE;
    IF NOT FOUND THEN RETURN false; END IF;
    PERFORM 1 FROM app.jobs parent WHERE parent.id=target_parent AND parent.account_id=target_account
       AND parent.kind IN ('manual_ingest','scheduled_ingest') FOR UPDATE;
    IF NOT FOUND THEN RETURN false; END IF;
    PERFORM 1 FROM app.jobs job WHERE job.id=target_job_id AND job.account_id=target_account
       AND job.parent_job_id=target_parent AND job.kind IN ('manual_ingest_source','scheduled_ingest_source')
       AND job.status='running' AND job.cancel_requested_at IS NULL AND job.worker_id=claiming_worker
       AND job.lease_token=current_lease_token AND job.lease_expires_at >= clock_timestamp() FOR UPDATE;
    IF NOT FOUND THEN RETURN false; END IF;
    SELECT * INTO old_progress FROM app.job_progress WHERE job_id=target_job_id AND account_id=target_account FOR UPDATE;
    IF FOUND AND (new_files_discovered < old_progress.files_discovered OR new_files_skipped < old_progress.files_skipped OR
       new_files_succeeded < old_progress.files_succeeded OR new_files_failed < old_progress.files_failed OR
       new_workouts_created < old_progress.workouts_created OR new_workouts_updated < old_progress.workouts_updated OR
       new_workouts_unchanged < old_progress.workouts_unchanged OR new_workouts_rejected < old_progress.workouts_rejected) THEN
        RETURN false;
    END IF;
    INSERT INTO app.job_progress(job_id,account_id,files_discovered,files_skipped,files_succeeded,files_failed,
        workouts_created,workouts_updated,workouts_unchanged,workouts_rejected)
    VALUES(target_job_id,target_account,new_files_discovered,new_files_skipped,new_files_succeeded,new_files_failed,
        new_workouts_created,new_workouts_updated,new_workouts_unchanged,new_workouts_rejected)
    ON CONFLICT (job_id) DO UPDATE SET files_discovered=EXCLUDED.files_discovered,files_skipped=EXCLUDED.files_skipped,
        files_succeeded=EXCLUDED.files_succeeded,files_failed=EXCLUDED.files_failed,workouts_created=EXCLUDED.workouts_created,
        workouts_updated=EXCLUDED.workouts_updated,workouts_unchanged=EXCLUDED.workouts_unchanged,
        workouts_rejected=EXCLUDED.workouts_rejected,updated_at=transaction_timestamp()
      WHERE app.job_progress.account_id=target_account;
    UPDATE app.jobs SET progress_current=new_files_skipped+new_files_succeeded+new_files_failed,progress_total=new_files_discovered
     WHERE id=target_job_id AND account_id=target_account;
    INSERT INTO app.job_progress(job_id,account_id,files_discovered,files_skipped,files_succeeded,files_failed,
        workouts_created,workouts_updated,workouts_unchanged,workouts_rejected)
    SELECT target_parent,target_account,sum(files_discovered),sum(files_skipped),sum(files_succeeded),sum(files_failed),
        sum(workouts_created),sum(workouts_updated),sum(workouts_unchanged),sum(workouts_rejected)
      FROM app.job_progress progress JOIN app.jobs child ON child.id=progress.job_id AND child.account_id=progress.account_id
     WHERE child.parent_job_id=target_parent AND child.account_id=target_account
    ON CONFLICT (job_id) DO UPDATE SET files_discovered=EXCLUDED.files_discovered,files_skipped=EXCLUDED.files_skipped,
        files_succeeded=EXCLUDED.files_succeeded,files_failed=EXCLUDED.files_failed,workouts_created=EXCLUDED.workouts_created,
        workouts_updated=EXCLUDED.workouts_updated,workouts_unchanged=EXCLUDED.workouts_unchanged,
        workouts_rejected=EXCLUDED.workouts_rejected,updated_at=transaction_timestamp();
    PERFORM set_config('app.job_transition','derive_parent',true);
    UPDATE app.jobs parent SET progress_current=progress.files_skipped+progress.files_succeeded+progress.files_failed,
        progress_total=progress.files_discovered FROM app.job_progress progress
     WHERE parent.id=target_parent AND parent.account_id=target_account AND progress.job_id=parent.id;
    RETURN true;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.record_job_event(target_job_id uuid, claiming_worker text, current_lease_token uuid,
    event_code text, event_fields jsonb)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    target_account uuid;
    target_parent uuid;
    target_source uuid;
    event_severity text;
    event_message text;
BEGIN
    SELECT severity,message INTO event_severity,event_message FROM (VALUES
        ('ingest-started','info','Ingest started.'),
        ('ingest-progress','info','Ingest progress was recorded.'),
        ('ingest-warning','warning','Ingest completed with warnings.'),
        ('ingest-failed','error','Ingest failed.')
    ) allowed(code,severity,message) WHERE code=event_code;
    IF NOT FOUND OR NOT app.valid_job_safe_fields(event_fields) THEN
        RAISE EXCEPTION 'invalid safe event' USING ERRCODE = '22023';
    END IF;
    SELECT job.account_id,job.parent_job_id,context.source_id INTO target_account,target_parent,target_source
      FROM app.jobs job JOIN app.job_source_contexts context ON context.job_id=job.id AND context.account_id=job.account_id
     WHERE job.id=target_job_id AND job.account_id=app.current_account_id()
       AND job.kind IN ('manual_ingest_source','scheduled_ingest_source');
    IF NOT FOUND THEN RETURN false; END IF;
    PERFORM 1 FROM app.sources source WHERE source.id=target_source AND source.account_id=target_account FOR UPDATE;
    IF NOT FOUND THEN RETURN false; END IF;
    PERFORM 1 FROM app.jobs parent WHERE parent.id=target_parent AND parent.account_id=target_account
       AND parent.kind IN ('manual_ingest','scheduled_ingest') FOR UPDATE;
    IF NOT FOUND THEN RETURN false; END IF;
    PERFORM 1 FROM app.jobs job WHERE job.id=target_job_id AND job.account_id=target_account
       AND job.parent_job_id=target_parent AND job.status='running' AND job.cancel_requested_at IS NULL
       AND job.worker_id=claiming_worker AND job.lease_token=current_lease_token
       AND job.lease_expires_at >= clock_timestamp() FOR UPDATE;
    IF NOT FOUND THEN RETURN false; END IF;
    IF (SELECT count(*) FROM app.job_events WHERE job_id=target_job_id AND account_id=target_account) >= 1000 THEN RETURN false; END IF;
    INSERT INTO app.job_events(account_id,job_id,severity,code,safe_message,fields)
    VALUES(target_account,target_job_id,event_severity,event_code,event_message,event_fields);
    RETURN true;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.record_job_log(target_job_id uuid, claiming_worker text, current_lease_token uuid,
    log_code text, log_fields jsonb)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    target_account uuid;
    target_parent uuid;
    target_source uuid;
    log_severity text;
    log_message text;
BEGIN
    SELECT severity,message INTO log_severity,log_message FROM (VALUES
        ('ingest-started','info','Ingest started.'),
        ('ingest-progress','debug','Ingest progress was recorded.'),
        ('ingest-completed','info','Ingest completed.'),
        ('ingest-failed','error','Ingest failed.')
    ) allowed(code,severity,message) WHERE code=log_code;
    IF NOT FOUND OR NOT app.valid_job_safe_fields(log_fields) THEN
        RAISE EXCEPTION 'invalid redacted log' USING ERRCODE = '22023';
    END IF;
    SELECT job.account_id,job.parent_job_id,context.source_id INTO target_account,target_parent,target_source
      FROM app.jobs job JOIN app.job_source_contexts context ON context.job_id=job.id AND context.account_id=job.account_id
     WHERE job.id=target_job_id AND job.account_id=app.current_account_id()
       AND job.kind IN ('manual_ingest_source','scheduled_ingest_source');
    IF NOT FOUND THEN RETURN false; END IF;
    PERFORM 1 FROM app.sources source WHERE source.id=target_source AND source.account_id=target_account FOR UPDATE;
    IF NOT FOUND THEN RETURN false; END IF;
    PERFORM 1 FROM app.jobs parent WHERE parent.id=target_parent AND parent.account_id=target_account
       AND parent.kind IN ('manual_ingest','scheduled_ingest') FOR UPDATE;
    IF NOT FOUND THEN RETURN false; END IF;
    PERFORM 1 FROM app.jobs job WHERE job.id=target_job_id AND job.account_id=target_account
       AND job.parent_job_id=target_parent AND job.status='running' AND job.cancel_requested_at IS NULL
       AND job.worker_id=claiming_worker AND job.lease_token=current_lease_token
       AND job.lease_expires_at >= clock_timestamp() FOR UPDATE;
    IF NOT FOUND THEN RETURN false; END IF;
    IF (SELECT count(*) FROM app.job_logs WHERE job_id=target_job_id AND account_id=target_account) >= 2000 THEN RETURN false; END IF;
    INSERT INTO app.job_logs(account_id,job_id,severity,code,redacted_message,fields)
    VALUES(target_account,target_job_id,log_severity,log_code,log_message,log_fields);
    RETURN true;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.request_owned_job_cancellation(target_job_id uuid, requester_id uuid)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
BEGIN
    IF requester_id IS NULL OR NOT EXISTS (
        SELECT 1 FROM app.users account_user
         WHERE account_user.principal_id=requester_id AND account_user.account_id=app.current_account_id()
    ) THEN
        RETURN false;
    END IF;
    RETURN app.request_job_cancellation(target_job_id,requester_id);
END;
$$;
-- +goose StatementEnd

ALTER TABLE app.job_source_contexts ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.job_source_contexts FORCE ROW LEVEL SECURITY;
CREATE POLICY job_source_contexts_account_policy ON app.job_source_contexts USING (account_id=app.current_account_id()) WITH CHECK (account_id=app.current_account_id());
ALTER TABLE app.job_progress ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.job_progress FORCE ROW LEVEL SECURITY;
CREATE POLICY job_progress_account_policy ON app.job_progress USING (account_id=app.current_account_id()) WITH CHECK (account_id=app.current_account_id());
ALTER TABLE app.source_objects ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.source_objects FORCE ROW LEVEL SECURITY;
CREATE POLICY source_objects_account_policy ON app.source_objects USING (account_id=app.current_account_id()) WITH CHECK (account_id=app.current_account_id());
ALTER TABLE app.ingest_file_slot_guard ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.ingest_file_slot_guard FORCE ROW LEVEL SECURITY;
CREATE POLICY ingest_file_slot_guard_owner_policy ON app.ingest_file_slot_guard TO workouts_security_owner USING (true) WITH CHECK (true);
ALTER TABLE app.ingest_file_slot_limits ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.ingest_file_slot_limits FORCE ROW LEVEL SECURITY;
CREATE POLICY ingest_file_slot_limits_owner_policy ON app.ingest_file_slot_limits TO workouts_security_owner USING (true) WITH CHECK (true);
ALTER TABLE app.ingest_file_slots ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.ingest_file_slots FORCE ROW LEVEL SECURITY;
CREATE POLICY ingest_file_slots_owner_policy ON app.ingest_file_slots TO workouts_security_owner USING (true) WITH CHECK (true);
ALTER TABLE app.job_file_candidate_sets ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.job_file_candidate_sets FORCE ROW LEVEL SECURITY;
CREATE POLICY job_file_candidate_sets_owner_policy ON app.job_file_candidate_sets TO workouts_security_owner USING (true) WITH CHECK (true);
ALTER TABLE app.job_file_candidates ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.job_file_candidates FORCE ROW LEVEL SECURITY;
CREATE POLICY job_file_candidates_owner_policy ON app.job_file_candidates TO workouts_security_owner USING (true) WITH CHECK (true);
ALTER TABLE app.job_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.job_events FORCE ROW LEVEL SECURITY;
CREATE POLICY job_events_account_policy ON app.job_events USING (account_id=app.current_account_id()) WITH CHECK (account_id=app.current_account_id());
ALTER TABLE app.job_logs ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.job_logs FORCE ROW LEVEL SECURITY;
CREATE POLICY job_logs_account_policy ON app.job_logs USING (account_id=app.current_account_id()) WITH CHECK (account_id=app.current_account_id());
ALTER TABLE app.notifications ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.notifications FORCE ROW LEVEL SECURITY;
CREATE POLICY notifications_account_policy ON app.notifications USING (account_id=app.current_account_id()) WITH CHECK (account_id=app.current_account_id());
ALTER TABLE app.source_sync_state ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.source_sync_state FORCE ROW LEVEL SECURITY;
CREATE POLICY source_sync_state_account_policy ON app.source_sync_state USING (account_id=app.current_account_id()) WITH CHECK (account_id=app.current_account_id());
CREATE POLICY sources_scheduler_owner_policy ON app.sources TO workouts_security_owner USING (true);
ALTER TABLE app.auto_sync_policy ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.auto_sync_policy FORCE ROW LEVEL SECURITY;
CREATE POLICY auto_sync_policy_owner_policy ON app.auto_sync_policy TO workouts_security_owner USING (true) WITH CHECK (true);
ALTER TABLE app.account_sync_schedules ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.account_sync_schedules FORCE ROW LEVEL SECURITY;
CREATE POLICY account_sync_schedules_owner_policy ON app.account_sync_schedules TO workouts_security_owner USING (true) WITH CHECK (true);

REVOKE ALL ON app.job_source_contexts,app.job_progress,app.source_objects,app.ingest_file_slot_guard,
    app.ingest_file_slot_limits,app.ingest_file_slots,app.job_file_candidate_sets,app.job_file_candidates,
    app.job_events,app.job_logs,app.notifications,app.source_sync_state,app.auto_sync_policy,app.account_sync_schedules
    FROM PUBLIC,workouts_api,workouts_worker;
GRANT SELECT ON app.job_source_contexts,app.job_progress TO workouts_api;
GRANT SELECT(id,account_id,job_id,severity,code,safe_message,fields,created_at) ON app.job_events TO workouts_api;
GRANT SELECT(id,account_id,type,severity,state,subject_type,subject_id,job_id,source_id,title,message,created_at,updated_at,resolved_at,remind_at) ON app.notifications TO workouts_api;
GRANT SELECT(account_id,source_id,last_sync_started_at,last_sync_succeeded_at,last_new_export_discovered_at,last_new_export_date,stale_since) ON app.source_sync_state TO workouts_api;
GRANT INSERT(job_id,account_id,source_id,source_generation,display_name,source_type) ON app.job_source_contexts TO workouts_api;
GRANT INSERT(job_id,account_id) ON app.job_progress TO workouts_api;
GRANT CREATE ON SCHEMA app TO workouts_security_owner;
-- Reassert inherited contracts so upgrading an old schema converges to the same ownership as a fresh schema.
ALTER FUNCTION app.claim_next_worker_job_internal(text,uuid,interval,boolean) OWNER TO workouts_security_owner;
ALTER FUNCTION app.claim_next_worker_job(text,uuid,interval) OWNER TO workouts_security_owner;
ALTER FUNCTION app.claim_next_source_connection_check(text,uuid,interval) OWNER TO workouts_security_owner;
ALTER FUNCTION app.fence_ingest_job(uuid,text,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.clear_ingest_write_capability() OWNER TO workouts_security_owner;
ALTER FUNCTION app.finish_job(uuid,text,uuid,text,text,text) OWNER TO workouts_security_owner;
ALTER FUNCTION app.recover_expired_job(uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.request_job_cancellation(uuid,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.delete_source(uuid,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.read_worker_job_log_context(uuid,text,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.record_ingest_progress(uuid,text,uuid,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint) OWNER TO workouts_security_owner;
ALTER FUNCTION app.record_job_event(uuid,text,uuid,text,jsonb) OWNER TO workouts_security_owner;
ALTER FUNCTION app.record_job_log(uuid,text,uuid,text,jsonb) OWNER TO workouts_security_owner;
ALTER FUNCTION app.request_owned_job_cancellation(uuid,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.claim_next_worker_job(text,uuid,interval,integer) OWNER TO workouts_security_owner;
ALTER FUNCTION app.configure_ingest_file_slot_limits(integer,integer) OWNER TO workouts_security_owner;
ALTER FUNCTION app.acquire_ingest_file_slot(uuid,text,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.release_ingest_file_slot(uuid,text,uuid,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.record_ingest_file_manifest(uuid,text,uuid,jsonb) OWNER TO workouts_security_owner;
ALTER FUNCTION app.record_successful_source_object(uuid,text,uuid,text,date,bigint,timestamptz,text,bytea) OWNER TO workouts_security_owner;
ALTER FUNCTION app.track_source_sync_job() OWNER TO workouts_security_owner;
ALTER FUNCTION app.create_source_sync_state() OWNER TO workouts_security_owner;
ALTER FUNCTION app.notify_terminal_ingest_parent() OWNER TO workouts_security_owner;
ALTER FUNCTION app.evaluate_source_staleness(uuid,integer,timestamptz) OWNER TO workouts_security_owner;
ALTER FUNCTION app.dismiss_owned_notification(uuid,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.read_owned_job_files(uuid,integer,integer) OWNER TO workouts_security_owner;
ALTER FUNCTION app.read_owned_job_logs(uuid,integer,integer) OWNER TO workouts_security_owner;
ALTER FUNCTION app.count_owned_job_rows(uuid,text) OWNER TO workouts_security_owner;
ALTER FUNCTION app.configure_auto_sync_policy(interval,integer) OWNER TO workouts_security_owner;
ALTER FUNCTION app.claim_due_sync_account(text,uuid,interval,integer) OWNER TO workouts_security_owner;
ALTER FUNCTION app.read_leased_sync_sources(uuid,text,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.enqueue_leased_scheduled_ingest(uuid,text,uuid,uuid,bytea,jsonb) OWNER TO workouts_security_owner;
ALTER FUNCTION app.finish_sync_account(uuid,text,uuid,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.release_sync_account(uuid,text,uuid,interval) OWNER TO workouts_security_owner;
ALTER FUNCTION app.read_owned_sync_schedule() OWNER TO workouts_security_owner;
ALTER FUNCTION app.create_legacy_ingest_read_models() OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
GRANT SELECT,INSERT,UPDATE ON app.job_progress TO workouts_security_owner;
GRANT SELECT ON app.jobs,app.sources,app.preferences,app.source_files,app.job_config_snapshots TO workouts_security_owner;
GRANT SELECT,INSERT ON app.job_source_contexts TO workouts_security_owner;
GRANT SELECT ON app.job_events,app.job_logs TO workouts_security_owner;
GRANT INSERT ON app.jobs,app.job_config_snapshots TO workouts_security_owner;
GRANT SELECT,INSERT,UPDATE,DELETE ON app.ingest_file_slot_guard,app.ingest_file_slot_limits,app.ingest_file_slots TO workouts_security_owner;
GRANT SELECT,INSERT,UPDATE ON app.job_file_candidate_sets,app.job_file_candidates TO workouts_security_owner;
GRANT SELECT,INSERT,UPDATE ON app.source_objects TO workouts_security_owner;
GRANT SELECT,INSERT,UPDATE ON app.source_sync_state,app.notifications TO workouts_security_owner;
GRANT INSERT ON app.job_events,app.job_logs TO workouts_security_owner;
GRANT SELECT,UPDATE ON app.auto_sync_policy TO workouts_security_owner;
GRANT SELECT,INSERT,UPDATE ON app.account_sync_schedules TO workouts_security_owner;
GRANT UPDATE(state) ON app.accounts TO workouts_security_owner;
GRANT USAGE,SELECT ON app.job_events_id_seq,app.job_logs_id_seq TO workouts_security_owner;
GRANT EXECUTE ON FUNCTION app.valid_job_safe_fields(jsonb) TO workouts_security_owner;
REVOKE ALL ON FUNCTION app.record_ingest_progress(uuid,text,uuid,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint),
    app.record_job_event(uuid,text,uuid,text,jsonb),app.record_job_log(uuid,text,uuid,text,jsonb),
    app.request_owned_job_cancellation(uuid,uuid),app.claim_next_worker_job(text,uuid,interval,integer),
    app.configure_ingest_file_slot_limits(integer,integer),app.acquire_ingest_file_slot(uuid,text,uuid),
    app.release_ingest_file_slot(uuid,text,uuid,uuid),
    app.record_ingest_file_manifest(uuid,text,uuid,jsonb),
    app.record_successful_source_object(uuid,text,uuid,text,date,bigint,timestamptz,text,bytea)
    FROM PUBLIC,workouts_api,workouts_worker;
REVOKE ALL ON FUNCTION app.track_source_sync_job(),app.create_source_sync_state(),app.notify_terminal_ingest_parent(),
    app.evaluate_source_staleness(uuid,integer,timestamptz),app.dismiss_owned_notification(uuid,uuid)
    FROM PUBLIC,workouts_api,workouts_worker;
REVOKE ALL ON FUNCTION app.read_owned_job_files(uuid,integer,integer),app.read_owned_job_logs(uuid,integer,integer)
    FROM PUBLIC,workouts_api,workouts_worker;
REVOKE ALL ON FUNCTION app.count_owned_job_rows(uuid,text) FROM PUBLIC,workouts_api,workouts_worker;
REVOKE ALL ON FUNCTION app.create_legacy_ingest_read_models() FROM PUBLIC,workouts_api,workouts_worker;
REVOKE ALL ON FUNCTION app.configure_auto_sync_policy(interval,integer),app.claim_due_sync_account(text,uuid,interval,integer),
    app.read_leased_sync_sources(uuid,text,uuid),app.enqueue_leased_scheduled_ingest(uuid,text,uuid,uuid,bytea,jsonb),
    app.finish_sync_account(uuid,text,uuid,uuid),app.release_sync_account(uuid,text,uuid,interval),app.read_owned_sync_schedule()
    FROM PUBLIC,workouts_api,workouts_worker;
-- Keep the schema-5 API readiness and request surface intact for a rolling deployment. The legacy
-- cancellation function remains tenant-scoped; workers lose it and are fenced by the claim blocker.
REVOKE EXECUTE ON FUNCTION app.request_job_cancellation(uuid,uuid) FROM workouts_worker;
GRANT EXECUTE ON FUNCTION app.record_ingest_progress(uuid,text,uuid,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint),
    app.record_job_event(uuid,text,uuid,text,jsonb),app.record_job_log(uuid,text,uuid,text,jsonb) TO workouts_worker;
GRANT EXECUTE ON FUNCTION app.claim_next_worker_job(text,uuid,interval,integer),
    app.configure_ingest_file_slot_limits(integer,integer),app.acquire_ingest_file_slot(uuid,text,uuid),
    app.release_ingest_file_slot(uuid,text,uuid,uuid),
    app.record_ingest_file_manifest(uuid,text,uuid,jsonb),
    app.record_successful_source_object(uuid,text,uuid,text,date,bigint,timestamptz,text,bytea) TO workouts_worker;
GRANT EXECUTE ON FUNCTION app.configure_auto_sync_policy(interval,integer),app.claim_due_sync_account(text,uuid,interval,integer),
    app.read_leased_sync_sources(uuid,text,uuid),app.enqueue_leased_scheduled_ingest(uuid,text,uuid,uuid,bytea,jsonb),
    app.finish_sync_account(uuid,text,uuid,uuid),app.release_sync_account(uuid,text,uuid,interval) TO workouts_worker;
GRANT SELECT(source_id,relative_name,observed_identity,successful_checksum) ON app.source_objects TO workouts_worker;
GRANT SELECT(job_id,account_id,files_discovered,files_skipped,files_succeeded,files_failed,
    workouts_created,workouts_updated,workouts_unchanged,workouts_rejected) ON app.job_progress TO workouts_worker;
GRANT EXECUTE ON FUNCTION app.request_owned_job_cancellation(uuid,uuid) TO workouts_api;
GRANT EXECUTE ON FUNCTION app.evaluate_source_staleness(uuid,integer,timestamptz),
    app.dismiss_owned_notification(uuid,uuid),app.read_owned_job_files(uuid,integer,integer),
    app.read_owned_job_logs(uuid,integer,integer),app.count_owned_job_rows(uuid,text) TO workouts_api;
GRANT EXECUTE ON FUNCTION app.read_owned_sync_schedule() TO workouts_api;
GRANT USAGE ON SCHEMA app TO workouts_api,workouts_worker,workouts_security_owner;
GRANT SELECT,INSERT ON app.jobs TO workouts_api;
GRANT SELECT ON app.schema_metadata TO workouts_api;
GRANT SELECT ON public.goose_db_version TO workouts_api;
GRANT EXECUTE ON FUNCTION app.current_account_id(),app.request_job_cancellation(uuid,uuid) TO workouts_api;

-- Runtime 6 APIs retain source-file reads and legacy tenant cancellation during rollout. Schema-7 insert
-- and claim fences stop old child writes/workers, while runtime 7 readiness validates the full new contract.
UPDATE app.schema_metadata SET schema_version=6, minimum_runtime_version=1;

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM app.account_sync_schedules WHERE lease_expires_at>=clock_timestamp()) THEN
        RAISE EXCEPTION 'cannot downgrade while scheduler leases are active' USING ERRCODE='55006';
    END IF;
    IF EXISTS (SELECT 1 FROM app.ingest_file_slots) THEN
        RAISE EXCEPTION 'cannot downgrade while ingest file slots are active' USING ERRCODE='55006';
    END IF;
    IF EXISTS (SELECT 1 FROM app.jobs job JOIN app.job_source_contexts context ON context.job_id=job.id
               WHERE job.status IN ('queued','running')) THEN
        RAISE EXCEPTION 'cannot downgrade while durable ingest jobs are active' USING ERRCODE='55006';
    END IF;
END
$$;
-- +goose StatementEnd
UPDATE app.schema_metadata SET schema_version=5, minimum_runtime_version=1;
-- Remove schema-6's internal rollout marker and normalization before restoring schema 5.
ALTER TABLE app.jobs DISABLE TRIGGER jobs_state_before_write;
UPDATE app.jobs SET parameters=parameters-'legacySchema6'-'mode'-'startDate'-'endDate'
 WHERE parameters->'legacySchema6'='true'::jsonb;
ALTER TABLE app.jobs ENABLE TRIGGER jobs_state_before_write;
REVOKE UPDATE(state) ON app.accounts FROM workouts_security_owner;
REVOKE EXECUTE ON FUNCTION app.record_ingest_progress(uuid,text,uuid,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint),
    app.record_job_event(uuid,text,uuid,text,jsonb),app.record_job_log(uuid,text,uuid,text,jsonb) FROM workouts_worker;
REVOKE EXECUTE ON FUNCTION app.request_owned_job_cancellation(uuid,uuid) FROM workouts_api;
REVOKE EXECUTE ON FUNCTION app.evaluate_source_staleness(uuid,integer,timestamptz),app.dismiss_owned_notification(uuid,uuid) FROM workouts_api;
REVOKE EXECUTE ON FUNCTION app.read_owned_job_files(uuid,integer,integer),app.read_owned_job_logs(uuid,integer,integer) FROM workouts_api;
REVOKE EXECUTE ON FUNCTION app.count_owned_job_rows(uuid,text) FROM workouts_api;
REVOKE EXECUTE ON FUNCTION app.read_owned_sync_schedule() FROM workouts_api;
REVOKE SELECT(account_id,source_id,last_sync_started_at,last_sync_succeeded_at,last_new_export_discovered_at,last_new_export_date,stale_since)
    ON app.source_sync_state FROM workouts_api;
GRANT SELECT ON app.source_files TO workouts_api;
REVOKE EXECUTE ON FUNCTION app.claim_next_worker_job(text,uuid,interval,integer),
    app.configure_ingest_file_slot_limits(integer,integer),app.acquire_ingest_file_slot(uuid,text,uuid),
    app.release_ingest_file_slot(uuid,text,uuid,uuid),
    app.record_ingest_file_manifest(uuid,text,uuid,jsonb),
    app.record_successful_source_object(uuid,text,uuid,text,date,bigint,timestamptz,text,bytea) FROM workouts_worker;
REVOKE EXECUTE ON FUNCTION app.configure_auto_sync_policy(interval,integer),app.claim_due_sync_account(text,uuid,interval,integer),
    app.read_leased_sync_sources(uuid,text,uuid),app.enqueue_leased_scheduled_ingest(uuid,text,uuid,uuid,bytea,jsonb),
    app.finish_sync_account(uuid,text,uuid,uuid),app.release_sync_account(uuid,text,uuid,interval) FROM workouts_worker;
REVOKE SELECT(source_id,relative_name,observed_identity,successful_checksum) ON app.source_objects FROM workouts_worker;
REVOKE SELECT(job_id,account_id,files_discovered,files_skipped,files_succeeded,files_failed,
    workouts_created,workouts_updated,workouts_unchanged,workouts_rejected) ON app.job_progress FROM workouts_worker;
REVOKE SELECT ON app.preferences FROM workouts_security_owner;
REVOKE INSERT ON app.jobs,app.job_config_snapshots FROM workouts_security_owner;
DROP FUNCTION app.record_successful_source_object(uuid,text,uuid,text,date,bigint,timestamptz,text,bytea);
DROP FUNCTION app.read_owned_sync_schedule();
DROP FUNCTION app.release_sync_account(uuid,text,uuid,interval);
DROP FUNCTION app.finish_sync_account(uuid,text,uuid,uuid);
DROP FUNCTION app.enqueue_leased_scheduled_ingest(uuid,text,uuid,uuid,bytea,jsonb);
DROP FUNCTION app.read_leased_sync_sources(uuid,text,uuid);
DROP FUNCTION app.claim_due_sync_account(text,uuid,interval,integer);
DROP FUNCTION app.configure_auto_sync_policy(interval,integer);
DROP TABLE app.account_sync_schedules;
DROP TABLE app.auto_sync_policy;
DROP POLICY sources_scheduler_owner_policy ON app.sources;
DROP FUNCTION app.record_ingest_file_manifest(uuid,text,uuid,jsonb);
DROP FUNCTION app.release_ingest_file_slot(uuid,text,uuid,uuid);
DROP FUNCTION app.acquire_ingest_file_slot(uuid,text,uuid);
DROP FUNCTION app.configure_ingest_file_slot_limits(integer,integer);
DROP FUNCTION app.claim_next_worker_job(text,uuid,interval,integer);
-- Restore migration 6's worker claim contract exactly.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.claim_next_worker_job(claiming_worker text, new_lease_token uuid, lease_duration interval)
RETURNS TABLE(job_id uuid, account_id uuid, kind text)
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
    SELECT claimed.job_id, claimed.account_id, claimed.kind
      FROM app.claim_next_worker_job_internal(claiming_worker, new_lease_token, lease_duration, true) claimed
$$;
-- +goose StatementEnd
ALTER FUNCTION app.claim_next_worker_job(text,uuid,interval) OWNER TO workouts_security_owner;
REVOKE ALL ON FUNCTION app.claim_next_worker_job(text,uuid,interval) FROM PUBLIC,workouts_api;
GRANT EXECUTE ON FUNCTION app.claim_next_worker_job(text,uuid,interval) TO workouts_worker;
DROP FUNCTION app.request_owned_job_cancellation(uuid,uuid);
GRANT EXECUTE ON FUNCTION app.request_job_cancellation(uuid,uuid) TO workouts_api,workouts_worker;
DROP FUNCTION app.record_job_log(uuid,text,uuid,text,jsonb);
DROP FUNCTION app.record_job_event(uuid,text,uuid,text,jsonb);
DROP FUNCTION app.record_ingest_progress(uuid,text,uuid,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint);
DROP TRIGGER sources_sync_state_after_insert ON app.sources;
DROP TRIGGER jobs_terminal_notification_after_update ON app.jobs;
DROP TRIGGER jobs_source_sync_after_update ON app.jobs;
DROP FUNCTION app.dismiss_owned_notification(uuid,uuid);
DROP FUNCTION app.evaluate_source_staleness(uuid,integer,timestamptz);
DROP FUNCTION app.notify_terminal_ingest_parent();
DROP FUNCTION app.create_source_sync_state();
DROP FUNCTION app.track_source_sync_job();
DROP FUNCTION app.read_owned_job_logs(uuid,integer,integer);
DROP FUNCTION app.read_owned_job_files(uuid,integer,integer);
DROP FUNCTION app.count_owned_job_rows(uuid,text);
DROP TABLE app.source_sync_state;
DROP TABLE app.notifications;
DROP TABLE app.job_logs;
DROP TABLE app.job_events;
DROP FUNCTION app.valid_job_safe_fields(jsonb);
DROP TABLE app.job_file_candidates;
DROP TABLE app.job_file_candidate_sets;
DROP TABLE app.ingest_file_slots;
DROP TABLE app.ingest_file_slot_limits;
DROP TABLE app.ingest_file_slot_guard;
DROP TABLE app.source_objects;
DROP TRIGGER job_config_snapshots_ingest_compatibility_after_insert ON app.job_config_snapshots;
DROP FUNCTION app.create_legacy_ingest_read_models();
DROP TABLE app.job_progress;
DROP TABLE app.job_source_contexts;
DROP TRIGGER jobs_retry_lineage_before_insert ON app.jobs;
DROP FUNCTION app.enforce_ingest_retry_lineage();
DROP INDEX app.jobs_retry_of_idx;
DROP TRIGGER jobs_ingest_child_coalescing_before_insert ON app.jobs;
DROP TRIGGER jobs_ingest_child_parameters_before_insert ON app.jobs;
DROP FUNCTION app.enforce_ingest_child_parameters();
DROP FUNCTION app.valid_ingest_child_parameters(jsonb);
DROP FUNCTION app.reject_ingest_child_coalescing();
DROP FUNCTION app.reject_immutable_read_model_write();
