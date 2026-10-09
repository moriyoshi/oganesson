package ogapi

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/microsoft/TypeScript/tsc/internal/ast"
	"github.com/microsoft/TypeScript/tsc/internal/core"
	"github.com/microsoft/TypeScript/tsc/internal/parser"
	"github.com/microsoft/TypeScript/tsc/internal/scanner"
)

// Module is the top level of a parsed JS/TS module: what source transforms
// (such as next/font's) need, as plain data with byte offsets into the text.
type Module struct {
	Imports []Import
	Vars    []VarStatement
	// Statements lists every top-level statement.
	Statements []Statement
	// Refs lists, per identifier name, the offsets of its references
	// outside import declarations: expressions and shorthand properties, not
	// declared names, property names or member names.
	Refs map[string][]int
}

// Statement is a top-level statement.
type Statement struct {
	Start, End int
	// Kind is "import", "var", "function", "class", "export" (an export
	// clause, possibly with From), "default" (export default / export =),
	// or "other".
	Kind     string
	Exported bool     // carries the export modifier
	Names    []string // top-level names it declares
	// Specifiers and From describe an export clause: export { a as b } from "m".
	Specifiers []ExportSpecifier
	From       string
	TypeOnly   bool
}

// ExportSpecifier is `Local as Exported` in an export clause.
type ExportSpecifier struct {
	Local, Exported string
	TypeOnly        bool
}

// Import is an import declaration.
type Import struct {
	Specifier  string
	Start, End int
	TypeOnly   bool
	Bindings   []ImportBinding
}

// ImportBinding is one imported name: Imported is "default" for a default
// import and "*" for a namespace import.
type ImportBinding struct {
	Local, Imported string
}

// VarStatement is a top-level variable statement.
type VarStatement struct {
	Start, End int
	Kind       string // "const", "let" or "var"
	Exported   bool
	Decls      []VarDecl
}

// VarDecl is one declarator. When its initializer calls an identifier,
// Callee is that identifier and Args the arguments as JSON values (or
// ArgsError when one is not a literal).
type VarDecl struct {
	Start, End  int    // the declarator, e.g. `a = f(1)`
	Name        string // "" for destructuring patterns
	Callee      string
	CalleeStart int
	Args        []any
	ArgsError   string
}

// ParseModule parses a module's top level. The file name's extension picks
// the dialect (.ts, .tsx, .jsx, .js, …).
func ParseModule(fileName, text string) *Module {
	kind := core.ScriptKindJS
	switch {
	case strings.HasSuffix(fileName, ".tsx"):
		kind = core.ScriptKindTSX
	case strings.HasSuffix(fileName, ".ts"), strings.HasSuffix(fileName, ".mts"), strings.HasSuffix(fileName, ".cts"):
		kind = core.ScriptKindTS
	case strings.HasSuffix(fileName, ".jsx"):
		kind = core.ScriptKindJSX
	}
	sf := parser.ParseSourceFile(ast.SourceFileParseOptions{FileName: fileName}, text, kind)
	m := &Module{}
	for _, st := range sf.Statements.Nodes {
		start, end := scanner.SkipTrivia(text, st.Pos()), st.End()
		m.Statements = append(m.Statements, statement(st, start, end))
		switch st.Kind {
		case ast.KindImportDeclaration:
			d := st.AsImportDeclaration()
			im := Import{Start: start, End: end}
			if d.ModuleSpecifier != nil && d.ModuleSpecifier.Kind == ast.KindStringLiteral {
				im.Specifier = d.ModuleSpecifier.Text()
			}
			if c := d.ImportClause; c != nil {
				cl := c.AsImportClause()
				im.TypeOnly = cl.IsTypeOnly()
				if n := c.Name(); n != nil {
					im.Bindings = append(im.Bindings, ImportBinding{Local: n.Text(), Imported: "default"})
				}
				if nb := cl.NamedBindings; nb != nil {
					switch nb.Kind {
					case ast.KindNamespaceImport:
						im.Bindings = append(im.Bindings, ImportBinding{Local: nb.Name().Text(), Imported: "*"})
					case ast.KindNamedImports:
						for _, el := range nb.AsNamedImports().Elements.Nodes {
							spec := el.AsImportSpecifier()
							if spec.IsTypeOnly {
								continue
							}
							imported := el.Name().Text()
							if spec.PropertyName != nil {
								imported = spec.PropertyName.Text()
							}
							im.Bindings = append(im.Bindings, ImportBinding{Local: el.Name().Text(), Imported: imported})
						}
					}
				}
			}
			m.Imports = append(m.Imports, im)
		case ast.KindVariableStatement:
			list := st.AsVariableStatement().DeclarationList
			vs := VarStatement{Start: start, End: end, Kind: "var", Exported: st.ModifierFlags()&ast.ModifierFlagsExport != 0}
			switch {
			case list.Flags&ast.NodeFlagsConst != 0:
				vs.Kind = "const"
			case list.Flags&ast.NodeFlagsLet != 0:
				vs.Kind = "let"
			}
			for _, dn := range list.AsVariableDeclarationList().Declarations.Nodes {
				d := dn.AsVariableDeclaration()
				vd := VarDecl{Start: scanner.SkipTrivia(text, dn.Pos()), End: dn.End()}
				if n := dn.Name(); n != nil && n.Kind == ast.KindIdentifier {
					vd.Name = n.Text()
				}
				if init := skipOuter(d.Initializer); init != nil && init.Kind == ast.KindCallExpression {
					call := init.AsCallExpression()
					if call.Expression.Kind == ast.KindIdentifier {
						vd.Callee = call.Expression.Text()
						vd.CalleeStart = scanner.SkipTrivia(text, call.Expression.Pos())
						for _, a := range call.Arguments.Nodes {
							v, err := literal(a)
							if err != nil {
								vd.ArgsError = err.Error()
								break
							}
							vd.Args = append(vd.Args, v)
						}
					}
				}
				vs.Decls = append(vs.Decls, vd)
			}
			m.Vars = append(m.Vars, vs)
		}
	}
	m.Refs = map[string][]int{}
	var walk func(n *ast.Node) bool
	walk = func(n *ast.Node) bool {
		if n.Kind == ast.KindImportDeclaration {
			return false
		}
		if n.Kind == ast.KindIdentifier && isReference(n) {
			m.Refs[n.Text()] = append(m.Refs[n.Text()], scanner.SkipTrivia(text, n.Pos()))
		}
		n.ForEachChild(walk)
		return false
	}
	sf.AsNode().ForEachChild(walk)
	return m
}

// isReference reports whether an identifier refers to a binding, rather
// than naming a declaration, a property or a member.
func isReference(n *ast.Node) bool {
	p := n.Parent
	if p == nil {
		return true
	}
	if p.Kind == ast.KindShorthandPropertyAssignment {
		return true
	}
	if name := p.Name(); name == n {
		return false
	}
	return true
}

func statement(st *ast.Node, start, end int) Statement {
	s := Statement{Start: start, End: end, Kind: "other", Exported: st.ModifierFlags()&ast.ModifierFlagsExport != 0}
	if st.ModifierFlags()&ast.ModifierFlagsDefault != 0 {
		s.Kind = "default"
	}
	switch st.Kind {
	case ast.KindImportDeclaration:
		s.Kind = "import"
	case ast.KindVariableStatement:
		s.Kind = "var"
		for _, d := range st.AsVariableStatement().DeclarationList.AsVariableDeclarationList().Declarations.Nodes {
			s.Names = append(s.Names, bindingNames(d.Name())...)
		}
	case ast.KindFunctionDeclaration, ast.KindClassDeclaration:
		if s.Kind != "default" {
			s.Kind = map[ast.Kind]string{ast.KindFunctionDeclaration: "function", ast.KindClassDeclaration: "class"}[st.Kind]
		}
		if n := st.Name(); n != nil {
			s.Names = []string{n.Text()}
		}
	case ast.KindExportAssignment:
		s.Kind = "default"
	case ast.KindExportDeclaration:
		d := st.AsExportDeclaration()
		s.Kind, s.TypeOnly = "export", d.IsTypeOnly
		if d.ModuleSpecifier != nil && d.ModuleSpecifier.Kind == ast.KindStringLiteral {
			s.From = d.ModuleSpecifier.Text()
		}
		if c := d.ExportClause; c != nil && c.Kind == ast.KindNamedExports {
			for _, el := range c.AsNamedExports().Elements.Nodes {
				spec := el.AsExportSpecifier()
				local := el.Name().Text()
				if spec.PropertyName != nil {
					local = spec.PropertyName.Text()
				}
				s.Specifiers = append(s.Specifiers, ExportSpecifier{Local: local, Exported: el.Name().Text(), TypeOnly: spec.IsTypeOnly})
			}
		}
	}
	return s
}

// bindingNames collects the identifiers a binding name declares.
func bindingNames(n *ast.Node) []string {
	if n == nil {
		return nil
	}
	if n.Kind == ast.KindIdentifier {
		return []string{n.Text()}
	}
	var out []string
	n.ForEachChild(func(c *ast.Node) bool {
		if c.Kind == ast.KindBindingElement {
			out = append(out, bindingNames(c.Name())...)
		} else {
			out = append(out, bindingNames(c)...)
		}
		return false
	})
	return out
}

func skipOuter(n *ast.Node) *ast.Node {
	for n != nil {
		switch n.Kind {
		case ast.KindParenthesizedExpression:
			n = n.AsParenthesizedExpression().Expression
		case ast.KindAsExpression:
			n = n.AsAsExpression().Expression
		case ast.KindSatisfiesExpression:
			n = n.AsSatisfiesExpression().Expression
		default:
			return n
		}
	}
	return nil
}

// errNotLiteral is next-swc's message for non-literal font loader values.
var errNotLiteral = fmt.Errorf("Font loader values must be explicitly written literals.")

// literal converts a literal expression to JSON data, as next-swc's
// expr_to_json does for font loader arguments.
func literal(n *ast.Node) (any, error) {
	n = skipOuter(n)
	if n == nil {
		return nil, errNotLiteral
	}
	switch n.Kind {
	case ast.KindStringLiteral, ast.KindNoSubstitutionTemplateLiteral:
		return n.Text(), nil
	case ast.KindNumericLiteral:
		return strconv.ParseFloat(n.Text(), 64)
	case ast.KindTrueKeyword:
		return true, nil
	case ast.KindFalseKeyword:
		return false, nil
	case ast.KindNullKeyword:
		return nil, nil
	case ast.KindPrefixUnaryExpression:
		u := n.AsPrefixUnaryExpression()
		if u.Operator == ast.KindMinusToken && u.Operand.Kind == ast.KindNumericLiteral {
			f, err := strconv.ParseFloat(u.Operand.Text(), 64)
			return -f, err
		}
	case ast.KindArrayLiteralExpression:
		out := []any{}
		for _, el := range n.Elements() {
			v, err := literal(el)
			if err != nil {
				return nil, err
			}
			out = append(out, v)
		}
		return out, nil
	case ast.KindObjectLiteralExpression:
		out := map[string]any{}
		for _, p := range n.Properties() {
			if p.Kind != ast.KindPropertyAssignment {
				if p.Kind == ast.KindSpreadAssignment {
					return nil, fmt.Errorf("Unexpected spread")
				}
				return nil, errNotLiteral
			}
			key := p.Name()
			switch key.Kind {
			case ast.KindIdentifier, ast.KindStringLiteral, ast.KindNumericLiteral:
			default:
				return nil, errNotLiteral
			}
			v, err := literal(p.AsPropertyAssignment().Initializer)
			if err != nil {
				return nil, err
			}
			out[key.Text()] = v
		}
		return out, nil
	}
	return nil, errNotLiteral
}

// Span is a piece of the parsed text: its byte offsets and its text.
type Span struct {
	Start, End int
	Text       string
}

// DynamicImport is an import(…) call: offsets of the whole call expression
// and of its first argument.
type DynamicImport struct {
	Start, End       int
	ArgStart, ArgEnd int
}

// DynamicImports lists the import(…) calls of a JS/TS module (byte offsets).
func DynamicImports(fileName, text string) []DynamicImport {
	kind := core.ScriptKindJS
	switch {
	case strings.HasSuffix(fileName, ".tsx"):
		kind = core.ScriptKindTSX
	case strings.HasSuffix(fileName, ".ts"), strings.HasSuffix(fileName, ".mts"), strings.HasSuffix(fileName, ".cts"):
		kind = core.ScriptKindTS
	case strings.HasSuffix(fileName, ".jsx"):
		kind = core.ScriptKindJSX
	}
	sf := parser.ParseSourceFile(ast.SourceFileParseOptions{FileName: fileName}, text, kind)
	var out []DynamicImport
	var walk func(n *ast.Node) bool
	walk = func(n *ast.Node) bool {
		if n.Kind == ast.KindCallExpression {
			call := n.AsCallExpression()
			if call.Expression.Kind == ast.KindImportKeyword && len(call.Arguments.Nodes) > 0 {
				arg := call.Arguments.Nodes[0]
				out = append(out, DynamicImport{
					Start: scanner.SkipTrivia(text, n.Pos()), End: n.End(),
					ArgStart: scanner.SkipTrivia(text, arg.Pos()), ArgEnd: arg.End(),
				})
			}
		}
		n.ForEachChild(walk)
		return false
	}
	sf.AsNode().ForEachChild(walk)
	return out
}

// RequireCalls lists the calls of the functions named in names (require,
// esbuild's __require) with at least one argument, as DynamicImport spans
// (the call and its first argument).
func RequireCalls(fileName, text string, names map[string]bool) []DynamicImport {
	sf := parser.ParseSourceFile(ast.SourceFileParseOptions{FileName: fileName}, text, scriptKindOf(fileName))
	var out []DynamicImport
	var walk func(n *ast.Node) bool
	walk = func(n *ast.Node) bool {
		if n.Kind == ast.KindCallExpression {
			call := n.AsCallExpression()
			if call.Expression.Kind == ast.KindIdentifier && names[call.Expression.Text()] && len(call.Arguments.Nodes) > 0 {
				arg := call.Arguments.Nodes[0]
				out = append(out, DynamicImport{
					Start: scanner.SkipTrivia(text, n.Pos()), End: n.End(),
					ArgStart: scanner.SkipTrivia(text, arg.Pos()), ArgEnd: arg.End(),
				})
			}
		}
		n.ForEachChild(walk)
		return false
	}
	sf.AsNode().ForEachChild(walk)
	return out
}

// ClassFieldNames lists class field declarations whose (identifier) name
// is in names, as offsets of the name.
func ClassFieldNames(fileName, text string, names map[string]bool) []Span {
	sf := parser.ParseSourceFile(ast.SourceFileParseOptions{FileName: fileName}, text, core.ScriptKindJS)
	var out []Span
	var walk func(n *ast.Node) bool
	walk = func(n *ast.Node) bool {
		if n.Kind == ast.KindPropertyDeclaration {
			if name := n.Name(); name != nil && name.Kind == ast.KindIdentifier && names[name.Text()] {
				start := scanner.SkipTrivia(text, name.Pos())
				out = append(out, Span{Start: start, End: name.End(), Text: text[start:name.End()]})
			}
		}
		n.ForEachChild(walk)
		return false
	}
	sf.AsNode().ForEachChild(walk)
	return out
}

// ThenMethod is a method named then of a class or object literal (or an
// object literal's then: function … property): the member's byte offsets,
// whether it is static or a class member, and the method as a function
// expression (async / generator kept).
type ThenMethod struct {
	Start, End int
	Static     bool
	InClass    bool
	Fn         string
}

// ThenMethods lists the then methods of a JavaScript text that can be
// restated as function expressions: not computed, not accessors, not
// arrow-valued, and not using super (a function expression has no home
// object). Members nested inside a listed one are not listed.
func ThenMethods(fileName, text string) []ThenMethod {
	sf := parser.ParseSourceFile(ast.SourceFileParseOptions{FileName: fileName}, text, core.ScriptKindJS)
	isThen := func(name *ast.Node) bool {
		return name != nil && (name.Kind == ast.KindIdentifier || name.Kind == ast.KindStringLiteral) && name.Text() == "then"
	}
	usesSuper := func(n *ast.Node) bool {
		found := false
		var walk func(c *ast.Node) bool
		walk = func(c *ast.Node) bool {
			if c.Kind == ast.KindSuperKeyword {
				found = true
				return true
			}
			// A nested function or class has its own super binding... except
			// arrow functions, which share it.
			if c.Kind == ast.KindFunctionExpression || c.Kind == ast.KindFunctionDeclaration || c.Kind == ast.KindClassExpression || c.Kind == ast.KindClassDeclaration ||
				(c.Kind == ast.KindMethodDeclaration && c != n) {
				return false
			}
			c.ForEachChild(walk)
			return found
		}
		n.ForEachChild(walk)
		return found
	}
	var out []ThenMethod
	var walk func(n *ast.Node) bool
	walk = func(n *ast.Node) bool {
		parent := n.Parent
		inClass := parent != nil && (parent.Kind == ast.KindClassDeclaration || parent.Kind == ast.KindClassExpression)
		inObject := parent != nil && parent.Kind == ast.KindObjectLiteralExpression
		switch {
		case n.Kind == ast.KindMethodDeclaration && (inClass || inObject) && isThen(n.Name()) && n.Body() != nil && !usesSuper(n):
			fn := "function"
			if ast.HasSyntacticModifier(n, ast.ModifierFlagsAsync) {
				fn = "async " + fn
			}
			if n.BodyData().AsteriskToken != nil {
				fn += "*"
			}
			start := scanner.SkipTrivia(text, n.Pos())
			out = append(out, ThenMethod{Start: start, End: n.End(), Static: ast.HasStaticModifier(n), InClass: inClass, Fn: fn + " then" + text[n.Name().End():n.End()]})
			return false
		case n.Kind == ast.KindPropertyAssignment && inObject && isThen(n.Name()):
			init := n.AsPropertyAssignment().Initializer
			if init != nil && init.Kind == ast.KindFunctionExpression {
				start := scanner.SkipTrivia(text, n.Pos())
				fnStart := scanner.SkipTrivia(text, init.Pos())
				out = append(out, ThenMethod{Start: start, End: n.End(), Fn: text[fnStart:init.End()]})
				return false
			}
		}
		n.ForEachChild(walk)
		return false
	}
	sf.AsNode().ForEachChild(walk)
	return out
}

// ImportMetaURLs lists the import.meta.url, import.meta.dirname and
// import.meta.filename expressions of a JS/TS module (byte offsets; Text
// tells which), and not the text inside strings, templates or comments.
func ImportMetaURLs(fileName, text string) []Span {
	// The parser wants an absolute, normalized name; only the kind matters.
	sf := parser.ParseSourceFile(ast.SourceFileParseOptions{FileName: "/og-module.js"}, text, scriptKindOf(fileName))
	var out []Span
	var walk func(n *ast.Node) bool
	walk = func(n *ast.Node) bool {
		if n.Kind == ast.KindPropertyAccessExpression {
			pa := n.AsPropertyAccessExpression()
			if name := pa.Name().Text(); pa.Expression.Kind == ast.KindMetaProperty && pa.Expression.AsMetaProperty().KeywordToken == ast.KindImportKeyword && (name == "url" || name == "dirname" || name == "filename") {
				start := scanner.SkipTrivia(text, n.Pos())
				out = append(out, Span{Start: start, End: n.End(), Text: text[start:n.End()]})
				return false
			}
		}
		n.ForEachChild(walk)
		return false
	}
	sf.AsNode().ForEachChild(walk)
	return out
}

func scriptKindOf(fileName string) core.ScriptKind {
	switch {
	case strings.HasSuffix(fileName, ".tsx"):
		return core.ScriptKindTSX
	case strings.HasSuffix(fileName, ".ts"), strings.HasSuffix(fileName, ".mts"), strings.HasSuffix(fileName, ".cts"):
		return core.ScriptKindTS
	case strings.HasSuffix(fileName, ".jsx"):
		return core.ScriptKindJSX
	}
	return core.ScriptKindJS
}
