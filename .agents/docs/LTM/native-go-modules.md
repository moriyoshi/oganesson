# Native Go Modules (`og:<name>`)

## Summary

Users can expose their own Go functions to the app's JavaScript as `og:<name>`
built-in modules. Functions live in a project-local Go module (`og/`) that `og
build` scans and compiles into the app binary, or in ejected code via
`native.Register`. The runtime side (`runtime/native`) converts values between
Go and JS and drives sync/async dispatch; the build side (`internal/nativegen`)
scans the `og/` module with `go/parser` (no type checker) to emit TypeScript
declarations and Go registration glue.

## Key Facts

- Project natives live under `og/` (its own Go module with `og/go.mod`). JS
  import specifier is `og:<name>` where `<name>` is the package name.
- JS function names are lowerCamel by default; override with `//og:name <name>`.
- A leading `context.Context` parameter makes a function async (returns a
  Promise); override with `//og:async` / `//og:sync`.
- A function may return at most one value and optionally an `error`. Variadics
  are rejected. Module names may not collide with og's own `og:*` names.
- Value conversion is JSON-style both directions (structs by `json` tags), with
  special cases: `int64`/`uint64` ↔ `bigint` at any depth (full 64-bit
  precision), top-level `[]byte` ↔ `Uint8Array`, and top-level `time.Time` ↔
  `Date` (nested, `[]byte`/`time.Time` keep their JSON forms: a base64 / RFC 3339
  string).
- `og build -o` with natives does NOT produce og-plus-payload; it generates a
  wrapper Go module in a temp dir and compiles it with the local Go toolchain
  (requires `-og-source`/`$OG_SOURCE`; `-runtime` is refused).
- `-eject` writes `natives_gen.go` and a `go.mod` that requires/replaces the
  user's `og/` module.

## Details

### User-facing conventions

Functions in a `og/<pkg>` package become `og:<pkg>`. The exported function's
name is lowerCameled for JS unless a `//og:name foo` directive overrides it. A
function whose first parameter is `context.Context` is async by default (runs on
a goroutine, returns a Promise); `//og:async` / `//og:sync` force the mode. A
function may return `(T)`, `(T, error)`, `(error)`, or nothing; more than one
non-error value or a variadic signature is rejected.

### `runtime/native` (runtime side)

- `Register(Module{Name, Funcs})` validates the module: name not reserved by
  og's own `og:*` namespace, no variadic functions, at most one value plus an
  optional error.
- `Plugin(names)` resolves only the listed `og:<name>` imports to
  `module.exports = globalThis[Symbol.for("og.native")](name)`, so the bundle
  needs no list of the individual functions.
- `Install(e)` (wired from `runtime/app`'s extensions) defines that loader per
  engine.
- Conversion (`convert.go`) preserves `int64`/`uint64` precision as a `bigint`
  at any depth, and is otherwise JSON-style. `toJS` walks the value reflectively
  (`nestedJS`): `int64`/`uint64` become a `bigint` via a `bigInt` helper that
  calls the JS `BigInt` function with the decimal string
  (`sobek.Runtime.ToValue(*big.Int)` truncates to a number, so it must go through
  JS `BigInt`); `[]byte`, `time.Time` and other `json.Marshaler` types keep their
  `encoding/json` forms (`jsonToJS`); structs/slices/maps recurse. Top-level
  `[]byte`/`time.Time` are special-cased to `Uint8Array`/`Date` before the walk.
  `toGo` unwraps a top-level bigint arg via `v.Export()` → `*big.Int` →
  `Int64()`/`Uint64()`; for a composite arg a `JSON.stringify` replacer
  (`markBigInts`) renders each nested bigint as a marker string that
  `bigintMarkerRe` then unquotes into a bare JSON number, so `json.Unmarshal`
  decodes it into the `int64`/`uint64` field with full precision (plain
  `JSON.stringify` throws on a bigint). `native.go` holds the
  `int64Type`/`uint64Type` reflect vars.
- Errors and panics throw (or reject) a plain `Error` with the message. Async
  functions run on a goroutine through `Loop.Async`; their `context.Context` is
  cancelled when the engine closes.

`structToJS` promotes a plain embedded struct's fields with precision
(`flattenFields`, matching json's promotion). It defers the whole struct to
`encoding/json` only for an embedding shape json treats specially (a tagged or
non-struct anonymous field, a `json.Marshaler`, or a field name shadowed across
embedding depths), or a map with non-string keys — all rare, and nested `int64`
precision is not kept inside those.

### `internal/nativegen` (build side)

- `Scan(project)` parses `og/go.mod` with `golang.org/x/mod/modfile` and reads
  each package under it with `go/parser` only — no type checker and no toolchain
  invocation. It records the module's `replace` directives on
  `Project.Replaces` (`OldPath`/`OldVersion`/`NewPath`/`NewVersion`).
- `TypeScript()` emits `declare module "og:<name>"` blocks: structs as
  interfaces by `json` tags, `omitempty` as optional, `*T` as `T | null`, doc
  comments as JSDoc (directive lines stripped). `writeTypes` appends these to
  `og-env.d.ts`.
- `tsType` maps `int64`/`uint64` to `bigint` at any depth, matching the runtime
  (which carries a nested one through JSON with a marker). The `top` flag still
  distinguishes `[]byte`/`time.Time` (top-level `Uint8Array`/`Date`, nested a
  base64 / RFC 3339 `string`).
- `Glue(pkg)` emits the gofmt'd `init` that registers the module's functions.
- The Next and Vite server bundles get `nativegen.Plugin(dir)`.

**Named-type `.d.ts` resolution.** A named non-struct type (`type Stamp
time.Time`) is mapped by recursing into its underlying type using the
*declaring* file's imports, not the referencing function's. `pkgInfo.imports`
records each named type's declaring-file imports; `tsType` resolves a named
type's underlying with `info.imports[name]`. Otherwise a selector underlying
(`time.Time`, `time.Duration`) degraded to `unknown` when used from a file that
did not import `time`. Named types are inlined at each use rather than aliased by
name, because a single `export type Stamp = …` cannot carry the
top-level-vs-nested distinction (top-level `[]byte`/`time.Time` are
`Uint8Array`/`Date`, nested they are a base64 / RFC 3339 `string`).

### `og build -o` (`cmd/og/native.go`)

An app binary with native modules is generated, not og-plus-payload. og creates
a module in a temp dir:

- `go.mod` requiring Oganesson and the user's module, replaced by
  `-og-source`/`$OG_SOURCE` (for og) and the project's `og/` (for the user
  module). It also emits the user's propagated `replace` lines (see below).
- `main.go` serving the appended payload (same as og's `servePayload`).
- `natives_gen.go` with the registration glue.

It seeds `go.sum` from the checkout, then runs `go mod tidy` and `go build
-trimpath -tags nodynamic` with CGO off, `GOWORK=off`, and `-target`'s
GOOS/GOARCH. `-og-source` is accepted with `-o`; `-runtime` is refused for native
projects. See [[install-and-packaging]] for the general build/package pipeline.

**Propagating `og/go.mod` replace directives.** Go applies `replace` only from
the main module, never from a dependency, so a `replace example.com/lib =>
../lib` in the project's `og/go.mod` was silently ignored and `go mod
tidy`/`build` failed to resolve it. `cmd/og/native.go`'s `userReplaces(proj)`
renders those directives into the generated `go.mod`: a filesystem replacement
(no version) is resolved against `og/` so the relative path still points at the
same tree from the temp build dir; a versioned replacement is kept verbatim.
Replaces that would shadow og, `github.com/microsoft/TypeScript/tsc`, or the
project module itself are dropped (og writes those lines). Both the
`nativeGoMod` template and the eject `go.mod` template (`cmd/og/eject.go`,
ranging `$.UserReplaces` inside `{{with .Natives}}`) emit the extra lines.

### Prerendering

`og build` prerenders static pages in-process, where the project's Go functions
do not exist — a page calling one failed its static render and fell back to
dynamic (`ƒ`), so its HTML was absent.

- **Host build**: when the built binary is for the current platform, static
  pages prerender *in it*. `app.PrerenderIn` writes files to a temp dir and runs
  the binary with `OG_PRERENDER_DIR` (`app.PrerenderMain`: routes on stdin, JSON
  result on stdout).
- **Cross build** (`cmd/og/build.go`): for a non-host `-target`, og also builds
  a throwaway *host* runtime (`buildNativeRuntime` for `runtime.GOOS/GOARCH`) and
  sets it as `prerenderExe`, so `PrerenderIn` runs the real Go functions. The Go
  toolchain and `-og-source` are already required on this path, so this adds no
  new prerequisite.
- Plain `og build` (no `-o`) and `-eject` still prerender in-process without the
  natives — keeping those paths toolchain-free is deliberate. The note "static
  pages are prerendered without the native Go modules …" now fires only for
  them, not for host or cross native builds.

### `-eject`

Writes `natives_gen.go` on every eject (and removes a generated one when the
project has no modules). A new `go.mod` requires and replaces the user's module;
a kept `go.mod` that lacks it gets a printed note.

## Files

- `runtime/native/native.go` — `Register`, `Plugin`, `Install`, validation,
  reflect type vars.
- `runtime/native/convert.go` — Go↔JS value conversion (JSON plus the
  `[]byte`/`time.Time`/`int64`/`uint64` special cases, `bigInt` helper).
- `internal/nativegen/nativegen.go` — `Scan`, `TypeScript`, `tsType`, `Glue`,
  `Plugin`; `go/parser` scan, modfile parsing, named-type import tracking.
- `cmd/og/native.go` — `og build -o` native-module path: generated module,
  `nativeGoMod` template, `userReplaces`, prerender wiring.
- `cmd/og/build.go` — cross-build host prerender runtime.
- `cmd/og/eject.go` — eject go.mod template with `$.UserReplaces`.
- `testdata/apps/next-native` — fixture: `og/hash` (`sha256` via `//og:name`,
  `raw` on bytes), `og/clock` (async `after`), a static page calling `sha256`,
  and route `app/api/digest`.
- `runtime/notices/THIRD_PARTY_NOTICES.txt` — regenerated when native SQL shims
  added pgx/mysql/puddle/pgpassfile/pgservicefile/edwards25519 to `cmd/og`'s
  dependency graph.

## Test Coverage

- `TestNativeModule` (runtime/native) — conversions, errors, panics, async
  calls, missing module.
- `TestNativeBigInt` (runtime/native) — passes 2^53 in and gets 2^53+1 back as a
  `bigint`; halves a uint64 above the int64 range. Neutralized by routing int64
  through the JSON number path (reports `number`/`...992`).
- `TestNativeNestedBigInt` — preserves full-width signed and unsigned values in
  struct fields, slices, maps, pointers, and promoted embedded structs in both
  directions; nested `[]byte` and `time.Time` retain their JSON forms.
- `TestScan` / `TestScanRejects` / `TestLowerCamel` (internal/nativegen) — golden
  `.d.ts` output including doc text before `//og:name`, a foreign `//nolint:`
  directive, `Scale(n int64) int64` → `scale(n: bigint): bigint`, a nested `Size
  int64` → `size: bigint`, and `type Stamp time.Time` used from a non-`time`-
  importing file (`touch(s: Date): Date`).
- `TestUserReplaces` (cmd/og) — pure unit test of replace rendering: relative
  path resolved to absolute, absolute kept, versioned verbatim, and the three
  shadowing drops. Neutralized by removing the `filepath.Join(proj.Dir, …)`
  resolution (reports `../lib`).
- `TestBuildNativeApp` (cmd/og) — real `og build -o` against the checkout; the
  prerendered `/` holds the Go-computed digest, the binary serves the sync, bytes
  and async calls, and `og-env.d.ts` has the declarations. Needs the Go toolchain
  and installed fixture. Neutralized by prerendering in-process ("/ was not
  prerendered with og:hash").
- `TestBuildNativeCrossPrerender` (cmd/og; skipped in `-short`/without toolchain
  or fixture) — builds `testdata/apps/next-native` for the non-host arch and
  asserts `/` appears in `prerender.json` with the og:hash digest. Neutralized by
  dropping the host-prerender branch (route becomes `ƒ dynamic`).
- `TestEjectNatives` (cmd/og) — gained a `replace example.com/lib => ../lib` in
  its fixture `og/go.mod` and asserts the ejected go.mod contains it resolved to
  an absolute path.

## Pitfalls

- `sobek.Runtime.ToValue(*big.Int)` truncates to a JS number; a bigint result
  must be constructed by calling the JS `BigInt` function with the decimal
  string.
- `ast.CommentGroup.List` keeps directive lines; a blank `//` before `//og:name`
  became a trailing empty JSDoc line.
- React inserts `<!-- -->` between adjacent text children, so in HTML assertions
  match the digest's hex alone, not `algorithm:hex`.
- Struct embedding shapes that defer wholesale to `encoding/json` (tagged or
  non-struct anonymous fields, `json.Marshaler`, shadowed promoted names) and
  maps with non-string keys do not preserve nested bigint precision. Plain
  structs, slices, maps with string keys, pointers, and ordinary embedding do.

## Remaining design-decision gaps

- `-og-source`/`$OG_SOURCE` is required to point at the Oganesson checkout for
  native builds — until Oganesson is published as a resolvable Go module, there
  is no toolchain-free way to build an app with native modules.
- Types declared in a package *other* than the module being scanned resolve to
  `unknown` in the generated `.d.ts`; only the declaring package's own files are
  parsed for named-type resolution.
