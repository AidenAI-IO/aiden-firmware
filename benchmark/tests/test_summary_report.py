from pathlib import Path

from runner.models import TaskResult
from runner.report import write_summary


def test_write_summary_describes_skill_activation_or_read(tmp_path: Path):
    result = TaskResult(
        suite="phone",
        run_id="run-1",
        task_id="open_settings",
        category="single_step",
        attempt=1,
        status="passed",
        rubric=[],
        rubric_pass_count=0,
        rubric_total=0,
        metrics={
            "trace_observations": [
                {
                    "id": "skill_read_device_operator",
                    "passed": True,
                    "reason": "Task requested active skill 'device-operator' via chat skills payload.",
                }
            ]
        },
        description_for_judge="Open Settings.",
    )

    write_summary(tmp_path / "summary.md", "phone", {"run_id": "run-1"}, [result])

    text = (tmp_path / "summary.md").read_text(encoding="utf-8")
    assert "device-operator skill activation/read: 1/1 tasks" in text
    assert "device-operator skill_read:" not in text
