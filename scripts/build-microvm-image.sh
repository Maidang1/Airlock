#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="${AIRLOCK_IMAGE_OUT:-$ROOT_DIR/_output/microvm-image}"
INIT_BIN="$ROOT_DIR/_output/bin/airlock-init"
IMAGE_TAG="airlock-microvm-rootfs:arm64"

mkdir -p "$OUT_DIR" "$(dirname "$INIT_BIN")"

echo "==> Building static linux/arm64 airlock-init..."
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 GOTOOLCHAIN=auto go build \
  -trimpath -ldflags "-s -w" \
  -o "$INIT_BIN" "$ROOT_DIR/cmd/airlock-init"

if ! command -v docker >/dev/null 2>&1; then
  echo "error: docker is required to assemble the Debian arm64 rootfs" >&2
  exit 1
fi

BUILD_CTX="$(mktemp -d)"
trap 'rm -rf "$BUILD_CTX"' EXIT
cp "$INIT_BIN" "$BUILD_CTX/airlock-init"

cat >"$BUILD_CTX/Dockerfile" <<'EOF'
FROM node:22-bookworm-slim AS node-runtime
FROM debian:bookworm-slim
ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get -o Acquire::Retries=5 -o Acquire::http::Timeout=30 -o Acquire::https::Timeout=30 update \
  && apt-get -o Acquire::Retries=5 -o Acquire::http::Timeout=30 -o Acquire::https::Timeout=30 install -y --no-install-recommends \
    linux-image-arm64 \
    ca-certificates \
    curl \
    git \
    zsh \
    iproute2 \
    iptables \
    locales \
    tzdata \
    sshfs \
    fuse3 \
    libatomic1 \
    libstdc++6 \
    chromium \
    tigervnc-standalone-server \
    openbox \
    x11-xserver-utils \
    e2fsprogs \
  && apt-get clean \
  && rm -rf /var/lib/apt/lists/* /tmp/* /var/tmp/*

COPY --from=node-runtime /usr/local/bin/node /usr/local/bin/node
COPY --from=node-runtime /usr/local/lib/node_modules/ /usr/local/lib/node_modules/
COPY --from=node-runtime /usr/local/include/node/ /usr/local/include/node/
RUN ln -s ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm \
  && ln -s ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx \
  && ln -s node /usr/local/bin/nodejs \
  && node -e 'if (Number(process.versions.node.split(".")[0]) < 22) process.exit(1)' \
  && node --version \
  && npm --version \
  && npm install -g --engine-strict @anthropic-ai/claude-code \
  && claude --version \
  && npm cache clean --force

RUN sed -i 's/^# *en_US.UTF-8/en_US.UTF-8/' /etc/locale.gen \
  && locale-gen \
  && useradd -m -s /bin/zsh -u 1000 work \
  && useradd -m -s /usr/sbin/nologin -u 1001 browser \
  && sed -i 's/^# *user_allow_other/user_allow_other/' /etc/fuse.conf \
  && mkdir -p /data /workspace \
  && rm -f /etc/machine-id /var/lib/dbus/machine-id

COPY airlock-init /sbin/airlock-init
# Direct boot has no systemd/udev after initramfs; load the VZ devices before
# handing control to airlock-init (including its diagnostic console).
RUN printf '%s\n' virtio_console virtio_net vmw_vsock_virtio_transport virtio_balloon virtio_rng overlay fuse >> /etc/initramfs-tools/modules \
  && update-initramfs -u -k all \
  && chmod 0755 /sbin/airlock-init
EOF

echo "==> Building ARM64 rootfs image in Docker..."
BUILD_LOG="$OUT_DIR/build.log"
if ! docker build --platform=linux/arm64 --progress=plain -t "$IMAGE_TAG" "$BUILD_CTX" 2>&1 | tee "$BUILD_LOG"; then
  echo "error: Docker build failed; full output saved to $BUILD_LOG" >&2
  exit 1
fi

echo "==> Extracting kernel, initrd, and building ext4 images..."
CID="$(docker create --platform=linux/arm64 "$IMAGE_TAG")"
trap 'docker rm -f "$CID" >/dev/null 2>&1 || true; rm -rf "$BUILD_CTX"' EXIT

mkdir -p "$BUILD_CTX/rootfs"
docker export "$CID" | tar -C "$BUILD_CTX/rootfs" -xf -

KERNEL_SRC="$(ls "$BUILD_CTX/rootfs"/boot/vmlinuz-* | head -n 1)"
INITRD_SRC="$(ls "$BUILD_CTX/rootfs"/boot/initrd.img-* | head -n 1)"
if [ -z "$KERNEL_SRC" ]; then
  echo "error: kernel vmlinuz not found inside rootfs" >&2
  exit 1
fi
cp "$KERNEL_SRC" "$OUT_DIR/vmlinuz"
if [ -n "$INITRD_SRC" ]; then
  cp "$INITRD_SRC" "$OUT_DIR/initrd.img"
fi

# Strip duplicate compressed kernels inside rootfs before packing rootfs.img.
rm -rf "$BUILD_CTX/rootfs/boot/"*

docker run --rm --platform=linux/arm64 \
  -v "$BUILD_CTX:/work" \
  -v "$OUT_DIR:/out" \
  "$IMAGE_TAG" \
  sh -eu -c '
    rm -f /out/rootfs.img
    # Include filesystem metadata and packing headroom; resize2fs shrinks it below.
    rootfs_mb=$(du -sm /work/rootfs | cut -f1)
    truncate -s "$((rootfs_mb + rootfs_mb / 4 + 256))M" /out/rootfs.img
    mkfs.ext4 -F -d /work/rootfs -L airlock-root /out/rootfs.img
    e2fsck -fy /out/rootfs.img || true
    resize2fs -M /out/rootfs.img
    if [ ! -f /out/data.img ]; then
      truncate -s 4G /out/data.img
      mkfs.ext4 -F -L airlock-data /out/data.img
    fi
  '

echo "==> MicroVM image artifacts ready in $OUT_DIR"
ls -lh "$OUT_DIR"
