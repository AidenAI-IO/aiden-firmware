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
