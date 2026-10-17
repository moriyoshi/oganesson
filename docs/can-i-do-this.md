# Can I do this with Oganesson?

Probably, if your application has a conventional Next.js or Vite production
build and does not depend on a Node.js development server, arbitrary native
addons, or an inbound WebSocket server. This page is a practical tour of what
Oganesson can do today. Follow the links for exact behavior and limitations.

## Build without Node.js

### Can I install JavaScript dependencies without npm, pnpm, or Yarn?

Yes. `og install` downloads npm-registry packages, verifies their integrity,
and populates `node_modules` without invoking a package manager or lifecycle
scripts. It understands npm and pnpm lockfiles, Yarn v1 and Berry locks, text
`bun.lock` files, and common workspace layouts. A persistent download cache is
enabled by default.

Packages that normally require selected native addons are replaced where a
compatible pure-JavaScript, WebAssembly, or Go-backed implementation exists.
Examples include bcrypt, sharp, SQLite, PostgreSQL, MySQL, Lightning CSS, and
Rollup. See [package and native-module compatibility](compatibility.md#package-installation).

### Can I type-check TypeScript?

Yes. `og check` uses TypeScript-Go, reads the project's `tsconfig.json`, and
reports normal file, line, and column diagnostics. It also generates
`og-env.d.ts` so the built-in `og:kv` API and project-owned Go modules have
TypeScript declarations.

### Can I use my existing framework configuration?

Yes. Oganesson evaluates JavaScript and TypeScript configuration in its
embedded build host. It uses Next.js and Vite's own configuration and build
logic, and understands associated PostCSS, Tailwind, Babel, Prisma, and
framework configuration files. An optional `og.config.*` adds esbuild plugins
and webpack-style loader rules. See [Project configuration](configuration.md).

## Build web applications

### Can I build a Next.js App Router application?

Yes. The implemented App Router surface includes:

- Server and Client Components, streaming SSR, hydration, and soft navigation;
- layouts, templates, loading and error boundaries, not-found handling, and
  dynamic segments;
- route handlers, including streamed request and response bodies;
- module-level and inline Server Actions, including form submissions,
  redirects, cookies, headers, and revalidation;
- `proxy.ts` and `middleware.ts`, including Next.js matchers;
- static generation, `generateStaticParams`, ISR, on-demand revalidation,
  Cache Components, and partial prerendering; and
- public assets, CSS, images, fonts, and metadata routes.

Oganesson uses Next.js's own entry templates, renderers, React Server
Components implementation, client router, and manifests. Read the complete
[Next.js guide](nextjs.md#app-router).

### Can I build a Pages Router application?

Yes. Pages Router support includes `getServerSideProps`, `getStaticProps`,
`getStaticPaths`, all three fallback modes, `getInitialProps`, custom `_app`
and `_document`, API routes, `/_next/data`, preview mode, error pages, and
time-based revalidation. App and Pages Routers can coexist, and Pages Router
applications using React 18 are covered.

### Can I use normal Next.js routing options?

Yes. `basePath`, `trailingSlash`, Pages Router internationalized routing,
locale detection, domain locales, proxy/middleware matching, canonical
redirects, and locale-aware static paths are implemented. Next.js redirects,
rewrites, headers, cookies, route handlers, API routes, and dynamic segments
follow the production server path.

### Can I build a Vite SPA?

Yes. Oganesson runs Vite 5 through 8 production builds. Validated stacks
include React, Vue, Svelte 5, Preact, Solid, Tailwind CSS 4, UnoCSS, TanStack
Router, and `vite-plugin-pwa`. Assets, CSS, aliases, source maps, manifests,
dynamic imports, `import.meta.glob`, base paths, and common plugin hooks are
supported.

Vite 5 through 7 use Rollup's WebAssembly build. Vite 8 uses a
Rolldown-compatible API backed by Go esbuild. Output chunking can differ from a
native Node.js build even when application behavior and URLs match. See the
[Vite build model](vite.md#vite-versions-and-build-model).

### Can I use a Vite-based framework?

Yes, for the validated production adapters:

- TanStack Start;
- Astro with its Node adapter;
- VitePress static documentation sites;
- React Router framework mode;
- SvelteKit with `adapter-node`;
- Nuxt and Nitro;
- SolidStart; and
- Qwik City with its Node server adapter.

These use their normal build and start scripts; an Oganesson-specific server
entry is not required. See [Vite framework support](vite.md#meta-frameworks).

### Can I package an existing Node HTTP server?

Often. Vite SSR programs using Express, Fastify, Koa, Hono with
`@hono/node-server`, or h3 are validated. Oganesson captures the program's
`http.createServer()` listener and dispatches requests through its Go server.
Static assets, streams, dynamic imports, and packaged filesystem reads work in
that model.

## Use styling, images, and other assets

### Can I use CSS Modules, Tailwind, Sass, or styled-jsx?

Yes. The Next.js pipeline supports global CSS, CSS Modules, PostCSS, Tailwind
CSS 3 and 4, Sass, Lightning CSS, styled-jsx, emitted assets, source maps, and
CSS ordering. The Vite pipeline supports framework CSS plugins, CSS-only
modules, emitted assets, and generated utility CSS.

### Can I use `next/image`, `next/font`, and generated metadata?

Yes. Static image imports, blur placeholders, and the runtime image optimizer
support the common JPEG, PNG, WebP, and GIF paths. `next/font/google` and
`next/font/local`, font preloads, `ImageResponse`, icons, Open Graph and Twitter
images, favicons, manifests, robots files, and sitemaps are implemented. The
optimizer does not emit AVIF, and Google font downloads need build-time network
access.

## Run server-side JavaScript

### Can I use Web Platform APIs?

Yes. Server code has `fetch`, Request and Response, Headers, forms, Blob and
File, URLs, Web Streams, encoding, timers, abort signals, structured cloning,
compression streams, Web Crypto, and runtime WebAssembly. The runtime is
covered by a pinned subset of Web Platform Tests. Browser-only APIs such as the
DOM and browser storage are not installed in the server VM.

### Can I use Node.js built-ins?

Many of them. Operational implementations include Buffer, `fs`, `path`,
streams, crypto, zlib, AsyncLocalStorage, events, util, assert, module,
process, VM primitives, the HTTP server surface used by packaged programs, and
outbound TCP and TLS sockets. A broader unenv surface supports package feature
detection, but less commonly used methods can still be stubs. See
[Runtime compatibility](runtime.md#nodejs-compatibility).

### Does module-global state persist between requests?

No. Every request receives a fresh Sobek VM and event loop. This prevents
module globals, timers, and unfinished promises from leaking into later
requests. Shared state belongs in a Go-backed process service or an external
system. Oganesson's ISR, Cache Components, optimized-image, database, and
`og:kv` services deliberately live outside request VMs.

### Can server code read and write files?

Yes, with an important distinction. Packaged application files are mounted
read-only at `/`; writes enter a shared in-memory overlay that lasts until the
process exits. Use absolute host paths for durable files and SQLite databases.
The executable itself is never modified by runtime writes.

### Can server code make network and TLS connections?

Yes. Server-side `fetch`, outbound `net.Socket`, and outbound `tls.TLSSocket`
use Go networking. This supports HTTP services and the native database drivers.
TLS verification uses the operating system trust store, so minimal container
images need CA certificates.

### Can I use WebAssembly?

Yes. The build host supports WebAssembly, WASI, worker-based WASI threads, and
the bridges required by packages such as Tailwind oxide, Lightning CSS,
Rollup, Qwik's optimizer, Astro's Markdown stack, and Prisma's schema engine.
Runtime WebAssembly and server-side `.wasm` imports are also available.

## Store and query data

### Is there a built-in key-value store?

Yes. `og:kv` provides a Deno-KV-style API with structured tuple keys,
expiration, ordered prefix/range listing, cursors, optimistic checks, atomic
set and delete operations, and unsigned 64-bit sum, minimum, and maximum
operations. It is process-local memory: data survives across requests but not
process restarts or replicas.

### Can I use SQLite, PostgreSQL, or MySQL?

Yes. Go-backed substitutes provide the commonly used APIs of `better-sqlite3`,
local `@libsql/client`, `pg`, `postgres`, and `mysql2`. The services can reuse
database pools across isolated request VMs. Use an absolute host path for
persistent SQLite; PostgreSQL and MySQL normally point to external servers.

### Can I use an ORM?

Validated combinations include:

- Drizzle with SQLite, local libSQL, node-postgres, postgres.js, and MySQL;
- Kysely with SQLite, PostgreSQL, and MySQL;
- TypeORM with better-sqlite3, decorators, relations, and transactions; and
- Prisma 7 with SQLite and PostgreSQL driver adapters.

Oganesson can run `prisma generate` during the build and package Prisma's
WebAssembly schema engine. Startup migrate-deploy is implemented for SQLite
and PostgreSQL; SQLite `prisma db push` is also validated. See
[Data, databases, and ORMs](data.md).

## Customize the build and runtime

### Can I use esbuild plugins or webpack loaders?

Yes, within the supported adapters. `og.config.*` can register JavaScript
esbuild plugins for all, client, server, or CSS builds and can add
webpack-style loader rules. Next.js `webpack()` aliases, fallbacks,
definitions, and representable loader rules are applied. Compatible Turbopack
loader rules and validated Babel transforms are supported. Arbitrary webpack
plugins and every Rollup or Rolldown plugin behavior are not guaranteed.

### Can I call my own Go code from JavaScript?

Yes. Put a Go module under the project's `og/` directory. Exported functions
become typed `og:<directory>` imports; Oganesson generates the JavaScript
bridge, registration code, and TypeScript declarations. Functions accepting a
`context.Context` can run asynchronously and receive request cancellation.

The final application build needs the Go toolchain, and an Oganesson checkout
only when `og` is an unpublished (locally built) binary. See
[Native Go modules](native-go-modules.md).

### Can I add Go HTTP middleware or routes around the application?

Yes. `og build -eject` creates a standalone Go module with a user-owned
`wrap.go`. Add routes, middleware, observability, signing, or other Go server
integration there, then compile it as a normal Go program. Re-ejecting updates
generated application files while preserving the intended user-owned files.

## Package and operate the application

### Can I deploy one executable?

Yes. `og build -o ./my-app` creates one executable containing the server
runtime, application code, browser assets, public files, prerendered responses,
JavaScript engine, compatibility layers, image codecs, and applicable license
notices. It does not need Node.js or a sidecar `node_modules` directory.

### Can I cross-compile it or run it in a minimal container?

Yes. Supply a matching CGO-free Oganesson runtime for the target OS and
architecture. The resulting application can run in a minimal or distroless
container; include CA certificates and time-zone data when the application
needs them. Projects with native Go modules compile their own target runtime.

### Can I inspect the build instead of creating an executable?

Yes. `og build` writes `.og/`, and `og serve` serves that directory without
rebuilding it. The directory contains server code, browser assets, public
files, build metadata, prerendered entries, traced files, and notices.

### Can I configure ports, environment variables, and shutdown behavior?

Yes. Servers default to `:3000`, honor `PORT`, and accept `-addr`. Application
server code reads normal process environment variables. `SIGINT` and `SIGTERM`
perform graceful shutdown. Packaged applications also provide `-licenses` to
print Oganesson and shipped application notices.

## What can't I do yet?

The most important current boundaries are:

- there is no Oganesson development server, file watching, HMR, or React Fast
  Refresh;
- inbound WebSocket upgrades, `net.Server`, and `tls.Server` are not
  implemented;
- arbitrary `.node` addons and child-process/native-executable workflows do
  not work unless Oganesson has a specific substitute;
- process-local KV, caches, optimized images, and virtual filesystem writes are
  not durable or shared across replicas;
- native build-plugin internals can differ, and arbitrary untested webpack,
  Vite, Rollup, or Rolldown plugins are not guaranteed; and
- Sobek is not V8, so exact stack traces, generated function names, and rare
  reflection edge cases can differ from Node.js.

For a decision checklist and the detailed boundaries, read
[Compatibility and limitations](compatibility.md#choosing-oganesson). Validate
your application using its production database, browser flows, and packaged
executable before release.
