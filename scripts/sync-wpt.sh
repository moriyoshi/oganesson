#!/bin/sh
# Fetches the Web Platform Tests subset Oganesson runs (sparse checkout, pinned).
set -eu
WPT_COMMIT=${WPT_COMMIT:-5cd8e3fa0a6c4ca11fa565f7c0956802c8e0045d}
root=$(cd "$(dirname "$0")/.." && pwd)
dest="$root/third_party/wpt"
dirs="resources url compression fetch/api/resources WebCryptoAPI/util encoding fetch/api/headers fetch/api/request fetch/api/response fetch/api/body fetch/data-urls streams FileAPI/blob FileAPI/file FileAPI/support xhr/formdata dom/events dom/abort html/webappapis/structured-clone html/webappapis/atob html/webappapis/timers WebCryptoAPI/digest WebCryptoAPI/getRandomValues.any.js WebCryptoAPI/randomUUID.https.any.js common"

if [ ! -d "$dest/.git" ]; then
  git clone --depth 1 --filter=blob:none --sparse https://github.com/web-platform-tests/wpt "$dest"
fi
git -C "$dest" sparse-checkout set --no-cone $(for d in $dirs; do printf '/%s ' "$d"; done)
if [ -n "$WPT_COMMIT" ] && [ "$(git -C "$dest" rev-parse HEAD)" != "$WPT_COMMIT" ]; then
  git -C "$dest" fetch --depth 1 origin "$WPT_COMMIT"
  git -C "$dest" checkout --quiet "$WPT_COMMIT"
fi
echo "wpt @ $(git -C "$dest" rev-parse HEAD)"
