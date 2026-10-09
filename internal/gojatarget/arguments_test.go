package gojatarget

import (
	"testing"

	"github.com/grafana/sobek"
)

// TestHoldArrowArguments runs code whose functions read arguments only
// through arrow functions, which plain Goja gets wrong (an index out of
// range panic, or another value), after Finish.
func TestHoldArrowArguments(t *testing.T) {
	for _, c := range []struct{ name, src, want string }{
		{"arrow", `function h() { const k = () => arguments[0]; return k(); } h("y")`, "y"},
		{"directive", `function h(a) { "use strict"; const k = () => arguments[0]; return k(); } h("y")`, "y"},
		{"nested arrows", `function h() { const k = () => () => arguments.length; return k()(); } h(1, 2)`, "2"},
		{"method", `var o = { m() { return (() => arguments[0])(); } }; o.m("m")`, "m"},
		{"apply forwarding", `var g = { f(a, b) { return a + b; } };
function w() { return [g].map((m) => m.f.apply(this, arguments))[0]; } w("a", "b")`, "ab"},
		{"direct use kept", `function h() { return arguments.length + (() => arguments[0])(); } h(5)`, "6"},
	} {
		code := Finish(c.src)
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("%s: panic %v\n%s", c.name, r, code)
				}
			}()
			v, err := sobek.New().RunString(code)
			if err != nil || v.String() != c.want {
				t.Errorf("%s: got %v, %v, want %s\n%s", c.name, v, err, c.want, code)
			}
		}()
	}
}
