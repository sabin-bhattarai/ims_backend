// Package migrations embeds the SQL schema migrations so the API binary can
// apply them without shipping the source tree or the golang-migrate CLI.
package migrations

import "embed"

// FS holds every .sql migration file in this directory.
//
//go:embed *.sql
var FS embed.FS
