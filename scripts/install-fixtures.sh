#!/bin/sh
# Installs the node_modules of test fixtures with og's own installer, so the
# fixture-based suites run instead of skipping (see .agents/docs/TESTING.md).
# Uses the network.
#
#   scripts/install-fixtures.sh                       # every fixture
#   scripts/install-fixtures.sh 'testdata/apps/vite-*' testdata/plugins/postcss
#
# Arguments are directories or shell patterns relative to the repository root.
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
cd "$root"
[ $# -gt 0 ] || set -- 'testdata/apps/*' 'testdata/plugins/*'

og=$(mktemp -d)/og
trap 'rm -rf "$(dirname "$og")"' EXIT
go build -tags nodynamic -o "$og" ./cmd/og

for pattern in "$@"; do
  # Unquoted on purpose: expand the pattern.
  for dir in $pattern; do
    [ -f "$dir/package.json" ] || continue
    echo "== $dir"
    "$og" install "$dir"
  done
done
