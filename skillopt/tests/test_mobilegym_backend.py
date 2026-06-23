import json
from pathlib import Path

import pytest

from runner.suite import HardAssertions, Suite, TaskSpec


def _write_mobilegym_config(root: Path) -> Path:
    config = root / "mobilegym" / "config"
    (config / "skills" / "device-operator").mkdir(parents=True)
    (config / "agent.toml.template").write_text("[model]\napi_key = \"{{MODEL_API_KEY}}\"\n", encoding="utf-8")
    (config / "skills" / "device-operator" / "SKILL.md").write_text("template skill", encoding="utf-8")
    return config


def _write_shared_skills(tmp_path: Path) -> Path:
    shared = tmp_path / "shared-skills"
    (shared / "device-operator").mkdir(parents=True)
    (shared / "device-operator" / "SKILL.md").write_text("shared skill", encoding="utf-8")
    return shared


def _suite(root: Path) -> Suite:
    suite_path = root / "suites" / "skillopt" / "device-operator" / "device_operator_train.json"
    suite_path.parent.mkdir(parents=True)
    suite_path.write_text(json.dumps({"name": "device_operator_train", "tasks": []}), encoding="utf-8")
    return Suite(
        name="device_operator_train",
        global_reset={},
        tasks=[],
        sha256="sha",
        source_path=suite_path,
    )


def _task() -> TaskSpec:
    return TaskSpec(
        id="case_one",
        category="single_step",
        description_for_judge="desc",
        prompt="prompt",
        rubric=[],
        hard_assertions=HardAssertions(),
    )


def test_mobilegym_backend_rollout_reports_unavailable_cli_backend(tmp_path: Path):
    from skillopt import mobilegym_backend

    benchmark_root = tmp_path / "benchmark"
    _write_mobilegym_config(benchmark_root)
    shared_skills = _write_shared_skills(tmp_path)
    backend = mobilegym_backend.MobileGymBackend(benchmark_root=benchmark_root, shared_skills_dir=shared_skills)

    with pytest.raises(RuntimeError, match="standalone SkillOpt CLI"):
        backend.run_rollout(
            suite=_suite(benchmark_root),
            tasks=[_task()],
            skill_name="device-operator",
            skill_path=shared_skills / "device-operator" / "SKILL.md",
            skill_text="candidate skill",
            phase="phase",
            run_id="run-1",
            run_root=tmp_path / "runs" / "run-1",
            judge_cfg=None,
        )


def test_prepare_source_config_rejects_skill_name_escape(tmp_path: Path):
    from skillopt import mobilegym_backend

    benchmark_root = tmp_path / "benchmark"
    _write_mobilegym_config(benchmark_root)
    shared_skills = _write_shared_skills(tmp_path)
    backend = mobilegym_backend.MobileGymBackend(benchmark_root=benchmark_root, shared_skills_dir=shared_skills)
    source_config = tmp_path / "source-config"

    with pytest.raises(ValueError, match="invalid skill_name"):
        backend._prepare_source_config(source_config, "../evil", "candidate skill")

    assert not (source_config / "evil" / "SKILL.md").exists()
