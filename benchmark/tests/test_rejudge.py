import json
from pathlib import Path

from runner import rejudge
from runner.models import RubricVerdict


def test_rejudge_uses_latest_legacy_step_screenshot_when_post_missing(tmp_path: Path, monkeypatch):
    run_dir = tmp_path / "run"
    attempt_dir = run_dir / "tasks" / "task-1"
    steps_dir = attempt_dir / "steps"
    steps_dir.mkdir(parents=True)
    (attempt_dir / "pre.jpg").write_bytes(b"\xff\xd8pre")
    (steps_dir / "001.jpg").write_bytes(b"\xff\xd8old")
    latest = steps_dir / "002.jpg"
    latest.write_bytes(b"\xff\xd8latest")
    (attempt_dir / "trace.json").write_text(json.dumps({"final_response": "done"}), encoding="utf-8")
    (run_dir / "results.jsonl").write_text(
        json.dumps(
            {
                "task_id": "task-1",
                "attempt": 1,
                "description_for_judge": "judge",
                "rubric_spec": [{"id": "ok", "check": "ok"}],
                "rubric_total": 1,
                "metrics": {},
            }
        )
        + "\n",
        encoding="utf-8",
    )
    captured = {}

    class Verdict:
        verdicts = [RubricVerdict(id="ok", verdict="yes", reason="ok")]

    def fake_judge_task(**kwargs):
        captured["post_screenshot"] = kwargs["post_screenshot"]
        return Verdict()

    monkeypatch.setattr(rejudge, "judge_task", fake_judge_task)

    assert rejudge.rejudge_run(run_dir, "judge-model", "https://judge.example/v1") == 0

    assert captured["post_screenshot"] == latest
    rows = [json.loads(line) for line in (run_dir / "results.rejudged.jsonl").read_text(encoding="utf-8").splitlines()]
    assert rows[0]["status"] == "passed"
