package readiness

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestBacklogAlarmsDoNotRemoveServingAPIAndSampleFailuresDo(t *testing.T) {
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
 INSERT INTO mpd_event_outbox VALUES('pending',now()-interval '6 minutes'),('dead',now()),('blocked',now());`)
	if err != nil {
		t.Fatal(err)
	}
	output.Reset()
	metrics, err = monitor.Sample(ctx)
	if err != nil || metrics.UploadFailures != 1 || metrics.OldProcessingJobs != 1 || metrics.OutboxOldestSeconds < 360 || metrics.OutboxDeadLetters != 2 {
		t.Fatalf("fault metrics: %+v %v", metrics, err)
	}
	if err := monitor.WorkersProbe(ctx); err != nil {
		t.Fatalf("backlog removed serving API: %v", err)
	}
	var emitted Metrics
	if err := json.Unmarshal(output.Bytes(), &emitted); err != nil || emitted != metrics {
		t.Fatalf("backlog alarm metrics lost: %+v %v", emitted, err)
	}
	ok := func(context.Context) error { return nil }
	checker := Checker{Database: ok, Storage: ok, Workers: monitor.WorkersProbe}
	assertStatus := func(want int) {
		t.Helper()
		response := httptest.NewRecorder()
		checker.ServeHTTP(response, httptest.NewRequest("GET", "/api/ready", nil))
		if response.Code != want {
			t.Fatalf("readiness status %d, want %d", response.Code, want)
		}
	}
	assertStatus(http.StatusOK)
	monitor.mu.Lock()
	monitor.lastSuccess = time.Now().Add(-3 * time.Minute)
	monitor.mu.Unlock()
	assertStatus(http.StatusServiceUnavailable)
	_, err = pool.Exec(ctx, `DELETE FROM videos;DELETE FROM mpd_event_outbox;`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = monitor.Sample(ctx)
	if err != nil || monitor.WorkersProbe(ctx) != nil {
		t.Fatal("monitor did not recover")
	}
	assertStatus(http.StatusOK)
	pool.Close()
	output.Reset()
	_, err = monitor.Sample(ctx)
	if err == nil || monitor.WorkersProbe(ctx) == nil {
		t.Fatal("closed database reported healthy")
	}
	assertStatus(http.StatusServiceUnavailable)
	if strings.Contains(output.String(), "postgres") || output.Len() != 0 {
		t.Fatal("database failure was logged")
	}
}

func TestProcessingFailureAlarmRecoversAfterWindow(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
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
 CREATE TEMP TABLE mpd_event_outbox(state text,created_at timestamptz);
 INSERT INTO videos VALUES('ready',now(),NULL,now()-interval '1 day','failed'),('ready',now(),NULL,now(),'failed');`)
	if err != nil {
		t.Fatal(err)
	}
	monitor := Monitor{Pool: pool}
	sampled, err := monitor.Sample(ctx)
	if err != nil || sampled.ProcessingFailures != 1 {
		t.Fatalf("old failure obscured new failure: %+v %v", sampled, err)
	}
	_, err = pool.Exec(ctx, `UPDATE videos SET updated_at=now()-interval '16 minutes'`)
	if err != nil {
		t.Fatal(err)
	}
	sampled, err = monitor.Sample(ctx)
	if err != nil || sampled.ProcessingFailures != 0 {
		t.Fatalf("failure alarm did not age out: %+v %v", sampled, err)
	}
	_, err = pool.Exec(ctx, `INSERT INTO videos VALUES('ready',now(),NULL,now(),'failed')`)
	if err != nil {
		t.Fatal(err)
	}
	sampled, err = monitor.Sample(ctx)
	if err != nil || sampled.ProcessingFailures != 1 {
		t.Fatalf("new failure not observed: %+v %v", sampled, err)
	}
	_, err = pool.Exec(ctx, `UPDATE videos SET transcript_status='ready'`)
	if err != nil {
		t.Fatal(err)
	}
	sampled, err = monitor.Sample(ctx)
	if err != nil || sampled.ProcessingFailures != 0 {
		t.Fatalf("recovered failure still counted: %+v %v", sampled, err)
	}
}
