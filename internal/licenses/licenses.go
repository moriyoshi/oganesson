// Package licenses collects the license texts of the npm packages an app
// build ships: every module under node_modules that an esbuild bundle of the
// app's server or browser output read (from its metafile). The text goes into
// the payload as notices.AppFile, after og's own notices.
//
// A module is attributed to the nearest package.json with a "name" between
// it and its node_modules package directory, so a copy vendored inside
// another package (next/dist/compiled/react-dom) is credited to the package
// it is, with that copy's license file when it has one.
//
// What esbuild does not read is not seen: CSS inlined by PostCSS
// (Tailwind's preflight), and files copied without bundling.
package licenses

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// Collector gathers packages from the builds of one app. A nil Collector
// ignores everything, for builds whose output is not shipped.
type Collector struct {
	mu      sync.Mutex
	dirs    map[string]bool             // attributed package directories
	pkgs    map[string]*packageManifest // package.json reads, by directory ("" name: none)
	notices []synthNotice               // synthetic sections (e.g. build-time Google Fonts)
}

// synthNotice is an attribution with no local package directory, such as
// fonts downloaded at build time.
type synthNotice struct{ name, body string }

type packageManifest struct {
	Name, Version string
	License       any // "MIT", {"type": "MIT"}, or absent
	Licenses      []struct {
		Type string `json:"type"`
	} // the deprecated array form
}

// AddMetafile records the node_modules inputs of an esbuild metafile whose
// relative input paths are relative to workDir.
func (c *Collector) AddMetafile(metafile, workDir string) error {
	if c == nil || metafile == "" {
		return nil
	}
	var m struct {
		Inputs map[string]json.RawMessage `json:"inputs"`
	}
	if err := json.Unmarshal([]byte(metafile), &m); err != nil {
		return fmt.Errorf("licenses: metafile: %w", err)
	}
	for k := range m.Inputs {
		if p := inputPath(k, workDir); p != "" {
			c.AddFile(p)
		}
	}
	return nil
}

// inputPath is the file a metafile input key names, or "" for a virtual
// module: keys are "ns:/abs/path" for plugin namespaces (with a "\x00"
// prefix or a "?query" from Vite ids) and working-directory-relative paths
// for the file namespace.
func inputPath(key, workDir string) string {
	if i := strings.Index(key, ":"); i > 1 && !strings.ContainsAny(key[:i], `/\`) {
		key = key[i+1:]
	}
	key = strings.TrimPrefix(key, "\x00")
	if i := strings.IndexByte(key, '?'); i >= 0 {
		key = key[:i]
	}
	if key == "" {
		return ""
	}
	if !filepath.IsAbs(key) {
		key = filepath.Join(workDir, key)
	}
	return filepath.Clean(key)
}

// AddFile records the package that file (an absolute path) belongs to, if
// it is under node_modules.
func (c *Collector) AddFile(file string) {
	if c == nil {
		return
	}
	slash := filepath.ToSlash(file)
	i := strings.LastIndex(slash, "/node_modules/")
	if i < 0 {
		return
	}
	rest := strings.Split(slash[i+len("/node_modules/"):], "/")
	n := 1
	if strings.HasPrefix(rest[0], "@") {
		n = 2
	}
	if len(rest) <= n { // node_modules/x itself, or a file directly in node_modules
		return
	}
	root := filepath.FromSlash(slash[:i] + "/node_modules/" + strings.Join(rest[:n], "/"))
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dirs == nil {
		c.dirs, c.pkgs = map[string]bool{}, map[string]*packageManifest{}
	}
	for dir := filepath.Dir(file); ; dir = filepath.Dir(dir) {
		if m := c.manifest(dir); m.Name != "" || dir == root || len(dir) <= len(root) {
			if m.Name == "" {
				dir = root
			}
			c.dirs[dir] = true
			return
		}
	}
}

// AddNotice records a synthetic attribution section (name and body) for
// content with no local package directory, such as fonts downloaded at build
// time. Repeated names are recorded once.
func (c *Collector) AddNotice(name, body string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, n := range c.notices {
		if n.name == name {
			return
		}
	}
	c.notices = append(c.notices, synthNotice{name, body})
}

// manifest reads dir/package.json once; c.mu is held.
func (c *Collector) manifest(dir string) *packageManifest {
	if m, ok := c.pkgs[dir]; ok {
		return m
	}
	m := &packageManifest{}
	if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		_ = json.Unmarshal(b, m)
	}
	c.pkgs[dir] = m
	return m
}

// licenseFiles are a package directory's license and notice files.
func licenseFiles(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var files []string
	for _, e := range entries {
		n := strings.ToUpper(e.Name())
		if !e.IsDir() && (strings.HasPrefix(n, "LICENSE") || strings.HasPrefix(n, "LICENCE") || strings.HasPrefix(n, "COPYING") || strings.HasPrefix(n, "NOTICE")) {
			files = append(files, filepath.Join(dir, e.Name()))
		}
	}
	slices.Sort(files)
	return files
}

// shallower orders package directories by depth, then by path.
func shallower(a, b string) bool {
	da, db := strings.Count(a, string(filepath.Separator)), strings.Count(b, string(filepath.Separator))
	if da != db {
		return da < db
	}
	return a < b
}

// declared is the license package.json declares, or "".
func (m *packageManifest) declared() string {
	switch l := m.License.(type) {
	case string:
		return l
	case map[string]any:
		if t, ok := l["type"].(string); ok {
			return t
		}
	}
	var types []string
	for _, l := range m.Licenses {
		if l.Type != "" {
			types = append(types, l.Type)
		}
	}
	return strings.Join(types, " OR ")
}

// Text renders the collected packages, sorted by name and version, one
// section per name@version; nil when no package was collected.
func (c *Collector) Text() ([]byte, error) {
	if c == nil {
		return nil, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	type pkg struct {
		id, dir string
		m       *packageManifest
	}
	byID := map[string]pkg{}
	for dir := range c.dirs {
		m := c.manifest(dir)
		name := m.Name
		if name == "" {
			name = filepath.Base(dir)
			if parent := filepath.Base(filepath.Dir(dir)); strings.HasPrefix(parent, "@") {
				name = parent + "/" + name
			}
		}
		id := name + " " + m.Version
		// The same name@version installed twice: keep the shallowest copy
		// (then the first by path), deterministically.
		if p, ok := byID[id]; !ok || shallower(dir, p.dir) {
			byID[id] = pkg{id: strings.TrimSpace(id), dir: dir, m: m}
		}
	}
	if len(byID) == 0 && len(c.notices) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	slices.Sort(ids)

	var buf bytes.Buffer
	buf.WriteString(`npm packages bundled into this app

The packages below were bundled into this app's server or browser code by
og build, from the app's node_modules. Each is distributed under its own
license, reproduced here when the package ships a license file.

`)
	for _, id := range ids {
		p := byID[id]
		title := p.id
		if l := p.m.declared(); l != "" {
			title += " (" + l + ")"
		}
		fmt.Fprintf(&buf, "%s\n%s\n\n", strings.Repeat("=", 80), title)
		files := licenseFiles(p.dir)
		if len(files) == 0 {
			if l := p.m.declared(); l != "" {
				fmt.Fprintf(&buf, "The package ships no license file; its package.json declares %s.\n\n", l)
			} else {
				buf.WriteString("The package ships no license file and declares no license.\n\n")
			}
			continue
		}
		for _, f := range files {
			b, err := os.ReadFile(f)
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(&buf, "--- %s\n\n%s\n\n", filepath.Base(f), bytes.TrimRight(b, "\n"))
		}
	}
	notices := slices.Clone(c.notices)
	slices.SortFunc(notices, func(a, b synthNotice) int { return strings.Compare(a.name, b.name) })
	for _, n := range notices {
		fmt.Fprintf(&buf, "%s\n%s\n\n%s\n\n", strings.Repeat("=", 80), n.name, strings.TrimRight(n.body, "\n"))
	}
	return buf.Bytes(), nil
}
