package video

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/sendrec/sendrec/internal/auth"
)

// Regression tests for the 2026-10-22 audit: batch tags must stay inside the
// caller's scope, and personal scope (no X-Organization-Id) must never reach a
// video recorded in a workspace, even by the user who recorded it.

func callAs(h http.HandlerFunc, method, userID, videoID string, body any) *httptest.ResponseRecorder {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, "/", &buf)
	ctx := auth.ContextWithUserID(req.Context(), userID)
	if videoID != "" {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", videoID)
		ctx = context.WithValue(ctx, chi.RouteCtxKey, rctx)
	}
	rec := httptest.NewRecorder()
	h(rec, req.WithContext(ctx))
	return rec
}

func TestBatchSetTags_LeavesOtherUsersVideosAlone(t *testing.T) {
	pool := accountDB(t)
	name := uniqueName(t)
	alice := mustID(t, pool, `INSERT INTO users (email, password, name) VALUES ($1, 'x', 'Alice') RETURNING id`, name+"-alice@example.com")
	bob := mustID(t, pool, `INSERT INTO users (email, password, name) VALUES ($1, 'x', 'Bob') RETURNING id`, name+"-bob@example.com")
	aliceVideo := seedVideo(t, pool, alice, nil, name+"/alice")
	bobVideo := seedVideo(t, pool, bob, nil, name+"/bob")
	aliceTag := mustID(t, pool, `INSERT INTO tags (user_id, name) VALUES ($1, 'kept') RETURNING id`, alice)
	bobTag := mustID(t, pool, `INSERT INTO tags (user_id, name) VALUES ($1, 'planted') RETURNING id`, bob)
	mustExec(t, pool, `INSERT INTO video_tags (video_id, tag_id) VALUES ($1, $2)`, aliceVideo, aliceTag)

	h := NewHandler(pool, &mockStorage{}, "https://example.com", 0, 0, 0, 0, "secret", false)
	rec := callAs(h.BatchSetTags, http.MethodPost, bob, "", batchTagsRequest{
		VideoIDs: []string{bobVideo, aliceVideo},
		TagIDs:   []string{bobTag},
	})

	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", rec.Code, rec.Body.String())
	}
	if !exists(t, pool, `SELECT 1 FROM video_tags WHERE video_id = $1 AND tag_id = $2`, aliceVideo, aliceTag) {
		t.Error("Alice's tag was removed from her video")
	}
	if exists(t, pool, `SELECT 1 FROM video_tags WHERE tag_id = $1`, bobTag) {
		t.Error("Bob's tag was attached despite the 404")
	}

	// Repeating an in-scope ID still succeeds.
	rec = callAs(h.BatchSetTags, http.MethodPost, bob, "", batchTagsRequest{
		VideoIDs: []string{bobVideo, bobVideo},
		TagIDs:   []string{bobTag},
	})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("own videos: want 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if !exists(t, pool, `SELECT 1 FROM video_tags WHERE video_id = $1 AND tag_id = $2`, bobVideo, bobTag) {
		t.Error("Bob's own video was not tagged")
	}
}

func TestPersonalScope_SkipsWorkspaceVideos(t *testing.T) {
	pool := accountDB(t)
	name := uniqueName(t)
	owner := mustID(t, pool, `INSERT INTO users (email, password, name) VALUES ($1, 'x', 'Owner') RETURNING id`, name+"-owner@example.com")
	former := mustID(t, pool, `INSERT INTO users (email, password, name) VALUES ($1, 'x', 'Former') RETURNING id`, name+"-former@example.com")
	team := mustID(t, pool, `INSERT INTO organizations (name, slug) VALUES ('Team', $1) RETURNING id`, name+"-team")
	// The former member was removed, so only the owner is left; the video they
	// recorded there still carries their user_id.
	mustExec(t, pool, `INSERT INTO organization_members (organization_id, user_id, role) VALUES ($1, $2, 'owner')`, team, owner)
	video := seedVideo(t, pool, former, &team, name+"/team")
	folder := mustID(t, pool, `INSERT INTO folders (user_id, name) VALUES ($1, 'mine') RETURNING id`, former)
	tag := mustID(t, pool, `INSERT INTO tags (user_id, name) VALUES ($1, 'mine') RETURNING id`, former)

	storage := &mockStorage{deleteCalled: make(chan string, 64)}
	h := NewHandler(pool, storage, "https://example.com", 0, 0, 0, 0, "secret", false)

	// Cases run in order as the slice is built.
	cases := []struct {
		name string
		rec  *httptest.ResponseRecorder
		want int
	}{
		{"batch folder", callAs(h.BatchSetFolder, http.MethodPost, former, "", batchFolderRequest{VideoIDs: []string{video}, FolderID: &folder}), http.StatusNoContent},
		{"batch tags", callAs(h.BatchSetTags, http.MethodPost, former, "", batchTagsRequest{VideoIDs: []string{video}, TagIDs: []string{tag}}), http.StatusNotFound},
		{"folder", callAs(h.SetVideoFolder, http.MethodPut, former, video, map[string]any{"folderId": folder}), http.StatusNotFound},
		{"tags", callAs(h.SetVideoTags, http.MethodPut, former, video, map[string]any{"tagIds": []string{tag}}), http.StatusNotFound},
		{"notification", callAs(h.SetVideoNotification, http.MethodPut, former, video, map[string]any{"viewNotification": "off"}), http.StatusNotFound},
		{"analytics", callAs(h.Analytics, http.MethodGet, former, video, nil), http.StatusNotFound},
		// Last, so a delete cannot hide the other cases behind status = 'deleted'.
		{"batch delete", callAs(h.BatchDelete, http.MethodPost, former, "", batchRequest{VideoIDs: []string{video}}), http.StatusOK},
	}
	for _, c := range cases {
		if c.rec.Code != c.want {
			t.Errorf("%s: want %d, got %d: %s", c.name, c.want, c.rec.Code, c.rec.Body.String())
		}
	}

	var status string
	var folderID, notification *string
	if err := pool.QueryRow(context.Background(),
		`SELECT status, folder_id::text, view_notification FROM videos WHERE id = $1`, video,
	).Scan(&status, &folderID, &notification); err != nil {
		t.Fatal(err)
	}
	if status == "deleted" {
		t.Error("batch delete without a workspace header deleted a workspace video")
	}
	if folderID != nil {
		t.Error("a personal folder was set on a workspace video")
	}
	if notification != nil {
		t.Errorf("notification setting changed to %q", *notification)
	}
	if exists(t, pool, `SELECT 1 FROM video_tags WHERE video_id = $1`, video) {
		t.Error("a personal tag was attached to a workspace video")
	}
	var deletedResp batchDeleteResponse
	if err := json.Unmarshal(cases[len(cases)-1].rec.Body.Bytes(), &deletedResp); err == nil && deletedResp.Deleted != 0 {
		t.Errorf("batch delete reported %d deleted", deletedResp.Deleted)
	}
}
