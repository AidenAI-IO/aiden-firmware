import base64
import json
from pathlib import Path

from runner.agent_client import ToolInvokeResult
from runner.html_report import generate_report_html, upload_report


class RecordingClient:
    def __init__(self):
        self.calls = []

    def invoke_tool(self, name, args):
        self.calls.append((name, args))
        return ToolInvokeResult(output="", is_error=False, duration_ms=1)


def test_upload_report_uploads_run_artifacts_for_benchmark_page(tmp_path: Path):
    run_dir = tmp_path / "2026-05-28_091421"
    run_dir.mkdir()
    (run_dir / "manifest.json").write_text(
        json.dumps({"run_id": "2026-05-28_091421"}), encoding="utf-8"
    )
    client = RecordingClient()

    assert upload_report(client, "<html>report</html>", run_dir=run_dir) is True

    command = client.calls[0][1]["command"]
    assert "/userdata/agent/benchmark/runs/2026-05-28_091421" in command
    assert "/userdata/agent/benchmark/runs/2026-05-28_091421/report.html" in command
    assert "/userdata/agent/benchmark/runs/2026-05-28_091421/manifest.json" in command


def test_upload_report_returns_false_when_manifest_is_missing(tmp_path: Path):
    run_dir = tmp_path / "2026-05-28_091421"
    run_dir.mkdir()
    client = RecordingClient()

    assert upload_report(client, "<html>report</html>", run_dir=run_dir) is False


def test_upload_report_returns_false_when_manifest_is_invalid(tmp_path: Path):
    run_dir = tmp_path / "2026-05-28_091421"
    run_dir.mkdir()
    (run_dir / "manifest.json").write_text("not json", encoding="utf-8")
    client = RecordingClient()

    assert upload_report(client, "<html>report</html>", run_dir=run_dir) is False


def test_generate_report_marks_timeout_as_fail_and_escapes_drawer_chips(tmp_path: Path):
    run_dir = tmp_path / "2026-05-28_091421"
    run_dir.mkdir()
    (run_dir / "manifest.json").write_text(
        json.dumps(
            {
                "run_id": "2026-05-28_091421",
                "suite_path": "suite.json",
                "totals": {"tasks": 1, "passed": 0, "failed": 0, "skipped": 0, "timeout": 1},
            }
        ),
        encoding="utf-8",
    )
    (run_dir / "results.jsonl").write_text(
        json.dumps(
            {
                "task_id": "task-1",
                "category": "<script>alert(1)</script>",
                "status": "timeout",
                "rubric_pass_count": 0,
                "rubric_total": 1,
                "metrics": {"tool_calls": "<b>2</b>", "wall_ms": "<i>9</i>"},
            }
        )
        + "\n",
        encoding="utf-8",
    )

    html = generate_report_html(run_dir)

    assert '<span class="badge fail">Timeout</span>' in html
    assert "'<span class=\"chip\">' + esc(t.category)" in html
    assert "'<span class=\"chip\">' + esc(t.status)" in html
    assert "esc(String(t.tool_calls_count))" in html
    assert "esc(String(t.wall_ms))" in html


def test_generate_report_includes_tool_hard_assertion_failures(tmp_path: Path):
    run_dir = tmp_path / "2026-05-28_091421"
    run_dir.mkdir()
    (run_dir / "manifest.json").write_text(
        json.dumps(
            {
                "run_id": "2026-05-28_091421",
                "suite_path": "suite.json",
                "totals": {"tasks": 1, "passed": 0, "failed": 1, "skipped": 0},
            }
        ),
        encoding="utf-8",
    )
    (run_dir / "results.jsonl").write_text(
        json.dumps(
            {
                "task_id": "task-1",
                "category": "multi_step",
                "status": "failed",
                "rubric_pass_count": 0,
                "rubric_total": 1,
                "hard_assertions": {
                    "required_tools": False,
                    "forbidden_tools": False,
                },
                "metrics": {"tool_calls": 2, "wall_ms": 9},
            }
        )
        + "\n",
        encoding="utf-8",
    )

    html = generate_report_html(run_dir)

    assert "Required Tools" in html
    assert "Forbidden Tools" in html


def test_generate_report_includes_full_trace_with_screenshots(tmp_path: Path):
    run_dir = tmp_path / "2026-05-28_091421"
    task_dir = run_dir / "tasks" / "task-1"
    steps_dir = task_dir / "steps"
    steps_dir.mkdir(parents=True)
    pre = b"\x89PNG\r\n\x1a\npre-shot"
    post = b"\xff\xd8post-shot"
    step = b"\xff\xd8step-shot"
    (task_dir / "pre.jpg").write_bytes(pre)
    (task_dir / "post.jpg").write_bytes(post)
    (steps_dir / "step_01_screenshot.jpg").write_bytes(step)
    image_payload = base64.b64encode(b"history-shot").decode("ascii")
    (task_dir / "history.json").write_text(
        json.dumps(
            [
                {"type": "user", "content": "open settings"},
                {"type": "tool_call", "tool_name": "screenshot", "tool_input": "{}"},
                {
                    "type": "tool_result",
                    "tool_name": "screenshot",
                    "content": json.dumps({"data": image_payload, "width": 10, "height": 20, "format": "jpeg"}),
                },
                {"type": "assistant", "content": "done"},
            ]
        ),
        encoding="utf-8",
    )
    (task_dir / "trace.json").write_text(
        json.dumps(
            {
                "tool_calls": [{"step": 1, "tool": "screenshot", "input": {}, "has_screenshot": True}],
                "final_response": "done",
                "total_tool_calls": 1,
            }
        ),
        encoding="utf-8",
    )
    (run_dir / "manifest.json").write_text(
        json.dumps(
            {
                "run_id": "2026-05-28_091421",
                "suite_path": "suite.json",
                "totals": {"tasks": 1, "passed": 1, "failed": 0, "skipped": 0},
            }
        ),
        encoding="utf-8",
    )
    (run_dir / "results.jsonl").write_text(
        json.dumps(
            {
                "task_id": "task-1",
                "category": "diagnostic",
                "status": "passed",
                "rubric_pass_count": 1,
                "rubric_total": 1,
                "rubric": [{"id": "ok", "reason": "looks correct", "verdict": "yes"}],
                "metrics": {"tool_calls": 1, "wall_ms": 9, "screenshots_taken": 1},
            }
        )
        + "\n",
        encoding="utf-8",
    )

    html = generate_report_html(run_dir)

    assert "View full trace" in html
    assert "Pre screenshot" in html
    assert "Post screenshot" in html
    assert "Tool call #1: screenshot" in html
    assert "<base64 image data" in html
    assert f"data:image/png;base64,{base64.b64encode(pre).decode('ascii')}" in html
    assert f"data:image/jpeg;base64,{base64.b64encode(post).decode('ascii')}" in html
    assert f"data:image/jpeg;base64,{base64.b64encode(step).decode('ascii')}" in html
    assert image_payload not in html
