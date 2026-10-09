package next

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/microsoft/TypeScript/tsc/ogapi"
)

// An ES module importing a name its target does not export is an error in
// esbuild ("No matching export"), but only a warning in webpack, which
// binds the import to undefined; next build passes, and the app works as
// long as the code path that uses the name is not taken (react-datocms
// 1.6.3 imports renderRule, which the renderer it depends on exports as
// renderNodeRule). og builds as webpack does: a build that fails only with
// such errors is retried with those imports read from the module's
// namespace instead (undefined, with esbuild's warning).

var noMatchingExportRe = regexp.MustCompile(`^No matching export in "[^"]*" for import "([^"]+)"$`)

// missingImports records the imports to read from namespaces: importer
// path -> imported names.
type missingImports struct {
	mu    sync.Mutex
	names map[string]map[string]bool
}

// note records errs when every one is a missing export; it reports
// whether anything new was recorded (and so whether a retry can help).
func (m *missingImports) note(dir string, errs []api.Message) bool {
	type miss struct{ file, name string }
	var found []miss
	for _, e := range errs {
		sub := noMatchingExportRe.FindStringSubmatch(e.Text)
		if sub == nil || e.Location == nil {
			return false
		}
		file := e.Location.File
		if i := strings.Index(file, ":"); i > 0 && !strings.Contains(file[:i], "/") {
			file = file[i+1:] // "rsc:/abs/path"
		}
		if !filepath.IsAbs(file) {
			file = filepath.Join(dir, file)
		}
		found = append(found, miss{file, sub[1]})
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.names == nil {
		m.names = map[string]map[string]bool{}
	}
	added := false
	for _, f := range found {
		if m.names[f.file] == nil {
			m.names[f.file] = map[string]bool{}
		}
		if !m.names[f.file][f.name] {
			m.names[f.file][f.name] = true
			added = true
		}
	}
	return added
}

func (m *missingImports) of(path string) map[string]bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.names[path]
}

// buildTolerant is api.Build, retried while it fails only with missing
// exports not seen before.
func (b *builder) buildTolerant(opts api.BuildOptions) api.BuildResult {
	for {
		res := api.Build(opts)
		if len(res.Errors) == 0 || !b.missing.note(b.dir, res.Errors) {
			return res
		}
	}
}

// rewriteMissingImports reads the recorded missing imports of the module at
// path from its namespace: `import { a, b as c } from "m"` with a missing
// becomes `import { b as c } from "m"; import * as __og_mx0 from "m"; var a
// = __og_mx0["a"];`.
func (b *builder) rewriteMissingImports(path, code string) string {
	names := b.missing.of(path)
	if len(names) == 0 {
		return code
	}
	m := ogapi.ParseModule(path, code)
	var sb strings.Builder
	last, n := 0, 0
	for _, im := range m.Imports {
		if im.TypeOnly {
			continue
		}
		var keep, read []ogapi.ImportBinding
		def, ns := "", ""
		for _, bd := range im.Bindings {
			switch {
			case names[bd.Imported] && bd.Imported != "*":
				read = append(read, bd)
			case bd.Imported == "default":
				def = bd.Local
			case bd.Imported == "*":
				ns = bd.Local
			default:
				keep = append(keep, bd)
			}
		}
		if len(read) == 0 {
			continue
		}
		spec := jsonStr(im.Specifier)
		sb.WriteString(code[last:im.Start])
		// The other bindings stay imports (a namespace import in a
		// statement of its own: it cannot share one with named imports).
		var parts []string
		for _, k := range keep {
			parts = append(parts, jsonStr(k.Imported)+" as "+k.Local)
		}
		switch {
		case def != "" && len(parts) > 0:
			fmt.Fprintf(&sb, "import %s, { %s } from %s; ", def, strings.Join(parts, ", "), spec)
		case def != "":
			fmt.Fprintf(&sb, "import %s from %s; ", def, spec)
		case len(parts) > 0:
			fmt.Fprintf(&sb, "import { %s } from %s; ", strings.Join(parts, ", "), spec)
		}
		if ns != "" {
			fmt.Fprintf(&sb, "import * as %s from %s; ", ns, spec)
		}
		holder := fmt.Sprintf("__og_mx%d", n)
		n++
		fmt.Fprintf(&sb, "import * as %s from %s;", holder, spec)
		for _, r := range read {
			fmt.Fprintf(&sb, " var %s = %s[%s];", r.Local, holder, jsonStr(r.Imported))
		}
		last = im.End
	}
	if last == 0 {
		return code
	}
	sb.WriteString(code[last:])
	return sb.String()
}
