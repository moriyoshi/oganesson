package ogapi

import (
	"github.com/microsoft/TypeScript/tsc/internal/ast"
	"github.com/microsoft/TypeScript/tsc/internal/parser"
)

// CommonJSExports finds a CommonJS module's export names as Node's
// cjs-module-lexer detects them (the names an ES module may import from
// it): exports.x = / module.exports.x = assignments (chained ones too),
// Object.defineProperty(exports, "x", …), and the keys of an object
// literal assigned to module.exports. Reexports are the specifiers of
// module.exports = require("…") and __exportStar(require("…"), exports)
// (TypeScript's and tslib's helpers, __export as well).
func CommonJSExports(fileName, text string) (names, reexports []string) {
	sf := parser.ParseSourceFile(ast.SourceFileParseOptions{FileName: "/og-module.js"}, text, scriptKindOf(fileName))
	seen := map[string]bool{}
	add := func(n string) {
		if n != "" && n != "__esModule" && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	var walk func(n *ast.Node) bool
	walk = func(n *ast.Node) bool {
		switch n.Kind {
		case ast.KindBinaryExpression:
			b := n.AsBinaryExpression()
			if b.OperatorToken.Kind == ast.KindEqualsToken {
				left := skipOuter(b.Left)
				if name, ok := exportsMember(left); ok {
					add(name)
				} else if isModuleExports(left) {
					right := skipOuter(b.Right)
					switch right.Kind {
					case ast.KindObjectLiteralExpression:
						for _, p := range right.AsObjectLiteralExpression().Properties.Nodes {
							switch p.Kind {
							case ast.KindPropertyAssignment, ast.KindShorthandPropertyAssignment, ast.KindMethodDeclaration:
								if nm := p.Name(); nm != nil {
									add(propertyNameText(nm))
								}
							case ast.KindSpreadAssignment:
								if spec, ok := requireCall(p.AsSpreadAssignment().Expression); ok {
									reexports = append(reexports, spec)
								}
							}
						}
					case ast.KindCallExpression:
						if spec, ok := requireCall(right); ok {
							reexports = append(reexports, spec)
						}
					}
				}
			}
		case ast.KindCallExpression:
			call := n.AsCallExpression()
			args := call.Arguments.Nodes
			callee := skipOuter(call.Expression)
			// Object.defineProperty(exports, "name", …)
			if callee.Kind == ast.KindPropertyAccessExpression && len(args) >= 2 {
				pa := callee.AsPropertyAccessExpression()
				if pa.Expression.Kind == ast.KindIdentifier && pa.Expression.AsIdentifier().Text == "Object" && pa.Name().Text() == "defineProperty" &&
					(isExports(skipOuter(args[0])) || isModuleExports(skipOuter(args[0]))) {
					if s, ok := staticString(skipOuter(args[1])); ok {
						add(s)
					}
				}
			}
			// __exportStar(require("x"), exports), tslib.__exportStar(...),
			// __export(require("x")).
			if helper := calleeName(callee); (helper == "__exportStar" || helper == "__export") && len(args) >= 1 {
				if spec, ok := requireCall(args[0]); ok {
					reexports = append(reexports, spec)
				}
			}
		}
		n.ForEachChild(walk)
		return false
	}
	sf.AsNode().ForEachChild(walk)
	return names, reexports
}

func isExports(n *ast.Node) bool {
	return n.Kind == ast.KindIdentifier && n.AsIdentifier().Text == "exports"
}

// isModuleExports matches module.exports.
func isModuleExports(n *ast.Node) bool {
	if n.Kind != ast.KindPropertyAccessExpression {
		return false
	}
	pa := n.AsPropertyAccessExpression()
	return pa.Expression.Kind == ast.KindIdentifier && pa.Expression.AsIdentifier().Text == "module" && pa.Name().Text() == "exports"
}

// exportsMember matches exports.x, module.exports.x and exports["x"].
func exportsMember(n *ast.Node) (string, bool) {
	switch n.Kind {
	case ast.KindPropertyAccessExpression:
		pa := n.AsPropertyAccessExpression()
		obj := skipOuter(pa.Expression)
		if isExports(obj) || isModuleExports(obj) {
			return pa.Name().Text(), true
		}
	case ast.KindElementAccessExpression:
		ea := n.AsElementAccessExpression()
		obj := skipOuter(ea.Expression)
		if isExports(obj) || isModuleExports(obj) {
			if s, ok := staticString(skipOuter(ea.ArgumentExpression)); ok {
				return s, true
			}
		}
	}
	return "", false
}

// requireCall matches require("x") (possibly wrapped, as in
// __importDefault(require("x")) it is not).
func requireCall(n *ast.Node) (string, bool) {
	n = skipOuter(n)
	if n.Kind != ast.KindCallExpression {
		return "", false
	}
	call := n.AsCallExpression()
	if c := skipOuter(call.Expression); c.Kind != ast.KindIdentifier || c.AsIdentifier().Text != "require" || len(call.Arguments.Nodes) != 1 {
		return "", false
	}
	return staticString(skipOuter(call.Arguments.Nodes[0]))
}

// calleeName is f for f(…) and o.f(…).
func calleeName(n *ast.Node) string {
	switch n.Kind {
	case ast.KindIdentifier:
		return n.AsIdentifier().Text
	case ast.KindPropertyAccessExpression:
		return n.AsPropertyAccessExpression().Name().Text()
	}
	return ""
}

func propertyNameText(n *ast.Node) string {
	switch n.Kind {
	case ast.KindIdentifier:
		return n.AsIdentifier().Text
	case ast.KindStringLiteral:
		return n.AsStringLiteral().Text
	}
	return ""
}
