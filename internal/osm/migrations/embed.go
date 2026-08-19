package migrations

import "embed"

// Files contains immutable Goose migrations for the public OSM database.
//
//go:embed *.sql
var Files embed.FS
