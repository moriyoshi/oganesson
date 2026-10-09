package ogapi

import (
	"github.com/microsoft/TypeScript/tsc/internal/ast"
	"github.com/microsoft/TypeScript/tsc/internal/parser"
	"github.com/microsoft/TypeScript/tsc/internal/scanner"
)

// TypeofSpan is one `typeof <name>` expression (byte offsets).
type TypeofSpan struct{ Start, End int }

// TypeofGlobal lists the `typeof name` expressions of a module, for
// replacing them with a constant as Next's SWC optimizer does
// (optimizer.globals.typeofs: typeof window). bound reports a declaration
// of name anywhere in the module (a variable, parameter, function, class or
// import binding): the expressions may then not refer to the global, and
// callers leave the module alone.
func TypeofGlobal(fileName, text, name string) (spans []TypeofSpan, bound bool) {
	sf := parser.ParseSourceFile(ast.SourceFileParseOptions{FileName: fileName}, text, scriptKindOf(fileName))
	var walk func(n *ast.Node) bool
	walk = func(n *ast.Node) bool {
		switch n.Kind {
		case ast.KindTypeOfExpression:
			if e := n.AsTypeOfExpression().Expression; e != nil && e.Kind == ast.KindIdentifier && e.Text() == name {
				spans = append(spans, TypeofSpan{Start: scanner.SkipTrivia(text, n.Pos()), End: n.End()})
			}
		case ast.KindVariableDeclaration, ast.KindParameter, ast.KindFunctionDeclaration, ast.KindFunctionExpression,
			ast.KindClassDeclaration, ast.KindClassExpression, ast.KindBindingElement, ast.KindImportSpecifier,
			ast.KindImportClause, ast.KindNamespaceImport, ast.KindImportEqualsDeclaration:
			for _, b := range bindingNames(n.Name()) {
				if b == name {
					bound = true
				}
			}
		}
		n.ForEachChild(walk)
		return false
	}
	sf.AsNode().ForEachChild(walk)
	return spans, bound
}
