#!/bin/bash
# PXE Station — post-install script for Ubuntu (cloud-init / autoinstall)
# Downloaded and executed via late-commands: curtin in-target --target=/target
# Requires: API_URL, LOG_FILE set by caller
#
# Already in the target root (curtin handles chroot).
# Network: generates netplan YAML from provision.json
# Then: hostname, SSH, file injection, custom scripts, hardware collection, completion

export TZ=UTC

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

# === Identify SN ===
SN=""
if [ -f /var/lib/pxe-provision.json ]; then
    SN=$(python3 -c "import json;print(json.load(open('/var/lib/pxe-provision.json')).get('sn',''))" 2>/dev/null)
fi
if [ -z "$SN" ]; then
    SN=$(cat /sys/class/dmi/id/product_serial 2>/dev/null | tr -d ' ')
    if [ -z "$SN" ] || [ "$SN" = "NotSpecified" ] || [ "$SN" = "Not Specified" ]; then
        SN=$(cat /sys/class/dmi/id/product_uuid 2>/dev/null | tr -d ' ')
    fi
fi
pxe_log INFO post_start "entering post-install (Ubuntu), SN=$SN"

# === Post-install begins (Subiquity finished, now in target chroot) ===

# === Load provision config ===
if [ -n "$SN" ] && [ "$SN" != "NotSpecified" ] && [ "$SN" != "Not Specified" ]; then
    if [ ! -f /var/lib/pxe-provision.json ] || ! python3 -c "import json;d=json.load(open('/var/lib/pxe-provision.json'));assert d.get('sn')" 2>/dev/null; then
        RESP=$(curl -s --connect-timeout 10 --retry 3 "${API_URL}/api/provision/by-sn/${SN}")
        echo "$RESP" | python3 -c "import json,sys;r=json.load(sys.stdin);d=r.get('data') or r;json.dump(d,open('/var/lib/pxe-provision.json','w'))" 2>/dev/null
        pxe_log INFO provision "re-fetched provision config for SN=$SN"
    else
        pxe_log INFO provision "using pre-copied provision.json"
    fi
fi
# Symlink for scripts that expect /tmp/provision.json
cp /var/lib/pxe-provision.json /tmp/provision.json 2>/dev/null

# === Set hostname ===
PROV_HOSTNAME=$(python3 -c "import json;print(json.load(open('/tmp/provision.json')).get('hostname', 'localhost'))" 2>/dev/null)
if [ -n "$PROV_HOSTNAME" ] && [ "$PROV_HOSTNAME" != "localhost" ]; then
    hostnamectl set-hostname "$PROV_HOSTNAME" 2>/dev/null || echo "$PROV_HOSTNAME" > /etc/hostname
    pxe_log INFO hostname "set hostname=$PROV_HOSTNAME"
fi

# === Root password ===
ROOT_PW=$(python3 -c "import json;print(json.load(open('/tmp/provision.json')).get('root_password', ''))" 2>/dev/null)
if [ -n "$ROOT_PW" ]; then
    echo "root:${ROOT_PW}" | chpasswd 2>/dev/null
    pxe_log INFO password "root password set"
fi

# === Write clean netplan from provision.json ===
# Disable cloud-init network (prevents 50-cloud-init.yaml regen at boot).
# Then wipe all netplan files and write a single 00-installer.yaml
# generated from provision.json — no noisy cloud-init comment header.
mkdir -p /etc/cloud/cloud.cfg.d
echo 'network: {config: disabled}' > /etc/cloud/cloud.cfg.d/99-disable-network-config.cfg

python3 << 'PYEOF'
import json, glob, os
from ipaddress import IPv4Network

try:
    import yaml
except ImportError:
    yaml = None

def mac_to_ifname(mac):
    mac = mac.lower().replace('-', ':')
    for path in glob.glob('/sys/class/net/*/address'):
        if open(path).read().strip().lower() == mac:
            return path.split('/')[-2]
    return None

try:
    d = json.load(open('/tmp/provision.json'))
except Exception:
    raise SystemExit(0)

net = d.get('network') or {}
ip_addr = net.get('ip', '')
netmask = net.get('netmask', '255.255.255.0')
gateway = net.get('gateway', '')
dns_list = net.get('dns', [])
bond = net.get('bond')

cfg = {'network': {'version': 2, 'renderer': 'networkd'}}

if bond:
    mode_map = {0:'balance-rr',1:'active-backup',2:'balance-xor',3:'broadcast',4:'802.3ad',5:'balance-tlb',6:'balance-alb'}
    mode = mode_map.get(bond.get('mode', 4), '802.3ad')
    slaves = [mac_to_ifname(s) or s for s in bond.get('slaves', [])]
    eths = {s: {'dhcp4': False, 'dhcp6': False} for s in slaves}
    cfg['network']['ethernets'] = eths
    bond_cfg = {
        'interfaces': slaves,
        'parameters': {'mode': mode, 'mii-monitor-interval': bond.get('miimon', 100)},
    }
    if mode == '802.3ad':
        bond_cfg['parameters']['lacp-rate'] = 'fast' if bond.get('lacp_rate', 1) == 1 else 'slow'
        bond_cfg['parameters']['transmit-hash-policy'] = bond.get('xmit_hash_policy', 'layer3+4')
    if ip_addr:
        prefix = IPv4Network(f'0.0.0.0/{netmask}', strict=False).prefixlen
        bond_cfg['addresses'] = [f'{ip_addr}/{prefix}']
        if gateway:
            bond_cfg['routes'] = [{'to': 'default', 'via': gateway}]
        if dns_list:
            bond_cfg['nameservers'] = {'addresses': dns_list}
    else:
        bond_cfg['dhcp4'] = True
    cfg['network']['bonds'] = {'bond0': bond_cfg}
elif ip_addr:
    mac = (net.get('mac') or '').lower()
    ifname = mac_to_ifname(mac) if mac else None
    if not ifname:
        for iface in sorted(os.listdir('/sys/class/net')):
            if iface != 'lo' and not iface.startswith(('veth', 'docker', 'br-')):
                ifname = iface
                break
    ifname = ifname or 'eth0'
    prefix = IPv4Network(f'0.0.0.0/{netmask}', strict=False).prefixlen
    eth = {'dhcp4': False, 'addresses': [f'{ip_addr}/{prefix}']}
    if mac:
        eth['match'] = {'macaddress': mac}
    if gateway:
        eth['routes'] = [{'to': 'default', 'via': gateway}]
    if dns_list:
        eth['nameservers'] = {'addresses': dns_list}
    cfg['network']['ethernets'] = {ifname: eth}
else:
    raise SystemExit(0)  # DHCP: leave defaults

os.makedirs('/etc/netplan', exist_ok=True)
for f in glob.glob('/etc/netplan/*.yaml'):
    os.remove(f)

out = '/etc/netplan/00-installer.yaml'
if yaml:
    yaml.dump(cfg, open(out, 'w'), default_flow_style=False, sort_keys=False)
else:
    open(out, 'w').write(json.dumps(cfg, indent=2))
os.chmod(out, 0o600)
print(f'wrote {out}')
PYEOF

pxe_log INFO network "netplan written to /etc/netplan/00-installer.yaml"

# === System tweaks ===
pxe_log INFO system_tweaks "configuring SSH and system files"
echo > /etc/motd && echo > /etc/issue && echo > /etc/issue.net
sed -i 's/^#\?PermitRootLogin.*/PermitRootLogin yes/' /etc/ssh/sshd_config
sed -i 's/^#\?PasswordAuthentication.*/PasswordAuthentication yes/' /etc/ssh/sshd_config
# Ensure sshd_config.d doesn't override
if [ -d /etc/ssh/sshd_config.d ]; then
    for f in /etc/ssh/sshd_config.d/*.conf; do
        [ -f "$f" ] && sed -i 's/^PasswordAuthentication no/PasswordAuthentication yes/' "$f"
    done
fi
pxe_log INFO system_tweaks "SSH configured: PermitRootLogin=yes, PasswordAuthentication=yes"

# === SSH keys handled by autoinstall ===
# pxe-pre-ubuntu.sh injects ssh.authorized-keys into /autoinstall.yaml,
# and cloud-init writes authorized_keys from the datasource.
# No post-install write needed — let cloud-init handle it.
pxe_log INFO ssh_keys "SSH keys handled via autoinstall/cloud-init"

# === Download inject files ===
FILE_COUNT=$(python3 -c "import json;d=json.load(open('/var/lib/pxe-provision.json'));print(len(d.get('files',[])))" 2>/dev/null || echo "0")
if [ "$FILE_COUNT" != "0" ] && [ -f /var/lib/pxe-provision.json ]; then
    FILE_NAMES=$(python3 -c "import json;d=json.load(open('/var/lib/pxe-provision.json'));print(', '.join(f['filename'] for f in d.get('files',[])))" 2>/dev/null || echo "${FILE_COUNT} files")
    pxe_event files_injecting "$FILE_NAMES"
    INJECT_FILES=$(python3 -c "
import json
d = json.load(open('/var/lib/pxe-provision.json'))
files = d.get('files', [])
for f in files:
    print(f['url'] + '|' + f['dest'])
" 2>/dev/null)
    echo "$INJECT_FILES" | while IFS='|' read -r url dest; do
        pxe_log INFO inject_files "downloading $url → $dest"
        mkdir -p "$(dirname "$dest")"
        curl -sf --connect-timeout 10 --retry 3 "${API_URL}${url}" -o "$dest" || \
            pxe_log WARN inject_files "failed to download $url"
    done
    pxe_log INFO inject_files "download complete"
fi

# === Execute custom scripts ===
if [ -f /var/lib/pxe-provision.json ]; then
    SCRIPTS_JSON=$(python3 -c "
import json
d = json.load(open('/var/lib/pxe-provision.json'))
scripts = d.get('scripts', [])
if not scripts and d.get('post_script'):
    scripts = [{'name': 'inline', 'type': 'bash', 'content': d['post_script']}]
import json as j
print(j.dumps(scripts))
" 2>/dev/null)

    if [ -n "$SCRIPTS_JSON" ] && [ "$SCRIPTS_JSON" != "[]" ]; then
        SCRIPT_NAMES=$(echo "$SCRIPTS_JSON" | python3 -c "import json,sys;scripts=json.load(sys.stdin);print(', '.join(s['name'] for s in scripts))" 2>/dev/null || echo "scripts")
        pxe_event script_executing "$SCRIPT_NAMES"
        pxe_log INFO custom_script "executing: $SCRIPT_NAMES"

        python3 -c "
import json, subprocess, sys, os

scripts = json.loads(open('/var/lib/pxe-provision.json').read()).get('scripts', [])
if not scripts:
    ps = json.loads(open('/var/lib/pxe-provision.json').read()).get('post_script', '')
    if ps:
        scripts = [{'name': 'inline', 'type': 'bash', 'content': ps}]

type_to_cmd = {'bash': 'bash', 'python': 'python3', 'sh': 'sh'}

for i, s in enumerate(scripts):
    name = s.get('name', f'script_{i}')
    stype = s.get('type', 'bash')
    content = s.get('content', '')
    if not content:
        continue
    interpreter = type_to_cmd.get(stype, 'bash')
    script_file = f'/tmp/custom-script-{i}.tmp'
    log_file = f'/var/log/pxe-script-{name}.log'
    with open(script_file, 'w') as f:
        f.write(content)
    print(f'[script] running {name} ({stype}) → log: {log_file}', flush=True)
    with open(log_file, 'w') as lf:
        lf.write(f'=== {name} ({stype}) ===\n')
    result = subprocess.run(
        [interpreter, script_file],
        stdout=open(log_file, 'a'),
        stderr=subprocess.STDOUT,
    )
    with open(log_file) as lf:
        tail = lf.read()[-2000:]
    print(f'[script] {name} exit={result.returncode}', flush=True)
    if tail.strip():
        print(f'[script-output] {name}:\n{tail}', flush=True)
    if result.returncode != 0:
        print(f'[script] {name} FAILED (exit={result.returncode})', file=sys.stderr)
    os.remove(script_file)
" >> $LOG_FILE 2>&1

        if [ $? -eq 0 ]; then
            pxe_log INFO custom_script "all scripts completed"
        else
            pxe_log ERROR custom_script "some scripts failed"
        fi
    fi
fi

# === Hardware Collection & Report ===
pxe_log INFO hardware "collecting hardware info"
pxe_event hardware_collecting "starting hw-collect"

curl -sf --connect-timeout 10 --retry 3 \
  "${API_URL}/api/pxe/scripts/hw-collect.py" -o /tmp/hw-collect.py

if [ -f /tmp/hw-collect.py ]; then
    COMPONENTS_JSON=$(python3 /tmp/hw-collect.py 2>/tmp/hw-collect.log)
else
    pxe_log WARN hardware "failed to download hw-collect.py, using minimal inline fallback"
    # Inline fallback
    COMPONENTS_JSON=$(python3 -c "
import json, subprocess, os

def cmd(c):
    try: return subprocess.check_output(c, shell=True, stderr=subprocess.DEVNULL).decode().strip()
    except: return ''

result = {'cpus':[], 'memory':[], 'disks':[], 'nics':[], 'gpus':[], 'ports':[], 'system':{}}
for line in cmd('lscpu').split('\n'):
    if 'Model name' in line:
        result['cpus'].append({'model': line.split(':',1)[1].strip(), 'cores': int(cmd('nproc') or '0')})
        break
mem_total = int(cmd(\"grep MemTotal /proc/meminfo | awk '{print \$2}'\") or '0')
if mem_total:
    result['memory'].append({'capacity_gb': round(mem_total/1024/1024), 'slot': 'total'})
for disk in sorted(os.listdir('/sys/block')):
    if not any(disk.startswith(p) for p in ('sd','nvme','vd')): continue
    size = int(open(f'/sys/block/{disk}/size').read().strip() or '0')
    if size == 0: continue
    result['disks'].append({'slot': disk, 'capacity_gb': round(size*512/1024/1024/1024), 'media':'', 'interface':''})
print(json.dumps(result))
" 2>/dev/null)
fi

pxe_log INFO hardware "COMPONENTS_JSON length: ${#COMPONENTS_JSON}"
HW_COUNTS=$(echo "$COMPONENTS_JSON" | python3 -c "import json,sys;c=json.load(sys.stdin);print(f\"CPU:{len(c.get('cpus',[]))} MEM:{len(c.get('memory',[]))} DISK:{len(c.get('disks',[]))} NIC:{len(c.get('nics',[]))} GPU:{len(c.get('gpus',[]))} PORT:{len(c.get('ports',[]))}\")" 2>/dev/null || echo "")
pxe_event hardware_reporting "$HW_COUNTS"

if [ -n "$COMPONENTS_JSON" ] && [ "$COMPONENTS_JSON" != "{}" ]; then
    pxe_log INFO hardware "reporting structured components to ${API_URL}/api/assets/${SN}/components"
    COMP_RESP=$(curl -s --connect-timeout 5 --retry 3 -X POST "${API_URL}/api/assets/${SN}/components" \
      -H "Content-Type: application/json" \
      -d "$COMPONENTS_JSON" 2>&1)
    pxe_log INFO hardware "components response: $COMP_RESP"

    # Summary report
    SUMMARY=$(echo "$COMPONENTS_JSON" | python3 -c "
import json, sys
c = json.load(sys.stdin)
hw = {}
hw['CPU'] = c['cpus'][0]['model'].split('\n')[0] if c.get('cpus') else ''
hw['CORES'] = sum(x['cores'] for x in c.get('cpus', []))
hw['memory_gb'] = sum(x.get('capacity_gb', 0) for x in c.get('memory', []))
hw['DISKS'] = ' | '.join(f\"{d['slot']} {d.get('capacity_gb',0)}GB\" for d in c.get('disks', []))
hw['OS'] = ''
try:
    with open('/etc/os-release') as f:
        for line in f:
            if line.startswith('PRETTY_NAME='):
                hw['OS'] = line.split('=',1)[1].strip().strip('\"')
                break
except: pass
try: hw['KERNEL'] = open('/proc/version').read().split()[2]
except: hw['KERNEL'] = ''
try: hw['hostname'] = open('/etc/hostname').read().strip()
except: hw['hostname'] = ''
sys_info = c.get('system', {})
hw['bios_version'] = sys_info.get('bios_version', '')
hw['bmc_version'] = sys_info.get('bmc_version', '')
hw['board_manufacturer'] = sys_info.get('board_manufacturer', '')
hw['board_product'] = sys_info.get('board_product', '')
cpus = c.get('cpus', [])
mems = c.get('memory', [])
disks = c.get('disks', [])
gpus = c.get('gpus', [])
nics = c.get('nics', [])
parts = []
if cpus:
    cpu_short = cpus[0]['model'].split()[-1] if cpus[0].get('model') else 'Unknown'
    parts.append(f\"{cpu_short}*{len(cpus)}\")
if mems:
    mem_cap = int(mems[0].get('capacity_gb', 0))
    parts.append(f\"{mem_cap}GB*{len(mems)}\")
if disks:
    dgrp = {}
    for d in disks:
        key = f\"{d.get('capacity_gb',0):.0f}GB\"
        dgrp[key] = dgrp.get(key, 0) + 1
    parts.append(' + '.join(f\"{k}*{v}\" for k, v in dgrp.items()))
if gpus:
    parts.append(f\"GPU*{len(gpus)}\")
if nics:
    parts.append(f\"NIC*{len(nics)}\")
hw['config_summary'] = '/'.join(parts)
print(json.dumps(hw))
" 2>/dev/null)
    pxe_log INFO hardware "SUMMARY: $SUMMARY"
    if [ -n "$SUMMARY" ]; then
        HW_RESP=$(curl -s --connect-timeout 5 --retry 3 -X POST "${API_URL}/api/assets/${SN}/hardware" \
          -H "Content-Type: application/json" \
          -d "$SUMMARY" 2>&1)
        pxe_log INFO hardware "hardware response: $HW_RESP"
    else
        pxe_log WARN hardware "SUMMARY generation failed"
    fi
else
    pxe_log WARN hardware "collection returned empty, COMPONENTS_JSON=$COMPONENTS_JSON"
fi

# === Upload log & complete ===
pxe_event install_completing "uploading log"
pxe_log INFO complete "post-install finished"

if [ -n "$SN" ] && [ "$SN" != "NotSpecified" ] && [ "$SN" != "Not Specified" ]; then
    for i in $(seq 1 30); do
        curl -s --connect-timeout 3 "${API_URL}/api/system/status" > /dev/null 2>&1 && break
        sleep 2
    done
    curl -s --connect-timeout 10 --retry 3 -X POST "${API_URL}/api/provision/complete" \
      -H "Content-Type: application/json" \
      -d "{\"sn\":\"${SN}\",\"log\":$(python3 -c "import json;print(json.dumps(open('$LOG_FILE').read()[:10000]))" 2>/dev/null || echo '""')}"
fi
