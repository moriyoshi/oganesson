// Oganesson's mysql2 (the callback API) on MySQL in Go (runtime/natives/mysql.go,
// via go-sql-driver), read from Symbol.for("og.mysql"). Kysely's MysqlDialect uses
// this callback form; Drizzle uses mysql2/promise (js/mysql2-promise.js). Both
// share the one Go pool, warm across requests.
const host = () => {
  const h = globalThis[Symbol.for("og.mysql")];
  if (!h) throw new Error("mysql2: Oganesson's MySQL service is not installed in this runtime");
  return h;
};

const toBuffer = (ab) => (globalThis.Buffer ? globalThis.Buffer.from(ab) : new Uint8Array(ab));
const wrap = (v) => (v instanceof ArrayBuffer ? toBuffer(v) : v);

// rowOptsOf collects the mysql2 options that shape result values, passed to the
// host per query: dateStrings and supportBigNumbers/bigNumberStrings.
const rowOptsOf = (o) => ({
  dateStrings: (o || {}).dateStrings,
  supportBigNumbers: (o || {}).supportBigNumbers,
  bigNumberStrings: (o || {}).bigNumberStrings,
  timezone: (o || {}).timezone,
  typeCast: (o || {}).typeCast,
});

function normalize(sqlOrConfig, params) {
  if (sqlOrConfig && typeof sqlOrConfig === "object") {
    return { sql: sqlOrConfig.sql, values: sqlOrConfig.values || params || null, rowsAsArray: !!sqlOrConfig.rowsAsArray };
  }
  return { sql: sqlOrConfig, values: params || null, rowsAsArray: false };
}

// shape returns the single result mysql2's callback delivers as its second
// argument: a ResultSetHeader for a write, an array of rows for a SELECT. (The
// fields go as the third argument.)
function shape(raw, rowsAsArray) {
  if (!raw.isSelect) {
    const header = {
      fieldCount: 0,
      affectedRows: raw.affectedRows,
      insertId: raw.insertId,
      info: "",
      serverStatus: 2,
      warningStatus: 0,
      changedRows: 0,
    };
    return { result: header, fields: undefined };
  }
  const fields = raw.fields;
  const rows = raw.rows.map((vals) => {
    if (rowsAsArray) return vals.map(wrap);
    const o = {};
    for (let i = 0; i < fields.length; i++) o[fields[i].name] = wrap(vals[i]);
    return o;
  });
  return { result: rows, fields: fields.map((f) => ({ name: f.name })) };
}

// splitArgs separates mysql2's (sql[, params][, callback]) call forms.
function splitArgs(paramsOrCb, cb) {
  if (typeof paramsOrCb === "function") return { params: null, cb: paramsOrCb };
  return { params: paramsOrCb || null, cb: cb };
}

function runCb(promise, rowsAsArray, cb) {
  promise.then(
    (raw) => {
      const s = shape(raw, rowsAsArray);
      if (cb) cb(null, s.result, s.fields);
    },
    (err) => {
      if (cb) cb(err);
    },
  );
}

class Connection {
  constructor(id, pool) {
    this._id = id;
    this._pool = pool;
    this.threadId = id;
  }
  query(sqlOrConfig, paramsOrCb, cb) {
    const q = normalize(sqlOrConfig, typeof paramsOrCb === "function" ? null : paramsOrCb);
    const { cb: callback } = splitArgs(paramsOrCb, cb);
    runCb(Promise.resolve(host().queryClient(this._id, q.sql, q.values, this._pool && this._pool._rowOpts)), q.rowsAsArray, callback);
    return undefined;
  }
  execute(sqlOrConfig, paramsOrCb, cb) {
    return this.query(sqlOrConfig, paramsOrCb, cb);
  }
  release() {
    if (this._id != null) {
      host().release(this._id);
      if (this._pool) this._pool._clients.delete(this);
      this._id = null;
    }
  }
  destroy() {
    this.release();
  }
  rollback(cb) {
    return this.query("ROLLBACK", cb);
  }
  commit(cb) {
    return this.query("COMMIT", cb);
  }
  beginTransaction(cb) {
    return this.query("BEGIN", cb);
  }
  on() {
    return this;
  }
  removeListener() {
    return this;
  }
}

class Pool {
  constructor(options = {}) {
    this._dsn = dsn(options);
    this._max = options.connectionLimit > 0 ? options.connectionLimit : 10;
    this._rowOpts = rowOptsOf(options);
    this._clients = new Set();
  }
  getConnection(cb) {
    Promise.resolve(host().acquire(this._dsn, this._max)).then(
      (id) => {
        const c = new Connection(id, this);
        this._clients.add(c);
        cb(null, c);
      },
      (err) => cb(err),
    );
  }
  query(sqlOrConfig, paramsOrCb, cb) {
    const q = normalize(sqlOrConfig, typeof paramsOrCb === "function" ? null : paramsOrCb);
    const { cb: callback } = splitArgs(paramsOrCb, cb);
    runCb(Promise.resolve(host().queryPool(this._dsn, this._max, q.sql, q.values, this._rowOpts)), q.rowsAsArray, callback);
    return undefined;
  }
  execute(sqlOrConfig, paramsOrCb, cb) {
    return this.query(sqlOrConfig, paramsOrCb, cb);
  }
  end(cb) {
    // The shared Go pool stays warm; hand back any borrowed connections.
    for (const c of [...this._clients]) c.release();
    if (typeof cb === "function") cb(null);
  }
  on() {
    return this;
  }
  removeListener() {
    return this;
  }
  // mysql2/promise wraps a callback pool; expose promise() for code that does.
  promise() {
    return this;
  }
}

function dsn(o) {
  o = o || {};
  let d;
  if (o.uri) {
    d = o.uri;
  } else {
    const user = encodeURIComponent(o.user || "root");
    const auth = o.password != null ? user + ":" + encodeURIComponent(String(o.password)) : user;
    const host = o.host || o.socketPath || "localhost";
    const port = o.port || 3306;
    const db = o.database != null ? "/" + encodeURIComponent(o.database) : "";
    d = `mysql://${auth}@${host}:${port}${db}`;
  }
  return withTLS(withOptions(d, o), o.ssl);
}

// withOptions maps mysql2's discrete connection options onto the mysql:// URL
// the host converts to a go-sql-driver DSN (mysql.go mysqlDSN). A query param
// go-sql-driver recognises (multiStatements, timeout, charset) is routed to its
// config field when the driver re-parses the formatted DSN, so these need no Go
// change: multipleStatements -> multiStatements, connectTimeout (ms) -> timeout
// (a Go duration string), charset passes through. Each is appended only when
// absent, so a value already in a uri wins.
function withOptions(d, o) {
  const params = [];
  const has = (k) => new RegExp("[?&]" + k + "=").test(d);
  const add = (k, v) => {
    if (v != null && v !== false && !has(k)) params.push(k + "=" + encodeURIComponent(String(v)));
  };
  if (o.multipleStatements) add("multiStatements", "true");
  if (typeof o.connectTimeout === "number" && o.connectTimeout > 0) add("timeout", o.connectTimeout + "ms");
  add("charset", o.charset);
  if (params.length === 0) return d;
  return d + (d.includes("?") ? "&" : "?") + params.join("&");
}

// withTLS maps mysql2's `ssl` option to go-sql-driver's `tls` DSN param. A
// falsy `ssl` is left alone (no TLS). An `ssl` object with a `ca` and
// `rejectUnauthorized` not false verifies the server against that CA (the host
// registers a TLS config and returns its name); otherwise `tls=skip-verify`
// encrypts without verification (the baseline, matching pg's sslmode=require).
function withTLS(d, ssl) {
  if (!ssl || /[?&]tls=/.test(d)) return d;
  return d + (d.includes("?") ? "&" : "?") + "tls=" + encodeURIComponent(tlsParam(ssl));
}

// tlsParam is the go-sql-driver TLS name for an `ssl` option.
function tlsParam(ssl) {
  if (ssl && typeof ssl === "object" && ssl.ca && ssl.rejectUnauthorized !== false) {
    const ca = Array.isArray(ssl.ca) ? ssl.ca.join("\n") : String(ssl.ca);
    return host().registerTLS(ca, ssl.servername || ssl.host || "");
  }
  return "skip-verify";
}

function createPool(options) {
  return new Pool(options);
}
function createConnection(options, cb) {
  const pool = new Pool(options);
  if (typeof cb === "function") {
    pool.getConnection(cb);
    return;
  }
  return pool;
}

const mysql = { createPool, createConnection, Pool, Connection };
export default mysql;
export { createPool, createConnection };
