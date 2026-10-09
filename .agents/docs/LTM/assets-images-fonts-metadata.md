# Assets: Images, Fonts, and Metadata Files

## Summary

How Oganesson handles the asset-shaped parts of a Next.js app without Node or
sharp: static metadata file conventions (icon, opengraph-image, favicon,
robots, sitemap, manifest), `next/image` optimization (`runtime/imageopt`),
`next/font` (google and local), `ImageResponse` (`next/og`), and a pure-Go
sharp substitute used at build time. Each piece ports Next's own code and
emits Next's own module shapes, substituting Go where sharp or native addons
would be required. Image/font CSS joins the shared [[css-pipeline]]; the
runtime WebAssembly `ImageResponse` relies on belongs to [[wasm-on-wazero]].

## Key Facts

- Static metadata routes are ordinary `IsRoute` entries emitted from ported
  Next loaders in `internal/frameworks/next/metadata.go`.
- `runtime/imageopt` replaces the sharp-backed parts of Next's
  `image-optimizer.js`; it serves `/_next/image` in front of the app.
- No AVIF encoding (decision): `gen2brain/avif` is 15 MB of generated Go, so
  AVIF requests fall back to the upstream image, as Next does when sharp fails.
- Build `og` with `-tags nodynamic` so `gen2brain/webp` uses its pure-Go wasm
  build instead of `dlopen`ing a system libwebp.
- `next/font` is a source transform (`internal/frameworks/next/font.go`) plus a
  jshost bridge that runs Next's own font loaders; google fonts download via a
  `fetch`-based substitute for `fetch-resource.js`.
- `ImageResponse` needs restricted WebAssembly (resvg, yoga) in the runtime;
  see [[wasm-on-wazero]].
- Build-time sharp substitute lives in `runtime/natives/sharp.go`
  (`metadata()` exposes sharp 0.34's fields) for build plugins like Qwik City's
  vite-imagetools.
- Route handlers (metadata routes included) are prerendered and cached under
  Next's static-generation rules; plain GET handlers stay dynamic.

## Details

### Static metadata files

`internal/frameworks/next/metadata.go` ports Next's metadata file conventions
the way `tree.go` ports next-app-loader:

- **Discovery and route names.** `isMetadataRouteFile`,
  `normalizeMetadataRoute`, `normalizeMetadataPageToRoute`,
  `fillStaticMetadataSegment` and `djb2Hash` follow
  `next/dist/lib/metadata/{is-metadata-route,get-metadata-route}.js`.
  `scanMetadataRoutes` mirrors route-discovery's `createPagesMapping`, with
  `rsc.ProbeExports` standing in for `getPageStaticInfo`
  (`generateImageMetadata` / `generateSitemaps`).
- **Loader tree.** `treeBuilder.segmentMetadata` ports
  `createStaticMetadataFromRoute` and `createMetadataExportsCode`. A level's
  directory metadata is attached to the page leaf and to every child segment's
  modules. `BuildTree` takes `basePath`.
- **Generated modules.** `og:metadata-image:<query>` is
  next-metadata-image-loader's output. For static images the size is computed
  in Go: `image.DecodeConfig` for png, jpeg and gif; the ICO header's first
  entry (like image-size); SVG gets `sizes="any"`. `og:metadata-route:<page>`
  is next-metadata-route-loader's output (static asset, robots/manifest,
  sitemap with and without `generateSitemaps`, image with and without
  `generateImageMetadata`). The emitted JS calls Next's `resolveRouteData`,
  `fillMetadataSegment` and `NextResponse`.
- Both module prefixes resolve into the RSC layer with `ResolveDir` set to the
  project. Metadata routes are ordinary `IsRoute` entries whose app-route
  template `VAR_USERLAND` is the generated module (`appRouteSource`).
- `[contenthash]` is the first 16 hex digits of the content's sha256; only its
  stability matters.
- **Pitfall:** running Next's isomorphic helpers in a bare Goja VM needs
  `process.env.NEXT_RUNTIME = "edge"` so that `shared/lib/isomorphic/path`
  picks path-browserify over `require("path")`.

### `next/image` and `runtime/imageopt`

`runtime/imageopt` (new) stands in for the sharp-backed parts of
`next/dist/server/image-optimizer.js`:

- `image.go`: `DetectContentType` (the same magic numbers as
  `detectContentType`), `IsAnimated` (the is-animated package: multi-frame
  GIF, PNG `acTL`, WebP VP8X animation flag), `Size` (image-size semantics:
  first ICO image, SVG width/height/viewBox, EXIF-rotated JPEG), and
  `Optimize` (EXIF auto-orient like sharp's `rotate()`, CatmullRom resize that
  never enlarges, then encode). Encoding uses Go's jpeg/png/gif encoders and
  `gen2brain/webp` for WebP; JPEG with alpha is flattened over black.
- `handler.go` ports `/_next/image`:
  - `handleNextImageRequest`: a non-default loader or `unoptimized` falls
    through to the app, which renders the 404.
  - `validateParams` (Next's exact error strings), `getSupportedMimeType`, and
    the cache key.
  - `fetchInternalImage`: the whole application serves local URLs, so public
    files, build media and route handlers all work.
  - `fetchExternalImage`: private-IP guard (any non-unicast ipaddr.js range),
    addresses pinned at dial time, up to 3 redirects, a 7 s timeout, and
    `maximumResponseBody`.
  - `imageOptimizer`: bypass/animated types, the SVG refusal, fallback to the
    upstream image when optimization fails, and reuse of the previous result
    when the upstream ETag is unchanged.
  - `sendResponse`: `Vary`, `Cache-Control` (immutable for
    `/_next/static/media`), ETag/304, `Content-Disposition` quoted like the
    content-disposition package, CSP, and `X-Nextjs-Cache` MISS/HIT/STALE. A
    stale entry is served while it revalidates.
- `pattern.go`: `hasLocalMatch` / `hasRemoteMatch` with a port of the picomatch
  subset image patterns use (`**`, `*`, `?`, `{a,b}`).
- `runtime/app`: `Meta.Images` and `Meta.BasePath`. With them set,
  `imagesHandler` serves `basePath + images.path` in front of static files and
  the JS handler; `Prerender` unwraps it (`unwrap`). Like `/_next/static`,
  `/_next/image` is served before the proxy/middleware runs.
- **Build** (`internal/frameworks/next/staticimage.go`) ports next-image-loader:
  image imports in both builds (`nextImageLoaderRegex`, unless
  `disableStaticImages`) become `{src, height, width, blurDataURL, blurWidth,
  blurHeight}`. The file is emitted as `static/media/<name>.<sha256[:8]><ext>`,
  and blur placeholders are 8 px images at quality 70 in the source format.
  `Output.Images` / `Output.BasePath` feed `meta.json` (`cmd/og/build.go`).

Deviations:

- The image cache lives in memory (Next keeps `.next/cache/images` on disk).
- **No AVIF encoding** (decision): `gen2brain/avif` is 15 MB of generated Go.
  If `images.formats` selects AVIF, the optimizer falls back to the upstream
  image, as Next does when sharp fails. An AVIF static import gets no blur
  placeholder.
- `gen2brain/webp` `dlopen`s a system libwebp when present (via purego) and
  otherwise uses its wasm2go pure-Go build. `-tags nodynamic` forces the
  pure-Go path; output is the same libwebp either way.

### `next/font` (google and local)

A source transform plus a jshost bridge, both running Next's own code:

- **Transform** (`internal/frameworks/next/font.go`, `transformFonts`) ports
  next-swc's `next_font` transform:
  - Imports of `next/font/{google,local}` and `@next/font/{google,local}` are
    removed; a namespace import is an error.
  - Each module-scope `const x = Fn({...})` becomes
    `import x from "og:next-font:<loader>/target.css?<query>"`, plus
    `export { x }` when exported. The query is Next's `{path, import,
    arguments, variableName}`; `import` is `""` for next/font/local's default
    export, which the local loader requires.
  - It runs on every JS/TS module in both builds, after plugin transforms (the
    og.config `LoadChain` branch included).
  - Errors follow next-swc: a non-const binding, non-literal arguments ("Font
    loader values must be explicitly written literals."), and spreads.
  - It keeps a `const` statement's other declarators as next-swc does
    (`export const a = 1, inter = Inter(), b = 2` splits into the font import,
    `export { inter }`, then `export const a = 1, b = 2`).
  - It ports next-swc's `find_functions_outside_module_scope`: any other
    reference to a font loader (a call inside a function, a re-export, a
    shorthand property) fails with "Font loaders must be called and assigned to
    a const in the module scope".
- **Parser.** `_tsoverlay/ogapi/module.go` adds `ogapi.ParseModule`,
  TypeScript-Go's parser reduced to top-level imports and variable statements
  with byte offsets. Call arguments are converted to JSON like next-swc's
  `expr_to_json`. It also reports declarator spans, callee offsets, and
  `Module.Refs` (identifier references outside import declarations, excluding
  declared names, property keys and member names, but counting shorthand
  properties). Re-run `scripts/sync-typescript.sh` after editing the overlay.
- **Loader.** `og:next-font:` resolves to `/og-next-font/<id>.js` in the
  `og-next-font` namespace (`fontPlugin`, registered before the builds'
  catch-all resolvers). Its default export is css-loader's locals in order
  (`style`, `className`, `variable`), and it imports `/og-next-font/<id>.css`.
  These come from the jshost bridge's `nextFont` (`PluginHost.NextFont`),
  running Next's own pieces:
  - `next/dist/compiled/@next/font/{google,local}/loader`, with an
    `emitFontFile` naming files `static/media/<sha256[:20]>[-s][.p].<ext>`;
  - then `next-font-loader/postcss-next-font`;
  - then css-loader's pure-mode scoping of `.className` / `.variable` to
    `__<name>_<sha1(css)[:6]>`.
- **CSS.** `compileCSS` builds `/og-next-font/*.css` from the loader's CSS
  (`finishCSS` was split out), without the user's PostCSS or CSS plugins (Next's
  font rule has no postcss-loader) and with `/_next/*` left external.
  `entryCSS` links it like any stylesheet, and the same walk fills the
  next/font manifest (`app[entry]` = the `.p.` files; `appUsingSizeAdjust` when
  a file has `-s`), which app-render turns into
  `<link rel="preload" as="font">`. This shares the [[css-pipeline]].
- **Network.** next/font/google downloads with `node:http(s)`, which unenv only
  stubs. `nativeSubstitute` replaces
  `@next/font/dist/google/fetch-resource.js` with a `fetch` version (Go's
  transport honors the proxy variables, as get-proxy-agent does).
  `NEXT_FONT_GOOGLE_MOCKED_RESPONSES` works unchanged.

### `ImageResponse` (`next/og`)

`ImageResponse` needs runtime WebAssembly (resvg for SVG rasterization, yoga
for layout via satori) plus `Intl.Segmenter`. The restricted, lazy wazero
mechanics are documented in [[wasm-on-wazero]]. The asset-level wiring:

- **Source patches** (`patches.go`, `sourcePatches`, applied in `builder.load`;
  each must match exactly once):
  - `server/og/image-response.js`: keep only the `index.edge.js` branch of the
    `NEXT_RUNTIME` ternary. Webpack folds it; esbuild bundled both dynamic
    imports, and the Node build needs `sharp`.
  - `@vercel/og/index.edge.js`: the fallback font, fetched via
    `new URL("./Geist-Regular.ttf", import.meta.url)` (an asset URL under
    webpack; nothing under esbuild's IIFE), becomes a binary import.
- `?module` and binary imports (`internal/frameworks/next/wasmmodule.go`,
  `wasmPlugin`): `x.wasm?module` becomes `new WebAssembly.Module(bytes)`, bytes
  from an `og-binary:` import (esbuild's binary loader), standing in for
  webpack's edge "asset/webassembly" handling.
- `Intl.Segmenter`: satori requires it. This was first a Go primitive
  (rivo/uniseg), later replaced at the user's direction with FormatJS's
  `@formatjs/intl-segmenter` polyfill (`runtime/webapi/js/src/intl.ts`); the
  uniseg dependency is gone.

### Route handler prerendering

- **Next's rules** (`export/routes/app-route.js` `exportAppRoute`, and the node
  app-route template's ISR): a route handler is statically generated only if
  `isStaticGenEnabled(userland)` holds (`dynamic` is `force-static` or `error`,
  `revalidate` is `false` or > 0, or it exports `generateStaticParams`), or it
  is a metadata route. Plain GET handlers stay dynamic (Next 15+). Dynamic
  usage bails out, and the response is cached unless its status is ≥ 400 other
  than 404.
- **Edge wrapper patch** (`server/web/edge-route-module-wrapper.js`,
  `sourcePatches`): under `x-og-static`, for qualifying routes,
  `supportsDynamicResponse` is false, so the work store is in static-generation
  mode. After `handle`, the patch reports `collectedRevalidate` /
  `collectedExpire` (INFINITE_CACHE → false / none) and `collectedTags` in the
  internal headers, as the page patch does.
- **ISR** (`runtime/isr`): a static render that reports cache metadata is
  cacheable whatever its content type. Non-HTML renders are route entries
  (`Entry.Route`). When served, a route entry keeps its own `Cache-Control`,
  and only gets `s-maxage=…` if it has none.
- **Build**: `og build` (and the fixture harness) prerender route handlers too.
  The entry's `staticParams` takes `generateStaticParams` from the route module
  itself when there is no loader tree. Metadata routes like `/robots.txt` are
  prerendered (HIT on the first request).

### Build-time sharp substitute

og had substituted sharp only in packaged apps, so build-time plugins that use
it (Qwik City's vite-imagetools) lost their image features ("image imports are
not available"). The build-time substitute fixes this:

- `internal/jshost/native.go`: a module file at
  `node_modules/{sharp,better-sqlite3}/lib/index.js` is replaced by og's
  substitute (`natives.ModuleSource`), converted to CommonJS once, its default
  export made `module.exports` (named exports as properties, no `default`, as
  the packages' own CommonJS). Both bundling and the runtime require go through
  `nativeSubstitute`.
- `prepare` installs the substitutes' Go side (`natives.Install`, one
  `Services` per host, created under `nativesMu`: worker engines prepare
  concurrently, and a lock in `Host` would be copied with it).
- `runtime/natives/sharp.go` `metadata()` exposes sharp 0.34's fields: `depth`,
  `bitsPerSample`, `density` (72, libvips' default), `isPalette`, `hasProfile`,
  and `autoOrient` (the size once oriented), which imagetools reads. Checked
  against sharp 0.34.5 on Node for the fixture's PNG.

## Files

- `internal/frameworks/next/metadata.go` — static metadata file conventions,
  discovery, route names, generated `og:metadata-image` / `og:metadata-route`
  modules.
- `runtime/imageopt/image.go` — content sniffing, animation detection, sizing,
  and `Optimize` (auto-orient, resize, encode).
- `runtime/imageopt/handler.go` — `/_next/image` request handling.
- `runtime/imageopt/pattern.go` — local/remote pattern matching (picomatch
  subset).
- `internal/frameworks/next/staticimage.go` — next-image-loader port (static
  image imports, blur placeholders).
- `internal/frameworks/next/font.go` — `transformFonts` (next-swc `next_font`
  port) and `fontPlugin`.
- `_tsoverlay/ogapi/module.go` — `ogapi.ParseModule` for the font transform.
- `internal/frameworks/next/wasmmodule.go` — `?module` / binary WASM imports.
- `internal/frameworks/next/patches.go` — `sourcePatches` for image-response,
  @vercel/og, and the edge route-module wrapper.
- `runtime/natives/sharp.go` — build-time sharp substitute `metadata()`.
- `internal/jshost/native.go` — `nativeSubstitute` module replacement.
- `runtime/app` — `Meta.Images`, `Meta.BasePath`, `imagesHandler`.
- `runtime/isr` — route-entry caching.
- `cmd/og/build.go` — `meta.json` with `Output.Images` / `Output.BasePath`.

## Test Coverage

- `TestMetadataRouteNames` — expected names produced by running Next 16.3.8's
  own `get-metadata-route.js` in Goja (a hand-written `(group)` hash guess was
  wrong; the port was right).
- `TestMetadataFiles` (fixture: `favicon.ico`, `icon.png`, `robots.ts`,
  `sitemap.ts`, `about/opengraph-image.png` + `.alt.txt`) — `<link rel="icon">`
  tags, og:image meta tags (URL, width, alt), and the five routes' bodies,
  content types and `Cache-Control`. Neutralized by blanking the tree's
  metadata.
- `TestNextImage` (fixture `app/gallery`: static `photo.jpg` with
  `placeholder="blur"`, `public/square.png`) — rendered `<img>` (size, blur SVG
  JPEG data URL, `src`), emitted media file, WebP negotiation and dimensions
  (never enlarged), immutable `Cache-Control`, `Content-Disposition`, CSP, MISS
  then HIT, 304 on `If-None-Match`, PNG without WebP resized to 48×48 with
  `max-age=14400`, and six 400 error messages. Neutralized by disabling the
  resize ("resized square: 96x96").
- `runtime/imageopt`: `TestGlob`, `TestSniffing`, `TestOrientation` (hand-built
  EXIF orientation-6 JPEG), `TestRemoteImages` (loopback and 10.x refused, then
  `dangerouslyAllowLocalIP` follows a redirect, resizes, honors upstream
  max-age, names the file after the URL). Neutralizing `isPrivateIP` fails both
  refusals.
- `TestTransformFonts` — query shapes for named, aliased, local-default,
  exported and argument-less calls; type-only imports kept; the split of a
  mixed `const`; three scope violations; a false-positive guard (`{ Inter: 1 }`,
  `o.Inter`), where neutralizing `isReference` fails the guard.
- `TestNextFont` (fixture `app/gallery`) — next/font/local with Next's bundled
  Geist woff2 in the page plus a `"use client"` caption reusing the file at
  weight 700 with `preload: false`; checks scoped classes, `style.fontFamily`,
  @font-face and fallback rules (`ascent-override` etc. from fontkit), the
  byte-identical served file, exactly one font preload, and none on `/`.
  Neutralized by dropping the manifest entry ("no font preload link").
- `TestNextFontGoogle` (jshost, mocked responses via a Proxy module) — file
  naming (`-s.p.woff2`), URL rewrite, Inter's fallback metrics, variable rule,
  export order. `TestFetchResourceShim` — an httptest server; fails without the
  fetch substitution.
- `TestImageResponse` (fixture `app/gallery/opengraph-image.tsx` with `size`,
  `contentType`, `alt`) — og:image meta tags (URL with hash, width, type, alt)
  and a decoded 120×63 PNG with the `#7928ca` background. Neutralizing the
  runtime WebAssembly install fails it.
- `TestRouteHandlerCaching` (fixtures `api/build-time` force-static,
  `api/hourly` with `revalidate = 60` and own `Cache-Control`) — both
  prerendered and served HIT with identical bodies; force-static request's
  headers stubbed (`"ua":null`); the route's own Cache-Control kept, default
  `s-maxage=31536000`; `/api/hello` and `/api/posts/[slug]` stay dynamic;
  `/robots.txt` is HIT. Neutralized by dropping reported cache metadata.
- `TestBuildTimeSharp` (a fake native sharp package that throws if loaded) —
  og's sharp used instead, metadata fields, WebP output, `require("sharp")` as
  the function without `default`. `TestServeQwikCity` — the optimized `<img>`
  (srcSet, width, height, asset name) identical to Node's build; both assets
  120×60 WebP. Neutralizations: the substitution (fake native throws) and
  `autoOrient` (missing in metadata).

Browser-verified on packaged binaries: both `next/image` images load through
`/_next/image` with the blur background cleared on load; both font faces load
with correct computed families/weight and clean hydration (neutralizing the
browser-build transform breaks hydration with
`(0 , D0.default) is not a function`).

## Pitfalls

- Next's isomorphic helpers need `process.env.NEXT_RUNTIME = "edge"` in a bare
  Goja VM, or `shared/lib/isomorphic/path` pulls in `require("path")`.
- The `/_next/image` cache is in-memory only; nothing persists across restarts.
- AVIF is a known gap: encoding is refused (falls back to upstream), and AVIF
  static imports get no blur placeholder.
- `-tags nodynamic` is required to keep `gen2brain/webp` off a system libwebp;
  the project gate uses it for this reason.
- `sassOptions` crossing the bridge as JSON loses function-valued options
  unless the live next.config path is used (see [[css-pipeline]]).
- The font transform rejects a `const` that mixes font calls with other
  declarators by splitting; a namespace import of `next/font/*` is an error.
- `Intl.Segmenter` gives no locale tailoring or dictionary segmentation; CJK
  splits per character where ICU would find words (satori text layout only).
