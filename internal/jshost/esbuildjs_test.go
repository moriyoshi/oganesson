package jshost

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestEsbuildPackage: the esbuild npm package's JS API (which would spawn
// its native binary) is answered by og's own esbuild, as Astro uses it
// (transform with define, and build from stdin as an IIFE).
func TestEsbuildPackage(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) {
		p := filepath.Join(dir, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("node_modules/esbuild/package.json", `{"name":"esbuild","version":"0.0.0","main":"lib/main.js"}`)
	write("node_modules/esbuild/lib/main.js", `throw new Error("Unsupported platform");`)
	write("lib/dir.js", `export default "directive";`)
	h := &Host{Dir: dir, Out: io.Discard}
	code, err := h.Bundle("", `
		const esbuild = require("esbuild");
		globalThis.result = (async () => {
			const t = await esbuild.transform("console.log(FLAG)", { define: { FLAG: "true" }, minify: true });
			const b = await esbuild.build({
				stdin: { contents: "import d from './lib/dir.js'; self.x = d;", resolveDir: __dirname },
				absWorkingDir: __dirname, format: "iife", minify: true, bundle: true, write: false,
			});
			return [t.code.trim(), b.outputFiles.length, b.outputFiles[0].text.includes('"directive"'), typeof esbuild.transformSync].join("|");
		})();
	`, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.Eval(context.Background(), code, "result")
	if err != nil {
		t.Fatal(err)
	}
	if want := "console.log(!0);|1|true|function"; v != want {
		t.Fatalf("got %q, want %q", v, want)
	}
}
