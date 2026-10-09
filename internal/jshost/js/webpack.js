// Oganesson's webpack loader shim: runs webpack-style loaders (babel-loader, …)
// inside Oganesson's esbuild builds. Configured in og.config.*:
//
//   webpack: { rules: [{ test: /\.[jt]sx$/, exclude: /node_modules/,
//                        use: [{ loader: "babel-loader", options: {...} }] }] }
//
// It implements the subset of webpack 5's loader API that real loaders use;
// loaders named by string are loaded with Oganesson's build-time require().

const pathMod = () => globalThis.require("node:path");

function matches(cond, path) {
  if (cond === undefined) return true;
  if (cond instanceof RegExp) return cond.test(path);
  if (typeof cond === "string") return path.startsWith(cond);
  if (typeof cond === "function") return !!cond(path);
  if (Array.isArray(cond)) return cond.some((c) => matches(c, path));
  if (cond && typeof cond === "object") {
    if (cond.and && !cond.and.every((c) => matches(c, path))) return false;
    if (cond.or && !cond.or.some((c) => matches(c, path))) return false;
    if (cond.not && matches(cond.not, path)) return false;
    return true;
  }
  return false;
}

function ruleApplies(rule, path, query = "") {
  if (rule.test !== undefined && !matches(rule.test, path)) return false;
  if (rule.include !== undefined && !matches(rule.include, path)) return false;
  if (rule.exclude !== undefined && matches(rule.exclude, path)) return false;
  if (rule.resource !== undefined && !matches(rule.resource, path)) return false;
  if (rule.resourceQuery !== undefined && !matches(rule.resourceQuery, query)) return false;
  return true;
}

// rulesUses lists the loaders the rules apply to a module, in webpack's
// order: every matching rule's (rule.use, or rule.loader with
// rule.options), a matching rule's nested rules, and the first match of
// its oneOf.
export function rulesUses(rules, path, query = "") {
  const uses = [];
  for (const rule of rules || []) {
    if (!rule || typeof rule !== "object" || !ruleApplies(rule, path, query)) continue;
    if (rule.use !== undefined) uses.push(...normalizeUse(rule.use));
    else if (rule.loader !== undefined) uses.push(...normalizeUse(rule.options !== undefined ? { loader: rule.loader, options: rule.options } : rule.loader));
    if (Array.isArray(rule.rules)) uses.push(...rulesUses(rule.rules, path, query));
    if (Array.isArray(rule.oneOf)) {
      for (const one of rule.oneOf) {
        const u = rulesUses([one], path, query);
        if (u.length > 0 || (one && ruleApplies(one, path, query))) {
          uses.push(...u);
          break;
        }
      }
    }
  }
  return uses;
}

function normalizeUse(use) {
  const list = Array.isArray(use) ? use : [use];
  return list.filter(Boolean).map((u) => (typeof u === "string" || typeof u === "function" ? { loader: u } : u));
}

function loadLoader(spec) {
  let mod = spec.loader;
  if (typeof mod === "string") {
    const [name] = mod.split("?");
    mod = globalThis.require(name);
  }
  const fn = typeof mod === "function" ? mod : mod && mod.default;
  if (typeof fn !== "function") throw new Error(`og: webpack loader ${String(spec.loader)} is not a function`);
  return { fn, raw: !!(mod.raw || fn.raw), options: spec.options };
}

const outputLoader = { ".ts": "ts", ".mts": "ts", ".cts": "ts", ".tsx": "tsx", ".jsx": "jsx", ".js": "jsx", ".mjs": "jsx", ".cjs": "jsx" };

// runLoader runs one loader function with a webpack-compatible context.
function runLoader(loader, ctx, content, map, meta) {
  return new Promise((resolve, reject) => {
    let settled = false;
    let isAsync = false;
    const callback = (err, out, outMap, outMeta) => {
      if (settled) return;
      settled = true;
      if (err) reject(err);
      else resolve([out, outMap, outMeta]);
    };
    const context = Object.assign(Object.create(ctx), {
      query: loader.options ?? {},
      getOptions: () => loader.options ?? {},
      async() { isAsync = true; return callback; },
      callback(...args) { isAsync = true; callback(...args); },
    });
    let input = content;
    if (loader.raw && typeof input === "string") input = Buffer.from(input);
    if (!loader.raw && typeof input !== "string") input = Buffer.from(input).toString("utf8");
    try {
      const r = loader.fn.call(context, input, map, meta);
      if (isAsync) return;
      if (r && typeof r.then === "function") r.then((v) => callback(null, v), callback);
      else callback(null, r);
    } catch (e) {
      callback(e);
    }
  });
}

// fsResolver resolves like webpack's enhanced-resolve for the options
// loaders pass to getResolve (extensions, mainFiles, mainFields,
// restrictions), without an esbuild build: relative and absolute requests
// against the file system, bare ones through node_modules.
export function fsResolver(options = {}) {
  const fs = globalThis.require("node:fs");
  const path = pathMod();
  const exts = options.extensions || [".js", ".json"];
  const mainFiles = (options.mainFiles || ["index"]).filter((f) => f !== "...");
  const mainFields = (options.mainFields || ["main"]).filter((f) => f !== "...");
  const isFile = (p) => { try { return fs.statSync(p).isFile(); } catch { return false; } };
  const isDir = (p) => { try { return fs.statSync(p).isDirectory(); } catch { return false; } };
  const asFile = (p) => {
    if (isFile(p)) return p;
    for (const e of exts) if (isFile(p + e)) return p + e;
    return null;
  };
  const asDir = (p) => {
    if (!isDir(p)) return null;
    const pkgFile = path.join(p, "package.json");
    if (isFile(pkgFile)) {
      try {
        const pkg = JSON.parse(fs.readFileSync(pkgFile, "utf8"));
        for (const f of [...mainFields, "main"]) {
          if (typeof pkg[f] === "string") {
            const r = asFile(path.join(p, pkg[f])) || asIndex(path.join(p, pkg[f]));
            if (r) return r;
          }
        }
      } catch {}
    }
    return asIndex(p);
  };
  const asIndex = (p) => {
    for (const m of mainFiles) {
      const r = asFile(path.join(p, m));
      if (r) return r;
    }
    return null;
  };
  const allowed = (p) => !options.restrictions || options.restrictions.every((r) => (r instanceof RegExp ? r.test(p) : p.startsWith(r)));
  return (context, request) => {
    let found = null;
    if (path.isAbsolute(request) || request.startsWith("./") || request.startsWith("../") || request === "." || request === "..") {
      const p = path.resolve(context, request);
      found = asFile(p) || asDir(p);
    } else {
      // preferRelative: a bare request is tried as a relative one first.
      if (options.preferRelative) {
        const p = path.resolve(context, request);
        found = asFile(p) || asDir(p);
      }
      for (let dir = context; !found; dir = path.dirname(dir)) {
        const p = path.join(dir, "node_modules", request);
        found = asFile(p) || asDir(p);
        if (path.dirname(dir) === dir) break;
      }
    }
    if (found && allowed(found)) return Promise.resolve(found);
    return Promise.reject(new Error(`Can't resolve '${request}' in '${context}'`));
  };
}

// runLoaderChain runs webpack loaders (right to left) over one resource and
// returns [content, map, meta, dependencies]. resolverFor(options) returns
// (context, request) => Promise<path>.
// traceSpan stands in for Next's trace spans: children are itself, traced
// functions just run.
const traceSpan = {
  traceChild: () => traceSpan,
  traceFn: (fn) => fn(traceSpan),
  traceAsyncFn: (fn) => fn(traceSpan),
  setAttribute() {},
  stop() {},
  manualTraceChild() {},
};

export async function runLoaderChain({ uses, resourcePath, rootContext, content, target, sourceMap, resolverFor }) {
  const fs = globalThis.require("node:fs");
  const path = pathMod();
  const deps = [];
  const ctx = {
    version: 2,
    webpack: true,
    mode: "production",
    target,
    sourceMap: !!sourceMap,
    hot: false,
    resource: resourcePath,
    resourcePath,
    resourceQuery: "",
    resourceFragment: "",
    context: path.dirname(resourcePath),
    rootContext,
    data: {},
    fs,
    // The module being built, as far as loaders reach into it: its
    // buildInfo (nextra stores its search data there).
    _module: { resource: resourcePath, buildInfo: {}, buildMeta: {} },
    // Next's tracing span (its Babel loader times itself with it).
    currentTraceSpan: traceSpan,
    cacheable() {},
    addDependency(f) { deps.push(f); },
    dependency(f) { deps.push(f); },
    addContextDependency() {},
    addMissingDependency() {},
    addBuildDependency() {},
    getDependencies: () => deps.slice(),
    clearDependencies() { deps.length = 0; },
    emitWarning(w) { console.warn(`[webpack loader] ${resourcePath}:`, w && w.message ? w.message : w); },
    emitError(e) { console.error(`[webpack loader] ${resourcePath}:`, e && e.message ? e.message : e); },
    emitFile() {},
    // webpack's default infrastructure logging level ("info") hides
    // debug and trace output.
    getLogger: () => ({
      error: console.error, warn: console.warn, info: console.info, log: console.log,
      debug() {}, trace() {}, status() {}, group() {}, groupEnd() {}, groupCollapsed() {},
      profile() {}, profileEnd() {}, time() {}, timeEnd() {}, timeLog() {}, clear() {},
    }),
    resolve(context, request, cb) {
      resolverFor({})(context, request).then((p) => cb(null, p), cb);
    },
    getResolve(options) {
      const r = resolverFor(options || {});
      return (context, request, cb) => {
        const p = r(context, request);
        if (cb) p.then((v) => cb(null, v), cb);
        else return p;
      };
    },
    utils: {
      absolutify: (context, request) => path.resolve(context, request),
      contextify: (context, request) => path.relative(context, request),
      // webpack's defaults (md4/xxhash64) are unavailable: use md5.
      createHash: (alg) => globalThis.require("node:crypto").createHash(!alg || alg === "md4" || alg === "xxhash64" ? "md5" : alg),
    },
  };
  const loaders = uses.map(loadLoader);
  ctx.loaders = loaders.map((l, i) => ({ request: String(uses[i].loader), options: l.options }));
  let map, meta;
  for (let i = loaders.length - 1; i >= 0; i--) {
    ctx.loaderIndex = i;
    [content, map, meta] = await runLoader(loaders[i], ctx, content, map, meta);
  }
  if (typeof content !== "string") content = Buffer.from(content).toString("utf8");
  return [content, map, meta, deps];
}

// webpackLoaderPlugin runs the rules' loaders on the modules they match.
// filter (a RegExp) limits the files offered (default: JavaScript and
// TypeScript files).
// buildResolver resolves a loader's requests through the build's
// resolvers as webpack's resolver does: to files (or externals) only, since
// og's stand-ins for packages that are not installed live in other
// namespaces and a loader would emit their names (@markdoc/next.js imports
// its schema files only when they resolve); with preferRelative, a bare
// request as a relative one first (markdoc's resolve(schemaDir, "tags")).
function buildResolver(build, options = {}) {
  const once = (request, context) =>
    build.resolve(request, { resolveDir: context, kind: "require-call" }).then((r) => {
      if (r.errors.length) throw new Error(r.errors[0].text);
      if (r.namespace !== "file" && !r.external) throw new Error(`Can't resolve '${request}' in '${context}'`);
      return r.path;
    });
  return (context, request) => {
    const bare = !/^(?:\.{1,2}(?:\/|$)|\/)/.test(request);
    if (options.preferRelative && bare) return once("./" + request, context).catch(() => once(request, context));
    return once(request, context);
  };
}

export function webpackLoaderPlugin(config, target, filter) {
  const rules = (config && config.webpack && config.webpack.rules) || [];
  return {
    name: "og-webpack-loaders",
    setup(build) {
      const root = build.initialOptions.absWorkingDir;
      build.onLoad({ filter: filter || /\.[cm]?[jt]sx?$/, namespace: "file" }, async (args) => {
        const uses = rulesUses(rules, args.path, args.suffix || "");
        if (uses.length === 0) return undefined;
        const path = pathMod();
        const [content, , , deps] = await runLoaderChain({
          uses,
          resourcePath: args.path,
          rootContext: root,
          content: globalThis.require("node:fs").readFileSync(args.path, "utf8"),
          target: build.initialOptions.og && build.initialOptions.og.target === "client" ? "web" : "node",
          sourceMap: false,
          resolverFor: (options) => buildResolver(build, options),
        });
        return {
          contents: content,
          loader: outputLoader[path.extname(args.path)] || "jsx",
          resolveDir: path.dirname(args.path),
          watchFiles: deps,
        };
      });
    },
  };
}

// turbopackConfig translates next.config's turbopack section into the
// webpack hook's terms (null when it has neither rules nor resolveAlias):
//
//   - rules: { glob: { loaders, as?, condition? } | [such objects] |
//     [loaders] }. A glob without "/" matches the file name, one with "/"
//     the path relative to the project. Rules whose output is JavaScript
//     (no "as", or as: "*.js" / "*.ts" / ...) become webpack rules
//     (jsRules); those yielding CSS (as: "*.css") run in the stylesheet
//     build instead (cssRules). Each carries ogExt, the extension its glob
//     ends with, so the build offers such files to it.
//   - conditions: "browser", "node" / "edge-light", "foreign" (a module
//     in node_modules), "production" / "development", { path: glob or
//     RegExp }, { content: RegExp }, { all }, { any }, { not }.
//   - resolveAlias: name -> target(s), or { browser, default } per target.
export function turbopackConfig(turbo, dir, isServer) {
  if (!turbo || typeof turbo !== "object") return null;
  const path = pathMod();
  const ruleMap = turbo.rules && typeof turbo.rules === "object" ? turbo.rules : {};
  const aliasIn = turbo.resolveAlias && typeof turbo.resolveAlias === "object" ? turbo.resolveAlias : {};
  if (Object.keys(ruleMap).length === 0 && Object.keys(aliasIn).length === 0) return null;
  const globMatcher = (glob) => {
    const re = globRegExp(glob);
    return glob.includes("/") ? (p) => re.test(path.relative(dir, p).split(path.sep).join("/")) : (p) => re.test(path.basename(p));
  };
  const cond = (c) => {
    if (c === undefined || c === null) return () => true;
    if (typeof c === "string") {
      switch (c) {
        case "browser": return () => !isServer;
        case "node": case "edge-light": return () => isServer;
        case "foreign": return (p) => /[\\/]node_modules[\\/]/.test(p);
        case "production": return () => true;
        case "development": return () => false;
        default: return () => false;
      }
    }
    if (Array.isArray(c)) {
      const all = c.map(cond);
      return (p) => all.every((f) => f(p));
    }
    if (typeof c === "object") {
      const parts = [];
      if (c.all) { const fs = c.all.map(cond); parts.push((p) => fs.every((f) => f(p))); }
      if (c.any) { const fs = c.any.map(cond); parts.push((p) => fs.some((f) => f(p))); }
      if (c.not !== undefined) { const f = cond(c.not); parts.push((p) => !f(p)); }
      if (c.path !== undefined) {
        const m = c.path instanceof RegExp ? (p) => c.path.test(p) : globMatcher(String(c.path));
        parts.push(m);
      }
      if (c.content instanceof RegExp) {
        const fs = globalThis.require("node:fs");
        parts.push((p) => { try { return c.content.test(fs.readFileSync(p, "utf8")); } catch { return false; } });
      }
      return (p) => parts.every((f) => f(p));
    }
    return () => false;
  };
  const jsRules = [];
  const cssRules = [];
  for (const [glob, value] of Object.entries(ruleMap)) {
    const isLoaderList = Array.isArray(value) && value.every((v) => typeof v === "string" || (v && typeof v === "object" && "loader" in v));
    const items = isLoaderList ? [{ loaders: value }] : Array.isArray(value) ? value : [value];
    const m = globMatcher(glob);
    const ext = /\.([A-Za-z0-9_]+)\}?$/.exec(glob);
    for (const item of items) {
      if (!item || typeof item !== "object" || !Array.isArray(item.loaders)) continue;
      const c = cond(item.condition);
      const rule = { test: (p) => m(p) && c(p), use: item.loaders, ogExt: ext ? ext[1].toLowerCase() : undefined };
      const as = typeof item.as === "string" ? path.extname(item.as.replace(/\*/g, "x")) : "";
      (as === ".css" ? cssRules : jsRules).push(rule);
    }
  }
  const aliases = {};
  // Turbopack's own module for @next/mdx's import source: the project's
  // mdx-components, else @mdx-js/react, else @next/mdx's default (the
  // candidates @next/mdx's webpack alias lists).
  const internal = {
    "@vercel/turbopack-next/mdx-import-source": [path.join(dir, "src/mdx-components"), path.join(dir, "mdx-components"), "@mdx-js/react", "@next/mdx/mdx-components.js"],
  };
  for (const [name, v] of Object.entries(aliasIn)) {
    if (typeof v === "string" && internal[v]) {
      aliases[name] = internal[v];
      continue;
    }
    if (typeof v === "string" || Array.isArray(v)) aliases[name] = v;
    else if (v && typeof v === "object") {
      const t = !isServer && v.browser !== undefined ? v.browser : v.default;
      if (t !== undefined) aliases[name] = t;
    }
  }
  return { jsRules, cssRules, aliases };
}

// globRegExp compiles a Turbopack rule glob: * (within a segment), **
// (across segments), ?, and {a,b} alternatives.
function globRegExp(glob) {
  let re = "";
  for (let i = 0; i < glob.length; i++) {
    const c = glob[i];
    if (c === "*" && glob[i + 1] === "*") {
      re += ".*";
      i++;
      if (glob[i + 1] === "/") i++;
    } else if (c === "*") re += "[^/]*";
    else if (c === "?") re += "[^/]";
    else if (c === "{") {
      const end = glob.indexOf("}", i);
      if (end < 0) { re += "\\{"; continue; }
      re += "(?:" + glob.slice(i + 1, end).split(",").map((a) => a.replace(/[.+^$()|[\]\\]/g, "\\$&").replace(/\*/g, "[^/]*")).join("|") + ")";
      i = end;
    } else re += c.replace(/[.+^$()|[\]\\]/g, "\\$&");
  }
  return new RegExp("^" + re + "$");
}
