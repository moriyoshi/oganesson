# Getting started

## Install `og`

### Download a release

Prebuilt `og` binaries for Linux, macOS, and Windows (amd64 and arm64) are
published on the
[releases page](https://github.com/moriyoshi/oganesson/releases). Download the
archive for your platform, optionally verify it against the release's
`SHA256SUMS`, extract it, and put `og` on your `PATH`:

```sh
tar -xzf og-<version>-<os>-<arch>.tar.gz   # unzip the .zip archive on Windows
./og-<version>-<os>-<arch>/og --help
```

Each archive also contains `LICENSE` and `THIRD_PARTY_NOTICES.txt`. You do not
need Node.js, npm, pnpm, Yarn, a C compiler, or a native-addon toolchain to run
`og`. The Go toolchain is optional: when it is present, `og build -o` compiles
a slim serve-only runtime so the final executable is much smaller, and without
it the build still works by packaging the whole `og`. A project that contains
[native Go modules](native-go-modules.md) always needs the Go toolchain for its
final executable.

### Build from source

Building `og` yourself is the contributor path, and it is also how you produce a
runtime for [cross-compilation](deployment.md#cross-compilation). You need Go
1.27, Git, and network access for the initial TypeScript-Go checkout:

```sh
git clone https://github.com/moriyoshi/oganesson
cd oganesson
scripts/sync-typescript.sh
scripts/build-og.sh ./og
```

`sync-typescript.sh` checks out the pinned TypeScript-Go source used by
`og check` and by the build pipeline. `build-og.sh` produces the same kind of
CGO-free runtime that `og build -o` expects to package.

## Install a project

Run this once, and again whenever the dependency manifest or lockfile changes:

```sh
./og install ./my-app
```

The installer:

- reads `package.json` and a supported npm, pnpm, Yarn, or text Bun lockfile;
- fetches npm registry tarballs directly and verifies their integrity;
- populates `node_modules` without calling a package manager;
- never runs package lifecycle scripts;
- uses a persistent metadata and tarball cache by default; and
- installs known pure-JavaScript or WebAssembly replacements for packages that
  otherwise load native code.

Use `-dev=false` to omit development dependencies, `-v` to print each package,
or `-cache off` to disable the installer cache.

## Type-check the project

```sh
./og check ./my-app
```

This is optional. It runs the Go implementation of TypeScript against the
project's `tsconfig.json` and writes `og-env.d.ts` first, so imports such as
`og:kv` and project-owned `og:<name>` modules are typed.

## Build and run

For a directory build:

```sh
./og build ./my-app
./og serve -addr :3000 ./my-app
```

The build is written to `my-app/.og/`. `og serve` can also be pointed directly
at a build-output directory that contains its metadata.

For the normal deployment artifact:

```sh
./og build -o ./my-app-server ./my-app
./my-app-server -addr :3000
```

The result is one executable containing the runtime, server bundle, browser
assets, public files, prerendered responses, metadata, and applicable license
notices. The server defaults to `:3000`; `PORT=8080` and `-addr :8080` both
change the port, with `-addr` taking precedence.

## What Oganesson detects

Oganesson chooses the build pipeline in this order:

1. A project with a `next.config.*` file or a `next` dependency is a Next.js
   project.
2. Otherwise, a project with a `vite.config.*` file or a Vite dependency is a
   Vite project.
3. The Vite pipeline recognizes framework build scripts and output layouts for
   the ecosystems in the [Vite guide](vite.md).

Use the framework's normal production configuration. Oganesson runs the
framework's own configuration and build code inside its embedded build host;
it does not require a special application entry point.

## Development workflow

Oganesson currently has no development server, hot-module replacement, or
React Fast Refresh. Use the framework's own development server while editing if
you have Node.js available, then use `og build` to verify the production result.
After a source change, rebuild and restart `og serve` or the packaged binary.
The [development workflow](development-workflow.md) gives the ordinary
development, build, preview, and start commands for every supported framework
family, and explains how to keep the Node.js and Oganesson dependency lanes
separate.

Next steps:

- configure build hooks in [Project configuration](configuration.md);
- review the relevant [Next.js](nextjs.md) or [Vite](vite.md) guide;
- choose application state and database services in [Data](data.md); and
- prepare a target artifact using [Packaging and deployment](deployment.md).
