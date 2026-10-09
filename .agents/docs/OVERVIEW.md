# Oganesson Overview

Oganesson (`github.com/moriyoshi/oganesson`, Go 1.27) compiles an existing Next.js project (and, later, a Vite project) into **one self-contained executable**. The executable serves the whole app: server and client components (RSC, SSR, hydration), route handlers, middleware (`proxy.ts`), server actions, static assets, `public/`, and ISR. Node.js is required neither at build time nor at runtime.

## Decisions already made (do not re-litigate)

| Decision | Consequence |
|----------|-------------|
| Pure Go, no Node at build time or at runtime | Build-time JavaScript (next.config, esbuild JS plugins, webpack loaders, prerendering) runs in Sobek. Packages are installed by Oganesson's own installer (`og install`). |
| Next.js first, Vite later | Next.js (App and Pages Router) is fully supported; Vite projects (M9) build and serve, from SPAs to TanStack Start, Astro, React Router, SvelteKit, Nuxt, SolidStart and Qwik City (TanStack added 2026-10-07 at the user's request). |
| Speed is not a criterion | Fidelity to `next start` wins over throughput. |
| Per-request isolated VMs | Every request runs in a fresh Sobek VM. The bundle is compiled once per process (~670 ms) and each request costs ~14 ms of VM setup. |
| Built-in KV store following the Deno KV API, memory only for now | `import { openKv } from "og:kv"`. It is shared across request VMs through Go. |
| No WebSockets for now (user decision 2026-10-08) | `server.on("upgrade")` is not adapted; long-lived connections do not fit per-request VMs. NestJS waits for decorator metadata (M10, with TypeORM). |
| Serve as the framework's own server does (user decision 2026-10-08) | Where a framework's production server and a static host would differ (TanStack Start's SPA mode), og follows the framework's server: Start renders pages in full and `_shell.html` is a static file. |
| Reuse prior work over hand-written shims | goja_nodejs/require is the build-time CommonJS loader. unenv provides the Node built-in surface. web-streams-polyfill provides Web Streams. Next's own templates and `loadConfig` drive the build. |

## Building blocks

- **TypeScript-Go** (`third_party/typescript`, behind the `_tsoverlay/ogapi` facade) provides `og check` and tsconfig resolution.
- **esbuild** (Go API) does every transform and bundle: the RSC server build, the SSR build, the browser build, CSS, and on-demand CommonJS conversion at build time.
- **Sobek** (Grafana's fork of Goja, since 2026-10-08; goja_nodejs's `require` vendored for it) runs JavaScript in two places:
  - in the produced binary (`runtime/`), with a WinterCG Web API surface;
  - at build time (`internal/jshost`), with a Node-like surface.

## How a build flows

1. `og install` materializes `node_modules` without npm.
2. `og build`:
   - loads next.config through Next's own `loadConfig`;
   - starts the JS plugin host when a `og.config.*` exists;
   - scans the app tree;
   - runs esbuild to produce the edge-runtime server bundle and the browser bundle;
   - computes manifests and CSS;
   - prerenders static routes.
3. `-o` appends a zip payload to the Oganesson executable to make the app binary.
4. When run, the app binary finds its payload and serves it.

See [../../ARCHITECTURE.md](../../ARCHITECTURE.md) for the full design.

## Milestones

| Milestone | Scope | State |
|-----------|-------|-------|
| M0 | Feasibility spikes (React in Goja, feature table, TS-Go overlay, AsyncLocalStorage) | done |
| M1 | Runtime core: per-request VM, event loop, Web APIs, WPT subset | done |
| M2 | Server-only App Router, packaged binary | done |
| M3 | Client components, hydration, soft navigation | done |
| M4 | Route handlers, middleware, server actions, KV | done |
| M5 | Static generation and ISR | done |
| M6 | Assets: CSS, plugins, fonts, images | done |
| M7 | Lockfile installer, cross-platform packaging, eject | done |
| M8 | Pages Router | done (follow-ups in TODO.md) |
| M9 | Vite, including TanStack (Router SPA, TanStack Start) | in progress: Vite 5 to 8 SPAs and plugin ecosystems; TanStack Router and Start (SSR, server functions, streaming, RSC); Node server programs (Express, Fastify, Koa, Hono, h3); Astro, VitePress, React Router, SvelteKit, Nuxt, SolidStart, Qwik City. Open: chunk grouping (esbuild's splitting where Rolldown's is expected; names follow Rolldown), minor facade gaps (TODO.md). WebSockets: not supported (user decision) |
| M10 | Databases and ORMs: Drizzle, Kysely, TypeORM, Prisma, and the drivers under them | done (2026-10-09): Go-native sockets with a protocol-level pool (PostgreSQL, MySQL), SQLite in Go, the four ORMs matching `next start`; native Go modules (`og:<name>`) alongside. Follow-ups in TODO.md |
| M11 | Compat corpus and hardening | in progress: `og compat` over Next's examples (`scripts/sync-next-examples.sh`, v16.4.0): 148 of 225 pass, every other failure explained (`testdata/compat/expected.json`, or an external service); package dependency resolution speed-up done (added 2026-10-10 at the user's request) |

Each milestone has a dated entry in [JOURNAL.md](./JOURNAL.md). Open work is tracked in [TODO.md](./TODO.md).
