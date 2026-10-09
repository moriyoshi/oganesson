// Package nativegen reads a project's native Go modules and generates what
// exposes them to its JavaScript: the registration of runtime/native
// modules (Go glue compiled into the app binary) and TypeScript declarations
// for og-env.d.ts.
//
// A project's native modules are a Go module in its og/ directory (with a
// go.mod); each package directly under it is one module, og:<dir name>. Its
// exported top-level functions are exported to JavaScript, named in
// lowerCamelCase ("HashPassword" is "hashPassword", "URLFor" is "urlFor");
// a function whose first parameter is a context.Context is asynchronous
// (returns a Promise). Directives in a function's doc comment override that:
//
//	//og:name hash     the JavaScript name
//	//og:async         asynchronous (it runs off the event loop)
//	//og:sync          synchronous
//	//og:skip          not exported
//
// Reading is syntactic (go/parser): no Go toolchain is needed for types.
package nativegen

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/evanw/esbuild/pkg/api"
	"golang.org/x/mod/modfile"

	"github.com/moriyoshi/oganesson/runtime/native"
)

// Dir is the directory of a project's native Go module.
const Dir = "og"

// Project is a project's native Go module.
type Project struct {
	Dir        string    // its directory (<project>/og)
	ModulePath string    // from its go.mod
	GoVersion  string    // its go.mod's go directive
	Replaces   []Replace // its go.mod's replace directives
	Modules    []Module
}

// Replace is a replace directive from the project's go.mod. Go ignores a
// dependency module's own replaces (only the main module's apply), so og
// propagates them into the generated module that builds the app binary.
type Replace struct {
	OldPath    string // module path replaced
	OldVersion string // its version, "" for all versions
	NewPath    string // replacement: a module path, or a filesystem path when NewVersion is ""
	NewVersion string // replacement version, "" when NewPath is a filesystem path
}

// Module is one native module: a package of the Go module.
type Module struct {
	Name       string // og:<Name>
	ImportPath string
	Funcs      []Func
	Types      []string // TypeScript declarations of the package's structs it uses
}

// Func is one exported function.
type Func struct {
	GoName string
	JSName string
	Async  bool
	TS     string // its TypeScript declaration (with JSDoc)
}

var nameRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// Scan reads projectDir's native Go module. It returns nil, nil when the
// project has none (no og/go.mod).
func Scan(projectDir string) (*Project, error) {
	dir := filepath.Join(projectDir, Dir)
	gomod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return nil, nil
	}
	p := &Project{Dir: dir}
	mf, err := modfile.Parse(filepath.Join(dir, "go.mod"), gomod, nil)
	if err != nil {
		return nil, fmt.Errorf("nativegen: %s: %w", filepath.Join(dir, "go.mod"), err)
	}
	if mf.Module != nil {
		p.ModulePath = mf.Module.Mod.Path
	}
	if mf.Go != nil {
		p.GoVersion = mf.Go.Version
	}
	for _, r := range mf.Replace {
		p.Replaces = append(p.Replaces, Replace{
			OldPath:    r.Old.Path,
			OldVersion: r.Old.Version,
			NewPath:    r.New.Path,
			NewVersion: r.New.Version,
		})
	}
	if p.ModulePath == "" {
		return nil, fmt.Errorf("nativegen: %s has no module line", filepath.Join(dir, "go.mod"))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || strings.HasPrefix(e.Name(), "_") || e.Name() == "testdata" {
			continue
		}
		m, err := scanPackage(filepath.Join(dir, e.Name()), e.Name(), p.ModulePath+"/"+e.Name())
		if err != nil {
			return nil, err
		}
		if m != nil {
			p.Modules = append(p.Modules, *m)
		}
	}
	return p, nil
}

// Names are the project's native module names.
func (p *Project) Names() []string {
	if p == nil {
		return nil
	}
	out := make([]string, len(p.Modules))
	for i, m := range p.Modules {
		out[i] = m.Name
	}
	return out
}

// pkgInfo is what type mapping needs of a package.
type pkgInfo struct {
	structs map[string]*ast.StructType   // the package's struct types
	imports map[string]map[string]string // each struct or named type's file's imports
	named   map[string]ast.Expr          // its other named types' underlying types
	used    map[string]bool              // structs the declarations refer to
}

func scanPackage(dir, name, importPath string) (*Module, error) {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool { return !strings.HasSuffix(fi.Name(), "_test.go") }, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	var files []*ast.File
	for pn, pkg := range pkgs {
		if pn == "main" || strings.HasSuffix(pn, "_test") {
			continue
		}
		for _, f := range pkg.Files {
			files = append(files, f)
		}
	}
	if len(files) == 0 {
		return nil, nil
	}
	if !nameRe.MatchString(name) || native.IsReserved(name) {
		return nil, fmt.Errorf("nativegen: og/%s: og:%s is not a usable module name (og uses it, or it has other characters than letters, digits, - and _)", name, name)
	}
	sort.Slice(files, func(i, j int) bool {
		return fset.Position(files[i].Pos()).Filename < fset.Position(files[j].Pos()).Filename
	})
	info := &pkgInfo{structs: map[string]*ast.StructType{}, imports: map[string]map[string]string{}, named: map[string]ast.Expr{}, used: map[string]bool{}}
	for _, f := range files {
		fileImps := fileImports(f)
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, s := range gd.Specs {
				ts := s.(*ast.TypeSpec)
				if st, ok := ts.Type.(*ast.StructType); ok {
					info.structs[ts.Name.Name] = st
					info.imports[ts.Name.Name] = fileImps
				} else {
					info.named[ts.Name.Name] = ts.Type
					info.imports[ts.Name.Name] = fileImps
				}
			}
		}
	}
	m := &Module{Name: name, ImportPath: importPath}
	seen := map[string]string{}
	for _, f := range files {
		imports := fileImports(f)
		for _, d := range f.Decls {
			fd, ok := d.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || !fd.Name.IsExported() || fd.Type.TypeParams != nil {
				continue
			}
			fn, skip, err := scanFunc(fset, fd, imports, info)
			if err != nil {
				return nil, fmt.Errorf("nativegen: og/%s: %w", name, err)
			}
			if skip {
				continue
			}
			if prev, dup := seen[fn.JSName]; dup {
				return nil, fmt.Errorf("nativegen: og/%s: %s and %s are both %q in JavaScript (//og:name renames one)", name, prev, fn.GoName, fn.JSName)
			}
			seen[fn.JSName] = fn.GoName
			m.Funcs = append(m.Funcs, fn)
		}
	}
	// Struct declarations, including those reached from other structs.
	done := map[string]bool{}
	for changed := true; changed; {
		changed = false
		var names []string
		for n := range info.used {
			if !done[n] {
				names = append(names, n)
			}
		}
		sort.Strings(names)
		for _, n := range names {
			done[n] = true
			changed = true
			m.Types = append(m.Types, structTS(n, info.structs[n], info))
		}
	}
	sort.Strings(m.Types)
	return m, nil
}

// fileImports maps a file's import names to their paths.
func fileImports(f *ast.File) map[string]string {
	out := map[string]string{}
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		name := p[strings.LastIndexByte(p, '/')+1:]
		if imp.Name != nil {
			name = imp.Name.Name
		}
		out[name] = p
	}
	return out
}

// directiveRe matches a directive comment's text (after "//"), as go/ast
// recognizes them.
var directiveRe = regexp.MustCompile(`^[a-z0-9]+:[a-z0-9]`)

func scanFunc(fset *token.FileSet, fd *ast.FuncDecl, imports map[string]string, info *pkgInfo) (Func, bool, error) {
	fn := Func{GoName: fd.Name.Name, JSName: lowerCamel(fd.Name.Name)}
	var doc []string
	asyncSet := false
	if fd.Doc != nil {
		for _, c := range fd.Doc.List {
			text := strings.TrimPrefix(c.Text, "//")
			switch {
			case strings.HasPrefix(text, "og:name "):
				fn.JSName = strings.TrimSpace(strings.TrimPrefix(text, "og:name "))
			case text == "og:async":
				fn.Async, asyncSet = true, true
			case text == "og:sync":
				fn.Async, asyncSet = false, true
			case text == "og:skip":
				return fn, true, nil
			case directiveRe.MatchString(text):
				// Another tool's directive (//go:noinline, //nolint:...).
			default:
				doc = append(doc, strings.TrimPrefix(text, " "))
			}
		}
		for len(doc) > 0 && strings.TrimSpace(doc[len(doc)-1]) == "" {
			doc = doc[:len(doc)-1]
		}
	}
	if !regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`).MatchString(fn.JSName) {
		return fn, false, fmt.Errorf("%s: %q is not a JavaScript name", fd.Name.Name, fn.JSName)
	}
	var params []string
	i := 0
	for pi, field := range fd.Type.Params.List {
		if _, ok := field.Type.(*ast.Ellipsis); ok {
			return fn, false, fmt.Errorf("%s is variadic (//og:skip it)", fd.Name.Name)
		}
		isCtx := pi == 0 && isSelector(field.Type, imports, "context", "Context")
		names := field.Names
		if len(names) == 0 {
			names = []*ast.Ident{nil}
		}
		for ni, n := range names {
			if isCtx && ni == 0 {
				if !asyncSet {
					fn.Async = true
				}
				i++
				continue
			}
			pname := fmt.Sprintf("arg%d", i)
			if n != nil && n.Name != "_" {
				pname = n.Name
			}
			params = append(params, pname+": "+tsType(field.Type, imports, info, true))
			i++
		}
	}
	result := "void"
	if res := fd.Type.Results; res != nil {
		var types []ast.Expr
		for _, f := range res.List {
			n := len(f.Names)
			if n == 0 {
				n = 1
			}
			for j := 0; j < n; j++ {
				types = append(types, f.Type)
			}
		}
		if last := len(types) - 1; last >= 0 && isIdent(types[last], "error") {
			types = types[:last]
		}
		switch len(types) {
		case 0:
		case 1:
			result = tsType(types[0], imports, info, true)
		default:
			return fn, false, fmt.Errorf("%s returns more than a value and an error (//og:skip it)", fd.Name.Name)
		}
	}
	if fn.Async {
		result = "Promise<" + result + ">"
	}
	var b strings.Builder
	if len(doc) > 0 {
		b.WriteString("  /**\n")
		for _, l := range doc {
			b.WriteString(strings.TrimRight("   * "+strings.ReplaceAll(l, "*/", "*\\/"), " ") + "\n")
		}
		b.WriteString("   */\n")
	}
	fmt.Fprintf(&b, "  export function %s(%s): %s;\n", fn.JSName, strings.Join(params, ", "), result)
	fn.TS = b.String()
	_ = fset
	return fn, false, nil
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}

func isSelector(e ast.Expr, imports map[string]string, path, sel string) bool {
	se, ok := e.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	x, ok := se.X.(*ast.Ident)
	return ok && imports[x.Name] == path && se.Sel.Name == sel
}

// tsType maps a Go type to TypeScript as values convert (runtime/native):
// at the top level []byte is a Uint8Array and time.Time a Date; inside
// structs, slices and maps, encoding/json's forms (a base64 string, an
// RFC 3339 string).
func tsType(e ast.Expr, imports map[string]string, info *pkgInfo, top bool) string {
	switch t := e.(type) {
	case *ast.Ident:
		switch t.Name {
		case "string":
			return "string"
		case "bool":
			return "boolean"
		case "int64", "uint64":
			// int64/uint64 cross as a bigint at any depth (full 64-bit
			// precision): top-level directly, nested as a bigint the runtime
			// carries through JSON with a marker (runtime/native/convert.go),
			// since a number cannot represent values beyond 2^53.
			return "bigint"
		case "int", "int8", "int16", "int32", "uint", "uint8", "uint16", "uint32", "uintptr",
			"float32", "float64", "byte", "rune":
			return "number"
		case "any", "error":
			return "unknown"
		}
		if _, ok := info.structs[t.Name]; ok {
			info.used[t.Name] = true
			return t.Name
		}
		if u, ok := info.named[t.Name]; ok {
			// Resolve the underlying type with the imports of the file that
			// declared the named type, not the referencing file: a selector
			// like time.Time in the declaration is meaningless under the
			// caller's imports (which may not import that package at all).
			return tsType(u, info.imports[t.Name], info, top)
		}
		return "unknown"
	case *ast.StarExpr:
		return tsType(t.X, imports, info, top) + " | null"
	case *ast.ArrayType:
		if isIdent(t.Elt, "byte") || isIdent(t.Elt, "uint8") {
			if top {
				return "Uint8Array"
			}
			return "string"
		}
		return wrapArray(tsType(t.Elt, imports, info, false)) + "[]"
	case *ast.MapType:
		return "Record<string, " + tsType(t.Value, imports, info, false) + ">"
	case *ast.SelectorExpr:
		if isSelector(t, imports, "time", "Time") {
			if top {
				return "Date"
			}
			return "string"
		}
		if isSelector(t, imports, "time", "Duration") {
			return "number"
		}
		return "unknown"
	case *ast.InterfaceType:
		return "unknown"
	case *ast.StructType:
		return inlineStruct(t, imports, info)
	}
	return "unknown"
}

func wrapArray(s string) string {
	if strings.ContainsAny(s, "| ") {
		return "(" + s + ")"
	}
	return s
}

// jsonField is a struct field as encoding/json names it: its name, whether
// it may be absent (omitempty), or skipped ("-", unexported).
func jsonField(f *ast.Field, n *ast.Ident) (name string, optional, skip bool) {
	name = n.Name
	if !ast.IsExported(name) {
		return "", false, true
	}
	if f.Tag != nil {
		tag, _ := strconv.Unquote(f.Tag.Value)
		if v, ok := lookupTag(tag, "json"); ok {
			parts := strings.Split(v, ",")
			if parts[0] == "-" && len(parts) == 1 {
				return "", false, true
			}
			if parts[0] != "" {
				name = parts[0]
			}
			for _, o := range parts[1:] {
				if o == "omitempty" || o == "omitzero" {
					optional = true
				}
			}
		}
	}
	return name, optional, false
}

func lookupTag(tag, key string) (string, bool) {
	for tag != "" {
		i := 0
		for i < len(tag) && tag[i] == ' ' {
			i++
		}
		tag = tag[i:]
		j := strings.Index(tag, ":\"")
		if j < 0 {
			return "", false
		}
		name := tag[:j]
		rest := tag[j+1:]
		end := 1
		for end < len(rest) && rest[end] != '"' {
			if rest[end] == '\\' {
				end++
			}
			end++
		}
		if end >= len(rest) {
			return "", false
		}
		v, _ := strconv.Unquote(rest[:end+1])
		if name == key {
			return v, true
		}
		tag = rest[end+1:]
	}
	return "", false
}

func structFields(st *ast.StructType, imports map[string]string, info *pkgInfo) []string {
	var out []string
	for _, f := range st.Fields.List {
		if len(f.Names) == 0 {
			// An embedded struct: its fields, flattened (as encoding/json).
			if id, ok := f.Type.(*ast.Ident); ok {
				if est, ok := info.structs[id.Name]; ok {
					out = append(out, structFields(est, imports, info)...)
				}
			}
			continue
		}
		for _, n := range f.Names {
			name, optional, skip := jsonField(f, n)
			if skip {
				continue
			}
			key := name
			if !regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*$`).MatchString(key) {
				key = strconv.Quote(key)
			}
			if optional {
				key += "?"
			}
			out = append(out, key+": "+tsType(f.Type, imports, info, false))
		}
	}
	return out
}

func structTS(name string, st *ast.StructType, info *pkgInfo) string {
	fields := structFields(st, info.imports[name], info)
	if len(fields) == 0 {
		return fmt.Sprintf("  export interface %s {}\n", name)
	}
	return fmt.Sprintf("  export interface %s {\n    %s;\n  }\n", name, strings.Join(fields, ";\n    "))
}

func inlineStruct(st *ast.StructType, imports map[string]string, info *pkgInfo) string {
	return "{ " + strings.Join(structFields(st, imports, info), "; ") + " }"
}

// lowerCamel is a Go name in JavaScript's convention: the leading capital,
// or a leading initialism, in lower case ("HashPassword" -> "hashPassword",
// "URLFor" -> "urlFor", "ID" -> "id").
func lowerCamel(s string) string {
	r := []rune(s)
	i := 0
	for i < len(r) && unicode.IsUpper(r[i]) {
		i++
	}
	switch {
	case i == 0:
		return s
	case i == len(r):
		return strings.ToLower(s)
	case i > 1:
		i-- // the last capital starts the next word: "URLFor" -> "url" + "For"
	}
	return strings.ToLower(string(r[:i])) + string(r[i:])
}

// TypeScript is the declarations of the project's modules, for
// og-env.d.ts.
func (p *Project) TypeScript() string {
	if p == nil || len(p.Modules) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n// Native Go modules (og/<name>), generated by og build.\n")
	for _, m := range p.Modules {
		fmt.Fprintf(&b, "declare module %q {\n", "og:"+m.Name)
		for _, t := range m.Types {
			b.WriteString(t)
		}
		for _, f := range m.Funcs {
			b.WriteString(f.TS)
		}
		b.WriteString("}\n")
	}
	return b.String()
}

// Glue is a Go file of package pkg that registers the project's modules
// with runtime/native.
func (p *Project) Glue(pkg string) ([]byte, error) {
	var b bytes.Buffer
	fmt.Fprintf(&b, "// Code generated by og build from %s/; DO NOT EDIT.\n\npackage %s\n\nimport (\n\t\"github.com/moriyoshi/oganesson/runtime/native\"\n", Dir, pkg)
	alias := func(name string) string {
		return "og_" + strings.Map(func(r rune) rune {
			if r == '-' {
				return '_'
			}
			return r
		}, name)
	}
	for _, m := range p.Modules {
		fmt.Fprintf(&b, "\t%s %q\n", alias(m.Name), m.ImportPath)
	}
	b.WriteString(")\n\nfunc init() {\n")
	for _, m := range p.Modules {
		fmt.Fprintf(&b, "\tnative.Register(native.Module{Name: %q, Funcs: []native.Func{\n", m.Name)
		for _, f := range m.Funcs {
			fmt.Fprintf(&b, "\t\t{Name: %q, Fn: %s.%s, Async: %v},\n", f.JSName, alias(m.Name), f.GoName, f.Async)
		}
		b.WriteString("\t}})\n")
	}
	b.WriteString("}\n")
	return format.Source(b.Bytes())
}

// Plugin is the esbuild plugin resolving the project's native modules
// (og:<name>, runtime/native), for a bundle of its server code.
func Plugin(projectDir string) (api.Plugin, error) {
	p, err := Scan(projectDir)
	if err != nil {
		return api.Plugin{}, err
	}
	return native.Plugin(p.Names()), nil
}
