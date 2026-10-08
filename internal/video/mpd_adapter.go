package video

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/sendrec/sendrec/internal/database"
	"github.com/sendrec/sendrec/internal/httputil"
)

type MPDServiceConfig struct {
	Enabled     bool
	WorkspaceID string
	TenantID    string
	// Hex SHA-256 verifiers, current and optionally previous during rotation.
	TokenHashes     []string
	RequirePassword bool
}

type MPDMetadata struct {
	VideoID           string     `json:"videoId"`
	OwnerStaffID      string     `json:"ownerStaffId"`
	TenantID          string     `json:"tenantId"`
	MediaVersion      int        `json:"mediaVersion"`
	Status            string     `json:"status"`
	Published         bool       `json:"published"`
	PublishedAt       *time.Time `json:"publishedAt"`
	PasswordProtected bool       `json:"passwordProtected"`
	WatchURL          *string    `json:"watchUrl"`
	EmbedURL          *string    `json:"embedUrl"`
}

type MPDAdapter struct {
	h   *Handler
	cfg MPDServiceConfig
}

func (h *Handler) MPDAdapter(cfg MPDServiceConfig) *MPDAdapter { return &MPDAdapter{h: h, cfg: cfg} }
func (a *MPDAdapter) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		sum := sha256.Sum256([]byte(token))
		matched := 0
		for _, hash := range a.cfg.TokenHashes {
			raw, e := hex.DecodeString(hash)
			if e == nil && len(raw) == 32 {
				matched |= subtle.ConstantTimeCompare(raw, sum[:])
			}
		}
		if !a.cfg.Enabled || !ok || len(token) < 32 || matched != 1 {
			httputil.WriteError(w, 401, "integration credential required")
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
func (a *MPDAdapter) metadata(ctx context.Context, db database.DBTX, id string) (MPDMetadata, error) {
	var m MPDMetadata
	var share string
	err := db.QueryRow(ctx, `SELECT s.video_id,s.owner_staff_id,s.tenant_id,s.media_version,
 CASE WHEN s.deleted_at IS NOT NULL THEN 'deleted' WHEN v.status='ready' THEN 'ready' ELSE 'processing' END,
 s.published,s.published_at,COALESCE(v.share_password IS NOT NULL,false),COALESCE(v.share_token,'')
 FROM mpd_video_state s LEFT JOIN videos v ON v.id=s.video_id
 WHERE s.video_id=$1 AND s.tenant_id=$2 AND EXISTS(SELECT 1 FROM mpd_managed_workspaces w WHERE w.organization_id=$3 AND w.tenant_id=s.tenant_id) AND (v.organization_id=$3 OR s.deleted_at IS NOT NULL)`, id, a.cfg.TenantID, a.cfg.WorkspaceID).
		Scan(&m.VideoID, &m.OwnerStaffID, &m.TenantID, &m.MediaVersion, &m.Status, &m.Published, &m.PublishedAt, &m.PasswordProtected, &share)
	if err == nil && m.Published && m.Status != "deleted" {
		watch := a.h.baseURL + "/watch/" + share
		embed := a.h.baseURL + "/embed/" + share
		m.WatchURL = &watch
		m.EmbedURL = &embed
	}
	return m, err
}
func (a *MPDAdapter) Metadata(w http.ResponseWriter, r *http.Request) {
	m, err := a.metadata(r.Context(), a.h.db, chi.URLParam(r, "videoId"))
	if err != nil {
		httputil.WriteError(w, 404, "recording unavailable")
		return
	}
	httputil.WriteJSON(w, 200, m)
}
func (a *MPDAdapter) Transcript(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "videoId")
	var media, generation int
	var segmentsJSON []byte
	err := a.h.db.QueryRow(r.Context(), `SELECT s.media_version,s.transcript_version,v.transcript_json FROM mpd_video_state s JOIN videos v ON v.id=s.video_id WHERE s.video_id=$1 AND s.tenant_id=$2 AND v.organization_id=$3 AND s.deleted_at IS NULL AND v.transcript_status='ready' AND s.transcript_version>0`, id, a.cfg.TenantID, a.cfg.WorkspaceID).Scan(&media, &generation, &segmentsJSON)
	if err != nil {
		httputil.WriteError(w, 409, "transcript unavailable")
		return
	}
	var segments []TranscriptSegment
	if len(segmentsJSON) > 1000000 || json.Unmarshal(segmentsJSON, &segments) != nil {
		httputil.WriteError(w, 409, "transcript unavailable")
		return
	}
	vtt := segmentsToVTT(segments)
	if len(vtt) > 1000000 {
		httputil.WriteError(w, 413, "transcript too large")
		return
	}
	httputil.WriteJSON(w, 200, map[string]any{"videoId": id, "mediaVersion": media, "transcriptVersion": generation, "vtt": vtt})
}
func (a *MPDAdapter) Password(w http.ResponseWriter, r *http.Request) { a.mutate(w, r, "password") }
func (a *MPDAdapter) Publication(w http.ResponseWriter, r *http.Request) {
	a.mutate(w, r, "publication")
}
func (a *MPDAdapter) mutate(w http.ResponseWriter, r *http.Request, operation string) {
	var request struct {
		MediaVersion int     `json:"mediaVersion"`
		Password     *string `json:"password,omitempty"`
		Published    *bool   `json:"published,omitempty"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	dec.DisallowUnknownFields()
	if dec.Decode(&request) != nil || request.MediaVersion < 1 || dec.Decode(new(any)) != io.EOF {
		httputil.WriteError(w, 400, "invalid mutation")
		return
	}
	if operation == "publication" && request.Published == nil || operation == "password" && request.Published != nil || request.Password != nil && (len(*request.Password) == 0 || len(*request.Password) > 72) {
		httputil.WriteError(w, 400, "invalid mutation")
		return
	}
	work, err := strconv.ParseInt(r.Header.Get("X-MPD-Work-Version"), 10, 64)
	key := r.Header.Get("Idempotency-Key")
	if err != nil || work <= 0 || len(key) == 0 || len(key) > 200 {
		httputil.WriteError(w, 400, "mutation identity required")
		return
	}
	body, _ := json.Marshal(request)
	digest := hmac.New(sha256.New, deriveCookieKey(a.h.hmacSecret, "mpd-receipt"))
	digest.Write(body)
	hash := hex.EncodeToString(digest.Sum(nil))
	id := chi.URLParam(r, "videoId")
	beginner, ok := a.h.db.(interface {
		Begin(context.Context) (pgx.Tx, error)
	})
	if !ok {
		httputil.WriteError(w, 503, "transaction unavailable")
		return
	}
	tx, err := beginner.Begin(r.Context())
	if err != nil {
		httputil.WriteError(w, 503, "transaction unavailable")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	var currentWork int64
	var media int
	err = tx.QueryRow(r.Context(), `SELECT s.work_version,s.media_version FROM mpd_video_state s JOIN videos v ON v.id=s.video_id WHERE s.video_id=$1 AND s.tenant_id=$2 AND v.organization_id=$3 AND s.deleted_at IS NULL FOR UPDATE OF s,v`, id, a.cfg.TenantID, a.cfg.WorkspaceID).Scan(&currentWork, &media)
	if err != nil {
		httputil.WriteError(w, 404, "recording unavailable")
		return
	}
	// An old worker must not mutate, even if its HTTP request was delayed in transit.
	if work < currentWork || media != request.MediaVersion {
		httputil.WriteError(w, 409, "stale recording work")
		return
	}
	var oldHash string
	var receipt []byte
	err = tx.QueryRow(r.Context(), `SELECT request_hash,response FROM mpd_adapter_receipts WHERE video_id=$1 AND operation=$2 AND effect_key=$3`, id, operation, key).Scan(&oldHash, &receipt)
	if err == nil {
		if oldHash != hash {
			httputil.WriteError(w, 409, "idempotency conflict")
			return
		}
		// Advancing the work fence never reapplies the original side effect.
		if _, err = tx.Exec(r.Context(), `UPDATE mpd_video_state SET work_version=$2 WHERE video_id=$1`, id, work); err == nil {
			err = tx.Commit(r.Context())
		}
		if err != nil {
			httputil.WriteError(w, 503, "transaction unavailable")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(receipt)
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		httputil.WriteError(w, 503, "transaction unavailable")
		return
	}
	var result any
	if operation == "password" {
		var encoded *string
		if request.Password != nil {
			value, e := hashSharePassword(*request.Password)
			if e != nil {
				httputil.WriteError(w, 503, "password unavailable")
				return
			}
			encoded = &value
		}
		_, err = tx.Exec(r.Context(), `UPDATE videos SET share_password=$2,updated_at=now() WHERE id=$1`, id, encoded)
		result = map[string]any{"mediaVersion": media, "passwordProtected": encoded != nil}
	} else {
		var ready, protected bool
		if err = tx.QueryRow(r.Context(), `SELECT status='ready',share_password IS NOT NULL FROM videos WHERE id=$1`, id).Scan(&ready, &protected); err == nil {
			if *request.Published && (!ready || a.cfg.RequirePassword && !protected) {
				httputil.WriteError(w, 409, "recording must be ready and protected before publication")
				return
			}
			_, err = tx.Exec(r.Context(), `UPDATE mpd_video_state SET published=$2,published_at=CASE WHEN $2 THEN COALESCE(published_at,now()) ELSE NULL END WHERE video_id=$1`, id, *request.Published)
		}
		if err == nil {
			result, err = a.metadata(r.Context(), tx, id)
		}
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE mpd_video_state SET work_version=$2 WHERE video_id=$1`, id, work)
	}
	if err == nil {
		receipt, err = json.Marshal(result)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO mpd_adapter_receipts(video_id,operation,effect_key,request_hash,response) VALUES($1,$2,$3,$4,$5)`, id, operation, key, hash, receipt)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		httputil.WriteError(w, 503, "transaction unavailable")
		return
	}
	httputil.WriteJSON(w, 200, result)
}
