"""Unit tests for the Aiden→MobileGym suite adapter in run_aiden.py."""

from __future__ import annotations

import importlib.util
import json
import sys
from pathlib import Path

import pytest


REPO_ROOT = Path(__file__).resolve().parents[2]
RUN_AIDEN_PATH = REPO_ROOT / "benchmark" / "mobilegym" / "scripts" / "run_aiden.py"


@pytest.fixture
def run_aiden_module(tmp_path, monkeypatch):
    """Import run_aiden.py as a module without executing main()."""
    benchmark_root = REPO_ROOT / "benchmark"
    monkeypatch.syspath_prepend(str(benchmark_root))
    spec = importlib.util.spec_from_file_location("run_aiden", RUN_AIDEN_PATH)
    mod = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = mod
    spec.loader.exec_module(mod)
    return mod


def _write_minimal_suite(suites_dir: Path, name: str = "test_suite") -> Path:
    suite = {
        "name": name,
        "description": "test suite",
        "prompt_prefix": "PREFIX",
        "global_reset": {"tool_sequence": [{"tool": "wait_ms", "args": {"ms": 100}}]},
        "tasks": [
            {
                "id": "task_one",
                "category": "single_step",
                "description_for_judge": "judge desc",
                "prompt": "do thing",
                "rubric": [{"id": "r1", "check": "x"}],
                "hard_assertions": {"min_tool_calls": 1, "max_tool_calls": 5},
                "setup": {"tool_sequence": [{"tool": "shell", "args": {"command": "true"}}]},
            }
        ],
    }
    suite_path = suites_dir / f"{name}.json"
    suite_path.parent.mkdir(parents=True, exist_ok=True)
    suite_path.write_text(json.dumps(suite))
    return suite_path


def test_load_aiden_suite_returns_one_task_per_taskspec(
    run_aiden_module, tmp_path, monkeypatch
):
    suites_dir = tmp_path / "suites"
    suites_dir.mkdir()
    _write_minimal_suite(suites_dir, "demo")

    monkeypatch.setattr(run_aiden_module, "BENCHMARK_ROOT", tmp_path)
    tasks = run_aiden_module._load_aiden_suite_as_mobilegym_tasks("demo")

    assert len(tasks) == 1
    assert tasks[0].task_id == "demo.task_one"


def test_load_aiden_suite_prepends_prompt_prefix(
    run_aiden_module, tmp_path, monkeypatch
):
    suites_dir = tmp_path / "suites"
    suites_dir.mkdir()
    _write_minimal_suite(suites_dir, "demo")

    monkeypatch.setattr(run_aiden_module, "BENCHMARK_ROOT", tmp_path)
    tasks = run_aiden_module._load_aiden_suite_as_mobilegym_tasks("demo")

    assert tasks[0].instruction.startswith("PREFIX")
    assert "do thing" in tasks[0].instruction


def test_load_aiden_suite_preserves_metadata(
    run_aiden_module, tmp_path, monkeypatch
):
    suites_dir = tmp_path / "suites"
    suites_dir.mkdir()
    _write_minimal_suite(suites_dir, "demo")

    monkeypatch.setattr(run_aiden_module, "BENCHMARK_ROOT", tmp_path)
    tasks = run_aiden_module._load_aiden_suite_as_mobilegym_tasks("demo")

    md = tasks[0].metadata
    assert md["category"] == "single_step"
    assert md["rubric"][0]["id"] == "r1"
    assert md["hard_assertions"]["min_tool_calls"] == 1
    assert md["setup"] is not None
    assert md["global_reset"]["tool_sequence"][0]["tool"] == "wait_ms"


def test_aiden_suite_evaluate_passes_expected_answer_and_recalled_memory(run_aiden_module):
    task = run_aiden_module.MobileGymTaskAdapter(
        task_id="personamem_lt_recall_v1.personamem_music",
        instruction="Choose one option.",
        metadata={
            "expected_answer": "(c)",
            "answer_format": "option_letter",
            "expected_recalled_memory_ids": ["mem_expected"],
            "aiden_last_response": "I used the stored preference.\n<final_answer>(c)</final_answer>",
            "aiden_last_chat_history": [
                {
                    "type": "tool_result",
                    "tool_name": "recall_memory",
                    "content": json.dumps({"results": [{"id": "mem_expected"}]}),
                }
            ],
        },
    )

    result = task.evaluate(None)

    assert result.success is True
    assert result.progress == 1.0


def test_aiden_suite_evaluate_fails_when_expected_memory_was_not_recalled(run_aiden_module):
    task = run_aiden_module.MobileGymTaskAdapter(
        task_id="personamem_lt_recall_v1.personamem_music",
        instruction="Choose one option.",
        metadata={
            "expected_answer": "(c)",
            "answer_format": "option_letter",
            "expected_recalled_memory_ids": ["mem_expected"],
            "aiden_last_response": "I used a different memory.\n<final_answer>(c)</final_answer>",
            "aiden_last_chat_history": [
                {
                    "type": "tool_result",
                    "tool_name": "recall_memory",
                    "content": json.dumps({"results": [{"id": "mem_other"}]}),
                }
            ],
        },
    )

    result = task.evaluate(None)

    assert result.success is False
    assert "missing expected recalled memory ids" in result.issues[0]["reason"]


def test_load_aiden_suite_missing_raises(run_aiden_module, tmp_path, monkeypatch):
    monkeypatch.setattr(run_aiden_module, "BENCHMARK_ROOT", tmp_path)
    with pytest.raises(run_aiden_module.LauncherError, match="Aiden suite not found"):
        run_aiden_module._load_aiden_suite_as_mobilegym_tasks("does_not_exist")


def test_validate_selection_rejects_combined_flags(run_aiden_module):
    import argparse
    args = argparse.Namespace(
        task_id="x", suite=None, split=None, aiden_suite="demo",
        shard_index=0, shard_count=1,
    )
    with pytest.raises(run_aiden_module.LauncherError, match="mutually exclusive"):
        run_aiden_module._validate_selection(args)


def test_validate_selection_accepts_aiden_suite_alone(run_aiden_module):
    import argparse
    args = argparse.Namespace(
        task_id=None, suite=None, split=None, aiden_suite="demo",
        shard_index=0, shard_count=1,
    )
    # Should not raise
    run_aiden_module._validate_selection(args)


def test_runner_args_use_physical_coord_space_for_bridge_pixels(run_aiden_module):
    import argparse

    args = argparse.Namespace(
        task_id="clock.CountAlarms",
        suite=None,
        split=None,
        env_url="http://mobilegym:4173",
        headless=True,
        parallel=1,
        max_steps=30,
        quiet=True,
        runs_dir="runs",
        aiden_suite=None,
    )

    runner_args = run_aiden_module._runner_args(args)

    assert runner_args.coord_space == "physical"


def test_load_aiden_suite_rejects_path_traversal(run_aiden_module, tmp_path, monkeypatch):
    monkeypatch.setattr(run_aiden_module, "BENCHMARK_ROOT", tmp_path)
    for bad in [".", "..", "../etc/passwd", "foo/../bar", "foo//bar", "foo bar", "foo;rm"]:
        with pytest.raises(run_aiden_module.LauncherError, match="invalid suite name"):
            run_aiden_module._load_aiden_suite_as_mobilegym_tasks(bad)


def test_load_aiden_suite_allows_nested_safe_path(run_aiden_module, tmp_path, monkeypatch):
    suites_dir = tmp_path / "suites"
    _write_minimal_suite(suites_dir, "perception/perception_v1")

    monkeypatch.setattr(run_aiden_module, "BENCHMARK_ROOT", tmp_path)
    tasks = run_aiden_module._load_aiden_suite_as_mobilegym_tasks("perception/perception_v1")

    assert len(tasks) == 1
