#!/usr/bin/env python3
import json
import re
import sys
from pathlib import Path

TASKS_FILE = Path("/Users/bytedance/GolandProjects/LLM/specs/20260423-eino-cli-mvp/tasks.md")
TASK_PATTERN = re.compile(r"^- \[(?P<done>[ xX])\] (?P<id>T\d+) .*?(?P<desc>`.*`|.+)$")


def load_input() -> dict:
    try:
        return json.load(sys.stdin)
    except Exception:
        return {}


def next_incomplete_task():
    if not TASKS_FILE.exists():
        return None
    for raw in TASKS_FILE.read_text(encoding="utf-8").splitlines():
        match = TASK_PATTERN.match(raw.strip())
        if not match:
            continue
        if match.group("done").lower() == "x":
            continue
        return {
            "id": match.group("id"),
            "line": raw.strip(),
        }
    return None


def main() -> int:
    input_data = load_input()
    if input_data.get("stop_hook_active", False):
        return 0

    next_task = next_incomplete_task()
    if not next_task:
        return 0

    reason = (
        f"当前还不能停止，继续执行下一个未完成任务 {next_task['id']}。"
        f"请直接继续实现，任务定义：{next_task['line']}"
    )
    print(json.dumps({"decision": "block", "reason": reason}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
