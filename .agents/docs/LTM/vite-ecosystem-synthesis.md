# Vite Ecosystem Synthesis

## Summary

Oganesson runs Vite's own configuration, environment, builder, and plugin code
inside the build host while replacing the native Rolldown binding with an
esbuild-backed compatibility facade. Static apps are served with Vite Preview
semantics; framework-built SSR handlers and project-authored Node HTTP programs
run in fresh request VMs. The facade and runtime have been exercised across the
major Vite framework ecosystems, including TanStack Start and RSC.

## Included Documents

| Document | Focus |
|----------|-------|
| [vite-and-rolldown-facade.md](./vite-and-rolldown-facade.md) | Vite host, Rolldown API facade, assets, chunks, and static serving. |
| [vite-framework-ecosystems.md](./vite-framework-ecosystems.md) | Framework and plugin fixtures and the compatibility work they required. |
| [tanstack.md](./tanstack.md) | TanStack Router and Start, including SSR, streaming, SPA, and RSC. |
| [server-programs-ssr.md](./server-programs-ssr.md) | Project-authored Node HTTP server programs under the request VM model. |

## Stable Knowledge

- Vite 8 runs on `internal/jshost`; `og.rolldown` implements the Rolldown API
  over Go esbuild. Running Rolldown's own threaded napi-rs wasm binding is not
  viable under the current Sobek host. Vite 5 through 7 instead use real
  Rollup via `@rollup/wasm-node` and Oganesson's esbuild npm shim.
- Vite itself owns config resolution, environments, `buildApp`, and JavaScript
  plugins. The facade owns hook dispatch, Rollup-compatible plugin contexts,
  esbuild resolution and transforms, generated bundles, sourcemaps, emitted
  files, file URLs, and chunk naming.
- The facade implements the plugin surfaces required by supported ecosystems:
  hook ordering and filters, `this.resolve`, `this.parse`, `emitFile`, module
  graph queries, combined sourcemaps, manifest generation, and output hooks.
- esbuild decides the actual split graph. Rolldown-style `manualChunks` and
  advanced groups can name compatible chunks but cannot force esbuild to group
  modules differently. Structural byte parity is not a project goal; behavior
  and public asset semantics are.
- Static SPA/MPA payloads use Vite Preview-style base stripping, file serving,
  HTML fallback, immutable hashed assets, and no-cache HTML. VitePress uses its
  own upstream static build API and the normal packager.
- Framework-built SSR outputs are rebundled for Sobek and exposed as Fetch
  handlers. Prerendered client files are served before the dynamic server where
  the framework expects that order.
- A server program is different from a framework handler: the project's own
  Express, Fastify, Koa, Hono, or h3 program executes inside every request VM,
  mounts packaged output at read-only `/`, calls a virtual `listen()`, and
  serves static and dynamic requests itself. WebSocket upgrades are out of
  scope.
- Server programs use Node-style `IncomingMessage` and `ServerResponse` over
  Web streams. Node streams, zlib/brotli, filesystem reads, stdout/stderr, and
  selected Annex B and process behaviors are supplied by the compatibility
  layer.
- Supported fixture ecosystems include Vue, Svelte, Tailwind, Preact, Solid,
  UnoCSS, PWA, Astro, React Router, SvelteKit, Nuxt, SolidStart, Qwik City,
  VitePress, TanStack Router, and TanStack Start.
- TanStack Start uses its own multi-environment `buildApp`. Oganesson supports
  loaders, server functions, middleware, deferred streaming, prerendering,
  selective SSR, SPA mode, sitemaps, and the plugin-rsc variant. `og:*` remains
  external in server environments and is resolved during the final Sobek
  bundle.

### Server splitting nuance

The source documents describe both split and unsplit server output. This is not
a single global rule. SvelteKit required split server environments so shared
render context was instantiated once; the esbuild define race that originally
blocked splitting is mitigated by `globalDefines`. TanStack Start's dedicated
server path remains documented as unsplit. Preserve framework-specific policy
unless a tested unification replaces both.

## Operational Guidance

- Let upstream Vite or the framework orchestrate its build. Add facade behavior
  only when an observed plugin contract is missing.
- Determine whether a project is static, a framework Fetch server, or a server
  program before changing runtime serving. Their static-file and fallback
  precedence differs.
- Match Rollup plugin semantics at the API boundary even when esbuild's graph
  differs. Record unavoidable structural differences instead of attempting an
  unbounded linker reimplementation.
- Use a Node reference build for status, headers, routes, actions, and asset
  behavior, but add browser verification for hydration, code splitting, service
  workers, and client-only transforms.
- For framework fixes, add a focused facade or host test in addition to the
  end-to-end fixture and neutralize the actual fix.

## Files

- `internal/frameworks/vite/host.go`, `esbuild.go`, `js/rolldown.js`,
  `js/binding-stub.js`, `js/parse.js` - Vite host and Rolldown facade.
- `internal/frameworks/vite/vite.go`, `server.go`, `scripts.go`, `program.go` -
  project detection, output rebundling, script interpretation, and programs.
- `internal/frameworks/vite/{astro,nuxt,vitepress}.go` - dedicated upstream
  framework adapters.
- `runtime/app/vite.go`, `runtime/vfs` - static/server payload serving and the
  packaged filesystem.
- `runtime/nodeshim/js/http.js`, `runtime/nodeshim/zlib.go` - server-program
  HTTP and compression adapters.
- `testdata/apps/vite-*`, `testdata/apps/tanstack-*` - ecosystem fixtures.

## Tests

- Facade tests include `TestBuildReactSPA`, `TestBuildViteFeatures`,
  `TestFacadeChunkNames`, `TestFacadeRenderedExports`,
  `TestFacadeResolveSkipChain`, and `TestReplaceImportMetaURL`.
- Ecosystem tests use `TestBuild*` for static frameworks and `TestServe*` for
  SSR. Representative suites are `TestBuildVitePress`, `TestServeAstroNode`,
  `TestServeSvelteKit`, `TestServeNuxt`, `TestServeSolidStart`, and
  `TestServeQwikCity`.
- TanStack coverage includes `TestBuildTanStackRouter`,
  `TestServeTanStackStart`, `TestStreamTanStackStartDeferred`,
  `TestServeTanStackStartSPA`, and `TestServeTanStackStartRSC`.
- `TestServeViteSSRServers` compares Express, Fastify, Koa, Hono, and h3 server
  programs, including compression and static behavior.

## Pitfalls

- Vite/Rolldown and esbuild output can differ in chunk grouping, names, CSS
  asset names, minifier identifiers, and serialized function source while
  remaining behaviorally equivalent.
- A curl comparison cannot detect client leakage or hydration bugs. The
  SolidStart server-function regression was visible only in a browser.
- Plugin resolution must carry the complete skip chain; skipping only the
  immediate plugin permits recursive resolve loops.
- Streaming tests need a non-bot User-Agent. Server-function POSTs may require
  a same-origin `Origin` or `Referer` because framework CSRF middleware runs.
- Start's RSC manifest has an upstream limitation for server functions reached
  only through a client component nested in a server component.
- Startup output is suppressed after the first server-program request by the
  runtime's process-wide startup-output guard; errors printed only during later
  startup runs are suppressed too.
