package readiness

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadinessRequiresEveryDependencyAndRedactsFailure(t *testing.T) {
	ok := func(context.Context) error { return nil }
	secretError := func(context.Context) error { return errors.New("postgres://private-password@host/customer-id") }
	for _, tc := range []struct {
		name    string
		checker Checker
		want    int
	}{
		{"healthy", Checker{ok, ok, ok}, 200},
		{"database outage", Checker{secretError, ok, ok}, 503},
		{"storage denied", Checker{ok, secretError, ok}, 503},
		{"worker stopped", Checker{ok, ok, secretError}, 503},
		{"missing probe", Checker{ok, ok, nil}, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := httptest.NewRecorder()
			tc.checker.ServeHTTP(response, httptest.NewRequest("GET", "/api/ready", nil))
			if response.Code != tc.want {
				t.Fatalf("status %d, want %d", response.Code, tc.want)
			}
			if strings.Contains(response.Body.String(), "private-password") || strings.Contains(response.Body.String(), "customer-id") {
				t.Fatal("private diagnostic leaked")
			}
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("readiness cached")
			}
		})
	}
}
