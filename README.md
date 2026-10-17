<div align="center">

# Oganesson

<img src="./assets/oganesson-logo.svg" width="256" height="256" alt="Oganesson logo" />

**Compile a Next.js or Vite application into one self-contained pure-Go executable.**

**No Node.js is required at build time or runtime.**

</div>

Oganesson's `og` command installs npm dependencies, runs the framework's
production build inside an embedded JavaScript host, and packages the result
with a Go HTTP server and JavaScript runtime. The deployed application is one
file: no Node.js installation, `node_modules` tree, native addons, or process
manager is required.

```sh
og install ./my-app
og build -o ./my-app-server ./my-app
./my-app-server -addr :3000
```

Oganesson supports Next.js App and Pages Routers, production Vite builds and a
wide Vite framework ecosystem, static generation and SSR, Server Actions, ISR
and Cache Components, common CSS/image/font pipelines, databases and ORMs, and
project-owned Go functions exposed as typed JavaScript modules.

The project favors fidelity and request isolation over throughput. Every
request runs in a fresh Sobek VM; state that must cross requests lives in an
explicit Go-backed service or external database.

## Documentation

The current user documentation lives in [`docs/`](docs/index.md):

- [Can I do this with Oganesson?](docs/can-i-do-this.md)
- [Introduction and feature map](docs/introduction.md)
- [Getting started](docs/getting-started.md)
- [Development workflow](docs/development-workflow.md)
- [Command-line reference](docs/cli.md)
- [Project configuration](docs/configuration.md)
- [Next.js support](docs/nextjs.md)
- [Vite and framework support](docs/vite.md)
- [Runtime compatibility](docs/runtime.md)
- [Data, databases, and ORMs](docs/data.md)
- [Native Go modules](docs/native-go-modules.md)
- [Packaging and deployment](docs/deployment.md)
- [Compatibility and limitations](docs/compatibility.md)

Start with the [feature map](docs/introduction.md#feature-map) for a compact
overview.
Contributor-facing subsystem design is documented in
[`ARCHITECTURE.md`](ARCHITECTURE.md).

The Markdown files are also a VitePress site. Preview it with optional Node.js
documentation tooling (Oganesson itself remains Node-free):

```sh
cd docs
npm ci
npm run dev
```

Or build and serve the production documentation with Oganesson itself:

```sh
./og install docs
./og build docs
./og serve docs
```

## Install `og`

Download a prebuilt binary for your platform (Linux, macOS, or Windows; amd64 or
arm64) from the
[releases page](https://github.com/moriyoshi/oganesson/releases), or build from
source. Go 1.27 and Git are required to build:

```sh
git clone https://github.com/moriyoshi/oganesson
cd oganesson
scripts/sync-typescript.sh
scripts/build-og.sh ./og
```

See [Getting started](docs/getting-started.md) for installation, type checking,
build modes, and the development workflow.

## Project status

Oganesson is under active development. Its compatibility surface is substantial
but not identical to Node.js, webpack, Rollup, or Rolldown. In particular,
WebSocket servers, arbitrary native addons, the development-server/HMR path,
and durable built-in storage are not implemented.

Read [Compatibility and limitations](docs/compatibility.md), and validate the
packaged binary against your application's production paths before deploying.

## Bill of Thanks

Oganesson stands on the work of many open-source projects. Our deep thanks to their authors and maintainers. Without them this project would not exist.

**The engines that make it possible**

- [Sobek](https://github.com/grafana/sobek) by Grafana Labs, a fork of [Goja](https://github.com/dop251/goja) by Dmitry Panov and contributors: an ECMAScript engine in pure Go. It runs every line of JavaScript in Oganesson, at build time and at run time. Goja's companion [goja_nodejs](https://github.com/dop251/goja_nodejs) provides its CommonJS loader.
- [esbuild](https://github.com/evanw/esbuild) by Evan Wallace: the bundler and transformer behind every build, usable from Go as a library.
- [TypeScript](https://github.com/microsoft/TypeScript) by Microsoft and the TypeScript team, whose Go-native compiler (`tsc/`) provides `og check`, tsconfig semantics, and the parser behind our ports of Next's SWC transforms.
- [wazero](https://github.com/tetratelabs/wazero) by Tetrate and contributors: a zero-dependency WebAssembly runtime in Go. It runs the WebAssembly builds of native tools.

**The frameworks we compile**

- [Next.js](https://github.com/vercel/next.js) and [React](https://github.com/facebook/react): whose own templates, renderer, and client router Oganesson drives as shipped.
- [Vite](https://github.com/vitejs/vite), [Rollup](https://github.com/rollup/rollup), and [Rolldown](https://github.com/rolldown/rolldown): Vite's builder runs as is, and Rollup's plugin semantics define our bundler facade.
- [TanStack Router and Start](https://github.com/TanStack/router).

**Vendored JavaScript**

- [unenv](https://github.com/unjs/unenv) and [pathe](https://github.com/unjs/pathe) by the UnJS team: the Node.js built-in surface.
- [readable-stream](https://github.com/nodejs/readable-stream) and [string_decoder](https://github.com/nodejs/string_decoder) by the Node.js project: Node's own streams, outside Node.
- [web-streams-polyfill](https://github.com/MattiasBuelens/web-streams-polyfill) by Mattias Buelens: the WHATWG Streams standard.
- [FormatJS](https://github.com/formatjs/formatjs): the `Intl` polyfills.
- [regexp-match-indices](https://github.com/rbuckton/regexp-match-indices) by Ron Buckton and [regexp-tree](https://github.com/DmitrySoshnikov/regexp-tree) by Dmitry Soshnikov: RegExp match indices.
- [acorn](https://github.com/acornjs/acorn): the ESTree parser behind our `rolldown/parseAst`.
- [napi-wasm](https://github.com/devongovett/napi-wasm) by Devon Govett: Node-API over WebAssembly.
- [Lightning CSS](https://github.com/parcel-bundler/lightningcss) and [bcrypt.js](https://github.com/dcodeIO/bcrypt.js): substitutes for native addons.

**Go libraries**

- [modernc.org/sqlite](https://gitlab.com/cznic/sqlite) by Jan Mercl: SQLite, transpiled to pure Go.
- [gen2brain/webp](https://github.com/gen2brain/webp) and [golang.org/x/image](https://pkg.go.dev/golang.org/x/image): image codecs for the image optimizer.
- [andybalholm/brotli](https://github.com/andybalholm/brotli): Brotli for `node:zlib`.
- [nlnwa/whatwg-url](https://github.com/nlnwa/whatwg-url): the WHATWG URL standard.
- [Masterminds/semver](https://github.com/Masterminds/semver): version ranges for the installer.
- [goccy/go-yaml](https://github.com/goccy/go-yaml): lockfile parsing.
- [golang.org/x/mod](https://pkg.go.dev/golang.org/x/mod), [x/sync](https://pkg.go.dev/golang.org/x/sync), and [x/text](https://pkg.go.dev/golang.org/x/text).

**Standards**

- [Web Platform Tests](https://github.com/web-platform-tests/wpt): the conformance suite our Web APIs are tested against.
- [WinterCG / WinterTC](https://wintertc.org/): the common server-side Web API surface.

**Inspiration**

- [Deno KV](https://docs.deno.com/deploy/kv/manual/): the API `og:kv` follows.
- [PHP](https://www.php.net/): its shared-nothing model, where every request starts from a clean slate, is the inspiration for Oganesson's fresh JavaScript VM per request.

And to the Go team, for a language and toolchain that make a pure-Go, cross-compiled, single-binary toolchain like this possible.

## License

Oganesson is licensed under the MIT License. Run `og licenses` to print
the licenses of Oganesson and the software it includes; a packaged application
also supports `-licenses` for its collected application notices.
