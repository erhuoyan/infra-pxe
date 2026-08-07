#!/bin/sh -e
# genapkovl-pxe.sh — 生成 infra-pxe apkovl overlay
#
# mkimage.sh 自动调用: genapkovl-pxe.sh <hostname>
# 产出: <hostname>.apkovl.tar.gz

HOSTNAME="$1"
[ -z "$HOSTNAME" ] && HOSTNAME="pxe-engine"

tmp="$(mktemp -d)"
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT

makefile() {
    local _mode="$1"
    local _owner="$2"
    local _file="$3"
    mkdir -p "$(dirname "$_file")"
    { cat; } > "$_file"
    chown "$_owner" "$_file" 2>/dev/null || true
    chmod "$_mode" "$_file"
}
rc_add() {
    mkdir -p "$tmp"/etc/runlevels/"$2"
    ln -sf /etc/init.d/"$1" "$tmp"/etc/runlevels/"$2"/"$1" 2>/dev/null || true
}

# ══════════════════════════════════════════════════
# 系统配置
# ══════════════════════════════════════════════════

makefile 0644 "root:root" "$tmp"/etc/hostname <<EOF
$HOSTNAME
EOF

# apk/world — 启动时自动安装
mkdir -p "$tmp"/etc/apk
makefile 0644 "root:root" "$tmp"/etc/apk/world <<EOF
alpine-base
dnsmasq
ipmitool
sqlite
bash
jq
curl
iproute2
bonding
util-linux
openssh
EOF

# root 免密
makefile 0640 "root:root" "$tmp"/etc/shadow <<EOF
root:::0:99999:7:::
bin:!::0:::::
daemon:!::0:::::
nobody:!::0:::::
EOF

# inittab
makefile 0644 "root:root" "$tmp"/etc/inittab <<EOF
::sysinit:/sbin/openrc sysinit
::sysinit:/sbin/openrc boot
::wait:/sbin/openrc default
tty1::respawn:/bin/sh -l
ttyS0::respawn:/bin/sh -l
::ctrlaltdel:/sbin/reboot
::shutdown:/sbin/openrc shutdown
EOF

# sshd
makefile 0644 "root:root" "$tmp"/etc/ssh/sshd_config <<EOF
PermitRootLogin yes
PasswordAuthentication yes
UseDNS no
EOF

# 网络最小配置，pxe-net 覆盖
makefile 0644 "root:root" "$tmp"/etc/network/interfaces <<EOF
auto lo
iface lo inet loopback
EOF

# MOTD
makefile 0644 "root:root" "$tmp"/etc/motd <<'MOTD'
 ┌───────────────────────────────────────────────────┐
 │       Infra PXE Engine                           │
 ├───────────────────────────────────────────────────┤
 │                                                   │
 │  First boot: pxe-up eth0 10.0.1.100/24 10.0.1.1  │
 │                                                   │
 │  Daily:                                          │
 │    pxe-net eth0 10.0.1.100/24 10.0.1.1            │
 │    pxe-net bond0 10.0.1.100/24 10.0.1.1 eth0,eth1 │
 │    bmc-setup 10.0.1.50 -p pass ip 10.0.1.60/24   │
 │    pxe-backup                                     │
 └───────────────────────────────────────────────────┘
MOTD

# ══════════════════════════════════════════════════
# 开机自动激活所有网口
# ══════════════════════════════════════════════════
mkdir -p "$tmp"/etc/local.d
makefile 0755 "root:root" "$tmp"/etc/local.d/link-up.start <<'LINKUP'
#!/bin/sh
ip link set lo up 2>/dev/null
for iface in /sys/class/net/eth* /sys/class/net/en*; do
    [ -e "$iface" ] || continue
    name=$(basename "$iface")
    ip link set "$name" up 2>/dev/null
done
LINKUP

# ══════════════════════════════════════════════════
# infra-pxe openrc 服务
# ══════════════════════════════════════════════════
makefile 0755 "root:root" "$tmp"/etc/init.d/infra-pxe <<'INITD'
#!/sbin/openrc-run
name="infra-pxe"
description="Infra PXE Engine"

command="/srv/infra/pxe/bin/infra-pxe"
command_args="--config /srv/infra/pxe/conf/pxe.yaml"
command_background="yes"
pidfile="/run/infra-pxe.pid"
directory="/srv/infra/pxe"

output_log="/srv/infra/pxe/logs/pxe.log"
error_log="/srv/infra/pxe/logs/pxe.log"

depend() { need net; after sshd; }

start_pre() {
    mkdir -p /srv/infra/pxe/data/dnsmasq /srv/infra/pxe/logs /srv/infra/pxe/boot/tftp /srv/infra/pxe/boot/http /srv/infra/pxe/boot/iso /srv/infra/pxe/backups
    if [ ! -x "$command" ]; then
        ebegin "Extracting infra-pxe payload"
        CDROM=""
        for d in /media/* /mnt/cdrom /mnt; do
            [ -f "$d/infra-pxe.tar.gz" ] && { CDROM="$d"; break; }
        done
        if [ -z "$CDROM" ]; then
            mkdir -p /mnt/cdrom
            mount /dev/sr0 /mnt/cdrom 2>/dev/null && [ -f /mnt/cdrom/infra-pxe.tar.gz ] && CDROM=/mnt/cdrom
        fi
        [ -z "$CDROM" ] && { eend 1 "infra-pxe.tar.gz not found"; return 1; }
        tar xzf "$CDROM/infra-pxe.tar.gz" -C /
        chmod +x "$command" 2>/dev/null || true
        [ -x "$command" ] || { eend 1 "infra-pxe binary missing after extract"; return 1; }
        eend 0
    fi
}
INITD

# ══════════════════════════════════════════════════
# 用户脚本
# ══════════════════════════════════════════════════
mkdir -p "$tmp"/usr/local/bin

# ── pxe-up ──
makefile 0755 "root:root" "$tmp"/usr/local/bin/pxe-up <<'PXEUP'
#!/bin/sh
# pxe-up — First boot: extract infra-pxe + configure network + start service
set -e
usage() {
    echo "Usage: pxe-up <IFACE> <IP/CIDR> <GATEWAY> [SLAVES|DNS]"
    echo "       pxe-up <IFACE> dhcp"
    echo "  pxe-up eth0 10.0.1.100/24 10.0.1.1"
    echo "  pxe-up bond0 10.0.1.100/24 10.0.1.1 eth0,eth1"
    echo "  pxe-up eth0 dhcp"
    exit 0
}
[ "${1:-}" = "--help" ] || [ "${1:-}" = "-h" ] && usage
[ $# -lt 2 ] && usage
IFACE="$1"; shift
PXE_DIR="/srv/infra/pxe"

echo "═══════════════════════════════════════════════"
echo " Infra PXE Engine — First Boot"
echo "═══════════════════════════════════════════════"

if [ ! -f "$PXE_DIR/bin/infra-pxe" ]; then
    echo "[1/3] Extracting PXE Engine..."
    CDROM=""; for d in /media/* /mnt/cdrom /mnt; do [ -f "$d/infra-pxe.tar.gz" ] && { CDROM="$d"; break; }; done
    [ -z "$CDROM" ] && { mkdir -p /mnt/cdrom; mount /dev/sr0 /mnt/cdrom 2>/dev/null && [ -f /mnt/cdrom/infra-pxe.tar.gz ] && CDROM=/mnt/cdrom; }
    [ -z "$CDROM" ] && { echo "ERROR: infra-pxe.tar.gz not found"; exit 1; }
    tar xzf "$CDROM/infra-pxe.tar.gz" -C /
    echo "       ✓"
else
    echo "[1/3] Already extracted"
fi

echo "[2/3] Configuring network..."
if [ "${1:-}" = "dhcp" ]; then
    pxe-net dhcp "$IFACE"
else
    pxe-net "$IFACE" "$@"
fi

echo "[3/3] Starting PXE Engine..."
rc-service infra-pxe restart 2>/dev/null || { cd "$PXE_DIR"; ./bin/infra-pxe --config conf/pxe.yaml >> logs/pxe.log 2>&1 & sleep 2; }
sleep 1
curl -sf http://127.0.0.1:9200/api/health >/dev/null 2>&1 && echo "       ✓" || echo "       ⚠ check: $PXE_DIR/logs/pxe.log"

IP=$(ip -4 addr show scope global 2>/dev/null | head -1 | awk '{print $2}' | cut -d/ -f1)
echo ""
echo "═══════════════════════════════════════════════"
echo " ✓ Ready"
echo "   API: http://${IP}:9200"
echo "   pxe-net --help | bmc-setup --help | pxe-backup --help"
echo "═══════════════════════════════════════════════"
PXEUP

# ── pxe-net ──
makefile 0755 "root:root" "$tmp"/usr/local/bin/pxe-net <<'PXENET'
#!/bin/sh
# pxe-net — Configure PXE engine OS network
usage() {
    echo "Usage: pxe-net <IFACE> <IP/CIDR> <GATEWAY> [DNS]"
    echo "       pxe-net bond0  <IP/CIDR> <GATEWAY>  <SLAVES> [DNS]"
    echo "       pxe-net <IFACE> dhcp"; echo "       pxe-net status"
    echo "  pxe-net eth0 10.0.1.100/24 10.0.1.1"
    echo "  pxe-net bond0 10.0.1.100/24 10.0.1.1 eth0,eth1"
    exit 0
}
[ $# -eq 0 ] && { echo "Run 'pxe-net --help'"; exit 1; }
[ "${1:-}" = "--help" ] || [ "${1:-}" = "-h" ] && usage
if [ "${2:-}" = "dhcp" ]; then
    set -- dhcp "$1"
fi

PXE_DIR="/srv/infra/pxe"; DNS_DEFAULT="223.5.5.5"

show_status() {
    echo "═══ Network ═══"; ip -4 addr show | grep -E "^[0-9]+:|inet " | sed 's/^/  /'
    echo "   Route:"; ip route show default 2>/dev/null | sed 's/^/     /' || echo "     (none)"
    echo ""; echo "═══ PXE Engine ═══"
    if pgrep -f "infra-pxe" >/dev/null 2>&1; then
        IP=$(ip -4 addr show | grep 'scope global' | head -1 | awk '{print $2}' | cut -d/ -f1)
        [ -n "$IP" ] || IP="127.0.0.1"
        echo "   running  http://${IP}:9200"; curl -sf http://127.0.0.1:9200/api/health 2>/dev/null | head -1
    else echo "   stopped  rc-service infra-pxe start"; fi
    echo ""
}
restart_pxe() {
    rc-service infra-pxe restart 2>/dev/null && { sleep 1; echo "   ✓ restarted"; return; }
    pkill -f "infra-pxe" 2>/dev/null || true; sleep 1
    cd "$PXE_DIR"; ./bin/infra-pxe --config conf/pxe.yaml >> logs/pxe.log 2>&1 & sleep 1
    pgrep -f "infra-pxe" >/dev/null && echo "   ✓ restarted" || echo "   ⚠ failed"
}
case "${1:-}" in
    status) show_status; exit 0 ;;
    dhcp) IFACE="${2:-eth0}"; echo "Switching $IFACE to DHCP..."
          ip addr flush dev "$IFACE" 2>/dev/null || true
          printf 'auto lo\niface lo inet loopback\n\nauto %s\niface %s inet dhcp\n' "$IFACE" "$IFACE" > /etc/network/interfaces
          ifup "$IFACE" 2>/dev/null || udhcpc -i "$IFACE"; sleep 2
          IP=$(ip -4 addr show "$IFACE" 2>/dev/null | grep 'inet ' | awk '{print $2}' | cut -d/ -f1)
          echo "   ✓ $IFACE: ${IP:-pending}"; restart_pxe; exit 0 ;;

esac
IFACE="$1"; IPCIDR="$2"; GATEWAY="${3:-}"; EXTRA="${4:-}"
case "$IFACE" in
    bond*) SLAVES="${EXTRA:-}"; [ -z "$SLAVES" ] && { echo "ERROR: bond requires slaves (e.g. eth0,eth1)"; usage; }; DNS="${5:-$DNS_DEFAULT}" ;;
    *)     SLAVES=""; DNS="${EXTRA:-$DNS_DEFAULT}"; [ -n "$EXTRA" ] && echo "$EXTRA" | grep -q ',' && { echo "ERROR: use bond0 for bonding"; usage; } ;;
esac
echo "$IPCIDR" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+/[0-9]+$' || { echo "ERROR: invalid IP/CIDR"; usage; }
MY_IP=$(echo "$IPCIDR" | cut -d/ -f1)
echo "═══ Configuring ═══"; echo "   $IFACE → $IPCIDR  gw $GATEWAY"; [ -n "$SLAVES" ] && echo "   Bond: 802.3ad slaves=$SLAVES"; echo ""
ip addr flush dev "$IFACE" 2>/dev/null || true; ip link set "$IFACE" down 2>/dev/null || true
if [ -n "$SLAVES" ]; then
    modprobe bonding 2>/dev/null || true
    for s in $(echo "$SLAVES" | tr ',' ' '); do ip link set "$s" down 2>/dev/null || true; ip addr flush dev "$s" 2>/dev/null || true; done
    ip link delete "$IFACE" 2>/dev/null || true
    ip link add "$IFACE" type bond mode 802.3ad miimon 100 lacp_rate fast
    for s in $(echo "$SLAVES" | tr ',' ' '); do ip link set "$s" master "$IFACE"; ip link set "$s" up; done
    ip addr add "$IPCIDR" dev "$IFACE"; ip link set "$IFACE" up
    { echo "auto lo"; echo "iface lo inet loopback"; echo ""; } > /etc/network/interfaces
    for s in $(echo "$SLAVES" | tr ',' ' '); do echo "auto $s" >> /etc/network/interfaces; echo "iface $s inet manual" >> /etc/network/interfaces; echo "" >> /etc/network/interfaces; done
    cat >> /etc/network/interfaces << EOF
auto $IFACE
iface $IFACE inet static
    address $IPCIDR; gateway $GATEWAY
    bond-mode 802.3ad; bond-miimon 100; bond-lacp-rate fast; bond-slaves $SLAVES
EOF
else
    ip addr add "$IPCIDR" dev "$IFACE"; ip link set "$IFACE" up
    cat > /etc/network/interfaces << EOF
auto lo
iface lo inet loopback

auto $IFACE
iface $IFACE inet static
    address $IPCIDR; gateway $GATEWAY
EOF
fi
[ -n "$GATEWAY" ] && ip route replace default via "$GATEWAY" 2>/dev/null || true
echo "nameserver $DNS" > /etc/resolv.conf
echo "   ✓ $IFACE up: $MY_IP"; [ -n "$GATEWAY" ] && ping -c 1 -W 2 "$GATEWAY" >/dev/null 2>&1 && echo "   ✓ Gateway reachable"
echo ""; restart_pxe; echo "═══ Done ═══"; echo "   API: http://${MY_IP}:9200"
PXENET

# ── bmc-setup ──
makefile 0755 "root:root" "$tmp"/usr/local/bin/bmc-setup <<'BMCSETUP'
#!/bin/sh
# bmc-setup — Configure server BMC via ipmitool
[ $# -eq 0 ] && { echo "Run 'bmc-setup --help'"; exit 1; }
[ "${1:-}" = "--help" ] || [ "${1:-}" = "-h" ] && {
    echo "Usage: bmc-setup <BMC_IP> -u <USER> -p <PASS> <ACTION>"; echo ""
    echo "  ip <IP>/<CIDR> <GW>  user <NAME> <PASS>  boot <pxe|disk>"
    echo "  dhcp  info  power on|off|reset|status  reset"; echo ""
    echo "  bmc-setup 10.0.1.50 -p pass ip 10.0.1.60/24 10.0.1.1"
    echo "  bmc-setup 10.0.1.50 -p pass boot pxe"; exit 0; }
BMC_IP="$1"; shift; BMC_USER="admin"; BMC_PASS=""; BMC_CHAN="1"
while [ $# -gt 0 ]; do case "$1" in -u) BMC_USER="$2"; shift 2;; -p) BMC_PASS="$2"; shift 2;; -c) BMC_CHAN="$2"; shift 2;; *) break;; esac; done
IPMI="ipmitool -I lanplus -H $BMC_IP -U $BMC_USER"; [ -n "$BMC_PASS" ] && IPMI="$IPMI -P $BMC_PASS"
ACTION="${1:-info}"; [ $# -gt 0 ] && shift
die() { echo "ERROR: $*" >&2; exit 1; }
check() { $IPMI mc info >/dev/null 2>&1 || die "cannot reach BMC"; }
c2m() { c=$1; M=""; o=$((c/8)); p=$((256-(1<<(8-(c%8))))); for i in 1 2 3 4; do if [ $i -le $o ]; then M="${M}255"; elif [ $i -eq $((o+1)) ]; then M="${M}${p}"; else M="${M}0"; fi; [ $i -lt 4 ] && M="${M}."; done; echo "$M"; }
case "$ACTION" in
    ip) [ $# -lt 2 ] && die "usage: ip <IP>/<CIDR> <GW>"; I="$1"; G="$2"; M=$(c2m $(echo "$I" | cut -d/ -f2))
        check; $IPMI lan set "$BMC_CHAN" ipsrc static; $IPMI lan set "$BMC_CHAN" ipaddr "$(echo "$I" | cut -d/ -f1)"
        $IPMI lan set "$BMC_CHAN" netmask "$M"; $IPMI lan set "$BMC_CHAN" defgw ipaddr "$G"; echo "   ✓";;
    dhcp) check; $IPMI lan set "$BMC_CHAN" ipsrc dhcp; echo "   ✓";;
    info) check; $IPMI lan print "$BMC_CHAN";;
    user) [ $# -lt 2 ] && die "usage: user <NAME> <PASS>"; check
        $IPMI user set name 2 "$1" 2>/dev/null || true; $IPMI user set password 2 "$2" 2>/dev/null || true
        $IPMI user enable 2 2>/dev/null || true; $IPMI channel setaccess "$BMC_CHAN" 2 callin=on ipmi=on link=on privilege=4
        echo "   ✓ $1 (slot 2)";;
    boot) D="${1:-pxe}"; check; $IPMI chassis bootdev "$D" options=persistent; echo "   ✓ $D";;
    power) C="${1:-status}"; check; $IPMI chassis power "$C";;
    reset) check; $IPMI mc reset cold; echo "   ✓ sent";;
esac
BMCSETUP

# ── pxe-backup ──
makefile 0755 "root:root" "$tmp"/usr/local/bin/pxe-backup <<'PBACKUP'
#!/bin/sh
# pxe-backup — Backup/restore infra-pxe SQLite database
[ "${1:-}" = "--help" ] || [ "${1:-}" = "-h" ] && { echo "Usage: pxe-backup [DEST]"; echo "       pxe-backup --list"; echo "       pxe-backup --restore <FILE>"; exit 0; }
PXE_DIR="/srv/infra/pxe"; DB="$PXE_DIR/data/pxe.db"; BACKUP_DIR="$PXE_DIR/backups"
case "${1:-backup}" in
    --list|-l) [ -d "$BACKUP_DIR" ] && ls "$BACKUP_DIR"/*.db >/dev/null 2>&1 && ls -lh "$BACKUP_DIR"/*.db || echo "(none)";;
    --restore|-r) F="${2:-}"; [ ! -f "$F" ] && { echo "ERROR: not found"; exit 1; }
        sqlite3 "$F" "SELECT count(*) FROM config;" >/dev/null 2>&1 || { echo "ERROR: not a valid PXE database"; exit 1; }
        printf "Stop and restore? [y/N] "; read -r c; [ "$c" != "y" ] && [ "$c" != "Y" ] && { echo "Cancelled"; exit 0; }
        rc-service infra-pxe stop 2>/dev/null || pkill -f "infra-pxe" 2>/dev/null || true; sleep 1
        [ -f "$DB" ] && cp "$DB" "$DB.pre-restore.$(date +%Y%m%d-%H%M%S)"; cp "$F" "$DB"; echo "✓ Restored"
        rc-service infra-pxe start 2>/dev/null || { cd "$PXE_DIR"; ./bin/infra-pxe --config conf/pxe.yaml >> logs/pxe.log 2>&1 & }; sleep 1;;
    *) D="${1:-$BACKUP_DIR}"; mkdir -p "$D"; [ ! -f "$DB" ] && { echo "ERROR: DB not found"; exit 1; }
        TS=$(date +%Y%m%d-%H%M%S); O="$D/pxe-${TS}.db"; sqlite3 "$DB" ".backup '$O'"
        sqlite3 "$O" "PRAGMA integrity_check;" | grep -q "ok" && echo "✓ $O ($(du -h "$O" | cut -f1))" || { echo "ERROR: integrity failed"; rm -f "$O"; exit 1; };;
esac
PBACKUP

# ══════════════════════════════════════════════════
# OpenRC services
# ══════════════════════════════════════════════════
rc_add devfs sysinit; rc_add dmesg sysinit; rc_add mdev sysinit
rc_add hwdrivers sysinit; rc_add modloop sysinit
rc_add hwclock boot; rc_add modules boot; rc_add sysctl boot
rc_add hostname boot; rc_add bootmisc boot
rc_add local default; rc_add sshd default; rc_add infra-pxe default
rc_add mount-ro shutdown; rc_add killprocs shutdown; rc_add savecache shutdown

# ══════════════════════════════════════════════════
# 打包
# ══════════════════════════════════════════════════
tar -c -C "$tmp" etc usr | gzip -9n > "$HOSTNAME.apkovl.tar.gz"
