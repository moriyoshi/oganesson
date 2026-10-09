# Runtime Engine and Shared Services

## Summary

Every request Oganesson serves runs in a fresh, isolated JavaScript VM with its
own event loop (`runtime/engine`). This per-request isolation means module-level
state never survives a request; the only state deliberately shared across request
VMs lives in Go-backed services (`runtime/kv`, `runtime/isr`). The engine is
Sobek (`github.com/grafana/sobek`, a Goja fork), switched from Goja for its
superior standards fidelity.

## Key Facts

- Engine package: `runtime/engine` — one Sobek VM plus its own event loop per request.
- Loop features: timers (with ref/unref), ref-counted Go async ops (`Loop.Async`), thread-safe `Loop.Post`, AsyncLocalStorage via `SetAsyncContextTracker`, `Await`/`Drain`, interrupt on context end, and panic → `PanicError` recovery so one bad request cannot crash the server.
- Server: `runtime/server` is an `http.Handler` with a fresh engine per request; streams request body in, response body out (flushing every chunk), aborts the request signal on client disconnect, and drains background work for a grace period after the response.
- JS engine is **Sobek** (`github.com/grafana/sobek`, regexp2 v2.8.1), aliased in code as `sobek.`; the API is identical to Goja's, so the switch was an import rewrite with no other code change.
- Only cross-request shared state: `runtime/kv` (built-in `og:kv` key-value store) and `runtime/isr` (ISR cache; see [[static-generation-and-isr]]).
- Server **programs** print startup output once (process-wide flag in `runtime/app`); the gate runs **without** `-race`.
- `go generate ./runtime/webapi/` regenerates `prelude.js` from the TS sources in `runtime/webapi/js/src`.
- `runtime/webapi`'s Go host functions back encoding with `golang.org/x/text`, URL with `nlnwa/whatwg-url`, crypto with the Go stdlib, and the fetch transport with `net/http`.
- Deliberately out of scope (not bugs or gaps): `setTimeout` with a string argument, `XMLHttpRequest`, cross-realm Workers / transferables, and the HTTP cache modes of `Request.cache`.

## Details

### Per-request engine and event loop

`runtime/engine` wraps a single Sobek VM with its own event loop. The loop
provides timers (with ref/unref), ref-counted Go async operations (`Loop.Async`),
a thread-safe `Loop.Post`, AsyncLocalStorage support through
`SetAsyncContextTracker`, `Await`/`Drain` to run the loop to quiescence,
interrupt-on-context-end, and panic-to-error recovery. The loop converts any Go
panic into a `PanicError`, so a single malformed request cannot bring down the
server.

`runtime/server` is the `http.Handler` that gives each request a fresh engine. It
streams the request body in and the response body out, flushing every chunk,
aborts the request's abort signal when the client disconnects, and keeps draining
background work for a grace period after the response is sent.

Historical engine bugs fixed early (M1, found via WPT): `Engine.Run` used to hand
its initial script's error to the lenient uncaught handler, which swallowed it —
it now returns the error.

### The engine is Sobek (migration from Goja)

Oganesson originally ran on Goja. It was switched to Sobek (Grafana's Goja fork,
k6's engine) because Sobek has materially better standards fidelity. The decisive
win was **Unicode property escapes**: Sobek implements `\p{…}` / `\P{…}` natively
(through regexp2) — general categories (long and short names), `Script` /
`Script_Extensions` (`sc`, `scx`), binary properties, astral characters,
properties negated and inside classes, `/iu`, and a `SyntaxError` for unknown
names. Goja accepts the syntax and silently never matches, which is why the old
prelude rewrote these patterns. Sobek also adds Iterator helpers
(`Iterator.prototype.map`, …).

What the switch entailed:

- `go.mod`: `github.com/grafana/sobek` (pulling regexp2 v2.8.1). Goja and
  goja_nodejs remain only for historical spikes.
- Every Goja import became Sobek, referenced as `sobek.` — the API shape is
  identical, including `AsyncContextTracker`.
- **goja_nodejs's `require`** (Oganesson's build-time CommonJS loader) is vendored
  into `third_party/goja_nodejs/require` by `scripts/vendor-goja-nodejs.sh`, with
  its Goja imports rewritten to Sobek — there is no upstream Sobek port.
- **RegExp**: the prelude (`runtime/webapi/js/src/regexp.ts`) no longer rewrites
  property escapes. It still runs the `v` flag as `u` where equivalent and
  emulates `d`, and restates regexp2's syntax errors in V8's form
  (`Invalid regular expression: /src/flags: reason`). esbuild no longer lowers
  property-escape literals, and `gojatarget.LowerRegExpLiterals` is gone. Sobek
  matches Node 24 (Unicode 17) on Unicode 16/17 additions, `Script=Unknown`, and
  lone surrogates — cases the old rewriter got wrong.
- Kept because the bugs persist in Sobek: `logical-assignment` lowering (Sobek
  still loses `x ??= (await P()).v` in test shapes), `QuoteKeywordClassFields`,
  and `HoldArrowArguments` (arrow-function `arguments` bug). `internal/gojatarget`
  keeps its name.
- Notices regenerated to credit Sobek and regexp2 (`go generate ./runtime/notices`).

Gaps shared by both engines (still present on Sobek): the `v` (unicodeSets) and
`d` (indices) flags as native features; async generators; stack overflow not
catchable as `RangeError`; `Uint8Array.from(string, fn)` rejecting a primitive
source; no `SharedArrayBuffer` / `Atomics` / `WeakRef` / `FinalizationRegistry` /
`Intl`; no `Promise.withResolvers`, `Object.groupBy`, `Array.fromAsync`,
`String.prototype.isWellFormed`, `using`; lone surrogates in `JSON.parse` become
U+FFFD.

The Sobek switch was validated alongside Astro's Markdown pipeline (satteri /
Shiki / napi-wasm on wazero); that detail lives in [[wasm-on-wazero]] and
[[vite-framework-ecosystems]]. One prelude fix from that work is engine-relevant:
Shiki decodes inlined Oniguruma wasm with `Uint8Array.from(atob(…), c => …)`, and
both engines reject a primitive source, so the prelude boxes it as the spec's
GetMethod does (`polyfills.ts`).

### og:kv — the built-in key-value store

Because every request gets a fresh, isolated engine, module-level state never
persists. `og:kv` is the explicit, opt-in way to share state across requests:
one store per Oganesson process, held in Go outside every engine.

```ts
import { openKv, KvU64 } from "og:kv";

const kv = await openKv();
await kv.set(["session", sid], { user: "ann" }, { expireIn: 3_600_000 }); // ms
const { value, versionstamp } = await kv.get<{ user: string }>(["session", sid]);
for await (const e of kv.list({ prefix: ["session"] })) { /* … */ }
await kv.atomic().check({ key: ["lock"], versionstamp: null }).set(["lock"], "mine").commit();
await kv.atomic().sum(["visits"], BigInt(1)).commit(); // atomic counter
```

Semantics follow Deno KV:

- **Keys** are tuples of `Uint8Array | string | number | bigint | boolean`,
  ordered by type (in that order) then value. `prefix` matches whole key parts.
- **Values** are structured-clone-style (objects, arrays, `Map`, `Set`, `Date`,
  `RegExp`, `BigInt`, typed arrays, `ArrayBuffer`, errors, `undefined`, `NaN`);
  cycles preserved. Every read returns a fresh copy, so no live object is ever
  shared between requests.
- **`KvU64`** supports atomic `sum`, `min`, `max`; sums wrap modulo 2^64.
- **Commits** apply all mutations or none, after checking each
  `{key, versionstamp}` (a `null` versionstamp means "must not exist").
  Versionstamps are 20 hex digits, increasing every commit.
- **`expireIn`** is milliseconds; expired entries vanish from reads and lists.
- **`list`** supports `prefix` / `start` / `end`, `limit`, `reverse`, and
  `cursor` (from `iterator.cursor`) for pagination.
- **Not implemented**: queues (`enqueue` / `listenQueue`) and `watch`.

Scope: in memory only (lost on restart); local to one process (not shared across
load-balanced instances); server-only (importing `og:kv` from client code fails
the build). `og build` and `og check` write the module's declarations into
`og-env.d.ts`, as Next writes `next-env.d.ts`.

### runtime/isr — the other shared service

`runtime/isr` is the second deliberate Go-backed service shared across request
VMs (the ISR / Incremental Static Regeneration cache handler). It is summarized
here only as a peer of `runtime/kv`; the cache-handler design and behavior live
in [[static-generation-and-isr]].

### Server programs print startup output once

A server program (e.g. an Express or srvx app) runs in every request's engine, so
its startup banner (printed from the listen callback) would otherwise appear once
per request; Node prints it once. `runtime/app` (`startupOutputOnce`, program mode
only) uses a process-wide flag: the first engine prints everything, and later
engines wrap `host.write` (the sink for the console and `process.stdout`) to drop
output until `Symbol.for("og.startupDone")` is called.
`runtime/nodeshim/js/http.js` `serve` calls startupDone after the program has
listened and right before `server.emit("request")`, so everything printed before
dispatch (top-level code, listen callbacks) counts as startup and the request's
own output shows. Trade-off: later engines' startup output is dropped, errors
included (the first run printed the same program's).

### process.uncaughtException / unhandledRejection listeners

In `runtime/engine/loop.go` (`claim`), before an uncaught exception (from a job
or timer) or an unhandled rejection (engines with `UnhandledRejections`) reaches
`Options.Uncaught`, it is offered to
`globalThis[Symbol.for("og.uncaught")](error, promise)`, which reports whether it
handled the error; an error the handler itself throws replaces the original.
Engines without the Node shims behave as before.

The Node shims (`runtime/nodeshim/js/globals.js`) implement that handler as Node
does: an exception emits to `"uncaughtException"` (origin `"uncaughtException"`);
a rejection emits to `"unhandledRejection"` (reason, promise), else falls back to
`"uncaughtException"` with origin `"unhandledRejection"` (Node 15+); with no
listeners it is not handled. `reportError` (used by microtasks such as
`process.nextTick` and `queueMicrotask`, and by EventTarget listeners) offers the
error to the same handler first. diagnostics_channel's
`triggerUncaughtException` now just throws.

### The gate runs without -race

`-race` was dropped from the standard gate: the fixture suites took 10-25 minutes
under it and it caught nothing. The whole gate now runs in about 3 minutes
(`vite` ~150 s, `next` ~66 s), with `-timeout 30m`. `QUALITY_GATE.md` keeps
`-race` documented as a deliberate tool for concurrent Go code (the engine loop,
workers, KV, ISR). Note: a shared Go build cache can be left inconsistent by disk
cleanup; builds/tests can use `GOCACHE=.agents-workspace/tmp/gocache-probe` to
work around it.

## Files

- `runtime/engine` — one Sobek VM plus its event loop per request (timers, async ops, `Loop.Post`, AsyncLocalStorage, panic recovery).
- `runtime/engine/loop.go` (`claim`) — offers uncaught exceptions / unhandled rejections to `Symbol.for("og.uncaught")` before `Options.Uncaught`.
- `runtime/server` — `http.Handler` with a fresh engine per request; streaming request/response, client-disconnect abort, post-response drain.
- `runtime/app` — installs host bindings into each request engine; `startupOutputOnce` program-mode startup-banner-once logic.
- `runtime/webapi` — Web APIs; TS sources in `js/src` bundled by `go generate` into `prelude.js` (esbuild Go API, no Node). Go host functions back encoding (`golang.org/x/text`), URL (`nlnwa/whatwg-url`), crypto (stdlib), and the fetch transport (`net/http`).
- `runtime/webapi/js/src/regexp.ts` — RegExp prelude; no longer rewrites property escapes on Sobek, emulates `v`/`d`, restates errors in V8 form.
- `runtime/kv/store.go` — the Go KV store (map + mutex); `keys.go` order-preserving binary key encoding.
- `runtime/kv/js/kv.js` — the `og:kv` module (Deno KV classes + value serializer).
- `runtime/kv/kv.go` — esbuild plugin and host bindings, installed per request by `runtime/app`.
- `runtime/isr` — ISR cache handler, the second Go-backed cross-request service (see [[static-generation-and-isr]]).
- `runtime/nodeshim/js/http.js` — `serve` calls startupDone before `server.emit("request")`.
- `runtime/nodeshim/js/globals.js` — Node-style uncaughtException / unhandledRejection emission; `reportError`.
- `internal/gojatarget` — esbuild settings for the engine (kept name); `HoldArrowArguments`, `QuoteKeywordClassFields`, logical-assignment lowering retained for Sobek bugs.
- `third_party/goja_nodejs/require` — vendored CommonJS `require` loader, Goja imports rewritten to Sobek (`scripts/vendor-goja-nodejs.sh`).

## Test Coverage

- `go test ./runtime/webapi -run TestWPT` — Web Platform Tests (`*.any.js` fetched by pinned `scripts/sync-wpt.sh`) against `testdata/wpt-expectations.json`; `-wpt.update` re-baselines, `-wpt.filter` narrows.
- `TestStartupOutputOnce` (runtime/app), `TestHTTPServeSignalsStartupDone`, `TestServerProgramStartupOnce` (Express fixture) — startup-output-once.
- `TestProcessUncaught` (Node 24's output), `TestDiagnosticsChannel` — uncaught-error hooks.
- `TestRegExpPropertyEscapes` (Unicode 16/17, `Script=Unknown`, surrogates, V8-form errors), `TestRegExpLiteralLowering` (literals stay literals) — Sobek RegExp fidelity.
- `TestTypedArrayFromPrimitive` — the `Uint8Array.from` primitive-boxing prelude fix.
- All runtime/jshost/wasm/gojatarget/pack/install/licenses/next/vite suites pass on Sobek.
- Standard gate runs without `-race`, `-timeout 30m`, ~3 minutes.

## Pitfalls

- Do not introduce state shared across request VMs except through the deliberate Go-backed services (`runtime/kv`, `runtime/isr`); any module-level JS state is per-request and dies with the request.
- Startup-output-once drops later engines' startup output entirely, errors included — only the first engine's startup prints.
- `Value.Export()` of a Symbol is not a `*sobek.Symbol`; assert on the Value itself.
- Both engines reject a primitive source to `Uint8Array.from(string, fn)`; the prelude boxes it (needed for Shiki's Oniguruma decode).
- Logical-assignment lowering, `QuoteKeywordClassFields`, and `HoldArrowArguments` must stay — the underlying bugs are still present in Sobek.
- A shared Go build cache can go inconsistent after disk cleanup (`no required module provides package …`); use a scratch `GOCACHE` to work around it.
