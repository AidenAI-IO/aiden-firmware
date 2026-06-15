import json
import subprocess
from pathlib import Path

import pytest

from runner.suite import HardAssertions, Suite, TaskSpec
from runner.skillopt.types import RolloutResult


def _write_mobilegym_config(root: Path) -> Path:
    config = root / "mobilegym" / "config"
    (config / "skills" / "device-operator").mkdir(parents=True)
    (config / "agent.toml.template").write_text("[model]\napi_key = \"{{MODEL_API_KEY}}\"\n", encoding="utf-8")
    (config / "skills" / "device-operator" / "SKILL.md").write_text("template skill", encoding="utf-8")
    (root / "mobilegym" / "docker").mkdir(parents=True)
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


def test_mobilegym_backend_injects_candidate_skill_and_preserves_env(monkeypatch, tmp_path: Path):
    from runner.skillopt import mobilegym_backend

    benchmark_root = tmp_path / "benchmark"
    _write_mobilegym_config(benchmark_root)
    shared_skills = _write_shared_skills(tmp_path)
    captured = {}

    def fake_run(command, *, cwd, env, text, stdout, stderr, check):
        del text, stdout, stderr, check
        source_config = Path(env["AIDEN_SOURCE_CONFIG_DIR"])
        captured["command"] = command
        captured["cwd"] = cwd
        captured["env"] = env
        captured["agent_template"] = (source_config / "agent.toml.template").read_text(encoding="utf-8")
        captured["skill_text"] = (source_config / "skills" / "device-operator" / "SKILL.md").read_text(encoding="utf-8")
        return subprocess.CompletedProcess(command, 0, stdout="ok", stderr="")

    def fake_load_rollouts(**kwargs):
        captured["batch_dir"] = kwargs["batch_dir"]
        return [RolloutResult(id="case_one", hard=1, soft=1.0)]

    monkeypatch.setenv("PATH", "/bin:/usr/bin")
    monkeypatch.setenv("MODEL_API_KEY", "model-key")
    monkeypatch.setattr(mobilegym_backend.subprocess, "run", fake_run)
    monkeypatch.setattr(mobilegym_backend.mobilegym_results, "load_aiden_suite_rollouts", fake_load_rollouts)

    backend = mobilegym_backend.MobileGymBackend(
        benchmark_root=benchmark_root,
        shared_skills_dir=shared_skills,
        parallel=3,
        env={"EXTRA_ENV": "yes"},
    )
    rollouts = backend.run_rollout(
        suite=_suite(benchmark_root),
        tasks=[_task()],
        skill_name="device-operator",
        skill_path=shared_skills / "device-operator" / "SKILL.md",
        skill_text="candidate skill",
        phase="step_01_train",
        run_id="run-1",
        run_root=tmp_path / "runs" / "run-1",
        judge_cfg=None,
    )

    assert rollouts[0].hard == 1
    assert captured["command"] == [
        "./parallel_run.sh",
        "--aiden-suite",
        "skillopt/device-operator/device_operator_train",
        "--aiden-task-ids",
        "case_one",
    ]
    assert captured["cwd"] == benchmark_root / "mobilegym" / "docker"
    assert captured["env"]["PARALLEL"] == "3"
    assert captured["env"]["MOBILEGYM_BATCH_ID"] == "run-1-step_01_train"
    assert captured["env"]["PATH"] == "/bin:/usr/bin"
    assert captured["env"]["MODEL_API_KEY"] == "model-key"
    assert captured["env"]["EXTRA_ENV"] == "yes"
    assert captured["agent_template"].startswith("[model]")
    assert captured["skill_text"] == "candidate skill"
    assert captured["batch_dir"] == benchmark_root / "runs" / "mobilegym" / "run-1-step_01_train"
    assert (benchmark_root / "mobilegym" / "config" / "skills" / "device-operator" / "SKILL.md").read_text(encoding="utf-8") == "template skill"


def test_mobilegym_backend_nonzero_without_rows_raises(monkeypatch, tmp_path: Path):
    from runner.skillopt import mobilegym_backend

    benchmark_root = tmp_path / "benchmark"
    _write_mobilegym_config(benchmark_root)
    shared_skills = _write_shared_skills(tmp_path)

    def fake_run(command, **kwargs):
        return subprocess.CompletedProcess(command, 7, stdout="out text", stderr="err text")

    monkeypatch.setattr(mobilegym_backend.subprocess, "run", fake_run)
    backend = mobilegym_backend.MobileGymBackend(benchmark_root=benchmark_root, shared_skills_dir=shared_skills)

    with pytest.raises(RuntimeError, match="MobileGym runner failed") as exc:
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

    message = str(exc.value)
    assert "out text" in message
    assert "err text" in message
    assert str(benchmark_root / "runs" / "mobilegym" / "run-1-phase") in message


def test_mobilegym_backend_nonzero_with_rows_returns_converted_failures(monkeypatch, tmp_path: Path):
    from runner.skillopt import mobilegym_backend

    benchmark_root = tmp_path / "benchmark"
    _write_mobilegym_config(benchmark_root)
    shared_skills = _write_shared_skills(tmp_path)
    captured = {}

    def fake_run(command, *, env, **kwargs):
        batch = benchmark_root / "runs" / "mobilegym" / env["MOBILEGYM_BATCH_ID"]
        path = batch / "device_operator_train" / "shard-0" / "raw" / "run" / "errors.jsonl"
        path.parent.mkdir(parents=True)
        path.write_text(json.dumps({"id": "device_operator_train.case_one", "error": "failed"}) + "\n", encoding="utf-8")
        return subprocess.CompletedProcess(command, 7, stdout="out", stderr="err")

    def fake_load_rollouts(**kwargs):
        captured["batch_dir"] = kwargs["batch_dir"]
        return [RolloutResult(id="case_one", hard=0, soft=0.0, fail_reason="failed")]

    monkeypatch.setattr(mobilegym_backend.subprocess, "run", fake_run)
    monkeypatch.setattr(mobilegym_backend.mobilegym_results, "load_aiden_suite_rollouts", fake_load_rollouts)
    backend = mobilegym_backend.MobileGymBackend(benchmark_root=benchmark_root, shared_skills_dir=shared_skills)

    rollouts = backend.run_rollout(
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

    assert rollouts[0].hard == 0
    assert captured["batch_dir"] == benchmark_root / "runs" / "mobilegym" / "run-1-phase"
