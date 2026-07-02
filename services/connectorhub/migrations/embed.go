// Package migrations embeds the goose SQL migrations so the binary is
// self-contained (no files to ship alongside the image).
package migrations

import "embed"

// FS holds the embedded goose migration files (passed to pkg/pg.Migrate).
//
//go:embed *.sql
var FS embed.FS
