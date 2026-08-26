from __future__ import annotations
import base64
import dataclasses as dc
import json
import shutil
import time
from pathlib import Path
from typing import Any

from PIL import Image

from runner.agent_client import AgentClient, AgentRequestError, AgentTimeoutError
from runner.assertions import (
    evaluate_expected_answer,
    evaluate_expected_recalled_memory_ids,
    evaluate_hard_assertions,
    evaluate_trace_observations,
)
from runner.capture import take_environment_screenshot
from runner.judge import judge_task, JudgeConfig
from runner.models import HardAssertionFailure, HardAssertionResults, RubricVerdict, TaskResult
from runner.recovery import prepare_task_isolation, recover_agent_after_timeout
from runner.reset import ResetError, SetupAssertionError
from runner.suite import Suite, TaskSpec, effective_mock_environment
from runner.trace import extract_trace
from runner.report import now_iso


def skipped_task_result(
    suite: Suite,
    task: TaskSpec,
    attempt: int,
    artifact_dir: Path,
    run_id: str,
    error: str,
) -> TaskResult:
    artifact_dir.mkdir(parents=True, exist_ok=True)
    started = now_iso()
    return TaskResult(
        suite=suite.name,
        run_id=run_id,
        task_id=task.id,
        category=task.category,
        attempt=attempt,
        status="skipped",
        rubric=[],
        rubric_pass_count=0,
        rubric_total=len(task.rubric),
        artifact_dir=str(artifact_dir),
        started_at=started,
        finished_at=started,
        description_for_judge=task.description_for_judge,
        rubric_spec=[dc.asdict(r) for r in task.rubric],
        metrics={"error": error},
    )


def evaluate_task_history(
    *,
    suite: Suite,
    task: TaskSpec,
    history: list[dict[str, Any]],
    attempt: int,
    artifact_dir: Path,
    judge_cfg: JudgeConfig | None,
    judge_cache_dir: Path | None,
    run_id: str,
    timed_out: bool,
    metrics: dict[str, Any] | None = None,
    pre_screenshot: Path | None = None,
    post_screenshot: Path | None = None,
    started_at: str | None = None,
    started_mono: float | None = None,
    active_skills: list[str] | None = None,
    episode: dict[str, Any] | None = None,
) -> TaskResult:
    artifact_dir.mkdir(parents=True, exist_ok=True)
    started = started_at or now_iso()
    base = TaskResult(
        suite=suite.name,
        run_id=run_id,
        task_id=task.id,
        category=task.category,
        attempt=attempt,
        status="failed",
        rubric=[],
        rubric_pass_count=0,
        rubric_total=len(task.rubric),
        artifact_dir=str(artifact_dir),
        started_at=started,
        description_for_judge=task.description_for_judge,
        rubric_spec=[dc.asdict(r) for r in task.rubric],
        metrics=dict(metrics or {}),
    )
    (artifact_dir / "history.json").write_text(
        json.dumps(history, ensure_ascii=False, indent=2), encoding="utf-8")
    trace = extract_trace(history)
    if started_mono is None:
        wall_ms = 0
    else:
        wall_ms = int((time.monotonic() - started_mono) * 1000)
    active_skills = _normalise_active_skills(active_skills)
    if active_skills:
        base.metrics["active_skills"] = active_skills
    if suite.trace_observations:
        observation_results = evaluate_trace_observations(
            trace,
            suite.trace_observations,
            active_skills=active_skills,
        )
        base.metrics["trace_observations"] = [
            {
                "id": item.id,
                "description": item.description,
                "passed": item.passed,
                "reason": item.reason,
            }
            for item in observation_results
        ]
    trace_dict = {
        "tool_calls": [dc.asdict(tc) for tc in trace.tool_calls],
        "final_response": trace.final_response,
        "total_tool_calls": trace.total_tool_calls,
    }
    last_shot_path = post_screenshot if post_screenshot is not None and post_screenshot.exists() else None
    base.metrics.update({"wall_ms": wall_ms, "tool_calls": trace.total_tool_calls,
                         "screenshots_taken": sum(1 for tc in trace.tool_calls if tc.has_screenshot),
                         "pre_screenshot_file": bool(pre_screenshot and pre_screenshot.exists()),
                         "post_screenshot_file": bool(post_screenshot and post_screenshot.exists())})
    # The public history includes provider-normalized usage on assistant
    # messages. Keep a task-level total so paired benchmark runs can report
    # token/cost overhead in addition to wall-clock overhead. Older agents may
    # omit usage; in that case these fields are simply absent.
    usage = _history_usage(history)
    if usage is not None:
        base.metrics.update(usage)
    recall_outcome = None
    if task.expected_recalled_memory_ids:
        recall_outcome = evaluate_expected_recalled_memory_ids(
            history,
            task.expected_recalled_memory_ids,
            episode=episode,
            recall_tool=task.expected_recalled_memory_tool,
            require_inline_recall=task.expected_recall_from_consolidation,
        )
        base.metrics.update({
            "expected_recalled_memory_ids": recall_outcome.expected_memory_ids,
            "recalled_memory_ids": recall_outcome.recalled_memory_ids,
            "memory_recall_evidence_source": recall_outcome.evidence_source,
            "expected_recalled_memory_match": recall_outcome.passed,
        })
        trace_dict.update({
            "recalled_memory_ids": recall_outcome.recalled_memory_ids,
            "memory_recall_evidence_source": recall_outcome.evidence_source,
            "expected_recalled_memory_match": recall_outcome.passed,
        })
    (artifact_dir / "trace.json").write_text(
        json.dumps(trace_dict, ensure_ascii=False, indent=2), encoding="utf-8")
    outcome = evaluate_hard_assertions(trace, task.hard_assertions, timed_out=timed_out)
    base.hard_assertions = outcome.results
    base.hard_assertion_failures = list(outcome.failures)
    if recall_outcome is not None:
        base.hard_assertions.expected_recalled_memory = recall_outcome.passed
    if not outcome.all_passed:
        base.status = "timeout" if timed_out else "failed"
        base.finished_at = now_iso()
        return base
    if task.expected_answer is not None:
        answer_outcome = evaluate_expected_answer(
            trace.final_response, task.expected_answer, task.answer_format or "option_letter"
        )
        base.metrics.update({
            "expected_answer": answer_outcome.expected_answer,
            "predicted_answer": answer_outcome.predicted_answer,
            "expected_answer_match": answer_outcome.passed,
        })
        base.hard_assertions.expected_answer = answer_outcome.passed
        if not answer_outcome.passed:
            base.hard_assertion_failures.append(
                HardAssertionFailure(
                    id="expected_answer",
                    label="Expected Answer",
                    requirement=f"Final answer must be {answer_outcome.expected_answer or 'unparseable expected answer'}.",
                    actual=f"Predicted answer was {answer_outcome.predicted_answer or 'none'}.",
                )
            )
            base.status = "failed"
            base.finished_at = now_iso()
            return base
    if recall_outcome is not None:
        if recall_outcome.passed is None:
            base.status = "judge_error"
            base.metrics["judge_error"] = (
                "Memory recall evidence is unavailable: episode could not be used "
                "and inline recall_memory results were incomplete or unparsable."
            )
            base.finished_at = now_iso()
            return base
        if recall_outcome.passed is False:
            missing_memory_ids = [
                memory_id
                for memory_id in recall_outcome.expected_memory_ids
                if memory_id not in recall_outcome.recalled_memory_ids
            ]
            if recall_outcome.recall_memory_called:
                actual = (
                    f"Missing: {_format_csv(missing_memory_ids)}. "
                    f"Recalled: {_format_csv(recall_outcome.recalled_memory_ids)}."
                )
            else:
                actual = (
                    "No recall_memory call was found. "
                    f"Missing: {_format_csv(missing_memory_ids)}. Recalled: none."
                )
            base.hard_assertion_failures.append(
                HardAssertionFailure(
                    id="expected_recalled_memory",
                    label="Expected Recalled Memory",
                    requirement=f"Must recall memory id(s): {_format_csv(recall_outcome.expected_memory_ids)}.",
                    actual=actual,
                )
            )
            base.status = "failed"
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
            pre_screenshot=pre_screenshot if pre_screenshot and pre_screenshot.exists() else None,
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
    base.metrics["judge_image_count"] = verdict.image_count
    base.metrics["judge_image_labels"] = verdict.image_labels
    (artifact_dir / "judge.json").write_text(json.dumps({
        "verdicts": [dc.asdict(v) for v in verdict.verdicts],
        "overall_notes": verdict.overall_notes,
        "cache_key": verdict.cache_key,
        "image_count": verdict.image_count,
        "image_labels": verdict.image_labels,
    }, ensure_ascii=False, indent=2), encoding="utf-8")
    base.status = "passed" if base.rubric_pass_count == base.rubric_total else "failed"
    base.finished_at = now_iso()
    return base


def _history_usage(history: list[dict[str, Any]]) -> dict[str, int] | None:
    """Sum normalized token usage exposed by the Agent history endpoint.

    Usage is attached to assistant messages, one record per model response.
    Providers use different names internally, but the public API normalizes
    them to ``input_tokens``, ``output_tokens`` and ``total_tokens``. We also
    accept the legacy prompt/completion names for old benchmark daemons.
    """
    input_tokens = 0
    output_tokens = 0
    total_tokens = 0
    found = False
    for message in history:
        raw = message.get("usage") if isinstance(message, dict) else None
        if not isinstance(raw, dict):
            continue
        input_value = raw.get("input_tokens", raw.get("prompt_tokens"))
        output_value = raw.get("output_tokens", raw.get("completion_tokens"))
        total_value = raw.get("total_tokens")
        values: list[int] = []
        for value in (input_value, output_value, total_value):
            try:
                values.append(int(value))
            except (TypeError, ValueError):
                values.append(0)
        if not any(values):
            continue
        found = True
        input_tokens += values[0]
        output_tokens += values[1]
        total_tokens += values[2] if values[2] else values[0] + values[1]
    if not found:
        return None
    return {
        "input_tokens": input_tokens,
        "output_tokens": output_tokens,
        "total_tokens": total_tokens,
    }


def run_one_task(
    client: AgentClient,
    suite: Suite,
    task: TaskSpec,
    attempt: int,
    artifact_dir: Path,
    judge_cfg: JudgeConfig | None,
    judge_cache_dir: Path | None,
    run_id: str,
    environment_url: str | None = None,
    benchmark_task_id: str | None = None,
    active_skills: list[str] | None = None,
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
    active_skills = _normalise_active_skills(active_skills)
    setup_result: dict[str, Any] | None = None
    try:
        setup_result = prepare_task_isolation(
            client,
            suite,
            task,
            environment_url=environment_url,
            benchmark_task_id=benchmark_task_id,
        )
    except SetupAssertionError as e:
        base.status = "failed"
        base.metrics = {"error": f"setup assertion: {e}"}
        base.finished_at = now_iso()
        return base
    except (ResetError, AgentTimeoutError, AgentRequestError) as e:
        failed_consolidation = getattr(e, "consolidation", None)
        if isinstance(failed_consolidation, dict):
            (artifact_dir / "consolidation.json").write_text(
                json.dumps(failed_consolidation, ensure_ascii=False, indent=2),
                encoding="utf-8",
            )
        base.status = "failed" if isinstance(failed_consolidation, dict) else "skipped"
        base.metrics = {"error": f"setup: {e}"}
        if isinstance(failed_consolidation, dict):
            base.metrics["consolidation_goal_result"] = (
                failed_consolidation.get("assessment", {}).get("goal_result")
                if isinstance(failed_consolidation.get("assessment"), dict)
                else None
            )
            base.metrics["consolidation_memory_count"] = len(
                failed_consolidation.get("memory_ids", [])
            )
        base.finished_at = now_iso()
        return base
    if setup_result is not None:
        if setup_result.get("consolidation") is not None:
            (artifact_dir / "consolidation.json").write_text(
                json.dumps(setup_result["consolidation"], ensure_ascii=False, indent=2),
                encoding="utf-8",
            )
            consolidation = setup_result["consolidation"]
            base.metrics["consolidation_goal_result"] = (
                consolidation.get("assessment", {}).get("goal_result")
                if isinstance(consolidation.get("assessment"), dict)
                else None
            )
            base.metrics["consolidation_memory_count"] = len(
                consolidation.get("memory_ids", [])
            )
        else:
            (artifact_dir / "setup.json").write_text(
                json.dumps(setup_result, ensure_ascii=False, indent=2),
                encoding="utf-8",
            )
    effective_task = task
    if task.expected_recall_from_consolidation:
        consolidation = setup_result.get("consolidation") if isinstance(setup_result, dict) else None
        memory_ids = consolidation.get("memory_ids") if isinstance(consolidation, dict) else None
        if not isinstance(memory_ids, list) or not memory_ids:
            base.status = "failed"
            base.metrics["error"] = "expected_recall_from_consolidation requires non-empty consolidation memory_ids"
            base.finished_at = now_iso()
            return base
        effective_task = dc.replace(task, expected_recalled_memory_ids=[str(item) for item in memory_ids])
    pre_path = artifact_dir / "pre.jpg"
    attachments = None
    input_screenshot_path = (
        suite.source_path.parent / task.input_screenshot
        if task.input_screenshot
        else None
    )
    if input_screenshot_path is not None and not input_screenshot_path.exists():
        base.status = "skipped"
        base.metrics = {
            "error": f"input_screenshot not found: {input_screenshot_path}"
        }
        base.finished_at = now_iso()
        return base
    mock_environment = effective_mock_environment(suite, task)
    uses_single_frame_mock = bool(
        mock_environment is not None and mock_environment.single_frame
    )
    if uses_single_frame_mock:
        if environment_url:
            try:
                take_environment_screenshot(
                    environment_url,
                    pre_path,
                    benchmark_task_id=benchmark_task_id,
                )
            except Exception as e:
                base.metrics["pre_screenshot_error"] = str(e)[:300]
        else:
            base.metrics["pre_screenshot_error"] = (
                "environment_url is required for mock screenshot capture"
            )
        if not pre_path.exists() and input_screenshot_path is not None:
            shutil.copy(input_screenshot_path, pre_path)
    elif input_screenshot_path is not None:
        shutil.copy(input_screenshot_path, pre_path)
        img_b64 = base64.b64encode(input_screenshot_path.read_bytes()).decode("ascii")
        with Image.open(input_screenshot_path) as image:
            width, height = image.size
        attachments = [
            {
                "kind": "image",
                "mime_type": "image/jpeg",
                "width": width,
                "height": height,
                "data": img_b64,
            }
        ]
    else:
        if environment_url:
            try:
                take_environment_screenshot(
                    environment_url,
                    pre_path,
                    benchmark_task_id=benchmark_task_id,
                )
            except Exception as e:
                base.metrics["pre_screenshot_error"] = str(e)[:300]
        else:
            base.metrics["pre_screenshot_error"] = (
                "environment_url is required for live screenshot capture"
            )
    timed_out = False
    chat_completed = False
    agent_started_mono = time.monotonic()
    episode = None
    try:
        prompt = effective_task.prompt
        if suite.prompt_prefix:
            prompt = f"{suite.prompt_prefix.rstrip()}\n\n{effective_task.prompt}"
        chat_kwargs: dict[str, Any] = {
            "timeout_sec": task.hard_assertions.must_complete_within_sec,
            "attachments": attachments,
        }
        if active_skills:
            chat_kwargs["skills"] = active_skills
        chat = client.chat(prompt, **chat_kwargs)
        history = chat.history
        chat_completed = True
    except AgentTimeoutError:
        timed_out = True
        history = client_history_or_empty(client)
        if not recover_agent_after_timeout(client):
            base.metrics["recovery_failed"] = True
    except Exception as e:
        history = client_history_or_empty(client)
        base.metrics["agent_error"] = str(e)[:300]
    base.metrics["agent_wall_ms"] = int((time.monotonic() - agent_started_mono) * 1000)
    if chat_completed and effective_task.expected_recalled_memory_ids:
        inline_recall_outcome = evaluate_expected_recalled_memory_ids(
            history,
            effective_task.expected_recalled_memory_ids,
            recall_tool=effective_task.expected_recalled_memory_tool,
            require_inline_recall=effective_task.expected_recall_from_consolidation,
        )
    else:
        inline_recall_outcome = None
    if inline_recall_outcome is not None and inline_recall_outcome.passed is None:
        episode_id = _unique_episode_id(history)
        if episode_id is not None:
            try:
                episode = client.get_episode(episode_id)
            except Exception as e:
                base.metrics["episode_error"] = str(e)[:300]
            else:
                (artifact_dir / "episode.json").write_text(
                    json.dumps(episode, ensure_ascii=False, indent=2),
                    encoding="utf-8",
                )
    # Capture the final device state directly from the environment screen API.
    # The agent history no longer embeds base64 image data, so the post-screenshot
    # must be grabbed live rather than extracted from history.
    post_path = artifact_dir / "post.jpg"
    if uses_single_frame_mock:
        post_path = None
    elif environment_url:
        try:
            take_environment_screenshot(
                environment_url,
                post_path,
                benchmark_task_id=benchmark_task_id,
            )
        except Exception as e:
            base.metrics["post_screenshot_error"] = str(e)[:300]
            post_path = None
    else:
        base.metrics["post_screenshot_error"] = (
            "environment_url is required for live screenshot capture"
        )
        post_path = None
    return evaluate_task_history(
        suite=suite,
        task=effective_task,
        history=history,
        attempt=attempt,
        artifact_dir=artifact_dir,
        judge_cfg=judge_cfg,
        judge_cache_dir=judge_cache_dir,
        run_id=run_id,
        timed_out=timed_out,
        metrics=base.metrics,
        pre_screenshot=pre_path if pre_path.exists() else None,
        post_screenshot=post_path if post_path and post_path.exists() else None,
        started_at=started,
        started_mono=started_mono,
        active_skills=active_skills,
        episode=episode,
    )


def client_history_or_empty(client: AgentClient) -> list[dict]:
    try:
        return client.get_history()
    except Exception:
        pass
    return []


def _format_csv(items: list[str]) -> str:
    return ", ".join(items) if items else "none"


def _unique_episode_id(history: list[dict[str, Any]]) -> str | None:
    episode_ids: list[str] = []
    for message in history:
        episode_id = message.get("episode_id")
        if not isinstance(episode_id, str):
            continue
        episode_id = episode_id.strip()
        if episode_id and episode_id not in episode_ids:
            episode_ids.append(episode_id)
    return episode_ids[0] if len(episode_ids) == 1 else None


def _normalise_active_skills(skills: list[str] | None) -> list[str]:
    seen = set()
    out = []
    for item in skills or []:
        name = str(item).strip()
        if not name or name in seen:
            continue
        seen.add(name)
        out.append(name)
    return out
