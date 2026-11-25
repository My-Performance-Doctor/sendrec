package video

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	objectstore "github.com/sendrec/sendrec/internal/storage"
)

type typedStorage struct {
	*memStorage
	contentType string
}

func (s typedStorage) HeadObject(ctx context.Context, key string) (int64, string, error) {
	size, _, err := s.memStorage.HeadObject(ctx, key)
	if err != nil {
		return 0, "", fmt.Errorf("head object: %w", objectstore.ErrObjectNotFound)
	}
	return size, s.contentType, nil
}

const importToken = "synthetic-import-token-0123456789abcdef"

func importFixture(t *testing.T, contentType string, files map[string]string) (*MPDImporter, func(string) string) {
	t.Helper()
	pool := accountDB(t)
	user := mustID(t, pool, `INSERT INTO users(email,password,name,retention_days) VALUES('import@example.test','','Synthetic',0) RETURNING id`)
	org := mustID(t, pool, `INSERT INTO organizations(name,slug,retention_days) VALUES('Synthetic','synthetic',0) RETURNING id`)
	mustExec(t, pool, `INSERT INTO mpd_managed_workspaces(organization_id,tenant_id,issuer,enabled,seat_limit) VALUES($1,'33333333-3333-4333-8333-333333333333','https://issuer.example.test',true,10)`, org)
	mustExec(t, pool, `INSERT INTO mpd_external_identities(issuer,subject,user_id,organization_id,staff_id,tenant_id) VALUES('https://issuer.example.test','import',$1,$2,'22222222-2222-4222-8222-222222222222','33333333-3333-4333-8333-333333333333')`, user, org)
	h := NewHandler(pool, typedStorage{newMemStorage(files), contentType}, "https://sendrec.example.test", 10000000, 0, 0, 0, "synthetic-hmac-key", true)
	sum := sha256.Sum256([]byte(importToken))
	importer := h.MPDImporter(MPDImportConfig{TokenHashes: []string{hex.EncodeToString(sum[:])}, OwnerUserID: user, WorkspaceID: org})

	query := func(sql string) string {
		var out string
		if err := pool.QueryRow(context.Background(), sql).Scan(&out); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return out
	}
	return importer, query
}

func postImport(importer *MPDImporter, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/integrations/mpd/imports", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	importer.Authenticate(http.HandlerFunc(importer.Import)).ServeHTTP(rec, req)
	return rec
}

func TestMPDImportCreatesOneManagedVideoPerVimeoID(t *testing.T) {
	importer, query := importFixture(t, "video/mp4", map[string]string{"imports/vimeo/123.mp4": "synthetic media"})
	body := `{"vimeoVideoId":"123","recordedAt":"2024-05-06T07:08:09Z","durationSeconds":600}`
	first := postImport(importer, importToken, body)
	if first.Code != http.StatusCreated {
		t.Fatalf("first import: %d %s", first.Code, first.Body)
	}
	var created importResponse
	_ = json.Unmarshal(first.Body.Bytes(), &created)
	if !created.Created || created.MediaVersion != 1 {
		t.Fatalf("created = %+v", created)
	}
	// The recording date is kept, and nothing from Vimeo names the video.
	if got := query(`SELECT to_char(created_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS') FROM videos WHERE id='` + created.VideoID + `'`); got != "2024-05-06T07:08:09" {
		t.Fatalf("created_at = %s", got)
	}
	if got := query(`SELECT title||'|'||file_key||'|'||status||'|'||transcript_status FROM videos WHERE id='` + created.VideoID + `'`); got != "MPD result video|imports/vimeo/123.mp4|ready|none" {
		t.Fatalf("video = %s", got)
	}
	if got := query(`SELECT published::text||'|'||owner_staff_id::text FROM mpd_video_state WHERE video_id='` + created.VideoID + `'`); got != "false|22222222-2222-4222-8222-222222222222" {
		t.Fatalf("state = %s", got)
	}
	if got := query(`SELECT count(*)::text FROM mpd_event_outbox WHERE video_id='` + created.VideoID + `' AND event_type='video.ready'`); got != "1" {
		t.Fatalf("video.ready events = %s", got)
	}
	// Nothing starts now: the normalize and thumbnail sweeps both pick it up.
	if got := query(`SELECT ios_normalized::text||'|'||(thumbnail_version < media_version)::text FROM videos WHERE id='` + created.VideoID + `'`); got != "false|true" {
		t.Fatalf("waiting for the sweeps = %s", got)
	}
	again := postImport(importer, importToken, body)
	var repeated importResponse
	_ = json.Unmarshal(again.Body.Bytes(), &repeated)
	if again.Code != http.StatusOK || repeated.VideoID != created.VideoID || repeated.Created {
		t.Fatalf("rerun: %d %+v", again.Code, repeated)
	}
	if got := query(`SELECT count(*)::text FROM videos`); got != "1" {
		t.Fatalf("videos = %s", got)
	}
	// A deleted import is refused: its pending file purge could take a reimport's file.
	if _, err := importer.h.db.Exec(context.Background(), `UPDATE videos SET status='deleted' WHERE id=$1`, created.VideoID); err != nil {
		t.Fatal(err)
	}
	if refused := postImport(importer, importToken, body); refused.Code != http.StatusConflict {
		t.Fatalf("reimport of a deleted video: %d %s", refused.Code, refused.Body)
	}
}

func TestMPDImportRefusesWithoutTokenFileOrValidRequest(t *testing.T) {
	importer, query := importFixture(t, "video/mp4", map[string]string{"imports/vimeo/123.mp4": "synthetic media"})
	cases := []struct {
		name, token, body string
		code              int
	}{
		{"no token", "", `{"vimeoVideoId":"123","recordedAt":"2024-05-06T07:08:09Z","durationSeconds":60}`, http.StatusUnauthorized},
		{"wrong token", strings.Repeat("x", 40), `{"vimeoVideoId":"123","recordedAt":"2024-05-06T07:08:09Z","durationSeconds":60}`, http.StatusUnauthorized},
		{"not a Vimeo id", importToken, `{"vimeoVideoId":"12a","recordedAt":"2024-05-06T07:08:09Z","durationSeconds":60}`, http.StatusBadRequest},
		{"no date", importToken, `{"vimeoVideoId":"123","durationSeconds":60}`, http.StatusBadRequest},
		{"no duration", importToken, `{"vimeoVideoId":"123","recordedAt":"2024-05-06T07:08:09Z"}`, http.StatusBadRequest},
		{"future date", importToken, `{"vimeoVideoId":"123","durationSeconds":60,"recordedAt":"` + time.Now().Add(48*time.Hour).UTC().Format(time.RFC3339) + `"}`, http.StatusBadRequest},
		{"trailing content", importToken, `{"vimeoVideoId":"123","recordedAt":"2024-05-06T07:08:09Z","durationSeconds":60}{"title":"x"}`, http.StatusBadRequest},
		{"year one", importToken, `{"vimeoVideoId":"123","recordedAt":"0001-01-02T00:00:00Z","durationSeconds":60}`, http.StatusBadRequest},
		{"unknown field", importToken, `{"vimeoVideoId":"123","recordedAt":"2024-05-06T07:08:09Z","durationSeconds":60,"title":"x"}`, http.StatusBadRequest},
		{"file not in place", importToken, `{"vimeoVideoId":"999","recordedAt":"2024-05-06T07:08:09Z","durationSeconds":60}`, http.StatusConflict},
	}
	for _, c := range cases {
		if got := postImport(importer, c.token, c.body); got.Code != c.code {
			t.Fatalf("%s: %d %s", c.name, got.Code, got.Body)
		}
	}
	if got := query(`SELECT count(*)::text FROM videos`); got != "0" {
		t.Fatalf("videos = %s", got)
	}
}

func TestMPDImportRefusesAFileThatIsNotMP4(t *testing.T) {
	importer, _ := importFixture(t, "video/webm", map[string]string{"imports/vimeo/123.mp4": "synthetic media"})
	got := postImport(importer, importToken, `{"vimeoVideoId":"123","recordedAt":"2024-05-06T07:08:09Z","durationSeconds":60}`)
	if got.Code != http.StatusConflict {
		t.Fatalf("webm: %d", got.Code)
	}
}

func TestMPDImportIsOffWithoutItsTokensOrOwner(t *testing.T) {
	for _, cfg := range []MPDImportConfig{{OwnerUserID: "u", WorkspaceID: "w"}, {TokenHashes: []string{"x"}, WorkspaceID: "w"}} {
		if cfg.Enabled() {
			t.Fatalf("enabled: %+v", cfg)
		}
	}
}
