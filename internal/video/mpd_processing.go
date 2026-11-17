package video

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/sendrec/sendrec/internal/database"
	"github.com/sendrec/sendrec/internal/httputil"
)

// MPDStepProgress is one processing step as MPD sees it: its state, a whole
// percent or null, and a short failure code, never error text.
type MPDStepProgress struct {
	State   string  `json:"state"`
	Percent *int    `json:"percent"`
	Failure *string `json:"failure"`
}

// MPDProcessing is a recording's conversion and transcript
// (add-mpd-processing-progress).
type MPDProcessing struct {
	Conversion MPDStepProgress `json:"conversion"`
	Transcript MPDStepProgress `json:"transcript"`
	UpdatedAt  time.Time       `json:"updatedAt"`
}

type processingRow struct {
	status         string
	contentType    string
	iosNormalized  bool
	attempts       int
	encoding       bool
	percent        *int
	transcript     string
	updatedAt      time.Time
	fileKey        string
	duration       int
	noiseReduction bool
}

func (a *MPDAdapter) processingRow(ctx context.Context, db database.DBTX, id string) (processingRow, error) {
	var row processingRow
	var percent *int16
	err := db.QueryRow(ctx, `SELECT status, COALESCE(content_type, ''), ios_normalized, transcode_attempts,
	        encode_started_at IS NOT NULL, conversion_percent, transcript_status, updated_at,
	        file_key, COALESCE(duration, 0), noise_reduction
	   FROM videos WHERE id = $1 AND organization_id = $2`, id, a.cfg.WorkspaceID).
		Scan(&row.status, &row.contentType, &row.iosNormalized, &row.attempts, &row.encoding, &percent,
			&row.transcript, &row.updatedAt, &row.fileKey, &row.duration, &row.noiseReduction)
	if percent != nil {
		p := int(*percent)
		row.percent = &p
	}
	return row, err
}

func failure(code string) *string { return &code }

// conversionStep is `done` once the recording is an MP4 that needs nothing
// more, `failed` only when no attempt remains, and `running` while ffmpeg
// runs or SendRec waits to try again.
func conversionStep(row processingRow) MPDStepProgress {
	needed := row.status == "processing" || row.contentType == "video/webm" ||
		((row.contentType == "video/mp4" || row.contentType == "video/quicktime") && !row.iosNormalized)
	switch {
	case !needed:
		return MPDStepProgress{State: "done"}
	case row.attempts >= maxTranscodeAttempts:
		return MPDStepProgress{State: "failed", Failure: failure("conversion_failed")}
	case row.encoding || row.attempts > 0 || row.percent != nil || row.status == "processing":
		return MPDStepProgress{State: "running", Percent: row.percent}
	default:
		return MPDStepProgress{State: "pending"}
	}
}

func transcriptStep(row processingRow) MPDStepProgress {
	switch row.transcript {
	case "processing":
		return MPDStepProgress{State: "running"}
	case "ready":
		return MPDStepProgress{State: "done"}
	case "no_audio":
		return MPDStepProgress{State: "no_audio"}
	case "failed":
		return MPDStepProgress{State: "failed", Failure: failure("transcript_failed")}
	default:
		return MPDStepProgress{State: "pending"}
	}
}

func processingOf(row processingRow) *MPDProcessing {
	return &MPDProcessing{Conversion: conversionStep(row), Transcript: transcriptStep(row), UpdatedAt: row.updatedAt.UTC()}
}

// Retry restarts a failed conversion or transcript for MPD, on the recorder's
// behalf. Anything not failed, or a media version that moved, is 409
// step_not_failed and changes nothing.
func (a *MPDAdapter) Retry(w http.ResponseWriter, r *http.Request) {
	ctx, id := r.Context(), chi.URLParam(r, "videoId")
	var req struct {
		Step         string `json:"step"`
		MediaVersion int    `json:"mediaVersion"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&req); err != nil ||
		(req.Step != "conversion" && req.Step != "transcript") || req.MediaVersion < 1 {
		httputil.WriteError(w, http.StatusUnprocessableEntity, "step and mediaVersion required")
		return
	}
	m, err := a.metadata(ctx, a.h.db, id)
	if err != nil || m.Status == "deleted" {
		httputil.WriteError(w, http.StatusNotFound, "recording unavailable")
		return
	}
	row, err := a.processingRow(ctx, a.h.db, id)
	if err != nil {
		httputil.WriteError(w, http.StatusNotFound, "recording unavailable")
		return
	}
	notFailed := func() { httputil.WriteError(w, http.StatusConflict, "step_not_failed") }
	if req.MediaVersion != m.MediaVersion {
		notFailed()
		return
	}
	switch req.Step {
	case "conversion":
		if conversionStep(row).State != "failed" {
			notFailed()
			return
		}
		tag, err := a.h.db.Exec(ctx, `UPDATE videos SET transcode_attempts = 0, transcode_error = NULL,
		        processing_error = NULL, conversion_percent = NULL, updated_at = now()
		  WHERE id = $1 AND file_key = $2 AND transcode_attempts >= $3`, id, row.fileKey, maxTranscodeAttempts)
		if err != nil {
			httputil.WriteError(w, http.StatusInternalServerError, "retry failed")
			return
		}
		if tag.RowsAffected() == 0 {
			notFailed()
			return
		}
		job := JobTypeNormalize
		if row.contentType == "video/webm" {
			job = JobTypeTranscode
		}
		a.h.EnqueueJob(ctx, job, id, map[string]any{
			"fileKey": row.fileKey, "audioFilter": sweepAudioFilter(row.noiseReduction), "duration": row.duration,
		})
	case "transcript":
		if transcriptStep(row).State != "failed" {
			notFailed()
			return
		}
		// Nothing would run it, as SendRec's own Retry transcript refuses.
		if !isTranscriptionEnabled() {
			httputil.WriteError(w, http.StatusServiceUnavailable, "transcription is not enabled")
			return
		}
		if err := EnqueueTranscription(ctx, a.h.db, id); err != nil {
			httputil.WriteError(w, http.StatusInternalServerError, "retry failed")
			return
		}
	}
	if row, err = a.processingRow(ctx, a.h.db, id); err != nil {
		httputil.WriteError(w, http.StatusInternalServerError, "retry failed")
		return
	}
	httputil.WriteJSON(w, http.StatusAccepted, map[string]any{"processing": processingOf(row)})
}
