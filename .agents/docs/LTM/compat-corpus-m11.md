# M11 Compatibility Corpus and Build-Time Config Hooks

## Summary

M11 built a compatibility harness, `og compat`, that runs Oganesson over
Vercel's `next.js/examples/` corpus (install, build, serve, crawl) and uses the
failures as a bug-finding and hardening program. The same milestone added the
build-time next.config hooks the corpus demanded (`webpack()`, Babel configs,
Turbopack rules, bundler selection), build-script command execution, and Next
16.4.0 support alongside 16.3.8. By the end, 148 of 225 examples pass and every
remaining failure has a recorded reason.

## Key Facts

- Corpus runner: `cmd/og/compat.go`, invoked as
  `og compat [-run re] [-j n] [-pages n] [-json file] [-keep] [-failed report.json] [-expected file] dir...`.
- Corpus source: `scripts/sync-next-examples.sh` sparse-checks out
  `vercel/next.js` `examples/` into `third_party/next.js` (gitignored). Pin is
  v16.4.0 (`e273d5b2`, 230 examples, 225 with a `package.json`); fixtures stay
  on 16.3.8.
- `corpusNext` = 16.4.0, `corpusReact` = 19.3.0. "latest"/"canary"/"rc" Next
  and React are pinned to these and the app's lockfile is dropped.
- Current result: 148 of 225 pass (~66%). Every non-service failure has a
  reason in `testdata/compat/expected.json` (37 apps).
- Key tests: `TestCompatLinks`, `TestCompatCopy`, `TestCompatFailed`,
  `TestCompatWorkspaces`, `TestCompatExpected`, `TestWebpackHook`,
  `TestBabelConfig`, `TestFindBin`, `TestRewriteMissingImports`,
  `TestTurbopack`, `TestBundlerEnv`.
- Cache Components ("use cache") is tracked separately in
  [[cache-components-use-cache]]. Many JS-language lowering fixes surfaced here
  live in [[js-language-gaps-and-lowering]]; Node API shims live in
  [[node-compat-shims]].

## Details

### The corpus harness (`cmd/og/compat.go`)

Each app is copied into the work directory (`.agents-workspace/tmp/compat` by
default), then `og install`, a `next-version` check, `og build`, `og serve`
and a crawl run as child `og` processes, so one app's crash is just that app's
failure. The crawl follows same-origin links from `/` and fetches the scripts,
stylesheets, preloads, icons and images each page references; a 5xx page or a
4xx/5xx asset fails the app. Logs go to `<work>/_logs/<app>/`; `-json` writes
the whole report.

- Each app's `node_modules` and `.og/` are deleted after its run unless
  `-keep` (a full `-keep` run fills the disk: ~6 GB for 40 apps).
- `og compat -failed report.json` reruns an earlier report's failures.
- `og compat -expected file` (default `testdata/compat/expected.json`) records
  each app's known-failure reason in the report. The summary's last lines are
  `unexpected failures: N (names)` (failures neither listed nor in an app with
  a `.env` example, i.e. og's to look into) and `listed as expected but
  passing: names` (entries to drop).
- Monorepos: the copy pins "latest" Next/React in every workspace's
  package.json and drops the lockfile; next-version, build and serve run in the
  first workspace that depends on next (`compatAppDir`).

Of the failures, a large share ship a `.env` example (external service: they
fail like `next build` without it); others pin Next 9 to 12 (`next-version`),
rely on React 18 internals, use native tools (rspack, couchbase), or are
genuine upstream breakage in the example.

### next.config's `webpack()` hook

The largest gap the corpus left: `@next/mdx`, `@svgr/webpack`, nextra and
app-level aliases all extend Next through `webpack(config, options)`.

- **Running the hook** (bridge's `webpackHook`, `internal/jshost/js/bridge.js`):
  per target ("server"/"client"), against a stand-in webpack config carrying
  Next's image rule, `resolve.alias`/`fallback`, `plugins`, and
  `watchOptions` = Next's `baseWatchOptions` (`ignored` is a RegExp). Options
  include `isServer`, `nextRuntime`, `buildId`, `dir`, the live next.config,
  `defaultLoaders.babel` (a marker for Next's SWC loader) and a stub `webpack`
  (`DefinePlugin` records its definitions; other plugin classes are inert).
  Rules naming only Next's own loaders are dropped (og does their work); the
  rest run through og's loader shim as the `webpack-server`/`webpack-client`
  plugins, offered the files whose extensions the rules match.
- **Go side** (`internal/frameworks/next/webpackhook.go`): hooks run after
  next.config loads. Aliases follow webpack: a key names the module and its
  subpaths (`$`: the module only), candidates in order, `false` an empty
  module; an aliased resolution is marked so `x` -> `x/dist` does not recurse.
  The browser's `resolve.fallback` applies to what does not resolve.
  DefinePlugin values join each target's defines. An `.mdx` page's exports are
  probed from the loaders' output (`webpackLoad`, `rsc.ProbeExportsSource`).
- **Loader shim** (`internal/jshost/js/webpack.js`): honors `rule.loader` with
  `rule.options`, `resourceQuery`, `resource`, nested `rules` and `oneOf`;
  loaders get `this._module` with a `buildInfo`. Inside builds it resolves to
  files only (a stand-in for a missing package must not resolve, or
  `@markdoc/next.js` emits `import * as config from 'config'`). `getResolve`'s
  `preferRelative` is honored. A rule's extension is taken from its `test` too
  (`/\.links$/`), not only a fixed list.

### Babel configs

A `.babelrc`/`babel.config.*` (unless `experimental.forceSwcTransforms`) makes
next build compile the project's files with Next's Babel loader
(`next/dist/build/babel/loader`, the `next/babel` preset and the config's
plugins) instead of SWC. The webpack hook's stand-in config carries that rule
first (project files, not node_modules, with getBabelLoader's options), also
for a project without next.config, and `defaultLoaders.babel` is it. `.mdx` and
Babel-only syntax are probed through the loaders (`probeExports`,
`internal/frameworks/next/missingexports.go` neighborhood). Next's Babel loader
installs SWC bindings first (React Compiler check): a no-op substitute on the
build host (`jshost/native.go`); loaders get Next's `currentTraceSpan` (a
stand-in).

### "No matching export" tolerance

esbuild rejects an ES module importing a name its target lacks; webpack warns
and binds undefined, so next build passes (`cms-datocms`: react-datocms imports
`renderRule`, exported as `renderNodeRule`). A build failing only with such
errors is retried (`buildTolerant`, server/browser/pages-browser), each recorded
import rewritten on load into a namespace read
(`import * as __og_mxN from "m"; var name = __og_mxN["name"];`).
`internal/frameworks/next/missingexports.go`; `TestRewriteMissingImports`.

### Turbopack rules and bundler selection

Next 16 builds with Turbopack by default, so apps configure loaders in
`turbopack.rules` and aliases in `turbopack.resolveAlias`.

- `turbopackConfig` (`internal/jshost/js/webpack.js`) translates both into the
  webpack hook's terms: a rule's glob matches the file name, or the
  project-relative path when it has a "/"; values can be a rule, an array, or a
  plain loader list; conditions `browser`/`node`/`edge-light`/`foreign`/
  `production`/`development`/`path`/`content`/`all`/`any`/`not`; aliases take a
  string, array, or `{ browser, default }`.
- Rules with JS output join the hook's rules; their extension (`ogExt`, from
  the glob) is offered to the loader plugin directly. Rules with `as: "*.css"`
  run in the stylesheet build (`b.isCSS`, `cssLoadFilter`); `postcss()` takes
  the loaders' output (`TurbopackCSS`) before PostCSS.
- **og picks the bundler as next build does** (`bundlerEnv`, `bundler.go`).
  with-rspack fails loading next.config unless `process.env.TURBOPACK === "auto"`;
  Next's `parseBundlerArgs` sets that before loading next.config when there is
  no bundler flag. The mode comes from the project's `next build` command:
  "auto" with no flag, "1" for `--turbopack`/`--turbo`, nothing for `--webpack`;
  a `TURBOPACK` the script/env sets is kept; both next.config loads (`LoadNext`
  and the plugin host) run with it.
- The webpack hook applies exactly one configuration: Turbopack mode uses the
  turbopack section only (ignoring `webpack()`); webpack/Rspack uses `webpack()`
  only; where next build would stop (webpack config + no turbopack config), og
  keeps building with `webpack()`. `getDefineEnv` keeps `isTurbopack: false` —
  og's bundles are webpack-shaped whichever configuration applies (passing the
  mode there took Turbopack-only runtime paths:
  "__turbopack_clear_chunk_cache__ is not defined").
- @next/mdx's Turbopack alias target `@vercel/turbopack-next/mdx-import-source`
  resolves to the candidates its webpack alias lists.

### Build-script command execution (`internal/frameworks/next/codegen.go`)

og runs, as npm would: `postinstall` and `prepare` (the installer runs no
lifecycle scripts), `prebuild`, and the build script's commands before
`next build`. `node file.js ...` and the bins of installed packages (`findBin`:
the package of that name, else a top-level package declaring the bin; og's
installer makes no `node_modules/.bin`) run on the build host (`RunProgram`, 10
min max), `prisma generate` as before. Type checkers, linters, formatters, shell
utilities and commands no installed package provides are skipped. A failing
command fails the build. `TestFindBin`.

Top-level-await programs (`jshost.RunProgram`): an ES module awaiting at top
level cannot be `require`d (Prisma 8's CLI); it is bundled in og's
top-level-await mode and imported. A hashbang stays the first line of a
localized file (`LocalizeFile` turning it into line 2 is a syntax error).

### Next 16.4.0 support

Diffing the ~150 Next modules og references between 16.3.8 and 16.4.0: all
still exist; 26 changed. Signatures og calls (`createRequestResponseMocks`,
`buildPagesStaticPaths`) are unchanged.

- **Static generation moved to `prerender()`.** 16.4 route modules split:
  `AppRouteRouteModule.prerender()`/`AppPageRouteModule.prerender()`
  (`prerenderToHTMLOrFlight`, `createPrerenderWorkStore`) statically generate,
  while `handle()`/`render()` always render dynamically. og's static attempts
  (`isr.StaticHeader`) call `prerender()` when the module has one. The edge
  route wrapper no longer sets `supportsDynamicResponse`. Without these patches,
  16.4 builds pass but silently lose static generation.
- The wrapper's anchor got a 16.4 variant: `patchAlts` in `patches.go` lists
  per-version alternatives keyed by the 16.3 text; one variant must apply
  exactly once.
- Unknown server action ids: 409 in 16.4 (`handleUnrecognizedAction`), 404 in
  16.3 — og follows Next.
- Tests run against other releases: `appDir` resolves fixtures under
  `$OG_TEST_APPS`; version-dependent expectations read `installedNext`;
  `TestRouteHandlerCaching` checks the recorded revalidate per route handler
  (3600 and -1). Neutralized on 16.4: dropping the app-page patch fails
  `TestBasePath`/`TestStaticAndISR`/`TestNextFont`; dropping the route patch
  fails `TestRouteHandlerCaching`.

### Categories of corpus fixes

Most fixes carried a regression test, neutralized to confirm the diagnostic.

- **JSX in a project's own `.js` files** (Next's SWC enables JSX for every
  non-TypeScript file): `loaderFor` in the server/browser loads and
  `rsc.ProbeExports`' loaders. ~55 examples failed on it.
- **Computed relative requires/imports** (``require(`../components/${name}.tsx`)``):
  become maps of literal requests (`context.go`, ogapi `ComputedImports`), on
  server builds only; the browser build's `file`-namespace loader handles
  esbuild's glob. `TestExpandComputedImports`.
- **`"use client"`/`"use server"` anywhere in the directive prologue**
  (`rsc.DirectivePrologue`); CommonJS client modules' named exports become
  client references (ogapi `CommonJSExports`, reexports followed).
- **Node-mode default imports** (`nodeinterop.go`): a `.js` ES module of a
  `"type": "module"` package gets a CommonJS module's `module.exports` as its
  default import (tslib/Chakra UI). Routed through an `og-node-default:`
  specifier; asset specifiers and `next/font` loader imports are left alone.
- **Resolve extensions**: Next's `resolve.extensions`
  (`.js`,`.mjs`,`.tsx`,`.ts`,`.jsx`,`.json`) for server/browser builds;
  esbuild's default lacks `.mjs` (Panda's `styled-system/css/index.mjs`).
- **Optional dependencies** (`internal/install`): og now installs portable
  optional deps (no `os`/`cpu` field) and still leaves platform-bound ones out;
  a dynamic `import()` of an uninstalled package rejects with
  `ERR_MODULE_NOT_FOUND` at run time. Missing non-optional peers are installed
  as npm 7 does. `TestHoistingAndConflicts`, `TestPeerDependencies`.
- **Browser builds**: a module a package's `"browser"` field disables is an
  empty module; a `require()` of an uninstalled package throws at run time
  (framer-motion's optional `@emotion/is-prop-valid`). Browser aliases follow
  Next's `getOptimizedModuleAliases` (fetch polyfills, `url` ->
  `next/dist/compiled/native-url`).
- **React 18 in the Pages Router** (`layers.go`): Pages code uses the project's
  `react`/`react-dom`, which Next 16 still accepts as React 18; og had aliased
  the vendored React 19 into the Pages layers. `TestPagesReact18`.
- **basePath** (previously ignored by og's router): the router strips basePath
  before matching; Go cache companion paths keep it; the static handler serves
  `/_next/static` and public files under basePath; prerender renders at basePath
  URLs; the image optimizer answers at `basePath + "/_next/image"`. `assetPrefix`
  (Next sets it to basePath when unset) prefixes CSS `url()` assets, next/font
  URLs, `__webpack_public_path__`, and `moduleLoading.prefix`. `TestBasePath`.
- **Lazy module loading**: routes load on first use through a getter
  (`lazyModule` in `virtual.go`), rethrowing a failed load each time, so one
  throwing route does not fail every request or the build
  (`TestRouteLoadFailureIsolated`). SSR client modules load when a page
  references them (`registrySource`, lazy `require`, esbuild's `__toESM`
  shape), so one that throws on the server does not fail even `/`
  (`TestClientModuleLoadFailureIsolated`).
- **`experiments.asyncWebAssembly`** (with-webassembly): a relative `.wasm`
  import in the server build is an ES module of the instance's exports;
  instantiation is synchronous, imports resolved next to it; import/export names
  from a small section reader (`wasmesm.go`).
- **Workspaces** (`internal/install/workspaces.go`): a root's `workspaces` or
  pnpm's `pnpm-workspace.yaml` names local packages; each is a relative symlink
  at `node_modules/<name>`; a dependency on a workspace resolves to the link
  (never fetched); an unnamed workspace is named after its directory. A
  package-lock layout is set aside when there are workspaces; pnpm/yarn pins
  apply. `TestWorkspaces`, `TestPnpmWorkspaces`.
- **sharp 0.35 at build time**: moved entry points to `dist/index.cjs`/`.mjs`
  behind `"exports"`; `nativeSubstitute` matched only `lib/index.js`, so the
  real package bundled into the build host and failed on `.node` requires. Both
  entry points now get og's substitute. `TestBuildTimeSharp`.
- **The page render's `res` starts at status 200** (`__ogResponse`): Node's
  `ServerResponse` starts at 200, while `WebNextResponse` left `statusCode`
  unset, so next-graphql-react's `getInitialProps` never resolved.
- **The page cache cannot be grown by arbitrary URLs/queries** (`runtime/isr`):
  unmatched URLs serve from the single `/_not-found` entry (`isr.MatchHeader`,
  `isr.CachePathHeader`); static App Router renders are keyed by path only
  (`PathKeyHeader`, since reading searchParams bails to dynamic); dynamic
  markers drop the query (`pathKey`). `TestNotFoundCachedOnce`,
  `TestStaticPageQueryIndependent`, `TestDynamicMarkerIgnoresQuery`.
- **with-msw** needed `node:_http_common` (see [[node-compat-shims]]),
  `typeof window` replaced per target (`typeofGlobal`, `typeofs.go`: project
  modules only, skip a module binding `window` itself;
  `TestTypeofWindowShadowed`), and `instanceof` with a Proxy of a function
  (`installProxyHasInstance` in the prelude; `TestProxyInstanceof`). Many of
  these engine/lowering fixes are detailed in [[js-language-gaps-and-lowering]].
- **GraphQL Mesh** (`mesh build` in the build host) needed `module.require`
  (`withModuleFields` prepended by `moduleSource`: `require`, `id`/`filename`,
  `path`, `paths`, `loaded`, `children`; `TestModuleObject`), a proper CallSite
  class with methods on its prototype (source-map-support's `cloneCallSite`;
  `TestPrepareStackTrace`), and `Module._preloadModules` (`TestModulePreload`).

## Files

- `cmd/og/compat.go` — the `og compat` harness (install/build/serve/crawl,
  `-run`/`-j`/`-pages`/`-json`/`-keep`/`-failed`/`-expected`).
- `scripts/sync-next-examples.sh` — sparse checkout of the corpus at v16.4.0.
- `testdata/compat/expected.json` — 37 apps, name -> known-failure reason.
- `internal/frameworks/next/webpackhook.go` — the Go side of the `webpack()`
  hook (aliases, fallback, defines, export probing).
- `internal/jshost/js/bridge.js` — `webpackHook`: the stand-in webpack config.
- `internal/jshost/js/webpack.js` — the loader shim and `turbopackConfig`.
- `internal/frameworks/next/missingexports.go` — "No matching export"
  tolerance and the namespace-read rewrite.
- `internal/frameworks/next/codegen.go` — build-script command execution.
- `internal/frameworks/next/bundler.go` — `bundlerEnv`: picking the bundler as
  next build does.
- `internal/frameworks/next/patches.go` — `patchAlts` for 16.3/16.4 anchors.
- `internal/install/workspaces.go` — npm/pnpm/yarn workspaces.
- `wasmesm.go` neighborhood — async WebAssembly ES-module shaping.
- `typeofs.go` — `typeof window` per target.
- Fixtures: `testdata/apps/next-webpack`, `next-turbopack`, `next-babel`,
  `next-basepath`, `next-pages-react18`, plus `next-pages`/`next-hello` cases.

## Test Coverage

- Harness: `TestCompatLinks`, `TestCompatCopy`, `TestCompatFailed`,
  `TestCompatWorkspaces`, `TestCompatExpected`.
- Config hooks: `TestWebpackHook`, `TestBabelConfig`, `TestTurbopack`,
  `TestBundlerEnv`, `TestRewriteMissingImports`.
- Build scripts: `TestFindBin`, `TestRunProgramTopLevelAwait`.
- The corpus itself needs the network and takes ~1 hour at `-j 4`; run it in
  the background and compare runs with `-json`. Regression tests run in the
  normal gate (Next suite ~300 s, Vite suite ~430 s).

## Pitfalls

- **Known open corpus gaps (TODO.md, M11):**
  - `webpack()` `config.plugins` do not run (only `DefinePlugin` is recorded;
    other plugin classes are inert). with-vanilla-extract would need Next's CSS
    `oneOf` rules and webpack child compilations.
  - `experimental.mdxRs` (Next's native SWC via `@next/swc-wasm-nodejs`): not
    done. Next tries to download the wasm into `$XDG_CACHE_HOME/next-swc`; og
    reports Linux with home `/`, and `getRegistry` runs
    `npm config get registry` (needs child_process). Loading the ~20 MB wasm
    under og is untried.
  - `npx`/CLI of uninstalled packages (with-edgedb's `npx edgeql-js`).
  - Computed requires inside packages (not just project code).
  - `issuer`/`externals` webpack rule selectors are not modeled.
  - KeepNames is deliberately NOT applied to the host bundle (it broke
    `ContextualThenables`' `then:` detection, Nuxt/TanStack Start
    `fn.toString()` inlining, and resolve-url-loader templates); only the
    per-request `moduleSource` uses `KeepNames` with a global `__name` helper.
- A precondition regex that matches nearly every module turns a rewrite into a
  full TS-Go parse of every module (jshost's `require` runs `Finish` per
  module); keep rewrite preconditions tight or the Vite suite hits go test's
  10-minute timeout.
- Disk pressure: a `-keep` run or the Go build cache
  (`.agents-workspace/tmp/gocache-probe`, has reached 25-31 GB) fills the
  volume; installs then fail with "no space left on device".
- prezto: `open(p,'w')` truncates before reading the same file; see AGENTS.md
  shell pitfalls for `cp`/`rm`/`NO_CLOBBER`.
- Expected-under-next-build failures (not og's): with-vanilla-extract,
  image-secure-compute (403 without a User-Agent), with-filbert (classic `@jsx`
  pragma under automatic runtime), with-yarn-workspaces (next-transpile-modules
  9 calls `watchOptions.ignored.filter`; Next 16's `ignored` is a RegExp),
  with-stencil, prisma-postgres (Prisma 8.0.0-rc has no `generate`),
  with-mdbreact (React 17 nested beside React 18), with-goober/with-rebass/
  with-yoga/with-turbopack/with-urql (undeclared deps or server-component
  `createContext`), with-sentry (`/ssr/*` throw on purpose),
  with-orbit-components.
