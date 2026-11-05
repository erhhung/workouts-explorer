package database

// This is an ingest-side geometry repair, not a new worker/coverage protocol.
const runtimeRouteGapReadinessSQL = `
SELECT (
    SELECT count(*)=2 AND bool_and(
        pg_get_userbyid(p.proowner)='workouts_security_owner'
        AND pg_get_function_result(p.oid)='geometry' AND l.lanname='sql'
        AND NOT p.prosecdef AND NOT p.proretset AND p.pronargdefaults=0
        AND p.provolatile=f.volatility AND p.proisstrict=f.strict AND p.proparallel=f.parallel
        AND p.proconfig=f.config AND md5(p.prosrc)=f.body_hash
        AND NOT EXISTS (SELECT 1 FROM aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner))) acl
            WHERE acl.grantee<>p.proowner)
    )
    FROM (VALUES
        ('app.split_raw_route_spatial_gaps(geometry)','i',true,'s',ARRAY['search_path=pg_catalog, public'],
            'd122b64c4a5e2bda9376356c20dacc49'),
        ('app.build_segmented_workout_route(uuid,uuid)','s',false,'u',ARRAY['search_path=pg_catalog, app, public'],
            '39d8101a86b1a1e96895509b78e320f8')
    ) f(signature,volatility,strict,parallel,config,body_hash)
    JOIN pg_proc p ON p.oid=to_regprocedure(f.signature)
    JOIN pg_language l ON l.oid=p.prolang
)
`
