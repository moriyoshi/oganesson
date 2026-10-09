#!/bin/sh
# Fetches TypeScript-Go at the pinned commit (sparse: tsc/ only) and installs
# the Oganesson facade package into it. Re-run after bumping TYPESCRIPT_COMMIT.
set -eu
TYPESCRIPT_COMMIT=edf7da4e93ab65e1f7766deae156c35132c0cde3
root=$(cd "$(dirname "$0")/.." && pwd)
dest="$root/third_party/typescript"

if [ ! -d "$dest/.git" ]; then
  git clone --filter=blob:none --sparse --no-checkout https://github.com/microsoft/TypeScript "$dest"
  git -C "$dest" sparse-checkout set tsc
fi
if [ "$(git -C "$dest" rev-parse HEAD 2>/dev/null)" != "$TYPESCRIPT_COMMIT" ]; then
  git -C "$dest" fetch --depth 1 origin "$TYPESCRIPT_COMMIT"
  git -C "$dest" checkout --quiet "$TYPESCRIPT_COMMIT"
fi

rm -rf "$dest/tsc/ogapi"
cp -R "$root/_tsoverlay/ogapi" "$dest/tsc/ogapi"
echo "typescript-go @ $TYPESCRIPT_COMMIT with ogapi installed"
