-- +goose Up
-- +goose StatementBegin
DO $block$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'workouts_security_owner' AND NOT rolcanlogin AND NOT rolsuper AND NOT rolbypassrls) THEN
        RAISE EXCEPTION 'required safe NOLOGIN role workouts_security_owner is missing';
    END IF;
END
$block$;

-- +goose StatementEnd

CREATE TABLE app.authentication_principals (
    id uuid PRIMARY KEY,
    role text NOT NULL CHECK (role IN ('administrator', 'user')),
    username text NOT NULL,
    canonical_username text COLLATE "C" NOT NULL UNIQUE,
    email text NOT NULL,
    canonical_email text COLLATE "C" NOT NULL UNIQUE,
    canonicalization_version smallint NOT NULL CHECK (canonicalization_version = 1),
    password_hash text NOT NULL,
    full_name text NOT NULL CHECK (length(full_name) BETWEEN 1 AND 200),
    disabled_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp()
);

CREATE TABLE app.administrators (
    principal_id uuid PRIMARY KEY REFERENCES app.authentication_principals (id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp()
);

CREATE TABLE app.accounts (
    id uuid PRIMARY KEY,
    state text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'disabled', 'deleting', 'deleted')),
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp()
);

CREATE TABLE app.users (
    principal_id uuid PRIMARY KEY REFERENCES app.authentication_principals (id) ON DELETE RESTRICT,
    account_id uuid NOT NULL UNIQUE REFERENCES app.accounts (id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp()
);

-- +goose StatementBegin
CREATE FUNCTION app.enforce_principal_role_consistency () RETURNS trigger LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
DECLARE
    target_id uuid;
    target_role text;
    administrator_exists boolean;
    user_exists boolean;
BEGIN
    IF TG_TABLE_NAME = 'authentication_principals' THEN
        IF TG_OP = 'DELETE' THEN target_id := OLD.id; ELSE target_id := NEW.id; END IF;
    ELSE
        IF TG_OP = 'DELETE' THEN target_id := OLD.principal_id; ELSE target_id := NEW.principal_id; END IF;
    END IF;
    SELECT role INTO target_role FROM app.authentication_principals WHERE id = target_id;
    IF NOT FOUND THEN RETURN NULL; END IF;
    SELECT EXISTS(SELECT 1 FROM app.administrators WHERE principal_id = target_id),
           EXISTS(SELECT 1 FROM app.users WHERE principal_id = target_id)
      INTO administrator_exists, user_exists;
    IF (target_role = 'administrator' AND (NOT administrator_exists OR user_exists)) OR
       (target_role = 'user' AND (administrator_exists OR NOT user_exists)) THEN
        RAISE EXCEPTION 'principal role is inconsistent with identity subtype' USING ERRCODE = '23514';
    END IF;
    RETURN NULL;
END;
$function$;

-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER authentication_principals_role_consistency
AFTER INSERT OR UPDATE OF role ON app.authentication_principals
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION app.enforce_principal_role_consistency ();

CREATE CONSTRAINT TRIGGER administrators_role_consistency
AFTER INSERT OR UPDATE OR DELETE ON app.administrators
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION app.enforce_principal_role_consistency ();

CREATE CONSTRAINT TRIGGER users_role_consistency
AFTER INSERT OR UPDATE OR DELETE ON app.users
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW
EXECUTE FUNCTION app.enforce_principal_role_consistency ();

CREATE TABLE app.preferences (
    account_id uuid PRIMARY KEY REFERENCES app.accounts (id) ON DELETE CASCADE,
    theme text NOT NULL DEFAULT 'dark' CHECK (theme IN ('dark', 'light')),
    units text NOT NULL DEFAULT 'imperial' CHECK (units IN ('imperial', 'metric')),
    timezone text NOT NULL DEFAULT 'UTC',
    first_weekday text NOT NULL DEFAULT 'monday' CHECK (first_weekday IN ('monday', 'sunday')),
    clock_format text NOT NULL DEFAULT '12h' CHECK (clock_format IN ('12h', '24h')),
    workout_columns TEXT[] NOT NULL DEFAULT ARRAY['date', 'type', 'duration', 'distance'],
    page_size integer NOT NULL DEFAULT 25 CHECK (page_size >= 10),
    date_range text CHECK (
        date_range IS NULL
        OR length(date_range) <= 64
    ),
    initialized_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    custom_date_range_limit integer NOT NULL DEFAULT 3 CHECK (custom_date_range_limit BETWEEN 1 AND 5),
    recent_custom_date_ranges text[] NOT NULL DEFAULT '{}',
    CONSTRAINT preferences_custom_date_ranges_bounded CHECK (
        cardinality(recent_custom_date_ranges) <= custom_date_range_limit
        AND array_position(recent_custom_date_ranges, NULL) IS NULL
    ),
    date_format text NOT NULL DEFAULT 'mm/dd/yyyy',
    CONSTRAINT preferences_date_format_check CHECK (date_format IN ('mm/dd/yyyy','dd/mm/yyyy','yyyy/mm/dd'))
);

ALTER TABLE app.preferences ENABLE ROW LEVEL SECURITY;

ALTER TABLE app.preferences FORCE ROW LEVEL SECURITY;

CREATE POLICY preferences_account_policy ON app.preferences USING (account_id = app.current_account_id ())
WITH
    CHECK (account_id = app.current_account_id ());

CREATE TABLE app.sessions (
    id uuid PRIMARY KEY,
    principal_id uuid NOT NULL REFERENCES app.authentication_principals (id) ON DELETE CASCADE,
    credential_kind text NOT NULL CHECK (credential_kind IN ('cookie', 'bearer')),
    credential_verifier bytea NOT NULL UNIQUE CHECK (octet_length(credential_verifier) = 32),
    csrf_token text CHECK ((credential_kind = 'cookie') = (csrf_token IS NOT NULL)),
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    CHECK (expires_at > created_at)
);

CREATE INDEX sessions_principal_live_idx ON app.sessions (principal_id, expires_at)
WHERE
    revoked_at IS NULL;

CREATE TABLE app.invitations (
    id uuid PRIMARY KEY,
    email text NOT NULL,
    canonical_email text COLLATE "C" NOT NULL,
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'accepted', 'revoked')),
    created_by uuid NOT NULL REFERENCES app.authentication_principals (id),
    accepted_by uuid REFERENCES app.authentication_principals (id),
    accepted_at timestamptz,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    CHECK (
        (state = 'accepted') = (
            accepted_by IS NOT NULL
            AND accepted_at IS NOT NULL
        )
    ),
    CHECK ((state = 'revoked') = (revoked_at IS NOT NULL))
);

CREATE UNIQUE INDEX invitations_pending_email_idx ON app.invitations (canonical_email)
WHERE
    state = 'pending';

CREATE TABLE app.invitation_tokens (
    invitation_id uuid NOT NULL REFERENCES app.invitations (id) ON DELETE CASCADE,
    verifier bytea PRIMARY KEY CHECK (octet_length(verifier) = 32),
    issued_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    expires_at timestamptz NOT NULL DEFAULT transaction_timestamp() + interval '7 days',
    used_at timestamptz,
    revoked_at timestamptz,
    replaced_at timestamptz,
    delivery_state text NOT NULL DEFAULT 'pending' CHECK (delivery_state IN ('pending', 'delivered', 'failed', 'unknown')),
    delivery_category text CHECK (delivery_category IN ('timeout', 'tls', 'authentication', 'rejected', 'queue_full', 'interrupted')),
    CHECK (expires_at = issued_at + interval '7 days')
);

CREATE UNIQUE INDEX invitation_tokens_live_idx ON app.invitation_tokens (invitation_id)
WHERE
    used_at IS NULL
    AND revoked_at IS NULL;

CREATE TABLE app.password_resets (
    principal_id uuid NOT NULL REFERENCES app.authentication_principals (id) ON DELETE CASCADE,
    verifier bytea PRIMARY KEY CHECK (octet_length(verifier) = 32),
    issued_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    expires_at timestamptz NOT NULL DEFAULT transaction_timestamp() + interval '30 minutes',
    used_at timestamptz,
    revoked_at timestamptz,
    replaced_at timestamptz,
    delivery_state text NOT NULL DEFAULT 'pending' CHECK (delivery_state IN ('pending', 'delivered', 'failed', 'unknown')),
    delivery_category text CHECK (delivery_category IN ('timeout', 'tls', 'authentication', 'rejected', 'queue_full', 'interrupted')),
    CHECK (expires_at = issued_at + interval '30 minutes')
);

CREATE UNIQUE INDEX password_resets_live_idx ON app.password_resets (principal_id)
WHERE
    used_at IS NULL
    AND revoked_at IS NULL;

CREATE TABLE app.rate_limits (
    operation text NOT NULL,
    key_kind text NOT NULL CHECK (key_kind IN ('network', 'subject')),
    key_digest bytea NOT NULL CHECK (octet_length(key_digest) = 32),
    window_start timestamptz NOT NULL,
    window_end timestamptz NOT NULL,
    count integer NOT NULL CHECK (count > 0),
    PRIMARY KEY (operation, key_kind, key_digest, window_start),
    CHECK (window_end > window_start)
);

CREATE TABLE app.audit_records (
    id uuid PRIMARY KEY,
    actor_principal_id uuid REFERENCES app.authentication_principals (id),
    account_id uuid REFERENCES app.accounts (id),
    action text NOT NULL CHECK (length(action) BETWEEN 1 AND 80),
    target_type text NOT NULL CHECK (length(target_type) BETWEEN 1 AND 40),
    target_id uuid,
    request_id text,
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp()
);

-- +goose StatementBegin
CREATE FUNCTION app.consume_rate_limit (operation_value text, kind_value text, digest_value bytea) RETURNS TABLE (allowed boolean, retry_after_seconds integer) LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
DECLARE
    resulting_count integer;
    window_seconds integer;
    limit_value integer;
    window_start_value timestamptz;
    window_end_value timestamptz;
BEGIN
    IF kind_value NOT IN ('network', 'subject') OR octet_length(digest_value) <> 32 THEN
        RAISE EXCEPTION 'invalid rate limit arguments' USING ERRCODE = '22023';
    END IF;
    SELECT profile.window_seconds,
           CASE kind_value WHEN 'network' THEN profile.network_limit ELSE profile.subject_limit END
      INTO window_seconds, limit_value
      FROM (VALUES
        ('signin', 600, 30, 10),
        ('registration', 3600, 20, 10),
        ('password_reset_request', 3600, 10, 3),
        ('password_reset', 3600, 20, 10)
      ) AS profile(operation, window_seconds, network_limit, subject_limit)
      WHERE profile.operation = operation_value;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'unknown rate limit operation' USING ERRCODE = '22023';
    END IF;
    window_start_value := to_timestamp(floor(extract(epoch FROM transaction_timestamp()) / window_seconds) * window_seconds);
    window_end_value := window_start_value + make_interval(secs => window_seconds);
    INSERT INTO app.rate_limits(operation, key_kind, key_digest, window_start, window_end, count)
    VALUES(operation_value, kind_value, digest_value, window_start_value, window_end_value, 1)
    ON CONFLICT(operation, key_kind, key_digest, window_start)
    DO UPDATE SET count = app.rate_limits.count + 1
    RETURNING count INTO resulting_count;
    RETURN QUERY SELECT resulting_count <= limit_value,
        GREATEST(1, CEIL(EXTRACT(epoch FROM window_end_value - clock_timestamp()))::integer);
END;
$function$;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.issue_password_reset (identity_value text, email_identity boolean, verifier_value bytea) RETURNS TABLE (result_principal_id uuid, result_email text) LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
BEGIN
    IF identity_value = '' OR octet_length(verifier_value) <> 32 THEN
        RAISE EXCEPTION 'invalid password reset arguments' USING ERRCODE = '22023';
    END IF;
    SELECT p.id, p.email INTO result_principal_id, result_email
      FROM app.authentication_principals p
      LEFT JOIN app.users u ON u.principal_id = p.id
      LEFT JOIN app.accounts a ON a.id = u.account_id
     WHERE ((email_identity AND p.canonical_email = identity_value) OR
            (NOT email_identity AND p.canonical_username = identity_value))
       AND p.disabled_at IS NULL AND (p.role = 'administrator' OR a.state = 'active')
     FOR UPDATE OF p;
    IF NOT FOUND THEN RETURN; END IF;
    UPDATE app.password_resets SET revoked_at = transaction_timestamp(), replaced_at = transaction_timestamp()
     WHERE app.password_resets.principal_id = result_principal_id
       AND used_at IS NULL AND revoked_at IS NULL;
    INSERT INTO app.password_resets(principal_id, verifier) VALUES(result_principal_id, verifier_value);
    RETURN NEXT;
END;
$function$;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.complete_password_reset (verifier_value bytea, password_hash_value text, audit_id_value uuid, request_id_value text) RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
DECLARE target_principal_id uuid; changed integer;
BEGIN
    IF octet_length(verifier_value) <> 32 OR password_hash_value = '' OR audit_id_value IS NULL THEN
        RAISE EXCEPTION 'invalid password reset completion arguments' USING ERRCODE = '22023';
    END IF;
    SELECT principal_id INTO target_principal_id FROM app.password_resets
     WHERE verifier = verifier_value AND used_at IS NULL AND revoked_at IS NULL
       AND transaction_timestamp() < expires_at FOR UPDATE;
    IF NOT FOUND THEN RETURN false; END IF;
    UPDATE app.authentication_principals SET password_hash = password_hash_value, updated_at = transaction_timestamp()
     WHERE id = target_principal_id AND disabled_at IS NULL;
    GET DIAGNOSTICS changed = ROW_COUNT;
    IF changed <> 1 THEN RETURN false; END IF;
    UPDATE app.password_resets SET used_at = transaction_timestamp() WHERE verifier = verifier_value;
    UPDATE app.sessions SET revoked_at = transaction_timestamp()
     WHERE principal_id = target_principal_id AND revoked_at IS NULL;
    INSERT INTO app.audit_records(id, actor_principal_id, action, target_type, target_id, request_id)
    VALUES(audit_id_value, target_principal_id, 'password.reset', 'principal', target_principal_id, request_id_value);
    RETURN true;
END;
$function$;

-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.cleanup_rate_limits () RETURNS integer LANGUAGE plpgsql SECURITY DEFINER
SET
    search_path = pg_catalog,
    app AS $function$
DECLARE deleted_count integer := 0; deleted_batch integer;
BEGIN
    IF NOT pg_try_advisory_xact_lock(94722101) THEN RETURN 0; END IF;
    FOR pass IN 1..5 LOOP
        WITH expired AS (SELECT ctid FROM app.rate_limits WHERE window_end < transaction_timestamp() - interval '24 hours' LIMIT 1000)
        DELETE FROM app.rate_limits WHERE ctid IN (SELECT ctid FROM expired);
        GET DIAGNOSTICS deleted_batch = ROW_COUNT;
        deleted_count := deleted_count + deleted_batch;
        EXIT WHEN deleted_batch < 1000;
    END LOOP;
    RETURN deleted_count;
END;
$function$;

-- +goose StatementEnd

GRANT CREATE ON SCHEMA app TO workouts_security_owner;

ALTER FUNCTION app.enforce_principal_role_consistency () OWNER TO workouts_security_owner;

ALTER FUNCTION app.consume_rate_limit (text, text, bytea) OWNER TO workouts_security_owner;

ALTER FUNCTION app.issue_password_reset (text, boolean, bytea) OWNER TO workouts_security_owner;

ALTER FUNCTION app.complete_password_reset (bytea, text, uuid, text) OWNER TO workouts_security_owner;

ALTER FUNCTION app.cleanup_rate_limits () OWNER TO workouts_security_owner;

REVOKE CREATE ON SCHEMA app
FROM
    workouts_security_owner;

GRANT USAGE ON SCHEMA app TO workouts_security_owner;

GRANT
SELECT
,
    INSERT,
UPDATE,
DELETE ON app.rate_limits TO workouts_security_owner;

GRANT
SELECT
    ON app.authentication_principals,
    app.administrators,
    app.users,
    app.accounts TO workouts_security_owner;

GRANT
UPDATE (password_hash, updated_at) ON app.authentication_principals TO workouts_security_owner;

GRANT
SELECT
,
    INSERT,
UPDATE ON app.password_resets TO workouts_security_owner;

GRANT
SELECT
    (principal_id, revoked_at) ON app.sessions TO workouts_security_owner;

GRANT
UPDATE (revoked_at) ON app.sessions TO workouts_security_owner;

GRANT INSERT ON app.audit_records TO workouts_security_owner;

REVOKE ALL ON app.authentication_principals,
app.administrators,
app.accounts,
app.users,
app.preferences,
app.sessions,
app.invitations,
app.invitation_tokens,
app.password_resets,
app.rate_limits,
app.audit_records
FROM
    PUBLIC,
    workouts_worker;

REVOKE ALL ON FUNCTION app.enforce_principal_role_consistency (),
app.consume_rate_limit (text, text, bytea),
app.issue_password_reset (text, boolean, bytea),
app.complete_password_reset (bytea, text, uuid, text),
app.cleanup_rate_limits ()
FROM
    PUBLIC,
    workouts_worker;

GRANT
SELECT
    (
        id,
        role,
        username,
        canonical_username,
        email,
        canonical_email,
        password_hash,
        full_name,
        disabled_at
    ) ON app.authentication_principals TO workouts_api;

GRANT INSERT (
    id,
    role,
    username,
    canonical_username,
    email,
    canonical_email,
    canonicalization_version,
    password_hash,
    full_name
) ON app.authentication_principals TO workouts_api;

GRANT
UPDATE (password_hash, full_name, updated_at) ON app.authentication_principals TO workouts_api;

GRANT
SELECT
    (id, state) ON app.accounts TO workouts_api;

GRANT INSERT (id) ON app.accounts TO workouts_api;

GRANT
SELECT
    (principal_id, account_id),
    INSERT (principal_id, account_id) ON app.users TO workouts_api;

GRANT
SELECT
,
    INSERT,
UPDATE ON app.preferences TO workouts_api;

GRANT
SELECT
    (id, principal_id, credential_kind, credential_verifier, csrf_token, expires_at, revoked_at),
    INSERT (id, principal_id, credential_kind, credential_verifier, csrf_token, expires_at),
UPDATE (revoked_at) ON app.sessions TO workouts_api;

GRANT
SELECT
    (id, email, canonical_email, state, created_by, accepted_by, accepted_at, revoked_at, created_at),
    INSERT (id, email, canonical_email, created_by),
UPDATE (state, accepted_by, accepted_at) ON app.invitations TO workouts_api;

GRANT
SELECT
    (
        invitation_id,
        verifier,
        issued_at,
        expires_at,
        used_at,
        revoked_at,
        replaced_at,
        delivery_state,
        delivery_category
    ),
    INSERT (invitation_id, verifier),
UPDATE (used_at, delivery_state, delivery_category) ON app.invitation_tokens TO workouts_api;

GRANT
SELECT
    (
        principal_id,
        verifier,
        issued_at,
        expires_at,
        used_at,
        revoked_at,
        replaced_at,
        delivery_state,
        delivery_category
    ),
UPDATE (delivery_state, delivery_category) ON app.password_resets TO workouts_api;

GRANT INSERT (id, actor_principal_id, account_id, action, target_type, target_id, request_id) ON app.audit_records TO workouts_api;

GRANT
EXECUTE ON FUNCTION app.consume_rate_limit (text, text, bytea),
app.issue_password_reset (text, boolean, bytea),
app.complete_password_reset (bytea, text, uuid, text),
app.cleanup_rate_limits () TO workouts_api;

-- Account deletion foundation. PL/pgSQL references to later source/ingest/
-- Coverage routines resolve only when called on the complete runtime schema.
CREATE TABLE app.account_deletions (
    job_id uuid PRIMARY KEY REFERENCES app.jobs(id) ON DELETE CASCADE,
    target_account_id uuid NOT NULL UNIQUE,
    target_principal_id uuid NOT NULL UNIQUE,
    requested_by uuid NOT NULL REFERENCES app.administrators(principal_id),
    phase text NOT NULL DEFAULT 'draining' CHECK (phase IN ('draining','purging','completed')),
    table_index integer NOT NULL DEFAULT 1,
    rows_purged bigint NOT NULL DEFAULT 0,
    workouts_total bigint,
    routes_total bigint,
    sources_total bigint,
    subject_digests bytea[] NOT NULL DEFAULT '{}',
    last_error_code text,
    next_attempt_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    completed_at timestamptz
);
CREATE TABLE app.account_purge_capabilities (
    backend_pid integer NOT NULL,
    transaction_id bigint NOT NULL,
    target_account_id uuid NOT NULL,
    purge_allowed boolean NOT NULL DEFAULT true,
    PRIMARY KEY(backend_pid,transaction_id)
);
CREATE TABLE app.private_staging_receipts (
    account_id uuid NOT NULL,
    job_id uuid NOT NULL,
    lease_token uuid NOT NULL,
    worker_id text NOT NULL,
    pod_name text NOT NULL,
    pod_uid text NOT NULL,
    root_kind text NOT NULL CHECK (root_kind IN ('private','connection-checks')),
    created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY(job_id,lease_token)
);
CREATE INDEX private_staging_receipts_account_idx ON app.private_staging_receipts(account_id);
ALTER TABLE app.account_deletions ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.account_deletions FORCE ROW LEVEL SECURITY;
ALTER TABLE app.account_purge_capabilities ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.account_purge_capabilities FORCE ROW LEVEL SECURITY;
ALTER TABLE app.private_staging_receipts ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.private_staging_receipts FORCE ROW LEVEL SECURITY;
CREATE POLICY account_deletions_owner ON app.account_deletions TO workouts_security_owner USING(true) WITH CHECK(true);
CREATE POLICY account_purge_capabilities_owner ON app.account_purge_capabilities TO workouts_security_owner USING(true) WITH CHECK(true);
CREATE POLICY private_staging_receipts_owner ON app.private_staging_receipts TO workouts_security_owner USING(true) WITH CHECK(true);
CREATE POLICY jobs_account_deletion_owner ON app.jobs TO workouts_security_owner
    USING(kind='account_deletion') WITH CHECK(kind='account_deletion');
REVOKE ALL ON app.account_deletions,app.account_purge_capabilities,app.private_staging_receipts FROM PUBLIC,workouts_api,workouts_worker,workouts_coverage_worker,workouts_tiles;
GRANT SELECT,INSERT,UPDATE,DELETE ON app.account_deletions,app.account_purge_capabilities,app.private_staging_receipts TO workouts_security_owner;

-- A private, transaction-bound capability, never a forgeable application GUC.
-- +goose StatementBegin
CREATE FUNCTION app.account_purge_active() RETURNS boolean
LANGUAGE sql STABLE SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
    SELECT EXISTS(SELECT 1 FROM app.account_purge_capabilities
        WHERE backend_pid=pg_backend_pid() AND transaction_id=txid_current() AND purge_allowed);
$function$;
-- +goose StatementEnd

-- API mutations authorized just before revocation must serialize with acceptance.
-- Worker cleanup remains governed by existing lease/cancellation capabilities.
-- +goose StatementBegin
CREATE FUNCTION app.require_active_api_account() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
BEGIN
    IF session_user='workouts_api' AND NEW.account_id IS NOT NULL AND NOT EXISTS(
        SELECT 1 FROM app.account_purge_capabilities WHERE backend_pid=pg_backend_pid()
          AND transaction_id=txid_current() AND target_account_id=NEW.account_id) THEN
        PERFORM 1 FROM app.accounts WHERE id=NEW.account_id AND state='active' FOR KEY SHARE;
        IF NOT FOUND THEN RAISE EXCEPTION 'account is not active' USING ERRCODE='42501'; END IF;
    END IF;
    RETURN NEW;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.account_purge_tables() RETURNS text[]
LANGUAGE sql IMMUTABLE SET search_path=pg_catalog AS $function$
    SELECT ARRAY['private_staging_receipts','account_data_generations','account_park_all_time','account_park_daily_rollups','account_path_all_time','account_path_daily_rollups','account_sync_schedules','adopted_coverage_handoff_receipts','adopted_workout_coverage_refreshes','coverage_applied_match_proofs','coverage_diagnostic_evidence','coverage_diagnostic_overall_labels','coverage_diagnostic_runs','coverage_diagnostic_segment_labels','coverage_display_dirty','coverage_display_entities','coverage_display_invalidations','coverage_display_members','coverage_display_revisions','coverage_display_visits','coverage_job_contexts','coverage_job_progress','coverage_parks','coverage_reconciliation_accounts','coverage_reconciliation_wakes','coverage_retry_attempts','coverage_retry_roots','coverage_route_changes','coverage_route_job_contexts','coverage_route_selection_proofs','coverage_tile_chunks','ingest_coverage_builders','ingest_coverage_handoffs','ingest_coverage_prepared','ingest_file_slots','ingest_write_capabilities','job_config_snapshots','job_events','job_file_candidate_sets','job_file_candidates','job_logs','job_progress','job_source_contexts','map_selection_workouts','map_selections','matcher_slots','notifications','preferences','raw_route_lod_chunks','raw_route_tile_chunks','source_objects','source_sync_state','stats_report_scopes','workout_adoption_capabilities','workout_aggregates','workout_coverage_regions','workout_coverage_states','workout_deletion_capabilities','workout_import_events','workout_park_attributions','workout_park_display_bounds','workout_path_attributions','workout_path_display_context','workout_path_display_partials','workout_route_points','workout_routes','workout_segment_matches','workout_timezone_write_capabilities','coverage_diagnostic_generations','coverage_display_contexts','path_segments','workouts','coverage_paths','source_files','workout_deletion_targets','workout_types','jobs','sources']::text[];
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.start_account_deletion(admin_session uuid,target_principal uuid,new_job uuid,new_audit uuid,request_value text,digests bytea[])
RETURNS uuid LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
DECLARE actor uuid; target_account uuid; account_state text; item record; existing_job uuid;
    old_account text:=current_setting('app.account_id',true); old_session text:=current_setting('app.session_id',true);
BEGIN
    SELECT p.id INTO actor FROM app.sessions s JOIN app.authentication_principals p ON p.id=s.principal_id
    WHERE s.id=admin_session AND p.role='administrator' AND p.disabled_at IS NULL AND s.revoked_at IS NULL
      AND s.expires_at>clock_timestamp() FOR SHARE OF s,p;
    IF actor IS NULL THEN RAISE EXCEPTION 'administrator session required' USING ERRCODE='42501'; END IF;
    SELECT u.account_id INTO target_account FROM app.users u JOIN app.authentication_principals p ON p.id=u.principal_id
    WHERE p.id=target_principal AND p.role='user';
    IF target_account IS NULL THEN RETURN NULL; END IF;
    SELECT state INTO account_state FROM app.accounts WHERE id=target_account FOR UPDATE;
    SELECT job_id INTO existing_job FROM app.account_deletions WHERE target_account_id=target_account;
    IF FOUND THEN
        UPDATE app.account_deletions SET next_attempt_at=clock_timestamp() WHERE job_id=existing_job AND phase<>'completed';
        RETURN existing_job;
    END IF;
    IF account_state NOT IN ('active','disabled') THEN RAISE EXCEPTION 'account cannot be deleted' USING ERRCODE='55000'; END IF;
    PERFORM 1 FROM app.authentication_principals WHERE id=target_principal FOR UPDATE;
    UPDATE app.accounts SET state='deleting' WHERE id=target_account;
    UPDATE app.authentication_principals SET disabled_at=coalesce(disabled_at,clock_timestamp()),updated_at=clock_timestamp() WHERE id=target_principal;
    UPDATE app.sessions SET revoked_at=clock_timestamp() WHERE principal_id=target_principal AND revoked_at IS NULL;
    INSERT INTO app.account_purge_capabilities(backend_pid,transaction_id,target_account_id,purge_allowed)
    VALUES(pg_backend_pid(),txid_current(),target_account,false);
    PERFORM set_config('app.account_id',target_account::text,true);
    PERFORM set_config('app.session_id','',true);
    UPDATE app.account_sync_schedules SET lease_worker=NULL,lease_token=NULL,lease_expires_at=NULL,lease_runtime_version=NULL,updated_at=clock_timestamp() WHERE account_id=target_account;
    FOR item IN SELECT id,kind FROM app.jobs WHERE account_id=target_account AND status IN ('queued','running') ORDER BY (parent_job_id IS NOT NULL),id LOOP
        IF item.kind='coverage_update' THEN PERFORM app.request_coverage_job_cancellation(item.id,actor);
        ELSE PERFORM app.request_job_cancellation(item.id,actor); END IF;
    END LOOP;
    INSERT INTO app.jobs(id,administrator_id,kind,priority,parameters)
    VALUES(new_job,actor,'account_deletion',100,jsonb_build_object('targetAccountId',upper(replace(target_account::text,'-',''))));
    INSERT INTO app.account_deletions(job_id,target_account_id,target_principal_id,requested_by,subject_digests)
    VALUES(new_job,target_account,target_principal,actor,coalesce(digests,'{}'));
    INSERT INTO app.audit_records(id,actor_principal_id,action,target_type,target_id,request_id)
    VALUES(new_audit,actor,'account.deletion_requested','account',target_account,request_value);
    SET CONSTRAINTS ALL IMMEDIATE;
    DELETE FROM app.account_purge_capabilities WHERE backend_pid=pg_backend_pid() AND transaction_id=txid_current();
    PERFORM set_config('app.account_id',coalesce(old_account,''),true);
    PERFORM set_config('app.session_id',coalesce(old_session,''),true);
    RETURN new_job;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.claim_account_deletion(claiming_worker text,new_lease uuid,runtime_version integer)
RETURNS uuid LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
DECLARE target uuid; state_value text;
BEGIN
    IF runtime_version<32 OR NOT EXISTS(SELECT 1 FROM app.schema_metadata WHERE singleton AND minimum_runtime_version<=runtime_version) THEN RETURN NULL; END IF;
    SELECT j.id,j.status INTO target,state_value FROM app.jobs j JOIN app.account_deletions d ON d.job_id=j.id
    WHERE j.kind='account_deletion' AND d.phase<>'completed' AND d.next_attempt_at<=clock_timestamp()
      AND (j.status='queued' OR (j.status='running' AND j.lease_expires_at<clock_timestamp()))
    ORDER BY j.created_at,j.id FOR UPDATE OF j SKIP LOCKED LIMIT 1;
    IF target IS NULL THEN RETURN NULL; END IF;
    IF state_value='running' THEN
        PERFORM set_config('app.job_transition','recover',true);
        UPDATE app.jobs SET status='queued',worker_id=NULL,lease_token=NULL,claimed_at=NULL,heartbeat_at=NULL,lease_expires_at=NULL WHERE id=target;
    END IF;
    PERFORM set_config('app.job_transition','claim',true);
    UPDATE app.jobs SET status='running',attempt=attempt+1,worker_id=claiming_worker,lease_token=new_lease,
      claimed_at=clock_timestamp(),heartbeat_at=clock_timestamp(),lease_expires_at=clock_timestamp()+interval '3 minutes',started_at=coalesce(started_at,clock_timestamp()) WHERE id=target;
    RETURN target;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.release_account_deletion(target_job uuid,claiming_worker text,current_lease uuid,error_code text)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
BEGIN
    PERFORM 1 FROM app.jobs WHERE id=target_job AND kind='account_deletion' AND status='running'
      AND worker_id=claiming_worker AND lease_token=current_lease FOR UPDATE;
    IF NOT FOUND THEN RETURN false; END IF;
    UPDATE app.account_deletions SET last_error_code=error_code,next_attempt_at=clock_timestamp()+CASE WHEN error_code IS NULL THEN interval '1 second' ELSE interval '10 seconds' END WHERE job_id=target_job;
    PERFORM set_config('app.job_transition','recover',true);
    UPDATE app.jobs SET status='queued',worker_id=NULL,lease_token=NULL,claimed_at=NULL,heartbeat_at=NULL,lease_expires_at=NULL WHERE id=target_job;
    RETURN true;
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.register_private_execution(target_job uuid,claiming_worker text,current_lease uuid,pod text,pod_identity text,root text)
RETURNS boolean LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
DECLARE target_account uuid;
BEGIN
    SELECT account_id INTO target_account FROM app.jobs WHERE id=target_job;
    IF target_account IS NULL THEN RETURN false; END IF;
    PERFORM set_config('app.account_id',target_account::text,true);
    SELECT account_id INTO target_account FROM app.jobs WHERE id=target_job AND status='running'
      AND worker_id=claiming_worker AND lease_token=current_lease AND lease_expires_at>clock_timestamp() FOR UPDATE;
    IF target_account IS NULL OR root NOT IN ('private','connection-checks') OR pod='' OR pod_identity='' THEN RETURN false; END IF;
    INSERT INTO app.private_staging_receipts(account_id,job_id,lease_token,worker_id,pod_name,pod_uid,root_kind)
    VALUES(target_account,target_job,current_lease,claiming_worker,pod,pod_identity,root) ON CONFLICT DO NOTHING;
    RETURN true;
END;
$function$;
-- +goose StatementEnd

-- Worker attests successful local Close/scavenging; only trusted worker role can call.
-- +goose StatementBegin
CREATE FUNCTION app.clear_private_execution(claiming_worker text,target_job uuid,current_lease uuid)
RETURNS void LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
    DELETE FROM app.private_staging_receipts WHERE worker_id=claiming_worker
      AND (target_job IS NULL OR (job_id=target_job AND lease_token=current_lease));
$function$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION app.ack_private_staging_root(pod text,pod_identity text,root text)
RETURNS void LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
    DELETE FROM app.private_staging_receipts WHERE pod_name=pod AND pod_uid=pod_identity AND root_kind=root;
$function$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION app.account_deletion_staging(target_job uuid,claiming_worker text,current_lease uuid)
RETURNS TABLE(pod_name text,pod_uid text) LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
    SELECT DISTINCT r.pod_name,r.pod_uid FROM app.private_staging_receipts r
    JOIN app.account_deletions d ON d.target_account_id=r.account_id JOIN app.jobs j ON j.id=d.job_id
    WHERE j.id=target_job AND j.kind='account_deletion' AND j.status='running' AND j.worker_id=claiming_worker AND j.lease_token=current_lease;
$function$;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE FUNCTION app.ack_disposed_staging_pod(target_job uuid,claiming_worker text,current_lease uuid,disposed_pod text,disposed_uid text)
RETURNS void LANGUAGE sql SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
    DELETE FROM app.private_staging_receipts r USING app.account_deletions d,app.jobs j
    WHERE r.account_id=d.target_account_id AND d.job_id=j.id AND j.id=target_job AND j.kind='account_deletion'
      AND j.status='running' AND j.worker_id=claiming_worker AND j.lease_token=current_lease
      AND r.pod_name=disposed_pod AND r.pod_uid=disposed_uid;
$function$;
-- +goose StatementEnd

-- Each call commits one bounded batch. FK enforcement stays enabled throughout.
-- +goose StatementBegin
CREATE FUNCTION app.step_account_deletion(target_job uuid,claiming_worker text,current_lease uuid)
RETURNS text LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
DECLARE d app.account_deletions%ROWTYPE; tables text[]:=app.account_purge_tables(); name text;
    affected bigint; remaining boolean; actual text[]; expected text[]; target_email text; expired record;
BEGIN
    PERFORM 1 FROM app.jobs WHERE id=target_job AND kind='account_deletion' AND status='running'
      AND worker_id=claiming_worker AND lease_token=current_lease AND lease_expires_at>clock_timestamp() FOR UPDATE;
    IF NOT FOUND THEN RETURN 'lease-lost'; END IF;
    SELECT * INTO d FROM app.account_deletions WHERE job_id=target_job FOR UPDATE;
    IF d.phase='completed' THEN RETURN 'completed'; END IF;
    PERFORM 1 FROM app.accounts WHERE id=d.target_account_id AND state='deleting' FOR UPDATE;
    IF NOT FOUND THEN RAISE EXCEPTION 'deleting account required' USING ERRCODE='55000'; END IF;
    PERFORM set_config('app.account_id',d.target_account_id::text,true);
    PERFORM set_config('app.session_id','',true);
    FOR expired IN SELECT id FROM app.jobs WHERE account_id=d.target_account_id AND status='running'
      AND lease_expires_at<clock_timestamp() ORDER BY id LOOP
        PERFORM app.recover_expired_job(expired.id);
    END LOOP;
    IF EXISTS(SELECT 1 FROM app.jobs WHERE account_id=d.target_account_id AND status IN ('queued','running')) THEN RETURN 'waiting-jobs'; END IF;
    IF EXISTS(SELECT 1 FROM app.private_staging_receipts WHERE account_id=d.target_account_id) THEN RETURN 'waiting-staging'; END IF;
    SELECT array_agg(c.relname::text ORDER BY c.relname) INTO actual FROM pg_class c JOIN pg_attribute a ON a.attrelid=c.oid
    WHERE c.relnamespace='app'::regnamespace AND c.relkind IN ('r','p') AND a.attname='account_id' AND NOT a.attisdropped;
    SELECT array_agg(t ORDER BY t) INTO expected FROM unnest(tables||ARRAY['audit_records','users'])t WHERE t<>'coverage_retry_attempts';
    IF actual IS DISTINCT FROM expected THEN RAISE EXCEPTION 'account purge inventory changed' USING ERRCODE='55000'; END IF;
    IF d.phase='draining' THEN
        UPDATE app.account_deletions SET phase='purging',last_error_code=NULL,
          workouts_total=(SELECT count(*) FROM app.workouts WHERE account_id=d.target_account_id),
          routes_total=(SELECT count(*) FROM app.workout_routes WHERE account_id=d.target_account_id),
          sources_total=(SELECT count(*) FROM app.sources WHERE account_id=d.target_account_id) WHERE job_id=target_job;
    END IF;
    INSERT INTO app.account_purge_capabilities(backend_pid,transaction_id,target_account_id) VALUES(pg_backend_pid(),txid_current(),d.target_account_id);
    PERFORM set_config('app.account_id',d.target_account_id::text,true);
    PERFORM set_config('app.session_id','',true);
    IF d.table_index<=cardinality(tables) THEN
        name:=tables[d.table_index];
        IF name='coverage_retry_attempts' THEN
            DELETE FROM app.coverage_retry_attempts WHERE job_id IN (SELECT id FROM app.jobs WHERE account_id=d.target_account_id);
            GET DIAGNOSTICS affected=ROW_COUNT;
        ELSIF name='jobs' THEN
            DELETE FROM app.jobs WHERE id IN (SELECT j.id FROM app.jobs j WHERE j.account_id=d.target_account_id
              AND NOT EXISTS(SELECT 1 FROM app.jobs child WHERE child.parent_job_id=j.id OR child.retry_of_job_id=j.id) LIMIT 5000);
            GET DIAGNOSTICS affected=ROW_COUNT;
        ELSE
            EXECUTE format('DELETE FROM app.%I WHERE account_id=$1 AND (tableoid,ctid) IN (SELECT tableoid,ctid FROM app.%I WHERE account_id=$1 LIMIT 5000)',name,name) USING d.target_account_id;
            GET DIAGNOSTICS affected=ROW_COUNT;
        END IF;
        IF name='coverage_retry_attempts' THEN remaining:=false;
        ELSE EXECUTE format('SELECT EXISTS(SELECT 1 FROM app.%I WHERE account_id=$1)',name) INTO remaining USING d.target_account_id; END IF;
        IF name='jobs' AND affected=0 AND remaining THEN RAISE EXCEPTION 'private job lineage cannot be purged' USING ERRCODE='55000'; END IF;
        UPDATE app.account_deletions SET rows_purged=rows_purged+affected,table_index=table_index+CASE WHEN remaining THEN 0 ELSE 1 END,last_error_code=NULL WHERE job_id=target_job;
        SET CONSTRAINTS ALL IMMEDIATE;
        DELETE FROM app.account_purge_capabilities WHERE backend_pid=pg_backend_pid() AND transaction_id=txid_current();
        RETURN 'purging';
    END IF;
    -- Verify every private scope again before releasing identity keys.
    FOREACH name IN ARRAY tables LOOP
        IF name='coverage_retry_attempts' THEN CONTINUE; END IF;
        EXECUTE format('SELECT EXISTS(SELECT 1 FROM app.%I WHERE account_id=$1)',name) INTO remaining USING d.target_account_id;
        IF remaining THEN RAISE EXCEPTION 'account purge verification failed' USING ERRCODE='55000'; END IF;
    END LOOP;
    SELECT canonical_email INTO target_email FROM app.authentication_principals WHERE id=d.target_principal_id AND role='user' FOR UPDATE;
    IF target_email IS NULL THEN RAISE EXCEPTION 'user principal required' USING ERRCODE='55000'; END IF;
    DELETE FROM app.invitations WHERE accepted_by=d.target_principal_id OR canonical_email=target_email;
    DELETE FROM app.sessions WHERE principal_id=d.target_principal_id;
    DELETE FROM app.password_resets WHERE principal_id=d.target_principal_id;
    DELETE FROM app.rate_limits WHERE key_kind='subject' AND key_digest=ANY(d.subject_digests);
    UPDATE app.audit_records SET actor_principal_id=NULL WHERE actor_principal_id=d.target_principal_id;
    UPDATE app.audit_records SET account_id=NULL WHERE account_id=d.target_account_id;
    DELETE FROM app.users WHERE principal_id=d.target_principal_id AND account_id=d.target_account_id;
    DELETE FROM app.authentication_principals WHERE id=d.target_principal_id AND role='user';
    DELETE FROM app.accounts WHERE id=d.target_account_id AND state='deleting';
    SET CONSTRAINTS ALL IMMEDIATE;
    DELETE FROM app.account_purge_capabilities WHERE backend_pid=pg_backend_pid() AND transaction_id=txid_current();
    UPDATE app.account_deletions SET phase='completed',completed_at=clock_timestamp(),subject_digests='{}',last_error_code=NULL WHERE job_id=target_job;
    INSERT INTO app.audit_records(id,actor_principal_id,action,target_type,target_id)
    VALUES(gen_random_uuid(),d.requested_by,'account.deletion_completed','account',d.target_account_id);
    PERFORM set_config('app.job_transition','finish',true);
    UPDATE app.jobs SET status='succeeded',terminal_at=clock_timestamp(),worker_id=NULL,lease_token=NULL,
      claimed_at=NULL,heartbeat_at=NULL,lease_expires_at=NULL WHERE id=target_job;
    RETURN 'completed';
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.admin_account_deletion(admin_session uuid,target_principal uuid)
RETURNS TABLE(job_id uuid,phase text,rows_purged bigint,workouts_total bigint,routes_total bigint,sources_total bigint,last_error_code text,completed_at timestamptz)
LANGUAGE plpgsql SECURITY DEFINER SET search_path=pg_catalog,app AS $function$
BEGIN
    IF NOT EXISTS(SELECT 1 FROM app.sessions s JOIN app.authentication_principals p ON p.id=s.principal_id WHERE s.id=admin_session AND p.role='administrator' AND p.disabled_at IS NULL AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp()) THEN RAISE EXCEPTION 'administrator session required' USING ERRCODE='42501'; END IF;
    RETURN QUERY SELECT d.job_id,d.phase,d.rows_purged,d.workouts_total,d.routes_total,d.sources_total,d.last_error_code,d.completed_at FROM app.account_deletions d WHERE d.target_principal_id=target_principal;
END;
$function$;
-- +goose StatementEnd


-- +goose StatementBegin
DO $block$
DECLARE item record;
BEGIN
    GRANT CREATE ON SCHEMA app TO workouts_security_owner;
    FOR item IN SELECT p.oid::regprocedure signature FROM pg_proc p WHERE p.pronamespace='app'::regnamespace AND p.proname IN
      ('account_purge_active','require_active_api_account','account_purge_tables','start_account_deletion','claim_account_deletion','release_account_deletion','register_private_execution','clear_private_execution','ack_private_staging_root','account_deletion_staging','ack_disposed_staging_pod','step_account_deletion','admin_account_deletion')
    LOOP
        EXECUTE format('ALTER FUNCTION %s OWNER TO workouts_security_owner',item.signature);
        EXECUTE format('REVOKE ALL ON FUNCTION %s FROM PUBLIC,workouts_api,workouts_worker,workouts_coverage_worker,workouts_tiles',item.signature);
    END LOOP;
    REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
END;
$block$;
-- +goose StatementEnd
GRANT EXECUTE ON FUNCTION app.account_purge_active() TO workouts_api,workouts_worker,workouts_coverage_worker;
GRANT EXECUTE ON FUNCTION app.start_account_deletion(uuid,uuid,uuid,uuid,text,bytea[]),app.admin_account_deletion(uuid,uuid) TO workouts_api;
GRANT EXECUTE ON FUNCTION app.claim_account_deletion(text,uuid,integer),app.release_account_deletion(uuid,text,uuid,text),
app.register_private_execution(uuid,text,uuid,text,text,text),app.clear_private_execution(text,uuid,uuid),
app.ack_private_staging_root(text,text,text),app.account_deletion_staging(uuid,text,uuid),app.ack_disposed_staging_pod(uuid,text,uuid,text,text),app.step_account_deletion(uuid,text,uuid) TO workouts_worker;


-- Account suspension reuses the later source/Coverage cancellation engines.
-- Their PL/pgSQL calls resolve when invoked on the complete runtime schema.
GRANT UPDATE(disabled_at) ON app.authentication_principals TO workouts_security_owner;

-- Password work happens outside the transaction. Recheck its hash and retain
-- account/principal locks through session issuance, in lifecycle lock order.
-- +goose StatementBegin
CREATE FUNCTION app.lock_signin_principal(target_principal uuid,expected_hash text) RETURNS text
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
DECLARE target_account uuid; target_state text; target_role text; disabled timestamptz;
BEGIN
    SELECT account_id INTO target_account FROM app.users WHERE principal_id=target_principal;
    IF target_account IS NOT NULL THEN
        SELECT state INTO target_state FROM app.accounts WHERE id=target_account FOR KEY SHARE;
    END IF;
    SELECT role,disabled_at INTO target_role,disabled FROM app.authentication_principals
    WHERE id=target_principal AND password_hash=expected_hash FOR UPDATE;
    IF NOT FOUND THEN RETURN 'unavailable'; END IF;
    IF target_role='user' AND target_account IS NOT NULL AND target_state IN ('active','disabled') THEN
        RETURN CASE WHEN disabled IS NOT NULL OR target_state='disabled' THEN 'disabled' ELSE 'active' END;
    END IF;
    IF target_role='administrator' AND disabled IS NULL THEN RETURN 'active'; END IF;
    RETURN 'unavailable';
END;
$function$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.set_account_access(admin_session uuid,target_principal uuid,enable_account boolean,
    audit_id uuid,request_id_value text) RETURNS text
LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
DECLARE
    actor uuid;
    target_account uuid;
    target_state text;
    target_job record;
    previous_account text := current_setting('app.account_id',true);
    previous_session text := current_setting('app.session_id',true);
    previous_transition text := current_setting('app.job_transition',true);
BEGIN
    IF enable_account IS NULL OR audit_id IS NULL THEN
        RAISE EXCEPTION 'account access arguments required' USING ERRCODE='22023';
    END IF;
    SELECT p.id INTO actor FROM app.sessions s JOIN app.authentication_principals p ON p.id=s.principal_id
    WHERE s.id=admin_session AND p.role='administrator' AND p.disabled_at IS NULL
        AND s.revoked_at IS NULL AND s.expires_at>clock_timestamp() FOR SHARE OF s,p;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'administrator session required' USING ERRCODE='42501';
    END IF;
    SELECT u.account_id INTO target_account FROM app.users u
    JOIN app.authentication_principals p ON p.id=u.principal_id
    WHERE p.id=target_principal AND p.role='user';
    IF NOT FOUND THEN RETURN 'not_found'; END IF;
    -- Match enqueue/sign-in ordering. FOR UPDATE also fences new job FK locks.
    SELECT state INTO target_state FROM app.accounts WHERE id=target_account FOR UPDATE;
    IF target_state NOT IN ('active','disabled') THEN RETURN 'conflict'; END IF;
    PERFORM 1 FROM app.authentication_principals WHERE id=target_principal FOR UPDATE;
    INSERT INTO app.account_purge_capabilities(backend_pid,transaction_id,target_account_id,purge_allowed) VALUES(pg_backend_pid(),txid_current(),target_account,false);
    UPDATE app.accounts SET state=CASE WHEN enable_account THEN 'active' ELSE 'disabled' END
    WHERE id=target_account;
    UPDATE app.authentication_principals
    SET disabled_at=CASE WHEN enable_account THEN NULL ELSE COALESCE(disabled_at,clock_timestamp()) END,
        updated_at=clock_timestamp() WHERE id=target_principal;
    IF NOT enable_account THEN
        UPDATE app.sessions SET revoked_at=clock_timestamp()
        WHERE principal_id=target_principal AND revoked_at IS NULL;
        PERFORM set_config('app.account_id',target_account::text,true);
        PERFORM set_config('app.session_id','',true);
        -- Invalidate a scheduler that read sources before suspension, even if
        -- the account is re-enabled before that scheduler finishes.
        UPDATE app.account_sync_schedules SET lease_worker=NULL,lease_token=NULL,
            lease_expires_at=NULL,lease_runtime_version=NULL,updated_at=clock_timestamp()
        WHERE account_id=target_account;
        FOR target_job IN SELECT id,kind FROM app.jobs
            WHERE account_id=target_account AND status IN ('queued','running')
            ORDER BY (parent_job_id IS NOT NULL),id
        LOOP
            IF target_job.kind='coverage_update' THEN
                PERFORM app.request_coverage_job_cancellation(target_job.id,actor);
            ELSE
                PERFORM app.request_job_cancellation(target_job.id,actor);
            END IF;
        END LOOP;
    END IF;
    INSERT INTO app.audit_records(id,actor_principal_id,account_id,action,target_type,target_id,request_id)
    VALUES(audit_id,actor,target_account,CASE WHEN enable_account THEN 'account.enabled' ELSE 'account.disabled' END,
        'principal',target_principal,request_id_value);
    SET CONSTRAINTS ALL IMMEDIATE;
    DELETE FROM app.account_purge_capabilities WHERE backend_pid=pg_backend_pid() AND transaction_id=txid_current();
    PERFORM set_config('app.account_id',COALESCE(previous_account,''),true);
    PERFORM set_config('app.session_id',COALESCE(previous_session,''),true);
    PERFORM set_config('app.job_transition',COALESCE(previous_transition,''),true);
    RETURN CASE WHEN enable_account THEN 'active' ELSE 'disabled' END;
EXCEPTION WHEN OTHERS THEN
    SET CONSTRAINTS ALL IMMEDIATE;
    DELETE FROM app.account_purge_capabilities WHERE backend_pid=pg_backend_pid() AND transaction_id=txid_current();
    PERFORM set_config('app.account_id',COALESCE(previous_account,''),true);
    PERFORM set_config('app.session_id',COALESCE(previous_session,''),true);
    PERFORM set_config('app.job_transition',COALESCE(previous_transition,''),true);
    RAISE;
END;
$function$;
-- +goose StatementEnd

-- Protect enqueues whose HTTP authorization preceded suspension. This does not
-- affect terminal updates and cleanup by already-running workers.
-- +goose StatementBegin
CREATE FUNCTION app.require_active_job_account() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
BEGIN
    IF NEW.account_id IS NOT NULL THEN
        PERFORM 1 FROM app.accounts WHERE id=NEW.account_id AND state='active' FOR KEY SHARE;
        IF NOT FOUND THEN
            RAISE EXCEPTION 'account is not active' USING ERRCODE='42501';
        END IF;
    END IF;
    RETURN NEW;
END;
$function$;
-- +goose StatementEnd
CREATE TRIGGER jobs_require_active_account BEFORE INSERT ON app.jobs
FOR EACH ROW EXECUTE FUNCTION app.require_active_job_account();

GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.set_account_access(uuid,uuid,boolean,uuid,text) OWNER TO workouts_security_owner;
ALTER FUNCTION app.require_active_job_account() OWNER TO workouts_security_owner;
ALTER FUNCTION app.lock_signin_principal(uuid,text) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
REVOKE ALL ON FUNCTION app.set_account_access(uuid,uuid,boolean,uuid,text),app.require_active_job_account(),app.lock_signin_principal(uuid,text)
FROM PUBLIC,workouts_api,workouts_worker,workouts_coverage_worker,workouts_tiles;
GRANT EXECUTE ON FUNCTION app.set_account_access(uuid,uuid,boolean,uuid,text) TO workouts_api;
GRANT EXECUTE ON FUNCTION app.lock_signin_principal(uuid,text) TO workouts_api;

GRANT UPDATE(state) ON app.accounts TO workouts_security_owner;
GRANT SELECT(id,expires_at) ON app.sessions TO workouts_security_owner;

UPDATE app.schema_metadata
SET
    schema_version = 2,
    minimum_runtime_version = 1;

-- +goose Down
UPDATE app.schema_metadata
SET
    schema_version = 1,
    minimum_runtime_version = 1;

DROP POLICY jobs_account_deletion_owner ON app.jobs;
DROP FUNCTION app.admin_account_deletion(uuid,uuid);
DROP FUNCTION app.step_account_deletion(uuid,text,uuid);
DROP FUNCTION app.ack_disposed_staging_pod(uuid,text,uuid,text,text);
DROP FUNCTION app.account_deletion_staging(uuid,text,uuid);
DROP FUNCTION app.ack_private_staging_root(text,text,text);
DROP FUNCTION app.clear_private_execution(text,uuid,uuid);
DROP FUNCTION app.register_private_execution(uuid,text,uuid,text,text,text);
DROP FUNCTION app.release_account_deletion(uuid,text,uuid,text);
DROP FUNCTION app.claim_account_deletion(text,uuid,integer);
DROP FUNCTION app.start_account_deletion(uuid,uuid,uuid,uuid,text,bytea[]);
DROP FUNCTION app.account_purge_tables();
DROP FUNCTION app.require_active_api_account();
DROP FUNCTION app.account_purge_active();
DROP TABLE app.private_staging_receipts,app.account_purge_capabilities,app.account_deletions;

DROP TRIGGER IF EXISTS jobs_require_active_account ON app.jobs;
DROP FUNCTION IF EXISTS app.require_active_job_account();
DROP FUNCTION IF EXISTS app.lock_signin_principal(uuid,text);
DROP FUNCTION IF EXISTS app.set_account_access(uuid,uuid,boolean,uuid,text);

DROP FUNCTION IF EXISTS app.cleanup_rate_limits ();

DROP FUNCTION IF EXISTS app.complete_password_reset (bytea, text, uuid, text);

DROP FUNCTION IF EXISTS app.issue_password_reset (text, boolean, bytea);

DROP FUNCTION IF EXISTS app.consume_rate_limit (text, text, bytea);

DROP FUNCTION IF EXISTS app.enforce_principal_role_consistency () CASCADE;

DROP TABLE IF EXISTS app.audit_records,
app.rate_limits,
app.password_resets,
app.invitation_tokens,
app.invitations,
app.sessions,
app.preferences,
app.users,
app.accounts,
app.administrators,
app.authentication_principals CASCADE;
