# WebAssembly on wazero

## Summary

Oganesson provides a browser-grade `WebAssembly` JS API backed by [wazero](https://wazero.io),
so native Rust/C addons that publish WebAssembly builds run unmodified in Sobek
at both build time and runtime, with no Node and no CGO. The host
(`internal/wasm`) links guest imports to JS, bridges napi-rs/emnapi Node-API
addons, supports `wasi-threads` (and stops them cleanly on close), and is
installed in a restricted form into every per-request runtime engine. Consumers
include `@tailwindcss/oxide`, `lightningcss-wasm`, satori/resvg (`next/og`),
Shiki's Oniguruma and satteri (Astro Markdown), and wasm-bindgen (Qwik's
optimizer).

## Key Facts

- Host package: `internal/wasm`. JS shim `wasm.js`; import glue generated in `binary.go`.
- JS API: `Module`, `Instance`, `Memory` (incl. `shared`), `Table`, the error
  classes, `instantiate`, `compile`, `validate`.
- Natives exposed via `Symbol.for("og.wasm")` (renamed from `Symbol.for("gots.wasm")`):
  `wasi()` (wazero preview1, host `/` mounted read-only), `native("emnapi")`,
  `native("wasi-threads")`.
- Compilation cache on disk: `$UserCacheDir/oganesson/wazero` (was
  `$UserCacheDir/gots/wazero` before the rename). lightningcss compiles in ~10 s
  cold, ~0.2 s warm.
- JIT (wazevo) is active on arm64: a 1.5 MB oxide scan took 347 ms compiled vs
  2111 ms interpreted.
- Runtime engines get a **restricted** install: `Options.Restricted` withholds
  WASI and the native imports, and `Symbol.for("og.wasm")` is undefined; the
  wazero runtime is created lazily on first use (`Runtime.wz`).
- napi-wasm vendored at 1.1.3; napi-rs addons load via `og:napi-rs-wasi`
  (`js/napi-rs-wasi.js`). emnapi environment ids start at **1**, not 0.
- `x.wasm?module` -> `new WebAssembly.Module(bytes)` via an `og-binary:` import
  (esbuild binary loader); `internal/frameworks/next/wasmmodule.go`, `wasmPlugin`.

## Details

### `internal/wasm` host mechanics

- **API.** `Module`, `Instance`, `Memory` (including `shared`), exported
  `Table`s, the error classes, and `instantiate` / `compile` / `validate`. Go
  primitives do the work under the small JS shim `wasm.js`.
- **Imports.** For each import namespace a generated glue module (`binary.go`)
  re-exports JS functions as Go host functions that call into Sobek, together
  with memories from their provider modules. wazero's
  `experimental.WithImportResolver` links the guest to the glue.
- **`Table.get` / table access.** wazero has no table API. Two mechanisms cover
  this:
  - The module binary is rewritten to export every function that table 0's
    active element segments reference, as `__og_fn_<idx>` (was `__gots_fn_<idx>`).
    The prefix lives in a shared `slotExportPrefix` constant used with
    `strings.HasPrefix` (`isSlotExport`); a hard-coded 10-byte comparison once
    leaked slot exports into `Module.exports` and was caught by `TestWebAssembly`.
  - `withTableAccessors` adds exported accessor functions per exported table
    (size; grow with `ref.null`; and for externref tables get/set).
    `WebAssembly.Table` then gains `set` and `grow`. An externref table's new
    slots hold the `init` argument (undefined when omitted, per the JS API).
- **Memory views.** `memory.buffer` is a Sobek ArrayBuffer aliasing wazero's
  memory. It is detached whenever the memory grew, checked at every JS<->wasm
  boundary; napi-wasm re-reads views when it finds `byteLength` 0.
- **externref.** externref values map to handles of JS values (null is the null
  reference). wasm-bindgen grows and fills an externref table at start and
  passes externrefs across.
- **Exceptions.** A JS exception thrown by an import is stashed, unwinds the
  wasm stack as a host panic, and is rethrown unchanged to the JS caller.
- **Natives** (`Symbol.for("og.wasm")`):
  - `wasi()` is wazero's preview1, host `/` mounted read-only;
  - `native("emnapi")` provides emnapi's runtime helpers in Go;
  - `native("wasi-threads")` provides `thread-spawn`: a new instance of the
    module on a goroutine, sharing the imports and memory, running
    `wasi_thread_start`. JS imports called from a thread trap.
- **Compilation cache.** Compiled code cached in `$UserCacheDir/oganesson/wazero`
  and shared through the package, so each request's `new WebAssembly.Module` of
  resvg is a cache hit.

### Stopping wasi-threads on Close

The race detector flagged `wasm.Runtime.Close` (`wazero.Runtime.Close`) running
while oxide's rayon worker goroutines were still inside wasm (WASI calls).
wazero must not close modules under running threads. Close now:

1. cancels the runtime context (`WithCloseOnContextDone`);
2. waits for the thread goroutines through a `sync.WaitGroup`;
3. only then closes the runtime.

Idle rayon workers park in `memory.atomic.wait32` and never observe the
cancellation, so every close used to burn the full 2 s timeout and abandon the
runtime. `wakeWaiters` fixes this: wazero keeps waiters in an unexported
`sync.Map` (address -> waiters) on its `MemoryInstance`, and its exported
`Notify(offset, count)` is reachable through an interface assertion. Close reads
the map's keys by reflection and notifies every address every 5 ms until the
threads are gone; woken threads loop back into wasm, hit wazero's context check,
and exit. This depends on **wazero v1.12 internals**; if they change, Close
falls back to the 2 s timeout, which `wasm.CloseTimeouts()` counts.

### napi-rs / emnapi bridging (`internal/jshost/native.go`, `js/napi-rs-wasi.js`)

- A napi-rs `<name>.wasi.cjs` with a sibling `<name>.wasm32-wasi.wasm` loads
  through `og:napi-rs-wasi` using vendored napi-wasm 1.1.3. It replicates the
  napi-rs loader sequence: `_initialize`, every `__napi_register__*` export,
  then `napi_register_wasm_v1` only.
- napi-wasm's fatal UTF-8 decoder is made lenient at load time to match Node's
  `napi_create_string_utf8`. Oxide extracts candidates from arbitrary file
  bytes, which can contain invalid UTF-8.
- **Environment ids start at 1.** napi-wasm numbered environments from 0, but
  emnapi's compiled-in C rejects a NULL env (napi-rs creates a threadsafe
  function for its custom GC at registration). A load-time patch of napi-wasm
  starts ids at 1.
- **emnapi runtime hooks.** emnapi's runtime hooks the module imports. Those
  meaningful without threads are implemented (`napi_set_last_error` returns its
  status; GC-access check, env refs, request counters, async-hook reports are
  no-ops); the rest trap with a named error when called (`emnapiTrapped`). The
  `emnapi` namespace's `emnapi_is_node_binding_available` returns 0.
- `module.register()` is a no-op (Tailwind registers an ESM cache-busting loader
  hook; Oganesson never loads modules through Node's ESM loader).
- A failed-load deferral remains as a safety net with a reworded message.
- `nativeSubstitute` also redirects `lightningcss/node/index.js` to require
  `lightningcss-wasm` instead of `lightningcss-<platform>`.

### Installer wasm fetch and substitution

- The installer fetches optional `*wasm32*` dependencies that declare
  `cpu: wasm32`, and adds the `wasmSubstitutes` map (e.g. `lightningcss` ->
  `lightningcss-wasm`, same version). It skips either kind quietly when it
  cannot fetch them.
- `@tailwindcss/oxide-wasm32-wasi` is a `wasm32-wasip1-threads` build that
  imports a shared memory, emnapi helpers and `wasi.thread-spawn`, while
  `lightningcss-wasm` is a non-threaded napi-wasm JS Node-API build.

### Restricted runtime WebAssembly and `?module` imports

`runtime/app` installs `internal/wasm` into every request engine, restricted and
lazy (see Key Facts). `?module` and binary imports are handled in
`internal/frameworks/next/wasmmodule.go` via `wasmPlugin`: `x.wasm?module`
becomes `new WebAssembly.Module(bytes)`, with the bytes from an `og-binary:`
import (esbuild's binary loader). This stands in for webpack's edge
`asset/webassembly` handling. `next/og` (`@vercel/og`) uses it: see
[[assets-images-fonts-metadata]] for `ImageResponse`, satori and the resvg/
fallback-font patches.

### Markdown wasm: satteri, Shiki, Oniguruma (Astro)

- **satteri** loads via napi-wasm/emnapi (the env-id-from-1 and emnapi-hook work
  above was what made it load).
- **Shiki** decodes its inlined Oniguruma WebAssembly with
  `Uint8Array.from(atob(...), c => c.charCodeAt(0))`. Goja and Sobek reject a
  primitive source; the prelude boxes it as the spec's `GetMethod` does
  (`polyfills.ts`, `TypedArray.from`).
- Compared with Node (`astro build`), the highlighted Markdown page is
  byte-identical.

### Build-time wasm in the Vite/Rollup path

- For an installed Vite below 8, bundling uses **real Rollup** rather than
  Rolldown: the installer adds `@rollup/wasm-node`, substituted for Rollup's
  native binding (plus the esbuild npm package, served by Oganesson's esbuild
  shim). See [[vite-and-rolldown-facade]] for the facade and Vite-major gating
  (`sharedVite` in `vite.go`).
- **Qwik's optimizer** is wasm-bindgen based and exercises externref and table
  growth (see the externref / `withTableAccessors` notes above). `require` of a
  `.node` addon throws Node's `ERR_DLOPEN_FAILED`, which Qwik catches and falls
  back to its WebAssembly build.

For the CSS consumers (`@tailwindcss/oxide` scanning and `lightningcss-wasm`
minification), see [[css-pipeline]]; the oxide ignore-rule detour is summarized
below.

### Oxide ignore-rule detour (why scans were slow)

- An early probe scanning `node_modules` took 131 s. The cause is upstream
  behavior (`crates/oxide/src/scanner/sources.rs`): if any `.gitignore` between
  the scan base and the repository root ignores the base, oxide promotes it to
  an **external** source and scans everything (`!/**/*`). The scratch project
  lived in gitignored `.agents-workspace/tmp/`.
- Rule: scratch Tailwind projects must not live in gitignored directories, or
  must set `source(...)`.
- `@napi-rs/wasm-runtime/dist/fs.js` (6 MB) takes oxide ~130 s under wazero
  while comparable files take under 0.5 s (open item in TODO).

## Files

- `internal/wasm/*.go`, `wasm.js` — the wazero-backed host and JS shim;
  `binary.go` generates import glue; `withTableAccessors`, `wakeWaiters`,
  `slotExportPrefix`/`isSlotExport`, `CloseTimeouts()`.
- `internal/jshost/native.go`, `internal/jshost/js/napi-rs-wasi.js` — napi-rs
  loader and napi-wasm/emnapi bridge (`nativeSubstitute`).
- `internal/frameworks/next/wasmmodule.go` (`wasmPlugin`) — `?module` / binary
  imports for the runtime.
- `runtime/app` — installs the restricted, lazy `internal/wasm` into each
  request engine.
- `runtime/webapi/js/src/polyfills.ts` — boxes a primitive source for
  `TypedArray.from` (Shiki/Oniguruma).

## Test Coverage

- `internal/wasm`: `TestWebAssembly`, `TestWebAssemblyErrors` (hand-assembled
  module), `TestRestricted` (no wazero runtime before use, no `og.wasm` symbol,
  standard API still works), `TestExternref` (wasm-bindgen table growth/fill).
- `internal/install`: `TestWasmBuilds` (neutralization verified).
- `internal/jshost`: `TestOxideIgnoreRules` (project inside a repo whose `.git`
  is in an ancestor; asserts no engine close timed out — neutralized by skipping
  the wake, which reports "1 engine close(s) timed out"), `TestSatteriMarkdown`
  (Node's HTML), `TestTypedArrayFromPrimitive`.
- `internal/frameworks/next`: `TestImageResponse` (resvg/satori via runtime
  WebAssembly; neutralizing the runtime install fails it).
- Astro build: `TestBuildAstroStatic` (highlighted Markdown page).

## Pitfalls

- The Close-wake machinery depends on **wazero v1.12 unexported internals**
  (`MemoryInstance`'s waiter `sync.Map`, `Notify`). If they change, closes fall
  back to the 2 s timeout, counted by `wasm.CloseTimeouts()`.
- JS imports invoked from a spawned wasi-thread trap; only the main instance may
  call into JS.
- `memory.buffer` detaches on growth — always re-read views across a JS<->wasm
  boundary (napi-wasm does this on `byteLength == 0`).
- Slot/table accessor exports (`__og_fn_<idx>`) must be filtered out of
  `Module.exports`; use the shared `slotExportPrefix`, not a magic length.
- Scratch Tailwind/oxide projects in gitignored directories trigger a
  full-everything scan; use `source(...)` or a non-ignored path.
- Probes must use absolute paths: a relative probe path resolved against the
  test working directory once scanned a nonexistent dir and returned `[]`,
  making "fixed" and "unfixed" runs look identical.
- `@napi-rs/wasm-runtime/dist/fs.js` (6 MB) scans in ~130 s under wazero (open).
