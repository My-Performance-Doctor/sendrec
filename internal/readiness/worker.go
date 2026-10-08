package readiness

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WithWorkerLock runs one synchronous cycle through the transaction that owns
// its cross-process advisory lock. The callback must not spawn background work.
// S3 writes still require unique versioned keys and database publication fencing.
// External sends require a durable outbox; a lock alone cannot deduplicate them.
func WithWorkerLock(ctx context.Context, pool *pgxpool.Pool, name string, work func(context.Context, pgx.Tx) error) (bool, error) {
	if name == "" || work == nil {
		return false, errors.New("worker name and callback required")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tx.Rollback(cleanup)
	}()
	var claimed bool
	if err = tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1, 0))`, "sendrec-worker:"+name).Scan(&claimed); err != nil {
		return false, err
	}
	if !claimed {
		return false, nil
	}
	if err = work(ctx, tx); err != nil {
		return true, err
	}
	return true, tx.Commit(ctx)
}
