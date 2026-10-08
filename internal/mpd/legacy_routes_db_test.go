package mpd_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sendrec/sendrec/internal/auth"
	"github.com/sendrec/sendrec/internal/database"
	"github.com/sendrec/sendrec/internal/server"
	"golang.org/x/crypto/bcrypt"
)

func isolatedRoutesDB(t *testing.T) *pgxpool.Pool {
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
	name := "mpd_routes_" + strconv.FormatInt(time.Now().UnixNano(), 36)
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
		if _, e := admin.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)"); e != nil {
			t.Error(e)
		}
		admin.Close()
	})
	if e = db.Migrate(u.String()); e != nil {
		t.Fatal(e)
	}
	return db.Pool
}

func TestActualRoutesRejectLegacyCredentialsAfterManagedLink(t *testing.T) {
	db := isolatedRoutesDB(t)
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, e := db.Exec(ctx, q, args...); e != nil {
			t.Fatal(e)
		}
	}
	id := func(q string, args ...any) string {
		t.Helper()
		var v string
		if e := db.QueryRow(ctx, q, args...).Scan(&v); e != nil {
			t.Fatal(e)
		}
		return v
	}
	password := "synthetic-password"
	hash, e := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	if e != nil {
		t.Fatal(e)
	}
	user := id(`INSERT INTO users(email,password,name,email_verified) VALUES('legacy@example.test',$1,'Synthetic',true) RETURNING id`, string(hash))
	outsider := id(`INSERT INTO users(email,password,name,email_verified) VALUES('outsider@example.test',$1,'Synthetic',true) RETURNING id`, string(hash))
	org := id(`INSERT INTO organizations(name,slug,subscription_plan) VALUES('Synthetic','legacy-guards','business') RETURNING id`)
	scimToken := "synthetic-scim-token"
	exec(`INSERT INTO organization_scim_tokens(organization_id,token_hash) VALUES($1,$2)`, org, auth.HashAPIKey(scimToken))
	srv := server.New(server.Config{DB: db, JWTSecret: strings.Repeat("s", 32), BaseURL: "https://recorder.example"})
	request := func(method, path, body, bearer, orgHeader string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			r.Header.Set("Authorization", "Bearer "+bearer)
		}
		if orgHeader != "" {
			r.Header.Set("X-Organization-Id", orgHeader)
		}
		for _, c := range cookies {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		srv.ServeHTTP(w, r)
		return w
	}
	loginBody := `{"email":"legacy@example.test","password":"synthetic-password"}`
	login := request("POST", "/api/auth/login", loginBody, "", "")
	if login.Code != 200 {
		t.Fatalf("local login positive control: %d %s", login.Code, login.Body.String())
	}
	var tokens struct {
		AccessToken string `json:"accessToken"`
	}
	if json.Unmarshal(login.Body.Bytes(), &tokens) != nil {
		t.Fatal("invalid login JSON")
	}
	var refresh *http.Cookie
	for _, c := range login.Result().Cookies() {
		if c.Name == "refresh_token" {
			refresh = c
		}
	}
	if refresh == nil {
		t.Fatal("missing local refresh positive control")
	}
	apiKey := "sr_" + strings.Repeat("a", 64)
	exec(`INSERT INTO api_keys(user_id,key_hash,name) VALUES($1,$2,'synthetic')`, user, auth.HashAPIKey(apiKey))
	for _, token := range []string{tokens.AccessToken, apiKey} {
		w := request("GET", "/api/videos/", "", token, "")
		if w.Code != 200 {
			t.Fatalf("legacy listing positive control:%d %s", w.Code, w.Body.String())
		}
	}
	// Old SCIM credentials are real and work before management takes ownership.
	scimPath := "/api/organizations/" + org + "/scim/v2/Users"
	if w := request("GET", "/api/organizations/"+org+"/scim/v2/ServiceProviderConfig", "", scimToken, ""); w.Code != 200 {
		t.Fatalf("SCIM positive control:%d %s", w.Code, w.Body.String())
	}
	tenant := "00000000-0000-4000-8000-000000000022"
	exec(`INSERT INTO mpd_managed_workspaces(organization_id,tenant_id,issuer,enabled,seat_limit) VALUES($1,$2,'https://issuer.example',true,3)`, org, tenant)
	exec(`INSERT INTO mpd_external_identities(issuer,subject,user_id,organization_id,staff_id,tenant_id) VALUES('https://issuer.example','legacy-linked',$1,$2,'00000000-0000-4000-8000-000000000023',$3)`, user, org, tenant)
	video := id(`INSERT INTO videos(user_id,organization_id,title,file_key,share_token,status) VALUES($1,$2,'Synthetic','synthetic.webm','legacy-guard-media','ready') RETURNING id`, user, org)
	for _, token := range []string{tokens.AccessToken, apiKey} {
		for _, header := range []string{"", org, "00000000-0000-4000-8000-000000000099"} {
			w := request("GET", "/api/videos/", "", token, header)
			if w.Code != 401 {
				t.Fatalf("managed legacy credential bypass:%d", w.Code)
			}
		}
	}
	outsiderJWT, e := auth.GenerateAccessToken(strings.Repeat("s", 32), outsider)
	if e != nil {
		t.Fatal(e)
	}
	if w := request("GET", "/api/videos/", "", outsiderJWT, org); w.Code != 401 {
		t.Fatalf("header escalation:%d", w.Code)
	}
	for _, tc := range []struct{ method, path, body string }{
		{"DELETE", "/api/user", ""}, {"PATCH", "/api/user", `{"retentionDays":1}`}, {"DELETE", "/api/organizations/" + org, ""}, {"PUT", "/api/organizations/" + org + "/sso", `{}`}, {"POST", "/api/organizations/" + org + "/scim-token", `{}`}, {"POST", "/api/videos/" + video + "/transfer", `{"organizationId":null}`},
	} {
		w := request(tc.method, tc.path, tc.body, tokens.AccessToken, "")
		if w.Code != 401 {
			t.Fatalf("legacy mutation bypass %s:%d", tc.path, w.Code)
		}
	}
	for _, method := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
		path := scimPath
		if method != "GET" && method != "POST" {
			path += "/" + user
		}
		w := request(method, path, `{}`, scimToken, "")
		if w.Code != 403 {
			t.Fatalf("old SCIM %s accepted:%d", method, w.Code)
		}
	}
	for _, w := range []*httptest.ResponseRecorder{request("POST", "/api/auth/login", loginBody, "", ""), request("POST", "/api/auth/refresh", "", "", "", refresh)} {
		if w.Code < 400 || strings.Contains(w.Body.String(), "accessToken") {
			t.Fatalf("local sign-in issued managed tokens:%d %s", w.Code, w.Body.String())
		}
	}

	// A token from a different unmanaged workspace must not target a managed user.
	otherOrg := id(`INSERT INTO organizations(name,slug,subscription_plan) VALUES('Other','other-scim','business') RETURNING id`)
	exec(`INSERT INTO organization_scim_tokens(organization_id,token_hash) VALUES($1,$2)`, otherOrg, auth.HashAPIKey("other-scim"))
	if w := request("GET", "/api/organizations/"+otherOrg+"/scim/v2/ServiceProviderConfig", "", "other-scim", ""); w.Code != 200 {
		t.Fatal("other SCIM positive control", w.Code)
	}
	for _, method := range []string{"PUT", "PATCH", "DELETE"} {
		w := request(method, "/api/organizations/"+otherOrg+"/scim/v2/Users/"+user, `{"schemas":["urn:ietf:params:scim:api:messages:2.0:PatchOp"],"Operations":[{"op":"replace","path":"displayName","value":"changed"}]}`, "other-scim", "")
		if w.Code != 403 {
			t.Fatalf("cross-workspace SCIM %s reached managed user:%d", method, w.Code)
		}
	}
	// Alternate SSO uses this same token issuance function; persisted identity
	// guards also prevent it issuing a usable local session after linking.
	if _, _, e := auth.IssueTokens(ctx, db, strings.Repeat("s", 32), user); e == nil {
		t.Fatal("alternate SSO issuance bypassed managed identity")
	}
	var exists bool
	if e := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM videos WHERE id=$1 AND status='ready' AND organization_id=$2)`, video, org).Scan(&exists); e != nil || !exists {
		t.Fatal("denied lifecycle operations changed media", e)
	}
}
