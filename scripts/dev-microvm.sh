#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
if [[ "$(uname -s)" != Darwin || "$(uname -m)" != arm64 ]]; then
  echo "MicroVM development requires Apple Silicon macOS." >&2
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
make cage
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
