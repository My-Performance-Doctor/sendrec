package mpd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coreos/go-oidc/v3/oidc"
)

type profileTransport func(*http.Request) (*http.Response, error)

func (f profileTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProfileFallbackForInitiateAuthToken(t *testing.T) {
	for _, tc := range []struct {
		name, subject, verified string
		allowed                 bool
	}{{"authorized", "verified-sub", "true", true}, {"subject mismatch", "different-sub", "true", false}, {"unverified email", "verified-sub", "false", false}} {
		t.Run(tc.name, func(t *testing.T) {
			var issuer string
			userinfoCalls, getUserCalls := 0, 0
			fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/.well-known/openid-configuration" {
					_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "userinfo_endpoint": issuer + "/userinfo"})
					return
				}
				userinfoCalls++
				w.WriteHeader(http.StatusUnauthorized)
			}))
			defer fixture.Close()
			issuer = fixture.URL
			p, e := oidc.NewProvider(context.Background(), issuer)
			if e != nil {
				t.Fatal(e)
			}
			client := fixture.Client()
			base := client.Transport
			client.Transport = profileTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host == strings.TrimPrefix(fixture.URL, "http://") {
					return base.RoundTrip(r)
				}
				getUserCalls++
				if r.URL.String() != "https://cognito-idp.ap-southeast-2.amazonaws.com/" || r.Method != "POST" || r.Header.Get("X-Amz-Target") != "AWSCognitoIdentityProviderService.GetUser" {
					t.Fatalf("unexpected profile destination: %s", r.URL)
				}
				var input struct{ AccessToken string }
				if json.NewDecoder(r.Body).Decode(&input) != nil || input.AccessToken != "synthetic-access-token" {
					t.Fatal("missing token in JSON body")
				}
				if r.Header.Get("Authorization") != "" {
					t.Fatal("token leaked to header")
				}
				body, _ := json.Marshal(map[string]any{"UserAttributes": []map[string]string{{"Name": "sub", "Value": tc.subject}, {"Name": "email", "Value": "synthetic@example.test"}, {"Name": "email_verified", "Value": tc.verified}, {"Name": "name", "Value": "Synthetic"}}})
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			})
			h := &Handler{provider: p, client: client, cfg: Config{Issuer: "https://cognito-idp.ap-southeast-2.amazonaws.com/ap-southeast-2_Synthetic"}}
			id := &Identity{Issuer: h.cfg.Issuer, Subject: "verified-sub"}
			e = h.populateDisplay(httptest.NewRequest("POST", "/api/auth/mpd/session", nil), "synthetic-access-token", id)
			if (e == nil) != tc.allowed || userinfoCalls != 1 || getUserCalls != 1 {
				t.Fatalf("allowed=%v err=%v userinfo=%d GetUser=%d", tc.allowed, e, userinfoCalls, getUserCalls)
			}
			if tc.allowed && id.Email != "synthetic@example.test" {
				t.Fatal("verified profile missing")
			}
		})
	}
}
func TestCognitoProfileEndpointRejectsUntrustedDestinations(t *testing.T) {
	for _, issuer := range []string{"http://cognito-idp.ap-southeast-2.amazonaws.com/ap-southeast-2_Test", "https://cognito-idp.ap-southeast-2.amazonaws.com.evil.test/ap-southeast-2_Test", "https://cognito-idp.ap-southeast-2.amazonaws.com:443/ap-southeast-2_Test", "https://user@cognito-idp.ap-southeast-2.amazonaws.com/ap-southeast-2_Test", "https://cognito-idp.ap-southeast-2.amazonaws.com/us-east-1_Test", "https://cognito-idp.ap-southeast-2.amazonaws.com/ap-southeast-2_Test?redirect=evil", "https://cognito-idp.ap-southeast-2.amazonaws.com/ap-southeast-2_Test/other"} {
		if _, e := cognitoEndpoint(issuer); e == nil {
			t.Fatalf("accepted untrusted issuer %s", issuer)
		}
	}
}

func TestCognitoProfileDoesNotFollowRedirects(t *testing.T) {
	calls := 0
	h := &Handler{cfg: Config{Issuer: "https://cognito-idp.ap-southeast-2.amazonaws.com/ap-southeast-2_Synthetic"}, client: &http.Client{Transport: profileTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if calls > 1 {
			t.Fatal("profile redirect followed")
		}
		return &http.Response{StatusCode: 307, Header: http.Header{"Location": []string{"https://unexpected.example/token"}}, Body: io.NopCloser(strings.NewReader("")), Request: r}, nil
	})}}
	err := h.cognitoProfile(context.Background(), "synthetic-access-token", &Identity{Issuer: h.cfg.Issuer, Subject: "verified-sub"})
	if err != ErrDenied || calls != 1 {
		t.Fatalf("redirect handling: %v %d", err, calls)
	}
}
func TestUserInfoSubjectMismatchDoesNotFallBack(t *testing.T) {
	var issuer string
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/.well-known/openid-configuration" {
			_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "userinfo_endpoint": issuer + "/userinfo"})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"sub": "different-sub", "email": "synthetic@example.test", "email_verified": true})
	}))
	defer fixture.Close()
	issuer = fixture.URL
	p, e := oidc.NewProvider(context.Background(), issuer)
	if e != nil {
		t.Fatal(e)
	}
	client := fixture.Client()
	base := client.Transport
	client.Transport = profileTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != strings.TrimPrefix(issuer, "http://") {
			t.Fatal("subject mismatch triggered fallback")
		}
		return base.RoundTrip(r)
	})
	h := &Handler{cfg: Config{Issuer: "https://cognito-idp.ap-southeast-2.amazonaws.com/ap-southeast-2_Synthetic"}, provider: p, client: client}
	if e = h.populateDisplay(httptest.NewRequest("POST", "/", nil), "synthetic-access-token", &Identity{Issuer: h.cfg.Issuer, Subject: "verified-sub"}); e != ErrDenied {
		t.Fatalf("subject mismatch: %v", e)
	}
}
