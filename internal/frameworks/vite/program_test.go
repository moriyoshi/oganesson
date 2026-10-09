package vite

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBundleProgram covers the server-program bundle's own plugins and
// resolution: each
// project file's import.meta.url is its URL in the packaged layout, sharp
// is og's built-in, and a dynamic import of a package that is not
// installed (outside a try, where esbuild would otherwise fail the build)
// becomes a stub that throws only when reached, as in Node.
func TestBundleProgram(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "dist", "server"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"node_modules/dual/package.json": `{"name":"dual","exports":{"import":"./esm.mjs","require":"./cjs.js"}}`,
		"node_modules/dual/esm.mjs":      `export default "dual-esm";`,
		"node_modules/dual/cjs.js":       `module.exports = "dual-cjs";`,
	} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	src := `globalThis.probe = {
  // require() takes a dual package's "require" build (Express 5 requires
  // is-promise this way), import its "import" build.
  required: require("dual"),
  imported: () => import("dual"),
  here: import.meta.url,
  image: () => import("sharp"),
  optional: () => import("not-installed-pkg"),
};
`
	file := filepath.Join(dir, "dist", "server", "entry.mjs")
	if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	code, err := bundleProgram(dir, file, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"file:///dist/server/entry.mjs"`,
		`og:sharp`,
		`og: not-installed-pkg is not part of the packaged app`,
		`"dual-cjs"`,
		`"dual-esm"`,
	} {
		if !strings.Contains(code, want) {
			t.Errorf("bundle lacks %s", want)

		}
	}
}

// TestBundleServerConditions: in a server environment's bundle, require()
// takes a dual package's "require" build and import its "import" build.
func TestBundleServerConditions(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{
		"node_modules/dual/package.json": `{"name":"dual","exports":{"import":"./esm.mjs","require":"./cjs.js"}}`,
		"node_modules/dual/esm.mjs":      `export default "dual-esm";`,
		"node_modules/dual/cjs.js":       `module.exports = "dual-cjs";`,
		"dist/server/entry.js":           "import d from \"dual\";\nexport default { fetch: () => new Response(require(\"dual\") + \"|\" + d) };\n",
	} {
		_ = os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755)
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	code, err := bundleServer(dir, filepath.Join(dir, "dist/server/entry.js"), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"dual-cjs"`, `"dual-esm"`} {
		if !strings.Contains(code, want) {
			t.Errorf("bundle lacks %s", want)
		}
	}
}
