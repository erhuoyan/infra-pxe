#!/bin/bash
# PXE Station — early-commands script for Ubuntu autoinstall
# Downloaded and executed during early-commands phase
# Requires: API_URL set by caller (or defaults to first arg)
#
# Fetches provision config by SN or MAC, then generates dynamic
# storage + network config and overwrites /autoinstall.yaml.
# Subiquity re-reads autoinstall.yaml AFTER early-commands complete.

set -e
API_URL="${API_URL:-$1}"
LOG="/tmp/pxe-early.log"
log() { echo "[$(date +%Y-%m-%dT%H:%M:%S)] $*" >> $LOG; }

log "=== early-commands START ==="
log "API_URL=$API_URL"
log "which curl: $(which curl 2>&1)"
log "which python3: $(which python3 2>&1)"
log "ip addr: $(ip -4 addr show 2>&1 | grep inet | head -5)"
log "ip route: $(ip route 2>&1 | head -3)"
log "test API connectivity: $(curl -s --connect-timeout 5 -o /dev/null -w '%{http_code}' ${API_URL}/api/system/status 2>&1)"

# === Identify SN ===
SN=$(cat /sys/class/dmi/id/product_serial 2>/dev/null | tr -d ' ')
[ -z "$SN" ] || [ "$SN" = "Not Specified" ] && SN=""
log "DMI SN=$SN"

# === Fetch provision config ===
PROVISION_OK=false
if [ -n "$SN" ]; then
    RESP=$(curl -s --connect-timeout 10 --retry 3 "${API_URL}/api/provision/by-sn/${SN}")
    echo "$RESP" | python3 -c "import json,sys;r=json.load(sys.stdin);d=r.get('data') or r;json.dump(d,open('/tmp/provision.json','w'))" 2>/dev/null
    if python3 -c "import json;d=json.load(open('/tmp/provision.json'));assert d.get('sn')" 2>/dev/null; then
        PROVISION_OK=true
        log "provision fetched by SN=$SN"
    fi
fi
if [ "$PROVISION_OK" != "true" ]; then
    for mac in $(ip link show | grep link/ether | awk '{print $2}'); do
        RESP=$(curl -s --connect-timeout 5 "${API_URL}/api/provision/by-mac/${mac}")
        DATA=$(echo "$RESP" | python3 -c "import json,sys;r=json.load(sys.stdin);d=r.get('data') or r;print(json.dumps(d))" 2>/dev/null)
        if echo "$DATA" | python3 -c "import json,sys;d=json.load(sys.stdin);assert d.get('sn')" 2>/dev/null; then
            echo "$DATA" > /tmp/provision.json
            PROVISION_OK=true
            log "matched by MAC=$mac"
            break
        fi
    done
fi

# Update SN from provision.json (may have been empty from DMI)
if [ "$PROVISION_OK" = "true" ]; then
    SN=$(python3 -c "import json;print(json.load(open('/tmp/provision.json')).get('sn',''))" 2>/dev/null)
    log "effective SN=$SN"
fi

# === Event reporting helper (now SN is known) ===
pxe_event() {
    curl -s --connect-timeout 3 -X POST "${API_URL}/api/pxe/event" \
      -H "Content-Type: application/json" \
      -d "{\"sn\":\"${SN:-unknown}\",\"stage\":\"$1\",\"detail\":\"$2\"}" 2>/dev/null || true
}
pxe_event sn_identifying "SN=$SN"
pxe_event provision_matching "config found"

if [ "$PROVISION_OK" != "true" ]; then
    log "no provision config, using defaults"
    pxe_event provision_failed "no config found, using defaults"
    exit 0
fi

# === Generate dynamic storage + network, patch /autoinstall.yaml ===
python3 << 'PYEOF'
import json, yaml, os, glob, sys
from ipaddress import IPv4Network

d = json.load(open('/tmp/provision.json'))
# Read existing autoinstall.yaml
ai_path = '/autoinstall.yaml'
if os.path.exists(ai_path):
    ai = yaml.safe_load(open(ai_path))
else:
    ai = {}
if 'autoinstall' in ai:
    ai = ai['autoinstall']

# =========== STORAGE ===========
# Disk selection: wwn → serial → capacity (same logic as pxe-pre.sh)
disk_wwn = d.get('disk_wwn', '')
disk_serial = d.get('disk_serial', '')
target_size = d.get('disk_target_size', 480)
best_disk = ''

# Priority 1: WWN
if disk_wwn:
    for link in glob.glob(f'/dev/disk/by-id/wwn-*{disk_wwn}*'):
        if os.path.exists(link):
            best_disk = os.path.realpath(link)
            break

# Priority 2: Serial
if not best_disk and disk_serial:
    for blk in sorted(os.listdir('/sys/block')):
        if not any(blk.startswith(p) for p in ('sd', 'nvme', 'vd')):
            continue
        serial_path = f'/sys/block/{blk}/device/serial'
        if os.path.exists(serial_path):
            sn = open(serial_path).read().strip()
            if sn == disk_serial:
                best_disk = f'/dev/{blk}'
                break

# Priority 3: Capacity match
if not best_disk:
    best_diff = 999999999
    for blk in sorted(os.listdir('/sys/block')):
        if not any(blk.startswith(p) for p in ('sd', 'nvme', 'vd')):
            continue
        removable = open(f'/sys/block/{blk}/removable').read().strip()
        if removable == '1':
            continue
        size_sectors = int(open(f'/sys/block/{blk}/size').read().strip() or '0')
        if size_sectors == 0:
            continue
        size_gb = size_sectors * 512 // (1024**3)
        if size_gb == 0:
            continue
        diff = abs(size_gb - target_size)
        if diff < best_diff:
            best_diff = diff
            best_disk = f'/dev/{blk}'

# Build storage config
partition = d.get('partition', {})
boot_size_mb = partition.get('boot_size', 1024)  # MB (same unit as kickstart)
root_fstype = partition.get('root_fstype', 'ext4')
use_lvm = partition.get('use_lvm', True)
# UEFI vs BIOS detection
is_uefi = os.path.isdir('/sys/firmware/efi')

disk_entry = {
    'type': 'disk', 'ptable': 'gpt', 'wipe': 'superblock-recursive',
    'preserve': False, 'name': '', 'grub_device': True, 'id': 'disk0',
}
if best_disk:
    disk_entry['path'] = best_disk
else:
    disk_entry['match'] = {'size': 'largest'}

storage_config = [disk_entry]
part_num = 1

if is_uefi:
    storage_config.append({
        'type': 'partition', 'device': 'disk0',
        'size': 536870912,  # 512M
        'flag': 'boot', 'number': part_num,
        'preserve': False, 'grub_device': True, 'id': 'part-efi',
    })
    storage_config.append({
        'type': 'format', 'fstype': 'fat32', 'volume': 'part-efi',
        'preserve': False, 'id': 'fmt-efi',
    })
    part_num += 1
else:
    storage_config.append({
        'type': 'partition', 'device': 'disk0',
        'size': 1048576,  # 1M
        'flag': 'bios_grub', 'number': part_num,
        'preserve': False, 'grub_device': False, 'id': 'part-bios',
    })
    part_num += 1

# /boot partition
boot_bytes = boot_size_mb * 1024 * 1024
storage_config.append({
    'type': 'partition', 'device': 'disk0',
    'size': boot_bytes, 'wipe': 'superblock', 'number': part_num,
    'preserve': False, 'grub_device': False, 'id': 'part-boot',
})
storage_config.append({
    'type': 'format', 'fstype': 'ext4', 'volume': 'part-boot',
    'preserve': False, 'id': 'fmt-boot',
})
part_num += 1

# Root partition (LVM or direct)
storage_config.append({
    'type': 'partition', 'device': 'disk0',
    'size': -1, 'wipe': 'superblock', 'number': part_num,
    'preserve': False, 'grub_device': False, 'id': 'part-root-pv',
})

if use_lvm:
    storage_config.append({
        'type': 'lvm_volgroup', 'name': 'vg0',
        'devices': ['part-root-pv'], 'preserve': False, 'id': 'vg0',
    })
    storage_config.append({
        'type': 'lvm_partition', 'name': 'lv-root',
        'volgroup': 'vg0', 'wipe': 'superblock',
        'size': -1, 'preserve': False, 'id': 'lv-root',
    })
    storage_config.append({
        'type': 'format', 'fstype': root_fstype, 'volume': 'lv-root',
        'preserve': False, 'id': 'fmt-root',
    })
else:
    storage_config.append({
        'type': 'format', 'fstype': root_fstype, 'volume': 'part-root-pv',
        'preserve': False, 'id': 'fmt-root',
    })

# Mounts
storage_config.append({'type': 'mount', 'device': 'fmt-root', 'path': '/', 'id': 'mnt-root'})
storage_config.append({'type': 'mount', 'device': 'fmt-boot', 'path': '/boot', 'id': 'mnt-boot'})
if is_uefi:
    storage_config.append({'type': 'mount', 'device': 'fmt-efi', 'path': '/boot/efi', 'id': 'mnt-efi'})

ai['storage'] = {'config': storage_config}

# =========== NETWORK ===========
net = d.get('network')
if net:
    ip_addr = net.get('ip', '')
    netmask = net.get('netmask', '255.255.255.0')
    gateway = net.get('gateway', '')
    dns_list = net.get('dns', [])
    bond = net.get('bond')
    prefix = IPv4Network(f'0.0.0.0/{netmask}', strict=False).prefixlen

    def mac_to_ifname(mac_or_name):
        if ':' not in mac_or_name and '-' not in mac_or_name:
            return mac_or_name
        mac = mac_or_name.lower().replace('-', ':')
        for path in glob.glob('/sys/class/net/*/address'):
            if open(path).read().strip().lower() == mac:
                return path.split('/')[-2]
        return mac_or_name

    net_cfg = {'version': 2}

    if bond:
        mode_map = {0:'balance-rr',1:'active-backup',2:'balance-xor',3:'broadcast',4:'802.3ad',5:'balance-tlb',6:'balance-alb'}
        mode = mode_map.get(bond.get('mode', 4), '802.3ad')
        raw_slaves = bond.get('slaves', [])
        slaves = [mac_to_ifname(s) for s in raw_slaves]

        ethernets = {}
        for s in slaves:
            ethernets[s] = {'dhcp4': False, 'dhcp6': False}
        net_cfg['ethernets'] = ethernets

        bond_cfg = {
            'interfaces': slaves,
            'parameters': {'mode': mode, 'mii-monitor-interval': bond.get('miimon', 100)},
        }
        if mode == '802.3ad':
            bond_cfg['parameters']['lacp-rate'] = 'fast' if bond.get('lacp_rate', 1) == 1 else 'slow'
            bond_cfg['parameters']['transmit-hash-policy'] = bond.get('xmit_hash_policy', 'layer3+4')
        if ip_addr:
            bond_cfg['addresses'] = [f'{ip_addr}/{prefix}']
            if gateway:
                bond_cfg['routes'] = [{'to': 'default', 'via': gateway}]
            if dns_list:
                bond_cfg['nameservers'] = {'addresses': dns_list}
        else:
            bond_cfg['dhcp4'] = True
        net_cfg['bonds'] = {'bond0': bond_cfg}

    elif ip_addr:
        mac = (net.get('mac') or '').lower()
        ifname = mac_to_ifname(mac) if mac else None
        if not ifname:
            for iface in sorted(os.listdir('/sys/class/net')):
                if iface != 'lo' and not iface.startswith(('veth', 'docker', 'br-')):
                    ifname = iface
                    break
        if not ifname:
            ifname = 'id0'
        eth_cfg = {'dhcp4': False, 'addresses': [f'{ip_addr}/{prefix}']}
        if gateway:
            eth_cfg['routes'] = [{'to': 'default', 'via': gateway}]
        if dns_list:
            eth_cfg['nameservers'] = {'addresses': dns_list}
        if mac:
            eth_cfg['match'] = {'macaddress': mac}
        net_cfg['ethernets'] = {ifname: eth_cfg}

    else:
        net_cfg = None

    if net_cfg:
        ai['network'] = net_cfg

# =========== IDENTITY ===========
hostname = d.get('hostname', 'localhost')
root_pw = d.get('root_password', '')
if hostname:
    if 'identity' not in ai:
        ai['identity'] = {}
    ai['identity']['hostname'] = hostname
if root_pw:
    import crypt
    hashed = crypt.crypt(root_pw, crypt.mksalt(crypt.METHOD_SHA512))
    if 'identity' not in ai:
        ai['identity'] = {}
    ai['identity']['password'] = hashed

# =========== SSH KEYS ===========
# Autoinstall's `ssh.authorized-keys` writes keys into /root/.ssh/authorized_keys
# and cloud-init preserves them across reboots. This is the ONLY authoritative
# place — do not also inject in post-install (cloud-init would overwrite).
ssh_keys = d.get('ssh_keys') or []
if ssh_keys:
    if 'ssh' not in ai:
        ai['ssh'] = {}
    ai['ssh']['install-server'] = True
    ai['ssh']['allow-pw'] = True
    ai['ssh']['authorized-keys'] = ssh_keys

# =========== WRITE ===========
output = {'autoinstall': ai} if 'version' in ai else ai
if 'autoinstall' in output:
    output['autoinstall'].setdefault('version', 1)
else:
    output.setdefault('version', 1)

with open(ai_path, 'w') as f:
    yaml.dump(output, f, default_flow_style=False, sort_keys=False)
print(f'Wrote dynamic autoinstall: disk={best_disk or "largest"}, net={"bond" if (net and net.get("bond")) else ("static" if (net and net.get("ip")) else "dhcp")}')
PYEOF

pxe_event disk_selected "target disk determined"
pxe_event partitioning "autoinstall config generated"
pxe_event pkg_installing "handing off to Subiquity installer"
log "early-commands complete"
