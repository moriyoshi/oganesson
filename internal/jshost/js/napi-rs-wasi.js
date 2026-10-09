// Loads a napi-rs wasm32-wasi addon (e.g. @tailwindcss/oxide-wasm32-wasi)
// in place of the package's generated *.wasi.cjs, which needs Node's WASI
// and worker threads through emnapi. Here Node-API comes from napi-wasm,
// WebAssembly from Oganesson (wazero), WASI and wasi-threads natively from Go.
import { readFileSync } from "node:fs";
import { Environment, napi } from "og:napi-wasm";

const ogWasm = globalThis[Symbol.for("og.wasm")];

// The shared memory Oganesson gives threaded addons: napi-rs asks for up to
// 4 GiB; 1 GiB is plenty for build tooling.
const MAX_PAGES = 16384;

export function load(wasmPath) {
  const module = new WebAssembly.Module(readFileSync(wasmPath));
  const env = { ...napi, ...ogWasm.native("emnapi") };
  for (const im of WebAssembly.Module.imports(module)) {
    if (im.kind === "memory") {
      env[im.name] = new WebAssembly.Memory({
        initial: im.minimum,
        maximum: Math.max(im.minimum, Math.min(im.maximum ?? MAX_PAGES, MAX_PAGES)),
        shared: im.shared,
      });
    }
  }
  const instance = new WebAssembly.Instance(module, {
    env,
    wasi_snapshot_preview1: ogWasm.wasi(),
    wasi: ogWasm.native("wasi-threads"),
    // Whether @emnapi/node-binding (async hooks for Node) is loaded: no.
    emnapi: { emnapi_is_node_binding_available: () => 0 },
  });
  const ex = instance.exports;
  ex._initialize?.();
  // napi-rs registers classes and functions through these (ctor-like)
  // exports before module registration, as its loader's beforeInit does.
  for (const name of Object.keys(ex)) {
    if (name.startsWith("__napi_register__")) ex[name]();
  }
  // napi-wasm registers through napi_register_module_v1 and then
  // napi_register_wasm_v1; napi-rs exports both for one registration, which
  // must run once (emnapi calls only the latter).
  const facade = {
    exports: {
      memory: ex.memory,
      __indirect_function_table: ex.__indirect_function_table,
      napi_wasm_malloc: ex.malloc,
      napi_wasm_free: ex.free,
      napi_register_wasm_v1: ex.napi_register_wasm_v1,
    },
  };
  return new Environment(facade).exports;
}
