import json
import re
from pathlib import Path

from runner.agent_client import ToolInvokeResult
from runner.html_report import generate_report_html, upload_report


class RecordingClient:
    def __init__(self):
        self.calls = []

    def invoke_tool(self, name, args):
        self.calls.append((name, args))
        return ToolInvokeResult(output="", is_error=False, duration_ms=1)


def read_report_tasks(html: str):
    match = re.search(r"const TASKS = (.*?);\nconst rows", html, re.S)
    assert match, "report TASKS payload missing"
    return json.loads(match.group(1))


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


def test_generate_report_embeds_failed_task_error_log(tmp_path: Path):
    run_dir = tmp_path / "2026-05-28_091421"
    task_dir = run_dir / "tasks" / "task-1"
    task_dir.mkdir(parents=True)
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
                "hard_assertions": {"max_tool_calls": False},
                "metrics": {"tool_calls": 2, "wall_ms": 9},
            }
        )
        + "\n",
        encoding="utf-8",
    )
    (task_dir / "trace.json").write_text(json.dumps({"final_response": "wrong", "events": ["TraceBoom"]}), encoding="utf-8")
    (task_dir / "history.json").write_text(json.dumps([{"type": "user", "content": "Do task"}]), encoding="utf-8")
    (task_dir / "judge.json").write_text(json.dumps({"reason": "JudgeBoom"}), encoding="utf-8")

    html = generate_report_html(run_dir)
    task = read_report_tasks(html)[0]

    assert "Max Tool Calls" in task["error_log_detail"]
    assert "JudgeBoom" not in task["error_log_detail"]
    assert "evidence_detail" not in task
    assert "tasks/task-1/trace.json" in task["artifacts_detail"]
    assert "tasks/task-1/history.json" in task["artifacts_detail"]
    assert "tasks/task-1/judge.json" in task["artifacts_detail"]
    assert "<strong>Error Log</strong>" in html


def test_generate_report_accepts_relative_run_dir_with_task_artifacts(tmp_path: Path, monkeypatch):
    workspace = tmp_path / "benchmark"
    run_dir = workspace / "runs" / "2026-05-28_091421"
    task_dir = run_dir / "tasks" / "task-1"
    task_dir.mkdir(parents=True)
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
                "metrics": {"tool_calls": 2, "wall_ms": 9},
            }
        )
        + "\n",
        encoding="utf-8",
    )
    (task_dir / "trace.json").write_text(json.dumps({"final_response": "wrong"}), encoding="utf-8")
    (task_dir / "history.json").write_text(json.dumps([{"type": "user", "content": "Do task"}]), encoding="utf-8")
    (task_dir / "judge.json").write_text(json.dumps({"reason": "JudgeBoom"}), encoding="utf-8")

    monkeypatch.chdir(workspace)
    html = generate_report_html(Path("runs") / "2026-05-28_091421")
    task = read_report_tasks(html)[0]

    assert "tasks/task-1/trace.json" in task["artifacts_detail"]
    assert "tasks/task-1/judge.json" in task["artifacts_detail"]
    assert "JudgeBoom" not in task["error_log_detail"]


def test_generate_report_includes_llm_analysis_section(tmp_path: Path):
    run_dir = tmp_path / "run"
    run_dir.mkdir()
    (run_dir / "manifest.json").write_text(
        json.dumps({"run_id": "run", "totals": {"tasks": 0, "passed": 0, "failed": 0}}),
        encoding="utf-8",
    )
    (run_dir / "results.jsonl").write_text("", encoding="utf-8")
    (run_dir / "llm_analysis.md").write_text(
        "# LLM Benchmark Analysis\n\nRoot cause summary", encoding="utf-8"
    )

    html = generate_report_html(run_dir)

    assert "LLM Analysis" in html
    assert "Root cause summary" in html


def test_upload_report_uploads_analysis_artifacts(tmp_path: Path):
    run_dir = tmp_path / "run-1"
    run_dir.mkdir()
    (run_dir / "manifest.json").write_text('{"run_id":"run-1"}', encoding="utf-8")
    (run_dir / "llm_analysis.md").write_text("analysis md", encoding="utf-8")
    (run_dir / "llm_analysis.json").write_text('{"summary":"analysis"}', encoding="utf-8")
    client = RecordingClient()

    assert upload_report(client, "<html></html>", run_dir) is True
    command = client.calls[0][1]["command"]
    assert "llm_analysis.md" in command
    assert "llm_analysis.json" in command
