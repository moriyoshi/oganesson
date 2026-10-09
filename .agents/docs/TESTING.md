# Testing guide

Every test runs with `go test` and needs neither Node.js nor a browser. Tests that need external inputs (a fixture's `node_modules`, the WPT checkout) skip with a message that names the command to prepare them. A skip is not a pass: report it.

## Prerequisites

| Input | Needed by | How to get it |
|-------|-----------|---------------|
| `third_party/typescript` (+ `_tsoverlay/ogapi`) | every build (`go.mod` `replace`) | `scripts/sync-typescript.sh` |
| `third_party/wpt` | `runtime/webapi` WPT runner | `scripts/sync-wpt.sh` |
| `testdata/apps/next-hello/node_modules` | `internal/frameworks/next`, `internal/jshost` (`TestLoadNext*`, `TestMiddlewareMatchers`) | `go run ./cmd/og install testdata/apps/next-hello` (network) |
| `testdata/apps/next-pages/node_modules` | `internal/frameworks/next` (`TestPages*`), `runtime/nodeshim` (`TestReadableStream`, `TestRawBody`) | `go run ./cmd/og install testdata/apps/next-pages` (network) |
| `testdata/apps/vite-react/node_modules` | `internal/frameworks/vite` (`TestBuildReactSPA`) | `go run ./cmd/og install testdata/apps/vite-react` (network) |
| `testdata/apps/tanstack-router/node_modules` | `internal/frameworks/vite` (`TestBuildTanStackRouter`) | `go run ./cmd/og install testdata/apps/tanstack-router` (network) |
| `testdata/apps/tanstack-start/node_modules` | `internal/frameworks/vite` (`TestServeTanStackStart`, `TestStreamTanStackStartDeferred`, `TestServeTanStackStartSPA`) | `go run ./cmd/og install testdata/apps/tanstack-start` (network) |
| `testdata/apps/vite-features/node_modules` | `internal/frameworks/vite` (`TestBuildViteFeatures`) | `go run ./cmd/og install testdata/apps/vite-features` (network) |
| `testdata/apps/vite-vue/node_modules`, `vite-svelte`, `vite-tailwind`, `vite-preact`, `vite-solid`, `vite-unocss` | `internal/frameworks/vite` (`TestBuildVue`, `TestBuildSvelte`, `TestBuildTailwind`, `TestBuildPreact`, `TestBuildSolid`, `TestBuildUnoCSS`) | `go run ./cmd/og install testdata/apps/<name>` (network) |
| `testdata/apps/vite-ssr-{express,fastify,koa,hono,h3}/node_modules`, `vite-pwa` | `internal/frameworks/vite` (`TestServeViteSSRServers`, one subtest each; `TestBuildPWA`) | `go run ./cmd/og install testdata/apps/vite-ssr-<name>` (network) |
| `testdata/apps/nuxt/node_modules` | `internal/frameworks/vite` (`TestServeNuxt`, about 80 s) | `go run ./cmd/og install testdata/apps/nuxt` (network) |
| `testdata/apps/solid-start/node_modules` | `internal/frameworks/vite` (`TestServeSolidStart`, about 30 s) | `go run ./cmd/og install testdata/apps/solid-start` (network) |
| `testdata/apps/qwik-city/node_modules`, `vite7-react` | `internal/frameworks/vite` (`TestServeQwikCity`, about 35 s; `TestBuildVite7SPA`, about 18 s) | `go run ./cmd/og install testdata/apps/<name>` (network) |
| `testdata/apps/sveltekit-node/node_modules` | `internal/frameworks/vite` (`TestServeSvelteKit`) | `go run ./cmd/og install testdata/apps/sveltekit-node` (network) |
| `testdata/apps/react-router/node_modules` | `internal/frameworks/vite` (`TestServeReactRouter`) | `go run ./cmd/og install testdata/apps/react-router` (network) |
| `testdata/apps/astro-static/node_modules`, `astro-node` | `internal/frameworks/vite` (`TestBuildAstroStatic`, `TestServeAstroNode`) | `go run ./cmd/og install testdata/apps/astro-<name>` (network) |
| `testdata/apps/tanstack-start-rsc/node_modules` | `internal/frameworks/vite` (`TestServeTanStackStartRSC`) | `go run ./cmd/og install testdata/apps/tanstack-start-rsc` (network) |
| `testdata/apps/next-i18n/node_modules` | `internal/frameworks/next` (`TestPagesI18n`) | `go run ./cmd/og install testdata/apps/next-i18n` (network) |
| `testdata/apps/next-mixed/node_modules` | `internal/frameworks/next` (`TestMixedRouters`, `TestAppPageConflict`) | `go run ./cmd/og install testdata/apps/next-mixed` (network) |
| `testdata/apps/next-native/node_modules` | `cmd/og` (`TestBuildNativeApp`: a real `og build -o` of an app with native Go modules; also needs `go` on PATH and `third_party/typescript`; skipped with `-short`) | `go run ./cmd/og install testdata/apps/next-native` (network) |
| `testdata/apps/next-webpack/node_modules` | `internal/frameworks/next` (`TestWebpackHook`) | `go run ./cmd/og install testdata/apps/next-webpack` (network) |
| `testdata/apps/next-babel/node_modules` | `internal/frameworks/next` (`TestBabelConfig`) | `go run ./cmd/og install testdata/apps/next-babel` (network) |
| `testdata/apps/next-pages-react18/node_modules` | `internal/frameworks/next` (`TestPagesReact18`) | `go run ./cmd/og install testdata/apps/next-pages-react18` (network) |
| `third_party/next.js/examples` | `og compat` (the compatibility corpus; no Go test reads it) | `scripts/sync-next-examples.sh` (network) |
| `testdata/plugins/postcss/node_modules` | `internal/jshost` `TestEsbuildPluginPostcss` | `go run ./cmd/og install testdata/plugins/postcss` (network) |
| `testdata/plugins/tailwind3/node_modules` | `internal/jshost` `TestPostCSSTailwind3` | `go run ./cmd/og install testdata/plugins/tailwind3` (network) |
| Every fixture at once | all of the above | `scripts/install-fixtures.sh` (or pass directories / patterns, e.g. `'testdata/apps/vite-*'`); CI uses it per shard |
| Vendored JS (`runtime/webapi/js/vendor`, `runtime/nodeshim/vendor`) | everything embedding them | already in tree; refresh with `scripts/vendor-js.sh` |

## Suites

| Package | What it covers | Notes |
|---------|----------------|-------|
| `runtime/engine` | event loop (`Async`, `Post`, `Await`, `Drain`), panic recovery, AsyncLocalStorage | fast |
| `runtime/webapi` | Web APIs, plus the WPT subset | `-wpt.filter=<substr>` narrows the files; `-wpt.update` rewrites `testdata/wpt-expectations.json` (the list of known-failing subtests). Review that diff: a newly passing subtest is progress, a newly failing one is a regression. |
| `runtime/server` | per-request VM handler, timeouts, streaming | |
| `runtime/kv` | Deno KV semantics: key ordering, atomic checks, versionstamps, TTL | |
| `runtime/isr` | build-seeded dynamic paths, static attempt then HIT, remembering runtime-dynamic variants | Most ISR behavior (STALE, revalidation, middleware phase) is covered end to end by `TestStaticAndISR` / `TestProxyMiddleware` in `internal/frameworks/next`. |
| `internal/jshost` | build-time host: next.config loading, middleware matchers, the esbuild plugin bridge, the webpack loader shim, dynamic `require`, Next's default PostCSS pipeline | |
| `internal/frameworks/next` | end-to-end over `testdata/apps/next-hello`: build, then serve in-process through `serveFixture` (prerender included). Covers SSR, route handlers, server actions, proxy, KV, static/ISR, CSS, PostCSS/Tailwind v3, styled-jsx. | slow: a few minutes. Use `-run TestCSS` etc. while iterating. |
| `internal/install` | the package installer: staging, integrity, unsupported specs, wasm32 optional builds and wasm substitutes | |
| `internal/wasm` | the WebAssembly API on a hand-assembled module: calls, imports, tables, memory views and growth, exceptions, link/compile errors | Threads and WASI are covered through oxide in `internal/jshost` and `internal/frameworks/next`. |
| `pack` | payload append and read (`OGPAYLD1` trailer) | |

Run everything:

```sh
go test -tags nodynamic -timeout 30m ./cmd/... ./runtime/... ./internal/... ./pack/...
```

`spikes/` contains throwaway experiments and is not part of the suite.

## Fixtures

- `testdata/apps/next-pages` is the Pages Router fixture (`pages_test.go`), `testdata/apps/next-mixed` has both routers (`mixed_test.go`), and `testdata/apps/next-i18n` has next.config i18n (`i18n_test.go`).
- `testdata/apps/next-hello` is the Next.js fixture. Every supported feature should be exercised by some route in it, with an assertion in `internal/frameworks/next/build_test.go`. When you add a feature, add it to the fixture and a test to that file.
- `testdata/apps/next-basepath` serves under `basePath: "/docs"` (`basepath_test.go`).
- `testdata/apps/next-cache` has `cacheComponents: true`: "use cache" functions (cacheLife, cacheTag), a page with a dynamic hole, a fully static page (`TestCacheComponents`).
- `testdata/apps/next-webpack` builds with `next build --webpack` (its `webpack()` hook, `TestWebpackHook`); `testdata/apps/next-turbopack` with plain `next build`, so Turbopack's configuration applies (`TestTurbopack`).
- The fixtures pin Next 16.3.8; og also supports 16.4. To run the Next suite against another release, copy the `next-*` fixtures (without `node_modules`) into a directory, change their `next` (and `@next/*`) versions, install each with `og install`, and point `OG_TEST_APPS` at that directory (absolute): `OG_TEST_APPS=$PWD/.agents-workspace/tmp/apps164 go test -tags nodynamic ./internal/frameworks/next/`. Tests whose expectations differ between releases read the installed version (`installedNext`).
- `testdata/plugins/postcss` holds an esbuild JS plugin (`esbuild-plugin-postcss`) driven through `og.config.ts`.

## Browser verification

Some behavior only exists in a browser: hydration, soft navigation, client-side server-action calls, CSS application, styled-jsx hydration. Verify it against a **packaged binary**, not the in-process test server:

```sh
scripts/build-og.sh                                  # -> ./.agents-workspace/tmp/og (release flags)
./.agents-workspace/tmp/og build -o ./.agents-workspace/tmp/next-hello testdata/apps/next-hello
./.agents-workspace/tmp/next-hello -addr :3123 &     # note the PID; kill it afterwards
```

Then drive `http://localhost:3123/` with the Playwright MCP tools. Check for console errors after hydration, follow `<Link>`s and confirm no full reload happens, submit the action forms, and read computed styles. Stop the server when done. Screenshots and other artifacts go in `./.agents-workspace/tmp`.

## TanStack Start fixture notes

- `testdata/apps/tanstack-start` builds in SPA mode when `OG_FIXTURE_SPA=1` is set (its `vite.config.ts` reads it); `TestServeTanStackStartSPA` sets it. Both modes write the same `dist/`.
- Start streams only for browsers: it waits for all data when `isbot` matches the User-Agent (curl does). Send a browser User-Agent to see deferred data stream.
- The Vite tests take about 2 minutes (each builds a fixture; the RSC fixture runs five Vite builds).
- `OG_VITE_TRACE=1` logs each plugin hook call of the Rolldown facade to stderr.
- The `grep` in Claude Code's shell skips gitignored files (`dist/`, `.agents-workspace/`); use `command grep` there.
- Comparing a server program with Node (diagnosis only): `.agents-workspace/tmp/compare.sh <fw>` style. Run the reference with `exec node` and kill whatever listens on the ports afterwards; a leftover Node server otherwise answers the next comparison.
- The WPT runner includes `compression` (CompressionStream). `-wpt.update` rewrites the whole expectations file from the run, so update with a full run, never with `-wpt.filter`.

## Compatibility corpus (`og compat`)

`og compat dir...` runs apps through og as a user would, each stage a child `og` process: copy (into `-work`, default `.agents-workspace/tmp/compat`; the sources are never touched), `og install`, a check that Next 16 was installed (`next-version`), `og build`, `og serve`, and a crawl (pages breadth-first from `/` along same-origin links, `-pages` of them, and the scripts, stylesheets, images and icons they reference). A page answering 5xx, or an asset 4xx/5xx, fails the crawl; error lines of the server log are reported. Logs are under `<work>/_logs/<app>/`.

- The corpus is Next's own examples at the release it installs: `scripts/sync-next-examples.sh` (sparse checkout of `vercel/next.js` `examples/` at v16.4.0 into `third_party/next.js`; v16.3.8 before 2026-10-10), then `go run ./cmd/og compat -j 4 -json report.json third_party/next.js/examples`. `-run re` picks apps by name; `-failed report.json` reruns the apps an earlier report did not pass.
- "latest", "canary" and "rc" Next and React versions are pinned to 16.4.0 / 19.3.0 (`corpusNext`, `corpusReact` in `cmd/og/compat.go`; the fixtures stay on 16.3.8) (and the example's lockfile dropped, since it would install what it locked). Examples pinned to older Next releases fail at `next-version`.
- Each app's `node_modules` and `.og/` are removed after its run (`-keep` keeps them): a corpus of 230 apps would need tens of GB.
- Many examples need settings for an external service (a `.env*example` file: the ENV column); those fail like `next build` without them (a CMS fetch in `generateStaticParams`, a missing secret). Prerender failures carry the server's console output.
- `testdata/compat/expected.json` lists the apps that fail outside og too (pinned to an old Next, broken upstream, needing a native tool), each with its reason; `-expected` names another file. The summary ends with `unexpected failures: N (...)`, the failures neither listed nor in an app that needs an external service (the ENV column): those are og's to look into. A listed app that passes is named (`listed as expected but passing`), to drop from the list. Run 9 (2026-10-11, Next 16.4.0): 148 of 225 pass, no unexpected failures.
- It needs the network (npm registry) and takes about an hour for the examples with `-j 4`.

## CI

`.github/workflows/ci.yml` runs on pushes to `main` and on pull requests. It runs lint and tests on Linux only and builds no binaries:

- `lint`: gofmt, `go vet`, and the freshness of the two checked-in generated files: `prelude.js` (genprelude) and `runtime/notices/THIRD_PARTY_NOTICES.txt` (`go generate ./runtime/notices`).
- `test`: shards `core` (everything outside `internal/frameworks`, with WPT), `next`, and `vite`. Fixtures are installed with `scripts/install-fixtures.sh` and cached weekly. The job summary lists every skipped test.

`.github/workflows/release.yml` runs on `v*` tags: it calls CI and, only after its `lint` and `test` jobs pass, builds an archive per platform (linux, darwin and windows on amd64 and arm64: og, LICENSE, README.md, and the checked-in THIRD_PARTY_NOTICES.txt) and publishes a GitHub release with `SHA256SUMS`.

## Database tests

- MySQL: `TestORMDriverMySQL` (`internal/frameworks/next`) runs in the default path against go-mysql-server (in memory, a test-only dependency).
- PostgreSQL: `TestORMDriverPostgres` needs `-tags aot` (or `-tags wazero`): PostgreSQL 18 from `github.com/moriyoshi/pglite-go` v0.2.3 (pure Go with the `pglite-go-aot` companion, ~2 minutes to compile the first time) serving the wire protocol with `(*DB).ServeConn`. The default build must not import pglite-go (its default backend is cgo). The data bundle is downloaded into the user cache on first use (`Config{Download: true}`); the test skips if that fails.

  ```
  go test -tags 'nodynamic aot' -run TestORMDriverPostgres ./internal/frameworks/next/
  ```
- **Caveat — JS-side connection pooling is discouraged for the native SQL shims.** og's native driver shims (`pg`, `postgres.js`, `mysql2`, `@libsql/client`) each pool connections process-wide in Go (`runtime/natives/pg.go`, `mysql.go`, and the shared SQLite service) and keep them warm across request VMs. A JS-side pool stacked on top is therefore redundant, and in tests it should be kept to a **single connection** (`new pg.Pool({ max: 1 })`, `postgres(url, { max: 1 })`, `mysql.createPool({ …, connectionLimit: 1 })`) so the fixture exercises the Go-side pool rather than a second, redundant one. This applies to every driver, not just Postgres.

  It becomes an outright **deadlock** against a single-session server. The embedded PGlite serves one backend and `pglite-go`'s `ServeConn` holds it for a connection's whole lifetime (even idle), so a JS pool that opens more than one connection stalls — a second connection blocks on the single backend while a warm/idle first connection squats it. Drizzle/Kysely over the shims drive one connection sequentially and pass; `@prisma/adapter-pg` keeps its own multi-connection pool that can't be reduced to one, so `TestPrismaPostgres` is skipped and would need a real multi-session Postgres. MySQL is the benign case: go-mysql-server is multi-session, so a small JS pool works there — and `TestORMDriverMySQL` intentionally uses `connectionLimit` 2 and 1 to verify cross-request reuse through the Go pool — but the same single-connection rule would be required against any single-session MySQL-compatible server.
- The pool: `TestSocketPool` (`runtime/nodeshim`) against a fake PostgreSQL server, in the default path.

