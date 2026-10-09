// Package install materializes node_modules from the npm registry without
// npm or Node. It resolves package.json ranges, hoists like npm (first
// version seen wins the top level, conflicts nest), verifies tarball
// integrity, and never runs lifecycle scripts.
//
// Optional dependencies are skipped: they are almost always native binaries
// (e.g. @next/swc-*, sharp) that Oganesson replaces. The exception is their
// WebAssembly builds, which build-time JS runs (see internal/wasm): optional
// "*wasm32*" packages declaring cpu "wasm32" (napi-rs wasm32-wasi builds,
// e.g. @tailwindcss/oxide-wasm32-wasi), and the separately published wasm
// builds and pure-JS stand-ins that runtime/natives substitutes for native
// packages (lightningcss-wasm, bcryptjs).
//
// With a lockfile (package-lock.json, bun.lock, pnpm-lock.yaml, yarn.lock;
// see lockfile.go) the installed versions are the lockfile's; without one,
// resolution picks the highest matching version.
package install

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/moriyoshi/oganesson/runtime/natives"
)

// Options configure an install.
type Options struct {
	Registry string // default https://registry.npmjs.org
	Client   *http.Client
	// Dev includes devDependencies of the root package.
	Dev bool
	Log func(format string, args ...any)
	// CacheDir keeps registry metadata and tarballs between installs
	// (cache.go); "" keeps nothing.
	CacheDir string
}

type manifest struct {
	Name                 string            `json:"name"`
	Version              string            `json:"version"`
	Dependencies         map[string]string `json:"dependencies"`
	DevDependencies      map[string]string `json:"devDependencies"`
	OptionalDependencies map[string]string `json:"optionalDependencies"`
	PeerDependencies     map[string]string `json:"peerDependencies"`
	PeerDependenciesMeta map[string]struct {
		Optional bool `json:"optional"`
	} `json:"peerDependenciesMeta"`
	Workspaces          json.RawMessage `json:"workspaces"`
	BundleDependencies  json.RawMessage `json:"bundleDependencies"`
	BundledDependencies json.RawMessage `json:"bundledDependencies"`
	CPU                 []string        `json:"cpu"`
	OS                  []string        `json:"os"`
	Dist                struct {
		Tarball   string `json:"tarball"`
		Integrity string `json:"integrity"`
		Shasum    string `json:"shasum"`
	} `json:"dist"`
}

type packument struct {
	Name     string               `json:"name"`
	DistTags map[string]string    `json:"dist-tags"`
	Versions map[string]*manifest `json:"versions"`
}

// node is a placed package in the node_modules tree.
type node struct {
	m        *manifest
	dir      string // path relative to the project root
	children map[string]*node
	lockKey  string // the package's identity in a pin lockfile
	link     string // a workspace's directory (workspaces.go): linked, not fetched
}

type installer struct {
	opts  Options
	mu    sync.Mutex
	calls map[string]*metaCall // metadata requests, by package name
	sem   chan struct{}        // bounds concurrent metadata requests
	pins  pinner               // a pin lockfile (pnpm, yarn), if any
	// workspaces are the root's local packages, by name (workspaces.go).
	workspaces map[string]*workspace

	// Downloads, started as resolution places packages (cache.go).
	root, staging string
	fetches       map[*node]*fetchCall
	fetchSem      chan struct{}
}

// Install installs the dependencies of the package.json in root.
func Install(ctx context.Context, root string, opts Options) error {
	if opts.Registry == "" {
		opts.Registry = "https://registry.npmjs.org"
	}
	if opts.Client == nil {
		opts.Client = http.DefaultClient
	}
	if opts.Log == nil {
		opts.Log = func(string, ...any) {}
	}
	b, err := os.ReadFile(filepath.Join(root, "package.json"))
	if err != nil {
		return err
	}
	var rootPkg manifest
	if err := json.Unmarshal(b, &rootPkg); err != nil {
		return fmt.Errorf("install: package.json: %w", err)
	}
	deps := map[string]string{}
	for k, v := range rootPkg.Dependencies {
		deps[k] = v
	}
	if opts.Dev {
		for k, v := range rootPkg.DevDependencies {
			deps[k] = v
		}
	}
	// Metadata prefetches outlive no install.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	in := &installer{opts: opts, calls: map[string]*metaCall{}, sem: make(chan struct{}, metadataConcurrency),
		root: root, fetches: map[*node]*fetchCall{}, fetchSem: make(chan struct{}, 16)}
	// Packages download into a staging area while the tree resolves, then
	// move into place in path order so that parents land before the
	// packages nested inside them.
	in.staging = filepath.Join(root, "node_modules", ".og-staging")
	if err := os.MkdirAll(in.staging, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(in.staging)
	defer in.stopFetches(cancel) // before the staging area goes
	top := &node{dir: "", children: map[string]*node{}}
	if in.workspaces, err = workspaces(root, rootPkg.Workspaces); err != nil {
		return err
	}
	for _, name := range sortedWorkspaces(in.workspaces) {
		ws := in.workspaces[name]
		top.children[name] = &node{m: ws.m, dir: filepath.Join("node_modules", name), children: map[string]*node{}, link: ws.dir}
	}
	layout, pins, err := loadLockfile(root)
	if err != nil {
		return err
	}
	// A Prisma project gets the WASM schema engine added implicitly, so the
	// packaged app can run `migrate deploy` at startup (resolve path only; a
	// lockfile is taken as authoritative and not amended).
	if layout == nil && pins == nil {
		in.injectPrismaSchemaEngine(ctx, deps)
	}
	if layout != nil && len(in.workspaces) > 0 {
		// TODO: package-lock.json's workspace entries (link: true).
		opts.Log("%s: workspaces resolve from the registry (the lockfile's layout is not used with workspaces)", layout.file)
		layout = nil
	}
	switch {
	case layout != nil:
		opts.Log("using %s", layout.file)
		if err := in.place(ctx, top, layout, deps); err != nil {
			return err
		}
	case pins != nil:
		opts.Log("using %s", pins.file())
		in.pins = pins
		fallthrough
	default:
		if err := in.resolve(ctx, top, top, deps, false); err != nil {
			return err
		}
		for _, name := range sortedWorkspaces(in.workspaces) {
			ws := in.workspaces[name]
			wsDeps := map[string]string{}
			for k, v := range ws.m.Dependencies {
				wsDeps[k] = v
			}
			if opts.Dev {
				for k, v := range ws.m.DevDependencies {
					wsDeps[k] = v
				}
			}
			if err := in.resolve(ctx, top, top.children[name], wsDeps, false); err != nil {
				return err
			}
		}
	}
	var all []*node
	var walk func(n *node)
	walk = func(n *node) {
		for _, c := range n.children {
			all = append(all, c)
			walk(c)
		}
	}
	walk(top)
	sort.Slice(all, func(i, j int) bool { return all[i].dir < all[j].dir })
	opts.Log("resolved %d packages", len(all))

	staged := make([]string, len(all))
	for _, n := range all {
		if n.link == "" {
			in.startFetch(ctx, n) // the ones resolution did not start
		}
	}
	for i, n := range all {
		if n.link != "" {
			continue
		}
		c := in.startFetch(ctx, n)
		<-c.done
		if c.err != nil {
			return c.err
		}
		staged[i] = c.dir
	}
	for i, n := range all {
		if n.link != "" {
			// Before the packages placed under it (all is in path order).
			if err := linkWorkspace(root, n); err != nil {
				return err
			}
			opts.Log("+ %s (workspace %s)", n.m.Name, n.link)
			continue
		}
		if staged[i] == "" {
			continue // already installed
		}
		dest := filepath.Join(root, n.dir)
		_ = os.RemoveAll(dest)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		if err := os.Rename(staged[i], dest); err != nil {
			return err
		}
		opts.Log("+ %s@%s", n.m.Name, n.m.Version)
	}
	return nil
}

// resolve places deps (requested by parent) into the tree, breadth-first so
// that shallower requests win the hoisted slot.
func (in *installer) resolve(ctx context.Context, top, parent *node, deps map[string]string, wasmOnly bool) error {
	type pending struct {
		parent   *node
		name     string
		rng      string
		wasmOnly bool // an optional WebAssembly build: skipped if unusable
		peer     bool // a peer dependency: installed only when none is reachable
		optional bool // an optional dependency: skipped if unusable or platform-bound
	}
	queue := []pending{}
	// Queuing a dependency starts fetching its metadata (cache.go), so the
	// breadth-first walk below rarely waits for the registry.
	enqueue := func(p pending) {
		queue = append(queue, p)
		if p.peer || unsupportedSpec(p.rng) || in.satisfiedByWorkspace(p.name, p.rng) {
			return
		}
		name, rng := p.name, p.rng
		if t, r, ok := npmAlias(p.rng); ok {
			name, rng = t, r
		}
		if in.pins != nil {
			return // the lockfile decides the version: exact asks for it
		}
		in.prefetch(ctx, name, rng)
	}
	for _, name := range sortedKeys(deps) {
		enqueue(pending{parent, name, deps[name], wasmOnly, false, false})
	}
	for len(queue) > 0 {
		p := queue[0]
		queue = queue[1:]
		name, rng := p.name, p.rng
		// A peer dependency is the dependent's requester's to provide; npm
		// (7 and later) installs one only when none is reachable. One that is,
		// in whatever version, is used (a second React would break React).
		if p.peer && findUp(p.parent, top, name) != nil {
			continue
		}
		if in.satisfiedByWorkspace(name, rng) {
			continue
		}
		var m *manifest
		var err error
		lockKey := ""
		if in.pins != nil {
			// The lockfile decides the version; a WebAssembly substitute it
			// does not know falls back to the registry.
			var version, integrity string
			version, lockKey, integrity, err = in.pins.pin(p.parent, name, rng)
			if err != nil && p.peer {
				continue // the lockfile locked no such peer
			}
			if err != nil && (p.wasmOnly || p.optional) {
				m, err = in.pick(ctx, name, rng)
			} else if err == nil {
				if n := findUp(p.parent, top, name); n != nil && n.m.Version == version {
					continue
				}
				m, err = in.exact(ctx, name, version, integrity)
			} else {
				err = fmt.Errorf("install: %w", err)
			}
		} else {
			// An npm alias ("h3-v2": "npm:h3@2.0.1") installs the target
			// package under the alias's name.
			target := name
			if t, r, ok := npmAlias(rng); ok {
				target, rng = t, r
			}
			if unsupportedSpec(rng) {
				return fmt.Errorf("install: %s: unsupported dependency spec %q (git, file and workspace deps need a lockfile)", name, rng)
			}
			// Already satisfied by an ancestor (Node's resolution walks upward)?
			if n := findUp(p.parent, top, name); n != nil && n.m.Name == target && satisfies(n.m.Version, rng) {
				continue
			}
			m, err = in.pick(ctx, target, rng)
		}
		if err != nil {
			if p.wasmOnly || p.optional {
				in.opts.Log("skipping %s@%s: %v", name, rng, err)
				continue
			}
			return err
		}
		// npm installs an optional dependency built for the host's platform;
		// og runs no native code, so a platform-bound one (a binary package
		// such as @esbuild/linux-x64) is left out, a portable one (node-fetch,
		// which realm-web lists as optional) installed.
		if p.optional && (len(m.CPU) > 0 || len(m.OS) > 0) {
			continue
		}
		if p.wasmOnly && !slices.Contains(m.CPU, "wasm32") && !isSubstitute(p.parent, name) {
			continue
		}
		// Hoist to the top level when free, else nest under the requester.
		host := top
		if existing, ok := top.children[name]; ok && existing.m.Version != m.Version {
			host = p.parent
		}
		if existing, ok := host.children[name]; ok {
			if existing.m.Version == m.Version {
				continue
			}
			return fmt.Errorf("install: conflict placing %s@%s under %s", name, m.Version, host.dir)
		}
		dir := filepath.Join(host.dir, "node_modules", name)
		n := &node{m: m, dir: dir, children: map[string]*node{}, lockKey: lockKey}
		host.children[name] = n
		in.startFetch(ctx, n) // download while the rest resolves
		bundled := m.bundled()
		for _, dn := range sortedKeys(m.Dependencies) {
			if _, optional := m.OptionalDependencies[dn]; optional || bundled(dn) {
				continue
			}
			enqueue(pending{n, dn, m.Dependencies[dn], false, false, false})
		}
		for _, dn := range sortedKeys(m.OptionalDependencies) {
			if strings.Contains(dn, "wasm32") {
				enqueue(pending{n, dn, m.OptionalDependencies[dn], true, false, false})
			} else {
				enqueue(pending{n, dn, m.OptionalDependencies[dn], false, false, true})
			}
		}
		for _, dn := range sortedKeys(m.PeerDependencies) {
			if m.PeerDependenciesMeta[dn].Optional {
				continue
			}
			if _, regular := m.Dependencies[dn]; regular {
				continue
			}
			enqueue(pending{n, dn, m.PeerDependencies[dn], false, true, false})
		}
		if sub, rng, ok := natives.InstallFor(m.Name, m.Version); ok {
			enqueue(pending{n, sub, rng, true, false, false})
		}
	}
	return nil
}

// bundled reports which dependencies ship inside the package's own tarball
// (bundleDependencies: a list of names, or true for all of them).
func (m *manifest) bundled() func(string) bool {
	raw := m.BundleDependencies
	if len(raw) == 0 {
		raw = m.BundledDependencies
	}
	var all bool
	if json.Unmarshal(raw, &all) == nil && all {
		return func(string) bool { return true }
	}
	var names []string
	_ = json.Unmarshal(raw, &names)
	return func(n string) bool { return slices.Contains(names, n) }
}

// isSubstitute reports whether name is what runtime/natives installs next
// to parent's package.
func isSubstitute(parent *node, name string) bool {
	if parent.m == nil {
		return false
	}
	sub, _, ok := natives.InstallFor(parent.m.Name, parent.m.Version)
	return ok && sub == name
}

// npmAlias parses "npm:<package>[@<range>]" (a range-less alias means the
// latest version).
func npmAlias(spec string) (pkg, rng string, ok bool) {
	rest, ok := strings.CutPrefix(spec, "npm:")
	if !ok {
		return "", "", false
	}
	if i := strings.LastIndex(rest, "@"); i > 0 {
		return rest[:i], rest[i+1:], true
	}
	return rest, "latest", true
}

func unsupportedSpec(rng string) bool {
	for _, p := range []string{"npm:", "file:", "link:", "workspace:", "git", "http:", "https:", "github:"} {
		if strings.HasPrefix(rng, p) {
			return true
		}
	}
	return false
}

func findUp(from, top *node, name string) *node {
	// Without parent links, check the requester's own children and the top
	// level; intermediate levels are rare in practice for hoisted trees.
	if n, ok := from.children[name]; ok {
		return n
	}
	if n, ok := top.children[name]; ok {
		return n
	}
	return nil
}

func satisfies(version, rng string) bool {
	c, err := constraint(rng)
	if err != nil {
		return false
	}
	v, err := semver.NewVersion(version)
	return err == nil && c.Check(v)
}

func constraint(rng string) (*semver.Constraints, error) {
	rng = strings.TrimSpace(rng)
	if rng == "" || rng == "latest" {
		rng = "*"
	}
	return semver.NewConstraint(rng)
}

// injectPrismaSchemaEngine adds @prisma/schema-engine-wasm to the root
// dependency set when the project uses Prisma (it depends on @prisma/client),
// so og's app-startup `prisma migrate deploy` has the WASM schema engine without
// the user declaring it. schema-engine-wasm is published once per engine commit,
// as a prerelease version (e.g. 7.10.0-4.<hash>) that a caret range would not
// select, so it is pinned to the exact @prisma/engines-version that
// @prisma/client resolves to. Best-effort: not a Prisma app, an already-declared
// engine, or a resolution miss all just skip it (never fail the install).
func (in *installer) injectPrismaSchemaEngine(ctx context.Context, deps map[string]string) {
	const enginePkg = "@prisma/schema-engine-wasm"
	if _, ok := deps[enginePkg]; ok {
		return // declared explicitly; leave it
	}
	clientRange, ok := deps["@prisma/client"]
	if !ok {
		return // not a Prisma app
	}
	m, err := in.pick(ctx, "@prisma/client", clientRange)
	if err != nil {
		in.opts.Log("prisma: not adding %s (@prisma/client %q: %v)", enginePkg, clientRange, err)
		return
	}
	// @prisma/schema-engine-wasm is published once per engine commit under the
	// @prisma/engines-version value. Prisma <= 6 pins that in @prisma/client's
	// dependencies; Prisma 7 moved it to devDependencies, which the registry's
	// abbreviated metadata omits, so read it from @prisma/engines instead (all
	// @prisma/* ship in lockstep at one version, and @prisma/engines still pins
	// engines-version in its dependencies).
	v := m.Dependencies["@prisma/engines-version"]
	if v == "" {
		if em, err := in.pick(ctx, "@prisma/engines", m.Version); err != nil {
			in.opts.Log("prisma: not adding %s (@prisma/engines %s: %v)", enginePkg, m.Version, err)
		} else {
			v = em.Dependencies["@prisma/engines-version"]
		}
	}
	if v == "" {
		in.opts.Log("prisma: not adding %s (no @prisma/engines-version for @prisma/client %s)", enginePkg, m.Version)
		return
	}
	deps[enginePkg] = v
	in.opts.Log("prisma: adding %s@%s for app-startup migrate deploy", enginePkg, v)
}

func (in *installer) pick(ctx context.Context, name, rng string) (*manifest, error) {
	// One version's manifest is a few KB where the whole metadata can be
	// tens of MB (next's: 25): an exact version or a dist-tag needs only
	// it, a range first tries the latest one (which wins when it
	// satisfies, below).
	if m, ok := in.quickPick(ctx, name, rng); ok {
		return m, nil
	}
	p, err := in.packument(ctx, name)
	if err != nil {
		return nil, err
	}
	if tag, ok := p.DistTags[rng]; ok {
		return p.Versions[tag], nil
	}
	// An exact version is common (e.g. next pins its own deps).
	if m, ok := p.Versions[rng]; ok {
		return m, nil
	}
	c, err := constraint(rng)
	if err != nil {
		return nil, fmt.Errorf("install: %s: unsupported range %q: %w", name, rng, err)
	}
	// Prefer the "latest" tag when it satisfies, like npm.
	if latest, ok := p.DistTags["latest"]; ok {
		if v, err := semver.NewVersion(latest); err == nil && c.Check(v) {
			return p.Versions[latest], nil
		}
	}
	var best *semver.Version
	for vs := range p.Versions {
		v, err := semver.NewVersion(vs)
		if err != nil || !c.Check(v) {
			continue
		}
		if best == nil || v.GreaterThan(best) {
			best = v
		}
	}
	if best == nil {
		return nil, fmt.Errorf("install: no version of %s satisfies %q", name, rng)
	}
	return p.Versions[best.Original()], nil
}

// exact returns name@version's manifest; a lockfile's integrity, when
// given, is the one the tarball must match.
func (in *installer) exact(ctx context.Context, name, version, integrity string) (*manifest, error) {
	m, err := in.versionManifest(ctx, name, version)
	if err != nil {
		p, perr := in.packument(ctx, name)
		if perr != nil {
			return nil, perr
		}
		var ok bool
		if m, ok = p.Versions[version]; !ok {
			return nil, fmt.Errorf("install: %s@%s (from the lockfile) is not in the registry", name, version)
		}
	}
	if integrity != "" {
		c := *m
		c.Dist.Integrity, c.Dist.Shasum = integrity, ""
		m = &c
	}
	return m, nil
}

// place builds the tree a layout lockfile describes, keeping the packages
// the root needs (see layoutLock.reachable).
func (in *installer) place(ctx context.Context, top *node, l *layoutLock, deps map[string]string) error {
	entries, err := l.reachable(deps)
	if err != nil {
		return fmt.Errorf("install: %w", err)
	}
	nodes := map[string]*node{"": top}
	for _, e := range entries { // sorted by path: parents first
		if e.integrity == "" {
			return fmt.Errorf("install: %s: %s@%s has no integrity in %s", e.dir, e.name, e.version, l.file)
		}
		m := &manifest{Name: e.name, Version: e.version, Dependencies: e.dependencies, OptionalDependencies: e.optionalDependencies, CPU: e.cpu}
		m.Dist.Tarball = e.resolved
		if m.Dist.Tarball == "" {
			m.Dist.Tarball = registryTarball(in.opts.Registry, e.name, e.version)
		}
		m.Dist.Integrity = e.integrity
		parentDir := ""
		if i := strings.LastIndex(e.dir, "/node_modules/"); i >= 0 {
			parentDir = e.dir[:i]
		}
		parent, ok := nodes[parentDir]
		if !ok {
			return fmt.Errorf("install: %s: its parent %s is not installed", e.dir, parentDir)
		}
		n := &node{m: m, dir: filepath.FromSlash(e.dir), children: map[string]*node{}}
		parent.children[e.name] = n
		nodes[e.dir] = n
	}
	// Substitutes (runtime/natives) are Oganesson's addition: no lockfile
	// has them.
	for _, e := range entries {
		if sub, rng, ok := natives.InstallFor(e.name, e.version); ok {
			if err := in.resolve(ctx, top, nodes[e.dir], map[string]string{sub: rng}, true); err != nil {
				return err
			}
		}
	}
	return nil
}

// fetchAttempts is how many times a registry request is made before its
// failure counts (npm's fetch-retries is 2: three attempts).
const fetchAttempts = 3

// fetchRetryDelay is the wait before the first retry; each later wait is
// ten times longer, at most fetchRetryMaxDelay (npm's fetch-retry-mintimeout,
// fetch-retry-factor and fetch-retry-maxtimeout: an outage of a few seconds
// does not fail an install).
var fetchRetryDelay = 10 * time.Second

const fetchRetryMaxDelay = time.Minute

// permanent marks an error a retry cannot fix.
type permanent struct{ error }

func (p permanent) Unwrap() error { return p.error }

// retry calls f until it succeeds, fails permanently, or has been called
// fetchAttempts times: a dropped connection, a reset mid-download, a 429 or
// a 5xx is retried, as npm does.
func retry(ctx context.Context, f func() error) error {
	delay := fetchRetryDelay
	for attempt := 1; ; attempt++ {
		err := f()
		var p permanent
		if err == nil || errors.As(err, &p) || attempt == fetchAttempts || ctx.Err() != nil {
			if errors.As(err, &p) {
				return p.error
			}
			return err
		}
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return err
		}
		delay = min(delay*10, fetchRetryMaxDelay)
	}
}

// statusError is a failed request's error: permanent unless the status
// asks for a retry.
func statusError(u string, resp *http.Response) error {
	err := fmt.Errorf("install: GET %s: %s", u, resp.Status)
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return err
	}
	return permanent{err}
}

// packument is name's whole registry metadata: fetched once per install,
// maybe from the persistent cache (cache.go).
func (in *installer) packument(ctx context.Context, name string) (*packument, error) {
	c := in.request(ctx, name, "")
	if err := c.wait(ctx); err != nil {
		return nil, err
	}
	return c.p, nil
}

// fetchPackument requests name's metadata from the registry (through the
// persistent cache when there is one).
func (in *installer) fetchPackument(ctx context.Context, name string) (*packument, error) {
	u := in.opts.Registry + "/" + strings.Replace(url.PathEscape(name), "%40", "@", 1)
	var body []byte
	err := retry(ctx, func() error {
		var err error
		body, err = in.cachedGet(ctx, u, "application/vnd.npm.install-v1+json")
		return err
	})
	if err != nil {
		return nil, err
	}
	var p packument
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("install: %s metadata: %w", name, err)
	}
	return &p, nil
}

// fetch downloads and verifies one package, extracting it to tmp. It
// returns "" when the right version is already installed.
func (in *installer) fetch(ctx context.Context, root, tmp string, n *node) (string, error) {
	if b, err := os.ReadFile(filepath.Join(root, n.dir, "package.json")); err == nil {
		var have manifest
		if json.Unmarshal(b, &have) == nil && have.Version == n.m.Version {
			return "", nil
		}
	}
	// A tarball kept from an earlier install, verified again.
	cachePath := in.tarballCachePath(n.m)
	if cachePath != "" {
		if ok := in.extractCached(cachePath, tmp, n.m); ok {
			return tmp, nil
		}
	}
	err := retry(ctx, func() error {
		// A retry starts over in an empty directory.
		if err := os.RemoveAll(tmp); err != nil {
			return permanent{err}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, n.m.Dist.Tarball, nil)
		if err != nil {
			return permanent{err}
		}
		resp, err := in.opts.Client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return statusError(n.m.Dist.Tarball, resp)
		}
		h, want, err := integrity(n.m)
		if err != nil {
			return permanent{err}
		}
		// The download goes to the cache too, kept once verified.
		var body io.Reader = resp.Body
		var keep *os.File
		if cachePath != "" && os.MkdirAll(filepath.Dir(cachePath), 0o755) == nil {
			if keep, err = os.CreateTemp(filepath.Dir(cachePath), ".tmp-*"); err == nil {
				defer os.Remove(keep.Name())
				defer keep.Close()
				body = io.TeeReader(resp.Body, keep)
			} else {
				keep = nil
			}
		}
		if err := extract(io.TeeReader(body, h), tmp); err != nil {
			return fmt.Errorf("install: %s@%s: %w", n.m.Name, n.m.Version, err)
		}
		if got := h.Sum(nil); !equalBytes(got, want) {
			return permanent{fmt.Errorf("install: %s@%s: integrity mismatch", n.m.Name, n.m.Version)}
		}
		if keep != nil {
			// extract stops at the archive's end; the rest of the gzip
			// stream belongs in the cached file too.
			if _, err := io.Copy(io.Discard, body); err == nil && keep.Close() == nil {
				_ = os.Rename(keep.Name(), cachePath)
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return tmp, nil
}

// extractCached extracts the cached tarball at path into tmp when it
// matches m's integrity; a mismatched or unreadable one is dropped.
func (in *installer) extractCached(path, tmp string, m *manifest) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	h, want, err := integrity(m)
	if err != nil {
		return false
	}
	_ = os.RemoveAll(tmp)
	r := io.TeeReader(f, h)
	if extract(r, tmp) == nil {
		_, _ = io.Copy(io.Discard, r)
		if equalBytes(h.Sum(nil), want) {
			return true
		}
	}
	_ = os.Remove(path)
	_ = os.RemoveAll(tmp)
	return false
}

func integrity(m *manifest) (hash.Hash, []byte, error) {
	// Subresource Integrity strings; the strongest listed algorithm wins
	// (older lockfiles record sha1).
	for _, alg := range []struct {
		prefix string
		h      func() hash.Hash
	}{{"sha512-", sha512.New}, {"sha384-", sha512.New384}, {"sha256-", sha256.New}, {"sha1-", sha1.New}} {
		for _, part := range strings.Fields(m.Dist.Integrity) {
			if b64, ok := strings.CutPrefix(part, alg.prefix); ok {
				want, err := base64.StdEncoding.DecodeString(b64)
				return alg.h(), want, err
			}
		}
	}
	if m.Dist.Shasum != "" {
		want, err := hex.DecodeString(m.Dist.Shasum)
		return sha1.New(), want, err
	}
	return nil, nil, fmt.Errorf("install: %s@%s has no integrity data", m.Name, m.Version)
}

// extract unpacks an npm tarball, stripping the leading path component
// (usually "package/") and refusing paths that escape dest.
func extract(r io.Reader, dest string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		name := hdr.Name
		if i := strings.IndexByte(name, '/'); i >= 0 {
			name = name[i+1:]
		} else {
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(name))
		if target != filepath.Clean(dest) && !strings.HasPrefix(target, filepath.Clean(dest)+string(os.PathSeparator)) {
			return fmt.Errorf("unsafe path %q in tarball", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if hdr.Mode&0o111 != 0 {
				mode = 0o755
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			if err := f.Close(); err != nil {
				return err
			}
		}
	}
	// Drain so the integrity hash covers the whole tarball.
	_, err = io.Copy(io.Discard, r)
	return err
}

func equalBytes(a, b []byte) bool {
	return len(a) == len(b) && string(a) == string(b)
}

func sortedKeys(m map[string]string) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
