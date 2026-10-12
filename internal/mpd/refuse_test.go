package mpd

import (
	"bytes"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRefuseLogsStageWithoutCredentials(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	defer slog.SetDefault(prev)

	for _, c := range []struct {
		err     error
		outcome string
		code    int
	}{{ErrDenied, "denied", 401}, {ErrUnavailable, "unavailable", 503}, {ErrCapacity, "capacity", 409}} {
		buf.Reset()
		r := httptest.NewRequest("POST", "/api/auth/mpd/session", nil)
		r.Header.Set("Authorization", "Bearer synthetic-secret-token")
		w := httptest.NewRecorder()
		refuse(w, r, "exchange", "provision", c.err)
		line := buf.String()
		if w.Code != c.code {
			t.Fatalf("%s: status %d", c.outcome, w.Code)
		}
		for _, want := range []string{"entry=exchange", "stage=provision", "outcome=" + c.outcome} {
			if !strings.Contains(line, want) {
				t.Fatalf("log %q missing %q", line, want)
			}
		}
		if strings.Contains(line, "synthetic-secret-token") {
			t.Fatal("refusal log leaked the bearer token")
		}
	}
}
