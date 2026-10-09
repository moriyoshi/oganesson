package next

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The expectations below were computed with Next 16.3.8's own
// normalizeMetadataRoute / fillStaticMetadataSegment.
func TestMetadataRouteNames(t *testing.T) {
	for in, want := range map[string]string{
		"/favicon.ico":                     "/favicon.ico/route",
		"/icon.png":                        "/icon.png/route",
		"/robots":                          "/robots.txt/route",
		"/manifest":                        "/manifest.webmanifest/route",
		"/sitemap":                         "/sitemap/route",
		"/blog/icon":                       "/blog/icon/route",
		"/@slot/icon":                      "/@slot/icon-xxgbpy/route",
		"/(group)/icon.png":                "/(group)/icon-1ow20b.png/route",
		"/about/page":                      "/about/page",
		"/blog/[slug]/opengraph-image.png": "/blog/[slug]/opengraph-image.png/route",
	} {
		if got := normalizeMetadataRoute(in); got != want {
			t.Errorf("normalizeMetadataRoute(%q) = %q, want %q", in, got, want)
		}
	}
	if got := normalizeMetadataPageToRoute("/sitemap/route", false); got != "/sitemap.xml/route" {
		t.Errorf("sitemap page = %q", got)
	}
	if got := normalizeMetadataPageToRoute("/icon/route", true); got != "/icon/[__metadata_id__]/route" {
		t.Errorf("icon page = %q", got)
	}
	for _, c := range [][3]string{
		{"", "favicon.ico", "/favicon.ico"},
		{"/blog/[slug]", "opengraph-image.png", "/blog/-/opengraph-image.png"},
		{"/(group)", "icon.png", "/icon-1ow20b.png"},
	} {
		if got := fillStaticMetadataSegment(c[0], c[1]); got != c[2] {
			t.Errorf("fillStaticMetadataSegment(%q, %q) = %q, want %q", c[0], c[1], got, c[2])
		}
	}
	for p, want := range map[string]bool{"/icon.png": true, "/blog/icon2.tsx": true, "/blog/robots.ts": false, "/icon.txt": false, "/page.tsx": false} {
		if got := isMetadataRouteFile(p, DefaultPageExtensions, true); got != want {
			t.Errorf("isMetadataRouteFile(%q) = %v", p, got)
		}
	}
}

func TestMetadataFiles(t *testing.T) {
	srv := serveFixture(t)
	get := func(p string) (*http.Response, string) {
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}
	_, home := get("/")
	for _, re := range []string{
		`<link rel="icon" href="/favicon.ico\?[0-9a-f]{16}" type="image/x-icon" sizes="16x16"/>`,
		`<link rel="icon" href="/icon.png\?[0-9a-f]{16}" type="image/png" sizes="32x32"/>`,
	} {
		if !regexp.MustCompile(re).MatchString(home) {
			t.Errorf("/ lacks %s", re)
		}
	}
	if strings.Contains(home, "og:image") {
		t.Errorf("/ has /about's og:image")
	}
	_, about := get("/about")
	for _, re := range []string{
		`<meta property="og:image" content="http://localhost:\d+/about/opengraph-image.png\?[0-9a-f]{16}"/>`,
		`<meta property="og:image:width" content="120"/>`,
		`<meta property="og:image:alt" content="About Oganesson"/>`,
	} {
		if !regexp.MustCompile(re).MatchString(about) {
			t.Errorf("/about lacks %s", re)
		}
	}
	for p, want := range map[string][2]string{
		"/favicon.ico":               {"image/x-icon", "\x00\x00\x01\x00"},
		"/icon.png":                  {"image/png", "\x89PNG"},
		"/about/opengraph-image.png": {"image/png", "\x89PNG"},
		"/robots.txt":                {"text/plain", "User-Agent: *\nAllow: /\nDisallow: /api/\n\nSitemap: https://example.com/sitemap.xml\n"},
		"/sitemap.xml":               {"application/xml", "<loc>https://example.com/about</loc>"},
	} {
		resp, body := get(p)
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("content-type"), want[0]) || !strings.Contains(body, want[1]) {
			t.Errorf("GET %s: %d %q %q", p, resp.StatusCode, resp.Header.Get("content-type"), body)
		}
		if cc := resp.Header.Get("cache-control"); cc != cacheRevalidate {
			t.Errorf("GET %s: cache-control %q", p, cc)
		}
	}
}

// TestImageSizeByContent: metadata images are measured by their content,
// as image-size does: a PNG named icon.ico (create-next-app's examples
// ship such files) is a PNG, a real ICO its first image.
func TestImageSizeByContent(t *testing.T) {
	png, err := os.ReadFile(filepath.Join(appDir("next-hello"), "app", "icon.png"))
	if err != nil {
		t.Fatal(err)
	}
	ico, err := os.ReadFile(filepath.Join(appDir("next-hello"), "app", "favicon.ico"))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		b    []byte
		ext  string
		w, h int
	}{
		{"png as ico", png, "ico", 32, 32},
		{"png", png, "png", 32, 32},
		{"ico", ico, "ico", 16, 16},
	} {
		w, h, err := imageSize(c.b, c.ext)
		if err != nil || w != c.w || h != c.h {
			t.Errorf("%s: %dx%d %v, want %dx%d", c.name, w, h, err, c.w, c.h)
		}
	}
}
