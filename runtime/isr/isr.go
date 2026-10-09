// Package isr is Oganesson's caching platform in front of a Next.js server: the
// role a CDN plays for Next deployments. On a miss it asks the app for a
// static render (Next's static generation mode) and caches the result if
// Next reports it cacheable; it serves stale entries while revalidating in
// the background, remembers variants that bail out to dynamic rendering,
// and drops entries when their tags are revalidated.
package isr

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Protocol headers shared with the patched Next templates.
const (
	StaticHeader       = "x-og-static"
	StaticParamsHeader = "x-og-static-params" // build time: list generateStaticParams of a page
	// FallbackHeader accompanies a pages route's static params: its
	// getStaticPaths fallback as Next's FallbackMode (a JSON string).
	FallbackHeader = "x-og-fallback"

	// PhaseHeader splits the pipeline: "middleware" (the proxy's decision
	// only) or "route" (render without re-running the proxy).
	PhaseHeader = "x-og-phase"
	// Middleware decision headers ("response" or "continue").
	MiddlewareHeader                = "x-og-mw"
	MiddlewareURLHeader             = "x-og-mw-url"
	MiddlewareRequestHeadersHeader  = "x-og-mw-request-headers"
	MiddlewareResponseHeadersHeader = "x-og-mw-response-headers"
	CacheControlHeader              = "x-og-cache-control"
	TagsHeader                      = "x-og-tags"
	// FlightHeader carries a static render's RSC payload (base64), cached
	// alongside its HTML for client navigation.
	FlightHeader = "x-og-flight"
	// A static render's companion: another URL's response from the same
	// render (base64 body, its URL path and content type). A Pages Router
	// SSG render yields both the HTML and the /_next/data JSON, which Next
	// caches as one entry.
	CompanionHeader     = "x-og-companion"
	CompanionPathHeader = "x-og-companion-path"
	CompanionTypeHeader = "x-og-companion-type"
	// PathKeyHeader ("1") marks a static render whose output does not depend
	// on the query string (Next's static pages and SSG data): it is cached
	// under its path alone and serves every query.
	PathKeyHeader = "x-og-path-key"
	// MatchHeader asks the JS handler where a URL that missed the cache is
	// cached; it answers with CachePathHeader: the not-found page's path for
	// a URL no route matches (Next serves those from its one /_not-found
	// entry), or nothing.
	MatchHeader     = "x-og-match"
	CachePathHeader = "x-og-cache-path"
	// PostponedHeader carries a static render's postponed state (base64):
	// the render postponed part of the page (Cache Components' dynamic
	// holes), so its HTML is a shell that each request resumes. A resume
	// render gets that state back in ResumeHeader and renders the rest.
	PostponedHeader = "x-og-postponed"
	ResumeHeader    = "x-og-resume"
)

// varyHeaders select distinct cached variants of one URL. Next varies app
// route responses on these plus next-router-state-tree; for cached
// (static) pages Next serves the full RSC payload whatever the client's
// router state, so the state tree is left out of the key and stripped from
// static renders. next-url (interception routes) still varies.
var varyHeaders = []string{"rsc", "next-router-prefetch", "next-router-segment-prefetch", "next-url"}

// Entry is a cached response.
type Entry struct {
	Status     int                 `json:"status"`
	Header     map[string][]string `json:"header"`
	Body       []byte              `json:"body"`
	Tags       []string            `json:"tags,omitempty"`
	Created    time.Time           `json:"created"`
	Revalidate int64               `json:"revalidate"` // seconds; -1 = never (revalidate: false)
	Expire     int64               `json:"expire"`     // seconds; -1 = never
	// Route marks a route handler's response (any content type), whose own
	// Cache-Control is kept when served from the cache.
	Route bool `json:"route,omitempty"`
	// PathOnly marks an entry cached under its path alone (PathKeyHeader).
	PathOnly bool `json:"pathOnly,omitempty"`
	// Postponed is the state of a render that postponed part of the page:
	// Body is the static shell, which each request completes by a resume
	// render (PostponedHeader).
	Postponed string `json:"postponed,omitempty"`
}

func (e *Entry) fresh(now time.Time) bool {
	return e.Revalidate < 0 || now.Before(e.Created.Add(time.Duration(e.Revalidate)*time.Second))
}

func (e *Entry) expired(now time.Time) bool {
	return e.Expire >= 0 && !now.Before(e.Created.Add(time.Duration(e.Expire)*time.Second))
}

// Cache is the page cache plus the set of known-dynamic variants.
type Cache struct {
	mu      sync.Mutex
	entries map[string]*Entry
	dynamic map[string]bool
	// dynamicPaths are paths the build found dynamic (SeedDynamic).
	dynamicPaths map[string]bool
	pending      map[string]bool // background revalidations in flight
	now          func() time.Time
	ds           *dataState     // Next's data (fetch) cache and tag revalidation times
	uc           *useCacheState // "use cache" entries and tags (usecache.go)
	// staticPaths are the build's static paths of dynamic pages routes, by
	// page (SeedStaticPaths).
	staticPaths map[string]*StaticPaths
	// revalidator regenerates a path on demand (SetRevalidator).
	revalidator func(ctx context.Context, path string, onlyGenerated bool) (int, bool, error)
}

// SetRevalidator installs the on-demand revalidation that JS reaches as
// og.cache.revalidatePath (typically a Handler's Revalidate).
func (c *Cache) SetRevalidator(f func(ctx context.Context, path string, onlyGenerated bool) (int, bool, error)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.revalidator = f
}

// StaticPaths is what the build found for a dynamic route with
// getStaticPaths: the fallback mode and the paths it prerendered (Next's
// prerender-manifest dynamicRoutes and routes).
type StaticPaths struct {
	// Fallback is Next's FallbackMode: "NOT_FOUND" (fallback: false),
	// "PRERENDER" (true) or "BLOCKING_STATIC_RENDER" ("blocking").
	Fallback string   `json:"fallback"`
	Paths    []string `json:"paths"`
}

// NewCache returns an empty cache.
func NewCache() *Cache {
	return &Cache{entries: map[string]*Entry{}, dynamic: map[string]bool{}, dynamicPaths: map[string]bool{}, pending: map[string]bool{}, now: time.Now}
}

// Seed adds prerendered entries (e.g. from the build).
func (c *Cache) Seed(entries map[string]*Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range entries {
		c.entries[k] = e
	}
}

// SeedDynamic records paths found to render dynamically at build time
// (whatever their query or headers), so requests for them go straight to
// dynamic rendering instead of attempting a static render first, as Next
// does for routes its prerender manifest lacks.
func (c *Cache) SeedDynamic(paths []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range paths {
		c.dynamicPaths[p] = true
	}
}

// SeedStaticPaths records the build's static paths by page; the JS
// dispatcher reads them (og.cache.staticPaths) to apply the fallback mode.
func (c *Cache) SeedStaticPaths(m map[string]*StaticPaths) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.staticPaths == nil {
		c.staticPaths = map[string]*StaticPaths{}
	}
	for k, v := range m {
		c.staticPaths[k] = v
	}
}

// Snapshot returns a copy of every entry (for writing prerender output).
func (c *Cache) Snapshot() map[string]*Entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make(map[string]*Entry, len(c.entries))
	for k, e := range c.entries {
		out[k] = e
	}
	return out
}

// RevalidateTags drops every entry carrying one of tags; the next request
// regenerates it.
func (c *Cache) RevalidateTags(tags []string) {
	set := map[string]bool{}
	for _, t := range tags {
		set[t] = true
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range c.entries {
		for _, t := range e.Tags {
			if set[t] {
				delete(c.entries, k)
				break
			}
		}
	}
}

// Key identifies a cached variant: path, query without the _rsc
// cache-buster, and the Vary-relevant request headers.
func Key(r *http.Request) string {
	q := r.URL.Query()
	q.Del("_rsc")
	var b strings.Builder
	b.WriteString(r.URL.Path)
	if enc := q.Encode(); enc != "" {
		b.WriteString("?" + enc)
	}
	for _, h := range varyHeaders {
		if v := r.Header.Get(h); v != "" {
			fmt.Fprintf(&b, "\n%s: %s", h, v)
		}
	}
	return b.String()
}

// nonHTMLDest are the Sec-Fetch-Dest values of subresource requests
// (Next's isNonHtmlSecFetchDest).
var nonHTMLDest = map[string]bool{
	"audio": true, "audioworklet": true, "font": true, "image": true, "json": true, "manifest": true,
	"paintworklet": true, "report": true, "script": true, "serviceworker": true, "sharedworker": true,
	"style": true, "track": true, "video": true, "webidentity": true, "worker": true, "xslt": true,
}

// pathKey is r's cache key without its query string.
func pathKey(r *http.Request) string {
	v := r.Clone(r.Context())
	u := *r.URL
	u.RawQuery = ""
	v.URL = &u
	return Key(v)
}

// lookup finds r's entry: under its own key, else a path-only entry for
// its path. It returns the key the entry is (or would be) stored under.
// c.mu must be held.
func (c *Cache) lookup(r *http.Request) (string, *Entry) {
	key := Key(r)
	if e := c.entries[key]; e != nil {
		return key, e
	}
	if pk := pathKey(r); pk != key {
		if e := c.entries[pk]; e != nil && e.PathOnly {
			return pk, e
		}
	}
	return key, nil
}

// rendered picks r's own entry out of a render's entries.
func rendered(entries map[string]*Entry, r *http.Request) *Entry {
	if e := entries[Key(r)]; e != nil {
		return e
	}
	if e := entries[pathKey(r)]; e != nil && e.PathOnly {
		return e
	}
	return nil
}

// Handler wraps next with the cache.
type Handler struct {
	Cache *Cache
	Next  http.Handler
	// Middleware reports that the app has a proxy/middleware, which then
	// runs on every request in front of the cache, as on Next's platform.
	Middleware bool
	// Bypass reports requests that must neither be served from the cache
	// nor fill it (Next's draft mode): they render per request.
	Bypass func(*http.Request) bool
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Internal protocol headers come only from Oganesson itself.
	for k := range r.Header {
		if strings.HasPrefix(strings.ToLower(k), "x-og-") {
			r.Header.Del(k)
		}
	}
	// Subresource requests (images, fonts, ...: a Sec-Fetch-Dest that can
	// never render HTML) bypass the cache: Next answers them a plain 404
	// before its cache when the URL is unknown or the 404 page.
	if (r.Method != http.MethodGet && r.Method != http.MethodHead) || isAction(r) || nonHTMLDest[r.Header.Get("Sec-Fetch-Dest")] {
		h.Next.ServeHTTP(stripInternal(w), r)
		return
	}
	var mwHeaders [][2]string
	if h.Middleware {
		var done bool
		r, mwHeaders, done = h.runMiddleware(w, r)
		if done {
			return
		}
	}
	w = withHeaders(stripInternal(w), mwHeaders)
	r.Header.Set(PhaseHeader, "route")
	if h.Bypass != nil && h.Bypass(r) {
		h.Next.ServeHTTP(w, r)
		return
	}
	if r.Header.Get("next-router-segment-prefetch") != "" {
		// Per-segment prefetch data is not cached (rendered per request).
		h.Next.ServeHTTP(w, r)
		return
	}
	c := h.Cache
	now := c.now()
	c.mu.Lock()
	key, e := c.lookup(r)
	// Dynamic markers drop the query: whether a page renders statically
	// never depends on it (reading searchParams makes it dynamic), so one
	// marker covers every query of a dynamic page.
	dynamic := c.dynamic[pathKey(r)] || c.dynamicPaths[r.URL.Path]
	c.mu.Unlock()
	if e == nil && !dynamic {
		// A URL no route matches is cached as the not-found page, once for
		// every such URL (they would otherwise fill the cache).
		if nf := h.notFoundRequest(r); nf != nil {
			r = nf
			c.mu.Lock()
			key, e = c.lookup(r)
			dynamic = c.dynamic[pathKey(r)]
			c.mu.Unlock()
		}
	}

	switch {
	case e != nil && e.fresh(now):
		h.write(w, r, e, "HIT")
		return
	case e != nil && !e.expired(now):
		h.write(w, r, e, "STALE")
		h.revalidateInBackground(r, key)
		return
	case dynamic:
		h.Next.ServeHTTP(w, r)
		return
	}

	entries, ok := h.render(r)
	e = rendered(entries, r)
	if !ok || e == nil {
		// Not cacheable: Next bailed out of static rendering (or failed).
		// Remember the variant and serve it dynamically.
		c.mu.Lock()
		c.dynamic[pathKey(r)] = true
		c.mu.Unlock()
		h.Next.ServeHTTP(w, r)
		return
	}
	c.store(entries)
	h.write(w, r, e, "MISS")
}

// notFoundRequest is r as the request for the not-found page that answers
// it, when no route matches r's URL (MatchHeader), else nil.
func (h *Handler) notFoundRequest(r *http.Request) *http.Request {
	q := r.Clone(r.Context())
	q.Header.Set(MatchHeader, "1")
	rec := newRecorder()
	h.Next.ServeHTTP(rec, q)
	p := rec.header.Get(CachePathHeader)
	if p == "" || !strings.HasPrefix(p, "/") {
		return nil
	}
	nf := r.Clone(r.Context())
	nf.URL = &url.URL{Path: p}
	nf.RequestURI = p
	return nf
}

func isAction(r *http.Request) bool {
	return r.Header.Get("Next-Action") != ""
}

// render statically renders the page r addresses. One render yields its
// HTML and, from Next's render metadata, its RSC payload, keyed by the
// cache keys of the HTML, RSC and RSC-prefetch variants. ok is false when
// the result must not be cached.
func (h *Handler) render(r *http.Request) (map[string]*Entry, bool) {
	entries, ok, _ := h.renderStatus(r)
	return entries, ok
}

// renderStatus is render, also reporting the render's status.
func (h *Handler) renderStatus(r *http.Request) (map[string]*Entry, bool, int) {
	req := r.Clone(r.Context())
	req.Method = http.MethodGet
	for _, v := range []string{"rsc", "next-router-state-tree", "next-router-prefetch", "next-router-segment-prefetch"} {
		req.Header.Del(v)
	}
	q := req.URL.Query()
	q.Del("_rsc")
	req.URL.RawQuery = q.Encode()
	req.Header.Set(StaticHeader, "1")
	rec := newRecorder()
	h.Next.ServeHTTP(rec, req)
	// Pages render HTML (200, or 404 for notFound()). Anything else that
	// reports cache metadata is a route handler, which Next caches unless
	// its status is an error other than 404 (exportAppRoute).
	route := !strings.HasPrefix(rec.header.Get("Content-Type"), "text/html")
	switch {
	case route && rec.status >= 400 && rec.status != http.StatusNotFound:
		return nil, false, rec.status
	case !route && rec.status != http.StatusOK && rec.status != http.StatusNotFound:
		return nil, false, rec.status
	}
	var cc struct {
		Revalidate any `json:"revalidate"`
		Expire     any `json:"expire"`
	}
	raw := rec.header.Get(CacheControlHeader)
	if raw == "" || json.Unmarshal([]byte(raw), &cc) != nil {
		return nil, false, rec.status
	}
	if len(rec.header.Values("Set-Cookie")) > 0 {
		return nil, false, rec.status // per-user output must never be shared
	}
	base := Entry{Status: rec.status, Created: h.Cache.now(), Revalidate: -1, Expire: -1, Route: route}
	switch v := cc.Revalidate.(type) {
	case float64:
		if v <= 0 {
			return nil, false, rec.status // revalidate: 0 means dynamic
		}
		base.Revalidate = int64(v)
	case bool: // false: never revalidate
	default:
		return nil, false, rec.status
	}
	if v, ok := cc.Expire.(float64); ok && v > 0 {
		base.Expire = int64(v)
	}
	if t := rec.header.Get(TagsHeader); t != "" {
		base.Tags = strings.Split(t, ",")
	}
	if p := rec.header.Get(PostponedHeader); p != "" {
		st, err := base64.StdEncoding.DecodeString(p)
		if err != nil {
			return nil, false, rec.status
		}
		base.Postponed = string(st)
	}
	header := map[string][]string{}
	for k, vs := range rec.header {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-og-") || lk == "date" || lk == "content-length" {
			continue
		}
		header[k] = append([]string(nil), vs...)
	}

	key := Key(req)
	if rec.header.Get(PathKeyHeader) == "1" {
		base.PathOnly = true
		key = pathKey(req)
	}
	html := base
	html.Header = header
	html.Body = rec.body.Bytes()
	entries := map[string]*Entry{key: &html}

	// A postponed render's RSC payload is partial: client navigations
	// render the page instead (only the HTML shell is cached).
	if f := rec.header.Get(FlightHeader); f != "" && base.Postponed == "" {
		if flight, err := base64.StdEncoding.DecodeString(f); err == nil {
			rsc := base
			rsc.Body = flight
			rsc.Header = map[string][]string{}
			for k, vs := range header {
				rsc.Header[k] = vs
			}
			rsc.Header["Content-Type"] = []string{"text/x-component"}
			for _, prefetch := range []string{"", "1"} {
				v := req.Clone(req.Context())
				v.Header.Set("rsc", "1")
				if prefetch != "" {
					v.Header.Set("next-router-prefetch", prefetch)
				}
				if base.PathOnly {
					entries[pathKey(v)] = &rsc
				} else {
					entries[Key(v)] = &rsc
				}
			}
		}
	}
	if p := rec.header.Get(CompanionPathHeader); p != "" {
		if body, err := base64.StdEncoding.DecodeString(rec.header.Get(CompanionHeader)); err == nil {
			comp := base
			comp.Body = body
			comp.Header = map[string][]string{}
			for k, vs := range header {
				comp.Header[k] = vs
			}
			comp.Header["Content-Type"] = []string{rec.header.Get(CompanionTypeHeader)}
			v := req.Clone(req.Context())
			v.URL = &url.URL{Path: p}
			v.Header = http.Header{}
			entries[Key(v)] = &comp
		}
	}
	return entries, true, rec.status
}

func (c *Cache) store(entries map[string]*Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, e := range entries {
		c.entries[k] = e
		delete(c.dynamic, k)
	}
}

func (h *Handler) revalidateInBackground(r *http.Request, key string) {
	c := h.Cache
	c.mu.Lock()
	if c.pending[key] {
		c.mu.Unlock()
		return
	}
	c.pending[key] = true
	c.mu.Unlock()
	// Detach from the client's request: it is about to complete.
	req := r.Clone(contextWithoutCancel(r))
	go func() {
		defer func() {
			c.mu.Lock()
			delete(c.pending, key)
			c.mu.Unlock()
		}()
		entries, ok := h.render(req)
		if ok && rendered(entries, req) != nil {
			c.store(entries)
			return
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		delete(c.entries, key)
		c.dynamic[pathKey(req)] = true
	}()
}

// write serves a cached entry with Next's Cache-Control semantics.
func (h *Handler) write(w http.ResponseWriter, r *http.Request, e *Entry, status string) {
	if e.Postponed != "" {
		h.writeResumed(w, r, e, status)
		return
	}
	hdr := w.Header()
	for k, vs := range e.Header {
		hdr[k] = vs
	}
	hdr.Set("x-nextjs-cache", status)
	// A route handler's own Cache-Control wins (Next's app-route template).
	if !e.Route || hdr.Get("Cache-Control") == "" {
		hdr.Set("Cache-Control", cacheControl(e))
	}
	if age := int(h.Cache.now().Sub(e.Created).Seconds()); age > 0 {
		hdr.Set("Age", fmt.Sprint(age))
	}
	hdr.Set("Content-Length", fmt.Sprint(len(e.Body)))
	w.WriteHeader(e.Status)
	if r.Method != http.MethodHead {
		_, _ = w.Write(e.Body)
	}
}

// writeResumed serves a postponed entry as Next's app-page handler does:
// the cached shell, then the rest of the page from a resume render of the
// postponed state, streamed after it. The response is not cacheable (its
// dynamic part is per request).
func (h *Handler) writeResumed(w http.ResponseWriter, r *http.Request, e *Entry, status string) {
	hdr := w.Header()
	for k, vs := range e.Header {
		hdr[k] = vs
	}
	hdr.Set("x-nextjs-cache", status)
	hdr.Set("x-nextjs-postponed", "1")
	hdr.Set("Cache-Control", "private, no-cache, no-store, max-age=0, must-revalidate")
	w.WriteHeader(e.Status)
	if r.Method == http.MethodHead {
		return
	}
	_, _ = w.Write(e.Body)
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	req := r.Clone(r.Context())
	req.Method = http.MethodGet
	req.Header.Del(StaticHeader)
	req.Header.Set(PhaseHeader, "route")
	req.Header.Set(ResumeHeader, base64.StdEncoding.EncodeToString([]byte(e.Postponed)))
	h.Next.ServeHTTP(appendWriter{w}, req)
}

// appendWriter appends a response's body to one already started (its
// status and headers are dropped).
type appendWriter struct{ w http.ResponseWriter }

func (a appendWriter) Header() http.Header         { return http.Header{} }
func (a appendWriter) WriteHeader(int)             {}
func (a appendWriter) Write(b []byte) (int, error) { return a.w.Write(b) }
func (a appendWriter) Flush() {
	if f, ok := a.w.(http.Flusher); ok {
		f.Flush()
	}
}

// cacheControl mirrors Next's getCacheControlHeader (server/lib/cache-control).
func cacheControl(e *Entry) string {
	const oneYear = 31536000
	swr := ""
	if e.Revalidate >= 0 && e.Expire >= 0 && e.Revalidate < e.Expire {
		swr = fmt.Sprintf(", stale-while-revalidate=%d", e.Expire-e.Revalidate)
	}
	if e.Revalidate >= 0 {
		return fmt.Sprintf("s-maxage=%d%s", e.Revalidate, swr)
	}
	return fmt.Sprintf("s-maxage=%d%s", oneYear, swr)
}

// --- helpers --------------------------------------------------------------

type recorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func newRecorder() *recorder                    { return &recorder{header: http.Header{}, status: http.StatusOK} }
func (r *recorder) Header() http.Header         { return r.header }
func (r *recorder) WriteHeader(s int)           { r.status = s }
func (r *recorder) Write(b []byte) (int, error) { return r.body.Write(b) }

// stripInternal hides x-og-* response headers from clients.
func stripInternal(w http.ResponseWriter) http.ResponseWriter { return &stripWriter{ResponseWriter: w} }

type stripWriter struct {
	http.ResponseWriter
	wrote bool
}

func (s *stripWriter) strip() {
	if s.wrote {
		return
	}
	s.wrote = true
	for k := range s.ResponseWriter.Header() {
		if strings.HasPrefix(strings.ToLower(k), "x-og-") {
			s.ResponseWriter.Header().Del(k)
		}
	}
}
func (s *stripWriter) WriteHeader(code int)        { s.strip(); s.ResponseWriter.WriteHeader(code) }
func (s *stripWriter) Write(b []byte) (int, error) { s.strip(); return s.ResponseWriter.Write(b) }
func (s *stripWriter) Flush() {
	s.strip()
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// SortedKeys returns the cache keys in order (diagnostics, tests).
func (c *Cache) SortedKeys() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	keys := make([]string, 0, len(c.entries))
	for k := range c.entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Prerender renders each request statically through next and adds the
// cacheable results to c. It returns the keys of uncacheable (dynamic)
// requests.
// Revalidate regenerates the page at path now, as Next's on-demand
// revalidation (res.revalidate) does: a static render that replaces the
// cached entries (with their companions). With onlyGenerated, a path that
// was never generated is left alone and reported as a 404. revalidated
// reports whether the render was cached; otherwise status is what the page
// answered.
func (h *Handler) Revalidate(ctx context.Context, path string, onlyGenerated bool) (status int, revalidated bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://localhost"+path, nil)
	if err != nil {
		return 0, false, err
	}
	req.Header.Set(PhaseHeader, "route") // like Next's revalidate: no middleware
	key := Key(req)
	c := h.Cache
	if onlyGenerated {
		c.mu.Lock()
		_, ok := c.entries[key]
		c.mu.Unlock()
		if !ok {
			return http.StatusNotFound, true, nil
		}
	}
	entries, ok, status := h.renderStatus(req)
	if !ok || rendered(entries, req) == nil {
		// Not cacheable (a dynamic page, or the static attempt failed):
		// Next renders it as a normal request; that render's status decides.
		if status == http.StatusServiceUnavailable {
			rec := newRecorder()
			h.Next.ServeHTTP(rec, req)
			status = rec.status
		}
		return status, false, nil
	}
	c.store(entries)
	return status, true, nil
}

func Prerender(c *Cache, next http.Handler, reqs []*http.Request) (dynamic []string) {
	h := &Handler{Cache: c, Next: next}
	for _, r := range reqs {
		// Like next build: prerendering does not run middleware.
		r.Header.Set(PhaseHeader, "route")
		key := Key(r)
		if entries, ok := h.render(r); ok && rendered(entries, r) != nil {
			c.store(entries)
		} else {
			dynamic = append(dynamic, key)
		}
	}
	return dynamic
}

// runMiddleware asks the app for its proxy's decision. It either writes a
// final response (done) or returns the request to render (rewritten URL,
// overridden headers) and headers to add to the eventual response.
func (h *Handler) runMiddleware(w http.ResponseWriter, r *http.Request) (*http.Request, [][2]string, bool) {
	req := r.Clone(r.Context())
	req.Header.Set(PhaseHeader, "middleware")
	rec := newRecorder()
	h.Next.ServeHTTP(rec, req)
	if rec.header.Get(MiddlewareHeader) != "continue" {
		// A redirect, a direct response, or a failure: send it as is.
		out := stripInternal(w)
		for k, vs := range rec.header {
			out.Header()[k] = vs
		}
		out.WriteHeader(rec.status)
		_, _ = out.Write(rec.body.Bytes())
		return nil, nil, true
	}
	next := r.Clone(r.Context())
	if u := rec.header.Get(MiddlewareURLHeader); u != "" {
		if parsed, err := url.Parse(u); err == nil {
			next.URL.Path, next.URL.RawPath, next.URL.RawQuery = parsed.Path, parsed.RawPath, parsed.RawQuery
		}
	}
	if raw := rec.header.Get(MiddlewareRequestHeadersHeader); raw != "" {
		var pairs [][2]string
		if json.Unmarshal([]byte(raw), &pairs) == nil {
			next.Header = http.Header{}
			for _, p := range pairs {
				// Host is the request's, not a header (Go keeps it apart; as a
				// header too, the app would read it twice).
				if strings.EqualFold(p[0], "host") {
					next.Host = p[1]
					continue
				}
				next.Header.Add(p[0], p[1])
			}
		}
	}
	var add [][2]string
	if raw := rec.header.Get(MiddlewareResponseHeadersHeader); raw != "" {
		_ = json.Unmarshal([]byte(raw), &add)
	}
	return next, add, false
}

// withHeaders applies middleware response headers just before the status
// line goes out (Set-Cookie appends; others replace).
func withHeaders(w http.ResponseWriter, add [][2]string) http.ResponseWriter {
	if len(add) == 0 {
		return w
	}
	return &headerWriter{ResponseWriter: w, add: add}
}

type headerWriter struct {
	http.ResponseWriter
	add  [][2]string
	done bool
}

func (h *headerWriter) apply() {
	if h.done {
		return
	}
	h.done = true
	hdr := h.ResponseWriter.Header()
	for _, kv := range h.add {
		if strings.EqualFold(kv[0], "set-cookie") {
			hdr.Add(kv[0], kv[1])
		} else {
			hdr.Set(kv[0], kv[1])
		}
	}
}
func (h *headerWriter) WriteHeader(code int)        { h.apply(); h.ResponseWriter.WriteHeader(code) }
func (h *headerWriter) Write(b []byte) (int, error) { h.apply(); return h.ResponseWriter.Write(b) }
func (h *headerWriter) Flush() {
	h.apply()
	if f, ok := h.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
