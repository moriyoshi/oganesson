#!/bin/sh
# Vendors third-party JS that the runtime prelude bundles. Pinned versions;
# fetched straight from the npm registry (no npm/Node needed).
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
vendor="$root/runtime/webapi/js/vendor"

fetch() { # name version file...
  name=$1 version=$2; shift 2
  tmp=$(mktemp -d)
  curl -fsSL "https://registry.npmjs.org/$name/-/$(basename "$name")-$version.tgz" | tar xz -C "$tmp"
  mkdir -p "$vendor/$name"
  for f in "$@"; do cp "$tmp/package/$f" "$vendor/$name/"; done
  echo "$version" > "$vendor/$name/VERSION"
  rm -rf "$tmp"
}

fetch web-streams-polyfill 4.3.0 dist/ponyfill.mjs LICENSE

# Intl polyfills from FormatJS (MIT), loaded lazily by js/src/intl.ts: Goja
# has no Intl. Laid out as node_modules so their bare imports of each other
# resolve when the prelude is bundled. Versions are the exact dependency
# closure of @formatjs/intl-segmenter and @formatjs/intl-locale.
formatjs() { # name version [extra files under the package]
  name=$1 version=$2
  shift 2
  tmp=$(mktemp -d)
  curl -fsSL "https://registry.npmjs.org/@formatjs/$name/-/$name-$version.tgz" | tar xz -C "$tmp"
  dest="$vendor/node_modules/@formatjs/$name"
  rm -rf "$dest"
  mkdir -p "$dest"
  for f in "$tmp"/package/*.js "$tmp/package/package.json" "$tmp/package/LICENSE.md"; do
    case "$f" in *.iife.js) continue ;; esac
    [ -f "$f" ] && cp "$f" "$dest/"
  done
  for f in "$@"; do
    mkdir -p "$dest/$(dirname "$f")"
    cp "$tmp/package/$f" "$dest/$f"
  done
  rm -rf "$tmp"
}
formatjs fast-memoize 3.1.7
formatjs intl-getcanonicallocales 3.2.12
formatjs intl-supportedvaluesof 2.3.11
formatjs intl-locale 5.3.12
formatjs intl-localematcher 0.9.0
formatjs intl-segmenter 12.2.15
# Intl.PluralRules / NumberFormat / DateTimeFormat (Goja has none): the
# polyfills with English locale data and the IANA time zone data; other
# locales' data is not vendored (a few hundred MB).
formatjs bigdecimal 0.2.7
formatjs intl-pluralrules 6.3.15 locale-data/en.js
formatjs intl-numberformat 9.4.3 locale-data/en.js
formatjs intl-datetimeformat 7.8.1 locale-data/en.js
# Intl.ListFormat / RelativeTimeFormat / DisplayNames, English data.
formatjs intl-listformat 8.3.15 locale-data/en.js locale-data/en-GB.js
formatjs intl-relativetimeformat 12.3.15 locale-data/en.js
formatjs intl-displaynames 7.3.15 locale-data/en.js

# RegExp match indices (the d flag), which Goja lacks: regexp-match-indices
# (Apache-2.0) and its parser regexp-tree (MIT), loaded lazily by
# js/src/regexp.ts when a d-flag RegExp is first used. node_modules layout so
# the bare require("regexp-tree") resolves when the prelude is bundled.
npm_tree() { # name version
  name=$1 version=$2
  tmp=$(mktemp -d)
  curl -fsSL "https://registry.npmjs.org/$name/-/$(basename "$name")-$version.tgz" | tar xz -C "$tmp"
  dest="$vendor/node_modules/$name"
  rm -rf "$dest"
  mkdir -p "$dest"
  (cd "$tmp/package" && find . -type f \( -name '*.js' -o -name 'package.json' -o -name 'LICENSE*' \) ! -path './node_modules/*' ! -name '*.map' | while read -r f; do mkdir -p "$dest/$(dirname "$f")"; cp "$f" "$dest/$f"; done)
  rm -rf "$tmp"
}
npm_tree regexp-match-indices 1.0.2
npm_tree regexp-tree 0.1.27

# Rolldown's parseAst for Vite builds (internal/frameworks/vite): acorn (MIT),
# the ESTree parser Rollup itself used, stands in for Rolldown's native
# (Oxc) parser.
vendor="$root/internal/frameworks/vite/js/vendor"
fetch acorn 8.19.0 dist/acorn.mjs LICENSE
# Source map composition for plugin transform chains (the facade's
# getCombinedSourcemap and the maps handed to esbuild): @jridgewell/remapping
# (MIT), as Vite's own combineSourcemaps uses, and its dependencies.
fetch @jridgewell/remapping 2.3.5 dist/remapping.mjs LICENSE
fetch @jridgewell/trace-mapping 0.3.31 dist/trace-mapping.mjs LICENSE
fetch @jridgewell/gen-mapping 0.3.13 dist/gen-mapping.mjs LICENSE
fetch @jridgewell/sourcemap-codec 1.6.0 dist/sourcemap-codec.mjs LICENSE
fetch @jridgewell/resolve-uri 3.1.2 dist/resolve-uri.mjs LICENSE

# Node-API over WebAssembly for build-time JS (internal/jshost): Oganesson runs
# napi-rs wasm32-wasi addons (e.g. Tailwind v4's oxide) with it instead of
# emnapi, whose loader needs Node's WASI and worker threads. MIT (stated in
# package.json; the package ships no LICENSE file).
vendor="$root/internal/jshost/js/vendor"
fetch napi-wasm 1.1.3 index.mjs package.json README.md

# Node built-in polyfills for runtime/nodeshim: unenv (complete API surface
# of every Node module; Oganesson overrides a few with Go-backed implementations)
# and its one dependency, pathe. Only the runtime ESM files are kept.
nodeshim="$root/runtime/nodeshim/vendor"
vendor_tree() { # dest name version subdir
  dest=$1 name=$2 version=$3 sub=$4
  tmp=$(mktemp -d)
  curl -fsSL "https://registry.npmjs.org/$name/-/$name-$version.tgz" | tar xz -C "$tmp"
  rm -rf "$dest/$name"
  mkdir -p "$dest/$name"
  (cd "$tmp/package" && find "$sub" -name '*.mjs' -o -name 'LICENSE' | cpio -pdm --quiet "$dest/$name")
  cp "$tmp/package/LICENSE" "$dest/$name/" 2>/dev/null || true
  echo "$version" > "$dest/$name/VERSION"
  rm -rf "$tmp"
}
rm -rf "$root/runtime/nodeshim/js/vendor"
vendor_tree "$nodeshim" unenv 2.0.0-rc.24 dist/runtime
vendor_tree "$nodeshim" pathe 2.0.3 dist

# node:stream for runtime/nodeshim: readable-stream (MIT), the userland copy
# of Node's own stream implementation (lib/stream.js is Node's module:
# the legacy Stream constructor with Readable, Writable, … on it). unenv's
# streams are stubs, and Node servers (Express, send, compression) need
# real ones. Its own dependencies resolve to built-ins (nodeshim.go).
vendor_cjs_tree() { # dest name version subdir
  dest=$1 name=$2 version=$3 sub=$4
  tmp=$(mktemp -d)
  curl -fsSL "https://registry.npmjs.org/$name/-/$name-$version.tgz" | tar xz -C "$tmp"
  rm -rf "$dest/$name"
  mkdir -p "$dest/$name"
  (cd "$tmp/package" && find "$sub" -name '*.js' | cpio -pdm --quiet "$dest/$name")
  cp "$tmp/package/LICENSE" "$dest/$name/"
  echo "$version" > "$dest/$name/VERSION"
  rm -rf "$tmp"
}
vendor_cjs_tree "$nodeshim" readable-stream 4.7.0 lib
# node:string_decoder (readable-stream's own dependency; unenv's is a stub).
vendor_cjs_tree "$nodeshim" string_decoder 1.3.0 lib
# _http_common's HTTPParser (msw's interceptors parse raw HTTP with it):
# http-parser-js (MIT), a pure-JS implementation of the API of Node's
# http_parser binding (js/http_common.js).
hpj_version=0.5.10
tmp=$(mktemp -d)
curl -fsSL "https://registry.npmjs.org/http-parser-js/-/http-parser-js-$hpj_version.tgz" | tar xz -C "$tmp"
rm -rf "$nodeshim/http-parser-js"
mkdir -p "$nodeshim/http-parser-js"
cp "$tmp/package/http-parser.js" "$tmp/package/LICENSE.md" "$nodeshim/http-parser-js/"
echo "$hpj_version" > "$nodeshim/http-parser-js/VERSION"
rm -rf "$tmp"
# Node's own lib files, at the Node release og's fidelity checks compare
# against: node:path (lib/path.js; unenv's is pathe, which turns every
# backslash into a separator, so posix.join("a\\-b") is "a/-b", and aliases
# win32 to posix), the web stream adapters behind Readable.toWeb and
# friends (lib/internal/webstreams/adapters.js; readable-stream leaves them
# out), fs.ReadStream / fs.WriteStream (lib/internal/fs/streams.js, over
# og's fs open / read / write / close), and node:diagnostics_channel
# (lib/diagnostics_channel.js; unenv's TracingChannel is a stub, which
# ioredis calls). Their internal requires (primordials, validators, errors) are
# shimmed at load time (runtime/nodeshim/nodeshim.go). The LICENSE keeps
# Node's notice and Joyent's; the rest of Node's LICENSE covers its bundled
# dependencies.
node_version=v24.15.0
rm -rf "$nodeshim/node"
for f in lib/path.js lib/internal/webstreams/adapters.js lib/internal/fs/streams.js lib/diagnostics_channel.js; do
  mkdir -p "$nodeshim/node/$(dirname "$f")"
  curl -fsSL "https://raw.githubusercontent.com/nodejs/node/$node_version/$f" -o "$nodeshim/node/$f"
done
curl -fsSL "https://raw.githubusercontent.com/nodejs/node/$node_version/LICENSE" | awk 'n < 4 { print } /^"""/ { n++ }' > "$nodeshim/node/LICENSE"
echo "$node_version" > "$nodeshim/node/VERSION"
# Node's bundled minimatch (deps/minimatch: one CommonJS file, its own
# BlueOak-1.0.0 license), behind path.matchesGlob as in Node
# (lib/internal/fs/glob.js matchGlobPattern; js/node_internals.js).
rm -rf "$nodeshim/minimatch"
mkdir -p "$nodeshim/minimatch"
for f in index.js LICENSE.md; do
  curl -fsSL "https://raw.githubusercontent.com/nodejs/node/$node_version/deps/minimatch/$f" -o "$nodeshim/minimatch/$f"
done
curl -fsSL "https://raw.githubusercontent.com/nodejs/node/$node_version/deps/minimatch/package.json" | sed -n 's/^  "version": "\(.*\)",$/\1/p' > "$nodeshim/minimatch/VERSION"
