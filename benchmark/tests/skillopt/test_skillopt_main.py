from pathlib import Path
import json

from runner.skillopt import main
from runner.skillopt.types import Edit, OptimizationResult, StepDecision


def test_resolve_skill_path_uses_packaged_board_skills(monkeypatch, tmp_path: Path):
    monkeypatch.delenv("AIDEN_SKILLS_DIR", raising=False)
    skill_path = tmp_path / "skills" / "device-operator" / "SKILL.md"
    skill_path.parent.mkdir(parents=True)
    skill_path.write_text("---\nname: device-operator\n---\n", encoding="utf-8")
    monkeypatch.setattr(main, "REPO_ROOT", tmp_path)

    assert main._resolve_skill_path("device-operator") == skill_path


def test_resolve_skill_path_prefers_env_skills_dir(monkeypatch, tmp_path: Path):
    repo_skill = tmp_path / "src" / "agent" / "config" / "skills" / "device-operator" / "SKILL.md"
    repo_skill.parent.mkdir(parents=True)
    repo_skill.write_text("repo", encoding="utf-8")
    env_root = tmp_path / "custom-skills"
    env_skill = env_root / "device-operator" / "SKILL.md"
    env_skill.parent.mkdir(parents=True)
    env_skill.write_text("env", encoding="utf-8")
    monkeypatch.setattr(main, "REPO_ROOT", tmp_path)
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


def test_cli_uses_explicit_train_and_selection_suites(monkeypatch, tmp_path: Path):
    skill_path = tmp_path / "src" / "agent" / "config" / "skills" / "device-operator" / "SKILL.md"
    skill_path.parent.mkdir(parents=True)
    skill_path.write_text("original skill", encoding="utf-8")
    _write_suite(
        tmp_path / "benchmark" / "suites" / "skillopt" / "train_v1.json",
        "train_v1",
        ["train_one", "train_two"],
    )
    _write_suite(
        tmp_path / "benchmark" / "suites" / "skillopt" / "validation_v1.json",
        "validation_v1",
        ["validation_one"],
    )
    captured = {}

    def fake_optimize_skill(cfg):
        captured["train_ids"] = [task.id for task in cfg.train_tasks]
        captured["selection_ids"] = [task.id for task in cfg.selection_tasks]
        captured["suite_name"] = cfg.suite.name
        captured["selection_suite_name"] = cfg.selection_suite.name
        return OptimizationResult(
            skill_name=cfg.skill_name,
            initial_score=0.0,
            best_score=1.0,
            best_skill="optimized skill",
        )

    monkeypatch.setattr(main, "REPO_ROOT", tmp_path)
    monkeypatch.setattr(main, "optimize_skill", fake_optimize_skill)

    rc = main.cli([
        "--skill", "device-operator",
        "--train-suite", "skillopt/train_v1",
        "--validation-suite", "skillopt/validation_v1",
        "--output", str(tmp_path / "optimized.md"),
    ])

    assert rc == 0
    assert captured == {
        "train_ids": ["train_one", "train_two"],
        "selection_ids": ["validation_one"],
        "suite_name": "train_v1",
        "selection_suite_name": "validation_v1",
    }


def test_cli_writes_run_artifacts_for_web_report(monkeypatch, tmp_path: Path):
    skill_path = tmp_path / "src" / "agent" / "config" / "skills" / "device-operator" / "SKILL.md"
    skill_path.parent.mkdir(parents=True)
    skill_path.write_text("original skill\n", encoding="utf-8")
    _write_suite(
        tmp_path / "benchmark" / "suites" / "skillopt" / "train_v1.json",
        "train_v1",
        ["train_one"],
    )
    _write_suite(
        tmp_path / "benchmark" / "suites" / "skillopt" / "validation_v1.json",
        "validation_v1",
        ["validation_one", "validation_two"],
    )
    artifact_root = tmp_path / "benchmark" / "runs"

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

    monkeypatch.setattr(main, "REPO_ROOT", tmp_path)
    monkeypatch.setattr(main, "optimize_skill", fake_optimize_skill)

    rc = main.cli([
        "--skill", "device-operator",
        "--train-suite", "skillopt/train_v1",
        "--validation-suite", "skillopt/validation_v1",
        "--artifact-root", str(artifact_root),
        "--run-id", "skillopt-test-run",
        "--output", str(artifact_root / "skillopt-test-run" / "best_skill.md"),
    ])

    assert rc == 0
    run_dir = artifact_root / "skillopt-test-run"
    manifest = json.loads((run_dir / "manifest.json").read_text(encoding="utf-8"))
    result = json.loads((run_dir / "result.json").read_text(encoding="utf-8"))
    assert manifest["mode"] == "skillopt"
    assert manifest["run_id"] == "skillopt-test-run"
    assert manifest["skill"] == "device-operator"
    assert manifest["totals"] == {"tasks": 2, "passed": 2, "failed": 0}
    assert result["initial_score"] == 0.25
    assert result["best_score"] == 0.75
    assert "-original skill" in (run_dir / "diff.patch").read_text(encoding="utf-8")
    assert "+optimized skill" in (run_dir / "diff.patch").read_text(encoding="utf-8")
    assert "SkillOpt Report" in (run_dir / "report.html").read_text(encoding="utf-8")
