// Package readiness checks dependencies without returning private failure details.
package readiness

import (
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type Probe func(context.Context) error

type Checker struct {
	Database Probe
	Storage  Probe
	Workers  Probe
}

// ServeHTTP requires all dependencies, including the monitoring probe. A missing
// probe is an incomplete configuration and must not make a release healthy.
func (c Checker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	checks := map[string]bool{}
	healthy := true
	for _, p := range []struct {
		name  string
		check Probe
	}{
		{"database", c.Database}, {"storage", c.Storage}, {"workers", c.Workers},
	} {
		checks[p.name] = p.check != nil && p.check(ctx) == nil
		healthy = healthy && checks[p.name]
	}
	status := "ready"
	if !healthy {
		status = "unavailable"
		w.WriteHeader(http.StatusServiceUnavailable)
	}
	_ = json.NewEncoder(w).Encode(struct {
		Status string          `json:"status"`
		Checks map[string]bool `json:"checks"`
	}{status, checks})
}
