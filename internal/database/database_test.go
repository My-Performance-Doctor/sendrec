package database

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/sendrec/sendrec/migrations"
)

func TestConnect_InvalidURL(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := Connect(ctx, "postgres://invalid:invalid@localhost:1/nonexistent?sslmode=disable&connect_timeout=1")
	if err == nil {
		t.Fatal("expected error for invalid database URL")
	}
}

func TestMigrate_InvalidURL(t *testing.T) {
	db := &DB{}
	err := db.Migrate("postgres://invalid:invalid@localhost:1/nonexistent")
	if err == nil {
		t.Fatal("expected error for invalid migration URL")
	}
}

type fakeMigrator struct {
	version uint
	dirty   bool
	err     error
	upErr   error
	ups     int
}

func (f *fakeMigrator) Version() (uint, bool, error) { return f.version, f.dirty, f.err }
func (f *fakeMigrator) Up() error                    { f.ups++; return f.upErr }

func TestMigrateUp_RunsForwardAndSkipsOnlyACleanNewerSchema(t *testing.T) {
	cases := []struct {
		name    string
		m       fakeMigrator
		ups     int
		wantErr bool
	}{
		{"new database", fakeMigrator{err: migrate.ErrNilVersion}, 1, false},
		{"forward release", fakeMigrator{version: 82}, 1, false},
		{"current release", fakeMigrator{version: 84, upErr: migrate.ErrNoChange}, 1, false},
		{"rollback to an older release", fakeMigrator{version: 85}, 0, false},
		{"dirty and newer still reaches Up, which refuses it", fakeMigrator{version: 85, dirty: true, upErr: migrate.ErrDirty{Version: 85}}, 1, true},
		{"unreadable version", fakeMigrator{err: errors.New("connection refused")}, 0, true},
		{"failed migration", fakeMigrator{version: 83, upErr: errors.New("syntax error")}, 1, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := c.m
			err := migrateUp(&m, 84)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, want error %v", err, c.wantErr)
			}
			if m.ups != c.ups {
				t.Fatalf("Up ran %d times, want %d", m.ups, c.ups)
			}
		})
	}
}

func TestNewestVersion_IsTheHighestShippedMigration(t *testing.T) {
	listing, err := iofs.New(migrations.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	newest, err := newestVersion(listing)
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := fs.Glob(migrations.FS, "*.up.sql")
	last := entries[len(entries)-1]
	var want uint
	if _, err := fmt.Sscanf(last, "%06d_", &want); err != nil {
		t.Fatal(err)
	}
	if newest != want {
		t.Fatalf("newest = %d, want %d from %s", newest, want, last)
	}
}

// A synthetic database only: a forward release applies its migrations, and redeploying the
// release before it, whose image lacks the newest migration, succeeds without touching the schema.
func TestMigrateFrom_ForwardThenRollback(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required for synthetic database checks")
	}
	if err := migrateFrom(migrations.FS, dsn); err != nil {
		t.Fatalf("forward release: %v", err)
	}
	entries, _ := fs.Glob(migrations.FS, "*.sql")
	older := fstest.MapFS{}
	newest := entries[len(entries)-1][:6]
	for _, name := range entries {
		if strings.HasPrefix(name, newest) {
			continue
		}
		body, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		older[name] = &fstest.MapFile{Data: body}
	}
	if err := migrateFrom(older, dsn); err != nil {
		t.Fatalf("rollback to the release before %s: %v", newest, err)
	}
	if err := migrateFrom(migrations.FS, dsn); err != nil {
		t.Fatalf("forward again: %v", err)
	}
}
