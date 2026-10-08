package mpd

import (
	"encoding/json"
	"github.com/jackc/pgx/v5/pgtype"
	"io"
	"time"
)

func validUUID(v string) bool { var u pgtype.UUID; return u.Scan(v) == nil && u.Valid }
func decodeGrant(body io.Reader, now, tokenExpiry time.Time) (*Grant, error) {
	var g Grant
	d := json.NewDecoder(io.LimitReader(body, 16385))
	if d.Decode(&g) != nil || g.SchemaVersion != 1 || !validUUID(g.StaffID) || !validUUID(g.TenantID) || !g.ValidUntil.After(now) || !g.Capabilities.Any() {
		return nil, ErrDenied
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return nil, ErrDenied
	}
	if g.ValidUntil.After(now.Add(30 * time.Second)) {
		g.ValidUntil = now.Add(30 * time.Second)
	}
	if g.ValidUntil.After(tokenExpiry) {
		g.ValidUntil = tokenExpiry
	}
	if !g.ValidUntil.After(now) {
		return nil, ErrDenied
	}
	return &g, nil
}
