package migrator

import (
	"io/fs"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Prokud1n/ai-agents/migrations"
)

func TestMigrateURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "replaces scheme",
			input:    "postgres://agent@localhost:5432/ai_agents?sslmode=disable",
			expected: "pgx5://agent@localhost:5432/ai_agents?sslmode=disable",
		},
		{
			name:     "keeps escaped user info",
			input:    "postgres://app%40corp@db:5432/agents?sslmode=require",
			expected: "pgx5://app%40corp@db:5432/agents?sslmode=require",
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

var migrationNameRe = regexp.MustCompile(`^(\d+)_([a-z0-9_]+)\.(up|down)\.sql$`)

// golang-migrate silently skips files it cannot parse, so every SQL file must follow its naming
// and every up migration must have a down pair.
func TestMigrationFiles(t *testing.T) {
	t.Parallel()

	files, err := fs.Glob(migrations.FS, "*.sql")
	require.NoError(t, err)
	require.NotEmpty(t, files)

	for _, name := range files {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			matches := migrationNameRe.FindStringSubmatch(name)
			require.NotNil(t, matches, "expected NNN_description.up.sql or NNN_description.down.sql")

			pairDirection := string(DownCommand)
			if matches[3] == string(DownCommand) {
				pairDirection = string(UpCommand)
			}

			assert.Contains(t, files, matches[1]+"_"+matches[2]+"."+pairDirection+".sql")
		})
	}
}
