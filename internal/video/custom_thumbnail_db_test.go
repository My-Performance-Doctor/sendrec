package video

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sendrec/sendrec/internal/auth"
)

type authorized struct {
	UploadURL    string `json:"uploadUrl"`
	ThumbnailKey string `json:"thumbnailKey"`
	MediaVersion int    `json:"mediaVersion"`
}

func thumbnailRequest(t *testing.T, pool *pgxpool.Pool, videoID, body string) *http.Request {
	t.Helper()
	var userID string
	if err := pool.QueryRow(context.Background(), `SELECT user_id FROM videos WHERE id = $1`, videoID).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	return withURLParam(req.WithContext(auth.ContextWithUserID(req.Context(), userID)), "id", videoID)
}

func authorizeThumbnail(t *testing.T, h *Handler, pool *pgxpool.Pool, videoID string) authorized {
	t.Helper()
	rec := httptest.NewRecorder()
	h.UploadThumbnail(rec, thumbnailRequest(t, pool, videoID, `{"contentType":"image/jpeg","contentLength":1000}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("authorize: %d %s", rec.Code, rec.Body.String())
	}
	var a authorized
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	return a
}

func completeThumbnail(t *testing.T, h *Handler, pool *pgxpool.Pool, videoID string, a authorized) int {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"thumbnailKey": a.ThumbnailKey, "mediaVersion": a.MediaVersion})
	rec := httptest.NewRecorder()
	h.CompleteThumbnail(rec, thumbnailRequest(t, pool, videoID, string(body)))
	return rec.Code
}

func seedThumbnailVideo(t *testing.T, pool *pgxpool.Pool) (string, *memStorage, *Handler) {
	t.Helper()
	const key, thumb = "recordings/u/tok.webm", "recordings/u/tok.t0.jpg"
	videoID := seedReadyVideo(t, pool, key, "video/webm")
	mustExecDB(t, pool, `UPDATE videos SET thumbnail_key = $2 WHERE id = $1`, videoID, thumb)
	s := newMemStorage(map[string]string{key: "video", thumb: "current thumbnail"})
	return videoID, s, NewHandler(pool, s, testBaseURL, 0, 0, 0, 0, testHMACSecret, false)
}

// Review #1: asking for an upload URL must not replace the working thumbnail;
// only a completed upload does.
func TestCustomThumbnailReplacesOnlyOnCompletion(t *testing.T) {
	pool := accountDB(t)
	videoID, s, h := seedThumbnailVideo(t, pool)

	a := authorizeThumbnail(t, h, pool, videoID)
	if got := readDerived(t, pool, videoID); str(got.thumb) != "recordings/u/tok.t0.jpg" {
		t.Fatalf("thumbnail_key = %s right after authorizing, want the current one kept", str(got.thumb))
	}
	if _, ok := retired(t, pool)["recordings/u/tok.t0.jpg"]; ok {
		t.Fatal("the working thumbnail was retired before any upload")
	}

	// The PUT never happened: completing must not publish a missing object.
	if code := completeThumbnail(t, h, pool, videoID, a); code < 400 {
		t.Fatalf("complete without an upload = %d, want an error", code)
	}
	if got := readDerived(t, pool, videoID); str(got.thumb) != "recordings/u/tok.t0.jpg" {
		t.Fatalf("thumbnail_key = %s, want the current one kept", str(got.thumb))
	}
	if r := retired(t, pool); r[a.ThumbnailKey] < 23*time.Hour {
		t.Errorf("abandoned upload %s due in %v, want it left in the attempt queue", a.ThumbnailKey, r[a.ThumbnailKey])
	}

	s.objects[a.ThumbnailKey] = "custom"
	if code := completeThumbnail(t, h, pool, videoID, a); code != http.StatusNoContent {
		t.Fatalf("complete = %d, want 204", code)
	}
	if got := readDerived(t, pool, videoID); str(got.thumb) != a.ThumbnailKey {
		t.Errorf("thumbnail_key = %s, want %s", str(got.thumb), a.ThumbnailKey)
	}
	if due := retired(t, pool)["recordings/u/tok.t0.jpg"]; due < 90*time.Minute {
		t.Errorf("replaced thumbnail due in %v, want it kept for issued URLs", due)
	}
}

// Review #9: a completion for content an edit has since replaced is stale.
func TestCustomThumbnailRejectsAStaleVersion(t *testing.T) {
	pool := accountDB(t)
	videoID, s, h := seedThumbnailVideo(t, pool)
	a := authorizeThumbnail(t, h, pool, videoID)
	s.objects[a.ThumbnailKey] = "custom"
	editContent(t, pool, videoID, "recordings/u/tok.e1.webm")
	mustExecDB(t, pool, `UPDATE videos SET thumbnail_key = 'recordings/u/tok.e1t.jpg' WHERE id = $1`, videoID)

	if code := completeThumbnail(t, h, pool, videoID, a); code != http.StatusConflict {
		t.Fatalf("stale complete = %d, want 409", code)
	}
	if got := readDerived(t, pool, videoID); str(got.thumb) != "recordings/u/tok.e1t.jpg" {
		t.Errorf("thumbnail_key = %s, want the edit's thumbnail kept", str(got.thumb))
	}
}

// Review #10: a crash right after an edit's switch owes the video a
// thumbnail; the cleanup loop makes it.
func TestMissingThumbnailIsRegenerated(t *testing.T) {
	pool := accountDB(t)
	const key = "recordings/u/tok.e1.webm"
	videoID := seedReadyVideo(t, pool, key, "video/webm")
	mustExecDB(t, pool, `UPDATE videos SET media_version = 1, thumbnail_key = NULL, updated_at = now() - INTERVAL '1 hour' WHERE id = $1`, videoID)
	stubFrame(t, nil)
	s := newMemStorage(map[string]string{key: "video"})

	RegenerateMissingThumbnails(context.Background(), pool, s)

	got := readDerived(t, pool, videoID)
	if got.thumb == nil || s.snapshot()[*got.thumb] != "jpeg" {
		t.Errorf("thumbnail_key = %s, want a thumbnail made for the edited content", str(got.thumb))
	}
}

// A thumbnail that can't be made is given up on for that version, so the
// loop doesn't retry it forever.
func TestFailedThumbnailIsNotRetriedForever(t *testing.T) {
	pool := accountDB(t)
	const key = "recordings/u/tok.e1.webm"
	videoID := seedReadyVideo(t, pool, key, "video/webm")
	mustExecDB(t, pool, `UPDATE videos SET media_version = 1, updated_at = now() - INTERVAL '1 hour' WHERE id = $1`, videoID)
	frame := extractFrameAt
	calls := 0
	extractFrameAt = func(context.Context, string, string, int) error { calls++; return errors.New("no frames") }
	t.Cleanup(func() { extractFrameAt = frame })
	s := newMemStorage(map[string]string{key: "video"})

	RegenerateMissingThumbnails(context.Background(), pool, s)
	RegenerateMissingThumbnails(context.Background(), pool, s)

	if calls != 1 {
		t.Errorf("frame extracted %d times over two passes, want 1", calls)
	}
}
