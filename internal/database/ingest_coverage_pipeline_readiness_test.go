package database

import (
	"strings"
	"testing"
)

func TestIngestCoveragePipelineReadinessContract(t *testing.T) {
	for _, required := range []string{
		"('app.coverage_job_contexts','parent_batch_size','integer',true)",
		"('app.coverage_batch_policy','batch_size','integer',true)",
		"('app.ingest_coverage_handoffs','coverage_child_job_id','uuid',false)",
		"('app.adopted_coverage_handoff_receipts','import_event_id','uuid',false)",
		"('app.configure_coverage_batch_size(integer,integer)',true,true,false,false,'v')",
		"('app.read_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,uuid,integer,boolean)',true,true,true,false,'v')",
		"('app.read_ingest_coverage_route_snapshot(uuid,uuid,text,uuid,bigint,uuid,uuid,text)',true,true,true,false,'s')",
		"('app.read_coverage_reconciliation_snapshot(uuid,bigint,text,uuid,uuid,bigint,bytea)',true,false,true,false,'s')",
		"TABLE(workout_id uuid, event_id uuid, origin text, content_sha256 bytea, ownership_epoch bigint)",
		"TABLE(content_sha256 bytea, ownership_epoch bigint, route_input_sha256 bytea, point_count integer, coordinates jsonb)",
		"('app.apply_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,jsonb,text,text,text,double precision,integer)',true,true,true,false,'v')",
		"('app.defer_ingest_coverage_batch(uuid,uuid,text,uuid,bigint,jsonb,integer)',true,true,true,false,'v')",
		"('app.ingest_coverage_candidates(uuid,uuid)',true,false,false,false,'s')",
		"('app.ingest_coverage_snapshot_digest(uuid,uuid)',true,false,false,false,'s')",
		"('app.coverage_job_contexts','CHECK ((parent_batch_size >= 1))')",
		"('app.coverage_batch_policy','CHECK (((batch_size >= 1) AND (batch_size <= 1000)))')",
		"ingest_coverage_handoffs_pending_idx", "workout_import_events_coverage_handoff", "adopted_coverage_origin",
		"minimum_runtime_version >= 28", "migration_baseline = 27", "a.attidentity='a'",
	} {
		if !strings.Contains(runtimeReadinessSQL, required) {
			t.Errorf("missing pipeline readiness check %q", required)
		}
	}
}
