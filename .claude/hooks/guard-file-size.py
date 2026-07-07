#!/usr/bin/env python3
"""硬界線:擋掉超過 800 行的程式碼檔案寫入(coding-style 規則);文件與資料檔豁免。"""
import json
import sys

MAX_LINES = 800
EXEMPT_SUFFIXES = (".md", ".json", ".txt", ".html", ".csv")


def main() -> None:
    payload = json.load(sys.stdin)
    tool_input = payload.get("tool_input", {})
    file_path = tool_input.get("file_path", "")
    content = tool_input.get("content", "")
    if not content or file_path.endswith(EXEMPT_SUFFIXES):
        sys.exit(0)

    lines = content.count("\n") + 1
    if lines > MAX_LINES:
        print(f"[Hook] BLOCKED: 檔案 {lines} 行,超過 {MAX_LINES} 行上限。", file=sys.stderr)
        print("[Hook] 請拆分成更小的模組(高內聚、低耦合)。", file=sys.stderr)
        sys.exit(2)
    sys.exit(0)


if __name__ == "__main__":
    main()
