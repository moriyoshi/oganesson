# Vite Framework and Plugin Ecosystems

## Summary

Oganesson builds arbitrary Vite projects through its Rolldown facade
([[vite-and-rolldown-facade]]) and serves SSR output as a packaged server
program ([[server-programs-ssr]]). Each framework and framework-plugin
ecosystem (Vue, Svelte 5, Tailwind 4, Preact, Solid, UnoCSS, Astro 7, React
Router 8, SvelteKit 3, Nuxt 4, SolidStart 2, Qwik City 1, vite-plugin-pwa) was
validated against a fixture under `testdata/apps/*` and compared path-for-path
with a real Node build. This document records, per ecosystem, the fixture, the
framework-specific fix each needed, and the structural differences that remain
against a native Rolldown/Rollup build.

## Key Facts

- Every ecosystem has a fixture under `testdata/apps/` and a dedicated test;
  fixtures skip when their `node_modules` is absent (install with
  `go run ./cmd/og install <dir>`).
- Fixtures and build/start scripts:

  | Ecosystem | Fixture | Build script | Start / server |
  | --- | --- | --- | --- |
  | Vue SFC | `vite-vue` | `vite build` | static |
  | Svelte 5 runes | `vite-svelte` | `vite build` | static |
  | Tailwind 4 | `vite-tailwind` | `vite build` | static |
  | Preact | `vite-preact` | `vite build` | static |
  | Solid | `vite-solid` | `vite build` | static |
  | UnoCSS | `vite-unocss` | `vite build` | static |
  | vite-plugin-pwa | `vite-pwa` | `vite build` | static (generateSW) |
  | Astro 7 static | (astro static) | `astro build` | MPA static |
  | Astro 7 node | (astro node) | `astro build` | `@astrojs/node` program |
  | React Router 8 | `react-router` | `react-router build` | `react-router-serve ./build/server/index.js` |
  | SvelteKit 3 | `sveltekit-node` | `vite build` | `node build` (`build/index.js`) |
  | Nuxt 4 | `nuxt` | `nuxt build` | `node .output/server/index.mjs` |
  | SolidStart 2 | `solid-start` | `vite build` | `node .output/server/index.mjs` |
  | Qwik City 1 | `qwik-city` | `qwik build` | `@builder.io/qwik` node-server adapter |
  | Vite 7 | `vite7-react` | `vite build` | static |

- Comparisons against Node consistently match in behavior (statuses, headers,
  data, routes, actions) and differ only structurally: esbuild's chunk names
  and split graph, minifier naming, and inlined function source where esbuild
  renamed identifiers (`storageKey2`, `resolve2`). See Pitfalls.

## Details

### Vue, Svelte 5, Tailwind 4 (M9 part 5)

- **Vue SFC** (`vite-vue`, `<script setup lang="ts">` + scoped style). Needed:
  the builtin TS transform to take its language from the id's own extension, so
  `App.vue?vue&type=script&lang.ts` is parsed as TypeScript (before it reached
  esbuild as JS: "Expected ')' but found ':'"). The scoped style
  (`App.vue?vue&type=style...&lang.css`) was being dropped because esbuild
  leaves zero-byte modules out of every output's `inputs`; the facade now
  reattaches each zero-code module to its nearest importer's output so
  `vite:css-post` still collects it. The binding stub gained `TsconfigCache`,
  `enhancedTransform(Sync)` and `minify(Sync)` (Vite's `transformWithOxc`,
  which plugin-vue uses). `createBuilder(config, null)` (as `vite build` passes)
  builds only the client environment; passing `false` built every environment
  and plugin-vue failed in an `ssr` build of `index.html`.
- **Svelte 5** (`vite-svelte`, runes, `lang="ts"`). Needed
  `this.getCombinedSourcemap()` working per transform call through a per-call
  context (maps original source to itself), which Svelte's plugin requires.
- **Tailwind 4** (`vite-tailwind`, `@theme`, classes in HTML and TS). Needed
  `@tailwindcss/oxide` to load its wasm32-wasi build inside Vite's config
  bundle: the napi-rs WASI substitutes (`og:napi-rs-wasi`) now apply in host
  bundling as well as runtime `require` (before: "Cannot find native binding").
  See [[wasm-on-wazero]].
- Tests: `TestBuildVue`, `TestBuildSvelte`, `TestBuildTailwind`.

### Preact, Solid, UnoCSS (M9 part 8)

- **Preact** (`vite-preact`, @preact/preset-vite 2.10.6, @babel/core 7.29.7 —
  the preset's peer range is 7.x). The preset builds
  `new Function("specifier", "return import(specifier)")` at module load so
  CommonJS compilation cannot turn it into `require()`; Sobek/Goja cannot
  compile `import()` in a Function body ("Unexpected reserved word"). jshost
  rewrites that idiom in host files to `((specifier) => import(specifier))` via
  the `functionImport` regex in `fileLocalsPlugin` (runtime `import()` of a
  variable specifier already works).
- **Solid** (`vite-solid`, vite-plugin-solid 2.11.14, solid-js 1.9.16): worked
  as is.
- **UnoCSS** (`vite-unocss`, unocss 66.10.5, presetWind3): worked. Note: its
  default pipeline includes `.html`, `.[jt]sx`, `.vue` but not plain `.ts`, so
  the fixture uses `index.html` and a `.tsx` module (a `.ts`-only first fixture
  got only preflight CSS).
- Tests: `TestBuildPreact`, `TestBuildSolid`, `TestBuildUnoCSS`, jshost
  `TestFunctionImport`.

### vite-plugin-pwa (M9 part 9)

`vite-pwa` fixture, `generateSW` mode. Obstacles, in order:

1. **`@vite-pwa/assets-generator`** is an optional peer imported by subpath in a
   lazy chunk. The host plugin now leaves an unresolvable import external when
   the importing package declares it optional (`peerDependenciesMeta`,
   `isOptionalPeer`), for any import kind, not only `require`.
2. **Rollup 4's native binding**: `runtime/natives` registers
   `rollup` -> `@rollup/wasm-node` at the same version (so og install adds it);
   jshost swaps `rollup/dist/native.js` for the WebAssembly build's (same API,
   as with lightningcss -> lightningcss-wasm). See [[wasm-on-wazero]].
3. **workbox-build writes temp files under `os.tmpdir()`**: unenv's `os.tmpdir()`
   now follows Node's rule (TMPDIR, TMP, TEMP, else /tmp), and the build host
   sets TMPDIR to `<project>/node_modules/.cache/og-tmp` so writes stay in the
   sandbox.
4. **Build-time `worker_threads`**: `@rollup/plugin-terser` runs terser in a
   `worker_threads` pool and unenv's Worker never answers, hanging the build.
   og implements build-time workers (`internal/jshost/workers.go`,
   `runtime/nodeshim/js/worker_threads.js`): each Worker runs its file through
   the host's runtime `require` in a new engine on its own goroutine with a
   cancellable context; messages are posted between loops as serialized strings
   (typed arrays, Map, Set, Date, RegExp, BigInt, Error, undefined); a
   referenced running worker holds the parent loop; `ref`/`unref`/`terminate`
   supported; uncaught errors become error then exit 1.
5. **`workbox-window` from the virtual module**
   `/@vite-plugin-pwa/virtual:pwa-register`: that id's "directory" does not
   exist, so bare imports now resolve from the root when the importer's
   directory is missing.

- Result: the build writes a minified `sw.js` (precaching `index.html`, the
  entry, workbox-window, `manifest.webmanifest`), the workbox runtime chunk and
  the manifest link; in the browser the SW registers, activates and precaches
  those four URLs. Test: `TestBuildPWA`.

### Astro 7 (M9 part 10; Markdown in the Sobek entry)

- **`internal/frameworks/vite/astro.go`**: `DetectAstro` (an `astro.config.*`
  plus the `astro` package) and `buildAstro`. The host entry imports the
  project config (bundled into the entry) and calls
  `build({ ...config, root, configFile: false })` — loading the config at run
  time via a `file://` import pulled in a second copy of Vite. Static output
  becomes an `mpa` client; a `dist/server/entry.mjs` makes it a server program
  with client dir `dist/client`, `NODE_ENV=production`.
- **The esbuild npm package** (`internal/jshost/esbuildjs.go`): `esbuild/lib/main.js`
  is shimmed over og's own esbuild (`transform`, `transformSync`, `build`,
  `buildSync`, `formatMessages`, `analyzeMetafile`; plugins and `context`
  unsupported). The npm package otherwise spawns its native binary.
- **satteri** (Astro's Markdown processor) is an emnapi/napi-rs WASI addon;
  its loader now loads via the napi-rs WASI runtime (see [[wasm-on-wazero]]),
  so Markdown pages build. **Shiki** decodes inlined Oniguruma wasm with
  `Uint8Array.from(atob(...), c => c.charCodeAt(0))`; Sobek/Goja reject a
  primitive source, so the prelude boxes it as GetMethod does (`polyfills.ts`).
- **Intl**: `PluralRules`, `NumberFormat`, `DateTimeFormat` from FormatJS as
  lazy groups (`runtime/webapi/js/src/intl.ts`) — Astro's logger formats times.
- Runtime: request bodies may be async iterables
  (`@astrojs/node` passes `IncomingMessage`, taken with `duplex: "half"`);
  `util.stripVTControlCharacters`; an `mpa` app serves `x/index.html` for `/x`
  after `x.html`.
- Tests: `TestBuildAstroStatic`, `TestServeAstroNode`, `TestSatteriMarkdown`,
  `TestEsbuildPackage`, `TestTypedArrayFromPrimitive`.
- Pitfall: `@astrojs/node` standalone builds request URLs with
  `process.env.PORT ?? options.port ?? 8080` and Astro's origin check rejects a
  POST whose `Origin` differs; the packaged binary sets `PORT` to the listening
  port so it matches. Real Node also refuses an `Origin` of
  `http://127.0.0.1:<port>`; the test uses `localhost`.

### React Router 8 framework mode (M9 part 11)

- `react-router` fixture (default framework-mode template plus loaders, an
  action, a resource route, a prerendered page). `react-router build` is Vite's
  `createBuilder({ root }).buildApp()`, which og's `vite build` already is.
  What it needed:
  - An `og:config-file` plugin (`enforce: "pre"`, sets `configFile` in
    `configResolved`): og passes the config inline but React Router requires a
    config file and reloads it itself (`vite.loadConfigFromFile`) for its child
    compiler.
  - `jshost.SharedModules`: the Vite host registers `vite` so a run-time
    require (by name or by the resolved file, as the config loader imports vite
    by file URL) gets the same instance.
  - package.json `"imports"` (`#name`, patterns and conditions); ESM modules
    declaring a wrapper name (`const __dirname = ...`) placed in a block;
    default imports between converted modules via `Symbol.for("og.esm")` and
    esbuild's `__toESM` honoring the mark (`markESM`, `HonorESM`).
  - Server programs: a script may run a dependency's bin (package.json `"bin"`)
    with arguments; `react-router-serve` imports its build by computed URL (the
    `Dynamic require` fallback finds it by packaged path or file URL).
    Prerendering goes through og's `startProgramPreview(root)` with a `node:http`
    client patched to `fetch`. See [[server-programs-ssr]].
  - esbuild `Conditions` for program/server bundles no longer name `"import"`:
    that made `require()` take dual packages' ESM builds (Express 5's
    `is-promise` became a namespace object, "Not a function").
- Tests: `TestServeReactRouter`, `TestRuntimeModules`, `TestServerProgram`,
  `TestBundleServerConditions`, `TestProcessStdin`, `TestProcessHrtime`.
- Difference: HTML differs in chunk names/graph and in `ScrollRestoration`'s
  inlined function source where esbuild renamed identifiers (`storageKey2`);
  both behave the same.

### SvelteKit 3, adapter-node (M9 part 12; error page in a later entry)

- `sveltekit-node` fixture (server `load`, form actions, a prerendered page, an
  endpoint, scoped CSS). SvelteKit 3.0.1 + `@sveltejs/adapter-node` 6.0.0 takes
  config through `sveltekit({ adapter })` in the Vite config (a `svelte.config.js`
  is an error). Start script `node build` -> server program `build/index.js`.
- What it needed:
  - `Host.TopLevelAwait`: bundles needing top-level await run as ESM in an async
    function (`EvalWith` awaits it); external imports become `require()` with
    `__toESM`-like interop, deferring load failures to use site
    (`requireExternals`). Kit awaits optional `typescript` at module level.
  - `Host.Shared`/`WorkerShared`: packages a bundle registers under
    `SharedModules`; build workers also share the config files' bare imports
    (`sharedPackages`) since a worker reloading the config needs them and
    `import *` would defeat tree shaking (UnoCSS's whole package loads a native
    binding).
  - `import.meta.dirname`/`filename` (`ReplaceImportMeta`, `ogapi.ImportMetaURLs`);
    worker `env` option, `process.exit()`, structured-clone messages (Kit's
    payloads have cycles).
  - engine: a 15000 call-depth limit so runaway recursion raises Node's
    `RangeError: Maximum call stack size exceeded` instead of a Go stack
    overflow.
  - `gojatarget.HoldArrowArguments`: a function whose `arguments` is read only
    by nested arrows loses it in Sobek/Goja; `void arguments;` keeps it (vitefu's
    CJS wrapper hit this).
  - nodeshim: `fs` file descriptors (`openSync`/`readSync`/`writeSync`/...);
    `Intl.ListFormat`, `RelativeTimeFormat`, `DisplayNames` (FormatJS, `en-GB`).
  - Server programs: the program's own directory is packaged when it is a build
    output (`build/`). Responses without `Content-Type` go out without one
    (`webapi.WriteResponse`); net/http had been sniffing one. `og build -o`
    writes the binary then renames it (`writeExecutable`): rewriting a signed
    binary in place made macOS kill it (exit 137).
- **The error page (split server builds).** `/nope` returned 500 instead of
  404. The real cause (an earlier entry blamed Goja's thenable job, which was
  wrong): the facade built server environments **unsplit**, so each entry
  carried its own copy of shared modules — Kit's fallback error page
  (`entries/fallbacks/error.svelte.js`) had its own `get_or_init_context_map`/`ssr_context`
  that the renderer (another copy) never set. Fix: **server builds are split**
  like client builds, so shared modules are one instance across entries. Server
  builds had been unsplit to dodge the esbuild NumberRenamer race; that race is
  now mitigated by `globalDefines` (object/array-valued defines become
  `globalThis.__og_define_<n>`, an entity name with no shared symbol, set once
  per chunk banner). See [[vite-and-rolldown-facade]].
- Tests: `TestServeSvelteKit` (`/nope` -> 404 with Kit's error page),
  `TestTopLevelAwait`, `TestWorkerEnvAndExit`, `TestHoldArrowArguments`,
  `TestCallStackLimit`, `TestNoContentTypeSniffing`, `TestWriteExecutable`,
  `TestGlobalDefines`.
- Pitfall: adapter-node v6 has no `ORIGIN`; protocol comes from
  `PROTOCOL_HEADER` (https otherwise), so a plain-http form POST is cross-site
  under Node too (the test sets `PROTOCOL_HEADER=x-forwarded-proto`). Kit
  answers a form action with JSON unless the request `Accept`s HTML.

### Nuxt 4, Vite and Nitro (M9 part 13)

- `nuxt` fixture (a page using `useFetch` against a server API route, a
  prerendered route, a component with scoped CSS and client state).
  **`nuxt.go`**: `DetectNuxt` and `buildNuxt` run what `nuxi build` runs
  (`@nuxt/kit`'s `loadNuxt`, `writeTypes`, `buildNuxt`). Nitro 2 builds the
  server with real Rollup (its wasm build) and esbuild (og's shim), prerenders
  through its own fetch, and writes `.output`. The server program is the start
  script's `node` program (`findServerProgram` without dependency bins), else
  Nitro's entry; `.output` is packaged with it.
- **Shared Rolldown** (`vitePackages`): every Vite build host shares `rolldown`
  and its entry points with run-time modules, as it shares `vite` — Nuxt loads
  its own modules at run time and they import `rolldown/utils`, which must be
  og's facade not the native package. See [[vite-and-rolldown-facade]].
- jshost run-time: a file Node loads as ESM (`.mjs`, or `.js` under
  `"type":"module"`) imports CommonJS in Node mode and resolves with import
  conditions, through `og.esmRequire` (caniuse-api, `@colordx/core`).
  `process.stdout`/`stderr` write to the host output directly, not through
  `console.log` — consola wraps both and logged through stdout's saved `write`,
  recursing until stack overflow (the build took 15 min to fail; now 76 s).
- nodeshim: built-ins have no `default` property (override modules had inherited
  unenv's `default`; mlly read `fs` through it); `fs.cp`/`cpSync`/`promises.cp`;
  `fs.readlink`.
- webapi: `Intl.Collator` over `golang.org/x/text/collate` and
  `String.prototype.localeCompare` through it (Nuxt's route sorting uses
  `new Intl.Collator("en-US")`); `Number`/`BigInt`/`Date` locale methods format
  through Intl; DateTimeFormat puts U+202F before AM/PM as CLDR/Node 24 do; the
  `v` RegExp flag runs as `u` when the pattern has no set notation (cssnano-utils
  uses `/[A-Z]/v`).
- Tests: `TestServeNuxt`, `TestParseModuleRecord`,
  `TestFacadeChunkEmittedDuringBuild`, `TestRuntimeModules`,
  `TestBuiltinsHaveNoDefault`, `TestFsCp`, `TestStdoutBypassesConsole`,
  `TestIntlNumberDate`.
- Difference: pages/payloads differ in chunk and CSS asset names (`WWPSRFP5.css`
  where Rolldown writes `error-404.css`) and timestamps. Nitro prints
  "imported from external module but never used" because esbuild keeps unused
  named imports of externals in SSR output.

### SolidStart 2, Nitro 3 (M9 part 14)

- `solid-start` fixture (basic template plus a `query` "use server" on the home
  route, an `action` with a form on `/about`, `api/hello.ts`, a `.brand` rule).
  Plain Vite build with a server program (no detector). Start script
  `node .output/server/index.mjs`.
- Critical bug — **server function bodies shipped to the client and ran in the
  browser.** `node:path` had been unenv's pathe, which turns every backslash
  into a separator and aliases `win32` to posix. Rollup's `createFilter` (used
  by SolidStart's server-function compiler) escapes regex chars in the project
  path (`solid\-start`) then `posix.join`s the pattern; pathe made it
  `solid/-start`, so no file matched and the "use server" transform never ran.
  Any project whose path has a `-` (or `.`, `+`, ...) hits this. Fix:
  **`node:path` is Node's own `lib/path.js`** (v24.15.0, vendored into
  `runtime/nodeshim/vendor/node`); `path/posix` and `path/win32` are its
  `posix`/`win32`. Only the browser check caught this (neither curl comparison
  nor SSR could — SSR runs the function server-side either way).
- Other Node internals vendored: Node's web stream adapters
  (`lib/internal/webstreams/adapters.js`) wired in because readable-stream
  leaves `Readable.toWeb`/`fromWeb` etc. empty, and srvx (Nitro 3, h3 v2) reads
  every request body through `Readable.toWeb(req)`. Shims for Node internals
  (`js/node_internals.js`, `js/node_stream_internals.js`: primordials Proxy,
  validators with Node's messages). `IncomingMessage` subclass deletes unenv's
  `readable=false`/`complete=true` class fields that shadowed readable-stream's
  getters (every request read as not readable; `request.clone()` threw "Body
  has already been consumed"). `Headers`/`URLSearchParams`/`Request` copy a
  source through a brand check (`#list in init`), not `instanceof`, so srvx's
  Node request headers are accepted.
- `fs.globSync`/`glob`/`promises.glob` (Node 22): SolidStart finds its app
  entry with `globSync(".../app.{j,t}sx")`. Non-code imports at build time
  (`.html`, `.md`, `.txt`, `.yml`, `.map`, `.gyp`, `.cc`, `.h`) load as empty
  modules. Server program choice (`scripts.go`): a `node <file>` command in
  preview/start/serve scripts wins over a dependency bin, and bins of packages
  whose preview og implements (`ownPreviews`: vite, astro) are never server
  programs (SolidStart lists vite under dependencies, so `vite preview` won
  before).
- Tests: `TestServeSolidStart`, `TestFsGlob`, `TestFindServerProgramPrefersNode`,
  `TestPathIsNodes`, `TestWebStreamAdapters`, `TestIncomingMessageReadable`,
  `TestInitFromLookalikes` (each against Node 24).
- Difference: the inline hydration script differs because seroval serializes
  functions by their source, carrying esbuild's formatting (`resolve2`); on
  `/?q=...` seroval's entry order matches Node's first cold request (og runs
  every request cold).

### Qwik City 1 (M9 part 15; Vite 5-7 support)

- `qwik-city` fixture (`create-qwik` base starter, empty-starter routes,
  node-server adapter, plus `routeLoader$` reading the query, a `routeAction$`
  form on `/about/`, a resumable `useSignal` counter, `useStylesScoped$`, an
  endpoint, Qwik City `<Link>`s). Qwik 1 (1.20.2) requires Vite `>=5 <8`.
- **Vite 5-7 support** (`sharedVite` in `vite.go`, used by vite/astro/nuxt
  hosts): for an installed Vite below 8, only `vite` is shared, not the facade's
  `rolldown/*` entry points (they do not exist). Those Vites bundle with real
  Rollup (`@rollup/wasm-node`, substituted) and the esbuild npm package (og's
  shim). Fixture `vite7-react` (Vite 7.3.7, @vitejs/plugin-react 5.2.0);
  `checkReactSPA` skips the two Vite 8-only traits (a modulepreload link,
  `import.meta.url` in the preload helper), which Node's Vite 7 build also lacks.
- **`qwik build`** (`scripts.go` `qwikBuildScripts`): read as the scripts the
  CLI runs (prebuild.*, build.client, build.lib, build.server or build.preview,
  build.static, postbuild.*).
- Globals `MessageEvent`, `MessageChannel`/`MessagePort`, `BroadcastChannel`
  (`runtime/webapi/js/src/messaging.ts`): undici (imported by Qwik City's Vite
  plugin) reads `MessagePort` on load.
- `require` failures are catchable (`require.go`): a `.node` addon throws Node's
  `ERR_DLOPEN_FAILED` (Qwik's optimizer catches it and falls back to its wasm
  build); an unparseable module throws `SyntaxError`; `MODULE_NOT_FOUND` is a
  plain `Error` (before, the first two were Go panics).
- Computed `import(x)`/`require(x)` resolve from their file (`jshost.go`
  `localizeImports`, `og.importFrom`): a bundled call had no file so
  `"../bindings/qwik.wasm.mjs"` and `"./node.cjs"` failed.
- WebAssembly externref and table growth (wasm-bindgen, Qwik's optimizer): og
  adds exported table accessors (`withTableAccessors`) and `WebAssembly.Table`
  gains `set`/`grow`. See [[wasm-on-wazero]].
- Build-time workers refined: workers end when idle (Node ends an idle worker;
  `parentPort` holds it while it has "message" listeners and is not unref'd);
  messages before a worker's listener wait in a `parentPort` queue (Qwik's SSG
  worker listens after `await createSystem(...)`); unreferenced workers count as
  weak handles (`engine.Loop.Weak`); a worker's file is evaluated whatever
  `"sideEffects"` says (esbuild had dropped the bare import of Qwik City's
  `"sideEffects": false` package, so every SSG worker ran an empty module).
- Tests: `TestServeQwikCity`, `TestBuildVite7SPA`, `TestQwikBuildInvocations`,
  `TestMessaging`, `TestRequireFailuresAreCatchable`, `TestComputedImportFromFile`,
  `TestExternref`, `TestUnrefWorkerReply`, `TestWorkerLateListener`,
  `TestWorkerExitsWhenIdle`, `TestWorkerSideEffectFreePackage`.
- Pitfall: `vite-imagetools` cannot load sharp at build time (og substitutes
  sharp only in packaged apps), so Qwik City prints "image imports are not
  available" (open).

## Files

- `internal/frameworks/vite/astro.go` — Astro detection and build.
- `internal/frameworks/vite/nuxt.go` — Nuxt detection and build (`@nuxt/kit`).
- `internal/frameworks/vite/vite.go` — `sharedVite`/`vitePackages`, Vite major
  gating (5-7 vs 8).
- `internal/frameworks/vite/scripts.go` — `qwikBuildScripts`, server-program
  choice (`ownPreviews`, node-first).
- `internal/jshost/esbuildjs.go` — esbuild npm-package shim over og's esbuild.
- `internal/jshost/workers.go`, `runtime/nodeshim/js/worker_threads.js` —
  build-time `worker_threads`.
- `internal/jshost/jshost.go` — `functionImport` rewrite, `localizeImports`,
  `SharedModules`, `TopLevelAwait`, `markESM`/`HonorESM`.
- `runtime/nodeshim/vendor/node` — vendored Node `lib/path.js`,
  web stream adapters, internals (`scripts/vendor-js.sh`).
- `runtime/nodeshim/js/http.js` — `IncomingMessage` subclass.
- `runtime/nodeshim/js/fs.js` — `globSync`/`glob`, fd I/O, `cp`, `readlink`.
- `runtime/webapi/js/src/intl.ts`, `messaging.ts`, `polyfills.ts`,
  `regexp.ts` — Intl groups, messaging globals, GetMethod boxing, RegExp.
- `internal/jshost/js/napi-rs-wasi.js`, `internal/wasm` — satteri/emnapi, Shiki,
  externref tables (see [[wasm-on-wazero]]).

## Test Coverage

Each ecosystem has an e2e test (`TestBuild*` for static, `TestServe*` for SSR)
plus focused unit tests listed per section above. They live under
`internal/frameworks/vite` and `internal/jshost`; they skip when the fixture's
`node_modules` is absent (`go run ./cmd/og install <dir>`). SSR tests run
through `serveWithPort`. The gate runs without `-race` (it found nothing on the
fixture suites and cost 10-25 min; full gate is about 3-5 min). Every SSR
fixture is also compared path-for-path against a real Node build and verified
in a browser against a packaged binary (hydration, scoped CSS, client
navigation, server actions/RPC).

## Pitfalls

- **Structural differences from Rolldown/Rollup are expected and benign.** og
  builds with esbuild, so output differs from a native build in: chunk names and
  the split graph; CSS asset names (`WWPSRFP5.css` vs `error-404.css`); minifier
  identifier naming; and inlined function source where esbuild renamed
  identifiers (`storageKey2` in React Router's `ScrollRestoration`, `resolve2`
  in SolidStart's seroval hydration script). Behavior matches; the bytes do not.
  Comparisons normalize chunk hashes, instance ids and symbol hashes.
- **A curl/SSR comparison only proves og matches Node, not that the fixture is
  correct, and not that client-only behavior is right.** The h3 fixture served
  escaped HTML on both og and Node (`html()` escaped a plain string — fixed with
  `html(raw(...))`). SolidStart's "use server" bodies ran in the browser and
  only the browser check caught it. Always add a browser check for hydration,
  server actions and anything client-side.
- **`manualChunks`/`advancedChunks`/`codeSplitting` groups are ignored** (with a
  warning) because esbuild cannot assign modules to named chunks. See
  [[vite-and-rolldown-facade]].
- Build-time `worker_threads` is a real feature several frameworks depend on
  (terser pool, SvelteKit prerender, Qwik SSG); its idle-exit and
  message-queue semantics are subtle (see Qwik section).
- `vite-imagetools` with Qwik City cannot load sharp at build time (open).
- A server program prints its startup banner on every request (open).
