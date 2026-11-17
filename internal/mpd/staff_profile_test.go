package mpd

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
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

// A phone saves a portrait as landscape pixels with Orientation 6. Re-encoding
// drops the tag, so the pixels are turned first and the photo stays upright.
func TestFetchPhotoAppliesTheOrientation(t *testing.T) {
	// 40 wide, 20 high: the left half red, the right half blue.
	img := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 40; x++ {
			c := color.RGBA{R: 255, A: 255}
			if x >= 20 {
				c = color.RGBA{B: 255, A: 255}
			}
			img.Set(x, y, c)
		}
	}
	var plain bytes.Buffer
	if err := jpeg.Encode(&plain, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	// A little-endian TIFF block with one IFD entry: Orientation (0x0112), SHORT, 1, value 6.
	tiff := []byte{'I', 'I', 42, 0, 8, 0, 0, 0, 1, 0, 0x12, 0x01, 3, 0, 1, 0, 0, 0, 6, 0, 0, 0, 0, 0, 0, 0}
	payload := append([]byte("Exif\x00\x00"), tiff...)
	segment := append([]byte{0xFF, 0xE1, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}, payload...)
	tagged := append(append(append([]byte{}, plain.Bytes()[:2]...), segment...), plain.Bytes()[2:]...)
	if jpegOrientation(tagged) != 6 || jpegOrientation(plain.Bytes()) != 1 {
		t.Fatalf("orientation read as %d and %d", jpegOrientation(tagged), jpegOrientation(plain.Bytes()))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write(tagged)
	}))
	defer srv.Close()
	h := &Handler{cfg: Config{AccessURL: srv.URL + "/video-access"}, client: srv.Client()}
	got, err := h.fetchPhoto(context.Background(), "synthetic-token", "v1")
	if err != nil {
		t.Fatal(err)
	}
	upright, err := jpeg.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatal(err)
	}
	// Turned 90 degrees clockwise: 20 wide, 40 high, red on top, blue below.
	if b := upright.Bounds(); b.Dx() != 20 || b.Dy() != 40 {
		t.Fatalf("photo is %dx%d, want 20x40", b.Dx(), b.Dy())
	}
	if r, _, bl, _ := upright.At(10, 5).RGBA(); r < bl {
		t.Error("the top of the turned photo is not red")
	}
	if r, _, bl, _ := upright.At(10, 35).RGBA(); bl < r {
		t.Error("the bottom of the turned photo is not blue")
	}
	if jpegOrientation(got) != 1 {
		t.Error("the stored photo still carries an orientation")
	}
}

func TestFinalPhotoStatus(t *testing.T) {
	for status, final := range map[int]bool{400: true, 404: true, 410: true, 422: true, 401: false, 403: false, 408: false, 429: false, 500: false, 503: false} {
		if finalPhotoStatus(status) != final {
			t.Errorf("status %d: final %v", status, !final)
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
	if err != nil || !isJPEG(got) {
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
	status, body, etag = http.StatusForbidden, nil, `"v1"`
	var refused *photoStatusError
	if _, err := h.fetchPhoto(context.Background(), "synthetic-token", "v1"); !errors.As(err, &refused) || refused.status != http.StatusForbidden {
		t.Errorf("a 403 did not report its status: %v", err)
	}
}

// A photo's EXIF block can carry location and device details. The stored photo
// is re-encoded, so no metadata segment reaches a patient's page.
func TestFetchPhotoDropsMetadata(t *testing.T) {
	photo := jpegPhoto(t, 128, 128)
	secret := "GPS 51.5N 0.1W synthetic camera"
	payload := append([]byte("Exif\x00\x00"), []byte(secret)...)
	segment := append([]byte{0xFF, 0xE1, byte((len(payload) + 2) >> 8), byte(len(payload) + 2)}, payload...)
	tagged := append(append(append([]byte{}, photo[:2]...), segment...), photo[2:]...)
	if !bytes.Contains(tagged, []byte(secret)) || !isJPEG(tagged) {
		t.Fatal("test photo lacks its EXIF block")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write(tagged)
	}))
	defer srv.Close()
	h := &Handler{cfg: Config{AccessURL: srv.URL + "/video-access"}, client: srv.Client()}
	got, err := h.fetchPhoto(context.Background(), "synthetic-token", "v1")
	if err != nil || !isJPEG(got) {
		t.Fatalf("photo not read: %v", err)
	}
	if bytes.Contains(got, []byte("Exif")) || bytes.Contains(got, []byte(secret)) || bytes.Contains(got, []byte{0xFF, 0xE1}) {
		t.Fatal("the EXIF block reached the stored photo")
	}
}
