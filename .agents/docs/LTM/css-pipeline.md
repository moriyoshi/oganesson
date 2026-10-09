# CSS Build Pipeline

## Summary

Oganesson runs every CSS source through Next's own CSS pipeline before esbuild compiles it, matching `next start` fidelity without reimplementing anything. The bridge calls Next's `lazyPostCSS`, `sassPreprocessors`, and lightningcss-loader; Tailwind v3 runs unmodified in Sobek, and Tailwind v4's native Rust addons (oxide, lightningcss) run as their WebAssembly builds on wazero. The guiding rule is "use Next's own code over reimplementations".

## Key Facts

- CSS build lives in `internal/frameworks/next/css.go` (`builder.postcss`); the JS bridge is `internal/jshost/js/bridge.js`.
- PostCSS goes through Next's `lazyPostCSS(root, getSupportedBrowsers(root), disablePostcssPresetEnv, useLightningcss)` from `next/dist/build/webpack/config/blocks/css`.
- Two esbuild plugins: `og-postcss` (onLoad for `.css` in the file namespace) and `og-cssmod` (CSS Modules). (Older JOURNAL entries call these `gots-postcss` / `gots-cssmod`.)
- The build JS host always starts, even without `og.config.*`. `StartPlugins(configFile, HostOptions{PostCSS: ...})` takes `""` to mean no config; next.config `experimental` flags pass through.
- Tailwind v3 runs unmodified in Sobek: `.mjs` config, `@tailwind`, `@apply` with theme extensions, content scanning through Go-backed fs.
- Tailwind v4 uses `@tailwindcss/postcss`; its native addons `@tailwindcss/oxide` (scanner) and `lightningcss` run as WebAssembly on wazero (see [[wasm-on-wazero]]).
- Sass (`.scss`/`.sass`, including `.module.*`) runs through Next's `sassPreprocessors` (sass-loader then resolve-url-loader) before PostCSS.
- `experimental.useLightningcss` swaps css-loader + postcss-loader for Next's lightningcss-loader, done over lightningcss-wasm.
- CSS Modules `composes` across files is handled by esbuild 0.28 `local-css` plus Oganesson's cross-file naming fix.

## Details

### PostCSS and Tailwind v3

Every CSS source (global sheets, `@import`ed files, CSS Modules) goes through Next's PostCSS pipeline, the same place webpack's postcss-loader runs. The bridge's `postcss(from, css)` calls Next's `lazyPostCSS`, giving Next's exact behavior:

- config discovery (`postcss.config.{js,cjs,mjs,json}`);
- plugin-shape validation and plugin loading;
- defaults when no config exists: flexbugs-fixes plus preset-env driven by the project's browserslist, falling back to Next's modern target.

Tailwind v3 works unmodified in Sobek. Measured costs: about 0.4 s to load the default pipeline and about 80 ms per file; with Tailwind, about 0.6 s to load and about 1 s for the first file.

A key enabling bug fix: `require.resolve` used to resolve from the project root, not from the calling module, which broke any dependency that `require.resolve`s its own nested dependency (Next's `getDefaultPlugins` among them). The global `require.resolve` is now native and uses the caller's frame (`CaptureCallStack`), as goja_nodejs's `require` does. Code bundled by esbuild has no module file of its own and still resolves from the project.

### Tailwind v4 via oxide + lightningcss-wasm

Tailwind v4 depends on two native Rust addons, both of which publish WebAssembly builds:

- `@tailwindcss/oxide`, the source scanner (napi-rs) → `@tailwindcss/oxide-wasm32-wasi`, a `wasm32-wasip1-threads` build importing shared memory, emnapi helpers and `wasi.thread-spawn`;
- `lightningcss` → `lightningcss-wasm`, a non-threaded build using napi-wasm's JS Node-API.

Oganesson provides the `WebAssembly` JS API to build-time Sobek and runs these builds unmodified on wazero; the wasm host mechanics (`internal/wasm`, emnapi/wasi-threads natives, memory views, compilation cache) are documented in [[wasm-on-wazero]].

The loader substitutions live in `nativeSubstitute` (`internal/jshost/native.go`):

- A napi-rs `<name>.wasi.cjs` with a sibling `<name>.wasm32-wasi.wasm` loads through `og:napi-rs-wasi` (`js/napi-rs-wasi.js`), using vendored napi-wasm 1.1.3. It replicates the napi-rs loader sequence: `_initialize`, every `__napi_register__*` export, then `napi_register_wasm_v1` only.
- `lightningcss/node/index.js` requires `lightningcss-wasm` instead of `lightningcss-<platform>`.
- napi-wasm's fatal UTF-8 decoder is made lenient at load time to match Node's `napi_create_string_utf8`. Oxide extracts candidates from arbitrary file bytes, which can contain invalid UTF-8.
- `module.register()` is a no-op. Tailwind registers an ESM cache-busting loader hook; Oganesson never loads modules through Node's ESM loader.

The installer fetches optional `*wasm32*` dependencies declaring `cpu: wasm32`, adds the `wasmSubstitutes` (`lightningcss` → `lightningcss-wasm`, same version), and skips either kind quietly when it cannot be fetched.

### Sass

`.scss` / `.sass` (and `.module.scss` / `.module.sass`) are CSS sources. `builder.postcss` runs them through the bridge's `sass(path)` first, then PostCSS, as Next's loader stack does. `sass(path)` runs Next's own preprocessors with Next's options (webpack-config's `sassPreprocessors`):

1. `next/dist/compiled/sass-loader` (`sourceMap: true`, the project's `sassOptions` minus `prependData` / `additionalData` / `implementation`, plus `additionalData`), then
2. `resolve-url-loader`, which rebases partials' `url()`s onto the entry file.

`webpack.js` exports `runLoaderChain` (the loader-context core factored out of the og.config plugin) and `fsResolver`, a file-system resolver for the `getResolve` options loaders pass (extensions, mainFiles, mainFields, restrictions); sass-loader's webpack importer uses it. dart-sass's dart2js build runs unmodified in Sobek; the first compile takes about 0.5 s. The fixture uses `sass@^1.80` (1.105.1).

**Live sassOptions.** `sassOptions` crosses into the bridge as JSON, so function-valued options (custom importers, Sass `functions`) would be lost. To fix this, `HostOptions.NextConfigFile` (`jshost.FindNextConfig`) lets the build host evaluate the project's next.config on the first Sass compile, through the host's runtime `require` (TS/ESM aware), with the function form called with the build phase and `defaultConfig`. The bridge's `sass()` takes `sassOptions` from it, so functions survive; the JSON copy remains the fallback.

### CSS Modules `composes`

esbuild 0.28's `local-css` implements `composes` (local, `from global`, and `from "./other.module.css"`), so the old "esbuild drops it" note is stale. The real bug was cross-file naming: each module compiles under a hashed virtual name, but a module pulled in by `composes … from` loaded under its real name, giving `shared_pill` where the module's own compile says `shared_7272a5_pill`. `cssModuleVirtual` now names every module, and the `og-cssmod` plugin routes `.module.*` imports from inside a CSS Module through it. The composed module's rules are also bundled into the composing sheet; they are identical, so this is harmless.

### experimental.useLightningcss

When this flag is set, Next replaces css-loader + postcss-loader with its lightningcss-loader. The bridge's `lightningcss(from, css)` does the loader's transform on lightningcss-wasm (resolved from the project, falling back to Next's directory):

- targets come from Next's own `getTargets` over `getSupportedBrowsers`;
- the include mask is Nesting plus `experimental.lightningCssFeatures.include`, minus the excludes;
- `drafts.customMedia` is set when custom media queries are included;
- `featureNamesToMask` maps kebab-case names onto lightningcss's `Features`.

It replaces the PostCSS step. With PostCSS plugins configured, it throws Next's "does not work with postcss plugins" error. esbuild keeps the css-loader duties (`@import`, `url()`, modules). Projects need `lightningcss-wasm` installed; it resolves from the project only, and a missing one fails with "add lightningcss-wasm to devDependencies and run og install". The installer adds it only alongside `lightningcss` (as with Tailwind v4), since it cannot see next.config and ~5 MB per project for one experimental flag is not worth it.

### Oxide's slow scans (upstream behavior, not an Oganesson defect)

Oxide can take ~130 s to scan certain files under wazero while comparable-sized files take under 0.5 s. This is upstream extraction behavior amplified by the scalar wasm build, not a defect in Oganesson:

- **Trigger is line length, not file size.** Extraction is quadratic in line length. Synthetic single-line base64 inputs scan in 111 / 412 / 1658 / 6672 ms for 25 / 50 / 100 / 200 KB; a 2 MB single line did not finish within 600 s, while 2.3 MB spread over 70k short lines takes 2.1 s. The old pathological 6 MB `@napi-rs/wasm-runtime/dist/fs.js` most likely carried a huge inline source map (the installed version is now 620 KB and scans in 111 ms).
- **Profile** (Go pprof, 200 KB line): about 99 % of samples are in JIT-compiled wasm (`_ExternalCode`, wazevo `callWithStack`) on one wasi thread. Memory growth, host calls and the JS↔wasm boundary don't register, so nothing on Oganesson's side scales with the input.
- **Why the gap vs native is large** (21–71× slower than native single-threaded): `tailwindcss-oxide.wasm32-wasi.wasm` compiles with SIMD disabled, so the native build's NEON/memchr-style byte scanning becomes scalar in wasm, and the quadratic path is scan-heavy.
- A local mitigation such as skipping long lines would change which classes Tailwind finds, so none is applied.

**Oxide ignore rules** (a separate scan-cost trap): if any `.gitignore` between the scan base and the repository root ignores the base, oxide promotes it to an **external** source and scans everything (`!/**/*`) — see `crates/oxide/src/scanner/sources.rs`. This is why scratch Tailwind projects must not live in gitignored directories (e.g. `.agents-workspace/tmp/`), or should set `source(...)`.

## Files

- `internal/frameworks/next/css.go` — `builder.postcss`, the build-time CSS driver and esbuild plugins (`og-postcss`, `og-cssmod`).
- `internal/jshost/js/bridge.js` — bridge functions `postcss(from, css)`, `sass(path)`, `lightningcss(from, css)`.
- `internal/jshost/native.go` — `nativeSubstitute`, the napi-rs/lightningcss-wasm loader substitutions.
- `internal/jshost/js/napi-rs-wasi.js` — `og:napi-rs-wasi` loader for napi-rs wasi addons (vendored napi-wasm 1.1.3).
- `internal/frameworks/next/webpack.js` — `runLoaderChain`, `fsResolver`, `sassPreprocessors` wiring.
- `internal/install` — installer logic for `*wasm32*` optional deps and `wasmSubstitutes`.

## Test Coverage

Fixture-based tests skip when the fixture's `node_modules` is absent; install with `go run ./cmd/og install <dir>` (uses the network once).

- `TestPostCSS` (next) — Tailwind v4 output on `/about`; neutralized by bypassing PostCSS (leftover `@tailwind`) or, without the napi-rs substitution, oxide's "Cannot find native binding".
- `TestPostCSSDefaults` (jshost) — default pipeline adds `-webkit-user-select` for Next's target, none for `.browserslistrc` of `chrome 120`.
- `TestPostCSSTailwind3` (`testdata/plugins/tailwind3`) — Tailwind v3 coverage, reusing next-hello's Next for `lazyPostCSS`.
- `TestCSS` (next) — accepts lightningcss's re-minified `--accent:#0070f3`, since `@tailwindcss/postcss` optimizes every stylesheet.
- `TestSass` (jshost) — url rebasing from a partial in another dir, `loadPaths` via sassOptions, `additionalData`, nesting; neutralized by dropping resolve-url-loader.
- `TestSass` (next, fixture `/gallery`) — scoped classes, rebased/emitted `stripe.svg`, `color.adjust`, nesting, indented syntax.
- `TestSassLiveOptions` (jshost) — a function-form next.config with a custom modern importer that sees the phase; neutralized by ignoring the live config.
- `TestComposes` (next) — a badge composes `base` plus `pill` from `styles/shared.module.css` and must carry the exact exported class (failed before the fix with `shared_pill`).
- `TestLightningcss` (jshost) — Safari 13 nesting lowered + `-webkit-` prefixes; Chrome 130 keeps `light-dark()` unless `include: ["light-dark"]`; PostCSS plugins refused.
- `TestLightningcssMissing` — missing lightningcss-wasm fails with the actionable message.
- `TestOxideIgnoreRules` (jshost) — a project inside a repo whose `.git` is in an ancestor: `node_modules` and `.gitignore`d paths skipped.
- wasm host tests (`TestWebAssembly`, `TestWasmBuilds`, etc.) live under [[wasm-on-wazero]].

## Pitfalls

- Next appends `sassPreprocessors.slice().reverse()` and webpack runs loaders right to left, so sass-loader must run first. With the order inverted, resolve-url-loader saw raw SCSS and silently rewrote nothing; the only symptom was esbuild failing to resolve `./stripe.svg`.
- Bundling the next.config import resolved `next` from the host's directory, not from `NextDir`; the runtime `require` from `NextDir` avoids that.
- The `TestLightningcss` include assertion must target Chrome 130, not Safari 13 — Safari 13 lowers `light-dark` anyway, so the assertion passed even with the feature mask neutralized.
- Scratch Tailwind projects in gitignored directories make oxide scan everything as an external source; keep them outside gitignored paths or set `source(...)`.
