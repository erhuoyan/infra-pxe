# System API — 系统状态和管理

## GET /api/health — 健康检查

**Response:** `{"status": "ok"}`

## GET /api/status — 系统总览

**Response:**
```json
{
  "tasks_count": 3,
  "dnsmasq": {"running": true, "host_entries": 3},
  "pending_results": 0
}
```

## GET /api/interfaces — 本机网卡列表

**Response:** `[{"name": "eth0", "ipv4": "192.168.100.1"}, ...]`


## POST /api/sync — 同步配置和任务（兼容旧 Controller）

Seed 时用此接口设置 pxe_server_ip，确保渲染的 kickstart/iPXE 使用正确地址。

**Request:**
```json
{
  "pxe_server_ip": "192.168.100.1",
  "pxe_server_port": "9200",
  "tasks": [],
  "os_templates": [],
  "templates": {},
  "sync_version": 1
}
```

`pxe_server_ip` 和 `pxe_server_port` 影响所有渲染输出中的 `http_server` 变量。
值从 `GET /api/interfaces` 获取 PXE 网口的 IP。
其余字段（tasks/os_templates/templates）可传空数组——已经通过 CRUD API 推送过了。
## POST /api/dnsmasq/start — 启动 dnsmasq

## POST /api/dnsmasq/stop — 停止 dnsmasq

## POST /api/dnsmasq/reload — 重载配置

触发重新生成 hostsfile 并 SIGHUP dnsmasq。

## GET /api/results — 装机历史

**Query:** `?sn=SRV001&limit=100`

**Response:** `[{"id", "sn", "status", "completed_at", "install_log"}, ...]`

## GET /api/results/{sn} — 按 SN 查历史

## POST /api/shutdown — 关闭 PXE Engine

慎用。停止 dnsmasq 并退出进程。
