# Install and Packaging

## Summary

Covers `og install` (dependency resolution that honors npm/pnpm/yarn/bun lockfiles, with concurrent metadata/tarball fetching and a persistent cache) and `og build` packaging (cross-platform app binaries, the `-eject` standalone Go module, the `-readable` option, and the native-module substitutes). Release `og` and every packaged app are built with `-tags nodynamic`.

## Key Facts

- Lockfile lookup order: `npm-shrinkwrap.json`, `package-lock.json`, `bun.lock`, `pnpm-lock.yaml`, `yarn.lock`.
- `og install` prefetches metadata (16 concurrent, single-flight), uses per-version manifests, downloads tarballs while resolving, and caches to `<UserCacheDir>/oganesson/npm` by default (`-cache dir|off`).
- `og build -o app -target goos/goarch [-runtime file]` packages for other platforms; runtimes are validated with `debug/buildinfo` and resolved before building.
- `og build -eject <dir> [-og-source <checkout>]` writes a standalone Go module; `-readable` leaves the browser bundle unminified (works with or without `-eject`).
- Native substitutes: bcrypt → bcryptjs, sharp → `og:sharp` (Go subset), better-sqlite3 → `og:better-sqlite3` (modernc.org/sqlite). Registry lives in `runtime/natives`.
- Release and app binaries: `CGO_ENABLED=0 -trimpath -tags nodynamic` via `scripts/build-og.sh [out] [goos/goarch]`.
- Key tests: `TestLockfiles`, `TestCheckRuntime`, `TestEject`, `TestNativePackages`.

## Details

### `og install` lockfiles (`internal/install/lockfile.go`)

`og install` honors the project's lockfile, found in the order above. Lockfiles fall into two kinds:

- **Layout lockfiles** describe the exact tree, and the installer places exactly that tree, downloading from the lock's `resolved` URL (or the registry default) and checking the lock's `integrity`:
  - `package-lock.json` / `npm-shrinkwrap.json` v2 and v3 (v1 refused).
  - `bun.lock` (text JSONC with trailing commas); keys such as `next/@swc/helpers` become `node_modules/next/node_modules/@swc/helpers`. `bun.lockb` (binary) is refused with the `--save-text-lockfile` hint.
  - What gets placed is computed by walking from `package.json`'s dependencies (dev ones with `-dev`, the default) through Node's upward resolution over the lock's paths. Native optional packages are skipped; `wasm32` builds are kept, and WebAssembly substitutes are added from the registry.
- **Pin lockfiles** fix versions, not layout, so the hoisting resolver runs and consults a `pinner`:
  - `pnpm-lock.yaml` 6.0/9.0: root versions from `importers["."]`, the rest from each placed package's snapshot (`node.lockKey`, peer suffixes kept), integrity from `packages`.
  - `yarn.lock` v1: Yarn's own format, keyed by `name@range` descriptors.
  - Yarn Berry YAML (`name@npm:range`): its checksums are of Yarn archives, so integrity comes from the registry; `@patch:` entries (PnP compatibility patches) are skipped.
  - A WebAssembly substitute the lock doesn't know falls back to the registry.

Behavior like `npm ci`: a `package.json` dependency missing from the lock is an "out of date" error, and the lockfile's integrity is authoritative. Workspaces, `link:` / `file:`, and non-npm Yarn resolutions are refused for now.

**Bundled dependencies** (`bundleDependencies` / `bundledDependencies`) ship inside their parent's tarball: npm's `inBundle` entries are skipped and the resolver does not request bundled names (without a lockfile they had been installed a second time, hoisted).

**Integrity**: SRI strings accept sha512, sha384, sha256, and sha1 (older lockfiles record sha1); the strongest listed algorithm wins. Dependency `github.com/goccy/go-yaml` v1.19.2.

### `og install` resolution speed-up (`internal/install/cache.go`, `install.go`, `cmd/og/install.go`)

- **Prefetch**: queueing a dependency starts its metadata request (single-flight per name and spec, 16 at a time) so the queue finds it ready. Peers, unsupported specs, and lockfile pins are not prefetched; npm aliases prefetch their target.
- **Per-version manifests**: `/<name>/<version|tag>` is about 3 KB, while the abbreviated `/<name>` is 25 MB for `next`. An exact version or dist-tag asks for itself, a range asks for `latest` (`quickSpec`); `quickPick` uses that manifest when the range is satisfied (prefers the latest, as npm does), otherwise falls back to whole metadata. Lockfile exact pins use `versionManifest` the same way.
- **Download while resolving**: each node placed in the tree starts its tarball download into a staging directory (16 at a time); the layout step waits for them, and downloads still running are cancelled if the install fails.
- **Persistent cache** (`Options.CacheDir`; `og install -cache dir|off`, default `<UserCacheDir>/oganesson/npm`): metadata stored by URL hash with its ETag, reused within `Cache-Control: max-age` (300 s on the registry) then revalidated with `If-None-Match`; tarballs stored by integrity and verified on every use (corrupted files dropped and refetched).
- Results for a Next app of 189 direct+transitive packages: cold 52 s → 36.7 s (prefetch) → 29 s (per-version manifests); warm cache 4.8 s; cached metadata 219 MB → 15 MB. The resolved tree (694 package versions) matched the prefetch-only resolver's exactly.

### Cross-platform app binaries (`cmd/og`, `runtime/app`, `pack`)

`og build -o app -target goos/goarch [-runtime og-for-target]`. An app binary is an og runtime with the app appended (`pack.Write`, which strips any existing payload from the runtime). For another platform the runtime is an og built for that platform, resolved via:

- `-runtime <file>`, or
- the runtime cache `$UserCacheDir/oganesson/runtimes/<source>/og-<goos>-<goarch>[.exe]`, where `<source>` is the VCS revision (`-dirty` when modified) or the module version (`devel` for unstamped builds).
- When neither exists, the error gives the exact `scripts/build-og.sh <path> goos/goarch` command. The runtime is resolved before building, so a missing one costs nothing. Windows outputs get `.exe`.

**Runtime validation** (`checkRuntime`, via `debug/buildinfo`, which reads ELF, Mach-O, and PE on any host): the file must be `cmd/og`, built for the requested GOOS/GOARCH, without cgo, and from the same source when both builds carry a VCS revision (`sameSource`).

**App format version** (`app.FormatVersion`, `meta.json` `format`): a runtime refuses to serve an app built for another format, with a message pointing at mismatched runtimes. This guards unstamped builds (such as this repository, with no commits yet).

### `og build -eject` and `-readable` (`cmd/og/eject.go`)

`og build -eject <dir> [-og-source <checkout>]` writes the built app as a standalone Go module:

- `app/`: the build output (server.js, meta.json, prerender.json, `_next/`, `public/`), embedded with `//go:embed all:app` (the `all:` prefix is needed for `_next/`).
- `main.go`: serves the app with `app.Serve`, the same server og builds into app binaries.
- `wrap.go`: a `wrap(http.Handler) http.Handler` hook for the user's routes and middleware; it starts with `GET /healthz`.
- `go.mod` and a README (with `go build -tags nodynamic`).

**Module details**: the module name comes from package.json's name (`@Acme/My App` → `acme-my-app`). `go.mod` requires `github.com/moriyoshi/oganesson` at `ogModuleVersion()`, which picks the first usable value from: the version injected at link time (`-ldflags "-X main.version=..."`, set by `scripts/build-og.sh` from `OG_VERSION`, which must be `vX.Y.Z[-pre]`; `release.yml` passes `$GITHUB_REF_NAME`), then `debug.BuildInfo.Main.Version` (set by `go install ...@vX`), then the zero pseudo-version. A version is "usable" only if semver-valid and canonical (`semver.Canonical(v) == v`), which rejects Go's `...+dirty` VCS stamps and non-module-valid semver like `v1.2`. Until Oganesson is published, the zero pseudo-version only resolves through a `replace`, so `go.mod` needs `replace` directives for Oganesson and for TypeScript-Go (Oganesson's own `replace` of `github.com/microsoft/TypeScript/tsc` does not apply to dependents); with `-og-source` they are written, otherwise they are commented-out instructions.

**Re-ejecting** replaces `app/` and the README and keeps the user's Go files and `go.mod`. It refuses a non-empty directory that is not an ejected module.

**Serving moved**: `app.Serve(ctx, fsys, addr, wrap)` and `app.DefaultAddr` replace `cmd/og`'s private serve code, so og and ejected projects serve identically.

**`-readable`** (`next.Options.Readable`) leaves the browser bundle and stylesheets unminified; the server bundle was never minified. Works with or without `-eject`.

### Native-module substitutes (`runtime/natives`)

The `Registry` keeps the substitution policy in one place: per native package, what og install adds next to it (`InstallFor`), an alias for server code (`Alias`), or a built-in module (`Plugin`, `Install`). The old installer `wasmSubstitutes` map (`lightningcss` → `lightningcss-wasm`) moved into it. The installer (pinned and layout lockfiles alike), the server build (`natives.Plugin()` before the builder's resolver, `natives.Alias` at the top of `builder.resolve`), and the runtime (`runtime/app` creates `natives.Services` once per process and installs them into every engine) all read it.

- **bcrypt → bcryptjs**: og install adds `bcryptjs@^3.0.2` next to `bcrypt`, and server imports of `bcrypt` resolve to it.
- **sharp → `og:sharp`** (`js/sharp.js` on `sharp.go`, built on `runtime/imageopt`): supports a common subset (metadata, rotate/flip/flop/extract/resize fit modes, grayscale, jpeg/png/webp/gif/toFormat, toBuffer/toFile/clone). Other operations (avif, composite, blur, streams, `create`) throw "not supported by Oganesson". The imageopt SQL-adjacent decode/encode primitives are detailed in [[databases-drivers-and-orms]] only for SQLite; image internals live with the native module itself.
- **better-sqlite3 → `og:better-sqlite3`** (`js/better-sqlite3.js` on `sqlite.go`, modernc.org/sqlite through `database/sql`): one pool per file per process shared by request engines; full statement/transaction/pragma API. The detailed SQL shim behavior belongs to [[databases-drivers-and-orms]].

The native SQL drivers and ORMs are covered by [[databases-drivers-and-orms]]; user-defined `og:<name>` Go modules are covered by [[native-go-modules]]. The og binary grew from 92.8 MB to 99.0 MB when these substitutes were added.

### `-tags nodynamic` release decision

Release builds and packaged apps use `-tags nodynamic` to keep gen2brain/webp from dlopening a system libwebp, so the shipped pure-Go WebP encoder is what actually runs. `scripts/build-og.sh [out] [goos/goarch]` builds with `CGO_ENABLED=0 -trimpath -tags nodynamic`; `og build -o` copies the running `og` into each app, so this is how app binaries are built too. The gate commands (AGENTS.md, QUALITY_GATE.md, TESTING.md) all pass `-tags nodynamic`. This mattered because Homebrew's libwebp on the dev machine had made every earlier image test exercise the system library through dlopen rather than the shipped encoder. AVIF output was skipped by user decision.

## Files

- `internal/install/lockfile.go` — lockfile parsing and placement for all five formats.
- `internal/install/cache.go`, `internal/install/install.go`, `cmd/og/install.go` — prefetch, per-version manifests, download-while-resolving, persistent cache.
- `cmd/og/eject.go` — `og build -eject`, module naming, `go.mod` version pinning (`ogModuleVersion`), re-eject.
- `cmd/og/main.go` — `var version string` set at link time.
- `runtime/app` — `app.Serve`, `app.DefaultAddr`, `app.FormatVersion`, per-process `natives.Services`.
- `pack` — `pack.Write` appends the app payload to a runtime.
- `runtime/natives` — `Registry` of native-module substitutes; `sharp.go` / `js/sharp.js`, `sqlite.go` / `js/better-sqlite3.js`.
- `runtime/imageopt` — exported `Decode` / `Orientation` / `Orient` / `Scale` / `Encode` / `Optimize` backing `og:sharp`.
- `scripts/build-og.sh` — builds og and app runtimes with `CGO_ENABLED=0 -trimpath -tags nodynamic`; reads `OG_VERSION`.

## Test Coverage

- `TestLockfiles` — five formats × dev/prod against a fake registry where highest matches differ from pins; integrity mismatch; out-of-date lockfiles for npm/yarn/pnpm. Neutralized by disabling lockfile loading (every format installs the registry's highest matches).
- `TestLockfileBundled` — npm `inBundle`, pnpm, and no-lockfile bundled cases, plus Yarn `patch:`. Neutralizing the bundled skip fails pnpm and no-lockfile cases.
- `TestMetadataPrefetch`, `TestInstallCache` (max-age, etag, corrupted tarball), `TestDownloadWhileResolving`, `TestVersionManifests` — resolution speed-up; neutralizing `quickPick` or the Accept header fails them.
- `TestCheckRuntime` (cross-compiled hello programs: wrong program, wrong platform, missing file), `TestSameSource`, `TestParseTarget` in `cmd/og`; `TestFormatVersion` (missing/future formats) in `runtime/app`.
- `TestEject` — module naming, `replace` directives with/without `-og-source`, app files, `main.go`, re-eject preservation and stale-file removal, refusals, and the go.mod version cases (injected, pre-release, non-canonical, `+dirty`). Neutralizing the keep-user-files rule or ignoring `version` in `ogModuleVersion` fails it.
- `TestSharp`, `TestSharpPixels`, `TestBetterSqlite3`, `TestSqliteShared` in `runtime/natives`; `TestNativePackages` checks the fixture `app/api/natives/route.ts` JSON (neutralizing the bcrypt alias makes real bcrypt fail with "No native build was found").

All run with `-tags nodynamic`.

## Pitfalls

- The npm registry answers `/<name>/<version>` with 406 for `Accept: application/vnd.npm.install-v1+json`; per-version manifests must ask for `application/json`. Getting this wrong makes every manifest request fail and the fallback fetch each package's whole metadata serially (79 s cold, slower than before).
- `-trimpath` leaves `-ldflags` out of `go version -m`, so verify the injected version by running `strings` on a built binary, not `go version -m`.
- In an ejected module, `go build -o app .` writes the binary into the `app/` directory where the next build would embed it; the README uses the project name as the output instead.
- App format version is the guard for unstamped builds (no VCS revision), such as this repository; `sameSource` only applies when both builds carry a VCS revision.
- Fake-registry tarballs once iterated a map for file order, so their bytes (and integrity) varied between calls; file order is now fixed. Test proxies must forward `Accept` and the upstream status, or a neutralization can pass for the wrong reason.
