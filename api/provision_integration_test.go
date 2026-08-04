package api

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestProvisionRolesIntegration(t *testing.T) {
	provisioningURL, apiURL := os.Getenv("ROLE_PROVISIONING_DATABASE_URL"), os.Getenv("API_DATABASE_URL")
	if provisioningURL == "" || apiURL == "" {
		t.Skip("ROLE_PROVISIONING_DATABASE_URL and API_DATABASE_URL are required")
	}
	ctx := context.Background()
	provisioner, err := pgxpool.New(ctx, provisioningURL)
	if err != nil {
		t.Fatal(err)
	}
	defer provisioner.Close()
	if err := ProvisionRoles(ctx, provisioner); err != nil {
		t.Fatal(err)
	}
	if err := ProvisionRoles(ctx, provisioner); err != nil {
		t.Fatalf("idempotent provisioning: %v", err)
	}
	var login, superuser, createRole, createDB, replication, bypass, member bool
	if err := provisioner.QueryRow(ctx, `SELECT r.rolcanlogin,r.rolsuper,r.rolcreaterole,r.rolcreatedb,r.rolreplication,r.rolbypassrls,EXISTS(SELECT 1 FROM pg_auth_members memberships JOIN pg_roles migration ON migration.oid=memberships.member WHERE memberships.roleid=r.oid AND migration.rolname='workouts_migration') FROM pg_roles r WHERE r.rolname='workouts_security_owner'`).Scan(&login, &superuser, &createRole, &createDB, &replication, &bypass, &member); err != nil {
		t.Fatal(err)
	}
	if login || superuser || createRole || createDB || replication || bypass || !member {
		t.Fatalf("unsafe provisioned role login=%t super=%t createrole=%t createdb=%t replication=%t bypass=%t member=%t", login, superuser, createRole, createDB, replication, bypass, member)
	}

	runtime, err := pgxpool.New(ctx, apiURL)
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if err := ProvisionRoles(ctx, runtime); err == nil || !strings.Contains(err.Error(), "CREATEROLE") {
		t.Fatalf("runtime credential provision result=%v", err)
	}

	if _, err := provisioner.Exec(ctx, `ALTER ROLE workouts_security_owner LOGIN`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = provisioner.Exec(context.Background(), `ALTER ROLE workouts_security_owner NOLOGIN`) }()
	if err := ProvisionRoles(ctx, provisioner); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("unsafe existing role result=%v", err)
	}
	var remainsLogin bool
	if err := provisioner.QueryRow(ctx, `SELECT rolcanlogin FROM pg_roles WHERE rolname='workouts_security_owner'`).Scan(&remainsLogin); err != nil || !remainsLogin {
		t.Fatalf("unsafe role was repaired or unavailable login=%t err=%v", remainsLogin, err)
	}
	if _, err := provisioner.Exec(ctx, `ALTER ROLE workouts_security_owner NOLOGIN`); err != nil {
		t.Fatal(err)
	}
}
