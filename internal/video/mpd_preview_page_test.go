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
