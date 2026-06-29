from pathlib import Path

import pytest

from skillopt import reflect
from skillopt.optimizer_client import OptimizerConfig, OptimizerError
from skillopt.types import RolloutResult


def test_run_reflect_propagates_optimizer_errors(monkeypatch, tmp_path: Path):
    artifact = tmp_path / "task"
    artifact.mkdir()
    (artifact / "history.json").write_text('[{"type":"user","content":"do it"}]', encoding="utf-8")

    def fail_chat(*args, **kwargs):
        raise OptimizerError("missing env var OPENROUTER_API_KEY")

    monkeypatch.setattr(reflect, "chat_optimizer", fail_chat)

    with pytest.raises(OptimizerError, match="missing env var OPENROUTER_API_KEY"):
        reflect.run_reflect(
            OptimizerConfig(),
            "skill text",
            [RolloutResult(id="case", hard=0, soft=0.0, artifact_dir=str(artifact))],
            edit_budget=1,
        )


def test_run_reflect_skips_rollouts_excluded_from_reflection(monkeypatch):
    seen_failures = []

    def capture_failures(cfg, skill_content, items, edit_budget=4, rejected_context=""):
        del cfg, skill_content, edit_budget, rejected_context
        seen_failures.extend(item.id for item in items)
        return None

    monkeypatch.setattr(reflect, "run_error_analyst_minibatch", capture_failures)
    monkeypatch.setattr(reflect, "run_success_analyst_minibatch", lambda *args, **kwargs: None)

    reflect.run_reflect(
        OptimizerConfig(),
        "skill text",
        [
            RolloutResult(id="real_failure", hard=0, soft=0.0, fail_reason="button not found"),
            RolloutResult(
                id="agent_error",
                hard=0,
                soft=0.0,
                fail_reason="Agent Error: model call timed out HTTP 500",
            ),
            RolloutResult(id="success", hard=1, soft=1.0),
        ],
        edit_budget=1,
    )

    assert seen_failures == ["real_failure"]
