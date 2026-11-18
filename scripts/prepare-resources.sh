#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

if [[ "$(uname -s)" != Darwin || "$(uname -m)" != arm64 ]]; then
  echo "cage-mygo requires Apple Silicon macOS (darwin/arm64)." >&2
  exit 1
fi

mode="full"
case "${1:-}" in
  "") ;;
  --fast) mode="fast" ;;
  --client) mode="client" ;;
  *) echo "Usage: scripts/prepare-resources.sh [--fast|--client]" >&2; exit 2 ;;
esac

export GOTOOLCHAIN=auto
export MACOSX_DEPLOYMENT_TARGET=14.0
export CGO_CFLAGS="${CGO_CFLAGS:-} -mmacosx-version-min=14.0"
export CGO_LDFLAGS="${CGO_LDFLAGS:-} -mmacosx-version-min=14.0"

version="$(tr -d '\r\n' < VERSION)"
res="$PWD/resources/darwin-arm64"
rm -rf "$res"
mkdir -p "$res"

node scripts/build-web-surface.mjs
make assets
make tailnet-assets

if [[ "$mode" == client ]]; then
  mkdir -p _output
  CGO_ENABLED=0 go list -tags cageclient -deps ./cmd/cage-client > _output/client-deps.txt
  if grep -Eq 'internal/limaembed|internal/vm(/|$)|internal/maclogin|github.com/lima-vm/lima/v2/pkg/(driver|hostagent|instance|store)|github.com/Code-Hex/vz' _output/client-deps.txt; then
    echo "The lightweight client unexpectedly depends on a virtual machine runtime." >&2
    exit 1
  fi
  CGO_ENABLED=0 go build -tags cageclient -trimpath \
    -ldflags "-s -w -X github.com/sylvinus/agent-vm/internal/version.Version=$version" \
    -o "$res/cage" ./cmd/cage-client
  "$res/cage" verify-web-assets
  codesign --force --sign "${CAGE_SIGNING_IDENTITY:--}" "$res/cage"
  node scripts/bundle-cloud-kit.mjs "$res"
  node scripts/collect-app-licenses.mjs "$res/licenses"
  exit 0
fi

bash scripts/fetch-proxy-core.sh
cp _output/proxy-core/mihomo "$res/mihomo"

if [[ "$mode" == fast ]]; then
  make cage VERSION="$version"
else
  bash scripts/guest-os/build-macos-guest-core.sh "$version"
fi
_output/bin/cage verify-web-assets
cp _output/bin/cage "$res/cage"
codesign --force --entitlements third_party/lima/vz.entitlements --sign "${CAGE_SIGNING_IDENTITY:--}" "$res/cage"
if [[ "$mode" != fast ]]; then
  "$res/cage" verify-macos-guest
fi
"$res/cage" verify-vm-assets

CGO_ENABLED=1 go build -trimpath -ldflags "-s -w -X github.com/sylvinus/agent-vm/internal/version.Version=$version" \
  -o "$res/CageUpdateInstaller" ./cmd/cage-update-installer
codesign --force --sign "${CAGE_SIGNING_IDENTITY:--}" "$res/CageUpdateInstaller"

if [[ "$mode" != fast ]]; then
  bash scripts/guest-os/package-macos-guest.sh "$res/guest-os"
  if [[ -d "$res/guest-os/licenses/sshfs" ]]; then
    mkdir -p "$res/licenses/sshfs"
    cp -R "$res/guest-os/licenses/sshfs/." "$res/licenses/sshfs/"
  fi
  node scripts/bundle-cloud-kit.mjs "$res"
fi

mkdir -p "$res/licenses/mihomo"
cp _output/proxy-core/LICENSE _output/proxy-core/NOTICE "$res/licenses/mihomo/"
node scripts/collect-app-licenses.mjs "$res/licenses"
