package database

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// DBTX is the interface for database operations used by handlers.
// Both *pgxpool.Pool and pgxmock satisfy this interface.
type DBTX interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Beginner is a DBTX that can open a transaction, as the pool can and a transaction cannot.
type Beginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}
