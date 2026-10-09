# Prisma Migrate via the WASM schema engine (feasibility + plan)

Status: investigated 2026-10-14, not yet built. Supersedes the old "out of reach
(no native code)" verdict in TODO.md, which was about the *native binary*.

## The finding

Prisma 7 ships a WebAssembly schema engine. In the installed fixture
(`testdata/apps/next-orm`, prisma 7.10.0):

- `node_modules/prisma/build/schema_engine_bg.wasm` (5.16 MB) is a complete
  wasm-bindgen module. Its 30 exports are the full migrate surface:
  `schemaengine_new`, `schemaengine_applyMigrations`, `schemaengine_schemaPush`,
  `schemaengine_diff`, `schemaengine_reset`, `schemaengine_createMigration`,
  `schemaengine_dbExecute`, `schemaengine_evaluateDataLoss`,
  `schemaengine_diagnoseMigrationHistory`, `schemaengine_markMigrationApplied`,
  `schemaengine_markMigrationRolledBack`, `schemaengine_introspect`,
  `schemaengine_ensureConnectionValidity`, `schemaengine_devDiagnostic`,
  `schemaengine_version`, plus `__wbindgen_*` memory helpers.
- Its 90 imports are **generic wasm-bindgen glue only** (JsValue inspection,
  `__wbg_call`/`apply`, Promise `then`/`resolve`/`queueMicrotask`, `crypto`
  getRandomValues, `setTimeout`, `process`/`versions` probes). There are **no
  database or socket host imports** — the engine cannot open its own connection.
  All DB access is a JS object passed into `schemaengine_new`, whose methods the
  Rust side calls back through the Promise glue: a **driver adapter**.

The migration-aware adapter contract already exists and is what we must implement:
`@prisma/driver-adapter-utils/dist/index.d.ts`:
- `SqlDriverAdapter` (line 320): `executeScript(script)` ("execute multiple SQL
  statements separated by semicolon" — the DDL entry point), `startTransaction`,
  `getConnectionInfo?`, `dispose`, plus `queryRaw`/`executeRaw`.
- `SqlMigrationAwareDriverAdapterFactory` (line 347): `connect()` +
  `connectToShadowDb()` (shadow DB for diffing).
- `@prisma/adapter-pg` and `@prisma/adapter-better-sqlite3` implement it over the
  same Go-backed shims (`runtime/natives`) we already drive the query engine with.

## Why the stock CLI does not get us there

`prisma/build/cli.js` (2.7 MB, minified) dispatches `migrate deploy` and
`db push` through a `SchemaEngine` class whose `internalInit()` (offset ~1615748)
**unconditionally `spawn()`s the native `schema-engine` binary** and talks JSON-RPC
over stdio. `PRISMA_SCHEMA_ENGINE_BINARY` only substitutes a *different native*
binary; there is no flag/env that selects the WASM engine. og has no
`child_process`, so this path is dead for us — unlike `prisma generate`, which
does not use the schema engine and already runs on jshost (`codegen.go:runPrisma`).

Also: `schema_engine_bg.wasm` here is **orphaned** — its glue (`schema_engine_bg.js`)
is not in the install, and `@prisma/schema-engine-wasm` is not a dependency. The
`.wasm` alone is not callable. `grep -rl schema_engine_bg node_modules` → nothing.

## Plan (proposed; scope/placement TBD by the user)

Not a CLI wrapper — a from-scratch in-process integration:

1. **Bring the glue + wasm.** Vendor `@prisma/schema-engine-wasm` (ships
   `schema_engine.js` / `schema_engine_bg.js` wasm-bindgen glue + the `.wasm`),
   pinned, via `scripts/vendor-js.sh` (or add it as a fixture dep for the test).
   Confirm its version matches `@prisma/engines-version` / the client.

2. **Instantiate in-process.** Load the module through the existing wazero-backed
   `WebAssembly` global that `prisma generate`'s `prisma_schema_build_bg.wasm`
   already uses (`runtime/wasm`, `wasm.Install`). The glue JS runs in Sobek; its
   imports are JS, so no new Go host imports are needed for the wasm itself.

3. **Migration-aware driver adapter over the Go backends.** Implement
   `SqlMigrationAwareDriverAdapterFactory` (`connect`, `connectToShadowDb`) +
   `SqlDriverAdapter` (`executeScript`, `queryRaw`, `executeRaw`,
   `startTransaction`, `getConnectionInfo`, `dispose`) on `Symbol.for("og.pg")` /
   `Symbol.for("og.sqlite")`. Most methods already exist on the query-side shims;
   `executeScript` (split-on-`;` multi-statement DDL) and `connectToShadowDb`
   (a throwaway database/schema) are the new pieces. For single-session PGlite,
   the shadow DB is the hard part — may need a second PGlite or a schema-scoped
   shadow; deploy-only avoids it.

4. **Orchestration.** Re-implement the thin CLI logic:
   - `migrate deploy`: read `prisma/migrations/migration_lock.toml` + each
     `prisma/migrations/<name>/` into the `migrationsList` shape (cli.js `Bl`,
     offset ~1619655), then `schemaengine_applyMigrations({migrationsList, filters})`.
     `_prisma_migrations` bookkeeping is inside the wasm engine and runs through
     the adapter's SQL callbacks — no native DB access.
   - `db push`: `schemaengine_schemaPush({force, schema, filters})` (schema diff
     against the live DB; uses the shadow DB).

5. **Placement (decide):** app-startup runtime migrator (like Drizzle's) against
   the runtime datasource / an `og` subcommand / build-time. Affects the test
   shape.

6. **Test.** Extend `testdata/apps/next-orm`: a `prisma/migrations/**` for the pg
   schema, drive `migrate deploy` through the new path, assert the tables/rows
   match `next start` + real `prisma migrate deploy`. `-tags aot` (PGlite).

## Locked decisions (2026-10-14, user) + execution plan for `migrate deploy`

Placement: **app startup** (apply pending migrations when the packaged binary boots,
before serving). Scope: **`migrate deploy`** first (no shadow DB); `db push` later.

Decisions:
- **Datasource**: evaluate the project's `prisma.config.ts` at build to get `schema`
  + `datasource.url`. sqlite's config has a static `datasource.url` (self-contained —
  first target). The pg/adapter case has no url in config (adapter supplies it at
  runtime) → needs a fallback (resolve a runtime env var; follow-up).
- **Schema engine**: og **implicitly injects** `@prisma/schema-engine-wasm` pinned to
  the matching `@prisma/engines-version` when it detects Prisma — the user does not add
  it. Bundle its wasm+glue into the app package only when migrate is used.

Execution plan (file by file):
1. [DONE 2026-10-14] `internal/install/install.go`: `injectPrismaSchemaEngine` adds
   `@prisma/schema-engine-wasm` to the root deps when the project depends on
   `@prisma/client`, pinned to the exact `@prisma/engines-version` that `@prisma/client`
   resolves to (it is published per-engine-commit as a prerelease a caret would skip;
   `prisma` itself does not list engines-version, `@prisma/client` does). Resolve path
   only (a lockfile is authoritative). Tests `TestPrismaSchemaEngineInjected` /
   `...NotInjected` (fakeRegistry), neutralization-checked.
2. [DONE 2026-10-14] `internal/frameworks/next/prismamigrate.go`: `(*builder).prismaMigrate`
   detects `prisma/migrations/*/migration.sql`, evaluates `prisma.config.ts` for schema +
   `datasource.url`, reads the provider from the schema, packages the migration SQL +
   `migration_lock.toml` + `schema_engine_bg.wasm` + a bundled migrator program into
   `Output.Files`, and sets `out.PrismaMigrate` → `meta.PrismaMigrate` (new field in
   `runtime/app/app.go`). The migrator entry (`prismaMigratorEntry`) bundles over the
   provider's adapter and assigns a promise to `globalThis.__ogMigrateDone`. Wired into
   `finish()`. Tests: `TestPrismaProvider`, `TestPrismaMigrationDirs`, `TestLoadPrismaConfig`
   (evaluates the fixture's config), `TestBundleMigrator` (bundles over the fixture's
   adapters); `TestTracedFiles` unaffected (nil for non-migrate apps). The full
   `prismaMigrate()` orchestration + runtime are validated end-to-end by step 4.

   (original note) `internal/frameworks/next` (build): evaluate `prisma.config.ts` on jshost by importing
   it and reading the default export (mirror `LoadNext` in `internal/jshost/nextconfig.go`
   — `EvalWith` a small entry that stashes `cfg.schema` + `cfg.datasource?.url`); read the
   `provider` from the `.prisma` schema file (`datasource … provider = "<x>"`, a simple
   regex). Detect `prisma/migrations/**`. If present, add to `Output.Files`: the migration
   SQL + `migration_lock.toml`, `schema_engine_bg.wasm` (from
   `node_modules/@prisma/schema-engine-wasm/`), and a bundled **migrator program**
   (og-authored entry, bundled via `jshost.Host.Bundle` resolving the app's
   `@prisma/adapter-*` + `@prisma/driver-adapter-utils` + the schema-engine glue). Set a
   new `app.Meta.PrismaMigrate` field `{migrationsDir, provider, url|urlEnv, program, wasm}`.
   Note: sqlite's `prisma.config.ts` has a static `datasource.url`; the pg case has none
   (adapter supplies it) → `urlEnv` fallback, a follow-up.
3. [DONE 2026-10-14] `runtime/app/prismamigrate.go` + `app.go`: `runPrismaMigrate` runs the
   bundled migrator in a one-off engine (shared `webapi`+exts, so same overlay DB), awaits
   `__ogMigrateDone`, before serving. Also fixed a cross-service memdb DSN collision
   (`runtime/natives/sqlite.go`: per-service id) the prerender-vs-serve path exposed as
   `SQLITE_BUSY`.
4. [DONE 2026-10-14] `TestPrismaMigrateDeploy` (sqlite) green + neutralized; fixture
   `testdata/apps/next-prisma-migrate` (pre-staged in git, node_modules symlinked to next-orm).
5. [DONE 2026-10-14] pg `urlEnv` datasource: `loadPrismaConfig` captures `datasource.url:
   env("VAR")` via a process.env Proxy during a deferred dynamic import (sentinels every read,
   so it works even with the var set at build for `prisma generate`); `meta.PrismaMigrate.urlEnv`
   resolved at startup. Tested (`TestLoadPrismaConfigEnv`). Migrator surfaces the real adapter
   error behind the engine's "External error id#N" via `errorRegistry.consumeError`.
6. [BLOCKED] pg runtime e2e: single-session PGlite can't serve the pg schema engine's migrate
   (it holds a pg_advisory_lock on one connection while migrating on another → needs >= 2;
   default pool hangs, max:1 deadlocks). `TestPrismaMigrateDeployPostgres` is skipped as
   scaffolding (fixture `next-prisma-migrate-pg`); needs a multi-session Postgres test server.
   Also: `db push` (shadow DB; sqlite's `:memory:` shadow worked in the M3 spike, so a sqlite
   `db push` mode is feasible but needs an opt-in since "no migrations dir" is ambiguous), and
   the startup migrator honoring its timeout on a connection hang.

   (original) `runtime/app/app.go`: in `handler()`, if `meta.PrismaMigrate` is set, run a one-off
   `engine.New` with the natives + `wasm.Install` extensions, load the migrator program,
   and `await` it before returning the handler. It reads migrations + wasm from the
   packaged FS (vfs), builds the adapter from the datasource, and calls
   `applyMigrations`. Reuse the proven entry logic from
   `spikes/prisma-migrate/spike-applymigrations.mjs`.
4. Test `TestPrismaMigrateDeploy` (sqlite, default path first): fixture with
   `prisma/migrations/**`; packaged app applies them at startup; a route reads the
   migrated tables and matches `next start`. Neutralize (break the migrator → test fails).

The migrator entry logic (instantiate wasm → bind adapter → build `MigrationList` from
the packaged migrations dir → `applyMigrations`) is fully validated in the spike (M1–M4);
step 3 lifts it verbatim.

## Spike result (2026-10-14) — feasibility PROVEN

A throwaway spike (`spikes/prisma-migrate/`, see `RESULT.md`) drove the WASM engine
on jshost end-to-end against the better-sqlite3 Go shim:
- M1: `schema_engine_bg.wasm` instantiates **synchronously** under og's wazero-backed
  `WebAssembly` with its 90 wasm-bindgen JS imports; `version()` → matching commit.
- M2: `SchemaEngine.new(opts, logCb, bindMigrationAwareSqlAdapterFactory(new
  PrismaBetterSqlite3({url})))`; `ensureConnectionValidity` + `version()` → `3.53.4`
  (a real query **through the adapter → Go sqlite → wasm**).
- M3: `schemaPush` (db push) → `executedSteps:1`, Widget table created through the
  adapter; shadow DB (`:memory:`) diffing works.

So: wasm-under-og, JS-function imports, and the adapter write/DDL path are no longer
unknowns — they work. Remaining work is orchestration, not feasibility.

## Risks / unknowns (updated)

- RESOLVED: wasm runs under og's WebAssembly (sync Module/Instance + JS imports).
- RESOLVED: the engine drives DDL through a JS driver adapter over the Go backend.
- OPEN: Shadow DB on single-session PGlite (`db push`, `evaluateDataLoss`) — the
  engine wants a separate connectable DB. `migrate deploy` avoids it; it is the
  safer first target. (sqlite's shadow is `:memory:` and already works.)
- Pin the glue tightly: the wasm ABI is wasm-bindgen-internal and unstable across
  versions. Use exactly `@prisma/schema-engine-wasm@<@prisma/engines-version>`.
- Prisma 7 forbids `url` in the schema `datasource` block when an adapter is used;
  connection info comes from the adapter / method `datasource` params.
