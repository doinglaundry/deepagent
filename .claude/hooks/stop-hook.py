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
    "last_pushed_phase": "",
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


def load_state(repo_root: Path) -> tuple[dict[str, Any], Path]:
    state_path = repo_root / ".claude" / "continuation-state.json"
    if not state_path.exists():
        return dict(DEFAULT_STATE), state_path
    try:
        data = json.loads(state_path.read_text(encoding="utf-8"))
    except Exception:
        return dict(DEFAULT_STATE), state_path
    state = dict(DEFAULT_STATE)
    if isinstance(data, dict):
        state.update(data)
    return state, state_path


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


def parse_phase_name(tasks: list[dict[str, Any]], task_id: str) -> str:
    for task in tasks:
        if task["id"] == task_id:
            return str(task.get("phase", "")).strip()
    return ""


def normalize_phase_name(phase: str) -> str:
    return re.sub(r"[^A-Za-z0-9_.-]+", "_", phase).strip("_")


def phase_has_changes(repo_root: Path) -> bool:
    try:
        result = subprocess.run(
            ["git", "-C", str(repo_root), "status", "--short"],
            capture_output=True,
            text=True,
            check=False,
        )
    except Exception:
        return False

    for raw in result.stdout.splitlines():
        line = raw.strip()
        if not line:
            continue
        if line.startswith("?? "):
            path = line[3:]
        else:
            if len(line) < 3:
                continue
            path = line[3:]
        if path == ".claude/continuation-state.json":
            continue
        return True
    return False


def run_git(repo_root: Path, args: list[str]) -> tuple[bool, str]:
    result = subprocess.run(
        ["git", "-C", str(repo_root), *args],
        capture_output=True,
        text=True,
        check=False,
    )
    output = (result.stdout + "\n" + result.stderr).strip()
    return result.returncode == 0, output


def auto_commit_push_phase(repo_root: Path, state: dict[str, Any], tasks: list[dict[str, Any]]) -> tuple[bool, str]:
    task_id = str(state.get("current_task_id", "")).strip()
    if not task_id:
        return True, ""

    phase_name = parse_phase_name(tasks, task_id)
    if not phase_name:
        return True, ""

    if not phase_complete(tasks, task_id):
        return True, ""

    phase_key = normalize_phase_name(phase_name)
    if not phase_key:
        return True, ""

    if str(state.get("last_pushed_phase", "")) == phase_key:
        return True, ""

    if not phase_has_changes(repo_root):
        state["last_pushed_phase"] = phase_key
        return True, ""

    ok, out = run_git(repo_root, ["add", "-A"])
    if not ok:
        return False, f"phase auto-commit 失败（git add）: {out}"

    commit_message = (
        f"Complete {phase_name}\n\n"
        "Co-Authored-By: Claude Opus 4.7 <noreply@anthropic.com>"
    )
    ok, out = run_git(repo_root, ["commit", "-m", commit_message])
    if not ok:
        return False, f"phase auto-commit 失败（git commit）: {out}"

    ok, out = run_git(repo_root, ["push"])
    if not ok:
        return False, f"phase auto-push 失败（git push）: {out}"

    state["last_pushed_phase"] = phase_key
    return True, ""


def save_state(state_path: Path, state: dict[str, Any]) -> None:
    state_path.parent.mkdir(parents=True, exist_ok=True)
    state_path.write_text(json.dumps(state, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def main() -> int:
    payload = load_input()
    if payload.get("stop_hook_active", False):
        return 0

    repo_root = repo_root_for(derive_cwd(payload))
    state, state_path = load_state(repo_root)
    tasks_file = locate_tasks_file(repo_root, state)
    if tasks_file is None or not tasks_file.exists():
        return 0

    tasks = parse_tasks(tasks_file)
    ok, err = auto_commit_push_phase(repo_root, state, tasks)
    if not ok:
        print(json.dumps({"decision": "block", "reason": err}, ensure_ascii=False))
        return 0

    try:
        save_state(state_path, state)
    except Exception as exc:
        print(json.dumps({"decision": "block", "reason": f"写入 continuation-state 失败: {exc}"}, ensure_ascii=False))
        return 0

    allow, reason = should_allow(state, tasks)
    if allow:
        return 0

    print(json.dumps({"decision": "block", "reason": reason}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
