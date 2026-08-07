#!/bin/bash
# build-iso.sh — 用官方 mkimage.sh 构建 infra-pxe 一体机 ISO
#
# 前置（只需一次）:
#   bash alpine/mkimage/build-builder.sh    # 构建 builder 镜像（缓存所有 apk）
#   make build-linux                         # 编译 infra-pxe 二进制
#
# 用法:
#   bash alpine/mkimage/build-iso.sh         # x86_64
#   bash alpine/mkimage/build-iso.sh aarch64 # ARM64
#
# 产出: release/dist/infra-pxe-engine-<arch>.iso

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
ARCH="${1:-x86_64}"
ALPINE_VER="3.24"
MIRROR="https://mirrors.nju.edu.cn/alpine"
BUILDER="infra-pxe-mkimage-builder:${ALPINE_VER}"

case "$ARCH" in
    x86_64)  GO_ARCH="amd64"; DOCKER_PLATFORM="linux/amd64" ;;
    aarch64) GO_ARCH="arm64"; DOCKER_PLATFORM="linux/arm64" ;;
    *) echo "ERROR: unsupported arch $ARCH"; exit 1 ;;
esac

DIST="$ROOT/release/dist"
OUTPUT_ISO="$DIST/infra-pxe-engine-${ARCH}.iso"

# 检查 builder 镜像
if ! docker image inspect "$BUILDER" >/dev/null 2>&1; then
    echo "ERROR: builder image $BUILDER not found"
    echo "  Run first: bash alpine/mkimage/build-builder.sh"
    exit 1
fi

# 检查二进制（必须是 Alpine 可直接运行的静态二进制）
BINARY="$ROOT/bin/infra-pxe-linux-${GO_ARCH}"
if [ ! -f "$BINARY" ]; then
    echo "ERROR: missing $BINARY — run: make build-linux"
    exit 1
fi
if file "$BINARY" | grep -qi 'dynamically linked'; then
    echo "ERROR: $BINARY is dynamically linked; rebuild with CGO_ENABLED=0"
    file "$BINARY"
    exit 1
fi
file "$BINARY"

echo "═══════════════════════════════════════════════"
echo " Building infra-pxe Engine ISO (mkimage)"
echo " Arch: $ARCH ($GO_ARCH)"
echo "═══════════════════════════════════════════════"

WORK=$(mktemp -d)
trap "rm -rf $WORK" EXIT

# ═══════════════════════════════════════════════════
# 1. 打包 infra-pxe.tar.gz（放到 ISO payload）
# ═══════════════════════════════════════════════════
echo "[1/2] Packing infra-pxe..."
PXE_STAGE="$WORK/pxe-stage/srv/infra/pxe"
mkdir -p "$PXE_STAGE"/{bin,conf,templates,seeds,boot/tftp,boot/http,scripts}
mkdir -p "$PXE_STAGE/data/dnsmasq"
mkdir -p "$PXE_STAGE"/{logs,backups}

cp "$BINARY" "$PXE_STAGE/bin/infra-pxe"; chmod +x "$PXE_STAGE/bin/infra-pxe"
[ -d "$ROOT/templates" ] && cp -a "$ROOT/templates/"* "$PXE_STAGE/templates/" 2>/dev/null || true
[ -d "$ROOT/seeds"    ] && cp -a "$ROOT/seeds/"*    "$PXE_STAGE/seeds/"    2>/dev/null || true
[ -d "$ROOT/boot/tftp" ] && cp -a "$ROOT/boot/tftp/"* "$PXE_STAGE/boot/tftp/" 2>/dev/null || true
[ -f "$ROOT/scripts/dhcp-event.sh" ] && cp "$ROOT/scripts/dhcp-event.sh" "$PXE_STAGE/scripts/" && chmod +x "$PXE_STAGE/scripts/dhcp-event.sh"
[ -d "$ROOT/templates/static" ] && cp -a "$ROOT/templates/static/"* "$PXE_STAGE/boot/http/" 2>/dev/null || true

cat > "$PXE_STAGE/conf/pxe.yaml" << 'YAML'
engine:
  listen: "0.0.0.0"
  port: 9200
  name: "pxe-engine"

dnsmasq:
  binary: "dnsmasq"
  conf_dir: "data/dnsmasq"
  pid_file: "data/dnsmasq/dnsmasq.pid"
  lease_time: "5m"

data:
  dir: "data"
YAML

PXE_PACK="$WORK/payload/infra-pxe.tar.gz"
mkdir -p "$(dirname "$PXE_PACK")"
(cd "$WORK/pxe-stage" && tar czf "$PXE_PACK" .)
echo "   $(du -sh "$PXE_PACK" | cut -f1)"

# ═══════════════════════════════════════════════════
# 2. Docker 内运行 mkimage.sh
# ═══════════════════════════════════════════════════
echo "[2/2] Building ISO in container..."
mkdir -p "$DIST"
rm -f "$OUTPUT_ISO"

OUTPUT_DIR="$WORK/output"
mkdir -p "$OUTPUT_DIR"
chmod 777 "$OUTPUT_DIR"

docker run --rm \
    --privileged \
    --platform "$DOCKER_PLATFORM" \
    -e PXE_PAYLOAD_DIR=/home/build/payload \
    -v "$(dirname "$PXE_PACK"):/home/build/payload:ro" \
    -v "$OUTPUT_DIR:/home/build/output" \
    -v "$SCRIPT_DIR/mkimg.pxe.sh:/home/build/aports/scripts/mkimg.pxe.sh:ro" \
    -v "$SCRIPT_DIR/genapkovl-pxe.sh:/home/build/aports/scripts/genapkovl-pxe.sh:ro" \
    "$BUILDER" \
    sh -c '
cd /home/build/aports/scripts
sh mkimage.sh \
    --tag "v'"$ALPINE_VER"'" \
    --outdir /home/build/output \
    --workdir /home/build/workdir \
    --arch "'"$ARCH"'" \
    --repository "'"$MIRROR"'/v'"$ALPINE_VER"'/main" \
    --repository "'"$MIRROR"'/v'"$ALPINE_VER"'/community" \
    --profile pxe
'

RESULT_ISO=$(ls "$OUTPUT_DIR"/alpine-pxe-*.iso 2>/dev/null | head -1)
if [ -z "$RESULT_ISO" ]; then
    echo "ERROR: ISO build failed — no output ISO found"
    echo "  Checked: $OUTPUT_DIR"
    ls -la "$OUTPUT_DIR/" 2>/dev/null || true
    exit 1
fi

mv "$RESULT_ISO" "$OUTPUT_ISO"

rm -rf "$WORK"
trap - EXIT

echo ""
echo "═══════════════════════════════════════════════"
echo " ✓ $OUTPUT_ISO"
echo "   $(du -sh "$OUTPUT_ISO" | cut -f1)"
echo ""
echo " Boot →"
echo "   pxe-up eth0 10.0.1.100/24 10.0.1.1"
echo "   pxe-up bond0 10.0.1.100/24 10.0.1.1 eth0,eth1"
echo ""
echo "   pxe-net --help | bmc-setup --help | pxe-backup --help"
echo "═══════════════════════════════════════════════"
