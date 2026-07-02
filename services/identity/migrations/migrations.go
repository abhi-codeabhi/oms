// Package migrations embeds the goose SQL migrations for the identity service
// so main.go can run them at startup via pkg/pg.Migrate.
package migrations

import "embed"

// FS holds the embedded goose migration files.
//
//go:embed *.sql
var FS embed.FS
