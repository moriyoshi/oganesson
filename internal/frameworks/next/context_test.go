package next

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestExpandComputedImports: computed relative requires and imports become
// maps of the literal requests matching their pattern (dynamic parts across
// directories, extensions optional), other calls stay, and a pattern with
// no match is left alone.
func TestExpandComputedImports(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"c/a.tsx", "c/b.tsx", "c/sub/d.tsx", "c/e.css", "c/node_modules/x.tsx", "locales/en.json", "locales/fr.json", "src/m.ts"} {
		p := filepath.Join(dir, filepath.FromSlash(f))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, nil, 0o644)
	}
	if got, want := contextKeys(filepath.Join(dir, "src"), []string{"../c/", ".tsx"}), []string{"../c/a.tsx", "../c/b.tsx", "../c/sub/d.tsx"}; !reflect.DeepEqual(got, want) {
		t.Errorf("keys %q, want %q", got, want)
	}
	if got, want := contextKeys(filepath.Join(dir, "src"), []string{"../locales/", ""}), []string{"../locales/en", "../locales/en.json", "../locales/fr", "../locales/fr.json"}; !reflect.DeepEqual(got, want) {
		t.Errorf("keys %q, want %q", got, want)
	}
	src := "const A = require(`../c/${name}.tsx`).default;\n" +
		"const l = await import(\"../locales/\" + lang + \".json\");\n" +
		"const x = require(\"./static\");\n" +
		"const y = require(`../none/${z}.js`);\n"
	got := expandComputedImports(filepath.Join(dir, "src", "m.ts"), src)
	for _, want := range []string{
		"const A = __og_context({\"../c/a.tsx\": () => require(\"../c/a.tsx\"), \"../c/b.tsx\": () => require(\"../c/b.tsx\"), \"../c/sub/d.tsx\": () => require(\"../c/sub/d.tsx\")}, `../c/${name}.tsx`, false).default;",
		"const l = await __og_context({\"../locales/en.json\": () => import(\"../locales/en.json\"), \"../locales/fr.json\": () => import(\"../locales/fr.json\")}, \"../locales/\" + lang + \".json\", true);",
		"const x = require(\"./static\");",
		"const y = require(`../none/${z}.js`);",
		"function __og_context(m, k, async) {",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %s\n%s", want, got)
		}
	}
	if plain := "const x = require(\"./a\");"; expandComputedImports(filepath.Join(dir, "src", "m.ts"), plain) != plain {
		t.Error("a module without computed imports changed")
	}
}

// TestCommonJSExports: the export names cjs-module-lexer finds, followed
// through reexports.
func TestCommonJSExports(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"a.js": `"use strict";
Object.defineProperty(exports, "__esModule", { value: true });
exports.b = exports.a = void 0;
exports.a = 1;
module.exports.c = 2;
exports["d-e"] = 3;
Object.defineProperty(exports, "f", { enumerable: true, get: function () { return 4; } });
tslib_1.__exportStar(require("./more"), exports);
exports.default = 5;
`,
		"more.js": `module.exports = { g: 1, h() {}, "i": 2, ...require("./most") };`,
		"most.js": `exports.j = 1; exports.default = 2;`,
	}
	for name, src := range files {
		_ = os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644)
	}
	got := commonJSExports(filepath.Join(dir, "a.js"), 0)
	want := []string{"a", "b", "c", "d-e", "default", "f", "g", "h", "i", "j"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestRewriteMissingImports: a recorded missing import is read from the
// module's namespace; the import's other bindings (default, named,
// namespace) stay imports.
func TestRewriteMissingImports(t *testing.T) {
	b := &builder{}
	b.missing.names = map[string]map[string]bool{"/p/a.js": {"gone": true, "lost": true}}
	src := "import def, { kept, gone as g } from \"m\";\nimport * as ns from \"n\";\nimport { lost } from \"o\";\nimport { other } from \"q\";\nuse(def, kept, g, ns, lost, other);\n"
	got := b.rewriteMissingImports("/p/a.js", src)
	for _, want := range []string{
		`import def, { "kept" as kept } from "m"; import * as __og_mx0 from "m"; var g = __og_mx0["gone"];`,
		`import * as ns from "n";`,
		`import * as __og_mx1 from "o"; var lost = __og_mx1["lost"];`,
		`import { other } from "q";`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output lacks %s\n%s", want, got)
		}
	}
	if b.rewriteMissingImports("/p/other.js", src) != src {
		t.Error("a module without missing imports changed")
	}
}

// TestFindBin: a script command's program is the bin an installed package
// declares: the package of that name (a string bin) or any top-level one
// naming it in its bin map (@graphql-codegen/cli's graphql-codegen).
func TestFindBin(t *testing.T) {
	dir := t.TempDir()
	for name, src := range map[string]string{
		"node_modules/solo/package.json":       `{"name":"solo","bin":"cli.js"}`,
		"node_modules/@scope/cli/package.json": `{"name":"@scope/cli","bin":{"gen":"bin/gen.js","gen-alias":"bin/gen.js"}}`,
		"node_modules/other/package.json":      `{"name":"other","description":"mentions \"gen\" but has no bin"}`,
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(src), 0o644)
	}
	for name, want := range map[string]string{
		"solo":    "node_modules/solo/cli.js",
		"gen":     "node_modules/@scope/cli/bin/gen.js",
		"missing": "",
	} {
		got, ok := findBin(dir, name)
		if ok {
			got, _ = filepath.Rel(dir, got)
		}
		if filepath.ToSlash(got) != want {
			t.Errorf("findBin(%s) = %q, want %q", name, got, want)
		}
	}
}
