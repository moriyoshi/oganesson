package next

import (
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/moriyoshi/oganesson/runtime/isr"
	"github.com/moriyoshi/oganesson/runtime/nodeshim"
)

// cacheHandlerImport points Next's incrementalCacheHandler at Oganesson's cache.
var cacheHandlerImport = map[string]*string{"incrementalCacheHandler": ptr(isr.CacheHandlerModule)}

func ptr[T any](v T) *T { return &v }

// cacheHandlersSetup is next-edge-ssr-loader's getCacheHandlersSetup: an
// import per next.config cacheHandlers entry, plus the registration that
// hands it to Next (cacheHandlers.setCacheHandler in the SSR templates,
// an edgeCacheHandlers property in edge-app-route). The registrations run
// after initializeCacheHandlers, so a configured kind replaces the
// default handler Oganesson's store backs (useCacheHandlerSource).
func (b *builder) cacheHandlersSetup() map[string]string {
	configured, _ := b.config["cacheHandlers"].(map[string]any)
	kinds := make([]string, 0, len(configured))
	for kind, p := range configured {
		if s, _ := p.(string); s != "" {
			kinds = append(kinds, kind)
		}
	}
	sort.Strings(kinds) // Object.entries order isn't kept by the Go map
	var imports, ssr, route []string
	for i, kind := range kinds {
		p := configured[kind].(string)
		if u, err := url.Parse(p); err == nil && u.Scheme == "file" {
			p = u.Path // resolveCacheHandlerPathToFilesystem
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(b.dir, p)
		}
		v := fmt.Sprintf("edgeCacheHandler_%d", i)
		imports = append(imports, fmt.Sprintf("import %s from %s", v, jsonStr(p)))
		ssr = append(ssr, fmt.Sprintf("  cacheHandlers.setCacheHandler(%s, %s)", jsonStr(kind), v))
		route = append(route, fmt.Sprintf("edgeCacheHandlers[%s] = %s", jsonStr(kind), v))
	}
	return map[string]string{
		"cacheHandlerImports":           strings.Join(imports, "\n"),
		"cacheHandlerRegistration":      strings.Join(ssr, "\n"),
		"edgeCacheHandlersRegistration": strings.Join(route, "\n"),
	}
}

// virtual returns the source of a og: module.
func (b *builder) virtual(path string) (string, error) {
	switch {
	case path == "og:entry":
		return b.entrySource(), nil
	case path == "og:manifests":
		return b.manifestsSource(), nil
	case path == "og:client-registry":
		return b.registrySource(), nil
	case path == actionsModuleID:
		return b.actionsRegistrySource(), nil
	case path == "og:middleware":
		return b.middlewareSource()
	case strings.HasPrefix(path, "og:route:"):
		page := strings.TrimPrefix(path, "og:route:")
		src, err := ExpandTemplate(b.nextDir, "edge-ssr-app", TemplateArgs{
			Replacements: map[string]string{"VAR_USERLAND": "og:app-page:" + page, "VAR_PAGE": page},
			Raw:          b.cacheHandlersSetup(),
			Imports:      cacheHandlerImport,
		})
		if err != nil {
			return "", err
		}
		return applyPatches("edge-ssr-app", src, edgeSSRAppPatches)
	case strings.HasPrefix(path, "og:edge-route:"):
		page := strings.TrimPrefix(path, "og:edge-route:")
		return ExpandTemplate(b.nextDir, "edge-app-route", TemplateArgs{
			Replacements: map[string]string{"VAR_USERLAND": "og:app-route:" + page, "VAR_PAGE": page},
			Raw:          b.cacheHandlersSetup(),
			Imports:      cacheHandlerImport,
		})
	case strings.HasPrefix(path, "og:app-route:"):
		page := strings.TrimPrefix(path, "og:app-route:")
		return b.appRouteSource(page)
	case strings.HasPrefix(path, metadataRouteModule):
		page := strings.TrimPrefix(path, metadataRouteModule)
		m, ok := b.metadataRoutes[page]
		if !ok {
			return "", fmt.Errorf("next: no metadata route %s", page)
		}
		return b.metadataRouteSource(m)
	case strings.HasPrefix(path, metadataImageModule):
		return b.metadataImageSource(strings.TrimPrefix(path, metadataImageModule))
	case strings.HasPrefix(path, pagesRouteModule):
		return b.pagesRouteSource(strings.TrimPrefix(path, pagesRouteModule))
	case strings.HasPrefix(path, pagesAPIRouteModule):
		return b.pagesAPIRouteSource(strings.TrimPrefix(path, pagesAPIRouteModule))
	case strings.HasPrefix(path, pagesAPIBridgeModule):
		return b.pagesAPIBridgeSource(strings.TrimPrefix(path, pagesAPIBridgeModule))
	case strings.HasPrefix(path, pagesAPINodeModule):
		return b.pagesAPINodeSource(strings.TrimPrefix(path, pagesAPINodeModule))
	case strings.HasPrefix(path, "og:app-page:"):
		page := strings.TrimPrefix(path, "og:app-page:")
		return b.appPageSource(page)
	}
	return "", fmt.Errorf("next: unknown virtual module %s", path)
}

// appPageSource is what next-app-loader emits: lazy module getters for the
// tree, then the app-page template.
func (b *builder) appPageSource(page string) (string, error) {
	tree, ok := b.trees[page]
	if !ok {
		return "", fmt.Errorf("next: no tree for %s", page)
	}
	pathname := NormalizeAppPath(page)
	var sb strings.Builder
	for _, d := range tree.Declarations {
		fmt.Fprintf(&sb, "const %s = () => import(%s);\n", d.Var, jsonStr(d.Path))
	}
	tpl, err := ExpandTemplate(b.nextDir, "app-page", TemplateArgs{
		Replacements: map[string]string{"VAR_DEFINITION_PAGE": page, "VAR_DEFINITION_PATHNAME": pathname},
		Injections: map[string]string{
			"tree": tree.Code,
			// Server-side requires resolve through Oganesson's module registry.
			"__next_app_require__":    "(id) => globalThis.__ogRequire(id)",
			"__next_app_load_chunk__": "() => Promise.resolve()",
		},
	})
	if err != nil {
		return "", err
	}
	sb.WriteString(tpl)
	return sb.String(), nil
}

// appRouteSource is what next-app-loader's createAppRouteCode emits.
func (b *builder) appRouteSource(page string) (string, error) {
	file := resolveFile(b.appDir, strings.TrimSuffix(page, "/route")+"/route", prefixed(b.exts))
	userland, filename := file, "route"
	if m, ok := b.metadataRoutes[page]; ok {
		// A metadata file: the userland module is next-metadata-route-loader's output.
		file, userland = m.File, metadataRouteModule+page
		filename, _, _ = strings.Cut(filepath.Base(m.File), ".")
	}
	if file == "" {
		return "", fmt.Errorf("next: can't resolve %s", page)
	}
	output := "undefined"
	if o, ok := b.config["output"].(string); ok {
		output = jsonStr(o)
	}
	return ExpandTemplate(b.nextDir, "app-route", TemplateArgs{
		Replacements: map[string]string{
			"VAR_USERLAND":               userland,
			"VAR_DEFINITION_PAGE":        page,
			"VAR_DEFINITION_PATHNAME":    NormalizeAppPath(page),
			"VAR_DEFINITION_FILENAME":    filename,
			"VAR_DEFINITION_BUNDLE_PATH": "app" + page,
			"VAR_RESOLVED_PAGE_PATH":     file,
		},
		Injections: map[string]string{"nextConfigOutput": output},
	})
}

func prefixed(exts []string) []string {
	out := make([]string, len(exts))
	for i, e := range exts {
		out[i] = "." + e
	}
	return out
}

// manifestsSource sets the self.__* globals Next's edge runtime reads at
// module load. The entry imports it first so it evaluates before any route.
func (b *builder) manifestsSource() string {
	clients := b.sortedClients()
	clientModules := map[string]any{}
	ssrMapping := map[string]any{}
	for _, m := range clients {
		ref := map[string]any{"id": m.ID, "name": "*", "chunks": []string{}, "async": false}
		clientModules[m.Path] = ref
		ssrMapping[m.ID] = map[string]any{"*": ref}
	}
	rscManifest := map[string]any{
		"moduleLoading":        map[string]any{"prefix": b.assetPrefix() + "/_next/", "crossOrigin": nil},
		"clientModules":        clientModules,
		"ssrModuleMapping":     ssrMapping,
		"edgeSSRModuleMapping": ssrMapping,
		"rscModuleMapping":     map[string]any{},
		"edgeRscModuleMapping": map[string]any{},
		"entryCSSFiles":        b.entryCSSFiles,
		"entryJSFiles":         map[string]any{},
	}
	perPage := map[string]any{}
	for _, r := range b.routes {
		perPage[r.Page] = rscManifest
	}
	serverFiles := map[string]any{
		"version": 1, "config": b.config, "appDir": b.appDir, "relativeAppDir": strings.TrimPrefix(strings.TrimPrefix(b.appDir, b.dir), "/"),
		"files": []string{}, "ignore": []string{},
	}
	rootMain, pagesMap, lowPriority := []string{}, map[string]any{"/_app": []string{}}, []string{}
	if b.clientMain != "" {
		rootMain = []string{b.clientMain} // the app browser entry; app-render bootstraps hydration with it
	}
	if b.pagesFiles != nil {
		for page, files := range b.pagesFiles {
			pagesMap[page] = files
		}
		// The pages client router reads these for navigation.
		lowPriority = []string{"static/" + b.buildID + "/_buildManifest.js", "static/" + b.buildID + "/_ssgManifest.js"}
	}
	buildManifest := map[string]any{
		"devFiles": []string{}, "ampDevFiles": []string{}, "polyfillFiles": []string{}, "lowPriorityFiles": lowPriority,
		"rootMainFiles": rootMain, "rootMainFilesTree": map[string]any{}, "pages": pagesMap,
	}
	serverActions := b.serverActionsManifest()
	fontApp := map[string]any{}
	for k, v := range b.fontManifest {
		fontApp[k] = v
	}
	font := map[string]any{"pages": map[string]any{}, "app": fontApp, "appUsingSizeAdjust": b.appUsingSizeAdjust, "pagesUsingSizeAdjust": false}

	var sb strings.Builder
	fmt.Fprintf(&sb, "self.__SERVER_FILES_MANIFEST = %s;\n", mustJSON(serverFiles))
	fmt.Fprintf(&sb, "self.__BUILD_MANIFEST = %s;\n", mustJSON(buildManifest))
	fmt.Fprintf(&sb, "self.__RSC_MANIFEST = %s;\n", mustJSON(perPage))
	fmt.Fprintf(&sb, "self.__RSC_SERVER_MANIFEST = %s;\n", jsonStr(mustJSON(serverActions)))
	fmt.Fprintf(&sb, "self.__NEXT_FONT_MANIFEST = %s;\n", jsonStr(mustJSON(font)))
	sb.WriteString("self.__REACT_LOADABLE_MANIFEST = \"{}\";\n")
	sb.WriteString("self.__INTERCEPTION_ROUTE_REWRITE_MANIFEST = \"[]\";\n")
	return sb.String()
}

// registrySource maps React's client reference ids to the SSR-layer
// instances of the client modules (globalThis.__ogRequire). Each loads when
// a render first references it, as __next_require__ loads it under next
// start: a client module that throws when it loads (@react-three/fiber 8
// under React 19) fails the pages that use it, not every page. A module
// is seen as its namespace, as an import of it would be.
func (b *builder) registrySource() string {
	var table strings.Builder
	for _, m := range b.sortedClients() {
		fmt.Fprintf(&table, "  %s: lazyNamespace(() => require(%s)),\n", jsonStr(m.ID), jsonStr(m.Path))
	}
	return `function lazyNamespace(load) {
  let ns, err, done = false;
  return () => {
    if (!done) {
      done = true;
      try {
        ns = namespaceOf(load());
      } catch (e) {
        err = e;
      }
    }
    if (err) throw err;
    return ns;
  };
}
// An ES module's require() is its namespace; a CommonJS module's
// module.exports becomes its default export, its properties the named ones
// (esbuild's __toESM).
function namespaceOf(m) {
  if (m && m.__esModule) return m;
  const ns = { default: m };
  if (m && (typeof m === "object" || typeof m === "function")) {
    for (const k of Object.getOwnPropertyNames(m)) {
      if (k !== "default") Object.defineProperty(ns, k, { get: () => m[k], enumerable: true });
    }
  }
  return ns;
}
export const modules = {
` + table.String() + "};\n"
}

// entrySource dispatches requests to routes, standing in for Next's router
// server and edge sandbox.
func (b *builder) entrySource() string {
	routes := slicesSortedRoutes(b.routes)
	var imports, table strings.Builder
	// Node globals first (webpack's ProvidePlugin supplies Buffer on the edge),
	// then the manifests that route modules read at load time.
	fmt.Fprintf(&imports, "import %s;\n", jsonStr(nodeshim.GlobalsModule))
	imports.WriteString("import \"og:manifests\";\nimport { modules } from \"og:client-registry\";\n")
	fmt.Fprintf(&imports, "import { actions } from %s;\n", jsonStr(actionsModuleID))
	if b.pages != nil {
		imports.WriteString("import { buildPagesStaticPaths } from \"next/dist/build/static-paths/pages\";\n")
		imports.WriteString("import { isBot } from \"next/dist/shared/lib/router/utils/is-bot\";\n")
		imports.WriteString("import { normalizeLocalePath } from \"next/dist/shared/lib/i18n/normalize-locale-path\";\n")
		imports.WriteString("import { getLocaleRedirect } from \"next/dist/shared/lib/i18n/get-locale-redirect\";\n")
		imports.WriteString("import { detectDomainLocale } from \"next/dist/shared/lib/i18n/detect-domain-locale\";\n")
	} else {
		imports.WriteString("const buildPagesStaticPaths = null, isBot = () => false, normalizeLocalePath = null, getLocaleRedirect = null, detectDomainLocale = null;\n")
	}
	imports.WriteString("import { isNonHtmlSecFetchDest } from \"next/dist/server/lib/is-non-html-sec-fetch-dest\";\n")
	mwPage, mwMatchers := "null", "[]"
	if b.middleware != nil {
		imports.WriteString("import * as middlewareMod from \"og:middleware\";\n")
		imports.WriteString("import { getMiddlewareRouteMatcher } from \"next/dist/shared/lib/router/utils/middleware-route-matcher\";\n")
		mwPage, mwMatchers = jsonStr(b.middleware.Page), mustJSON(b.middleware.Matchers)
	} else {
		imports.WriteString("const middlewareMod = null, getMiddlewareRouteMatcher = () => () => false;\n")
	}
	// "use cache" (Cache Components): Next's default cache handler, its
	// entries and tags kept in og.cache's store (runtime/isr/usecache.go)
	// instead of this engine's memory, so every request's engine shares
	// them. Its checks are the default handler's (lib/cache-handlers/
	// default.js, incremental-cache/tags-manifest.external.js).
	imports.WriteString(useCacheHandlerSource)
	// lazyModule loads a module once; a load that throws throws again on
	// every use, as Node's require (esbuild's lazy init would not).
	imports.WriteString(`function lazyModule(load) {
  let mod, err, done = false;
  return () => {
    if (!done) {
      done = true;
      try {
        mod = load();
      } catch (e) {
        err = e;
      }
    }
    if (err) throw err;
    return mod;
  };
}
`)
	for i, r := range routes {
		re, keys := routeRegex(r.Pathname)
		mod := "og:route:" + r.Page
		switch {
		case r.Pages && r.IsRoute:
			mod = pagesAPIRouteModule + r.Page
		case r.Pages:
			mod = pagesRouteModule + r.Page
		case r.IsRoute:
			mod = "og:edge-route:" + r.Page
		}
		// Each route's code loads when the route is first used, as next
		// start loads it: a module that throws when it loads (with-sentry's
		// API route) fails its own route only.
		fmt.Fprintf(&imports, "const r%d = lazyModule(() => require(%s));\n", i, jsonStr(mod))
		// _error renders 404s and errors; it has no URL of its own.
		fmt.Fprintf(&table, "  { page: %s, pathname: %s, re: new RegExp(%s), keys: %s, get mod() { return r%d(); }, pages: %t, api: %t, internal: %t },\n",
			jsonStr(r.Page), jsonStr(r.Pathname), jsonStr(re), mustJSON(keys), i, r.Pages, r.Pages && r.IsRoute, r.Pages && r.Page == pagesErrorPage)
	}
	notFoundPage := notFoundRouteEntry
	if b.appDir == "" && b.pages != nil {
		notFoundPage = b.pages.notFoundPage()
	}
	nc := map[string]any{
		"basePath":      b.config["basePath"],
		"i18n":          b.config["i18n"],
		"trailingSlash": b.config["trailingSlash"],
	}
	if exp, ok := b.config["experimental"].(map[string]any); ok {
		nc["experimental"] = map[string]any{
			"cacheLife":                 exp["cacheLife"],
			"authInterrupts":            exp["authInterrupts"],
			"clientParamParsingOrigins": exp["clientParamParsingOrigins"],
		}
	}
	return imports.String() + `
globalThis.__ogRequire = (id) => {
  if (id === ` + jsonStr(actionsModuleID) + `) return actions;
  const m = modules[id];
  if (!m) throw new Error("og: unknown module id " + id);
  return m();
};
const routes = [
` + table.String() + `];
const notFound = routes.find((r) => r.page === ` + jsonStr(notFoundPage) + `);
// Pages data requests: /_next/data/<buildId>/<page>.json (Next's router
// server maps them to the page; RouteModule.prepare recognizes the URL).
const DATA_PREFIX = "/_next/data/" + ` + jsonStr(b.buildID) + ` + "/";
function dataPage(pathname) {
  if (!pathname.startsWith(DATA_PREFIX) || !pathname.endsWith(".json")) return null;
  const p = "/" + pathname.slice(DATA_PREFIX.length, -".json".length);
  return p === "/index" ? "/" : p;
}
const nextConfig = ` + mustJSON(nc) + `;

// i18n (Pages Router only): pages match the path without its locale
// prefix, which the route module's prepare reads back from the URL; API
// routes and app routes are not localized (router-server).
const i18n = normalizeLocalePath && nextConfig.i18n ? nextConfig.i18n : null;
function delocalize(pathname) {
  return i18n ? normalizeLocalePath(pathname, i18n.locales) : { pathname, detectedLocale: undefined };
}
// canonicalPath is a page path's URL with the default locale unprefixed
// ("/en/posts/1" and "/posts/1" are one page), for comparing static paths.
// addTrailingSlash puts a path in nextConfig.trailingSlash's canonical form:
// a non-root HTML path ends with a slash when trailingSlash is set.
const TSLASH = !!nextConfig.trailingSlash;
function addTrailingSlash(p) {
  return TSLASH && p !== "/" && !p.endsWith("/") ? p + "/" : p;
}
function canonicalPath(pathname) {
  if (!i18n) return addTrailingSlash(pathname);
  const { pathname: p, detectedLocale } = delocalize(pathname);
  const locale = detectedLocale || i18n.defaultLocale;
  return addTrailingSlash(locale === i18n.defaultLocale ? p : "/" + locale + (p === "/" ? "" : p));
}

// dataPath is the /_next/data URL the client requests for a page path:
// with i18n always locale-prefixed ("/en.json", "/fr/about.json").
function dataPath(pathname) {
  if (i18n) {
    const { pathname: p, detectedLocale } = delocalize(pathname);
    pathname = "/" + (detectedLocale || i18n.defaultLocale) + (p === "/" ? "" : p);
  }
  // The data URL has no trailing slash even under trailingSlash (next start:
  // /about/ -> /_next/data/<id>/about.json).
  const base = pathname === "/" ? "/" : pathname.replace(/\/+$/, "") || "/";
  return DATA_PREFIX + (base === "/" ? "index" : base.slice(1)) + ".json";
}

// basePath: every URL of the app is under it (router-server); the routes
// match the path without it, and a path outside it is a 404. withBase
// gives a route path's URL.
const BASE = nextConfig.basePath || "";
function stripBase(pathname) {
  if (!BASE) return pathname;
  if (pathname === BASE) return "/";
  return pathname.startsWith(BASE + "/") ? pathname.slice(BASE.length) : null;
}
function withBase(pathname) {
  return BASE && pathname === "/" ? BASE : BASE + pathname;
}

// trailingSlashRedirect returns the canonical slash form of a request
// pathname to 308-redirect to, enforcing nextConfig.trailingSlash as next
// start does, or null when the path is already canonical. Skips /_next/
// (assets and data), file requests (a dot in the last segment, which covers
// the .json data URLs), and the root (and the basePath root).
function trailingSlashRedirect(p) {
  // The true root "/" never takes a slash; the basePath root ("/docs") is a
  // normal path (next start redirects /docs -> /docs/ under trailingSlash).
  if (p === "/") return null;
  if (p.startsWith("/_next/") || (BASE && p.startsWith(BASE + "/_next/"))) return null;
  if (p.slice(p.lastIndexOf("/") + 1).includes(".")) return null;
  const noTrail = p.replace(/\/+$/, "") || "/";
  const canon = nextConfig.trailingSlash && noTrail !== "/" ? noTrail + "/" : noTrail;
  return canon !== p ? canon : null;
}

function match(pathname, pagesOnly) {
  const pagePath = delocalize(pathname).pathname;
  for (const r of routes) {
    if (r === notFound || r.internal || (pagesOnly && (!r.pages || r.api))) continue;
    const m = r.re.exec(r.pages && !r.api ? pagePath : pathname);
    if (!m) continue;
    const params = {};
    r.keys.forEach((k, i) => {
      const v = m[i + 1];
      if (v === undefined) return;
      params[k.name] = k.catchAll ? v.split("/").map(decodeURIComponent) : decodeURIComponent(v);
    });
    return { route: r, params };
  }
  return { route: notFound, params: {}, notFound: true };
}

const middlewarePage = ` + mwPage + `;
const middlewareMatches = getMiddlewareRouteMatcher(` + mwMatchers + `);

function plainHeaders(headers) {
  const out = {};
  headers.forEach((v, k) => {
    if (k === "set-cookie") return;
    out[k] = v;
  });
  return out;
}

function invoke(mod, page, request, url, params, extra) {
  return mod.default({
    request: {
      url,
      method: request.method,
      headers: plainHeaders(request.headers),
      nextConfig,
      page: { name: page, params },
      body: request.body ?? undefined,
      signal: request.signal,
      waitUntil: extra.waitUntil,
    },
  });
}

const internalHeader = /^x-middleware-/;

// runMiddleware mirrors the router server's handling of the proxy/middleware
// response: next(), rewrite(), redirects and direct responses.
// localeRedirect is router-server's locale detection for "/": the
// NEXT_LOCALE cookie, Accept-Language, domain locales (getLocaleRedirect).
// Like router-server it runs ahead of the page cache.
function localeRedirect(request) {
  if (!i18n || i18n.localeDetection === false) return null;
  const url = new URL(request.url);
  let urlNoQuery = url.pathname;
  if (nextConfig.basePath && urlNoQuery.startsWith(nextConfig.basePath)) urlNoQuery = urlNoQuery.slice(nextConfig.basePath.length) || "/";
  const pathLocale = delocalize(urlNoQuery).detectedLocale;
  const domainLocale = detectDomainLocale(i18n.domains, url.hostname);
  const defaultLocale = (domainLocale && domainLocale.defaultLocale) || i18n.defaultLocale;
  return getLocaleRedirect({
    defaultLocale,
    domainLocale,
    headers: plainHeaders(request.headers),
    nextConfig,
    pathLocale,
    urlParsed: { pathname: pathLocale ? "/" + pathLocale + urlNoQuery : urlNoQuery, query: Object.fromEntries(url.searchParams), search: url.search },
  });
}

async function runMiddleware(request, waitUntil) {
  const redirect = localeRedirect(request);
  if (redirect) return { request, responseHeaders: null, response: new Response(redirect, { status: 307, headers: { Location: redirect } }) };
  const url = new URL(request.url);
  const query = Object.fromEntries(url.searchParams);
  if (!middlewareMod || !middlewareMatches(url.pathname, { headers: plainHeaders(request.headers) }, query)) {
    return { request, responseHeaders: null };
  }
  // Both middleware and the route may read the body.
  let body = null;
  if (request.method !== "GET" && request.method !== "HEAD" && request.body) {
    body = new Uint8Array(await request.arrayBuffer());
  }
  const withBody = (r, u, headers) => new Request(u ?? r.url, { method: r.method, headers: headers ?? r.headers, body, signal: r.signal, redirect: "manual" });
  const result = await invoke(middlewareMod, middlewarePage, withBody(request), request.url, {}, { waitUntil });
  if (result.waitUntil) waitUntil(result.waitUntil);
  const res = result.response;
  const rewrite = res.headers.get("x-middleware-rewrite");
  if (!res.headers.has("x-middleware-next") && !rewrite) {
    // Redirect or a complete response: strip internal headers and return.
    const headers = new Headers();
    res.headers.forEach((v, k) => { if (!internalHeader.test(k)) headers.append(k, v); });
    for (const c of res.headers.getSetCookie()) headers.append("set-cookie", c);
    return { response: new Response(res.body, { status: res.status, statusText: res.statusText, headers }) };
  }
  let headers = request.headers;
  const override = res.headers.get("x-middleware-override-headers");
  if (override !== null) {
    headers = new Headers();
    for (const k of override.split(",").filter(Boolean)) {
      const v = res.headers.get("x-middleware-request-" + k);
      if (v !== null) headers.set(k, v);
    }
  }
  const responseHeaders = new Headers();
  res.headers.forEach((v, k) => { if (!internalHeader.test(k) && k !== "set-cookie" && k !== "content-length" && k !== "content-type") responseHeaders.append(k, v); });
  for (const c of res.headers.getSetCookie()) responseHeaders.append("set-cookie", c);
  const target = rewrite ? new URL(rewrite, request.url) : url;
  if (rewrite && target.origin !== url.origin) {
    return { response: await fetch(withBody(request, target.href, headers)), responseHeaders };
  }
  return { request: withBody(request, target.href, headers), responseHeaders, originalURL: request.url };
}

// pagesStaticPaths runs a dynamic pages route's getStaticPaths through
// Next's buildPagesStaticPaths (validation, path strings, i18n): its
// prerendered routes and FallbackMode, or null without getStaticPaths.
async function pagesStaticPaths(route) {
  const userland = route.mod.ComponentMod && route.mod.ComponentMod.routeModule.userland;
  if (!userland || typeof userland.getStaticPaths !== "function") return null;
  return buildPagesStaticPaths({
    page: route.page,
    getStaticPaths: userland.getStaticPaths,
    configFileName: "next.config.js",
    locales: nextConfig.i18n ? nextConfig.i18n.locales : undefined,
    defaultLocale: nextConfig.i18n ? nextConfig.i18n.defaultLocale : undefined,
  });
}

// staticPathsOf is what the build recorded for a dynamic pages route
// (og.cache.staticPaths): { fallback, paths }, or null.
function staticPathsOf(page) {
  const cache = globalThis[Symbol.for("og.cache")];
  if (!cache || typeof cache.staticPaths !== "function") return null;
  const json = cache.staticPaths(page);
  return json ? JSON.parse(json) : null;
}

// staticParams evaluates generateStaticParams along a page's loader tree,
// top-down: each layout/page runs once per parent param set, receiving
// { params }, and its results extend those params (Next's semantics).
async function staticParams(page) {
  const route = routes.find((r) => r.page === page);
  if (!route || !route.mod.ComponentMod) return [];
  const userland = route.mod.ComponentMod.routeModule.userland;
  if (!userland.loaderTree) {
    // A route handler: its own generateStaticParams, as for an app route.
    const gen = userland.generateStaticParams;
    return typeof gen === "function" ? (await gen({ params: {} })) ?? [] : [];
  }
  let node = userland.loaderTree;
  const getters = [];
  while (node) {
    const [, parallel, modules] = node;
    if (modules.layout) getters.push(modules.layout[0]);
    if (modules.page) getters.push(modules.page[0]);
    node = parallel.children;
  }
  let sets = [{}];
  let generated = false;
  for (const get of getters) {
    const mod = await get();
    if (typeof mod.generateStaticParams !== "function") continue;
    generated = true;
    const next = [];
    for (const params of sets) {
      for (const p of (await mod.generateStaticParams({ params })) ?? []) next.push({ ...params, ...p });
    }
    sets = next;
  }
  // Without any generateStaticParams the route has no build-time paths
  // (Next renders it on request), not one with no params.
  return generated ? sets : [];
}

// Pipeline phases requested by the Go ISR layer for GET/HEAD requests:
// "middleware" returns the proxy's decision only, "route" renders without
// running the proxy again. Without a phase header the whole pipeline runs.
const PHASE = ` + jsonStr(isr.PhaseHeader) + `;

globalThis.__ogHandler = async (incoming) => {
  const paramsFor = incoming.headers.get(` + jsonStr(isr.StaticParamsHeader) + `);
  if (paramsFor) {
    const route = routes.find((r) => r.page === paramsFor);
    if (route && route.pages) {
      const sp = await pagesStaticPaths(route);
      if (!sp) return Response.json([]);
      // The paths themselves (with their locale; the default unprefixed).
      return Response.json(sp.prerenderedRoutes.map((r) => canonicalPath(r.encodedPathname || r.pathname)), {
        headers: { ` + jsonStr(isr.FallbackHeader) + `: JSON.stringify(sp.fallbackMode) },
      });
    }
    return Response.json(await staticParams(paramsFor));
  }
  // Where the cache keeps a URL (isr.MatchHeader): one no app route
  // matches is the not-found page, which Next serves from its single
  // /_not-found cache entry. Pages Router 404s render uncached anyway.
  if (incoming.headers.get(` + jsonStr(isr.MatchHeader) + `)) {
    const routePath = stripBase(new URL(incoming.url).pathname);
    const unmatched = routePath === null || (dataPage(routePath) === null && !routePath.startsWith("/_next/") && match(routePath, false).notFound);
    const headers = new Headers();
    if (unmatched && !notFound.pages) headers.set(` + jsonStr(isr.CachePathHeader) + `, withBase("/_not-found"));
    return new Response(null, { status: 204, headers });
  }
  const phase = incoming.headers.get(PHASE);
  if (phase === "middleware") return middlewareDecision(incoming);
  // A static render attempt that fails (typically Next's dynamic-usage
  // bailout) is reported quietly; the Go ISR layer then renders dynamically.
  if (incoming.headers.get(` + jsonStr(isr.StaticHeader) + `) === "1") {
    try {
      return await handle(incoming, phase === "route");
    } catch {
      return new Response(null, { status: 503 });
    }
  }
  return handle(incoming, phase === "route");
};

async function middlewareDecision(incoming) {
  const pending = [];
  const mw = await runMiddleware(incoming, (p) => pending.push(p));
  for (const p of pending) Promise.resolve(p).catch((e) => console.error("waitUntil:", e));
  if (mw.response) {
    const headers = new Headers(mw.response.headers);
    if (mw.responseHeaders) {
      mw.responseHeaders.forEach((v, k) => { if (k !== "set-cookie") headers.set(k, v); });
      for (const c of mw.responseHeaders.getSetCookie()) headers.append("set-cookie", c);
    }
    headers.set(` + jsonStr(isr.MiddlewareHeader) + `, "response");
    return new Response(mw.response.body, { status: mw.response.status, statusText: mw.response.statusText, headers });
  }
  const headers = new Headers({ [` + jsonStr(isr.MiddlewareHeader) + `]: "continue", [` + jsonStr(isr.MiddlewareURLHeader) + `]: mw.request.url });
  if (mw.request.headers !== incoming.headers) {
    const req = [];
    mw.request.headers.forEach((v, k) => req.push([k, v]));
    headers.set(` + jsonStr(isr.MiddlewareRequestHeadersHeader) + `, JSON.stringify(req));
  }
  if (mw.responseHeaders) {
    const res = [];
    mw.responseHeaders.forEach((v, k) => { if (k !== "set-cookie") res.push([k, v]); });
    for (const c of mw.responseHeaders.getSetCookie()) res.push(["set-cookie", c]);
    headers.set(` + jsonStr(isr.MiddlewareResponseHeadersHeader) + `, JSON.stringify(res));
  }
  return new Response(null, { status: 204, headers });
}

// renderPagesNotFound renders the pages 404 page (pages/404, else _error)
// with status 404. cacheControl, when set, is the cache metadata of the page
// that answered notFound (pages-handler's notFoundRevalidate).
async function renderPagesNotFound(request, url, waitUntil, cacheControl) {
  // Subresource requests (images, fonts, ...) get a plain 404 (base-server).
  if ((request.method === "GET" || request.method === "HEAD") && isNonHtmlSecFetchDest(request.headers.get("sec-fetch-dest"))) {
    return new Response("Not Found", { status: 404, headers: { "Content-Type": "text/plain; charset=utf-8", "Cache-Control": "private, no-cache, no-store, max-age=0, must-revalidate" } });
  }
  const headers = new Headers(request.headers);
  headers.set(` + jsonStr(pagesStatusHeader) + `, "404");
  headers.delete("x-nextjs-data");
  const req = new Request(request.url, { method: "GET", headers });
  const result = await invoke(notFound.mod, notFound.page, req, url.href, {}, { waitUntil });
  if (result.waitUntil) waitUntil(result.waitUntil);
  const res = result.response;
  const out = new Headers(res.headers);
  // Only a page's notFound (with its cache metadata) is cacheable; other
  // 404s render per request, so arbitrary URLs never fill the cache.
  out.delete(` + jsonStr(isr.CacheControlHeader) + `);
  if (!cacheControl) out.set("Cache-Control", "private, no-cache, no-store, max-age=0, must-revalidate");
  if (cacheControl) out.set(` + jsonStr(isr.CacheControlHeader) + `, cacheControl);
  return new Response(res.body, { status: 404, statusText: "Not Found", headers: out });
}

async function handle(incoming, skipMiddleware) {
  const pending = [];
  const waitUntil = (p) => pending.push(p);
  // trailingSlash: redirect to the canonical slash form (308), as next start
  // does, ahead of middleware. The build only ever requests canonical paths
  // (so a prerender/static attempt is never redirected); a non-canonical path
  // reaches here only at request time. GET/HEAD only.
  if (incoming.method === "GET" || incoming.method === "HEAD") {
    const tsURL = new URL(incoming.url);
    const canon = trailingSlashRedirect(tsURL.pathname);
    if (canon) return new Response(null, { status: 308, headers: { Location: canon + tsURL.search } });
  }
  const mw = skipMiddleware ? { request: incoming, responseHeaders: null } : await runMiddleware(incoming, waitUntil);
  let res = mw.response;
  if (!res) {
    let request = mw.request;
    const url = new URL(request.url);
    const routePath = stripBase(url.pathname);
    const data = routePath === null ? null : dataPage(routePath);
    // Data requests for another build: the client then hard-navigates.
    if (data === null && routePath !== null && routePath.startsWith("/_next/data/")) return new Response("Not Found", { status: 404 });
    let { route, params, notFound: unmatched } = routePath === null ? { route: notFound, params: {}, notFound: true } : match(data ?? routePath, data !== null);
    if (data !== null) {
      if (unmatched) return new Response("Not Found", { status: 404 });
      const headers = new Headers(request.headers);
      headers.set("x-nextjs-data", "1");
      request = new Request(request, { headers });
    }
    // API routes are never statically generated.
    if (route.api && incoming.headers.get(` + jsonStr(isr.StaticHeader) + `) === "1") return new Response(null, { status: 503 });
    // Paths a getStaticPaths page did not prerender (pages-handler):
    //   - fallback: false: 404s (NoFallbackError);
    //   - fallback: true: HTML requests from browsers get the fallback shell,
    //     never cached (a static attempt bails out); the client's data
    //     request then renders the page, which caches its HTML too. Data
    //     requests and bots render the page, as for "blocking".
    if (route.pages && !route.api && !unmatched && route.keys.length > 0) {
      const sp = staticPathsOf(route.page);
      if (sp !== null && !sp.paths.map(canonicalPath).includes(canonicalPath(data ?? routePath))) {
        if (sp.fallback === "NOT_FOUND") {
          if (data !== null) return new Response("Not Found", { status: 404 });
          ({ route, params, notFound: unmatched } = { route: notFound, params: {}, notFound: true });
        } else if (sp.fallback === "PRERENDER" && data === null && !isBot(request.headers.get("user-agent") || "")) {
          if (incoming.headers.get(` + jsonStr(isr.StaticHeader) + `) === "1") return new Response(null, { status: 503 });
          const headers = new Headers(request.headers);
          headers.set(` + jsonStr(pagesFallbackHeader) + `, "1");
          request = new Request(request, { headers });
        }
      }
    }
    // Status pages visited directly answer with their status (base-server):
    // pages/404 is only matched by URL when the app's not-found renders 404s.
    if (route.pages && (route.page === "/404" || route.page === "/500") && data === null) {
      if (route.page === "/404" && isNonHtmlSecFetchDest(request.headers.get("sec-fetch-dest"))) return renderPagesNotFound(request, url, waitUntil, null);
      const headers = new Headers(request.headers);
      headers.set(` + jsonStr(pagesStatusHeader) + `, route.page.slice(1));
      request = new Request(request, { headers });
    }
    if (unmatched && (request.method === "GET" || request.method === "HEAD") && isNonHtmlSecFetchDest(request.headers.get("sec-fetch-dest"))) {
      // Subresource requests (images, fonts, ...) for unknown URLs get a
      // plain 404 instead of the not-found page (router-server).
      res = new Response("Not Found", { status: 404, headers: { "Content-Type": "text/plain; charset=utf-8", "Cache-Control": "private, no-cache, no-store, max-age=0, must-revalidate" } });
    } else if (unmatched && notFound.pages) {
      res = await renderPagesNotFound(request, url, waitUntil, null);
    } else {
    // Like Next's runEdgeFunction: route params travel in the query string
    // with the nxtP prefix; the adapter normalizes and strips them.
    const target = new URL(url);
    for (const [k, v] of Object.entries(params)) {
      for (const item of Array.isArray(v) ? v : [v]) target.searchParams.append("nxtP" + k, item);
    }
    const result = await invoke(route.mod, route.page, request, target.href, params, { waitUntil });
    if (result.waitUntil) waitUntil(result.waitUntil);
    res = result.response;
    // Like Next's router server: an unmatched URL renders /_not-found as a 404.
    if (unmatched && res.status === 200) {
      res = new Response(res.body, { status: 404, statusText: "Not Found", headers: res.headers });
    }
    // The companion of an SSG render is the other URL of the page: its
    // data JSON for HTML, its HTML for data.
    if (route.pages && res.headers.has(` + jsonStr(isr.CompanionHeader) + `)) {
      const headers = new Headers(res.headers);
      headers.set(` + jsonStr(isr.CompanionPathHeader) + `, withBase(data !== null ? canonicalPath(data) : dataPath(routePath)));
      res = new Response(res.body, { status: res.status, statusText: res.statusText, headers });
    }
    // A page's notFound renders the pages 404 page (pages-handler's render404).
    if (route.pages && res.headers.get(` + jsonStr(pagesNotFoundHeader) + `) === "1") {
      res = await renderPagesNotFound(request, url, waitUntil, res.headers.get(` + jsonStr(isr.CacheControlHeader) + `));
    }
    }
  }
  for (const p of pending) Promise.resolve(p).catch((e) => console.error("waitUntil:", e));
  if (mw.responseHeaders) {
    const headers = new Headers(res.headers);
    mw.responseHeaders.forEach((v, k) => { if (k !== "set-cookie") headers.set(k, v); });
    for (const c of mw.responseHeaders.getSetCookie()) headers.append("set-cookie", c);
    res = new Response(res.body, { status: res.status, statusText: res.statusText, headers });
  }
  return res;
}
`
}

type routeKey struct {
	Name     string `json:"name"`
	CatchAll bool   `json:"catchAll"`
	Optional bool   `json:"optional"`
}

var dynSeg = regexp.MustCompile(`^\[(\[)?(\.\.\.)?([^\]]+)\]?\]$`)

// routeRegex compiles a Next pathname pattern ("/blog/[slug]") to a regexp.
func routeRegex(pathname string) (string, []routeKey) {
	if pathname == "/" {
		return `^/?$`, []routeKey{}
	}
	var re strings.Builder
	keys := []routeKey{}
	re.WriteString("^")
	for _, seg := range strings.Split(strings.Trim(pathname, "/"), "/") {
		m := dynSeg.FindStringSubmatch(seg)
		if m == nil {
			re.WriteString("/" + regexp.QuoteMeta(seg))
			continue
		}
		optional, catchAll, name := m[1] == "[", m[2] == "...", m[3]
		keys = append(keys, routeKey{Name: name, CatchAll: catchAll, Optional: optional})
		switch {
		case catchAll && optional:
			re.WriteString("(?:/(.+?))?")
		case catchAll:
			re.WriteString("/(.+?)")
		default:
			re.WriteString("/([^/]+?)")
		}
	}
	re.WriteString("/?$")
	return re.String(), keys
}

// slicesSortedRoutes orders routes like Next's getSortedRoutes: static
// segments before dynamic, dynamic before catch-all.
func slicesSortedRoutes(in []Route) []Route {
	out := append([]Route(nil), in...)
	rank := func(p string) []int {
		var r []int
		for _, s := range strings.Split(strings.Trim(p, "/"), "/") {
			switch {
			case strings.HasPrefix(s, "[[..."):
				r = append(r, 3)
			case strings.HasPrefix(s, "[..."):
				r = append(r, 2)
			case strings.HasPrefix(s, "["):
				r = append(r, 1)
			default:
				r = append(r, 0)
			}
		}
		return r
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := rank(out[i].Pathname), rank(out[j].Pathname)
		for k := 0; k < len(a) && k < len(b); k++ {
			if a[k] != b[k] {
				return a[k] < b[k]
			}
		}
		return len(a) > len(b)
	})
	return out
}

const useCacheHandlerSource = `(() => {
  const store = globalThis[Symbol.for("og.cache")];
  const sym = Symbol.for("@next/cache-handlers");
  if (!store || !store.useCacheGet || (globalThis[sym] && globalThis[sym].DefaultCache)) return;
  const tagState = (tags) => (tags && tags.length ? JSON.parse(store.useCacheTags(tags)) : {});
  const areTagsExpired = (tags, timestamp) => {
    const m = tagState(tags);
    for (const t of tags) {
      const at = m[t] && m[t].expired;
      if (typeof at === "number" && at <= Date.now() && at > timestamp) return true;
    }
    return false;
  };
  const areTagsStale = (tags, timestamp) => {
    const m = tagState(tags);
    for (const t of tags) if (((m[t] && m[t].stale) || 0) > timestamp) return true;
    return false;
  };
  const streamOf = (buffer) => new ReadableStream({
    start(c) {
      if (buffer.byteLength > 0) c.enqueue(new Uint8Array(buffer));
      c.close();
    },
  });
  globalThis[sym] = Object.assign({}, globalThis[sym], {
    DefaultCache: {
      async get(cacheKey) {
        const r = store.useCacheGet(cacheKey);
        if (!r) return undefined;
        const entry = JSON.parse(r.meta);
        if (entry.expire < 0) return undefined;
        if (Date.now() > entry.timestamp + entry.revalidate * 1000) return undefined;
        if (areTagsExpired(entry.tags, entry.timestamp)) return undefined;
        const revalidate = areTagsStale(entry.tags, entry.timestamp) ? -1 : entry.revalidate;
        return { ...entry, revalidate, value: streamOf(r.body) };
      },
      async set(cacheKey, pendingEntry) {
        const entry = await pendingEntry;
        if (entry.expire === 0) return; // a dynamic entry
        const chunks = [];
        let size = 0;
        const reader = entry.value.getReader();
        for (let c; !(c = await reader.read()).done; ) {
          chunks.push(c.value);
          size += c.value.byteLength;
        }
        const bytes = new Uint8Array(size);
        let at = 0;
        for (const c of chunks) {
          bytes.set(c, at);
          at += c.byteLength;
        }
        const { value, ...meta } = entry;
        store.useCacheSet(cacheKey, JSON.stringify(meta), bytes.buffer);
      },
      async refreshTags() {},
      async getExpiration(tags) {
        const m = tagState(tags);
        return Math.max(0, ...tags.map((t) => (m[t] && m[t].expired) || 0));
      },
      async updateTags(tags, durations) {
        store.useCacheUpdateTags(tags, !!durations, durations ? durations.expire : undefined);
      },
    },
  });
})();
`
