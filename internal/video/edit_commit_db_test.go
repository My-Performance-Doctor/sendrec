package video

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
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

// BG-10: the edit used to overwrite the only copy of the video and then update
// the row. Now it lands under a new key, the row switches to it together with
// the new duration, and only then does the original go.
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
				// strand it: the row and the original must still agree.
				s.onPut = func(key string) {
					if key == tc.key {
						t.Errorf("edit overwrote the original at %s", key)
					}
					if got := readEditRow(t, pool, videoID); got.fileKey != tc.key || got.duration != 10 {
						t.Errorf("row changed before the edit was stored: %+v", got)
					}
					if s.snapshot()[tc.key] != "original" {
						t.Errorf("original gone before the row switched")
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
				if objects[got.fileKey] != "edited" {
					t.Errorf("row points at %q, which holds %q", got.fileKey, objects[got.fileKey])
				}
				if _, ok := objects[tc.key]; ok {
					t.Errorf("original %s left behind after the switch", tc.key)
				}
			})
		}
	}
}

// When the row is no longer the one the edit started from, the edit must not
// take it over, and the object it uploaded belongs to nobody.
func TestEditDoesNotSwitchAMovedRow(t *testing.T) {
	for _, job := range editJobs {
		t.Run(job.name, func(t *testing.T) {
			pool := accountDB(t)
			const key = "recordings/u/tok.webm"
			videoID := seedProcessingVideo(t, pool, key, "video/webm")
			stubEdit(t, "edited")
			s := newMemStorage(map[string]string{key: "original"})
			s.onPut = func(string) {
				if _, err := pool.Exec(context.Background(),
					`UPDATE videos SET file_key = 'recordings/u/tok.mp4', content_type = 'video/mp4' WHERE id = $1`, videoID,
				); err != nil {
					t.Fatal(err)
				}
			}

			job.run(context.Background(), pool, s, videoID, key, "video/webm")

			got := readEditRow(t, pool, videoID)
			if got.fileKey != "recordings/u/tok.mp4" || got.duration != 10 || got.status != "ready" {
				t.Errorf("row = %+v, want the moved key, the old duration and ready", got)
			}
			if objects := s.snapshot(); len(objects) != 1 || objects[key] != "original" {
				t.Errorf("objects = %v, want only the untouched original", objects)
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
