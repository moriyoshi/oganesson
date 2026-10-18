package jshost

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/grafana/sobek"
	"github.com/moriyoshi/oganesson/internal/gojatarget"
	"github.com/moriyoshi/oganesson/runtime/nodeshim"
	"github.com/moriyoshi/oganesson/third_party/goja_nodejs/require"
)

// Dynamic require() for build-time JavaScript. Bundling handles static
// imports, but build tooling also requires modules dynamically: config
// files, package.json reads, Babel/PostCSS plugins named by string. The
// loader itself is goja_nodejs/require; this file supplies the parts it
// lacks (package "exports", more extensions, ESM/TS sources, built-ins).

// requireConditions are the export conditions of a Node require().
var requireConditions = []string{"node", "require", "default"}

// importConditions are those of an import from an ES module (Node's ESM
// loader; module-sync aside, as the runtime reports Node 22.0).
var importConditions = []string{"node", "import", "default"}

var requireExts = []string{".js", ".cjs", ".mjs", ".json", ".ts", ".mts", ".cts", ".tsx", ".jsx"}

// resolveModule resolves request from fromDir; built-ins resolve to "node:<name>".
func resolveModule(request, fromDir string) (string, error) {
	return resolveModuleWith(request, fromDir, requireConditions)
}

// ResolveNode resolves a bare specifier from fromDir as Node does: package
// "exports" with the node and import (esm) or require conditions, else
// "main". Built-ins resolve to "node:<name>".
func ResolveNode(request, fromDir string, esm bool) (string, error) {
	if esm {
		return resolveModuleWith(request, fromDir, importConditions)
	}
	return resolveModuleWith(request, fromDir, requireConditions)
}

// resolveModuleWith is resolveModule with the given export conditions.
func resolveModuleWith(request, fromDir string, conds []string) (string, error) {
	name := strings.TrimPrefix(request, "node:")
	if nodeshim.Builtins[name] {
		return "node:" + name, nil
	}
	if strings.HasPrefix(request, ".") || filepath.IsAbs(request) {
		p := request
		if !filepath.IsAbs(p) {
			p = filepath.Join(fromDir, p)
		}
		if r, ok := resolveFileOrDir(p); ok {
			return r, nil
		}
		return "", fmt.Errorf("Cannot find module '%s' from '%s'", request, fromDir)
	}
	if strings.HasPrefix(request, "#") {
		if r, ok := resolveImportsWith(request, fromDir, conds); ok {
			return r, nil
		}
		return "", fmt.Errorf("Cannot find module '%s' from '%s'", request, fromDir)
	}
	pkg, sub := splitBare(request)
	for dir := fromDir; ; dir = filepath.Dir(dir) {
		pkgDir := filepath.Join(dir, "node_modules", pkg)
		if st, err := os.Stat(pkgDir); err == nil && st.IsDir() {
			if r, ok := resolvePackageWith(pkgDir, sub, conds); ok {
				return r, nil
			}
		}
		if filepath.Dir(dir) == dir {
			return "", fmt.Errorf("Cannot find module '%s' from '%s'", request, fromDir)
		}
	}
}

func splitBare(request string) (pkg, sub string) {
	parts := strings.SplitN(request, "/", 3)
	if strings.HasPrefix(request, "@") && len(parts) >= 2 {
		pkg = parts[0] + "/" + parts[1]
		if len(parts) == 3 {
			sub = parts[2]
		}
		return pkg, sub
	}
	pkg = parts[0]
	if i := strings.IndexByte(request, '/'); i >= 0 {
		sub = request[i+1:]
	}
	return pkg, sub
}

func resolvePackage(pkgDir, sub string) (string, bool) {
	return resolvePackageWith(pkgDir, sub, requireConditions)
}

func resolvePackageWith(pkgDir, sub string, conds []string) (string, bool) {
	var pj struct {
		Main    string          `json:"main"`
		Exports json.RawMessage `json:"exports"`
	}
	if b, err := os.ReadFile(filepath.Join(pkgDir, "package.json")); err == nil {
		_ = json.Unmarshal(b, &pj)
	}
	if len(pj.Exports) > 0 {
		var exports any
		_ = json.Unmarshal(pj.Exports, &exports)
		subpath := "."
		if sub != "" {
			subpath = "./" + sub
		}
		if t, ok := exportTarget(exports, subpath, conds); ok {
			if r, ok := resolveFileOrDir(filepath.Join(pkgDir, filepath.FromSlash(t))); ok {
				return r, true
			}
		}
	}
	if sub != "" {
		return resolveFileOrDir(filepath.Join(pkgDir, filepath.FromSlash(sub)))
	}
	if pj.Main != "" {
		if r, ok := resolveFileOrDir(filepath.Join(pkgDir, pj.Main)); ok {
			return r, true
		}
	}
	return resolveFileOrDir(filepath.Join(pkgDir, "index"))
}

// resolveImports resolves a package.json "imports" specifier ("#name") in
// the package enclosing fromDir, for require conditions.
func resolveImports(request, fromDir string) (string, bool) {
	return resolveImportsWith(request, fromDir, requireConditions)
}

func resolveImportsWith(request, fromDir string, conds []string) (string, bool) {
	for dir := fromDir; ; dir = filepath.Dir(dir) {
		if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
			var pj struct {
				Imports map[string]any `json:"imports"`
			}
			if json.Unmarshal(b, &pj) != nil || pj.Imports == nil {
				return "", false
			}
			t, ok := pickTarget(pj.Imports[request], "", conds)
			if !ok {
				for pat, target := range pj.Imports {
					prefix, suffix, star := strings.Cut(pat, "*")
					if star && strings.HasPrefix(request, prefix) && strings.HasSuffix(request, suffix) && len(request) >= len(prefix)+len(suffix) {
						if t, ok = pickTarget(target, request[len(prefix):len(request)-len(suffix)], conds); ok {
							break
						}
					}
				}
			}
			if !ok {
				return "", false
			}
			if strings.HasPrefix(t, "./") {
				return resolveFileOrDir(filepath.Join(dir, filepath.FromSlash(t)))
			}
			// A bare target is a dependency, resolved from the package.
			r, err := resolveModuleWith(t, dir, conds)
			return r, err == nil
		}
		if filepath.Dir(dir) == dir {
			return "", false
		}
	}
}

// exportTarget matches package.json "exports" for require conditions.
// (Condition order follows requireConditions, which suffices here.)
func exportTarget(exports any, subpath string, conds []string) (string, bool) {
	if m, ok := exports.(map[string]any); ok {
		for k := range m {
			if strings.HasPrefix(k, ".") {
				if t, ok := m[subpath]; ok {
					return pickTarget(t, "", conds)
				}
				for pat, t := range m {
					prefix, suffix, ok := strings.Cut(pat, "*")
					if ok && strings.HasPrefix(subpath, prefix) && strings.HasSuffix(subpath, suffix) {
						return pickTarget(t, subpath[len(prefix):len(subpath)-len(suffix)], conds)
					}
				}
				return "", false
			}
			break
		}
	}
	if subpath != "." {
		return "", false
	}
	return pickTarget(exports, "", conds)
}

func pickTarget(t any, star string, conds []string) (string, bool) {
	switch v := t.(type) {
	case string:
		return strings.ReplaceAll(v, "*", star), true
	case []any:
		for _, e := range v {
			if r, ok := pickTarget(e, star, conds); ok {
				return r, true
			}
		}
	case map[string]any:
		for _, c := range conds {
			if e, ok := v[c]; ok {
				if r, ok := pickTarget(e, star, conds); ok {
					return r, true
				}
			}
		}
		if e, ok := v["import"]; ok { // ESM-only packages: we transform anyway
			return pickTarget(e, star, conds)
		}
	}
	return "", false
}

func resolveFileOrDir(p string) (string, bool) {
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return p, true
	}
	for _, ext := range requireExts {
		if st, err := os.Stat(p + ext); err == nil && !st.IsDir() {
			return p + ext, true
		}
	}
	if st, err := os.Stat(p); err == nil && st.IsDir() {
		var pj struct {
			Main string `json:"main"`
		}
		if b, err := os.ReadFile(filepath.Join(p, "package.json")); err == nil && json.Unmarshal(b, &pj) == nil && pj.Main != "" {
			if r, ok := resolveFileOrDir(filepath.Join(p, pj.Main)); ok {
				return r, true
			}
		}
		return resolveFileOrDir(filepath.Join(p, "index"))
	}
	return "", false
}

// moduleSource returns a file as CommonJS source for the module wrapper.
func moduleSource(path string, patch func(path, src string) (string, error)) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if strings.HasSuffix(path, ".json") {
		return string(b), nil // goja_nodejs parses JSON modules itself
	}
	src := string(b)
	if patch != nil {
		if src, err = patch(path, src); err != nil {
			return "", err
		}
	}
	// A hashbang line (a CLI's "#!/usr/bin/env node") is a comment to
	// Node; esbuild keeps it first, where the module wrapper puts it
	// mid-line.
	if strings.HasPrefix(src, "#!") {
		src = "//" + src[2:]
	}
	src = replaceImportMeta(path, src, path)
	// import() becomes og.import(specifier, dir) so a top-level-await module
	// can be loaded asynchronously; esbuild would lower it to a synchronous
	// require() that cannot await the target's evaluation (jshost.go).
	src = rewriteDynamicImports(path, src)
	opts := gojatarget.TransformOptions(loaderFor(path), path)
	opts.Format = api.FormatCommonJS
	// esbuild renames a function expression that shares its variable's
	// name (var Root = function Root() {} becomes function Root2); Node
	// keeps it, and Stylus's compiler dispatches on constructor.name.
	opts.KeepNames = true
	// An ES module as Node sees it (.mjs, or .js in a "type": "module"
	// package) imports CommonJS in Node mode: the default import is
	// module.exports even when it sets __esModule (caniuse-api, imported by
	// postcss-reduce-initial). esbuild decides that by the file name.
	if isNodeESM(path) && !strings.HasSuffix(path, ".mjs") && !strings.HasSuffix(path, ".mts") {
		opts.Sourcefile = path + ".mjs"
	}
	res := api.Transform(src, opts)
	if len(res.Errors) > 0 {
		return "", &moduleSyntaxError{fmt.Sprintf("%s: %s", path, res.Errors[0].Text)}
	}
	code := withModuleFields(path, shadowWrapperNames(markESM(gojatarget.Finish(string(res.Code)))))
	if isNodeESM(path) {
		code = withESMRequire(code)
	}
	return code, nil
}

// tlaNotAvailable is the fragment of esbuild's error for a top-level await in
// a target that does not support it ("…("esnext" + N overrides)"). og.import
// detects such a module (moduleHasTLA) to load it through the async path.
const tlaNotAvailable = "Top-level await is not available"

// moduleHasTLA reports whether the module at path awaits at its top level, by
// asking esbuild to lower it to the Goja target (where top-level await is
// unsupported) and checking for its diagnostic. Only ES modules can; others
// (CommonJS, JSON) short-circuit.
func moduleHasTLA(path string, patch func(path, src string) (string, error)) bool {
	if strings.HasSuffix(path, ".json") {
		return false
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	src := string(b)
	if patch != nil {
		if s, err := patch(path, src); err == nil {
			src = s
		}
	}
	if strings.HasPrefix(src, "#!") {
		src = "//" + src[2:]
	}
	opts := gojatarget.TransformOptions(loaderFor(path), path)
	opts.Format = api.FormatCommonJS
	for _, e := range api.Transform(src, opts).Errors {
		if strings.Contains(e.Text, tlaNotAvailable) {
			return true
		}
	}
	return false
}

// withModuleFields gives the module object what Node's has and the
// loader's lacks: module.require (relative to the module, as the module's
// own require resolves), id / filename, path, paths, loaded and children
// (GraphQL Mesh's CLI calls module.require). The prologue follows a "use
// strict" directive and precedes the module's code, so require is still
// the wrapper's parameter.
func withModuleFields(path, code string) string {
	directive := ""
	for _, d := range []string{"\"use strict\";\n", "'use strict';\n"} {
		if strings.HasPrefix(code, d) {
			directive, code = d, code[len(d):]
			break
		}
	}
	var paths []string
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		if filepath.Base(dir) != "node_modules" {
			paths = append(paths, filepath.Join(dir, "node_modules"))
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	pathsJSON, _ := json.Marshal(paths)
	prologue := fmt.Sprintf(`if (typeof module.require !== "function") { const __og_require = require; Object.defineProperty(module, "require", { value: function require(id) { return __og_require(id); }, writable: true, configurable: true }); if (module.id === undefined) module.id = %[1]s; if (module.filename === undefined) module.filename = %[1]s; if (module.path === undefined) module.path = %[2]s; if (module.paths === undefined) module.paths = %[3]s; if (module.loaded === undefined) module.loaded = false; if (module.children === undefined) module.children = []; }`+"\n",
		jsonString(path), jsonString(filepath.Dir(path)), pathsJSON)
	return directive + prologue + code
}

// withESMRequire makes a converted ES module's require (its imports,
// converted) resolve with import conditions, as Node's ESM loader does: a
// dual package gives an ES importer its import build (@colordx/core's
// plugins), whose default export differs from the require build's.
func withESMRequire(code string) string {
	directive := ""
	for _, d := range []string{"\"use strict\";\n", "'use strict';\n"} {
		if strings.HasPrefix(code, d) {
			directive, code = d, code[len(d):]
			break
		}
	}
	return directive + `require = globalThis[Symbol.for("og.esmRequire")](require, __filename);` + "\n" + code
}

// isNodeESM reports whether Node loads path as an ES module: .mjs / .mts,
// or another JavaScript or TypeScript file whose nearest package.json says
// "type": "module".
func isNodeESM(path string) bool {
	switch filepath.Ext(path) {
	case ".mjs", ".mts":
		return true
	case ".cjs", ".cts", ".json":
		return false
	}
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
			var pj struct {
				Type string `json:"type"`
			}
			_ = json.Unmarshal(b, &pj)
			return pj.Type == "module"
		}
		if filepath.Dir(dir) == dir {
			return false
		}
	}
}

// esmExports is the export object an ES module converted by esbuild
// assigns; esmNodeMode is the test in esbuild's __toESM helper that gives
// an importer in Node mode (an .mjs file) module.exports as the default.
var (
	esmExports  = regexp.MustCompile(`(?m)^module\.exports = __toCommonJS\(\w+\);$`)
	esmNodeMode = "isNodeMode || !mod || !mod.__esModule ?"
)

// markESM handles default imports between modules that are ES modules on
// disk but CommonJS once converted. Node gives an ES module's importer its
// real default export, and a CommonJS module's importer (in Node mode)
// module.exports. The converted module marks its exports as an ES
// module's, and the helper honors the mark (also in bundles: HonorESM).
func markESM(code string) string {
	if loc := esmExports.FindStringIndex(code); loc != nil {
		code = code[:loc[1]] + "\nObject.defineProperty(module.exports, Symbol.for(\"og.esm\"), { value: true });" + code[loc[1]:]
	}
	return HonorESM(code)
}

// HonorESM makes esbuild's __toESM helpers in code take an ES module's
// default export from a module marked by markESM.
func HonorESM(code string) string {
	return strings.ReplaceAll(code, esmNodeMode, `isNodeMode && !(mod && mod[Symbol.for("og.esm")]) || !mod || !mod.__esModule ?`)
}

// wrapperDecl matches a top-level lexical declaration of a name the
// CommonJS wrapper binds as a parameter. ES modules have no such bindings
// and commonly declare their own (`const __dirname =
// path.dirname(fileURLToPath(import.meta.url))`), which redeclares the
// parameter once converted. esbuild's output starts top-level statements
// at column 0.
var wrapperDecl = regexp.MustCompile(`(?m)^(?:const|let|class)\s+(?:__dirname|__filename|require|module|exports)\b`)

// shadowWrapperNames puts a module that declares a wrapper name into a
// block, where its declaration shadows the parameter; a leading "use
// strict" directive stays outside so it remains a directive.
func shadowWrapperNames(code string) string {
	if !wrapperDecl.MatchString(code) {
		return code
	}
	directive := ""
	for _, d := range []string{"\"use strict\";\n", "'use strict';\n"} {
		if strings.HasPrefix(code, d) {
			directive, code = d, code[len(d):]
			break
		}
	}
	return directive + "{\n" + code + "\n}\n"
}

// SharedModules names the global symbol (Symbol.for) under which a
// bundle entry registers packages it contains, by bare specifier, for
// run-time require() and computed import() to return.
const SharedModules = "og.sharedModules"

// installRequire enables goja_nodejs's CommonJS loader on vm, adapted for
// build-time tooling: sources are transformed to CommonJS by esbuild (so
// TypeScript and ESM files load), paths resolve with package "exports" and
// the .cjs/.mjs/.ts extensions, and Node built-ins come from
// runtime/nodeshim. Bundled code (which has no module file of its own)
// resolves from the project; module.createRequire(url) from the URL.
func (h *Host) installRequire(vm *sobek.Runtime) error {
	sharedObj, err := vm.RunString(`() => globalThis[Symbol.for("` + SharedModules + `")]`)
	if err != nil {
		return err
	}
	sharedRegistry, _ := sobek.AssertFunction(sharedObj)
	// shared returns the registered module id names: by bare specifier, or
	// by the file that specifier resolves to from the project (Node's
	// module cache is keyed on the file: Vite's config loader imports
	// "vite" by its file URL).
	sharedFiles := map[string]string{}
	registry := func() *sobek.Object {
		v, err := sharedRegistry(sobek.Undefined())
		if o, ok := v.(*sobek.Object); ok && err == nil {
			return o
		}
		return nil
	}
	// sharedName is the registered name whose file is file, or "".
	sharedName := func(file string) string {
		reg := registry()
		if reg == nil {
			return ""
		}
		for _, name := range reg.Keys() {
			f, ok := sharedFiles[name]
			if !ok {
				f, _ = resolveModule(name, h.Dir)
				sharedFiles[name] = f
			}
			if f == file {
				return name
			}
		}
		return ""
	}
	shared := func(id string) sobek.Value {
		reg := registry()
		if reg == nil {
			return nil
		}
		if filepath.IsAbs(id) {
			id = sharedName(id)
		}
		if m := reg.Get(id); id != "" && m != nil && !sobek.IsUndefined(m) {
			return m
		}
		return nil
	}
	reg := require.NewRegistry(
		require.WithLoader(func(path string) ([]byte, error) {
			// goja_nodejs probes paths as files first; directories and
			// missing files must report ModuleFileDoesNotExistError.
			if st, err := os.Stat(path); err != nil || st.IsDir() {
				return nil, require.ModuleFileDoesNotExistError
			}
			if src, ok := nativeSubstitute(path); ok {
				return []byte(src), nil
			}
			src, err := moduleSource(path, h.Patch)
			if err != nil {
				if os.IsNotExist(err) {
					return nil, require.ModuleFileDoesNotExistError
				}
				return nil, err
			}
			return []byte(src), nil
		}),
		require.WithPathResolver(func(base, p string) string {
			// Code without a module file of its own (the bundle) resolves
			// from relative bases ("." and then "node_modules"): anchor them
			// at the project, not the process cwd.
			if !filepath.IsAbs(base) {
				base = filepath.Join(h.Dir, base)
			}
			return resolvePathHook(base, p)
		}),
	)
	reg.RegisterNativeModule(napiRSWasiModule, h.embeddedLoader(napiRSWasiModule))
	// Built-ins answer to both "fs" and "node:fs", ahead of node_modules.
	for name := range nodeshim.Builtins {
		load := h.builtinLoader(name)
		reg.RegisterNativeModule(name, load)
		reg.RegisterNativeModule("node:"+name, load)
	}
	rm := reg.Enable(vm)
	// A native require (like goja_nodejs's own, so the caller frame used for
	// relative resolution stays the same) that reports missing modules the
	// way Node does: an Error with code MODULE_NOT_FOUND, which tooling
	// checks to treat optional dependencies as absent.
	// og.esmRequire(require, filename): the require of a converted ES module
	// (withESMRequire), resolving packages with import conditions first.
	esmRequire := func(call sobek.FunctionCall) sobek.Value {
		orig, ok := sobek.AssertFunction(call.Argument(0))
		if !ok {
			return call.Argument(0)
		}
		dir := filepath.Dir(call.Argument(1).String())
		return vm.ToValue(func(c sobek.FunctionCall) sobek.Value {
			id := c.Argument(0).String()
			target := id
			if !nodeshim.Builtins[strings.TrimPrefix(id, "node:")] && !strings.HasPrefix(id, "file://") {
				if r, err := resolveModuleWith(id, dir, importConditions); err == nil && filepath.IsAbs(r) {
					target = r
				}
			}
			v, err := orig(sobek.Undefined(), vm.ToValue(target))
			if err != nil {
				panic(err)
			}
			return v
		})
	}
	if err := defineSymbol(vm, "og.esmRequire", vm.ToValue(esmRequire)); err != nil {
		return err
	}
	// og.importFrom(specifier, dir): a relative import() specifier resolved
	// from dir (jshost.localizeImports); others unchanged.
	importFrom := func(spec sobek.Value, dir string) sobek.Value {
		if s, ok := spec.Export().(string); ok && (strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../")) {
			return vm.ToValue(filepath.Join(dir, s))
		}
		return spec
	}
	if err := defineSymbol(vm, "og.importFrom", vm.ToValue(importFrom)); err != nil {
		return err
	}
	cjsRequire := vm.ToValue(func(call sobek.FunctionCall) sobek.Value {
		id := call.Argument(0).String()
		// import() (lowered to require by esbuild) takes file: URLs, as in
		// Node (Astro imports its config entry point by URL).
		if strings.HasPrefix(id, "file://") {
			if u, err := url.Parse(id); err == nil {
				id = u.Path
			}
		}
		// A package the bundle itself contains (registered by its entry,
		// see SharedModules) is that instance: Node would load the same
		// file once, and a second copy (Vite's, through a computed
		// import("vite")) has state of its own.
		if m := shared(id); m != nil {
			return m
		}
		// A native addon: og loads none (natives.go substitutes the known
		// ones). Node's error when dlopen fails, which loaders catch to fall
		// back to a WebAssembly build (Qwik's optimizer).
		if strings.HasSuffix(id, ".node") {
			e := jsError(vm, "Error", fmt.Sprintf("og cannot load the native addon %s (no dlopen in a pure Go runtime)", id))
			_ = e.Set("code", "ERR_DLOPEN_FAILED")
			panic(e)
		}
		v, err := rm.Require(id)
		if err == nil {
			return v
		}
		if errors.Is(err, require.InvalidModuleError) || errors.Is(err, require.IllegalModuleNameError) ||
			errors.Is(err, require.NoSuchBuiltInModuleError) || errors.Is(err, require.ModuleFileDoesNotExistError) {
			e := jsError(vm, "Error", fmt.Sprintf("Cannot find module '%s'", id))
			_ = e.Set("code", "MODULE_NOT_FOUND")
			panic(e)
		}
		// A JavaScript exception propagates as is; any other loader error
		// (a module that does not parse) becomes one, which the caller can
		// catch, rather than a Go panic out of the engine.
		var ex *sobek.Exception
		if errors.As(err, &ex) {
			panic(err)
		}
		var syn *sobek.CompilerSyntaxError
		var msyn *moduleSyntaxError
		if errors.As(err, &syn) {
			panic(jsError(vm, "SyntaxError", err.Error()))
		} else if errors.As(err, &msyn) {
			panic(jsError(vm, "SyntaxError", msyn.msg))
		}
		panic(vm.NewGoError(err))
	})
	if err := vm.Set("require", cjsRequire); err != nil {
		return err
	}
	resolve := func(request, fromDir string) string {
		r, err := resolveModule(request, fromDir)
		if err != nil {
			e := vm.NewGoError(err)
			_ = e.Set("code", "MODULE_NOT_FOUND")
			panic(e)
		}
		return r
	}
	// The global require.resolve resolves from the calling module's file,
	// as goja_nodejs's require does (and as Node's per-module require
	// does), or from the project for code without a module file.
	callerResolve := func(call sobek.FunctionCall) sobek.Value {
		id := call.Argument(0).String()
		var dirs []string
		if opts, ok := call.Argument(1).Export().(map[string]any); ok {
			if paths, ok := opts["paths"].([]any); ok {
				for _, p := range paths {
					dirs = append(dirs, fmt.Sprint(p))
				}
			}
		}
		if len(dirs) == 0 {
			dir := h.Dir
			var buf [2]sobek.StackFrame
			if frames := vm.CaptureCallStack(2, buf[:0]); len(frames) == 2 {
				if d := filepath.Dir(frames[1].SrcName()); filepath.IsAbs(d) {
					dir = d
				}
			}
			dirs = []string{dir}
		}
		var lastErr error
		for _, dir := range dirs {
			r, err := resolveModule(id, dir)
			if err == nil {
				return vm.ToValue(strings.TrimPrefix(r, "node:"))
			}
			lastErr = err
		}
		e := vm.NewGoError(lastErr)
		_ = e.Set("code", "MODULE_NOT_FOUND")
		panic(e)
	}
	// og.resolveDyn(specifier, dir): resolve a dynamic import's target to a
	// path (or node:<builtin>) and report whether it awaits at its top level,
	// so og.import (requireFront) can load a top-level-await module through
	// the async path and anything else through the synchronous require.
	tlaOf := map[string]bool{}
	resolveDyn := func(call sobek.FunctionCall) sobek.Value {
		id := call.Argument(0).String()
		if strings.HasPrefix(id, "file://") {
			if u, err := url.Parse(id); err == nil {
				id = u.Path
			}
		}
		o := vm.NewObject()
		// A module the bundle itself contains, or a bare/builtin specifier that
		// resolveModule leaves unresolved, requires synchronously as before.
		if shared(id) != nil || strings.HasSuffix(id, ".node") {
			_ = o.Set("path", id)
			_ = o.Set("tla", false)
			return o
		}
		target := id
		if r, err := resolveModule(id, call.Argument(1).String()); err == nil {
			target = r
		}
		tla := false
		if filepath.IsAbs(target) {
			var ok bool
			if tla, ok = tlaOf[target]; !ok {
				tla = moduleHasTLA(target, h.Patch)
				tlaOf[target] = tla
			}
		}
		_ = o.Set("path", target)
		_ = o.Set("tla", tla)
		return o
	}
	// og.runTLA(path): bundle a top-level-await module (Bundle's ESM fallback
	// wraps its evaluation in a promise under Symbol.for("og.bundleDone") and
	// records the namespace under Symbol.for("og.dynModules")), evaluate it on
	// this VM, and return the promise of its evaluation. requireFront's
	// og.import reads the namespace once the promise settles.
	runTLA := func(call sobek.FunctionCall) sobek.Value {
		path := call.Argument(0).String()
		stdin := fmt.Sprintf("import * as __ogDynNS from %[1]s;\n(globalThis[Symbol.for(%[2]q)] ??= Object.create(null))[%[1]s] = __ogDynNS;\n",
			jsonString(path), "og.dynModules")
		savedTLA := h.TopLevelAwait
		h.TopLevelAwait = true
		code, err := h.Bundle("", stdin, "")
		h.TopLevelAwait = savedTLA
		if err != nil {
			panic(jsError(vm, "Error", err.Error()))
		}
		if _, err := vm.RunString(code); err != nil {
			panic(err)
		}
		done, err := vm.RunString(`globalThis[Symbol.for("og.bundleDone")]`)
		if err != nil {
			panic(err)
		}
		return done
	}
	fn, err := vm.RunString(requireFront)
	if err != nil {
		return err
	}
	install, _ := sobek.AssertFunction(fn)
	_, err = install(sobek.Undefined(), cjsRequire, vm.ToValue(resolve), vm.ToValue(h.Dir), vm.ToValue(callerResolve), vm.ToValue(resolveDyn), vm.ToValue(runTLA))
	return err
}

// resolvePathHook extends goja_nodejs's path joining: bare specifiers looked
// up under node_modules honor package.json "exports", and extension-less
// paths also try the extensions goja_nodejs does not (.cjs, .mjs, .ts…).
func resolvePathHook(base, p string) string {
	joined := require.DefaultPathResolver(base, p)
	if filepath.IsAbs(p) {
		joined = filepath.Clean(p) // DefaultPathResolver would join it onto base
	}
	if filepath.Base(base) == "node_modules" && strings.HasPrefix(p, "#") {
		// goja_nodejs looks "#name" up as a package, first under the
		// requiring module's own node_modules: resolve it in that
		// module's package instead.
		if r, ok := resolveImports(p, filepath.Dir(base)); ok {
			return r
		}
		return joined
	}
	if filepath.Base(base) == "node_modules" && !strings.HasPrefix(p, ".") {
		pkg, sub := splitBare(p)
		if _, err := os.Stat(filepath.Join(base, pkg, "package.json")); err == nil {
			if r, ok := resolvePackage(filepath.Join(base, pkg), sub); ok {
				return r
			}
		}
		return joined
	}
	if _, err := os.Stat(joined); err == nil {
		return joined
	}
	for _, ext := range []string{".js", ".json"} { // goja_nodejs tries these itself
		if _, err := os.Stat(joined + ext); err == nil {
			return joined
		}
	}
	if r, ok := resolveFileOrDir(joined); ok {
		return r
	}
	return joined
}

// builtinLoader bundles a nodeshim module on first require.
func (h *Host) builtinLoader(name string) require.ModuleLoader {
	return func(vm *sobek.Runtime, module *sobek.Object) {
		code, err := h.bundle(fmt.Sprintf("globalThis.__ogBuiltin = require(%q);", "node:"+name), nil)
		if err != nil {
			panic(vm.NewGoError(err))
		}
		if _, err := vm.RunScript("node:"+name, code); err != nil {
			panic(err)
		}
		_ = module.Set("exports", vm.Get("__ogBuiltin"))
	}
}

// requireFront makes createRequire(from) resolve ids from `from` to an
// absolute path (or node:<builtin>) before delegating to goja_nodejs.
const requireFront = `(function (cjsRequire, resolve, root, callerResolve, resolveDyn, runTLA) {
  const dirname = (p) => p.slice(0, p.lastIndexOf("/")) || "/";
  // og.import(specifier, dir) is what moduleSource rewrites import() to. It
  // mirrors esbuild's import()->require() lowering for an ordinary module (a
  // promise of __toESM(require(...))), but loads a top-level-await module
  // asynchronously (runTLA) instead of requiring it synchronously.
  const toNamespace = (m) => {
    if (m && m.__esModule) return m;
    const ns = Object.create(null);
    if (m != null && (typeof m === "object" || typeof m === "function")) {
      for (const k of Object.keys(m)) if (k !== "default") ns[k] = m[k];
    }
    ns.default = m;
    return ns;
  };
  const DYN = Symbol.for("og.dynModules");
  const inflight = new Map();
  const loadTLA = (path) => {
    const reg = globalThis[DYN];
    if (reg && path in reg) return Promise.resolve(reg[path]);
    if (inflight.has(path)) return inflight.get(path);
    const p = Promise.resolve(runTLA(path)).then(
      () => { inflight.delete(path); return globalThis[DYN][path]; },
      (e) => { inflight.delete(path); throw e; },
    );
    inflight.set(path, p);
    return p;
  };
  globalThis[Symbol.for("og.import")] = (spec, dir) => Promise.resolve().then(() => {
    const info = resolveDyn(String(spec), String(dir == null ? root : dir));
    if (info.tla) return loadTLA(info.path);
    return toNamespace(cjsRequire(info.path));
  });
  function makeRequire(fromDir) {
    const require = (id) => cjsRequire(resolve(String(id), fromDir));
    // require.resolve(id, { paths }) is how Babel and others locate plugins.
    require.resolve = (id, options) => {
      const dirs = options && Array.isArray(options.paths) && options.paths.length ? options.paths : [fromDir];
      let lastErr;
      for (const dir of dirs) {
        try {
          const r = resolve(String(id), String(dir));
          return r.startsWith("node:") ? r.slice(5) : r;
        } catch (e) {
          lastErr = e;
        }
      }
      throw lastErr;
    };
    require.resolve.paths = () => [];
    require.cache = Object.create(null);
    require.extensions = { ".js": function () {}, ".json": function () {}, ".node": function () {} };
    return require;
  }
  // globalThis.require stays goja_nodejs's: it hands each loaded module the
  // global require and resolves relative ids from the calling module's file.
  // Give it the properties Node tooling pokes at.
  const projectRequire = makeRequire(root);
  cjsRequire.extensions = projectRequire.extensions;
  cjsRequire.cache = projectRequire.cache;
  cjsRequire.resolve = callerResolve;
  cjsRequire.resolve.paths = () => [];
  globalThis[Symbol.for("og.createRequire")] = (from) => {
    let p = String(from ?? root);
    if (p.startsWith("file://")) p = decodeURIComponent(p.slice(7));
    return makeRequire(p.endsWith("/") ? p.slice(0, -1) : dirname(p));
  };
  // Required modules keep their function names (esbuild's keepNames, see
  // moduleSource), whose __name calls end up in function sources; code
  // rebuilt from a function's text and evaluated in this realm
  // (resolve-url-loader's templates) finds esbuild's helper here.
  if (typeof globalThis.__name !== "function") {
    Object.defineProperty(globalThis, "__name", {
      value: (target, value) => Object.defineProperty(target, "name", { value, configurable: true }),
      writable: true,
      configurable: true,
    });
  }
})`

// jsError is a new instance of the global error class ctor (Error,
// SyntaxError) with message, as Node throws (not the engine's GoError).
func jsError(vm *sobek.Runtime, ctor, message string) *sobek.Object {
	c, _ := vm.Get(ctor).(*sobek.Object)
	if c != nil {
		if o, err := vm.New(c, vm.ToValue(message)); err == nil {
			return o
		}
	}
	return vm.NewGoError(errors.New(message))
}

// moduleSyntaxError is a module source that does not parse (esbuild's
// message): a SyntaxError for the requiring code, as in Node.
type moduleSyntaxError struct{ msg string }

func (e *moduleSyntaxError) Error() string { return e.msg }
