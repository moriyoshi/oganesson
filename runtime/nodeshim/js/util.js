// node:util: unenv's module plus styleText (Node 20.12+/21.7+),
// stripVTControlCharacters and parseEnv (Node 21.7+), which unenv leaves unimplemented and tools such as
// Vite (its logger, loadEnv) call.
"use strict";
const base = require("unenv:node/util");

// util.inspect.colors: [open, close] SGR codes.
const styles = {
  reset: [0, 0], bold: [1, 22], dim: [2, 22], italic: [3, 23], underline: [4, 24], blink: [5, 25],
  inverse: [7, 27], hidden: [8, 28], strikethrough: [9, 29], doubleunderline: [21, 24],
  black: [30, 39], red: [31, 39], green: [32, 39], yellow: [33, 39], blue: [34, 39], magenta: [35, 39], cyan: [36, 39], white: [37, 39],
  bgBlack: [40, 49], bgRed: [41, 49], bgGreen: [42, 49], bgYellow: [43, 49], bgBlue: [44, 49], bgMagenta: [45, 49], bgCyan: [46, 49], bgWhite: [47, 49],
  framed: [51, 54], overlined: [53, 55],
  gray: [90, 39], redBright: [91, 39], greenBright: [92, 39], yellowBright: [93, 39], blueBright: [94, 39], magentaBright: [95, 39], cyanBright: [96, 39], whiteBright: [97, 39],
  bgGray: [100, 49], bgRedBright: [101, 49], bgGreenBright: [102, 49], bgYellowBright: [103, 49], bgBlueBright: [104, 49], bgMagentaBright: [105, 49], bgCyanBright: [106, 49], bgWhiteBright: [107, 49],
};
styles.grey = styles.gray;
styles.blackBright = styles.gray;
styles.bgGrey = styles.bgGray;
styles.bgBlackBright = styles.bgGray;
styles.faint = styles.dim;
styles.crossedout = styles.strikethrough;
styles.strikeThrough = styles.strikethrough;
styles.crossedOut = styles.strikethrough;
styles.conceal = styles.hidden;
styles.swapColors = styles.inverse;
styles.swapcolors = styles.inverse;
styles.doubleUnderline = styles.doubleunderline;

// shouldColorize (lib/internal/util/colors): FORCE_COLOR, NO_COLOR, a TTY.
function shouldColorize(stream) {
  const env = (globalThis.process && globalThis.process.env) || {};
  if (env.FORCE_COLOR !== undefined) return env.FORCE_COLOR !== "0" && env.FORCE_COLOR !== "false";
  return !!(stream && stream.isTTY) && env.NO_COLOR === undefined && env.NODE_DISABLE_COLORS === undefined && env.TERM !== "dumb";
}

function styleText(format, text, { validateStream = true, stream = globalThis.process && globalThis.process.stdout } = {}) {
  if (typeof text !== "string") {
    const e = new TypeError(`The "text" argument must be of type string. Received ${typeof text}`);
    e.code = "ERR_INVALID_ARG_TYPE";
    throw e;
  }
  const formats = Array.isArray(format) ? format : [format];
  for (const f of formats) {
    if (f !== "none" && !Object.prototype.hasOwnProperty.call(styles, f)) {
      const e = new TypeError(`The argument 'format' must be one of: ${Object.keys(styles).join(", ")}. Received ${JSON.stringify(f)}`);
      e.code = "ERR_INVALID_ARG_VALUE";
      throw e;
    }
  }
  if (validateStream && !shouldColorize(stream)) return text;
  let open = "", close = "";
  for (const f of formats) {
    if (f === "none") continue;
    const [a, b] = styles[f];
    open += `\u001b[${a}m`;
    close = `\u001b[${b}m` + close;
  }
  return open + text + close;
}

// parseEnv (Node 21.7+): dotenv content to an object. Node's parser
// follows dotenv's grammar; this is dotenv's parse (lib/main.js, v16).
// dotenv: Copyright (c) 2015, Scott Motte. BSD-2-Clause license:
// redistribution with or without modification is permitted provided the
// copyright notice, this list of conditions and the disclaimer are retained.
const LINE = /(?:^|^)\s*(?:export\s+)?([\w.-]+)(?:\s*=\s*?|:\s+?)(\s*'(?:\\'|[^'])*'|\s*"(?:\\"|[^"])*"|\s*`(?:\\`|[^`])*`|[^#\r\n]+)?\s*(?:#.*)?(?:$|$)/gm;
function parseEnv(content) {
  if (typeof content !== "string") {
    const e = new TypeError(`The "content" argument must be of type string. Received ${typeof content}`);
    e.code = "ERR_INVALID_ARG_TYPE";
    throw e;
  }
  const obj = {};
  const lines = content.replace(/\r\n?/gm, "\n");
  let match;
  LINE.lastIndex = 0;
  while ((match = LINE.exec(lines)) != null) {
    const key = match[1];
    let value = (match[2] || "").trim();
    const maybeQuote = value[0];
    value = value.replace(/^(['"`])([\s\S]*)\1$/gm, "$2");
    if (maybeQuote === '"') value = value.replace(/\\n/g, "\n").replace(/\\r/g, "\r");
    obj[key] = value;
  }
  return obj;
}

// stripVTControlCharacters (Node 16.11+): Node's own ANSI escape pattern
// (lib/internal/util/inspect.js), which unenv leaves unimplemented.
const ansi = new RegExp(
  "[\\u001B\\u009B][[\\]()#;?]*" +
    "(?:(?:(?:(?:;[-a-zA-Z\\d\\/\\#&.:=?%@~_]+)*" +
    "|[a-zA-Z\\d]+(?:;[-a-zA-Z\\d\\/\\#&.:=?%@~_]*)*)?" +
    "(?:\\u0007|\\u001B\\u005C|\\u009C))" +
    "|(?:(?:\\d{1,4}(?:;\\d{0,4})*)?" +
    "[\\dA-PR-TZcf-nq-uy=><~]))",
  "g",
);
function stripVTControlCharacters(str) {
  if (typeof str !== "string") throw new TypeError(`The "str" argument must be of type string. Received ${typeof str}`);
  return str.replace(ansi, "");
}

// util.types predicates unenv leaves unimplemented, as Node defines them:
// by internal slot (here: toStringTag, TypedArray's brand, a valueOf that
// only works on its own boxed type). isProxy and isKeyObject cannot be told
// from JavaScript; they answer false.
const tag = (v) => Object.prototype.toString.call(v);
const typedArrayTag = Object.getOwnPropertyDescriptor(Object.getPrototypeOf(Uint8Array.prototype), Symbol.toStringTag).get;
const typedArrayKind = (v) => typedArrayTag.call(v);
const boxed = (valueOf) => (v) => {
  if (v === null || typeof v !== "object") return false;
  try {
    valueOf.call(v);
    return true;
  } catch {
    return false;
  }
};
const isNumberObject = boxed(Number.prototype.valueOf);
const isStringObject = boxed(String.prototype.valueOf);
const isBooleanObject = boxed(Boolean.prototype.valueOf);
const isBigIntObject = boxed(BigInt.prototype.valueOf);
const isSymbolObject = boxed(Symbol.prototype.valueOf);
const typed = (name) => (v) => typedArrayKind(v) === name;
const types = Object.assign({}, base.types, {
  isArgumentsObject: (v) => tag(v) === "[object Arguments]",
  isNativeError: (v) => v instanceof Error || tag(v) === "[object Error]",
  isAsyncFunction: (v) => typeof v === "function" && /^\[object Async(Generator)?Function\]$/.test(tag(v)),
  isGeneratorFunction: (v) => typeof v === "function" && /^\[object (Async)?GeneratorFunction\]$/.test(tag(v)),
  isGeneratorObject: (v) => tag(v) === "[object Generator]" || tag(v) === "[object AsyncGenerator]",
  isMapIterator: (v) => tag(v) === "[object Map Iterator]",
  isSetIterator: (v) => tag(v) === "[object Set Iterator]",
  isProxy: () => false,
  isModuleNamespaceObject: (v) => v !== null && typeof v === "object" && v[Symbol.toStringTag] === "Module" && Object.getPrototypeOf(v) === null,
  isAnyArrayBuffer: (v) => v instanceof ArrayBuffer || (typeof SharedArrayBuffer !== "undefined" && v instanceof SharedArrayBuffer),
  isNumberObject, isStringObject, isBooleanObject, isBigIntObject, isSymbolObject,
  isBoxedPrimitive: (v) => isNumberObject(v) || isStringObject(v) || isBooleanObject(v) || isBigIntObject(v) || isSymbolObject(v),
  isArrayBufferView: (v) => ArrayBuffer.isView(v),
  isTypedArray: (v) => typedArrayKind(v) !== undefined,
  isUint8Array: typed("Uint8Array"),
  isUint8ClampedArray: typed("Uint8ClampedArray"),
  isUint16Array: typed("Uint16Array"),
  isUint32Array: typed("Uint32Array"),
  isInt8Array: typed("Int8Array"),
  isInt16Array: typed("Int16Array"),
  isInt32Array: typed("Int32Array"),
  isFloat32Array: typed("Float32Array"),
  isFloat64Array: typed("Float64Array"),
  isBigInt64Array: typed("BigInt64Array"),
  isBigUint64Array: typed("BigUint64Array"),
  isKeyObject: () => false,
  isCryptoKey: (v) => typeof CryptoKey !== "undefined" && v instanceof CryptoKey,
});

// callbackify, _extend, toUSVString and aborted, which unenv leaves
// unimplemented, as Node's lib/util.js has them (@svgr/webpack's loader
// callbackifies its transform).
function invalidArgType(name, type, value) {
  const e = new TypeError(`The "${name}" argument must be of type ${type}. Received ${value === null ? "null" : typeof value}`);
  e.code = "ERR_INVALID_ARG_TYPE";
  return e;
}

function callbackifyOnRejected(reason, cb) {
  // A falsy rejection reason becomes an Error carrying it, as in Node.
  if (!reason) {
    const e = new Error("Promise was rejected with a falsy value");
    e.code = "ERR_FALSY_VALUE_REJECTION";
    e.reason = reason;
    reason = e;
  }
  return cb(reason);
}

function callbackify(original) {
  if (typeof original !== "function") throw invalidArgType("original", "function", original);
  function callbackified(...args) {
    const maybeCb = args.pop();
    if (typeof maybeCb !== "function") throw invalidArgType("last argument", "function", maybeCb);
    const cb = maybeCb.bind(this);
    Reflect.apply(original, this, args).then(
      (ret) => globalThis.process.nextTick(cb, null, ret),
      (rej) => globalThis.process.nextTick(callbackifyOnRejected, rej, cb),
    );
  }
  const descriptors = Object.getOwnPropertyDescriptors(original);
  if (typeof descriptors.length.value === "number") descriptors.length.value++;
  if (typeof descriptors.name.value === "string") descriptors.name.value += "Callbackified";
  const proto = Object.getPrototypeOf(original);
  if (proto !== null && proto !== undefined) Object.setPrototypeOf(callbackified, proto);
  Object.defineProperties(callbackified, descriptors);
  return callbackified;
}

function _extend(target, source) {
  if (source === null || typeof source !== "object") return target;
  const keys = Object.keys(source);
  let i = keys.length;
  while (i--) target[keys[i]] = source[keys[i]];
  return target;
}

function toUSVString(input) {
  return String(input).toWellFormed ? String(input).toWellFormed() : String(input).replace(/[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/g, "\uFFFD");
}

function aborted(signal, resource) {
  if (signal === undefined || signal === null || typeof signal !== "object" || !("aborted" in signal)) throw invalidArgType("signal", "AbortSignal", signal);
  if (resource === null || (typeof resource !== "object" && typeof resource !== "function")) throw invalidArgType("resource", "object", resource);
  if (signal.aborted) return Promise.resolve();
  return new Promise((resolve) => signal.addEventListener("abort", () => resolve(), { once: true }));
}

module.exports = Object.assign({}, base, { styleText, parseEnv, stripVTControlCharacters, types, callbackify, _extend, toUSVString, aborted });
