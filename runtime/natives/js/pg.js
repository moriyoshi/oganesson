// Oganesson's pg (node-postgres): its async API on PostgreSQL in Go
// (runtime/natives/pg.go, via jackc/pgx). The pgx pool is shared by every
// request of the process; a JS Pool borrows connections from it and its end()
// releases them without closing the shared pool.
const host = () => {
  const h = globalThis[Symbol.for("og.pg")];
  if (!h) throw new Error("pg: Oganesson's PostgreSQL service is not installed in this runtime");
  return h;
};

const toBuffer = (ab) => (globalThis.Buffer ? globalThis.Buffer.from(ab) : new Uint8Array(ab));
const wrap = (v) => (v instanceof ArrayBuffer ? toBuffer(v) : v);

// build turns the host result (positional rows + field descriptors) into a
// node-postgres Result: object rows by default, positional rows when the query
// asked for rowMode "array" (Drizzle's node-postgres driver does, to keep
// duplicate column names apart).
//
// parse, when set, is the query's getTypeParser (node-postgres' per-OID type
// parsers; Prisma's adapter-pg passes custom ones for temporal/numeric/json/
// bytea). The host then returns each column as its Postgres text (string or
// null), and we apply the parser here, exactly as node-postgres does. Without
// parse the host already returns decoded values; wrap only fixes bytea Buffers.
function build(raw, rowMode, parse) {
  const fields = raw.fields;
  const arrayMode = rowMode === "array";
  const conv = parse
    ? (v, i) => {
        if (v == null) return null;
        const p = parse(fields[i].dataTypeID, "text");
        return p ? p(v) : v;
      }
    : (v) => wrap(v);
  const rows = raw.rows.map((vals) => {
    if (arrayMode) return vals.map(conv);
    const o = {};
    for (let i = 0; i < fields.length; i++) o[fields[i].name] = conv(vals[i], i);
    return o;
  });
  return { command: raw.command, rowCount: raw.rowCount, oid: null, rows, fields, rowAsArray: arrayMode };
}

// config normalizes query(text, values) and query({ text, values, rowMode, types }).
function config(textOrConfig, values) {
  if (textOrConfig && typeof textOrConfig === "object") {
    return { text: textOrConfig.text, values: textOrConfig.values || values || null, rowMode: textOrConfig.rowMode, types: textOrConfig.types };
  }
  return { text: textOrConfig, values: values || null, rowMode: undefined, types: undefined };
}

// typeParser extracts the query's getTypeParser, if it supplied one. When it
// did, the host returns raw text and build() applies these parsers.
function typeParser(c) {
  return c.types && typeof c.types.getTypeParser === "function" ? c.types.getTypeParser.bind(c.types) : null;
}

// A connection borrowed from a Pool (pool.connect()). Transactions run their
// BEGIN / COMMIT / ROLLBACK on one of these.
class Client {
  constructor(id) {
    this._id = id;
  }
  async query(textOrConfig, values) {
    if (this._id == null) throw new Error("Cannot use a released client");
    const c = config(textOrConfig, values);
    const parse = typeParser(c);
    return build(await host().queryClient(this._id, c.text, c.values, !!parse), c.rowMode, parse);
  }
  release() {
    if (this._id != null) {
      host().release(this._id);
      this._id = null;
    }
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
    this.options = options;
    this._dsn = connectionString(options);
    // max bounds the Go pool's connections (pg.go); a one-off query borrows and
    // returns one, pool.connect() pins one for a transaction. max:1 (used to
    // serve single-session PGlite) therefore keeps the DSN to one connection.
    this._max = options.max > 0 ? options.max : 10;
    this._clients = new Set();
    this.ended = false;
    this.totalCount = 0;
    this.idleCount = 0;
    this.waitingCount = 0;
  }
  async query(textOrConfig, values) {
    const c = config(textOrConfig, values);
    const parse = typeParser(c);
    return build(await host().queryPool(this._dsn, this._max, c.text, c.values, !!parse), c.rowMode, parse);
  }
  async connect(cb) {
    try {
      const id = await host().acquire(this._dsn, this._max);
      const client = new Client(id);
      this._clients.add(client);
      const release = client.release.bind(client);
      client.release = () => {
        this._clients.delete(client);
        release();
      };
      if (cb) {
        cb(null, client, client.release);
        return;
      }
      return client;
    } catch (e) {
      if (cb) {
        cb(e);
        return;
      }
      throw e;
    }
  }
  async end() {
    for (const c of [...this._clients]) c.release();
    this.ended = true;
  }
  on() {
    return this;
  }
  removeListener() {
    return this;
  }
}

// connectionString builds the DSN pgx parses. node-postgres also accepts
// discrete host/port/user/password/database fields; map the common ones.
function connectionString(o) {
  let dsn;
  if (o.connectionString) {
    dsn = o.connectionString;
  } else {
    const user = encodeURIComponent(o.user || "postgres");
    const auth = o.password != null ? user + ":" + encodeURIComponent(String(o.password)) : user;
    const host = o.host || "localhost";
    const port = o.port || 5432;
    const db = o.database != null ? "/" + encodeURIComponent(o.database) : "";
    dsn = `postgres://${auth}@${host}:${port}${db}`;
  }
  return withTLSVerify(withSslmode(withOptions(dsn, o), o.ssl), o);
}

// withTLSVerify maps node-postgres' `ssl: { ca, rejectUnauthorized }` to a
// verifying TLS config: the host registers one from the CA and returns the
// og-internal `ogtls` DSN key (pg.go strips it and applies the config). An
// `ssl` without a `ca`, or rejectUnauthorized false, keeps the encrypt-only
// baseline (sslmode set by withSslmode).
function withTLSVerify(dsn, o) {
  const ssl = o.ssl;
  if (!ssl || typeof ssl !== "object" || !ssl.ca || ssl.rejectUnauthorized === false || /[?&]ogtls=/.test(dsn)) {
    return dsn;
  }
  const ca = Array.isArray(ssl.ca) ? ssl.ca.join("\n") : String(ssl.ca);
  const key = host().registerTLS(ca, ssl.servername || o.host || "");
  return dsn + (dsn.includes("?") ? "&" : "?") + "ogtls=" + encodeURIComponent(key);
}

// withOptions maps node-postgres' discrete connection options onto the DSN that
// pgx parses. application_name, statement_timeout and
// idle_in_transaction_session_timeout are startup runtime parameters (pgx sends
// a DSN query param it does not recognise as a connection setting in the
// startup message, as node-postgres sends these in its startup packet);
// connectionTimeoutMillis maps to libpq connect_timeout (seconds, rounded up);
// `options` passes libpq command-line options through. Each is appended only
// when absent from the DSN, so a value already in a connectionString wins.
function withOptions(dsn, o) {
  const params = [];
  const has = (k) => new RegExp("[?&]" + k + "=").test(dsn);
  const add = (k, v) => {
    if (v != null && v !== false && !has(k)) params.push(k + "=" + encodeURIComponent(String(v)));
  };
  add("application_name", o.application_name);
  if (typeof o.statement_timeout === "number") add("statement_timeout", o.statement_timeout);
  if (typeof o.idle_in_transaction_session_timeout === "number") add("idle_in_transaction_session_timeout", o.idle_in_transaction_session_timeout);
  if (typeof o.connectionTimeoutMillis === "number" && o.connectionTimeoutMillis > 0) add("connect_timeout", Math.ceil(o.connectionTimeoutMillis / 1000));
  add("options", o.options);
  if (params.length === 0) return dsn;
  return dsn + (dsn.includes("?") ? "&" : "?") + params.join("&");
}

// withSslmode maps node-postgres'/postgres.js' `ssl` option to the DSN's
// sslmode so pgx matches the JS driver instead of its own sslmode=prefer
// default. A falsy/absent `ssl` is left alone: the Go side (pg.go) treats a
// DSN without sslmode as no TLS, matching the JS drivers' `ssl: false` default.
// (Full certificate verification from an `ssl` object is a follow-up; this
// maps presence of TLS, not CA/verify options.)
function withSslmode(dsn, ssl) {
  if (!ssl || /\bsslmode=/.test(dsn)) return dsn;
  return dsn + (dsn.includes("?") ? "&" : "?") + "sslmode=require";
}

// node-postgres exposes pg.types.builtins: the map of type name to OID
// (pg-types' TypeId). Consumers read it to recognise column types by OID —
// @prisma/adapter-pg destructures it as its ScalarColumnType and switches on
// ScalarColumnType.INT4 etc. An empty object makes every such case `undefined`,
// so adapter-pg falls through to "unsupported native type" for every column.
// The values are pg-types lib/builtins.js verbatim.
const builtins = {
  BOOL: 16,
  BYTEA: 17,
  CHAR: 18,
  INT8: 20,
  INT2: 21,
  INT4: 23,
  REGPROC: 24,
  TEXT: 25,
  OID: 26,
  TID: 27,
  XID: 28,
  CID: 29,
  JSON: 114,
  XML: 142,
  PG_NODE_TREE: 194,
  SMGR: 210,
  PATH: 602,
  POLYGON: 604,
  CIDR: 650,
  FLOAT4: 700,
  FLOAT8: 701,
  ABSTIME: 702,
  RELTIME: 703,
  TINTERVAL: 704,
  CIRCLE: 718,
  MACADDR8: 774,
  MONEY: 790,
  MACADDR: 829,
  INET: 869,
  ACLITEM: 1033,
  BPCHAR: 1042,
  VARCHAR: 1043,
  DATE: 1082,
  TIME: 1083,
  TIMESTAMP: 1114,
  TIMESTAMPTZ: 1184,
  INTERVAL: 1186,
  TIMETZ: 1266,
  BIT: 1560,
  VARBIT: 1562,
  NUMERIC: 1700,
  REFCURSOR: 1790,
  REGPROCEDURE: 2202,
  REGOPER: 2203,
  REGOPERATOR: 2204,
  REGCLASS: 2205,
  REGTYPE: 2206,
  UUID: 2950,
  TXID_SNAPSHOT: 2970,
  PG_LSN: 3220,
  PG_NDISTINCT: 3361,
  PG_DEPENDENCIES: 3402,
  TSVECTOR: 3614,
  TSQUERY: 3615,
  GTSVECTOR: 3642,
  REGCONFIG: 3734,
  REGDICTIONARY: 3769,
  JSONB: 3802,
  REGNAMESPACE: 4089,
  REGROLE: 4096,
};

// node-postgres' default text parsers, by OID. These matter only for a query
// that passes its own types.getTypeParser (Prisma's adapter-pg, Drizzle): the
// host then returns raw Postgres text and these decode it, exactly as
// node-postgres would. Those consumers override the temporal OIDs themselves
// (adapter-pg normalizes to strings; Drizzle keeps the raw string), so the
// date/timestamp parsers here are a best-effort fallback. A query that passes
// no types keeps the Go-decoded values (pg.go jsVal) and never reaches these.
function parseBytea(v) {
  const hex = v.slice(2); // "\x6162…"
  const n = hex.length >> 1;
  const buf = globalThis.Buffer ? globalThis.Buffer.alloc(n) : new Uint8Array(n);
  for (let i = 0; i < n; i++) buf[i] = parseInt(hex.substr(i * 2, 2), 16);
  return buf;
}
const defaultParsers = {
  16: (v) => v === "t" || v === "true" || v === "TRUE" || v === "1" || v === "y", // bool
  21: (v) => parseInt(v, 10), // int2
  23: (v) => parseInt(v, 10), // int4
  26: (v) => parseInt(v, 10), // oid
  700: (v) => parseFloat(v), // float4
  701: (v) => parseFloat(v), // float8
  114: (v) => JSON.parse(v), // json
  3802: (v) => JSON.parse(v), // jsonb
  17: parseBytea, // bytea
  1082: (v) => new Date(v), // date
  1114: (v) => new Date(v.replace(" ", "T") + "Z"), // timestamp (no tz): read as UTC
  1184: (v) => new Date(v), // timestamptz
  // int8 (20) and numeric (1700) stay strings (node-postgres avoids precision
  // loss); varchar/text and the rest fall through to identity below.
};

const types = {
  setTypeParser() {},
  getTypeParser(oid) {
    return defaultParsers[oid] || ((v) => v);
  },
  builtins,
};

const pg = {
  Pool,
  Client,
  types,
  // pg-native is optional (a separate native addon); node-postgres' lazy
  // getter returns null when it is absent, which it always is here.
  get native() {
    return null;
  },
};

export default pg;
export { Pool, Client, types };
