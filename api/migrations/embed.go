package migrations

import (
	"embed"
	"io/fs"
)

//go:embed sql/*.sql
var sqlFiles embed.FS

type migrationFS struct {
	root fs.FS
}

func (f migrationFS) Open(name string) (fs.File, error) {
	return f.root.Open(name)
}

func (f migrationFS) ReadFile(name string) ([]byte, error) {
	return fs.ReadFile(f.root, name)
}

func (f migrationFS) ReadDir(name string) ([]fs.DirEntry, error) {
	return fs.ReadDir(f.root, name)
}

// Files contains the active Goose SQL migrations for the migration command.
// Its virtual root retains the migration basenames, independent of disk layout.
var Files = func() migrationFS {
	root, err := fs.Sub(sqlFiles, "sql")
	if err != nil {
		panic(err)
	}
	return migrationFS{root: root}
}()
