package mpdevents

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sendrec/sendrec/internal/database"
)

type pollDB struct {
	database.DBTX
	queries []string
	cancel  context.CancelFunc
}

func (db *pollDB) QueryRow(_ context.Context, sql string, _ ...any) pgx.Row {
	db.queries = append(db.queries, sql)
	db.cancel()
	return emptyPollRow{}
}

type emptyPollRow struct{}

func (emptyPollRow) Scan(...any) error { return pgx.ErrNoRows }

func TestDeliveryPollDoesNotScanRetainedBacklog(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	db := &pollDB{cancel: cancel}
	worker := &Worker{db: db, config: Config{Enabled: true}}
	ticks := make(chan time.Time, 1)
	ticks <- time.Now()
	worker.runTicks(ctx, ticks)
	if len(db.queries) != 1 || !strings.HasPrefix(db.queries[0], "UPDATE mpd_event_outbox SET state='leased'") {
		t.Fatalf("delivery poll queried retained backlog: %v", db.queries)
	}
}
