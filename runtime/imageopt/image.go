// Package imageopt is Oganesson's stand-in for the sharp-backed parts of
// Next's image optimizer (next/dist/server/image-optimizer.js): content
// sniffing, image sizes, animation detection, and resize/re-encode. The
// /_next/image endpoint (handler.go) and the build's static image imports
// both use it.
package imageopt

import (
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math"
	"regexp"
	"strconv"
	"strings"

	"github.com/gen2brain/webp"
	_ "golang.org/x/image/bmp" // decoder registration
	xdraw "golang.org/x/image/draw"
)

// Content types, as in image-optimizer.js.
const (
	AVIF = "image/avif"
	WEBP = "image/webp"
	PNG  = "image/png"
	JPEG = "image/jpeg"
	JXL  = "image/jxl"
	JP2  = "image/jp2"
	HEIC = "image/heic"
	GIF  = "image/gif"
	SVG  = "image/svg+xml"
	ICO  = "image/x-icon"
	ICNS = "image/x-icns"
	TIFF = "image/tiff"
	BMP  = "image/bmp"
	PDF  = "application/pdf"
)

// AnimatableTypes and BypassTypes mirror ANIMATABLE_TYPES and BYPASS_TYPES.
var (
	AnimatableTypes = []string{WEBP, PNG, GIF}
	BypassTypes     = []string{SVG, ICO, ICNS, BMP, JXL, HEIC}
)

func match(b []byte, sig ...int) bool {
	if len(b) < len(sig) {
		return false
	}
	for i, s := range sig {
		if s >= 0 && b[i] != byte(s) {
			return false
		}
	}
	return true
}

// DetectContentType ports detectContentType (magic numbers only; -1 is a
// wildcard byte). It returns "" for unknown content.
func DetectContentType(b []byte) string {
	switch {
	case len(b) == 0:
		return ""
	case match(b, 0xff, 0xd8, 0xff):
		return JPEG
	case match(b, 0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a):
		return PNG
	case match(b, 0x47, 0x49, 0x46, 0x38):
		return GIF
	case match(b, 0x52, 0x49, 0x46, 0x46, -1, -1, -1, -1, 0x57, 0x45, 0x42, 0x50):
		return WEBP
	case match(b, 0x3c, 0x3f, 0x78, 0x6d, 0x6c), match(b, 0x3c, 0x73, 0x76, 0x67):
		return SVG
	case match(b, -1, -1, -1, -1, 0x66, 0x74, 0x79, 0x70, 0x61, 0x76, 0x69, 0x66):
		return AVIF
	case match(b, 0x00, 0x00, 0x01, 0x00):
		return ICO
	case match(b, 0x69, 0x63, 0x6e, 0x73):
		return ICNS
	case match(b, 0x49, 0x49, 0x2a, 0x00):
		return TIFF
	case match(b, 0x42, 0x4d):
		return BMP
	case match(b, 0xff, 0x0a), match(b, 0x00, 0x00, 0x00, 0x0c, 0x4a, 0x58, 0x4c, 0x20, 0x0d, 0x0a, 0x87, 0x0a):
		return JXL
	case match(b, -1, -1, -1, -1, 0x66, 0x74, 0x79, 0x70, 0x68, 0x65, 0x69, 0x63):
		return HEIC
	case match(b, 0x25, 0x50, 0x44, 0x46, 0x2d):
		return PDF
	case match(b, 0x00, 0x00, 0x00, 0x0c, 0x6a, 0x50, 0x20, 0x20, 0x0d, 0x0a, 0x87, 0x0a):
		return JP2
	}
	// Next falls back to image-size's detector; of its formats, only
	// these matter here.
	if cfg, format, err := image.DecodeConfig(bytes.NewReader(b)); err == nil && cfg.Width > 0 {
		switch format {
		case "jpeg":
			return JPEG
		case "png":
			return PNG
		case "gif":
			return GIF
		case "bmp":
			return BMP
		}
	}
	if head := b[:min(len(b), 1024)]; bytes.HasPrefix(bytes.TrimSpace(head), []byte("<")) && bytes.Contains(head, []byte("<svg")) {
		return SVG
	}
	return ""
}

// Extension maps a content type to the file extension Next's serve-static
// getExtension returns.
func Extension(contentType string) string {
	switch contentType {
	case JPEG:
		return "jpeg"
	case PNG:
		return "png"
	case GIF:
		return "gif"
	case WEBP:
		return "webp"
	case AVIF:
		return "avif"
	case SVG:
		return "svg"
	case ICO:
		return "ico"
	case ICNS:
		return "icns"
	case TIFF:
		return "tiff"
	case BMP:
		return "bmp"
	case JXL:
		return "jxl"
	case HEIC:
		return "heic"
	case JP2:
		return "jp2"
	case PDF:
		return "pdf"
	}
	return ""
}

// IsAnimated ports the is-animated package: a GIF with more than one
// frame, a PNG with an acTL chunk, or a WebP with the VP8X animation flag.
func IsAnimated(b []byte) bool {
	switch DetectContentType(b) {
	case GIF:
		g, err := gif.DecodeAll(bytes.NewReader(b))
		return err == nil && len(g.Image) > 1
	case PNG:
		for off := 8; off+8 <= len(b); {
			n := int(binary.BigEndian.Uint32(b[off:]))
			typ := string(b[off+4 : off+8])
			if typ == "acTL" {
				return true
			}
			if typ == "IDAT" {
				return false
			}
			off += 12 + n
		}
	case WEBP:
		return len(b) > 20 && string(b[12:16]) == "VP8X" && b[20]&0x02 != 0
	}
	return false
}

// Size reports an image's dimensions as the image-size package does (for
// an ICO, its first image; for an SVG, width/height or the viewBox).
func Size(b []byte) (w, h int, err error) {
	switch DetectContentType(b) {
	case ICO:
		if len(b) < 22 || binary.LittleEndian.Uint16(b[4:]) == 0 {
			return 0, 0, errors.New("invalid ICO")
		}
		w, h = int(b[6]), int(b[7])
		if w == 0 {
			w = 256
		}
		if h == 0 {
			h = 256
		}
		return w, h, nil
	case SVG:
		return svgSize(b)
	case WEBP:
		cfg, err := webp.DecodeConfig(bytes.NewReader(b))
		return cfg.Width, cfg.Height, err
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return 0, 0, fmt.Errorf("unsupported image type")
	}
	if o := jpegOrientation(b); o >= 5 { // image-size reports the rotated size
		return cfg.Height, cfg.Width, nil
	}
	return cfg.Width, cfg.Height, nil
}

var svgUnits = map[string]float64{"": 1, "px": 1, "in": 96, "cm": 96 / 2.54, "mm": 96 / 25.4, "pt": 96.0 / 72, "pc": 96.0 / 6, "em": 16, "ex": 8}

var svgLength = regexp.MustCompile(`^([0-9.]+(?:e\d+)?)([a-z]*)$`)

func svgSize(b []byte) (int, int, error) {
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = false
	for {
		tok, err := dec.Token()
		if err != nil {
			return 0, 0, errors.New("invalid SVG")
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Local != "svg" {
			continue
		}
		attrs := map[string]string{}
		for _, a := range se.Attr {
			attrs[a.Name.Local] = a.Value
		}
		parse := func(s string) float64 {
			m := svgLength.FindStringSubmatch(strings.TrimSpace(s))
			if m == nil {
				return 0
			}
			f, _ := strconv.ParseFloat(m[1], 64)
			return f * svgUnits[m[2]]
		}
		w, h := parse(attrs["width"]), parse(attrs["height"])
		var vb []float64
		for _, f := range strings.FieldsFunc(attrs["viewBox"], func(r rune) bool { return r == ' ' || r == ',' }) {
			v, _ := strconv.ParseFloat(f, 64)
			vb = append(vb, v)
		}
		ratio := 0.0
		if len(vb) == 4 && vb[3] != 0 {
			ratio = vb[2] / vb[3]
		}
		switch {
		case w > 0 && h > 0:
		case w > 0 && ratio > 0:
			h = w / ratio
		case h > 0 && ratio > 0:
			w = h * ratio
		case len(vb) == 4:
			w, h = vb[2], vb[3]
		default:
			return 0, 0, errors.New("invalid SVG")
		}
		return int(math.Floor(w)), int(math.Floor(h)), nil
	}
}

// jpegOrientation returns the EXIF orientation of a JPEG (1 when absent).
func jpegOrientation(b []byte) int {
	if !match(b, 0xff, 0xd8) {
		return 1
	}
	for off := 2; off+4 <= len(b); {
		if b[off] != 0xff {
			return 1
		}
		marker := b[off+1]
		n := int(binary.BigEndian.Uint16(b[off+2:]))
		if marker == 0xda || off+2+n > len(b) { // start of scan
			return 1
		}
		seg := b[off+4 : off+2+n]
		if marker == 0xe1 && len(seg) > 14 && string(seg[:6]) == "Exif\x00\x00" {
			return exifOrientation(seg[6:])
		}
		off += 2 + n
	}
	return 1
}

func exifOrientation(t []byte) int {
	var bo binary.ByteOrder
	switch string(t[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return 1
	}
	ifd := int(bo.Uint32(t[4:]))
	if ifd+2 > len(t) {
		return 1
	}
	count := int(bo.Uint16(t[ifd:]))
	for i := 0; i < count; i++ {
		e := ifd + 2 + 12*i
		if e+12 > len(t) {
			return 1
		}
		if bo.Uint16(t[e:]) == 0x0112 {
			if o := int(bo.Uint16(t[e+8:])); o >= 1 && o <= 8 {
				return o
			}
			return 1
		}
	}
	return 1
}

// orient applies an EXIF orientation, like sharp's rotate().
func orient(src image.Image, o int) image.Image {
	if o <= 1 {
		return src
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	dw, dh := w, h
	if o >= 5 {
		dw, dh = h, w
	}
	dst := image.NewNRGBA(image.Rect(0, 0, dw, dh))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch o {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			dst.Set(dx, dy, src.At(b.Min.X+x, b.Min.Y+y))
		}
	}
	return dst
}

// Optimize ports optimizeImage: auto-orient, resize to width (keeping the
// aspect ratio and never enlarging) or to width x height when height > 0,
// then encode as contentType with quality. AVIF output is not supported.
func Optimize(b []byte, contentType string, quality, width, height int) ([]byte, error) {
	src, _, err := Decode(b)
	if err != nil {
		return nil, err
	}
	src = Orient(src, Orientation(b))
	sb := src.Bounds()
	dw, dh := sb.Dx(), sb.Dy()
	switch {
	case height > 0:
		dw, dh = width, height
	case width < sb.Dx():
		dw, dh = width, max(1, int(math.Round(float64(sb.Dy())*float64(width)/float64(sb.Dx()))))
	}
	return Encode(Scale(src, dw, dh), contentType, quality)
}

// Decode decodes an image (JPEG, PNG, GIF, WebP, BMP) and reports its
// content type. EXIF orientation is not applied (see Orient).
func Decode(b []byte) (image.Image, string, error) {
	ct := DetectContentType(b)
	if ct == WEBP {
		img, err := webp.Decode(bytes.NewReader(b))
		return img, ct, err
	}
	img, _, err := image.Decode(bytes.NewReader(b))
	return img, ct, err
}

// Orientation is the EXIF orientation of an encoded image (1 when none).
func Orientation(b []byte) int { return jpegOrientation(b) }

// Orient applies an EXIF orientation (2..8), like sharp's rotate().
func Orient(img image.Image, orientation int) image.Image { return orient(img, orientation) }

// Scale resamples img to w x h (CatmullRom), or returns it when unchanged.
func Scale(img image.Image, w, h int) image.Image {
	b := img.Bounds()
	if w == b.Dx() && h == b.Dy() {
		return img
	}
	dst := image.NewNRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), img, b, xdraw.Src, nil)
	return dst
}

// Encode encodes img as contentType (JPEG, PNG, WebP, GIF) with quality
// (JPEG and WebP).
func Encode(img image.Image, contentType string, quality int) ([]byte, error) {
	var out bytes.Buffer
	var err error
	switch contentType {
	case WEBP:
		err = webp.Encode(&out, img, webp.Options{Quality: quality, Method: 4})
	case PNG:
		err = (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&out, img)
	case JPEG:
		err = jpeg.Encode(&out, flatten(img), &jpeg.Options{Quality: quality})
	case GIF:
		err = gif.Encode(&out, img, nil)
	default:
		err = fmt.Errorf("imageopt: cannot encode %s", contentType)
	}
	if err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// flatten composites an image with alpha over black, as libvips does when
// writing JPEG.
func flatten(img image.Image) image.Image {
	if o, ok := img.(interface{ Opaque() bool }); ok && o.Opaque() {
		return img
	}
	dst := image.NewRGBA(img.Bounds())
	draw.Draw(dst, dst.Bounds(), image.NewUniform(color.Black), image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), img, img.Bounds().Min, draw.Over)
	return dst
}
