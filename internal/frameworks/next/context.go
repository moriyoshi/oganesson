package next

import (
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/microsoft/TypeScript/tsc/ogapi"
)

// A computed relative require or import (require(`./c/${name}.tsx`),
// import("./locales/" + lang)) names any file matching its pattern; webpack
// bundles them all as a context module. esbuild expands such globs too, but
// loads the matches itself, outside og's plugins, so they would miss their
// layer (React Server Components or SSR) and og's transforms (a CSS module
// they import fails the build). expandComputedImports rewrites each call into
// a map of literal requires or imports instead, which og resolves as any
// other.

// contextFileLimit bounds the files one pattern may match.
const contextFileLimit = 1000

// contextExts are the extensions a request may leave out (webpack's
// resolve.extensions for Next).
var contextExts = map[string]bool{".js": true, ".jsx": true, ".ts": true, ".tsx": true, ".mjs": true, ".cjs": true, ".json": true}

// expandComputedImports rewrites the computed relative requires and imports
// of the module at file. A pattern that matches no file is left to the
// bundler.
func expandComputedImports(file, code string) string {
	if !strings.Contains(code, "require(") && !strings.Contains(code, "import(") {
		return code
	}
	imps := ogapi.ComputedImports(file, code)
	if len(imps) == 0 {
		return code
	}
	var sb strings.Builder
	last, used := 0, false
	for _, im := range imps {
		keys := contextKeys(filepath.Dir(file), im.Parts)
		if len(keys) == 0 {
			continue
		}
		used = true
		sb.WriteString(code[last:im.Start])
		sb.WriteString("__og_context({")
		for i, k := range keys {
			if i > 0 {
				sb.WriteString(", ")
			}
			call := "require"
			if im.Import {
				call = "import"
			}
			fmt.Fprintf(&sb, "%s: () => %s(%s)", jsonStr(k), call, jsonStr(k))
		}
		fmt.Fprintf(&sb, "}, %s, %v)", code[im.ArgStart:im.ArgEnd], im.Import)
		last = im.End
	}
	if !used {
		return code
	}
	sb.WriteString(code[last:])
	// A declaration: hoisted, and harmless in ES modules and CommonJS alike.
	sb.WriteString(`
function __og_context(m, k, async) {
  const f = Object.prototype.hasOwnProperty.call(m, k) ? m[k] : undefined;
  if (f) return f();
  const e = new Error("Cannot find module '" + k + "'");
  e.code = "MODULE_NOT_FOUND";
  if (async) return Promise.reject(e);
  throw e;
}
`)
	return sb.String()
}

// contextKeys are the requests matching parts (static pieces around dynamic
// ones, the first relative) that name files under dir, sorted: each file
// as named from the pattern's directory, and without its extension when
// that is one a request may leave out and the shorter request matches too.
// A dynamic piece matches any characters, "/" included (webpack's
// recursive context).
func contextKeys(dir string, parts []string) []string {
	var re strings.Builder
	re.WriteString("^")
	for i, p := range parts {
		if i > 0 {
			re.WriteString("(.*)")
		}
		re.WriteString(regexp.QuoteMeta(p))
	}
	re.WriteString("$")
	pattern, err := regexp.Compile(re.String())
	if err != nil {
		return nil
	}
	base := parts[0][:strings.LastIndex(parts[0], "/")+1] // "./", "../c/"
	root := filepath.Join(dir, filepath.FromSlash(base))
	if st, err := os.Stat(root); err != nil || !st.IsDir() {
		return nil
	}
	seen := map[string]bool{}
	count := 0
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && (d.Name() == "node_modules" || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		key := base + filepath.ToSlash(rel)
		matched := false
		if pattern.MatchString(key) {
			seen[key], matched = true, true
		}
		if ext := path.Ext(key); contextExts[ext] {
			if short := strings.TrimSuffix(key, ext); pattern.MatchString(short) {
				seen[short], matched = true, true
			}
		}
		if matched {
			if count++; count >= contextFileLimit {
				return filepath.SkipAll
			}
		}
		return nil
	})
	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
