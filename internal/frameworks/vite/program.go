package vite

import (
	"encoding/json"
	"fmt"
	"github.com/moriyoshi/oganesson/internal/nativegen"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/moriyoshi/oganesson/internal/gojatarget"
	"github.com/moriyoshi/oganesson/internal/jshost"
	"github.com/moriyoshi/oganesson/internal/licenses"
	"github.com/moriyoshi/oganesson/runtime/kv"
	"github.com/moriyoshi/oganesson/runtime/natives"
	"github.com/moriyoshi/oganesson/runtime/nodeshim"
)

// A server program is a project's own Node server (Express, Fastify, …)
// that serves a `vite build --ssr` output, as create-vite-extra's SSR
// templates do: the preview / start script runs `node server`, which
// reads ./dist/client/index.html, imports ./dist/server/entry-server.js
// and calls http.createServer(...).listen(). og runs it per request: the
// program runs in the request's VM, its listen() opens nothing, and the
// request is handed to its request listener (runtime/nodeshim's http.js).
// The build outputs are packaged read-only at "/", the runtime's cwd.

// FilesDir holds a server program's project files in the payload.
const FilesDir = "files"

func buildServerProgram(dir string, pkg packageJSON, prog *ServerProgram, results []any, opts Options) (*Output, error) {
	out := &Output{AppType: "custom", Files: map[string][]byte{}, Env: prog.Env}
	for _, rv := range results {
		r, _ := rv.(map[string]any)
		root, _ := r["root"].(string)
		outDir, _ := r["outDir"].(string)
		if root == "" {
			root = dir
		}
		if !filepath.IsAbs(outDir) {
			outDir = filepath.Join(root, outDir)
		}
		rel, err := filepath.Rel(dir, outDir)
		if err != nil || strings.HasPrefix(rel, "..") {
			return nil, fmt.Errorf("vite build: output %s is outside the project", outDir)
		}
		if r["ssr"] != true && out.ClientDir == "" {
			out.ClientDir = filepath.ToSlash(rel)
			out.Base, _ = r["base"].(string)
		}
		// The run's output, and each environment's (a builder run writes
		// the client and the server apart: build/client, build/server).
		dirs := []string{outDir}
		envs, _ := r["environments"].([]any)
		for _, ev := range envs {
			e, _ := ev.(map[string]any)
			if d, _ := e["outDir"].(string); d != "" {
				if !filepath.IsAbs(d) {
					d = filepath.Join(root, d)
				}
				if rel, err := filepath.Rel(dir, d); err == nil && !strings.HasPrefix(rel, "..") {
					dirs = append(dirs, d)
				}
			}
		}
		for _, d := range dirs {
			err = filepath.WalkDir(d, func(p string, de fs.DirEntry, err error) error {
				if os.IsNotExist(err) && p == d {
					return nil // an environment that did not build
				}
				if err != nil || de.IsDir() {
					return err
				}
				b, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				name, _ := filepath.Rel(dir, p)
				out.Files[filepath.ToSlash(name)] = b
				return nil
			})
			if err != nil {
				return nil, err
			}
		}
	}
	// The program's own directory, when it is a build output of its own
	// (SvelteKit's adapter-node writes build/ with its static files and
	// prerendered pages): not the project root (a server.js there) and not
	// a package (a bin).
	if progDir := filepath.Dir(prog.File); progDir != dir && strings.HasPrefix(progDir, dir+string(filepath.Separator)) && !strings.Contains(progDir, string(filepath.Separator)+"node_modules"+string(filepath.Separator)) {
		err := filepath.WalkDir(progDir, func(p string, de fs.DirEntry, err error) error {
			if err != nil || de.IsDir() {
				return err
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			name, _ := filepath.Rel(dir, p)
			out.Files[filepath.ToSlash(name)] = b
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	js, err := bundleProgram(dir, prog.File, prog.Args, pkg.DevDependencies, opts.licenses)
	if err != nil {
		return nil, err
	}
	out.ServerJS = js
	rel, _ := filepath.Rel(dir, prog.File)
	opts.Log("vite: server program %s (env %v), %d project file(s)", rel, prog.Env, len(out.Files))
	return out, nil
}

const programGlue = "og:server-program-glue"

// programProbe marks a resolution the server-program plugin makes itself.
type programProbeKey struct{}

var programProbe = programProbeKey{}

// bundleProgram bundles the server program for Goja. It is ESM with
// top-level await (the template awaits fs reads and imports), so the
// bundle is ESM run inside an async function; the glue, evaluated first,
// gives the runtime its handler before the program's first await.
func bundleProgram(dir, file string, args []string, devDependencies map[string]string, col *licenses.Collector) (string, error) {
	nativePlugin, err := nativegen.Plugin(dir)
	if err != nil {
		return "", err
	}
	entry := fmt.Sprintf("import %q;\nimport %q;\nimport %q;\n", nodeshim.GlobalsModule, programGlue, file)
	rel, _ := filepath.Rel(dir, file)
	argv, _ := json.Marshal(append([]string{"node", "/" + filepath.ToSlash(rel)}, args...))
	// Arguments naming project modules (react-router-serve's server build)
	// are imported by a computed specifier (its file URL), which no
	// bundler follows: they are bundled too, evaluated on first import,
	// and found by their path in the packaged layout.
	var modules []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		m := resolveNodeMain(filepath.Join(dir, a))
		if m == "" || !strings.HasPrefix(m, dir+string(filepath.Separator)) || !jsModule.MatchString(m) {
			continue
		}
		mrel, _ := filepath.Rel(dir, m)
		modules = append(modules, fmt.Sprintf("%q: () => require(%q)", "/"+filepath.ToSlash(mrel), m))
	}
	glue := `import { serve } from "node:http";
process.argv = ` + string(argv) + `;
const modules = {` + strings.Join(modules, ", ") + `};
const loaded = {};
globalThis[Symbol.for("og.programModule")] = (id) => {
  const p = String(id).startsWith("file://") ? decodeURIComponent(new URL(id).pathname) : String(id);
  return Object.hasOwn(modules, p) ? (loaded[p] ??= modules[p]()) : undefined;
};
globalThis.__ogHandler = (request) => serve(request, globalThis.__ogProgram);
`
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		realDir = dir
	}
	opts := api.BuildOptions{
		Stdin:         &api.StdinOptions{Contents: entry, ResolveDir: dir, Sourcefile: "og-server-program.js", Loader: api.LoaderJS},
		AbsWorkingDir: dir,
		Bundle:        true,
		Write:         false,
		LogLevel:      api.LogLevelSilent,
		Metafile:      true,
		Plugins: []api.Plugin{{
			Name: "og-server-program",
			Setup: func(b api.PluginBuild) {
				// Each file's import.meta.url is its URL in the packaged
				// layout (project files mounted at "/", runtime/vfs): the
				// program finds its files from it (@astrojs/node walks up from
				// it to dist/client). A bundled script has no import.meta.
				b.OnLoad(api.OnLoadOptions{Filter: `\.(m?js|m?ts|jsx|tsx)$`}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
					if a.Namespace != "file" {
						return api.OnLoadResult{}, nil
					}
					// esbuild reports real paths: the project may be reached
					// through a symlink (macOS's /var is /private/var).
					root := dir
					if !strings.HasPrefix(a.Path, root+string(filepath.Separator)) {
						if root = realDir; !strings.HasPrefix(a.Path, root+string(filepath.Separator)) {
							return api.OnLoadResult{}, nil
						}
					}
					src, err := os.ReadFile(a.Path)
					if err != nil || !strings.Contains(string(src), "import.meta.") {
						return api.OnLoadResult{}, nil
					}
					rel, _ := filepath.Rel(root, a.Path)
					code := jshost.ReplaceImportMeta(a.Path, string(src), "/"+filepath.ToSlash(rel))
					loader := api.LoaderJS
					switch filepath.Ext(a.Path) {
					case ".ts", ".mts":
						loader = api.LoaderTS
					case ".tsx":
						loader = api.LoaderTSX
					case ".jsx":
						loader = api.LoaderJSX
					}
					return api.OnLoadResult{Contents: &code, Loader: loader, ResolveDir: filepath.Dir(a.Path)}, nil
				})
				b.OnResolve(api.OnResolveOptions{Filter: "^" + programGlue + "$"}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
					return api.OnResolveResult{Path: programGlue, Namespace: "og-program"}, nil
				})
				b.OnLoad(api.OnLoadOptions{Filter: ".*", Namespace: "og-program"}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
					if a.Path == programGlue {
						return api.OnLoadResult{Contents: &glue, Loader: api.LoaderJS, ResolveDir: dir}, nil
					}
					stub := fmt.Sprintf("throw new Error(%q);", "og: "+a.Path+" is not part of the packaged app (a devDependency, or not installed)")
					return api.OnLoadResult{Contents: &stub, Loader: api.LoaderJS}, nil
				})
				// A devDependency imported dynamically is a development
				// branch (the template's `await import('vite')`), and a
				// dynamic import of a package that is not installed is an
				// optional feature: either stays out of the bundle, failing
				// if it is ever reached, as in Node.
				b.OnResolve(api.OnResolveOptions{Filter: `^[^./]`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
					if a.Kind != api.ResolveJSDynamicImport || a.PluginData == programProbe {
						return api.OnResolveResult{}, nil
					}
					if _, dev := devDependencies[packageName(a.Path)]; !dev {
						r := b.Resolve(a.Path, api.ResolveOptions{Importer: a.Importer, ResolveDir: a.ResolveDir, Kind: a.Kind, Namespace: a.Namespace, PluginData: programProbe})
						if len(r.Errors) == 0 {
							return api.OnResolveResult{Path: r.Path, Namespace: r.Namespace, External: r.External, PluginData: r.PluginData}, nil
						}
					}
					return api.OnResolveResult{Path: a.Path, Namespace: "og-program"}, nil
				})
			},
		}, natives.Plugin(), nativePlugin, nodeshim.Plugin(nodeshim.Options{ResolveDir: dir}), kv.Plugin()},
		Define: map[string]string{"process.env.NODE_ENV": `"production"`},
	}
	gojatarget.Apply(&opts)
	opts.Format = api.FormatESModule
	opts.Platform = api.PlatformNeutral
	opts.MainFields = []string{"module", "main"}
	// esbuild adds "import" or "require" per import kind; naming "import"
	// here would resolve require() to a package's ESM build too.
	opts.Conditions = []string{"node", "production"}
	if opts.Supported == nil {
		opts.Supported = map[string]bool{}
	}
	opts.Supported["top-level-await"] = true
	res := api.Build(opts)
	if len(res.Errors) > 0 {
		return "", fmt.Errorf("vite: bundling the server program for Goja:\n%s", formatMessages(res.Errors))
	}
	if err := col.AddMetafile(res.Metafile, dir); err != nil {
		return "", err
	}
	// esbuild's __require fallback, reached by computed import() and
	// require(), finds the program's argument modules.
	code := strings.Replace(string(res.OutputFiles[0].Contents), dynamicRequireThrow,
		`var og$find = globalThis[Symbol.for("og.programModule")], og$m = og$find && og$find(x);
    if (og$m) return og$m;
    `+dynamicRequireThrow, 1)
	code = "globalThis.__ogProgram = (async () => {\n" + code + "\n})();\n"
	return gojatarget.Finish(code), nil
}

// dynamicRequireThrow ends esbuild's __require helper: a require (or a
// lowered import()) that the bundle did not resolve.
const dynamicRequireThrow = `throw Error('Dynamic require of "' + x + '" is not supported');`

var jsModule = regexp.MustCompile(`\.(?:[cm]?js|[cm]?ts)$`)

// packageName is the npm package a bare specifier names.
func packageName(spec string) string {
	parts := strings.SplitN(spec, "/", 3)
	if strings.HasPrefix(spec, "@") && len(parts) > 1 {
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}
