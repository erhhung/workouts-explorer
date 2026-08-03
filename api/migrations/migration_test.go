package migrations

import (
	"strings"
	"testing"
)

func TestMigrationFailsClosedOnRolesAndPrivileges(t *testing.T) {
	source, err := Files.ReadFile("00001_job_foundations.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, required := range []string{
		"required NOBYPASSRLS login role",
		"ALTER DEFAULT PRIVILEGES IN SCHEMA app REVOKE ALL ON TABLES FROM PUBLIC",
		"ALTER DEFAULT PRIVILEGES IN SCHEMA app REVOKE ALL ON SEQUENCES FROM PUBLIC",
		"ALTER DEFAULT PRIVILEGES IN SCHEMA app REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC",
		"REVOKE CREATE ON SCHEMA public FROM PUBLIC",
		"REVOKE ALL ON ALL FUNCTIONS IN SCHEMA app FROM PUBLIC",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("migration is missing security foundation %q", required)
		}
	}
}

func TestProceduralStatementsAreProtectedFromGooseSplitting(t *testing.T) {
	source, err := Files.ReadFile("00001_job_foundations.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	proceduralStatements := strings.Count(text, "DO $$") + strings.Count(text, "AS $$")
	starts := strings.Count(text, "-- +goose StatementBegin")
	ends := strings.Count(text, "-- +goose StatementEnd")
	if starts != proceduralStatements || ends != proceduralStatements {
		t.Fatalf("procedural statements = %d, StatementBegin = %d, StatementEnd = %d", proceduralStatements, starts, ends)
	}
}
