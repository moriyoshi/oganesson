# Route Handlers, Server Actions, and Proxy/Middleware

## Summary

Oganesson runs Next.js App Router `route.ts` handlers, Server Actions (both
module-level `"use server"` files and inline `"use server"` closures with
encrypted bound args), and `proxy.ts` / `middleware.ts` in the packaged Go
binary. Each of these reimplements the slice of Next's SWC transform, flight
loaders, and router server that Oganesson needs, while reusing Next's own entry
templates and manifest helpers wherever possible. All three run in the
per-request isolated Sobek VM model (see [[foundations-and-architecture]]).

## Key Facts

- Route handlers wrap Next's `edge-app-route` template around its `app-route`
  template; the handler runs in the RSC layer as in Next.
- Dynamic route params arrive as query-string entries `nxtP<name>`, the way
  Next's server delivers them to `EdgeRouteModuleWrapper`; the adapter
  normalizes and strips them.
- Server Action ids are 42 hex chars: info byte `7f` ("server action, all args
  used, rest args") + SHA-1(build id + file + export name). The inline scheme
  uses the same `"7f"` + sha1(build id, path, export name).
- Module-level actions: `internal/frameworks/next/actions.go`. Inline closures:
  `inline_actions.go` (same package). Closure analysis lives in
  `_tsoverlay/ogapi/actions.go` (`ogapi.ServerActions`).
- The action encryption key is `NEXT_SERVER_ACTIONS_ENCRYPTION_KEY` (generated
  per build), read by Next's `getActionEncryptionKey` /
  `encryptActionBoundArgs` / `decryptActionBoundArgs`.
- Proxy/middleware accepts `proxy.ts` (Next 16 name) and `middleware.ts`,
  wrapped with Next's `middleware` template.

## Details

### Route handlers (`route.ts`)

Each handler is Next's `edge-app-route` template wrapping its `app-route`
template; the handler lives in the RSC layer, as in Next. Dynamic params reach
`EdgeRouteModuleWrapper` the way Next's server delivers them: appended to the
query string as `nxtP<name>`, which the adapter normalizes and strips. Verified
surfaces: GET/POST, `Response.json`, `NextResponse` with custom headers,
`next/headers`, dynamic `[slug]`, and handler-produced 404s. The RSC layer
these handlers and actions run in is the same one described in
[[app-router-rendering]].

### Module-level Server Actions (`"use server"` files)

`internal/frameworks/next/actions.go` replaces the parts of Next's SWC
transform and flight loaders that Oganesson needs:

- **Ids:** 42 hex chars, info byte `7f` followed by SHA-1(build id + file +
  export).
- **RSC layer:** the module's own code runs, followed by
  `ensureServerEntryExports` and `registerServerReference` (via a self-import,
  so `default` exports work).
- **SSR and browser layers:** the module is replaced by
  `createServerReference(id, callServer, …)` stubs from Next's
  `action-client-wrapper`.
- **Registry:** `og:actions` (RSC layer) maps action id → function and is
  reachable through `__next_require__`.
  `__RSC_SERVER_MANIFEST.edge[id].workers["app<page>"]` points every page at
  it. (Older JOURNAL entries call this `gots:actions`, pre-rename.)
- **Verified:** RPC calls from client components return values; `<form action>`
  works with and without JavaScript (multipart `$ACTION_ID_…`); `cookies().set`
  and `revalidatePath` work; unknown ids get 404 from Next's action handler.

### Inline `"use server"` closures (encrypted bound args)

Inline `"use server"` closures were initially rejected at build time with a
clear message; they are now supported in the RSC layer.

- **`ogapi.ServerActions`** (`_tsoverlay/ogapi/actions.go`) finds functions
  whose directive prologue holds `"use server"` and computes what they close
  over using TypeScript-Go's binder (`binder.BindSourceFile`, then each
  reference's declaring container via `Locals()`): values of enclosing
  functions and blocks, not of the module, globals, or the function itself.
  Captures are narrowed to the property paths actually read
  (`user.profile.greeting`), stopping before optional chains, computed members,
  a method call's last member, and assignment / `delete` targets. A path read
  through a shorter captured path becomes that capture plus a suffix, since
  next-swc keeps the shortest. It also reports kind, name, async, export
  modifiers, parameter and body ranges, nesting, and `this` / `arguments` /
  `super` use.
- **`inline_actions.go`** applies next-swc's server_actions transform in the
  RSC layer. Each action is hoisted after the directive prologue as:

  ```js
  export const $$RSC_SERVER_ACTION_<n> = async function name($$ACTION_CLOSURE_BOUND, …) {
    var [$$ACTION_ARG_0, …] = await decryptActionBoundArgs(id, $$ACTION_CLOSURE_BOUND);
    …
  }
  ```

  and registered with `registerServerReference`; captured reads become
  `$$ACTION_ARG_<i>` (shorthand properties expanded). The original site becomes
  `$$RSC_SERVER_ACTION_<n>.bind(null, encryptActionBoundArgs(id, …captures))`
  (`var name = …` for declarations, with `export` / `export default` kept), or
  the bare reference when there are no captures. The hoisted exports are the
  module's actions (`addInlineActions`) in the registry and manifest. Ids
  follow the module-level scheme (`"7f"` + sha1 of build id, path, export
  name).

The `"use cache"` transform shares this inline-actions machinery; see
[[cache-components-use-cache]].

Nested inline actions are transformed in two passes. Every action is hoisted
first; then an action nested directly inside another is replaced within the
parent's hoisted body by its encrypted binding. A nested action that shares a
capture with an enclosing action remains rejected because the parent's
decrypted argument would need to be threaded into the child; `sharesCapture`
detects this by source position and emits a clear build error.

`transformInlineActions` also receives `moduleUseServer`. In a module whose
top-level directive is `"use server"`, a redundant directive on a top-level
function no longer causes that function to be both inline-hoisted and
module-registered. It stays a module action, while inline `"use cache"`
functions in the same file are still hoisted.

### Proxy / middleware (`proxy.ts`, Next 16; `middleware.ts` also accepted)

The file is wrapped with Next's `middleware` template.

- **Matchers:** the file's `config.matcher` is evaluated at build time in
  jshost and converted by Next's own `getMiddlewareMatchers`, lifted verbatim
  from the installed `get-page-static-info.js` because importing that module
  pulls in Node-only dependencies. At runtime, Next's
  `getMiddlewareRouteMatcher` applies them, including `has` / `missing`
  conditions.
- **Response protocol** (follows Next's router server):
  - `x-middleware-next`, `x-middleware-rewrite` (same-origin re-dispatch;
    external URLs proxied with `fetch`), redirects, and direct responses;
  - `x-middleware-override-headers` / `x-middleware-request-*` request header
    forwarding;
  - response headers and cookies merged into the route's response;
  - internal `x-middleware-*` headers stripped.
- **Static assets** (`/_next/static`, `public/`) are served before the proxy
  runs. Next would run the proxy for them unless the matcher excludes them, as
  the common matcher pattern does.

## Files

- `internal/frameworks/next/actions.go` — module-level `"use server"`
  transform: id scheme, RSC/SSR/browser layer rewrites, registry and manifest
  wiring.
- `internal/frameworks/next/inline_actions.go` — inline `"use server"` closure
  transform: hoisting, `registerServerReference`, bind-site rewrite,
  `addInlineActions`, encrypted bound args.
- `_tsoverlay/ogapi/actions.go` (`ogapi.ServerActions`) — closure-capture
  analysis via TypeScript-Go's binder; capture narrowing to read property
  paths; reports nesting, `this` / `arguments` / `super`, parameter defaults.
- Route handler and middleware wrapping live in the same
  `internal/frameworks/next` package (Next's `edge-app-route` / `app-route` /
  `middleware` templates and `getMiddlewareMatchers` lifted from
  `get-page-static-info.js`).

## Test Coverage

Go tests in `internal/frameworks/next` (fixture-based; skip when the fixture's
`node_modules` is absent — install with `go run ./cmd/og install <dir>`):

- `TestTransformInlineActions` — exact output for a declaration with captures
  through property paths, a method call and a shorthand property, an arrow
  without captures, and an exported module-level action; verifies narrowing;
  checks the output parses as TSX; covers nested actions and the error cases.
- `TestInlineActionsUseServerModule` — a redundant top-level directive in a
  `"use server"` module remains module-registered once, while inline cache
  functions are still transformed.
- `TestInlineServerActions` — on `testdata/apps/next-hello/app/inline/page.tsx`:
  the bound action's form posted without JavaScript (React's `$ACTION_REF`
  fields carry the encrypted closure; the page HTML does not contain the
  captured value in clear), and the module-level action called over RPC.
- Neutralizations confirmed failing as intended: no binding at the original
  site, no narrowing, no registration, not in the registry.

Browser verification (packaged binary, Playwright): route handler GET/POST and
404s; module-level actions over RPC and via `<form>` with and without JS; the
bound inline action from the client router (soft nav, `hi og js`), the form
reset after it, and the module-level action.

## Pitfalls

- **Inline-action shapes still rejected** (explicit build errors): nested
  actions that share a capture with an enclosing action, object/class
  **methods**, non-async functions (Next's own message), use of `this` /
  `arguments` / `super`, and **parameter defaults that read closed-over
  (captured) values**. Inline actions outside
  the RSC layer (i.e. in Client Components) get Next's error: "It is not allowed
  to define inline \"use server\" annotated Server Actions in Client
  Components."
- **Capture narrowing is load-bearing.** Without narrowing to the read property
  paths, a capture of an object that holds a function fails at run time
  ("Failed to find Server Action"); next-swc's narrowing avoids exactly this.
- **`id="reset"` shadows `form.reset()`.** A button with `id="reset"` inside a
  form shadows `form.reset()`, which React calls after an action, causing
  "t.reset is not a function". This is a fixture bug, identical under Node.
- **Per-request isolated VMs:** module-level mutable state does not survive
  between requests. Keep cross-request state in cookies or external stores. Two
  invariants follow: ISR and data caches must live on the Go side, outside the
  VM; and a future `og compat` check could warn about module-level mutable
  state, since it does not persist across requests. See
  [[foundations-and-architecture]].
