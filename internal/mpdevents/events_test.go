package mpdevents

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sendrec/sendrec/internal/database"
)

func testDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	base := os.Getenv("TEST_DATABASE_URL")
	if base == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	name := "mpdevents_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	db, err := database.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		_, err := admin.Exec(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)")
		if err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	if err = db.Migrate(u.String()); err != nil {
		t.Fatal(err)
	}
	return db.Pool
}
func exec(t *testing.T, db *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := db.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}
func queryString(t *testing.T, db *pgxpool.Pool, sql string, args ...any) string {
	t.Helper()
	var value string
	if err := db.QueryRow(context.Background(), sql, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}
func count(t *testing.T, db *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

const tenant = "33333333-3333-4333-8333-333333333333"
const staff = "22222222-2222-4222-8222-222222222222"

func owner(t *testing.T, db *pgxpool.Pool) (string, string) {
	t.Helper()
	user := queryString(t, db, `INSERT INTO users(email,password,name) VALUES('synthetic@example.test','','Synthetic') RETURNING id`)
	org := queryString(t, db, `INSERT INTO organizations(name,slug) VALUES('Synthetic','synthetic') RETURNING id`)
	exec(t, db, `INSERT INTO mpd_managed_workspaces(organization_id,tenant_id,issuer,enabled,seat_limit) VALUES($1,$2,'https://issuer.example.test',true,10)`, org, tenant)
	exec(t, db, `INSERT INTO mpd_external_identities(issuer,subject,user_id,organization_id,staff_id,tenant_id) VALUES('https://issuer.example.test','synthetic',$1,$2,$3,$4)`, user, org, staff, tenant)
	return user, org
}
func video(t *testing.T, db *pgxpool.Pool, user, org, status string) string {
	t.Helper()
	return queryString(t, db, `INSERT INTO videos(user_id,organization_id,title,file_key,share_token,status) VALUES($1,$2,'Synthetic','synthetic.mp4',gen_random_uuid()::text,$3) RETURNING id`, user, org, status)
}
func TestLifecycleAtomicityAdmissionAndRetainedDeletion(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	user, org := owner(t, db)
	other := queryString(t, db, `INSERT INTO users(email,password,name) VALUES('other@example.test','','Synthetic') RETURNING id`)
	if _, err := db.Exec(ctx, `INSERT INTO videos(user_id,organization_id,title,file_key,share_token) VALUES($1,$2,'Synthetic','x','unbound')`, other, org); err == nil {
		t.Fatal("unbound managed admission succeeded")
	}
	exec(t, db, `INSERT INTO videos(user_id,title,file_key,share_token,status) VALUES($1,'Evaluation','x','evaluation','ready')`, other)
	if n := count(t, db, `SELECT count(*) FROM mpd_event_outbox`); n != 0 {
		t.Fatalf("evaluation emitted %d", n)
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO videos(user_id,organization_id,title,file_key,share_token,status) VALUES($1,$2,'Synthetic','x','rollback','ready')`, user, org); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT count(*) FROM mpd_event_outbox`); n != 0 {
		t.Fatalf("rollback emitted %d", n)
	}
	id := video(t, db, user, org, "uploading")
	exec(t, db, `UPDATE videos SET status='deleted' WHERE id=$1`, id)
	exec(t, db, `DELETE FROM videos WHERE id=$1`, id)
	if n := count(t, db, `SELECT count(*) FROM mpd_event_outbox WHERE video_id=$1 AND event_type='video.deleted' AND media_version=1`, id); n != 1 {
		t.Fatalf("delete-before-ready events=%d", n)
	}
	exec(t, db, `DELETE FROM mpd_external_identities WHERE user_id=$1`, user)
	// Removing identity cannot erase the immutable owner snapshot or evidence.
	var event map[string]any
	body := queryString(t, db, `SELECT convert_from(body,'UTF8') FROM mpd_event_outbox WHERE video_id=$1`, id)
	if err = json.Unmarshal([]byte(body), &event); err != nil {
		t.Fatal(err)
	}
	if event["ownerStaffId"] != staff || event["tenantId"] != tenant {
		t.Fatal("lost verified binding")
	}
	if _, err = db.Exec(ctx, `DELETE FROM mpd_event_outbox WHERE video_id=$1`, id); err == nil {
		t.Fatal("pending event purged")
	}
	if _, err = db.Exec(ctx, `UPDATE mpd_event_outbox SET body='changed' WHERE video_id=$1`, id); err == nil {
		t.Fatal("immutable body changed")
	}
	exec(t, db, `UPDATE mpd_event_outbox SET state='acknowledged',acknowledged_at=now() WHERE video_id=$1`, id)
	if _, err = db.Exec(ctx, `DELETE FROM mpd_event_outbox WHERE video_id=$1`, id); err == nil {
		t.Fatal("fresh acknowledgment purged")
	}
	exec(t, db, `UPDATE mpd_event_outbox SET acknowledged_at=now()-interval '31 days' WHERE video_id=$1`, id)
	exec(t, db, `DELETE FROM mpd_event_outbox WHERE video_id=$1`, id)
}
func TestPlaybackProofAndMediaFences(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	user, org := owner(t, db)
	id := video(t, db, user, org, "ready")
	if _, err := NewSession(ctx, db, id, false); err == nil {
		t.Fatal("unpublished anonymous session")
	}
	preview, err := NewSession(ctx, db, id, true)
	if err != nil {
		t.Fatal(err)
	}
	exec(t, db, `UPDATE mpd_video_state SET published=true,published_at=now() WHERE video_id=$1`, id)
	anon, err := NewSession(ctx, db, id, false)
	if err != nil {
		t.Fatal(err)
	}
	observation := Progress{SessionID: anon.ID, Playing: true, PreviousTime: 0, CurrentTime: 0.5, ElapsedMilliseconds: 500}
	for _, bad := range []Progress{{SessionID: anon.ID}, {SessionID: anon.ID, Playing: true, CurrentTime: 20, ElapsedMilliseconds: 500}, {SessionID: anon.ID, Playing: true, CurrentTime: 0.5, ElapsedMilliseconds: 500, Seeking: true}} {
		if err = Accept(ctx, db, id, bad); err == nil {
			t.Fatal("invalid playback accepted")
		}
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- Accept(ctx, db, id, observation) }()
	}
	wg.Wait()
	close(errs)
	for err = range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := count(t, db, `SELECT count(*) FROM mpd_playback_facts`); n != 1 {
		t.Fatalf("duplicate facts %d", n)
	}
	observation.SessionID = preview.ID
	if err = Accept(ctx, db, id, observation); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT count(*) FROM mpd_event_outbox WHERE event_type='video.playback_started' AND convert_from(body,'UTF8')::jsonb->'data'->>'viewerClass'='staff_preview'`); n != 1 {
		t.Fatal("preview class not preserved")
	}
	exec(t, db, `UPDATE videos SET status='processing' WHERE id=$1`, id)
	if n := count(t, db, `SELECT media_version FROM videos WHERE id=$1`, id); n != 2 {
		t.Fatal("version not reserved at acceptance")
	}
	exec(t, db, `UPDATE videos SET status='ready',media_version=media_version+1 WHERE id=$1`, id)
	if n := count(t, db, `SELECT media_version FROM videos WHERE id=$1`, id); n != 2 {
		t.Fatal("completion allocated another version")
	}
	if err = Accept(ctx, db, id, observation); err == nil {
		t.Fatal("stale media proof accepted")
	}
	if _, err = db.Exec(ctx, `UPDATE videos SET media_version=1 WHERE id=$1`, id); err == nil {
		t.Fatal("media version regressed")
	}
	exec(t, db, `UPDATE videos SET status='deleted' WHERE id=$1`, id)
	if _, err = db.Exec(ctx, `UPDATE videos SET status='ready' WHERE id=$1`, id); err == nil {
		t.Fatal("tombstone revived")
	}
}
func TestPasswordRemovalAtomicallyUnpublishes(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	user, org := owner(t, db)
	id := video(t, db, user, org, "ready")
	exec(t, db, `UPDATE videos SET share_password='synthetic-hash' WHERE id=$1`, id)
	exec(t, db, `UPDATE mpd_video_state SET published=true,published_at=now() WHERE video_id=$1`, id)
	session, err := NewSession(ctx, db, id, false)
	if err != nil {
		t.Fatal(err)
	}
	exec(t, db, `UPDATE videos SET share_password=NULL WHERE id=$1`, id)
	if n := count(t, db, `SELECT count(*) FROM mpd_video_state WHERE video_id=$1 AND NOT published AND published_at IS NULL`, id); n != 1 {
		t.Fatal("passwordless managed video remained published")
	}
	if _, err = NewSession(ctx, db, id, false); err == nil {
		t.Fatal("anonymous session allowed after password removal")
	}
	if err = Accept(ctx, db, id, Progress{SessionID: session.ID, Playing: true, CurrentTime: 0.5, ElapsedMilliseconds: 500}); err == nil {
		t.Fatal("old session played after password removal")
	}
}

func TestTranscriptAcceptanceWinsReverseCompletion(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	user, org := owner(t, db)
	id := video(t, db, user, org, "ready")
	exec(t, db, `UPDATE videos SET transcript_status='pending',transcript_generation=transcript_generation+1 WHERE id=$1`, id)
	exec(t, db, `UPDATE videos SET transcript_status='processing' WHERE id=$1`, id)
	exec(t, db, `UPDATE videos SET transcript_status='pending',transcript_generation=transcript_generation+1 WHERE id=$1`, id)
	publish := `UPDATE videos SET transcript_status='ready',transcript_key=$2,transcript_published_generation=$3 WHERE id=$1 AND transcript_generation=$3 AND media_version=1`
	exec(t, db, publish, id, "synthetic-new.vtt", 2)
	tag, err := db.Exec(ctx, publish, id, "synthetic-old.vtt", 1)
	if err != nil {
		t.Fatal(err)
	}
	if tag.RowsAffected() != 0 {
		t.Fatal("stale completion won")
	}
	if n := count(t, db, `SELECT transcript_version FROM mpd_video_state WHERE video_id=$1`, id); n != 2 {
		t.Fatalf("transcript version %d", n)
	}
	if n := count(t, db, `SELECT count(*) FROM mpd_event_outbox WHERE event_type='video.transcript_ready'`); n != 1 {
		t.Fatalf("stale event emitted %d", n)
	}
	if _, err = db.Exec(ctx, `UPDATE videos SET transcript_key='unreserved.vtt',transcript_published_generation=1 WHERE id=$1`, id); err == nil {
		t.Fatal("unguarded publication succeeded")
	}
}
func TestOutboxRestartRetryRotationAndReplay(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	user, org := owner(t, db)
	id := video(t, db, user, org, "ready")
	var calls atomic.Int32
	var status atomic.Int32
	status.Store(503)
	var mu sync.Mutex
	var bodies [][]byte
	var keyIDs []string
	keys := map[string][]byte{"old": []byte("01234567890123456789012345678901"), "new": []byte("abcdefghijklmnopqrstuvwxyz012345")}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		stamp := r.Header.Get("X-SendRec-Delivery-Timestamp")
		kid := r.Header.Get("X-SendRec-Key-Id")
		if r.Header.Get("X-SendRec-Signature") != Signature(keys[kid], stamp, body) {
			t.Error("invalid signature")
		}
		secs, err := strconv.ParseInt(stamp, 10, 64)
		if err != nil || time.Since(time.Unix(secs, 0)) > 5*time.Minute {
			t.Error("stale timestamp")
		}
		mu.Lock()
		bodies = append(bodies, body)
		keyIDs = append(keyIDs, kid)
		mu.Unlock()
		calls.Add(1)
		w.WriteHeader(int(status.Load()))
	}))
	defer server.Close()
	worker := func(kid string) *Worker {
		w, err := NewWorker(db, Config{Enabled: true, Destination: server.URL, KeyID: kid, SigningKey: keys[kid], AllowHTTPForTests: true})
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	old := worker("old")
	if worked, err := old.DeliverOne(ctx); !worked || err != nil {
		t.Fatalf("initial %t %v", worked, err)
	}
	if s := queryString(t, db, `SELECT state FROM mpd_event_outbox WHERE video_id=$1`, id); s != "pending" {
		t.Fatal(s)
	}
	// A task dies after its committed claim. Its successor recovers the lease.
	exec(t, db, `UPDATE mpd_event_outbox SET state='leased',lease_token=gen_random_uuid(),lease_until=now()-interval '1 second' WHERE video_id=$1`, id)
	status.Store(401)
	if worked, err := worker("new").DeliverOne(ctx); !worked || err != nil {
		t.Fatal(err)
	}
	eid := queryString(t, db, `SELECT event_id FROM mpd_event_outbox WHERE video_id=$1`, id)
	if s := queryString(t, db, `SELECT state FROM mpd_event_outbox WHERE event_id=$1`, eid); s != "blocked" {
		t.Fatal(s)
	}
	if err := Replay(ctx, db, eid); err != nil {
		t.Fatal(err)
	}
	status.Store(204)
	if worked, err := worker("new").DeliverOne(ctx); !worked || err != nil {
		t.Fatal(err)
	}
	if err := Replay(ctx, db, eid); err == nil {
		t.Fatal("acknowledged replay allowed")
	}
	if calls.Load() != 3 {
		t.Fatalf("calls=%d", calls.Load())
	}
	for _, body := range bodies {
		if string(body) != string(bodies[0]) {
			t.Fatal("replay changed immutable evidence")
		}
	}
	if fmt.Sprint(keyIDs) != "[old new new]" {
		t.Fatal(keyIDs)
	}
	// Retry deadline is based on the explicit replay window, not process uptime.
	second := video(t, db, user, org, "ready")
	exec(t, db, `UPDATE mpd_event_outbox SET replayed_at=now()-interval '25 hours' WHERE video_id=$1`, second)
	status.Store(503)
	if _, err := old.DeliverOne(ctx); err != nil {
		t.Fatal(err)
	}
	if s := queryString(t, db, `SELECT state FROM mpd_event_outbox WHERE video_id=$1`, second); s != "dead" {
		t.Fatal(s)
	}
	if calls.Load() != 3 {
		t.Fatal("expired work attempted delivery after 24 hours")
	}
	b, err := Inspect(ctx, db)
	if err != nil || b.Dead != 1 {
		t.Fatalf("backlog %+v %v", b, err)
	}
}
func TestConcurrentWorkersDeliverOncePerLease(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	user, org := owner(t, db)
	video(t, db, user, org, "ready")
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		close(entered)
		<-release
		w.WriteHeader(204)
	}))
	defer server.Close()
	c := Config{Enabled: true, Destination: server.URL, KeyID: "test", SigningKey: []byte("01234567890123456789012345678901"), AllowHTTPForTests: true}
	a, _ := NewWorker(db, c)
	b, _ := NewWorker(db, c)
	done := make(chan error, 1)
	go func() { _, err := a.DeliverOne(ctx); done <- err }()
	<-entered
	worked, err := b.DeliverOne(ctx)
	close(release)
	if worked || err != nil {
		t.Fatalf("second worker got lease: %t %v", worked, err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("concurrent delivery")
	}
}
func TestSignatureAndInvalidProgress(t *testing.T) {
	body := []byte(`{"eventId":"synthetic"}`)
	key := []byte("01234567890123456789012345678901")
	if Signature(key, "1", body) == Signature(key, "2", body) || Signature(key, "1", body) == Signature(key, "1", append(body, ' ')) {
		t.Fatal("timestamp/body not authenticated")
	}
	for _, p := range []Progress{{Playing: true, CurrentTime: 1, ElapsedMilliseconds: 0}, {Playing: true, PreviousTime: 1, CurrentTime: 1, ElapsedMilliseconds: 500}} {
		if p.Valid() {
			t.Fatal("non advancing playback accepted")
		}
	}
	if _, err := NewWorker(nil, Config{Enabled: true, Destination: "http://unsafe.test", KeyID: "x", SigningKey: key}); err == nil {
		t.Fatal("insecure receiver accepted")
	}
}

// The synthetic receiver commits its receipt before deliberately losing the
// response. A replacement worker retries the same event, and the receipt's
// unique key makes the consumer effect occur only once.
func TestReceiverCommitLostResponseAndReorderedReplay(t *testing.T) {
	db := testDB(t)
	ctx := context.Background()
	user, org := owner(t, db)
	id := video(t, db, user, org, "ready")
	exec(t, db, `CREATE TABLE synthetic_receipts(event_id UUID PRIMARY KEY,event_type TEXT NOT NULL)`)
	exec(t, db, `UPDATE videos SET transcript_generation=1,transcript_published_generation=1,transcript_status='ready',transcript_key='synthetic.vtt' WHERE id=$1`, id)
	exec(t, db, `UPDATE videos SET status='deleted' WHERE id=$1`, id)
	// Deliver deletion ahead of readiness and captions. Ordering is not promised.
	exec(t, db, `UPDATE mpd_event_outbox SET available_at=now()-CASE WHEN event_type='video.deleted' THEN interval '3 minutes' ELSE interval '1 minute' END`)
	var lose atomic.Bool
	lose.Store(true)
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var event struct {
			ID   string `json:"eventId"`
			Type string `json:"eventType"`
		}
		if err := json.NewDecoder(r.Body).Decode(&event); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if _, err := db.Exec(r.Context(), `INSERT INTO synthetic_receipts(event_id,event_type) VALUES($1,$2) ON CONFLICT DO NOTHING`, event.ID, event.Type); err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		attempts.Add(1)
		if lose.Swap(false) {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	config := Config{Enabled: true, Destination: server.URL, KeyID: "synthetic", SigningKey: []byte("01234567890123456789012345678901"), AllowHTTPForTests: true}
	first, err := NewWorker(db, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = first.DeliverOne(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT count(*) FROM synthetic_receipts WHERE event_type='video.deleted'`); n != 1 {
		t.Fatal("deletion did not arrive first")
	}
	exec(t, db, `UPDATE mpd_event_outbox SET available_at=now() WHERE state='pending'`)
	restarted, err := NewWorker(db, config)
	if err != nil {
		t.Fatal(err)
	}
	for range 4 {
		worked, err := restarted.DeliverOne(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !worked {
			break
		}
	}
	if attempts.Load() != 4 {
		t.Fatalf("expected one duplicate attempt: %d", attempts.Load())
	}
	if n := count(t, db, `SELECT count(*) FROM synthetic_receipts`); n != 3 {
		t.Fatalf("consumer effects %d", n)
	}
	if n := count(t, db, `SELECT count(*) FROM mpd_event_outbox WHERE state='acknowledged'`); n != 3 {
		t.Fatalf("acknowledged %d", n)
	}
}

func TestContractFixturesMatchProducer(t *testing.T) {
	raw, err := os.ReadFile("testdata/events.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []map[string]any
	if err = json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	db := testDB(t)
	ctx := context.Background()
	user, org := owner(t, db)
	id := video(t, db, user, org, "ready")
	exec(t, db, `UPDATE videos SET transcript_generation=2,transcript_published_generation=2,transcript_status='ready',transcript_key='synthetic.vtt' WHERE id=$1`, id)
	exec(t, db, `UPDATE mpd_video_state SET published=true,published_at=now() WHERE video_id=$1`, id)
	for _, preview := range []bool{false, true} {
		s, err := NewSession(ctx, db, id, preview)
		if err != nil {
			t.Fatal(err)
		}
		if err = Accept(ctx, db, id, Progress{SessionID: s.ID, Playing: true, CurrentTime: 0.5, ElapsedMilliseconds: 500}); err != nil {
			t.Fatal(err)
		}
	}
	exec(t, db, `UPDATE videos SET status='deleted' WHERE id=$1`, id)
	rows, err := db.Query(ctx, `SELECT body FROM mpd_event_outbox ORDER BY created_at,event_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	emitted := make([]json.RawMessage, 0, 5)
	for rows.Next() {
		var body []byte
		if err = rows.Scan(&body); err != nil {
			t.Fatal(err)
		}
		emitted = append(emitted, json.RawMessage(body))
		var event map[string]any
		if err = json.Unmarshal(body, &event); err != nil {
			t.Fatal(err)
		}
		var match map[string]any
		for _, fixture := range fixtures {
			if fixture["eventType"] != event["eventType"] {
				continue
			}
			if fixture["eventType"] == "video.playback_started" && fixture["data"].(map[string]any)["viewerClass"] != event["data"].(map[string]any)["viewerClass"] {
				continue
			}
			match = fixture
			break
		}
		if match == nil {
			t.Fatalf("unexpected event type %v", event["eventType"])
		}
		event["eventId"] = match["eventId"]
		event["videoId"] = match["videoId"]
		event["occurredAt"] = match["occurredAt"]
		if event["eventType"] == "video.playback_started" {
			event["data"].(map[string]any)["playbackSessionId"] = match["data"].(map[string]any)["playbackSessionId"]
		}
		want, _ := json.Marshal(match)
		got, _ := json.Marshal(event)
		if string(got) != string(want) {
			t.Fatalf("fixture mismatch\ngot %s\nwant %s", got, want)
		}
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(emitted) != len(fixtures) {
		t.Fatalf("emitted=%d fixtures=%d", len(emitted), len(fixtures))
	}
	// Optional local handoff validates actual emitted bytes in the consumer's
	// runtime without making the Go suite depend on another repository.
	if output := os.Getenv("MPD_EVENT_CONFORMANCE_OUTPUT"); output != "" {
		body, _ := json.MarshalIndent(emitted, "", "  ")
		if err = os.WriteFile(output, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
