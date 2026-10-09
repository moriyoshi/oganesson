package next

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/moriyoshi/oganesson/internal/bundle/rsc"
	"github.com/moriyoshi/oganesson/internal/jshost"
)

// next.config's webpack(config, options) is where plugins and apps extend
// Next's webpack build: @next/mdx adds its loader for .mdx files,
// @svgr/webpack turns .svg imports into components, apps alias modules and
// define constants. og runs the hook for each target against a stand-in
// webpack config (the build host's webpackHook) and honors what it can:
//
//   - module rules: their loaders run on the files they match, through og's
//     webpack loader shim (the "webpack-server" / "webpack-client" plugins);
//   - resolve.alias (and the client's resolve.fallback): applied by og's
//     resolvers, candidates in order, false an empty module;
//   - DefinePlugin definitions: added to the target's defines.
//
// Other changes to the config (plugins, optimization, externals) have no
// esbuild counterpart and are ignored.
//
// Under Turbopack (next build's default since Next 16, bundler.go) the
// turbopack section applies instead of webpack(): its rules (loaders
// whose output is JavaScript go through the same shim, those yielding CSS
// into the stylesheet build) and resolveAlias.

// runWebpackHooks runs the hook for the server and the client.
func (b *builder) runWebpackHooks(log func(string, ...any)) error {
	if b.plugins == nil {
		return nil
	}
	if b.buildID == "" {
		b.buildID = randomHex(12)
	}
	b.hooks = map[string]*jshost.WebpackHook{}
	babel := b.babelConfig()
	if babel != nil {
		log("using %s: the project's files compile with Next's Babel loader (next build disables SWC for them)", filepath.Base(babel.ConfigFile))
	}
	for _, target := range []string{"server", "client"} {
		h, err := b.plugins.WebpackHook(target, b.dir, b.buildID, babel)
		if err != nil {
			return err
		}
		if h == nil {
			return nil // no webpack() in next.config
		}
		b.hooks[target] = h
		defines := b.defines
		if target == "client" {
			defines = b.clientDefines
		}
		for k, v := range h.Defines {
			defines[k] = v
		}
		if target == "server" && h.Rules > 0 {
			what := "webpack()"
			if h.Bundler == "turbopack" {
				what = "turbopack"
			}
			log("next.config %s: %d module rule(s) for %s files", what, h.Rules, strings.Join(h.Extensions, ", "))
		}
	}
	return nil
}

// probeExports lists a module's exports (rsc.ProbeExports), as the
// webpack() hook's loaders compile it when they apply: an .mdx page, a page
// in syntax only the project's Babel config knows (do-expressions).
func (b *builder) probeExports(path string) ([]string, error) {
	if b.hooks["server"] == nil {
		return rsc.ProbeExports(path)
	}
	code, ok, err := b.plugins.WebpackLoad("server", path)
	if err != nil {
		return nil, err
	}
	if !ok {
		return rsc.ProbeExports(path)
	}
	return rsc.ProbeExportsSource(path, code, api.LoaderJSX)
}

// hookAlias maps spec through the hook's aliases for target (fallbacks
// too, when fallback): the candidate specifiers in order ("" is an empty
// module), as webpack's resolve.alias matches: a key names the module and
// its subpaths, a key ending in "$" the module only. The longest key wins.
func (b *builder) hookAlias(target, spec string, fallback bool) ([]string, bool) {
	h := b.hooks[target]
	if h == nil {
		return nil, false
	}
	table := h.Aliases
	if fallback {
		table = h.Fallbacks
	}
	keys := make([]string, 0, len(table))
	for k := range table {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, k := range keys {
		exact := strings.HasSuffix(k, "$")
		name := strings.TrimSuffix(k, "$")
		rest := ""
		switch {
		case spec == name:
		case !exact && strings.HasPrefix(spec, name+"/"):
			rest = spec[len(name):]
		default:
			continue
		}
		var out []string
		for _, t := range table[k] {
			if t == "" {
				out = append(out, "")
			} else {
				out = append(out, t+rest)
			}
		}
		return out, true
	}
	return nil, false
}

// resolveHookAlias resolves spec through the hook's aliases: the first
// candidate that resolves (through every plugin, in the importer's
// namespace), an empty module for false. ok is false when no alias names
// spec.
func (b *builder) resolveHookAlias(pb api.PluginBuild, target string, a api.OnResolveArgs, fallback bool) (api.OnResolveResult, bool, error) {
	candidates, ok := b.hookAlias(target, a.Path, fallback)
	if !ok {
		return api.OnResolveResult{}, false, nil
	}
	for _, c := range candidates {
		if c == "" {
			return api.OnResolveResult{Path: a.Path, Namespace: emptyNamespace}, true, nil
		}
		r := pb.Resolve(c, api.ResolveOptions{Kind: a.Kind, Importer: a.Importer, Namespace: a.Namespace, ResolveDir: a.ResolveDir, PluginData: aliasedMarker})
		if len(r.Errors) > 0 {
			continue
		}
		out := api.OnResolveResult{Path: r.Path, Namespace: r.Namespace, External: r.External, Suffix: r.Suffix, PluginData: r.PluginData}
		if !r.SideEffects {
			out.SideEffects = api.SideEffectsFalse
		}
		return out, true, nil
	}
	return api.OnResolveResult{}, false, nil
}

// aliasedMarker marks a resolution of an alias's candidate, which is not
// aliased again (an alias of "x" to "x/dist" would recurse).
var aliasedMarker = &struct{ _ byte }{}

// babelConfigFiles are the files next build looks for (getBabelConfigFile).
var babelConfigFiles = []string{".babelrc", ".babelrc.json", ".babelrc.js", ".babelrc.mjs", ".babelrc.cjs", "babel.config.js", "babel.config.json", "babel.config.mjs", "babel.config.cjs"}

// babelConfig is the project's Babel configuration, unless
// experimental.forceSwcTransforms keeps SWC.
func (b *builder) babelConfig() *jshost.BabelConfig {
	if experimental, _ := b.config["experimental"].(map[string]any); experimental["forceSwcTransforms"] == true {
		return nil
	}
	for _, name := range babelConfigFiles {
		p := filepath.Join(b.dir, name)
		if fileExists(p) {
			cfg := &jshost.BabelConfig{ConfigFile: p, SrcDir: b.dir}
			for _, d := range []string{"pages", "src/pages"} {
				if dirExists(filepath.Join(b.dir, d)) {
					cfg.PagesDir = filepath.Join(b.dir, d)
					cfg.SrcDir = cfg.PagesDir
					break
				}
			}
			for _, d := range []string{"app", "src/app"} {
				if dirExists(filepath.Join(b.dir, d)) {
					cfg.SrcDir = filepath.Join(b.dir, d)
					break
				}
			}
			return cfg
		}
	}
	return nil
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
