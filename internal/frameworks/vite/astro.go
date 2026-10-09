package vite

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/moriyoshi/oganesson/internal/jshost"
	"github.com/moriyoshi/oganesson/runtime/engine"
)

// Astro builds through Vite: og runs Astro's own build() on the build host
// (with the Rolldown facade), the project's astro.config bundled in and
// passed inline (configFile: false), as og passes vite.config to Vite: the
// config then shares the build host's single copy of Astro and Vite.
//
// A static site (output "static", no adapter) is its outDir, served as
// files. With @astrojs/node in standalone mode the server entry
// (dist/server/entry.mjs) is a server program (program.go).

var astroConfigNames = []string{"astro.config.mjs", "astro.config.js", "astro.config.ts", "astro.config.mts", "astro.config.cjs", "astro.config.cts"}

// FindAstroConfig returns the project's astro.config.*, or "".
func FindAstroConfig(dir string) string {
	for _, n := range astroConfigNames {
		if p := filepath.Join(dir, n); fileExists(p) {
			return p
		}
	}
	return ""
}

// DetectAstro reports an Astro project: an astro.config.* or an "astro"
// dependency.
func DetectAstro(dir string) bool {
	if FindAstroConfig(dir) != "" {
		return true
	}
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct{ Dependencies, DevDependencies map[string]string }
	_ = json.Unmarshal(b, &pkg)
	_, dep := pkg.Dependencies["astro"]
	_, dev := pkg.DevDependencies["astro"]
	return dep || dev
}

func buildAstro(ctx context.Context, dir string, opts Options) (*Output, error) {
	configImport, configExpr := "", "{}"
	if cfg := FindAstroConfig(dir); cfg != "" {
		configImport = fmt.Sprintf("import * as userConfigModule from %q;\n", cfg)
		configExpr = `("default" in userConfigModule ? userConfigModule.default : userConfigModule)`
	}
	entry := configImport + `
import { build } from "astro";
globalThis.__ogResult = (async () => {
  const config = (await ` + configExpr + `) ?? {};
  await build({ ...config, root: ` + jsonString(dir) + `, configFile: false });
  const b = config.build ?? {};
  return { outDir: config.outDir ?? "./dist", client: b.client ?? "./client", server: b.server ?? "./server", serverEntry: b.serverEntry ?? "entry.mjs", base: config.base ?? "/" };
})();
`
	host := &jshost.Host{Dir: dir, Plugins: hostPlugins(), Patch: applyPatches, TopLevelAwait: true, Shared: sharedVite(dir), WorkerShared: sharedPackages(dir, astroConfigs(dir))}
	code, err := host.Bundle("", entry, "")
	if err != nil {
		return nil, err
	}
	v, err := host.EvalWith(ctx, code, "__ogResult", func(e *engine.Engine) error { return installRolldown(e, opts.licenses) })
	if err != nil {
		return nil, fmt.Errorf("astro build: %w", err)
	}
	addAssetLicenses(host, opts.licenses)
	res, _ := v.(map[string]any)
	str := func(k string) string { s, _ := res[k].(string); return s }
	outDir := filepath.Join(dir, str("outDir"))
	serverEntry := filepath.Join(outDir, str("server"), str("serverEntry"))
	if fileExists(serverEntry) {
		// An adapter's server (@astrojs/node standalone): its entry is the
		// server program; it serves outDir/client itself.
		rel, _ := filepath.Rel(dir, outDir)
		results := []any{map[string]any{"root": dir, "outDir": outDir, "ssr": false, "base": str("base")}}
		out, err := buildServerProgram(dir, readPackage(dir), &ServerProgram{File: serverEntry, Env: map[string]string{"NODE_ENV": "production"}}, results, opts)
		if err != nil {
			return nil, err
		}
		out.ClientDir = filepath.ToSlash(filepath.Join(rel, str("client")))
		return out, nil
	}
	out := &Output{Client: map[string][]byte{}, AppType: "mpa", Base: str("base")}
	err = filepath.WalkDir(outDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(outDir, p)
		out.Client[filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	opts.Log("astro: built %d file(s) into %s", len(out.Client), outDir)
	return out, nil
}

// astroConfigs lists the project's Astro config, if any.
func astroConfigs(dir string) []string {
	if cfg := FindAstroConfig(dir); cfg != "" {
		return []string{cfg}
	}
	return nil
}
