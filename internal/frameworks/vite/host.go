package vite

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/microsoft/TypeScript/tsc/ogapi"
	"github.com/moriyoshi/oganesson/internal/jshost"
)

//go:embed js
var jsFiles embed.FS

func jsFile(name string) string {
	b, err := jsFiles.ReadFile("js/" + name)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// patch is an exact-once source replacement in one of Vite's files.
type patch struct{ old, new string }

// vitePatches adjust Vite's own code while it is bundled for the build
// host, keyed by path suffix.
var vitePatches = map[string][]patch{
	"/vite/dist/node/chunks/node.js": {
		// Plugins Vite swaps for a Rolldown builtin (Rust) when an
		// environment is bundled keep their own JS implementation, which
		// Vite uses for unbundled (dev) environments: import.meta.glob,
		// dynamic import variables, and aliases (@rollup/plugin-alias).
		{"if (environment.config.isBundled) return viteImportGlobPlugin({", "if (false) return viteImportGlobPlugin({"},
		{"if (environment.config.isBundled) {\n\t\t\t\tconst { include, exclude } = environment.config.build.dynamicImportVarsOptions;\n\t\t\t\treturn viteDynamicImportVarsPlugin({",
			"if (false) {\n\t\t\t\tconst { include, exclude } = environment.config.build.dynamicImportVarsOptions;\n\t\t\t\treturn viteDynamicImportVarsPlugin({"},
		{"if (environment.config.isBundled && !environment.config.resolve.alias.some((v) => v.customResolver)) return viteAliasPlugin({",
			"if (false) return viteAliasPlugin({"},
	},
	// TanStack Start prerenders (and renders the SPA shell) by fetching
	// pages from `vite preview`, which serves the server build with
	// Node's http. og serves it the way the packaged binary will instead
	// (preview.go), so what is prerendered is what og would render.
	"/@tanstack/start-plugin-core/dist/esm/vite/prerender.js": {
		{"const previewServer = await startPreviewServer(serverEnv.config);",
			"const previewServer = await globalThis[Symbol.for(\"og.rolldown\")].startPreviewFor(builder.config.root, outputDir, serverEnv.config.build.outDir);"},
	},
	// React Router prerenders by fetching pages from `vite preview` (its
	// plugin's configurePreviewServer) with a node:http client. og serves
	// them through the start script's program (react-router-serve), as the
	// packaged binary will, and fetches with fetch (manual redirects, as
	// node:http does not follow them).
	"/@react-router/dev/dist/vite.js": {
		{"const previewServer = await startPreviewServer(viteConfig);",
			"const previewServer = await globalThis[Symbol.for(\"og.rolldown\")].startProgramPreview(viteConfig.root);"},
		{"const response = await nodeHttpFetch(new Request(url, request), { signal });",
			"const response = await fetch(new Request(url, request), { signal, redirect: \"manual\" });"},
	},
}

func applyPatches(path, src string) (string, error) {
	for suffix, ps := range vitePatches {
		if !strings.HasSuffix(filepath.ToSlash(path), suffix) {
			continue
		}
		for _, p := range ps {
			if n := strings.Count(src, p.old); n != 1 {
				return "", fmt.Errorf("vite: %s: expected exactly one %q, found %d (unsupported Vite version?)", path, p.old, n)
			}
			src = strings.Replace(src, p.old, p.new, 1)
		}
	}
	return src, nil
}

// toStringRe finds NAME.toString() calls.
var toStringRe = regexp.MustCompile(`\b([A-Za-z_$][\w$]*)\.toString\(\)`)

// preserveFunctionSources replaces NAME.toString() in out (the file being
// prepared) with the original source text (src) of the file's top-level
// function NAME. Vite stringifies functions
// into browser code (the preload helper, detectScriptRel, the dynamic
// import and wasm helpers); bundled for the build host they are no longer
// the original text (esbuild renames locals and lowers import.meta).
func preserveFunctionSources(src, out string) string {
	if !toStringRe.MatchString(out) {
		return out
	}
	m := ogapi.ParseModule("/vite-node.js", src)
	sources := map[string]string{}
	for _, st := range m.Statements {
		if st.Kind == "function" && len(st.Names) == 1 {
			sources[st.Names[0]] = src[st.Start:st.End]
		}
	}
	for _, v := range m.Vars {
		for _, d := range v.Decls {
			text := src[d.Start:d.End]
			if d.Name == "" || !strings.HasPrefix(text, d.Name) {
				continue
			}
			init := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text[len(d.Name):]), "="))
			if strings.HasPrefix(init, "function") || strings.HasPrefix(init, "async") || strings.HasPrefix(init, "(") {
				sources[d.Name] = init
			}
		}
	}
	return toStringRe.ReplaceAllStringFunc(out, func(call string) string {
		name := toStringRe.FindStringSubmatch(call)[1]
		if text, ok := sources[name]; ok {
			b, _ := json.Marshal(text)
			return string(b)
		}
		return call
	})
}

// probeMarker marks the resolution a require() probe runs itself.
const probeMarker = "og-vite-probe"

// optionalPeers are Vite's optional peer dependencies, imported lazily
// when a feature needs them; left external, a missing one fails only then.
const optionalPeers = `^(esbuild|@vitejs/devtools(/.*)?|jiti|tsx|terser|sass|sass-embedded|less|stylus|sugarss|yaml)$`

// isOptionalPeer reports whether the package containing importer declares
// spec's package an optional peer dependency.
func isOptionalPeer(importer, spec string) bool {
	if importer == "" || !filepath.IsAbs(importer) {
		return false
	}
	name := spec
	if parts := strings.SplitN(spec, "/", 3); strings.HasPrefix(spec, "@") && len(parts) > 1 {
		name = parts[0] + "/" + parts[1]
	} else {
		name = parts[0]
	}
	for dir := filepath.Dir(importer); filepath.Dir(dir) != dir; dir = filepath.Dir(dir) {
		b, err := os.ReadFile(filepath.Join(dir, "package.json"))
		if err != nil {
			continue
		}
		var pkg struct {
			Name                 string                             `json:"name"`
			PeerDependenciesMeta map[string]struct{ Optional bool } `json:"peerDependenciesMeta"`
		}
		if json.Unmarshal(b, &pkg) != nil || pkg.Name == "" {
			continue // a nested package.json without a name ("type": "module" markers)
		}
		return pkg.PeerDependenciesMeta[name].Optional
	}
	return false
}

// hostPlugins are the esbuild plugins for bundling Vite (and the project's
// vite.config) into the build host.
func hostPlugins() []api.Plugin {
	return []api.Plugin{{
		Name: "og-vite-host",
		Setup: func(b api.PluginBuild) {
			b.OnResolve(api.OnResolveOptions{Filter: optionalPeers}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
				return api.OnResolveResult{Path: a.Path, External: true}, nil
			})
			// A require() / require.resolve() that does not resolve fails at
			// run time in Node, where code catches it (Babel probes optional
			// presets this way): left external, it throws MODULE_NOT_FOUND
			// through the host's require. Static imports still must resolve.
			// An import of a package the importer declares as an optional
			// peer dependency (peerDependenciesMeta), when not installed, is
			// left external the same way (vite-plugin-pwa's lazily loaded
			// @vite-pwa/assets-generator).
			b.OnResolve(api.OnResolveOptions{Filter: `^[^./]`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
				isRequire := a.Kind == api.ResolveJSRequireCall || a.Kind == api.ResolveJSRequireResolve
				if a.PluginData == probeMarker || (!isRequire && !isOptionalPeer(a.Importer, a.Path)) {
					return api.OnResolveResult{}, nil
				}
				r := b.Resolve(a.Path, api.ResolveOptions{Importer: a.Importer, ResolveDir: a.ResolveDir, Kind: a.Kind, Namespace: a.Namespace, PluginData: probeMarker})
				if len(r.Errors) > 0 {
					return api.OnResolveResult{Path: a.Path, External: true}, nil
				}
				res := api.OnResolveResult{Path: r.Path, Namespace: r.Namespace, External: r.External, PluginData: r.PluginData}
				if !r.SideEffects {
					res.SideEffects = api.SideEffectsFalse
				}
				return res, nil
			})
			// Vite imports { rolldown } from "rolldown": the facade, which
			// re-exports the real package as og:rolldown-real.
			b.OnResolve(api.OnResolveOptions{Filter: `^rolldown$`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
				if a.Namespace == "og-vite-facade" {
					return api.OnResolveResult{}, nil
				}
				return api.OnResolveResult{Path: "rolldown", Namespace: "og-vite-facade", PluginData: a.ResolveDir}, nil
			})
			b.OnResolve(api.OnResolveOptions{Filter: `^og:rolldown-real$`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
				r := b.Resolve("rolldown", api.ResolveOptions{ResolveDir: a.ResolveDir, Kind: api.ResolveJSImportStatement, Namespace: "og-vite-facade"})
				if len(r.Errors) > 0 {
					return api.OnResolveResult{}, fmt.Errorf("vite: resolving rolldown: %s", r.Errors[0].Text)
				}
				return api.OnResolveResult{Path: r.Path}, nil
			})
			b.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: "og-vite-facade"}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
				src := jsFile("rolldown.js")
				dir, _ := a.PluginData.(string)
				return api.OnLoadResult{Contents: &src, Loader: api.LoaderJS, ResolveDir: dir}, nil
			})
			// Rolldown's parser (Oxc) -> acorn.
			b.OnLoad(api.OnLoadOptions{Filter: `rolldown/dist/shared/parse-[^/]*\.mjs$`}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
				src := jsFile("parse.js")
				return api.OnLoadResult{Contents: &src, Loader: api.LoaderJS}, nil
			})
			b.OnResolve(api.OnResolveOptions{Filter: `^og:acorn$`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
				return api.OnResolveResult{Path: "vendor/acorn/acorn.mjs", Namespace: "og-vite-embedded"}, nil
			})
			// @jridgewell/remapping (vendored), and its dependencies'
			// imports of each other.
			b.OnResolve(api.OnResolveOptions{Filter: `^og:sourcemap-codec$`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
				return api.OnResolveResult{Path: "vendor/@jridgewell/sourcemap-codec/sourcemap-codec.mjs", Namespace: "og-vite-embedded"}, nil
			})
			b.OnResolve(api.OnResolveOptions{Filter: `^og:remapping$`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
				return api.OnResolveResult{Path: "vendor/@jridgewell/remapping/remapping.mjs", Namespace: "og-vite-embedded"}, nil
			})
			b.OnResolve(api.OnResolveOptions{Filter: `^@jridgewell/[a-z-]+$`, Namespace: "og-vite-embedded"}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
				name := strings.TrimPrefix(a.Path, "@jridgewell/")
				return api.OnResolveResult{Path: "vendor/@jridgewell/" + name + "/" + name + ".mjs", Namespace: "og-vite-embedded"}, nil
			})
			b.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: "og-vite-embedded"}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
				src := jsFile(a.Path)
				return api.OnLoadResult{Contents: &src, Loader: api.LoaderJS}, nil
			})
			// Rolldown's native binding.
			b.OnLoad(api.OnLoadOptions{Filter: `rolldown/dist/shared/binding-[^/]*\.mjs$`}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
				src := jsFile("binding-stub.js")
				return api.OnLoadResult{Contents: &src, Loader: api.LoaderJS}, nil
			})
			b.OnLoad(api.OnLoadOptions{Filter: `/(@tanstack/start-plugin-core/dist/esm/(vite/prerender|post-build)|@react-router/dev/dist/vite)\.js$`}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
				src, err := os.ReadFile(a.Path)
				if err != nil {
					return api.OnLoadResult{}, err
				}
				s, err := applyPatches(a.Path, jshost.LocalizeFile(a.Path, string(src)))
				if err != nil {
					return api.OnLoadResult{}, err
				}
				return api.OnLoadResult{Contents: &s, Loader: api.LoaderJS, ResolveDir: filepath.Dir(a.Path)}, nil
			})
			b.OnLoad(api.OnLoadOptions{Filter: `/vite/dist/node/chunks/node\.js$`}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
				src, err := os.ReadFile(a.Path)
				if err != nil {
					return api.OnLoadResult{}, err
				}
				// Function sources come from the original text; the file is
				// localized (import.meta.url, __dirname) before they are
				// spliced in as strings, which must stay verbatim.
				original := string(src)
				s, err := applyPatches(a.Path, jshost.LocalizeFile(a.Path, original))
				if err != nil {
					return api.OnLoadResult{}, err
				}
				s = preserveFunctionSources(original, s)
				return api.OnLoadResult{Contents: &s, Loader: api.LoaderJS, ResolveDir: filepath.Dir(a.Path)}, nil
			})
		},
	}}
}
