package server_test

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/pashagolub/pgxmock/v5"
	"github.com/sendrec/sendrec/internal/server"
	"github.com/sendrec/sendrec/internal/video"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDisabledMPDConfigurationPreservesUnpublishedPolicy(t *testing.T) {
	for _, path := range []string{"/watch/synthetic", "/embed/synthetic", "/api/watch/synthetic", "/api/watch/synthetic/download", "/api/watch/synthetic/thumbnail", "/api/videos/synthetic/oembed", "/api/watch/synthetic/comments"} {
		t.Run(path, func(t *testing.T) {
			db, err := pgxmock.NewPool()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			db.ExpectQuery("SELECT s.published,s.deleted_at,v.share_password").WithArgs("synthetic").WillReturnRows(pgxmock.NewRows([]string{"published", "deleted_at", "share_password", "email_gate_enabled", "share_expires_at"}).AddRow(false, nil, nil, false, nil))
			srv := server.New(server.Config{DB: db, Storage: &mockStorage{}, JWTSecret: "synthetic", BaseURL: "https://sendrec.example.test"})
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, httptest.NewRequest("GET", path, nil))
			if w.Code != 404 {
				t.Fatalf("disabled configuration exposed unpublished route: %d %s", w.Code, w.Body.String())
			}
			if err := db.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestServiceCredentialCannotUseStaffOrDeleteRoutes(t *testing.T) {
	token := strings.Repeat("s", 32)
	sum := sha256.Sum256([]byte(token))
	for _, c := range []struct {
		method, path string
		want         int
	}{
		{"DELETE", "/api/integrations/mpd/videos/11111111-1111-4111-8111-111111111111", 405},
		{"POST", "/api/videos", 401},
		{"DELETE", "/api/videos/11111111-1111-4111-8111-111111111111", 401},
		{"GET", "/api/user", 401},
	} {
		t.Run(c.method+c.path, func(t *testing.T) {
			db, err := pgxmock.NewPool()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			srv := server.New(server.Config{DB: db, Storage: &mockStorage{}, JWTSecret: "synthetic", BaseURL: "https://sendrec.example.test", MPDService: video.MPDServiceConfig{Enabled: true, TokenHashes: []string{hex.EncodeToString(sum[:])}}})
			r := httptest.NewRequest(c.method, c.path, nil)
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("X-Organization-ID", "11111111-1111-4111-8111-111111111111")
			w := httptest.NewRecorder()
			srv.ServeHTTP(w, r)
			if w.Code != c.want {
				t.Fatalf("service credential route got %d want %d: %s", w.Code, c.want, w.Body.String())
			}
			if err := db.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
