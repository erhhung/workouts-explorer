-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname='workouts_security_owner' AND NOT rolcanlogin AND NOT rolsuper AND NOT rolbypassrls) THEN
        RAISE EXCEPTION 'required safe NOLOGIN role workouts_security_owner is missing';
    END IF;
END
$$;
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
    principal_id uuid PRIMARY KEY REFERENCES app.authentication_principals(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp()
);

CREATE TABLE app.accounts (
    id uuid PRIMARY KEY,
    state text NOT NULL DEFAULT 'active' CHECK (state IN ('active', 'deleting', 'deleted')),
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp()
);

CREATE TABLE app.users (
    principal_id uuid PRIMARY KEY REFERENCES app.authentication_principals(id) ON DELETE RESTRICT,
    account_id uuid NOT NULL UNIQUE REFERENCES app.accounts(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp()
);

-- +goose StatementBegin
CREATE FUNCTION app.enforce_principal_role_consistency()
RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $$
DECLARE
    target_id uuid;
    target_role text;
    administrator_exists boolean;
    user_exists boolean;
BEGIN
    IF TG_TABLE_NAME = 'authentication_principals' THEN
        target_id := CASE WHEN TG_OP = 'DELETE' THEN OLD.id ELSE NEW.id END;
    ELSE
        target_id := CASE WHEN TG_OP = 'DELETE' THEN OLD.principal_id ELSE NEW.principal_id END;
    END IF;
    SELECT role INTO target_role FROM app.authentication_principals WHERE id = target_id;
    IF NOT FOUND THEN RETURN NULL; END IF;
    SELECT EXISTS(SELECT 1 FROM app.administrators WHERE principal_id=target_id),
           EXISTS(SELECT 1 FROM app.users WHERE principal_id=target_id)
      INTO administrator_exists, user_exists;
    IF (target_role = 'administrator' AND (NOT administrator_exists OR user_exists)) OR
       (target_role = 'user' AND (administrator_exists OR NOT user_exists)) THEN
        RAISE EXCEPTION 'principal role is inconsistent with identity subtype' USING ERRCODE='23514';
    END IF;
    RETURN NULL;
END;
$$;
-- +goose StatementEnd

CREATE CONSTRAINT TRIGGER authentication_principals_role_consistency
AFTER INSERT OR UPDATE OF role ON app.authentication_principals
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION app.enforce_principal_role_consistency();
CREATE CONSTRAINT TRIGGER administrators_role_consistency
AFTER INSERT OR UPDATE OR DELETE ON app.administrators
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION app.enforce_principal_role_consistency();
CREATE CONSTRAINT TRIGGER users_role_consistency
AFTER INSERT OR UPDATE OR DELETE ON app.users
DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION app.enforce_principal_role_consistency();

CREATE TABLE app.preferences (
    account_id uuid PRIMARY KEY REFERENCES app.accounts(id) ON DELETE CASCADE,
    theme text NOT NULL DEFAULT 'dark' CHECK (theme IN ('dark', 'light')),
    units text NOT NULL DEFAULT 'imperial' CHECK (units IN ('imperial', 'metric')),
    timezone text NOT NULL DEFAULT 'UTC',
    first_weekday text NOT NULL DEFAULT 'monday' CHECK (first_weekday IN ('monday', 'sunday')),
    clock_format text NOT NULL DEFAULT '12h' CHECK (clock_format IN ('12h', '24h')),
    workout_columns text[] NOT NULL DEFAULT ARRAY['date','type','duration','distance'],
    page_size integer NOT NULL DEFAULT 25 CHECK (page_size >= 10),
    date_range text CHECK (date_range IS NULL OR length(date_range) <= 64),
    updated_at timestamptz NOT NULL DEFAULT transaction_timestamp()
);
ALTER TABLE app.preferences ENABLE ROW LEVEL SECURITY;
ALTER TABLE app.preferences FORCE ROW LEVEL SECURITY;
CREATE POLICY preferences_account_policy ON app.preferences
    USING (account_id = app.current_account_id())
    WITH CHECK (account_id = app.current_account_id());

CREATE TABLE app.sessions (
    id uuid PRIMARY KEY,
    principal_id uuid NOT NULL REFERENCES app.authentication_principals(id) ON DELETE CASCADE,
    credential_kind text NOT NULL CHECK (credential_kind IN ('cookie', 'bearer')),
    credential_verifier bytea NOT NULL UNIQUE CHECK (octet_length(credential_verifier) = 32),
    csrf_token text CHECK ((credential_kind = 'cookie') = (csrf_token IS NOT NULL)),
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    expires_at timestamptz NOT NULL,
    revoked_at timestamptz,
    CHECK (expires_at > created_at)
);
CREATE INDEX sessions_principal_live_idx ON app.sessions(principal_id, expires_at) WHERE revoked_at IS NULL;

CREATE TABLE app.invitations (
    id uuid PRIMARY KEY,
    email text NOT NULL,
    canonical_email text COLLATE "C" NOT NULL,
    state text NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'accepted', 'revoked')),
    created_by uuid NOT NULL REFERENCES app.authentication_principals(id),
    accepted_by uuid REFERENCES app.authentication_principals(id),
    accepted_at timestamptz,
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    CHECK ((state = 'accepted') = (accepted_by IS NOT NULL AND accepted_at IS NOT NULL)),
    CHECK ((state = 'revoked') = (revoked_at IS NOT NULL))
);
CREATE UNIQUE INDEX invitations_pending_email_idx ON app.invitations(canonical_email) WHERE state = 'pending';

CREATE TABLE app.invitation_tokens (
    invitation_id uuid NOT NULL REFERENCES app.invitations(id) ON DELETE CASCADE,
    verifier bytea PRIMARY KEY CHECK (octet_length(verifier) = 32),
    issued_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    expires_at timestamptz NOT NULL DEFAULT transaction_timestamp() + interval '7 days',
    used_at timestamptz,
    revoked_at timestamptz,
    replaced_at timestamptz,
    delivery_state text NOT NULL DEFAULT 'pending' CHECK (delivery_state IN ('pending','delivered','failed','unknown')),
    delivery_category text CHECK (delivery_category IN ('timeout','tls','authentication','rejected','queue_full','interrupted')),
    CHECK (expires_at = issued_at + interval '7 days')
);
CREATE UNIQUE INDEX invitation_tokens_live_idx ON app.invitation_tokens(invitation_id) WHERE used_at IS NULL AND revoked_at IS NULL;

CREATE TABLE app.password_resets (
    principal_id uuid NOT NULL REFERENCES app.authentication_principals(id) ON DELETE CASCADE,
    verifier bytea PRIMARY KEY CHECK (octet_length(verifier) = 32),
    issued_at timestamptz NOT NULL DEFAULT transaction_timestamp(),
    expires_at timestamptz NOT NULL DEFAULT transaction_timestamp() + interval '30 minutes',
    used_at timestamptz,
    revoked_at timestamptz,
    replaced_at timestamptz,
    delivery_state text NOT NULL DEFAULT 'pending' CHECK (delivery_state IN ('pending','delivered','failed','unknown')),
    delivery_category text CHECK (delivery_category IN ('timeout','tls','authentication','rejected','queue_full','interrupted')),
    CHECK (expires_at = issued_at + interval '30 minutes')
);
CREATE UNIQUE INDEX password_resets_live_idx ON app.password_resets(principal_id) WHERE used_at IS NULL AND revoked_at IS NULL;

CREATE TABLE app.rate_limits (
    operation text NOT NULL,
    key_kind text NOT NULL CHECK (key_kind IN ('network', 'subject')),
    key_digest bytea NOT NULL CHECK (octet_length(key_digest) = 32),
    window_start timestamptz NOT NULL,
    window_end timestamptz NOT NULL,
    count integer NOT NULL CHECK (count > 0),
    PRIMARY KEY(operation, key_kind, key_digest, window_start),
    CHECK (window_end > window_start)
);

CREATE TABLE app.audit_records (
    id uuid PRIMARY KEY,
    actor_principal_id uuid REFERENCES app.authentication_principals(id),
    account_id uuid REFERENCES app.accounts(id),
    action text NOT NULL CHECK (length(action) BETWEEN 1 AND 80),
    target_type text NOT NULL CHECK (length(target_type) BETWEEN 1 AND 40),
    target_id uuid,
    request_id text,
    created_at timestamptz NOT NULL DEFAULT transaction_timestamp()
);

-- +goose StatementBegin
CREATE FUNCTION app.consume_rate_limit(operation_value text, kind_value text, digest_value bytea)
RETURNS TABLE(allowed boolean, retry_after_seconds integer)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $$
DECLARE
    resulting_count integer;
    window_seconds integer;
    limit_value integer;
    window_start_value timestamptz;
    window_end_value timestamptz;
BEGIN
    IF kind_value NOT IN ('network','subject') OR octet_length(digest_value) <> 32 THEN
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
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.issue_password_reset(identity_value text, email_identity boolean, verifier_value bytea)
RETURNS TABLE(result_principal_id uuid, result_email text)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $$
BEGIN
    IF identity_value = '' OR octet_length(verifier_value) <> 32 THEN
        RAISE EXCEPTION 'invalid password reset arguments' USING ERRCODE='22023';
    END IF;
    SELECT p.id,p.email INTO result_principal_id,result_email
      FROM app.authentication_principals p
      LEFT JOIN app.users u ON u.principal_id=p.id
      LEFT JOIN app.accounts a ON a.id=u.account_id
     WHERE ((email_identity AND p.canonical_email=identity_value) OR
            (NOT email_identity AND p.canonical_username=identity_value))
       AND p.disabled_at IS NULL AND (p.role='administrator' OR a.state='active')
     FOR UPDATE OF p;
    IF NOT FOUND THEN RETURN; END IF;
    UPDATE app.password_resets SET revoked_at=transaction_timestamp(),replaced_at=transaction_timestamp()
     WHERE app.password_resets.principal_id=result_principal_id
       AND used_at IS NULL AND revoked_at IS NULL;
    INSERT INTO app.password_resets(principal_id,verifier) VALUES(result_principal_id,verifier_value);
    RETURN NEXT;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.complete_password_reset(verifier_value bytea, password_hash_value text,
    audit_id_value uuid, request_id_value text)
RETURNS boolean
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $$
DECLARE target_principal_id uuid; changed integer;
BEGIN
    IF octet_length(verifier_value) <> 32 OR password_hash_value = '' OR audit_id_value IS NULL THEN
        RAISE EXCEPTION 'invalid password reset completion arguments' USING ERRCODE='22023';
    END IF;
    SELECT principal_id INTO target_principal_id FROM app.password_resets
     WHERE verifier=verifier_value AND used_at IS NULL AND revoked_at IS NULL
       AND transaction_timestamp()<expires_at FOR UPDATE;
    IF NOT FOUND THEN RETURN false; END IF;
    UPDATE app.authentication_principals SET password_hash=password_hash_value,updated_at=transaction_timestamp()
     WHERE id=target_principal_id AND disabled_at IS NULL;
    GET DIAGNOSTICS changed = ROW_COUNT;
    IF changed <> 1 THEN RETURN false; END IF;
    UPDATE app.password_resets SET used_at=transaction_timestamp() WHERE verifier=verifier_value;
    UPDATE app.sessions SET revoked_at=transaction_timestamp()
     WHERE principal_id=target_principal_id AND revoked_at IS NULL;
    INSERT INTO app.audit_records(id,actor_principal_id,action,target_type,target_id,request_id)
    VALUES(audit_id_value,target_principal_id,'password.reset','principal',target_principal_id,request_id_value);
    RETURN true;
END;
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION app.cleanup_rate_limits()
RETURNS integer LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $$
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
$$;
-- +goose StatementEnd

GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.enforce_principal_role_consistency() OWNER TO workouts_security_owner;
ALTER FUNCTION app.consume_rate_limit(text,text,bytea) OWNER TO workouts_security_owner;
ALTER FUNCTION app.issue_password_reset(text,boolean,bytea) OWNER TO workouts_security_owner;
ALTER FUNCTION app.complete_password_reset(bytea,text,uuid,text) OWNER TO workouts_security_owner;
ALTER FUNCTION app.cleanup_rate_limits() OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
GRANT USAGE ON SCHEMA app TO workouts_security_owner;
GRANT SELECT, INSERT, UPDATE, DELETE ON app.rate_limits TO workouts_security_owner;
GRANT SELECT ON app.authentication_principals, app.administrators, app.users, app.accounts TO workouts_security_owner;
GRANT UPDATE (password_hash,updated_at) ON app.authentication_principals TO workouts_security_owner;
GRANT SELECT, INSERT, UPDATE ON app.password_resets TO workouts_security_owner;
GRANT SELECT (principal_id,revoked_at) ON app.sessions TO workouts_security_owner;
GRANT UPDATE (revoked_at) ON app.sessions TO workouts_security_owner;
GRANT INSERT ON app.audit_records TO workouts_security_owner;

REVOKE ALL ON app.authentication_principals, app.administrators, app.accounts, app.users,
    app.preferences, app.sessions, app.invitations, app.invitation_tokens, app.password_resets,
    app.rate_limits, app.audit_records FROM PUBLIC, workouts_worker;
REVOKE ALL ON FUNCTION app.enforce_principal_role_consistency(), app.consume_rate_limit(text,text,bytea),
    app.issue_password_reset(text,boolean,bytea), app.complete_password_reset(bytea,text,uuid,text),
    app.cleanup_rate_limits() FROM PUBLIC, workouts_worker;
GRANT SELECT (id,role,username,canonical_username,email,canonical_email,password_hash,full_name,disabled_at)
    ON app.authentication_principals TO workouts_api;
GRANT INSERT (id,role,username,canonical_username,email,canonical_email,canonicalization_version,password_hash,full_name)
    ON app.authentication_principals TO workouts_api;
GRANT UPDATE (password_hash,full_name,updated_at) ON app.authentication_principals TO workouts_api;
GRANT SELECT (id,state) ON app.accounts TO workouts_api;
GRANT INSERT (id) ON app.accounts TO workouts_api;
GRANT SELECT (principal_id,account_id), INSERT (principal_id,account_id) ON app.users TO workouts_api;
GRANT SELECT, INSERT, UPDATE ON app.preferences TO workouts_api;
GRANT SELECT (id,principal_id,credential_kind,credential_verifier,csrf_token,expires_at,revoked_at),
    INSERT (id,principal_id,credential_kind,credential_verifier,csrf_token,expires_at), UPDATE (revoked_at)
    ON app.sessions TO workouts_api;
GRANT SELECT (id,email,canonical_email,state,created_by,accepted_by,accepted_at,revoked_at,created_at),
    INSERT (id,email,canonical_email,created_by), UPDATE (state,accepted_by,accepted_at)
    ON app.invitations TO workouts_api;
GRANT SELECT (invitation_id,verifier,issued_at,expires_at,used_at,revoked_at,replaced_at,delivery_state,delivery_category),
    INSERT (invitation_id,verifier), UPDATE (used_at,delivery_state,delivery_category)
    ON app.invitation_tokens TO workouts_api;
GRANT SELECT (principal_id,verifier,issued_at,expires_at,used_at,revoked_at,replaced_at,delivery_state,delivery_category),
    UPDATE (delivery_state,delivery_category) ON app.password_resets TO workouts_api;
GRANT INSERT (id,actor_principal_id,account_id,action,target_type,target_id,request_id) ON app.audit_records TO workouts_api;
GRANT EXECUTE ON FUNCTION app.consume_rate_limit(text,text,bytea), app.issue_password_reset(text,boolean,bytea),
    app.complete_password_reset(bytea,text,uuid,text), app.cleanup_rate_limits() TO workouts_api;
UPDATE app.schema_metadata SET schema_version = 2, minimum_runtime_version = 1;

-- +goose Down
UPDATE app.schema_metadata SET schema_version = 1, minimum_runtime_version = 1;
DROP FUNCTION IF EXISTS app.cleanup_rate_limits();
DROP FUNCTION IF EXISTS app.complete_password_reset(bytea,text,uuid,text);
DROP FUNCTION IF EXISTS app.issue_password_reset(text,boolean,bytea);
DROP FUNCTION IF EXISTS app.consume_rate_limit(text,text,bytea);
DROP FUNCTION IF EXISTS app.enforce_principal_role_consistency() CASCADE;
DROP TABLE IF EXISTS app.audit_records, app.rate_limits, app.password_resets, app.invitation_tokens,
    app.invitations, app.sessions, app.preferences, app.users, app.accounts, app.administrators,
    app.authentication_principals CASCADE;
