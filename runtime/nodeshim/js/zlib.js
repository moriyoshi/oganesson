// node:zlib: unenv's module with gzip/deflate/raw deflate implemented over
// Go's compress/* when the host provides it (Symbol.for("og.zlib")).
"use strict";
const host = globalThis[Symbol.for("og.zlib")];
const { Buffer } = require("buffer");

function op(name) {
  return (data, opts) => {
    if (!host) throw new Error(`og: zlib.${name} is not available in this runtime`);

    const input = typeof data === "string" ? new TextEncoder().encode(data) : new Uint8Array(data.buffer ? data.buffer.slice(data.byteOffset, data.byteOffset + data.byteLength) : data);
    return Buffer.from(host[name](input, (opts && opts.level) ?? -1));
  };
}

// unenv supplies the full surface (constants, classes, stream factories).
const zlib = Object.assign({}, require("unenv:node/zlib"));
// Brotli's quality comes from params[BROTLI_PARAM_QUALITY] (Node's
// default: 11), passed to the host as the level.
const brotliLevel = (opts) => {
  const q = opts && opts.params && opts.params[zlibConstants().BROTLI_PARAM_QUALITY];
  return { level: typeof q === "number" ? q : 11 };
};
function zlibConstants() {
  return require("unenv:node/zlib").constants || {};
}
for (const [name, impl] of [["gzip", "gzip"], ["gunzip", "gunzip"], ["deflate", "deflate"], ["inflate", "inflate"], ["deflateRaw", "deflateRaw"], ["inflateRaw", "inflateRaw"], ["unzip", "gunzip"], ["brotliCompress", "brotliCompress"], ["brotliDecompress", "brotliDecompress"]]) {
  const sync = impl.startsWith("brotli") ? (data, opts) => op(impl)(data, brotliLevel(opts)) : op(impl);
  zlib[name + "Sync"] = sync;
  zlib[name] = (data, opts, cb) => {
    if (typeof opts === "function") { cb = opts; opts = undefined; }
    let out, err = null;
    try { out = sync(data, opts); } catch (e) { err = e; }
    queueMicrotask(() => cb(err, out));
  };
}
// Stream factories: Transforms over the host's streaming codecs: output
// is emitted as input arrives, flush() emits what was written so far (a
// sync flush), and the end finishes the stream.
for (const [name, kind] of [["Gzip", "gzip"], ["Gunzip", "gunzip"], ["Deflate", "deflate"], ["Inflate", "inflate"], ["DeflateRaw", "deflateRaw"], ["InflateRaw", "inflateRaw"], ["Unzip", "unzip"], ["BrotliCompress", "brotliCompress"], ["BrotliDecompress", "brotliDecompress"]]) {
  zlib["create" + name] = (opts) => {
    if (!host) throw new Error(`og: zlib.create${name} is not available in this runtime`);
    const { Transform } = require("stream");
    const level = kind.startsWith("brotli") ? brotliLevel(opts).level : (opts && opts.level) ?? -1;
    const codec = host.stream(kind, level);
    const bytes = (ab) => Buffer.from(ab);
    const t = new Transform({
      transform(chunk, encoding, cb) {
        try {
          const out = codec.write(typeof chunk === "string" ? Buffer.from(chunk, encoding) : chunk);
          cb(null, out.byteLength ? bytes(out) : undefined);
        } catch (e) {
          cb(e);
        }
      },
      flush(cb) {
        try {
          const out = codec.finish();
          cb(null, out.byteLength ? bytes(out) : undefined);
        } catch (e) {
          cb(e);
        }
      },
    });
    t.flush = (kind, cb) => {
      if (typeof kind === "function") cb = kind;
      try {
        const out = codec.flush();
        if (out.byteLength) t.push(bytes(out));
      } catch (e) {
        t.destroy(e);
      }
      if (cb) queueMicrotask(cb);
    };
    t.params = (level, strategy, cb) => cb && queueMicrotask(cb);
    t.close = (cb) => {
      t.destroy();
      if (cb) queueMicrotask(cb);
    };
    t.bytesWritten = 0;
    return t;
  };
}

module.exports = zlib;
