import json
import pytest
from pathlib import Path

from runner.compare import build_comparison, compare_runs


def _write_results(run_dir: Path, rows: list[dict]):
    run_dir.mkdir(parents=True)
    (run_dir / "results.jsonl").write_text(
        "\n".join(json.dumps(row, sort_keys=True) for row in rows) + "\n",
        encoding="utf-8",
    )


def test_compare_runs_summarizes_before_after_quality_efficiency_and_observations(
    tmp_path: Path, capsys
):
    before = tmp_path / "before"
    after = tmp_path / "after"
    _write_results(
        before,
        [
            {
                "task_id": "open_settings",
                "attempt": 1,
                "status": "passed",
                "metrics": {
                    "tool_calls": 5,
                    "wall_ms": 1000,
                    "trace_observations": [
                        {"id": "skill_read_device_operator", "passed": True},
                        {"id": "used_old_launch_path", "passed": True},
                    ],
                },
            },
            {
                "task_id": "type_text",
                "attempt": 1,
                "status": "failed",
                "metrics": {
                    "tool_calls": 10,
                    "wall_ms": 2000,
                    "trace_observations": [
                        {"id": "skill_read_device_operator", "passed": True},
                        {"id": "used_old_text_path", "passed": True},
                    ],
                },
            },
        ],
    )
    _write_results(
        after,
        [
            {
                "task_id": "open_settings",
                "attempt": 1,
                "status": "passed",
                "metrics": {
                    "tool_calls": 3,
                    "wall_ms": 800,
                    "trace_observations": [
                        {"id": "skill_read_device_operator", "passed": True},
                        {"id": "used_old_launch_path", "passed": False},
                    ],
                },
            },
            {
                "task_id": "type_text",
                "attempt": 1,
                "status": "passed",
                "metrics": {
                    "tool_calls": 7,
                    "wall_ms": 1500,
                    "trace_observations": [
                        {"id": "skill_read_device_operator", "passed": True},
                        {"id": "used_old_text_path", "passed": False},
                        {"id": "used_enter_text", "passed": True},
                    ],
                },
            },
        ],
    )

    assert compare_runs(before, after) == 0
    output = capsys.readouterr().out

    assert "Passed: 1/2 -> 2/2 (delta +1)" in output
    assert "Tool calls median: 7.5 -> 5.0 (delta -2.5)" in output
    assert "Wall median: 1500ms -> 1150ms (delta -350ms)" in output
    assert "~ type_text#1  failed -> passed" in output
    assert "used_old_text_path: 1/1 -> 0/1 (delta -1)" in output
    assert "used_enter_text: 0/0 -> 1/1 (delta +1)" in output


def test_build_comparison_reports_rates_lifts_and_token_overhead(tmp_path: Path):
    before = tmp_path / "thinking-off"
    after = tmp_path / "thinking-on"
    rubric = [
        {"id": "step_a", "verdict": "yes", "reason": "ok"},
        {"id": "step_b", "verdict": "no", "reason": "missed"},
    ]
    _write_results(
        before,
        [
            {
                "task_id": "a", "attempt": 1, "status": "passed", "rubric": rubric,
                "metrics": {"wall_ms": 1000, "total_tokens": 100,
                            "expected_answer_match": True},
            },
            {
                "task_id": "b", "attempt": 1, "status": "failed", "rubric": rubric,
                "metrics": {"wall_ms": 2000, "total_tokens": 200,
                            "expected_answer_match": False},
            },
        ],
    )
    _write_results(
        after,
        [
            {
                "task_id": "a", "attempt": 1, "status": "passed", "rubric": [
                    {"id": "step_a", "verdict": "yes"},
                    {"id": "step_b", "verdict": "yes"},
                ],
                "metrics": {"wall_ms": 1500, "total_tokens": 300,
                            "expected_answer_match": True},
            },
            {
                "task_id": "b", "attempt": 1, "status": "passed", "rubric": [
                    {"id": "step_a", "verdict": "yes"},
                    {"id": "step_b", "verdict": "no"},
                ],
                "metrics": {"wall_ms": 2500, "total_tokens": 400,
                            "expected_answer_match": True},
            },
        ],
    )

    comparison = build_comparison(before, after)
    quality = comparison["quality"]
    efficiency = comparison["efficiency"]
    assert quality["success_rate"] == {
        "baseline": 0.5, "candidate": 1.0,
        "delta_pp": 50.0, "relative_pct": 100.0,
    }
    assert quality["rubric_completion"]["baseline"] == 0.5
    assert quality["rubric_completion"]["candidate"] == 0.75
    assert quality["answer_accuracy"]["delta_pp"] == 50.0
    assert efficiency["wall_ms_median"]["baseline"] == 1500.0
    assert efficiency["wall_ms_median"]["candidate"] == 2000.0
    assert efficiency["wall_ms_median"]["relative_pct"] == pytest.approx(100.0 / 3.0)
    assert efficiency["total_tokens_median"]["candidate"] == 350.0
    assert comparison["task_flips"] == {"common": 2, "improved": 1, "regressed": 0}
