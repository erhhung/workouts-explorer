package database

const runtimeAdminAccountsReadinessSQL = `
SELECT $1<32 OR (
    EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid='app.authentication_principals'::regclass
        AND attname='last_login_at' AND atttypid='timestamptz'::regtype AND NOT attnotnull AND NOT attisdropped)
    AND has_column_privilege('workouts_api','app.authentication_principals','created_at','SELECT')
    AND NOT has_column_privilege('workouts_api','app.authentication_principals','last_login_at','UPDATE')
    AND has_column_privilege('workouts_api','app.invitations','revoked_at','UPDATE')
    AND has_column_privilege('workouts_api','app.invitation_tokens','revoked_at','UPDATE')
    AND has_column_privilege('workouts_api','app.invitation_tokens','replaced_at','UPDATE')
    AND EXISTS (SELECT 1 FROM pg_proc p WHERE p.oid=to_regprocedure('app.admin_account_usage(uuid,uuid,text)')
        AND pg_get_userbyid(p.proowner)='workouts_security_owner' AND p.prosecdef AND p.provolatile='v'
        AND p.proretset AND NOT p.proleakproof
        AND p.proconfig=ARRAY['search_path=pg_catalog, app']
        AND md5(p.prosrc)='a7da55864ecff35bb6648f3aebf24c46'
        AND has_function_privilege('workouts_api',p.oid,'EXECUTE')
        AND (SELECT count(*) FROM aclexplode(p.proacl))=2
        AND NOT EXISTS (SELECT 1 FROM aclexplode(p.proacl) a
            WHERE a.is_grantable OR a.privilege_type<>'EXECUTE'
                OR a.grantee NOT IN (p.proowner,'workouts_api'::regrole::oid)))
    AND EXISTS (SELECT 1 FROM pg_proc p WHERE p.oid=to_regprocedure('app.record_principal_login()')
        AND pg_get_userbyid(p.proowner)='workouts_security_owner' AND p.prosecdef
        AND p.proconfig=ARRAY['search_path=pg_catalog, app']
        AND md5(p.prosrc)='4d286865b68f082ee3484d0866cf2cd3'
        AND (SELECT count(*) FROM aclexplode(p.proacl))=1
        AND NOT EXISTS (SELECT 1 FROM aclexplode(p.proacl) a WHERE a.grantee<>p.proowner OR a.is_grantable))
    AND EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid='app.sessions'::regclass
        AND tgname='sessions_record_login' AND tgfoid=to_regprocedure('app.record_principal_login()')
        AND tgtype=5 AND tgenabled='O' AND NOT tgisinternal)
) AND ($1<32 OR (
    has_column_privilege('workouts_security_owner','app.authentication_principals','disabled_at','UPDATE')
    AND NOT has_column_privilege('workouts_api','app.authentication_principals','disabled_at','UPDATE')
    AND EXISTS (SELECT 1 FROM pg_constraint WHERE conrelid='app.accounts'::regclass
        AND conname='accounts_state_check' AND convalidated
        AND pg_get_constraintdef(oid)='CHECK ((state = ANY (ARRAY[''active''::text, ''disabled''::text, ''deleting''::text, ''deleted''::text])))')
    AND NOT EXISTS (
        SELECT 1 FROM (VALUES
            ('app.set_account_access(uuid,uuid,boolean,uuid,text)','e253a0d8b6f4478b7c9e294d6461549b',true),
            ('app.lock_signin_principal(uuid,text)','e114ed634825510d6386e224d6e64382',true),
            ('app.require_active_job_account()','7a61da2d781e8c06b39e4332e327a88e',false)
        ) expected(signature,body_hash,api_execute)
        LEFT JOIN pg_proc p ON p.oid=to_regprocedure(expected.signature)
        WHERE p.oid IS NULL OR pg_get_userbyid(p.proowner)<>'workouts_security_owner'
            OR NOT p.prosecdef OR p.provolatile<>'v' OR p.proretset OR p.proleakproof
            OR p.proconfig IS DISTINCT FROM ARRAY['search_path=pg_catalog, app']
            OR md5(p.prosrc)<>expected.body_hash
            OR has_function_privilege('workouts_api',p.oid,'EXECUTE')<>expected.api_execute
            OR (SELECT count(*) FROM aclexplode(p.proacl))<>CASE WHEN expected.api_execute THEN 2 ELSE 1 END
            OR EXISTS (SELECT 1 FROM aclexplode(p.proacl) a
                WHERE a.is_grantable OR a.privilege_type<>'EXECUTE'
                    OR a.grantee<>p.proowner AND (NOT expected.api_execute OR a.grantee<>'workouts_api'::regrole::oid)))
    AND EXISTS (SELECT 1 FROM pg_trigger WHERE tgrelid='app.jobs'::regclass
        AND tgname='jobs_require_active_account' AND tgfoid=to_regprocedure('app.require_active_job_account()')
        AND tgtype=7 AND tgenabled='O' AND NOT tgisinternal)
))`
