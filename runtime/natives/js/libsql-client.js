// Oganesson's @libsql/client: the local (file:/:memory:) client on SQLite in
// Go (runtime/natives/sqlite.go, the same service better-sqlite3 uses). Only
// the libsql protocol over a local file is supported; remote libsql:// / http
// / ws URLs need a network client, which Oganesson does not provide here.
//
// drizzle-orm/libsql drives this through createClient(...).execute({sql,args}),
// .batch(), .transaction() and .close(); execute returns a ResultSet whose
// rows are dual-shape (column-name keys for Object.keys, and array-indexable
// via length + indices for Array.prototype.slice.call), as libsql's are.
const host = () => {
  const h = globalThis[Symbol.for("og.sqlite")];
  if (!h) throw new Error("@libsql/client: Oganesson's SQLite service is not installed in this runtime");
  return h;
};

class LibsqlError extends Error {
  constructor(message, code, rawCode, cause) {
    super(code ? `${code}: ${message}` : message, cause !== undefined ? { cause } : undefined);
    this.name = "LibsqlError";
    this.code = code;
    this.rawCode = rawCode;
  }
}

function wrap(fn) {
  try {
    return fn();
  } catch (e) {
    if (e instanceof LibsqlError) throw e;
    const msg = String(e && e.message ? e.message : e).replace(/^GoError: /, "");
    throw new LibsqlError(msg, (e && e.code) || "UNKNOWN", e && e.rawCode);
  }
}

// file:<path> / file://<authority>/<path> / :memory: -> a name the SQLite
// service opens (an absolute OS path is a key in its in-memory overlay).
function fileFromUrl(url) {
  if (typeof url !== "string") throw new LibsqlError("The client config must have a string url", "URL_INVALID");
  if (url === ":memory:") return ":memory:";
  let scheme = url.slice(0, url.indexOf(":"));
  if (scheme === "file") {
    let rest = url.slice(5);
    if (rest.startsWith("//")) rest = rest.slice(2).replace(/^[^/]*/, ""); // drop authority
    const q = rest.search(/[?#]/);
    if (q >= 0) rest = rest.slice(0, q);
    return decodeURIComponent(rest) || ":memory:";
  }
  throw new LibsqlError(
    `Oganesson's @libsql/client supports only "file:" and ":memory:" URLs, not ${JSON.stringify(url)} (remote libsql needs an HTTP/WebSocket client)`,
    "URL_SCHEME_NOT_SUPPORTED",
  );
}

// A libsql Row: column names enumerable (Object.keys -> names), numeric
// indices and length present but non-enumerable (Array.prototype.slice works).
function makeRow(columns, vals) {
  const row = {};
  for (let i = 0; i < vals.length; i++) {
    Object.defineProperty(row, i, { value: vals[i], enumerable: false, writable: true, configurable: true });
  }
  Object.defineProperty(row, "length", { value: vals.length, enumerable: false, writable: false, configurable: true });
  for (let i = 0; i < columns.length; i++) row[columns[i]] = vals[i];
  return row;
}

function toHostArgs(args) {
  const view = (v) => (ArrayBuffer.isView(v) ? v.buffer.slice(v.byteOffset, v.byteOffset + v.byteLength) : v);
  if (args == null) return [[], undefined];
  if (Array.isArray(args)) return [args.map(view), undefined];
  const named = {};
  for (const [k, v] of Object.entries(args)) named[k] = view(v);
  return [[], named];
}

function statement(stmt) {
  if (typeof stmt === "string") return { sql: stmt, args: [] };
  if (stmt && typeof stmt.sql === "string") return { sql: stmt.sql, args: stmt.args };
  throw new LibsqlError("A statement must be a string or an { sql, args } object", "ARGS_INVALID");
}

function executeOn(id, stmt) {
  const { sql, args } = statement(stmt);
  const [positional, named] = toHostArgs(args);
  return wrap(() => {
    if (host().isReader(sql)) {
      const { columns, rows } = host().query(id, sql, positional, named, false, 0);
      return {
        columns,
        columnTypes: columns.map(() => ""),
        rows: rows.map((vals) => makeRow(columns, vals)),
        rowsAffected: 0,
        lastInsertRowid: undefined,
        toJSON() {
          return { columns: this.columns, rows: this.rows.map((r) => Array.prototype.slice.call(r)), rowsAffected: this.rowsAffected, lastInsertRowid: null };
        },
      };
    }
    const { changes, lastInsertRowid } = host().run(id, sql, positional, named);
    return {
      columns: [],
      columnTypes: [],
      rows: [],
      rowsAffected: changes,
      lastInsertRowid: lastInsertRowid == null ? undefined : BigInt(Math.trunc(lastInsertRowid)),
      toJSON() {
        return { columns: [], rows: [], rowsAffected: this.rowsAffected, lastInsertRowid: this.lastInsertRowid != null ? String(this.lastInsertRowid) : null };
      },
    };
  });
}

function makeTransaction(id) {
  let open = true;
  const guard = () => {
    if (!open) throw new LibsqlError("The transaction is closed", "TRANSACTION_CLOSED");
  };
  return {
    execute: (stmt) => {
      guard();
      return executeOn(id, stmt);
    },
    batch: (stmts) => {
      guard();
      return stmts.map((s) => executeOn(id, s));
    },
    executeMultiple: (sql) => {
      guard();
      return wrap(() => host().exec(id, sql));
    },
    commit: () => {
      guard();
      open = false;
      return wrap(() => host().exec(id, "COMMIT"));
    },
    rollback: () => {
      guard();
      open = false;
      return wrap(() => host().exec(id, "ROLLBACK"));
    },
    close: () => {
      open = false;
    },
    get closed() {
      return !open;
    },
  };
}

class Client {
  constructor(config) {
    this.closed = false;
    this.protocol = "file";
    this._handle = wrap(() => host().open(fileFromUrl(config && config.url), false, false, (config && config.timeout) || 5000));
  }
  execute(stmt, args) {
    // libsql also accepts execute(sql, args).
    if (typeof stmt === "string" && args !== undefined) stmt = { sql: stmt, args };
    return Promise.resolve().then(() => executeOn(this._handle, stmt));
  }
  batch(stmts) {
    return Promise.resolve().then(() =>
      wrap(() => {
        host().exec(this._handle, "BEGIN");
        try {
          const out = stmts.map((s) => executeOn(this._handle, s));
          host().exec(this._handle, "COMMIT");
          return out;
        } catch (e) {
          try {
            host().exec(this._handle, "ROLLBACK");
          } catch {}
          throw e;
        }
      }),
    );
  }
  executeMultiple(sql) {
    return Promise.resolve().then(() => wrap(() => host().exec(this._handle, sql)));
  }
  transaction(mode) {
    return Promise.resolve().then(() => {
      const begin = mode === "write" ? "BEGIN IMMEDIATE" : mode === "readonly" ? "BEGIN" : "BEGIN DEFERRED";
      wrap(() => host().exec(this._handle, begin));
      return makeTransaction(this._handle);
    });
  }
  sync() {
    return Promise.resolve();
  }
  close() {
    if (!this.closed) wrap(() => host().close(this._handle));
    this.closed = true;
  }
}

export function createClient(config) {
  return new Client(config);
}

export { LibsqlError };
export default { createClient, LibsqlError };
