// Package mpd implements the deployment-controlled MPD identity boundary.
package mpd

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/sendrec/sendrec/internal/database"
	"github.com/sendrec/sendrec/internal/integration"
	"golang.org/x/oauth2"
)

type Capabilities struct {
	Record          bool `json:"record"`
	Read            bool `json:"read"`
	ManageOwn       bool `json:"manageOwn"`
	ManageWorkspace bool `json:"manageWorkspace"`
	ReadPassword    bool `json:"readPassword"`
}
type Grant struct {
	SchemaVersion int          `json:"schemaVersion"`
	StaffID       string       `json:"staffId"`
	TenantID      string       `json:"tenantId"`
	Capabilities  Capabilities `json:"capabilities"`
	ValidUntil    time.Time    `json:"validUntil"`
}
type Principal struct {
	UserID, OrganizationID, StaffID, TenantID, SessionID string
	Capabilities                                         Capabilities
}
type principalKey struct{}

func PrincipalFromContext(ctx context.Context) (*Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(*Principal)
	return p, ok
}
func ContextWithPrincipal(ctx context.Context, p *Principal) context.Context {
	return context.WithValue(ctx, principalKey{}, p)
}
func (c Capabilities) Role() string {
	if c.ManageWorkspace {
		return "admin"
	}
	if c.Record || c.ManageOwn {
		return "member"
	}
	return "viewer"
}
func (c Capabilities) Any() bool {
	return c.Record || c.Read || c.ManageOwn || c.ManageWorkspace || c.ReadPassword
}

type Config struct {
	Enabled                                                                         bool
	Issuer, ClientID, ClientSecret, AccessURL, BaseURL, JWTSecret, EncryptionSecret string
	AllowedClientIDs, AllowedOrigins                                                []string
}
type Identity struct {
	Issuer, Subject, Email, Name string
	ExpiresAt                    time.Time
}
type Handler struct {
	db         database.DBTX
	cfg        Config
	key        []byte
	client     *http.Client
	verifier   *oidc.IDTokenVerifier
	idVerifier *oidc.IDTokenVerifier
	provider   *oidc.Provider
	oauth      oauth2.Config
}

var ErrDenied = errors.New("MPD authorization denied")
var ErrUnavailable = errors.New("MPD authorization unavailable")
var ErrLinkRequired = errors.New("explicit operator identity link required")
var ErrCapacity = errors.New("managed workspace seat capacity reached")

func NewHandler(ctx context.Context, db database.DBTX, cfg Config) (*Handler, error) {
	h := &Handler{db: db, cfg: cfg, client: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if !cfg.Enabled {
		return h, nil
	}
	for _, raw := range []string{cfg.Issuer, cfg.AccessURL, cfg.BaseURL} {
		u, e := url.Parse(raw)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return nil, errors.New("MPD endpoints must be fixed HTTPS URLs")
		}
	}
	if cfg.ClientID == "" || cfg.ClientSecret == "" || len(cfg.EncryptionSecret) < 32 || len(cfg.JWTSecret) < 32 || len(cfg.AllowedClientIDs) == 0 {
		return nil, errors.New("MPD identity configuration incomplete")
	}
	if !contains(cfg.AllowedClientIDs, cfg.ClientID) {
		return nil, errors.New("native MPD client is not approved")
	}
	for _, raw := range cfg.AllowedOrigins {
		u, e := url.Parse(raw)
		if e != nil || u.Scheme != "https" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
			return nil, errors.New("MPD origins must be exact HTTPS origins")
		}
	}
	h.key = integration.DeriveKey(cfg.EncryptionSecret)
	ctx = oidc.ClientContext(ctx, h.client)
	p, err := oidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		return nil, errors.New("MPD OIDC discovery unavailable")
	}
	h.provider = p
	h.verifier = p.Verifier(&oidc.Config{SkipClientIDCheck: true, SupportedSigningAlgs: []string{"RS256"}})
	h.idVerifier = p.Verifier(&oidc.Config{ClientID: cfg.ClientID, SupportedSigningAlgs: []string{"RS256"}})
	h.oauth = oauth2.Config{ClientID: cfg.ClientID, ClientSecret: cfg.ClientSecret, RedirectURL: strings.TrimRight(cfg.BaseURL, "/") + "/api/auth/mpd/callback", Endpoint: p.Endpoint(), Scopes: []string{"openid", "email", "profile"}}
	return h, nil
}
func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
func (h *Handler) VerifyAccess(ctx context.Context, raw string) (*Identity, error) {
	if !h.cfg.Enabled || h.verifier == nil {
		return nil, ErrDenied
	}
	token, err := h.verifier.Verify(oidc.ClientContext(ctx, h.client), raw)
	if err != nil {
		return nil, ErrDenied
	}
	var c struct {
		ClientID string `json:"client_id"`
		TokenUse string `json:"token_use"`
	}
	if token.Claims(&c) != nil || c.TokenUse != "access" || !contains(h.cfg.AllowedClientIDs, c.ClientID) || token.Subject == "" || token.Issuer != h.cfg.Issuer || !token.Expiry.After(time.Now()) {
		return nil, ErrDenied
	}
	return &Identity{Issuer: token.Issuer, Subject: token.Subject, ExpiresAt: token.Expiry}, nil
}
func (h *Handler) Access(ctx context.Context, raw string, id *Identity) (*Grant, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.cfg.AccessURL, nil)
	if err != nil {
		return nil, ErrUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+raw)
	resp, err := h.client.Do(req)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, ErrDenied
	}
	if resp.StatusCode != 200 {
		return nil, ErrUnavailable
	}
	g, err := decodeGrant(resp.Body, time.Now(), id.ExpiresAt)
	return g, err
}
