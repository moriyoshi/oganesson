// og:kv, Oganesson's built-in key-value store with Deno KV's API. Data lives
// in the Go host and is shared by every request of the process; values
// are serialized, so reads always return fresh copies.
const host = globalThis[Symbol.for("og.kv")];
if (!host) throw new Error("og:kv is only available on the server");

const MAX_U64 = (1n << 64n) - 1n;

export class KvU64 {
  #value;
  constructor(value) {
    if (typeof value !== "bigint") throw new TypeError("KvU64: value must be a bigint");
    if (value < 0n || value > MAX_U64) throw new RangeError("KvU64: value out of range");
    this.#value = value;
  }
  get value() { return this.#value; }
  valueOf() { return this.#value; }
  toString() { return this.#value.toString(); }
  get [Symbol.toStringTag]() { return "Deno.KvU64"; }
}

// --- keys -----------------------------------------------------------------

function checkKey(key, allowEmpty = false) {
  if (!Array.isArray(key)) throw new TypeError("kv: key must be an array");
  if (!allowEmpty && key.length === 0) throw new TypeError("kv: key must not be empty");
  return key.map((p) => {
    if (typeof p === "string" || typeof p === "bigint" || typeof p === "boolean") return p;
    if (typeof p === "number") return p;
    if (p instanceof Uint8Array) return new Uint8Array(p);
    throw new TypeError(`kv: invalid key part ${String(p)}`);
  });
}

// Go returns byte key parts as ArrayBuffers (never a valid key part itself).
const fromHostKey = (key) => key.map((p) => (p instanceof ArrayBuffer ? new Uint8Array(p) : p));

// --- values: a tagged JSON encoding of the structured-clone types ---------

function encode(value) {
  const ids = new Map();
  const enc = (v) => {
    switch (typeof v) {
      case "string": case "boolean": return v;
      case "number":
        if (Number.isFinite(v) && !Object.is(v, -0)) return v;
        return ["$n", Number.isNaN(v) ? "NaN" : Object.is(v, -0) ? "-0" : v > 0 ? "Inf" : "-Inf"];
      case "bigint": return ["$b", v.toString()];
      case "undefined": return ["$u"];
      case "symbol": case "function": throw new TypeError(`kv: cannot store a ${typeof v}`);
    }
    if (v === null) return null;
    if (v instanceof KvU64) throw new TypeError("kv: KvU64 values can only be stored at the top level");
    if (ids.has(v)) return ["$r", ids.get(v)];
    const id = ids.size;
    ids.set(v, id);
    if (Array.isArray(v)) return ["$a", id, ...v.map(enc)];
    if (v instanceof Date) return ["$d", id, v.getTime()];
    if (v instanceof RegExp) return ["$re", id, v.source, v.flags];
    if (v instanceof Map) return ["$m", id, [...v].map(([k, x]) => [enc(k), enc(x)])];
    if (v instanceof Set) return ["$s", id, [...v].map(enc)];
    if (v instanceof ArrayBuffer) return ["$ab", id, Array.from(new Uint8Array(v))];
    if (ArrayBuffer.isView(v)) return ["$ta", id, v.constructor.name, Array.from(new Uint8Array(v.buffer, v.byteOffset, v.byteLength))];
    if (v instanceof Error) return ["$e", id, v.name, v.message];
    const proto = Object.getPrototypeOf(v);
    if (proto !== Object.prototype && proto !== null) throw new TypeError(`kv: cannot store a ${Object.prototype.toString.call(v)}`);
    const o = {};
    for (const k of Object.keys(v)) o[k] = enc(v[k]);
    return ["$o", id, o];
  };
  return new TextEncoder().encode(JSON.stringify(enc(value)));
}

const typedArrays = { Int8Array, Uint8Array, Uint8ClampedArray, Int16Array, Uint16Array, Int32Array, Uint32Array, Float32Array, Float64Array, BigInt64Array, BigUint64Array, DataView };

function decode(bytes) {
  const refs = [];
  const dec = (v) => {
    if (!Array.isArray(v)) return v;
    switch (v[0]) {
      case "$n": return { NaN: NaN, "-0": -0, Inf: Infinity, "-Inf": -Infinity }[v[1]];
      case "$b": return BigInt(v[1]);
      case "$u": return undefined;
      case "$r": return refs[v[1]];
      case "$a": { const a = []; refs[v[1]] = a; for (const x of v.slice(2)) a.push(dec(x)); return a; }
      case "$d": return (refs[v[1]] = new Date(v[2]));
      case "$re": return (refs[v[1]] = new RegExp(v[2], v[3]));
      case "$m": { const m = new Map(); refs[v[1]] = m; for (const [k, x] of v[2]) m.set(dec(k), dec(x)); return m; }
      case "$s": { const s = new Set(); refs[v[1]] = s; for (const x of v[2]) s.add(dec(x)); return s; }
      case "$ab": return (refs[v[1]] = new Uint8Array(v[2]).buffer);
      case "$ta": {
        const buf = new Uint8Array(v[3]).buffer;
        const C = typedArrays[v[2]] || Uint8Array;
        return (refs[v[1]] = C === DataView ? new DataView(buf) : new C(buf));
      }
      case "$e": { const C = globalThis[v[2]]; const e = typeof C === "function" && C.prototype instanceof Error ? new C(v[3]) : new Error(v[3]); return (refs[v[1]] = e); }
      case "$o": { const o = {}; refs[v[1]] = o; for (const k of Object.keys(v[2])) o[k] = dec(v[2][k]); return o; }
    }
    throw new Error("kv: corrupt stored value");
  };
  return dec(JSON.parse(new TextDecoder().decode(bytes)));
}

const toHostValue = (value) => (value instanceof KvU64 ? { u64: value.value } : { data: encode(value) });
const fromHostValue = (v) => (v.u64 !== undefined ? new KvU64(v.u64) : decode(new Uint8Array(v.data)));

function entry(e, key) {
  if (!e) return { key, value: null, versionstamp: null };
  return { key: fromHostKey(e.key), value: fromHostValue(e.value), versionstamp: e.versionstamp };
}

// --- atomic operations ------------------------------------------------------

function expireIn(options) {
  const ms = options && options.expireIn;
  if (ms === undefined) return 0;
  if (typeof ms !== "number" || !(ms > 0)) throw new TypeError("kv: expireIn must be a positive number of milliseconds");
  return ms;
}

export class AtomicOperation {
  #checks = [];
  #mutations = [];
  check(...checks) {
    for (const c of checks) this.#checks.push({ key: checkKey(c.key), versionstamp: c.versionstamp ?? "" });
    return this;
  }
  mutate(...mutations) {
    for (const m of mutations) {
      const key = checkKey(m.key);
      switch (m.type) {
        case "set": this.#mutations.push({ type: "set", key, value: toHostValue(m.value), expireIn: expireIn(m) }); break;
        case "delete": this.#mutations.push({ type: "delete", key }); break;
        case "sum": case "min": case "max":
          if (!(m.value instanceof KvU64)) throw new TypeError(`kv: ${m.type} requires a KvU64 value`);
          this.#mutations.push({ type: m.type, key, value: { u64: m.value.value } });
          break;
        default: throw new TypeError(`kv: unknown mutation type ${m.type}`);
      }
    }
    return this;
  }
  set(key, value, options) { return this.mutate({ type: "set", key, value, expireIn: options && options.expireIn }); }
  delete(key) { return this.mutate({ type: "delete", key }); }
  sum(key, n) { return this.mutate({ type: "sum", key, value: n instanceof KvU64 ? n : new KvU64(BigInt(n)) }); }
  min(key, n) { return this.mutate({ type: "min", key, value: n instanceof KvU64 ? n : new KvU64(BigInt(n)) }); }
  max(key, n) { return this.mutate({ type: "max", key, value: n instanceof KvU64 ? n : new KvU64(BigInt(n)) }); }
  async commit() {
    const r = host.commit(this.#checks, this.#mutations);
    return r.ok ? { ok: true, versionstamp: r.versionstamp } : { ok: false };
  }
}

// --- the store --------------------------------------------------------------

class KvListIterator {
  #selector; #options; #cursor; #buffer = []; #done = false; #remaining;
  constructor(selector, options) {
    this.#selector = selector;
    this.#options = options;
    this.#cursor = options.cursor;
    this.#remaining = options.limit ?? Infinity;
  }
  get cursor() { return this.#cursor ?? ""; }
  async next() {
    if (this.#buffer.length === 0 && !this.#done && this.#remaining > 0) {
      const batch = Math.min(this.#options.batchSize ?? 100, this.#remaining);
      const r = host.list(this.#selector, { limit: batch, reverse: !!this.#options.reverse, cursor: this.#cursor });
      this.#buffer = r.entries.map((e, i) => ({ e, cursor: r.cursors[i] }));
      if (r.entries.length < batch) this.#done = true;
    }
    const item = this.#buffer.shift();
    if (!item || this.#remaining <= 0) return { done: true, value: undefined };
    this.#remaining--;
    this.#cursor = item.cursor;
    return { done: false, value: entry(item.e) };
  }
  [Symbol.asyncIterator]() { return this; }
}

export class Kv {
  async get(key) {
    const k = checkKey(key);
    return entry(host.get(k), k);
  }
  async getMany(keys) {
    return Promise.all(keys.map((k) => this.get(k)));
  }
  async set(key, value, options) {
    return new AtomicOperation().set(key, value, options).commit();
  }
  async delete(key) {
    await new AtomicOperation().delete(key).commit();
  }
  list(selector, options = {}) {
    if (!selector || typeof selector !== "object") throw new TypeError("kv: list requires a selector");
    const sel = {};
    if (selector.prefix) sel.prefix = checkKey(selector.prefix, true);
    if (selector.start) sel.start = checkKey(selector.start);
    if (selector.end) sel.end = checkKey(selector.end);
    if (!sel.prefix && !(sel.start && sel.end)) throw new TypeError("kv: list selector needs a prefix, or both start and end");
    return new KvListIterator(sel, options);
  }
  atomic() { return new AtomicOperation(); }
  close() {}
  [Symbol.dispose]() {}
}

const shared = new Kv();

/** Returns the process-wide store (the path argument is accepted for Deno compatibility). */
export async function openKv(_path) {
  return shared;
}
