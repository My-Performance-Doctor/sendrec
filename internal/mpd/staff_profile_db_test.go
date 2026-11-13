package mpd

import (
	"bytes"
	"context"
	"encoding/json"
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

func (f *profileFixture) read(t *testing.T) (string, *string, []byte) {
	t.Helper()
	photoFetches.wg.Wait()
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
	if got != name || version == nil || *version != first || !bytes.Equal(stored, f.photo) {
		t.Fatalf("profile not applied: %q %v", got, version)
	}
	f.h.applyProfile(ctx, f.p.UserID, "synthetic-token", &Profile{DisplayName: &name, PhotoVersion: &first})
	if f.read(t); f.fetches.Load() != 1 {
		t.Fatalf("an unchanged photo was fetched again: %d fetches", f.fetches.Load())
	}

	// A failed fetch keeps the old photo and waits before trying that version again.
	f.status.Store(http.StatusServiceUnavailable)
	f.h.applyProfile(ctx, f.p.UserID, "synthetic-token", &Profile{DisplayName: &name, PhotoVersion: &second})
	f.read(t)
	f.h.applyProfile(ctx, f.p.UserID, "synthetic-token", &Profile{DisplayName: &name, PhotoVersion: &second})
	if _, version, _ = f.read(t); *version != first || f.fetches.Load() != 2 {
		t.Fatalf("after a failure: version %s, %d fetches", *version, f.fetches.Load())
	}
	f.status.Store(http.StatusOK)
	photoFetches.failed.Store(f.p.UserID, photoFailure{version: second, at: time.Now().Add(-photoRetryAfter)})
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
