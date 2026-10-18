# Oganesson architecture

Oganesson compiles a Next.js App Router project into one executable that serves it, with no Node.js at build time or at runtime. This document is the canonical design reference. The history behind each part is in `.agents/docs/JOURNAL.md`.

## Building blocks

| Component | Role |
|-----------|------|
| [esbuild](https://github.com/evanw/esbuild) (Go API) | Every transform and bundle: server layers, browser build, CSS, and on-demand CJS conversion at build time. |
| [Sobek](https://github.com/grafana/sobek) (Grafana's fork of Goja) + goja_nodejs's `require` | The JavaScript engine, both in the produced binary and in the build-time host. goja_nodejs's CommonJS loader is vendored into `third_party/goja_nodejs` (`scripts/vendor-goja-nodejs.sh`), rewritten for Sobek. |
| [TypeScript-Go](https://github.com/microsoft/TypeScript) | `og check` and tsconfig semantics, reached through the `_tsoverlay/ogapi` facade, which is installed inside `third_party/typescript` because TS-Go's packages are `internal/`. |
| Next.js itself | Its entry templates (`edge-ssr-app`, `app-page`, `edge-app-route`, `app-route`, `middleware`), `loadConfig`, `getDefineEnv`, app-render, and the client router are used as shipped and patched only where necessary (`internal/frameworks/next/patches.go`). |
| unenv, pathe, web-streams-polyfill, Node's own `lib/` files and bundled minimatch | Vendored JS (`scripts/vendor-js.sh`) for the Node built-in surface and Web Streams; `node:path` (with `matchesGlob` on Node's minimatch) and the web stream adapters (`Readable.toWeb` and friends) are Node's own files, pinned to a Node release. |

Sobek lacks async generators, ESM (as og uses it), the RegExp `v` and `d` flags, `Intl`, and `WeakRef`; it has Unicode property escapes. `internal/gojatarget` (named for Goja, which Sobek forks) holds the esbuild `Supported` table every engine-bound bundle is lowered with, and the source fixups for parser and runtime bugs Sobek shares with Goja (keyword-named class fields, `arguments` read only by nested arrow functions, and thenable jobs that drop the async context: `then` methods become context-capturing accessors). It also sets the UTF-8 charset, which avoids a parser panic on mixed escapes.

## Repository layout

| Path | Contents |
|------|----------|
| `cmd/og` | CLI: `install`, `check`, `build`, `serve`, `compat` (the compatibility corpus runner: install, build, serve and crawl apps, each a child `og` process), `licenses`, `version`. A Oganesson executable with an appended payload acts as that app's server. |
| `internal/install` | npm-free package installer (registry tarballs, integrity checks, no lifecycle scripts). |
| `internal/jshost` | Build-time Sobek host: next.config loading, middleware matchers, the esbuild JS plugin bridge, the webpack loader shim, `require` (a computed `require(x)` / `import(x)` in a bundled module resolves a relative `x` from that module's file; a `.node` addon throws Node's `ERR_DLOPEN_FAILED`; sharp and better-sqlite3 are og's Go-backed substitutes, as in packaged apps), and `worker_threads` workers (each its own engine and goroutine; a worker ends when its loop is idle, its `parentPort` holding it while it has message listeners; build-time writes, temp files included via a project-local TMPDIR, stay inside the project). |
| `internal/frameworks/next` | The Next.js compiler: app tree scan, layers, RSC transforms, server actions, the browser build, CSS, manifests, the entry dispatcher. next.config's `webpack()` hook runs against a stand-in webpack config; the loaders, aliases, fallbacks and definitions it adds are applied (`webpackhook.go`). |
| `internal/frameworks/vite` | Vite builds: Vite's own JS (config, builder, plugins) on the build host, with Rolldown replaced by a facade over esbuild (see "Vite builds"). |
| `internal/wasm` | The WebAssembly JS API for build-time Sobek, on wazero (threads, WASI), so WebAssembly builds of native addons (Tailwind's oxide, lightningcss, Qwik's optimizer) run unmodified. Tables are reached through accessor functions og adds to the module (wazero exposes none); externref values map to JS values. |
| `internal/gojatarget`, `internal/bundle` | Shared esbuild settings for Sobek; RSC helpers. |
| `runtime/engine` | One Sobek VM and its event loop, plus AsyncLocalStorage on Sobek's `AsyncContextTracker` (Sobek's API). |
| `runtime/webapi` | WinterCG Web APIs: Go hosts (fetch, URL, encoding, crypto, server) and a TypeScript prelude (`js/src`, generated into `prelude.js`). The prelude also supplies `Intl` (vendored FormatJS, loaded lazily) and a `RegExp` that adds the `v` flag (its set notation translated to `u` patterns, `js/src/unicodesets.ts`; properties of strings from Go tables generated from Unicode's emoji data, `emoji_tables.go`) and the `d` flag, which Sobek lacks. |
| `runtime/nodeshim` | Node built-ins as an unenv preset with Oganesson overrides; `stream` and `string_decoder` are the vendored npm copies of Node's own (unenv's are stubs); `path`, the web stream adapters and `fs.ReadStream` / `WriteStream` are Node's `lib/` files, their internal requires (primordials, validators, errors) shimmed by `js/node_internals.js` and `js/node_stream_internals.js`; `http` runs servers inside a request's VM; `net` / `tls` are client sockets over Go connections (`net.go`; chains verified in Go with OpenSSL's codes, identity by Node's `checkServerIdentity`), through a process-wide pool that keeps PostgreSQL and MySQL sessions across request VMs at the protocol level (`netpool.go`: handshakes recorded and replayed, sessions reset between clients); zlib is Go's compress/* and andybalholm/brotli; `assert` is the callable function. |
| `runtime/codec` | Streaming compressors / decompressors (Go's compress/*, andybalholm/brotli) for `CompressionStream` and `node:zlib` streams. |
| `runtime/vfs` | The runtime's `node:fs`: the files packaged with the app (a Next app's traced files, a server program's build outputs) under an in-memory writable layer (`Overlay`), process-wide, mounted at `/` (the runtime's cwd); SQLite database files live in it too. |
| `runtime/server` | `net/http` handler that runs each request in a fresh VM. |
| `runtime/isr` | ISR cache, middleware phase, and Next cache-handler bridge. |
| `runtime/kv` | `og:kv`, the built-in Deno-KV-compatible store. |
| `runtime/native` | User-defined Go functions as `og:<name>` modules: a registry (`native.Register`), JSON-style value conversion (`[]byte` as `Uint8Array`, `time.Time` as `Date`), synchronous calls on the loop and asynchronous ones on goroutines as Promises, a `context.Context` cancelled with the request's engine. |
| `internal/nativegen` | Scans a project's `og/` Go module (one package per `og:<name>`; `//og:name`, `//og:async`, `//og:sync`, `//og:skip`) into `og-env.d.ts` declarations and the registration glue that `og build -o` compiles, with the local Go toolchain, into the app binary (`cmd/og/native.go`; static pages then prerender in that binary) and `-eject` writes as `natives_gen.go`. |
| `runtime/natives` | Substitutes for npm packages with native addons, from one registry: bcrypt → bcryptjs (installed and aliased), sharp → a Go-backed subset (`og:sharp`), better-sqlite3 → SQLite in Go (`og:better-sqlite3`, modernc.org/sqlite, shared across requests). |
| `runtime/imageopt` | Image sniffing, sizing, and resize/re-encode (Go codecs plus `gen2brain/webp`), and the `/_next/image` optimizer (a port of Next's image-optimizer request path). |
| `runtime/notices` | The license texts of Oganesson and everything linked or embedded into og (`THIRD_PARTY_NOTICES.txt`, generated by `scripts/collect-notices.sh`, checked in), printed by `og licenses`, app binaries' and ejected modules' `-licenses` (followed by the app's own, the payload's `THIRD_PARTY_NOTICES.txt`). |
| `internal/licenses` | Build-time collector of the npm packages an app ships: every `node_modules` input in the metafiles of the shipped esbuild bundles (Next server, browser, pages browser and CSS builds; the Vite facade, server entry and server program), attributed to the nearest named `package.json` (so `next/dist/compiled/*` copies count as themselves), rendered with their license files into the payload. |
| `runtime/app` | Assembles a built app (`.og/` or payload) into an `http.Handler`: the image optimizer, static files, ISR, prerender seeding. |
| `pack` | Payload format: zip appended to the executable with a `OGPAYLD1` trailer. |
| `testdata/apps/next-hello` | The feature fixture; `testdata/plugins/*` cover JS plugins. `next-pages`, `next-mixed`, `next-i18n` cover the Pages Router; `vite-react`, `vite-features`, `vite-vue`, `vite-svelte`, `vite-tailwind`, `vite-preact`, `vite-solid`, `vite-unocss`, `vite-pwa`, `vite-ssr-express` / `-fastify` / `-koa` / `-hono` / `-h3`, `tanstack-router`, `tanstack-start`, `tanstack-start-rsc`, `astro-static`, `astro-node`, `react-router`, `sveltekit-node`, `nuxt`, `solid-start`, `qwik-city` and (Vite 7) `vite7-react` cover Vite. |
| `spikes/` | Two retained feasibility spikes referenced by shipped code for provenance (new experiments go under `.agents-workspace/tmp/spikes`). |

## Build pipeline (`og build`)

1. **Configuration.**
   - `internal/jshost` evaluates the user's next.config in Sobek and normalizes it through Next's own `loadConfig`.
   - It computes Next's edge and client `define`s with `getDefineEnv`.
   - If a `og.config.*` exists, a long-lived plugin host starts (see Extensibility).
2. **Tree scan.** The app directory becomes Next's loader trees (layouts, pages, boundaries, route handlers) plus the route list.
3. **Server build (pass 1).** All routes compile into one bundle that runs Next's **edge templates**: Next's and React's own server code resolves with edge conditions, while the app's packages resolve with Node's conditions as on `next start`'s Node runtime (`jshost.ResolveNode`). TypeScript files with legacy decorators are transpiled by TypeScript-Go when tsconfig sets `emitDecoratorMetadata`.
   - The webpack layers become esbuild namespaces: `rsc` (react-server conditions), `ssr`, and `shared`.
   - A `"use client"` module becomes a `registerClientReference` proxy in `rsc`.
   - A `"use server"` module becomes action references whose ids are `"7f"` + sha1.
   - Inline `"use server"` functions in server modules are hoisted as next-swc does (`inline_actions.go`, scope analysis by `ogapi.ServerActions` on TypeScript-Go's binder): each becomes an exported, registered `$$RSC_SERVER_ACTION_<n>`, and the values it closes over (narrowed to the property paths it reads) are bound arguments, encrypted with Next's `encryptActionBoundArgs` where it is defined and decrypted when it runs.
   - Node built-ins resolve to `runtime/nodeshim`; `og:*` modules resolve to their runtime implementations.
   - The metafile records the server import graph.
4. **Browser build.** Its entry points are the client components found in pass 1.
   - It uses the CommonJS `next/dist` builds; `cjsTwin` maps server-side module paths to their browser counterparts.
   - A small `__webpack_require__` runtime shim lets Next's client router and `react-server-dom-webpack/client` run unmodified.
5. **CSS and manifests.**
   - CSS sources compile individually, after Next's PostCSS pipeline (`lazyPostCSS`, run in the build host), and CSS Modules get deterministic hashed class names shared by both builds.
   - `entryCSSFiles` comes from walking the server graph, crossing into the browser graph at client modules.
   - The `self.__RSC_MANIFEST`, `__RSC_SERVER_MANIFEST`, `__BUILD_MANIFEST`, and related manifests are emitted.
6. **Server build (pass 2).** The server bundle is rebuilt with the manifests inlined. Output goes to `.og/`: `server.js`, static files, `public/`, and metadata.
7. **Prerender.** Static routes, including `generateStaticParams` results, render through the same runtime into `prerender.json`, which also lists the paths that turned out dynamic so the runtime never attempts to render them statically.
8. **Licenses.** The npm packages the shipped bundles read (`internal/licenses`, from esbuild metafiles) go into the payload as `THIRD_PARTY_NOTICES.txt`, which the binary prints after og's own notices with `-licenses`.
9. **Packaging (`-o`).** The Oganesson executable is copied, the `.og/` content is appended as a zip with a `OGPAYLD1` trailer, and the result is the app binary.

## Vite builds (`internal/frameworks/vite`)

`og build` takes the Vite path for a project with a `vite.config.*`, a `vite`
dependency, or a recognized Vite-based framework dependency and no Next.js.

- **Vite's own code runs the build.** Vite 8 is bundled into the build host (`internal/jshost`) with the project's `vite.config.*`, and `createBuilder().buildApp()` runs as `vite build` would. That covers config resolution, environments, `buildApp` orchestration, and Vite's and the project's plugins.
- **Rolldown, Vite's Rust bundler, is replaced** (its WASI build needs threads Sobek lacks):
  - **The binding stub** (`js/binding-stub.js`): Rolldown's native binding module, whose housekeeping calls are no-ops and whose builtin plugins keep their options. `rolldown/utils`' transform and minify (Vite's `transformWithOxc`, used by plugin-vue) run on esbuild.
  - **The `rolldown()` facade** (`js/rolldown.js`, Go side `esbuild.go`):
    - esbuild bundles. Every resolution and load goes through a Rollup-compatible plugin container: hook order, hook filters, plugin contexts, `emitFile`.
    - The output is a Rollup `OutputBundle` built from esbuild's metafile, for `renderChunk` / `generateBundle` / `writeBundle`. Per-module `renderedExports` are the module's declared exports, following `export *` through the build's resolutions (esbuild reports none).
    - `this.resolve` follows Rollup's skip chain: a plugin is skipped for the same source and importer anywhere below its own call, so plugins that resolve through one another terminate.
    - Rolldown file-URL references (`import.meta.ROLLDOWN_FILE_URL_<ref>`, for workers and assets) are resolved per chunk through `resolveFileUrl` hooks before `renderChunk`.
    - Modules that render no code (CSS after `vite:css-post`) are kept in their importer's chunk, as Rollup lists them.
    - Chunks are grouped by esbuild's code splitting (coarser than Rolldown's per-entry tree-shaking) but named by Rolldown's rules: an entry by its input name, a dynamic entry by its module's representative name, a shared chunk by its last-executed module's (`index` / `mod` files by their directory), each group of `manualChunks` / `advancedChunks` / `codeSplitting` naming a chunk whose modules all belong to it. Groups do not move modules between chunks.
    - Imports of external modules keep only the bindings the chunk uses (a bare import when none is), as Rolldown prints them; esbuild prints them as written, and the chunk's source map is shifted to match.
    - Module side effects: an id a plugin resolves gets its package's `"sideEffects"` (package.json, globs included) as Rolldown's resolver gives it; esbuild reads the field only for paths it resolves itself.
    - Source maps: each module's load and transform maps are composed (@jridgewell/remapping, as Vite's combineSourcemaps) and handed to esbuild as the module's input map; output maps are written as Rollup writes them (`sourcemap: true | "hidden" | "inline"`).
    - `OG_VITE_TRACE=1` logs every plugin hook call.
  - **Rolldown's builtins:**
    - resolve: esbuild's resolver with Vite's options, root-relative ids and `/@fs/`;
    - TS/JSX transform: esbuild, in plugin order, with the nearest `tsconfig.json` as TypeScript-Go resolves it (`extends`);
    - build manifest (`build.manifest`): Vite 7's JS manifest logic;
    - JSON, load fallback, module-preload polyfill;
    - build import analysis: the `__vitePreload` wrapping of dynamic imports, whose dependency lists Vite's JS fills in;
    - alias, `import.meta.glob`, dynamic-import variables: Vite's own JS implementations, kept by load-time patches of Vite.
  - **`rolldown/parseAst`** is acorn (vendored).
- **Vite's source is patched while it is bundled** (`host.go`): those `applyToEnvironment` switches, and `fn.toString()` of Vite's browser helpers replaced by their original source text (bundling for Sobek rewrites `import.meta`).
- **Server environments** (e.g. TanStack Start's `ssr`): the facade builds them split as Rolldown does (modules shared by entries are one instance), with Node built-ins and og's own modules (`og:kv`) external. Object- and array-valued defines become `globalThis.__og_define_<n>` set by each chunk's banner, which avoids an esbuild renamer race in split unminified builds. `vite.Build` takes the first server consumer's written entry, whose default export has `fetch(request)`, and bundles it for Sobek (`server.go`):
  - Node built-ins come from `runtime/nodeshim`;
  - esbuild's ESM `__require("x")` shim calls in the output become plain `require("x")` again, so they resolve statically.
- **The project's scripts** (`scripts.go`): `og build` runs the `vite build` commands of the `build` script (through `npm run` / pnpm / yarn chains, with their CLI flags merged over the config as Vite merges inline config, and `createBuilder(…, null)` as the CLI passes).
- **Server programs** (`program.go`): when the build runs `vite build --ssr` and the `preview` / `start` / `serve` script runs `node <file>` (Express, as create-vite-extra's SSR templates), that program is the server:
  - it is bundled for Sobek as ESM wrapped in an async function (it uses top-level await); dynamically imported devDependencies (the dev branch's `import("vite")`) become modules that throw;
  - the build outputs are packaged under `files/` and mounted read-only at `/`, the runtime's cwd (`runtime/vfs`), so `./dist/client/index.html` and sirv's reads work;
  - per request the program runs in the request's VM; `http.createServer(...).listen()` opens nothing, and the request is handed to its listener as `IncomingMessage` / `ServerResponse` whose body streams into the web Response (`runtime/nodeshim/js/http.js`);
  - the program serves every request itself, static files included;
  - each project file's `import.meta.url` is its URL in that layout (`file:///dist/server/entry.mjs`), so programs find their files from it; packages og implements natively (`sharp`) are og's modules, and a dynamic import of a package that is not installed throws only when reached;
  - the script may run a dependency's bin with arguments (`react-router-serve ./build/server/index.js`): the glue sets `process.argv`, and arguments naming project modules are bundled too and found by the program's computed `import()` of their file URL;
  - a server environment that wrote an entry is served by the start script's program when there is one;
  - Express, Fastify, Koa and Hono's node adapter are covered (`testdata/apps/vite-ssr-*`).
- **Nuxt** (`nuxt.go`): what `nuxi build` runs (`@nuxt/kit`'s `loadNuxt`, `writeTypes`, `buildNuxt`) on the build host; Nitro builds the server with Rollup's WebAssembly build and prerenders; `node .output/server/index.mjs` is the server program, `.output` packaged with it.
- **Vite 5 to 7** bundle with Rollup (its WebAssembly build, substituted for the native one) and the esbuild npm package (og's esbuild); og's Rolldown facade and the shared `rolldown/*` entry points apply to Vite 8 only (`sharedVite`, by the installed Vite's major).
- **Qwik City** (no detector): `qwik build` is read as the scripts it runs (prebuild.*, build.client, build.lib, build.server or build.preview, build.static, postbuild.*); `qwik check-client` adds nothing after build.client. The node-server adapter's `server/entry.node-server.js` is the server program. Qwik's optimizer falls back to its wasm-bindgen build when its native binding fails to load.
- **SolidStart** (no detector): a plain `vite build` with SolidStart's plugins and Nitro 3 (`nitro/vite`); the start script's `node .output/server/index.mjs` is the server program. A `node <file>` script wins over any dependency bin, and the bins of packages whose preview og implements (`vite`, `astro`) are never server programs.
- **SvelteKit** (adapter-node): Kit's post-build tasks (route analysis, prerendering, fallback) run in `worker_threads` workers, which reload the Vite config; build-time workers are bundled and set up like the build host (shared packages, the Rolldown facade). `node build` is the server program; its directory (`build/`, static files and prerendered pages) is packaged with it.
- **React Router** (framework mode): `react-router build` is Vite's builder, so og's `vite build` runs it; prerendering fetches from og's own serving of the server program (`startProgramPreview`, a load-time patch of its `startPreviewServer`).
- **Astro** (`astro.go`): `astro build()` runs on the build host with the project's `astro.config.*` bundled into the host entry (`configFile: false`). Static output is served as a multi-page app (`/about` is `about/index.html`); with `@astrojs/node` standalone, `dist/server/entry.mjs` is a server program as above.
- **VitePress** (`vitepress.go`): a declared `vitepress` dependency selects the Vite path even without a direct `vite` dependency. VitePress's own `build()` API runs on the build host, performs its client/server Vite builds and static page rendering, and its configured output becomes a multi-page static app. Unresolved virtual IDs such as `@siteData` continue through the plugin chain before the SSR unresolved-import fallback can externalize them; advanced chunk-name callbacks receive the Rolldown chunk context VitePress uses to inspect module graphs.
- **SSR externals**: server environments keep bare imports of `node_modules` packages as imports (Vite's SSR externalization, with `ssr.noExternal` / `external` matched by package name); the server bundle resolves them.
- **Preview server** (`preview.go`): tools that render through `vite preview` get og's own serving instead: the server entry bundled for Sobek and the client output, behind `runtime/app` on a local port. TanStack Start's prerendering and SPA shell use it (a load-time patch of its `startPreviewServer`), so what is prerendered is what the binary renders.
- **TanStack Start:**
  - three environments with the RSC variant (`rsc`, then `client`, then `ssr`; `@vitejs/plugin-rsc` orchestrates them); the `ssr` output imports the `rsc` output, and both end up in `server.js`;
  - SPA mode: the prerendered `_shell.html` is a static file, and pages are rendered as Start's own server renders them (`TSS_SHELL` is set only for the shell's prerender, never at run time).
- **Runtime:** a Vite app's payload is `meta.json` (`framework: "vite"`, `appType`, base, env), `client/` (the client environment's output) and, with a server, `server.js`. `runtime/app` serves:
  - client files first, then prerendered pages (`p/index.html`, `p.html`) for GET and HEAD;
  - then the server's `fetch` through the per-request VM handler (no Next ISR layer);
  - or, without a server, `vite preview`'s HTML fallback (the SPA's `index.html`) and an empty 404.

  Hashed `assets/` files are immutable.

## Runtime

- **Per-request isolation.** `runtime/server` compiles the bundle once per process. Each request then gets a **fresh Sobek VM**: the Web APIs are installed, the program runs, and a Request is dispatched to the bundle's entry. Module-level state never survives a request. Measured costs are roughly 670 ms once for compilation and roughly 14 ms per request for VM setup.
- **Shared state lives in Go.** The only cross-request state is in Go-backed services, injected through `Handler.Extensions`:
  - `og:kv` (one in-memory store per process);
  - the ISR cache (`og:cache-handler`, backing Next's cache handler).
- **Entry dispatcher** (`internal/frameworks/next/virtual.go`): routes a request to the page, route-handler, or server-action entry. Route params are passed in Next's `nxtP` query form.
- **ISR and middleware** (`runtime/isr`). The handler in front of the app:
  1. strips every incoming `x-og-*` header;
  2. runs the middleware phase (redirects, rewrites, cookies);
  3. serves the cache as HIT, STALE (with background revalidation), or MISS.
  - Static renders are requested with `x-og-static`, which turns off `supportsDynamicResponse` in Next's template.
  - Cache metadata (revalidate, tags) comes back in internal response headers.
  - One static render produces both the HTML entry and the RSC entry.
- **Static assets.** `/_next/static/**` is served immutable, and `public/` as is.
- **WebAssembly.** Request engines get the standard `WebAssembly` API (`internal/wasm` in restricted mode: no WASI or host access; the wazero runtime is created on first use), e.g. for `next/og`'s resvg and yoga.
- **Images.** `/_next/image` (`runtime/imageopt`) is served in front of the app. It fetches local images from the app itself and remote ones under `images.remotePatterns` with Next's private-IP guard, and caches results in memory.

## Build-time JavaScript host (`internal/jshost`)

Build tooling expects Node, and jshost provides a Node-like Sobek environment for it:

- Bundling uses esbuild with condition `node`.
- Dynamic `require` (and computed `import()`) is goja_nodejs/require extended with:
  - package `exports` and `imports` (`#name`), TS/ESM sources (converted with esbuild), and the nodeshim built-ins;
  - ES module semantics the conversion would lose: a module's own `const __dirname` shadows the wrapper's, and a default import of a converted ES module gets its default export (`markESM`);
  - shared modules: a package the bundle contains (`Host.Shared`: the Vite host shares `vite` and its config files' imports) is that instance when required at run time, by name or by its file;
  - `Host.TopLevelAwait`: bundles may contain modules with top-level await (ESM run in an async function, awaited by `EvalWith`);
  - `Host.Patch`, the same source patches bundled code gets.
- Native addons cannot load, but their WebAssembly builds can:
  - `internal/wasm` provides `WebAssembly` on wazero, with shared memories, WASI preview1 (the host filesystem, read-only) and wasi-threads (goroutines). JS imports are main-thread only.
  - Node-API comes from the vendored napi-wasm.
  - `nativeSubstitute` swaps napi-rs `*.wasi.cjs` loaders for Oganesson's loader, and points lightningcss at `lightningcss-wasm`.
  - The installer fetches `*wasm32*` optional dependencies (`cpu: wasm32`) and these substitutes.
- `worker_threads` workers run in their own engines: their files are bundled like the host's code, the parent's engine setup applies, messages are structured clones (cycles and shared references kept), the `env` option and `process.exit` work, and an unhandled rejection is the worker's `error` (exit 1).
- Built-ins come from unenv, with Go-backed overrides:
  - `fs`: reads are unrestricted, writes are confined to the project directory;
  - `zlib`, `crypto`, `module`, `process`, and `async_hooks`;
  - `events`: unenv's emitter made callable (`EventEmitter.call(this)`), with `on` / `off` aliased to `addListener` / `removeListener` as in Node;
  - `util.debuglog` honors `NODE_DEBUG` (a load-time rewrite).
- The Next server build takes `buffer`, `stream` (readable-stream) and `string_decoder` from Next's compiled polyfills, since unenv's `stream` and `StringDecoder` are stubs.
- `crypto` adds `createCipheriv` / `createDecipheriv` (AES GCM, CBC, CTR over Go) and secret `KeyObject`s (`createSecretKey`).
- `Error.captureStackTrace` honors `Error.prepareStackTrace` with V8 CallSite objects parsed from Sobek frames (depd, callsites).

## Extensibility (`og.config.*`)

```ts
export default {
  esbuild: {
    plugins: [],        // every JS build (server and browser)
    clientPlugins: [],  // browser build only
    serverPlugins: [],  // server build only
    cssPlugins: [],     // CSS compilation builds
  },
  webpack: { rules: [] }, // webpack-style loaders, e.g. babel-loader + styled-jsx/babel
};
```

- Plugins are standard esbuild JS-API plugins, run in the build-time host and adapted to the Go API.
- `import "esbuild"` inside plugins resolves to a shim.
- `onLoad` hooks for files are chained before Oganesson's own loaders, so a plugin's output still gets `"use client"` / `"use server"` processing.
- `webpack.rules` run through a loader-context shim covering what Babel-style loaders use.

## Using Oganesson

```sh
og install [dir]              # install package.json dependencies without npm
og check [dir]                # type-check with TypeScript-Go
og build [-o app] [dir]       # build into .og/ (and a single binary with -o)
og serve [-addr :3000] [dir]  # serve a built .og/
og licenses                   # licenses of og and everything it includes
og version                    # the Oganesson version this CLI corresponds to
./app [-addr :3000]             # a packaged app binary (PORT is honored; -licenses)
```

Built-in modules available to app code:

- `og:kv`, the Deno KV API: `openKv`, `get`, `getMany`, `list`, `set`, `delete`, `atomic` (with checks, `sum`/`min`/`max`), and `expireIn`. Memory only. Types are written by `og build` to `og-env.d.ts`.

Supported today:

- App Router pages, layouts, and boundaries;
- server and client components, with soft navigation;
- route handlers;
- server actions, module-level and inline closures (RPC, form, and no-JS);
- `proxy.ts` / `middleware.ts`;
- `generateStaticParams`, `revalidate`, `revalidatePath`, `revalidateTag`;
- global CSS, CSS Modules (with `composes`), Sass (Next's sass-loader and resolve-url-loader), PostCSS (the project's `postcss.config.*`, e.g. Tailwind v3 or v4, or Next's default flexbugs + preset-env pipeline), `experimental.useLightningcss` (on lightningcss-wasm), styled-jsx (via config), and esbuild JS plugins;
- `next/font` (google and local, through Next's own loaders; the `next_font` transform is ported over TypeScript-Go's parser via `ogapi.ParseModule`);
- `next/image` (static imports with blur placeholders, and the optimizer);
- metadata files (`icon`, `apple-icon`, `opengraph-image`, `twitter-image`, `favicon.ico`, `manifest`, `robots`, `sitemap`);
- Pages Router pages (in progress, M8): one Next `edge-ssr` template per page (`pages` / `pages-browser` layers); `getServerSideProps`, `getStaticProps` / `getStaticPaths`, `getInitialProps`, custom `_app` / `_document` / `404`, and `/_next/data`, with next start's response semantics patched in (`edgeSSRPagesPatches`); the browser build uses Next's pages client and a port of the `next_ssg` transform (`ssg.go`); `pages/api` through Next's `pages-edge-api` / `pages-api` templates on its req/res mocks (Node-style and edge handlers, body parser, streaming, `res.revalidate`); `getStaticPaths` fallback (`false`, `'blocking'`, and `true` with the uncached fallback shell); app and pages directories in one project (404s through the app's not-found, Next's conflict error); draft / preview mode (render.js reads preview cookies as in the Node runtime; the ISR layer bypasses its cache for a valid `__prerender_bypass`, configured by `meta.json` `cacheBypass`); i18n (locale prefixes resolved by the route module's `prepare`, the root locale redirect ahead of the cache, every locale prerendered with locale-prefixed data); an SSG render caches its HTML and `/_next/data` JSON together (ISR companion entries), keyed on the path only.

Not yet supported:

- AVIF image output;
- per-route code splitting;
- `Intl` locale data beyond English (Collator excepted);
- `"use cache"`.

See `.agents/docs/TODO.md`.

## Testing

`go test -race ./runtime/... ./internal/... ./pack/...` covers:

- the engine and Web APIs, including a pinned Web Platform Tests subset with an expectations file;
- KV and ISR;
- the build host;
- the whole Next pipeline end to end over `testdata/apps/next-hello`.

Fixture tests skip until fixtures are installed. Browser behavior is verified against packaged binaries. See `.agents/docs/TESTING.md`.
