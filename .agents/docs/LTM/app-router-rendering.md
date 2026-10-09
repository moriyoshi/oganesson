# App Router Rendering: Server Render, Browser Bundle, Hydration, Navigation

## Summary

Oganesson compiles a Next.js App Router project into a single Go executable that
server-renders RSC/SSR HTML with inline Flight data and ships a browser bundle
that hydrates and performs client (soft) navigation — all with no Node.js and no
`node_modules`, running Next's own JavaScript inside Sobek/Goja. This document
covers the server render pipeline, how the browser bundle is produced, and the
hydration and navigation behaviors it enables. For the esbuild webpack-layer
mechanics see [[build-system-esbuild-layers]]; for route handlers, server
actions, and middleware see [[route-handlers-actions-middleware]].

## Key Facts

- `og build -o app <appdir>` produces one executable serving the app with no
  Node.js and no `node_modules`.
- Server render supports: root layout with the `metadata` API, async server
  components, `next/link`, dynamic routes (`/blog/[slug]` with awaited
  `params`), `notFound()`, the built-in `/_not-found` page with status 404, and
  streamed HTML with inline Flight data.
- With `experimental.globalNotFound`, the loader tree includes a
  `global-not-found` component (user-provided or built in), which Next's own
  app renderer serves as a full-document 404.
- The browser bundle is one esbuild build (separate from the server build) into
  `static/chunks/main-app-<contenthash>.js` (ES2020, minified).
- Build order: server pass 1 (discovers client modules) → browser build →
  server pass 2 (`rootMainFiles` holds the hashed chunk name).
- The client uses CommonJS `next/dist`, NOT `next/dist/esm`; server ids pointing
  into `next/dist/esm` are mapped to their CommonJS twins so the browser holds a
  single router/React-context instance.
- Verified in Chromium (Playwright): no console errors, hydration of a
  `"use client"` counter, `usePathname()`, soft navigation via `?_rsc=` Flight
  payloads, viewport prefetching, immutable static caching, and `public/` serving.

## Details

### Server render pipeline (all Go, plus Next's own JS in Goja)

1. **Install** (`internal/install`): resolves the npm registry, hoists packages,
   verifies integrity and extracts. Runs no lifecycle scripts; skips optional
   native binaries (`@next/swc-*`).
2. **Config** (`internal/jshost`): `next.config.*` is bundled by esbuild and
   evaluated in Goja. Then Next's real `loadConfig(..., {customConfig})` and
   `getDefineEnv` run in the same engine, so `.env*` loading, defaults,
   validation, and `NEXT_PUBLIC_*` defines all match `next build`. Build-time
   Node APIs come from `runtime/nodeshim`: a Go-backed read-only `fs`, plus
   `path`, `os`, `url`, `util`, `events`, `buffer`, `crypto`, `http.Agent`,
   `assert`, `async_hooks`, and `string_decoder`.
3. **Routes** (`internal/frameworks/next`): `ScanAppPaths` / `GroupRoutes`, then
   `BuildTree`, a Go port of next-app-loader's `createTreeCodeFromPath`.
4. **Server bundle**: one esbuild build of the whole server. The esbuild
   namespace encodes Next's webpack layer (`rsc`, `ssr`, `shared`) — see
   [[build-system-esbuild-layers]] for the full layer mechanics. Layer
   transitions follow webpack-config: `app-page` / `app-page/module` resolve to
   the `ssr` layer and the async storages resolve to the `shared` layer.
   Per-layer aliases follow Next's `create-compiler-aliases`, and RSC-layer bare
   imports resolve with the `react-server` export condition. The
   `internal/bundle/rsc` package (promoted from the M0 spike) provides the
   `"use client"` boundary plugin — `registerClientReference` proxies for
   `"use client"` modules in the rsc layer — and the Flight manifests. Virtual
   modules:
   - `og:manifests` sets the `self.__*` manifests and runs before any route.
   - `og:route:<page>` is Next's `edge-ssr-app` template.
   - `og:app-page:<page>` is Next's `app-page` template with the generated tree.
   - `og:client-registry` holds the SSR instances of client modules, behind
     `__next_require__`.
   - `og:entry` is the URL dispatcher that stands in for Next's router and
     sandbox.

   Two passes: the first discovers client modules, the second emits them.
5. **Package** (`pack`): the `og` executable itself plus a zip payload
   (`server.js` and `meta.json`). At startup any `og` binary checks for a
   payload and, if present, serves it (`runtime/app`).

Runtime fixes the server render forced:
- **Goja**: `import()` must be lowered (`dynamic-import` added to `gojatarget`).
- Next's edge build expects `global`, a mocked `__dirname` / `__filename`, and a
  `require` stub with `.extensions` at build time; and `Buffer` provided
  globally, whose `indexOf` accepts any `Uint8Array` as Node's does.

### Browser bundle (`internal/frameworks/next/client.go`)

One esbuild build, separate from the server build, bundling everything into
`static/chunks/main-app-<contenthash>.js` (ES2020, minified). It composes three
things, in order:

1. A **webpack-runtime shim**, evaluated first. Provides `__webpack_require__`
   (with `.u`, `.k`, `.miniCssF`, `.p`), `__webpack_chunk_load__`,
   `__webpack_public_path__`, and a `process` stub.
2. A **client registry** that registers every `"use client"` module found by
   the server build under the id its Flight `I[...]` rows use.
3. Next's unmodified `next/dist/client/app-next` bootstrap.

Next's client compiler setup is copied faithfully:
- `app-pages-browser` React aliases;
- `resolve.fallback` Node polyfills from `next/dist/compiled`;
- the browser-variant swaps, with the module list read from the installed Next's
  `browser-variant-modules.js`;
- the production `navigation-testing-lock.disabled` swap.

**CommonJS, not ESM.** The client uses CommonJS `next/dist`, not
`next/dist/esm` — it's what webpack does, and `esm` lacks client files such as
`build/polyfills/polyfill-module`. Server ids that point into `next/dist/esm`
are mapped to their CommonJS twins in the registry (the same mapping Next's
flight-manifest plugin applies), so the browser holds a single instance of the
router and its React contexts.

**Turbopack-only import attributes** (`with { 'turbopack-transition': … }`) are
stripped from Next's dist files in both the server and browser builds.

### Hydration and navigation (verified behavior)

Verified in Chromium (Playwright) against the packaged binary, run from an empty
directory:
- **Hydration**: a `"use client"` counter in the root layout responds to clicks,
  and `usePathname()` works; no console errors on load.
- **Soft navigation**: clicking a `next/link` fetches `?_rsc=` Flight payloads
  (`text/x-component`, correct `Vary`), with no document reload. Layout state
  survives navigation, and viewport prefetching works.
- **Static files**: `/_next/static/chunks/main-app-<hash>.js` is served with
  `immutable` caching, and `public/` files are served from the root.

### Experimental global not-found

`BuildTree(..., globalNotFound)` emits the `global-not-found` convention into
the root loader tree when `experimental.globalNotFound` is enabled. It selects
the project's `app/global-not-found` file or Next's builtin fallback. No custom
render path is necessary: Next's own app-render template recognizes that loader
tree key and returns the full-document 404.

## Files

- `internal/frameworks/next/client.go` — browser bundle build (webpack-runtime
  shim, client registry, app-next bootstrap, Next client compiler setup).
- `internal/frameworks/next/` — route scanning (`ScanAppPaths`, `GroupRoutes`)
  and `BuildTree` (Go port of next-app-loader's `createTreeCodeFromPath`); server
  bundle and virtual modules.
- `internal/frameworks/next/tree.go` — loader-tree conventions including
  `global-not-found`.
- `testdata/apps/next-global-not-found` — custom full-document 404 fixture.
- `internal/jshost/` — config evaluation in Goja (`loadConfig`, `getDefineEnv`).
- `internal/install/` — npm install without lifecycle scripts or native binaries.
- `runtime/nodeshim/` — Go-backed Node built-ins for the build-time JS.
- `runtime/app/` — startup payload detection and serving.
- `pack/` — executable + zip payload (`server.js`, `meta.json`).

## Test Coverage

- `TestBuildTreeGlobalNotFound` checks the structured loader-tree key for the
  user convention and builtin fallback. The assertion uses
  `'global-not-found': [` rather than a bare token that also appears in the
  fixture directory name.
- The `testdata/apps/next-global-not-found` fixture verifies that Next's own
  app renderer returns the custom full-document 404.
- Removing the loader-tree convention makes the focused tests fail with the
  intended missing-component diagnostic rather than a compile error.

## Pitfalls

- **No code splitting**: one ~550 KB bundle serves every route, and
  `clientModules[*].chunks` is empty.
- If server ids into `next/dist/esm` are not mapped to CommonJS twins, the
  browser ends up with duplicate router/React-context instances.
- `import()` must be lowered for Goja or the server render fails.
- `Buffer.indexOf` must accept any `Uint8Array` to match Node.
- **Not covered here** (later milestones): CSS imports and asset handling are
  ignored in both builds; no `favicon.ico` handling or file-based metadata
  images; route handlers, server actions, and middleware are in
  [[route-handlers-actions-middleware]]; static prerender/ISR renders every
  request. Whether `notFound()` thrown inside a page should stream the
  `__next_error__` shell is unverified and needs an oracle diff against
  `next start`.
- **Binary size**: the packaged binary carries the whole toolchain (~75 MB);
  a runtime-only base binary would roughly halve that.
- App Router conventions are often loader-tree-only ports. Before adding a
  custom renderer, check whether Next's existing app-render template already
  consumes the convention's tree key, as it does for `global-not-found`.
