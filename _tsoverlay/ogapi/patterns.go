package ogapi

import (
	"sort"
	"strings"

	"github.com/microsoft/TypeScript/tsc/internal/ast"
	"github.com/microsoft/TypeScript/tsc/internal/core"
	"github.com/microsoft/TypeScript/tsc/internal/parser"
	"github.com/microsoft/TypeScript/tsc/internal/scanner"
)

// HoistDefaultedArrayPatterns rewrites the binding patterns Goja's (and
// Sobek's) parser rejects with "Invalid destructuring binding target": an
// array pattern holding a default (an element's, through nested array
// patterns or a rest element) that is an object pattern property's value
// with a default of its own:
//
//	function f({ args: [w = 1, h = 1] = [], ...rest }) { … }
//
// (the parser reads `[w = 1, h = 1] = []` as an assignment first, and its
// conversion to a binding rejects the elements' defaults). The array
// pattern becomes a temporary name, destructured right after:
//
//	function f({ args: tmp = [], ...rest }) { var [w = 1, h = 1] = tmp; … }
//	const { args: tmp = [] } = o, [w = 1, h = 1] = tmp;
//
// A parameter's pattern is destructured at the top of the body, so later
// parameters' defaults cannot read its names (they read outer bindings) and
// a generator destructures it on its first next(). Patterns in for-in/of
// heads and catch clauses are left alone. Only the outermost such patterns
// are rewritten; ones nested in the moved text are found by running it
// again. tmp names the temporaries; changed reports a rewrite.
func HoistDefaultedArrayPatterns(fileName, text string, tmp func() string) (out string, changed bool) {
	sf := parser.ParseSourceFile(ast.SourceFileParseOptions{FileName: fileName}, text, core.ScriptKindJS)
	type edit struct {
		pos   int
		end   int
		text  string
		order int // among edits at one position: lower first
	}
	var edits []edit
	type fnFix struct {
		fn    *ast.Node
		decls []string
	}
	var fns []*fnFix
	fnIndex := map[*ast.Node]*fnFix{}
	var walk func(n *ast.Node) bool
	walk = func(n *ast.Node) bool {
		if n.Kind == ast.KindBindingElement && n.Parent != nil && n.Parent.Kind == ast.KindObjectBindingPattern {
			b := n.AsBindingElement()
			name := n.Name()
			if b.Initializer != nil && name != nil && name.Kind == ast.KindArrayBindingPattern && arrayPatternHasDefault(name) {
				root := n.Parent
				for root != nil && (root.Kind == ast.KindObjectBindingPattern || root.Kind == ast.KindArrayBindingPattern || root.Kind == ast.KindBindingElement) {
					root = root.Parent
				}
				start := scanner.SkipTrivia(text, name.Pos())
				pattern := text[start:name.End()]
				switch {
				case root != nil && root.Kind == ast.KindParameter && root.Parent != nil && root.Parent.Body() != nil:
					t := tmp()
					edits = append(edits, edit{pos: start, end: name.End(), text: t})
					f := fnIndex[root.Parent]
					if f == nil {
						f = &fnFix{fn: root.Parent}
						fnIndex[root.Parent] = f
						fns = append(fns, f)
					}
					f.decls = append(f.decls, "var "+pattern+" = "+t+";")
					walk(b.Initializer)
					return false
				case root != nil && root.Kind == ast.KindVariableDeclaration && root.Parent != nil && root.Parent.Kind == ast.KindVariableDeclarationList &&
					root.Parent.Parent != nil && (root.Parent.Parent.Kind == ast.KindVariableStatement || root.Parent.Parent.Kind == ast.KindForStatement):
					t := tmp()
					edits = append(edits, edit{pos: start, end: name.End(), text: t})
					edits = append(edits, edit{pos: root.End(), end: root.End(), text: ", " + pattern + " = " + t, order: len(edits)})
					walk(b.Initializer)
					return false
				}
			}
		}
		n.ForEachChild(walk)
		return false
	}
	sf.AsNode().ForEachChild(walk)
	for _, f := range fns {
		body := f.fn.Body()
		bodyStart := scanner.SkipTrivia(text, body.Pos())
		decls := strings.Join(f.decls, " ")
		if body.Kind == ast.KindBlock {
			edits = append(edits, edit{pos: bodyStart + 1, end: bodyStart + 1, text: " " + decls})
			continue
		}
		// An arrow function's expression body becomes a block. Closing
		// braces at one position close the inner function first.
		edits = append(edits, edit{pos: bodyStart, end: bodyStart, text: "{ " + decls + " return "})
		edits = append(edits, edit{pos: body.End(), end: body.End(), text: "; }", order: -scanner.SkipTrivia(text, f.fn.Pos())})
	}
	if len(edits) == 0 {
		return text, false
	}
	sort.SliceStable(edits, func(i, j int) bool {
		if edits[i].pos != edits[j].pos {
			return edits[i].pos < edits[j].pos
		}
		return edits[i].order < edits[j].order
	})
	var sb strings.Builder
	last := 0
	for _, e := range edits {
		sb.WriteString(text[last:e.pos])
		sb.WriteString(e.text)
		last = e.end
	}
	sb.WriteString(text[last:])
	return sb.String(), true
}

// arrayPatternHasDefault reports an element default in an array binding
// pattern or the array patterns nested in it.
func arrayPatternHasDefault(p *ast.Node) bool {
	found := false
	p.ForEachChild(func(c *ast.Node) bool {
		if c.Kind != ast.KindBindingElement {
			return false
		}
		if c.AsBindingElement().Initializer != nil {
			found = true
		} else if name := c.Name(); name != nil && name.Kind == ast.KindArrayBindingPattern && arrayPatternHasDefault(name) {
			found = true
		}
		return found
	})
	return found
}
