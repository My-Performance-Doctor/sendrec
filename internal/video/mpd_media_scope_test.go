package video

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/sendrec/sendrec/internal/auth"
	"github.com/sendrec/sendrec/internal/database"
	"github.com/sendrec/sendrec/internal/mpd"
)

type expiryStorage struct {
	*mockStorage
	expiries []time.Duration
}

func (s *expiryStorage) GenerateDownloadURL(_ context.Context, _ string, expiry time.Duration) (string, error) {
	s.expiries = append(s.expiries, expiry)
	return "https://storage.example.test/media", nil
}
func (s *expiryStorage) GenerateDownloadURLWithDisposition(ctx context.Context, key, _ string, expiry time.Duration) (string, error) {
	return s.GenerateDownloadURL(ctx, key, expiry)
}

type countedMediaDB struct {
	database.DBTX
	queries, rows int
}

func (d *countedMediaDB) Query(ctx context.Context, q string, args ...any) (pgx.Rows, error) {
	d.queries++
	return d.DBTX.Query(ctx, q, args...)
}
func (d *countedMediaDB) QueryRow(ctx context.Context, q string, args ...any) pgx.Row {
	d.rows++
	return d.DBTX.QueryRow(ctx, q, args...)
}

func TestMPDMediaScopePersistsWithoutFeatureFlags(t *testing.T) {
	db, h, _, id, share := seedManagedAdapter(t)
	ctx := context.Background()
	s := &expiryStorage{mockStorage: &mockStorage{}}
	h.storage = s
	h.BoundMPDMediaURLs()
	for _, c := range []struct {
		key  string
		want time.Duration
	}{{"synthetic.mp4", 5 * time.Minute}, {"legacy.mp4", time.Hour}, {"branding/logo.png", time.Hour}} {
		if _, err := h.storage.GenerateDownloadURL(ctx, c.key, time.Hour); err != nil {
			t.Fatal(err)
		}
		if got := s.expiries[len(s.expiries)-1]; got != c.want {
			t.Fatalf("%s expiry %s, want %s", c.key, got, c.want)
		}
		if _, err := h.storage.GenerateDownloadURLWithDisposition(ctx, c.key, "download.mp4", time.Hour); err != nil {
			t.Fatal(err)
		}
		if got := s.expiries[len(s.expiries)-1]; got != c.want {
			t.Fatalf("disposition %s expiry %s", c.key, got)
		}
	}
	mustExec(t, db, `UPDATE mpd_video_state SET published=true WHERE video_id=$1`, id)
	// No enabled MPD handler/config is needed for persisted public policy.
	for _, token := range []string{share, "legacy-token"} {
		req := withURLParam(httptest.NewRequest("GET", "/watch/"+token, nil), "shareToken", token)
		rec := httptest.NewRecorder()
		h.MPDPublicAccess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := managedShare(r.Context(), token); got != (token == share) {
				t.Errorf("token scope = %v", got)
			}
			w.WriteHeader(204)
		})).ServeHTTP(rec, req)
		if rec.Code != 204 {
			t.Fatalf("public scope response %d %s", rec.Code, rec.Body.String())
		}
	}
}

func TestMPDManagedListLoadsStateOnce(t *testing.T) {
	db, h, _, id, _ := seedManagedAdapter(t)
	ctx := context.Background()
	var owner, org string
	if err := db.QueryRow(ctx, `SELECT user_id,organization_id FROM videos WHERE id=$1`, id).Scan(&owner, &org); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `UPDATE videos SET thumbnail_key='synthetic.jpg' WHERE id=$1`, id)
	mustExec(t, db, `INSERT INTO videos(user_id,organization_id,title,file_key,thumbnail_key,share_token,status) SELECT user_id,organization_id,'Second','second.mp4','second.jpg',gen_random_uuid()::text,'ready' FROM videos WHERE id=$1`, id)
	counted := &countedMediaDB{DBTX: db}
	h.db = counted
	store := &expiryStorage{mockStorage: &mockStorage{}}
	h.storage = store
	h.BoundMPDMediaURLs()
	principal := &mpd.Principal{UserID: owner, OrganizationID: org}
	req := httptest.NewRequest("GET", "/api/videos", nil).WithContext(mpd.ContextWithPrincipal(auth.ContextWithOrg(auth.ContextWithUserID(ctx, owner), org, "admin"), principal))
	rec := httptest.NewRecorder()
	h.List(rec, req)
	if rec.Code != 200 {
		t.Fatalf("list %d %s", rec.Code, rec.Body.String())
	}
	var items []listItem
	if err := json.Unmarshal(rec.Body.Bytes(), &items); err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || counted.queries != 1 || counted.rows != 0 {
		t.Fatalf("items=%d list queries=%d extra rows=%d", len(items), counted.queries, counted.rows)
	}
	for _, item := range items {
		if !item.Managed || item.MediaVersion != 1 || item.ShareURL != "" {
			t.Fatalf("managed state missing: %+v", item)
		}
	}
	for _, expiry := range store.expiries {
		if expiry != 5*time.Minute {
			t.Fatalf("managed thumbnail expiry %s", expiry)
		}
	}
}

func TestMPDMixedPlaylistScopesEachPersistedRecording(t *testing.T) {
	db, h, _, id, _ := seedManagedAdapter(t)
	ctx := context.Background()
	var owner, legacy, playlist string
	if err := db.QueryRow(ctx, `INSERT INTO users(email,password,name) VALUES('legacy@example.test','','Legacy') RETURNING id`).Scan(&owner); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `INSERT INTO videos(user_id,title,file_key,share_token,status) VALUES($1,'Legacy','legacy.mp4','legacy-token','ready') RETURNING id`, owner).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `INSERT INTO playlists(user_id,title,share_token,is_shared) VALUES($1,'Mixed','mixed-token',true) RETURNING id`, owner).Scan(&playlist); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO playlist_videos(playlist_id,video_id,position) VALUES($1,$2,0),($1,$3,1)`, playlist, id, legacy)
	mustExec(t, db, `UPDATE mpd_video_state SET published=true WHERE video_id=$1`, id)
	store := &expiryStorage{mockStorage: &mockStorage{}}
	h.storage = store
	h.BoundMPDMediaURLs()
	req := withURLParam(httptest.NewRequest("GET", "/watch/playlist/mixed-token", nil), "shareToken", "mixed-token")
	rec := httptest.NewRecorder()
	h.MPDPublicAccess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		items, err := h.loadPlaylistVideos(r.Context(), playlist)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 2 || !items[0].Managed || items[1].Managed {
			t.Fatalf("playlist scope %+v", items)
		}
		w.WriteHeader(204)
	})).ServeHTTP(rec, req)
	if rec.Code != 204 {
		t.Fatalf("playlist response %d %s", rec.Code, rec.Body.String())
	}
	if len(store.expiries) != 2 || store.expiries[0] != 5*time.Minute || store.expiries[1] != time.Hour {
		t.Fatalf("playlist expiry %v", store.expiries)
	}
}
