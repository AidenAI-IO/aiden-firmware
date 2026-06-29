from __future__ import annotations

import json
import re
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

from runner.suite import Suite, TaskSpec
from skillopt.types import RolloutResult


PHASE_SCHEMA = "skillopt.phase.v1"


def write_phase_started(
    run_root: Path,
    *,
    phase: str,
    kind: str,
    suite: Suite,
    tasks: list[TaskSpec],
) -> dict[str, Any]:
    record = _base_phase_record(run_root, phase=phase, kind=kind, suite=suite, tasks=tasks)
    record["status"] = "running"
    _write_phase_record(run_root, phase, record)
    return record


def write_phase_completed(
    run_root: Path,
    *,
    phase: str,
    kind: str,
    suite: Suite,
    tasks: list[TaskSpec],
    rollouts: list[RolloutResult],
    score: Any,
) -> dict[str, Any]:
    existing = _read_phase_record(run_root, phase) or {}
    record = _base_phase_record(run_root, phase=phase, kind=kind, suite=suite, tasks=tasks)
    if existing.get("started_at"):
        record["started_at"] = existing["started_at"]
    record["status"] = "completed"
    record["finished_at"] = now_iso()
    record["score"] = _score_payload(score)
    record["tasks"] = _task_records_from_rollouts(run_root, tasks, rollouts)
    record["counts"] = _count_tasks(record["tasks"])
    raw_report = _first_raw_report(record["tasks"])
    if raw_report:
        record["raw_report"] = raw_report
    _write_phase_record(run_root, phase, record)
    return record


def write_phase_failed(
    run_root: Path,
    *,
    phase: str,
    kind: str,
    suite: Suite,
    tasks: list[TaskSpec],
    error: str,
    rollouts: list[RolloutResult] | None = None,
) -> dict[str, Any]:
    existing = _read_phase_record(run_root, phase) or {}
    record = _base_phase_record(run_root, phase=phase, kind=kind, suite=suite, tasks=tasks)
    if existing.get("started_at"):
        record["started_at"] = existing["started_at"]
    record["status"] = "failed"
    record["finished_at"] = now_iso()
    record["error"] = str(error)
    if rollouts:
        record["tasks"] = _task_records_from_rollouts(run_root, tasks, rollouts)
        record["counts"] = _count_tasks(record["tasks"])
        raw_report = _first_raw_report(record["tasks"])
        if raw_report:
            record["raw_report"] = raw_report
    _write_phase_record(run_root, phase, record)
    return record


def load_phase_records(run_root: Path) -> list[dict[str, Any]]:
    phases_dir = Path(run_root) / "phases"
    if not phases_dir.exists():
        return []
    records: list[dict[str, Any]] = []
    for path in phases_dir.glob("*.json"):
        try:
            payload = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            continue
        if isinstance(payload, dict) and payload.get("schema") == PHASE_SCHEMA:
            records.append(payload)
    records.sort(key=lambda record: _phase_sort_key(str(record.get("phase") or "")))
    return records


def latest_phase_record(run_root: Path) -> dict[str, Any] | None:
    phases_dir = Path(run_root) / "phases"
    if not phases_dir.exists():
        return None
    candidates = sorted(phases_dir.glob("*.json"), key=lambda path: path.stat().st_mtime, reverse=True)
    for path in candidates:
        try:
            payload = json.loads(path.read_text(encoding="utf-8"))
        except (OSError, json.JSONDecodeError):
            continue
        if isinstance(payload, dict) and payload.get("schema") == PHASE_SCHEMA:
            return payload
    return None


def progress_from_phase_record(record: dict[str, Any]) -> dict[str, Any]:
    tasks = [task for task in record.get("tasks", []) if isinstance(task, dict)]
    counts = _count_tasks(tasks)
    total = int(counts.get("total") or 0)
    completed = _completed_count(counts)
    running = [str(task.get("id") or "") for task in tasks if str(task.get("status") or "") == "running"]
    failed = [
        str(task.get("id") or "")
        for task in tasks
        if str(task.get("status") or "") in {"failed", "skipped", "judge_error", "timeout"}
    ]
    running = [task_id for task_id in running if task_id]
    failed = [task_id for task_id in failed if task_id]
    phase = str(record.get("phase") or "")
    summary = f"{phase}: {completed}/{total} completed"
    if running:
        summary += f", {len(running)} running ({_summarize_ids(running)})"
    if failed:
        summary += f", {len(failed)} failed ({_summarize_ids(failed)})"
    return {
        "source": "skillopt_phase",
        "phase": phase,
        "iteration": _iteration_from_phase(phase),
        "status": str(record.get("status") or ""),
        "started_tasks": completed + len(running),
        "completed_tasks": completed,
        "total_tasks": total,
        "running_tasks": running,
        "failed_tasks": failed,
        "tasks": tasks,
        "summary": summary,
    }


def now_iso() -> str:
    return datetime.now(timezone.utc).isoformat()


def _base_phase_record(
    run_root: Path,
    *,
    phase: str,
    kind: str,
    suite: Suite,
    tasks: list[TaskSpec],
) -> dict[str, Any]:
    queued = [
        {
            "id": task.id,
            "category": task.category,
            "status": "queued",
        }
        for task in tasks
    ]
    return {
        "schema": PHASE_SCHEMA,
        "phase": phase,
        "kind": kind,
        "suite_name": suite.name,
        "status": "queued",
        "started_at": now_iso(),
        "finished_at": "",
        "counts": _count_tasks(queued),
        "tasks": queued,
    }


def _task_records_from_rollouts(run_root: Path, tasks: list[TaskSpec], rollouts: list[RolloutResult]) -> list[dict[str, Any]]:
    task_by_id = {task.id: task for task in tasks}
    records: list[dict[str, Any]] = []
    for rollout in rollouts:
        task = task_by_id.get(rollout.id)
        status = str(rollout.extras.get("benchmark_status") or "").strip()
        if not status:
            status = "passed" if rollout.hard else "failed"
        record: dict[str, Any] = {
            "id": rollout.id,
            "category": task.category if task else "",
            "status": status,
            "hard": rollout.hard,
            "soft": rollout.soft,
            "turns": rollout.n_turns,
            "reason": rollout.fail_reason,
        }
        artifact_dir = _relative_to_run_root(run_root, rollout.artifact_dir)
        if artifact_dir:
            record["artifact_dir"] = artifact_dir
        raw_report = _relative_to_run_root(run_root, str(rollout.extras.get("benchmark_report") or ""))
        if raw_report:
            record["raw_report"] = raw_report
        records.append(record)
    return records


def _count_tasks(tasks: list[dict[str, Any]]) -> dict[str, int]:
    counts = {
        "total": len(tasks),
        "queued": 0,
        "running": 0,
        "completed": 0,
        "passed": 0,
        "failed": 0,
        "skipped": 0,
        "judge_error": 0,
        "timeout": 0,
        "error": 0,
    }
    for task in tasks:
        status = str(task.get("status") or "").strip()
        if status in counts:
            counts[status] += 1
        elif status:
            counts["failed"] += 1
    counts["error"] = counts["skipped"] + counts["judge_error"] + counts["timeout"]
    return counts


def _completed_count(counts: dict[str, int]) -> int:
    return sum(int(counts.get(status) or 0) for status in ("completed", "passed", "failed", "skipped", "judge_error", "timeout"))


def _score_payload(score: Any) -> dict[str, Any]:
    return {
        "hard": float(getattr(score, "hard", 0.0)),
        "soft": float(getattr(score, "soft", 0.0)),
        "n": int(getattr(score, "n", 0)),
        "n_passed": int(getattr(score, "n_passed", 0)),
    }


def _first_raw_report(tasks: list[dict[str, Any]]) -> str:
    for task in tasks:
        raw_report = str(task.get("raw_report") or "").strip()
        if raw_report:
            return raw_report
    return ""


def _relative_to_run_root(run_root: Path, value: str) -> str:
    value = str(value or "").strip()
    if not value:
        return ""
    if "://" in value:
        return value
    path = Path(value)
    if not path.is_absolute():
        return path.as_posix()
    try:
        return path.resolve().relative_to(Path(run_root).resolve()).as_posix()
    except (OSError, ValueError):
        return value


def _phase_record_path(run_root: Path, phase: str) -> Path:
    safe = re.sub(r"[^A-Za-z0-9_.-]+", "-", str(phase or "")).strip("-.") or "phase"
    return Path(run_root) / "phases" / f"{safe}.json"


def _read_phase_record(run_root: Path, phase: str) -> dict[str, Any] | None:
    path = _phase_record_path(run_root, phase)
    if not path.exists():
        return None
    try:
        payload = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        return None
    return payload if isinstance(payload, dict) else None


def _write_phase_record(run_root: Path, phase: str, record: dict[str, Any]) -> None:
    path = _phase_record_path(run_root, phase)
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(record, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def _phase_sort_key(phase: str) -> tuple[int, int, str]:
    if phase == "baseline_selection":
        return (0, 0, phase)
    match = re.match(r"step_(\d+)_(train|selection)$", phase)
    if match:
        phase_order = 1 if match.group(2) == "train" else 2
        return (int(match.group(1)), phase_order, phase)
    return (999_999, 0, phase)


def _iteration_from_phase(phase: str) -> int:
    match = re.match(r"step_(\d+)_(train|selection)$", str(phase or ""))
    return int(match.group(1)) if match else 0


def _summarize_ids(ids: list[str]) -> str:
    shown = ", ".join(ids[:3])
    if len(ids) > 3:
        shown += f", +{len(ids) - 3} more"
    return shown
