// Package migrations embeds the goose SQL migrations so the API binary
// carries its own schema and applies it at startup.
package migrations

import "embed"

//go:embed *.sql
var FS embed.FS
