# Draft issue for grafana/sobek (not filed)

Drafted 2026-10-10. Not filed: issues and PRs on public repositories are not opened from the moriyoshi-base account; whoever files it does so by hand. Goja (dop251/goja) has the same code and the same bug; a copy for Goja needs only the names and line numbers changed.

---

**Title:** Parser rejects an array binding pattern with element defaults when it is a defaulted object pattern property (`{ p: [a = 1] = [] }`)

### Description

A binding pattern of the form `{ key: [element = default, ...] = default }` fails to parse with `SyntaxError: Invalid destructuring binding target`. It is valid ECMAScript, and V8, SpiderMonkey and JavaScriptCore accept it. It fails in every binding context: function and arrow parameters, `var`/`let`/`const`, `for-of` heads, and `catch` parameters.

Bundlers and minifiers produce this shape. For example, `@react-three/drei`'s `RoundedBox` component compiles to:

```js
function RoundedBox({ args: [w = 1, h = 1, d = 1] = [], radius = 0.05, ...rest }, ref) { /* ... */ }
```

so any bundle containing it fails to compile.

### Reproduction

Sobek at `f56574604191` (2026-10-05); Goja at `0f92c903ca4a` behaves the same.

```go
package main

import (
	"fmt"

	"github.com/grafana/sobek"
)

func main() {
	for _, src := range []string{
		`function f({ p: [a = 1] = [] }) { return a } f({})`, // Node: 1
		`var { p: [a = 1, b = 2] = [9] } = {}; [a, b].join()`, // Node: "9,2"
		`(({ p: [a = 1] = [] }) => a)({ p: [5] })`,           // Node: 5
		`const { p: [[a = 3]] = [[]] } = {}; a`,               // Node: 3
		`var { p: [...[a = 7]] = [] } = {}; a`,                // Node: 7
	} {
		_, err := sobek.New().RunString(src)
		fmt.Println(err)
	}
}
```

Every line prints `SyntaxError: ... Invalid destructuring binding target`.

These parse correctly:

- `{ p: [a = 1] }` (no property default);
- `{ p: [a] = [] }` (no element default);
- `[[a = 1] = []]` (an array inside an array);
- the assignment form `({ p: [a = 1] = [] } = {})`.

### Cause

When the parser reads the property value `[a = 1] = []`, it parses an assignment expression. The left side `[a = 1]` is converted early to an assignment pattern (`ast.ArrayPattern`) whose elements are `*ast.AssignExpression` nodes. When the enclosing object literal is later reinterpreted as a binding pattern, `reinterpretAsBindingElement` passes that `ArrayPattern` to `reinterpretArrayAssignPatternAsBinding` (`parser/expression.go`). That function converts each element with `reinterpretAsDestructBindingTarget`, which accepts no `AssignExpression`:

```go
func (self *_parser) reinterpretArrayAssignPatternAsBinding(pattern *ast.ArrayPattern) *ast.ArrayPattern {
	for i, item := range pattern.Elements {
		pattern.Elements[i] = self.reinterpretAsDestructBindingTarget(item)
	}
	...
}
```

### Proposed fix

Convert the elements as binding *elements*, which may carry an initializer, as `reinterpretArrayObjectPatternAsBinding` already does for object pattern properties:

```diff
 func (self *_parser) reinterpretArrayAssignPatternAsBinding(pattern *ast.ArrayPattern) *ast.ArrayPattern {
 	for i, item := range pattern.Elements {
-		pattern.Elements[i] = self.reinterpretAsDestructBindingTarget(item)
+		pattern.Elements[i] = self.reinterpretAsBindingElement(item)
 	}
```

With this change:

- all the cases above, plus the `catch ({ p: [a = 4] = [] })` and `for (const { p: [a = 6] = [] } of [{}])` forms, run and give Node's values;
- invalid targets are still rejected (`var { p: [a.b = 1] = [] } = {}` is still a SyntaxError);
- `go test ./parser/ .` passes.

A parser test along these lines would cover it:

```go
func TestDefaultedArrayPatternInObjectBinding(t *testing.T) {
	const SCRIPT = `
	function f({ p: [a = 1, b = 2] = [] }) { return a + b; }
	var { q: [c = 3] = [] } = {};
	f({}) + f({ p: [10] }) + c;
	`
	testScript(SCRIPT, valueInt(3+12+3), t)
}
```

### Aside (separate, pre-existing)

While testing, I noticed `var { p: [a, (b)] = [] } = {}` is accepted, with or without the change above. Node rejects it ("Invalid destructuring assignment target"), because a parenthesized name is not a valid binding target. This may deserve its own issue.
