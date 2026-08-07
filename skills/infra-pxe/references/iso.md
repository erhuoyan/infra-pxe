# ISO API — ISO 镜像管理

## GET /api/iso/list — 已有 ISO 文件

**Response:** `[{"filename", "path", "size_mb", "mounted", "distro_path", "progress", "status"}, ...]`

## POST /api/iso/download — 后台下载 ISO

**Request:**
```json
{
  "url": "http://mirrors.example.com/openEuler-22.03-LTS-SP4-aarch64-dvd.iso",
  "filename": "openEuler-...-dvd.iso"   // 可选，默认从 URL 推断
}
```

**Response:** `{"filename": "...", "status": "downloading"}`

后台异步下载，通过 `GET /api/iso/list` 查看进度（progress 字段 0-100）。

## POST /api/iso/mount — 挂载 ISO

**Request:**
```json
{
  "filename": "openEuler-22.03-LTS-SP4-x86_64-dvd.iso",   // required, ISO 文件名（相对 boot/iso/）
  "distro_path": "openeuler/openEuler-22.03-LTS-SP4/standard/x86_64"  // 可选，指定挂载位置
}
```

若不指定 `distro_path`，用文件名（去扩展名）作为挂载路径。

挂载后 ISO 内容通过 `http://worker:9200/{distro_path}/repo/` 访问。

**Response:**
```json
{
  "iso": "openEuler-...-dvd.iso",
  "distro_path": "openeuler/.../x86_64",
  "repo_path": "/joyops/infra/worker/boot/http/openeuler/.../repo",
  "status": "mounted"
}
```

## POST /api/iso/umount — 卸载

**Request:**
```json
{"distro_path": "openeuler/openEuler-22.03-LTS-SP4/standard/x86_64"}
```

按 `distro_path` 卸载（不是文件名，因为一个 ISO 可能被挂到多处）。

## GET /api/iso/mounted — 已挂载列表

**Response:** `[{distro_path, iso, repo_path}, ...]`

---

## 装机流程中 ISO 的位置

```
1. ISO 文件放到 boot/iso/ (或软链接进去)
2. POST /api/iso/mount → 挂载到 boot/http/{distro_path}/repo/
3. 目标机 PXE 启动 → HTTP 拉 http://worker:9200/{distro_path}/repo/images/pxeboot/vmlinuz
```

**distro_path 必须匹配 os_template.distro_path**，否则装机时找不到 kernel/initrd。
