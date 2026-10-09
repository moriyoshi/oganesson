package vite

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildVitePress(t *testing.T) {
	out, err := Build(context.Background(), fixture(t, "vitepress-basic"), Options{Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	if out.AppType != "mpa" || out.Base != "/" {
		t.Errorf("app type = %q, base = %q", out.AppType, out.Base)
	}
	for name, want := range map[string]string{
		"index.html":  "VitePress on Oganesson",
		"guide.html":  "This page was rendered by VitePress on Oganesson.",
		"fixture.txt": "vitepress-public-asset",
	} {
		if got := string(out.Client[name]); !strings.Contains(got, want) {
			t.Errorf("%s lacks %q:\n%s", name, want, got)
		}
	}
}

func TestDetectVitePress(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{
  "devDependencies": { "vitepress": "2.0.0-alpha.20" }
}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !DetectVitePress(dir) {
		t.Fatal("DetectVitePress returned false for a declared vitepress dependency")
	}
	if !Detect(dir) {
		t.Fatal("Detect returned false for a VitePress project")
	}
}

// VitePress gives its codeSplitting group-name function the Rolldown chunk
// context and calls getModuleInfo while choosing the theme chunk name.
func TestFacadeAdvancedChunkNameContext(t *testing.T) {
	res := runFacade(t, `
import { rolldown } from "rolldown";
globalThis.__ogResult = (async () => {
  const plugin = {
    name: "modules",
    resolveId: (id) => (["entry", "lazy"].includes(id) ? "\0" + id : null),
    load: (id) => id === "\0entry" ? "import('lazy')" : id === "\0lazy" ? "export default 42" : null,
  };
  const bundle = await rolldown({ input: "entry", plugins: [plugin] });
  const { output } = await bundle.generate({
    format: "es",
    entryFileNames: "[name].js",
    chunkFileNames: "[name].js",
    codeSplitting: { groups: [{
      test: (id, ctx) => id === "\0lazy" && typeof ctx.getModuleInfo(id) === "object",
      name: (id, ctx) => ctx.getModuleInfo(id) ? "theme" : "missing-context",
    }] },
  });
  return { files: output.map((item) => item.fileName).sort().join(" ") };
})();
`)
	if res["files"] != "entry.js theme.js" {
		t.Fatalf("files = %q, want entry.js theme.js", res["files"])
	}
}

// The built-in Vite resolver runs before ordinary framework plugins. An
// unresolved bare-looking virtual ID must continue through the chain so a
// plugin such as VitePress's @siteData resolver can claim it.
func TestFacadeVirtualBareIDAfterBuiltinResolver(t *testing.T) {
	res := runFacade(t, `
import { rolldown } from "rolldown";
globalThis.__ogResult = (async () => {
  const bundle = await rolldown({
    input: "@siteData",
    platform: "node",
    plugins: [
      { name: "builtin:vite-resolve", _options: { environmentConsumer: "server", resolveOptions: { root: process.cwd() } } },
      {
        name: "vitepress-data",
        resolveId: (id) => id === "@siteData" ? "\0site-data" : null,
        load: (id) => id === "\0site-data" ? "export default 'resolved-site-data'" : null,
      },
    ],
  });
  const { output } = await bundle.generate({ format: "es" });
  return { code: output[0].code };
})();
`)
	code, _ := res["code"].(string)
	if !strings.Contains(code, "resolved-site-data") {
		t.Fatalf("virtual module was not bundled:\n%s", code)
	}
}
