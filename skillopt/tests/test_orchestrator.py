"""Unit tests for SkillOpt orchestrator."""
import json
from pathlib import Path

import pytest

from runner.suite import HardAssertions, RubricItem, Suite, TaskSpec
from skillopt import orchestrator
from skillopt.optimizer_client import OptimizerError
from skillopt.types import Edit, Patch, RawPatch, RolloutResult


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


def test_optimize_skill_rejects_lint_invalid_candidate_before_selection(monkeypatch, tmp_path):
    backend = CapturingBackend()
    invalid_candidate = """
## Failed Attempt Handling

After a failed attempt:

1. Observe with `screenshot`.
2. Compare expected vs observed result.
3. Never repeat the exact same failed action more than once. After 2 total failed attempts on the same goal, stop and report the blocker to the user immediately instead of continuing to attempt untested actions that waste turns.
4. Change one variable at a time: target location, gesture type, coordinate space, navigation path, or input method.
5. After 2 failed attempts on the same goal, choose a different strategy.
6. After 3 failed attempts total, summarize what was tried and ask the user or switch to diagnosis.
"""
    monkeypatch.setattr(
        orchestrator,
        "run_reflect",
        lambda *args, **kwargs: [RawPatch(patch=Patch(edits=[Edit(op="append", content="conflict")]))],
    )
    monkeypatch.setattr(
        orchestrator,
        "aggregate",
        lambda raw_patches, edit_budget: Patch(edits=[Edit(op="append", content="conflict")]),
    )
    monkeypatch.setattr(
        orchestrator,
        "apply_patch_with_report",
        lambda current, patch: (invalid_candidate, []),
    )

    result = orchestrator.optimize_skill(_backend_config(tmp_path, backend))

    assert result.accepted_count == 0
    assert result.rejected_count == 1
    assert result.steps[0].accepted is False
    assert "skill lint failed" in result.steps[0].reason
    assert [call[0] for call in backend.calls] == ["baseline_selection", "step_01_train"]
    lint_artifact = tmp_path / "runs" / "skillopt" / "run-001" / "step_01" / "candidate_lint.json"
    assert "conflicting_failed_attempt_thresholds" in lint_artifact.read_text(encoding="utf-8")


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


def test_optimize_skill_writes_skillopt_phase_records(monkeypatch, tmp_path):
    skill_path = tmp_path / "SKILL.md"
    skill_path.write_text("base", encoding="utf-8")

    class PhaseBackend:
        def close(self):
            pass

        def run_rollout(self, *, phase, run_root, **kwargs):
            del kwargs
            if phase == "baseline_selection":
                return [
                    RolloutResult(
                        id="validation_task",
                        hard=0,
                        soft=0.25,
                        n_turns=3,
                        fail_reason="No tool calls.",
                        artifact_dir=str(run_root / "benchmark" / "run-001-baseline_selection" / "tasks" / "validation_task"),
                        extras={
                            "benchmark_run_id": "run-001-baseline_selection",
                            "benchmark_report": str(run_root / "benchmark" / "run-001-baseline_selection" / "report.html"),
                            "benchmark_status": "failed",
                        },
                    )
                ]
            return [
                RolloutResult(
                    id="train_task",
                    hard=1,
                    soft=1.0,
                    n_turns=2,
                    artifact_dir=str(run_root / "benchmark" / "run-001-step_01_train" / "tasks" / "train_task"),
                    extras={
                        "benchmark_run_id": "run-001-step_01_train",
                        "benchmark_report": str(run_root / "benchmark" / "run-001-step_01_train" / "report.html"),
                        "benchmark_status": "passed",
                    },
                )
            ]

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
        rollout_backend=PhaseBackend(),
    )

    orchestrator.optimize_skill(cfg)

    run_root = tmp_path / "runs" / "skillopt" / "run-001"
    baseline = json.loads((run_root / "phases" / "baseline_selection.json").read_text(encoding="utf-8"))
    train = json.loads((run_root / "phases" / "step_01_train.json").read_text(encoding="utf-8"))
    assert baseline["schema"] == "skillopt.phase.v1"
    assert baseline["phase"] == "baseline_selection"
    assert baseline["kind"] == "verification"
    assert baseline["suite_name"] == "validation_suite"
    assert baseline["status"] == "completed"
    assert baseline["counts"]["total"] == 1
    assert baseline["counts"]["failed"] == 1
    assert baseline["tasks"] == [{
        "id": "validation_task",
        "category": "single_step",
        "status": "failed",
        "hard": 0,
        "soft": 0.25,
        "turns": 3,
        "reason": "No tool calls.",
        "artifact_dir": "benchmark/run-001-baseline_selection/tasks/validation_task",
        "raw_report": "benchmark/run-001-baseline_selection/report.html",
    }]
    assert train["counts"]["passed"] == 1


def test_optimize_skill_marks_phase_record_failed_when_rollout_raises(tmp_path):
    backend = CapturingBackend(fail=True)
    cfg = _backend_config(tmp_path, backend)

    with pytest.raises(RuntimeError, match="rollout failed"):
        orchestrator.optimize_skill(cfg)

    phase = json.loads((tmp_path / "runs" / "skillopt" / "run-001" / "phases" / "baseline_selection.json").read_text(encoding="utf-8"))
    assert phase["status"] == "failed"
    assert phase["error"] == "rollout failed"


def test_optimize_skill_failed_phase_records_partial_rollout_results(tmp_path):
    class RolloutError(RuntimeError):
        pass

    class PartialBackend:
        def close(self):
            pass

        def run_rollout(self, *, run_root, phase, **kwargs):
            del kwargs
            exc = RolloutError("environment setup failed")
            exc.rollouts = [
                RolloutResult(
                    id="train_pass",
                    hard=1,
                    soft=1.0,
                    n_turns=2,
                    artifact_dir=str(run_root / "benchmark" / "run-001-step_01_train" / "tasks" / "train_pass"),
                    extras={
                        "benchmark_status": "passed",
                        "benchmark_report": str(run_root / "benchmark" / "run-001-step_01_train" / "report.html"),
                    },
                ),
                RolloutResult(
                    id="train_skipped",
                    hard=0,
                    soft=0.0,
                    n_turns=0,
                    fail_reason="setup endpoint failed HTTP 504",
                    artifact_dir=str(run_root / "benchmark" / "run-001-step_01_train" / "tasks" / "train_skipped"),
                    extras={
                        "benchmark_status": "skipped",
                        "benchmark_report": str(run_root / "benchmark" / "run-001-step_01_train" / "report.html"),
                    },
                ),
            ]
            raise exc

    skill_path = tmp_path / "SKILL.md"
    skill_path.write_text("base", encoding="utf-8")
    cfg = orchestrator.OptimizationConfig(
        skill_name="device-operator",
        skill_path=skill_path,
        suite=_suite(tmp_path, "train_suite"),
        train_suite=_suite(tmp_path, "train_suite"),
        selection_suite=_suite(tmp_path, "validation_suite"),
        train_tasks=[_task("train_pass"), _task("train_skipped")],
        selection_tasks=[_task("selection")],
        budget=1,
        run_id="run-001",
        artifact_root=tmp_path / "runs" / "skillopt",
        rollout_backend=PartialBackend(),
    )

    with pytest.raises(RolloutError, match="environment setup failed"):
        orchestrator._run_rollout_phase(
            cfg.rollout_backend,
            cfg,
            suite=cfg.train_suite,
            tasks=cfg.train_tasks,
            skill_text="base",
            phase="step_01_train",
            kind="train",
            run_root=cfg.artifact_root / cfg.run_id,
        )

    phase = json.loads((tmp_path / "runs" / "skillopt" / "run-001" / "phases" / "step_01_train.json").read_text(encoding="utf-8"))
    assert phase["status"] == "failed"
    assert phase["counts"]["passed"] == 1
    assert phase["counts"]["skipped"] == 1
    assert phase["tasks"][0]["status"] == "passed"
    assert phase["tasks"][1]["status"] == "skipped"
    assert phase["tasks"][1]["reason"] == "setup endpoint failed HTTP 504"


def test_optimize_skill_writes_reflect_error_artifact(monkeypatch, tmp_path):
    skill_path = tmp_path / "SKILL.md"
    skill_path.write_text("base", encoding="utf-8")

    class SummaryBackend:
        def close(self):
            pass

        def run_rollout(self, *, phase, **kwargs):
            del kwargs
            return [RolloutResult(id=phase, hard=0, soft=0.0, artifact_dir=str(tmp_path / phase))]

    def fail_reflect(*args, **kwargs):
        raise OptimizerError("missing env var OPENROUTER_API_KEY")

    monkeypatch.setattr(orchestrator, "run_reflect", fail_reflect)

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
        rollout_backend=SummaryBackend(),
    )

    result = orchestrator.optimize_skill(cfg)

    assert result.stop_reason == "step 1: reflect failed: missing env var OPENROUTER_API_KEY"
    artifact = tmp_path / "runs" / "skillopt" / "run-001" / "step_01" / "reflect_error.json"
    assert artifact.exists()
    assert "missing env var OPENROUTER_API_KEY" in artifact.read_text(encoding="utf-8")
