// Package rsc implements the React Server Components module boundary for
// esbuild: in the server (react-server) layer, "use client" modules are
// replaced by client-reference proxies, and the discovered client modules
// feed the SSR and browser builds and the Flight manifests.
package rsc

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/evanw/esbuild/pkg/api"
)

// ClientModule is one "use client" module discovered in the server layer.
type ClientModule struct {
	ID      string   // stable module id (root-relative path, forward slashes)
	Path    string   // absolute path
	Exports []string // export names; "default" included when present
}

// Graph collects client modules across a build. It is safe for concurrent
// use by esbuild's parallel OnLoad callbacks.
type Graph struct {
	Root string // module ids are relative to Root

	mu      sync.Mutex
	clients map[string]*ClientModule
}

// NewGraph returns an empty graph rooted at root.
func NewGraph(root string) *Graph {
	return &Graph{Root: root, clients: map[string]*ClientModule{}}
}

// Clients returns the discovered client modules sorted by id.
func (g *Graph) Clients() []*ClientModule {
	g.mu.Lock()
	defer g.mu.Unlock()
	out := make([]*ClientModule, 0, len(g.clients))
	for _, m := range g.clients {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// DirectivePrologue matches what may precede a directive: whitespace,
// comments and other directives ("use strict"; then 'use client', as
// TypeScript's CommonJS output of a client module starts).
const DirectivePrologue = `^(?:\s|//[^\n]*\n|/\*[\s\S]*?\*/|'[^'\n]*'\s*;?|"[^"\n]*"\s*;?)*`

var useClient = regexp.MustCompile(DirectivePrologue + `['"]use client['"]`)

// HasUseClient reports whether source's directive prologue has a "use
// client" directive.
func HasUseClient(src []byte) bool { return useClient.Match(src) }

// ServerReferenceModule is the import specifier used for registerClientReference.
const ServerReferenceModule = "react-server-dom-webpack/server.edge"

// ClientBoundaryPlugin rewrites "use client" modules for the server layer.
// Modules under node_modules are included: libraries ship client components
// too.
func (g *Graph) ClientBoundaryPlugin() api.Plugin {
	return api.Plugin{
		Name: "og-rsc-client-boundary",
		Setup: func(b api.PluginBuild) {
			b.OnLoad(api.OnLoadOptions{Filter: `\.[cm]?[jt]sx?$`}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
				src, err := os.ReadFile(a.Path)
				if err != nil || !HasUseClient(src) {
					return api.OnLoadResult{}, err
				}
				mod, err := g.add(a.Path)
				if err != nil {
					return api.OnLoadResult{}, err
				}
				contents := proxySource(mod)
				return api.OnLoadResult{Contents: &contents, Loader: api.LoaderJS, ResolveDir: filepath.Dir(a.Path)}, nil
			})
		},
	}
}

func (g *Graph) add(path string) (*ClientModule, error) {
	rel, err := filepath.Rel(g.Root, path)
	if err != nil {
		return nil, err
	}
	id := filepath.ToSlash(rel)
	exports, err := ProbeExports(path)
	if err != nil {
		return nil, err
	}
	mod := &ClientModule{ID: id, Path: path, Exports: exports}
	g.mu.Lock()
	g.clients[id] = mod
	g.mu.Unlock()
	return mod, nil
}

func proxySource(m *ClientModule) string {
	var b strings.Builder
	fmt.Fprintf(&b, "import { registerClientReference } from %q;\n", ServerReferenceModule)
	for _, name := range m.Exports {
		ref := fmt.Sprintf("registerClientReference(function () { throw new Error(%q); }, %q, %q)",
			"Attempted to call "+name+"() from the server but "+name+" is on the client. "+
				"It's not possible to invoke a client function from the server, it can only be rendered as a Component or passed to props of a Client Component.",
			m.ID, name)
		if name == "default" {
			fmt.Fprintf(&b, "export default %s;\n", ref)
		} else {
			fmt.Fprintf(&b, "export const %s = %s;\n", name, ref)
		}
	}
	return b.String()
}

// probeLoaders lets a project's own JavaScript contain JSX, as Next's SWC
// parses it (packages' may not).
func probeLoaders(path string) map[string]api.Loader {
	if strings.Contains(path, "/node_modules/") {
		return nil
	}
	return map[string]api.Loader{".js": api.LoaderJSX, ".mjs": api.LoaderJSX, ".cjs": api.LoaderJSX}
}

// ProbeExportsSource is ProbeExports for a module's compiled source (a
// file a loader turns into JavaScript, such as an .mdx page).
func ProbeExportsSource(path, contents string, loader api.Loader) ([]string, error) {
	res := api.Build(api.BuildOptions{
		Stdin:    &api.StdinOptions{Contents: contents, Loader: loader, ResolveDir: filepath.Dir(path), Sourcefile: path},
		Format:   api.FormatESModule,
		Metafile: true,
		Write:    false,
		Outdir:   "/probe",
		JSX:      api.JSXAutomatic,
		LogLevel: api.LogLevelSilent,
	})
	if len(res.Errors) > 0 {
		return nil, fmt.Errorf("rsc: probing exports of %s: %s", path, res.Errors[0].Text)
	}
	return metafileExports(res.Metafile)
}

// ProbeExports asks esbuild for a module's export names via its metafile,
// without bundling its dependencies.
func ProbeExports(path string) ([]string, error) {
	res := api.Build(api.BuildOptions{
		EntryPoints: []string{path},
		Format:      api.FormatESModule,
		Metafile:    true,
		Write:       false,
		Outdir:      "/probe",
		JSX:         api.JSXAutomatic,
		LogLevel:    api.LogLevelSilent,
		Loader:      probeLoaders(path),
	})
	if len(res.Errors) > 0 {
		return nil, fmt.Errorf("rsc: probing exports of %s: %s", path, res.Errors[0].Text)
	}
	return metafileExports(res.Metafile)
}

// metafileExports reads the probe's one output's export names.
func metafileExports(metafile string) ([]string, error) {
	var meta struct {
		Outputs map[string]struct {
			Exports []string `json:"exports"`
		} `json:"outputs"`
	}
	if err := json.Unmarshal([]byte(metafile), &meta); err != nil {
		return nil, err
	}
	for _, o := range meta.Outputs {
		return o.Exports, nil
	}
	return nil, nil
}

// ManifestEntry is React's {id, chunks, name} module reference.
type ManifestEntry struct {
	ID     string   `json:"id"`
	Chunks []string `json:"chunks"`
	Name   string   `json:"name"`
}

// Manifests builds the server-side client manifest (keyed "id#export") and
// the SSR consumer module map (id -> export -> entry). chunks maps a module
// id to the browser chunks that must load before it (nil for SSR-only use).
func (g *Graph) Manifests(chunks map[string][]string) (client map[string]ManifestEntry, consumer map[string]map[string]ManifestEntry) {
	client = map[string]ManifestEntry{}
	consumer = map[string]map[string]ManifestEntry{}
	for _, m := range g.Clients() {
		perExport := map[string]ManifestEntry{}
		cs := chunks[m.ID]
		if cs == nil {
			cs = []string{}
		}
		for _, name := range m.Exports {
			e := ManifestEntry{ID: m.ID, Chunks: cs, Name: name}
			client[m.ID+"#"+name] = e
			perExport[name] = e
		}
		consumer[m.ID] = perExport
	}
	return client, consumer
}
