// What vendored Node lib/ files (vendor/node/lib) take from Node's
// internals: primordials and the internal/* modules they require. Each
// such require resolves to this one module (nodeshim.go).

// primordials: XPrototypeY(self, ...args) is X.prototype.y called on self
// (XPrototypeGetY reads the accessor y; XPrototypeYApply takes the
// arguments as an array), XY is the static X.y, SafeX is X, and any other
// name is the global of that name.
const uncurry = (f) => (self, ...args) => Reflect.apply(f, self, args);
const TypedArray = Object.getPrototypeOf(Uint8Array);
const lowerFirst = (s) => s[0].toLowerCase() + s.slice(1);
const ctorOf = (name) => (name === "TypedArray" ? TypedArray : typeof globalThis[name] === "function" ? globalThis[name] : undefined);
// A namespace object's statics (MathMin, ReflectApply, JSONParse).
const staticsOf = (name) => ctorOf(name) ?? (typeof globalThis[name] === "object" && globalThis[name] !== null ? globalThis[name] : undefined);
const specialPrimordials = {
  // SafePromiseAll(promises, mapFn): Promise.all over the (mapped) array.
  SafePromiseAll: (promises, mapFn) => Promise.all(mapFn ? Array.prototype.map.call(promises, mapFn) : promises),
  SafePromiseAllReturnVoid: (promises, mapFn) => Promise.all(mapFn ? Array.prototype.map.call(promises, mapFn) : promises).then(() => {}),
  PromiseWithResolvers: () => {
    let resolve, reject;
    const promise = new Promise((res, rej) => {
      resolve = res;
      reject = rej;
    });
    return { promise, resolve, reject };
  },
};
function primordial(name) {
  if (Object.prototype.hasOwnProperty.call(specialPrimordials, name)) return specialPrimordials[name];
  if (name.startsWith("Safe")) return primordial(name.slice(4));
  // XPrototypeYApply(self, args): X.prototype.y applied to an array of args.
  if (name.endsWith("Apply") && name.includes("Prototype") && !(name in globalThis)) {
    const f = primordial(name.slice(0, -5));
    return (self, args) => f(self, ...args);
  }
  let m = /^([A-Z][A-Za-z0-9]*?)Prototype(Get|Set)?([A-Z][A-Za-z0-9]*)$/.exec(name);
  if (m && ctorOf(m[1])) {
    const proto = ctorOf(m[1]).prototype;
    const key = lowerFirst(m[3]);
    if (m[2]) {
      const d = Object.getOwnPropertyDescriptor(proto, key);
      const f = d && (m[2] === "Get" ? d.get : d.set);
      if (f) return uncurry(f);
    }
    const f = proto[lowerFirst(m[2] ? m[2] + m[3] : m[3])];
    if (typeof f === "function") return uncurry(f);
  }
  if (name in globalThis) return globalThis[name];
  for (let i = 1; i < name.length; i++) {
    if (name[i] < "A" || name[i] > "Z") continue;
    const ctor = staticsOf(name.slice(0, i));
    const f = ctor && ctor[lowerFirst(name.slice(i))];
    if (typeof f === "function") return f.bind(ctor);
    if (typeof f === "symbol") return f; // SymbolHasInstance, SymbolDispose, ...
  }
  throw new Error(`og: primordial ${name} is not shimmed`);
}
const primordials = new Proxy({}, {
  get(_, name) {
    return typeof name === "string" ? primordial(name) : undefined;
  },
});

// internal/constants: the character codes lib/path.js reads.
const constants = {
  CHAR_UPPERCASE_A: 65,
  CHAR_LOWERCASE_A: 97,
  CHAR_UPPERCASE_Z: 90,
  CHAR_LOWERCASE_Z: 122,
  CHAR_DOT: 46,
  CHAR_FORWARD_SLASH: 47,
  CHAR_BACKWARD_SLASH: 92,
  CHAR_COLON: 58,
  CHAR_QUESTION_MARK: 63,
};

// internal/errors' ERR_INVALID_ARG_TYPE message for one expected type, with
// its description of the value received (determineSpecificType).
function received(value) {
  if (value == null) return ` Received ${value}`;
  if (typeof value === "function") return ` Received function ${value.name}`;
  if (typeof value === "object") {
    if (value.constructor && value.constructor.name) return ` Received an instance of ${value.constructor.name}`;
    return " Received [Object: null prototype]";
  }
  let shown = inspect(value);
  if (shown.length > 28) shown = `${shown.slice(0, 25)}...`;
  return ` Received type ${typeof value} (${shown})`;
}
function inspect(value) {
  return typeof value === "string" ? `'${value}'` : typeof value === "bigint" ? `${value}n` : String(value);
}
function invalidArgType(name, type, value) {
  const e = new TypeError(`The "${name}" argument must be of type ${type}.${received(value)}`);
  e.code = "ERR_INVALID_ARG_TYPE";
  return e;
}

// internal/validators
function validateString(value, name) {
  if (typeof value !== "string") throw invalidArgType(name, "string", value);
}
function validateBoolean(value, name) {
  if (typeof value !== "boolean") throw invalidArgType(name, "boolean", value);
}
function validateFunction(value, name) {
  if (typeof value !== "function") throw invalidArgType(name, "function", value);
}
function validateObject(value, name) {
  if (value === null || Array.isArray(value) || typeof value !== "object") throw invalidArgType(name, "object", value);
}
function validateInteger(value, name, min = Number.MIN_SAFE_INTEGER, max = Number.MAX_SAFE_INTEGER) {
  if (typeof value !== "number") throw invalidArgType(name, "number", value);
  if (!Number.isInteger(value)) throw outOfRange(name, "an integer", value);
  if (value < min || value > max) throw outOfRange(name, `>= ${min} && <= ${max}`, value);
}
function validateInt32(value, name, min = -2147483648, max = 2147483647) {
  if (typeof value !== "number") throw invalidArgType(name, "number", value);
  if (!Number.isInteger(value)) throw outOfRange(name, "an integer", value);
  if (value < min || value > max) throw outOfRange(name, `>= ${min} && <= ${max}`, value);
}
function outOfRange(name, range, value) {
  const e = new RangeError(`The value of "${name}" is out of range. It must be ${range}. Received ${inspect(value)}`);
  e.code = "ERR_OUT_OF_RANGE";
  return e;
}
// validateThisInternalField: a method called on an object that is not
// its class's instance.
function validateThisInternalField(object, fieldKey, className) {
  if (typeof object !== "object" || object === null || !(fieldKey in object)) {
    const e = new TypeError(`Value of "this" must be of type ${className}`);
    e.code = "ERR_INVALID_THIS";
    throw e;
  }
}
function validateOneOf(value, name, oneOf) {
  if (!oneOf.includes(value)) {
    const allowed = oneOf.map((v) => (typeof v === "string" ? `'${v}'` : String(v))).join(", ");
    const kind = name.includes(".") ? "property" : "argument";
    const e = new TypeError(`The ${kind} '${name}' must be one of: ${allowed}. Received ${inspect(value)}`);
    e.code = "ERR_INVALID_ARG_VALUE";
    throw e;
  }
}

// internal/util
function getLazy(initializer) {
  let value;
  let initialized = false;
  return function () {
    if (!initialized) {
      value = initializer();
      initialized = true;
    }
    return value;
  };
}

const kEmptyObject = Object.freeze({ __proto__: null });

// deprecate(fn, msg, code): fn, warning once when first called (as
// util.deprecate; NODE_NO_DEPRECATION silences it).
function deprecate(fn, msg, code) {
  let warned = false;
  return function (...args) {
    if (!warned && !globalThis.process?.noDeprecation) {
      warned = true;
      globalThis.process?.emitWarning?.(msg, "DeprecationWarning", code);
    }
    return new.target ? Reflect.construct(fn, args, new.target) : Reflect.apply(fn, this, args);
  };
}

// normalizeEncoding: Node's canonical name of a Buffer encoding, or
// undefined.
function normalizeEncoding(enc) {
  if (enc == null || enc === "utf8" || enc === "utf-8") return "utf8";
  switch (String(enc).toLowerCase()) {
    case "utf8":
    case "utf-8":
      return "utf8";
    case "ucs2":
    case "ucs-2":
    case "utf16le":
    case "utf-16le":
      return "utf16le";
    case "latin1":
    case "binary":
      return "latin1";
    case "base64":
    case "base64url":
    case "hex":
    case "ascii":
      return String(enc).toLowerCase();
    case "":
      return "utf8";
  }
  return undefined;
}

// WeakReference (internal/util): a weak reference with a count of strong
// holds; og's WeakRef holds strongly anyway (a request's VM is short-lived).
class WeakReference {
  #target;
  #refs = 0;
  constructor(target) {
    this.#target = target;
  }
  get() {
    return this.#target;
  }
  incRef() {
    return ++this.#refs;
  }
  decRef() {
    return --this.#refs;
  }
}

// internal/fs/glob's matchGlobPattern (path.matchesGlob), as Node's: its
// bundled minimatch with Node's options. og reports Linux, so nocase is
// off (Node turns it on for macOS and Windows).
let minimatch;
function matchGlobPattern(path, pattern, windows = false) {
  validateString(path, "path");
  validateString(pattern, "pattern");
  minimatch ??= require("internal/deps/minimatch/index");
  const platform = globalThis.process?.platform;
  return minimatch.minimatch(path, pattern, {
    kEmptyObject: {},
    nocase: platform === "darwin" || platform === "win32",
    windowsPathsNoEscape: true,
    nonegate: true,
    nocomment: true,
    optimizationLevel: 2,
    platform: windows ? "win32" : "posix",
    nocaseMagicOnly: true,
  });
}

module.exports = {
  primordials,
  ...constants,
  validateString,
  validateBoolean,
  validateFunction,
  validateObject,
  validateOneOf,
  validateInteger,
  validateInt32,
  validateThisInternalField,
  deprecate,
  isWindows: false,
  getLazy,
  kEmptyObject,
  normalizeEncoding,
  matchGlobPattern,
  WeakReference,
};
