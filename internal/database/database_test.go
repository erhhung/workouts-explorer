package database

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/metric/metricdata/metricdatatest"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.39.0"
)

func TestSupportedSchemaVersion(t *testing.T) {
	if SupportedSchemaVersion != 31 {
		t.Fatalf("SupportedSchemaVersion = %d, want 31", SupportedSchemaVersion)
	}
}

func TestRuntimeReadinessFeatureThresholds(t *testing.T) {
	// The immutable pipeline checkpoint stays at 28; all ingest status features
	// arrive together at 29, followed by both display contracts at 30.
	for _, check := range []struct {
		name, sql, predicate string
	}{
		{"pipeline", runtimeReadinessSQL, "minimum_runtime_version >= 28"},
		{"ingest status", runtimeReadinessSQL, "AND ($1 < 29 OR"},
		{"phase", runtimeIngestPhaseReadinessSQL, "SELECT $1 < 29 OR ("},
		{"precomputations", runtimeCoverageDisplayReadinessSQL, "SELECT $1 < 30 OR ("},
		{"canonical", runtimeCanonicalCoverageDisplayReadinessSQL, "SELECT $1 < 30 OR ("},
	} {
		t.Run(check.name, func(t *testing.T) {
			if !strings.Contains(check.sql, check.predicate) {
				t.Fatalf("missing feature threshold %q", check.predicate)
			}
		})
	}
	if got := strings.Count(runtimeReadinessSQL, "AND ($1 < 29 OR"); got != 3 {
		t.Fatalf("partial success, outcomes and reprocessing must all start at 29, got %d gates", got)
	}
	if strings.Contains(runtimeReadinessSQL, "AND ($1 < 30 OR") {
		t.Fatal("ingest outcomes must not remain gated at the display checkpoint")
	}
}

func TestIngestFileFilterReadiness(t *testing.T) {
	for _, required := range []string{
		"to_regprocedure('app.read_owned_job_files(uuid,integer,integer,boolean)')",
		"p.prosecdef AND p.provolatile='v' AND p.pronargdefaults=0",
		"has_function_privilege('workouts_api',p.oid,'EXECUTE')",
		"acl.is_grantable OR acl.grantee<>'workouts_api'::regrole::oid",
	} {
		if !strings.Contains(runtimeReadinessSQL, required) {
			t.Errorf("missing ingest file filter readiness check: %s", required)
		}
	}
}

func TestIngestOutcomeReadiness(t *testing.T) {
	for _, required := range []string{
		"to_regprocedure('app.ingest_outcome_status(text,text,bigint,bigint)')",
		"NOT p.prosecdef AND p.provolatile='i' AND p.pronargdefaults=0",
		"p.proconfig=ARRAY['search_path=pg_catalog']",
	} {
		if !strings.Contains(runtimeReadinessSQL, required) {
			t.Errorf("missing ingest outcome readiness check: %s", required)
		}
	}
}

func TestIngestReprocessingReadiness(t *testing.T) {
	for _, required := range []string{
		"minimum_runtime_version >= 29",
		"source_files_success_receipt",
		"source_files_receipt_before_write",
		"to_regprocedure('app.ingest_retry_ancestor_jobs(uuid,uuid,uuid)')",
		"to_regprocedure('app.stamp_ingest_source_file_receipt()')",
		"pg_get_function_result(p.oid)='uuid[]'",
		"has_column_privilege(r.oid,a.attrelid,a.attname,'SELECT')",
	} {
		if !strings.Contains(runtimeReadinessSQL, required) {
			t.Errorf("missing ingest reprocessing readiness check: %s", required)
		}
	}
}

func TestRuntimeReadinessCurrentSchema(t *testing.T) {
	sql := strings.Join(strings.Fields(runtimeReadinessSQL), " ")
	for _, required := range []string{
		"COALESCE(max(version_id) FILTER (WHERE is_applied), 0) = $1",
		"SELECT schema_version = $1 FROM app.schema_metadata WHERE singleton",
		"NOT EXISTS(SELECT 1 FROM public.goose_db_version WHERE version_id > $1)",
		"WHERE singleton AND schema_version >= $1 AND minimum_runtime_version <= $1",
		"migration_baseline = 27",
		"has_column_privilege('workouts_coverage_worker','app.jobs','created_at','SELECT')",
		"has_column_privilege('workouts_coverage_worker','app.jobs','attempt','SELECT')",
		"has_column_privilege('workouts_coverage_worker','app.jobs','retry_of_job_id','SELECT')",
		"NOT has_table_privilege('workouts_coverage_worker','app.jobs','SELECT')",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("runtime readiness SQL is missing %q", required)
		}
	}
}

func TestICloudRuntimeReadinessContract(t *testing.T) {
	for _, signature := range []string{
		"app.read_typed_job_source_snapshot(uuid,text,uuid)",
		"app.refresh_source_credentials(uuid,text,uuid,bigint,jsonb)",
		"app.finish_connection_check_with_refresh(uuid,text,uuid,text,text,text,jsonb)",
		"app.fail_source_connection(uuid,text,uuid,bigint,text,text)",
	} {
		if !strings.Contains(runtimeReadinessSQL, "'"+signature+"'::regprocedure") ||
			!strings.Contains(runtimeReadinessSQL, "has_function_privilege(current_user, '"+signature+"', 'EXECUTE')") {
			t.Errorf("readiness does not require owner and worker privilege for %s", signature)
		}
	}
	if !strings.Contains(runtimeReadinessSQL, "attname='observed_identity' AND NOT attisdropped") {
		t.Fatal("readiness does not require remote manifest identity")
	}
	for _, required := range []string{
		"has_column_privilege('workouts_security_owner', 'app.sources', 'generation', 'UPDATE')",
		"NOT has_function_privilege(current_user, 'app.lock_source_job_for_credentials(uuid,text,uuid)', 'EXECUTE')",
		"NOT has_function_privilege(current_user, 'app.valid_source_config_envelope(jsonb)', 'EXECUTE')",
	} {
		if !strings.Contains(runtimeReadinessSQL, required) {
			t.Errorf("readiness does not enforce private credential permissions: %s", required)
		}
	}
}

func TestICloudSchedulerReadinessFenceAndPrivacy(t *testing.T) {
	sql := strings.Join(strings.Fields(runtimeReadinessSQL), " ")
	for _, required := range []string{
		"attname='lease_runtime_version' AND atttypid='integer'::regtype AND NOT attisdropped",
		"runtime_version >= 21 AND runtime_version >= metadata.minimum_runtime_version",
		"FROM app.schema_metadata metadata WHERE metadata.singleton",
		"'app.claim_due_sync_account_internal(text,uuid,interval,integer)'::regprocedure",
		"'app.enqueue_leased_scheduled_ingest_internal(uuid,text,uuid,uuid,bytea,jsonb)'::regprocedure",
		"'app.finish_sync_account_internal(uuid,text,uuid,uuid)'::regprocedure",
		"p.prosecdef AND p.proconfig=ARRAY['search_path=pg_catalog, app']",
		"aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner)))",
		"acl.grantee=0 AND acl.privilege_type='EXECUTE'",
		"runtime_role.rolname IN ('workouts_api','workouts_worker','workouts_coverage_worker','workouts_tiles')",
		"has_function_privilege(runtime_role.oid,p.oid,'EXECUTE')",
		"has_column_privilege('workouts_security_owner', 'app.account_sync_schedules', 'lease_runtime_version', 'UPDATE')",
		"has_table_privilege('workouts_security_owner', 'app.schema_metadata', 'SELECT')",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("scheduler readiness is missing %q", required)
		}
	}
	if strings.Contains(sql, "worker runtime version 8 or newer is required") {
		t.Fatal("readiness still requires obsolete runtime-8 exception wording")
	}
}

func TestOpenDoesNotReturnDatabaseCredentials(t *testing.T) {
	secret := "do-not-expose-this-value"
	_, err := Open(context.Background(), "postgresql://user:"+secret+"@%zz", "test")
	if err == nil {
		t.Fatal("invalid database URL unexpectedly succeeded")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatal("database error exposed credentials")
	}
}

func TestPoolMetricsUseSafeApplicationName(t *testing.T) {
	config, err := pgxpool.ParseConfig("postgresql://user:secret@private-database.internal/sensitive_name")
	if err != nil {
		t.Fatal(err)
	}
	reader := metric.NewManualReader()
	provider := metric.NewMeterProvider(metric.WithReader(reader))
	defer provider.Shutdown(context.Background())
	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := recordPoolStats(pool, "workouts-test", otelpgx.WithStatsMeterProvider(provider)); err != nil {
		t.Fatal(err)
	}
	var metrics metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &metrics); err != nil {
		t.Fatal(err)
	}
	metricdatatest.AssertHasAttributes(t, metrics, semconv.DBClientConnectionPoolName("workouts-test"))
	serialized := fmt.Sprint(metrics)
	for _, forbidden := range []string{"secret", "private-database.internal", "sensitive_name"} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("pool metrics contain sensitive value %q", forbidden)
		}
	}
}

func TestDatabaseTraceOmitsSQLParametersAndConnectionDetails(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	defer provider.Shutdown(context.Background())
	tracer := newTracer(otelpgx.WithTracerProvider(provider))
	secret := "sensitive-database-marker"
	parentCtx, parent := provider.Tracer("test").Start(context.Background(), "parent")
	ctx := tracer.TraceQueryStart(parentCtx, nil, pgx.TraceQueryStartData{
		SQL:  "SELECT * FROM private_data WHERE credential = $1",
		Args: []any{secret},
	})
	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})
	parent.End()
	spans := recorder.Ended()
	var serialized string
	for _, span := range spans {
		if span.Name() == "postgresql.query" {
			serialized = fmt.Sprint(span.Attributes())
		}
	}
	if serialized == "" {
		t.Fatalf("unexpected database spans: %#v", spans)
	}
	for _, forbidden := range []string{secret, "private_data", "credential", "server.address", "db.namespace"} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("database span contains sensitive value %q: %s", forbidden, serialized)
		}
	}
}
