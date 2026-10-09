# Foundations and Architecture

## Summary

Oganesson compiles a Next.js (later Vite) application into a single self-contained
Go executable that serves the app with fidelity to `next start`. The founding
constraints are pure Go with no Node.js at build time or runtime, a fresh
per-request Sobek VM, correctness over speed, and reuse of upstream code over
reimplementation. The M0 spike established that the full React 19 Server
Components + SSR pipeline can run inside a single embedded JavaScript engine, and
pinned the exact dependency versions the project builds on.

## Key Facts

- **Goal.** One `og build` turns a Next.js/Vite project into one self-contained
  Go binary; the binary serves the app without Node.
- **Pure Go / no Node.** Neither the build nor the produced binary may require
  Node.js, npm, or native addons. Build-time JS runs in the embedded engine
  (`internal/jshost`); runtime JS runs in the engine under `runtime/`.
- **Per-request isolated VMs.** Each request gets a fresh engine VM; no state is
  shared between request VMs except through deliberate Go-backed services
  (`runtime/kv`, `runtime/isr`).
- **Speed is not a criterion.** Prefer correctness and fidelity to `next start`
  over throughput.
- **Reuse prior work.** Adopt existing libraries and Next's own templates/config
  helpers before hand-writing shims; patch Next at bundle time rather than
  re-deriving its behavior.
- **RSC + SSR share one VM.** The two React layers are two IIFE bundles in the
  same VM, each with its own copy of React (built with and without the
  `react-server` condition), so only one engine is needed per request.

### Version pins (M0 baseline)

| Dependency | Pin |
|---|---|
| Goja (engine) | `0f92c903ca4a` (2026-09-30) |
| goja_nodejs | `b481721df8a2` |
| esbuild | v0.28.2 |
| TypeScript-Go | `edf7da4e93ab` |
| React / react-dom / react-server-dom-webpack | 19.3.0 |
| web-streams-polyfill | 4.3.0 |
| Go toolchain | 1.27 (`go 1.27.0` directive) |

(The embedded engine is referred to in current code as Sobek; the M0 spike used
Goja. goja_nodejs's `require` is vendored for the engine.)

## Details

### Feasibility results (M0)

The M0 spike proved every load-bearing capability in the embedded engine:

| Check | Result | Spike |
|---|---|---|
| React 19 `renderToString` in the engine | works | `spikes/m0/run` + `app/entry_string.tsx` |
| RSC: `react-server-dom-webpack/server.edge` → Flight → `client.edge` → `react-dom/server.edge` HTML, all in one VM | works | `spikes/m0/rsc` |
| `"use client"` boundary as a Go esbuild plugin (exports discovered via metafile probe) | works | `clientBoundary` in `spikes/m0/rsc/main.go` |
| AsyncLocalStorage across `await` and timers via `Runtime.SetAsyncContextTracker` | works | `spikes/m0/shim/als.go` |
| TypeScript-Go imported from our module via an overlay package | works | `_tsoverlay/ogapi` (was `gotsapi`), `scripts/sync-typescript.sh`, `spikes/m0/tscheck` |

### Design notes confirmed

- RSC and SSR are two IIFE bundles in the **same VM**. Each carries its own copy
  of React, so one engine serves both layers per request.
- The SSR layer resolves Flight client references through a global
  `__webpack_require__` that returns statically imported module namespaces.
  `__webpack_chunk_load__` resolves immediately.
- TypeScript-Go integration: the TS-Go module is
  `github.com/microsoft/TypeScript/tsc` and every package under it is
  `internal/`. Our facade is copied to `tsc/ogapi` and consumed through a
  `replace` directive. Build the vendored tree itself with `GOWORK=off`, because
  its `go.work` references `tools/`, which the sparse checkout omits.
- TS 7 rejects `baseUrl` (TS5102). Many Next projects still set it, so Oganesson
  must downgrade that diagnostic and still honor `baseUrl` for module
  resolution.

### Engine language gaps

The engine lacks several modern JS syntax features, built-ins, and regexp
capabilities; some are lowered by esbuild at build time and others need
polyfills or dedicated solutions (regexp property escapes, `Intl`, `WebAssembly`
via wazero, and so on). These are summarized here only in passing; see
[[js-language-gaps-and-lowering]] for the full inventory and the mitigation for
each.

## Files

- `ARCHITECTURE.md` — canonical system architecture (read before changing
  subsystem boundaries).
- `cmd/og/main.go` — small `flag`-based CLI dispatcher; new commands go in its
  table.
- `internal/jshost` — the build-time JavaScript host (engine).
- `runtime/` — runtime JavaScript execution per request.
- `runtime/kv`, `runtime/isr` — Go-backed services that are the only sanctioned
  cross-request state.
- `runtime/webapi` — host-provided Web APIs (TextEncoder/Decoder,
  AbortController, MessageChannel, etc.); these graduated from `spikes/m0/shim`
  in M1.
- `internal/frameworks/next/patches.go` — where Next's own code is patched at
  bundle time, with a comment explaining why.
- `_tsoverlay/ogapi`, `scripts/sync-typescript.sh` — TypeScript-Go facade and the
  sync script that installs it at the pinned commit.
- `spikes/m0/` — the original feasibility spikes (`run`, `rsc`, `shim`,
  `tscheck`, `features`); throwaway experiments, not part of the build gate.

## Pitfalls

- The vendored TypeScript-Go tree must be built with `GOWORK=off`; its `go.work`
  references `tools/`, absent from the sparse checkout.
- `baseUrl` must be honored for resolution even though TS 7 emits TS5102 for it;
  downgrade the diagnostic rather than dropping the setting.
- Do not introduce state shared between request VMs outside the sanctioned
  Go-backed services; the per-request isolation guarantee depends on it.
