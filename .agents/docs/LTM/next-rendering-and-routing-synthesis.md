# Next.js Rendering and Routing Synthesis

## Summary

Oganesson compiles both Next.js routers into one Go executable and drives
Next's own route modules, render templates, manifests, and client bootstraps in
Sobek. A Go dispatcher coordinates routing, middleware, static generation,
ISR, Pages data routes, Server Actions, and Cache Components while preserving
the fresh-VM-per-request model. This document is the orientation map for the
request lifecycle; the included topic documents retain implementation detail.

## Included Documents

| Document | Focus |
|----------|-------|
| [app-router-rendering.md](./app-router-rendering.md) | App Router RSC/SSR, browser bundle, hydration, and soft navigation. |
| [route-handlers-actions-middleware.md](./route-handlers-actions-middleware.md) | Route handlers, Server Actions, and proxy/middleware. |
| [static-generation-and-isr.md](./static-generation-and-isr.md) | Build-time prerendering and the Go ISR page cache. |
| [pages-router.md](./pages-router.md) | Pages rendering, API routes, data routes, fallback, draft mode, and i18n. |
| [cache-components-use-cache.md](./cache-components-use-cache.md) | `"use cache"`, shared cache handlers, and partial prerendering. |

## Stable Knowledge

- The server build preserves Next's `rsc`, `ssr`, `shared`, `pages`, and
  browser-layer semantics. Next's templates remain the primary implementation;
  Oganesson fills their loader inputs and patches only bounded incompatibilities.
- App Router rendering uses one request VM for the RSC and SSR bundles. The
  browser build contains Next's CommonJS `app-next` bootstrap, a webpack-runtime
  shim, and a registry for discovered Client Components. Mapping Next `dist/esm`
  ids to CommonJS twins prevents duplicate router and React context instances.
- The server build is two-pass: discover Client Components, build the browser
  assets, then rebuild the server with the hashed root client file available.
- Route handlers use Next's `edge-app-route` and `app-route` templates. Dynamic
  parameters are delivered through `nxtP<name>` query entries, matching Next's
  edge wrapper contract.
- Module-level and inline Server Actions are transformed into registered server
  references. Inline actions use TypeScript-Go closure analysis, narrowed
  captures, encrypted bound arguments, and the same 42-hex-character id scheme
  as Next. Unsupported closure shapes fail explicitly at build time.
- `proxy.ts` and `middleware.ts` use Next's matcher and middleware response
  protocols. Middleware runs before cache lookup, so rewrites and overridden
  headers determine the effective ISR key; response headers and cookies are
  merged onto cached content.
- `runtime/isr` is an HTTP-aware Go cache separate from `og:kv`. `og build`
  seeds it from `prerender.json`; request-time states are HIT, STALE with
  single-flight regeneration, blocking regeneration, or a remembered dynamic
  marker.
- Cache keys are intentionally bounded: unmatched App Router URLs converge on
  `/_not-found`, static output can declare path-only keying, and dynamic markers
  ignore query strings. Internal `x-og-*` protocol headers are stripped at the
  public boundary.
- Pages Router output uses Next's `edge-ssr` and `pages-edge-api` templates.
  `/_next/data` routes, `getStaticProps`, `getStaticPaths`, all fallback modes,
  draft mode, localized routing, domain locales, basePath, trailingSlash, and
  mixed App/Pages projects are integrated with the same dispatcher and ISR.
- SSG companion entries keep HTML and JSON or RSC representations from the
  same render. Static attempts must return 503 immediately for dynamic pages
  and API routes, preventing double execution.
- Cache Components hoist `"use cache"` functions using the inline-action
  machinery, but store results in the shared Go `runtime/isr` cache handler.
  Partial prerendering stores a shell and postponed state, then appends a
  per-request resume stream.
- Custom cache handlers, private/remote cache kinds, `cacheLife`, and
  server-action redirect RSC fetches are wired. `__NEXT_PRIVATE_ORIGIN` is set
  only after listening so self-fetches target the real server.

### Source chronology

The `static-generation-and-isr.md` pitfall section predates Cache Components and
states that PPR and `"use cache"` were not built. That statement is historical:
`cache-components-use-cache.md` records their later implementation and is the
current authority. The ISR source remains authoritative for the shared page
cache and bounded-key design.

## Operational Guidance

- Start from Next's router-server, loader, route-module, or app-page behavior
  and port the smallest missing slice. Before adding a custom render path,
  check whether a loader-tree key or manifest entry lets Next's own template do
  the work.
- Keep routing, cache identity, and response representation separate. For any
  static feature, test HTML, RSC or data payload, middleware interaction, and
  the first and subsequent cache states.
- Route internal metadata through the constants in `runtime/isr`; never expose
  new `x-og-*` headers to clients.
- Treat per-request VM isolation as a design constraint. Server Actions and
  cache handlers cannot persist module-level maps; use cookies, `og:kv`, ISR,
  or another Go-backed service.
- For Pages changes, test both data-first and HTML-first order so a companion
  created by an earlier request cannot make an incomplete test pass.
- Use a packaged binary and a browser for hydration, soft navigation, action
  forms, fallback shells, and mixed-router transitions.

## Files

- `internal/frameworks/next/build.go`, `virtual.go`, `tree.go`, `client.go` -
  server/browser builds, route table, loader tree, and App client.
- `internal/frameworks/next/actions.go`, `inline_actions.go` and
  `_tsoverlay/ogapi/actions.go` - action and cache transforms.
- `internal/frameworks/next/pages.go`, `ssg.go` - Pages and API routes,
  browser manifests, and data-export stripping.
- `internal/frameworks/next/patches.go` - guarded Next source/template patches.
- `runtime/isr` - page, data, RSC, route, tag, and use-cache storage.
- `runtime/app/serve.go`, `runtime/app/prerender.go` - serving, private origin,
  prerendering, and payload seed data.

## Tests

- Core fixture tests live in `internal/frameworks/next`; install their
  `node_modules` with `go run ./cmd/og install <fixture>` before treating skips
  as results.
- High-value suites include `TestBuildTreeGlobalNotFound`,
  `TestInlineServerActions`, `TestTransformInlineActions`, `TestStaticAndISR`,
  `TestPagesRouter`, `TestPagesAPIRoutes`, `TestPagesSSGCache`,
  `TestPagesFallbackTrue`, `TestPagesDraftMode`, `TestPagesI18n`,
  `TestMixedRouters`, and `TestCacheComponents`.
- `runtime/isr` tests `TestNotFoundCachedOnce`,
  `TestStaticPageQueryIndependent`, `TestDynamicMarkerIgnoresQuery`,
  `TestCompanionAndRevalidate`, and `TestPathOnly` guard cache identity.

## Pitfalls

- Failure to map `next/dist/esm` client ids to CommonJS can create duplicate
  routers and React contexts even when SSR looks correct.
- Inline action capture narrowing is load-bearing. Capturing a larger object can
  serialize functions and produce a run-time "Failed to find Server Action".
- A static attempt that runs a dynamic handler before returning 503 causes
  duplicate side effects.
- `__N_SSG` and `__N_SSP` are required in browser page modules; without them,
  client navigation omits the data request and renders empty props.
- PPR shells are not normal cached documents: the tail is request-specific and
  requires uncacheable response headers and exactly one final document close.
- A custom JavaScript cache handler is recreated per request. Persistent custom
  handlers need a Go-backed store.
- `dynamicParams = false`, static GET handlers in all shapes, and the duration
  argument of Next 16 `revalidateTag` remain limited; consult the source docs
  and `TODO.md` before assuming full parity.
