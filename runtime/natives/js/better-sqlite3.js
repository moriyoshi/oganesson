// Oganesson's better-sqlite3: its synchronous API on SQLite in Go
// (runtime/natives/sqlite.go). Databases are shared by every request of the
// process; each Database object has its own connection.
const host = () => {
  const h = globalThis[Symbol.for("og.sqlite")];
  if (!h) throw new Error("better-sqlite3: Oganesson's SQLite service is not installed in this runtime");
  return h;
};

const toBuffer = (ab) => (globalThis.Buffer ? globalThis.Buffer.from(ab) : new Uint8Array(ab));

class SqliteError extends Error {
  constructor(message, code) {
    super(message);
    this.name = "SqliteError";
    this.code = code;
  }
}

function call(fn) {
  try {
    return fn();
  } catch (e) {
    if (e instanceof TypeError || e instanceof RangeError) throw e;
    throw new SqliteError(String(e && e.message ? e.message : e).replace(/^GoError: /, ""), (e && e.code) || "SQLITE_ERROR");
  }
}

function isNamed(v) {
  return v !== null && typeof v === "object" && !ArrayBuffer.isView(v) && !(v instanceof ArrayBuffer) && !Array.isArray(v);
}

function bindings(args) {
  const positional = [];
  let named;
  const add = (v) => {
    if (Array.isArray(v)) v.forEach(add);
    else if (isNamed(v)) named = Object.assign(named || {}, v);
    else if (ArrayBuffer.isView(v)) positional.push(v.buffer.slice(v.byteOffset, v.byteOffset + v.byteLength));
    else positional.push(v);
  };
  args.forEach(add);
  if (named) {
    for (const [k, v] of Object.entries(named)) {
      if (ArrayBuffer.isView(v)) named[k] = v.buffer.slice(v.byteOffset, v.byteOffset + v.byteLength);
    }
  }
  return [positional, named];
}

class Statement {
  constructor(db, source) {
    this.database = db;
    this.source = source;
    this.reader = host().isReader(source);
    this.readonly = this.reader && !/\breturning\b/i.test(source);
    this.busy = false;
    this._mode = "object"; // object | raw | pluck | expand
    this._safe = db._safeIntegers;
    this._bound = null;
  }
  _args(args) {
    if (this._bound) {
      if (args.length) throw new TypeError("This statement already has bound parameters");
      return this._bound;
    }
    return bindings(args);
  }
  _rows(args, limit) {
    if (!this.reader) throw new TypeError("This statement does not return data. Use run() instead");
    const [p, n] = this._args(args);
    const { columns, rows } = call(() => host().query(this.database._id(), this.source, p, n, this._safe, limit));
    return rows.map((vals) => {
      vals = vals.map((v) => (v instanceof ArrayBuffer ? toBuffer(v) : v));
      switch (this._mode) {
        case "raw":
          return vals;
        case "pluck":
          return vals[0];
        case "expand":
          return { $: Object.fromEntries(columns.map((c, i) => [c, vals[i]])) };
      }
      const row = {};
      columns.forEach((c, i) => (row[c] = vals[i]));
      return row;
    });
  }
  run(...args) {
    const [p, n] = this._args(args);
    const r = call(() => host().run(this.database._id(), this.source, p, n));
    return { changes: r.changes, lastInsertRowid: this._safe ? BigInt(r.lastInsertRowid) : r.lastInsertRowid };
  }
  get(...args) {
    return this._rows(args, 1)[0];
  }
  all(...args) {
    return this._rows(args, 0);
  }
  *iterate(...args) {
    yield* this._rows(args, 0);
  }
  pluck(on = true) {
    this._mode = on ? "pluck" : "object";
    return this;
  }
  raw(on = true) {
    this._mode = on ? "raw" : "object";
    return this;
  }
  expand(on = true) {
    this._mode = on ? "expand" : "object";
    return this;
  }
  safeIntegers(on = true) {
    this._safe = !!on;
    return this;
  }
  bind(...args) {
    if (this._bound) throw new TypeError("The bind() method can only be invoked once per statement object");
    this._bound = bindings(args);
    return this;
  }
  // From the prepared statement, without running it (an INSERT ...
  // RETURNING would run twice: Prisma's adapter asks for the columns first).
  columns() {
    if (!this.reader) throw new TypeError("The columns() method is only for statements that return data");
    return call(() => host().columns(this.database._id(), this.source));
  }
}

let savepoints = 0;

class Database {
  constructor(filename = "", options = {}) {
    if (typeof filename !== "string") throw new TypeError("better-sqlite3: Oganesson supports file names and :memory: (not serialized buffers)");
    this.name = filename;
    this.memory = filename === "" || filename === ":memory:";
    this.readonly = !!options.readonly;
    this.inTransaction = false;
    this._safeIntegers = false;
    this._depth = 0;
    this._handle = call(() => host().open(filename, this.readonly, !!options.fileMustExist, options.timeout ?? 5000));
    this.open = true;
  }
  _id() {
    if (!this.open) throw new TypeError("The database connection is not open");
    return this._handle;
  }
  prepare(source) {
    this._id();
    return new Statement(this, String(source));
  }
  exec(source) {
    call(() => host().exec(this._id(), String(source)));
    return this;
  }
  pragma(source, options = {}) {
    const st = this.prepare("PRAGMA " + source);
    if (options.simple) return st.pluck().get();
    return st.all();
  }
  transaction(fn) {
    if (typeof fn !== "function") throw new TypeError("Expected first argument to be a function");
    const db = this;
    const make = (begin) =>
      function (...args) {
        if (db._depth > 0) {
          const sp = `og_sp_${++savepoints}`;
          db.exec(`SAVEPOINT ${sp}`);
          db._depth++;
          try {
            const r = fn.apply(this, args);
            db.exec(`RELEASE ${sp}`);
            return r;
          } catch (e) {
            db.exec(`ROLLBACK TO ${sp}`).exec(`RELEASE ${sp}`);
            throw e;
          } finally {
            db._depth--;
          }
        }
        db.exec(begin);
        db._depth = 1;
        db.inTransaction = true;
        try {
          const r = fn.apply(this, args);
          db.exec("COMMIT");
          return r;
        } catch (e) {
          if (db.open) db.exec("ROLLBACK");
          throw e;
        } finally {
          db._depth = 0;
          db.inTransaction = false;
        }
      };
    const tx = make("BEGIN");
    tx.deferred = make("BEGIN DEFERRED");
    tx.immediate = make("BEGIN IMMEDIATE");
    tx.exclusive = make("BEGIN EXCLUSIVE");
    tx.default = tx;
    return tx;
  }
  defaultSafeIntegers(on = true) {
    this._safeIntegers = !!on;
    return this;
  }
  unsafeMode() {
    return this;
  }
  close() {
    if (this.open) host().close(this._handle);
    this.open = false;
    return this;
  }
}

for (const name of ["function", "aggregate", "table", "backup", "serialize", "loadExtension"]) {
  Database.prototype[name] = function () {
    throw new TypeError(`better-sqlite3: Database.${name}() is not supported by Oganesson`);
  };
}

Database.SqliteError = SqliteError;
export default Database;
export { SqliteError };
