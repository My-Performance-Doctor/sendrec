package video

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// Review #3: the upload URL of a rejected completion is still valid, so its
// key keeps the attempt deadline instead of being swept at once; a PUT after
// the sweep would otherwise recreate an object nothing tracks.
func TestRejectedCompletionKeepsTheAttemptDeadline(t *testing.T) {
	pool := accountDB(t)
	videoID, s, h := seedThumbnailVideo(t, pool)
	a := authorizeThumbnail(t, h, pool, videoID)
	s.objects[a.ThumbnailKey] = "custom"
	editContent(t, pool, videoID, "recordings/u/tok.e1.webm")

	if code := completeThumbnail(t, h, pool, videoID, a); code != http.StatusConflict {
		t.Fatalf("stale complete = %d, want 409", code)
	}
	if due := retired(t, pool)[a.ThumbnailKey]; due < 23*time.Hour {
		t.Errorf("rejected upload due in %v, want the original attempt deadline", due)
	}
}

// movesOnHead hands the video to someone else while the completion is
// checking the upload.
type movesOnHead struct {
	*memStorage
	move func()
}

func (m movesOnHead) HeadObject(ctx context.Context, key string) (int64, string, error) {
	m.move()
	return m.memStorage.HeadObject(ctx, key)
}

// Review #6: the publication keeps the caller's ownership check, so a video
// that left the caller's scope during the upload check isn't changed.
func TestCompletionKeepsTheCallersScope(t *testing.T) {
	pool := accountDB(t)
	videoID, s, _ := seedThumbnailVideo(t, pool)
	other := mustID(t, pool, `INSERT INTO users (email, password, name) VALUES ($1, 'x', 'Other') RETURNING id`, uniqueName(t)+"@example.com")
	h := NewHandler(pool, movesOnHead{s, func() {
		mustExecDB(t, pool, `UPDATE videos SET user_id = $2 WHERE id = $1`, videoID, other)
	}}, testBaseURL, 0, 0, 0, 0, testHMACSecret, false)
	a := authorizeThumbnail(t, h, pool, videoID)
	s.objects[a.ThumbnailKey] = "custom"
	req := thumbnailRequest(t, pool, videoID, `{"thumbnailKey":"`+a.ThumbnailKey+`","mediaVersion":0}`) // as the owner

	rec := httptest.NewRecorder()
	h.CompleteThumbnail(rec, req)

	if got := readDerived(t, pool, videoID); str(got.thumb) != "recordings/u/tok.t0.jpg" {
		t.Errorf("thumbnail_key = %s (status %d), want the new owner's video left alone", str(got.thumb), rec.Code)
	}
}

// The same for a transcript upload.
func TestTranscriptUploadKeepsTheCallersScope(t *testing.T) {
	pool := accountDB(t)
	videoID, s, _ := seedThumbnailVideo(t, pool)
	other := mustID(t, pool, `INSERT INTO users (email, password, name) VALUES ($1, 'x', 'Other') RETURNING id`, uniqueName(t)+"@example.com")
	s.onPut = func(string) { mustExecDB(t, pool, `UPDATE videos SET user_id = $2 WHERE id = $1`, videoID, other) }
	h := NewHandler(pool, s, testBaseURL, 0, 0, 0, 0, testHMACSecret, false)
	req := authenticatedMultipartRequest(t, "/", "file", "t.vtt", []byte("WEBVTT\n\n00:00:00.000 --> 00:00:01.000\nhi\n"))
	var userID string
	if err := pool.QueryRow(context.Background(), `SELECT user_id FROM videos WHERE id = $1`, videoID).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	req = withURLParam(req.WithContext(auth.ContextWithUserID(req.Context(), userID)), "id", videoID)

	rec := httptest.NewRecorder()
	h.UploadTranscript(rec, req)

	if got := readDerived(t, pool, videoID); got.transcript != nil {
		t.Errorf("transcript_key = %s (status %d), want the new owner's video left alone", str(got.transcript), rec.Code)
	}
}

type recoveryRow struct {
	owed     bool
	attempts int
	retryIn  *time.Duration
}

func readRecovery(t *testing.T, pool *pgxpool.Pool, videoID string) recoveryRow {
	t.Helper()
	var r recoveryRow
	if err := pool.QueryRow(context.Background(),
		`SELECT thumbnail_version < media_version, thumbnail_attempts, thumbnail_retry_at - now() FROM videos WHERE id = $1`, videoID,
	).Scan(&r.owed, &r.attempts, &r.retryIn); err != nil {
		t.Fatal(err)
	}
	return r
}

func seedOwedThumbnail(t *testing.T, pool *pgxpool.Pool, key string) string {
	t.Helper()
	videoID := seedReadyVideo(t, pool, key, "video/webm")
	mustExecDB(t, pool, `UPDATE videos SET media_version = 1, thumbnail_key = NULL, updated_at = now() - INTERVAL '1 hour' WHERE id = $1`, videoID)
	return videoID
}

// Review #5: a storage error that may pass is retried later, not given up on.
func TestThumbnailRecoveryRetriesTransientErrors(t *testing.T) {
	pool := accountDB(t)
	const key = "recordings/u/tok.e1.webm"
	videoID := seedOwedThumbnail(t, pool, key)
	stubFrame(t, nil)

	RegenerateMissingThumbnails(context.Background(), pool, newMemStorage(map[string]string{})) // storage can't serve the file
	got := readRecovery(t, pool, videoID)
	if !got.owed || got.attempts != 1 || got.retryIn == nil || *got.retryIn <= 0 {
		t.Fatalf("after a transient failure: %+v, want still owed, 1 attempt, a retry later", got)
	}

	mustExecDB(t, pool, `UPDATE videos SET thumbnail_retry_at = now() - INTERVAL '1 second' WHERE id = $1`, videoID)
	RegenerateMissingThumbnails(context.Background(), pool, newMemStorage(map[string]string{key: "video"}))
	if got := readDerived(t, pool, videoID); got.thumb == nil {
		t.Error("the thumbnail wasn't made once storage recovered")
	}
}

// A job that runs into its deadline is an attempt too.
func TestThumbnailRecoveryCountsTimeouts(t *testing.T) {
	pool := accountDB(t)
	const key = "recordings/u/tok.e1.webm"
	videoID := seedOwedThumbnail(t, pool, key)
	timeout := thumbnailJobTimeout
	thumbnailJobTimeout = 100 * time.Millisecond
	t.Cleanup(func() { thumbnailJobTimeout = timeout })
	frame := extractFrameAt
	extractFrameAt = func(ctx context.Context, _, _ string, _ int) error { <-ctx.Done(); return ctx.Err() }
	t.Cleanup(func() { extractFrameAt = frame })

	RegenerateMissingThumbnails(context.Background(), pool, newMemStorage(map[string]string{key: "video"}))

	if got := readRecovery(t, pool, videoID); !got.owed || got.attempts != 1 {
		t.Errorf("after a timeout: %+v, want still owed with 1 attempt", got)
	}
}

// A shutdown is no attempt, and the video isn't held back by it.
func TestThumbnailRecoveryIgnoresShutdown(t *testing.T) {
	pool := accountDB(t)
	const key = "recordings/u/tok.e1.webm"
	videoID := seedOwedThumbnail(t, pool, key)
	ctx, cancel := context.WithCancel(context.Background())
	stubFrame(t, cancel)

	RegenerateMissingThumbnails(ctx, pool, newMemStorage(map[string]string{key: "video"}))

	if got := readRecovery(t, pool, videoID); !got.owed || got.attempts != 0 || (got.retryIn != nil && *got.retryIn > 0) {
		t.Errorf("after a shutdown: %+v, want still owed, no attempt, due now", got)
	}
}

// Retries are bounded.
func TestThumbnailRecoveryGivesUp(t *testing.T) {
	pool := accountDB(t)
	videoID := seedOwedThumbnail(t, pool, "recordings/u/tok.e1.webm")
	mustExecDB(t, pool, `UPDATE videos SET thumbnail_attempts = $2 WHERE id = $1`, videoID, maxThumbnailAttempts-1)
	stubFrame(t, nil)

	RegenerateMissingThumbnails(context.Background(), pool, newMemStorage(map[string]string{}))

	if got := readRecovery(t, pool, videoID); got.owed || got.attempts != maxThumbnailAttempts {
		t.Errorf("after the last attempt: %+v, want it given up after %d attempts", got, maxThumbnailAttempts)
	}
}

// Videos that keep failing must not hold the batch.
func TestThumbnailRecoveryIsNotStarved(t *testing.T) {
	pool := accountDB(t)
	var stuck []string
	for i := 0; i < 10; i++ {
		stuck = append(stuck, seedOwedThumbnailNamed(t, pool, i))
	}
	const key = "recordings/u/fresh.webm"
	fresh := seedReadyVideoNamed(t, pool, key, "fresh")
	mustExecDB(t, pool, `UPDATE videos SET media_version = 1, updated_at = now() - INTERVAL '30 minutes' WHERE id = $1`, fresh)
	stubFrame(t, nil)
	s := newMemStorage(map[string]string{key: "video"}) // the stuck ones' files are missing

	RegenerateMissingThumbnails(context.Background(), pool, s)
	RegenerateMissingThumbnails(context.Background(), pool, s)

	if got := readDerived(t, pool, fresh); got.thumb == nil {
		t.Errorf("the newer video got no thumbnail behind %d failing ones", len(stuck))
	}
	for _, id := range stuck {
		if got := readRecovery(t, pool, id); !got.owed || got.attempts != 1 || got.retryIn == nil || *got.retryIn <= 0 {
			t.Errorf("failing video %s: %+v, want one attempt and a retry later", id, got)
		}
	}
}

func seedReadyVideoNamed(t *testing.T, pool *pgxpool.Pool, key, token string) string {
	t.Helper()
	var userID string
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO users (email, password, name) VALUES ('edit@example.com', 'x', 'Editor')
		 ON CONFLICT (email) DO UPDATE SET name = EXCLUDED.name RETURNING id`,
	).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	return mustID(t, pool, `INSERT INTO videos (user_id, title, file_key, share_token, content_type, status)
		VALUES ($1, 'Clip', $2, $3, 'video/webm', 'ready') RETURNING id`, userID, key, token)
}

func seedOwedThumbnailNamed(t *testing.T, pool *pgxpool.Pool, i int) string {
	t.Helper()
	id := seedReadyVideoNamed(t, pool, fmt.Sprintf("recordings/u/stuck%d.webm", i), fmt.Sprintf("stuck%d", i))
	mustExecDB(t, pool, `UPDATE videos SET media_version = 1, updated_at = now() - INTERVAL '2 hours' WHERE id = $1`, id)
	return id
}
