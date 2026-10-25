package migrations

import "embed"

// FS holds all SQL migration files embedded into the binary.
//
//go:embed *.sql
var FS embed.FS
