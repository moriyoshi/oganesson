// The stream-related internals vendored Node lib files require
// (lib/internal/webstreams/adapters.js): errors from readable-stream (the
// userland port of Node's), the WHATWG stream classes from the runtime's
// globals, and internalBinding. Each such require resolves to this one
// module (nodeshim.go); the cheaper ones live in node_internals.js.
const rsErrors = require("/readable-stream/lib/ours/errors.js");
const { types } = require("util");

class ERR_INVALID_STATE extends Error {
  constructor(message) {
    super(`Invalid state: ${message}`);
    this.code = "ERR_INVALID_STATE";
  }
}
ERR_INVALID_STATE.TypeError = class extends TypeError {
  constructor(message) {
    super(`Invalid state: ${message}`);
    this.code = "ERR_INVALID_STATE";
  }
};

// ERR_SYSTEM_ERROR(context): an error a system call reported.
class ERR_SYSTEM_ERROR extends Error {
  constructor(ctx = {}) {
    super(`A system error occurred: ${ctx.syscall} returned ${ctx.code} (${ctx.message})`);
    this.code = "ERR_SYSTEM_ERROR";
    this.info = ctx;
  }
}

// ErrnoException(err, syscall): a libuv error; only the StreamBase
// adapters raise it, and og has no StreamBase.
class ErrnoException extends Error {
  constructor(err, syscall) {
    super(`${syscall} errno ${err}`);
    this.errno = err;
    this.syscall = syscall;
  }
}

// internalBinding: the bindings adapters.js reads at load time. og has no
// StreamBase (stream_wrap); zlib's negative return codes are zlib.h's.
function internalBinding(name) {
  switch (name) {
    case "constants":
      return {
        zlib: { Z_ERRNO: -1, Z_STREAM_ERROR: -2, Z_DATA_ERROR: -3, Z_MEM_ERROR: -4, Z_BUF_ERROR: -5, Z_VERSION_ERROR: -6 },
      };
    case "uv":
      return { UV_EOF: -4095 };
    case "stream_wrap":
      return { WriteWrap: class WriteWrap {}, ShutdownWrap: class ShutdownWrap {}, kReadBytesOrError: 0, kLastWriteWasAsync: 1, streamBaseState: [] };
    case "errors":
      // diagnostics_channel reports a subscriber's error as uncaught (from
      // a nextTick): thrown from the task, the engine offers it to
      // process's listeners (globals.js), as Node does.
      return {
        triggerUncaughtException(err) {
          throw err;
        },
      };
    case "diagnostics_channel":
      return dcBinding;
  }
  throw new Error(`og: internalBinding('${name}') is not shimmed`);
}

// diagnostics_channel's binding: per-channel subscriber counts that
// native code reads (og has none), and the hook native channels use.
const dcIndexes = new Map();
const dcBinding = {
  subscribers: [],
  getOrCreateChannelIndex(name) {
    let i = dcIndexes.get(name);
    if (i === undefined) {
      i = dcBinding.subscribers.push(0) - 1;
      dcIndexes.set(name, i);
    }
    return i;
  },
  linkNativeChannel() {},
};

const eos = require("stream").finished;

module.exports = {
  // internal/errors
  AbortError: rsErrors.AbortError,
  ErrnoException,
  codes: { ...rsErrors.codes, ERR_INVALID_STATE, ERR_SYSTEM_ERROR },
  internalBinding,
  // internal/encoding
  TextEncoder: globalThis.TextEncoder,
  TextDecoder: globalThis.TextDecoder,
  // internal/webstreams/*
  ReadableStream: globalThis.ReadableStream,
  WritableStream: globalThis.WritableStream,
  isReadableStream: (v) => v instanceof globalThis.ReadableStream,
  isWritableStream: (v) => v instanceof globalThis.WritableStream,
  CountQueuingStrategy: globalThis.CountQueuingStrategy,
  ByteLengthQueuingStrategy: globalThis.ByteLengthQueuingStrategy,
  // internal/util/types
  ...types,
  // internal/streams/end-of-stream
  eos,
  finished: eos,
  // internal/fs/promises: FileHandle and the refcount symbols streams.js
  // calls on a FileHandle given as the fd option.
  kRef: Symbol("kRef"),
  kUnref: Symbol("kUnref"),
  get FileHandle() {
    return require("fs").promises.FileHandle;
  },
  // internal/fs/utils
  copyObject(source) {
    const target = {};
    for (const key in source) target[key] = source[key];
    return target;
  },
  getOptions(options, defaultOptions = Object.freeze({ __proto__: null })) {
    if (options == null || typeof options === "function") return defaultOptions;
    if (typeof options === "string") return { ...defaultOptions, encoding: options };
    if (typeof options !== "object") throw new rsErrors.codes.ERR_INVALID_ARG_TYPE("options", ["string", "Object"], options);
    if (options.encoding !== "buffer" && options.encoding && !Buffer.isEncoding(options.encoding)) {
      throw new rsErrors.codes.ERR_INVALID_ARG_VALUE("encoding", options.encoding, "is invalid encoding");
    }
    return options;
  },
  getValidatedFd(fd, propName = "fd") {
    if (Object.is(fd, -0)) return 0;
    require("internal/validators").validateInt32(fd, propName, 0);
    return fd;
  },
  validatePath(path, propName = "path") {
    if (typeof path !== "string" && !(path instanceof Uint8Array)) {
      throw new rsErrors.codes.ERR_INVALID_ARG_TYPE(propName, ["string", "Buffer", "URL"], path);
    }
    if (typeof path === "string" ? path.includes("\u0000") : path.includes(0)) {
      throw new rsErrors.codes.ERR_INVALID_ARG_VALUE(propName, path, "must be a string, Uint8Array, or URL without null bytes");
    }
  },
  // internal/url
  toPathIfFileURL(p) {
    return p != null && typeof p === "object" && p.href !== undefined && p.protocol !== undefined ? require("url").fileURLToPath(p) : p;
  },
};
