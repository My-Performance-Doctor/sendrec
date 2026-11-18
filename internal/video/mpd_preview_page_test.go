package video

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	// cmd/sendrec builds the zone database in; the tests need it too on hosts without zoneinfo.
	_ "time/tzdata"

	"github.com/sendrec/sendrec/internal/httputil"
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
	// Only the title is capped, so the link state is never clipped in a short window.
	if strings.Contains(mpdPreviewCSS, "#mpd-meta{max-height") {
		t.Error("the header itself is capped")
	}
	for _, want := range []string{"#mpd-meta:not(.shown)", "video:focus-visible{outline:2px solid", "#mpd-meta.full #mpd-title{display:block;-webkit-line-clamp:none"} {
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
		if got := recordedLabel(parsed.In(time.FixedZone("Los Angeles", -8*3600))); got != want {
			t.Errorf("%s: %q, want %q", at, got, want)
		}
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
		got := previewLink(MPDMetadata{Published: c.published, PasswordProtected: c.password}, c.expires, c.emailGate, now).text()
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}
