# Long-Term Memory Index

Durable, topic-organised project knowledge distilled from `JOURNAL.md` by the
`good-sleep` and `deep-sleep` skills. The canonical
`## LTM Consolidation Record` in [../JOURNAL.md](../JOURNAL.md) holds the
journal-to-source mapping. Start with a synthesis document for orientation,
then follow its included-document links for implementation history and detail.

## Synthesis documents

| Document | Source documents consolidated | Summary |
|----------|-------------------------------|---------|
| [runtime-platform-synthesis.md](runtime-platform-synthesis.md) | foundations-and-architecture, runtime-engine, js-language-gaps-and-lowering, unicode-regexp-intl, wasm-on-wazero, node-compat-shims, webcrypto | Pure-Go Sobek runtime architecture, isolation, lowering, Web/Node APIs, Intl, WebAssembly, and WebCrypto. |
| [next-rendering-and-routing-synthesis.md](next-rendering-and-routing-synthesis.md) | app-router-rendering, route-handlers-actions-middleware, static-generation-and-isr, pages-router, cache-components-use-cache | End-to-end Next request lifecycle across both routers, middleware, actions, prerendering, ISR, and Cache Components. |
| [next-build-and-assets-synthesis.md](next-build-and-assets-synthesis.md) | build-system-esbuild-layers, css-pipeline, assets-images-fonts-metadata, compat-corpus-m11 | Next build layers and configuration, CSS/assets/fonts/images, and corpus-driven compatibility hardening. |
| [vite-ecosystem-synthesis.md](vite-ecosystem-synthesis.md) | vite-and-rolldown-facade, vite-framework-ecosystems, tanstack, server-programs-ssr | Vite's Rolldown facade, static and SSR delivery models, framework ecosystems, and TanStack support. |
| [packaging-and-native-extension-synthesis.md](packaging-and-native-extension-synthesis.md) | install-and-packaging, native-go-modules | Dependency installation, executable packaging, built-in substitutes, and project-defined Go modules. |
| [data-and-orm-synthesis.md](data-and-orm-synthesis.md) | databases-drivers-and-orms, prisma | Process-wide database services, driver/ORM compatibility, packaged data files, and Prisma query/migration flows. |
| [project-delivery-synthesis.md](project-delivery-synthesis.md) | harness-and-project-meta, user-documentation-and-site | Repository process, CI/release, license attribution, user guides, and VitePress deployment. |

## Source topic documents

These one-topic documents are preserved for traceability and detailed
implementation guidance. They are refreshed through the 2026-10-16 journal
entries; none is intentionally standalone outside the synthesis set.

## Core runtime and engine

| Document | Summary |
|----------|---------|
| [foundations-and-architecture.md](foundations-and-architecture.md) | Founding goals and constraints (one self-contained Go binary, pure-Go/no-Node, per-request isolated VMs, correctness over speed), the M0 feasibility table, and version pins. |
| [runtime-engine.md](runtime-engine.md) | The per-request Sobek engine and event loop, the Go-backed cross-request services (`runtime/kv`, `runtime/isr`), the Goja to Sobek migration, startup-output-once, and uncaught-error hooks. |
| [js-language-gaps-and-lowering.md](js-language-gaps-and-lowering.md) | Goja/Sobek language gaps and the bundle-time `gojatarget.*` workarounds, the thenable async-context fix, the esbuild renamer/define races, and `.name` preservation. |
| [unicode-regexp-intl.md](unicode-regexp-intl.md) | `Intl` from FormatJS and `x/text/collate`, RegExp `\p{...}` property escapes (rewriter now deleted), and the `v`-flag set-notation / properties-of-strings emulation. |
| [wasm-on-wazero.md](wasm-on-wazero.md) | The wazero-backed `WebAssembly` host (`internal/wasm`), napi-wasm/emnapi, wasi-threads, the restricted runtime global, and the wasm consumers (oxide, lightningcss, next/og, Shiki). |
| [node-compat-shims.md](node-compat-shims.md) | The Node compatibility layer: unenv stubs vs. Node's own vendored modules (fs streams, path, web streams, zlib, `_http_common`), and `path.matchesGlob`. |
| [webcrypto.md](webcrypto.md) | WebCrypto asymmetric algorithms, key formats, Go/JavaScript representation boundaries, and regression coverage. |
| [build-system-esbuild-layers.md](build-system-esbuild-layers.md) | The esbuild build layers and `og-*` namespaces, the JS plugin architecture, the webpack loader shim, build-time `require`, and the unenv preset. |

## Next.js support

| Document | Summary |
|----------|---------|
| [app-router-rendering.md](app-router-rendering.md) | Server-rendered App Router (RSC/SSR), the browser bundle, hydration, and client/soft navigation. |
| [route-handlers-actions-middleware.md](route-handlers-actions-middleware.md) | Route handlers, Server Actions (module-level and inline `"use server"` closures), and proxy/middleware. |
| [static-generation-and-isr.md](static-generation-and-isr.md) | Static generation, the `runtime/isr` page cache, time-based and on-demand revalidation, and bounded cache keys. |
| [pages-router.md](pages-router.md) | The Pages Router: pages/API routes, `getStaticPaths` fallback modes, SSG consistency, draft/preview, on-demand revalidation, and i18n. |
| [cache-components-use-cache.md](cache-components-use-cache.md) | Cache Components / `"use cache"`: the transform, the shared cache handler, static and partial prerendering, custom handlers, and `cacheLife`. |
| [css-pipeline.md](css-pipeline.md) | PostCSS and Tailwind v3/v4, Sass, CSS Modules `composes`, lightningcss, live sassOptions, and the oxide perf explanation. |
| [assets-images-fonts-metadata.md](assets-images-fonts-metadata.md) | Static metadata files, `next/image` (`runtime/imageopt`), `next/font`, `ImageResponse` (next/og), and the build-time sharp substitute. |
| [compat-corpus-m11.md](compat-corpus-m11.md) | The `og compat` corpus harness, the `webpack()`/Babel/Turbopack build hooks, build-script execution, Next 16.4.0, and bundler selection. |

## Vite support

| Document | Summary |
|----------|---------|
| [vite-and-rolldown-facade.md](vite-and-rolldown-facade.md) | Running Vite's own code on a Rolldown-API facade over esbuild: SPA/VitePress builds, facade gaps, chunk naming, and Vite 5-to-7 compatibility. |
| [vite-framework-ecosystems.md](vite-framework-ecosystems.md) | Per-framework Vite plugin support: Vue, Svelte, Tailwind, Preact, Solid, UnoCSS, Astro, React Router, SvelteKit, Nuxt, SolidStart, Qwik, PWA. |
| [tanstack.md](tanstack.md) | TanStack Router (SPA) and TanStack Start (SSR, server functions, streaming, RSC, SPA mode). |
| [server-programs-ssr.md](server-programs-ssr.md) | The "server program" SSR model: a project's node:http server run per request; Express/Fastify/Koa/Hono/h3 parity and provider environments. |

## Install, packaging, data, and project meta

| Document | Summary |
|----------|---------|
| [install-and-packaging.md](install-and-packaging.md) | `og install` (lockfiles, resolution speed-up) and `og build` packaging: cross-platform binaries, `-eject`, `-readable`, and native-module substitutes. |
| [databases-drivers-and-orms.md](databases-drivers-and-orms.md) | The driver layer (net/tls shims, the Go connection pool, native pg/postgres/mysql2/libsql shims, pglite, the writable overlay FS) and Drizzle/Kysely/TypeORM. |
| [prisma.md](prisma.md) | Prisma 7: `prisma generate`, the driver adapters (incl. the adapter-pg hang root causes), and Prisma Migrate (`migrate deploy` + `db push`) via the WASM schema engine. |
| [native-go-modules.md](native-go-modules.md) | User-defined Go functions as `og:<name>` modules: `runtime/native`, `internal/nativegen`, value conversion, `.d.ts` generation, and the toolchain build. |
| [harness-and-project-meta.md](harness-and-project-meta.md) | The agent coding harness, the gots to Oganesson rename and naming conventions, CI/CD, and the third-party-notices/license system. |
| [user-documentation-and-site.md](user-documentation-and-site.md) | The user-guide source of truth, VitePress authoring site, branding/theme, self-hosted build, and GitHub Pages deployment. |
