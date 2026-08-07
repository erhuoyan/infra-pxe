# OS Templates API — 操作系统模板

## POST /api/os-templates — 创建/更新

**Request:**
```json
{
  "bid": "tpl-euler03a64-std",   // required, primary key
  "label": "openEuler 22.03 ARM64",
  "distro_path": "openeuler/openEuler-22.03-LTS-SP4/standard/arm64",
  "distro_family": "openeuler",  // openeuler|centos|rocky|ubuntu
  "boot_type": "kickstart",      // kickstart|cloud-init
  "kernel_args": "inst.geoloc=0",
  "iso_path": "openEuler-22.03-LTS-SP4-aarch64-dvd.iso",
  "mirror_url": "",
  "template": "openeuler.ks.cfg.j2"  // 渲染用的模板文件名
}
```

**Response:** os_template object

## GET /api/os-templates — 列表

**Response:** `[{os_template}, ...]`

## GET /api/os-templates/{bid} — 查单条

## DELETE /api/os-templates/{bid} — 删除

**Response:** `{"deleted": "tpl-euler03a64-std"}`

---

## 字段说明

| 字段 | 说明 |
|------|------|
| bid | 唯一标识，如 `tpl-euler03a64-std` |
| distro_path | ISO 挂载后 HTTP 路径，拼接规则: `{distro_family}/{version}/{variant}/{arch}` |
| distro_family | 发行版族: openeuler, centos, rocky, ubuntu |
| boot_type | kickstart (RHEL 系) 或 cloud-init (Ubuntu) |
| template | 指向 templates 里的 j2 文件名 |
| kernel_args | iPXE kernel 行追加参数 |
| iso_path | ISO 文件名（用于 cloud-init url= 参数） |
| mirror_url | 在线镜像 URL（Ubuntu apt 源） |
