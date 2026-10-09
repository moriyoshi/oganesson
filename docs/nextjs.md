# Next.js support

Oganesson builds Next.js applications with Next's own configuration loader,
entry templates, renderer, React Server Components implementation, client
router, and manifest formats. The current compatibility target and corpus use
Next.js 16.4.0 and React 19.3.0; Pages Router fixtures also cover React 18.

## App Router

Implemented App Router behavior includes:

- Server Components and Client Components;
- React Server Components payloads and streaming HTML SSR;
- browser hydration and client-side soft navigation;
- layouts, templates, loading and error boundaries, not-found handling, and
  dynamic segments;
- `generateStaticParams` and static/dynamic route classification;
- route handlers in `route.ts`, including streaming bodies;
- module-level and inline Server Actions;
- Server Action calls from JavaScript, forms, and no-JavaScript forms;
- `redirect`, cookies, headers, and action-triggered revalidation;
- `proxy.ts` and the legacy `middleware.ts` name, including Next's matchers;
- static generation, time-based ISR, and on-demand revalidation;
- Cache Components and partial prerendering; and
- public files, static assets, metadata routes, images, fonts, and CSS.

Each request executes the server bundle in a new VM. Module-level JavaScript
state therefore starts fresh on every request. Use [application services or a
database](data.md) for state that must be shared.

## Server Actions

Oganesson handles both forms Next emits:

```ts
// actions.ts
"use server";

export async function save(form: FormData) {
  // ...
}
```

```tsx
export default async function Page() {
  const prefix = "hello";
  async function save(form: FormData) {
    "use server";
    return `${prefix} ${form.get("name")}`;
  }
  return <form action={save}>{/* ... */}</form>;
}
```

Inline actions are hoisted and their closed-over values are bound and encrypted
with Next's own action helpers. Nested actions work except when a nested action
and its enclosing action both capture the same outer value; that shape is
rejected during the build with an explanation. Next-invalid action shapes such
as non-async functions, methods, and captures of `this`, `arguments`, or
`super` are rejected.

## Static generation, ISR, and Cache Components

At build time Oganesson prerenders routes through the same runtime used by the
packaged server. Implemented caching behavior includes:

- static App Router pages and route handlers;
- `generateStaticParams` paths;
- `export const revalidate = ...`;
- `revalidatePath` and `revalidateTag`;
- Next's data and fetch cache bridge;
- dynamic paths cached on first request when Next classifies them as static;
- shared HTML and RSC cache entries with bounded keys; and
- draft/preview requests bypassing applicable page caches.

Cache Components support includes module and function-level `"use cache"`,
named cache kinds, `"use cache: private"`, `"use cache: remote"`, `cacheLife`
profiles, and `next.config` custom `cacheHandlers`. The default handler is a
Go-backed process-wide cache so entries survive the fresh VM used by each
request.

Partial prerendering stores a static shell and React's postponed state. A
request receives the shell and a streamed resume rendering for its dynamic
holes. The build output marks these routes separately from fully static routes.

All built-in caches are process-local memory. They are shared by concurrent
requests but not by multiple processes and do not survive a restart.

## Pages Router

Implemented Pages Router features include:

- ordinary pages and the Next client router;
- `getServerSideProps`;
- `getStaticProps` and `getStaticPaths`;
- fallback modes `false`, `"blocking"`, and `true`;
- `getInitialProps`;
- custom `_app`, `_document`, `404`, and error handling;
- `/_next/data` responses;
- `pages/api` with Node-style and edge-style handlers, body parsing, streamed
  responses, and `res.revalidate`;
- draft/preview mode;
- `getStaticProps` time-based revalidation;
- App Router and Pages Router in the same project; and
- React 18 compatibility for Pages Router applications.

An SSG page's HTML and `/_next/data` JSON are produced from the same render and
cached as companion entries, avoiding inconsistent values between the initial
page and client navigation.

## URL configuration

Implemented routing configuration includes:

- `basePath` for pages, route handlers, data URLs, generated links, assets,
  CSS URLs, middleware matchers, and cache keys;
- `trailingSlash` canonicalization with 308 redirects, including `basePath` and
  static/dynamic Pages Router output; and
- Pages Router `i18n` locale prefixes, locale detection, default-locale root
  redirects, locale-aware static paths and data URLs, and domain routing with
  cross-domain locale redirects.

Domain-default and explicitly prefixed locales are covered for custom 404 pages.
Config-level redirects using `locale: false` under domain routing remain a
niche unvalidated combination.

## Proxy and middleware

Both `proxy.ts` (the Next 16 name) and `middleware.ts` are accepted. Oganesson
uses Next's matcher generation, including `basePath`. Proxy/middleware executes
before the ISR cache so rewrites, redirects, headers, and short-circuit
responses affect cached and dynamic routes consistently.

WebSocket upgrade handling is not implemented.

## CSS and styling

Implemented styling features include:

- global CSS;
- CSS Modules with deterministic class names and `composes`;
- the project's PostCSS configuration;
- Next's default PostCSS pipeline when no custom config exists;
- Tailwind CSS 3 through PostCSS;
- Tailwind CSS 4 through its WebAssembly oxide and Lightning CSS dependencies;
- Sass through Next's loader behavior and `sassOptions`;
- `experimental.useLightningcss` through `lightningcss-wasm`;
- styled-jsx, including configured Babel loader use; and
- URL rewriting, emitted assets, source maps, and CSS ordering from the server
  and client module graphs.

## Images, fonts, and metadata

Implemented asset features include:

- static imports recognized by `next/image`, including dimensions and blur
  placeholders;
- the `/_next/image` runtime optimizer for local and permitted remote sources;
- JPEG, PNG, WebP, and GIF decoding/encoding paths used by the optimizer;
- `next/font/google` and `next/font/local` through Next's own loaders;
- font preloads, generated CSS, and size-adjust metadata;
- `ImageResponse` from `next/og`; and
- static and generated metadata files for icons, apple icons, Open Graph and
  Twitter images, favicon, manifest, robots, and sitemap.

The image optimizer does not emit AVIF. Google fonts are downloaded at build
time and therefore require network access unless the application's normal font
mocking or local-font strategy avoids it.

## Build configuration and code generation

Oganesson loads `next.config` with Next's own loader and supports the options
listed in [Project configuration](configuration.md). It also implements:

- a `webpack(config, options)` compatibility hook for aliases, fallbacks,
  definitions, and loader rules;
- webpack-style loaders from `og.config`;
- Babel configuration for validated transformations;
- compatible Turbopack loader rules;
- TypeScript decorator metadata when `experimentalDecorators` and
  `emitDecoratorMetadata` are enabled; and
- supported pre-build script commands such as `prisma generate`.

Arbitrary webpack plugins do not run. Native SWC-only features such as
`experimental.mdxRs` require an alternative supported loader or transform.

## Next.js-specific limits

- Browser output is not split per route. Oganesson emits a coarser browser
  bundle and a no-op chunk loader while preserving route behavior.
- A computed `require()` inside a package can fall outside Oganesson's layer
  model depending on the package pattern.
- Build-time `require.resolve` from code inlined into a config bundle resolves
  from the project rather than the original dependency's directory.
- Assets discovered only through arbitrary runtime `new URL(...,
  import.meta.url)` or `__dirname` expressions may not be traced.
- The runtime limitations in [Compatibility](compatibility.md), especially
  WebSockets and native addons, also apply to Next.js packages.
