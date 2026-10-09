// Oganesson's postgres (postgres.js): its tagged-template API on the same Go
// PostgreSQL service pg uses (runtime/natives/pg.go, via jackc/pgx), read from
// Symbol.for("og.pg"). postgres.js and node-postgres speak the same protocol, so
// there is no postgres-specific Go code; only this JS surface differs. The pgx
// pool is shared by every request of the process and stays warm across them.
const host = () => {
  const h = globalThis[Symbol.for("og.pg")];
  if (!h) throw new Error("postgres: Oganesson's PostgreSQL service is not installed in this runtime");
  return h;
};

const toBuffer = (ab) => (globalThis.Buffer ? globalThis.Buffer.from(ab) : new Uint8Array(ab));
const wrap = (v) => (v instanceof ArrayBuffer ? toBuffer(v) : v);

// rowsOf turns the host result (positional rows + field descriptors) into the
// array postgres.js returns: object rows by default, positional rows for
// .values(). The array carries postgres.js's extra properties (count, command,
// columns), which some callers read.
function rowsOf(raw, arrayMode) {
  const fields = raw.fields;
  const out = raw.rows.map((vals) => {
    if (arrayMode) return vals.map(wrap);
    const o = {};
    for (let i = 0; i < fields.length; i++) o[fields[i].name] = wrap(vals[i]);
    return o;
  });
  out.count = raw.rowCount;
  out.command = raw.command;
  out.columns = fields.map((f) => ({ name: f.name, type: f.dataTypeID }));
  out.statement = {};
  return out;
}

// pending is postgres.js's lazy query result: a thenable that runs the query
// when awaited (object rows), with .values() for a positional-row run and
// .execute() to start it eagerly. run(text, params) returns the host result.
function pending(run, text, params) {
  const go = (arrayMode) => Promise.resolve(run(text, params)).then((raw) => rowsOf(raw, arrayMode));
  const self = {
    then: (onF, onR) => go(false).then(onF, onR),
    catch: (onR) => go(false).catch(onR),
    finally: (onF) => go(false).finally(onF),
    values: () => {
      // A fresh pending that resolves to positional rows; the base is never run.
      const v = pending(run, text, params);
      v.then = (onF, onR) => go(true).then(onF, onR);
      v.catch = (onR) => go(true).catch(onR);
      v.finally = (onF) => go(true).finally(onF);
      return v;
    },
    execute: () => go(false),
    raw: () => self.values(),
  };
  return self;
}

// client builds a tagged-template `sql` function over a runner (text, params) ->
// host result. The pool runner and a transaction's pinned-client runner share
// this shape so the callback `sql` behaves like the top-level one.
function client(run) {
  const sql = function (strings, ...args) {
    if (Array.isArray(strings) && strings.raw) {
      // sql`select ... ${a} ... ${b}` -> "select ... $1 ... $2", [a, b]
      let text = strings[0];
      for (let i = 0; i < args.length; i++) text += "$" + (i + 1) + strings[i + 1];
      return pending(run, text, args.length ? args : null);
    }
    if (typeof strings === "string") return pending(run, strings, args.length ? args : null);
    throw new Error("postgres: unsupported sql() call form");
  };
  sql.unsafe = (query, params) => pending(run, query, params && params.length ? params : null);
  // Drizzle mutates these at construction; keep them present and mutable.
  sql.options = { parsers: {}, serializers: {} };
  return sql;
}

export default function postgres(url, options) {
  let dsn, opts;
  if (typeof url === "string") {
    dsn = url;
    opts = options || {};
  } else {
    opts = url || {};
    dsn = connectionString(opts);
  }
  dsn = withSslmode(dsn, opts.ssl);
  const max = opts.max > 0 ? opts.max : 10;
  const poolRun = (text, params) => host().queryPool(dsn, max, text, params || null);
  const sql = client(poolRun);

  // A transaction: one pinned connection carries BEGIN ... COMMIT/ROLLBACK, and
  // the callback's sql runs on it. Nested begin opens a savepoint.
  async function begin(a, b) {
    const fn = typeof a === "function" ? a : b; // begin(options, fn) or begin(fn)
    const id = await host().acquire(dsn, max);
    const txRun = (text, params) => host().queryClient(id, text, params || null);
    let depth = 0;
    const makeTx = (level) => {
      const txSql = client(txRun);
      txSql.savepoint = (x, y) => runScope(typeof x === "function" ? x : y, level + 1);
      txSql.begin = txSql.savepoint;
      return txSql;
    };
    const runScope = async (cb, level) => {
      const name = "og_sp_" + level;
      const open = level === 0 ? "BEGIN" : "SAVEPOINT " + name;
      const commit = level === 0 ? "COMMIT" : "RELEASE SAVEPOINT " + name;
      const rollback = level === 0 ? "ROLLBACK" : "ROLLBACK TO SAVEPOINT " + name;
      await host().queryClient(id, open, null);
      try {
        const r = await cb(makeTx(level));
        await host().queryClient(id, commit, null);
        return r;
      } catch (e) {
        try {
          await host().queryClient(id, rollback, null);
        } catch {}
        throw e;
      }
    };
    try {
      return await runScope(fn, depth);
    } finally {
      host().release(id);
    }
  }
  sql.begin = begin;
  sql.end = async () => {
    // The shared Go pool stays warm for the next request; a per-query connection
    // is already released, so there is nothing to close here.
  };
  sql.close = sql.end;
  sql.options = Object.assign(sql.options, { max, parsers: sql.options.parsers, serializers: sql.options.serializers });
  return sql;
}

// connectionString maps postgres.js's discrete options to a DSN pgx parses;
// a URL string is used as given.
function connectionString(o) {
  if (o.url) return typeof o.url === "string" ? o.url : o.url.href;
  const user = encodeURIComponent(o.user || o.username || "postgres");
  const auth = o.password != null ? user + ":" + encodeURIComponent(String(o.password)) : user;
  const host = (Array.isArray(o.host) ? o.host[0] : o.host) || "localhost";
  const port = (Array.isArray(o.port) ? o.port[0] : o.port) || 5432;
  const db = o.database != null ? "/" + encodeURIComponent(o.database) : "";
  return `postgres://${auth}@${host}:${port}${db}`;
}

// withSslmode maps postgres.js' `ssl` option to the DSN's sslmode so pgx matches
// the JS driver instead of its own sslmode=prefer default. A falsy/absent `ssl`
// is left alone: pg.go treats a DSN without sslmode as no TLS, matching the JS
// driver's no-TLS default. (CA/verify options are a follow-up.)
function withSslmode(dsn, ssl) {
  if (!ssl || /\bsslmode=/.test(dsn)) return dsn;
  return dsn + (dsn.includes("?") ? "&" : "?") + "sslmode=require";
}
