package api

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	if err := provisioner.QueryRow(ctx, `SELECT rolcanlogin,rolsuper,rolcreaterole,rolcreatedb,rolreplication,rolbypassrls FROM pg_roles WHERE rolname='workouts_tiles'`).Scan(&login, &superuser, &createRole, &createDB, &replication, &bypass); err != nil {
		t.Fatal(err)
	}
	if !login || superuser || createRole || createDB || replication || bypass {
		t.Fatalf("unsafe tile role login=%t super=%t createrole=%t createdb=%t replication=%t bypass=%t", login, superuser, createRole, createDB, replication, bypass)
	}
	var defaults bool
	if err := provisioner.QueryRow(ctx, `SELECT rolconfig @> ARRAY['row_security=on','statement_timeout=4s','lock_timeout=250ms','idle_in_transaction_session_timeout=5s'] FROM pg_roles WHERE rolname='workouts_tiles'`).Scan(&defaults); err != nil || !defaults {
		t.Fatalf("tile role defaults missing: defaults=%t err=%v", defaults, err)
	}
	t.Run("fresh tile connections enforce defaults", func(t *testing.T) {
		tileURL := os.Getenv("TILE_DATABASE_URL")
		if tileURL == "" {
			t.Skip("TILE_DATABASE_URL is required")
		}
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		// SET ROLE does not load login defaults; use a new physical tile connection.
		tile, err := pgx.Connect(ctx, tileURL)
		if err != nil {
			t.Fatal(err)
		}
		defer tile.Close(context.Background())
		var user string
		if err := tile.QueryRow(ctx, `SELECT current_user`).Scan(&user); err != nil || user != "workouts_tiles" {
			t.Fatalf("tile login=%q err=%v", user, err)
		}
		for setting, want := range map[string]string{
			"row_security": "on", "statement_timeout": "4s", "lock_timeout": "250ms", "idle_in_transaction_session_timeout": "5s",
		} {
			var got string
			if err := tile.QueryRow(ctx, "SHOW "+setting).Scan(&got); err != nil || got != want {
				t.Fatalf("%s=%q want %q err=%v", setting, got, want, err)
			}
		}
		started := time.Now()
		_, err = tile.Exec(ctx, `SELECT pg_sleep(4.1)`)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "57014" || !strings.Contains(pgErr.Message, "statement timeout") {
			t.Fatalf("tile statement deadline: %v", err)
		}
		if elapsed := time.Since(started); elapsed < 3500*time.Millisecond || elapsed > 10*time.Second {
			t.Fatalf("tile statement deadline elapsed=%s", elapsed)
		}
		blocker, err := pgx.Connect(ctx, tileURL)
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Close(context.Background())
		holder, err := blocker.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer holder.Rollback(context.Background())
		if _, err := holder.Exec(ctx, `SELECT pg_advisory_xact_lock(94722104)`); err != nil {
			t.Fatal(err)
		}
		waiter, err := tile.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer waiter.Rollback(context.Background())
		started = time.Now()
		_, err = waiter.Exec(ctx, `SELECT pg_advisory_xact_lock(94722104)`)
		if !errors.As(err, &pgErr) || pgErr.Code != "55P03" || !strings.Contains(pgErr.Message, "lock timeout") {
			t.Fatalf("tile lock deadline: %v", err)
		}
		if elapsed := time.Since(started); elapsed < 200*time.Millisecond || elapsed > 3*time.Second {
			t.Fatalf("tile lock deadline elapsed=%s", elapsed)
		}
	})

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
