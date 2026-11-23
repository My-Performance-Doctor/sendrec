package database

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	"github.com/golang-migrate/migrate/v4/source"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sendrec/sendrec/migrations"
)

type DB struct {
	Pool *pgxpool.Pool
}

func Connect(ctx context.Context, databaseURL string) (*DB, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("connect to database: %w", err)
	}

	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}

	return &DB{Pool: pool}, nil
}

func (db *DB) Close() {
	db.Pool.Close()
}

func (db *DB) Ping(ctx context.Context) error {
	return db.Pool.Ping(ctx)
}

func (db *DB) Migrate(databaseURL string) error {
	return migrateFrom(migrations.FS, databaseURL)
}

// migrateFrom applies every migration in files that the database lacks. A database already
// past the newest migration in files, as after rolling back to an older release, is left as
// it is: golang-migrate would otherwise stop with "no migration found for version N", and an
// older release could never be deployed again after a newer one migrated.
func migrateFrom(files fs.FS, databaseURL string) error {
	listing, err := iofs.New(files, ".")
	if err != nil {
		return fmt.Errorf("create migration source: %w", err)
	}
	newest, err := newestVersion(listing)
	_ = listing.Close()
	if err != nil {
		return err
	}

	sourceDriver, err := iofs.New(files, ".")
	if err != nil {
		return fmt.Errorf("create migration source: %w", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", sourceDriver, databaseURL)
	if err != nil {
		return fmt.Errorf("create migrator: %w", err)
	}
	defer func() { _, _ = m.Close() }()

	return migrateUp(m, newest)
}

type upMigrator interface {
	Version() (uint, bool, error)
	Up() error
}

// migrateUp runs Up unless the schema is clean and already newer than newest. A dirty schema
// always reaches Up, which refuses it, so a half-applied migration is never passed over.
func migrateUp(m upMigrator, newest uint) error {
	version, dirty, err := m.Version()
	switch {
	case errors.Is(err, migrate.ErrNilVersion):
		// A new database: every migration runs.
	case err != nil:
		return fmt.Errorf("read schema version: %w", err)
	case !dirty && version > newest:
		slog.Info("database schema is newer than this release; no migration to run",
			"schema_version", version, "release_newest_migration", newest)
		return nil
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}

// newestVersion is the highest migration version the source holds.
func newestVersion(files source.Driver) (uint, error) {
	version, err := files.First()
	if err != nil {
		return 0, fmt.Errorf("read first migration: %w", err)
	}
	for {
		next, err := files.Next(version)
		if errors.Is(err, fs.ErrNotExist) {
			return version, nil
		}
		if err != nil {
			return 0, fmt.Errorf("read migration after %d: %w", version, err)
		}
		version = next
	}
}
