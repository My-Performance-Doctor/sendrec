package video

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// The title backfill used to pick the same row every ten seconds when the AI
// call failed, or when the SQL and Go "auto title" checks disagreed, paying
// for a call each time. A dismissed suggestion was regenerated the same way.
// Each video now gets one attempt. BG-12, audit 3.6.
func TestProcessNextTitleSuggestion_DB_TriesEachVideoOnce(t *testing.T) {
	pool := accountDB(t)
	name := uniqueName(t)
	user := mustID(t, pool, `INSERT INTO users (email, password, name) VALUES ($1, 'x', 'U') RETURNING id`, name+"@example.com")
	for _, title := range []string{"Recording 2/20/2026 3:45:12 PM", "Recording notes"} {
		id := seedVideo(t, pool, user, nil, name+"/"+title)
		mustExec(t, pool, `UPDATE videos SET title = $2, summary_status = 'ready',
			transcript_json = '[{"start":0,"end":1,"text":"a"},{"start":1,"end":2,"text":"b"}]' WHERE id = $1`, id, title)
	}

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer srv.Close()
	ai := NewAIClient(srv.URL, "key", "model", 0)

	for range 5 {
		processNextTitleSuggestion(context.Background(), pool, ai)
	}

	if n := calls.Load(); n != 1 {
		t.Errorf("want one AI call for the one auto-titled video, got %d", n)
	}
}
