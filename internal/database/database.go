package database

import (
	"context"
	"fmt"
	"time"

	"github.com/erhhung/workouts-explorer/internal/osm"
	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
)

const SupportedSchemaVersion = 28

func Open(ctx context.Context, databaseURL, applicationName string) (*pgxpool.Pool, error) {
	return OpenWithMaxConns(ctx, databaseURL, applicationName, 0)
}

func OpenWithMaxConns(ctx context.Context, databaseURL, applicationName string, maxConns int32) (*pgxpool.Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database configuration")
	}
	poolConfig.ConnConfig.RuntimeParams["application_name"] = applicationName
	poolConfig.ConnConfig.Tracer = newTracer()
	if maxConns > 0 {
		poolConfig.MaxConns = maxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("create database pool")
	}
	if err := recordPoolStats(pool, applicationName); err != nil {
		pool.Close()
		return nil, fmt.Errorf("register database pool metrics: %w", err)
	}
	return pool, nil
}

func recordPoolStats(pool otelpgx.PoolStats, applicationName string, options ...otelpgx.StatsOption) error {
	privacyOptions := []otelpgx.StatsOption{
		otelpgx.WithMinimumReadDBStatsInterval(5 * time.Second),
		otelpgx.WithStatsAttributes(semconv.DBClientConnectionPoolName(applicationName)),
	}
	return otelpgx.RecordStats(pool, append(privacyOptions, options...)...)
}

func newTracer(options ...otelpgx.Option) *otelpgx.Tracer {
	privacyOptions := []otelpgx.Option{
		otelpgx.WithDisableSQLStatementInAttributes(),
		otelpgx.WithDisableConnectionDetailsInAttributes(),
		otelpgx.WithSpanNameCtxFunc(func(context.Context, string) string { return "postgresql.query" }),
		otelpgx.WithTrimSQLInSpanName(),
		otelpgx.WithDisableQuerySpanNamePrefix(),
	}
	return otelpgx.NewTracer(append(privacyOptions, options...)...)
}

// Metadata permits compatible older runtimes during a rolling upgrade; the explicit object and privilege
// checks are the authoritative readiness contract for this runtime version.
const runtimeReadinessSQL = `
		SELECT COALESCE(max(version_id) FILTER (WHERE is_applied), 0) >= $1
		   AND to_regclass('app.schema_metadata') IS NOT NULL
		   AND to_regclass('app.jobs') IS NOT NULL
		   AND has_schema_privilege(current_user, 'app', 'USAGE')
		   AND has_table_privilege(current_user, 'app.schema_metadata', 'SELECT')
		   AND (current_user='workouts_coverage_worker' OR has_table_privilege(current_user, 'app.jobs', 'SELECT'))
		   AND (current_user='workouts_coverage_worker' OR has_table_privilege(current_user, 'app.jobs', 'INSERT'))
		   AND to_regclass('app.sources') IS NOT NULL
		   AND to_regclass('app.job_config_snapshots') IS NOT NULL
		   AND to_regclass('app.source_files') IS NOT NULL
		   AND to_regclass('app.workout_types') IS NOT NULL
		   AND to_regclass('app.workouts') IS NOT NULL
		   AND to_regclass('app.workout_aggregates') IS NOT NULL
		   AND to_regclass('app.workout_route_points') IS NOT NULL
		   AND to_regclass('app.workout_routes') IS NOT NULL
		   AND to_regclass('app.timezone_migration_function_backup') IS NOT NULL
		   AND to_regclass('app.workout_timezone_write_capabilities') IS NOT NULL
		   AND to_regclass('app.workout_coverage_states') IS NOT NULL
		   AND to_regclass('app.workout_coverage_regions') IS NOT NULL
		   AND to_regclass('app.coverage_diagnostic_runs') IS NOT NULL
		   AND to_regclass('app.coverage_diagnostic_generations') IS NOT NULL
		   AND to_regclass('app.coverage_diagnostic_evidence') IS NOT NULL
		   AND to_regclass('app.coverage_diagnostic_overall_labels') IS NOT NULL
		   AND to_regclass('app.coverage_diagnostic_segment_labels') IS NOT NULL
		   AND (SELECT count(*) FROM pg_constraint c
		       WHERE c.conrelid='app.coverage_diagnostic_runs'::regclass AND c.contype='c'
		         AND EXISTS (SELECT 1 FROM unnest(c.conkey) key(attnum)
		             JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=key.attnum
		             WHERE a.attname IN ('original_point_count','sampled_point_count')))=2
		   AND NOT EXISTS (
		       SELECT 1 FROM (VALUES ('coverage_diagnostic_runs'),('coverage_diagnostic_generations'),
		           ('coverage_diagnostic_evidence'),('coverage_diagnostic_overall_labels'),
		           ('coverage_diagnostic_segment_labels')) diagnostic(name)
		       LEFT JOIN pg_class c ON c.oid=to_regclass('app.'||diagnostic.name)
		       WHERE c.oid IS NULL OR NOT c.relrowsecurity OR NOT c.relforcerowsecurity
		         OR pg_get_userbyid(c.relowner)<>'workouts_security_owner'
		         OR NOT EXISTS (SELECT 1 FROM pg_policy p WHERE p.polrelid=c.oid
		             AND p.polname=diagnostic.name||'_account_policy' AND p.polcmd='*' AND p.polpermissive
		             AND p.polroles=ARRAY[0::oid]
		             AND pg_get_expr(p.polqual,p.polrelid)='(account_id = app.current_account_id())'
		             AND pg_get_expr(p.polwithcheck,p.polrelid)='(account_id = app.current_account_id())')
		         OR NOT EXISTS (SELECT 1 FROM pg_policy p WHERE p.polrelid=c.oid
		             AND p.polname=diagnostic.name||'_owner_policy' AND p.polcmd='*' AND p.polpermissive
		             AND p.polroles=ARRAY['workouts_security_owner'::regrole::oid]
		             AND pg_get_expr(p.polqual,p.polrelid)='true' AND pg_get_expr(p.polwithcheck,p.polrelid)='true')
		         OR (SELECT count(*) FROM pg_policy p WHERE p.polrelid=c.oid)<>2
		         OR NOT has_table_privilege('workouts_api',c.oid,'SELECT')
		         OR EXISTS (SELECT 1 FROM pg_roles r
		             WHERE r.rolname IN ('workouts_api','workouts_worker','workouts_coverage_worker','workouts_tiles')
		               AND (has_table_privilege(r.oid,c.oid,'INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
		                 OR has_any_column_privilege(r.oid,c.oid,'INSERT,UPDATE,REFERENCES')
		                 OR (r.rolname<>'workouts_api' AND has_any_column_privilege(r.oid,c.oid,'SELECT'))))
		         OR EXISTS (SELECT 1 FROM aclexplode(COALESCE(c.relacl,acldefault('r',c.relowner))) acl
		             WHERE acl.grantee<>c.relowner
		               AND (acl.grantee<>'workouts_api'::regrole::oid OR acl.privilege_type<>'SELECT' OR acl.is_grantable)))
		   AND NOT EXISTS (
		       SELECT 1 FROM (VALUES
		           ('app.persist_coverage_diagnostic(uuid,uuid,uuid,bigint,bytea,text,text,text,text,double precision,text,integer,integer,integer,integer,integer,integer,integer,integer,integer,integer,jsonb,jsonb)','search_path=pg_catalog, app, public'),
		           ('app.set_coverage_diagnostic_labels(uuid,uuid,boolean,text,jsonb)','search_path=pg_catalog, app')) expected(signature,path)
		       LEFT JOIN pg_proc p ON p.oid=to_regprocedure(expected.signature)
		       WHERE p.oid IS NULL OR NOT p.prosecdef OR pg_get_userbyid(p.proowner)<>'workouts_security_owner'
		         OR p.proconfig IS DISTINCT FROM ARRAY[expected.path]
		         OR NOT has_function_privilege('workouts_api',p.oid,'EXECUTE')
		         OR EXISTS (SELECT 1 FROM pg_roles r WHERE r.rolname IN ('workouts_worker','workouts_coverage_worker','workouts_tiles')
		             AND has_function_privilege(r.oid,p.oid,'EXECUTE'))
		         OR EXISTS (SELECT 1 FROM aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner))) acl
		             WHERE acl.grantee<>p.proowner AND (acl.grantee<>'workouts_api'::regrole::oid OR acl.is_grantable)))
		   AND to_regclass('app.coverage_job_contexts') IS NOT NULL
		   AND to_regclass('app.coverage_route_job_contexts') IS NOT NULL
		   AND to_regclass('app.coverage_job_progress') IS NOT NULL
		   AND to_regclass('app.matcher_slots') IS NOT NULL
		   AND to_regclass('app.matcher_slots_reader_active_idx') IS NOT NULL
		   AND EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid='app.matcher_slots'::regclass
		       AND attname='reader_cluster_key' AND atttypid='text'::regtype AND NOT attisdropped)
		   AND EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid='app.matcher_slots'::regclass
		       AND attname='reader_backend_key' AND atttypid='text'::regtype AND NOT attisdropped)
		   AND EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid='app.matcher_slot_limits'::regclass
		       AND attname='reader_limit' AND atttypid='integer'::regtype AND NOT attisdropped)
		   AND to_regclass('app.coverage_paths') IS NOT NULL
		   AND to_regclass('app.path_segments') IS NOT NULL
		   AND to_regclass('app.workout_segment_matches') IS NOT NULL
		   AND to_regclass('app.workout_path_attributions') IS NOT NULL
		   AND to_regclass('app.coverage_reconciliation_state') IS NOT NULL
		   AND to_regclass('app.coverage_reconciliation_accounts') IS NOT NULL
		   AND to_regclass('app.coverage_region_catalog') IS NOT NULL
		   AND to_regclass('app.account_path_daily_rollups') IS NOT NULL
		   AND to_regclass('app.account_path_all_time') IS NOT NULL
		   AND to_regclass('app.coverage_parks') IS NOT NULL
		   AND to_regclass('app.workout_park_attributions') IS NOT NULL
		   AND to_regclass('app.account_park_daily_rollups') IS NOT NULL
		   AND to_regclass('app.account_park_all_time') IS NOT NULL
		   AND EXISTS (SELECT 1 FROM pg_trigger
		       WHERE tgrelid='app.workout_coverage_states'::regclass
		         AND tgname='workout_coverage_states_data_generation_after_current' AND NOT tgisinternal)
		   AND EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid='app.workouts'::regclass
		       AND attname='timezone_dataset_release' AND NOT attisdropped)
		   AND to_regclass('app.workout_import_events') IS NOT NULL
		   AND to_regclass('app.ingest_write_capabilities') IS NOT NULL
		   AND to_regclass('app.workout_deletion_targets') IS NOT NULL
		   AND to_regclass('app.workout_deletion_capabilities') IS NOT NULL
		   AND to_regclass('app.job_source_contexts') IS NOT NULL
		   AND to_regclass('app.job_progress') IS NOT NULL
		   AND to_regclass('app.source_objects') IS NOT NULL
		   AND to_regclass('app.ingest_file_slot_guard') IS NOT NULL
		   AND to_regclass('app.ingest_file_slot_limits') IS NOT NULL
		   AND to_regclass('app.ingest_file_slots') IS NOT NULL
		   AND to_regclass('app.job_file_candidate_sets') IS NOT NULL
		   AND to_regclass('app.job_file_candidates') IS NOT NULL
		   AND EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid='app.job_file_candidates'::regclass
		       AND attname='observed_identity' AND NOT attisdropped)
		   AND to_regclass('app.job_events') IS NOT NULL
		   AND to_regclass('app.job_logs') IS NOT NULL
		   AND to_regclass('app.notifications') IS NOT NULL
		   AND to_regclass('app.source_sync_state') IS NOT NULL
		   AND to_regclass('app.auto_sync_policy') IS NOT NULL
		   AND to_regclass('app.account_sync_schedules') IS NOT NULL
		   AND EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid='app.account_sync_schedules'::regclass
		       AND attname='lease_runtime_version' AND atttypid='integer'::regtype AND NOT attisdropped)
		   AND EXISTS (SELECT 1 FROM pg_extension WHERE extname='postgis')
		   AND to_regclass('app.account_data_generations') IS NOT NULL
		   AND to_regclass('app.map_selections') IS NOT NULL
		   AND to_regclass('app.map_selection_workouts') IS NOT NULL
		   AND EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid='app.map_selections'::regclass
		       AND attname='focused_workout_id' AND NOT attisdropped)
		   AND to_regclass('app.workout_routes_route_gist_idx') IS NOT NULL
		   AND EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid='app.workout_routes'::regclass
		       AND attname='route' AND NOT attisdropped)
		   AND has_function_privilege(current_user, 'app.current_account_id()', 'EXECUTE')
		   AND (current_user IN ('workouts_worker','workouts_coverage_worker') OR has_function_privilege(current_user, 'app.current_session_id()', 'EXECUTE'))
		   AND (SELECT bool_and(pg_get_userbyid(p.proowner) = 'workouts_security_owner')
		          FROM pg_proc p
		         WHERE p.oid IN (
		             'app.claim_next_worker_job_internal(text,uuid,interval,boolean)'::regprocedure,
		             'app.claim_next_worker_job(text,uuid,interval)'::regprocedure,
		             'app.claim_next_worker_job(text,uuid,interval,integer)'::regprocedure,
		             'app.claim_next_source_connection_check(text,uuid,interval)'::regprocedure,
		             'app.fence_ingest_job(uuid,text,uuid)'::regprocedure,
		             'app.clear_ingest_write_capability()'::regprocedure,
		             'app.finish_job(uuid,text,uuid,text,text,text)'::regprocedure,
		             'app.read_typed_job_source_snapshot(uuid,text,uuid)'::regprocedure,
		             'app.refresh_source_credentials(uuid,text,uuid,bigint,jsonb)'::regprocedure,
		             'app.finish_connection_check_with_refresh(uuid,text,uuid,text,text,text,jsonb)'::regprocedure,
		             'app.fail_source_connection(uuid,text,uuid,bigint,text,text)'::regprocedure,
		             'app.lock_source_job_for_credentials(uuid,text,uuid)'::regprocedure,
		             'app.valid_source_config_envelope(jsonb)'::regprocedure,
		             'app.recover_expired_job(uuid)'::regprocedure,
		             'app.request_job_cancellation(uuid,uuid)'::regprocedure,
		             'app.request_owned_job_cancellation(uuid,uuid)'::regprocedure,
		             'app.delete_source(uuid,uuid)'::regprocedure,
		             'app.read_worker_job_log_context(uuid,text,uuid)'::regprocedure,
		             'app.record_ingest_progress(uuid,text,uuid,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint)'::regprocedure,
		             'app.record_job_event(uuid,text,uuid,text,jsonb)'::regprocedure,
		             'app.record_job_log(uuid,text,uuid,text,jsonb)'::regprocedure
		             ,'app.replace_workout_route_summary(uuid,integer,double precision,double precision,double precision,double precision,double precision,double precision,double precision,boolean)'::regprocedure
		             ,'app.invalidate_workout_route_summary()'::regprocedure
		             ,'app.configure_ingest_file_slot_limits(integer,integer)'::regprocedure
		             ,'app.acquire_ingest_file_slot(uuid,text,uuid)'::regprocedure
		             ,'app.release_ingest_file_slot(uuid,text,uuid,uuid)'::regprocedure
		             ,'app.record_ingest_file_manifest(uuid,text,uuid,jsonb)'::regprocedure
		             ,'app.record_successful_source_object(uuid,text,uuid,text,date,bigint,timestamp with time zone,text,bytea)'::regprocedure
		             ,'app.track_source_sync_job()'::regprocedure
		             ,'app.create_source_sync_state()'::regprocedure
		             ,'app.notify_terminal_ingest_parent()'::regprocedure
		             ,'app.evaluate_source_staleness(uuid,integer,timestamp with time zone)'::regprocedure
		             ,'app.dismiss_owned_notification(uuid,uuid)'::regprocedure
		             ,'app.read_owned_job_files(uuid,integer,integer)'::regprocedure
		             ,'app.read_owned_job_logs(uuid,integer,integer)'::regprocedure
		             ,'app.count_owned_job_rows(uuid,text)'::regprocedure
		             ,'app.configure_auto_sync_policy(interval,integer)'::regprocedure
		             ,'app.claim_due_sync_account(text,uuid,interval,integer)'::regprocedure
		             ,'app.claim_due_sync_account_internal(text,uuid,interval,integer)'::regprocedure
		             ,'app.read_leased_sync_sources(uuid,text,uuid)'::regprocedure
		             ,'app.enqueue_leased_scheduled_ingest(uuid,text,uuid,uuid,bytea,jsonb)'::regprocedure
		             ,'app.enqueue_leased_scheduled_ingest_internal(uuid,text,uuid,uuid,bytea,jsonb)'::regprocedure
		             ,'app.finish_sync_account(uuid,text,uuid,uuid)'::regprocedure
		             ,'app.finish_sync_account_internal(uuid,text,uuid,uuid)'::regprocedure
		             ,'app.release_sync_account(uuid,text,uuid,interval)'::regprocedure
		             ,'app.read_owned_sync_schedule()'::regprocedure
		             ,'app.create_legacy_ingest_read_models()'::regprocedure
		             ,'app.enqueue_workout_deletion(uuid,uuid)'::regprocedure
		             ,'app.enqueue_workout_range_deletion(date,date,uuid)'::regprocedure
		             ,'app.retry_workout_deletion(uuid,uuid,integer)'::regprocedure
		             ,'app.claim_next_workout_deletion(text,uuid,interval,integer)'::regprocedure
		             ,'app.fence_workout_deletion(uuid,text,uuid)'::regprocedure
		             ,'app.clear_workout_deletion_capability()'::regprocedure
		             ,'app.purge_workout_deletion(uuid,text,uuid)'::regprocedure
		             ,'app.workout_deletion_suppressed(uuid,text,text,bytea)'::regprocedure
		             ,'app.notify_failed_workout_deletion()'::regprocedure
		             ,'app.enforce_workout_deletion_target_lifecycle()'::regprocedure
		             ,'app.require_ingest_write_capability()'::regprocedure
		             ,'app.clear_workout_timezone_write_capability()'::regprocedure
		             ,'app.reject_workout_import_event_mutation()'::regprocedure
		             ,'app.advance_account_data_generation()'::regprocedure
		             ,'app.current_session_id()'::regprocedure
		             ,'app.seed_account_data_generation()'::regprocedure
		             ,'app.validate_map_selection()'::regprocedure
		             ,'app.validate_map_selection_workout()'::regprocedure
		             ,'app.cleanup_expired_map_selections()'::regprocedure
		             ,'app.lock_account_data_generation()'::regprocedure
		             ,'app.raw_route_mvt(integer,integer,integer,json)'::regprocedure
		             ,'app.set_workout_route_timezone(uuid,uuid,text,text)'::regprocedure
		             ,'app.reconcile_account_workout_timezones(uuid,uuid,text,uuid)'::regprocedure
		             ,'app.read_workout_timezone_backfill(uuid,uuid,integer)'::regprocedure
		             ,'app.reconcile_all_workout_timezones()'::regprocedure
		             ,'app.initialize_workout_coverage(uuid,uuid,bytea,uuid,text,uuid)'::regprocedure
		             ,'app.set_workout_coverage_readiness(uuid,uuid,bigint,text,text,jsonb,uuid,text,uuid)'::regprocedure
		             ,'app.persist_coverage_diagnostic(uuid,uuid,uuid,bigint,bytea,text,text,text,text,double precision,text,integer,integer,integer,integer,integer,integer,integer,integer,integer,integer,jsonb,jsonb)'::regprocedure
		             ,'app.set_coverage_diagnostic_labels(uuid,uuid,boolean,text,jsonb)'::regprocedure
		             ,'app.enqueue_coverage_update(uuid,uuid,text,bigint,bigint,text,text,text,double precision,jsonb)'::regprocedure
		             ,'app.claim_next_coverage_route(text,uuid,interval,integer)'::regprocedure
		             ,'app.acquire_coverage_matcher_slot(uuid,text,uuid,uuid)'::regprocedure
		             ,'app.acquire_diagnostic_matcher_slot(uuid,text,uuid,uuid,text[],interval)'::regprocedure
		             ,'app.release_matcher_slot(uuid,text,uuid)'::regprocedure
		             ,'app.assign_matcher_reader(uuid,text,uuid,text,text[])'::regprocedure
		             ,'app.clear_matcher_reader(uuid,text,uuid)'::regprocedure
		             ,'app.heartbeat_diagnostic_matcher_slot(uuid,text,uuid,interval)'::regprocedure
		             ,'app.job_cancel_requested(uuid,text,uuid)'::regprocedure
		             ,'app.retry_coverage_update(uuid,uuid,integer)'::regprocedure
		             ,'app.read_coverage_route(uuid,text,uuid)'::regprocedure
		             ,'app.persist_coverage_route(uuid,text,uuid,integer,jsonb,jsonb)'::regprocedure
		             ,'app.finish_coverage_route(uuid,text,uuid,text,integer,text,text)'::regprocedure
		             ,'app.fail_coverage_route(uuid,text,uuid,integer,text,text)'::regprocedure
		             ,'app.observe_coverage_reconciliation(bigint,text)'::regprocedure
		             ,'app.sync_coverage_region_catalog(jsonb)'::regprocedure
		             ,'app.claim_coverage_reconciliation(text,uuid,interval,integer)'::regprocedure
		             ,'app.apply_coverage_reconciliation(uuid,bigint,text,uuid,uuid,bigint,bytea,text,text,jsonb,uuid,uuid,text,text,text,double precision)'::regprocedure
		             ,'app.repair_coverage_reconciliation_input(uuid,bigint,text,uuid,uuid,bigint,bytea,bytea)'::regprocedure
		             ,'app.read_coverage_reconciliation_routes(uuid,bigint,text,uuid,uuid,integer)'::regprocedure
		             ,'app.read_coverage_reconciliation_route(uuid,bigint,text,uuid,uuid,bigint,bytea)'::regprocedure
		             ,'app.advance_coverage_reconciliation(uuid,bigint,text,uuid,uuid,boolean,interval)'::regprocedure
		             ,'app.supersede_coverage_route(uuid,text,uuid,integer)'::regprocedure
		             ,'app.map_selection_path_counts(uuid,uuid,uuid,bigint)'::regprocedure
		             ,'app.map_selection_coverage_entities(uuid,uuid,uuid,bigint,text)'::regprocedure
		             ,'app.map_selection_coverage_entity_detail(uuid,uuid,uuid,bigint,text,uuid)'::regprocedure
		             ,'app.coverage_count_bucket(bigint)'::regprocedure
		             ,'app.coverage_mvt(integer,integer,integer,json)'::regprocedure
		         ))
		   AND EXISTS (SELECT 1 FROM pg_trigger
		       WHERE tgname='job_config_snapshots_ingest_compatibility_after_insert' AND NOT tgisinternal)
		   AND EXISTS (SELECT 1 FROM pg_policy WHERE polname='workouts_api_not_deleted_policy'
		       AND polrelid='app.workouts'::regclass AND NOT polpermissive)
		   AND EXISTS (SELECT 1 FROM pg_policy WHERE polname='workout_timezone_capabilities_owner_policy'
		       AND polrelid='app.workout_timezone_write_capabilities'::regclass)
		   AND EXISTS (SELECT 1 FROM pg_trigger
		       WHERE tgname='workout_timezone_write_capability_cleanup' AND NOT tgisinternal)
		   AND has_table_privilege('workouts_security_owner','app.workout_timezone_write_capabilities','SELECT')
		   AND has_table_privilege('workouts_security_owner','app.workout_timezone_write_capabilities','INSERT')
		   AND has_table_privilege('workouts_security_owner','app.workout_timezone_write_capabilities','DELETE')
		   AND NOT has_table_privilege(current_user,'app.workout_timezone_write_capabilities','SELECT')
		   AND NOT has_table_privilege(current_user,'app.workout_timezone_write_capabilities','INSERT')
		   AND NOT has_table_privilege(current_user,'app.workout_timezone_write_capabilities','DELETE')
		   AND EXISTS (SELECT 1 FROM pg_trigger
		       WHERE tgname='workout_deletion_capability_cleanup' AND NOT tgisinternal)
		   AND EXISTS (SELECT 1 FROM pg_trigger
		       WHERE tgname='jobs_workout_deletion_failure_notification' AND NOT tgisinternal)
		   AND EXISTS (SELECT 1 FROM pg_trigger
		       WHERE tgname='workout_routes_data_generation_after_write' AND NOT tgisinternal)
		   AND EXISTS (SELECT 1 FROM pg_trigger
		       WHERE tgname='workouts_data_generation_after_write' AND NOT tgisinternal)
		   AND EXISTS (SELECT 1 FROM pg_trigger
		       WHERE tgname='map_selections_validate_before_write' AND NOT tgisinternal)
		   AND EXISTS (SELECT 1 FROM pg_trigger
		       WHERE tgname='map_selection_workouts_validate_before_write' AND NOT tgisinternal)
		   AND EXISTS (SELECT 1 FROM pg_trigger
		       WHERE tgname='map_selections_focus_validate_after_write' AND NOT tgisinternal)
		   AND position('worker runtime version 6 or newer is required' in
		       pg_get_functiondef('app.claim_next_worker_job(text,uuid,interval)'::regprocedure)) > 0
		   AND (SELECT bool_and(p.prosecdef AND p.proconfig=ARRAY['search_path=pg_catalog, app']
		       AND position('runtime_version >= 21 AND runtime_version >= metadata.minimum_runtime_version' in
		           regexp_replace(pg_get_functiondef(p.oid),'[[:space:]]+',' ','g')) > 0
		       AND position('FROM app.schema_metadata metadata WHERE metadata.singleton' in
		           regexp_replace(pg_get_functiondef(p.oid),'[[:space:]]+',' ','g')) > 0)
		       FROM pg_proc p WHERE p.oid IN (
		           'app.claim_next_worker_job(text,uuid,interval,integer)'::regprocedure,
		           'app.claim_due_sync_account(text,uuid,interval,integer)'::regprocedure))
		   AND (SELECT bool_and(p.prosecdef AND p.proconfig=ARRAY['search_path=pg_catalog, app']
		       AND NOT EXISTS (SELECT 1 FROM aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner))) acl
		           WHERE acl.grantee=0 AND acl.privilege_type='EXECUTE')
		       AND NOT EXISTS (SELECT 1 FROM pg_roles runtime_role
		           WHERE runtime_role.rolname IN ('workouts_api','workouts_worker','workouts_coverage_worker','workouts_tiles')
		             AND has_function_privilege(runtime_role.oid,p.oid,'EXECUTE')))
		       FROM pg_proc p WHERE p.oid IN (
		           'app.claim_due_sync_account_internal(text,uuid,interval,integer)'::regprocedure,
		           'app.enqueue_leased_scheduled_ingest_internal(uuid,text,uuid,uuid,bytea,jsonb)'::regprocedure,
		           'app.finish_sync_account_internal(uuid,text,uuid,uuid)'::regprocedure))
		   AND EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'workouts_security_owner'
		       AND NOT rolcanlogin AND NOT rolsuper AND NOT rolbypassrls)
		   AND EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'workouts_tiles'
		       AND rolcanlogin AND NOT rolsuper AND NOT rolcreatedb AND NOT rolcreaterole
		       AND NOT rolreplication AND NOT rolbypassrls)
		   AND has_schema_privilege('workouts_tiles','app','USAGE')
		   AND has_function_privilege('workouts_tiles','app.raw_route_mvt(integer,integer,integer,json)','EXECUTE')
		   AND has_function_privilege('workouts_tiles','app.coverage_mvt(integer,integer,integer,json)','EXECUTE')
		   AND NOT has_table_privilege('workouts_tiles','app.workout_routes','SELECT')
		   AND NOT has_table_privilege('workouts_tiles','app.workouts','SELECT')
		   AND NOT has_table_privilege('workouts_tiles','app.map_selections','SELECT')
		   AND NOT has_table_privilege('workouts_tiles','app.map_selection_workouts','SELECT')
		   AND NOT has_table_privilege('workouts_tiles','app.account_data_generations','SELECT')
		   AND NOT has_table_privilege('workouts_tiles','app.sessions','SELECT')
		   AND NOT has_table_privilege('workouts_tiles','app.workout_types','SELECT')
		   AND EXISTS (SELECT 1 FROM pg_roles WHERE rolname='workouts_coverage_worker'
		       AND rolcanlogin AND NOT rolsuper AND NOT rolcreatedb AND NOT rolcreaterole
		       AND NOT rolreplication AND NOT rolbypassrls)
		   AND has_schema_privilege('workouts_coverage_worker','app','USAGE')
		   AND has_function_privilege('workouts_coverage_worker','app.claim_next_coverage_route(text,uuid,interval,integer)','EXECUTE')
		   AND has_function_privilege('workouts_coverage_worker','app.persist_coverage_route(uuid,text,uuid,integer,jsonb,jsonb)','EXECUTE')
		   AND has_function_privilege('workouts_coverage_worker','app.coverage_route_timeout_retry_count(uuid,text,uuid)','EXECUTE')
		   AND has_function_privilege('workouts_coverage_worker','app.claim_coverage_reconciliation(text,uuid,interval,integer)','EXECUTE')
		   AND has_function_privilege('workouts_coverage_worker','app.sync_coverage_region_catalog(jsonb)','EXECUTE')
		   AND (SELECT p.prosecdef AND p.proconfig=ARRAY['search_path=pg_catalog, app']
		       AND position('runtime_version IS NULL OR runtime_version < 26' in pg_get_functiondef(p.oid)) > 0
		       AND has_function_privilege('workouts_coverage_worker',p.oid,'EXECUTE')
		       AND NOT has_function_privilege('workouts_api',p.oid,'EXECUTE')
		       AND NOT has_function_privilege('workouts_worker',p.oid,'EXECUTE')
		       AND NOT has_function_privilege('workouts_tiles',p.oid,'EXECUTE')
		       AND NOT EXISTS (SELECT 1 FROM aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner))) acl
		           WHERE acl.grantee=0 AND acl.privilege_type='EXECUTE')
		       FROM pg_proc p WHERE p.oid='app.claim_next_coverage_route(text,uuid,interval,integer)'::regprocedure)
		   AND (SELECT bool_and(p.prosecdef
		       AND position('job.cancel_requested_at IS NULL AND parent.cancel_requested_at IS NULL' in pg_get_functiondef(p.oid)) > 0)
		       FROM pg_proc p WHERE p.oid IN (
		           'app.persist_coverage_route(uuid,text,uuid,integer,jsonb,jsonb)'::regprocedure,
		           'app.finish_coverage_route(uuid,text,uuid,text,integer,text,text)'::regprocedure))
		   AND (SELECT bool_and(p.prosecdef AND p.proconfig=ARRAY['search_path=pg_catalog, app']
		       AND has_function_privilege('workouts_api',p.oid,'EXECUTE')
		       AND has_function_privilege('workouts_coverage_worker',p.oid,'EXECUTE')
		       AND NOT has_function_privilege('workouts_worker',p.oid,'EXECUTE')
		       AND NOT has_function_privilege('workouts_tiles',p.oid,'EXECUTE')
		       AND NOT EXISTS (SELECT 1 FROM aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner))) acl
		           WHERE acl.grantee=0 AND acl.privilege_type='EXECUTE'))
		       FROM pg_proc p WHERE p.oid IN (
		           'app.assign_matcher_reader(uuid,text,uuid,text,text[])'::regprocedure,
		           'app.clear_matcher_reader(uuid,text,uuid)'::regprocedure))
		   AND (SELECT p.prosecdef AND p.proconfig=ARRAY['search_path=pg_catalog, app']
		       AND has_function_privilege('workouts_api',p.oid,'EXECUTE')
		       AND NOT has_function_privilege('workouts_coverage_worker',p.oid,'EXECUTE')
		       AND NOT has_function_privilege('workouts_worker',p.oid,'EXECUTE')
		       AND NOT has_function_privilege('workouts_tiles',p.oid,'EXECUTE')
		       AND NOT EXISTS (SELECT 1 FROM aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner))) acl
		           WHERE acl.grantee=0 AND acl.privilege_type='EXECUTE')
		       FROM pg_proc p WHERE p.oid='app.heartbeat_diagnostic_matcher_slot(uuid,text,uuid,interval)'::regprocedure)
		   AND (SELECT p.prosecdef AND p.provolatile='s' AND p.proconfig=ARRAY['search_path=pg_catalog, app']
		       AND has_function_privilege('workouts_coverage_worker',p.oid,'EXECUTE')
		       AND NOT has_function_privilege('workouts_api',p.oid,'EXECUTE')
		       AND NOT has_function_privilege('workouts_worker',p.oid,'EXECUTE')
		       AND NOT has_function_privilege('workouts_tiles',p.oid,'EXECUTE')
		       AND NOT EXISTS (SELECT 1 FROM aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner))) acl
		           WHERE acl.grantee=0 AND acl.privilege_type='EXECUTE')
		       FROM pg_proc p WHERE p.oid='app.job_cancel_requested(uuid,text,uuid)'::regprocedure)
		   AND (SELECT bool_and(c.relrowsecurity AND c.relforcerowsecurity)
		       FROM pg_class c WHERE c.oid IN ('app.matcher_slots'::regclass,
		           'app.matcher_slot_limits'::regclass,'app.matcher_slot_guard'::regclass))
		   AND NOT EXISTS (SELECT 1 FROM pg_roles runtime_role
		       CROSS JOIN (VALUES ('app.matcher_slots'),('app.matcher_slot_limits'),('app.matcher_slot_guard')) private_table(name)
		       WHERE runtime_role.rolname IN ('workouts_api','workouts_worker','workouts_coverage_worker','workouts_tiles')
		         AND (has_table_privilege(runtime_role.oid,private_table.name,'SELECT,INSERT,UPDATE,DELETE')
		           OR has_any_column_privilege(runtime_role.oid,private_table.name,'SELECT,INSERT,UPDATE')))
		   AND (SELECT bool_and(p.prosecdef AND p.proconfig=ARRAY['search_path=pg_catalog, app']
		       AND has_function_privilege('workouts_coverage_worker',p.oid,'EXECUTE')
		       AND NOT EXISTS (SELECT 1 FROM aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner))) acl
		           WHERE acl.grantee=0 AND acl.privilege_type='EXECUTE')
		       AND NOT has_function_privilege('workouts_api',p.oid,'EXECUTE')
		       AND NOT has_function_privilege('workouts_worker',p.oid,'EXECUTE')
		       AND NOT has_function_privilege('workouts_tiles',p.oid,'EXECUTE'))
		       FROM pg_proc p WHERE p.oid IN (
		           'app.read_coverage_reconciliation_routes(uuid,bigint,text,uuid,uuid,integer)'::regprocedure,
		           'app.read_coverage_reconciliation_route(uuid,bigint,text,uuid,uuid,bigint,bytea)'::regprocedure,
		           'app.repair_coverage_reconciliation_input(uuid,bigint,text,uuid,uuid,bigint,bytea,bytea)'::regprocedure,
		           'app.advance_coverage_reconciliation(uuid,bigint,text,uuid,uuid,boolean,interval)'::regprocedure,
		           'app.read_coverage_route(uuid,text,uuid)'::regprocedure))
		   AND NOT has_table_privilege('workouts_coverage_worker','app.workout_coverage_states','INSERT,UPDATE,DELETE')
		   AND NOT has_function_privilege('workouts_worker','app.sync_coverage_region_catalog(jsonb)','EXECUTE')
		   AND NOT has_table_privilege('workouts_coverage_worker','app.coverage_region_catalog','SELECT,INSERT,UPDATE,DELETE')
		   AND NOT has_table_privilege('workouts_worker','app.coverage_region_catalog','SELECT,INSERT,UPDATE,DELETE')
		   AND has_column_privilege('workouts_coverage_worker','app.jobs','id','SELECT')
		   AND has_column_privilege('workouts_coverage_worker','app.jobs','account_id','SELECT')
		   AND has_column_privilege('workouts_coverage_worker','app.jobs','kind','SELECT')
		   AND has_column_privilege('workouts_coverage_worker','app.jobs','status','SELECT')
		   AND has_column_privilege('workouts_coverage_worker','app.jobs','cancel_requested_at','SELECT')
		   AND has_column_privilege('workouts_coverage_worker','app.jobs','worker_id','SELECT')
		   AND has_column_privilege('workouts_coverage_worker','app.jobs','lease_token','SELECT')
		   AND has_column_privilege('workouts_coverage_worker','app.jobs','lease_expires_at','SELECT')
		   AND has_column_privilege('workouts_coverage_worker','app.jobs','created_at','SELECT')
		   AND has_column_privilege('workouts_coverage_worker','app.jobs','attempt','SELECT')
		   AND has_column_privilege('workouts_coverage_worker','app.jobs','retry_of_job_id','SELECT')
		   AND NOT has_table_privilege('workouts_coverage_worker','app.jobs','SELECT')
		   AND NOT has_table_privilege('workouts_coverage_worker','app.jobs','INSERT')
		   AND NOT has_table_privilege('workouts_coverage_worker','app.workouts','SELECT')
		   AND NOT has_table_privilege('workouts_coverage_worker','app.workout_route_points','SELECT')
		   AND NOT has_function_privilege('workouts_worker','app.claim_next_coverage_route(text,uuid,interval,integer)','EXECUTE')
		   AND NOT has_function_privilege('workouts_worker','app.persist_coverage_route(uuid,text,uuid,integer,jsonb,jsonb)','EXECUTE')
		   AND NOT has_function_privilege('workouts_worker','app.coverage_route_timeout_retry_count(uuid,text,uuid)','EXECUTE')
		   AND has_table_privilege('workouts_security_owner', 'app.jobs', 'SELECT')
		   AND has_table_privilege('workouts_security_owner', 'app.sources', 'SELECT')
		   AND has_column_privilege('workouts_security_owner', 'app.sources', 'generation', 'UPDATE')
		   AND NOT has_function_privilege(current_user, 'app.lock_source_job_for_credentials(uuid,text,uuid)', 'EXECUTE')
		   AND NOT has_function_privilege(current_user, 'app.valid_source_config_envelope(jsonb)', 'EXECUTE')
		   AND (current_user='workouts_worker' OR (
		       NOT has_function_privilege(current_user, 'app.read_typed_job_source_snapshot(uuid,text,uuid)', 'EXECUTE')
		       AND NOT has_function_privilege(current_user, 'app.refresh_source_credentials(uuid,text,uuid,bigint,jsonb)', 'EXECUTE')
		       AND NOT has_function_privilege(current_user, 'app.finish_connection_check_with_refresh(uuid,text,uuid,text,text,text,jsonb)', 'EXECUTE')
		       AND NOT has_function_privilege(current_user, 'app.fail_source_connection(uuid,text,uuid,bigint,text,text)', 'EXECUTE')
		   ))
		   AND has_table_privilege('workouts_security_owner', 'app.preferences', 'SELECT')
		   AND has_table_privilege('workouts_security_owner', 'app.source_files', 'SELECT')
		   AND has_table_privilege('workouts_security_owner', 'app.job_source_contexts', 'SELECT')
		   AND has_table_privilege('workouts_security_owner', 'app.auto_sync_policy', 'SELECT')
		   AND has_table_privilege('workouts_security_owner', 'app.account_sync_schedules', 'SELECT')
		   AND has_column_privilege('workouts_security_owner', 'app.account_sync_schedules', 'lease_runtime_version', 'UPDATE')
		   AND has_table_privilege('workouts_security_owner', 'app.schema_metadata', 'SELECT')
		   AND has_table_privilege('workouts_security_owner', 'app.workout_deletion_targets', 'SELECT')
		   AND has_table_privilege('workouts_security_owner', 'app.workouts', 'DELETE')
		   AND has_table_privilege('workouts_security_owner', 'app.workout_import_events', 'DELETE')
		   AND has_column_privilege('workouts_security_owner', 'app.accounts', 'state', 'UPDATE')
		   AND (current_user IN ('workouts_worker','workouts_coverage_worker') OR (
		       to_regclass('app.authentication_principals') IS NOT NULL
		       AND to_regclass('app.sessions') IS NOT NULL
		       AND has_column_privilege(current_user, 'app.authentication_principals', 'password_hash', 'SELECT')
		       AND has_column_privilege(current_user, 'app.authentication_principals', 'full_name', 'UPDATE')
		       AND has_column_privilege(current_user, 'app.sessions', 'credential_verifier', 'INSERT')
		       AND has_function_privilege(current_user, 'app.consume_rate_limit(text,text,bytea)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.issue_password_reset(text,boolean,bytea)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.complete_password_reset(bytea,text,uuid,text)', 'EXECUTE')
		       AND has_column_privilege(current_user, 'app.sources', 'config_envelope', 'SELECT')
		       AND has_column_privilege(current_user, 'app.sources', 'config_envelope', 'UPDATE')
		       AND has_column_privilege(current_user, 'app.sources', 'canonical_display_name', 'INSERT')
		       AND has_column_privilege(current_user, 'app.sources', 'canonical_display_name', 'UPDATE')
		       AND has_function_privilege(current_user, 'app.delete_source(uuid,uuid)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.request_owned_job_cancellation(uuid,uuid)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.request_job_cancellation(uuid,uuid)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.enqueue_workout_deletion(uuid,uuid)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.enqueue_workout_range_deletion(date,date,uuid)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.retry_workout_deletion(uuid,uuid,integer)', 'EXECUTE')
		       AND NOT has_function_privilege(current_user, 'app.claim_next_workout_deletion(text,uuid,interval,integer)', 'EXECUTE')
		       AND NOT has_function_privilege(current_user, 'app.fence_workout_deletion(uuid,text,uuid)', 'EXECUTE')
		       AND NOT has_function_privilege(current_user, 'app.purge_workout_deletion(uuid,text,uuid)', 'EXECUTE')
		       AND NOT has_function_privilege(current_user, 'app.workout_deletion_suppressed(uuid,text,text,bytea)', 'EXECUTE')
		       AND NOT has_column_privilege(current_user, 'app.sources', 'deleted_at', 'UPDATE')
		       AND has_column_privilege(current_user, 'app.job_config_snapshots', 'config_envelope', 'INSERT')
		       AND has_column_privilege(current_user, 'app.job_config_snapshots', 'source_id', 'SELECT')
		       AND NOT has_column_privilege(current_user, 'app.job_config_snapshots', 'config_envelope', 'SELECT')
		       AND NOT has_column_privilege(current_user, 'app.job_config_snapshots', 'created_at', 'SELECT')
		       AND has_table_privilege(current_user, 'app.source_files', 'SELECT')
		       AND has_function_privilege(current_user, 'app.read_owned_job_files(uuid,integer,integer)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.read_owned_job_logs(uuid,integer,integer)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.count_owned_job_rows(uuid,text)', 'EXECUTE')
		       AND has_table_privilege(current_user, 'app.workouts', 'SELECT')
		       AND has_table_privilege(current_user, 'app.workout_routes', 'SELECT')
		       AND has_table_privilege(current_user, 'app.workout_coverage_states', 'SELECT')
		       AND NOT has_table_privilege(current_user, 'app.workout_coverage_states', 'INSERT')
		       AND NOT has_table_privilege(current_user, 'app.workout_coverage_states', 'UPDATE')
		       AND has_table_privilege(current_user, 'app.coverage_diagnostic_runs', 'SELECT')
		       AND has_table_privilege(current_user, 'app.coverage_diagnostic_generations', 'SELECT')
		       AND has_table_privilege(current_user, 'app.coverage_diagnostic_evidence', 'SELECT')
		       AND has_table_privilege(current_user, 'app.coverage_diagnostic_overall_labels', 'SELECT')
		       AND has_table_privilege(current_user, 'app.coverage_diagnostic_segment_labels', 'SELECT')
		       AND NOT has_table_privilege(current_user, 'app.coverage_diagnostic_runs', 'INSERT,UPDATE,DELETE')
		       AND NOT has_table_privilege(current_user, 'app.coverage_diagnostic_evidence', 'INSERT,UPDATE,DELETE')
		       AND NOT has_table_privilege(current_user, 'app.coverage_diagnostic_overall_labels', 'INSERT,UPDATE,DELETE')
		       AND has_function_privilege(current_user, 'app.persist_coverage_diagnostic(uuid,uuid,uuid,bigint,bytea,text,text,text,text,double precision,text,integer,integer,integer,integer,integer,integer,integer,integer,integer,integer,jsonb,jsonb)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.set_coverage_diagnostic_labels(uuid,uuid,boolean,text,jsonb)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.acquire_diagnostic_matcher_slot(uuid,text,uuid,uuid,text[],interval)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.release_matcher_slot(uuid,text,uuid)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.retry_coverage_update(uuid,uuid,integer)', 'EXECUTE')
		       AND has_table_privilege(current_user, 'app.coverage_paths', 'SELECT')
		       AND has_table_privilege(current_user, 'app.path_segments', 'SELECT')
		       AND has_table_privilege(current_user, 'app.workout_segment_matches', 'SELECT')
		       AND has_table_privilege(current_user, 'app.workout_path_attributions', 'SELECT')
		       AND has_table_privilege(current_user, 'app.account_path_daily_rollups', 'SELECT')
		       AND has_table_privilege(current_user, 'app.account_path_all_time', 'SELECT')
		       AND has_table_privilege(current_user, 'app.coverage_parks', 'SELECT')
		       AND has_table_privilege(current_user, 'app.workout_park_attributions', 'SELECT')
		       AND has_table_privilege(current_user, 'app.account_park_daily_rollups', 'SELECT')
		       AND has_table_privilege(current_user, 'app.account_park_all_time', 'SELECT')
		       AND has_function_privilege(current_user, 'app.map_selection_path_counts(uuid,uuid,uuid,bigint)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.map_selection_coverage_entities(uuid,uuid,uuid,bigint,text)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.map_selection_coverage_entity_detail(uuid,uuid,uuid,bigint,text,uuid)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.coverage_count_bucket(bigint)', 'EXECUTE')
		       AND NOT has_table_privilege(current_user, 'app.workout_segment_matches', 'INSERT,UPDATE,DELETE')
		       AND has_table_privilege(current_user, 'app.account_data_generations', 'SELECT')
		       AND has_table_privilege(current_user, 'app.map_selections', 'SELECT')
		       AND has_table_privilege(current_user, 'app.map_selections', 'INSERT')
		       AND has_table_privilege(current_user, 'app.map_selections', 'DELETE')
		       AND NOT has_table_privilege(current_user, 'app.map_selections', 'UPDATE')
		       AND has_table_privilege(current_user, 'app.map_selection_workouts', 'SELECT')
		       AND has_table_privilege(current_user, 'app.map_selection_workouts', 'INSERT')
		       AND has_table_privilege(current_user, 'app.map_selection_workouts', 'DELETE')
		       AND NOT has_table_privilege(current_user, 'app.map_selection_workouts', 'UPDATE')
		       AND has_function_privilege(current_user, 'app.cleanup_expired_map_selections()', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.lock_account_data_generation()', 'EXECUTE')
		       AND NOT has_function_privilege(current_user, 'app.raw_route_mvt(integer,integer,integer,json)', 'EXECUTE')
		       AND NOT has_function_privilege(current_user, 'app.coverage_mvt(integer,integer,integer,json)', 'EXECUTE')
		       AND has_table_privilege(current_user, 'app.workout_import_events', 'SELECT')
		       AND has_column_privilege(current_user, 'app.workout_import_events', 'warnings', 'SELECT')
		       AND NOT has_table_privilege(current_user, 'app.workouts', 'INSERT')
		       AND NOT has_table_privilege(current_user, 'app.workouts', 'DELETE')
		       AND NOT has_table_privilege(current_user, 'app.workout_deletion_targets', 'SELECT')
		       AND NOT has_table_privilege(current_user, 'app.workout_deletion_targets', 'INSERT')
		       AND NOT has_table_privilege(current_user, 'app.workout_deletion_targets', 'UPDATE')
		       AND NOT has_table_privilege(current_user, 'app.workout_deletion_targets', 'DELETE')
		       AND NOT has_table_privilege(current_user, 'app.workout_deletion_capabilities', 'SELECT')
		       AND NOT has_table_privilege(current_user, 'app.workout_deletion_capabilities', 'INSERT')
		       AND NOT has_table_privilege(current_user, 'app.workout_deletion_capabilities', 'DELETE')
		       AND has_table_privilege(current_user, 'app.job_source_contexts', 'SELECT')
		       AND has_column_privilege(current_user, 'app.job_source_contexts', 'job_id', 'INSERT')
		       AND has_column_privilege(current_user, 'app.job_source_contexts', 'source_type', 'INSERT')
		       AND has_table_privilege(current_user, 'app.job_progress', 'SELECT')
		       AND has_column_privilege(current_user, 'app.job_progress', 'job_id', 'INSERT')
		       AND NOT has_table_privilege(current_user, 'app.job_progress', 'UPDATE')
		       AND has_column_privilege(current_user, 'app.job_events', 'safe_message', 'SELECT')
		       AND NOT has_table_privilege(current_user, 'app.job_events', 'SELECT')
		       AND has_column_privilege(current_user, 'app.notifications', 'message', 'SELECT')
		       AND NOT has_table_privilege(current_user, 'app.notifications', 'SELECT')
		       AND NOT has_table_privilege(current_user, 'app.job_logs', 'SELECT')
		       AND has_column_privilege(current_user, 'app.source_sync_state', 'account_id', 'SELECT')
		       AND has_column_privilege(current_user, 'app.source_sync_state', 'source_id', 'SELECT')
		       AND has_column_privilege(current_user, 'app.source_sync_state', 'last_sync_started_at', 'SELECT')
		       AND has_column_privilege(current_user, 'app.source_sync_state', 'last_sync_succeeded_at', 'SELECT')
		       AND has_column_privilege(current_user, 'app.source_sync_state', 'last_new_export_discovered_at', 'SELECT')
		       AND has_column_privilege(current_user, 'app.source_sync_state', 'last_new_export_date', 'SELECT')
		       AND has_column_privilege(current_user, 'app.source_sync_state', 'stale_since', 'SELECT')
		       AND NOT has_table_privilege(current_user, 'app.source_sync_state', 'SELECT')
		       AND has_function_privilege(current_user, 'app.evaluate_source_staleness(uuid,integer,timestamp with time zone)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.dismiss_owned_notification(uuid,uuid)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.read_owned_sync_schedule()', 'EXECUTE')
		       AND NOT has_function_privilege(current_user, 'app.configure_auto_sync_policy(interval,integer)', 'EXECUTE')
		       AND NOT has_function_privilege(current_user, 'app.claim_due_sync_account(text,uuid,interval,integer)', 'EXECUTE')
		       AND NOT has_function_privilege(current_user, 'app.read_leased_sync_sources(uuid,text,uuid)', 'EXECUTE')
		       AND NOT has_function_privilege(current_user, 'app.enqueue_leased_scheduled_ingest(uuid,text,uuid,uuid,bytea,jsonb)', 'EXECUTE')
		       AND NOT has_function_privilege(current_user, 'app.finish_sync_account(uuid,text,uuid,uuid)', 'EXECUTE')
		       AND NOT has_function_privilege(current_user, 'app.release_sync_account(uuid,text,uuid,interval)', 'EXECUTE')
		       AND NOT has_table_privilege(current_user, 'app.auto_sync_policy', 'SELECT')
		       AND NOT has_table_privilege(current_user, 'app.auto_sync_policy', 'INSERT')
		       AND NOT has_table_privilege(current_user, 'app.auto_sync_policy', 'UPDATE')
		       AND NOT has_table_privilege(current_user, 'app.auto_sync_policy', 'DELETE')
		       AND NOT has_table_privilege(current_user, 'app.account_sync_schedules', 'SELECT')
		       AND NOT has_table_privilege(current_user, 'app.account_sync_schedules', 'INSERT')
		       AND NOT has_table_privilege(current_user, 'app.account_sync_schedules', 'UPDATE')
		       AND NOT has_table_privilege(current_user, 'app.account_sync_schedules', 'DELETE')
		   ))
		   AND EXISTS (
		       SELECT 1 FROM pg_roles
		       WHERE rolname = current_user AND rolcanlogin AND NOT rolsuper AND NOT rolbypassrls
		         AND rolname IN ('workouts_api', 'workouts_worker', 'workouts_coverage_worker')
		   )
		   AND (current_user <> 'workouts_worker' OR (
		       has_function_privilege(current_user, 'app.claim_job(uuid,text,uuid,interval)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.heartbeat_job(uuid,text,uuid,interval)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.finish_job(uuid,text,uuid,text,text,text)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.recover_expired_job(uuid)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.read_job_config_snapshot(uuid,text,uuid)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.read_typed_job_source_snapshot(uuid,text,uuid)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.refresh_source_credentials(uuid,text,uuid,bigint,jsonb)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.finish_connection_check_with_refresh(uuid,text,uuid,text,text,text,jsonb)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.fail_source_connection(uuid,text,uuid,bigint,text,text)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.claim_next_worker_job(text,uuid,interval)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.fence_ingest_job(uuid,text,uuid,integer)', 'EXECUTE')
		       AND has_table_privilege(current_user, 'app.source_files', 'INSERT')
		       AND has_table_privilege(current_user, 'app.workouts', 'UPDATE')
		       AND has_function_privilege(current_user, 'app.replace_workout_route_summary(uuid,integer,double precision,double precision,double precision,double precision,double precision,double precision,double precision,boolean)', 'EXECUTE')
		       AND has_table_privilege(current_user, 'app.workout_import_events', 'INSERT')
		       AND has_column_privilege(current_user, 'app.workout_import_events', 'warnings', 'INSERT')
		       AND has_function_privilege(current_user, 'app.valid_workout_warnings(jsonb)', 'EXECUTE')
		   AND has_function_privilege(current_user, 'app.initialize_workout_coverage(uuid,uuid,bytea,uuid,text,uuid)', 'EXECUTE')
		   AND has_function_privilege(current_user, 'app.set_workout_coverage_readiness(uuid,uuid,bigint,text,text,jsonb,uuid,text,uuid)', 'EXECUTE')
		   AND has_function_privilege(current_user, 'app.enqueue_coverage_update(uuid,uuid,text,bigint,bigint,text,text,text,double precision,jsonb)', 'EXECUTE')
		   AND NOT has_table_privilege(current_user, 'app.coverage_paths', 'INSERT,UPDATE,DELETE')
		   AND NOT has_table_privilege(current_user, 'app.path_segments', 'INSERT,UPDATE,DELETE')
		   AND NOT has_table_privilege(current_user, 'app.workout_segment_matches', 'INSERT,UPDATE,DELETE')
		       AND has_function_privilege(current_user, 'app.read_worker_job_log_context(uuid,text,uuid)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.record_ingest_progress(uuid,text,uuid,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.record_job_event(uuid,text,uuid,text,jsonb)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.record_job_log(uuid,text,uuid,text,jsonb)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.claim_next_worker_job(text,uuid,interval,integer)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.claim_next_workout_deletion(text,uuid,interval,integer)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.fence_workout_deletion(uuid,text,uuid)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.purge_workout_deletion(uuid,text,uuid)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.workout_deletion_suppressed(uuid,text,text,bytea)', 'EXECUTE')
		       AND NOT has_function_privilege(current_user, 'app.enqueue_workout_deletion(uuid,uuid)', 'EXECUTE')
		       AND NOT has_function_privilege(current_user, 'app.enqueue_workout_range_deletion(date,date,uuid)', 'EXECUTE')
		       AND NOT has_function_privilege(current_user, 'app.retry_workout_deletion(uuid,uuid,integer)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.configure_ingest_file_slot_limits(integer,integer)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.acquire_ingest_file_slot(uuid,text,uuid)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.release_ingest_file_slot(uuid,text,uuid,uuid)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.record_ingest_file_manifest(uuid,text,uuid,jsonb)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.record_successful_source_object(uuid,text,uuid,text,date,bigint,timestamp with time zone,text,bytea)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.configure_auto_sync_policy(interval,integer)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.claim_due_sync_account(text,uuid,interval,integer)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.read_leased_sync_sources(uuid,text,uuid)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.enqueue_leased_scheduled_ingest(uuid,text,uuid,uuid,bytea,jsonb)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.finish_sync_account(uuid,text,uuid,uuid)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.release_sync_account(uuid,text,uuid,interval)', 'EXECUTE')
		       AND NOT has_function_privilege(current_user, 'app.read_owned_sync_schedule()', 'EXECUTE')
		       AND has_column_privilege(current_user, 'app.job_progress', 'job_id', 'SELECT')
		       AND has_column_privilege(current_user, 'app.job_progress', 'account_id', 'SELECT')
		       AND has_column_privilege(current_user, 'app.job_progress', 'files_discovered', 'SELECT')
		       AND has_column_privilege(current_user, 'app.job_progress', 'files_skipped', 'SELECT')
		       AND has_column_privilege(current_user, 'app.job_progress', 'files_succeeded', 'SELECT')
		       AND has_column_privilege(current_user, 'app.job_progress', 'files_failed', 'SELECT')
		       AND has_column_privilege(current_user, 'app.job_progress', 'workouts_created', 'SELECT')
		       AND has_column_privilege(current_user, 'app.job_progress', 'workouts_updated', 'SELECT')
		       AND has_column_privilege(current_user, 'app.job_progress', 'workouts_unchanged', 'SELECT')
		       AND has_column_privilege(current_user, 'app.job_progress', 'workouts_rejected', 'SELECT')
		       AND NOT has_table_privilege(current_user, 'app.job_progress', 'SELECT')
		       AND has_column_privilege(current_user, 'app.source_objects', 'observed_identity', 'SELECT')
		       AND NOT has_table_privilege(current_user, 'app.source_objects', 'INSERT')
		       AND NOT has_table_privilege(current_user, 'app.ingest_file_slots', 'SELECT')
		       AND NOT has_table_privilege(current_user, 'app.ingest_file_slot_limits', 'SELECT')
		       AND NOT has_table_privilege(current_user, 'app.job_file_candidates', 'SELECT')
		       AND NOT has_function_privilege(current_user, 'app.request_owned_job_cancellation(uuid,uuid)', 'EXECUTE')
		       AND NOT has_function_privilege(current_user, 'app.request_job_cancellation(uuid,uuid)', 'EXECUTE')
		       AND NOT has_table_privilege(current_user, 'app.job_progress', 'UPDATE')
		       AND NOT has_table_privilege(current_user, 'app.job_events', 'INSERT')
		       AND NOT has_table_privilege(current_user, 'app.job_logs', 'INSERT')
		       AND NOT has_table_privilege(current_user, 'app.workout_import_events', 'DELETE')
		       AND NOT has_table_privilege(current_user, 'app.ingest_write_capabilities', 'SELECT')
		       AND NOT has_table_privilege(current_user, 'app.ingest_write_capabilities', 'INSERT')
		       AND NOT has_table_privilege(current_user, 'app.workouts', 'DELETE')
		       AND NOT has_table_privilege(current_user, 'app.workout_import_events', 'DELETE')
		       AND NOT has_table_privilege(current_user, 'app.workout_deletion_targets', 'SELECT')
		       AND NOT has_table_privilege(current_user, 'app.workout_deletion_targets', 'INSERT')
		       AND NOT has_table_privilege(current_user, 'app.workout_deletion_targets', 'UPDATE')
		       AND NOT has_table_privilege(current_user, 'app.workout_deletion_targets', 'DELETE')
		       AND NOT has_table_privilege(current_user, 'app.workout_deletion_capabilities', 'SELECT')
		       AND NOT has_table_privilege(current_user, 'app.workout_deletion_capabilities', 'INSERT')
		       AND NOT has_table_privilege(current_user, 'app.workout_deletion_capabilities', 'DELETE')
		       AND NOT has_table_privilege(current_user, 'app.job_config_snapshots', 'SELECT')
		       AND NOT has_column_privilege(current_user, 'app.sources', 'config_envelope', 'SELECT')
		       AND NOT has_table_privilege(current_user, 'app.auto_sync_policy', 'SELECT')
		       AND NOT has_table_privilege(current_user, 'app.auto_sync_policy', 'INSERT')
		       AND NOT has_table_privilege(current_user, 'app.auto_sync_policy', 'UPDATE')
		       AND NOT has_table_privilege(current_user, 'app.auto_sync_policy', 'DELETE')
		       AND NOT has_table_privilege(current_user, 'app.account_sync_schedules', 'SELECT')
		       AND NOT has_table_privilege(current_user, 'app.account_sync_schedules', 'INSERT')
		       AND NOT has_table_privilege(current_user, 'app.account_sync_schedules', 'UPDATE')
		       AND NOT has_table_privilege(current_user, 'app.account_sync_schedules', 'DELETE')
		       AND NOT has_table_privilege(current_user, 'app.authentication_principals', 'SELECT')
		       AND NOT has_table_privilege(current_user, 'app.users', 'SELECT')
		       AND NOT has_table_privilege(current_user, 'app.administrators', 'SELECT')
		       AND NOT has_function_privilege(current_user, 'app.delete_source(uuid,uuid)', 'EXECUTE')
		   ))
		   AND NOT EXISTS (
		       SELECT 1 FROM (VALUES
		           ('app.accounts','workout_ownership_epoch','bigint',true),
		           ('app.ingest_write_capabilities','ownership_epoch','bigint',false),
		           ('app.ingest_write_capabilities','runtime_version','integer',false),
		           ('app.workouts','provider_namespace','text',true),
		           ('app.workout_deletion_targets','provider_namespace','text',true),
		           ('app.source_objects','ownership_blocked_epoch','bigint',false),
		           ('app.coverage_diagnostic_runs','original_point_count','integer',true),
		           ('app.coverage_diagnostic_runs','sampled_point_count','integer',true),
		           ('app.workout_import_events','has_route','boolean',false),
		           ('app.job_progress','workouts_created_with_route','bigint',false),
		           ('app.job_progress','workouts_updated_with_route','bigint',false),
		           ('app.coverage_job_contexts','matcher_contract_version','text',true),
		           ('app.coverage_job_contexts','parent_batch_size','integer',true),
		           ('app.coverage_job_contexts','sealed_ingest_root_id','uuid',false),
		           ('app.coverage_batch_policy','singleton','boolean',true),
		           ('app.coverage_batch_policy','batch_size','integer',true),
		           ('app.coverage_reconciliation_accounts','lease_runtime_version','integer',false),
		           ('app.ingest_coverage_handoffs','event_id','uuid',true),
		           ('app.ingest_coverage_handoffs','account_id','uuid',true),
		           ('app.ingest_coverage_handoffs','workout_id','uuid',true),
		           ('app.ingest_coverage_handoffs','source_id','uuid',true),
		           ('app.ingest_coverage_handoffs','source_file_id','uuid',true),
		           ('app.ingest_coverage_handoffs','ingest_job_id','uuid',true),
		           ('app.ingest_coverage_handoffs','origin','text',true),
		           ('app.ingest_coverage_handoffs','repair','boolean',true),
		           ('app.ingest_coverage_handoffs','content_sha256','bytea',true),
		           ('app.ingest_coverage_handoffs','ordinal','bigint',true),
		           ('app.ingest_coverage_handoffs','requested_at','timestamp with time zone',true),
		           ('app.ingest_coverage_handoffs','next_attempt_at','timestamp with time zone',true),
		           ('app.ingest_coverage_handoffs','acknowledged_at','timestamp with time zone',false),
		           ('app.ingest_coverage_handoffs','coverage_parent_job_id','uuid',false),
		           ('app.ingest_coverage_handoffs','coverage_child_job_id','uuid',false),
		           ('app.ingest_coverage_handoffs','outcome','text',false),
		           ('app.adopted_coverage_handoff_receipts','event_id','uuid',true),
		           ('app.adopted_coverage_handoff_receipts','account_id','uuid',true),
		           ('app.adopted_coverage_handoff_receipts','workout_id','uuid',true),
		           ('app.adopted_coverage_handoff_receipts','source_id','uuid',true),
		           ('app.adopted_coverage_handoff_receipts','source_file_id','uuid',true),
		           ('app.adopted_coverage_handoff_receipts','ingest_job_id','uuid',true),
		           ('app.adopted_coverage_handoff_receipts','import_event_id','uuid',false),
		           ('app.adopted_coverage_handoff_receipts','acknowledged_at','timestamp with time zone',false),
		           ('app.adopted_coverage_handoff_receipts','coverage_parent_job_id','uuid',false),
		           ('app.adopted_coverage_handoff_receipts','coverage_child_job_id','uuid',false),
		           ('app.coverage_retry_roots','root_job_id','uuid',true),
		           ('app.coverage_retry_roots','account_id','uuid',true),
		           ('app.coverage_retry_roots','enrolled_at','timestamp with time zone',false),
		           ('app.coverage_retry_roots','automatic_attempts_used','integer',true),
		           ('app.coverage_retry_attempts','job_id','uuid',true),
		           ('app.coverage_retry_attempts','root_job_id','uuid',true),
		           ('app.coverage_retry_attempts','predecessor_job_id','uuid',false),
		           ('app.coverage_retry_attempts','origin','text',true),
		           ('app.coverage_retry_attempts','automatic_ordinal','integer',false),
		           ('app.workout_adoption_capabilities','backend_pid','integer',true),
		           ('app.workout_adoption_capabilities','transaction_id','bigint',true),
		           ('app.workout_adoption_capabilities','account_id','uuid',true),
		           ('app.workout_adoption_capabilities','workout_id','uuid',true),
		           ('app.workout_adoption_capabilities','old_source_id','uuid',true),
		           ('app.workout_adoption_capabilities','new_source_id','uuid',true),
		           ('app.workout_adoption_capabilities','source_file_id','uuid',true),
		           ('app.workout_adoption_capabilities','job_id','uuid',true),
		           ('app.adopted_workout_coverage_refreshes','workout_id','uuid',true),
		           ('app.adopted_workout_coverage_refreshes','account_id','uuid',true),
		           ('app.adopted_workout_coverage_refreshes','event_id','uuid',true),
		           ('app.adopted_workout_coverage_refreshes','source_id','uuid',true),
		           ('app.adopted_workout_coverage_refreshes','source_file_id','uuid',true),
		           ('app.adopted_workout_coverage_refreshes','requested_at','timestamp with time zone',true),
		           ('app.adopted_workout_coverage_refreshes','next_attempt_at','timestamp with time zone',true)
		       ) expected(relation_name,column_name,type_name,not_null)
		       WHERE NOT EXISTS (SELECT 1 FROM pg_attribute a
		           WHERE a.attrelid=to_regclass(expected.relation_name) AND a.attname=expected.column_name
		             AND a.atttypid=to_regtype(expected.type_name) AND a.attnotnull=expected.not_null AND NOT a.attisdropped
		             AND (expected.column_name<>'provider_namespace' OR a.attcollation='pg_catalog."C"'::regcollation)))
		   AND NOT EXISTS (
		       SELECT 1 FROM (VALUES
		           ('app.workouts_account_provider_identity_idx','app.workouts',true,false,'account_id, provider_namespace, provider_id','(provider_id IS NOT NULL)'),
		           ('app.workout_deletion_targets_provider_namespace_idx','app.workout_deletion_targets',false,false,'account_id, provider_namespace, provider_id','(provider_id IS NOT NULL)'),
		           ('app.workout_import_events_ownership_blocked_file_idx','app.workout_import_events',false,false,'account_id, job_id, source_file_id','(kind = ''skipped_live_owner''::text)'),
		           ('app.workout_adoption_capabilities_pkey','app.workout_adoption_capabilities',true,true,'backend_pid, transaction_id, workout_id',NULL),
		           ('app.adopted_workout_coverage_refreshes_pkey','app.adopted_workout_coverage_refreshes',true,true,'account_id, workout_id',NULL),
		           ('app.coverage_batch_policy_pkey','app.coverage_batch_policy',true,true,'singleton',NULL),
		           ('app.ingest_coverage_handoffs_pkey','app.ingest_coverage_handoffs',true,true,'event_id',NULL),
		           ('app.ingest_coverage_handoffs_pending_idx','app.ingest_coverage_handoffs',false,false,'account_id, workout_id, ordinal','(acknowledged_at IS NULL)'),
		           ('app.ingest_coverage_handoffs_origin_idx','app.ingest_coverage_handoffs',false,false,'account_id, ingest_job_id, source_file_id',NULL),
		           ('app.ingest_coverage_handoffs_history_idx','app.ingest_coverage_handoffs',false,false,'account_id, workout_id, ordinal',NULL),
		           ('app.ingest_coverage_handoffs_repair_idx','app.ingest_coverage_handoffs',true,false,'ingest_job_id, workout_id','repair'),
		           ('app.adopted_coverage_handoff_receipts_pkey','app.adopted_coverage_handoff_receipts',true,true,'event_id',NULL),
		           ('app.adopted_coverage_handoff_receipts_workout_idx','app.adopted_coverage_handoff_receipts',false,false,'account_id, workout_id, acknowledged_at',NULL),
		           ('app.coverage_retry_roots_pkey','app.coverage_retry_roots',true,true,'root_job_id',NULL),
		           ('app.coverage_retry_roots_budget_idx','app.coverage_retry_roots',false,false,'automatic_attempts_used, root_job_id','(enrolled_at IS NOT NULL)'),
		           ('app.coverage_retry_attempts_pkey','app.coverage_retry_attempts',true,true,'job_id',NULL),
		           ('app.coverage_retry_attempts_root_job_id_automatic_ordinal_key','app.coverage_retry_attempts',true,false,'root_job_id, automatic_ordinal',NULL),
		           ('app.coverage_retry_attempts_predecessor_idx','app.coverage_retry_attempts',true,false,'predecessor_job_id','(origin <> ''observed''::text)'),
		           ('app.adopted_workout_coverage_refreshes_due_idx','app.adopted_workout_coverage_refreshes',false,false,'account_id, next_attempt_at',NULL)
		       ) expected(index_name,relation_name,is_unique,is_primary,columns,predicate)
		       WHERE NOT EXISTS (SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid
		           JOIN pg_am am ON am.oid=c.relam
		           WHERE i.indexrelid=to_regclass(expected.index_name) AND i.indrelid=to_regclass(expected.relation_name)
		             AND i.indisvalid AND i.indisready AND i.indisunique=expected.is_unique AND am.amname='btree'
		             AND NOT i.indnullsnotdistinct
		             AND i.indisprimary=expected.is_primary
		             AND (expected.index_name<>'app.ingest_coverage_handoffs_pending_idx' OR i.indoption::text='0 0 3')
		             AND (SELECT string_agg(pg_get_indexdef(i.indexrelid,n,true),', ' ORDER BY n)
		                 FROM generate_series(1,i.indnkeyatts) n)=expected.columns
		             AND pg_get_expr(i.indpred,i.indrelid) IS NOT DISTINCT FROM expected.predicate))
		   AND NOT EXISTS (
		       SELECT 1 FROM (VALUES
		           ('app.sources','sources_ownership_epoch_after_delete','app.advance_workout_ownership_epoch()',17),
		           ('app.jobs','coverage_retry_failure_enrollment','app.enroll_coverage_retry_failure()',17),
		           ('app.workout_import_events','workout_import_events_coverage_handoff','app.record_ingest_coverage_handoff()',5),
		           ('app.adopted_workout_coverage_refreshes','adopted_coverage_origin','app.record_adopted_coverage_origin()',21),
		           ('app.workouts','workouts_namespace_before_insert','app.enforce_ingest_identity()',7)
		       ) expected(relation_name,trigger_name,function_name,trigger_type)
		       WHERE NOT EXISTS (SELECT 1 FROM pg_trigger t
		           WHERE t.tgrelid=to_regclass(expected.relation_name) AND t.tgname=expected.trigger_name
		             AND t.tgfoid=to_regprocedure(expected.function_name) AND t.tgtype=expected.trigger_type
		             AND t.tgenabled IN ('O','A') AND NOT t.tgisinternal))
		   AND NOT EXISTS (
		       SELECT 1 FROM (VALUES
		           ('app.workout_adoption_capabilities','workout_adoption_capabilities_owner_policy'),
		           ('app.ingest_write_capabilities','ingest_write_capabilities_owner_policy'),
		           ('app.coverage_retry_roots','coverage_retry_roots_owner_policy'),
		           ('app.coverage_retry_attempts','coverage_retry_attempts_owner_policy'),
		           ('app.coverage_batch_policy','coverage_batch_policy_owner_policy'),
		           ('app.ingest_coverage_handoffs','ingest_coverage_handoffs_owner_policy'),
		           ('app.adopted_coverage_handoff_receipts','adopted_coverage_handoff_receipts_owner_policy'),
		           ('app.ingest_coverage_builders','ingest_coverage_builders_owner_policy'),
		           ('app.ingest_coverage_prepared','ingest_coverage_prepared_owner_policy'),
		           ('app.adopted_workout_coverage_refreshes','adopted_workout_coverage_refreshes_owner_policy')
		       ) private_table(name,policy_name)
		       LEFT JOIN pg_class c ON c.oid=to_regclass(private_table.name)
		       WHERE c.oid IS NULL OR NOT c.relrowsecurity OR NOT c.relforcerowsecurity
		           OR pg_get_userbyid(c.relowner)<>'workouts_security_owner'
		           OR NOT EXISTS (SELECT 1 FROM pg_policy policy WHERE policy.polrelid=c.oid
		               AND policy.polname=private_table.policy_name AND policy.polcmd='*' AND policy.polpermissive
		               AND policy.polroles=ARRAY['workouts_security_owner'::regrole::oid]
		               AND pg_get_expr(policy.polqual,policy.polrelid)='true'
		               AND pg_get_expr(policy.polwithcheck,policy.polrelid)='true')
		           OR EXISTS (SELECT 1 FROM pg_policy policy WHERE policy.polrelid=c.oid
		               AND policy.polname<>private_table.policy_name)
		           OR EXISTS (SELECT 1 FROM (VALUES ('SELECT'),('INSERT'),('UPDATE'),('DELETE')) required(privilege)
		               WHERE NOT has_table_privilege('workouts_security_owner',c.oid,required.privilege))
		           OR EXISTS (SELECT 1 FROM pg_roles runtime_role
		               WHERE runtime_role.rolname IN ('workouts_api','workouts_worker','workouts_coverage_worker','workouts_tiles')
		                 AND (has_table_privilege(runtime_role.oid,c.oid,'SELECT,INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
		                   OR has_any_column_privilege(runtime_role.oid,c.oid,'SELECT,INSERT,UPDATE,REFERENCES')))
		           OR EXISTS (SELECT 1 FROM aclexplode(COALESCE(c.relacl,acldefault('r',c.relowner))) acl
		               WHERE acl.grantee<>c.relowner))
		   AND NOT EXISTS (
		       SELECT 1 FROM (VALUES
		           ('app.workout_adoption_capabilities','app.ingest_write_capabilities','backend_pid,transaction_id','backend_pid,transaction_id','c'),
		           ('app.coverage_retry_roots','app.jobs','root_job_id','id','c'),
		           ('app.coverage_retry_roots','app.accounts','account_id','id','c'),
		           ('app.coverage_retry_attempts','app.jobs','job_id','id','c'),
		           ('app.coverage_retry_attempts','app.coverage_retry_roots','root_job_id','root_job_id','c'),
		           ('app.coverage_retry_attempts','app.jobs','predecessor_job_id','id','c'),
		           ('app.ingest_coverage_handoffs','app.workouts','workout_id,account_id','id,account_id','c'),
		           ('app.adopted_coverage_handoff_receipts','app.workouts','workout_id,account_id','id,account_id','c'),
		           ('app.adopted_workout_coverage_refreshes','app.workouts','workout_id,account_id','id,account_id','c'),
		           ('app.workout_import_events','app.workouts','workout_id,account_id','id,account_id','r')
		       ) expected(relation_name,parent_name,columns,parent_columns,delete_action)
		       WHERE NOT EXISTS (SELECT 1 FROM pg_constraint constraint_row
		           WHERE constraint_row.conrelid=to_regclass(expected.relation_name)
		             AND constraint_row.confrelid=to_regclass(expected.parent_name)
		             AND constraint_row.contype='f' AND constraint_row.convalidated
		             AND constraint_row.confdeltype::text=expected.delete_action
		             AND (SELECT string_agg(a.attname,',' ORDER BY key.ordinality)
		                 FROM unnest(constraint_row.conkey) WITH ORDINALITY key(attnum,ordinality)
		                 JOIN pg_attribute a ON a.attrelid=constraint_row.conrelid AND a.attnum=key.attnum)=expected.columns
		             AND (SELECT string_agg(a.attname,',' ORDER BY key.ordinality)
		                 FROM unnest(constraint_row.confkey) WITH ORDINALITY key(attnum,ordinality)
		                 JOIN pg_attribute a ON a.attrelid=constraint_row.confrelid AND a.attnum=key.attnum)=expected.parent_columns))
		   AND NOT EXISTS (SELECT 1 FROM pg_constraint
		       WHERE conrelid='app.adopted_workout_coverage_refreshes'::regclass AND contype='f'
		         AND confrelid='app.accounts'::regclass)
		   AND NOT EXISTS (
		       SELECT 1 FROM (VALUES
		           ('event_id','gen_random_uuid()'),
		           ('requested_at','clock_timestamp()'),
		           ('next_attempt_at','''-infinity''::timestamp with time zone')
		       ) expected(column_name,default_expression)
		       WHERE NOT EXISTS (SELECT 1 FROM pg_attribute a JOIN pg_attrdef d
		           ON d.adrelid=a.attrelid AND d.adnum=a.attnum
		           WHERE a.attrelid='app.adopted_workout_coverage_refreshes'::regclass AND a.attname=expected.column_name
		             AND pg_get_expr(d.adbin,d.adrelid)=expected.default_expression))
		   AND NOT EXISTS (
		       SELECT 1 FROM (VALUES ('app.workout_import_events','has_route'),
		           ('app.coverage_retry_roots','enrolled_at'),('app.coverage_retry_attempts','automatic_ordinal'),
		           ('app.coverage_retry_attempts','predecessor_job_id'),('app.coverage_retry_attempts','origin')) expected(relation_name,column_name)
		       JOIN pg_attribute a ON a.attrelid=to_regclass(expected.relation_name) AND a.attname=expected.column_name
		       JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum)
		   AND NOT EXISTS (
		       SELECT 1 FROM (VALUES
		           ('app.workout_provider_namespace(text)',false,false,false,false,'i'),
		           ('app.bump_workout_ownership_epoch(uuid)',true,false,false,false,'v'),
		           ('app.source_workout_ownership_changed()',true,false,false,false,'v'),
		           ('app.valid_ingest_ownership_epoch(uuid,bigint,integer)',true,false,false,false,'v'),
		           ('app.enforce_ingest_identity()',true,false,false,false,'v'),
		           ('app.advance_workout_ownership_epoch()',true,false,false,false,'v'),
		           ('app.fence_ingest_job(uuid,text,uuid)',true,true,false,false,'v'),
		           ('app.fence_ingest_job(uuid,text,uuid,integer)',true,true,false,false,'v'),
		           ('app.resolve_ingest_workout_identity(uuid,text,uuid,uuid,uuid,text,text,bytea)',true,true,false,false,'v'),
		           ('app.read_ingest_candidate_actions(uuid,text,uuid,jsonb)',true,true,false,false,'v'),
		           ('app.record_ingest_progress(uuid,text,uuid,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint)',true,true,false,false,'v'),
		           ('app.refresh_ingest_route_counts(uuid)',true,false,false,false,'v'),
		           ('app.retry_coverage_update(uuid,uuid,integer)',true,false,false,true,'v'),
		           ('app.read_coverage_retry_eligibility(uuid,integer)',true,false,false,true,'v'),
		           ('app.read_job_history(text,text,integer,bigint,integer)',true,false,false,true,'v'),
		           ('app.retry_next_coverage_update(integer,integer)',true,false,true,false,'v'),
		           ('app.construct_coverage_retry(uuid,uuid,integer,integer)',true,false,false,false,'v'),
		           ('app.coverage_retry_root(uuid)',true,false,false,false,'v'),
		           ('app.coverage_retry_targets(uuid,uuid)',true,false,false,false,'v'),
		           ('app.enroll_coverage_retry_failure()',true,false,false,false,'v'),
		           ('app.current_coverage_matcher_contract()',true,false,false,false,'s'),
		           ('app.coverage_auto_retry_parent_eligible(uuid,uuid)',true,false,false,false,'v'),
		           ('app.current_coverage_batch_size()',true,false,false,false,'s'),
		           ('app.configure_coverage_batch_size(integer,integer)',true,true,false,false,'v'),
		           ('app.record_ingest_coverage_handoff()',true,false,false,false,'v'),
		           ('app.record_adopted_coverage_origin()',true,false,false,false,'v'),
		           ('app.record_adopted_coverage_receipt(uuid,uuid,uuid)',true,false,false,false,'v'),
		           ('app.enqueue_coverage_update_core(uuid,uuid,text,bigint,bigint,text,text,text,double precision,jsonb,boolean)',true,false,false,false,'v'),
		           ('app.enqueue_adopted_coverage_update(uuid,uuid,text,bigint,bigint,text,text,text,double precision,jsonb)',true,false,false,false,'v'),
		           ('app.ingest_coverage_candidates(uuid,uuid)',true,false,false,false,'s'),
		           ('app.ingest_coverage_snapshot_digest(uuid,uuid)',true,false,false,false,'s'),
		           ('app.read_pending_ingest_coverage_workouts(uuid,uuid,text,uuid)',true,true,false,false,'v'),
		           ('app.read_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,uuid,integer,boolean)',true,true,true,false,'v'),
		           ('app.read_ingest_coverage_route_snapshot(uuid,uuid,text,uuid,bigint,uuid,uuid,text)',true,true,true,false,'s'),
		           ('app.read_coverage_reconciliation_snapshot(uuid,bigint,text,uuid,uuid,bigint,bytea)',true,false,true,false,'s'),
		           ('app.apply_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,jsonb,text,text,text,double precision,integer)',true,true,true,false,'v'),
		           ('app.ingest_coverage_root_terminal(uuid,uuid)',true,false,false,false,'s'),
		           ('app.ingest_coverage_cleanup_due(uuid)',true,false,false,false,'s'),
		           ('app.cleanup_ingest_coverage_origins(uuid,uuid,text,uuid,bigint)',true,false,false,false,'v'),
		           ('app.ingest_coverage_existing_target(uuid,uuid,bigint,bytea,jsonb,text,text,text,double precision)',true,false,false,false,'s'),
		           ('app.read_ingest_coverage_prepared(uuid,uuid,text,uuid,bigint,integer)',true,true,true,false,'v'),
		           ('app.ack_ingest_coverage_prepared(uuid,uuid,uuid,uuid,uuid,text)',true,false,false,false,'v'),
		           ('app.seal_ingest_coverage_cohort(uuid,uuid,text,uuid,bigint,uuid,jsonb,integer)',true,true,true,false,'v'),
		           ('app.read_coverage_job_scope(uuid)',true,false,false,true,'s'),
		           ('app.record_ingest_coverage_repair(uuid,uuid,text,uuid,uuid,integer)',true,true,false,false,'v'),
		           ('app.read_ingest_coverage_source_id(uuid,uuid,text,uuid)',true,true,false,false,'v'),
		           ('app.defer_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,jsonb,integer)',true,true,true,false,'v'),
		           ('app.apply_ingest_coverage_normal(uuid,uuid,bigint,text,uuid,uuid,bigint,bytea,text,text,jsonb,uuid,uuid,text,text,text,double precision)',true,false,false,false,'v'),
		           ('app.record_adopted_workout_coverage_refresh(uuid,text,uuid,uuid)',true,true,false,false,'v'),
		           ('app.read_pending_adopted_workout_coverage_refreshes(uuid,uuid,text,uuid)',true,true,false,false,'v'),
		           ('app.adopted_coverage_authority_current(uuid,uuid,text,uuid,bigint)',true,false,false,false,'v'),
		           ('app.adopted_coverage_route_digest(uuid,uuid)',true,false,false,false,'v'),
		           ('app.read_adopted_workout_coverage_refreshes(uuid,uuid,text,uuid,bigint,uuid,integer)',true,true,true,false,'v'),
		           ('app.read_adopted_workout_coverage_route(uuid,uuid,text,uuid,bigint,uuid,uuid)',true,true,true,false,'v'),
		           ('app.apply_adopted_workout_coverage_refresh(uuid,uuid,text,uuid,bigint,uuid,uuid,bytea,text,text,jsonb,uuid,uuid,text,text,text,double precision)',true,false,false,false,'v'),
		           ('app.read_workout_provenance(uuid)',true,false,false,true,'s')
		       ) expected(signature,security_definer,ingest_worker,coverage_worker,api,volatility)
		       LEFT JOIN pg_proc p ON p.oid=to_regprocedure(expected.signature)
		       WHERE p.oid IS NULL OR pg_get_userbyid(p.proowner)<>'workouts_security_owner'
		           OR p.prosecdef<>expected.security_definer
		           OR p.proconfig IS DISTINCT FROM CASE WHEN expected.security_definer
		               THEN ARRAY['search_path=pg_catalog, app'] ELSE ARRAY['search_path=pg_catalog'] END
		           OR p.provolatile::text<>expected.volatility
		           OR (NOT expected.security_definer AND NOT p.proisstrict)
		           OR has_function_privilege('workouts_worker',p.oid,'EXECUTE')<>expected.ingest_worker
		           OR has_function_privilege('workouts_coverage_worker',p.oid,'EXECUTE')<>expected.coverage_worker
		           OR has_function_privilege('workouts_api',p.oid,'EXECUTE')<>expected.api
		           OR has_function_privilege('workouts_tiles',p.oid,'EXECUTE')
		           OR EXISTS (SELECT 1 FROM aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner))) acl
		               WHERE acl.privilege_type='EXECUTE' AND acl.grantee<>p.proowner
		                 AND (acl.is_grantable OR NOT (
		                     (expected.ingest_worker AND acl.grantee='workouts_worker'::regrole::oid)
		                     OR (expected.coverage_worker AND acl.grantee='workouts_coverage_worker'::regrole::oid)
		                     OR (expected.api AND acl.grantee='workouts_api'::regrole::oid)))))
		   AND (SELECT position('ERRCODE = ''55000''' in pg_get_functiondef(p.oid)) > 0
		       AND position('versioned ingest fence requires runtime 26 or newer' in pg_get_functiondef(p.oid)) > 0
		       FROM pg_proc p WHERE p.oid='app.fence_ingest_job(uuid,text,uuid)'::regprocedure)
		   AND (SELECT position('runtime_version IS NULL OR runtime_version < 26' in pg_get_functiondef(p.oid)) > 0
		       AND position('minimum_runtime_version <= runtime_version' in pg_get_functiondef(p.oid)) > 0
		       FROM pg_proc p WHERE p.oid='app.fence_ingest_job(uuid,text,uuid,integer)'::regprocedure)
		   AND has_column_privilege('workouts_security_owner','app.accounts','workout_ownership_epoch','UPDATE')
		   AND has_column_privilege('workouts_security_owner','app.ingest_write_capabilities','ownership_epoch','UPDATE')
		   AND has_column_privilege('workouts_security_owner','app.ingest_write_capabilities','runtime_version','UPDATE')
		   AND NOT EXISTS (
		       SELECT 1 FROM (VALUES
		           ('app.job_progress','workouts_created_with_route','0'),
		           ('app.job_progress','workouts_updated_with_route','0'),
		           ('app.coverage_retry_roots','automatic_attempts_used','0'),
		           ('app.coverage_job_contexts','matcher_contract_version','app.current_coverage_matcher_contract()')
		           ,('app.coverage_job_contexts','parent_batch_size','app.current_coverage_batch_size()')
		           ,('app.coverage_batch_policy','batch_size','25')
		           ,('app.coverage_batch_policy','singleton','true')
		           ,('app.ingest_coverage_handoffs','requested_at','clock_timestamp()')
		           ,('app.ingest_coverage_handoffs','next_attempt_at','''-infinity''::timestamp with time zone')
		           ,('app.ingest_coverage_handoffs','origin','''normal''::text')
		           ,('app.ingest_coverage_handoffs','repair','false')
		       ) expected(relation_name,column_name,default_expression)
		       WHERE NOT EXISTS (SELECT 1 FROM pg_attribute a JOIN pg_attrdef d
		           ON d.adrelid=a.attrelid AND d.adnum=a.attnum
		           WHERE a.attrelid=to_regclass(expected.relation_name) AND a.attname=expected.column_name
		             AND pg_get_expr(d.adbin,d.adrelid)=expected.default_expression))
		   AND NOT EXISTS (
		       SELECT 1 FROM (VALUES
		           ('app.coverage_diagnostic_runs','CHECK (((original_point_count >= 0) AND (original_point_count <= 50000)))'),
		           ('app.coverage_diagnostic_runs','CHECK (((sampled_point_count >= 0) AND (sampled_point_count <= 12000)))'),
		           ('app.job_progress','CHECK (((workouts_created_with_route >= 0) AND (workouts_created_with_route <= workouts_created)))'),
		           ('app.job_progress','CHECK (((workouts_updated_with_route >= 0) AND (workouts_updated_with_route <= workouts_updated)))'),
		           ('app.coverage_job_contexts','CHECK ((matcher_contract_version ~ ''^coverage-production-v[1-9][0-9]*$''::text))'),
		           ('app.coverage_job_contexts','CHECK ((parent_batch_size >= 1))'),
		           ('app.coverage_batch_policy','CHECK (singleton)'),
		           ('app.coverage_batch_policy','CHECK (((batch_size >= 1) AND (batch_size <= 1000)))'),
		           ('app.coverage_reconciliation_accounts','CHECK (((lease_runtime_version IS NULL) OR (lease_runtime_version >= 28)))'),
		           ('app.ingest_coverage_handoffs','CHECK ((octet_length(content_sha256) = 32))'),
		           ('app.ingest_coverage_handoffs','CHECK ((origin = ANY (ARRAY[''normal''::text, ''adopted''::text])))'),
		           ('app.ingest_coverage_handoffs','CHECK (((acknowledged_at IS NULL) = (outcome IS NULL)))'),
		           ('app.ingest_coverage_handoffs','CHECK ((outcome = ANY (ARRAY[''queued''::text, ''existing''::text, ''pending''::text, ''unavailable''::text, ''superseded''::text, ''deleted''::text])))'),
		           ('app.coverage_retry_roots','CHECK (((automatic_attempts_used >= 0) AND (automatic_attempts_used <= 20)))'),
		           ('app.coverage_retry_attempts','CHECK ((origin = ANY (ARRAY[''observed''::text, ''manual''::text, ''automatic''::text])))'),
		           ('app.coverage_retry_attempts','CHECK ((((origin = ''automatic''::text) AND (automatic_ordinal IS NOT NULL) AND ((automatic_ordinal >= 1) AND (automatic_ordinal <= 20))) OR ((origin <> ''automatic''::text) AND (automatic_ordinal IS NULL))))'),
		           ('app.coverage_retry_attempts','CHECK (((origin = ''observed''::text) OR (predecessor_job_id IS NOT NULL)))')
		       ) expected(relation_name,definition)
		       WHERE NOT EXISTS (SELECT 1 FROM pg_constraint c
		           WHERE c.conrelid=to_regclass(expected.relation_name) AND c.contype='c'
		             AND c.convalidated AND pg_get_constraintdef(c.oid)=expected.definition))
		   AND NOT EXISTS (
		       SELECT 1 FROM (VALUES
		           ('app.workout_import_events','has_route','workouts_api','SELECT'),
		           ('app.workout_import_events','has_route','workouts_worker','SELECT'),
		           ('app.workout_import_events','has_route','workouts_worker','INSERT'),
		           ('app.workout_import_events','has_route','workouts_security_owner','SELECT'),
		           ('app.job_progress','workouts_created_with_route','workouts_api','SELECT'),
		           ('app.job_progress','workouts_updated_with_route','workouts_api','SELECT'),
		           ('app.job_progress','workouts_created_with_route','workouts_worker','SELECT'),
		           ('app.job_progress','workouts_updated_with_route','workouts_worker','SELECT'),
		           ('app.job_progress','workouts_created_with_route','workouts_security_owner','SELECT'),
		           ('app.job_progress','workouts_updated_with_route','workouts_security_owner','SELECT'),
		           ('app.job_progress','workouts_created_with_route','workouts_security_owner','UPDATE'),
		           ('app.job_progress','workouts_updated_with_route','workouts_security_owner','UPDATE')
		       ) required(relation_name,column_name,role_name,privilege)
		       WHERE NOT has_column_privilege(required.role_name,required.relation_name,required.column_name,required.privilege))
		   AND NOT EXISTS (SELECT 1 FROM pg_roles r
		       CROSS JOIN (VALUES ('app.job_progress','workouts_created_with_route'),
		           ('app.job_progress','workouts_updated_with_route'),('app.workout_import_events','has_route')) protected(relation_name,column_name)
		       WHERE r.rolname IN ('workouts_api','workouts_worker','workouts_coverage_worker','workouts_tiles')
		         AND (has_column_privilege(r.oid,protected.relation_name,protected.column_name,'UPDATE')
		           OR (NOT (r.rolname='workouts_worker' AND protected.column_name='has_route')
		             AND has_column_privilege(r.oid,protected.relation_name,protected.column_name,'INSERT'))
		           OR (r.rolname IN ('workouts_coverage_worker','workouts_tiles')
		             AND has_column_privilege(r.oid,protected.relation_name,protected.column_name,'SELECT,INSERT'))))
		   AND (SELECT bool_and(c.relrowsecurity AND c.relforcerowsecurity)
		       FROM pg_class c WHERE c.oid IN ('app.workout_import_events'::regclass,
		           'app.job_progress'::regclass,'app.coverage_job_contexts'::regclass))
		   AND (SELECT position('runtime_version IS NULL OR runtime_version < 26' in pg_get_functiondef(p.oid)) > 0
		       AND position('runtime_version < (SELECT minimum_runtime_version FROM app.schema_metadata WHERE singleton)' in pg_get_functiondef(p.oid)) > 0
		       AND position('app.coverage_auto_retry_parent_eligible(job.id,job.account_id)' in pg_get_functiondef(p.oid)) > 0
		       FROM pg_proc p WHERE p.oid='app.retry_next_coverage_update(integer,integer)'::regprocedure)
		   AND (SELECT position('capability.backend_pid = pg_backend_pid()' in pg_get_functiondef(p.oid)) > 0
		       AND position('capability.transaction_id = txid_current()' in pg_get_functiondef(p.oid)) > 0
		       AND position('capability.account_id = target_account' in pg_get_functiondef(p.oid)) > 0
		       AND position('capability.job_id = job' in pg_get_functiondef(p.oid)) > 0
		       AND position('capability.worker_id = worker' in pg_get_functiondef(p.oid)) > 0
		       AND position('capability.lease_token = lease' in pg_get_functiondef(p.oid)) > 0
		       AND position('SELECT capability.runtime_version FROM app.ingest_write_capabilities capability' in pg_get_functiondef(p.oid)) > 0
		       AND position('app.fence_ingest_job(job, worker, lease, COALESCE((' in pg_get_functiondef(p.oid)) > 0
		       FROM pg_proc p WHERE p.oid='app.read_ingest_candidate_actions(uuid,text,uuid,jsonb)'::regprocedure)
		   AND NOT EXISTS (
		       SELECT 1 FROM (VALUES
		           ('app.record_ingest_progress(uuid,text,uuid,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint,bigint)','boolean'),
		           ('app.refresh_ingest_route_counts(uuid)','void'),
		           ('app.retry_coverage_update(uuid,uuid,integer)','TABLE(job_id uuid, route_count integer)'),
		           ('app.read_coverage_retry_eligibility(uuid,integer)','TABLE(can_retry boolean, reason text, eligible_route_count bigint, unsuccessful_route_count bigint, current_route_count bigint, current_coverage_job_ids uuid[])'),
		           ('app.read_job_history(text,text,integer,bigint,integer)','TABLE(job_id uuid, display_status text, total_items bigint)'),
		           ('app.retry_next_coverage_update(integer,integer)','TABLE(job_id uuid, route_count integer)'),
		           ('app.construct_coverage_retry(uuid,uuid,integer,integer)','TABLE(job_id uuid, route_count integer)'),
		           ('app.coverage_retry_root(uuid)','TABLE(root_job_id uuid, depth integer)'),
		           ('app.coverage_retry_targets(uuid,uuid)','TABLE(child_id uuid, workout_id uuid, route_revision bigint, route_digest bytea, generations jsonb, timeout_count integer)'),
		           ('app.enroll_coverage_retry_failure()','trigger'),
		           ('app.current_coverage_matcher_contract()','text'),
		           ('app.coverage_auto_retry_parent_eligible(uuid,uuid)','boolean')
		           ,('app.current_coverage_batch_size()','integer')
		           ,('app.configure_coverage_batch_size(integer,integer)','boolean')
		           ,('app.record_ingest_coverage_handoff()','trigger')
		           ,('app.record_adopted_coverage_origin()','trigger')
		           ,('app.record_adopted_coverage_receipt(uuid,uuid,uuid)','void')
		           ,('app.read_pending_ingest_coverage_workouts(uuid,uuid,text,uuid)','TABLE(workout_id uuid)')
		           ,('app.ingest_coverage_candidates(uuid,uuid)','TABLE(workout_id uuid, event_id uuid, origin text, source_id uuid, source_file_id uuid, ingest_job_id uuid)')
		           ,('app.ingest_coverage_snapshot_digest(uuid,uuid)','bytea')
		           ,('app.read_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,uuid,integer,boolean)','TABLE(workout_id uuid, event_id uuid, origin text, content_sha256 bytea, ownership_epoch bigint)')
		           ,('app.read_ingest_coverage_route_snapshot(uuid,uuid,text,uuid,bigint,uuid,uuid,text)','TABLE(content_sha256 bytea, ownership_epoch bigint, route_input_sha256 bytea, point_count integer, coordinates jsonb)')
		           ,('app.read_coverage_reconciliation_snapshot(uuid,bigint,text,uuid,uuid,bigint,bytea)','TABLE(content_sha256 bytea, ownership_epoch bigint, route_input_sha256 bytea, point_count integer, coordinates jsonb)')
		           ,('app.apply_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,jsonb,text,text,text,double precision,integer)','TABLE(workout_id uuid, event_id uuid, parent_job_id uuid, child_job_id uuid, outcome text)')
		           ,('app.read_ingest_coverage_prepared(uuid,uuid,text,uuid,bigint,integer)','TABLE(builder_id uuid, root_job_id uuid, batch_size integer, rules text, sampling text, policy text, minimum double precision, targets jsonb)')
		           ,('app.seal_ingest_coverage_cohort(uuid,uuid,text,uuid,bigint,uuid,jsonb,integer)','TABLE(parent_job_id uuid, route_count integer, outcome text)')
		           ,('app.read_coverage_job_scope(uuid)','TABLE(region_ids text[], mixed_regions boolean)')
		           ,('app.ingest_coverage_cleanup_due(uuid)','boolean')
		           ,('app.cleanup_ingest_coverage_origins(uuid,uuid,text,uuid,bigint)','integer')
		           ,('app.record_ingest_coverage_repair(uuid,uuid,text,uuid,uuid,integer)','boolean')
		           ,('app.read_ingest_coverage_source_id(uuid,uuid,text,uuid)','uuid')
		           ,('app.defer_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,jsonb,integer)','boolean')
		           ,('app.enqueue_coverage_update_core(uuid,uuid,text,bigint,bigint,text,text,text,double precision,jsonb,boolean)','TABLE(job_id uuid, route_count integer, reused boolean)')
		           ,('app.enqueue_adopted_coverage_update(uuid,uuid,text,bigint,bigint,text,text,text,double precision,jsonb)','TABLE(job_id uuid, route_count integer, reused boolean)')
		           ,('app.apply_ingest_coverage_normal(uuid,uuid,bigint,text,uuid,uuid,bigint,bytea,text,text,jsonb,uuid,uuid,text,text,text,double precision)','boolean')
		       ) expected(signature,result)
		       WHERE pg_get_function_result(to_regprocedure(expected.signature)) IS DISTINCT FROM expected.result)
		   AND EXISTS (SELECT 1 FROM pg_trigger t
		       WHERE t.tgrelid='app.jobs'::regclass AND t.tgname='coverage_retry_failure_enrollment'
		         AND t.tgattr::text=(SELECT attnum::text FROM pg_attribute WHERE attrelid=t.tgrelid AND attname='status')
		         AND t.tgqual IS NOT NULL
		         AND position('old.status IS DISTINCT FROM new.status' in pg_get_triggerdef(t.oid)) > 0
		         AND position('new.kind = ''coverage_update''::text' in pg_get_triggerdef(t.oid)) > 0
		         AND position('new.status = ANY (ARRAY[''failed''::text, ''partially_succeeded''::text])' in pg_get_triggerdef(t.oid)) > 0)
		   AND EXISTS (SELECT 1 FROM pg_attribute a
		       WHERE a.attrelid='app.ingest_coverage_handoffs'::regclass AND a.attname='ordinal' AND a.attidentity='a')
		   AND NOT EXISTS (SELECT 1 FROM pg_constraint c
		       WHERE c.conrelid='app.coverage_job_contexts'::regclass AND c.contype='c'
		         AND position('parent_batch_size' in pg_get_constraintdef(c.oid))>0
		         AND pg_get_constraintdef(c.oid)<>'CHECK ((parent_batch_size >= 1))')
		   AND NOT EXISTS (SELECT 1 FROM pg_roles r WHERE r.rolname IN ('workouts_api','workouts_worker','workouts_coverage_worker','workouts_tiles')
		       AND has_sequence_privilege(r.oid,'app.ingest_coverage_handoffs_ordinal_seq','USAGE,SELECT,UPDATE'))
		   AND EXISTS (SELECT 1 FROM pg_class c WHERE c.oid='app.ingest_coverage_handoffs_ordinal_seq'::regclass
		       AND pg_get_userbyid(c.relowner)='workouts_security_owner')
		   AND has_column_privilege('workouts_security_owner','app.coverage_job_contexts','parent_batch_size','SELECT,INSERT')
		   AND has_column_privilege('workouts_security_owner','app.coverage_reconciliation_accounts','lease_runtime_version','SELECT,UPDATE')
		   AND EXISTS (
		       SELECT 1 FROM app.schema_metadata
		       WHERE singleton AND schema_version >= $1 AND minimum_runtime_version <= $1
		         AND minimum_runtime_version >= 28
		         AND migration_baseline = 27
		   )
		FROM public.goose_db_version`

// Deferred invalidation is part of runtime 28, not an optional later migration.
const runtimeDeferredReadinessSQL = `
	SELECT NOT EXISTS (
		SELECT 1 FROM (VALUES
			('app.workout_routes','workout_routes_data_generation_after_write',29,ARRAY[]::text[],NULL::text),
			('app.workouts','workouts_data_generation_after_write',29,ARRAY[]::text[],NULL::text),
			('app.workout_types','workout_types_data_generation_after_update',17,ARRAY['type_key','provider_label'],NULL::text),
			('app.workout_coverage_states','workout_coverage_states_data_generation_after_current',17,ARRAY['processing_finished_at'],
			 '((new.processing_state = ''current''::text) AND (new.processing_finished_at IS DISTINCT FROM old.processing_finished_at))')
		) expected(relation,name,events,columns,qual)
		LEFT JOIN pg_trigger t ON t.tgrelid=to_regclass(expected.relation) AND t.tgname=expected.name
		LEFT JOIN pg_constraint c ON c.oid=t.tgconstraint
		WHERE t.oid IS NULL OR t.tgisinternal OR t.tgenabled<>'O'
			OR t.tgtype<>expected.events OR NOT t.tgdeferrable OR NOT t.tginitdeferred
			OR t.tgfoid<>'app.advance_account_data_generation()'::regprocedure
			OR t.tgnargs<>0 OR t.tgargs<>''::bytea
			OR c.oid IS NULL OR c.contype<>'t' OR c.conname<>expected.name OR c.conrelid<>t.tgrelid
			OR NOT c.condeferrable OR NOT c.condeferred
			OR t.tgattr::text IS DISTINCT FROM COALESCE((
				SELECT string_agg(a.attnum::text,' ' ORDER BY col.ordinality)
				FROM unnest(expected.columns) WITH ORDINALITY col(name,ordinality)
				JOIN pg_attribute a ON a.attrelid=t.tgrelid AND a.attname=col.name AND NOT a.attisdropped),'')
			OR substring(pg_get_triggerdef(t.oid) FROM ' WHEN \((.*)\) EXECUTE FUNCTION ') IS DISTINCT FROM expected.qual
			OR (t.tgqual IS NULL) IS DISTINCT FROM (expected.qual IS NULL)
	)
	AND (SELECT count(*) FROM pg_trigger
		WHERE tgfoid='app.advance_account_data_generation()'::regprocedure AND NOT tgisinternal)=4
	AND EXISTS (SELECT 1 FROM pg_trigger
		WHERE tgrelid='app.accounts'::regclass AND tgname='accounts_data_generation_after_insert'
			AND NOT tgisinternal AND tgenabled='O' AND tgtype=5 AND tgconstraint=0
			AND NOT tgdeferrable AND NOT tginitdeferred AND tgqual IS NULL AND tgattr::text=''
			AND tgfoid='app.seed_account_data_generation()'::regprocedure)
	AND NOT EXISTS (
		SELECT 1 FROM (VALUES
			('app.advance_account_data_generation()',false,'trigger',$body$
DECLARE
    target_account_id uuid;
BEGIN
    IF TG_OP = 'DELETE' THEN target_account_id := OLD.account_id; ELSE target_account_id := NEW.account_id; END IF;
    UPDATE app.account_data_generations SET
        generation = CASE WHEN generation = 9223372036854775807 THEN 1 ELSE generation + 1 END,
        updated_at = transaction_timestamp()
     WHERE account_id = target_account_id;
    IF TG_OP = 'DELETE' THEN RETURN OLD; END IF;
    RETURN NEW;
END;
$body$),
			('app.lock_account_data_generation()',true,'bigint',$body$
DECLARE
    current_generation bigint;
BEGIN
    SELECT generation INTO current_generation
      FROM app.account_data_generations
     WHERE account_id = app.current_account_id();
    IF current_generation IS NULL THEN
        RAISE EXCEPTION 'account data generation is unavailable' USING ERRCODE = '55000';
    END IF;
    RETURN current_generation;
END;
$body$)
		) expected(signature,api,result,body)
		LEFT JOIN pg_proc p ON p.oid=to_regprocedure(expected.signature)
		WHERE p.oid IS NULL OR NOT p.prosecdef OR p.provolatile<>'v'
			OR p.prokind<>'f' OR p.prolang<>(SELECT oid FROM pg_language WHERE lanname='plpgsql')
			OR pg_get_userbyid(p.proowner)<>'workouts_security_owner'
			OR p.proconfig IS DISTINCT FROM ARRAY['search_path=pg_catalog, app']
			OR pg_get_function_result(p.oid)<>expected.result
			OR md5(regexp_replace(p.prosrc,'[[:space:]]','','g'))<>
			   md5(regexp_replace(expected.body,'[[:space:]]','','g'))
			OR has_function_privilege('workouts_api',p.oid,'EXECUTE') IS DISTINCT FROM expected.api
			OR EXISTS (SELECT 1 FROM pg_roles r
				WHERE r.rolname IN ('workouts_worker','workouts_coverage_worker','workouts_tiles')
					AND has_function_privilege(r.oid,p.oid,'EXECUTE'))
			OR EXISTS (SELECT 1 FROM aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner))) acl
				WHERE acl.grantee<>p.proowner
					AND (NOT expected.api OR acl.grantee<>'workouts_api'::regrole::oid OR acl.is_grantable))
	)
	AND EXISTS (
		SELECT 1 FROM pg_proc p WHERE p.oid=to_regprocedure('app.fence_ingest_job(uuid,text,uuid,integer)')
			AND p.prosecdef AND p.provolatile='v' AND pg_get_userbyid(p.proowner)='workouts_security_owner'
			AND p.proconfig=ARRAY['search_path=pg_catalog, app']
			AND pg_get_function_result(p.oid)='TABLE(source_id uuid)'
			AND p.prosrc ~* '\mPERFORM\s+1\s+FROM\s+app\s*\.\s*account_data_generations\s+WHERE\s+account_id\s*=\s*target_account\s*;\s*IF\s+NOT\s+FOUND\s+THEN\s+RAISE\s+EXCEPTION\s+''missing account data generation''\s+USING\s+ERRCODE\s*=\s*''55000''\s*;\s*END\s+IF\s*;'
			AND position('''missing account data generation''' IN p.prosrc)>0
			AND (SELECT count(*) FROM regexp_matches(p.prosrc,'\maccount_data_generations\M','gi'))=1
			AND p.prosrc !~* '\m(?:FROM|JOIN)\s+app\s*\.\s*account_data_generations\M[^;]*\mFOR\M'
			AND has_function_privilege('workouts_worker',p.oid,'EXECUTE')
			AND NOT EXISTS (SELECT 1 FROM pg_roles r
				WHERE r.rolname IN ('workouts_api','workouts_coverage_worker','workouts_tiles')
					AND has_function_privilege(r.oid,p.oid,'EXECUTE'))
			AND NOT EXISTS (SELECT 1 FROM aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner))) acl
				WHERE acl.grantee<>p.proowner AND (acl.grantee<>'workouts_worker'::regrole::oid OR acl.is_grantable))
	)
	AND has_table_privilege('workouts_security_owner','app.account_data_generations','SELECT')
	AND has_table_privilege('workouts_security_owner','app.account_data_generations','UPDATE')
	AND has_table_privilege('workouts_api','app.account_data_generations','SELECT')
	AND NOT EXISTS (SELECT 1 FROM pg_roles r
		WHERE r.rolname IN ('workouts_api','workouts_worker','workouts_coverage_worker','workouts_tiles')
			AND (has_table_privilege(r.oid,'app.account_data_generations','INSERT,UPDATE,DELETE,TRUNCATE,REFERENCES,TRIGGER')
				OR has_any_column_privilege(r.oid,'app.account_data_generations','INSERT,UPDATE,REFERENCES')))`

const runtimeCohortReadinessSQL = `
	SELECT to_regprocedure('app.apply_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,jsonb,text,text,text,double precision)') IS NULL
	AND to_regprocedure('app.defer_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,jsonb)') IS NULL
	AND NOT EXISTS (
		SELECT 1 FROM (VALUES
			('app.ingest_coverage_builders','builder_id','uuid'),
			('app.ingest_coverage_builders','account_id','uuid'),
			('app.ingest_coverage_builders','root_job_id','uuid'),
			('app.ingest_coverage_builders','batch_size','integer'),
			('app.ingest_coverage_builders','rules','text'),
			('app.ingest_coverage_builders','sampling','text'),
			('app.ingest_coverage_builders','policy','text'),
			('app.ingest_coverage_builders','minimum','double precision'),
			('app.ingest_coverage_builders','matcher_contract','text'),
			('app.ingest_coverage_prepared','account_id','uuid'),
			('app.ingest_coverage_prepared','workout_id','uuid'),
			('app.ingest_coverage_prepared','builder_id','uuid'),
			('app.ingest_coverage_prepared','event_id','uuid'),
			('app.ingest_coverage_prepared','source_id','uuid'),
			('app.ingest_coverage_prepared','source_file_id','uuid'),
			('app.ingest_coverage_prepared','ingest_job_id','uuid'),
			('app.ingest_coverage_prepared','origin','text'),
			('app.ingest_coverage_prepared','content_sha256','bytea'),
			('app.ingest_coverage_prepared','route_input_sha256','bytea'),
			('app.ingest_coverage_prepared','route_input_revision','bigint'),
			('app.ingest_coverage_prepared','ownership_epoch','bigint'),
			('app.ingest_coverage_prepared','catalog_fingerprint','bytea'),
			('app.ingest_coverage_prepared','generations','jsonb')
		) expected(relation,name,type)
		WHERE NOT EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid=to_regclass(expected.relation)
			AND a.attname=expected.name AND a.atttypid=to_regtype(expected.type) AND a.attnotnull AND NOT a.attisdropped))
	AND NOT EXISTS (
		SELECT 1 FROM (VALUES ('app.ingest_coverage_builders',9),('app.ingest_coverage_prepared',14)) expected(relation,columns)
		WHERE (SELECT count(*) FROM pg_attribute a WHERE a.attrelid=to_regclass(expected.relation)
			AND a.attnum>0 AND NOT a.attisdropped)<>expected.columns)
	AND NOT EXISTS (
		SELECT 1 FROM (VALUES
			('app.ingest_coverage_builders','CHECK (((batch_size >= 1) AND (batch_size <= 1000)))'),
			('app.ingest_coverage_builders','CHECK (((minimum >= (0.1)::double precision) AND (minimum <= (100)::double precision)))'),
			('app.ingest_coverage_prepared','CHECK ((octet_length(content_sha256) = 32))'),
			('app.ingest_coverage_prepared','CHECK ((octet_length(route_input_sha256) = 32))'),
			('app.ingest_coverage_prepared','CHECK ((octet_length(catalog_fingerprint) = 32))'),
			('app.ingest_coverage_prepared','CHECK ((route_input_revision > 0))'),
			('app.ingest_coverage_prepared','CHECK ((ownership_epoch >= 0))'),
			('app.ingest_coverage_prepared','CHECK ((origin = ANY (ARRAY[''normal''::text, ''adopted''::text])))'),
			('app.ingest_coverage_prepared','CHECK (app.valid_coverage_generation_vector(generations))')
		) expected(relation,definition)
		WHERE NOT EXISTS (SELECT 1 FROM pg_constraint c WHERE c.conrelid=to_regclass(expected.relation)
			AND c.contype='c' AND c.convalidated AND pg_get_constraintdef(c.oid)=expected.definition))
	AND NOT EXISTS (
		SELECT 1 FROM (VALUES
			('app.ingest_coverage_builders_pkey','app.ingest_coverage_builders',true,'builder_id'),
			('app.ingest_coverage_builders_account_id_root_job_id_key','app.ingest_coverage_builders',true,'account_id, root_job_id'),
			('app.ingest_coverage_prepared_pkey','app.ingest_coverage_prepared',true,'account_id, workout_id'),
			('app.ingest_coverage_prepared_builder_idx','app.ingest_coverage_prepared',false,'builder_id, workout_id')
		) expected(name,relation,unique_index,columns)
		WHERE NOT EXISTS (SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid=i.indexrelid JOIN pg_am am ON am.oid=c.relam
			WHERE i.indexrelid=to_regclass(expected.name) AND i.indrelid=to_regclass(expected.relation)
				AND i.indisvalid AND i.indisready AND i.indisunique=expected.unique_index AND am.amname='btree'
				AND i.indpred IS NULL AND NOT i.indnullsnotdistinct
				AND (SELECT string_agg(pg_get_indexdef(i.indexrelid,n,true),', ' ORDER BY n)
					FROM generate_series(1,i.indnkeyatts) n)=expected.columns))
	AND NOT EXISTS (
		SELECT 1 FROM (VALUES
			('app.ingest_coverage_builders','app.accounts','account_id','id'),
			('app.ingest_coverage_builders','app.jobs','root_job_id,account_id','id,account_id'),
			('app.ingest_coverage_prepared','app.ingest_coverage_builders','builder_id','builder_id'),
			('app.ingest_coverage_prepared','app.workouts','workout_id,account_id','id,account_id')
		) expected(relation,parent,columns,parent_columns)
		WHERE NOT EXISTS (SELECT 1 FROM pg_constraint c WHERE c.conrelid=to_regclass(expected.relation)
			AND c.confrelid=to_regclass(expected.parent) AND c.contype='f' AND c.convalidated AND c.confdeltype='c'
			AND (SELECT string_agg(a.attname,',' ORDER BY key.ordinality) FROM unnest(c.conkey) WITH ORDINALITY key(num,ordinality)
				JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=key.num)=expected.columns
			AND (SELECT string_agg(a.attname,',' ORDER BY key.ordinality) FROM unnest(c.confkey) WITH ORDINALITY key(num,ordinality)
				JOIN pg_attribute a ON a.attrelid=c.confrelid AND a.attnum=key.num)=expected.parent_columns))
	AND NOT EXISTS (
		SELECT 1 FROM (VALUES
			('app.ingest_coverage_candidates(uuid,uuid)','proof.account_id=adoption.account_id AND proof.workout_id=adoption.workout_id'),
			('app.ingest_coverage_candidates(uuid,uuid)','proof.source_id=adoption.source_id AND proof.source_file_id=adoption.source_file_id'),
			('app.ingest_coverage_candidates(uuid,uuid)','proof.ingest_job_id=receipt.ingest_job_id AND proof.origin=''adopted'''),
			('app.read_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,uuid,integer,boolean)','app.ingest_coverage_prepared'),
			('app.ingest_coverage_existing_target(uuid,uuid,bigint,bytea,jsonb,text,text,text,double precision)','state.processing_finished_at=child.terminal_at'),
			('app.ingest_coverage_existing_target(uuid,uuid,bigint,bytea,jsonb,text,text,text,double precision)','other.terminal_at>=child.created_at'),
			('app.ingest_coverage_existing_target(uuid,uuid,bigint,bytea,jsonb,text,text,text,double precision)','context.route_input_revision=$3'),
			('app.enqueue_coverage_update_core(uuid,uuid,text,bigint,bigint,text,text,text,double precision,jsonb,boolean)','app.ingest_coverage_existing_target'),
			('app.cleanup_ingest_coverage_origins(uuid,uuid,text,uuid,bigint)','handoff.event_id=retired.event_id'),
			('app.cleanup_ingest_coverage_origins(uuid,uuid,text,uuid,bigint)','handoff.content_sha256=retired.content_sha256'),
			('app.cleanup_ingest_coverage_origins(uuid,uuid,text,uuid,bigint)','request.event_id=receipt.event_id'),
			('app.read_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,uuid,integer,boolean)','app.cleanup_ingest_coverage_origins'),
			('app.read_ingest_coverage_prepared(uuid,uuid,text,uuid,bigint,integer)','app.cleanup_ingest_coverage_origins'),
			('app.seal_ingest_coverage_cohort(uuid,uuid,text,uuid,bigint,uuid,jsonb,integer)','app.cleanup_ingest_coverage_origins'),
			('app.claim_coverage_reconciliation(text,uuid,interval,integer)','app.ingest_coverage_cleanup_due'),
			('app.read_coverage_reconciliation_routes(uuid,bigint,text,uuid,uuid,integer)','source.account_id=workout.account_id AND source.deleted_at IS NULL'),
			('app.apply_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,jsonb,text,text,text,double precision,integer)','INSERT INTO app.ingest_coverage_prepared'),
			('app.apply_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,jsonb,text,text,text,double precision,integer)','decode(item->>''catalogFingerprint'',''hex'')'),
			('app.apply_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,jsonb,text,text,text,double precision,integer)','jsonb_typeof(item->''catalogFingerprint'') IS DISTINCT FROM ''string'''),
			('app.apply_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,jsonb,text,text,text,double precision,integer)','IF outcome IS DISTINCT FROM ''existing'' THEN'),
			('app.read_ingest_coverage_prepared(uuid,uuid,text,uuid,bigint,integer)','app.ingest_coverage_root_terminal'),
			('app.read_ingest_coverage_prepared(uuid,uuid,text,uuid,bigint,integer)','''catalogFingerprint'',encode(prepared.catalog_fingerprint,''hex'')'),
			('app.seal_ingest_coverage_cohort(uuid,uuid,text,uuid,bigint,uuid,jsonb,integer)','targets<builder.batch_size'),
			('app.seal_ingest_coverage_cohort(uuid,uuid,text,uuid,bigint,uuid,jsonb,integer)','ORDER BY parent.id,child.id FOR UPDATE OF child NOWAIT'),
			('app.seal_ingest_coverage_cohort(uuid,uuid,text,uuid,bigint,uuid,jsonb,integer)','app.ingest_coverage_snapshot_digest'),
			('app.seal_ingest_coverage_cohort(uuid,uuid,text,uuid,bigint,uuid,jsonb,integer)','app.coverage_generation_vectors_equal'),
			('app.seal_ingest_coverage_cohort(uuid,uuid,text,uuid,bigint,uuid,jsonb,integer)','catalog_proof IS DISTINCT FROM prepared.catalog_fingerprint'),
			('app.construct_coverage_retry(uuid,uuid,integer,integer)','prior_parent.sealed_ingest_root_id'),
			('app.enqueue_coverage_update_core(uuid,uuid,text,bigint,bigint,text,text,text,double precision,jsonb,boolean)','context.sealed_ingest_root_id IS NULL'),
			('app.claim_coverage_reconciliation(text,uuid,interval,integer)','app.ingest_coverage_builders')
		) expected(signature,fragment)
		LEFT JOIN pg_proc p ON p.oid=to_regprocedure(expected.signature)
		WHERE p.oid IS NULL OR position(expected.fragment IN p.prosrc)=0)
	AND (SELECT p.prosrc !~ 'app[.]enqueue_' FROM pg_proc p
		WHERE p.oid=to_regprocedure('app.apply_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,jsonb,text,text,text,double precision,integer)'))
	AND (SELECT p.prosrc !~ 'app[.]workout_import_events' FROM pg_proc p
		WHERE p.oid=to_regprocedure('app.ingest_coverage_candidates(uuid,uuid)'))`

const runtimeFullReadinessSQL = `SELECT (` + runtimeReadinessSQL + `) AND (` + runtimeDeferredReadinessSQL + `) AND (` + runtimeCohortReadinessSQL + `)`

func Ready(ctx context.Context, pool *pgxpool.Pool) bool {
	var ready bool
	err := pool.QueryRow(ctx, runtimeFullReadinessSQL, SupportedSchemaVersion).Scan(&ready)
	return err == nil && ready
}

func OSMReady(ctx context.Context, pool *pgxpool.Pool) bool {
	return osm.Ready(ctx, pool)
}
