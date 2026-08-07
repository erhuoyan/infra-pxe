# DHCP API — DHCP 配置和静态绑定

## PUT /api/dhcp/config — 配置 DHCP

**Request:**
```json
{
  "interface": "eth0",
  "dhcp_start": "192.168.100.100",
  "dhcp_end": "192.168.100.200",
  "netmask": "255.255.255.0",
  "gateway": "192.168.100.1",
  "dns": "8.8.8.8",
  "lease_time": "5m",
  "enable_dns": false
}
```

写入 dnsmasq.conf 并重启 dnsmasq 进程。

**Response:** 生成的配置内容

## GET /api/dhcp/config — 查看当前配置

**Response:** 当前 DHCP 参数

## GET /api/dhcp/leases — 当前租约

**Response:** `[{"mac": "aa:bb:...", "ip": "192.168.100.101", "hostname": "node01", "expires": "..."}, ...]`

## GET /api/dhcp/bindings — 静态绑定列表

**Response:** `[{"mac", "ip", "hostname"}, ...]`

## POST /api/dhcp/bindings — 添加静态绑定

**Request:**
```json
{"mac": "xx:xx:xx:xx:xx:xx", "ip": "192.168.100.10", "hostname": "node01"}
```

## DELETE /api/dhcp/bindings/{mac} — 删除绑定

MAC 格式: 冒号分隔小写 `xx:xx:xx:xx:xx:xx`
