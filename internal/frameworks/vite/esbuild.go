package vite

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
	"github.com/grafana/sobek"
	"github.com/microsoft/TypeScript/tsc/ogapi"
	"github.com/moriyoshi/oganesson/internal/licenses"
	"github.com/moriyoshi/oganesson/runtime/engine"
)

// The Go side of the rolldown() facade (js/rolldown.js): og.rolldown.build
// runs one esbuild build whose resolution and loading go through the JS
// plugin container, and og.rolldown.resolve is esbuild's own resolution
// for the builtin resolver (builtin:vite-resolve) and this.resolve.
//
// Rollup module ids map onto esbuild paths: an absolute file path without
// a query is in the "file" namespace; any other id (virtual "\0…" ids,
// ids with a query) is in the vite namespace, its path the id verbatim.

const (
	viteNamespace = "og-vite"
	// defaultResolve marks a resolution the JS side already ran its
	// hooks for: esbuild's own resolver answers it.
	defaultResolve = "og-vite-default"
)

// buildSpec is the esbuild configuration rolldown.js derives from Rolldown
// input and output options.
type buildSpec struct {
	Entries []struct {
		In  string `json:"in"`
		Out string `json:"out"`
	} `json:"entries"`
	AbsWorkingDir string            `json:"absWorkingDir"`
	Outdir        string            `json:"outdir"`
	Format        string            `json:"format"`
	Platform      string            `json:"platform"`
	Target        []string          `json:"target"`
	Define        map[string]string `json:"define"`
	JSX           struct {
		Runtime      string `json:"runtime"`
		ImportSource string `json:"importSource"`
		Development  bool   `json:"development"`
	} `json:"jsx"`
	Minify        bool     `json:"minify"`
	Sourcemap     bool     `json:"sourcemap"`
	EntryNames    string   `json:"entryNames"`
	ChunkNames    string   `json:"chunkNames"`
	Splitting     bool     `json:"splitting"`
	Conditions    []string `json:"conditions"`
	MainFields    []string `json:"mainFields"`
	Extensions    []string `json:"extensions"`
	LegalComments string   `json:"legalComments"`
}

// rolldownHost holds the builds in flight, for og.rolldown.resolve.
type rolldownHost struct {
	e      *engine.Engine
	mu     sync.Mutex
	builds map[int64]api.PluginBuild
	nextID int64
	// tsconfigs caches tsconfigFor by directory.
	tsconfigs map[string]string
	// resolvers are the standalone resolvers, by configuration.
	resolvers map[string]api.PluginBuild
	// licenses records the npm packages each build reads.
	licenses *licenses.Collector
}

// tsconfigFor is esbuild's TsconfigRaw for file: the settings of the
// nearest tsconfig.json that change how TypeScript compiles (decorators,
// class field semantics, import elision), resolved by TypeScript-Go as tsc
// resolves them ("extends" chains, target-dependent defaults). "" if none.
func (h *rolldownHost) tsconfigFor(file string) string {
	if file == "" || !filepath.IsAbs(file) {
		return ""
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.tsconfigs == nil {
		h.tsconfigs = map[string]string{}
	}
	var visited []string
	raw := ""
	for dir := filepath.Dir(file); ; dir = filepath.Dir(dir) {
		if v, ok := h.tsconfigs[dir]; ok {
			raw = v
			break
		}
		visited = append(visited, dir)
		if p := filepath.Join(dir, "tsconfig.json"); fileExists(p) {
			if cfg, _ := ogapi.LoadConfig(p, dir); cfg != nil {
				b, _ := json.Marshal(map[string]any{"compilerOptions": map[string]any{
					"experimentalDecorators":  cfg.ExperimentalDecorators,
					"useDefineForClassFields": cfg.UseDefineForClassFields,
					"verbatimModuleSyntax":    cfg.VerbatimModuleSyntax,
				}})
				raw = string(b)
			}
			break
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	for _, d := range visited {
		h.tsconfigs[d] = raw
	}
	return raw
}

// standaloneResolver is an esbuild resolver outside any build, configured
// by optsJSON ({platform, conditions, mainFields, extensions}); one per
// configuration, disposed with the host engine.
func (h *rolldownHost) standaloneResolver(optsJSON string) (api.PluginBuild, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if pb, ok := h.resolvers[optsJSON]; ok {
		return pb, nil
	}
	var o struct {
		Platform   string   `json:"platform"`
		Conditions []string `json:"conditions"`
		MainFields []string `json:"mainFields"`
		Extensions []string `json:"extensions"`
	}
	if optsJSON != "" {
		if err := json.Unmarshal([]byte(optsJSON), &o); err != nil {
			return api.PluginBuild{}, err
		}
	}
	var pb api.PluginBuild
	ctx, cerr := api.Context(api.BuildOptions{
		Bundle:            true,
		Write:             false,
		LogLevel:          api.LogLevelSilent,
		Platform:          platforms[o.Platform],
		Conditions:        o.Conditions,
		MainFields:        o.MainFields,
		ResolveExtensions: o.Extensions,
		Plugins:           []api.Plugin{{Name: "og-resolver", Setup: func(b api.PluginBuild) { pb = b }}},
	})
	if cerr != nil {
		return api.PluginBuild{}, fmt.Errorf("og: standalone resolver: %s", formatMessages(cerr.Errors))
	}
	if h.resolvers == nil {
		h.resolvers = map[string]api.PluginBuild{}
	}
	h.resolvers[optsJSON] = pb
	h.e.OnClose(ctx.Dispose)
	return pb, nil
}

// installRolldown installs og.rolldown into the host engine.
func installRolldown(e *engine.Engine, col *licenses.Collector) error {
	h := &rolldownHost{e: e, builds: map[int64]api.PluginBuild{}, licenses: col}
	vm := e.VM
	o := vm.NewObject()
	_ = o.Set("build", func(specJSON string, hooks *sobek.Object) sobek.Value {
		promise, resolve, reject := vm.NewPromise()
		var spec buildSpec
		if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
			_ = reject(vm.NewGoError(err))
			return vm.ToValue(promise)
		}
		resolveHook, _ := sobek.AssertFunction(hooks.Get("resolve"))
		loadHook, _ := sobek.AssertFunction(hooks.Get("load"))
		h.mu.Lock()
		h.nextID++
		id := h.nextID
		h.mu.Unlock()
		complete := e.Loop.Async()
		go func() {
			res := h.run(id, spec, resolveHook, loadHook)
			if len(res.Errors) == 0 {
				if err := h.licenses.AddMetafile(res.Metafile, spec.AbsWorkingDir); err != nil {
					res.Errors = append(res.Errors, api.Message{Text: err.Error()})
				}
			}
			h.mu.Lock()
			delete(h.builds, id)
			h.mu.Unlock()
			complete(func(vm *sobek.Runtime) error {
				if len(res.Errors) > 0 {
					return reject(vm.NewGoError(fmt.Errorf("%s", formatMessages(res.Errors))))
				}
				outputs := make([]map[string]any, 0, len(res.OutputFiles))
				for _, f := range res.OutputFiles {
					outputs = append(outputs, map[string]any{"path": f.Path, "text": string(f.Contents)})
				}
				return resolve(vm.ToValue(map[string]any{
					"buildId": id, "outputs": outputs, "metafile": res.Metafile, "warnings": formatMessages(res.Warnings),
				}))
			})
		}()
		return vm.ToValue(promise)
	})
	// resolve(buildId, source, resolveDir, kind, optsJSON): esbuild's
	// resolver, inside the build buildId, or (outside one: plugins resolving
	// in buildStart, Astro's) a standalone resolver with optsJSON's
	// platform, conditions, main fields and extensions.
	_ = o.Set("resolve", func(buildID int64, source, resolveDir, kind, optsJSON string) sobek.Value {
		promise, resolve, reject := vm.NewPromise()
		h.mu.Lock()
		pb, ok := h.builds[buildID]
		h.mu.Unlock()
		if !ok {
			var err error
			if pb, err = h.standaloneResolver(optsJSON); err != nil {
				_ = reject(vm.NewGoError(err))
				return vm.ToValue(promise)
			}
		}
		k := resolveKinds[kind]
		if k == api.ResolveNone {
			k = api.ResolveJSImportStatement
		}
		complete := e.Loop.Async()
		go func() {
			r := pb.Resolve(source, api.ResolveOptions{ResolveDir: resolveDir, Kind: k, PluginData: defaultResolve})
			complete(func(vm *sobek.Runtime) error {
				if len(r.Errors) > 0 {
					return resolve(sobek.Null())
				}
				return resolve(vm.ToValue(map[string]any{"path": r.Path, "external": r.External, "sideEffects": r.SideEffects}))
			})
		}()
		return vm.ToValue(promise)
	})
	// dynamicImports(id, code): the import(…) calls of a module, as UTF-16
	// offsets [start, end, argStart, argEnd] (the build import analysis).
	_ = o.Set("dynamicImports", func(id, code string) [][4]int {
		ext := filepath.Ext(strings.SplitN(strings.TrimPrefix(id, "\x00"), "?", 2)[0])
		var out [][4]int
		u := utf16Offsets(code)
		for _, d := range ogapi.DynamicImports("/og-module"+ext, code) {
			out = append(out, [4]int{u(d.Start), u(d.End), u(d.ArgStart), u(d.ArgEnd)})
		}
		return out
	})
	// transform(code, options): Rolldown's TS/JSX transform
	// (builtin:vite-transform) as esbuild's; ESM stays ESM.
	// transform(code, optsJSON): TS/JSX to JS with esbuild (Rolldown's Oxc
	// transform, and rolldown/utils' transform and minify). The nearest
	// tsconfig.json gives the TypeScript settings (decorators, class
	// fields, JSX). With withMap, the result is { code, map }.
	_ = o.Set("transform", func(code string, optsJSON string) sobek.Value {
		var o struct {
			Loader       string            `json:"loader"`
			Sourcefile   string            `json:"sourcefile"`
			JSXRuntime   string            `json:"jsxRuntime"` // automatic, classic, preserve
			ImportSource string            `json:"importSource"`
			Development  bool              `json:"development"`
			Define       map[string]string `json:"define"`
			Target       []string          `json:"target"`
			Minify       bool              `json:"minify"`
			WithMap      bool              `json:"withMap"`
		}
		if err := json.Unmarshal([]byte(optsJSON), &o); err != nil {
			panic(vm.NewGoError(err))
		}
		to := api.TransformOptions{Loader: loaders[o.Loader], Sourcefile: o.Sourcefile, Target: api.ESNext, Charset: api.CharsetUTF8, JSXDev: o.Development, Define: o.Define}
		if to.Loader == api.LoaderNone {
			to.Loader = api.LoaderJS
		}
		switch o.JSXRuntime {
		case "classic":
			to.JSX = api.JSXTransform
		case "preserve":
			to.JSX = api.JSXPreserve
		default:
			to.JSX = api.JSXAutomatic
			to.JSXImportSource = o.ImportSource
		}
		if len(o.Target) > 0 {
			to.Engines, to.Target = engines(o.Target)
		}
		if o.Minify {
			to.MinifyWhitespace, to.MinifyIdentifiers, to.MinifySyntax = true, true, true
		}
		if o.WithMap {
			to.Sourcemap = api.SourceMapExternal
		}
		if to.Loader == api.LoaderTS || to.Loader == api.LoaderTSX {
			to.TsconfigRaw = h.tsconfigFor(o.Sourcefile)
		}
		res := api.Transform(code, to)
		if len(res.Errors) > 0 {
			panic(vm.NewGoError(fmt.Errorf("%s", formatMessages(res.Errors))))
		}
		if o.WithMap {
			return vm.ToValue(map[string]any{"code": string(res.Code), "map": string(res.Map)})
		}
		return vm.ToValue(string(res.Code))
	})
	// preview(root, clientDir, serverEntry, env): the built app served on a
	// local port (preview.go) -> { id, url }; closePreview(id).
	_ = o.Set("preview", func(root, clientDir, serverEntry string, env map[string]string) sobek.Value {
		promise, resolve, reject := vm.NewPromise()
		complete := e.Loop.Async()
		go func() {
			id, url, err := startPreview(root, clientDir, serverEntry, env)
			complete(func(vm *sobek.Runtime) error {
				if err != nil {
					return reject(vm.NewGoError(err))
				}
				return resolve(vm.ToValue(map[string]any{"id": id, "url": url}))
			})
		}()
		return vm.ToValue(promise)
	})
	// programPreview(root): the project served through its server program
	// (preview.go) -> { id, url }.
	_ = o.Set("programPreview", func(root string) sobek.Value {
		promise, resolve, reject := vm.NewPromise()
		complete := e.Loop.Async()
		go func() {
			id, url, err := startProgramPreview(root)
			complete(func(vm *sobek.Runtime) error {
				if err != nil {
					return reject(vm.NewGoError(err))
				}
				return resolve(vm.ToValue(map[string]any{"id": id, "url": url}))
			})
		}()
		return vm.ToValue(promise)
	})
	_ = o.Set("closePreview", func(id int) { stopPreview(id) })
	install, err := vm.RunString(`(o) => Object.defineProperty(globalThis, Symbol.for("og.rolldown"), { value: o })`)
	if err != nil {
		return err
	}
	fn, _ := sobek.AssertFunction(install)
	_, err = fn(sobek.Undefined(), o)
	return err
}

// globalDefines moves object- and array-valued defines (Vite's
// process.env = {}, Astro's import.meta.env) off esbuild's define: esbuild
// shares such a value through a symbol all files reference, which makes
// its renamer race in split, unminified builds (spikes/esbuild-define-race;
// an injected module's export races the same way). Server builds must be
// split (modules shared by entries, such as Svelte's component context,
// are one instance). Each becomes globalThis.__og_define_<n>, an entity
// name with no symbol behind it, and the banner sets it once: one object
// for the whole output, as esbuild's shared value is. trimDefineBanners
// then keeps, per chunk, only the statements for the names it uses.
func globalDefines(define map[string]string) (map[string]string, []defineStmt) {
	out := map[string]string{}
	var stmts []defineStmt
	keys := make([]string, 0, len(define))
	for k := range define {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for i, k := range keys {
		v := define[k]
		if t := strings.TrimSpace(v); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
			name := fmt.Sprintf("globalThis.__og_define_%d", i)
			// (|| rather than ??=: the banner is not lowered to the target)
			stmts = append(stmts, defineStmt{name: name, js: fmt.Sprintf("%s = %s || %s;", name, name, v)})
			out[k] = name
			continue
		}
		out[k] = v
	}
	return out, stmts
}

// defineStmt is one banner statement and the name it sets.
type defineStmt struct{ name, js string }

func joinStmts(stmts []defineStmt) string {
	var b strings.Builder
	for _, st := range stmts {
		b.WriteString(st.js)
	}
	return b.String()
}

// trimDefineBanners rewrites each JavaScript output's banner line to the
// statements for the define names the chunk references, and removes the
// line (and its line in the chunk's source map) when it references none:
// Astro inlines small page scripts, which would otherwise carry every
// define.
func trimDefineBanners(res *api.BuildResult, stmts []defineStmt) {
	if len(stmts) == 0 {
		return
	}
	banner := joinStmts(stmts) + "\n"
	maps := map[string]int{}
	for i, f := range res.OutputFiles {
		if strings.HasSuffix(f.Path, ".map") {
			maps[f.Path] = i
		}
	}
	for i, f := range res.OutputFiles {
		if !strings.HasSuffix(f.Path, ".js") && !strings.HasSuffix(f.Path, ".mjs") && !strings.HasSuffix(f.Path, ".cjs") {
			continue
		}
		code := string(f.Contents)
		at := strings.Index(code, banner)
		if at < 0 || (at > 0 && code[at-1] != '\n') {
			continue
		}
		body := code[:at] + code[at+len(banner):]
		var keep []defineStmt
		for _, st := range stmts {
			if regexp.MustCompile(regexp.QuoteMeta(st.name) + `\b`).MatchString(body) {
				keep = append(keep, st)
			}
		}
		if len(keep) > 0 {
			res.OutputFiles[i].Contents = []byte(code[:at] + joinStmts(keep) + "\n" + code[at+len(banner):])
			continue
		}
		res.OutputFiles[i].Contents = []byte(body)
		if m, ok := maps[f.Path+".map"]; ok {
			res.OutputFiles[m].Contents = dropMapLine(res.OutputFiles[m].Contents, strings.Count(code[:at], "\n"))
		}
	}
}

// dropMapLine removes generated line n (which has no mappings) from a
// source map's mappings.
func dropMapLine(m []byte, n int) []byte {
	var sm map[string]any
	if json.Unmarshal(m, &sm) != nil {
		return m
	}
	mappings, _ := sm["mappings"].(string)
	lines := strings.Split(mappings, ";")
	if n >= len(lines) || lines[n] != "" {
		return m
	}
	sm["mappings"] = strings.Join(append(lines[:n], lines[n+1:]...), ";")
	out, err := json.Marshal(sm)
	if err != nil {
		return m
	}
	return out
}

// packageSideEffects reports whether the module at path may have side
// effects by its package's "sideEffects" field (true when the field is
// absent, the path is not a file, or no package.json is found): false
// declares every file free of them, an array of patterns lists those that
// have them (a pattern without "/" matches a file name anywhere, as in
// webpack and Rolldown).
func packageSideEffects(id string) bool {
	if id == "" || !filepath.IsAbs(id) || strings.ContainsAny(id, "\x00?") {
		return true
	}
	for dir := filepath.Dir(id); ; dir = filepath.Dir(dir) {
		if se, ok := packageSideEffectsField(dir); ok {
			switch v := se.(type) {
			case bool:
				return v
			case []any:
				rel, err := filepath.Rel(dir, id)
				if err != nil {
					return true
				}
				rel = filepath.ToSlash(rel)
				for _, p := range v {
					pat, _ := p.(string)
					pat = strings.TrimPrefix(pat, "./")
					if !strings.Contains(pat, "/") {
						pat = "**/" + pat
					}
					if globMatch(pat, rel) {
						return true
					}
				}
				return false
			}
			return true
		}
		if parent := filepath.Dir(dir); parent == dir || filepath.Base(dir) == "node_modules" {
			return true
		}
	}
}

var sideEffectsCache sync.Map // dir -> sideEffectsEntry

type sideEffectsEntry struct {
	value any
	found bool // a package.json with a name is in dir
}

// packageSideEffectsField is the "sideEffects" value of dir's
// package.json (ok when dir has a package.json naming a package, which
// ends the search, as Rolldown's resolver finds a module's package).
func packageSideEffectsField(dir string) (any, bool) {
	if v, ok := sideEffectsCache.Load(dir); ok {
		e := v.(sideEffectsEntry)
		return e.value, e.found
	}
	var e sideEffectsEntry
	if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		var pj struct {
			Name        string `json:"name"`
			SideEffects any    `json:"sideEffects"`
		}
		if json.Unmarshal(b, &pj) == nil && pj.Name != "" {
			e = sideEffectsEntry{value: pj.SideEffects, found: true}
		}
	}
	sideEffectsCache.Store(dir, e)
	return e.value, e.found
}

// globMatch matches a slash-separated path against a pattern with *, ?
// and ** (any number of directories).
func globMatch(pat, name string) bool {
	var re strings.Builder
	re.WriteString("^")
	for i := 0; i < len(pat); i++ {
		switch c := pat[i]; {
		case c == '*' && i+1 < len(pat) && pat[i+1] == '*':
			i++
			if i+1 < len(pat) && pat[i+1] == '/' {
				i++
				re.WriteString("(?:.*/)?")
			} else {
				re.WriteString(".*")
			}
		case c == '*':
			re.WriteString("[^/]*")
		case c == '?':
			re.WriteString("[^/]")
		default:
			re.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	re.WriteString("$")
	ok, _ := regexp.MatchString(re.String(), name)
	return ok
}

func bannerFor(js string) map[string]string {
	if js == "" {
		return nil
	}
	return map[string]string{"js": js}
}

func (h *rolldownHost) run(id int64, spec buildSpec, resolveHook, loadHook sobek.Callable) api.BuildResult {
	define, stmts := globalDefines(spec.Define)
	plugin := api.Plugin{
		Name: "og-rolldown",
		Setup: func(pb api.PluginBuild) {
			h.mu.Lock()
			h.builds[id] = pb
			h.mu.Unlock()
			pb.OnResolve(api.OnResolveOptions{Filter: `.*`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
				if a.PluginData == defaultResolve {
					return api.OnResolveResult{}, nil
				}
				importer := a.Importer
				v, err := h.call(resolveHook, map[string]any{
					"source": a.Path, "importer": importer, "kind": kindNames[a.Kind], "isEntry": a.Kind == api.ResolveEntryPoint,
					"resolveDir": a.ResolveDir, "buildId": id,
				})
				if err != nil {
					return api.OnResolveResult{}, err
				}
				m, ok := v.(map[string]any)
				if !ok {
					return api.OnResolveResult{}, fmt.Errorf("could not resolve %q from %q", a.Path, importer)
				}
				rid, _ := m["id"].(string)
				if m["external"] == true {
					return api.OnResolveResult{Path: rid, External: true}, nil
				}
				r := api.OnResolveResult{Path: rid, Namespace: namespaceOf(rid)}
				switch m["moduleSideEffects"] {
				case false:
					r.SideEffects = api.SideEffectsFalse
				case nil:
					// Rolldown's resolver default: the nearest package.json's
					// "sideEffects", whichever plugin resolved the id (esbuild
					// does not read it for plugin results, and kept every
					// module of a side-effect-free package live for each entry
					// that imports its index: one shared chunk where Rolldown
					// makes several).
					if !packageSideEffects(rid) {
						r.SideEffects = api.SideEffectsFalse
					}
				}
				return r, nil
			})
			load := func(a api.OnLoadArgs) (api.OnLoadResult, error) {
				v, err := h.call(loadHook, map[string]any{"id": a.Path, "buildId": id})
				if err != nil {
					return api.OnLoadResult{}, err
				}
				m, _ := v.(map[string]any)
				code, _ := m["code"].(string)
				loader, ok := loaders[str(m["loader"])]
				if !ok {
					loader = api.LoaderJS
				}
				dir := spec.AbsWorkingDir
				if p := strings.SplitN(strings.TrimPrefix(a.Path, "\x00"), "?", 2)[0]; filepath.IsAbs(p) {
					dir = filepath.Dir(p)
				}
				return api.OnLoadResult{Contents: &code, Loader: loader, ResolveDir: dir}, nil
			}
			pb.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: "file"}, load)
			pb.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: viteNamespace}, load)
		},
	}
	var entries []api.EntryPoint
	for _, e := range spec.Entries {
		entries = append(entries, api.EntryPoint{InputPath: e.In, OutputPath: e.Out})
	}
	opts := api.BuildOptions{
		EntryPointsAdvanced: entries,
		AbsWorkingDir:       spec.AbsWorkingDir,
		Outdir:              spec.Outdir,
		Bundle:              true,
		Write:               false,
		Metafile:            true,
		Splitting:           spec.Splitting,
		Format:              formats[spec.Format],
		Platform:            platforms[spec.Platform],
		Define:              define,
		Banner:              bannerFor(joinStmts(stmts)),
		MinifyWhitespace:    spec.Minify,
		MinifySyntax:        spec.Minify,
		MinifyIdentifiers:   spec.Minify,
		EntryNames:          spec.EntryNames,
		ChunkNames:          spec.ChunkNames,
		Conditions:          spec.Conditions,
		MainFields:          spec.MainFields,
		ResolveExtensions:   spec.Extensions,
		Charset:             api.CharsetUTF8,
		LogLevel:            api.LogLevelSilent,
		Plugins:             []api.Plugin{plugin},
	}
	if spec.Sourcemap {
		opts.Sourcemap = api.SourceMapExternal
	}
	switch spec.LegalComments {
	case "none":
		opts.LegalComments = api.LegalCommentsNone
	case "inline":
		opts.LegalComments = api.LegalCommentsInline
	}
	if len(spec.Target) > 0 {
		opts.Engines, opts.Target = engines(spec.Target)
	}
	switch spec.JSX.Runtime {
	case "classic":
		opts.JSX = api.JSXTransform
	default:
		opts.JSX = api.JSXAutomatic
		opts.JSXImportSource = spec.JSX.ImportSource
		opts.JSXDev = spec.JSX.Development
	}
	res := api.Build(opts)
	trimDefineBanners(&res, stmts)
	return res
}

// call runs a JS hook on the engine's loop from an esbuild goroutine and
// waits for its (possibly asynchronous) result, as plain data.
func (h *rolldownHost) call(fn sobek.Callable, arg any) (any, error) {
	type result struct {
		v   any
		err error
	}
	ch := make(chan result, 1)
	h.e.Loop.Post(func(vm *sobek.Runtime) error {
		v, err := fn(sobek.Undefined(), vm.ToValue(arg))
		if err != nil {
			ch <- result{err: err}
			return nil
		}
		settle := func(v sobek.Value) {
			if v == nil || sobek.IsUndefined(v) || sobek.IsNull(v) {
				ch <- result{}
				return
			}
			var out any
			ch <- result{v: out, err: vm.ExportTo(v, &out)}
		}
		if p, ok := v.Export().(*sobek.Promise); ok {
			then, _ := sobek.AssertFunction(vm.ToValue(p).ToObject(vm).Get("then"))
			_, err := then(vm.ToValue(p),
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
	return v.String()
}

// utf16Offsets maps byte offsets of s to UTF-16 code unit offsets (the
// string indices of JavaScript).
func utf16Offsets(s string) func(int) int {
	ascii := true
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			ascii = false
			break
		}
	}
	if ascii {
		return func(b int) int { return b }
	}
	return func(b int) int {
		n := 0
		for _, r := range s[:b] {
			if r >= 0x10000 {
				n += 2
			} else {
				n++
			}
		}
		return n
	}
}

func namespaceOf(id string) string {
	if filepath.IsAbs(id) && !strings.ContainsAny(id, "?#\x00") {
		return "file"
	}
	return viteNamespace
}

func formatMessages(msgs []api.Message) string {
	var sb strings.Builder
	for _, m := range msgs {
		if m.Location != nil {
			fmt.Fprintf(&sb, "%s:%d:%d: ", m.Location.File, m.Location.Line, m.Location.Column)
		}
		sb.WriteString(m.Text + "\n")
	}
	return sb.String()
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

var loaders = map[string]api.Loader{
	"js": api.LoaderJS, "jsx": api.LoaderJSX, "ts": api.LoaderTS, "tsx": api.LoaderTSX, "json": api.LoaderJSON,
	"text": api.LoaderText, "base64": api.LoaderBase64, "dataurl": api.LoaderDataURL, "binary": api.LoaderBinary,
	"empty": api.LoaderEmpty, "css": api.LoaderCSS,
}

var formats = map[string]api.Format{"es": api.FormatESModule, "esm": api.FormatESModule, "module": api.FormatESModule, "cjs": api.FormatCommonJS, "commonjs": api.FormatCommonJS, "iife": api.FormatIIFE}

var platforms = map[string]api.Platform{"browser": api.PlatformBrowser, "node": api.PlatformNode, "neutral": api.PlatformNeutral}

var kindNames = map[api.ResolveKind]string{
	api.ResolveEntryPoint: "entry-point", api.ResolveJSImportStatement: "import-statement",
	api.ResolveJSRequireCall: "require-call", api.ResolveJSDynamicImport: "dynamic-import",
	api.ResolveJSRequireResolve: "require-resolve", api.ResolveCSSImportRule: "import-rule",
	api.ResolveCSSURLToken: "url-token",
}

var resolveKinds = func() map[string]api.ResolveKind {
	m := map[string]api.ResolveKind{}
	for k, v := range kindNames {
		m[v] = k
	}
	return m
}()

// engines maps Rolldown/browserslist-style targets ("chrome111",
// "safari16.4", "es2020") onto esbuild's engines and language target.
func engines(targets []string) ([]api.Engine, api.Target) {
	names := map[string]api.EngineName{
		"chrome": api.EngineChrome, "edge": api.EngineEdge, "firefox": api.EngineFirefox, "safari": api.EngineSafari,
		"ios": api.EngineIOS, "node": api.EngineNode, "deno": api.EngineDeno, "opera": api.EngineOpera,
	}
	esTargets := map[string]api.Target{
		"es2015": api.ES2015, "es2016": api.ES2016, "es2017": api.ES2017, "es2018": api.ES2018, "es2019": api.ES2019,
		"es2020": api.ES2020, "es2021": api.ES2021, "es2022": api.ES2022, "es2023": api.ES2023, "es2024": api.ES2024, "esnext": api.ESNext,
	}
	var out []api.Engine
	target := api.ESNext
	for _, t := range targets {
		t = strings.ToLower(t)
		if es, ok := esTargets[t]; ok {
			target = es
			continue
		}
		for prefix, name := range names {
			if v, ok := strings.CutPrefix(t, prefix); ok && v != "" && (v[0] >= '0' && v[0] <= '9') {
				out = append(out, api.Engine{Name: name, Version: v})
				break
			}
		}
	}
	return out, target
}
