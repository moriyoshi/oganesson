# Next.js Build and Assets Synthesis

## Summary

The Next.js builder is a Go-driven esbuild pipeline that preserves Next's
webpack layers and executes Next's own configuration, loader, CSS, font, image,
metadata, and compatibility code in the build host. The `og compat` corpus is
the feedback loop that hardens these integrations against real Next examples.
The result is not a webpack implementation; it is a focused compatibility
surface over esbuild, TypeScript-Go, WebAssembly substitutes, and guarded
patches of upstream Next code.

## Included Documents

| Document | Focus |
|----------|-------|
| [build-system-esbuild-layers.md](./build-system-esbuild-layers.md) | Layer namespaces, plugin host, webpack loaders, and build-time `require`. |
| [css-pipeline.md](./css-pipeline.md) | Next PostCSS, Sass, Tailwind, CSS Modules, and lightningcss. |
| [assets-images-fonts-metadata.md](./assets-images-fonts-metadata.md) | Metadata routes, image optimization, fonts, ImageResponse, and sharp substitution. |
| [compat-corpus-m11.md](./compat-corpus-m11.md) | Corpus harness, config hooks, build scripts, and compatibility hardening. |

## Stable Knowledge

- Server modules retain Next's layer identity through esbuild namespaces.
  `FileNamespaces` lets file plugins run in `rsc`, `ssr`, and `shared`, while
  `MapResolve` keeps results in the importer's layer.
- One long-lived `PluginHost` executes JavaScript plugins for a build. The
  bridge provides an `esbuild` module over the Go API and routes file `onLoad`
  hooks through `LoadChain` so Oganesson's directive and framework transforms
  still run.
- Build-time CommonJS uses vendored goja_nodejs `require`, with Node-like
  resolution, package exports, caller-relative `require.resolve`, module fields,
  and Node built-ins from `runtime/nodeshim`.
- `og.config.*` can supply esbuild and webpack-loader plugins. Next's
  `webpack()` and Turbopack configuration are translated into the compatible
  subset: aliases, fallbacks, definitions, loader rules, nested `oneOf`, and
  CSS-producing rules. Babel configs use Next's own Babel loader.
- The selected config mode follows the project's `next build` command. The
  runtime bundles stay webpack-shaped, so `getDefineEnv` deliberately receives
  `isTurbopack: false` even when Turbopack configuration supplied the loaders.
- Next's CSS pipeline is reused: `lazyPostCSS`, Sass preprocessors,
  resolve-url-loader, css-loader behavior through esbuild, and the
  lightningcss loader. Tailwind v4 and lightningcss native addons run through
  their wasm builds on `internal/wasm`.
- CSS Modules use esbuild `local-css`; Oganesson supplies stable cross-file
  virtual naming for `composes`. Function-valued `sassOptions` require live
  evaluation of `next.config`, not the JSON copy.
- Metadata file routes are generated from ports of Next's metadata loaders and
  become ordinary route entries. Static image imports produce Next-shaped
  objects and emitted media; `runtime/imageopt` provides safe local/remote
  fetching, resizing, content negotiation, ETags, and in-memory revalidation.
- `next/font` is a source transform followed by Next's own Google/local font
  loaders and PostCSS step. Font assets, CSS locals, and preload manifests are
  emitted in the same build graph.
- `ImageResponse` uses the edge `@vercel/og` path with restricted runtime
  WebAssembly for resvg/yoga and the shared Intl implementation.
- The native sharp substitute supports the common image operation subset at
  build and run time. AVIF encoding is intentionally omitted; WebP must use the
  pure-Go path selected by `-tags nodynamic`.
- `og compat` installs, builds, serves, and crawls the pinned Next examples
  corpus. It records expected failures, can rerun failures, supports workspaces
  and per-app dummy env, and isolates app crashes in child processes.
- Compatibility hardening includes computed imports, webpack-like missing
  export tolerance, Node/CJS interop, optional peers, build-script programs,
  lazy route modules, basePath and assetPrefix, Next 16.3/16.4 patch variants,
  and browser target behavior.

### Source chronology

The assets source retains an older `Intl.Segmenter` limitation note from the
pre-FormatJS implementation. The current implementation is FormatJS as recorded
in [unicode-regexp-intl.md](./unicode-regexp-intl.md); any remaining CJK
segmentation fidelity should be remeasured before being stated as a current
limitation.

## Operational Guidance

- Reuse Next's loaders, templates, config helpers, and manifest utilities. Put
  unavoidable source adjustments in `internal/frameworks/next/patches.go` with
  an explanation and an exact-match guard.
- Keep layer transitions explicit when adding resolvers. A correct module from
  the wrong layer can silently select the wrong React or Next runtime.
- Route plugin file loads through `LoadChain`. Keep CSS ownership in the CSS
  pipeline rather than allowing a generic loader to bypass it.
- For asset features, verify the JavaScript module shape, emitted bytes,
  manifest references, HTTP behavior, and browser result.
- Treat the corpus as a discovery tool, not the normal gate. Every corpus fix
  needs a focused regression test and a neutralization; a one-hour corpus run is
  not a substitute for a deterministic fixture.
- Run reference builds outside the Oganesson repository when source scanning
  or gitignore behavior could change the result.

## Files

- `internal/jshost/plugins.go`, `internal/jshost/js/bridge.js`,
  `internal/jshost/js/webpack.js`, `internal/jshost/require.go` - plugin and
  loader host.
- `internal/frameworks/next/css.go`, `font.go`, `metadata.go`,
  `staticimage.go`, `wasmmodule.go` - CSS and asset build features.
- `runtime/imageopt`, `runtime/natives/sharp.go` - runtime optimizer and sharp
  substitute.
- `internal/frameworks/next/webpackhook.go`, `bundler.go`, `codegen.go`,
  `missingexports.go`, `patches.go` - compatibility and config integration.
- `cmd/og/compat.go`, `scripts/sync-next-examples.sh`,
  `testdata/compat/expected.json` - corpus harness and baseline.

## Tests

- Focused build tests include `TestPluginBridge`, `TestEsbuildPluginPostcss`,
  `TestWebpackHook`, `TestBabelConfig`, `TestTurbopack`, `TestBundlerEnv`,
  `TestRewriteMissingImports`, and `TestRunProgramTopLevelAwait`.
- CSS and asset coverage includes `TestPostCSS`, `TestSass`,
  `TestSassLiveOptions`, `TestComposes`, `TestLightningcss`,
  `TestMetadataFiles`, `TestNextImage`, `TestNextFont`,
  `TestNextFontGoogle`, `TestImageResponse`, and `TestBuildTimeSharp`.
- Corpus harness coverage includes `TestCompatLinks`, `TestCompatCopy`,
  `TestCompatFailed`, `TestCompatWorkspaces`, and `TestCompatExpected`.

## Pitfalls

- A file plugin registered directly with esbuild can bypass directive
  processing. Use `LoadChain` and layer-aware resolution.
- Build-host `KeepNames` is deliberately disabled; several frameworks inline
  `fn.toString()` and would ship an unavailable `__name` helper.
- `webpack()` plugin instances other than `DefinePlugin` remain inert, and
  issuer/externals selectors and webpack child compilations are not modeled.
- `experimental.mdxRs`, uninstalled-package `npx`, and computed requires inside
  dependencies remain corpus gaps.
- Oxide scanning is pathological for very long lines and for projects placed
  under an ignored directory. Do not "fix" it by skipping source, which changes
  Tailwind output.
- `/_next/image` cache state is process-local; AVIF falls back to the original
  image. `-tags nodynamic` is required for the shipped WebP implementation.
- A kept corpus work directory and large Go cache can exhaust disk space;
  preserve only runs needed for diagnosis.
