import json
from pathlib import Path

from runner.agent_client import ToolInvokeResult
from runner.html_report import upload_report


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
