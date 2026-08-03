-- +goose Up
-- +goose StatementBegin
DO $$
DECLARE
    role_name text;
BEGIN
    FOREACH role_name IN ARRAY ARRAY['workouts_api', 'workouts_worker'] LOOP
        IF NOT EXISTS (
            SELECT FROM pg_roles
            WHERE rolname = role_name AND rolcanlogin AND NOT rolsuper AND NOT rolbypassrls
        ) THEN
            RAISE EXCEPTION 'required NOBYPASSRLS login role % is missing or unsafe', role_name;
        END IF;
    END LOOP;
END
$$;
-- +goose StatementEnd

REVOKE CREATE ON SCHEMA public FROM PUBLIC;
CREATE SCHEMA IF NOT EXISTS app;
REVOKE ALL ON SCHEMA app FROM PUBLIC;
ALTER DEFAULT PRIVILEGES IN SCHEMA app REVOKE ALL ON TABLES FROM PUBLIC;
ALTER DEFAULT PRIVILEGES IN SCHEMA app REVOKE ALL ON SEQUENCES FROM PUBLIC;
ALTER DEFAULT PRIVILEGES IN SCHEMA app REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC;

CREATE TABLE app.schema_metadata (
    singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),
    schema_version integer NOT NULL CHECK (schema_version >= 1),
    minimum_runtime_version integer NOT NULL CHECK (minimum_runtime_version >= 1 AND minimum_runtime_version <= schema_version)
);
INSERT INTO app.schema_metadata (schema_version, minimum_runtime_version) VALUES (1, 1);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.current_account_id()
RETURNS uuid
LANGUAGE plpgsql
STABLE
SET search_path = pg_catalog
AS $$
DECLARE
    value text;
BEGIN
    value := current_setting('app.account_id', true);
    IF value IS NULL OR value = '' THEN
        RETURN NULL;
    END IF;
    RETURN value::uuid;
EXCEPTION WHEN invalid_text_representation THEN
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE TABLE app.jobs (
    id uuid PRIMARY KEY,
    parent_job_id uuid REFERENCES app.jobs (id),
    account_id uuid,
    administrator_id uuid,
    kind text NOT NULL CHECK (kind IN (
        'source_connection_check', 'workout_deletion', 'account_deletion',
        'manual_ingest', 'manual_ingest_source', 'scheduled_ingest',
        'scheduled_ingest_source', 'osm_bootstrap', 'osm_refresh'
    )),
    priority smallint NOT NULL CHECK (priority >= 0),
    status text NOT NULL DEFAULT 'queued' CHECK (status IN (
        'queued', 'running', 'succeeded', 'partially_succeeded', 'failed', 'cancelled'
    )),
    parameters jsonb NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(parameters) = 'object'),
    coalescing_version smallint,
    coalescing_scope text,
    coalescing_key bytea CHECK (coalescing_key IS NULL OR octet_length(coalescing_key) = 32),
    attempt integer NOT NULL DEFAULT 0 CHECK (attempt >= 0),
    progress_current bigint NOT NULL DEFAULT 0 CHECK (progress_current >= 0),
    progress_total bigint CHECK (progress_total IS NULL OR progress_total >= 0),
    cancel_requested_at timestamptz,
    cancel_requested_by uuid,
    worker_id text,
    lease_token uuid,
    claimed_at timestamptz,
    heartbeat_at timestamptz,
    lease_expires_at timestamptz,
    originating_request_id text,
    originating_trace_id text,
    retry_of_job_id uuid REFERENCES app.jobs (id),
    started_at timestamptz,
    terminal_at timestamptz,
    failure_code text CHECK (failure_code IS NULL OR length(failure_code) <= 64),
    failure_summary text CHECK (failure_summary IS NULL OR length(failure_summary) <= 512),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK ((account_id IS NOT NULL)::integer + (administrator_id IS NOT NULL)::integer = 1),
    CHECK (
        (kind IN ('account_deletion', 'osm_bootstrap', 'osm_refresh') AND administrator_id IS NOT NULL) OR
        (kind NOT IN ('account_deletion', 'osm_bootstrap', 'osm_refresh') AND account_id IS NOT NULL)
    ),
    CHECK ((parent_job_id IS NULL) = (kind NOT IN ('manual_ingest_source', 'scheduled_ingest_source'))),
    CHECK (status <> 'partially_succeeded' OR kind IN ('manual_ingest', 'scheduled_ingest')),
    CHECK ((coalescing_key IS NULL) = (coalescing_version IS NULL)),
    CHECK ((coalescing_key IS NULL) = (coalescing_scope IS NULL)),
    CHECK ((cancel_requested_at IS NULL) = (cancel_requested_by IS NULL)),
	CHECK (kind NOT IN ('manual_ingest', 'scheduled_ingest') OR attempt = 0),
	CHECK (kind NOT IN ('manual_ingest', 'scheduled_ingest') OR
		(worker_id IS NULL AND lease_token IS NULL AND claimed_at IS NULL AND heartbeat_at IS NULL AND lease_expires_at IS NULL)),
    CHECK ((status = 'running' AND kind NOT IN ('manual_ingest', 'scheduled_ingest')) =
        (worker_id IS NOT NULL AND lease_token IS NOT NULL AND claimed_at IS NOT NULL
         AND heartbeat_at IS NOT NULL AND lease_expires_at IS NOT NULL)),
	CHECK (status = 'running' OR
		(worker_id IS NULL AND lease_token IS NULL AND claimed_at IS NULL AND heartbeat_at IS NULL AND lease_expires_at IS NULL)),
    CHECK ((status IN ('succeeded', 'partially_succeeded', 'failed', 'cancelled')) = (terminal_at IS NOT NULL)),
    CHECK (status NOT IN ('succeeded', 'partially_succeeded', 'failed', 'cancelled') OR
        (worker_id IS NULL AND lease_token IS NULL AND lease_expires_at IS NULL))
);

CREATE UNIQUE INDEX jobs_active_coalescing_idx
ON app.jobs (
    (CASE WHEN account_id IS NOT NULL THEN 'account' ELSE 'administrator' END),
    (COALESCE(account_id, administrator_id)), coalescing_scope, coalescing_key
)
WHERE status IN ('queued', 'running') AND coalescing_key IS NOT NULL;

CREATE INDEX jobs_claim_idx ON app.jobs (priority DESC, created_at, id)
WHERE status = 'queued' AND kind NOT IN ('manual_ingest', 'scheduled_ingest');
CREATE INDEX jobs_parent_idx ON app.jobs (parent_job_id) WHERE parent_job_id IS NOT NULL;
CREATE INDEX jobs_account_idx ON app.jobs (account_id, created_at DESC) WHERE account_id IS NOT NULL;

-- +goose StatementBegin
CREATE FUNCTION app.enforce_job_hierarchy()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog, app
AS $$
DECLARE
    parent app.jobs%ROWTYPE;
BEGIN
    IF NEW.parent_job_id IS NULL THEN
        RETURN NEW;
    END IF;
    SELECT * INTO parent FROM app.jobs WHERE id = NEW.parent_job_id;
    IF NOT FOUND OR parent.account_id IS DISTINCT FROM NEW.account_id OR NEW.administrator_id IS NOT NULL OR
       (parent.kind = 'manual_ingest' AND NEW.kind <> 'manual_ingest_source') OR
       (parent.kind = 'scheduled_ingest' AND NEW.kind <> 'scheduled_ingest_source') OR
       parent.kind NOT IN ('manual_ingest', 'scheduled_ingest') THEN
        RAISE EXCEPTION 'invalid job hierarchy' USING ERRCODE = '23514';
    END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER jobs_hierarchy_before_write
BEFORE INSERT OR UPDATE OF parent_job_id, account_id, administrator_id, kind ON app.jobs
FOR EACH ROW EXECUTE FUNCTION app.enforce_job_hierarchy();

-- +goose StatementBegin
CREATE FUNCTION app.enforce_job_write()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
DECLARE
    transition text := COALESCE(current_setting('app.job_transition', true), '');
BEGIN
	IF TG_OP = 'INSERT' THEN
		IF NEW.status <> 'queued' OR NEW.attempt <> 0 OR NEW.terminal_at IS NOT NULL OR
		   NEW.cancel_requested_at IS NOT NULL OR NEW.worker_id IS NOT NULL OR NEW.lease_token IS NOT NULL OR
		   NEW.claimed_at IS NOT NULL OR NEW.heartbeat_at IS NOT NULL OR NEW.lease_expires_at IS NOT NULL THEN
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
	IF NEW.kind IN ('manual_ingest', 'scheduled_ingest') THEN
		IF transition NOT IN ('derive_parent', 'cancel') THEN
			RAISE EXCEPTION 'parent jobs are changed only by state-machine functions' USING ERRCODE = '23514';
		END IF;
	ELSE
		IF NEW.status IS DISTINCT FROM OLD.status AND NOT (
			(transition = 'claim' AND OLD.status = 'queued' AND NEW.status = 'running') OR
			(transition = 'recover' AND OLD.status = 'running' AND NEW.status IN ('queued', 'cancelled')) OR
			(transition = 'finish' AND OLD.status = 'running' AND NEW.status IN ('succeeded', 'failed', 'cancelled')) OR
			(transition = 'cancel' AND OLD.status = 'queued' AND NEW.status = 'cancelled')
		) THEN
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
$$;
-- +goose StatementEnd

CREATE TRIGGER jobs_state_before_write
BEFORE INSERT OR UPDATE ON app.jobs
FOR EACH ROW EXECUTE FUNCTION app.enforce_job_write();

ALTER TABLE app.jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.jobs FORCE ROW LEVEL SECURITY;
CREATE POLICY jobs_account_policy ON app.jobs
    USING (account_id = app.current_account_id())
    WITH CHECK (account_id = app.current_account_id());

REVOKE ALL ON ALL TABLES IN SCHEMA app FROM PUBLIC;
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA app FROM PUBLIC;

-- +goose StatementBegin
CREATE FUNCTION app.derive_parent_status(parent_id uuid)
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
	derived_status text;
	child_count integer;
	queued_count integer;
	running_count integer;
	succeeded_count integer;
	failed_count integer;
	cancelled_count integer;
BEGIN
	SELECT count(*), count(*) FILTER (WHERE status = 'queued'), count(*) FILTER (WHERE status = 'running'),
	       count(*) FILTER (WHERE status = 'succeeded'), count(*) FILTER (WHERE status = 'failed'),
	       count(*) FILTER (WHERE status = 'cancelled')
	INTO child_count, queued_count, running_count, succeeded_count, failed_count, cancelled_count
	FROM app.jobs WHERE parent_job_id = parent_id AND account_id = app.current_account_id();
	IF child_count = 0 THEN
		RETURN;
	END IF;
	derived_status := CASE
		WHEN queued_count = child_count THEN 'queued'
		WHEN running_count > 0 OR (queued_count > 0 AND queued_count < child_count) THEN 'running'
		WHEN succeeded_count = child_count THEN 'succeeded'
		WHEN succeeded_count > 0 AND failed_count + cancelled_count > 0 THEN 'partially_succeeded'
		WHEN succeeded_count = 0 AND cancelled_count = child_count THEN 'cancelled'
		ELSE 'failed'
	END;
	PERFORM set_config('app.job_transition', 'derive_parent', true);
	UPDATE app.jobs
	SET status = derived_status,
	    started_at = CASE WHEN derived_status = 'running' THEN COALESCE(started_at, now()) ELSE started_at END,
	    terminal_at = CASE WHEN derived_status IN ('succeeded', 'partially_succeeded', 'failed', 'cancelled') THEN now() ELSE NULL END
	WHERE id = parent_id AND account_id = app.current_account_id()
	  AND kind IN ('manual_ingest', 'scheduled_ingest') AND status IS DISTINCT FROM derived_status;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.claim_job(job_id uuid, claiming_worker text, new_lease_token uuid, lease_duration interval)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
	changed boolean;
	parent_id uuid;
BEGIN
	IF claiming_worker = '' OR new_lease_token IS NULL OR lease_duration <= interval '0 seconds' THEN
		RAISE EXCEPTION 'invalid claim arguments' USING ERRCODE = '22023';
	END IF;
	PERFORM set_config('app.job_transition', 'claim', true);
	UPDATE app.jobs SET status = 'running', attempt = attempt + 1, worker_id = claiming_worker,
		lease_token = new_lease_token, claimed_at = now(), heartbeat_at = now(),
		lease_expires_at = now() + lease_duration, started_at = COALESCE(started_at, now())
	WHERE id = job_id AND account_id = app.current_account_id()
	  AND status = 'queued' AND kind NOT IN ('manual_ingest', 'scheduled_ingest')
	RETURNING parent_job_id INTO parent_id;
	changed := FOUND;
	IF changed AND parent_id IS NOT NULL THEN PERFORM app.derive_parent_status(parent_id); END IF;
	RETURN changed;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.heartbeat_job(job_id uuid, claiming_worker text, current_lease_token uuid, lease_duration interval)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
BEGIN
	IF lease_duration <= interval '0 seconds' THEN RAISE EXCEPTION 'invalid lease duration' USING ERRCODE = '22023'; END IF;
	PERFORM set_config('app.job_transition', 'heartbeat', true);
	UPDATE app.jobs SET heartbeat_at = now(), lease_expires_at = now() + lease_duration
	WHERE id = job_id AND account_id = app.current_account_id()
	  AND status = 'running' AND worker_id = claiming_worker AND lease_token = current_lease_token;
	RETURN FOUND;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.finish_job(job_id uuid, claiming_worker text, current_lease_token uuid, terminal_status text,
	failure_code_value text DEFAULT NULL, failure_summary_value text DEFAULT NULL)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE parent_id uuid;
BEGIN
	IF terminal_status NOT IN ('succeeded', 'failed', 'cancelled') THEN RAISE EXCEPTION 'invalid terminal status' USING ERRCODE = '22023'; END IF;
	PERFORM set_config('app.job_transition', 'finish', true);
	UPDATE app.jobs SET status = terminal_status, terminal_at = now(), worker_id = NULL, lease_token = NULL,
		claimed_at = NULL, heartbeat_at = NULL, lease_expires_at = NULL,
		failure_code = failure_code_value, failure_summary = failure_summary_value
	WHERE id = job_id AND account_id = app.current_account_id()
	  AND status = 'running' AND worker_id = claiming_worker AND lease_token = current_lease_token
	RETURNING parent_job_id INTO parent_id;
	IF NOT FOUND THEN RETURN false; END IF;
	IF parent_id IS NOT NULL THEN PERFORM app.derive_parent_status(parent_id); END IF;
	RETURN true;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.recover_expired_job(job_id uuid)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE parent_id uuid;
BEGIN
	PERFORM set_config('app.job_transition', 'recover', true);
	UPDATE app.jobs SET status = CASE WHEN cancel_requested_at IS NULL THEN 'queued' ELSE 'cancelled' END,
		terminal_at = CASE WHEN cancel_requested_at IS NULL THEN NULL ELSE now() END,
		worker_id = NULL, lease_token = NULL, claimed_at = NULL, heartbeat_at = NULL, lease_expires_at = NULL
	WHERE id = job_id AND account_id = app.current_account_id() AND status = 'running' AND lease_expires_at < now()
	RETURNING parent_job_id INTO parent_id;
	IF NOT FOUND THEN RETURN false; END IF;
	IF parent_id IS NOT NULL THEN PERFORM app.derive_parent_status(parent_id); END IF;
	RETURN true;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.request_job_cancellation(job_id uuid, requester_id uuid)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE target_kind text; target_status text; parent_id uuid;
BEGIN
	IF requester_id IS NULL THEN RAISE EXCEPTION 'requester is required' USING ERRCODE = '22023'; END IF;
	SELECT kind, status, parent_job_id INTO target_kind, target_status, parent_id FROM app.jobs
	WHERE id = job_id AND account_id = app.current_account_id() FOR UPDATE;
	IF NOT FOUND OR target_status IN ('succeeded', 'partially_succeeded', 'failed', 'cancelled') THEN RETURN false; END IF;
	PERFORM set_config('app.job_transition', 'cancel', true);
	IF target_kind IN ('manual_ingest', 'scheduled_ingest') THEN
		UPDATE app.jobs SET cancel_requested_at = COALESCE(cancel_requested_at, now()), cancel_requested_by = COALESCE(cancel_requested_by, requester_id)
		WHERE id = job_id AND account_id = app.current_account_id();
		UPDATE app.jobs SET status = 'cancelled', terminal_at = now(), cancel_requested_at = now(), cancel_requested_by = requester_id
		WHERE parent_job_id = job_id AND account_id = app.current_account_id() AND status = 'queued';
		UPDATE app.jobs SET cancel_requested_at = COALESCE(cancel_requested_at, now()), cancel_requested_by = COALESCE(cancel_requested_by, requester_id)
		WHERE parent_job_id = job_id AND account_id = app.current_account_id() AND status = 'running';
		PERFORM app.derive_parent_status(job_id);
	ELSE
		UPDATE app.jobs SET status = CASE WHEN status = 'queued' THEN 'cancelled' ELSE status END,
			terminal_at = CASE WHEN status = 'queued' THEN now() ELSE terminal_at END,
			cancel_requested_at = COALESCE(cancel_requested_at, now()), cancel_requested_by = COALESCE(cancel_requested_by, requester_id)
		WHERE id = job_id AND account_id = app.current_account_id() AND status IN ('queued', 'running');
		IF parent_id IS NOT NULL THEN PERFORM app.derive_parent_status(parent_id); END IF;
	END IF;
	RETURN true;
END;
$$;
-- +goose StatementEnd

REVOKE ALL ON ALL FUNCTIONS IN SCHEMA app FROM PUBLIC;

-- +goose StatementBegin
DO $$
BEGIN
	GRANT USAGE ON SCHEMA app TO workouts_api, workouts_worker;
	GRANT SELECT, INSERT ON app.jobs TO workouts_api, workouts_worker;
	GRANT SELECT ON app.schema_metadata TO workouts_api, workouts_worker;
	GRANT SELECT ON public.goose_db_version TO workouts_api, workouts_worker;
	GRANT EXECUTE ON FUNCTION app.current_account_id() TO workouts_api, workouts_worker;
	GRANT EXECUTE ON FUNCTION app.request_job_cancellation(uuid, uuid) TO workouts_api, workouts_worker;
	GRANT EXECUTE ON FUNCTION app.claim_job(uuid, text, uuid, interval) TO workouts_worker;
	GRANT EXECUTE ON FUNCTION app.heartbeat_job(uuid, text, uuid, interval) TO workouts_worker;
	GRANT EXECUTE ON FUNCTION app.finish_job(uuid, text, uuid, text, text, text) TO workouts_worker;
	GRANT EXECUTE ON FUNCTION app.recover_expired_job(uuid) TO workouts_worker;
END
$$;
-- +goose StatementEnd

-- +goose Down
DROP SCHEMA IF EXISTS app CASCADE;
