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

## License

Oganesson is licensed under the MIT License. Run `og licenses` to print
the licenses of Oganesson and the software it includes; a packaged application
also supports `-licenses` for its collected application notices.
