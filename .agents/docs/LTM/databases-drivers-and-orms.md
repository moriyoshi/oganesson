# Databases, Drivers, and ORMs (M10)

## Summary

M10 makes Oganesson run real database drivers and ORMs unchanged. Below the
ORMs sit two generations of plumbing: first Go-native `node:net` / `node:tls`
shims plus a protocol-level connection pool shared across request VMs
(`netpool`), over which the real `pg`, `postgres` (postgres.js) and `mysql2` JS
drivers connected; then native Go-backed driver substitutes (registered in
`runtime/natives` like `better-sqlite3`) that replace those JS drivers with
pgx / go-sql-driver backends, bypassing the socket layer entirely. SQLite needs
no sockets at all. The ORMs verified unchanged are Drizzle, Kysely and TypeORM
(Prisma has its own doc, [[prisma]]).

## Key Facts

- **Driver support matrix:** SQLite (`better-sqlite3`, `@libsql/client`),
  PostgreSQL (`pg`, `postgres`), MySQL (`mysql2`), all as native Go-backed
  substitutes in `runtime/natives`. No Node, no native addons.
- **ORMs verified:** Drizzle 0.45.3, Kysely 0.29.6, TypeORM 1.1.2 (over
  reflect-metadata 0.2.2). Prisma: separate doc [[prisma]].
- **Two eras of PG/MySQL support.** Era 1: real JS drivers over og's
  `node:net` / `node:tls` Go shims + the protocol-aware `netpool` (pooled across
  request VMs). Era 2 (current): native Go-backed shims (`pg.go`, `mysql.go`)
  over `jackc/pgx/v5`+pgxpool and `go-sql-driver/mysql`, process-wide warm
  pools. postgres.js reuses pg's Go service with no new Go code.
- **Test fixture:** `testdata/apps/next-orm` (Next 16.3.8 App Router) drives
  every driver and ORM from dynamic pages, route handlers and server actions.
- **Test servers (no Docker):** go-mysql-server v0.20.0 (in-memory, test-only)
  for MySQL; the user's `moriyoshi/pglite-go` (pure-Go PostgreSQL 18) for
  Postgres. Postgres tests need `-tags aot` (or `-tags wazero`).
- **Deps:** `github.com/jackc/pgx/v5 v5.11.0`,
  `github.com/go-sql-driver/mysql v1.10.1`, both direct.
- **Runtime filesystem:** a writable in-memory overlay (`runtime/vfs`) over
  Next-traced project files, so request-time reads (Drizzle migrations, content
  files) and SQLite file writes work in a packaged binary.

## Details

### SQLite (no sockets)

Drizzle (`drizzle-orm/better-sqlite3`) and Kysely (`SqliteDialect`) run over
og's `better-sqlite3` substitute (`runtime/natives`) unchanged: the relational
query API, SQL-like builder, `count()`, `RETURNING`, boolean/timestamp modes,
transactions and rollbacks, function columns and aggregates. `require()` of a
substitute now gets a CommonJS wrapper whose `module.exports` is the default
export with named exports attached (real packages export this way; a bare ES
namespace gave "Value is not a constructor").

The better-sqlite3 substitute's API contract: one pool per file per process
(shared by every request engine), each JS `Database` holding its own connection
(so its transactions stay on it), and a `:memory:` database belonging to its own
object. Statements support `prepare` / `run` / `get` / `all` / `iterate` with
`pluck` / `raw` / `expand` / `safeIntegers`; databases support `exec`, `pragma`,
and `transaction` (`deferred` / `immediate` / `exclusive`). Errors are
`SqliteError` with `code`. `statement.reader` is inferred from the SQL (database/sql
cannot report it without running the statement). Unsupported (throws):
`function` / `aggregate` / `table` / `backup` / `serialize` / `loadExtension`.

`@libsql/client` is a JS-only substitute (`runtime/natives/js/libsql-client.js`)
over the same SQLite service (`Symbol.for("og.sqlite")`), so
`drizzle-orm/libsql` runs against a local file. `createClient({url})` parses
`file:` / `:memory:` and rejects remote `libsql://` / `http` / `ws` with
`URL_SCHEME_NOT_SUPPORTED`. The key subtlety is its dual-shape Row:
enumerable column-name keys (`Object.keys` yields names) plus non-enumerable
numeric indices and `length` (so `Array.prototype.slice.call(row)` works), which
drizzle-orm/libsql's `normalizeRow` and fields path both need. The package is
NOT installed in the fixture — the esbuild Plugin intercepts the bare specifier
at build time, and `drizzle-orm/libsql` is a subpath of the already-installed
`drizzle-orm`, so the native libsql addon is never fetched.

**SQLite DATETIME text:** modernc's database/sql driver always parses text in
DATE / DATETIME / TIMESTAMP columns into `time.Time` with no opt-out, but
better-sqlite3 returns the stored text (TypeORM saw a Go time object instead of
a Date). Rebuilding text from the time is lossy. `query` therefore drives
modernc's rows at the driver level (`conn.Raw`) and, for a value returned as
`time.Time`, reads the stored text from the statement
(`sqlite3_column_type` / `column_text`) via modernc's unexported `rows.pstmt`
and `conn.tls` by reflection (checked; a moved field panics with a code
pointer).

### node:net / node:tls Go shims (Era 1 socket layer)

The user chose Go-native socket shims over porting Node's `net.js` (which needs
~25 internal modules and the `tcp_wrap` / `stream_wrap` / `uv` / `cares_wrap`
bindings; TLS needs `tls_wrap`). Implemented in `runtime/nodeshim/net.go`,
`js/net.js`, `js/tls.js`. See [[node-compat-shims]] for the general node-shim
mechanics; the database-specific points:

- Go side (`InstallNet`, `og.net`): `connect` (TCP, Unix sockets, timeouts,
  local address, optional TLS handshake before data), a reader goroutine per
  connection (pause/resume for backpressure), one ordered writer goroutine
  (writes + write-side shutdown; TLS sends close_notify then TCP FIN),
  `startTLS` (upgrades a connected socket in place via `prefixConn` — the
  reader is stopped with a read deadline and bytes already read go into the
  handshake), `setNoDelay` / `setKeepAlive`, `isIP`, `canonicalizeIP`. Errors
  carry Node's `code` / `errno` (Linux values, as og reports Linux) /
  `syscall` / `address` / `port` ("connect ECONNREFUSED 127.0.0.1:5432").
- Sockets are weak loop handles (`Loop.Weak`): `Engine.Await` waits on them
  while a request needs them but the post-response drain does not (a ref'd idle
  connection would hold every response for the 5 s grace). `Engine.Await` now
  fires unref'd timers too while weak handles keep it waiting
  (`Loop.stepAlive`), matching Node — a socket's idle timeout is an unref'd
  timer that previously never fired. Socket events post in the opening async
  context (`Loop.Bind`) so request ALS survives callbacks.
- `net.Socket` is a readable-stream Duplex with Node's argument normalization,
  buffered writes while connecting, `allowHalfOpen`, `"close"` with hadError
  after `"error"`, idle `setTimeout`, `readyState`, counters, `_getpeername` /
  `_getsockname` caching. Servers stay unenv's.
- `tls.connect`: its own connection, or `{ socket }` (the upgrade PG/MySQL
  drivers do). The chain is verified in Go (against `ca`, else system roots)
  and errors reported with OpenSSL's codes/messages (classified from the
  chain's shape — the macOS platform verifier does not report them), including
  Node 24's `--use-system-ca` hint on the three codes Node adds it to.
  Identity is checked in JS by `options.checkServerIdentity` (Node's
  `tls.checkServerIdentity` transcribed from lib/tls.js). `getPeerCertificate`,
  `getProtocol`, `getCipher`, ALPN supported.

### netpool: the protocol-level pool shared across request VMs (Era 1)

The user chose a Go-side pool shared by request VMs over per-request
connections. `runtime/nodeshim/netpool.go` (core + PostgreSQL) and
`netpool_mysql.go`: every `net.connect` without direct TLS goes through
`Sockets.connect`, which hands the JS socket a `pooledConn` (a `net.Conn`);
`net.go` changes only where it dials, upgrades and closes.

- **Recording:** fresh connections are recorded with TLS terminating in Go so
  the pool sees plaintext. PostgreSQL is detected from the client's first
  packet (the pre-StartupMessage packets with the server's one-byte answers and
  where TLS followed, the StartupMessage, the greeting from AuthenticationOk to
  the first ReadyForQuery). MySQL is detected from a protocol-10 greeting before
  the client wrote (the handshake as alternating turns to the server's OK, the
  SSLRequest turn, capabilities incl. CLIENT_DEPRECATE_EOF).
- **Release** parks a session after a reset. PostgreSQL (nothing unanswered or
  half-framed) gets ROLLBACK if the last ReadyForQuery said a transaction was
  open, then DISCARD ALL. MySQL (between commands) gets COM_RESET_CONNECTION.
  The client's Terminate / COM_QUIT is withheld and the client reads EOF (as the
  server would close; postgres.js waits for it). Anything else closes. Parked
  sessions idle out after 60 s, at most 16 per server.
- **Reuse:** a connection to a server with a parked session answers the
  client's handshake from the recording (TLS without a new handshake, the chain
  judged by the new options) while the client's packets must match byte for
  byte. PostgreSQL on a mismatch ends the parked session (Terminate) and tries
  another matching parked session, else a fresh connection replayed with the
  client's steps so far. MySQL on a mismatch cannot move (it already answered
  this session's scramble) so it gets an ERR packet (1105) and the session ends,
  forcing a fresh reconnect.
- A first client write shorter than 8 bytes is taken as another protocol
  immediately (PG clients write their first packet whole). Other protocols are
  relayed and closed with their socket.

### Native Go-backed driver shims (Era 2, current)

The user then asked to replace the real JS drivers with native Go-backed
substitutes, registered in `runtime/natives` exactly as better-sqlite3/sharp
are. These bypass `node:net`/`netpool` entirely.

**pg (node-postgres)** — `runtime/natives/pg.go` + `js/pg.js`. A process-wide
`Postgres` service: `map[string]*pgxpool.Pool` keyed by DSN, shared across
request engines (`MaxConns` from the first `new Pool({max})`). Per-engine
`pgBinding` with async host methods `queryPool` / `acquire` / `queryClient` /
`release` and a `cleanup` (via `e.OnClose`) releasing only this engine's
borrowed conns, leaving the shared pool warm. Async uses the
`vm.NewPromise()` + `e.Loop.Async()` pattern (template
`runtime/native/native.go:211`). Results are node-pg-shaped (`rows`, `rowCount`,
`fields` with `name`/`dataTypeID`, `command`); type mapping is by OID to match
node-pg's parsers — int8/numeric to string, timestamps to Date, bytea to
ArrayBuffer. Errors carry the server message + SQLSTATE as `.code`. The JS
default export is `{ Pool, Client, types, get native()->null }`; supports both
`query(text, values)` and `query({text, values, rowMode})` (Kysely uses the
config form, Drizzle uses `rowMode:"array"`). `Postgres.get()` must not hold
`s.mu` across `pgxpool.NewWithConfig` — that call dials the server, so a stalled
connect deadlocked a second caller of the same DSN on the mutex. It builds the
pool WITHOUT the mutex, then re-locks after dialing (double-checked locking;
if another caller won the race for that DSN, close the loser), and sets
`cfg.ConnConfig.ConnectTimeout = 30s` so a stalled dial fails instead of hanging
forever. MySQL's `get()` needs no such change — `sql.Open` is lazy (no dial
under the lock).

**postgres (postgres.js)** — `runtime/natives/js/postgres.js` only, NO new Go
code; reuses pg's Go service (`Symbol.for("og.pg")`). `postgres(url, {max,
onnotice})` returns a tagged-template `sql`. `sql.unsafe(query, params)` returns
a lazy thenable PendingQuery resolving to object rows with `.values()`
(positional) and `.execute()` — what Drizzle's postgres-js driver uses.
`sql.begin(async sql => ...)` runs BEGIN/COMMIT/ROLLBACK on a pinned client,
nested begin -> SAVEPOINT. `sql.options.parsers` / `serializers` must be present
mutable objects (Drizzle mutates them at construction; absent -> crash before
any query).

**mysql2** — `runtime/natives/mysql.go` + `js/mysql2.js` (callback) +
`js/mysql2-promise.js` (promise), for two consumers (Drizzle uses
`mysql2/promise`, Kysely's MysqlDialect uses the callback API). A `MySQL`
service: process-wide `map[string]*sql.DB` per DSN via go-sql-driver, kept warm
with idle conns so a second request reuses them. Host methods
`queryPool`/`acquire`/`queryClient`/`release` (same promise pattern as pg),
`?` placeholders. A keyword heuristic (`isSelectLike`) splits Query (rows) from
Exec (affectedRows/insertId) since database/sql has no single call for both.
Types to mysql2 defaults: integers incl. bigint -> number, decimal -> string,
text -> string, blob -> Buffer, date -> Date. Errors carry
`.code`/`.errno`/`.sqlState` from `*mysql.MySQLError`. DSN converts a
`mysql://` URL to go-sql-driver's `user@tcp(host:port)/db` form. `rowsAsArray`
(Drizzle's positional request) honored; a write yields a ResultSetHeader
(`affectedRows`/`insertId`/`changedRows`). The `mysql2/promise` subpath is
served by Plugin directly (sources key `"mysql2/promise"`; OnLoad switched to
`strings.TrimPrefix(path, "og:")`).

**Wiring (all three):** `natives.Registry` entries (e.g. `{Package:"pg",
Module:"og:pg"}`), embed + `Plugin()` regex + `ModuleSource` + `Services.*` +
`Install` (binds e.g. `Symbol.for("og.pg")`, registers cleanup).
`internal/jshost/native.go` matches the real package entrypoints
(`node_modules/pg/lib/index.js`, `postgres/src/index.js` + `cjs/src/index.js`,
`mysql2/index.js` + `mysql2/promise.js`).

**Teardown:** `app.Meta.Close` (nil-guarded, `json:"-"`) closes
`nativeServices.Postgres` and `.MySQL`; `runtime/app/serve.go` defers it,
`serveORMFixture` registers it as a cleanup. Required because a process-wide
pgxpool conn held PGlite's one session open and deadlocked its `Close()`.
MySQL's database/sql idle conns do not block go-mysql-server's Close, but
MySQL is closed too for symmetry.

**Connection-settings fidelity (SSL default):** node-postgres and postgres.js
default to `ssl: false` (no TLS), but pgx defaults to `sslmode=prefer` (send
SSLRequest, TLS-then-plaintext fallback). The early shim built a DSN from only
endpoint fields + `max` and let pgx apply its defaults, so it did a TLS probe +
dual connection the real drivers never do. Fix: `pg.go` `Postgres.get()` forces
plaintext (`ConnConfig.TLSConfig = nil`, `Fallbacks = nil`) when the DSN carries
no `sslmode`; `js/pg.js` + `js/postgres.js` add `withSslmode(dsn, ssl)`
appending `sslmode=require` when the client passes a truthy `ssl`. mysql2 had no
such acute bug (mysql2 and go-sql-driver both default to no TLS); for parity a
truthy `ssl` maps to go-sql-driver's `tls=skip-verify` (encrypt-only).

Discrete pg options are folded into the DSN by `pg.js` `withOptions`:
`application_name`, `statement_timeout`,
`idle_in_transaction_session_timeout`, `connectionTimeoutMillis` (libpq
`connect_timeout`), and `options`. mysql2 maps `multipleStatements`,
`connectTimeout`, and `charset` into the mysql URL/DSN.

mysql2 query calls pass a `rowOptions` object to the Go host. Result conversion
honors `dateStrings`; `supportBigNumbers` / `bigNumberStrings` (including
BIGINT values beyond 2^53-1); `timezone` (`Z`, `local`, or `±HH:MM`); and
`typeCast(field, next)`. The callback sees mysql2 type-name strings plus
`field.string()` / `field.buffer()`; `typeCast: false` returns raw Buffers.

An `ssl` object containing a CA with `rejectUnauthorized: true` performs full
verification. mysql2 registers a content-hash-keyed go-sql-driver TLS config
and selects it with `tls=<key>`. pg registers a `*tls.Config` in its service,
passes an internal `ogtls` DSN parameter, and strips that parameter before pgx
parses the connection string. `runtime/tlsroots` supplies fallback public roots
on systems without an OS trust store and exposes the same bundle to
`node:tls`; see [[node-compat-shims]]. Without a supplied CA, the current
truthy-`ssl` paths still encrypt without verification.

### ORMs

Drizzle and Kysely (see SQLite and driver sections above). **TypeORM 1.1.2**
over the better-sqlite3 substitute needed four things:

1. **Decorator metadata** (`_tsoverlay/ogapi/decorators.go`, `build.go`):
   TypeORM reads column types from `design:type`, emitted by
   `emitDecoratorMetadata`. Next's SWC honors it; esbuild does not. When
   tsconfig sets `experimentalDecorators` + `emitDecoratorMetadata`, a
   TypeScript file outside node_modules that looks decorated (`decoratorRe`) is
   transpiled by TypeScript-Go's `transpile.TranspileModule` (facade
   `ogapi.TranspileDecorators`: project compiler options, target ESNext with
   `useDefineForClassFields` per the project target, ES modules, JSX preserved)
   then bundled as JS. Output is tsc's `__decorate` / `__metadata`.
2. **Node resolution conditions** (user's choice): the Next server bundle used
   to resolve every package with edge conditions (edge-light, worker, browser),
   which pulls TypeORM's browser build (dummy drivers). Now bare specifiers
   imported by the app or other packages resolve with `jshost.ResolveNode`
   (node + import/require, else main); Next/React and family (`edgePackages`,
   `@next/*`) keep the edge conditions og's templates are built on, RSC's
   react-server lookup first. Relative imports of Node-resolved modules resolve
   the same way (`nodeFiles`) — esbuild applies a package's `browser` field to
   relative paths too (TypeORM maps `./index.js` to `./browser/index.js`).
3. **TypeORM's driver require** (`patches.go`): `PlatformTools.load` does
   `require(name)` from its `KNOWN_MODULES` list. The patch prepends a `switch`
   of static requires: the installed driver is bundled, missing ones throw
   MODULE_NOT_FOUND when reached.
4. **CommonJS substitute shape** and **DATETIME text** (see SQLite section).

The fixture's `next.config` lists TypeORM in `serverExternalPackages` (Next's
docs; without it `next start` fails "SQLite package has not been found
installed").

### Missing optional modules

A `require()` of an uninstalled package (pg's optional `pg-native`;
`pg-cloudflare`) made the Next server build fail — Next leaves pg external so
Node loads it and the require fails only when reached. The server bundle now
resolves such a require to a module that throws Node's `MODULE_NOT_FOUND`
(`og-missing` namespace, `build.go`), as the Vite build host does; static
imports must still resolve.

### Runtime filesystem for packaged apps

A packaged Next app had no filesystem at run time (Drizzle's `migrate()` reading
`./drizzle`, routes reading `content/*.md`). The user chose Next's file tracing
for what to package plus a writable overlay for writes.

- **File tracing** (`internal/frameworks/next/trace.go`): a second server build
  keeps Node built-ins external (`og-trace-builtins`, ESM); its metafile names
  modules importing fs/path, which Next's own `@vercel/nft` traces at their
  paths (TS/JSX as esbuild JS, nft `readFile` hook), with `processCwd` at the
  project and an `ignore` that does not follow bundled modules. Then
  `outputFileTracingIncludes` / `outputFileTracingExcludes` (Next's glob +
  picomatch; og has one bundle for all routes). Per-module trace (0.5 s) beats
  tracing the 7 MB bundle (64 s in Sobek, and nft could not tell bundled `path`
  from Node's). Result is `Output.Files`, packaged under `files/`.
- **The overlay** (`runtime/vfs/overlay.go`): `vfs.Overlay` puts packaged files
  (read-only) under an in-memory write layer (files, dirs, whiteouts),
  process-wide (all request engines share it, like og:kv), lost at exit.
  `vfs.Install` gives `og.fs` write ops when handed one; a plain `fs.FS` stays
  read-only (EROFS). `runtime/app` mounts one overlay per process over traced
  files, server-program outputs, or nothing (`vfs.Empty`). `node:fs` gained
  `rmdir`/`rmSync` of a dir without `recursive` -> ERR_FS_EISDIR, `unlinkSync`
  of a dir -> EISDIR (Linux code).
- **SQLite in the overlay** (`runtime/natives/sqlite.go`): with `SQLite.Files`
  set, a db path opens a shared in-memory db (SQLite memdb VFS,
  `file:/og/<gen>/<name>?vfs=memdb`), kept by a keeper connection, seeded from
  the packaged file if any, registered as the overlay's provider: node:fs reads
  current bytes (`Serialize`), a write replaces it, a removal starts fresh. No
  db file reaches disk.

### Prisma

Prisma runs through driver adapters (`@prisma/adapter-pg` over the native pg
shim) rather than the native query engine; adapter-pg passes a per-query `types`
object to decode by OID itself, which the pg shim does not yet honor. Full story
in [[prisma]].

## Files

- `runtime/natives/pg.go` — process-wide `Postgres` service over pgx/pgxpool.
- `runtime/natives/js/pg.js` — node-postgres surface (`Pool`, `Client`).
- `runtime/natives/js/postgres.js` — postgres.js surface, reuses pg's service.
- `runtime/natives/mysql.go` — `MySQL` service over go-sql-driver/mysql.
- `runtime/natives/js/mysql2.js`, `js/mysql2-promise.js` — mysql2 callback + promise APIs.
- `runtime/natives/js/libsql-client.js` — `@libsql/client` over the SQLite service.
- `runtime/natives/sqlite.go` — better-sqlite3 substitute, DATETIME-text read, overlay-backed in-memory db.
- `runtime/natives/natives.go` — Registry, Plugin, CommonJS wrapper, Install bindings, Services.
- `runtime/nodeshim/net.go`, `js/net.js`, `js/tls.js` — Go-native node:net / node:tls (Era 1).
- `runtime/tlsroots` — embedded fallback trust store shared by Go TLS and
  `node:tls.rootCertificates`.
- `runtime/nodeshim/netpool.go`, `netpool_mysql.go` — protocol-aware pool shared across request VMs (Era 1).
- `runtime/vfs/overlay.go` — writable in-memory overlay filesystem.
- `internal/frameworks/next/trace.go` — Next file tracing (what to package).
- `internal/frameworks/next/patches.go` — TypeORM `PlatformTools.load` driver-require patch.
- `_tsoverlay/ogapi/decorators.go`, `internal/frameworks/next/build.go` — decorator-metadata transpile; `og-missing` namespace for uninstalled requires.
- `internal/jshost/native.go` — matches real package entrypoints to native substitutes; `jshost.ResolveNode`.
- `testdata/apps/next-orm` — the fixture (routes `/api/drivers/[driver]`, `/api/typeorm`, `/api/content`, `/api/migrate`, `/api/files`; `lib/entities.ts`).

## Test Coverage

- `TestORMsOnSQLite` (`internal/frameworks/next/orm_test.go`) — Drizzle + Kysely over the SQLite substitute; expectations from `next start` on Node 24. No `-tags aot`.
- `TestORMDriverMySQL` — Drizzle + Kysely + mysql2 against go-mysql-server v0.20.0 (in-memory); includes the connection-reuse assertion (second request opens no new connection). `-tags nodynamic`, no aot.
- `TestORMDriverPostgres` (`internal/frameworks/next`) — pg + postgres.js against pglite-go; the `pg, pg, postgres` sequence exercising cross-request warm-pool reuse on a one-session server. Needs `-tags aot` (or `-tags wazero`); the companion import is in `orm_pg_companion_test.go` (aot only).
- `TestORMDriverLibSQL` (`internal/frameworks/next/orm_libsql_test.go`) — `drizzle-orm/libsql` over the @libsql/client substitute; SQLite semantics. No aot.
- `TestTypeORM` — schema sync, saves, a loaded relation, query builder, rolled-back transaction, and column types from metadata. No aot.
- `TestSocketPool` (`runtime/nodeshim/netpool_test.go`) — fake PostgreSQL server, six clients in six engines sharing one `Sockets`: takeover of a reset session, another identity forcing a fresh connection, TLS takeover without re-handshake, a client gone mid-query. Neutralized three ways (no pool / no ROLLBACK / no TLS reuse).
- `TestNetAndTLS` — node:net/node:tls against Go-side servers (echo, silent, TLS echo, STARTTLS, closed port); expectations from Node 24.
- `TestPG` (`runtime/natives/pg_test.go`, `//go:build aot || wazero`, companion `pg_companion_test.go`), `TestPostgresJS`, `TestMySQL2` — native-shim unit coverage.
- `TestPGConnectionOptionsDSN` / `TestPGParseConfigAcceptsOptions` and
  `TestMySQLConnectionOptionsDSN` / `TestMySQLDSNOptions` cover discrete
  option mapping.
- `TestMySQLDateStrings`, `TestMySQLBigNumbers`, `TestMySQLTimezone`, and
  `TestMySQLTypeCast` exercise result shaping against go-mysql-server.
- `TestMySQLTLSVerify` performs a real TLS handshake with a correct and an
  unrelated CA. `TestPGRegisterExtractTLS` and `TestPGSSLVerifyDSN` cover pg at
  the configuration/DSN layer because PGlite has no TLS endpoint.
- `TestOverlay` (runtime/vfs), `TestSQLiteInOverlay` + `TestSQLiteDateText` (runtime/natives), `TestTracedFiles` (internal/frameworks/next) — overlay FS, SQLite-in-overlay, DATETIME text, traced files.
- `TestMissingOptionalModule` — `pg.native` is null, as under `next start`.

Postgres tests need pglite-go v0.2.3 (v0.2.2 recovers from PGlite's exit(100)
after a SQL error, which v0.2.0/v0.2.1 did not — those ended the whole session
on any SQL error; v0.2.3 then shipped the #7 fix, "negotiate SSL/startup before
taking the single-backend lock", PR #8, so a second concurrent client no longer
stalls during startup). The aot companion `pglite-go-aot` stays v0.1.0; it
(~235 MB of Go) compiles in ~2 min, cached after.

## Pitfalls

- **MySQL COM_CHANGE_USER missing (netpool, Era 1):** a MySQL client that
  answered a parked session's scramble cannot be re-pointed at a different
  parked identity; it gets an ERR (1105) and reconnects fresh. COM_CHANGE_USER
  would serve it but go-mysql-server lacks it. (TODO)
- **Parked-session starvation (netpool):** a server serving few sessions
  (PGlite's one; PostgreSQL's `max_connections`) can be starved by parked
  sessions of another identity — postgres.js after pg hung against one PGlite.
  pgbouncer has the same trade-off. The test gives postgres.js its own server.
- **Async write loss (Era 1):** og writes via a writer goroutine, so `write()`
  then `destroy()` at once can lose the write where Node's synchronous try-write
  usually sends it. (TODO)
- **TLS without a CA:** a truthy `ssl` value without `ca` still encrypts without
  verification (`tls=skip-verify` / plain `sslmode`). pg needs an end-to-end TLS
  handshake test against a real TLS PostgreSQL server; PGlite cannot provide
  one.
- **Remaining discrete options:** `idleTimeoutMillis` and other options not
  listed above are not mapped.
- **mysql2 gaps:** query streaming (`conn.connection.query().stream()`,
  Drizzle's async iterator) is unsupported. `typeCast` has minor fidelity gaps:
  `field.length` is best-effort, `db`/`table` are empty, and per-query
  `typeCast`/`timezone` overrides are not wired.
- **Entity / migration globs:** TypeORM needs `serverExternalPackages` in
  `next.config` or `next start` itself fails. Migration/entity file discovery
  relies on the traced-files + overlay layer being populated.
- **Edge-runtime routes' deps** still resolve with Node conditions (not
  distinguished from Node-runtime routes). (TODO)
- **Integration over unit tests:** the pg unit test passed first try but the
  integration test caught a warm-pool teardown deadlock (PGlite `Close()`
  blocked on a pool-held conn — fixed by `app.Meta.Close`). Port the library's
  full semantics (defaults included), not just enough to connect: green
  integration tests hid the SSL default divergence until adapter-pg exercised
  it.
- **npm peer ranges:** TypeORM 1.1.2's peer range is better-sqlite3 ^12; the
  Node reference used `--legacy-peer-deps`. og's installer does not enforce peer
  ranges.
