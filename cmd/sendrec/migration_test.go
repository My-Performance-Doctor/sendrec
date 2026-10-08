package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// This child process runs the same entrypoint as the ECS migration definition.
func TestMigrationProcess(t *testing.T) {
	if os.Getenv("SENDREC_MIGRATION_SUBPROCESS") != "1" {
		return
	}
	main()
	os.Exit(0)
}

func TestMigrationOnlyNeedsDatabaseCredentials(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required for synthetic migration task")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := conn.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	name := fmt.Sprintf("migration_entrypoint_%d", time.Now().UnixNano())
	if _, err = conn.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, err := conn.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)")
		if err != nil {
			t.Error(err)
		}
	}()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMigrationProcess$")
	command.Env = []string{"SENDREC_MIGRATION_SUBPROCESS=1", "MIGRATIONS_MODE=only", "DATABASE_URL=" + u.String()}
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("DB-only migration task: %v %s", err, output)
	}
	restored, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := restored.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	var exists bool
	if err = restored.QueryRow(ctx, `SELECT to_regclass('public.mpd_event_outbox') IS NOT NULL AND NOT EXISTS(SELECT 1 FROM schema_migrations WHERE dirty)`).Scan(&exists); err != nil || !exists {
		t.Fatalf("migration postcondition: %v %v", exists, err)
	}
}

func TestApplicationModesStillRequireJWTSecret(t *testing.T) {
	for _, mode := range []string{"auto", "skip"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMigrationProcess$")
			command.Env = []string{"SENDREC_MIGRATION_SUBPROCESS=1", "MIGRATIONS_MODE=" + mode, "DATABASE_URL=postgres://synthetic.invalid/unreachable"}
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "JWT_SECRET is required") {
				t.Fatalf("unconfigured application started: %v %s", err, output)
			}
		})
	}
}
