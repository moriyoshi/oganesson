#!/bin/sh
# Fetches vercel/next.js's examples/ (sparse checkout, pinned to the Next
# release og compat installs for "latest": corpusNext in cmd/og/compat.go)
# for og compat, the compatibility corpus (M11).
set -eu
NEXT_TAG=${NEXT_TAG:-v16.4.0}
NEXT_COMMIT=${NEXT_COMMIT:-e273d5b2e869d0be06c0e86f788e1594f9dd40cf}
root=$(cd "$(dirname "$0")/.." && pwd)
dest="$root/third_party/next.js"

if [ ! -d "$dest/.git" ]; then
  git clone --depth 1 --branch "$NEXT_TAG" --filter=blob:none --sparse https://github.com/vercel/next.js "$dest"
fi
git -C "$dest" sparse-checkout set --no-cone /examples
if [ "$(git -C "$dest" rev-parse HEAD)" != "$NEXT_COMMIT" ]; then
  git -C "$dest" fetch --depth 1 origin "$NEXT_COMMIT"
  git -C "$dest" checkout --quiet "$NEXT_COMMIT"
fi
echo "next.js examples @ $NEXT_TAG ($(git -C "$dest" rev-parse HEAD)): $(ls "$dest/examples" | wc -l | tr -d ' ') examples"
