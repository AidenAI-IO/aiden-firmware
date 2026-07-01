import json
from pathlib import Path

from runner.compare import compare_runs


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
                        {"id": "used_enter_text_in_field", "passed": True},
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
    assert "used_enter_text_in_field: 0/0 -> 1/1 (delta +1)" in output
