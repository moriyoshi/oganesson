// The rolldown() of Oganesson's Vite builds. Vite's own code (config,
// builder, environments, its JS plugins) runs on Oganesson's build host;
// this module stands in for Rolldown's bundler: esbuild bundles (Go side:
// og.rolldown, esbuild.go), every resolution and load goes through the
// Rollup-compatible plugin container below, Rolldown's builtin plugins
// (Rust in Rolldown) are implemented here, and the output is a Rollup
// OutputBundle built from esbuild's metafile, so renderChunk /
// generateBundle / writeBundle plugins run as with Rolldown.
export * from "og:rolldown-real";
import { VERSION } from "og:rolldown-real";
import fs from "node:fs";
import path from "node:path";
import picomatch from "picomatch";
import * as acorn from "og:acorn";
import remapping from "og:remapping";
import { decode as decodeMappings, encode as encodeMappings } from "og:sourcemap-codec";

const host = globalThis[Symbol.for("og.rolldown")];
const TRACE = process.env.OG_VITE_TRACE === "1";

// startPreviewFor serves the build written to clientDir and serverDir the
// way og will (preview.go), shaped as Vite's PreviewServer for the tools
// that render through `vite preview` (TanStack Start's prerendering).
// The server sees the build's TSS_* environment (prerendering flags it
// reads at module load).
host.startPreviewFor = async (root, clientDir, serverDir) => {
  if (!path.isAbsolute(serverDir)) serverDir = path.join(root, serverDir);
  if (!path.isAbsolute(clientDir)) clientDir = path.join(root, clientDir);
  const build = (globalThis[Symbol.for("og.viteBuilds")] ?? []).find((b) => path.resolve(b.dir) === path.resolve(serverDir));
  if (!build?.entries.length) throw new Error(`og: no server build written to ${serverDir}`);
  const env = {};
  for (const [k, v] of Object.entries(process.env)) if (k.startsWith("TSS_") && v != null) env[k] = String(v);
  const { id, url } = await host.preview(root, clientDir, path.join(serverDir, build.entries[0]), env);
  return { resolvedUrls: { local: [url], network: [] }, close: async () => host.closePreview(id) };
};
// startProgramPreview serves the project through the server program its
// scripts run (React Router's react-router-serve), as og will, shaped as
// Vite's PreviewServer (React Router's prerendering).
host.startProgramPreview = async (root) => {
  const { id, url } = await host.programPreview(root);
  return { resolvedUrls: { local: [url], network: [] }, close: async () => host.closePreview(id) };
};
const ROLLUP_VERSION = "4.40.0";

// --- hooks ------------------------------------------------------------------

const handlerOf = (hook) => (typeof hook === "function" ? hook : hook && hook.handler);
const orderOf = (hook) => (hook && typeof hook === "object" ? hook.order : undefined);

function sorted(plugins, name) {
  const pre = [], normal = [], post = [];
  for (const p of plugins) {
    if (!p[name] && !builtins[p.name]?.[name]) continue;
    const o = orderOf(p[name]);
    (o === "pre" ? pre : o === "post" ? post : normal).push(p);
  }
  return [...pre, ...normal, ...post];
}

// Hook filters ({ filter: { id, code, moduleType }, handler }): strings
// are picomatch globs for ids and substrings for code.
function matcher(pattern, kind) {
  if (pattern instanceof RegExp) {
    return (v) => {
      pattern.lastIndex = 0;
      return pattern.test(v);
    };
  }
  if (typeof pattern === "string") {
    if (kind === "code") return (v) => v.includes(pattern);
    if (kind === "moduleType") return (v) => v === pattern;
    const m = picomatch(pattern, { dot: true });
    return (v) => m(v) || v === pattern;
  }
  throw new Error("og: unsupported filter pattern " + String(pattern));
}

function passes(f, value, kind) {
  if (f == null) return true;
  const n = typeof f === "string" || f instanceof RegExp || Array.isArray(f) ? { include: f } : f;
  const list = (x) => (x == null ? null : [].concat(x).map((p) => matcher(p, kind)));
  const include = list(n.include), exclude = list(n.exclude);
  if (exclude && exclude.some((m) => m(value))) return false;
  if (include && !include.some((m) => m(value))) return false;
  return true;
}

function filterPasses(hook, { id, code, moduleType }) {
  const f = hook && typeof hook === "object" ? hook.filter : undefined;
  if (!f) return true;
  if (Array.isArray(f)) return true; // composable filter expressions: the handler decides
  if (f.id !== undefined && id !== undefined && !passes(f.id, id, "id")) return false;
  if (f.code !== undefined && code !== undefined && !passes(f.code, code, "code")) return false;
  if (f.moduleType !== undefined && moduleType !== undefined && !passes(f.moduleType, moduleType, "moduleType")) return false;
  return true;
}

// --- module types -------------------------------------------------------------

const defaultModuleTypes = {
  ".js": "js", ".mjs": "js", ".cjs": "js", ".jsx": "jsx", ".ts": "ts", ".mts": "ts", ".cts": "ts", ".tsx": "tsx",
  ".json": "json", ".txt": "text",
};

function cleanId(id) {
  return id.replace(/^\0/, "").split(/[?#]/)[0];
}

// --- Rolldown's builtin plugins (Rust in Rolldown) -----------------------------

const MODULE_PRELOAD_POLYFILL = `(function polyfill() {
  const relList = document.createElement("link").relList;
  if (relList && relList.supports && relList.supports("modulepreload")) return;
  for (const link of document.querySelectorAll('link[rel="modulepreload"]')) processPreload(link);
  new MutationObserver((mutations) => {
    for (const mutation of mutations) {
      if (mutation.type !== "childList") continue;
      for (const node of mutation.addedNodes) if (node.tagName === "LINK" && node.rel === "modulepreload") processPreload(node);
    }
  }).observe(document, { childList: true, subtree: true });
  function getFetchOpts(link) {
    const fetchOpts = {};
    if (link.integrity) fetchOpts.integrity = link.integrity;
    if (link.referrerPolicy) fetchOpts.referrerPolicy = link.referrerPolicy;
    if (link.crossOrigin === "use-credentials") fetchOpts.credentials = "include";
    else if (link.crossOrigin === "anonymous") fetchOpts.credentials = "omit";
    else fetchOpts.credentials = "same-origin";
    return fetchOpts;
  }
  function processPreload(link) {
    if (link.ep) return;
    link.ep = true;
    const fetchOpts = getFetchOpts(link);
    fetch(link.href, fetchOpts);
  }
})();`;

// testFilter matches id against a builtin plugin's include / exclude
// (RegExp, string or array); absent is `absent`.
function testFilter(f, id, absent) {
  if (f == null) return absent;
  return [].concat(f).some((p) => matcher(p, "id")(id));
}

// isBuiltin: a runtime built-in of the environment (builtin:vite-resolve's
// builtins: strings and RegExps, e.g. node:* for a server consumer).
function isBuiltin(o, source) {
  const list = o.builtins || [];
  // og's own modules (og:kv) are built-ins of the server og bundles the
  // server environment's output into (server.go).
  if (o.environmentConsumer === "server" && (source.startsWith("node:") || source.startsWith("og:"))) return true;
  return list.some((b) => (b instanceof RegExp ? ((b.lastIndex = 0), b.test(source)) : b === source));
}

// isSsrExternal: a server environment's bare import stays an import of the
// package, as Vite externalizes dependencies for SSR: unless noExternal
// covers it (true, a name, a pattern), when external lists it (or is true),
// or by default when it resolves to a JavaScript file in node_modules.
function isSsrExternal(o, source, resolved) {
  if (o.environmentConsumer !== "server") return false;
  if (!/^(?![a-zA-Z]:)[\w@](?!.*:\/\/)/.test(source)) return false; // bare imports only
  const pkg = source.startsWith("@") ? source.split("/").slice(0, 2).join("/") : source.split("/")[0];
  // Vite filters by the package name (createFilter: picomatch, dot).
  const test = (m, x) => (m instanceof RegExp ? ((m.lastIndex = 0), m.test(x)) : typeof m === "string" && (m === x || picomatch.isMatch(x, m, { dot: true })));
  const matches = (list) => [].concat(list ?? []).some((m) => test(m, pkg) || test(m, source));
  const noExternal = o.noExternal;
  if (noExternal === true || (Array.isArray(noExternal) ? matches(noExternal) : noExternal != null && matches([noExternal]))) return false;
  if (o.external === true || (Array.isArray(o.external) && matches(o.external))) return true;
  if (resolved == null) return true; // unresolvable: left to the runtime
  const ext = path.extname(cleanId(resolved));
  return /[\\/]node_modules[\\/]/.test(resolved) && (!ext || ext === ".js" || ext === ".mjs" || ext === ".cjs");
}

const PRELOAD_HELPER = "\0vite/preload-helper.js";

// Hooks of builtin plugins, called with (build, options, ...args).
const builtins = {
  // Resolution (oxc-resolver in Rolldown): Vite's root-relative and /@fs/
  // ids, then esbuild's resolver configured with Vite's resolve options.
  "builtin:vite-resolve": {
    async resolveId(b, o, source, importer, extra) {
      // Built-ins of a server environment stay external (Rolldown's
      // platform: "node"; Vite lists them in builtins).
      if (isBuiltin(o, source)) return { id: source, external: true };
      if (source.startsWith("\0") || /^[a-z][a-z0-9+.-]*:/i.test(source) && !source.startsWith("file:")) return null;
      const [file, query = ""] = source.split(/(?=[?#])/);
      let p = file;
      if (p.startsWith("file://")) p = decodeURIComponent(new URL(p).pathname);
      if (p.startsWith("/@fs/")) p = p.slice(4);
      const root = o.resolveOptions.root;
      if (p.startsWith("/") && !fs.existsSync(p) && fs.existsSync(path.join(root, p))) p = path.join(root, p);
      // Virtual importers ("/@vite-plugin-pwa/virtual:pwa-register") have no
      // directory on disk: their bare imports resolve from the root.
      let resolveDir = importer && !importer.startsWith("\0") ? path.dirname(cleanId(importer)) : root;
      if (!fs.existsSync(resolveDir)) resolveDir = root;
      if (path.isAbsolute(p) && fs.existsSync(p) && fs.statSync(p).isFile()) return { id: p + query };
      const ro = o.resolveOptions || {};
      const resolverOpts = JSON.stringify({ platform: o.environmentConsumer === "server" ? "node" : "browser", conditions: ro.conditions, mainFields: ro.mainFields, extensions: ro.extensions });
      const r = await host.resolve(extra.buildId || 0, p, resolveDir, extra.kind || "import-statement", resolverOpts);
      // A server environment's bare import that does not resolve (an
      // optional package, as Astro's dynamic import("sharp")) stays an
      // import, as Vite externalizes it for SSR, unless noExternal covers it.
      // A later plugin may own an unresolved bare or virtual id (@siteData
      // in VitePress). Defer it here; the build's final unresolved-import
      // fallback externalizes genuine SSR package imports.
      if (!r) return null;
      if (!r.external && isSsrExternal(o, source, r.path)) return { id: source, external: true };
      return { id: r.path + query, external: r.external, moduleSideEffects: r.sideEffects === false ? false : undefined };
    },
  },
  // Rolldown's load fallback: the file itself.
  "builtin:vite-load-fallback": {
    load(b, o, id) {
      const file = cleanId(id);
      if (id.startsWith("\0") || !path.isAbsolute(file) || !fs.existsSync(file)) return null;
      return fs.readFileSync(file, "utf8");
    },
  },
  "builtin:vite-module-preload-polyfill": {
    resolveId(b, o, source) {
      return source === "vite/modulepreload-polyfill" ? "\0vite/modulepreload-polyfill.js" : null;
    },
    load(b, o, id) {
      if (id !== "\0vite/modulepreload-polyfill.js") return null;
      return o.isServer ? "" : MODULE_PRELOAD_POLYFILL;
    },
  },
  // TypeScript and JSX (Oxc in Rolldown), in plugin order: later plugins
  // (import.meta.glob, CSS, …) see JavaScript.
  "builtin:vite-transform": {
    transform(b, o, code, id, meta) {
      const file = cleanId(id);
      // The language is the id's own extension when it has one (a Vue
      // block, App.vue?vue&type=script&lang.ts, is TypeScript), else the
      // file's.
      const langOf = (x) => ({ ".ts": "ts", ".mts": "ts", ".cts": "ts", ".tsx": "tsx", ".jsx": "jsx" })[(x.match(/\.[a-z]+$/) || [""])[0]];
      const lang = meta.moduleType && meta.moduleType !== "js" ? meta.moduleType : langOf(id) ?? langOf(file);
      if (!lang || !["ts", "tsx", "jsx"].includes(lang)) return null;
      // As Vite's own filter, the id or the id without its query
      // (src/routes/index.tsx?tsr-split=component is TSX).
      const matches = (x) => testFilter(o.include, x, true) && !testFilter(o.exclude, x, false);
      if (!matches(id) && !matches(file)) return null;
      const jsx = (o.transformOptions && o.transformOptions.jsx) || {};
      const withMap = !!b.output?.sourcemap;
      const out = host.transform(code, JSON.stringify({ loader: lang, sourcefile: file, jsxRuntime: jsx.runtime || "automatic", importSource: jsx.importSource || "react", development: !!jsx.development, withMap }));
      return withMap ? { code: out.code, map: out.map, moduleType: "js" } : { code: out, moduleType: "js" };
    },
  },
  // Dynamic imports load their chunk's CSS and imports first: each
  // import(…) becomes __vitePreload(() => import(…), __VITE_PRELOAD__ …),
  // whose markers Vite's own vite:build-import-analysis fills in with the
  // chunk's dependencies in generateBundle (as Vite 7's JS plugin did).
  "builtin:vite-build-import-analysis": {
    resolveId(b, o, source) {
      return source === PRELOAD_HELPER ? PRELOAD_HELPER : null;
    },
    load(b, o, id) {
      return id === PRELOAD_HELPER ? o.preloadCode : null;
    },
    transform(b, o, code, id) {
      if (!o.insertPreload || id === PRELOAD_HELPER || !/\bimport\s*\(/.test(code)) return null;
      const imports = host.dynamicImports(id, code);
      if (!imports || imports.length === 0) return null;
      const urlArg = o.optimizeModulePreloadRelativePaths || o.renderBuiltUrl ? ",import.meta.url" : "";
      let out = code;
      for (let i = imports.length - 1; i >= 0; i--) {
        const [start, end] = imports[i];
        out = out.slice(0, start) + "__vitePreload(() => " + out.slice(start, end) + ",__VITE_IS_MODERN__?__VITE_PRELOAD__:void 0" + urlArg + ")" + out.slice(end);
      }
      if (!code.includes("const __vitePreload =")) out = `import { __vitePreload } from ${JSON.stringify(PRELOAD_HELPER)};` + out;
      return { code: out };
    },
  },
  // build.manifest (Rolldown's Rust plugin): Vite 7's JS manifestPlugin,
  // whose output Vite 8's native:manifest-compatible then completes. The
  // asset goes into the bundle directly, since that plugin reads it in
  // the same generateBundle round.
  "builtin:vite-manifest": {
    generateBundle(b, o, output, bundle) {
      const root = o.root;
      const isLegacy = !!o.isOutputOptionsForLegacyChunks?.(output);
      const nameOf = (chunk) => {
        if (chunk.facadeModuleId) {
          let name = path.relative(root, chunk.facadeModuleId).split(path.sep).join("/");
          if (isLegacy && !chunk.name.includes("-legacy")) {
            const ext = path.extname(name);
            name = `${ext ? name.slice(0, -ext.length) : name}-legacy${ext}`;
          }
          return name.replace(/\0/g, "");
        }
        return "_" + path.basename(chunk.fileName);
      };
      const internal = (files) => files.filter((f) => bundle[f] !== undefined).map((f) => nameOf(bundle[f]));
      const cssEntryFiles = new Set();
      for (const ref of Object.keys(o.cssEntries?.() || {})) {
        try {
          cssEntryFiles.add(b.getFileName(ref));
        } catch {}
      }
      const manifest = {};
      for (const item of Object.values(bundle)) {
        if (item.type === "chunk") {
          const c = { file: item.fileName, name: item.name };
          if (item.facadeModuleId) c.src = nameOf(item);
          if (item.isEntry) c.isEntry = true;
          if (item.isDynamicEntry) c.isDynamicEntry = true;
          const imports = internal(item.imports);
          if (imports.length) c.imports = imports;
          const dynamicImports = internal(item.dynamicImports);
          if (dynamicImports.length) c.dynamicImports = dynamicImports;
          if (item.viteMetadata?.importedCss.size) c.css = [...item.viteMetadata.importedCss];
          if (item.viteMetadata?.importedAssets.size) c.assets = [...item.viteMetadata.importedAssets];
          manifest[nameOf(item)] = c;
        } else if (item.names.length > 0) {
          const src = item.originalFileNames.length > 0 ? item.originalFileNames[0] : "_" + path.basename(item.fileName);
          const a = { file: item.fileName, src };
          if (cssEntryFiles.has(item.fileName)) {
            a.isEntry = true;
            a.names = item.names;
          }
          for (const key of [src, ...item.originalFileNames.slice(1)]) {
            if (!/\.[cm]?js$/.test(manifest[key]?.file || "")) manifest[key] = a;
          }
        }
      }
      const sorted = Object.fromEntries(Object.keys(manifest).sort().map((k) => [k, manifest[k]]));
      bundle[o.outPath] = outputAsset({ source: JSON.stringify(sorted, undefined, 2), name: path.basename(o.outPath) }, o.outPath);
    },
  },
  // JSON: esbuild's json loader (default and named exports).
  "builtin:vite-json": {
    transform(b, o, code, id, meta) {
      if (meta.moduleType === "json" || /\.json(?:$|\?)(?!commonjs-)/.test(id) && !/[?&](?:raw|url)\b/.test(id)) {
        return { code, moduleType: "json" };
      }
      return null;
    },
  },
};

// Builtin hooks that also run in a JS plugin container (Vite's dev server,
// as React Router's child compiler), where Rolldown makes the plugin
// callable (binding-stub.js BindingCallableBuiltinPlugin).
Object.defineProperty(globalThis, Symbol.for("og.rolldownCallable"), {
  value: { "builtin:vite-resolve": { resolveId: builtins["builtin:vite-resolve"].resolveId } },
});

// --- the build ------------------------------------------------------------------

class PluginContext {
  constructor(build, plugin) {
    this._build = build;
    this._plugin = plugin;
    this.meta = { rollupVersion: ROLLUP_VERSION, rolldownVersion: VERSION, watchMode: false };
  }
  get pluginName() {
    return this._plugin.name;
  }
  resolve(source, importer, options = {}) {
    return this._build.resolveId(source, importer, {
      ...options,
      skip: options.skipSelf === false ? [] : [{ plugin: this._plugin, source, importer }],
    });
  }
  emitFile(file) {
    return this._build.emitFile(file);
  }
  getFileName(ref) {
    return this._build.getFileName(ref);
  }
  setAssetSource(ref, source) {
    const f = this._build.emitted.get(ref);
    if (f) f.source = source;
  }
  getModuleInfo(id) {
    return this._build.moduleInfo(id);
  }
  getModuleIds() {
    return this._build.modules.keys();
  }
  async load({ id }) {
    await this._build.load(id);
    return this._build.moduleInfo(id);
  }
  addWatchFile() {}
  // Rollup's this.parse: an ESTree Program (acorn, as Rollup itself used;
  // Rolldown's is Oxc). TypeScript is not supported.
  parse(code, options = {}) {
    if (options.lang && options.lang !== "js") throw new Error("og: this.parse supports JavaScript only, not " + options.lang + " (" + this._plugin.name + ")");
    return acorn.parse(code, { ecmaVersion: "latest", sourceType: "module", allowHashBang: true, allowAwaitOutsideFunction: true, allowReturnOutsideFunction: !!options.allowReturnOutsideFunction });
  }
  warn(w) {
    this._build.log("warn", w, this._plugin);
  }
  info(w) {
    this._build.log("info", w, this._plugin);
  }
  debug() {}
  error(e) {
    const err = e instanceof Error ? e : Object.assign(new Error(typeof e === "string" ? e : e.message), e);
    err.plugin ??= this._plugin.name;
    throw err;
  }
}

function normalizeInput(input) {
  if (typeof input === "string") input = [input];
  if (Array.isArray(input)) {
    return input.map((id) => ({ name: path.basename(cleanId(id)).replace(/\.[^.]+$/, ""), id }));
  }
  return Object.entries(input || {}).map(([name, id]) => ({ name, id }));
}

// Rolldown's define, as esbuild's. A wildcard key ("import.meta.env.*":
// undefined, which Vite adds) has no esbuild form; when its prefix is
// itself defined as an object (import.meta.env), member reads of unknown
// keys already evaluate to undefined, so the wildcard is redundant.
function esbuildDefine(define, warn) {
  const out = {};
  for (const [k, v] of Object.entries(define)) {
    if (!k.endsWith(".*")) {
      out[k] = esbuildDefineValue(v);
      continue;
    }
    const prefix = k.slice(0, -2);
    if (!(prefix in define)) warn({ message: "og: define " + k + " has no esbuild equivalent and is ignored" });
  }
  return out;
}

// esbuildDefineValue: esbuild takes JSON or an entity name; Rolldown any
// expression. A literal that is not JSON (Astro's import.meta.env object
// has undefined members) is evaluated (define values come from the build's
// own config) and given as JSON, its undefined members omitted (reading
// them still gives undefined).
function esbuildDefineValue(v) {
  if (typeof v !== "string") return v;
  try {
    JSON.parse(v);
    return v;
  } catch {}
  if (!/^\s*[\[{]/.test(v)) return v;
  try {
    const value = Function("return (" + v + ")")();
    if (value !== null && typeof value === "object") return JSON.stringify(value);
  } catch {}
  return v;
}

// representativeName is Rolldown's name for a chunk from one of its
// modules (representative_file_name): the file stem, or for index / mod the
// directory's name.
function representativeName(id) {
  const clean = cleanId(id);
  const ext = path.extname(clean);
  const stem = path.basename(clean, ext) || clean;
  if (stem === "index" || stem === "mod") return path.basename(path.dirname(clean)) || stem;
  return stem;
}

// chunkGroupNamer returns the group name output.manualChunks (a function of
// the id, or an object of names to ids / packages) or codeSplitting /
// advancedChunks groups ({ name, test, priority }) give a module, or
// undefined.
function chunkGroupNamer(output, options, facade) {
  const manual = output.manualChunks ?? options.manualChunks;
  const split = output.codeSplitting ?? options.codeSplitting;
  const advanced = (split && typeof split === "object" ? split : null) ?? output.advancedChunks ?? options.advancedChunks;
  const meta = { getModuleInfo: (id) => facade.moduleInfo(id), getModuleIds: () => facade.modules.keys() };
  if (typeof manual === "function") {
    return (id) => manual(id, meta) ?? undefined;
  }
  if (manual && typeof manual === "object") {
    const lists = Object.entries(manual).map(([name, ids]) => [name, [].concat(ids)]);
    return (id) => {
      const clean = cleanId(id);
      for (const [name, ids] of lists) {
        for (const want of ids) {
          if (clean === want || clean.includes("/node_modules/" + want + "/")) return name;
        }
      }
      return undefined;
    };
  }
  if (advanced && Array.isArray(advanced.groups)) {
    const groups = advanced.groups.map((g, i) => ({ ...g, i })).sort((a, b) => (b.priority ?? 0) - (a.priority ?? 0) || a.i - b.i);
    return (id) => {
      for (const g of groups) {
        const t = g.test;
        const ok = t == null ? true : typeof t === "function" ? t(id, meta) : t instanceof RegExp ? (t.lastIndex = 0, t.test(id)) : id.includes(String(t));
        if (ok) return typeof g.name === "function" ? g.name(id, meta) ?? undefined : g.name;
      }
      return undefined;
    };
  }
  return null;
}

// Rollup file name patterns ("assets/[name]-[hash].js") as esbuild's.
// DIR_SEP stands for "/" in entry names given to esbuild.
const DIR_SEP = "~og~";

function esbuildNames(pattern, fallback) {
  if (typeof pattern !== "string") return fallback;
  return pattern.replace(/\.(?:js|mjs|cjs)$/, "").replace(/\[hash(?::\d+)?\]/g, "[hash]").replace(/\[format\]/g, "es");
}

// exportNames lists the names a module's code exports. reexports(source)
// gives the names of an `export * from source` (default never comes
// through export *).
function exportNames(code, reexports = () => []) {
  let program;
  try {
    program = acorn.parse(code, { ecmaVersion: "latest", sourceType: "module", allowHashBang: true, allowAwaitOutsideFunction: true });
  } catch {
    return [];
  }
  const names = new Set();
  const bindings = (p) => {
    if (!p) return;
    if (p.type === "Identifier") names.add(p.name);
    else if (p.type === "ObjectPattern") p.properties.forEach((q) => bindings(q.type === "RestElement" ? q.argument : q.value));
    else if (p.type === "ArrayPattern") p.elements.forEach((q) => bindings(q && q.type === "RestElement" ? q.argument : q));
    else if (p.type === "AssignmentPattern") bindings(p.left);
  };
  for (const n of program.body) {
    if (n.type === "ExportDefaultDeclaration") names.add("default");
    else if (n.type === "ExportAllDeclaration" && n.exported) names.add(n.exported.name ?? n.exported.value);
    else if (n.type === "ExportAllDeclaration") reexports(n.source.value).forEach((x) => x !== "default" && names.add(x));
    else if (n.type === "ExportNamedDeclaration") {
      const d = n.declaration;
      if (d?.type === "VariableDeclaration") d.declarations.forEach((x) => bindings(x.id));
      else if (d?.id) names.add(d.id.name);
      for (const sp of n.specifiers || []) names.add(sp.exported.name ?? sp.exported.value);
    }
  }
  return [...names];
}

function hashOf(source) {
  const bytes = typeof source === "string" ? new TextEncoder().encode(source) : source;
  let h1 = 0x811c9dc5, h2 = 0x01000193;
  for (let i = 0; i < bytes.length; i++) {
    h1 = Math.imul(h1 ^ bytes[i], 16777619) >>> 0;
    h2 = Math.imul(h2 + bytes[i], 2246822519) >>> 0;
  }
  const s = (h1.toString(36) + h2.toString(36)).replace(/[^a-z0-9]/g, "");
  return (s + "00000000").slice(0, 8);
}

class Build {
  constructor(options) {
    this.options = options;
    this.plugins = (options.plugins || []).flat(Infinity).filter(Boolean);
    this.contexts = new Map();
    this.modules = new Map();
    // importer + "\0" + source -> resolved id, for following export *.
    this.resolutions = new Map();
    // The module graph as esbuild resolves it: id -> Set of imported ids,
    // static and dynamic, for getModuleInfo's importers / importedIds (Astro
    // walks importers to place CSS).
    this.imports = new Map();
    this.dynamicImports = new Map();
    this.emitted = new Map();
    this.nextRef = 0;
    this.buildId = 0;
    this.root = this.plugins.find((p) => p.name === "builtin:vite-resolve")?._options?.resolveOptions?.root || process.cwd();
  }

  ctx(plugin) {
    let c = this.contexts.get(plugin);
    if (!c) this.contexts.set(plugin, (c = new PluginContext(this, plugin)));
    return c;
  }

  log(level, w, plugin) {
    const log = typeof w === "string" ? { message: w } : w;
    log.plugin ??= plugin && plugin.name;
    if (this.options.onLog) this.options.onLog(level, log, () => {});
    else if (level === "warn") console.warn(log.message);
  }

  async call(plugin, name, args, ctx = this.ctx(plugin)) {
    // OG_VITE_TRACE=1 logs every plugin hook call (debugging builds).
    if (TRACE) console.error("og-vite-trace", plugin.name, name, typeof args[0] === "string" ? args[name === "transform" ? 1 : 0] : "");
    const builtin = builtins[plugin.name]?.[name];
    if (builtin) return builtin(this, plugin._options || {}, ...args);
    const fn = handlerOf(plugin[name]);
    return fn ? fn.apply(ctx, args) : undefined;
  }

  async parallel(name, args) {
    for (const p of sorted(this.plugins, name)) await this.call(p, name, args);
  }

  // skip is Rollup's: the {plugin, source, importer} of each this.resolve
  // (skipSelf) up the chain. A plugin is skipped for the same source and
  // importer it is resolving itself, and the list grows through nested
  // resolves, so plugins that resolve through one another terminate.
  // resolveId applies the build's external option (Rollup's: tested on
  // the specifier, then on the resolved id) around the plugins' resolution.
  async resolveId(source, importer, opts = {}) {
    if (!opts.isEntry && this.isExternalOption(source, importer, false)) return { id: source, external: true };
    const r = await this.resolvePlugins(source, importer, opts);
    if (r && !r.external && !opts.isEntry && this.isExternalOption(r.id, importer, true)) return { ...r, external: true };
    return r;
  }

  isExternalOption(id, importer, isResolved) {
    const ext = this.options.external;
    if (ext == null) return false;
    if (typeof ext === "function") return !!ext(id, importer, isResolved);
    return [].concat(ext).some((e) => (e instanceof RegExp ? e.test(id) : e === id));
  }

  async resolvePlugins(source, importer, { isEntry = false, kind, skip = [], custom, attributes = {}, buildId } = {}) {
    const skipped = new Set(skip.filter((c) => c.source === source && c.importer === importer).map((c) => c.plugin));
    for (const p of sorted(this.plugins, "resolveId")) {
      if (skipped.has(p) || !filterPasses(p.resolveId, { id: source })) continue;
      const ctx = Object.create(this.ctx(p));
      ctx.resolve = (s, i, opts = {}) => this.resolveId(s, i, { ...opts, skip: opts.skipSelf === false ? skip : [...skip, { plugin: p, source: s, importer: i }] });
      const r = await this.call(p, "resolveId", [source, importer, { isEntry, kind, custom, attributes, buildId: buildId ?? this.buildId }], ctx);
      if (r == null) continue;
      if (r === false) return { id: source, external: true };
      if (typeof r === "string") return { id: r, external: false };
      return { external: false, ...r, external: r.external === true || r.external === "absolute" || r.external === "relative" };
    }
    return this.defaultResolve(source, importer, kind, buildId ?? this.buildId);
  }

  // defaultResolve is Rolldown's own resolution after the plugins, for
  // builds without Vite's resolve plugin (which resolves everything in a
  // Vite build): plain rolldown() calls, as Vite's loadConfigFromFile.
  async defaultResolve(source, importer, kind, buildId) {
    if (this.plugins.some((p) => p.name === "builtin:vite-resolve")) return null;
    if (source.startsWith("\0") || (/^[a-z][a-z0-9+.-]*:/i.test(source) && !source.startsWith("file:"))) return null;
    let p = source.startsWith("file://") ? decodeURIComponent(new URL(source).pathname) : source;
    if (path.isAbsolute(p) && fs.existsSync(p) && fs.statSync(p).isFile()) return { id: p, external: false };
    const ro = this.options.resolve || {};
    const resolveDir = importer && !importer.startsWith("\0") ? path.dirname(cleanId(importer)) : process.cwd();
    const opts = JSON.stringify({ platform: this.options.platform === "node" ? "node" : "browser", conditions: ro.conditionNames, mainFields: ro.mainFields, extensions: ro.extensions });
    const r = await host.resolve(buildId || 0, p, resolveDir, kind || "import-statement", opts);
    if (!r) return null;
    return r.external ? { id: source, external: true } : { id: r.path, external: false, moduleSideEffects: r.sideEffects === false ? false : undefined };
  }

  moduleTypeOf(id) {
    const ext = path.extname(cleanId(id));
    return { ...defaultModuleTypes, ...(this.options.moduleTypes || {}) }[ext];
  }

  async load(id) {
    let code, moduleType, meta = {};
    // The source maps of the load and transform steps, in order: composed
    // (remapping, as Vite's combineSourcemaps) into the map esbuild reads
    // with the module, so output maps point at the original source.
    const maps = [];
    const addMap = (m) => {
      if (m == null) return; // no map: the step kept positions
      if (typeof m === "string") m = JSON.parse(m);
      if (!m.mappings) return; // { mappings: "" }: no map either
      maps.push(m);
    };
    for (const p of sorted(this.plugins, "load")) {
      if (!filterPasses(p.load, { id })) continue;
      const r = await this.call(p, "load", [id]);
      if (r == null) continue;
      if (typeof r === "string") code = r;
      else {
        ({ code, moduleType, meta = {} } = { ...r, meta: r.meta || {} });
        addMap(r.map);
      }
      break;
    }
    // Rolldown's own load (the file), for builds without Vite's fallback.
    if (code == null && !id.startsWith("\0") && path.isAbsolute(cleanId(id)) && fs.existsSync(cleanId(id))) code = fs.readFileSync(cleanId(id), "utf8");
    if (code == null) throw new Error(`Could not load ${id}`);
    moduleType ??= this.moduleTypeOf(id) || "js";
    const original = code;
    const file = cleanId(id);
    const combined = () => {
      if (!maps.length) {
        return { version: 3, sources: [file], sourcesContent: [original], names: [], mappings: "" };
      }
      const chain = maps.slice().reverse().map((m) => ({ ...m, sources: (m.sources || []).map((s) => s || file) }));
      const last = chain[chain.length - 1];
      if (!last.sourcesContent || last.sourcesContent.every((c) => c == null)) last.sourcesContent = last.sources.map(() => original);
      return JSON.parse(remapping(chain, () => null).toString());
    };
    for (const p of sorted(this.plugins, "transform")) {
      if (!filterPasses(p.transform, { id, code, moduleType })) continue;
      // this.getCombinedSourcemap() is about the module being transformed.
      const ctx = Object.create(this.ctx(p));
      ctx.getCombinedSourcemap = () => sourceMapOf(JSON.stringify(combined()), path.dirname(file));
      const r = await this.call(p, "transform", [code, id, { moduleType }], ctx);
      if (r == null) continue;
      if (typeof r === "string") {
        code = r;
        continue;
      }
      if (r.code != null) code = r.code;
      if (r.moduleType) moduleType = r.moduleType;
      if (r.meta) Object.assign(meta, r.meta);
      addMap(r.map);
    }
    const map = this.output?.sourcemap && maps.length ? combined() : null;
    this.modules.set(id, { id, code, meta, moduleType });
    return { code, moduleType, map };
  }

  // exportsOf lists a module's exports, following export * through the
  // build's resolutions (seen guards cycles).
  exportsOf(id, seen = new Set()) {
    const m = this.modules.get(id);
    if (!m || !["js", "jsx", "ts", "tsx"].includes(m.moduleType) || seen.has(id)) return [];
    seen.add(id);
    // A module left in TS or JSX by a late transform (plugin-vue's
    // <script lang="ts">) is parsed after esbuild strips it.
    let code = m.code;
    if (m.moduleType !== "js") {
      try {
        code = host.transform(code, JSON.stringify({ loader: m.moduleType, sourcefile: cleanId(id), jsxRuntime: "automatic", importSource: "react" }));
      } catch {
        return [];
      }
    }
    return exportNames(code, (source) => {
      const dep = this.resolutions.get(id + "\0" + source);
      return dep ? this.exportsOf(dep, seen) : [];
    });
  }

  moduleInfo(id) {
    const m = this.modules.get(id);
    if (!m) return null;
    const importersIn = (graph) => [...graph].filter(([, ids]) => ids.has(id)).map(([from]) => from);
    return {
      id, code: m.code, meta: m.meta, isEntry: this.entryIds?.has(id) ?? false, isExternal: false,
      importers: importersIn(this.imports), dynamicImporters: importersIn(this.dynamicImports),
      importedIds: [...(this.imports.get(id) || [])], dynamicallyImportedIds: [...(this.dynamicImports.get(id) || [])],
      exports: [], hasDefaultExport: null,
    };
  }

  emitFile(file) {
    const ref = "og" + (++this.nextRef).toString(36);
    this.emitted.set(ref, { ...file });
    if (file.type === "asset" && file.source != null) this.emitted.get(ref).fileName ??= this.assetFileName(file);
    return ref;
  }

  assetFileName(file) {
    if (file.fileName) return file.fileName;
    const name = file.name || (file.names && file.names[0]) || "asset";
    const ext = path.extname(name);
    const pattern = this.output?.assetFileNames ?? "assets/[name]-[hash][extname]";
    const info = { type: "asset", name, names: [name], source: file.source, originalFileName: file.originalFileName ?? null, originalFileNames: file.originalFileNames ?? [] };
    const p = typeof pattern === "function" ? pattern(info) : pattern;
    return p
      .replace(/\[name\]/g, path.basename(name, ext))
      .replace(/\[hash(?::\d+)?\]/g, hashOf(file.source))
      .replace(/\[extname\]/g, ext)
      .replace(/\[ext\]/g, ext.slice(1));
  }

  getFileName(ref) {
    const f = this.emitted.get(ref);
    if (!f) throw new Error("og: unknown file reference " + ref);
    if (f.type === "asset") return (f.fileName ??= this.assetFileName(f));
    if (f.fileName) return f.fileName;
    throw new Error("og: the file name of emitted chunk " + ref + " is not known yet");
  }

  // resolveFileUrls replaces import.meta.ROLLDOWN_FILE_URL_<ref>[_<urlId>]
  // (and Rollup's ROLLUP_FILE_URL_) in a chunk, as Rollup does while
  // rendering it: the first resolveFileUrl hook's expression, else a URL
  // relative to the chunk.
  async resolveFileUrls(chunk, output) {
    const re = /import\.meta\.ROLL(?:DOWN|UP)_FILE_URL_(og[0-9a-z]+)(?:_([0-9a-f]+))?/g;
    const found = [...chunk.code.matchAll(re)];
    if (!found.length) return chunk.code;
    let code = "", last = 0;
    for (const m of found) {
      const ref = m[1], urlId = m[2];
      const fileName = this.getFileName(ref);
      const relativePath = path.posix.relative(path.posix.dirname(chunk.fileName), fileName) || fileName;
      const info = { chunkId: chunk.fileName, fileName, format: output.format || "es", moduleId: null, referenceId: ref, relativePath, urlId };
      let expr = null;
      for (const p of sorted(this.plugins, "resolveFileUrl")) {
        expr = await this.call(p, "resolveFileUrl", [info]);
        if (expr != null) break;
      }
      if (expr == null) {
        expr = info.format === "cjs"
          ? `require("u" + "rl").pathToFileURL(__dirname + "/" + ${JSON.stringify(relativePath)}).href`
          : `new URL(${JSON.stringify(relativePath)}, import.meta.url).href`;
      }
      code += chunk.code.slice(last, m.index) + expr;
      last = m.index + m[0].length;
    }
    return code + chunk.code.slice(last);
  }

  // The esbuild run: build hooks (resolve/load/transform) happen here.
  // late: chunks emitted during an earlier pass (a plugin's transform
  // emitting one, as Nuxt's SSR styles do), built in a pass of their own.
  async bundle(output, late) {
    this.output = output;
    // Chunk grouping options have no esbuild equivalent: chunks follow
    // esbuild's code splitting (shared modules go to shared chunks), and a
    // chunk whose modules all belong to one group takes its name (toBundle).
    for (const k of ["manualChunks", "advancedChunks", "codeSplitting"]) {
      const v = output[k] ?? this.options[k];
      if (v && (k !== "codeSplitting" || typeof v === "object")) this.log("warn", { message: `og: output.${k} names chunks but does not group them; chunks follow esbuild's code splitting` });
    }
    const entries = late ? [] : normalizeInput(this.options.input);
    for (const f of late ?? this.emitted.values()) {
      if (f.type === "chunk") entries.push({ name: f.name || path.basename(cleanId(f.id)).replace(/\.[^.]+$/, ""), id: f.id, emitted: f });
    }
    const resolved = [];
    for (const e of entries) {
      const r = await this.resolveId(e.id, undefined, { isEntry: true, kind: "entry-point" });
      resolved.push({ ...e, resolved: r ? r.id : e.id, result: r });
    }
    // esbuild gets the resolved ids as its entry points; they are not
    // resolved again (Rollup resolves an entry once; "\0virtual" means
    // nothing to the plugins).
    const resolvedEntries = new Map(resolved.filter((e) => e.result).map((e) => [e.resolved, e.result]));
    this.entryIds = new Set([...(late ? this.entryIds ?? [] : []), ...resolved.map((e) => e.resolved)]);
    const transform = this.options.transform || {};
    const vt = this.plugins.find((p) => p.name === "builtin:vite-transform")?._options;
    const jsx = (vt && vt.transformOptions && vt.transformOptions.jsx) || transform.jsx || {};
    const vr = this.plugins.find((p) => p.name === "builtin:vite-resolve")?._options?.resolveOptions || {};
    const isProd = vr.isProduction !== false;
    const spec = {
      // esbuild keeps only the base of an output path under a [name]
      // template: input names with directories (SvelteKit's
      // "entries/pages/+page.svelte") are encoded, and renameChunks puts
      // them back as Rollup writes them.
      entries: resolved.map((e) => ({ in: e.resolved, out: e.name.replaceAll("/", DIR_SEP) })),
      absWorkingDir: this.root,
      outdir: output.dir || path.dirname(output.file || path.join(this.root, "dist", "x")),
      format: output.format || "es",
      platform: this.options.platform || "browser",
      target: [].concat(transform.target || []).filter(Boolean),
      define: esbuildDefine(transform.define || {}, (m) => this.log("warn", m)),
      jsx: { runtime: jsx.runtime || "automatic", importSource: jsx.importSource || "react", development: !!jsx.development },
      minify: output.minify === true || (output.minify && typeof output.minify === "object" && output.minify.compress !== false),
      sourcemap: !!output.sourcemap,
      entryNames: esbuildNames(output.entryFileNames, "[name]-[hash]"),
      chunkNames: esbuildNames(output.chunkFileNames, "[name]-[hash]"),
      // Split as Rolldown splits: modules shared by entries are one
      // instance (SvelteKit's error page and its pages share Svelte's
      // component context). Object-valued defines, which make esbuild's
      // renamer race in split unminified builds, are injected (esbuild.go).
      splitting: (output.format || "es") === "es" && !output.inlineDynamicImports,
      conditions: (vr.conditions || []).map((c) => (c === "development|production" ? (isProd ? "production" : "development") : c)),
      mainFields: vr.mainFields,
      extensions: vr.extensions,
      legalComments: output.comments === false || (output.comments && output.comments.legal === false) ? "none" : undefined,
    };
    const result = await host.build(JSON.stringify(spec), {
      resolve: async (a) => {
        this.buildId = a.buildId;
        if (a.isEntry && resolvedEntries.has(a.source)) return resolvedEntries.get(a.source);
        const r = await this.resolveId(a.source, a.importer || undefined, { isEntry: a.isEntry, kind: a.kind, buildId: a.buildId });
        // A bare or virtual import nothing resolves is external, with
        // Rollup's UNRESOLVED_IMPORT warning (Vite's log handler makes it an
        // error for client builds); a relative one fails.
        if (!r && !a.isEntry && a.importer && !/^\.{0,2}\//.test(a.source) && !path.isAbsolute(a.source)) {
          this.log("warn", {
            code: "UNRESOLVED_IMPORT", exporter: a.source, id: a.importer,
            message: `"${a.source}" is imported by "${path.relative(this.root, a.importer)}", but could not be resolved – treating it as an external dependency.`,
          });
          return { id: a.source, external: true };
        }
        if (r && !r.external && a.importer) {
          this.resolutions.set(a.importer + "\0" + a.source, r.id);
          const graph = a.kind === "dynamic-import" ? this.dynamicImports : this.imports;
          if (!graph.has(a.importer)) graph.set(a.importer, new Set());
          graph.get(a.importer).add(r.id);
        }
        return r;
      },
      load: async (a) => {
        this.buildId = a.buildId;
        const { code, moduleType, map } = await this.load(a.id);
        // esbuild reads a module's input map from its sourceMappingURL.
        const withMap = map ? code + "\n//# sourceMappingURL=data:application/json;base64," + Buffer.from(JSON.stringify(map)).toString("base64") + "\n" : code;
        return { code: withMap, loader: moduleType === "css" ? "js" : moduleType };
      },
    });
    return { result, resolved };
  }

  // esbuild's outputs as a Rollup OutputBundle.
  toBundle({ result, resolved }, output) {
    const meta = JSON.parse(result.metafile);
    const outdir = path.resolve(this.root, output.dir || "dist");
    const fileOf = (p) => path.relative(outdir, path.resolve(this.root, p)).split(path.sep).join("/");
    const idOf = (key) => (key.startsWith(this_ns()) ? key.slice(this_ns().length) : path.resolve(this.root, key));
    const userEntries = new Map(resolved.filter((e) => !e.emitted).map((e) => [e.resolved, e]));
    const emittedEntries = new Map(resolved.filter((e) => e.emitted).map((e) => [e.resolved, e]));
    const texts = new Map(result.outputs.map((o) => [path.resolve(o.path), o.text]));
    // esbuild leaves modules that render no code (CSS modules after
    // vite:css-post) out of every output's inputs; Rollup lists them in
    // their chunk, and Vite collects CSS through that. Each goes to the
    // output of its nearest importer that has one.
    const outputOf = new Map();
    for (const [key, out] of Object.entries(meta.outputs)) for (const k of Object.keys(out.inputs || {})) if (!outputOf.has(k)) outputOf.set(k, key);
    const importers = new Map();
    for (const [k, info] of Object.entries(meta.inputs || {})) {
      for (const i of info.imports || []) if (!i.external) (importers.get(i.path) ?? importers.set(i.path, []).get(i.path)).push(k);
    }
    for (const k of Object.keys(meta.inputs || {})) {
      if (outputOf.has(k)) continue;
      const seen = new Set([k]);
      for (let queue = [...(importers.get(k) || [])]; queue.length; ) {
        const imp = queue.shift();
        if (seen.has(imp)) continue;
        seen.add(imp);
        const owner = outputOf.get(imp);
        if (owner) {
          meta.outputs[owner].inputs[k] = { bytesInOutput: 0 };
          break;
        }
        queue.push(...(importers.get(imp) || []));
      }
    }
    const bundle = {};
    const groupOf = chunkGroupNamer(output, this.options, this);
    for (const [key, out] of Object.entries(meta.outputs)) {
      if (key.endsWith(".map")) continue;
      const fileName = fileOf(key);
      const code = texts.get(path.resolve(this.root, key)) ?? "";
      const facade = out.entryPoint ? idOf(out.entryPoint) : null;
      const moduleIds = Object.keys(out.inputs || {}).map(idOf);
      const modules = {};
      for (const [k, v] of Object.entries(out.inputs || {})) {
        const id = idOf(k);
        const m = this.modules.get(id);
        modules[id] = { code: null, originalLength: m?.code.length ?? 0, renderedLength: v.bytesInOutput, removedExports: [] };
        // esbuild reports no per-module exports: the module's declared
        // ones stand in (plugin-rsc builds client reference proxies from
        // them), computed when first read.
        let rendered;
        Object.defineProperty(modules[id], "renderedExports", {
          get: () => (rendered ??= this.exportsOf(id)),
          set: (v) => { rendered = v; },
          enumerable: true, configurable: true,
        });
      }
      const imports = [], dynamicImports = [], externals = [];
      for (const i of out.imports || []) {
        if (i.external) {
          externals.push(i.path);
          continue;
        }
        (i.kind === "dynamic-import" ? dynamicImports : imports).push(fileOf(i.path));
      }
      const userEntry = facade && userEntries.get(facade);
      const emitted = facade && emittedEntries.get(facade);
      // Rolldown's names for the chunks esbuild names itself ("chunk" for
      // a shared one, the file's base for a dynamic entry): a dynamic
      // entry's module, a shared chunk's last executed module, or the group
      // all of its modules belong to (manualChunks / codeSplitting).
      const esbuildName = path.basename(fileName).replace(/-[^-]*\.js$/, "").replace(/\.js$/, "");
      let name = userEntry?.name ?? emitted?.name;
      let rolldownNamed = false;
      if (name == null) {
        const own = Object.keys(out.inputs || {}).filter((k) => k !== "<runtime>").map(idOf);
        name = facade ? representativeName(facade) : own.length ? representativeName(own[own.length - 1]) : "chunk";
        if (groupOf && own.length) {
          const groups = new Set(own.map((id) => groupOf(id)));
          if (groups.size === 1 && [...groups][0] != null) name = String([...groups][0]);
        }
        rolldownNamed = name !== esbuildName;
      }
      const chunk = {
        type: "chunk", fileName, preliminaryFileName: fileName, name: name ?? esbuildName,
        code, map: sourceMapOf(texts.get(path.resolve(this.root, key) + ".map"), path.dirname(path.resolve(outdir, fileName))), sourcemapFileName: null,
        isEntry: !!(userEntry || emitted), isDynamicEntry: !!facade && !userEntry && !emitted, isImplicitEntry: false,
        facadeModuleId: facade, moduleIds, imports, dynamicImports, exports: out.exports || [], importedBindings: {}, referencedFiles: [],
      };
      // Vite (re)defines these per hook call (injectChunkMetadata); being
      // configurable keeps its redefinitions legal on this one object.
      Object.defineProperty(chunk, "ogExternals", { value: externals, configurable: true, writable: true });
      Object.defineProperty(chunk, "ogRename", { value: rolldownNamed, configurable: true, writable: true });
      Object.defineProperty(chunk, "modules", { value: modules, enumerable: true, configurable: true, writable: true });
      Object.defineProperty(chunk, "viteMetadata", { value: { importedCss: new Set(), importedAssets: new Set() }, enumerable: true, configurable: true, writable: true });
      if (chunk.map) chunk.sourcemapFileName = fileName + ".map";
      pruneExternalImports(chunk);
      bundle[fileName] = chunk;
      if (emitted) emitted.emitted.fileName = fileName;
    }
    this.applyPaths(bundle, output);
    return this.renameChunks(bundle, output, emittedEntries);
  }

  // applyPaths rewrites external import specifiers by output.paths (an
  // object or a function of the id), as Rollup writes them.
  applyPaths(bundle, output) {
    const paths = output.paths;
    if (!paths) return;
    const map = (id) => (typeof paths === "function" ? paths(id) : Object.hasOwn(paths, id) ? paths[id] : undefined);
    const re = /(\bfrom\s*|\bimport\s*\(\s*|\bimport\s*)(["'])([^"'\n]+)\2/g;
    for (const chunk of Object.values(bundle)) {
      if (chunk.type !== "chunk") continue;
      chunk.code = chunk.code.replace(re, (all, lead, q, id) => {
        if (!(chunk.ogExternals || []).includes(id)) return all;
        const to = map(id);
        return to == null ? all : lead + JSON.stringify(to);
      });
    }
  }

  // renameChunks applies what esbuild's naming cannot express: function
  // entryFileNames / chunkFileNames (Rollup calls them with the chunk's
  // info; Astro names its server entry this way) and extensions other than
  // .js. Chunks are renamed after the build and the imports between them
  // (relative specifiers in the code) rewritten.
  renameChunks(bundle, output, emittedEntries) {
    const renames = new Map();
    for (const chunk of Object.values(bundle)) {
      if (chunk.type !== "chunk") continue;
      // (A chunk Rolldown names differently from esbuild is renamed by the
      // pattern, Rolldown's default if none.)
      const pattern = (chunk.isEntry ? output.entryFileNames : output.chunkFileNames) ?? (chunk.ogRename ? (chunk.isEntry ? "[name].js" : "[name]-[hash].js") : undefined);
      const nested = chunk.fileName.includes(DIR_SEP);
      if (typeof pattern !== "function" && !(typeof pattern === "string" && (nested || chunk.ogRename || /\.(?:mjs|cjs)$/.test(pattern)))) continue;
      const info = {
        type: "chunk", name: chunk.name, facadeModuleId: chunk.facadeModuleId, isEntry: chunk.isEntry, isDynamicEntry: chunk.isDynamicEntry,
        isImplicitEntry: false, moduleIds: chunk.moduleIds, exports: chunk.exports,
      };
      const p = typeof pattern === "function" ? pattern(info) : pattern;
      if (typeof p !== "string") continue;
      const hash = hashOf(chunk.code);
      // Rollup's sanitizeFileName on the name (a drive letter aside).
      const safeName = chunk.name.replace(/[\u0000-\u001F"#$%&*+,:;<=>?[\]^`{|}\u007F]/g, "_");
      const name = p
        .replace(/\[name\]/g, safeName)
        .replace(/\[hash(?::(\d+))?\]/g, (_, n) => (n ? hash.slice(0, Number(n)) : hash))
        .replace(/\[format\]/g, "es")
        .replace(/\[extname\]/g, ".js")
        .replace(/\[ext\]/g, "js");
      if (name !== chunk.fileName && !bundle[name] && ![...renames.values()].includes(name)) renames.set(chunk.fileName, name);
    }
    if (!renames.size) return bundle;
    const rel = (from, to) => {
      const r = path.posix.relative(path.posix.dirname(from), to);
      return r.startsWith(".") ? r : "./" + r;
    };
    const out = {};
    for (const item of Object.values(bundle)) {
      const oldName = item.fileName;
      const newName = renames.get(oldName) ?? oldName;
      if (item.type === "chunk") {
        // References from this chunk to renamed chunks, then its own name.
        for (const [from, to] of renames) {
          if (!item.imports.includes(from) && !item.dynamicImports.includes(from)) continue;
          for (const q of ['"', "'"]) item.code = item.code.split(q + rel(oldName, from) + q).join(q + rel(newName, to) + q);
        }
        // Its own relative imports of unrenamed chunks move with it.
        if (newName !== oldName && path.posix.dirname(newName) !== path.posix.dirname(oldName)) {
          for (const dep of [...item.imports, ...item.dynamicImports]) {
            if (renames.has(dep)) continue;
            for (const q of ['"', "'"]) item.code = item.code.split(q + rel(oldName, dep) + q).join(q + rel(newName, dep) + q);
          }
        }
        item.imports = item.imports.map((f) => renames.get(f) ?? f);
        item.dynamicImports = item.dynamicImports.map((f) => renames.get(f) ?? f);
        if (item.map) item.sourcemapFileName = newName + ".map";
        item.fileName = item.preliminaryFileName = newName;
      }
      out[newName] = item;
    }
    for (const e of emittedEntries.values()) if (renames.has(e.emitted.fileName)) e.emitted.fileName = renames.get(e.emitted.fileName);
    return out;
  }

  addAssets(bundle) {
    for (const f of this.emitted.values()) {
      if (f.type !== "asset") continue;
      const fileName = (f.fileName ??= this.assetFileName(f));
      if (bundle[fileName]) continue;
      bundle[fileName] = outputAsset(f, fileName);
    }
  }
}

// sourceMapOf is a Rollup SourceMap for esbuild's map text. It maps the
// chunk to the modules as loaded (after plugin transforms, whose own maps
// are not chained in).
function sourceMapOf(text, dir) {
  if (!text) return null;
  const m = JSON.parse(text);
  // Module ids in og's namespace become paths relative to the map, as
  // esbuild writes file sources (no absolute paths in published maps).
  // Absolute paths (from plugins' input maps) likewise.
  const sources = (m.sources || []).map((s) => {
    const id = s.startsWith(this_ns()) ? s.slice(this_ns().length) : s;
    return path.isAbsolute(id) ? path.relative(dir, id).split(path.sep).join("/") : id;
  });
  // One entry per source, as Rolldown writes maps (Rollup rejects one
  // source with two contents: esbuild lists a Vue SFC once per block
  // module). Duplicates take the first index; the content kept is the
  // file's own when it is on disk.
  let contents = m.sourcesContent;
  let mappings = m.mappings || "";
  const first = new Map();
  const index = sources.map((s, i) => (first.has(s) ? first.get(s) : (first.set(s, i), i)));
  if (index.some((j, i) => j !== i)) {
    const kept = [...new Set(index)];
    const renumber = new Map(kept.map((j, k) => [j, k]));
    const decoded = decodeMappings(mappings);
    for (const line of decoded) for (const seg of line) if (seg.length > 1) seg[1] = renumber.get(index[seg[1]]);
    mappings = encodeMappings(decoded);
    contents = contents && kept.map((j) => {
      const file = path.resolve(dir, sources[j]);
      try {
        if (fs.existsSync(file)) return fs.readFileSync(file, "utf8");
      } catch {}
      return contents[j];
    });
    sources.splice(0, sources.length, ...kept.map((j) => sources[j]));
  }
  const map = { version: 3, file: m.file, sources, sourcesContent: contents, names: m.names || [], mappings };
  Object.defineProperties(map, {
    toString: { value: () => JSON.stringify(map) },
    toUrl: { value: () => "data:application/json;charset=utf-8;base64," + Buffer.from(JSON.stringify(map)).toString("base64") },
  });
  return map;
}

// pruneExternalImports drops the unused bindings of a chunk's imports of
// external modules. esbuild prints an external import as the module wrote
// it; Rolldown (as Rollup) keeps only the bindings the chunk uses, and a
// bare import when none is (externals are assumed to have side effects).
// A Rollup build reading the chunk again (Nitro's) warns about each unused
// one. The chunk's source map follows the edits.
function pruneExternalImports(chunk) {
  const externals = chunk.ogExternals;
  if (!externals?.length || !/\bimport\b/.test(chunk.code)) return;
  let program;
  try {
    program = acorn.parse(chunk.code, { ecmaVersion: "latest", sourceType: "module", allowHashBang: true, allowAwaitOutsideFunction: true });
  } catch {
    return;
  }
  const decls = program.body.filter((n) => n.type === "ImportDeclaration" && n.specifiers.length && externals.includes(n.source.value));
  if (!decls.length) return;
  // Every identifier outside the import declarations counts as a use
  // (property keys and shadowing locals too: they only keep a binding).
  const used = new Set();
  const stack = program.body.filter((n) => n.type !== "ImportDeclaration");
  while (stack.length) {
    const node = stack.pop();
    if (!node || typeof node !== "object") continue;
    if (Array.isArray(node)) {
      stack.push(...node);
      continue;
    }
    if (node.type === "Identifier") {
      used.add(node.name);
      continue;
    }
    for (const k in node) if (k !== "type" && k !== "start" && k !== "end") stack.push(node[k]);
  }
  const edits = [];
  for (const d of decls) {
    const keep = d.specifiers.filter((sp) => used.has(sp.local.name));
    if (keep.length === d.specifiers.length) continue;
    const head = chunk.code.slice(d.start, d.source.start);
    const pad = /\{\s/.test(head) ? " " : "";
    const name = (n) => (n.type === "Literal" ? n.raw : n.name);
    const def = keep.filter((sp) => sp.type === "ImportDefaultSpecifier").map((sp) => sp.local.name);
    const ns = keep.filter((sp) => sp.type === "ImportNamespaceSpecifier").map((sp) => "* as " + sp.local.name);
    const named = keep.filter((sp) => sp.type === "ImportSpecifier").map((sp) => (name(sp.imported) === sp.local.name ? sp.local.name : name(sp.imported) + " as " + sp.local.name));
    const clause = [...def, ...ns, ...(named.length ? ["{" + pad + named.join("," + (pad || "")) + pad + "}"] : [])].join("," + pad);
    edits.push({ start: d.start, end: d.source.start, text: clause ? "import " + clause + (pad ? " from " : "from") : "import " });
  }
  if (!edits.length) return;
  let code = chunk.code;
  const decoded = chunk.map ? decodeMappings(chunk.map.mappings) : null;
  const lineStarts = [0];
  for (let i = code.indexOf("\n"); i >= 0; i = code.indexOf("\n", i + 1)) lineStarts.push(i + 1);
  const pos = (offset) => {
    let lo = 0, hi = lineStarts.length - 1;
    while (lo < hi) {
      const mid = (lo + hi + 1) >> 1;
      if (lineStarts[mid] <= offset) lo = mid;
      else hi = mid - 1;
    }
    return [lo, offset - lineStarts[lo]];
  };
  // From the end, so earlier offsets stay valid.
  for (const e of edits.sort((a, b) => b.start - a.start)) {
    if (decoded) {
      const [sl, sc] = pos(e.start), [el, ec] = pos(e.end);
      const before = (decoded[sl] || []).filter((seg) => seg[0] <= sc);
      const after = (decoded[el] || []).filter((seg) => seg[0] >= ec).map((seg) => [sc + e.text.length + seg[0] - ec, ...seg.slice(1)]);
      decoded.splice(sl, el - sl + 1, [...before, ...after]);
    }
    code = code.slice(0, e.start) + e.text + code.slice(e.end);
  }
  chunk.code = code;
  if (decoded) chunk.map.mappings = encodeMappings(decoded);
}

// outputAsset is a Rollup OutputAsset for an emitted asset file.
function outputAsset(f, fileName) {
  const name = f.name || path.basename(fileName);
  const asset = { type: "asset", fileName, name, names: f.names || [name], originalFileName: f.originalFileName ?? null, originalFileNames: f.originalFileNames || (f.originalFileName ? [f.originalFileName] : []), source: f.source, needsCodeReference: !!f.needsCodeReference };
  Object.defineProperty(asset, "viteMetadata", { value: { importedCss: new Set(), importedAssets: new Set() }, enumerable: true, configurable: true, writable: true });
  return asset;
}

const this_ns = () => "og-vite:";

export async function rolldown(inputOptions) {
  let options = { ...inputOptions };
  const initial = new Build(options);
  for (const p of sorted(initial.plugins, "options")) {
    const r = await initial.call(p, "options", [options]);
    if (r) options = r;
  }
  const b = new Build(options);
  await b.parallel("buildStart", [options]);
  let closed = false;

  async function generate(outputOptions = {}, write) {
    let output = { ...outputOptions };
    for (const p of sorted(b.plugins, "outputOptions")) {
      const r = await b.call(p, "outputOptions", [output]);
      if (r) output = r;
    }
    output.dir = path.resolve(b.root, output.dir || (output.file ? path.dirname(output.file) : "dist"));
    // Output hooks see the normalized format, as Rollup's do ("esm" and
    // "module" are "es"; vite:css-post emits code-split CSS only for es/cjs).
    output.format = { esm: "es", module: "es", commonjs: "cjs", systemjs: "system" }[output.format] || output.format || "es";
    // Chunks emitted while building (Rollup builds them with the rest)
    // get passes of their own.
    const passes = [];
    try {
      passes.push(await b.bundle(output));
      for (let i = 0; i < 10; i++) {
        const known = new Set(passes.flatMap((p) => p.resolved.filter((e) => e.emitted).map((e) => e.emitted)));
        const late = [...b.emitted.values()].filter((f) => f.type === "chunk" && !known.has(f));
        if (!late.length) break;
        passes.push(await b.bundle(output, late));
      }
    } catch (e) {
      await b.parallel("buildEnd", [e]);
      throw e;
    }
    await b.parallel("buildEnd", []);
    await b.parallel("renderStart", [output, options]);
    const bundle = Object.assign({}, ...passes.map((built) => b.toBundle(built, output)));
    for (const chunk of Object.values(bundle)) {
      chunk.code = await b.resolveFileUrls(chunk, output);
      for (const p of sorted(b.plugins, "renderChunk")) {
        const r = await b.call(p, "renderChunk", [chunk.code, chunk, output, { chunks: bundle }]);
        if (r == null) continue;
        chunk.code = typeof r === "string" ? r : r.code;
      }
    }
    b.addAssets(bundle);
    await b.parallel("generateBundle", [output, bundle, !!write]);
    b.addAssets(bundle);
    // Source maps as Rollup writes them: a .map file with a
    // sourceMappingURL comment (true), without it ("hidden"), or inline.
    for (const item of Object.values(bundle)) {
      if (item.type !== "chunk" || !item.map || !output.sourcemap) continue;
      if (output.sourcemap === "inline") {
        item.code += "\n//# sourceMappingURL=" + item.map.toUrl();
        continue;
      }
      const mapName = item.sourcemapFileName || item.fileName + ".map";
      if (output.sourcemap !== "hidden") item.code += "\n//# sourceMappingURL=" + path.posix.basename(mapName);
      if (!bundle[mapName]) bundle[mapName] = outputAsset({ source: item.map.toString(), name: path.posix.basename(mapName) }, mapName);
    }
    if (write) {
      for (const item of Object.values(bundle)) {
        const file = path.join(output.dir, item.fileName);
        fs.mkdirSync(path.dirname(file), { recursive: true });
        fs.writeFileSync(file, item.type === "chunk" ? item.code : item.source);
      }
      await b.parallel("writeBundle", [output, bundle]);
      // What Oganesson packages: each written output's entries.
      (globalThis[Symbol.for("og.viteBuilds")] ??= []).push({
        dir: output.dir,
        platform: options.platform || "browser",
        entries: Object.values(bundle).filter((c) => c.type === "chunk" && c.isEntry).map((c) => c.fileName),
      });
    }
    const items = Object.values(bundle);
    const order = (x) => (x.type === "chunk" ? (x.isEntry ? 0 : 1) : 2);
    return { output: items.sort((a, c) => order(a) - order(c)) };
  }

  return {
    generate: (o) => generate(o, false),
    write: (o) => generate(o, true),
    async close() {
      if (closed) return;
      closed = true;
      await b.parallel("closeBundle", []);
    },
    get closed() {
      return closed;
    },
    watchFiles: [],
  };
}
