package vite

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/moriyoshi/oganesson/internal/jshost"
	"github.com/moriyoshi/oganesson/runtime/engine"
)

// Nuxt builds through Vite (the app) and Nitro (the server): og runs what
// `nuxi build` runs (@nuxt/kit's loadNuxt, writeTypes, buildNuxt) on the
// build host with the Rolldown facade. Nitro's output (.output: the server
// and its public files) is served by the start script's program
// (`node .output/server/index.mjs`), as a server program (program.go).

var nuxtConfigNames = []string{"nuxt.config.ts", "nuxt.config.mts", "nuxt.config.js", "nuxt.config.mjs"}

// DetectNuxt reports a Nuxt project: a nuxt.config.* or a "nuxt"
// dependency.
func DetectNuxt(dir string) bool {
	for _, n := range nuxtConfigNames {
		if fileExists(filepath.Join(dir, n)) {
			return true
		}
	}
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct{ Dependencies, DevDependencies map[string]string }
	_ = json.Unmarshal(b, &pkg)
	_, dep := pkg.Dependencies["nuxt"]
	_, dev := pkg.DevDependencies["nuxt"]
	return dep || dev
}

func buildNuxt(ctx context.Context, dir string, opts Options) (*Output, error) {
	entry := `
import { loadNuxt, writeTypes, buildNuxt } from "@nuxt/kit";
import { existsSync, promises as fsp } from "node:fs";
import { join } from "node:path";
globalThis.__ogResult = (async () => {
  process.env.NODE_ENV = "production";
  const nuxt = await loadNuxt({ cwd: ` + jsonString(dir) + `, ready: false, dotenv: { cwd: ` + jsonString(dir) + ` } });
  await nuxt.ready();
  // nuxi's clearBuildDir: the previous build's outputs go (Vite does not
  // empty the client outDir, and Nitro copies all of it to .output/public).
  const buildDir = nuxt.options.buildDir;
  if (existsSync(buildDir)) {
    for (const name of await fsp.readdir(buildDir)) {
      if (!["cache", "analyze", "nuxt.json", "nuxt.lock"].includes(name)) await fsp.rm(join(buildDir, name), { recursive: true, force: true });
    }
  }
  await writeTypes(nuxt);
  await buildNuxt(nuxt);
  const output = nuxt.options.nitro?.output ?? {};
  const result = { dir: output.dir ?? ".output", serverDir: output.serverDir ?? ".output/server", publicDir: output.publicDir ?? ".output/public", base: nuxt.options.app?.baseURL ?? "/" };
  await nuxt.close();
  return result;
})();
`
	host := &jshost.Host{Dir: dir, Plugins: hostPlugins(), Patch: applyPatches, TopLevelAwait: true, Shared: sharedVite(dir)}
	code, err := host.Bundle("", entry, "")
	if err != nil {
		return nil, err
	}
	v, err := host.EvalWith(ctx, code, "__ogResult", func(e *engine.Engine) error { return installRolldown(e, opts.licenses) })
	if err != nil {
		return nil, fmt.Errorf("nuxt build: %w", err)
	}
	addAssetLicenses(host, opts.licenses)
	res, _ := v.(map[string]any)
	str := func(k string) string { s, _ := res[k].(string); return s }
	abs := func(p string) string {
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		return p
	}
	pkg := readPackage(dir)
	prog := findServerProgram(dir, pkg, false)
	if prog == nil {
		// Nitro's node server, as `nuxt preview` runs it.
		prog = &ServerProgram{File: filepath.Join(abs(str("serverDir")), "index.mjs")}
	}
	if prog.Env == nil {
		prog.Env = map[string]string{}
	}
	if _, ok := prog.Env["NODE_ENV"]; !ok {
		prog.Env["NODE_ENV"] = "production"
	}
	results := []any{map[string]any{"root": dir, "outDir": abs(str("dir")), "ssr": true, "base": str("base")}}
	out, err := buildServerProgram(dir, pkg, prog, results, opts)
	if err != nil {
		return nil, err
	}
	rel, _ := filepath.Rel(dir, abs(str("publicDir")))
	out.ClientDir = filepath.ToSlash(rel)
	return out, nil
}
