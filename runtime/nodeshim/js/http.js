// node:http: unenv's module, with a server that runs inside a request's VM.
// A program calls http.createServer(listener).listen(...) as under Node; no
// socket is opened. The runtime hands each request to the listening server
// (serve below) as an IncomingMessage and a ServerResponse whose body
// streams into a web Response.
"use strict";
const base = require("unenv:node/http");
const { EventEmitter } = require("events");
const { Buffer } = require("buffer");

let announce;
let started = false;
// Resolves with the first server that listens.
const listening = new Promise((resolve) => (announce = resolve));

class Server extends EventEmitter {
  constructor(options, listener) {
    super();
    if (typeof options === "function") listener = options;
    if (listener) this.on("request", listener);
    this.listening = false;
    this.timeout = 0;
    this.keepAliveTimeout = 5000;
    this.headersTimeout = 60000;
    this.requestTimeout = 300000;
  }
  listen(...args) {
    const cb = args.find((a) => typeof a === "function");
    if (cb) this.once("listening", cb);
    this.listening = true;
    started = true;
    queueMicrotask(() => {
      this.emit("listening");
      announce(this);
    });
    return this;
  }
  // The port requests arrive on (the incoming request's, as og's listener
  // has no other; PORT before the first request).
  address() {
    return { address: "::", family: "IPv6", port: this._ogPort || Number(process.env.PORT) || 3000 };
  }
  close(cb) {
    this.listening = false;
    queueMicrotask(() => {
      this.emit("close");
      if (cb) cb();
    });
    return this;
  }
  closeAllConnections() {}
  closeIdleConnections() {}
  setTimeout(ms, cb) {
    this.timeout = ms;
    if (cb) this.on("timeout", cb);
    return this;
  }
  ref() {
    return this;
  }
  unref() {
    return this;
  }
}

const createServer = (options, listener) => new Server(options, listener);

// The response: Node's ServerResponse surface (unenv's), whose writes go
// to a web ReadableStream; the head is fixed at writeHead or the first
// write / end, as Node sends it.
class ServerResponse extends base.ServerResponse {
  constructor(req) {
    super(req);
    this.body = new ReadableStream({ start: (c) => (this._ogBody = c) });
    this._ogHeadSent = false;
    this.onHead = null;
  }
  _ogHead() {
    if (this._ogHeadSent) return;
    this._ogHeadSent = true;
    this.headersSent = true;
    if (this.onHead) this.onHead();
  }
  writeHead(statusCode, arg1, arg2) {
    super.writeHead(statusCode, arg1, arg2);
    if (Array.isArray(arg2 || arg1)) {
      const raw = arg2 || arg1;
      for (let i = 0; i + 1 < raw.length; i += 2) this.appendHeader(String(raw[i]), raw[i + 1]);
    }
    this._ogHead();
    return this;
  }
  _write(chunk, encoding, cb) {
    this._ogHead();
    const bytes = typeof chunk === "string" ? Buffer.from(chunk, encoding) : chunk;
    if (bytes.length) this._ogBody.enqueue(new Uint8Array(bytes.buffer, bytes.byteOffset, bytes.byteLength));
    cb();
  }
  _final(cb) {
    this._ogHead();
    this.finished = true;
    this._ogBody.close();
    cb();
  }
  get writableEnded() {
    return this.finished || super.writableEnded;
  }
}

// The request's socket: no connection behind it, but the surface servers
// touch (addresses, destroy, timeouts, events).
class Socket extends EventEmitter {
  constructor(encrypted) {
    super();
    this.remoteAddress = "127.0.0.1";
    this.remotePort = 0;
    this.remoteFamily = "IPv4";
    this.localAddress = "127.0.0.1";
    this.localPort = Number(process.env.PORT) || 3000;
    this.encrypted = encrypted;
    this.readable = true;
    this.writable = true;
    this.destroyed = false;
  }
  destroy(err) {
    if (this.destroyed) return this;
    this.destroyed = true;
    this.readable = this.writable = false;
    queueMicrotask(() => {
      if (err) this.emit("error", err);
      this.emit("close", !!err);
    });
    return this;
  }
  end() {
    return this.destroy();
  }
  setTimeout() {
    return this;
  }
  setNoDelay() {
    return this;
  }
  setKeepAlive() {
    return this;
  }
  ref() {
    return this;
  }
  unref() {
    return this;
  }
  address() {
    return { address: this.localAddress, family: "IPv4", port: this.localPort };
  }
}

// unenv declares readable, closed and errored as class fields, which
// shadow the Readable getters (so readable is always false, and
// Readable.toWeb(req) is an empty, cancelled stream), and complete as
// true. Node's complete turns true once the whole message has arrived.
class IncomingMessage extends base.IncomingMessage {
  constructor(socket) {
    super(socket);
    delete this.readable;
    delete this.closed;
    delete this.errored;
    this.complete = false;
  }
}

// endBody ends req's body: all of the message has arrived.
function endBody(req) {
  req.complete = true;
  req.push(null);
}

// serve(request, program) answers a web Request through the server the
// program (the promise of its run) listens with; it fails if the program
// fails or ends without listening.
async function serve(request, program) {
  const server = await (program
    ? Promise.race([
        listening,
        program.then(() => {
          if (!started) throw new Error("og: the server program ended without calling listen()");
          return listening;
        }),
      ])
    : listening);
  const url = new URL(request.url);
  const port = Number(url.port) || (url.protocol === "https:" ? 443 : 80);
  server._ogPort = port;
  const socket = new Socket(url.protocol === "https:");
  socket.localPort = port;
  const req = new IncomingMessage(socket);
  req.method = request.method;
  req.url = url.pathname + url.search;
  const headers = {};
  for (const [k, v] of request.headers) headers[k] = k in headers ? headers[k] + ", " + v : v;
  if (!("host" in headers)) headers.host = url.host;
  req.headers = headers;
  if (request.body) {
    (async () => {
      const reader = request.body.getReader();
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        req.push(Buffer.from(value));
      }
      endBody(req);
    })().catch((e) => req.destroy(e));
  } else {
    endBody(req);
  }
  const res = new ServerResponse(req);
  res.socket = res.connection = req.socket;
  return new Promise((resolve, reject) => {
    res.onHead = () => {
      const h = new Headers();
      for (const [k, v] of Object.entries(res.getHeaders())) {
        if (v === undefined) continue;
        for (const one of Array.isArray(v) ? v : [v]) h.append(k, String(one));
      }
      const empty = req.method === "HEAD" || res.statusCode === 204 || res.statusCode === 304;
      resolve(new Response(empty ? null : res.body, { status: res.statusCode, statusText: res.statusMessage || undefined, headers: h }));
    };
    res.on("error", reject);
    // Everything the program printed until now was its startup, which the
    // runtime shows once per process (as Node runs a server once).
    globalThis[Symbol.for("og.startupDone")]?.();
    try {
      server.emit("request", req, res);
    } catch (e) {
      reject(e);
    }
  });
}

// validateHeaderName / validateHeaderValue, as Node's (lib/_http_outgoing.js
// and internal/http's token and header-character checks), with its errors.
const tokenRe = /^[\^_`a-zA-Z\-0-9!#$%&'*+.|~]+$/;
const invalidHeaderCharRe = /[^\t\x20-\x7e\x80-\xff]/;
function validateHeaderName(name, label = "Header name") {
  if (typeof name !== "string" || !name || !tokenRe.test(name)) {
    const e = new TypeError(`${label} must be a valid HTTP token ["${name}"]`);
    e.code = "ERR_INVALID_HTTP_TOKEN";
    throw e;
  }
}
function validateHeaderValue(name, value) {
  if (value === undefined) {
    const e = new TypeError(`Invalid value "${value}" for header "${name}"`);
    e.code = "ERR_HTTP_INVALID_HEADER_VALUE";
    throw e;
  }
  if (invalidHeaderCharRe.test(String(value))) {
    const e = new TypeError(`Invalid character in header content ["${name}"]`);
    e.code = "ERR_INVALID_CHAR";
    throw e;
  }
}

module.exports = Object.assign({}, base, { Server, createServer, ServerResponse, IncomingMessage, serve, validateHeaderName, validateHeaderValue });
