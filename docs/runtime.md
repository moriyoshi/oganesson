# Runtime compatibility

The deployed Oganesson server is a Go `net/http` application containing the
compiled JavaScript application and a Sobek JavaScript engine. It targets
framework compatibility rather than Node.js throughput.

## Request isolation

The server bundle is compiled once when the process starts. Every request then
gets a new Sobek VM and event loop. This has deliberate consequences:

- global mutations, module-level variables, timers, and unfinished promises do
  not leak into later requests;
- an application's startup path can run in each request VM, although startup
  console output is suppressed after the first successful initialization;
- Node `AsyncLocalStorage` and framework request contexts are propagated across
  supported promises, timers, streams, and thenables; and
- shared state must use a Go-backed process service or an external system.

The built-in shared services are the ISR/Cache Components cache, optimized
image cache, database connection services, and [`og:kv`](data.md#ogkv).

## Web APIs

Server bundles have the Web Platform APIs expected by modern edge-style
framework code, including:

- `Request`, `Response`, `Headers`, `FormData`, `Blob`, `File`, and URL-encoded
  form data;
- `fetch`, including streamed request and response bodies;
- `URL` and `URLSearchParams`;
- `ReadableStream`, `WritableStream`, `TransformStream`, queuing strategies,
  readers, writers, and piping;
- `TextEncoder`, `TextDecoder`, base64 helpers, and structured clone behavior
  used by the runtime;
- timers, microtasks, abort controllers and signals;
- `crypto.getRandomValues`, `randomUUID`, and Web Crypto with SHA-1/256/384/512,
  HMAC, AES-GCM/CBC/CTR, PBKDF2, HKDF, Ed25519, ECDSA and ECDH on
  P-256/P-384/P-521, RSASSA-PKCS1-v1_5, RSA-PSS, RSA-OAEP, and X25519;
- `CompressionStream` and `DecompressionStream`; and
- `WebAssembly` at runtime in the restricted form described below.

The repository runs a pinned Web Platform Tests subset for these APIs. It is not
a claim that every browser API exists: DOM, window, layout, media, and browser
storage APIs are client-side concerns and are not present in the server VM.

## Node.js compatibility

Imports with or without the `node:` prefix resolve against a broad unenv-based
Node built-in surface. Important real or Go-backed implementations include:

- `buffer` and Node-style globals;
- `fs` and `fs/promises`, including file descriptors, stats, directory access,
  streams, and common synchronous and asynchronous calls;
- `path`, `path/posix`, `path/win32`, and `path.matchesGlob`;
- `stream`, `stream/promises`, string decoders, and Web Stream adapters;
- `crypto` and Web Crypto interoperation;
- `zlib` streams and callbacks for gzip, deflate, raw deflate, and Brotli;
- `async_hooks`, especially `AsyncLocalStorage`;
- `events`, `util`, `assert`, `module`, `process`, and `vm`;
- `http` server primitives used by packaged server programs;
- outbound `net.Socket` and `tls.TLSSocket`; and
- build-time `worker_threads` for tools such as terser.

Other built-ins come from unenv and can be complete enough for package feature
detection while still containing stubbed operations. Treat the list above as
the implemented operational core, not as a promise of complete Node conformance.

Inbound `net.Server`, `tls.Server`, cluster/process spawning, and WebSocket
upgrade servers are not implemented. Oganesson owns the single HTTP listener.

## Filesystem

Packaged files are mounted at `/`, which is also the server VM's working
directory. Reads see the build outputs and files traced into the payload. Writes
go to a process-wide in-memory overlay visible to later request VMs.

This makes common temporary and SQLite workflows work, but it is not durable
storage:

- changes do not alter the executable;
- changes vanish when the process exits; and
- multiple Oganesson processes do not share the overlay.

Use absolute host paths for persistent SQLite files and other durable data.
Build-time tools get a project-local temporary directory so their writes remain
inside the project workspace.

## Networking and TLS

Server-side `fetch` uses Go's HTTP client. `node:net` and `node:tls` client
sockets use Go connections and support the database clients described in
[Data](data.md). TLS certificate verification uses the host system's trust
store, not Node's bundled Mozilla certificate list. Minimal container images
therefore need CA certificates when the application makes HTTPS or TLS
connections.

The database socket layer can retain protocol sessions across request VMs. A
request still receives a fresh JavaScript connection object; the shared Go
service handles pooling and reset behavior.

## WebAssembly

At build time, Oganesson supplies a WebAssembly JS API on wazero, including
WASI, worker-based WASI threads, externref tables, and napi-wasm/emnapi bridges.
This enables validated WebAssembly packages such as:

- Tailwind oxide;
- `lightningcss-wasm`;
- Rollup's WebAssembly build;
- the Qwik optimizer;
- Astro's Markdown stack and Oniguruma; and
- Prisma's schema engine.

At runtime, ordinary `WebAssembly` use is available with a deliberately
restricted global environment. Server-side `.wasm` ESM imports are supported;
client-component imports remain browser assets.

## JavaScript language behavior

Oganesson lowers syntax that Sobek cannot parse directly, including ESM and
async-generator paths used by dependencies. It also applies targeted transforms
for known engine incompatibilities in destructuring, keyword-named class
fields, nested-arrow `arguments`, and async-context propagation through
thenables.

Implemented compatibility includes RegExp Unicode property escapes, indices
(`d` flag), and Unicode set notation/properties of strings (`v` flag), including
emoji sequence tables. `WeakRef` and `FinalizationRegistry` have compatibility
polyfills rather than native garbage-collector semantics.

Function/class names and stack traces can differ from V8 because esbuild and
Sobek are not V8. Do not use exact generated function names or V8 stack formatting
as an application protocol.

## Internationalization

The runtime installs these `Intl` APIs lazily:

- `getCanonicalLocales`, `Locale`, and `Segmenter`;
- `PluralRules`, `NumberFormat`, and `DateTimeFormat`;
- `ListFormat`, `RelativeTimeFormat`, and `DisplayNames`; and
- `Collator`.

The FormatJS-backed groups currently ship English locale data (with specific
`en-GB` data where needed); `DateTimeFormat` includes IANA time-zone data.
`Collator` uses Go's CLDR collation data and supports locales more broadly.
Locale-aware methods on `Number`, `BigInt`, and `Date` use the same providers.

Applications requiring non-English formatting should test their exact locale
and API. Missing locale data can fall back rather than match a full Node ICU
build.

## Process lifetime and operations

- `SIGINT` and `SIGTERM` trigger graceful shutdown.
- Uncaught exception and unhandled rejection listeners are supported.
- The process-wide caches, virtual writes, and `og:kv` are cleared on restart.
- The default address is `PORT` or `:3000`.
- The runtime sets the application's private origin after binding the listener,
  allowing Server Action redirects to fetch their own RSC response.
- Oganesson prioritizes fidelity and isolation, not requests per second; a fresh
  VM adds measurable work to every request.
