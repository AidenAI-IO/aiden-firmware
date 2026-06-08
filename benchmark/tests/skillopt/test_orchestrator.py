"""Unit tests for SkillOpt orchestrator."""
from contextlib import contextmanager
from pathlib import Path

from runner.suite import HardAssertions, RubricItem, Suite, TaskSpec
from runner.skillopt import orchestrator
from runner.skillopt.types import Edit, Patch, RawPatch, RolloutResult


class DummyClient:
    def __init__(self, base_url: str):
        self.base_url = base_url
        self.closed = False

    def close(self):
        self.closed = True


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


def test_optimize_skill_scopes_artifacts_by_run_id(monkeypatch, tmp_path):
    skill_path = tmp_path / "SKILL.md"
    skill_path.write_text("base", encoding="utf-8")
    run_id = "run-001"
    artifact_root = tmp_path / "runs" / "skillopt"
    seen_artifact_roots = []

    def fake_rollout_tasks(client, suite, tasks, skill_name, rollout_run_id, phase, phase_artifact_root, judge_cfg):
        seen_artifact_roots.append((phase, phase_artifact_root))
        hard = 1 if phase.endswith("_selection") and phase != "baseline_selection" else 0
        return [], [RolloutResult(id=phase, hard=hard, soft=float(hard))]

    @contextmanager
    def fake_skill_override(client, skill_path, candidate_content):
        yield

    monkeypatch.setattr(orchestrator, "AgentClient", DummyClient)
    monkeypatch.setattr(orchestrator, "_rollout_tasks", fake_rollout_tasks)
    monkeypatch.setattr(orchestrator, "run_reflect", lambda *args, **kwargs: [RawPatch(patch=Patch(edits=[Edit(op="append", content="candidate")]))])
    monkeypatch.setattr(orchestrator, "aggregate", lambda raw_patches, edit_budget: Patch(edits=[Edit(op="append", content="candidate")]))
    monkeypatch.setattr(orchestrator, "apply_patch_with_report", lambda current, patch: (current + "\ncandidate", []))
    monkeypatch.setattr(orchestrator, "with_skill_override", fake_skill_override)

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


def test_optimize_skill_closes_client_when_rollout_raises(monkeypatch, tmp_path):
    skill_path = tmp_path / "SKILL.md"
    skill_path.write_text("base", encoding="utf-8")
    clients = []

    class CapturedClient(DummyClient):
        def __init__(self, base_url: str):
            super().__init__(base_url)
            clients.append(self)

    def failing_rollout_tasks(*args, **kwargs):
        raise RuntimeError("rollout failed")

    monkeypatch.setattr(orchestrator, "AgentClient", CapturedClient)
    monkeypatch.setattr(orchestrator, "_rollout_tasks", failing_rollout_tasks)

    cfg = orchestrator.OptimizationConfig(
        skill_name="device-operator",
        skill_path=skill_path,
        suite=_suite(tmp_path),
        train_tasks=[_task("train")],
        selection_tasks=[_task("selection")],
        budget=1,
        run_id="run-001",
        artifact_root=tmp_path / "runs" / "skillopt",
    )

    import pytest
    with pytest.raises(RuntimeError, match="rollout failed"):
        orchestrator.optimize_skill(cfg)

    assert len(clients) == 1
    assert clients[0].closed


def test_optimize_skill_uses_explicit_train_and_selection_suites(monkeypatch, tmp_path):
    skill_path = tmp_path / "SKILL.md"
    skill_path.write_text("base", encoding="utf-8")
    seen = []

    def fake_rollout_tasks(client, suite, tasks, skill_name, rollout_run_id, phase, phase_artifact_root, judge_cfg):
        seen.append((phase, suite.name, [task.id for task in tasks]))
        return [], [RolloutResult(id=phase, hard=0, soft=0.0)]

    monkeypatch.setattr(orchestrator, "AgentClient", DummyClient)
    monkeypatch.setattr(orchestrator, "_rollout_tasks", fake_rollout_tasks)
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
    )

    orchestrator.optimize_skill(cfg)

    assert seen == [
        ("baseline_selection", "validation_suite", ["validation_task"]),
        ("step_01_train", "train_suite", ["train_task"]),
    ]
