#!/bin/sh
# Builds the og binary for release (and for anything packaged with og build
# -o, which copies the og executable it runs as).
#
#   [OG_VERSION=vX.Y.Z] scripts/build-og.sh [out] [goos/goarch]
#
# - OG_VERSION: the release version, stamped into main.version. og build
#   -eject pins github.com/moriyoshi/oganesson at it in the ejected go.mod,
#   so it must be the module version of this source (the release tag).
# - CGO_ENABLED=0: Oganesson is pure Go.
# - -tags nodynamic: gen2brain/webp (the /_next/image WebP encoder) would
#   otherwise dlopen a system libwebp when one is installed; with the tag it
#   always uses its bundled pure-Go build, so the binary behaves the same on
#   every host.
# - -trimpath: no build-machine paths in the binary.
# - -ldflags "-s -w": drop the symbol table and DWARF debug info. og build -o
#   copies this binary into every packaged app, so the ~30% it saves applies to
#   both the release og and every shipped app. Stack traces keep file:line from
#   runtime.pclntab, which -w does not strip.
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
out=${1:-"$root/.agents-workspace/tmp/og"}
target=${2:-}
if [ -n "$target" ]; then
  GOOS=${target%/*}
  GOARCH=${target#*/}
  export GOOS GOARCH
fi
version=${OG_VERSION:-}
if [ -n "$version" ] && ! printf '%s\n' "$version" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z.-]+)?$'; then
  echo "build-og.sh: OG_VERSION $version is not a module version (vX.Y.Z[-pre])" >&2
  exit 1
fi
cd "$root"
CGO_ENABLED=0 go build -trimpath -tags nodynamic -ldflags "-s -w -X main.version=$version" -o "$out" ./cmd/og
echo "built $out${version:+ $version}${target:+ ($target)}"
