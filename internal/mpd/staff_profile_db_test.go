package mpd

import (
	"bytes"
	"context"
	"encoding/json"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/sendrec/sendrec/internal/integration"
)

// profileFixture provisions one staff member who has no Cognito name, and
// serves their grant and photo from one mpd-api stand-in.
type profileFixture struct {
	h       *Handler
	db      *pgxpool.Pool
	sign    func(map[string]any) string
	p       *Principal
	id      *Identity
	g       *Grant
	profile atomic.Value // the grant's profile JSON
	version atomic.Value // the photo route's ETag version
	status  atomic.Int32 // the photo route's status
	hang    chan struct{}
	fetches atomic.Int32
	photo   []byte
}

func newProfileFixture(t *testing.T) *profileFixture {
	t.Helper()
	db := identityDB(t)
	ctx := context.Background()
	h, sign := signedFixture(t)
	h.db = db
	h.key = integration.DeriveKey(strings.Repeat("k", 32))
	f := &profileFixture{h: h, db: db, sign: sign, photo: jpegPhoto(t, 128, 128), hang: make(chan struct{})}
	close(f.hang)
	f.profile.Store(`null`)
	f.version.Store("v1")
	f.status.Store(http.StatusOK)
	for _, q := range []string{`INSERT INTO organizations(id,name,slug) VALUES('` + orgID + `','Synthetic','mpd-synthetic')`, `INSERT INTO mpd_managed_workspaces(organization_id,tenant_id,issuer,enabled,seat_limit) VALUES('` + orgID + `','` + tenantID + `','` + h.cfg.Issuer + `',true,2)`} {
		if _, err := db.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/photo") {
			f.fetches.Add(1)
			<-f.hang
			w.Header().Set("ETag", `"`+f.version.Load().(string)+`"`)
			w.WriteHeader(int(f.status.Load()))
			_, _ = w.Write(f.photo)
			return
		}
		grant, _ := json.Marshal(f.g)
		_, _ = w.Write(append(grant[:len(grant)-1], []byte(`,"profile":`+f.profile.Load().(string)+`}`)...))
	}))
	t.Cleanup(srv.Close)
	h.cfg.AccessURL = srv.URL + "/video-access"
	h.client = srv.Client()
	f.id = &Identity{Issuer: h.cfg.Issuer, Subject: "synthetic-staff", Email: "synthetic@example.test", ExpiresAt: time.Now().Add(time.Hour)}
	f.g = &Grant{SchemaVersion: 1, StaffID: staffID, TenantID: tenantID, Capabilities: Capabilities{Read: true, Record: true}, ValidUntil: time.Now().Add(time.Hour)}
	p, err := h.provision(ctx, f.id, f.g)
	if err != nil {
		t.Fatal(err)
	}
	f.p = p
	return f
}

func (f *profileFixture) read(t *testing.T, others ...*Handler) (string, *string, []byte) {
	t.Helper()
	f.h.photos.wg.Wait()
	for _, o := range others {
		o.photos.wg.Wait()
	}
	var name string
	var version *string
	var stored []byte
	if err := f.db.QueryRow(context.Background(), `SELECT u.name,s.version,s.jpeg FROM users u LEFT JOIN mpd_staff_photos s ON s.user_id=u.id WHERE u.id=$1`, f.p.UserID).Scan(&name, &version, &stored); err != nil {
		t.Fatal(err)
	}
	return name, version, stored
}

func TestApplyProfileKeepsNameAndPhotoCurrent(t *testing.T) {
	f := newProfileFixture(t)
	ctx := context.Background()
	if name, _, _ := f.read(t); name != "MPD staff" {
		t.Fatalf("a staff member without a Cognito name starts as %q", name)
	}

	name, first, second := "Dr Synthetic Staff", "v1", "v2"
	f.h.applyProfile(ctx, f.p.UserID, "synthetic-token", &Profile{DisplayName: &name, PhotoVersion: &first})
	got, version, stored := f.read(t)
	if got != name || version == nil || *version != first || !isJPEG(stored) {
		t.Fatalf("profile not applied: %q %v", got, version)
	}
	f.h.applyProfile(ctx, f.p.UserID, "synthetic-token", &Profile{DisplayName: &name, PhotoVersion: &first})
	if f.read(t); f.fetches.Load() != 1 {
		t.Fatalf("an unchanged photo was fetched again: %d fetches", f.fetches.Load())
	}

	// A failed fetch keeps the old photo and waits before trying that version again.
	f.version.Store(second)
	f.status.Store(http.StatusServiceUnavailable)
	f.h.applyProfile(ctx, f.p.UserID, "synthetic-token", &Profile{DisplayName: &name, PhotoVersion: &second})
	f.read(t)
	f.h.applyProfile(ctx, f.p.UserID, "synthetic-token", &Profile{DisplayName: &name, PhotoVersion: &second})
	if _, version, _ = f.read(t); *version != first || f.fetches.Load() != 2 {
		t.Fatalf("after a failure: version %s, %d fetches", *version, f.fetches.Load())
	}
	f.status.Store(http.StatusOK)
	f.h.photos.mu.Lock()
	f.h.photos.failed[f.p.UserID] = photoFailure{version: second, at: time.Now().Add(-photoRetryAfter)}
	f.h.photos.mu.Unlock()
	f.h.applyProfile(ctx, f.p.UserID, "synthetic-token", &Profile{DisplayName: &name, PhotoVersion: &second})
	if _, version, _ = f.read(t); *version != second {
		t.Fatal("the photo was not retried after the wait")
	}

	f.h.applyProfile(ctx, f.p.UserID, "synthetic-token", nil)
	if got, version, _ = f.read(t); got != name || version == nil {
		t.Fatal("a grant without a profile changed the profile")
	}
	f.h.applyProfile(ctx, f.p.UserID, "synthetic-token", &Profile{})
	if got, version, _ = f.read(t); got != name || version != nil {
		t.Fatalf("a profile without a name or photo should keep %q and remove the photo, got %q %v", name, got, version)
	}
}

func TestApplyProfileDoesNotWaitForThePhoto(t *testing.T) {
	f := newProfileFixture(t)
	f.hang = make(chan struct{})
	name, version := "Dr Synthetic Staff", "v1"
	start := time.Now()
	f.h.applyProfile(context.Background(), f.p.UserID, "synthetic-token", &Profile{DisplayName: &name, PhotoVersion: &version})
	f.h.applyProfile(context.Background(), f.p.UserID, "synthetic-token", &Profile{DisplayName: &name, PhotoVersion: &version})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("a hanging photo read held the request for %s", elapsed)
	}
	close(f.hang)
	if _, stored, _ := f.read(t); stored == nil || f.fetches.Load() != 1 {
		t.Fatalf("expected one fetch to store the photo: %v, %d fetches", stored, f.fetches.Load())
	}
}

func TestGrantRefreshAppliesTheProfile(t *testing.T) {
	f := newProfileFixture(t)
	ctx := context.Background()
	f.h.cfg.JWTSecret = strings.Repeat("j", 32)
	sid, err := f.h.createSession(ctx, f.p, f.id, f.g, f.sign(nil), "")
	if err != nil {
		t.Fatal(err)
	}
	f.profile.Store(`{"displayName":"Dr Synthetic Staff","photoVersion":"v1"}`)
	if _, err = f.db.Exec(ctx, `UPDATE mpd_sessions SET grant_expires_at=now()-interval '1 second' WHERE id=$1`, sid); err != nil {
		t.Fatal(err)
	}
	if _, err = f.h.ValidateSession(ctx, sid); err != nil {
		t.Fatal(err)
	}
	if name, version, _ := f.read(t); name != "Dr Synthetic Staff" || version == nil || *version != "v1" {
		t.Fatalf("grant refresh did not apply the profile: %q %v", name, version)
	}
}

func TestApplyProfileRemovalWinsOverAFetchInFlight(t *testing.T) {
	f := newProfileFixture(t)
	f.hang = make(chan struct{})
	version := "v1"
	f.h.applyProfile(context.Background(), f.p.UserID, "synthetic-token", &Profile{PhotoVersion: &version})
	f.inFlight(t, 1)
	f.h.applyProfile(context.Background(), f.p.UserID, "synthetic-token", &Profile{})
	close(f.hang)
	if _, stored, _ := f.read(t); stored != nil || f.fetches.Load() != 1 {
		t.Fatalf("a photo removed during its read came back: %v, %d fetches", stored, f.fetches.Load())
	}
}

func TestApplyProfileSkipsTheRefreshTransaction(t *testing.T) {
	f := newProfileFixture(t)
	ctx := context.Background()
	tx, err := f.db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	scoped := *f.h
	scoped.db = tx
	name, version := "Dr Synthetic Staff", "v1"
	scoped.applyProfile(ctx, f.p.UserID, "synthetic-token", &Profile{DisplayName: &name, PhotoVersion: &version})
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if got, stored, _ := f.read(t); got != "MPD staff" || stored != nil || f.fetches.Load() != 0 {
		t.Fatalf("the profile was applied inside the refresh transaction: %q %v %d", got, stored, f.fetches.Load())
	}
}

// inFlight waits until n photo reads have reached the mpd-api stand-in, so a
// test changes the wanted version while a read is really running.
func (f *profileFixture) inFlight(t *testing.T, n int32) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); f.fetches.Load() < n; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("photo read %d never started", n)
		}
	}
}

func isJPEG(b []byte) bool {
	_, err := jpeg.Decode(bytes.NewReader(b))
	return len(b) > 0 && err == nil
}

// otherTask is a second task's handler: the same database and mpd-api with
// its own fetcher, as two ECS tasks have.
func (f *profileFixture) otherTask() *Handler {
	o := *f.h
	o.photos = newPhotoFetcher()
	return &o
}

func TestApplyProfileRemovalOnAnotherTaskWinsOverAFetchInFlight(t *testing.T) {
	f := newProfileFixture(t)
	f.hang = make(chan struct{})
	other := f.otherTask()
	version := "v1"
	f.h.applyProfile(context.Background(), f.p.UserID, "synthetic-token", &Profile{PhotoVersion: &version})
	f.inFlight(t, 1)
	other.applyProfile(context.Background(), f.p.UserID, "synthetic-token", &Profile{})
	close(f.hang)
	if _, stored, _ := f.read(t, other); stored != nil || f.fetches.Load() != 1 {
		t.Fatalf("a photo removed on another task during its read came back: %v, %d fetches", stored, f.fetches.Load())
	}
}

func TestApplyProfileNewerVersionOnAnotherTaskWins(t *testing.T) {
	f := newProfileFixture(t)
	f.hang = make(chan struct{})
	other := f.otherTask()
	first, second := "v1", "v2"
	f.h.applyProfile(context.Background(), f.p.UserID, "synthetic-token", &Profile{PhotoVersion: &first})
	f.inFlight(t, 1)
	other.applyProfile(context.Background(), f.p.UserID, "synthetic-token", &Profile{PhotoVersion: &second})
	f.version.Store(second)
	close(f.hang)
	if _, version, stored := f.read(t, other); version == nil || *version != second || !isJPEG(stored) {
		t.Fatalf("the newer version did not end up stored: %v", version)
	}
}

func TestApplyProfileFetchesANewerVersionAfterTheBusyFetch(t *testing.T) {
	f := newProfileFixture(t)
	f.hang = make(chan struct{})
	first, second := "v1", "v2"
	f.h.applyProfile(context.Background(), f.p.UserID, "synthetic-token", &Profile{PhotoVersion: &first})
	f.inFlight(t, 1)
	f.h.applyProfile(context.Background(), f.p.UserID, "synthetic-token", &Profile{PhotoVersion: &second})
	f.version.Store(second)
	close(f.hang)
	if _, version, stored := f.read(t); version == nil || *version != second || !isJPEG(stored) || f.fetches.Load() != 2 {
		t.Fatalf("a version named during a fetch was dropped: %v, %d fetches", version, f.fetches.Load())
	}
}

// ageFailure moves the user's recorded photo failure past the retry wait.
func (f *profileFixture) ageFailure(t *testing.T) photoFailure {
	t.Helper()
	f.h.photos.mu.Lock()
	defer f.h.photos.mu.Unlock()
	fail := f.h.photos.failed[f.p.UserID]
	fail.at = time.Now().Add(-2 * photoRetryAfter)
	f.h.photos.failed[f.p.UserID] = fail
	return fail
}

// A 404 says mpd-api has no such photo, so that version is not read again. A
// 403 can belong to one token, so it is read again after the wait.
func TestApplyProfileRetriesOnlyPassingRefusals(t *testing.T) {
	f := newProfileFixture(t)
	version := "v1"
	f.status.Store(http.StatusForbidden)
	f.h.applyProfile(context.Background(), f.p.UserID, "synthetic-token", &Profile{PhotoVersion: &version})
	f.read(t)
	if fail := f.ageFailure(t); fail.final {
		t.Fatal("a 403 was treated as final")
	}
	f.status.Store(http.StatusNotFound)
	f.h.applyProfile(context.Background(), f.p.UserID, "synthetic-token", &Profile{PhotoVersion: &version})
	f.read(t)
	if f.fetches.Load() != 2 {
		t.Fatalf("a 403 was not read again after the wait: %d fetches", f.fetches.Load())
	}
	refused := f.ageFailure(t)
	f.status.Store(http.StatusOK)
	f.h.applyProfile(context.Background(), f.p.UserID, "synthetic-token", &Profile{PhotoVersion: &version})
	if _, stored, _ := f.read(t); stored != nil || f.fetches.Load() != 2 || !refused.final {
		t.Fatalf("a 404 for a version was read again: %v, %d fetches, final %v", stored, f.fetches.Load(), refused.final)
	}
	f.h.photos.mu.Lock()
	tokens, busy := len(f.h.photos.tokens), len(f.h.photos.busy)
	f.h.photos.mu.Unlock()
	if tokens != 0 || busy != 0 {
		t.Fatalf("a finished read left %d tokens and %d busy users", tokens, busy)
	}
}

// mpd-api sends no departure signal, and a staff member may have neither a
// name nor a photo, so a profile with both null keeps the name.
func TestApplyProfileKeepsTheNameWhenTheProfileHasNone(t *testing.T) {
	f := newProfileFixture(t)
	name, version := "Dr Synthetic Staff", "v1"
	f.h.applyProfile(context.Background(), f.p.UserID, "synthetic-token", &Profile{DisplayName: &name, PhotoVersion: &version})
	f.h.applyProfile(context.Background(), f.p.UserID, "synthetic-token", &Profile{})
	if got, stored, photo := f.read(t); got != name || stored != nil || photo != nil {
		t.Fatalf("a profile with no name and no photo gave %q and photo %v", got, stored)
	}
}
