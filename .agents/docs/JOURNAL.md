# Oganesson Development Journal

Chronological log of findings, insights, and review history. New entries are
normally appended; `good-sleep` consolidates them into `.agents/docs/LTM/`, and
`reconcile-journal-ltm` removes covered entries while preserving their mapping
below.

---

## LTM Consolidation Record

This journal was audited against the actual long-term-memory and TODO content
on 2026-10-16. Every consolidated entry's durable design, implementation,
testing, pitfall, and follow-up information is represented under
`.agents/docs/LTM/` or `.agents/docs/TODO.md`; the consolidated source entries
and superseded record blocks have been removed. Where an entry was split, the
primary document is listed first and secondary documents follow in
parentheses.

| JOURNAL section | LTM document(s) |
|-----------------|-----------------|
| 2026-10-01: M0 feasibility findings | foundations-and-architecture (js-language-gaps-and-lowering) |
| 2026-10-01: M1 status: runtime core | runtime-engine |
| 2026-10-01: M2 status: server-rendered App Router | app-router-rendering |
| 2026-10-02: M3 status: browser bundle, hydration and client navigation | app-router-rendering |
| 2026-10-06: M4 status: route handlers, server actions, proxy/middleware | route-handlers-actions-middleware |
| 2026-10-06: Built-in key-value store (`gots:kv`) | runtime-engine |
| 2026-10-06: M5 status: static generation and ISR | static-generation-and-isr |
| 2026-10-06: M6 (part 1): CSS, esbuild JS plugins, webpack loader shim, build-time require, unenv | build-system-esbuild-layers |
| 2026-10-06: Adopted the agent coding harness from moriyoshi/cornus | harness-and-project-meta |
| 2026-10-06: M6 (part 2): PostCSS and Tailwind v3 | css-pipeline |
| 2026-10-06: M6 (part 3): WebAssembly on wazero, Tailwind v4 | wasm-on-wazero (css-pipeline) |
| 2026-10-06: Renamed gots to Oganesson; wasm threads stop on close | harness-and-project-meta (wasm-on-wazero) |
| 2026-10-06: M6 (part 4): static metadata files | assets-images-fonts-metadata |
| 2026-10-06: M6 (part 5): `next/image` | assets-images-fonts-metadata |
| 2026-10-06: M6 (part 6): `next/font` | assets-images-fonts-metadata |
| 2026-10-06: M6 (part 7): Sass, CSS Modules `composes`, `experimental.useLightningcss` | css-pipeline |
| 2026-10-06: M6 (part 8): `ImageResponse` (`next/og`), runtime WebAssembly, `Intl.Segmenter` | assets-images-fonts-metadata (wasm-on-wazero) |
| 2026-10-06: `Intl` from FormatJS; RegExp Unicode property escapes | unicode-regexp-intl |
| 2026-10-06: M6 follow-ups: route handler prerendering, next/font transform gaps, live sassOptions | assets-images-fonts-metadata (css-pipeline) |
| 2026-10-06: Oxide's slow scans explained | css-pipeline |
| 2026-10-06: M6 closed; release builds use `-tags nodynamic` | install-and-packaging |
| 2026-10-06: M7 (part 1): lockfiles in `og install` | install-and-packaging |
| 2026-10-06: M7 (part 2): cross-platform app binaries | install-and-packaging |
| 2026-10-07: M7 (part 3): `og build -eject` and `-readable` | install-and-packaging |
| 2026-10-07: M7 (part 4): native-module substitutes | install-and-packaging (databases-drivers-and-orms) |
| 2026-10-07: Fixture tests share one prerender | harness-and-project-meta |
| 2026-10-07: M8 (part 1): Pages Router pages | pages-router |
| 2026-10-07: M8 (part 2): Pages API routes, getStaticPaths fallback, SSG cache consistency, on-demand revalidation | pages-router (static-generation-and-isr) |
| 2026-10-07: M8 (part 3): `fallback: true`, app and pages in one project, subresource 404s | pages-router |
| 2026-10-07: M8 (part 4): draft / preview mode, revalidation status; TanStack added to M9 | pages-router |
| 2026-10-07: Milestone map: databases and ORMs (M10) | databases-drivers-and-orms |
| 2026-10-07: M8 (part 5): Pages Router i18n; M8 done | pages-router |
| 2026-10-07: M9 design: Vite's own JS on a Rolldown facade over esbuild | vite-and-rolldown-facade |
| 2026-10-07: M9 (part 1): Vite SPA builds | vite-and-rolldown-facade |
| 2026-10-07: Spike: Rolldown's WASI binding on wazero | vite-and-rolldown-facade |
| 2026-10-07: M9 (part 2): TanStack Router and TanStack Start | tanstack |
| 2026-10-07: M9 part 3: TanStack Start streaming, server routes, middleware, prerendering, SPA mode, RSC | tanstack |
| 2026-10-07: M9 part 4: TanStack Start remaining features | tanstack |
| 2026-10-07: M9 part 5: Vite facade gaps, Vue / Svelte / Tailwind | vite-and-rolldown-facade (vite-framework-ecosystems) |
| 2026-10-07: M9 part 6: Express-style SSR (server programs) | server-programs-ssr |
| 2026-10-07: M9 part 7: Fastify, Koa and Hono's node adapter | server-programs-ssr |
| 2026-10-07: M9 part 8: Preact / Solid / UnoCSS, streaming zlib, the esbuild renamer race | vite-framework-ecosystems (js-language-gaps-and-lowering, node-compat-shims) |
| 2026-10-07: CI/CD workflows, fixture installer, third-party notices | harness-and-project-meta |
| 2026-10-07: M9 part 9: h3/srvx, tsconfig extends, chained source maps, vite-plugin-pwa | server-programs-ssr (vite-framework-ecosystems, vite-and-rolldown-facade) |
| 2026-10-07: og licenses | harness-and-project-meta |
| 2026-10-07: Oganesson logo | harness-and-project-meta |
| 2026-10-07: App npm licenses collected at build time | harness-and-project-meta |
| 2026-10-07: M9 part 10: Astro 7, Intl formatters, the esbuild package | vite-framework-ecosystems (unicode-regexp-intl) |
| 2026-10-07: M9 part 11: React Router 8 framework mode | vite-framework-ecosystems |
| 2026-10-07: Inline "use server" functions (Server Action closures) | route-handlers-actions-middleware |
| 2026-10-07: M9 part 12: SvelteKit 3 (adapter-node) | vite-framework-ecosystems |
| 2026-10-07: The gate runs without `-race` | runtime-engine |
| 2026-10-08: M9 part 13: Nuxt 4 | vite-framework-ecosystems |
| 2026-10-08: SvelteKit's error page, split server builds, and the esbuild define race | vite-framework-ecosystems (js-language-gaps-and-lowering) |
| 2026-10-08: M9 part 14: SolidStart 2, and Node's own `path` and web stream adapters | vite-framework-ecosystems (node-compat-shims) |
| 2026-10-08: Evaluating Sobek | runtime-engine |
| 2026-10-08: The engine is Sobek; Astro's Markdown (satteri) and Shiki | runtime-engine (wasm-on-wazero, vite-framework-ecosystems) |
| 2026-10-08: The RegExp rewriter's Go half deleted | unicode-regexp-intl |
| 2026-10-08: The thenable async-context gap, revisited for Astro | js-language-gaps-and-lowering |
| 2026-10-08: Thenables keep their async context (bundle-time workaround) | js-language-gaps-and-lowering |
| 2026-10-08: The RegExp v flag's set notation | unicode-regexp-intl |
| 2026-10-08: unenv audit; fs.ReadStream / WriteStream are Node's | node-compat-shims |
| 2026-10-08: Server programs print their startup output once | runtime-engine |
| 2026-10-08: M9 part 15: Vite 5 to 7, and Qwik City 1 | vite-and-rolldown-facade (vite-framework-ecosystems, wasm-on-wazero) |
| 2026-10-08: Build-time sharp | assets-images-fonts-metadata |
| 2026-10-08: The v flag's properties of strings | unicode-regexp-intl |
| 2026-10-08: Rolldown chunk names on esbuild's grouping | vite-and-rolldown-facade |
| 2026-10-08: path.matchesGlob, pruned external imports, provider environments | node-compat-shims (server-programs-ssr) |
| 2026-10-08: TanStack Start's SPA mode follows Start's server; M9 decisions | tanstack |
| 2026-10-08: M10 part 1: Drizzle and Kysely on SQLite | databases-drivers-and-orms |
| 2026-10-08: M10 part 2: node:net and node:tls on Go sockets; mysql2 | databases-drivers-and-orms (node-compat-shims) |
| 2026-10-08: M10 part 3: the Go-side connection pool | databases-drivers-and-orms |
| 2026-10-08: M10 part 4: project files at run time, and a writable in-memory filesystem | databases-drivers-and-orms |
| 2026-10-08: pglite-go v0.2.1 | databases-drivers-and-orms |
| 2026-10-09: M10 part 5: TypeORM | databases-drivers-and-orms |
| 2026-10-09: M10 part 6: Prisma 7 | prisma |
| 2026-10-09: `og build -eject` pins the release version in go.mod | install-and-packaging |
| 2026-10-09: native Go modules (`og:<name>`) | native-go-modules |
| 2026-10-09: CI runs lint and tests only, on Linux | harness-and-project-meta |
| 2026-10-09: M11 part 1: `og compat` over Next's examples | compat-corpus-m11 |
| 2026-10-09: next.config's `webpack()` hook | compat-corpus-m11 |
| 2026-10-09: Babel configs, missing exports, and V8's stack API | compat-corpus-m11 |
| 2026-10-09: build-script commands, .mjs resolution, top-level await programs | compat-corpus-m11 |
| 2026-10-10: process's uncaughtException and unhandledRejection listeners | runtime-engine |
| 2026-10-10: corpus fixes: optional dependencies, React 18 pages, browser aliases, fonts in packages, lazy routes | compat-corpus-m11 |
| 2026-10-10: `og install` resolution speed-up | install-and-packaging |
| 2026-10-10: M11 corpus part 3 | compat-corpus-m11 |
| 2026-10-10: Next 16.4.0 support | compat-corpus-m11 |
| 2026-10-10: M11 corpus part 4: turbopack rules; function names in build-time require | compat-corpus-m11 (js-language-gaps-and-lowering) |
| 2026-10-10: M11 corpus part 5: og picks the bundler | compat-corpus-m11 |
| 2026-10-11: M11 corpus part 6: workspaces, sharp 0.35 | compat-corpus-m11 |
| 2026-10-11: M11 corpus part 7: with-msw | compat-corpus-m11 (node-compat-shims) |
| 2026-10-11: M11 corpus part 8: cache keys | static-generation-and-isr |
| 2026-10-11: M11 corpus part 9: every failure explained | compat-corpus-m11 |
| 2026-10-11: M11 hardening: GraphQL Mesh; mdxRs findings | compat-corpus-m11 |
| 2026-10-12: Cache Components part 1 | cache-components-use-cache |
| 2026-10-12: Cache Components part 2: partial prerendering | cache-components-use-cache |
| 2026-10-12: Cache Components part 3: custom cache handlers and "use cache: private" | cache-components-use-cache |
| 2026-10-12: Cache Components part 4: cacheLife profiles, server action redirect origin | cache-components-use-cache |
| 2026-10-12: og compat -env | cache-components-use-cache |
| 2026-10-13 TODO sweep (tackle-todos) | harness-and-project-meta |
| 2026-10-13 Native pg shim | databases-drivers-and-orms |
| 2026-10-13 Native postgres.js and mysql2 shims | databases-drivers-and-orms |
| 2026-10-13 @libsql/client substitute | databases-drivers-and-orms |
| 2026-10-13 libsql done; Prisma-over-adapter-pg WIP | prisma |
| 2026-10-13 Prisma-over-adapter-pg hang — root-caused (correction) | prisma |
| 2026-10-13 Prisma-over-adapter-pg hang — ACTUAL root cause | prisma |
| 2026-10-13 Prisma-over-adapter-pg hang — DEFINITIVE root cause | prisma |
| 2026-10-13 pg shim: stop holding the pool mutex across the dial | prisma (databases-drivers-and-orms) |
| 2026-10-13 Prisma-over-adapter-pg hang — resolved to a pglite-go bug (#7) | prisma |
| 2026-10-13 pglite-go v0.2.3 (fixes #7) | prisma (databases-drivers-and-orms) |
| 2026-10-13 Prisma-over-adapter-pg query-path hang — CONFIRMED | prisma |
| 2026-10-13 Prisma-over-adapter-pg: capping the pool to one connection | prisma |
| 2026-10-13 pg shim: delegate node-pg's connection settings to pgx (SSL) | databases-drivers-and-orms |
| 2026-10-14 mysql2 shim: map the `ssl` option | databases-drivers-and-orms |
| 2026-10-14 TestPrismaPostgres green: four stacked root causes | prisma |
| 2026-10-14 Prisma Migrate is feasible via the WASM schema engine | prisma |
| 2026-10-14 Prisma Migrate spike | prisma |
| 2026-10-14 migrate deploy: applyMigrations validated | prisma |
| 2026-10-14 migrate deploy, step 1: implicit @prisma/schema-engine-wasm injection | prisma |
| 2026-10-14 migrate deploy, step 2: build-side packaging | prisma |
| 2026-10-14 Prisma migrate deploy at app startup: end-to-end green | prisma |
| 2026-10-14 Prisma migrate deploy: PostgreSQL build-side (env datasource) | prisma |
| 2026-10-14 pg migrate deploy: advisory lock + array params | prisma |
| 2026-10-14 Prisma migrate deploy: PostgreSQL end-to-end | prisma |
| 2026-10-14 Prisma db push at app startup (sqlite) | prisma |
| 2026-10-14 Top-level int64 / uint64 cross as JS bigint | native-go-modules |
| 2026-10-14 Propagate og/go.mod replace directives | native-go-modules |
| 2026-10-14 Cross builds prerender with the project's native Go functions | native-go-modules |
| 2026-10-14 Named-type .d.ts resolution uses the declaring file's imports | native-go-modules |
| 2026-10-14 — Completed TODO items archived from TODO.md | css-pipeline (assets-images-fonts-metadata, wasm-on-wazero, install-and-packaging, static-generation-and-isr, route-handlers-actions-middleware, cache-components-use-cache, runtime-engine, unicode-regexp-intl, node-compat-shims, native-go-modules, harness-and-project-meta, vite-and-rolldown-facade, vite-framework-ecosystems, server-programs-ssr, tanstack, databases-drivers-and-orms, prisma, compat-corpus-m11, js-language-gaps-and-lowering) |
| 2026-10-14 — Notices coverage guard extended to the vendored trees | harness-and-project-meta |
| 2026-10-14 — Root NOTICE file | harness-and-project-meta |
| 2026-10-14 — App npm license attribution for JS-host-inlined assets (Next) | harness-and-project-meta |
| 2026-10-15 — App npm license attribution extended to the Vite builder | harness-and-project-meta |
| 2026-10-16 — User documentation set and README refresh | user-documentation-and-site |
| 2026-10-16 — User guide index filename | user-documentation-and-site |
| 2026-10-16 — VitePress user-documentation site | user-documentation-and-site |
| 2026-10-16 — VitePress landing page and logo | user-documentation-and-site |
| 2026-10-16 — VitePress landing-page theme | user-documentation-and-site |
| 2026-10-16 — Separate landing and introduction pages | user-documentation-and-site |
| 2026-10-16 — User-facing capability guide | user-documentation-and-site |
| 2026-10-16 — VitePress production builds through Oganesson | vite-and-rolldown-facade (user-documentation-and-site) |
| 2026-10-16 — Shared CD cross-build job | harness-and-project-meta |
| 2026-10-16 — Deploy VitePress documentation to GitHub Pages | user-documentation-and-site (harness-and-project-meta) |
| 2026-10-16 — Completed TODO items (sweep): WebCrypto, Pages/App Router, native DB drivers, native modules, TLS | webcrypto (pages-router, route-handlers-actions-middleware, app-router-rendering, databases-drivers-and-orms, native-go-modules, node-compat-shims, harness-and-project-meta) |

### Synthesis documents

| Synthesis LTM document | Source LTM documents |
|------------------------|----------------------|
| data-and-orm-synthesis | databases-drivers-and-orms, prisma |
| next-build-and-assets-synthesis | assets-images-fonts-metadata, build-system-esbuild-layers, compat-corpus-m11, css-pipeline |
| next-rendering-and-routing-synthesis | app-router-rendering, cache-components-use-cache, pages-router, route-handlers-actions-middleware, static-generation-and-isr |
| packaging-and-native-extension-synthesis | install-and-packaging, native-go-modules |
| project-delivery-synthesis | harness-and-project-meta, user-documentation-and-site |
| runtime-platform-synthesis | foundations-and-architecture, js-language-gaps-and-lowering, node-compat-shims, runtime-engine, unicode-regexp-intl, wasm-on-wazero, webcrypto |
| vite-ecosystem-synthesis | server-programs-ssr, tanstack, vite-and-rolldown-facade, vite-framework-ecosystems |

All 26 source LTM documents feed one of these syntheses; none is intentionally
standalone. The source documents remain intact for traceability. The syntheses
also preserve the source chronology where Cache Components/PPR, Prisma's pg
type parsing, and framework-specific server splitting supersede or qualify
older notes.

See [.agents/docs/LTM/INDEX.md](./LTM/INDEX.md) for the full synthesis-first and
source-topic index.

## 2026-10-17 — Ordinary Node.js tooling workflow guide

- Added `docs/development-workflow.md`, a user-facing two-lane workflow: use each
  framework's ordinary Node.js development and production commands for fast
  feedback and comparison, then install, check, build, and test the actual
  Oganesson executable.
- Covered Next.js, plain Vite and TanStack Router SPAs, custom Vite SSR,
  TanStack Start, Astro, VitePress, React Router, SvelteKit, Nuxt, SolidStart,
  and Qwik City. The guide also records package-manager equivalents,
  dependency-tree and lifecycle-script differences, code generation, and the
  boundary around `og:*` modules under Node.js.
- Linked the guide from the README, introduction, getting-started guide, and
  VitePress sidebar.
- Verification: VitePress production build and documentation hygiene checks.

## Prisma 7 CI failures: schema-engine injection (fixed) and a flaky config-load under CPU starvation (open)

Context: after the `internal/frameworks/next` CI job was split into shards
(`next-orm`, `next-prisma`, `next-build`, catch-all `next`), the new shard
cache keys forced fresh `og install`s, which exposed two latent bugs the old
single timing-out shard had hidden.

### Bug 1 (FIXED) - og install did not inject @prisma/schema-engine-wasm for Prisma 7

- `injectPrismaSchemaEngine` (`internal/install/install.go`) read
  `@prisma/engines-version` from `@prisma/client`'s `dependencies`. Prisma 7
  moved that pin into `@prisma/client`'s `devDependencies`, which the npm
  registry's abbreviated metadata (what `pick` fetches) omits, so the value was
  empty and the schema engine was never added. A clean install then skipped the
  migrate/db-push tests and `prisma generate` had no engine.
- Fix: fall back to reading `@prisma/engines-version` from `@prisma/engines`
  (published in lockstep at the client's version, still pinned in its
  `dependencies`); keep the old `@prisma/client` path for Prisma <= 6.
- Regression test: `TestPrismaSchemaEngineInjectedPrisma7` (models the Prisma 7
  shape: client without the pin, engines with it). Neutralized: fails without
  the fix, passes with it; the Prisma <= 6 test still passes.
- Confirmed in CI: the failing run after this landed had zero
  "schema-engine-wasm not installed" skips.

### Bug 2 (OPEN) - `prisma generate` config load flakes under CPU starvation

Symptom: `prisma generate` dies with Prisma's `ConfigFileSyntaxError`
("Failed to parse syntax of config file at .../prisma.config.ts") and
`process.exit(1)`. It is the remaining CI blocker (hits any test building the
`next-orm` fixture: `TestORMDriverMySQL`, `TestTypeORM`, `TestPrisma`, ...).

Established by reproduction (fixture copy + `og build`):
- Purely timing/resource-starvation driven: 0/6 idle, 6/6 under 6-way parallel
  `og build` oversubscription. Reproduces on 2-core CI runners.
- NOT content- or collision-based: unique vs identical config content both fail
  under load (so it is not a shared temp path keyed by config content).
- No og-side error surfaces. Instrumenting every og failure path -
  module-require TS transform (`require.go`), the esbuild shim's `transform`
  AND `build` (`esbuildjs.go`), and the worker `uncaught` + run-error paths
  (`workers.go`) - none fired during failures. (Instrumentation was temporary
  and has been removed.)
- The config is loaded by `@prisma/config` -> `c12` -> `jiti` (confirmed in
  `@prisma/config/dist/index.js`). jiti uses its own bundled TS transpiler,
  which is why og's esbuild shim is never involved. So the failure is jiti/c12
  loading the TS config inside og's build host under scheduling pressure.

Next step to pin it: engine/event-loop-level instrumentation (runtime/engine)
or instrumenting jiti's transpile/evaluate path, exercised under load, to catch
what mis-behaves when the loop is starved. Fixing a scheduling/timing bug there
is non-trivial and verification is statistical (flaky). Likely owner: the
build-host engine (`runtime/engine`, `internal/jshost`).

### Bug 2 follow-up - bounded retry mitigation applied (engine race still open)

Quick targeted fix attempts for the engine race were ruled out by reproduction:
disabling jiti's filesystem cache (`JITI_FS_CACHE=0`) did not help (still 6/6
under 6-way oversubscription). The engine-level root cause remains open per the
notes above.

To unblock CI/publishing, `runPrisma` (`internal/frameworks/next/codegen.go`)
now retries `prisma generate` up to 3 times (fresh VM each run), only when it
fails and the context is not cancelled. CI's real failure mode is intermittent,
not deterministic (in one failing run `next-prisma` passed while `next-orm`
failed on the same hardware), so a bounded retry clears it. A genuinely broken
config fails all attempts and still surfaces.

Verification: under 4-way parallel `og build` oversubscription (which failed
builds before), 24 builds now had 0 build-level failures and the retry log
fired 8 times - i.e. the flake occurred mid-build 8 times and was recovered
every time. Idle regression check: `TestPrisma` and `TestTracedFiles` pass.
This is a mitigation, not a fix for the build-host engine race, which stays
tracked above for the engine owner.

## 2026-10-18 - Release target builds parallelized

- Replaced the release workflow's sequential six-target build loop with a
  six-entry `GOOS`/`GOARCH` matrix. Linux, macOS, and Windows archives for amd64
  and arm64 can now build concurrently after the shared CI workflow succeeds.
- Each matrix entry retains the release-version stamp and `go version -m`
  checks for `GOOS`, `GOARCH`, `CGO_ENABLED=0`, and `-tags=nodynamic`, then
  uploads a uniquely named artifact. The publish job waits for every matrix
  entry and merges all `dist-*` artifacts before generating `SHA256SUMS` and
  creating the release, preserving all-or-nothing publication.
- The tradeoff is six runner setups and cache restores instead of one shared
  setup/build cache; the release's wall-clock critical path is reduced at the
  cost of additional compute.
- Verification: parsed the workflow and structurally checked the exact six
  target pairs, unique artifact names, and merged download configuration.

## 2026-10-18 — Prisma MySQL query support via a native `mariadb` driver shim

Added the Prisma MySQL *query* path by substituting the `mariadb` npm driver
with a built-in shim, so the real `@prisma/adapter-mariadb` runs unchanged over
og's existing Go MySQL service. This mirrors the pg story (the real
`@prisma/adapter-pg` over the substituted `pg`), per the "use real packages"
principle.

**What changed:**

- `runtime/natives/js/mariadb.js` (new): the `mariadb` driver surface
  (`createPool`, pool/connection `query`/`execute`/`getConnection`/`release`/
  `end`) over `Symbol.for("og.mysql")`. A SELECT is returned as the driver's
  rowsAsArray result — real JS row arrays carrying a non-enumerable `meta` of
  column defs, each exposing `name()`, `type`, `flags` (UNSIGNED = 1<<5) and
  `collation.index` (63 = binary). `mapType` derives all of that from
  go-sql-driver's `DatabaseTypeName`, which already encodes signedness
  (`UNSIGNED INT`) and text-vs-binary (`TEXT` vs `BLOB`). Registered in
  `runtime/natives/natives.go` ({Package:"mariadb", Module:"og:mariadb"} +
  embed + Plugin filter/sources + ModuleSource).
- `runtime/natives/mysql.go`: a mariadb value path (`shapeMaria`/`mariaVal`) and
  host methods (`queryPoolMaria`/`queryClientMaria`, reusing acquire/release/
  registerTLS). mariaVal produces the driver-faithful values the adapter's
  fixed options (rowsAsArray, dateStrings, autoJsonMap:false/jsonStrings,
  bitOneIsBoolean:false) imply: an Int32 column a number, an Int64 column
  (BIGINT, unsigned INT) a **string** (exact beyond 2^53 — the adapter would
  otherwise stringify a JS bigint), DECIMAL a string, dates MySQL literal
  strings, binary/BIT/GEOMETRY a Buffer. `jsError` now also sets `sqlMessage`
  (mysql2 carries it; the adapter's `isDriverError` requires errno + sqlMessage
  + sqlState together). `toGo` handles a JS BigInt arg (`*big.Int`). Shared
  `decimalString`/`toFloat` gained uint64 cases (unsigned BIGINT).

**Pitfalls found:**

- `new URL(...)` throws `TypeError: Value is not an object` inside a bundled
  built-in module (the sibling shims never parse URLs in JS — they hand the raw
  connection string to the Go side, which parses it). The shim follows that
  convention and filters connection-string query params to the go-sql-driver
  allowlist with plain string ops, dropping `prepareCacheLength` (the adapter
  appends it) and any Prisma-/mariadb-level params go-sql-driver would reject.
- The host returns Go-slice-backed values; `Object.defineProperty(rows, "meta")`
  fails on them and `Array.isArray(row)` is false. `shape` rebuilds genuine JS
  arrays with `Array.from` before attaching `meta`.

**Tests:** `runtime/natives/mariadb_test.go` (`TestMariaDB`) drives the shim the
way the adapter does against dolthub/go-mysql-server: DDL, a parameterized
insert, a rowsAsArray SELECT read through `result.meta`, a committed
transaction on a pinned connection, and a driver error carrying errno +
sqlMessage. Neutralized by making BIGINT return a number — the test fails and
the 2^53+ value visibly loses precision (`...993` → `...992`). Full
`runtime/natives` suite green; `go build`/`go vet` clean.

**Not done — migrate is blocked upstream.** `@prisma/adapter-mariadb` 7.10.x
does not implement the migration-aware interface: `executeScript` throws "Not
implemented yet" and its factory has no `connectToShadowDb` (which
`bindMigrationAwareSqlAdapterFactory` requires). So `prismaAdapters`
(`internal/frameworks/next/prismamigrate.go`) was left unchanged — a mysql
entry would make the boot-time migrator throw rather than give the current
clear "unsupported datasource provider" error. `migrate deploy`/`db push` for
MySQL need upstream to implement executeScript (plus the shadow-DB story that
also blocks pg `db push`). Remaining validation: an end-to-end Prisma MySQL
query fixture test with the real adapter (mirrors `TestPrismaPostgres`).

**Addendum — end-to-end query test landed.** `TestPrismaMySQL`
(`internal/frameworks/next/orm_prisma_mysql_test.go`) drives the real
`@prisma/adapter-mariadb` 7.10.0 generated client against go-mysql-server: a new
`prisma/schema-mysql.prisma` + `prisma-mysql.config.ts` + a third `prisma
generate --config prisma-mysql.config.ts` in next-orm's build script, an
`app/api/prisma-mysql` route, and `@prisma/adapter-mariadb` added to the
fixture's package.json. The expectation is byte-identical to
`TestPrismaPostgres` (same Widget model/operations) — Prisma is DB-agnostic, so
the shim must reproduce it: BIGINT `count` coerced to the number 2, TINYINT(1)
`active` to booleans, `_sum` of qty to 10, and an interactive `$transaction`
rolled back. Green. The build logs "Failed to parse syntax of config file …
prisma-mysql.config.ts" once on the cold build (the known brand-new-.ts-config
artifact) and the warm retry generates the client. Not aot-tagged (no PGlite;
go-mysql-server is a plain dep, like `TestORMDriverMySQL`). The fixture's
`generated/` and `node_modules/` are gitignored — `og install` materializes the
adapter + `mariadb`, the build regenerates the client.

## 2026-10-18 - Next CSS preserved through symlinked project roots

- A stock `create-next-app` Tailwind project built from `/tmp/test` on macOS
  completed successfully but emitted no `static/css` chunks and embedded an
  empty `entryCSSFiles` manifest. `/tmp` resolves to `/private/tmp`; esbuild
  canonicalized module paths while the Next loader trees retained the
  symlinked project path, so the exact-string import graph traversal never
  reached `app/globals.css`.
- `next.Build` now resolves the absolute project directory through
  `filepath.EvalSymlinks` before discovering the app and constructing either
  graph. This keeps loader-tree, server, and browser module identities aligned
  for `/tmp`, linked worktrees, and other symlinked project roots.
- Extended `TestBuildFixture` to build through a symlink. Neutralization
  confirmed that the unpatched build completed with an empty CSS manifest; the
  fixed build emits linked CSS entries.
- End-to-end verification used a patched `og` against the stock Next 16.4.2
  Tailwind template through a symlink: three CSS chunks were generated and
  loaded by Chromium, Tailwind layout styles were applied, and the browser had
  no warnings or errors. As documented, `og install` is required first so the
  pure-Go build host has Tailwind's WebAssembly substitutes.

## 2026-10-18 - README Bill of Thanks recovered

- Restored the complete `Bill of Thanks` section immediately before `License`
  in `README.md`. The text was recovered from reflog commit `5532d27` (and is
  byte-identical in the earlier `18b1de3` and `1f52b60` snapshots).
- Kept the current condensed README and its newer license wording intact; only
  the missing acknowledgements section was reintroduced.
- Verification compared the restored section with `5532d27:README.md` and
  checked the documentation tree for decomposed Japanese characters.

## 2026-10-18 - Path-filtered GitHub Actions CI

- Added SHA-pinned `dorny/paths-filter` v4.0.0 to `ci.yml`. PR file-list read
  permission is scoped to the selection job; `release.yml` grants the matching
  permission to its reusable CI call. Branch pushes compare with the pre-push
  commit. Tags and manual runs bypass filtering and retain the full gate.
- `.github/ci-paths.yml` skips lint and tests for documentation-only changes.
  Source, embedded assets, modules, vendored inputs, scripts, licenses, and CI
  configuration select the full gate. Framework source changes retain core
  coverage because the CLI imports both adapters.
- Fixture-only changes select core and the applicable framework shards; WPT
  expectations select core alone. `.github/ci-test-matrix.json` preserves the
  previous six shard definitions exactly. A job condition guards empty matrices.
- Verification: YAML/JSON parsing, `bash -n` for workflow shell snippets,
  16 picomatch path cases with dot matching, and six executions of the actual
  matrix-selection step, including empty selection and forced full execution.
  Neutralizing Vite's shared-runtime filter fails the same dependency assertion.
  The extracted matrix equals the original workflow matrix; `git diff --check`
  and the decomposed-kana scan pass. GitHub-hosted execution remains unverified;
  actionlint is unavailable locally. No Go files changed; Go tests were not run.
- Checked TODO.md: no matching open item or new follow-up.
