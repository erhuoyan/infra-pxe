# Validate — 校验 Worker 就绪状态

Worker seed 完之后、创建装机任务之前，必须校验整条链路完整。

## 校验流程

### 1. OS 模板校验

对 `GET /api/os-templates` 返回的每个 OS 模板，逐项检查：

```
对每个 os_template:
  ├── bid 存在 ✓
  ├── distro_path 不为空 ✓
  ├── boot_type 是 kickstart 或 cloud-init ✓
  ├── template 字段引用的文件 → GET /api/templates/{template} 能拿到 ✓
  ├── ISO 是否挂载:
  │   ├── GET /api/iso/mounted → 检查 distro_path 对应的 ISO 已挂载
  │   └── 或 HTTP 可达: curl http://worker:9200/{distro_path}/repo/ 返回 200
  └── boot_type == kickstart:
      ├── {distro_path}/repo/images/pxeboot/vmlinuz 可访问
      └── {distro_path}/repo/images/pxeboot/initrd.img 可访问
      boot_type == cloud-init:
      ├── {distro_path}/repo/casper/vmlinuz 可访问
      └── {distro_path}/repo/casper/initrd 可访问
```

### 2. 模板文件校验

```
GET /api/templates → 列出所有模板文件

必须存在:
  ├── menu.ipxe.j2           (iPXE 菜单)
  ├── 至少一个 ks 模板 (ks.cfg.j2 / openeuler.ks.cfg.j2 / centos.ks.cfg.j2)
  │   └── 或 user-data.j2 (如果有 cloud-init OS)
  └── scripts/:
      ├── pxe-pre.sh         (kickstart 需要)
      ├── pxe-post.sh        (kickstart 需要)
      ├── pxe-pre-ubuntu.sh  (cloud-init 需要)
      ├── pxe-post-ubuntu.sh (cloud-init 需要)
      └── hw-collect.py      (硬件采集)

对每个已注册 OS 的 template 字段:
  └── GET /api/templates/{template} → 200 且内容非空
```

### 3. ISO / 安装源校验

```
GET /api/iso/list    → 已有 ISO 文件
GET /api/iso/mounted → 已挂载列表

对每个 os_template.distro_path:
  └── curl http://worker:9200/{distro_path}/repo/ → 200
      kickstart:
        curl http://worker:9200/{distro_path}/repo/images/pxeboot/vmlinuz → 200
      cloud-init:
        curl http://worker:9200/{distro_path}/repo/casper/vmlinuz → 200
```

如果访问失败 → 提示用户需要:
1. 下载 ISO: `POST /api/iso/download {"url": "..."}`
2. 挂载 ISO: `POST /api/iso/mount {"iso_path": "xxx.iso"}`

### 4. DHCP 校验

```
GET /api/dhcp/config → 检查:
  ├── running == true (dnsmasq 在跑)
  ├── interface 不为空
  ├── dhcp_start / dhcp_end 不为空
  └── gateway 不为空
```

### 5. 文件管理校验（如果 OS 模板需要额外文件）

```
GET /api/files → 已注册文件列表

对每个文件:
  └── GET /api/files/{bid}/check → exists == true
```

### 6. 脚本可达性校验

```
对 scripts/ 下的每个脚本:
  └── curl http://worker:9200/api/pxe/scripts/{name} → 200 且内容非空

必须通过:
  ├── /api/pxe/scripts/pxe-pre.sh
  ├── /api/pxe/scripts/pxe-post.sh
  ├── /api/pxe/scripts/pxe-pre-ubuntu.sh
  ├── /api/pxe/scripts/pxe-post-ubuntu.sh
  └── /api/pxe/scripts/hw-collect.py
```

## 校验输出格式

```
=== Worker Validation: http://worker:9200 ===

[OS Templates]
  ✓ tpl-euler03x64-std — openeuler.ks.cfg.j2 存在, ISO 已挂载, vmlinuz 可达
  ✓ tpl-rocky98x64 — ks.cfg.j2 存在, ISO 已挂载, vmlinuz 可达
  ✗ tpl-ubuntu2204-x64 — user-data.j2 存在, ISO 未挂载!

[Templates]
  ✓ ks.cfg.j2
  ✓ openeuler.ks.cfg.j2
  ✓ menu.ipxe.j2
  ✓ scripts/pxe-pre.sh
  ✗ scripts/hw-collect.py — 文件为空!

[DHCP]
  ✓ running, range 192.168.100.100-200

[Scripts HTTP]
  ✓ pxe-pre.sh (10.5KB)
  ✓ pxe-post.sh (15.8KB)
  ✗ hw-collect.py — 404

[Summary]
  Ready: 3/5 OS templates
  Issues: 2 (需要挂载 ubuntu ISO, hw-collect.py 缺失)
```

## 何时触发校验

- Seed 完成后自动跑一次
- 用户说"检查 worker"/"校验"/"validate"
- 创建装机任务前，对选中的 OS 做单条校验
