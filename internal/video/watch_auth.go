package video

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sendrec/sendrec/internal/httputil"
	"golang.org/x/crypto/bcrypt"
)

func hashSharePassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

func checkSharePassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

func watchCookieName(shareToken string) string {
	prefix := shareToken
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	return "wa_" + prefix
}

// deriveCookieKey separates a cookie-signing key from the JWT secret it comes
// from, so no single key signs two different kinds of message (SR-12). Deriving
// here rather than at the call sites keeps the raw secret out of every HMAC.
func deriveCookieKey(secret, purpose string) []byte {
	sum := sha256.Sum256([]byte("sendrec/" + purpose + "/v1|" + secret))
	return sum[:]
}

func signWatchCookie(hmacSecret, shareToken, passwordHash string) string {
	hashPrefix := passwordHash
	if len(hashPrefix) > 16 {
		hashPrefix = hashPrefix[:16]
	}
	mac := hmac.New(sha256.New, deriveCookieKey(hmacSecret, "watch-cookie"))
	mac.Write([]byte(shareToken + "|" + hashPrefix))
	return hex.EncodeToString(mac.Sum(nil))
}

func verifyWatchCookie(hmacSecret, shareToken, passwordHash, cookieValue string) bool {
	expected := signWatchCookie(hmacSecret, shareToken, passwordHash)
	return hmac.Equal([]byte(expected), []byte(cookieValue))
}

func setWatchCookie(w http.ResponseWriter, shareToken, cookieValue string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     watchCookieName(shareToken),
		Value:    cookieValue,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteNoneMode,
		MaxAge:   int(7 * 24 * time.Hour / time.Second),
	})
}

func hasValidWatchCookie(r *http.Request, hmacSecret, shareToken, passwordHash string) bool {
	cookie, err := r.Cookie(watchCookieName(shareToken))
	if err != nil {
		return false
	}
	return verifyWatchCookie(hmacSecret, shareToken, passwordHash, cookie.Value)
}

// enforceWatchAccess applies the share-password and email-gate checks that guard
// a shared video. Every watch surface — JSON, download, thumbnail, oEmbed — must
// route through it; enforcing only on the rendered pages left the data APIs open
// to anyone holding the share link.
//
// It writes the error response and returns false when access is denied.
func (h *Handler) enforceWatchAccess(w http.ResponseWriter, r *http.Request, shareToken string, sharePassword *string, emailGateEnabled bool) bool {
	if sharePassword != nil && !hasValidWatchCookie(r, h.hmacSecret, shareToken, *sharePassword) {
		httputil.WriteError(w, http.StatusForbidden, "password required")
		return false
	}

	if emailGateEnabled {
		if _, ok := hasValidEmailGateCookie(r, h.hmacSecret, shareToken); !ok {
			httputil.WriteError(w, http.StatusForbidden, "email required")
			return false
		}
	}

	return true
}

// A video's share password locks for sharePasswordWindow once it has had
// sharePasswordMaxFailures wrong guesses in one window, whichever addresses
// they came from. The per-IP limiter still applies on top. The window is
// fixed: it starts at the first failure and a failure after it ends starts a
// new one. While locked, even the right password waits, so the lock gives
// away nothing.
const (
	sharePasswordMaxFailures = 10
	sharePasswordWindow      = 15 * time.Minute
)

func (h *Handler) recordSharePasswordFailure(ctx context.Context, videoID string) {
	if _, err := h.db.Exec(ctx,
		`INSERT INTO share_password_attempts (video_id, window_start, failures) VALUES ($1, now(), 1)
		 ON CONFLICT (video_id) DO UPDATE SET
		   failures = CASE WHEN share_password_attempts.window_start > now() - make_interval(secs => $2)
		                   THEN share_password_attempts.failures + 1 ELSE 1 END,
		   window_start = CASE WHEN share_password_attempts.window_start > now() - make_interval(secs => $2)
		                       THEN share_password_attempts.window_start ELSE now() END`,
		videoID, sharePasswordWindow.Seconds(),
	); err != nil {
		slog.Error("watch: failed to record wrong share password", "video_id", videoID, "error", err)
	}
}

type verifyPasswordRequest struct {
	Password string `json:"password"`
}

func (h *Handler) VerifyWatchPassword(w http.ResponseWriter, r *http.Request) {
	shareToken := chi.URLParam(r, "shareToken")

	var req verifyPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if len(req.Password) > 128 {
		httputil.WriteError(w, http.StatusBadRequest, "password is too long")
		return
	}

	// The lockout state comes back with the video, so if it cannot be read
	// the video cannot either and the attempt is refused.
	var videoID string
	var sharePassword *string
	var locked bool
	err := h.db.QueryRow(r.Context(),
		`SELECT v.id, v.share_password,
		        COALESCE(a.failures >= $2 AND a.window_start > now() - make_interval(secs => $3), false)
		 FROM videos v LEFT JOIN share_password_attempts a ON a.video_id = v.id
		 WHERE v.share_token = $1 AND v.status IN ('ready', 'processing')`,
		shareToken, sharePasswordMaxFailures, sharePasswordWindow.Seconds(),
	).Scan(&videoID, &sharePassword, &locked)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, "video not found")
		return
	}

	if sharePassword == nil {
		w.WriteHeader(http.StatusOK)
		return
	}

	if locked {
		w.Header().Set("Retry-After", strconv.Itoa(int(sharePasswordWindow.Seconds())))
		httputil.WriteError(w, http.StatusTooManyRequests, "too many incorrect passwords, try again later")
		return
	}

	if !checkSharePassword(*sharePassword, req.Password) {
		h.recordSharePasswordFailure(r.Context(), videoID)
		httputil.WriteError(w, http.StatusForbidden, "incorrect password")
		return
	}

	sig := signWatchCookie(h.hmacSecret, shareToken, *sharePassword)
	setWatchCookie(w, shareToken, sig, h.secureCookies)
	w.WriteHeader(http.StatusOK)
}

// --- Email gate cookie helpers ---

func emailGateCookieName(shareToken string) string {
	prefix := shareToken
	if len(prefix) > 8 {
		prefix = prefix[:8]
	}
	return "eg_" + prefix
}

func signEmailGateCookie(hmacSecret, shareToken, email string) string {
	mac := hmac.New(sha256.New, deriveCookieKey(hmacSecret, "email-gate-cookie"))
	mac.Write([]byte(shareToken + "|" + email))
	sig := hex.EncodeToString(mac.Sum(nil))
	return email + "|" + sig
}

func verifyEmailGateCookie(hmacSecret, shareToken, cookieValue string) (string, bool) {
	parts := strings.SplitN(cookieValue, "|", 2)
	if len(parts) != 2 {
		return "", false
	}
	email := parts[0]
	expected := signEmailGateCookie(hmacSecret, shareToken, email)
	if !hmac.Equal([]byte(expected), []byte(cookieValue)) {
		return "", false
	}
	return email, true
}

func setEmailGateCookie(w http.ResponseWriter, shareToken, value string, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     emailGateCookieName(shareToken),
		Value:    value,
		Path:     "/",
		MaxAge:   7 * 24 * 60 * 60,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteNoneMode,
	})
}

func hasValidEmailGateCookie(r *http.Request, hmacSecret, shareToken string) (string, bool) {
	cookie, err := r.Cookie(emailGateCookieName(shareToken))
	if err != nil {
		return "", false
	}
	return verifyEmailGateCookie(hmacSecret, shareToken, cookie.Value)
}

type identifyViewerRequest struct {
	Email string `json:"email"`
}

func (h *Handler) IdentifyViewer(w http.ResponseWriter, r *http.Request) {
	shareToken := chi.URLParam(r, "shareToken")

	var req identifyViewerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	req.Email = strings.TrimSpace(req.Email)
	if req.Email == "" || len(req.Email) > 320 || !strings.Contains(req.Email, "@") {
		httputil.WriteError(w, http.StatusBadRequest, "invalid email")
		return
	}

	var videoID string
	err := h.db.QueryRow(r.Context(),
		`SELECT v.id FROM videos v WHERE v.share_token = $1 AND v.status IN ('ready', 'processing')`,
		shareToken,
	).Scan(&videoID)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, "video not found")
		return
	}

	ip := httputil.ClientIP(r)
	hash := viewerHash(ip, r.UserAgent())

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if _, err := h.db.Exec(ctx,
			`INSERT INTO video_viewers (video_id, email, viewer_hash) VALUES ($1, $2, $3)
			 ON CONFLICT (video_id, email) DO NOTHING`,
			videoID, req.Email, hash,
		); err != nil {
			slog.Error("watch-auth: failed to record viewer identity", "video_id", videoID, "error", err)
		}
	}()

	sig := signEmailGateCookie(h.hmacSecret, shareToken, req.Email)
	setEmailGateCookie(w, shareToken, sig, h.secureCookies)

	w.WriteHeader(http.StatusOK)
}
