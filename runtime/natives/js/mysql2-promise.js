// Oganesson's mysql2/promise: the promise API on MySQL in Go
// (runtime/natives/mysql.go, via go-sql-driver/mysql). The database/sql pool is
// shared by every request of the process; a JS Pool borrows connections from it
// and its end() releases them without closing the shared pool.
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

// dsn produces the mysql:// URL the Go service parses (runtime/natives/mysql.go
// converts it to a go-sql-driver DSN). mysql2 accepts a uri or discrete
// host/user/... fields; pass the uri through, or assemble one.
function dsn(config) {
  config = config || {};
  let d;
  if (config.uri) d = config.uri;
  else if (config.connectionString) d = config.connectionString;
  else {
    const user = encodeURIComponent(config.user != null ? String(config.user) : "root");
    const auth = config.password != null ? `${user}:${encodeURIComponent(String(config.password))}` : user;
    const host = config.host || "localhost";
    const port = config.port || 3306;
    const db = config.database != null ? "/" + encodeURIComponent(String(config.database)) : "/";
    d = `mysql://${auth}@${host}:${port}${db}`;
  }
  return withTLS(d, config.ssl);
}

// withTLS maps mysql2's `ssl` option to go-sql-driver's `tls` DSN param (which
// mysqlDSN forwards). A falsy/absent `ssl` is left alone (no TLS). An `ssl`
// object with a `ca` and `rejectUnauthorized` not false verifies the server
// against that CA (the host registers a TLS config and returns its name);
// otherwise `tls=skip-verify` encrypts without verification (the baseline,
// matching pg's sslmode=require).
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

// norm handles query(sql, values) and query({ sql, values, rowsAsArray }).
function norm(sqlOrConfig, values) {
  if (sqlOrConfig && typeof sqlOrConfig === "object") {
    return { sql: sqlOrConfig.sql, params: sqlOrConfig.values ?? values ?? null, rowsAsArray: !!sqlOrConfig.rowsAsArray };
  }
  return { sql: sqlOrConfig, params: values ?? null, rowsAsArray: false };
}

// result turns the host result into what mysql2 returns: [rowsOrOkPacket,
// fields]. A read gives object rows (or positional rows when rowsAsArray); a
// write gives an OkPacket.
function result(raw, rowsAsArray) {
  if (!raw.isSelect) {
    return [{ affectedRows: raw.affectedRows, insertId: raw.insertId, changedRows: 0, fieldCount: 0, warningStatus: 0 }, undefined];
  }
  const fields = raw.fields;
  const rows = raw.rows.map((vals) => {
    if (rowsAsArray) return vals.map(wrap);
    const o = {};
    for (let i = 0; i < fields.length; i++) o[fields[i].name] = wrap(vals[i]);
    return o;
  });
  return [rows, fields];
}

class Connection {
  constructor(id, dsnStr, max, rowOpts) {
    this._id = id;
    this._dsn = dsnStr;
    this._max = max;
    this._rowOpts = rowOpts;
    // Drizzle reaches through .connection for the raw query([rows, fields]).
    this.connection = {
      query: (sqlOrConfig, values) => this.query(sqlOrConfig, values),
      execute: (sqlOrConfig, values) => this.execute(sqlOrConfig, values),
    };
  }
  async _run(sqlOrConfig, values) {
    const q = norm(sqlOrConfig, values);
    const raw = this._id != null
      ? await host().queryClient(this._id, q.sql, q.params, this._rowOpts)
      : await host().queryPool(this._dsn, this._max, q.sql, q.params, this._rowOpts);
    return result(raw, q.rowsAsArray);
  }
  query(sqlOrConfig, values) {
    return this._run(sqlOrConfig, values);
  }
  execute(sqlOrConfig, values) {
    return this._run(sqlOrConfig, values);
  }
  async beginTransaction() {
    await host().queryClient(this._id, "START TRANSACTION", null);
  }
  async commit() {
    await host().queryClient(this._id, "COMMIT", null);
  }
  async rollback() {
    await host().queryClient(this._id, "ROLLBACK", null);
  }
  release() {
    if (this._id != null) {
      host().release(this._id);
      this._id = null;
    }
  }
  async end() {
    this.release();
  }
  on() {
    return this;
  }
  removeListener() {
    return this;
  }
}

class Pool {
  constructor(config = {}) {
    this._dsn = dsn(config);
    this._max = config.connectionLimit > 0 ? config.connectionLimit : 10;
    this._rowOpts = rowOptsOf(config);
  }
  async query(sqlOrConfig, values) {
    const q = norm(sqlOrConfig, values);
    return result(await host().queryPool(this._dsn, this._max, q.sql, q.params, this._rowOpts), q.rowsAsArray);
  }
  execute(sqlOrConfig, values) {
    return this.query(sqlOrConfig, values);
  }
  async getConnection() {
    const id = await host().acquire(this._dsn, this._max);
    return new Connection(id, this._dsn, this._max, this._rowOpts);
  }
  async end() {
    // The shared Go pool stays warm for the next request.
  }
  on() {
    return this;
  }
  removeListener() {
    return this;
  }
}

async function createConnection(config) {
  return new Connection(null, dsn(config), config && config.connectionLimit > 0 ? config.connectionLimit : 10, rowOptsOf(config));
}

function createPool(config) {
  return new Pool(config);
}

const mysql = { createPool, createConnection, Pool, Connection };
export default mysql;
export { createPool, createConnection, Pool, Connection };
