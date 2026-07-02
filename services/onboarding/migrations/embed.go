// Package migrations embeds the goose SQL migrations so they ship in the binary
// and run at startup via pkg/pg.Migrate.
package migrations

import "embed"

// FS holds the embedded goose migration files.
//
//go:embed *.sql
var FS embed.FS
