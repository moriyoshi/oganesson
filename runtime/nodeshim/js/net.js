// node:net: unenv's module with a client Socket over Go connections
// (Symbol.for("og.net"), runtime/nodeshim/net.go): connect / createConnection,
// a Duplex with Node's events ("connect", "ready", "data", "end", "close"
// with hadError, "timeout"), half-close, setTimeout, setNoDelay,
// setKeepAlive, ref / unref, and isIP. Servers stay unenv's (og serves
// HTTP itself). js/tls.js upgrades a Socket in place.
"use strict";
const base = require("unenv:node/net");
const { Duplex } = require("stream");
const { Buffer } = require("buffer");

const host = () => {
  const h = globalThis[Symbol.for("og.net")];
  if (!h) throw new Error("og: node:net sockets are not available in this runtime");
  return h;
};

function systemError(info) {
  const err = new Error(info.message);
  for (const k of ["code", "errno", "syscall", "address", "port", "hostname"]) if (info[k] !== undefined) err[k] = info[k];
  return err;
}

function badPort(port) {
  const err = new RangeError(`options.port should be >= 0 and < 65536. Received type ${typeof port} (${JSON.stringify(port)}).`);
  err.code = "ERR_SOCKET_BAD_PORT";
  return err;
}

// normalizeArgs is Node's: (options[, cb]), (port[, host][, cb]), (path[, cb]).
function normalizeArgs(args) {
  if (args.length === 0) return [{}, null];
  const arg0 = args[0];
  let options = {};
  if (typeof arg0 === "object" && arg0 !== null) options = arg0;
  else if (typeof arg0 === "string" && Number.isNaN(Number(arg0))) options.path = arg0;
  else {
    options.port = arg0;
    if (args.length > 1 && typeof args[1] === "string") options.host = args[1];
  }
  const cb = args[args.length - 1];
  return [options, typeof cb === "function" ? cb : null];
}

const kId = Symbol("og.net.id");
const kTLS = Symbol("og.net.tls");

class Socket extends Duplex {
  constructor(options = {}) {
    if (typeof options === "number") options = { fd: options };
    super({
      allowHalfOpen: !!options.allowHalfOpen,
      emitClose: false,
      autoDestroy: true,
      decodeStrings: false,
      readableHighWaterMark: options.readableHighWaterMark,
      writableHighWaterMark: options.writableHighWaterMark,
    });
    if (options.fd !== undefined) throw new Error("og: net.Socket({ fd }) is not supported");
    this[kId] = null;
    this.connecting = false;
    this.bytesRead = 0;
    this._bytesWritten = 0;
    this._info = {};
    this._timeoutMs = 0;
    this._timer = null;
    this._noDelay = null;
    this._keepAlive = null;
    this._wantRead = false;
    this.server = null;
    this._server = null;
    if (options.signal) {
      const onAbort = () => this.destroy(Object.assign(new Error("The operation was aborted"), { name: "AbortError", code: "ABORT_ERR" }));
      if (options.signal.aborted) process.nextTick(onAbort);
      else options.signal.addEventListener("abort", onAbort, { once: true });
    }
  }

  connect(...args) {
    const [options, cb] = Array.isArray(args[0]) ? args[0] : normalizeArgs(args);
    if (this.destroyed) throw Object.assign(new Error("Socket is closed"), { code: "ERR_SOCKET_CLOSED" });
    if (cb) this.once("connect", cb);
    const opts = { timeout: 0 };
    if (options.path) opts.path = String(options.path);
    else {
      let port = options.port;
      if (typeof port === "string" && port.trim() !== "") port = Number(port);
      if (typeof port !== "number" || !Number.isInteger(port) || port < 0 || port > 65535) throw badPort(options.port);
      opts.port = port;
      opts.host = options.host || "localhost";
      this._host = opts.host;
      if (options.family) opts.family = Number(options.family);
      if (options.localAddress) opts.localAddress = options.localAddress;
      if (options.localPort) opts.localPort = options.localPort;
    }
    // tls.js: a TLSSocket's own connection handshakes before any data.
    if (options[kTLS]) opts.tls = options[kTLS];
    if (options.timeout) this.setTimeout(options.timeout);
    if (options.noDelay !== undefined) this._noDelay = !!options.noDelay;
    if (options.keepAlive) this._keepAlive = [true, options.keepAliveInitialDelay || 0];
    this.connecting = true;
    this[kId] = host().connect(opts, (type, a) => this._event(type, a));
    return this;
  }

  _event(type, a) {
    switch (type) {
      case "connect":
        this._info = a;
        this.connecting = false;
        if (this._noDelay !== null) host().setNoDelay(this[kId], this._noDelay);
        if (this._keepAlive) host().setKeepAlive(this[kId], ...this._keepAlive);
        this._touch();
        this.emit("connect");
        this.emit("ready");
        return;
      case "data": {
        const buf = Buffer.from(a);
        this.bytesRead += buf.length;
        this._touch();
        if (!this.push(buf)) host().pause(this[kId]);
        return;
      }
      case "end":
        this.push(null);
        return;
      case "error":
        this.destroy(systemError(a));
        return;
    }
  }

  _onConnected(fn) {
    if (this.connecting) this.once("connect", fn);
    else fn();
  }

  _read() {
    this._wantRead = true;
    if (this[kId] !== null && !this.connecting) host().resume(this[kId]);
  }

  _write(chunk, encoding, cb) {
    const data = typeof chunk === "string" ? Buffer.from(chunk, encoding) : chunk;
    this._onConnected(() => {
      if (this[kId] === null) return cb(Object.assign(new Error("This socket has been ended by the other party"), { code: "EPIPE" }));
      host().write(this[kId], new Uint8Array(data.buffer, data.byteOffset, data.byteLength), (err) => {
        if (err) return cb(systemError(err));
        this._bytesWritten += data.byteLength;
        this._touch();
        cb();
      });
    });
  }

  _final(cb) {
    this._onConnected(() => {
      if (this[kId] === null) return cb();
      host().shutdown(this[kId], (err) => cb(err ? systemError(err) : null));
    });
  }

  _destroy(err, cb) {
    this._clearTimer();
    if (this[kId] !== null) host().close(this[kId], !!this._reset);
    this[kId] = null;
    this.connecting = false;
    cb(err);
    process.nextTick(() => this.emit("close", !!err));
  }

  // Socket timeouts are idle timers: "timeout" after ms without activity.
  setTimeout(ms, cb) {
    this._timeoutMs = Number(ms) || 0;
    if (cb) {
      if (this._timeoutMs === 0) this.removeListener("timeout", cb);
      else this.once("timeout", cb);
    }
    this._touch();
    return this;
  }
  get timeout() {
    return this._timeoutMs || undefined;
  }
  _clearTimer() {
    if (this._timer) clearTimeout(this._timer);
    this._timer = null;
  }
  _touch() {
    this._clearTimer();
    if (!this._timeoutMs || this.destroyed) return;
    this._timer = setTimeout(() => this.emit("timeout"), this._timeoutMs);
    this._timer.unref?.();
  }

  setNoDelay(on = true) {
    this._noDelay = !!on;
    if (this[kId] !== null && !this.connecting) host().setNoDelay(this[kId], this._noDelay);
    return this;
  }
  setKeepAlive(on = false, delay = 0) {
    this._keepAlive = [!!on, Math.floor(Number(delay) || 0)];
    if (this[kId] !== null && !this.connecting) host().setKeepAlive(this[kId], ...this._keepAlive);
    return this;
  }
  // Sockets never keep a request's VM alive after its response (they are
  // weak handles of the loop), so ref / unref change nothing.
  ref() {
    return this;
  }
  unref() {
    return this;
  }
  // As Node's _getpeername / _getsockname: the peer is remembered once read
  // while connected; neither is known after the socket closed otherwise.
  _getpeername() {
    if (this[kId] === null || this.connecting || !this._info.remoteFamily && !this._info.remoteAddress) return this._peername || {};
    this._peername ??= { address: this._info.remoteAddress, family: this._info.remoteFamily, port: this._info.remotePort };
    return this._peername;
  }
  _getsockname() {
    if (this[kId] === null || !this._info.localAddress) return {};
    this._sockname ??= { address: this._info.localAddress, family: this._info.localFamily, port: this._info.localPort };
    return this._sockname;
  }
  address() {
    return this._getsockname();
  }
  get remoteAddress() {
    return this._getpeername().address;
  }
  get remotePort() {
    return this._getpeername().port;
  }
  get remoteFamily() {
    return this._getpeername().family;
  }
  get localAddress() {
    return this._getsockname().address;
  }
  get localPort() {
    return this._getsockname().port;
  }
  get localFamily() {
    return this._getsockname().family;
  }
  get bytesWritten() {
    return this._bytesWritten;
  }
  get bufferSize() {
    return this.writableLength;
  }
  get pending() {
    return this[kId] === null || this.connecting;
  }
  get readyState() {
    if (this.connecting) return "opening";
    if (this.readable && this.writable) return "open";
    if (this.readable && !this.writable) return "readOnly";
    if (!this.readable && this.writable) return "writeOnly";
    return "closed";
  }
  destroySoon() {
    if (this.writable) this.end();
    if (this.writableFinished) this.destroy();
    else this.once("finish", () => this.destroy());
  }
  resetAndDestroy() {
    this._reset = true;
    return this.destroy();
  }
}

function connect(...args) {
  const [options, cb] = normalizeArgs(args);
  const socket = new Socket(options);
  if (options.timeout) socket.setTimeout(options.timeout);
  return socket.connect([options, cb]);
}

const isIP = (input) => (globalThis[Symbol.for("og.net")] ? host().isIP(String(input)) : base.isIP(String(input)));
const isIPv4 = (input) => isIP(input) === 4;
const isIPv6 = (input) => isIP(input) === 6;

let autoSelectFamily = true;
let autoSelectFamilyAttemptTimeout = 250;

module.exports = Object.assign({}, base, {
  Socket,
  Stream: Socket,
  connect,
  createConnection: connect,
  isIP,
  isIPv4,
  isIPv6,
  getDefaultAutoSelectFamily: () => autoSelectFamily,
  setDefaultAutoSelectFamily: (v) => void (autoSelectFamily = !!v),
  getDefaultAutoSelectFamilyAttemptTimeout: () => autoSelectFamilyAttemptTimeout,
  setDefaultAutoSelectFamilyAttemptTimeout: (v) => void (autoSelectFamilyAttemptTimeout = Math.max(10, Number(v) || 0)),
  _normalizeArgs: normalizeArgs,
  [Symbol.for("og.net.internals")]: { kId, kTLS, host, systemError },
});
