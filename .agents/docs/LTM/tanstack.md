# TanStack Router and TanStack Start

## Summary

Oganesson compiles TanStack Router (Vite SPA) and TanStack Start (SSR with
server functions) apps into a single Go binary, driving TanStack's own Vite
plugins and build (`buildApp`) through the Rolldown facade, then re-bundling the
server for Goja. This covers the Router SPA, Start SSR/loaders, GET/POST server
functions over RPC, client navigation, streaming/deferred data, server routes,
request and function middleware, prerendering, SPA mode, and the RSC variant
(`@vitejs/plugin-rsc`). See [[vite-and-rolldown-facade]] for the facade and
[[server-programs-ssr]] for the generic server-program runtime.

## Key Facts

- Three fixtures: `testdata/apps/tanstack-router` (Router SPA),
  `testdata/apps/tanstack-start` (Start SSR), `testdata/apps/tanstack-start-rsc`
  (Start RSC).
- Router's `@tanstack/router-plugin/vite` (Babel) runs on the facade:
  generates the route tree into `src/routeTree.gen.ts` and does automatic code
  splitting.
- Start's own `buildApp` builds `client` (`virtual:tanstack-start-client-entry`
  → `dist/client`) and `ssr` (`dist/server/server.js`, default export
  `{ fetch }`). The glue sets `__ogHandler = (req) => server.default.fetch(req)`.
- Server environments keep Node built-ins and `og:*` modules external; they are
  built unsplit (avoids an esbuild `NumberRenamer` data race caught under
  `-race`), then re-bundled for Goja by `bundleServer`.
- Start streams only for non-bot User-Agents (`isbot`); curl is treated as a
  bot, so streaming tests must send a browser User-Agent.
- SPA mode: Oganesson follows Start's own production server — serve the
  prerendered `_shell.html` as a client file and send every other path to the
  server's `fetch`, which renders pages in full. There is no `TSS_SHELL`
  override patch.
- Known upstream limitation: a server function referenced only from a client
  component nested inside a server component is never registered in the rsc
  resolver manifest, yielding "Server function info not found" (500).

## Details

### TanStack Router (Vite SPA)

`@tanstack/router-plugin/vite` generates `src/routeTree.gen.ts` and
code-splits routes automatically. Supporting this surfaced requirements in the
Node shims:

- Host bundles leave a `require()` / `require.resolve()` that must fail as in
  Node when unresolvable (Babel probes optional presets this way, throwing
  `MODULE_NOT_FOUND`), while static imports still resolve.
- nodeshim fs additions: BigInt stats with an exact `mtimeNs` from Go passed as
  a string; `chmod` (Go, confined to the project) and `chown` (existence
  check); `fs.promises.open` returning a path-based `FileHandle` with
  r / w / a / x flag semantics.

Browser behavior: deep link with loader data, and soft navigation that loads
code-split route chunks on demand.

### TanStack Start build and runtime

`buildApp` runs Start's server-function compiler, manifest plugin, and import
protection as written. `vite.Build` reads the environments (consumer, outDir)
and the facade's per-write entries (`og.viteBuilds`); the client environment's
outDir is the client. The first server consumer's entry is bundled for Goja
(`server.go`: nodeshim, `gojatarget.Finish`). `Output.Payload()` (meta,
`client/`, `server.js`) is shared by `og build` and the tests. At runtime a Vite
payload with `server.js` serves client files first, then the per-request VM
handler; there is no ISR layer and no `vite preview` fallback.

Two Goja-compatibility rewrites are required and applied wherever Goja bundles
are produced:

- **The `__require` shim rewrite.** The facade's ESM output turns CommonJS
  `require("util")` (React's Node server build) into esbuild's `__require` shim,
  which throws without a real `require`. Goja bundling rewrites literal
  `__require("x")` calls back to `require("x")` in all server output files (the
  shim lives in a shared chunk, so every file is rewritten, not just the
  definer).
- **Keyword class fields.** Goja's parser treats a class field named `get`,
  `set`, or `async` followed by `=` as a SyntaxError (Hono's `FrozenHeaders`:
  `set = frozen("set")`). `gojatarget.QuoteKeywordClassFields` quotes such
  field names (via `ogapi.ClassFieldNames`). `gojatarget.Finish` = that plus
  `LowerRegExpLiterals`.

Browser behavior (packaged binary): SSR with the GET server function's loader
data then hydration; the POST server function (`/_serverFn/…`) returns `PING`;
client navigation calls the GET server function over RPC; clean console apart
from the favicon.

### Streaming / deferred data

`src/routes/deferred.tsx` has a loader with an unawaited 1.5 s promise under
`<Await>`. The fallback arrives within 1 s, the whole response takes at least
1.2 s, and the fallback does not arrive together with the value. Streaming only
happens for non-bot User-Agents.

### Server routes and middleware

- `src/routes/api/hello.ts`: a server route with GET and POST `server.handlers`.
- Request middleware: `src/start.ts` adds middleware that sets
  `x-og-middleware`.
- Function middleware: `about.tsx` uses function middleware to give a server
  function its context.

These work with no Oganesson changes beyond the base Start support.

### Prerendering

Start prerenders by setting `TSS_PRERENDERING`, starting `vite.preview()` (Node
http serving the built server), fetching each page, and writing `p/index.html`
into the client outDir. Oganesson patches
`start-plugin-core/dist/esm/vite/prerender.js` so `startPreviewServer` is
replaced by `og.rolldown.startPreviewFor(root, clientDir, serverOutDir)` (in
`rolldown.js`). It:

- finds the server build's entry in `og.viteBuilds`;
- passes the build's `TSS_*` environment along;
- calls the Go `preview()`, which bundles the entry for Goja (`bundleServer`),
  puts it with the client files into a MapFS payload, and serves it with
  `app.Handler` on `127.0.0.1:0`.

Prerendering therefore goes through the same code path as the packaged binary;
Start's own `fetch` (webapi) talks to that server. At runtime, with a server,
`runtime/app/vite.go` serves `p/index.html` or `p.html` for GET and HEAD before
handing the request to the server, as a static host serves prerendered pages.

### SPA mode

Start's post-build prerenders the shell with the `X-TSS_SHELL` header
(`post-build.js`) to `_shell.html`. `createStartHandler` reads `TSS_SHELL` from
the environment, which Start never sets for production. Start's own production
server (`vite preview`, its `preview-server-plugin`) serves the client files
(`_shell.html` among them) and sends everything else to the server build's
`fetch`, which renders pages in full.

Oganesson follows that assumption: it does **not** set `TSS_SHELL`. An earlier
approach patched `post-build.js` to record `TSS_SHELL=true` into `og.viteEnv`
(carried as `Output.Env`) so the packaged server rendered the shell for every
page; that patch and the `og.viteEnv` plumbing (`vite.go`) were removed.
`Output.Env` remains for server programs' script environments. The fixture
switches to SPA mode with `OG_FIXTURE_SPA=1` (in `vite.config.ts`).

### The RSC variant

Setup: `tanstackStart({ rsc: { enabled: true } })` plus `@vitejs/plugin-rsc`
0.5.36 and `react-server-dom-webpack`. plugin-rsc's `buildApp` runs five builds:
scans of rsc and ssr, then real builds of rsc, client, and ssr. The ssr output
imports `./rsc/index.js` dynamically, so `bundleServer` folds both into
`server.js`; the rsc output carries its own react-server React.

Facade fixes (`js/rolldown.js`), each needed by plugin-rsc:

- **TS/JSX transform filter.** Tested against the id and against the id without
  its query, as Vite's filter does. Otherwise `index.tsx?tsr-split=component`
  skipped the builtin transform; esbuild masked this by inferring `.tsx` from
  the path, but plugin-rsc's scan strip (es-module-lexer) hit raw JSX ("Parse
  error @:6:34").
- **`this.resolve` skip chain (Rollup semantics).** Every hook call gets a
  context whose `resolve` extends the chain's `{plugin, source, importer}`
  list. Previously only the immediate caller was skipped, so two plugins
  resolving through each other (`rsc:virtual-client-package` and
  `tanstack-start-core:import-protection`) recursed forever, using 20 GB and
  getting SIGKILLed with no message.
- **`renderedExports`.** Computed lazily (acorn) from the module's declared
  exports; `export * from` is followed via `Build.exportsOf(id)` (leaves out
  `default`, guards cycles, keeps `export * as ns` as one name), built from the
  recorded `importer + source -> resolved id` map. Previously always `[]`, so
  plugin-rsc's client-reference group modules came out empty
  (`export_<id> = {}`), SSR got `undefined` for `Counter`, and the boundary
  errored (`<!--$!-->`).
- **Entries resolve once.** esbuild's already-resolved entry ids (e.g.
  `\0virtual`) are not sent back through the plugins.
- **Tracing.** `OG_VITE_TRACE=1` logs every hook call (how the resolve loop was
  found).

Other Oganesson fixes for the RSC fixture:

- **`og:*` external in server environments** (`js/rolldown.js` `isBuiltin`):
  treated like `node:*`. `bundleServer` then resolves them (`kv.Plugin`) so
  Vite server code can use `og:kv`. Client environments still fail to resolve
  them, as they should.
- **Relative paths in build-time fs** (`internal/jshost/fs.go`): the bridge
  resolved relative paths against Oganesson's own working directory, while
  build-time JS sees `process.cwd()` as the project. Symptom: Start's sitemap
  writer (`writeFileSync("dist/client/sitemap.xml")`) hit the write sandbox's
  EACCES, which the sitemap builder only logged. Fix: every bridge operation
  joins relative paths onto the project directory.

### Additional Start features

- **`createIsomorphicFn`** (`src/routes/iso.tsx`): called both in the loader
  (runs server-side) and on click (runs client-side).
- **Selective SSR**: `src/routes/client-only.tsx` (`ssr: false`) has neither
  markup nor server loader data; `src/routes/data-only.tsx` (`ssr: "data-only"`)
  has server loader data but no markup.
- **Sitemaps**: `sitemap: { enabled: true, host: "https://og.example" }` in
  `vite.config.ts`; `sitemap.xml` lists the routes under the host, served as
  XML.
- **RSC CSS**: `greeting.css` imported by a server component produces a client
  CSS asset; the stylesheet link points at it.
- **RSC slots**: `routes/composite.tsx` uses `createCompositeComponent` with a
  render-prop slot and children (both slots fill on SSR).
- **RSC mutation** ("server actions"): Start has no React `"use server"`
  actions; its docs map Next server actions to explicit
  `createServerFn({ method: "POST" })`. `visits.ts` defines a POST server
  function, `visitStore.ts` holds the count in `og:kv` (types via
  `og-env.d.ts`), `BumpButton` calls it then `router.invalidate()`. Server
  function POSTs must carry a same-origin `Origin` (or `Referer`) or Start's
  CSRF middleware returns 403.

## Files

- `testdata/apps/tanstack-router` — Router Vite SPA fixture (route tree,
  code-split routes).
- `testdata/apps/tanstack-start` — Start SSR fixture: GET/POST server functions,
  deferred streaming, server routes, middleware, prerendered `/static`, SPA mode
  (`OG_FIXTURE_SPA=1`), `createIsomorphicFn`, selective SSR, sitemap.
- `testdata/apps/tanstack-start-rsc` — Start RSC fixture: server/client
  components, CSS, composite slots, `og:kv`-backed mutation.
- `internal/frameworks/vite/vite_test.go` — the Start/RSC serve tests.
- `internal/frameworks/vite/rolldown.js` (`js/rolldown.js`) — facade:
  `startPreviewFor`, `isBuiltin` (`og:*` external), resolve skip chain,
  `renderedExports`/`exportsOf`, transform filter.
- `internal/frameworks/vite/server.go` — bundles the server entry for Goja.
- `internal/jshost/fs.go` — build-time fs bridge (BigInt stats, `FileHandle`,
  relative-path joining onto the project dir).
- `runtime/app/vite.go` — runtime: serves prerendered `p/index.html` / `p.html`
  before the server; routes POST to the server.
- `gojatarget` — `QuoteKeywordClassFields`, `LowerRegExpLiterals`, `Finish`;
  `ogapi.ClassFieldNames`.
- Patched Start files: `start-plugin-core/dist/esm/vite/prerender.js`,
  `start-plugin-core/dist/esm/post-build.js` (SPA, now reverted to no override).

## Test Coverage

Fixture-based tests skip when the fixture's `node_modules` is absent; install
with `go run ./cmd/og install <dir>`. The vite package runs ~10 min under
`-race` and needs `-timeout 40m` (541 s with builds shared; `serveStart` caches
builds by fixture name and SPA mode).

- `TestBuildTanStackRouter`: generated route tree (deleted before the build);
  route components split out of the entry.
- `TestServeTanStackStart`: SSR of `/` and `/about`, client assets, 404 for
  unknown paths; middleware header and context; server routes (GET/POST);
  prerendered `/static` served byte-for-byte (fixed build stamp); `/iso` loader
  runs on the server; `/client-only` has no markup/loader data; `/data-only` has
  loader data (`at:"server"`) but no markup; `sitemap.xml` lists routes and is
  XML.
- `TestStreamTanStackStartDeferred`: fallback within 1 s, total ≥ 1.2 s,
  fallback not bundled with the value.
- `TestServeTanStackStartSPA`: no `TSS_SHELL` in env; `_shell.html` built (shell,
  not a page) and served at `/_shell.html`; `/about` rendered in full; server
  routes and prerendered pages unchanged.
- `TestServeTanStackStartRSC`: SSR of the server component with the `Counter`
  client component; no errored boundary; Counter's code in a client chunk;
  stylesheet link points at a client CSS asset containing `#010203`; two POSTs
  to `bumpVisits` (function id from the server bundle manifest, with `Origin`)
  make `/` show "visits 2"; `/composite` SSR fills both slots.
- `rolldown_test.go`: `TestFacadeResolveSkipChain`, `TestFacadeRenderedExports`
  (incl. `export *` across `\0` virtual and file-path modules, a cycle, and
  `export * as ns`) — run the facade directly on the vite-react fixture's
  Rolldown.
- `runtime/app/vite_test.go`: `TestViteAppWithServer` (prerendered files before
  the server; POST to the server).
- jshost: `TestFsFileHandleAndBigIntStats`, `TestFsRelativePaths`.
- gojatarget: `TestQuoteKeywordClassFields`.

Neutralizations (each fails with the intended diagnostic):

| Fix disabled | Failing test and message |
| --- | --- |
| `fs.promises.open` | `TestBuildTanStackRouter` |
| `__require` rewrite | SSR fails |
| class-field quoting | server.js does not compile |
| Prerendered-page serving | `/static` is not the prerendered page |
| SPA (restore the removed patch) | env `TSS_SHELL` present and `/about` fails (page instead of full render) |
| `renderedExports` | RSC boundary errors |
| Transform filter | RSC build parse error |
| Skip chain | "resolve loop: a,b,a,b,…" |
| Entry re-resolve | "could not resolve \0virtual" |
| Deferred with bot User-Agent | "not streamed" |
| `og:*` external | RSC build: could not resolve "og:kv" |
| Relative fs paths | EACCES unit failure; sitemap missing, `/sitemap.xml` 404 |
| `export *` following | renderedExports lacks a, c, d |

Browser checks (Playwright on packaged binaries): Router deep-link + soft nav;
Start SSR+hydration, POST returns `PING`, RPC on navigation; SPA `/about`
renders client-side with the server function result; RSC hydration (counter
increments), client nav fetches the Flight stream through `_serverFn`; `/iso`
click gives "client"; `/client-only` "loaded in browser", `/data-only` "loaded
in server"; RSC greeting colour rgb(1, 2, 3), composite slots after client nav,
bump updates "visits 1" then "visits 2". Only `favicon.ico` 404s in the console.

## Pitfalls

- **Upstream RSC limitation (not fixable in Oganesson).** A server function
  reached only through a client component nested inside a server component is
  never registered in the rsc resolver manifest, so it 500s with "Server
  function info not found". Start fills `serverFnsById` while compiling the ssr
  and client graphs; the rsc build (which owns the resolver) runs before the
  client build, and the ssr scan does not enter client components inside server
  components. The same manifest comes out of upstream `vite build` (verified
  with Node in `.agents-workspace/tmp/rsc-ref`, `og:kv` stubbed). Fixtures
  render `BumpButton` from the route component, the shape Start supports.
- Start's CSRF middleware rejects server-function POSTs lacking a same-origin
  `Origin` or `Referer`; tests must send one, as browsers do.
- macOS SIGKILLs (exit 137) a binary rewritten in place while cached; `rm -f`
  the packaged binary before `og build -o` to the same path.
- Claude Code's shell `grep` skips gitignored paths (`dist/`,
  `.agents-workspace/`), which looks like empty output; use `command grep`.
- Server environments must be built unsplit: the split SSR build triggered a
  data race in esbuild's `NumberRenamer` under `-race`.
