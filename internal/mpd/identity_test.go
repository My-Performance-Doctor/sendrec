package mpd

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	jose "github.com/go-jose/go-jose/v4"
	"github.com/golang-jwt/jwt/v5"
	"github.com/pashagolub/pgxmock/v5"
)

const staffID = "00000000-0000-4000-8000-000000000001"
const tenantID = "00000000-0000-4000-8000-000000000002"
const videoID = "00000000-0000-4000-8000-000000000003"
const orgID = "00000000-0000-4000-8000-000000000004"

func signedFixture(t *testing.T) (*Handler, func(map[string]any) string) {
	t.Helper()
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	jwks := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "synthetic", Algorithm: "RS256", Use: "sig"}}})
	}))
	t.Cleanup(jwks.Close)
	h := &Handler{cfg: Config{Enabled: true, Issuer: "https://issuer.example", AllowedClientIDs: []string{"recorder", "future"}}, client: jwks.Client()}
	h.verifier = oidc.NewVerifier(h.cfg.Issuer, oidc.NewRemoteKeySet(context.Background(), jwks.URL), &oidc.Config{SkipClientIDCheck: true, SupportedSigningAlgs: []string{"RS256"}})
	h.idVerifier = oidc.NewVerifier(h.cfg.Issuer, oidc.NewRemoteKeySet(context.Background(), jwks.URL), &oidc.Config{ClientID: "recorder", SupportedSigningAlgs: []string{"RS256"}})
	sign := func(changes map[string]any) string {
		c := jwt.MapClaims{"iss": h.cfg.Issuer, "sub": "synthetic-staff", "exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(), "client_id": "recorder", "token_use": "access"}
		for k, v := range changes {
			c[k] = v
		}
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
		token.Header["kid"] = "synthetic"
		s, e := token.SignedString(key)
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	return h, sign
}
func TestCognitoAccessSignatureAndClaims(t *testing.T) {
	h, sign := signedFixture(t)
	for _, tc := range []struct {
		name    string
		changes map[string]any
		allowed bool
	}{
		{"access without aud", nil, true}, {"future client", map[string]any{"client_id": "future"}, true}, {"wrong client", map[string]any{"client_id": "patient"}, false}, {"ID token", map[string]any{"token_use": "id"}, false}, {"wrong issuer", map[string]any{"iss": "https://patient.example"}, false}, {"expired", map[string]any{"exp": time.Now().Add(-time.Minute).Unix()}, false}, {"no subject", map[string]any{"sub": ""}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, e := h.VerifyAccess(context.Background(), sign(tc.changes))
			if (e == nil) != tc.allowed {
				t.Fatalf("allowed=%v err=%v", tc.allowed, e)
			}
		})
	}
	raw := sign(nil)
	raw = raw[:len(raw)-8] + "abcdefgh"
	if _, e := h.VerifyAccess(context.Background(), raw); e == nil {
		t.Fatal("tampered signature accepted")
	}
}
func TestGrantBoundsAndContract(t *testing.T) {
	now := time.Now()
	g := Grant{SchemaVersion: 1, StaffID: staffID, TenantID: tenantID, Capabilities: Capabilities{Read: true}, ValidUntil: now.Add(time.Hour)}
	raw, _ := json.Marshal(g)
	got, e := decodeGrant(strings.NewReader(string(raw)), now, now.Add(time.Minute))
	if e != nil || !got.ValidUntil.Equal(now.Add(30*time.Second)) {
		t.Fatalf("cache not capped: %+v %v", got, e)
	}
	got, e = decodeGrant(strings.NewReader(string(raw)), now, now.Add(3*time.Second))
	if e != nil || !got.ValidUntil.Equal(now.Add(3*time.Second)) {
		t.Fatal("token expiry not enforced")
	}
	if _, e = decodeGrant(strings.NewReader(string(raw)+" {}"), now, now.Add(time.Minute)); e == nil {
		t.Fatal("trailing JSON accepted")
	}
	g.ValidUntil = now.Add(-time.Second)
	raw, _ = json.Marshal(g)
	if _, e = decodeGrant(strings.NewReader(string(raw)), now, now.Add(time.Minute)); e == nil {
		t.Fatal("expired grant accepted")
	}
}
func TestExactCapabilitiesAndOwnership(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body, owner, status string
		caps                                    Capabilities
		allowed                                 bool
	}{
		{"record only creates", "POST", "/api/videos", "", staffID, "", Capabilities{Record: true}, true},
		{"manage own cannot create", "POST", "/api/videos", "", staffID, "", Capabilities{ManageOwn: true}, false},
		{"read only cannot delete", "DELETE", "/api/videos/" + videoID, "", staffID, "ready", Capabilities{Read: true}, false},
		{"manage own deletes own", "DELETE", "/api/videos/" + videoID, "", staffID, "ready", Capabilities{ManageOwn: true}, true},
		{"manage own cannot delete others", "DELETE", "/api/videos/" + videoID, "", tenantID, "ready", Capabilities{ManageOwn: true}, false},
		{"workspace deletes others", "DELETE", "/api/videos/" + videoID, "", tenantID, "ready", Capabilities{ManageWorkspace: true}, true},
		{"record finalizes owned upload", "PATCH", "/api/videos/" + videoID, `{"status":"ready"}`, staffID, "uploading", Capabilities{Record: true}, true},
		{"record retries owned finalized upload", "PATCH", "/api/videos/" + videoID, `{"status":"ready"}`, staffID, "ready", Capabilities{Record: true}, true},
		{"record retries owned processing upload", "PATCH", "/api/videos/" + videoID, `{"status":"ready"}`, staffID, "processing", Capabilities{Record: true}, true},
		{"record cannot rename finished video", "PATCH", "/api/videos/" + videoID, `{"title":"rename"}`, staffID, "ready", Capabilities{Record: true}, false},
		{"preview needs read", "POST", "/api/videos/" + videoID + "/preview", "", staffID, "ready", Capabilities{ManageWorkspace: true}, false},
		{"read preview", "POST", "/api/videos/" + videoID + "/preview", "", staffID, "ready", Capabilities{Read: true}, true},
		{"workspace cannot transfer", "POST", "/api/videos/" + videoID + "/transfer", "", staffID, "ready", Capabilities{ManageWorkspace: true}, false},
		{"workspace cannot retire user", "DELETE", "/api/user", "", staffID, "", Capabilities{ManageWorkspace: true}, false},
		{"retention refused", "PATCH", "/api/user", `{"retentionDays":1}`, staffID, "", Capabilities{ManageWorkspace: true}, false},
		{"name permitted", "PATCH", "/api/user", `{"name":"Synthetic"}`, staffID, "", Capabilities{Read: true}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, e := pgxmock.NewPool()
			if e != nil {
				t.Fatal(e)
			}
			defer db.Close()
			h := &Handler{db: db}
			if strings.HasPrefix(tc.path, "/api/videos/"+videoID) {
				db.ExpectQuery("SELECT user_id").WithArgs(videoID).WillReturnRows(pgxmock.NewRows([]string{"user_id", "organization_id", "status"}).AddRow(tc.owner, orgID, tc.status))
			}
			r := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			p := &Principal{UserID: staffID, OrganizationID: orgID, Capabilities: tc.caps}
			if got := h.authorize(r, p); got != tc.allowed {
				t.Fatalf("got=%v want=%v", got, tc.allowed)
			}
			if e := db.ExpectationsWereMet(); e != nil {
				t.Fatal(e)
			}
		})
	}
}
func TestOriginAllowlist(t *testing.T) {
	h := &Handler{cfg: Config{BaseURL: "https://recorder.example", AllowedOrigins: []string{"https://staff.example"}}}
	for _, tc := range []struct {
		origin          string
		native, allowed bool
	}{{"https://recorder.example", true, true}, {"https://staff.example", false, true}, {"https://staff.example", true, false}, {"https://staff.example.evil.test", false, false}, {"null", false, false}, {"", false, false}} {
		r := httptest.NewRequest("POST", "/", nil)
		r.Header.Set("Origin", tc.origin)
		w := httptest.NewRecorder()
		if h.allowedOrigin(w, r, tc.native) != tc.allowed {
			t.Fatal(tc)
		}
	}
}

func TestFutureClientCORS(t *testing.T) {
	h := &Handler{cfg: Config{Enabled: true, AllowedOrigins: []string{"https://staff.example"}}}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(405) })
	for _, origin := range []string{"https://staff.example", "https://evil.example"} {
		r := httptest.NewRequest("OPTIONS", "/api/videos", nil)
		r.Header.Set("Origin", origin)
		w := httptest.NewRecorder()
		h.CORS(next).ServeHTTP(w, r)
		if origin == "https://staff.example" {
			if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != origin || !strings.Contains(w.Header().Get("Access-Control-Allow-Headers"), "Authorization") {
				t.Fatal("approved client preflight failed")
			}
		} else if w.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Fatal("unapproved origin allowed")
		}
		if w.Header().Get("Access-Control-Allow-Credentials") != "" {
			t.Fatal("cross-site cookies allowed")
		}
	}
}
