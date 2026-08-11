package database

import (
	"context"
	"fmt"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
)

const SupportedSchemaVersion = 10

func Open(ctx context.Context, databaseURL, applicationName string) (*pgxpool.Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database configuration")
	}
	poolConfig.ConnConfig.RuntimeParams["application_name"] = applicationName
	poolConfig.ConnConfig.Tracer = newTracer()
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

func Ready(ctx context.Context, pool *pgxpool.Pool) bool {
	// Metadata permits compatible older runtimes during a rolling upgrade; the explicit object and privilege
	// checks below are the authoritative readiness contract for this runtime version.
	var ready bool
	err := pool.QueryRow(ctx, `
		SELECT COALESCE(max(version_id) FILTER (WHERE is_applied), 0) >= $1
		   AND to_regclass('app.schema_metadata') IS NOT NULL
		   AND to_regclass('app.jobs') IS NOT NULL
		   AND has_schema_privilege(current_user, 'app', 'USAGE')
		   AND has_table_privilege(current_user, 'app.schema_metadata', 'SELECT')
		   AND has_table_privilege(current_user, 'app.jobs', 'SELECT')
		   AND has_table_privilege(current_user, 'app.jobs', 'INSERT')
		   AND to_regclass('app.sources') IS NOT NULL
		   AND to_regclass('app.job_config_snapshots') IS NOT NULL
		   AND to_regclass('app.source_files') IS NOT NULL
		   AND to_regclass('app.workout_types') IS NOT NULL
		   AND to_regclass('app.workouts') IS NOT NULL
		   AND to_regclass('app.workout_aggregates') IS NOT NULL
		   AND to_regclass('app.workout_route_points') IS NOT NULL
		   AND to_regclass('app.workout_routes') IS NOT NULL
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
		   AND to_regclass('app.job_events') IS NOT NULL
		   AND to_regclass('app.job_logs') IS NOT NULL
		   AND to_regclass('app.notifications') IS NOT NULL
		   AND to_regclass('app.source_sync_state') IS NOT NULL
		   AND to_regclass('app.auto_sync_policy') IS NOT NULL
		   AND to_regclass('app.account_sync_schedules') IS NOT NULL
		   AND EXISTS (SELECT 1 FROM pg_extension WHERE extname='postgis')
		   AND to_regclass('app.account_data_generations') IS NOT NULL
		   AND to_regclass('app.map_selections') IS NOT NULL
		   AND to_regclass('app.map_selection_workouts') IS NOT NULL
		   AND to_regclass('app.workout_routes_route_gist_idx') IS NOT NULL
		   AND EXISTS (SELECT 1 FROM pg_attribute WHERE attrelid='app.workout_routes'::regclass
		       AND attname='route' AND NOT attisdropped)
		   AND has_function_privilege(current_user, 'app.current_account_id()', 'EXECUTE')
		   AND (current_user='workouts_worker' OR has_function_privilege(current_user, 'app.current_session_id()', 'EXECUTE'))
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
		             ,'app.read_leased_sync_sources(uuid,text,uuid)'::regprocedure
		             ,'app.enqueue_leased_scheduled_ingest(uuid,text,uuid,uuid,bytea,jsonb)'::regprocedure
		             ,'app.finish_sync_account(uuid,text,uuid,uuid)'::regprocedure
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
		             ,'app.reject_workout_import_event_mutation()'::regprocedure
		             ,'app.advance_account_data_generation()'::regprocedure
		             ,'app.current_session_id()'::regprocedure
		             ,'app.seed_account_data_generation()'::regprocedure
		             ,'app.validate_map_selection()'::regprocedure
		             ,'app.validate_map_selection_workout()'::regprocedure
		             ,'app.cleanup_expired_map_selections()'::regprocedure
		             ,'app.lock_account_data_generation()'::regprocedure
		             ,'app.raw_route_mvt(integer,integer,integer,uuid,uuid,uuid,bigint)'::regprocedure
		         ))
		   AND EXISTS (SELECT 1 FROM pg_trigger
		       WHERE tgname='job_config_snapshots_ingest_compatibility_after_insert' AND NOT tgisinternal)
		   AND EXISTS (SELECT 1 FROM pg_policy WHERE polname='workouts_api_not_deleted_policy'
		       AND polrelid='app.workouts'::regclass AND NOT polpermissive)
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
		   AND position('worker runtime version 6 or newer is required' in
		       pg_get_functiondef('app.claim_next_worker_job(text,uuid,interval)'::regprocedure)) > 0
		   AND position('worker runtime version 8 or newer is required' in
		       pg_get_functiondef('app.claim_next_worker_job(text,uuid,interval,integer)'::regprocedure)) > 0
		   AND EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'workouts_security_owner'
		       AND NOT rolcanlogin AND NOT rolsuper AND NOT rolbypassrls)
		   AND EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'workouts_tiles'
		       AND rolcanlogin AND NOT rolsuper AND NOT rolcreatedb AND NOT rolcreaterole
		       AND NOT rolreplication AND NOT rolbypassrls)
		   AND has_schema_privilege('workouts_tiles','app','USAGE')
		   AND has_function_privilege('workouts_tiles','app.raw_route_mvt(integer,integer,integer,uuid,uuid,uuid,bigint)','EXECUTE')
		   AND NOT has_table_privilege('workouts_tiles','app.workout_routes','SELECT')
		   AND NOT has_table_privilege('workouts_tiles','app.workouts','SELECT')
		   AND NOT has_table_privilege('workouts_tiles','app.map_selections','SELECT')
		   AND NOT has_table_privilege('workouts_tiles','app.map_selection_workouts','SELECT')
		   AND NOT has_table_privilege('workouts_tiles','app.account_data_generations','SELECT')
		   AND NOT has_table_privilege('workouts_tiles','app.sessions','SELECT')
		   AND NOT has_table_privilege('workouts_tiles','app.workout_types','SELECT')
		   AND has_table_privilege('workouts_security_owner', 'app.jobs', 'SELECT')
		   AND has_table_privilege('workouts_security_owner', 'app.sources', 'SELECT')
		   AND has_table_privilege('workouts_security_owner', 'app.preferences', 'SELECT')
		   AND has_table_privilege('workouts_security_owner', 'app.source_files', 'SELECT')
		   AND has_table_privilege('workouts_security_owner', 'app.job_source_contexts', 'SELECT')
		   AND has_table_privilege('workouts_security_owner', 'app.auto_sync_policy', 'SELECT')
		   AND has_table_privilege('workouts_security_owner', 'app.account_sync_schedules', 'SELECT')
		   AND has_table_privilege('workouts_security_owner', 'app.workout_deletion_targets', 'SELECT')
		   AND has_table_privilege('workouts_security_owner', 'app.workouts', 'DELETE')
		   AND has_table_privilege('workouts_security_owner', 'app.workout_import_events', 'DELETE')
		   AND has_column_privilege('workouts_security_owner', 'app.accounts', 'state', 'UPDATE')
		   AND (current_user = 'workouts_worker' OR (
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
		       AND NOT has_function_privilege(current_user, 'app.raw_route_mvt(integer,integer,integer,uuid,uuid,uuid,bigint)', 'EXECUTE')
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
		         AND rolname IN ('workouts_api', 'workouts_worker')
		   )
		   AND (current_user <> 'workouts_worker' OR (
		       has_function_privilege(current_user, 'app.claim_job(uuid,text,uuid,interval)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.heartbeat_job(uuid,text,uuid,interval)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.finish_job(uuid,text,uuid,text,text,text)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.recover_expired_job(uuid)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.read_job_config_snapshot(uuid,text,uuid)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.claim_next_worker_job(text,uuid,interval)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.fence_ingest_job(uuid,text,uuid)', 'EXECUTE')
		       AND has_table_privilege(current_user, 'app.source_files', 'INSERT')
		       AND has_table_privilege(current_user, 'app.workouts', 'UPDATE')
		       AND has_function_privilege(current_user, 'app.replace_workout_route_summary(uuid,integer,double precision,double precision,double precision,double precision,double precision,double precision,double precision,boolean)', 'EXECUTE')
		       AND has_table_privilege(current_user, 'app.workout_import_events', 'INSERT')
		       AND has_column_privilege(current_user, 'app.workout_import_events', 'warnings', 'INSERT')
		       AND has_function_privilege(current_user, 'app.valid_workout_warnings(jsonb)', 'EXECUTE')
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
		   AND EXISTS (
		       SELECT 1 FROM app.schema_metadata
		       WHERE singleton AND schema_version >= $1 AND minimum_runtime_version <= $1
		   )
		FROM public.goose_db_version`, SupportedSchemaVersion).Scan(&ready)
	return err == nil && ready
}
