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

// DetectVitePress reports whether dir declares VitePress as a dependency.
func DetectVitePress(dir string) bool {
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct{ Dependencies, DevDependencies map[string]string }
	_ = json.Unmarshal(b, &pkg)
	_, dep := pkg.Dependencies["vitepress"]
	_, dev := pkg.DevDependencies["vitepress"]
	return dep || dev
}

// buildVitePress runs VitePress's own build API on the build host. That API
// performs both Vite client/server builds and renders the Markdown routes into
// the configured output directory.
func buildVitePress(ctx context.Context, dir string, opts Options) (*Output, error) {
	entry := `
import { build, resolveConfig } from "vitepress";
globalThis.__ogResult = (async () => {
  const root = ` + jsonString(dir) + `;
  const config = await resolveConfig(root, "build", "production");
  await build(root);
  return { outDir: config.outDir, base: config.site.base };
})();
`
	host := &jshost.Host{Dir: dir, Plugins: hostPlugins(), Patch: applyPatches, TopLevelAwait: true, Shared: sharedVite(dir)}
	code, err := host.Bundle("", entry, "")
	if err != nil {
		return nil, err
	}
	v, err := host.EvalWith(ctx, code, "__ogResult", func(e *engine.Engine) error { return installRolldown(e, opts.licenses) })
	if err != nil {
		return nil, fmt.Errorf("vitepress build: %w", err)
	}
	addAssetLicenses(host, opts.licenses)
	res, _ := v.(map[string]any)
	outDir, _ := res["outDir"].(string)
	if outDir == "" {
		return nil, fmt.Errorf("vitepress build: no output directory")
	}
	if !filepath.IsAbs(outDir) {
		outDir = filepath.Join(dir, outDir)
	}
	base, _ := res["base"].(string)
	out := &Output{Client: map[string][]byte{}, AppType: "mpa", Base: base}
	err = filepath.WalkDir(outDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(outDir, path)
		if err != nil {
			return err
		}
		out.Client[filepath.ToSlash(rel)] = b
		return nil
	})
	if err != nil {
		return nil, err
	}
	opts.Log("vitepress: built %d file(s) into %s", len(out.Client), outDir)
	return out, nil
}
