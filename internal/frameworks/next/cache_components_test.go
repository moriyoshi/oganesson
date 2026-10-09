package next

import (
	"context"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/moriyoshi/oganesson/runtime/app"
)

// TestCacheComponents: "use cache" under cacheComponents
// (testdata/apps/next-cache). A cached function's result is shared by
// every request (each a fresh engine: the store is og's, runtime/isr);
// a page's dynamic part (cookies, in Suspense) renders per request after
// its cached static shell (partial prerendering);
// revalidateTag from a server action gives a tagged function a new value;
// a page without dynamic data is prerendered; next.config's cacheHandlers
// serve their "use cache: <kind>" functions; "use cache: private" reads
// the request's cookies and is not shared between requests; a cacheLife
// profile from next.config sets an entry's lifetime; a redirecting server
// action returns its target's payload.
func TestCacheComponents(t *testing.T) {
	dir := appDir("next-cache")
	if _, err := os.Stat(filepath.Join(dir, "node_modules", "next")); err != nil {
		t.Skip("fixture not installed (go run ./cmd/og install testdata/apps/next-cache)")
	}
	out, err := Build(context.Background(), dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	fsys, err := packageFixture(out)
	if err != nil {
		t.Fatal(err)
	}
	// The server's own origin, as app.Serve sets it once it listens; the
	// environment is read when the handler is made.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(app.PrivateOriginEnv, "http://"+ln.Addr().String())
	h, _, err := app.Handler(fsys, slog.New(slog.NewTextHandler(testWriter{t}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := &httptest.Server{Listener: ln, Config: &http.Server{Handler: h}}
	srv.Start()
	defer srv.Close()
	get := func(path, cookie string) (string, http.Header) {
		t.Helper()
		req, _ := http.NewRequest(http.MethodGet, srv.URL+path, nil)
		if cookie != "" {
			req.Header.Set("Cookie", cookie)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("GET %s: %d\n%.800s", path, resp.StatusCode, b)
		}
		return string(b), resp.Header
	}
	find := func(re, body string) string {
		t.Helper()
		m := regexp.MustCompile(re).FindStringSubmatch(body)
		if m == nil {
			t.Fatalf("no %s in\n%.1500s", re, body)
		}
		return m[1]
	}

	home1, _ := get("/", "")
	home2, _ := get("/", "name=bob")
	s1, s2 := find(`<p id="stamp">([^<]*)</p>`, home1), find(`<p id="stamp">([^<]*)</p>`, home2)
	if s1 != s2 || !strings.Contains(s1, "(call 1)") {
		t.Errorf("the cached stamp differs between requests: %q / %q", s1, s2)
	}
	if !strings.Contains(home2, "bob") {
		t.Errorf("the dynamic part lacks the request's cookie:\n%.1500s", home2)
	}
	// Partial prerendering: the build's static shell (the Suspense
	// fallback) from the cache, then the resumed dynamic part streamed
	// after it in the same document, not cacheable.
	_, homeHdr := get("/", "name=carol")
	if homeHdr.Get("x-nextjs-cache") != "HIT" || homeHdr.Get("x-nextjs-postponed") != "1" || !strings.HasPrefix(homeHdr.Get("Cache-Control"), "private, no-cache") {
		t.Errorf("/: x-nextjs-cache %q, x-nextjs-postponed %q, cache-control %q, want the cached shell, resumed", homeHdr.Get("x-nextjs-cache"), homeHdr.Get("x-nextjs-postponed"), homeHdr.Get("Cache-Control"))
	}
	if !strings.Contains(home2, `<p id="visitor">loading</p>`) || strings.Count(home2, "</html>") != 1 || strings.Index(home2, "loading") > strings.Index(home2, "bob") {
		t.Errorf("/ is not the shell followed by its resumed part:\n%.3000s", home2)
	}
	// Client navigations render the page (the shell's RSC payload is partial).
	rscReq, _ := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	rscReq.Header.Set("rsc", "1")
	rscReq.Header.Set("Cookie", "name=dave")
	rscResp, err := http.DefaultClient.Do(rscReq)
	if err != nil {
		t.Fatal(err)
	}
	rscBody, _ := io.ReadAll(rscResp.Body)
	rscResp.Body.Close()
	if !strings.Contains(string(rscBody), "dave") {
		t.Errorf("the RSC payload of / lacks the request's cookie:\n%.1500s", rscBody)
	}

	tagged, _ := get("/tagged", "")
	t1 := find(`<p id="tagged">([^<]*)</p>`, tagged)
	if again, _ := get("/tagged", ""); find(`<p id="tagged">([^<]*)</p>`, again) != t1 {
		t.Errorf("the tagged value changed without revalidation")
	}
	// The form's server action calls revalidateTag("og-tagged", "max"):
	// the next render computes the function again.
	field := find(`name="(\$ACTION_ID_[0-9a-f]+)"`, tagged)
	var body strings.Builder
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField(field, "")
	_ = mw.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/tagged", strings.NewReader(body.String()))
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Origin", srv.URL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if after, _ := get("/tagged", ""); find(`<p id="tagged">([^<]*)</p>`, after) == t1 {
		t.Errorf("revalidateTag left the tagged value as it was (%s)", t1)
	}

	// The custom "kv" handler (lib/kv-cache-handler.ts, over og:kv): the
	// first request sets the entry, the second gets it.
	k1 := find(`<p id="kv-stamp">(kv [^<]*)</p>`, func() string { b, _ := get("/custom", ""); return b }())
	k2 := find(`<p id="kv-stamp">(kv [^<]*)</p>`, func() string { b, _ := get("/custom", ""); return b }())
	if k1 != k2 {
		t.Errorf("the kv-cached stamp differs between requests: %q / %q", k1, k2)
	}
	if stats, _ := get("/custom/stats", ""); stats != `{"get":2,"set":1}` {
		t.Errorf("the custom handler's counters: %s, want 2 gets and 1 set", stats)
	}

	for _, name := range []string{"alice", "bob"} {
		page, _ := get("/private", "name="+name)
		if got := find(`<p id="greeting">(hello [^<]*)</p>`, page); got != "hello "+name {
			t.Errorf("/private for %s: %q", name, got)
		}
	}

	// next.config's "brief" profile revalidates after a second.
	brief := func() string {
		b, _ := get("/brief", "")
		return find(`<p id="brief-stamp">(brief [^<]*)</p>`, b)
	}
	b1, b2 := brief(), brief()
	if b1 != b2 {
		t.Errorf("the brief stamp differs within its lifetime: %q / %q", b1, b2)
	}
	time.Sleep(1500 * time.Millisecond)
	if b3 := brief(); b3 == b1 {
		t.Errorf("the brief stamp outlived its profile's revalidate (1s): %q", b3)
	}

	// A redirecting action called as the client router calls it: the
	// response carries the target's RSC payload, which Next fetched from
	// the server's own origin.
	goPage, _ := get("/go", "")
	actionID := strings.TrimPrefix(find(`name="(\$ACTION_ID_[0-9a-f]+)"`, goPage), "$ACTION_ID_")
	actReq, _ := http.NewRequest(http.MethodPost, srv.URL+"/go", strings.NewReader("[]"))
	actReq.Header.Set("Next-Action", actionID)
	actReq.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	actReq.Header.Set("Origin", srv.URL)
	actResp, err := http.DefaultClient.Do(actReq)
	if err != nil {
		t.Fatal(err)
	}
	actBody, _ := io.ReadAll(actResp.Body)
	actResp.Body.Close()
	if !strings.HasPrefix(actResp.Header.Get("x-action-redirect"), "/static") || !strings.HasPrefix(actResp.Header.Get("Content-Type"), "text/x-component") || !strings.Contains(string(actBody), "static computed at") {
		t.Errorf("the redirecting action: %d, x-action-redirect %q, content-type %q, want /static's RSC payload\n%.800s", actResp.StatusCode, actResp.Header.Get("x-action-redirect"), actResp.Header.Get("Content-Type"), actBody)
	}

	static, hdr := get("/static", "")
	if hdr.Get("x-nextjs-cache") != "HIT" || !strings.Contains(static, "static computed at") {
		t.Errorf("/static: x-nextjs-cache %q, want the prerendered page\n%.800s", hdr.Get("x-nextjs-cache"), static)
	}
}

// TestCacheHandlersSetup: next.config cacheHandlers become imports and
// registrations in kind order; a file: URL is a path, and a relative
// path is the project's; an empty entry is skipped.
func TestCacheHandlersSetup(t *testing.T) {
	b := &builder{dir: "/proj", config: map[string]any{"cacheHandlers": map[string]any{
		"remote": "file:///abs/remote.js", "default": "./h/default.js", "unset": "",
	}}}
	got := b.cacheHandlersSetup()
	want := map[string]string{
		"cacheHandlerImports":           "import edgeCacheHandler_0 from \"/proj/h/default.js\"\nimport edgeCacheHandler_1 from \"/abs/remote.js\"",
		"cacheHandlerRegistration":      "  cacheHandlers.setCacheHandler(\"default\", edgeCacheHandler_0)\n  cacheHandlers.setCacheHandler(\"remote\", edgeCacheHandler_1)",
		"edgeCacheHandlersRegistration": "edgeCacheHandlers[\"default\"] = edgeCacheHandler_0\nedgeCacheHandlers[\"remote\"] = edgeCacheHandler_1",
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s:\n%s\nwant\n%s", k, got[k], w)
		}
	}
}
