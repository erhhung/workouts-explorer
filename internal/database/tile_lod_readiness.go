package database

// This additive contract composes with the unchanged runtime31 tile contract.
// Older workers do not need to discover the new Martin entrypoints.
const runtimeTileLODReadinessSQL = `
SELECT $1 < 31 OR (
    WITH functions(signature,result,language,volatility,definer,strict,parallel,returns_set,defaults,config,tiles,body_hash) AS (VALUES
        ('app.project_route_lod(geometry)','TABLE(zoom_band integer, component integer, piece integer, geom geometry)','sql','i',false,true,'s',true,0,
            ARRAY['search_path=pg_catalog, public'],false,'c6cce11de27bde21cb3d7ce9b5cf893f'),
        ('app.maintain_raw_route_lod_chunks()','trigger','plpgsql','v',true,false,'u',false,0,
            ARRAY['search_path=pg_catalog, app, public','jit=off'],false,'179d3b83117cac37eb15d5f1ea25d0e1'),
        ('app.tile_scope(json,boolean)','TABLE(account_id uuid, selection_id uuid, context_id uuid, start_date date, end_date date)','plpgsql','s',false,false,'u',true,0,
            ARRAY['search_path=pg_catalog, app'],false,'8ef86e9b902c45293f60f889ce80ad9a'),
        ('app.raw_route_mvt(integer,integer,integer,json)','bytea','plpgsql','s',true,false,'u',false,0,
            ARRAY['search_path=pg_catalog, app, public','jit=off'],true,'f92b4cb5358376574736869fa030b4fa'),
        ('app.coverage_mvt(integer,integer,integer,json)','bytea','plpgsql','s',true,false,'u',false,1,
            ARRAY['search_path=pg_catalog, app, public','jit=off','work_mem=32MB'],true,'9a04626be3ff4e8e95d3414f87d20cf5')
    )
    SELECT (SELECT schema_version >= 31 AND minimum_runtime_version >= 31 AND minimum_runtime_version <= $1
        AND migration_baseline = 27 FROM app.schema_metadata WHERE singleton)
    AND EXISTS (
        SELECT 1 FROM pg_class c WHERE c.oid=to_regclass('app.raw_route_lod_chunks')
            AND c.relkind='r' AND c.relpersistence='p' AND pg_get_userbyid(c.relowner)='workouts_security_owner'
            AND c.relrowsecurity AND c.relforcerowsecurity
            AND ARRAY(SELECT a.attname::text FROM pg_attribute a WHERE a.attrelid=c.oid AND a.attnum>0
                AND NOT a.attisdropped ORDER BY a.attnum)=ARRAY['account_id','workout_id','zoom_band','component','piece','geom']
            AND NOT EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped
                AND (NOT a.attnotnull OR a.atthasdef OR a.attidentity<>'' OR a.attgenerated<>''
                    OR format_type(a.atttypid,a.atttypmod)<>CASE WHEN a.attname='geom' THEN 'geometry(Geometry,3857)'
                        WHEN a.attname IN ('zoom_band','component','piece') THEN 'integer' ELSE 'uuid' END))
            AND (SELECT count(*) FROM pg_policy p WHERE p.polrelid=c.oid)=1
            AND EXISTS (SELECT 1 FROM pg_policy p WHERE p.polrelid=c.oid AND p.polname='raw_route_lod_chunks_owner_policy'
                AND p.polcmd='*' AND p.polpermissive AND p.polroles=ARRAY['workouts_security_owner'::regrole::oid]
                AND pg_get_expr(p.polqual,p.polrelid)='true' AND pg_get_expr(p.polwithcheck,p.polrelid)='true')
            AND (SELECT count(*) FROM pg_constraint k WHERE k.conrelid=c.oid AND k.contype IN ('p','f','c'))=4
            AND NOT EXISTS (SELECT 1 FROM unnest(ARRAY[
                'PRIMARY KEY (account_id, workout_id, zoom_band, component, piece)',
                'FOREIGN KEY (account_id, workout_id) REFERENCES app.workout_routes(account_id, workout_id) ON UPDATE CASCADE ON DELETE CASCADE',
                'CHECK (((zoom_band = ANY (ARRAY[10, 12, 14])) AND (component > 0) AND (piece >= 0)))',
                'CHECK (((geometrytype(geom) = ANY (ARRAY[''LINESTRING''::text, ''POINT''::text])) AND (NOT st_isempty(geom)) AND (st_npoints(geom) <= 256)))']) definition
                WHERE NOT EXISTS (SELECT 1 FROM pg_constraint k WHERE k.conrelid=c.oid AND k.convalidated AND k.conenforced
                    AND NOT k.condeferrable AND NOT k.condeferred AND pg_get_constraintdef(k.oid)=definition))
            AND NOT EXISTS (SELECT 1 FROM pg_constraint k LEFT JOIN pg_index i ON i.indexrelid=k.conindid
                LEFT JOIN pg_class idx ON idx.oid=i.indexrelid LEFT JOIN pg_am am ON am.oid=idx.relam
                WHERE k.conrelid=c.oid AND k.contype='p' AND (i.indexrelid IS NULL OR i.indrelid<>c.oid
                    OR NOT i.indisprimary OR NOT i.indisunique OR NOT i.indisvalid OR NOT i.indisready OR NOT i.indislive
                    OR NOT i.indimmediate OR i.indisexclusion OR i.indnullsnotdistinct OR am.amname<>'btree'
                    OR idx.relowner<>c.relowner OR i.indpred IS NOT NULL OR i.indexprs IS NOT NULL
                    OR i.indkey::text<>array_to_string(k.conkey,' ') OR i.indnatts<>cardinality(k.conkey)
                    OR i.indnkeyatts<>cardinality(k.conkey)))
            AND NOT EXISTS (SELECT 1 FROM aclexplode(COALESCE(c.relacl,acldefault('r',c.relowner))) acl WHERE acl.grantee<>c.relowner)
            AND NOT EXISTS (SELECT 1 FROM pg_attribute a CROSS JOIN LATERAL aclexplode(a.attacl) acl
                WHERE a.attrelid=c.oid AND NOT a.attisdropped AND acl.grantee<>c.relowner)
            AND NOT EXISTS (SELECT 1 FROM pg_roles r WHERE r.rolname IN ('workouts_api','workouts_worker','workouts_coverage_worker','workouts_tiles')
                AND (has_table_privilege(r.oid,c.oid,'SELECT,INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
                    OR has_any_column_privilege(r.oid,c.oid,'SELECT,INSERT,UPDATE,REFERENCES')))
    )
    AND EXISTS (
        SELECT 1 FROM pg_class idx JOIN pg_index i ON i.indexrelid=idx.oid JOIN pg_am am ON am.oid=idx.relam
        WHERE idx.oid=to_regclass('app.raw_route_lod_chunks_geom_idx') AND idx.relkind='i' AND idx.relpersistence='p'
            AND am.amname='gist' AND pg_get_userbyid(idx.relowner)='workouts_security_owner'
            AND i.indrelid=to_regclass('app.raw_route_lod_chunks') AND i.indisvalid AND i.indisready AND i.indislive AND i.indimmediate
            AND NOT i.indisunique AND NOT i.indisprimary AND NOT i.indisexclusion AND i.indpred IS NULL AND i.indexprs IS NULL
            AND i.indnatts=1 AND i.indnkeyatts=1 AND pg_get_indexdef(i.indexrelid,1,true)='geom'
            AND NOT EXISTS (SELECT 1 FROM unnest(i.indclass) cls JOIN pg_opclass op ON op.oid=cls WHERE op.opcname<>'gist_geometry_ops_2d')
    )
    AND NOT EXISTS (
        SELECT 1 FROM functions expected LEFT JOIN pg_proc p ON p.oid=to_regprocedure(expected.signature)
        WHERE p.oid IS NULL OR pg_get_userbyid(p.proowner)<>'workouts_security_owner' OR p.prokind<>'f'
            OR pg_get_function_result(p.oid)<>expected.result OR p.prolang<>(SELECT oid FROM pg_language WHERE lanname=expected.language)
            OR p.provolatile::text<>expected.volatility OR p.prosecdef<>expected.definer OR p.proisstrict<>expected.strict
            OR p.proparallel::text<>expected.parallel OR p.proretset<>expected.returns_set OR p.proleakproof
            OR p.pronargdefaults<>expected.defaults OR p.proconfig IS DISTINCT FROM expected.config
            OR pg_get_expr(p.proargdefaults,0) IS DISTINCT FROM CASE WHEN expected.defaults=1 THEN '''{}''::json' END
            OR md5(p.prosrc)<>expected.body_hash
            OR has_function_privilege('workouts_tiles',p.oid,'EXECUTE')<>expected.tiles
            OR EXISTS (SELECT 1 FROM pg_roles r WHERE r.rolname IN ('workouts_api','workouts_worker','workouts_coverage_worker')
                AND has_function_privilege(r.oid,p.oid,'EXECUTE'))
            OR EXISTS (SELECT 1 FROM aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner))) acl WHERE acl.grantee<>p.proowner
                AND (acl.is_grantable OR NOT (expected.tiles AND acl.grantee='workouts_tiles'::regrole::oid)))
    )
    AND EXISTS (
        SELECT 1 FROM pg_trigger t WHERE t.tgrelid=to_regclass('app.workout_routes') AND t.tgname='workout_routes_lod_chunks_after_write'
            AND NOT t.tgisinternal AND t.tgenabled='O' AND t.tgtype=29
            AND t.tgfoid=to_regprocedure('app.maintain_raw_route_lod_chunks()') AND t.tgattr::text='' AND t.tgnargs=0 AND t.tgargs=''::bytea
            AND NOT t.tgdeferrable AND NOT t.tginitdeferred AND t.tgconstraint=0 AND t.tgqual IS NULL
            AND (SELECT count(*) FROM pg_trigger callback WHERE NOT callback.tgisinternal
                AND callback.tgfoid=to_regprocedure('app.maintain_raw_route_lod_chunks()'))=1
    )
)`
