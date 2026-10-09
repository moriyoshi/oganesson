# SSR Server Programs

## Summary

A Vite `build --ssr` project that runs its own Node HTTP server (a "server
program") is compiled by Oganesson into a bundle that runs **inside each
request's VM**, behind the client files. The project's own `server.js` (an
Express/Fastify/Koa/Hono/h3 app) is executed per request; the built output is
mounted read-only at `/`, and the program serves every request itself, static
files included. This covers plain Node-server SSR apps; apps whose framework
builds the server for you (Astro, Nuxt, TanStack Start, SolidStart, …) are a
separate concern, see [[vite-framework-ecosystems]].

## Key Facts

- Chosen model (user decision): run the server program inside each request's
  fresh Sobek VM. Speed is not a goal; fidelity to `next start` / `node
  server.js` is.
- `vite build` never builds `server.js`; plain Node runs it. og follows the
  project's own scripts (`internal/frameworks/vite/scripts.go`).
- Payload `FormatVersion` is **5**. Build outputs go under `files/`
  (`meta.files = "files"`, plus `meta.clientDir`), mounted read-only at `/` via
  `runtime/vfs` (the runtime's cwd).
- The program serves **all** requests, static files included (og's own static
  layer is bypassed), because sirv with `extensions: []` does not map `/` to
  `index.html` — under Node, `/` is rendered by the framework.
- Program bundle is ESM with top-level await, wrapped in
  `globalThis.__ogProgram = (async () => {…})()`; a glue module sets
  `__ogHandler = (request) => http.serve(request, __ogProgram)` before the
  program's first await.
- WebSocket upgrades are **out of scope by decision**; `net.Server` /
  `tls.Server` remain stubs.
- Parity verified against real Vite + Node builds for six server shapes:
  Express, Fastify, Koa, Hono (node adapter), and h3 v2 (srvx).
- Runtime reports Node **v22.0.0** (so no zlib zstd); `process.versions.node` is
  derived from `process.version`.

## Details

### What Vite stores in globals (measured)

Measured with create-vite-extra 5.0.3's `template-ssr-react`, built and run with
real Vite 8.3.3 and Node 24, snapshotting `globalThis` after Node start, after
importing `dist/server/entry-server.js`, after importing `server.js` (Express
listening), and after three requests:

- No stage adds or changes a global. The only diffs were a `NaN !== NaN` artefact
  and Node's lazily created undici dispatcher symbol (Node's own fetch
  machinery). The built files contain no global writes.
- The SSR bundle keeps `react` / `react-dom` as imports of `node_modules`.
- State lives in **module scope**: cached `templateHtml`, the Express `app`, and
  the imported `render` held by the module cache. Recomputing that per request
  (fresh VM) is safe — the central justification for the per-request-VM model.

### Server-program build pipeline (`internal/frameworks/vite/scripts.go`)

- `buildInvocations` expands `build` through `npm run` / `pnpm` / `yarn run` /
  `bun run` chains (split on `&&` and `;`), strips `cross-env` / `A=B` / `npx`
  prefixes, keeps `vite build …` commands, skips others such as `tsc`.
- `cliConfig` maps `vite build` flags to what Vite's CLI passes: root, `--mode`,
  `-c`, `--base`, and build options (`--ssr [entry]`, `--outDir`, `--sourcemap`,
  …).
- `serverProgram` takes `node <file>` from `preview` / `start` / `serve`,
  resolved as node resolves it, with its environment (`NODE_ENV=production`).
- One JS host run executes every invocation in order: the user's config function
  is called with the right `ConfigEnv` (`isSsrBuild`); Vite's own `mergeConfig`
  merges the CLI inline config over it; `createBuilder(…, null)` is used, as the
  CLI passes.
- Dynamically imported devDependencies (template dev branches, `await
  import('vite')`) resolve to modules that throw if ever evaluated.
- `og build` summary reports "with a server program, N project file(s)".

### `node:http` adapter (`runtime/nodeshim/js/http.js`)

- `createServer` / `Server`: `listen()` opens nothing; it emits `listening` and
  announces the server.
- `serve(request, program)`: waits for `listen()`, rejects if the program ends
  without calling it ("og: the server program ended without calling listen()").
  It builds an `IncomingMessage` (unenv's) with a `Socket` (addresses,
  `destroy`, timeouts) and pipes the request body into it.
- `ServerResponse` (unenv's surface): `_write` / `_final` stream into a web
  `ReadableStream`; the head is fixed at `writeHead` or at the first write / end,
  as Node sends it.
- The module exports `IncomingMessage` / `ServerResponse` under the standard
  names because Express builds `req` / `res` from their prototypes.

### What Express needed underneath

- **`node:stream`:** unenv's streams are stubs, and `send` does
  `util.inherits(SendStream, require('stream'))`. Fix: vendor `readable-stream`
  4.7.0 `lib/` (Node's own stream implementation; `lib/stream.js` is Node's
  module) and npm's `string_decoder` 1.3.0 via `scripts/vendor-js.sh`
  (`vendor_cjs_tree`). nodeshim maps `stream`, `stream/promises`,
  `string_decoder` to them; their `process/`, `string_decoder/`, `safe-buffer`
  requires go to built-ins; `abort-controller` maps to the globals. The resolver
  normalizes extension-less CommonJS requires so each file loads once.
- **`process.stdout` / `stderr`** (`runtime/nodeshim/js/globals.js`): non-TTY
  Writables to the console (`debug` reads `process.stderr.fd`).
- **`fs.createReadStream`** (`runtime/nodeshim/js/fs.js`), for sirv.
- **zlib:** Go codecs live in `runtime/nodeshim/zlib.go` as `InstallZlib` (moved
  from `internal/jshost/zlib.go`), used by both the build host and request VMs.
  `createGzip` / `createDeflate` / … are Transforms that compress at end.

### Fastify / Koa / Hono / h3 parity

Fixtures `testdata/apps/vite-ssr-fastify`, `vite-ssr-koa`, `vite-ssr-hono`,
`vite-ssr-h3` share the Express fixture's React app, scripts and Vite config;
each `server.js` is the Express template ported:

- **Fastify:** `@fastify/compress`, `@fastify/static` (`wildcard: false`,
  `index: false`). Dev branch uses `@fastify/middie` and `vite` (devDeps).
- **Koa:** `koa-compress`, `koa-static` (`index: false`).
- **Hono:** `hono/compress`, `@hono/node-server`'s `serve` and `serveStatic`
  for `/assets/*` and the two public files.
- **h3 v2 (`vite-ssr-h3`):** h3 2.0.1 serves through srvx's node `serve`; matched
  Node path-for-path on first build. A sixth row of `TestServeViteSSRServers`,
  whose `compress` / `publicFiles` columns cover a server doing neither.

Pinned versions: fastify 5.12.5, @fastify/static 10.1.5, @fastify/compress
9.2.0, @fastify/middie 9.3.4, koa 3.2.1, koa-static 5.0.0, koa-compress 5.2.2,
hono 4.13.13, @hono/node-server 2.1.3, h3 2.0.1.

Gaps found and fixed for these frameworks:

- **`require("assert")` callable** (Fastify's router): a nodeshim override
  exports unenv's default, and `assert/strict` its `strict`. unenv's
  `AssertionError` dropped its message (`super()`); a load-time rewrite
  (`vendorRewrites`) passes it on and sets the name.
- **`process.versions.node`** (Hono's node adapter splits it): set from
  `process.version` in nodeshim globals.
- **`Object.prototype.__defineGetter__` / `__defineSetter__` /
  `__lookupGetter__` / `__lookupSetter__`** (Annex B, omitted by Goja; Koa's
  `delegates` uses it): defined non-enumerable in nodeshim globals.
- **`CompressionStream` / `DecompressionStream`** (`runtime/webapi`,
  `compression.go` + `js/src/compression.ts`), used by Hono's `compress()`:
  formats gzip, deflate, deflate-raw, brotli. Compression streams through Go's
  writers. Decompression streams through `streamDecoder` (a goroutine runs Go's
  decoder over a chunk queue; implements `io.ByteReader` so gzip/zlib/flate do
  not wrap it in `bufio` and swallow trailing input; input after stream end
  errors after decoded output is delivered; engine close stops abandoned
  decoders). WPT `compression`: 278 of 302 subtests pass (24 environmental
  failures: SharedArrayBuffer needs `WebAssembly.Memory`; Goja has no
  `Float16Array`; output-length needs a WPT-server media file; idlharness needs
  `WebIDLParser`).
- **Brotli in `node:zlib`:** `@fastify/compress` assumes Node always has brotli;
  removing the factories made it send `Content-Encoding: br` with an empty body
  (blank page in browsers). Fix: `github.com/andybalholm/brotli` (pure Go) backs
  `brotliCompress(Sync)` / `brotliDecompress(Sync)` and `createBrotli*`, quality
  from `params[BROTLI_PARAM_QUALITY]` (default 11). Fastify, Koa and Express
  (`compression`) answer `br` to browsers; Hono uses gzip via
  `CompressionStream`, as under Node. No zstd (runtime reports Node v22.0.0,
  before zlib's zstd). `compression`'s `'createBrotliCompress' in zlib` check
  works because the factories now exist.

### Server environments / provider environments

- Server environments keep bare imports of JS files in `node_modules` as imports
  (as Vite does). `rolldown.js` `isSsrExternal` governs this. Exceptions:
  `ssr.noExternal` (matched by package name with picomatch, dot) and `external`
  (forces externalization). Output now matches Vite's: `import { renderToString }
  from "react-dom/server"`. (A root-cause bug earlier bundled `react-dom` into
  `dist/server/entry-server.js` with esbuild's `__require` shim — "Dynamic
  require of util".) The first fix matched the full specifier; TanStack Start's
  `@tanstack/start**` then missed `@tanstack/start-server-core` — fixed to match
  by package name.
- **Provider environments:** `@tanstack/react-start`'s Vite plugin uses `ssr` as
  the provider, its RSC variant `rsc` (`providerEnvironmentName`), covered by
  `testdata/apps/tanstack-start-rsc`. Nothing else in the fixtures sets it.
  `preview` hooks remain for user plugins only — framework-built servers serve
  their own built server, which og serves directly (see
  [[vite-framework-ecosystems]]).

## Files

- `internal/frameworks/vite/scripts.go` — `buildInvocations`, `cliConfig`,
  `serverProgram`.
- `internal/frameworks/vite/program.go` — program bundle (`__ogProgram` wrapper,
  glue module, dev-dep throw modules).
- `internal/frameworks/vite/js/rolldown.js` — `isSsrExternal` (SSR externals).
- `runtime/app` — mounts `files/` read-only at `/`, installs Go zlib, lets the
  program serve every request.
- `runtime/vfs` — the read-only `/` mount; `runtime/vfs` `TestNodeFS`.
- `runtime/nodeshim/js/http.js` — `createServer` / `Server` / `serve` /
  `IncomingMessage` / `ServerResponse`.
- `runtime/nodeshim/js/globals.js` — `process.stdout` / `stderr`,
  `process.versions.node`, Annex B getters/setters.
- `runtime/nodeshim/js/fs.js` — `fs.createReadStream`.
- `runtime/nodeshim/zlib.go` — `InstallZlib`; brotli via andybalholm/brotli.
- `runtime/webapi/compression.go`, `runtime/webapi/js/src/compression.ts` —
  `CompressionStream` / `DecompressionStream`.
- `testdata/apps/vite-ssr-express`, `vite-ssr-fastify`, `vite-ssr-koa`,
  `vite-ssr-hono`, `vite-ssr-h3` — fixtures.

## Test Coverage

- `TestServeViteSSRServers` (replaced `TestServeViteSSRExpress`): one subtest per
  framework (Express, Fastify, Koa, Hono, h3), with `compress` / `publicFiles`
  columns. Checks gzip, and br-or-gzip as each framework chooses (response
  decoded and must equal the uncompressed body), plus each framework's static
  behaviour and content assertions.
- `TestBuildInvocations`, `TestCLIConfig`, `TestServerProgram`.
- nodeshim: `TestStreamIsNodes`, `TestHTTPServerServe`,
  `TestHTTPServeWithoutListen`, `TestZlibStreams` (with a brotli round trip),
  `TestNodeServerBasics` (assert, its message, `versions.node`, Annex B).
- `runtime/vfs` `TestNodeFS`.
- WPT `compression` (requires `scripts/sync-wpt.sh`).
- Against Node: reference builds of each fixture with real Vite, run under Node,
  compared on 8 paths each (`/`, `/favicon.svg`, `/index.html`, `/nope`,
  `/assets/`, `/?x=1`, `/missing.js`, the linked CSS). Status, content type and
  hash-normalized body match on every path for all frameworks.
- Browser: all frameworks hydrate (counter goes to 1), 1 stylesheet, 5 images,
  no console errors.

## Pitfalls

- A comparison once reported Koa/Hono as different because earlier `node
  server.js` processes were still listening — `kill $!` of a `( cd … && node … )
  &` subshell does not reach node, so the "Node" side was another framework's
  server. Scripts now `exec node`, refuse to start while a port is busy, and kill
  port listeners at the end.
- The comparison only proves og matches Node, not that the fixture is right: h3
  v2's `html()` escapes a plain string, so both sides served escaped HTML and
  `compare.sh` said "same"; only the test's content assertions caught it. Fix:
  `html(raw(...))`.
- Neutralizing the brotli stream factories alone passed, because Fastify
  compresses string bodies with the sync functions — so the sync path had to be
  neutralized instead.
- zsh does not word-split an unquoted `$FILES`; neutralization loops over `for f
  in $FILES` copied nothing and piled up mutations. Run neutralization scripts
  under bash with arrays and check backups before mutating.
- Under prezto's NO_CLOBBER, `>` into an existing file silently fails inside a
  loop; use `>|`.
- A compile error is not a valid neutralization (the first vfs and zlib
  neutralizations were unused-import compile errors and had to be redone).
