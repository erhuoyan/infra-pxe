#!/bin/bash
# PXE Station — %pre script for anaconda kickstart
# Downloaded and executed during %pre phase
# Requires: API_URL, LOG_FILE set by caller

export TZ=UTC

# === Logging ===
pxe_log() {
    local level="$1" stage="$2" msg="$3"
    echo "[$(date +%Y-%m-%dT%H:%M:%S)] [$level] [$stage] $msg" >> $LOG_FILE
}

pxe_event() {
    local stage="$1" detail="$2"
    curl -s --connect-timeout 3 -X POST "${API_URL}/api/pxe/event" \
      -H "Content-Type: application/json" \
      -d "{\"sn\":\"${SN:-unknown}\",\"stage\":\"${stage}\",\"detail\":\"${detail}\"}" 2>/dev/null || true
}

# === Wait for network ===
pxe_log INFO network "Waiting for anaconda network..."
for i in $(seq 1 60); do
    ip route | grep -q default && break
    sleep 1
done
pxe_log INFO network "network ready: $(ip route | grep default)"

# === Identify SN ===
SN=$(cat /sys/class/dmi/id/product_serial 2>/dev/null | tr -d ' ')
pxe_event sn_identifying "SN=$SN"
pxe_log INFO sn_identify "SN=$SN"

# === Fetch provision config ===
PROVISION_OK=false
if [ -n "$SN" ] && [ "$SN" != "NotSpecified" ] && [ "$SN" != "Not Specified" ]; then
    RESP=$(curl -s --connect-timeout 10 --retry 3 "${API_URL}/api/provision/by-sn/${SN}")
    echo "$RESP" | python3 -c "import json,sys;r=json.load(sys.stdin);d=r.get('data') or r;json.dump(d,open('/tmp/provision.json','w'))" 2>/dev/null
    if python3 -c "import json;d=json.load(open('/tmp/provision.json'));assert d.get('sn')" 2>/dev/null; then
        PROVISION_OK=true
        pxe_log INFO provision "config fetched for SN=$SN"
    else
        pxe_log ERROR provision "config not found or invalid for SN=$SN, falling back to MAC"
    fi
fi
if [ "$PROVISION_OK" != "true" ]; then
    # MAC 兜底：SN 为空或 SN 查不到都执行（对齐 pxe-pre-ubuntu.sh）
    MATCHED=false
    for mac in $(ip link show | grep link/ether | awk '{print $2}'); do
        RESP=$(curl -s --connect-timeout 5 "${API_URL}/api/provision/by-mac/${mac}")
        # Response is {"code":"200","data":{...}} — extract data
        DATA=$(echo "$RESP" | python3 -c "import json,sys;r=json.load(sys.stdin);d=r.get('data') or r;print(json.dumps(d))" 2>/dev/null)
        if echo "$DATA" | python3 -c "import json,sys;d=json.load(sys.stdin);assert d.get('sn')" 2>/dev/null; then
            echo "$DATA" > /tmp/provision.json
            MATCHED=true
            PROVISION_OK=true
            pxe_log INFO provision "matched by MAC=$mac"
            break
        fi
    done
    if ! $MATCHED; then
        pxe_log ERROR provision "no host matched by SN or MAC"
        pxe_event provision_failed "no match"
        exit 1
    fi
fi

# Update SN from provision.json (needed for event reporting when SN was empty)
if [ -z "$SN" ] && [ -f /tmp/provision.json ]; then
    SN=$(python3 -c "import json;print(json.load(open('/tmp/provision.json')).get('sn',''))" 2>/dev/null)
fi

# Report provision status (now SN is known)
if [ "$PROVISION_OK" = "true" ]; then
    PROV_HOST=$(python3 -c "import json;print(json.load(open('/tmp/provision.json')).get('hostname',''))" 2>/dev/null)
    PROV_IP=$(python3 -c "import json;print(json.load(open('/tmp/provision.json')).get('ip',''))" 2>/dev/null)
    PROV_MAC=$(ip link show | grep link/ether | head -1 | awk '{print $2}')
    pxe_event provision_matching "hostname=$PROV_HOST, ip=$PROV_IP, mac=$PROV_MAC, SN=$SN"
fi
# === Disk selection (priority: wwn → serial → capacity) ===
TARGET_SIZE=${DEFAULT_DISK_SIZE:-480}
DISK_WWN=""
DISK_SERIAL=""
if [ -f /tmp/provision.json ]; then
    TARGET_SIZE=$(python3 -c "import json;print(json.load(open('/tmp/provision.json')).get('disk_target_size', ${TARGET_SIZE}))" 2>/dev/null || echo "$TARGET_SIZE")
    DISK_WWN=$(python3 -c "import json;print(json.load(open('/tmp/provision.json')).get('disk_wwn', ''))" 2>/dev/null)
    DISK_SERIAL=$(python3 -c "import json;print(json.load(open('/tmp/provision.json')).get('disk_serial', ''))" 2>/dev/null)
fi

best_disk=""

# Priority 1: Match by WWN
if [ -n "$DISK_WWN" ]; then
    for link in /dev/disk/by-id/wwn-*"$DISK_WWN"*; do
        if [ -e "$link" ]; then
            best_disk=$(basename "$(readlink -f "$link")")
            pxe_log INFO disk "matched by WWN: $DISK_WWN → $best_disk"
            break
        fi
    done
fi

# Priority 2: Match by serial number
if [ -z "$best_disk" ] && [ -n "$DISK_SERIAL" ]; then
    for disk in $(ls /sys/block | grep -E '^(sd|nvme|vd)'); do
        sn=$(cat /sys/block/$disk/device/serial 2>/dev/null | tr -d ' ')
        [ -z "$sn" ] && sn=$(smartctl -i /dev/$disk 2>/dev/null | grep -i 'Serial' | awk '{print $NF}')
        if [ "$sn" = "$DISK_SERIAL" ]; then
            best_disk=$disk
            pxe_log INFO disk "matched by serial: $DISK_SERIAL → $best_disk"
            break
        fi
    done
fi

# Priority 3: Match by capacity (fallback)
if [ -z "$best_disk" ]; then
    best_diff=999999999
    for disk in $(ls /sys/block | grep -E '^(sd|nvme|vd)'); do
        # Skip removable devices (USB sticks, CD-ROM)
        [ "$(cat /sys/block/$disk/removable 2>/dev/null)" = "1" ] && continue
        # Skip USB-connected disks
        readlink -f /sys/block/$disk/device 2>/dev/null | grep -qi usb && continue
        # Skip devices with no medium / zero size
        size=$(cat /sys/block/$disk/size 2>/dev/null)
        [ -z "$size" ] || [ "$size" = "0" ] && continue
        size_gb=$((size * 512 / 1024 / 1024 / 1024))
        [ $size_gb -eq 0 ] && continue
        diff=$((size_gb - TARGET_SIZE))
        [ $diff -lt 0 ] && diff=$((-diff))
        if [ $diff -lt $best_diff ]; then
            best_diff=$diff
            best_disk=$disk
        fi
    done
    [ -n "$best_disk" ] && pxe_log INFO disk "matched by capacity: target=${TARGET_SIZE}GB → $best_disk"
fi

if [ -z "$best_disk" ]; then
    pxe_log ERROR disk "no suitable disk found (wwn=$DISK_WWN, serial=$DISK_SERIAL, target=${TARGET_SIZE}GB)"
    pxe_event provision_failed "no disk found"
    exit 1
fi
echo "bootloader --location=mbr --driveorder=$best_disk" > /tmp/partation.ks
echo "ignoredisk --only-use=$best_disk" >> /tmp/partation.ks
echo "clearpart --all --initlabel --drives=$best_disk" >> /tmp/partation.ks
pxe_log INFO disk "selected $best_disk (target=${TARGET_SIZE}GB, diff=${best_diff}GB)"
pxe_event disk_selected "disk=$best_disk(${size_gb:-?}G), target=${TARGET_SIZE}GB"

# === Partition layout ===
if [ -f /tmp/provision.json ] && python3 -c "import json;json.load(open('/tmp/provision.json'))" 2>/dev/null; then
    python3 << 'PYEOF' > /tmp/partition_layout.ks
import json
d = json.load(open('/tmp/provision.json'))
p = d.get('partition', {})
boot_size = p.get('boot_size', 1024)
efi_size = p.get('efi_size', 1024)
root_fstype = p.get('root_fstype', 'xfs')
use_lvm = p.get('use_lvm', True)

print(f'part /boot --fstype="xfs" --size={boot_size}')
print(f'part /boot/efi --fstype="vfat" --size={efi_size}')
if use_lvm:
    print('part pv.01 --fstype="lvmpv" --grow')
    print('volgroup vg_root --pesize=4096 pv.01')
    print(f'logvol / --fstype="{root_fstype}" --percent=100 --name=root --vgname=vg_root')
else:
    print(f'part / --fstype="{root_fstype}" --grow')
PYEOF
else
    cat > /tmp/partition_layout.ks << 'PARTEOF'
part /boot --fstype="xfs" --size=1024
part /boot/efi --fstype="vfat" --size=1024
part / --fstype="xfs" --grow
PARTEOF
fi
PART_LAYOUT=$(cat /tmp/partition_layout.ks 2>/dev/null | tr '\n' '; ' | sed 's/; $//')
pxe_event partitioning "disk=$best_disk(${size_gb:-?}G), layout: $PART_LAYOUT"

# === Root password ===
if [ -f /tmp/provision.json ] && python3 -c "import json;json.load(open('/tmp/provision.json'))" 2>/dev/null; then
    ROOTPW=$(python3 -c "import json;print(json.load(open('/tmp/provision.json')).get('root_password', '123456'))" 2>/dev/null)
    echo "rootpw --plaintext ${ROOTPW}" > /tmp/rootpw.ks
else
    echo "rootpw --plaintext 123456" > /tmp/rootpw.ks
fi

# === Network configuration (anaconda) ===
if [ -f /tmp/provision.json ]; then
    python3 << 'PYEOF' > /tmp/network.ks
import json, glob

def mac_to_ifname(mac_or_name):
    if ':' not in mac_or_name and '-' not in mac_or_name:
        return mac_or_name
    mac = mac_or_name.lower().replace('-', ':')
    for path in glob.glob('/sys/class/net/*/address'):
        if open(path).read().strip().lower() == mac:
            return path.split('/')[-2]
    return mac_or_name

try:
    d = json.load(open('/tmp/provision.json'))
except Exception:
    print('network --device=link --bootproto=dhcp --onboot=yes --noipv6 --hostname=localhost')
    raise SystemExit(0)

hostname = d.get('hostname', 'localhost')
net = d.get('network')

if not net:
    print(f'network --device=link --bootproto=dhcp --onboot=yes --noipv6 --hostname={hostname}')
    raise SystemExit(0)

ip = net.get('ip', '')
netmask = net.get('netmask', '255.255.255.0')
gateway = net.get('gateway', '')
dns_list = net.get('dns', [])
dns = ','.join(dns_list)
bond = net.get('bond')

dns_opt = f'--nameserver={dns}' if dns_list else ''
common = f'--bootproto=static --ip={ip} --netmask={netmask} --gateway={gateway} {dns_opt} --hostname={hostname} --onboot=yes --noipv6'

if bond:
    mode = bond.get('mode', 4)
    raw_slaves = bond.get('slaves', [])
    # --bondslaves requires interface names, not MACs
    resolved_slaves = [mac_to_ifname(s) for s in raw_slaves]
    slaves = ','.join(resolved_slaves)
    miimon = bond.get('miimon', 100)
    lacp_rate = bond.get('lacp_rate', 1)
    xmit_hash_policy = bond.get('xmit_hash_policy', 'layer3+4')
    mode_map = {0:'balance-rr',1:'active-backup',2:'balance-xor',3:'broadcast',4:'802.3ad',5:'balance-tlb',6:'balance-alb'}
    mode_name = mode_map.get(mode, '802.3ad')
    lacp_rate_name = 'fast' if lacp_rate == 1 else 'slow'
    opts = f'mode={mode_name},miimon={miimon},lacp_rate={lacp_rate_name},xmit_hash_policy={xmit_hash_policy}'
    print(f'network --device=bond0 --bondslaves={slaves} --bondopts={opts} {common}')
    # Activate slave devices (use interface names — proven working with bond)
    for slave in resolved_slaves:
        print(f'network --device={slave} --onboot=yes --noipv6')
elif ip:
    mac = net.get('mac', '')
    if mac:
        # Use MAC to identify device (more reliable than "link")
        device = mac.lower()
    else:
        device = 'link'
    print(f'network --device={device} {common}')
else:
    mac = net.get('mac', '')
    device = mac.lower() if mac else 'link'
    print(f'network --device={device} --bootproto=dhcp --onboot=yes --noipv6 --hostname={hostname}')
PYEOF
else
    echo "network --device=link --bootproto=dhcp --onboot=yes --noipv6 --hostname=localhost" > /tmp/network.ks
fi

# Signal: %pre done, anaconda will start package installation
pxe_event pkg_installing "partitioning done, installing packages..."
pxe_log INFO pre_done "pxe-pre.sh completed, handing off to anaconda"
