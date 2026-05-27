from __future__ import annotations
import dataclasses as dc
import json
import time
from pathlib import Path
from runner.agent_client import AgentClient, AgentTimeoutError
from runner.assertions import evaluate_hard_assertions
from runner.capture import take_screenshot, write_step_screenshot
from runner.judge import judge_task, JudgeConfig
from runner.models import TaskResult, RubricVerdict, HardAssertionResults
from runner.reset import global_reset, per_task_setup, ResetError
from runner.suite import Suite, TaskSpec
from runner.trace import extract_trace, extract_step_screenshots
from runner.report import now_iso


def run_one_task(
    client: AgentClient,
    suite: Suite,
    task: TaskSpec,
    attempt: int,
    artifact_dir: Path,
    judge_cfg: JudgeConfig | None,
    judge_cache_dir: Path | None,
    run_id: str,
) -> TaskResult:
    artifact_dir.mkdir(parents=True, exist_ok=True)
    started = now_iso()
    started_mono = time.monotonic()
    base = TaskResult(
        suite=suite.name, run_id=run_id, task_id=task.id, category=task.category,
        attempt=attempt, status="failed",
        rubric=[], rubric_pass_count=0, rubric_total=len(task.rubric),
        artifact_dir=str(artifact_dir), started_at=started,
        description_for_judge=task.description_for_judge,
        rubric_spec=[dc.asdict(r) for r in task.rubric],
    )
    try:
        client.clear_history()
        if suite.global_reset.get("tool_sequence"):
            global_reset(client, suite.global_reset)
        per_task_setup(client, task.setup)
    except ResetError as e:
        base.status = "skipped"
        base.metrics = {"error": f"setup: {e}"}
        base.finished_at = now_iso()
        return base
    pre_path = artifact_dir / "pre.jpg"
    try:
        take_screenshot(client, pre_path)
    except Exception:
        pass
    timed_out = False
    try:
        chat = client.chat(task.prompt, timeout_sec=task.hard_assertions.must_complete_within_sec)
        history = chat.history
    except AgentTimeoutError:
        timed_out = True
        history = client_history_or_empty(client)
    except Exception as e:
        history = client_history_or_empty(client)
        base.metrics["agent_error"] = str(e)[:300]
    (artifact_dir / "history.json").write_text(
        json.dumps(history, ensure_ascii=False, indent=2), encoding="utf-8")
    trace = extract_trace(history)
    wall_ms = int((time.monotonic() - started_mono) * 1000)
    trace_dict = {
        "tool_calls": [dc.asdict(tc) for tc in trace.tool_calls],
        "final_response": trace.final_response,
        "total_tool_calls": trace.total_tool_calls,
    }
    (artifact_dir / "trace.json").write_text(
        json.dumps(trace_dict, ensure_ascii=False, indent=2), encoding="utf-8")
    steps_dir = artifact_dir / "steps"
    last_shot_path: Path | None = None
    for i, (tool_name, b64) in enumerate(extract_step_screenshots(history), start=1):
        # Sanitize tool_name to prevent path traversal
        safe_name = "".join(c if c.isalnum() or c in "-_" else "_" for c in tool_name)[:50]
        p = steps_dir / f"step_{i:02d}_{safe_name}.jpg"
        write_step_screenshot(p, b64)
        last_shot_path = p
    base.metrics.update({"wall_ms": wall_ms, "tool_calls": trace.total_tool_calls,
                         "screenshots_taken": sum(1 for tc in trace.tool_calls if tc.has_screenshot)})
    outcome = evaluate_hard_assertions(trace, task.hard_assertions, timed_out=timed_out)
    base.hard_assertions = outcome.results
    if not outcome.all_passed:
        base.status = "timeout" if timed_out else "failed"
        base.finished_at = now_iso()
        return base
    if judge_cfg is None:
        base.status = "passed"
        base.finished_at = now_iso()
        return base
    try:
        verdict = judge_task(
            description=task.description_for_judge,
            rubric=task.rubric,
            pre_screenshot=pre_path if pre_path.exists() else None,
            post_screenshot=last_shot_path,
            trace=trace_dict,
            final_response=trace.final_response,
            cfg=judge_cfg,
            cache_dir=judge_cache_dir,
        )
    except Exception as e:
        base.status = "judge_error"
        base.metrics["judge_error"] = str(e)
        base.finished_at = now_iso()
        return base
    base.rubric = verdict.verdicts
    base.rubric_pass_count = sum(1 for v in verdict.verdicts if v.verdict == "yes")
    (artifact_dir / "judge.json").write_text(json.dumps({
        "verdicts": [dc.asdict(v) for v in verdict.verdicts],
        "overall_notes": verdict.overall_notes,
        "cache_key": verdict.cache_key,
    }, ensure_ascii=False, indent=2), encoding="utf-8")
    base.status = "passed" if base.rubric_pass_count == base.rubric_total else "failed"
    base.finished_at = now_iso()
    return base


def client_history_or_empty(client: AgentClient) -> list[dict]:
    try:
        r = client._client.get("/api/history", timeout=5)
        if r.status_code == 200:
            return r.json()
    except Exception:
        pass
    return []
