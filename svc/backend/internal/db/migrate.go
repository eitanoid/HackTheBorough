// Package db holds the connection pool and the migration runner.
package db

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Migrations are applied in filename order at startup and tracked in
// schema_migrations. Never edit an applied file once the schema is frozen:
// add a new numbered file instead.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// advisoryLockID serialises migrators across replicas ("hacktbmig").
const advisoryLockID int64 = 0x6861636b74626d

// Open connects to Postgres, retrying briefly so the backend can start
// before the database is ready.
func Open(ctx context.Context, url string) (*pgxpool.Pool, error) {
	if url == "" {
		return nil, fmt.Errorf("DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for i := 0; i < 30; i++ {
		if lastErr = pool.Ping(ctx); lastErr == nil {
			return pool, nil
		}
		select {
		case <-ctx.Done():
			pool.Close()
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	pool.Close()
	return nil, fmt.Errorf("database not reachable: %w", lastErr)
}

// Migrate applies every embedded migration not yet recorded in
// schema_migrations. Each file runs in its own transaction together with its
// bookkeeping row, so a failure leaves the database at the last good version.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	return migrate(ctx, pool, migrationsFS)
}

func migrate(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) error {
	// The advisory lock is session-scoped, so hold one connection throughout.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, advisoryLockID); err != nil {
		return fmt.Errorf("migration lock: %w", err)
	}
	defer conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, advisoryLockID)

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version    text PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	applied := map[string]bool{}
	rows, err := conn.Query(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return err
		}
		applied[v] = true
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	files, err := fs.Glob(fsys, "migrations/*.sql")
	if err != nil {
		return err
	}
	sort.Strings(files)

	for _, f := range files {
		version := f[len("migrations/"):]
		if applied[version] {
			continue
		}
		sqlBytes, err := fs.ReadFile(fsys, f)
		if err != nil {
			return err
		}
		if err := applyOne(ctx, conn, version, string(sqlBytes)); err != nil {
			return fmt.Errorf("migration %s: %w", version, err)
		}
		slog.Info("migration applied", "version", version)
	}
	return nil
}

func applyOne(ctx context.Context, conn *pgxpool.Conn, version, sql string) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	// Multi-statement files need the simple protocol.
	if _, err := tx.Exec(ctx, sql, pgx.QueryExecModeSimpleProtocol); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, version); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
