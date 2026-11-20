#!/usr/bin/env bash
# Fast desktop development on the local MicroVM image: rebuild only stale embedded
# assets and the cage helper, then run mygo dev. The first run (or a missing
# resources/darwin-arm64) falls back to prepare-resources.sh --fast.
set -euo pipefail
cd "$(dirname "$0")/.."

if [[ "$(uname -s)" != Darwin || "$(uname -m)" != arm64 ]]; then
  echo "cage-mygo requires Apple Silicon macOS (darwin/arm64)." >&2
  exit 1
fi

export AIRLOCK_MICROVM_IMAGE_DIR="${AIRLOCK_MICROVM_IMAGE_DIR:-$PWD/_output/microvm-image}"
export CLAUDE_CAGE_CONFIG="${AIRLOCK_MICROVM_CONFIG:-$HOME/.claude-cage-microvm-dev/config.json}"
for file in vmlinuz rootfs.img data.img; do
  if [[ ! -s "$AIRLOCK_MICROVM_IMAGE_DIR/$file" ]]; then
    echo "Missing $AIRLOCK_MICROVM_IMAGE_DIR/$file; run make microvm-image first." >&2
    exit 1
  fi
done

export AIRLOCK_MICROVM_DEV=1
export GOTOOLCHAIN=auto
export CGO_ENABLED=1
# Match prepare-resources.sh so both paths share the Go build cache.
export MACOSX_DEPLOYMENT_TARGET=14.0
export CGO_CFLAGS="${CGO_CFLAGS:-} -mmacosx-version-min=14.0"
export CGO_LDFLAGS="${CGO_LDFLAGS:-} -mmacosx-version-min=14.0"

res=resources/darwin-arm64

# stale TARGET SOURCE...: TARGET is missing or any SOURCE is newer than it.
stale() {
  local target=$1
  shift
  [[ ! -e "$target" ]] || [[ -n "$(find "$@" -newer "$target" -print -quit 2>/dev/null)" ]]
}

if [[ ! -x "$res/cage" || ! -x "$res/mihomo" || ! -x "$res/CageUpdateInstaller" || ! -d "$res/licenses" ]]; then
  bash scripts/prepare-resources.sh --fast
else
  web_rebuilt=0
  if stale internal/workbench/web/build-integrity.json web-surface third_party/novnc internal/cage/novnc-patch.py scripts/build-web-surface.mjs; then
    make web-surface
    web_rebuilt=1
  fi
  if stale internal/limaembed/assets/Darwin/lima-guestagent.Darwin-aarch64.gz internal/limaembed/lima-version go.mod third_party/lima/templates \
    || [[ ! -d internal/limaembed/assets/templates ]]; then
    make assets
  fi
  if stale internal/tailnetassets/assets/cage-tailnet-linux-arm64.gz tools/tailnet \
    || [[ ! -e internal/tailnetassets/assets/cage-tailnet-linux-amd64.gz ]]; then
    make tailnet-assets
  fi
  # go build is incremental; an unchanged helper relinks from cache in seconds.
  make cage-bin
  if [[ "$web_rebuilt" == 1 ]]; then
    _output/bin/cage verify-web-assets
  fi
  cp _output/bin/cage "$res/cage"
  codesign --force --entitlements third_party/lima/vz.entitlements --sign "${CAGE_SIGNING_IDENTITY:--}" "$res/cage"
fi

# The development profile runs its own signed helper copy.
mkdir -p "$(dirname "$CLAUDE_CAGE_CONFIG")/bin"
chmod 0700 "$(dirname "$CLAUDE_CAGE_CONFIG")"
export CAGE_HELPER_BIN="$(dirname "$CLAUDE_CAGE_CONFIG")/bin/cage"
staged_helper="$(mktemp "$(dirname "$CAGE_HELPER_BIN")/.cage-XXXXXX")"
trap 'rm -f "$staged_helper"' EXIT
cp _output/bin/cage "$staged_helper"
chmod 0755 "$staged_helper"
codesign -f --entitlements third_party/lima/vz.entitlements -s - "$staged_helper"
mv -f "$staged_helper" "$CAGE_HELPER_BIN"
printf 'MicroVM images: %s\nDevelopment config: %s\nChoose shared folders with Add Project in the app.\n' "$AIRLOCK_MICROVM_IMAGE_DIR" "$CLAUDE_CAGE_CONFIG"
exec go tool mygo dev
