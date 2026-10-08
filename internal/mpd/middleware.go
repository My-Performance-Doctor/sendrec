package mpd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/sendrec/sendrec/internal/auth"
	"github.com/sendrec/sendrec/internal/httputil"
)

func (h *Handler) ValidateSession(ctx context.Context, sid string) (*Principal, error) {
	s, e := h.validateSession(ctx, sid)
	if e != nil {
		return nil, e
	}
	return &s.Principal, nil
}

// Guard also runs for legacy JWTs and API keys. Persisted managed identities
// cannot regain personal access when the integration is disabled.
func (h *Handler) Guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := auth.UserIDFromContext(r.Context())
		sid := auth.ManagedSessionFromContext(r.Context())
		if sid == "" {
			var managed bool
			org := r.Header.Get("X-Organization-Id")
			if !validUUID(org) {
				org = "00000000-0000-0000-0000-000000000000"
			}
			e := h.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM mpd_external_identities WHERE user_id=$1) OR EXISTS(SELECT 1 FROM mpd_managed_workspaces WHERE organization_id=$2)`, user, org).Scan(&managed)
			if e != nil {
				identityError(w, ErrUnavailable)
				return
			}
			if managed {
				identityError(w, ErrDenied)
				return
			}
			// Administrative paths use a path parameter instead of the organization header.
			path := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
			if len(path) >= 3 && path[0] == "api" && path[1] == "organizations" && validUUID(path[2]) {
				if h.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM mpd_managed_workspaces WHERE organization_id=$1)`, path[2]).Scan(&managed) != nil {
					identityError(w, ErrUnavailable)
					return
				}
				if managed {
					httputil.WriteError(w, 403, "managed workspace is deployment controlled")
					return
				}
			}
			next.ServeHTTP(w, r)
			return
		}
		p, e := h.ValidateSession(r.Context(), sid)
		if e != nil {
			identityError(w, e)
			return
		}
		if p.UserID != user {
			identityError(w, ErrDenied)
			return
		}
		if org := r.Header.Get("X-Organization-Id"); org != "" && org != p.OrganizationID {
			httputil.WriteError(w, 403, "managed workspace mismatch")
			return
		}
		if !h.authorize(r, p) {
			httputil.WriteError(w, 403, "MPD capability does not permit this action")
			return
		}
		r.Header.Set("X-Organization-Id", p.OrganizationID)
		ctx := ContextWithPrincipal(r.Context(), p)
		ctx = auth.ContextWithOrg(ctx, p.OrganizationID, p.Capabilities.Role())
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (h *Handler) authorize(r *http.Request, p *Principal) bool {
	path := strings.TrimRight(r.URL.Path, "/")
	read := r.Method == http.MethodGet
	c := p.Capabilities
	if path == "/api/user" {
		if read {
			return true
		}
		if r.Method != "PATCH" {
			return false
		}
		fields, ok := readObject(r)
		if !ok || len(fields) != 1 {
			return false
		}
		_, ok = fields["name"]
		return ok
	}
	if path == "/api/organizations" {
		return read
	}
	if strings.HasPrefix(path, "/api/organizations/") {
		return read && path == "/api/organizations/"+p.OrganizationID
	}
	if strings.HasPrefix(path, "/api/settings/") {
		return read && c.Read && strings.HasSuffix(path, "/branding")
	}
	if strings.HasPrefix(path, "/api/analytics/") {
		return read && c.Read
	}
	if path == "/api/videos" || path == "/api/videos/upload" {
		if read {
			return c.Read
		}
		return r.Method == "POST" && c.Record
	}
	if path == "/api/videos/limits" {
		return read && (c.Record || c.Read)
	}
	if strings.HasPrefix(path, "/api/videos/batch/") {
		if r.Method != "POST" || (!c.ManageOwn && !c.ManageWorkspace) {
			return false
		}
		fields, ok := readObject(r)
		if !ok {
			return false
		}
		var ids []string
		if json.Unmarshal(fields["videoIds"], &ids) != nil || len(ids) == 0 || len(ids) > 100 {
			return false
		}
		for _, id := range ids {
			if !validUUID(id) {
				return false
			}
			var owner, org string
			if h.db.QueryRow(r.Context(), `SELECT user_id,coalesce(organization_id::text,'') FROM videos WHERE id=$1`, id).Scan(&owner, &org) != nil || org != p.OrganizationID || (!c.ManageWorkspace && owner != p.UserID) {
				return false
			}
		}
		return path == "/api/videos/batch/delete" || path == "/api/videos/batch/folder" || path == "/api/videos/batch/tags"
	}
	if strings.HasPrefix(path, "/api/videos/") {
		parts := strings.Split(strings.TrimPrefix(path, "/api/videos/"), "/")
		if !validUUID(parts[0]) {
			return false
		}
		var owner, org, status string
		if h.db.QueryRow(r.Context(), `SELECT user_id,coalesce(organization_id::text,''),status FROM videos WHERE id=$1`, parts[0]).Scan(&owner, &org, &status) != nil || org != p.OrganizationID {
			return false
		}
		suffix := ""
		if len(parts) > 1 {
			suffix = strings.Join(parts[1:], "/")
		}
		if suffix == "transfer" {
			return false
		}
		if read || suffix == "preview" {
			return c.Read
		}
		if suffix == "upload-url" {
			return owner == p.UserID && c.Record && status == "uploading"
		}
		// Finishing the caller's pending upload is part of record, not media management.
		if suffix == "" && r.Method == "PATCH" && (status == "uploading" || status == "ready" || status == "processing") && owner == p.UserID && c.Record {
			b, e := io.ReadAll(io.LimitReader(r.Body, 65537))
			r.Body = io.NopCloser(bytes.NewReader(b))
			if e != nil || len(b) > 65536 {
				return false
			}
			var fields map[string]json.RawMessage
			if json.Unmarshal(b, &fields) == nil && len(fields) == 1 {
				var s string
				if json.Unmarshal(fields["status"], &s) == nil && s == "ready" {
					return true
				}
			}
		}
		return c.ManageWorkspace || owner == p.UserID && c.ManageOwn
	}
	for _, prefix := range []string{"/api/folders", "/api/tags", "/api/playlists"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return read && c.Read || !read && c.ManageWorkspace
		}
	}
	return false
}

// GuardSCIM prevents even previously issued SCIM tokens from touching the workspace.
func (h *Handler) GuardSCIM(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) < 3 || !validUUID(parts[2]) {
			httputil.WriteError(w, 403, "SCIM unavailable")
			return
		}
		var managed bool
		if h.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM mpd_managed_workspaces WHERE organization_id=$1)`, parts[2]).Scan(&managed) != nil {
			identityError(w, ErrUnavailable)
			return
		}
		if managed {
			httputil.WriteError(w, 403, "managed provisioning is deployment controlled")
			return
		}
		if len(parts) > 5 && validUUID(parts[5]) {
			if h.db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM mpd_external_identities WHERE user_id=$1)`, parts[5]).Scan(&managed) != nil {
				identityError(w, ErrUnavailable)
				return
			}
			if managed {
				httputil.WriteError(w, 403, "managed provisioning is deployment controlled")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func readObject(r *http.Request) (map[string]json.RawMessage, bool) {
	b, e := io.ReadAll(io.LimitReader(r.Body, 65537))
	r.Body = io.NopCloser(bytes.NewReader(b))
	if e != nil || len(b) > 65536 {
		return nil, false
	}
	var fields map[string]json.RawMessage
	e = json.Unmarshal(b, &fields)
	return fields, e == nil && fields != nil
}

// CORS permits only explicitly approved future clients. They send a short
// bearer session and receive no cross-site refresh cookie or credentialed CORS.
func (h *Handler) CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if h.cfg.Enabled && origin != "" && contains(h.cfg.AllowedOrigins, origin) && strings.HasPrefix(r.URL.Path, "/api/") {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Add("Vary", "Origin")
			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Organization-Id")
				w.Header().Set("Access-Control-Max-Age", "300")
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
