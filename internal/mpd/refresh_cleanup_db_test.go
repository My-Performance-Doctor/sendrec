package mpd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sendrec/sendrec/internal/integration"
	"golang.org/x/oauth2"
)

func TestNativeRefreshOutageRetryAndConcurrentReplay(t *testing.T) {
	db := identityDB(t)
	ctx := context.Background()
	h, sign := signedFixture(t)
	h.db = db
	h.key = integration.DeriveKey(strings.Repeat("k", 32))
	h.cfg.JWTSecret = strings.Repeat("j", 32)
	h.cfg.BaseURL = "https://recorder.example"
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := db.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec(`INSERT INTO organizations(id,name,slug) VALUES($1,'Synthetic','refresh')`, orgID)
	exec(`INSERT INTO mpd_managed_workspaces(organization_id,tenant_id,issuer,enabled,seat_limit) VALUES($1,$2,$3,true,2)`, orgID, tenantID, h.cfg.Issuer)
	id := &Identity{Issuer: h.cfg.Issuer, Subject: "synthetic-staff", Email: "refresh@example.test", ExpiresAt: time.Now().Add(time.Hour)}
	grant := &Grant{SchemaVersion: 1, StaffID: staffID, TenantID: tenantID, Capabilities: Capabilities{Read: true}, ValidUntil: time.Now().Add(30 * time.Second)}
	p, e := h.provision(ctx, id, grant)
	if e != nil {
		t.Fatal(e)
	}
	sid, e := h.createSession(ctx, p, id, grant, sign(nil), "initial-upstream-refresh")
	if e != nil {
		t.Fatal(e)
	}
	cookie := strings.Repeat("c", 43)
	exec(`UPDATE mpd_sessions SET refresh_hash=$2 WHERE id=$1`, sid, hash(cookie))
	var tokenUnavailable, accessUnavailable, accessDenied atomic.Bool
	var tokenCalls atomic.Int32
	var upstreamMu sync.Mutex
	nextUpstream := "initial-upstream-refresh"
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tokenUnavailable.Load() {
			w.WriteHeader(503)
			return
		}
		upstreamMu.Lock()
		defer upstreamMu.Unlock()
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if r.Form.Get("refresh_token") != nextUpstream {
			t.Error("retry used superseded Cognito refresh credential")
			w.WriteHeader(401)
			return
		}
		tokenCalls.Add(1)
		nextUpstream += "-rotated"
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": sign(nil), "refresh_token": nextUpstream, "token_type": "Bearer", "expires_in": 3600})
	}))
	defer tokenServer.Close()
	accessServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if accessUnavailable.Load() {
			w.WriteHeader(503)
			return
		}
		if accessDenied.Load() {
			w.WriteHeader(403)
			return
		}
		copy := *grant
		copy.ValidUntil = time.Now().Add(30 * time.Second)
		_ = json.NewEncoder(w).Encode(copy)
	}))
	defer accessServer.Close()
	h.cfg.AccessURL = accessServer.URL
	h.oauth = oauth2.Config{ClientID: "recorder", ClientSecret: "synthetic", Endpoint: oauth2.Endpoint{TokenURL: tokenServer.URL, AuthStyle: oauth2.AuthStyleInHeader}}
	refresh := func(cookie string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/auth/mpd/refresh", nil)
		r.Header.Set("Origin", h.cfg.BaseURL)
		r.AddCookie(&http.Cookie{Name: "mpd_refresh", Value: cookie})
		w := httptest.NewRecorder()
		h.Refresh(w, r)
		return w
	}
	tokenUnavailable.Store(true)
	if w := refresh(cookie); w.Code != 503 || len(w.Result().Cookies()) != 0 {
		t.Fatalf("Cognito outage: %d", w.Code)
	}
	tokenUnavailable.Store(false)
	accessUnavailable.Store(true)
	if w := refresh(cookie); w.Code != 503 || len(w.Result().Cookies()) != 0 {
		t.Fatalf("MPD outage: %d", w.Code)
	}
	var savedCookie, savedRefresh string
	if e = db.QueryRow(ctx, `SELECT refresh_hash,refresh_encrypted FROM mpd_sessions WHERE id=$1`, sid).Scan(&savedCookie, &savedRefresh); e != nil {
		t.Fatal(e)
	}
	decrypted, e := integration.Decrypt(h.key, savedRefresh)
	if e != nil || decrypted != nextUpstream || savedCookie != hash(cookie) {
		t.Fatal("outage lost retry credentials", e)
	}
	accessUnavailable.Store(false)
	exec(`CREATE FUNCTION fail_cookie_rotation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.refresh_hash IS DISTINCT FROM OLD.refresh_hash THEN RAISE EXCEPTION 'synthetic cookie write failure'; END IF; RETURN NEW; END $$`)
	exec(`CREATE TRIGGER fail_cookie_rotation BEFORE UPDATE ON mpd_sessions FOR EACH ROW EXECUTE FUNCTION fail_cookie_rotation()`)
	if w := refresh(cookie); w.Code != 503 || len(w.Result().Cookies()) != 0 {
		t.Fatalf("local cookie write failure: %d", w.Code)
	}
	exec(`DROP TRIGGER fail_cookie_rotation ON mpd_sessions`)
	if e = db.QueryRow(ctx, `SELECT refresh_hash,refresh_encrypted FROM mpd_sessions WHERE id=$1`, sid).Scan(&savedCookie, &savedRefresh); e != nil {
		t.Fatal(e)
	}
	decrypted, e = integration.Decrypt(h.key, savedRefresh)
	if e != nil || decrypted != nextUpstream || savedCookie != hash(cookie) {
		t.Fatal("local cookie failure lost retry credentials", e)
	}
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- refresh(cookie) }()
	}
	wg.Wait()
	close(results)
	success, denial := 0, 0
	var renewedCookie string
	for w := range results {
		switch w.Code {
		case 200:
			success++
			for _, c := range w.Result().Cookies() {
				if c.Name == "mpd_refresh" {
					renewedCookie = c.Value
				}
			}
		case 401:
			denial++
		default:
			t.Fatalf("concurrent refresh:%d %s", w.Code, w.Body.String())
		}
	}
	if success != 1 || denial != 1 || renewedCookie == "" || tokenCalls.Load() != 3 {
		t.Fatalf("rotation replay success=%d denied=%d upstreamCalls=%d", success, denial, tokenCalls.Load())
	}
	accessDenied.Store(true)
	if w := refresh(renewedCookie); w.Code != 401 {
		t.Fatalf("offboarding:%d", w.Code)
	}
	var revoked bool
	if e = db.QueryRow(ctx, `SELECT revoked FROM mpd_sessions WHERE id=$1`, sid).Scan(&revoked); e != nil || !revoked {
		t.Fatal("denied membership did not revoke session", e)
	}
}

func TestCleanupExpiresOnlyEphemeralCredentials(t *testing.T) {
	db := identityDB(t)
	ctx := context.Background()
	h, sign := signedFixture(t)
	h.db = db
	h.key = integration.DeriveKey(strings.Repeat("k", 32))
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := db.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec(`INSERT INTO organizations(id,name,slug) VALUES($1,'Synthetic','cleanup')`, orgID)
	exec(`INSERT INTO mpd_managed_workspaces(organization_id,tenant_id,issuer,enabled,seat_limit) VALUES($1,$2,$3,true,2)`, orgID, tenantID, h.cfg.Issuer)
	id := &Identity{Issuer: h.cfg.Issuer, Subject: "synthetic-staff", Email: "cleanup@example.test", ExpiresAt: time.Now().Add(time.Hour)}
	g := &Grant{SchemaVersion: 1, StaffID: staffID, TenantID: tenantID, Capabilities: Capabilities{Read: true}, ValidUntil: time.Now().Add(30 * time.Second)}
	p, e := h.provision(ctx, id, g)
	if e != nil {
		t.Fatal(e)
	}
	live, e := h.createSession(ctx, p, id, g, sign(nil), "refreshable")
	if e != nil {
		t.Fatal(e)
	}
	expired, e := h.createSession(ctx, p, id, g, sign(nil), "")
	if e != nil {
		t.Fatal(e)
	}
	exec(`UPDATE mpd_sessions SET cognito_expires_at=now()-interval '1 minute',grant_expires_at=now()-interval '1 minute' WHERE id=$1`, live)
	exec(`UPDATE mpd_sessions SET expires_at=now()-interval '1 minute' WHERE id=$1`, expired)
	for _, state := range []string{"expired1", "expired2", "live"} {
		duration := "-1 minute"
		if state == "live" {
			duration = "1 hour"
		}
		exec(`INSERT INTO mpd_login_transactions(state_hash,browser_hash,nonce,verifier_encrypted,callback,expires_at) VALUES($1,'browser','nonce','encrypted','callback',now()+$2::interval)`, state, duration)
		exec(`INSERT INTO mpd_login_handoffs(code_hash,browser_hash,session_id,expires_at) VALUES($1,'browser',$2,now()+$3::interval)`, state, live, duration)
		exec(`INSERT INTO mpd_preview_handoffs(token_hash,video_id,media_version,session_id,origin,nonce,expires_at) VALUES($1,$2,1,$3,'https://recorder.example','nonce',now()+$4::interval)`, state, videoID, live, duration)
		exec(`INSERT INTO mpd_preview_sessions(token_hash,video_id,media_version,session_id,expires_at) VALUES($1,$2,1,$3,now()+$4::interval)`, state, videoID, live, duration)
		exec(`INSERT INTO mpd_playback_sessions(video_id,media_version,viewer_class,expires_at) VALUES($1,1,'anonymous',now()+$2::interval)`, videoID, duration)
	}
	exec(`INSERT INTO mpd_playback_facts(session_id,video_id,media_version) VALUES($1,$2,1)`, tenantID, videoID)
	if e = CleanupExpired(ctx, db, 1); e != nil {
		t.Fatal(e)
	}
	for _, table := range []string{"mpd_login_transactions", "mpd_login_handoffs", "mpd_preview_handoffs", "mpd_preview_sessions", "mpd_playback_sessions"} {
		var count int
		if e = db.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); e != nil || count != 2 {
			t.Fatalf("%s batch count:%d %v", table, count, e)
		}
	}
	if e = CleanupExpired(ctx, db, 10); e != nil {
		t.Fatal(e)
	}
	for _, table := range []string{"mpd_login_transactions", "mpd_login_handoffs", "mpd_preview_handoffs", "mpd_preview_sessions", "mpd_playback_sessions", "mpd_sessions", "mpd_external_identities", "mpd_identity_audit", "mpd_playback_facts"} {
		var count int
		if e = db.QueryRow(ctx, "SELECT count(*) FROM "+table).Scan(&count); e != nil || count != 1 {
			t.Fatalf("%s retained count:%d %v", table, count, e)
		}
	}
	var retained bool
	if e = db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM mpd_sessions WHERE id=$1 AND refresh_encrypted<>'')`, live).Scan(&retained); e != nil || !retained {
		t.Fatal("expired access destroyed refreshable session", e)
	}
}
