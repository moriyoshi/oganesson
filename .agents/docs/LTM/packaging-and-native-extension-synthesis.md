# Packaging and Native Extension Synthesis

## Summary

Oganesson installs JavaScript dependencies itself, builds framework output, and
appends the result to an `og` runtime to make a self-contained executable. The
same packaging system supports cross-target runtimes, ejected Go modules, built-
in native substitutes, and project-defined `og:<name>` modules compiled into
the application. Native extensions preserve the no-Node runtime while making
stateful or performance-sensitive Go services available to JavaScript.

## Included Documents

| Document | Focus |
|----------|-------|
| [install-and-packaging.md](./install-and-packaging.md) | Lockfiles, installer cache, app binaries, eject, and built-in substitutes. |
| [native-go-modules.md](./native-go-modules.md) | User-defined Go modules, code generation, conversion, and native builds. |

## Stable Knowledge

- `og install` supports npm shrinkwrap/package-lock, Bun text lockfiles, pnpm,
  and Yarn lockfiles. Layout locks reproduce the tree; pin locks constrain the
  hoisting resolver. Lockfile integrity is authoritative.
- Metadata and tarball fetches are concurrent and single-flight. The persistent
  cache stores ETag-aware metadata by URL and integrity-checked tarballs under
  `<UserCacheDir>/oganesson/npm` unless disabled.
- `pack.Write` appends a zip payload to an `og` runtime. The payload format is
  versioned, and the runtime refuses incompatible applications.
- Cross-platform packaging validates the selected runtime's program, target,
  cgo state, and source revision with `debug/buildinfo`. Runtimes come from
  `-runtime` or the per-source cache; Oganesson does not silently build a
  missing target runtime.
- `og build -eject` writes an independently buildable Go module around the
  payload and preserves user Go files on re-eject. `-readable` leaves browser
  output unminified.
- Release and packaged runtimes use `CGO_ENABLED=0 -trimpath -tags nodynamic`.
  The tag is necessary to prevent the WebP dependency from dynamically loading
  a system library.
- `runtime/natives.Registry` centralizes built-in package substitution across
  install, server bundle, and runtime. Examples include bcrypt to bcryptjs and
  Go-backed sharp, SQLite, PostgreSQL, and MySQL modules.
- Project natives live in their own `og/` Go module. An exported Go function
  becomes part of `og:<package>`; JavaScript names default to lower camel case
  and can be overridden with `//og:name`.
- A leading `context.Context` makes a native function asynchronous by default.
  Async calls use the engine loop and receive cancellation when their request
  engine closes.
- Native values use JSON-style conversion, with precise `int64` and `uint64`
  as JavaScript bigint at any depth. Top-level `[]byte` and `time.Time` become
  `Uint8Array` and `Date`; nested values retain JSON string forms.
- `internal/nativegen` uses `go/parser`, not type checking, to validate the
  supported signature surface and emit declarations and registration glue.
- An app with project natives is compiled as a generated wrapper module instead
  of copying an existing runtime. It needs the Go toolchain and an Oganesson
  source/module resolution path; `-runtime` is incompatible with this path.
- Host-target native builds prerender inside the generated app so static pages
  can call project Go functions. Cross builds create a throwaway host runtime
  for the same purpose.

## Operational Guidance

- Treat the lockfile as the source of package identity and integrity. Test any
  resolver change against all supported lockfile classes and without a lock.
- Keep all built-in substitute policy in `runtime/natives.Registry`; do not add
  separate installer and bundler maps that can drift.
- Keep project-native signatures within the documented reflection contract.
  Extend runtime conversion, declaration generation, validation, and tests as
  one change.
- Propagate relevant `og/go.mod` replace directives into generated modules,
  resolving filesystem replacements against `og/`. Never allow them to shadow
  Oganesson, TypeScript-Go, or the project module.
- For native prerendering, distinguish plain build/eject paths from built app
  binaries. The former intentionally remain toolchain-free and cannot execute
  project native functions during in-process prerender.

## Files

- `internal/install/lockfile.go`, `cache.go`, `install.go` - dependency layout,
  resolution, download, and cache.
- `pack`, `runtime/app`, `cmd/og/build.go`, `cmd/og/eject.go` - payload and
  executable packaging.
- `runtime/natives` - built-in native-substitute registry and services.
- `runtime/native` - user native registration, dispatch, and conversion.
- `internal/nativegen` - source scan, TypeScript declarations, and Go glue.
- `cmd/og/native.go` - generated native wrapper module and host/cross build.
- `scripts/build-og.sh` - canonical release-runtime build.

## Tests

- Installer and packaging tests include `TestLockfiles`,
  `TestLockfileBundled`, `TestMetadataPrefetch`, `TestInstallCache`,
  `TestCheckRuntime`, `TestFormatVersion`, and `TestEject`.
- Native tests include `TestNativeModule`, `TestNativeBigInt`,
  `TestNativeNestedBigInt`, `TestScan`, `TestScanRejects`,
  `TestUserReplaces`, `TestBuildNativeApp`, and
  `TestBuildNativeCrossPrerender`.
- Run all executable builds and image/native tests with `-tags nodynamic`.

## Pitfalls

- Per-version registry requests require `Accept: application/json`; the npm
  abbreviated-metadata media type receives 406 and silently destroys the
  intended installer speedup if fallback hides it.
- `go build -o app .` inside an ejected module collides with its `app/`
  directory. Use a different output filename.
- A project-native build currently needs `-og-source` or `OG_SOURCE` until the
  Oganesson Go module is published and resolvable.
- `sobek.Runtime.ToValue(*big.Int)` loses precision. Construct JavaScript
  bigint through the `BigInt` function with a decimal string.
- Complex embedded structs and maps with non-string keys can fall back to
  `encoding/json` and lose nested bigint precision.
- Types declared outside the scanned native package become `unknown` in the
  generated declaration file.
