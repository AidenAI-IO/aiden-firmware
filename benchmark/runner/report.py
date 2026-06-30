from __future__ import annotations
import dataclasses as dc
import json
import subprocess
from datetime import datetime, timezone
from pathlib import Path
from typing import Any
from runner.models import TaskResult
from runner.metrics import aggregate

def now_iso() -> str:
    return datetime.now(timezone.utc).isoformat()

def git_sha(repo_root: Path) -> tuple[str, bool]:
    try:
        sha = subprocess.check_output(
            ["git", "rev-parse", "HEAD"], cwd=repo_root, text=True
        ).strip()
        dirty = bool(subprocess.check_output(
            ["git", "status", "--porcelain"], cwd=repo_root, text=True
        ).strip())
        return sha, dirty
    except Exception:
        return "", False

def write_jsonl(path: Path, results: list[TaskResult]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8") as fp:
        for r in results:
            fp.write(json.dumps(dc.asdict(r), ensure_ascii=False, sort_keys=True) + "\n")

def write_manifest(path: Path, manifest: dict[str, Any]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(
        json.dumps(manifest, ensure_ascii=False, indent=2, sort_keys=True),
        encoding="utf-8",
    )

def write_summary(path: Path, suite_name: str, manifest: dict[str, Any],
                  results: list[TaskResult]) -> None:
    agg = aggregate(results)
    lines = [
        f"# {suite_name} — {manifest.get('run_id', '')}",
        "",
        f"Agent: {manifest.get('agent_url', '')}",
        f"Judge: {(manifest.get('judge_config') or {}).get('provider', 'none')}"
        f" / {(manifest.get('judge_config') or {}).get('model', 'none')}",
        f"Total: {agg['passed']}/{agg['tasks']} passed",
        "",
        "## By category",
        "",
        "| category | passed | total | rubric step % |",
        "|---|---|---|---|",
    ]
    for cat, c in agg["by_category"].items():
        pct = (100.0 * c["rubric_pass"] / c["rubric_total"]) if c["rubric_total"] else 0
        lines.append(f"| {cat} | {c['passed']} | {c['total']} | {pct:.0f}% |")
    lines += [
        "",
        "## Efficiency",
        "",
        f"median wall: {agg.get('wall_ms_median')} ms"
        f"    p95 wall: {agg.get('wall_ms_p95')} ms",
        f"median tool calls: {agg.get('tool_calls_median')}"
        f"    p95: {agg.get('tool_calls_p95')}",
        "",
    ]
    skill_obs = agg.get("skill_read_device_operator") or {}
    if skill_obs.get("tasks_observed"):
        hits = skill_obs.get("tasks_with_skill_read", 0)
        total = skill_obs.get("tasks_observed", 0)
        lines += [
            "## Skill activation (informational)",
            "",
            f"device-operator skill activation/read: {hits}/{total} tasks",
            "",
        ]
    lines += [
        "## Failures",
        "",
    ]
    for r in results:
        if r.status == "passed":
            continue
        bad = [v for v in r.rubric if v.verdict == "no"]
        reasons = "; ".join(f"{v.id}: {v.reason}" for v in bad) or r.status
        lines.append(f"- **{r.task_id}** ({r.status}) — {reasons}")
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text("\n".join(lines) + "\n", encoding="utf-8")
