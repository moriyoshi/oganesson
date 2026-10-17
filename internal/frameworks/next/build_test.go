package next

import (
	"context"
	"encoding/json"
	stdhtml "html"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/moriyoshi/oganesson/runtime/app"
)

// appDir is the fixture app name (a path) under testdata/apps, or under
// $OG_TEST_APPS when set: an absolute directory with copies of the
// fixtures installed with another Next version (TESTING.md).
func appDir(name string) string {
	root := os.Getenv("OG_TEST_APPS")
	if root == "" {
		root = "../../../testdata/apps"
	}
	dir, _ := filepath.Abs(filepath.Join(root, name))
	return dir
}

// installedNext is the version of Next installed in the fixture app dir
// (OG_TEST_APPS runs the suite against other releases).
func installedNext(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "node_modules", "next", "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct{ Version string }
	if err := json.Unmarshal(b, &pkg); err != nil {
		t.Fatal(err)
	}
	return pkg.Version
}

func fixtureDir(t *testing.T) string {
	t.Helper()
	dir := appDir("next-hello")
	if _, err := os.Stat(filepath.Join(dir, "node_modules/next")); err != nil {
		t.Skip("fixture not installed (go run ./cmd/og install testdata/apps/next-hello)")
	}
	return dir
}

func TestBuildFixture(t *testing.T) {
	dir := fixtureDir(t)
	link := filepath.Join(t.TempDir(), "project")
	if err := os.Symlink(dir, link); err != nil {
		t.Skipf("symlink fixture: %v", err)
	}
	out, err := Build(context.Background(), link, Options{Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("server bundle: %d bytes, %d routes", len(out.ServerJS), len(out.Routes))
	if strings.Contains(out.ServerJS, `"entryCSSFiles": {}`) {
		t.Fatal("build through symlink emitted an empty CSS manifest")
	}
}

// serveFixture builds and prerenders the fixture once and serves it exactly
// as a packaged binary does: runtime/app over the build output. Each test
// gets a fresh handler (fresh caches, KV, databases) over the same output.
func serveFixture(t *testing.T) *httptest.Server {
	t.Helper()
	fixtureOnce.Do(func() {
		fixtureOut, fixtureErr = Build(context.Background(), fixtureDir(t), Options{})
		if fixtureErr == nil {
			fixtureFS, fixtureErr = packageFixture(fixtureOut)
		}
	})
	if fixtureErr != nil {
		t.Fatal(fixtureErr)
	}
	h, _, err := app.Handler(fixtureFS, slog.New(slog.NewTextHandler(testWriter{t}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

// packageFixture lays out out like og build's payload, prerender included.
func packageFixture(out *Output) (fstest.MapFS, error) {
	meta, err := out.Meta()
	if err != nil {
		return nil, err
	}
	fsys := fstest.MapFS{
		app.ServerFile: {Data: []byte(out.ServerJS)},
		app.MetaFile:   {Data: []byte(mustJSON(meta))},
	}
	for name, b := range out.Static {
		fsys[app.NextDir+"/"+name] = &fstest.MapFile{Data: b}
	}
	for name, b := range out.Public {
		fsys[app.PublicDir+"/"+name] = &fstest.MapFile{Data: b}
	}
	for name, b := range out.Files {
		fsys[FilesDir+"/"+name] = &fstest.MapFile{Data: b}
	}
	// Like og build: prerender static routes into the payload.
	pre, err := app.Prerender(fsys, out.PrerenderRoutes())
	if err != nil {
		return nil, err
	}
	fsys[app.PrerenderFile] = &fstest.MapFile{Data: pre.JSON}
	return fsys, nil
}

var (
	fixtureOnce sync.Once
	fixtureOut  *Output
	fixtureFS   fstest.MapFS
	fixtureErr  error
)

func TestServeFixture(t *testing.T) {
	srv := serveFixture(t)
	out := fixtureOut
	cases := []struct {
		path   string
		status int
		want   []string
	}{
		{"/", 200, []string{`<title>next-hello</title>`, `<meta name="description" content="Oganesson fixture"/>`, `<header>Oganesson fixture</header>`, `<a href="/blog/hello">Hello, Oganesson</a>`, `self.__next_f.push`}},
		{"/blog/hello", 200, []string{`>Hello, Oganesson</h1>`, `>This page was rendered by Goja.</p></article>`}},
		{"/blog/missing", 404, []string{`<meta name="robots" content="noindex"/>`}},
		{"/nope", 404, []string{`This page could not be found.`, `<header>Oganesson fixture</header>`}},
	}
	var mainScript string
	for name := range out.Static {
		if strings.HasPrefix(name, "static/chunks/main-app-") {
			mainScript = "/_next/" + name
		}
	}
	resp, err := http.Get(srv.URL + mainScript)
	if err != nil {
		t.Fatal(err)
	}
	js, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("content-type"), "javascript") || !strings.Contains(resp.Header.Get("cache-control"), "immutable") || len(js) < 1000 {
		t.Errorf("GET %s: %d %q %q (%d bytes)", mainScript, resp.StatusCode, resp.Header.Get("content-type"), resp.Header.Get("cache-control"), len(js))
	}
	cases[0].want = append(cases[0].want, `<script src="`+mainScript+`"`, `<button id="counter" class="`)

	for _, c := range cases {
		resp, err := http.Get(srv.URL + c.path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != c.status {
			t.Errorf("GET %s: status %d, want %d", c.path, resp.StatusCode, c.status)
		}
		if ct := resp.Header.Get("content-type"); !strings.HasPrefix(ct, "text/html") {
			t.Errorf("GET %s: content-type %q", c.path, ct)
		}
		for _, w := range c.want {
			if !strings.Contains(string(body), w) {
				t.Errorf("GET %s: body lacks %q\n%s", c.path, w, truncate(string(body), 2000))
			}
		}
	}
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) { w.t.Log(string(p)); return len(p), nil }

func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

func TestRouteHandlers(t *testing.T) {
	srv := serveFixture(t)
	cases := []struct {
		method, path, body string
		status             int
		want               []string
		header             [2]string
	}{
		{"GET", "/api/hello?name=Oganesson", "", 200, []string{`"hello":"Oganesson"`, `"runtime":"edge"`}, [2]string{"content-type", "application/json"}},
		{"POST", "/api/hello", `{"a":1}`, 201, []string{`{"received":{"a":1}}`}, [2]string{}},
		{"GET", "/api/posts/rsc", "", 200, []string{`"title":"Server Components"`, `"ua":"og-test"`}, [2]string{"x-post", "rsc"}},
		{"GET", "/api/posts/none", "", 404, []string{`{"error":"not found"}`}, [2]string{}},
	}
	for _, c := range cases {
		req, _ := http.NewRequest(c.method, srv.URL+c.path, strings.NewReader(c.body))
		req.Header.Set("User-Agent", "og-test")
		if c.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != c.status {
			t.Errorf("%s %s: status %d, want %d: %s", c.method, c.path, resp.StatusCode, c.status, truncate(string(body), 500))
		}
		for _, w := range c.want {
			if !strings.Contains(string(body), w) {
				t.Errorf("%s %s: body %q lacks %q", c.method, c.path, truncate(string(body), 500), w)
			}
		}
		if c.header[0] != "" && !strings.Contains(resp.Header.Get(c.header[0]), c.header[1]) {
			t.Errorf("%s %s: header %s = %q", c.method, c.path, c.header[0], resp.Header.Get(c.header[0]))
		}
	}
}

var actionFieldRe = regexp.MustCompile(`name="(\$ACTION_ID_[0-9a-f]{42})"`)

func TestServerActions(t *testing.T) {
	srv := serveFixture(t)
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	html, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	m := actionFieldRe.FindSubmatch(html)
	if m == nil {
		t.Fatalf("no server action form field in:\n%s", truncate(string(html), 2000))
	}
	field := string(m[1])
	actionID := strings.TrimPrefix(field, "$ACTION_ID_")

	t.Run("form without JavaScript", func(t *testing.T) {
		var body strings.Builder
		mw := multipart.NewWriter(&body)
		_ = mw.WriteField(field, "")
		_ = mw.Close()
		req, _ := http.NewRequest("POST", srv.URL+"/", strings.NewReader(body.String()))
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Cookie", "likes=5")
		req.Header.Set("Origin", srv.URL)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Set-Cookie"), "likes=6") || !strings.Contains(string(b), `id="likes">6<`) {
			t.Errorf("status %d, set-cookie %q, body:\n%s", resp.StatusCode, resp.Header.Get("Set-Cookie"), truncate(string(b), 1500))
		}
	})

	t.Run("RPC from the client router", func(t *testing.T) {
		// Call the form's action the way callServer does: Next-Action header,
		// encodeReply body, RSC response with the action's cookie mutation.
		req, _ := http.NewRequest("POST", srv.URL+"/", strings.NewReader(`[]`))
		req.Header.Set("Next-Action", actionID)
		req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
		req.Header.Set("Accept", "text/x-component")
		req.Header.Set("Cookie", "likes=1")
		req.Header.Set("Origin", srv.URL)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/x-component") || !strings.Contains(resp.Header.Get("Set-Cookie"), "likes=2") {
			t.Errorf("status %d, content-type %q, set-cookie %q, body:\n%s", resp.StatusCode, resp.Header.Get("Content-Type"), resp.Header.Get("Set-Cookie"), truncate(string(b), 1500))
		}
	})

	t.Run("unknown action", func(t *testing.T) {
		req, _ := http.NewRequest("POST", srv.URL+"/", strings.NewReader(`[]`))
		req.Header.Set("Next-Action", "7f"+strings.Repeat("0", 40))
		req.Header.Set("Origin", srv.URL)
		req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		// Next 16.4 answers an action id it does not know with 409
		// (handleUnrecognizedAction); 16.3 with 404.
		want := 404
		if v := installedNext(t, fixtureDir(t)); !strings.HasPrefix(v, "16.3.") {
			want = 409
		}
		if resp.StatusCode != want {
			t.Errorf("status %d, want %d", resp.StatusCode, want)
		}
	})
}

var (
	formRe        = regexp.MustCompile(`(?s)<form[^>]*>(.*?)</form>`)
	hiddenInputRe = regexp.MustCompile(`<input type="hidden" name="([^"]+)"(?: value="([^"]*)")?/>`)
)

// Inline "use server" functions (inline_actions.go): the bound action's
// form posts its encrypted closure without JavaScript, and the action
// reads it back decrypted; a module-level inline action has no closure.
func TestInlineServerActions(t *testing.T) {
	srv := serveFixture(t)
	resp, err := http.Get(srv.URL + "/inline?who=og-bound")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	html := string(b)
	forms := formRe.FindAllStringSubmatch(html, -1)
	if resp.StatusCode != 200 || len(forms) != 2 {
		t.Fatalf("GET /inline: %d, %d forms:\n%s", resp.StatusCode, len(forms), truncate(html, 2000))
	}
	post := func(form string, fields map[string]string) (*http.Response, string) {
		t.Helper()
		var body strings.Builder
		mw := multipart.NewWriter(&body)
		for _, m := range hiddenInputRe.FindAllStringSubmatch(form, -1) {
			_ = mw.WriteField(m[1], stdhtml.UnescapeString(m[2]))
		}
		for k, v := range fields {
			_ = mw.WriteField(k, v)
		}
		_ = mw.Close()
		req, _ := http.NewRequest("POST", srv.URL+"/inline?who=og-bound", strings.NewReader(body.String()))
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Origin", srv.URL)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}
	// The bound action: its closure (user.profile.greeting, user.name)
	// travels encrypted in the form, not as plain text. ("-" is not in the
	// ciphertext's base64 alphabet, so the check cannot match by chance, as
	// a bare "og" did.)
	if strings.Contains(forms[0][1], "og-bound") {
		t.Errorf("the bound arguments are not encrypted:\n%s", forms[0][1])
	}
	resp, body := post(forms[0][1], map[string]string{"note": "n1"})
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Set-Cookie"), "greeted=hi%20og-bound%20n1") || !strings.Contains(body, `<p id="greeted">hi og-bound n1</p>`) {
		t.Errorf("greet: %d, set-cookie %q:\n%s", resp.StatusCode, resp.Header.Get("Set-Cookie"), truncate(body, 1500))
	}
	// The module-level action, called by id as the client router does.
	m := actionFieldRe.FindStringSubmatch(forms[1][1])
	if m == nil {
		t.Fatalf("no action id field in the reset form:\n%s", forms[1][1])
	}
	req, _ := http.NewRequest("POST", srv.URL+"/inline", strings.NewReader(`[]`))
	req.Header.Set("Next-Action", strings.TrimPrefix(m[1], "$ACTION_ID_"))
	req.Header.Set("Content-Type", "text/plain;charset=UTF-8")
	req.Header.Set("Accept", "text/x-component")
	req.Header.Set("Cookie", "greeted=x")
	req.Header.Set("Origin", srv.URL)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 200 || !strings.Contains(r.Header.Get("Set-Cookie"), "greeted=;") {
		t.Errorf("reset: %d, set-cookie %q", r.StatusCode, r.Header.Get("Set-Cookie"))
	}
}

func TestProxyMiddleware(t *testing.T) {
	srv := serveFixture(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(path string) (*http.Response, string) {
		resp, err := client.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}

	resp, _ := get("/old")
	if resp.StatusCode != 307 || !strings.HasSuffix(resp.Header.Get("Location"), "/") {
		t.Errorf("/old: %d location %q", resp.StatusCode, resp.Header.Get("Location"))
	}
	resp, body := get("/blocked")
	if resp.StatusCode != 403 || body != "blocked by proxy" {
		t.Errorf("/blocked: %d %q", resp.StatusCode, body)
	}
	resp, body = get("/hello-rewrite")
	if resp.StatusCode != 200 || !strings.Contains(body, "This page was rendered by Goja.") || resp.Header.Get("x-proxied") != "rewrite" {
		t.Errorf("/hello-rewrite: %d x-proxied=%q\n%s", resp.StatusCode, resp.Header.Get("x-proxied"), truncate(body, 800))
	}
	resp, body = get("/api/whoami")
	if host := strings.TrimPrefix(srv.URL, "http://"); resp.StatusCode != 200 || !strings.Contains(body, `"fromProxy":"yes","host":"`+host+`"`) || resp.Header.Get("x-proxied") != "next" || !strings.Contains(resp.Header.Get("Set-Cookie"), "seen=1") {
		t.Errorf("/api/whoami: %d %q x-proxied=%q set-cookie=%q", resp.StatusCode, body, resp.Header.Get("x-proxied"), resp.Header.Get("Set-Cookie"))
	}
	for k := range resp.Header {
		if strings.HasPrefix(strings.ToLower(k), "x-middleware-") {
			t.Errorf("internal header leaked: %s", k)
		}
	}
	// Static assets bypass the proxy (excluded by its matcher anyway).
	resp, _ = get("/hello.txt")
	if resp.StatusCode != 200 {
		t.Errorf("/hello.txt: %d", resp.StatusCode)
	}
}

func TestBuiltinKV(t *testing.T) {
	srv := serveFixture(t)
	var last string
	for i := 0; i < 3; i++ {
		resp, err := http.Post(srv.URL+"/api/visits", "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		last = string(b)
	}
	if last != `{"visits":"3"}` {
		t.Errorf("after 3 requests: %s", last)
	}
}

var timeRe = regexp.MustCompile(`<p id="time">(\d+)</p>`)

func TestStaticAndISR(t *testing.T) {
	srv := serveFixture(t)
	get := func(path string, hdr map[string]string) (*http.Response, string) {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}

	t.Run("prerendered static page", func(t *testing.T) {
		resp, body := get("/about", nil)
		if resp.Header.Get("x-nextjs-cache") != "HIT" || !strings.Contains(body, `<p id="about">`) {
			t.Errorf("/about: cache %q\n%s", resp.Header.Get("x-nextjs-cache"), truncate(body, 500))
		}
		if cc := resp.Header.Get("Cache-Control"); cc != "s-maxage=31536000" {
			t.Errorf("/about: Cache-Control %q", cc)
		}
		// The proxy still runs per request in front of the cache.
		if resp.Header.Get("x-proxied") != "next" || !strings.Contains(resp.Header.Get("Set-Cookie"), "seen=1") {
			t.Errorf("/about: proxy headers missing: x-proxied=%q set-cookie=%q", resp.Header.Get("x-proxied"), resp.Header.Get("Set-Cookie"))
		}
		for k := range resp.Header {
			if strings.HasPrefix(strings.ToLower(k), "x-og-") {
				t.Errorf("internal header leaked: %s", k)
			}
		}
	})

	t.Run("prerendered RSC payload serves any router state", func(t *testing.T) {
		tree := url.QueryEscape(`["",{"children":["__PAGE__",{}]},null,null,true]`)
		resp, body := get("/blog/hello?_rsc=abc", map[string]string{"RSC": "1", "Next-Router-State-Tree": tree})
		if resp.Header.Get("x-nextjs-cache") != "HIT" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/x-component") {
			t.Errorf("rsc: cache %q type %q\n%s", resp.Header.Get("x-nextjs-cache"), resp.Header.Get("Content-Type"), truncate(body, 300))
		}
	})

	t.Run("dynamic segment without generateStaticParams renders on request", func(t *testing.T) {
		resp, body := get("/tags/go", nil)
		// The client components render in SSR: one whose "use client"
		// follows "use strict", and a CommonJS one reached through a
		// CommonJS reexport.
		if resp.StatusCode != http.StatusOK || !strings.Contains(body, `<p id="tag">tag <!-- -->go</p>`) || !strings.Contains(body, `<span id="legacy-client">legacy client <!-- -->5</span>`) || !strings.Contains(body, `<b id="cjs-client">cjs client 9</b>`) {
			t.Errorf("/tags/go: %d\n%s", resp.StatusCode, truncate(body, 500))
		}
	})

	t.Run("dynamic page is never cached", func(t *testing.T) {
		for i := 0; i < 2; i++ {
			resp, _ := get("/", nil)
			if c := resp.Header.Get("x-nextjs-cache"); c != "" {
				t.Errorf("/: x-nextjs-cache %q on request %d", c, i)
			}
		}
	})

	t.Run("revalidate: stale-while-revalidate", func(t *testing.T) {
		stamp := func(body string) string {
			m := timeRe.FindStringSubmatch(body)
			if m == nil {
				t.Fatalf("no timestamp in\n%s", truncate(body, 500))
			}
			return m[1]
		}
		// Wait for a fresh entry (the build-time one may already be stale).
		var first string
		for deadline := time.Now().Add(5 * time.Second); ; {
			resp, body := get("/time", nil)
			if resp.Header.Get("x-nextjs-cache") != "STALE" {
				first = stamp(body)
				if cc := resp.Header.Get("Cache-Control"); !strings.HasPrefix(cc, "s-maxage=1") {
					t.Errorf("/time Cache-Control %q", cc)
				}
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("never got a fresh /time")
			}
			time.Sleep(50 * time.Millisecond)
		}
		time.Sleep(1100 * time.Millisecond)
		resp, body := get("/time", nil)
		if c := resp.Header.Get("x-nextjs-cache"); c != "STALE" || stamp(body) != first {
			t.Errorf("after expiry: cache %q, stamp changed=%v", c, stamp(body) != first)
		}
		// The background revalidation replaces the entry shortly after.
		deadline := time.Now().Add(5 * time.Second)
		for {
			resp, body = get("/time", nil)
			if stamp(body) != first {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("entry never regenerated")
			}
			time.Sleep(50 * time.Millisecond)
		}
	})

	t.Run("revalidatePath invalidates cached pages", func(t *testing.T) {
		resp, before := get("/about", nil)
		if resp.Header.Get("x-nextjs-cache") != "HIT" {
			t.Fatalf("precondition: %q", resp.Header.Get("x-nextjs-cache"))
		}
		req, _ := http.NewRequest("POST", srv.URL+"/api/revalidate?path=/about", nil)
		r2, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		r2.Body.Close()
		resp, after := get("/about", nil)
		if c := resp.Header.Get("x-nextjs-cache"); c != "MISS" {
			t.Errorf("after revalidatePath: cache %q", c)
		}
		if !strings.Contains(after, `<p id="about">`) || len(before) == 0 {
			t.Error("regenerated page is wrong")
		}
		if resp, _ := get("/about", nil); resp.Header.Get("x-nextjs-cache") != "HIT" {
			t.Errorf("third request: %q", resp.Header.Get("x-nextjs-cache"))
		}
	})
}

var (
	stylesheetRe = regexp.MustCompile(`<link rel="stylesheet" href="(/_next/static/css/[0-9a-f]+\.css)"`)
	classRe      = func(id string) *regexp.Regexp { return regexp.MustCompile(`<` + id + `[^>]*class="([^"]+)"`) }
	mediaRe      = regexp.MustCompile(`url\("?(/_next/static/media/[^)"]+)"?\)`)
)

func TestCSS(t *testing.T) {
	srv := serveFixture(t)
	fetch := func(path string) (*http.Response, string) {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}
	stylesheets := func(html string) string {
		var all strings.Builder
		for _, m := range stylesheetRe.FindAllStringSubmatch(html, -1) {
			resp, css := fetch(m[1])
			if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/css") {
				t.Errorf("%s: %d %q", m[1], resp.StatusCode, resp.Header.Get("Content-Type"))
			}
			all.WriteString(css)
		}
		return all.String()
	}

	_, home := fetch("/")
	css := stylesheets(home)
	// (The fixture's PostCSS config runs @tailwindcss/postcss over every
	// stylesheet, whose lightningcss pass re-minifies them.)
	for _, want := range []*regexp.Regexp{regexp.MustCompile(`--accent: ?#0070f3`), regexp.MustCompile(`font-family:system-ui`)} {
		if !want.MatchString(css) {
			t.Errorf("global CSS lacks %s:\n%s", want, css)
		}
	}
	if m := mediaRe.FindStringSubmatch(css); m == nil {
		t.Errorf("url() not rewritten to a build asset:\n%s", css)
	} else if resp, _ := fetch(m[1]); resp.StatusCode != 200 {
		t.Errorf("asset %s: %d", m[1], resp.StatusCode)
	}
	// Client component CSS module (reached through the browser graph).
	if m := regexp.MustCompile(`<button id="counter" class="([^"]+)"`).FindStringSubmatch(home); m == nil {
		t.Errorf("counter has no class:\n%s", truncate(home, 1500))
	} else if !strings.Contains(css, "."+m[1]+"{") {
		t.Errorf("no rule for counter class %q in linked CSS:\n%s", m[1], css)
	}

	// Server component CSS module on a prerendered page.
	_, post := fetch("/blog/hello")
	m := classRe("h1").FindStringSubmatch(post)
	if m == nil || !strings.Contains(m[1], "_title") {
		t.Fatalf("post title class: %v\n%s", m, truncate(post, 1500))
	}
	if postCSS := stylesheets(post); !strings.Contains(postCSS, "."+m[1]+"{color:") {
		t.Errorf("no rule for %q in the post's stylesheets:\n%s", m[1], postCSS)
	}
}

// TestPostCSS covers Next's PostCSS pipeline: the fixture's
// postcss.config.mjs runs Tailwind v4 (@tailwindcss/postcss) over
// app/about/tailwind.css: @import "tailwindcss", @theme, @apply, and
// utilities found by oxide scanning app/ (oxide and lightningcss run as
// WebAssembly, see internal/wasm).
func TestPostCSS(t *testing.T) {
	srv := serveFixture(t)
	get := func(path string) string {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("%s: %d", path, resp.StatusCode)
		}
		return string(b)
	}
	html := get("/about")
	if !strings.Contains(html, `class="about-note underline tracking-widest"`) {
		t.Fatalf("about page lacks the Tailwind classes:\n%s", truncate(html, 1500))
	}
	var css strings.Builder
	for _, m := range stylesheetRe.FindAllStringSubmatch(html, -1) {
		css.WriteString(get(m[1]))
	}
	all := css.String()
	if strings.Contains(all, "@tailwind") || strings.Contains(all, "@apply") || strings.Contains(all, `@import "tailwindcss"`) {
		t.Errorf("Tailwind directives left unprocessed:\n%s", all)
	}
	for _, want := range []string{
		"--color-brand:#0b7285", // @theme
		".about-note{--tw-font-weight:var(--font-weight-bold);font-weight:var(--font-weight-bold);color:var(--color-brand)}", // @apply
		"@layer utilities{",
		".underline{text-decoration-line:underline}", // found in app/ by oxide
		".tracking-widest{--tw-tracking:var(--tracking-widest);letter-spacing:var(--tracking-widest)}", // found in app/ by oxide
	} {
		if !strings.Contains(all, want) {
			t.Errorf("stylesheets lack %q:\n%s", want, all)
		}
	}
	// Only used utilities are generated.
	if strings.Contains(all, ".text-center{") {
		t.Errorf("unused utility generated:\n%s", all)
	}
}

// TestStyledJSX covers the webpack loader shim: babel-loader with
// styled-jsx/babel (configured in the fixture's og.config.ts), and the
// App Router style registry flushing styles into the SSR HTML.
func TestStyledJSX(t *testing.T) {
	srv := serveFixture(t)
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	html := string(b)
	m := regexp.MustCompile(`<p id="fancy" class="(jsx-\d+)[^"]*"`).FindStringSubmatch(html)
	if m == nil {
		t.Fatalf("styled-jsx did not scope the element:\n%s", truncate(html, 3000))
	}
	style := regexp.MustCompile(`<style id="__jsx-\d+">([^<]*)</style>`).FindStringSubmatch(html)
	if style == nil || !strings.Contains(style[1], "p."+m[1]+"{color:teal") {
		t.Errorf("collected styles missing or unscoped: %v\n%s", style, truncate(html, 3000))
	}
}

// The npm packages the server and browser bundles read are credited with
// their license files; build-time-only packages (Tailwind, Sass, Babel,
// TypeScript) are not.
func TestFixtureNotices(t *testing.T) {
	serveFixture(t)
	text := string(fixtureOut.Notices)
	for _, want := range []string{
		"\nnext " + installedNext(t, fixtureDir(t)) + " (MIT)\n\n--- license.md\n\nThe MIT License (MIT)\n\nCopyright (c) 2025 Vercel, Inc.",
		"\nreact-dom-builtin\n", // Next's compiled copy, under the name it gives itself
		"\nstyled-jsx 5.1.6 (MIT)\n",
		"\nbcryptjs ", // bcrypt's substitute (runtime/natives)
		// tailwindcss ships preflight/theme CSS that @import "tailwindcss"
		// inlines into the output (PluginHost.AssetReads), so it is attributed
		// even though esbuild never saw it as a bundle input.
		"\ntailwindcss ",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("notices lack %q", want)
		}
	}
	// Build-time-only tooling whose code does not ship stays unattributed: the
	// Sass and PostCSS compilers, Babel, TypeScript. (tailwindcss is listed
	// above because its CSS bytes do ship.)
	for _, bad := range []string{"\nsass ", "\n@babel/core ", "\ntypescript ", "\npostcss ", "\n@tailwindcss/postcss "} {
		if strings.Contains(text, bad) {
			t.Errorf("notices list the build-time package %q", strings.TrimSpace(bad))
		}
	}
}

// TestClientModuleLoadFailureIsolated: a client module that throws when it
// loads on the server (app/client-load-error/widget.tsx) fails the page
// that renders it, each time, not the others: SSR loads client modules as
// pages reference them, as next start does.
func TestClientModuleLoadFailureIsolated(t *testing.T) {
	srv := serveFixture(t)
	for i := 0; i < 2; i++ {
		resp, err := http.Get(srv.URL + "/client-load-error")
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 500 {
			t.Errorf("GET /client-load-error (%d): %d, want 500", i, resp.StatusCode)
		}
	}
	for _, path := range []string{"/", "/about"} {
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("GET %s: %d, want 200", path, resp.StatusCode)
		}
	}
}

// TestTypeofWindowShadowed: a module that binds window itself keeps its
// typeof window expressions; the global's are replaced.
func TestTypeofWindowShadowed(t *testing.T) {
	for src, want := range map[string]string{
		`const a = typeof window === "undefined";`:                              `const a = "object" === "undefined";`,
		`function f(window) { return typeof window; } const b = typeof window;`: `function f(window) { return typeof window; } const b = typeof window;`,
		`import window from "./w"; export const c = typeof window;`:             `import window from "./w"; export const c = typeof window;`,
		`const d = typeof windowed; const e = typeof globalThis.window;`:        `const d = typeof windowed; const e = typeof globalThis.window;`,
	} {
		if got := typeofWindow("/app/x.ts", src, "object"); got != want {
			t.Errorf("%s\n got %s\nwant %s", src, got, want)
		}
	}
}
