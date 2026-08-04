package migrations_test

import (
	"context"
	"database/sql"
	"os"
	"testing"

	apiapp "github.com/erhhung/workouts-explorer/api"
	"github.com/erhhung/workouts-explorer/api/migrations"
	"github.com/erhhung/workouts-explorer/internal/database"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

func TestCleanSchemaV1Upgrade(t *testing.T) {
	migrationURL := os.Getenv("UPGRADE_MIGRATION_DATABASE_URL")
	apiURL := os.Getenv("UPGRADE_API_DATABASE_URL")
	provisioningURL := os.Getenv("ROLE_PROVISIONING_DATABASE_URL")
	if migrationURL == "" || apiURL == "" || provisioningURL == "" {
		t.Skip("upgrade and role-provisioning database URLs are required")
	}
	ctx := context.Background()
	db, err := sql.Open("pgx", migrationURL)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	goose.SetBaseFS(migrations.Files)
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpToContext(ctx, db, ".", 1); err != nil {
		t.Fatal(err)
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT schema_version FROM app.schema_metadata WHERE singleton`).Scan(&version); err != nil || version != 1 {
		t.Fatalf("schema-v1 version=%d err=%v", version, err)
	}
	var lifecycleAbsent bool
	if err := db.QueryRowContext(ctx, `SELECT to_regclass('app.authentication_principals') IS NULL`).Scan(&lifecycleAbsent); err != nil || !lifecycleAbsent {
		t.Fatalf("lifecycle schema exists before upgrade: %t err=%v", lifecycleAbsent, err)
	}
	provisioner, err := pgxpool.New(ctx, provisioningURL)
	if err != nil {
		t.Fatal(err)
	}
	defer provisioner.Close()
	if err := apiapp.ProvisionRoles(ctx, provisioner); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpToContext(ctx, db, ".", 2); err != nil {
		t.Fatal(err)
	}
	if err := goose.UpContext(ctx, db, "."); err != nil {
		t.Fatal(err)
	}
	apiDB, err := pgxpool.New(ctx, apiURL)
	if err != nil {
		t.Fatal(err)
	}
	defer apiDB.Close()
	if !database.Ready(ctx, apiDB) {
		t.Fatal("API role is not ready after schema-v1 upgrade")
	}
}
