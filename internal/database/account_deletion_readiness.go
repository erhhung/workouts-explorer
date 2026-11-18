package database

const runtimeAccountDeletionReadinessSQL = `($1<32 OR (
    NOT EXISTS (SELECT 1 FROM (VALUES
('app.ack_disposed_staging_pod(uuid,text,uuid,text,text)','e3043ac5d5363e72fedcf3eaebe70187',false,true,false,true),
('app.step_account_deletion(uuid,text,uuid)','c865f0877c46c538ae3c337d293752cf',false,true,false,true),
('app.account_purge_tables()','fd977729f6e57175d092a56764c7d3b1',false,false,false,false),
('app.account_purge_active()','6fb99967da204e223d746c791dfe0bbf',true,true,true,true),
('app.require_active_api_account()','434344e3ab8babb2a42945fe94a2a8d4',false,false,false,true),
('app.release_account_deletion(uuid,text,uuid,text)','50cf67d1a5b3013c44f6f2f967e7242b',false,true,false,true),
('app.start_account_deletion(uuid,uuid,uuid,uuid,text,bytea[])','ed1e31f3b6f864a05f00651f16b436dd',true,false,false,true),
('app.admin_account_deletion(uuid,uuid)','86e08eaebb09ee8d443375621463ca3f',true,false,false,true),
('app.claim_account_deletion(text,uuid,integer)','dbf0d4032559cec237514fa947817aca',false,true,false,true),
('app.register_private_execution(uuid,text,uuid,text,text,text)','b725c7bddab7d9244e482d71dbc87437',false,true,false,true),
('app.clear_private_execution(text,uuid,uuid)','6f9df7b1aa2466a8893a69d1be05f1f8',false,true,false,true),
('app.ack_private_staging_root(text,text,text)','6bc6f4067c5fb82d71da1e7cd8b98c3e',false,true,false,true),
('app.account_deletion_staging(uuid,text,uuid)','994349a5a3975cdc748d43a873137493',false,true,false,true)
    ) expected(signature,body_hash,api,worker,coverage,definer)
    LEFT JOIN pg_proc p ON p.oid=to_regprocedure(expected.signature)
    WHERE p.oid IS NULL OR pg_get_userbyid(p.proowner)<>'workouts_security_owner'
      OR p.prosecdef<>expected.definer OR p.proconfig IS DISTINCT FROM ARRAY['search_path=pg_catalog, app'] AND expected.definer
      OR md5(p.prosrc)<>expected.body_hash
      OR has_function_privilege('workouts_api',p.oid,'EXECUTE')<>expected.api
      OR has_function_privilege('workouts_worker',p.oid,'EXECUTE')<>expected.worker
      OR has_function_privilege('workouts_coverage_worker',p.oid,'EXECUTE')<>expected.coverage
      OR has_function_privilege('workouts_tiles',p.oid,'EXECUTE')
      OR (SELECT count(*) FROM aclexplode(p.proacl))<>1+expected.api::integer+expected.worker::integer+expected.coverage::integer
      OR EXISTS(SELECT 1 FROM aclexplode(p.proacl) a WHERE a.grantee=0 OR a.is_grantable))
    AND NOT EXISTS(SELECT 1 FROM (VALUES ('app.advance_account_data_generation()','f7685748d0f942fb835b3ed22af46599'),('app.advance_workout_ownership_epoch()','f31ac9f5da317a3f053980540acc2556'),('app.attribute_workout_segment_to_park()','6e3a2fc953a0fbfb6e24c30eb8903e7d'),('app.capture_coverage_matching_descriptor()','20a7152f5f9e77ba73fc13fdedd28bed'),('app.clear_ingest_write_capability()','45abea2abbab2db1124d9e997b7b2fc9'),('app.clear_workout_deletion_capability()','1668a6cd9c3f5d2fe229b484bdc72b84'),('app.clear_workout_timezone_write_capability()','74c2ebbe2f1d3e3337b466d0acce4139'),('app.create_legacy_ingest_read_models()','f2ba977a9d4a9170c1e9ff6895a6c3e6'),('app.create_source_sync_state()','a36766dbb870b8d66edd34ab12cdb6d2'),('app.decrement_coverage_path_rollups()','9c1122b1b16537592931a74c6a09c5fc'),('app.decrement_park_rollups()','5844a6e33161dad73799242e12101b95'),('app.dirty_coverage_route_inputs()','02f6a0bd1a49ec6a06d02d5674b286b3'),('app.enforce_ingest_child_parameters()','135ddc6d2da3f5ed697bcd291c12fa27'),('app.enforce_ingest_identity()','24f955f166f8a0d4e30a637d82a0c04c'),('app.enforce_ingest_retry_lineage()','292e6507028d909fd451b7e97008bd2f'),('app.enforce_job_hierarchy()','07d950b0f21ea10262508862209e7fe9'),('app.enforce_job_snapshot_consistency()','18f357632a1be3e15ae0ef177e2e954c'),('app.enforce_job_write()','4120dcabd9336d14f2deab192982df09'),('app.enforce_principal_role_consistency()','c8ae6d14db45234c3d88a80924741f22'),('app.enforce_source_write()','78d45ccaaa5b43f99313e3a9dec69f65'),('app.enforce_workout_deletion_target_lifecycle()','27f0621dee49624ca427471545ce4a17'),('app.enroll_coverage_retry_failure()','d25474483a97c40a3c8882d3606d5f20'),('app.flush_coverage_display_dirty_account()','e5985945523c1da38d36a11f734591e2'),('app.flush_coverage_display_invalidations()','1767a5e12fc13b16d7f9e1df05250b48'),('app.increment_coverage_path_rollups()','24ddd6a64a2afd2ed9cdc2d2fca9b2c7'),('app.increment_park_rollups()','e94852a356a06c98a33e6b6541ba97fe'),('app.invalidate_workout_route_summary()','610685b1e9c321e8fca891eead285ca6'),('app.maintain_coverage_tile_chunks()','cf62e5ef01fec90635973ba8811c03da'),('app.maintain_raw_route_lod_chunks()','322f75b43b2ef31ce2de6fbb9d41e93a'),('app.maintain_raw_route_tile_chunks()','e8f58f7de3c776fcdab7521382f0bc37'),('app.mark_coverage_display_dirty()','a2248186ce67f2d668341a260903e46f'),('app.notify_failed_workout_deletion()','fc516a93b9791f60fe8ab6d87ce75cab'),('app.notify_terminal_ingest_parent()','bc5ef21863a30d9697faf19782dc094d'),('app.prune_unreferenced_coverage_copies()','e0893255d4c08a405c6ac51dd742efda'),('app.prune_unreferenced_coverage_path()','a9c12015b4957778270cf717e7c7941d'),('app.queue_coverage_display_metadata_invalidation()','31ca82625a8fcbad0a70d7ebcca45d42'),('app.queue_coverage_display_source_invalidation()','d4b68fed41730857f2626f5978c0eb54'),('app.record_adopted_coverage_origin()','83409ea54725c9bee9df4c972e6e08e8'),('app.record_ingest_coverage_handoff()','9a177eb0d70e240c58e49ad1c3923d89'),('app.reject_immutable_read_model_write()','08c0ac90a547f8c1392c6b6230dfd040'),('app.reject_ingest_child_coalescing()','33053bce90ee44157a9cb6771c190743'),('app.reject_job_snapshot_update()','e0f753763fadf21dc57ff744475c9936'),('app.reject_workout_import_event_mutation()','b6b5bd5b924e56e5c1e9ae9c592a35d5'),('app.remove_workout_segment_park_attribution()','4a734fca59ee3a40575b271b89add1d5'),('app.require_active_job_account()','7a61da2d781e8c06b39e4332e327a88e'),('app.require_ingest_write_capability()','44dcd1f613dea838fb6c517726d6e67f'),('app.set_coverage_attribution_visit_date()','79beb84e1c94d94dc32eb570cb238c6c'),('app.stamp_coverage_applied_match_proof()','1ffdcef8a0031c6240c6d5c3e9caa8f3'),('app.stamp_ingest_source_file_receipt()','1d8d112ff11b170a0513ddf890de9d43'),('app.sync_coverage_matcher_slot_lease()','e441373bf89caad543d3cc7c07f493f0'),('app.sync_coverage_route_job_state()','6ecd49a7bd44fd790eb8c7e5177d02d0'),('app.track_source_sync_job()','f6ed4cac10fa5ca29ba01ceb65fc58c9'),('app.validate_map_selection()','5ae114239f9183fe5b38d66b9eb620e5'),('app.validate_map_selection_focus()','158e211dfb7bc449b4363feeb09439c9'),('app.validate_map_selection_workout()','7611212758a901583e5635c957de5490'),('app.wake_coverage_reconciliation()','0f864db6c36ffcda74e50d93fe2952a6')
    ) expected(signature,body_hash) LEFT JOIN pg_proc p ON p.oid=to_regprocedure(expected.signature)
    WHERE p.oid IS NULL OR md5(p.prosrc)<>expected.body_hash)
    AND NOT EXISTS(SELECT 1 FROM (VALUES ('account_deletions'),('account_purge_capabilities'),('private_staging_receipts')) expected(name)
      LEFT JOIN pg_class c ON c.oid=to_regclass('app.'||expected.name)
      WHERE c.oid IS NULL OR NOT c.relrowsecurity OR NOT c.relforcerowsecurity
      OR EXISTS(SELECT 1 FROM pg_roles r WHERE r.rolname IN ('workouts_api','workouts_worker','workouts_coverage_worker','workouts_tiles')
        AND (has_table_privilege(r.oid,c.oid,'INSERT') OR has_table_privilege(r.oid,c.oid,'UPDATE') OR has_table_privilege(r.oid,c.oid,'DELETE') OR has_table_privilege(r.oid,c.oid,'SELECT'))))
    AND NOT EXISTS(SELECT 1 FROM pg_class c JOIN pg_attribute a ON a.attrelid=c.oid
      WHERE c.relnamespace='app'::regnamespace AND c.relkind IN ('r','p') AND a.attname='account_id' AND NOT a.attisdropped
        AND c.relname NOT IN ('audit_records','users') AND NOT EXISTS(SELECT 1 FROM pg_trigger t
          WHERE t.tgrelid=c.oid AND t.tgname='account_active_api_write' AND t.tgenabled='O' AND NOT t.tgisinternal
          AND t.tgfoid=to_regprocedure('app.require_active_api_account()') AND t.tgtype=23))
))`
