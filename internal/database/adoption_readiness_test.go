package database

import (
	"fmt"
	"strings"
	"testing"
)

var adoptionReadinessFunctions = []struct {
	signature                      string
	definer, worker, coverage, api bool
	volatility                     string
}{
	{"app.workout_provider_namespace(text)", false, false, false, false, "i"},
	{"app.bump_workout_ownership_epoch(uuid)", true, false, false, false, "v"},
	{"app.source_workout_ownership_changed()", true, false, false, false, "v"},
	{"app.valid_ingest_ownership_epoch(uuid,bigint,integer)", true, false, false, false, "v"},
	{"app.enforce_ingest_identity()", true, false, false, false, "v"},
	{"app.advance_workout_ownership_epoch()", true, false, false, false, "v"},
	{"app.fence_ingest_job(uuid,text,uuid)", true, true, false, false, "v"},
	{"app.fence_ingest_job(uuid,text,uuid,integer)", true, true, false, false, "v"},
	{"app.resolve_ingest_workout_identity(uuid,text,uuid,uuid,uuid,text,text,bytea)", true, true, false, false, "v"},
	{"app.read_ingest_candidate_actions(uuid,text,uuid,jsonb)", true, true, false, false, "v"},
	{"app.record_adopted_workout_coverage_refresh(uuid,text,uuid,uuid)", true, true, false, false, "v"},
	{"app.read_pending_adopted_workout_coverage_refreshes(uuid,uuid,text,uuid)", true, true, false, false, "v"},
	{"app.adopted_coverage_authority_current(uuid,uuid,text,uuid,bigint)", true, false, false, false, "v"},
	{"app.adopted_coverage_route_digest(uuid,uuid)", true, false, false, false, "v"},
	{"app.read_adopted_workout_coverage_refreshes(uuid,uuid,text,uuid,bigint,uuid,integer)", true, true, true, false, "v"},
	{"app.read_adopted_workout_coverage_route(uuid,uuid,text,uuid,bigint,uuid,uuid)", true, true, true, false, "v"},
	{"app.apply_adopted_workout_coverage_refresh(uuid,uuid,text,uuid,bigint,uuid,uuid,bytea,text,text,jsonb,uuid,uuid,text,text,text,double precision)", true, false, false, false, "v"},
	{"app.read_workout_provenance(uuid)", true, false, false, true, "s"},
}

func TestAdoptionReadinessFunctionSecurity(t *testing.T) {
	for _, function := range adoptionReadinessFunctions {
		tuple := fmt.Sprintf("('%s',%t,%t,%t,%t,'%s')", function.signature, function.definer, function.worker, function.coverage, function.api, function.volatility)
		if !strings.Contains(runtimeReadinessSQL, tuple) {
			t.Errorf("readiness lacks exact security/role contract %s", tuple)
		}
	}
	for _, required := range []string{
		"LEFT JOIN pg_proc p ON p.oid=to_regprocedure(expected.signature)",
		"p.oid IS NULL OR pg_get_userbyid(p.proowner)<>'workouts_security_owner'",
		"p.prosecdef<>expected.security_definer",
		"p.proconfig IS DISTINCT FROM CASE WHEN expected.security_definer",
		"THEN ARRAY['search_path=pg_catalog, app'] ELSE ARRAY['search_path=pg_catalog'] END",
		"p.provolatile::text<>expected.volatility",
		"NOT expected.security_definer AND NOT p.proisstrict",
		"has_function_privilege('workouts_worker',p.oid,'EXECUTE')<>expected.ingest_worker",
		"has_function_privilege('workouts_coverage_worker',p.oid,'EXECUTE')<>expected.coverage_worker",
		"OR has_function_privilege('workouts_api',p.oid,'EXECUTE')<>expected.api",
		"OR has_function_privilege('workouts_tiles',p.oid,'EXECUTE')",
		"acl.privilege_type='EXECUTE' AND acl.grantee<>p.proowner",
		"acl.is_grantable OR NOT (",
		"expected.ingest_worker AND acl.grantee='workouts_worker'::regrole::oid",
		"expected.coverage_worker AND acl.grantee='workouts_coverage_worker'::regrole::oid",
		"expected.api AND acl.grantee='workouts_api'::regrole::oid",
	} {
		if !strings.Contains(runtimeReadinessSQL, required) {
			t.Errorf("readiness lacks security check %q", required)
		}
	}
}

func TestAdoptionReadinessCatalog(t *testing.T) {
	for _, required := range []string{
		"('app.accounts','workout_ownership_epoch','bigint',true)",
		"('app.ingest_write_capabilities','ownership_epoch','bigint',false)",
		"('app.ingest_write_capabilities','runtime_version','integer',false)",
		"('app.workouts','provider_namespace','text',true)",
		"('app.workout_deletion_targets','provider_namespace','text',true)",
		"('app.source_objects','ownership_blocked_epoch','bigint',false)",
		"a.atttypid=to_regtype(expected.type_name) AND a.attnotnull=expected.not_null AND NOT a.attisdropped",
		"a.attcollation='pg_catalog.\"C\"'::regcollation",
		"app.workouts_account_provider_identity_idx", "app.workout_deletion_targets_provider_namespace_idx",
		"app.workout_import_events_ownership_blocked_file_idx", "app.workout_adoption_capabilities_pkey",
		"app.adopted_workout_coverage_refreshes_pkey", "app.adopted_workout_coverage_refreshes_due_idx",
		"i.indisvalid AND i.indisready AND i.indisunique=expected.is_unique AND am.amname='btree'",
		"i.indisprimary=expected.is_primary",
		"pg_get_expr(i.indpred,i.indrelid) IS NOT DISTINCT FROM expected.predicate",
		"sources_ownership_epoch_after_delete", "workouts_namespace_before_insert",
		"t.tgfoid=to_regprocedure(expected.function_name) AND t.tgtype=expected.trigger_type",
		"t.tgenabled IN ('O','A') AND NOT t.tgisinternal",
		"workout_adoption_capabilities_owner_policy", "adopted_workout_coverage_refreshes_owner_policy",
		"c.oid IS NULL OR NOT c.relrowsecurity OR NOT c.relforcerowsecurity",
		"policy.polroles=ARRAY['workouts_security_owner'::regrole::oid]",
		"has_any_column_privilege(runtime_role.oid,c.oid,'SELECT,INSERT,UPDATE,REFERENCES')",
		"aclexplode(COALESCE(c.relacl,acldefault('r',c.relowner)))",
		"WHERE acl.grantee<>c.relowner",
		"WHERE NOT has_table_privilege('workouts_security_owner',c.oid,required.privilege)",
		"('event_id','gen_random_uuid()')", "('requested_at','clock_timestamp()')",
		"('next_attempt_at','''-infinity''::timestamp with time zone')",
		"pg_get_expr(d.adbin,d.adrelid)=expected.default_expression",
		"('app.workout_adoption_capabilities','app.ingest_write_capabilities','backend_pid,transaction_id','backend_pid,transaction_id','c')",
		"('app.adopted_workout_coverage_refreshes','app.workouts','workout_id,account_id','id,account_id','c')",
		"('app.workout_import_events','app.workouts','workout_id,account_id','id,account_id','r')",
		"constraint_row.contype='f' AND constraint_row.convalidated",
	} {
		if !strings.Contains(runtimeReadinessSQL, required) {
			t.Errorf("readiness lacks catalog check %q", required)
		}
	}
	for _, column := range []string{"workout_id", "account_id", "event_id", "source_id", "source_file_id", "requested_at", "next_attempt_at"} {
		if !strings.Contains(runtimeReadinessSQL, "('app.adopted_workout_coverage_refreshes','"+column+"',") {
			t.Errorf("readiness lacks outbox column %s", column)
		}
	}
	for _, column := range []string{"backend_pid", "transaction_id", "account_id", "workout_id", "old_source_id", "new_source_id", "source_file_id", "job_id"} {
		if !strings.Contains(runtimeReadinessSQL, "('app.workout_adoption_capabilities','"+column+"',") {
			t.Errorf("readiness lacks adoption capability column %s", column)
		}
	}
}

func TestAdoptionReadinessVersionedFences(t *testing.T) {
	for _, required := range []string{
		"position('runtime_version IS NULL OR runtime_version < 26' in pg_get_functiondef(p.oid)) > 0",
		"position('minimum_runtime_version <= runtime_version' in pg_get_functiondef(p.oid)) > 0",
		"position('ERRCODE = ''55000''' in pg_get_functiondef(p.oid)) > 0",
		"position('versioned ingest fence requires runtime 26 or newer' in pg_get_functiondef(p.oid)) > 0",
		"has_function_privilege(current_user, 'app.fence_ingest_job(uuid,text,uuid,integer)', 'EXECUTE')",
		"WHERE singleton AND schema_version >= $1 AND minimum_runtime_version <= $1",
	} {
		if !strings.Contains(runtimeReadinessSQL, required) {
			t.Errorf("readiness lacks runtime fence %q", required)
		}
	}
	if strings.Contains(runtimeReadinessSQL, "runtime_version IS NULL OR runtime_version < 23") {
		t.Fatal("latest readiness still accepts the schema-23 coverage claim fence")
	}
}
