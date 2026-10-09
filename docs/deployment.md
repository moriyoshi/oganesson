# Packaging and deployment

Oganesson supports three outputs: a `.og/` directory for local serving, one
self-contained executable for deployment, and an ejected Go module for custom
server code.

## Build directory

```sh
og build ./project
og serve -addr :3000 ./project
```

The project receives a `.og/` directory containing its server program, browser
assets, public files, build metadata, prerendered entries, traced files, and
application license notices. The directory is replaced on every build.

This form is useful for inspection and local validation. It cannot run
project-owned [native Go modules](native-go-modules.md), because they are not
compiled into the stock `og serve` executable.

## Self-contained executable

```sh
og build -o ./my-app ./project
./my-app -addr :3000
```

Oganesson copies a compatible `og` runtime and appends the `.og/` payload as a
zip followed by an `OGPAYLD1` trailer. At startup the executable discovers its
own payload and becomes the application server.

The artifact contains all framework server code, browser assets, public files,
prerendered entries, Web/Node compatibility code, JavaScript engine, image
codecs, SQLite, and notices required by the application. It does not require a
sidecar `node_modules` tree.

The executable is replaced atomically when rebuilt, so a failed write does not
damage the previous artifact and a running executable is never modified in
place.

## Cross-compilation

Build a matching Oganesson runtime for the target, then package with it:

```sh
scripts/build-og.sh ./og-linux-arm64 linux/arm64
./og build \
  -o ./my-app \
  -target linux/arm64 \
  -runtime ./og-linux-arm64 \
  ./project
```

Without `-runtime`, Oganesson looks in:

```text
<UserCacheDir>/oganesson/runtimes/<source-id>/og-<goos>-<goarch>
```

If the file is absent, the error prints the exact `scripts/build-og.sh` command
needed to create it. Oganesson rejects a runtime that:

- targets another OS or architecture;
- is not the `og` program;
- was built with CGO; or
- was stamped from a different source revision.

Projects with native Go modules build their own target runtime and cannot use
`-runtime`. Oganesson also builds a temporary host runtime to prerender with
those Go modules during a cross build.

## Ejected Go module

Use ejecting when you need Go HTTP routes, Go middleware, custom build/signing
control, or direct runtime registration:

```sh
og build \
  -eject ./my-app-server \
  -og-source /path/to/oganesson \
  ./project
```

The directory contains:

| Path | Purpose | Re-eject behavior |
|------|---------|-------------------|
| `app/` | Build output embedded with `go:embed`. | Replaced. |
| `main.go` | Server flags, signals, notices, and `app.Serve`. | Preserved after first creation. |
| `wrap.go` | User-owned HTTP wrapper, initially with `GET /healthz`. | Preserved. |
| `go.mod` | Oganesson dependency and replacements. | Preserved. |
| `natives_gen.go` | Registration for project-owned Go modules, when present. | Regenerated. |
| `README.md` | Build instructions for the ejected module. | Regenerated. |
| `THIRD_PARTY_NOTICES.txt` | Oganesson and application notices. | Regenerated. |

Oganesson refuses to eject into a non-empty directory that does not look like a
previous ejected output. This protects unrelated files. Re-ejecting preserves
the files intended for user changes.

Add routes or middleware in `wrap.go`, then build as a normal Go program:

```sh
cd my-app-server
go mod tidy
CGO_ENABLED=0 go build -trimpath -tags nodynamic -o my-app .
./my-app -addr :3000
```

`nodynamic` keeps WebP support on the pure-Go path and matches release builds.
Until Oganesson is published as a Go module, the ejected module requires
`-og-source` and a checkout with TypeScript-Go synchronized.

## Containers

A packaged binary can run in a minimal image:

```dockerfile
FROM gcr.io/distroless/static-debian12:nonroot
COPY my-app /my-app
EXPOSE 3000
ENTRYPOINT ["/my-app"]
```

Cross-build the executable for the image platform first. Distroless is useful
because it includes CA certificates and time-zone data. `scratch` also works if
the application does not need them, or if you copy the required certificate and
zoneinfo files into the image.

Mount host directories and pass absolute paths for persistent SQLite or files.
The packaged virtual filesystem's writable overlay is memory-only.

## Runtime configuration

```sh
PORT=8080 ./my-app
./my-app -addr 127.0.0.1:8080
./my-app -licenses
```

The server binds all interfaces on port 3000 by default. Prefer an explicit
address when local-only binding is required. Application environment variables
are read normally by server code.

Horizontal replicas do not share ISR entries, Cache Components data, optimized
images, `og:kv`, or virtual filesystem writes. Use external shared services when
replicas must observe the same state.

## License notices

`og licenses` prints the licenses compiled or embedded into Oganesson. During an
application build, Oganesson also inspects esbuild inputs and build-host asset
reads to collect notices for npm packages whose code, CSS, or fonts ship in the
artifact. Downloaded Google Fonts receive an attribution notice.

Use `my-app -licenses` for a packaged binary. Ejected modules also include a
`THIRD_PARTY_NOTICES.txt` file.

The collector intentionally excludes build-time-only tools, but it cannot infer
every legal relationship. Review the output before distribution, especially for
copied files, generated assets, and framework-specific static build paths.
