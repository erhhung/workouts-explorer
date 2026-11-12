package database

// Display/export membership is separate from immutable observations and Coverage.
const runtimeRouteGapReadinessSQL = `
SELECT (
    SELECT count(*)=3 AND bool_and(
        pg_get_userbyid(p.proowner)='workouts_security_owner'
        AND pg_get_function_result(p.oid)=f.result AND l.lanname=f.language
        AND NOT p.prosecdef AND p.proretset=f.returns_set AND p.pronargdefaults=0
        AND p.provolatile=f.volatility AND p.proisstrict=f.strict AND p.proparallel=f.parallel
        AND p.proconfig=f.config AND md5(p.prosrc)=f.body_hash
        AND NOT EXISTS (SELECT 1 FROM aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner))) acl
            WHERE acl.grantee<>p.proowner AND NOT(f.api_read AND acl.grantee='workouts_api'::regrole AND acl.privilege_type='EXECUTE'))
        AND (NOT f.api_read OR has_function_privilege('workouts_api',p.oid,'EXECUTE'))
    )
    FROM (VALUES
        ('app.split_raw_route_spatial_gaps(geometry)','i',true,'s',ARRAY['search_path=pg_catalog, public'],
            'd122b64c4a5e2bda9376356c20dacc49','geometry','sql',false,false),
        ('app.build_segmented_workout_route(uuid,uuid)','s',false,'u',ARRAY['search_path=pg_catalog, app, public'],
            '5f10dff2f9464be2223879dd0b03561c','geometry','sql',false,false),
        ('app.raw_route_display_points(uuid,uuid)','s',false,'u',ARRAY['search_path=pg_catalog, app, public'],
            '2e428412b709e03a56960e34dcd55980','TABLE(sequence integer, component integer)','plpgsql',true,true)
    ) f(signature,volatility,strict,parallel,config,body_hash,result,language,returns_set,api_read)
    JOIN pg_proc p ON p.oid=to_regprocedure(f.signature)
    JOIN pg_language l ON l.oid=p.prolang
)
`
