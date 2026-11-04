package video

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/pashagolub/pgxmock/v5"
)

func managedWatchRequest(shareToken string) *http.Request {
	req := watchPageRequest(shareToken)
	return req.WithContext(context.WithValue(req.Context(), mpdManagedTokensKey{}, map[string]bool{shareToken: true}))
}

func expectManagedWatchRow(mock pgxmock.PgxPoolIface, shareToken, title string, password *string, expires *time.Time) {
	mock.ExpectQuery(`SELECT v.id, v.title, v.file_key`).
		WithArgs(shareToken).
		WillReturnRows(pgxmock.NewRows(watchPageColumns).AddRow(
			"vid-1", title, "recordings/u1/abc.webm", "Alice", time.Date(2026, 2, 5, 14, 0, 0, 0, time.UTC), expires,
			(*string)(nil), password, "disabled",
			(*string)(nil), (*string)(nil), "none",
			"owner-user-id", "owner@example.com", (*string)(nil), "video/webm",
			(*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil),
			(*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil),
			(*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil),
			true, (*string)(nil), (*string)(nil),
			false,
			(*string)(nil), (*string)(nil), "none",
			0,
			"free",
			"ready",
			(*string)(nil),
		))
}

func useMPDPatientPages(t *testing.T) {
	t.Helper()
	SetMPDPatientPages(true)
	t.Cleanup(func() { SetMPDPatientPages(false) })
}

func assertMPDUnavailablePage(t *testing.T, rec *httptest.ResponseRecorder, status int) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("expected %d, got %d", status, rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("expected a page, got %s", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "This video isn&#39;t available") && !strings.Contains(body, "This video isn't available") {
		t.Errorf("expected the unavailable message, got %s", body)
	}
	if strings.Contains(body, "sendrec.eu") {
		t.Error("a patient should not be sent to SendRec's site")
	}
}

func TestWatchPage_Managed_GateAsksForVideoCodeWithoutTheTitle(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	password, _ := hashSharePassword("K7QM-2XPA-9RTF-HD4W")
	shareToken := "managedgate1"
	expectManagedWatchRow(mock, shareToken, "Results for Synthetic Patient", &password, nil)

	handler := NewHandler(mock, &mockStorage{downloadURL: "https://s3.example.com/video"}, testBaseURL, 0, 0, 0, 0, testHMACSecret, false)
	rec := serveWatchPage(handler, managedWatchRequest(shareToken))

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{"Video code", `autocapitalize="characters"`, "/api/watch/" + shareToken + "/verify", `nonce="test-nonce"`} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in the gate", want)
		}
	}
	for _, absent := range []string{"Results for Synthetic Patient", "<video", "password protected"} {
		if strings.Contains(body, absent) {
			t.Errorf("expected no %q before the gate", absent)
		}
	}
}

func TestWatchPage_Managed_UnlockedPageUsesMPDLookAndNoSignInLink(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	password, _ := hashSharePassword("K7QM-2XPA-9RTF-HD4W")
	shareToken := "managedopen1"
	expectManagedWatchRow(mock, shareToken, "Sleep check-in", &password, nil)
	expectViewRecording(mock, "vid-1")

	handler := NewHandler(mock, &mockStorage{downloadURL: "https://s3.example.com/video.webm"}, testBaseURL, 0, 0, 0, 0, testHMACSecret, false)
	req := managedWatchRequest(shareToken)
	req.AddCookie(&http.Cookie{Name: watchCookieName(shareToken), Value: signWatchCookie(testHMACSecret, shareToken, password)})
	rec := serveWatchPage(handler, req)

	body := rec.Body.String()
	if !strings.Contains(body, "<video") || !strings.Contains(body, "--brand-bg: #f4f7f6") || !strings.Contains(body, "/images/mpd-orb.svg") {
		t.Error("expected the video with MPD's look")
	}
	if strings.Contains(body, `href="`+testBaseURL+`" class="logo"`) {
		t.Error("the logo should not lead a patient to SendRec's sign-in")
	}
	waitAndCheckExpectations(t, mock)
}

func TestWatchPage_UnmanagedKeepsSendRecPages(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	password, _ := hashSharePassword("secret123")
	shareToken := "plaingate123"
	expectManagedWatchRow(mock, shareToken, "A video", &password, nil)

	useMPDPatientPages(t)
	handler := NewHandler(mock, &mockStorage{downloadURL: "https://s3.example.com/video"}, testBaseURL, 0, 0, 0, 0, testHMACSecret, false)
	rec := serveWatchPage(handler, watchPageRequest(shareToken))

	if body := rec.Body.String(); !strings.Contains(body, "password protected") || strings.Contains(body, "Video code") {
		t.Error("an unmanaged share keeps SendRec's password page and its free-form password")
	}
}

func TestWatchPage_MPDPages_UnknownLink(t *testing.T) {
	useMPDPatientPages(t)
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	handler := NewHandler(mock, &mockStorage{downloadURL: "https://s3.example.com/video"}, testBaseURL, 0, 0, 0, 0, testHMACSecret, false)

	mock.ExpectQuery(`SELECT v.id, v.title, v.file_key`).WithArgs("unknownlink1").WillReturnError(errors.New("no rows"))
	assertMPDUnavailablePage(t, serveWatchPage(handler, watchPageRequest("unknownlink1")), http.StatusNotFound)
}

func TestWatchPage_MPDPages_ExpiredUnmanagedShareKeepsSendRecPage(t *testing.T) {
	useMPDPatientPages(t)
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	handler := NewHandler(mock, &mockStorage{downloadURL: "https://s3.example.com/video"}, testBaseURL, 0, 0, 0, 0, testHMACSecret, false)

	expired := time.Now().Add(-time.Hour)
	expectManagedWatchRow(mock, "expiredlink1", "Old video", nil, &expired)
	rec := serveWatchPage(handler, watchPageRequest("expiredlink1"))
	if rec.Code != http.StatusGone || !strings.Contains(rec.Body.String(), "This link has expired") {
		t.Errorf("expected SendRec's expired page, got %d", rec.Code)
	}
}

func TestMPDPublicAccess_UnavailableLinkIsAPageWhileOtherPathsStayJSON(t *testing.T) {
	expired := time.Now().Add(-time.Hour)
	deleted := time.Now().Add(-time.Hour)
	for name, state := range map[string]struct {
		published        bool
		deleted, expires *time.Time
	}{
		"unpublished": {},
		"deleted":     {published: true, deleted: &deleted},
		"expired":     {published: true, expires: &expired},
	} {
		t.Run(name, func(t *testing.T) {
			assertManagedLinkUnavailable(t, state.published, state.deleted, state.expires)
		})
	}
}

func assertManagedLinkUnavailable(t *testing.T, published bool, deleted, expires *time.Time) {
	t.Helper()
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	handler := NewHandler(mock, &mockStorage{}, testBaseURL, 0, 0, 0, 0, testHMACSecret, false)
	const shareToken = "unavailable1"
	expectState := func() {
		mock.ExpectQuery(`SELECT s.published,s.deleted_at`).WithArgs(shareToken).WillReturnRows(
			pgxmock.NewRows([]string{"published", "deleted_at", "share_password", "email_gate_enabled", "share_expires_at"}).
				AddRow(published, deleted, (*string)(nil), false, expires))
	}
	router := chi.NewRouter()
	router.With(handler.MPDPublicAccess).Get("/watch/{shareToken}", func(http.ResponseWriter, *http.Request) { t.Error("an unavailable link was served") })
	router.With(handler.MPDPublicAccess).Get("/api/watch/{shareToken}", func(http.ResponseWriter, *http.Request) { t.Error("an unavailable link was served") })

	expectState()
	page := httptest.NewRecorder()
	router.ServeHTTP(page, watchPageRequest(shareToken))
	assertMPDUnavailablePage(t, page, http.StatusNotFound)

	expectState()
	api := httptest.NewRecorder()
	router.ServeHTTP(api, httptest.NewRequest(http.MethodGet, "/api/watch/"+shareToken, nil))
	if api.Code != http.StatusNotFound || strings.Contains(api.Body.String(), "<html") {
		t.Errorf("expected the API to stay JSON, got %d %s", api.Code, api.Body.String())
	}
}
