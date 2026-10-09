package jshost

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/grafana/sobek"
	"github.com/moriyoshi/oganesson/internal/wasm"
)

const testConfig = `
import { transformSync } from "esbuild";

let starts = 0;
const custom = {
  name: "custom",
  setup(build) {
    build.onStart(() => { starts++; });
    build.onResolve({ filter: /^virtual:info$/ }, () => ({ path: "info", namespace: "virtual", pluginData: { answer: 42 } }));
    build.onLoad({ filter: /.*/, namespace: "virtual" }, async (args) => {
      // build.resolve calls back into the Go resolver (and other plugins).
      const r = await build.resolve("./data.txt", { resolveDir: build.initialOptions.absWorkingDir, kind: "import-statement" });
      return {
        contents: "export const answer = " + args.pluginData.answer + "; export const dataPath = " + JSON.stringify(r.path) + "; export const starts = " + starts + ";",
        loader: "js",
      };
    });
    build.onLoad({ filter: /\.txt$/ }, async (args) => {
      const ts = transformSync("const x: number = 1; export default x", { loader: "ts" }).code;
      return { contents: "shouted " + require("fs").readFileSync(args.path, "utf8").toUpperCase() + " " + (ts.includes(": number") ? "ts-kept" : "ts-stripped"), loader: "text" };
    });
    build.onEnd((result) => { globalThis.__ended = result.errors.length; });
  },
};
export default { esbuild: { plugins: [custom] } };
`

func TestPluginBridge(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("og.config.mjs", testConfig)
	write("data.txt", "hello")
	write("entry.js", `import { answer, dataPath, starts } from "virtual:info"; import text from "./data.txt"; globalThis.result = [answer, dataPath.endsWith("data.txt"), starts, text].join("|");`)

	h := &Host{Dir: dir, Out: io.Discard}
	ph, err := h.StartPlugins(FindConfig(dir), HostOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer ph.Close()
	plugins, err := ph.Plugins(BridgeOptions{Kinds: []string{"all"}, InitialOptions: map[string]any{"absWorkingDir": dir}})
	if err != nil {
		t.Fatal(err)
	}
	res := api.Build(api.BuildOptions{
		EntryPoints: []string{filepath.Join(dir, "entry.js")}, Bundle: true, Write: false, AbsWorkingDir: dir,
		Plugins: plugins, LogLevel: api.LogLevelSilent,
	})
	if len(res.Errors) > 0 {
		t.Fatalf("build: %s", res.Errors[0].Text)
	}
	vm := sobek.New()
	if _, err := vm.RunString(string(res.OutputFiles[0].Contents)); err != nil {
		t.Fatal(err)
	}
	if got, want := vm.Get("result").String(), "42|true|1|shouted HELLO ts-stripped"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// Plugin errors surface as build errors.
	write("bad.js", `import "virtual:info"; import "./missing.txt";`)
	plugins, _ = ph.Plugins(BridgeOptions{Kinds: []string{"all"}, InitialOptions: map[string]any{"absWorkingDir": dir}})
	res = api.Build(api.BuildOptions{EntryPoints: []string{filepath.Join(dir, "bad.js")}, Bundle: true, Write: false, AbsWorkingDir: dir, Plugins: plugins, LogLevel: api.LogLevelSilent})
	if len(res.Errors) == 0 || !strings.Contains(res.Errors[0].Text, "missing.txt") {
		t.Errorf("expected an error about missing.txt, got %v", res.Errors)
	}
}

// TestEsbuildPluginPostcss runs the real esbuild-plugin-postcss package
// (LESS + PostCSS + postcss-modules) through the bridge.
func TestEsbuildPluginPostcss(t *testing.T) {
	dir, _ := filepath.Abs("../../testdata/plugins/postcss")
	if _, err := os.Stat(filepath.Join(dir, "node_modules", "esbuild-plugin-postcss")); err != nil {
		t.Skip("run: go run ./cmd/og install testdata/plugins/postcss")
	}
	h := &Host{Dir: dir, Out: io.Discard}
	ph, err := h.StartPlugins(FindConfig(dir), HostOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer ph.Close()
	plugins, err := ph.Plugins(BridgeOptions{Kinds: []string{"all", "client"}, InitialOptions: map[string]any{"absWorkingDir": dir}})
	if err != nil {
		t.Fatal(err)
	}
	res := api.Build(api.BuildOptions{
		EntryPoints: []string{filepath.Join(dir, "entry.js")}, Bundle: true, Write: false, AbsWorkingDir: dir,
		Plugins: plugins, LogLevel: api.LogLevelSilent,
	})
	if len(res.Errors) > 0 {
		t.Fatalf("build: %s", res.Errors[0].Text)
	}
	out := string(res.OutputFiles[0].Contents)
	// The plugin injects the compiled CSS at runtime and exports the
	// postcss-modules class map.
	for _, want := range []string{"border: 1px solid #0070f3", "document.createElement(\"style\")"} {
		if !strings.Contains(out, want) {
			t.Errorf("bundle lacks %q:\n%s", want, out)
		}
	}
	vm := sobek.New()
	_, _ = vm.RunString(`globalThis.document = { body: { appendChild() {} }, getElementById() { return null; }, createElement() { return {}; } };`)
	if _, err := vm.RunString(out); err != nil {
		t.Fatal(err)
	}
	cls := vm.Get("result").ToObject(vm)
	if c := cls.Get("card"); c == nil || !strings.HasPrefix(c.String(), "_card_") {
		t.Errorf("class map: card = %v", c)
	}
	// declaration: true makes the plugin write a .d.ts next to the source.
	if b, err := os.ReadFile(filepath.Join(dir, "card.module.less.d.ts")); err != nil || !strings.Contains(string(b), `"card"`) {
		t.Errorf("declaration file: %v %q", err, b)
	}
}

func TestDynamicRequire(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) {
		p := filepath.Join(dir, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("node_modules/exp/package.json", `{"name":"exp","exports":{".":{"import":"./esm.mjs","require":"./cjs.cjs"},"./sub":"./sub/index.ts"}}`)
	write("node_modules/exp/cjs.cjs", `module.exports = { kind: "cjs" };`)
	write("node_modules/exp/esm.mjs", `export const kind = "esm";`)
	write("node_modules/exp/sub/index.ts", `const n: number = 2; export default n;`)
	// A dependency resolving its own nested dependency: require.resolve
	// must start from the calling module, not the project.
	write("node_modules/dep/index.js", `module.exports = require.resolve("inner");`)
	write("node_modules/dep/node_modules/inner/index.js", `module.exports = 1;`)
	write("lib/data.json", `{"v": 1}`)
	write("lib/esm-only.mjs", `import path from "node:path"; export const base = path.basename(new URL(import.meta.url).pathname);`)
	h := &Host{Dir: dir, Out: io.Discard}
	code, err := h.Bundle("", `
		const r = require("module").createRequire(__dirname + "/lib/x.js");
		globalThis.result = [
			require("exp").kind,
			require("exp/sub").default,
			r("./data.json").v,
			r("./esm-only.mjs").base,
			require("node:path").join("a", "b"),
			(() => { try { require("missing-pkg"); return "no"; } catch (e) { return e.code; } })(),
			// (a computed id, so esbuild leaves the require to run time)
			require(["dep"][0]).endsWith("/node_modules/dep/node_modules/inner/index.js"),
			// import() of a file: URL (lowered to require), as Node takes it
			require(["file://" + __dirname + "/lib/esm-only.mjs"][0]).base,
		].join("|");
	`, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.Eval(context.Background(), code, "result")
	if err != nil {
		t.Fatal(err)
	}
	if want := "cjs|2|1|esm-only.mjs|a/b|MODULE_NOT_FOUND|true|esm-only.mjs"; v != want {
		t.Errorf("got %v, want %s", v, want)
	}
}

// TestPostCSSDefaults covers Next's default PostCSS pipeline (no
// postcss.config.*): postcss-preset-env's autoprefixer runs for the
// project's browserslist, falling back to Next's modern target.
func TestPostCSSDefaults(t *testing.T) {
	nextDir, _ := filepath.Abs("../../testdata/apps/next-hello")
	if _, err := os.Stat(filepath.Join(nextDir, "node_modules/next")); err != nil {
		t.Skip("run: go run ./cmd/og install testdata/apps/next-hello")
	}
	process := func(root string) string {
		t.Helper()
		h := &Host{Dir: root, Out: io.Discard}
		ph, err := h.StartPlugins("", HostOptions{PostCSS: &PostCSSOptions{NextDir: nextDir, Root: root}})
		if err != nil {
			t.Fatal(err)
		}
		defer ph.Close()
		css, _, err := ph.PostCSS(filepath.Join(root, "a.css"), ".a{user-select:none}")
		if err != nil {
			t.Fatal(err)
		}
		return css
	}

	// Next's default target includes Safari 16.4, which needs the prefix.
	if css := process(t.TempDir()); !strings.Contains(css, "-webkit-user-select:none") {
		t.Errorf("default target: no -webkit- prefix: %s", css)
	}
	// A browserslist that needs no prefix.
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".browserslistrc"), []byte("chrome 120\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if css := process(root); strings.Contains(css, "-webkit-") || !strings.Contains(css, "user-select:none") {
		t.Errorf("browserslist chrome 120: %s", css)
	}
}

// TestPostCSSTailwind3 runs Tailwind v3 (testdata/plugins/tailwind3: its
// postcss.config.js, tailwind.config.js and content scanning) through
// Next's PostCSS setup, taken from the next-hello fixture.
func TestPostCSSTailwind3(t *testing.T) {
	nextDir, _ := filepath.Abs("../../testdata/apps/next-hello")
	root, _ := filepath.Abs("../../testdata/plugins/tailwind3")
	for _, d := range []string{nextDir, root} {
		if _, err := os.Stat(filepath.Join(d, "node_modules")); err != nil {
			t.Skip("run: go run ./cmd/og install " + d)
		}
	}
	h := &Host{Dir: root, Out: io.Discard}
	ph, err := h.StartPlugins("", HostOptions{PostCSS: &PostCSSOptions{NextDir: nextDir, Root: root}})
	if err != nil {
		t.Fatal(err)
	}
	defer ph.Close()
	css, _, err := ph.PostCSS(filepath.Join(root, "src/app.css"), "@tailwind utilities;\n.note { @apply text-brand font-bold; }")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		".underline {\n    text-decoration-line: underline\n}",
		".tracking-widest {\n    letter-spacing: 0.1em\n}",
		"color: rgb(11 114 133",
		"font-weight: 700",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("output lacks %q:\n%s", want, css)
		}
	}
	if strings.Contains(css, "@tailwind") || strings.Contains(css, ".text-center") {
		t.Errorf("unprocessed directive or unused utility:\n%s", css)
	}
}

// TestOxideIgnoreRules: Tailwind v4's scanner (WebAssembly, see
// internal/wasm) skips node_modules and .gitignore'd paths for a project
// inside a git repository (the .git is in an ancestor), as it does
// natively. (A project in a gitignored directory is scanned whole: oxide
// promotes such a base to an "external" source; that is upstream behavior.)
func TestOxideIgnoreRules(t *testing.T) {
	nextDir, _ := filepath.Abs("../../testdata/apps/next-hello")
	if _, err := os.Stat(filepath.Join(nextDir, "node_modules/@tailwindcss/oxide-wasm32-wasi")); err != nil {
		t.Skip("run: go run ./cmd/og install testdata/apps/next-hello")
	}
	repo := t.TempDir()
	app := filepath.Join(repo, "apps", "web")
	for name, body := range map[string]string{
		".git/HEAD":                    "ref: refs/heads/main\n",
		"apps/web/.gitignore":          "generated/\n",
		"apps/web/src/page.js":         `export const a = "grid";`,
		"apps/web/generated/x.js":      `export const b = "block";`,
		"apps/web/node_modules/m/i.js": `export const c = "flex";`,
	} {
		p := filepath.Join(repo, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := &Host{Dir: nextDir, Out: io.Discard}
	code, err := h.Bundle("", fmt.Sprintf(`
		const { Scanner } = require(["@tailwindcss/oxide"][0]);
		const s = new Scanner({ sources: [{ base: %s, pattern: "**/*", negated: false }] });
		const candidates = s.scan();
		globalThis.result = { files: s.files.map((f) => f.split("/apps/web/")[1]).sort(), grid: candidates.includes("grid"), flex: candidates.includes("flex") };
	`, jsonString(app)), "")
	if err != nil {
		t.Fatal(err)
	}
	timeouts := wasm.CloseTimeouts()
	v, err := h.Eval(context.Background(), code, "result")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprint(v), "map[files:[src/page.js] flex:false grid:true]"; got != want {
		t.Errorf("got %s, want %s", got, want)
	}
	// Oxide's idle worker threads park in memory.atomic.wait; closing the
	// engine must still stop them (see wasm.Runtime.Close).
	if n := wasm.CloseTimeouts() - timeouts; n != 0 {
		t.Errorf("%d engine close(s) timed out waiting for wasm threads", n)
	}
}

// TestPluginHostEsbuildBuildSync: in the build host, the esbuild package
// (og's substitute, loaded at run time as a PostCSS plugin loads it) has
// the whole API, buildSync included (Panda CSS's PostCSS plugin bundles its
// config with it); the plugin bridge's own esbuild object lives apart.
func TestPluginHostEsbuildBuildSync(t *testing.T) {
	dir := t.TempDir()
	for name, src := range map[string]string{
		"node_modules/esbuild/package.json": `{"name":"esbuild","version":"0.25.0","main":"lib/main.js"}`,
		"node_modules/esbuild/lib/main.js":  `throw new Error("the native esbuild package");`,
		"og.config.mjs": `export default { esbuild: { plugins: [{
  name: "uses-esbuild",
  setup(build) {
    build.onLoad({ filter: /\.txt$/ }, () => {
      const esb = globalThis[Symbol.for("og.createRequire")](build.initialOptions.absWorkingDir + "/x.js")("esbuild");
      const out = esb.buildSync({ stdin: { contents: "export default 40 + 2", loader: "js" }, write: false, minifySyntax: true, format: "esm" }).outputFiles[0].text;
      return { contents: out.includes("42") ? "built" : out, loader: "text" };
    });
  },
}] } };`,
		"data.txt": "x",
		"entry.js": `import text from "./data.txt"; globalThis.result = text;`,
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := &Host{Dir: dir, Out: io.Discard}
	ph, err := h.StartPlugins(FindConfig(dir), HostOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer ph.Close()
	plugins, err := ph.Plugins(BridgeOptions{Kinds: []string{"all"}, InitialOptions: map[string]any{"absWorkingDir": dir}})
	if err != nil {
		t.Fatal(err)
	}
	res := api.Build(api.BuildOptions{EntryPoints: []string{filepath.Join(dir, "entry.js")}, Bundle: true, Write: false, AbsWorkingDir: dir, Plugins: plugins, LogLevel: api.LogLevelSilent})
	if len(res.Errors) > 0 {
		t.Fatalf("build: %s", res.Errors[0].Text)
	}
	vm := sobek.New()
	if _, err := vm.RunString(string(res.OutputFiles[0].Contents)); err != nil {
		t.Fatal(err)
	}
	if got := vm.Get("result").String(); got != "built" {
		t.Errorf("got %q", got)
	}
}
