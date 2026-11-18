package video

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/sendrec/sendrec/internal/database"
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
	if cfg.ClinicZone == nil {
		// cmd/sendrec always sets it; a handler built without it, as in tests, uses the default.
		zone, err := time.LoadLocation(DefaultClinicZone)
		if err != nil {
			panic("zone database unavailable: " + err.Error())
		}
		cfg.ClinicZone = zone
	}
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
	// The capability is checked before the recording is looked up, so staff who may not read or manage
	// recordings learn nothing about which ones exist.
	if write && !p.Capabilities.ManageWorkspace && !p.Capabilities.ManageOwn || !write && !p.Capabilities.Read {
		return MPDMetadata{}, mpd.ErrDenied
	}
	result, err := m.h.MPDAdapter(m.Config).metadata(ctx, m.h.db, id)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && result.Status == "deleted" {
		return result, errRecordingGone
	} else if err != nil {
		return result, errPreviewUnavailable
	}
	if write && !p.Capabilities.ManageWorkspace && result.OwnerStaffID != p.StaffID {
		return result, mpd.ErrDenied
	}
	return result, nil
}

// writeStaffRefusal answers SendRec's own staff endpoints (Info, CreatePreview, Publish). A deleted or
// unknown recording is refused with the endpoint's own 403, as before previews could close; only the
// preview page's redeem and renewal paths read it as a closed preview (410).
func writeStaffRefusal(w http.ResponseWriter, err error, denied string) {
	if errors.Is(err, errRecordingGone) {
		err = mpd.ErrDenied
	}
	writeRefusal(w, err, denied, codeDenied)
}
func (m *MPDMedia) Info(w http.ResponseWriter, r *http.Request) {
	p, ok := mpd.PrincipalFromContext(r.Context())
	if !ok {
		httputil.WriteJSON(w, 200, map[string]any{"managed": false})
		return
	}
	result, err := m.video(r.Context(), chi.URLParam(r, "id"), p, false)
	if err != nil {
		writeStaffRefusal(w, err, "recording unavailable")
		return
	}
	httputil.WriteJSON(w, 200, map[string]any{"managed": true, "metadata": result})
}
func (m *MPDMedia) CreatePreview(w http.ResponseWriter, r *http.Request) {
	p, _ := mpd.PrincipalFromContext(r.Context())
	id := chi.URLParam(r, "id")
	metadata, err := m.video(r.Context(), id, p, false)
	if err != nil {
		writeStaffRefusal(w, err, "preview unavailable")
		return
	}
	if metadata.Status != "ready" {
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

// Why a request about a managed recording was refused. The preview page tells staff what to do from
// the code in the body: closed (open the preview again), signed_out (sign in again), unavailable (a
// passing fault) or invalid (the request itself is wrong).
var (
	// errPreviewClosed: the handoff or preview session is spent, expired or for an older recording.
	errPreviewClosed = errors.New("preview closed")
	// errRecordingGone: the recording is deleted or no longer in this workspace.
	errRecordingGone = errors.New("recording gone")
	// errPreviewUnavailable: a passing fault; the same request may work in a moment.
	errPreviewUnavailable = errors.New("preview unavailable")
)

const (
	codeClosed      = "closed"
	codeSignedOut   = "signed_out"
	codeUnavailable = "unavailable"
	codeInvalid     = "invalid"
	codeDenied      = "denied"
)

// writeRefusal answers a refused request: 410 closed for a spent preview or a recording that is gone,
// 503 unavailable for a passing fault, and otherwise 403 with the caller's message and code.
func writeRefusal(w http.ResponseWriter, err error, denied, deniedCode string) {
	switch {
	case errors.Is(err, errPreviewClosed), errors.Is(err, errRecordingGone):
		httputil.WriteJSON(w, 410, map[string]string{"error": "preview closed", "code": codeClosed})
	case errors.Is(err, errPreviewUnavailable), errors.Is(err, mpd.ErrUnavailable):
		httputil.WriteJSON(w, 503, map[string]string{"error": "temporarily unavailable", "code": codeUnavailable})
	default:
		httputil.WriteJSON(w, 403, map[string]string{"error": denied, "code": deniedCode})
	}
}

// writePreviewRefusal answers the preview page, for which a denied staff session means signing in again.
func writePreviewRefusal(w http.ResponseWriter, err error) {
	writeRefusal(w, err, "preview access ended", codeSignedOut)
}

// writeInvalidPreview answers a preview request that is wrong in itself, which no retry mends.
func writeInvalidPreview(w http.ResponseWriter, status int, message string) {
	httputil.WriteJSON(w, status, map[string]string{"error": message, "code": codeInvalid})
}

// spendHandoff spends a preview handoff and opens its 5-minute preview session in one statement, only while
// the handoff is unspent, unexpired and for this recording and media version, and the recording is not deleted.
// A recording deleted after the redeem's checks is refused here too (pgx.ErrNoRows), so it never gets a session.
func spendHandoff(ctx context.Context, db database.DBTX, handoffDigest, nonce, origin, videoID string, version int, proofDigest string) error {
	return db.QueryRow(ctx, `WITH spent AS (UPDATE mpd_preview_handoffs SET used_at=now()
 WHERE token_hash=$1 AND nonce=$2 AND origin=$3 AND used_at IS NULL AND expires_at>now() AND video_id=$4 AND media_version=$5
   AND EXISTS (SELECT 1 FROM videos WHERE id=$4 AND status <> 'deleted') RETURNING session_id)
 INSERT INTO mpd_preview_sessions(token_hash,video_id,media_version,session_id,expires_at) SELECT $6,$4,$5,session_id,now()+interval '5 minutes' FROM spent RETURNING 1`,
		handoffDigest, nonce, origin, videoID, version, proofDigest).Scan(new(int))
}

func (m *MPDMedia) RedeemPreview(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != m.h.baseURL {
		writeInvalidPreview(w, 403, "preview origin denied")
		return
	}
	var input struct {
		Handoff      string `json:"handoff"`
		Nonce        string `json:"nonce"`
		ParentOrigin string `json:"parentOrigin"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048)).Decode(&input) != nil {
		writeInvalidPreview(w, 400, "invalid preview")
		return
	}
	var id, sid, title string
	var version int
	var recordedAt time.Time
	var expires *time.Time
	var emailGate bool
	// The handoff is read, not spent, while the staff session and the recording are checked; those checks
	// call out to MPD and take their own connections, so nothing is held open across them. The title and
	// recording date come back with it, so naming the recording adds no later step.
	err := m.h.db.QueryRow(r.Context(), `SELECT h.video_id,h.media_version,h.session_id,v.title,v.created_at,v.share_expires_at,v.email_gate_enabled
 FROM mpd_preview_handoffs h JOIN videos v ON v.id=h.video_id
 WHERE h.token_hash=$1 AND h.nonce=$2 AND h.origin=$3 AND h.used_at IS NULL AND h.expires_at>now()`,
		tokenDigest(input.Handoff), input.Nonce, input.ParentOrigin).Scan(&id, &version, &sid, &title, &recordedAt, &expires, &emailGate)
	if errors.Is(err, pgx.ErrNoRows) {
		writePreviewRefusal(w, errPreviewClosed)
		return
	} else if err != nil {
		writePreviewRefusal(w, errPreviewUnavailable)
		return
	}
	p, err := m.Identity.ValidateSession(r.Context(), sid)
	if err != nil {
		writePreviewRefusal(w, err)
		return
	}
	metadata, err := m.video(r.Context(), id, p, false)
	if err != nil {
		writePreviewRefusal(w, err)
		return
	}
	if metadata.MediaVersion != version {
		writePreviewRefusal(w, errPreviewClosed)
		return
	}
	// One statement spends the handoff, if it is still unspent, unexpired and for the same recording, and
	// opens the preview session. A refusal or a fault before it leaves the handoff for Staff OS to reopen.
	proof := randomProof()
	err = spendHandoff(r.Context(), m.h.db, tokenDigest(input.Handoff), input.Nonce, input.ParentOrigin, id, version, tokenDigest(proof))
	if errors.Is(err, pgx.ErrNoRows) {
		writePreviewRefusal(w, errPreviewClosed)
		return
	} else if err != nil {
		writePreviewRefusal(w, errPreviewUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	// Named so a preview opened on its own reads as this recording (MPD-515). The title is the one the
	// patient's watch page shows and can name the patient, so the page puts it only in its own header.
	httputil.WriteJSON(w, 200, map[string]any{"previewToken": proof, "videoId": id, "title": title,
		"recorded": recordedLabel(recordedAt, m.Config.ClinicZone), "linkState": previewLinkState(metadata, expires, emailGate, time.Now())})
}

// DefaultClinicZone is where the clinic works, unless MPD_CLINIC_TIMEZONE says otherwise. The preview's
// recording date is shown in the clinic's zone, not the browser's, so it matches the date Staff OS
// lists beside the same video ("18 Nov 2026" for a recording made at 22:30 UTC on 17 Nov) on a laptop
// set to another zone or locale. mpd-api schedules its video reports in the same zone
// (video_report_timezone, Australia/Sydney). cmd/sendrec builds the zone database in (time/tzdata),
// since the Alpine images ship none, and checks the setting at startup.
const DefaultClinicZone = "Australia/Sydney"

// recordedLabel is the preview's recording date: the clinic's day, written as en-AU writes it.
func recordedLabel(at time.Time, zone *time.Location) string {
	return "Recorded " + at.In(zone).Format("2 Jan 2006")
}

// previewLinkState says, for the preview header, whether patients can open the recording's link,
// by the patient watch gate's own rule (patientLinkClosed), and the gates they pass on the way. The
// preview shows only recordings that are not deleted. An unpublished link may never have been sent
// or may have been taken down: publication clears published_at and keeps no history, so it does not
// say "yet".
func previewLinkState(metadata MPDMetadata, expires *time.Time, emailGate bool, now time.Time) string {
	switch patientLinkClosed(metadata.Published, nil, expires, now) {
	case linkUnpublished:
		return "Patients can't open the link"
	case linkExpired:
		return "The patient link has expired"
	}
	text := "Patients can open the link"
	if metadata.PasswordProtected {
		text += " with their code"
	}
	if emailGate {
		text += " after giving their email"
	}
	return text
}

func (m *MPDMedia) preview(r *http.Request) (MPDMetadata, error) {
	proof, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return MPDMetadata{}, mpd.ErrDenied
	}
	var id, sid string
	var version int
	err := m.h.db.QueryRow(r.Context(), `SELECT video_id,media_version,session_id FROM mpd_preview_sessions WHERE token_hash=$1 AND expires_at>now()`, tokenDigest(proof)).Scan(&id, &version, &sid)
	if errors.Is(err, pgx.ErrNoRows) {
		return MPDMetadata{}, errPreviewClosed
	} else if err != nil {
		return MPDMetadata{}, errPreviewUnavailable
	}
	p, err := m.Identity.ValidateSession(r.Context(), sid)
	if err != nil {
		return MPDMetadata{}, err
	}
	current, err := m.video(r.Context(), id, p, false)
	if err != nil {
		return MPDMetadata{}, err
	}
	if current.MediaVersion != version {
		return MPDMetadata{}, errPreviewClosed
	}
	// Rolling preview expiry never extends authorization: each use revalidates MPD.
	if _, err = m.h.db.Exec(r.Context(), `UPDATE mpd_preview_sessions SET expires_at=now()+interval '5 minutes' WHERE token_hash=$1`, tokenDigest(proof)); err != nil {
		return MPDMetadata{}, errPreviewUnavailable
	}
	return current, nil
}
func (m *MPDMedia) PreviewMedia(w http.ResponseWriter, r *http.Request) {
	current, err := m.preview(r)
	if err != nil {
		writePreviewRefusal(w, err)
		return
	}
	id := current.VideoID
	var key string
	var transcript *string
	var expires *time.Time
	var emailGate bool
	if m.h.db.QueryRow(r.Context(), `SELECT file_key,transcript_key,share_expires_at,email_gate_enabled FROM videos WHERE id=$1 AND status='ready'`, id).Scan(&key, &transcript, &expires, &emailGate) != nil {
		// Not ready yet is a passing state: the page tries again.
		httputil.WriteJSON(w, 409, map[string]string{"error": "recording not ready", "code": codeUnavailable})
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
	// Each renewal, about every four minutes, carries the link's state again, so a header left open
	// follows a link that is taken down or expires. It reads the same rows the patient gate does.
	httputil.WriteJSON(w, 200, map[string]any{"videoUrl": media, "transcriptUrl": captions, "expiresAt": time.Now().Add(5 * time.Minute),
		"linkState": previewLinkState(current, expires, emailGate, time.Now())})
}
func (m *MPDMedia) PreviewPlayback(w http.ResponseWriter, r *http.Request) {
	current, err := m.preview(r)
	if err != nil {
		writePreviewRefusal(w, err)
		return
	}
	id := current.VideoID
	if strings.HasSuffix(r.URL.Path, "/session") {
		s, err := mpdevents.NewSession(r.Context(), m.h.db, id, true)
		if err != nil {
			writePreviewRefusal(w, errPreviewUnavailable)
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
		writeStaffRefusal(w, err, "publication denied")
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
	db, ok := m.h.db.(database.Beginner)
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
