package video

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

// Opened in its own tab, the preview names the recording in a header; framed in Staff OS the
// header is only visually hidden. The title is set as text, never as markup.
func TestMPDPreviewPageNamesTheRecording(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/mpd-preview", nil)
	r = r.WithContext(httputil.ContextWithNonce(r.Context(), "test-nonce"))
	w := httptest.NewRecorder()
	(&Handler{}).MPDPreviewPage(w, r)
	body := w.Body.String()
	for _, want := range []string{`<header id="mpd-meta">`, `<h1 id="mpd-title">Staff preview</h1>`, `<p id="mpd-detail"></p>`, "#mpd-meta:not(.shown)"} {
		if !strings.Contains(body, want) {
			t.Errorf("preview page missing %q", want)
		}
	}
	for _, want := range []string{"describe(redeemed)", "getElementById('mpd-title').textContent=v.title", "window.parent===window)meta.classList.add('shown')"} {
		if !strings.Contains(mpdPlayerJS, want) {
			t.Errorf("player script missing %q", want)
		}
	}
	for _, sink := range []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write", "document.title"} {
		if strings.Contains(mpdPlayerJS, sink) {
			t.Errorf("player script uses %s; the title may name a patient and stays text in the page header", sink)
		}
	}
}
