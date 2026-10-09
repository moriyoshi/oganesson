// Node-style globals for bundles that expect a Node environment.
"use strict";
globalThis.global = globalThis;
if (typeof globalThis.Buffer === "undefined") {
  const { Buffer } = require("buffer");
  // Node's Buffer#indexOf/lastIndexOf/includes accept any Uint8Array; the
  // feross implementation only accepts Buffers. Next's stream helpers rely
  // on Node's behavior.
  for (const name of ["indexOf", "lastIndexOf", "includes"]) {
    const orig = Buffer.prototype[name];
    Buffer.prototype[name] = function (val, byteOffset, encoding) {
      if (val instanceof Uint8Array && !Buffer.isBuffer(val)) val = Buffer.from(val.buffer, val.byteOffset, val.byteLength);
      return orig.call(this, val, byteOffset, encoding);
    };
  }
  globalThis.Buffer = Buffer;
}

// process.stdout / stderr (the minimal process has neither): non-TTY
// streams writing to the console, as Node's are when not on a terminal
// (debug, morgan and loggers read their fd / isTTY or write to them).
if (globalThis.process && !globalThis.process.stdout) {
  const { Writable } = require("stream");
  const out = (fd, log) => {
    const w = new Writable({
      write(chunk, encoding, cb) {
        const s = typeof chunk === "string" ? chunk : Buffer.from(chunk).toString("utf8");
        log(s.endsWith("\n") ? s.slice(0, -1) : s);
        cb();
      },
    });
    w.fd = fd;
    w.isTTY = false;
    return w;
  };
  globalThis.process.stdout = out(1, (s) => console.log(s));
  globalThis.process.stderr = out(2, (s) => console.error(s));
}

// process.stdin: unenv's is a bare tty.ReadStream without events (and the
// minimal process has none). A non-TTY Readable that never receives
// input, created on first use (Vite listens for its "end" to shut down).
if (globalThis.process && !(globalThis.process.stdin && typeof globalThis.process.stdin.on === "function")) {
  let stdin;
  Object.defineProperty(globalThis.process, "stdin", {
    configurable: true,
    enumerable: true,
    get() {
      if (!stdin) {
        const { Readable } = require("stream");
        stdin = new Readable({ read() {} });
        stdin.fd = 0;
        stdin.isTTY = false;
      }
      return stdin;
    },
  });
}

// process.hrtime and hrtime.bigint (the minimal process has neither;
// morgan and response-time measure with them), from performance.now().
if (globalThis.process && typeof globalThis.process.hrtime !== "function") {
  const nowNs = () => BigInt(Math.round(performance.now() * 1e6));
  const hrtime = (prev) => {
    const ns = nowNs();
    const t = [Number(ns / 1000000000n), Number(ns % 1000000000n)];
    if (!prev) return t;
    let s = t[0] - prev[0], n = t[1] - prev[1];
    if (n < 0) {
      s--;
      n += 1e9;
    }
    return [s, n];
  };
  hrtime.bigint = nowNs;
  globalThis.process.hrtime = hrtime;
}

// process.versions.node, matching process.version (code feature-detects
// Node by it).
if (globalThis.process && globalThis.process.versions && !globalThis.process.versions.node && typeof globalThis.process.version === "string") {
  globalThis.process.versions.node = globalThis.process.version.replace(/^v/, "");
}

// process.execPath and argv0 (the minimal process has neither): og
// presents itself as Node on Linux (process.platform, version), so a Linux
// install's path. Nothing can spawn it (there is no child_process), but code
// that inspects it (Prisma's CLI looks for npm's prefix from it) gets one.
if (globalThis.process && typeof globalThis.process.execPath !== "string") {
  globalThis.process.execPath = "/usr/local/bin/node";
}
// Uncaught errors reach process's listeners as in Node (the engine offers
// each to globalThis[Symbol.for("og.uncaught")], runtime/engine): an
// exception to "uncaughtException", a rejection to "unhandledRejection",
// else to "uncaughtException" with origin "unhandledRejection" (Node's
// default since 15). Without a listener, the engine's own handling stays.
if (globalThis.process) {
  Object.defineProperty(globalThis, Symbol.for("og.uncaught"), {
    configurable: true,
    value(err, promise) {
      const p = globalThis.process;
      if (!p || typeof p.listenerCount !== "function") return false;
      if (promise !== undefined) {
        if (p.listenerCount("unhandledRejection") > 0) {
          p.emit("unhandledRejection", err, promise);
          return true;
        }
        if (p.listenerCount("uncaughtException") > 0) {
          p.emit("uncaughtException", err, "unhandledRejection");
          return true;
        }
        return false;
      }
      if (p.listenerCount("uncaughtException") > 0) {
        p.emit("uncaughtException", err, "uncaughtException");
        return true;
      }
      return false;
    },
  });
}

// process.release, as Node's for the version og reports: code tells Node
// from browsers by release.name (Compiled's isNodeEnvironment, which picks
// its server style cache).
if (globalThis.process && !globalThis.process.release) {
  const v = String(globalThis.process.version || "v22.0.0");
  const lts = { 18: "Hydrogen", 20: "Iron", 22: "Jod", 24: "Krypton" }[Number(v.slice(1).split(".")[0])];
  globalThis.process.release = {
    name: "node",
    ...(lts ? { lts } : {}),
    sourceUrl: `https://nodejs.org/download/release/${v}/node-${v}.tar.gz`,
    headersUrl: `https://nodejs.org/download/release/${v}/node-${v}-headers.tar.gz`,
  };
}
if (globalThis.process && typeof globalThis.process.argv0 !== "string") {
  globalThis.process.argv0 = "node";
}

// process.report (diagnostic reports): og presents itself as Node on
// Linux with glibc, which is what detect-libc (sharp, lightningcss, the
// Payload CMS config) reads from a report's header. A report has the
// header and empty sections.
if (globalThis.process && !globalThis.process.report) {
  globalThis.process.report = {
    compact: false,
    directory: "",
    filename: "",
    reportOnFatalError: false,
    reportOnSignal: false,
    reportOnUncaughtException: false,
    signal: "SIGUSR2",
    excludeNetwork: false,
    getReport() {
      const p = globalThis.process;
      return {
        header: {
          reportVersion: 5,
          event: "JavaScript API",
          trigger: "GetReport",
          nodejsVersion: p.version,
          platform: p.platform,
          arch: p.arch,
          processId: p.pid,
          cwd: typeof p.cwd === "function" ? p.cwd() : "/",
          commandLine: Array.isArray(p.argv) ? p.argv.slice() : [],
          glibcVersionRuntime: "2.39",
          glibcVersionCompiler: "2.28",
          osName: "Linux",
        },
        javascriptStack: {},
        javascriptHeap: {},
        nativeStack: [],
        resourceUsage: {},
        libuv: [],
        workers: [],
        environmentVariables: { ...(p.env || {}) },
        userLimits: {},
        sharedObjects: [],
      };
    },
    writeReport() {
      return "";
    },
  };
}

// process.binding, deprecated but still in Node: older packages read
// constants through it (tmp's process.binding("constants"), Buffer
// polyfills' process.binding("buffer")). Other modules throw as Node's does.
if (globalThis.process && typeof globalThis.process.binding !== "function") {
  globalThis.process.binding = function binding(name) {
    switch (name) {
      case "constants":
        return { os: require("os").constants || {}, fs: require("fs").constants || {}, crypto: {}, zlib: {}, trace: {}, internal: {} };
      case "buffer":
        return { kMaxLength: 2 ** 32, kStringMaxLength: 2 ** 29 - 24 };
    }
    throw new Error(`No such module: ${name}`);
  };
}

// process is an EventEmitter in Node (signal-exit, tmp and Prisma's CLI
// call process.addListener, emit, listeners); the minimal process only has
// no-op on / once / off. Events og does not raise are never emitted.
if (globalThis.process && typeof globalThis.process.addListener !== "function") {
  const { EventEmitter } = require("events");
  const p = globalThis.process;
  const em = new EventEmitter();
  em.setMaxListeners(0);
  for (const name of ["on", "addListener", "once", "prependListener", "prependOnceListener", "off", "removeListener", "removeAllListeners", "emit", "listeners", "rawListeners", "listenerCount", "eventNames", "setMaxListeners", "getMaxListeners"]) {
    const fn = em[name];
    p[name] = function (...args) {
      const r = fn.apply(em, args);
      return r === em ? p : r;
    };
  }
}

// Object.prototype.__defineGetter__ and friends (ECMAScript Annex B, which
// Goja omits; delegates, used by Koa, defines accessors with them).
if (!Object.prototype.__defineGetter__) {
  const define = (name, fn) => Object.defineProperty(Object.prototype, name, { value: fn, writable: true, configurable: true, enumerable: false });
  define("__defineGetter__", function (key, get) {
    Object.defineProperty(this, key, { get, enumerable: true, configurable: true });
  });
  define("__defineSetter__", function (key, set) {
    Object.defineProperty(this, key, { set, enumerable: true, configurable: true });
  });
  const lookup = (kind) =>
    function (key) {
      for (let o = this; o != null; o = Object.getPrototypeOf(o)) {
        const d = Object.getOwnPropertyDescriptor(o, key);
        if (d) return d[kind];
      }
      return undefined;
    };
  define("__lookupGetter__", lookup("get"));
  define("__lookupSetter__", lookup("set"));
}
