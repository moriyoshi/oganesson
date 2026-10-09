# Runtime Platform Synthesis

## Summary

Oganesson runs build-time and request-time JavaScript in Sobek while preserving
the project invariant of a pure-Go, self-contained executable. Each request gets
a fresh VM and event loop; deliberate Go-backed services provide the only
cross-request state. Compatibility is supplied as a layered platform: targeted
bundle-time rewrites, Web and Node API shims, FormatJS, Go cryptography, and a
wazero-backed WebAssembly implementation.

## Included Documents

| Document | Focus |
|----------|-------|
| [foundations-and-architecture.md](./foundations-and-architecture.md) | Founding constraints and the original RSC/SSR feasibility work. |
| [runtime-engine.md](./runtime-engine.md) | Sobek VM lifecycle, event loop, shared services, and error handling. |
| [js-language-gaps-and-lowering.md](./js-language-gaps-and-lowering.md) | Engine gaps, bundle-time transforms, and esbuild correctness workarounds. |
| [unicode-regexp-intl.md](./unicode-regexp-intl.md) | FormatJS Intl and Unicode-aware RegExp behavior. |
| [wasm-on-wazero.md](./wasm-on-wazero.md) | WebAssembly, WASI, napi-rs/emnapi, and wasm consumers. |
| [node-compat-shims.md](./node-compat-shims.md) | Node built-ins from unenv, vendored Node code, and Go services. |
| [webcrypto.md](./webcrypto.md) | Go-backed asymmetric WebCrypto and key representation. |

## Stable Knowledge

- `og build` and produced applications must not require Node.js, npm, CGO, or
  native addons. Build-time JavaScript runs in `internal/jshost`; request-time
  JavaScript runs under `runtime/`.
- Every request receives a new Sobek VM and event loop. JavaScript module state
  is therefore request-local. Shared state must be explicit and Go-backed, such
  as `runtime/kv`, `runtime/isr`, the writable VFS overlay, or native services.
- RSC and SSR execute as separate bundles in one request VM, each carrying the
  React variant appropriate to its layer. Correctness and upstream fidelity
  take precedence over throughput.
- `runtime/engine` owns timers, ref/unref and weak handles, asynchronous Go
  operations, `Loop.Post`, AsyncLocalStorage tracking, draining, cancellation,
  and panic-to-`PanicError` recovery. `runtime/server` streams requests and
  responses and aborts work on client disconnect.
- Sobek replaced Goja for improved language fidelity, especially native Unicode
  property escapes. The vendored goja_nodejs `require` loader is rewritten to
  Sobek because no upstream Sobek port exists.
- Engine-bound bundles always pass through `gojatarget.Finish`. Important
  transforms include logical-assignment lowering, `HoldArrowArguments`,
  `QuoteKeywordClassFields`, and `ContextualThenables`. The latter preserves
  AsyncLocalStorage inside statically recognizable thenables through
  `__ogContextThen`.
- Object- and array-valued esbuild defines use `globalDefines` rather than
  esbuild's shared define symbol, avoiding the `NumberRenamer` race while
  keeping one value per output. `KeepNames` is limited to build-time `require`
  modules and must not be enabled on host bundles.
- `Intl` is assembled lazily from exact-pinned FormatJS packages, with
  `Collator` backed by `golang.org/x/text/collate`. Sobek handles `\p{...}` and
  `\P{...}` natively; the prelude translates `v`-flag set notation to `u`, with
  generated Unicode 17 emoji-sequence tables for properties of strings.
- `internal/wasm` exposes browser-style `WebAssembly` over wazero, including
  shared memory, tables, externref, WASI, napi-rs/emnapi, and wasi-threads.
  Runtime VMs receive a restricted, lazy installation without WASI or native
  imports; build hosts receive the extended surface needed by toolchains.
- Node compatibility starts with unenv but replaces fidelity-critical pieces
  with vendored Node 24 source or vetted libraries. Notable replacements are
  `node:path`, fs streams, WHATWG stream adapters, `path.matchesGlob`,
  `_http_common`, zlib, and fallback TLS roots.
- WebCrypto operations execute in Go while TypeScript normalizes WebCrypto
  shapes. Supported asymmetric families are Ed25519, ECDSA, RSA signatures,
  RSA-OAEP, ECDH, and X25519. ECDSA signatures cross the API as IEEE P1363, not
  ASN.1.

## Operational Guidance

- First decide whether a compatibility issue belongs at build time, in the
  common runtime prelude, in a Node module shim, or in a Go host service. Keep
  per-framework patches out of the general runtime unless the behavior is truly
  platform-wide.
- Prefer upstream JavaScript and exact-pinned libraries over local
  reimplementations. Vendor through the repository scripts and patch at load
  time; never hand-edit generated vendor trees.
- When adding a transform, apply it to every engine-bound bundle and keep its
  trigger narrow. Broad TypeScript-Go parsing on every dependency can dominate
  the test suite.
- Treat async context, event-loop liveness, streaming, and engine close as one
  system. Test cancellation and cleanup, not only the successful return value.
- After editing `runtime/webapi/js/src`, run `go generate ./runtime/webapi/`.
  After changing TypeScript-Go overlays, run `scripts/sync-typescript.sh`.
- Compare compatibility behavior with Node 24 and neutralize fixes. Browser-only
  behavior, worker behavior, and wasm close paths need tests beyond a simple
  server response.

## Files

- `runtime/engine`, `runtime/server`, `runtime/app` - VM lifecycle, event loop,
  HTTP bridge, and per-request installation.
- `runtime/webapi/js/src`, `runtime/webapi/prelude.js` - Web APIs, polyfills,
  Intl, Unicode sets, and generated runtime prelude.
- `internal/gojatarget`, `_tsoverlay/ogapi` - engine-target lowering and
  TypeScript-Go analysis.
- `runtime/nodeshim`, `runtime/codec`, `runtime/tlsroots` - Node built-ins,
  streaming codecs, and trust roots.
- `internal/wasm`, `internal/jshost/js/napi-rs-wasi.js` - WebAssembly and native
  addon bridges.
- `runtime/kv`, `runtime/isr` - explicit cross-request services.
- `runtime/webapi/crypto.go`, `runtime/webapi/js/src/crypto.ts` - WebCrypto.

## Tests

- `go test -tags nodynamic ./runtime/... ./internal/jshost/... ./internal/wasm/...`
  covers the engine, Web APIs, Node shims, host, and wasm base layers.
- High-value focused tests include `TestThenableAsyncContext`,
  `TestGlobalDefines`, `TestKeepNames`, `TestRegExpUnicodeSets`,
  `TestRegExpPropertiesOfStrings`, `TestWebAssembly`, `TestExternref`,
  `TestFsStreams`, `TestPathIsNodes`, `TestWebStreamAdapters`, and `TestCrypto`.
- WPT coverage is available through `go test ./runtime/webapi -run TestWPT`
  after `scripts/sync-wpt.sh`; skipped fixtures are not passing fixtures.

## Pitfalls

- Module-level JavaScript state never persists between requests. A library that
  assumes a process-wide singleton needs an explicit Go-backed service.
- `ContextualThenables` does not cover run-time-assigned, arrow-valued, or
  `super`-using `then` implementations, and its accessor changes `x.then`
  identity.
- `KeepNames` on a host bundle breaks thenable recognition, source-string
  consumers, and evaluated loader templates.
- `memory.buffer` detaches after wasm growth. Re-read views after JS-to-wasm
  boundaries. wasi-thread shutdown depends on wazero v1.12 internals and may
  fall back to a timeout if they change.
- unenv stream classes can shadow prototype accessors with class fields. Audit
  own properties whenever adopting another stream-derived class.
- Node server-side `net.Server` and `tls.Server`, cross-realm workers,
  transferables, `XMLHttpRequest`, and several advanced JS built-ins remain
  incomplete or deliberately out of scope.
