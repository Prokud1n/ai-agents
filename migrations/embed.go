// Package migrations embeds SQL migrations into the binary.
package migrations

import "embed"

// FS contains migration files named "NNN-description.up.sql".
//
//go:embed *.sql
var FS embed.FS
