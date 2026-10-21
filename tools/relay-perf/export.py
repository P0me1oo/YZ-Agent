#!/usr/bin/env python3
"""只导出测量结果和 /proc 摘要，不读取角色配置或错误日志。"""
import argparse
import json
from pathlib import Path
import platform
import time

from run import root

parser = argparse.ArgumentParser()
parser.add_argument("--role", choices=("front", "landing", "client"), required=True)
args = parser.parse_args()
base = root()
files = {}
for f in sorted(base.iterdir()):
    if args.role == "client":
        allowed = f.name.startswith(("r1-", "r2-", "r3-", "s1-", "s2-", "s3-", "check-", "smoke-")) and f.name.endswith((".jsonl", ".process.json"))
    else:
        allowed = f.name.startswith(("r1-", "r2-", "r3-", "s1-", "s2-", "s3-")) and f.name.endswith(".monitor.jsonl")
    if allowed and f.is_file():
        files[f.name] = [json.loads(line) for line in f.read_text(encoding="utf-8").splitlines()] if f.name.endswith(".jsonl") else json.loads(f.read_text(encoding="utf-8"))
record = {"role": args.role, "exported_unix": time.time(), "kernel": platform.release(), "machine": platform.machine(), "files": files}
output = base / (args.role + ".results.json")
with output.open("x", encoding="utf-8") as f:
    json.dump(record, f, ensure_ascii=False)
print(json.dumps({"event": "exported", "file_count": len(files), "bytes": output.stat().st_size}))
