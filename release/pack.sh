#!/usr/bin/env bash
# pack.sh — 编译打包 PXE Engine
# 产出: release/dist/infra-pxe-<ver>-linux-{amd64,arm64}.tar.gz
#
# 包内结构:
#   infra-pxe/
#   ├── bin/infra-pxe
#   ├── conf/pxe.yaml.example
#   ├── boot/tftp/          (iPXE 固件)
#   ├── templates/          (kickstart/cloud-init 模板)
#   ├── seeds/              (OS 模板种子数据)
#   ├── scripts/            (dhcp-event hook)
#   ├── data/               (空目录，运行时生成 pxe.db)
#   ├── infra-pxe.service
#   └── install.sh          (一键部署脚本)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
VERSION="${1:-$(date +%Y%m%d)}"
DIST="$ROOT/release/dist"

for PLATFORM in linux-amd64 linux-arm64; do
    echo "── Building PXE Engine ($PLATFORM)..."
    (cd "$ROOT" && CGO_ENABLED=0 GOOS=linux GOARCH=${PLATFORM#linux-} go build -ldflags="-s -w" -o "$ROOT/release/bin/infra-pxe-$PLATFORM" .)

    STAGE=$(mktemp -d)
    DEST="$STAGE/infra-pxe"
    mkdir -p "$DEST"/{bin,conf,boot/tftp,data,data/dnsmasq,templates,seeds,scripts,logs}

    # Binary
    cp "$ROOT/release/bin/infra-pxe-$PLATFORM" "$DEST/bin/infra-pxe"
    chmod +x "$DEST/bin/infra-pxe"

    # Config example
    cp "$ROOT/conf/pxe.yaml.example" "$DEST/conf/pxe.yaml.example"

    # PXE templates (kickstart/cloud-init + scripts)
    if [ -d "$ROOT/templates" ]; then
        for f in "$ROOT/templates"/*.j2; do
            [ -f "$f" ] || continue
            case "$(basename "$f")" in
                menu.ipxe.j2) continue ;;
            esac
            cp "$f" "$DEST/templates/"
        done
        if [ -d "$ROOT/templates/scripts" ]; then
            mkdir -p "$DEST/templates/scripts"
            for f in "$ROOT/templates"/scripts/*.sh "$ROOT/templates"/scripts/*.py; do
                [ -f "$f" ] && cp "$f" "$DEST/templates/scripts/"
            done
        fi
    fi

    # Seed yaml — imported via POST /api/seed/import
    if [ -d "$ROOT/seeds" ]; then
        for f in iso_sources.yaml os_templates.yaml scripts.yaml files.yaml bmc_drivers.yaml; do
            [ -f "$ROOT/seeds/$f" ] && cp "$ROOT/seeds/$f" "$DEST/seeds/"
        done
    fi

    # iPXE firmware (pre-built)
    cp -a "$ROOT/boot/tftp/efi64" "$DEST/boot/tftp/"
    cp -a "$ROOT/boot/tftp/efiarm" "$DEST/boot/tftp/"
    cp -a "$ROOT/boot/tftp/bios" "$DEST/boot/tftp/"

    # autoexec.ipxe: iPXE EFI always fetches this at boot (efi_autoexec.c),
    # relative to the loaded firmware's directory (cwuri). MUST be non-empty
    # (zero-length files cause dnsmasq TFTP to stall).
    for d in . efiarm efi64 bios; do
        mkdir -p "$DEST/boot/tftp/$d"
        printf '# placeholder autoexec — menu is served via DHCP user-class\n' > "$DEST/boot/tftp/$d/autoexec.ipxe"
    done

    # Static HTTP files (boot background, etc.)
    if [ -d "$ROOT/templates/static" ]; then
        mkdir -p "$DEST/boot/http/template"
        cp -a "$ROOT/templates/static/"* "$DEST/boot/http/template/"
    fi

    # DHCP event script
    [ -f "$ROOT/scripts/dhcp-event.sh" ] && cp "$ROOT/scripts/dhcp-event.sh" "$DEST/scripts/"

    # systemd service
    cat > "$DEST/infra-pxe.service" << 'UNIT'
[Unit]
Description=JoyOps Infra PXE Engine
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
WorkingDirectory=/joyops/infra/pxe
ExecStart=/joyops/infra/pxe/bin/infra-pxe --config conf/pxe.yaml
Restart=on-failure
RestartSec=5
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
UNIT

    # Install / uninstall scripts
    cp "$ROOT/release/install.sh" "$DEST/install.sh"
    chmod +x "$DEST/install.sh"
    cp "$ROOT/release/uninstall.sh" "$DEST/uninstall.sh"
    chmod +x "$DEST/uninstall.sh"

    # Pack
    mkdir -p "$DIST"
    tar czf "$DIST/infra-pxe-${VERSION}-${PLATFORM}.tar.gz" --no-xattrs --no-mac-metadata -C "$STAGE" infra-pxe 2>/dev/null || \
    tar czf "$DIST/infra-pxe-${VERSION}-${PLATFORM}.tar.gz" -C "$STAGE" infra-pxe
    rm -rf "$STAGE"
    echo "✓ $DIST/infra-pxe-${VERSION}-${PLATFORM}.tar.gz"
done

echo ""
echo "Done. Deploy with:"
echo "  scp release/dist/infra-pxe-*-linux-arm64.tar.gz root@target:/tmp/"
echo "  ssh root@target 'tar xzf /tmp/infra-pxe-*.tar.gz -C /tmp && /tmp/infra-pxe/install.sh'"
