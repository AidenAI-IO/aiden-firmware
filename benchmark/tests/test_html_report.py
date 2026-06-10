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
