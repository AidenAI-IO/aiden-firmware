import json
from pathlib import Path

import pytest

import runner.runtask as runtask_mod
from runner.judge import JudgeConfig, JudgeOutput
from runner.models import RubricVerdict
from runner.runtask import evaluate_task_history
from runner.suite import HardAssertions, RubricItem, Suite, TaskSpec


FIXTURE_PATH = (
    Path(__file__).parent / "fixtures" / "personamem_204453_compressed_recall.json"
)
CASES = json.loads(FIXTURE_PATH.read_text(encoding="utf-8"))
EXPECTED_HISTORY_SHAPES = {
    "personamem_music_preference_evolution": (4, ["recall_memory"]),
    "personamem_music_creative_getaway": (4, ["recall_memory"]),
    "personamem_music_new_expression": (4, ["recall_memory"]),
    "personamem_food_fusion_cuisine": (4, ["recall_memory"]),
    "personamem_therapy_games_dislike": (
        8,
        ["recall_memory", "recall_memory", "shell"],
    ),
    "personamem_writing_style_discrimination": (4, ["recall_memory"]),
    "personamem_study_consultation_generalization": (4, ["recall_memory"]),
}


def _suite_and_task(case: dict, tmp_path: Path) -> tuple[Suite, TaskSpec]:
    suite = Suite(
        name="personamem_lt_recall_v1",
        global_reset={},
        tasks=[],
        sha256="fixture",
        source_path=tmp_path / "suite.json",
    )
    task = TaskSpec(
        id=case["task_id"],
        category="memory",
        description_for_judge="Use the recalled memory to answer the user.",
        prompt="fixture",
        rubric=[RubricItem(id="uses_memory", check="Uses the recalled memory.")],
        hard_assertions=HardAssertions(min_tool_calls=1, max_tool_calls=50),
        expected_recalled_memory_ids=case["expected_memory_ids"],
    )
    return suite, task


@pytest.mark.parametrize("case", CASES, ids=lambda case: case["task_id"])
def test_204453_compressed_personamem_failures_use_episode_evidence(
    case,
    tmp_path: Path,
    monkeypatch,
):
    suite, task = _suite_and_task(case, tmp_path)
    artifact_dir = tmp_path / case["task_id"]
    judge_calls = []
    history = case["history"]

    expected_message_count, expected_tool_names = EXPECTED_HISTORY_SHAPES[
        case["task_id"]
    ]
    assert len(history) == expected_message_count
    assert history[0]["type"] == "user"
    assert history[-1]["type"] == "assistant"
    assert history[-1]["content"]
    tool_calls = [item for item in history if item["type"] == "tool_call"]
    assert [item["tool_name"] for item in tool_calls] == expected_tool_names
    assert all(
        item["tool_input"] != "{}"
        for item in tool_calls
        if item["tool_name"] == "recall_memory"
    )

    def fake_judge_task(**kwargs):
        judge_calls.append(kwargs)
        return JudgeOutput(
            verdicts=[RubricVerdict(id="uses_memory", verdict="yes", reason="used")],
            overall_notes="",
            cache_key="fixture",
            raw_response="{}",
        )

    monkeypatch.setattr(runtask_mod, "judge_task", fake_judge_task)

    result = evaluate_task_history(
        suite=suite,
        task=task,
        history=history,
        episode=case["episode"],
        attempt=1,
        artifact_dir=artifact_dir,
        judge_cfg=JudgeConfig(),
        judge_cache_dir=None,
        run_id="full-opus5-9a2ff3f3-20260814-204453-personamem_lt_recall_v1",
        timed_out=False,
    )

    assert result.status == "passed"
    assert len(judge_calls) == 1
    memory_failures = [
        failure
        for failure in result.hard_assertion_failures
        if failure.id == "expected_recalled_memory"
    ]
    assert memory_failures == []
    assert all(
        "Recalled: none" not in failure.actual
        for failure in result.hard_assertion_failures
    )
    assert (
        result.metrics["recalled_memory_ids"]
        == case["episode"]["retrieved_memory_refs"]
    )
    assert result.metrics["memory_recall_evidence_source"] == "episode"
    assert result.metrics["expected_recalled_memory_match"] is True

    trace = json.loads((artifact_dir / "trace.json").read_text(encoding="utf-8"))
    assert trace["recalled_memory_ids"] == case["episode"]["retrieved_memory_refs"]
    assert trace["memory_recall_evidence_source"] == "episode"
    assert trace["expected_recalled_memory_match"] is True
