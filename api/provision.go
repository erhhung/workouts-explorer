package api

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func ProvisionRoles(ctx context.Context, db *pgxpool.Pool) error {
	tx, err := db.Begin(ctx)
	if err != nil {
		return errors.New("role provisioning database is unavailable")
	}
	defer tx.Rollback(ctx)
	var canCreateRole bool
	if err := tx.QueryRow(ctx, `SELECT rolcreaterole FROM pg_roles WHERE rolname=current_user`).Scan(&canCreateRole); err != nil || !canCreateRole {
		return errors.New("role provisioning credential must have CREATEROLE")
	}
	var migrationExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='workouts_migration')`).Scan(&migrationExists); err != nil || !migrationExists {
		return errors.New("required migration role is unavailable")
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(94722103)`); err != nil {
		return errors.New("role provisioning lock is unavailable")
	}
	var canLogin, superuser, createRole, createDB, replication, bypassRLS bool
	err = tx.QueryRow(ctx, `SELECT rolcanlogin,rolsuper,rolcreaterole,rolcreatedb,rolreplication,rolbypassrls FROM pg_roles WHERE rolname='workouts_security_owner'`).Scan(&canLogin, &superuser, &createRole, &createDB, &replication, &bypassRLS)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err := tx.Exec(ctx, `CREATE ROLE workouts_security_owner NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`); err != nil {
			return errors.New("security owner role could not be created")
		}
	} else if err != nil {
		return errors.New("security owner role could not be verified")
	} else if canLogin || superuser || createRole || createDB || replication || bypassRLS {
		return errors.New("existing security owner role is unsafe")
	}
	var ownerInheritsRole bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_auth_members memberships JOIN pg_roles member_role ON member_role.oid=memberships.member WHERE member_role.rolname='workouts_security_owner')`).Scan(&ownerInheritsRole); err != nil || ownerInheritsRole {
		return errors.New("existing security owner role has unsafe memberships")
	}
	var migrationIsMember bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_auth_members memberships JOIN pg_roles granted_role ON granted_role.oid=memberships.roleid JOIN pg_roles member_role ON member_role.oid=memberships.member WHERE granted_role.rolname='workouts_security_owner' AND member_role.rolname='workouts_migration')`).Scan(&migrationIsMember); err != nil {
		return errors.New("security owner role membership could not be verified")
	}
	if !migrationIsMember {
		if _, err := tx.Exec(ctx, `GRANT workouts_security_owner TO workouts_migration`); err != nil {
			return errors.New("security owner role membership could not be granted")
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return errors.New("role provisioning could not be committed")
	}
	return nil
}
