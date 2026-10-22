package video

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

func verifySharePassword(h *Handler, shareToken, password string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(verifyPasswordRequest{Password: password})
	r := chi.NewRouter()
	r.Post("/api/watch/{shareToken}/verify", h.VerifyWatchPassword)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/watch/"+shareToken+"/verify", bytes.NewReader(body)))
	return rec
}

func TestVerifyWatchPassword_LocksVideoAfterRepeatedFailures(t *testing.T) {
	pool := accountDB(t)
	name := uniqueName(t)
	owner := mustID(t, pool, `INSERT INTO users (email, password, name) VALUES ($1, 'x', 'Owner') RETURNING id`, name+"-owner@example.com")
	video := seedVideo(t, pool, owner, nil, name+"-v")
	hash, err := hashSharePassword("right synthetic")
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, pool, `UPDATE videos SET share_password = $1 WHERE id = $2`, hash, video)
	token := name + "-v-tok"
	h := NewHandler(pool, &mockStorage{}, "https://example.com", 0, 0, 0, 0, "secret", false)

	for i := 1; i <= sharePasswordMaxFailures; i++ {
		if rec := verifySharePassword(h, token, "wrong synthetic"); rec.Code != http.StatusForbidden {
			t.Fatalf("wrong password %d: want 403, got %d: %s", i, rec.Code, rec.Body.String())
		}
	}

	rec := verifySharePassword(h, token, "right synthetic")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("locked video, right password: want 429, got %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Error("429 without Retry-After")
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Error("a locked video set a watch cookie")
	}

	// Once the window has passed, the password is checked again and a wrong
	// one starts a new window.
	mustExec(t, pool, `UPDATE share_password_attempts SET window_start = now() - interval '16 minutes' WHERE video_id = $1`, video)
	if rec := verifySharePassword(h, token, "wrong synthetic"); rec.Code != http.StatusForbidden {
		t.Fatalf("after the window, wrong password: want 403, got %d", rec.Code)
	}
	var failures int
	if err := pool.QueryRow(context.Background(), `SELECT failures FROM share_password_attempts WHERE video_id = $1`, video).Scan(&failures); err != nil {
		t.Fatal(err)
	}
	if failures != 1 {
		t.Errorf("new window failures = %d, want 1", failures)
	}
	if rec := verifySharePassword(h, token, "right synthetic"); rec.Code != http.StatusOK {
		t.Fatalf("after the window, right password: want 200, got %d: %s", rec.Code, rec.Body.String())
	}
}
