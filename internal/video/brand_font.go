package video

import (
	"net/http"
	"os"
	"path/filepath"
)

// brandHeadingFont is the one file BrandFont serves: TT Neoris DemiBold, as
// licensed, unchanged.
const brandHeadingFont = "tt-neoris-demibold.ttf"

// brandFontDir is the directory MPD's runtime image keeps its licensed fonts
// in. Set once at startup, before the server takes requests, and read-only
// after. Empty means none.
var brandFontDir string

func SetBrandFontDir(dir string) { brandFontDir = dir }

// BrandFont serves the heading font from brandFontDir, and 404 when the image
// does not carry it, so pages fall back to Geist.
func BrandFont(w http.ResponseWriter, r *http.Request) {
	if brandFontDir == "" {
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(filepath.Join(brandFontDir, brandHeadingFont))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "font/ttf")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	http.ServeContent(w, r, brandHeadingFont, info.ModTime(), file)
}
