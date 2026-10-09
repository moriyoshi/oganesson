# Static Generation and ISR

## Summary

Oganesson plays the role a CDN or Vercel plays for a Next deployment: Next's
edge runtime never caches by itself, so Oganesson asks Next for static
generation renders and caches the results in Go. The `runtime/isr` package is
the request-time page cache, fronted by middleware/proxy, with time-based and
on-demand revalidation. Cache keys are deliberately bounded so that arbitrary
URLs and queries cannot grow process memory.

## Key Facts

- Build (`og build`) prerenders every page route in-process through the freshly
  built server; static results are stored in `prerender.json` (HTML + RSC
  payload) so the binary starts warm. Dynamic segments expand via
  `generateStaticParams`, run top-down along the loader tree.
- Route summary mirrors `next build`: `○` static, `●` dynamic segment with
  prerendered paths, `ƒ` dynamic or route handler (and "ƒ API route").
- Request-time cache lives in `runtime/isr` (a dedicated Go structure, not
  `og:kv`; it needs HTTP headers, tags, and stale-while-revalidate timing).
- Static render is requested with header `x-og-static: 1`, which turns off
  `supportsDynamicResponse` in Next's edge template.
- Cache states: fresh `x-nextjs-cache: HIT`; `STALE` served with single-flight
  background regeneration; expired → blocking re-render.
- `Cache-Control` follows Next's `getCacheControlHeader`
  (`s-maxage=…, stale-while-revalidate=…`).
- Key header constants (package `isr`): `PathKeyHeader`, `MatchHeader`,
  `CachePathHeader`, `FallbackHeader`, `CompanionHeader`, `CompanionPathHeader`,
  `CompanionTypeHeader`.
- Bounding tests: `TestNotFoundCachedOnce`, `TestStaticPageQueryIndependent`,
  `TestDynamicMarkerIgnoresQuery`, `TestPathOnly`, `TestCompanionAndRevalidate`.

## Details

### Request flow (middleware in front of the cache)

The middleware/proxy runs on every request, ahead of the cache:

1. Go first asks JS for the proxy's decision only (`x-og-phase: middleware`).
2. It then looks up the cache with the *effective* request (rewritten URL,
   overridden headers).
3. The proxy's response headers and cookies are applied on top of cached pages.

On a **miss** a static render is attempted (`x-og-static: 1`). If Next reports a
cacheable result (its `metadata.cacheControl`, with `revalidate` > 0 or
`false`), Oganesson stores the HTML plus the Flight payload from
`metadata.flightData` under the RSC and RSC-prefetch keys. A dynamic-usage
bailout, an error, or a response with `Set-Cookie` marks that variant dynamic,
and it renders per request from then on.

**Security:** clients cannot reach the internal protocol. Every `x-og-*`
request header is stripped on entry and every `x-og-*` response header on exit.

### Next's data/fetch cache handler

Next's cache handler (`og:cache-handler`, wired in as the templates'
`incrementalCacheHandler`) holds the fetch/data cache in Go. Its
`revalidateTag` (and `revalidatePath`, which maps to Next's implicit `_N_T_…`
tags) also drops matching cached pages.

### Cache keys are bounded so arbitrary URLs/queries cannot grow them

The naive keying cached unmatched URLs per URL, static App Router pages per
query, and dynamic markers per query — so requests to `/nope-a`, `/nope-b`,
`/nope-c?x=1` each added an entry, letting anyone grow memory. Next keys all
three on less. The fixes:

- **Unmatched URLs → a single `/_not-found` entry.** On a cache miss the ISR
  layer asks the JS handler where the URL is cached via `isr.MatchHeader`;
  when no app route matches it is answered with `isr.CachePathHeader` =
  `<basePath>/_not-found`. The request is then looked up, rendered, and stored
  under that path. (Pages Router 404s already render uncached.)
  `TestNotFoundCachedOnce`: three different unknown URLs are HITs after the
  first (neutralized: MISS each).
- **Static App Router renders are keyed by path.** Their output never depends
  on the query, since reading `searchParams` bails out to dynamic. The
  edge-ssr-app patch sets `isr.PathKeyHeader` on static renders; the ISR layer
  stores the path-only render's RSC variants under path keys too.
  `TestStaticPageQueryIndependent`: `/about` under any query, and
  `/tags/og-query-test` rendered on its first request with a query (HTML and
  RSC), are HITs. Neutralized: without the header every query MISSes; without
  the RSC path keys the RSC request after a different query MISSes. (The RSC
  path-key case was added specifically because build-time prerenders are keyed
  without a query, so only the on-demand path exercised it.)
- **Dynamic markers drop the query** (`pathKey`).
  `TestDynamicMarkerIgnoresQuery`: 20 queries give 1 static attempt and 1
  marker (neutralized: 20 and 20).

The same `PathKeyHeader` mechanism bounds Pages Router SSG entries (a static
render declares its output ignores the query, stored under `Entry.PathOnly`
with lookup fallback) — see [[pages-router]].

### App Router cache key composition

Path + query (with the `_rsc` cache-buster removed) + `rsc`,
`next-router-prefetch`, `next-router-segment-prefetch`, and `next-url`. The
router-state header is left out: like Next, a static page's full RSC payload
serves any navigation origin. Segment-prefetch requests bypass the cache.

### Revalidation

- **Time-based:** `revalidate = 1` gives `HIT` → `STALE` (old body served while
  a single-flight background render runs) → regenerated body. Expired entries
  force a blocking re-render.
- **On-demand:** `revalidatePath` from a route handler gives `MISS` then `HIT`.
  Next's `res.revalidate` (Pages) flows through the bridge's request-meta
  `revalidate`, read by `RouteModule.getRouterServerContext`, which calls
  `og.cache.revalidatePath(path, onlyGenerated)` — a promise over
  `isr.Handler.Revalidate` (wired with `Cache.SetRevalidator`). `Revalidate`
  re-renders the path now and stores entries with their companions;
  `onlyGenerated` leaves never-generated paths alone (a 404, counted as
  success), and a failed render keeps the cached page. The success check is
  `NextNodeServer.revalidate`'s. See [[pages-router]] for the SSG companion-entry
  and `getStaticPaths` fallback details.

### SSG cache consistency (one render, two URLs)

The ISR layer originally cached a page's HTML and its `/_next/data` JSON from
separate renders, so `builtAt` differed and client navigation showed different
props than the SSR HTML; Next keeps them as one entry. The fix is **ISR
companion entries** (`isr.CompanionHeader`, `CompanionPathHeader`,
`CompanionTypeHeader`), generalizing the RSC flight mechanism: a static SSG
render attaches the other representation (HTML carries the pageData JSON; data
requests carry the HTML), and the dispatcher names the other URL. The
Pages-Router specifics (`__ogRespond`, `renderToHTMLImpl`, the dynamic-route
query problem solved by `PathKeyHeader`) live in [[pages-router]].

## Files

- `runtime/isr/` — the request-time page cache: HIT/STALE/expired states,
  single-flight regeneration, tags, companions, and the `*Header` constants.
- `internal/frameworks/next/patches.go` — patches to Next's expanded
  `edge-ssr-app` template (and the pages patches); each must match exactly once
  or the build fails as an unsupported Next version.
- `prerender.json` (in the payload) — build-time static HTML + RSC and
  `staticPaths` records.
- `og:cache-handler` — Next's `incrementalCacheHandler`, Go-backed fetch/data
  cache with `revalidateTag`/`revalidatePath`.

## Test Coverage

Go tests in `runtime/isr` (fixture-based; no Node/network on the default path):

- `TestNotFoundCachedOnce`, `TestStaticPageQueryIndependent`,
  `TestDynamicMarkerIgnoresQuery` — cache-key bounding.
- `TestCompanionAndRevalidate`, `TestPathOnly` — SSG companion consistency and
  path-only keying.
- M5 behaviors verified: prerendered page is a HIT with proxy headers applied;
  prerendered RSC serves an arbitrary router state; a dynamic page is never
  cached; `revalidate = 1` HIT → STALE → regenerated; `revalidatePath` from a
  route handler MISS → HIT.

Browser (packaged binary): client navigation consumes the prerendered RSC
payload with layout state preserved and no errors. Pages-Router SSG browser
checks are in [[pages-router]].

Pitfall caught by tests: the `isr.MatchHeader` match query is one extra request
to the JS handler on each cache miss; the isr fakes counted every request, and
two tests failed until the fakes answered match queries without counting them.
Run every package a change touches before the gate.

## Pitfalls

- **Not enforced:** `dynamicParams = false` (404 for unlisted params).
- **Not built:** PPR / Cache Components, `"use cache"`, the `durations`
  argument of Next 16's `revalidateTag` (Oganesson invalidates immediately), and
  static GET route handlers (always dynamic).
- **Prerender host:** the build prerenders with host `localhost`. Pages that
  render absolute URLs from the request host bake that in.
- **Cache scope:** in-memory and per process, like `og:kv`; not shared across
  processes.
- A static attempt on an API route must answer 503 immediately, or the ISR
  layer runs the handler twice for every new dynamic API path (static attempt
  then real render). See [[pages-router]].
