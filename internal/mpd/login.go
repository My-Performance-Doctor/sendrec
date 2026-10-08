package mpd

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/sendrec/sendrec/internal/auth"
	"github.com/sendrec/sendrec/internal/httputil"
	"github.com/sendrec/sendrec/internal/integration"
	"golang.org/x/oauth2"
)

func (h *Handler) allowedOrigin(w http.ResponseWriter, r *http.Request, native bool) bool {
	origin := r.Header.Get("Origin")
	own := strings.TrimRight(h.cfg.BaseURL, "/")
	if origin == own {
		return true
	}
	if !native && contains(h.cfg.AllowedOrigins, origin) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
		return true
	}
	httputil.WriteError(w, 403, "origin is not allowed")
	return false
}
func (h *Handler) Preflight(w http.ResponseWriter, r *http.Request) {
	if !h.allowedOrigin(w, r, false) {
		return
	}
	w.Header().Set("Access-Control-Allow-Methods", "POST")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
	w.Header().Set("Access-Control-Max-Age", "300")
	w.WriteHeader(204)
}
func identityError(w http.ResponseWriter, err error) {
	if err == ErrLinkRequired || err == ErrCapacity {
		httputil.WriteError(w, 409, err.Error())
		return
	}
	code := http.StatusUnauthorized
	if err == ErrUnavailable {
		code = 503
	}
	httputil.WriteError(w, code, "MPD sign in or authorization unavailable")
}
func (h *Handler) populateDisplay(r *http.Request, raw string, id *Identity) error {
	info, err := h.provider.UserInfo(oidc.ClientContext(r.Context(), h.client), oauth2.StaticTokenSource(&oauth2.Token{AccessToken: raw}))
	if err != nil {
		return h.cognitoProfile(r.Context(), raw, id)
	}
	if info.Subject != id.Subject {
		return ErrDenied
	}
	var c struct {
		Name string `json:"name"`
	}
	if info.Claims(&c) != nil || !info.EmailVerified || info.Email == "" {
		return ErrDenied
	}
	id.Email = info.Email
	id.Name = c.Name
	return nil
}
func (h *Handler) Exchange(w http.ResponseWriter, r *http.Request) {
	if !h.allowedOrigin(w, r, false) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || len(raw) > 16384 {
		identityError(w, ErrDenied)
		return
	}
	id, err := h.VerifyAccess(r.Context(), raw)
	if err != nil {
		identityError(w, err)
		return
	}
	g, err := h.Access(r.Context(), raw, id)
	if err != nil {
		identityError(w, err)
		return
	}
	if err = h.populateDisplay(r, raw, id); err != nil {
		identityError(w, err)
		return
	}
	p, err := h.provision(r.Context(), id, g)
	if err != nil {
		identityError(w, err)
		return
	}
	sid, err := h.createSession(r.Context(), p, id, g, raw, "")
	if err != nil {
		identityError(w, ErrUnavailable)
		return
	}
	h.respondSession(w, r, sid, false)
}
func (h *Handler) Login(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.Enabled {
		http.NotFound(w, r)
		return
	}
	state, e := randomSecret()
	if e != nil {
		identityError(w, ErrUnavailable)
		return
	}
	browser, e := randomSecret()
	if e != nil {
		identityError(w, ErrUnavailable)
		return
	}
	nonce, e := randomSecret()
	if e != nil {
		identityError(w, ErrUnavailable)
		return
	}
	verifier := oauth2.GenerateVerifier()
	enc, e := integration.Encrypt(h.key, verifier)
	if e != nil {
		identityError(w, ErrUnavailable)
		return
	}
	_, e = h.db.Exec(r.Context(), `INSERT INTO mpd_login_transactions(state_hash,browser_hash,nonce,verifier_encrypted,callback,expires_at) VALUES($1,$2,$3,$4,$5,$6)`, hash(state), hash(browser), nonce, enc, h.oauth.RedirectURL, time.Now().Add(5*time.Minute))
	if e != nil {
		identityError(w, ErrUnavailable)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "mpd_login", Value: browser, Path: "/api/auth/mpd", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 300})
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, h.oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier)), http.StatusFound)
}
func (h *Handler) Callback(w http.ResponseWriter, r *http.Request) {
	if !h.cfg.Enabled {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	c, e := r.Cookie("mpd_login")
	if e != nil {
		identityError(w, ErrDenied)
		return
	}
	var nonce, encrypted, callback string
	e = h.db.QueryRow(r.Context(), `DELETE FROM mpd_login_transactions WHERE state_hash=$1 AND browser_hash=$2 AND expires_at>now() RETURNING nonce,verifier_encrypted,callback`, hash(r.URL.Query().Get("state")), hash(c.Value)).Scan(&nonce, &encrypted, &callback)
	if e != nil || callback != h.oauth.RedirectURL || r.URL.Query().Get("code") == "" {
		identityError(w, ErrDenied)
		return
	}
	verifier, e := integration.Decrypt(h.key, encrypted)
	if e != nil {
		identityError(w, ErrDenied)
		return
	}
	token, e := h.oauth.Exchange(oidc.ClientContext(r.Context(), h.client), r.URL.Query().Get("code"), oauth2.VerifierOption(verifier))
	if e != nil {
		identityError(w, ErrDenied)
		return
	}
	rawID, ok := token.Extra("id_token").(string)
	if !ok {
		identityError(w, ErrDenied)
		return
	}
	verified, e := h.idVerifier.Verify(oidc.ClientContext(r.Context(), h.client), rawID)
	if e != nil || verified.Nonce != nonce {
		identityError(w, ErrDenied)
		return
	}
	id, e := h.VerifyAccess(r.Context(), token.AccessToken)
	if e != nil || id.Subject != verified.Subject {
		identityError(w, ErrDenied)
		return
	}
	var display struct {
		Email    string `json:"email"`
		Name     string `json:"name"`
		Verified bool   `json:"email_verified"`
	}
	if verified.Claims(&display) != nil || !display.Verified {
		identityError(w, ErrDenied)
		return
	}
	id.Email = display.Email
	id.Name = display.Name
	g, e := h.Access(r.Context(), token.AccessToken, id)
	if e != nil {
		identityError(w, e)
		return
	}
	p, e := h.provision(r.Context(), id, g)
	if e != nil {
		identityError(w, e)
		return
	}
	if token.RefreshToken == "" {
		identityError(w, ErrDenied)
		return
	}
	sid, e := h.createSession(r.Context(), p, id, g, token.AccessToken, token.RefreshToken)
	if e != nil {
		identityError(w, ErrUnavailable)
		return
	}
	code, e := randomSecret()
	if e != nil {
		identityError(w, ErrUnavailable)
		return
	}
	_, e = h.db.Exec(r.Context(), `INSERT INTO mpd_login_handoffs(code_hash,browser_hash,session_id,expires_at) VALUES($1,$2,$3,$4)`, hash(code), hash(c.Value), sid, time.Now().Add(time.Minute))
	if e != nil {
		identityError(w, ErrUnavailable)
		return
	}
	// Fragment is not sent in HTTP requests or referrers. The SPA removes it before redemption.
	http.Redirect(w, r, strings.TrimRight(h.cfg.BaseURL, "/")+"/login#mpd_code="+code, http.StatusFound)
}
func (h *Handler) Handoff(w http.ResponseWriter, r *http.Request) {
	if !h.allowedOrigin(w, r, true) {
		return
	}
	var b struct {
		Code string `json:"code"`
	}
	if json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&b) != nil || len(b.Code) != 43 {
		identityError(w, ErrDenied)
		return
	}
	c, e := r.Cookie("mpd_login")
	if e != nil {
		identityError(w, ErrDenied)
		return
	}
	var sid string
	if h.db.QueryRow(r.Context(), `DELETE FROM mpd_login_handoffs WHERE code_hash=$1 AND browser_hash=$2 AND expires_at>now() RETURNING session_id`, hash(b.Code), hash(c.Value)).Scan(&sid) != nil {
		identityError(w, ErrDenied)
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "mpd_login", Path: "/api/auth/mpd", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	h.respondSession(w, r, sid, true)
}

type sessionResponse struct {
	session                         *session
	access, refresh, classification string
	expiry                          time.Time
}

func (h *Handler) prepareSession(ctx context.Context, sid string, native bool) (*sessionResponse, error) {
	s, err := h.validateSession(ctx, sid)
	if err != nil {
		return nil, err
	}
	expiry := time.Now().Add(15 * time.Minute)
	if expiry.After(s.CognitoExpiry) {
		expiry = s.CognitoExpiry
	}
	access, err := auth.GenerateManagedAccessToken(h.cfg.JWTSecret, s.UserID, sid, expiry)
	if err != nil {
		return nil, ErrUnavailable
	}
	result := &sessionResponse{session: s, access: access, expiry: expiry}
	if native {
		result.refresh, err = randomSecret()
		if err != nil {
			return nil, ErrUnavailable
		}
		result.classification, err = randomSecret()
		if err != nil {
			return nil, ErrUnavailable
		}
		tag, e := h.db.Exec(ctx, `UPDATE mpd_sessions SET refresh_hash=$2,classification_hash=$3,classification_expires_at=$4 WHERE id=$1 AND NOT revoked`, sid, hash(result.refresh), hash(result.classification), expiry)
		if e != nil {
			return nil, ErrUnavailable
		}
		if tag.RowsAffected() != 1 {
			return nil, ErrDenied
		}
	}
	return result, nil
}
func (h *Handler) writeSession(w http.ResponseWriter, result *sessionResponse) {
	s := result.session
	if result.refresh != "" {
		// Switching to managed sign-in cannot leave a competing local session cookie.
		for _, path := range []string{"/", "/api/auth"} {
			http.SetCookie(w, &http.Cookie{Name: "refresh_token", Path: path, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
		}
		http.SetCookie(w, &http.Cookie{Name: "mpd_refresh", Value: result.refresh, Path: "/api/auth", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: int(time.Until(s.Expiry).Seconds())})
		http.SetCookie(w, &http.Cookie{Name: "mpd_classification", Value: result.classification, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: int(time.Until(result.expiry).Seconds())})
	}
	w.Header().Set("Cache-Control", "no-store")
	httputil.WriteJSON(w, 200, map[string]any{"accessToken": result.access, "expiresAt": result.expiry, "organizationId": s.OrganizationID, "capabilities": s.Capabilities, "managed": true})
}
func (h *Handler) respondSession(w http.ResponseWriter, r *http.Request, sid string, native bool) {
	result, err := h.prepareSession(r.Context(), sid, native)
	if err != nil {
		identityError(w, err)
		return
	}
	h.writeSession(w, result)
}
func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	if !h.allowedOrigin(w, r, true) {
		return
	}
	c, e := r.Cookie("mpd_refresh")
	if e != nil || len(c.Value) != 43 {
		identityError(w, ErrDenied)
		return
	}
	b, ok := h.db.(beginner)
	if !ok {
		identityError(w, ErrUnavailable)
		return
	}
	// Serialize refreshes using the row lock. A failed upstream request leaves the
	// cookie valid; a committed rotation makes a concurrent replay fail its lookup.
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	tx, e := b.Begin(ctx)
	if e != nil {
		identityError(w, ErrUnavailable)
		return
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var sid string
	if tx.QueryRow(ctx, `SELECT id FROM mpd_sessions WHERE refresh_hash=$1 AND NOT revoked AND expires_at>now() FOR UPDATE`, hash(c.Value)).Scan(&sid) != nil {
		identityError(w, ErrDenied)
		return
	}
	scoped := *h
	scoped.db = tx
	s, e := scoped.loadSession(ctx, sid)
	if e != nil || s.RefreshEncrypted == "" {
		identityError(w, ErrDenied)
		return
	}
	raw, e := integration.Decrypt(h.key, s.RefreshEncrypted)
	if e != nil {
		identityError(w, ErrDenied)
		return
	}
	token, e := h.oauth.TokenSource(oidc.ClientContext(ctx, h.client), &oauth2.Token{RefreshToken: raw}).Token()
	if e != nil {
		identityError(w, ErrUnavailable)
		return
	}
	id, e := h.VerifyAccess(ctx, token.AccessToken)
	if e != nil {
		identityError(w, e)
		return
	}
	var subject string
	if tx.QueryRow(ctx, `SELECT subject FROM mpd_external_identities WHERE user_id=$1 AND issuer=$2`, s.UserID, id.Issuer).Scan(&subject) != nil || subject != id.Subject {
		identityError(w, ErrDenied)
		return
	}
	enc, e := integration.Encrypt(h.key, token.AccessToken)
	if e != nil {
		identityError(w, ErrUnavailable)
		return
	}
	if token.RefreshToken == "" {
		token.RefreshToken = raw
	}
	refresh, e := integration.Encrypt(h.key, token.RefreshToken)
	if e != nil {
		identityError(w, ErrUnavailable)
		return
	}
	if _, e = tx.Exec(ctx, `UPDATE mpd_sessions SET access_encrypted=$2,refresh_encrypted=$3,cognito_expires_at=$4 WHERE id=$1 AND NOT revoked`, sid, enc, refresh, id.ExpiresAt); e != nil {
		identityError(w, ErrUnavailable)
		return
	}
	g, e := h.Access(ctx, token.AccessToken, id)
	if e != nil {
		// Preserve a newly rotated Cognito credential after an MPD outage, while
		// granting no local access and keeping the same browser cookie retryable.
		if e == ErrDenied {
			_, e = tx.Exec(ctx, `UPDATE mpd_sessions SET revoked=true,refresh_hash=NULL WHERE id=$1`, sid)
			if e != nil {
				identityError(w, ErrUnavailable)
				return
			}
			e = ErrDenied
		}
		failure := e
		if tx.Commit(ctx) != nil {
			identityError(w, ErrUnavailable)
			return
		}
		identityError(w, failure)
		return
	}
	if g.StaffID != s.StaffID || g.TenantID != s.TenantID {
		identityError(w, ErrDenied)
		return
	}
	// A local cookie-write failure must not discard credentials already rotated
	// upstream. The savepoint preserves them without granting local access.
	if _, e = tx.Exec(ctx, `SAVEPOINT local_session_rotation`); e != nil {
		identityError(w, ErrUnavailable)
		return
	}
	preserveRetry := func() {
		if _, err := tx.Exec(ctx, `ROLLBACK TO SAVEPOINT local_session_rotation`); err == nil {
			_ = tx.Commit(ctx)
		}
	}
	js, _ := json.Marshal(g)
	if _, e = tx.Exec(ctx, `UPDATE mpd_sessions SET grant_json=$2,grant_expires_at=$3 WHERE id=$1 AND NOT revoked`, sid, js, g.ValidUntil); e != nil {
		preserveRetry()
		identityError(w, ErrUnavailable)
		return
	}
	result, e := scoped.prepareSession(ctx, sid, true)
	if e != nil {
		if e == ErrUnavailable {
			preserveRetry()
		}
		identityError(w, e)
		return
	}
	if tx.Commit(ctx) != nil {
		identityError(w, ErrUnavailable)
		return
	}
	h.writeSession(w, result)
}
func (h *Handler) Logout(w http.ResponseWriter, r *http.Request) {
	if !h.allowedOrigin(w, r, true) {
		return
	}
	if c, e := r.Cookie("mpd_refresh"); e == nil {
		_, e = h.db.Exec(r.Context(), `UPDATE mpd_sessions SET revoked=true,refresh_hash=NULL WHERE refresh_hash=$1`, hash(c.Value))
		if e != nil {
			identityError(w, ErrUnavailable)
			return
		}
	}
	for name, path := range map[string]string{"mpd_refresh": "/api/auth", "mpd_classification": "/"} {
		http.SetCookie(w, &http.Cookie{Name: name, Path: path, Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	}
	httputil.WriteJSON(w, 200, map[string]string{"message": "Signed out"})
}

func (h *Handler) Info(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	httputil.WriteJSON(w, 200, map[string]bool{"enabled": h.cfg.Enabled})
}
