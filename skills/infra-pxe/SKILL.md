---
name: infra-pxe
description: |
  Build, deploy, seed, and operate the JoyOps Infra PXE Engine.
  Drives bare-metal provisioning: pack binary, deploy to servers, inject seed data,
  create install tasks, manage ISOs, trigger PXE boot.
  Use when user mentions "装机", "PXE", "pxe节点", "runner", "打包", "部署 pxe", "seed",
  "初始化 pxe", "推模板", "注册 OS", "配 DHCP", "创建装机任务", "ISO",
  "bare metal", "kickstart", "cloud-init", "pxe boot", or provides server SN/MAC/IP
  wanting OS installation.
---

# Infra PXE Engine

Worker = self-contained PXE engine (Go + SQLite). **所有操作通过 `infra-pxe` MCP 执行。**

**⚠️ 规则：**
- 本 Skill 所有 tool 均使用 `infra-pxe` MCP（tool 前缀 `mcp__infra_pxe_*`），**禁止调用 `joyops-infra`**
- 参数值必须来自**用户明确提供**或**其他 tool 返回值**，否则必须先问用户。禁止猜测 bid、路径、IP 等

---

## 操作流程（必须按此顺序）

```
① 部署 Worker
   └→ install.sh → 重启 session → MCP 连接

② 初始化
   ├→ seed_import（导入 OS 模板 + 脚本 + 文件元数据）
   ├→ list_interfaces → dhcp_config_update（配网段，自动启动 dnsmasq）
   └→ ISO：用户告知路径 → AI 软连接到 boot/iso/ → mount_iso

③ 物料配置（按需）
   **必须先查已有物料**：`list_scripts` + `list_files`，有现成的直接用，不要创建新的。
   只有确认没有才问用户要不要新建。
   ├→ 脚本：list_scripts 里有 → 直接 update_os_template 绑定。没有 → 问用户要内容 → create_script
   └→ 文件：list_files 里有 → 确认物理文件存在 → update_os_template 绑定。没有 → 问用户路径/bid → 软连接 + create_file

④ 校验
   └→ validate_os_template → 全部 ready 才能装机
       ├─ ✅ ready → 进入⑤
       └─ ❌ not ready → 回到②或③修复

⑤ 装机
   └→ create_task（自动继承模板的 scripts/files）→ 触发 PXE boot
```

**首次交互**：`system_status` 判断阶段 → dnsmasq 没跑从②开始；模板没 ready 从③④开始；全就绪直接⑤。

---

## 决策逻辑

### 装机

1. `system_status` → Worker 在线？dnsmasq 跑着？
2. `get_dhcp_config` → running=true？
3. `list_os_templates` → 有模板？
4. `validate_os_template` → ready=true？
5. **问用户收集信息**（必填项必问，有默认值的提醒用户可自定义）：
   - `sn` — 服务器序列号（必填）
   - `hostname` — 主机名（必填）
   - `ip` — 业务 IP（必填）
   - `os` — 模板 bid（必填，从 list_os_templates 选）
   - `disk_target_size` — 磁盘大小 GB（默认 480，必问）
   - `network` — 网络配置 JSON（必有值且含 MAC：单口 network.mac / bond bond.slaves，见下方格式）
   - DHCP 静态绑定不用在任务里配——用 worker MCP 的 `create_dhcp_binding` 单独管理
   - `root_password` — root 密码（有默认值，展示时用 *** 隐藏）
   - `ssh_keys` — SSH 公钥 JSON array（默认 []）
   - `partition` — 分区配置 JSON（默认 {}，用模板内置分区逻辑）

   **展示方式**：列出所有参数和当前默认值，让用户确认或修改。例如：
   > 以下参数将使用默认值，需要修改请告诉我：
   > - root 密码: ***（有默认值，需要修改请告诉我）
   > - SSH 公钥: 无
   > - 分区: 自动（按 disk_target_size 选盘）
6. `create_task` — scripts/files 自动从 os_template 继承，想换组合就改模板（`update_os_template`）
7. **触发 PXE boot**：
   - 物理机：`ipmitool -I lanplus -H <bmc_ip> -U <user> -P <pass> chassis bootdev pxe && ipmitool ... power reset`
   - 虚拟机：`virsh destroy <vm> ; virsh start <vm>`（需要 VM 的 boot order 设为 PXE）

#### network JSON 格式（create_task 的 network 参数，JSON 字符串）

**network 必须有值，且含 MAC（单口 network.mac / bond bond.slaves）**——无 MAC 任务无法 PXE 匹配。

**① 单口**（`mac` + `ip` 必填，`mac` 是网口 MAC 地址）：
```json
{
  "ip": "10.0.1.100",
  "netmask": "255.255.255.0",
  "gateway": "10.0.1.1",
  "dns": ["8.8.8.8"],
  "mac": "xx:xx:xx:xx:xx:xx"
}
```

**② Bond**（slaves 写 **MAC 地址**，不写网卡名；anaconda 环境网卡名与装机后不一致，脚本按 MAC 自动解析）：
```json
{
  "ip": "10.0.1.100",
  "netmask": "255.255.255.0",
  "gateway": "10.0.1.1",
  "bond": {
    "mode": 4,
    "slaves": ["xx:xx:xx:xx:xx:01", "xx:xx:xx:xx:xx:02"]
  }
}
```

**字段表**：`ip`（必填）、`netmask`（默认 255.255.254.0）、`gateway`、`dns`（list）、`mac`（单口网口 MAC）、`bond.mode`（int，4=802.3ad）、`bond.slaves`（list of MAC）、`bond.miimon`（默认 100）、`bond.lacp_rate`（默认 1）、`bond.xmit_hash_policy`（默认 layer3+4）。

**统一规则：任务的所有 MAC 都在 network JSON 里，没有顶层 mac**。单口 = `network.mac`（1 个）；bond = `network.bond.slaves`（2 个）。`/boot/mac` 匹配从 network 读：单口命中 network.mac，bond 命中两个 slaves（PXE 从任意一块口启动都能出脚本）。**DHCP 静态绑定与任务无关**——需要固定 IP 用 worker MCP `create_dhcp_binding` 单独配（`POST /api/dhcp/bindings`），任务创建/删除不碰 hostsfile。

**易错点**：`bond` 是**对象**不是布尔（`"bond": true` 会静默入库、装机时脚本崩溃）；没有 `bond_name`/`bond_mode` 字段（bond0 硬编码、802.3ad 用 `mode: 4`）。

### 去堆叠双上联 ARP 双发

非堆叠交换机环境下，bond mode 4 (LACP) 双上联到两台独立交换机时，广播/ARP 默认只从一个 slave 发出，另一台交换机学不到 bond MAC，导致部分流量被黑洞丢弃。解决方案：

1. 设置 `all_slaves_active=1`（广播/组播从所有 slave 发出）
2. 部署 `arp_dual_send` 服务（持续从每个物理 slave 发送免费 ARP）

**bond 配置（nmcli，老版本 NetworkManager 1.32.x 用 `bond.options` 字符串）：**
```bash
nmcli con add type bond con-name bond0 ifname bond0 \
  bond.options "mode=802.3ad,miimon=100,lacp_rate=1,xmit_hash_policy=layer3+4,all_slaves_active=1" \
  ip4 <IP>/<CIDR> gw4 <GATEWAY>
nmcli con add type ethernet con-name bond0-eth0 ifname eth0 master bond0 slave-type bond
nmcli con add type ethernet con-name bond0-eth1 ifname eth1 master bond0 slave-type bond
nmcli con up bond0
```

**部署文件**（见 skill 目录 `scripts/`）：
- `scripts/arp_dual_send.sh` → `/usr/local/bin/arp_dual_send.sh`
- `scripts/arp_dual_send.service` → `/usr/lib/systemd/system/arp_dual_send.service`

部署步骤：将两个文件 scp 到目标节点对应路径，然后：
```bash
chmod +x /usr/local/bin/arp_dual_send.sh
systemctl daemon-reload
systemctl enable --now arp_dual_send.service
```

也可通过 worker post-install script 批量部署。

### 初始化

1. `system_status` → 不在线则提醒部署
2. `seed_import` — 导入 OS 模板 + 脚本 + 文件
3. **问用户** PXE 网口和网段 → `dhcp_config_update`（自动算 pxe_server_ip）
4. ISO：**问用户** ISO 在目标机器的路径 → SSH 软连接 → `mount_iso`
5. `validate_os_template` 校验

### 校验

1. `list_os_templates` → 拿所有 bid
2. 每个 bid → `validate_os_template`
3. `get_dhcp_config`

展示表格**必须 6 列，缺一不可**：
```
| 模板 | template | ISO | 挂载 | 脚本 | 文件 | 状态 |
```
每列取值规则：
- "✅" — 检查通过
- "❌ 缺: xxx" — 检查失败，注明缺什么
- "—" — 该模板未绑定此类物料（scripts/files 为空）

**禁止省略脚本和文件列**，即使全部为空也要显示 "—"。

### 添加物料

**脚本**：
1. **问用户**：bid？name？脚本内容？
2. `create_script`
3. `update_os_template` 绑定 script_bids
4. `validate_os_template` 确认

**文件**（大文件）：
1. **问用户**：文件在目标机器的路径？bid？dest_dir？
2. SSH: `mkdir -p /joyops/infra/worker/boot/http/{bid} && ln -sf {路径} /joyops/infra/worker/boot/http/{bid}/`
3. `create_file` 注册元数据
4. `update_os_template` 绑定 file_bids
5. `validate_os_template` 确认

> 路径规则：`boot/http/{bid}/{filename}` → HTTP 可访问 `http://worker:9200/{bid}/{filename}`

**ISO 管理注意事项：**
- os_template 的 `iso_path` = 期望的 ISO **文件名**
- **软连接名必须匹配 `iso_path`**
- ISO 实际文件名跟模板 iso_path 不一致时，**必须问用户选择**：
  - **A. 改软连接名**（推荐，不动模板）：
    ```bash
    ln -sf /实际路径/xxx-everything.iso /joyops/infra/worker/boot/iso/{模板期望的文件名}
    ```
  - **B. 改模板**（换 OS 变体时）：`update_os_template` 改 iso_path + distro_path → 重新 mount
  - **禁止自动选择**，必须让用户决定
- `distro_path` 决定挂载位置，改了 iso_path 通常也要改 distro_path + 重新 mount

### 部署

1. 问 target IP、架构
2. `bash release/pack-worker.sh`
3. `scp + ssh install.sh`（不需要 `-i`）
4. `curl http://<ip>:9200/api/health` 验证
5. 提醒：重启 session 连接 MCP

### 卸载

```bash
ssh root@<ip> "/joyops/infra/worker/uninstall.sh -y"
```

---

## 提醒清单

| 状态 | 提醒 |
|------|------|
| Worker 不在线 | 部署或 `systemctl start infra-worker` |
| 没有 OS 模板 | `seed_import` |
| ISO 未挂载 | 问用户 ISO 路径 → 软连接 → `mount_iso` |
| DHCP 没配 | 问用户网段 → `dhcp_config_update` |
| dnsmasq 没跑 | `dnsmasq_start` |
| MCP 未连接 | 刚部署？重启 session |

---

## HTTP API（Agent 禁止调用）

仅系统集成使用：任务同步 (`POST /api/sync`)、健康检查 (`GET /api/health`)、文件上传 (`POST /api/files/upload`)。
