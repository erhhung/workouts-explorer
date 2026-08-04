package api

import (
	"context"
	"errors"
	"io"
	"os"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sys/unix"
)

type BootstrapAdminOptions struct {
	Username     string
	Email        string
	PasswordFile string
	PasswordMin  int
}

func BootstrapAdmin(ctx context.Context, db *pgxpool.Pool, options BootstrapAdminOptions) error {
	if options.PasswordMin < 12 || options.PasswordMin > 64 {
		return errors.New("bootstrap password minimum is invalid")
	}
	username, canonicalUsernameValue, err := canonicalUsername(options.Username)
	if err != nil {
		return errors.New("bootstrap administrator input is invalid")
	}
	email, canonicalEmailValue, err := canonicalEmail(options.Email)
	if err != nil {
		return errors.New("bootstrap administrator input is invalid")
	}
	password, err := readPrivateRegularFile(options.PasswordFile, 512)
	if err != nil {
		return errors.New("bootstrap password file must be a private regular file")
	}
	hasher := newPasswordHasher(options.PasswordMin)
	if err := hasher.validate(string(password)); err != nil {
		return errors.New("bootstrap administrator input is invalid")
	}
	hash, err := hasher.hash(ctx, string(password))
	if err != nil {
		return errors.New("bootstrap administrator could not be created")
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		return errors.New("bootstrap administrator could not be created")
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(94722102)`); err != nil {
		return errors.New("bootstrap administrator could not be created")
	}
	rows, err := tx.Query(ctx, `SELECT p.canonical_username,p.canonical_email,p.password_hash,p.disabled_at IS NULL FROM app.administrators a JOIN app.authentication_principals p ON p.id=a.principal_id`)
	if err != nil {
		return errors.New("bootstrap administrator could not be created")
	}
	type existingAdmin struct {
		username, email, hash string
		active                bool
	}
	var existing []existingAdmin
	for rows.Next() {
		var admin existingAdmin
		if err := rows.Scan(&admin.username, &admin.email, &admin.hash, &admin.active); err != nil {
			rows.Close()
			return errors.New("bootstrap administrator could not be created")
		}
		existing = append(existing, admin)
	}
	rows.Close()
	if rows.Err() != nil {
		return errors.New("bootstrap administrator could not be created")
	}
	if len(existing) == 1 {
		verified, _, verifyErr := hasher.verify(ctx, string(password), existing[0].hash)
		if verifyErr != nil || !verified || !existing[0].active || existing[0].username != canonicalUsernameValue || existing[0].email != canonicalEmailValue {
			return errors.New("bootstrap administrator does not match existing state")
		}
		if err := tx.Commit(ctx); err != nil {
			return errors.New("bootstrap administrator could not be verified")
		}
		return nil
	}
	if len(existing) != 0 {
		return errors.New("bootstrap administrator does not match existing state")
	}
	id := uuid.Must(uuid.NewV7())
	if _, err = tx.Exec(ctx, `INSERT INTO app.authentication_principals(id,role,username,canonical_username,email,canonical_email,canonicalization_version,password_hash,full_name) VALUES($1,'administrator',$2,$3,$4,$5,1,$6,'Administrator')`, id, username, canonicalUsernameValue, email, canonicalEmailValue, hash); err != nil {
		return errors.New("bootstrap administrator could not be created")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO app.administrators(principal_id) VALUES($1)`, id); err != nil {
		return errors.New("bootstrap administrator could not be created")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO app.audit_records(id,actor_principal_id,action,target_type,target_id) VALUES($2,$1,'administrator.bootstrapped','administrator',$1)`, id, uuid.Must(uuid.NewV7())); err != nil {
		return errors.New("bootstrap administrator could not be created")
	}
	if err := tx.Commit(ctx); err != nil {
		return errors.New("bootstrap administrator could not be created")
	}
	return nil
}

func readPrivateRegularFile(path string, maximum int64) ([]byte, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), path)
	if file == nil {
		_ = unix.Close(descriptor)
		return nil, errors.New("invalid file descriptor")
	}
	defer file.Close()
	var stat unix.Stat_t
	if err := unix.Fstat(descriptor, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o077 != 0 {
		return nil, errors.New("file is not private and regular")
	}
	if int(stat.Uid) != os.Geteuid() {
		return nil, errors.New("file is not owned by the current user")
	}
	value, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil || int64(len(value)) > maximum {
		return nil, errors.New("file exceeds the allowed size")
	}
	return value, nil
}
