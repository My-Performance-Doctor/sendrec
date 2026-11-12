package video

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/sendrec/sendrec/internal/database"
	"github.com/sendrec/sendrec/internal/httputil"
)

// mpd-api writes a summary of two or three sentences and two to six points.
// These limits leave room for staff edits without letting a page grow without
// bound.
const (
	maxPatientSummaryRunes = 1200
	maxPatientSummaryPoint = 300
	maxPatientSummaryItems = 8
	maxPatientSummaryStart = 24 * 60 * 60
)

// patientSummaryPoint is one point of an approved summary, made start seconds
// into the video.
type patientSummaryPoint struct {
	Start float64 `json:"start"`
	Text  string  `json:"text"`
}

// patientSummary is what mpd-api sends once staff approve it for the patient.
type patientSummary struct {
	Text   string                `json:"text"`
	Points []patientSummaryPoint `json:"points"`
}

// PatientSummary sets or clears a managed recording's patient summary for its
// current media version. A null summary with no points clears it.
func (a *MPDAdapter) PatientSummary(w http.ResponseWriter, r *http.Request) {
	var request struct {
		MediaVersion int                   `json:"mediaVersion"`
		Summary      *string               `json:"summary"`
		Points       []patientSummaryPoint `json:"points"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
	dec.DisallowUnknownFields()
	if dec.Decode(&request) != nil || request.MediaVersion < 1 || dec.Decode(new(any)) != io.EOF {
		httputil.WriteError(w, 400, "invalid patient summary")
		return
	}
	summary, ok := tidyPatientSummary(request.Summary, request.Points)
	if !ok {
		httputil.WriteError(w, 400, "invalid patient summary")
		return
	}
	request.Summary, request.Points = nil, nil
	if summary != nil {
		request.Summary, request.Points = &summary.Text, summary.Points
	}
	body, _ := json.Marshal(request)
	a.fenced(w, r, "patient_summary", request.MediaVersion, body, func(ctx context.Context, tx pgx.Tx, id string, media int) (any, error) {
		var stored []byte
		if summary != nil {
			stored, _ = json.Marshal(summary)
		}
		_, err := tx.Exec(ctx, `UPDATE mpd_video_state SET patient_summary=$2,patient_summary_media_version=CASE WHEN $2::jsonb IS NULL THEN NULL ELSE $3::integer END WHERE video_id=$1`, id, stored, media)
		return map[string]any{"mediaVersion": media, "patientSummary": summary != nil}, err
	})
}

// tidyPatientSummary trims the summary and its points and puts the points in
// video order. It refuses blank or oversized text, points without a summary,
// and start times that are not a plain number of seconds within a day.
func tidyPatientSummary(text *string, points []patientSummaryPoint) (*patientSummary, bool) {
	if text == nil {
		return nil, len(points) == 0
	}
	summary := &patientSummary{Text: strings.TrimSpace(*text), Points: make([]patientSummaryPoint, 0, len(points))}
	if summary.Text == "" || utf8.RuneCountInString(summary.Text) > maxPatientSummaryRunes || len(points) > maxPatientSummaryItems {
		return nil, false
	}
	for _, point := range points {
		point.Text = strings.TrimSpace(point.Text)
		if point.Text == "" || utf8.RuneCountInString(point.Text) > maxPatientSummaryPoint ||
			math.IsNaN(point.Start) || math.IsInf(point.Start, 0) || point.Start < 0 || point.Start > maxPatientSummaryStart {
			return nil, false
		}
		summary.Points = append(summary.Points, point)
	}
	sort.SliceStable(summary.Points, func(i, j int) bool { return summary.Points[i].Start < summary.Points[j].Start })
	return summary, true
}

// currentPatientSummary reads the summary stored for the recording's current
// media version, or nil when there is none or it was written for older media.
func currentPatientSummary(ctx context.Context, db database.DBTX, videoID string) *patientSummary {
	var raw []byte
	err := db.QueryRow(ctx, `SELECT patient_summary FROM mpd_video_state WHERE video_id=$1 AND deleted_at IS NULL AND patient_summary IS NOT NULL AND patient_summary_media_version=media_version`, videoID).Scan(&raw)
	if err != nil {
		return nil
	}
	var summary patientSummary
	if json.Unmarshal(raw, &summary) != nil || summary.Text == "" {
		return nil
	}
	return &summary
}

// managedSummary is what a managed watch page shows in its Summary tab: the
// approved summary with its points as the "In this video" list, or nothing.
func managedSummary(ctx context.Context, db database.DBTX, videoID string) (string, []Chapter, string, string) {
	summary := currentPatientSummary(ctx, db, videoID)
	if summary == nil {
		return "", []Chapter{}, "none", ""
	}
	points := make([]Chapter, 0, len(summary.Points))
	for _, point := range summary.Points {
		points = append(points, Chapter{Title: point.Text, Start: point.Start})
	}
	return summary.Text, points, "ready", ""
}
