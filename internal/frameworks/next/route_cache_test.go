package next

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/moriyoshi/oganesson/runtime/app"
	"github.com/moriyoshi/oganesson/runtime/isr"
)

// TestRouteHandlerCaching covers route handler prerendering and caching
// (Next's exportAppRoute rules): routes that opt into static generation are
// prerendered and served from the cache; plain GET handlers stay dynamic
// (Next 15+).
func TestRouteHandlerCaching(t *testing.T) {
	srv := serveFixture(t)
	get := func(p string) (*http.Response, string) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+p, nil)
		req.Header.Set("User-Agent", "og-test")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}

	r1, b1 := get("/api/build-time")
	r2, b2 := get("/api/build-time")
	if r1.StatusCode != 200 || b1 != b2 || r1.Header.Get("x-nextjs-cache") != "HIT" || r2.Header.Get("x-nextjs-cache") != "HIT" {
		t.Errorf("force-static route not served from the build: %q %q / %q %q", r1.Header.Get("x-nextjs-cache"), b1, r2.Header.Get("x-nextjs-cache"), b2)
	}
	if r1.Header.Get("x-route") != "build-time" || r1.Header.Get("Content-Type") != "application/json" || r1.Header.Get("Cache-Control") != "s-maxage=31536000" {
		t.Errorf("force-static route headers: %v", r1.Header)
	}
	if want := `"ua":null`; !strings.Contains(b1, want) {
		t.Errorf("force-static request headers should be empty, got %s", b1)
	}

	r3, b3 := get("/api/hourly")
	_, b4 := get("/api/hourly")
	// Served from the cache: HIT, or STALE once revalidate has passed (the
	// prerender is shared by the whole suite); the body only stays put on HITs.
	c3 := r3.Header.Get("x-nextjs-cache")
	if (c3 != "HIT" && c3 != "STALE") || (c3 == "HIT" && b3 != b4) || r3.Header.Get("Cache-Control") != "public, max-age=30" || r3.Header.Get("Content-Type") != "text/plain" {
		t.Errorf("revalidate route: %q %q %v / %q", c3, b3, r3.Header, b4)
	}

	// The build's prerender recorded each route's revalidate (as Next's
	// exportAppRoute reads it from the route's static generation: 16.4's
	// prerender(), 16.3's handle() without dynamic response support).
	var pre struct {
		Entries map[string]*isr.Entry `json:"entries"`
	}
	if err := json.Unmarshal(fixtureFS[app.PrerenderFile].Data, &pre); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]int64{"/api/hourly": 3600, "/api/build-time": -1} {
		found := false
		for k, e := range pre.Entries {
			if k == path || strings.HasPrefix(k, path+"\n") || strings.HasPrefix(k, path+"?") {
				found = true
				if e.Revalidate != want {
					t.Errorf("%s prerendered with revalidate %d, want %d", path, e.Revalidate, want)
				}
			}
		}
		if !found {
			t.Errorf("%s not prerendered", path)
		}
	}

	for _, p := range []string{"/api/hello?name=a", "/api/posts/rsc"} {
		r, _ := get(p)
		if c := r.Header.Get("x-nextjs-cache"); c != "" {
			t.Errorf("%s: dynamic route served with x-nextjs-cache %q", p, c)
		}
	}
	if r, _ := get("/robots.txt"); r.Header.Get("x-nextjs-cache") != "HIT" {
		t.Errorf("/robots.txt: metadata route not prerendered (%q)", r.Header.Get("x-nextjs-cache"))
	}
}

// TestNotFoundCachedOnce: URLs no route matches are all answered from the
// not-found page's single cache entry, as Next serves them from
// /_not-found; arbitrary URLs (and queries) do not each add an entry.
func TestNotFoundCachedOnce(t *testing.T) {
	srv := serveFixture(t)
	get := func(p string) (int, string, string) {
		t.Helper()
		resp, err := http.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, resp.Header.Get("x-nextjs-cache"), string(b)
	}
	code, first, body := get("/og-unknown-1")
	if code != 404 || !strings.Contains(body, "404") {
		t.Fatalf("GET /og-unknown-1: %d %q", code, first)
	}
	for _, p := range []string{"/og-unknown-2", "/og-unknown-3?q=1", "/og/unknown/4"} {
		if code, cache, _ := get(p); code != 404 || cache != "HIT" {
			t.Errorf("GET %s: %d, x-nextjs-cache %q, want 404 from the shared entry (HIT)", p, code, cache)
		}
	}
	if code, _, _ := get("/about"); code != 200 {
		t.Errorf("GET /about: %d", code)
	}
}

// TestStaticPageQueryIndependent: a statically rendered App Router page's
// output does not depend on the query (using searchParams makes a page
// dynamic), and Next caches it by path: any query (HTML or RSC request) is
// served from the prerendered entry instead of a new entry per query.
func TestStaticPageQueryIndependent(t *testing.T) {
	srv := serveFixture(t)
	for _, c := range []struct {
		path string
		rsc  bool
		want string
	}{
		{"/about?x=1", false, "HIT"}, {"/about?x=2&y=3", false, "HIT"}, {"/about?x=4", true, "HIT"},
		// Rendered on its first request (a param generateStaticParams did not
		// list), with a query: its HTML and RSC variants are cached by path.
		{"/tags/og-query-test?x=1", false, "MISS"}, {"/tags/og-query-test?x=2", false, "HIT"}, {"/tags/og-query-test?x=3", true, "HIT"},
	} {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+c.path, nil)
		if c.rsc {
			req.Header.Set("rsc", "1")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 || resp.Header.Get("x-nextjs-cache") != c.want {
			t.Errorf("GET %s (rsc %t): %d, x-nextjs-cache %q, want %s", c.path, c.rsc, resp.StatusCode, resp.Header.Get("x-nextjs-cache"), c.want)
		}
	}
}
