package jshost

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/grafana/sobek"
	"github.com/moriyoshi/oganesson/runtime/engine"
)

// og.config.{ts,mts,mjs,js} lets projects extend Oganesson's esbuild builds:
//
//	export default {
//	  esbuild: {
//	    plugins: [...],       // every JS build (server and browser)
//	    clientPlugins: [...], // browser build only
//	    serverPlugins: [...], // server build only
//	    cssPlugins: [...],    // CSS compilation builds
//	  },
//	  webpack: { rules: [...] }, // webpack-style loaders (see webpack.go)
//	};
//
// The plugins are ordinary esbuild JS-API plugins. They run in one Goja
// engine per build (the plugin host) and are adapted to esbuild's Go API.

var configNames = []string{"og.config.ts", "og.config.mts", "og.config.mjs", "og.config.js"}

//go:embed js/bridge.js
var bridgeSource string

//go:embed js/esbuild.js
var esbuildShimSource string

//go:embed js/webpack.js
var webpackShimSource string

// FindConfig returns the project's og.config.* path, or "".
func FindConfig(dir string) string {
	for _, n := range configNames {
		p := filepath.Join(dir, n)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// PluginHost runs a project's JS esbuild plugins for the duration of a build.
type PluginHost struct {
	e      *engine.Engine
	bridge *sobek.Object
	cancel context.CancelFunc
	done   chan struct{}

	mu      sync.Mutex
	builds  map[int64]api.PluginBuild
	nextID  int64
	release func(func(*sobek.Runtime) error)
	reads   *fileReads // node_modules asset reads of this host's build (from the Host)
}

// HostOptions configure services of the build's JS host beyond the
// og.config.* plugins.
type HostOptions struct {
	// PostCSS enables PostCSS (see PluginHost.PostCSS) and the other
	// Next.js services (Sass, next/font), which resolve Next from NextDir.
	PostCSS *PostCSSOptions
	// SassOptions is next.config's sassOptions (JSON-serializable values
	// only: functions do not survive the config's trip through JSON).
	SassOptions map[string]any
	// NextConfigFile is the project's next.config.*, evaluated (lazily) in
	// the host so values that do not survive JSON reach Next's loaders:
	// sassOptions' importers and Sass functions.
	NextConfigFile string
	// LightningCSSFeatures is experimental.lightningCssFeatures
	// ({include, exclude}), used with PostCSSOptions.UseLightningcss.
	LightningCSSFeatures map[string]any
}

// PostCSSOptions select Next.js's PostCSS setup (lazyPostCSS): the
// project's postcss.config.* or, without one, Next's defaults
// (postcss-flexbugs-fixes and postcss-preset-env for the project's
// browserslist).
type PostCSSOptions struct {
	// NextDir is where "next" resolves from (the project directory).
	NextDir string
	// Root is the directory searched for postcss.config.* and browserslist.
	Root string
	// Next's experimental.disablePostcssPresetEnv / useLightningcss.
	DisablePresetEnv bool `json:"disablePresetEnv"`
	UseLightningcss  bool `json:"useLightningcss"`
}

// StartPlugins evaluates configFile ("" for none) in a long-lived engine
// that serves the build: JS esbuild plugins and the HostOptions services.
// Call Close when the build is done.
func (h *Host) StartPlugins(configFile string, opts HostOptions) (*PluginHost, error) {
	cfgExpr := "{}"
	imp := ""
	if configFile != "" {
		imp = fmt.Sprintf("import * as cfg from %s;", jsonString(configFile))
		cfgExpr = `("default" in cfg ? cfg.default : cfg)`
	}
	services, err := json.Marshal(map[string]any{"postcss": opts.PostCSS, "sassOptions": opts.SassOptions, "lightningCssFeatures": opts.LightningCSSFeatures})
	if err != nil {
		return nil, err
	}
	nextConfig := "undefined"
	if opts.NextConfigFile != "" {
		// Resolved like LoadNext: a function config is called with the build
		// phase.
		// Loaded at run time through the host's require (TS/ESM aware), from
		// the directories Next and the config live in.
		nextConfig = fmt.Sprintf(`async () => {
  const createRequire = globalThis[Symbol.for("og.createRequire")];
  const { defaultConfig } = createRequire(services.postcss.NextDir + "/package.json")("next/dist/server/config-shared");
  const file = %s;
  const user = createRequire(file)(file);
  let c = user && user.__esModule && "default" in user ? user.default : user;
  if (c && typeof c === "object" && Object.keys(c).length === 1 && "default" in c) c = c.default;
  if (typeof c === "function") c = await c(%q, { defaultConfig });
  return (await c) ?? {};
}`, jsonString(opts.NextConfigFile), PhaseProductionBuild)
	}
	entry := fmt.Sprintf(`
%s
import { createBridge } from "og:plugin-bridge";
const services = %s;
services.nextConfig = %s;
globalThis.__ogBridge = createBridge(%s, services);
`, imp, services, nextConfig, cfgExpr)
	saved := h.Alias
	h.Alias = map[string]string{}
	for k, v := range saved {
		h.Alias[k] = v
	}
	code, err := h.bundle(entry, []api.Plugin{bridgeModules()})
	h.Alias = saved
	if err != nil {
		return nil, err
	}

	p := &PluginHost{builds: map[int64]api.PluginBuild{}, done: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	ready := make(chan error, 1)
	go func() {
		defer close(p.done)
		e := engine.New(engine.Options{Uncaught: func(err error) error {
			fmt.Fprintln(os.Stderr, "og: plugin error:", err)
			return nil
		}})
		p.e = e
		defer e.Close()
		err := e.Run(ctx, func(vm *sobek.Runtime) error {
			if err := h.prepare(e); err != nil {
				return err
			}
			p.reads = h.reads
			if err := p.installHost(vm); err != nil {
				return err
			}
			if _, err := vm.RunScript("og-plugins.js", code); err != nil {
				return err
			}
			p.bridge = vm.Get("__ogBridge").ToObject(vm)
			// Keep the loop alive until Close; calls arrive via Loop.Post.
			p.release = e.Loop.Async()
			ready <- nil
			return nil
		})
		if err != nil && err != engine.ErrInterrupted {
			select {
			case ready <- err:
			default:
			}
		}
	}()
	if err := <-ready; err != nil {
		cancel()
		if configFile == "" {
			return nil, fmt.Errorf("jshost: starting the build host: %w", err)
		}
		return nil, fmt.Errorf("jshost: loading %s: %w", filepath.Base(configFile), err)
	}
	return p, nil
}

// PostCSS runs Next.js's PostCSS pipeline over one stylesheet (from is its
// path, for plugins that resolve relative to it) and returns the result
// and any plugin warnings. It needs HostOptions.PostCSS.
func (p *PluginHost) PostCSS(from, css string) (string, []string, error) {
	v, err := p.call("postcss", from, css)
	if err != nil {
		return "", nil, fmt.Errorf("postcss %s: %w", from, err)
	}
	m, _ := v.(map[string]any)
	return str(m["css"]), strs(m["warnings"]), nil
}

// Lightningcss transforms one stylesheet as Next's lightningcss-loader does
// under experimental.useLightningcss (in place of PostCSS).
func (p *PluginHost) Lightningcss(from, css string) (string, error) {
	v, err := p.call("lightningcss", from, css)
	if err != nil {
		return "", fmt.Errorf("lightningcss %s: %w", from, err)
	}
	s, _ := v.(string)
	return s, nil
}

// WebpackHook is what og takes from next.config's webpack() hook for one
// target (see the bridge's webpackHook): aliases and fallbacks (name to
// candidate targets in order; "" is an empty module), DefinePlugin
// definitions, and how many module rules it added (their loaders run in
// the "webpack-<target>" plugins).
type WebpackHook struct {
	Aliases, Fallbacks map[string][]string
	Defines            map[string]string
	Rules              int
	Extensions         []string // file extensions the rules match
	// AsyncWebAssembly is experiments.asyncWebAssembly: .wasm imports are
	// ES modules of the instance's exports.
	AsyncWebAssembly bool
	// CSSExtensions are the extensions of next.config turbopack rules whose
	// loaders yield CSS (as: "*.css", e.g. "styl"): such files are
	// stylesheets, compiled by TurbopackCSS.
	CSSExtensions []string
	// Bundler is the bundler whose configuration applied: "webpack"
	// (webpack()) or "turbopack" (the turbopack section).
	Bundler string
}

// BabelConfig is a project's Babel configuration, which makes next build
// compile the project's files with Next's Babel loader instead of SWC.
type BabelConfig struct {
	ConfigFile string `json:"configFile"`
	PagesDir   string `json:"pagesDir"` // "" without one
	SrcDir     string `json:"srcDir"`   // the app or pages directory
}

// WebpackHook runs next.config's webpack(config, options) for target
// ("server" or "client"), with Next's Babel rule first when babel is set;
// nil when there is neither.
func (p *PluginHost) WebpackHook(target, dir, buildID string, babel *BabelConfig) (*WebpackHook, error) {
	var arg any
	if babel != nil {
		arg = map[string]any{"configFile": babel.ConfigFile, "pagesDir": babel.PagesDir, "srcDir": babel.SrcDir}
	}
	v, err := p.call("webpackHook", target, dir, buildID, arg)
	if err != nil {
		return nil, fmt.Errorf("next.config webpack() for the %s: %w", target, err)
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, nil
	}
	lists := func(v any) map[string][]string {
		out := map[string][]string{}
		mm, _ := v.(map[string]any)
		for k, l := range mm {
			out[k] = strs(l)
		}
		return out
	}
	h := &WebpackHook{Aliases: lists(m["aliases"]), Fallbacks: lists(m["fallbacks"]), Defines: map[string]string{}, Extensions: strs(m["extensions"])}
	if d, ok := m["defines"].(map[string]any); ok {
		for k, v := range d {
			h.Defines[k] = str(v)
		}
	}
	h.AsyncWebAssembly, _ = m["asyncWebAssembly"].(bool)
	h.CSSExtensions = strs(m["cssExtensions"])
	h.Bundler = str(m["bundler"])
	if n, ok := m["rules"].(int64); ok {
		h.Rules = int(n)
	} else if f, ok := m["rules"].(float64); ok {
		h.Rules = int(f)
	}
	return h, nil
}

// WebpackLoad runs path through the loaders of target's webpack() hook
// rules (WebpackHook), returning the module's JavaScript; ok is false when
// no rule applies.
func (p *PluginHost) WebpackLoad(target, path string) (contents string, ok bool, err error) {
	v, err := p.call("webpackLoad", target, path)
	if err != nil {
		return "", false, fmt.Errorf("%s: %w", path, err)
	}
	m, isMap := v.(map[string]any)
	if !isMap {
		return "", false, nil
	}
	return str(m["contents"]), true, nil
}

// TurbopackCSS runs path through target's turbopack rules that yield CSS
// (WebpackHook.CSSExtensions); ok is false when none applies.
func (p *PluginHost) TurbopackCSS(target, path string) (css string, ok bool, err error) {
	v, err := p.call("turbopackCSS", target, path)
	if err != nil {
		return "", false, fmt.Errorf("%s: %w", path, err)
	}
	s, isStr := v.(string)
	return s, isStr, nil
}

// Sass compiles a .scss/.sass file through Next's sass-loader and
// resolve-url-loader, returning CSS for the PostCSS step.
func (p *PluginHost) Sass(path string) (string, error) {
	v, err := p.call("sass", path)
	if err != nil {
		return "", fmt.Errorf("sass %s: %w", path, err)
	}
	s, _ := v.(string)
	return s, nil
}

// FontFile is a font file emitted by a next/font loader.
type FontFile struct {
	Name string // e.g. "static/media/<hash>.p.woff2"
	Data []byte
}

// FontResult is a next/font module: its CSS, the css-loader exports
// (name/value pairs, in order) and the emitted font files.
type FontResult struct {
	CSS     string
	Exports [][2]any
	Files   []FontFile
}

// NextFont runs Next's next/font pipeline (an @next/font loader, then
// postcss-next-font) for one target.css query.
func (p *PluginHost) NextFont(query map[string]any, assetPrefix string) (*FontResult, error) {
	v, err := p.call("nextFont", query, assetPrefix)
	if err != nil {
		return nil, err
	}
	m, _ := v.(map[string]any)
	r := &FontResult{CSS: str(m["css"])}
	exps, _ := m["exports"].([]any)
	for _, e := range exps {
		em, _ := e.(map[string]any)
		r.Exports = append(r.Exports, [2]any{str(em["name"]), em["value"]})
	}
	files, _ := m["files"].([]any)
	for _, f := range files {
		fm, _ := f.(map[string]any)
		data, err := base64.StdEncoding.DecodeString(str(fm["data"]))
		if err != nil {
			return nil, err
		}
		r.Files = append(r.Files, FontFile{Name: str(fm["name"]), Data: data})
	}
	return r, nil
}

// AssetReads returns the node_modules asset files (CSS, fonts) the build host
// read while running PostCSS, Sass and the next/font loaders. esbuild's
// metafile does not list them because a plugin inlined their bytes (Tailwind's
// preflight, a package's next/font/local files); the license collector
// attributes the packages they belong to.
func (p *PluginHost) AssetReads() []string {
	return p.reads.assets()
}

// Close stops the plugin host.
func (p *PluginHost) Close() {
	if p.release != nil {
		p.release(func(*sobek.Runtime) error { return nil })
	}
	p.cancel()
	<-p.done
}

// call invokes bridge[method](args...) on the host loop and waits for the
// (possibly asynchronous) result, returned as plain JSON data.
func (p *PluginHost) call(method string, args ...any) (any, error) {
	type result struct {
		v   any
		err error
	}
	ch := make(chan result, 1)
	p.e.Loop.Post(func(vm *sobek.Runtime) error {
		fn, ok := sobek.AssertFunction(p.bridge.Get(method))
		if !ok {
			ch <- result{err: fmt.Errorf("jshost: bridge has no %s", method)}
			return nil
		}
		vals := make([]sobek.Value, len(args))
		for i, a := range args {
			vals[i] = vm.ToValue(a)
		}
		v, err := fn(p.bridge, vals...)
		if err != nil {
			ch <- result{err: err}
			return nil
		}
		settle := func(v sobek.Value) {
			if v == nil || sobek.IsUndefined(v) || sobek.IsNull(v) {
				ch <- result{}
				return
			}
			s, err := vm.RunString("(v) => JSON.stringify(v)")
			if err != nil {
				ch <- result{err: err}
				return
			}
			stringify, _ := sobek.AssertFunction(s)
			js, err := stringify(sobek.Undefined(), v)
			if err != nil {
				ch <- result{err: err}
				return
			}
			var out any
			ch <- result{v: out, err: json.Unmarshal([]byte(js.String()), &out)}
		}
		if prom, ok := v.Export().(*sobek.Promise); ok {
			then, _ := sobek.AssertFunction(vm.ToValue(prom).ToObject(vm).Get("then"))
			_, err := then(vm.ToValue(prom),
				vm.ToValue(func(c sobek.FunctionCall) sobek.Value { settle(c.Argument(0)); return sobek.Undefined() }),
				vm.ToValue(func(c sobek.FunctionCall) sobek.Value {
					ch <- result{err: fmt.Errorf("%s", errorText(c.Argument(0)))}
					return sobek.Undefined()
				}))
			if err != nil {
				ch <- result{err: err}
			}
			return nil
		}
		settle(v)
		return nil
	})
	r := <-ch
	return r.v, r.err
}

func errorText(v sobek.Value) string {
	if o, ok := v.(*sobek.Object); ok {
		if st := o.Get("stack"); st != nil && !sobek.IsUndefined(st) {
			return st.String()
		}
	}
	if v == nil {
		return "undefined"
	}
	return v.String()
}

// installHost exposes Go services to the bridge and the esbuild shim.
func (p *PluginHost) installHost(vm *sobek.Runtime) error {
	hostObj := vm.NewObject()
	_ = hostObj.Set("resolve", func(buildID int64, path string, opts map[string]any) sobek.Value {
		promise, resolve, reject := vm.NewPromise()
		p.mu.Lock()
		pb, ok := p.builds[buildID]
		p.mu.Unlock()
		if !ok {
			_ = reject(vm.NewTypeError("build.resolve: unknown build"))
			return vm.ToValue(promise)
		}
		ro := api.ResolveOptions{
			Importer:   str(opts["importer"]),
			Namespace:  str(opts["namespace"]),
			ResolveDir: str(opts["resolveDir"]),
			Kind:       resolveKinds[str(opts["kind"])],
		}
		if t, ok := opts["pluginData"].(int64); ok && t != 0 {
			ro.PluginData = t
		}
		if ro.Kind == api.ResolveNone {
			ro.Kind = api.ResolveJSImportStatement
		}
		complete := p.e.Loop.Async()
		// Resolve off the loop: it may call back into JS plugins.
		go func() {
			r := pb.Resolve(path, ro)
			complete(func(vm *sobek.Runtime) error {
				out := map[string]any{
					"path": r.Path, "namespace": r.Namespace, "external": r.External,
					"sideEffects": r.SideEffects, "suffix": r.Suffix, "pluginData": tokenOf(r.PluginData),
					"errors": msgs(r.Errors), "warnings": msgs(r.Warnings),
				}
				return resolve(vm.ToValue(out))
			})
		}()
		return vm.ToValue(promise)
	})
	esb := vm.NewObject()
	_ = esb.Set("version", esbuildVersion())
	_ = esb.Set("transform", func(input string, opts map[string]any) map[string]any {
		res := api.Transform(input, transformOptions(opts))
		if len(res.Errors) > 0 {
			panic(vm.NewGoError(fmt.Errorf("transform failed: %s", res.Errors[0].Text)))
		}
		return map[string]any{"code": string(res.Code), "map": string(res.Map), "warnings": msgs(res.Warnings)}
	})
	install, err := vm.RunString(`(function (h, e) {
		Object.defineProperty(globalThis, Symbol.for("og.pluginHost"), { value: h });
		// Not og.esbuild: that is the full API (installEsbuild), which the
		// esbuild package's substitute calls (Panda CSS's buildSync).
		Object.defineProperty(globalThis, Symbol.for("og.pluginEsbuild"), { value: e });
	})`)
	if err != nil {
		return err
	}
	fn, _ := sobek.AssertFunction(install)
	_, err = fn(sobek.Undefined(), hostObj, esb)
	return err
}

// BridgeOptions adapt JS plugins to one og build.
type BridgeOptions struct {
	// Kinds selects config lists: "all" (esbuild.plugins), "client",
	// "server", "css".
	Kinds []string
	// InitialOptions is what plugins see as build.initialOptions.
	InitialOptions map[string]any
	// FileNamespaces are extra namespaces that registrations for the "file"
	// namespace also cover (Oganesson keeps server modules in layer namespaces).
	FileNamespaces []string
	// MapResolve may adjust a plugin's resolve result for the importer's
	// namespace (e.g. keep resolved modules inside their layer).
	MapResolve func(importerNamespace string, r *api.OnResolveResult)
	// SkipLoad lets Oganesson keep loads it must own (e.g. CSS imports).
	SkipLoad func(path string) bool
	// LoadChain, if set, receives onLoad registrations for the "file"
	// namespace instead of esbuild: Oganesson's own loaders run the chain first
	// and post-process its output ("use client"/"use server" handling).
	LoadChain *LoadChain
}

// LoadChain holds plugin onLoad callbacks that Oganesson runs from its own
// loaders, so plugin transforms and Oganesson's processing compose.
type LoadChain struct {
	mu      sync.Mutex
	entries []chainEntry
}

type chainEntry struct {
	filter *regexp.Regexp
	load   func(api.OnLoadArgs) (api.OnLoadResult, error)
}

// Load runs the first matching callback that returns contents.
func (c *LoadChain) Load(a api.OnLoadArgs) (api.OnLoadResult, bool, error) {
	if c == nil {
		return api.OnLoadResult{}, false, nil
	}
	c.mu.Lock()
	entries := append([]chainEntry(nil), c.entries...)
	c.mu.Unlock()
	for _, e := range entries {
		if !e.filter.MatchString(a.Path) {
			continue
		}
		r, err := e.load(a)
		if err != nil || r.Contents != nil || len(r.Errors) > 0 {
			return r, true, err
		}
	}
	return api.OnLoadResult{}, false, nil
}

// Plugins adapts the configured JS plugins for one build.
func (p *PluginHost) Plugins(opts BridgeOptions) ([]api.Plugin, error) {
	v, err := p.call("plugins", opts.Kinds)
	if err != nil {
		return nil, err
	}
	list, _ := v.([]any)
	var out []api.Plugin
	for _, item := range list {
		m := item.(map[string]any)
		kind, index, name := m["kind"].(string), int(m["index"].(float64)), m["name"].(string)
		out = append(out, api.Plugin{Name: name, Setup: func(pb api.PluginBuild) { p.setup(pb, kind, index, opts) }})
	}
	return out, nil
}

func (p *PluginHost) setup(pb api.PluginBuild, kind string, index int, opts BridgeOptions) {
	p.mu.Lock()
	p.nextID++
	buildID := p.nextID
	p.builds[buildID] = pb
	p.mu.Unlock()
	v, err := p.call("setup", kind, index, buildID, opts.InitialOptions)
	if err != nil {
		pb.OnStart(func() (api.OnStartResult, error) { return api.OnStartResult{}, err })
		return
	}
	regs, _ := v.([]any)
	for _, r := range regs {
		reg := r.(map[string]any)
		id := int64(reg["id"].(float64))
		filter, ns := str(reg["filter"]), str(reg["namespace"])
		namespaces := []string{ns}
		if ns == "file" {
			namespaces = append([]string{"file"}, opts.FileNamespaces...)
		}
		switch reg["type"] {
		case "resolve":
			for _, n := range namespaces {
				pb.OnResolve(api.OnResolveOptions{Filter: filter, Namespace: n}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
					return p.onResolve(id, a, opts)
				})
			}
		case "load":
			if ns == "file" && opts.LoadChain != nil {
				re, err := regexp.Compile(filter)
				if err != nil {
					pb.OnStart(func() (api.OnStartResult, error) { return api.OnStartResult{}, err })
					continue
				}
				opts.LoadChain.mu.Lock()
				opts.LoadChain.entries = append(opts.LoadChain.entries, chainEntry{filter: re, load: func(a api.OnLoadArgs) (api.OnLoadResult, error) {
					if opts.SkipLoad != nil && opts.SkipLoad(a.Path) {
						return api.OnLoadResult{}, nil
					}
					return p.onLoad(id, a)
				}})
				opts.LoadChain.mu.Unlock()
				continue
			}
			for _, n := range namespaces {
				pb.OnLoad(api.OnLoadOptions{Filter: filter, Namespace: n}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
					if opts.SkipLoad != nil && opts.SkipLoad(a.Path) {
						return api.OnLoadResult{}, nil
					}
					return p.onLoad(id, a)
				})
			}
		case "start":
			pb.OnStart(func() (api.OnStartResult, error) {
				r, err := p.call("call", id, map[string]any{})
				m, _ := r.(map[string]any)
				return api.OnStartResult{Errors: toMessages(m["errors"]), Warnings: toMessages(m["warnings"])}, err
			})
		case "end":
			pb.OnEnd(func(res *api.BuildResult) (api.OnEndResult, error) {
				r, err := p.call("call", id, map[string]any{"errors": msgs(res.Errors), "warnings": msgs(res.Warnings), "metafile": res.Metafile})
				m, _ := r.(map[string]any)
				return api.OnEndResult{Errors: toMessages(m["errors"]), Warnings: toMessages(m["warnings"])}, err
			})
		case "dispose":
			pb.OnDispose(func() { _, _ = p.call("call", id, map[string]any{}) })
		}
	}
}

func (p *PluginHost) onResolve(id int64, a api.OnResolveArgs, opts BridgeOptions) (api.OnResolveResult, error) {
	v, err := p.call("call", id, map[string]any{
		"path": a.Path, "importer": a.Importer, "namespace": a.Namespace, "resolveDir": a.ResolveDir,
		"kind": kindNames[a.Kind], "pluginData": tokenOf(a.PluginData), "with": a.With,
	})
	if err != nil || v == nil {
		return api.OnResolveResult{}, err
	}
	m := v.(map[string]any)
	r := api.OnResolveResult{
		Path: str(m["path"]), Namespace: str(m["namespace"]), Suffix: str(m["suffix"]),
		External: m["external"] == true, Errors: toMessages(m["errors"]), Warnings: toMessages(m["warnings"]),
		WatchFiles: strs(m["watchFiles"]), PluginData: fromToken(m["pluginData"]),
	}
	switch m["sideEffects"] {
	case false:
		r.SideEffects = api.SideEffectsFalse
	case true:
		r.SideEffects = api.SideEffectsTrue
	}
	if opts.MapResolve != nil && r.Path != "" {
		opts.MapResolve(a.Namespace, &r)
	}
	return r, nil
}

func (p *PluginHost) onLoad(id int64, a api.OnLoadArgs) (api.OnLoadResult, error) {
	v, err := p.call("call", id, map[string]any{
		"path": a.Path, "namespace": a.Namespace, "suffix": a.Suffix, "pluginData": tokenOf(a.PluginData), "with": a.With,
	})
	if err != nil || v == nil {
		return api.OnLoadResult{}, err
	}
	m := v.(map[string]any)
	r := api.OnLoadResult{
		ResolveDir: str(m["resolveDir"]), Errors: toMessages(m["errors"]), Warnings: toMessages(m["warnings"]),
		WatchFiles: strs(m["watchFiles"]), PluginData: fromToken(m["pluginData"]),
	}
	if c, ok := m["contents"].(string); ok {
		r.Contents = &c
	}
	if l, ok := loaderNames[str(m["loader"])]; ok {
		r.Loader = l
	}
	if r.ResolveDir == "" && r.Contents != nil {
		r.ResolveDir = filepath.Dir(a.Path)
	}
	return r, nil
}

// --- conversions -------------------------------------------------------------

var loaderNames = map[string]api.Loader{
	"js": api.LoaderJS, "jsx": api.LoaderJSX, "ts": api.LoaderTS, "tsx": api.LoaderTSX, "css": api.LoaderCSS,
	"local-css": api.LoaderLocalCSS, "global-css": api.LoaderGlobalCSS, "json": api.LoaderJSON, "text": api.LoaderText,
	"base64": api.LoaderBase64, "dataurl": api.LoaderDataURL, "file": api.LoaderFile, "binary": api.LoaderBinary,
	"copy": api.LoaderCopy, "empty": api.LoaderEmpty, "default": api.LoaderDefault,
}

var kindNames = map[api.ResolveKind]string{
	api.ResolveEntryPoint: "entry-point", api.ResolveJSImportStatement: "import-statement",
	api.ResolveJSRequireCall: "require-call", api.ResolveJSDynamicImport: "dynamic-import",
	api.ResolveJSRequireResolve: "require-resolve", api.ResolveCSSImportRule: "import-rule",
	api.ResolveCSSComposesFrom: "composes-from", api.ResolveCSSURLToken: "url-token",
}

var resolveKinds = func() map[string]api.ResolveKind {
	m := map[string]api.ResolveKind{}
	for k, v := range kindNames {
		m[v] = k
	}
	return m
}()

func transformOptions(o map[string]any) api.TransformOptions {
	t := api.TransformOptions{
		Loader:     loaderNames[str(o["loader"])],
		Sourcefile: str(o["sourcefile"]),
		Charset:    api.CharsetUTF8,
	}
	switch str(o["format"]) {
	case "esm":
		t.Format = api.FormatESModule
	case "cjs":
		t.Format = api.FormatCommonJS
	case "iife":
		t.Format = api.FormatIIFE
	}
	if o["minify"] == true {
		t.MinifyWhitespace, t.MinifySyntax, t.MinifyIdentifiers = true, true, true
	}
	if o["sourcemap"] == true || o["sourcemap"] == "external" {
		t.Sourcemap = api.SourceMapExternal
	} else if o["sourcemap"] == "inline" {
		t.Sourcemap = api.SourceMapInline
	}
	if d, ok := o["define"].(map[string]any); ok {
		t.Define = map[string]string{}
		for k, v := range d {
			t.Define[k] = str(v)
		}
	}
	if j := str(o["jsx"]); j == "automatic" {
		t.JSX = api.JSXAutomatic
	} else if j == "preserve" {
		t.JSX = api.JSXPreserve
	}
	if tc, ok := o["tsconfigRaw"]; ok {
		if s, ok := tc.(string); ok {
			t.TsconfigRaw = s
		} else if b, err := json.Marshal(tc); err == nil {
			t.TsconfigRaw = string(b)
		}
	}
	return t
}

func msgs(in []api.Message) []map[string]any {
	out := make([]map[string]any, len(in))
	for i, m := range in {
		out[i] = map[string]any{"text": m.Text, "pluginName": m.PluginName}
	}
	return out
}

func toMessages(v any) []api.Message {
	list, _ := v.([]any)
	out := make([]api.Message, 0, len(list))
	for _, item := range list {
		if m, ok := item.(map[string]any); ok {
			out = append(out, api.Message{Text: str(m["text"]), Detail: m["detail"]})
		}
	}
	return out
}

func tokenOf(v any) int64 {
	if t, ok := v.(int64); ok {
		return t
	}
	return 0
}

func fromToken(v any) any {
	if f, ok := v.(float64); ok && f != 0 {
		return int64(f)
	}
	return nil
}

func str(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func strs(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		out = append(out, str(x))
	}
	return out
}

// bridgeModules provides og:plugin-bridge and the esbuild package shim.
func bridgeModules() api.Plugin {
	return api.Plugin{
		Name: "og-plugin-bridge",
		Setup: func(b api.PluginBuild) {
			b.OnResolve(api.OnResolveOptions{Filter: `^(og:plugin-bridge|esbuild)$`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
				return api.OnResolveResult{Path: a.Path, Namespace: "og-bridge"}, nil
			})
			b.OnResolve(api.OnResolveOptions{Filter: `^\./webpack\.js$`, Namespace: "og-bridge"}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
				return api.OnResolveResult{Path: "webpack", Namespace: "og-bridge"}, nil
			})
			b.OnLoad(api.OnLoadOptions{Filter: ".*", Namespace: "og-bridge"}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
				src := map[string]string{
					"og:plugin-bridge": bridgeSource,
					"esbuild":          esbuildShimSource,
					"webpack":          webpackShimSource,
				}[a.Path]
				return api.OnLoadResult{Contents: &src, Loader: api.LoaderJS}, nil
			})
		},
	}
}

// esbuildVersion is the embedded esbuild's module version (e.g. "0.28.2").
func esbuildVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			if d.Path == "github.com/evanw/esbuild" {
				return strings.TrimPrefix(d.Version, "v")
			}
		}
	}
	return "0.0.0"
}
