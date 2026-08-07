# Tasks API — 装机任务

## POST /api/tasks — 创建任务

### 必填

| 字段 | 类型 | 说明 |
|------|------|------|
| sn | string | 序列号，唯一 |
| hostname | string | 装机后主机名 |
| ip | string | 目标 IP |
| mac | string | PXE 网口 MAC (`xx:xx:xx:xx:xx:01`) |
| os | string | os_templates.bid，如 `tpl-euler03x64-std` |

### 重要（有默认值但建议显式设）

| 字段 | 类型 | 默认 | 说明 |
|------|------|------|------|
| disk_target_size | int | 480 | 目标磁盘大小 GB。**必须匹配实际磁盘** |
| root_password | string | CentOS@2026 | root 密码 |
| ssh_keys | []string | [] | 注入 /root/.ssh/authorized_keys 的公钥列表 |

### 可选

| 字段 | 类型 | 说明 |
|------|------|------|
| network | JSON string | 网络配置，`"{}"` 则 DHCP |
| partition | JSON string | 分区方案，`"{}"` 则自动 LVM |
| scripts | JSON string | 额外装机脚本，`"[]"` 无 |
| files | JSON string | 下载文件列表，`"[]"` 无 |

### network 格式

**单网卡静态 IP：**
```json
{"ip":"192.168.100.10","netmask":"255.255.255.0","gateway":"192.168.100.1","mac":"xx:xx:xx:xx:xx:01","dns":["114.114.114.114"]}
```

**Bond：**
```json
{"ip":"...","netmask":"...","gateway":"...","bond":{"slaves":["xx:xx:xx:xx:xx:01","xx:xx:xx:xx:xx:02"],"mode":4}}
```
Bond mode: 0=balance-rr 1=active-backup 2=balance-xor 3=broadcast 4=802.3ad 5=balance-tlb 6=balance-alb

**DHCP：** 不传或 `"{}"` = device=link 自动。`{"mac":"xx:xx:...:01"}` = 指定网卡 DHCP。

### partition 格式

```json
{"boot_size":1024,"efi_size":1024,"root_fstype":"xfs","use_lvm":true}
```
`"{}"` = 自动 `/boot` + `/boot/efi` + `/` LVM。

### scripts 格式

JSON 字符串数组，装机后执行：
```json
[{"name":"marker","type":"bash","content":"#!/bin/bash\necho installed > /root/done.txt"}]
```
`type`: bash/python/sh。`"[]"` = 不执行。

### files 格式

JSON 字符串数组，装完后下载到目标机：
```json
[{"url":"/fil-demo01/driver.rpm","filename":"driver.rpm","dest":"/root/driver.rpm"}]
```
`url` — PXE Engine HTTP 相对路径（文件在 `boot/http/` 下，HTTP 从 `/` 起）。
需先用 `POST /api/files/pull` 或 `/api/files/upload` 把文件放到 PXE Engine。
`"[]"` = 不下载。

### 完整示例

```json
{
  "sn": "VM-EULER-X86-01",
  "hostname": "node01",
  "ip": "192.168.100.49",
  "mac": "xx:xx:xx:xx:xx:xx",
  "os": "tpl-euler03x64-std",
  "disk_target_size": 40,
  "root_password": "MyPass@2026",
  "ssh_keys": ["ssh-ed25519 AAAA..."],
  "network": "{\"ip\":\"192.168.100.49\",\"netmask\":\"255.255.255.0\",\"gateway\":\"192.168.100.1\"}",
  "partition": "{}",
  "scripts": "[{\"name\":\"marker\",\"type\":\"bash\",\"content\":\"#!/bin/bash\\necho done > /root/done.txt\"}]",
  "files": "[{\"url\":\"/fil-demo01/driver.rpm\",\"filename\":\"driver.rpm\",\"dest\":\"/root/driver.rpm\"}]"
}
```

---

## GET /api/tasks — 列表

**Query:** `?status=pending|installing|installed|failed`

## GET /api/tasks/{sn} — 查单条

## PUT /api/tasks/{sn} — 更新

## DELETE /api/tasks/{sn} — 删除

## POST /api/tasks/batch — 批量创建
