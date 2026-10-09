# JavaScript Language Gaps and Bundle-Time Lowering

## Summary

The Goja / Sobek JavaScript engine that Oganesson runs per request does not
implement the full modern JS language or standard library. Oganesson closes the
gap two ways: esbuild lowers the syntax it can, and a family of Oganesson-owned
bundle-time transforms (`gojatarget.*`, applied to every engine-bound bundle)
rewrite the constructs esbuild leaves alone or that the engine mishandles. This
document catalogs the engine gaps, the lowering/transform workarounds that
compensate, and three esbuild build-determinism hazards (the NumberRenamer race,
the object-valued define race, and function-name loss) with their mitigations.

## Key Facts

- Pinned engine-adjacent versions (M0): Goja `0f92c903ca4a` (2026-09-30),
  goja_nodejs `b481721df8a2`, esbuild v0.28.2, TypeScript-Go `edf7da4e93ab`,
  React / react-dom / react-server-dom-webpack 19.3.0, web-streams-polyfill
  4.3.0.
- esbuild lowers: async generators, decorators, `using`, `import.meta`,
  top-level await.
- esbuild does NOT touch regexp contents, so RegExp `\p{…}` property escapes and
  the `/d` and `/v` flags need their own solution (build-time regexp rewriter or
  upstream engine support). See [[unicode-regexp-intl]].
- Missing built-ins to polyfill (M0 list): all of `Intl`, `WeakRef`,
  `FinalizationRegistry`, `structuredClone`, `queueMicrotask`,
  `Error.captureStackTrace`, `Promise.withResolvers`, `Array.fromAsync`,
  `Object/Map.groupBy`, `String#isWellFormed`, `Symbol.dispose`,
  `ArrayBuffer#transfer`, iterator helpers, `SharedArrayBuffer` / `Atomics`,
  `WebAssembly` (routed to wazero).
- `gojatarget.*` transforms run as part of `Finish` on every engine-bound
  bundle. Known members referenced across the project include
  `HoldArrowArguments`, `ContextualThenables`, `HoistDefaultedArrayPatterns`,
  `QuoteKeywordClassFields`, and `installProxyHasInstance`. The engine-side
  details of the engine itself live in [[runtime-engine]]; this document details
  `ContextualThenables`.
- Thenable `then` methods lose `AsyncLocalStorage` context in Sobek; fixed at
  bundle time by `gojatarget.ContextualThenables` + `__ogContextThen`.
- Two esbuild build-determinism races exist with object/array-valued defines;
  the general mitigation is `globalDefines` in `esbuild.go`.
- Function names (`fn.name`) are preserved in build-time `require` via esbuild
  `KeepNames` + a global `__name` helper; NOT applied to the host bundle.

## Details

### Engine language gaps (M0 survey)

Discovered via `go run ./spikes/m0/features`.

Syntax esbuild can lower (so no further work): async generators, decorators,
`using`, `import.meta`, top-level await.

Not lowerable by esbuild, needing their own solution: RegExp `\p{…}` property
escapes plus the `/d` and `/v` flags. esbuild leaves regexp contents alone; the
options are a build-time regexp rewriter (regexpu-style, in Go) or upstream
support in the engine. See [[unicode-regexp-intl]].

Missing built-ins polyfilled by the runtime: `Intl` (all of it), `WeakRef`,
`FinalizationRegistry`, `structuredClone`, `queueMicrotask`,
`Error.captureStackTrace`, `Promise.withResolvers`, `Array.fromAsync`,
`Object/Map.groupBy`, `String#isWellFormed`, `Symbol.dispose`,
`ArrayBuffer#transfer`, iterator helpers, `SharedArrayBuffer` / `Atomics`,
`WebAssembly` (routed to wazero).

Host-provided Web APIs first stubbed in `spikes/m0/shim` and later moved into
`runtime/webapi`: TextEncoder / TextDecoder, AbortController, MessageChannel.

Parser panic on mixed escapes: Goja / Sobek's parser panics (rather than raising
a `SyntaxError`) on a string literal that mixes a `\uXXXX` escape with an astral
`\u{…}` escape (e.g. a `"\x00\u{10FFFF}"`-style literal). The bundle-time
workaround is esbuild's `Charset: UTF8` (set in `internal/gojatarget`), which
emits the characters directly instead of as mixed escapes; at the source level
`String.fromCodePoint` avoids the construct. The engine loop also converts any
such panic into a `PanicError`, so one bad request cannot crash the server.

### Thenable async-context loss and its fix

The engine gap: Sobek's `newPromiseResolveThenableJob` neither captures nor
resumes the `AsyncContextTracker` context, whereas ordinary promise reaction
jobs do. Concretely, inside an awaited thenable's `then`, `als.getStore()` is
`undefined` on og (Sobek) but is the store on Node 24; after the `await` both
have it. This surfaced for Astro: with `experimental.incrementalBuild`, Astro
installs a render scope (`core/render-scope`, an `AsyncLocalStorage`) that
records per-prerendered-page content entries and images; a page or library that
awaits a thenable (e.g. Drizzle-style query builders that run work inside
`then`) loses that recording. Without the flag no render scope is installed.

Root cause mechanics: Sobek reads `thenable.then` synchronously while resolving
(that is the context Node runs the thenable job in) and calls it later from a job
with no tracker hooks. A runtime hook can observe neither point; the synchronous
`then` read is the one place reachable from JavaScript. (A prior entry briefly
blamed this same thenable gap for SvelteKit's error page returning 500; that was
wrong — the real cause there was unsplit server builds, see the define-race
section — but the gap itself is real.)

The fix is a bundle-time transform rather than patching Sobek:

- `ogapi.ThenMethods` (`_tsoverlay/ogapi/module.go`, re-installed into
  `third_party/typescript`) marks `then` methods of classes and object literals
  (static, async, generator kept) and object literals' `then: function …` as
  function expressions. Computed names, accessors, arrow values, and methods
  using `super` are skipped; nested ones are not listed.
- `gojatarget.ContextualThenables`, part of `Finish` on every engine-bound
  bundle, rewrites each marked `then` to:
  ```js
  get then() { return (globalThis.__ogContextThen || ((f) => f))(function then(…) {…}); }
  ```
  plus a setter that defines an own data property (so `x.then = …` keeps
  working).
- `__ogContextThen` (`runtime/engine/als.go`) is the function that wraps the
  inner `then` to run in the async context current at the call (i.e. the
  getter's read), preserving the name `then` and the original `length`.

Observable differences from Node: `then` is now an accessor and each read yields
a new function (`x.then !== x.then`); a `then` read in one context and called in
another runs in the first context. Uncovered shapes (still lose context):
`then` attached at run time (`obj.then = fn`, prototype assignment,
`defineProperty`), arrow-valued `then`, and `then` using `super`.

The alternative considered was an upstream Sobek PR (Grab at enqueue,
Resumed / Exited around the `then` call, as reaction jobs do), or a patched
Sobek via `replace` until it lands. No JS-level workaround can keep Node's exact
timing short of calling `then` synchronously.

### esbuild NumberRenamer race

An unminified (`build.minify: false`) copy of vite-react races under `-race` on
every build. Both goroutines are in `renamer.(*NumberRenamer).assignName` from
`AssignNamesByScope` (`linker.renameSymbolsInChunk`). Narrowing: no splitting →
no race; no `define` → no race; no target → still races. Bisecting the 15 Vite
defines, only the object-valued `process.env = {}` (and `global.process.env`,
`globalThis.process.env`) triggers it. Minimal repro: plain esbuild v0.28.2
bundling `react-dom/client` plus a dynamic `import()` of a module that imports
`react`, with `Splitting`, ESM, and `Define {"process.env": "{}"}`; drop the
define and it stops. Hand-written CommonJS miniatures did not reproduce it. Kept
as `spikes/esbuild-define-race`
(`go run -race ./spikes/esbuild-define-race testdata/apps/vite-react`).

The underlying bug: esbuild shares an object/array-valued define's value through
a single symbol that every referencing file points at, so parallel renamer
goroutines collide on it. An injected module (`inject` + identifier define)
races the same way.

### esbuild define race mitigation (`globalDefines`)

Initially there was no og-side mitigation (changing Vite's define or forcing
identifier minification would change output; server builds were simply left
unsplit and Vite client builds minify by default). But leaving server builds
unsplit caused real bugs — SvelteKit's fallback error page
(`entries/fallbacks/error.svelte.js`) carried its own
`get_or_init_context_map` / `ssr_context` that the renderer (a separate copy)
never set, yielding 500 instead of 404. Splitting server builds like client
builds (`rolldown.js`) makes shared modules a single instance across entries,
but re-exposed the renamer race.

`globalDefines` (`esbuild.go`) is the fix: it turns object- and array-valued
defines into `globalThis.__og_define_<n>`, an entity name with no symbol behind
it, and each output chunk's banner sets it once with `x = x || value` (not
`??=`, because the banner is not lowered to the target). This yields one object
for the whole output, matching esbuild's shared-value intent, without the symbol
collision.

### Function names in build-time `require` (KeepNames / `__name`)

esbuild's transform renames a function expression named like its variable:
`var Root = module.exports = function Root() {}` becomes `function Root2` in
every format. This broke Stylus, whose compiler dispatches on
`'visit' + node.constructor.name`, so Stylus returned its AST uncompiled ("The
first argument must be one of type string…", from a `Root2` object).

Fix: `moduleSource` now transforms with esbuild `KeepNames`. The bootstrap
defines a global `__name` (esbuild's helper, non-enumerable); `KeepNames`
inserts `__name(inner, "inner")` inside function bodies. resolve-url-loader
rebuilds code from function text and evaluates it in the same realm, so the
global `__name` must exist there.

Not applied to the host bundle. Setting `KeepNames` there failed the gate in
three packages:

- `ContextualThenables` no longer recognized `then: function` properties once
  wrapped in `__name`;
- Nuxt and TanStack Start inline `fn.toString()` into pages, where `__name` does
  not exist;
- resolve-url-loader's evaluated templates failed.

(Recorded in TODO at the time.)

## Files

- `_tsoverlay/ogapi/module.go` ... `ogapi.ThenMethods`, marks `then` methods /
  properties for the `ContextualThenables` rewrite; re-installed into
  `third_party/typescript`.
- `runtime/engine/als.go` ... `__ogContextThen`, wraps the inner `then` to run
  in the async context current at the getter read.
- `esbuild.go` ... `globalDefines` (object/array-valued define → `globalThis.__og_define_<n>`
  + per-chunk banner); `moduleSource` KeepNames + `__name` global.
- `internal/jshost/js/webpack.js` ... (adjacent) build-time module machinery.
- `spikes/m0/features` ... the M0 engine-gap probe.
- `spikes/m0/shim` ... early Web API stubs (TextEncoder/Decoder,
  AbortController, MessageChannel).
- `spikes/esbuild-define-race` ... standalone `-race` reproducer for the
  object-valued define races.

## Test Coverage

- `TestThenableAsyncContext` (jshost): asserts Node 24's results — class,
  static, async, object-literal, and `then: function` thenables see the store
  through `await`, `resolve(thenable)`, and `Promise.resolve(thenable)`; checks
  own-property assignment, `typeof`, `name`, `length`; the two uncovered shapes
  assert `undefined`. Neutralized by making `ContextualThenables` return its
  input: every context becomes `undefined`.
- `TestContextualThenables`: the rewrite itself — calls, strings, accessors,
  computed names, super, and arrow values left untouched.
- `TestServeSvelteKit`: `/nope` → 404 with Kit's error page; neutralization
  (server builds unsplit) → 500.
- `TestGlobalDefines`: the `globalDefines` rewrite.
- Define race: an unminified vite-react built by an og compiled with `-race`
  races 3/3 with esbuild's own object define and with the injected module, and
  0/3 with `globalDefines`. Split server suites (SvelteKit, Astro node, React
  Router, TanStack Start, Vite SSR servers) under `-race`: no races.
- `TestKeepNames`: covers both the require KeepNames and the global `__name`,
  against Node 24's values; neutralizations yield `Root2` and a `ReferenceError`
  respectively.
- An Astro `experimental.incrementalBuild` probe page (temporary, restored)
  confirmed og now `inThen=object` like Node.

## Pitfalls

- `ContextualThenables` only covers statically-written `then` methods /
  properties. Runtime-attached `then` (`obj.then = fn`, prototype assignment,
  `defineProperty`), arrow-valued `then`, and `then` using `super` still lose
  async context. Nested `then`s not listed in `ThenMethods` are also uncovered.
- The accessor rewrite makes `x.then !== x.then` (new function per read); code
  that identity-compares `then` observes a difference from Node.
- `KeepNames` must never go on the host bundle — it breaks
  `ContextualThenables` recognition of `then: function`, breaks `fn.toString()`
  inlining (Nuxt, TanStack Start) where `__name` is absent, and breaks
  resolve-url-loader's evaluated templates.
- The object/array-valued-define races are esbuild's own bug (reproducible in
  plain esbuild v0.28.2); `globalDefines` sidesteps it rather than fixing it
  upstream.
- RegExp `\p{…}`, `/d`, and `/v` are engine gaps esbuild cannot help with — do
  not assume lowering covers them (see [[unicode-regexp-intl]]).
