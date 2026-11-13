package mpd

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestApplyProfileKeepsNameAndPhotoCurrent(t *testing.T) {
	db := identityDB(t)
	ctx := context.Background()
	h, _ := signedFixture(t)
	h.db = db
	if _, err := db.Exec(ctx, `INSERT INTO organizations(id,name,slug) VALUES($1,'Synthetic','mpd-synthetic')`, orgID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `INSERT INTO mpd_managed_workspaces(organization_id,tenant_id,issuer,enabled,seat_limit) VALUES($1,$2,$3,true,2)`, orgID, tenantID, h.cfg.Issuer); err != nil {
		t.Fatal(err)
	}
	id := &Identity{Issuer: h.cfg.Issuer, Subject: "synthetic-staff", Email: "synthetic@example.test", ExpiresAt: time.Now().Add(time.Hour)}
	p, err := h.provision(ctx, id, &Grant{SchemaVersion: 1, StaffID: staffID, TenantID: tenantID, Capabilities: Capabilities{Read: true, Record: true}, ValidUntil: time.Now().Add(30 * time.Second)})
	if err != nil {
		t.Fatal(err)
	}

	photo := jpegPhoto(t, 128, 128)
	var fetches atomic.Int32
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		w.WriteHeader(status)
		_, _ = w.Write(photo)
	}))
	defer srv.Close()
	h.cfg.AccessURL = srv.URL + "/video-access"
	h.client = srv.Client()

	read := func() (string, *string, []byte) {
		t.Helper()
		var name string
		var version *string
		var stored []byte
		if err := db.QueryRow(ctx, `SELECT u.name,s.version,s.jpeg FROM users u LEFT JOIN mpd_staff_photos s ON s.user_id=u.id WHERE u.id=$1`, p.UserID).Scan(&name, &version, &stored); err != nil {
			t.Fatal(err)
		}
		return name, version, stored
	}
	if name, _, _ := read(); name != "MPD staff" {
		t.Fatalf("a staff member without a Cognito name starts as %q", name)
	}

	name, first, second := "Dr Synthetic Staff", "v1", "v2"
	h.applyProfile(ctx, p.UserID, "synthetic-token", &Profile{DisplayName: &name, PhotoVersion: &first})
	got, version, stored := read()
	if got != name || version == nil || *version != first || !bytes.Equal(stored, photo) {
		t.Fatalf("profile not applied: %q %v", got, version)
	}
	h.applyProfile(ctx, p.UserID, "synthetic-token", &Profile{DisplayName: &name, PhotoVersion: &first})
	if fetches.Load() != 1 {
		t.Fatalf("an unchanged photo was fetched again: %d fetches", fetches.Load())
	}

	// A failed fetch keeps the old photo and retries on the next grant.
	status = http.StatusServiceUnavailable
	h.applyProfile(ctx, p.UserID, "synthetic-token", &Profile{DisplayName: &name, PhotoVersion: &second})
	if _, version, _ = read(); *version != first {
		t.Fatalf("a failed fetch replaced the photo with %v", *version)
	}
	status = http.StatusOK
	h.applyProfile(ctx, p.UserID, "synthetic-token", &Profile{DisplayName: &name, PhotoVersion: &second})
	if _, version, _ = read(); *version != second {
		t.Fatal("the next grant did not retry the photo")
	}

	h.applyProfile(ctx, p.UserID, "synthetic-token", nil)
	if got, version, _ = read(); got != name || version == nil {
		t.Fatal("a grant without a profile changed the profile")
	}
	h.applyProfile(ctx, p.UserID, "synthetic-token", &Profile{})
	if got, version, _ = read(); got != name || version != nil {
		t.Fatalf("a profile without a name or photo should keep %q and remove the photo, got %q %v", name, got, version)
	}
}
