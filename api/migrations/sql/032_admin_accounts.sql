-- +goose Up
-- Keep first-registration and last-authentication metadata separate from private
-- activity. Preserve login history after session cleanup, expiry or revocation.
ALTER TABLE app.authentication_principals ADD COLUMN last_login_at timestamptz;
UPDATE app.authentication_principals p SET last_login_at = history.latest
FROM (SELECT principal_id,max(created_at) AS latest FROM app.sessions GROUP BY principal_id) history
WHERE history.principal_id=p.id;

GRANT SELECT(created_at) ON app.authentication_principals TO workouts_api;
GRANT UPDATE(revoked_at) ON app.invitations TO workouts_api;
GRANT UPDATE(revoked_at,replaced_at) ON app.invitation_tokens TO workouts_api;
GRANT UPDATE(last_login_at) ON app.authentication_principals TO workouts_security_owner;

-- +goose StatementBegin
CREATE FUNCTION app.record_principal_login() RETURNS trigger
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, app AS $function$
BEGIN
    UPDATE app.authentication_principals SET last_login_at=GREATEST(last_login_at,NEW.created_at)
    WHERE id=NEW.principal_id;
    RETURN NEW;
END;
$function$;
-- +goose StatementEnd

CREATE TRIGGER sessions_record_login AFTER INSERT ON app.sessions
FOR EACH ROW EXECUTE FUNCTION app.record_principal_login();

-- Restore the caller's logical scope on success and exception. No caller
-- receives an account capability or private row result.
-- +goose StatementBegin
CREATE FUNCTION app.admin_account_usage(admin_session uuid,target_principal uuid,target_timezone text)
RETURNS TABLE(full_name text,last_login_at timestamptz,workout_count bigint,route_count bigint,last_workout_date date)
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, app AS $function$
DECLARE
    target_account uuid;
    previous_account text := current_setting('app.account_id',true);
    previous_session text := current_setting('app.session_id',true);
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM app.sessions s JOIN app.authentication_principals p ON p.id=s.principal_id
        WHERE s.id=admin_session AND p.role='administrator' AND p.disabled_at IS NULL
          AND s.revoked_at IS NULL AND s.expires_at>transaction_timestamp()
    ) THEN
        RAISE EXCEPTION 'administrator session required' USING ERRCODE='42501';
    END IF;
    SELECT u.account_id INTO target_account
    FROM app.users u JOIN app.authentication_principals p ON p.id=u.principal_id
    WHERE p.id=target_principal AND p.role='user';
    IF target_account IS NULL THEN RETURN; END IF;
    PERFORM set_config('app.account_id',target_account::text,true);
    PERFORM set_config('app.session_id','',true);
    RETURN QUERY
    SELECT p.full_name,p.last_login_at,count(w.id),count(r.workout_id),
        (max(w.started_at) AT TIME ZONE target_timezone)::date
    FROM app.authentication_principals p
    LEFT JOIN app.workouts w ON w.account_id=target_account AND w.deletion_requested_at IS NULL
    LEFT JOIN app.workout_routes r ON r.account_id=target_account AND r.workout_id=w.id
    WHERE p.id=target_principal GROUP BY p.id;
    PERFORM set_config('app.account_id',COALESCE(previous_account,''),true);
    PERFORM set_config('app.session_id',COALESCE(previous_session,''),true);
EXCEPTION WHEN OTHERS THEN
    PERFORM set_config('app.account_id',COALESCE(previous_account,''),true);
    PERFORM set_config('app.session_id',COALESCE(previous_session,''),true);
    RAISE;
END;
$function$;
-- +goose StatementEnd

GRANT CREATE ON SCHEMA app TO workouts_security_owner;
ALTER FUNCTION app.record_principal_login() OWNER TO workouts_security_owner;
ALTER FUNCTION app.admin_account_usage(uuid,uuid,text) OWNER TO workouts_security_owner;
REVOKE CREATE ON SCHEMA app FROM workouts_security_owner;
REVOKE ALL ON FUNCTION app.record_principal_login(),app.admin_account_usage(uuid,uuid,text)
FROM PUBLIC,workouts_api,workouts_worker,workouts_coverage_worker,workouts_tiles;
GRANT EXECUTE ON FUNCTION app.admin_account_usage(uuid,uuid,text) TO workouts_api;

-- Advance only the existing phase writer's literal runtime fence.
-- +goose StatementBegin
DO $block$
DECLARE definition text;
BEGIN
    IF (SELECT md5(prosrc) FROM pg_proc WHERE oid='app.record_ingest_phase(uuid,text,uuid,text,bigint,bigint)'::regprocedure)
        IS DISTINCT FROM '5686c29cbf2caf7a845b619e348e97aa' THEN
        RAISE EXCEPTION 'unexpected ingest phase definition before runtime32' USING ERRCODE='55000';
    END IF;
    definition:=pg_get_functiondef('app.record_ingest_phase(uuid,text,uuid,text,bigint,bigint)'::regprocedure);
    definition:=replace(definition,'minimum_runtime_version <= 31','minimum_runtime_version <= 32');
    definition:=replace(definition,'ingest phase runtime 31 is required','ingest phase runtime 32 is required');
    definition:=replace(definition,'fence_ingest_job(target_job_id, claiming_worker, current_lease_token, 31)',
        'fence_ingest_job(target_job_id, claiming_worker, current_lease_token, 32)');
    EXECUTE definition;
END;
$block$;
-- +goose StatementEnd

-- Later dependencies are complete here: install guarded trigger bodies,
-- scoped API write fences and final purge-table grants only after 004-031.
-- Full-schema account-purge installation (folded lifecycle foundation): guarded by a non-forgeable transaction receipt.
-- Existing lease/append-only guards remain unchanged outside that path; FK
-- constraints and RI cascades are never disabled.
-- +goose StatementBegin
DO $block$
DECLARE item record; definition text; body text; table_name text;
BEGIN
    FOR item IN SELECT DISTINCT p.oid,p.prosrc FROM pg_trigger t JOIN pg_proc p ON p.oid=t.tgfoid
      WHERE NOT t.tgisinternal AND t.tgrelid IN (SELECT c.oid FROM pg_class c JOIN pg_attribute a ON a.attrelid=c.oid WHERE c.relnamespace='app'::regnamespace AND a.attname='account_id')
    LOOP
        body:=regexp_replace(item.prosrc,'\mBEGIN\M',E'BEGIN\n    IF app.account_purge_active() THEN\n        IF TG_LEVEL=''STATEMENT'' THEN RETURN NULL; END IF;\n        IF TG_OP=''DELETE'' THEN RETURN OLD; END IF;\n        RETURN NEW;\n    END IF;',1,1);
        IF body=item.prosrc THEN RAISE EXCEPTION 'unrecognized account trigger body' USING ERRCODE='55000'; END IF;
        definition:=replace(pg_get_functiondef(item.oid),item.prosrc,body);
        EXECUTE definition;
    END LOOP;
    FOREACH table_name IN ARRAY app.account_purge_tables() LOOP
        EXECUTE format('GRANT SELECT,DELETE ON app.%I TO workouts_security_owner',table_name);
        IF table_name<>'coverage_retry_attempts' THEN
            EXECUTE format('CREATE TRIGGER account_active_api_write BEFORE INSERT OR UPDATE ON app.%I FOR EACH ROW EXECUTE FUNCTION app.require_active_api_account()',table_name);
        END IF;
    END LOOP;
END;
$block$;
-- +goose StatementEnd
GRANT SELECT,DELETE ON app.users,app.authentication_principals,app.accounts,app.invitations,app.invitation_tokens,app.password_resets,app.sessions,app.rate_limits TO workouts_security_owner;
GRANT UPDATE(actor_principal_id,account_id) ON app.audit_records TO workouts_security_owner;
GRANT SELECT ON app.audit_records TO workouts_security_owner;


UPDATE app.schema_metadata SET schema_version=32,minimum_runtime_version=32 WHERE singleton;

-- +goose Down
-- +goose StatementBegin
DO $block$ BEGIN
    RAISE EXCEPTION 'admin account metadata is forward-only; restore the pre-migration backup to roll back'
        USING ERRCODE='55000';
END; $block$;
-- +goose StatementEnd
