# Next.js Pages Router

## Summary

Oganesson compiles the Next.js Pages Router (`pages/`) into the same self-contained Go binary it produces for the App Router, reusing Next's own edge-ssr and route-module templates rather than reimplementing them. This document covers page discovery and rendering, API routes, `getStaticProps` / `getStaticPaths` with every `fallback` mode, app-and-pages coexistence in one project, draft / preview mode, on-demand revalidation, and i18n. The generic ISR / cache-handler mechanics (companion entries, path-only keys, revalidation plumbing) live in [[static-generation-and-isr]]; this doc covers how the Pages Router drives them.

## Key Facts

- Pages live in `pages/` or `src/pages`, discovered in `internal/frameworks/next/pages.go`. Keys match Next: `/blog/index` → `/blog`, `/index` → `/`. `_app`, `_document`, `_error` fall back to `next/dist/pages/*`.
- Each page compiles to a Next `edge-ssr` template instance (`og:pages-route:<page>`). API routes compile to `pages-edge-api` (`og:pages-api-route:<page>`).
- Pages compile in the `pages` layer (server) and `pages-browser` layer (client); they get plain React and generic `next/*` aliases (the Pages versions of `next/link`, `next/head`), not the App Router aliases.
- A static (prerender) attempt is allowed only for SSG and automatically-static pages; everything else answers 503 immediately so `getStaticProps` never runs twice.
- `app.FormatVersion` has grown with Pages Router features: 2 (`og.cache.staticPaths`), 3 (`cacheBypass` meta for draft mode). `packageFixture` writes `meta.json` from `Output.Meta()`, shared with `og build`, so they cannot drift.
- `Build` accepts an app directory, a pages directory, or both. `next build`'s "Conflicting app and page file(s)" check (`checkAppPageConflicts`) is ported with Next's exact message.
- A project can mix routers; both route sets join one dispatcher route table (`virtual.go`).

## Details

### Page rendering (edge-ssr + pages-handler port)

Each page is one `edge-ssr` template instance filled the way `next-edge-ssr-loader` fills it: the page, `_app`, `_document`, `_error`, an optional `pages/500`, and route-module options with `normalizePagePath`. The template expander gained `// OPTIONAL_IMPORT:* as name` for the optional pieces.

`edgeSSRPagesPatches` port `route-modules/pages/pages-handler.js` (the `next start` path) around `renderToHTMLImpl`:

- `getStaticProps` and `getStaticPaths` reach the render; the edge template only passes gSSP.
- The static attempt is gated: SSG pages and automatically-static pages (no gSSP, and no custom `getInitialProps` on the page or `_app`, compared against `origGetInitialProps`). Non-static pages answer 503 during a static attempt. The render's `cacheControl` goes into `x-og-cache-control`.
- `notFound`: data requests get `{"notFound":true}` with 404; HTML requests get a marker and the dispatcher renders the 404 page (`render404`).
- Redirects: data requests get the page data; HTML requests get `Location`, plus `Refresh` for 308.
- SSG data requests serve `pageData` as `application/json; charset=utf-8`.
- Per-request pages get `private, no-cache, no-store, max-age=0, must-revalidate`.
- Renders take their status from `x-og-status`; errors render with 500 so `_error`'s `getInitialProps` sees the right code.

### Dispatcher (`virtual.go`)

- Pages routes join the route table; `_error` is internal (never URL-matched).
- The not-found page is `/_not-found/page` for app projects, otherwise `pages/404` or `_error`. Unmatched URLs and `notFound` answers render it with status 404. Only a page's `notFound` keeps cache metadata, so arbitrary URLs never fill the cache.
- `/_next/data/<buildId>/<page>.json` matches pages only, sent with `x-nextjs-data`; `RouteModule.prepare` recognizes the URL. Another build's data URL gets a plain 404.
- `staticParams` resolves pages through `getStaticPaths`.
- Direct `/404` is answered 404 and `/500` is answered 500 via `x-og-status` (base-server behavior); in a mixed project `pages/404` is matchable and would otherwise answer 200.

### Client bundle

- A second browser bundle (`static/chunks/pages-<hash>.js`, `buildPagesClient`): Next's `next/dist/client/next` plus every page, `_app`, `_error` pushed onto `__NEXT_P` with a lazy `require`.
- `__BUILD_MANIFEST.pages` (`pagesManifest`): per page, the bundle plus the CSS its server import graph reaches; `_app`'s CSS sits under `/_app`.
- `static/<buildId>/_buildManifest.js` and `_ssgManifest.js` are listed in `lowPriorityFiles`, as Next does. `_buildManifest.js` `sortedPages` uses Next's own `getSortedRoutes` (`jshost.Host.SortedRoutes`), not alphabetical, so the client matches `/posts/new` before `/posts/[id]` and a root `[slug]` last.
- Discovery (`pages.go`) runs `rsc.ProbeExports` to find the pages that export `getStaticProps`; those populate the client's `__SSG_MANIFEST` (`_ssgManifest.js`).

### `next_ssg` port (`ssg.go`, browser build only)

Mirrors Next's `next-ssg-transform`. The `ogapi` facade gained `Module.Statements`:

- the data exports are removed, together with declarations and import bindings only they referenced (to a fixpoint);
- `export var __N_SSG` / `__N_SSP = true;` is appended;
- declaring both `getStaticProps` and `getServerSideProps` is Next's error.

Without the `__N_SSG` / `__N_SSP` flags the client router never fetched `/_next/data`, so client navigation to a gSSP page rendered empty props (found in the browser).

### API routes (`pages/api/**`)

Composed from Next's own templates, in the `pages` layer:

- `og:pages-api-route:<page>` is the `pages-edge-api` template (the edge adapter the dispatcher drives).
- Its handler is the bridge `og:pages-api-bridge:<page>`:
  - `config.runtime` `edge` / `experimental-edge`: the user's handler as is.
  - Otherwise: the `pages-api` template's `handler(req, res, ctx)` (`og:pages-api:<page>`) on Next's own `createRequestResponseMocks`.
- The request body is buffered and pushed from the mock request's `_read`. `resWriter` streams writes into a `TransformStream`; the Response is created at the head when `headPromise` resolves.
- Routes are `Route{Pages, IsRoute}` (`api` in the table). They never match `/_next/data`. A static attempt answers 503 at once; without it the ISR layer runs the handler twice for every new path of a dynamic API route. Build label: "ƒ API route".
- Matches `next start`: query, dynamic params, cookies; JSON / urlencoded / raw bodies (`Invalid JSON` → 400); `res.redirect`, ETag and `If-None-Match` → 304, HEAD; streaming writes; a thrown handler → 500; edge handlers.

Node shims required by Next's API runtime (the mocks, `api-resolver`, `raw-body`), in `runtime/nodeshim`:

- `stream` and `string_decoder` come from Next's compiled polyfills (`nextNodeOverrides`, with `buffer`); unenv's versions are stubs.
- `events` override (`runtime/nodeshim/js/events.js`): unenv's `EventEmitter` is an ES class, but readable-stream does `EventEmitter.call(this)` and `new (require("events"))()`. The override is a callable constructor with `EventEmitter.EventEmitter` set. Pitfall: unenv's `on()` calls `this.addListener()` and readable-stream sets `addListener = on`, giving unbounded recursion (the VM spun, requests died at the interrupt). The override aliases `on`↔`addListener` and `off`↔`removeListener` as Node does.
- `Error.captureStackTrace` (webapi polyfill) honors `Error.prepareStackTrace`, passing V8 CallSite objects parsed from Goja frames, because depd (`raw-body`'s dependency) reads `callSite.getFileName()`.
- `util.debuglog` only logs when `NODE_DEBUG` names the section (load-time rewrite of unenv's always-on version).

### getStaticProps / getStaticPaths and fallback modes

The static-params request runs a page's `getStaticPaths` through Next's `buildPagesStaticPaths` (validation, path strings, i18n). Params go back plus `isr.FallbackHeader` (Next's `FallbackMode`). `app.Prerender` records `staticPaths` (page → `{fallback, paths}`) in `prerender.json`; the cache seeds them (`SeedStaticPaths`) and JS reads them via `og.cache.staticPaths(page)`.

- **`fallback: false`** — the dispatcher applies pages-handler's `NoFallbackError`. An unknown path becomes an unmatched URL for HTML (the pages 404, or the App Router not-found) and a plain 404 for data (after which the client hard-navigates).
- **`fallback: 'blocking'`** — renders like a normal dynamic path.
- **`fallback: true`** (`isIsrFallback`) — in production Next serves the build-time fallback shell uncached, `revalidate: 0` (`private, no-cache`), `x-nextjs-cache: MISS`; the client's `/_next/data` request generates and caches the real page, with no background generation.
  - Dispatcher, for a `PRERENDER` route with a non-prerendered path, on an HTML request whose user agent is not a bot (Next's `isBot`): the static attempt answers 503 (so the shell is never cached) and the dynamic render carries `x-og-fallback-render`.
  - Patch: `renderContext.isFallback: __ogFallback`, set only outside static attempts. `renderToHTMLImpl` then skips `getStaticProps`, renders empty props and `__NEXT_DATA__.isFallback`, and the response gets `private, no-cache` / MISS.
  - Data requests and bots render the page as for `'blocking'`.
  - Companion entries (see below) mean the data render also caches the HTML; `store` clears the key's dynamic flag so the next HTML request is a HIT.
  - Pitfall: the first version sent no Cache-Control for the shell; pages-handler sets `cacheControl = {revalidate: 0}` when `isIsrFallback`.

### SSG cache consistency (one render, two URLs)

The Pages Router drives the ISR companion mechanism (detailed in [[static-generation-and-isr]]). The problem was that HTML and `/_next/data` JSON came from separate renders, so `builtAt` differed and client navigation showed different props than the SSR HTML (Next keeps them as one entry). The fix: ISR companion entries (`isr.CompanionHeader`, `CompanionPathHeader`, `CompanionTypeHeader`). The pages patch (`__ogRespond`, now async) attaches the other representation on static SSG renders — HTML carries the pageData JSON, data requests carry the HTML (`renderToHTMLImpl` renders both for SSG data). For dynamic routes the client adds params as a query (`/docs/x.json?slug=x`), so `isr.PathKeyHeader` stores the output under the path-only key (`Entry.PathOnly`) and lookups fall back to it; this also stops arbitrary queries from fragmenting entries.

### On-demand revalidation (`res.revalidate`)

The bridge supplies the request-meta `revalidate`, read by `RouteModule.getRouterServerContext`. It calls `og.cache.revalidatePath(path, onlyGenerated)` — a promise over `isr.Handler.Revalidate`, set with `Cache.SetRevalidator`. `Revalidate` re-renders the path now and stores entries with their companions: `onlyGenerated` leaves never-generated paths alone (a 404, counted as success); a failed render keeps the cached page. The success check is `NextNodeServer.revalidate`'s. When the static render is not cacheable (the 503 bailout), `Revalidate` renders the path as a normal request and reports that status: a gSSP page revalidates successfully; a throwing page reports "Invalid response 500" (was 503).

### Draft / preview mode

`res.setDraftMode` already set `__prerender_bypass`; the build generates `__NEXT_PREVIEW_MODE_*` into the runtime env (`build.go`) and Next's edge runtime reads them. The missing pieces:

1. **The ISR layer served the cached page to draft visitors.** New `isr.Handler.Bypass`. `app.Meta.CacheBypass` (`{cookie, valueEnv}`, from `next.Output.Meta()`) makes requests whose `__prerender_bypass` equals the runtime env's `__NEXT_PREVIEW_MODE_ID` skip the cache entirely. Wrong or absent cookies still get the cached page. The bypass also protects other visitors: a draft static attempt reports no cache metadata, which would otherwise mark the key dynamic for everyone.
2. **Next's edge runtime never reads preview data.** `render.js` guards `tryGetPreviewData` with `NEXT_RUNTIME !== 'edge'`; `next start` runs pages in Node, so `sourcePatches["/server/render.js"]` removes that guard and gSP / gSSP get `draftMode`, `preview`, `previewData`, plus `__N_PREVIEW`. `route-module.js`'s `prepare` has the same guard but the edge template passes `res = null`, so the pages prologue instead computes `__ogDraft` (the cookie equals the id — `tryGetPreviewData`'s validity check).
3. **Draft renders are per request:** `private, no-cache`, no `x-og-cache-control` (pages-handler: `isDraftMode` → revalidate 0).
4. **`res.setPreviewData` needs node:crypto pieces:** `createCipheriv` / `createDecipheriv` (aes-{128,192,256}-{gcm,cbc,ctr} over Go `aesGcm` / `aesCbc` / `aesCtr`, input buffered until `final()`); secret `KeyObject`s (`createSecretKey`), and `createPrivateKey` / `createPublicKey` that throw for non-keys as Node does (for `jsonwebtoken` v9).

### App and pages in one project

Mostly worked already: both route sets join one table, and 404s (unmatched URLs and a page's `notFound`) render the app's `/_not-found`, as `renderErrorToResponse` does when an app directory exists. Fixed: direct `/404` status (above), and the conflict check (`checkAppPageConflicts`) comparing pages and API files against app pages and route handlers by URL (route groups normalized), with Next's exact message. Browser-verified: App→Pages is a full-page load (the App Router's RSC fetch gets HTML); Pages→Pages is a soft navigation through `/_next/data`; Pages→App is an immediate hard navigation with no data request.

Left open (TODO, predates M8): App Router 404s for unmatched URLs are cached per URL; Next uses one `/_not-found` entry.

### Subresource 404s (`Sec-Fetch-Dest`: image, font, script, …)

Next answers these with a plain-text 404 instead of a rendered not-found page (router-server for unknown URLs, base-server for `/404`, the app-page template for `/_not-found`). Next's own `isNonHtmlSecFetchDest` is imported into the dispatcher for unmatched URLs and the pages 404 renders. Such requests bypass the ISR cache (`nonHTMLDest`, the same list as Next's), which had previously cached the prerendered `/404` / not-found HTML.

### i18n (locale routing)

Config example: `i18n: { locales: [en, fr, nl-NL], defaultLocale: en }`. `RouteModule.prepare` already detected the locale prefix through `routesManifest.i18n` (`__NEXT_I18N_CONFIG`, defined by `getDefineEnv`). Three gaps were closed:

1. **The dispatcher matched raw paths, so `/fr` was a 404.** Pages routes now match `normalizeLocalePath`'s path while the URL keeps its prefix for `prepare`. API routes and App Router routes are not localized. `canonicalPath` (default locale, unprefixed) is used for static-path comparisons and the data → HTML companion. `dataPath` names the HTML → data companion; the client always locale-prefixes data URLs (`/_next/data/<id>/en.json`), which the first version missed.
2. **The edge template's `renderOpts` lacked `locale` / `locales` / `defaultLocale`.** pages-handler passes them, so gSP got `undefined` and `__NEXT_DATA__` lacked them; a patch adds them from `prepareResult`.
3. **Root locale detection.** router-server's `getLocaleRedirect` covers `NEXT_LOCALE`, `Accept-Language` and domain locales as a 307 that keeps the query; it runs in `runMiddleware`, the pre-cache phase. With locale detection on, `meta.json` `hasMiddleware` is set (`Output.LocaleDetection`), so the ISR layer asks before serving a cached `/`.

Prerender: `Output.PrerenderRoutes()` (shared by `og build` and `packageFixture`) adds each non-default locale's static pages (`/fr`, `/nl-NL/about`). Dynamic pages' static params are the paths themselves, from `buildPagesStaticPaths`' `prerenderedRoutes`, canonicalized; `app.Prerender` accepts strings besides param sets.

**Domain locales.** `i18n.domains` selects the domain's default locale for the
root cross-domain redirect, `getStaticProps`, and `pages/404`; an explicit
locale prefix overrides the domain default. The `getStaticProps`
`defaultLocale` must remain the global configured default, matching
`next start`, rather than being replaced by the domain's default. Config-level
redirects with `locale: false` under domain routing remain untested.

### basePath and middleware

Middleware matchers are built through Next's own `getMiddlewareMatchers` with
the complete `nextConfig`, so `basePath` is prepended to the matcher. A request
to `/docs/guarded` is intercepted while `/guarded` is an ordinary 404. These
paired assertions prevent a test from passing merely because the route itself
is absent.

### trailingSlash canonicalization

Page requests redirect with 308 in both directions to the configured canonical
form, preserving the query string. Static and dynamic SSG routes are
prerendered under the canonical slash form, so the redirected destination hits
the cache rather than rendering a second entry. With `basePath`, the base root
also redirects to its slash form and hits the prerendered home page; the former
`p === BASE` exemption was incorrect.

## Files

- `internal/frameworks/next/pages.go` — page / API route discovery, template composition, `pagesManifest`.
- `internal/frameworks/next/ssg.go` — the `next_ssg` transform (browser build).
- `internal/frameworks/next/patches.go` — `edgeSSRPagesPatches`, `sourcePatches["/server/render.js"]`, etc.
- `internal/frameworks/next/virtual.go` — the dispatcher: route table, `/_next/data` routing, locale matching, 404 / subresource handling.
- `runtime/nodeshim/js/events.js` — the callable `EventEmitter` override.
- `runtime/nodeshim/nodeshim.go` — `nextNodeOverrides` (stream, string_decoder), `util.debuglog` rewrite.
- `runtime/isr/` — companion entries, path-only keys, `Handler.Revalidate`, `Handler.Bypass` (see [[static-generation-and-isr]]).
- `runtime/app/` — `FormatVersion`, `Prerender`, `Meta.CacheBypass`, `og.cache.staticPaths`.
- `internal/jshost` — `Host.SortedRoutes` (Next's `getSortedRoutes`).
- `runtime/webapi` — `Error.captureStackTrace` / `prepareStackTrace` polyfill.
- Fixtures: `testdata/apps/next-pages`, `testdata/apps/next-mixed`, `testdata/apps/next-i18n`.
- Additional routing fixtures: `testdata/apps/next-basepath`,
  `testdata/apps/next-i18n-domains`, and
  `testdata/apps/next-trailingslash`.

## Test Coverage

Fixture-based tests skip when the fixture's `node_modules` is absent (install with `go run ./cmd/og install <dir>`). Each test below was neutralized (fix removed → test fails with the intended message).

- `internal/frameworks/next`:
  - `TestPagesRouter` — HTML, status, `x-nextjs-cache` and Cache-Control per page kind; data routes (gSSP, SSG, notFound, redirect, stale build); redirect headers; SSG prop stability; `_buildManifest.js` / `_ssgManifest.js`; `/posts/new` precedence; fallback false and blocking; the SSG `notFound` cache.
  - `TestPagesAPIRoutes` — including "handler ran 2 times" via an `og:kv` counter on a dynamic API route.
  - `TestPagesSSGCache` — build / data-first / HTML-first consistency, query-independent hits, revalidation, `onlyGenerated`, failure, gSSP revalidation and "Invalid response 500".
  - `TestPagesFallbackTrue` — the shell twice (`isFallback`, empty `pageProps`, `private, no-cache`, MISS); data generates the page, then the HTML is a HIT; a Googlebot request renders the page.
  - `TestMixedRouters` — both routers, data routes, the app not-found for 404s, direct `/404`, subresource 404s.
  - `TestAppPageConflict` — a page, an API route, and a route-group conflict, against Next's exact message.
  - `TestPagesDraftMode` — public HIT; with the cookie, per-request renders, `private, no-cache`, `draftMode` in props and data; cache untouched for others / wrong cookie; `setPreviewData` round trip; disabling clears the cookie.
  - `TestPagesI18n` — routing, locale props and `__NEXT_DATA__`, fallback 404s per locale, localized 404 pages, prerender HITs, locale redirects. Pitfall: `dataPath`'s neutralization first passed because earlier HTML rows had created the data entries as companions; the data rows now come first so they test what the build stored.
  - `TestPagesI18nDomains` — cross-domain locale redirect, global versus
    per-domain default locale, and domain-localized custom 404 behavior,
    checked against a `next start` oracle.
  - `TestTrailingSlash`, `TestTrailingSlashDefault`, and
    `TestTrailingSlashBasePath` — both redirect directions, query preservation,
    dynamic/static SSG cache hits, and the basePath root.
  - `TestBasePath` — the basePath-prefixed middleware matcher and its unprefixed
    negative control.
- `internal/jshost`: `TestSortedRoutes`.
- `internal/frameworks/next`: `TestSSGTransform` — removal, dead-code elimination, export clauses, mixed-declarator rewrite, both functions refused.
- `runtime/nodeshim`: `TestEventEmitterNodeCompat` (recursion → timeout; class → "not invoked without new"), `TestReadableStream`, `TestRawBody` (`getFileName`), `TestDebuglog`. (Next-dependent ones skip without the next-pages fixture.)
- `runtime/webapi`: the `prepareStackTrace` check.
- `runtime/isr`: `TestCompanionAndRevalidate` (including the dynamic-page case), `TestPathOnly`.
- `runtime/app`: `TestFormatVersion` (version-relative, refuses the previous format).
- `runtime/nodeshim` crypto: `TestCipheriv` (NIST GCM / CBC / CTR vectors, GCM tamper rejection, key-length check), `TestSecretKeyObject`.
- Browser-verified on packaged binaries: hydration, counter, CSS, soft navigation across page kinds and routers, fallback-shell generation then HIT, data-404 hard navigation, draft mode, and locale switching with locale-prefixed data URLs.

## Pitfalls

- A static attempt must answer 503 for any non-static page / API route, or the handler and gSSP run twice per new path.
- The `__N_SSG` / `__N_SSP` flags are mandatory, or the client router never fetches `/_next/data` and client navigation renders empty props.
- `events.js` recursion: do not let `on` and `addListener` call each other (unenv × readable-stream).
- The `fallback: true` shell must carry `cacheControl = {revalidate: 0}` (no Cache-Control = wrong).
- The i18n client always locale-prefixes data URLs; forget `dataPath` and the HTML → data companion never matches. A test that creates the data entry as a companion from an earlier HTML row can mask this, so order data rows first.

### Known open gaps / follow-ups

- Config-level redirects with `locale: false` under `i18n.domains` are not yet
  exercised.
