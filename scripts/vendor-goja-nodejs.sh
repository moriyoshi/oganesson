#!/bin/sh
# Vendors goja_nodejs's CommonJS loader (require/) at the pinned commit into
# third_party/goja_nodejs/require, rewritten for Sobek (grafana/sobek, the
# Goja fork og runs on): goja_nodejs imports Goja, and there is no Sobek port.
# Only module.go and resolve.go are kept (og's jshost is the sole user).
# Re-run after bumping GOJA_NODEJS_VERSION.
set -eu
GOJA_NODEJS_VERSION=v0.0.0-20260918173711-b481721df8a2
root=$(cd "$(dirname "$0")/.." && pwd)
dest="$root/third_party/goja_nodejs"
src=$(cd "$root" && GOWORK=off GOFLAGS=-mod=mod go mod download -json "github.com/dop251/goja_nodejs@$GOJA_NODEJS_VERSION" | sed -n 's/^[[:space:]]*"Dir": "\(.*\)",$/\1/p')
rm -rf "$dest"
mkdir -p "$dest/require"
for f in module.go resolve.go; do
  sed -e 's#js "github.com/dop251/goja"#js "github.com/grafana/sobek"#' \
      -e 's#"github.com/dop251/goja/parser"#"github.com/grafana/sobek/parser"#' \
      "$src/require/$f" > "$dest/require/$f"
done
cp "$src/LICENSE" "$dest/LICENSE"
chmod -R u+w "$dest"
echo "$GOJA_NODEJS_VERSION" > "$dest/VERSION"
if grep -rq 'dop251/goja"' "$dest"; then echo "vendor-goja-nodejs: an import was not rewritten" >&2; exit 1; fi
