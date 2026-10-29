package database

// Catalog-only checks: private proof/queue contents never require runtime grants.
// This feature belongs to 029, while the complete APP runtime remains 30.
const runtimeCoverageMetadataReadinessSQL = `
	SELECT $1 < 29 OR (
		WITH relations(name,columns) AS (VALUES
			('coverage_route_changes',ARRAY['account_id:uuid:true','workout_id:uuid:true','mutation_version:bigint:true',
				'resolved_version:bigint:true','next_attempt_at:timestamp with time zone:true']),
			('coverage_route_selection_proofs',ARRAY['account_id:uuid:true','workout_id:uuid:true','content_sha256:bytea:true',
				'source_id:uuid:true','route_input_revision:bigint:true','route_input_sha256:bytea:true','mutation_version:bigint:true',
				'catalog_revision:bigint:true','catalog_fingerprint:text:true','selection_fingerprint:text:true',
				'selection_policy:text:true','readiness:text:true','reason:text:false','region_ids:text[]:true','generations:jsonb:true']),
			('coverage_applied_match_proofs',ARRAY['account_id:uuid:true','workout_id:uuid:true','route_input_revision:bigint:true',
				'route_input_sha256:bytea:true','generations:jsonb:true','matching_descriptor:jsonb:true','job_id:uuid:true']),
			('coverage_reconciliation_wakes',ARRAY['account_id:uuid:true','transaction_id:bigint:true'])
		), functions(signature,language,volatility,definer,worker,coverage,result) AS (VALUES
			('app.coverage_matching_descriptor(text,text,text,double precision,text)','sql','i',false,false,false,'jsonb'),
			('app.capture_coverage_matching_descriptor()','plpgsql','v',true,false,false,'trigger'),
			('app.stamp_coverage_applied_match_proof()','plpgsql','v',true,false,false,'trigger'),
			('app.dirty_coverage_route_inputs()','plpgsql','v',true,false,false,'trigger'),
			('app.wake_coverage_reconciliation()','plpgsql','v',true,false,false,'trigger'),
			('app.coverage_applied_target_current(uuid,uuid,bigint,bytea,jsonb,text,text,text,double precision)','sql','s',true,false,false,'boolean'),
			('app.coverage_verified_route_digest(uuid,uuid)','plpgsql','s',true,false,false,'bytea'),
			('app.read_coverage_catalog_observation()','sql','s',true,true,true,
				'TABLE(catalog_revision bigint, last_osm_event_id bigint, catalog_fingerprint text, selection_fingerprint text, selection_policy text, active_heads jsonb, matching_descriptor jsonb)'),
			('app.observe_coverage_catalog(bigint,bigint,text,text,jsonb,text,jsonb,jsonb)','plpgsql','v',true,false,true,'boolean'),
			('app.coverage_selection_metadata(uuid,uuid)','sql','s',true,false,false,'jsonb'),
			('app.coverage_route_actionable(uuid,uuid)','sql','s',true,false,false,'boolean'),
			('app.read_coverage_selection_metadata(uuid,uuid,text,uuid,bigint,uuid)','plpgsql','v',true,true,true,'jsonb'),
			('app.ingest_coverage_route_needs_proof(uuid,uuid,text,uuid,uuid)','sql','v',true,true,false,'boolean'),
			('app.coverage_production_descriptor_current(uuid,text,uuid,jsonb)','sql','s',true,false,true,'boolean'),
			('app.record_coverage_selection_proof(uuid,uuid,jsonb)','plpgsql','v',true,false,false,'void'),
			('app.rebind_ingest_coverage_prepared(uuid,uuid,uuid,jsonb)','plpgsql','v',true,false,false,'void'),
			('app.coverage_reconciliation_queue_due(uuid)','sql','s',true,false,false,'boolean'),
			('app.claim_coverage_reconciliation_work(text,uuid,interval,integer)','plpgsql','v',true,false,true,
				'TABLE(account_id uuid, reconciliation_revision bigint, cursor_workout_id uuid, scan_required boolean)'),
			('app.claim_coverage_reconciliation(text,uuid,interval,integer)','plpgsql','v',true,false,true,
				'TABLE(account_id uuid, reconciliation_revision bigint, cursor_workout_id uuid)'),
			('app.read_coverage_reconciliation_routes(uuid,bigint,text,uuid,uuid,integer)','sql','s',true,false,true,
				'TABLE(workout_id uuid, route_input_revision bigint, route_input_sha256 bytea)'),
			('app.advance_coverage_reconciliation(uuid,bigint,text,uuid,uuid,boolean,interval)','plpgsql','v',true,false,true,'boolean'),
			('app.apply_coverage_selection(uuid,bigint,text,uuid,uuid,bigint,bytea,jsonb,text,text,text,double precision)','plpgsql','v',true,false,true,'boolean')
			,('app.defer_coverage_reconciliation_route(uuid,bigint,text,uuid,uuid,bigint,bytea,bigint)','plpgsql','v',true,false,true,'boolean')
		)
		SELECT (SELECT schema_version >= $1 AND minimum_runtime_version >= 29 AND minimum_runtime_version <= $1
			AND migration_baseline=27 FROM app.schema_metadata WHERE singleton)
		AND NOT EXISTS (
			SELECT 1 FROM relations expected LEFT JOIN pg_class c ON c.oid=to_regclass('app.'||expected.name)
			WHERE c.oid IS NULL OR c.relkind<>'r' OR c.relpersistence<>'p'
				OR pg_get_userbyid(c.relowner)<>'workouts_security_owner' OR NOT c.relrowsecurity OR NOT c.relforcerowsecurity
				OR ARRAY(SELECT a.attname||':'||format_type(a.atttypid,a.atttypmod)||':'||a.attnotnull::text
					FROM pg_attribute a WHERE a.attrelid=c.oid AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum)<>expected.columns
				OR NOT EXISTS (SELECT 1 FROM pg_constraint pk JOIN pg_index i ON i.indexrelid=pk.conindid
					JOIN pg_class backing ON backing.oid=i.indexrelid JOIN pg_am am ON am.oid=backing.relam
					WHERE pk.conrelid=c.oid AND pk.contype='p' AND pk.convalidated AND NOT pk.condeferrable AND NOT pk.condeferred
						AND i.indrelid=c.oid AND i.indisprimary AND i.indisunique AND i.indimmediate AND NOT i.indisexclusion
						AND i.indisvalid AND i.indisready AND i.indislive AND i.indnatts=2 AND i.indnkeyatts=2
						AND i.indexprs IS NULL AND i.indpred IS NULL AND backing.relkind='i' AND backing.relpersistence='p'
						AND backing.relowner=c.relowner AND am.amname='btree' AND i.indoption::text='0 0' AND i.indcollation::text='0 0'
						AND ARRAY(SELECT a.attname::text FROM unnest(pk.conkey) WITH ORDINALITY key(num,ordinality)
							JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum=key.num ORDER BY key.ordinality)
							=CASE WHEN expected.name='coverage_reconciliation_wakes' THEN ARRAY['account_id','transaction_id'] ELSE ARRAY['account_id','workout_id'] END
						AND ARRAY(SELECT a.attname::text FROM unnest(i.indkey) WITH ORDINALITY key(num,ordinality)
							JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum=key.num ORDER BY key.ordinality)
							=CASE WHEN expected.name='coverage_reconciliation_wakes' THEN ARRAY['account_id','transaction_id'] ELSE ARRAY['account_id','workout_id'] END)
				OR (SELECT count(*) FROM pg_policy p WHERE p.polrelid=c.oid)<>1
				OR NOT EXISTS (SELECT 1 FROM pg_policy p WHERE p.polrelid=c.oid AND p.polname=expected.name||'_owner_policy'
					AND p.polcmd='*' AND p.polpermissive AND p.polroles=ARRAY['workouts_security_owner'::regrole::oid]
					AND pg_get_expr(p.polqual,p.polrelid)='true' AND pg_get_expr(p.polwithcheck,p.polrelid)='true')
				OR EXISTS (SELECT 1 FROM pg_roles r WHERE r.rolname IN ('workouts_api','workouts_worker','workouts_coverage_worker','workouts_tiles')
					AND (has_table_privilege(r.oid,c.oid,'SELECT,INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
						OR has_any_column_privilege(r.oid,c.oid,'SELECT,INSERT,UPDATE,REFERENCES')))
				OR EXISTS (SELECT 1 FROM aclexplode(COALESCE(c.relacl,acldefault('r',c.relowner))) acl WHERE acl.grantee<>c.relowner))
		AND NOT EXISTS (
			SELECT 1 FROM functions expected LEFT JOIN pg_proc p ON p.oid=to_regprocedure(expected.signature)
			LEFT JOIN pg_language language ON language.oid=p.prolang
			WHERE p.oid IS NULL OR pg_get_userbyid(p.proowner)<>'workouts_security_owner' OR p.prokind<>'f'
				OR language.lanname<>expected.language OR p.provolatile::text<>expected.volatility OR p.prosecdef<>expected.definer
				OR p.proconfig IS DISTINCT FROM CASE WHEN expected.definer THEN ARRAY['search_path=pg_catalog, app'] ELSE ARRAY['search_path=pg_catalog'] END
				OR p.proisstrict OR p.proleakproof OR p.proparallel<>'u' OR p.pronargdefaults<>0
				OR pg_get_function_result(p.oid)<>expected.result
				OR has_function_privilege('workouts_worker',p.oid,'EXECUTE')<>expected.worker
				OR has_function_privilege('workouts_coverage_worker',p.oid,'EXECUTE')<>expected.coverage
				OR EXISTS (SELECT 1 FROM pg_roles r WHERE r.rolname IN ('workouts_api','workouts_tiles') AND has_function_privilege(r.oid,p.oid,'EXECUTE'))
				OR EXISTS (SELECT 1 FROM aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner))) acl
					WHERE acl.is_grantable OR acl.privilege_type<>'EXECUTE' OR
						(acl.grantee<>p.proowner AND NOT (expected.worker AND acl.grantee='workouts_worker'::regrole::oid)
							AND NOT (expected.coverage AND acl.grantee='workouts_coverage_worker'::regrole::oid))))
		AND NOT EXISTS (
			SELECT 1 FROM (VALUES
				('wake_coverage_reconciliation','81b3e5e86ba0696a04c43959a9faf403'),
				('coverage_matching_descriptor','34490d61ae8f9402e3d0894437f191e3'),
				('capture_coverage_matching_descriptor','885740b2cb0af88be7815828b3f09765'),
				('stamp_coverage_applied_match_proof','f4440cbb918850021c855f3c74a411d3'),
				('dirty_coverage_route_inputs','a84a3a9685b89a67ce923fd6e962769c'),
				('coverage_applied_target_current','8110deeeb2690e0a8feb58536095f5a7'),
				('coverage_verified_route_digest','90d8a0b61f91324a2d8709542a988eb1'),
				('read_coverage_catalog_observation','441766201a9467b4fa10d23f094d660e'),
				('observe_coverage_catalog','680d73736e59d3e9e0a11a652e22228f'),
				('coverage_selection_metadata','4f6b49278a7ed2b3e770351e019ae80f'),
				('coverage_route_actionable','440c63ec4d2037a2d6b6b22a3a8d8926'),
				('read_coverage_selection_metadata','dcf89c494b7b84d541dad54310ca068a'),
				('ingest_coverage_route_needs_proof','d572133790b50c4acb572ace741a7139'),
				('coverage_production_descriptor_current','3ad2529843df50876c146de4b741b976'),
				('record_coverage_selection_proof','efda0754f9231c013ba31a3bde8e18ed'),
				('rebind_ingest_coverage_prepared','488b6b309896d8cd50ae4a27574d2bfb'),
				('coverage_reconciliation_queue_due','8a68d1db762a7c669534fe8c2184056c'),
				('claim_coverage_reconciliation_work','63d18b9a2a785fad137853294fc6c420'),
				('claim_coverage_reconciliation','f276e51e64a003c40087c07af4d74f3d'),
				('read_coverage_reconciliation_routes','ed5bdc8e96df06bbac6a49f24e6e88d2'),
				('advance_coverage_reconciliation','a7e23e47f1a28b2315b16b38f445df60'),
				('apply_coverage_selection','94c6e6eb953f9fcf8cf893ee1f786ca5'),
				('defer_coverage_reconciliation_route','1a6466f46d61adf880dbde142e9484fc')
			) expected(name,body_hash) JOIN functions signature ON split_part(signature.signature,'(',1)='app.'||expected.name
			LEFT JOIN pg_proc p ON p.oid=to_regprocedure(signature.signature)
			WHERE p.oid IS NULL OR md5(p.prosrc)<>expected.body_hash)
		AND NOT EXISTS (
			SELECT 1 FROM (VALUES
				('coverage_reconciliation_state','catalog_revision','bigint',true),
				('coverage_reconciliation_state','catalog_fingerprint','text',true),
				('coverage_reconciliation_state','selection_fingerprint','text',true),
				('coverage_reconciliation_state','selection_policy','text',true),
				('coverage_reconciliation_state','active_heads','jsonb',true),
				('coverage_reconciliation_state','matching_descriptor','jsonb',true),
				('coverage_reconciliation_accounts','scan_required','boolean',true),
				('coverage_reconciliation_accounts','queue_retry_at','timestamp with time zone',true),
				('coverage_job_contexts','matching_descriptor','jsonb',false)
			) expected(relation,name,type,required)
			LEFT JOIN pg_attribute a ON a.attrelid=to_regclass('app.'||expected.relation) AND a.attname=expected.name AND NOT a.attisdropped
			WHERE a.attnum IS NULL OR format_type(a.atttypid,a.atttypmod)<>expected.type OR a.attnotnull<>expected.required)
		AND EXISTS (SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_am am ON am.oid=c.relam
			WHERE i.indexrelid=to_regclass('app.coverage_route_changes_dirty_idx') AND i.indrelid=to_regclass('app.coverage_route_changes')
				AND am.amname='btree' AND c.relpersistence='p' AND c.relowner=(SELECT relowner FROM pg_class WHERE oid=i.indrelid)
				AND i.indisvalid AND i.indisready AND i.indislive
				AND i.indimmediate AND NOT i.indisunique AND NOT i.indisexclusion AND i.indnatts=2 AND i.indnkeyatts=2
				AND NOT i.indisprimary AND i.indoption::text='0 0' AND i.indcollation::text='0 0'
				AND i.indexprs IS NULL AND pg_get_expr(i.indpred,i.indrelid)='(mutation_version > resolved_version)'
				AND ARRAY(SELECT a.attname::text FROM unnest(i.indkey) WITH ORDINALITY key(num,ordinality)
					JOIN pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=key.num ORDER BY key.ordinality)=ARRAY['account_id','workout_id'])
		AND EXISTS (SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_am am ON am.oid=c.relam
			WHERE i.indexrelid=to_regclass('app.coverage_route_selection_proofs_catalog_idx')
				AND i.indrelid=to_regclass('app.coverage_route_selection_proofs') AND am.amname='btree'
				AND c.relowner=(SELECT relowner FROM pg_class WHERE oid=i.indrelid) AND c.relpersistence='p'
				AND i.indisvalid AND i.indisready AND i.indislive AND i.indimmediate
				AND NOT i.indisunique AND NOT i.indisexclusion AND NOT i.indisprimary AND i.indnatts=1 AND i.indnkeyatts=1
				AND i.indexprs IS NULL AND i.indpred IS NULL AND i.indoption::text='3' AND i.indcollation::text='0'
				AND ARRAY(SELECT a.attname::text FROM unnest(i.indkey) key(num)
					JOIN pg_attribute a ON a.attrelid=i.indrelid AND a.attnum=key.num)=ARRAY['catalog_revision'])
		AND NOT EXISTS (
			SELECT 1 FROM (VALUES
				('workout_route_points','workout_route_points_coverage_dirty_insert','app.dirty_coverage_route_inputs()',4,'changed_points',NULL),
				('workout_route_points','workout_route_points_coverage_dirty_update','app.dirty_coverage_route_inputs()',16,'changed_points',NULL),
				('workout_route_points','workout_route_points_coverage_dirty_update_old','app.dirty_coverage_route_inputs()',16,NULL,'changed_points'),
				('workout_route_points','workout_route_points_coverage_dirty_delete','app.dirty_coverage_route_inputs()',8,NULL,'changed_points'),
				('coverage_job_contexts','coverage_job_contexts_matching_descriptor','app.capture_coverage_matching_descriptor()',7,NULL,NULL),
				('workout_coverage_states','workout_coverage_states_applied_match_proof','app.stamp_coverage_applied_match_proof()',17,NULL,NULL),
				('ingest_coverage_handoffs','ingest_coverage_handoffs_reconciliation_wake','app.wake_coverage_reconciliation()',5,NULL,NULL),
				('jobs','jobs_ingest_reconciliation_wake','app.wake_coverage_reconciliation()',17,NULL,NULL)
			) expected(relation,name,function,type,new_table,old_table)
			LEFT JOIN pg_trigger t ON t.tgrelid=to_regclass('app.'||expected.relation) AND t.tgname=expected.name AND NOT t.tgisinternal
			WHERE t.oid IS NULL OR t.tgfoid<>to_regprocedure(expected.function) OR t.tgtype<>expected.type OR t.tgenabled<>'O'
				OR t.tgnargs<>0 OR t.tgattr::text IS DISTINCT FROM CASE WHEN expected.name='jobs_ingest_reconciliation_wake'
					THEN (SELECT attnum::text FROM pg_attribute WHERE attrelid=t.tgrelid AND attname='status')
					WHEN expected.name='workout_coverage_states_applied_match_proof'
					THEN (SELECT attnum::text FROM pg_attribute WHERE attrelid=t.tgrelid AND attname='processing_finished_at') ELSE '' END
				OR (t.tgqual IS NOT NULL)<>(expected.name IN ('jobs_ingest_reconciliation_wake','workout_coverage_states_applied_match_proof'))
				OR t.tgnewtable::text IS DISTINCT FROM expected.new_table OR t.tgoldtable::text IS DISTINCT FROM expected.old_table)
		AND NOT EXISTS (
			SELECT 1 FROM (VALUES
				('app.observe_coverage_catalog(bigint,bigint,text,text,jsonb,text,jsonb,jsonb)','revision<prior.catalog_revision'),
				('app.observe_coverage_catalog(bigint,bigint,text,text,jsonb,text,jsonb,jsonb)','conflicting coverage catalog revision'),
				('app.coverage_selection_metadata(uuid,uuid)','proof.mutation_version=COALESCE(change.mutation_version,0)'),
				('app.coverage_selection_metadata(uuid,uuid)','proof.selection_fingerprint=catalog.selection_fingerprint'),
				('app.coverage_selection_metadata(uuid,uuid)','proof.route_input_revision=state.route_input_revision'),
				('app.record_coverage_selection_proof(uuid,uuid,jsonb)','route selection input changed'),
				('app.record_coverage_selection_proof(uuid,uuid,jsonb)','route selection policy changed'),
				('app.defer_coverage_reconciliation_route(uuid,bigint,text,uuid,uuid,bigint,bytea,bigint)','current_version<>target_mutation_version'),
				('app.record_coverage_selection_proof(uuid,uuid,jsonb)','FOR UPDATE NOWAIT'),
				('app.read_coverage_selection_metadata(uuid,uuid,text,uuid,bigint,uuid)','app.adopted_coverage_authority_current'),
				('app.read_coverage_reconciliation_routes(uuid,bigint,text,uuid,uuid,integer)','app.coverage_route_actionable'),
				('app.read_coverage_reconciliation_routes(uuid,bigint,text,uuid,uuid,integer)','reconciliation.scan_required OR'),
				('app.apply_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,jsonb,text,text,text,double precision,integer)','app.record_coverage_selection_proof'),
				('app.apply_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,jsonb,text,text,text,double precision,integer)','app.coverage_verified_route_digest'),
				('app.apply_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,jsonb,text,text,text,double precision,integer)','invalid producer matching descriptor'),
				('app.seal_ingest_coverage_cohort(uuid,uuid,text,uuid,bigint,uuid,jsonb,integer)','unsupported desired cohort descriptor'),
				('app.seal_ingest_coverage_cohort(uuid,uuid,text,uuid,bigint,uuid,jsonb,integer)','app.rebind_ingest_coverage_prepared'),
				('app.seal_ingest_coverage_cohort(uuid,uuid,text,uuid,bigint,uuid,jsonb,integer)','cohort result authority expired'),
				('app.ingest_coverage_existing_target(uuid,uuid,bigint,bytea,jsonb,text,text,text,double precision)','app.coverage_applied_target_current'),
				('app.enqueue_coverage_update_core(uuid,uuid,text,bigint,bigint,text,text,text,double precision,jsonb,boolean)','AND context.matching_descriptor=app.coverage_matching_descriptor'),
				('app.apply_coverage_reconciliation(uuid,bigint,text,uuid,uuid,bigint,bytea,text,text,jsonb,uuid,uuid,text,text,text,double precision)','AND parent_context.matching_descriptor=app.coverage_matching_descriptor'),
				('app.apply_ingest_coverage_normal(uuid,uuid,bigint,text,uuid,uuid,bigint,bytea,text,text,jsonb,uuid,uuid,text,text,text,double precision)','AND parent_context.matching_descriptor=app.coverage_matching_descriptor'),
				('app.advance_coverage_reconciliation(uuid,bigint,text,uuid,uuid,boolean,interval)','WHEN completed AND scan_required'),
				('app.claim_coverage_reconciliation(text,uuid,interval,integer)','candidate.active_revision IS DISTINCT FROM candidate.desired_revision'),
				('app.coverage_reconciliation_queue_due(uuid)','app.ingest_coverage_cleanup_due'),
				('app.stamp_coverage_applied_match_proof()','context.result_outcome IN (''applied'',''no_evidence'')')
			) expected(signature,fragment) LEFT JOIN pg_proc p ON p.oid=to_regprocedure(expected.signature)
			WHERE p.oid IS NULL OR position(expected.fragment IN p.prosrc)=0)
	)`
