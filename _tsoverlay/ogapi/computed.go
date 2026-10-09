package ogapi

import (
	"github.com/microsoft/TypeScript/tsc/internal/ast"
	"github.com/microsoft/TypeScript/tsc/internal/parser"
	"github.com/microsoft/TypeScript/tsc/internal/scanner"
)

// ComputedImport is a require(…) or import(…) whose specifier is computed
// from a relative prefix: a template literal (`./x/${a}.js`) or a string
// concatenation ("./x/" + a + ".js"). Parts are the static pieces around the
// dynamic ones (len(Parts) is one more than their number); the first starts
// with "./" or "../". Start and End span the whole call, ArgStart and ArgEnd
// its argument.
type ComputedImport struct {
	Start, End       int
	ArgStart, ArgEnd int
	Import           bool // import(…), else require(…)
	Parts            []string
}

// ComputedImports lists a module's computed relative requires and imports,
// which bundlers resolve to every matching file (webpack's context modules,
// esbuild's glob imports).
func ComputedImports(fileName, text string) []ComputedImport {
	sf := parser.ParseSourceFile(ast.SourceFileParseOptions{FileName: "/og-module.js"}, text, scriptKindOf(fileName))
	var out []ComputedImport
	var walk func(n *ast.Node) bool
	walk = func(n *ast.Node) bool {
		if n.Kind == ast.KindCallExpression {
			call := n.AsCallExpression()
			isImport := call.Expression.Kind == ast.KindImportKeyword
			isRequire := call.Expression.Kind == ast.KindIdentifier && call.Expression.AsIdentifier().Text == "require"
			if (isImport || isRequire) && call.Arguments != nil && len(call.Arguments.Nodes) == 1 {
				arg := call.Arguments.Nodes[0]
				if parts, ok := computedParts(arg); ok && len(parts) > 1 && (hasPrefix(parts[0], "./") || hasPrefix(parts[0], "../")) {
					out = append(out, ComputedImport{
						Start: scanner.SkipTrivia(text, n.Pos()), End: n.End(),
						ArgStart: scanner.SkipTrivia(text, arg.Pos()), ArgEnd: arg.End(),
						Import: isImport, Parts: parts,
					})
				}
			}
		}
		n.ForEachChild(walk)
		return false
	}
	sf.AsNode().ForEachChild(walk)
	return out
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }

// computedParts splits a specifier expression into static parts around
// dynamic ones; ok is false for an expression that is not a template
// literal or a concatenation starting with a string.
func computedParts(n *ast.Node) ([]string, bool) {
	n = skipOuter(n)
	switch n.Kind {
	case ast.KindTemplateExpression:
		t := n.AsTemplateExpression()
		parts := []string{t.Head.Text()}
		for _, span := range t.TemplateSpans.Nodes {
			parts = append(parts, span.AsTemplateSpan().Literal.Text())
		}
		return parts, true
	case ast.KindBinaryExpression:
		b := n.AsBinaryExpression()
		if b.OperatorToken.Kind != ast.KindPlusToken {
			return nil, false
		}
		left, ok := computedParts(b.Left)
		if !ok {
			return nil, false
		}
		right := skipOuter(b.Right)
		if s, ok := staticString(right); ok {
			left[len(left)-1] += s
			return left, true
		}
		if rp, ok := computedParts(right); ok {
			// a + `x${b}y`: the left's last part joins the right's first.
			left[len(left)-1] += rp[0]
			return append(left, rp[1:]...), true
		}
		return append(left, ""), true
	}
	if s, ok := staticString(n); ok {
		return []string{s}, true
	}
	return nil, false
}

func staticString(n *ast.Node) (string, bool) {
	switch n.Kind {
	case ast.KindStringLiteral:
		return n.AsStringLiteral().Text, true
	case ast.KindNoSubstitutionTemplateLiteral:
		return n.AsNoSubstitutionTemplateLiteral().Text, true
	}
	return "", false
}
