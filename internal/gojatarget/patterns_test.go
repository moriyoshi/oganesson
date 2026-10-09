package gojatarget

import (
	"strings"
	"testing"

	"github.com/grafana/sobek"
)

// TestHoistDefaultedArrayPatterns runs, after Finish, patterns the engine's
// parser rejects ("Invalid destructuring binding target"): an array pattern
// with element defaults as a defaulted object pattern property, in
// parameters (@react-three/drei's RoundedBox), declarations and for heads.
// The wanted values are Node 24's.
func TestHoistDefaultedArrayPatterns(t *testing.T) {
	for _, c := range []struct{ name, src, want string }{
		{"param", `function f({ args: [w = 1, h = 2] = [], ...rest }, m) { return [w, h, JSON.stringify(rest), m].join(); } [f({ args: [5], k: 1 }, "m"), f({}, "n")].join("|")`, `5,2,{"k":1},m|1,2,{},n`},
		{"arrow", `var f = ({ p: [a = 1] = [] }) => a * 10; [f({}), f({ p: [3] })].join()`, "10,30"},
		{"arrow object body", `var f = ({ p: [a = 1] = [] }) => ({ a }); JSON.stringify(f({}))`, `{"a":1}`},
		{"nested arrows", `var f = ({ p: [a = 1] = [] }) => ({ q: [b = 2] = [] }) => a + b; [f({})({}), f({ p: [10] })({ q: [20] })].join()`, "3,30"},
		{"var", `var { p: [a = 1, , c = 3] = [], q } = { q: "q" }; [a, c, q].join()`, "1,3,q"},
		{"const nested", `const { p: [a = 1] = [7], r: { s: [b = 2] = [] } = {} } = { r: {} }; [a, b].join()`, "7,2"},
		{"rest", `let { p: [...[a = 4]] = [] } = {}; String(a)`, "4"},
		{"nested array", `let { p: [[a = 5]] = [[]] } = {}; String(a)`, "5"},
		{"computed key", `let k = "p"; let { [k]: [a = 6] = [] } = {}; String(a)`, "6"},
		{"for init", `var out = []; for (let { p: [a = 1] = [] } = {}, i = 0; i < 2; i++) out.push(a + i); out.join()`, "1,2"},
		{"method", `var o = { m({ p: [a = 1] = [] }) { return a; } }; String(o.m({}))`, "1"},
		{"iterator", `function* g() { yield 8; } let { p: [a = 1] = [] } = { p: g() }; String(a)`, "8"},
		{"outer default", `let { p: [a = 1, b = 2] = [9] } = {}; [a, b].join()`, "9,2"},
		{"in a default", `function f({ p: [a = 1] = [((({ q: [b = 2] = [] }) => b)({}))] }) { return a; } String(f({}))`, "2"},
		{"earlier param", `function f(x, { p: [a = x] = [] } = {}) { return a; } String(f(42))`, "42"},
	} {
		code := Finish(c.src)
		v, err := sobek.New().RunString(code)
		if err != nil || v.String() != c.want {
			t.Errorf("%s: got %v, %v, want %s\n%s", c.name, v, err, c.want, code)
		}
	}
	// Code without such patterns is left as it is.
	for _, src := range []string{
		`var { p: [a] = [] } = {}; a[0] = 1;`,
		`({ p: [a = 1] = [] } = {});`,
		`for (const { p: [a = 1] = [] } of [{}]) {}`,
	} {
		if got := Finish(src); got != src {
			t.Errorf("rewrote %s\nto %s", src, got)
		}
	}
	if got := Finish(`var { p: [a = 1] = [] } = {};`); !strings.Contains(got, "__og_dp0") {
		t.Errorf("not rewritten: %s", got)
	}
}
