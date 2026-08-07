#!/bin/bash
# arp_dual_send.sh - 去堆叠双上联环境下，从 bond 所有成员接口发送免费 ARP
# 通过 systemd 管理: systemctl enable --now arp_dual_send.service
set -euo pipefail

# 配置参数（支持环境变量）
BOND_IFS=${BOND_IFS:-"bond0"}
SLEEP_INTERVAL=${SLEEP_INTERVAL:-5}
ARP_COUNT=${ARP_COUNT:-1}
ARP_TIMEOUT=${ARP_TIMEOUT:-3}

log_info() { echo "[$(date '+%Y-%m-%d %H:%M:%S')] INFO: $*"; }
log_warn() { echo "[$(date '+%Y-%m-%d %H:%M:%S')] WARN: $*" >&2; }
log_error() { echo "[$(date '+%Y-%m-%d %H:%M:%S')] ERROR: $*" >&2; }

cleanup() { log_info "脚本正在退出..."; exit 0; }
trap cleanup SIGINT SIGTERM

# 检查依赖
for dep in ip arping; do
    command -v "$dep" &>/dev/null || { log_error "依赖命令 $dep 未找到"; exit 1; }
done

# 获取接口上所有 global IPv4 地址
get_ipv4_addrs() {
    ip -4 addr show "$1" | awk '/inet / && !/scope link/ {print $2}' | cut -d'/' -f1
}

# 获取 bond 的所有成员接口
get_bond_slaves() {
    if [[ -f "/sys/class/net/$1/bonding/slaves" ]]; then
        cat "/sys/class/net/$1/bonding/slaves"
    else
        return 1
    fi
}

# 从指定接口发送免费 ARP
send_gratuitous_arp() {
    local ip=$1 iface=$2
    if arping -c "$ARP_COUNT" -w "$ARP_TIMEOUT" -I "$iface" -A "$ip" &>/dev/null; then
        log_info "成功从 $iface 发送免费ARP $ip"
    else
        log_warn "$iface 发送免费ARP $ip 失败"
    fi
}

main() {
    log_info "开始执行ARP双发脚本 (bond: $BOND_IFS, interval: ${SLEEP_INTERVAL}s)"

    while true; do
        for BOND_IF in $BOND_IFS; do
            SLAVES=$(get_bond_slaves "$BOND_IF") || continue
            IP_LIST=$(get_ipv4_addrs "$BOND_IF")
            [[ -z "$IP_LIST" ]] && continue

            while IFS= read -r ip; do
                [[ -z "$ip" ]] && continue
                for IFACE in $SLAVES; do
                    send_gratuitous_arp "$ip" "$IFACE"
                done
            done <<< "$IP_LIST"
        done
        sleep "$SLEEP_INTERVAL"
    done
}

main "$@"
