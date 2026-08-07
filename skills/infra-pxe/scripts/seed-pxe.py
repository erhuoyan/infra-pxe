#!/usr/bin/env python3
"""
seed-pxe.py — 触发 PXE Engine 导入自带 seed 数据

用法:
    ./seed-pxe.py <pxe_url>                   # 默认不覆盖已有
    ./seed-pxe.py <pxe_url> --overwrite       # 强制覆盖

PXE Engine 部署后自带 seeds/ 和 templates/ 目录，此脚本只是调用
POST /api/seed/import 让 PXE Engine 把本地文件导入 DB。

与 Controller 配套使用时无需 seed — Controller 会通过 /api/os-templates
和 /api/templates 接口按需推送。此脚本用于独立 PXE Engine 场景。
"""
from __future__ import annotations

import argparse
import json
import sys
import urllib.error
import urllib.request


def main() -> int:
    ap = argparse.ArgumentParser(description="Trigger PXE Engine seed import.")
    ap.add_argument("pxe_url", help="e.g. http://192.168.1.240:9200")
    ap.add_argument("--overwrite", action="store_true", help="overwrite existing OS templates")
    args = ap.parse_args()

    url = args.pxe_url.rstrip("/") + "/api/seed/import"
    if args.overwrite:
        url += "?overwrite=true"

    print(f"── Triggering seed import: {url}")
    req = urllib.request.Request(url, data=b"", method="POST",
                                headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            resp = json.loads(r.read())
    except urllib.error.HTTPError as e:
        body = e.read().decode(errors="replace")
        print(f"❌ HTTP {e.code}: {body}", file=sys.stderr)
        return 1
    except Exception as e:
        print(f"❌ {e}", file=sys.stderr)
        return 1

    data = resp.get("data", resp)
    tpl = data.get("templates", {})
    os_r = data.get("os", {})

    print(f"  templates_dir: {data.get('templates_dir', '?')}")
    print(f"  seeds_dir:     {data.get('seeds_dir', '?')}")
    print(f"  templates: {tpl.get('added', 0)} on disk")
    print(f"  os: +{os_r.get('added', 0)} added, ~{os_r.get('updated', 0)} updated, "
          f"={os_r.get('skipped', 0)} skipped")

    errors = tpl.get("errors", []) + os_r.get("errors", [])
    if errors:
        print(f"\n⚠️  Errors ({len(errors)}):")
        for e in errors:
            print(f"    {e}")
        return 1

    print("\n✅ Seed 完成")
    return 0


if __name__ == "__main__":
    sys.exit(main())
