// Package migrations embeds SQL migrations into the binary.
package migrations

import "embed"

// FS contains migration files named "NNN_description.up.sql" / "NNN_description.down.sql".
//
//go:embed *.sql
var FS embed.FS
