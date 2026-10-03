package video

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sendrec/sendrec/internal/auth"
)

func TestAnalyticsDashboardDB_TotalVideosSkipsDeleted(t *testing.T) {
	pool := brandingTestDB(t)
	userID, _ := seedUserAndOrg(t, pool)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `DELETE FROM videos WHERE user_id = $1`, userID); err != nil {
		t.Fatalf("reset videos: %v", err)
	}
	for i, status := range []string{"ready", "deleted"} {
		if _, err := pool.Exec(ctx,
			`INSERT INTO videos (user_id, title, file_key, share_token, status)
			 VALUES ($1, 'v', $2, $2, $3)`,
			userID, t.Name()+string(rune('a'+i)), status,
		); err != nil {
			t.Fatalf("insert video: %v", err)
		}
	}

	h := NewHandler(pool, &mockStorage{}, "https://app.sendrec.eu", 0, 0, 0, 0, "secret", false)
	req := httptest.NewRequest(http.MethodGet, "/api/analytics/dashboard?range=7d", nil)
	req = req.WithContext(auth.ContextWithUserID(req.Context(), userID))
	rec := httptest.NewRecorder()
	h.AnalyticsDashboard(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var resp dashboardResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Summary.TotalVideos != 1 {
		t.Errorf("totalVideos = %d, want 1 (deleted videos excluded)", resp.Summary.TotalVideos)
	}
}
