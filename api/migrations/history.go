package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
)

// ValidateHistory checks the active applied prefix without changing the database.
// The caller holds the migration command's session advisory lock before Goose Up.
func ValidateHistory(ctx context.Context, db *sql.DB) error {
	entries, err := fs.ReadDir(Files, ".")
	if err != nil {
		return err
	}
	known := make(map[int64]bool)
	var maximum int64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		prefix, _, _ := strings.Cut(entry.Name(), "_")
		version, err := strconv.ParseInt(prefix, 10, 64)
		if err != nil || version < 1 || known[version] {
			return fmt.Errorf("invalid active migration %q", entry.Name())
		}
		known[version] = true
		if version > maximum {
			maximum = version
		}
	}
	if maximum == 0 || int64(len(known)) != maximum {
		return fmt.Errorf("active migration history is not a contiguous prefix")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	defer tx.Rollback()
	invalid := func(reason string) error {
		return fmt.Errorf("migration history rejected: %s; use the matching retained release and the operator recovery procedure before retrying", reason)
	}
	var metadataExists, ledgerExists bool
	if err := tx.QueryRowContext(ctx, `SELECT to_regclass('app.schema_metadata') IS NOT NULL,
		to_regclass('public.goose_db_version') IS NOT NULL`).Scan(&metadataExists, &ledgerExists); err != nil {
		return err
	}
	if !ledgerExists {
		if metadataExists {
			return invalid("schema metadata exists without migration history")
		}
		return nil
	}
	var count, distinct, first, last int64
	var allApplied bool
	if err := tx.QueryRowContext(ctx, `SELECT count(*), count(DISTINCT version_id),
		coalesce(min(version_id),-1), coalesce(max(version_id),-1), coalesce(bool_and(is_applied IS TRUE),false)
		FROM public.goose_db_version`).Scan(&count, &distinct, &first, &last, &allApplied); err != nil {
		return err
	}
	if first != 0 || last < 0 || last > maximum || count != last+1 || distinct != count || !allApplied {
		return invalid("applied migrations are not an exact known prefix")
	}
	if !metadataExists {
		if last != 0 {
			return invalid("applied migrations exist without schema metadata")
		}
		return nil
	}
	var version, minimum, rows int64
	var marker sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT schema_version, minimum_runtime_version,
		(to_jsonb(metadata)->>'migration_baseline')::bigint, (SELECT count(*) FROM app.schema_metadata)
		FROM app.schema_metadata metadata WHERE singleton`).Scan(&version, &minimum, &marker, &rows); err != nil {
		return invalid("schema metadata cannot be read")
	}
	if rows != 1 || version < 1 || version != last || minimum < 1 || minimum > version {
		return invalid("schema metadata does not match applied migrations")
	}
	if version >= 27 {
		if !marker.Valid || marker.Int64 != 27 || (version == 27 && minimum != 26) {
			return invalid("canonical coverage metadata is missing or inconsistent")
		}
	} else if marker.Valid {
		return invalid("coverage metadata precedes its migration")
	}
	return nil
}
