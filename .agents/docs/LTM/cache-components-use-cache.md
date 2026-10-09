# Cache Components and "use cache"

## Summary

Oganesson supports Next's Cache Components / `"use cache"`: a build-time transform
hoists cached functions like inline actions, a shared Go-backed cache handler
replaces Next's per-module in-memory store (og runs a fresh VM per request),
Cache Components pages are statically prerendered, and partial prerendering
serves a cached shell resumed per request. Custom `cacheHandlers`, `"use cache:
private"`/`"use cache: remote"`, and `cacheLife` profiles are wired in, and a
related fix sets the server's origin so server-action redirects can fetch their
own RSC payload. The transform shares machinery with
[[route-handlers-actions-middleware]]; the entry store is
[[static-generation-and-isr]].

## Key Facts

- The `"use cache"` transform was built against next-swc's server-actions
  fixtures at v16.4.0 (fixtures 33 to 73).
- Cached functions hoist like inline actions, sharing their counter:
  `$$RSC_SERVER_CACHE_<n>_INNER` (body without directive) and
  `export var $$RSC_SERVER_CACHE_<n> = cache(function name() { return $$cache__(kind, id, boundCount, INNER, args) })`.
  `cache` is React's; `$$cache__` is Next's `next-flight-loader/cache-wrapper`.
- Shared cache handler lives in `runtime/isr/usecache.go`, installed on the JS
  side as `Symbol.for("@next/cache-handlers").DefaultCache`; `revalidateTag`
  reaches it via `updateTags`.
- Partial prerendering uses `isr.PostponedHeader` (shell state, base64) and
  `isr.ResumeHeader` (per-request resume); the ISR `Entry.Postponed` persists in
  `prerender.json`.
- `__NEXT_PRIVATE_ORIGIN` must be set from the listener so server-action
  redirects can fetch the target's RSC payload (`runtime/app/serve.go`).
- `og compat -env file` (default `testdata/compat/env.json`) supplies dummy env
  values to corpus apps that need only a secret.

## Details

### The transform

Built against next-swc's server-actions fixtures at v16.4.0 (33 to 73, fetched
into a scratch sparse checkout). Lives in `inline_actions.go` with
`_tsoverlay/ogapi/actions.go`.

Cached functions are **hoisted like inline actions, sharing their counter**:

- `$$RSC_SERVER_CACHE_<n>_INNER`: the body without its directive. Captured
  values arrive decrypted as one array, the first parameter.
- `export var $$RSC_SERVER_CACHE_<n> = cache(function name() { return $$cache__(kind, id, boundCount, INNER, args) })`.
  `args` is `[]`, `slice(arguments)` for a rest parameter, or
  `slice(arguments, 0, n)`.
- The export is registered as a server reference, renamed after the original,
  and the original place binds it
  (`.bind(null, encryptActionBoundArgs(id, …captures))` when it captures).

**Ids:** the info byte is 0x80, plus a bit per argument used among the first six
(the bound blob counts as the first), plus the low bit for a rest parameter or
more than six. The fixtures' e0, ff and c0 hold.

**`ogapi`:**

- `ServerActions` reports each function's directive (`"use server"`,
  `"use cache"`, `"use cache: kind"`) and the directive statement's span;
- an anonymous function initializing a variable takes the variable's name, as
  next-swc names hoisted functions (inline actions too: `async function plain()`);
- `CacheFileFunctions` lists a module-level `"use cache"` file's exported async
  functions (declarations, variable initializers, export clauses, default).

**The builder:**

- `inlineDirectiveRe` / `useCacheFileRe` gate it;
- a `"use server"` module's inline `"use cache"` functions are hoisted before its
  exports register, and the registry entry merges both (`actionModule.exports`,
  merging `addInlineActions`);
- `"use cache"` in a client layer is an error.

### The shared cache handler

Next's default handler keeps entries in the module's memory, which is per request
in og (a fresh VM each time). `runtime/isr/usecache.go` keeps the entries
(metadata JSON and the RSC stream's bytes, evicting the oldest past 50 MB,
Next's default `cacheMaxMemorySize`) and the tags manifest (stale / expired ms),
through new `og.cache` functions.

The Next entry installs a JS handler as
`Symbol.for("@next/cache-handlers").DefaultCache` with the default handler's exact
checks: expire < 0, the revalidate age, expired tags (a miss), stale tags
(revalidate -1), and skipping entries with expire 0. `revalidateTag` from a
server action reaches it through `updateTags`.

### Static prerendering

Next's Cache Components modules refuse the edge runtime: CacheSignal,
runInSequentialTasks, createAtomicTimerGroup, the fast setImmediate, the resume
data cache, and Node stream conversions each throw an "… edge runtime"
InvariantError, because Next's edge has no `process.nextTick` / `setImmediate` or
Node streams.

og's edge templates have all three (nodeshim). In the listed modules
(`nodeRuntimeModules` in `patches.go`), every
`process.env.NEXT_RUNTIME === 'edge'` test becomes false, so they take Next's
Node branches. A listed module without the test fails the build, as an unmatched
patch does. The fast setImmediate now installs in every Next app.

Timers gained Node's `_idleStart`: when a timer was armed, writable to move its
due time, as in Node. `createAtomicTimerGroup` copies the first timer's onto the
group, and warned without it ("cannot guarantee that Cache Components will run as
expected due to … setTimeout()"). `TestTimerIdleStart` covers Node 24's order.

Debugging note: the static attempt's console is held back during build-time
prerender, and the server never retries a route the build found dynamic. A
throwaway test inside `runtime/app` that unwrapped the ISR handler and sent
`isr.StaticHeader` directly (the unexported `handler` / `unwrap`) showed each edge
invariant in turn; it was deleted afterwards.

### Partial prerendering (a cached shell, resumed per request)

Follows Next's app-page handler (`app-page-runtime.js`: on a postponed cache
entry, `body.push(transformer.readable)` and `doRender({ postponed })`).

- **Build or first request:** the static render that postpones (a dynamic hole
  such as cookies inside Suspense on `/`) returns its shell (the prelude, without
  closing tags) and reports `metadata.postponed`. The edge-ssr-app patch sends
  the cache metadata for it plus the state in `isr.PostponedHeader` (base64). The
  ISR entry keeps it (`Entry.Postponed`, in `prerender.json` as well).
- **Serving such an entry** (`writeResumed`): the shell with `x-nextjs-cache`,
  `x-nextjs-postponed: 1` and Next's uncacheable Cache-Control
  (`private, no-cache, no-store, max-age=0, must-revalidate`, since its tail is
  per request), flushed. Then a resume render of the same request is streamed
  after it through an `appendWriter` that drops its status and headers.
- **The resume request** carries the state in `isr.ResumeHeader`. The template's
  renderOpts get `postponed: __ogPostponed`, so app-render resumes the postponed
  tree (React's resume) and emits only the rest: the hole's content as a hidden
  segment, with the `$RC` swap script and the closing tags. On `/` with a cookie:
  the `loading` fallback in `<template id="B:0">`, then `S:0` with `visitor bob`,
  and one `</html>`.
- **RSC requests** (client navigations) of such a page are not served from the
  shell: the page renders. The ISR also skips caching a postponed render's RSC
  payload, but the static render reports none (no `flightData`), so that guard is
  defensive (no test covers it).
- The build summary marks such routes "◐ partial prerender (static shell, dynamic
  parts per request)" (`PrerenderResult.Partial`).

### Custom cacheHandlers and "use cache: private" / "use cache: remote"

`cacheHandlers` from `next.config` are wired into the edge templates the way
Next's loaders do it (`getCacheHandlersSetup` in next-edge-ssr-loader and
next-edge-app-route-loader). `builder.cacheHandlersSetup`
(`internal/frameworks/next/virtual.go`) fills three `INJECT_RAW` slots:

- `cacheHandlerImports`: one `import edgeCacheHandler_<i>` per configured kind;
- `cacheHandlerRegistration`: `cacheHandlers.setCacheHandler(kind, …)`, in
  edge-ssr-app and edge-ssr (`pages.go`);
- `edgeCacheHandlersRegistration`: `edgeCacheHandlers[kind] = …`, in
  edge-app-route.

Kinds are sorted, because a Go map doesn't keep `Object.entries` order. A `file:`
URL becomes its path (Next's `resolveCacheHandlerPathToFilesystem`). A relative
path is resolved against the project directory. An empty entry is skipped. The
registrations run after `initializeCacheHandlers`. A configured `default`
therefore replaces og's shared store (`useCacheHandlerSource`), as it replaces
Next's in-memory handler under next start.

`"use cache: remote"` needs nothing: `initializeCacheHandlers` falls back to the
`DefaultCache` symbol, which is og's store. `"use cache: private"` already worked
through Next's own path, the resume data cache, with no handler outside dev.

A custom handler's module state does not survive across requests (each request is
a fresh engine); a real handler needs a Go-backed store. A `"use cache: kv"`
function must be called per request (e.g. after `connection()`), not at build-time
prerender, or it is prerendered whole and the runtime handler never runs.

### cacheLife profiles

No code change was needed: the edge templates read `nextConfig.cacheLife` from
`__SERVER_FILES_MANIFEST.config`, which is the whole loaded config. Dropping
`cacheLife` from the manifest config breaks even the built-in `"hours"` profile
(unknown), returning 500.

### Server-action redirects need the server's origin

Found with the `with-iron-session-cache-components` corpus example (needs only
`SESSION_PASSWORD`). With the secret set, logging in failed in the browser with
"An unexpected response was received from the server"; the server logged
`failed to get redirect response … Get "http://localhost/?_rsc": … [::1]:80: connection refused`.

- **Cause:** when a server action redirects to an app path and the client router
  called it, Next's `createRedirectRenderResult` (`action-handler.js`) fetches the
  target's RSC payload from the server itself to return alongside the action
  response. The origin comes from `process.env.__NEXT_PRIVATE_ORIGIN`, which next
  start's `start-server.js` sets to `http://<host>:<port>` once it listens.
  Without it, the origin falls back to `initURL`, which `RouteModule.prepare`
  builds as `http://localhost<url>` when there is no router server context. On
  failure Next returns an empty result and the client throws.
- **Fix:** `app.Serve` now listens first. Unless the environment already sets it,
  it sets `__NEXT_PRIVATE_ORIGIN` from the listener (`privateOrigin`: an
  unspecified address becomes `localhost`, IPv6 is bracketed) before `Handler`,
  which snapshots the environment. `Serve` is split so that
  `serve(ctx, fsys, ln, wrap)` takes the listener (`runtime/app/serve.go`).
  `og serve`, packaged binaries and ejected projects all go through it. The
  redirect fetch is real HTTP to the server's own address, as under next start
  (correctness over speed).
- After the fix, login redirects to `/` signed in, with cached announcements, the
  streamed session and the user's notes. Adding a note does not persist: the
  example's database is a module-level `Map`, reset with each request's engine
  (per-request VMs, by design).

### og compat -env

`og compat -env file` (default `testdata/compat/env.json`): app name ->
`{VAR: value}`, dummy values only. The values go to the app's build (`compatRun`
takes an env list; install gets none) and its server (`srv.Env`). An app that
ships a `.env` example but has values here is no longer excused as `env`: its
failures count as unexpected. The report lists the names it got
(`compatResult.Env`); the summary's ENV column says `env set`.
`testdata/compat/env.json` gives `with-iron-session-cache-components` a
`SESSION_PASSWORD`. Run 9 reported `/ 500` for that example; it now passes (the
500 was fixed by the partial prerendering work). The crawl sends no session
cookie, so `getSession` returns early and never reads the password; the secret
matters only for login.

## Files

- `internal/frameworks/next/inline_actions.go` ... the `"use cache"` transform
  (hoisting, info bytes, slicing), shared with inline actions.
- `_tsoverlay/ogapi/actions.go` ... `ServerActions`, `CacheFileFunctions`, and
  directive/span reporting.
- `internal/frameworks/next/patches.go` ... `nodeRuntimeModules` forcing Next's
  Node branches in Cache Components modules under og's edge runtime.
- `internal/frameworks/next/virtual.go` ... `builder.cacheHandlersSetup` filling
  the three `INJECT_RAW` cache-handler slots.
- `internal/frameworks/next/pages.go` ... edge-ssr cache-handler registration.
- `runtime/isr/usecache.go` ... the shared Go-backed cache handler (entries, 50 MB
  eviction, tags manifest), installed as `DefaultCache`.
- `runtime/app/serve.go` ... `serve(ctx, fsys, ln, wrap)` listens first and sets
  `__NEXT_PRIVATE_ORIGIN` (`privateOrigin`, `PrivateOriginEnv`).
- `testdata/apps/next-cache` ... fixture (`cacheComponents: true`, `cacheLife`,
  `cacheHandlers`): `app/static`, `app/custom` (+`/stats`), `app/private`,
  `app/brief`, `app/go`, `lib/kv-cache-handler.ts`.
- `testdata/compat/env.json` ... per-app dummy env values for `og compat -env`.

## Test Coverage

- `TestTransformUseCache` (`internal/frameworks/next`) checks fixtures 33, 34, 38,
  39, 46: info bytes, kinds, bound counts, argument slicing, call sites, and which
  file-level exports are wrapped. `TestTransformInlineActions` updated for the
  arrow name.
- `TestCacheComponents` (`testdata/apps/next-cache`): a cached stamp stable across
  requests; a cookie reaching the dynamic hole; `revalidateTag` from a form action
  giving a new value; `/static` prerendered HIT; the HIT + postponed headers and
  Cache-Control; the fallback before the resumed cookie in one document with one
  `</html>`; a cookie reaching an RSC request of `/`; the kv stamp stable on two
  requests; stats exactly `{"get":2,"set":1}`; `/private` greeting each request's
  cookie (alice, then bob); the `cacheLife("brief")` value stable then changing
  1.5s later; `app/go`'s redirect action returning `x-action-redirect: /static…`,
  `text/x-component`, and the target content (the test makes its listener first
  and sets `app.PrivateOriginEnv` as `Serve` does). Neutralizing any piece (shared
  handler, postponed guard, Node branches, transform, `_idleStart`, resume write,
  origin) reproduces the matching failure.
- `TestCacheHandlersSetup`: imports and registrations for a `file:` URL, a
  relative path, and an empty entry, in kind order (neutralized: build fails
  `Unknown cache handler: kv`; or the import is `/proj/file:/abs/remote.js`).
- `TestTimerIdleStart`: Node 24's timer order with `_idleStart`.
- `runtime/app/serve_test.go`: `TestPrivateOrigin` (address forms);
  `TestServeSetsPrivateOrigin` (`serve` sets the variable from its listener, keeps
  an existing value).
- `TestCompatEnv` (parsing, sorting, repo file, missing file); `TestCompatRunEnv`
  (`compatRun` with `/bin/sh` echoes the value). The server's `srv.Env` line has
  no automated test (verified by a real compat run).

Fixture-based app tests skip when the fixture's `node_modules` is absent; install
with `go run ./cmd/og install <dir>`.

## Pitfalls

- A `"use cache"` function must do its dynamic access per request (inside Suspense,
  or after `connection()` / a cookie read); called outside with no dynamic access
  it is prerendered whole at build time and the runtime handler never runs.
- A custom cache handler's JS module state does not survive between requests
  (per-request VMs); back real handlers with a Go store (e.g. `og:kv`).
- A listed `nodeRuntimeModules` entry without the
  `process.env.NEXT_RUNTIME === 'edge'` test fails the build (unmatched patch).
- Without `__NEXT_PRIVATE_ORIGIN`, server-action redirects fetch
  `http://localhost:80` and fail; the server must listen before snapshotting the
  environment.
- A local port probe can hit an unrelated leftover process; check
  `lsof -iTCP:<port>` before trusting it.
- Adding a note / mutation does not persist in examples whose store is a
  module-level `Map`: it resets per request by design.
