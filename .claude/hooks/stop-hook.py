#!/usr/bin/env python3
import json
import re
import subprocess
import sys
from pathlib import Path
from typing import Any, Optional

TASK_PATTERN = re.compile(r"^- \[(?P<done>[ xX])\] (?P<id>T\d+) .*?(?P<desc>`.*`|.+)$")
PHASE_PATTERN = re.compile(r"^## Phase\s+\d+:")
CHECKPOINT_PATTERN = re.compile(r"^\*\*Checkpoint\*\*:")
DEFAULT_STATE = {
    "version": 1,
    "mode": "auto_continue",
    "phase": "executing",
    "active_spec": "",
    "current_task_id": "",
    "last_completed_task_id": "",
    "allow_stop_reason": "",
}


def load_input() -> dict[str, Any]:
    try:
        return json.load(sys.stdin)
    except Exception:
        return {}


def first_path(*values: Any) -> str:
    for value in values:
        if isinstance(value, str) and value.strip():
            return value.strip()
    return ""


def derive_cwd(payload: dict[str, Any]) -> Path:
    candidates = [
        payload.get("cwd"),
        payload.get("workspace"),
    ]
    session = payload.get("session")
    if isinstance(session, dict):
        candidates.extend([session.get("cwd"), session.get("workspace")])
    transcript_path = payload.get("transcript_path")
    if isinstance(transcript_path, str) and transcript_path.strip():
        candidates.append(str(Path(transcript_path).expanduser().resolve().parent))
    candidate = first_path(*candidates)
    if candidate:
        return Path(candidate).expanduser().resolve()
    return Path.cwd().resolve()


def repo_root_for(path: Path) -> Path:
    probe = path if path.is_dir() else path.parent
    try:
        result = subprocess.run(
            ["git", "-C", str(probe), "rev-parse", "--show-toplevel"],
            check=True,
            capture_output=True,
            text=True,
        )
        return Path(result.stdout.strip()).resolve()
    except Exception:
        current = probe.resolve()
        for candidate in [current, *current.parents]:
            if (candidate / ".git").exists():
                return candidate
        return current


def load_state(repo_root: Path) -> dict[str, Any]:
    state_path = repo_root / ".claude" / "continuation-state.json"
    if not state_path.exists():
        return dict(DEFAULT_STATE)
    try:
        data = json.loads(state_path.read_text(encoding="utf-8"))
    except Exception:
        return dict(DEFAULT_STATE)
    state = dict(DEFAULT_STATE)
    if isinstance(data, dict):
        state.update(data)
    return state


def locate_tasks_file(repo_root: Path, state: dict[str, Any]) -> Optional[Path]:
    active_spec = state.get("active_spec")
    if isinstance(active_spec, str) and active_spec.strip():
        candidate = (repo_root / active_spec).resolve()
        if candidate.exists():
            return candidate
    matches = sorted((repo_root / "specs").glob("*/tasks.md")) if (repo_root / "specs").exists() else []
    if not matches:
        return None
    return matches[0]


def parse_tasks(tasks_file: Path) -> list[dict[str, Any]]:
    tasks: list[dict[str, Any]] = []
    current_phase = ""
    pending_boundary = False
    for raw in tasks_file.read_text(encoding="utf-8").splitlines():
        line = raw.strip()
        if PHASE_PATTERN.match(line):
            current_phase = line
            pending_boundary = False
            continue
        if CHECKPOINT_PATTERN.match(line):
            pending_boundary = True
            continue
        match = TASK_PATTERN.match(line)
        if not match:
            continue
        tasks.append(
            {
                "id": match.group("id"),
                "line": line,
                "done": match.group("done").lower() == "x",
                "phase": current_phase,
                "boundary_after": pending_boundary,
            }
        )
        pending_boundary = False
    return tasks


def first_incomplete_task(tasks: list[dict[str, Any]]) -> Optional[dict[str, Any]]:
    for task in tasks:
        if not task["done"]:
            return task
    return None


def phase_complete(tasks: list[dict[str, Any]], task_id: str) -> bool:
    target_phase = ""
    for task in tasks:
        if task["id"] == task_id:
            target_phase = task["phase"]
            break
    if not target_phase:
        return False
    for task in tasks:
        if task["phase"] == target_phase and not task["done"]:
            return False
    return True


def should_allow(state: dict[str, Any], tasks: list[dict[str, Any]]) -> tuple[bool, str]:
    mode = str(state.get("mode", "auto_continue"))
    phase = str(state.get("phase", "executing"))
    next_task = first_incomplete_task(tasks)

    if mode == "all_done" or phase in {"waiting_user", "finished"}:
        return True, str(state.get("allow_stop_reason", ""))
    if next_task is None:
        return True, str(state.get("allow_stop_reason", ""))

    if mode == "pause_at_boundary":
        current_task_id = str(state.get("current_task_id", ""))
        if phase == "boundary_ready" or (current_task_id and phase_complete(tasks, current_task_id)):
            return True, str(state.get("allow_stop_reason", ""))
        return False, (
            f"当前还不能停止，继续执行当前阶段内的未完成任务 {next_task['id']}。"
            f"请直接继续实现，任务定义：{next_task['line']}"
        )

    return False, (
        f"当前还不能停止，继续执行下一个未完成任务 {next_task['id']}。"
        f"请直接继续实现，任务定义：{next_task['line']}"
    )


def main() -> int:
    payload = load_input()
    if payload.get("stop_hook_active", False):
        return 0

    repo_root = repo_root_for(derive_cwd(payload))
    state = load_state(repo_root)
    tasks_file = locate_tasks_file(repo_root, state)
    if tasks_file is None or not tasks_file.exists():
        return 0

    tasks = parse_tasks(tasks_file)
    allow, reason = should_allow(state, tasks)
    if allow:
        return 0

    print(json.dumps({"decision": "block", "reason": reason}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
