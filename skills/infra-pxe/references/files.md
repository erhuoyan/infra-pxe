# Files API — 文件管理 (驱动/固件等)

## GET /api/files — 文件清单

**Response:** `[{"bid": "file-001", "filename": "driver.rpm", "path": "...", "size": 1024}, ...]`

## GET /api/files/{bid}/check — 检查文件是否存在

**Response:** `{"exists": true, "size": 1024}`

## POST /api/files/upload — 上传文件

Multipart form-data 上传。

## POST /api/files/pull — 从 URL 拉取

**Request:**
```json
{
  "bid": "file-001",
  "url": "http://example.com/driver.rpm",
  "filename": "driver.rpm",
  "dest_dir": "/tmp/drivers"
}
```

**Response:** `{"bid": "file-001", "status": "downloading"}`

## DELETE /api/files/{bid} — 删除文件
