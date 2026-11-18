package video

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	// cmd/sendrec builds the zone database in; the tests need it too on hosts without zoneinfo.
	_ "time/tzdata"

	"github.com/pashagolub/pgxmock/v5"
	"github.com/sendrec/sendrec/internal/httputil"
	"github.com/sendrec/sendrec/internal/mpd"
)

func TestMPDPreviewPageStylesCarryTheCSPNonce(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/mpd-preview", nil)
	r = r.WithContext(httputil.ContextWithNonce(r.Context(), "test-nonce"))
	w := httptest.NewRecorder()
	(&Handler{}).MPDPreviewPage(w, r)
	body := w.Body.String()
	if !strings.Contains(body, `<style nonce="test-nonce">`) || !strings.Contains(body, "object-fit:contain") {
		t.Fatalf("preview page styles are missing the request nonce: %s", body)
	}
	// The CSP has no 'unsafe-inline', so a style attribute would be ignored.
	if strings.Contains(body, " style=") {
		t.Fatalf("preview page uses an inline style attribute: %s", body)
	}
}

// Opened in its own tab, the preview names the recording in a header under a visible "Staff preview"
// label, with a toggle for a clamped title; framed in Staff OS the header is only visually hidden.
func TestMPDPreviewPageNamesTheRecording(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/mpd-preview", nil)
	r = r.WithContext(httputil.ContextWithNonce(r.Context(), "test-nonce"))
	w := httptest.NewRecorder()
	(&Handler{}).MPDPreviewPage(w, r)
	body := w.Body.String()
	_, rest, opened := strings.Cut(body, `<header id="mpd-meta">`)
	header, _, closed := strings.Cut(rest, `</header>`)
	if !opened || !closed {
		t.Fatalf("preview page has no complete mpd-meta header: %s", body)
	}
	// In reading order: the label, the title, the date and link state, then the toggle.
	at := 0
	for _, want := range []string{`<p id="mpd-label">Staff preview</p>`, `<h1 id="mpd-title">Staff preview</h1>`, `<p id="mpd-detail"></p>`,
		`<button type="button" id="mpd-more" aria-controls="mpd-title" aria-expanded="false" hidden>`} {
		found := strings.Index(header[at:], want)
		if found < 0 {
			t.Fatalf("preview header missing %q after the parts before it: %s", want, header)
		}
		at += found + len(want)
	}
	// Collapsed, the header is not capped, so the link state is never clipped in a short window; expanded,
	// it scrolls within a cap so the video keeps most of the window.
	if strings.Contains(mpdPreviewCSS, "#mpd-meta{max-height") {
		t.Error("the header itself is capped")
	}
	for _, want := range []string{"#mpd-meta:not(.shown)", "video:focus-visible{outline:2px solid", "#mpd-meta.full{max-height:40vh;overflow-y:auto}", "#mpd-meta.full #mpd-title{display:block;-webkit-line-clamp:none"} {
		if !strings.Contains(mpdPreviewCSS, want) {
			t.Errorf("preview styles missing %q", want)
		}
	}
	// The title can name a patient: it stays text in the page header, never markup, an attribute,
	// the tab title or the address. The date comes from the server, not the browser's locale.
	for _, sink := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "document.title", "setAttribute('title'", ".title=v.title", "pushState", "replaceState(null,v", "toLocaleDateString"} {
		if strings.Contains(mpdPlayerJS, sink) {
			t.Errorf("player script uses %s", sink)
		}
	}
}

// The recording date is the clinic's day in en-AU form, whatever zone the server or browser runs in.
func TestPreviewRecordedDateIsTheClinicsDay(t *testing.T) {
	for at, want := range map[string]string{
		"2026-11-17T22:30:00Z": "Recorded 18 Nov 2026",
		"2026-11-18T12:59:00Z": "Recorded 18 Nov 2026",
		"2026-11-18T13:00:00Z": "Recorded 19 Nov 2026",
		"2026-06-30T14:00:00Z": "Recorded 1 Jul 2026",
	} {
		parsed, err := time.Parse(time.RFC3339, at)
		if err != nil {
			t.Fatal(err)
		}
		if got := recordedLabel(parsed.In(time.FixedZone("Los Angeles", -8*3600)), nil); got != want {
			t.Errorf("%s: %q, want %q", at, got, want)
		}
	}
}

type fixedIdentity struct{ principal *mpd.Principal }

func (f fixedIdentity) ValidateSession(context.Context, string) (*mpd.Principal, error) {
	return f.principal, nil
}

// A fault reading the recording is 503 and leaves the handoff unspent, so the page can try again.
func TestRedeemAnswers503WhenTheRecordingCannotBeRead(t *testing.T) {
	mock, err := pgxmock.NewPool()
	if err != nil {
		t.Fatal(err)
	}
	defer mock.Close()
	h := &Handler{db: mock, baseURL: "https://sendrec.example.test"}
	principal := &mpd.Principal{OrganizationID: "synthetic-org", TenantID: "synthetic-tenant", Capabilities: mpd.Capabilities{Read: true}}
	media := h.MPDMedia(fixedIdentity{principal}, MPDServiceConfig{WorkspaceID: "synthetic-org", TenantID: "synthetic-tenant"}, nil)
	mock.ExpectBegin()
	mock.ExpectQuery("FROM mpd_preview_handoffs").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnRows(pgxmock.NewRows([]string{"video_id", "media_version", "session_id", "title", "created_at", "share_expires_at", "email_gate_enabled"}).
		AddRow("synthetic-video", 1, "synthetic-session", "Synthetic", time.Now(), (*time.Time)(nil), false))
	mock.ExpectQuery("FROM mpd_video_state").WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).WillReturnError(errors.New("synthetic connection reset"))
	mock.ExpectRollback()
	request := httptest.NewRequest(http.MethodPost, "/api/mpd/preview/redeem", strings.NewReader(`{"handoff":"synthetic","nonce":"synthetic","parentOrigin":"https://sendrec.example.test"}`))
	request.Header.Set("Origin", "https://sendrec.example.test")
	recorder := httptest.NewRecorder()
	media.RedeemPreview(recorder, request)
	if recorder.Code != 503 {
		t.Fatalf("redeem with a database fault: %d %s", recorder.Code, recorder.Body.String())
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// MPD_CLINIC_TIMEZONE moves the clinic's day.
func TestPreviewRecordedDateFollowsTheConfiguredZone(t *testing.T) {
	perth, err := time.LoadLocation("Australia/Perth")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 11, 17, 15, 30, 0, 0, time.UTC)
	if got := recordedLabel(at, perth); got != "Recorded 17 Nov 2026" {
		t.Errorf("Perth: %q", got)
	}
	if got := recordedLabel(at, nil); got != "Recorded 18 Nov 2026" {
		t.Errorf("default: %q", got)
	}
}

// The header's link state follows the patient gate, and an unpublished link never says "yet": it may
// have been taken down on purpose.
func TestPreviewLinkStateFollowsThePatientGate(t *testing.T) {
	now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	for _, c := range []struct {
		name      string
		published bool
		password  bool
		expires   *time.Time
		emailGate bool
		want      string
	}{
		{"never published or taken down", false, true, nil, false, "Patients can't open the link"},
		{"unpublished and expired", false, true, &past, true, "Patients can't open the link"},
		{"published", true, false, nil, false, "Patients can open the link"},
		{"published with code", true, true, &future, false, "Patients can open the link with their code"},
		{"published with code and email", true, true, nil, true, "Patients can open the link with their code after giving their email"},
		{"expired", true, true, &past, true, "The patient link has expired"},
	} {
		got := previewLinkState(MPDMetadata{Published: c.published, PasswordProtected: c.password}, c.expires, c.emailGate, now)
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
