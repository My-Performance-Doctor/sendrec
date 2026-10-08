package readiness

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestConcurrentWorkerAndInterruptedLeader(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required for synthetic PostgreSQL concurrency test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	claimed := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := WithWorkerLock(ctx, pool, "synthetic-concurrency", func(context.Context, pgx.Tx) error {
			close(claimed)
			<-release
			return context.Canceled
		})
		done <- err
	}()
	select {
	case <-claimed:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	second, err := WithWorkerLock(ctx, pool, "synthetic-concurrency", func(context.Context, pgx.Tx) error {
		t.Error("second worker entered occupied cycle")
		return nil
	})
	if err != nil || second {
		t.Fatalf("second worker claimed=%v err=%v", second, err)
	}
	close(release)
	if err = <-done; err != context.Canceled {
		t.Fatalf("leader interruption: %v", err)
	}
	recovered, err := WithWorkerLock(ctx, pool, "synthetic-concurrency", func(context.Context, pgx.Tx) error { return nil })
	if err != nil || !recovered {
		t.Fatalf("recovery claimed=%v err=%v", recovered, err)
	}
}
