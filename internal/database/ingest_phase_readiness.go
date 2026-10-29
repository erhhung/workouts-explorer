package database

// Phase reporting belongs to ingest status reporting, introduced in schema 29.
const runtimeIngestPhaseReadinessSQL = `
	SELECT $1 < 29 OR (
		(SELECT schema_version >= $1 AND minimum_runtime_version >= 29 AND minimum_runtime_version <= $1
			AND migration_baseline = 27 FROM app.schema_metadata WHERE singleton)
		AND EXISTS (SELECT 1 FROM pg_proc p WHERE p.oid=to_regprocedure('app.record_ingest_phase(uuid,text,uuid,text,bigint,bigint)')
			AND pg_get_userbyid(p.proowner)='workouts_security_owner' AND p.prokind='f' AND p.prosecdef
			AND p.proconfig=ARRAY['search_path=pg_catalog, app'] AND p.provolatile='v' AND p.proparallel='u'
			AND p.prolang=(SELECT oid FROM pg_language WHERE lanname='plpgsql')
			AND p.prorettype='boolean'::regtype AND NOT p.proretset AND NOT p.proisstrict AND NOT p.proleakproof
			AND p.pronargdefaults=0
			AND (SELECT count(*) FROM aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner))))=2
			AND has_function_privilege('workouts_worker',p.oid,'EXECUTE')
			AND NOT EXISTS (SELECT 1 FROM pg_roles r WHERE r.rolname IN ('workouts_api','workouts_coverage_worker','workouts_tiles')
				AND has_function_privilege(r.oid,p.oid,'EXECUTE'))
			AND NOT EXISTS (SELECT 1 FROM aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner))) acl
				WHERE acl.is_grantable OR acl.privilege_type<>'EXECUTE'
					OR acl.grantee NOT IN (p.proowner,'workouts_worker'::regrole::oid)))
		AND EXISTS (SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_am am ON am.oid=c.relam
			WHERE i.indexrelid=to_regclass('app.job_events_ingest_phase_latest_idx')
				AND i.indrelid=to_regclass('app.job_events') AND c.relkind='i' AND c.relpersistence='p' AND am.amname='btree'
				AND c.relowner=(SELECT relowner FROM pg_class WHERE oid=to_regclass('app.job_events'))
				AND i.indisvalid AND i.indisready AND i.indislive AND i.indimmediate
				AND NOT i.indisunique AND NOT i.indisprimary AND NOT i.indisexclusion AND NOT i.indnullsnotdistinct
				AND i.indnatts=2 AND i.indnkeyatts=2 AND i.indexprs IS NULL
				AND ARRAY(SELECT a.attname::text FROM unnest(i.indkey) WITH ORDINALITY key(num,ordinality)
					JOIN pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=key.num ORDER BY key.ordinality)=ARRAY['job_id','id']
				AND i.indoption::text='0 3' AND i.indcollation::text='0 0'
				AND NOT EXISTS (SELECT 1 FROM unnest(i.indclass) opclass JOIN pg_opclass op ON op.oid=opclass
					WHERE NOT op.opcdefault OR op.opcnamespace<>'pg_catalog'::regnamespace)
				AND pg_get_expr(i.indpred,i.indrelid)='(code ~~ ''ingest-phase-%''::text)')
	)`
