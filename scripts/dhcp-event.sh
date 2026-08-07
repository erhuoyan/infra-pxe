#!/bin/bash
# dhcp-event.sh — dnsmasq DHCP event hook → reports to PXE Engine API
# Args from dnsmasq: $1=action(add/del/old) $2=MAC $3=IP $4=hostname
ACTION=$1; MAC=$2; IP=$3; HOST=$4
[ -z "$MAC" ] && exit 0

NOW=$(date +%Y-%m-%dT%H:%M:%S)
WORKER_API="http://127.0.0.1:9200"
API_URL="${WORKER_API}/api/pxe/event"

# Try to resolve SN by MAC (provision_by-mac)
SN=""
if [ "$ACTION" = "add" ]; then
    RESP=$(curl -s --connect-timeout 3 "${WORKER_API}/api/provision/by-mac/${MAC}" 2>/dev/null)
    SN=$(echo "$RESP" | python3 -c "import sys,json;d=json.load(sys.stdin);print(d.get('sn',''))" 2>/dev/null)
fi

# Report dhcp_assigned if we have an SN
if [ -n "$SN" ] && [ "$SN" != "None" ] && [ "$SN" != "" ]; then
    curl -sf --connect-timeout 3 -X POST "${API_URL}" \
      -H "Content-Type: application/json" \
      -d "{\"sn\":\"${SN}\",\"stage\":\"dhcp_assigned\",\"detail\":\"ip=${IP} mac=${MAC}\"}" 2>/dev/null || true
fi
