package database

import (
	"context"
	"fmt"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
)

const SupportedSchemaVersion = 1

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
		SELECT COALESCE(max(version_id) FILTER (WHERE is_applied), 0) >= 1
		   AND to_regclass('app.schema_metadata') IS NOT NULL
		   AND to_regclass('app.jobs') IS NOT NULL
		   AND has_schema_privilege(current_user, 'app', 'USAGE')
		   AND has_table_privilege(current_user, 'app.schema_metadata', 'SELECT')
		   AND has_table_privilege(current_user, 'app.jobs', 'SELECT')
		   AND has_table_privilege(current_user, 'app.jobs', 'INSERT')
		   AND has_function_privilege(current_user, 'app.current_account_id()', 'EXECUTE')
		   AND has_function_privilege(current_user, 'app.request_job_cancellation(uuid,uuid)', 'EXECUTE')
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
		   ))
		   AND EXISTS (
		       SELECT 1 FROM app.schema_metadata
		       WHERE singleton AND schema_version >= 1 AND minimum_runtime_version <= $1
		   )
		FROM public.goose_db_version`, SupportedSchemaVersion).Scan(&ready)
	return err == nil && ready
}
