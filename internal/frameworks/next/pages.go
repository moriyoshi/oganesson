package next

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"

	"github.com/moriyoshi/oganesson/runtime/isr"
)

// The Pages Router. Pages compile the way Next's edge runtime compiles
// them: one edge-ssr template instance per page (next-edge-ssr-loader), in
// the "pages" layer (plain React, no Server Components), with _app,
// _document and _error. edgeSSRPagesPatches give the template next start's
// response semantics (pages-handler.js). The browser gets Next's own pages
// client (next/dist/client/next.js) with every page registered lazily on
// window.__NEXT_P, as next-client-pages-loader entries would.

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// nextNodeOverrides are the Node built-ins the server build takes from
// Next's compiled polyfills instead of unenv.
var nextNodeOverrides = map[string]string{
	"buffer":         "next/dist/compiled/buffer",
	"stream":         "next/dist/compiled/stream-browserify",
	"string_decoder": "next/dist/compiled/string_decoder",
}

// pagesInfo is the project's pages directory.
type pagesInfo struct {
	dir   string
	pages map[string]string // page ("/blog/[slug]", "/404") -> file
	api   map[string]string // "/api/hello" -> file
	// _app, _document and _error: the project's files, or Next's built-ins.
	app, document, errorPage string
	// ssg lists pages exporting getStaticProps (the client's __SSG_MANIFEST).
	ssg map[string]bool
}

const (
	pagesRouteModule = "og:pages-route:" // + page
	pagesErrorPage   = "/_error"
	// API routes: Next's pages-edge-api template around a bridge, which runs
	// the route's handler as an edge handler (config.runtime "edge") or
	// through Next's pages-api template on mocked Node req/res.
	pagesAPIRouteModule  = "og:pages-api-route:"  // + page
	pagesAPIBridgeModule = "og:pages-api-bridge:" // + page
	pagesAPINodeModule   = "og:pages-api:"        // + page
)

// scanPages finds the pages directory (pages or src/pages) and its pages.
func scanPages(dir string, exts []string, probe func(string) ([]string, error)) (*pagesInfo, error) {
	var pagesDir string
	for _, d := range []string{"pages", "src/pages"} {
		if st, err := os.Stat(filepath.Join(dir, d)); err == nil && st.IsDir() {
			pagesDir = filepath.Join(dir, d)
			break
		}
	}
	if pagesDir == "" {
		return nil, nil
	}
	pi := &pagesInfo{dir: pagesDir, pages: map[string]string{}, api: map[string]string{}, ssg: map[string]bool{},
		app: "next/dist/pages/_app", document: "next/dist/pages/_document", errorPage: "next/dist/pages/_error"}
	err := filepath.WalkDir(pagesDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if strings.HasSuffix(p, ".d.ts") {
			return nil
		}
		ext := strings.TrimPrefix(filepath.Ext(p), ".")
		if !slices.Contains(exts, ext) {
			return nil
		}
		rel, _ := filepath.Rel(pagesDir, p)
		page := pageFromPath("/" + filepath.ToSlash(strings.TrimSuffix(rel, "."+ext)))
		switch page {
		case "/_app":
			pi.app = p
		case "/_document":
			pi.document = p
		case pagesErrorPage:
			pi.errorPage = p
		default:
			if page == "/api" || strings.HasPrefix(page, "/api/") {
				pi.api[page] = p
			} else {
				if prev, dup := pi.pages[page]; dup {
					return fmt.Errorf("next: duplicate page %s: %s and %s", page, prev, p)
				}
				pi.pages[page] = p
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	for page, file := range pi.pages {
		names, err := probe(file)
		if err != nil {
			return nil, err
		}
		if slices.Contains(names, "getStaticProps") {
			pi.ssg[page] = true
		}
	}
	return pi, nil
}

// checkAppPageConflicts is next build's check for a URL served by both an
// app entry (page or route handler) and a pages file.
func (b *builder) checkAppPageConflicts(dir string) error {
	if b.appDir == "" || b.pages == nil {
		return nil
	}
	var lines []string
	for _, r := range b.routes {
		if r.Pages {
			continue
		}
		pageFile := b.pages.pages[r.Pathname]
		if pageFile == "" {
			pageFile = b.pages.api[r.Pathname]
		}
		if pageFile == "" {
			continue
		}
		for _, ap := range r.AppPaths {
			appFile := ""
			for _, ext := range b.exts {
				if f := filepath.Join(b.appDir, filepath.FromSlash(ap)+"."+ext); fileExists(f) {
					appFile = f
					break
				}
			}
			if appFile == "" {
				continue
			}
			rp, _ := filepath.Rel(dir, pageFile)
			ra, _ := filepath.Rel(dir, appFile)
			lines = append(lines, fmt.Sprintf("  %q - %q", filepath.ToSlash(rp), filepath.ToSlash(ra)))
		}
	}
	if len(lines) == 0 {
		return nil
	}
	sort.Strings(lines)
	verb := "s were"
	if len(lines) == 1 {
		verb = " was"
	}
	return fmt.Errorf("Conflicting app and page file%s found, please remove the conflicting files to continue:\n%s", verb, strings.Join(lines, "\n"))
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// pageFromPath maps "/blog/index" to "/blog" and "/index" to "/".
func pageFromPath(p string) string {
	if p == "/index" {
		return "/"
	}
	return strings.TrimSuffix(p, "/index")
}

// normalizePagePath ports shared/lib/page-path/normalize-page-path.
func normalizePagePath(page string) string {
	switch {
	case page == "/":
		return "/index"
	case regexp.MustCompile(`^/index(/|$)`).MatchString(page) && !isDynamicSegment(strings.Split(page, "/")[1]):
		return "/index" + page
	}
	return page
}

func routeModuleOptions(page string) string {
	return mustJSON(map[string]any{"definition": map[string]any{
		"kind": "PAGES", "page": normalizePagePath(page), "pathname": page, "bundlePath": "", "filename": "",
	}})
}

// notFoundPage is the page that renders 404s: pages/404, else _error.
func (pi *pagesInfo) notFoundPage() string {
	if _, ok := pi.pages["/404"]; ok {
		return "/404"
	}
	return pagesErrorPage
}

// pagesRoutes lists the routes of the pages directory: one per page and API
// route, plus _error when it renders 404s (internal: never matched by URL).
func (pi *pagesInfo) routes() []Route {
	var out []Route
	for _, page := range sortedKeys(pi.pages) {
		out = append(out, Route{Page: page, Pathname: page, AppPaths: []string{page}, Pages: true})
	}
	for _, page := range sortedKeys(pi.api) {
		out = append(out, Route{Page: page, Pathname: page, AppPaths: []string{page}, Pages: true, IsRoute: true})
	}
	if pi.notFoundPage() == pagesErrorPage {
		out = append(out, Route{Page: pagesErrorPage, Pathname: pagesErrorPage, AppPaths: []string{pagesErrorPage}, Pages: true})
	}
	return out
}

// pagesRouteSource is what next-edge-ssr-loader emits for page.
func (b *builder) pagesRouteSource(page string) (string, error) {
	pi := b.pages
	file := pi.pages[page]
	if page == pagesErrorPage {
		file = pi.errorPage
	}
	if file == "" {
		return "", fmt.Errorf("next: no page %s", page)
	}
	var user500 *string
	if f, ok := pi.pages["/500"]; ok {
		user500 = &f
	}
	src, err := ExpandTemplate(b.nextDir, "edge-ssr", TemplateArgs{
		Replacements: map[string]string{
			"VAR_USERLAND": file, "VAR_DEFINITION_PATHNAME": page,
			"VAR_MODULE_DOCUMENT": pi.document, "VAR_MODULE_APP": pi.app, "VAR_MODULE_GLOBAL_ERROR": pi.errorPage,
		},
		Injections: map[string]string{
			"pageRouteModuleOptions":    routeModuleOptions(page),
			"errorRouteModuleOptions":   routeModuleOptions(pagesErrorPage),
			"user500RouteModuleOptions": routeModuleOptions("/500"),
		},
		Raw:     b.cacheHandlersSetup(),
		Imports: map[string]*string{"userland500Page": user500, "incrementalCacheHandler": ptr(isr.CacheHandlerModule)},
	})
	if err != nil {
		return "", err
	}
	return applyPatches("edge-ssr", src, edgeSSRPagesPatches)
}

// pagesAPIRouteSource is what next-edge-function-loader emits for an API
// route: the pages-edge-api template, whose handler is the bridge.
func (b *builder) pagesAPIRouteSource(page string) (string, error) {
	if b.pages.api[page] == "" {
		return "", fmt.Errorf("next: no API route %s", page)
	}
	return ExpandTemplate(b.nextDir, "pages-edge-api", TemplateArgs{
		Replacements: map[string]string{
			"VAR_USERLAND": pagesAPIBridgeModule + page, "VAR_DEFINITION_PAGE": page, "VAR_DEFINITION_PATHNAME": page,
		},
		Imports: map[string]*string{"incrementalCacheHandler": ptr(isr.CacheHandlerModule)},
	})
}

// pagesAPINodeSource is the pages-api template, which next start runs for
// Node-runtime API routes.
func (b *builder) pagesAPINodeSource(page string) (string, error) {
	return ExpandTemplate(b.nextDir, "pages-api", TemplateArgs{
		Replacements: map[string]string{
			"VAR_USERLAND": b.pages.api[page], "VAR_DEFINITION_PAGE": normalizePagePath(page), "VAR_DEFINITION_PATHNAME": page,
		},
	})
}

// pagesAPIBridgeSource adapts an API route to the edge adapter's handler
// signature. Edge routes (config.runtime) are used as they are; Node routes
// run the pages-api template's handler on Next's own req/res mocks
// (server/lib/mock-request), with the body buffered into the request and the
// response streamed out as the handler writes it.
func (b *builder) pagesAPIBridgeSource(page string) (string, error) {
	file := b.pages.api[page]
	if file == "" {
		return "", fmt.Errorf("next: no API route %s", page)
	}
	return `import * as userland from ` + jsonStr(file) + `;
import { handler as nodeHandler } from ` + jsonStr(pagesAPINodeModule+page) + `;
import { createRequestResponseMocks } from "next/dist/server/lib/mock-request";
import { toNodeOutgoingHttpHeaders } from "next/dist/server/web/utils";
import { Buffer } from "buffer";

const runtime = userland.config && userland.config.runtime;
const isEdge = runtime === "edge" || runtime === "experimental-edge";
const nullBodyStatus = [101, 204, 205, 304];

// res.revalidate: regenerate the page now through the ISR layer, with the
// success check of Next's NextNodeServer.revalidate.
async function revalidate({ urlPath, opts }) {
  const cache = globalThis[Symbol.for("og.cache")];
  if (!cache || typeof cache.revalidatePath !== "function") throw new Error("og: on-demand revalidation is not available here");
  const { status, revalidated } = await cache.revalidatePath(urlPath, Boolean(opts && opts.unstable_onlyGenerated));
  if (!revalidated && status !== 200) throw new Error("Invalid response " + status);
}

async function nodeBridge(request, event) {
  const url = new URL(request.url);
  let body = null;
  if (request.method !== "GET" && request.method !== "HEAD" && request.body) {
    body = Buffer.from(await request.arrayBuffer());
  }
  const { readable, writable } = new TransformStream();
  const writer = writable.getWriter();
  let res;
  const mocks = createRequestResponseMocks({
    url: url.pathname + url.search,
    headers: toNodeOutgoingHttpHeaders(request.headers),
    method: request.method,
    // Like ServerResponse.write: the first write sends the head.
    resWriter: (chunk) => {
      res.headersSent = true;
      if (res.headPromiseResolve) res.headPromiseResolve();
      writer.write(typeof chunk === "string" ? Buffer.from(chunk) : chunk).catch(() => {});
      return true;
    },
  });
  const req = mocks.req;
  res = mocks.res;
  req._read = function () {
    if (body) this.push(body);
    body = null;
    this.push(null);
  };
  res.hasStreamed.then(() => writer.close(), (e) => writer.abort(e)).catch(() => {});
  const done = nodeHandler(req, res, { waitUntil: (p) => event.waitUntil(p), requestMeta: { revalidate } });
  event.waitUntil(done);
  await Promise.race([res.headPromise, done]);
  await res.headPromise;
  const status = res.statusCode;
  const noBody = request.method === "HEAD" || nullBodyStatus.includes(status);
  return new Response(noBody ? null : readable, { status, statusText: res.statusMessage || undefined, headers: res.headers });
}

export default isEdge ? userland.default : nodeBridge;
`, nil
}

// pagesClientEntrySource is the browser entry of the pages: the webpack
// runtime shim, Next's pages client, and every page registered on
// __NEXT_P (evaluated when the router asks for it).
func (b *builder) pagesClientEntrySource() string {
	var sb strings.Builder
	sb.WriteString("import \"og:webpack-runtime\";\nimport \"next/dist/client/next\";\nconst P = (self.__NEXT_P = self.__NEXT_P || []);\n")
	reg := func(page, file string) {
		fmt.Fprintf(&sb, "P.push([%s, () => require(%s)]);\n", jsonStr(page), jsonStr(file))
	}
	reg("/_app", b.pages.app)
	reg(pagesErrorPage, b.pages.errorPage)
	for _, page := range sortedKeys(b.pages.pages) {
		reg(page, b.pages.pages[page])
	}
	return sb.String()
}

// browserPageTransform applies next_ssg to page modules in the pages
// browser bundle.
func (b *builder) browserPageTransform(layer Layer, path, code string) (string, error) {
	if layer != LayerPagesBrowser || b.pages == nil {
		return code, nil
	}
	for _, f := range b.pages.pages {
		if f == path {
			return ssgTransform(path, code)
		}
	}
	return code, nil
}

// pagesManifest computes __BUILD_MANIFEST.pages: per page, the browser
// bundle plus the stylesheets its server import graph reaches (_app's under
// "/_app"), the way Next lists each page's chunks and CSS.
func (b *builder) pagesManifest() (map[string][]string, error) {
	out := map[string][]string{}
	add := func(page, file string) error {
		files := []string{b.pagesClient}
		seen := map[string]bool{}
		var walk func(key string)
		var css []string
		walk = func(key string) {
			if seen[key] {
				return
			}
			seen[key] = true
			if p := keyPath(key); b.isCSS(p) {
				css = append(css, p)
				return
			}
			for _, imp := range b.serverMeta[key] {
				walk(imp)
			}
		}
		walk(string(LayerPages) + ":" + file)
		for _, src := range css {
			outPath, err := b.compileCSS(src)
			if err != nil {
				return err
			}
			if !slices.Contains(files, outPath) {
				files = append(files, outPath)
			}
		}
		out[page] = files
		return nil
	}
	if err := add("/_app", b.pages.app); err != nil {
		return nil, err
	}
	if err := add(pagesErrorPage, b.pages.errorPage); err != nil {
		return nil, err
	}
	for page, file := range b.pages.pages {
		if err := add(page, file); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// clientBuildManifests are static/<buildId>/_buildManifest.js and
// _ssgManifest.js, which the pages client router reads for navigation
// (build-manifest-plugin's generateClientManifest).
func (b *builder) clientBuildManifests(ctx context.Context, pages map[string][]string) (map[string][]byte, error) {
	m := map[string]any{"__rewrites": map[string]any{"afterFiles": []any{}, "beforeFiles": []any{}, "fallback": []any{}}}
	for page, files := range pages {
		m[page] = files
	}
	sorted, err := b.host.SortedRoutes(ctx, sortedKeys(pages))
	if err != nil {
		return nil, err
	}
	m["sortedPages"] = sorted
	ssgJSON, _ := json.Marshal(sortedKeys(b.pages.ssg))
	dir := "static/" + b.buildID + "/"
	return map[string][]byte{
		dir + "_buildManifest.js": []byte("self.__BUILD_MANIFEST = " + mustJSON(m) + ";self.__BUILD_MANIFEST_CB && self.__BUILD_MANIFEST_CB()"),
		dir + "_ssgManifest.js":   []byte("self.__SSG_MANIFEST = new Set(" + string(ssgJSON) + ");self.__SSG_MANIFEST_CB && self.__SSG_MANIFEST_CB()"),
	}, nil
}
