# Development workflow

Oganesson is a production builder and runtime, not a replacement for a
framework's development server. Keep the ordinary Node.js workflow for fast
feedback while editing, and add an Oganesson build as the production target
you test and deploy.

You do not need an Oganesson-specific starter or application layout. Start
with the framework's normal project, keep its `package.json` scripts and
lockfile, and use the same source tree with both toolchains.

## The two workflows

During day-to-day development, install with the project's package manager and
run its development script:

```sh
npm install
npm run dev
```

This is where file watching, hot-module replacement, React Fast Refresh, and
framework development overlays run. Oganesson deliberately does not implement
those development-only facilities.

Before merging or deploying, exercise both production paths:

```sh
# Optional reference build under Node.js.
npm run build
npm run preview                 # Some frameworks call this script "start".

# The artifact that will actually be deployed.
og install .
og check .                      # Optional, but useful in CI.
og build -o ./app-server .
./app-server -addr :3000
```

The Node.js production build is a useful reference, but it is not an input to
`og build` and does not have to run first. Test the Oganesson binary directly:
the embedded JavaScript engine, request isolation, native-package substitutes,
and packaged filesystem can expose differences that a Node.js preview cannot.

### Other package managers

The examples use npm only to keep the commands concrete. The equivalent
development commands are:

| Task | npm | pnpm | Yarn | Bun |
|------|-----|------|------|-----|
| Install | `npm install` | `pnpm install` | `yarn install` | `bun install --save-text-lockfile` |
| Development | `npm run dev` | `pnpm dev` | `yarn dev` | `bun run dev` |
| Production build | `npm run build` | `pnpm build` | `yarn build` | `bun run build` |
| Preview/start | `npm run preview` | `pnpm preview` | `yarn preview` | `bun run preview` |

Use the script name your project already defines. Oganesson reads npm, pnpm,
Yarn, and text Bun lockfiles. Binary `bun.lockb` is not supported, which is why
the Bun example requests `bun.lock`.

## Framework scripts

Framework starters normally create these scripts for you. The table shows the
conventional commands behind them; retain a working starter's scripts when
they differ. In every row, the Oganesson production workflow remains
`og install .` followed by `og build -o ./app-server .`.

| Framework | `dev` | `build` | Node.js production check |
|-----------|-------|---------|--------------------------|
| Next.js (App or Pages Router) | `next dev` | `next build` | `next start` |
| Vite SPA: React, Vue, Svelte, Preact, Solid, or vanilla | `vite` | `vite build` | `vite preview` |
| TanStack Router SPA | `vite` | `vite build` | `vite preview` |
| TanStack Start | `vite dev` | `vite build` | `vite preview` |
| Astro | `astro dev` | `astro build` | `astro preview` |
| VitePress | `vitepress dev .` | `vitepress build .` | `vitepress preview .` |
| React Router framework mode | `react-router dev` | `react-router build` | `react-router-serve ./build/server/index.js` |
| SvelteKit with `adapter-node` | `vite dev` | `vite build` | `vite preview`, or `node build` for the adapter output |
| Nuxt | `nuxt dev` | `nuxt build` | `nuxt preview`, or `node .output/server/index.mjs` |
| SolidStart | `vite dev` | `vite build` | `vite preview`, or `node .output/server/index.mjs` |
| Qwik City | `vite --mode ssr` | `qwik build` | The adapter's preview or serve script |

Tailwind, UnoCSS, and `vite-plugin-pwa` use the Vite row; they do not need a
separate development workflow. Oganesson recognizes the production commands
listed above and runs the relevant framework build inside its own build host.

### Next.js

A typical script set is:

```json
{
  "scripts": {
    "dev": "next dev",
    "build": "next build",
    "start": "next start"
  }
}
```

Use `npm run dev` while editing. Use `npm run build && npm run start` when you
want a Node.js production reference, then build the deployable executable with
Oganesson. Keep flags such as `next build --webpack` in the existing build
script; Oganesson interprets the supported build choice and configuration.

### Vite SPAs and TanStack Router

React, Vue, Svelte, Preact, Solid, vanilla Vite, and TanStack Router projects
normally share this script shape:

```json
{
  "scripts": {
    "dev": "vite",
    "build": "vite build",
    "preview": "vite preview"
  }
}
```

`vite preview` checks Vite's generated client files under Node.js. An
Oganesson binary serves the client output itself with the configured base path
and SPA or MPA fallback behavior.

### Custom Vite SSR servers

Express, Fastify, Koa, Hono's Node adapter, and h3 applications usually own
their server and scripts. Keep that normal setup. For example:

```json
{
  "scripts": {
    "dev": "node server",
    "build": "npm run build:client && npm run build:server",
    "build:client": "vite build --outDir dist/client",
    "build:server": "vite build --ssr src/entry-server.js --outDir dist/server",
    "preview": "NODE_ENV=production node server"
  }
}
```

Use a cross-platform environment helper instead of the inline assignment when
the project needs to run on Windows. Oganesson follows the Vite invocations in
the build script and inspects a `preview`, `start`, or `serve` script to find
the production server program. Keep the output paths passed to that program in
the script rather than hiding them in an unrelated shell wrapper.

### TanStack Start

Use the starter's Vite scripts for development, build, and preview. Oganesson
packages the server environment written by the Start plugin, including SSR,
server functions, middleware, routes, and prerendered output. Test mutations
and streamed responses against the executable as well as the Vite server.

### Astro

Use `astro dev`, `astro build`, and `astro preview` through package scripts.
For server output, keep `@astrojs/node` configured in standalone mode;
Oganesson packages that generated server. Static output is packaged as a
multi-page site.

### VitePress

Use `vitepress dev .` while writing and `vitepress build .` plus `vitepress
preview .` for the Node.js production check. The current Oganesson build host
requires the safe configuration described in the [VitePress support
notes](vite.md#meta-frameworks), including disabling `lastUpdated`, automatic
bare-URL linkification, and native CSS minification.

### React Router

Use the framework's `react-router dev` and `react-router build` commands. A
production `start` script commonly invokes `react-router-serve` with the built
server entry. Oganesson reads that script so it can package both the entry and
its client assets.

### SvelteKit

Use Vite's development and build scripts as generated by SvelteKit. With
`@sveltejs/adapter-node`, `node build` runs the Node.js production output;
`vite preview` is useful for a local preview. Oganesson packages the
adapter-node server and output files.

### Nuxt

Use `nuxt dev`, `nuxt build`, and `nuxt preview`. The direct production
equivalent is normally `node .output/server/index.mjs`. Oganesson runs Nuxt's
build APIs, packages Nitro's output, and uses that server entry in the
executable.

### SolidStart

Use `vite dev`, `vite build`, and `vite preview`. A Node deployment normally
runs `.output/server/index.mjs`; Oganesson packages the corresponding Nitro
output and server program.

### Qwik City

Qwik starters split their build into scripts such as `build.client`,
`build.server`, and `build.preview`, coordinated by `qwik build`. Keep those
generated scripts rather than flattening them. Use the starter's `dev` script
for the Vite SSR development server and its adapter-provided `serve` or
`preview` script for the Node.js production check. Oganesson understands the
`qwik build` sequence and packages the Node server adapter.

## Dependency installation and code generation

An ordinary package-manager install and `og install` both populate
`node_modules`, but they have different jobs:

- npm, pnpm, Yarn, or Bun may run lifecycle scripts and install native packages
  for the development machine;
- `og install` never runs lifecycle scripts, skips arbitrary native optional
  packages, and adds known pure-JavaScript or WebAssembly substitutes needed by
  the Oganesson build; and
- the lockfile is the shared source of dependency versions.

Run the ordinary install before using the Node.js development lane and rerun
`og install` after the manifest or lockfile changes before validating the
Oganesson lane. In CI, separate jobs or clean worktrees make the two dependency
trees easiest to reason about.

If a framework needs code generation, put it in an explicit checked-in script
instead of relying only on an install hook. The Node.js lane can run any tool
available to it. Oganesson's build host runs the supported framework and
code-generation commands documented in [Project configuration](configuration.md#build-scripts),
but it does not provide a general shell or native executable runner.

## Oganesson-only code during Node.js development

Node.js does not know how to load `og:kv` or a project-owned `og:<name>` Go
module. If a route imports one directly, the ordinary framework server cannot
execute that route without a project-provided Node.js adapter. Keep the
Oganesson API behind a small server-only interface and select a Node.js
implementation with the framework's normal alias or conditional-resolution
mechanism when development parity is important. Otherwise, use the Node.js
server for unaffected UI work and rebuild the Oganesson executable to test
that route.

The same principle applies to behavior backed by Oganesson's native database,
image, or filesystem substitutes. A passing Node.js development session proves
the application-level path, not compatibility with the packaged runtime.

## A practical check before deployment

At minimum, exercise these against the packaged executable:

- initial HTML and browser hydration;
- client-side navigation and lazy chunks;
- forms, server actions/functions, loaders, and API routes;
- streamed responses and error paths;
- static files, images, CSS, base paths, and fallback routing; and
- any database, `og:*`, native-package, or filesystem-dependent behavior.

See [Packaging and deployment](deployment.md) for target binaries and
[Compatibility and limitations](compatibility.md) for runtime boundaries.
