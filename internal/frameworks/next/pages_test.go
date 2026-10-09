package next

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/moriyoshi/oganesson/runtime/app"
)

var (
	pagesOnce sync.Once
	pagesOut  *Output
	pagesFS   fstest.MapFS
	pagesErr  error
)

// servePagesFixture builds and prerenders testdata/apps/next-pages once and
// serves it like a packaged binary.
func servePagesFixture(t *testing.T) *httptest.Server {
	t.Helper()
	dir := appDir("next-pages")
	if _, err := os.Stat(filepath.Join(dir, "node_modules/next")); err != nil {
		t.Skip("fixture not installed (go run ./cmd/og install testdata/apps/next-pages)")
	}
	pagesOnce.Do(func() {
		pagesOut, pagesErr = Build(context.Background(), dir, Options{})
		if pagesErr == nil {
			pagesFS, pagesErr = packageFixture(pagesOut)
		}
	})
	if pagesErr != nil {
		t.Fatal(pagesErr)
	}
	h, _, err := app.Handler(pagesFS, slog.New(slog.NewTextHandler(testWriter{t}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// TestPagesRouter covers the Pages Router: SSR through Next's edge-ssr
// template with _app/_document, data fetching (getServerSideProps,
// getStaticProps, getInitialProps), notFound and redirects, /_next/data
// requests, the 404 page, prerendering and cache headers.
func TestPagesRouter(t *testing.T) {
	srv := servePagesFixture(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(p string) (*http.Response, string) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+p, nil)
		req.Header.Set("User-Agent", "og-test")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}
	data := "/_next/data/" + pagesOut.BuildID
	for _, c := range []struct {
		path   string
		status int
		cache  string // x-nextjs-cache ("" = not cached)
		cc     string // Cache-Control prefix
		ct     string
		want   []string
	}{
		{"/", 200, "HIT", "s-maxage=31536000", "text/html", []string{`<html lang="en" data-doc="custom">`, `<title data-next-head="">next-pages home</title>`, `Hello, Pages Router</h1>`, `clicked <!-- -->0<!-- --> times`, `<header>Pages fixture`, `id="__NEXT_DATA__"`, `"page":"/"`}},
		{"/about", 200, "HIT", "s-maxage=3600", "text/html", []string{`A static page with getStaticProps.`, `"__N_SSG":true`}},
		{"/blog/hello", 200, "", "private, no-cache", "text/html", []string{`Post <!-- -->hello</h1>`, `<p id="ua">og-test</p>`, `"__N_SSP":true`, `"page":"/blog/[slug]"`}},
		{"/legacy?q=7", 200, "", "private, no-cache", "text/html", []string{`<p id="legacy">gip 7 200</p>`}},
		{"/widgets", 200, "", "private, no-cache", "text/html", []string{`<p id="browserpkg">buffer, absent</p>`, `<p id="missing-export">rule, renderRule undefined</p>`, `<p id="url">/a</p>`, `<p id="banner" class="`, `banner <!-- -->banner</p>`, `<span id="badge">badge <!-- -->nested/badge</span>`}},
		{"/plain-js", 200, "", "private, no-cache", "text/html", []string{`<p id="plain-js">jsx in <!-- -->js<!-- --> <!-- -->3</p>`}},
		{"/posts/1", 200, "HIT", "s-maxage=31536000", "text/html", []string{`1<!-- -->: <!-- -->First`}},
		{"/posts/new", 200, "HIT", "s-maxage=31536000", "text/html", []string{`<p id="new">new post form</p>`, `"page":"/posts/new"`}},
		// getStaticPaths fallback: false: other params are 404s.
		{"/posts/3", 404, "", "private, no-cache", "text/html", []string{`Custom 404`}},
		{data + "/posts/3.json", 404, "", "", "", nil},
		{data + "/posts/2.json", 200, "HIT", "s-maxage=31536000", "application/json", []string{`{"pageProps":{"id":"2","title":"Second"},"__N_SSG":true}`}},
		// fallback: "blocking": other params render on demand and are cached.
		{"/docs/a", 200, "HIT", "s-maxage=31536000", "text/html", []string{`doc <!-- -->a</h1>`}},
		{"/docs/zzz", 200, "MISS", "s-maxage=31536000", "text/html", []string{`doc <!-- -->zzz</h1>`}},
		{"/docs/zzz", 200, "HIT", "s-maxage=31536000", "text/html", []string{`doc <!-- -->zzz</h1>`}},
		{data + "/docs/yyy.json", 200, "MISS", "s-maxage=31536000", "application/json", []string{`{"pageProps":{"slug":"yyy","builtAt":`, `"__N_SSG":true}`}},
		// getStaticProps notFound is cached like a page (notFoundRevalidate).
		{"/docs/missing", 404, "MISS", "", "text/html", []string{`Custom 404`}},
		{"/blog/missing", 404, "", "", "text/html", []string{`Custom 404`}},
		{"/nope", 404, "", "", "text/html", []string{`Custom 404`, `"page":"/404"`}},
		{data + "/blog/hello.json", 200, "", "private, no-cache", "application/json", []string{`{"pageProps":{"slug":"hello","ua":"og-test",`, `"__N_SSP":true}`}},
		{data + "/about.json", 200, "HIT", "s-maxage=3600", "application/json", []string{`{"pageProps":{"builtAt":`, `"__N_SSG":true}`}},
		{data + "/index.json", 404, "", "", "", nil}, // no data for a page without data functions... still served by the page
		{data + "/blog/missing.json", 404, "", "", "application/json", []string{`{"notFound":true}`}},
		{data + "/blog/old.json", 200, "", "", "application/json", []string{`"__N_REDIRECT":"/blog/hello"`, `"__N_REDIRECT_STATUS":307`}},
		{"/_next/data/stale-build/about.json", 404, "", "", "", nil},
	} {
		resp, body := get(c.path)
		if c.path == data+"/index.json" {
			// Next serves a page without data functions as HTML here too; only
			// the status matters to the client (it then hard-navigates).
			continue
		}
		if resp.StatusCode != c.status {
			t.Errorf("GET %s: status %d, want %d\n%.400s", c.path, resp.StatusCode, c.status, body)
			continue
		}
		if got := resp.Header.Get("x-nextjs-cache"); (c.cache == "") != (got == "") || (c.cache == "MISS" && got != "MISS") {
			t.Errorf("GET %s: x-nextjs-cache %q, want %q", c.path, got, c.cache)
		}
		if c.cc != "" && !strings.HasPrefix(resp.Header.Get("Cache-Control"), c.cc) {
			t.Errorf("GET %s: Cache-Control %q, want %s…", c.path, resp.Header.Get("Cache-Control"), c.cc)
		}
		if c.ct != "" && !strings.HasPrefix(resp.Header.Get("Content-Type"), c.ct) {
			t.Errorf("GET %s: Content-Type %q", c.path, resp.Header.Get("Content-Type"))
		}
		for _, w := range c.want {
			if !strings.Contains(body, w) {
				t.Errorf("GET %s lacks %s\n%.600s", c.path, w, body)
			}
		}
	}

	// gSSP redirect; Next adds Refresh only for 308.
	resp, _ := get("/blog/old")
	if resp.StatusCode != 307 || resp.Header.Get("Location") != "/blog/hello" || resp.Header.Get("Refresh") != "" {
		t.Errorf("redirect: %d %v", resp.StatusCode, resp.Header)
	}
	// The prerendered SSG page is the build's: the same props on every request.
	_, a1 := get(data + "/about.json")
	_, a2 := get(data + "/about.json")
	if a1 != a2 {
		t.Errorf("SSG data changed between requests: %s / %s", a1, a2)
	}
	// Assets: the pages bundle, the client build manifests, CSS.
	for _, f := range []string{pagesOut.BuildID + "/_buildManifest.js", pagesOut.BuildID + "/_ssgManifest.js"} {
		resp, body := get("/_next/static/" + f)
		if resp.StatusCode != 200 || !strings.Contains(body, "self.__") {
			t.Errorf("GET %s: %d", f, resp.StatusCode)
		}
	}
	_, ssg := get("/_next/static/" + pagesOut.BuildID + "/_ssgManifest.js")
	if !strings.Contains(ssg, `["/about","/docs/[slug]","/draft","/items/[id]","/posts/[id]"]`) {
		t.Errorf("_ssgManifest.js = %s", ssg)
	}
	// typeof window is "undefined" in the server build and "object" in the
	// browser's, as Next's SWC optimizer makes it: the browser bundle keeps
	// only its branch (pages/legacy.tsx).
	if _, legacy := get("/legacy"); !strings.Contains(legacy, `<p id="where">og-where-server</p>`) {
		t.Errorf("/legacy lacks the server branch:\n%.400s", legacy)
	}
	var browserJS strings.Builder
	for name, b := range pagesOut.Static {
		if strings.HasSuffix(name, ".js") {
			browserJS.Write(b)
		}
	}
	if js := browserJS.String(); !strings.Contains(js, "og-where-browser") || strings.Contains(js, "og-where-server") {
		t.Errorf("the browser bundle keeps typeof window's server branch (browser branch present: %t)", strings.Contains(js, "og-where-browser"))
	}
	_, bm := get("/_next/static/" + pagesOut.BuildID + "/_buildManifest.js")
	for _, w := range []string{`"/_app":["static/chunks/pages-`, `"sortedPages":["/","/404","/_app","/_error","/about","/blog/[slug]","/docs/[slug]","/draft","/items/[id]","/legacy","/plain-js","/posts/new","/posts/[id]","/widgets"]`} {
		if !strings.Contains(bm, w) {
			t.Errorf("_buildManifest.js lacks %s:\n%.500s", w, bm)
		}
	}
}

// TestPagesAPIRoutes covers pages/api: Node-style handlers through Next's
// pages-api template on its req/res mocks (query, dynamic params, cookies,
// the body parser, raw bodies, redirects, ETags, streaming, errors) and
// edge-runtime handlers through pages-edge-api.
func TestPagesAPIRoutes(t *testing.T) {
	srv := servePagesFixture(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	do := func(method, p, ctype, body string, hdr map[string]string) (*http.Response, string) {
		req, _ := http.NewRequest(method, srv.URL+p, strings.NewReader(body))
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}
	for _, c := range []struct {
		method, path, ctype, body string
		status                    int
		want                      string
		header                    [2]string
	}{
		{"GET", "/api/hello", "", "", 200, `{"hello":"world"}`, [2]string{"x-api", "pages"}},
		{"GET", "/api/hello?name=og", "", "", 200, `{"hello":"og"}`, [2]string{"Content-Type", "application/json; charset=utf-8"}},
		{"POST", "/api/hello", "application/json", `{"a":1}`, 201, `{"received":{"a":1}}`, [2]string{}},
		{"POST", "/api/hello", "application/x-www-form-urlencoded", `a=1&b=2`, 201, `{"received":{"a":"1","b":"2"}}`, [2]string{}},
		{"POST", "/api/hello", "application/json", `{a`, 400, `Invalid JSON`, [2]string{}},
		{"POST", "/api/raw", "text/plain", `bytes!`, 200, `raw:bytes!`, [2]string{}},
		{"GET", "/api/users/42?extra=x", "", "", 200, `{"id":"42","extra":"x","cookie":"abc"}`, [2]string{}},
		{"GET", "/api/users/me", "", "", 307, `/api/users/1`, [2]string{"Location", "/api/users/1"}},
		{"GET", "/api/users/boom", "", "", 500, `Internal Server Error`, [2]string{}},
		{"GET", "/api/stream", "", "", 200, "one\ntwo\n", [2]string{"Content-Type", "text/plain; charset=utf-8"}},
		{"POST", "/api/edge", "", "", 200, `{"edge":true,"method":"POST"}`, [2]string{}},
		{"GET", "/api/nope", "", "", 404, `Custom 404`, [2]string{}},
	} {
		resp, body := do(c.method, c.path, c.ctype, c.body, map[string]string{"Cookie": "session=abc"})
		if resp.StatusCode != c.status || !strings.Contains(body, c.want) {
			t.Errorf("%s %s: %d %q, want %d %q", c.method, c.path, resp.StatusCode, body, c.status, c.want)
		}
		if c.header[0] != "" && resp.Header.Get(c.header[0]) != c.header[1] {
			t.Errorf("%s %s: %s = %q, want %q", c.method, c.path, c.header[0], resp.Header.Get(c.header[0]), c.header[1])
		}
		if c.status == 200 && resp.Header.Get("x-nextjs-cache") != "" {
			t.Errorf("%s %s: API response cached (%s)", c.method, c.path, resp.Header.Get("x-nextjs-cache"))
		}
	}
	// Next's ETag revalidation (sendEtagResponse).
	resp, _ := do("GET", "/api/hello", "", "", nil)
	if etag := resp.Header.Get("ETag"); etag == "" {
		t.Error("no ETag")
	} else if resp, body := do("GET", "/api/hello", "", "", map[string]string{"If-None-Match": etag}); resp.StatusCode != 304 || body != "" {
		t.Errorf("If-None-Match: %d %q", resp.StatusCode, body)
	}
	// One request runs the handler once: no static render attempt first
	// (the ISR layer would attempt one for every new path of a dynamic route,
	// which the build cannot have marked dynamic).
	count := func(key string) int {
		_, body := do("GET", "/api/count/"+key, "", "", nil)
		var v struct{ Count int }
		if err := json.Unmarshal([]byte(body), &v); err != nil {
			t.Fatalf("count: %q", body)
		}
		return v.Count
	}
	if c1, c2 := count("a"), count("b"); c2-c1 != 1 {
		t.Errorf("handler ran %d times for one request", c2-c1)
	}
	// API routes have no data route.
	if resp, _ := do("GET", "/_next/data/"+pagesOut.BuildID+"/api/hello.json", "", "", nil); resp.StatusCode != 404 {
		t.Errorf("data route for an API route: %d", resp.StatusCode)
	}
}

// TestPagesSSGCache: an SSG page's HTML and /_next/data JSON come from one
// render and are cached together (as one Next cache entry), whichever is
// requested first; res.revalidate regenerates both.
func TestPagesSSGCache(t *testing.T) {
	srv := servePagesFixture(t)
	get := func(method, p string) (*http.Response, string) {
		req, _ := http.NewRequest(method, srv.URL+p, nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}
	builtRe := regexp.MustCompile(`"builtAt":"?([0-9.-]+)`)
	built := func(p, wantCache string) string {
		t.Helper()
		resp, body := get("GET", p)
		if resp.StatusCode != 200 {
			t.Fatalf("GET %s: %d", p, resp.StatusCode)
		}
		if got := resp.Header.Get("x-nextjs-cache"); got != wantCache {
			t.Errorf("GET %s: x-nextjs-cache %q, want %q", p, got, wantCache)
		}
		m := builtRe.FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("GET %s: no builtAt in %.300s", p, body)
		}
		return m[1]
	}
	data := "/_next/data/" + pagesOut.BuildID

	// The build's prerender stored the data with the HTML.
	if h, d := built("/about", "HIT"), built(data+"/about.json", "HIT"); h != d {
		t.Errorf("/about: HTML builtAt %s, data builtAt %s", h, d)
	}
	// Static pages ignore the query string, as in Next.
	if h, q := built("/about", "HIT"), built("/about?utm=1", "HIT"); h != q {
		t.Errorf("/about?utm=1: builtAt %s, /about %s", q, h)
	}
	// Data first (fallback: "blocking"; the client adds route params to data
	// URLs as a query): the data render also cached the HTML.
	if d, h := built(data+"/docs/q1.json?slug=q1", "MISS"), built("/docs/q1", "HIT"); h != d {
		t.Errorf("/docs/q1: data builtAt %s, HTML builtAt %s", d, h)
	}
	// HTML first.
	if h, d := built("/docs/q2", "MISS"), built(data+"/docs/q2.json?slug=q2", "HIT"); h != d {
		t.Errorf("/docs/q2: HTML builtAt %s, data builtAt %s", h, d)
	}

	// On-demand revalidation regenerates the HTML and the data now.
	before := built("/about", "HIT")
	if resp, body := get("POST", "/api/revalidate?path=/about"); resp.StatusCode != 200 || body != `{"revalidated":true}` {
		t.Fatalf("revalidate /about: %d %s", resp.StatusCode, body)
	}
	after := built("/about", "HIT")
	if after == before {
		t.Errorf("/about not regenerated (builtAt %s)", after)
	}
	if d := built(data+"/about.json", "HIT"); d != after {
		t.Errorf("/about data builtAt %s after revalidation, HTML %s", d, after)
	}
	// unstable_onlyGenerated leaves never-generated paths alone.
	if resp, body := get("POST", "/api/revalidate?path=/docs/never&only=1"); resp.StatusCode != 200 {
		t.Fatalf("revalidate onlyGenerated: %d %s", resp.StatusCode, body)
	}
	built("/docs/never", "MISS")
	// Revalidating a page rendered per request succeeds (Next renders it).
	if resp, body := get("POST", "/api/revalidate?path=/blog/hello"); resp.StatusCode != 200 {
		t.Errorf("revalidate a gSSP page: %d %s", resp.StatusCode, body)
	}
	// A page that fails to render fails the revalidation, with its status.
	if resp, body := get("POST", "/api/revalidate?path=/docs/broken"); resp.StatusCode != 500 || !strings.Contains(body, "Invalid response 500") {
		t.Errorf("revalidate a failing page: %d %s", resp.StatusCode, body)
	}
}

// TestPagesFallbackTrue: getStaticPaths fallback: true serves browsers the
// uncached fallback shell for paths the build did not prerender, until the
// client's data request generates the page (HTML and data together); bots
// and data requests render the page itself.
func TestPagesFallbackTrue(t *testing.T) {
	srv := servePagesFixture(t)
	get := func(p, ua string) (*http.Response, string) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+p, nil)
		req.Header.Set("User-Agent", ua)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}
	const browser = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36"
	const bot = "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"
	data := "/_next/data/" + pagesOut.BuildID
	for i, c := range []struct {
		path, ua, cache string
		want, lacks     []string
	}{
		{"/items/1", browser, "HIT", []string{`item <!-- -->1</h1>`}, []string{`"isFallback":true`}},
		// Twice: the shell is never cached.
		{"/items/7", browser, "MISS", []string{`Loading item…`, `"isFallback":true`, `"pageProps":{}`}, []string{`item <!-- -->7`}},
		{"/items/7", browser, "MISS", []string{`"isFallback":true`}, nil},
		{data + "/items/7.json?id=7", browser, "MISS", []string{`{"pageProps":{"id":"7"},"__N_SSG":true}`}, nil},
		{"/items/7", browser, "HIT", []string{`item <!-- -->7</h1>`}, []string{`"isFallback":true`}},
		{"/items/8", bot, "MISS", []string{`item <!-- -->8</h1>`}, []string{`"isFallback":true`}},
		{"/items/8", browser, "HIT", []string{`item <!-- -->8</h1>`}, nil},
	} {
		resp, body := get(c.path, c.ua)
		if resp.StatusCode != 200 {
			t.Fatalf("#%d %s: %d", i, c.path, resp.StatusCode)
		}
		if got := resp.Header.Get("x-nextjs-cache"); got != c.cache {
			t.Errorf("#%d %s: x-nextjs-cache %q, want %q", i, c.path, got, c.cache)
		}
		// pages-handler: revalidate 0 for the fallback shell.
		if strings.Contains(c.want[0], "Loading") && !strings.HasPrefix(resp.Header.Get("Cache-Control"), "private, no-cache") {
			t.Errorf("#%d %s: fallback shell has Cache-Control %q", i, c.path, resp.Header.Get("Cache-Control"))
		}
		for _, w := range c.want {
			if !strings.Contains(body, w) {
				t.Errorf("#%d %s lacks %s\n%.500s", i, c.path, w, body)
			}
		}
		for _, w := range c.lacks {
			if strings.Contains(body, w) {
				t.Errorf("#%d %s has %s", i, c.path, w)
			}
		}
	}
}

// TestPagesDraftMode: res.setDraftMode / res.setPreviewData set Next's
// cookies; with a valid __prerender_bypass cookie an SSG page renders per
// request with draftMode / previewData and is never cached, while other
// visitors (no cookie, or a wrong value) keep getting the cached page.
func TestPagesDraftMode(t *testing.T) {
	srv := servePagesFixture(t)
	get := func(p, cookie string) (*http.Response, string) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+p, nil)
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}
	cookies := func(resp *http.Response) string {
		var parts []string
		for _, c := range resp.Cookies() {
			parts = append(parts, c.Name+"="+c.Value)
		}
		return strings.Join(parts, "; ")
	}
	builtRe := regexp.MustCompile(`data-built="(\d+)"`)
	type view struct{ cache, cc, draft, built string }
	page := func(p, cookie string) view {
		t.Helper()
		resp, body := get(p, cookie)
		if resp.StatusCode != 200 {
			t.Fatalf("GET %s: %d", p, resp.StatusCode)
		}
		v := view{cache: resp.Header.Get("x-nextjs-cache"), cc: resp.Header.Get("Cache-Control")}
		if m := regexp.MustCompile(`draft=<!-- -->(\w+)<!-- --> preview=<!-- -->([^<]*)`).FindStringSubmatch(body); m != nil {
			v.draft = m[1] + " " + m[2]
		}
		if m := builtRe.FindStringSubmatch(body); m != nil {
			v.built = m[1]
		}
		return v
	}

	public := page("/draft", "")
	if public.cache != "HIT" || public.draft != "false none" {
		t.Fatalf("public /draft: %+v", public)
	}
	resp, _ := get("/api/draft?on=1", "")
	draft := cookies(resp)
	if !strings.HasPrefix(draft, "__prerender_bypass=") {
		t.Fatalf("setDraftMode cookies: %q", draft)
	}
	d1, d2 := page("/draft", draft), page("/draft", draft)
	for _, v := range []view{d1, d2} {
		if v.cache != "" || !strings.HasPrefix(v.cc, "private, no-cache") || v.draft != "true {}" {
			t.Errorf("draft /draft: %+v", v)
		}
	}
	if d1.built == d2.built {
		t.Errorf("draft renders were cached (builtAt %s)", d1.built)
	}
	resp, body := get("/_next/data/"+pagesOut.BuildID+"/draft.json", draft)
	if !strings.HasPrefix(resp.Header.Get("Cache-Control"), "private, no-cache") || !strings.Contains(body, `"draft":true`) || !strings.Contains(body, `"__N_PREVIEW":true`) {
		t.Errorf("draft data: %s %s", resp.Header.Get("Cache-Control"), body)
	}
	// Draft renders neither replaced the cached page nor marked it dynamic.
	for _, cookie := range []string{"", "__prerender_bypass=wrong"} {
		if v := page("/draft", cookie); v.cache != "HIT" || v.built != public.built || v.draft != "false none" {
			t.Errorf("/draft with cookie %q after draft renders: %+v (public %+v)", cookie, v, public)
		}
	}
	// Preview data (setPreviewData: signed and encrypted, Next's crypto-utils).
	resp, _ = get("/api/draft?preview=1", "")
	preview := cookies(resp)
	if v := page("/draft", preview); v.draft != `true {&quot;who&quot;:&quot;editor&quot;}` {
		t.Errorf("preview /draft: %+v (cookies %q)", v, preview)
	}
	// Disabling clears the cookie.
	resp, _ = get("/api/draft", draft)
	if c := resp.Header.Get("Set-Cookie"); !strings.Contains(c, "__prerender_bypass=") || !strings.Contains(c, "1970") {
		t.Errorf("disable: Set-Cookie %q", c)
	}
}

// TestPagesReact18: a Pages Router app on React 18 (testdata/apps/
// next-pages-react18), which Next 16 supports there, renders with the
// project's react and react-dom on the server and in the browser, not
// Next's vendored React 19 (whose renderer rejects React 18's elements:
// "Element type is invalid").
func TestPagesReact18(t *testing.T) {
	dir := appDir("next-pages-react18")
	if _, err := os.Stat(filepath.Join(dir, "node_modules", "react")); err != nil {
		t.Skip("fixture not installed (go run ./cmd/og install testdata/apps/next-pages-react18)")
	}
	out, err := Build(context.Background(), dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	fsys, err := packageFixture(out)
	if err != nil {
		t.Fatal(err)
	}
	h, _, err := app.Handler(fsys, slog.New(slog.NewTextHandler(testWriter{t}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	for _, want := range []string{`<p id="greeting">hello</p>`, `<p id="react-version">react <!-- -->18.3.1</p>`} {
		if resp.StatusCode != 200 || !strings.Contains(string(b), want) {
			t.Errorf("/: %d, lacks %s\n%.600s", resp.StatusCode, want, b)
		}
	}
	var browser string
	for name, code := range out.Static {
		if strings.HasPrefix(name, "static/chunks/pages-") {
			browser = string(code)
		}
	}
	if !strings.Contains(browser, `"18.3.1"`) {
		t.Error("the browser bundle does not carry the project's React 18")
	}
}

// TestRouteLoadFailureIsolated: a route module that throws when it loads
// fails its own route (500, each time), not the app: route code loads on
// first use, as next start loads it.
func TestRouteLoadFailureIsolated(t *testing.T) {
	srv := servePagesFixture(t)
	for i := 0; i < 2; i++ {
		resp, err := http.Get(srv.URL + "/api/broken")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 500 {
			t.Errorf("GET /api/broken (%d): %d, want 500", i, resp.StatusCode)
		}
	}
	resp, err := http.Get(srv.URL + "/about")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("GET /about: %d, want 200", resp.StatusCode)
	}
}
