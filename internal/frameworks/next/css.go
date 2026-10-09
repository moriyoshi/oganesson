package next

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/grafana/sobek"
)

// CSS support (global CSS and CSS Modules), standing in for Next's webpack
// css/mini-css-extract setup:
//
//   - Each CSS source compiles to its own content-hashed static/css/*.css
//     (imports inlined, url() assets emitted under static/media/).
//   - CSS Modules compile in an isolated esbuild build under a virtual file
//     name that embeds a hash of the real path, so class names are unique,
//     deterministic, and identical in the server and browser builds, which
//     both import the resulting class map.
//   - entryCSSFiles (client reference manifest) lists, per layout/page file,
//     the stylesheets its import graph reaches, crossing from server
//     components into client components. app-render turns them into <link>
//     tags and RSC payload hints.
//
//   - Every CSS source (global, @import-ed, CSS Module) goes through
//     Next's PostCSS pipeline first, as postcss-loader does under webpack:
//     the project's postcss.config.* (e.g. Tailwind) or Next's defaults.
//
//   - Sass (.scss/.sass, and .module.scss/.module.sass) runs through Next's
//     own sass-loader and resolve-url-loader before PostCSS.
//
//   - CSS Modules `composes` (esbuild's local-css): a module composed from
//     another file is loaded under that file's virtual name, so the class
//     names agree; its rules are also bundled into the composing sheet.

var cssExts = map[string]bool{".css": true, ".scss": true, ".sass": true}

var preloadFontRe = regexp.MustCompile(`\.p\.(woff|woff2|eot|ttf|otf)$`)

func isCSS(path string) bool { return cssExts[filepath.Ext(path)] }

func isCSSModule(path string) bool {
	return strings.HasSuffix(strings.TrimSuffix(path, filepath.Ext(path)), ".module")
}

func isSass(path string) bool { ext := filepath.Ext(path); return ext == ".scss" || ext == ".sass" }

// isCSS is isCSS, plus the files next.config's turbopack rules compile to
// CSS (as: "*.css": stylus-loader's .styl).
func (b *builder) isCSS(path string) bool {
	return isCSS(path) || b.turbopackCSSExt(path)
}

// turbopackCSSExt reports a file of a turbopack CSS rule's extension.
func (b *builder) turbopackCSSExt(path string) bool {
	h := b.hooks["client"]
	if h == nil {
		return false
	}
	ext := strings.TrimPrefix(filepath.Ext(path), ".")
	for _, e := range h.CSSExtensions {
		if strings.EqualFold(e, ext) {
			return true
		}
	}
	return false
}

// cssLoadFilter matches the stylesheets the CSS build loads.
func (b *builder) cssLoadFilter() string {
	exts := []string{"css", "scss", "sass"}
	if h := b.hooks["client"]; h != nil {
		for _, e := range h.CSSExtensions {
			exts = append(exts, regexp.QuoteMeta(e))
		}
	}
	return `\.(` + strings.Join(exts, "|") + `)$`
}

type cssState struct {
	mu       sync.Mutex
	chunks   map[string]string            // source path -> output path (static/css/…)
	mappings map[string]map[string]string // CSS module path -> class name map
	outputs  map[string][]byte            // output path -> contents (CSS and assets)
}

func (b *builder) cssState() *cssState {
	b.cssOnce.Do(func() {
		b.css = &cssState{chunks: map[string]string{}, mappings: map[string]map[string]string{}, outputs: map[string][]byte{}}
	})
	return b.css
}

// cssModuleSource is what a CSS module import evaluates to in JS bundles.
func (b *builder) cssModuleSource(path string) (string, error) {
	if !isCSSModule(path) {
		return "export {};", nil // global CSS: linked via entryCSSFiles
	}
	if _, err := b.compileCSS(path); err != nil {
		return "", err
	}
	st := b.cssState()
	st.mu.Lock()
	m := st.mappings[path]
	st.mu.Unlock()
	return "export default " + mustJSON(m) + ";", nil
}

// compileCSS compiles one CSS source (once) and returns its output path.
func (b *builder) compileCSS(path string) (string, error) {
	st := b.cssState()
	st.mu.Lock()
	if out, ok := st.chunks[path]; ok {
		st.mu.Unlock()
		return out, nil
	}
	st.mu.Unlock()

	opts := api.BuildOptions{
		Bundle:           true,
		Write:            false,
		AbsWorkingDir:    b.dir,
		Outdir:           filepath.Join(b.dir, ".og-css"),
		AssetNames:       "static/media/[name].[hash]",
		PublicPath:       b.assetPrefix() + "/_next/",
		MinifyWhitespace: b.minify,
		MinifySyntax:     b.minify,
		LogLevel:         api.LogLevelSilent,
		Loader:           assetLoaders(),
		Metafile:         true,
		LegalComments:    api.LegalCommentsNone,
		Target:           api.ES2020,
		Engines:          []api.Engine{{Name: api.EngineChrome, Version: "111"}, {Name: api.EngineSafari, Version: "16.4"}, {Name: api.EngineFirefox, Version: "111"}},
		GlobalName:       "__ogCSSModule",
		Format:           api.FormatIIFE,
	}
	module := isCSSModule(path)
	fontCSS, isFont, err := b.fontCSS(path)
	if err != nil {
		return "", err
	}
	if isFont {
		// next/font stylesheets skip the user's PostCSS and CSS plugins, as
		// Next's font rule has no postcss-loader; their font URLs are already
		// final /_next/ paths (under assetPrefix, which is basePath unless
		// set: github-pages' fonts are at /gh-pages-test/_next/…).
		opts.Stdin = &api.StdinOptions{Contents: fontCSS, Loader: api.LoaderCSS, ResolveDir: b.dir, Sourcefile: path}
		opts.External = []string{b.assetPrefix() + "/_next/*"}
		opts.Format = api.FormatDefault
		opts.GlobalName = ""
		return b.finishCSS(path, api.Build(opts), opts.Outdir, false)
	}
	if module {
		opts.Stdin = &api.StdinOptions{Contents: `import s from "og-cssmod"; export default s;`, ResolveDir: b.dir}
		opts.Plugins = []api.Plugin{{Name: "og-cssmod", Setup: func(pb api.PluginBuild) {
			pb.OnResolve(api.OnResolveOptions{Filter: "^og-cssmod$"}, func(api.OnResolveArgs) (api.OnResolveResult, error) {
				return api.OnResolveResult{Path: cssModuleVirtual(path), Namespace: "og-cssmod", PluginData: path}, nil
			})
			// `composes: x from "./other.module.css"` loads the other module
			// under its own virtual name, so the composed class is the one that
			// module's compile (and its importers) use.
			pb.OnResolve(api.OnResolveOptions{Filter: `\.module\.(css|scss|sass)$`, Namespace: "og-cssmod"}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
				real := a.Path
				switch {
				case filepath.IsAbs(real):
				case strings.HasPrefix(real, "./") || strings.HasPrefix(real, "../"):
					real = filepath.Join(a.ResolveDir, real)
				default:
					r := pb.Resolve(a.Path, api.ResolveOptions{ResolveDir: a.ResolveDir, Kind: a.Kind, Namespace: "file"})
					if len(r.Errors) > 0 {
						return api.OnResolveResult{}, fmt.Errorf("%s", r.Errors[0].Text)
					}
					real = r.Path
				}
				return api.OnResolveResult{Path: cssModuleVirtual(real), Namespace: "og-cssmod", PluginData: real}, nil
			})
			pb.OnLoad(api.OnLoadOptions{Filter: ".*", Namespace: "og-cssmod"}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
				real := a.PluginData.(string)
				s, err := b.postcss(real)
				return api.OnLoadResult{Contents: &s, Loader: api.LoaderLocalCSS, ResolveDir: filepath.Dir(real)}, err
			})
		}}}
	} else {
		opts.EntryPoints = []string{path}
	}
	opts.Plugins = append(opts.Plugins, api.Plugin{Name: "og-postcss", Setup: func(pb api.PluginBuild) {
		pb.OnLoad(api.OnLoadOptions{Filter: b.cssLoadFilter(), Namespace: "file"}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			s, err := b.postcss(a.Path)
			loader := api.LoaderCSS
			if isCSSModule(a.Path) {
				loader = api.LoaderLocalCSS
			}
			return api.OnLoadResult{Contents: &s, Loader: loader, ResolveDir: filepath.Dir(a.Path)}, err
		})
	}})
	// cssPlugins (e.g. PostCSS) see the CSS module's virtual namespace as "file".
	user, err := b.userPlugins([]string{"css"}, "css", []string{"og-cssmod"}, nil)
	if err != nil {
		return "", err
	}
	opts.Plugins = append(user, opts.Plugins...)
	return b.finishCSS(path, api.Build(opts), opts.Outdir, module)
}

// finishCSS records one compiled stylesheet and its assets.
func (b *builder) finishCSS(path string, res api.BuildResult, outdir string, module bool) (string, error) {
	st := b.cssState()
	if len(res.Errors) > 0 {
		return "", buildError("css "+path, res.Errors)
	}
	if err := b.licenses.AddMetafile(res.Metafile, b.dir); err != nil {
		return "", err
	}
	var css []byte
	var mapping map[string]string
	assets := map[string][]byte{}
	for _, f := range res.OutputFiles {
		rel, _ := filepath.Rel(outdir, f.Path)
		rel = filepath.ToSlash(rel)
		switch {
		case strings.HasSuffix(rel, ".css") && !strings.HasPrefix(rel, "static/media/"):
			css = f.Contents
		case strings.HasSuffix(rel, ".js"):
			if module {
				m, err := evalCSSModuleMap(string(f.Contents))
				if err != nil {
					return "", fmt.Errorf("next: css module %s: %w", path, err)
				}
				mapping = m
			}
		default:
			assets[rel] = f.Contents
		}
	}
	sum := sha256.Sum256(css)
	out := "static/css/" + hex.EncodeToString(sum[:8]) + ".css"
	st.mu.Lock()
	defer st.mu.Unlock()
	st.chunks[path] = out
	st.outputs[out] = css
	for k, v := range assets {
		st.outputs[k] = v
	}
	if module {
		if mapping == nil {
			mapping = map[string]string{}
		}
		st.mappings[path] = mapping
	}
	return out, nil
}

// cssModuleVirtual is the virtual name a CSS Module compiles under. It
// drives esbuild's local class names, "<base>-<hash>_<class>", unique per
// source file and the same wherever the module is compiled.
func cssModuleVirtual(path string) string {
	sum := sha256.Sum256([]byte(path))
	base := strings.TrimSuffix(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)), ".module")
	return filepath.Join(filepath.Dir(path), fmt.Sprintf("%s-%s.module.css", base, hex.EncodeToString(sum[:3])))
}

// postcss reads one stylesheet and runs it through Next's PostCSS pipeline
// in the build's JS host; Sass sources go through Next's Sass
// preprocessors first.
func (b *builder) postcss(path string) (string, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if b.plugins == nil {
		if isSass(path) {
			return "", fmt.Errorf("next: %s: Sass needs the build's JS host", path)
		}
		return string(src), nil
	}
	code := string(src)
	if isSass(path) {
		if code, err = b.plugins.Sass(path); err != nil {
			return "", err
		}
	} else if b.turbopackCSSExt(path) {
		// The browser target's rules: the stylesheet is the client's.
		css, ok, err := b.plugins.TurbopackCSS("client", path)
		if err != nil {
			return "", err
		}
		if !ok {
			return "", fmt.Errorf("next: %s: no turbopack rule compiles it to CSS", path)
		}
		code = css
	}
	if experimental, _ := b.config["experimental"].(map[string]any); experimental["useLightningcss"] == true {
		return b.plugins.Lightningcss(path, code)
	}
	css, warnings, err := b.plugins.PostCSS(path, code)
	if err != nil {
		return "", err
	}
	for _, w := range warnings {
		b.log("postcss: %s", w)
	}
	return css, nil
}

// evalCSSModuleMap runs esbuild's generated IIFE to read the class map.
func evalCSSModuleMap(js string) (map[string]string, error) {
	vm := sobek.New()
	if _, err := vm.RunString(js); err != nil {
		return nil, err
	}
	v, err := vm.RunString(`JSON.stringify((__ogCSSModule && __ogCSSModule.default) || {})`)
	if err != nil {
		return nil, err
	}
	var m map[string]string
	return m, json.Unmarshal([]byte(v.String()), &m)
}

// assetLoaders emit files referenced from CSS (and imported from JS) as
// static/media assets.
func assetLoaders() map[string]api.Loader {
	m := map[string]api.Loader{}
	for _, ext := range []string{".png", ".jpg", ".jpeg", ".gif", ".webp", ".avif", ".ico", ".svg", ".woff", ".woff2", ".ttf", ".otf", ".eot"} {
		m[ext] = api.LoaderFile
	}
	return m
}

// --- import graph -> entryCSSFiles ------------------------------------------

type metaGraph map[string][]string // normalized module key -> imported keys

// parseMetafile normalizes esbuild metafile input keys: custom namespaces
// appear as "ns:/abs/path", the file namespace as a path relative to the
// working directory (turned absolute here).
func parseMetafile(meta, workDir string) (metaGraph, error) {
	var m struct {
		Inputs map[string]struct {
			Imports []struct {
				Path string `json:"path"`
			} `json:"imports"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal([]byte(meta), &m); err != nil {
		return nil, err
	}
	norm := func(k string) string {
		if i := strings.Index(k, ":"); i > 0 && !strings.Contains(k[:i], "/") {
			return k // "ns:/abs/path"
		}
		if filepath.IsAbs(k) {
			return "file:" + k
		}
		return "file:" + filepath.Join(workDir, k)
	}
	g := metaGraph{}
	for k, in := range m.Inputs {
		var imps []string
		for _, imp := range in.Imports {
			imps = append(imps, norm(imp.Path))
		}
		g[norm(k)] = imps
	}
	return g, nil
}

func keyPath(k string) string {
	if i := strings.Index(k, ":"); i > 0 {
		return k[i+1:]
	}
	return k
}

// entryCSS computes entryCSSFiles: for each layout/page/boundary file in
// the loader trees, the compiled stylesheets its imports reach, in order.
func (b *builder) entryCSS(server, browser metaGraph) (map[string]any, error) {
	files := map[string]bool{}
	for _, t := range b.trees {
		for _, d := range t.Declarations {
			if filepath.IsAbs(d.Path) {
				files[d.Path] = true
			}
		}
	}
	out := map[string]any{}
	for file := range files {
		var order, fonts []string
		seen := map[string]bool{}
		var walk func(inServer bool, key string)
		walk = func(inServer bool, key string) {
			if seen[key] {
				return
			}
			seen[key] = true
			p := keyPath(key)
			if files := b.fontFilesFor(key); len(files) > 0 {
				fonts = append(fonts, files...)
			}
			if b.isCSS(p) {
				order = append(order, p)
				return
			}
			b.mu.Lock()
			_, isClient := b.clients[p]
			b.mu.Unlock()
			if isClient && inServer {
				// The RSC proxy imports nothing; continue in the browser graph,
				// where the client module's real imports live.
				walk(false, "file:"+cjsTwin(p))
				return
			}
			g := browser
			if inServer {
				g = server
			}
			for _, imp := range g[key] {
				walk(inServer, imp)
			}
		}
		walk(true, "rsc:"+file)
		// NextFontManifestPlugin: the entry's preloadable (".p.") font files;
		// an entry with fonts but none to preload still gets an (empty) list.
		if len(fonts) > 0 {
			preload := []string{}
			for _, f := range fonts {
				if preloadFontRe.MatchString(f) {
					preload = append(preload, f)
				}
				if strings.Contains(f, "-s") {
					b.appUsingSizeAdjust = true
				}
			}
			if b.fontManifest == nil {
				b.fontManifest = map[string][]string{}
			}
			b.fontManifest[strings.TrimSuffix(file, filepath.Ext(file))] = preload
		}
		if len(order) == 0 {
			continue
		}
		var list []any
		for _, src := range order {
			outPath, err := b.compileCSS(src)
			if err != nil {
				return nil, err
			}
			list = append(list, map[string]any{"path": outPath, "inlined": false})
		}
		out[strings.TrimSuffix(file, filepath.Ext(file))] = list
	}
	return out, nil
}
