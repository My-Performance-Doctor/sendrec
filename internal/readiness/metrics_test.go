package readiness

import (
	"context"
	"testing"
	"time"
)

func TestWorkersProbeRequiresRecentSuccessfulSample(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name          string
		sampleHealthy bool
		lastSuccess   time.Time
		healthy       bool
	}{
		{name: "never sampled"},
		{name: "recent success", sampleHealthy: true, lastSuccess: time.Now(), healthy: true},
		{name: "stale success", sampleHealthy: true, lastSuccess: time.Now().Add(-3 * time.Minute)},
		{name: "failed sample after success", sampleHealthy: false, lastSuccess: time.Now()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			monitor := Monitor{sampleHealthy: tc.sampleHealthy, lastSuccess: tc.lastSuccess}
			if got := monitor.WorkersProbe(ctx) == nil; got != tc.healthy {
				t.Fatalf("healthy=%v want=%v", got, tc.healthy)
			}
		})
	}
}

func TestStoppedMonitorImmediatelyFailsReadiness(t *testing.T) {
	monitor := Monitor{sampleHealthy: true, lastSuccess: time.Now()}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	monitor.Run(ctx)
	if monitor.WorkersProbe(context.Background()) == nil {
		t.Fatal("stopped monitor retained readiness")
	}
}
