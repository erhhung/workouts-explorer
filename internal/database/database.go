package database

import (
	"context"
	"fmt"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
)

const SupportedSchemaVersion = 6

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
		   AND to_regclass('app.workout_import_events') IS NOT NULL
		   AND to_regclass('app.ingest_write_capabilities') IS NOT NULL
		   AND has_function_privilege(current_user, 'app.current_account_id()', 'EXECUTE')
		   AND has_function_privilege(current_user, 'app.request_job_cancellation(uuid,uuid)', 'EXECUTE')
		   AND (SELECT count(*) = 10 AND bool_and(pg_get_userbyid(p.proowner) = 'workouts_security_owner')
		          FROM pg_proc p
		         WHERE p.oid IN (
		             'app.claim_next_worker_job_internal(text,uuid,interval,boolean)'::regprocedure,
		             'app.claim_next_worker_job(text,uuid,interval)'::regprocedure,
		             'app.claim_next_source_connection_check(text,uuid,interval)'::regprocedure,
		             'app.fence_ingest_job(uuid,text,uuid)'::regprocedure,
		             'app.clear_ingest_write_capability()'::regprocedure,
		             'app.finish_job(uuid,text,uuid,text,text,text)'::regprocedure,
		             'app.recover_expired_job(uuid)'::regprocedure,
		             'app.request_job_cancellation(uuid,uuid)'::regprocedure,
		             'app.delete_source(uuid,uuid)'::regprocedure,
		             'app.read_worker_job_log_context(uuid,text,uuid)'::regprocedure
		         ))
		   AND EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'workouts_security_owner'
		       AND NOT rolcanlogin AND NOT rolsuper AND NOT rolbypassrls)
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
		       AND NOT has_column_privilege(current_user, 'app.sources', 'deleted_at', 'UPDATE')
		       AND has_column_privilege(current_user, 'app.job_config_snapshots', 'config_envelope', 'INSERT')
		       AND has_column_privilege(current_user, 'app.job_config_snapshots', 'source_id', 'SELECT')
		       AND NOT has_column_privilege(current_user, 'app.job_config_snapshots', 'config_envelope', 'SELECT')
		       AND NOT has_column_privilege(current_user, 'app.job_config_snapshots', 'created_at', 'SELECT')
		       AND has_table_privilege(current_user, 'app.source_files', 'SELECT')
		       AND has_table_privilege(current_user, 'app.workouts', 'SELECT')
		       AND has_table_privilege(current_user, 'app.workout_import_events', 'SELECT')
		       AND has_column_privilege(current_user, 'app.workout_import_events', 'warnings', 'SELECT')
		       AND NOT has_table_privilege(current_user, 'app.workouts', 'INSERT')
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
		       AND has_table_privilege(current_user, 'app.workout_import_events', 'INSERT')
		       AND has_column_privilege(current_user, 'app.workout_import_events', 'warnings', 'INSERT')
		       AND has_function_privilege(current_user, 'app.valid_workout_warnings(jsonb)', 'EXECUTE')
		       AND has_function_privilege(current_user, 'app.read_worker_job_log_context(uuid,text,uuid)', 'EXECUTE')
		       AND NOT has_table_privilege(current_user, 'app.workout_import_events', 'DELETE')
		       AND NOT has_table_privilege(current_user, 'app.ingest_write_capabilities', 'SELECT')
		       AND NOT has_table_privilege(current_user, 'app.ingest_write_capabilities', 'INSERT')
		       AND NOT has_table_privilege(current_user, 'app.job_config_snapshots', 'SELECT')
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
