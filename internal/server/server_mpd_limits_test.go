package server_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v5"
)

func TestMPDAnonymousRoutesShareWatchRateLimit(t *testing.T) {
	for _, route := range []struct {
		method, path, query string
		status              int
	}{
		{"GET", "/api/watch/synthetic/renew", "SELECT file_key,share_password", 404},
		{"POST", "/api/watch/synthetic/playback-session", "SELECT share_password,share_expires_at", 404},
		{"POST", "/api/watch/synthetic/playback-start", "SELECT share_password,share_expires_at", 404},
		{"POST", "/api/mpd/preview/redeem", "", 403},
		{"POST", "/api/mpd/preview/media", "", 403},
		{"POST", "/api/mpd/preview/session", "", 403},
		{"POST", "/api/mpd/preview/playback-start", "", 403},
	} {
		t.Run(route.path, func(t *testing.T) {
			srv, mock := newServerWithDB(t)
			// Exhaust the shared bucket through existing watch traffic. This also
			// proves the new endpoint cannot bypass it by switching route names.
			for i := 0; i < 19; i++ {
				mock.ExpectQuery("SELECT v.id, v.title, v.duration, v.file_key").WithArgs("synthetic").WillReturnError(pgx.ErrNoRows)
				if w := executeRequest(srv, http.MethodGet, "/api/watch/synthetic"); w.Code != 404 {
					t.Fatalf("watch %d", w.Code)
				}
			}
			if route.query != "" {
				mock.ExpectQuery(route.query).WithArgs("synthetic").WillReturnError(pgx.ErrNoRows)
			}
			if w := executeRequest(srv, route.method, route.path); w.Code != route.status {
				t.Fatalf("allowed request %d %s", w.Code, w.Body.String())
			}
			if w := executeRequest(srv, route.method, route.path); w.Code != 429 || w.Header().Get("Retry-After") == "" {
				t.Fatalf("limited request %d %s", w.Code, w.Body.String())
			}
			// A different client remains able to reach the route.
			if route.query != "" {
				mock.ExpectQuery(route.query).WithArgs("synthetic").WillReturnError(pgx.ErrNoRows)
			}
			req := httptest.NewRequest(route.method, route.path, nil)
			req.RemoteAddr = "198.51.100.27:1234"
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, req)
			if w.Code != route.status {
				t.Fatalf("independent client %d", w.Code)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMPDPreviewRejectsOversizedRedeemBeforeDatabase(t *testing.T) {
	srv, mock := newServerWithDB(t)
	// A syntactically valid normal body reaches the handoff lookup.
	mock.ExpectQuery("UPDATE mpd_preview_handoffs").WithArgs(pgxmock.AnyArg(), "synthetic", "https://localhost:8080").WillReturnRows(pgxmock.NewRows([]string{"video_id", "media_version", "session_id"}))
	request := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/mpd/preview/redeem", strings.NewReader(body))
		req.Header.Set("Origin", "https://localhost:8080")
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, req)
		return w
	}
	if w := request(`{"handoff":"synthetic","nonce":"synthetic","parentOrigin":"https://localhost:8080"}`); w.Code != 403 || !strings.Contains(w.Body.String(), "preview expired") {
		t.Fatalf("normal body %d %s", w.Code, w.Body.String())
	}
	if w := request(`{"handoff":"` + strings.Repeat("x", 4096) + `"}`); w.Code != 400 || !strings.Contains(w.Body.String(), "invalid preview") {
		t.Fatalf("oversized body %d %s", w.Code, w.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
