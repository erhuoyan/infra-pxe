#!/bin/bash
# JoyOps Infra PXE Engine — 卸载脚本
# Usage: uninstall.sh [-d DIR] [-y]

set -e

INSTALL_DIR="/joyops/infra/pxe"
FORCE=false

while [[ $# -gt 0 ]]; do
    case $1 in
        -d|--dir) INSTALL_DIR="$2"; shift 2 ;;
        -y|--yes) FORCE=true; shift ;;
        -h|--help)
            echo "Usage: $0 [-d DIR] [-y]"
            echo "  -d, --dir DIR  安装目录 (default: /joyops/infra/pxe)"
            echo "  -y, --yes      跳过确认"
            exit 0 ;;
        *) echo "Unknown option: $1"; exit 1 ;;
    esac
done

if [ ! -d "$INSTALL_DIR" ]; then
    echo "PXE Engine 目录不存在: $INSTALL_DIR"
    exit 0
fi

if [ "$FORCE" != "true" ]; then
    echo "将卸载 PXE Engine: $INSTALL_DIR"
    echo "此操作会删除所有数据（DB、ISO、配置）"
    read -p "确认? [y/N] " confirm
    if [[ "$confirm" != "y" && "$confirm" != "Y" ]]; then
        echo "取消"
        exit 0
    fi
fi

echo ""
echo "── 停止服务..."
systemctl stop infra-pxe 2>/dev/null || true
systemctl disable infra-pxe 2>/dev/null || true
rm -f /etc/systemd/system/infra-pxe.service
systemctl daemon-reload 2>/dev/null || true

echo "── 卸载已挂载的 ISO..."
# 查找所有在 PXE 目录下的挂载点并 umount
grep "$INSTALL_DIR" /proc/mounts 2>/dev/null | awk '{print $2}' | sort -r | while read -r mp; do
    echo "  umount $mp"
    umount "$mp" 2>/dev/null || umount -l "$mp" 2>/dev/null || true
done

echo "── 删除文件..."
rm -rf "$INSTALL_DIR"

echo ""
echo "✅ PXE Engine 已卸载"
