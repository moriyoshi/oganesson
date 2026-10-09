# Vite and framework support

Oganesson runs Vite's own configuration, environment, builder, and plugin code
inside its build host. Bundling is performed by Rollup or by a
Rolldown-compatible facade over Go esbuild, depending on the installed Vite
major version.

## Vite versions and build model

- Vite 5 through 7 use the WebAssembly build of Rollup and Oganesson's Go-backed
  replacement for the `esbuild` npm package.
- Vite 8 uses Oganesson's Rolldown API facade over Go esbuild.
- The build script's `vite build` invocations and CLI flags are honored.
- Client assets are served with Vite-preview-style fallback and base-path
  behavior.
- Server environments are rebundled for Sobek with Node built-ins and `og:*`
  modules available.
- CSS-only modules, emitted assets, dynamic-import preload lists, manifests,
  source maps, plugin hooks, aliases, `import.meta.glob`, and dynamic-import
  variables are supported.

The facade follows Rollup plugin order and contexts for resolution, loading,
transformation, `emitFile`, `resolveFileUrl`, `renderChunk`, `generateBundle`,
and `writeBundle`. Chunk membership still follows esbuild's splitter, so output
graphs may differ from native Rolldown even when URLs and behavior match.

Set `OG_VITE_TRACE=1` during a build to log plugin hook calls.

## Validated SPA stacks

The following static/client application combinations have repository fixtures:

| Stack | Covered behavior |
|-------|------------------|
| React | JSX/TypeScript, assets, CSS, lazy chunks, base paths |
| Vue | Vue SFCs, `<script setup lang="ts">`, scoped styles |
| Svelte 5 | runes, TypeScript, scoped styles, transform source maps |
| Preact | `@preact/preset-vite` and its Babel path |
| Solid | `vite-plugin-solid` |
| Tailwind CSS 4 | `@tailwindcss/vite` and oxide WebAssembly |
| UnoCSS | generated utility CSS |
| TanStack Router | file-based SPA routing, loaders, navigation |
| vite-plugin-pwa | `generateSW`, service-worker output, manifest, precache |

## SSR server programs

If the project builds an SSR entry and its `preview`, `start`, or `serve` script
runs `node <file>` or a dependency's server binary, Oganesson packages that
program and its build outputs.

At runtime the program executes in each request's isolated VM. Its
`http.createServer(...).listen()` call does not open another listening socket;
Oganesson hands the current request to the registered listener and converts the
Node response back to a Web `Response`. This model is validated with:

- Express;
- Fastify;
- Koa;
- Hono with `@hono/node-server`; and
- h3.

The server program can read its packaged build outputs through `node:fs`, use
`import.meta.url`, perform dynamic imports of packaged modules, stream a
response, and serve static assets. Startup logging is emitted once per process
even though the program is initialized in request VMs.

Server programs cannot accept WebSocket upgrades or open their own inbound
`net.Server`/`tls.Server` listeners.

## TanStack Start

Validated TanStack Start behavior includes:

- SSR and browser hydration;
- route loaders;
- GET and POST server functions;
- request middleware and server routes;
- deferred data and streaming;
- prerendered routes;
- SPA mode while retaining the framework server's rendering semantics;
- redirects, not-found responses, headers, cookies, and errors; and
- the React Server Components variant with its provider environment.

One upstream Start limitation also occurs under real Vite: a server function
reachable only through a client component nested in a server component may not
be registered. Reference it from the route graph.

## Meta-frameworks

The following production build and server shapes are validated:

| Framework | Implemented shape |
|-----------|-------------------|
| Astro | Static output and server output with `@astrojs/node`; Markdown and Shiki/Oniguruma WebAssembly paths |
| VitePress | Static documentation sites through VitePress's own build and page-rendering API; custom themes, local search, clean URLs, and public assets |
| React Router framework mode | SSR, loaders, actions, resource routes, prerendering, and `react-router-serve` |
| SvelteKit | SSR, server loads, form actions, endpoints, prerendering, CSS, error pages, and `adapter-node` |
| Nuxt | Nuxt build, Nitro server output and prerendering |
| SolidStart | Vite/Nitro build and server output |
| Qwik City | Qwik build scripts, optimizer WebAssembly, and the Node server adapter |

Oganesson recognizes these frameworks' normal build and start scripts; no
Oganesson-specific server entry is required.

VitePress currently needs build-host-safe options: disable `lastUpdated`
(it spawns Git through `child_process`), Markdown-it's automatic `linkify`,
and native CSS minification. Explicit Markdown links and unminified CSS work
normally. This site's [VitePress config](https://github.com/moriyoshi/oganesson/blob/main/docs/.vitepress/config.mts)
is a complete working example.

## Filesystem and deployment behavior

Static Vite apps package the client output. SSR server-program applications
package the build outputs under the runtime virtual filesystem and use `/` as
their working directory. The packaged layer is readable; writes go to a shared
in-memory overlay for the lifetime of the process.

Use an external filesystem path, database, or service for persistent state.
Writing a relative file inside the packaged filesystem does not modify the
executable and does not survive a restart.

## Vite-specific limits

- `manualChunks`, advanced chunk groups, and code-splitting groups influence
  names and warnings but do not move modules between esbuild's chunks.
- `augmentChunkHash` is not implemented; hashes are esbuild's pre-render values.
- `this.parse`/`parseAst` does not parse TypeScript syntax.
- Plugin preview-server middleware is not generally run. Supported frameworks'
  built servers are served directly instead.
- Vite development-only native-binding paths, the dev server, dependency
  optimizer, and HMR are outside the production build path and unsupported.
- Browser and server output can differ structurally from native
  Rollup/Rolldown output, including chunk grouping and renamed identifiers in
  stringified functions. Validated behavior, URLs, statuses, and data match.
