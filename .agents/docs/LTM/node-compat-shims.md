# Node.js Compatibility Shims

## Summary

Oganesson runs Node built-in modules under Sobek without Node present. The base layer is unenv's stubs, but several stubs are not-implemented or lossy, so og replaces them with Node's own vendored source (v24.15.0, pulled by `scripts/vendor-js.sh`) or with vetted pure-JS libraries. This document records which modules are Node's own, which remain unenv stubs, the accessor-shadowing audit, and `path.matchesGlob` over vendored minimatch.

## Key Facts

- Node's own source vendored into `runtime/nodeshim/vendor/node` (and `.../vendor/minimatch`), embedded and served to the bundler as `og-node-lib:<path>`; `internal/*` requires resolve through `nodeInternals` / `nodeshim` in `runtime/nodeshim/nodeshim.go`.
- Replaced with Node's own: `fs.ReadStream` / `WriteStream` (`lib/internal/fs/streams.js`), `node:path` + `path/posix` + `path/win32` (`lib/path.js`), the web stream adapters `Readable/Writable/Duplex.toWeb/fromWeb` (`lib/internal/webstreams/adapters.js`), `path.matchesGlob` (`internal/fs/glob` `matchGlobPattern` over bundled minimatch 10.2.4).
- Replaced with a pure-JS library: `node:_http_common`'s `HTTPParser` = http-parser-js 0.5.10 (`runtime/nodeshim/js/http_common.js`).
- Streaming zlib lives in Go at `runtime/codec` (shared `Compressor` / `Decoder`); `nodeshim.InstallZlib(e)` wires Node's `zlib.js` streams to it.
- `runtime/tlsroots` embeds Mozilla's fallback root set for minimal systems and
  exposes the same certificates through `node:tls.rootCertificates`.
- Still unenv stubs (not constructible / not replaced): `http.OutgoingMessage`, `http.ClientRequest`, `child_process.ChildProcess`, `net.Server`, `tls.Server`.
- Accessor-shadowing audit: only `IncomingMessage` had class fields shadowing prototype getters; fixed. All other stream-based unenv classes are clean.

## Details

### unenv audit: accessor-shadowing class fields

unenv declared some stream properties as class fields, which shadow the getters readable-stream defines on the prototype chain. `IncomingMessage` declared `readable = false`, `closed`, `errored` and `complete = true` as fields: every request read as not-readable, so Node's web-stream adapter returned an empty, cancelled stream and `request.clone()` failed with "Body has already been consumed". og's subclass (`runtime/nodeshim/js/http.js`) deletes the fields; `complete` starts `false` and turns `true` when the whole body has arrived, as in Node.

A follow-up audit instantiated every stream-based class og takes from unenv and checked its own properties against accessors on its prototype chain. None besides `IncomingMessage` shadow getters: `http.ServerResponse`, `net.Socket`, `tls.TLSSocket`, `tty.ReadStream` / `WriteStream`, `crypto.Hash`, `process.stdout` / `stdin`, `readline.Interface` are all clean. The non-constructible unenv stubs (`http.OutgoingMessage`, `http.ClientRequest`, `child_process.ChildProcess`) could not be audited this way and remain stubs.

### fs.ReadStream / WriteStream are Node's

unenv's `fs.createWriteStream` was a not-implemented stub (over a host fs, which at runtime is the packaged app's read-only `/`); `fs.createReadStream` was a plain Readable that read the whole file (no `fs.ReadStream`, no chunking, no `fd`). Both are now Node's own `ReadStream` / `WriteStream` from `lib/internal/fs/streams.js` over og's fd operations. `fs.ReadStream`, `WriteStream` and the legacy `FileReadStream` / `FileWriteStream` are lazy accessors on the module, as in Node.

Supporting shims grew: `validateInteger`, `validateInt32`, `validateThisInternalField`, `deprecate` (node_internals.js); `ERR_SYSTEM_ERROR`, `internal/fs/promises` (`kRef`, `kUnref`, `FileHandle`), `internal/fs/utils` (`copyObject`, `getOptions`, `getValidatedFd`, `validatePath`), `internal/url` (`toPathIfFileURL`), `internal/streams/destroy` (readable-stream's) (node_stream_internals.js); primordials for namespace statics (`MathMin`). `fs.writev` / `writevSync` were added (WriteStream prefers writev, and unenv's threw); `openSync` of a missing file now reports Node's error (`syscall: "open"`, errno -2), not the internal stat's.

### node:path is Node's lib/path.js

unenv's path is pathe, which turns every backslash into a separator and aliases `win32` to posix. This broke Rollup's `createFilter` (used by SolidStart's "use server" compiler via Vite): createFilter escapes regex characters in the project path, e.g. `solid\-start`, then `posix.join`s the pattern; pathe rewrote it to `solid/-start`, so no file matched, the "use server" transform never ran, and **server function bodies shipped in the client bundle and ran in the browser**. Any project whose path contains `-` (or `.`, `+`, …) hits this with every relative createFilter pattern. og now uses Node's `lib/path.js`; `path/posix` and `path/win32` are its `posix` / `win32`.

### Node's web stream adapters

readable-stream leaves `Readable.toWeb` / `fromWeb`, `Writable.toWeb` / `fromWeb`, `Duplex.toWeb` / `fromWeb` out (`webStreamsAdapters = {}`). A load-time rewrite points them at Node's `lib/internal/webstreams/adapters.js`. srvx (Nitro 3, h3 v2) reads every request body through `Readable.toWeb(req)`.

Both path and the adapters needed Node-internal shims: `js/node_internals.js` (primordials as a Proxy that uncurries `XPrototypeY`, reads `XPrototypeGetY` accessors, binds statics, maps `SafeX` to `X`; character constants; `validateString` / `Boolean` / `Function` / `Object` / `OneOf` with Node's messages; `getLazy`, `kEmptyObject`, `normalizeEncoding`; `isWindows: false`) and `js/node_stream_internals.js` (readable-stream's errors plus `ERR_INVALID_STATE`, the WHATWG stream globals, `util.types`, `eos`, `internalBinding` stubs for StreamBase adapters og has no use for).

### path.matchesGlob over vendored minimatch

The stub threw "not implemented". Node's `path.matchesGlob` is `internal/fs/glob` `matchGlobPattern` over Node's bundled minimatch (`deps/minimatch/index.js`, minimatch 10.2.4, BlueOak-1.0.0, one self-contained CommonJS file). `scripts/vendor-js.sh` vendors it from Node v24.15.0 into `runtime/nodeshim/vendor/minimatch` (with LICENSE.md and VERSION; `collect-notices.sh` picks it up); `nodeshim.go` embeds it as `internal/deps/minimatch/index`; `js/node_internals.js` `matchGlobPattern` is Node's function with Node's options. `nocase` follows `process.platform` as Node's does (on by default on darwin / win32): og reports linux, so it is off.

### node:_http_common = http-parser-js

msw's interceptors import `node:_http_common`'s `HTTPParser`. `js/http_common.js` exports Node's names (`HTTPParser`, methods, parsers, `freeParser`, `CRLF`, the expressions, the header checks, `prepareError`) over http-parser-js 0.5.10, a pure-JS implementation of the http_parser binding's API, vendored through `scripts/vendor-js.sh` (notices regenerated from its LICENSE.md).

Pitfall: http-parser-js passes `kOnHeadersComplete` one info object (its Node 0.12 mode) until something reads `HTTPParser.kOnExecute`. The shim reads it once; its value stays 99 (a slot key), where Node's is 5.

### Streaming zlib (runtime/codec)

The shared streaming codecs live in Go at `runtime/codec`: `Compressor` (`Write` / `Flush` / `Close`) and `Decoder`, a goroutine-fed streaming decoder (`Feed` / `Close` / `Stop`, `io.ByteReader`, errors on data after the end, `unzip` by magic number), moved out of webapi. `CompressionStream` uses it. `nodeshim.InstallZlib(e)` takes the engine (abandoned decoders stop when it closes) and adds `stream(kind, level)`; `zlib.js`'s `createGzip` … `createBrotliDecompress` emit output per write, `flush()` sync-flushes (as `res.flush()` in compression / koa-compress needs), and the end finishes the stream.

### Embedded TLS roots

`runtime/tlsroots` blank-imports
`golang.org/x/crypto/x509roots/fallback`, which installs Mozilla's trust store
through `x509.SetFallbackRoots`. Go's TLS clients therefore retain public-root
verification on minimal Linux images without a system bundle. The package also
exports the bundle as PEM; `runtime/nodeshim/net.go` installs it on the
`og.net` host object and `js/tls.js` exposes it as Node's
`tls.rootCertificates`, replacing unenv's empty array. Native pg/mysql2 TLS
verification uses the same trust basis; see [[databases-drivers-and-orms]].

## Files

- `runtime/nodeshim/nodeshim.go` — embeds vendored Node source; resolves `internal/*` requires via `nodeInternals`; serves files to the bundler as `og-node-lib:<path>` / `internal/deps/minimatch/index`; `InstallZlib`.
- `runtime/nodeshim/vendor/node` — vendored Node v24.15.0 lib source (`lib/path.js`, `lib/internal/fs/streams.js`, `lib/internal/webstreams/adapters.js`, …).
- `runtime/nodeshim/vendor/minimatch` — vendored minimatch 10.2.4.
- `runtime/nodeshim/js/http.js` — `IncomingMessage` subclass deleting shadowing fields.
- `runtime/nodeshim/js/http_common.js` — `node:_http_common` over http-parser-js 0.5.10.
- `runtime/nodeshim/js/node_internals.js` — primordials, validators, `matchGlobPattern`.
- `runtime/nodeshim/js/node_stream_internals.js` — readable-stream errors, WHATWG globals, `internalBinding` stubs.
- `runtime/nodeshim/js/fs.js` — `fs.globSync` / `glob` / `promises.glob`.
- `runtime/nodeshim/js/zlib.js` — Node zlib stream classes.
- `runtime/codec` — shared Go streaming `Compressor` / `Decoder`.
- `runtime/tlsroots` — fallback root installation and PEM exposure.
- `scripts/vendor-js.sh` — pins and vendors all of the above; `collect-notices.sh` regenerates NOTICE.

## Test Coverage

- `TestFsStreams` (jshost) — Node 24's results: pending, instanceof, open > ready > finish > close, bytesWritten, append flag, highWaterMark chunks, bytesRead, start / end with encoding, ENOENT from open, async iteration.
- `TestPathIsNodes` — posix, win32 and error messages against Node 24.
- `TestPathMatchesGlob` — Node 24's results (globstar, dot files, braces, classes, no negation or comments, win32 separators, the argument error) plus a case-sensitivity case from Node's source.
- `TestWebStreamAdapters` — against Node 24.
- `TestIncomingMessageReadable` — against Node 24 over a real server.
- `TestHTTPCommon` — a request's head, body and end, and a response head, as Node 24 parses them. Neutralized: without the `kOnExecute` read, the headers are undefined.
- `TestZlibStreamsIncremental` — a sync-flushed gzip prefix decodes before the end through a streaming gunzip; the full stream round-trips; unzip handles gzip and zlib. Neutralized: with `Flush` emitting nothing, fails ("false|…").
- `TestFsGlob` — against Node 24 on the same tree.
- `TestNetAndTLS` — covers the net/tls surface (see Pitfalls for what remains stubbed).
- `TestPEMs`, `TestPoolVerifiesEmbeddedRoot`, and
  `TestTLSRootCertificates` cover the embedded bundle, Go verification, and
  Node-facing root list.

These fixture/jshost tests skip when the fixture's `node_modules` is absent.

## Pitfalls

- `http.OutgoingMessage`, `http.ClientRequest`, `child_process.ChildProcess`, `net.Server` and `tls.Server` are still unenv stubs — not replaced with Node's own. net/tls server-side support is partial; see [[databases-drivers-and-orms]] for how DB drivers lean on the net/tls surface.
- unenv class fields silently shadow readable-stream prototype getters; adding a stream-based module requires the accessor audit, not just a smoke test.
- The `path` regression was invisible to curl and SSR checks — the "use server" bodies only leaked in the browser (SSR runs the function server-side either way). Browser verification caught it.
- http-parser-js's `kOnExecute` must be read once at load, or it stays in Node-0.12 single-info-object mode.
- Vendored trees under `runtime/nodeshim/vendor/` are produced by `scripts/vendor-js.sh`; do not hand-edit — change the script or patch at load time in `nodeshim.go`.

Overrides are served to the bundler through og's esbuild namespaces; see [[build-system-esbuild-layers]] for how `og-node-lib:` and friends fit the layer scheme.
