# Draft issue for github.com/moriyoshi/pglite-go

Status: FILED 2026-10-13 as moriyoshi/pglite-go#7
(https://github.com/moriyoshi/pglite-go/issues/7). Found while running Oganesson's
native `pg` (pgx) shim against pglite-go as a test server.

## Title

ServeConn negotiates SSL/startup while holding the single-backend lock, so a second
concurrent client using `sslmode=prefer` (pgx/libpq default) hangs forever in SSLRequest

## Summary

`ServeConn` performs the startup handshake — including the SSLRequest/GSSENCRequest
decline — only after it has acquired the exclusive single-backend lock, which it holds
for the entire connection lifetime. Because pglite has one shared backend session, a
second client that connects while the first is still open blocks on that lock before a
single byte of its stream is read. A client that opens the connection by sending an
SSLRequest first — the default for pgx/jackc and libpq (`sslmode=prefer`) — therefore
blocks indefinitely waiting for the `S`/`N` response, which is only sent once the first
client disconnects.

## Where

`serveconn.go`:

```go
func (db *DB) ServeConn(conn net.Conn) error {
	defer conn.Close()

	db.mu.Lock()            // <- acquired before any client bytes are read
	defer db.mu.Unlock()    // <- held for the whole connection lifetime
	...
	// Startup phase: negotiate SSL/GSS (declined), then answer the StartupMessage.
	if err := db.serveStartup(conn, br); err != nil { ... }   // <- SSL 'N' reply is in here
	...
}
```

`serveStartup` correctly replies `N` to SSLRequest (80877103) / GSSENCRequest (80877104)
and then reads the StartupMessage — but it only runs after `db.mu.Lock()` returns.

## Why it hangs

- Client A connects, `ServeConn` A takes `db.mu`, completes startup, and keeps the
  connection open (e.g. a connection pool holding a warm connection). `db.mu` stays held.
- Client B connects. `ServeConn` B blocks on `db.mu.Lock()` before reading anything from
  B's socket.
- Client B's driver (pgx default `sslmode=prefer`) has already written an SSLRequest and
  is blocked reading the 1-byte `S`/`N` reply. pglite never reads/answers it until A
  disconnects, so B hangs — permanently, if A is a pooled connection that stays open.

Clients that disable SSL (node-postgres default `ssl: false`, or `sslmode=disable`) skip
the SSLRequest and so do not hit this; clients that default to SSL negotiation (pgx,
libpq/psql, most Go/Rust drivers) do.

## Observed goroutine state (the stalled second client)

```
jackc/puddle.initResourceValue
  -> pgx/v5.ConnectConfig -> pgconn.connectPreferred (2 fallbacks: TLS, plaintext)
    -> pgconn.connectOne -> pgconn.startTLS (pgconn.go:548)   [blocked reading S/N]
github.com/moriyoshi/pglite-go.(*DB).ServeConn                 [blocked in db.mu.Lock, or
  another ServeConn in readFrontendMessage holding db.mu]
```

## Reproduction

Start a listener that calls `db.ServeConn(c)` per accepted connection (as the README
shows). Point a `pgxpool.Pool` at it with the default sslmode (prefer) and `MaxConns > 1`,
and issue two overlapping queries (or keep one connection checked out while acquiring a
second). The second connection hangs in `pgconn.startTLS`. The same two queries succeed
if the DSN sets `sslmode=disable`.

## Suggested fix

Do the startup/SSL negotiation BEFORE taking the exclusive backend lock — only the
query-execution relay needs `db.mu`. Reply `N` to SSLRequest/GSSENCRequest and read the
StartupMessage outside the lock; acquire `db.mu` only once the client is ready to issue
queries. (A second client would then complete its handshake and simply wait at the
query-relay boundary, instead of deadlocking mid-SSL-negotiation.)

Alternatively, support TLS in the wire protocol (reply `S` and perform a TLS handshake),
and/or document that concurrent clients must use `sslmode=disable` with the single-session
server.

## Note for Oganesson

Oganesson's native pg shim uses pgx with its default sslmode, which is correct for real
servers. Hardening already applied on the og side (`runtime/natives/pg.go`): the pool
mutex is no longer held across `pgxpool.NewWithConfig`, and `ConnConfig.ConnectTimeout`
is set, so a stall now fails fast instead of hanging forever. `TestPrismaPostgres` stays
skipped until pglite-go is fixed (or the test URL is given `sslmode=disable` as an interim
workaround).
