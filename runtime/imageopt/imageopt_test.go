package imageopt

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"
)

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func ptr(s string) *string { return &s }

func TestGlob(t *testing.T) {
	for _, c := range []struct {
		glob, s string
		want    bool
	}{
		{"**", "/a/b.png", true},
		{"/images/**", "/images/a/b.png", true},
		{"/images/**", "/images", true},
		{"/images/**", "/other/a.png", false},
		{"/images/*", "/images/a.png", true},
		{"/images/*", "/images/a/b.png", false},
		{"**.example.com", "a.b.example.com", true},
		{"*.example.com", "cdn.example.com", true},
		{"*.example.com", "example.com", false},
		{"/a/**/b.png", "/a/x/y/b.png", true},
		{"/a/**/b.png", "/a/b.png", true},
		{"/{x,y}/*.png", "/y/1.png", true},
	} {
		if got := globRe(c.glob).MatchString(c.s); got != c.want {
			t.Errorf("glob %q on %q = %v, want %v", c.glob, c.s, got, c.want)
		}
	}
	u, _ := url.Parse("https://cdn.example.com:8443/img/a.png?v=1")
	pat := RemotePattern{Protocol: ptr("https"), Hostname: ptr("*.example.com"), Port: ptr("8443"), Pathname: ptr("/img/**")}
	if !hasRemoteMatch(nil, []RemotePattern{pat}, u) {
		t.Error("remote pattern did not match")
	}
	pat.Search = ptr("?v=2")
	if hasRemoteMatch(nil, []RemotePattern{pat}, u) {
		t.Error("search mismatch matched")
	}
	if !hasLocalMatch(nil, "/x.png") || hasLocalMatch([]LocalPattern{{Pathname: ptr("/assets/**")}}, "/x.png") {
		t.Error("local patterns")
	}
}

func TestSniffing(t *testing.T) {
	p := pngBytes(t, 3, 2)
	if ct := DetectContentType(p); ct != PNG {
		t.Errorf("png: %q", ct)
	}
	if w, h, err := Size(p); err != nil || w != 3 || h != 2 {
		t.Errorf("png size %d %d %v", w, h, err)
	}
	svg := []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 30 15" width="60"></svg>`)
	if w, h, err := Size(svg); err != nil || w != 60 || h != 30 {
		t.Errorf("svg size %d %d %v", w, h, err)
	}
	var anim bytes.Buffer
	pal := image.NewPaletted(image.Rect(0, 0, 2, 2), color.Palette{color.Black, color.White})
	gif.EncodeAll(&anim, &gif.GIF{Image: []*image.Paletted{pal, pal}, Delay: []int{1, 1}})
	if DetectContentType(anim.Bytes()) != GIF || !IsAnimated(anim.Bytes()) || IsAnimated(p) {
		t.Error("animated GIF detection")
	}
}

// exifJPEG is a 4x2 JPEG with an APP1 EXIF orientation of 6 (rotate 90° CW).
func exifJPEG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 4, 2)), nil)
	tiff := []byte("MM\x00\x2a\x00\x00\x00\x08\x00\x01\x01\x12\x00\x03\x00\x00\x00\x01\x00\x06\x00\x00\x00\x00\x00\x00")
	app1 := append([]byte("Exif\x00\x00"), tiff...)
	seg := append([]byte{0xff, 0xe1, byte((len(app1) + 2) >> 8), byte(len(app1) + 2)}, app1...)
	j := b.Bytes()
	return append(append(append([]byte{}, j[:2]...), seg...), j[2:]...)
}

func TestOrientation(t *testing.T) {
	j := exifJPEG(t)
	if o := jpegOrientation(j); o != 6 {
		t.Fatalf("orientation = %d", o)
	}
	if w, h, _ := Size(j); w != 2 || h != 4 {
		t.Errorf("Size = %dx%d, want rotated 2x4", w, h)
	}
	out, err := Optimize(j, PNG, 75, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _, _ := image.DecodeConfig(bytes.NewReader(out))
	if cfg.Width != 2 || cfg.Height != 4 {
		t.Errorf("optimized %dx%d, want 2x4 (auto-oriented, not enlarged)", cfg.Width, cfg.Height)
	}
}

func TestRemoteImages(t *testing.T) {
	img := pngBytes(t, 64, 32)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/moved.png" {
			http.Redirect(w, r, "/a.png", http.StatusFound)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=99999")
		w.Write(img)
	}))
	defer upstream.Close()
	uu, _ := url.Parse(upstream.URL)
	cfg := Config{
		DeviceSizes: []int{640}, ImageSizes: []int{16},
		RemotePatterns: []RemotePattern{{Hostname: ptr(uu.Hostname())}, {Hostname: ptr("images.test")}},
	}
	do := func(h *Handler, src string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/_next/image?w=16&q=75&url="+url.QueryEscape(src), nil))
		return rec
	}
	// A loopback upstream is refused unless dangerouslyAllowLocalIP.
	if rec := do(&Handler{Config: cfg}, upstream.URL+"/a.png"); rec.Code != 400 || rec.Body.String() != `"url" parameter is not allowed` {
		t.Errorf("loopback: %d %q", rec.Code, rec.Body)
	}
	// A hostname resolving to a private address is refused too.
	h := &Handler{Config: cfg, Resolver: func(context.Context, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("10.1.2.3")}, nil
	}}
	if rec := do(h, "http://images.test/a.png"); rec.Code != 400 {
		t.Errorf("private DNS: %d %q", rec.Code, rec.Body)
	}
	cfg.DangerouslyAllowLocalIP = true
	rec := do(&Handler{Config: cfg}, upstream.URL+"/moved.png")
	if rec.Code != 200 || rec.Header().Get("Content-Type") != PNG || rec.Header().Get("Cache-Control") != "public, max-age=99999, must-revalidate" {
		t.Fatalf("allowed: %d %v %q", rec.Code, rec.Header(), rec.Body)
	}
	if c, _, _ := image.DecodeConfig(bytes.NewReader(rec.Body.Bytes())); c.Width != 16 || c.Height != 8 {
		t.Errorf("resized to %dx%d", c.Width, c.Height)
	}
	// Named after the requested URL, as in Next.
	if rec.Header().Get("Content-Disposition") != `attachment; filename="moved.png"` {
		t.Errorf("Content-Disposition %q", rec.Header().Get("Content-Disposition"))
	}
}
