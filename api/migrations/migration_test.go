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
	for _, name := range []string{"00001_job_foundations.sql", "00002_account_lifecycle.sql"} {
		source, err := Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(source)
		proceduralStatements := strings.Count(text, "DO $$") + strings.Count(text, "AS $$")
		starts := strings.Count(text, "-- +goose StatementBegin")
		ends := strings.Count(text, "-- +goose StatementEnd")
		if starts != proceduralStatements || ends != proceduralStatements {
			t.Fatalf("%s: procedural statements = %d, StatementBegin = %d, StatementEnd = %d", name, proceduralStatements, starts, ends)
		}
	}
}

func TestAccountLifecycleMigrationContainsSecurityBoundaries(t *testing.T) {
	source, err := Files.ReadFile("00002_account_lifecycle.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, required := range []string{
		`canonical_username text COLLATE "C" NOT NULL UNIQUE`,
		`canonical_email text COLLATE "C" NOT NULL UNIQUE`,
		"ALTER TABLE app.preferences FORCE ROW LEVEL SECURITY",
		"SET search_path = pg_catalog, app",
		"OWNER TO workouts_security_owner",
		"GRANT CREATE ON SCHEMA app TO workouts_security_owner",
		"REVOKE CREATE ON SCHEMA app FROM workouts_security_owner",
		"CREATE FUNCTION app.issue_password_reset",
		"CREATE FUNCTION app.complete_password_reset",
		"GRANT UPDATE (password_hash,full_name,updated_at)",
		"REVOKE ALL ON app.authentication_principals",
		"CHECK (expires_at = issued_at + interval '7 days')",
		"CHECK (expires_at = issued_at + interval '30 minutes')",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("account lifecycle migration is missing %q", required)
		}
	}
	grant := strings.Index(text, "GRANT CREATE ON SCHEMA app TO workouts_security_owner")
	owner := strings.Index(text, "ALTER FUNCTION app.consume_rate_limit(text,text,bytea) OWNER TO workouts_security_owner")
	revoke := strings.Index(text, "REVOKE CREATE ON SCHEMA app FROM workouts_security_owner")
	if grant < 0 || owner <= grant || revoke <= owner {
		t.Fatal("security-owner CREATE authority is not temporary around ownership transfer")
	}
	if strings.Contains(text, "GRANT SELECT, INSERT, UPDATE ON app.authentication_principals") {
		t.Fatal("API retains broad principal table privileges")
	}
}
