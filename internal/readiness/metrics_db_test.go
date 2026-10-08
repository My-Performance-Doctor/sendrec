package readiness

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMonitorDetectsOverdueWorkAndRedactsFailures(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required for synthetic database checks")
	}
	ctx := context.Background()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	_, err = pool.Exec(ctx, `CREATE TEMP TABLE videos(status text,created_at timestamptz,processing_started_at timestamptz,updated_at timestamptz,transcript_status text);
 CREATE TEMP TABLE mpd_event_outbox(state text,created_at timestamptz);`)
	if err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	monitor := Monitor{Pool: pool, Output: &output}
	if monitor.WorkersProbe(ctx) == nil {
		t.Fatal("unsampled monitor reported healthy")
	}
	metrics, err := monitor.Sample(ctx)
	if err != nil || metrics != (Metrics{}) || monitor.WorkersProbe(ctx) != nil {
		t.Fatalf("empty healthy control: %+v %v", metrics, err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO videos VALUES('uploading',now()-interval '2 hours',NULL,now(),'none'),('processing',now(),now()-interval '40 minutes',now(),'none');
 INSERT INTO mpd_event_outbox VALUES('pending',now()-interval '6 minutes'),('dead',now());`)
	if err != nil {
		t.Fatal(err)
	}
	metrics, err = monitor.Sample(ctx)
	if err != nil || metrics.UploadFailures != 1 || metrics.OldProcessingJobs != 1 || metrics.OutboxOldestSeconds < 360 || metrics.OutboxDeadLetters != 1 {
		t.Fatalf("fault metrics: %+v %v", metrics, err)
	}
	if monitor.WorkersProbe(ctx) == nil {
		t.Fatal("overdue workers reported healthy")
	}
	_, err = pool.Exec(ctx, `DELETE FROM videos;DELETE FROM mpd_event_outbox;`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = monitor.Sample(ctx)
	if err != nil || monitor.WorkersProbe(ctx) != nil {
		t.Fatal("monitor did not recover")
	}
	pool.Close()
	output.Reset()
	_, err = monitor.Sample(ctx)
	if err == nil || monitor.WorkersProbe(ctx) == nil {
		t.Fatal("closed database reported healthy")
	}
	if strings.Contains(output.String(), "postgres") || output.Len() != 0 {
		t.Fatal("database failure was logged")
	}
}
