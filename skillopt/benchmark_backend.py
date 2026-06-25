from __future__ import annotations

import dataclasses as dc
import json
import os
import re
import shutil
import subprocess
import sys
from pathlib import Path
from typing import Any

from runner.judge import JudgeConfig
from runner.models import HardAssertionFailure, HardAssertionResults, RubricVerdict, TaskResult
from runner.suite import Suite, TaskSpec
from runner.webui import prepare_run_config
from skillopt.score import task_result_to_rollout
from skillopt.types import RolloutResult


DEFAULT_DAEMON_IMAGE = "aiden-agent-daemon:local"


class BenchmarkRunnerBackend:
    """Run SkillOpt rollouts through the current benchmark runner architecture."""

    def __init__(
        self,
        *,
        benchmark_root: Path,
        base_config_dir: Path,
        shared_skills_dir: Path,
        environment_url: str,
        backend: str = "mobilegym",
        daemon_image: str = DEFAULT_DAEMON_IMAGE,
        build_daemon_image: bool = True,
        agent_config_path: Path | None = None,
        python_executable: str | None = None,
    ):
        environment_url = str(environment_url or "").strip().rstrip("/")
        if not environment_url:
            raise ValueError("environment_url is required")
        self.benchmark_root = Path(benchmark_root)
        self.base_config_dir = Path(base_config_dir)
        self.shared_skills_dir = Path(shared_skills_dir)
        self.environment_url = environment_url
        self.backend = backend
        self.daemon_image = daemon_image
        self.build_daemon_image = build_daemon_image
        self.agent_config_path = Path(agent_config_path) if agent_config_path else None
        self.python_executable = python_executable or sys.executable

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
        run_root = Path(run_root)
        child_runs_root = run_root / "benchmark"
        child_run_id = sanitize_run_id(f"{run_id}-{phase}")
        phase_config = run_root / "configs" / sanitize_run_id(phase)
        self.prepare_phase_config(phase_config, skill_name, skill_text)

        cmd = self._runner_command(
            suite=suite,
            tasks=tasks,
            child_runs_root=child_runs_root,
            child_run_id=child_run_id,
            phase_config=phase_config,
            judge_cfg=judge_cfg,
        )
        proc = subprocess.run(
            cmd,
            cwd=self.benchmark_root,
            env=os.environ.copy(),
            text=True,
            stdout=subprocess.PIPE,
            stderr=subprocess.STDOUT,
            check=False,
        )
        child_run_dir = child_runs_root / child_run_id
        (run_root / "logs").mkdir(parents=True, exist_ok=True)
        (run_root / "logs" / f"{sanitize_run_id(phase)}.log").write_text(proc.stdout or "", encoding="utf-8")
        results = load_benchmark_task_results(child_run_dir)
        if not results:
            raise RuntimeError(
                f"benchmark runner produced no task results for {phase} "
                f"(exit={proc.returncode}, run={child_run_dir})"
            )
        rollouts = [task_result_to_rollout(result) for result in results]
        report = child_run_dir / "report.html"
        for rollout in rollouts:
            rollout.extras["benchmark_run_id"] = child_run_id
            rollout.extras["benchmark_run_dir"] = str(child_run_dir)
            if report.exists():
                rollout.extras["benchmark_report"] = str(report)
        return rollouts

    def prepare_phase_config(self, dest_dir: Path, skill_name: str, skill_text: str) -> Path:
        prepare_run_config(self.base_config_dir, dest_dir)
        target_skills = dest_dir / "skills"
        if target_skills.exists():
            shutil.rmtree(target_skills)
        if self.shared_skills_dir.exists():
            shutil.copytree(self.shared_skills_dir, target_skills)
        else:
            target_skills.mkdir(parents=True)
        skills_root = target_skills.resolve()
        skill_dir = (skills_root / skill_name).resolve()
        if skill_dir == skills_root or not skill_dir.is_relative_to(skills_root):
            raise ValueError(f"invalid skill_name: {skill_name!r}")
        skill_dir.mkdir(parents=True, exist_ok=True)
        (skill_dir / "SKILL.md").write_text(skill_text, encoding="utf-8")
        return dest_dir

    def _runner_command(
        self,
        *,
        suite: Suite,
        tasks: list[TaskSpec],
        child_runs_root: Path,
        child_run_id: str,
        phase_config: Path,
        judge_cfg: JudgeConfig | None,
    ) -> list[str]:
        cmd = [
            self.python_executable,
            "-m",
            "runner.main",
            "run",
            "--suite",
            str(suite.source_path),
            "--auto-agent-setup",
            "--environment-url",
            self.environment_url,
            "--daemon-image",
            self.daemon_image,
            "--base-config-dir",
            str(phase_config),
            "--out",
            str(child_runs_root),
            "--run-id",
            child_run_id,
        ]
        if not self.build_daemon_image:
            cmd.append("--no-build-daemon-image")
        if self.agent_config_path:
            cmd.extend(["--agent-config", str(self.agent_config_path)])
        for task in tasks:
            cmd.extend(["--task-id", task.id])
        if judge_cfg is None:
            cmd.append("--no-judge")
        else:
            cmd.extend(["--judge-model", judge_cfg.model])
        return cmd


def sanitize_run_id(value: str) -> str:
    cleaned = re.sub(r"[^A-Za-z0-9_.-]+", "-", str(value or "")).strip("-.")
    return cleaned or "skillopt"


def load_benchmark_task_results(run_dir: Path) -> list[TaskResult]:
    path = Path(run_dir) / "results.jsonl"
    if not path.exists():
        return []
    results: list[TaskResult] = []
    for line in path.read_text(encoding="utf-8").splitlines():
        if not line.strip():
            continue
        payload = json.loads(line)
        if isinstance(payload, dict):
            results.append(task_result_from_dict(payload))
    return results


def task_result_from_dict(payload: dict[str, Any]) -> TaskResult:
    rubric = [RubricVerdict(**item) for item in payload.get("rubric", []) if isinstance(item, dict)]
    hard_payload = payload.get("hard_assertions")
    hard_assertions = HardAssertionResults(**hard_payload) if isinstance(hard_payload, dict) else None
    failures = [
        HardAssertionFailure(**item)
        for item in payload.get("hard_assertion_failures", [])
        if isinstance(item, dict)
    ]
    fields = {field.name for field in dc.fields(TaskResult)}
    data = {key: value for key, value in payload.items() if key in fields}
    data["rubric"] = rubric
    data["hard_assertions"] = hard_assertions
    data["hard_assertion_failures"] = failures
    return TaskResult(**data)
