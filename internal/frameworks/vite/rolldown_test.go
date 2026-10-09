package vite

import (
	"context"
	"github.com/evanw/esbuild/pkg/api"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/moriyoshi/oganesson/internal/jshost"
	"github.com/moriyoshi/oganesson/runtime/engine"
)

// runFacade runs entry (which sets globalThis.__ogResult) on the build host
// with the Rolldown facade, from a fixture with Vite (and so Rolldown)
// installed.
func runFacade(t *testing.T, entry string) map[string]any {
	t.Helper()
	dir := fixture(t, "vite-react")
	host := &jshost.Host{Dir: dir, Plugins: hostPlugins()}
	code, err := host.Bundle("", entry, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := host.EvalWith(context.Background(), code, "__ogResult", func(e *engine.Engine) error { return installRolldown(e, nil) })
	if err != nil {
		t.Fatal(err)
	}
	res, _ := v.(map[string]any)
	return res
}

// TestFacadeResolveSkipChain: this.resolve skips the calling plugin for
// the rest of the chain (Rollup's skipSelf), so two plugins that resolve
// through each other (plugin-rsc's and TanStack Start's do) reach the
// next plugin instead of recursing forever.
func TestFacadeResolveSkipChain(t *testing.T) {
	res := runFacade(t, `
import { rolldown } from "rolldown";
globalThis.__ogResult = (async () => {
  const calls = [];
  const through = (name) => ({
    name,
    async resolveId(source, importer) {
      if (source !== "virtual") return null;
      calls.push(name);
      if (calls.length > 20) throw new Error("resolve loop: " + calls.join(","));
      return await this.resolve(source, importer);
    },
  });
  const target = {
    name: "target",
    resolveId: (s) => (s === "virtual" ? "\0virtual" : null),
    load: (id) => (id === "\0virtual" ? "export default 42;" : null),
  };
  const bundle = await rolldown({ input: "virtual", plugins: [through("a"), through("b"), target] });
  const { output } = await bundle.generate({ format: "es" });
  return { calls: calls.join(","), code: output[0].code };
})();
`)
	if res["calls"] != "a,b" {
		t.Errorf("resolveId calls %v", res["calls"])
	}
}

// TestFacadeRenderedExports: chunk.modules[id].renderedExports lists the
// module's exports (plugin-rsc builds client reference proxies from them),
// following export * (without default; through a cycle; into a module
// left in TypeScript).
func TestFacadeRenderedExports(t *testing.T) {
	res := runFacade(t, `
import { rolldown } from "rolldown";
globalThis.__ogResult = (async () => {
  let exports;
  const plugin = {
    name: "mod",
    // dep3 has a file path id (esbuild's file namespace), the others are virtual.
    resolveId: (s) => (["mod", "dep", "dep2", "tsmod"].includes(s) ? "\0" + s : s === "dep3" ? "/og-facade-test/dep3.js" : null),
    load: (id) => ({
      "\0mod": "export const x = 1, { y } = { y: 2 }, [w] = [3]; export function f() {} export class C {} export default 1; const q = 4; export { q as z }; export * from 'dep'; export * as ns from 'dep2';",
      "\0dep": "export const a = 1; export default 2; export * from 'dep2';",
      "\0dep2": "export const c = 3; export * from 'dep'; export * from 'dep3';",
      "/og-facade-test/dep3.js": "export const d = 4; export * from 'dep2'; export * from 'tsmod';",
      // Left in TypeScript by its loader (as plugin-vue's <script lang="ts">).
      "\0tsmod": { code: "export const t: number = 5; export type T = string;", moduleType: "ts" },
    })[id] ?? null,
    generateBundle(_, bundle) {
      for (const c of Object.values(bundle)) if (c.type === "chunk" && c.modules["\0mod"]) exports = c.modules["\0mod"].renderedExports;
    },
  };
  const bundle = await rolldown({ input: "mod", plugins: [plugin] });
  await bundle.generate({ format: "es" });
  return { exports: (exports || []).slice().sort().join(",") };
})();
`)
	if res["exports"] != "C,a,c,d,default,f,ns,t,w,x,y,z" {
		t.Errorf("renderedExports %v", res["exports"])
	}
}

// TestParseModuleRecord: rolldown/utils' parseSync returns Oxc's module
// record (Nuxt checks a plugin's default export in staticExports).
func TestParseModuleRecord(t *testing.T) {
	res := runFacade(t, `
import { parseSync } from "rolldown/utils";
const code = 'import d, { a as b } from "m"; import * as ns from "n";\n' +
  'export const x = 1, { y } = {}; export { b as c }; export { z } from "o"; export * from "p"; export * as q from "r";\n' +
  'export default function f() { return import("lazy"), import.meta.url; }';
const m = parseSync("x.js", code).module;
const span = (s) => code.slice(s.start, s.end);
globalThis.__ogResult = {
  imports: m.staticImports.map((i) => i.moduleRequest.value + ":" + i.entries.map((e) => e.importName.kind + "/" + (e.importName.name ?? "") + "/" + e.localName.value).join(",")).join(" "),
  exports: m.staticExports.flatMap((s) => s.entries.map((e) => e.exportName.kind + "/" + (e.exportName.name ?? "") + "<" + e.importName.kind + (e.moduleRequest ? "@" + e.moduleRequest.value : "") + "|" + e.localName.kind + "/" + (e.localName.name ?? ""))).join(" "),
  dynamic: m.dynamicImports.map((d) => span(d.moduleRequest)).join(","),
  metas: m.importMetas.map(span).join(","),
  hasModuleSyntax: m.hasModuleSyntax,
  json: JSON.stringify([m.staticExports, m.staticImports[0], m.dynamicImports, m.importMetas]),
};
`)
	want := map[string]any{
		"imports": `m:Default//d,Name/a/b n:NamespaceObject//ns`,
		// as oxc-parser reports them (the indirect export under its import)
		"exports":         `Name/c<Name@m|None/ Name/x<None|Name/x Name/y<None|Name/y Name/z<Name@o|None/ None/<AllButDefault@p|None/ Name/q<All@r|None/ Default/<None|Name/f`,
		"dynamic":         `"lazy"`,
		"metas":           `import.meta`,
		"hasModuleSyntax": true,
		// field for field, from oxc-parser
		"json": oxcRecordJSON,
	}
	for k, v := range want {
		if res[k] != v {
			t.Errorf("%s: got %v\nwant %v", k, res[k], v)
		}
	}
}

// oxcRecordJSON is oxc-parser's record of TestParseModuleRecord's code.
const oxcRecordJSON = `[[{"start":0,"end":30,"entries":[{"start":97,"end":103,"moduleRequest":{"value":"m","start":26,"end":29},"importName":{"kind":"Name","name":"a","start":12,"end":13},"exportName":{"kind":"Name","name":"c","start":102,"end":103},"localName":{"kind":"None","name":null,"start":null,"end":null},"isType":false}]},{"start":56,"end":87,"entries":[{"start":63,"end":87,"moduleRequest":null,"importName":{"kind":"None","name":null,"start":null,"end":null},"exportName":{"kind":"Name","name":"x","start":69,"end":70},"localName":{"kind":"Name","name":"x","start":69,"end":70},"isType":false},{"start":63,"end":87,"moduleRequest":null,"importName":{"kind":"None","name":null,"start":null,"end":null},"exportName":{"kind":"Name","name":"y","start":78,"end":79},"localName":{"kind":"Name","name":"y","start":78,"end":79},"isType":false}]},{"start":107,"end":129,"entries":[{"start":116,"end":117,"moduleRequest":{"value":"o","start":125,"end":128},"importName":{"kind":"Name","name":"z","start":116,"end":117},"exportName":{"kind":"Name","name":"z","start":116,"end":117},"localName":{"kind":"None","name":null,"start":null,"end":null},"isType":false}]},{"start":130,"end":148,"entries":[{"start":130,"end":148,"moduleRequest":{"value":"p","start":144,"end":147},"importName":{"kind":"AllButDefault","name":null,"start":null,"end":null},"exportName":{"kind":"None","name":null,"start":null,"end":null},"localName":{"kind":"None","name":null,"start":null,"end":null},"isType":false}]},{"start":149,"end":172,"entries":[{"start":149,"end":172,"moduleRequest":{"value":"r","start":168,"end":171},"importName":{"kind":"All","name":null,"start":null,"end":null},"exportName":{"kind":"Name","name":"q","start":161,"end":162},"localName":{"kind":"None","name":null,"start":null,"end":null},"isType":false}]},{"start":173,"end":244,"entries":[{"start":188,"end":244,"moduleRequest":null,"importName":{"kind":"None","name":null,"start":null,"end":null},"exportName":{"kind":"Default","name":null,"start":180,"end":187},"localName":{"kind":"Name","name":"f","start":197,"end":198},"isType":false}]}],{"start":0,"end":30,"moduleRequest":{"value":"m","start":26,"end":29},"entries":[{"importName":{"kind":"Default","name":null,"start":7,"end":8},"localName":{"value":"d","start":7,"end":8},"isType":false},{"importName":{"kind":"Name","name":"a","start":12,"end":13},"localName":{"value":"b","start":17,"end":18},"isType":false}]},[{"start":210,"end":224,"moduleRequest":{"start":217,"end":223}}],[{"start":226,"end":237}]]`

// TestFacadeChunkEmittedDuringBuild: a chunk a plugin emits from transform
// (Nuxt's SSR styles) is built too, and its file name is known by
// generateBundle.
func TestFacadeChunkEmittedDuringBuild(t *testing.T) {
	res := runFacade(t, `
import { rolldown } from "rolldown";
globalThis.__ogResult = (async () => {
  let ref, name;
  const plugin = {
    name: "late",
    resolveId: (s) => (s === "main" || s === "late" ? "\0" + s : null),
    load: (id) => (id === "\0main" ? "export default 1;" : id === "\0late" ? "export default 'late chunk';" : null),
    transform(code, id) {
      if (id === "\0main") ref = this.emitFile({ type: "chunk", id: "late", name: "late" });
    },
    generateBundle() { name = this.getFileName(ref); },
  };
  const bundle = await rolldown({ input: "main", plugins: [plugin], platform: "node" });
  const { output } = await bundle.generate({ format: "es", entryFileNames: "[name].js" });
  return { name, files: output.map((o) => o.fileName).sort().join(","), late: output.find((o) => o.fileName === name)?.code.includes("late chunk") };
})();
`)
	if res["name"] != "late.js" || res["files"] != "late.js,main.js" || res["late"] != true {
		t.Errorf("got %v", res)
	}
}

// TestGlobalDefines: object- and array-valued defines become entity names
// on globalThis set by each chunk's banner (esbuild's renamer races on its
// shared define values in split, unminified builds); others stay defines.
func TestGlobalDefines(t *testing.T) {
	define, stmts := globalDefines(map[string]string{
		"process.env":          "{}",
		"import.meta.env":      `{"MODE":"production"}`,
		"__LIST__":             "[1, 2]",
		"process.env.NODE_ENV": `"production"`,
		"__DEV__":              "false",
	})
	banner := joinStmts(stmts)
	if define["process.env.NODE_ENV"] != `"production"` || define["__DEV__"] != "false" {
		t.Errorf("scalar defines changed: %v", define)
	}
	for _, k := range []string{"process.env", "import.meta.env", "__LIST__"} {
		name := define[k]
		if !strings.HasPrefix(name, "globalThis.__og_define_") || !strings.Contains(banner, name+" = "+name+" || ") {
			t.Errorf("%s: define %q, banner %s", k, name, banner)
		}
	}
	if !strings.Contains(banner, `|| {"MODE":"production"};`) || !strings.Contains(banner, "|| [1, 2];") {
		t.Errorf("banner %s", banner)
	}
}

// TestTrimDefineBanners: each chunk keeps the banner statements for the
// define names it references (__og_define_1 is not __og_define_13); a chunk
// that references none loses the line, and its source map the line's
// (empty) mappings.
func TestTrimDefineBanners(t *testing.T) {
	stmts := []defineStmt{
		{"globalThis.__og_define_1", "globalThis.__og_define_1 = globalThis.__og_define_1 || {};"},
		{"globalThis.__og_define_13", "globalThis.__og_define_13 = globalThis.__og_define_13 || {\"A\":1};"},
	}
	banner := joinStmts(stmts) + "\n"
	res := api.BuildResult{OutputFiles: []api.OutputFile{
		{Path: "/o/uses13.js", Contents: []byte(banner + "console.log(globalThis.__og_define_13.A);\n")},
		{Path: "/o/none.js", Contents: []byte(banner + "console.log(1);\n//# sourceMappingURL=none.js.map\n")},
		{Path: "/o/none.js.map", Contents: []byte(`{"version":3,"sources":["a.js"],"mappings":";AAAA"}`)},
		{Path: "/o/bang.mjs", Contents: []byte("#!/usr/bin/env node\n" + banner + "f(globalThis.__og_define_1);\n")},
		{Path: "/o/style.css", Contents: []byte(banner)},
	}}
	trimDefineBanners(&res, stmts)
	want := []string{
		stmts[1].js + "\nconsole.log(globalThis.__og_define_13.A);\n",
		"console.log(1);\n//# sourceMappingURL=none.js.map\n",
		`{"mappings":"AAAA","sources":["a.js"],"version":3}`,
		"#!/usr/bin/env node\n" + stmts[0].js + "\nf(globalThis.__og_define_1);\n",
		banner,
	}
	for i, w := range want {
		if got := string(res.OutputFiles[i].Contents); got != w {
			t.Errorf("%s:\n got %q\nwant %q", res.OutputFiles[i].Path, got, w)
		}
	}
}

// TestFacadeChunkNames: chunks esbuild names itself get Rolldown's names: a
// dynamic entry's module (index -> its directory), a shared chunk's last
// executed module, and with manualChunks the group all of a chunk's modules
// belong to. Real Rolldown 1.2.12 on the same files writes
// "Lazy.js a.js b.js lib.js pages.js", and with manualChunks
// "Lazy.js a.js b.js pages.js vendor.js".
func TestFacadeChunkNames(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("x", 12000)
	for name, src := range map[string]string{
		"util.js":        `export const util = (x) => x * 2;`,
		"lib/index.js":   `import { util } from "../util.js"; export const lib = (x) => util(x) + 1;`,
		"a.js":           `import { lib } from "./lib/index.js"; console.log(lib(1)); import("./pages/index.js"); import("./Lazy.js");`,
		"b.js":           `import { lib } from "./lib/index.js"; console.log(lib(2)); import("./pages/index.js"); import("./Lazy.js");`,
		"pages/index.js": `export default "` + big + `"; console.log("page");`,
		"Lazy.js":        `export default "` + big + `"; console.log("lazy");`,
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res := runFacade(t, `
import { rolldown } from "rolldown";
const dir = `+strconv.Quote(dir)+`;
globalThis.__ogResult = (async () => {
  const names = [];
  for (const manual of [false, true]) {
    const out = { format: "es", dir: dir + "/out", entryFileNames: "[name].js", chunkFileNames: "[name].js" };
    if (manual) out.manualChunks = (id) => (id.includes("/lib/") || id.includes("util") ? "vendor" : undefined);
    const b = await rolldown({ input: { a: dir + "/a.js", b: dir + "/b.js" }, cwd: dir, logLevel: "silent" });
    const { output } = await b.generate(out);
    names.push(output.map((o) => o.fileName).sort().join(" "));
  }
  return { plain: names[0], manual: names[1] };
})();
`)
	if res["plain"] != "Lazy.js a.js b.js lib.js pages.js" || res["manual"] != "Lazy.js a.js b.js pages.js vendor.js" {
		t.Errorf("got %v", res)
	}
}

// A package's "sideEffects" (package.json) applies to the ids the facade
// resolves, as Rolldown's resolver applies it: esbuild reads it only for
// paths it resolves itself, not for a plugin's resolveId result (Vite's
// resolver is a plugin). An import of a module the field marks side-effect free, with no
// bindings used, is dropped; one the glob list keeps stays.
func TestFacadePackageSideEffects(t *testing.T) {
	dir := t.TempDir()
	for name, src := range map[string]string{
		"node_modules/pkg/package.json": `{"name": "pkg", "sideEffects": ["./keep/*.js", "*.css.js"]}`,
		"node_modules/pkg/drop.js":      `globalThis.dropped = "DROPPED";`,
		"node_modules/pkg/keep/a.js":    `globalThis.kept = "KEPT_A";`,
		"node_modules/pkg/sub/x.css.js": `globalThis.kept = "KEPT_CSS";`,
		"node_modules/all/package.json": `{"name": "all", "sideEffects": false}`,
		"node_modules/all/index.js":     `globalThis.dropped = "DROPPED_ALL";`,
		"main.js":                       `import "pkg/drop.js"; import "pkg/keep/a.js"; import "pkg/sub/x.css.js"; import "all"; console.log("main");`,
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res := runFacade(t, `
import { rolldown } from "rolldown";
const dir = `+strconv.Quote(dir)+`;
globalThis.__ogResult = (async () => {
  // (A plugin resolves the package ids, as Vite's resolver does.)
  const plugin = { name: "resolve-pkg", resolveId(s) { if (s.startsWith("pkg/")) return dir + "/node_modules/" + s; if (s === "all") return dir + "/node_modules/all/index.js"; } };
  const b = await rolldown({ input: dir + "/main.js", cwd: dir, logLevel: "silent", plugins: [plugin] });
  const { output } = await b.generate({ format: "es", dir: dir + "/out" });
  return { code: output[0].code };
})();
`)
	code, _ := res["code"].(string)
	if strings.Contains(code, "DROPPED") || !strings.Contains(code, "KEPT_A") || !strings.Contains(code, "KEPT_CSS") || !strings.Contains(code, "main") {
		t.Errorf("got\n%s", code)
	}
}

// Unused bindings of external imports are dropped, as Rolldown drops them
// (esbuild prints the import as written, and Nitro's Rollup warned "…
// imported from external module … but never used" for each): what a chunk
// uses stays, an import with nothing left stays bare (for the external's
// side effects), minified output too. The source map follows: the call
// after a multi-line import still maps to its original line.
func TestFacadePrunesExternalImports(t *testing.T) {
	dir := t.TempDir()
	src := "import {\n  Transition,\n  h,\n  onMounted as om\n} from \"vue\";\nimport Def, * as NS from \"ext2\";\nimport \"side\";\nimport { keep } from \"ext3\";\nexport const x = h(\"div\", keep);\n"
	if err := os.WriteFile(filepath.Join(dir, "a.js"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	res := runFacade(t, `
import { rolldown } from "rolldown";
import { decode } from "og:sourcemap-codec";
const dir = `+strconv.Quote(dir)+`;
globalThis.__ogResult = (async () => {
  const out = {};
  for (const minify of [false, true]) {
    const b = await rolldown({ input: dir + "/a.js", cwd: dir, logLevel: "silent", external: ["vue", "ext2", "side", "ext3"] });
    const { output } = await b.generate({ format: "es", dir: dir + "/out", minify, sourcemap: true });
    const { code, map } = output[0];
    out[minify ? "min" : "plain"] = code.split("\n").filter((l) => /^import|^export|import\{|import"/.test(l)).join("\n");
    if (!minify) {
      // The original line of the output's h("div") call.
      const lines = code.split("\n");
      const line = lines.findIndex((l) => l.includes('h("div"'));
      const col = lines[line].indexOf("h(");
      const seg = decode(map.mappings)[line].find((s) => s[0] === col);
      out.mapped = seg ? String(seg[2]) : "none";
    }
  }
  return out;
})();
`)
	plain := "import { h } from \"vue\";\nimport \"ext2\";\nimport \"side\";\nimport { keep } from \"ext3\";\nexport {"
	if res["plain"] != plain || res["mapped"] != "8" {
		t.Errorf("got\n%v\nmapped to line %v (want 8)", res["plain"], res["mapped"])
	}
	if min, _ := res["min"].(string); !strings.Contains(min, `import{h as `) || strings.Contains(min, "Transition") || !strings.Contains(min, `import"ext2";`) {
		t.Errorf("minified:\n%s", min)
	}
}
