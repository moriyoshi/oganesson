# Prisma 7 Support

## Summary

Oganesson runs Prisma 7 with no native engine: `prisma generate` runs on the
build host through its own CLI, and the generated client executes at request
time over a Go-backed driver adapter (`@prisma/adapter-better-sqlite3` or
`@prisma/adapter-pg`) using Prisma's WebAssembly query compiler. Prisma Migrate
(`migrate deploy` and `db push`) also works at app startup via Prisma's
WebAssembly schema engine, driven over the same adapters. The generic native
SQL driver layer these adapters sit on is [[databases-drivers-and-orms]].

## Key Facts

- Prisma 7.10.0 (`prisma`, `@prisma/client`, `@prisma/adapter-better-sqlite3`,
  `@prisma/adapter-pg`). npm `latest` pointed at an 8.0 RC, `prev` at 7.10.0.
- Prisma 7's client is TypeScript with a WebAssembly query compiler over a
  driver adapter: no native engine at run time.
- `prisma generate` runs on the build host (`internal/frameworks/next/codegen.go`)
  via the package's own bin, using `jshost.Host.RunProgram`. `CHECKPOINT_DISABLE=1`
  suppresses telemetry; `PRISMA_SCHEMA_ENGINE_BINARY` points at a placeholder
  (generate does not use the native schema engine).
- Query-engine tests: `TestPrisma` (sqlite), `TestPrismaPostgres` (`-tags aot`,
  adapter-pg on PGlite). Migrate tests: `TestPrismaMigrateDeploy` (sqlite),
  `TestPrismaMigrateDeployPostgres` (pg), `TestPrismaDbPush` (sqlite).
- Prisma 7 rejects `url`/`datasource.url` in the schema when an adapter is used;
  the connection comes from the adapter. A per-schema `*.config.ts` invoked via
  `prisma generate --config` is how multiple schemas coexist.
- The WASM schema engine is `@prisma/schema-engine-wasm`, injected implicitly by
  `og install` (keyed off `@prisma/client`, pinned to `@prisma/engines-version`).

## Details

### `prisma generate` on the build host

Before compiling, og runs the code generators that the `postinstall` and build
scripts run and that it recognizes — `prisma generate` through the package's own
bin (`internal/frameworks/next/codegen.go`). Other commands are skipped, as Vite
builds skip them. `jshost.Host.RunProgram` (`program.go`) implements
`node file args...` on the build host: `process.argv`, `process.exit(code)`, and,
as Node, ending the run when the loop goes idle (`process.exitCode`).

Prisma's CLI asks for a native schema engine before any command; `generate` does
not use it, so `PRISMA_SCHEMA_ENGINE_BINARY` points at a placeholder that cannot
run. `CHECKPOINT_DISABLE=1` keeps the CLI off its telemetry request. The Node
surface the CLI needed (all now Node-faithful, `TestNodeSurfaceForCLIs`):

- a hashbang line in a module loaded at run time kept as a comment (esbuild had
  left it first inside the CommonJS wrapper — a syntax error);
- `process` as an EventEmitter (addListener/emit/listeners/...), `process.execPath`/
  `argv0` (presented as a Linux install path — og presents itself as Linux),
  `process.pid`/`ppid` (the og process's), `process.binding("constants"/"buffer")`;
- `Module._nodeModulePaths`, `vm.runInThisContext`, `vm.Script` (new `js/vm.js`
  indirect eval; jiti evaluates `prisma.config.ts` with it), `util.types`
  predicates unenv left unimplemented, `http.validateHeaderName`/`validateHeaderValue`.

Multiple schemas: a brand-new `.ts` config hits a known cold-parse artifact
("Failed to parse syntax of config file") on its first build; a warm re-run
clears it. A separate `prisma-pg.config.ts` (schema only, no `url`), invoked via
`prisma generate --config prisma-pg.config.ts`, avoids P1012 ("datasource
property `url` no longer supported") that occurred when the default sqlite
config's `datasource.url` was applied to the pg schema.

### The generated client at run time

- `import.meta.url`/`dirname`/`filename` in the Next server bundle are set to the
  module file's (`jshost.ReplaceImportMeta`, as Next's bundlers write them); the
  generated client reads `__dirname` from it.
- Next's compiled `buffer` predates `base64url` (used by Prisma's query plans);
  patched as og's unenv buffer is. The override reaches that file outside og's
  layer namespaces, so source patches also apply to plain files (`sourcePatchFilter`).

### better-sqlite3 adapter (the Go shim)

- `statement.columns()` must not run the statement — Prisma's adapter asks an
  `INSERT ... RETURNING` for columns first, which previously inserted every row
  twice. Column info now comes from the prepared statement (modernc's
  `ColumnInfo`): column, table, database, and declared type (`TestSQLiteColumns`).
- Errors carry better-sqlite3's code (SQLite's extended result-code name, e.g.
  `SQLITE_CONSTRAINT_UNIQUE` which Prisma maps to P2002 — it was `SQLITE_CONSTRAINT`
  for every constraint) and SQLite's own message, "errstr: errmsg (rc)"
  (`TestSQLiteErrors`).

### The adapter-pg hang: four stacked root causes

`TestPrismaPostgres` (`-tags aot`) exercises adapter-pg on single-session PGlite.
Over 2026-10-13 several wrong leads were caught in transient goroutine dumps (a
PGlite pool guess, a PrismaPromise/ContextualThenables thenable spin, a
TypeScript-Go parser spin in `QuoteKeywordClassFields`, a pgx `startTLS` stall
under `s.mu`, and an upstream pglite-go SSL-under-lock bug filed as #7 — a real pglite-go bug,
not the hang's root cause: the Postgres-via-PGlite test server was bumped to
pglite-go v0.2.3 (PR #8 negotiates SSL/startup before taking the single-backend
lock, fixing #7) and that bump is kept); the
actual resolution is four independent bugs, each masked by the one before it:

1. **`app.Prerender` leaked a warm pg connection.** `serveORMFixture` runs
   `app.Prerender` then `app.Handler` — two apps, each with its own
   `natives.NewServices()`. Prerendering a `force-dynamic` route opens a pg
   connection; `Prerender` never called `meta.Close`, so it held PGlite's single
   backend session, and the serving app's connection timed out
   (`engine: interrupted`, 500). Fix: `Prerender` defers `meta.Close()`
   (`runtime/app/prerender.go`). (In real `og build`, prerender and serve are
   separate processes, so this is masked — but leaking build-time pools is wrong.)
2. **Concurrent pool creation raced.** Two `Postgres.get(dsn)` for a not-yet-created
   pool both dialled; the second connection blocked on PGlite's one backend. Fix:
   serialize pool creation per DSN with `golang.org/x/sync/singleflight`
   (`sf.Do(dsn, …)`), without holding `s.mu` across the dial; added a 30 s
   `ConnectTimeout` and dropped pgx's SSLRequest probe unless the DSN names `sslmode`.
3. **pgx statement cache vs PGlite rollback.** The route's rolled-back
   `$transaction` produced `prepared statement "stmtcache_…" already exists`:
   pgx's default `QueryExecModeCacheStatement` caches auto-named prepared
   statements per connection, but PGlite drops prepared statements on rollback, so
   pgx re-Parses a name the server still thinks exists. Fix:
   `cfg.ConnConfig.DefaultQueryExecMode = QueryExecModeCacheDescribe` — executes
   through the unnamed statement (no name to collide) while still caching the
   describe for binary decoding (keeps int4-as-number fidelity). `QueryExecModeExec`
   was tried first but falls back to text format and mis-types results.
4. **Empty `pg.types.builtins` in the shim.** `Failed to deserialize column of
   type 'int4'` even though the shim returned `int32(1)` with OID 23.
   `@prisma/adapter-pg` does `const { builtins } = require("pg").types` and
   `switch`es on `builtins.INT4`; with `builtins === {}` every case was
   `case undefined`, falling to the default `throw new UnsupportedNativeDataType(23)`
   where `typeNames[23] === "int4"`. Fix: `js/pg.js` defines the full `builtins`
   OID map (pg-types `lib/builtins.js` verbatim).

The route keeps `new PrismaPg({ …, max: 1 })`, which bounds the Go pool to one
connection (`MaxConns=1`). An earlier JS-side `max:1 ⇒ _single shared connection`
path in `js/pg.js` proved unnecessary once the four causes were fixed (the route's
queries are sequential) and was removed. The pool size is not what made the test
pass — the four fixes are.

### Prisma Migrate (migrate deploy + db push) at app startup

The old TODO verdict — Migrate "needs the native schema engine: out of reach" —
was about the native binary and is retired. Prisma 7 ships
`prisma/build/schema_engine_bg.wasm` (~5.2 MB), a complete wasm-bindgen module
whose 30 exports are the whole migrate surface (`schemaengine_applyMigrations`,
`schemaengine_schemaPush`, `schemaengine_diff`, `schemaengine_reset`,
`schemaengine_ensureConnectionValidity`, …) and whose 90 imports are generic
wasm-bindgen glue only — no DB/socket host imports. The engine gets all DB access
from a JS object passed to `schemaengine_new`: a migration-aware driver adapter
(`SqlMigrationAwareDriverAdapterFactory` with `executeScript`/`connectToShadowDb`,
`@prisma/driver-adapter-utils`), implemented by adapter-pg / adapter-better-sqlite3
over the same Go shims. (The stock `cli.js` `SchemaEngine.internalInit()` instead
`spawn()`s a native binary over JSON-RPC, which og has no child_process for — so
migrate is a from-scratch in-process integration, not a CLI wrapper.)

The spike proved: `new WebAssembly.Module(bytes)` + `new WebAssembly.Instance(mod,
{"./schema_engine_bg.js": glue})` instantiates the wasm **synchronously** under
og's wazero-backed `WebAssembly` global (see [[wasm-on-wazero]]); the `_bg.js`
subpath does not import the `.wasm`, so esbuild never handles a wasm ESM import.
`SchemaEngine.new(options, logCb, bindMigrationAwareSqlAdapterFactory(adapter))`
constructs, `ensureConnectionValidity` runs a query through the adapter, and
`applyMigrations`/`schemaPush` execute DDL and write `_prisma_migrations`
bookkeeping through the adapter's SQL callbacks (idempotent on a second run).

Integration (landed in steps):

- **Implicit dep injection** (`internal/install/install.go`): adds
  `@prisma/schema-engine-wasm` to a Prisma project's dependency set, keyed off
  `@prisma/client`, pinned to the exact `@prisma/engines-version` `@prisma/client`
  depends on (schema-engine-wasm is published once per engine commit as a
  prerelease `7.10.0-4.<hash>` that a caret range would not select; `prisma` itself
  does not list engines-version while `@prisma/client` does). Resolve-path only;
  a lockfile is treated as authoritative and left unamended. Best-effort — skips
  silently on non-Prisma projects, explicit declarations, or resolution misses.
- **Build-side packaging** (`internal/frameworks/next/prismamigrate.go`):
  `(*builder).prismaMigrate` detects migrations (`prisma/migrations/*/migration.sql`)
  or an explicit `prisma db push` in the build script, evaluates `prisma.config.ts`
  on the build host (mirroring `LoadNext`: bundle the import + TLA entry, then
  `Eval` the stashed JSON) for the schema path and static `datasource.url`, reads
  the `provider` from the `.prisma` schema (regex over `datasource … provider`),
  and packages into `Output.Files`: the migration SQL + `migration_lock.toml` (or
  the `.prisma` schema, for push), `schema_engine_bg.wasm`, and a bundled migrator
  program. The migrator entry (`prismaMigratorEntry`) is bundled over the provider's
  adapter + schema-engine glue + `driver-adapter-utils` and assigns a promise to
  `globalThis.__ogMigrateDone` (no top-level await, so it bundles as a plain script
  that RunScripts then awaits). `app.Meta.PrismaMigrate` carries
  `{dir, provider, url|urlEnv, program, wasm, push, acceptDataLoss, schema}`.
- **Runtime boot-time migrator** (`runtime/app/prismamigrate.go`, wired in `app.go`):
  when `meta.PrismaMigrate` is set, `handler()` runs `runPrismaMigrate` before
  building the request handler. It spins a one-off `engine.New` with the SAME
  `webapi` + extension set (natives, wasm, vfs over the shared overlay) the request
  engines get, so the migrator drives the very database the server then reads. It
  sets `globalThis.__ogPrismaMigrate = {dir, url, wasm}`, RunScripts the bundled
  migrator, and `e.Await`s `__ogMigrateDone`. The datasource URL is
  `meta.PrismaMigrate.URL` (sqlite's static config url) or `env[URLEnv]`. A failed
  migration fails startup — and now calls `meta.Close()` on the failure path so a
  leaked connection does not deadlock teardown.

**db push** (`TestPrismaDbPush`, sqlite): the opt-in is the build-script command
itself (`prisma db push [--accept-data-loss]`); codegen detects it, records
`prismaPush` on the builder, and skips running it (no datasource at build). An
explicit `prisma db push` takes precedence over a migrations directory. Push
packages the `.prisma` schema (schemaPush diffs schema against the live DB — no
migration files); `--accept-data-loss` maps to schemaPush's `force`. The migrator
branches on `cfg.push`: `engine.schemaPush({force, schema, filters})` vs
`applyMigrations({migrationsList, filters})`, returning `{applied}` for deploy or
`{push, executedSteps, warnings, unexecutable}` for push.

**PostgreSQL migrate deploy** (`TestPrismaMigrateDeployPostgres`): extra pieces
beyond sqlite, all landed and green end-to-end on single-session PGlite:

- **env datasource capture.** The pg/adapter config has no literal url, expressing
  it as `datasource: { url: env("DATABASE_URL") }`. Prisma's `env(name)` resolves
  `process.env[name]` eagerly, so a passive eval can't recover the variable name.
  `loadPrismaConfig` installs a `process.env` Proxy returning a sentinel
  (`__OG_ENV__<KEY>__`) for every read, then `import()`s the config so the Proxy is
  active during eval; a url that comes out as a sentinel is recorded as
  `meta.PrismaMigrate.urlEnv`. (It sentinels every read because the build sets
  DATABASE_URL for `prisma generate`, which loads the config itself with the real
  environment, so its own `env()` is unaffected.) `runPrismaMigrate` resolves
  `env[URLEnv]` at startup.
- **Advisory lock disabled.** The pg schema engine holds a `pg_advisory_lock` on a
  second connection while migrating on the first — single-session PGlite can't
  grant it (nondeterministic hang; `max:1` deadlocks). `runPrismaMigrate` sets
  `PRISMA_SCHEMA_DISABLE_ADVISORY_LOCK` in the migrator engine's env (the var is
  presence-checked, so the value "false" disables it). The one-off startup migrator
  is the only writer, so the lock is unneeded. The pg migrator uses the default pool
  (a real Postgres gives the engine the connections it wants; `max:1` would deadlock
  there too).
- **Array parameters** (`runtime/natives/pg.go` `toGo`): a JS array parameter fell
  through to `v.String()`, so the schema list `text[]` `["public"]` became the bare
  string `"public"` → Postgres "malformed array literal". Added a `[]any` case
  returning the Go slice, which pgx array-encodes using the describe's parameter OID.
- **Honor the query's `getTypeParser`** (the general fidelity upgrade). node-postgres
  applies per-OID parsers from the query's `types.getTypeParser` to each column's raw
  text; og's shim had always Go-decoded, giving the schema engine a Date for
  `_prisma_migrations`' timestamptz where it wanted a string ("expected a string in
  column 'finished_at'"). Now: when a query passes `types.getTypeParser`, the shim
  fetches in the **text** format (pgx `QueryResultFormats{TextFormatCode}`, read via
  `rows.RawValues()`), returns raw text per column, and `pg.js` applies the query's
  parser — node-postgres' model. A query with no `types` keeps the Go-decoded (jsVal)
  path, so plain consumers are unaffected. Default text parsers were also implemented
  in `js/pg.js` (bool `t`→true, int2/4/oid→parseInt, float4/8→parseFloat,
  json/jsonb→JSON.parse, bytea `\x`→Buffer; int8/numeric stay strings; date/timestamp
  best-effort Dates) because adapter-pg and Drizzle fall back to the default parser
  for OIDs they don't override.
- **Error surfacing.** The migrator folds the real driver-adapter error (bound
  adapter's `errorRegistry.consumeError(id)`) into the thrown error, so the engine's
  opaque "External error id#N" shows its cause.

**sqlite memdb collision** (the hard bug behind `TestPrismaMigrateDeploy`): the
migrator's adapter hit `SQLITE_BUSY` opening `migrate.db`. og.sqlite's memdb DSN
was `file:/og/<gen>/<name>` with `gen` restarting per SQLite-service instance, and
modernc's memdb VFS keys its databases by that DSN path PROCESS-WIDE. The build-time
prerender app (a separate `natives.Services`, never closed for SQLite — `meta.Close`
only closed Postgres/MySQL) opened `migrate.db` as gen 1 and held its keeper
connection; the serving app's migrator also assigned gen 1 → the same global memdb →
locked out. Fix (`runtime/natives/sqlite.go`): a process-global `sqliteServiceSeq`
gives each `SQLite` service a unique id, so the DSN is
`file:/og/<serviceID>/<gen>/<name>?vfs=memdb` and two services never collide.

## Files

- `internal/frameworks/next/codegen.go` — runs `prisma generate` (and detects
  `prisma db push`) on the build host via the package bin.
- `internal/jshost/program.go` — `jshost.Host.RunProgram` (`node file args...`).
- `internal/frameworks/next/prismamigrate.go` — build-side migrate packaging:
  `(*builder).prismaMigrate`, `loadPrismaConfig` (incl. env-sentinel Proxy),
  `prismaMigratorEntry`, provider/migration-dir detection.
- `internal/install/install.go` — implicit `@prisma/schema-engine-wasm` injection.
- `runtime/app/prismamigrate.go` + `runtime/app/app.go` — boot-time migrator
  (`runPrismaMigrate`); migrate-failure `meta.Close()` cleanup.
- `runtime/app/prerender.go` — defers `meta.Close()` (prerender pool-leak fix).
- `runtime/natives/pg.go` — adapter-pg backend: singleflight pool creation,
  `QueryExecModeCacheDescribe`, ConnectTimeout, `[]any` array params, raw/text
  result mode for `getTypeParser`.
- `runtime/natives/js/pg.js` — `types.builtins` OID map, `getTypeParser` honoring,
  default text parsers.
- `runtime/natives/sqlite.go` — per-service memdb DSN id (`sqliteServiceSeq`);
  better-sqlite3 `ColumnInfo`, extended result-code error names.
- `testdata/apps/next-orm` — Prisma fixture (sqlite + pg schemas, `/api/prisma`,
  `/api/prisma-pg`).
- `testdata/apps/next-prisma-migrate`, `next-prisma-migrate-pg`, `next-prisma-push`
  — migrate/push fixtures.

## Test Coverage

- `TestPrisma` (sqlite query engine) — regenerates the client, serves, matches
  `next start` JSON. Neutralized by removing codegen / the base64url patch / the
  import.meta replacement.
- `TestPrismaPostgres` (`-tags aot`) — adapter-pg on PGlite; was long skipped
  through the hang investigation, now green with the four fixes.
- `TestPrismaMigrateDeploy` (sqlite) — build packages migrations + wasm + migrator;
  startup applies deploy; `/api/widgets` reads the migrated table. Neutralized by
  disabling the startup migrator (route 500s "table main.Widget does not exist").
- `TestPrismaMigrateDeployPostgres` (pg) — green end-to-end on single-session PGlite.
- `TestPrismaDbPush` (sqlite) — startup schemaPush, `/api/widgets` insert+read.
- Build/unit: `TestPrismaProvider`, `TestPrismaMigrationDirs`, `TestLoadPrismaConfig`
  (sqlite literal url), `TestLoadPrismaConfigEnv` (pg env() name capture),
  `TestBundleMigrator`, `TestPrismaSchemaEngineInjected` /
  `TestPrismaSchemaEngineNotInjected` (install injection), `TestSQLiteColumns`,
  `TestSQLiteErrors`, `TestNodeSurfaceForCLIs`, `TestRunProgram`.
- Non-regression for the driver shims: `TestORMDriverPostgres`, `TestORMDriverMySQL`,
  `TestORMDriverLibSQL`, `TestORMsOnSQLite`, `TestPG`, `TestPostgresJS` (see
  [[databases-drivers-and-orms]]).
- Fixture tests skip when `node_modules` is absent (install with
  `go run ./cmd/og install <dir>`). The migrate fixtures share next-orm's install
  via a committed `node_modules` symlink.

## Pitfalls

- A connection-layer symptom (stall, timeout) can be an artifact of a *different*
  app instance (prerender) holding the one DB resource. Check how many
  `natives.Services` exist (`%p` on the service pointer) before blaming the shim's
  own pooling.
- npm refuses `@prisma/adapter-better-sqlite3` with better-sqlite3 13 (its range is
  `^12`); the reference install used `--legacy-peer-deps`.
- Prisma's CLI warns it cannot detect OpenSSL (og presents Linux); harmless for
  `generate`.
- A brand-new `.ts` Prisma config hits a cold-parse artifact on its first build; a
  warm re-run clears it.
- The pg schema engine needs >= 2 concurrent connections (advisory lock on one while
  migrating on another) unless `PRISMA_SCHEMA_DISABLE_ADVISORY_LOCK` is set; the
  query engine works on single-session PGlite because it serializes one connection.
- The startup migrator's 2-minute context does not abort a pg connection hang (the
  block is in pgx's connect) — harmless against a working Postgres, but a real hang
  would stall startup.

## Remaining Prisma follow-ups

- `@prisma/adapter-mariadb` **query** path now works (2026-10-18): the `mariadb`
  npm driver is substituted by a built-in shim (`runtime/natives/js/mariadb.js`)
  over the shared Go MySQL service, as `pg` is for adapter-pg, so the real
  adapter runs unchanged. See the JOURNAL entry of that date. Its **migrate**
  path is still blocked upstream: adapter-mariadb 7.10.x stubs `executeScript`
  ("Not implemented yet") and its factory has no `connectToShadowDb`, so
  `prismaAdapters` has no mysql entry and `migrate deploy`/`db push` report
  "unsupported datasource provider". Covered end to end by `TestPrismaMySQL`
  (`internal/frameworks/next`, real adapter client over go-mysql-server, want
  byte-identical to `TestPrismaPostgres`). Remaining: upstream executeScript (+
  the shadow-DB story) for migrate.
- pg/mysql `db push` needs a multi-session shadow DB (schemaPush uses a shadow DB);
  single-session PGlite can't provide one. Only sqlite db push is wired end-to-end.
- Implicit `prisma generate` for db-push-only builds: real `prisma db push` also
  regenerates the client, but og skips the command, so a db-push project must run
  `prisma generate` itself in its build script. og could run generate when it sees
  `db push` with no separate generate.
