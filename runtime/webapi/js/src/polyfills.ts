// Built-ins Goja lacks (see the M0 entry in .agents/docs/JOURNAL.md). Requests run in a fresh VM,
// so WeakRef may hold strongly and FinalizationRegistry may never fire.
import { defineGlobal } from "./util";
import { host } from "./host";

function defineMethod(target: any, name: string, fn: Function) {
  if (typeof target[name] === "function") return;
  Object.defineProperty(target, name, { value: fn, writable: true, configurable: true, enumerable: false });
}

// CallSite is V8's: its methods live on the prototype, where tools find
// them (source-map-support's cloneCallSite copies the prototype's own
// methods; GraphQL Mesh's CLI loads it).
class CallSite {
  #fn: string | null;
  #loc: string;
  #file: string | null;
  #line: number | null;
  #col: number | null;
  #native: boolean;
  #dot: number;
  constructor(fn: string | null, loc: string) {
    this.#fn = fn;
    this.#loc = loc;
    this.#native = loc === "native";
    const lm = /^(.*):(\d+):(\d+)$/.exec(loc);
    this.#file = this.#native ? null : lm ? lm[1] : loc || null;
    this.#line = lm ? Number(lm[2]) : null;
    this.#col = lm ? Number(lm[3]) : null;
    this.#dot = fn ? fn.lastIndexOf(".") : -1;
  }
  getThis() { return undefined; }
  getTypeName() { return this.#dot > 0 ? this.#fn!.slice(0, this.#dot) : null; }
  getFunction() { return undefined; }
  getFunctionName() { return this.#fn; }
  getMethodName() { return this.#dot > 0 ? this.#fn!.slice(this.#dot + 1) : null; }
  getFileName() { return this.#file ?? undefined; }
  getLineNumber() { return this.#line; }
  getColumnNumber() { return this.#col; }
  getEvalOrigin() { return undefined; }
  getScriptNameOrSourceURL() { return this.#file ?? undefined; }
  getScriptHash() { return ""; }
  getEnclosingLineNumber() { return this.#line; }
  getEnclosingColumnNumber() { return this.#col; }
  getPosition() { return 0; }
  getPromiseIndex() { return null; }
  isToplevel() { return this.#fn === null; }
  isEval() { return false; }
  isNative() { return this.#native; }
  isConstructor() { return false; }
  isAsync() { return false; }
  isPromiseAll() { return false; }
  toString() {
    const where = this.#native ? "native" : this.#line !== null ? `${this.#file}:${this.#line}:${this.#col}` : this.#loc;
    return this.#fn ? `${this.#fn} (${where})` : where;
  }
}

// callSite parses a Goja stack frame ("    at fn (file:12:3(45))",
// "    at file:12:3(45)", "    at call (native)") into a V8 CallSite.
function callSite(line: string): object | null {
  const m = /^\s*at (?:(.+?) \((.*)\)|(.*))$/.exec(line);
  if (!m) return null;
  return new CallSite(m[1] ?? null, (m[2] ?? m[3] ?? "").replace(/\(\d+\)$/, ""));
}

export function installPolyfills() {
  if (typeof (globalThis as any).WeakRef === "undefined") {
    class WeakRef<T extends object> {
      #target: T;
      constructor(target: T) {
        if (Object(target) !== target) throw new TypeError("WeakRef: target must be an object");
        this.#target = target;
      }
      deref(): T | undefined { return this.#target; }
    }
    Object.defineProperty(WeakRef.prototype, Symbol.toStringTag, { value: "WeakRef", configurable: true });
    defineGlobal("WeakRef", WeakRef);
  }
  if (typeof (globalThis as any).FinalizationRegistry === "undefined") {
    class FinalizationRegistry {
      constructor(cb: (held: unknown) => void) {
        if (typeof cb !== "function") throw new TypeError("FinalizationRegistry: cleanup must be callable");
      }
      register(target: object, _held?: unknown, _token?: object) {
        if (Object(target) !== target) throw new TypeError("FinalizationRegistry.register: target must be an object");
      }
      unregister(_token: object) { return false; }
    }
    Object.defineProperty(FinalizationRegistry.prototype, Symbol.toStringTag, { value: "FinalizationRegistry", configurable: true });
    defineGlobal("FinalizationRegistry", FinalizationRegistry);
  }

  // V8 stack API used by many libraries (and React's dev builds). With
  // Error.prepareStackTrace set, stack is what it returns for V8 CallSite
  // objects (depd, callsites, source-map-support), parsed from Goja's frames.
  const E = Error as any;
  defineMethod(E, "captureStackTrace", function (obj: any) {
    const stack = new E().stack || ""; // the engine's own format
    // Drop the "Error" header line and this frame.
    const lines = stack.split("\n").slice(2);
    const head = obj && obj.name !== undefined ? `${obj.name}${obj.message ? ": " + obj.message : ""}` : "Error";
    let value: unknown = [head, ...lines].join("\n");
    const G = (globalThis as any).Error; // where code sets the statics
    if (typeof G.prepareStackTrace === "function") {
      const limit = typeof G.stackTraceLimit === "number" ? G.stackTraceLimit : 10;
      const sites = lines.map(callSite).filter((s): s is object => s !== null).slice(0, Math.max(0, limit));
      value = G.prepareStackTrace(obj, sites);
    }
    Object.defineProperty(obj, "stack", { value, writable: true, configurable: true, enumerable: false });
  });
  if (E.stackTraceLimit === undefined) E.stackTraceLimit = 10;

  // In V8, Error.prepareStackTrace also shapes the stack of every error
  // created afterwards: `Error.prepareStackTrace = (_, s) => s; new
  // Error().stack` is an array of CallSites (callsites and parent-module,
  // through cosmiconfig's import-fresh: Lingui's and Compiled's Babel
  // configs). Goja formats an error's stack itself, so the Error
  // constructor is wrapped: with prepareStackTrace set, a new error's stack
  // is what it returns. instanceof, subclasses and Error's statics are the
  // original's; errors the engine throws keep Goja's stack.
  const OrigError = E;
  let preparing = false;
  const prepared = (err: any) => {
    const prep = (globalThis as any).Error.prepareStackTrace;
    if (preparing || typeof prep !== "function" || err === null || typeof err !== "object") return err;
    preparing = true;
    try {
      const lines = String(err.stack ?? "").split("\n").slice(1);
      // The wrapper's own frames come first.
      while (lines.length > 0 && /og:prelude|\(native\)$/.test(lines[0])) lines.shift();
      const limit = typeof (globalThis as any).Error.stackTraceLimit === "number" ? (globalThis as any).Error.stackTraceLimit : 10;
      const sites = lines.map(callSite).filter((s): s is object => s !== null).slice(0, Math.max(0, limit));
      // A formatter that fails on Goja's CallSites (asking for more than
      // they have) leaves the engine's stack, as creating the error must
      // not throw.
      let value: unknown;
      try {
        value = prep(err, sites);
      } catch {
        return err;
      }
      Object.defineProperty(err, "stack", { value, writable: true, configurable: true, enumerable: false });
    } finally {
      preparing = false;
    }
    return err;
  };
  // A plain function (Goja's instanceof does not take a Proxy of one): it
  // constructs through the original with new.target (subclasses, calls
  // without new), shares its prototype object (instanceof holds for every
  // error, the engine's included) and carries its static members.
  const ErrorWrapper = function Error(this: any, ...args: any[]) {
    return prepared(Reflect.construct(OrigError, args, new.target || ErrorWrapper));
  } as any;
  for (const key of Reflect.ownKeys(OrigError)) {
    if (key === "prototype" || key === "length" || key === "name") continue;
    Object.defineProperty(ErrorWrapper, key, Object.getOwnPropertyDescriptor(OrigError, key)!);
  }
  Object.defineProperty(ErrorWrapper, "prototype", { value: OrigError.prototype, writable: false, configurable: false, enumerable: false });
  Object.defineProperty(ErrorWrapper, "name", { value: "Error", configurable: true });
  // og's own stack reads use the engine's Error (a stack string).
  Object.defineProperty(globalThis, Symbol.for("og.nativeError"), { value: OrigError });
  Object.setPrototypeOf(ErrorWrapper, Object.getPrototypeOf(OrigError));
  Object.defineProperty(OrigError.prototype, "constructor", { value: ErrorWrapper, writable: true, configurable: true, enumerable: false });
  Object.defineProperty(globalThis, "Error", { value: ErrorWrapper, writable: true, configurable: true, enumerable: false });

  defineMethod(Promise, "withResolvers", function <T>(this: PromiseConstructor) {
    let resolve!: (v: T) => void, reject!: (e: unknown) => void;
    const promise = new this<T>((res, rej) => { resolve = res; reject = rej; });
    return { promise, resolve, reject };
  });
  defineMethod(Object, "groupBy", function (items: Iterable<any>, fn: (v: any, i: number) => PropertyKey) {
    const out = Object.create(null);
    let i = 0;
    for (const v of items) {
      const k = fn(v, i++);
      (out[k] ??= []).push(v);
    }
    return out;
  });
  defineMethod(Map, "groupBy", function (items: Iterable<any>, fn: (v: any, i: number) => unknown) {
    const out = new Map();
    let i = 0;
    for (const v of items) {
      const k = fn(v, i++);
      if (!out.has(k)) out.set(k, []);
      out.get(k).push(v);
    }
    return out;
  });
  defineMethod(Array, "fromAsync", async function (this: any, items: any, mapFn?: (v: any, i: number) => any, thisArg?: any) {
    const out: any[] = [];
    let i = 0;
    if (items[Symbol.asyncIterator]) {
      for await (const v of items) out.push(mapFn ? await mapFn.call(thisArg, v, i++) : v);
    } else {
      for (const v of Array.from(items as ArrayLike<any>)) {
        const awaited = await v;
        out.push(mapFn ? await mapFn.call(thisArg, awaited, i++) : awaited);
      }
    }
    return out;
  });
  defineMethod(String.prototype, "isWellFormed", function (this: string) {
    return !/[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?:^|[^\uD800-\uDBFF])[\uDC00-\uDFFF]/.test(String(this));
  });
  defineMethod(String.prototype, "toWellFormed", function (this: string) {
    const s = String(this);
    let out = "";
    for (let i = 0; i < s.length; i++) {
      const c = s.charCodeAt(i);
      if (c >= 0xd800 && c <= 0xdbff) {
        const d = s.charCodeAt(i + 1);
        if (d >= 0xdc00 && d <= 0xdfff) { out += s[i] + s[i + 1]; i++; continue; }
        out += "�";
      } else if (c >= 0xdc00 && c <= 0xdfff) {
        out += "�";
      } else {
        out += s[i];
      }
    }
    return out;
  });
  // ES2024 ArrayBuffer transfer, backed by Goja's real detach.
  const AB = ArrayBuffer.prototype as any;
  if (!AB.transfer) {
    const transfer = function (this: ArrayBuffer, newLength?: number) {
      if (!(this instanceof ArrayBuffer)) throw new TypeError("ArrayBuffer.prototype.transfer called on incompatible receiver");
      if (host.abDetached(this)) throw new TypeError("Cannot transfer a detached ArrayBuffer");
      const len = newLength === undefined ? this.byteLength : Number(newLength);
      const out = new ArrayBuffer(len);
      new Uint8Array(out).set(new Uint8Array(this, 0, Math.min(len, this.byteLength)));
      host.abDetach(this);
      return out;
    };
    defineMethod(AB, "transfer", transfer);
    defineMethod(AB, "transferToFixedLength", transfer);
    Object.defineProperty(AB, "detached", { get(this: ArrayBuffer) { return host.abDetached(this); }, configurable: true });
  }
  // %TypedArray%.from reads its source's @@iterator with GetMethod, which
  // boxes a primitive (Uint8Array.from("ab", f) iterates the string; Shiki
  // decodes its inlined WebAssembly so). The engine requires an object:
  // passing Object(source) is the same lookup.
  const TypedArray = Object.getPrototypeOf(Uint8Array) as any;
  const nativeFrom = TypedArray.from;
  try {
    Uint8Array.from("a" as any);
  } catch {
    const from = {
      from(this: any, source: unknown, ...rest: unknown[]) {
        return Reflect.apply(nativeFrom, this, [source !== null && source !== undefined && typeof source !== "object" && typeof source !== "function" ? Object(source) : source, ...rest]);
      },
    }.from;
    Object.defineProperty(TypedArray, "from", { value: from, writable: true, configurable: true, enumerable: false });
  }
  // Annex B: toGMTString is the same function object as toUTCString.
  if (!(Date.prototype as any).toGMTString) {
    Object.defineProperty(Date.prototype, "toGMTString", { value: Date.prototype.toUTCString, writable: true, configurable: true });
  }
  const S = Symbol as any;
  if (!S.dispose) Object.defineProperty(Symbol, "dispose", { value: Symbol("Symbol.dispose") });
  if (!S.asyncDispose) Object.defineProperty(Symbol, "asyncDispose", { value: Symbol("Symbol.asyncDispose") });
  installProxyHasInstance();
}

// The engine's instanceof rejects a callable Proxy on its right ("Expecting
// a function in instanceof check"): its proxy objects lack the ordinary
// [[HasInstance]] functions have. msw replaces globalThis.Headers, Request
// and Response with Proxies of themselves, after which every `x instanceof
// Headers` threw. instanceof first reads C[Symbol.hasInstance] (through the
// Proxy's get trap) and calls it with the Proxy as this, so Proxies of
// functions get a get trap that answers the default Symbol.hasInstance with
// an ordinary prototype-chain check instead. The handler's own traps (get
// included) still apply: the wrapper inherits from it.
function installProxyHasInstance() {
  const NativeProxy = Proxy;
  const defaultHasInstance = (Function.prototype as any)[Symbol.hasInstance];
  const ordinaryHasInstance = function (this: any, o: unknown): boolean {
    if (typeof this !== "function") return false;
    if (o === null || (typeof o !== "object" && typeof o !== "function")) return false;
    const proto = this.prototype;
    if (proto === null || (typeof proto !== "object" && typeof proto !== "function")) {
      throw new TypeError("Function has non-object prototype '" + String(proto) + "' in instanceof check");
    }
    for (let p = Object.getPrototypeOf(o); p !== null; p = Object.getPrototypeOf(p)) {
      if (p === proto) return true;
    }
    return false;
  };
  const wrapHandler = (target: any, handler: any) => {
    if (typeof target !== "function" || handler === null || typeof handler !== "object") return handler;
    const wrapped = Object.create(handler);
    Object.defineProperty(wrapped, "get", {
      value(t: any, k: PropertyKey, r: any) {
        const g = handler.get;
        const v = typeof g === "function" ? g.call(handler, t, k, r) : Reflect.get(t, k, r);
        return k === Symbol.hasInstance && v === defaultHasInstance ? ordinaryHasInstance : v;
      },
    });
    return wrapped;
  };
  // A bound function: constructible, without a prototype property (as
  // Proxy has none).
  const impl = function (this: any, target: any, handler: any) {
    if (!new.target) throw new TypeError("Constructor Proxy requires 'new'");
    return new NativeProxy(target, wrapHandler(target, handler));
  };
  const OgProxy: any = impl.bind(null);
  Object.defineProperty(OgProxy, "name", { value: "Proxy", configurable: true });
  Object.defineProperty(OgProxy, "length", { value: 2, configurable: true });
  Object.defineProperty(OgProxy, "revocable", {
    value: function revocable(target: any, handler: any) {
      return NativeProxy.revocable(target, wrapHandler(target, handler));
    },
    writable: true,
    configurable: true,
  });
  Object.defineProperty(globalThis, "Proxy", { value: OgProxy, writable: true, configurable: true, enumerable: false });
}
