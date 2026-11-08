// Package testdb opens and resets a Postgres database for integration tests.
// It is intentionally only imported from _test files in this repository.
package testdb

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"hacktheburgh/backend/internal/db"
)

const truncateSQL = `
TRUNCATE data_records, project_sources, insights, reviews, runs,
         workflow_steps, workflows, projects, data_sources
CASCADE`

// Open connects to TEST_DATABASE_URL, applies migrations, and clears all
// application tables. Tests are skipped when TEST_DATABASE_URL is unset.
func Open(t *testing.T) *pgxpool.Pool {
	t.Helper()

	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := db.Open(ctx, url)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := db.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	if _, err := pool.Exec(ctx, truncateSQL); err != nil {
		t.Fatalf("truncate test database: %v", err)
	}
	return pool
}
