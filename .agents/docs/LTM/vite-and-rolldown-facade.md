# Vite on a Rolldown-API Facade over esbuild

## Summary

Oganesson runs Vite's own JavaScript (config resolution, environments, the `buildApp` builder, and Vite's JS plugins) on `internal/jshost`, replacing only Rolldown with a facade (`og.rolldown`) implemented over Go esbuild. This mirrors the Next strategy of driving upstream code on esbuild rather than reimplementing it. The facade covers SPA (and MPA) builds served like `vite preview`; SSR server programs are [[server-programs-ssr]] and per-framework plugins are [[vite-framework-ecosystems]].

## Key Facts

- Vite 8.3.3 is built on Rolldown 1.2.12 (a Rust bundler with napi-rs bindings); esbuild is only an optional peer. The facade lives in `internal/frameworks/vite`.
- Running Rolldown's own wasm binding on wazero is not viable (emnapi threadsafe-functions need emnapi's JS runtime plus `SharedArrayBuffer`/`Atomics`, which Goja lacks). The esbuild facade is the chosen path. See [[wasm-on-wazero]].
- `og build` takes the Vite path when a project has a Vite config, a `vite`
  dependency, or a `vitepress` dependency and no Next.js (`isNextProject`).
  VitePress has a dedicated static adapter.
- Payload: `meta.json` (`framework: "vite"`, `appType`, `basePath`) plus `client/`. `app.FormatVersion` is 4. Runtime serving in `runtime/app/vite.go`.
- Vite 5-7 (below 8) bundle with real Rollup (`@rollup/wasm-node`) and the esbuild npm package (og's esbuild shim), not the facade.

## Details

### Why the facade, not Rolldown itself

`@rolldown/binding-wasm32-wasi` (11 MB) is `wasm32-wasip1-threads`: shared memory (`flags=3`), `wasi.thread-spawn`, and an emnapi loader built on `node:worker_threads`, `SharedArrayBuffer` and `Atomics` — none of which Goja has. A spike (`spikes/rolldown-wasi`, `.agents-workspace/tmp/rdspike`) loaded the binding through jshost's napi-rs WASI loader (`NAPI_RS_FORCE_WASI=error`). With logging stubs the module instantiates, but `napi_register_module_v1` fails with "Create Custom GC ThreadsafeFunction … failed": emnapi compiles threadsafe functions and async work into the wasm (C), depending on state created by emnapi's own JS runtime (`@emnapi/core`), which napi-wasm does not create. napi-wasm worked for Tailwind's oxide only because oxide uses neither. Running Rolldown would additionally need emnapi's runtime on Goja, thread-side `_emnapi_*` imports posted to the JS loop, Atomics over shared wasm memory, and proof that tokio + promise integration work. The user chose option (a): keep the esbuild facade.

### Facade architecture

Vite's own code does config, environments, the builder (`buildApp`, so e.g. TanStack Start's build orchestration runs as written) and its JS plugins. Oganesson replaces only Rolldown. Pieces:

- **The binding stub** (`js/binding-stub.js`): housekeeping calls are no-ops; builtin plugins keep `name` / `_options`; everything else throws a clear error.
- **The `rolldown` facade** (`js/rolldown.js`): `rolldown()` runs Go esbuild. A single esbuild plugin dispatches `resolveId` / `load` / `transform` to the plugin list through a Rollup-compatible plugin context, on jshost's `Loop.Post` bridge. Contains the plugin container (hook order, picomatch id filters, contexts for resolve / `emitFile` / `getFileName` / `getModuleInfo` / `load` / warn / error), the `options`/`outputOptions` and buildStart…buildEnd / renderStart / renderChunk / generateBundle / writeBundle / closeBundle hooks, the builtins, and the `OutputBundle` built from esbuild's metafile so `renderChunk`/`generateBundle`/`writeBundle` run as in Rolldown.
- **The builtins** are implemented by Oganesson (esbuild's resolver and transforms where they fit): resolve, load fallback, module-preload polyfill, TS/JSX transform, JSON, build-import-analysis.
- **`rolldown/parseAst` / `this.parse`**: acorn, vendored by `scripts/vendor-js.sh` (`js/parse.js`), JavaScript only.

The user's `vite.config.*` is bundled into the same host (so its `vite` / plugin imports get the same substitutions) and handed to Vite inline with `configFile: false`; a function config gets Vite's `ConfigEnv`.

### Build flow (`internal/frameworks/vite`)

- **`Build`** bundles a host entry (the project's `vite.config.*` plus Vite) and runs `createBuilder({ ...config, configFile: false }).buildApp()` on jshost (`EvalWith` installs `og.rolldown`), then reads `build.outDir`.
  - **`createBuilder(config, null)`** as the `vite build` CLI passes: without a `builder` config only the client environment is built. `false` built every environment, which broke plugin-vue's project in an `ssr` build of `index.html`. (Frameworks that set `builder` themselves are unaffected.)
- **`host.go`** holds the host-bundle esbuild plugins:
  - Substitutions: Rolldown's binding → `js/binding-stub.js`; `import "rolldown"` → `js/rolldown.js` (re-exports the real package as `og:rolldown-real`); Rolldown's parse module → `js/parse.js`; Vite's optional peers external.
  - Source patches of `vite/dist/node/chunks/node.js`: `applyToEnvironment` keeps the JS `import.meta.glob`, dynamic-import-vars and alias plugins in bundled environments; `preserveFunctionSources` restores the original source of top-level functions (Vite stringifies the preload helper, `detectScriptRel` etc. into browser code, and bundling for Goja had rewritten `import.meta` in them).
- **`esbuild.go`** (`og.rolldown`): `build(spec, hooks)` runs one esbuild build whose single plugin forwards every resolve/load to JS over the event loop (`Loop.Post`); Rollup ids map onto the `file` namespace (absolute paths) or `og-vite` (virtual ids, queries). Also `resolve` (esbuild's resolver for `builtin:vite-resolve` / `this.resolve`), `transform` (TS/JSX, also taking `define`, `target`, `minify`, preserved JSX, `withMap`), `dynamicImports` (TypeScript-Go, UTF-16 offsets), and `engines` (maps Rolldown targets like "chrome111" to esbuild engines).

### Facade gaps closed (feature fixtures)

- **`this.parse`**: acorn, JavaScript only.
- **`builtin:vite-manifest`**: Vite 8 has no JS manifest builder, so this ports Vite 7's `manifestPlugin`. The asset goes straight into the bundle because `native:manifest-compatible` reads it in the same `generateBundle` round.
- **File-URL references**: Vite 8 emits `import.meta.ROLLDOWN_FILE_URL_<ref>[_<urlId>]` for workers, `new URL(…, import.meta.url)` assets and `?url`; `Build.resolveFileUrls` replaces them per chunk before `renderChunk`, taking the first `resolveFileUrl` hook's expression (Vite's `vite:asset`), falling back to `new URL(rel, import.meta.url).href`.
- **`import.meta.url` in host files** (`internal/jshost`): the textual quote-aware replacer wrongly rewrote template text Vite generates (`` `new URL(${…}, import.meta.url)` ``), leaking `file:///…/vite/dist/node/chunks/node.js` into browser bundles. Fix: `replaceImportMetaURL` now replaces only real expressions found by `ogapi.ImportMetaURLs` (TypeScript-Go AST; the parser needs an absolute file name, so it parses under a synthetic one). (This supersedes the earlier string-skipping `replaceImportMetaURL`, which only skipped quoted occurrences.)
- **Source maps**: esbuild's external maps become `chunk.map` (a Rollup `SourceMap`) with `og-vite:` sources made relative to the map, written as Rollup writes them (a `.map` asset plus `sourceMappingURL`, `hidden`, or `inline`). Plugin transform and load maps are composed (as Vite's `combineSourcemaps` does) into the module's input map, which esbuild reads from an inline `sourceMappingURL`; a null map or `{mappings: ""}` counts as unchanged. The composition uses the vendored `@jridgewell/remapping` 2.3.5 plus its four dependencies (`scripts/vendor-js.sh` into `internal/frameworks/vite/js/vendor/@jridgewell/`; the host plugin resolves `og:remapping` and their `@jridgewell/*` imports of each other). `this.getCombinedSourcemap()` returns the composition so far; `sourceMapOf` also makes plugin-provided absolute `sources` relative. Svelte's plugin needs `getCombinedSourcemap`.
- **tsconfig**: the TS transform resolves the `tsconfig` through TypeScript-Go's `ogapi.LoadConfig`, which follows `extends` chains and target-dependent defaults (including `GetUseDefineForClassFields`) as tsc does, and passes esbuild the resolved `experimentalDecorators` / `useDefineForClassFields` / `verbatimModuleSyntax` (JSX stays out, since Vite's options win). The binding stub also has `TsconfigCache` (empty), `enhancedTransform(Sync)` and `minify(Sync)` on esbuild (Vite's `transformWithOxc` path, used by plugin-vue).
- **TS language from the id's own extension**: `App.vue?vue&type=script&lang.ts` is TypeScript; before it reached esbuild as JS and failed with "Expected ')' but found ':'".
- **Zero-byte modules kept in their chunk**: esbuild leaves zero-byte modules (CSS modules after `vite:css-post`) out of every output's `inputs`; each is attached to the output of its nearest importer that has one, using `metafile.inputs` edges. Without this, Vue's scoped style (`App.vue?vue&type=style…&lang.css`) was dropped.
- **`exportsOf` parses `ts`/`tsx`/`jsx` modules after an esbuild strip** (modules a late transform left in that language).
- **Advanced chunk callback context**: `advancedChunks.groups[*].test` and
  `.name` receive the same `getModuleInfo` / `getModuleIds` graph context that
  `manualChunks` receives. VitePress's chunk-name function requires it.
- **Virtual bare ids after the built-in resolver**: an unresolved SSR
  package-shaped id is deferred instead of externalized immediately, allowing a
  later framework plugin to claim virtual ids such as VitePress's `@siteData`.
  The final unresolved-import path still externalizes genuine SSR dependencies.

### Framework-specific facade behaviors

These facade internals exist for particular frameworks (the per-framework plugins and SSR entries are [[vite-framework-ecosystems]] and [[server-programs-ssr]]):

- **Astro**: `output.format` alias normalization (`esm` / `module` → `es`, `commonjs` → `cjs`, `systemjs`; CSS is collected only for `es`); `standaloneResolver` (`esbuild.go`) backs `this.resolve` outside a build; `define` values that are object literals but not JSON are evaluated and passed to esbuild as JSON.
- **React Router**: `BindingCallableBuiltinPlugin` / `og.rolldownCallable` expose a callable `viteResolvePlugin` for the child compiler; the default facade resolution / load runs after plugins when no Vite resolve plugin matched; `Host.Patch` / `applyPatches` patch run-time modules.
- **SvelteKit**: the `external` input option (string / RegExp / function) is honoured, plus `output.paths` and directory-bearing entry-name encoding that round-trips through `renameChunks`.
- **Nuxt**: output source-map `sources` are deduplicated (each source emitted once, since Rollup / Nitro rejects duplicate contents); the engine stack-overflow report shows the innermost 40 frames.
- **Astro markdown**: per-chunk define-banner trimming (`esbuild.go`) keeps only the `__og_define_<n>` banner lines each chunk references.

### Chunk naming (Rolldown's rules on esbuild's grouping)

The user chose chunk fidelity, then "names and manualChunks only" (emulating Rolldown's grouping would need og's own linker over esbuild's per-module output).

- esbuild groups more coarsely than Rolldown: Rolldown tree-shakes per entry at statement level; esbuild makes one chunk per set of entries reaching a module. Verified with standalone esbuild 0.28.2 (React and react-router land in one chunk there too) — not an og artifact.
- Rolldown's naming (1.2.12, `generate_stage/mod.rs`): a common chunk takes the representative name of its last-executed non-runtime module ("chunk" if none), a dynamic entry its module's; the representative name is the file stem, `index`/`mod` taking the parent directory's name. Defaults `[name].js` / `[name]-[hash].js`.
- `js/rolldown.js` implements `representativeName`; `chunkGroupNamer` (function and object `manualChunks` with `getModuleInfo`/`getModuleIds`; `codeSplitting`/`advancedChunks` groups by priority with function, RegExp or string `test`); `toBundle` names non-entry chunks by Rolldown's rules, a group's name when all its modules fall in one group, and marks chunks whose name differs from esbuild's (`ogRename`), which `renameChunks` renames by the pattern. The grouping warning says groups name chunks but do not group them.
- `esbuild.go` `packageSideEffects`: reads the nearest named package.json's `"sideEffects"` (boolean or globs; slash-less patterns match at any depth) for plugin-resolved ids without `moduleSideEffects`, because esbuild ignores the field for plugin results. (Did not change React Router's grouping.)
- Earlier state: `manualChunks`/`advancedChunks`/`codeSplitting` groups logged a warning and were ignored because esbuild cannot assign modules to named chunks; the warning did not fire for vite-react, vite-features or tanstack-start.

### Runtime serving (`runtime/app/vite.go`, `vite preview`'s stack)

- An SPA (`appType: "spa"`) serves `dist` with `vite preview`'s history fallback; an MPA (`mpa`) and base handling are also supported. Strip the base, serve files, then `htmlFallbackMiddleware` for HTML-accepting `GET`/`HEAD` (x.html, x/index.html, x → x.html, the SPA's `/index.html`), then an empty 404. Hashed `assets/` are immutable; HTML is `no-cache`. SSR apps serve a server entry (fetch handler) from per-request VMs as Next's dispatcher does — see [[server-programs-ssr]].

### VitePress static adapter

A project that declares `vitepress` without declaring `vite` must still select
the Vite builder. `internal/frameworks/vite/vitepress.go` runs VitePress's own
`resolveConfig()` and `build()` APIs on the build host, gathers the configured
output directory as a Vite multi-page app, and passes it through the normal
Oganesson packager. This produces a static `.og/` payload without requiring a
project-authored Vite entry.

The adapter deliberately relies on VitePress's upstream build rather than
reimplementing its page discovery or rendering. The fixture covers a home page,
guide page, clean URLs, local search, and a public asset. Oganesson's own
documentation site also builds through this path; see
[[user-documentation-and-site]].

### Vite 5 to 7 compatibility

- `sharedVite` (`vite.go`, used by vite / astro / nuxt hosts): for an installed Vite below 8, only `vite` is shared; the facade's `rolldown/*` entry points are not shared (they do not exist). Those Vites bundle with real Rollup (og's installer adds `@rollup/wasm-node`, substituted for the native binding) and the esbuild npm package (og's esbuild shim).
- `testdata/apps/vite7-react` (vite-react on Vite 7.3.7, @vitejs/plugin-react 5.2.0): against Node's build, same files, identical HTML and CSS, JS differing only in minifier naming (og's esbuild is not the npm esbuild version Vite pins). `checkReactSPA` (shared with `TestBuildReactSPA`) skips the two Vite 8-only traits (a modulepreload link, `import.meta.url` in the preload helper), which Node's Vite 7 build also lacks.

### Enabling runtime/jshost fixes found on the way

- `internal/wasm`: exported globals as `WebAssembly.Global` (i32 / i64 BigInt / f32 / f64, setter for mutable ones); es-module-lexer reads `__heap_base.value`.
- jshost: bundling applies native-addon substitutes (`lightningcss` → `lightningcss-wasm`; not napi-rs WASI loaders); `LocalizeFile`, `Host.Plugins`, `EvalWith` exported. napi-rs WASI substitutes now also apply in host bundling (`og:napi-rs-wasi` external in the bundle, loaded by runtime `require`), letting `@tailwindcss/oxide` load its wasm32-wasi build inside Vite's config bundle.
- nodeshim: `util.parseEnv` (dotenv's grammar, used by Vite's `loadEnv`); `util.styleText`; Buffer `base64url`.
- `gojatarget.LowerRegExpLiterals` rewrites regex literals with Unicode property escapes inside a character class (`/[\p{ID_Continue}]/u`, used by js-tokens) to `new RegExp(…)` via TypeScript-Go's parser, since esbuild does not lower them and Goja rejects them.

## Files

- `internal/frameworks/vite/host.go` — host-bundle esbuild plugins, substitutions, source patches of Vite's `node.js`.
- `internal/frameworks/vite/esbuild.go` — the `og.rolldown` Go side: `build`, `resolve`, `transform`, `dynamicImports`, `engines`, `packageSideEffects`.
- `internal/frameworks/vite/js/rolldown.js` — the facade: plugin container, hooks, builtins, `OutputBundle`, `representativeName`, `chunkGroupNamer`, `toBundle`, `renameChunks`.
- `internal/frameworks/vite/js/binding-stub.js` — Rolldown binding stub.
- `internal/frameworks/vite/js/parse.js` — vendored acorn for `parseAst` / `this.parse`.
- `internal/frameworks/vite/vite.go` — `sharedVite`, shared host wiring (also astro / nuxt).
- `internal/frameworks/vite/vitepress.go` — VitePress detection and static
  build adapter.
- `runtime/app/vite.go` — `vite preview`-style serving (base strip, file serve, HTML fallback).
- `internal/jshost` — `replaceImportMetaURL` / `ogapi.ImportMetaURLs`, bundling substitutes, `EvalWith`, `LocalizeFile`.

## Test Coverage

Fixture-based tests skip when the fixture's `node_modules` is absent; install with `go run ./cmd/og install <dir>`.

- `TestBuildReactSPA` (`testdata/apps/vite-react`): Vite's injected HTML, CSS per chunk, `__vite__mapDeps` with the lazy chunk and its CSS, glob and alias results, no `import_meta`/`file://` leaks.
- `TestBuildViteFeatures` (`testdata/apps/vite-features`, vanilla TS + nanoid): resolved URLs, no `ROLLDOWN_FILE_URL` or `file://`, the manifest, map sources, tsconfig semantics, `{main:7}` from `this.parse`.
- `TestBuildVite7SPA` / `vite7-react`: Vite 7 compatibility via shared `checkReactSPA`.
- `TestFacadeChunkNames` (shared, `index`, dynamic entries, with function `manualChunks`; expectations from real Rolldown 1.2.12); `TestFacadePackageSideEffects` (plugin-resolved ids: `false`, glob list with/without slash); `TestFacadeRenderedExports` / `TestReplaceImportMetaURL`.
- `TestDetectVitePress`, `TestBuildVitePress`,
  `TestFacadeAdvancedChunkNameContext`, and
  `TestFacadeVirtualBareIDAfterBuiltinResolver` cover detection, VitePress's
  own client/server builds, and both facade gaps. Neutralizing each fix produces
  its intended failure rather than a skip or compile error.
- `TestViteApp` (`runtime/app`): spa / mpa / base handling. `TestExportedGlobals`, `TestParseEnv`.
- Browser (packaged binary): deep-link SPA fallback and hydration; env/define/JSON render; CSS; the lazy chunk loads with its CSS via `__vitePreload`; clean console; vite-features workers, fetched asset, `?raw`/`?url`/`?inline`, the decorator.
- Each fix was neutralized (fix removed → test fails with the intended message). One neutralization passed at first (TS `exportsOf`): the Vue test actually depended on the zero-byte fix, so `exportsOf` gained its own facade case.

## Pitfalls

- **Chunk metadata**: Vite defines `chunk.viteMetadata` and a `chunk.modules` getter with `Object.defineProperty` per hook call (`ChunkMetadataMap`, keyed by `preliminaryFileName`). Chunks must carry both as configurable own properties, or the second definition throws.
- **Wildcard defines**: `"import.meta.env.*"` has no esbuild form; it is dropped when the prefix is defined as an object (unknown members are then undefined anyway).
- **TS/JSX must run in plugin order**: after it, `vite:import-glob` would otherwise see `import.meta.glob<string>(…)` as TypeScript.
- **CSS**: CSS modules are type `js` (Vite's `moduleTypes`); `vite:css-post` extracts CSS per chunk from `chunk.moduleIds`.
- esbuild cannot assign modules to named chunks, so `manualChunks`/`advancedChunks` groups only name chunks, they do not group them.
- Node reference builds must run outside og's git repository: inside it, Tailwind's source detection scans differently (a React Router CSS size difference was that artifact).
- VitePress currently needs `lastUpdated`, Markdown-it automatic bare-URL
  linkification, and Vite 8's native Lightning CSS minifier disabled. These are
  independent build-host gaps, not limitations of the static adapter itself.

## Deferred / open

- Express-style SSR and the Fastify/Koa/Hono/h3 variants are supported by
  replaying the built server program in each request VM; see
  [[server-programs-ssr]]. Preview-hook middleware from arbitrary user plugins
  remains outside that model.
- The esbuild `NumberRenamer` race was avoided by moving object/array defines
  into per-chunk globals. Reporting the upstream race remains deferred; see
  [[js-language-gaps-and-lowering]].
