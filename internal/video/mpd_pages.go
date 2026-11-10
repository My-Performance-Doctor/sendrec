package video

import (
	"html/template"
	"log/slog"
	"net/http"

	"github.com/sendrec/sendrec/internal/httputil"
)

// mpdPatientPages makes an unknown link show MPD's page instead of SendRec's,
// because it cannot say whether it was ever a managed one. Set once at startup,
// before the server takes requests, and read-only after.
var mpdPatientPages bool

func SetMPDPatientPages(enabled bool) { mpdPatientPages = enabled }

const mpdFontCSS = `@font-face { font-family: "Geist"; src: url("/fonts/geist-variable.woff2") format("woff2"); font-weight: 100 900; font-display: swap; }`

// The arc sits in the page's flow, so it never covers text on a short screen.
const mpdPageCSS = mpdFontCSS + `
        * { box-sizing: border-box; margin: 0; }
        html { -webkit-text-size-adjust: 100%; }
        body { display: flex; flex-direction: column; min-height: 100vh; min-height: 100dvh; background: #f4f7f6; color: #103340; font: 16px/1.4 "Geist", system-ui, -apple-system, sans-serif; }
        main { width: 100%; max-width: 438px; margin: 0 auto; padding: 24px 24px 32px; }
        .logo { display: block; width: 96px; height: 30px; margin-bottom: 44px; }
        h1 { margin-bottom: 8px; font-size: 30px; font-weight: 600; line-height: 1.2; letter-spacing: -0.02em; }
        .stop { color: #d9ae6b; }
        p { color: #42594f; }
        .arc { display: block; width: 100%; height: auto; margin-top: auto; }`

const mpdHeadStart = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <meta name="robots" content="noindex">
    <link rel="icon" type="image/svg+xml" href="/images/mpd-orb.svg">`

// mpdGatePageTemplate asks for the code mpd-api set on the video. It says code,
// not password, so a patient does not reach for their app password. Reads
// nothing from the recording: its title can name a patient.
var mpdGatePageTemplate = template.Must(template.New("mpd-gate").Parse(mpdHeadStart + `
    <title>Your video — {{.Branding.CompanyName}}</title>
    <style nonce="{{.Nonce}}">
        ` + mpdPageCSS + `
        .intro { margin-bottom: 24px; }
        label { display: block; margin-bottom: 6px; font-size: 14px; font-weight: 500; }
        .field { display: flex; align-items: center; gap: 8px; height: 56px; padding: 0 6px 0 14px; border: 1px solid rgb(47 65 52 / 55%); border-radius: 12px; background: #fff; }
        .field:focus-within { border-color: #18566b; box-shadow: 0 0 0 3px rgb(24 86 107 / 28%); }
        .field[data-invalid] { border-color: #af2929; }
        input { flex: 1; min-width: 0; border: 0; outline: 0; background: none; color: inherit; font: 500 16px/1 "Geist", system-ui, -apple-system, sans-serif; letter-spacing: 0.03em; font-variant-numeric: tabular-nums; }
        input::placeholder { color: #6b7a74; font-weight: 400; }
        button { font: inherit; cursor: pointer; }
        button:focus-visible { outline: 2px solid #18566b; outline-offset: 2px; }
        .paste { flex: none; height: 44px; padding: 0 14px; border: 0; border-radius: 8px; background: #e7f0f2; color: #18566b; font-size: 14px; font-weight: 500; }
        .error { margin-top: 8px; color: #af2929; font-size: 14px; }
        .error:empty { display: none; }
        .watch { width: 100%; height: 52px; margin-top: 12px; border: 0; border-radius: 12px; background: #18566b; color: #fff; font-weight: 600; }
        .watch:disabled, .paste:disabled { background: #e8eeed; color: #52665f; cursor: default; }
        .help { margin-top: 14px; font-size: 14px; text-align: center; }
    </style>
</head>
<body>
    <main>
        <img class="logo" src="/images/mpd-os-black.svg" alt="{{.Branding.CompanyName}}" width="96" height="30">
        <h1>Your video is ready<span class="stop">.</span></h1>
        <p class="intro">Enter the code from your message to watch it.</p>
        <form id="code-form" novalidate>
            <label for="code">Video code</label>
            <div class="field" id="field">
                <input id="code" type="text" placeholder="XXXX-XXXX-XXXX-XXXX" autocomplete="off" autocapitalize="characters" autocorrect="off" spellcheck="false" aria-describedby="error">
                <button class="paste" id="paste" type="button" hidden>Paste</button>
            </div>
            <p class="error" id="error" role="alert"></p>
            <button class="watch" id="watch" type="submit" disabled>Watch video</button>
        </form>
        <p class="help">Can't find your code? Ask your care team.</p>
    </main>
    <img class="arc" src="/images/terra-arc-light.svg" alt="" width="1440" height="300">
    <script nonce="{{.Nonce}}">
        (function () {
            var input = document.getElementById('code');
            var field = document.getElementById('field');
            var paste = document.getElementById('paste');
            var watch = document.getElementById('watch');
            var error = document.getElementById('error');
            var code = /[A-HJ-NP-Z2-9]{4}-[A-HJ-NP-Z2-9]{4}-[A-HJ-NP-Z2-9]{4}-[A-HJ-NP-Z2-9]{4}/;
            var busy = false;
            var refused = '';

            function show(message) {
                error.textContent = message;
                if (message) field.setAttribute('data-invalid', '');
                else field.removeAttribute('data-invalid');
                input.setAttribute('aria-invalid', message ? 'true' : 'false');
            }

            // Finds a complete code in any case, spacing or missing hyphens, or in a
            // whole pasted message. Anything else, such as an older password, stays as entered.
            function tidy(value) {
                var upper = value.toUpperCase();
                var found = upper.match(code);
                if (found) return found[0];
                var typed = upper.replace(/[\s-]/g, '');
                return /^[A-HJ-NP-Z2-9]{16}$/.test(typed) ? typed.match(/.{4}/g).join('-') : value.trim();
            }

            function lock(locked) {
                input.disabled = locked;
                paste.disabled = locked;
                watch.disabled = locked || !input.value;
            }

            function submit() {
                if (busy || !input.value) return;
                busy = true;
                watch.disabled = true;
                show('');
                var sent = tidy(input.value);
                fetch('/api/watch/{{.ShareToken}}/verify', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ password: sent })
                }).then(function (response) {
                    if (response.ok) { window.location.reload(); return; }
                    busy = false;
                    watch.disabled = false;
                    if (response.status === 429) {
                        // Too many tries from one address waits seconds; too many wrong codes for the video waits 15 minutes.
                        var wait = Number(response.headers.get('Retry-After'));
                        lock(true);
                        if (wait > 0 && wait < 60) {
                            show('Too many tries. Wait a few seconds and try again.');
                            setTimeout(function () { lock(false); show(''); }, wait * 1000);
                        } else show('Too many tries. Try again in 15 minutes.');
                    } else if (response.status === 403) {
                        refused = sent;
                        show("That code didn't match. Check your message and try again.");
                    } else show('Something went wrong. Try again.');
                }).catch(function () {
                    busy = false;
                    watch.disabled = false;
                    show('Something went wrong. Try again.');
                });
            }

            function entered() {
                show('');
                watch.disabled = !input.value;
                var tidied = tidy(input.value);
                // A refused code is only sent again from the Watch button, so edits around it don't use up tries.
                if (code.test(tidied) && tidied !== refused) {
                    input.value = tidied;
                    submit();
                }
            }

            input.addEventListener('input', entered);
            document.getElementById('code-form').addEventListener('submit', function (event) {
                event.preventDefault();
                submit();
            });
            // Browsers without it, or a refusal, leave pressing and holding the field.
            if (navigator.clipboard && navigator.clipboard.readText) {
                paste.hidden = false;
                paste.addEventListener('click', function () {
                    navigator.clipboard.readText().then(function (text) {
                        input.value = text;
                        entered();
                    }).catch(function () { input.focus(); });
                });
            }
        })();
    </script>
</body>
</html>`))

type mpdUnavailablePageData struct {
	CompanyName string
	Nonce       string
}

// mpdUnavailablePageTemplate covers unpublished, deleted and expired managed
// links and unknown links alike, so the page says nothing about which it was.
var mpdUnavailablePageTemplate = template.Must(template.New("mpd-unavailable").Parse(mpdHeadStart + `
    <title>Video unavailable — {{.CompanyName}}</title>
    <style nonce="{{.Nonce}}">
        ` + mpdPageCSS + `
    </style>
</head>
<body>
    <main>
        <img class="logo" src="/images/mpd-os-black.svg" alt="{{.CompanyName}}" width="96" height="30">
        <h1>This video isn't available<span class="stop">.</span></h1>
        <p>Ask your care team for a new link.</p>
    </main>
    <img class="arc" src="/images/terra-arc-light.svg" alt="" width="1440" height="300">
</body>
</html>`))

func writeMPDUnavailablePage(w http.ResponseWriter, r *http.Request, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := mpdUnavailablePageTemplate.Execute(w, mpdUnavailablePageData{
		CompanyName: instanceDefaults.CompanyName,
		Nonce:       httputil.NonceFromContext(r.Context()),
	}); err != nil {
		slog.Error("mpd-pages: failed to render unavailable page", "error", err)
	}
}

// watchPageCSS gives a managed recording's watch page MPD's light look over the
// page's dark defaults. It goes through the custom CSS slot so the upstream
// template stays as it is. The player keeps its dark controls.
func watchPageCSS(managed bool, custom string) template.CSS {
	if !managed {
		return template.CSS(custom)
	}
	return template.CSS(mpdWatchCSS + custom)
}

const mpdWatchCSS = mpdFontCSS + `
        :root { --brand-bg: #f4f7f6; --brand-surface: #e3eaec; --brand-text: #103340; --brand-accent: #18566b; }
        body { font-family: "Geist", system-ui, -apple-system, sans-serif; }
        .logo { display: block; width: 96px; height: 30px; background: url("/images/mpd-os-black.svg") left center / contain no-repeat; color: transparent; font-size: 0; }
        .logo img { display: none; }
        .player-container { --player-accent: #d9ae6b; }
        .video-title { font-weight: 600; letter-spacing: -0.02em; }
        .video-meta, .video-meta-avatar, .comment-meta, .comment-form-actions label, .no-comments, .panel-tab,
        .transcript-processing, .processing-desc, .cta-desc, .cta-dismiss, .timestamp-toggle { color: #42594f; }
        .video-meta-name, .comment-author, .comment-body, .transcript-header, .transcript-text, .comment-form textarea,
        .comment-form input, .reaction-btn, .emoji-trigger { color: #103340; }
        .video-meta-sep, .branding, .emoji-category { color: #52665f; }
        .branding { border-top-color: rgb(47 65 52 / 16%); }
        .comment, .comment-form textarea, .comment-form input, .reaction-btn, .comment.emoji-reaction, .emoji-trigger, .emoji-grid { border-color: rgb(47 65 52 / 16%); }
        .emoji-grid { background: #fff; }
        .comment-form textarea::placeholder, .comment-form input::placeholder { color: #6b7a74; }
        .download-btn:hover, .reaction-btn:hover, .timestamp-toggle.active, .timestamp-toggle.active:hover,
        .chapter-item.active, .transcript-segment.active { background: #e7f0f2; }
        .chapter-item:hover, .transcript-segment:hover, .timestamp-toggle:hover { background: rgb(47 65 52 / 6%); color: #103340; }
        .timestamp-toggle { background: rgb(47 65 52 / 6%); }
        @media (max-width: 640px) {
            .container { padding: 12px 20px; }
            .player-container { width: auto; margin-inline: -20px; border-radius: 0; }
            .video-title { font-size: 24px; }
            .transcript-segment { min-height: 44px; }
        }
`
