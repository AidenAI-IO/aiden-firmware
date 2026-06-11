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


def test_load_aiden_suite_missing_raises(run_aiden_module, tmp_path, monkeypatch):
    monkeypatch.setattr(run_aiden_module, "BENCHMARK_ROOT", tmp_path)
    with pytest.raises(run_aiden_module.LauncherError, match="Aiden suite not found"):
        run_aiden_module._load_aiden_suite_as_mobilegym_tasks("does_not_exist")
