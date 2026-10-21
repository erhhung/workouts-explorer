package migrations

import "embed"

// Files contains the active Goose SQL migrations for the migration command.
//
//go:embed *.sql
var Files embed.FS
