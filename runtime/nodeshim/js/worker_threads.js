// node:worker_threads: unenv's module, with Worker, parentPort and
// workerData backed by the host's workers where it has them
// (Symbol.for("og.workers"): internal/jshost runs each worker in its own
// engine). Messages cross engines serialized: structured-clone data (plain
// objects, arrays, primitives, typed arrays, ArrayBuffer, Map, Set, Date,
// RegExp, BigInt, Error, undefined).
"use strict";
const base = require("unenv:node/worker_threads");
const { EventEmitter } = require("events");
const host = globalThis[Symbol.for("og.workers")];
const ctx = globalThis[Symbol.for("og.workerContext")];

const TAG = "__og_wt__";
// encode serializes a structured-clone value to JSON. Objects keep their
// identity: each gets an id, and later occurrences (shared references,
// cycles) are references to it, as structured clone preserves them.
function encode(value) {
  const ids = new Map();
  const walk = (v) => {
    if (v === undefined) return { [TAG]: "undefined" };
    if (typeof v === "bigint") return { [TAG]: "bigint", v: String(v) };
    if (typeof v === "number" && !Number.isFinite(v)) return { [TAG]: "number", v: String(v) };
    if (v === null || typeof v !== "object") return v;
    if (ids.has(v)) return { [TAG]: "ref", id: ids.get(v) };
    const id = ids.size;
    ids.set(v, id);
    if (ArrayBuffer.isView(v)) return { [TAG]: "view", id, type: v.constructor.name, v: Array.from(new Uint8Array(v.buffer, v.byteOffset, v.byteLength)) };
    if (v instanceof ArrayBuffer) return { [TAG]: "ab", id, v: Array.from(new Uint8Array(v)) };
    if (v instanceof Map) return { [TAG]: "map", id, v: [...v].map(([k, x]) => [walk(k), walk(x)]) };
    if (v instanceof Set) return { [TAG]: "set", id, v: [...v].map(walk) };
    if (v instanceof Date) return { [TAG]: "date", id, v: v.getTime() };
    if (v instanceof RegExp) return { [TAG]: "regexp", id, source: v.source, flags: v.flags };
    if (v instanceof Error) return { [TAG]: "error", id, name: v.name, message: v.message, stack: v.stack };
    if (Array.isArray(v)) return { [TAG]: "array", id, v: v.map(walk) };
    const out = {};
    for (const k of Object.keys(v)) out[k] = walk(v[k]);
    return { [TAG]: "object", id, v: out };
  };
  return JSON.stringify(walk(value));
}
function decode(s) {
  const refs = [];
  // Containers are registered before their members are decoded, so a
  // member may refer back to them.
  const walk = (v) => {
    if (v === null || typeof v !== "object") return v;
    switch (v[TAG]) {
      case "undefined": return undefined;
      case "bigint": return BigInt(v.v);
      case "number": return Number(v.v);
      case "ref": return refs[v.id];
      case "view": {
        const bytes = new Uint8Array(v.v);
        const C = v.type === "Buffer" ? Uint8Array : globalThis[v.type] || Uint8Array;
        return (refs[v.id] = C === Uint8Array ? bytes : new C(bytes.buffer, 0, bytes.byteLength / (C.BYTES_PER_ELEMENT || 1)));
      }
      case "ab": return (refs[v.id] = new Uint8Array(v.v).buffer);
      case "map": {
        const m = (refs[v.id] = new Map());
        for (const [k, x] of v.v) m.set(walk(k), walk(x));
        return m;
      }
      case "set": {
        const out = (refs[v.id] = new Set());
        for (const x of v.v) out.add(walk(x));
        return out;
      }
      case "date": return (refs[v.id] = new Date(v.v));
      case "regexp": return (refs[v.id] = new RegExp(v.source, v.flags));
      case "error": return (refs[v.id] = Object.assign(new (globalThis[v.name] || Error)(v.message), { stack: v.stack }));
      case "array": {
        const out = (refs[v.id] = []);
        for (const x of v.v) out.push(walk(x));
        return out;
      }
      case "object": {
        const out = (refs[v.id] = {});
        for (const k of Object.keys(v.v)) out[k] = walk(v.v[k]);
        return out;
      }
    }
    return v;
  };
  return s === undefined ? undefined : walk(JSON.parse(s));
}

let isMainThread = true;
let parentPort = null;
let workerData = null;
let threadId = 0;
if (ctx) {
  isMainThread = false;
  threadId = ctx.id;
  workerData = decode(ctx.workerData);
  // parentPort is a MessagePort in Node: messages that arrive before a
  // "message" listener wait for one (adding it starts the port), in order.
  // A worker that sets its listener up after an await (Qwik City's static
  // generation) gets the messages sent meanwhile.
  //
  // It also keeps the worker alive exactly while it has "message"
  // listeners and is not unref'd (a referenced MessagePort): a worker whose
  // loop is otherwise idle ends, with exit code 0.
  class ParentPort extends EventEmitter {
    constructor() {
      super();
      this._queue = [];
      this._unref = false;
      this._held = false;
      this.on("newListener", (event) => {
        if (event !== "message") return;
        // (newListener fires before the listener is added.)
        queueMicrotask(() => this._sync());
        if (this._queue.length > 0) queueMicrotask(() => this._flush());
      });
      this.on("removeListener", (event) => {
        if (event === "message") this._sync();
      });
    }
    _sync() {
      const want = !this._unref && this.listenerCount("message") > 0;
      if (want !== this._held) {
        this._held = want;
        ctx.hold(want);
      }
    }
    _flush() {
      while (this._queue.length > 0 && this.listenerCount("message") > 0) this.emit("message", this._queue.shift());
    }
    _receive(value) {
      if (this.listenerCount("message") > 0 && this._queue.length === 0) this.emit("message", value);
      else this._queue.push(value);
    }
    postMessage(value) {
      ctx.post(encode(value));
    }
    close() {
      ctx.close();
    }
    start() {}
    ref() {
      this._unref = false;
      this._sync();
    }
    unref() {
      this._unref = true;
      this._sync();
    }
  }
  parentPort = new ParentPort();
  ctx.onMessage = (s) => parentPort._receive(decode(s));
}

class Worker extends EventEmitter {
  constructor(filename, options = {}) {
    super();
    if (!host) throw new Error("og: worker_threads.Worker is not available in this runtime");
    if (options.eval) throw new Error("og: worker_threads.Worker with eval: true is not supported");
    let file = filename instanceof URL ? decodeURIComponent(filename.pathname) : String(filename);
    if (file.startsWith("file://")) file = decodeURIComponent(new URL(file).pathname);
    if (!file.startsWith("/")) file = require("path").resolve(process.cwd(), file);
    // env: the worker's process.env (values as strings), or SHARE_ENV /
    // absent: the parent's.
    let env = "";
    if (options.env && typeof options.env === "object") {
      env = JSON.stringify(Object.fromEntries(Object.entries(options.env).filter(([, v]) => v !== undefined).map(([k, v]) => [k, String(v)])));
    }
    this.threadId = host.spawn(file, encode(options.workerData), {
      online: () => this.emit("online"),
      message: (s) => this.emit("message", decode(s)),
      error: (message) => this.emit("error", new Error(message)),
      exit: (code) => {
        this._exited = true;
        this.emit("exit", code);
      },
    }, env);
  }
  postMessage(value) {
    host.post(this.threadId, encode(value));
  }
  // (A worker that has already ended: nothing to wait for.)
  terminate() {
    if (this._exited) return Promise.resolve(undefined);
    return new Promise((resolve) => {
      this.once("exit", resolve);
      host.terminate(this.threadId);
    });
  }
  ref() {
    host.ref(this.threadId, true);
    return this;
  }
  unref() {
    host.ref(this.threadId, false);
    return this;
  }
}

module.exports = Object.assign({}, base, { Worker, isMainThread, parentPort, workerData, threadId });
