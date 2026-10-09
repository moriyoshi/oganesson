# Native Go modules

A project can expose its own Go functions to server-side JavaScript. Each Go
package under the project's `og/` module becomes an `og:<directory>` import, and
Oganesson generates both registration code and TypeScript declarations.

## Project layout

```text
my-app/
  package.json
  og/
    go.mod
    hash/
      hash.go
```

```go
package hash

import "context"

// Sum returns the digest of s.
//
//og:name sha256
func Sum(s string) (Digest, error) { /* ... */ }

// Verify is asynchronous because its first argument is a context.
func Verify(ctx context.Context, password string, digest []byte) (bool, error) {
	/* ... */
}
```

```ts
import { sha256, verify } from "og:hash";

const digest = sha256("oganesson");
const ok = await verify(password, storedBytes);
```

## Export rules

- Exported top-level functions become JavaScript exports.
- Names are converted to lower camel case; `URLFor` becomes `urlFor`.
- `//og:name name` overrides the JavaScript name.
- `//og:skip` omits a function.
- A first `context.Context` parameter makes the function asynchronous: it runs
  on a goroutine and returns a Promise.
- `//og:async` and `//og:sync` override automatic async selection.
- The request context passed to Go is cancelled when its request engine ends.

Only functions are exported. Methods, generic exported APIs that cannot be
expressed by the generated bridge, and package state are not automatically
projected as JavaScript modules.

## Value conversion

Most values use `encoding/json` conventions, including struct `json` tags.
Additional mappings are:

| Go | JavaScript |
|----|------------|
| `[]byte` | `Uint8Array` |
| `time.Time` | `Date` |
| top-level `int64` / `uint64` | `bigint` |
| returned non-nil `error` | thrown `Error`, or rejected Promise |
| panic | thrown `Error`, or rejected Promise |

Nested 64-bit integers inside structs or slices pass through JSON and are
numbers. Types imported from packages other than `time` generally become
`unknown` in generated TypeScript, even if runtime JSON conversion succeeds.

## Generated TypeScript

`og check` and `og build` scan source with Go's parser and add declarations to
`og-env.d.ts`. Structs become interfaces, supported scalar/container types are
mapped, and Go doc comments become JSDoc. The declaration generator is
syntactic and does not perform whole-program Go type checking.

## Building a binary

The stock `og` executable cannot contain application-specific Go packages.
When a project has an `og/` module, this command generates a small application
runtime, compiles it with the local Go toolchain, registers the packages, and
then appends the web payload:

```sh
og build -o ./my-app \
  -og-source /path/to/oganesson \
  ./project
```

Until Oganesson is published as a resolvable Go module, `-og-source` or
`OG_SOURCE` is required. `replace` directives in the project's `og/go.mod` are
propagated into generated application/ejected modules because Go ignores a
dependency's own replacements.

For a cross build, Oganesson builds both the target executable and a temporary
host executable. The host executable prerenders static pages with the project's
Go functions, while the target executable becomes the artifact.

## Directory builds and ejecting

A plain `og build` and `og build -eject` prerender in the running `og` process,
which does not contain the project's modules. Routes that call them are treated
as dynamic and render later; a `generateStaticParams` call that requires them
cannot succeed in that mode. `og serve` of such a standalone `.og/` directory
also cannot provide the modules.

An ejected module receives a generated `natives_gen.go` that imports and
registers the packages. After building the ejected Go module, the functions are
available normally at runtime. See [Packaging and deployment](deployment.md).

## Registering modules manually

An ejected or custom Go application can use the runtime native registry directly
through `github.com/moriyoshi/oganesson/runtime/native`. This is the lower-level
escape hatch when generated top-level function bindings are insufficient. The
same request cancellation, synchronous/asynchronous execution, and value
conversion rules apply.
