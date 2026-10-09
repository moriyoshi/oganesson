# Build System: esbuild Layers and JS Plugins

## Summary

Oganesson's build system drives esbuild from Go, keeping server modules in distinct layer namespaces and exposing an esbuild/webpack plugin surface that runs entirely in Sobek (Goja) with no Node.js. This document covers the layer namespaces, the JS plugin architecture, the webpack loader shim, build-time `require`, and the introduction of the unenv preset for Node built-ins. CSS specifics are in [[css-pipeline]]; the detailed Node-builtin shimming is in [[node-compat-shims]].

## Key Facts

- Layer namespaces for server modules: `rsc`, `ssr`, and `shared`. A plugin registered for `namespace: "file"` is also applied to these via `FileNamespaces`; `MapResolve` keeps resolve results inside the importer's layer.
- User config file: `og.config.{ts,mts,mjs,js}` (older notes call it `gots.config.*`). Shape exposes `esbuild: { plugins, clientPlugins, serverPlugins, cssPlugins }` and `webpack: { rules }`.
- Plugins run in **one long-lived Goja engine per build** (`PluginHost`). Go calls the bridge through `call()`, which posts to the host loop and awaits the result.
- `import "esbuild"` inside plugins resolves to a shim; `transform` is backed by the Go API; `esbuild.version` is the embedded module version (read via `debug.ReadBuildInfo` since the Go API has no `api.Version`).
- Metafile keys arrive as `ns:/abs/path` for custom namespaces and as relative paths for the file namespace; `parseMetafile` normalizes both to `ns:/abs`.
- Build-time `require` is **goja_nodejs/require** (vendored for Sobek), not hand-written.
- jshost bundles with Conditions `["node"]` only. With `[node, import, require]`, esbuild picked ESM entries for code that `require()`s.
- Node built-ins come from vendored **unenv 2.0.0-rc.24** plus a small set of overrides.

## Details

### Layer namespaces

Server modules live in three layer namespaces (`rsc`, `ssr`, `shared`). Because a plugin registered for the standard `file` namespace must still apply inside these layers, `FileNamespaces` extends the plugin's reach to them, and `MapResolve` ensures a plugin's resolve result stays in the importer's layer rather than leaking across layers.

### esbuild JS plugins (`internal/jshost/plugins.go`, `js/bridge.js`, `js/esbuild.js`)

Plugins are ordinary esbuild JS-API plugins. They execute in a single long-lived Goja engine per build (`PluginHost`); Go invokes them through the bridge's `call()`, which posts work to the host loop and awaits the result. `import "esbuild"` resolves to a shim where `transform` is backed by the Go API and `esbuild.version` reports the embedded module version.

**Load ordering (`LoadChain`).** If plugin `onLoad` hooks for the file namespace went straight to esbuild, they would bypass Oganesson's `"use client"` / `"use server"` directive handling. Instead they are routed through a `LoadChain`:

- Oganesson's own loaders run the chain first;
- then Oganesson post-processes the chain's output;
- CSS loads stay owned by Oganesson (`SkipLoad`).

Verified with `esbuild-plugin-postcss@0.3.0` (`testdata/plugins/postcss`, a `.module.less` file through PostCSS). `less` calls `createRequire`, supplied by the `module` override.

### webpack loader shim (`internal/jshost/js/webpack.js`)

`webpack.rules` (`test` / `include` / `exclude` / `use`) become one more esbuild plugin, prepended to the `all` plugin list. Loaders named by string are loaded with `require`. The loader context is the subset Babel-style loaders use:

- `getOptions`, `async`, `callback`, `resourcePath`, `resourceQuery`, `target`, `sourceMap`;
- `getLogger` (debug and trace are no-ops, otherwise babel-loader floods stderr);
- `resolve` / `getResolve`, `utils`, `addDependency`.

The output loader is inferred from the file extension. **styled-jsx** works through this shim: `babel-loader` + `styled-jsx/babel`, configured with only the styled-jsx plugin and parserOpts `jsx` + `typescript`, so esbuild still handles TS and JSX. Fixture: `testdata/apps/next-hello/gots.config.ts` with `styled-registry.tsx` and `fancy.tsx`. Scoped styles SSR and hydrate correctly in the browser.

### Build-time `require` (`internal/jshost/require.go`)

The loader is goja_nodejs/require (vendored for Sobek), chosen at the user's direction over a hand-written one. Oganesson supplies the pieces it lacks:

- `WithLoader` turns ESM and TS sources into CJS with esbuild, passes JSON through, and returns `ModuleFileDoesNotExistError` for directories and missing files so goja_nodejs keeps searching.
- `WithPathResolver` anchors relative bases at the project directory, keeps absolute paths, and resolves package `exports` with conditions `node`, `require`, `default`.
- Built-ins are registered under both `name` and `node:name`, bundled on demand from nodeshim.

The global `require` must remain goja_nodejs's own function, because it resolves relative to the caller's frame. A native wrapper maps not-found errors to `code: "MODULE_NOT_FOUND"`. `requireFront` adds `require.extensions`, `require.cache`, `require.resolve(id, {paths})`, and `Symbol.for("gots.createRequire")`.

### Node built-ins: unenv preset

Vendored unenv 2.0.0-rc.24 (`dist/runtime`) plus pathe 2.0.3 provide the Node built-in surface, with a few Go-backed and JS overrides on top. The mechanics and gotchas are detailed in [[node-compat-shims]]; in brief, `Builtins` is derived from unenv's file list, overrides live in `runtime/nodeshim/js`, and at build time Go backs fs (`internal/jshost/fs.go`) and zlib (`internal/jshost/zlib.go`). Embedding uses `//go:embed all:...` (the `all:` prefix is required because unenv has `_internal` directories).

## Files

- `internal/jshost/plugins.go` — esbuild JS plugin host, layer namespace mapping, `LoadChain`.
- `internal/jshost/js/bridge.js`, `internal/jshost/js/esbuild.js` — plugin bridge and the `esbuild` module shim.
- `internal/jshost/js/webpack.js` — webpack loader shim (`webpack.rules` to esbuild plugin, loader context).
- `internal/jshost/require.go` — build-time `require` built on goja_nodejs/require (vendored for Sobek).
- `internal/jshost/fs.go`, `internal/jshost/zlib.go` — Go backends for fs and zlib at build time.
- `runtime/nodeshim/` — unenv preset and overrides (see [[node-compat-shims]]).
- `internal/frameworks/next/css.go` — CSS compilation and `entryCSSFiles` manifest (see [[css-pipeline]]).

## Test Coverage

- `internal/jshost`: `TestPluginBridge`, `TestEsbuildPluginPostcss`, `TestDynamicRequire`.
- `internal/frameworks/next`: `TestCSS`, `TestStyledJSX`.
- Full race suite `go test -race ./runtime/... ./internal/... ./pack/...` passed.
- Browser-verified on a packaged binary: global CSS, CSS module classes, styled-jsx scoping, and toggling after hydration.
- Fixture-based tests skip when the fixture's `node_modules` is absent; install with `go run ./cmd/og install <dir>`.

## Pitfalls

- `.mjs` configs that default-import CJS packages hit interop issues. Prefer `og.config.ts` (historically `gots.config.ts`).
- babel needs `process.stderr.fd`, `tty`, `zlib`, and `require.extensions`; all now come from unenv plus the overrides (see [[node-compat-shims]]).
- Bundling with `[node, import, require]` conditions makes esbuild pick ESM entries for `require()`d code; jshost uses `["node"]` only.
- Plugin `onLoad` hooks for the file namespace must go through `LoadChain`, not straight to esbuild, or they bypass `"use client"` / `"use server"` handling.
