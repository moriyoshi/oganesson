# Compatibility and limitations

Oganesson aims to match framework production behavior while replacing Node.js
and native toolchains with pure Go. Compatibility is broad but not identical to
V8, Node, webpack, Rollup, or Rolldown. This page distinguishes implemented
substitutions from known boundaries.

## What "supported" means

A feature listed in the framework guides is implemented in the current source
tree. Most named frameworks and integrations have a focused fixture that builds
and, where applicable, serves real requests. Some package APIs are implemented
only to the extent exercised by those consumers; those cases are described as
validated combinations rather than complete drop-in replacements.

Skipped fixture tests are not evidence of support. Contributors install fixture
dependencies before running the corresponding suites and separately verify
browser behavior for hydration, navigation, actions, CSS, and service workers.

## Package installation

Implemented installation behavior includes npm registry tarballs, integrity
checking, dependency/peer/optional resolution, a local download cache,
`package-lock.json` / `npm-shrinkwrap.json` v2-v3, `pnpm-lock.yaml`, Yarn v1
and Berry locks, text `bun.lock`, and package.json/pnpm workspaces.

Known limits include:

- package lifecycle scripts never run;
- binary `bun.lockb` is unsupported (convert it to text `bun.lock`);
- package-lock workspace layouts containing `link: true` entries are not fully
  reproduced;
- recursive `**` workspace globs are not implemented;
- arbitrary git, patch, `link:`, `file:`, and non-npm Yarn resolution forms are
  not all implemented;
- only the public npm registry (`https://registry.npmjs.org`) is used; custom or
  private registries, `.npmrc` configuration, scoped auth tokens, and HTTP
  proxies are not configurable; and
- `npx` cannot download and execute a missing package during a build.

## Native package substitutions

Oganesson cannot load `.node` addons. It substitutes these packages on supported
paths:

| Requested package | Substitute |
|-------------------|------------|
| `bcrypt` | installed and bundled as `bcryptjs` |
| `sharp` | Go-backed `og:sharp` subset |
| `better-sqlite3` | pure-Go SQLite through `og:better-sqlite3` |
| `pg` | Go-backed PostgreSQL through `og:pg` |
| `postgres` | postgres.js-compatible surface over the PostgreSQL service |
| `mysql2`, `mysql2/promise` | Go-backed MySQL through `og:mysql2` |
| `@libsql/client` | local SQLite `file:` and `:memory:` mode |
| `lightningcss` | `lightningcss-wasm` at build time |
| `rollup` native binding | `@rollup/wasm-node` at build time |

The sharp subset accepts Buffer, typed-array, ArrayBuffer, and file-path input.
It implements `clone`, `rotate`, `autoOrient`, `flip`, `flop`, `extract`,
`resize` (common fit/position/background and enlargement/reduction options),
`grayscale`/`greyscale`, `metadata`, `toBuffer`, `toFile`, `toFormat`, and
JPEG/PNG/WebP/GIF format helpers. `withMetadata` and `keepMetadata` are accepted
but do not preserve input metadata. Streaming input, image creation, AVIF/HEIF,
composite, blur, sharpen, trim, extend, affine, stats, and the other methods that
the substitute reports as unsupported are not implemented. The `next/image`
optimizer is independent of the sharp substitute.

Unrecognized native addons fail to load with the Node-style native-binding
error. Prefer a pure-JavaScript or WebAssembly package when one exists.

## Major unsupported features

- No development server, file watching, HMR, or React Fast Refresh.
- No WebSocket upgrade server and no inbound `net.Server` or `tls.Server`.
- No child-process/native executable compatibility as a general facility.
- No AVIF output from the image optimizer.
- No durable built-in KV, ISR, Cache Components, optimized-image, or virtual
  filesystem store.
- No multi-process coordination for process-local services.
- No guarantee that arbitrary untested webpack, Vite, Rollup, or Rolldown
  plugins work.
- No arbitrary Node native addon loading.

## Runtime differences from Node.js

- Every request gets a new JavaScript VM; module globals do not persist.
- Sobek is not V8. Exact stack formatting, error locations, function/class
  names, and edge-case reflection can differ.
- `WeakRef` and `FinalizationRegistry` are compatibility polyfills.
- FormatJS-backed `Intl` services ship English locale data; test non-English
  formatting explicitly. `Intl.Collator` has broader CLDR-backed coverage.
- TLS uses the operating system trust store. Minimal images need CA files.
- The packaged filesystem is a read-only payload plus an in-memory writable
  overlay.
- Unsupported or lightly used Node built-ins can resolve through unenv but
  still contain stubbed methods.

## Build-system differences

Next.js:

- browser code is not split per route;
- arbitrary webpack plugins do not execute;
- only representable webpack aliases, fallbacks, definitions, and loader rules
  are applied; and
- native SWC-only transformations need a supported alternative.

Vite:

- Vite 8 uses esbuild chunk membership behind a Rolldown-compatible API;
- chunk grouping can differ even when names and runtime behavior match;
- `augmentChunkHash` and TypeScript syntax in plugin `parseAst` are missing;
- general plugin preview-server middleware is not run; and
- development-server-only plugin paths are unsupported.

## Data limits

- `og:kv` is memory-only.
- SQLite files inside the packaged virtual filesystem are memory-only; use an
  absolute host path for persistence.
- Native PostgreSQL and MySQL shims implement common connection/query options,
  not every package option, parser, TLS mode, or streaming API.
- `@libsql/client` remote HTTP transport is not implemented.
- TypeORM glob discovery is not generally traced.
- Prisma uses driver adapters; other generators and every database adapter are
  not implemented.
- Prisma startup migrate-deploy is implemented for SQLite and PostgreSQL;
  `db push` is currently validated with SQLite.

## Native Go module limits

- Building a final app executable requires the Go toolchain, and an Oganesson
  checkout only when `og` is an unpublished (locally built) binary.
- Plain directory builds and `og serve` cannot execute project-owned Go
  modules.
- Generated TypeScript types are syntactic; foreign package types generally
  become `unknown`.
- Nested 64-bit integers use JSON number conversion; top-level 64-bit integers
  use `bigint`.

See [Native Go modules](native-go-modules.md) for the exact build modes.

## Choosing Oganesson

Oganesson is a good fit when one-file deployment, absence of Node.js on the
build/deployment host, framework fidelity, and request isolation matter more
than peak throughput. Validate the exact application with its production
environment, database, browser paths, and packaged executable before release.

It is a poor fit today for applications centered on WebSockets, native addons
without WebAssembly or Go substitutes, Node child processes, in-memory
module-global sessions, or plugins that require full webpack/Rolldown internals.
