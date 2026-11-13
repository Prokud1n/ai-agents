package migrator

import (
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
)

// Project migrations are named "NNN-description.up.sql" (see CODE_STYLE.md), while golang-migrate
// only parses "NNN_description.up.sql" and silently skips anything else. migrateFS exposes the
// project files under golang-migrate names and fails on a file that matches neither, so a
// misnamed migration cannot be skipped unnoticed.
var migrationNameRe = regexp.MustCompile(`^([0-9]+)-([^.]+)\.(up|down)\.sql$`)

var errInvalidMigrationName = errors.New("invalid migration file name, expected NNN-description.up.sql")

const sqlExt = ".sql"

type migrateFS struct {
	fsys     fs.FS
	entries  []fs.DirEntry
	original map[string]string // golang-migrate name -> project name
}

func newMigrateFS(fsys fs.FS) (*migrateFS, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("failed to read migrations dir: %w", err)
	}

	result := &migrateFS{
		fsys:     fsys,
		entries:  make([]fs.DirEntry, 0, len(entries)),
		original: make(map[string]string, len(entries)),
	}

	for _, e := range entries {
		if e.IsDir() || path.Ext(e.Name()) != sqlExt {
			continue
		}

		name, err := toMigrateName(e.Name())
		if err != nil {
			return nil, err
		}

		result.entries = append(result.entries, renamedEntry{DirEntry: e, name: name})
		result.original[name] = e.Name()
	}

	return result, nil
}

func (m *migrateFS) Open(name string) (fs.File, error) {
	original, ok := m.original[name]
	if !ok {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
	}

	file, err := m.fsys.Open(original)
	if err != nil {
		return nil, fmt.Errorf("failed to open migration %s: %w", original, err)
	}

	return file, nil
}

func (m *migrateFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if name != "." {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}

	return slices.Clone(m.entries), nil
}

type renamedEntry struct {
	fs.DirEntry

	name string
}

func (e renamedEntry) Name() string {
	return e.name
}

// toMigrateName converts "001-init.up.sql" to "001_init.up.sql".
func toMigrateName(name string) (string, error) {
	if !migrationNameRe.MatchString(name) {
		return "", fmt.Errorf("failed to parse migration name %q: %w", name, errInvalidMigrationName)
	}

	return migrationNameRe.ReplaceAllString(name, "${1}_${2}.${3}.sql"), nil
}
