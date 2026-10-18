package jshost

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestAsyncDynamicImportTLA covers a module loaded through the runtime require
// (moduleSource) that dynamically imports a module which awaits at its top
// level, as nextra's webpack loader does (nextra/loader.cjs awaits
// import("./dist/server/loader.js"), and that module awaits at its top level).
// esbuild lowers import() to a synchronous require() that cannot load such a
// module, failing the Goja target with `Top-level await is not available in
// the configured target environment`; og.import loads it asynchronously
// instead (require.go, jshost.go). Both a literal and a computed specifier,
// and the non-top-level-await path, run through og.import.
func TestAsyncDynamicImportTLA(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) {
		p := filepath.Join(dir, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A genuine top-level await at module scope.
	write("lib/tla.mjs", `export const value = await Promise.resolve("awaited");
export default await Promise.resolve(42);`)
	// A plain ES module, dynamically imported through the non-await path.
	write("lib/plain.mjs", `export const plain = "plain-value"; export default "plain-default";`)
	// Loaded at run time (a computed require forces moduleSource), awaiting a
	// literal and a computed dynamic import.
	write("lib/consumer.cjs", `module.exports = (async () => {
  const tla = await import("./tla.mjs");
  const spec = "./plain.mjs";
  const plain = await import(spec);
  return [tla.value, tla.default, plain.plain, plain.default].join("|");
})();`)

	h := &Host{Dir: dir, Out: io.Discard}
	code, err := h.Bundle("", `
		const load = (id) => require([id][0]);
		globalThis.result = load(__dirname + "/lib/consumer.cjs");
	`, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.Eval(context.Background(), code, "result")
	if err != nil {
		t.Fatal(err)
	}
	if want := "awaited|42|plain-value|plain-default"; v != want {
		t.Fatalf("got %q, want %q", v, want)
	}
}

// TestModuleHasTLA checks the detection that routes a module to the async
// import path: only a module awaiting at its top level qualifies.
func TestModuleHasTLA(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	tla := write("tla.mjs", `export const x = await Promise.resolve(1);`)
	// await inside an async function is not top-level await.
	nested := write("nested.mjs", `export async function f() { return await Promise.resolve(1); }`)
	plain := write("plain.cjs", `module.exports = 1;`)
	for _, tc := range []struct {
		path string
		want bool
	}{{tla, true}, {nested, false}, {plain, false}} {
		if got := moduleHasTLA(tc.path, nil); got != tc.want {
			t.Errorf("moduleHasTLA(%s) = %v, want %v", filepath.Base(tc.path), got, tc.want)
		}
	}
}
