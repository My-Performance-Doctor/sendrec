package video

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func seedManagedAdapter(t *testing.T) (*pgxpool.Pool, *Handler, *MPDAdapter, string, string) {
	t.Helper()
	db := accountDB(t)
	ctx := context.Background()
	var user, org, id, share string
	for _, q := range []struct {
		sql string
		out *string
	}{
		{`INSERT INTO users(email,password,name) VALUES('adapter@example.test','','Synthetic') RETURNING id`, &user},
		{`INSERT INTO organizations(name,slug) VALUES('Synthetic','adapter') RETURNING id`, &org},
	} {
		if err := db.QueryRow(ctx, q.sql).Scan(q.out); err != nil {
			t.Fatal(err)
		}
	}
	tenant := "33333333-3333-4333-8333-333333333333"
	mustExec(t, db, `INSERT INTO mpd_managed_workspaces(organization_id,tenant_id,issuer,enabled,seat_limit) VALUES($1,$2,'https://issuer.example.test',true,10)`, org, tenant)
	mustExec(t, db, `INSERT INTO mpd_external_identities(issuer,subject,user_id,organization_id,staff_id,tenant_id) VALUES('https://issuer.example.test','synthetic',$1,$2,'22222222-2222-4222-8222-222222222222',$3)`, user, org, tenant)
	if err := db.QueryRow(ctx, `INSERT INTO videos(user_id,organization_id,title,file_key,share_token,status) VALUES($1,$2,'Synthetic','synthetic.mp4',gen_random_uuid()::text,'ready') RETURNING id,share_token`, user, org).Scan(&id, &share); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(db, newMemStorage(map[string]string{"synthetic.mp4": "synthetic bytes"}), "https://sendrec.example.test", 10000000, 0, 0, 0, "synthetic-hmac-key", true)
	sum := sha256.Sum256([]byte(strings.Repeat("s", 32)))
	return db, h, h.MPDAdapter(MPDServiceConfig{Enabled: true, WorkspaceID: org, TenantID: tenant, TokenHashes: []string{hex.EncodeToString(sum[:])}, RequirePassword: true}), id, share
}
func adapterRequest(a *MPDAdapter, id, op, body, work, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("PUT", "/api/integrations/mpd/videos/"+id+"/"+op, strings.NewReader(body))
	r = withURLParam(r, "videoId", id)
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("s", 32))
	r.Header.Set("X-MPD-Work-Version", work)
	r.Header.Set("Idempotency-Key", key)
	fn := a.Password
	if op == "publication" {
		fn = a.Publication
	}
	w := httptest.NewRecorder()
	a.Authenticate(http.HandlerFunc(fn)).ServeHTTP(w, r)
	return w
}
func TestMPDAdapterWorkFenceAndImmutableReceipt(t *testing.T) {
	db, _, a, id, _ := seedManagedAdapter(t)
	for _, c := range []struct {
		body, version, key string
		want               int
	}{
		{`{"mediaVersion":1,"password":"first synthetic"}`, "10", "one", 200},
		{`{"mediaVersion":1,"password":"latest synthetic"}`, "20", "two", 200},
		{`{"mediaVersion":1,"password":"first synthetic"}`, "10", "one", 409},
		{`{"mediaVersion":1,"password":"first synthetic"}`, "30", "one", 200},
		{`{"mediaVersion":1,"password":"changed"}`, "30", "one", 409},
		{`{"mediaVersion":2,"password":"changed"}`, "40", "three", 409},
	} {
		w := adapterRequest(a, id, "password", c.body, c.version, c.key)
		if w.Code != c.want {
			t.Fatalf("%s %s => %d %s", c.version, c.key, w.Code, w.Body.String())
		}
	}
	var password string
	if err := db.QueryRow(context.Background(), `SELECT share_password FROM videos WHERE id=$1`, id).Scan(&password); err != nil {
		t.Fatal(err)
	}
	if !checkSharePassword(password, "latest synthetic") {
		t.Fatal("replay or old worker replaced latest password")
	}
	w := adapterRequest(a, id, "publication", `{"mediaVersion":1,"published":true}`, "31", "publish")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var metadata MPDMetadata
	if json.Unmarshal(w.Body.Bytes(), &metadata) != nil || !metadata.Published || metadata.WatchURL == nil || metadata.PublishedAt == nil {
		t.Fatal("invalid metadata", w.Body.String())
	}
	// Current receipt replay must return the original response without publishing again.
	w = adapterRequest(a, id, "publication", `{"mediaVersion":1,"published":false}`, "32", "unpublish")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = adapterRequest(a, id, "publication", `{"mediaVersion":1,"published":true}`, "33", "publish")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var published bool
	if err := db.QueryRow(context.Background(), `SELECT published FROM mpd_video_state WHERE video_id=$1`, id).Scan(&published); err != nil || published {
		t.Fatal("receipt replay republished", err)
	}
}
func TestMPDAdapterScopeCredentialAndPublicationPolicy(t *testing.T) {
	_, _, a, id, _ := seedManagedAdapter(t)
	if w := adapterRequest(a, id, "publication", `{"mediaVersion":1,"published":true}`, "1", "publish"); w.Code != 409 {
		t.Fatal("unprotected publish", w.Code)
	}
	r := withURLParam(httptest.NewRequest("GET", "/", nil), "videoId", id)
	w := httptest.NewRecorder()
	a.Authenticate(http.HandlerFunc(a.Metadata)).ServeHTTP(w, r)
	if w.Code != 401 {
		t.Fatal("missing credential accepted")
	}
	r.Header.Set("Authorization", "Bearer "+strings.Repeat("s", 32))
	a.cfg.WorkspaceID = "44444444-4444-4444-8444-444444444444"
	w = httptest.NewRecorder()
	a.Authenticate(http.HandlerFunc(a.Metadata)).ServeHTTP(w, r)
	if w.Code != 404 {
		t.Fatal("wrong workspace", w.Code)
	}
}
func TestMPDPublicPolicyCoversAuxiliaryPathsAndPassword(t *testing.T) {
	db, h, _, id, share := seedManagedAdapter(t)
	paths := []string{"/watch/", "/embed/", "/api/watch/"}
	for _, prefix := range paths {
		for _, suffix := range []string{"", "/download", "/thumbnail", "/comments", "/milestone", "/segments", "/verify", "/identify", "/cta-click"} {
			w := httptest.NewRecorder()
			r := withURLParam(httptest.NewRequest("GET", prefix+share+suffix, nil), "shareToken", share)
			h.MPDPublicAccess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, r)
			if w.Code != 404 {
				t.Fatalf("unpublished %s => %d", r.URL.Path, w.Code)
			}
		}
	}
	hash, _ := hashSharePassword("synthetic protected")
	mustExec(t, db, `UPDATE videos SET share_password=$2 WHERE id=$1`, id, hash)
	mustExec(t, db, `UPDATE mpd_video_state SET published=true WHERE video_id=$1`, id)
	r := withURLParam(httptest.NewRequest("GET", "/api/watch/"+share+"/thumbnail", nil), "shareToken", share)
	handler := h.MPDPublicAccess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("password bypass", w.Code)
	}
	r.AddCookie(&http.Cookie{Name: watchCookieName(share), Value: signWatchCookie(h.hmacSecret, share, hash)})
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatal("positive password failed", w.Code)
	}
}

func TestMPDPlaylistEnforcesEachManagedItemPolicy(t *testing.T) {
	db, h, _, id, share := seedManagedAdapter(t)
	var playlist string
	if err := db.QueryRow(context.Background(), `INSERT INTO playlists(user_id,title,share_token,is_shared) SELECT user_id,'Synthetic','synthetic-playlist',true FROM videos WHERE id=$1 RETURNING id`, id).Scan(&playlist); err != nil {
		t.Fatal(err)
	}
	mustExec(t, db, `INSERT INTO playlist_videos(playlist_id,video_id) VALUES($1,$2)`, playlist, id)
	handler := h.MPDPublicAccess(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	check := func(want int, cookies ...*http.Cookie) {
		t.Helper()
		r := withURLParam(httptest.NewRequest("GET", "/watch/playlist/synthetic-playlist", nil), "shareToken", "synthetic-playlist")
		for _, c := range cookies {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("playlist got %d want %d: %s", w.Code, want, w.Body.String())
		}
	}
	check(403)
	hash, _ := hashSharePassword("synthetic protected")
	mustExec(t, db, `UPDATE videos SET share_password=$2,email_gate_enabled=true WHERE id=$1`, id, hash)
	mustExec(t, db, `UPDATE mpd_video_state SET published=true WHERE video_id=$1`, id)
	check(403)
	password := &http.Cookie{Name: watchCookieName(share), Value: signWatchCookie(h.hmacSecret, share, hash)}
	email := &http.Cookie{Name: emailGateCookieName(share), Value: signEmailGateCookie(h.hmacSecret, share, "synthetic@example.test")}
	check(403, password)
	check(204, password, email)
	mustExec(t, db, `UPDATE videos SET share_expires_at=now()-interval '1 second' WHERE id=$1`, id)
	check(403, password, email)
}

func TestMPDServiceTokenRotation(t *testing.T) {
	_, _, a, id, _ := seedManagedAdapter(t)
	next := strings.Repeat("n", 32)
	sum := sha256.Sum256([]byte(next))
	nextHash := hex.EncodeToString(sum[:])
	a.cfg.TokenHashes = append(a.cfg.TokenHashes, nextHash)
	call := func(token string, want int) {
		t.Helper()
		r := withURLParam(httptest.NewRequest("GET", "/api/integrations/mpd/videos/"+id, nil), "videoId", id)
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		a.Authenticate(http.HandlerFunc(a.Metadata)).ServeHTTP(w, r)
		if w.Code != want {
			t.Fatalf("token rotation got %d want %d", w.Code, want)
		}
	}
	call(strings.Repeat("s", 32), 200)
	call(next, 200)
	call(strings.Repeat("x", 32), 401)
	a.cfg.TokenHashes = []string{nextHash}
	call(strings.Repeat("s", 32), 401)
	call(next, 200)
	a.cfg.Enabled = false
	call(next, 401)
}
