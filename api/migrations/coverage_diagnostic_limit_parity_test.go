package migrations

import (
	stdstrings "strings"
	"testing"

	"github.com/erhhung/workouts-explorer/internal/testutil/migrationfixture"
)

func TestCoverageDiagnosticLimitParityMigration(t *testing.T) {
	source, err := migrationfixture.PhaseSQL(Files, migrationfixture.DiagnosticLimits)
	if err != nil {
		t.Fatal(err)
	}
	parts := stdstrings.Split(string(source), "-- +goose Down")
	for _, required := range []string{
		"DROP CONSTRAINT coverage_diagnostic_runs_original_point_count_check",
		"DROP CONSTRAINT coverage_diagnostic_runs_sampled_point_count_check",
		"CHECK (original_point_count BETWEEN 0 AND 50000)",
		"CHECK (sampled_point_count BETWEEN 0 AND 12000)",
	} {
		if !sqlStrings.Contains(parts[0], required) {
			t.Errorf("diagnostic parity migration lacks %q", required)
		}
	}
	for _, forbidden := range []string{"GRANT ", "REVOKE ", "FUNCTION ", "POLICY ", "ROW LEVEL SECURITY", "DELETE FROM", "UPDATE app.jobs", "UPDATE app.job_progress"} {
		if sqlStrings.Contains(parts[0], forbidden) {
			t.Errorf("diagnostic parity migration changes unrelated contract: %q", forbidden)
		}
	}
	canonical, err := Files.ReadFile(migrationfixture.CanonicalFile)
	if err != nil {
		t.Fatal(err)
	}
	down := stdstrings.Split(string(canonical), "-- +goose Down")[1]
	if !sqlStrings.Contains(down, "forward-only") || !sqlStrings.Contains(down, "ERRCODE = '55000'") {
		t.Fatal("diagnostic parity migration must reject lossy downgrade")
	}
}
