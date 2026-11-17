package video

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func metadataRead(a *MPDAdapter, id string) map[string]any {
	r := withURLParam(httptest.NewRequest("GET", "/api/integrations/mpd/videos/"+id, nil), "videoId", id)
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("s", 32))
	w := httptest.NewRecorder()
	a.Authenticate(http.HandlerFunc(a.Metadata)).ServeHTTP(w, r)
	var body map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	return body
}

func retryRequest(a *MPDAdapter, id, body string) *httptest.ResponseRecorder {
	r := withURLParam(httptest.NewRequest("POST", "/api/integrations/mpd/videos/"+id+"/retry", strings.NewReader(body)), "videoId", id)
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("s", 32))
	w := httptest.NewRecorder()
	a.Authenticate(http.HandlerFunc(a.Retry)).ServeHTTP(w, r)
	return w
}

func TestMPDMetadataCarriesProcessingOnlyWhenTheSettingIsOn(t *testing.T) {
	_, h, off, id, _ := seedManagedAdapter(t)
	if body := metadataRead(off, id); body["videoId"] != id || body["processing"] != nil {
		t.Fatalf("off: %v", body)
	}
	cfg := off.cfg
	cfg.ProcessingProgress = true
	body := metadataRead(h.MPDAdapter(cfg), id)
	processing, ok := body["processing"].(map[string]any)
	if !ok {
		t.Fatalf("on: %v", body)
	}
	if processing["transcript"].(map[string]any)["state"] != "pending" {
		t.Fatalf("on: %v", processing)
	}
}

func TestMPDRetryRestartsOnlyAFailedStep(t *testing.T) {
	db, _, a, id, _ := seedManagedAdapter(t)
	ctx := context.Background()
	mustExec(t, db, `UPDATE videos SET content_type='video/webm', transcode_attempts=$2, transcode_error='raw ffmpeg text', processing_error='x', transcript_status='processing' WHERE id=$1`, id, maxTranscodeAttempts)

	for _, c := range []struct {
		body string
		want int
	}{
		{`{"step":"other","mediaVersion":1}`, 422},
		{`{"step":"conversion","mediaVersion":2}`, 409},
		{`{"step":"transcript","mediaVersion":1}`, 409},
	} {
		if w := retryRequest(a, id, c.body); w.Code != c.want {
			t.Fatalf("%s => %d %s", c.body, w.Code, w.Body.String())
		}
	}

	w := retryRequest(a, id, `{"step":"conversion","mediaVersion":1}`)
	if w.Code != 202 || strings.Contains(w.Body.String(), "raw ffmpeg text") {
		t.Fatalf("conversion => %d %s", w.Code, w.Body.String())
	}
	var attempts int
	var transcodeError *string
	if err := db.QueryRow(ctx, `SELECT transcode_attempts, transcode_error FROM videos WHERE id=$1`, id).Scan(&attempts, &transcodeError); err != nil || attempts != 0 || transcodeError != nil {
		t.Fatalf("attempts %d error %v (%v)", attempts, transcodeError, err)
	}
	if w := retryRequest(a, id, `{"step":"conversion","mediaVersion":1}`); w.Code != 409 {
		t.Fatalf("second conversion retry => %d", w.Code)
	}

	mustExec(t, db, `UPDATE videos SET transcript_status='failed' WHERE id=$1`, id)
	if w := retryRequest(a, id, `{"step":"transcript","mediaVersion":1}`); w.Code != 503 {
		t.Fatalf("transcript with transcription off => %d", w.Code)
	}
	t.Setenv("TRANSCRIPTION_ENABLED", "true")
	if w := retryRequest(a, id, `{"step":"transcript","mediaVersion":1}`); w.Code != 202 {
		t.Fatalf("transcript => %d %s", w.Code, w.Body.String())
	}
	var status string
	if err := db.QueryRow(ctx, `SELECT transcript_status FROM videos WHERE id=$1`, id).Scan(&status); err != nil || status != "pending" {
		t.Fatalf("transcript status %q (%v)", status, err)
	}
}

func TestMPDRetryLeavesAFailedWebcamCompositeToTheApp(t *testing.T) {
	db, _, a, id, _ := seedManagedAdapter(t)
	mustExec(t, db, `UPDATE videos SET status='processing', content_type='video/webm', transcode_attempts=$2 WHERE id=$1`, id, maxTranscodeAttempts)
	if w := retryRequest(a, id, `{"step":"conversion","mediaVersion":1}`); w.Code != 409 && w.Code != 404 {
		t.Fatalf("composite retry => %d %s", w.Code, w.Body.String())
	}
	var attempts int
	if err := db.QueryRow(context.Background(), `SELECT transcode_attempts FROM videos WHERE id=$1`, id).Scan(&attempts); err != nil || attempts != maxTranscodeAttempts {
		t.Fatalf("attempts %d (%v)", attempts, err)
	}
}
