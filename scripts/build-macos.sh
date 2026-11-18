#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."

if [[ "$(uname -s)" != Darwin || "$(uname -m)" != arm64 ]]; then
  echo "Airlock.app must be built on Apple Silicon macOS." >&2
  exit 1
fi

skip_dmg="-skip-dmg"
install=false
for arg in "$@"; do
  case "$arg" in
    --dmg) skip_dmg="" ;;
    --install) install=true ;;
    *) echo "Usage: scripts/build-macos.sh [--dmg] [--install]" >&2; exit 2 ;;
  esac
done

export GOTOOLCHAIN=auto
export MACOSX_DEPLOYMENT_TARGET=14.0
export CGO_CFLAGS="${CGO_CFLAGS:-} -mmacosx-version-min=14.0"
export CGO_LDFLAGS="${CGO_LDFLAGS:-} -mmacosx-version-min=14.0"

bash scripts/prepare-resources.sh

build_flags=(-platform darwin/arm64)
if [[ -n "$skip_dmg" ]]; then
  build_flags+=("$skip_dmg")
fi
if [[ -z "${CAGE_NOTARY_PROFILE:-}" ]]; then
  build_flags+=(-skip-notarize)
fi

CGO_ENABLED=1 go tool mygo build "${build_flags[@]}"

app="_output/dist/darwin-arm64/Airlock.app"
if [[ ! -d "$app" ]]; then
  echo "Expected app bundle not found at $app" >&2
  exit 1
fi

# Verify VZ entitlement on nested cage helper and re-sign outer bundle if needed.
if ! codesign -d --entitlements :- "$app/Contents/Resources/cage" 2>/dev/null | grep -q 'com.apple.security.virtualization'; then
  identity="${CAGE_SIGNING_IDENTITY:--}"
  sign=(codesign --force --sign "$identity")
  if [[ "$identity" != - ]]; then sign+=(--timestamp --options runtime); fi
  "${sign[@]}" --entitlements third_party/lima/vz.entitlements "$app/Contents/Resources/cage"
  "${sign[@]}" "$app"
fi

codesign --verify --deep --strict --verbose=2 "$app"
"$app/Contents/Resources/cage" verify-macos-guest
"$app/Contents/Resources/cage" verify-vm-assets
"$app/Contents/Resources/cage" verify-web-assets
version="$(tr -d '\r\n' < VERSION)"
node scripts/guest-os/collect-macos-sshfs-sources.mjs verify \
  "$app/Contents/Resources/guest-os/sources/cage-macos-guest-$version-source.tar.gz" \
  "$app/Contents/Resources/guest-os/project/cage-sshfs" "$version"

echo "Built and verified: $app"

if [[ "$install" == true ]]; then
  mkdir -p "$HOME/Applications"
  dest="$HOME/Applications/Airlock.app"
  stage="$(mktemp -d "$HOME/Applications/.Airlock-install.XXXXXX")"
  trap 'rm -rf "$stage"' EXIT
  ditto "$app" "$stage/Airlock.app"
  codesign --verify --deep --strict --verbose=2 "$stage/Airlock.app"
  rm -rf "$dest"
  mv "$stage/Airlock.app" "$dest"
  echo "Installed: $dest"
fi
