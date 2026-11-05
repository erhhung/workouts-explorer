package database

// Catalog-only checks run under restricted runtime roles, never against private
// chunk rows. Exact bodies also protect the API cache authorization boundary.
const runtimeTileReadinessSQL = `
SELECT $1 < 31 OR (
    WITH relations(name,columns,primary_key,foreign_key) AS (VALUES
        ('raw_route_tile_chunks', ARRAY['account_id','workout_id','component','piece','geom'],
            'PRIMARY KEY (account_id, workout_id, component, piece)',
            'FOREIGN KEY (account_id, workout_id) REFERENCES app.workout_routes(account_id, workout_id) ON UPDATE CASCADE ON DELETE CASCADE'),
        ('coverage_tile_chunks', ARRAY['account_id','workout_id','physical_segment_id','component','piece','geom'],
            'PRIMARY KEY (account_id, workout_id, physical_segment_id, component, piece)',
            'FOREIGN KEY (account_id, workout_id, physical_segment_id) REFERENCES app.workout_segment_matches(account_id, workout_id, physical_segment_id) ON UPDATE CASCADE ON DELETE CASCADE')
    ), functions(signature,result,language,volatility,definer,strict,parallel,returns_set,defaults,config,api,tiles,body_hash) AS (VALUES
        ('app.project_tile_chunks(geometry)','TABLE(component integer, piece integer, geom geometry)','sql','i',false,true,'s',true,0,
            ARRAY['search_path=pg_catalog, public'],false,false,'ec26acf7465e35d92f949ba42a175e55'),
        ('app.tile_display_geometry(geometry,integer)','geometry','sql','i',false,true,'s',false,0,
            ARRAY['search_path=pg_catalog, public'],false,false,'c222ba2b36cd72e51de406cea383fce7'),
        ('app.maintain_raw_route_tile_chunks()','trigger','plpgsql','v',true,false,'u',false,0,
            ARRAY['search_path=pg_catalog, app, public','jit=off'],false,false,'dbd527b591a83b7025fbbbf3c08a2e20'),
        ('app.maintain_coverage_tile_chunks()','trigger','plpgsql','v',true,false,'u',false,0,
            ARRAY['search_path=pg_catalog, app, public','jit=off'],false,false,'385bb0bfcd3c486d9eef987847f1ff08'),
        ('app.authorize_map_tile(uuid,uuid,uuid,bigint,boolean)','timestamp with time zone','sql','s',true,false,'u',false,0,
            ARRAY['search_path=pg_catalog, app'],true,false,'0ee9b086746dc45775aad7901052bfa7'),
        ('app.raw_route_mvt(integer,integer,integer,json)','bytea','plpgsql','s',true,false,'u',false,0,
            ARRAY['search_path=pg_catalog, app, public','jit=off'],false,true,'f92b4cb5358376574736869fa030b4fa'),
        ('app.coverage_mvt(integer,integer,integer,json)','bytea','plpgsql','s',true,false,'u',false,1,
            ARRAY['search_path=pg_catalog, app, public','jit=off','work_mem=32MB'],false,true,'9a04626be3ff4e8e95d3414f87d20cf5')
    )
    SELECT (SELECT schema_version >= 31 AND minimum_runtime_version >= 31 AND minimum_runtime_version <= $1
        AND migration_baseline = 27 FROM app.schema_metadata WHERE singleton)
    AND EXISTS (SELECT 1 FROM pg_proc p WHERE p.oid=to_regprocedure('app.record_ingest_phase(uuid,text,uuid,text,bigint,bigint)')
        AND md5(p.prosrc)='5686c29cbf2caf7a845b619e348e97aa')
    AND NOT EXISTS (
        SELECT 1 FROM relations expected LEFT JOIN pg_class c ON c.oid=to_regclass('app.'||expected.name)
        WHERE c.oid IS NULL OR c.relkind<>'r' OR c.relpersistence<>'p'
            OR pg_get_userbyid(c.relowner)<>'workouts_security_owner' OR NOT c.relrowsecurity OR NOT c.relforcerowsecurity
            OR ARRAY(SELECT a.attname::text FROM pg_attribute a WHERE a.attrelid=c.oid AND a.attnum>0
                AND NOT a.attisdropped ORDER BY a.attnum) IS DISTINCT FROM expected.columns
            OR EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped
                AND (NOT a.attnotnull OR a.atthasdef OR a.attidentity<>'' OR a.attgenerated<>''
                    OR format_type(a.atttypid,a.atttypmod)<>CASE WHEN a.attname='geom' THEN 'geometry(Geometry,3857)'
                        WHEN a.attname IN ('component','piece') THEN 'integer' ELSE 'uuid' END))
            OR (SELECT count(*) FROM pg_policy p WHERE p.polrelid=c.oid)<>1
            OR NOT EXISTS (SELECT 1 FROM pg_policy p WHERE p.polrelid=c.oid AND p.polname=expected.name||'_owner_policy'
                AND p.polcmd='*' AND p.polpermissive AND p.polroles=ARRAY['workouts_security_owner'::regrole::oid]
                AND pg_get_expr(p.polqual,p.polrelid)='true' AND pg_get_expr(p.polwithcheck,p.polrelid)='true')
            OR (SELECT count(*) FROM pg_constraint k WHERE k.conrelid=c.oid AND k.contype IN ('p','f','c'))<>4
            OR EXISTS (SELECT 1 FROM unnest(ARRAY[expected.primary_key,expected.foreign_key,
                'CHECK (((component > 0) AND (piece >= 0)))',
                'CHECK (((geometrytype(geom) = ANY (ARRAY[''LINESTRING''::text, ''POINT''::text])) AND (NOT st_isempty(geom)) AND (st_npoints(geom) <= 256)))']) definition
                WHERE NOT EXISTS (SELECT 1 FROM pg_constraint k WHERE k.conrelid=c.oid AND k.convalidated AND k.conenforced
                    AND NOT k.condeferrable AND NOT k.condeferred AND pg_get_constraintdef(k.oid)=definition))
            OR EXISTS (SELECT 1 FROM pg_constraint k LEFT JOIN pg_index i ON i.indexrelid=k.conindid
                LEFT JOIN pg_class idx ON idx.oid=i.indexrelid LEFT JOIN pg_am am ON am.oid=idx.relam
                WHERE k.conrelid=c.oid AND k.contype='p' AND (i.indexrelid IS NULL OR i.indrelid<>c.oid
                    OR NOT i.indisprimary OR NOT i.indisunique OR NOT i.indisvalid OR NOT i.indisready OR NOT i.indislive
                    OR NOT i.indimmediate OR i.indisexclusion OR i.indnullsnotdistinct OR am.amname<>'btree'
                    OR idx.relowner<>c.relowner OR i.indpred IS NOT NULL OR i.indexprs IS NOT NULL
                    OR i.indkey::text<>array_to_string(k.conkey,' ') OR i.indnatts<>cardinality(k.conkey)
                    OR i.indnkeyatts<>cardinality(k.conkey)))
            OR EXISTS (SELECT 1 FROM aclexplode(COALESCE(c.relacl,acldefault('r',c.relowner))) acl WHERE acl.grantee<>c.relowner)
            OR EXISTS (SELECT 1 FROM pg_attribute a CROSS JOIN LATERAL aclexplode(a.attacl) acl
                WHERE a.attrelid=c.oid AND NOT a.attisdropped AND acl.grantee<>c.relowner)
            OR EXISTS (SELECT 1 FROM pg_roles r WHERE r.rolname IN ('workouts_api','workouts_worker','workouts_coverage_worker','workouts_tiles')
                AND (has_table_privilege(r.oid,c.oid,'SELECT,INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
                    OR has_any_column_privilege(r.oid,c.oid,'SELECT,INSERT,UPDATE,REFERENCES')))
    )
    AND NOT EXISTS (
        SELECT 1 FROM relations expected
        LEFT JOIN pg_class idx ON idx.oid=to_regclass('app.'||expected.name||'_geom_idx')
        LEFT JOIN pg_index i ON i.indexrelid=idx.oid LEFT JOIN pg_am am ON am.oid=idx.relam
        WHERE idx.oid IS NULL OR idx.relkind<>'i' OR idx.relpersistence<>'p' OR am.amname<>'gist'
            OR pg_get_userbyid(idx.relowner)<>'workouts_security_owner' OR i.indrelid<>to_regclass('app.'||expected.name)
            OR NOT i.indisvalid OR NOT i.indisready OR NOT i.indislive OR NOT i.indimmediate
            OR i.indisunique OR i.indisprimary OR i.indisexclusion OR i.indpred IS NOT NULL OR i.indexprs IS NOT NULL
            OR i.indnatts<>1 OR i.indnkeyatts<>1 OR pg_get_indexdef(i.indexrelid,1,true)<>'geom'
            OR EXISTS (SELECT 1 FROM unnest(i.indclass) cls JOIN pg_opclass op ON op.oid=cls WHERE op.opcname<>'gist_geometry_ops_2d')
    )
    AND NOT EXISTS (
        SELECT 1 FROM (VALUES
            ('app.map_selection_workouts_tile_lookup_idx','app.map_selection_workouts',3,ARRAY['account_id','selection_id','workout_id','sort_order']),
            ('app.workouts_tile_lookup_idx','app.workouts',2,ARRAY['account_id','id','workout_type_id','local_start_date','deletion_requested_at'])
        ) expected(name,relation,keys,columns)
        LEFT JOIN pg_class idx ON idx.oid=to_regclass(expected.name)
        LEFT JOIN pg_index i ON i.indexrelid=idx.oid LEFT JOIN pg_am am ON am.oid=idx.relam
        WHERE idx.oid IS NULL OR idx.relkind<>'i' OR idx.relpersistence<>'p' OR am.amname<>'btree'
            OR i.indrelid IS DISTINCT FROM to_regclass(expected.relation)
            OR idx.relowner IS DISTINCT FROM (SELECT relowner FROM pg_class WHERE oid=to_regclass(expected.relation))
            OR NOT i.indisvalid OR NOT i.indisready OR NOT i.indislive OR NOT i.indimmediate
            OR i.indisunique OR i.indisprimary OR i.indisexclusion OR i.indpred IS NOT NULL OR i.indexprs IS NOT NULL
            OR i.indnkeyatts<>expected.keys OR i.indnatts<>cardinality(expected.columns)
            OR ARRAY(SELECT a.attname::text FROM unnest(i.indkey) WITH ORDINALITY key(num,position)
                JOIN pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=key.num ORDER BY key.position) IS DISTINCT FROM expected.columns
            OR EXISTS (SELECT 1 FROM unnest(i.indoption) option WHERE option<>0)
            OR EXISTS (SELECT 1 FROM unnest(i.indclass) cls JOIN pg_opclass op ON op.oid=cls
                WHERE NOT op.opcdefault OR op.opcnamespace<>'pg_catalog'::regnamespace)
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
            OR has_function_privilege('workouts_api',p.oid,'EXECUTE')<>expected.api
            OR has_function_privilege('workouts_tiles',p.oid,'EXECUTE')<>expected.tiles
            OR EXISTS (SELECT 1 FROM pg_roles r WHERE r.rolname IN ('workouts_worker','workouts_coverage_worker') AND has_function_privilege(r.oid,p.oid,'EXECUTE'))
            OR EXISTS (SELECT 1 FROM aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner))) acl WHERE acl.grantee<>p.proowner
                AND (acl.is_grantable OR NOT ((expected.api AND acl.grantee='workouts_api'::regrole::oid)
                    OR (expected.tiles AND acl.grantee='workouts_tiles'::regrole::oid))))
    )
    AND NOT EXISTS (
        SELECT 1 FROM (VALUES ('app.workout_routes','workout_routes_tile_chunks_after_write','app.maintain_raw_route_tile_chunks()'),
            ('app.workout_segment_matches','workout_matches_tile_chunks_after_write','app.maintain_coverage_tile_chunks()')) expected(relation,name,function)
        LEFT JOIN pg_trigger t ON t.tgrelid=to_regclass(expected.relation) AND t.tgname=expected.name
        WHERE t.oid IS NULL OR t.tgisinternal OR t.tgenabled<>'O' OR t.tgtype<>29
            OR t.tgfoid IS DISTINCT FROM to_regprocedure(expected.function) OR t.tgattr::text<>'' OR t.tgnargs<>0 OR t.tgargs<>''::bytea
            OR t.tgdeferrable OR t.tginitdeferred OR t.tgconstraint<>0 OR t.tgqual IS NOT NULL
            OR (SELECT count(*) FROM pg_trigger callback WHERE NOT callback.tgisinternal AND callback.tgfoid=to_regprocedure(expected.function))<>1
    )
)`
