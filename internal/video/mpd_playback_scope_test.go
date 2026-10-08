package video

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sendrec/sendrec/internal/mpd"
	"github.com/sendrec/sendrec/internal/mpdevents"
)

func TestMPDPlaybackClassificationMatchesPersistedScope(t *testing.T) {
	db, h, a, id, share := seedManagedAdapter(t)
	mustExec(t, db, `UPDATE mpd_video_state SET published=true WHERE video_id=$1`, id)
	for _, tc := range []struct {
		name      string
		principal *mpd.Principal
		class     string
	}{
		{"anonymous", nil, "anonymous"},
		{"same workspace and tenant", &mpd.Principal{OrganizationID: a.cfg.WorkspaceID, TenantID: a.cfg.TenantID, Capabilities: mpd.Capabilities{Read: true}}, "staff_preview"},
		{"another workspace", &mpd.Principal{OrganizationID: "44444444-4444-4444-8444-444444444444", TenantID: a.cfg.TenantID, Capabilities: mpd.Capabilities{Read: true}}, "anonymous"},
		{"another tenant", &mpd.Principal{OrganizationID: a.cfg.WorkspaceID, TenantID: "44444444-4444-4444-8444-444444444444", Capabilities: mpd.Capabilities{Read: true}}, "anonymous"},
		{"no read grant", &mpd.Principal{OrganizationID: a.cfg.WorkspaceID, TenantID: a.cfg.TenantID}, "anonymous"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := withURLParam(httptest.NewRequest("POST", "/api/watch/"+share+"/playback-session", nil), "shareToken", share)
			w := httptest.NewRecorder()
			h.MPDPublicAccess(h.MPDPlaybackAccess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { h.MPDPlaybackSession(w, r, tc.principal) }))).ServeHTTP(w, req)
			if w.Code != 201 {
				t.Fatalf("session %d %s", w.Code, w.Body.String())
			}
			var session mpdevents.Session
			if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
				t.Fatal(err)
			}
			var class string
			if err := db.QueryRow(context.Background(), `SELECT viewer_class FROM mpd_playback_sessions WHERE session_id=$1`, session.ID).Scan(&class); err != nil || class != tc.class {
				t.Fatalf("class %s want %s: %v", class, tc.class, err)
			}
		})
	}
}

func TestMPDRenewalRetainsPublicationPasswordEmailAndExpiryPolicy(t *testing.T) {
	db, h, _, id, share := seedManagedAdapter(t)
	counted := &countedMediaDB{DBTX: db}
	h.db = counted
	handler := h.MPDPublicAccess(http.HandlerFunc(h.RenewWatchMedia))
	check := func(want int, cookies ...*http.Cookie) {
		t.Helper()
		req := withURLParam(httptest.NewRequest("GET", "/api/watch/"+share+"/renew", nil), "shareToken", share)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("renew %d want %d: %s", w.Code, want, w.Body.String())
		}
	}
	check(404)
	passwordHash, err := hashSharePassword("synthetic protected")
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `UPDATE videos SET share_password=$2,email_gate_enabled=true WHERE id=$1`, id, passwordHash)
	mustExec(t, db, `UPDATE mpd_video_state SET published=true WHERE video_id=$1`, id)
	password := &http.Cookie{Name: watchCookieName(share), Value: signWatchCookie(h.hmacSecret, share, passwordHash)}
	email := &http.Cookie{Name: emailGateCookieName(share), Value: signEmailGateCookie(h.hmacSecret, share, "synthetic@example.test")}
	check(403)
	check(403, password)
	counted.rows = 0
	check(200, password, email)
	if counted.rows != 2 {
		t.Fatalf("renewal queries %d want publication + media", counted.rows)
	}
	mustExec(t, db, `UPDATE videos SET share_expires_at=now()-interval '1 second' WHERE id=$1`, id)
	check(404, password, email)
	mustExec(t, db, `UPDATE videos SET share_expires_at=NULL,status='processing' WHERE id=$1`, id)
	check(404, password, email)
}
