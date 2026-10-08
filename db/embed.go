// Package db carries the database schema inside the binaries, so memoryd runs
// from any directory without a checkout beside it.
package db

import _ "embed"

// Schema is db/schema.sql.
//
//go:embed schema.sql
var Schema string
