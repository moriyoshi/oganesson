# Command-line reference

The general form is:

```text
og <command> [options] [project-directory]
```

When the project directory is optional and omitted, it defaults to the current
directory.

## `og install`

```text
og install [-dev=true] [-v] [-cache directory|off] [dir]
```

Installs `package.json` dependencies into `node_modules` without npm.

| Option | Meaning |
|--------|---------|
| `-dev` | Include `devDependencies`. Defaults to `true`. |
| `-v` | Log every installed package. |
| `-cache path` | Cache registry metadata and tarballs at this path. The default is the platform user-cache directory under `oganesson/npm`. |
| `-cache off` | Disable the installer cache. |

The installer verifies package integrity and does not execute lifecycle scripts.
It always uses the public npm registry; there is no registry, `.npmrc`,
auth-token, or proxy configuration. See [Compatibility](compatibility.md) for
lockfile and native-package limits.

## `og check`

```text
og check [dir]
```

Writes `og-env.d.ts`, loads `tsconfig.json`, and type-checks the project with
TypeScript-Go. Diagnostics use the usual `file:line:column` form. The removed
TypeScript `baseUrl` option is reported as a warning because Oganesson still
honors it for compatibility with existing Next.js projects.

## `og build`

```text
og build [-o bin] [-target goos/goarch] [-runtime og]
         [-eject dir] [-og-source checkout] [-readable] [dir]
```

Always creates or replaces the project's `.og/` directory. It can also write a
single executable, an ejected Go module, or both.

| Option | Meaning |
|--------|---------|
| `-o path` | Also write a self-contained application executable. With the Go toolchain on `PATH`, it embeds a slim serve-only runtime (much smaller); without Go it copies the whole `og` and prints a note. |
| `-target goos/goarch` | Target platform for `-o`; defaults to the host. With Go, the slim runtime is cross-compiled for it directly; otherwise `-runtime` or the runtime cache supplies one. |
| `-runtime path` | Package this target-platform `og` executable verbatim instead of building or caching a runtime. It must be CGO-free, target the requested platform, and come from the same Oganesson source revision. |
| `-eject dir` | Also write a standalone Go module containing the built application. |
| `-og-source path` | Oganesson checkout to build against. Needed only for an unpublished (locally built) `og`; a released `og` resolves itself from the module proxy. Used by an ejected module, the slim runtime, and a binary with project-owned Go modules. Defaults to `OG_SOURCE`. |
| `-go path` | Go toolchain that compiles the `-o` runtime, a path or a name on `PATH`. Defaults to `OG_GO`, then `go`. |
| `-readable` | Leave browser JavaScript and stylesheets unminified. |

`-target` and `-runtime` require `-o`. A project containing native Go modules is
compiled into its own runtime, so it cannot use `-runtime`.

The command prints each Next.js route and whether it is dynamic, prerendered,
partially prerendered, an API/route handler, or a dynamic segment. Vite builds
print the detected application type and whether a server or server program was
packaged.

## `og serve`

```text
og serve [-addr host:port] [dir]
```

Serves an existing `.og/` build. The address defaults to `PORT` when set, and
otherwise to `:3000`. It does not rebuild and does not watch source files.

## `og licenses`

```text
og licenses
```

Prints the licenses of Oganesson and the software linked or embedded in the
`og` executable.

A packaged application has its own flag:

```text
my-app -licenses
```

That output adds the npm-package notices collected from the application's
shipped bundles and assets.

## `og version`

```text
og version
```

Prints the Oganesson version this `og` corresponds to, along with the host
platform and the Go version it was built with, for example:

```text
og v0.3.0 darwin/arm64 go1.27.0
```

A build that was not stamped with a release version (a local build) reports the
Go module version instead, such as a pseudo-version or `(devel)`.

## `og compat`

```text
og compat [options] dir...
```

Runs one application, or every immediate child application in a directory,
through isolated copy, install, build, serve, and HTTP-crawl stages. This is a
compatibility-corpus and regression tool rather than the normal way to launch
an application.

| Option | Default | Meaning |
|--------|---------|---------|
| `-work path` | `.agents-workspace/tmp/compat` | Work directory. Sources are copied before modification. |
| `-keep` | false | Keep installed packages and build output. These can consume hundreds of megabytes per app. |
| `-run regexp` | all | Select applications by base name. |
| `-j n` | 1 | Process this many applications concurrently. |
| `-pages n` | 20 | Maximum pages to crawl per application. |
| `-json file` | none | Write the full JSON report. |
| `-timeout duration` | 10 minutes | Limit each install and build stage. |
| `-failed report.json` | none | Re-run only applications that failed in an earlier report. |
| `-expected file` | `testdata/compat/expected.json` | Map applications known to fail outside Oganesson to explanations. |
| `-env file` | `testdata/compat/env.json` | Per-application dummy environment variables supplied to build and server stages. |

## Packaged application flags

An executable created by `og build -o` recognizes:

| Option | Meaning |
|--------|---------|
| `-addr host:port` | Listening address; defaults to `PORT` or `:3000`. |
| `-licenses` | Print Oganesson and application notices, then exit. |

`SIGINT` and `SIGTERM` stop `og serve` and packaged applications gracefully.
