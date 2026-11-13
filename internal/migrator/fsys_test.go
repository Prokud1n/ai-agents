package migrator

import (
	"io"
	"io/fs"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToMigrateName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		expected  string
		expectErr bool
	}{
		{name: "up migration", input: "001-init.up.sql", expected: "001_init.up.sql"},
		{name: "down migration", input: "002-add-posts.down.sql", expected: "002_add-posts.down.sql"},
		{name: "underscore in description", input: "010-create_users.up.sql", expected: "010_create_users.up.sql"},
		{name: "golang-migrate style is rejected", input: "001_init.up.sql", expectErr: true},
		{name: "no direction", input: "001-init.sql", expectErr: true},
		{name: "no version", input: "init.up.sql", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := toMigrateName(tt.input)
			if tt.expectErr {
				require.ErrorIs(t, err, errInvalidMigrationName)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestMigrateFS(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     fstest.MapFS
		expected  map[string]string
		expectErr bool
	}{
		{
			name: "renames sql files and skips others",
			input: fstest.MapFS{
				"001-init.up.sql":     {Data: []byte("CREATE EXTENSION vector;")},
				"002-posts.up.sql":    {Data: []byte("CREATE TABLE posts();")},
				"embed.go":            {Data: []byte("package migrations")},
				"nested/003-x.up.sql": {Data: []byte("SELECT 1;")},
				"002-posts.down.sql":  {Data: []byte("DROP TABLE posts;")},
			},
			expected: map[string]string{
				"001_init.up.sql":    "CREATE EXTENSION vector;",
				"002_posts.down.sql": "DROP TABLE posts;",
				"002_posts.up.sql":   "CREATE TABLE posts();",
			},
		},
		{
			name: "misnamed migration fails",
			input: fstest.MapFS{
				"001_init.up.sql": {Data: []byte("SELECT 1;")},
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			fsys, err := newMigrateFS(tt.input)
			if tt.expectErr {
				require.ErrorIs(t, err, errInvalidMigrationName)

				return
			}

			require.NoError(t, err)

			entries, err := fs.ReadDir(fsys, ".")
			require.NoError(t, err)

			got := make(map[string]string, len(entries))
			for _, e := range entries {
				file, err := fsys.Open(e.Name())
				require.NoError(t, err)

				data, err := io.ReadAll(file)
				require.NoError(t, err)
				require.NoError(t, file.Close())

				got[e.Name()] = string(data)
			}

			assert.Equal(t, tt.expected, got)
		})
	}
}

func TestMigrateURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "replaces scheme",
			input:    "postgres://agent:secret@localhost:5432/ai_agents?sslmode=disable",
			expected: "pgx5://agent:secret@localhost:5432/ai_agents?sslmode=disable",
		},
		{
			name:     "keeps escaped password",
			input:    "postgres://app:p%40ss@db:5432/agents?sslmode=require",
			expected: "pgx5://app:p%40ss@db:5432/agents?sslmode=require",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := migrateURL(tt.input)

			require.NoError(t, err)
			assert.Equal(t, tt.expected, got)
		})
	}
}
