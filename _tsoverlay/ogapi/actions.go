package ogapi

import (
	"strings"

	"github.com/microsoft/TypeScript/tsc/internal/ast"
	"github.com/microsoft/TypeScript/tsc/internal/binder"
	"github.com/microsoft/TypeScript/tsc/internal/parser"
	"github.com/microsoft/TypeScript/tsc/internal/scanner"
)

// ServerAction is a function whose body starts with a "use server"
// directive (an inline Server Action) or a "use cache" one (a cached
// function), with what next-swc's server_actions transform needs to hoist
// it: byte offsets into the text and the values it closes over.
type ServerAction struct {
	// Directive is "use server", "use cache" or "use cache: <kind>";
	// DirectiveStart and DirectiveEnd span its statement.
	Directive                    string
	DirectiveStart, DirectiveEnd int
	// Start and End span the function: a declaration with its modifiers.
	Start, End int
	// Kind is "declaration", "expression", "arrow" or "method" (an object
	// or class method, which Next rejects).
	Kind     string
	Name     string // the declared or function expression name, if any
	Async    bool
	Exported bool // a declaration's export modifier
	Default  bool // a declaration's default modifier
	// ParamsStart and ParamsEnd span the parameter list's text, without
	// the parentheses.
	ParamsStart, ParamsEnd int
	// BodyStart and BodyEnd span the body block's contents, inside the
	// braces.
	BodyStart, BodyEnd int
	// Captured are the values the function closes over (variables of
	// enclosing functions, not of the module), in order of first use.
	Captured []Capture
	// Nested reports a server action inside another one.
	Nested bool
	// UsesThis reports `this`, `arguments` or `super` (not in a nested
	// non-arrow function), which a hoisted action cannot keep.
	UsesThis bool
}

// Capture is a closed-over value: a variable, or a property path of one
// (`props.id`), as next-swc narrows captures to what the action reads.
type Capture struct {
	Path string // "x" or "x.a.b"
	Refs []CaptureRef
}

// CaptureRef is one use of a capture: the expression it replaces.
// Suffix is the rest of a longer path read through this capture (".b"),
// Shorthand an object literal's shorthand property (`{ x }`).
type CaptureRef struct {
	Start, End int
	Suffix     string
	Shorthand  bool
}

// ServerActions lists the functions of a module whose bodies start with
// a "use server" directive.
func ServerActions(fileName, text string) []ServerAction {
	sf := parser.ParseSourceFile(ast.SourceFileParseOptions{FileName: "/og-module.js"}, text, scriptKindOf(fileName))
	binder.BindSourceFile(sf)
	var out []ServerAction
	var walk func(n *ast.Node, inAction bool) bool
	walk = func(n *ast.Node, inAction bool) bool {
		if d, st := functionDirective(n); d != "" {
			a := serverAction(text, n, inAction)
			a.Directive = d
			a.DirectiveStart, a.DirectiveEnd = scanner.SkipTrivia(text, st.Pos()), st.End()
			out = append(out, a)
			inAction = true
		}
		n.ForEachChild(func(c *ast.Node) bool { return walk(c, inAction) })
		return false
	}
	sf.AsNode().ForEachChild(func(c *ast.Node) bool { return walk(c, false) })
	return out
}

// functionDirective is the "use server" or "use cache[: kind]" directive of
// a function-like node's body prologue, with its statement ("" without).
func functionDirective(n *ast.Node) (string, *ast.Node) {
	switch n.Kind {
	case ast.KindFunctionDeclaration, ast.KindFunctionExpression, ast.KindArrowFunction, ast.KindMethodDeclaration:
	default:
		return "", nil
	}
	body := n.Body()
	if body == nil || body.Kind != ast.KindBlock {
		return "", nil
	}
	for _, st := range body.Statements() {
		if !ast.IsPrologueDirective(st) {
			return "", nil
		}
		if d := st.AsExpressionStatement().Expression.Text(); d == "use server" || d == "use cache" || strings.HasPrefix(d, "use cache:") {
			return d, st
		}
	}
	return "", nil
}

func serverAction(text string, n *ast.Node, nested bool) ServerAction {
	a := ServerAction{Start: scanner.SkipTrivia(text, n.Pos()), End: n.End(), Nested: nested}
	switch n.Kind {
	case ast.KindFunctionDeclaration:
		a.Kind = "declaration"
	case ast.KindFunctionExpression:
		a.Kind = "expression"
	case ast.KindArrowFunction:
		a.Kind = "arrow"
	default:
		a.Kind = "method"
	}
	if name := n.Name(); name != nil && name.Kind == ast.KindIdentifier {
		a.Name = name.Text()
	} else if p := n.Parent; p != nil && p.Kind == ast.KindVariableDeclaration && p.Name() != nil && p.Name().Kind == ast.KindIdentifier {
		// const fn = async () => {}: named after its variable, as next-swc
		// names the hoisted function.
		a.Name = p.Name().Text()
	}
	flags := n.ModifierFlags()
	a.Async = flags&ast.ModifierFlagsAsync != 0
	a.Exported = flags&ast.ModifierFlagsExport != 0
	a.Default = flags&ast.ModifierFlagsDefault != 0
	params := n.ParameterList()
	a.ParamsStart, a.ParamsEnd = params.Pos(), params.End()
	body := n.Body()
	a.BodyStart, a.BodyEnd = scanner.SkipTrivia(text, body.Pos())+1, body.End()-1

	// Captures: references inside the function (parameters included, for
	// default values) whose declaration is in an enclosing function or
	// block, not the module and not the function itself.
	type use struct {
		path       []string
		start, end int
		shorthand  bool
	}
	var uses []use
	var visit func(c *ast.Node) bool
	visit = func(c *ast.Node) bool {
		if ast.IsTypeNode(c) || c.Kind == ast.KindTypeParameter {
			return false
		}
		switch c.Kind {
		case ast.KindThisKeyword, ast.KindSuperKeyword:
			if !insideOwnFunction(c, n) {
				a.UsesThis = true
			}
		case ast.KindIdentifier:
			if c.Text() == "arguments" && !insideOwnFunction(c, n) {
				a.UsesThis = true
			}
			if !isReference(c) || !capturedBy(c, n) {
				return false
			}
			u := use{path: []string{c.Text()}, start: scanner.SkipTrivia(text, c.Pos()), end: c.End()}
			if c.Parent != nil && c.Parent.Kind == ast.KindShorthandPropertyAssignment {
				u.shorthand = true
			} else {
				// Narrow to the property path read (x.a.b), not through
				// optional chains or computed members, and not the last
				// member of a method call (it needs its object as this).
				e := c
				for p := e.Parent; p != nil && p.Kind == ast.KindPropertyAccessExpression; p = e.Parent {
					pa := p.AsPropertyAccessExpression()
					if pa.Expression != e || pa.QuestionDotToken != nil || p.Flags&ast.NodeFlagsOptionalChain != 0 || pa.Name().Kind != ast.KindIdentifier {
						break
					}
					e = p
					u.path = append(u.path, pa.Name().Text())
				}
				for e != c && (isCallee(e) || ast.IsAssignmentTarget(e) || isDeleteOperand(e)) {
					e = e.AsPropertyAccessExpression().Expression
					u.path = u.path[:len(u.path)-1]
				}
				u.end = e.End()
			}
			uses = append(uses, u)
			return false
		}
		c.ForEachChild(visit)
		return false
	}
	for _, p := range n.Parameters() {
		visit(p)
	}
	visit(body)

	// A path read through a shorter captured path is that capture plus a
	// suffix (next-swc keeps the shortest).
	paths := map[string]bool{}
	for _, u := range uses {
		paths[strings.Join(u.path, ".")] = true
	}
	index := map[string]int{}
	for _, u := range uses {
		cut := len(u.path)
		for i := 1; i < len(u.path); i++ {
			if paths[strings.Join(u.path[:i], ".")] {
				cut = i
				break
			}
		}
		key := strings.Join(u.path[:cut], ".")
		i, ok := index[key]
		if !ok {
			i = len(a.Captured)
			index[key] = i
			a.Captured = append(a.Captured, Capture{Path: key})
		}
		suffix := ""
		if cut < len(u.path) {
			suffix = "." + strings.Join(u.path[cut:], ".")
		}
		a.Captured[i].Refs = append(a.Captured[i].Refs, CaptureRef{Start: u.start, End: u.end, Suffix: suffix, Shorthand: u.shorthand})
	}
	return a
}

// capturedBy reports whether identifier id, inside fn, refers to a value
// declared in a function or block enclosing fn (below the module).
func capturedBy(id, fn *ast.Node) bool {
	name := id.Text()
	inside := true
	for c := id; c != nil; c = c.Parent {
		if c == fn.Parent {
			inside = false
		}
		locals := c.Locals()
		if locals == nil {
			continue
		}
		sym, ok := locals[name]
		if !ok || sym.Flags&ast.SymbolFlagsValue == 0 {
			continue
		}
		if inside || c.Kind == ast.KindSourceFile {
			return false
		}
		return sym.ValueDeclaration != fn
	}
	return false // a global
}

// insideOwnFunction reports whether n is inside a non-arrow function (or
// class) nested in fn, which has its own this and arguments.
func insideOwnFunction(n, fn *ast.Node) bool {
	for c := n.Parent; c != nil && c != fn; c = c.Parent {
		switch c.Kind {
		case ast.KindFunctionDeclaration, ast.KindFunctionExpression, ast.KindMethodDeclaration, ast.KindClassDeclaration, ast.KindClassExpression,
			ast.KindGetAccessor, ast.KindSetAccessor, ast.KindConstructor:
			return true
		}
	}
	return false
}

func isCallee(e *ast.Node) bool {
	p := e.Parent
	return p != nil && (p.Kind == ast.KindCallExpression && p.AsCallExpression().Expression == e ||
		p.Kind == ast.KindTaggedTemplateExpression && p.AsTaggedTemplateExpression().Tag == e)
}

func isDeleteOperand(e *ast.Node) bool {
	p := e.Parent
	return p != nil && p.Kind == ast.KindDeleteExpression
}

// ArrowArgumentsHolders lists, for each non-arrow function whose
// `arguments` is read only through nested arrow functions, the offset in
// its body after the directive prologue. Goja's compiler then fails to
// keep the function's arguments object (an index-out-of-range panic, or
// a wrong value), which a reference in the function itself avoids.
func ArrowArgumentsHolders(fileName, text string) []int {
	sf := parser.ParseSourceFile(ast.SourceFileParseOptions{FileName: "/og-module.js"}, text, scriptKindOf(fileName))
	type fn struct {
		node          *ast.Node
		direct, arrow bool
	}
	var stack []*fn
	var out []int
	var walk func(n *ast.Node) bool
	walk = func(n *ast.Node) bool {
		own := false
		switch n.Kind {
		case ast.KindFunctionDeclaration, ast.KindFunctionExpression, ast.KindMethodDeclaration,
			ast.KindGetAccessor, ast.KindSetAccessor, ast.KindConstructor:
			if b := n.Body(); b != nil && b.Kind == ast.KindBlock {
				stack = append(stack, &fn{node: n})
				own = true
			}
		case ast.KindIdentifier:
			if n.Text() == "arguments" && isReference(n) && len(stack) > 0 {
				f := stack[len(stack)-1]
				inArrow := false
				for c := n.Parent; c != nil && c != f.node; c = c.Parent {
					if c.Kind == ast.KindArrowFunction {
						inArrow = true
						break
					}
				}
				if inArrow {
					f.arrow = true
				} else {
					f.direct = true
				}
			}
		}
		n.ForEachChild(walk)
		if own {
			f := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			if f.arrow && !f.direct {
				body := f.node.Body()
				at := scanner.SkipTrivia(text, body.Pos()) + 1
				for _, st := range body.Statements() {
					if !ast.IsPrologueDirective(st) {
						break
					}
					at = st.End()
				}
				out = append(out, at)
			}
		}
		return false
	}
	sf.AsNode().ForEachChild(walk)
	return out
}

// CacheFileFunctions lists the functions a module-level "use cache"
// directive caches: the module's exported async functions (declarations,
// variables initialized with an async function or arrow, exported directly,
// through an export clause or as the default), as cached functions with
// that directive and nothing captured. Other exports are left alone.
func CacheFileFunctions(fileName, text, directive string) []ServerAction {
	sf := parser.ParseSourceFile(ast.SourceFileParseOptions{FileName: "/og-module.js"}, text, scriptKindOf(fileName))
	binder.BindSourceFile(sf)
	exported := map[string]bool{}
	for _, st := range sf.Statements.Nodes {
		switch st.Kind {
		case ast.KindExportDeclaration:
			ed := st.AsExportDeclaration()
			if ed.ModuleSpecifier != nil || ed.ExportClause == nil || ed.ExportClause.Kind != ast.KindNamedExports || ed.IsTypeOnly {
				continue
			}
			for _, el := range ed.ExportClause.AsNamedExports().Elements.Nodes {
				es := el.AsExportSpecifier()
				if es.IsTypeOnly {
					continue
				}
				local := es.Name()
				if es.PropertyName != nil {
					local = es.PropertyName
				}
				exported[local.Text()] = true
			}
		case ast.KindExportAssignment:
			if e := st.AsExportAssignment().Expression; e != nil && e.Kind == ast.KindIdentifier {
				exported[e.Text()] = true
			}
		}
	}
	var out []ServerAction
	add := func(fn *ast.Node, name string, exportedDecl, defaultDecl bool) {
		if fn.ModifierFlags()&ast.ModifierFlagsAsync == 0 || fn.Body() == nil || fn.Body().Kind != ast.KindBlock {
			return
		}
		a := serverAction(text, fn, false)
		a.Directive = directive
		if name != "" {
			a.Name = name
		}
		a.Exported, a.Default = exportedDecl, defaultDecl
		out = append(out, a)
	}
	for _, st := range sf.Statements.Nodes {
		flags := st.ModifierFlags()
		switch st.Kind {
		case ast.KindFunctionDeclaration:
			name := ""
			if st.Name() != nil {
				name = st.Name().Text()
			}
			if flags&ast.ModifierFlagsExport != 0 || exported[name] {
				add(st, name, flags&ast.ModifierFlagsExport != 0, flags&ast.ModifierFlagsDefault != 0)
			}
		case ast.KindVariableStatement:
			for _, d := range st.AsVariableStatement().DeclarationList.AsVariableDeclarationList().Declarations.Nodes {
				vd := d.AsVariableDeclaration()
				if d.Name() == nil || d.Name().Kind != ast.KindIdentifier || vd.Initializer == nil {
					continue
				}
				init := vd.Initializer
				if init.Kind != ast.KindArrowFunction && init.Kind != ast.KindFunctionExpression {
					continue
				}
				if flags&ast.ModifierFlagsExport != 0 || exported[d.Name().Text()] {
					add(init, d.Name().Text(), false, false)
				}
			}
		case ast.KindExportAssignment:
			// export default async function () {} / async () => {}
			if e := st.AsExportAssignment().Expression; e != nil && (e.Kind == ast.KindArrowFunction || e.Kind == ast.KindFunctionExpression) {
				add(e, "", false, false)
			}
		}
	}
	return out
}
