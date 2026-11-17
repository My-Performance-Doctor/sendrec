package mpd

import (
	"encoding/binary"
	"image"
	"image/draw"
)

// jpegOrientation returns the EXIF Orientation (1 to 8) of a JPEG, or 1 when
// it has none or the block cannot be read. Re-encoding drops the EXIF block,
// so the photo's pixels must be turned to match it first, or a portrait taken
// on a phone would show on its side.
func jpegOrientation(b []byte) int {
	if len(b) < 4 || b[0] != 0xFF || b[1] != 0xD8 {
		return 1
	}
	for i := 2; i+4 <= len(b); {
		if b[i] != 0xFF {
			return 1
		}
		marker := b[i+1]
		if marker == 0xD9 || marker == 0xDA { // end of image, or start of scan: no more metadata
			return 1
		}
		size := int(binary.BigEndian.Uint16(b[i+2 : i+4]))
		if size < 2 || i+2+size > len(b) {
			return 1
		}
		segment := b[i+4 : i+2+size]
		if marker == 0xE1 && len(segment) >= 6 && string(segment[:6]) == "Exif\x00\x00" {
			return tiffOrientation(segment[6:])
		}
		i += 2 + size
	}
	return 1
}

// tiffOrientation reads tag 0x0112 from the first IFD of an EXIF TIFF block.
func tiffOrientation(t []byte) int {
	if len(t) < 8 {
		return 1
	}
	var order binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	ifd := int(order.Uint32(t[4:8]))
	if ifd < 8 || ifd+2 > len(t) {
		return 1
	}
	count := int(order.Uint16(t[ifd : ifd+2]))
	for n := 0; n < count; n++ {
		entry := ifd + 2 + n*12
		if entry+12 > len(t) {
			return 1
		}
		// Orientation is one SHORT, stored in the first two bytes of the value.
		if order.Uint16(t[entry:entry+2]) == 0x0112 && order.Uint16(t[entry+2:entry+4]) == 3 {
			if v := int(order.Uint16(t[entry+8 : entry+10])); v >= 1 && v <= 8 {
				return v
			}
			return 1
		}
	}
	return 1
}

// orient returns img turned and flipped as EXIF orientation o describes, so it
// displays upright without the tag.
func orient(img image.Image, o int) image.Image {
	if o <= 1 || o > 8 {
		return img
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	src := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(src, src.Bounds(), img, b.Min, draw.Src)
	dw, dh := w, h
	if o >= 5 { // 5 to 8 swap width and height
		dw, dh = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2: // mirrored horizontally
				dx, dy = w-1-x, y
			case 3: // rotated 180
				dx, dy = w-1-x, h-1-y
			case 4: // mirrored vertically
				dx, dy = x, h-1-y
			case 5: // mirrored along the top-left diagonal
				dx, dy = y, x
			case 6: // needs a 90 degree clockwise turn
				dx, dy = h-1-y, x
			case 7: // mirrored along the top-right diagonal
				dx, dy = h-1-y, w-1-x
			case 8: // needs a 90 degree anticlockwise turn
				dx, dy = y, w-1-x
			}
			dst.Set(dx, dy, src.At(x, y))
		}
	}
	return dst
}
