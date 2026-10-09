package install

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/goccy/go-yaml"
)

// Lockfiles. Two kinds:
//
//   - Layout lockfiles describe the node_modules tree itself:
//     package-lock.json / npm-shrinkwrap.json (v2, v3) and bun.lock. The
//     installer places exactly that tree, downloading each package from the
//     lockfile's URL and checking the lockfile's integrity.
//   - Pin lockfiles fix versions but not a layout: yarn.lock (v1 and Berry)
//     and pnpm-lock.yaml. The installer hoists as without a lockfile, but
//     every version comes from the lockfile (the requester's entry for pnpm,
//     the dependency's range for yarn), and the lockfile's integrity wins.
//
// A package.json dependency the lockfile lacks is an error, as with
// `npm ci`: the lockfile is out of date.

// layoutEntry is one package of a layout lockfile.
type layoutEntry struct {
	dir                  string // e.g. "node_modules/a/node_modules/b"
	name, version        string
	resolved, integrity  string
	dependencies         map[string]string
	optionalDependencies map[string]string
	cpu                  []string
	link                 bool
	bundled              func(string) bool // dependencies inside the package's tarball
}

type layoutLock struct {
	file    string
	entries map[string]*layoutEntry
}

// pinner answers which version a requester gets for a dependency.
type pinner interface {
	file() string
	// pin returns the pinned version, the key that identifies the placed
	// package for its own dependencies, and the integrity (if recorded).
	pin(requester *node, name, rng string) (version, key, integrity string, err error)
}

// loadLockfile finds and parses root's lockfile (nil, nil when none).
func loadLockfile(root string) (*layoutLock, pinner, error) {
	read := func(name string) ([]byte, bool, error) {
		b, err := os.ReadFile(filepath.Join(root, name))
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return b, err == nil, err
	}
	for _, name := range []string{"npm-shrinkwrap.json", "package-lock.json"} {
		if b, ok, err := read(name); err != nil || ok {
			if err != nil {
				return nil, nil, err
			}
			l, err := parseNpmLock(b)
			if l != nil {
				l.file = name
			}
			return l, nil, wrap(name, err)
		}
	}
	if b, ok, err := read("bun.lock"); err != nil || ok {
		if err != nil {
			return nil, nil, err
		}
		l, err := parseBunLock(b)
		if l != nil {
			l.file = "bun.lock"
		}
		return l, nil, wrap("bun.lock", err)
	}
	if _, err := os.Stat(filepath.Join(root, "bun.lockb")); err == nil {
		return nil, nil, fmt.Errorf("install: bun.lockb (binary) is not supported; run `bun install --save-text-lockfile` to write bun.lock")
	}
	if b, ok, err := read("pnpm-lock.yaml"); err != nil || ok {
		if err != nil {
			return nil, nil, err
		}
		p, err := parsePnpmLock(b)
		return nil, p, wrap("pnpm-lock.yaml", err)
	}
	if b, ok, err := read("yarn.lock"); err != nil || ok {
		if err != nil {
			return nil, nil, err
		}
		var y *yarnLock
		if bytes.Contains(b, []byte("__metadata:")) {
			y, err = parseYarnBerry(b)
		} else {
			y, err = parseYarnV1(b)
		}
		return nil, y, wrap("yarn.lock", err)
	}
	return nil, nil, nil
}

func wrap(file string, err error) error {
	if err != nil {
		return fmt.Errorf("install: %s: %w", file, err)
	}
	return nil
}

// --- package-lock.json ------------------------------------------------------

func parseNpmLock(b []byte) (*layoutLock, error) {
	var doc struct {
		LockfileVersion int `json:"lockfileVersion"`
		Packages        map[string]struct {
			Name                 string            `json:"name"`
			Version              string            `json:"version"`
			Resolved             string            `json:"resolved"`
			Integrity            string            `json:"integrity"`
			Link                 bool              `json:"link"`
			InBundle             bool              `json:"inBundle"`
			BundleDependencies   json.RawMessage   `json:"bundleDependencies"`
			Dependencies         map[string]string `json:"dependencies"`
			OptionalDependencies map[string]string `json:"optionalDependencies"`
			CPU                  []string          `json:"cpu"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	if doc.LockfileVersion < 2 || doc.Packages == nil {
		return nil, fmt.Errorf("lockfileVersion %d is not supported (npm 7 or later writes v2/v3)", doc.LockfileVersion)
	}
	l := &layoutLock{entries: map[string]*layoutEntry{}}
	for dir, p := range doc.Packages {
		if p.InBundle {
			continue // shipped inside its parent's tarball
		}
		e := &layoutEntry{
			dir: dir, name: p.Name, version: p.Version, resolved: p.Resolved, integrity: p.Integrity, link: p.Link,
			dependencies: p.Dependencies, optionalDependencies: p.OptionalDependencies, cpu: p.CPU,
			bundled: (&manifest{BundleDependencies: p.BundleDependencies}).bundled(),
		}
		if !strings.HasPrefix(dir, "node_modules/") {
			continue // the root ("") and workspace sources ("packages/x")
		}
		if e.name == "" {
			e.name = lastPackage(dir)
		}
		l.entries[dir] = e
	}
	return l, nil
}

// lastPackage is the package name a node_modules path ends with.
func lastPackage(dir string) string {
	i := strings.LastIndex(dir, "node_modules/")
	return dir[i+len("node_modules/"):]
}

// --- bun.lock ---------------------------------------------------------------

var trailingComma = regexp.MustCompile(`,(\s*[}\]])`)

func parseBunLock(b []byte) (*layoutLock, error) {
	// bun.lock is JSON with trailing commas (JSONC without comments).
	var doc struct {
		LockfileVersion int                          `json:"lockfileVersion"`
		Workspaces      map[string]json.RawMessage   `json:"workspaces"`
		Packages        map[string][]json.RawMessage `json:"packages"`
	}
	if err := json.Unmarshal(stripTrailingCommas(b), &doc); err != nil {
		return nil, err
	}
	if _, ok := doc.Workspaces[""]; !ok {
		return nil, fmt.Errorf("no root workspace")
	}
	l := &layoutLock{entries: map[string]*layoutEntry{}}
	for key, raw := range doc.Packages {
		if len(raw) == 0 {
			continue
		}
		var ident string
		if err := json.Unmarshal(raw[0], &ident); err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		name, version, ok := cutVersion(ident)
		if !ok {
			return nil, fmt.Errorf("%s: unexpected identifier %q", key, ident)
		}
		e := &layoutEntry{name: name, version: version}
		if strings.HasPrefix(version, "workspace:") || strings.HasPrefix(version, "link:") || strings.HasPrefix(version, "file:") {
			e.link = true
		}
		if len(raw) > 1 {
			_ = json.Unmarshal(raw[1], &e.resolved)
		}
		if len(raw) > 2 {
			var meta struct {
				Dependencies         map[string]string `json:"dependencies"`
				OptionalDependencies map[string]string `json:"optionalDependencies"`
				CPU                  json.RawMessage   `json:"cpu"`
			}
			_ = json.Unmarshal(raw[2], &meta)
			e.dependencies, e.optionalDependencies = meta.Dependencies, meta.OptionalDependencies
			var one string
			if json.Unmarshal(meta.CPU, &one) == nil && one != "" {
				e.cpu = []string{one}
			} else {
				_ = json.Unmarshal(meta.CPU, &e.cpu)
			}
		}
		if len(raw) > 3 {
			_ = json.Unmarshal(raw[3], &e.integrity)
		}
		if e.resolved != "" && !strings.Contains(e.resolved, "://") {
			e.resolved = "" // a registry name, not a URL: use the configured registry
		}
		dir, err := bunKeyDir(key)
		if err != nil {
			return nil, err
		}
		e.dir = dir
		l.entries[dir] = e
	}
	return l, nil
}

// bunKeyDir maps a bun.lock package key ("next/@swc/helpers") to its
// node_modules path ("node_modules/next/node_modules/@swc/helpers").
func bunKeyDir(key string) (string, error) {
	segs := strings.Split(key, "/")
	var names []string
	for i := 0; i < len(segs); i++ {
		if strings.HasPrefix(segs[i], "@") {
			if i+1 >= len(segs) {
				return "", fmt.Errorf("bad package key %q", key)
			}
			names = append(names, segs[i]+"/"+segs[i+1])
			i++
			continue
		}
		names = append(names, segs[i])
	}
	return "node_modules/" + strings.Join(names, "/node_modules/"), nil
}

func stripTrailingCommas(b []byte) []byte {
	// Outside strings only.
	var out bytes.Buffer
	inStr, esc := false, false
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch {
		case inStr:
			if esc {
				esc = false
			} else if c == '\\' {
				esc = true
			} else if c == '"' {
				inStr = false
			}
		case c == '"':
			inStr = true
		case c == ',':
			j := i + 1
			for j < len(b) && (b[j] == ' ' || b[j] == '\t' || b[j] == '\n' || b[j] == '\r') {
				j++
			}
			if j < len(b) && (b[j] == '}' || b[j] == ']') {
				continue
			}
		}
		out.WriteByte(c)
	}
	return out.Bytes()
}

// cutVersion splits "name@version" (the name may be scoped).
func cutVersion(s string) (name, version string, ok bool) {
	i := strings.LastIndex(s, "@")
	if i <= 0 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

// --- layout resolution ------------------------------------------------------

// find resolves a dependency of the package at dir as Node does: its own
// node_modules, then each ancestor's, then the top level.
func (l *layoutLock) find(dir, name string) *layoutEntry {
	for d := dir; ; {
		cand := name
		if d != "" {
			cand = d + "/node_modules/" + name
		} else {
			cand = "node_modules/" + name
		}
		if e, ok := l.entries[cand]; ok {
			return e
		}
		if d == "" {
			return nil
		}
		i := strings.LastIndex(d, "/node_modules/")
		if i < 0 {
			d = ""
		} else {
			d = d[:i]
		}
	}
}

// reachable returns the entries the root needs: the package.json
// dependencies in start, and theirs. Optional dependencies are followed
// only to WebAssembly builds (see the package comment).
func (l *layoutLock) reachable(start map[string]string) ([]*layoutEntry, error) {
	seen := map[*layoutEntry]bool{}
	var out []*layoutEntry
	type item struct {
		from     string
		name     string
		optional bool
	}
	var queue []item
	for _, n := range sortedKeys(start) {
		queue = append(queue, item{"", n, false})
	}
	for len(queue) > 0 {
		it := queue[0]
		queue = queue[1:]
		e := l.find(it.from, it.name)
		if e == nil {
			if it.optional {
				continue
			}
			if it.from == "" {
				return nil, fmt.Errorf("%s is not in %s; the lockfile is out of date with package.json", it.name, l.file)
			}
			return nil, fmt.Errorf("%s (needed by %s) is not in %s", it.name, lastPackage(it.from), l.file)
		}
		if seen[e] {
			continue
		}
		if it.optional && !slices.Contains(e.cpu, "wasm32") && !strings.Contains(e.name, "wasm32") {
			continue
		}
		if e.link {
			return nil, fmt.Errorf("%s: workspace and linked packages are not supported yet", e.name)
		}
		seen[e] = true
		out = append(out, e)
		bundled := e.bundled
		if bundled == nil {
			bundled = func(string) bool { return false }
		}
		for _, n := range sortedKeys(e.dependencies) {
			if _, opt := e.optionalDependencies[n]; opt || bundled(n) {
				continue
			}
			queue = append(queue, item{e.dir, n, false})
		}
		for _, n := range sortedKeys(e.optionalDependencies) {
			if !bundled(n) {
				queue = append(queue, item{e.dir, n, true})
			}
		}
	}
	slices.SortFunc(out, func(a, b *layoutEntry) int { return strings.Compare(a.dir, b.dir) })
	return out, nil
}

// --- pnpm-lock.yaml ---------------------------------------------------------

type pnpmLock struct {
	importers map[string]map[string]string // "dependencies", … merged: name -> version
	snapshots map[string]map[string]string // snapshot key -> deps (optional included)
	integrity map[string]string            // "name@version" -> integrity
}

func (p *pnpmLock) file() string { return "pnpm-lock.yaml" }

func parsePnpmLock(b []byte) (*pnpmLock, error) {
	type deps map[string]any
	var doc struct {
		LockfileVersion any `yaml:"lockfileVersion"`
		Importers       map[string]struct {
			Dependencies         deps `yaml:"dependencies"`
			DevDependencies      deps `yaml:"devDependencies"`
			OptionalDependencies deps `yaml:"optionalDependencies"`
		} `yaml:"importers"`
		Packages map[string]struct {
			Resolution struct {
				Integrity string `yaml:"integrity"`
			} `yaml:"resolution"`
			Dependencies         map[string]string `yaml:"dependencies"`
			OptionalDependencies map[string]string `yaml:"optionalDependencies"`
		} `yaml:"packages"`
		Snapshots map[string]struct {
			Dependencies         map[string]string `yaml:"dependencies"`
			OptionalDependencies map[string]string `yaml:"optionalDependencies"`
		} `yaml:"snapshots"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	v, _ := strconv.ParseFloat(fmt.Sprint(doc.LockfileVersion), 64)
	if v < 6 {
		return nil, fmt.Errorf("lockfileVersion %v is not supported (pnpm 8 or later writes 6.0/9.0)", doc.LockfileVersion)
	}
	p := &pnpmLock{importers: map[string]map[string]string{}, snapshots: map[string]map[string]string{}, integrity: map[string]string{}}
	for dir, imp := range doc.Importers {
		m := map[string]string{}
		for _, d := range []deps{imp.Dependencies, imp.DevDependencies, imp.OptionalDependencies} {
			for name, spec := range d {
				switch s := spec.(type) {
				case string: // v6 (5.x style): name: version
					m[name] = s
				case map[string]any: // {specifier, version}
					m[name] = fmt.Sprint(s["version"])
				}
			}
		}
		p.importers[dir] = m
	}
	merge := func(a, b map[string]string) map[string]string {
		m := map[string]string{}
		for k, v := range a {
			m[k] = v
		}
		for k, v := range b {
			m[k] = v
		}
		return m
	}
	for key, pkg := range doc.Packages {
		k := strings.TrimPrefix(key, "/") // v6 keys start with "/"
		p.integrity[stripPeers(k)] = pkg.Resolution.Integrity
		if doc.Snapshots == nil { // v6: dependencies live on packages
			p.snapshots[k] = merge(pkg.Dependencies, pkg.OptionalDependencies)
		}
	}
	for key, s := range doc.Snapshots {
		p.snapshots[key] = merge(s.Dependencies, s.OptionalDependencies)
	}
	return p, nil
}

// stripPeers drops pnpm's peer suffix: "a@1.0.0(react@19.0.0)" -> "a@1.0.0".
func stripPeers(s string) string {
	if i := strings.IndexByte(s, '('); i >= 0 {
		return s[:i]
	}
	return s
}

func (p *pnpmLock) pin(requester *node, name, rng string) (string, string, string, error) {
	var ref string
	var ok bool
	if requester.m == nil {
		ref, ok = p.importers["."][name]
		if !ok {
			return "", "", "", fmt.Errorf("%s is not in pnpm-lock.yaml; the lockfile is out of date with package.json", name)
		}
	} else {
		deps, found := p.snapshots[requester.lockKey]
		if !found {
			return "", "", "", fmt.Errorf("%s is not in pnpm-lock.yaml", requester.lockKey)
		}
		if ref, ok = deps[name]; !ok {
			return "", "", "", fmt.Errorf("%s (needed by %s) is not in pnpm-lock.yaml", name, requester.lockKey)
		}
	}
	if strings.HasPrefix(ref, "link:") || strings.HasPrefix(ref, "file:") || strings.HasPrefix(ref, "workspace:") {
		return "", "", "", fmt.Errorf("%s: workspace and linked packages are not supported yet", name)
	}
	version := stripPeers(ref)
	key := name + "@" + ref
	return version, key, p.integrity[name+"@"+version], nil
}

// --- yarn.lock --------------------------------------------------------------

type yarnEntry struct {
	version, resolved, integrity string
}

type yarnLock struct {
	berry   bool
	entries map[string]*yarnEntry // descriptor ("name@range") -> entry
}

func (y *yarnLock) file() string { return "yarn.lock" }

func (y *yarnLock) pin(_ *node, name, rng string) (string, string, string, error) {
	keys := []string{name + "@" + rng}
	if y.berry && !strings.Contains(rng, ":") {
		keys = []string{name + "@npm:" + rng, name + "@" + rng}
	}
	for _, k := range keys {
		if e, ok := y.entries[k]; ok {
			return e.version, k, e.integrity, nil
		}
	}
	return "", "", "", fmt.Errorf("%s@%s is not in yarn.lock; the lockfile is out of date with package.json", name, rng)
}

func unquote(s string) string {
	if len(s) >= 2 && s[0] == '"' {
		if u, err := strconv.Unquote(s); err == nil {
			return u
		}
	}
	return s
}

// parseYarnV1 reads Yarn 1's own lockfile format.
func parseYarnV1(b []byte) (*yarnLock, error) {
	y := &yarnLock{entries: map[string]*yarnEntry{}}
	var cur *yarnEntry
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		line = strings.TrimSpace(line)
		switch indent {
		case 0:
			if !strings.HasSuffix(line, ":") {
				return nil, fmt.Errorf("unexpected line %q", line)
			}
			cur = &yarnEntry{}
			for _, d := range strings.Split(strings.TrimSuffix(line, ":"), ",") {
				y.entries[unquote(strings.TrimSpace(d))] = cur
			}
		case 2:
			if cur == nil {
				return nil, fmt.Errorf("unexpected line %q", line)
			}
			k, v, _ := strings.Cut(line, " ")
			v = unquote(strings.TrimSpace(v))
			switch k {
			case "version":
				cur.version = v
			case "resolved":
				cur.resolved = v
			case "integrity":
				cur.integrity = v
			}
		}
	}
	return y, sc.Err()
}

// parseYarnBerry reads Yarn 2+'s YAML lockfile. Its checksums are of
// Yarn's own archives, not of registry tarballs, so integrity comes from
// the registry.
func parseYarnBerry(b []byte) (*yarnLock, error) {
	var doc map[string]struct {
		Version    string `yaml:"version"`
		Resolution string `yaml:"resolution"`
	}
	if err := yaml.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	y := &yarnLock{berry: true, entries: map[string]*yarnEntry{}}
	for key, e := range doc {
		if key == "__metadata" {
			continue
		}
		if strings.Contains(e.Resolution, "@patch:") {
			// Yarn's builtin compatibility patches (e.g. for typescript) only
			// matter to Plug'n'Play; the plain npm entry is also recorded.
			continue
		}
		if !strings.Contains(e.Resolution, "@npm:") {
			if strings.Contains(e.Resolution, "@workspace:") {
				continue // the project itself and workspaces
			}
			return nil, fmt.Errorf("%s: only npm registry packages are supported (resolution %q)", key, e.Resolution)
		}
		entry := &yarnEntry{version: e.Version}
		for _, d := range strings.Split(key, ",") {
			y.entries[strings.TrimSpace(d)] = entry
		}
	}
	return y, nil
}

// registryTarball is the default tarball URL of name@version.
func registryTarball(registry, name, version string) string {
	return registry + "/" + name + "/-/" + path.Base(name) + "-" + version + ".tgz"
}
