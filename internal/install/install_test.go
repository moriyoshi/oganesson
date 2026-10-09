package install

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakePkg struct {
	name, version  string
	deps, optional map[string]string
	peers          map[string]string
	optionalPeers  []string // peerDependenciesMeta optional
	cpu            []string
	corrupt        bool
	bundled        []string // shipped in the tarball's node_modules
}

func tarball(t *testing.T, p fakePkg) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	type file struct {
		name string
		body []byte
	}
	pj, _ := json.Marshal(map[string]any{"name": p.name, "version": p.version, "dependencies": p.deps})
	// A fixed order keeps the bytes (and so the integrity) reproducible.
	files := []file{{"package/package.json", pj}, {"package/index.js", []byte("module.exports = 1;")}}
	for _, b := range p.bundled {
		bj, _ := json.Marshal(map[string]any{"name": b, "version": "9.9.9"})
		files = append(files, file{"package/node_modules/" + b + "/package.json", bj})
	}
	for _, f := range files {
		_ = tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o644, Size: int64(len(f.body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write(f.body)
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func fakeRegistry(t *testing.T, pkgs []fakePkg) *httptest.Server {
	t.Helper()
	tarballs := map[string][]byte{}
	docs := map[string]map[string]any{}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b, ok := tarballs[r.URL.Path]; ok {
			_, _ = w.Write(b)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/")
		doc, ok := docs[name]
		if !ok {
			// /<name>/<version or dist-tag>: one version's manifest, as
			// the npm registry serves it (as JSON only: it answers the
			// abbreviated metadata's Accept with 406).
			if i := strings.LastIndex(name, "/"); i > 0 {
				if d, ok := docs[name[:i]]; ok {
					if strings.Contains(r.Header.Get("Accept"), "install-v1") {
						http.Error(w, "not acceptable", http.StatusNotAcceptable)
						return
					}
					spec := name[i+1:]
					if v, ok := d["dist-tags"].(map[string]string)[spec]; ok {
						spec = v
					}
					if m, ok := d["versions"].(map[string]any)[spec]; ok {
						_ = json.NewEncoder(w).Encode(m)
						return
					}
				}
			}
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(doc)
	}))
	for _, p := range pkgs {
		tb := tarball(t, p)
		path := "/tarballs/" + p.name + "-" + p.version + ".tgz"
		tarballs[path] = tb
		sum := sha512.Sum512(tb)
		if p.corrupt {
			sum[0] ^= 0xff
		}
		doc := docs[p.name]
		if doc == nil {
			doc = map[string]any{"name": p.name, "dist-tags": map[string]string{}, "versions": map[string]any{}}
			docs[p.name] = doc
		}
		peersMeta := map[string]any{}
		for _, n := range p.optionalPeers {
			peersMeta[n] = map[string]bool{"optional": true}
		}
		doc["versions"].(map[string]any)[p.version] = map[string]any{
			"name": p.name, "version": p.version, "dependencies": p.deps, "optionalDependencies": p.optional, "cpu": p.cpu, "bundleDependencies": p.bundled,
			"peerDependencies": p.peers, "peerDependenciesMeta": peersMeta,
			"dist": map[string]string{"tarball": srv.URL + path, "integrity": "sha512-" + base64.StdEncoding.EncodeToString(sum[:])},
		}
		doc["dist-tags"].(map[string]string)["latest"] = p.version
	}
	t.Cleanup(srv.Close)
	return srv
}

func project(t *testing.T, deps map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	b, _ := json.Marshal(map[string]any{"name": "root", "dependencies": deps})
	if err := os.WriteFile(filepath.Join(dir, "package.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func installedVersion(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return ""
	}
	var m manifest
	_ = json.Unmarshal(b, &m)
	return m.Version
}

func TestHoistingAndConflicts(t *testing.T) {
	srv := fakeRegistry(t, []fakePkg{
		{name: "a", version: "1.0.0", deps: map[string]string{"shared": "^1.0.0"}},
		{name: "b", version: "1.0.0", deps: map[string]string{"shared": "^2.0.0"}, optional: map[string]string{"native": "1.0.0", "portable": "1.0.0"}},
		{name: "shared", version: "1.2.0"},
		{name: "shared", version: "1.5.0"},
		{name: "shared", version: "2.1.0"},
		{name: "native", version: "1.0.0", cpu: []string{"x64"}},
		{name: "portable", version: "1.0.0"},
	})
	dir := project(t, map[string]string{"a": "1.0.0", "b": "^1"})
	if err := Install(context.Background(), dir, Options{Registry: srv.URL}); err != nil {
		t.Fatal(err)
	}
	nm := filepath.Join(dir, "node_modules")
	// "shared" is latest 2.1.0, which does not satisfy a's ^1; resolution
	// picks the highest ^1 match and hoists it (a is processed first).
	for path, want := range map[string]string{
		"a":                     "1.0.0",
		"b":                     "1.0.0",
		"shared":                "1.5.0",
		"b/node_modules/shared": "2.1.0",
		"native":                "",      // optional and platform-bound: og runs no native code
		"portable":              "1.0.0", // optional and portable: installed, as npm does
		"a/node_modules/shared": "",
	} {
		if got := installedVersion(t, filepath.Join(nm, path)); got != want {
			t.Errorf("%s: version %q, want %q", path, got, want)
		}
	}
}

// A Prisma project (one that depends on @prisma/client) gets
// @prisma/schema-engine-wasm added implicitly, pinned to the exact
// @prisma/engines-version @prisma/client depends on, so og's app-startup
// `migrate deploy` has the WASM schema engine without the user declaring it.
func TestPrismaSchemaEngineInjected(t *testing.T) {
	const engineVer = "7.10.0-4.abcdef"
	srv := fakeRegistry(t, []fakePkg{
		{name: "@prisma/client", version: "7.10.0", deps: map[string]string{"@prisma/engines-version": engineVer}},
		{name: "@prisma/engines-version", version: engineVer},
		{name: "@prisma/schema-engine-wasm", version: engineVer},
	})
	dir := project(t, map[string]string{"@prisma/client": "7.10.0"})
	if err := Install(context.Background(), dir, Options{Registry: srv.URL}); err != nil {
		t.Fatal(err)
	}
	nm := filepath.Join(dir, "node_modules")
	if got := installedVersion(t, filepath.Join(nm, "@prisma/schema-engine-wasm")); got != engineVer {
		t.Errorf("@prisma/schema-engine-wasm: version %q, want %q", got, engineVer)
	}
}

// Prisma 7 moved @prisma/engines-version out of @prisma/client's dependencies
// into its devDependencies, which the registry's abbreviated metadata omits. og
// must still find the engine version (and inject the WASM schema engine) by
// reading it from @prisma/engines, which ships at the same version and keeps it
// in dependencies. Without this, a clean install of a Prisma 7 app silently
// ships no schema engine and app-startup `migrate deploy` has nothing to run.
func TestPrismaSchemaEngineInjectedPrisma7(t *testing.T) {
	const engineVer = "7.10.0-4.abcdef"
	srv := fakeRegistry(t, []fakePkg{
		{name: "@prisma/client", version: "7.10.0"}, // engines-version only in devDependencies (not served)
		{name: "@prisma/engines", version: "7.10.0", deps: map[string]string{"@prisma/engines-version": engineVer}},
		{name: "@prisma/engines-version", version: engineVer},
		{name: "@prisma/schema-engine-wasm", version: engineVer},
	})
	dir := project(t, map[string]string{"@prisma/client": "7.10.0"})
	if err := Install(context.Background(), dir, Options{Registry: srv.URL}); err != nil {
		t.Fatal(err)
	}
	nm := filepath.Join(dir, "node_modules")
	if got := installedVersion(t, filepath.Join(nm, "@prisma/schema-engine-wasm")); got != engineVer {
		t.Errorf("@prisma/schema-engine-wasm: version %q, want %q", got, engineVer)
	}
}

// Without @prisma/client the engine is not added; and an explicit declaration
// (any version) is left untouched rather than overridden.
func TestPrismaSchemaEngineNotInjected(t *testing.T) {
	srv := fakeRegistry(t, []fakePkg{
		{name: "left-pad", version: "1.0.0"},
		{name: "@prisma/schema-engine-wasm", version: "1.2.3"},
	})
	dir := project(t, map[string]string{"left-pad": "1.0.0"})
	if err := Install(context.Background(), dir, Options{Registry: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if got := installedVersion(t, filepath.Join(dir, "node_modules", "@prisma/schema-engine-wasm")); got != "" {
		t.Errorf("schema-engine-wasm installed (%q) without @prisma/client", got)
	}
}

func TestIntegrityMismatch(t *testing.T) {
	srv := fakeRegistry(t, []fakePkg{{name: "bad", version: "1.0.0", corrupt: true}})
	dir := project(t, map[string]string{"bad": "1.0.0"})
	err := Install(context.Background(), dir, Options{Registry: srv.URL})
	if err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "node_modules", "bad")); !os.IsNotExist(err) {
		t.Fatal("corrupt package was installed")
	}
}

func TestUnsupportedSpecs(t *testing.T) {
	srv := fakeRegistry(t, nil)
	for _, spec := range []string{"file:../x", "workspace:*", "github:a/b"} {
		dir := project(t, map[string]string{"x": spec})
		if err := Install(context.Background(), dir, Options{Registry: srv.URL}); err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Errorf("%s: err = %v", spec, err)
		}
	}
}

// npm aliases ("npm:<package>@<range>") install the target package under
// the alias name, beside the real one at another version.
func TestNpmAlias(t *testing.T) {
	srv := fakeRegistry(t, []fakePkg{
		{name: "h3", version: "1.15.0"},
		{name: "h3", version: "2.0.1-rc.20"},
		{name: "@scope/lib", version: "3.1.0"},
		{name: "uses-alias", version: "1.0.0", deps: map[string]string{"lib-alias": "npm:@scope/lib@^3"}},
	})
	// The registry's latest h3 is 2.0.1-rc.20; the root asks for ^1.
	dir := project(t, map[string]string{"h3": "^1.0.0", "h3-v2": "npm:h3@2.0.1-rc.20", "uses-alias": "1.0.0", "bare": "npm:h3"})
	if err := Install(context.Background(), dir, Options{Registry: srv.URL}); err != nil {
		t.Fatal(err)
	}
	nm := filepath.Join(dir, "node_modules")
	for path, want := range map[string]string{
		"h3":        "1.15.0",
		"h3-v2":     "2.0.1-rc.20",
		"lib-alias": "3.1.0",
		"bare":      "2.0.1-rc.20",
	} {
		if got := installedVersion(t, filepath.Join(nm, path)); got != want {
			t.Errorf("%s: version %q, want %q", path, got, want)
		}
	}
}

// Optional dependencies are native builds and skipped, except WebAssembly
// builds (cpu wasm32) and the wasm substitutes of known native addons.
func TestWasmBuilds(t *testing.T) {
	srv := fakeRegistry(t, []fakePkg{
		{name: "addon", version: "1.0.0", optional: map[string]string{
			"addon-darwin-arm64": "1.0.0", "addon-wasm32-wasi": "1.0.0", "addon-wasm32-fake": "1.0.0", "addon-wasm32-missing": "1.0.0",
		}},
		{name: "addon-darwin-arm64", version: "1.0.0", cpu: []string{"arm64"}},
		{name: "addon-wasm32-wasi", version: "1.0.0", cpu: []string{"wasm32"}},
		{name: "addon-wasm32-fake", version: "1.0.0", cpu: []string{"x64"}},
		{name: "lightningcss", version: "1.32.0"},
		{name: "lightningcss-wasm", version: "1.31.0"},
		{name: "lightningcss-wasm", version: "1.32.0"},
	})
	dir := project(t, map[string]string{"addon": "1.0.0", "lightningcss": "^1"})
	if err := Install(context.Background(), dir, Options{Registry: srv.URL}); err != nil {
		t.Fatal(err)
	}
	nm := filepath.Join(dir, "node_modules")
	for path, want := range map[string]string{
		"addon":                "1.0.0",
		"addon-wasm32-wasi":    "1.0.0",
		"addon-darwin-arm64":   "",
		"addon-wasm32-fake":    "",
		"addon-wasm32-missing": "",
		"lightningcss":         "1.32.0",
		"lightningcss-wasm":    "1.32.0",
	} {
		if got := installedVersion(t, filepath.Join(nm, path)); got != want {
			t.Errorf("%s: version %q, want %q", path, got, want)
		}
	}
}

// TestPeerDependencies: as npm 7 and later, a missing non-optional peer is
// installed (hoisted, where its dependent resolves it); a reachable one is
// used in whatever version (no second copy), and an optional one is left
// out (@kiwicom/orbit-components peers on @babel/runtime).
func TestPeerDependencies(t *testing.T) {
	srv := fakeRegistry(t, []fakePkg{
		{name: "ui", version: "1.0.0", peers: map[string]string{"runtime": "^7.0.0", "react": "^18", "extra": "*"}, optionalPeers: []string{"extra"}},
		{name: "runtime", version: "7.2.0"},
		{name: "react", version: "18.3.0"},
		{name: "react", version: "19.0.0"},
		{name: "extra", version: "1.0.0"},
	})
	dir := project(t, map[string]string{"ui": "1.0.0", "react": "19.0.0"})
	if err := Install(context.Background(), dir, Options{Registry: srv.URL}); err != nil {
		t.Fatal(err)
	}
	nm := filepath.Join(dir, "node_modules")
	for path, want := range map[string]string{
		"runtime":               "7.2.0",
		"react":                 "19.0.0",
		"ui/node_modules/react": "",
		"extra":                 "",
	} {
		if got := installedVersion(t, filepath.Join(nm, path)); got != want {
			t.Errorf("%s: version %q, want %q", path, got, want)
		}
	}
}

// TestFetchRetries: as npm, a registry request that fails transiently (a
// dropped connection, a 503) is retried, for metadata and tarballs alike,
// and one that fails permanently (a 404) is not.
func TestFetchRetries(t *testing.T) {
	saved := fetchRetryDelay
	fetchRetryDelay = time.Millisecond
	defer func() { fetchRetryDelay = saved }()
	reg := fakeRegistry(t, []fakePkg{{name: "flaky", version: "1.0.0"}})
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits[r.URL.Path]++
		n := hits[r.URL.Path]
		mu.Unlock()
		switch {
		case strings.HasPrefix(r.URL.Path, "/missing"):
			http.NotFound(w, r)
			return
		case n == 1: // the first request of each URL: the connection drops
			c, _, _ := w.(http.Hijacker).Hijack()
			c.Close()
			return
		case n == 2 && strings.HasPrefix(r.URL.Path, "/tarballs/"):
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		// The fake registry's documents name its own tarball URLs; proxy.
		req, _ := http.NewRequest(http.MethodGet, reg.URL+r.URL.Path, nil)
		req.Header.Set("Accept", r.Header.Get("Accept"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Error(err)
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write([]byte(strings.ReplaceAll(string(b), reg.URL, "http://"+r.Host)))
	}))
	defer srv.Close()
	dir := project(t, map[string]string{"flaky": "1.0.0"})
	if err := Install(context.Background(), dir, Options{Registry: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if got := installedVersion(t, filepath.Join(dir, "node_modules", "flaky")); got != "1.0.0" {
		t.Errorf("flaky: version %q", got)
	}
	if h := hits["/flaky/1.0.0"]; h != 2 {
		t.Errorf("metadata requests: %d, want 2", h)
	}
	if h := hits["/tarballs/flaky-1.0.0.tgz"]; h != 3 {
		t.Errorf("tarball requests: %d, want 3", h)
	}
	dir = project(t, map[string]string{"missing": "1.0.0"})
	if err := Install(context.Background(), dir, Options{Registry: srv.URL}); err == nil {
		t.Error("installing a missing package succeeded")
	}
	for _, p := range []string{"/missing/1.0.0", "/missing"} {
		if h := hits[p]; h != 1 {
			t.Errorf("404 requests of %s: %d, want 1 (no retry)", p, h)
		}
	}
}

// countingRegistry serves reg through a proxy that counts requests per
// path, delays metadata responses, tracks how many are in flight at once,
// and adds the given headers (with ETag revalidation when etag is set).
type countingRegistry struct {
	mu          sync.Mutex
	hits        map[string]int
	inFlight    int
	maxInFlight int
	notModified int
	order       []string // request paths, in arrival order
}

func (c *countingRegistry) count(path string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.hits[path]
}

func newCountingRegistry(t *testing.T, reg *httptest.Server, delay time.Duration, cacheControl string, etag bool) (*countingRegistry, *httptest.Server) {
	c := &countingRegistry{hits: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		meta := !strings.HasPrefix(r.URL.Path, "/tarballs/")
		c.mu.Lock()
		c.hits[r.URL.Path]++
		c.order = append(c.order, r.URL.Path)
		if meta { // metadata requests in flight
			c.inFlight++
			if c.inFlight > c.maxInFlight {
				c.maxInFlight = c.inFlight
			}
		}
		c.mu.Unlock()
		if meta {
			defer func() { c.mu.Lock(); c.inFlight--; c.mu.Unlock() }()
			time.Sleep(delay)
		}
		tag := `"v1` + r.URL.Path + `"`
		if etag && r.Header.Get("If-None-Match") == tag {
			c.mu.Lock()
			c.notModified++
			c.mu.Unlock()
			w.WriteHeader(http.StatusNotModified)
			return
		}
		req, _ := http.NewRequest(http.MethodGet, reg.URL+r.URL.Path, nil)
		req.Header.Set("Accept", r.Header.Get("Accept"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Error(err)
			return
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			w.WriteHeader(resp.StatusCode)
			return
		}
		if cacheControl != "" {
			w.Header().Set("Cache-Control", cacheControl)
		}
		if etag {
			w.Header().Set("ETag", tag)
		}
		_, _ = w.Write([]byte(strings.ReplaceAll(string(b), reg.URL, "http://"+r.Host)))
	}))
	t.Cleanup(srv.Close)
	return c, srv
}

func fanOutRegistry(t *testing.T) *httptest.Server {
	pkgs := []fakePkg{{name: "leaf", version: "1.0.0"}}
	deps := map[string]string{}
	for _, n := range []string{"a", "b", "c", "d", "e", "f"} {
		pkgs = append(pkgs, fakePkg{name: n, version: "1.0.0", deps: map[string]string{"leaf": "^1"}})
		deps[n] = "1.0.0"
	}
	return fakeRegistry(t, pkgs)
}

// TestMetadataPrefetch: registry metadata is fetched concurrently while
// the tree resolves (the root's six dependencies are requested at once,
// not one after another), and the tree is the same.
func TestMetadataPrefetch(t *testing.T) {
	c, srv := newCountingRegistry(t, fanOutRegistry(t), 100*time.Millisecond, "", false)
	dir := project(t, map[string]string{"a": "1.0.0", "b": "1.0.0", "c": "1.0.0", "d": "1.0.0", "e": "1.0.0", "f": "1.0.0"})
	start := time.Now()
	if err := Install(context.Background(), dir, Options{Registry: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if c.maxInFlight < 4 {
		t.Errorf("at most %d metadata requests at once; want them concurrent", c.maxInFlight)
	}
	// Seven packages one after another would take 700 ms.
	if d := time.Since(start); d > 600*time.Millisecond {
		t.Errorf("install took %s", d)
	}
	for _, n := range []string{"a", "f", "leaf"} {
		if got := installedVersion(t, filepath.Join(dir, "node_modules", n)); got != "1.0.0" {
			t.Errorf("%s: %q", n, got)
		}
	}
}

// TestInstallCache: with a cache directory, a second install takes the
// tarballs from it (verified again; a corrupted one is fetched anew), and
// the metadata too while the registry's max-age holds; stale metadata is
// revalidated with its ETag (a 304 reuses it).
func TestInstallCache(t *testing.T) {
	reg := fanOutRegistry(t)
	deps := map[string]string{"a": "1.0.0", "b": "1.0.0"}
	tarballs := func(c *countingRegistry) int {
		return c.count("/tarballs/a-1.0.0.tgz") + c.count("/tarballs/b-1.0.0.tgz") + c.count("/tarballs/leaf-1.0.0.tgz")
	}
	t.Run("max-age", func(t *testing.T) {
		cache := t.TempDir()
		c, srv := newCountingRegistry(t, reg, 0, "public, max-age=300", false)
		for i := 0; i < 2; i++ {
			if err := Install(context.Background(), project(t, deps), Options{Registry: srv.URL, CacheDir: cache}); err != nil {
				t.Fatal(err)
			}
		}
		if got := c.count("/a/1.0.0"); got != 1 {
			t.Errorf("metadata of a fetched %d times, want 1", got)
		}
		if got := tarballs(c); got != 3 {
			t.Errorf("%d tarball downloads, want 3 (the second install's from the cache)", got)
		}
	})
	t.Run("etag", func(t *testing.T) {
		cache := t.TempDir()
		c, srv := newCountingRegistry(t, reg, 0, "", true)
		for i := 0; i < 2; i++ {
			dir := project(t, deps)
			if err := Install(context.Background(), dir, Options{Registry: srv.URL, CacheDir: cache}); err != nil {
				t.Fatal(err)
			}
			if got := installedVersion(t, filepath.Join(dir, "node_modules", "leaf")); got != "1.0.0" {
				t.Errorf("install %d: leaf %q", i, got)
			}
		}
		if got := c.count("/a/1.0.0"); got != 2 {
			t.Errorf("metadata of a requested %d times, want 2 (revalidated)", got)
		}
		if c.notModified != 3 {
			t.Errorf("%d metadata answered 304, want 3 (a, b, leaf the second time)", c.notModified)
		}
		if got := tarballs(c); got != 3 {
			t.Errorf("%d tarball downloads, want 3", got)
		}
	})
	t.Run("corrupted tarball", func(t *testing.T) {
		cache := t.TempDir()
		c, srv := newCountingRegistry(t, reg, 0, "public, max-age=300", false)
		if err := Install(context.Background(), project(t, deps), Options{Registry: srv.URL, CacheDir: cache}); err != nil {
			t.Fatal(err)
		}
		files, _ := filepath.Glob(filepath.Join(cache, "tarballs", "*.tgz"))
		if len(files) != 3 {
			t.Fatalf("%d cached tarballs, want 3", len(files))
		}
		for _, f := range files {
			_ = os.WriteFile(f, []byte("garbage"), 0o644)
		}
		dir := project(t, deps)
		if err := Install(context.Background(), dir, Options{Registry: srv.URL, CacheDir: cache}); err != nil {
			t.Fatal(err)
		}
		if got := installedVersion(t, filepath.Join(dir, "node_modules", "a")); got != "1.0.0" {
			t.Errorf("a: %q", got)
		}
		if got := tarballs(c); got != 6 {
			t.Errorf("%d tarball downloads, want 6 (the corrupted cache refetched)", got)
		}
	})
}

// TestDownloadWhileResolving: a package's tarball downloads as soon as
// resolution places it, while its dependencies' metadata is still being
// fetched (a chain a -> b -> c -> d resolves one level at a time).
func TestDownloadWhileResolving(t *testing.T) {
	reg := fakeRegistry(t, []fakePkg{
		{name: "a", version: "1.0.0", deps: map[string]string{"b": "1.0.0"}},
		{name: "b", version: "1.0.0", deps: map[string]string{"c": "1.0.0"}},
		{name: "c", version: "1.0.0", deps: map[string]string{"d": "1.0.0"}},
		{name: "d", version: "1.0.0"},
	})
	c, srv := newCountingRegistry(t, reg, 100*time.Millisecond, "", false)
	dir := project(t, map[string]string{"a": "1.0.0"})
	if err := Install(context.Background(), dir, Options{Registry: srv.URL}); err != nil {
		t.Fatal(err)
	}
	pos := func(path string) int {
		for i, p := range c.order {
			if p == path {
				return i
			}
		}
		return -1
	}
	if ta, md := pos("/tarballs/a-1.0.0.tgz"), pos("/d/1.0.0"); ta < 0 || md < 0 || ta > md {
		t.Errorf("request order %v: a's tarball should come before d's metadata", c.order)
	}
	if got := installedVersion(t, filepath.Join(dir, "node_modules", "d")); got != "1.0.0" {
		t.Errorf("d: %q", got)
	}
}

// TestVersionManifests: an exact version or a dist-tag is resolved from
// that version's manifest alone, a range from the latest version's when it
// satisfies, and the whole metadata (tens of MB for next) is fetched only
// for a range the latest version does not satisfy.
func TestVersionManifests(t *testing.T) {
	reg := fakeRegistry(t, []fakePkg{
		{name: "exact", version: "1.0.0"},
		{name: "tagged", version: "2.0.0"},
		{name: "ranged", version: "3.1.0"},
		{name: "old", version: "1.5.0"},
		{name: "old", version: "2.0.0"},
	})
	c, srv := newCountingRegistry(t, reg, 0, "", false)
	dir := project(t, map[string]string{"exact": "1.0.0", "tagged": "latest", "ranged": "^3.0.0", "old": "^1.0.0"})
	if err := Install(context.Background(), dir, Options{Registry: srv.URL}); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]int{
		"/exact/1.0.0": 1, "/exact": 0,
		"/tagged/latest": 1, "/tagged": 0,
		"/ranged/latest": 1, "/ranged": 0,
		"/old/latest": 1, "/old": 1, // 2.0.0 does not satisfy ^1.0.0
	} {
		if got := c.count(path); got != want {
			t.Errorf("%s requested %d times, want %d", path, got, want)
		}
	}
	if got := installedVersion(t, filepath.Join(dir, "node_modules", "old")); got != "1.5.0" {
		t.Errorf("old: %q, want 1.5.0", got)
	}
}

// TestWorkspaces: a root's workspaces install as npm 7 installs them: each
// linked at node_modules/<name>, their dependencies hoisted to the root
// when free and nested in the workspace's own node_modules when not (lib's
// a@2 beside the root's a@1), and a dependency on a workspace ("workspace:*"
// or a range its version satisfies) answered by the link, never the
// registry. A workspace without a name is named after its directory.
func TestWorkspaces(t *testing.T) {
	reg := fakeRegistry(t, []fakePkg{
		{name: "a", version: "1.0.0"},
		{name: "a", version: "2.0.0"},
		{name: "b", version: "1.0.0"},
	})
	c, srv := newCountingRegistry(t, reg, 0, "", false)
	dir := t.TempDir()
	write := func(rel string, v any) {
		b, _ := json.Marshal(v)
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("package.json", map[string]any{"name": "root", "private": true, "workspaces": []string{"packages/*"}, "dependencies": map[string]string{"a": "^1.0.0"}})
	write("packages/app/package.json", map[string]any{"name": "app", "version": "0.1.0", "dependencies": map[string]string{"lib": "workspace:*", "a": "^1.0.0"}})
	write("packages/lib/package.json", map[string]any{"name": "lib", "version": "0.2.0", "dependencies": map[string]string{"a": "2.0.0", "b": "^1.0.0", "app": "^0.1.0"}})
	// Unnamed: named after its directory, as npm names it.
	write("packages/site/package.json", map[string]any{"dependencies": map[string]string{"b": "^1.0.0"}})
	if err := Install(context.Background(), dir, Options{Registry: srv.URL}); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"app": "../packages/app", "lib": "../packages/lib", "site": "../packages/site"} {
		got, err := os.Readlink(filepath.Join(dir, "node_modules", name))
		if err != nil || got != target {
			t.Errorf("node_modules/%s: link %q (%v), want %q", name, got, err, target)
		}
	}
	for rel, want := range map[string]string{
		"node_modules/a":              "1.0.0",
		"node_modules/b":              "1.0.0",
		"packages/lib/node_modules/a": "2.0.0",
		"node_modules/lib":            "0.2.0", // through the link
	} {
		if got := installedVersion(t, filepath.Join(dir, rel)); got != want {
			t.Errorf("%s: %q, want %s", rel, got, want)
		}
	}
	for _, p := range []string{"/lib", "/app", "/lib/latest", "/app/latest"} {
		if n := c.count(p); n != 0 {
			t.Errorf("%s requested %d times from the registry", p, n)
		}
	}
	// A second install keeps the links and the tree.
	if err := Install(context.Background(), dir, Options{Registry: srv.URL}); err != nil {
		t.Fatal(err)
	}
	if got := installedVersion(t, filepath.Join(dir, "packages/lib/node_modules/a")); got != "2.0.0" {
		t.Errorf("after reinstall, packages/lib/node_modules/a: %q", got)
	}
}

// TestPnpmWorkspaces: pnpm declares workspaces in pnpm-workspace.yaml
// ("!" globs exclude).
func TestPnpmWorkspaces(t *testing.T) {
	reg := fakeRegistry(t, []fakePkg{{name: "b", version: "1.0.0"}})
	dir := t.TempDir()
	files := map[string]string{
		"package.json":              `{"name":"root","private":true}`,
		"pnpm-workspace.yaml":       "packages:\n  - \"apps/*\"\n  - \"!apps/skipped\"\n",
		"apps/web/package.json":     `{"name":"web","dependencies":{"b":"^1.0.0"}}`,
		"apps/skipped/package.json": `{"name":"skipped","dependencies":{"not-in-registry":"1.0.0"}}`,
	}
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := Install(context.Background(), dir, Options{Registry: reg.URL}); err != nil {
		t.Fatal(err)
	}
	if got, err := os.Readlink(filepath.Join(dir, "node_modules", "web")); err != nil || got != "../apps/web" {
		t.Errorf("node_modules/web: %q (%v)", got, err)
	}
	if got := installedVersion(t, filepath.Join(dir, "node_modules", "b")); got != "1.0.0" {
		t.Errorf("b: %q", got)
	}
	if _, err := os.Lstat(filepath.Join(dir, "node_modules", "skipped")); err == nil {
		t.Error("an excluded workspace was linked")
	}
}
