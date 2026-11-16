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
	decode := func(profile string) *Profile {
		t.Helper()
		raw := `{"schemaVersion":1,"staffId":"` + staffID + `","tenantId":"` + tenantID + `","capabilities":{"read":true},"validUntil":"` + now.Add(time.Minute).Format(time.RFC3339Nano) + `"` + profile + `}`
		g, err := decodeGrant(strings.NewReader(raw), now, now.Add(time.Minute))
		if err != nil {
			t.Fatalf("profile %s refused the grant: %v", profile, err)
		}
		return g.Profile
	}
	str := func(p *string) string {
		if p == nil {
			return "<nil>"
		}
		return *p
	}
	long := strings.Repeat("é", maxNameRunes)
	for _, tc := range []struct {
		profile, name, version string
		dropped                bool
	}{
		{profile: ``, dropped: true},
		{profile: `,"profile":null`, dropped: true},
		{profile: `,"profile":"x"`, dropped: true},
		{profile: `,"profile":{"displayName":"Dr Synthetic Staff"}`, dropped: true},
		{profile: `,"profile":{"photoVersion":5}`, dropped: true},
		{profile: `,"profile":{"photoVersion":"../photo"}`, dropped: true},
		{profile: `,"profile":{"photoVersion":""}`, dropped: true},
		{profile: `,"profile":{"displayName":" Dr Synthetic Staff ","photoVersion":"0f3a9c"}`, name: "Dr Synthetic Staff", version: "0f3a9c"},
		{profile: `,"profile":{"displayName":null,"photoVersion":null}`, name: "<nil>", version: "<nil>"},
		{profile: `,"profile":{"photoVersion":null}`, name: "<nil>", version: "<nil>"},
		{profile: `,"profile":{"displayName":"` + long + `","photoVersion":null}`, name: long, version: "<nil>"},
		{profile: `,"profile":{"displayName":"` + long + `e","photoVersion":"0f3a9c"}`, name: "<nil>", version: "0f3a9c"},
		{profile: `,"profile":{"displayName":"   ","photoVersion":"0f3a9c"}`, name: "<nil>", version: "0f3a9c"},
		{profile: `,"profile":{"displayName":"Dr\nStaff","photoVersion":"0f3a9c"}`, name: "<nil>", version: "0f3a9c"},
		{profile: `,"profile":{"displayName":5,"photoVersion":"0f3a9c"}`, name: "<nil>", version: "0f3a9c"},
	} {
		got := decode(tc.profile)
		if tc.dropped {
			if got != nil {
				t.Errorf("%s: profile kept", tc.profile)
			}
			continue
		}
		if got == nil || str(got.DisplayName) != tc.name || str(got.PhotoVersion) != tc.version {
			t.Errorf("%s: got %+v", tc.profile, got)
		}
	}
}

func TestFetchPhotoAcceptsOnlyASmallJPEG(t *testing.T) {
	photo := jpegPhoto(t, 128, 128)
	status, body, etag := http.StatusOK, photo, `"v1"`
	var auth, path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, path = r.Header.Get("Authorization"), r.URL.Path
		w.Header().Set("ETag", etag)
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	h := &Handler{cfg: Config{AccessURL: srv.URL + "/api/v1/staff/video-access"}, client: srv.Client()}

	got, err := h.fetchPhoto(context.Background(), "synthetic-token", "v1")
	if err != nil || !bytes.Equal(got, photo) {
		t.Fatalf("photo not read: %v", err)
	}
	if auth != "Bearer synthetic-token" || path != "/api/v1/staff/video-access/photo" {
		t.Fatalf("photo read from %s with %q", path, auth)
	}
	for name, tc := range map[string]struct {
		status int
		body   []byte
		etag   string
	}{
		"other version": {http.StatusOK, photo, `"v0"`},
		"no version":    {http.StatusOK, photo, ""},
		"no photo":      {http.StatusNotFound, nil, `"v1"`},
		"PNG":           {http.StatusOK, encodedImage(t, 128, 128, func(b *bytes.Buffer, m image.Image) error { return png.Encode(b, m) }), `"v1"`},
		"too wide":      {http.StatusOK, jpegPhoto(t, 513, 16), `"v1"`},
		"too large":     {http.StatusOK, append(append([]byte{}, photo...), make([]byte, maxPhotoBytes)...), `"v1"`},
		"truncated":     {http.StatusOK, photo[:len(photo)/2], `"v1"`},
	} {
		status, body, etag = tc.status, tc.body, tc.etag
		if _, err := h.fetchPhoto(context.Background(), "synthetic-token", "v1"); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
