from pathlib import Path
import json
import typing

import pytest

from skillopt import main
from skillopt.types import Edit, OptimizationResult, PhaseSummary, ScoreSummary, StepDecision


TRAIN_LABEL = "skillopt/device-operator/device_operator_train"
VERIFICATION_LABEL = "skillopt/device-operator/device_operator_verification"


class FakeDeviceBackend:
    def __init__(self, *, agent_url: str):
        self.agent_url = agent_url

    def close(self):
        pass


class FakeMobileGymBackend:
    def __init__(self, *, benchmark_root: Path, shared_skills_dir: Path, parallel: int):
        self.benchmark_root = benchmark_root
        self.shared_skills_dir = shared_skills_dir
        self.parallel = parallel

    def close(self):
        pass


def _set_roots(monkeypatch: pytest.MonkeyPatch, tmp_path: Path) -> None:
    monkeypatch.setattr(main, "REPO_ROOT", tmp_path)
    monkeypatch.setattr(main, "SKILLOPT_ROOT", tmp_path / "skillopt")


def test_resolve_skill_path_uses_packaged_board_skills(monkeypatch, tmp_path: Path):
    monkeypatch.delenv("AIDEN_SKILLS_DIR", raising=False)
    skill_path = tmp_path / "skills" / "device-operator" / "SKILL.md"
    skill_path.parent.mkdir(parents=True)
    skill_path.write_text("---\nname: device-operator\n---\n", encoding="utf-8")
    _set_roots(monkeypatch, tmp_path)

    assert main._resolve_skill_path("device-operator") == skill_path


def test_resolve_skill_path_prefers_env_skills_dir(monkeypatch, tmp_path: Path):
    repo_skill = tmp_path / "src" / "agent" / "config" / "skills" / "device-operator" / "SKILL.md"
    repo_skill.parent.mkdir(parents=True)
    repo_skill.write_text("repo", encoding="utf-8")
    env_root = tmp_path / "custom-skills"
    env_skill = env_root / "device-operator" / "SKILL.md"
    env_skill.parent.mkdir(parents=True)
    env_skill.write_text("env", encoding="utf-8")
    _set_roots(monkeypatch, tmp_path)
    monkeypatch.setenv("AIDEN_SKILLS_DIR", str(env_root))

    assert main._resolve_skill_path("device-operator") == env_skill


def _write_suite(path: Path, name: str, task_ids: list[str]) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps({
        "name": name,
        "tasks": [
            {
                "id": task_id,
                "category": "single_step",
                "description_for_judge": f"Judge {task_id}",
                "prompt": f"Run {task_id}",
                "rubric": [{"id": "ok", "check": "Task succeeds."}],
                "hard_assertions": {"min_tool_calls": 0, "max_tool_calls": 5},
            }
            for task_id in task_ids
        ],
    }), encoding="utf-8")


def _write_device_operator_suites(tmp_path: Path) -> None:
    _write_suite(
        tmp_path / "skillopt" / "suites" / "device-operator" / "device_operator_train.json",
        "device_operator_train",
        ["train_one", "train_two"],
    )
    _write_suite(
        tmp_path / "skillopt" / "suites" / "device-operator" / "device_operator_verification.json",
        "device_operator_verification",
        ["validation_one", "validation_two"],
    )


def test_cli_uses_explicit_train_and_selection_suites(monkeypatch, tmp_path: Path):
    skill_path = tmp_path / "src" / "agent" / "config" / "skills" / "device-operator" / "SKILL.md"
    skill_path.parent.mkdir(parents=True)
    skill_path.write_text("original skill", encoding="utf-8")
    _write_device_operator_suites(tmp_path)
    captured = {}

    def fake_optimize_skill(cfg):
        captured["train_ids"] = [task.id for task in cfg.train_tasks]
        captured["selection_ids"] = [task.id for task in cfg.selection_tasks]
        captured["suite_name"] = cfg.suite.name
        captured["selection_suite_name"] = cfg.selection_suite.name
        captured["rollout_backend"] = cfg.rollout_backend
        return OptimizationResult(
            skill_name=cfg.skill_name,
            initial_score=0.0,
            best_score=1.0,
            best_skill="optimized skill",
        )

    _set_roots(monkeypatch, tmp_path)
    monkeypatch.setattr(main, "optimize_skill", fake_optimize_skill)
    monkeypatch.setattr(main, "AidenDeviceBackend", FakeDeviceBackend, raising=False)

    rc = main.cli([
        "--skill", "device-operator",
        "--train-suite", TRAIN_LABEL,
        "--validation-suite", VERIFICATION_LABEL,
        "--output", str(tmp_path / "optimized.md"),
    ])

    assert rc == 0
    assert captured == {
        "train_ids": ["train_one", "train_two"],
        "selection_ids": ["validation_one", "validation_two"],
        "suite_name": "device_operator_train",
        "selection_suite_name": "device_operator_verification",
        "rollout_backend": captured["rollout_backend"],
    }
    assert isinstance(captured["rollout_backend"], FakeDeviceBackend)
    assert captured["rollout_backend"].agent_url == "http://localhost:8080"


def test_cli_writes_run_artifacts_for_web_report(monkeypatch, tmp_path: Path):
    skill_path = tmp_path / "src" / "agent" / "config" / "skills" / "device-operator" / "SKILL.md"
    skill_path.parent.mkdir(parents=True)
    skill_path.write_text("original skill\n", encoding="utf-8")
    _write_device_operator_suites(tmp_path)
    artifact_root = tmp_path / "skillopt" / "runs"

    def fake_optimize_skill(cfg):
        return OptimizationResult(
            skill_name=cfg.skill_name,
            initial_score=0.25,
            best_score=0.75,
            best_skill="optimized skill\n",
            steps=[
                StepDecision(
                    step=1,
                    candidate_score=0.75,
                    current_score=0.25,
                    accepted=True,
                    reason="candidate improved",
                    edits_applied=[Edit(op="append", content="new rule")],
                )
            ],
            accepted_count=1,
            rejected_count=0,
        )

    _set_roots(monkeypatch, tmp_path)
    monkeypatch.setattr(main, "optimize_skill", fake_optimize_skill)
    monkeypatch.setattr(main, "AidenDeviceBackend", FakeDeviceBackend, raising=False)

    rc = main.cli([
        "--backend", "device",
        "--skill", "device-operator",
        "--train-suite", TRAIN_LABEL,
        "--validation-suite", VERIFICATION_LABEL,
        "--artifact-root", str(artifact_root),
        "--run-id", "skillopt-test-run",
        "--output", str(artifact_root / "skillopt-test-run" / "best_skill.md"),
    ])

    assert rc == 0
    run_dir = artifact_root / "skillopt-test-run"
    manifest = json.loads((run_dir / "manifest.json").read_text(encoding="utf-8"))
    result = json.loads((run_dir / "result.json").read_text(encoding="utf-8"))
    assert manifest["mode"] == "skillopt"
    assert manifest["backend"] == "device"
    assert manifest["run_id"] == "skillopt-test-run"
    assert manifest["skill"] == "device-operator"
    assert manifest["totals"] == {"tasks": 2, "passed": 2, "failed": 0}
    assert result["initial_score"] == 0.25
    assert result["best_score"] == 0.75
    assert "-original skill" in (run_dir / "diff.patch").read_text(encoding="utf-8")
    assert "+optimized skill" in (run_dir / "diff.patch").read_text(encoding="utf-8")
    assert "SkillOpt Report" in (run_dir / "report.html").read_text(encoding="utf-8")


def test_cli_writes_aggregated_skillopt_summary_report(monkeypatch, tmp_path: Path):
    skill_path = tmp_path / "src" / "agent" / "config" / "skills" / "device-operator" / "SKILL.md"
    skill_path.parent.mkdir(parents=True)
    skill_path.write_text("original skill\n", encoding="utf-8")
    _write_device_operator_suites(tmp_path)
    artifact_root = tmp_path / "skillopt" / "runs"

    def fake_optimize_skill(cfg):
        train_score = ScoreSummary(hard=0.25, soft=0.50, n=4, n_passed=1)
        candidate_score = ScoreSummary(hard=0.75, soft=0.875, n=4, n_passed=3)
        return OptimizationResult(
            skill_name=cfg.skill_name,
            initial_score=0.50,
            best_score=0.75,
            best_skill="optimized skill\n",
            phase_summaries=[
                PhaseSummary(
                    phase="baseline_selection",
                    kind="verification",
                    suite_name="device_operator_verification",
                    score=ScoreSummary(hard=0.50, soft=0.75, n=4, n_passed=2),
                ),
                PhaseSummary(
                    phase="step_01_train",
                    kind="train",
                    suite_name="device_operator_train",
                    score=train_score,
                ),
                PhaseSummary(
                    phase="step_01_selection",
                    kind="verification",
                    suite_name="device_operator_verification",
                    score=candidate_score,
                ),
            ],
            steps=[
                StepDecision(
                    step=1,
                    candidate_score=0.75,
                    current_score=0.50,
                    accepted=True,
                    reason="candidate improved",
                    edits_applied=[Edit(op="append", content="new rule")],
                    train_score=train_score,
                    candidate_selection_score=candidate_score,
                    patch_reasoning="failure analyst found missing launch guidance\nSecond line should wrap cleanly",
                )
            ],
            accepted_count=1,
            rejected_count=0,
        )

    _set_roots(monkeypatch, tmp_path)
    monkeypatch.setattr(main, "optimize_skill", fake_optimize_skill)
    monkeypatch.setattr(main, "MobileGymBackend", FakeMobileGymBackend, raising=False)

    rc = main.cli([
        "--backend", "mobilegym",
        "--skill", "device-operator",
        "--train-suite", TRAIN_LABEL,
        "--validation-suite", VERIFICATION_LABEL,
        "--artifact-root", str(artifact_root),
        "--run-id", "skillopt-summary-run",
        "--output", str(artifact_root / "skillopt-summary-run" / "best_skill.md"),
    ])

    assert rc == 0
    run_dir = artifact_root / "skillopt-summary-run"
    manifest = json.loads((run_dir / "manifest.json").read_text(encoding="utf-8"))
    assert manifest["score_summary"]["baseline_verification"]["hard"] == 0.50
    assert manifest["score_summary"]["latest_train"]["hard"] == 0.25
    assert manifest["score_summary"]["best_verification"]["hard"] == 0.75
    assert manifest["linked_reports"]["step_01_train"] == "/benchmark/report/skillopt-summary-run-step_01_train"

    report = (run_dir / "report.html").read_text(encoding="utf-8")
    assert "Scores" in report
    assert "baseline_selection" in report
    assert "step_01_train" in report
    assert "0.25" in report
    assert "0.75" in report
    assert "failure analyst found missing launch guidance" in report
    assert "Second line should wrap cleanly" in report
    assert "pre.skillopt-reasoning" in report
    assert "white-space:pre-wrap" in report
    assert '<pre class="skillopt-reasoning">' in report
    assert "new rule" in report
    assert "best_skill.md" in report
    assert "diff.patch" in report
    assert 'href="best_skill.md"' in report
    assert 'href="diff.patch"' in report
    assert 'href="result.json"' in report
    assert 'class="artifact-link"' in report
    assert 'data-artifact-url="best_skill.md"' in report
    assert 'drawer-backdrop' in report
    assert 'class="drawer"' in report
    assert "function openArtifactDrawer" in report
    assert "/benchmark/report/skillopt-summary-run-step_01_train" in report


def test_web_report_shows_raw_mobilegym_and_skillopt_scores_for_no_edit_run(tmp_path: Path):
    artifact_root = tmp_path / "skillopt" / "runs"
    run_id = "skillopt-no-edit-run"
    raw_dir = tmp_path / "skillopt" / "mobilegym" / f"{run_id}-step_01_train"
    raw_dir.mkdir(parents=True)
    (raw_dir / "summary.json").write_text(json.dumps({
        "tasks": 12,
        "passed": 11,
        "failed": 0,
        "error": 1,
        "pass_rate": 11 / 12,
    }), encoding="utf-8")
    cfg = main.OptimizationConfig(
        skill_name="device-operator",
        skill_path=tmp_path / "SKILL.md",
        suite=object(),
        train_tasks=[object()] * 12,
        selection_tasks=[object()] * 6,
        artifact_root=artifact_root,
        run_id=run_id,
        optimizer_cfg=main.OptimizerConfig(),
    )
    result = OptimizationResult(
        skill_name="device-operator",
        initial_score=5 / 6,
        best_score=5 / 6,
        best_skill="best skill\n",
        phase_summaries=[
            PhaseSummary(
                phase="step_01_train",
                kind="train",
                suite_name="device_operator_train",
                score=ScoreSummary(hard=10 / 12, soft=10 / 12, n=12, n_passed=10),
            )
        ],
        stop_reason="step 1: aggregate produced 0 edits after dedup",
    )

    main._write_web_artifacts(
        cfg=cfg,
        result=result,
        original_skill="best skill\n",
        diff_text="",
        optimizer_model="optimizer",
        judge_model="judge",
        train_suite_label=TRAIN_LABEL,
        selection_suite_label=VERIFICATION_LABEL,
        backend="mobilegym",
    )

    run_dir = artifact_root / run_id
    manifest = json.loads((run_dir / "manifest.json").read_text(encoding="utf-8"))
    assert manifest["raw_score_summary"]["step_01_train"] == {
        "passed": 11,
        "tasks": 12,
        "failed": 0,
        "error": 1,
        "pass_rate": 11 / 12,
    }
    report = (run_dir / "report.html").read_text(encoding="utf-8")
    assert "MobileGym result" in report
    assert "Optimization score" in report
    assert "11/12" in report
    assert "10/12" in report
    assert "Task pass rate" in report
    assert "Rubric pass rate" in report
    assert "Hard" not in report
    assert "Soft" not in report
    assert "Step 1" in report
    assert "stopped before candidate verification" in report
    assert "aggregate produced 0 edits after dedup" in report


def test_cli_dry_run_does_not_write_output_or_web_artifacts(monkeypatch, tmp_path: Path):
    skill_path = tmp_path / "src" / "agent" / "config" / "skills" / "device-operator" / "SKILL.md"
    skill_path.parent.mkdir(parents=True)
    skill_path.write_text("original skill\n", encoding="utf-8")
    _write_device_operator_suites(tmp_path)
    artifact_root = tmp_path / "skillopt" / "runs"
    output_path = tmp_path / "optimized.md"

    def fake_optimize_skill(cfg):
        return OptimizationResult(
            skill_name=cfg.skill_name,
            initial_score=0.0,
            best_score=1.0,
            best_skill="optimized skill\n",
        )

    _set_roots(monkeypatch, tmp_path)
    monkeypatch.setattr(main, "optimize_skill", fake_optimize_skill)
    monkeypatch.setattr(main, "AidenDeviceBackend", FakeDeviceBackend, raising=False)

    rc = main.cli([
        "--skill", "device-operator",
        "--train-suite", TRAIN_LABEL,
        "--validation-suite", VERIFICATION_LABEL,
        "--artifact-root", str(artifact_root),
        "--run-id", "skillopt-dry-run",
        "--output", str(output_path),
        "--dry-run",
    ])

    assert rc == 0
    assert not output_path.exists()
    assert not (artifact_root / "skillopt-dry-run").exists()


def test_web_artifact_type_hints_resolve():
    hints = typing.get_type_hints(main._write_web_artifacts)

    assert hints["result"] is OptimizationResult


@pytest.mark.parametrize("skill", ["../device-operator", ".", ".."])
def test_cli_rejects_unsafe_skill_names(monkeypatch, tmp_path: Path, capsys, skill: str):
    _set_roots(monkeypatch, tmp_path)

    rc = main.cli([
        "--skill", skill,
        "--train-suite", TRAIN_LABEL,
        "--validation-suite", VERIFICATION_LABEL,
    ])

    assert rc == 2
    assert "invalid skill name" in capsys.readouterr().err


def test_cli_rejects_cross_skill_skillopt_suite(monkeypatch, tmp_path: Path, capsys):
    skill_path = tmp_path / "src" / "agent" / "config" / "skills" / "device-operator" / "SKILL.md"
    skill_path.parent.mkdir(parents=True)
    skill_path.write_text("skill", encoding="utf-8")
    _set_roots(monkeypatch, tmp_path)

    rc = main.cli([
        "--skill", "device-operator",
        "--train-suite", "skillopt/planner/train",
        "--validation-suite", VERIFICATION_LABEL,
    ])

    assert rc == 2
    assert "must be under skillopt/device-operator" in capsys.readouterr().err


def test_cli_rejects_unsafe_suite_traversal(monkeypatch, tmp_path: Path, capsys):
    skill_path = tmp_path / "src" / "agent" / "config" / "skills" / "device-operator" / "SKILL.md"
    skill_path.parent.mkdir(parents=True)
    skill_path.write_text("skill", encoding="utf-8")
    _set_roots(monkeypatch, tmp_path)

    rc = main.cli([
        "--skill", "device-operator",
        "--train-suite", "skillopt/device-operator/../x",
        "--validation-suite", VERIFICATION_LABEL,
    ])

    assert rc == 2
    assert "invalid suite label" in capsys.readouterr().err


def test_cli_rejects_unsafe_split_suite_traversal(monkeypatch, tmp_path: Path, capsys):
    skill_path = tmp_path / "src" / "agent" / "config" / "skills" / "device-operator" / "SKILL.md"
    skill_path.parent.mkdir(parents=True)
    skill_path.write_text("skill", encoding="utf-8")
    _set_roots(monkeypatch, tmp_path)

    rc = main.cli([
        "--skill", "device-operator",
        "--suite", "../outside",
    ])

    assert rc == 2
    assert "invalid suite label" in capsys.readouterr().err


def test_cli_rejects_cross_skill_split_suite(monkeypatch, tmp_path: Path, capsys):
    skill_path = tmp_path / "src" / "agent" / "config" / "skills" / "device-operator" / "SKILL.md"
    skill_path.parent.mkdir(parents=True)
    skill_path.write_text("skill", encoding="utf-8")
    _set_roots(monkeypatch, tmp_path)

    rc = main.cli([
        "--skill", "device-operator",
        "--suite", "skillopt/planner/train",
    ])

    assert rc == 2
    assert "must be under skillopt/device-operator" in capsys.readouterr().err


def test_cli_rejects_invalid_run_id(monkeypatch, tmp_path: Path, capsys):
    skill_path = tmp_path / "src" / "agent" / "config" / "skills" / "device-operator" / "SKILL.md"
    skill_path.parent.mkdir(parents=True)
    skill_path.write_text("skill", encoding="utf-8")
    _write_device_operator_suites(tmp_path)
    _set_roots(monkeypatch, tmp_path)
    monkeypatch.setattr(
        main,
        "optimize_skill",
        lambda cfg: (_ for _ in ()).throw(AssertionError("optimize_skill should not be called")),
    )

    rc = main.cli([
        "--skill", "device-operator",
        "--train-suite", TRAIN_LABEL,
        "--validation-suite", VERIFICATION_LABEL,
        "--run-id", "../x",
    ])

    assert rc == 2
    assert "invalid run_id" in capsys.readouterr().err


def test_cli_rejects_invalid_backend():
    with pytest.raises(SystemExit):
        main.cli([
            "--backend", "simulator",
            "--skill", "device-operator",
            "--train-suite", TRAIN_LABEL,
            "--validation-suite", VERIFICATION_LABEL,
        ])


def test_cli_constructs_mobilegym_backend(monkeypatch, tmp_path: Path):
    skill_path = tmp_path / "src" / "agent" / "config" / "skills" / "device-operator" / "SKILL.md"
    skill_path.parent.mkdir(parents=True)
    skill_path.write_text("skill", encoding="utf-8")
    _write_device_operator_suites(tmp_path)
    captured = {}

    def fake_optimize_skill(cfg):
        captured["rollout_backend"] = cfg.rollout_backend
        return OptimizationResult(
            skill_name=cfg.skill_name,
            initial_score=0.0,
            best_score=1.0,
            best_skill="optimized skill",
        )

    _set_roots(monkeypatch, tmp_path)
    monkeypatch.setattr(main, "optimize_skill", fake_optimize_skill)
    monkeypatch.setattr(main, "MobileGymBackend", FakeMobileGymBackend, raising=False)

    rc = main.cli([
        "--backend", "mobilegym",
        "--mobilegym-parallel", "3",
        "--skill", "device-operator",
        "--train-suite", TRAIN_LABEL,
        "--validation-suite", VERIFICATION_LABEL,
    ])

    assert rc == 0
    backend = captured["rollout_backend"]
    assert isinstance(backend, FakeMobileGymBackend)
    assert backend.benchmark_root == tmp_path / "benchmark"
    assert backend.shared_skills_dir == tmp_path / "src" / "agent" / "config" / "skills"
    assert backend.parallel == 3


def test_resolve_skill_path_prefers_shared_skill_over_mobilegym_template(monkeypatch, tmp_path: Path):
    shared = tmp_path / "src" / "agent" / "config" / "skills" / "device-operator" / "SKILL.md"
    shared.parent.mkdir(parents=True)
    shared.write_text("shared", encoding="utf-8")
    template = tmp_path / "benchmark" / "mobilegym" / "config" / "skills" / "device-operator" / "SKILL.md"
    template.parent.mkdir(parents=True)
    template.write_text("template", encoding="utf-8")
    _set_roots(monkeypatch, tmp_path)
    monkeypatch.delenv("AIDEN_SKILLS_DIR", raising=False)

    assert main._resolve_skill_path("device-operator") == shared


def test_cli_does_not_use_mobilegym_template_as_skill_source(monkeypatch, tmp_path: Path, capsys):
    template = tmp_path / "benchmark" / "mobilegym" / "config" / "skills" / "device-operator" / "SKILL.md"
    template.parent.mkdir(parents=True)
    template.write_text("template", encoding="utf-8")
    _write_device_operator_suites(tmp_path)
    _set_roots(monkeypatch, tmp_path)
    monkeypatch.delenv("AIDEN_SKILLS_DIR", raising=False)

    rc = main.cli([
        "--skill", "device-operator",
        "--train-suite", TRAIN_LABEL,
        "--validation-suite", VERIFICATION_LABEL,
    ])

    assert rc == 2
    assert "skill not found" in capsys.readouterr().err
