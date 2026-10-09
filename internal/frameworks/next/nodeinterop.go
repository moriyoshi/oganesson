package next

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/microsoft/TypeScript/tsc/ogapi"
	"github.com/moriyoshi/oganesson/internal/jshost"
)

// An ES module's default import of a CommonJS module is that module's
// module.exports in Node, whatever its __esModule flag; webpack follows
// Node for .mjs files and .js files of "type": "module" packages, and so
// does esbuild ("Node mode") when it knows the importer's module type.
// It knows it from the importer's path for .mjs files, but for a .js file
// only from its own resolver, which plugin-resolved modules (og's layers)
// bypass: there, a default import of a CommonJS module flagged __esModule
// is its .default (tslib's modules/index.js reads undefined from tslib.js,
// which Chakra UI's framer-motion imports). og restores Node's rule: such
// an importer's default imports go through nodeDefaultPrefix, which
// resolves a CommonJS target to a module exporting require(target) as its
// default.

const nodeDefaultPrefix = "og-node-default:"

// isNodeModeESM reports a .js / .jsx file whose package is "type":
// "module" (the files webpack treats as strict ES modules and esbuild would
// import in Node mode).
func isNodeModeESM(path string) bool {
	if ext := filepath.Ext(path); ext != ".js" && ext != ".jsx" {
		return false
	}
	return packageType(filepath.Dir(path)) == "module"
}

var packageTypes sync.Map // dir -> "module", "commonjs" or ""

// packageType is the "type" of the nearest package.json at or above dir.
func packageType(dir string) string {
	if t, ok := packageTypes.Load(dir); ok {
		return t.(string)
	}
	t := ""
	if b, err := os.ReadFile(filepath.Join(dir, "package.json")); err == nil {
		var pkg struct{ Type string }
		_ = json.Unmarshal(b, &pkg)
		t = pkg.Type
		if t == "" {
			t = "commonjs"
		}
	} else if parent := filepath.Dir(dir); parent != dir {
		t = packageType(parent)
	}
	packageTypes.Store(dir, t)
	return t
}

// rewriteNodeDefaultImports routes the default imports of a Node-mode ES
// module through nodeDefaultPrefix (an import with other bindings is split
// in two).
func rewriteNodeDefaultImports(path, code string) string {
	if !strings.Contains(code, "import") {
		return code
	}
	m := ogapi.ParseModule(path, code)
	var sb strings.Builder
	last := 0
	for _, im := range m.Imports {
		// next/font's loaders are compiled away by og's font transform,
		// which looks for these imports (geist is a "type": "module" package).
		if im.TypeOnly || strings.HasPrefix(im.Specifier, "og:") || strings.HasPrefix(im.Specifier, "node:") || !maybeJSSpecifier(im.Specifier) || slices.Contains(fontLoaders, im.Specifier) {
			continue
		}
		def, others := "", []string{}
		for _, b := range im.Bindings {
			switch {
			case b.Imported == "default":
				def = b.Local
			case b.Imported == "*":
				others = append(others, "* as "+b.Local)
			default:
				others = append(others, jsonStr(b.Imported)+" as "+b.Local)
			}
		}
		if def == "" {
			continue
		}
		sb.WriteString(code[last:im.Start])
		spec := jsonStr(im.Specifier)
		sb.WriteString("import " + def + " from " + jsonStr(nodeDefaultPrefix+im.Specifier) + ";")
		switch {
		case len(others) == 1 && strings.HasPrefix(others[0], "* as "):
			sb.WriteString(" import " + others[0] + " from " + spec + ";")
		case len(others) > 0:
			sb.WriteString(" import { " + strings.Join(others, ", ") + " } from " + spec + ";")
		}
		last = im.End
	}
	if last == 0 {
		return code
	}
	sb.WriteString(code[last:])
	return sb.String()
}

var commonJSFiles sync.Map // path -> bool

// isCommonJSFile reports a module Node loads as CommonJS: a .cjs file, or
// a .js file of a package that is not "type": "module" and has no import
// or export of its own (esbuild's rule for such files).
func isCommonJSFile(path string) bool {
	switch filepath.Ext(path) {
	case ".cjs":
		return true
	case ".js":
	default:
		return false
	}
	if v, ok := commonJSFiles.Load(path); ok {
		return v.(bool)
	}
	cjs := false
	if packageType(filepath.Dir(path)) != "module" {
		if src, err := os.ReadFile(path); err == nil {
			cjs = !hasModuleSyntax(path, string(src))
		}
	}
	commonJSFiles.Store(path, cjs)
	return cjs
}

// hasModuleSyntax reports top-level import or export statements.
func hasModuleSyntax(path, src string) bool {
	if !strings.Contains(src, "import") && !strings.Contains(src, "export") {
		return false
	}
	m := ogapi.ParseModule(path, src)
	if len(m.Imports) > 0 {
		return true
	}
	for _, st := range m.Statements {
		if st.Exported || st.Kind == "export" || st.Kind == "default" {
			return true
		}
	}
	return false
}

// maybeJSSpecifier reports a specifier that may name a CommonJS module:
// not an asset (a .wasm, a stylesheet, an image, JSON), which other
// plugins resolve by their extension and esbuild imports alike in either
// mode.
func maybeJSSpecifier(spec string) bool {
	switch strings.ToLower(filepath.Ext(spec)) {
	case "", ".js", ".cjs", ".jsx":
		return true
	case ".mjs", ".ts", ".tsx", ".mts", ".cts", ".json", ".wasm", ".node", ".css", ".scss", ".sass", ".less",
		".svg", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".avif", ".ico", ".bmp", ".txt", ".md", ".html", ".woff", ".woff2", ".ttf", ".otf":
		return false
	}
	// A bare specifier's "extension" may be part of its name (lodash.merge).
	return !strings.HasPrefix(spec, ".") && !strings.HasPrefix(spec, "/")
}

// commonJSExports are a CommonJS module's export names (ogapi's
// cjs-module-lexer equivalent), with those of the modules it reexports
// (followed a few levels, as the lexer's users do), sorted.
func commonJSExports(path string, depth int) []string {
	src, err := os.ReadFile(path)
	if err != nil || depth > 4 {
		return nil
	}
	names, reexports := ogapi.CommonJSExports(path, string(src))
	seen := map[string]bool{}
	var out []string
	add := func(n string) {
		if !seen[n] {
			seen[n] = true
			out = append(out, n)
		}
	}
	for _, n := range names {
		add(n)
	}
	for _, spec := range reexports {
		target, err := jshost.ResolveNode(spec, filepath.Dir(path), false)
		if err != nil || !filepath.IsAbs(target) {
			continue
		}
		for _, n := range commonJSExports(target, depth+1) {
			if n != "default" { // export * does not reexport a default
				add(n)
			}
		}
	}
	sort.Strings(out)
	return out
}
