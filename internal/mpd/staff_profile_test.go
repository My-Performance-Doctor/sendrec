package mpd

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func encodedImage(t *testing.T, width, height int, encode func(*bytes.Buffer, image.Image) error) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := encode(&b, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func jpegPhoto(t *testing.T, width, height int) []byte {
	return encodedImage(t, width, height, func(b *bytes.Buffer, m image.Image) error { return jpeg.Encode(b, m, nil) })
}

func TestGrantProfile(t *testing.T) {
	now := time.Now()
	decode := func(profile string) *Grant {
		t.Helper()
		raw := `{"schemaVersion":1,"staffId":"` + staffID + `","tenantId":"` + tenantID + `","capabilities":{"read":true},"validUntil":"` + now.Add(time.Minute).Format(time.RFC3339Nano) + `"` + profile + `}`
		g, err := decodeGrant(strings.NewReader(raw), now, now.Add(time.Minute))
		if err != nil {
			t.Fatalf("grant refused: %v", err)
		}
		return g
	}
	if decode("").Profile != nil {
		t.Error("a grant from an older mpd-api has no profile")
	}
	g := decode(`,"profile":{"displayName":" Dr Synthetic Staff ","photoVersion":"0f3a9c"}`)
	if g.Profile == nil || *g.Profile.DisplayName != "Dr Synthetic Staff" || *g.Profile.PhotoVersion != "0f3a9c" {
		t.Fatalf("profile not kept and trimmed: %+v", g.Profile)
	}
	g = decode(`,"profile":{"displayName":null,"photoVersion":null}`)
	if g.Profile == nil || g.Profile.DisplayName != nil || g.Profile.PhotoVersion != nil {
		t.Fatalf("an empty profile means no name and no photo: %+v", g.Profile)
	}
	for _, bad := range []string{`"   "`, `"Dr\nStaff"`, `"` + strings.Repeat("a", 101) + `"`} {
		g = decode(`,"profile":{"displayName":` + bad + `,"photoVersion":"0f3a9c"}`)
		if g.Profile == nil || g.Profile.DisplayName != nil || *g.Profile.PhotoVersion != "0f3a9c" {
			t.Errorf("name %s should be dropped and the photo kept: %+v", bad, g.Profile)
		}
	}
	for _, bad := range []string{`""`, `"../photo"`} {
		if decode(`,"profile":{"displayName":"Dr Synthetic Staff","photoVersion":`+bad+`}`).Profile != nil {
			t.Errorf("photo version %s should drop the profile without refusing the grant", bad)
		}
	}
}

func TestFetchPhotoAcceptsOnlyASmallJPEG(t *testing.T) {
	photo := jpegPhoto(t, 128, 128)
	status, body := http.StatusOK, photo
	var auth, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	h := &Handler{cfg: Config{AccessURL: srv.URL + "/api/v1/staff/video-access"}, client: srv.Client()}

	got, err := h.fetchPhoto(context.Background(), "synthetic-token")
	if err != nil || !bytes.Equal(got, photo) {
		t.Fatalf("photo not read: %v", err)
	}
	if auth != "Bearer synthetic-token" || path != "/api/v1/staff/video-access/photo" {
		t.Fatalf("photo read from %s with %q", path, auth)
	}
	for name, tc := range map[string]struct {
		status int
		body   []byte
	}{
		"no photo":  {http.StatusNotFound, nil},
		"PNG":       {http.StatusOK, encodedImage(t, 128, 128, func(b *bytes.Buffer, m image.Image) error { return png.Encode(b, m) })},
		"too wide":  {http.StatusOK, jpegPhoto(t, 513, 16)},
		"too large": {http.StatusOK, append(append([]byte{}, photo...), make([]byte, maxPhotoBytes)...)},
		"truncated": {http.StatusOK, photo[:len(photo)/2]},
	} {
		status, body = tc.status, tc.body
		if _, err := h.fetchPhoto(context.Background(), "synthetic-token"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
