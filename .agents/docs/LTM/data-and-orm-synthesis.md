# Data and ORM Synthesis

## Summary

Oganesson supplies process-wide Go-backed database services to request-local
JavaScript VMs, allowing common drivers and ORMs to run without native addons
or Node. SQLite, PostgreSQL, and MySQL compatibility is exposed through package-
shaped JavaScript substitutes; traced files and a writable in-memory overlay
support migrations and file databases. Prisma 7 additionally uses its WebAssembly
query compiler and schema engine over the same driver adapters.

## Included Documents

| Document | Focus |
|----------|-------|
| [databases-drivers-and-orms.md](./databases-drivers-and-orms.md) | Driver shims, native pools, VFS, Drizzle, Kysely, and TypeORM. |
| [prisma.md](./prisma.md) | Prisma generate, query adapters, migrate deploy, and db push. |

## Stable Knowledge

- The current driver architecture uses native Go-backed substitutes registered
  in `runtime/natives`: better-sqlite3 and libsql over modernc SQLite, pg and
  postgres.js over pgx, and mysql2 over go-sql-driver/mysql. Earlier real-JS
  drivers over `node:net`, `node:tls`, and protocol-aware `netpool` remain
  supported infrastructure and historical context, but the native shims are
  the primary path.
- Database services live once per application process and are installed into
  each request engine. Pools remain warm across requests, while borrowed
  connections are tracked per engine and released on engine close.
- SQLite pools are shared by path, with a dedicated connection per JavaScript
  Database object for transaction affinity. `:memory:` stays object-local.
  Overlay-backed SQLite uses a process-unique memdb identity and serializes
  current bytes through the VFS provider.
- PostgreSQL results follow node-postgres conventions: fields carry OIDs,
  int8/numeric default to strings, timestamps become Dates, and bytea becomes
  binary data. Per-query `types.getTypeParser` switches to raw text results and
  applies the requested JavaScript parser.
- PostgreSQL pool creation is single-flight per DSN, does not hold the service
  mutex while dialing, has a connect timeout, and avoids pgx's TLS-prefer probe
  unless SSL was requested. The default query mode caches descriptions rather
  than prepared statements for PGlite compatibility.
- mysql2 supports callback and Promise APIs, array rows, common connection and
  result-shaping options, and verified CA-based TLS. Query streaming and some
  per-query option fidelity remain incomplete.
- Drizzle and Kysely run over all appropriate native shims. TypeORM uses
  TypeScript-Go decorator-metadata transpilation, Node resolution conditions,
  static driver requires, and the traced-file overlay.
- Next file tracing runs through `@vercel/nft` over relevant source modules.
  Packaged files are mounted below an in-memory writable overlay shared by
  request VMs and lost on process exit.
- Prisma 7 generation runs the package's own CLI in `internal/jshost` with a
  placeholder native schema engine and telemetry disabled. The generated client
  uses Prisma's wasm query compiler with better-sqlite3 or pg driver adapters.
- Prisma Migrate runs at application startup through
  `@prisma/schema-engine-wasm`, not a child process. Build-side code packages
  the schema or migration SQL, wasm module, and provider-specific migrator; the
  runtime executes it in a one-off engine sharing the application's VFS and
  native services.
- `migrate deploy` works for SQLite and PostgreSQL. `db push` is wired end to
  end for SQLite. A migration failure prevents startup and closes native
  services.

### Source chronology

The generic database source says Prisma adapter-pg did not yet honor the
query's `types` parser. That statement is superseded by `prisma.md`: the pg
shim now requests text format when `getTypeParser` is present and applies the
per-OID parser in JavaScript. Likewise, earlier Prisma hang diagnoses are
historical; the current resolution is the four-part sequence recorded in the
Prisma source: prerender cleanup, serialized pool creation, describe-cache
query mode, and a complete `pg.types.builtins` map.

## Operational Guidance

- Model package-visible behavior, including defaults and error shapes, not only
  successful SQL execution. ORMs exercise metadata, transaction affinity,
  parsing, teardown, and uncommon options.
- Keep service lifetime separate from engine lifetime. An engine releases its
  borrowed connections; application metadata closes process-wide pools during
  teardown.
- Test integrations with a second request to prove cross-request pool reuse and
  with application teardown to expose leaked sessions.
- When diagnosing a single-session database stall, identify every application
  and `natives.Services` instance first. A prerender app may hold the resource
  needed by the serving app.
- Preserve the traced-file plus overlay model for migration files, content, and
  SQLite. Do not write packaged files to the host filesystem.
- For Prisma, distinguish build-time generation, request-time query execution,
  and startup migration. They use related adapters but different engines and
  lifecycles.

## Files

- `runtime/natives/{sqlite,pg,mysql}.go`, `runtime/natives/js/*` - native
  database services and package-shaped JavaScript APIs.
- `runtime/nodeshim/net.go`, `netpool.go`, `netpool_mysql.go` - socket and
  protocol-pool compatibility layer.
- `runtime/vfs/overlay.go`, `internal/frameworks/next/trace.go` - packaged file
  tracing and writable overlay.
- `_tsoverlay/ogapi/decorators.go`, `internal/frameworks/next/build.go` -
  TypeORM decorator metadata.
- `internal/frameworks/next/codegen.go`, `prismamigrate.go` - Prisma generation
  and migration packaging.
- `runtime/app/prismamigrate.go`, `runtime/app/prerender.go` - startup migration
  and service cleanup.

## Tests

- ORM/driver fixtures include `TestORMsOnSQLite`, `TestORMDriverLibSQL`,
  `TestORMDriverMySQL`, `TestORMDriverPostgres`, and `TestTypeORM`.
- Native service tests include `TestSQLiteInOverlay`, `TestSQLiteDateText`,
  `TestPG`, `TestPostgresJS`, `TestMySQL2`, option-shaping tests, and TLS tests.
- Prisma coverage includes `TestPrisma`, `TestPrismaPostgres`,
  `TestPrismaMigrateDeploy`, `TestPrismaMigrateDeployPostgres`, and
  `TestPrismaDbPush`. PostgreSQL/PGlite tests require `-tags aot` or
  `-tags wazero` as documented by the source files.

## Pitfalls

- TLS requested without a CA currently encrypts without verification. Full pg
  handshake parity still needs a real TLS PostgreSQL test server.
- Era-1 netpool has parked-session starvation and an async write/destroy race;
  MySQL identity mismatch reconnects because COM_CHANGE_USER is unavailable.
- mysql2 query streaming and several fine-grained field/typeCast behaviors are
  incomplete.
- A new TypeScript Prisma config can exhibit a cold-parse failure that clears on
  a warm rerun.
- PostgreSQL schema-engine migration needs multiple sessions unless advisory
  locking is disabled; the one-off startup migrator disables it for PGlite.
- PostgreSQL/MySQL `db push` still needs a multi-session shadow database, and a
  db-push-only build must currently arrange `prisma generate` separately.
