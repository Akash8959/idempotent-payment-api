// Package store provides PostgreSQL connection management, database migrations,
// and repository access for payments and idempotency keys.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/example/idempotent-payment-api/internal/config"
	"github.com/example/idempotent-payment-api/migrations"
)

// NewPool initializes a managed PostgreSQL connection pool using pgxpool.
func NewPool(ctx context.Context, cfg config.DatabaseConfig) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("failed to parse database DSN: %w", err)
	}

	poolCfg.MaxConns = cfg.MaxConns
	poolCfg.MinConns = cfg.MinConns
	poolCfg.MaxConnLifetime = cfg.MaxConnLifetime
	poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime

	ctxTimeout, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	pool, err := pgxpool.NewWithConfig(ctxTimeout, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create pgx connection pool: %w", err)
	}

	if err := pool.Ping(ctxTimeout); err != nil {
		pool.Close()
		return nil, fmt.Errorf("failed to ping database: %w", err)
	}

	return pool, nil
}

// MigrateUp executes all pending migrations up to the latest version.
func MigrateUp(dsn string) error {
	m, err := newMigrator(dsn)
	if err != nil {
		return err
	}
	defer m.Close()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up failed: %w", err)
	}
	return nil
}

// MigrateDown rolls back all executed migrations.
func MigrateDown(dsn string) error {
	m, err := newMigrator(dsn)
	if err != nil {
		return err
	}
	defer m.Close()

	if err := m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate down failed: %w", err)
	}
	return nil
}

func newMigrator(dsn string) (*migrate.Migrate, error) {
	d, err := iofs.New(migrations.FS, ".")
	if err != nil {
		return nil, fmt.Errorf("failed to initialize migration filesystem: %w", err)
	}

	// golang-migrate pgx/v5 driver requires pgx5:// scheme
	migrateDSN := dsn
	if len(migrateDSN) > 11 && migrateDSN[:11] == "postgres://" {
		migrateDSN = "pgx5://" + migrateDSN[11:]
	} else if len(migrateDSN) > 13 && migrateDSN[:13] == "postgresql://" {
		migrateDSN = "pgx5://" + migrateDSN[13:]
	}

	m, err := migrate.NewWithSourceInstance("iofs", d, migrateDSN)
	if err != nil {
		return nil, fmt.Errorf("failed to create migrate instance: %w", err)
	}

	return m, nil
}
