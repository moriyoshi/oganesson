// Stands in for Rolldown's native binding module (rolldown/dist/shared/
// binding-*.mjs, which loads the napi-rs addon). Oganesson bundles with
// esbuild instead (rolldown.js); Vite's own code only needs the binding's
// shape at load time. Housekeeping calls are no-ops, builtin plugins keep
// their name and options (the facade implements them), and anything else
// fails with a clear message when called.

const unavailable = (name) =>
  function () {
    throw new Error("og: Rolldown's native " + name + " is not available (Oganesson bundles with esbuild)");
  };

// A Rust builtin plugin made callable from JavaScript (Rolldown's
// makeBuiltinPluginCallable): the hooks the facade can run outside its own
// builds (rolldown.js, og.rolldownCallable); none for the others.
class BindingCallableBuiltinPlugin {
  constructor(plugin) {
    const hooks = (globalThis[Symbol.for("og.rolldownCallable")] || {})[plugin && plugin.__name] || {};
    for (const [name, hook] of Object.entries(hooks)) {
      this[name] = (...args) => hook(null, plugin.options || {}, ...args);
    }
  }
  getOrder() {
    return undefined;
  }
}

const host = () => globalThis[Symbol.for("og.rolldown")];

// rolldown/utils' tsconfig cache: esbuild's transform finds the nearest
// tsconfig.json itself (esbuild.go), so the cache holds nothing.
class TsconfigCache {
  constructor(_yarnPnp, path) {
    this.path = path;
  }
  clear() {}
  size() {
    return 0;
  }
}

// Oxc's transform (rolldown/utils transform / transformSync, which Vite's
// transformWithOxc uses), on esbuild.
function enhancedTransformSync(filename, sourceText, options = {}) {
  const file = String(filename).replace(/[?#].*$/, "");
  const lang = options.lang || { ".ts": "ts", ".mts": "ts", ".cts": "ts", ".tsx": "tsx", ".jsx": "jsx" }[file.slice(file.lastIndexOf("."))] || "js";
  const jsx = options.jsx;
  const out = host().transform(sourceText, JSON.stringify({
    loader: lang === "dts" ? "ts" : lang,
    sourcefile: file,
    jsxRuntime: jsx === "preserve" ? "preserve" : jsx?.runtime || "automatic",
    importSource: jsx?.importSource || "react",
    development: !!jsx?.development,
    define: options.define,
    target: [].concat(options.target || []).filter(Boolean),
    withMap: !!options.sourcemap,
  }));
  const result = typeof out === "string" ? { code: out, map: null } : { code: out.code, map: out.map ? JSON.parse(out.map) : null };
  return { ...result, errors: [], warnings: [], helpersUsed: {}, tsconfigFilePaths: [] };
}

// Oxc's minifier (rolldown/utils minify / minifySync), on esbuild.
function minifySync(filename, sourceText, options = {}) {
  const out = host().transform(sourceText, JSON.stringify({ loader: "js", sourcefile: String(filename), jsxRuntime: "preserve", minify: true, withMap: !!options.sourcemap }));
  return typeof out === "string" ? { code: out, map: null, errors: [] } : { code: out.code, map: out.map ? JSON.parse(out.map) : null, errors: [] };
}

const provided = {
  TsconfigCache,
  enhancedTransformSync,
  enhancedTransform: async (...args) => enhancedTransformSync(...args),
  minifySync,
  minify: async (...args) => minifySync(...args),
  initTraceSubscriber: () => undefined,
  startAsyncRuntime: () => undefined,
  shutdownAsyncRuntime: () => undefined,
  resetNativeMemoryStats: () => undefined,
  BindingCallableBuiltinPlugin,
};

const binding = new Proxy(
  {},
  {
    get: (_, k) => (k === "__esModule" ? false : provided[k] || unavailable(String(k))),
  },
);

// The module's own exports: require_binding and its __toESM helper.
export const t = () => binding;
export const n = (mod) => new Proxy({}, { get: (_, k) => (k === "default" ? mod : mod[k]) });
