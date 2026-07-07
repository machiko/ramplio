#!/usr/bin/env python3
"""硬界線:擋掉在 main 分支上直接 git commit(合併請走 feature branch + git merge)。"""
import json
import re
import subprocess
import sys


def main() -> None:
    payload = json.load(sys.stdin)
    command = payload.get("tool_input", {}).get("command", "")
    # 只在「指令位置」比對(行首或 ; && || | 之後),避免誤擋參數字串裡剛好含 git commit 的指令
    if not re.search(r"(?:^|[;&|]\s*)git\s+(?:-\S+\s+|-C\s+\S+\s+)*commit\b", command):
        sys.exit(0)

    cwd = payload.get("cwd", ".")
    result = subprocess.run(
        ["git", "-C", cwd, "branch", "--show-current"],
        capture_output=True, text=True, timeout=10,
    )
    branch = result.stdout.strip()
    if branch == "main":
        print("[Hook] BLOCKED: 禁止在 main 分支直接 commit。", file=sys.stderr)
        print("[Hook] 請開 feat/ 或 fix/ 或 chore/ 分支,完成後由使用者決定合併。", file=sys.stderr)
        sys.exit(2)
    sys.exit(0)


if __name__ == "__main__":
    main()
