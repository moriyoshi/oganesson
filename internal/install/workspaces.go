package install

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/goccy/go-yaml"
)

// Workspaces (npm 7, Yarn, pnpm): a root package.json's "workspaces" (a
// list of globs, or { "packages": [...] }), or pnpm-workspace.yaml's
// "packages", names local packages ("!" globs exclude; "**" is not
// supported). As npm does, each
// is linked at node_modules/<name> (a symlink to its directory) and its
// dependencies are installed with the rest: hoisted to the root when free,
// else nested under the link, which places them in the workspace's own
// node_modules. A dependency on a workspace ("*", a range its version
// satisfies, or "workspace:...") is the link.

// workspace is one local package of the root's workspaces.
type workspace struct {
	dir string // relative to the root
	m   *manifest
}

// workspaces lists the root's workspace packages, by name: a directory
// without package.json is none; one without a name is named after its
// directory (@scope/dir under an @scope directory), as npm's
// map-workspaces names it (@npmcli/name-from-folder).
func workspaces(root string, raw json.RawMessage) (map[string]*workspace, error) {
	var globs []string
	if len(raw) > 0 && json.Unmarshal(raw, &globs) != nil {
		var obj struct {
			Packages []string `json:"packages"`
		}
		_ = json.Unmarshal(raw, &obj)
		globs = obj.Packages
	}
	if len(raw) == 0 {
		if b, err := os.ReadFile(filepath.Join(root, "pnpm-workspace.yaml")); err == nil {
			var ws struct {
				Packages []string `yaml:"packages"`
			}
			if err := yaml.Unmarshal(b, &ws); err != nil {
				return nil, fmt.Errorf("install: pnpm-workspace.yaml: %w", err)
			}
			globs = ws.Packages
		}
	}
	if len(globs) == 0 {
		return nil, nil
	}
	excluded := map[string]bool{}
	for _, g := range globs {
		if neg, ok := strings.CutPrefix(g, "!"); ok {
			matches, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(strings.TrimSuffix(strings.TrimPrefix(neg, "./"), "/"))))
			for _, m := range matches {
				excluded[m] = true
			}
		}
	}
	out := map[string]*workspace{}
	for _, g := range globs {
		if strings.HasPrefix(g, "!") {
			continue
		}
		g = strings.TrimSuffix(strings.TrimPrefix(g, "./"), "/")
		matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(g)))
		if err != nil {
			return nil, err
		}
		sort.Strings(matches)
		for _, dir := range matches {
			if excluded[dir] {
				continue
			}
			b, err := os.ReadFile(filepath.Join(dir, "package.json"))
			if err != nil {
				continue
			}
			var m manifest
			if json.Unmarshal(b, &m) != nil {
				continue
			}
			if m.Name == "" {
				m.Name = filepath.Base(dir)
				if parent := filepath.Base(filepath.Dir(dir)); strings.HasPrefix(parent, "@") {
					m.Name = parent + "/" + m.Name
				}
			}
			rel, err := filepath.Rel(root, dir)
			if err != nil {
				return nil, err
			}
			out[m.Name] = &workspace{dir: rel, m: &m}
		}
	}
	return out, nil
}

// satisfiedByWorkspace reports a dependency on a workspace package that its
// link answers.
func (in *installer) satisfiedByWorkspace(name, rng string) bool {
	ws := in.workspaces[name]
	if ws == nil {
		return false
	}
	if strings.HasPrefix(rng, "workspace:") || rng == "" || rng == "*" || rng == "latest" {
		return true
	}
	return ws.m.Version != "" && satisfies(ws.m.Version, rng)
}

// linkWorkspace makes node_modules/<name> a link to the workspace n stands
// for.
func linkWorkspace(root string, n *node) error {
	dest := filepath.Join(root, n.dir)
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	_ = os.RemoveAll(dest) // a link removes as a link, its target stays
	target, err := filepath.Rel(filepath.Dir(dest), filepath.Join(root, n.link))
	if err != nil {
		return err
	}
	return os.Symlink(target, dest)
}

func sortedWorkspaces(ws map[string]*workspace) []string {
	names := make([]string, 0, len(ws))
	for n := range ws {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
