// Oganesson's mariadb driver on MySQL in Go (runtime/natives/mysql.go, via
// go-sql-driver), read from Symbol.for("og.mysql") — the same shared Go pool
// mysql2 uses, warm across requests. @prisma/adapter-mariadb drives this (it
// imports `mariadb` and calls createPool), so Prisma's MySQL query and migrate
// paths work without the native driver. Only the field metadata and value
// conversion differ from mysql2's; the host exposes queryPoolMaria/
// queryClientMaria for those, and reuses acquire/release/registerTLS.
const host = () => {
  const h = globalThis[Symbol.for("og.mysql")];
  if (!h) throw new Error("mariadb: Oganesson's MySQL service is not installed in this runtime");
  return h;
};

// go-sql-driver DSN query params the host (mysqlDSN) forwards to go-sql-driver.
// Any other param becomes a server SET and errors, so a mariadb-driver-only
// option (prepareCacheLength, connectionLimit, …) that rode in on a connection
// string is dropped here.
const GO_DSN_PARAMS = new Set([
  "allowAllFiles", "allowCleartextPasswords", "allowFallbackToPlaintext", "allowNativePasswords",
  "allowOldPasswords", "charset", "checkConnLiveness", "collation", "clientFoundRows",
  "columnsWithAlias", "interpolateParams", "loc", "time_zone", "timeTruncate", "maxAllowedPacket",
  "multiStatements", "parseTime", "readTimeout", "rejectReadOnly", "serverPubKey", "timeout",
  "tls", "writeTimeout", "connectionAttributes",
]);

// toDSN turns the adapter's config (a connection string or a settings object)
// into the mysql:// URL the host converts to a go-sql-driver DSN. A connection
// string is handed to the host as-is (it parses it, as pg.js does for pg),
// except its query params are filtered to the ones go-sql-driver recognizes:
// the adapter appends prepareCacheLength, and a Prisma MySQL URL may carry
// connection_limit/sslaccept/… — all mariadb-/Prisma-level options go-sql-driver
// would reject as unknown server variables.
function toDSN(config) {
  if (typeof config === "string") {
    const i = config.indexOf("?");
    if (i < 0) return config;
    const kept = config
      .slice(i + 1)
      .split("&")
      .filter((p) => p && GO_DSN_PARAMS.has(p.split("=")[0]));
    return kept.length ? config.slice(0, i) + "?" + kept.join("&") : config.slice(0, i);
  }
  const o = config || {};
  if (o.uri) return withTLS(o.uri, o.ssl);
  const user = encodeURIComponent(o.user || "root");
  const auth = o.password != null ? user + ":" + encodeURIComponent(String(o.password)) : user;
  const h = o.host || o.socketPath || "localhost";
  const port = o.port || 3306;
  const db = o.database != null ? "/" + encodeURIComponent(o.database) : "";
  return withTLS(`mysql://${auth}@${h}:${port}${db}`, o.ssl);
}

// withTLS maps a mariadb `ssl` option to go-sql-driver's `tls` DSN param, the
// way js/mysql2.js does: an ssl object with a `ca` and rejectUnauthorized not
// false verifies against that CA, any other truthy ssl encrypts without
// verification, a falsy ssl is left plaintext.
function withTLS(d, ssl) {
  if (!ssl || /[?&]tls=/.test(d)) return d;
  let param;
  if (typeof ssl === "object" && ssl.ca && ssl.rejectUnauthorized !== false) {
    const ca = Array.isArray(ssl.ca) ? ssl.ca.join("\n") : String(ssl.ca);
    param = host().registerTLS(ca, ssl.servername || ssl.host || "");
  } else {
    param = "skip-verify";
  }
  return d + (d.includes("?") ? "&" : "?") + "tls=" + encodeURIComponent(param);
}

const UNSIGNED_FLAG = 1 << 5; // @prisma/adapter-mariadb reads field.flags & this
const BINARY_COLLATION_INDEX = 63; // and field.collation.index === this (Bytes)
const TEXT_COLLATION_INDEX = 33; // any non-binary index works

// mapType derives the mariadb driver's field.type string (and whether the
// column is binary) from go-sql-driver's DatabaseTypeName, which already
// encodes signedness ("UNSIGNED INT") and text-vs-binary ("TEXT" vs "BLOB").
function mapType(dbType) {
  const up = String(dbType || "").toUpperCase();
  const unsigned = up.startsWith("UNSIGNED ");
  const base = unsigned ? up.slice("UNSIGNED ".length) : up;
  let type = base,
    binary = false;
  switch (base) {
    case "TINYINT": type = "TINY"; break;
    case "SMALLINT": type = "SHORT"; break;
    case "MEDIUMINT": type = "INT24"; break;
    case "INT": case "INTEGER": type = "INT"; break;
    case "BIGINT": type = "BIGINT"; break;
    case "YEAR": type = "YEAR"; break;
    case "FLOAT": type = "FLOAT"; break;
    case "DOUBLE": type = "DOUBLE"; break;
    case "DECIMAL": case "NEWDECIMAL": type = "NEWDECIMAL"; break;
    case "DATE": type = "DATE"; break;
    case "DATETIME": type = "DATETIME"; break;
    case "TIMESTAMP": type = "TIMESTAMP"; break;
    case "TIME": type = "TIME"; break;
    case "VARCHAR": type = "VARCHAR"; break;
    case "VARBINARY": type = "VAR_STRING"; binary = true; break;
    case "CHAR": type = "STRING"; break;
    case "BINARY": type = "STRING"; binary = true; break;
    case "TEXT": type = "BLOB"; break;
    case "BLOB": type = "BLOB"; binary = true; break;
    case "TINYTEXT": type = "TINY_BLOB"; break;
    case "TINYBLOB": type = "TINY_BLOB"; binary = true; break;
    case "MEDIUMTEXT": type = "MEDIUM_BLOB"; break;
    case "MEDIUMBLOB": type = "MEDIUM_BLOB"; binary = true; break;
    case "LONGTEXT": type = "LONG_BLOB"; break;
    case "LONGBLOB": type = "LONG_BLOB"; binary = true; break;
    case "JSON": type = "JSON"; break;
    case "ENUM": type = "ENUM"; break;
    case "SET": type = "SET"; break;
    case "BIT": type = "BIT"; binary = true; break;
    case "GEOMETRY": type = "GEOMETRY"; binary = true; break;
    case "NULL": type = "NULL"; break;
    default: type = base || "BLOB"; break;
  }
  return { type, unsigned, binary };
}

// metaField builds one driver ColumnDef the adapter reads: name() returns the
// column name, type/flags/collation.index drive mapColumnType.
function metaField(f) {
  const { type, unsigned, binary } = mapType(f.dbType);
  const name = f.name;
  return {
    name: () => name,
    type,
    flags: unsigned ? UNSIGNED_FLAG : 0,
    collation: { index: binary ? BINARY_COLLATION_INDEX : TEXT_COLLATION_INDEX },
    columnLength: f.length,
  };
}

// shape turns the host result into the mariadb driver's result: a write becomes
// an upsert-info object (affectedRows/insertId), a SELECT an array of row arrays
// carrying a non-enumerable `meta` (the column defs), which is what the driver
// returns under rowsAsArray.
function shape(raw) {
  if (!raw.isSelect) {
    return { affectedRows: raw.affectedRows, insertId: raw.insertId, warningStatus: 0 };
  }
  // The host returns Go-slice-backed values; rebuild as genuine JS arrays so
  // the adapter's Array.isArray(row) holds and `meta` can be attached.
  const meta = Array.from(raw.fields, metaField);
  const rows = Array.from(raw.rows || [], (r) => Array.from(r));
  Object.defineProperty(rows, "meta", { value: meta, enumerable: false });
  return rows;
}

// sqlOf accepts the driver's call forms: a string, or an options object with a
// `sql` (the adapter always passes { sql, rowsAsArray, dateStrings, … }).
function sqlOf(sqlOrOpts) {
  return sqlOrOpts && typeof sqlOrOpts === "object" ? sqlOrOpts.sql : sqlOrOpts;
}

class Connection {
  constructor(id, pool) {
    this._id = id;
    this._pool = pool;
  }
  async query(sqlOrOpts, values) {
    return shape(await host().queryClientMaria(this._id, sqlOf(sqlOrOpts), values || null));
  }
  execute(sqlOrOpts, values) {
    return this.query(sqlOrOpts, values);
  }
  async release() {
    if (this._id != null) {
      host().release(this._id);
      if (this._pool) this._pool._clients.delete(this);
      this._id = null;
    }
  }
  end() {
    return this.release();
  }
  on() {
    return this;
  }
  off() {
    return this;
  }
  removeListener() {
    return this;
  }
}

class Pool {
  constructor(config) {
    this._dsn = toDSN(config);
    const limit = config && typeof config === "object" ? config.connectionLimit : undefined;
    this._max = limit > 0 ? limit : 10;
    this._clients = new Set();
  }
  async query(sqlOrOpts, values) {
    return shape(await host().queryPoolMaria(this._dsn, this._max, sqlOf(sqlOrOpts), values || null));
  }
  execute(sqlOrOpts, values) {
    return this.query(sqlOrOpts, values);
  }
  async getConnection() {
    const id = await host().acquire(this._dsn, this._max);
    const c = new Connection(id, this);
    this._clients.add(c);
    return c;
  }
  async end() {
    // The shared Go pool stays warm; hand back any borrowed connections.
    for (const c of [...this._clients]) await c.release();
  }
  on() {
    return this;
  }
  off() {
    return this;
  }
  removeListener() {
    return this;
  }
}

function createPool(config) {
  return new Pool(config);
}
async function createConnection(config) {
  const pool = new Pool(config);
  return pool.getConnection();
}

const mariadb = { createPool, createConnection, Pool, Connection };
export default mariadb;
export { createPool, createConnection };
