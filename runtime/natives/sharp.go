package natives

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"math"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/grafana/sobek"
	"github.com/moriyoshi/oganesson/runtime/imageopt"
)

// sharpOps is the pipeline js/sharp.js records, in sharp's order of
// operations: orientation and rotation, flip/flop, extract, resize,
// colour, output.
type sharpOps struct {
	AutoOrient bool `json:"autoOrient"`
	Rotate     int  `json:"rotate"`
	Flip       bool `json:"flip"`
	Flop       bool `json:"flop"`
	Extract    *struct {
		Left, Top, Width, Height int
	} `json:"extract"`
	Resize *struct {
		Width              int        `json:"width"`
		Height             int        `json:"height"`
		Fit                string     `json:"fit"`
		Position           string     `json:"position"`
		Background         [4]float64 `json:"background"`
		WithoutEnlargement bool       `json:"withoutEnlargement"`
		WithoutReduction   bool       `json:"withoutReduction"`
	} `json:"resize"`
	Grayscale bool   `json:"grayscale"`
	Format    string `json:"format"` // jpeg, png, webp, gif; "" keeps the input's
	Quality   int    `json:"quality"`
}

var sharpFormats = map[string]string{"jpeg": imageopt.JPEG, "png": imageopt.PNG, "webp": imageopt.WEBP, "gif": imageopt.GIF}

func formatName(contentType string) string {
	for k, v := range sharpFormats {
		if v == contentType {
			return k
		}
	}
	return strings.TrimPrefix(contentType, "image/")
}

// sharpInput reads the input: bytes, or a file path.
func sharpInput(v sobek.Value) ([]byte, error) {
	switch x := v.Export().(type) {
	case string:
		return os.ReadFile(x)
	case sobek.ArrayBuffer:
		return append([]byte(nil), x.Bytes()...), nil
	case []byte:
		return append([]byte(nil), x...), nil
	}
	return nil, fmt.Errorf("Input file is missing or of an unsupported image format")
}

func hasAlpha(img image.Image) bool {
	switch m := img.(type) {
	case *image.Gray, *image.Gray16, *image.YCbCr, *image.CMYK:
		return false
	case *image.Paletted:
		for _, c := range m.Palette {
			if _, _, _, a := c.RGBA(); a != 0xffff {
				return true
			}
		}
		return false
	}
	return true
}

func channels(img image.Image) int {
	switch img.(type) {
	case *image.Gray, *image.Gray16:
		return 1
	}
	if hasAlpha(img) {
		return 4
	}
	return 3
}

// sharpMetadata reports what sharp's metadata() does (dimensions are those
// stored, before EXIF orientation).
func sharpMetadata(b []byte) (map[string]any, error) {
	img, ct, err := imageopt.Decode(b)
	if err != nil {
		return nil, fmt.Errorf("Input buffer contains unsupported image format")
	}
	m := map[string]any{
		"format": formatName(ct), "width": img.Bounds().Dx(), "height": img.Bounds().Dy(),
		"space": "srgb", "channels": channels(img), "hasAlpha": hasAlpha(img), "size": len(b),
		"isProgressive": false,
	}
	if _, gray := img.(*image.Gray); gray {
		m["space"] = "b-w"
	}
	// As sharp 0.34 reports them: sample depth, libvips' default density,
	// palette and profile flags, and the size once auto-oriented (which
	// vite-imagetools reads).
	_, paletted := img.(*image.Paletted)
	bits := 8
	switch img.(type) {
	case *image.RGBA64, *image.NRGBA64, *image.Gray16:
		bits = 16
	}
	m["depth"] = map[int]string{8: "uchar", 16: "ushort"}[bits]
	m["bitsPerSample"] = bits
	m["density"] = 72
	m["isPalette"] = paletted
	m["hasProfile"] = false
	w, h := img.Bounds().Dx(), img.Bounds().Dy()
	o := imageopt.Orientation(b)
	if o > 1 {
		m["orientation"] = o
	}
	if o >= 5 && o <= 8 {
		w, h = h, w
	}
	m["autoOrient"] = map[string]any{"width": w, "height": h}
	return m, nil
}

func sharpProcess(b []byte, ops sharpOps) ([]byte, map[string]any, error) {
	img, ct, err := imageopt.Decode(b)
	if err != nil {
		return nil, nil, fmt.Errorf("Input buffer contains unsupported image format")
	}
	if ops.AutoOrient {
		img = imageopt.Orient(img, imageopt.Orientation(b))
	}
	switch ((ops.Rotate % 360) + 360) % 360 {
	case 0:
	case 90:
		img = imageopt.Orient(img, 6)
	case 180:
		img = imageopt.Orient(img, 3)
	case 270:
		img = imageopt.Orient(img, 8)
	default:
		return nil, nil, fmt.Errorf("rotate: only multiples of 90 degrees are supported by Oganesson")
	}
	if ops.Flip {
		img = imageopt.Orient(img, 4)
	}
	if ops.Flop {
		img = imageopt.Orient(img, 2)
	}
	if e := ops.Extract; e != nil {
		r := image.Rect(e.Left, e.Top, e.Left+e.Width, e.Top+e.Height).Add(img.Bounds().Min)
		if !r.In(img.Bounds()) || r.Empty() {
			return nil, nil, fmt.Errorf("extract_area: bad extract area")
		}
		img = crop(img, r)
	}
	if r := ops.Resize; r != nil {
		img, err = resize(img, r.Width, r.Height, r.Fit, r.Position, r.Background, r.WithoutEnlargement, r.WithoutReduction)
		if err != nil {
			return nil, nil, err
		}
	}
	if ops.Grayscale {
		g := image.NewGray(img.Bounds())
		draw.Draw(g, g.Bounds(), img, img.Bounds().Min, draw.Src)
		img = g
	}
	out := ct
	if ops.Format != "" {
		var ok bool
		if out, ok = sharpFormats[ops.Format]; !ok {
			return nil, nil, fmt.Errorf("toFormat: %s output is not supported by Oganesson (jpeg, png, webp, gif are)", ops.Format)
		}
	}
	if _, ok := sharpFormats[formatName(out)]; !ok {
		out = imageopt.PNG // e.g. BMP input without an explicit format
	}
	q := ops.Quality
	if q <= 0 {
		q = 80 // sharp's default for JPEG and WebP
	}
	data, err := imageopt.Encode(img, out, q)
	if err != nil {
		return nil, nil, err
	}
	info := map[string]any{
		"format": formatName(out), "width": img.Bounds().Dx(), "height": img.Bounds().Dy(),
		"channels": channels(img), "size": len(data), "premultiplied": false,
	}
	if out == imageopt.JPEG {
		info["channels"] = 3
	}
	return data, info, nil
}

func crop(img image.Image, r image.Rectangle) image.Image {
	dst := image.NewNRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	draw.Draw(dst, dst.Bounds(), img, r.Min, draw.Src)
	return dst
}

// resize follows sharp's fit modes: cover (default; crop), contain
// (letterbox with background), fill, inside, outside.
func resize(img image.Image, w, h int, fit, position string, bg [4]float64, noEnlarge, noReduce bool) (image.Image, error) {
	sw, sh := img.Bounds().Dx(), img.Bounds().Dy()
	if w <= 0 && h <= 0 {
		return img, nil
	}
	if fit == "" {
		fit = "cover"
	}
	// One dimension given: keep the aspect ratio.
	if w <= 0 || h <= 0 {
		var scale float64
		if w > 0 {
			scale = float64(w) / float64(sw)
		} else {
			scale = float64(h) / float64(sh)
		}
		if (scale > 1 && noEnlarge) || (scale < 1 && noReduce) {
			return img, nil
		}
		return imageopt.Scale(img, max(1, int(math.Round(float64(sw)*scale))), max(1, int(math.Round(float64(sh)*scale)))), nil
	}
	fx, fy := float64(w)/float64(sw), float64(h)/float64(sh)
	var scale float64
	switch fit {
	case "fill":
		if (fx > 1 || fy > 1) && noEnlarge || (fx < 1 || fy < 1) && noReduce {
			return img, nil
		}
		return imageopt.Scale(img, w, h), nil
	case "inside", "contain":
		scale = math.Min(fx, fy)
	case "cover", "outside":
		scale = math.Max(fx, fy)
	default:
		return nil, fmt.Errorf("resize: unknown fit %q", fit)
	}
	if (scale > 1 && noEnlarge) || (scale < 1 && noReduce) {
		return img, nil
	}
	rw, rh := max(1, int(math.Round(float64(sw)*scale))), max(1, int(math.Round(float64(sh)*scale)))
	scaled := imageopt.Scale(img, rw, rh)
	switch fit {
	case "cover":
		x, y := offset(rw-w, rh-h, position)
		return crop(scaled, image.Rect(x, y, x+w, y+h).Add(scaled.Bounds().Min)), nil
	case "contain":
		dst := image.NewNRGBA(image.Rect(0, 0, w, h))
		c := color.NRGBA{R: uint8(bg[0]), G: uint8(bg[1]), B: uint8(bg[2]), A: uint8(math.Round(bg[3] * 255))}
		draw.Draw(dst, dst.Bounds(), image.NewUniform(c), image.Point{}, draw.Src)
		x, y := offset(w-rw, h-rh, position)
		draw.Draw(dst, image.Rect(x, y, x+rw, y+rh), scaled, scaled.Bounds().Min, draw.Over)
		return dst, nil
	}
	return scaled, nil
}

// offset places a w x h overflow by position (centre by default).
func offset(dx, dy int, position string) (int, int) {
	x, y := dx/2, dy/2
	for _, p := range strings.Fields(strings.NewReplacer("-", " ", "north", "top", "south", "bottom", "east", "right", "west", "left").Replace(position)) {
		switch p {
		case "top":
			y = 0
		case "bottom":
			y = dy
		case "left":
			x = 0
		case "right":
			x = dx
		}
	}
	return x, y
}

// metadataKeys is the order sharp's metadata() sets its keys in
// (src/metadata.cc), and autoOrient's.
var metadataKeys = []string{
	"format", "size", "width", "height", "space", "channels", "depth", "density", "isProgressive",
	"isPalette", "bitsPerSample", "hasProfile", "hasAlpha", "orientation", "autoOrient",
}

// orderedObject is m as an object with sharp's key order (a Go map's
// order is random); keys not listed follow.
func orderedObject(vm *sobek.Runtime, m map[string]any) *sobek.Object {
	o := vm.NewObject()
	set := func(k string, v any) {
		if ao, ok := v.(map[string]any); ok && k == "autoOrient" {
			w := vm.NewObject()
			_ = w.Set("width", ao["width"])
			_ = w.Set("height", ao["height"])
			v = w
		}
		_ = o.Set(k, v)
	}
	for _, k := range metadataKeys {
		if v, ok := m[k]; ok {
			set(k, v)
		}
	}
	rest := make([]string, 0, len(m))
	for k := range m {
		if !slices.Contains(metadataKeys, k) {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	for _, k := range rest {
		set(k, m[k])
	}
	return o
}

func installSharp(vm *sobek.Runtime) (*sobek.Object, error) {
	o := vm.NewObject()
	throw := func(err error) { panic(vm.NewGoError(err)) }
	_ = o.Set("metadata", func(input sobek.Value) sobek.Value {
		b, err := sharpInput(input)
		if err != nil {
			throw(err)
		}
		m, err := sharpMetadata(b)
		if err != nil {
			throw(err)
		}
		return orderedObject(vm, m)
	})
	_ = o.Set("process", func(input sobek.Value, opsJSON string) sobek.Value {
		b, err := sharpInput(input)
		if err != nil {
			throw(err)
		}
		var ops sharpOps
		if err := json.Unmarshal([]byte(opsJSON), &ops); err != nil {
			throw(err)
		}
		data, info, err := sharpProcess(b, ops)
		if err != nil {
			throw(err)
		}
		return vm.ToValue(map[string]any{"data": vm.NewArrayBuffer(data), "info": info})
	})
	_ = o.Set("writeFile", func(path string, data sobek.ArrayBuffer) {
		if err := os.WriteFile(path, data.Bytes(), 0o644); err != nil {
			throw(err)
		}
	})
	return o, nil
}
