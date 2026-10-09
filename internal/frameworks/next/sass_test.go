package next

import (
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

// TestSass covers Sass in the fixture's /gallery: a .module.scss that @uses
// a partial from another directory (whose url() resolve-url-loader rebases),
// sass:color, nesting, and a global indented-syntax .sass file.
func TestSass(t *testing.T) {
	srv := serveFixture(t)
	get := func(p string) string {
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("GET %s: %d", p, resp.StatusCode)
		}
		return string(b)
	}
	body := get("/gallery")
	m := regexp.MustCompile(`<figure id="sass" class="(gallery_[0-9a-f]{6}_figure)"><span class="(gallery_[0-9a-f]{6}_inner)">`).FindStringSubmatch(body)
	if m == nil {
		t.Fatalf("Sass module classes not applied:\n%s", regexp.MustCompile(`<figure id="sass".*?</figure>`).FindString(body))
	}
	var css string
	for _, l := range regexp.MustCompile(`<link rel="stylesheet" href="(/_next/static/css/[^"]+)"`).FindAllStringSubmatch(body, -1) {
		css += get(l[1])
	}
	media := regexp.MustCompile(`url\("?(/_next/static/media/stripe\.[^")]+\.svg)"?\)`).FindStringSubmatch(css)
	if media == nil {
		t.Fatalf("the partial's url() was not rebased and emitted:\n%s", css)
	}
	if svg := get(media[1]); !strings.Contains(svg, "<svg") {
		t.Errorf("GET %s: %q", media[1], svg)
	}
	for _, w := range []string{
		"." + m[1] + "{border:2px solid #7928ca;padding:4px;",
		"color:#60209f}", // color.adjust($accent, $lightness: -10%)
		"." + m[1] + " ." + m[2] + "{font-weight:700}",
		".sass-note{padding:6px;font-style:italic}",
	} {
		if !strings.Contains(css, w) {
			t.Errorf("stylesheets lack %s", w)
		}
	}
}

// TestComposes: a CSS Module composing a class from another CSS Module gets
// that module's real (hashed) class name, as css-loader resolves it.
func TestComposes(t *testing.T) {
	srv := serveFixture(t)
	resp, err := http.Get(srv.URL + "/gallery")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	body := string(b)
	pill := regexp.MustCompile(`<span id="pill" class="([^"]+)">`).FindStringSubmatch(body)
	badge := regexp.MustCompile(`<span id="badge" class="([^"]+)">`).FindStringSubmatch(body)
	if pill == nil || badge == nil {
		t.Fatal("spans not rendered")
	}
	classes := strings.Fields(badge[1])
	if len(classes) != 3 || !slicesContains(classes, pill[1]) || !strings.Contains(badge[1], "_base") || !strings.Contains(badge[1], "_badge") {
		t.Errorf("badge classes %q should compose base and the shared module's %q", badge[1], pill[1])
	}
}

func slicesContains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
