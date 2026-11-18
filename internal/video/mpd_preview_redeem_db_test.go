package video

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sendrec/sendrec/internal/mpd"
)

// slowIdentity checks the staff session as the real one does, after a pause standing in for the
// outbound calls to MPD, and takes its own connection from the pool.
type slowIdentity struct{ syntheticRecordingIdentity }

func (s slowIdentity) ValidateSession(ctx context.Context, id string) (*mpd.Principal, error) {
	time.Sleep(200 * time.Millisecond)
	return s.syntheticRecordingIdentity.ValidateSession(ctx, id)
}

// Redeems hold no connection or lock while the staff session is checked, so more redeems than the pool
// has connections all finish, each spending only its own handoff.
func TestConcurrentRedeemsDoNotExhaustAsmallPool(t *testing.T) {
	db := accountDB(t)
	ctx := context.Background()
	_, user, org := seedManagedLifecycle(t, db, "ready")
	tenant, staff := "33333333-3333-4333-8333-333333333333", "22222222-2222-4222-8222-222222222222"
	sid := mustID(t, db, `INSERT INTO mpd_sessions(user_id,access_encrypted,classification_hash,cognito_expires_at,expires_at,grant_json,grant_expires_at) VALUES($1,'synthetic-unused','synthetic-classification',now()+interval '1 hour',now()+interval '1 hour','{}',now()+interval '1 hour') RETURNING id`, user)
	video := mustID(t, db, `INSERT INTO videos(user_id,organization_id,title,file_key,share_token,status,duration,content_type) VALUES($1,$2,'Synthetic','synthetic-redeem.webm','synthetic-redeem','ready',20,'video/webm') RETURNING id`, user, org)
	mustExec(t, db, `INSERT INTO mpd_video_state(video_id,owner_staff_id,tenant_id,media_version) VALUES($1,$2,$3,1) ON CONFLICT(video_id) DO NOTHING`, video, staff, tenant)

	config := db.Config()
	config.MaxConns = 2
	small, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer small.Close()
	principal := &mpd.Principal{UserID: user, OrganizationID: org, StaffID: staff, TenantID: tenant, SessionID: sid, Capabilities: mpd.Capabilities{Read: true}}
	base := "https://sendrec.example.test"
	h := NewHandler(small, newMemStorage(map[string]string{}), base, 1000000, 0, 0, 0, "synthetic-key", false)
	media := h.MPDMedia(slowIdentity{syntheticRecordingIdentity{small, principal}}, MPDServiceConfig{Enabled: true, WorkspaceID: org, TenantID: tenant}, nil)

	const redeems = 6
	handoffs := make([]string, redeems)
	for i := range handoffs {
		handoffs[i] = randomProof()
		mustExec(t, db, `INSERT INTO mpd_preview_handoffs(token_hash,video_id,media_version,session_id,origin,nonce,expires_at) VALUES($1,$2,1,$3,$4,$5,now()+interval '60 seconds')`, tokenDigest(handoffs[i]), video, sid, base, fmt.Sprintf("synthetic-nonce-%d", i))
	}
	codes := make([]int, redeems)
	var wg sync.WaitGroup
	for i := range handoffs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			request := httptest.NewRequest("POST", "/api/mpd/preview/redeem", strings.NewReader(fmt.Sprintf(`{"handoff":%q,"nonce":"synthetic-nonce-%d","parentOrigin":%q}`, handoffs[i], i, base))).WithContext(requestCtx)
			request.Header.Set("Origin", base)
			recorder := httptest.NewRecorder()
			media.RedeemPreview(recorder, request)
			codes[i] = recorder.Code
		}()
	}
	wg.Wait()
	for i, code := range codes {
		if code != 200 {
			t.Errorf("redeem %d: %d", i, code)
		}
	}
	var spent int
	if err := db.QueryRow(ctx, `SELECT count(*) FROM mpd_preview_handoffs WHERE video_id=$1 AND used_at IS NOT NULL`, video).Scan(&spent); err != nil || spent != redeems {
		t.Fatalf("%d handoffs spent, want %d: %v", spent, redeems, err)
	}
}
