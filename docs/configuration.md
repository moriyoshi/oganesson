# Project configuration

Oganesson consumes the configuration already present in a Next.js or Vite
project. It adds one optional file, `og.config.*`, for build extensions and one
generated declaration file, `og-env.d.ts`.

## Supported config-file formats

The build host evaluates JavaScript and TypeScript configuration inside Sobek.
It supports CommonJS and ESM forms used by the frameworks, including top-level
await where the consuming tool permits it.

Relevant files include:

- `next.config.js`, `.mjs`, `.ts`, and their supported variants;
- `vite.config.js`, `.mjs`, `.ts`, and their supported variants;
- framework configs such as Astro, VitePress, React Router, SvelteKit, Nuxt,
  and Qwik;
- `postcss.config.*`, `tailwind.config.*`, Babel configs, and `prisma.config.*`;
- `og.config.js`, `.mjs`, or `.ts`; and
- `package.json` scripts used to select Vite and framework build steps.

The build host provides Node-like module loading, filesystem access within the
project, worker threads, and WebAssembly. Native `.node` addons cannot load;
known replacements are described in [Compatibility](compatibility.md).

## `next.config`

Oganesson calls Next.js's own configuration loader. Implemented user-facing
configuration includes:

- environment definitions consumed by Next's server and client bundles;
- `basePath`, including assets, links, middleware matching, data URLs, static
  pages, and routes outside the base path returning 404;
- `trailingSlash`, including canonical 308 redirects and prerendered static and
  dynamic Pages Router paths;
- Pages Router `i18n` locales, default locale, locale detection, domain default
  locales, and cross-domain locale redirects;
- `images` settings used by `next/image` and the runtime optimizer;
- `sassOptions` and `experimental.useLightningcss`;
- `cacheComponents`, `cacheLife`, and custom `cacheHandlers`;
- `webpack(config, options)` aliases, fallbacks, definitions, and loader rules;
  and
- compatible `turbopack.rules` loader declarations when the build selects
  them.

The webpack hook is an adapter, not a webpack compiler. Oganesson applies
loader rules, aliases, fallbacks, and definitions that can be represented in
its esbuild pipeline. Arbitrary webpack plugins, externals, and every webpack
resolution customization are not implemented.

## `og.config`

Use `og.config.ts` when the application needs build extensions beyond its
framework configuration:

```ts
export default {
  esbuild: {
    plugins: [],
    clientPlugins: [],
    serverPlugins: [],
    cssPlugins: [],
  },
  webpack: {
    rules: [],
  },
};
```

The plugin groups run in these scopes:

| Property | Scope |
|----------|-------|
| `esbuild.plugins` | Every JavaScript build. |
| `esbuild.clientPlugins` | Browser builds only. |
| `esbuild.serverPlugins` | Server builds only. |
| `esbuild.cssPlugins` | CSS builds. |
| `webpack.rules` | Webpack-style loader matching and execution. |

JavaScript esbuild plugins run in the long-lived build host. Resolution, load,
and transform callbacks are bridged to Oganesson's Go esbuild build. Webpack
loaders run with a compatibility context sufficient for validated loaders such
as the styled-jsx Babel path; it is not the full webpack loader API.

## Build scripts

For Next.js projects, Oganesson understands scripts that perform code generation
before `next build`. It runs supported local package binaries in the build host,
including `prisma generate`, and recognizes Babel and selected Turbopack loader
configuration. It does not download missing `npx` packages or run arbitrary
native executables.

For Vite projects, Oganesson interprets the `build` script and its `npm run`,
pnpm, or Yarn subcommands. It runs the Vite build invocations and merges CLI
flags the way Vite's CLI does. Framework-specific commands such as `astro
build`, `react-router build`, `nuxt build`, and `qwik build` are handled by the
validated adapters in the [Vite guide](vite.md).

## Environment variables

Build-time code sees the environment of `og build`. The packaged application's
server code sees the environment of the application process. Framework rules
still determine which variables are exposed to browser code.

Operational variables understood by Oganesson include:

| Variable | Purpose |
|----------|---------|
| `PORT` | Default listening port for `og serve`, packaged binaries, and ejected binaries. |
| `OG_SOURCE` | Default Oganesson checkout for `-og-source`. |
| `OG_VITE_TRACE=1` | Log Vite/Rollup-compatible plugin hook calls while diagnosing a build. |
| `NODE_DEBUG` | Enables matching Node-style debug sections in the compatibility layer. |
| `TMPDIR`, `TMP`, `TEMP` | Node-compatible temporary-directory selection. Build-time temporary writes are redirected inside the project. |

Oganesson also sets framework-private values when required for fidelity, such as
the application's own origin after the HTTP listener starts. Do not depend on
those internal variables in application code.

## Generated files

`og check` and `og build` write `og-env.d.ts` beside the project sources. It
declares `og:kv` and any project-owned native Go modules. Commit it only if that
matches the project's normal generated-file policy.

`og build` replaces `.og/`. Do not put hand-written files there. The output can
be served directly with `og serve` or packed into an executable with `-o`.
