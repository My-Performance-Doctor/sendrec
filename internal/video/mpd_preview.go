package video

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/sendrec/sendrec/internal/httputil"
	"github.com/sendrec/sendrec/internal/mpd"
	"github.com/sendrec/sendrec/internal/mpdevents"
)

type MPDSessionValidator interface {
	ValidateSession(context.Context, string) (*mpd.Principal, error)
}

type MPDMedia struct {
	h        *Handler
	Identity MPDSessionValidator
	Config   MPDServiceConfig
	Origins  []string
}

func (h *Handler) MPDMedia(identity MPDSessionValidator, cfg MPDServiceConfig, origins []string) *MPDMedia {
	return &MPDMedia{h, identity, cfg, origins}
}
func tokenDigest(s string) string { x := sha256.Sum256([]byte(s)); return hex.EncodeToString(x[:]) }
func randomProof() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("secure randomness unavailable")
	}
	return hex.EncodeToString(b[:])
}
func (m *MPDMedia) video(ctx context.Context, id string, p *mpd.Principal, write bool) (MPDMetadata, error) {
	if p == nil || p.OrganizationID != m.Config.WorkspaceID || p.TenantID != m.Config.TenantID {
		return MPDMetadata{}, mpd.ErrDenied
	}
	result, err := m.h.MPDAdapter(m.Config).metadata(ctx, m.h.db, id)
	if err != nil || result.Status == "deleted" {
		return result, mpd.ErrDenied
	}
	if write {
		if !p.Capabilities.ManageWorkspace && (!p.Capabilities.ManageOwn || result.OwnerStaffID != p.StaffID) {
			return result, mpd.ErrDenied
		}
	} else if !p.Capabilities.Read {
		return result, mpd.ErrDenied
	}
	return result, nil
}
func (m *MPDMedia) Info(w http.ResponseWriter, r *http.Request) {
	p, ok := mpd.PrincipalFromContext(r.Context())
	if !ok {
		httputil.WriteJSON(w, 200, map[string]any{"managed": false})
		return
	}
	result, err := m.video(r.Context(), chi.URLParam(r, "id"), p, false)
	if err != nil {
		httputil.WriteError(w, 403, "recording unavailable")
		return
	}
	httputil.WriteJSON(w, 200, map[string]any{"managed": true, "metadata": result})
}
func (m *MPDMedia) CreatePreview(w http.ResponseWriter, r *http.Request) {
	p, _ := mpd.PrincipalFromContext(r.Context())
	id := chi.URLParam(r, "id")
	metadata, err := m.video(r.Context(), id, p, false)
	if err != nil || metadata.Status != "ready" {
		httputil.WriteError(w, 403, "preview unavailable")
		return
	}
	var input struct {
		Origin string `json:"origin"`
		Nonce  string `json:"nonce"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&input) != nil || len(input.Nonce) < 16 || len(input.Nonce) > 128 {
		httputil.WriteError(w, 400, "preview origin and nonce required")
		return
	}
	allowed := input.Origin == m.h.baseURL
	for _, origin := range m.Origins {
		allowed = allowed || origin == input.Origin
	}
	if !allowed {
		httputil.WriteError(w, 403, "preview origin denied")
		return
	}
	proof := randomProof()
	_, err = m.h.db.Exec(r.Context(), `INSERT INTO mpd_preview_handoffs(token_hash,video_id,media_version,session_id,origin,nonce,expires_at) VALUES($1,$2,$3,$4,$5,$6,now()+interval '60 seconds')`, tokenDigest(proof), id, metadata.MediaVersion, p.SessionID, input.Origin, input.Nonce)
	if err != nil {
		httputil.WriteError(w, 503, "preview unavailable")
		return
	}
	fragment := url.Values{"handoff": {proof}, "nonce": {input.Nonce}, "parentOrigin": {input.Origin}}.Encode()
	w.Header().Set("Cache-Control", "no-store")
	httputil.WriteJSON(w, 201, map[string]any{"previewUrl": m.h.baseURL + "/mpd-preview#" + fragment, "handoff": proof, "nonce": input.Nonce})
}
func (m *MPDMedia) RedeemPreview(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != m.h.baseURL {
		httputil.WriteError(w, 403, "preview origin denied")
		return
	}
	var input struct {
		Handoff      string `json:"handoff"`
		Nonce        string `json:"nonce"`
		ParentOrigin string `json:"parentOrigin"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&input) != nil {
		httputil.WriteError(w, 400, "invalid preview")
		return
	}
	var id, sid string
	var version int
	err := m.h.db.QueryRow(r.Context(), `UPDATE mpd_preview_handoffs SET used_at=now() WHERE token_hash=$1 AND nonce=$2 AND origin=$3 AND used_at IS NULL AND expires_at>now() RETURNING video_id,media_version,session_id`, tokenDigest(input.Handoff), input.Nonce, input.ParentOrigin).Scan(&id, &version, &sid)
	if err != nil {
		httputil.WriteError(w, 403, "preview expired")
		return
	}
	p, err := m.Identity.ValidateSession(r.Context(), sid)
	if err != nil {
		httputil.WriteError(w, 403, "preview access ended")
		return
	}
	metadata, err := m.video(r.Context(), id, p, false)
	if err != nil || metadata.MediaVersion != version {
		httputil.WriteError(w, 403, "preview changed")
		return
	}
	proof := randomProof()
	_, err = m.h.db.Exec(r.Context(), `INSERT INTO mpd_preview_sessions(token_hash,video_id,media_version,session_id,expires_at) VALUES($1,$2,$3,$4,now()+interval '5 minutes')`, tokenDigest(proof), id, version, sid)
	if err != nil {
		httputil.WriteError(w, 503, "preview unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httputil.WriteJSON(w, 200, map[string]any{"previewToken": proof, "videoId": id})
}
func (m *MPDMedia) preview(r *http.Request) (string, int, error) {
	proof, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return "", 0, mpd.ErrDenied
	}
	var id, sid string
	var version int
	err := m.h.db.QueryRow(r.Context(), `SELECT video_id,media_version,session_id FROM mpd_preview_sessions WHERE token_hash=$1 AND expires_at>now()`, tokenDigest(proof)).Scan(&id, &version, &sid)
	if err != nil {
		return "", 0, mpd.ErrDenied
	}
	p, err := m.Identity.ValidateSession(r.Context(), sid)
	if err != nil {
		return "", 0, err
	}
	current, err := m.video(r.Context(), id, p, false)
	if err != nil || current.MediaVersion != version {
		return "", 0, mpd.ErrDenied
	}
	// Rolling preview expiry never extends authorization: each use revalidates MPD.
	_, err = m.h.db.Exec(r.Context(), `UPDATE mpd_preview_sessions SET expires_at=now()+interval '5 minutes' WHERE token_hash=$1`, tokenDigest(proof))
	return id, version, err
}
func (m *MPDMedia) PreviewMedia(w http.ResponseWriter, r *http.Request) {
	id, _, err := m.preview(r)
	if err != nil {
		httputil.WriteError(w, 403, "preview access ended")
		return
	}
	var key string
	var transcript *string
	if m.h.db.QueryRow(r.Context(), `SELECT file_key,transcript_key FROM videos WHERE id=$1 AND status='ready'`, id).Scan(&key, &transcript) != nil {
		httputil.WriteError(w, 409, "recording not ready")
		return
	}
	media, err := m.h.storage.GenerateDownloadURL(r.Context(), key, 5*time.Minute)
	if err != nil {
		httputil.WriteError(w, 503, "media unavailable")
		return
	}
	var captions string
	if transcript != nil {
		captions, _ = m.h.storage.GenerateDownloadURL(r.Context(), *transcript, 5*time.Minute)
	}
	w.Header().Set("Cache-Control", "no-store")
	httputil.WriteJSON(w, 200, map[string]any{"videoUrl": media, "transcriptUrl": captions, "expiresAt": time.Now().Add(5 * time.Minute)})
}
func (m *MPDMedia) PreviewPlayback(w http.ResponseWriter, r *http.Request) {
	id, _, err := m.preview(r)
	if err != nil {
		httputil.WriteError(w, 403, "preview access ended")
		return
	}
	if strings.HasSuffix(r.URL.Path, "/session") {
		s, err := mpdevents.NewSession(r.Context(), m.h.db, id, true)
		if err != nil {
			httputil.WriteError(w, 403, "preview unavailable")
			return
		}
		httputil.WriteJSON(w, 201, s)
		return
	}
	var p mpdevents.Progress
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&p) != nil || mpdevents.Accept(r.Context(), m.h.db, id, p) != nil {
		httputil.WriteError(w, 403, "invalid playback observation")
		return
	}
	w.WriteHeader(204)
}
func (m *MPDMedia) Publish(w http.ResponseWriter, r *http.Request) {
	p, _ := mpd.PrincipalFromContext(r.Context())
	id := chi.URLParam(r, "id")
	if _, err := m.video(r.Context(), id, p, true); err != nil {
		httputil.WriteError(w, 403, "publication denied")
		return
	}
	var input struct {
		Published    bool `json:"published"`
		MediaVersion int  `json:"mediaVersion"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&input) != nil || input.MediaVersion <= 0 {
		httputil.WriteError(w, 400, "invalid publication")
		return
	}
	db, ok := m.h.db.(interface {
		Begin(context.Context) (pgx.Tx, error)
	})
	if !ok {
		httputil.WriteError(w, 503, "transaction unavailable")
		return
	}
	tx, err := db.Begin(r.Context())
	if err != nil {
		httputil.WriteError(w, 503, "transaction unavailable")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	var version int
	var protected, ready bool
	err = tx.QueryRow(r.Context(), `SELECT s.media_version,v.share_password IS NOT NULL,v.status='ready' FROM mpd_video_state s JOIN videos v ON v.id=s.video_id WHERE s.video_id=$1 AND s.deleted_at IS NULL FOR UPDATE OF s,v`, id).Scan(&version, &protected, &ready)
	if err != nil || version != input.MediaVersion || input.Published && (!ready || m.Config.RequirePassword && !protected) {
		httputil.WriteError(w, 409, "set a password and wait for processing before publishing")
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE mpd_video_state SET published=$2,published_at=CASE WHEN $2 THEN COALESCE(published_at,now()) ELSE NULL END WHERE video_id=$1`, id, input.Published)
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		httputil.WriteError(w, 503, "publication unavailable")
		return
	}
	httputil.WriteJSON(w, 200, map[string]any{"published": input.Published, "mediaVersion": version})
}
