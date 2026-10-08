package readiness

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Monitor emits aggregate counts only. It never logs query errors or row data.
// It checks work overdue beyond the documented media and event recovery bounds.
// A successful sample proves monitoring is alive, not that every idle worker is.
// Backlog alarms must not remove a serving API from its load balancer.
type Monitor struct {
	Pool          *pgxpool.Pool
	Output        io.Writer
	mu            sync.RWMutex
	lastSuccess   time.Time
	sampleHealthy bool
}

type Metrics struct {
	ProcessingFailures  int64 `json:"ProcessingFailures"`
	UploadFailures      int64 `json:"UploadFailures"`
	OldProcessingJobs   int64 `json:"OldProcessingJobs"`
	OutboxOldestSeconds int64 `json:"OutboxOldestSeconds"`
	OutboxDeadLetters   int64 `json:"OutboxDeadLetters"`
}

func (m *Monitor) Sample(ctx context.Context) (Metrics, error) {
	var result Metrics
	err := m.Pool.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM videos WHERE status<>'deleted' AND transcript_status='failed'),
 (SELECT count(*) FROM videos WHERE status='uploading' AND created_at<now()-interval '1 hour'),
 (SELECT count(*) FROM videos WHERE (status='processing' AND COALESCE(processing_started_at,updated_at)<now()-interval '32 minutes') OR (transcript_status IN ('pending','processing') AND updated_at<now()-interval '32 minutes')),
 (SELECT COALESCE(EXTRACT(EPOCH FROM now()-min(created_at))::bigint,0) FROM mpd_event_outbox WHERE state IN ('pending','leased','blocked')),
 (SELECT count(*) FROM mpd_event_outbox WHERE state IN ('dead','blocked'))`).Scan(
		&result.ProcessingFailures, &result.UploadFailures, &result.OldProcessingJobs, &result.OutboxOldestSeconds, &result.OutboxDeadLetters)
	if err == nil && m.Output != nil {
		err = json.NewEncoder(m.Output).Encode(result)
	}
	m.mu.Lock()
	if err == nil {
		m.lastSuccess = time.Now()
		m.sampleHealthy = true
	} else {
		m.sampleHealthy = false
	}
	m.mu.Unlock()
	return result, err
}

// WorkersProbe checks recent successful monitoring, independently of queue depth.
// It fails closed before the first sample, on a sample failure, or after monitoring
// stops. It does not assert liveness of each individual processing loop.
func (m *Monitor) WorkersProbe(context.Context) error {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if !m.sampleHealthy || time.Since(m.lastSuccess) > 2*time.Minute {
		return errors.New("worker monitoring unavailable")
	}
	return nil
}

func (m *Monitor) Run(ctx context.Context) {
	defer func() {
		m.mu.Lock()
		m.sampleHealthy = false
		m.mu.Unlock()
	}()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		probe, cancel := context.WithTimeout(ctx, 5*time.Second)
		_, _ = m.Sample(probe)
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
