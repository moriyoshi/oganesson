package vite

import (
	"fmt"
	"github.com/moriyoshi/oganesson/internal/nativegen"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/moriyoshi/oganesson/internal/gojatarget"
	"github.com/moriyoshi/oganesson/internal/licenses"
	"github.com/moriyoshi/oganesson/runtime/kv"
	"github.com/moriyoshi/oganesson/runtime/nodeshim"
)

// bundleServer bundles the server environment's entry (ESM, as Vite wrote
// it) into the Goja script the runtime runs per request: its default
// export's fetch(request) answers every request the client files do not.
// Node built-ins resolve to runtime/nodeshim.
func bundleServer(dir, entry string, col *licenses.Collector) (string, error) {
	nativePlugin, err := nativegen.Plugin(dir)
	if err != nil {
		return "", err
	}
	glue := fmt.Sprintf(`import %q;
import * as server from %q;
const handler = server.default ?? server;
globalThis.__ogHandler = (request) => handler.fetch(request);
`, nodeshim.GlobalsModule, entry)
	opts := api.BuildOptions{
		Stdin:         &api.StdinOptions{Contents: glue, ResolveDir: dir, Sourcefile: "og-vite-server.js", Loader: api.LoaderJS},
		AbsWorkingDir: dir,
		Bundle:        true,
		Write:         false,
		LogLevel:      api.LogLevelSilent,
		Plugins:       []api.Plugin{serverOutputPlugin(filepath.Dir(entry)), nativePlugin, nodeshim.Plugin(nodeshim.Options{ResolveDir: dir}), kv.Plugin()},
		Define:        map[string]string{"process.env.NODE_ENV": `"production"`},
		Metafile:      true,
	}
	gojatarget.Apply(&opts)
	opts.Platform = api.PlatformNeutral
	opts.MainFields = []string{"module", "main"}
	// esbuild adds "import" or "require" per import kind; naming "import"
	// here would resolve require() to a package's ESM build too.
	opts.Conditions = []string{"node", "production"}
	res := api.Build(opts)
	if len(res.Errors) > 0 {
		return "", fmt.Errorf("vite: bundling the server for Goja:\n%s", formatMessages(res.Errors))
	}
	if err := col.AddMetafile(res.Metafile, dir); err != nil {
		return "", err
	}
	return gojatarget.Finish(string(res.OutputFiles[0].Contents)), nil
}

// requireShimCall matches calls of esbuild's ESM require shim (__require,
// renamed __require2… on conflicts) with a literal specifier.
var requireShimCall = regexp.MustCompile(`\b__require\d*\(\s*("[^"]+"|'[^']+')\s*\)`)

// serverOutputPlugin loads the server environment's output (outDir) for the
// Goja bundle. The facade's ESM output keeps CommonJS require()s of Node
// built-ins as esbuild's __require shim, which only works where a real
// require exists (Node); here they become plain require() calls again, so
// this bundle resolves them statically (to runtime/nodeshim).
func serverOutputPlugin(outDir string) api.Plugin {
	return api.Plugin{
		Name: "og-vite-server-output",
		Setup: func(b api.PluginBuild) {
			b.OnLoad(api.OnLoadOptions{Filter: `\.m?js$`}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
				if !strings.HasPrefix(a.Path, outDir+string(filepath.Separator)) {
					return api.OnLoadResult{}, nil
				}
				src, err := os.ReadFile(a.Path)
				if err != nil {
					return api.OnLoadResult{}, err
				}
				code := string(src)
				// The shim may be defined in a shared chunk the caller imports.
				code = requireShimCall.ReplaceAllString(code, "require($1)")
				return api.OnLoadResult{Contents: &code, Loader: api.LoaderJS, ResolveDir: filepath.Dir(a.Path)}, nil
			})
		},
	}
}
