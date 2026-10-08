package auth

import (
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestLocalSessionCookieReplacesManagedBrowserState(t *testing.T) {
	jar, e := cookiejar.New(nil)
	if e != nil {
		t.Fatal(e)
	}
	origin, _ := url.Parse("https://recorder.example/api/auth/login")
	jar.SetCookies(origin, []*http.Cookie{
		{Name: "mpd_refresh", Value: "stale", Path: "/api/auth", Secure: true},
		{Name: "mpd_classification", Value: "stale", Path: "/", Secure: true},
		{Name: "mpd_login", Value: "stale", Path: "/api/auth/mpd", Secure: true},
	})
	response := httptest.NewRecorder()
	SetRefreshTokenCookie(response, "new-local-refresh", true)
	jar.SetCookies(origin, response.Result().Cookies())
	for _, path := range []string{"/api/auth/refresh", "/api/auth/mpd/login", "/watch/synthetic"} {
		u, _ := url.Parse("https://recorder.example" + path)
		found := false
		for _, cookie := range jar.Cookies(u) {
			if cookie.Name == "mpd_refresh" || cookie.Name == "mpd_classification" || cookie.Name == "mpd_login" {
				t.Fatalf("stale managed cookie survived local login on%s", path)
			}
			if cookie.Name == "refresh_token" && cookie.Value == "new-local-refresh" {
				found = true
			}
		}
		if !found {
			t.Fatalf("new local session absent on%s", path)
		}
	}
}
