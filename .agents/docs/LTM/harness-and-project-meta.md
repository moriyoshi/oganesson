# Harness and Project Meta

## Summary

Project-level infrastructure for Oganesson: the agent-coding harness (the
`.agents/` docs, skills, and quality gate adopted from moriyoshi/cornus), the
rename from "gots" to "Oganesson" and the naming conventions it fixed, the CI/CD
workflows, and the third-party-notices / license-attribution system that embeds
license text into `og` itself, into packaged app binaries, and into ejected
modules. These are the concerns that govern how the repo is developed and
released rather than what the runtime does.

## Key Facts

- Project renamed gots → Oganesson on 2026-10-06 (user decision). Conventions:
  CLI/binary `og`, built-in modules `og:*`, internal headers `x-og-*`, build
  output `.og/`, config `og.config.*`, payload trailer `OGPAYLD1`, JS hooks
  `Symbol.for("og.*")` and `__og*` globals, esbuild namespaces/plugins `og-*`,
  Go module `github.com/moriyoshi/oganesson`.
- The harness came from cornus at commit `df47166a`: `AGENTS.md` (with
  `CLAUDE.md` as a symlink), `.agents/docs/` (`OVERVIEW.md`, `JOURNAL.md`,
  `LTM/INDEX.md`, `TODO.md`, `TESTING.md`, `QUALITY_GATE.md`), root
  `ARCHITECTURE.md`, and the memory-cycle skills.
- CI (`.github/workflows/ci.yml`) runs lint and tests only, all on
  `ubuntu-latest`. It builds nothing; release is the only place binaries are
  built.
- Tagged releases build the six supported OS/architecture binaries
  sequentially in one job, verify each binary's embedded Go build metadata,
  and publish all archives atomically with `SHA256SUMS`.
- Notices system: `scripts/collect-notices.sh` generates a deterministic
  `runtime/notices/THIRD_PARTY_NOTICES.txt` (~260 KB, checked in). `og licenses`
  and `<app> -licenses` print it; ejected modules get `-licenses` too.
- The root `NOTICE` summarizes the principal dependencies and points to the
  generated notices file as the authoritative license text.
- App npm licenses are collected at build time by `internal/licenses` from
  esbuild metafiles plus build-host reads of package CSS and fonts, then
  appended after og's own notices.

## Details

### The cornus harness (adopted 2026-10-06, source `df47166a`)

Adopted and rewritten for the project:

- `AGENTS.md`, with `CLAUDE.md` as a symlink to it. Rules cover project
  decisions, the gate commands, fixture/skip semantics, and prezto shell
  pitfalls (including the zsh `=word` expansion trap).
- The `.agents/docs/` set: `OVERVIEW.md`, `JOURNAL.md`, `LTM/INDEX.md`,
  `TODO.md`, `TESTING.md`, `QUALITY_GATE.md`.
- Root `ARCHITECTURE.md` as the canonical design document.
- Skills in `.agents/skills/`, exposed as `.claude/skills`: the memory cycle
  (`good-sleep`, `deep-sleep`, `reconcile-journal-ltm`, `distill-memories`) and
  `tackle-todos`. Each was adapted to the project's topics, package filters, and
  docs layout; `distill-memories` targets `OVERVIEW.md` and `ARCHITECTURE.md`
  (no docs site).
- Workspace layout `.agents-workspace/{tmp,worktrees}` with `.claude/worktrees`
  symlinked into it, plus the cornus `.gitignore` files for `.agents/`,
  `.agents-workspace/`, and `.claude/`.

Not adopted (cornus-specific): `translate-documents` and the JA/ZH glossaries,
`assess-doc-quality` (VitePress), `web-screenshot` (needs `npx playwright`; this
project uses the Playwright MCP tools), `DESIGN_SYSTEM.md`, the E2E Starlark
harness docs. `audit-licenses` was not ported but is worth porting before a
release (tracked in TODO.md).

The pre-harness milestone records (`docs/m0-findings.md` … `docs/m5-status.md`,
`docs/kv.md`) were folded into JOURNAL.md as dated entries and `docs/` removed.

### The rename (gots → Oganesson, 2026-10-06)

An ordered rule set ran over all repo-authored files, excluding vendored trees,
`node_modules`, and the append-only journal. Order: module path; `cmd/gots` →
`cmd/og`; `gotsapi` → `ogapi` (TS-Go overlay, reinstalled into
`third_party/typescript/tsc/ogapi`); `GOTSPKG1` → `OGPAYLD1`; `GOTS_*` →
`OG_*`; `__gots*` → `__og*`; `x-gots-` → `x-og-`; `gots.config` → `og.config`;
`gots:` → `og:`; `Symbol.for("gots.…")` → `og.…`; `gots-` namespaces/plugins →
`og-`; `.gots/` → `.og/`; CLI commands in prose → `og <cmd>`; remaining prose →
"Oganesson". Files renamed: `cmd/og/`, `_tsoverlay/ogapi/ogapi.go`, the
fixtures' `og.config.ts` and `og-env.d.ts`. The wazero compilation cache moved
to `$UserCacheDir/oganesson/wazero`. Older JOURNAL entries keep the old names.

The rename exposed a bug: `isSlotExport` compared a hard-coded 10-byte prefix
(`"__gots_fn_"`); it now uses a shared `slotExportPrefix` constant with
`strings.HasPrefix`. `TestWebAssembly` caught it (slot exports had leaked into
`Module.exports`). (The wasm-threads-stop-on-close work landed in the same
journal entry; see [[wasm-on-wazero]].)

### CI/CD

CI (`.github/workflows/ci.yml`, triggered on push to `main`, pull requests, and
`workflow_call`) runs lint and tests only, and builds nothing (changed
2026-10-09). The `lint` job runs gofmt over `cmd internal runtime pack
_tsoverlay`, vet, genprelude freshness, and the notices diff (`go vet` and `go
test` still compile every gated package). Test shards are `core`, `next`, and
`vite`, all on `ubuntu-latest` (`-race -timeout 60m -v`; skipped tests listed in
the job summary, logs uploaded). The earlier `cross-build` job (six
`scripts/build-og.sh` targets) and the `go build ./...` step were removed:
per-platform binaries are built only in release. The `os` matrix key is gone
from job names, step summary, and artifact names.

Supporting pieces:

- `.github/actions/typescript`: a composite action that restores
  `third_party/typescript` from cache and always re-runs
  `scripts/sync-typescript.sh` (reinstalls `_tsoverlay/ogapi`).
- `.github/workflows/release.yml` (`v*` tags): `needs: ci` (waits for `lint` and
  `test`), then one job builds Linux, macOS, and Windows for amd64 and arm64
  sequentially. Sharing checkout, TypeScript-Go sync, module downloads, and the
  Go build cache avoids six independent matrix jobs. Each binary is checked
  with `go version -m` for the requested `GOOS`, `GOARCH`, `CGO_ENABLED=0`, and
  `-tags=nodynamic` before packaging. One complete `dist` artifact feeds the
  publishing job, which adds `SHA256SUMS`; tags containing `-` are prereleases.
  Linux/macOS archives are `.tar.gz`, Windows archives are `.zip`, and each
  contains the executable, `LICENSE`, `README.md`, and
  `THIRD_PARTY_NOTICES.txt`.
- `scripts/install-fixtures.sh`: builds og once and runs `og install` over
  fixture directories or patterns.

CI pitfalls: `-race` needs cgo, so CGO is not disabled globally in CI; only
`scripts/build-og.sh` sets `CGO_ENABLED=0`. Fixtures have no lockfiles (direct
deps pinned exactly, transitive ones float), so the fixture cache key includes
the ISO week. genunicode is not re-run in CI (it downloads the UCD). Release
outputs must stay outside the checkout: an untracked file makes Go stamp
`vcs.modified=true`, the source id becomes `<rev>-dirty`, and `og build -target`
rejects runtimes whose source id differs.

### Third-party notices embedded into og

`runtime/notices` (public so ejected modules can import it; `internal/` cannot
be imported by an ejected module) holds `notices.Text` via `//go:embed
THIRD_PARTY_NOTICES.txt`, with `//go:generate sh ../../scripts/collect-notices.sh
THIRD_PARTY_NOTICES.txt`.

`scripts/collect-notices.sh` writes one deterministic file: Oganesson's own
`LICENSE` first, the Go toolchain's LICENSE, then the union of `go list -e -deps`
over the six release targets (`OG_NOTICE_TARGETS` overrides), TypeScript's
repository `NOTICE.txt` (Apache-2.0 section 4(d)), and every vendored JS tree.
`LC_ALL=C`, no paths, no toolchain version. `-e` is required because
`runtime/notices` embeds the file being generated. The collector exits non-zero
when a component has no license file; napi-wasm (no LICENSE file, MIT in
package.json) is special-cased. Homebrew's Go keeps `LICENSE` one level above
GOROOT, so the collector checks both places. The notice set is per target
(windows links `github.com/Microsoft/go-winio` and not `github.com/google/uuid`).

The test guard mirrors the generator rather than trusting Go build information
alone. `TestNoticesCoverVendoredJS` walks the same four vendored-JS trees,
applies the same nested-`node_modules` and build-output exclusions, and requires
one notice heading per discovered package. Its `specialNoLicense` map mirrors
the generator's explicit exceptions (currently `napi-wasm`).
`TestNoticesCoverLinkedModules` additionally checks the vendored-Go
goja_nodejs section and generated Unicode emoji-data section. A zero-package
walk fails instead of passing vacuously.

The root `NOTICE` gives Oganesson's MIT license, summarizes the principal
linked and vendored components with their actual licenses, and directs readers
to `runtime/notices/THIRD_PARTY_NOTICES.txt` for the complete authoritative
text. In particular, minimatch uses the Blue Oak Model License 1.0.0 rather
than ISC.

Surfaces:

- `og licenses` (command table in `cmd/og/main.go`).
- `<app> -licenses` for packaged binaries (flag parsing in `appFlags`,
  `serve.go`).
- The ejected `main.go` template gets `-licenses` too. Eject writes
  `THIRD_PARTY_NOTICES.txt` at the module root on every eject (the user's
  `main.go` is kept on re-eject, so older ejects do not get the flag). The
  ejected README mentions it, and that modules the user adds are not listed.

CI diffs a regenerated `THIRD_PARTY_NOTICES.txt` (as it does for `prelude.js`).
The embedded text is a superset for any single target and for ejected binaries
(which link fewer modules); over-attribution is harmless, a missing entry is
not.

### App npm licenses collected at build time

`internal/licenses` provides a `Collector` fed with esbuild metafiles. Every
input under `node_modules` is attributed to the nearest `package.json` with a
`name` between the file and its node_modules package directory (unnamed
`dist/package.json` files are skipped), so `next/dist/compiled/react-dom` counts
as its own package. Keys are normalized like `parseMetafile`: `ns:` prefixes,
Vite's `\x00` and `?query`, relative paths joined to the working directory.
`Text()` renders one section per name@version (shallowest copy wins), with the
declared license (`license` string or object, or the legacy `licenses` array)
and every `LICENSE*` / `LICENCE*` / `COPYING*` / `NOTICE*` file. A nil Collector
ignores everything.

Wiring:

- Next (`builder.licenses`): server passes, browser and pages-browser builds,
  and CSS builds (Metafile enabled for the last two). Exposed as
  `Output.Notices`; `cmd/og` writes it as `notices.AppFile`.
- Vite (`Options.licenses`, set by `Build`, which wraps `build`): the Rolldown
  facade (`installRolldown(e, col)`), `bundleServer`, and `bundleProgram`; the
  preview server's bundle passes nil (not shipped). `Output.Notices`; `Payload`
  writes it.
- `runtime/notices`: `AppFile` and `Write(w, app fs.FS)`, which prints og's
  text, then a `#` separator and the app's file. App binaries, the ejected
  `main.go` (now opens `fsys` before handling `-licenses`), and the ejected root
  `THIRD_PARTY_NOTICES.txt` use it.

Findings: vite-react collects exactly react, react-dom, and scheduler.
next-hello collects ~26 packages (next, Next's compiled copies like
`react-builtin` / `react-dom-builtin`, styled-jsx, bcryptjs, `@vercel/og`
(MPL-2.0)). The payload file is not served (Next answers 404, the SPA answers
with its index.html fallback).

Esbuild metafiles do not see assets that build-time JavaScript reads and
inlines. `internal/jshost/fs.go` therefore records successful reads through
`Symbol.for("og.fs")`; `Host.AssetReads` and `PluginHost.AssetReads` return
only node_modules files whose extensions can ship (`.css`, `.scss`, `.sass`,
`.woff`, `.woff2`, `.ttf`, `.otf`, `.eot`). Next and Vite feed those files to
`Collector.AddFile`, covering PostCSS/Tailwind CSS and package-owned local
fonts without attributing build-only JavaScript such as PostCSS, Sass, Babel,
TypeScript, Vite, or Tailwind's build plugins. `Collector.AddNotice` provides a
deduplicated synthetic Google Fonts notice when `next/font/google` downloads a
font at build time. Vite calls the same collection path after every app-building
`EvalWith`, including Astro and Nuxt.

### The logo

The final square mark is `assets/branding/oganesson-logo.svg` with a 512 px PNG
preview: a rounded periodic-table tile with `118`, `Og`, `OGANESSON`, three
stylized electron orbits, and a dense nucleus with perspective, depth shading,
and coral and blue particles; transparent padding. The nucleus has 118 proton
and 176 neutron instances for oganesson-294, but the coordinates are
illustrative, not predicted positions. Intermediate variants remain in
`assets/branding/` for comparison.

### Shared-prerender fixture optimization (2026-10-07)

The `internal/frameworks/next` suite under `-race` was 702 s (past `go test`'s
default 10-minute timeout) because every `serveFixture` reprerendered every
route even though the build was cached (`fixtureOnce`). `serveFixture` now
builds and prerenders once (`packageFixture`, cached as `fixtureFS`); each test
still gets a fresh `app.Handler` so its caches, KV, and SQLite stay isolated.
The suite dropped to 305 s. The shared prerender exposed a timing assumption in
`TestRouteHandlerCaching` (`api/hourly` with `revalidate = 60` was prerendered
minutes before and correctly served STALE then revalidated); the fixture now
uses `revalidate = 3600` and the test accepts STALE, comparing bodies only on a
HIT.

### TODO sweep (2026-10-13, tackle-todos)

Swept TODO.md plus every `// TODO` / `// FIXME`. Only four code TODOs exist
(none in og-authored JS/TS): `internal/install/install.go:166` (package-lock
workspace layout, deliberately set aside); `runtime/webapi/crypto.go:22`
(asymmetric WebCrypto, a large feature); `internal/frameworks/next/client.go:21`
(per-route code splitting, needs og's own linker over esbuild);
`internal/frameworks/next/tree.go:18` (experimental `global-not-found`, the one
bounded item). The rest of the TODO.md backlog is design-category, user-deferred,
or upstream Sobek/Goja reports. Only the data-only item was taken: dummy env
values for three client-only Next examples (`with-google-tag-manager`,
`with-facebook-pixel`, `with-google-maps-embed`) in `testdata/compat/env.json`,
left unverified (verifying needs a full `og compat` run). The TLS-roots item
(TODO line 133) turned out non-trivial (new dependency
`golang.org/x/crypto/x509roots/fallback` and a trust-store design decision) and
was left as a design item; Go's system roots remain the deliberate behavior.

## Files

- `AGENTS.md`, `CLAUDE.md` (symlink) — project rules and conventions.
- `.agents/docs/{OVERVIEW,JOURNAL,TODO,TESTING,QUALITY_GATE}.md`,
  `.agents/docs/LTM/INDEX.md`, `ARCHITECTURE.md` — harness docs.
- `.agents/skills/` (exposed as `.claude/skills`) — memory-cycle and
  tackle-todos skills.
- `.github/workflows/ci.yml` — lint + test on Linux.
- `.github/workflows/release.yml` — `v*` tag releases.
- `.github/actions/typescript` — restore/sync TypeScript-Go composite action.
- `scripts/collect-notices.sh` — generates `THIRD_PARTY_NOTICES.txt`.
- `scripts/install-fixtures.sh` — builds og and installs fixtures.
- `scripts/build-og.sh` — the only CGO-off build (release binaries).
- `runtime/notices/` — `notices.Text`, `AppFile`, `Write`, embedded
  `THIRD_PARTY_NOTICES.txt`.
- `NOTICE` — root summary pointing to the generated complete notices.
- `internal/licenses/` — build-time npm license Collector.
- `internal/jshost/fs.go` — build-host asset-read recorder.
- `cmd/og/main.go` (`og licenses`), `cmd/og/serve.go` (`appFlags`, `-licenses`).
- `assets/branding/oganesson-logo.svg` — the logo.

## Test Coverage

- `TestNoticesCoverLinkedModules` (`cmd/og/licenses_test.go`): every dependency
  in the test binary's build info has a heading in `notices.Text`, plus
  Oganesson's MIT header, the Go section, TypeScript's NOTICE, vendored Go, and
  the Unicode emoji data. `TestNoticesCoverVendoredJS` mirrors the generator's
  four-tree discovery. A throwaway package with only `VERSION` made the latter
  fail with the expected missing-notice diagnostic.
- `TestAppFlags`; `TestEject` (checks the main.go flag, that the root
  `THIRD_PARTY_NOTICES.txt` equals `notices.Text`, and refresh on re-eject;
  with app npm licenses, that the root file is og's text followed by the app's).
- `internal/licenses` `TestCollect` and `TestAddNotice` (synthetic node_modules covering scoped
  packages, unnamed `dist/package.json`, vendored named copies, the `licenses`
  array, nothing declared, duplicates, app code, virtual keys, order
  independence) and `TestEmptyAndNil`.
- `TestBuildReactNotices` (vite: exactly react/react-dom/scheduler, React's
  license text, the payload file); `TestFixtureNotices` (Next: Tailwind's
  shipped CSS is attributed while build-only packages are absent);
  `TestNoteGoogleFonts`; `TestBuildTailwind` (Vite: Tailwind CSS attributed,
  build-only Vite/Tailwind packages absent).
- `runtime/notices` `TestWrite`.
- `TestWebAssembly` caught the `isSlotExport` prefix bug during the rename.
- End to end: `scripts/build-og.sh` binaries' `og licenses` / `-licenses`
  (og, packaged next-hello and vite-react, and a built ejected module) all equal
  the checked-in file byte for byte; regeneration is byte-identical across runs.
- Release validation cross-built all six binaries, checked their embedded build
  metadata, and inspected representative Linux and Windows archives. Presenting
  a Linux binary as Windows was rejected by the metadata assertion.

## Pitfalls

- Over-attribution in the embedded notices is harmless; a missing entry is not.
  The text is a superset for any single target and for ejected binaries.
- The app's own npm dependencies (Next.js, React, …) ship in the payload; the
  header says so and does not re-attribute them in og's own notices (they are
  covered by the build-time app npm collection instead).
- The remaining app-license gap is over-attribution in Astro static builds:
  packages used only while prerendering can be listed even though their code is
  not shipped. The missing CSS/font attribution gaps are closed.
- The root `NOTICE` exists; porting cornus' `audit-licenses` skill remains open.
- Release outputs must stay outside the checkout (untracked files dirty the VCS
  stamp and break `og build -target`).
- Older ejected modules do not get the `-licenses` flag because eject keeps the
  user's `main.go` on re-eject.
- The installed Next fixture suite can exceed Go's default 10-minute timeout;
  use `-timeout 30m` or a focused `-run`, and diagnose the `panic:` line rather
  than an arbitrary blocked goroutine in the timeout dump.
- After deliberately breaking and restoring a fix, rerun the green test.
  `gofmt` can reshape a neutralization so a scripted textual revert silently
  misses it.
