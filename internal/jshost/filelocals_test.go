package jshost

import "testing"

// TestReplaceImportMeta: import.meta.url, dirname and filename
// expressions are replaced by the file's values, not the same text inside
// strings, template text (code Vite generates for the browser) or comments.
func TestReplaceImportMeta(t *testing.T) {
	const U = `"file:///p/x.js"`
	for _, c := range []struct{ in, want string }{
		{`f(import.meta.url)`, `f(` + U + `)`},
		{"`${import.meta.url}/x`", "`${" + U + "}/x`"},
		{`define({ "import.meta.url": v, 'import.meta.url': w })`, `define({ "import.meta.url": v, 'import.meta.url': w })`},
		{`new URL(".", import.meta.url); import.meta.urlx`, `new URL(".", ` + U + `); import.meta.urlx`},
		{"code = `new URL(${JSON.stringify(f)}, import.meta.url)`", "code = `new URL(${JSON.stringify(f)}, import.meta.url)`"},
		{"// import.meta.url\n/* import.meta.url */ g(import.meta.url)", "// import.meta.url\n/* import.meta.url */ g(" + U + ")"},
		// Node 20.11+: the directory and file paths (SvelteKit reads
		// import.meta.dirname).
		{`join(import.meta.dirname, "a.html"); h(import.meta.filename)`, `join("/p", "a.html"); h("/p/x.js")`},
	} {
		if got := replaceImportMeta("x.js", c.in, "/p/x.js"); got != c.want {
			t.Errorf("%s: got %s, want %s", c.in, got, c.want)
		}
	}
}

// TestFunctionImport: new Function("x", "return import(x)"), which Goja
// cannot compile, becomes the equivalent arrow function.
func TestFunctionImport(t *testing.T) {
	in := `const importEsm = new Function("specifier", "return import(specifier)"); const b = new Function('s', 'return import(s)');`
	want := `const importEsm = ((specifier) => import(specifier)); const b = ((s) => import(s));`
	if got := functionImport.ReplaceAllString(in, "(($2) => import($2))"); got != want {
		t.Fatalf("got %s", got)
	}
}
