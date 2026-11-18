#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

if [[ "$(uname -s)" != Darwin || "$(uname -m)" != arm64 ]]; then
  echo "Cage Client.app must be built on Apple Silicon macOS." >&2
  exit 1
fi

export GOTOOLCHAIN=auto
export CAGE_CLIENT_BUILD=1
export MACOSX_DEPLOYMENT_TARGET=14.0
export CGO_CFLAGS="${CGO_CFLAGS:-} -mmacosx-version-min=14.0"
export CGO_LDFLAGS="${CGO_LDFLAGS:-} -mmacosx-version-min=14.0"

bash scripts/prepare-resources.sh --client

build_flags=(-platform darwin/arm64 -skip-dmg -skip-notarize)
CGO_ENABLED=1 go tool mygo build "${build_flags[@]}"

app="_output/client-dist/darwin-arm64/Cage Client.app"
if [[ ! -d "$app" ]]; then
  echo "Expected client bundle not found at $app" >&2
  exit 1
fi

codesign --verify --deep --strict --verbose=2 "$app"
"$app/Contents/Resources/cage" verify-web-assets

version="$(tr -d '\r\n' < VERSION)"
zip_out="_output/client-dist/darwin-arm64/Cage-Client-$version-macos-arm64.zip"
rm -f "$zip_out"
ditto -c -k --keepParent "$app" "$zip_out"
echo "Built standalone client: $app ($zip_out)"
