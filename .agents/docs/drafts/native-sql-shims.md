# Native Go-backed shims for pg / postgres.js / mysql2 (design)

Status: ALL THREE slices DONE and verified (2026-10-13): pg (jackc/pgx), postgres.js
(reuses pg's backend), mysql2 (go-sql-driver). See the JOURNAL entries "Native pg
(node-postgres) shim" and "Native postgres.js and mysql2 shims", and TODO.md line ~136.
Scope this doc: the design agreed before implementation.
TODO.md line ~136 ("Native (Go-backed) shims compatible with `mysql2`, `pg` and
`postgres` (postgres.js), as `better-sqlite3`'s substitute is").

## Why

Today these three packages run as the *real JS driver* over og's Go socket shims
(`node:net` / `node:tls`) plus a protocol-level pool (`netpool.go`,
`netpool_mysql.go`). That works and matches `next start`, but the socket pool has
sharp edges: handshake-mismatch reconnects (MySQL, no COM_CHANGE_USER), cross-identity
starvation on small servers, write-then-destroy loss, and TLS-root gaps. A native
shim over a Go SQL client driver replaces the JS driver at build/resolve time
(`natives.Registry`) and pools in Go, sidestepping all of that.

## Pattern (copies better-sqlite3 / sharp in `runtime/natives`)

- `Registry` entry `{Package: "pg", Module: "og:pg"}` (later `postgres`, `mysql2`).
- `runtime/natives/js/pg.js`: the package's JS API surface, reading the host from
  `globalThis[Symbol.for("og.pg")]`.
- `runtime/natives/pg.go`: `installPG(e *engine.Engine, svc *Postgres) *sobek.Object`
  of Go host methods. Note: takes the full `*engine.Engine` (not just `vm`) because
  async host methods need `e.Loop`.
- `Services` gains a process-wide `Postgres` field (like `SQLite`).
- Wire into `natives.Install`, `ModuleSource`, `Plugin()` regex, and the
  `nativeSubstitute` entry-file matcher in `internal/jshost/native.go`.

## Async bridge (not new — the established pattern)

`vm.NewPromise()` + `e.Loop.Async()`, exactly as `runtime/native/native.go:211`,
`runtime/webapi/fetch.go`, `runtime/isr/data.go`:

```go
promise, resolve, reject := vm.NewPromise()
done := e.Loop.Async()            // on loop goroutine; keeps loop alive
go func() {                       // blocking SQL off the loop
    rows, err := work()
    done(func(vm *sobek.Runtime) error {   // back on the loop
        if err != nil { return reject(jsError(vm, err)) }
        return resolve(toJS(vm, rows))
    })
}()
return vm.ToValue(promise)
```

A small shared helper in `runtime/natives` will wrap this for reuse by pg/postgres/mysql2.

## pg driver: pgx/v5 + pgxpool

- Process-wide `map[string]*pgxpool.Pool` keyed by DSN (connection string), shared
  across request engines. `MaxConns` from the first `new Pool({max})` for that DSN.
- **Pool lifecycle decision:** JS `new Pool()` / `pool.end()` map to acquire/release
  against the shared pgxpool; `pool.end()` releases this request's borrowed conns but
  leaves the shared pool open. This is what enables cross-request reuse and makes
  `TestORMDriverPostgres` (PGlite serves one session; pg runs twice) pass.
- `pool.connect()` → `pgxpool.Acquire()` a pinned conn (needed for Drizzle/Kysely
  transactions: BEGIN … COMMIT/ROLLBACK span one conn). `client.release()` → `Release()`.
- `pool.query(text, values?)` one-off: acquire+query+release.

### node-postgres Result fidelity (the crux)

Target (from `TestORMDriverPostgres`):
`{"drizzle":[{"id":1,"name":"alpha","done":false},{"id":2,"name":"beta","done":true}],"rollback":"failed","count":"2","kysely":[{"name":"alpha","done":true},{"name":"beta","done":true}]}`

- `Result` = `{ rows: [...objects...], rowCount, fields: [{name, dataTypeID}], command }`.
- Type mapping by OID must match node-pg's default parsers: int2/int4 → number,
  **int8 → string** (why `count` is `"2"`), float4/float8 → number, bool → boolean,
  text/varchar → string, timestamp/timestamptz/date → `Date`, json/jsonb → parsed,
  numeric → string, bytea → Buffer. Use pgx `FieldDescriptions()` (OIDs) + decoded
  values; override the node-pg-specific string cases by OID.
- `pg.native` is a lazy getter returning `null` (pg-native absent).

## Verification

- Fast loop: a `runtime/natives/pg_test.go` (`//go:build aot || wazero`) that starts
  PGlite on a TCP listener (like `startPostgres` in `orm_pg_aot_test.go`) and runs the
  fixture scenario through `import pg from "pg"`, asserting the node-pg-shaped output
  incl. `count:"2"` and transaction rollback. `-tags wazero` avoids the 235 MB AOT
  companion (pure-Go wasm; only the PGlite data bundle downloads).
- Integration: `TestORMDriverPostgres` once the shim is wired into the Next build.

## Sequencing

pg first (this slice), then review, then postgres.js (reuses the pgx backend; new JS
surface only), then mysql2 (adds go-sql-driver/mysql + MySQL pool + JS surface).
