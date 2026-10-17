package natives

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/grafana/sobek"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/moriyoshi/oganesson/runtime/engine"
	"golang.org/x/sync/singleflight"
)

// Postgres is the process-wide side of the pg (node-postgres) substitute: one
// pgx pool per DSN, shared by every request engine. node-postgres code makes a
// fresh pg.Pool per request and ends it in a finally; the shim maps that onto
// the shared Go pool, which stays warm across requests (pool.end() releases the
// request's borrowed connections but leaves the pool open). That is what lets a
// second request reuse a server that serves one session at a time.
type Postgres struct {
	mu    sync.Mutex
	pools map[string]*pgxpool.Pool // by DSN
	// tlsConfigs holds the verifying TLS configs built from node-postgres'
	// ssl: { ca } options, keyed by a content hash (the og-internal `ogtls`
	// DSN parameter names one). pgx has no global TLS registry like
	// go-sql-driver's, so the service keeps its own.
	tlsConfigs map[string]*tls.Config
	// sf serializes pool creation per DSN: without it two concurrent get()s for
	// a new DSN both dial, which against a single-session server (PGlite) means a
	// second connection that blocks on the one backend.
	sf singleflight.Group
}

// NewPostgres returns an empty Postgres service.
func NewPostgres() *Postgres {
	return &Postgres{pools: map[string]*pgxpool.Pool{}, tlsConfigs: map[string]*tls.Config{}}
}

// registerTLS records a TLS config that verifies the server against caPEM
// (node-postgres' ssl: { ca, rejectUnauthorized: true }) and returns the
// og-internal `ogtls` key for it. The key is a content hash, so an identical
// ssl option reuses one config and distinct ones never collide.
func (s *Postgres) registerTLS(caPEM, serverName string) (string, error) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(caPEM)) {
		return "", errors.New("pg: ssl.ca contains no valid certificate")
	}
	sum := sha256.Sum256([]byte(serverName + "\x00" + caPEM))
	key := "og-" + hex.EncodeToString(sum[:8])
	s.mu.Lock()
	s.tlsConfigs[key] = &tls.Config{RootCAs: pool, ServerName: serverName, MinVersion: tls.VersionTLS12}
	s.mu.Unlock()
	return key, nil
}

// extractTLS pulls the og-internal `ogtls` parameter out of dsn, returning the
// DSN with it removed (so pgx does not see an unknown parameter) and the TLS
// config it names (nil when absent).
func (s *Postgres) extractTLS(dsn string) (string, *tls.Config) {
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn, nil
	}
	q := u.Query()
	key := q.Get("ogtls")
	if key == "" {
		return dsn, nil
	}
	q.Del("ogtls")
	u.RawQuery = q.Encode()
	s.mu.Lock()
	cfg := s.tlsConfigs[key]
	s.mu.Unlock()
	return u.String(), cfg
}

// Close closes every pool.
func (s *Postgres) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for dsn, p := range s.pools {
		p.Close()
		delete(s.pools, dsn)
	}
}

// get returns the shared pool for dsn, creating it (capped at max connections,
// honored only on creation) the first time.
func (s *Postgres) get(dsn string, max int32) (*pgxpool.Pool, error) {
	s.mu.Lock()
	if p, ok := s.pools[dsn]; ok {
		s.mu.Unlock()
		return p, nil
	}
	s.mu.Unlock()

	// Create at most one pool per DSN, even under concurrent get()s. Without
	// this, two concurrent callers for a new DSN both dial; against a
	// single-session server (PGlite) the second connection blocks on the one
	// backend and times out. singleflight collapses concurrent creations for the
	// same DSN (different DSNs still proceed in parallel), and s.mu is not held
	// across the dial, so a slow connect does not block get() for other DSNs.
	v, err, _ := s.sf.Do(dsn, func() (any, error) {
		s.mu.Lock()
		p := s.pools[dsn]
		s.mu.Unlock()
		if p != nil {
			return p, nil
		}
		// An ssl: { ca } verifying config travels as the og-internal `ogtls`
		// parameter; strip it (pgx would reject the unknown key) and apply it
		// as the connection's TLS config below. The pool is still keyed by the
		// full dsn (ogtls included), so distinct CAs get distinct pools.
		cleanDSN, tlsCfg := s.extractTLS(dsn)
		cfg, err := pgxpool.ParseConfig(cleanDSN)
		if err != nil {
			return nil, err
		}
		if max > 0 {
			cfg.MaxConns = max
		}
		// pgx's default (QueryExecModeCacheStatement) caches auto-named prepared
		// statements per connection. That desyncs from a server that drops
		// prepared statements on transaction rollback (PGlite does): pgx then
		// re-Parses a name the server still has and the query fails with
		// `prepared statement "stmtcache_…" already exists`. (Observed with
		// Prisma's adapter-pg, whose route rolls back a transaction mid-request.)
		// QueryExecModeCacheDescribe executes through the *unnamed* statement, so
		// there is no named statement to collide, while still caching the
		// describe (parameter/result OIDs) for binary decoding — keeping the
		// value fidelity the default mode gives (int4 as a number, etc.), unlike
		// QueryExecModeExec which falls back to the text format and mis-types
		// results. node-postgres likewise keeps no client-side statement cache.
		cfg.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeCacheDescribe
		// node-postgres and postgres.js default to NO TLS (`ssl: false`); pgx
		// instead defaults to sslmode=prefer, which sends an SSLRequest and does
		// a TLS-then-plaintext fallback. Match the JS drivers: unless the DSN
		// asked for TLS (sslmode=…, set by js/pg.js and js/postgres.js when the
		// client passes `ssl`), connect in plaintext with no SSLRequest probe.
		if !strings.Contains(cleanDSN, "sslmode") {
			cfg.ConnConfig.TLSConfig = nil
			cfg.ConnConfig.Fallbacks = nil
		}
		// ssl: { ca }: verify the server against the caller's CA (pgx uses this
		// config as-is, so there is no TLS-then-plaintext fallback).
		if tlsCfg != nil {
			cfg.ConnConfig.TLSConfig = tlsCfg
			cfg.ConnConfig.Fallbacks = nil
		}
		if cfg.ConnConfig.ConnectTimeout == 0 {
			cfg.ConnConfig.ConnectTimeout = 30 * time.Second
		}
		np, err := pgxpool.NewWithConfig(context.Background(), cfg)
		if err != nil {
			return nil, err
		}
		s.mu.Lock()
		s.pools[dsn] = np
		s.mu.Unlock()
		return np, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*pgxpool.Pool), nil
}

// pgField is a result column: node-postgres exposes name and dataTypeID (OID).
type pgField struct {
	name string
	oid  uint32
}

// pgResult is one query's result, decoded off the loop. rows are positional
// (the shim builds node-pg's object rows, or Drizzle's rowMode:"array" rows,
// in JS).
type pgResult struct {
	fields   []pgField
	rows     [][]any
	rowCount int64
	command  string
	// raw: rows hold the Postgres text representation (string, or nil for
	// NULL) of each column instead of a Go-decoded value. The JS side then
	// applies the query's own type parsers (node-postgres' getTypeParser),
	// which is what consumers like Prisma's adapter-pg rely on.
	raw bool
}

// querier is what both *pgxpool.Pool and *pgxpool.Conn satisfy.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// runQuery runs text on q and collects the result. The server error for a bad
// statement surfaces here (at Query or rows.Err), carrying the SQLSTATE.
func runQuery(ctx context.Context, q querier, text string, args []any, raw bool) (*pgResult, error) {
	qargs := args
	if raw {
		// Ask Postgres for every column in the text format, so the raw bytes are
		// the text node-postgres' type parsers expect.
		qargs = append([]any{pgx.QueryResultFormats{pgx.TextFormatCode}}, args...)
	}
	rows, err := q.Query(ctx, text, qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res pgResult
	res.raw = raw
	for _, fd := range rows.FieldDescriptions() {
		res.fields = append(res.fields, pgField{name: fd.Name, oid: fd.DataTypeOID})
	}
	for rows.Next() {
		if raw {
			rv := rows.RawValues() // the buffer is reused per row; string() copies
			row := make([]any, len(rv))
			for i, b := range rv {
				if b == nil {
					row[i] = nil
				} else {
					row[i] = string(b)
				}
			}
			res.rows = append(res.rows, row)
			continue
		}
		vals, err := rows.Values()
		if err != nil {
			return nil, err
		}
		row := make([]any, len(vals))
		copy(row, vals)
		res.rows = append(res.rows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	tag := rows.CommandTag()
	res.rowCount = tag.RowsAffected()
	res.command = firstWord(tag.String())
	return &res, nil
}

func firstWord(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' {
			return s[:i]
		}
	}
	return s
}

// pgBinding is the per-engine host object. Pinned connections (from
// pool.connect(), needed for transactions) are kept by an integer handle.
type pgBinding struct {
	e     *engine.Engine
	vm    *sobek.Runtime
	svc   *Postgres
	ctx   context.Context
	conns map[int64]*pgxpool.Conn
	next  int64
}

// jsError turns a Go error into a node-pg-shaped JS Error: the server's message
// and SQLSTATE as .code, so Drizzle/Kysely see the error they expect.
func (b *pgBinding) jsError(err error) sobek.Value {
	msg, code := err.Error(), ""
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		msg, code = pgErr.Message, pgErr.Code
	}
	o := b.vm.NewGoError(errors.New(msg))
	if code != "" {
		_ = o.Set("code", code)
	}
	return o
}

// params converts a JS array of bind values to Go for pgx. Called on the loop.
func (b *pgBinding) params(v sobek.Value) []any {
	if v == nil || sobek.IsUndefined(v) || sobek.IsNull(v) {
		return nil
	}
	o := v.ToObject(b.vm)
	n := int(o.Get("length").ToInteger())
	out := make([]any, n)
	for i := 0; i < n; i++ {
		out[i] = b.toGo(o.Get(strconv.Itoa(i)))
	}
	return out
}

func (b *pgBinding) toGo(v sobek.Value) any {
	if v == nil || sobek.IsUndefined(v) || sobek.IsNull(v) {
		return nil
	}
	switch x := v.Export().(type) {
	case bool:
		return x
	case int64:
		return x
	case float64:
		return x
	case string:
		return x
	case []byte:
		return append([]byte(nil), x...)
	case sobek.ArrayBuffer:
		return append([]byte(nil), x.Bytes()...)
	case time.Time:
		return x
	case []any:
		// A JS array parameter (e.g. a Postgres array like the schema list
		// text[] ["public"]). Return a Go slice so pgx array-encodes it, using
		// the parameter OID from the describe; without this it would stringify
		// to "public" and Postgres rejects it as a malformed array literal.
		return x
	}
	return v.String()
}

// jsVal maps a decoded value to the JS value node-postgres' default parsers
// give for its OID. The notable cases: int8 and numeric come back as strings
// (node-pg avoids precision loss), timestamps as Date, json as parsed.
func (b *pgBinding) jsVal(oid uint32, v any) any {
	if v == nil {
		return nil
	}
	switch oid {
	case 20, 1700: // int8, numeric -> string
		return decimalString(v)
	case 16: // bool
		if bv, ok := v.(bool); ok {
			return bv
		}
	case 21, 23, 26, 700, 701: // int2, int4, oid, float4, float8 -> number
		if f, ok := toFloat(v); ok {
			return f
		}
	case 1082, 1114, 1184: // date, timestamp, timestamptz -> Date
		if t, ok := v.(time.Time); ok {
			d, err := b.vm.New(b.vm.Get("Date"), b.vm.ToValue(float64(t.UnixMilli())))
			if err == nil {
				return d
			}
		}
	case 114, 3802: // json, jsonb -> parsed
		return b.jsonParse(v)
	case 17: // bytea -> ArrayBuffer (pg.js wraps it as a Buffer)
		if bb, ok := v.([]byte); ok {
			return b.vm.NewArrayBuffer(append([]byte(nil), bb...))
		}
	}
	switch x := v.(type) {
	case string:
		return x
	case []byte:
		return string(x)
	}
	return b.vm.ToValue(v)
}

func (b *pgBinding) jsonParse(v any) any {
	var raw []byte
	switch x := v.(type) {
	case []byte:
		raw = x
	case string:
		raw = []byte(x)
	default:
		return b.vm.ToValue(v)
	}
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return string(raw)
	}
	return b.vm.ToValue(out)
}

func decimalString(v any) string {
	switch x := v.(type) {
	case int64:
		return strconv.FormatInt(x, 10)
	case int32:
		return strconv.FormatInt(int64(x), 10)
	case int16:
		return strconv.FormatInt(int64(x), 10)
	case uint64:
		return strconv.FormatUint(x, 10)
	case string:
		return x
	case []byte:
		return string(x)
	}
	return fmt.Sprintf("%v", v)
}

func toFloat(v any) (float64, bool) {
	switch x := v.(type) {
	case int16:
		return float64(x), true
	case int32:
		return float64(x), true
	case int64:
		return float64(x), true
	case uint32:
		return float64(x), true
	case uint64:
		return float64(x), true
	case float32:
		return float64(x), true
	case float64:
		return x, true
	}
	return 0, false
}

// shape builds the host result (positional rows + field descriptors). Runs on
// the loop.
func (b *pgBinding) shape(res *pgResult) sobek.Value {
	vm := b.vm
	o := vm.NewObject()
	fields := make([]any, len(res.fields))
	for i, f := range res.fields {
		fo := vm.NewObject()
		_ = fo.Set("name", f.name)
		_ = fo.Set("dataTypeID", int64(f.oid))
		fields[i] = fo
	}
	rows := make([]any, len(res.rows))
	for i, r := range res.rows {
		vals := make([]any, len(r))
		for j := range r {
			if res.raw {
				// Text (string) or nil; the JS side applies the query's parsers.
				vals[j] = r[j]
				continue
			}
			oid := uint32(0)
			if j < len(res.fields) {
				oid = res.fields[j].oid
			}
			vals[j] = b.jsVal(oid, r[j])
		}
		rows[i] = vm.ToValue(vals)
	}
	_ = o.Set("fields", vm.ToValue(fields))
	_ = o.Set("rows", vm.ToValue(rows))
	_ = o.Set("rowCount", res.rowCount)
	_ = o.Set("command", res.command)
	return o
}

// async runs a query off the loop and settles a JS promise with the shaped
// result, the established engine pattern (runtime/native, runtime/webapi).
func (b *pgBinding) async(work func() (*pgResult, error)) sobek.Value {
	vm := b.vm
	promise, resolve, reject := vm.NewPromise()
	done := b.e.Loop.Async()
	go func() {
		res, err := work()
		done(func(vm *sobek.Runtime) error {
			if err != nil {
				return reject(b.jsError(err))
			}
			return resolve(b.shape(res))
		})
	}()
	return vm.ToValue(promise)
}

// queryPool runs a one-off query on the shared pool (acquire+query+release).
// raw asks for the Postgres text representation of each column (see pgResult).
func (b *pgBinding) queryPool(dsn string, max int64, text string, params sobek.Value, raw bool) sobek.Value {
	args := b.params(params)
	return b.async(func() (*pgResult, error) {
		pool, err := b.svc.get(dsn, int32(max))
		if err != nil {
			return nil, err
		}
		return runQuery(b.ctx, pool, text, args, raw)
	})
}

// acquire pins a connection from the shared pool (pool.connect()); the handle
// is returned to JS. Needed for transactions, which span one connection.
func (b *pgBinding) acquire(dsn string, max int64) sobek.Value {
	vm := b.vm
	promise, resolve, reject := vm.NewPromise()
	done := b.e.Loop.Async()
	go func() {
		pool, err := b.svc.get(dsn, int32(max))
		var conn *pgxpool.Conn
		if err == nil {
			conn, err = pool.Acquire(b.ctx)
		}
		done(func(vm *sobek.Runtime) error {
			if err != nil {
				return reject(b.jsError(err))
			}
			b.next++
			id := b.next
			b.conns[id] = conn
			return resolve(vm.ToValue(id))
		})
	}()
	return vm.ToValue(promise)
}

// queryClient runs a query on a pinned connection.
func (b *pgBinding) queryClient(id int64, text string, params sobek.Value, raw bool) sobek.Value {
	args := b.params(params)
	conn := b.conns[id]
	return b.async(func() (*pgResult, error) {
		if conn == nil {
			return nil, errors.New("Client was released before use")
		}
		return runQuery(b.ctx, conn, text, args, raw)
	})
}

// release returns a pinned connection to the pool.
func (b *pgBinding) release(id int64) {
	if c, ok := b.conns[id]; ok {
		c.Release()
		delete(b.conns, id)
	}
}

func (b *pgBinding) cleanup() {
	for id, c := range b.conns {
		c.Release()
		delete(b.conns, id)
	}
}

// registerTLS records a verifying TLS config for an ssl: { ca } option and
// returns its og-internal `ogtls` key for the DSN (js/pg.js).
func (b *pgBinding) registerTLS(caPEM, serverName string) string {
	key, err := b.svc.registerTLS(caPEM, serverName)
	if err != nil {
		panic(b.vm.NewTypeError(err.Error()))
	}
	return key
}

func installPG(e *engine.Engine, svc *Postgres) (*sobek.Object, func()) {
	b := &pgBinding{e: e, vm: e.VM, svc: svc, ctx: context.Background(), conns: map[int64]*pgxpool.Conn{}}
	o := e.VM.NewObject()
	_ = o.Set("queryPool", b.queryPool)
	_ = o.Set("acquire", b.acquire)
	_ = o.Set("queryClient", b.queryClient)
	_ = o.Set("release", b.release)
	_ = o.Set("registerTLS", b.registerTLS)
	return o, b.cleanup
}
