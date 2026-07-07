#!/usr/bin/env python3
"""驗證迴路:.go 檔編輯後自動 gofmt -w,並以 go vet 回饋問題(今天 19 檔格式債的教訓)。"""
import json
import os
import subprocess
import sys


def main() -> None:
    payload = json.load(sys.stdin)
    file_path = payload.get("tool_input", {}).get("file_path", "")
    if not file_path.endswith(".go") or not os.path.isfile(file_path):
        sys.exit(0)

    subprocess.run(["gofmt", "-w", file_path], capture_output=True, timeout=30)

    pkg_dir = os.path.dirname(file_path) or "."
    vet = subprocess.run(
        ["go", "vet", "./" + os.path.relpath(pkg_dir, payload.get("cwd", "."))],
        capture_output=True, text=True, timeout=120, cwd=payload.get("cwd", "."),
    )
    if vet.returncode != 0:
        print(f"[Hook] go vet 發現問題:\n{vet.stderr.strip()}", file=sys.stderr)
        sys.exit(2)
    sys.exit(0)


if __name__ == "__main__":
    main()
