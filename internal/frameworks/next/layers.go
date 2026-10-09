package next

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Layer mirrors Next's webpack module layers. Oganesson encodes the layer as the
// esbuild namespace of every module, so one build holds several instances of
// a file when layers differ, exactly like webpack layers.
type Layer string

const (
	LayerSSR    Layer = "ssr"    // serverSideRendering: edge wrapper, app-render, SSR of client components
	LayerRSC    Layer = "rsc"    // reactServerComponents: the app page, layouts, server components
	LayerShared Layer = "shared" // single-instance async storages shared by rsc and ssr
	// LayerBrowser is appPagesBrowser: the client bundle. It lives in its
	// own esbuild build, so it never mixes with the server layers.
	LayerBrowser Layer = "browser"
	// LayerPages holds Pages Router modules on the server and
	// LayerPagesBrowser in the pages browser bundle: webpack builds them
	// without a layer, so plain React and the Pages versions of next/link,
	// next/head and next/dynamic (no App Router aliases).
	LayerPages        Layer = "pages"
	LayerPagesBrowser Layer = "pages-browser"
)

func isBrowserLayer(l Layer) bool { return l == LayerBrowser || l == LayerPagesBrowser }

// Layer transitions taken from next/dist/build/webpack-config.js.
var (
	// "Ensure that the app page module is in the client layers".
	appPageModuleRe = regexp.MustCompile(`next[\\/]dist[\\/](esm[\\/])?server[\\/]route-modules[\\/]app-page[\\/]module`)
	// asyncStoragesRegex: one instance across layers.
	asyncStoragesRe = regexp.MustCompile(`next[\\/]dist[\\/](esm[\\/])?server[\\/]app-render[\\/](work-async-storage|action-async-storage|dynamic-access-async-storage|work-unit-async-storage)`)
)

// layerFor returns the layer a resolved module lands in when imported from
// a module in the from layer.
func layerFor(from Layer, resolvedPath string) Layer {
	if asyncStoragesRe.MatchString(resolvedPath) {
		return LayerShared
	}
	if from == LayerRSC && appPageModuleRe.MatchString(resolvedPath) {
		return LayerSSR
	}
	if from == LayerShared {
		return LayerSSR
	}
	return from
}

// Exact ("$") aliases from createVendoredReactAliases for the edge server.
var reactAliases = map[Layer]map[string]string{
	LayerRSC: {
		"react":                                     "next/dist/compiled/react/react.react-server",
		"react/compiler-runtime":                    "next/dist/compiled/react/compiler-runtime",
		"react/jsx-dev-runtime":                     "next/dist/compiled/react/jsx-dev-runtime.react-server",
		"react/jsx-runtime":                         "next/dist/compiled/react/jsx-runtime.react-server",
		"react-dom":                                 "next/dist/compiled/react-dom/react-dom.react-server",
		"react-dom/client":                          "next/dist/compiled/react-dom/client",
		"react-dom/server":                          "next/dist/build/webpack/alias/react-dom-server.js",
		"react-dom/server.browser":                  "next/dist/compiled/react-dom/server.browser",
		"react-dom/server.edge":                     "next/dist/build/webpack/alias/react-dom-server.js",
		"react-dom/static":                          "next/dist/compiled/react-dom/static.edge",
		"react-dom/static.browser":                  "next/dist/compiled/react-dom/static.browser",
		"react-dom/static.edge":                     "next/dist/compiled/react-dom/static.edge",
		"react-server-dom-webpack/client":           "next/dist/compiled/react-server-dom-webpack/client.edge",
		"react-server-dom-webpack/server":           "next/dist/compiled/react-server-dom-webpack/server.edge",
		"react-server-dom-webpack/server.edge":      "next/dist/compiled/react-server-dom-webpack/server.edge",
		"react-server-dom-webpack/server.node":      "next/dist/compiled/react-server-dom-webpack/server.node",
		"react-server-dom-webpack/static":           "next/dist/compiled/react-server-dom-webpack/static.edge",
		"next/dist/compiled/react":                  "next/dist/compiled/react/react.react-server",
		"next/dist/compiled/react/compiler-runtime": "next/dist/compiled/react/compiler-runtime",
		"next/dist/compiled/react/jsx-dev-runtime":  "next/dist/compiled/react/jsx-dev-runtime.react-server",
		"next/dist/compiled/react/jsx-runtime":      "next/dist/compiled/react/jsx-runtime.react-server",
		"next/dist/compiled/react-dom":              "next/dist/compiled/react-dom/react-dom.react-server",
		// createServerOnlyClientOnlyAliases(isServer) for server-only layers.
		"server-only":                    "next/dist/compiled/server-only/empty",
		"client-only":                    "next/dist/compiled/client-only/error",
		"next/dist/compiled/server-only": "next/dist/compiled/server-only/empty",
		"next/dist/compiled/client-only": "next/dist/compiled/client-only/error",
		"setimmediate":                   "next/dist/compiled/setimmediate",
	},
	LayerSSR: {
		"react":                                "next/dist/compiled/react",
		"react/compiler-runtime":               "next/dist/compiled/react/compiler-runtime",
		"react/jsx-dev-runtime":                "next/dist/compiled/react/jsx-dev-runtime",
		"react/jsx-runtime":                    "next/dist/compiled/react/jsx-runtime",
		"react-dom":                            "next/dist/compiled/react-dom",
		"react-dom/client":                     "next/dist/compiled/react-dom/client",
		"react-dom/server":                     "next/dist/build/webpack/alias/react-dom-server.js",
		"react-dom/server.browser":             "next/dist/compiled/react-dom/server.browser",
		"react-dom/server.edge":                "next/dist/build/webpack/alias/react-dom-server.js",
		"react-dom/static":                     "next/dist/compiled/react-dom/static.edge",
		"react-dom/static.browser":             "next/dist/compiled/react-dom/static.browser",
		"react-dom/static.edge":                "next/dist/compiled/react-dom/static.edge",
		"react-server-dom-webpack/client":      "next/dist/compiled/react-server-dom-webpack/client.edge",
		"react-server-dom-webpack/client.edge": "next/dist/compiled/react-server-dom-webpack/client.edge",
		"react-server-dom-webpack/server":      "next/dist/compiled/react-server-dom-webpack/server.edge",
		"react-server-dom-webpack/server.node": "next/dist/compiled/react-server-dom-webpack/server.node",
		"react-server-dom-webpack/static":      "next/dist/compiled/react-server-dom-webpack/static.edge",
		"server-only":                          "next/dist/compiled/server-only/index",
		"client-only":                          "next/dist/compiled/client-only/index",
		"next/dist/compiled/client-only":       "next/dist/compiled/client-only/index",
		"next/dist/compiled/server-only":       "next/dist/compiled/server-only/index",
		"setimmediate":                         "next/dist/compiled/setimmediate",
	},
}

func init() {
	// createVendoredReactAliases for browser + client, plus the
	// react-server-dom-webpack client and server-only/client-only aliases.
	reactAliases[LayerBrowser] = map[string]string{
		"react":                           "next/dist/compiled/react",
		"react/compiler-runtime":          "next/dist/compiled/react/compiler-runtime",
		"react/jsx-dev-runtime":           "next/dist/compiled/react/jsx-dev-runtime",
		"react/jsx-runtime":               "next/dist/compiled/react/jsx-runtime",
		"react-dom":                       "next/dist/compiled/react-dom",
		"react-dom/client":                "next/dist/compiled/react-dom/client",
		"react-dom/server":                "next/dist/compiled/react-dom/server.browser",
		"react-dom/server.browser":        "next/dist/compiled/react-dom/server.browser",
		"react-dom/static":                "next/dist/compiled/react-dom/static.browser",
		"react-dom/static.browser":        "next/dist/compiled/react-dom/static.browser",
		"react-server-dom-webpack/client": "next/dist/compiled/react-server-dom-webpack/client.browser",
		"scheduler":                       "next/dist/compiled/scheduler",
		"server-only":                     "next/dist/compiled/server-only/index",
		"client-only":                     "next/dist/compiled/client-only/index",
		"next/dist/compiled/server-only":  "next/dist/compiled/server-only/index",
		"next/dist/compiled/client-only":  "next/dist/compiled/client-only/index",
		"setimmediate":                    "next/dist/compiled/setimmediate",
	}
	appRouterAPIAliases[LayerBrowser] = appRouterAPIAliases[LayerSSR]
	// Next vendors React for the App Router's layers only: Pages Router code
	// uses the project's react and react-dom (React 18 included, which Next
	// 16 still supports there: reactstrap's example).
	reactAliases[LayerPages] = withoutReact(reactAliases[LayerSSR])
	reactAliases[LayerPagesBrowser] = withoutReact(reactAliases[LayerBrowser])
	// getOptimizedModuleAliases, for the browser builds (the server ones
	// resolve packages as Node does, url included).
	for _, l := range []Layer{LayerBrowser, LayerPagesBrowser} {
		for k, v := range optimizedModuleAliases {
			reactAliases[l][k] = v
		}
	}
}

// optimizedModuleAliases are Next's getOptimizedModuleAliases
// (create-compiler-aliases.js): smaller polyfills for the browser, and its
// url (a page requiring "url": with-shallow-routing).
var optimizedModuleAliases = map[string]string{
	"unfetch":                      "next/dist/build/polyfills/fetch/index.js",
	"isomorphic-unfetch":           "next/dist/build/polyfills/fetch/index.js",
	"whatwg-fetch":                 "next/dist/build/polyfills/fetch/whatwg-fetch.js",
	"object-assign":                "next/dist/build/polyfills/object-assign.js",
	"object.assign/auto":           "next/dist/build/polyfills/object.assign/auto.js",
	"object.assign/implementation": "next/dist/build/polyfills/object.assign/implementation.js",
	"object.assign/polyfill":       "next/dist/build/polyfills/object.assign/polyfill.js",
	"object.assign/shim":           "next/dist/build/polyfills/object.assign/shim.js",
	"url":                          "next/dist/compiled/native-url",
}

// withoutReact is aliases without those of react, react-dom and scheduler.
func withoutReact(aliases map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range aliases {
		switch {
		case k == "react", strings.HasPrefix(k, "react/"), k == "react-dom", strings.HasPrefix(k, "react-dom/"), k == "scheduler":
			continue
		}
		out[k] = v
	}
	return out
}

// Prefix aliases from createWebpackAliases (edge server).
var prefixAliases = [][2]string{
	{"next/dist/api", "next/dist/esm/api"},
	{"next/dist/build", "next/dist/esm/build"},
	{"next/dist/client", "next/dist/esm/client"},
	{"next/dist/shared", "next/dist/esm/shared"},
	{"next/dist/pages", "next/dist/esm/pages"},
	{"next/dist/lib", "next/dist/esm/lib"},
	{"next/dist/server", "next/dist/esm/server"},
	{"@opentelemetry/api", "next/dist/compiled/@opentelemetry/api"},
}

// Post-resolution aliases for next/<api>.js (createNextApiEsmAliases and,
// per layer, createAppRouterApiAliases), keyed by the API name.
var nextAPIAliases = map[string]string{
	"error": "next/dist/api/error", "head": "next/dist/api/head", "image": "next/dist/api/image",
	"constants": "next/dist/api/constants", "router": "next/dist/api/router", "dynamic": "next/dist/api/dynamic",
	"script": "next/dist/api/script", "link": "next/dist/api/link", "form": "next/dist/api/form",
	"navigation": "next/dist/api/navigation", "headers": "next/dist/api/headers", "og": "next/dist/api/og",
	"server": "next/dist/api/server", "document": "next/dist/api/document", "app": "next/dist/api/app",
}

var appRouterAPIAliases = map[Layer]map[string]string{
	LayerSSR: {
		"head": "next/dist/client/components/noop-head", "dynamic": "next/dist/api/app-dynamic",
		"link": "next/dist/client/app-dir/link", "form": "next/dist/client/app-dir/form",
	},
	LayerRSC: {
		"head": "next/dist/client/components/noop-head", "dynamic": "next/dist/api/app-dynamic",
		"link": "next/dist/client/app-dir/link.react-server", "form": "next/dist/client/app-dir/form",
		"error": "next/dist/api/error.react-server", "navigation": "next/dist/api/navigation.react-server",
	},
}

// aliasSpecifier applies exact and prefix aliases to an import specifier.
func aliasSpecifier(layer Layer, spec string) string {
	for range 4 {
		next := spec
		if t, ok := reactAliases[layer][spec]; ok {
			next = t
		} else if !isBrowserLayer(layer) { // the client builds use next/dist (CJS) as-is
			for _, p := range prefixAliases {
				if spec == p[0] || strings.HasPrefix(spec, p[0]+"/") {
					next = p[1] + spec[len(p[0]):]
					break
				}
			}
		}
		if next == spec {
			return spec
		}
		spec = next
	}
	return spec
}

// aliasResolved maps a resolved next/<api>.js file to its layer target.
func aliasResolved(layer Layer, nextDir, resolved string) (string, bool) {
	if filepath.Dir(resolved) != nextDir || filepath.Ext(resolved) != ".js" {
		return "", false
	}
	api := strings.TrimSuffix(filepath.Base(resolved), ".js")
	if t, ok := appRouterAPIAliases[layer][api]; ok {
		return t, true
	}
	if isBrowserLayer(layer) {
		return "", false // createNextApiEsmAliases is edge-only
	}
	if t, ok := nextAPIAliases[api]; ok {
		return t, true
	}
	return "", false
}

// rscConditions are the export conditions of the RSC layer on the edge.
var rscConditions = []string{"react-server", "edge-light", "worker", "browser", "import", "module", "default"}

// resolveReactServerExport resolves a bare specifier through package.json
// "exports" with the react-server condition. ok is false when the package
// has no react-server export, so the caller falls back to esbuild.
func resolveReactServerExport(spec, fromDir string) (string, bool) {
	name, sub := splitPackage(spec)
	if name == "" {
		return "", false
	}
	for dir := fromDir; ; dir = filepath.Dir(dir) {
		pkgDir := filepath.Join(dir, "node_modules", name)
		if b, err := os.ReadFile(filepath.Join(pkgDir, "package.json")); err == nil {
			if !strings.Contains(string(b), `"react-server"`) {
				return "", false
			}
			var pj struct {
				Exports json.RawMessage `json:"exports"`
			}
			if json.Unmarshal(b, &pj) != nil || len(pj.Exports) == 0 {
				return "", false
			}
			exports, err := decodeOrdered(json.NewDecoder(bytes.NewReader(pj.Exports)))
			if err != nil {
				return "", false
			}
			target, ok := matchExports(exports, "."+sub)
			if !ok {
				return "", false
			}
			return filepath.Join(pkgDir, filepath.FromSlash(target)), true
		}
		if parent := filepath.Dir(dir); parent == dir {
			return "", false
		}
	}
}

func splitPackage(spec string) (name, sub string) {
	if spec == "" || spec[0] == '.' || spec[0] == '/' {
		return "", ""
	}
	parts := strings.SplitN(spec, "/", 3)
	if strings.HasPrefix(spec, "@") {
		if len(parts) < 2 {
			return "", ""
		}
		name = parts[0] + "/" + parts[1]
		if len(parts) == 3 {
			sub = "/" + parts[2]
		}
		return name, sub
	}
	name = parts[0]
	if i := strings.IndexByte(spec, '/'); i >= 0 {
		sub = spec[i:]
	}
	return name, sub
}

// ordered is a JSON object that keeps key order: Node matches export
// conditions in package.json order.
type ordered []struct {
	Key   string
	Value any
}

func (o ordered) get(k string) (any, bool) {
	for _, kv := range o {
		if kv.Key == k {
			return kv.Value, true
		}
	}
	return nil, false
}

func decodeOrdered(d *json.Decoder) (any, error) {
	tok, err := d.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			var o ordered
			for d.More() {
				kt, err := d.Token()
				if err != nil {
					return nil, err
				}
				v, err := decodeOrdered(d)
				if err != nil {
					return nil, err
				}
				o = append(o, struct {
					Key   string
					Value any
				}{kt.(string), v})
			}
			_, err := d.Token() // '}'
			return o, err
		case '[':
			var a []any
			for d.More() {
				v, err := decodeOrdered(d)
				if err != nil {
					return nil, err
				}
				a = append(a, v)
			}
			_, err := d.Token() // ']'
			return a, err
		}
	}
	return tok, nil
}

// matchExports implements the package.json "exports" subpath + condition
// algorithm for rscConditions.
func matchExports(exports any, subpath string) (string, bool) {
	// Sugar: a string, array or condition object applies to ".".
	if o, ok := exports.(ordered); ok && len(o) > 0 && strings.HasPrefix(o[0].Key, ".") {
		if t, ok := o.get(subpath); ok {
			return resolveTarget(t, "")
		}
		// Longest wildcard pattern match.
		best, bestKey := "", ""
		for _, kv := range o {
			prefix, suffix, ok := strings.Cut(kv.Key, "*")
			if ok && strings.HasPrefix(subpath, prefix) && strings.HasSuffix(subpath, suffix) &&
				len(subpath) >= len(prefix)+len(suffix) && len(kv.Key) > len(bestKey) {
				bestKey, best = kv.Key, subpath[len(prefix):len(subpath)-len(suffix)]
			}
		}
		if bestKey != "" {
			t, _ := o.get(bestKey)
			return resolveTarget(t, best)
		}
		return "", false
	}
	if subpath != "." {
		return "", false
	}
	return resolveTarget(exports, "")
}

var rscConditionSet = func() map[string]bool {
	m := map[string]bool{}
	for _, c := range rscConditions {
		m[c] = true
	}
	return m
}()

func resolveTarget(t any, star string) (string, bool) {
	switch v := t.(type) {
	case string:
		return strings.ReplaceAll(v, "*", star), true
	case []any:
		for _, e := range v {
			if r, ok := resolveTarget(e, star); ok {
				return r, true
			}
		}
	case ordered:
		for _, kv := range v {
			if rscConditionSet[kv.Key] {
				if r, ok := resolveTarget(kv.Value, star); ok {
					return r, true
				}
			}
		}
	}
	return "", false
}

// importAttrRe matches Turbopack-only import attributes in Next's dist
// files (e.g. `with { 'turbopack-transition': 'next-shared' }`), which
// webpack ignores and esbuild rejects.
var importAttrRe = regexp.MustCompile(`\s+with\s*\{[^{}]*['"]turbopack-[a-z-]+['"][^{}]*\}`)

func stripTurbopackAttributes(src string) string {
	if !strings.Contains(src, "turbopack-") {
		return src
	}
	return importAttrRe.ReplaceAllString(src, "")
}

// cjsTwin maps a next/dist/esm file to its CommonJS twin, which is what the
// browser build uses (Next's flight-manifest plugin does the same mapping).
func cjsTwin(path string) string {
	sep := string(filepath.Separator)
	marker := sep + "next" + sep + "dist" + sep + "esm" + sep
	if i := strings.Index(path, marker); i >= 0 {
		return path[:i] + sep + "next" + sep + "dist" + sep + path[i+len(marker):]
	}
	return path
}
