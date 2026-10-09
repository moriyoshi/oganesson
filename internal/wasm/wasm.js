// The WebAssembly JS API over Oganesson's wazero-backed primitives (wasm.go).
(function (native, HANDLE) {
  "use strict";

  class CompileError extends Error {}
  class LinkError extends Error {}
  class RuntimeError extends Error {}
  for (const E of [CompileError, LinkError, RuntimeError]) {
    Object.defineProperty(E.prototype, "name", { value: E.name, writable: true, configurable: true });
  }
  native.setErrors(CompileError, LinkError, RuntimeError);

  const INTERNAL = Symbol("internal");

  function bufferSource(src) {
    if (src instanceof ArrayBuffer) return src;
    if (ArrayBuffer.isView(src)) {
      return src.buffer.slice(src.byteOffset, src.byteOffset + src.byteLength);
    }
    throw new TypeError("WebAssembly: argument must be a buffer source");
  }

  class Module {
    constructor(bytes) {
      this[HANDLE] = native.compile(bufferSource(bytes));
    }
    static imports(module) {
      return native.moduleImports(handleOf(module, Module));
    }
    static exports(module) {
      return native.moduleExports(handleOf(module, Module));
    }
    static customSections() {
      return [];
    }
  }

  class Memory {
    constructor(descriptor, handle) {
      if (descriptor === INTERNAL) {
        this[HANDLE] = handle;
        return;
      }
      if (typeof descriptor !== "object" || descriptor === null) throw new TypeError("WebAssembly.Memory: descriptor must be an object");
      this[HANDLE] = native.newMemory(descriptor.initial ?? descriptor.minimum ?? 0, descriptor.maximum, !!descriptor.shared, this);
    }
    get buffer() {
      return native.memoryBuffer(this[HANDLE]);
    }
    grow(delta) {
      return native.memoryGrow(this[HANDLE], delta);
    }
  }

  class Table {
    constructor(descriptor, handle) {
      if (descriptor !== INTERNAL) throw new TypeError("WebAssembly.Table: only exported tables are supported");
      this[HANDLE] = handle;
    }
    get(index) {
      return native.tableGet(this[HANDLE], index);
    }
    // set / grow: externref tables (wasm-bindgen's); grow also funcref.
    set(index, value) {
      native.tableSet(this[HANDLE], index, value);
    }
    grow(delta, init) {
      return native.tableGrow(this[HANDLE], delta, init);
    }
    get length() {
      return native.tableLength(this[HANDLE]);
    }
  }

  // WebAssembly.Global: exported globals (value / valueOf).
  class Global {
    constructor(descriptor, handle) {
      if (descriptor !== INTERNAL) throw new TypeError("WebAssembly.Global: only exported globals are supported");
      this[HANDLE] = handle;
    }
    get value() {
      return native.globalGet(this[HANDLE]);
    }
    set value(v) {
      native.globalSet(this[HANDLE], v);
    }
    valueOf() {
      return native.globalGet(this[HANDLE]);
    }
  }

  class Instance {
    constructor(module, importObject) {
      const exports = native.instantiate(
        handleOf(module, Module),
        importObject ?? {},
        (h) => new Memory(INTERNAL, h),
        (h) => new Table(INTERNAL, h),
        (h) => new Global(INTERNAL, h),
      );
      Object.setPrototypeOf(exports, null);
      this.exports = Object.freeze(exports);
    }
  }

  function handleOf(v, cls) {
    if (!(v instanceof cls)) throw new TypeError("WebAssembly: expected a " + cls.name);
    return v[HANDLE];
  }

  const WebAssembly = {
    Module,
    Instance,
    Memory,
    Table,
    Global,
    CompileError,
    LinkError,
    RuntimeError,
    validate(bytes) {
      try {
        new Module(bytes);
        return true;
      } catch {
        return false;
      }
    },
    async compile(bytes) {
      return new Module(bytes);
    },
    async instantiate(source, importObject) {
      if (source instanceof Module) return new Instance(source, importObject);
      const module = new Module(source);
      return { module, instance: new Instance(module, importObject) };
    },
  };
  Object.defineProperty(WebAssembly, Symbol.toStringTag, { value: "WebAssembly" });
  Object.defineProperty(globalThis, "WebAssembly", { value: WebAssembly, writable: true, configurable: true });

  // The Oganesson extensions are absent in restricted mode (production).
  if (native.wasi) {
    Object.defineProperty(globalThis, Symbol.for("og.wasm"), {
      value: Object.freeze({
        wasi: () => native.wasi(),
        native: (set) => native.native(set),
      }),
    });
  }
});
