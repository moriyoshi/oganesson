package isr

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// fakeApp stands in for the JS handler: "/static" renders statically,
// anything else bails out of static rendering (503 to a static attempt).
type fakeApp struct {
	mu             sync.Mutex
	staticAttempts map[string]int
}

func (f *fakeApp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(StaticHeader) == "1" {
		f.mu.Lock()
		f.staticAttempts[r.URL.Path]++
		f.mu.Unlock()
		if r.URL.Path != "/static" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set(CacheControlHeader, `{"revalidate":false}`)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte("<p>" + r.URL.Path + "</p>"))
}

func (f *fakeApp) attempts(path string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.staticAttempts[path]
}

// TestSeedDynamic: paths the build found dynamic skip the static render
// attempt for any query, while other paths are still tried (and cached).
func TestSeedDynamic(t *testing.T) {
	app := &fakeApp{staticAttempts: map[string]int{}}
	c := NewCache()
	c.SeedDynamic([]string{"/dyn"})
	h := &Handler{Cache: c, Next: app}
	get := func(target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d", target, rec.Code)
		}
		return rec
	}

	for _, target := range []string{"/dyn", "/dyn?q=1"} {
		if rec := get(target); rec.Header().Get("x-nextjs-cache") != "" {
			t.Errorf("%s: served from the cache layer (%s)", target, rec.Header().Get("x-nextjs-cache"))
		}
	}
	if n := app.attempts("/dyn"); n != 0 {
		t.Errorf("/dyn: %d static render attempt(s), want 0", n)
	}

	// Unseeded paths: a static attempt, then the cache.
	if rec := get("/static"); rec.Header().Get("x-nextjs-cache") != "MISS" {
		t.Errorf("/static first request: %q, want MISS", rec.Header().Get("x-nextjs-cache"))
	}
	if rec := get("/static"); rec.Header().Get("x-nextjs-cache") != "HIT" {
		t.Errorf("/static second request: %q, want HIT", rec.Header().Get("x-nextjs-cache"))
	}
	get("/other")
	get("/other")
	if n := app.attempts("/other"); n != 1 {
		t.Errorf("/other: %d static render attempt(s), want 1 (then remembered as dynamic)", n)
	}
}

// pageApp renders /page statically with a companion (its data JSON at
// /data/page.json), stamping each render with a counter.
type pageApp struct {
	mu           sync.Mutex
	renders      int
	status       int
	staticStatus int // status of static attempts only
}

func (p *pageApp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get(MatchHeader) != "" {
		w.WriteHeader(http.StatusNoContent) // every URL has a route
		return
	}
	p.mu.Lock()
	p.renders++
	n, status := p.renders, p.status
	if r.Header.Get(StaticHeader) == "1" && p.staticStatus != 0 {
		status = p.staticStatus
	}
	p.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	if r.Header.Get(StaticHeader) == "1" {
		w.Header().Set(CacheControlHeader, `{"revalidate":false}`)
		w.Header().Set(CompanionPathHeader, "/data/page.json")
		w.Header().Set(CompanionTypeHeader, "application/json")
		w.Header().Set(CompanionHeader, base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf(`{"n":%d}`, n))))
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, "<p>%d</p>", n)
}

// TestCompanionAndRevalidate: one static render caches its companion under
// the companion's URL; Revalidate regenerates both, honoring onlyGenerated.
func TestCompanionAndRevalidate(t *testing.T) {
	app := &pageApp{}
	h := &Handler{Cache: NewCache(), Next: app}
	get := func(target string) (string, string, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		return rec.Body.String(), rec.Header().Get("x-nextjs-cache"), rec.Header().Get("Content-Type")
	}
	if body, cache, _ := get("/page"); body != "<p>1</p>" || cache != "MISS" {
		t.Fatalf("/page: %q %s", body, cache)
	}
	if body, cache, ct := get("/data/page.json"); body != `{"n":1}` || cache != "HIT" || ct != "application/json" {
		t.Fatalf("companion: %q %s %s", body, cache, ct)
	}

	status, revalidated, err := h.Revalidate(context.Background(), "/page", false)
	if err != nil || status != 200 || !revalidated {
		t.Fatalf("Revalidate: %d %v %v", status, revalidated, err)
	}
	if body, cache, _ := get("/page"); body != "<p>2</p>" || cache != "HIT" {
		t.Errorf("/page after revalidation: %q %s", body, cache)
	}
	if body, _, _ := get("/data/page.json"); body != `{"n":2}` {
		t.Errorf("companion after revalidation: %q", body)
	}

	// onlyGenerated: a path never generated is not rendered.
	renders := app.renders
	if status, revalidated, _ := h.Revalidate(context.Background(), "/other", true); status != 404 || !revalidated || app.renders != renders {
		t.Errorf("onlyGenerated: %d %v, %d render(s)", status, revalidated, app.renders-renders)
	}
	// A failing render is reported, and the cached page is kept.
	app.status = 500
	if status, revalidated, _ := h.Revalidate(context.Background(), "/page", false); status != 500 || revalidated {
		t.Errorf("failing render: %d %v", status, revalidated)
	}
	// A static attempt that bails out (503) is followed by a normal render,
	// whose status is reported (a dynamic page revalidates fine).
	app.status, app.staticStatus = 0, 503
	if status, revalidated, _ := h.Revalidate(context.Background(), "/page", false); status != 200 || revalidated {
		t.Errorf("dynamic page: %d %v", status, revalidated)
	}
	app.staticStatus = 0
	if body, cache, _ := get("/page"); body != "<p>2</p>" || cache != "HIT" {
		t.Errorf("/page after a failed revalidation: %q %s", body, cache)
	}
}

// TestPathOnly: a render marked PathKeyHeader is cached under its path and
// serves every query; unmarked renders stay keyed by query.
func TestPathOnly(t *testing.T) {
	var renders int
	app := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(MatchHeader) != "" {
			w.WriteHeader(http.StatusNoContent) // every URL has a route
			return
		}
		renders++
		if r.Header.Get(StaticHeader) == "1" {
			w.Header().Set(CacheControlHeader, `{"revalidate":false}`)
			if r.URL.Path == "/path-only" {
				w.Header().Set(PathKeyHeader, "1")
			}
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprintf(w, "<p>%d</p>", renders)
	})
	h := &Handler{Cache: NewCache(), Next: app}
	get := func(target string) (string, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		return rec.Body.String(), rec.Header().Get("x-nextjs-cache")
	}
	for _, c := range []struct{ target, body, cache string }{
		{"/path-only?a=1", "<p>1</p>", "MISS"},
		{"/path-only", "<p>1</p>", "HIT"},
		{"/path-only?b=2", "<p>1</p>", "HIT"},
		{"/keyed?a=1", "<p>2</p>", "MISS"},
		{"/keyed?b=2", "<p>3</p>", "MISS"},
		{"/keyed?a=1", "<p>2</p>", "HIT"},
	} {
		if body, cache := get(c.target); body != c.body || cache != c.cache {
			t.Errorf("%s: %q %s, want %q %s", c.target, body, cache, c.body, c.cache)
		}
	}
}

// TestDynamicMarkerIgnoresQuery: a page that bails out of static rendering
// is marked dynamic once for every query (whether Next renders a page
// statically never depends on the query): one static attempt, one marker,
// however many queries arrive.
func TestDynamicMarkerIgnoresQuery(t *testing.T) {
	app := &fakeApp{staticAttempts: map[string]int{}}
	c := NewCache()
	h := &Handler{Cache: c, Next: app}
	for i := 0; i < 20; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/other?q=%d", i), nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("q=%d: %d", i, rec.Code)
		}
	}
	if n := app.attempts("/other"); n != 1 {
		t.Errorf("static attempts: %d, want 1", n)
	}
	c.mu.Lock()
	n := len(c.dynamic)
	c.mu.Unlock()
	if n != 1 {
		t.Errorf("dynamic markers: %d, want 1", n)
	}
}
