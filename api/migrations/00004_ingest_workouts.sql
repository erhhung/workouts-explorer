-- +goose Up
CREATE TABLE app.source_files (
    id uuid PRIMARY KEY,
    account_id uuid NOT NULL,
    source_id uuid NOT NULL,
    job_id uuid NOT NULL,
    relative_name text COLLATE "C" NOT NULL CHECK (
        length(relative_name) BETWEEN 1 AND 4096 AND
        relative_name !~ '^/' AND relative_name !~ '(^|/)\.\.(/|$)'
    ),
    size_bytes bigint NOT NULL CHECK (size_bytes >= 0),
    modified_at timestamptz,
    checksum_sha256 bytea CHECK (checksum_sha256 IS NULL OR octet_length(checksum_sha256) = 32),
    state text NOT NULL DEFAULT 'discovered' CHECK (state IN ('discovered', 'processing', 'succeeded', 'failed')),
    failure_code text CHECK (failure_code IS NULL OR length(failure_code) BETWEEN 1 AND 64),
    failure_summary text CHECK (failure_summary IS NULL OR length(failure_summary) BETWEEN 1 AND 512),
    processing_started_at timestamptz,
    processed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    UNIQUE (id, account_id),
    UNIQUE (id, account_id, source_id),
    UNIQUE (id, account_id, source_id, job_id),
    UNIQUE (account_id, job_id, relative_name),
    FOREIGN KEY (source_id, account_id) REFERENCES app.sources(id, account_id) ON DELETE RESTRICT,
    FOREIGN KEY (job_id, account_id) REFERENCES app.jobs(id, account_id) ON DELETE RESTRICT,
    CHECK ((state = 'failed') = (failure_code IS NOT NULL)),
    CHECK ((failure_code IS NULL) = (failure_summary IS NULL)),
    CHECK (state <> 'succeeded' OR checksum_sha256 IS NOT NULL),
    CHECK ((state IN ('processing', 'succeeded', 'failed')) = (processing_started_at IS NOT NULL)),
    CHECK ((state IN ('succeeded', 'failed')) = (processed_at IS NOT NULL)),
    CHECK (processed_at IS NULL OR processed_at >= processing_started_at)
);
CREATE INDEX source_files_source_chronology_idx
    ON app.source_files (account_id, source_id, modified_at DESC NULLS LAST, id);
CREATE INDEX source_files_job_state_idx
    ON app.source_files (account_id, job_id, state, relative_name);

CREATE TABLE app.workout_types (
    id uuid PRIMARY KEY,
    account_id uuid NOT NULL REFERENCES app.accounts(id) ON DELETE CASCADE,
    type_key text COLLATE "C" NOT NULL CHECK (octet_length(type_key) BETWEEN 1 AND 512),
    provider_label text NOT NULL CHECK (length(provider_label) BETWEEN 1 AND 4096),
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    UNIQUE (id, account_id),
    UNIQUE (account_id, type_key)
);

CREATE TABLE app.workouts (
    id uuid PRIMARY KEY,
    account_id uuid NOT NULL,
    source_id uuid NOT NULL,
    source_file_id uuid NOT NULL,
    workout_type_id uuid NOT NULL,
    provider_id text COLLATE "C" CHECK (provider_id IS NULL OR length(provider_id) BETWEEN 1 AND 4096),
    fallback_fingerprint_version text COLLATE "C" CHECK (
        fallback_fingerprint_version IS NULL OR length(fallback_fingerprint_version) BETWEEN 1 AND 128
    ),
    fallback_sha256 bytea CHECK (fallback_sha256 IS NULL OR octet_length(fallback_sha256) = 32),
    content_sha256 bytea NOT NULL CHECK (octet_length(content_sha256) = 32),
    provider_label text NOT NULL CHECK (length(provider_label) BETWEEN 1 AND 4096),
    started_at timestamptz NOT NULL,
    ended_at timestamptz NOT NULL,
    start_offset_minutes smallint CHECK (start_offset_minutes BETWEEN -1080 AND 1080),
    end_offset_minutes smallint CHECK (end_offset_minutes BETWEEN -1080 AND 1080),
    local_start_date date,
    timezone_name text CHECK (timezone_name IS NULL OR length(timezone_name) BETWEEN 1 AND 255),
    timezone_source text CHECK (timezone_source IS NULL OR length(timezone_source) BETWEEN 1 AND 64),
    provider_duration numeric NOT NULL CHECK (provider_duration >= 0 AND provider_duration <= 1000000000000000),
    is_indoor boolean,
    location text CHECK (location IS NULL OR length(location) BETWEEN 1 AND 4096),
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    UNIQUE (id, account_id),
    UNIQUE (id, account_id, source_id),
    FOREIGN KEY (source_id, account_id) REFERENCES app.sources(id, account_id) ON DELETE RESTRICT,
    FOREIGN KEY (source_file_id, account_id, source_id)
        REFERENCES app.source_files(id, account_id, source_id) ON DELETE RESTRICT,
    FOREIGN KEY (workout_type_id, account_id) REFERENCES app.workout_types(id, account_id) ON DELETE RESTRICT,
    CHECK (ended_at >= started_at),
    CHECK ((provider_id IS NOT NULL)::integer + (fallback_sha256 IS NOT NULL)::integer = 1),
    CHECK ((fallback_sha256 IS NULL) = (fallback_fingerprint_version IS NULL)),
    CHECK ((timezone_name IS NULL) = (timezone_source IS NULL))
);
CREATE UNIQUE INDEX workouts_source_provider_id_idx
    ON app.workouts (account_id, source_id, provider_id) WHERE provider_id IS NOT NULL;
CREATE UNIQUE INDEX workouts_source_fallback_idx
    ON app.workouts (account_id, source_id, fallback_fingerprint_version, fallback_sha256)
    WHERE provider_id IS NULL;
CREATE INDEX workouts_account_chronology_idx
    ON app.workouts (account_id, started_at DESC, id DESC);
CREATE INDEX workouts_account_local_date_idx
    ON app.workouts (account_id, local_start_date DESC NULLS LAST, started_at DESC, id DESC);
CREATE INDEX workouts_account_type_chronology_idx
    ON app.workouts (account_id, workout_type_id, started_at DESC, id DESC);

CREATE TABLE app.workout_aggregates (
    account_id uuid NOT NULL,
    workout_id uuid NOT NULL,
    metric text COLLATE "C" NOT NULL CHECK (metric IN (
        'active_energy_burned', 'heart_rate_average', 'speed_average', 'distance', 'elevation_up',
        'flights_climbed', 'heart_rate_minimum', 'humidity', 'intensity', 'heart_rate_maximum',
        'speed_maximum', 'speed', 'step_cadence', 'temperature', 'total_energy'
    )),
    value numeric NOT NULL CHECK (value BETWEEN -1000000000000000 AND 1000000000000000),
    unit text COLLATE "C" NOT NULL CHECK (length(unit) BETWEEN 1 AND 128),
    origin text COLLATE "C" NOT NULL CHECK (origin IN ('provider_direct', 'provider_heart_rate')),
    PRIMARY KEY (account_id, workout_id, metric),
    FOREIGN KEY (workout_id, account_id) REFERENCES app.workouts(id, account_id) ON DELETE CASCADE
);

CREATE TABLE app.workout_route_points (
    account_id uuid NOT NULL,
    workout_id uuid NOT NULL,
    sequence integer NOT NULL CHECK (sequence >= 0),
    recorded_at timestamptz NOT NULL,
    timestamp_offset_minutes smallint CHECK (timestamp_offset_minutes BETWEEN -1080 AND 1080),
    latitude double precision NOT NULL CHECK (latitude BETWEEN -90 AND 90),
    longitude double precision NOT NULL CHECK (longitude BETWEEN -180 AND 180),
    altitude double precision CHECK (altitude BETWEEN -1000000 AND 1000000),
    speed double precision CHECK (speed BETWEEN 0 AND 10000),
    course double precision CHECK (course BETWEEN 0 AND 360),
    horizontal_accuracy double precision CHECK (horizontal_accuracy BETWEEN 0 AND 1000000),
    vertical_accuracy double precision CHECK (vertical_accuracy BETWEEN 0 AND 1000000),
    speed_accuracy double precision CHECK (speed_accuracy BETWEEN 0 AND 1000000),
    course_accuracy double precision CHECK (course_accuracy BETWEEN 0 AND 360),
    PRIMARY KEY (account_id, workout_id, sequence),
    FOREIGN KEY (workout_id, account_id) REFERENCES app.workouts(id, account_id) ON DELETE CASCADE
);
CREATE INDEX workout_route_points_chronology_idx
    ON app.workout_route_points (account_id, workout_id, recorded_at, sequence);

-- A capability can only be minted by the fenced SECURITY DEFINER function. Its
-- backend/transaction tuple cannot be reused, and the deferred trigger removes
-- it before commit so committed state never contains an authorization token.
CREATE TABLE app.ingest_write_capabilities (
    backend_pid integer NOT NULL,
    transaction_id bigint NOT NULL,
    account_id uuid NOT NULL,
    source_id uuid NOT NULL,
    job_id uuid NOT NULL,
    worker_id text NOT NULL CHECK (length(worker_id) BETWEEN 1 AND 512),
    lease_token uuid NOT NULL,
    PRIMARY KEY (backend_pid, transaction_id),
    FOREIGN KEY (source_id, account_id) REFERENCES app.sources(id, account_id) ON DELETE CASCADE,
    FOREIGN KEY (job_id, account_id) REFERENCES app.jobs(id, account_id) ON DELETE CASCADE
);

-- +goose StatementBegin
CREATE FUNCTION app.valid_workout_warnings(warnings jsonb)
RETURNS boolean
LANGUAGE plpgsql
IMMUTABLE
SET search_path = pg_catalog
AS $$
DECLARE
    warning_value jsonb;
    warning_code text;
    warning_field text;
    route_point_text text;
    route_point integer;
BEGIN
    IF jsonb_typeof(warnings) <> 'array' OR jsonb_array_length(warnings) > 4096 OR
       octet_length(warnings::text) > 262144 THEN
        RETURN false;
    END IF;
    FOR warning_value IN SELECT value FROM jsonb_array_elements(warnings) LOOP
        IF jsonb_typeof(warning_value) <> 'object' OR
           NOT (warning_value ? 'code' AND warning_value ? 'field' AND warning_value ? 'route_point') OR
           (SELECT count(*) FROM jsonb_object_keys(warning_value)) <> 3 OR
           jsonb_typeof(warning_value->'code') <> 'string' OR
           jsonb_typeof(warning_value->'field') <> 'string' OR
           jsonb_typeof(warning_value->'route_point') <> 'number' THEN
            RETURN false;
        END IF;
        warning_code := warning_value->>'code';
        warning_field := warning_value->>'field';
        route_point_text := warning_value->>'route_point';
        IF route_point_text !~ '^-?[0-9]+$' OR length(route_point_text) > 6 THEN RETURN false; END IF;
        route_point := route_point_text::integer;
        IF warning_code IN ('incomplete_metric', 'unexpected_unit') THEN
            IF route_point <> -1 OR warning_field NOT IN (
                'active_energy_burned', 'heart_rate_average', 'speed_average', 'distance', 'elevation_up',
                'flights_climbed', 'heart_rate_minimum', 'humidity', 'intensity', 'heart_rate_maximum',
                'speed_maximum', 'speed', 'step_cadence', 'temperature', 'total_energy'
            ) THEN RETURN false; END IF;
        ELSIF warning_code = 'invalid_optional_route_value' THEN
            IF route_point NOT BETWEEN 0 AND 249999 OR warning_field NOT IN (
                'route_altitude', 'route_course', 'route_course_accuracy', 'route_horizontal_accuracy',
                'route_speed', 'route_speed_accuracy', 'route_vertical_accuracy'
            ) THEN RETURN false; END IF;
        ELSE
            RETURN false;
        END IF;
    END LOOP;
    RETURN true;
EXCEPTION WHEN numeric_value_out_of_range THEN
    RETURN false;
END;
$$;
-- +goose StatementEnd

CREATE TABLE app.workout_import_events (
    id uuid PRIMARY KEY,
    account_id uuid NOT NULL,
    source_id uuid NOT NULL,
    workout_id uuid NOT NULL,
    source_file_id uuid NOT NULL,
    job_id uuid NOT NULL,
    kind text COLLATE "C" NOT NULL CHECK (kind IN ('created', 'updated', 'matched_unchanged')),
    content_sha256 bytea NOT NULL CHECK (octet_length(content_sha256) = 32),
    warnings jsonb NOT NULL DEFAULT '[]'::jsonb CHECK (app.valid_workout_warnings(warnings)),
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    UNIQUE (id, account_id),
    FOREIGN KEY (source_id, account_id) REFERENCES app.sources(id, account_id) ON DELETE RESTRICT,
    FOREIGN KEY (workout_id, account_id, source_id)
        REFERENCES app.workouts(id, account_id, source_id) ON DELETE RESTRICT,
    FOREIGN KEY (source_file_id, account_id, source_id, job_id)
        REFERENCES app.source_files(id, account_id, source_id, job_id) ON DELETE RESTRICT
);
-- Import events intentionally RESTRICT workout deletion. A future deletion
-- migration must add a narrowly granted SECURITY DEFINER path that authorizes
-- event removal before workout removal; cascades cannot bypass append-only audit.
CREATE INDEX workout_import_events_workout_chronology_idx
    ON app.workout_import_events (account_id, workout_id, created_at DESC, id DESC);
CREATE INDEX workout_import_events_job_chronology_idx
    ON app.workout_import_events (account_id, job_id, created_at, id);
CREATE INDEX workout_import_events_warnings_idx
    ON app.workout_import_events USING gin (warnings jsonb_path_ops) WHERE warnings <> '[]'::jsonb;

ALTER TABLE app.source_files ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.source_files FORCE ROW LEVEL SECURITY;
CREATE POLICY source_files_account_policy ON app.source_files
    USING (account_id = app.current_account_id()) WITH CHECK (account_id = app.current_account_id());
ALTER TABLE app.workout_types ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.workout_types FORCE ROW LEVEL SECURITY;
CREATE POLICY workout_types_account_policy ON app.workout_types
    USING (account_id = app.current_account_id()) WITH CHECK (account_id = app.current_account_id());
ALTER TABLE app.workouts ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.workouts FORCE ROW LEVEL SECURITY;
CREATE POLICY workouts_account_policy ON app.workouts
    USING (account_id = app.current_account_id()) WITH CHECK (account_id = app.current_account_id());
ALTER TABLE app.workout_aggregates ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.workout_aggregates FORCE ROW LEVEL SECURITY;
CREATE POLICY workout_aggregates_account_policy ON app.workout_aggregates
    USING (account_id = app.current_account_id()) WITH CHECK (account_id = app.current_account_id());
ALTER TABLE app.workout_route_points ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.workout_route_points FORCE ROW LEVEL SECURITY;
CREATE POLICY workout_route_points_account_policy ON app.workout_route_points
    USING (account_id = app.current_account_id()) WITH CHECK (account_id = app.current_account_id());
ALTER TABLE app.workout_import_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.workout_import_events FORCE ROW LEVEL SECURITY;
CREATE POLICY workout_import_events_account_policy ON app.workout_import_events
    USING (account_id = app.current_account_id()) WITH CHECK (account_id = app.current_account_id());
ALTER TABLE app.ingest_write_capabilities ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.ingest_write_capabilities FORCE ROW LEVEL SECURITY;
CREATE POLICY ingest_write_capabilities_owner_policy ON app.ingest_write_capabilities TO workouts_security_owner
    USING (true) WITH CHECK (true);

-- The security owner cannot log in or bypass RLS. These policies expose only
-- the rows needed by its fixed cross-account claim and downgrade-guard functions.
CREATE POLICY jobs_cross_account_claim_policy ON app.jobs FOR SELECT TO workouts_security_owner USING (true);
CREATE POLICY job_config_snapshots_cross_account_guard_policy ON app.job_config_snapshots
    FOR SELECT TO workouts_security_owner USING (true);

-- +goose StatementBegin
CREATE FUNCTION app.enforce_ingest_identity()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    IF TG_OP = 'INSERT' THEN RETURN NEW; END IF;
    IF TG_TABLE_NAME = 'source_files' THEN
        IF (OLD.id, OLD.account_id, OLD.source_id, OLD.job_id, OLD.relative_name, OLD.size_bytes, OLD.modified_at, OLD.created_at)
           IS DISTINCT FROM
           (NEW.id, NEW.account_id, NEW.source_id, NEW.job_id, NEW.relative_name, NEW.size_bytes, NEW.modified_at, NEW.created_at) THEN
            RAISE EXCEPTION 'immutable source file identity changed' USING ERRCODE = '23514';
        END IF;
        IF OLD.state IN ('succeeded', 'failed') THEN
            RAISE EXCEPTION 'terminal source files are immutable' USING ERRCODE = '23514';
        END IF;
        IF NEW.state IS DISTINCT FROM OLD.state AND NOT (
            (OLD.state = 'discovered' AND NEW.state IN ('processing', 'failed')) OR
            (OLD.state = 'processing' AND NEW.state IN ('succeeded', 'failed'))
        ) THEN
            RAISE EXCEPTION 'invalid source file state transition' USING ERRCODE = '23514';
        END IF;
        IF OLD.checksum_sha256 IS NOT NULL AND NEW.checksum_sha256 IS DISTINCT FROM OLD.checksum_sha256 THEN
            RAISE EXCEPTION 'source file checksum is immutable once set' USING ERRCODE = '23514';
        END IF;
    ELSIF TG_TABLE_NAME = 'workout_types' THEN
        IF (OLD.id, OLD.account_id, OLD.type_key, OLD.created_at) IS DISTINCT FROM
           (NEW.id, NEW.account_id, NEW.type_key, NEW.created_at) THEN
            RAISE EXCEPTION 'immutable workout type identity changed' USING ERRCODE = '23514';
        END IF;
    ELSE
        IF (OLD.id, OLD.account_id, OLD.source_id, OLD.provider_id, OLD.fallback_fingerprint_version,
            OLD.fallback_sha256, OLD.created_at) IS DISTINCT FROM
           (NEW.id, NEW.account_id, NEW.source_id, NEW.provider_id, NEW.fallback_fingerprint_version,
            NEW.fallback_sha256, NEW.created_at) OR
           (OLD.provider_id IS NULL AND
            (OLD.workout_type_id, OLD.started_at) IS DISTINCT FROM (NEW.workout_type_id, NEW.started_at)) THEN
            RAISE EXCEPTION 'immutable workout provider identity changed' USING ERRCODE = '23514';
        END IF;
    END IF;
    NEW.updated_at := transaction_timestamp();
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER source_files_identity_before_write BEFORE UPDATE ON app.source_files
FOR EACH ROW EXECUTE FUNCTION app.enforce_ingest_identity();
CREATE TRIGGER workout_types_identity_before_write BEFORE UPDATE ON app.workout_types
FOR EACH ROW EXECUTE FUNCTION app.enforce_ingest_identity();
CREATE TRIGGER workouts_identity_before_write BEFORE UPDATE ON app.workouts
FOR EACH ROW EXECUTE FUNCTION app.enforce_ingest_identity();

-- +goose StatementBegin
CREATE FUNCTION app.reject_workout_import_event_mutation()
RETURNS trigger
LANGUAGE plpgsql
SET search_path = pg_catalog
AS $$
BEGIN
    RAISE EXCEPTION 'workout import events are append-only' USING ERRCODE = '23514';
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER workout_import_events_append_only
BEFORE UPDATE OR DELETE ON app.workout_import_events
FOR EACH ROW EXECUTE FUNCTION app.reject_workout_import_event_mutation();

-- +goose StatementBegin
CREATE FUNCTION app.require_ingest_write_capability()
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
    capability_exists boolean;
BEGIN
    IF TG_OP = 'DELETE' THEN
        IF TG_TABLE_NAME = 'source_files' THEN
            target_account_id := OLD.account_id; target_source_id := OLD.source_id; target_job_id := OLD.job_id;
        ELSIF TG_TABLE_NAME = 'workout_types' THEN
            target_account_id := OLD.account_id;
        ELSIF TG_TABLE_NAME = 'workouts' THEN
            target_account_id := OLD.account_id; target_source_id := OLD.source_id; target_record_id := OLD.source_file_id;
        ELSIF TG_TABLE_NAME IN ('workout_aggregates', 'workout_route_points') THEN
            target_account_id := OLD.account_id; target_record_id := OLD.workout_id;
        ELSIF TG_TABLE_NAME = 'workout_import_events' THEN
            target_account_id := OLD.account_id; target_source_id := OLD.source_id; target_job_id := OLD.job_id;
        ELSE
            RAISE EXCEPTION 'unsupported ingest capability trigger table %', TG_TABLE_NAME USING ERRCODE = '55000';
        END IF;
    ELSE
        IF TG_TABLE_NAME = 'source_files' THEN
            target_account_id := NEW.account_id; target_source_id := NEW.source_id; target_job_id := NEW.job_id;
        ELSIF TG_TABLE_NAME = 'workout_types' THEN
            target_account_id := NEW.account_id;
        ELSIF TG_TABLE_NAME = 'workouts' THEN
            target_account_id := NEW.account_id; target_source_id := NEW.source_id; target_record_id := NEW.source_file_id;
        ELSIF TG_TABLE_NAME IN ('workout_aggregates', 'workout_route_points') THEN
            target_account_id := NEW.account_id; target_record_id := NEW.workout_id;
        ELSIF TG_TABLE_NAME = 'workout_import_events' THEN
            target_account_id := NEW.account_id; target_source_id := NEW.source_id; target_job_id := NEW.job_id;
        ELSE
            RAISE EXCEPTION 'unsupported ingest capability trigger table %', TG_TABLE_NAME USING ERRCODE = '55000';
        END IF;
    END IF;

    IF TG_TABLE_NAME = 'workouts' THEN
        SELECT file.job_id INTO target_job_id FROM app.source_files file
         WHERE file.id = target_record_id AND file.account_id = target_account_id AND file.source_id = target_source_id;
    ELSIF TG_TABLE_NAME IN ('workout_aggregates', 'workout_route_points') THEN
        SELECT workout.source_id, file.job_id INTO target_source_id, target_job_id
          FROM app.workouts workout
          JOIN app.source_files file ON file.id = workout.source_file_id
             AND file.account_id = workout.account_id AND file.source_id = workout.source_id
         WHERE workout.id = target_record_id AND workout.account_id = target_account_id;
    END IF;

    SELECT EXISTS (
        SELECT 1 FROM app.ingest_write_capabilities capability
         WHERE capability.backend_pid = pg_backend_pid()
           AND capability.transaction_id = txid_current()
           AND capability.account_id = target_account_id
           AND (target_source_id IS NULL OR capability.source_id = target_source_id)
           AND (target_job_id IS NULL OR capability.job_id = target_job_id)
    ) INTO capability_exists;
    IF NOT capability_exists THEN
        RAISE EXCEPTION 'ingest domain write requires a live transaction fence' USING ERRCODE = '42501';
    END IF;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$$;
-- +goose StatementEnd

CREATE TRIGGER source_files_capability_before_write BEFORE INSERT OR UPDATE OR DELETE ON app.source_files
FOR EACH ROW EXECUTE FUNCTION app.require_ingest_write_capability();
CREATE TRIGGER workout_types_capability_before_write BEFORE INSERT OR UPDATE OR DELETE ON app.workout_types
FOR EACH ROW EXECUTE FUNCTION app.require_ingest_write_capability();
CREATE TRIGGER workouts_capability_before_write BEFORE INSERT OR UPDATE OR DELETE ON app.workouts
FOR EACH ROW EXECUTE FUNCTION app.require_ingest_write_capability();
CREATE TRIGGER workout_aggregates_capability_before_write BEFORE INSERT OR UPDATE OR DELETE ON app.workout_aggregates
FOR EACH ROW EXECUTE FUNCTION app.require_ingest_write_capability();
CREATE TRIGGER workout_route_points_capability_before_write BEFORE INSERT OR UPDATE OR DELETE ON app.workout_route_points
FOR EACH ROW EXECUTE FUNCTION app.require_ingest_write_capability();
CREATE TRIGGER workout_import_events_capability_before_write BEFORE INSERT OR UPDATE OR DELETE ON app.workout_import_events
FOR EACH ROW EXECUTE FUNCTION app.require_ingest_write_capability();

-- The initial fence requires an unexpired lease. At deferred cleanup the source,
-- parent, and child locks acquired by that fence are still held, so recovery and
-- cancellation cannot change ownership before commit even if wall clock passes
-- lease_expires_at while domain work is being committed.
-- +goose StatementBegin
CREATE FUNCTION app.clear_ingest_write_capability()
RETURNS trigger
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    snapshot_source_id uuid;
    parent_id uuid;
BEGIN
    -- The initial fence still holds source, parent, and child locks; recovery and
    -- cancellation cannot change ownership before commit after wall-clock expiry.
    IF NEW.backend_pid <> pg_backend_pid() OR NEW.transaction_id <> txid_current() THEN
        RAISE EXCEPTION 'ingest capability transaction changed before commit' USING ERRCODE = '40001';
    END IF;
    SELECT snapshot.source_id INTO snapshot_source_id
      FROM app.job_config_snapshots snapshot
     WHERE snapshot.job_id = NEW.job_id AND snapshot.account_id = NEW.account_id
       AND snapshot.source_id = NEW.source_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'ingest snapshot changed before commit' USING ERRCODE = '40001';
    END IF;

    PERFORM 1 FROM app.sources source
     WHERE source.id = NEW.source_id AND source.account_id = NEW.account_id
       AND source.deleted_at IS NULL
     FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'ingest source changed before commit' USING ERRCODE = '40001';
    END IF;

    SELECT job.parent_job_id INTO parent_id FROM app.jobs job
     WHERE job.id = NEW.job_id AND job.account_id = NEW.account_id
       AND job.kind = 'manual_ingest_source';
    IF NOT FOUND OR parent_id IS NULL THEN
        RAISE EXCEPTION 'ingest job changed before commit' USING ERRCODE = '40001';
    END IF;
    PERFORM 1 FROM app.jobs parent
     WHERE parent.id = parent_id AND parent.account_id = NEW.account_id
       AND parent.kind = 'manual_ingest' AND parent.status = 'running'
       AND parent.cancel_requested_at IS NULL
     FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'ingest parent changed before commit' USING ERRCODE = '40001';
    END IF;

    PERFORM 1 FROM app.jobs job
     WHERE job.id = NEW.job_id AND job.account_id = NEW.account_id
       AND job.kind = 'manual_ingest_source' AND job.status = 'running'
       AND job.cancel_requested_at IS NULL AND job.worker_id = NEW.worker_id
       AND job.lease_token = NEW.lease_token
     FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'ingest lease ownership changed before commit' USING ERRCODE = '40001';
    END IF;
    DELETE FROM app.ingest_write_capabilities capability
     WHERE capability.backend_pid = NEW.backend_pid
       AND capability.transaction_id = NEW.transaction_id;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd
CREATE CONSTRAINT TRIGGER ingest_write_capability_cleanup
AFTER INSERT ON app.ingest_write_capabilities
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION app.clear_ingest_write_capability();

-- +goose StatementBegin
CREATE FUNCTION app.assert_no_active_manual_ingest()
RETURNS void
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
BEGIN
    LOCK TABLE app.jobs, app.job_config_snapshots IN SHARE ROW EXCLUSIVE MODE;
    IF EXISTS (
        SELECT 1 FROM app.jobs job
         WHERE job.kind = 'manual_ingest_source' AND job.status IN ('queued', 'running')
    ) OR EXISTS (
        SELECT 1 FROM app.job_config_snapshots snapshot
        JOIN app.jobs job ON job.id = snapshot.job_id AND job.account_id = snapshot.account_id
        WHERE job.kind = 'manual_ingest_source'
    ) THEN
        RAISE EXCEPTION 'cannot downgrade while manual ingest jobs or snapshots are active' USING ERRCODE = '55006';
    END IF;
END;
$$;
-- +goose StatementEnd

-- The row locks survive until the caller's surrounding account transaction
-- commits or rolls back, fencing every file-domain write in that transaction.
-- Source-parent-child order matches source deletion and parent cancellation.
-- +goose StatementBegin
CREATE FUNCTION app.fence_ingest_job(job_id uuid, claiming_worker text, current_lease_token uuid)
RETURNS TABLE(source_id uuid)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    snapshot_source_id uuid;
    parent_id uuid;
BEGIN
    IF claiming_worker = '' OR current_lease_token IS NULL THEN
        RAISE EXCEPTION 'invalid ingest fence arguments' USING ERRCODE = '22023';
    END IF;
    SELECT snapshot.source_id INTO snapshot_source_id
      FROM app.job_config_snapshots snapshot
     WHERE snapshot.job_id = fence_ingest_job.job_id
       AND snapshot.account_id = app.current_account_id();
    IF NOT FOUND THEN RETURN; END IF;

    PERFORM 1 FROM app.sources source
     WHERE source.id = snapshot_source_id
       AND source.account_id = app.current_account_id()
       AND source.deleted_at IS NULL
     FOR UPDATE;
    IF NOT FOUND THEN RETURN; END IF;

    SELECT job.parent_job_id INTO parent_id FROM app.jobs job
     WHERE job.id = fence_ingest_job.job_id AND job.account_id = app.current_account_id()
       AND job.kind = 'manual_ingest_source';
    IF NOT FOUND OR parent_id IS NULL THEN RETURN; END IF;
    PERFORM 1 FROM app.jobs parent
     WHERE parent.id = parent_id AND parent.account_id = app.current_account_id()
       AND parent.kind = 'manual_ingest'
     FOR UPDATE;
    IF NOT FOUND THEN RETURN; END IF;

    PERFORM 1 FROM app.jobs job
     WHERE job.id = fence_ingest_job.job_id
       AND job.account_id = app.current_account_id()
       AND job.kind = 'manual_ingest_source'
       AND job.status = 'running'
       AND job.cancel_requested_at IS NULL
       AND job.worker_id = claiming_worker
       AND job.lease_token = current_lease_token
       AND job.lease_expires_at >= clock_timestamp()
     FOR UPDATE;
    IF NOT FOUND THEN RETURN; END IF;
    INSERT INTO app.ingest_write_capabilities
        (backend_pid, transaction_id, account_id, source_id, job_id, worker_id, lease_token)
    VALUES (pg_backend_pid(), txid_current(), app.current_account_id(), snapshot_source_id,
        fence_ingest_job.job_id, claiming_worker, current_lease_token)
    ON CONFLICT (backend_pid, transaction_id) DO NOTHING;
    PERFORM 1 FROM app.ingest_write_capabilities capability
     WHERE capability.backend_pid = pg_backend_pid() AND capability.transaction_id = txid_current()
       AND capability.account_id = app.current_account_id() AND capability.source_id = snapshot_source_id
       AND capability.job_id = fence_ingest_job.job_id AND capability.worker_id = claiming_worker
       AND capability.lease_token = current_lease_token;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'transaction already fenced for another ingest job' USING ERRCODE = '25001';
    END IF;
    source_id := snapshot_source_id;
    RETURN NEXT;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.finish_job(job_id uuid, claiming_worker text, current_lease_token uuid, terminal_status text,
    failure_code_value text DEFAULT NULL, failure_summary_value text DEFAULT NULL)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    parent_id uuid;
    snapshot_source_id uuid;
BEGIN
    IF terminal_status NOT IN ('succeeded', 'failed', 'cancelled') THEN
        RAISE EXCEPTION 'invalid terminal status' USING ERRCODE = '22023';
    END IF;
    SELECT job.parent_job_id, snapshot.source_id INTO parent_id, snapshot_source_id
      FROM app.jobs job
      LEFT JOIN app.job_config_snapshots snapshot ON snapshot.job_id = job.id AND snapshot.account_id = job.account_id
     WHERE job.id = finish_job.job_id AND job.account_id = app.current_account_id();
    IF NOT FOUND THEN RETURN false; END IF;

    IF snapshot_source_id IS NOT NULL THEN
        PERFORM 1 FROM app.sources source
         WHERE source.id = snapshot_source_id AND source.account_id = app.current_account_id()
         FOR UPDATE;
        IF NOT FOUND THEN RETURN false; END IF;
    END IF;
    IF parent_id IS NOT NULL THEN
        PERFORM 1 FROM app.jobs parent
         WHERE parent.id = parent_id AND parent.account_id = app.current_account_id()
         FOR UPDATE;
        IF NOT FOUND THEN RETURN false; END IF;
    END IF;
    PERFORM 1 FROM app.jobs job
     WHERE job.id = finish_job.job_id AND job.account_id = app.current_account_id()
       AND job.status = 'running' AND job.worker_id = claiming_worker
       AND job.lease_token = current_lease_token AND job.lease_expires_at >= clock_timestamp()
     FOR UPDATE;
    IF NOT FOUND THEN RETURN false; END IF;

    DELETE FROM app.job_config_snapshots snapshot WHERE snapshot.job_id = finish_job.job_id;
    PERFORM set_config('app.job_transition', 'finish', true);
    UPDATE app.jobs job SET status = terminal_status, terminal_at = transaction_timestamp(),
        worker_id = NULL, lease_token = NULL, claimed_at = NULL, heartbeat_at = NULL, lease_expires_at = NULL,
        failure_code = failure_code_value, failure_summary = failure_summary_value
     WHERE job.id = finish_job.job_id AND job.account_id = app.current_account_id()
       AND job.status = 'running' AND job.worker_id = claiming_worker AND job.lease_token = current_lease_token;
    IF NOT FOUND THEN RETURN false; END IF;
    IF parent_id IS NOT NULL THEN PERFORM app.derive_parent_status(parent_id); END IF;
    RETURN true;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.recover_expired_job(job_id uuid)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    parent_id uuid;
    snapshot_source_id uuid;
    cancellation_pending boolean;
BEGIN
    SELECT job.parent_job_id, snapshot.source_id INTO parent_id, snapshot_source_id
      FROM app.jobs job
      LEFT JOIN app.job_config_snapshots snapshot ON snapshot.job_id = job.id AND snapshot.account_id = job.account_id
     WHERE job.id = recover_expired_job.job_id AND job.account_id = app.current_account_id();
    IF NOT FOUND THEN RETURN false; END IF;

    IF snapshot_source_id IS NOT NULL THEN
        PERFORM 1 FROM app.sources source
         WHERE source.id = snapshot_source_id AND source.account_id = app.current_account_id()
         FOR UPDATE;
        IF NOT FOUND THEN RETURN false; END IF;
    END IF;
    IF parent_id IS NOT NULL THEN
        PERFORM 1 FROM app.jobs parent
         WHERE parent.id = parent_id AND parent.account_id = app.current_account_id()
         FOR UPDATE;
        IF NOT FOUND THEN RETURN false; END IF;
    END IF;
    SELECT job.cancel_requested_at IS NOT NULL INTO cancellation_pending FROM app.jobs job
     WHERE job.id = recover_expired_job.job_id AND job.account_id = app.current_account_id()
       AND job.status = 'running' AND job.lease_expires_at < clock_timestamp()
     FOR UPDATE;
    IF NOT FOUND THEN RETURN false; END IF;

    IF cancellation_pending THEN
        DELETE FROM app.job_config_snapshots snapshot WHERE snapshot.job_id = recover_expired_job.job_id;
    END IF;
    PERFORM set_config('app.job_transition', 'recover', true);
    UPDATE app.jobs job SET status = CASE WHEN job.cancel_requested_at IS NULL THEN 'queued' ELSE 'cancelled' END,
        terminal_at = CASE WHEN job.cancel_requested_at IS NULL THEN NULL ELSE transaction_timestamp() END,
        worker_id = NULL, lease_token = NULL, claimed_at = NULL, heartbeat_at = NULL, lease_expires_at = NULL
     WHERE job.id = recover_expired_job.job_id AND job.account_id = app.current_account_id()
       AND job.status = 'running';
    IF NOT FOUND THEN RETURN false; END IF;
    IF parent_id IS NOT NULL THEN PERFORM app.derive_parent_status(parent_id); END IF;
    RETURN true;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.request_job_cancellation(job_id uuid, requester_id uuid)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    target_kind text;
    target_status text;
    parent_id uuid;
    snapshot_source_id uuid;
    source_to_lock uuid;
    child_to_lock uuid;
    locked_source_ids uuid[] := ARRAY[]::uuid[];
BEGIN
    IF requester_id IS NULL THEN RAISE EXCEPTION 'requester is required' USING ERRCODE = '22023'; END IF;
    SELECT job.kind, job.status, job.parent_job_id, snapshot.source_id
      INTO target_kind, target_status, parent_id, snapshot_source_id
      FROM app.jobs job
      LEFT JOIN app.job_config_snapshots snapshot ON snapshot.job_id = job.id AND snapshot.account_id = job.account_id
     WHERE job.id = request_job_cancellation.job_id AND job.account_id = app.current_account_id();
    IF NOT FOUND OR target_status IN ('succeeded', 'partially_succeeded', 'failed', 'cancelled') THEN RETURN false; END IF;

    IF target_kind IN ('manual_ingest', 'scheduled_ingest') THEN
        FOR source_to_lock IN
            SELECT DISTINCT snapshot.source_id
              FROM app.jobs child
              JOIN app.job_config_snapshots snapshot ON snapshot.job_id = child.id AND snapshot.account_id = child.account_id
             WHERE child.parent_job_id = request_job_cancellation.job_id
               AND child.account_id = app.current_account_id() AND child.status IN ('queued', 'running')
             ORDER BY snapshot.source_id
        LOOP
            PERFORM 1 FROM app.sources source
             WHERE source.id = source_to_lock AND source.account_id = app.current_account_id()
             FOR UPDATE;
            IF NOT FOUND THEN RETURN false; END IF;
            locked_source_ids := array_append(locked_source_ids, source_to_lock);
        END LOOP;

        SELECT job.kind, job.status INTO target_kind, target_status FROM app.jobs job
         WHERE job.id = request_job_cancellation.job_id AND job.account_id = app.current_account_id()
         FOR UPDATE;
        IF NOT FOUND OR target_kind NOT IN ('manual_ingest', 'scheduled_ingest') OR
           target_status IN ('succeeded', 'partially_succeeded', 'failed', 'cancelled') THEN RETURN false; END IF;
        IF EXISTS (
            SELECT 1 FROM app.jobs child
            JOIN app.job_config_snapshots snapshot ON snapshot.job_id = child.id AND snapshot.account_id = child.account_id
            WHERE child.parent_job_id = request_job_cancellation.job_id
              AND child.account_id = app.current_account_id() AND child.status IN ('queued', 'running')
              AND NOT (snapshot.source_id = ANY(locked_source_ids))
        ) THEN
            RAISE EXCEPTION 'source children changed during cancellation' USING ERRCODE = '40001';
        END IF;
        FOR child_to_lock IN
            SELECT child.id FROM app.jobs child
             WHERE child.parent_job_id = request_job_cancellation.job_id
               AND child.account_id = app.current_account_id() AND child.status IN ('queued', 'running')
             ORDER BY child.id
        LOOP
            PERFORM 1 FROM app.jobs child WHERE child.id = child_to_lock AND child.account_id = app.current_account_id()
             FOR UPDATE;
        END LOOP;

        PERFORM set_config('app.job_transition', 'cancel', true);
        UPDATE app.jobs job SET cancel_requested_at = COALESCE(job.cancel_requested_at, transaction_timestamp()),
            cancel_requested_by = COALESCE(job.cancel_requested_by, requester_id)
         WHERE job.id = request_job_cancellation.job_id AND job.account_id = app.current_account_id();
        DELETE FROM app.job_config_snapshots snapshot USING app.jobs child
         WHERE child.parent_job_id = request_job_cancellation.job_id AND child.account_id = app.current_account_id()
           AND child.status = 'queued' AND snapshot.job_id = child.id;
        UPDATE app.jobs child SET status = 'cancelled', terminal_at = transaction_timestamp(),
            cancel_requested_at = transaction_timestamp(), cancel_requested_by = requester_id
         WHERE child.parent_job_id = request_job_cancellation.job_id
           AND child.account_id = app.current_account_id() AND child.status = 'queued';
        UPDATE app.jobs child SET cancel_requested_at = COALESCE(child.cancel_requested_at, transaction_timestamp()),
            cancel_requested_by = COALESCE(child.cancel_requested_by, requester_id)
         WHERE child.parent_job_id = request_job_cancellation.job_id
           AND child.account_id = app.current_account_id() AND child.status = 'running';
        PERFORM app.derive_parent_status(request_job_cancellation.job_id);
    ELSE
        IF snapshot_source_id IS NOT NULL THEN
            PERFORM 1 FROM app.sources source
             WHERE source.id = snapshot_source_id AND source.account_id = app.current_account_id()
             FOR UPDATE;
            IF NOT FOUND THEN RETURN false; END IF;
        END IF;
        IF parent_id IS NOT NULL THEN
            PERFORM 1 FROM app.jobs parent
             WHERE parent.id = parent_id AND parent.account_id = app.current_account_id()
             FOR UPDATE;
            IF NOT FOUND THEN RETURN false; END IF;
        END IF;
        SELECT job.kind, job.status INTO target_kind, target_status FROM app.jobs job
         WHERE job.id = request_job_cancellation.job_id AND job.account_id = app.current_account_id()
         FOR UPDATE;
        IF NOT FOUND OR target_status IN ('succeeded', 'partially_succeeded', 'failed', 'cancelled') THEN RETURN false; END IF;

        PERFORM set_config('app.job_transition', 'cancel', true);
        IF target_status = 'queued' THEN
            DELETE FROM app.job_config_snapshots snapshot WHERE snapshot.job_id = request_job_cancellation.job_id;
        END IF;
        UPDATE app.jobs job SET status = CASE WHEN job.status = 'queued' THEN 'cancelled' ELSE job.status END,
            terminal_at = CASE WHEN job.status = 'queued' THEN transaction_timestamp() ELSE job.terminal_at END,
            cancel_requested_at = COALESCE(job.cancel_requested_at, transaction_timestamp()),
            cancel_requested_by = COALESCE(job.cancel_requested_by, requester_id)
         WHERE job.id = request_job_cancellation.job_id AND job.account_id = app.current_account_id()
           AND job.status IN ('queued', 'running');
        IF parent_id IS NOT NULL THEN PERFORM app.derive_parent_status(parent_id); END IF;
    END IF;
    RETURN true;
END;
$$;
-- +goose StatementEnd

DROP FUNCTION app.claim_next_source_connection_check(text,uuid,interval);

-- Candidate discovery is intentionally lock-free under the fixed owner policy.
-- Each candidate is then locked source -> parent -> child with SKIP LOCKED,
-- avoiding inversion with source deletion and parent cancellation.
-- +goose StatementBegin
CREATE FUNCTION app.claim_next_worker_job_internal(claiming_worker text, new_lease_token uuid,
    lease_duration interval, include_manual_ingest boolean)
RETURNS TABLE(job_id uuid, account_id uuid, kind text)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    candidate_job_id uuid;
    candidate_account_id uuid;
    candidate_kind text;
    candidate_status text;
    candidate_parent_id uuid;
    candidate_source_id uuid;
    attempted uuid[] := ARRAY[]::uuid[];
BEGIN
    IF claiming_worker = '' OR new_lease_token IS NULL OR lease_duration <= interval '0 seconds' THEN
        RAISE EXCEPTION 'invalid claim arguments' USING ERRCODE = '22023';
    END IF;
    LOOP
        SELECT job.id, job.account_id, job.kind, job.status, job.parent_job_id
          INTO candidate_job_id, candidate_account_id, candidate_kind, candidate_status, candidate_parent_id
          FROM app.jobs job
         WHERE (job.kind = 'source_connection_check' OR
                (include_manual_ingest AND job.kind = 'manual_ingest_source')) AND
               ((job.status = 'queued' AND job.cancel_requested_at IS NULL) OR
                (job.status = 'running' AND job.lease_expires_at < transaction_timestamp())) AND
               NOT (job.id = ANY(attempted))
         ORDER BY job.priority DESC, job.created_at, job.id
         LIMIT 1;
        IF NOT FOUND THEN RETURN; END IF;
        attempted := array_append(attempted, candidate_job_id);
        PERFORM set_config('app.account_id', candidate_account_id::text, true);

        SELECT snapshot.source_id INTO candidate_source_id FROM app.job_config_snapshots snapshot
         WHERE snapshot.job_id = candidate_job_id AND snapshot.account_id = candidate_account_id;
        IF NOT FOUND THEN CONTINUE; END IF;
        PERFORM 1 FROM app.sources source
         WHERE source.id = candidate_source_id AND source.account_id = candidate_account_id
           AND source.deleted_at IS NULL
         FOR UPDATE SKIP LOCKED;
        IF NOT FOUND THEN CONTINUE; END IF;

        IF candidate_parent_id IS NOT NULL THEN
            PERFORM 1 FROM app.jobs parent
             WHERE parent.id = candidate_parent_id AND parent.account_id = candidate_account_id
               AND parent.kind = 'manual_ingest'
             FOR UPDATE SKIP LOCKED;
            IF NOT FOUND THEN CONTINUE; END IF;
        END IF;

        SELECT job.status INTO candidate_status FROM app.jobs job
         WHERE job.id = candidate_job_id AND job.account_id = candidate_account_id
           AND job.kind = candidate_kind
           AND ((job.status = 'queued' AND job.cancel_requested_at IS NULL) OR
                (job.status = 'running' AND job.lease_expires_at < transaction_timestamp()))
         FOR UPDATE SKIP LOCKED;
        IF NOT FOUND THEN CONTINUE; END IF;
        IF candidate_status = 'running' AND NOT app.recover_expired_job(candidate_job_id) THEN
            CONTINUE;
        END IF;
        IF app.claim_job(candidate_job_id, claiming_worker, new_lease_token, lease_duration) THEN
            job_id := candidate_job_id;
            account_id := candidate_account_id;
            kind := candidate_kind;
            RETURN NEXT;
            RETURN;
        END IF;
    END LOOP;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.claim_next_worker_job(claiming_worker text, new_lease_token uuid, lease_duration interval)
RETURNS TABLE(job_id uuid, account_id uuid, kind text)
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
    SELECT claimed.job_id, claimed.account_id, claimed.kind
      FROM app.claim_next_worker_job_internal(claiming_worker, new_lease_token, lease_duration, true) claimed
$$;
-- +goose StatementEnd

-- Compatibility for the migration-3 worker cannot consume manual ingest work.
-- +goose StatementBegin
CREATE FUNCTION app.claim_next_source_connection_check(claiming_worker text, new_lease_token uuid, lease_duration interval)
RETURNS TABLE(job_id uuid, account_id uuid)
LANGUAGE sql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
    SELECT claimed.job_id, claimed.account_id
      FROM app.claim_next_worker_job_internal(claiming_worker, new_lease_token, lease_duration, false) claimed
$$;
-- +goose StatementEnd

REVOKE ALL ON app.source_files, app.workout_types, app.workouts, app.workout_aggregates,
    app.workout_route_points, app.workout_import_events, app.ingest_write_capabilities
    FROM PUBLIC, workouts_api, workouts_worker;
REVOKE ALL ON FUNCTION app.require_ingest_write_capability(), app.clear_ingest_write_capability(),
    app.valid_workout_warnings(jsonb), app.assert_no_active_manual_ingest(), app.fence_ingest_job(uuid,text,uuid),
    app.claim_next_worker_job_internal(text,uuid,interval,boolean), app.claim_next_worker_job(text,uuid,interval),
    app.claim_next_source_connection_check(text,uuid,interval) FROM PUBLIC, workouts_api, workouts_worker;

GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER TABLE app.ingest_write_capabilities OWNER TO workouts_security_owner;
ALTER FUNCTION app.require_ingest_write_capability() OWNER TO workouts_security_owner;
ALTER FUNCTION app.clear_ingest_write_capability() OWNER TO workouts_security_owner;
ALTER FUNCTION app.assert_no_active_manual_ingest() OWNER TO workouts_security_owner;
ALTER FUNCTION app.fence_ingest_job(uuid,text,uuid) OWNER TO workouts_security_owner;
ALTER FUNCTION app.claim_next_worker_job_internal(text,uuid,interval,boolean) OWNER TO workouts_security_owner;
ALTER FUNCTION app.claim_next_worker_job(text,uuid,interval) OWNER TO workouts_security_owner;
ALTER FUNCTION app.claim_next_source_connection_check(text,uuid,interval) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
GRANT EXECUTE ON FUNCTION app.assert_no_active_manual_ingest() TO workouts_migration;
GRANT SELECT, UPDATE ON app.jobs TO workouts_security_owner;
GRANT SELECT ON app.sources, app.job_config_snapshots, app.source_files, app.workouts TO workouts_security_owner;
GRANT EXECUTE ON FUNCTION app.claim_job(uuid,text,uuid,interval) TO workouts_security_owner;

GRANT SELECT ON app.source_files, app.workout_types, app.workouts, app.workout_aggregates,
    app.workout_route_points, app.workout_import_events TO workouts_api;

GRANT SELECT, INSERT, UPDATE ON app.source_files, app.workout_types, app.workouts TO workouts_worker;
GRANT SELECT, INSERT, UPDATE, DELETE ON app.workout_aggregates, app.workout_route_points TO workouts_worker;
GRANT SELECT, INSERT ON app.workout_import_events TO workouts_worker;
GRANT EXECUTE ON FUNCTION app.valid_workout_warnings(jsonb), app.fence_ingest_job(uuid,text,uuid),
    app.claim_next_worker_job(text,uuid,interval),
    app.claim_next_source_connection_check(text,uuid,interval) TO workouts_worker;

UPDATE app.schema_metadata SET schema_version = 4, minimum_runtime_version = 1;

-- +goose Down
SELECT app.assert_no_active_manual_ingest();
UPDATE app.schema_metadata SET schema_version = 3, minimum_runtime_version = 1;

-- Restore migration-3 transition functions exactly.
-- +goose StatementBegin
CREATE OR REPLACE FUNCTION app.finish_job(job_id uuid, claiming_worker text, current_lease_token uuid, terminal_status text,
    failure_code_value text DEFAULT NULL, failure_summary_value text DEFAULT NULL)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $$
DECLARE parent_id uuid;
BEGIN
    IF terminal_status NOT IN ('succeeded', 'failed', 'cancelled') THEN RAISE EXCEPTION 'invalid terminal status' USING ERRCODE = '22023'; END IF;
    PERFORM 1 FROM app.jobs WHERE id = job_id AND account_id = app.current_account_id()
      AND status = 'running' AND worker_id = claiming_worker AND lease_token = current_lease_token
      AND lease_expires_at >= clock_timestamp() FOR UPDATE;
    IF NOT FOUND THEN RETURN false; END IF;
    DELETE FROM app.job_config_snapshots snapshot WHERE snapshot.job_id = finish_job.job_id;
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
CREATE OR REPLACE FUNCTION app.recover_expired_job(job_id uuid)
RETURNS boolean
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE parent_id uuid; cancellation_pending boolean;
BEGIN
    SELECT cancel_requested_at IS NOT NULL INTO cancellation_pending FROM app.jobs
    WHERE id = job_id AND account_id = app.current_account_id() AND status = 'running' AND lease_expires_at < now()
    FOR UPDATE;
    IF NOT FOUND THEN RETURN false; END IF;
    IF cancellation_pending THEN
        DELETE FROM app.job_config_snapshots snapshot WHERE snapshot.job_id = recover_expired_job.job_id;
    END IF;
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
CREATE OR REPLACE FUNCTION app.request_job_cancellation(job_id uuid, requester_id uuid)
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
        DELETE FROM app.job_config_snapshots snapshot USING app.jobs child
        WHERE child.parent_job_id = request_job_cancellation.job_id AND child.account_id = app.current_account_id()
          AND child.status = 'queued' AND snapshot.job_id = child.id;
        UPDATE app.jobs SET status = 'cancelled', terminal_at = now(), cancel_requested_at = now(), cancel_requested_by = requester_id
        WHERE parent_job_id = job_id AND account_id = app.current_account_id() AND status = 'queued';
        UPDATE app.jobs SET cancel_requested_at = COALESCE(cancel_requested_at, now()), cancel_requested_by = COALESCE(cancel_requested_by, requester_id)
        WHERE parent_job_id = job_id AND account_id = app.current_account_id() AND status = 'running';
        PERFORM app.derive_parent_status(job_id);
    ELSE
        IF target_status = 'queued' THEN
            DELETE FROM app.job_config_snapshots snapshot WHERE snapshot.job_id = request_job_cancellation.job_id;
        END IF;
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

DROP FUNCTION app.fence_ingest_job(uuid,text,uuid);
DROP FUNCTION app.claim_next_source_connection_check(text,uuid,interval);
DROP FUNCTION app.claim_next_worker_job(text,uuid,interval);
DROP FUNCTION app.claim_next_worker_job_internal(text,uuid,interval,boolean);
DROP FUNCTION app.assert_no_active_manual_ingest();
DROP POLICY job_config_snapshots_cross_account_guard_policy ON app.job_config_snapshots;
DROP POLICY jobs_cross_account_claim_policy ON app.jobs;
REVOKE EXECUTE ON FUNCTION app.claim_job(uuid,text,uuid,interval) FROM workouts_security_owner;

-- Restore the migration-3 claim implementation exactly rather than retaining a
-- wrapper that depends on migration-4 objects.
-- +goose StatementBegin
CREATE FUNCTION app.claim_next_source_connection_check(claiming_worker text, new_lease_token uuid, lease_duration interval)
RETURNS TABLE(job_id uuid, account_id uuid)
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, app
AS $$
DECLARE
    candidate_job_id uuid;
    candidate_account_id uuid;
    candidate_status text;
BEGIN
    IF claiming_worker = '' OR new_lease_token IS NULL OR lease_duration <= interval '0 seconds' THEN
        RAISE EXCEPTION 'invalid claim arguments' USING ERRCODE = '22023';
    END IF;
    LOOP
        SELECT job.id, job.account_id, job.status
          INTO candidate_job_id, candidate_account_id, candidate_status
          FROM app.jobs job
         WHERE job.kind = 'source_connection_check' AND
               ((job.status = 'queued' AND job.cancel_requested_at IS NULL) OR
                (job.status = 'running' AND job.lease_expires_at < transaction_timestamp()))
         ORDER BY job.priority DESC, job.created_at, job.id
         FOR UPDATE SKIP LOCKED
         LIMIT 1;
        IF NOT FOUND THEN RETURN; END IF;
        PERFORM set_config('app.account_id', candidate_account_id::text, true);
        IF candidate_status = 'running' AND NOT app.recover_expired_job(candidate_job_id) THEN
            CONTINUE;
        END IF;
        IF app.claim_job(candidate_job_id, claiming_worker, new_lease_token, lease_duration) THEN
            job_id := candidate_job_id;
            account_id := candidate_account_id;
            RETURN NEXT;
            RETURN;
        END IF;
    END LOOP;
END;
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION app.claim_next_source_connection_check(text,uuid,interval) FROM PUBLIC, workouts_api, workouts_worker;
GRANT EXECUTE ON FUNCTION app.claim_next_source_connection_check(text,uuid,interval) TO workouts_worker;

DROP FUNCTION app.reject_workout_import_event_mutation() CASCADE;
DROP FUNCTION app.enforce_ingest_identity() CASCADE;
DROP FUNCTION app.require_ingest_write_capability() CASCADE;
DROP FUNCTION app.clear_ingest_write_capability() CASCADE;
DROP TABLE app.workout_import_events;
DROP FUNCTION app.valid_workout_warnings(jsonb);
DROP TABLE app.workout_route_points;
DROP TABLE app.workout_aggregates;
DROP TABLE app.workouts;
DROP TABLE app.workout_types;
DROP TABLE app.source_files;
DROP TABLE app.ingest_write_capabilities;
