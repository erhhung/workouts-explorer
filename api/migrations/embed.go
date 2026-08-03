package migrations

import "embed"

// Files contains immutable Goose SQL migrations for the migration command.
//
//go:embed *.sql
var Files embed.FS
