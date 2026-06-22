"""Unit tests for SkillOpt orchestrator."""
from pathlib import Path

import pytest

from runner.suite import HardAssertions, RubricItem, Suite, TaskSpec
from runner.skillopt import orchestrator
from runner.skillopt.types import Edit, Patch, RawPatch, RolloutResult


def _task(task_id: str) -> TaskSpec:
    return TaskSpec(
        id=task_id,
        category="single_step",
        description_for_judge="desc",
        prompt="prompt",
        rubric=[RubricItem(id="r1", check="check")],
        hard_assertions=HardAssertions(),
    )


def _suite(tmp_path: Path, name: str = "s") -> Suite:
    return Suite(
        name=name,
        global_reset={},
        tasks=[],
        sha256="sha",
        source_path=tmp_path / f"{name}.json",
    )


class CapturingBackend:
    def __init__(self, *, fail: bool = False):
        self.calls = []
        self.close_count = 0
        self.fail = fail

    def close(self):
        self.close_count += 1

    def run_rollout(
        self,
        *,
        suite,
        tasks,
        skill_name,
        skill_path,
        skill_text,
        phase,
        run_id,
        run_root,
        judge_cfg,
    ):
        del skill_name, skill_path, run_id, run_root, judge_cfg
        if self.fail:
            raise RuntimeError("rollout failed")
        self.calls.append((phase, skill_text, suite.name, [task.id for task in tasks]))
        hard = 1 if phase.endswith("_selection") and phase != "baseline_selection" else 0
        return [RolloutResult(id=phase, hard=hard, soft=float(hard))]


def _patch_optimizer(monkeypatch):
    monkeypatch.setattr(
        orchestrator,
        "run_reflect",
        lambda *args, **kwargs: [RawPatch(patch=Patch(edits=[Edit(op="append", content="candidate")]))],
    )
    monkeypatch.setattr(
        orchestrator,
        "aggregate",
        lambda raw_patches, edit_budget: Patch(edits=[Edit(op="append", content="candidate")]),
    )
    monkeypatch.setattr(
        orchestrator,
        "apply_patch_with_report",
        lambda current, patch: (current + "\ncandidate", []),
    )


def _backend_config(tmp_path: Path, backend, *, budget: int = 1) -> orchestrator.OptimizationConfig:
    skill_path = tmp_path / "SKILL.md"
    skill_path.write_text("base", encoding="utf-8")
    return orchestrator.OptimizationConfig(
        skill_name="device-operator",
        skill_path=skill_path,
        suite=_suite(tmp_path, "train_suite"),
        train_suite=_suite(tmp_path, "train_suite"),
        selection_suite=_suite(tmp_path, "validation_suite"),
        train_tasks=[_task("train")],
        selection_tasks=[_task("selection")],
        budget=budget,
        min_delta=0.0,
        run_id="run-001",
        artifact_root=tmp_path / "runs" / "skillopt",
        rollout_backend=backend,
    )


def test_optimize_skill_sends_skill_text_to_backend_for_each_phase(monkeypatch, tmp_path):
    backend = CapturingBackend()
    _patch_optimizer(monkeypatch)

    result = orchestrator.optimize_skill(_backend_config(tmp_path, backend))

    assert result.accepted_count == 1
    assert backend.calls == [
        ("baseline_selection", "base", "validation_suite", ["selection"]),
        ("step_01_train", "base", "train_suite", ["train"]),
        ("step_01_selection", "base\ncandidate", "validation_suite", ["selection"]),
    ]


def test_optimize_skill_uses_accepted_candidate_for_next_train(monkeypatch, tmp_path):
    backend = CapturingBackend()
    _patch_optimizer(monkeypatch)

    orchestrator.optimize_skill(_backend_config(tmp_path, backend, budget=2))

    assert ("step_02_train", "base\ncandidate", "train_suite", ["train"]) in backend.calls


def test_optimize_skill_closes_injected_backend_on_success(monkeypatch, tmp_path):
    backend = CapturingBackend()
    _patch_optimizer(monkeypatch)

    orchestrator.optimize_skill(_backend_config(tmp_path, backend))

    assert backend.close_count == 1


def test_optimize_skill_closes_injected_backend_when_rollout_raises(tmp_path):
    backend = CapturingBackend(fail=True)

    with pytest.raises(RuntimeError, match="rollout failed"):
        orchestrator.optimize_skill(_backend_config(tmp_path, backend))

    assert backend.close_count == 1


def test_optimize_skill_scopes_artifacts_by_run_id(monkeypatch, tmp_path):
    skill_path = tmp_path / "SKILL.md"
    skill_path.write_text("base", encoding="utf-8")
    run_id = "run-001"
    artifact_root = tmp_path / "runs" / "skillopt"
    seen_artifact_roots = []

    class ArtifactBackend:
        def close(self):
            pass

        def run_rollout(self, *, phase, run_root, **kwargs):
            del kwargs
            seen_artifact_roots.append((phase, run_root))
            hard = 1 if phase.endswith("_selection") and phase != "baseline_selection" else 0
            return [RolloutResult(id=phase, hard=hard, soft=float(hard))]

    _patch_optimizer(monkeypatch)

    cfg = orchestrator.OptimizationConfig(
        skill_name="device-operator",
        skill_path=skill_path,
        suite=_suite(tmp_path),
        train_tasks=[_task("train")],
        selection_tasks=[_task("selection")],
        budget=1,
        min_delta=0.0,
        run_id=run_id,
        artifact_root=artifact_root,
        rollout_backend=ArtifactBackend(),
    )

    result = orchestrator.optimize_skill(cfg)

    run_root = artifact_root / run_id
    assert result.accepted_count == 1
    assert seen_artifact_roots == [
        ("baseline_selection", run_root),
        ("step_01_train", run_root),
        ("step_01_selection", run_root),
    ]
    assert (run_root / "step_01" / "candidate.md").exists()
    assert (run_root / "best_skill.md").exists()


def test_optimize_skill_uses_explicit_train_and_selection_suites(monkeypatch, tmp_path):
    skill_path = tmp_path / "SKILL.md"
    skill_path.write_text("base", encoding="utf-8")
    seen = []

    class SuiteBackend:
        def close(self):
            pass

        def run_rollout(self, *, phase, suite, tasks, **kwargs):
            del kwargs
            seen.append((phase, suite.name, [task.id for task in tasks]))
            return [RolloutResult(id=phase, hard=0, soft=0.0)]

    monkeypatch.setattr(orchestrator, "run_reflect", lambda *args, **kwargs: [])

    cfg = orchestrator.OptimizationConfig(
        skill_name="device-operator",
        skill_path=skill_path,
        suite=_suite(tmp_path, "train_suite"),
        train_suite=_suite(tmp_path, "train_suite"),
        selection_suite=_suite(tmp_path, "validation_suite"),
        train_tasks=[_task("train_task")],
        selection_tasks=[_task("validation_task")],
        budget=1,
        run_id="run-001",
        artifact_root=tmp_path / "runs" / "skillopt",
        rollout_backend=SuiteBackend(),
    )

    orchestrator.optimize_skill(cfg)

    assert seen == [
        ("baseline_selection", "validation_suite", ["validation_task"]),
        ("step_01_train", "train_suite", ["train_task"]),
    ]


def test_optimize_skill_records_phase_scores_and_stop_reason(monkeypatch, tmp_path):
    skill_path = tmp_path / "SKILL.md"
    skill_path.write_text("base", encoding="utf-8")

    class SummaryBackend:
        def close(self):
            pass

        def run_rollout(self, *, phase, **kwargs):
            del kwargs
            if phase == "baseline_selection":
                return [
                    RolloutResult(id="selection_pass", hard=1, soft=1.0),
                    RolloutResult(id="selection_fail", hard=0, soft=0.5),
                ]
            return [
                RolloutResult(id="train_pass", hard=1, soft=1.0),
                RolloutResult(id="train_fail", hard=0, soft=0.0),
            ]

    monkeypatch.setattr(orchestrator, "run_reflect", lambda *args, **kwargs: [])

    cfg = orchestrator.OptimizationConfig(
        skill_name="device-operator",
        skill_path=skill_path,
        suite=_suite(tmp_path, "train_suite"),
        train_suite=_suite(tmp_path, "train_suite"),
        selection_suite=_suite(tmp_path, "validation_suite"),
        train_tasks=[_task("train_pass"), _task("train_fail")],
        selection_tasks=[_task("selection_pass"), _task("selection_fail")],
        budget=1,
        run_id="run-001",
        artifact_root=tmp_path / "runs" / "skillopt",
        rollout_backend=SummaryBackend(),
    )

    result = orchestrator.optimize_skill(cfg)

    assert result.initial_score == 0.5
    assert result.best_score == 0.5
    assert result.stop_reason == "step 1: no patches produced by reflect"
    assert result.phase_summaries[0].phase == "baseline_selection"
    assert result.phase_summaries[0].kind == "verification"
    assert result.phase_summaries[0].score.hard == 0.5
    assert result.phase_summaries[0].score.soft == 0.75
    assert result.phase_summaries[0].score.n_passed == 1
    assert result.phase_summaries[1].phase == "step_01_train"
    assert result.phase_summaries[1].kind == "train"
    assert result.phase_summaries[1].score.hard == 0.5
    assert result.phase_summaries[1].score.soft == 0.5
    assert result.steps == []
