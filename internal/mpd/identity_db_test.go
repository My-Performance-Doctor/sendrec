package mpd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sendrec/sendrec/internal/auth"
	"github.com/sendrec/sendrec/internal/database"
	"github.com/sendrec/sendrec/internal/integration"
	"golang.org/x/oauth2"
	"sync/atomic"
)

func identityDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL required")
	}
	ctx := context.Background()
	admin, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	name := "mpd_identity_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if _, e = admin.Exec(ctx, "CREATE DATABASE "+name); e != nil {
		t.Fatal(e)
	}
	u, e := url.Parse(dsn)
	if e != nil {
		t.Fatal(e)
	}
	u.Path = "/" + name
	db, e := database.Connect(ctx, u.String())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		db.Close()
		_, e := admin.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
		if e != nil {
			t.Error(e)
		}
		admin.Close()
	})
	if e = db.Migrate(u.String()); e != nil {
		t.Fatal(e)
	}
	return db.Pool
}
func TestIdentityDatabaseBoundaries(t *testing.T) {
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
	exec(`INSERT INTO organizations(id,name,slug) VALUES($1,'Synthetic','mpd-synthetic')`, orgID)
	exec(`INSERT INTO mpd_managed_workspaces(organization_id,tenant_id,issuer,enabled,seat_limit) VALUES($1,$2,$3,true,2)`, orgID, tenantID, h.cfg.Issuer)
	id := &Identity{Issuer: h.cfg.Issuer, Subject: "synthetic-staff", Email: "synthetic@example.test", Name: "Synthetic", ExpiresAt: time.Now().Add(time.Hour)}
	g := &Grant{SchemaVersion: 1, StaffID: staffID, TenantID: tenantID, Capabilities: Capabilities{Read: true, Record: true, ManageOwn: true}, ValidUntil: time.Now().Add(30 * time.Second)}
	var wg sync.WaitGroup
	ps := make(chan *Principal, 2)
	es := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); p, e := h.provision(ctx, id, g); ps <- p; es <- e }()
	}
	wg.Wait()
	close(ps)
	close(es)
	for e := range es {
		if e != nil {
			t.Fatal(e)
		}
	}
	var p *Principal
	for candidate := range ps {
		if p != nil && candidate.UserID != p.UserID {
			t.Fatal("concurrent provision duplicated user")
		}
		p = candidate
	}
	var count int
	if e := db.QueryRow(ctx, `SELECT count(*) FROM mpd_external_identities`).Scan(&count); e != nil || count != 1 {
		t.Fatalf("identity count %d %v", count, e)
	}
	collision := *id
	collision.Subject = "other-staff"
	otherGrant := *g
	otherGrant.StaffID = videoID
	if _, e := h.provision(ctx, &collision, &otherGrant); e == nil {
		t.Fatal("email linked implicitly")
	}
	wrongIssuer := *id
	wrongIssuer.Issuer = "https://patient.example"
	if _, e := h.provision(ctx, &wrongIssuer, g); e == nil {
		t.Fatal("issuer isolation failed")
	}
	for _, q := range []string{
		`UPDATE users SET retention_days=1 WHERE id=$1`,
		`UPDATE users SET password='new' WHERE id=$1`,
		`DELETE FROM users WHERE id=$1`,
		`INSERT INTO refresh_tokens(token_id,user_id,expires_at,revoked) VALUES('legacy',$1,now()+interval '1 day',false)`,
		`INSERT INTO external_identities(user_id,provider,external_id,email) VALUES($1,'legacy','same-sub','synthetic@example.test')`,
		`DELETE FROM organization_members WHERE user_id=$1`,
	} {
		if _, e := db.Exec(ctx, q, p.UserID); e == nil {
			t.Fatalf("boundary bypass: %s", q)
		}
	}
	for _, q := range []string{`UPDATE organizations SET retention_days=1 WHERE id=$1`, `DELETE FROM organizations WHERE id=$1`, `INSERT INTO organization_scim_tokens(organization_id,token_hash) VALUES($1,'old-token')`} {
		if _, e := db.Exec(ctx, q, orgID); e == nil {
			t.Fatalf("workspace bypass: %s", q)
		}
	}
	raw := sign(nil)
	sid, e := h.createSession(ctx, p, id, g, raw, "")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = h.ValidateSession(ctx, sid); e != nil {
		t.Fatal(e)
	}
	// The local session can use a cached grant only through its original deadline.
	unavailable := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer unavailable.Close()
	h.cfg.AccessURL = unavailable.URL
	h.client = unavailable.Client()
	exec(`UPDATE mpd_sessions SET grant_expires_at=now()-interval '1 second' WHERE id=$1`, sid)
	if _, e = h.ValidateSession(ctx, sid); e != ErrUnavailable {
		t.Fatalf("expired grant outage did not fail closed: %v", e)
	}
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }))
	defer denied.Close()
	h.cfg.AccessURL = denied.URL
	if _, e = h.ValidateSession(ctx, sid); e != ErrDenied {
		t.Fatalf("offboarded staff accepted: %v", e)
	}
	exec(`UPDATE mpd_sessions SET grant_expires_at=now()+interval '10 seconds',revoked=true WHERE id=$1`, sid)
	if _, e = h.ValidateSession(ctx, sid); e == nil {
		t.Fatal("revoked session accepted")
	}
	// Even integration rollback does not permit old JWT/API-key personal access.
	h.cfg.Enabled = false
	r := httptest.NewRequest("GET", "/api/videos", nil)
	r = r.WithContext(auth.ContextWithUserID(ctx, p.UserID))
	w := httptest.NewRecorder()
	h.Guard(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("legacy token bypassed boundary") })).ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatalf("legacy guard: %d", w.Code)
	}
}

func TestNativeCallbackAndHandoffReplay(t *testing.T) {
	db := identityDB(t)
	h, sign := signedFixture(t)
	h.db = db
	h.key = integration.DeriveKey(strings.Repeat("k", 32))
	h.cfg.JWTSecret = strings.Repeat("j", 32)
	h.cfg.BaseURL = "https://recorder.example"
	ctx := context.Background()
	for _, q := range []string{`INSERT INTO organizations(id,name,slug) VALUES('` + orgID + `','Synthetic','native')`, `INSERT INTO mpd_managed_workspaces(organization_id,tenant_id,issuer,enabled,seat_limit) VALUES('` + orgID + `','` + tenantID + `','https://issuer.example',true,2)`} {
		if _, e := db.Exec(ctx, q); e != nil {
			t.Fatal(e)
		}
	}
	access := sign(nil)
	nonce := "synthetic-nonce"
	idToken := sign(map[string]any{"token_use": "id", "aud": "recorder", "nonce": nonce, "email": "native@example.test", "email_verified": true})
	tokenServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if r.Form.Get("code_verifier") != "synthetic-verifier" || r.Form.Get("redirect_uri") != "https://recorder.example/api/auth/mpd/callback" {
			t.Error("PKCE/callback missing")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": access, "refresh_token": "synthetic-refresh", "token_type": "Bearer", "expires_in": 3600, "id_token": idToken})
	}))
	defer tokenServer.Close()
	var denyGrant atomic.Bool
	grantServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if denyGrant.Load() {
			w.WriteHeader(403)
			return
		}
		_ = json.NewEncoder(w).Encode(Grant{SchemaVersion: 1, StaffID: staffID, TenantID: tenantID, Capabilities: Capabilities{Read: true}, ValidUntil: time.Now().Add(30 * time.Second)})
	}))
	defer grantServer.Close()
	h.cfg.AccessURL = grantServer.URL
	h.oauth = oauth2.Config{ClientID: "recorder", ClientSecret: "synthetic", RedirectURL: "https://recorder.example/api/auth/mpd/callback", Endpoint: oauth2.Endpoint{TokenURL: tokenServer.URL}}
	verifier, e := integration.Encrypt(h.key, "synthetic-verifier")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.Exec(ctx, `INSERT INTO mpd_login_transactions(state_hash,browser_hash,nonce,verifier_encrypted,callback,expires_at) VALUES($1,$2,$3,$4,$5,now()+interval '5 minutes')`, hash("state"), hash("browser"), nonce, verifier, h.oauth.RedirectURL); e != nil {
		t.Fatal(e)
	}
	callback := func(browser string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("GET", "/api/auth/mpd/callback?state=state&code=synthetic", nil)
		r.AddCookie(&http.Cookie{Name: "mpd_login", Value: browser})
		w := httptest.NewRecorder()
		h.Callback(w, r)
		return w
	}
	if w := callback("wrong"); w.Code != 401 {
		t.Fatalf("wrong browser accepted: %d", w.Code)
	}
	w := callback("browser")
	if w.Code != 302 {
		t.Fatalf("callback: %d %s", w.Code, w.Body.String())
	}
	location := w.Header().Get("Location")
	if strings.Contains(location, access) || strings.Contains(location, idToken) || !strings.Contains(location, "#mpd_code=") {
		t.Fatal("unsafe callback redirect")
	}
	if callback("browser").Code != 401 {
		t.Fatal("callback replay accepted")
	}
	code := strings.Split(location, "#mpd_code=")[1]
	redeem := func() *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/auth/mpd/handoff", strings.NewReader(`{"code":"`+code+`"}`))
		r.Header.Set("Origin", h.cfg.BaseURL)
		r.AddCookie(&http.Cookie{Name: "mpd_login", Value: "browser"})
		w := httptest.NewRecorder()
		h.Handoff(w, r)
		return w
	}
	w = redeem()
	if w.Code != 200 {
		t.Fatalf("handoff %d %s", w.Code, w.Body.String())
	}
	var nativeResponse struct {
		AccessToken string `json:"accessToken"`
	}
	if json.Unmarshal(w.Body.Bytes(), &nativeResponse) != nil {
		t.Fatal("native response invalid")
	}
	nativeClaims, e := auth.ValidateToken(h.cfg.JWTSecret, nativeResponse.AccessToken)
	if e != nil {
		t.Fatal(e)
	}
	var classification string
	for _, c := range w.Result().Cookies() {
		if c.Name == "mpd_classification" {
			classification = c.Value
			if !c.HttpOnly || !c.Secure || c.Path != "/" || c.SameSite != http.SameSiteLaxMode {
				t.Fatal("classification cookie unsafe")
			}
		}
	}
	if _, e = h.ValidateClassification(ctx, classification); e != nil {
		t.Fatal(e)
	}
	if redeem().Code != 401 {
		t.Fatal("handoff replay accepted")
	}
	if _, e = db.Exec(ctx, `UPDATE mpd_sessions SET classification_expires_at=now()-interval '1 second'`); e != nil {
		t.Fatal(e)
	}
	if _, e = h.ValidateClassification(ctx, classification); e == nil {
		t.Fatal("expired classification accepted")
	}
	// Drive the future HTTP exchange with another approved client, after native
	// sign-in has established the local account. Profile email is display only.
	var profileIssuer string
	profile := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/openid-configuration" {
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": profileIssuer, "authorization_endpoint": profileIssuer + "/authorize", "token_endpoint": profileIssuer + "/token", "jwks_uri": profileIssuer + "/keys", "userinfo_endpoint": profileIssuer + "/userinfo"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"sub": "synthetic-staff", "email": "native@example.test", "email_verified": true})
	}))
	defer profile.Close()
	profileIssuer = profile.URL
	h.provider, e = oidc.NewProvider(ctx, profileIssuer)
	if e != nil {
		t.Fatal(e)
	}
	h.cfg.AllowedOrigins = []string{"https://staff.example"}
	exchange := func(origin, raw string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/auth/mpd/session", nil)
		r.Header.Set("Origin", origin)
		r.Header.Set("Authorization", "Bearer "+raw)
		w := httptest.NewRecorder()
		h.Exchange(w, r)
		return w
	}
	futureToken := sign(map[string]any{"client_id": "future"})
	w = exchange("https://staff.example", futureToken)
	if w.Code != 200 {
		t.Fatalf("future exchange: %d %s", w.Code, w.Body.String())
	}
	var futureResponse struct {
		AccessToken string `json:"accessToken"`
	}
	if json.Unmarshal(w.Body.Bytes(), &futureResponse) != nil {
		t.Fatal("future response invalid")
	}
	futureClaims, e := auth.ValidateToken(h.cfg.JWTSecret, futureResponse.AccessToken)
	if e != nil {
		t.Fatal(e)
	}
	if futureClaims.UserID != nativeClaims.UserID || futureClaims.ManagedSessionID == nativeClaims.ManagedSessionID {
		t.Fatal("entry points failed same-account distinct-session binding")
	}
	if len(w.Result().Cookies()) != 0 {
		t.Fatal("future exchange issued cookies")
	}

	// Run the real JWT authentication plus MPD guard chain for current sessions.
	localAuth := auth.NewHandler(db, h.cfg.JWTSecret, true)
	localAuth.SetManagedSessionHooks(h.Guard, h.Refresh, h.Logout)
	guarded := localAuth.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if auth.OrgIDFromContext(r.Context()) != orgID {
			t.Error("managed request entered personal context")
		}
		w.WriteHeader(204)
	}))
	for _, tc := range []struct {
		method, org string
		want        int
	}{{"GET", "", 204}, {"GET", orgID, 204}, {"GET", tenantID, 403}, {"POST", "", 403}} {
		r := httptest.NewRequest(tc.method, "/api/videos", strings.NewReader(`{}`))
		r.Header.Set("Authorization", "Bearer "+futureResponse.AccessToken)
		r.Header.Set("X-Organization-Id", tc.org)
		w := httptest.NewRecorder()
		guarded.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("managed session/header gate:%d want%d", w.Code, tc.want)
		}
	}
	for _, tc := range []struct {
		origin, token string
		want          int
	}{
		{"https://unapproved.example", futureToken, 403},
		{"https://staff.example", sign(map[string]any{"client_id": "unapproved"}), 401},
		{"https://staff.example", sign(map[string]any{"token_use": "id", "aud": "future"}), 401},
	} {
		if got := exchange(tc.origin, tc.token); got.Code != tc.want {
			t.Fatalf("exchange rejection got%d want%d", got.Code, tc.want)
		}
	}

	for _, tc := range []struct {
		name, callback, nonce string
		denied                bool
	}{
		{"wrong callback", h.oauth.RedirectURL + "-wrong", nonce, false},
		{"wrong nonce", h.oauth.RedirectURL, "wrong-nonce", false},
		{"failed membership", h.oauth.RedirectURL, nonce, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			denyGrant.Store(tc.denied)
			defer denyGrant.Store(false)
			if _, e = db.Exec(ctx, `INSERT INTO mpd_login_transactions(state_hash,browser_hash,nonce,verifier_encrypted,callback,expires_at) VALUES($1,$2,$3,$4,$5,now()+interval '5 minutes')`, hash("state"), hash("browser"), tc.nonce, verifier, tc.callback); e != nil {
				t.Fatal(e)
			}
			var before, after int
			if e = db.QueryRow(ctx, `SELECT count(*) FROM mpd_sessions`).Scan(&before); e != nil {
				t.Fatal(e)
			}
			rejected := callback("browser")
			if rejected.Code != 401 || rejected.Header().Get("Location") != "" || len(rejected.Result().Cookies()) != 0 {
				t.Fatalf("negative callback issued credentials: %d", rejected.Code)
			}
			if e = db.QueryRow(ctx, `SELECT count(*) FROM mpd_sessions`).Scan(&after); e != nil || after != before {
				t.Fatal("failed callback created a session", e)
			}
		})
	}

}
