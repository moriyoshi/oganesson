package app

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"

	"github.com/moriyoshi/oganesson/runtime/isr"
)

// PrerenderResult reports a build-time prerender.
type PrerenderResult struct {
	JSON    []byte   // contents of PrerenderFile
	Static  []string // paths whose HTML was prerendered
	Dynamic []string // paths that render per request
	// Partial are the static paths whose HTML is a shell with postponed
	// parts, resumed per request (Cache Components' partial prerendering).
	Partial []string
	Entries map[string]*isr.Entry
	// StaticPaths are the getStaticPaths results of dynamic pages routes,
	// by page (for their fallback mode at runtime).
	StaticPaths map[string]*isr.StaticPaths
}

// PrerenderRoute is a route to prerender: a pathname pattern and, for
// patterns with dynamic segments, the page whose generateStaticParams
// supplies the concrete paths.
type PrerenderRoute struct {
	Pathname string // e.g. "/blog/[slug]"
	Page     string // e.g. "/blog/[slug]/page"
}

// Prerender renders routes (HTML and RSC payload) through the application
// in fsys and returns the cacheable results, ready to bundle as
// PrerenderFile. JS console output is kept back: pages that bail out of
// static rendering are reported as dynamic instead; a failed
// generateStaticParams (or getStaticPaths) reports it with its error.
func Prerender(fsys fs.FS, routes []PrerenderRoute) (*PrerenderResult, error) {
	console := &consoleBuffer{}
	logger := slog.New(slog.NewTextHandler(console, nil))
	h, meta, err := handler(fsys, logger, console)
	if err != nil {
		return nil, err
	}
	// Prerendering a force-dynamic route executes it once (to detect it is
	// dynamic), which can open a database connection through the native SQL
	// shims. Release those warm pools when prerendering finishes: the process
	// is otherwise left holding a connection, and against a single-session
	// server (PGlite in tests) that connection blocks the one the serving app
	// then needs. In a real `og build` prerender and serve are separate
	// processes, so this is also plain build-time cleanup.
	if meta.Close != nil {
		defer meta.Close()
	}
	// Pages are rendered (and cached) at their URLs, under basePath.
	base := strings.TrimSuffix(meta.BasePath, "/")
	// Handler wraps the JS handler in the ISR layer; prerender beneath it.
	jsHandler := unwrap(h).next.(*isr.Handler).Next
	var paths []string
	staticPaths := map[string]*isr.StaticPaths{}
	for _, r := range routes {
		if !strings.Contains(r.Pathname, "[") {
			paths = append(paths, r.Pathname)
			continue
		}
		req := httptest.NewRequest(http.MethodGet, "http://localhost/", nil)
		req.Header.Set(isr.StaticParamsHeader, r.Page)
		console.Reset()
		rec := httptest.NewRecorder()
		jsHandler.ServeHTTP(rec, req)
		// Param sets to fill into the pattern, or concrete paths (strings).
		var sets []any
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &sets) != nil {
			return nil, fmt.Errorf("prerender: generateStaticParams for %s failed (%d): %s%s", r.Page, rec.Code, rec.Body.String(), console.Report())
		}
		var sp *isr.StaticPaths
		if fb := rec.Header().Get(isr.FallbackHeader); fb != "" {
			sp = &isr.StaticPaths{Paths: []string{}}
			if err := json.Unmarshal([]byte(fb), &sp.Fallback); err != nil {
				return nil, fmt.Errorf("prerender: fallback of %s: %w", r.Page, err)
			}
			staticPaths[r.Page] = sp
		}
		for _, set := range sets {
			var p string
			switch v := set.(type) {
			case string:
				p = v
			case map[string]any:
				if p, err = fillPattern(r.Pathname, v); err != nil {
					return nil, fmt.Errorf("prerender: %s: %w", r.Page, err)
				}
			default:
				return nil, fmt.Errorf("prerender: %s: unexpected static params %v", r.Page, set)
			}
			// trailingSlash: prerender a dynamic route's concrete paths under
			// their canonical slash form, so the request-time redirect to it
			// lands on a cache HIT (static routes are already canonical from
			// PrerenderRoutes). The runtime path comparison canonicalizes both
			// sides, so sp.Paths may keep either form.
			if meta.TrailingSlash && p != "/" && !strings.HasSuffix(p, "/") {
				p += "/"
			}
			paths = append(paths, p)
			if sp != nil {
				sp.Paths = append(sp.Paths, p)
			}
		}
	}
	cache := isr.NewCache()
	res := &PrerenderResult{StaticPaths: staticPaths}
	for _, p := range paths {
		// One static render yields the HTML and the RSC payload entries.
		u := prerenderURL(base, p, meta.TrailingSlash)
		html := httptest.NewRequest(http.MethodGet, "http://localhost"+u, nil)
		if dyn := isr.Prerender(cache, jsHandler, []*http.Request{html}); len(dyn) > 0 {
			res.Dynamic = append(res.Dynamic, p)
		} else {
			res.Static = append(res.Static, p)
		}
	}
	res.Entries = cache.Snapshot()
	for _, p := range res.Static {
		u := prerenderURL(base, p, meta.TrailingSlash)
		if e := res.Entries[isr.Key(httptest.NewRequest(http.MethodGet, "http://localhost"+u, nil))]; e != nil && e.Postponed != "" {
			res.Partial = append(res.Partial, p)
		}
	}
	sort.Strings(res.Static)
	sort.Strings(res.Dynamic)
	res.JSON, err = json.Marshal(prerenderFile{Entries: res.Entries, Dynamic: res.Dynamic, StaticPaths: staticPaths})
	return res, err
}

// prerenderURL is the URL to prerender path p at, under basePath. The home is
// served at the basePath itself ("/docs"), or at its slash form ("/docs/")
// when trailingSlash canonicalizes it there.
func prerenderURL(base, p string, trailingSlash bool) string {
	if base != "" && p == "/" {
		if trailingSlash {
			return base + "/"
		}
		return base
	}
	return base + p
}

// fillPattern substitutes params into a Next pathname pattern.
func fillPattern(pattern string, params map[string]any) (string, error) {
	var out []string
	for _, seg := range strings.Split(strings.Trim(pattern, "/"), "/") {
		if !strings.HasPrefix(seg, "[") {
			out = append(out, seg)
			continue
		}
		name := strings.Trim(seg, "[]")
		optional := strings.HasPrefix(seg, "[[")
		catchAll := strings.HasPrefix(name, "...")
		name = strings.TrimPrefix(name, "...")
		v, ok := params[name]
		switch {
		case !ok || v == nil:
			if optional {
				continue
			}
			return "", fmt.Errorf("generateStaticParams result lacks %q", name)
		case catchAll:
			parts, ok := v.([]any)
			if !ok {
				return "", fmt.Errorf("catch-all param %q must be an array", name)
			}
			for _, p := range parts {
				out = append(out, url.PathEscape(fmt.Sprint(p)))
			}
		default:
			out = append(out, url.PathEscape(fmt.Sprint(v)))
		}
	}
	return "/" + strings.Join(out, "/"), nil
}

// consoleBuffer keeps the server's output (console and log) since the last
// Reset, for an error report.
type consoleBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *consoleBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.buf.Len() < 64<<10 {
		c.buf.Write(p)
	}
	return len(p), nil
}

func (c *consoleBuffer) Reset() {
	c.mu.Lock()
	c.buf.Reset()
	c.mu.Unlock()
}

// Report is the output, indented under an error message ("" for none).
func (c *consoleBuffer) Report() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := strings.TrimRight(c.buf.String(), "\n")
	if out == "" {
		return ""
	}
	return "\n  " + strings.ReplaceAll(out, "\n", "\n  ")
}
