package natives

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/hex"
	"errors"
	"math/big"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	driver "github.com/go-sql-driver/mysql"
	"github.com/grafana/sobek"
	"github.com/moriyoshi/oganesson/runtime/engine"
)

// MySQL is the process-wide side of the mysql2 substitute: one database/sql
// pool per DSN, shared by every request engine, as the Postgres service is for
// pg/postgres.js. mysql2 code makes a fresh pool per request and ends it in a
// finally; the shim maps that onto the shared Go pool, which stays warm across
// requests (pool.end() releases the request's borrowed connections but leaves
// the pool open), so a second request reuses the first's parked connections.
type MySQL struct {
	mu  sync.Mutex
	dbs map[string]*sql.DB // by driver DSN
}

// NewMySQL returns an empty MySQL service.
func NewMySQL() *MySQL { return &MySQL{dbs: map[string]*sql.DB{}} }

// Close closes every pool.
func (s *MySQL) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, db := range s.dbs {
		_ = db.Close()
		delete(s.dbs, k)
	}
}

// get returns the shared pool for raw (a mysql:// URL or a go-sql-driver DSN),
// creating it the first time (capped at max connections, honored on creation).
// Idle connections are kept so the next request reuses them.
func (s *MySQL) get(raw string, max int) (*sql.DB, error) {
	dsn, err := mysqlDSN(raw)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if db, ok := s.dbs[dsn]; ok {
		return db, nil
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	if max > 0 {
		db.SetMaxOpenConns(max)
		db.SetMaxIdleConns(max)
	}
	db.SetConnMaxLifetime(0)
	s.dbs[dsn] = db
	return db, nil
}

// mysqlDSN converts a mysql:// (or mariadb://) URL to the DSN go-sql-driver
// parses; a DSN that is not such a URL is returned unchanged.
func mysqlDSN(raw string) (string, error) {
	if !strings.HasPrefix(raw, "mysql://") && !strings.HasPrefix(raw, "mariadb://") {
		return raw, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	cfg := driver.NewConfig()
	cfg.Net = "tcp"
	cfg.Addr = u.Host
	if u.User != nil {
		cfg.User = u.User.Username()
		cfg.Passwd, _ = u.User.Password()
	}
	cfg.DBName = strings.TrimPrefix(u.Path, "/")
	cfg.ParseTime = true
	for k, vs := range u.Query() {
		if len(vs) > 0 {
			if cfg.Params == nil {
				cfg.Params = map[string]string{}
			}
			cfg.Params[k] = vs[0]
		}
	}
	return cfg.FormatDSN(), nil
}

// myField is a result column (mysql2 exposes name; the type drives conversion).
type myField struct {
	name   string
	dbType string
	length int64 // column display/length, 0 when the driver does not report it
}

// myResult is one statement's result decoded off the loop. A SELECT carries
// positional rows and fields; a write carries affectedRows and insertId, which
// the shim turns into mysql2's ResultSetHeader.
type myResult struct {
	isSelect bool
	fields   []myField
	rows     [][]any
	affected int64
	insertId int64
	rowOpts  rowOptions // mysql2 result-formatting options (dateStrings, big numbers)
}

// rowOptions are the mysql2 options that shape result values, threaded to the
// host per query: dateStrings returns date columns as the MySQL literal string
// instead of a JS Date, and supportBigNumbers/bigNumberStrings return a BIGINT
// as a string (every one, or only those outside JS's safe-integer range)
// instead of a lossy number.
type rowOptions struct {
	dateStrings       dateStringSet
	supportBigNumbers bool
	bigNumberStrings  bool
	timezone          *time.Location // interprets DATE/DATETIME/TIMESTAMP wall-clocks; nil = UTC
	typeCast          sobek.Callable // mysql2 typeCast(field, next) callback, or nil
	typeCastRaw       bool           // typeCast: false -> every column as a raw Buffer
}

// bigNumberString reports whether a BIGINT value should be returned as a string
// rather than a number: with bigNumberStrings every one, otherwise only a value
// beyond JS's Number.MAX_SAFE_INTEGER (2^53 - 1), which a number cannot hold.
func (o rowOptions) bigNumberString(v any) bool {
	if !o.supportBigNumbers {
		return false
	}
	if o.bigNumberStrings {
		return true
	}
	switch n := v.(type) {
	case int64:
		return n > maxSafeInteger || n < -maxSafeInteger
	case uint64:
		return n > maxSafeInteger
	}
	return false
}

const maxSafeInteger = 1<<53 - 1

// dateStringSet is mysql2's dateStrings option: return DATE/DATETIME/TIMESTAMP
// columns as the MySQL literal string instead of a JS Date. `all` is true for
// `dateStrings: true`; `types` holds the uppercased type names for the array
// form (`dateStrings: ["DATE"]`).
type dateStringSet struct {
	all   bool
	types map[string]bool
}

// has reports whether a column of dbType should be returned as a string.
func (d dateStringSet) has(dbType string) bool {
	if d.all {
		return true
	}
	return d.types[strings.ToUpper(dbType)]
}

// execQuerier is satisfied by *sql.DB (pool) and *sql.Conn (pinned client).
type execQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// isSelectLike reports whether stmt returns a result set (mysql2 code never
// tells us; the first keyword does, as database/sql has no single call that
// both returns rows and a last-insert id).
func isSelectLike(stmt string) bool {
	s := strings.TrimLeft(stmt, " \t\r\n(")
	// Skip a leading line/block comment.
	for {
		if strings.HasPrefix(s, "--") {
			if i := strings.IndexByte(s, '\n'); i >= 0 {
				s = strings.TrimLeft(s[i+1:], " \t\r\n(")
				continue
			}
		}
		if strings.HasPrefix(s, "/*") {
			if i := strings.Index(s, "*/"); i >= 0 {
				s = strings.TrimLeft(s[i+2:], " \t\r\n(")
				continue
			}
		}
		break
	}
	word := s
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' || s[i] == '\n' || s[i] == '\r' || s[i] == '(' {
			word = s[:i]
			break
		}
	}
	switch strings.ToUpper(word) {
	case "SELECT", "SHOW", "DESCRIBE", "DESC", "EXPLAIN", "WITH", "CALL", "VALUES", "TABLE", "ANALYZE", "CHECK":
		return true
	}
	return false
}

func runMySQL(ctx context.Context, q execQuerier, stmt string, args []any) (*myResult, error) {
	if isSelectLike(stmt) {
		rows, err := q.QueryContext(ctx, stmt, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		cols, err := rows.Columns()
		if err != nil {
			return nil, err
		}
		cts, _ := rows.ColumnTypes()
		res := &myResult{isSelect: true, fields: make([]myField, len(cols))}
		for i, c := range cols {
			res.fields[i] = myField{name: c}
			if i < len(cts) {
				res.fields[i].dbType = cts[i].DatabaseTypeName()
				if l, ok := cts[i].Length(); ok {
					res.fields[i].length = l
				}
			}
		}
		for rows.Next() {
			dest := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range dest {
				ptrs[i] = &dest[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				return nil, err
			}
			res.rows = append(res.rows, dest)
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return res, nil
	}
	r, err := q.ExecContext(ctx, stmt, args...)
	if err != nil {
		return nil, err
	}
	res := &myResult{isSelect: false}
	res.affected, _ = r.RowsAffected()
	res.insertId, _ = r.LastInsertId()
	return res, nil
}

// myBinding is the per-engine host object. Pinned connections (a promise
// connection, or a transaction) are kept by an integer handle.
type myBinding struct {
	e     *engine.Engine
	vm    *sobek.Runtime
	svc   *MySQL
	ctx   context.Context
	conns map[int64]*sql.Conn
	next  int64
}

// jsError turns a Go error into a mysql2-shaped JS Error: the server's message,
// with code/errno/sqlState from a *mysql.MySQLError, so Drizzle/Kysely see the
// error they expect. sqlMessage (the server message without the "Error NNNN"
// prefix) is also set: mysql2 carries it, and @prisma/adapter-mariadb's
// isDriverError predicate requires errno + sqlMessage + sqlState together.
func (b *myBinding) jsError(err error) sobek.Value {
	o := b.vm.NewGoError(err)
	var myErr *driver.MySQLError
	if errors.As(err, &myErr) {
		_ = o.Set("code", mysqlCodeName(myErr.Number))
		_ = o.Set("errno", int64(myErr.Number))
		_ = o.Set("sqlState", string(myErr.SQLState[:]))
		_ = o.Set("sqlMessage", myErr.Message)
	}
	return o
}

// mysqlCodeName maps the common server error numbers to mysql2's string codes.
func mysqlCodeName(n uint16) string {
	switch n {
	case 1045:
		return "ER_ACCESS_DENIED_ERROR"
	case 1049:
		return "ER_BAD_DB_ERROR"
	case 1051:
		return "ER_BAD_TABLE_ERROR"
	case 1054:
		return "ER_BAD_FIELD_ERROR"
	case 1062:
		return "ER_DUP_ENTRY"
	case 1064:
		return "ER_PARSE_ERROR"
	case 1146:
		return "ER_NO_SUCH_TABLE"
	case 1452:
		return "ER_NO_REFERENCED_ROW_2"
	}
	return "ER_" + strconv.FormatUint(uint64(n), 10)
}

func (b *myBinding) params(v sobek.Value) []any {
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

func (b *myBinding) toGo(v sobek.Value) any {
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
	case *big.Int:
		// A JS BigInt bind value (@prisma/adapter-mariadb maps Int64 args to
		// BigInt). Bind as int64 when it fits, otherwise as its decimal string,
		// which MySQL accepts for a numeric column without precision loss.
		if x.IsInt64() {
			return x.Int64()
		}
		return x.String()
	}
	return v.String()
}

// jsVal maps a scanned value to the JS value mysql2's default types give for the
// column: integers to numbers (bigint included, as mysql2 does without
// supportBigNumbers), decimals to strings, text to strings, blobs to buffers,
// dates to Date.
func (b *myBinding) jsVal(dbType string, v any, opts rowOptions) any {
	if v == nil {
		return nil
	}
	switch strings.ToUpper(dbType) {
	case "DECIMAL", "NEWDECIMAL":
		return decimalString(v)
	case "TINYINT", "SMALLINT", "MEDIUMINT", "INT", "INTEGER", "BIGINT", "YEAR", "BIT":
		if strings.ToUpper(dbType) == "BIGINT" && opts.bigNumberString(v) {
			return decimalString(v)
		}
		if f, ok := toFloat(v); ok {
			return f
		}
		return decimalString(v)
	case "FLOAT", "DOUBLE":
		if f, ok := toFloat(v); ok {
			return f
		}
	case "DATE", "DATETIME", "TIMESTAMP":
		if t, ok := v.(time.Time); ok {
			if opts.dateStrings.has(dbType) {
				return mysqlDateString(strings.ToUpper(dbType), t)
			}
			// go-sql-driver parses these columns in UTC, so t's wall-clock is the
			// stored literal; mysql2's timezone decides which instant it names.
			loc := opts.timezone
			if loc == nil {
				loc = time.UTC
			}
			inst := time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), loc)
			if d, err := b.vm.New(b.vm.Get("Date"), b.vm.ToValue(float64(inst.UnixMilli()))); err == nil {
				return d
			}
		}
	case "BLOB", "TINYBLOB", "MEDIUMBLOB", "LONGBLOB", "BINARY", "VARBINARY", "GEOMETRY":
		if bb, ok := v.([]byte); ok {
			return b.vm.NewArrayBuffer(append([]byte(nil), bb...))
		}
	}
	switch x := v.(type) {
	case []byte:
		return string(x)
	case string:
		return x
	case int64:
		return float64(x)
	}
	return b.vm.ToValue(v)
}

// mysqlDateString formats a parsed time as the MySQL literal mysql2 returns for
// dateStrings: "YYYY-MM-DD" for DATE, "YYYY-MM-DD HH:MM:SS" for DATETIME and
// TIMESTAMP. go-sql-driver parses these columns in UTC (mysqlDSN sets no loc),
// so formatting in the value's own location reproduces the stored literal.
func mysqlDateString(dbType string, t time.Time) string {
	if dbType == "DATE" {
		return t.Format("2006-01-02")
	}
	return t.Format("2006-01-02 15:04:05")
}

// shape builds the host result: a ResultSetHeader for a write, or positional
// rows + fields for a SELECT (the shim builds mysql2's object rows, or Drizzle's
// rowsAsArray rows, in JS). Runs on the loop.
func (b *myBinding) shape(res *myResult) sobek.Value {
	vm := b.vm
	o := vm.NewObject()
	_ = o.Set("isSelect", res.isSelect)
	if !res.isSelect {
		_ = o.Set("affectedRows", res.affected)
		_ = o.Set("insertId", res.insertId)
		return o
	}
	fields := make([]any, len(res.fields))
	for i, f := range res.fields {
		fo := vm.NewObject()
		_ = fo.Set("name", f.name)
		fields[i] = fo
	}
	rows := make([]any, len(res.rows))
	for i, r := range res.rows {
		vals := make([]any, len(r))
		for j := range r {
			var f myField
			if j < len(res.fields) {
				f = res.fields[j]
			}
			switch {
			case res.rowOpts.typeCast != nil:
				vals[j] = b.typeCastCell(f, r[j], res.rowOpts)
			case res.rowOpts.typeCastRaw:
				vals[j] = b.rawBuffer(r[j]) // typeCast: false -> raw Buffer
			default:
				vals[j] = b.jsVal(f.dbType, r[j], res.rowOpts)
			}
		}
		rows[i] = vm.ToValue(vals)
	}
	_ = o.Set("fields", vm.ToValue(fields))
	_ = o.Set("rows", vm.ToValue(rows))
	return o
}

// mysql2TypeName maps a go-sql-driver DatabaseTypeName to the type name mysql2
// exposes as field.type inside a typeCast callback (its protocol type-code name,
// e.g. INT -> "LONG", BIGINT -> "LONGLONG", VARCHAR -> "VAR_STRING"). An
// unmapped type passes through uppercased.
func mysql2TypeName(dbType string) string {
	switch strings.ToUpper(dbType) {
	case "DECIMAL", "NEWDECIMAL":
		return "NEWDECIMAL"
	case "TINYINT":
		return "TINY"
	case "SMALLINT":
		return "SHORT"
	case "MEDIUMINT":
		return "INT24"
	case "INT", "INTEGER":
		return "LONG"
	case "BIGINT":
		return "LONGLONG"
	case "FLOAT":
		return "FLOAT"
	case "DOUBLE":
		return "DOUBLE"
	case "DATE":
		return "DATE"
	case "DATETIME":
		return "DATETIME"
	case "TIMESTAMP":
		return "TIMESTAMP"
	case "TIME":
		return "TIME"
	case "YEAR":
		return "YEAR"
	case "VARCHAR":
		return "VAR_STRING"
	case "CHAR":
		return "STRING"
	case "TEXT":
		return "BLOB"
	case "TINYTEXT", "TINYBLOB":
		return "TINY_BLOB"
	case "MEDIUMTEXT", "MEDIUMBLOB":
		return "MEDIUM_BLOB"
	case "LONGTEXT", "LONGBLOB":
		return "LONG_BLOB"
	case "BLOB":
		return "BLOB"
	case "BINARY":
		return "STRING"
	case "VARBINARY":
		return "VAR_STRING"
	case "BIT":
		return "BIT"
	case "JSON":
		return "JSON"
	case "ENUM":
		return "ENUM"
	case "SET":
		return "SET"
	case "GEOMETRY":
		return "GEOMETRY"
	}
	return strings.ToUpper(dbType)
}

// fieldString is the raw column value as a string, for a typeCast field's
// .string() (mysql2 returns the undecoded column text).
func fieldString(dbType string, v any) string {
	switch x := v.(type) {
	case []byte:
		return string(x)
	case string:
		return x
	case time.Time:
		return mysqlDateString(strings.ToUpper(dbType), x)
	case bool:
		if x {
			return "1"
		}
		return "0"
	}
	return decimalString(v)
}

// jsBuffer wraps bytes as a Node Buffer when the shim installs one, else a
// Uint8Array (via an ArrayBuffer).
func (b *myBinding) jsBuffer(data []byte) sobek.Value {
	ab := b.vm.NewArrayBuffer(data)
	if ctor := b.vm.Get("Buffer"); ctor != nil && !sobek.IsUndefined(ctor) {
		if from, ok := sobek.AssertFunction(ctor.ToObject(b.vm).Get("from")); ok {
			if buf, err := from(ctor, b.vm.ToValue(ab)); err == nil {
				return buf
			}
		}
	}
	return b.vm.ToValue(ab)
}

// rawBuffer returns a value as a Buffer (mysql2 typeCast: false gives every
// column as its raw bytes); a nil stays null.
func (b *myBinding) rawBuffer(v any) any {
	if v == nil {
		return nil
	}
	if bb, ok := v.([]byte); ok {
		return b.jsBuffer(append([]byte(nil), bb...))
	}
	return b.jsBuffer([]byte(fieldString("", v)))
}

// typeCastCell runs mysql2's typeCast(field, next) callback for one column. The
// field exposes { type, name, length, db, table, string(), buffer() } and next
// returns og's default conversion (which still honors dateStrings / big numbers
// / timezone). Runs on the loop, where invoking the JS callback is safe.
func (b *myBinding) typeCastCell(f myField, v any, opts rowOptions) any {
	vm := b.vm
	field := vm.NewObject()
	_ = field.Set("type", mysql2TypeName(f.dbType))
	_ = field.Set("name", f.name)
	_ = field.Set("length", f.length)
	_ = field.Set("db", "")
	_ = field.Set("table", "")
	_ = field.Set("string", func(sobek.FunctionCall) sobek.Value {
		if v == nil {
			return sobek.Null()
		}
		return vm.ToValue(fieldString(f.dbType, v))
	})
	_ = field.Set("buffer", func(sobek.FunctionCall) sobek.Value {
		if v == nil {
			return sobek.Null()
		}
		if bb, ok := v.([]byte); ok {
			return b.jsBuffer(append([]byte(nil), bb...))
		}
		return b.jsBuffer([]byte(fieldString(f.dbType, v)))
	})
	// next(): the default conversion, without re-entering typeCast.
	def := opts
	def.typeCast = nil
	def.typeCastRaw = false
	next := func(sobek.FunctionCall) sobek.Value {
		return vm.ToValue(b.jsVal(f.dbType, v, def))
	}
	out, err := opts.typeCast(sobek.Undefined(), field, vm.ToValue(next))
	if err != nil {
		panic(err)
	}
	return out
}

// asyncWith runs a query off the loop and settles a JS promise with shape(res).
func (b *myBinding) asyncWith(shape func(*myResult) sobek.Value, work func() (*myResult, error)) sobek.Value {
	vm := b.vm
	promise, resolve, reject := vm.NewPromise()
	done := b.e.Loop.Async()
	go func() {
		res, err := work()
		done(func(vm *sobek.Runtime) error {
			if err != nil {
				return reject(b.jsError(err))
			}
			return resolve(shape(res))
		})
	}()
	return vm.ToValue(promise)
}

func (b *myBinding) async(work func() (*myResult, error)) sobek.Value {
	return b.asyncWith(b.shape, work)
}

func (b *myBinding) queryPool(dsn string, max int64, stmt string, params, rowOpts sobek.Value) sobek.Value {
	args := b.params(params)
	opts := b.parseRowOpts(rowOpts)
	return b.async(func() (*myResult, error) {
		db, err := b.svc.get(dsn, int(max))
		if err != nil {
			return nil, err
		}
		res, err := runMySQL(b.ctx, db, stmt, args)
		if res != nil {
			res.rowOpts = opts
		}
		return res, err
	})
}

func (b *myBinding) acquire(dsn string, max int64) sobek.Value {
	vm := b.vm
	promise, resolve, reject := vm.NewPromise()
	done := b.e.Loop.Async()
	go func() {
		db, err := b.svc.get(dsn, int(max))
		var conn *sql.Conn
		if err == nil {
			conn, err = db.Conn(b.ctx)
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

func (b *myBinding) queryClient(id int64, stmt string, params, rowOpts sobek.Value) sobek.Value {
	args := b.params(params)
	opts := b.parseRowOpts(rowOpts)
	conn := b.conns[id]
	return b.async(func() (*myResult, error) {
		if conn == nil {
			return nil, errors.New("connection was released before use")
		}
		res, err := runMySQL(b.ctx, conn, stmt, args)
		if res != nil {
			res.rowOpts = opts
		}
		return res, err
	})
}

// parseRowOpts reads the mysql2 result-formatting options object the shim
// passes per query ({ dateStrings, supportBigNumbers, bigNumberStrings }).
func (b *myBinding) parseRowOpts(v sobek.Value) rowOptions {
	if v == nil || sobek.IsUndefined(v) || sobek.IsNull(v) {
		return rowOptions{}
	}
	o := v.ToObject(b.vm)
	opts := rowOptions{
		dateStrings:       b.parseDateStrings(o.Get("dateStrings")),
		supportBigNumbers: boolOpt(o.Get("supportBigNumbers")),
		bigNumberStrings:  boolOpt(o.Get("bigNumberStrings")),
		timezone:          parseMySQLTimezone(optString(o.Get("timezone"))),
	}
	if tc := o.Get("typeCast"); tc != nil && !sobek.IsUndefined(tc) && !sobek.IsNull(tc) {
		if fn, ok := sobek.AssertFunction(tc); ok {
			opts.typeCast = fn
		} else if b, ok := tc.Export().(bool); ok && !b {
			opts.typeCastRaw = true // typeCast: false
		}
	}
	return opts
}

// optString reads an optional JS string ("" when missing/undefined/null).
func optString(v sobek.Value) string {
	if v == nil || sobek.IsUndefined(v) || sobek.IsNull(v) {
		return ""
	}
	return v.String()
}

// parseMySQLTimezone maps mysql2's timezone option to a time.Location used to
// interpret DATE/DATETIME/TIMESTAMP wall-clocks: "" or "Z" is UTC (og's
// baseline, since go-sql-driver parses these columns in UTC), "local" is the
// runtime's zone, and "+HH:MM" / "-HH:MM" a fixed offset. An unparseable value
// falls back to UTC.
func parseMySQLTimezone(tz string) *time.Location {
	switch {
	case tz == "" || tz == "Z" || tz == "z":
		return time.UTC
	case tz == "local" || tz == "Local":
		return time.Local
	case len(tz) == 6 && (tz[0] == '+' || tz[0] == '-') && tz[3] == ':':
		h, err1 := strconv.Atoi(tz[1:3])
		m, err2 := strconv.Atoi(tz[4:6])
		if err1 != nil || err2 != nil {
			return time.UTC
		}
		off := (h*60 + m) * 60
		if tz[0] == '-' {
			off = -off
		}
		return time.FixedZone(tz, off)
	}
	return time.UTC
}

// boolOpt reads an optional JS boolean (a missing/undefined/null value is false).
func boolOpt(v sobek.Value) bool {
	return v != nil && !sobek.IsUndefined(v) && !sobek.IsNull(v) && v.ToBoolean()
}

// parseDateStrings reads mysql2's dateStrings option: true (every date column),
// an array of type names, or absent/false (none).
func (b *myBinding) parseDateStrings(v sobek.Value) dateStringSet {
	if v == nil || sobek.IsUndefined(v) || sobek.IsNull(v) {
		return dateStringSet{}
	}
	if on, ok := v.Export().(bool); ok {
		return dateStringSet{all: on}
	}
	o := v.ToObject(b.vm)
	n := int(o.Get("length").ToInteger())
	types := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		types[strings.ToUpper(o.Get(strconv.Itoa(i)).String())] = true
	}
	return dateStringSet{types: types}
}

func (b *myBinding) release(id int64) {
	if c, ok := b.conns[id]; ok {
		_ = c.Close()
		delete(b.conns, id)
	}
}

func (b *myBinding) cleanup() {
	for id, c := range b.conns {
		_ = c.Close()
		delete(b.conns, id)
	}
}

// registerTLS registers a go-sql-driver TLS config that verifies the server
// against caPEM (mysql2's ssl: { ca, rejectUnauthorized: true }) and returns
// the DSN `tls=<key>` name to use. The key is a content hash, so an identical
// ssl option reuses the same registration across request VMs (go-sql-driver's
// registry is process-wide) and distinct ones never collide. go-sql-driver
// fills an empty ServerName with the connection host at dial time.
func (b *myBinding) registerTLS(caPEM, serverName string) string {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM([]byte(caPEM)) {
		panic(b.vm.NewTypeError("mysql2: ssl.ca contains no valid certificate"))
	}
	sum := sha256.Sum256([]byte(serverName + "\x00" + caPEM))
	key := "og-" + hex.EncodeToString(sum[:8])
	if err := driver.RegisterTLSConfig(key, &tls.Config{RootCAs: pool, ServerName: serverName, MinVersion: tls.VersionTLS12}); err != nil {
		panic(b.vm.NewGoError(err))
	}
	return key
}

// mariaDateTimeString formats a DATETIME/TIMESTAMP as the literal string the
// mariadb driver returns under dateStrings (go-sql-driver parses these columns
// in UTC, so the wall-clock is the stored value). Fractional seconds are
// included only when present; @prisma/adapter-mariadb's mapRow reparses it as
// a UTC Date, so millisecond precision (Prisma's DATETIME(3) default) survives.
func mariaDateTimeString(t time.Time) string {
	if t.Nanosecond() == 0 {
		return t.Format("2006-01-02 15:04:05")
	}
	return t.Format("2006-01-02 15:04:05.999999")
}

// mariaVal maps a scanned value to the JS value the mariadb driver would
// deliver to @prisma/adapter-mariadb under its fixed options (rowsAsArray,
// dateStrings, autoJsonMap:false/jsonStrings, bitOneIsBoolean:false): an Int32
// column is a number, an Int64 column (BIGINT, unsigned INT) a string the
// adapter forwards verbatim, DECIMAL a string, a date the MySQL literal string,
// a binary/BIT/GEOMETRY column a Buffer, and text/JSON/ENUM a string. The
// dbType is go-sql-driver's DatabaseTypeName, which already encodes signedness
// ("UNSIGNED INT") and text-vs-binary ("TEXT" vs "BLOB").
func (b *myBinding) mariaVal(dbType string, v any) any {
	if v == nil {
		return nil
	}
	up := strings.ToUpper(dbType)
	unsigned := strings.HasPrefix(up, "UNSIGNED ")
	switch strings.TrimPrefix(up, "UNSIGNED ") {
	case "TINYINT", "SMALLINT", "MEDIUMINT", "YEAR":
		if f, ok := toFloat(v); ok {
			return f
		}
		return decimalString(v)
	case "INT", "INTEGER":
		// Unsigned INT is Int64 to the adapter (up to 2^32-1): a string keeps
		// values above 2^31 exact; signed INT fits a JS number.
		if unsigned {
			return decimalString(v)
		}
		if f, ok := toFloat(v); ok {
			return f
		}
		return decimalString(v)
	case "BIGINT":
		return decimalString(v) // Int64 -> string (exact beyond 2^53)
	case "FLOAT", "DOUBLE":
		if f, ok := toFloat(v); ok {
			return f
		}
	case "DECIMAL", "NEWDECIMAL":
		return decimalString(v)
	case "DATE":
		if t, ok := v.(time.Time); ok {
			return t.Format("2006-01-02")
		}
	case "DATETIME", "TIMESTAMP":
		if t, ok := v.(time.Time); ok {
			return mariaDateTimeString(t)
		}
	case "BIT", "BLOB", "TINYBLOB", "MEDIUMBLOB", "LONGBLOB", "BINARY", "VARBINARY", "GEOMETRY":
		if bb, ok := v.([]byte); ok {
			return b.jsBuffer(append([]byte(nil), bb...))
		}
	}
	// TIME, TEXT/VARCHAR/CHAR/ENUM/SET and JSON: strings.
	switch x := v.(type) {
	case []byte:
		return string(x)
	case string:
		return x
	case int64:
		return float64(x)
	}
	return b.vm.ToValue(v)
}

// shapeMaria builds the host result for the mariadb driver shim (js/mariadb.js):
// a write carries affectedRows + insertId; a SELECT carries positional rows
// (mariaVal-converted) and fields exposing name + go-sql-driver dbType + length,
// from which js/mariadb.js derives the driver's field.type/flags/collation.
func (b *myBinding) shapeMaria(res *myResult) sobek.Value {
	vm := b.vm
	o := vm.NewObject()
	_ = o.Set("isSelect", res.isSelect)
	if !res.isSelect {
		_ = o.Set("affectedRows", res.affected)
		_ = o.Set("insertId", res.insertId)
		return o
	}
	fields := make([]any, len(res.fields))
	for i, f := range res.fields {
		fo := vm.NewObject()
		_ = fo.Set("name", f.name)
		_ = fo.Set("dbType", f.dbType)
		_ = fo.Set("length", f.length)
		fields[i] = fo
	}
	rows := make([]any, len(res.rows))
	for i, r := range res.rows {
		vals := make([]any, len(r))
		for j := range r {
			dbType := ""
			if j < len(res.fields) {
				dbType = res.fields[j].dbType
			}
			vals[j] = b.mariaVal(dbType, r[j])
		}
		rows[i] = vm.ToValue(vals)
	}
	_ = o.Set("fields", vm.ToValue(fields))
	_ = o.Set("rows", vm.ToValue(rows))
	return o
}

// queryPoolMaria runs a one-off mariadb-shim query on the shared pool.
func (b *myBinding) queryPoolMaria(dsn string, max int64, stmt string, params sobek.Value) sobek.Value {
	args := b.params(params)
	return b.asyncWith(b.shapeMaria, func() (*myResult, error) {
		db, err := b.svc.get(dsn, int(max))
		if err != nil {
			return nil, err
		}
		return runMySQL(b.ctx, db, stmt, args)
	})
}

// queryClientMaria runs a mariadb-shim query on a pinned connection (a
// transaction spans one).
func (b *myBinding) queryClientMaria(id int64, stmt string, params sobek.Value) sobek.Value {
	args := b.params(params)
	conn := b.conns[id]
	return b.asyncWith(b.shapeMaria, func() (*myResult, error) {
		if conn == nil {
			return nil, errors.New("connection was released before use")
		}
		return runMySQL(b.ctx, conn, stmt, args)
	})
}

func installMySQL(e *engine.Engine, svc *MySQL) (*sobek.Object, func()) {
	b := &myBinding{e: e, vm: e.VM, svc: svc, ctx: context.Background(), conns: map[int64]*sql.Conn{}}
	o := e.VM.NewObject()
	_ = o.Set("queryPool", b.queryPool)
	_ = o.Set("acquire", b.acquire)
	_ = o.Set("queryClient", b.queryClient)
	_ = o.Set("release", b.release)
	_ = o.Set("registerTLS", b.registerTLS)
	// @prisma/adapter-mariadb's driver shim (js/mariadb.js) shares the acquire/
	// release/registerTLS surface but needs richer field metadata and
	// driver-faithful value conversion, so it has its own query entry points.
	_ = o.Set("queryPoolMaria", b.queryPoolMaria)
	_ = o.Set("queryClientMaria", b.queryClientMaria)
	return o, b.cleanup
}
