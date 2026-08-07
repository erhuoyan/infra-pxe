#!/bin/bash
# build-builder.sh — 构建 mkimage builder Docker 镜像（一次性）
#
# 预缓存所有 apk 包、kernel、modloop，后续 build-iso.sh 不再联网。
# aports 在宿主机预先 clone 并 COPY 进 Docker（避开 Docker 内 git 不通）。
#
# 用法: bash alpine/mkimage/build-builder.sh

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
ALPINE_VER="3.24"
MIRROR="https://mirrors.nju.edu.cn/alpine"
IMAGE="infra-pxe-mkimage-builder:${ALPINE_VER}"

echo "═══════════════════════════════════════════════"
echo " Building mkimage builder image"
echo " Base: alpine:${ALPINE_VER}  Mirror: ${MIRROR}"
echo "═══════════════════════════════════════════════"

# Pre-clone aports on host
APORTS_CACHE="$ROOT/release/iso/aports-cache"
if [ ! -d "$APORTS_CACHE/.git" ]; then
    echo "── Cloning aports (one-time, ~100MB)..."
    rm -rf "$APORTS_CACHE"
    git clone --depth=1 --single-branch \
        https://ghproxy.net/https://github.com/alpinelinux/aports.git \
        "$APORTS_CACHE" || {
        echo "ERROR: cannot clone aports via ghproxy"
        echo "  Try: git clone --depth=1 https://github.com/alpinelinux/aports.git $APORTS_CACHE"
        exit 1
    }
    echo "   ✓ $(du -sh "$APORTS_CACHE" | cut -f1)"
fi
# Build context
DOCKER_CTX=$(mktemp -d)
trap "rm -rf $DOCKER_CTX" EXIT

cp "$SCRIPT_DIR/mkimg.pxe.sh" "$DOCKER_CTX/"
cp "$SCRIPT_DIR/genapkovl-pxe.sh" "$DOCKER_CTX/"
cp -a "$APORTS_CACHE" "$DOCKER_CTX/aports"

cat > "$DOCKER_CTX/Dockerfile" << 'DOCKERFILE'
FROM alpine:3.24
ARG MIRROR=https://mirrors.nju.edu.cn/alpine
ARG ALPINE_VER=3.24

RUN printf '%s\n' "${MIRROR}/v${ALPINE_VER}/main" "${MIRROR}/v${ALPINE_VER}/community" \
    > /etc/apk/repositories

RUN apk update && apk add --no-cache \
    alpine-sdk alpine-conf syslinux xorriso squashfs-tools \
    grub grub-efi mtools dosfstools \
    sudo bash git fakeroot abuild apk-tools

RUN adduser -D build -G abuild && \
    echo "build ALL=(ALL) NOPASSWD: ALL" >> /etc/sudoers && \
    mkdir -p /etc/apk/keys

USER build
WORKDIR /home/build
RUN abuild-keygen -i -a -n

# aports pre-cloned on host, copied in
COPY --chown=build:abuild aports /home/build/aports

COPY --chown=build:abuild mkimg.pxe.sh /home/build/aports/scripts/mkimg.pxe.sh
COPY --chown=build:abuild genapkovl-pxe.sh /home/build/aports/scripts/genapkovl-pxe.sh
RUN chmod +x /home/build/aports/scripts/mkimg.pxe.sh \
             /home/build/aports/scripts/genapkovl-pxe.sh

# Pre-build to cache all apk packages
RUN mkdir -p /home/build/iso /home/build/workdir && \
    cd /home/build/aports/scripts && \
    sh mkimage.sh \
        --tag "v${ALPINE_VER}" \
        --outdir /home/build/iso \
        --workdir /home/build/workdir \
        --arch x86_64 \
        --repository "${MIRROR}/v${ALPINE_VER}/main" \
        --repository "${MIRROR}/v${ALPINE_VER}/community" \
        --profile pxe && \
    echo "Pre-build done" && \
    rm -f /home/build/iso/*.iso

VOLUME ["/home/build/output"]
DOCKERFILE

echo "Building (first time ~5-10 min, caches all apks)..."
docker build \
    --build-arg MIRROR="$MIRROR" \
    --build-arg ALPINE_VER="$ALPINE_VER" \
    -t "$IMAGE" \
    "$DOCKER_CTX"

echo ""
echo "═══════════════════════════════════════════════"
echo " ✓ $IMAGE"
echo ""
echo "  后续 build-iso.sh 使用此镜像，无需联网。"
echo "═══════════════════════════════════════════════"
