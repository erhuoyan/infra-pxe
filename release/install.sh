#!/bin/bash
# install-pxe.sh — 一键部署 PXE Engine 到当前机器
# 
# 用法:
#   ./install.sh                          # 自动检测网口，全部默认
#   ./install.sh -i eth1                  # 指定数据网口
#   ./install.sh -i eth1 -p 9200          # 指定网口和端口
#   ./install.sh --webhook http://cmdb:8080/hook  # 配置 Webhook URL（可选）
#
# 也可以直接从 tar.gz 运行:
#   tar xzf infra-pxe-*.tar.gz -C /tmp && /tmp/infra-pxe/install.sh
#
set -e

# ══════════════════════════════════════════════════════════
# 参数解析
# ══════════════════════════════════════════════════════════
INSTALL_DIR="/joyops/infra/pxe"
INTERFACE=""
PORT=9200
WEBHOOK_URL=""
TOKEN="agent-token-changeme"
PXE_NAME=""

usage() {
    echo "Usage: $0 [OPTIONS]"
    echo ""
    echo "Options:"
    echo "  -d, --dir DIR          安装目录 (default: /joyops/infra/pxe)"
    echo "  -i, --interface IFACE  PXE 网口 (可选，部署后可通过 API/MCP 配置)"
    echo "  -p, --port PORT        监听端口 (default: 9200)"
    echo "  -n, --name NAME        PXE Engine 名称 (default: hostname)"
    echo "  -c, --webhook URL     Webhook URL (可选，装机事件转发到外部系统)"
    echo "  -t, --token TOKEN      Agent token (default: agent-token-changeme)"
    echo "  -h, --help             显示帮助"
    exit 0
}

while [[ $# -gt 0 ]]; do
    case $1 in
        -d|--dir) INSTALL_DIR="$2"; shift 2 ;;
        -i|--interface) INTERFACE="$2"; shift 2 ;;
        -p|--port) PORT="$2"; shift 2 ;;
        -n|--name) PXE_NAME="$2"; shift 2 ;;
        -c|--webhook) WEBHOOK_URL="$2"; shift 2 ;;
        -t|--token) TOKEN="$2"; shift 2 ;;
        -h|--help) usage ;;
        *) echo "Unknown option: $1"; usage ;;
    esac
done

# ══════════════════════════════════════════════════════════
# 自动检测
# ══════════════════════════════════════════════════════════

# 包的位置（install.sh 所在目录）
PKG_DIR="$(cd "$(dirname "$0")" && pwd)"

# 检测网口（不再自动猜测，留空则需要部署后通过 API 配置）
# 用户可通过 -i 指定，或部署后 PUT /api/dhcp/config 设置

# PXE Engine 名称默认 hostname
if [ -z "$PXE_NAME" ]; then
    PXE_NAME=$(hostname -s 2>/dev/null || echo "pxe-1")
fi

echo ""
echo "╔══════════════════════════════════════════╗"
echo "║   JoyOps Infra PXE — 一键安装         ║"
echo "╠══════════════════════════════════════════╣"
echo "║  安装目录: $INSTALL_DIR"
echo "║  数据网口: ${INTERFACE:-(部署后通过 API 配置)}"
echo "║  监听端口: $PORT"
echo "║  名称:     $PXE_NAME"
if [ -n "$WEBHOOK_URL" ]; then
echo "║  Webhook:    $WEBHOOK_URL"
else
echo "║  Webhook:    (独立运行，不转发事件)"
fi
echo "╚══════════════════════════════════════════╝"
echo ""

# ══════════════════════════════════════════════════════════
# Step 1: 安装文件
# ══════════════════════════════════════════════════════════
echo "[1/4] 安装文件到 $INSTALL_DIR ..."

mkdir -p "$INSTALL_DIR"/{bin,conf,data/dnsmasq,templates,seeds,boot/{tftp,http,iso},scripts,logs}

# 拷贝 binary
cp "$PKG_DIR/bin/infra-pxe" "$INSTALL_DIR/bin/infra-pxe"
chmod +x "$INSTALL_DIR/bin/infra-pxe"

# 拷贝 iPXE 固件
[ -d "$PKG_DIR/boot/tftp" ] && cp -a "$PKG_DIR/boot/tftp/"* "$INSTALL_DIR/boot/tftp/" 2>/dev/null || true

# 拷贝 DHCP event 脚本
[ -f "$PKG_DIR/scripts/dhcp-event.sh" ] && cp "$PKG_DIR/scripts/dhcp-event.sh" "$INSTALL_DIR/scripts/" && chmod +x "$INSTALL_DIR/scripts/dhcp-event.sh"

# 拷贝 templates (j2 + scripts)
[ -d "$PKG_DIR/templates" ] && cp -a "$PKG_DIR/templates/"* "$INSTALL_DIR/templates/" 2>/dev/null || true

# 拷贝 seed yaml
[ -d "$PKG_DIR/seeds" ] && cp -a "$PKG_DIR/seeds/"* "$INSTALL_DIR/seeds/" 2>/dev/null || true

# 拷贝 boot HTTP static files
[ -d "$PKG_DIR/boot/http" ] && { mkdir -p "$INSTALL_DIR/boot/http"; cp -a "$PKG_DIR/boot/http/"* "$INSTALL_DIR/boot/http/" 2>/dev/null || true; }

echo "  ✓ 文件安装完成"

# ══════════════════════════════════════════════════════════
# Step 2: 生成配置（不覆盖已有）
# ══════════════════════════════════════════════════════════
echo "[2/4] 生成配置..."

CONF_FILE="$INSTALL_DIR/conf/pxe.yaml"
if [ -f "$CONF_FILE" ]; then
    echo "  → 配置已存在，跳过（保留现有配置）"
    echo "  → 如需重新生成，删除后重跑: rm $CONF_FILE"
else
    cat > "$CONF_FILE" << EOF
worker:
  listen: "0.0.0.0"
  port: $PORT
  interface: "$INTERFACE"
  name: "$PXE_NAME"

dnsmasq:
  binary: "/usr/sbin/dnsmasq"
  conf_dir: "$INSTALL_DIR/data/dnsmasq"
  pid_file: "$INSTALL_DIR/data/dnsmasq/dnsmasq.pid"
  lease_time: "5m"
  dhcp_script: "$INSTALL_DIR/scripts/dhcp-event.sh"

data:
  dir: "$INSTALL_DIR/data"

paths:
  boot_dir: "$INSTALL_DIR/boot"
  templates_dir: "$INSTALL_DIR/templates"
EOF

    # 如果指定了 Webhook，追加配置
    if [ -n "$WEBHOOK_URL" ]; then
        cat >> "$CONF_FILE" << EOF

webhook:
  url: "$WEBHOOK_URL"
  token: "$TOKEN"
EOF
    fi

    echo "  ✓ 配置已生成: $CONF_FILE"
fi

# ══════════════════════════════════════════════════════════
# Step 3: 安装 dnsmasq
# ══════════════════════════════════════════════════════════
echo "[3/4] 检查 dnsmasq..."

if command -v dnsmasq >/dev/null 2>&1; then
    echo "  ✓ dnsmasq 已安装: $(dnsmasq --version 2>&1 | head -1)"
elif command -v apt-get >/dev/null 2>&1; then
    echo "  → 安装 dnsmasq (apt)..."
    apt-get install -y dnsmasq >/dev/null 2>&1
    systemctl stop dnsmasq 2>/dev/null || true
    systemctl disable dnsmasq 2>/dev/null || true
    echo "  ✓ dnsmasq 已安装（系统服务已禁用，由 PXE Engine 管理）"
elif command -v yum >/dev/null 2>&1; then
    echo "  → 安装 dnsmasq (yum)..."
    yum install -y dnsmasq >/dev/null 2>&1
    systemctl stop dnsmasq 2>/dev/null || true
    systemctl disable dnsmasq 2>/dev/null || true
    echo "  ✓ dnsmasq 已安装（系统服务已禁用，由 PXE Engine 管理）"
elif command -v dnf >/dev/null 2>&1; then
    echo "  → 安装 dnsmasq (dnf)..."
    dnf install -y dnsmasq >/dev/null 2>&1
    systemctl stop dnsmasq 2>/dev/null || true
    systemctl disable dnsmasq 2>/dev/null || true
    echo "  ✓ dnsmasq 已安装（系统服务已禁用，由 PXE Engine 管理）"
else
    echo "  ⚠ 未检测到包管理器，请手动安装 dnsmasq"
fi

# ══════════════════════════════════════════════════════════
# Step 4: 安装 systemd 服务并启动
# ══════════════════════════════════════════════════════════
echo "[4/4] 安装 systemd 服务..."

cat > /etc/systemd/system/infra-pxe.service << EOF
[Unit]
Description=JoyOps Infra PXE Engine (PXE Engine)
After=network.target

[Service]
Type=simple
WorkingDirectory=$INSTALL_DIR
ExecStart=$INSTALL_DIR/bin/infra-pxe -config $INSTALL_DIR/conf/pxe.yaml
Restart=on-failure
RestartSec=5
LimitNOFILE=65535

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable infra-pxe

# 重启（如果已在运行则 restart，否则 start）
if systemctl is-active infra-pxe >/dev/null 2>&1; then
    systemctl restart infra-pxe
    echo "  ✓ 服务已重启"
else
    systemctl start infra-pxe
    echo "  ✓ 服务已启动"
fi

sleep 1

# ══════════════════════════════════════════════════════════
# 验证
# ══════════════════════════════════════════════════════════
echo ""
if systemctl is-active infra-pxe >/dev/null 2>&1; then
    LOCAL_IP=$(ip -4 route get 1.0.0.0 2>/dev/null | awk '/src/{print $7; exit}')
    echo "╔═══════════════════════════════════════════════════════════╗"
    echo "║  ✅ PXE Engine 安装成功!                                      ║"
    echo "╠═══════════════════════════════════════════════════════════╣"
    echo "║"
    echo "║  状态: $(systemctl is-active infra-pxe)"
    echo "║  API:  http://${LOCAL_IP:-127.0.0.1}:$PORT"
    echo "║  MCP:  http://${LOCAL_IP:-127.0.0.1}:$PORT/mcp"
    echo "║  配置: $INSTALL_DIR/conf/pxe.yaml"
    echo "║  日志: journalctl -u infra-pxe -f"
    echo "║"
    echo "║  快速验证:"
    echo "║    curl http://127.0.0.1:$PORT/api/health"
    echo "║"
    echo "║  ┌─ AI Agent (推荐) ──────────────────────────────────┐"
    echo "║  │  重启 Agent session 后自动连接 MCP，然后:          │"
    echo "║  │    1. seed_import          — 导入 OS 模板          │"
    echo "║  │    2. list_interfaces      — 查看 PXE 网口         │"
    echo "║  │    3. dhcp_config_update   — 配置 DHCP 网段        │"
    echo "║  │    4. validate_os_template — 校验物料就绪           │"
    echo "║  └────────────────────────────────────────────────────┘"
    echo "║"
    echo "║  ┌─ 手动 API ────────────────────────────────────────┐"
    echo "║  │  curl -X POST http://127.0.0.1:$PORT/api/seed/import     │"
    echo "║  │  curl -X PUT  http://127.0.0.1:$PORT/api/dhcp/config ... │"
    echo "║  └────────────────────────────────────────────────────┘"
    echo "║"
    echo "╚═══════════════════════════════════════════════════════════╝"
else
    echo "❌ PXE Engine 启动失败"
    echo ""
    echo "查看日志:"
    echo "  journalctl -u infra-pxe -n 30 --no-pager"
    exit 1
fi
