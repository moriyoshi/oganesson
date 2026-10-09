package next

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/moriyoshi/oganesson/runtime/isr"
)

// Patches to expanded Next templates that let Oganesson act as the caching
// platform in front of the edge handlers (the role Vercel plays for Next).
// Every patch must apply exactly once (it or one of its alternatives, for
// the Next versions og supports: 16.3 and 16.4); a miss means an
// unsupported Next version and fails the build loudly.

// The protocol between these patches and the Go ISR layer (runtime/isr):
// isr.StaticHeader asks a page render for Next's static generation mode
// (supportsDynamicResponse: false); isr.CacheControlHeader and
// isr.TagsHeader carry the render's cache metadata back.
const (
	StaticHeader       = isr.StaticHeader
	CacheControlHeader = isr.CacheControlHeader
	TagsHeader         = isr.TagsHeader
)

// patch replaces old with new. patchAlts lists variants of a patch (by its
// old) for other Next versions, tried in order when old does not occur
// exactly once.
type patch struct{ old, new string }

// Partial prerendering (Cache Components): a static render that postponed
// part of the page (dynamic holes) reports its postponed state with its
// shell (isr.PostponedHeader); the ISR layer serves the shell and appends
// a resume render, which gets the state back (isr.ResumeHeader) and
// renders with renderOpts.postponed, as Next's app-page handler resumes.
var edgeSSRAppPatches = []patch{
	// 16.4 statically renders through the route module's prerender()
	// (prerenderToHTMLOrFlight), as Next's export does; render() always
	// renders dynamically there. 16.3 has render() only, which honors
	// supportsDynamicResponse (below).
	{
		"const result = await pageRouteModule.render(baseReq, baseRes, renderContext)",
		"const result = await (__ogStatic && typeof pageRouteModule.prerender === 'function' ? pageRouteModule.prerender : pageRouteModule.render).call(pageRouteModule, baseReq, baseRes, renderContext)",
	},
	{
		"async function requestHandler(req, event) {",
		"async function requestHandler(req, event) {\n    const __ogStatic = req.headers.get(" + jsonStr(StaticHeader) + ") === '1';\n    const __ogResume = req.headers.get(" + jsonStr(isr.ResumeHeader) + ");\n    const __ogPostponed = __ogResume ? Buffer.from(__ogResume, 'base64').toString('utf8') : undefined;",
	},
	{
		"supportsDynamicResponse: true,",
		"supportsDynamicResponse: !__ogStatic,\n            postponed: __ogPostponed,",
	},
	{
		"headers.set('x-edge-runtime', '1');",
		"headers.set('x-edge-runtime', '1');\n        if (__ogStatic) headers.set(" + jsonStr(isr.PathKeyHeader) + ", '1');\n        if (metadata.cacheControl) headers.set(" + jsonStr(CacheControlHeader) + ", JSON.stringify(metadata.cacheControl));\n        if (metadata.fetchTags) headers.set(" + jsonStr(TagsHeader) + ", metadata.fetchTags);\n        if (__ogStatic && metadata.flightData) headers.set(" + jsonStr(isr.FlightHeader) + ", Buffer.from(metadata.flightData).toString('base64'));\n        if (__ogStatic && typeof metadata.postponed === 'string') headers.set(" + jsonStr(isr.PostponedHeader) + ", Buffer.from(metadata.postponed).toString('base64'));",
	},
}

func applyPatches(name, src string, patches []patch) (string, error) {
next:
	for _, p := range patches {
		for _, v := range append([]patch{p}, patchAlts[p.old]...) {
			if strings.Count(src, v.old) == 1 {
				src = strings.Replace(src, v.old, v.new, 1)
				continue next
			}
		}
		return "", fmt.Errorf("next: template %s: expected exactly one %q, found %d (unsupported Next version?)", name, p.old, strings.Count(src, p.old))
	}
	return src, nil
}

var patchAlts = map[string][]patch{
	// 16.4: the edge route wrapper no longer sets supportsDynamicResponse;
	// static generation is the route module's prerender() (the handle
	// patch below calls it).
	"        const context = {\n            params,\n            previewProps,\n            renderOpts: {\n                supportsDynamicResponse: true,": {{
		"        const context = {\n            params,\n            previewProps,\n            renderOpts: {\n                waitUntil,",
		"        const __ogStatic = " + edgeRouteStatic + ";\n" +
			"        const context = {\n            params,\n            previewProps,\n            renderOpts: {\n                waitUntil,",
	}},
}

// edgeRouteStatic is whether a route handler request is a static
// generation attempt of a route that allows it.
const edgeRouteStatic = "request.headers.get(" + `"` + StaticHeader + `"` + ") === '1' && (__ogIsStaticGenEnabled(this.routeModule.userland) || __ogIsMetadataRoute(__ogNormalizeAppPath(this.routeModule.definition.page) + '/route'))"

// sourcePatches adjust Next's own modules while bundling, keyed by path
// suffix. Like the template patches, each must apply exactly once.
var sourcePatches = map[string][]patch{
	// The buffer polyfill Next compiles (og's server bundle uses it, see
	// nextNodeOverrides) predates "base64url", which Node's Buffer has had
	// since 15.7 (Prisma's query plans use it). As og's unenv buffer
	// (runtime/nodeshim): the encoding is known, measured and written as
	// base64 (whose decoder takes "-", "_" and no padding), and read as
	// base64 made URL-safe.
	"/next/dist/compiled/buffer/index.js": {
		{`case"binary":case"base64":case"ucs2":case"ucs-2"`, `case"binary":case"base64":case"base64url":case"ucs2":case"ucs-2"`},
		{`case"base64":return base64ToBytes(e).length`, `case"base64url":case"base64":return base64ToBytes(e).length`},
		{`case"base64":return base64Slice(this,r,t);`, `case"base64":return base64Slice(this,r,t);case"base64url":return base64Slice(this,r,t).replace(/\+/g,"-").replace(/\//g,"_").replace(/=+$/,"");`},
		{`case"base64":return base64Write(this,e,r,t);`, `case"base64url":case"base64":return base64Write(this,e,r,t);`},
		// Nor has it Node's constants (MAX_LENGTH is the polyfill's own
		// limit; MAX_STRING_LENGTH is V8's, as Node 24 reports it).
		{`r.kMaxLength=o;`, `r.kMaxLength=o;r.kStringMaxLength=536870888;r.constants={MAX_LENGTH:o,MAX_STRING_LENGTH:536870888};`},
	},
	// TypeORM loads its drivers with require(name) on a name from a fixed
	// list (KNOWN_MODULES), which no bundler resolves; under next start,
	// Node loads them from node_modules. Each becomes a static require:
	// an installed driver is bundled, a missing one throws MODULE_NOT_FOUND
	// when reached (build.go), as Node's require does.
	"/typeorm/platform/PlatformTools.js": {
		{
			"            return require(name);\n",
			"            switch (name) {\n" +
				"                case \"typeorm-aurora-data-api-driver\": return require(\"typeorm-aurora-data-api-driver\");\n" +
				"                case \"better-sqlite3\": return require(\"better-sqlite3\");\n" +
				"                case \"expo-sqlite\": return require(\"expo-sqlite\");\n" +
				"                case \"@google-cloud/spanner\": return require(\"@google-cloud/spanner\");\n" +
				"                case \"mssql\": return require(\"mssql\");\n" +
				"                case \"mongodb\": return require(\"mongodb\");\n" +
				"                case \"mysql2\": return require(\"mysql2\");\n" +
				"                case \"oracledb\": return require(\"oracledb\");\n" +
				"                case \"pg\": return require(\"pg\");\n" +
				"                case \"pg-native\": return require(\"pg-native\");\n" +
				"                case \"pg-query-stream\": return require(\"pg-query-stream\");\n" +
				"                case \"react-native-sqlite-storage\": return require(\"react-native-sqlite-storage\");\n" +
				"                case \"@sap/hana-client\": return require(\"@sap/hana-client\");\n" +
				"                case \"@sap/hana-client/extension/Stream\": return require(\"@sap/hana-client/extension/Stream\");\n" +
				"                case \"sql.js\": return require(\"sql.js\");\n" +
				"                case \"redis\": return require(\"redis\");\n" +
				"                case \"ioredis\": return require(\"ioredis\");\n" +
				"            }\n" +
				"            return require(name);\n",
		},
	},
	// Route handlers under Oganesson's caching platform, as Next's node
	// app-route template and exportAppRoute treat them: a static attempt
	// (isr.StaticHeader) of a route that opts into static generation
	// (isStaticGenEnabled, or a metadata route) runs without dynamic
	// response support, so dynamic usage bails out, and reports its cache
	// metadata in the internal headers.
	"/server/web/edge-route-module-wrapper.js": {
		{
			"import { WebNextRequest } from '../../server/base-http/web';",
			"import { WebNextRequest } from '../../server/base-http/web';\n" +
				"import { isStaticGenEnabled as __ogIsStaticGenEnabled } from '../route-modules/app-route/helpers/is-static-gen-enabled';\n" +
				"import { isMetadataRoute as __ogIsMetadataRoute } from '../../lib/metadata/is-metadata-route';\n" +
				"import { normalizeAppPath as __ogNormalizeAppPath } from '../../shared/lib/router/utils/app-paths';\n" +
				"import { INFINITE_CACHE as __ogInfiniteCache } from '../../lib/constants';",
		},
		{
			"        const context = {\n            params,\n            previewProps,\n            renderOpts: {\n                supportsDynamicResponse: true,",
			"        const __ogStatic = " + edgeRouteStatic + ";\n" +
				"        const context = {\n            params,\n            previewProps,\n            renderOpts: {\n                supportsDynamicResponse: !__ogStatic,",
		},
		{
			"        let res = await this.routeModule.handle(request, context);",
			// 16.4's route module statically generates in prerender(), as
			// Next's exportAppRoute calls it; 16.3's handle() did with
			// supportsDynamicResponse false.
			"        let res = __ogStatic && typeof this.routeModule.prerender === 'function' ? await this.routeModule.prerender(request, context) : await this.routeModule.handle(request, context);\n" +
				"        if (__ogStatic) {\n" +
				"            const ro = context.renderOpts;\n" +
				"            const revalidate = ro.collectedRevalidate === undefined || ro.collectedRevalidate >= __ogInfiniteCache ? false : ro.collectedRevalidate;\n" +
				"            const expire = ro.collectedExpire === undefined || ro.collectedExpire >= __ogInfiniteCache ? undefined : ro.collectedExpire;\n" +
				"            const headers = new Headers(res.headers);\n" +
				"            headers.set(" + jsonStr(CacheControlHeader) + ", JSON.stringify({ revalidate, expire }));\n" +
				"            if (ro.collectedTags) headers.set(" + jsonStr(TagsHeader) + ", ro.collectedTags);\n" +
				"            res = new Response(res.body, { status: res.status, statusText: res.statusText, headers });\n" +
				"        }",
		},
	},
	// Webpack folds this ternary for the edge build (NEXT_RUNTIME is
	// defined); esbuild would still bundle both dynamic imports, and the
	// Node build needs sharp.
	// Draft / preview mode as in next start: the Node runtime's render reads
	// the __prerender_bypass / __next_preview_data cookies
	// (tryGetPreviewData) for getStaticProps / getServerSideProps; Next's
	// edge runtime skips that, which would leave draft mode without effect
	// on pages. The decryption needs only node:crypto (nodeshim).
	"/server/render.js": {
		{"let tryGetPreviewData;", "let tryGetPreviewData = require('./api-utils/node/try-get-preview-data').tryGetPreviewData;"},
		{
			"if ((isSSG || getServerSideProps) && !isFallback && process.env.NEXT_RUNTIME !== 'edge' && previewProps) {",
			"if ((isSSG || getServerSideProps) && !isFallback && previewProps) {",
		},
	},
	"/server/og/image-response.js": {
		{
			"import(process.env.NEXT_RUNTIME === 'edge' ? 'next/dist/compiled/@vercel/og/index.edge.js' : 'next/dist/compiled/@vercel/og/index.node.js')",
			"import('next/dist/compiled/@vercel/og/index.edge.js')",
		},
	},
	// @vercel/og's edge build fetches its fallback font relative to
	// import.meta.url, which webpack turns into an asset URL and esbuild's
	// IIFE output does not have. The font is bundled as bytes instead.
	"/@vercel/og/index.edge.js": {
		{
			"var fallbackFont = fetch(\n  new URL(\"./Geist-Regular.ttf\", import.meta.url)\n).then((res) => res.arrayBuffer());",
			"import __ogFallbackFont from \"" + binaryPrefix + "./Geist-Regular.ttf\";\nvar fallbackFont = Promise.resolve(__ogFallbackFont.buffer.slice(__ogFallbackFont.byteOffset, __ogFallbackFont.byteOffset + __ogFallbackFont.byteLength));",
		},
	},
}

// patchSource applies sourcePatches to a module being bundled.
// sourcePatchFilter is an esbuild filter matching the files sourcePatches
// patches (by path suffix).
func sourcePatchFilter() string {
	var alts []string
	for suffix := range sourcePatches {
		alts = append(alts, regexp.QuoteMeta(suffix))
	}
	sort.Strings(alts)
	return "(" + strings.Join(alts, "|") + ")$"
}

// nodeRuntimeModules are the Cache Components modules that refuse Next's
// edge runtime (it has neither process.nextTick and setImmediate nor
// Node's streams, which their Node branches use; "CacheSignal cannot be
// used in the edge runtime"). og's edge templates run where both exist
// (nodeshim), so these modules take their Node branches: every
// `process.env.NEXT_RUNTIME === 'edge'` test in them is false.
var nodeRuntimeModules = []string{
	"/server/app-render/cache-signal.js",
	"/server/app-render/app-render-render-utils.js",
	"/server/app-render/app-render-scheduling.js",
	"/server/app-render/app-render-prerender-utils.js",
	"/server/app-render/use-flight-response.js",
	"/server/app-render/module-loading/track-dynamic-import.js",
	"/server/app-render/instant-validation/stream-utils.js",
	// Its patched setImmediate / process.nextTick ("fast setImmediate"),
	// which the staged prerender schedules with.
	"/server/node-environment-extensions/fast-set-immediate.external.js",
	// The prerender's resume data cache (zlib), and its Node streams.
	"/server/resume-data-cache/resume-data-cache.js",
	"/server/render-result.js",
	"/server/stream-utils/node-web-streams-helper.js",
}

const edgeRuntimeTest = "process.env.NEXT_RUNTIME === 'edge'"

func patchSource(path, src string) (string, error) {
	for _, suffix := range nodeRuntimeModules {
		if strings.HasSuffix(path, suffix) {
			if !strings.Contains(src, edgeRuntimeTest) {
				return "", fmt.Errorf("next: %s: no %q to take the Node branch of (unsupported Next version?)", path, edgeRuntimeTest)
			}
			src = strings.ReplaceAll(src, edgeRuntimeTest, "false /* og: Node branch (nodeRuntimeModules) */")
		}
	}
	for suffix, ps := range sourcePatches {
		if strings.HasSuffix(path, suffix) {
			return applyPatches(path, src, ps)
		}
	}
	return src, nil
}

// Internal headers between the dispatcher (virtual.go) and the pages
// template: the status to render with (404 for not-found renders), and the
// pages template's "render the 404 page" answer.
const (
	pagesStatusHeader   = "x-og-status"
	pagesNotFoundHeader = "x-og-not-found"
	// pagesFallbackHeader asks for the fallback shell of a getStaticPaths
	// fallback: true page (renderContext.isFallback: no getStaticProps,
	// empty props, __NEXT_DATA__.isFallback).
	pagesFallbackHeader = "x-og-fallback-render"
)

// edgeSSRPagesPatches give Next's edge-ssr template (Pages Router) what
// next start's pages handler (route-modules/pages/pages-handler.js) does
// around renderToHTMLImpl:
//   - getStaticProps / getStaticPaths reach the render (the edge template
//     passes only getServerSideProps);
//   - a static attempt (isr.StaticHeader) renders without dynamic response
//     support, but only pages Next would prerender: getStaticProps, or no
//     getServerSideProps and no custom getInitialProps (page or _app; Next's
//     automatic static optimization). Others answer 503 at once, so
//     getServerSideProps never runs twice. The render's cacheControl is
//     reported in isr.CacheControlHeader;
//   - notFound: a {"notFound":true} 404 for data requests, otherwise the
//     dispatcher renders the 404 page (pagesNotFoundHeader);
//   - redirects: the page data for data requests, otherwise Location (and
//     Refresh for 308);
//   - getStaticProps data requests answer the page data as JSON;
//   - responses of pages that render per request (getServerSideProps or
//     getInitialProps) are never cached (private, no-cache);
//   - renders get their status code (pagesStatusHeader; 500 for errors), so
//     _error's getInitialProps sees it;
//   - the fallback shell (pagesFallbackHeader) renders with isFallback and,
//     like pages-handler's fallback response, with revalidate 0 (private,
//     no-cache), marked as a cache MISS.
var edgeSSRPagesPatches = []patch{
	{
		"async function requestHandler(req, _event) {",
		"async function requestHandler(req, _event) {\n" +
			"    const __ogApp = appMod.default, __ogPage = userlandPage.default;\n" +
			"    const __ogStaticEligible = !userlandPage.getServerSideProps && (!!userlandPage.getStaticProps || !((__ogPage && __ogPage.getInitialProps) || (__ogApp && __ogApp.getInitialProps && __ogApp.getInitialProps !== __ogApp.origGetInitialProps)));\n" +
			"    const __ogStatic = req.headers.get(" + jsonStr(StaticHeader) + ") === '1';\n" +
			"    if (__ogStatic && !__ogStaticEligible) return new Response(null, { status: 503 });\n" +
			"    const __ogStatus = Number(req.headers.get(" + jsonStr(pagesStatusHeader) + ")) || 0;\n" +
			"    const __ogFallback = !__ogStatic && req.headers.get(" + jsonStr(pagesFallbackHeader) + ") === '1';\n" +
			"    // Draft mode: a __prerender_bypass cookie with the preview mode id (tryGetPreviewData's check).\n" +
			"    const __ogDraftId = process.env.__NEXT_PREVIEW_MODE_ID, __ogDraftCookie = /(?:^|;\\s*)__prerender_bypass=([^;]*)/.exec(req.headers.get('cookie') || '');\n" +
			"    const __ogDraft = !!__ogDraftId && !!__ogDraftCookie && __ogDraftCookie[1] === __ogDraftId;",
	},
	{"isFallback: false,", "isFallback: __ogFallback,"},
	// i18n: the render gets the request's locale, as pages-handler passes
	// prepare's locale / locales / defaultLocale (getStaticProps context,
	// __NEXT_DATA__ for the client router). For a static prerender (og's build
	// crawl), force the GLOBAL defaultLocale as `next build` does: with
	// i18n.domains, prepare derives the domain's default locale, but next
	// build's static export always passes the global default to getStaticProps
	// (a request-time render keeps the domain-based default).
	{
		"domainLocales: (_nextConfig_i18n = nextConfig.i18n) == null ? void 0 : _nextConfig_i18n.domains,",
		"domainLocales: (_nextConfig_i18n = nextConfig.i18n) == null ? void 0 : _nextConfig_i18n.domains,\n            locale: prepareResult.locale,\n            locales: prepareResult.locales,\n            defaultLocale: ((nextConfig.i18n && req.headers.get(" + jsonStr(StaticHeader) + ") === \"1\") ? nextConfig.i18n.defaultLocale : prepareResult.defaultLocale),",
	},
	{"supportsDynamicResponse: true,", "supportsDynamicResponse: !__ogStatic,"},
	{
		"getServerSideProps: pageMod.getServerSideProps,",
		"getServerSideProps: pageMod.getServerSideProps,\n                    getStaticProps: pageMod.getStaticProps,\n                    getStaticPaths: pageMod.getStaticPaths,",
	},
	{
		"baseReq, new WebNextResponse(undefined), {\n                ...renderContext,\n                renderOpts: {",
		"baseReq, __ogResponse(__ogStatus), {\n                ...renderContext,\n                renderOpts: {",
	},
	{
		"baseReq, new WebNextResponse(undefined), {\n                ...renderContext,\n                page: error500Mod",
		"baseReq, __ogResponse(500), {\n                ...renderContext,\n                page: error500Mod",
	},
	{"return renderResultToResponse(result);", "return __ogRespond(result);"},
	{"return renderResultToResponse(errResult);", "return __ogWithStatus(renderResultToResponse(errResult), 500);"},
	{
		"    let finalStatus = 200;",
		// The render's res starts at 200, as Node's ServerResponse does
		// under next start (next-graphql-react resolves getInitialProps only
		// when ctx.res.statusCode is set); WebNextResponse leaves it unset.
		`    const __ogResponse = (status) => { const r = new WebNextResponse(undefined); r.statusCode = status || 200; return r; };
    const __ogWithStatus = (res, status) => new Response(res.body, { status, headers: res.headers });
    const __ogIsData = isNextDataRequest && !!(pageMod.getStaticProps || pageMod.getServerSideProps);
    const __ogRespond = async (result) => {
        const md = result.metadata || {};
        // A static SSG render caches the HTML and the data JSON together, as
        // one Next cache entry: the other one travels as the companion.
        let companion = null;
        const json = (body, status) => new Response(body, { status, headers: { 'content-type': 'application/json; charset=utf-8' } });
        let res;
        if (md.isNotFound) {
            res = __ogIsData ? json('{"notFound":true}', 404) : new Response(null, { status: 404, headers: { '` + pagesNotFoundHeader + `': '1' } });
        } else if (md.isRedirect) {
            if (__ogIsData) {
                res = json(JSON.stringify(md.pageData), 200);
            } else {
                const p = md.pageData.pageProps;
                const status = p.__N_REDIRECT_STATUS || 307;
                let dest = p.__N_REDIRECT;
                if (nextConfig.basePath && p.__N_REDIRECT_BASE_PATH !== false && dest.startsWith('/')) dest = nextConfig.basePath + dest;
                if (dest.startsWith('/')) dest = dest.replace(/\/+/g, '/');
                const headers = { Location: dest };
                if (status === 308) headers.Refresh = '0;url=' + dest;
                res = new Response(dest, { status, headers });
            }
        } else if (__ogIsData && pageMod.getStaticProps) {
            res = json(JSON.stringify(md.pageData), 200);
            if (__ogStatic) companion = ['text/html; charset=utf-8', await result.toUnchunkedString(true)];
        } else {
            res = renderResultToResponse(result);
            if (__ogStatus && res.status === 200) res = __ogWithStatus(res, __ogStatus);
            if (__ogStatic && pageMod.getStaticProps && md.pageData && res.status === 200) companion = ['application/json; charset=utf-8', JSON.stringify(md.pageData)];
        }
        if (companion) {
            res.headers.set(` + jsonStr(isr.CompanionTypeHeader) + `, companion[0]);
            res.headers.set(` + jsonStr(isr.CompanionHeader) + `, Buffer.from(companion[1]).toString('base64'));
        }
        // Pages that are neither SSG nor automatically static render per
        // request: Next sends revalidate 0 (pages-handler).
        // Draft mode renders per request and is never cached (pages-handler).
        if (!__ogStaticEligible || __ogDraft) res.headers.set('Cache-Control', 'private, no-cache, no-store, max-age=0, must-revalidate');
        if (__ogFallback) {
            res.headers.set('Cache-Control', 'private, no-cache, no-store, max-age=0, must-revalidate');
            res.headers.set('x-nextjs-cache', 'MISS');
        }
        if (__ogStatic && !__ogDraft) res.headers.set(` + jsonStr(CacheControlHeader) + `, JSON.stringify(md.cacheControl || { revalidate: false }));
        // Next's static pages and SSG data ignore the query string (the
        // client even adds route params to data URLs as a query).
        if (__ogStatic) res.headers.set(` + jsonStr(isr.PathKeyHeader) + `, '1');
        return res;
    };
    let finalStatus = 200;`,
	},
}
