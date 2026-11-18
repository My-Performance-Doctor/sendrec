package video

import (
	_ "embed"
	"html"
	"net/http"
	"strings"

	"github.com/sendrec/sendrec/internal/httputil"
)

// The player script and the preview page's markup are files of their own, so web/'s tests run the
// real script against the real markup in a DOM (web/src/pages/VideoDetail/mpdPreviewPlayer.test.ts).
var (
	//go:embed mpd_player.js
	mpdPlayerJS string
	//go:embed mpd_preview.html
	mpdPreviewHTML string
	mpdPreviewBody = strings.TrimSpace(mpdPreviewHTML)
)

func (h *Handler) MPDPlayerScript(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(mpdPlayerJS))
}
func (h *Handler) MPDPreviewPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	nonce := html.EscapeString(httputil.NonceFromContext(r.Context()))
	_, _ = w.Write([]byte(`<!doctype html><html lang="en"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>Staff recording preview</title><style nonce="` + nonce + `">` + mpdPreviewCSS + `</style><body>` + mpdPreviewBody + `<script src="/api/mpd/player.js" defer></script></body></html>`))
}

// The preview fills its window, framed by Staff OS or opened in a tab, and letterboxes the recording. The status shows
// on its own while access is checked or once it ends, and stays with screen readers while media is loaded.
// The page's CSP blocks inline style attributes, so the styles carry the request's nonce.
// Opened in its own tab, the page names the recording in a header above the video; framed in Staff OS, which names it
// already, the header is only visually hidden (screen readers still get it) and the video fills the frame.
// A small "Staff preview" label sits above the title, so the tab never reads as the patient's page. A
// title longer than two lines is clamped, and a toggle under the link state shows it in full without putting
// it in an attribute. Collapsed, nothing is capped, so the label, date and link state are never clipped;
// expanded, the header scrolls within 40% of the window so the video keeps most of it.
// Focus shows a 2px yellow ring, inset on the video so it stays clear of the header's border.
const mpdPreviewCSS = `html,body{height:100%;margin:0;background:#000;color:#fff;font:14px/1.4 system-ui,sans-serif}` +
	`body{display:flex;flex-direction:column}` +
	`#mpd-meta{flex:none;padding:12px 16px;border-bottom:1px solid #333}` +
	`#mpd-title{margin:0;font-size:16px;font-weight:600;overflow-wrap:anywhere;display:-webkit-box;-webkit-box-orient:vertical;-webkit-line-clamp:2;overflow:hidden}#mpd-detail{margin:2px 0 0;color:#bbb}` +
	`#mpd-label{margin:0 0 2px;color:#9ecbff;font-size:12px;font-weight:600;letter-spacing:.04em;text-transform:uppercase}` +
	`#mpd-meta.full{max-height:40vh;overflow-y:auto}#mpd-meta.full #mpd-title{display:block;-webkit-line-clamp:none}` +
	`#mpd-more{margin:2px 0 0;padding:0;border:0;background:none;color:#9ecbff;font:inherit;text-decoration:underline;cursor:pointer}#mpd-more[hidden]{display:none}` +
	`#mpd-more:focus-visible{outline:2px solid #ffd54a;outline-offset:2px}video:focus-visible{outline:2px solid #ffd54a;outline-offset:-4px}` +
	`#mpd-status{position:absolute;top:50%;right:16px;left:16px;margin:0;text-align:center;transform:translateY(-50%)}` +
	`#mpd-meta:not(.shown),body:has(video[src]) #mpd-status{position:absolute;width:1px;height:1px;overflow:hidden;clip-path:inset(50%);white-space:nowrap}` +
	`video{display:block;flex:1;min-height:0;width:100%;object-fit:contain}video:not([src]){visibility:hidden}`
