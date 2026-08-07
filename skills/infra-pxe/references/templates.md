# Templates API — 装机模板 (kickstart/cloud-init/scripts)

模板存储在 PXE Engine 文件系统 `templates/` 目录，API 读写文件。

## POST /api/templates — 上传/更新模板

**Request:**
```json
{
  "name": "ks.cfg.j2",          // 文件名，支持路径: "scripts/pxe-pre.sh"
  "content": "模板内容..."
}
```

**Response:** `{"name": "ks.cfg.j2", "status": "saved"}`

## GET /api/templates — 列表

**Response:** `[{"name": "ks.cfg.j2"}, {"name": "scripts/pxe-pre.sh"}, ...]`

## GET /api/templates/{name} — 查看内容

支持子目录: `GET /api/templates/scripts/pxe-pre.sh`

**Response:** plain text (Content-Type: text/plain)

## DELETE /api/templates/{name} — 删除

---

## 模板清单

| 文件 | 用途 |
|------|------|
| ks.cfg.j2 | 通用 kickstart (CentOS/Rocky) |
| openeuler.ks.cfg.j2 | openEuler kickstart |
| centos.ks.cfg.j2 | CentOS kickstart |
| user-data.j2 | Ubuntu cloud-init autoinstall |
| menu.ipxe.j2 | iPXE 启动菜单 |
| scripts/pxe-pre.sh | kickstart %pre（分区/网络探测） |
| scripts/pxe-post.sh | kickstart %post（配网/SSH/回调） |
| scripts/pxe-pre-ubuntu.sh | Ubuntu early-commands |
| scripts/pxe-post-ubuntu.sh | Ubuntu late-commands |
| scripts/hw-collect.py | 硬件信息采集 |

模板源码位置: `src/infrakit/controller/apps/pxe/templates/`
