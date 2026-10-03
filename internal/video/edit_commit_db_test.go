package video

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sendrec/sendrec/internal/auth"
)

// memStorage keeps objects in memory so a test can see which objects exist
// after a job, not just which calls it made.
type memStorage struct {
	mockStorage
	mu      sync.Mutex
	objects map[string]string
	onPut   func(key string)
}

func newMemStorage(objects map[string]string) *memStorage {
	return &memStorage{objects: objects}
}

func (s *memStorage) DownloadToFile(_ context.Context, key, dest string) error {
	s.mu.Lock()
	body, ok := s.objects[key]
	s.mu.Unlock()
	if !ok {
		return fmt.Errorf("no such key %q", key)
	}
	return os.WriteFile(dest, []byte(body), 0o600)
}

func (s *memStorage) UploadFile(_ context.Context, key, path, _ string) error {
	body, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.objects[key] = string(body)
	s.mu.Unlock()
	if s.onPut != nil {
		s.onPut(key)
	}
	return nil
}

func (s *memStorage) DeleteObject(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key)
	return nil
}

func (s *memStorage) GenerateDownloadURL(_ context.Context, key string, _ time.Duration) (string, error) {
	return "https://storage.test/" + key, nil
}

func (s *memStorage) snapshot() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(s.objects))
	for k, v := range s.objects {
		out[k] = v
	}
	return out
}

// stubEdit replaces the ffmpeg step of both edits with one that writes body.
func stubEdit(t *testing.T, body string) {
	t.Helper()
	trim, remove := trimVideo, removeSegmentsFromVideo
	trimVideo = func(_ context.Context, _, out, _ string, _, _ float64) error {
		return os.WriteFile(out, []byte(body), 0o600)
	}
	removeSegmentsFromVideo = func(_ context.Context, _, out, _ string, _ []segmentRange, _ bool) error {
		return os.WriteFile(out, []byte(body), 0o600)
	}
	t.Cleanup(func() { trimVideo, removeSegmentsFromVideo = trim, remove })
}

type editRow struct {
	fileKey, status string
	duration        int
	fileSize        int64
}

func readEditRow(t *testing.T, pool *pgxpool.Pool, videoID string) editRow {
	t.Helper()
	var r editRow
	if err := pool.QueryRow(context.Background(),
		`SELECT file_key, status, duration, file_size FROM videos WHERE id = $1`, videoID,
	).Scan(&r.fileKey, &r.status, &r.duration, &r.fileSize); err != nil {
		t.Fatalf("read video: %v", err)
	}
	return r
}

// seedProcessingVideo stores a video the way the edit endpoints leave it just
// before the job starts.
func seedProcessingVideo(t *testing.T, pool *pgxpool.Pool, fileKey, contentType string) string {
	t.Helper()
	ctx := context.Background()
	var userID, videoID string
	if err := pool.QueryRow(ctx,
		`INSERT INTO users (email, password, name) VALUES ('edit@example.com', 'x', 'Editor')
		 ON CONFLICT (email) DO UPDATE SET name = EXCLUDED.name RETURNING id`,
	).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO videos (user_id, title, duration, file_size, file_key, share_token, content_type, status, processing_started_at)
		 VALUES ($1, 'Clip', 10, 8, $2, $3, $4, 'processing', now()) RETURNING id`,
		userID, fileKey, "tok"+strings.ReplaceAll(t.Name(), "/", ""), contentType,
	).Scan(&videoID); err != nil {
		t.Fatalf("insert video: %v", err)
	}
	return videoID
}

var editJobs = []struct {
	name string
	run  func(ctx context.Context, pool *pgxpool.Pool, s ObjectStorage, videoID, fileKey, contentType string)
}{
	{"trim", func(ctx context.Context, pool *pgxpool.Pool, s ObjectStorage, videoID, fileKey, contentType string) {
		TrimVideoAsync(ctx, pool, s, videoID, fileKey, "recordings/u/tok.jpg", contentType, 2, 6)
	}},
	{"remove-segments", func(ctx context.Context, pool *pgxpool.Pool, s ObjectStorage, videoID, fileKey, contentType string) {
		RemoveSegmentsAsync(ctx, pool, s, videoID, fileKey, "recordings/u/tok.jpg", contentType, []segmentRange{{Start: 0, End: 6}}, 10)
	}},
}

// retired returns how long from now each tracked key is due for deletion.
func retired(t *testing.T, pool *pgxpool.Pool) map[string]time.Duration {
	t.Helper()
	rows, err := pool.Query(context.Background(), `SELECT key, delete_after - now() FROM retired_objects`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]time.Duration{}
	for rows.Next() {
		var key string
		var due time.Duration
		if err := rows.Scan(&key, &due); err != nil {
			t.Fatal(err)
		}
		out[key] = due
	}
	return out
}

// BG-10: the edit used to overwrite the only copy of the video and then update
// the row. Now it lands under a new key that is recorded first, and the row
// switches to it together with the new duration while the original is retired
// for longer than any URL already issued for it.
func TestEditReplacesVideoUnderNewKey(t *testing.T) {
	for _, job := range editJobs {
		for _, tc := range []struct{ contentType, key, ext string }{
			{"video/webm", "recordings/u/tok.webm", ".webm"},
			{"video/mp4", "recordings/u/tok.mp4", ".mp4"},
			{"video/quicktime", "recordings/u/tok.mov", ".mov"},
		} {
			t.Run(job.name+"/"+tc.contentType, func(t *testing.T) {
				pool := accountDB(t)
				videoID := seedProcessingVideo(t, pool, tc.key, tc.contentType)
				stubEdit(t, "edited")
				s := newMemStorage(map[string]string{tc.key: "original"})
				// The moment the edit is in storage is the moment a crash would
				// strand it: the row and the original must still agree, and the
				// upload must already be on record.
				s.onPut = func(key string) {
					if key == tc.key {
						t.Errorf("edit overwrote the original at %s", key)
					}
					if got := readEditRow(t, pool, videoID); got.fileKey != tc.key || got.duration != 10 {
						t.Errorf("row changed before the edit was stored: %+v", got)
					}
					if due, ok := retired(t, pool)[key]; !ok || due < 23*time.Hour {
						t.Errorf("upload %s not recorded for later cleanup (due in %v)", key, due)
					}
				}

				job.run(context.Background(), pool, s, videoID, tc.key, tc.contentType)

				got := readEditRow(t, pool, videoID)
				if got.status != "ready" || got.duration != 4 || got.fileSize != int64(len("edited")) {
					t.Errorf("row after edit = %+v, want ready, 4s, %d bytes", got, len("edited"))
				}
				if got.fileKey == tc.key || !strings.HasPrefix(got.fileKey, "recordings/u/tok.") || !strings.HasSuffix(got.fileKey, tc.ext) {
					t.Errorf("file_key = %q, want a new key next to %q ending %s", got.fileKey, tc.key, tc.ext)
				}
				objects := s.snapshot()
				if objects[got.fileKey] != "edited" || objects[tc.key] != "original" {
					t.Errorf("objects = %v, want the edit and the original kept for issued URLs", objects)
				}
				r := retired(t, pool)
				if due, ok := r[tc.key]; !ok || due < 90*time.Minute || due > 2*time.Hour {
					t.Errorf("original due in %v (tracked %t), want past the one-hour URL lifetime", due, ok)
				}
				if _, ok := r[got.fileKey]; ok {
					t.Errorf("the live edit %s is still on the deletion list", got.fileKey)
				}
			})
		}
	}
}

// A claim held by another job (here: the row already moved to another key and
// processing again) must not be switched or released by this edit, and the
// object it uploaded is due for deletion.
func TestEditLeavesAnotherClaimAlone(t *testing.T) {
	for _, job := range editJobs {
		t.Run(job.name, func(t *testing.T) {
			pool := accountDB(t)
			const key = "recordings/u/tok.webm"
			videoID := seedProcessingVideo(t, pool, key, "video/webm")
			stubEdit(t, "edited")
			s := newMemStorage(map[string]string{key: "original"})
			var uploaded string
			s.onPut = func(k string) {
				uploaded = k
				if _, err := pool.Exec(context.Background(),
					`UPDATE videos SET file_key = 'recordings/u/tok.other.webm' WHERE id = $1`, videoID,
				); err != nil {
					t.Error(err)
				}
			}

			job.run(context.Background(), pool, s, videoID, key, "video/webm")

			got := readEditRow(t, pool, videoID)
			if got.fileKey != "recordings/u/tok.other.webm" || got.duration != 10 || got.status != "processing" {
				t.Errorf("row = %+v, want the other claim untouched", got)
			}
			if due, ok := retired(t, pool)[uploaded]; !ok || due > 0 {
				t.Errorf("discarded upload %s due in %v (tracked %t), want due now", uploaded, due, ok)
			}
		})
	}
}

// An edit that fails before switching releases only its own claim. Edit A's
// switch can commit with the error lost; edit B then claims the row on A's key,
// and A's fallback must not hand B's claim back.
func TestEditFallbackReleasesOnlyItsClaim(t *testing.T) {
	for _, job := range editJobs {
		t.Run(job.name, func(t *testing.T) {
			pool := accountDB(t)
			videoID := seedProcessingVideo(t, pool, "recordings/u/tok.b.webm", "video/webm")
			s := newMemStorage(map[string]string{}) // A's download fails

			job.run(context.Background(), pool, s, videoID, "recordings/u/tok.webm", "video/webm")

			if got := readEditRow(t, pool, videoID); got.status != "processing" {
				t.Errorf("row = %+v, want B's claim still held", got)
			}
		})
	}
}

// A crash between the upload and the switch leaves the row as it was and the
// upload on record, so the sweep reclaims it.
func TestEditCrashAfterUploadIsReclaimed(t *testing.T) {
	pool := accountDB(t)
	const key = "recordings/u/tok.webm"
	videoID := seedProcessingVideo(t, pool, key, "video/webm")
	stubEdit(t, "edited")
	ctx, cancel := context.WithCancel(context.Background())
	s := newMemStorage(map[string]string{key: "original"})
	var uploaded string
	s.onPut = func(k string) { uploaded = k; cancel() }

	TrimVideoAsync(ctx, pool, s, videoID, key, "recordings/u/tok.jpg", "video/webm", 2, 6)

	if got := readEditRow(t, pool, videoID); got.fileKey != key || got.duration != 10 {
		t.Errorf("row = %+v, want it unchanged", got)
	}
	if _, ok := retired(t, pool)[uploaded]; !ok {
		t.Fatalf("stranded upload %s is not on record", uploaded)
	}
	if _, err := pool.Exec(context.Background(), `UPDATE retired_objects SET delete_after = now() - INTERVAL '1 second'`); err != nil {
		t.Fatal(err)
	}
	DeleteRetiredObjects(context.Background(), pool, s)
	if objects := s.snapshot(); len(objects) != 1 || objects[key] != "original" {
		t.Errorf("objects after sweep = %v, want only the original", objects)
	}
}

// failingDeleteStorage refuses to delete the keys in fail.
type failingDeleteStorage struct {
	*memStorage
	fail map[string]bool
}

func (f failingDeleteStorage) DeleteObject(ctx context.Context, key string) error {
	if f.fail == nil || f.fail[key] {
		return fmt.Errorf("storage down")
	}
	return f.memStorage.DeleteObject(ctx, key)
}

func mustExecDB(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func TestDeleteRetiredObjects(t *testing.T) {
	pool := accountDB(t)
	ctx := context.Background()
	seedProcessingVideo(t, pool, "recordings/u/live.webm", "video/webm")
	mustExecDB(t, pool, `INSERT INTO retired_objects (key, delete_after) VALUES
		('recordings/u/live.webm', now() - INTERVAL '1 minute'),
		('recordings/u/old.webm',  now() - INTERVAL '1 minute'),
		('recordings/u/fresh.webm', now() + INTERVAL '1 hour')`)
	s := newMemStorage(map[string]string{
		"recordings/u/live.webm": "live", "recordings/u/old.webm": "old", "recordings/u/fresh.webm": "fresh",
	})

	DeleteRetiredObjects(ctx, pool, failingDeleteStorage{memStorage: s})
	r := retired(t, pool)
	if _, ok := r["recordings/u/live.webm"]; ok || len(r) != 2 || r["recordings/u/old.webm"] <= 0 {
		t.Errorf("after a failed delete: %v, want live dropped and old kept, retried later", r)
	}

	mustExecDB(t, pool, `UPDATE retired_objects SET delete_after = now() - INTERVAL '1 second' WHERE key = 'recordings/u/old.webm'`)
	DeleteRetiredObjects(ctx, pool, s)
	if r := retired(t, pool); len(r) != 1 || r["recordings/u/fresh.webm"] <= 0 {
		t.Errorf("tracked after sweep: %v, want only the not-yet-due key", r)
	}
	if objects := s.snapshot(); len(objects) != 2 || objects["recordings/u/live.webm"] != "live" || objects["recordings/u/fresh.webm"] != "fresh" {
		t.Errorf("objects after sweep = %v, want the live and the not-yet-due ones", objects)
	}
}

// A retired key can still be in use under another column, on the row that
// retired it or on any other.
func TestDeleteRetiredObjectsKeepsEveryReference(t *testing.T) {
	pool := accountDB(t)
	ctx := context.Background()
	a := seedProcessingVideo(t, pool, "recordings/u/a.new.webm", "video/webm")
	mustExecDB(t, pool, `UPDATE videos SET thumbnail_key = 'k/a-thumb', transcript_key = 'k/a-vtt', webcam_key = 'k/a-cam' WHERE id = $1`, a)
	var userID string
	if err := pool.QueryRow(ctx, `SELECT user_id FROM videos WHERE id = $1`, a).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	mustExecDB(t, pool, `INSERT INTO videos (user_id, title, file_key, share_token, content_type, status, thumbnail_key, transcript_key, webcam_key)
		VALUES ($1, 'B', 'k/b-file', 'tokb', 'video/webm', 'ready', 'k/b-thumb', 'k/b-vtt', 'k/b-cam')`, userID)

	referenced := []string{"recordings/u/a.new.webm", "k/a-thumb", "k/a-vtt", "k/a-cam", "k/b-file", "k/b-thumb", "k/b-vtt", "k/b-cam"}
	objects := map[string]string{"k/free": "free"}
	for _, key := range referenced {
		objects[key] = key
		mustExecDB(t, pool, `INSERT INTO retired_objects (key, delete_after) VALUES ($1, now() - INTERVAL '1 minute')`, key)
	}
	mustExecDB(t, pool, `INSERT INTO retired_objects (key, delete_after) VALUES ('k/free', now() - INTERVAL '1 minute')`)
	s := newMemStorage(objects)

	DeleteRetiredObjects(ctx, pool, s)

	left := s.snapshot()
	for _, key := range referenced {
		if _, ok := left[key]; !ok {
			t.Errorf("deleted %s, which a video still references", key)
		}
	}
	if _, ok := left["k/free"]; ok {
		t.Errorf("unreferenced k/free survived the sweep")
	}
}

// A key whose delete keeps failing must not hold up the keys behind it.
func TestDeleteRetiredObjectsIsNotStarvedByFailures(t *testing.T) {
	pool := accountDB(t)
	ctx := context.Background()
	objects := map[string]string{"k/ok": "ok"}
	fail := map[string]bool{}
	for i := 0; i < 100; i++ {
		key := fmt.Sprintf("k/stuck-%03d", i)
		objects[key], fail[key] = key, true
		mustExecDB(t, pool, `INSERT INTO retired_objects (key, delete_after) VALUES ($1, now() - INTERVAL '1 hour')`, key)
	}
	mustExecDB(t, pool, `INSERT INTO retired_objects (key, delete_after) VALUES ('k/ok', now() - INTERVAL '1 minute')`)
	s := failingDeleteStorage{memStorage: newMemStorage(objects), fail: fail}

	DeleteRetiredObjects(ctx, pool, s)
	DeleteRetiredObjects(ctx, pool, s)

	if _, ok := s.snapshot()["k/ok"]; ok {
		t.Error("k/ok is still there after two sweeps behind 100 failing keys")
	}
	if r := retired(t, pool); len(r) != 100 || r["k/stuck-000"] <= 0 {
		t.Errorf("failing keys: %d tracked, first due in %v; want all kept, retried later", len(r), r["k/stuck-000"])
	}
}

// The switch adopts the replacement only while its attempt record exists. Once
// the sweep has claimed the record (and is about to delete the object), the
// switch must not point the video at it.
func TestSwitchRequiresTheAttemptRecord(t *testing.T) {
	pool := accountDB(t)
	ctx := context.Background()
	const key = "recordings/u/tok.webm"
	videoID := seedProcessingVideo(t, pool, key, "video/webm")
	const update = `UPDATE videos SET file_key = $3, status = 'ready' WHERE id = $1 AND file_key = $2 AND status = 'processing'`

	// No attempt record: the sweep got there first.
	switched, err := switchFileKey(ctx, pool, update, videoID, key, "recordings/u/tok.swept.webm")
	if err != nil || switched {
		t.Fatalf("switch without an attempt record = %t, %v; want no switch", switched, err)
	}
	if got := readEditRow(t, pool, videoID); got.fileKey != key || got.status != "processing" {
		t.Errorf("row = %+v, want it untouched", got)
	}

	if err := recordReplacementAttempt(ctx, pool, "recordings/u/tok.kept.webm"); err != nil {
		t.Fatal(err)
	}
	switched, err = switchFileKey(ctx, pool, update, videoID, key, "recordings/u/tok.kept.webm")
	if err != nil || !switched {
		t.Fatalf("switch with an attempt record = %t, %v; want a switch", switched, err)
	}
	if r := retired(t, pool); len(r) != 1 || r[key] <= 0 {
		t.Errorf("tracked = %v, want only the retired original", r)
	}
}

// keyMovesAfterRead lets the edit endpoints read the video, then switches its
// file_key the way a conversion finishing at that moment would.
type keyMovesAfterRead struct {
	*pgxpool.Pool
	move func()
}

type movingRow struct {
	pgx.Row
	move func()
}

func (r movingRow) Scan(dest ...any) error {
	err := r.Row.Scan(dest...)
	r.move()
	return err
}

func (d keyMovesAfterRead) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return movingRow{d.Pool.QueryRow(ctx, sql, args...), d.move}
}

func TestEditEndpointsClaimTheKeyTheyRead(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		serve      func(h *Handler, w http.ResponseWriter, r *http.Request)
	}{
		{"trim", `{"startSeconds": 1, "endSeconds": 5}`, func(h *Handler, w http.ResponseWriter, r *http.Request) { h.Trim(w, r) }},
		{"remove-segments", `{"segments": [{"start": 1, "end": 2}]}`, func(h *Handler, w http.ResponseWriter, r *http.Request) { h.RemoveSegments(w, r) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool := accountDB(t)
			const key = "recordings/u/tok.webm"
			videoID := seedProcessingVideo(t, pool, key, "video/webm")
			mustExecDB(t, pool, `UPDATE videos SET status = 'ready' WHERE id = $1`, videoID)
			var userID string
			if err := pool.QueryRow(context.Background(), `SELECT user_id FROM videos WHERE id = $1`, videoID).Scan(&userID); err != nil {
				t.Fatal(err)
			}
			db := keyMovesAfterRead{pool, func() {
				mustExecDB(t, pool, `UPDATE videos SET file_key = 'recordings/u/tok.conv.mp4' WHERE id = $1`, videoID)
			}}
			h := NewHandler(db, newMemStorage(map[string]string{}), testBaseURL, 0, 0, 0, 0, testHMACSecret, false)
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
			req = withURLParam(req.WithContext(auth.ContextWithUserID(req.Context(), userID)), "id", videoID)
			rec := httptest.NewRecorder()

			tc.serve(h, rec, req)

			if rec.Code != http.StatusConflict {
				t.Errorf("status = %d (%s), want 409 when the key moved after the read", rec.Code, rec.Body.String())
			}
			if got := readEditRow(t, pool, videoID); got.status != "ready" {
				t.Errorf("row = %+v, want it left ready for the next edit", got)
			}
		})
	}
}

// Playlists built the URL from the share token, which stops matching the
// object as soon as an edit moves it.
func TestPlaylistPlaysTheStoredFileKey(t *testing.T) {
	pool := accountDB(t)
	videoID := seedProcessingVideo(t, pool, "recordings/u/tok.1a2b.webm", "video/webm")
	ctx := context.Background()
	var userID, playlistID string
	if err := pool.QueryRow(ctx, `UPDATE videos SET status = 'ready' WHERE id = $1 RETURNING user_id`, videoID).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`INSERT INTO playlists (user_id, title, share_token) VALUES ($1, 'P', 'pltok') RETURNING id`, userID,
	).Scan(&playlistID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO playlist_videos (playlist_id, video_id, position) VALUES ($1, $2, 0)`, playlistID, videoID); err != nil {
		t.Fatal(err)
	}

	h := NewHandler(pool, newMemStorage(nil), testBaseURL, 0, 0, 0, 0, testHMACSecret, false)
	items, err := h.loadPlaylistVideos(ctx, playlistID)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].VideoURL != "https://storage.test/recordings/u/tok.1a2b.webm" {
		t.Errorf("playlist items = %+v, want the stored file_key", items)
	}
}
