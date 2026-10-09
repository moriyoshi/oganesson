// The JS half of Oganesson's esbuild plugin bridge. It runs inside the build-time
// plugin host (one Goja engine per build) and adapts esbuild JS-API plugins
// to the Go API that actually performs the builds. Go calls the methods of
// the object returned by createBridge; JS reaches Go via globalThis[HOST].
import { webpackLoaderPlugin, runLoaderChain, fsResolver, rulesUses, turbopackConfig } from "./webpack.js";

const HOST = Symbol.for("og.pluginHost");

export function createBridge(config, services = {}) {
  const host = globalThis[HOST];
  const callbacks = new Map(); // id -> { type, fn }
  const pluginData = new Map(); // token -> any
  let nextId = 1;

  const token = (v) => {
    if (v === undefined) return 0;
    const t = nextId++;
    pluginData.set(t, v);
    return t;
  };
  const untoken = (t) => (t ? pluginData.get(t) : undefined);

  // webpack.rules become one more esbuild plugin for the JS builds.
  const loaderShim = config && config.webpack && config.webpack.rules ? [webpackLoaderPlugin(config)] : [];

  // next.config's webpack(config, options) hook, run per target against a
  // stand-in webpack config (webpackHook): the rules it adds, kept here
  // (functions and RegExps), run by one more plugin per target.
  const hooks = {}; // target -> { rules, filter }
  const hookPlugins = (target) => {
    const h = hooks[target];
    return h && h.rules.length > 0 ? [webpackLoaderPlugin({ webpack: { rules: h.rules } }, target, h.filter)] : [];
  };

  function lists() {
    const esb = (config && config.esbuild) || {};
    return {
      all: [...loaderShim, ...(esb.plugins || [])],
      client: esb.clientPlugins || [],
      server: esb.serverPlugins || [],
      css: esb.cssPlugins || [],
      "webpack-client": hookPlugins("client"),
      "webpack-server": hookPlugins("server"),
    };
  }

  function messages(list) {
    return (list || []).map((m) => ({ text: String(m.text ?? m), detail: m.detail === undefined ? "" : String(m.detail) }));
  }

  let liveNextConfig; // Promise of the evaluated next.config, on first use

  // Next.js's own PostCSS setup, loaded on first use.
  let postcssSetup;
  function nextPostCSS() {
    const o = services.postcss;
    if (!o) throw new Error("og: PostCSS is not enabled for this build");
    if (!postcssSetup) {
      // Resolve Next from the project, as its webpack config does.
      const req = globalThis[Symbol.for("og.createRequire")](o.NextDir + "/package.json");
      const { lazyPostCSS } = req("next/dist/build/webpack/config/blocks/css");
      const { getSupportedBrowsers } = req("next/dist/build/get-supported-browsers");
      postcssSetup = lazyPostCSS(o.Root, getSupportedBrowsers(o.Root, false), o.disablePresetEnv, o.useLightningcss);
    }
    return postcssSetup;
  }

  return {
    config,

    // postcss(from, css) -> { css, warnings }
    async postcss(from, css) {
      const { postcssWithPlugins } = await nextPostCSS();
      const r = await postcssWithPlugins.process(css, { from, to: from, map: false });
      return { css: r.css, warnings: r.warnings().map((w) => w.toString()) };
    },

    // lightningcss(from, css) -> css
    //
    // experimental.useLightningcss: what Next's lightningcss-loader does to
    // a stylesheet's code (its css-loader duties, @import/url()/modules,
    // stay with esbuild). Next's @next/swc binding is native; its
    // lightningcss transform is lightningcss-wasm's, with the loader's
    // targets (getTargets over the project's browserslist) and feature masks.
    async lightningcss(from, css) {
      const o = services.postcss;
      if (!o) throw new Error("og: the Next.js services are not enabled for this build");
      const { postcssWithPlugins } = await nextPostCSS();
      if (postcssWithPlugins && postcssWithPlugins.plugins && postcssWithPlugins.plugins.length > 0) {
        throw new Error("[lightningcss-loader]: experimental.useLightningcss does not work with postcss plugins. Please remove 'useLightningcss: true' from your configuration.");
      }
      const req = globalThis[Symbol.for("og.createRequire")](o.NextDir + "/package.json");
      let lightning;
      try {
        lightning = globalThis[Symbol.for("og.createRequire")](o.Root + "/package.json")("lightningcss-wasm");
      } catch {
        throw new Error("experimental.useLightningcss needs the lightningcss-wasm package (it stands in for the lightningcss in Next's native @next/swc binding): add lightningcss-wasm to devDependencies and run og install");
      }
      const { getTargets } = req("next/dist/build/webpack/loaders/lightningcss-loader/src/utils");
      const { ECacheKey } = req("next/dist/build/webpack/loaders/lightningcss-loader/src/interface");
      const { getSupportedBrowsers } = req("next/dist/build/get-supported-browsers");
      // @next/swc's featureNamesToMask: kebab-case names to Features bits.
      const featureNamesToMask = (names) => names.reduce((mask, n) => {
        const key = n.split("-").map((w) => w.charAt(0).toUpperCase() + w.slice(1)).join("");
        if (!(key in lightning.Features)) throw new Error(`Unknown lightningcss feature: ${n}`);
        return mask | lightning.Features[key];
      }, 0);
      const features = services.lightningCssFeatures || {};
      const userInclude = features.include ? featureNamesToMask(features.include) : 0;
      const userExclude = features.exclude ? featureNamesToMask(features.exclude) : 0;
      const includeMask = (lightning.Features.Nesting | userInclude) & ~userExclude;
      const drafts = (includeMask & featureNamesToMask(["custom-media-queries"])) !== 0 ? { customMedia: true } : undefined;
      const { code } = lightning.transform({
        filename: from,
        code: new TextEncoder().encode(css),
        targets: getTargets({ targets: getSupportedBrowsers(o.Root, false), key: ECacheKey.loader }),
        include: includeMask,
        exclude: userExclude,
        drafts,
      });
      return new TextDecoder().decode(code);
    },

    // sass(path) -> css
    //
    // Next's Sass preprocessors (webpack-config's sassPreprocessors): its
    // compiled sass-loader with the project's sassOptions, then its
    // resolve-url-loader, which rebases url()s from partials onto the entry
    // file. PostCSS runs afterwards, as postcss-loader does.
    async sass(path) {
      const o = services.postcss;
      if (!o) throw new Error("og: the Next.js services are not enabled for this build");
      const req = globalThis[Symbol.for("og.createRequire")](o.NextDir + "/package.json");
      // The live next.config when available (functions intact), else its
      // JSON-safe sassOptions.
      if (services.nextConfig && !liveNextConfig) liveNextConfig = services.nextConfig();
      const live = liveNextConfig ? (await liveNextConfig).sassOptions : undefined;
      const { prependData, additionalData, implementation, ...sassOptions } = live || services.sassOptions || {};
      // Next appends sassPreprocessors reversed; webpack runs loaders right
      // to left, so sass-loader runs first.
      const uses = [
        {
          loader: req.resolve("next/dist/build/webpack/loaders/resolve-url-loader/index"),
          options: { postcss: () => nextPostCSS(), sourceMap: true },
        },
        {
          loader: req.resolve("next/dist/compiled/sass-loader"),
          options: { implementation, sourceMap: true, sassOptions, additionalData: prependData || additionalData },
        },
      ];
      const fs = req("fs");
      const [css] = await runLoaderChain({
        uses,
        resourcePath: path,
        rootContext: o.Root,
        content: fs.readFileSync(path),
        target: "web",
        sourceMap: true,
        resolverFor: fsResolver,
      });
      return css;
    },

    // nextFont(query, assetPrefix) -> { css, exports, files }
    //
    // What next-font-loader followed by css-loader produce for one
    // `<loader>/target.css?<query>` module: the @next/font loader's
    // @font-face CSS, run through postcss-next-font, with the .className and
    // .variable rules scoped as css-loader's getLocalIdent does
    // (`__<name>_<fontFamilyHash>`). files are the emitted font files.
    async nextFont(q, assetPrefix) {
      const o = services.postcss;
      if (!o) throw new Error("og: the Next.js services are not enabled for this build");
      const req = globalThis[Symbol.for("og.createRequire")](o.NextDir + "/package.json");
      const path = req("path");
      const crypto = req("crypto");
      const fs = req("fs");
      const loaderPath = q.loader.endsWith("/local") ? "next/dist/compiled/@next/font/local/loader" : "next/dist/compiled/@next/font/google/loader";
      const fontLoader = req(loaderPath).default;
      const files = [];
      const emitFontFile = (content, ext, preload, isUsingSizeAdjust) => {
        const buf = Buffer.from(content);
        const hash = crypto.createHash("sha256").update(buf).digest("hex").slice(0, 20);
        const name = `static/media/${hash}${isUsingSizeAdjust ? "-s" : ""}${preload ? ".p" : ""}.${ext}`;
        files.push({ name, data: buf.toString("base64") });
        return `${assetPrefix}/_next/${name}`;
      };
      const fromFile = path.join(o.Root, q.path);
      const fileRequire = globalThis[Symbol.for("og.createRequire")](fromFile);
      let { css, fallbackFonts, adjustFontFallback, weight, style, variable } = await fontLoader({
        functionName: q.import,
        variableName: q.variableName,
        data: q.arguments,
        emitFontFile,
        resolve: async (src) => fileRequire.resolve(src.startsWith(".") ? src : `./${src}`),
        isDev: false,
        isServer: false,
        loaderContext: { fs, rootContext: o.Root, resourcePath: fromFile },
      });
      const { postcss } = await nextPostCSS();
      const exports = [];
      const fontFamilyHash = crypto.createHash("sha1").update(Buffer.from(css)).digest("hex").slice(0, 6);
      const plugin = req("next/dist/build/webpack/loaders/next-font-loader/postcss-next-font").default;
      const result = await postcss(plugin({ exports, fallbackFonts, weight, style, adjustFontFallback, variable })).process(css, { from: undefined });
      const locals = [];
      result.root.walkRules((rule) => {
        rule.selector = rule.selector.replace(/\.(className|variable)\b/g, (_, name) => {
          if (!locals.includes(name)) locals.push(name);
          return `.__${name}_${fontFamilyHash}`;
        });
      });
      for (const name of locals) exports.push({ name, value: `__${name}_${fontFamilyHash}` });
      return { css: result.root.toString(), exports, files };
    },

    // webpackHook(target, dir, buildId) -> { aliases, fallbacks, defines, rules } | null
    //
    // Runs next.config's webpack(config, options) for target ("server" or
    // "client") as next build does for its webpack configs, against a
    // stand-in config: the module rules it adds (loaders: @next/mdx's,
    // @svgr/webpack), resolve.alias / resolve.fallback, and DefinePlugin
    // definitions are what og can honor. The stand-in carries Next's image
    // rule (svgr's documented setup finds and edits it); Next's own rules
    // and its SWC loader (defaultLoaders.babel) are og's already, so rules
    // naming only them are dropped.
    //
    // babel ({ configFile, pagesDir, srcDir }, or null): the project's Babel
    // config, which makes Next compile the project's own files with its
    // Babel loader (next/babel) instead of SWC: the rule Next's webpack
    // config has for that comes first, and defaultLoaders.babel is it.
    async webpackHook(target, dir, buildId, babel) {
      if (services.nextConfig && !liveNextConfig) liveNextConfig = services.nextConfig();
      // A project may have a Babel config and no next.config.
      const nextConfig = (liveNextConfig && (await liveNextConfig)) || {};
      const isServer = target !== "client";
      // The bundler next build uses (internal/frameworks/next/bundler.go):
      // Turbopack applies the turbopack section and ignores webpack();
      // webpack (and Rspack, NEXT_RSPACK) the reverse. Where next build
      // stops instead (Turbopack by default, a webpack() and no turbopack
      // section: "This build is using Turbopack, with a `webpack` config"),
      // og builds with webpack().
      const turbopackMode = !!process.env.TURBOPACK && !process.env.NEXT_RSPACK;
      const useWebpack = typeof nextConfig.webpack === "function" && (!turbopackMode || (process.env.TURBOPACK === "auto" && !nextConfig.turbopack));
      const bundler = useWebpack || !turbopackMode ? "webpack" : "turbopack";
      const hasHook = useWebpack;
      const turbo = bundler === "turbopack" ? turbopackConfig(nextConfig.turbopack, dir, isServer) : null;
      if (!hasHook && !babel && !turbo) return null;
      const builtin = (name) => `og:next-builtin:${name}`;
      let swcLoader = { loader: builtin("next-swc-loader"), options: {} };
      let babelRule;
      if (babel) {
        const req = globalThis[Symbol.for("og.createRequire")](dir + "/package.json");
        const p = req("node:path");
        swcLoader = {
          loader: req.resolve("next/dist/build/babel/loader/index"),
          options: {
            transformMode: "default",
            configFile: babel.configFile,
            isServer,
            distDir: p.join(dir, ".next"),
            pagesDir: babel.pagesDir || undefined,
            cwd: dir,
            srcDir: p.dirname(babel.srcDir || dir),
            development: false,
            hasReactRefresh: false,
            hasJsxRuntime: true,
          },
        };
        babelRule = { test: /\.(tsx|ts|js|cjs|mjs|jsx)$/, include: [dir], exclude: /[\\/]node_modules[\\/]/, use: [swcLoader] };
      }
      const imageRule = {
        test: /\.(png|jpg|jpeg|gif|webp|avif|ico|bmp|svg)$/i,
        loader: builtin("next-image-loader"),
        issuer: { not: /\.(css|scss|sass)$/ },
        dependency: { not: ["url"] },
        resourceQuery: { not: [/__next_metadata__/, /__next_metadata_route__/, /__next_metadata_image_meta__/] },
        options: { isDev: false, compilerType: isServer ? "server" : "client", basePath: nextConfig.basePath || "", assetPrefix: nextConfig.assetPrefix || "" },
      };
      class DefinePlugin {
        constructor(definitions) {
          this.definitions = definitions;
        }
        apply() {}
      }
      const stub = (name) => class {
        constructor(...args) {
          this.args = args;
          this.pluginName = name;
        }
        apply() {}
      };
      const webpack = {
        version: "5.98.0",
        DefinePlugin,
        ProvidePlugin: stub("ProvidePlugin"),
        IgnorePlugin: stub("IgnorePlugin"),
        NormalModuleReplacementPlugin: stub("NormalModuleReplacementPlugin"),
        ContextReplacementPlugin: stub("ContextReplacementPlugin"),
        EnvironmentPlugin: stub("EnvironmentPlugin"),
        BannerPlugin: stub("BannerPlugin"),
        optimize: { LimitChunkCountPlugin: stub("LimitChunkCountPlugin") },
      };
      let wp = {
        mode: "production",
        name: isServer ? "server" : "client",
        target: isServer ? "node" : ["web", "es6"],
        context: dir,
        entry: {},
        output: { path: dir + "/.next", publicPath: (nextConfig.assetPrefix || "") + "/_next/" },
        module: { rules: [{ oneOf: [] }, imageRule], parser: {}, generator: {} },
        resolve: { alias: {}, fallback: {}, extensions: [".js", ".mjs", ".tsx", ".ts", ".jsx", ".json", ".wasm"], modules: ["node_modules"], plugins: [], mainFields: isServer ? ["main", "module"] : ["browser", "module", "main"] },
        resolveLoader: { alias: {}, modules: ["node_modules"] },
        plugins: [],
        externals: [],
        optimization: { minimize: true, minimizer: [], splitChunks: {} },
        experiments: {},
        performance: false,
        devtool: false,
        node: {},
        // Next's baseWatchOptions (plugins read ignored).
        watchOptions: { aggregateTimeout: 5, ignored: /^((?:[^/]*(?:\/|$))*)(\.(git|next)|node_modules)(\/((?:[^/]*(?:\/|$))*)(?:$|\/))?/ },
        cache: false,
      };
      const options = {
        buildId,
        dev: false,
        isServer,
        nextRuntime: isServer ? "nodejs" : undefined,
        config: nextConfig,
        defaultLoaders: { babel: swcLoader },
        dir,
        totalPages: 0,
        webpack,
      };
      if (hasHook) wp = (await nextConfig.webpack(wp, options)) || wp;

      // Rules: Next's own (and the SWC loader) stay og's.
      const isBuiltin = (u) => (u === swcLoader && !babel) || (u && typeof u === "object" && typeof u.loader === "string" && u.loader.startsWith("og:next-builtin:")) || (typeof u === "string" && u.startsWith("og:next-builtin:"));
      const clean = (rule) => {
        if (!rule || typeof rule !== "object") return null;
        if (rule === imageRule) return null;
        const out = { ...rule };
        if (out.loader !== undefined && isBuiltin(out.loader)) return null;
        if (out.use !== undefined) {
          const uses = (Array.isArray(out.use) ? out.use : [out.use]).filter((u) => !isBuiltin(u));
          if (uses.length === 0) return null;
          out.use = uses;
        }
        for (const k of ["oneOf", "rules"]) {
          if (Array.isArray(out[k])) out[k] = out[k].map(clean).filter(Boolean);
        }
        if (out.use === undefined && out.loader === undefined && !(out.oneOf && out.oneOf.length) && !(out.rules && out.rules.length)) return null;
        return out;
      };
      const rules = ((wp.module && wp.module.rules) || []).map(clean).filter(Boolean);
      if (babelRule) rules.unshift(babelRule);
      if (turbo) rules.push(...turbo.jsRules);

      // The files to offer the rules' loaders: the extensions they match,
      // among common ones and those the rules' tests name (/\.links$/,
      // /\.(md|mdoc)$/, "x.ext").
      const exts = new Set(["js", "jsx", "mjs", "cjs", "ts", "tsx", "mts", "cts", "md", "mdx", "markdown", "svg", "graphql", "gql", "yaml", "yml", "toml", "txt", "html", "htm", "glsl", "vert", "frag", "csv", "xml", "po"]);
      const named = (t) => {
        if (Array.isArray(t)) return t.forEach(named);
        if (t instanceof RegExp) {
          for (const m of t.source.matchAll(/\\\.(?:\((?:\?:)?([A-Za-z0-9_|]+)\)|([A-Za-z0-9_]+))/g)) {
            for (const e of (m[1] || m[2]).split("|")) if (e) exts.add(e.toLowerCase());
          }
        } else if (typeof t === "string") {
          const m = /\.([A-Za-z0-9_]+)$/.exec(t);
          if (m) exts.add(m[1].toLowerCase());
        }
      };
      const walkTests = (list) => {
        for (const r of list || []) {
          if (!r || typeof r !== "object") continue;
          named(r.test);
          named(r.include);
          named(r.resource);
          if (r.ogExt) exts.add(r.ogExt);
          walkTests(r.oneOf);
          walkTests(r.rules);
        }
      };
      walkTests(rules);
      // Turbopack rules name their extension (ogExt): a probe file cannot
      // match a glob such as "*.react.svg".
      const matched = [...new Set([...[...exts].filter((e) => rulesUses(rules, `${dir}/og-probe.${e}`, "").length > 0), ...(turbo ? turbo.jsRules.map((r) => r.ogExt).filter(Boolean) : [])])];
      const filter = matched.length > 0 ? new RegExp(`\\.(${matched.join("|")})$`) : /^$/;
      hooks[target] = { rules, filter, cssRules: turbo ? turbo.cssRules : [] };

      // Aliases: name -> candidate targets (false: an empty module).
      const rootDir = (v) => (typeof v === "string" && (v === "private-next-root-dir" || v.startsWith("private-next-root-dir/")) ? dir + v.slice("private-next-root-dir".length) : v);
      const aliasMap = (a) => {
        const out = {};
        const put = (name, value) => {
          const list = (Array.isArray(value) ? value : [value]).map(rootDir).filter((v) => v === false || typeof v === "string");
          if (list.length > 0) out[name] = list.map((v) => (v === false ? "" : v));
        };
        if (Array.isArray(a)) for (const e of a) e && put(e.name, e.alias);
        else if (a && typeof a === "object") for (const [k, v] of Object.entries(a)) put(k, v);
        return out;
      };
      const defines = {};
      for (const p of wp.plugins || []) {
        if (!(p instanceof DefinePlugin) || !p.definitions) continue;
        for (const [k, v] of Object.entries(p.definitions)) {
          if (typeof v === "string") defines[k] = v;
          else if (v === undefined) defines[k] = "undefined";
          else if (typeof v !== "function" && (typeof v !== "object" || v === null || !v.fn)) defines[k] = JSON.stringify(v);
        }
      }
      const aliases = aliasMap(wp.resolve && wp.resolve.alias);
      if (turbo) Object.assign(aliases, aliasMap(turbo.aliases));
      return {
        aliases,
        fallbacks: aliasMap(wp.resolve && wp.resolve.fallback),
        defines,
        rules: rules.length,
        extensions: matched,
        asyncWebAssembly: !!(wp.experiments && wp.experiments.asyncWebAssembly),
        cssExtensions: turbo ? [...new Set(turbo.cssRules.map((r) => r.ogExt).filter(Boolean))] : [],
        bundler,
      };
    },

    // webpackLoad(target, path) -> { contents, loader } | null
    //
    // A module through the hook's loaders (webpackHook), outside a build:
    // the export probe of an .mdx page.
    async webpackLoad(target, path) {
      const h = hooks[target];
      if (!h) return null;
      const uses = rulesUses(h.rules, path, "");
      if (uses.length === 0) return null;
      const req = globalThis.require;
      const fs = req("node:fs");
      const p = req("node:path");
      const [content] = await runLoaderChain({
        uses,
        resourcePath: path,
        rootContext: services.postcss ? services.postcss.Root : p.dirname(path),
        content: fs.readFileSync(path, "utf8"),
        target: target === "client" ? "web" : "node",
        sourceMap: false,
        resolverFor: fsResolver,
      });
      return { contents: content, loader: "jsx" };
    },

    // A stylesheet through turbopack rules whose loaders yield CSS
    // (as: "*.css"): the CSS, for the PostCSS step.
    async turbopackCSS(target, path) {
      const h = hooks[target];
      const uses = h ? rulesUses(h.cssRules, path, "") : [];
      if (uses.length === 0) return null;
      const req = globalThis.require;
      const fs = req("node:fs");
      const p = req("node:path");
      const [content] = await runLoaderChain({
        uses,
        resourcePath: path,
        rootContext: services.postcss ? services.postcss.Root : p.dirname(path),
        content: fs.readFileSync(path, "utf8"),
        target: target === "client" ? "web" : "node",
        sourceMap: false,
        resolverFor: fsResolver,
      });
      return String(content);
    },

    // plugins(kinds) -> [{ kind, index, name }]
    plugins(kinds) {
      const l = lists();
      const out = [];
      for (const kind of kinds) (l[kind] || []).forEach((p, index) => out.push({ kind, index, name: p.name || "anonymous" }));
      return out;
    },

    // setup(kind, index, buildId, initialOptions) -> registrations
    async setup(kind, index, buildId, initialOptions) {
      const plugin = lists()[kind][index];
      const regs = [];
      const register = (type) => (opts, fn) => {
        if (typeof (fn ?? opts) !== "function") throw new TypeError(`${type}: callback must be a function`);
        const id = nextId++;
        if (type === "start" || type === "end" || type === "dispose") {
          callbacks.set(id, { type, fn: opts });
          regs.push({ id, type, filter: "", namespace: "" });
        } else {
          callbacks.set(id, { type, fn });
          const f = opts && opts.filter;
          regs.push({ id, type, filter: f instanceof RegExp ? f.source : String(f ?? ".*"), namespace: (opts && opts.namespace) || "" });
        }
      };
      const build = {
        initialOptions,
        onResolve: register("resolve"),
        onLoad: register("load"),
        onStart: register("start"),
        onEnd: register("end"),
        onDispose: register("dispose"),
        resolve: async (path, options = {}) => {
          const r = await host.resolve(buildId, String(path), {
            importer: options.importer ?? "",
            namespace: options.namespace ?? "",
            resolveDir: options.resolveDir ?? "",
            kind: options.kind ?? "",
            pluginData: token(options.pluginData),
          });
          return { ...r, pluginData: untoken(r.pluginData), errors: r.errors ?? [], warnings: r.warnings ?? [] };
        },
        esbuild: globalThis[Symbol.for("og.pluginEsbuild")],
      };
      await plugin.setup(build);
      return regs;
    },

    async call(id, args) {
      const cb = callbacks.get(id);
      if (!cb) throw new Error("og: unknown plugin callback " + id);
      if (cb.type === "resolve" || cb.type === "load") {
        const r = await cb.fn({ ...args, pluginData: untoken(args.pluginData), with: args.with ?? {} });
        if (r === undefined || r === null) return null;
        const out = { ...r, pluginData: token(r.pluginData), errors: messages(r.errors), warnings: messages(r.warnings) };
        if (cb.type === "load" && r.contents !== undefined) {
          out.contents = typeof r.contents === "string" ? r.contents : new TextDecoder().decode(r.contents);
        }
        return out;
      }
      const r = await cb.fn(args);
      return r ? { errors: messages(r.errors), warnings: messages(r.warnings) } : null;
    },
  };
}
