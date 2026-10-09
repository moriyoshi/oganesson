package next

import (
	"bytes"
	"html"
	"image"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/gen2brain/webp"
)

func TestNextImage(t *testing.T) {
	srv := serveFixture(t)
	get := func(p, accept string, hdr ...string) (*http.Response, []byte) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+p, nil)
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		for i := 0; i+1 < len(hdr); i += 2 {
			req.Header.Set(hdr[i], hdr[i+1])
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, b
	}
	_, page := get("/gallery", "")
	body := string(page)
	photo := regexp.MustCompile(`<img id="photo"[^>]*>`).FindString(body)
	square := regexp.MustCompile(`<img id="square"[^>]*>`).FindString(body)
	if photo == "" || square == "" {
		t.Fatalf("images not rendered:\n%s", body)
	}
	for _, want := range []string{`width="200"`, `height="100"`, `background-image:url(&quot;data:image/svg+xml`, `data:image/jpeg;base64,`} {
		if !strings.Contains(photo, want) {
			t.Errorf("photo <img> lacks %s: %s", want, photo)
		}
	}
	src := func(tag string) string {
		m := regexp.MustCompile(` src="([^"]+)"`).FindStringSubmatch(tag)
		if m == nil {
			t.Fatalf("no src in %s", tag)
		}
		return html.UnescapeString(m[1])
	}
	photoSrc, squareSrc := src(photo), src(square)
	if !regexp.MustCompile(`^/_next/image\?url=%2F_next%2Fstatic%2Fmedia%2Fphoto\.[0-9a-f]{8}\.jpg&w=640&q=75$`).MatchString(photoSrc) {
		t.Errorf("photo src = %s", photoSrc)
	}
	if squareSrc != "/_next/image?url=%2Fsquare.png&w=96&q=75" {
		t.Errorf("square src = %s", squareSrc)
	}

	// The emitted static asset.
	media := regexp.MustCompile(`/_next/static/media/photo\.[0-9a-f]{8}\.jpg`).FindString(photoSrc + " " + strings.ReplaceAll(photoSrc, "%2F", "/"))
	if r, b := get(media, ""); r.StatusCode != 200 || !bytes.HasPrefix(b, []byte{0xff, 0xd8}) {
		t.Errorf("GET %s: %d", media, r.StatusCode)
	}

	// WebP when accepted; the static import is immutable; never enlarged.
	r, b := get(photoSrc, "image/avif,image/webp,*/*")
	if r.StatusCode != 200 || r.Header.Get("Content-Type") != "image/webp" || r.Header.Get("X-Nextjs-Cache") != "MISS" ||
		r.Header.Get("Cache-Control") != "public, max-age=315360000, immutable" || r.Header.Get("Vary") != "Accept" {
		t.Fatalf("GET %s: %d %v", photoSrc, r.StatusCode, r.Header)
	}
	if cfg, err := webp.DecodeConfig(bytes.NewReader(b)); err != nil || cfg.Width != 200 || cfg.Height != 100 {
		t.Errorf("optimized photo: %+v %v", cfg, err)
	}
	if cd := r.Header.Get("Content-Disposition"); cd != `attachment; filename="photo.webp"` {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if csp := r.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") {
		t.Errorf("Content-Security-Policy = %q", csp)
	}
	etag := r.Header.Get("ETag")
	if r, _ := get(photoSrc, "image/webp"); r.Header.Get("X-Nextjs-Cache") != "HIT" {
		t.Errorf("second GET: X-Nextjs-Cache %q", r.Header.Get("X-Nextjs-Cache"))
	}
	if r, _ := get(photoSrc, "image/webp", "If-None-Match", etag); r.StatusCode != http.StatusNotModified {
		t.Errorf("If-None-Match: %d", r.StatusCode)
	}

	// Without WebP in Accept, the upstream format; public files get minimumCacheTTL.
	r, b = get("/_next/image?url=%2Fsquare.png&w=48&q=75", "image/png")
	if r.StatusCode != 200 || r.Header.Get("Content-Type") != "image/png" || r.Header.Get("Cache-Control") != "public, max-age=14400, must-revalidate" {
		t.Fatalf("square: %d %v", r.StatusCode, r.Header)
	}
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(b)); err != nil || cfg.Width != 48 || cfg.Height != 48 {
		t.Errorf("resized square: %+v %v", cfg, err)
	}

	for p, want := range map[string]string{
		"/_next/image?w=48&q=75":                                       `"url" parameter is required`,
		"/_next/image?url=%2Fsquare.png&w=123&q=75":                    `"w" parameter (width) of 123 is not allowed`,
		"/_next/image?url=%2Fsquare.png&w=48":                          `"q" parameter (quality) is required`,
		"/_next/image?url=%2F_next%2Fimage%3Furl%3Dx&w=48&q=75":        `"url" parameter cannot be recursive`,
		"/_next/image?url=https%3A%2F%2Fexample.com%2Fa.png&w=48&q=75": `"url" parameter is not allowed`,
		"/_next/image?url=%2Fhello.txt&w=48&q=75":                      `The requested resource isn't a valid image.`,
	} {
		if r, b := get(p, ""); r.StatusCode != 400 || string(b) != want {
			t.Errorf("GET %s: %d %q, want 400 %q", p, r.StatusCode, b, want)
		}
	}
}
