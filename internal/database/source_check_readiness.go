package database

const runtimeSourceCheckReadinessSQL = `($1<32 OR EXISTS (
    SELECT 1 FROM pg_proc p
    WHERE p.oid=to_regprocedure('app.claim_next_source_check(text,uuid,interval,integer)')
    AND pg_get_userbyid(p.proowner)='workouts_security_owner'
    AND p.prosecdef AND p.provolatile='v' AND p.proretset AND NOT p.proleakproof
    AND pg_get_function_result(p.oid)='TABLE(job_id uuid, account_id uuid, kind text)'
    AND p.proconfig=ARRAY['search_path=pg_catalog, app']
    AND md5(p.prosrc)='c5ccf5d6d30f5ce3e91d402888268b53'
    AND (SELECT count(*) FROM aclexplode(p.proacl))=2
    AND has_function_privilege('workouts_worker',p.oid,'EXECUTE')
    AND NOT EXISTS (SELECT 1 FROM aclexplode(p.proacl) a
        WHERE a.is_grantable OR a.privilege_type<>'EXECUTE'
        OR a.grantee NOT IN (p.proowner,'workouts_worker'::regrole::oid))
))`
