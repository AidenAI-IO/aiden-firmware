from __future__ import annotations

import dataclasses as dc
import json
import os
import re
import shutil
import subprocess
import tempfile
from collections import Counter
from pathlib import Path

from runner.html_report import generate_report_html
from runner.judge import JudgeConfig
from runner.models import TaskResult
from runner.report import now_iso, write_jsonl, write_manifest, write_summary
from runner.suite import Suite, TaskSpec
from runner.skillopt import mobilegym_results
from runner.skillopt.score import task_result_to_rollout
from runner.skillopt.types import RolloutResult


class MobileGymBackend:
    def __init__(
        self,
        *,
        benchmark_root: Path,
        shared_skills_dir: Path,
        parallel: int = 1,
        env: dict[str, str] | None = None,
    ):
        if parallel < 1:
            raise ValueError("parallel must be positive")
        self.benchmark_root = Path(benchmark_root)
        self.shared_skills_dir = Path(shared_skills_dir)
        self.parallel = parallel
        self.env = dict(env or {})

    def close(self) -> None:
        return None

    def run_rollout(
        self,
        *,
        suite: Suite,
        tasks: list[TaskSpec],
        skill_name: str,
        skill_path: Path,
        skill_text: str,
        phase: str,
        run_id: str,
        run_root: Path,
        judge_cfg: JudgeConfig | None,
    ) -> list[RolloutResult]:
        del skill_path
        suite_label = self._suite_label(suite)
        batch_id = _sanitize_batch_id(f"{run_id}-{phase}")
        batch_dir = self.benchmark_root / "runs" / "mobilegym" / batch_id

        with tempfile.TemporaryDirectory(prefix="skillopt-mobilegym-") as tmp:
            source_config = Path(tmp) / "config"
            self._prepare_source_config(source_config, skill_name, skill_text)
            command = ["./parallel_run.sh", "--aiden-suite", suite_label]
            if tasks:
                command.extend(["--aiden-task-ids", ",".join(task.id for task in tasks)])
            timeout_sec = int(self.env.get(
                "MOBILEGYM_RUN_TIMEOUT_SEC",
                os.environ.get("MOBILEGYM_RUN_TIMEOUT_SEC", "3600"),
            ))
            try:
                result = subprocess.run(
                    command,
                    cwd=self.benchmark_root / "mobilegym" / "docker",
                    env=self._run_env(source_config, batch_id),
                    text=True,
                    stdout=subprocess.PIPE,
                    stderr=subprocess.PIPE,
                    check=False,
                    timeout=timeout_sec,
                )
            except subprocess.TimeoutExpired as exc:
                raise RuntimeError(
                    f"MobileGym runner timed out after {timeout_sec}s (batch_dir={batch_dir})"
                ) from exc

        if result.returncode != 0 and not _has_readable_task_rows(batch_dir):
            raise RuntimeError(
                "MobileGym runner failed "
                f"(exit={result.returncode}, batch_dir={batch_dir})\n"
                f"stdout:\n{result.stdout}\n"
                f"stderr:\n{result.stderr}"
            )

        run_health = _read_mobilegym_run_health(batch_dir, expected_tasks=len(tasks))
        task_results = mobilegym_results.load_aiden_suite_task_results(
            batch_dir=batch_dir,
            suite=suite,
            tasks=tasks,
            run_id=run_id,
            phase_artifact_dir=run_root / phase,
            judge_cfg=judge_cfg,
            judge_cache_dir=run_root / "_judge_cache",
        )
        _write_json(batch_dir / "run_health.json", run_health)
        _write_aiden_judged_phase_report(
            batch_dir=batch_dir,
            batch_id=batch_id,
            suite=suite,
            suite_label=suite_label,
            task_results=task_results,
            judge_cfg=judge_cfg,
        )
        return [task_result_to_rollout(task_result) for task_result in task_results]

    def _suite_label(self, suite: Suite) -> str:
        suites_root = self.benchmark_root / "suites"
        rel = suite.source_path.relative_to(suites_root)
        return rel.with_suffix("").as_posix()

    def _prepare_source_config(self, source_config: Path, skill_name: str, skill_text: str) -> None:
        template_config = self.benchmark_root / "mobilegym" / "config"
        shutil.copytree(template_config, source_config, dirs_exist_ok=True)
        target_skills = source_config / "skills"
        if target_skills.exists():
            shutil.rmtree(target_skills)
        shutil.copytree(self.shared_skills_dir, target_skills)
        skills_root = target_skills.resolve()
        skill_dir = (skills_root / skill_name).resolve()
        if skill_dir == skills_root or not skill_dir.is_relative_to(skills_root):
            raise ValueError(f"invalid skill_name: {skill_name!r}")
        skill_dir.mkdir(parents=True, exist_ok=True)
        (skill_dir / "SKILL.md").write_text(skill_text, encoding="utf-8")

    def _run_env(self, source_config: Path, batch_id: str) -> dict[str, str]:
        run_env = os.environ.copy()
        run_env.update(self.env)
        run_env.update({
            "AIDEN_SOURCE_CONFIG_DIR": str(source_config),
            "MOBILEGYM_BATCH_ID": batch_id,
            "PARALLEL": str(self.parallel),
        })
        return run_env


def _sanitize_batch_id(value: str) -> str:
    cleaned = re.sub(r"[^A-Za-z0-9_.-]+", "-", value).strip("-.")
    return cleaned or "skillopt"


def _has_readable_task_rows(batch_dir: Path) -> bool:
    for pattern in ("**/results.jsonl", "**/errors.jsonl"):
        for path in batch_dir.glob(pattern):
            try:
                if path.read_text(encoding="utf-8").strip():
                    return True
            except OSError:
                continue
    return False


def _read_mobilegym_run_health(batch_dir: Path, *, expected_tasks: int) -> dict:
    try:
        payload = json.loads((batch_dir / "summary.json").read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError):
        payload = {}
    if not isinstance(payload, dict):
        payload = {}
    return {
        "source": "mobilegym_backend",
        "tasks": int(payload.get("tasks") or expected_tasks or 0),
        "passed": int(payload.get("passed") or 0),
        "failed": int(payload.get("failed") or 0),
        "timeout": int(payload.get("timeout") or 0),
        "error": int(payload.get("error") or 0),
        "unknown": int(payload.get("unknown") or 0),
        "worker_failed": int(payload.get("worker_failed") or 0),
    }


def _write_aiden_judged_phase_report(
    *,
    batch_dir: Path,
    batch_id: str,
    suite: Suite,
    suite_label: str,
    task_results: list[TaskResult],
    judge_cfg: JudgeConfig | None,
) -> None:
    batch_dir.mkdir(parents=True, exist_ok=True)
    _copy_task_artifacts(batch_dir, task_results)
    child_results = _child_task_results(batch_dir, task_results)
    totals = _aiden_totals(task_results)
    started = min((result.started_at for result in task_results if result.started_at), default=now_iso())
    finished = max((result.finished_at for result in task_results if result.finished_at), default=now_iso())
    manifest = {
        "run_id": batch_id,
        "suite_path": str(suite.source_path),
        "suite_sha256": suite.sha256,
        "agent_url": "mobilegym",
        "backend": "mobilegym",
        "judge_source": "aiden_suite",
        "judge_config": {"provider": judge_cfg.provider, "model": judge_cfg.model} if judge_cfg else None,
        "judge_prompt_version": "v1",
        "started_at": started,
        "finished_at": finished,
        "totals": totals,
    }
    write_manifest(batch_dir / "manifest.json", manifest)
    write_jsonl(batch_dir / "results.jsonl", child_results)
    write_summary(batch_dir / "summary.md", suite.name, manifest, child_results)
    summary = _aiden_summary(batch_id=batch_id, suite_label=suite_label, totals=totals)
    _write_json(batch_dir / "summary.json", summary)
    html = generate_report_html(batch_dir)
    (batch_dir / "index.html").write_text(html, encoding="utf-8")
    (batch_dir / "report.html").write_text(html, encoding="utf-8")


def _copy_task_artifacts(batch_dir: Path, task_results: list[TaskResult]) -> None:
    tasks_dir = batch_dir / "tasks"
    tasks_dir.mkdir(parents=True, exist_ok=True)
    for result in task_results:
        source = Path(result.artifact_dir) if result.artifact_dir else None
        target = tasks_dir / result.task_id
        if source is None or not source.exists():
            target.mkdir(parents=True, exist_ok=True)
            continue
        try:
            if source.resolve() == target.resolve():
                continue
        except OSError:
            pass
        if target.exists():
            shutil.rmtree(target)
        shutil.copytree(source, target)


def _child_task_results(batch_dir: Path, task_results: list[TaskResult]) -> list[TaskResult]:
    return [
        dc.replace(result, artifact_dir=str(batch_dir / "tasks" / result.task_id))
        for result in task_results
    ]


def _aiden_totals(task_results: list[TaskResult]) -> dict[str, int]:
    statuses = Counter(result.status for result in task_results)
    return {
        "tasks": len(task_results),
        "passed": statuses.get("passed", 0),
        "failed": statuses.get("failed", 0),
        "skipped": statuses.get("skipped", 0),
        "judge_error": statuses.get("judge_error", 0),
        "timeout": statuses.get("timeout", 0),
    }


def _aiden_summary(*, batch_id: str, suite_label: str, totals: dict[str, int]) -> dict:
    failed_for_mobilegym_shape = totals["failed"]
    error_for_mobilegym_shape = totals["judge_error"] + totals["skipped"]
    summary = {
        "batch_id": batch_id,
        "suite": suite_label,
        "judge_source": "aiden_suite",
        "tasks": totals["tasks"],
        "passed": totals["passed"],
        "failed": failed_for_mobilegym_shape,
        "timeout": totals["timeout"],
        "error": error_for_mobilegym_shape,
        "unknown": 0,
        "worker_failed": 0,
        "empty": 0,
    }
    summary["pass_rate"] = (summary["passed"] / summary["tasks"]) if summary["tasks"] else 0.0
    summary["suites"] = [{"suite": suite_label, **summary}]
    return summary


def _write_json(path: Path, payload: dict) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(payload, ensure_ascii=False, indent=2, sort_keys=True), encoding="utf-8")
