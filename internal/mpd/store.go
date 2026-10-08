package mpd

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sendrec/sendrec/internal/integration"
)

type beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

func randomSecret() (string, error) {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func hash(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }

func (h *Handler) provision(ctx context.Context, id *Identity, g *Grant) (*Principal, error) {
	b, ok := h.db.(beginner)
	if !ok {
		return nil, ErrUnavailable
	}
	tx, err := b.Begin(ctx)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var org string
	var limit int
	// Lock the workspace to serialize seat accounting and concurrent first logins.
	err = tx.QueryRow(ctx, `SELECT organization_id,seat_limit FROM mpd_managed_workspaces WHERE tenant_id=$1 AND issuer=$2 AND enabled FOR UPDATE`, g.TenantID, id.Issuer).Scan(&org, &limit)
	if err != nil {
		return nil, ErrDenied
	}
	var user, staff, tenant, linkedOrg string
	err = tx.QueryRow(ctx, `SELECT user_id,staff_id,tenant_id,organization_id FROM mpd_external_identities WHERE issuer=$1 AND subject=$2`, id.Issuer, id.Subject).Scan(&user, &staff, &tenant, &linkedOrg)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrUnavailable
	}
	if errors.Is(err, pgx.ErrNoRows) {
		if id.Email == "" || len(id.Email) > 320 {
			return nil, ErrDenied
		}
		var collision bool
		if tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE lower(email)=lower($1))`, id.Email).Scan(&collision) != nil {
			return nil, ErrUnavailable
		}
		if collision {
			return nil, ErrLinkRequired
		}
		var seats int
		if tx.QueryRow(ctx, `SELECT count(*) FROM mpd_external_identities WHERE organization_id=$1`, org).Scan(&seats) != nil {
			return nil, ErrUnavailable
		}
		if seats >= limit {
			return nil, ErrCapacity
		}
		name := strings.TrimSpace(id.Name)
		if name == "" {
			name = "MPD staff"
		}
		if len(name) > 100 {
			name = "MPD staff"
		}
		if tx.QueryRow(ctx, `INSERT INTO users(email,password,name,email_verified,retention_days) VALUES($1,'',$2,true,0) RETURNING id`, id.Email, name).Scan(&user) != nil {
			return nil, ErrUnavailable
		}
		if _, err = tx.Exec(ctx, `INSERT INTO mpd_external_identities(issuer,subject,user_id,organization_id,staff_id,tenant_id) VALUES($1,$2,$3,$4,$5,$6)`, id.Issuer, id.Subject, user, org, g.StaffID, g.TenantID); err != nil {
			return nil, ErrDenied
		}
	} else if staff != g.StaffID || tenant != g.TenantID || linkedOrg != org {
		return nil, ErrDenied
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('sendrec.mpd_provision','1',true)`); err != nil {
		return nil, ErrUnavailable
	}
	if _, err = tx.Exec(ctx, `INSERT INTO organization_members(organization_id,user_id,role) VALUES($1,$2,$3) ON CONFLICT(organization_id,user_id) DO UPDATE SET role=EXCLUDED.role`, org, user, g.Capabilities.Role()); err != nil {
		return nil, ErrUnavailable
	}
	if _, err = tx.Exec(ctx, `INSERT INTO mpd_identity_audit(actor,action,user_id,organization_id) VALUES('cognito','authorized_session',$1,$2)`, user, org); err != nil {
		return nil, ErrUnavailable
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, ErrUnavailable
	}
	return &Principal{UserID: user, OrganizationID: org, StaffID: g.StaffID, TenantID: g.TenantID, Capabilities: g.Capabilities}, nil
}

type session struct {
	Principal
	AccessEncrypted, RefreshEncrypted  string
	CognitoExpiry, Expiry, GrantExpiry time.Time
	Grant                              Grant
}

func (h *Handler) createSession(ctx context.Context, p *Principal, id *Identity, g *Grant, access, refresh string) (string, error) {
	a, err := integration.Encrypt(h.key, access)
	if err != nil {
		return "", err
	}
	enc := ""
	if refresh != "" {
		enc, err = integration.Encrypt(h.key, refresh)
		if err != nil {
			return "", err
		}
	}
	classification, err := randomSecret()
	if err != nil {
		return "", err
	}
	expires := id.ExpiresAt
	if refresh != "" {
		expires = time.Now().Add(7 * 24 * time.Hour)
	}
	js, _ := json.Marshal(g)
	var sid string
	err = h.db.QueryRow(ctx, `INSERT INTO mpd_sessions(user_id,access_encrypted,refresh_encrypted,classification_hash,cognito_expires_at,expires_at,grant_json,grant_expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id`, p.UserID, a, enc, hash(classification), id.ExpiresAt, expires, js, g.ValidUntil).Scan(&sid)
	return sid, err
}
func (h *Handler) loadSession(ctx context.Context, id string) (*session, error) {
	s := &session{}
	var js []byte
	err := h.db.QueryRow(ctx, `SELECT s.id,s.user_id,i.organization_id,i.staff_id,i.tenant_id,s.access_encrypted,s.refresh_encrypted,s.cognito_expires_at,s.expires_at,s.grant_json,s.grant_expires_at FROM mpd_sessions s JOIN mpd_external_identities i ON i.user_id=s.user_id JOIN mpd_managed_workspaces w ON w.organization_id=i.organization_id WHERE s.id=$1 AND NOT s.revoked AND w.enabled AND w.issuer=$2 AND s.expires_at>now()`, id, h.cfg.Issuer).Scan(&s.SessionID, &s.UserID, &s.OrganizationID, &s.StaffID, &s.TenantID, &s.AccessEncrypted, &s.RefreshEncrypted, &s.CognitoExpiry, &s.Expiry, &js, &s.GrantExpiry)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrDenied
		}
		return nil, ErrUnavailable
	}
	if json.Unmarshal(js, &s.Grant) != nil {
		return nil, ErrDenied
	}
	s.Capabilities = s.Grant.Capabilities
	return s, nil
}
func (h *Handler) validateSession(ctx context.Context, sid string) (*session, error) {
	if !h.cfg.Enabled || !validUUID(sid) {
		return nil, ErrDenied
	}
	s, err := h.loadSession(ctx, sid)
	if err != nil {
		return nil, err
	}
	if !s.CognitoExpiry.After(time.Now()) {
		return nil, ErrDenied
	}
	if s.GrantExpiry.After(time.Now()) && s.GrantExpiry.Before(time.Now().Add(31*time.Second)) {
		return s, nil
	}
	raw, err := integration.Decrypt(h.key, s.AccessEncrypted)
	if err != nil {
		return nil, ErrDenied
	}
	id, err := h.VerifyAccess(ctx, raw)
	if err != nil {
		return nil, err
	}
	g, err := h.Access(ctx, raw, id)
	if err != nil {
		return nil, err
	}
	if g.StaffID != s.StaffID || g.TenantID != s.TenantID {
		return nil, ErrDenied
	}
	js, _ := json.Marshal(g)
	if _, err = h.db.Exec(ctx, `UPDATE mpd_sessions SET grant_json=$2,grant_expires_at=$3 WHERE id=$1 AND NOT revoked`, sid, js, g.ValidUntil); err != nil {
		return nil, ErrUnavailable
	}
	s.Grant = *g
	s.GrantExpiry = g.ValidUntil
	s.Capabilities = g.Capabilities
	return s, nil
}
func (h *Handler) ValidateClassification(ctx context.Context, raw string) (*Principal, error) {
	if len(raw) != 43 {
		return nil, ErrDenied
	}
	var sid string
	if h.db.QueryRow(ctx, `SELECT id FROM mpd_sessions WHERE classification_hash=$1 AND NOT revoked AND expires_at>now() AND classification_expires_at>now()`, hash(raw)).Scan(&sid) != nil {
		return nil, ErrDenied
	}
	s, err := h.validateSession(ctx, sid)
	if err != nil {
		return nil, err
	}
	return &s.Principal, nil
}
