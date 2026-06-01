#!/usr/bin/env bash
# build-mcpb.sh — package loom-mcp as a Claude Desktop Extension (.mcpb).
#
# A .mcpb is just a zip of the manifest plus the loom-mcp binary. Claude
# Desktop installs it via Settings → Extensions. One bundle per OS/arch
# because it ships a native binary.
#
# Usage:
#   scripts/build-mcpb.sh <version> [goos] [goarch]
#   scripts/build-mcpb.sh 0.4.3 darwin arm64
#
# Defaults to the host platform. Output: dist/loom-<version>-<os>-<arch>.mcpb
set -euo pipefail

VERSION="${1:?usage: build-mcpb.sh <version> [goos] [goarch]}"
GOOS_ARG="${2:-$(go env GOOS)}"
GOARCH_ARG="${3:-$(go env GOARCH)}"

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
STAGE="$(mktemp -d)"
trap 'rm -rf "$STAGE"' EXIT

BIN_NAME="loom-mcp"
[ "$GOOS_ARG" = "windows" ] && BIN_NAME="loom-mcp.exe"

mkdir -p "$STAGE/bin" "$ROOT/dist"

echo "building $BIN_NAME for $GOOS_ARG/$GOARCH_ARG ..."
CGO_ENABLED=0 GOOS="$GOOS_ARG" GOARCH="$GOARCH_ARG" \
  go build -trimpath -ldflags "-s -w -X main.Version=$VERSION" \
  -o "$STAGE/bin/$BIN_NAME" "$ROOT/cmd/loom-mcp"

# entry_point must match the binary name for the target OS.
sed -e "s/\"version\": \"0.0.0\"/\"version\": \"$VERSION\"/" \
    -e "s#\"entry_point\": \"bin/loom-mcp\"#\"entry_point\": \"bin/$BIN_NAME\"#" \
    -e "s#\${__dirname}/bin/loom-mcp#\${__dirname}/bin/$BIN_NAME#" \
    "$ROOT/extension/manifest.json" > "$STAGE/manifest.json"

# sanity: valid JSON
python3 -c "import json,sys; json.load(open('$STAGE/manifest.json'))" \
  2>/dev/null || node -e "require('$STAGE/manifest.json')"

OUT="$ROOT/dist/loom-${VERSION}-${GOOS_ARG}-${GOARCH_ARG}.mcpb"
rm -f "$OUT"
( cd "$STAGE" && zip -q -r "$OUT" manifest.json bin )
echo "wrote $OUT"
