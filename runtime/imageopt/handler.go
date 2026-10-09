package imageopt

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// This file ports the /_next/image request path: next-server's
// handleNextImageRequest, ImageOptimizerCache.validateParams, the upstream
// fetchers and imageOptimizer, and sendResponse.
//
// Differences from Next: the cache is in memory (Next keeps .next/cache/images
// on disk), AVIF output is unsupported (the optimizer then falls back to the
// upstream image, as Next does when sharp fails), and glob patterns use a
// port of the subset of picomatch that image patterns need.

// Config is next.config's `images`, as finalized by Next's loadConfig.
type Config struct {
	DeviceSizes             []int           `json:"deviceSizes"`
	ImageSizes              []int           `json:"imageSizes"`
	Qualities               []int           `json:"qualities"`
	Formats                 []string        `json:"formats"`
	Domains                 []string        `json:"domains"`
	RemotePatterns          []RemotePattern `json:"remotePatterns"`
	LocalPatterns           []LocalPattern  `json:"localPatterns"`
	MinimumCacheTTL         *int            `json:"minimumCacheTTL"`
	DangerouslyAllowSVG     bool            `json:"dangerouslyAllowSVG"`
	DangerouslyAllowLocalIP bool            `json:"dangerouslyAllowLocalIP"`
	ContentSecurityPolicy   string          `json:"contentSecurityPolicy"`
	ContentDispositionType  string          `json:"contentDispositionType"`
	MaximumResponseBody     int64           `json:"maximumResponseBody"`
	Loader                  string          `json:"loader"`
	Path                    string          `json:"path"`
	Unoptimized             bool            `json:"unoptimized"`
}

// RemotePattern is an images.remotePatterns entry. A URL-string entry is
// converted by Next's config finalization before it gets here.
type RemotePattern struct {
	Protocol *string `json:"protocol"`
	Hostname *string `json:"hostname"`
	Port     *string `json:"port"`
	Pathname *string `json:"pathname"`
	Search   *string `json:"search"`
}

// LocalPattern is an images.localPatterns entry.
type LocalPattern struct {
	Pathname *string `json:"pathname"`
	Search   *string `json:"search"`
}

const (
	cacheVersion  = 4
	blurImgSize   = 8
	blurQuality   = 70
	upstreamLimit = 3 // redirects
)

var upstreamTimeout = 7 * time.Second

// Handler serves /_next/image. App fetches local images (the whole
// application, like Next's handleRequest); Fallback renders the 404 when the
// default loader is disabled.
type Handler struct {
	Config   Config
	BasePath string
	App      http.Handler
	Fallback http.Handler
	Logger   *slog.Logger

	mu    sync.Mutex
	cache map[string]*entry
	// Resolver overrides DNS for remote images (tests).
	Resolver func(ctx context.Context, host string) ([]netip.Addr, error)
}

type entry struct {
	buf          []byte
	contentType  string
	etag         string
	upstreamEtag string
	maxAge       int
	expires      time.Time
	refreshing   bool
}

type params struct {
	href       string
	isAbsolute bool
	isStatic   bool
	width      int
	quality    int
	mimeType   string
}

// ImageError carries the status and message Next sends for a failed image.
type ImageError struct {
	Status  int
	Message string
}

func (e *ImageError) Error() string { return e.Message }

func (h *Handler) logf(msg string, args ...any) {
	if h.Logger != nil {
		h.Logger.Error(fmt.Sprintf(msg, args...))
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cfg := h.Config
	if (cfg.Loader != "" && cfg.Loader != "default") || cfg.Unoptimized {
		h.Fallback.ServeHTTP(w, r)
		return
	}
	p, msg := h.validate(r)
	if msg != "" {
		textError(w, http.StatusBadRequest, msg)
		return
	}
	key := cacheKey(p)
	h.mu.Lock()
	if h.cache == nil {
		h.cache = map[string]*entry{}
	}
	e, xcache := h.cache[key], "HIT"
	if e != nil && time.Now().After(e.expires) {
		xcache = "STALE"
		if !e.refreshing { // serve stale while revalidating, like Next's response cache
			e.refreshing = true
			go func() {
				ne, err := h.produce(r, p, e)
				h.mu.Lock()
				defer h.mu.Unlock()
				e.refreshing = false
				if err == nil {
					h.cache[key] = ne
				}
			}()
		}
	}
	h.mu.Unlock()
	if e == nil {
		var err error
		if e, err = h.produce(r, p, nil); err != nil {
			var ie *ImageError
			if errors.As(err, &ie) {
				textError(w, ie.Status, ie.Message)
				return
			}
			h.logf("image optimizer: %v", err)
			textError(w, http.StatusInternalServerError, "Internal Server Error")
			return
		}
		xcache = "MISS"
		h.mu.Lock()
		h.cache[key] = e
		h.mu.Unlock()
	}
	h.send(w, r, p, e, xcache)
}

func textError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(status)
	io.WriteString(w, msg)
}

func (h *Handler) validate(r *http.Request) (*params, string) {
	cfg := h.Config
	q := r.URL.Query()
	u := q["url"]
	switch {
	case len(u) == 0 || u[0] == "":
		return nil, `"url" parameter is required`
	case len(u) > 1:
		return nil, `"url" parameter cannot be an array`
	}
	raw := u[0]
	if len(raw) > 3072 {
		return nil, `"url" parameter is too long`
	}
	if strings.HasPrefix(raw, "//") {
		return nil, `"url" parameter cannot be a protocol-relative URL (//)`
	}
	p := &params{}
	if strings.HasPrefix(raw, "/") {
		p.href = raw
		pu, _ := url.Parse(raw)
		pathname := ""
		if pu != nil {
			pathname, _ = url.PathUnescape(pu.Path)
		}
		if recursiveRe.MatchString(pathname) {
			return nil, `"url" parameter cannot be recursive`
		}
		if !hasLocalMatch(cfg.LocalPatterns, raw) {
			return nil, `"url" parameter is not allowed`
		}
	} else {
		pu, err := url.Parse(raw)
		if err != nil || !pu.IsAbs() || pu.Host == "" {
			return nil, `"url" parameter is invalid`
		}
		if pu.Scheme != "http" && pu.Scheme != "https" {
			return nil, `"url" parameter is invalid`
		}
		p.href, p.isAbsolute = pu.String(), true
		if !hasRemoteMatch(cfg.Domains, cfg.RemotePatterns, pu) {
			return nil, `"url" parameter is not allowed`
		}
	}
	ws := q["w"]
	switch {
	case len(ws) == 0 || ws[0] == "":
		return nil, `"w" parameter (width) is required`
	case len(ws) > 1:
		return nil, `"w" parameter (width) cannot be an array`
	case !digits.MatchString(ws[0]):
		return nil, `"w" parameter (width) must be an integer greater than 0`
	}
	qs := q["q"]
	switch {
	case len(qs) == 0 || qs[0] == "":
		return nil, `"q" parameter (quality) is required`
	case len(qs) > 1:
		return nil, `"q" parameter (quality) cannot be an array`
	case !digits.MatchString(qs[0]):
		return nil, `"q" parameter (quality) must be an integer between 1 and 100`
	}
	p.width, _ = strconv.Atoi(ws[0])
	if p.width <= 0 {
		return nil, `"w" parameter (width) must be an integer greater than 0`
	}
	if !slices.Contains(cfg.DeviceSizes, p.width) && !slices.Contains(cfg.ImageSizes, p.width) {
		return nil, fmt.Sprintf(`"w" parameter (width) of %d is not allowed`, p.width)
	}
	p.quality, _ = strconv.Atoi(qs[0])
	if p.quality < 1 || p.quality > 100 {
		return nil, `"q" parameter (quality) must be an integer between 1 and 100`
	}
	if cfg.Qualities != nil && !slices.Contains(cfg.Qualities, p.quality) {
		return nil, fmt.Sprintf(`"q" parameter (quality) of %s is not allowed`, qs[0])
	}
	formats := cfg.Formats
	if formats == nil {
		formats = []string{WEBP}
	}
	p.mimeType = supportedMimeType(formats, r.Header.Get("Accept"))
	p.isStatic = strings.HasPrefix(raw, h.BasePath+"/_next/static/media") || strings.HasPrefix(raw, h.BasePath+"/_next/static/immutable/media")
	return p, ""
}

var (
	recursiveRe = regexp.MustCompile(`/_next/image($|/)`)
	digits      = regexp.MustCompile(`^[0-9]+$`)
)

// supportedMimeType ports getSupportedMimeType (the `accept` package's
// mediaType, then the substring check): the first of formats the Accept
// header ranks highest.
func supportedMimeType(formats []string, accept string) string {
	best, bestQ := "", 0.0
	for _, f := range formats {
		fq := 0.0
		for _, part := range strings.Split(accept, ",") {
			mt, ps, err := mime.ParseMediaType(strings.TrimSpace(part))
			if err != nil {
				continue
			}
			typ, sub, _ := strings.Cut(mt, "/")
			ft, fs, _ := strings.Cut(f, "/")
			if (typ != "*" && typ != ft) || (sub != "*" && sub != fs) {
				continue
			}
			qv := 1.0
			if s, ok := ps["q"]; ok {
				qv, _ = strconv.ParseFloat(s, 64)
			}
			fq = max(fq, qv)
		}
		if fq > bestQ {
			best, bestQ = f, fq
		}
	}
	if best == "" || !strings.Contains(accept, best) {
		return ""
	}
	return best
}

func cacheKey(p *params) string {
	s := sha256.New()
	for _, item := range []string{strconv.Itoa(cacheVersion), p.href, strconv.Itoa(p.width), strconv.Itoa(p.quality), p.mimeType} {
		s.Write([]byte(item))
	}
	return base64.RawURLEncoding.EncodeToString(s.Sum(nil))
}

func etagOf(b []byte) string {
	sum := sha256.Sum256(b)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

type upstream struct {
	buf          []byte
	contentType  string
	cacheControl string
	etag         string
}

// produce fetches and optimizes the image (imageOptimizer).
func (h *Handler) produce(r *http.Request, p *params, prev *entry) (*entry, error) {
	var up *upstream
	var err error
	if p.isAbsolute {
		up, err = h.fetchExternal(r.Context(), p.href, upstreamLimit)
	} else {
		up, err = h.fetchInternal(r, p.href)
	}
	if err != nil {
		return nil, err
	}
	cfg := h.Config
	minTTL := 14400
	if cfg.MinimumCacheTTL != nil {
		minTTL = *cfg.MinimumCacheTTL
	}
	maxAge := max(minTTL, maxAgeOf(up.cacheControl))
	newEntry := func(buf []byte, ct, etag string, age int) *entry {
		return &entry{buf: buf, contentType: ct, etag: etag, upstreamEtag: up.etag, maxAge: age, expires: time.Now().Add(time.Duration(age) * time.Second)}
	}
	upType := DetectContentType(up.buf)
	if upType == "" || !strings.HasPrefix(upType, "image/") || strings.Contains(upType, ",") {
		h.logf("The requested resource isn't a valid image for %s received %s", p.href, upType)
		return nil, &ImageError{400, "The requested resource isn't a valid image."}
	}
	if strings.HasPrefix(upType, "image/svg") && !cfg.DangerouslyAllowSVG {
		h.logf(`The requested resource %q has type %q but dangerouslyAllowSVG is disabled. Consider adding the "unoptimized" property to the <Image>.`, p.href, upType)
		return nil, &ImageError{400, `"url" parameter is valid but image type is not allowed`}
	}
	if (slices.Contains(AnimatableTypes, upType) && IsAnimated(up.buf)) || slices.Contains(BypassTypes, upType) {
		return newEntry(up.buf, upType, up.etag, maxAge), nil
	}
	contentType := p.mimeType
	if contentType == "" {
		if Extension(upType) != "" && upType != WEBP && upType != AVIF {
			contentType = upType
		} else {
			contentType = JPEG
		}
	}
	// getPreviouslyCachedImageOrNull: an unchanged upstream reuses the result.
	if prev != nil && prev.upstreamEtag != prev.etag && up.etag == prev.upstreamEtag {
		return newEntry(prev.buf, contentType, prev.etag, maxAge), nil
	}
	out, err := Optimize(up.buf, contentType, p.quality, p.width, 0)
	if err != nil {
		// Like Next: fall back to the upstream image.
		return newEntry(up.buf, upType, up.etag, minTTL), nil
	}
	return newEntry(out, contentType, etagOf(out), maxAge), nil
}

func maxAgeOf(cc string) int {
	m := map[string]string{}
	if cc == "" {
		return 0
	}
	for _, d := range strings.Split(cc, ",") {
		k, v, _ := strings.Cut(strings.TrimSpace(d), "=")
		m[strings.ToLower(k)] = strings.ToLower(v)
	}
	age := m["s-maxage"]
	if age == "" {
		age = m["max-age"]
	}
	age = strings.Trim(age, `"`)
	n, err := strconv.Atoi(leadingInt(age))
	if err != nil {
		return 0
	}
	return n
}

func leadingInt(s string) string {
	i := 0
	if i < len(s) && (s[i] == '-' || s[i] == '+') {
		i++
	}
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return s[:i]
}

func (h *Handler) maxBody() int64 {
	if h.Config.MaximumResponseBody > 0 {
		return h.Config.MaximumResponseBody
	}
	return 50_000_000
}

// fetchInternal ports fetchInternalImage: the application itself serves
// the URL (public files, build assets, route handlers).
func (h *Handler) fetchInternal(r *http.Request, href string) (*upstream, error) {
	req := httptest.NewRequest(http.MethodGet, href, nil).WithContext(r.Context())
	req.Host = r.Host
	rec := httptest.NewRecorder()
	h.App.ServeHTTP(rec, req)
	res := rec.Result()
	if res.StatusCode < 200 || res.StatusCode > 299 {
		h.logf("image response failed for %s %d", href, res.StatusCode)
		return nil, &ImageError{res.StatusCode, `"url" parameter is valid but internal response is invalid`}
	}
	body := rec.Body.Bytes()
	if len(body) == 0 {
		h.logf("internal image response is empty for %s", href)
		return nil, &ImageError{400, `"url" parameter is valid but internal response is invalid`}
	}
	if int64(len(body)) > h.maxBody() {
		h.logf("internal image response exceeded maximum size for %s", href)
		return nil, &ImageError{413, `"url" parameter is valid but internal response is invalid`}
	}
	return &upstream{buf: body, contentType: res.Header.Get("Content-Type"), cacheControl: res.Header.Get("Cache-Control"), etag: extractEtag(res.Header.Get("ETag"), body)}, nil
}

func extractEtag(etag string, body []byte) string {
	if etag != "" {
		return base64.RawURLEncoding.EncodeToString([]byte(etag))
	}
	return etagOf(body)
}

// fetchExternal ports fetchExternalImage: unless dangerouslyAllowLocalIP,
// the host must not resolve to a private address, and the connection is
// pinned to the addresses that were checked.
func (h *Handler) fetchExternal(ctx context.Context, href string, redirects int) (*upstream, error) {
	u, err := url.Parse(href)
	if err != nil {
		return nil, &ImageError{400, `"url" parameter is invalid`}
	}
	host := strings.Trim(u.Hostname(), "[]")
	var pinned []netip.Addr
	if !h.Config.DangerouslyAllowLocalIP {
		if a, err := netip.ParseAddr(host); err == nil {
			pinned = []netip.Addr{a}
		} else {
			resolve := h.Resolver
			if resolve == nil {
				resolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
					return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
				}
			}
			if pinned, err = resolve(ctx, host); err != nil {
				h.logf("upstream image response failed for %s %v", href, err)
				return nil, &ImageError{500, `"url" parameter is valid but upstream response is invalid`}
			}
		}
		for _, a := range pinned {
			if isPrivateIP(a) {
				h.logf("upstream image %s hostname resolved to private IP %s. If this is expected and you understand SSRF risk, use images.dangerouslyAllowLocalIP = true to continue.", href, a)
				return nil, &ImageError{400, `"url" parameter is not allowed`}
			}
		}
	}
	dialer := &net.Dialer{Timeout: upstreamTimeout}
	transport := &http.Transport{
		Proxy:              nil,
		DisableCompression: false,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if len(pinned) == 0 || h.Config.DangerouslyAllowLocalIP {
				return dialer.DialContext(ctx, network, addr)
			}
			_, port, _ := net.SplitHostPort(addr)
			var last error
			for _, a := range pinned {
				c, err := dialer.DialContext(ctx, network, net.JoinHostPort(a.String(), port))
				if err == nil {
					return c, nil
				}
				last = err
			}
			return nil, last
		},
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     transport,
		Timeout:       upstreamTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, href, nil)
	res, err := client.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, syscall.ETIMEDOUT) || isTimeout(err) {
			h.logf("upstream image response timed out for %s", href)
			return nil, &ImageError{504, `"url" parameter is valid but upstream response timed out`}
		}
		h.logf("upstream image response failed for %s %v", href, err)
		return nil, &ImageError{500, `"url" parameter is valid but upstream response is invalid`}
	}
	defer res.Body.Close()
	if loc := res.Header.Get("Location"); isRedirect(res.StatusCode) && loc != "" {
		if next, err := u.Parse(loc); err == nil {
			if redirects == 0 {
				h.logf("upstream image response had too many redirects %s", href)
				return nil, &ImageError{508, `"url" parameter is valid but upstream response is invalid`}
			}
			return h.fetchExternal(ctx, next.String(), redirects-1)
		}
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		h.logf("upstream image response failed for %s %d", href, res.StatusCode)
		return nil, &ImageError{res.StatusCode, `"url" parameter is valid but upstream response is invalid`}
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, h.maxBody()+1))
	if err != nil {
		h.logf("upstream image response failed for %s %v", href, err)
		return nil, &ImageError{500, `"url" parameter is valid but upstream response is invalid`}
	}
	if int64(len(body)) > h.maxBody() {
		h.logf("upstream image response exceeded maximum size for %s %d", href, len(body))
		return nil, &ImageError{413, `"url" parameter is valid but upstream response is invalid`}
	}
	if len(body) == 0 {
		h.logf("upstream image response is empty for %s", href)
		return nil, &ImageError{400, `"url" parameter is valid but upstream response is invalid`}
	}
	return &upstream{buf: body, contentType: res.Header.Get("Content-Type"), cacheControl: res.Header.Get("Cache-Control"), etag: extractEtag(res.Header.Get("ETag"), body)}, nil
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

func isRedirect(code int) bool {
	return code == 301 || code == 302 || code == 303 || code == 307 || code == 308
}

// isPrivateIP mirrors next/dist/server/is-private-ip: any address whose
// ipaddr.js range is not "unicast".
func isPrivateIP(a netip.Addr) bool {
	a = a.Unmap()
	if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsUnspecified() || a.IsMulticast() || a.IsInterfaceLocalMulticast() {
		return true
	}
	for _, p := range reservedPrefixes {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

var reservedPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "192.88.99.0/24",
		"192.175.48.0/24", "192.31.196.0/24", "192.52.193.0/24", "198.18.0.0/15",
		"198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "255.255.255.255/32",
		"::ffff:0:0:0/96", "64:ff9b::/96", "64:ff9b:1::/48", "100::/64", "2001::/23",
		"2001:db8::/32", "2002::/16", "fc00::/7",
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// send ports setResponseHeaders and sendResponse.
func (h *Handler) send(w http.ResponseWriter, r *http.Request, p *params, e *entry, xcache string) {
	hd := w.Header()
	hd.Set("Vary", "Accept")
	if p.isStatic {
		hd.Set("Cache-Control", "public, max-age=315360000, immutable")
	} else {
		hd.Set("Cache-Control", fmt.Sprintf("public, max-age=%d, must-revalidate", e.maxAge))
	}
	if e.etag != "" {
		hd.Set("ETag", e.etag)
	}
	if inm := r.Header.Get("If-None-Match"); inm != "" && etagMatches(inm, e.etag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	ct := e.contentType
	if Extension(ct) == "" {
		ct = ""
	}
	if ct != "" {
		hd.Set("Content-Type", ct)
	}
	disp := h.Config.ContentDispositionType
	if disp == "" {
		disp = "attachment"
	}
	// The content-disposition package always quotes the filename.
	name := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(fileNameWithExtension(p.href, ct))
	hd.Set("Content-Disposition", disp+`; filename="`+name+`"`)
	if h.Config.ContentSecurityPolicy != "" {
		hd.Set("Content-Security-Policy", h.Config.ContentSecurityPolicy)
	}
	hd.Set("X-Nextjs-Cache", xcache)
	hd.Set("Content-Length", strconv.Itoa(len(e.buf)))
	if r.Method == http.MethodHead {
		return
	}
	io.Copy(w, bytes.NewReader(e.buf))
}

// etagMatches follows the `fresh` package's If-None-Match comparison.
func etagMatches(inm, etag string) bool {
	if strings.TrimSpace(inm) == "*" {
		return true
	}
	for _, t := range strings.Split(inm, ",") {
		t = strings.TrimSpace(t)
		if t == etag || t == "W/"+etag || "W/"+t == etag {
			return true
		}
	}
	return false
}

func fileNameWithExtension(href, contentType string) string {
	base, _, _ := strings.Cut(href, "?")
	name := base[strings.LastIndex(base, "/")+1:]
	if contentType == "" || name == "" {
		return "image.bin"
	}
	name, _, _ = strings.Cut(name, ".")
	return name + "." + Extension(contentType)
}
