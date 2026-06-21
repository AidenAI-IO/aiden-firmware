from __future__ import annotations

import os
import re
import shutil
import subprocess
import tempfile
from pathlib import Path

from runner.judge import JudgeConfig
from runner.suite import Suite, TaskSpec
from runner.skillopt import mobilegym_results
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

        return mobilegym_results.load_aiden_suite_rollouts(
            batch_dir=batch_dir,
            suite=suite,
            tasks=tasks,
            run_id=run_id,
            phase_artifact_dir=run_root / phase,
            judge_cfg=judge_cfg,
            judge_cache_dir=run_root / "_judge_cache",
        )

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
