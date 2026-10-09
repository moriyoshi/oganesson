package natives

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"modernc.org/libc"
	sqlite3 "modernc.org/sqlite/lib"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"github.com/grafana/sobek"
	"github.com/moriyoshi/oganesson/runtime/vfs"
	"modernc.org/sqlite" // database/sql driver "sqlite"
)

// SQLite is the process-wide side of better-sqlite3: one connection pool
// per database file, shared by every request engine (each JS Database gets
// its own connection, so its transactions stay on it).
type SQLite struct {
	mu    sync.Mutex
	pools map[string]*sql.DB // by DSN
	// Files, when set, is the runtime's filesystem (runtime/vfs's overlay):
	// database files live in it, in memory (SQLite's memdb), seeded from
	// the packaged file if there is one, their bytes visible to node:fs.
	Files *vfs.Overlay
	dbs   map[string]*memFile // by name in Files
	gen   int
	// id makes this service's memdb DSNs globally unique. modernc's memdb VFS
	// keys its in-memory databases by the DSN path process-wide, so two SQLite
	// services (e.g. a build-time prerender app and the serving app) that both
	// open the same file name would otherwise land on the same memory and the
	// first's keeper connection would lock out the second (SQLITE_BUSY).
	id uint64
}

// sqliteServiceSeq numbers SQLite services for globally unique memdb DSNs.
var sqliteServiceSeq atomic.Uint64

// memFile is a database file in Files: a shared in-memory database, kept
// open by keeper, that backs its name in Files.
type memFile struct {
	svc    *SQLite
	dsn    string
	keeper *sql.Conn
	pool   *sql.DB
}

// Content serializes the database (vfs.Provider).
func (m *memFile) Content() ([]byte, error) {
	var out []byte
	err := m.keeper.Raw(func(dc any) error {
		b, err := dc.(interface{ Serialize() ([]byte, error) }).Serialize()
		out = b
		return err
	})
	return out, err
}

// Replace loads data as the database (vfs.Provider): fs writes to the
// file.
func (m *memFile) Replace(data []byte) error { return seedMemDB(m.dsn, data) }

// seedMemDB loads a database image into the shared in-memory database dsn:
// a private connection deserializes it, then backs it up into dsn.
func seedMemDB(dsn string, data []byte) error {
	p, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return err
	}
	defer p.Close()
	c, err := p.Conn(context.Background())
	if err != nil {
		return err
	}
	defer c.Close()
	return c.Raw(func(dc any) error {
		if err := dc.(interface{ Deserialize([]byte) error }).Deserialize(data); err != nil {
			return err
		}
		bk, err := dc.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		}).NewBackup(dsn)
		if err != nil {
			return err
		}
		for {
			more, err := bk.Step(-1)
			if err != nil {
				bk.Finish()
				return err
			}
			if !more {
				break
			}
		}
		return bk.Finish()
	})
}

// memDSN is the DSN of the database file name in Files, created (or
// seeded from the file there) on first use.
func (s *SQLite) memDSN(name string, mustExist bool) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dbs == nil {
		s.dbs = map[string]*memFile{}
	}
	if m, ok := s.dbs[name]; ok {
		if s.Files.ProviderOf(name) == m {
			return m.dsn, nil
		}
		// The file was removed or replaced through node:fs: a new database.
		_ = m.keeper.Close()
		_ = m.pool.Close()
		delete(s.dbs, name)
	}
	initial, exists := s.Files.Initial(name)
	if mustExist && !exists {
		return "", errors.New("unable to open database file")
	}
	s.gen++
	dsn := fmt.Sprintf("file:/og/%d/%d/%s?vfs=memdb", s.id, s.gen, name)
	pool, err := sql.Open("sqlite", dsn)
	if err != nil {
		return "", err
	}
	keeper, err := pool.Conn(context.Background())
	if err != nil {
		pool.Close()
		return "", err
	}
	if len(initial) > 0 {
		if err := seedMemDB(dsn, initial); err != nil {
			keeper.Close()
			pool.Close()
			return "", err
		}
	}
	m := &memFile{svc: s, dsn: dsn, keeper: keeper, pool: pool}
	if err := s.Files.Provide(name, m); err != nil {
		keeper.Close()
		pool.Close()
		return "", err
	}
	s.dbs[name] = m
	return dsn, nil
}

// NewSQLite returns an empty SQLite service.
func NewSQLite() *SQLite {
	return &SQLite{pools: map[string]*sql.DB{}, id: sqliteServiceSeq.Add(1)}
}

// Close closes every pool.
func (s *SQLite) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var errs []error
	for _, db := range s.pools {
		errs = append(errs, db.Close())
	}
	s.pools = map[string]*sql.DB{}
	return errors.Join(errs...)
}

func (s *SQLite) pool(dsn string, shared bool) (*sql.DB, error) {
	if !shared {
		return sql.Open("sqlite", dsn)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if db, ok := s.pools[dsn]; ok {
		return db, nil
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s.pools[dsn] = db
	return db, nil
}

// readerRe recognizes statements that return rows (better-sqlite3's
// statement.reader); database/sql cannot report it before running them.
var (
	readerRe    = regexp.MustCompile(`(?is)^\s*(?:(?:--[^\n]*\n|/\*.*?\*/)\s*)*(select|with|values|explain|pragma)\b`)
	returningRe = regexp.MustCompile(`(?is)\breturning\b`)
)

func isReader(q string) bool { return readerRe.MatchString(q) || returningRe.MatchString(q) }

type sqliteDB struct {
	conn *sql.Conn
	pool *sql.DB // owned (in-memory databases); nil for shared pools
}

type sqliteBinding struct {
	vm   *sobek.Runtime
	svc  *SQLite
	ctx  context.Context
	dbs  map[int64]*sqliteDB
	next int64
}

// throw raises err as better-sqlite3 would: SQLite's message, and its
// result code's name (extended: SQLITE_CONSTRAINT_UNIQUE, which Prisma's
// adapter maps to P2002) as the code.
func (b *sqliteBinding) throw(err error) {
	code, message := sqliteErrorInfo(err)
	o := b.vm.NewGoError(errors.New(message))
	_ = o.Set("code", code)
	panic(o)
}

func (b *sqliteBinding) db(id int64) *sqliteDB {
	d, ok := b.dbs[id]
	if !ok {
		panic(b.vm.NewTypeError("The database connection is not open"))
	}
	return d
}

// open(filename, readonly, fileMustExist, timeoutMs) -> id
func (b *sqliteBinding) open(filename string, readonly, mustExist bool, timeout int64) int64 {
	memory := filename == "" || filename == ":memory:"
	var dsn string
	if memory {
		dsn = ":memory:"
	} else if b.svc.Files != nil {
		// A file in the runtime's filesystem (its root is the runtime's
		// cwd, as node:fs sees it).
		name := path.Clean("/" + filepath.ToSlash(filename))[1:]
		d, err := b.svc.memDSN(name, readonly || mustExist)
		if err != nil {
			b.throw(err)
		}
		dsn = d
		if readonly {
			dsn += "&mode=ro"
		}
	} else {
		abs, err := filepath.Abs(filename)
		if err != nil {
			b.throw(err)
		}
		mode := "rwc"
		if readonly {
			mode = "ro"
		} else if mustExist {
			mode = "rw"
		}
		dsn = "file:" + abs + "?mode=" + mode
	}
	if timeout <= 0 {
		timeout = 5000
	}
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	dsn += sep + fmt.Sprintf("_pragma=busy_timeout(%d)", timeout)
	pool, err := b.svc.pool(dsn, !memory)
	if err != nil {
		b.throw(err)
	}
	conn, err := pool.Conn(b.ctx)
	if err == nil {
		err = conn.PingContext(b.ctx)
	}
	if err != nil {
		if memory {
			_ = pool.Close()
		}
		b.throw(err)
	}
	d := &sqliteDB{conn: conn}
	if memory {
		d.pool = pool
	}
	b.next++
	b.dbs[b.next] = d
	return b.next
}

func (b *sqliteBinding) close(id int64) {
	d, ok := b.dbs[id]
	if !ok {
		return
	}
	delete(b.dbs, id)
	_ = d.conn.Close()
	if d.pool != nil {
		_ = d.pool.Close()
	}
}

func (b *sqliteBinding) closeAll() {
	for id := range b.dbs {
		b.close(id)
	}
}

// args converts better-sqlite3 bindings: positional values and, from a
// plain object, named parameters.
func (b *sqliteBinding) args(positional []sobek.Value, named sobek.Value) []any {
	var out []any
	for _, v := range positional {
		out = append(out, b.toSQL(v))
	}
	if named != nil && !sobek.IsUndefined(named) && !sobek.IsNull(named) {
		o := named.ToObject(b.vm)
		for _, k := range o.Keys() {
			out = append(out, sql.Named(k, b.toSQL(o.Get(k))))
		}
	}
	return out
}

func (b *sqliteBinding) toSQL(v sobek.Value) any {
	if v == nil || sobek.IsUndefined(v) || sobek.IsNull(v) {
		return nil
	}
	switch x := v.Export().(type) {
	case int64:
		return x
	case float64:
		if x == math.Trunc(x) && math.Abs(x) < 1<<53 {
			return int64(x)
		}
		return x
	case *big.Int:
		if !x.IsInt64() {
			panic(b.vm.NewTypeError("BigInt value is too large to bind"))
		}
		return x.Int64()
	case string:
		return x
	case []byte:
		return append([]byte(nil), x...)
	case sobek.ArrayBuffer:
		return append([]byte(nil), x.Bytes()...)
	}
	panic(b.vm.NewTypeError("SQLite3 can only bind numbers, strings, bigints, buffers, and null"))
}

func (b *sqliteBinding) toJS(v any, safeIntegers bool) any {
	switch x := v.(type) {
	case nil:
		return nil
	case int64:
		if safeIntegers {
			return big.NewInt(x)
		}
		return float64(x)
	case []byte:
		return b.vm.NewArrayBuffer(append([]byte(nil), x...))
	}
	return v
}

// run(id, sql, positional, named) -> {changes, lastInsertRowid}
func (b *sqliteBinding) run(id int64, q string, positional []sobek.Value, named sobek.Value) sobek.Value {
	res, err := b.db(id).conn.ExecContext(b.ctx, q, b.args(positional, named)...)
	if err != nil {
		b.throw(err)
	}
	changes, _ := res.RowsAffected()
	last, _ := res.LastInsertId()
	return b.vm.ToValue(map[string]any{"changes": changes, "lastInsertRowid": last})
}

// query(id, sql, positional, named, safeIntegers, limit) -> {columns, rows}
func (b *sqliteBinding) query(id int64, q string, positional []sobek.Value, named sobek.Value, safeIntegers bool, limit int) sobek.Value {
	var args []driver.NamedValue
	for i, a := range b.args(positional, named) {
		nv := driver.NamedValue{Ordinal: i + 1, Value: a}
		if n, ok := a.(sql.NamedArg); ok {
			nv.Name, nv.Value = n.Name, n.Value
		}
		args = append(args, nv)
	}
	var cols []string
	out := [][]any{}
	err := b.db(id).conn.Raw(func(dc any) error {
		dr, err := dc.(driver.QueryerContext).QueryContext(b.ctx, q, args)
		if err != nil {
			return err
		}
		defer dr.Close()
		cols = dr.Columns()
		dest := make([]driver.Value, len(cols))
		for limit <= 0 || len(out) < limit {
			if err := dr.Next(dest); err != nil {
				if errors.Is(err, io.EOF) {
					return nil
				}
				return err
			}
			vals := make([]any, len(cols))
			for i, v := range dest {
				// better-sqlite3 gives text as stored; modernc parses text in
				// DATE / DATETIME / TIMESTAMP columns into time.Time.
				if _, isTime := v.(time.Time); isTime {
					if text, ok := storedText(dr, i); ok {
						v = text
					}
				}
				vals[i] = b.toJS(v, safeIntegers)
			}
			out = append(out, vals)
		}
		return nil
	})
	if err != nil {
		b.throw(err)
	}
	return b.vm.ToValue(map[string]any{"columns": cols, "rows": out})
}

// columns(id, sql) describes a statement's result columns as
// better-sqlite3's statement.columns() does, from the prepared statement
// (modernc's ColumnInfo), without running it: name, column, table and
// database (null for expressions), type (the declared type, or null).
func (b *sqliteBinding) columns(id int64, q string) sobek.Value {
	var out []any
	err := b.db(id).conn.Raw(func(dc any) error {
		ci, ok := dc.(interface {
			ColumnInfo(string) ([]sqlite.ColumnInfo, error)
		})
		if !ok {
			return errors.New("og: modernc.org/sqlite has no ColumnInfo")
		}
		infos, err := ci.ColumnInfo(q)
		if err != nil {
			return err
		}
		orNull := func(s string) any {
			if s == "" {
				return nil
			}
			return s
		}
		for _, c := range infos {
			// In better-sqlite3's key order (a Go map's is random).
			o := b.vm.NewObject()
			_ = o.Set("name", c.Name)
			_ = o.Set("column", orNull(c.OriginName))
			_ = o.Set("table", orNull(c.TableName))
			_ = o.Set("database", orNull(c.DatabaseName))
			_ = o.Set("type", orNull(c.DeclType))
			out = append(out, o)
		}
		return nil
	})
	if err != nil {
		b.throw(err)
	}
	if out == nil {
		out = []any{}
	}
	return b.vm.ToValue(out)
}

// storedText is column i of the row modernc's driver rows stand on, as
// SQLite stores it, when it is text (sqlite3_column_type, then
// sqlite3_column_text). modernc keeps the statement handle and its TLS
// unexported (rows.pstmt, rows.c.tls) and offers no option to leave such
// text unparsed: they are read by reflection. The layout is checked once
// (sqliteLayout); TestSQLiteDateText fails if an upgrade moves them.
func storedText(dr driver.Rows, i int) (string, bool) {
	pstmt, tls, ok := sqliteHandles(dr)
	if !ok {
		panic("og: modernc.org/sqlite's rows no longer have pstmt / c.tls; see storedText")
	}
	if sqlite3.Xsqlite3_column_type(tls, pstmt, int32(i)) != sqlite3.SQLITE_TEXT {
		return "", false
	}
	p := sqlite3.Xsqlite3_column_text(tls, pstmt, int32(i))
	n := sqlite3.Xsqlite3_column_bytes(tls, pstmt, int32(i))
	if p == 0 || n <= 0 {
		return "", true
	}
	return string(libc.GoBytes(p, int(n))), true
}

// sqliteHandles reads the statement handle and TLS of modernc's rows.
func sqliteHandles(dr driver.Rows) (uintptr, *libc.TLS, bool) {
	rv := reflect.ValueOf(dr)
	if rv.Kind() != reflect.Pointer || rv.Elem().Kind() != reflect.Struct {
		return 0, nil, false
	}
	r := rv.Elem()
	pf, cf := r.FieldByName("pstmt"), r.FieldByName("c")
	if !pf.IsValid() || pf.Kind() != reflect.Uintptr || !cf.IsValid() || cf.Kind() != reflect.Pointer || cf.IsNil() {
		return 0, nil, false
	}
	tf := cf.Elem().FieldByName("tls")
	if !tf.IsValid() || tf.Type() != reflect.TypeOf((*libc.TLS)(nil)) {
		return 0, nil, false
	}
	pstmt := *(*uintptr)(unsafe.Pointer(pf.UnsafeAddr()))
	tls := *(**libc.TLS)(unsafe.Pointer(tf.UnsafeAddr()))
	return pstmt, tls, tls != nil
}

func (b *sqliteBinding) exec(id int64, q string) {
	if _, err := b.db(id).conn.ExecContext(b.ctx, q); err != nil {
		b.throw(err)
	}
}

func installSQLite(vm *sobek.Runtime, svc *SQLite) (*sobek.Object, func()) {
	b := &sqliteBinding{vm: vm, svc: svc, ctx: context.Background(), dbs: map[int64]*sqliteDB{}}
	o := vm.NewObject()
	_ = o.Set("open", b.open)
	_ = o.Set("close", b.close)
	_ = o.Set("exec", b.exec)
	_ = o.Set("run", b.run)
	_ = o.Set("query", b.query)
	_ = o.Set("columns", b.columns)
	_ = o.Set("isReader", isReader)
	return o, b.closeAll
}
