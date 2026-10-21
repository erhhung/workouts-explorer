package migrations

import (
	stdstrings "strings"
	"testing"

	"github.com/erhhung/workouts-explorer/internal/testutil/migrationfixture"
)

func TestIngestRuntimeCompatibilityMigration(t *testing.T) {
	source, err := migrationfixture.PhaseSQL(Files, migrationfixture.RuntimeCapability)
	if err != nil {
		t.Fatal(err)
	}
	parts := stdstrings.Split(string(source), "-- +goose Down")
	for _, required := range []string{
		"pg_get_functiondef('app.read_ingest_candidate_actions(uuid,text,uuid,jsonb)'::regprocedure)",
		"anchor text := 'app.fence_ingest_job(job, worker, lease, 26)'",
		"(length(definition) - length(replace(definition,anchor,''))) / length(anchor)",
		"IF anchor_count <> 1 THEN", "EXECUTE replace(definition,anchor,$replacement$",
		"app.fence_ingest_job(job, worker, lease, COALESCE((",
		"SELECT capability.runtime_version FROM app.ingest_write_capabilities capability",
		"capability.backend_pid = pg_backend_pid()", "capability.transaction_id = txid_current()",
		"capability.account_id = target_account", "capability.job_id = job",
		"capability.worker_id = worker", "capability.lease_token = lease", "),26))",
	} {
		if !sqlStrings.Contains(parts[0], required) {
			t.Errorf("runtime compatibility migration lacks %q", required)
		}
	}
	for _, forbidden := range []string{"NO TRANSACTION", "GRANT ", "REVOKE ", "ALTER FUNCTION", "CREATE FUNCTION", "UPDATE app.ingest_write_capabilities", "set_config(", "SELECT schema_version", "NO FORCE ROW LEVEL SECURITY"} {
		if sqlStrings.Contains(parts[0], forbidden) {
			t.Errorf("runtime compatibility migration changes authority: %q", forbidden)
		}
	}
	canonical, err := Files.ReadFile(migrationfixture.CanonicalFile)
	if err != nil {
		t.Fatal(err)
	}
	down := stdstrings.Split(string(canonical), "-- +goose Down")[1]
	if !sqlStrings.Contains(down, "forward-only") || !sqlStrings.Contains(down, "ERRCODE = '55000'") {
		t.Fatal("runtime compatibility must reject downgrade")
	}
}

func TestOnlyCandidateHelperHasLiteralRuntime26Fence(t *testing.T) {
	// Audit the shipped definitions without modifying them. All other versioned
	// internal fences must already use caller/capability runtime arguments.
	var found []string
	for _, name := range migrationFiles[:26] {
		source, err := Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		up := stdstrings.Split(string(source), "-- +goose Down")[0]
		for _, line := range stdstrings.Split(up, "\n") {
			if stdstrings.Contains(line, "app.fence_ingest_job(") && stdstrings.Contains(line, ", 26)") {
				found = append(found, name)
			}
		}
	}
	if len(found) != 1 || found[0] != "025_owner_aware_source_inventory.sql" {
		t.Fatalf("unexpected literal-runtime fence inventory: %v", found)
	}
}
