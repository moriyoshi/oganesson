// Package vite builds Vite projects. Vite's own JavaScript (config
// resolution, the builder and its environments, its plugins and the
// project's) runs on the build host (internal/jshost); Rolldown, Vite's
// Rust bundler, is replaced by a facade over esbuild (js/rolldown.js,
// esbuild.go).
package vite

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/microsoft/TypeScript/tsc/ogapi"
	"github.com/moriyoshi/oganesson/runtime/nodeshim"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/moriyoshi/oganesson/internal/jshost"
	"github.com/moriyoshi/oganesson/internal/licenses"
	"github.com/moriyoshi/oganesson/runtime/app"
	"github.com/moriyoshi/oganesson/runtime/engine"
	"github.com/moriyoshi/oganesson/runtime/notices"
)

// Options configure a build.
type Options struct {
	Log func(format string, args ...any)

	licenses *licenses.Collector // npm packages the shipped bundles read
}

// Output is a built Vite project.
type Output struct {
	// Client holds the client build (build.outDir), by path relative to it.
	Client map[string][]byte
	// AppType is Vite's appType: "spa" (unknown paths serve index.html),
	// "mpa" or "custom".
	AppType string
	// Base is Vite's base (the public path the app is served under).
	Base string
	// ServerJS is the server environment's entry (a module whose default
	// export has fetch(request), as TanStack Start's), bundled for Goja;
	// empty for a client-only app.
	ServerJS string
	// Env is environment the server needs at run time (a server program's
	// script environment).
	Env map[string]string
	// Files are a server program's project files (its build outputs), by
	// path relative to the project; the program reads them at run time.
	// ClientDir is the client build among them (dist/client).
	Files     map[string][]byte
	ClientDir string
	// Notices are the licenses of the npm packages bundled into the client
	// and server code (notices.AppFile), nil when none.
	Notices []byte
}

// configNames are the files Vite loads its config from, in its order.
var configNames = []string{"vite.config.js", "vite.config.mjs", "vite.config.ts", "vite.config.cjs", "vite.config.mts", "vite.config.cts"}

// FindConfig returns the project's vite.config.*, or "".
func FindConfig(dir string) string {
	for _, n := range configNames {
		if p := filepath.Join(dir, n); fileExists(p) {
			return p
		}
	}
	return ""
}

// Detect reports whether dir is a Vite project: a vite.config.* or a
// "vite" dependency, or a supported framework built through Vite.
func Detect(dir string) bool {
	if FindConfig(dir) != "" || DetectAstro(dir) || DetectNuxt(dir) || DetectVitePress(dir) {
		return true
	}
	b, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct {
		Dependencies, DevDependencies map[string]string
	}
	_ = json.Unmarshal(b, &pkg)
	_, dep := pkg.Dependencies["vite"]
	_, dev := pkg.DevDependencies["vite"]
	return dep || dev
}

// run is one Vite build: a `vite build` of the build script, or the
// default `vite build`.
type run struct {
	Config int            `json:"config"` // index into the config modules, -1: none
	Mode   string         `json:"mode"`
	Root   string         `json:"root"`
	Inline map[string]any `json:"inline"` // the CLI flags as inline config
}

// Build runs Vite's production build of the project in dir: the `vite
// build` commands of its build script, or a plain `vite build`.
func Build(ctx context.Context, dir string, opts Options) (*Output, error) {
	opts.licenses = &licenses.Collector{}
	out, err := build(ctx, dir, opts)
	if err != nil {
		return nil, err
	}
	if out.Notices, err = opts.licenses.Text(); err != nil {
		return nil, err
	}
	return out, nil
}

// addAssetLicenses attributes the npm packages whose CSS or font bytes a
// build-time plugin (Tailwind, Vite's PostCSS) inlined into the output, which
// esbuild never saw as bundle inputs. The host recorded them as it read them
// through og.fs (jshost.Host.AssetReads).
func addAssetLicenses(host *jshost.Host, col *licenses.Collector) {
	for _, f := range host.AssetReads() {
		col.AddFile(f)
	}
}

func build(ctx context.Context, dir string, opts Options) (*Output, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if opts.Log == nil {
		opts.Log = func(string, ...any) {}
	}
	if DetectNuxt(dir) {
		return buildNuxt(ctx, dir, opts)
	}
	if DetectAstro(dir) {
		return buildAstro(ctx, dir, opts)
	}
	if DetectVitePress(dir) {
		return buildVitePress(ctx, dir, opts)
	}
	pkg := readPackage(dir)
	invocations := buildInvocations(pkg)
	if len(invocations) == 0 {
		invocations = []Invocation{{}}
	}
	var configs []string
	var runs []run
	for _, inv := range invocations {
		root, mode, configFile, inline := cliConfig(inv.Args)
		r := run{Config: -1, Mode: mode, Root: dir, Inline: inline}
		if root != "" {
			r.Root = filepath.Join(dir, root)
		}
		if configFile != "" {
			configFile = filepath.Join(dir, configFile)
		} else {
			configFile = FindConfig(r.Root)
		}
		if configFile != "" {
			r.Config = slices.Index(configs, configFile)
			if r.Config < 0 {
				configs = append(configs, configFile)
				r.Config = len(configs) - 1
			}
		}
		runs = append(runs, r)
	}
	var imports, modules []string
	for i, c := range configs {
		imports = append(imports, fmt.Sprintf("import * as config%d from %q;", i, c))
		modules = append(modules, fmt.Sprintf("config%d", i))
	}
	runsJSON, _ := json.Marshal(runs)
	configFilesJSON, _ := json.Marshal(configs)
	entry := strings.Join(imports, "\n") + `
import { createBuilder, mergeConfig } from "vite";
const configs = [` + strings.Join(modules, ", ") + `];
const configFiles = ` + string(configFilesJSON) + `;
globalThis.__ogResult = (async () => {
  const results = [];
  for (const run of ` + string(runsJSON) + `) {
    // Vite's ConfigEnv for "vite build", with the CLI's flags merged over
    // the config as Vite merges inline config; the config is passed inline.
    const mode = run.mode || "production";
    const env = { command: "build", mode, isSsrBuild: !!run.inline?.build?.ssr, isPreview: false };
    let config = run.config < 0 ? {} : configs[run.config];
    if (run.config >= 0 && "default" in config) config = config.default;
    if (typeof config === "function") config = config(env);
    config = mergeConfig((await config) ?? {}, run.inline ?? {});
    // useLegacyBuilder null, as the vite build CLI passes: without a
    // builder config only the client (or --ssr) environment is built.
    // The config is passed inline, but the resolved config names its
    // file as with "vite build" (React Router loads it again itself).
    const plugins = [...(config.plugins ?? [])];
    if (run.config >= 0) plugins.unshift({ name: "og:config-file", enforce: "pre", configResolved(c) { c.configFile = configFiles[run.config]; } });
    const builder = await createBuilder({ ...config, plugins, configFile: false, mode, root: config.root ?? run.root }, null);
    await builder.buildApp();
    const c = builder.config;
    const environments = Object.entries(builder.environments).map(([name, e]) => ({
      name, consumer: e.config.consumer, outDir: e.config.build.outDir,
    }));
    results.push({ root: c.root, outDir: c.build.outDir, ssr: !!c.build.ssr, appType: c.appType, base: c.base, environments });
  }
  return { runs: results, builds: globalThis[Symbol.for("og.viteBuilds")] ?? [] };
})();
`
	host := &jshost.Host{Dir: dir, Plugins: hostPlugins(), Patch: applyPatches, TopLevelAwait: true, Shared: sharedVite(dir), WorkerShared: sharedPackages(dir, configs)}
	code, err := host.Bundle("", entry, "")
	if err != nil {
		return nil, err
	}
	v, err := host.EvalWith(ctx, code, "__ogResult", func(e *engine.Engine) error { return installRolldown(e, opts.licenses) })
	if err != nil {
		return nil, fmt.Errorf("vite build: %w", err)
	}
	addAssetLicenses(host, opts.licenses)
	all, _ := v.(map[string]any)
	results, _ := all["runs"].([]any)
	if len(results) == 0 {
		return nil, fmt.Errorf("vite build: no build ran")
	}
	// The client build (the first without --ssr) is the app's primary.
	res, _ := results[0].(map[string]any)
	ssrRuns := []map[string]any{}
	for _, rv := range results {
		r, _ := rv.(map[string]any)
		if r["ssr"] == true {
			ssrRuns = append(ssrRuns, r)
		} else if res["ssr"] == true {
			res = r
		}
	}
	res["builds"] = all["builds"]
	if len(ssrRuns) > 0 {
		if prog := serverProgram(dir, pkg); prog != nil {
			return buildServerProgram(dir, pkg, prog, results, opts)
		}
		return nil, fmt.Errorf("vite build: the build script runs vite build --ssr, but no preview / start / serve script runs a node server program")
	}

	outDir, _ := res["outDir"].(string)
	if outDir == "" {
		return nil, fmt.Errorf("vite build: no output directory")
	}
	// The client environment's output is what the browser gets; a server
	// consumer's written entry is the server.
	var serverEntry string
	envs, _ := res["environments"].([]any)
	builds, _ := res["builds"].([]any)
	for _, ev := range envs {
		e, _ := ev.(map[string]any)
		envDir, _ := e["outDir"].(string)
		if envDir == "" {
			continue
		}
		if !filepath.IsAbs(envDir) {
			envDir = filepath.Join(dir, envDir)
		}
		switch e["consumer"] {
		case "client":
			if e["name"] == "client" {
				outDir = envDir
			}
		case "server":
			for _, bv := range builds {
				b, _ := bv.(map[string]any)
				entries, _ := b["entries"].([]any)
				if bd, _ := b["dir"].(string); filepath.Clean(bd) == filepath.Clean(envDir) && len(entries) > 0 && serverEntry == "" {
					first, _ := entries[0].(string)
					serverEntry = filepath.Join(envDir, filepath.FromSlash(first))
				}
			}
		}
	}
	if !filepath.IsAbs(outDir) {
		outDir = filepath.Join(dir, outDir)
	}
	// A server environment wrote an entry, and the project's start script
	// serves the build with a program of its own (React Router's
	// react-router-serve): that program is the server, as with --ssr.
	if serverEntry != "" {
		if prog := serverProgram(dir, pkg); prog != nil {
			return buildServerProgram(dir, pkg, prog, results, opts)
		}
	}
	out := &Output{Client: map[string][]byte{}}
	out.AppType, _ = res["appType"].(string)
	out.Base, _ = res["base"].(string)
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
	if serverEntry != "" {
		if out.ServerJS, err = bundleServer(dir, serverEntry, opts.licenses); err != nil {
			return nil, err
		}
		opts.Log("vite: server entry %s", serverEntry)
	}
	opts.Log("vite: built %d client file(s) into %s", len(out.Client), outDir)
	return out, nil
}

// Payload is the app as og packages it: meta.json, the client build under
// app.ClientDir and, with a server, app.ServerFile.
func (out *Output) Payload() map[string][]byte {
	meta := app.Meta{Format: app.FormatVersion, Framework: "vite", AppType: out.AppType, BasePath: strings.TrimSuffix(out.Base, "/"), Env: out.Env}
	files := map[string][]byte{}
	if out.Files != nil {
		// A server program: its project files, read-only at "/"; the
		// program serves them itself.
		meta.Files = FilesDir
		meta.ClientDir = FilesDir + "/" + out.ClientDir
		for name, b := range out.Files {
			files[FilesDir+"/"+name] = b
		}
	}
	metaJSON, _ := json.MarshalIndent(meta, "", "  ")
	files[app.MetaFile] = metaJSON
	if out.ServerJS != "" {
		files[app.ServerFile] = []byte(out.ServerJS)
	}
	for name, b := range out.Client {
		files[app.ClientDir+"/"+name] = b
	}
	if len(out.Notices) > 0 {
		files[notices.AppFile] = out.Notices
	}
	return files
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// vitePackages are the packages every build host shares with modules
// loaded at run time (jshost.Host.Shared), as Node loads a package once:
// Vite, which plugins import by a computed specifier (React Router's
// getVite()) or file URL (Vite's own config loader), and Rolldown's entry
// points, which are og's facade in the bundle (Nuxt loads its own modules
// at run time, and they import rolldown/utils).
var vitePackages = []string{"vite", "rolldown", "rolldown/experimental", "rolldown/filter", "rolldown/parseAst", "rolldown/plugins", "rolldown/utils"}

// sharedVite is vitePackages for the project's Vite: Vite 5 to 7 bundle
// with Rollup (its WebAssembly build, natives.go) and have no Rolldown.
func sharedVite(dir string) []string {
	if viteMajor(dir) < 8 {
		return []string{"vite"}
	}
	return vitePackages
}

// viteMajor is the major version of the project's installed Vite (0 if
// unknown).
func viteMajor(dir string) int {
	b, err := os.ReadFile(filepath.Join(dir, "node_modules", "vite", "package.json"))
	if err != nil {
		return 0
	}
	var pj struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(b, &pj) != nil {
		return 0
	}
	major, _ := strconv.Atoi(strings.SplitN(pj.Version, ".", 2)[0])
	return major
}

// sharedPackages are the packages build workers share with modules they
// load at run time (jshost.Host.WorkerShared), as Node loads a package
// once: vitePackages, and the packages the config files import, which a
// worker reloading the config (SvelteKit's analysis and prerendering) gets
// as bundled (top-level await included).
func sharedPackages(dir string, configs []string) []string {
	out := append([]string(nil), sharedVite(dir)...)
	seen := map[string]bool{}
	for _, p := range out {
		seen[p] = true
	}
	for _, c := range configs {
		src, err := os.ReadFile(c)
		if err != nil {
			continue
		}
		for _, im := range ogapi.ParseModule(c, string(src)).Imports {
			spec := im.Specifier
			if im.TypeOnly || spec == "" || seen[spec] || strings.HasPrefix(spec, ".") || strings.HasPrefix(spec, "/") || strings.HasPrefix(spec, "node:") || nodeshim.Builtins[spec] {
				continue
			}
			seen[spec] = true
			out = append(out, spec)
		}
	}
	return out
}
