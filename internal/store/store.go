// Package store owns the Postgres connection pool and schema migrations.
package store

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"slices"

	"github.com/jackc/pgx/v5/pgxpool"

	"zkillmanager/migrations"
)

// migrationLockKey guards Migrate with a Postgres session advisory lock so two instances
// starting at once (old + new during a rolling deploy) can't both run a new migration's
// bare CREATE TABLE. Value is "zkillmgr" packed as ASCII -- distinct from any lock other
// apps sharing the eve DB might take.
const migrationLockKey int64 = 0x7A6B696C6C6D6772

type Store struct {
	pool *pgxpool.Pool
}

// Open connects and verifies the pool is reachable.
func Open(ctx context.Context, dsn string) (*Store, error) {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return nil, fmt.Errorf("pgxpool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping: %w", err)
	}
	return &Store{pool: pool}, nil
}

func (s *Store) Close() { s.pool.Close() }

// Ping is used by the health check.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Migrate applies every embedded migration not yet recorded, each in its own
// transaction. Applied versions are tracked in killmails.schema_migrations. Concurrent
// migrators are serialized by an advisory lock held for the whole run; the loop runs on
// that same connection so a small pool can't deadlock against the held connection.
func (s *Store) Migrate(ctx context.Context) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration conn: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, migrationLockKey); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	// A session-scoped lock outlives Release, so free it explicitly; Background context so a
	// cancelled ctx still releases it for other instances.
	defer func() {
		if _, err := conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, migrationLockKey); err != nil {
			slog.Warn("release migration lock", "err", err)
		}
	}()

	// The killmails schema is created (owned by this role) by the one-time bootstrap, not
	// here -- the app role deliberately lacks CREATE on the database, and Postgres enforces
	// that even for CREATE SCHEMA IF NOT EXISTS. We only create tables within the owned schema.
	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS killmails.schema_migrations (
		version     TEXT PRIMARY KEY,
		applied_at  TIMESTAMPTZ NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("ensure migrations table: %w", err)
	}

	files, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		return err
	}
	slices.Sort(files)

	for _, name := range files {
		var applied bool
		if err := conn.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM killmails.schema_migrations WHERE version = $1)`, name,
		).Scan(&applied); err != nil {
			return fmt.Errorf("check %s: %w", name, err)
		}
		if applied {
			continue
		}

		sqlBytes, err := migrations.FS.ReadFile(name)
		if err != nil {
			return err
		}

		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO killmails.schema_migrations (version) VALUES ($1)`, name,
		); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit %s: %w", name, err)
		}
	}
	return nil
}
