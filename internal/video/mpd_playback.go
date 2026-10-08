package video

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/sendrec/sendrec/internal/httputil"
	"github.com/sendrec/sendrec/internal/mpdevents"
)

// MPDPlaybackSession's wrapper must repeat the same media-access checks as the
// native watch player. staffPreview comes exclusively from verified middleware.
func (h *Handler) MPDPlaybackSession(w http.ResponseWriter, r *http.Request, staffPreview bool) {
	var id string
	if err := h.db.QueryRow(r.Context(), `SELECT id FROM videos WHERE share_token=$1 AND status='ready'`, chi.URLParam(r, "shareToken")).Scan(&id); err != nil {
		httputil.WriteError(w, 404, "video not found")
		return
	}
	s, err := mpdevents.NewSession(r.Context(), h.db, id, staffPreview)
	if err != nil {
		httputil.WriteError(w, 403, "playback unavailable")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httputil.WriteJSON(w, 201, s)
}
func (h *Handler) MPDPlaybackStart(w http.ResponseWriter, r *http.Request) {
	var id string
	if err := h.db.QueryRow(r.Context(), `SELECT id FROM videos WHERE share_token=$1 AND status='ready'`, chi.URLParam(r, "shareToken")).Scan(&id); err != nil {
		httputil.WriteError(w, 404, "video not found")
		return
	}
	var p mpdevents.Progress
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2048))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		httputil.WriteError(w, 400, "invalid playback observation")
		return
	}
	if err := mpdevents.Accept(r.Context(), h.db, id, p); err != nil {
		httputil.WriteError(w, 403, "invalid or expired playback observation")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
