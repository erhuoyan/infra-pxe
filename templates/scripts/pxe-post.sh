#!/bin/bash
# PXE Station — %post script for anaconda kickstart
# Downloaded and executed during %post phase
# Requires: API_URL, LOG_FILE set by caller
#
# IMPORTANT: Network (bond/static IP) is already configured by anaconda
# via %pre-generated network.ks. This script only:
# 1. Removes DHCP leftovers that conflict with the static config
# 2. Sets hostname
# 3. Reports back to PXE server

export TZ=UTC

pxe_log() {
    local level="$1" stage="$2" msg="$3"
    echo "[$(date +%Y-%m-%dT%H:%M:%S)] [$level] [$stage] $msg" >> $LOG_FILE
}

pxe_event() {
    local stage="$1" detail="$2"
    curl -s --connect-timeout 3 -X POST "${API_URL}/api/pxe/event" \
      -H "Content-Type: application/json" \
      -d "{\"sn\":\"${SN}\",\"stage\":\"${stage}\",\"detail\":\"${detail}\"}" 2>/dev/null || true
}

# === Identify SN (prefer provision.json from %pre, fallback to DMI) ===
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
pxe_log INFO post_start "entering %post, SN=$SN"

# === Load provision config ===
if [ -n "$SN" ] && [ "$SN" != "NotSpecified" ] && [ "$SN" != "Not Specified" ]; then
    RESP=$(curl -s --connect-timeout 10 --retry 3 "${API_URL}/api/provision/by-sn/${SN}")
    echo "$RESP" | python3 -c "import json,sys;r=json.load(sys.stdin);d=r.get('data') or r;json.dump(d,open('/tmp/provision.json','w'))" 2>/dev/null
    if ! python3 -c "import json;d=json.load(open('/tmp/provision.json'));assert d.get('sn')" 2>/dev/null; then
        pxe_log WARN provision "curl failed in %post, using copied provision.json"
        cp /var/lib/pxe-provision.json /tmp/provision.json 2>/dev/null
    fi
else
    cp /var/lib/pxe-provision.json /tmp/provision.json 2>/dev/null
fi

# === Set hostname ===
PROV_HOSTNAME=$(python3 -c "import json;print(json.load(open('/tmp/provision.json')).get('hostname', 'localhost'))" 2>/dev/null)
[ -n "$PROV_HOSTNAME" ] && hostnamectl set-hostname "$PROV_HOSTNAME"

# === Clean DHCP leftovers (preserve bond/static config from anaconda) ===

python3 << 'PYEOF'
import json, subprocess, os, glob
from datetime import datetime

LOG_FILE = os.environ.get('LOG_FILE', '/var/log/pxe-post.log')

def log(level, stage, msg):
    ts = datetime.now().strftime('%Y-%m-%dT%H:%M:%S')
    with open(LOG_FILE, 'a') as f:
        f.write(f'[{ts}] [{level}] [{stage}] {msg}\n')

def run(cmd):
    r = subprocess.run(cmd, shell=True, capture_output=True, text=True)
    if r.returncode != 0:
        log('ERROR', 'nmcli', f'cmd={cmd} rc={r.returncode} stderr={r.stderr.strip()}')
    return r

try:
    d = json.load(open('/tmp/provision.json'))
except Exception as e:
    log('ERROR', 'network', f'failed to parse provision.json: {e}')
    raise SystemExit(0)

net = d.get('network')
if not net:
    log('WARN', 'network', 'no network config, skipping cleanup')
    raise SystemExit(0)

bond = net.get('bond')
mac = (net.get('mac') or '').lower()

# Build whitelist of connections to KEEP
keep_names = set()
if bond:
    keep_names.add('bond0')
    # Bond slave connections — anaconda names them like "bond0-slave-eth0" or interface names
    for s in bond.get('slaves', []):
        keep_names.add(s.lower())
        # anaconda may use various naming patterns
        keep_names.add(f'bond0-slave-{s.lower()}')
        keep_names.add(f'bond-slave-{s.lower()}')
else:
    # Single NIC — find interface name by MAC
    target_if = ''
    for path in glob.glob('/sys/class/net/*/address'):
        ifname = path.split('/')[-2]
        addr = open(path).read().strip().lower()
        if mac and addr == mac:
            target_if = ifname
            break
    if target_if:
        keep_names.add(target_if)
    # Also keep connections named with the MAC (anaconda style)
    if mac:
        keep_names.add(mac)

# Always keep loopback
keep_names.add('lo')

log('INFO', 'network', f'whitelist (keep): {keep_names}')

# List all NM connections
result = subprocess.run('nmcli -t -f NAME,TYPE,DEVICE con show', shell=True, capture_output=True, text=True)
log('INFO', 'network', f'existing connections: {result.stdout.strip()}')

# Delete connections NOT in whitelist
removed = []
for line in result.stdout.strip().split('\n'):
    if not line.strip():
        continue
    parts = line.split(':')
    con_name = parts[0] if parts else ''
    if not con_name:
        continue

    # Check if this connection should be kept
    should_keep = False
    con_lower = con_name.lower()
    for keep in keep_names:
        if keep in con_lower or con_lower in keep:
            should_keep = True
            break
    # Also keep if bond-related
    if bond and 'bond' in con_lower:
        should_keep = True

    if not should_keep:
        run(f'nmcli con del "{con_name}"')
        removed.append(con_name)

# Clean DHCP ifcfg files (but keep target connection files)
for f in glob.glob('/etc/sysconfig/network-scripts/ifcfg-*'):
    basename = os.path.basename(f).replace('ifcfg-', '')
    if basename == 'lo':
        continue
    if basename.lower() in keep_names:
        continue
    if bond and 'bond' in basename.lower():
        continue
    try:
        content = open(f).read()
        if 'BOOTPROTO=dhcp' in content or 'BOOTPROTO="dhcp"' in content:
            os.remove(f)
            log('DEBUG', 'network', f'removed DHCP ifcfg: {basename}')
    except Exception:
        pass

log('INFO', 'network', f'removed connections: {removed}')

# Log final state
r = subprocess.run('nmcli con show', shell=True, capture_output=True, text=True)
log('INFO', 'network', f'final connections:\n{r.stdout.strip()}')
PYEOF

nmcli con reload 2>/dev/null
pxe_log INFO network "network cleanup complete"

# === System tweaks ===
echo > /etc/motd && echo > /etc/issue && echo > /etc/issue.net
sed -i 's/^#\?PermitRootLogin.*/PermitRootLogin yes/' /etc/ssh/sshd_config

# === Inject SSH keys ===
SSH_KEYS=$(python3 -c "
import json
d = json.load(open('/var/lib/pxe-provision.json'))
keys = d.get('ssh_keys', [])
for k in keys:
    print(k)
" 2>/dev/null)
if [ -n "$SSH_KEYS" ]; then
    mkdir -p /root/.ssh
    chmod 700 /root/.ssh
    echo "$SSH_KEYS" > /root/.ssh/authorized_keys
    chmod 600 /root/.ssh/authorized_keys
    KEY_COUNT=$(echo "$SSH_KEYS" | wc -l)
    pxe_log INFO ssh_keys "injected $KEY_COUNT SSH keys"
    pxe_event ssh_keys_injected "$KEY_COUNT keys"
else
    pxe_log INFO ssh_keys "no SSH keys to inject"
fi

# === Configure PXE local repo (before custom scripts that may dnf install) ===
# Disable all system repos that require internet access, use only PXE local source.
if [ -n "$PXE_REPO_URL" ]; then
    if curl -sf --connect-timeout 5 "${PXE_REPO_URL}/repodata/repomd.xml" -o /dev/null 2>/dev/null; then
        # Disable all existing repos (they likely need internet we don't have)
        if command -v dnf >/dev/null 2>&1; then
            for repo_file in /etc/yum.repos.d/*.repo; do
                [ -f "$repo_file" ] && sed -i 's/^enabled=1/enabled=0/' "$repo_file"
            done
        fi
        # Add PXE local repo as the only enabled source
        printf '[pxe-local]\nname=PXE Local Repo\nbaseurl=%s\nenabled=1\ngpgcheck=0\n' \
            "$PXE_REPO_URL" > /etc/yum.repos.d/pxe-local.repo
        dnf clean all >/dev/null 2>&1 || yum clean all >/dev/null 2>&1 || true
        pxe_log INFO repo "configured PXE local repo (disabled remote repos): $PXE_REPO_URL"
    else
        pxe_log WARN repo "PXE repo not reachable ($PXE_REPO_URL), using system default repos"
    fi
else
    pxe_log WARN repo "PXE_REPO_URL not set, skipping local repo config"
fi

# === Download inject files (drivers, packages from file library) ===
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
    mkdir -p /tmp/drivers
    echo "$INJECT_FILES" | while IFS='|' read -r url dest; do
        if [ -z "$url" ] || [ -z "$dest" ]; then
            pxe_log WARN inject_files "skipping empty entry (url='$url', dest='$dest') — check task files JSON format"
            continue
        fi
        pxe_log INFO inject_files "downloading ${API_URL}${url} → $dest"
        mkdir -p "$(dirname "$dest")"
        if curl -sf --connect-timeout 10 --retry 3 "${API_URL}${url}" -o "$dest"; then
            pxe_log INFO inject_files "downloaded $url ($(stat -c%s "$dest" 2>/dev/null || echo '?') bytes)"
        else
            pxe_log WARN inject_files "failed to download ${API_URL}${url} → $dest (curl exit=$?)"
        fi
    done
    pxe_log INFO inject_files "download complete"
fi

# === Execute custom scripts (per-script interpreter) ===
if [ -f /var/lib/pxe-provision.json ]; then
    SCRIPTS_JSON=$(python3 -c "
import json
d = json.load(open('/var/lib/pxe-provision.json'))
scripts = d.get('scripts', [])
# Backward compat: if no scripts array but has post_script, wrap it
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
import json, subprocess, sys

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
    # Also append summary to main log
    with open(log_file) as lf:
        tail = lf.read()[-2000:]  # last 2KB
    print(f'[script] {name} exit={result.returncode}', flush=True)
    if tail.strip():
        print(f'[script-output] {name}:\n{tail}', flush=True)
    if result.returncode != 0:
        print(f'[script] {name} FAILED (exit={result.returncode})', file=sys.stderr)
    import os
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

# Download standalone hw-collect.py (can also be run manually for debugging)
curl -sf --connect-timeout 10 --retry 3 \
  "${API_URL}/api/pxe/scripts/hw-collect.py" -o /tmp/hw-collect.py

if [ -f /tmp/hw-collect.py ]; then
    COMPONENTS_JSON=$(python3 /tmp/hw-collect.py 2>/tmp/hw-collect.log)
else
    pxe_log WARN hardware "failed to download hw-collect.py, using minimal inline fallback"
    COMPONENTS_JSON=$(python3 -c "
import subprocess, json, os, glob
def run(cmd):
    r = subprocess.run(cmd, shell=True, capture_output=True, text=True, timeout=30)
    return r.stdout.strip() if r.returncode == 0 else ''
components = {'cpus':[],'memory':[],'disks':[],'nics':[],'gpus':[],'psus':[],'ports':[],'system':{}}
# Minimal CPU
try:
    model = run(\"lscpu | grep 'Model name' | sed 's/.*: *//' \").split(chr(10))[0]
    sockets = int(run(\"lscpu | grep 'Socket' | awk '{print \$2}'\") or '1')
    cores = int(run(\"lscpu | grep 'Core(s) per socket' | awk '{print \$NF}'\") or '0')
    for i in range(sockets):
        components['cpus'].append({'slot':f'CPU{i}','model':model,'cores':cores,'threads':0,'freq_ghz':0,'max_freq_ghz':0,'arch':run('uname -m'),'manufacturer':'','serial_number':''})
except: pass
# Minimal disks
try:
    for dev in glob.glob('/sys/block/sd*')+glob.glob('/sys/block/nvme*'):
        name=os.path.basename(dev)
        sz=int(open(f'{dev}/size').read().strip())*512//(1024**3)
        if sz>0: components['disks'].append({'slot':name,'capacity_gb':sz,'interface':'','media':'','model':'','serial_number':'','firmware':'','manufacturer':'','form_factor':'','rpm':0,'nand_type':'','purpose':'','dev_path':f'/dev/{name}'})
except: pass
print(json.dumps(components))
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

    # Generate and report summary (including system/board info + config_summary)
    SUMMARY=$(echo "$COMPONENTS_JSON" | python3 -c "
import json, sys
c = json.load(sys.stdin)
hw = {}
hw['CPU'] = c['cpus'][0]['model'].split('\n')[0] if c.get('cpus') else ''
hw['CORES'] = sum(x['cores'] for x in c.get('cpus', []))
hw['memory_gb'] = sum(x.get('capacity_gb', 0) for x in c.get('memory', []))
hw['DISKS'] = ' | '.join(f\"{d['slot']} {d.get('capacity_gb',0)}GB {d.get('media','')}\" for d in c.get('disks', []))
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
# System info
sys_info = c.get('system', {})
hw['bios_version'] = sys_info.get('bios_version', '')
hw['bmc_version'] = sys_info.get('bmc_version', '')
hw['board_manufacturer'] = sys_info.get('board_manufacturer', '')
hw['board_product'] = sys_info.get('board_product', '')
# Config summary
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
        iface = d.get('interface', d.get('media', ''))
        key = f\"{d.get('capacity_gb',0):.0f}GB {iface}\"
        dgrp[key] = dgrp.get(key, 0) + 1
    parts.append(' + '.join(f\"{k}*{v}\" for k, v in dgrp.items()))
if gpus:
    ggrp = {}
    for g in gpus:
        vram = int(g.get('vram_gb', 0))
        key = f\"{g.get('model','GPU')} {vram}GB\" if vram else g.get('model', 'GPU')
        ggrp[key] = ggrp.get(key, 0) + 1
    parts.append(' + '.join(f\"{k}*{v}\" for k, v in ggrp.items()))
if nics:
    ngrp = {}
    for n in nics:
        sp = n.get('speed_gbps', 0)
        key = f\"{sp:.0f}G\" if sp >= 1 else f\"{int(sp*1000)}M\"
        ngrp[key] = ngrp.get(key, 0) + n.get('port_count', 1)
    parts.append(' + '.join(f\"{k}*{v}\" for k, v in ngrp.items()))
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
pxe_log INFO complete "post-install finished, uploading log"

if [ -n "$SN" ] && [ "$SN" != "NotSpecified" ] && [ "$SN" != "Not Specified" ]; then
    for i in $(seq 1 30); do
        curl -s --connect-timeout 3 "${API_URL}/api/system/status" > /dev/null 2>&1 && break
        sleep 2
    done
    curl -s --connect-timeout 10 --retry 3 -X POST "${API_URL}/api/provision/complete" \
      -H "Content-Type: application/json" \
      -d "{\"sn\":\"${SN}\",\"log\":$(python3 -c "import json;print(json.dumps(open('$LOG_FILE').read()[:10000]))" 2>/dev/null || echo '""')}"
fi
