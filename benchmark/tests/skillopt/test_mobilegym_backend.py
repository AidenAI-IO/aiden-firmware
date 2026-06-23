import json
import subprocess
from pathlib import Path

import pytest

from runner.models import HardAssertionResults, TaskResult
from runner.suite import HardAssertions, Suite, TaskSpec


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


def _task_result(*, status: str = "passed", artifact_dir: Path | None = None) -> TaskResult:
    return TaskResult(
        suite="device_operator_train",
        run_id="run-1",
        task_id="case_one",
        category="single_step",
        attempt=1,
        status=status,
        rubric=[],
        rubric_pass_count=0,
        rubric_total=0,
        hard_assertions=HardAssertionResults(response_exists=True),
        artifact_dir=str(artifact_dir or ""),
        description_for_judge="desc",
        rubric_spec=[],
    )


def test_mobilegym_backend_injects_candidate_skill_and_preserves_env(monkeypatch, tmp_path: Path):
    from runner.skillopt import mobilegym_backend

    benchmark_root = tmp_path / "benchmark"
    _write_mobilegym_config(benchmark_root)
    shared_skills = _write_shared_skills(tmp_path)
    captured = {}

    def fake_run(command, *, cwd, env, text, stdout, stderr, check, timeout):
        del text, stdout, stderr, check, timeout
        source_config = Path(env["AIDEN_SOURCE_CONFIG_DIR"])
        captured["command"] = command
        captured["cwd"] = cwd
        captured["env"] = env
        captured["agent_template"] = (source_config / "agent.toml.template").read_text(encoding="utf-8")
        captured["skill_text"] = (source_config / "skills" / "device-operator" / "SKILL.md").read_text(encoding="utf-8")
        return subprocess.CompletedProcess(command, 0, stdout="ok", stderr="")

    def fake_load_task_results(**kwargs):
        captured["batch_dir"] = kwargs["batch_dir"]
        return [_task_result(artifact_dir=kwargs["phase_artifact_dir"] / "case_one")]

    monkeypatch.setenv("PATH", "/bin:/usr/bin")
    monkeypatch.setenv("MODEL_API_KEY", "model-key")
    monkeypatch.setattr(mobilegym_backend.subprocess, "run", fake_run)
    monkeypatch.setattr(mobilegym_backend.mobilegym_results, "load_aiden_suite_task_results", fake_load_task_results)

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


def test_mobilegym_backend_child_summary_uses_aiden_judged_results(monkeypatch, tmp_path: Path):
    from runner.skillopt import mobilegym_backend

    benchmark_root = tmp_path / "benchmark"
    _write_mobilegym_config(benchmark_root)
    shared_skills = _write_shared_skills(tmp_path)

    def fake_run(command, *, env, **kwargs):
        del command, kwargs
        batch = benchmark_root / "runs" / "mobilegym" / env["MOBILEGYM_BATCH_ID"]
        batch.mkdir(parents=True)
        (batch / "summary.json").write_text(json.dumps({
            "batch_id": env["MOBILEGYM_BATCH_ID"],
            "tasks": 1,
            "passed": 1,
            "failed": 0,
            "error": 0,
            "pass_rate": 1.0,
        }), encoding="utf-8")
        return subprocess.CompletedProcess([], 0, stdout="ok", stderr="")

    def fake_load_task_results(**kwargs):
        artifact_dir = kwargs["phase_artifact_dir"] / "case_one"
        artifact_dir.mkdir(parents=True)
        (artifact_dir / "history.json").write_text("[]", encoding="utf-8")
        (artifact_dir / "trace.json").write_text("{}", encoding="utf-8")
        return [TaskResult(
            suite="device_operator_train",
            run_id="run-1",
            task_id="case_one",
            category="single_step",
            attempt=1,
            status="failed",
            rubric=[],
            rubric_pass_count=0,
            rubric_total=1,
            hard_assertions=HardAssertionResults(response_exists=False),
            metrics={"error": "Aiden suite judge failed"},
            artifact_dir=str(artifact_dir),
            description_for_judge="desc",
            rubric_spec=[{"id": "ok", "check": "Task succeeds."}],
        )]

    monkeypatch.setattr(mobilegym_backend.subprocess, "run", fake_run)
    monkeypatch.setattr(mobilegym_backend.mobilegym_results, "load_aiden_suite_task_results", fake_load_task_results)
    backend = mobilegym_backend.MobileGymBackend(benchmark_root=benchmark_root, shared_skills_dir=shared_skills)

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

    batch = benchmark_root / "runs" / "mobilegym" / "run-1-step_01_train"
    summary = json.loads((batch / "summary.json").read_text(encoding="utf-8"))
    health = json.loads((batch / "run_health.json").read_text(encoding="utf-8"))
    result_row = json.loads((batch / "results.jsonl").read_text(encoding="utf-8").splitlines()[0])
    assert rollouts[0].hard == 0
    assert summary["judge_source"] == "aiden_suite"
    assert summary["passed"] == 0
    assert summary["failed"] == 1
    assert health["passed"] == 1
    assert result_row["artifact_dir"] == str(batch / "tasks" / "case_one")
    assert (batch / "index.html").exists()
    assert (batch / "tasks" / "case_one" / "trace.json").exists()


def test_mobilegym_backend_passes_subprocess_timeout(monkeypatch, tmp_path: Path):
    from runner.skillopt import mobilegym_backend

    benchmark_root = tmp_path / "benchmark"
    _write_mobilegym_config(benchmark_root)
    shared_skills = _write_shared_skills(tmp_path)
    captured = {}

    def fake_run(command, **kwargs):
        captured["timeout"] = kwargs.get("timeout")
        return subprocess.CompletedProcess(command, 0, stdout="ok", stderr="")

    monkeypatch.setattr(mobilegym_backend.subprocess, "run", fake_run)
    monkeypatch.setattr(
        mobilegym_backend.mobilegym_results,
        "load_aiden_suite_task_results",
        lambda **kwargs: [_task_result(artifact_dir=kwargs["phase_artifact_dir"] / "case_one")],
    )
    backend = mobilegym_backend.MobileGymBackend(
        benchmark_root=benchmark_root,
        shared_skills_dir=shared_skills,
        env={"MOBILEGYM_RUN_TIMEOUT_SEC": "12"},
    )

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

    assert captured["timeout"] == 12


def test_mobilegym_backend_wraps_subprocess_timeout(monkeypatch, tmp_path: Path):
    from runner.skillopt import mobilegym_backend

    benchmark_root = tmp_path / "benchmark"
    _write_mobilegym_config(benchmark_root)
    shared_skills = _write_shared_skills(tmp_path)

    def fake_run(command, **kwargs):
        raise subprocess.TimeoutExpired(command, kwargs.get("timeout"))

    monkeypatch.setattr(mobilegym_backend.subprocess, "run", fake_run)
    backend = mobilegym_backend.MobileGymBackend(
        benchmark_root=benchmark_root,
        shared_skills_dir=shared_skills,
        env={"MOBILEGYM_RUN_TIMEOUT_SEC": "12"},
    )

    with pytest.raises(RuntimeError, match="timed out after 12s"):
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
    from runner.skillopt import mobilegym_backend

    benchmark_root = tmp_path / "benchmark"
    _write_mobilegym_config(benchmark_root)
    shared_skills = _write_shared_skills(tmp_path)
    backend = mobilegym_backend.MobileGymBackend(benchmark_root=benchmark_root, shared_skills_dir=shared_skills)
    source_config = tmp_path / "source-config"

    with pytest.raises(ValueError, match="invalid skill_name"):
        backend._prepare_source_config(source_config, "../evil", "candidate skill")

    assert not (source_config / "evil" / "SKILL.md").exists()


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

    def fake_load_task_results(**kwargs):
        captured["batch_dir"] = kwargs["batch_dir"]
        return [_task_result(status="failed", artifact_dir=kwargs["phase_artifact_dir"] / "case_one")]

    monkeypatch.setattr(mobilegym_backend.subprocess, "run", fake_run)
    monkeypatch.setattr(mobilegym_backend.mobilegym_results, "load_aiden_suite_task_results", fake_load_task_results)
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
