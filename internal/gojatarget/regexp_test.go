package gojatarget

import "testing"

func TestQuoteKeywordClassFields(t *testing.T) {
	in := `class A extends B { set = f("set"); get = 1; async = 2; x = 3; get y() { return obj.set = 1; } static set = 4 }`
	want := `class A extends B { "set" = f("set"); "get" = 1; "async" = 2; x = 3; get y() { return obj.set = 1; } static "set" = 4 }`
	if got := QuoteKeywordClassFields(in); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

// TestContextualThenables: then methods become accessors that wrap them in
// the context of the read; calls of then, strings, existing accessors,
// computed names, super-using and arrow-valued then stay as they are.
func TestContextualThenables(t *testing.T) {
	get := `get then() { return (globalThis.__ogContextThen || ((f) => f))(`
	set := `set then(v) { Object.defineProperty(this, "then", { value: v, writable: true, enumerable: true, configurable: true }); }`
	for _, c := range []struct{ in, want string }{
		{`class Q { then(a, b) { return f(a, b); } }`, `class Q { ` + get + `function then(a, b) { return f(a, b); }); } ` + set + ` }`},
		{`class S { static async then(r) { r(1); } }`, `class S { static ` + get + `async function then(r) { r(1); }); } static ` + set + ` }`},
		{`o = { then(r) { r(1); }, x: 1 };`, `o = { ` + get + `function then(r) { r(1); }); }, ` + set + `, x: 1 };`},
		{`o = { "then": function (r) { r(1); } };`, `o = { ` + get + `function (r) { r(1); }); }, ` + set + ` };`},
		{`p.then(f); s = "then(x) {}"; o = { get then() { return 1; }, ["then"]() {}, then: (r) => r() };`, ""},
		{`class B extends A { then(r) { super.then(r); } }`, ""},
	} {
		want := c.want
		if want == "" {
			want = c.in
		}
		if got := ContextualThenables(c.in); got != want {
			t.Errorf("%s\n got  %s\n want %s", c.in, got, want)
		}
	}
}
