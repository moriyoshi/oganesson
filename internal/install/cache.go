package install

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Masterminds/semver/v3"
)

// Resolution speed (M11):
//
//   - metadata is fetched concurrently: a dependency's registry metadata is
//     requested when the dependency is queued (prefetch), so the
//     breadth-first resolution finds it ready instead of waiting for each
//     request in turn;
//   - with Options.CacheDir, metadata and tarballs persist between
//     installs (npm's cache does the same): metadata is reused while the
//     registry's max-age holds (300 s on registry.npmjs.org), then
//     revalidated with its ETag; tarballs are kept by integrity and
//     verified again on every use.

// metadataConcurrency bounds the metadata requests in flight.
const metadataConcurrency = 16

// metaCall is one metadata request: a package's whole metadata (spec "")
// or one version's manifest (spec a version or a dist-tag).
type metaCall struct {
	done chan struct{}
	p    *packument
	m    *manifest
	err  error
}

// request starts fetching name's metadata (spec "") or the manifest of
// name@spec unless it is fetched or in flight, and returns the request.
func (in *installer) request(ctx context.Context, name, spec string) *metaCall {
	key := name + "\x00" + spec
	in.mu.Lock()
	c, ok := in.calls[key]
	if !ok {
		c = &metaCall{done: make(chan struct{})}
		in.calls[key] = c
	}
	in.mu.Unlock()
	if !ok {
		go func() {
			defer close(c.done)
			select {
			case in.sem <- struct{}{}:
			case <-ctx.Done():
				c.err = ctx.Err()
				return
			}
			defer func() { <-in.sem }()
			if spec == "" {
				c.p, c.err = in.fetchPackument(ctx, name)
			} else {
				c.m, c.err = in.fetchManifest(ctx, name, spec)
			}
		}()
	}
	return c
}

func (c *metaCall) wait(ctx context.Context) error {
	select {
	case <-c.done:
		return c.err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// quickSpec is the one-version request that may answer name@rng: the
// version or dist-tag itself, else the latest version (a range).
func quickSpec(rng string) string {
	if rng == "" || rng == "*" {
		return "latest"
	}
	if _, err := semver.StrictNewVersion(rng); err == nil {
		return rng
	}
	if isTagName(rng) {
		return rng
	}
	return "latest"
}

// tagNameRe is what a dist-tag looks like ("latest", "canary", "rc").
var tagNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]*$`)

// isTagName reports a dist-tag: a name that is not a range ("x" is one).
func isTagName(s string) bool {
	if !tagNameRe.MatchString(s) {
		return false
	}
	_, err := semver.NewConstraint(s)
	return err != nil
}

// prefetch starts the request pick will make first for name@rng.
func (in *installer) prefetch(ctx context.Context, name, rng string) {
	in.request(ctx, name, quickSpec(rng))
}

// quickPick answers name@rng from one version's manifest when it can: the
// exact version or dist-tag asked for, or a latest version satisfying the
// range (pick prefers it then, as npm does).
func (in *installer) quickPick(ctx context.Context, name, rng string) (*manifest, bool) {
	spec := quickSpec(rng)
	c := in.request(ctx, name, spec)
	if c.wait(ctx) != nil || c.m == nil {
		return nil, false
	}
	if spec != "latest" || rng == "latest" || rng == "" || rng == "*" {
		return c.m, true
	}
	cons, err := constraint(rng)
	if err != nil {
		return nil, false
	}
	if v, err := semver.NewVersion(c.m.Version); err == nil && cons.Check(v) {
		return c.m, true
	}
	return nil, false
}

// versionManifest is name@version's manifest by itself.
func (in *installer) versionManifest(ctx context.Context, name, version string) (*manifest, error) {
	c := in.request(ctx, name, version)
	if err := c.wait(ctx); err != nil {
		return nil, err
	}
	return c.m, nil
}

// fetchManifest requests one version's (or dist-tag's) manifest.
func (in *installer) fetchManifest(ctx context.Context, name, spec string) (*manifest, error) {
	u := in.opts.Registry + "/" + strings.Replace(url.PathEscape(name), "%40", "@", 1) + "/" + url.PathEscape(spec)
	var body []byte
	err := retry(ctx, func() error {
		var err error
		body, err = in.cachedGet(ctx, u, "application/json")
		return err
	})
	if err != nil {
		return nil, err
	}
	var m manifest
	if err := json.Unmarshal(body, &m); err != nil || m.Version == "" || m.Dist.Tarball == "" {
		return nil, fmt.Errorf("install: %s@%s: no manifest", name, spec)
	}
	return &m, nil
}

// metaEntry is a cached metadata response's bookkeeping.
type metaEntry struct {
	ETag    string    `json:"etag"`
	Fetched time.Time `json:"fetched"`
	MaxAge  int       `json:"maxAge"` // seconds
}

func cacheKey(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// cachedGet is the body of a metadata GET of u (accepting accept), from
// the cache when it is fresh, revalidated when it is stale, fetched
// otherwise. Errors are retry's (statusError). A version's manifest is
// only served as JSON: registry.npmjs.org answers the abbreviated
// metadata's type there with 406.
func (in *installer) cachedGet(ctx context.Context, u, accept string) ([]byte, error) {
	var bodyFile, entryFile string
	var entry metaEntry
	var cached []byte
	if dir := in.opts.CacheDir; dir != "" {
		key := cacheKey(u)
		bodyFile = filepath.Join(dir, "metadata", key+".json")
		entryFile = filepath.Join(dir, "metadata", key+".entry")
		if b, err := os.ReadFile(entryFile); err == nil && json.Unmarshal(b, &entry) == nil {
			if body, err := os.ReadFile(bodyFile); err == nil {
				cached = body
				if time.Since(entry.Fetched) < time.Duration(entry.MaxAge)*time.Second {
					return cached, nil
				}
			}
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, permanent{err}
	}
	req.Header.Set("Accept", accept)
	if cached != nil && entry.ETag != "" {
		req.Header.Set("If-None-Match", entry.ETag)
	}
	resp, err := in.opts.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotModified && cached != nil:
		entry.Fetched, entry.MaxAge = time.Now(), maxAge(resp)
		writeFileAtomic(entryFile, mustMarshal(entry))
		return cached, nil
	case resp.StatusCode != http.StatusOK:
		return nil, statusError(u, resp)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if bodyFile != "" {
		writeFileAtomic(bodyFile, body)
		writeFileAtomic(entryFile, mustMarshal(metaEntry{ETag: resp.Header.Get("ETag"), Fetched: time.Now(), MaxAge: maxAge(resp)}))
	}
	return body, nil
}

// maxAge is a response's Cache-Control max-age in seconds (0 without one).
func maxAge(resp *http.Response) int {
	for _, part := range strings.Split(resp.Header.Get("Cache-Control"), ",") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(part), "max-age="); ok {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				return n
			}
		}
	}
	return 0
}

func mustMarshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// writeFileAtomic writes a cache file through a temporary one; a cache
// that cannot be written is only slower.
func writeFileAtomic(path string, data []byte) {
	if path == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil || os.Rename(f.Name(), path) != nil {
		_ = os.Remove(f.Name())
	}
}

// tarballCachePath is where a package's tarball is kept, by its integrity
// ("" without a cache or integrity).
func (in *installer) tarballCachePath(m *manifest) string {
	if in.opts.CacheDir == "" || m.Dist.Integrity == "" && m.Dist.Shasum == "" {
		return ""
	}
	return filepath.Join(in.opts.CacheDir, "tarballs", cacheKey(m.Dist.Integrity+"|"+m.Dist.Shasum)+".tgz")
}

// fetchCall is one package's download (fetch).
type fetchCall struct {
	done chan struct{}
	dir  string // its staging directory, "" when already installed
	err  error
}

// startFetch starts downloading n's package into the staging area unless
// it is started, and returns the download.
func (in *installer) startFetch(ctx context.Context, n *node) *fetchCall {
	in.mu.Lock()
	c, ok := in.fetches[n]
	if !ok {
		c = &fetchCall{done: make(chan struct{})}
		in.fetches[n] = c
	}
	tmp := filepath.Join(in.staging, strconv.Itoa(len(in.fetches)))
	in.mu.Unlock()
	if !ok {
		go func() {
			defer close(c.done)
			select {
			case in.fetchSem <- struct{}{}:
			case <-ctx.Done():
				c.err = ctx.Err()
				return
			}
			defer func() { <-in.fetchSem }()
			c.dir, c.err = in.fetch(ctx, in.root, tmp, n)
		}()
	}
	return c
}

// stopFetches cancels the downloads still running and waits for them.
func (in *installer) stopFetches(cancel context.CancelFunc) {
	cancel()
	in.mu.Lock()
	calls := make([]*fetchCall, 0, len(in.fetches))
	for _, c := range in.fetches {
		calls = append(calls, c)
	}
	in.mu.Unlock()
	for _, c := range calls {
		<-c.done
	}
}
