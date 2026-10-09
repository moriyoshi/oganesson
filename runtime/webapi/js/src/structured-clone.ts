import { DOMException } from "./events";
import { Blob, File } from "./blob";
import { defineGlobal } from "./util";

const typedArrays: any[] = [Int8Array, Uint8Array, Uint8ClampedArray, Int16Array, Uint16Array, Int32Array, Uint32Array, Float32Array, Float64Array, BigInt64Array, BigUint64Array];
const errorCtors: Record<string, ErrorConstructor> = { Error, EvalError, RangeError, ReferenceError, SyntaxError, TypeError, URIError };

// structuredClone implements the HTML structured serialize/deserialize pair in
// one pass. Transfer lists are accepted but buffers are copied (Goja cannot
// detach ArrayBuffers).
export function structuredClone(value: unknown, options?: { transfer?: unknown[] }) {
  if (arguments.length === 0) throw new TypeError("structuredClone requires 1 argument");
  void options;
  const memory = new Map<object, unknown>();
  const clone = (v: unknown): unknown => {
    if (typeof v === "symbol") throw new DOMException(`${String(v)} could not be cloned.`, "DataCloneError");
    if (typeof v === "function") throw new DOMException(`${(v as Function).name || "function"} could not be cloned.`, "DataCloneError");
    if (v === null || typeof v !== "object") return v;
    if (memory.has(v)) return memory.get(v);
    const o = v as any;
    let out: any;
    if (o instanceof Boolean) out = new Boolean(o.valueOf());
    else if (o instanceof Number) out = new Number(o.valueOf());
    else if (o instanceof String) out = new String(o.valueOf());
    else if (typeof BigInt !== "undefined" && Object.prototype.toString.call(o) === "[object BigInt]") out = Object(o.valueOf());
    else if (o instanceof Date) out = new Date(o.getTime());
    else if (o instanceof RegExp) out = new RegExp(o.source, o.flags);
    else if (o instanceof ArrayBuffer) out = o.slice(0);
    else if (o instanceof DataView) { const b = clone(o.buffer) as ArrayBuffer; out = new DataView(b, o.byteOffset, o.byteLength); }
    else if (ArrayBuffer.isView(o)) {
      const C = typedArrays.find((T) => o instanceof T);
      const b = clone(o.buffer) as ArrayBuffer;
      out = new C(b, o.byteOffset, (o as any).length);
    } else if (o instanceof Map) {
      out = new Map();
      memory.set(o, out);
      for (const [k, x] of o) out.set(clone(k), clone(x));
      return out;
    } else if (o instanceof Set) {
      out = new Set();
      memory.set(o, out);
      for (const x of o) out.add(clone(x));
      return out;
    } else if (o instanceof File) out = new File([o], o.name, { type: o.type, lastModified: o.lastModified });
    else if (o instanceof Blob) out = new Blob([o], { type: o.type });
    else if (o instanceof Error) {
      const name = errorCtors[o.name] ? o.name : "Error";
      out = new errorCtors[name](o.message);
      memory.set(o, out);
      if ("stack" in o) Object.defineProperty(out, "stack", { value: o.stack, writable: true, configurable: true });
      if ("cause" in o) out.cause = clone(o.cause);
      return out;
    } else if (Array.isArray(o)) {
      out = new Array(o.length);
      memory.set(o, out);
      for (const k of Object.keys(o)) out[k] = clone(o[k]);
      return out;
    } else {
      const proto = Object.getPrototypeOf(o);
      const tagName = Object.prototype.toString.call(o);
      if ((proto !== Object.prototype && proto !== null && tagName !== "[object Object]") || o instanceof Promise || o instanceof WeakMap || o instanceof WeakSet) {
        throw new DOMException(`${tagName} could not be cloned.`, "DataCloneError");
      }
      out = {};
      memory.set(o, out);
      for (const k of Object.keys(o)) out[k] = clone(o[k]);
      return out;
    }
    memory.set(o, out);
    return out;
  };
  return clone(value);
}

export function installStructuredClone() {
  defineGlobal("structuredClone", structuredClone);
}
