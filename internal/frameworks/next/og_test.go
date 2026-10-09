package next

import (
	"bytes"
	"html"
	"image"
	"image/png"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// TestImageResponse renders a dynamic opengraph-image.tsx (next/og's
// ImageResponse: satori, yoga and resvg on the runtime's WebAssembly).
func TestImageResponse(t *testing.T) {
	srv := serveFixture(t)
	resp, err := http.Get(srv.URL + "/gallery")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	body := string(b)
	m := regexp.MustCompile(`<meta property="og:image" content="http://[^/]+(/gallery/opengraph-image\?[0-9a-f]{16})"/>`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("no og:image for the dynamic image")
	}
	for _, w := range []string{`<meta property="og:image:width" content="120"/>`, `<meta property="og:image:type" content="image/png"/>`, `<meta property="og:image:alt" content="The gallery"/>`} {
		if !strings.Contains(body, w) {
			t.Errorf("/gallery lacks %s", w)
		}
	}
	resp, err = http.Get(srv.URL + html.UnescapeString(m[1]))
	if err != nil {
		t.Fatal(err)
	}
	img, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("GET %s: %d %q %.200s", m[1], resp.StatusCode, resp.Header.Get("Content-Type"), img)
	}
	decoded, err := png.Decode(bytes.NewReader(img))
	if err != nil {
		t.Fatal(err)
	}
	if b := decoded.Bounds(); b != image.Rect(0, 0, 120, 63) {
		t.Errorf("image is %v, want 120x63", b)
	}
	// A corner pixel shows the background (#7928ca).
	r, g, bl, _ := decoded.At(2, 2).RGBA()
	if r>>8 != 0x79 || g>>8 != 0x28 || bl>>8 != 0xca {
		t.Errorf("corner pixel = #%02x%02x%02x, want #7928ca", r>>8, g>>8, bl>>8)
	}
}
