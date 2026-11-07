// Package db holds the embedded migrations and (soon) the pgx pool,
// migration runner and queries.
package db

import "embed"

// Migrations are applied in filename order at startup and tracked in
// schema_migrations.
//
//go:embed migrations/*.sql
var Migrations embed.FS
