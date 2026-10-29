package database

import (
	"strings"
	"testing"

	"github.com/erhhung/workouts-explorer/api/migrations"
)

func TestIngestPhaseReadinessContract(t *testing.T) {
	if SupportedSchemaVersion != 30 || !strings.Contains(runtimeFullReadinessSQL, " AND ("+runtimeIngestPhaseReadinessSQL+")") {
		t.Fatal("runtime30 must compose the schema29 phase contract")
	}
	for _, required := range []string{
		"SELECT $1 < 29 OR (", "schema_version >= $1", "minimum_runtime_version >= 29", "minimum_runtime_version <= $1",
		"migration_baseline = 27", "to_regprocedure('app.record_ingest_phase(uuid,text,uuid,text,bigint,bigint)')",
		"pg_get_userbyid(p.proowner)='workouts_security_owner'", "p.prosecdef", "p.proconfig=ARRAY['search_path=pg_catalog, app']",
		"p.prorettype='boolean'::regtype", "p.pronargdefaults=0", "has_function_privilege('workouts_worker',p.oid,'EXECUTE')",
		"('workouts_api','workouts_coverage_worker','workouts_tiles')", "aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner)))",
		"acl.is_grantable", "acl.grantee NOT IN (p.proowner,'workouts_worker'::regrole::oid)",
		"to_regclass('app.job_events_ingest_phase_latest_idx')", "i.indrelid=to_regclass('app.job_events')",
		"c.relowner=(SELECT relowner FROM pg_class WHERE oid=to_regclass('app.job_events'))", "am.amname='btree'",
		"i.indisvalid AND i.indisready AND i.indislive AND i.indimmediate", "i.indnatts=2 AND i.indnkeyatts=2",
		"i.indoption::text='0 3'", "(code ~~ ''ingest-phase-%''::text)",
	} {
		if !strings.Contains(runtimeIngestPhaseReadinessSQL, required) {
			t.Errorf("missing schema29 phase catalog guard %q", required)
		}
	}
	for _, other := range []string{runtimeReadinessSQL, runtimeDeferredReadinessSQL, runtimeCohortReadinessSQL, runtimeCoverageDisplayReadinessSQL, runtimeCanonicalCoverageDisplayReadinessSQL} {
		if strings.Contains(other, "record_ingest_phase") || strings.Contains(other, "job_events_ingest_phase_latest_idx") {
			t.Fatal("phase object checks must stay in the dedicated schema29 contract")
		}
	}
}

func TestIngestPhaseNativeMigrationContract(t *testing.T) {
	raw, err := migrations.Files.ReadFile("029_ingest_status_reporting.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, down, found := strings.Cut(string(raw), "-- +goose Down")
	if !found || !strings.Contains(down, "RAISE EXCEPTION") || !strings.Contains(down, "forward-only") {
		t.Fatal("ingest status reporting must be forward-only")
	}
	_, phase, found := strings.Cut(up, "CREATE FUNCTION app.record_ingest_phase(")
	if !found {
		t.Fatal("schema29 must create phase reporting")
	}
	for _, required := range []string{
		"app.fence_ingest_job(target_job_id, claiming_worker, current_lease_token, 30)",
		"schema_version >= 29 AND minimum_runtime_version <= 30",
		"GRANT EXECUTE ON FUNCTION app.record_ingest_phase(uuid,text,uuid,text,bigint,bigint) TO workouts_worker",
	} {
		if !strings.Contains(phase, required) {
			t.Errorf("missing native phase contract %q", required)
		}
	}
}
